package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ========== Agent Loop: Claude Code-style ==========
// The LLM reads SKILL.md as system prompt, then autonomously decides
// which tools to call and in what order.

// AgentConfig configures the agent loop.
type AgentConfig struct {
	LLM          *LLMClient
	WorkspaceID  string
	SkillContent string // loaded SKILL.md content
	Instructions string // user-provided instructions
	OutputDir    string
}

// AgentResult holds the final output of an agent run.
type AgentResult struct {
	Outputs []OutputFile
	Log     string
	Error   string
}

// RunAgentLoop executes the agent with the given config.
// The LLM reads SKILL.md, then autonomously calls tools.
func RunAgentLoop(ctx context.Context, cfg *AgentConfig) (*AgentResult, error) {
	start := time.Now()

	// 1. Create ChatModel
	chatModel := NewDeepSeekChatModel(cfg.LLM)
	chatModel.BindTools(nil)

	// 2. Build system prompt from SKILL.md
	systemPrompt := buildSystemPrompt(cfg)

	// 3. Create tool instances
	compiler := &Compiler{}
	agentTools := createAgentTools(compiler)

	// 4. Reset global state
	globalState = &AgentState{
		Input: &CompileInput{WorkspaceID: cfg.WorkspaceID},
		Task:  &TaskState{ID: "agent", Status: "running", Log: ""},
	}

	// 5. Agent Loop: LLM decides which tool to call next
	maxTurns := 30
	turnCount := 0

	toolDescs := formatToolDescriptions(agentTools)

	messages := []*schema.Message{
		schema.SystemMessage(systemPrompt + "\n\n## 可用工具\n\n" + toolDescs),
	}

	userContent := cfg.Instructions
	if userContent == "" {
		userContent = "请按照上述工作流程，逐步执行知识编译任务。"
	}
	messages = append(messages, schema.UserMessage(userContent))

	for turnCount < maxTurns {
		turnCount++
		globalState.appendLog("[TURN %d] LLM reasoning...\n", turnCount)

		resp, err := chatModel.Generate(ctx, messages)
		if err != nil {
			globalState.appendLog("[ERROR] LLM call failed: %v\n", err)
			break
		}

		if len(resp.ToolCalls) > 0 {
			assistantMsg := schema.AssistantMessage(resp.Content, resp.ToolCalls)
			messages = append(messages, assistantMsg)
			globalState.appendLog("[TURN %d] LLM called %d tool(s)\n", turnCount, len(resp.ToolCalls))

			for _, tc := range resp.ToolCalls {
				globalState.appendLog("  -> tool: %s\n", tc.Function.Name)
				result := executeToolCall(agentTools, tc.Function.Name, tc.Function.Arguments)
				globalState.appendLog("  <- result: %s\n", truncate(result, 100))

				toolMsg := schema.ToolMessage(result, tc.ID)
				messages = append(messages, toolMsg)
			}

			if isTaskComplete(globalState) {
				globalState.appendLog("[DONE] Task appears complete\n")
				break
			}
		} else {
			globalState.appendLog("[TURN %d] LLM response: %s\n", turnCount, truncate(resp.Content, 200))
			messages = append(messages, schema.AssistantMessage(resp.Content, nil))

			if strings.Contains(resp.Content, "完成") ||
				strings.Contains(resp.Content, "DONE") ||
				strings.Contains(resp.Content, "complete") {
				globalState.appendLog("[DONE] LLM indicated completion\n")
				break
			}

			if turnCount > 3 && len(globalState.Outputs) > 0 {
				break
			}
		}
	}

	elapsed := time.Since(start)
	globalState.appendLog("[TASK] Completed in %v (%d turns)\n", elapsed, turnCount)

	// Collect log from global state
	var logText string
	if globalState.Task != nil {
		logText = globalState.Task.GetLog()
	}

	return &AgentResult{
		Outputs: globalState.Outputs,
		Log:     logText,
	}, nil
}

