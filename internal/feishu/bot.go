package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	ctxpkg "github.com/liang21/go-tiny-claw/internal/context"
	"github.com/liang21/go-tiny-claw/internal/engine"
	"github.com/liang21/go-tiny-claw/internal/schema"
)

// reporterKey 定义 Context 中存放 Reporter 的专属键
type reportKey struct {
}

// ContextWithReporter 将专属的 Reporter 封入上下文
func ContextWithReporter(ctx context.Context, r engine.Reporter) context.Context {
	return context.WithValue(ctx, reportKey{}, r)
}

// ReporterFromContext 供底层的 Middleware 提取专属的 Reporter 发送审批卡片
func ReporterFromContext(ctx context.Context) engine.Reporter {
	if r, ok := ctx.Value(reportKey{}).(engine.Reporter); ok {
		return r
	}
	return nil
}

// ==========================================
// 2. 飞书 Bot 核心调度器
// ==========================================
// AgentEngineFactory 允许每次收到消息时，根据 Session 动态创建引擎

type AgentEngineFactory func(session *ctxpkg.Session) *engine.AgentEngine
type FeishuBot struct {
	client    *lark.Client
	appID     string
	appSecret string
	workDir   string
	factory   AgentEngineFactory
}

func NewFeishuBotWithFactory(factory AgentEngineFactory, workDir string) *FeishuBot {
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")
	if appID == "" || appSecret == "" {
		log.Fatal("请先设置 FEISHU_APP_ID 和 FEISHU_APP_SECRET 环境变量")
	}
	//	实例化飞书官方客户端
	client := lark.NewClient(appID, appSecret)

	return &FeishuBot{
		client:    client,
		appID:     appID,
		appSecret: appSecret,
		workDir:   workDir,
		factory:   factory,
	}
}

// GetEventDispatcher 用于注册到 HTTP 服务器，处理来自飞书的 POST 事件
func (b *FeishuBot) GetEventDispatcher() *dispatcher.EventDispatcher {
	encryptKey := os.Getenv("FEISHU_ENCRYPT_KEY")
	verifyToken := os.Getenv("FEISHU_VERIFY_TOKEN")
	// 使用官方 SDK 构建调度器，监听 "接收消息" 事件
	handler := dispatcher.NewEventDispatcher(verifyToken, encryptKey).
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			// 由于飞书消息体是 JSON，我们需要粗略地提取其中的文本内容。
			//这里简单处理：去掉开头结尾的特殊转义字符和引用的机器人名字。
			contentStr := *event.Event.Message.Content
			contentStr = strings.TrimPrefix(contentStr, `{"text":"`)
			contentStr = strings.TrimSuffix(contentStr, `"}`)

			chatId := *event.Event.Message.ChatId
			log.Printf("[Feishu] 收到会话 %s 消息: %s\n", chatId, contentStr)
			// 【新增】：拦截人工审批的特殊口令
			if strings.HasPrefix(contentStr, "approve") {
				taskID := strings.TrimPrefix(contentStr, "approve ")
				taskID = strings.TrimSpace(taskID)
				// 唤醒挂起的引擎协程！
				GlobalApprovalManager.ResolveApproval(taskID, true, "人类管理员已批准操作")
				log.Printf("[Feishu] 会话 %s: ✅ 已为您批准任务 %s", chatId, taskID)
				return nil
			}
			if strings.HasPrefix(contentStr, "reject") {
				taskID := strings.TrimPrefix(contentStr, "reject ")
				taskID = strings.TrimSpace(taskID)
				// 唤醒挂起的引擎协程！
				GlobalApprovalManager.ResolveApproval(taskID, false, "人类管理员拒绝了操作")
				log.Printf("[Feishu] 会话 %s: ❌ 已为您拒绝任务 %s", chatId, taskID)
				return nil
			}
			// 如果不是审批命令，则是正常对话，启动一个新的 Agent 任务去处理
			go b.handleAgentRun(chatId, contentStr)
			return nil
		}).
		OnP2MessageReadV1(func(ctx context.Context, event *larkim.P2MessageReadV1) error {
			// 消息已读事件，静默忽略（避免日志干扰）
			return nil
		})
	return handler
}

func (b *FeishuBot) handleAgentRun(chatId string, prompt string) {
	// 为当前聊天窗口实例化一个专属的 Reporter
	reporter := &FeishuReporter{
		client: b.client,
		chatId: chatId,
	}
	// 1. 获取物理隔离的 Session
	sess := ctxpkg.GlobalSessionManager.GetOrCreate(chatId, b.workDir)
	sess.Append(schema.Message{
		Role:    schema.RoleUser,
		Content: prompt,
	})
	// 2. 通过工厂模式，为当前会话生成一个挂好了专属 CostTracker 的新引擎
	eng := b.factory(sess)
	runCtx := ContextWithReporter(context.Background(), reporter)
	if err := eng.Run(runCtx, sess, reporter); err != nil {
		log.Printf("[Feishu] 运行出错: %v\n", err)
		reporter.sendMsg(fmt.Sprintf("❌ 运行出错：%v", err))
	}
}

type FeishuReporter struct {
	client *lark.Client
	chatId string
}

// sendMsg 封装了调用飞书 OpenAPI 发送卡片/文本的操作
func (r *FeishuReporter) sendMsg(text string) {
	// 构建文本消息内容
	textContent := map[string]string{
		"text": text,
	}
	contentBytes, _ := json.Marshal(textContent)
	contentStr := string(contentBytes)
	msgReq := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.CreateMessageV1ReceiveIDTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(r.chatId).
			MsgType(larkim.MsgTypeText).
			Content(contentStr).
			Build()).
		Build()
	_, _ = r.client.Im.Message.Create(context.Background(), msgReq)
}

func (r *FeishuReporter) OnThinking(ctx context.Context) {
	// 仅发一个轻量级提示，避免飞书刷屏
	r.sendMsg("🤔 模型正在慢思考 (Thinking)...")
}

func (r *FeishuReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	r.sendMsg(fmt.Sprintf("🛠️ **正在执行工具**：`%s`\n参数：`%s`", toolName, args))
}

func (r *FeishuReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		r.sendMsg(fmt.Sprintf("⚠️ **执行报错** (%s)：\n%s", toolName, result))
	} else {
		// 成功时仅汇报成功，不刷全量日志
		r.sendMsg(fmt.Sprintf("✅ **执行成功** (%s)", toolName))
	}
}

func (r *FeishuReporter) OnMessage(ctx context.Context, content string) {
	// 将模型最终的纯文本回答发给用户
	r.sendMsg(content)
}
