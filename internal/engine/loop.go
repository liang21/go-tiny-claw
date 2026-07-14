package engine

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	ctxpkg "github.com/liang21/go-tiny-claw/internal/context"
	"github.com/liang21/go-tiny-claw/internal/observability"
	"github.com/liang21/go-tiny-claw/internal/provider"
	"github.com/liang21/go-tiny-claw/internal/schema"
	"github.com/liang21/go-tiny-claw/internal/tools"
)

type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	EnableThinking bool
	PlanMode       bool
	compactor      *ctxpkg.Compactor // 【新增】压缩器实例
	recovery       *ctxpkg.RecoveryManager
	injector       *ReminderInject
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, enableThinking bool, planMode bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		EnableThinking: enableThinking,
		PlanMode:       planMode,
		compactor:      ctxpkg.NewCompactor(20000, 6),
		recovery:       ctxpkg.NewRecoveryManager(),
		injector:       NewReminderInject(),
	}
}

func (e *AgentEngine) Run(ctx context.Context, session *ctxpkg.Session, reporter Reporter) error {
	log.Printf("[Engine] 唤醒会话 [%s]，锁定工作区: %s\n", session.ID, session.WorkDir)
	// 【埋点 1】：开启 Root Span，记录整个任务的生命周期
	ctx, rootSpan := observability.StartSpan(ctx, "Agent.Run")
	rootSpan.AddAttribute("session_id", session.ID)
	rootSpan.AddAttribute("work_dir", session.WorkDir)

	// defer 保证在引擎退出时，无论成功失败，都能结束根 Span 并导出 Trace 报告
	defer func() {
		rootSpan.EndSpan()
		_ = observability.ExportTraceToFile(rootSpan, session.WorkDir, session.ID)
		log.Printf("📊 [Tracing] 本次任务的执行回放链路已保存至工作区的 .claw/traces 目录下\n")
	}()

	composer := ctxpkg.NewPromptComposer(session.WorkDir, e.PlanMode)
	systemMsg := composer.Build()
	turnCount := 0

	for {
		turnCount++
		// 【埋点 2】：记录单次 Turn 循环
		turnCtx, turnSpan := observability.StartSpan(ctx, fmt.Sprintf("Turn-%d", turnCount))
		defer turnSpan.EndSpan()

		availableTools := e.registry.GetAvailableTools()

		// 1. 从 Session 提取出近期的 Working Memory (例如最近 20 条，给压缩器留下充足的判断空间)
		workingMemory := session.GetWorkingMemory(20)

		var contextHistory []schema.Message
		contextHistory = append(contextHistory, systemMsg)
		contextHistory = append(contextHistory, workingMemory...)

		// 2. 【核心注入点】: 在向 Provider 发起推理前，过一遍内存压缩器！
		// 无论你带出了多少上下文，如果字符总数超标，早期日志将被掩码化，超大日志将被掐头去尾
		compactedContext := e.compactor.Compact(contextHistory)

		// 3. 后续的 Provider.Generate 全面使用被保护过的新鲜上下文 (compactedContext)

		var currentTurnThinkingContent string
		// ================= Phase 1: Thinking =================
		if e.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(ctx)
			}
			// 【埋点 3】：记录 Thinking 调用
			thinkCtx, thinkSpan := observability.StartSpan(turnCtx, "LLM.Thinking")
			thinkResp, err := e.provider.Generate(thinkCtx, compactedContext, nil)
			thinkSpan.EndSpan()
			if err != nil {
				return fmt.Errorf("Thinking 阶段失败: %w", err)
			}
			if thinkResp.Content != "" {
				session.Append(*thinkResp)
				compactedContext = append(compactedContext, *thinkResp)
			}
		}

		// ================= Phase 2: Action =================
		// 【埋点 4】：记录 Action 调用
		actCtx, actSpan := observability.StartSpan(turnCtx, "LLM.Action")

		actionResp, err := e.provider.Generate(actCtx, compactedContext, availableTools)
		actSpan.EndSpan()
		if err != nil {
			return fmt.Errorf("Action 阶段失败: %w", err)
		}

		finalAssistantMsg := schema.Message{
			Role:      schema.RoleAssistant,
			Content:   strings.TrimSpace(currentTurnThinkingContent + "\n" + actionResp.Content),
			ToolCalls: actionResp.ToolCalls,
		}

		session.Append(finalAssistantMsg)
		compactedContext = append(compactedContext, *actionResp)

		if actionResp.Content != "" && reporter != nil {
			reporter.OnMessage(ctx, actionResp.Content)
		}

		// ... (执行工具与并发逻辑，与上一讲完全一致) ...
		if len(actionResp.ToolCalls) == 0 {
			turnSpan.EndSpan()
			break
		}

		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
		var wg sync.WaitGroup

		var lastToolCall schema.ToolCall
		var lastToolResult schema.ToolResult

		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1)
			go func(idx int, call schema.ToolCall) {
				defer wg.Done()
				if reporter != nil {
					reporter.OnToolCall(ctx, call.Name, string(call.Arguments))
				}

				result := e.registry.Execute(turnCtx, call)

				finalOutput := result.Output
				if result.IsError {
					finalOutput = e.recovery.AnalyzeAndInject(call.Name, result.Output)
					log.Printf(" -> [Go-%d] ❌ 注入救援指南: %s\n", idx, finalOutput)
				} else {
					log.Printf(" -> [Go-%d] ✅ 工具执行成功 (返回 %d 字节)\n", idx, len(result.Output))
				}
				if reporter != nil {
					displayOutput := result.Output
					if len(displayOutput) > 200 {
						displayOutput = displayOutput[:200] + "... (已截断)"
					}
					reporter.OnToolResult(ctx, call.Name, displayOutput, result.IsError)
				}
				observationMsgs[idx] = schema.Message{
					Role:       schema.RoleUser,
					Content:    result.Output,
					ToolCallID: call.ID,
				}
			}(i, toolCall)
		}
		wg.Wait()
		turnSpan.EndSpan()
		// 将全量观测结果持久化到 Session 中
		session.Append(observationMsgs...)
		reminderMsg := e.injector.CheckAndInject(lastToolCall, lastToolResult)
		if reminderMsg != nil {
			session.Append(*reminderMsg)
		}
	}

	return nil
}

