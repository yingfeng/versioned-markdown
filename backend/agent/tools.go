package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ========== Tool Implementations ==========
// Each pipeline stage is an InvokableTool. The LLM reads SKILL.md
// and autonomously decides which tools to call and in what order.

// LoadFilesTool loads workspace files and skills.
type LoadFilesTool struct {
	compiler *Compiler
	task     *TaskState
	input    *CompileInput
}

func (t *LoadFilesTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "load_files",
		Desc: "加载工作区文件、技能文件。需要 workspace_id 参数。返回源文件列表和技能内容。必须先调用此工具。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"workspace_id": {
				Desc:     "要加载的工作区 ID",
				Required: true,
				Type:     schema.String,
			},
		}),
	}, nil
}

func (t *LoadFilesTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := jsonUnmarshalStrict([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	t.input.WorkspaceID = args.WorkspaceID
	files, skills := t.compiler.loadPhase(t.task, t.input)
	globalState.Files = files
	globalState.Skills = skills
	result := map[string]any{
		"file_count":  len(files),
		"skill_count": len(skills),
	}
	data, _ := json.Marshal(result)
	return string(data), nil
}

// ScanTopicsTool scans loaded files to discover knowledge topics.
type ScanTopicsTool struct {
	compiler *Compiler
	task     *TaskState
}

func (t *ScanTopicsTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "scan_topics",
		Desc: "扫描已加载的源文件，发现知识主题。必须先调用 load_files。返回主题列表。",
	}, nil
}

func (t *ScanTopicsTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	s := globalState
	topics := t.compiler.scanPhase(t.task, s.Files, s.Skills, s.Input.Instructions)
	s.Topics = topics
	return fmt.Sprintf("发现 %d 个主题", len(topics)), nil
}

// CompileTool compiles a single topic into a knowledge article.
type CompileTool struct {
	compiler *Compiler
	task     *TaskState
}

func (t *CompileTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "compile_topic",
		Desc: "编译单个主题的知识文章。需要 topic_name 参数。输出 Markdown 格式文章。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"topic_name": {
				Desc:     "要编译的主题名称",
				Required: true,
				Type:     schema.String,
			},
		}),
	}, nil
}

func (t *CompileTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		TopicName string `json:"topic_name"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	s := globalState
	for i := range s.Topics {
		if s.Topics[i].Name == args.TopicName {
			article := t.compiler.compilePhase(t.task, s.Topics[i], s.Files, s.Topics, s.Outputs, "synthesis", s.Skills, "wiki", "")
			if article != nil {
				s.Outputs = append(s.Outputs, *article)
				return fmt.Sprintf("已编译: %s (%d chars)", article.Path, len(article.Content)), nil
			}
			return fmt.Sprintf("编译失败: %s", args.TopicName), nil
		}
	}
	return fmt.Sprintf("未找到主题: %s", args.TopicName), nil
}

// ReadSourceTool reads a specific source file's full content.
type ReadSourceTool struct{}

func (t *ReadSourceTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "read_file",
		Desc: "读取指定源文件的完整内容。需要 file_path 参数。必须在 load_files 之后调用。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file_path": {
				Desc:     "要读取的源文件路径",
				Required: true,
				Type:     schema.String,
			},
		}),
	}, nil
}

func (t *ReadSourceTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	for _, f := range globalState.Files {
		if f.Path == args.FilePath {
			return f.Content, nil
		}
	}
	return fmt.Sprintf("文件未找到: %s", args.FilePath), nil
}

// WriteOutputTool writes compiled content as an output file.
type WriteOutputTool struct{}

func (t *WriteOutputTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "write_output",
		Desc: "将编译结果写入输出文件。需要 file_path 和 content 参数。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file_path": {
				Desc:     "输出文件路径（如 INDEX.md）",
				Required: true,
				Type:     schema.String,
			},
			"content": {
				Desc:     "Markdown 内容",
				Required: true,
				Type:     schema.String,
			},
		}),
	}, nil
}

func (t *WriteOutputTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	globalState.Outputs = append(globalState.Outputs, OutputFile{Path: args.FilePath, Content: args.Content})
	return fmt.Sprintf("已写入: %s (%d chars)", args.FilePath, len(args.Content)), nil
}

// jsonUnmarshalStrict is a helper to unmarshal with strict error reporting.
func jsonUnmarshalStrict(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
