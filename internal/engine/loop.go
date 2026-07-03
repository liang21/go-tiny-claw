package engine

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/liang21/go-tiny-claw/internal/provider"
	"github.com/liang21/go-tiny-claw/internal/schema"
	"github.com/liang21/go-tiny-claw/internal/tools"
)

type AgentEngine struct {
	provider provider.LLMProvider
	registry tools.Registry

	WorkDir        string
	EnableThinking bool //【新增】慢思考模式开关
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		WorkDir:        workDir,
		EnableThinking: enableThinking,
	}
}

func (e *AgentEngine) Run(ctx context.Context, userPrompt string) error {
	log.Printf("[Engine] 引擎启动，锁定工作区: %s\n", e.WorkDir)
	log.Printf("[Engine] 慢思考模式 (Thinking Phase): %v\n", e.EnableThinking)

	contextHistory := []schema.Message{
		{
			Role:    schema.RoleSystem,
			Content: "You are go-tiny-claw, an expert coding assistant. You have full access to tools in the workspace.",
		},
		{
			Role:    schema.RoleUser,
			Content: userPrompt,
		},
	}

	turnCount := 0

	for {
		turnCount++
		log.Printf("\n========== [Turn %d] 开始 ==========\n", turnCount)

		availableTools := e.registry.GetAvailableTools()

		// ================= Phase 1: Thinking =================
		if e.EnableThinking {
			log.Println("[Engine][Phase 1] 剥夺工具访问权，强制进入慢思考与规划阶段...")
			thinkResp, err := e.provider.Generate(ctx, contextHistory, nil) // 传入 nil 剥夺工具
			if err != nil {
				return fmt.Errorf("Thinking 阶段生成失败: %w", err)
			}
			if thinkResp.Content != "" {
				fmt.Printf("🧠 [内部思考 Trace]: %s\n", thinkResp.Content)
				contextHistory = append(contextHistory, *thinkResp)
			}
		}

		// ================= Phase 2: Action =================
		log.Println("[Engine][Phase 2] 恢复工具挂载，等待模型采取行动...")
		actionResp, err := e.provider.Generate(ctx, contextHistory, availableTools)
		if err != nil {
			return fmt.Errorf("Action 阶段生成失败: %w", err)
		}

		contextHistory = append(contextHistory, *actionResp)

		if actionResp.Content != "" {
			fmt.Printf("🤖 [对外回复]: %s\n", actionResp.Content)
		}

		// ================= 执行判断 =================
		if len(actionResp.ToolCalls) == 0 {
			log.Println("[Engine] 模型未请求调用工具，任务宣告完成。")
			break
		}

		log.Printf("[Engine] 模型请求并发调用 %d 个工具...\n", len(actionResp.ToolCalls))

		// 【核心改造开始】: 从串行 (Sequential) 演进为并行 (Parallel)
		// 1. 预分配一个固定长度的切片，用于安全地存放各个并发工具的执行结果（Observation）
		// 长度与 ToolCalls 的数量完全一致
		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
		// 2. 声明 WaitGroup 用于阻塞等待所有协程完成
		var wg sync.WaitGroup
		// 3. 遍历模型请求的所有工具，为每一个工具单独 Fork 出一个 Goroutine
		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1) // 添加一个任务
			go func(idx int, call schema.ToolCall) {
				defer wg.Done() // 协程结束时技术减1
				log.Printf(" -> [Go-%d] 🛠️ 触发并行执行: %s\n", idx, call.Name)
				//	调用底层Registry执行工具
				result := e.registry.Execute(ctx, toolCall)

				if result.IsError {
					log.Printf(" -> [Go-%d]❌ 工具执行报错: %s\n", idx, result.Output)
				} else {
					log.Printf(" -> [Go-%d]✅ 工具执行成功 (返回 %d 字节)\n", idx, len(result.Output))
				}

				obsMsg := schema.Message{
					Role:       schema.RoleUser,
					Content:    result.Output,
					ToolCallID: toolCall.ID,
				}
				// 【线程安全】: 由于每个 Goroutine 操作的是预分配切片的不同索引，
				// 这里不需要加锁 (Mutex)，性能极高！
				observationMsgs[idx] = obsMsg
			}(i, toolCall)
			// 4. Join 阻塞等待：主循环挂起，直到所有的并发协程全部执行完毕
			wg.Wait()
			log.Println("[Engine] 所有并发工具执行完毕，开始聚合观察结果 (Observation)...")
			// 5. 聚合装填：将并行的结果，按照原本的顺序，一次性追加到上下文时间线中
			// 这等价于 contextHistory = append(contextHistory, observationMsgs...)
			for _, obs := range observationMsgs {
				contextHistory = append(contextHistory, obs)

			}
		}
	}

	return nil
}
