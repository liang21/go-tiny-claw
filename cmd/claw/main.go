// cmd/claw/main.go
package main

import (
	"context"
	"log"
	"os"

	"github.com/joho/godotenv"
	context2 "github.com/liang21/go-tiny-claw/internal/context"
	"github.com/liang21/go-tiny-claw/internal/engine"
	"github.com/liang21/go-tiny-claw/internal/provider"
	"github.com/liang21/go-tiny-claw/internal/schema"
	"github.com/liang21/go-tiny-claw/internal/tools"
)

type mockProvider struct {
	turn int
}

// 模拟大模型的响应：第一轮请求执行 bash，第二轮输出最终结果
func (m *mockProvider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	if len(tools) == 0 {
		return &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "【推理中】目标是检查文件。我不能直接盲猜，我需要先调用 bash 工具执行 ls 命令，看看当前目录下有什么，然后再做定夺。",
		}, nil
	}
	m.turn++
	if m.turn == 1 {
		return &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "我要执行我刚才计划的步骤了。",
			ToolCalls: []schema.ToolCall{
				{ID: "call_123", Name: "bash", Arguments: []byte(`{"command": "ls -la"}`)},
			},
		}, nil
	}
	return &schema.Message{
		Role:    schema.RoleAssistant,
		Content: "根据工具返回的结果，我看到了 main.go，任务圆满完成！",
	}, nil
}

// ==========================================
// 2. 伪造的 Tool Registry
// ==========================================
type mockRegistry struct {
}

func (m *mockRegistry) GetAvailableTools() []schema.ToolDefinition {
	return []schema.ToolDefinition{
		{
			Name:        "get_wether",
			Description: "获取天气信息",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"city": map[string]interface{}{
						"type": "string",
					},
				},
				"required": []string{"city"},
			},
		},
	}
}

func (m *mockRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	log.Printf(" -> [Mock 工具执行] 获取 %s 的天气中...\n", call.Name)
	return schema.ToolResult{
		ToolCallID: call.ID,
		Output:     "API 返回：今天是晴天，气温 25 度。",
		IsError:    false,
	}
}
func init() {
	_ = godotenv.Load()
}
func main() {
	// 从 .env 加载配置（若存在）；不覆盖已存在的环境变量，缺失时静默回退到 shell 环境
	if os.Getenv("ZHIPU_API_KEY") == "" {
		log.Fatal("请先导出 ZHIPU_API_KEY 环境变量（或在项目根目录创建 .env 文件）")
	}
	workDir, _ := os.Getwd()
	modelName := "glm-4.5-air"
	workDir += "/workspace"
	llmProvider := provider.NewZhipuOpenAIProvider(modelName)
	registry := tools.NewRegistry()

	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))

	eng := engine.NewAgentEngine(llmProvider, registry, false, false)

	reporter := engine.NewTerminalReporter()
	// 触发一个跨工具类型的并发任务
	prompt := ` 
		为了加快执行速度，请你在一轮回复中，【同时并行】完成以下两件事： 
		1. 使用 bash 工具执行 'sleep 2 && echo "系统环境检查完毕"' 
		2. 使用 write_file 工具，在当前目录下创建一个 'trace_test.md'，内容写上 "测试并发的写入"。 
		请确保你是分别调用两个不同的工具，不要试图把它们合并成一个命令！`
	sess := context2.GlobalSessionManager.GetOrCreate("test_trace_001", workDir)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})
	log.Println("\n>>> 🚀 启动带 Tracing 链路追踪的测试...")
	err := eng.Run(context.Background(), sess, reporter)
	if err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
	//// 假设一个bot绑定一个session
	//sessionID := "test_command_intercept_001"
	//
	//sess := engine.GlobalSessionManager.GetOrCreate(sessionID, workDir)
	//sess.Append(schema.Message{Role: schema.RoleUser, Content: ""})
	//
	//bot := feishu.NewFeishuBot(eng, sess)
	//handler := httpserverext.NewEventHandlerFunc(bot.GetEventDispatcher())
	//// 【核心注入】注册安全拦截 Middleware
	//registry.Use(func(ctx context.Context, call schema.ToolCall) (bool, string) {
	//	argsStr := string(call.Arguments)
	//	//	检查是否命中高危特征库
	//	if feishu.IsDangerousCommand(call.Name, argsStr) {
	//		taskID := call.ID
	//		// 挂起当前协程，发送消息给飞书，死死等待人类的审批！
	//		allowed, reason := feishu.GlobalApprovalManager.WaitForApproval(taskID, call.Name, argsStr, bot.Reporter())
	//		if allowed {
	//			return false, reason
	//		}
	//		return true, ""
	//	}
	//	return true, ""
	//})
	//
	//// 3. 注册路由并启动 HTTP 服务
	//http.HandleFunc("/webhook/event", handler)
	//
	//port := ":48080"
	//log.Printf("🚀 go-tiny-claw 飞书服务端已启动，正在监听 %s 端口\n", port)
	//err := http.ListenAndServe(port, nil)
	//if err != nil {
	//	log.Fatalf("服务器启动失败: %v", err)
	//}
	//// 实例化引擎 (关闭思考模式以提速)
	//eng := engine.NewAgentEngine(llmProvider, registry, false, false)
	//reporter := engine.NewTerminalReporter()

}
