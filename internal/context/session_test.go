package context

import (
	"testing"

	"github.com/liang21/go-tiny-claw/internal/schema"
)

// TestSessionGetWorkingMemoryNoPanic 复现 session.go 中 RLocker() 误用导致的
// "RUnlock of unlocked RWMutex" 崩溃。修复前该用例会因 fatal panic 失败。
func TestSessionGetWorkingMemoryNoPanic(t *testing.T) {
	s := NewSession("sess_test", ".")
	s.Append(
		schema.Message{Role: schema.RoleUser, Content: "你好"},
		schema.Message{Role: schema.RoleAssistant, Content: "我是 go-tiny-claw"},
		schema.Message{Role: schema.RoleUser, Content: "帮我列一下文件"},
	)

	// limit 大于总量：应全量返回
	if mem := s.GetWorkingMemory(20); len(mem) != 3 {
		t.Fatalf("limit=20 时期望 3 条工作记忆，实际 %d", len(mem))
	}

	// limit 小于总量：应从后往前截取
	if mem := s.GetWorkingMemory(1); len(mem) != 1 {
		t.Fatalf("limit=1 时期望 1 条工作记忆，实际 %d", len(mem))
	}
}

// TestWorkingMemoryStartsWithUser 复现 GLM "messages 参数非法" (1214) 崩溃：
// 当历史超过 limit、原始任务被滑出窗口后，截取的工作记忆会以 assistant 消息开头。
// 而 GLM 等大模型要求 system 之后的首条消息必须是真正的 user 消息，否则整包请求被拒。
func TestWorkingMemoryStartsWithUser(t *testing.T) {
	s := NewSession("sess_window", ".")

	// 1 条原始任务 + 10 组 (assistant tool_call / tool_result) = 21 条历史
	s.Append(schema.Message{Role: schema.RoleUser, Content: "帮我分析并发安全问题"})
	for i := 0; i < 10; i++ {
		callID := "call_" + string(rune('a'+i))
		s.Append(schema.Message{
			Role:      schema.RoleAssistant,
			ToolCalls: []schema.ToolCall{{ID: callID, Name: "bash", Arguments: []byte(`{"command":"ls"}`)}},
		})
		s.Append(schema.Message{Role: schema.RoleUser, Content: "执行结果...", ToolCallID: callID})
	}

	mem := s.GetWorkingMemory(20)
	if len(mem) == 0 {
		t.Fatal("工作记忆不应为空")
	}
	first := mem[0]
	if first.Role != schema.RoleUser || first.ToolCallID != "" {
		t.Fatalf("工作记忆首条必须是真正的 user 消息（对话锚点），实际 role=%q toolCallID=%q",
			first.Role, first.ToolCallID)
	}
}