// createAgentTools builds the list of tools available to the LLM.
func createAgentTools(compiler *Compiler) []tool.InvokableTool {
	return []tool.InvokableTool{
		ToolInfoToCallable("load_files", usage("加载工作区文件。需要 workspace_id。返回文件摘要。"),
			func(ctx context.Context, argsJSON string) (string, error) {
				var args struct {
					WorkspaceID string `json:"workspace_id"`
				}
				if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
					return "", fmt.Errorf("parse args: %w", err)
				}
				globalState.Input.WorkspaceID = args.WorkspaceID

				input := &CompileInput{WorkspaceID: args.WorkspaceID}
				files, skills := compiler.loadPhase(globalState.Task, input)
				globalState.Files = files
				globalState.Skills = skills

				result := map[string]any{
					"file_count":  len(files),
					"skill_count": len(skills),
				}
				data, _ := json.Marshal(result)
				return string(data), nil
			}),

		ToolInfoToCallable("read_file", usage("读取指定源文件的完整内容。需要 file_path。"),
			func(ctx context.Context, argsJSON string) (string, error) {
				var args struct {
					FilePath string `json:"file_path"`
				}
				if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
					return "", fmt.Errorf("parse args: %w", err)
				}
				for _, f := range globalState.Files {
					if strings.Contains(f.Path, args.FilePath) || f.Path == args.FilePath {
						return fmt.Sprintf("---\npath: %s\ntitle: %s\ncontent:\n%s\n---",
							f.Path, f.Title, f.Content), nil
					}
				}
				return fmt.Sprintf("未找到: %s", args.FilePath), nil
			}),

		ToolInfoToCallable("extract_keywords", usage("从已加载文件中提取关键词。"),
			func(ctx context.Context, argsJSON string) (string, error) {
				compiler.extractKeywords(globalState.Task, globalState.Files)
				return fmt.Sprintf("已提取 %d 个文件的关键词", len(globalState.Files)), nil
			}),

		ToolInfoToCallable("scan_topics", usage("扫描文件发现知识主题。"),
			func(ctx context.Context, argsJSON string) (string, error) {
				topics := compiler.scanPhase(globalState.Task, globalState.Files, globalState.Skills, globalState.Input.Instructions)
				globalState.Topics = topics
				data, _ := json.MarshalIndent(topics, "", "  ")
				return fmt.Sprintf("发现 %d 个主题:\n%s", len(topics), string(data)), nil
			}),

		ToolInfoToCallable("compile_topic", usage("编译单个主题的知识文章。需要 topic_name。"),
			func(ctx context.Context, argsJSON string) (string, error) {
				var args struct {
					TopicName string `json:"topic_name"`
				}
				if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
					return "", fmt.Errorf("parse args: %w", err)
				}

				for i := range globalState.Topics {
					if globalState.Topics[i].Name == args.TopicName {
						article := compiler.compilePhase(globalState.Task, globalState.Topics[i],
							globalState.Files, globalState.Topics, globalState.Outputs, "synthesis", globalState.Skills, "wiki")
						if article != nil {
							globalState.Outputs = append(globalState.Outputs, *article)
							return fmt.Sprintf("已编译: %s (%d chars)", article.Path, len(article.Content)), nil
						}
						return fmt.Sprintf("编译失败: %s", args.TopicName), nil
					}
				}
				return fmt.Sprintf("未找到主题: %s", args.TopicName), nil
			}),

		ToolInfoToCallable("consistency_review", usage("对所有已编译文章进行一致性审查。"),
			func(ctx context.Context, argsJSON string) (string, error) {
				if len(globalState.Outputs) == 0 {
					return "没有已编译的文章", nil
				}
				globalState.Outputs = compiler.consistencyReview(globalState.Task, globalState.Outputs)
				return fmt.Sprintf("已审查 %d 篇文章", len(globalState.Outputs)), nil
			}),

		ToolInfoToCallable("generate_index", usage("生成 INDEX.md 索引文件。"),
			func(ctx context.Context, argsJSON string) (string, error) {
				index := generateDomainIndex(nil, globalState.Outputs)
				globalState.Outputs = append(globalState.Outputs, index)
				return fmt.Sprintf("已生成 INDEX.md (%d chars)", len(index.Content)), nil
			}),

		ToolInfoToCallable("generate_log", usage("生成 log.md 编译日志。"),
			func(ctx context.Context, argsJSON string) (string, error) {
				log := compiler.generateLog(globalState.Task, globalState.Topics, len(globalState.Files), "synthesis")
				globalState.Outputs = append(globalState.Outputs, log)
				return "已生成 log.md", nil
			}),

		ToolInfoToCallable("quality_review", usage("质量审查：统计链接、孤立文章、覆盖度。"),
			func(ctx context.Context, argsJSON string) (string, error) {
				globalState.Outputs = compiler.qualityReview(globalState.Task, globalState.Topics, globalState.Outputs)
				return fmt.Sprintf("已审查 %d 篇文章", len(globalState.Outputs)), nil
			}),

		ToolInfoToCallable("write_output", usage("保存编译结果。"),
			func(ctx context.Context, argsJSON string) (string, error) {
				return fmt.Sprintf("输出 %d 个文件:\n%s", len(globalState.Outputs), outputPaths(globalState.Outputs)), nil
			}),

		ToolInfoToCallable("search_files", usage("在已加载文件中搜索关键词。需要 query。"),
			func(ctx context.Context, argsJSON string) (string, error) {
				var args struct {
					Query string `json:"query"`
				}
				if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
					return "", fmt.Errorf("parse args: %w", err)
				}
				var matches []string
				for _, f := range globalState.Files {
					if strings.Contains(f.Content, args.Query) {
						matches = append(matches, f.Path)
					}
				}
				if len(matches) == 0 {
					return "未找到匹配", nil
				}
				return fmt.Sprintf("找到 %d 个:\n%s", len(matches), strings.Join(matches, "\n")), nil
			}),
	}
}