func (e *AgentEngine) RunSub(ctx context.Context, taskPrompt string, readOnlyRegistry tools.Registry, reporter any) (string, error) {
	contextHistory := []schema.Message{
		{
			Role: schema.RoleSystem,
			Content: `你是一个专门负责深度探索的探路者 (Explorer Subagent)。
				你的任务是根据主架构师的指令，在当前工作区内仔细阅读代码、查阅日志，搜集足够的信息。
				【核心纪律】
					1. 你必须、且只能依靠内置工具（如 bash 的 find/grep，或 read_file）去寻找答案。绝对不允许凭空捏造或猜测！
					2. 如果你没有找到确切的答案，你必须继续使用工具深入搜索。
					3. 当且仅当你找到了确切的线索后，停止调用工具，直接输出一段纯文本作为你的终极汇报。主架构师会根据你的汇报来做下一步决策。`,
		},
		{
			Role:    schema.RoleUser,
			Content: taskPrompt,
		},
	}
	// 限制子智能体最多只能跑 10 个 Turn，防止它自己卡死
	const maxSubTurns = 10
	turnCount := 0
	for {
		turnCount++
		if turnCount > maxSubTurns {
			return "", fmt.Errorf("子智能体探索过于深入，超过 %d 轮被强制召回，请主 Agent 给它更明确的指令", maxSubTurns)
		}
		// 【驾驭底线】：子智能体仅能获取传入的只读工具注册表
		availableTools := readOnlyRegistry.GetAvailableTools()
		compactedContext := e.compactor.Compact(contextHistory)
		// 子任务要求急速响应，强制关闭主体的慢思考，直接预测行动
		actionResp, err := e.provider.Generate(ctx, compactedContext, availableTools)
		if err != nil {
			return "", fmt.Errorf("子智能体探索失败: %w", err)
		}
		contextHistory = append(contextHistory, *actionResp)
		//	[核心退出条件]:子智能体一旦不调用工具了,说明它做好了总结汇报
		if len(actionResp.ToolCalls) == 0 {
			// 直接将它的这段汇报内容剥离出来返回给上层
			return actionResp.Content, nil
		}
		// 执行只读工具的并发循环
		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
		var wg sync.WaitGroup
		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1)
			go func(idx int, call schema.ToolCall) {
				defer wg.Done()
				// 【可视化的关键】：让终端用户看到 Subagent 正在干嘛
				var r Reporter
				if reporter != nil {
					r = reporter.(Reporter)
					r.OnToolCall(ctx, fmt.Sprintf("[Subagent] %s", call.Name), string(call.Arguments))
				}
				result := readOnlyRegistry.Execute(ctx, call)
				finalOutput := result.Output
				if result.IsError {
					finalOutput = e.recovery.AnalyzeAndInject(call.Name, result.Output)
				}
				if reporter != nil {
					displayOutput := finalOutput
					if len(displayOutput) > 200 {
						displayOutput = displayOutput[:200] + "... (已截断)"
					}
					r.OnToolResult(ctx, fmt.Sprintf("[Subagent] %s", call.Name), displayOutput, result.IsError)
				}
				observationMsgs[idx] = schema.Message{
					Role:       schema.RoleUser,
					Content:    finalOutput,
					ToolCallID: call.ID,
				}
			}(i, toolCall)
		}
		wg.Wait()
		contextHistory = append(contextHistory, observationMsgs...)
	}
}
