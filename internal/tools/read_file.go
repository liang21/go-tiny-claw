package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/liang21/go-tiny-claw/internal/schema"
)

type ReadFileTool struct {
	workDir string
}

func NewReadFileTool(workDir string) *ReadFileTool {
	return &ReadFileTool{workDir: workDir}
}

func (t *ReadFileTool) Name() string {
	return "read_file"
}
func (t *ReadFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "读取指定路径的文件内容。请提供相对工作区的路径。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "要读取的文件路径，如 cmd/claw/main.go",
				},
			},
			"required": []string{"path"},
		},
	}
}

type readFileArgs struct {
	Path string `json:"path"`
}

func (t *ReadFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// 1. 延迟解析：将大模型传过来的 JSON 参数解析为强类型结构体
	var input readFileArgs
	if err := json.Unmarshal(args, &input); err != nil {
		// 返回 error 会被 Registry 捕获并传给大模型，模型会知道自己 JSON 格式写错了
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	// 2. 拼接绝对路径 (注意：生产环境中需要做路径穿越检测防范，防止 ../../etc/passwd)
	fullPath := filepath.Join(t.workDir, input.Path)
	//	3.执行无力IO操作
	file, err := os.Open(fullPath)
	if err != nil {
		return "", fmt.Errorf("打开文件失败: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("读取文件内容失败: %w", err)
	}
	// 4. 【核心防线】长度截断保护
	//为了防止大模型读取几百 MB 的日志文件导致 Context 瞬间爆炸 (OOM)，
	//我们在工具内部直接进行物理截断。
	const maxLen = 8000
	if len(content) > maxLen {
		truncatedMsg := fmt.Sprintf("%s\n\n...[由于内容过长，已被系统截断至前 %d 字节]...", string(content[:maxLen]), maxLen)
		return truncatedMsg, nil
	}
	return string(content), nil
}
