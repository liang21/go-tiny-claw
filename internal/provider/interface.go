package provider

import (
	"context"

	"github.com/liang21/go-tiny-claw/internal/schema"
)

type LLMProvider interface {
	Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error)
}
