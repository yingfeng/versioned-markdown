package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ========== Agent State ==========

// AgentState holds the working state across multiple tool calls in an agent session.
type AgentState struct {
	Files   []FileNode
	Skills  []SkillDef
	Topics  []TopicInfo
	Outputs []OutputFile
	Input   *CompileInput
	Task    *TaskState
	mu      sync.Mutex
}

// globalState is the shared state for the current agent run.
var globalState = &AgentState{
	Input: &CompileInput{},
	Task:  &TaskState{ID: "agent", Status: "running", Log: ""},
}

func (s *AgentState) appendLog(format string, args ...interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Task != nil {
		s.Task.AppendLog(format, args...)
	}
}

// ========== Tool adapter ==========

// ToolInfoToCallable converts a function into an InvokableTool.
func ToolInfoToCallable(name, desc string, fn func(ctx context.Context, argsJSON string) (string, error)) tool.InvokableTool {
	return &adapterTool{
		name: name,
		desc: desc,
		fn:   fn,
	}
}

type adapterTool struct {
	name string
	desc string
	fn   func(ctx context.Context, argsJSON string) (string, error)
}

func (t *adapterTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: t.desc,
	}, nil
}

func (t *adapterTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	return t.fn(ctx, argumentsInJSON)
}

// ========== Tool name constants ==========

const (
	ToolLoadFiles     = "load_files"
	ToolScanTopics    = "scan_topics"
	ToolCompileTopic  = "compile_topic"
	ToolConsistency   = "consistency_review"
	ToolGenerateIndex = "generate_index"
	ToolGenerateLog   = "generate_log"
	ToolQualityReview = "quality_review"
	ToolReadSource    = "read_source_file"
	ToolWriteOutput   = "write_output"
	ToolAppendNote    = "append_mapping_note"
)

// ========== Tool result helpers ==========

func toolResultOK(status string) string {
	data, _ := json.Marshal(map[string]string{"status": status})
	return string(data)
}

func usage(desc string) string {
	return fmt.Sprintf("工具说明：\n%s\n\n调用时传入JSON参数。", desc)
}

// ========== Summarizers ==========

func summarizeFiles(files []FileNode) []map[string]any {
	result := make([]map[string]any, 0, len(files))
	for _, f := range files {
		summary := f.Content
		if len(summary) > 200 {
			summary = summary[:200] + "..."
		}
		result = append(result, map[string]any{
			"path":    f.Path,
			"title":   f.Title,
			"summary": summary,
			"size":    len(f.Content),
		})
	}
	return result
}

func summarizeSkills(skills []SkillDef) []map[string]any {
	result := make([]map[string]any, 0, len(skills))
	for _, s := range skills {
		result = append(result, map[string]any{
			"name":    s.Name,
			"content": truncate(s.Content, 300),
		})
	}
	return result
}

func topicNames(topics []TopicInfo) []string {
	names := make([]string, len(topics))
	for i, t := range topics {
		names[i] = t.Name
	}
	return names
}

// ========== Tool call executor ==========

// executeToolCall finds and executes a tool by name.
func executeToolCall(tools []tool.InvokableTool, name string, argsJSON string) string {
	for _, t := range tools {
		info, err := t.Info(context.Background())
		if err != nil {
			continue
		}
		if info.Name == name {
			result, err := t.InvokableRun(context.Background(), argsJSON)
			if err != nil {
				return fmt.Sprintf("错误: %v", err)
			}
			return result
		}
	}
	return fmt.Sprintf("未知工具: %s", name)
}

// formatToolDescriptions creates a string listing all available tools for the prompt.
func formatToolDescriptions(tools []tool.InvokableTool) string {
	var b strings.Builder
	for _, t := range tools {
		info, err := t.Info(context.Background())
		if err != nil {
			continue
		}
		b.WriteString(fmt.Sprintf("- **%s**: %s", info.Name, info.Desc))
		b.WriteString("\n")
	}
	return b.String()
}

// isTaskComplete checks if the agent should stop.
func isTaskComplete(state *AgentState) bool {
	if len(state.Outputs) > 0 {
		hasIndex := false
		hasLog := false
		for _, o := range state.Outputs {
			if o.Path == "INDEX.md" {
				hasIndex = true
			}
			if o.Path == "log.md" {
				hasLog = true
			}
		}
		return hasIndex && hasLog
	}
	return false
}