// buildSystemPrompt constructs the agent's system prompt from SKILL.md.
func buildSystemPrompt(cfg *AgentConfig) string {
	var b strings.Builder
	b.WriteString("你是一个知识编译 Agent。你的工作流程由以下 SKILL.md 定义：\n\n")

	if cfg.SkillContent != "" {
		b.WriteString(cfg.SkillContent)
	} else {
		b.WriteString("[无已加载技能]\n")
	}

	b.WriteString("\n\n## 工作方式\n\n")
	b.WriteString("1. 调用 load_files 加载工作区\n")
	b.WriteString("2. 调用 scan_topics 发现知识主题\n")
	b.WriteString("3. 对每个主题调用 compile_topic 编译文章\n")
	b.WriteString("4. 调用 generate_index 和 generate_log\n")
	b.WriteString("5. 调用 write_output 保存结果\n")

	return b.String()
}

// StartAgentCompile starts a skill-driven agent compilation.
func (c *Compiler) StartAgentCompile(ctx context.Context, input *CompileInput) (*TaskState, error) {
	taskID := c.entityID()
	task := &TaskState{
		ID:        taskID,
		Status:    "running",
		CreatedAt: time.Now(),
	}
	c.tasksMu.Lock()
	c.tasks[taskID] = task
	c.tasksMu.Unlock()

	go func() {
		_, skills := c.loadPhase(task, input)
		var skillContent string
		for _, s := range skills {
			skillContent += fmt.Sprintf("### Skill: %s\n\n%s\n\n", s.Name, s.Content)
		}

		cfg := &AgentConfig{
			LLM:          c.llm,
			WorkspaceID:  input.WorkspaceID,
			SkillContent: skillContent,
			Instructions: input.Instructions,
			OutputDir:    input.OutputDir,
		}

		result, err := RunAgentLoop(ctx, cfg)
		if err != nil {
			task.SetStatus("failed")
			task.Error = err.Error()
			task.FinishedAt = time.Now()
			return
		}

		task.AppendLog("%s", result.Log)

		for _, o := range result.Outputs {
			task.AppendLog("[OUTPUT] %s\n", o.Path)
		}
		task.Result = &CompileResult{
			TaskID:       taskID,
			FilesCreated: len(result.Outputs),
		}
		task.SetStatus("success")
		task.FinishedAt = time.Now()
	}()

	return task, nil
}

// outputPaths returns the paths of all output files.
func outputPaths(outputs []OutputFile) string {
	var paths []string
	for _, o := range outputs {
		paths = append(paths, o.Path)
	}
	return strings.Join(paths, "\n")
}
