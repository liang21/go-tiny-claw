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
