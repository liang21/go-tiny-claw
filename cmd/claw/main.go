// cmd/claw/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
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

func (m mockRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
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
	//fmt.Println("🚀 欢迎来到 go-tiny-claw 引擎启动序列")
	//
	//workDir, _ := os.Getwd()
	//p := &mockProvider{}
	//r := &mockRegistry{}
	//eng := engine.NewAgentEngine(p, r, workDir, true)
	//err := eng.Run(context.Background(), "帮我检查一下当前目录下的文件并输出一个 README.md 大纲")
	//if err != nil {
	//	log.Fatalf("引擎运行崩溃: %v", err)
	//}
	// TODO: 1. 初始化模型 Provider (大脑)
	// provider := provider.NewClaudeProvider(...)

	// TODO: 2. 初始化 Tool Registry (手脚)
	// registry := tools.NewRegistry()
	// registry.Register(tools.NewBashTool())

	// TODO: 3. 初始化上下文管理器 (内存管理器)
	// ctxManager := context.NewManager(...)

	// TODO: 4. 组装并启动核心 Engine (操作系统心脏)
	// eng := eng.NewAgentEngine(provider, registry, ctxManager)

	// fmt.Println("开始执行任务...")
	// err := eng.Run("帮我检查一下当前目录下的文件并输出一个 README.md 大纲")
	// if err != nil {
	//  log.Fatalf("引擎运行崩溃: %v", err)
	// }

	//log.Println("架构蓝图搭建完毕，等待各核心模块注入！")
	//通过命令行参数接收用户的 prompt
	promptPtr := flag.String("prompt", "", "要交给 Agent 执行的任务描述")
	flag.Parse()
	if *promptPtr == "" {
		fmt.Println("用法: go run cmd/claw/main.go -prompt \"你的任务指令\"")
		os.Exit(1)
	}
	// 从 .env 加载配置（若存在）；不覆盖已存在的环境变量，缺失时静默回退到 shell 环境
	if os.Getenv("ZHIPU_API_KEY") == "" {
		log.Fatal("请先导出 ZHIPU_API_KEY 环境变量（或在项目根目录创建 .env 文件）")
	}
	workDir, _ := os.Getwd()
	workDir += "/workspace"
	llmProvider := provider.NewZhipuOpenAIProvider("glm-4.5-air")
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	// 实例化引擎 (关闭思考模式以提速)
	eng := engine.NewAgentEngine(llmProvider, registry, false, true)
	reporter := engine.NewTerminalReporter()
	sessionID := "task_web_server_01"
	sess := engine.GlobalSessionManager.GetOrCreate(sessionID, workDir)
	log.Printf("\n>>> 🚀 收到指令: %s\n", *promptPtr)
	// 发起一个会导致读取大文件的恶意任务
	//prompt := ` 请帮我执行以下三个步骤： 1. 使用 bash 执行 echo "开始排查日志" 2. 使用 read_file 工具读取当前目录下的巨大文件 mock_log.txt 3. 使用 bash 执行 date 命令获取当前时间，并告诉我任务全部完成。 `
	sess.Append(schema.Message{Role: schema.RoleUser, Content: *promptPtr})
	err := eng.Run(context.Background(), sess, reporter)
	if err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
	//prompt := `
	//	我当前目录下有 a.txt, b.txt, c.txt 三个文件。
	//	为了节省时间，请你同时一次性读取这三个文件，并将它们的内容综合起来，告诉我它们分别记录了什么领域的信息。
	//`
	// 2.初始化飞书 Bot 调度器
	//bot := feishu.NewFeishuBot(eng)
	//handler := httpserverext.NewEventHandlerFunc(bot.GetEventDispatcher())
	//http.HandleFunc("/webhook/event", handler)
	//port := ":48080"
	//log.Printf("🚀 go-tiny-claw 飞书服务端已启动，正在监听 %s 端口\n", port)
	//err := http.ListenAndServe(port, nil)
	////err := eng.Run(context.Background(), prompt)
	//if err != nil {
	//	log.Fatalf("服务器启动失败: %v", err)
	//}
}
