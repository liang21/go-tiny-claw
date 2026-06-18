package tools

import (
	"context"

	"github.com/liang21/go-tiny-claw/internal/schema"
)

type Registry interface {
	GetAvailableTools() []schema.ToolDefinition
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}
