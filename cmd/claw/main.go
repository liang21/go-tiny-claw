// cmd/claw/main.go
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	"github.com/larksuite/oapi-sdk-go/v3/core/httpserverext"
	ctxpkg "github.com/liang21/go-tiny-claw/internal/context"
	"github.com/liang21/go-tiny-claw/internal/engine"
	"github.com/liang21/go-tiny-claw/internal/feishu"
	"github.com/liang21/go-tiny-claw/internal/observability"
	"github.com/liang21/go-tiny-claw/internal/provider"
	"github.com/liang21/go-tiny-claw/internal/schema"
	"github.com/liang21/go-tiny-claw/internal/tools"
)

func init() {
	_ = godotenv.Load()
}
func main() {
	log.Println("🚀 正在启动 go-tiny-claw AgentOps 飞书服务端...")
	if os.Getenv("ZHIPU_API_KEY") == "" || os.Getenv("FEISHU_APP_ID") == "" {
		log.Fatal("请设置环境变量 ZHIPU_API_KEY")
	}
	// 1. 设定监控的物理工作区
	workDir, _ := os.Getwd()
	workDir += "/workspace"
	if err := os.MkdirAll(workDir, 0755); err != nil {
		log.Fatalf("创建工作目录失败: %v", err)
	}

	//	2.初始化核心基础服务
	modelName := "glm-4.5-air"
	llmProvider := provider.NewZhipuOpenAIProvider(modelName)

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	// 3. 【核心防御】：注入安全拦截 Middleware
	registry.Use(func(ctx context.Context, call schema.ToolCall) (bool, string) {
		argsStr := string(call.Arguments)
		//	检查是否命中危险命令黑名单
		if feishu.IsDangerousCommand(call.Name, argsStr) {
			taskID := call.ID
			log.Printf("[Middleware] 拦截到高危操作: %s，触发飞书审批挂起...\n", call.Name)
			// 【驾驭魔术】：从 Context 中优雅地取出专属于发起该请求群聊的 Reporter！
			//  注意这里的强转，因为我们在 WaitForApproval 中需要调用 FeishuReporter 特有的 sendMsg。
			currentReporter, _ := feishu.ReporterFromContext(ctx).(*feishu.FeishuReporter)
			// 当前 Goroutine 死死挂起，向飞书发送卡片，等待人类决定
			allowed, reason := feishu.GlobalApprovalManager.WaitForApproval(taskID, call.Name, argsStr, currentReporter)
			if !allowed {
				log.Printf("[Middleware] 拒绝执行高危操作: %s，请检查飞书审批结果...\n", call.Name)
				return false, reason
			}
			return true, ""
		}
		return true, ""
	})
	log.Println("🛡️ 安全防御 Middleware 已挂载。")
	// 4. 动态 Factory 组装器：保证高并发调用的物理独立性与账单准确追踪
	engineFactory := func(session *ctxpkg.Session) *engine.AgentEngine {
		tracker := observability.NewCostTracker(llmProvider, modelName, session)
		return engine.NewAgentEngine(tracker, registry, false, false)
	}
	// 5. 初始化飞书 Bot 调度中心
	bot := feishu.NewFeishuBotWithFactory(engineFactory, workDir)
	handler := httpserverext.NewEventHandlerFunc(bot.GetEventDispatcher())
	// 6. 注册 Webhook 路由并启动 HTTP Server
	http.HandleFunc("/webhook/event", handler)
	prot := ":48080"
	log.Printf("🚀 服务已启动，监听端口 %s...\n", prot)
	err := http.ListenAndServe(prot, nil)
	if err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
