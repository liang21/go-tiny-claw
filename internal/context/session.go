package context

import (
	"sync"
	"time"

	"github.com/liang21/go-tiny-claw/internal/schema"
)

type Session struct {
	ID        string
	WorkDir   string
	CreatedAt time.Time
	UpdatedAt time.Time

	// [新增]用于统计该Session累积消耗的资源
	TotalPromptTokens     int
	TotalCompletionTokens int
	TotalCostCNY          float64

	history []schema.Message
	mu      sync.RWMutex
}

func NewSession(id string, workDir string) *Session {
	return &Session{
		ID:        id,
		WorkDir:   workDir,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		history:   make([]schema.Message, 0),
	}
}

func (s *Session) RecordUsage(prompt int, completion int, cost float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalPromptTokens += prompt
	s.TotalCompletionTokens += completion
	s.TotalCostCNY += cost
}

// Append 线程安全地向 Session 中追加消息
func (s *Session) Append(msgs ...schema.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, msgs...)
	s.UpdatedAt = time.Now()
	// 【持久化预留点】：在真实的工业级实现中（如 Claude Code），
	// 我们会在这里将 s.history 以 JSONL 的格式 Append 到 workDir/.claw/sessions/xxx.jsonl 中。
	// s.SaveToDisk()
}

// GetWorkingMemory 是驾驭工程的核心！
// 它不返回全量历史，而是从后往前截取最近的 N 条消息，形成 Agent 的“短期工作记忆”。
func (s *Session) GetWorkingMemory(limit int) []schema.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := len(s.history)
	if total <= limit || limit <= 0 {
		// 如果历史总量小于限制，或者不设限，全量返回 (需要深拷贝以防外部修改
		res := make([]schema.Message, total)
		copy(res, s.history)
		return res
	}
	// 截取最近的 limit 条消息
	res := make([]schema.Message, limit)
	copy(res, s.history[total-limit:])
	// 【驾驭防线 1】：大模型 API 强制要求历史消息的连续性！
	// 如果我们截断的第一条消息恰好是一个 ToolResult (RoleUser 且含有 ToolCallID)，
	// 但发出这个请求的 ToolCall 被我们截断抛弃了，大模型 API 会直接报 400 Bad Request。
	// 因此，如果切片首条属于“孤儿”工具响应，我们必须将其强行舍弃，顺延到下一条正常的 User/Assistant 消息
	for len(res) > 0 {
		if res[0].Role == schema.RoleUser && res[0].ToolCallID != "" {
			res = res[1:]
		} else {
			break
		}
	}
	// 【驾驭防线 2】：GLM 等大模型要求 system 之后的首条消息必须是「真正的 user 消息」。
	// 一旦历史增长到超过 limit，原始任务(history[0]) 会被滑出窗口，导致 res 以 assistant 消息开头，
	// 大模型会直接以 "messages 参数非法" (code 1214) 拒绝整包请求。
	// 因此当窗口头部不是真正的 user 消息时，把整段历史里的第一条真实 user 消息（用户的原始任务）
	// 重新钉在最前面，既锚定了对话起点，也让模型始终记得自己要完成的目标。
	if len(res) == 0 || res[0].Role != schema.RoleUser || res[0].ToolCallID != "" {
		for i := range s.history {
			anchor := s.history[i]
			if anchor.Role == schema.RoleUser && anchor.ToolCallID == "" {
				res = append([]schema.Message{anchor}, res...)
				break
			}
		}
	}
	return res
}

// ==========================================
// 全局 Session Manager: 用于多用户/多终端隔离
// ==========================================
type SessionManager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
}

var GlobalSessionManager = &SessionManager{
	sessions: make(map[string]*Session),
}

// GetOrCreate 获取或创建一个会话
func (sm *SessionManager) GetOrCreate(id string, workDir string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sess, exists := sm.sessions[id]; exists {
		return sess
	}
	sess := NewSession(id, workDir)
	sm.sessions[id] = sess
	return sess
}
