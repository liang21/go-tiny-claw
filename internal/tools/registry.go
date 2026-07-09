package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/liang21/go-tiny-claw/internal/schema"
)

type MiddlewareFunc func(ctx context.Context, call schema.ToolCall) (allow bool, rejectReason string)

type BaseTool interface {
	Name() string
	Definition() schema.ToolDefinition
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

type Registry interface {
	Register(tool BaseTool)
	Use(mw MiddlewareFunc)
	GetAvailableTools() []schema.ToolDefinition
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}

type registryImpl struct {
	tools      map[string]BaseTool
	middleware []MiddlewareFunc //【新增】保存挂载的中间件链
}

func NewRegistry() Registry {
	return &registryImpl{
		tools:      make(map[string]BaseTool),
		middleware: make([]MiddlewareFunc, 0),
	}
}

func (r *registryImpl) Register(tool BaseTool) {
	name := tool.Name()
	if _, exits := r.tools[name]; exits {
		log.Printf("[Warning] 工具 '%s' 已经被注册，将被覆盖。\n", name)
	}
	r.tools[name] = tool
	log.Printf("[Registry] 成功挂载工具: %s\n", name)
}

func (r *registryImpl) Use(mw MiddlewareFunc) {
	r.middleware = append(r.middleware, mw)
}
func (r *registryImpl) GetAvailableTools() []schema.ToolDefinition {
	var defs []schema.ToolDefinition
	for _, tool := range r.tools {
		defs = append(defs, tool.Definition())
	}
	return defs
}

func (r *registryImpl) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	// 1. 路由查找：如果在注册表中找不到该工具，这是模型产生了幻觉，直接向模型抛出错误
	tool, exits := r.tools[call.Name]
	if !exits {
		errMsg := fmt.Sprintf("Error: 系统中不存在名为 '%s' 的工具。", call.Name)
		return schema.ToolResult{
			IsError:    true,
			Output:     errMsg,
			ToolCallID: call.ID,
		}
	}
	// 2. 【核心防御】在执行底层逻辑前，依次运行所有的 Middleware
	for _, mw := range r.middleware {
		allow, rejectReason := mw(ctx, call)
		log.Printf("[Registry] ⚠️ 工具 %s 被 Middleware 拦截: %s\n", call.Name, rejectReason)
		if !allow {
			return schema.ToolResult{
				IsError:    true,
				Output:     fmt.Sprintf("执行被系统拦截。原因: %s", rejectReason),
				ToolCallID: call.ID,
			}
		}
	}
	// 3. 执行工具逻辑 (如果所有 Middleware 都放行了)
	output, err := tool.Execute(ctx, call.Arguments)
	if err != nil {
		errMsg := fmt.Sprintf("Error executing %s: %v", call.Name, err)
		return schema.ToolResult{
			IsError:    true,
			Output:     errMsg,
			ToolCallID: call.ID,
		}
	}
	return schema.ToolResult{
		IsError:    false,
		Output:     output,
		ToolCallID: call.ID,
	}
}
