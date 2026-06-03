package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"llmwiki/backend/entity"
	"llmwiki/backend/service"
)

// CompileInput is the input to the compilation pipeline.
type CompileInput struct {
	WorkspaceID  string
	TenantID     string
	Actor        string
	Instructions string
	SkillRefs    []string
	OutputDir    string
	CommitMsg    string
}

// CompileResult is the output of the compilation pipeline.
type CompileResult struct {
	TaskID       string
	CommitID     string
	FilesCreated int
	ErrorMessage string
}

// OutputFile represents a single output file to create.
type OutputFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// LLMResult contains the structured output from a single LLM call.
type LLMResult struct {
	Files []OutputFile `json:"files"`
}

// FileNode represents a file in the workspace tree.
type FileNode struct {
	ID      string
	Name    string
	Path    string
	Content string
	Summary string
	Title   string
	Keywords []string
}

// SkillDef represents a loaded skill definition.
type SkillDef struct {
	Name    string
	Content string
}

// TopicInfo from Phase 1 scan.
type TopicInfo struct {
	Name        string   `json:"name"`
	SourcePaths []string `json:"source_paths"`
	Description string   `json:"description"`
}

// ConceptInfo from concept discovery phase.
type ConceptInfo struct {
	Name        string   `json:"name"`
	Slug        string   `json:"slug"`
	Description string   `json:"description"`
	Topics      []string `json:"topics"`
}

// QualityReport from review phase.
type QualityReport struct {
	Passed           bool     `json:"passed"`
	Issues           []string `json:"issues"`
	ArticleCount     int      `json:"article_count"`
	TotalLinks       int      `json:"total_links"`
	OrphanArticles   []string `json:"orphan_articles"`
	LowCoverageCount int      `json:"low_coverage_count"`
}

// TaskState tracks a running/tracked compilation task.
type TaskState struct {
	ID         string
	Status     string
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	Log        string
	Result     *CompileResult
	Error      string
	mu         sync.RWMutex
}

func (t *TaskState) AppendLog(format string, args ...interface{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Log += fmt.Sprintf(format, args...)
}

func (t *TaskState) SetStatus(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Status = s
}

func (t *TaskState) GetStatus() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status
}

func (t *TaskState) GetLog() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Log
}

// Compiler is the knowledge compilation agent.
type Compiler struct {
	fileSvc  *service.FileService
	llm      *LLMClient
	tasks    map[string]*TaskState
	tasksMu  sync.RWMutex
	entityID func() string
}

func NewCompiler(fileSvc *service.FileService, llm *LLMClient) *Compiler {
	return &Compiler{
		fileSvc:  fileSvc,
		llm:      llm,
		tasks:    make(map[string]*TaskState),
		entityID: entity.NewID,
	}
}

func (c *Compiler) StartCompile(ctx context.Context, input *CompileInput) (*TaskState, error) {
	taskID := c.entityID()
	task := &TaskState{
		ID:        taskID,
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	c.tasksMu.Lock()
	c.tasks[taskID] = task
	c.tasksMu.Unlock()

	go c.runCompile(task, input)
	return task, nil
}

func (c *Compiler) GetTask(taskID string) *TaskState {
	c.tasksMu.RLock()
	defer c.tasksMu.RUnlock()
	return c.tasks[taskID]
}

func (c *Compiler) ListTasks() []*TaskState {
	c.tasksMu.RLock()
	defer c.tasksMu.RUnlock()
	result := make([]*TaskState, 0, len(c.tasks))
	for _, t := range c.tasks {
		result = append(result, t)
	}
	return result
}

// ========== Main Pipeline ==========

func (c *Compiler) runCompile(task *TaskState, input *CompileInput) {
	task.SetStatus("running")
	task.StartedAt = time.Now()
	task.AppendLog("[TASK] ===== Multi-Phase Compilation =====\n")
	task.AppendLog("[TASK] Workspace: %s\n", input.WorkspaceID)
	task.AppendLog("[TASK] Output: %s\n", input.OutputDir)
	task.AppendLog("[TASK] Skills: %v\n", input.SkillRefs)
	task.AppendLog("[TASK] Instructions: %s\n\n", input.Instructions)

	c.llm.SetLogCallback(func(format string, args ...interface{}) {
		task.AppendLog(format, args...)
	})

	outputDir := input.OutputDir
	if outputDir == "" {
		outputDir = "synthesis"
	}

	// P4: Batch-load files (if >20, process in batches for keyword extraction)
	files, skills := c.loadPhase(task, input)
	if len(files) == 0 {
		return
	}

	// P1: Keyword extraction for each file
	c.extractKeywords(task, files)

	// P2: Discover topics (uses keyword-enriched file info)
	topics := c.scanPhase(task, files, skills, input.Instructions)
	if len(topics) == 0 {
		return
	}
	task.AppendLog("\n[SCAN] Discovered %d topics:\n", len(topics))
	for _, t := range topics {
		task.AppendLog("  - %s (%d sources): %s\n", t.Name, len(t.SourcePaths), t.Description)
	}

	// P3: Compile each topic article with:
	//   - cross-topic context (previously compiled articles)
	//   - on-demand file reading (tool calling)
	var allOutputs []OutputFile
	compileStart := time.Now()
	for i, topic := range topics {
		tStart := time.Now()
		article := c.compilePhase(task, topic, files, topics, allOutputs, outputDir)
		elapsed := time.Since(tStart).Round(time.Second)
		if article != nil {
			allOutputs = append(allOutputs, *article)
		}
		remaining := len(topics) - i - 1
		avgTime := time.Since(compileStart) / time.Duration(i+1)
		eta := avgTime * time.Duration(remaining)
		task.AppendLog("[COMPILE] Topic %d/%d: '%s' (%v, ETA %v)\n",
			i+1, len(topics), topic.Name, elapsed, eta.Round(time.Second))
	}
	compileTotal := time.Since(compileStart).Round(time.Second)
	task.AppendLog("[COMPILE] All %d topics compiled in %v\n", len(topics), compileTotal)

	// P4: Post-compilation consistency review — re-read & fix overlap
	task.AppendLog("[REVIEW] Consistency check across %d articles...\n", len(allOutputs))
	allOutputs = c.consistencyReview(task, allOutputs)

	// P5: Concept discovery — find cross-topic patterns
	concepts := c.conceptPhase(task, topics, allOutputs)
	for _, concept := range concepts {
		allOutputs = append(allOutputs, concept)
	}

	// P6: INDEX.md + log.md
	allOutputs = append(allOutputs,
		c.generateIndex(task, topics, len(files), outputDir),
		c.generateLog(task, topics, len(files), outputDir))

	// P7: Quality review (P3)
	allOutputs = c.qualityReview(task, topics, allOutputs)

	// Write + commit
	created, outputWkspID, err := c.writeOutputFiles(task, input, outputDir, allOutputs)
	if err != nil {
		task.AppendLog("[ERROR] Write output: %v\n", err)
		task.Error = err.Error()
		task.SetStatus("failed")
		task.FinishedAt = time.Now()
		return
	}
	task.AppendLog("[OUTPUT] Created %d files\n", len(created))

	commitID, err := c.commit(input, outputWkspID)
	if err != nil {
		task.AppendLog("[COMMIT] Warning: %v\n", err)
	} else {
		task.AppendLog("[COMMIT] ID: %s\n", commitID)
	}

	task.AppendLog("[TASK] ===== Complete =====\n")
	task.Result = &CompileResult{FilesCreated: len(created), CommitID: commitID}
	task.SetStatus("success")
	task.FinishedAt = time.Now()
}

// ========== P4: Batch Loading ==========

func (c *Compiler) loadPhase(task *TaskState, input *CompileInput) ([]FileNode, []SkillDef) {
	task.AppendLog("[LOAD] Loading workspace files...\n")
	files, err := c.loadWorkspaceFiles(input.WorkspaceID)
	if err != nil {
		task.AppendLog("[ERROR] Load files: %v\n", err)
		task.Error = err.Error()
		task.SetStatus("failed")
		task.FinishedAt = time.Now()
		return nil, nil
	}
	task.AppendLog("[LOAD] %d files loaded\n", len(files))

	// P4: If >20 files, show batching info
	if len(files) > 20 {
		task.AppendLog("[LOAD] Large workspace (%d files), using batch processing\n", len(files))
	}

	task.AppendLog("[LOAD] Loading skills...\n")
	skills, err := c.loadSkills(input.WorkspaceID, input.SkillRefs)
	if err != nil {
		task.AppendLog("[WARN] Load skills: %v\n", err)
	}
	task.AppendLog("[LOAD] %d skills loaded\n\n", len(skills))
	return files, skills
}

// ========== P1: Keyword Extraction ==========

func (c *Compiler) extractKeywords(task *TaskState, files []FileNode) {
	task.AppendLog("[EXTRACT] Extracting keywords from %d files...\n", len(files))

	// P4: Batch processing — process files in groups of 10
	batchSize := 10
	for start := 0; start < len(files); start += batchSize {
		end := start + batchSize
		if end > len(files) {
			end = len(files)
		}
		batch := files[start:end]

		var b strings.Builder
		b.WriteString("从以下文件中提取关键词（每个文件3-5个关键词）。输出JSON格式：\n")

		type keywordReq struct {
			Path     string `json:"path"`
			Title    string `json:"title"`
			First300 string `json:"first_300_chars"`
		}
		var reqs []keywordReq
		for _, f := range batch {
			summary := f.Content
			if len(summary) > 300 {
				summary = summary[:300]
			}
			title := ""
			for _, line := range strings.Split(f.Content, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "# ") {
					title = strings.TrimPrefix(strings.TrimSpace(line), "# ")
					break
				}
			}
			reqs = append(reqs, keywordReq{
				Path:     f.Path,
				Title:    title,
				First300: summary,
			})
		}
		data, _ := json.Marshal(reqs)
		b.Write(data)
		b.WriteString("\n\n输出格式: [{\"path\": \"file.md\", \"keywords\": [\"kw1\",\"kw2\"]}]")

		systemMsg := "你是一个文档分析专家。对每个文件提取3-5个最能代表内容的关键词。"

		kwCtx, kwCancel := context.WithTimeout(context.Background(), 90*time.Second)
		content, err := c.llm.ChatRaw(kwCtx, systemMsg, b.String())
		kwCancel()
		if err != nil {
			task.AppendLog("[EXTRACT] Batch %d error: %v\n", start/batchSize, err)
			continue
		}

		var results []struct {
			Path     string   `json:"path"`
			Keywords []string `json:"keywords"`
		}
		if err := json.Unmarshal([]byte(content), &results); err != nil {
			task.AppendLog("[EXTRACT] Parse error for batch %d\n", start/batchSize)
			continue
		}

		kwMap := make(map[string][]string)
		for _, r := range results {
			kwMap[r.Path] = r.Keywords
		}
		for i := range files {
			if kws, ok := kwMap[files[i].Path]; ok {
				files[i].Keywords = kws
			}
		}
		task.AppendLog("[EXTRACT] Batch %d: %d files processed\n", start/batchSize+1, len(results))
	}

	// Fill in titles and summaries for files where we can
	for i := range files {
		if files[i].Summary == "" {
			s := files[i].Content
			if len(s) > 500 {
				s = s[:500]
			}
			files[i].Summary = s
		}
		if files[i].Title == "" {
			for _, line := range strings.Split(files[i].Content, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "# ") {
					files[i].Title = strings.TrimPrefix(strings.TrimSpace(line), "# ")
					break
				}
			}
		}
	}
}

// ========== P1+P4: Batch Scan (handles large workspaces) ==========

const scanBatchSize = 25

func (c *Compiler) scanPhase(task *TaskState, files []FileNode, skills []SkillDef, instructions string) []TopicInfo {
	task.AppendLog("[SCAN] Analyzing %d files to discover topics...\n", len(files))

	if len(files) <= scanBatchSize {
		return c.scanBatch(task, files, instructions)
	}

	// Large workspace: scan in parallel sub-batches, then merge
	task.AppendLog("[SCAN] Large workspace: scanning %d files in parallel batches of %d\n", len(files), scanBatchSize)

	type batchResult struct {
		index  int
		topics []TopicInfo
	}

	numBatches := (len(files) + scanBatchSize - 1) / scanBatchSize
	resultCh := make(chan batchResult, numBatches)

	for start := 0; start < len(files); start += scanBatchSize {
		end := start + scanBatchSize
		if end > len(files) {
			end = len(files)
		}
		batch := files[start:end]
		batchNum := start / scanBatchSize

		go func(idx int, bf []FileNode) {
			task.AppendLog("[SCAN] Sub-agent %d: scanning files %d-%d...\n", idx+1, idx*scanBatchSize+1, idx*scanBatchSize+len(bf))
			topics := c.scanBatch(task, bf, instructions)
			resultCh <- batchResult{index: idx, topics: topics}
		}(batchNum, batch)
	}

	var allCandidates []TopicInfo
	for i := 0; i < numBatches; i++ {
		res := <-resultCh
		if len(res.topics) > 0 {
			task.AppendLog("[SCAN] Sub-agent %d: found %d candidate topics\n", res.index+1, len(res.topics))
			allCandidates = append(allCandidates, res.topics...)
		} else {
			task.AppendLog("[SCAN] Sub-agent %d: no topics found\n", res.index+1)
		}
	}
	close(resultCh)

	if len(allCandidates) == 0 {
		task.AppendLog("[SCAN] No candidates from batches, using fallback\n")
		return c.fallbackTopics(files)
	}

	// Merge candidates: send all candidate topics to LLM for dedup + consolidation
	task.AppendLog("[SCAN] Merging %d candidate topics...\n", len(allCandidates))
	return c.mergeTopics(task, allCandidates, instructions)
}

// scanBatch scans a single batch of files for topics with timeout and retry.
func (c *Compiler) scanBatch(task *TaskState, files []FileNode, instructions string) []TopicInfo {
	var b strings.Builder
	b.WriteString("分析以下源文件，发现其中的知识主题。\n\n")
	b.WriteString("规则：\n")
	b.WriteString("1. 共享关键词的文件归为同一主题\n")
	b.WriteString("2. 一个文件可能属于多个主题\n")
	b.WriteString("3. 主题名用 kebab-case\n\n")

	b.WriteString("文件列表：\n\n")
	for _, f := range files {
		kw := strings.Join(f.Keywords, ", ")
		if kw == "" {
			kw = "(未提取)"
		}
		summary := f.Content
		if len(summary) > 500 {
			summary = summary[:500] + "..."
		}
		b.WriteString(fmt.Sprintf("### %s\n", f.Path))
		b.WriteString(fmt.Sprintf("- 标题: %s | 关键词: %s\n", f.Title, kw))
		b.WriteString(fmt.Sprintf("- 摘要:\n```\n%s\n```\n\n", summary))
	}

	if instructions != "" {
		b.WriteString(fmt.Sprintf("## 用户指令\n\n%s\n", instructions))
	}

	b.WriteString(`输出JSON: {"topics": [{"name": "slug", "source_paths": ["file.md"], "description": "描述"}]}`)

	systemMsg := "你是一个信息架构师。分析关键词和摘要来发现知识主题。"

	// Call with timeout and retry
	const batchTimeout = 90 * time.Second
	const maxRetries = 2

	var content string
	var err error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			task.AppendLog("[RETRY] scanBatch attempt %d\n", attempt+1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), batchTimeout)
		content, err = c.llm.ChatRaw(ctx, systemMsg, b.String())
		cancel()
		if err == nil {
			break
		}
		if attempt < maxRetries-1 {
			time.Sleep(2 * time.Second)
		}
	}
	if err != nil {
		task.AppendLog("[SCAN] Batch failed after %d attempts: %v\n", maxRetries, err)
		return nil
	}

	// Strip possible ```json fence
	content = stripJSONFence(content)

	var result struct {
		Topics []TopicInfo `json:"topics"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		task.AppendLog("[SCAN] JSON parse error, first 200 chars: %s\n", truncate(content, 200))
		return nil
	}
	return result.Topics
}

// mergeTopics merges candidate topics from multiple batches.
func (c *Compiler) mergeTopics(task *TaskState, candidates []TopicInfo, instructions string) []TopicInfo {
	var b strings.Builder
	b.WriteString("以下是多个批次中发现的主题候选。合并去重：\n\n")
	b.WriteString("规则：\n")
	b.WriteString("1. 名称相似的主题合并为一个\n")
	b.WriteString("2. 同一个文件在不同批次中出现 → 归到合并后的主题\n")
	b.WriteString("3. source_paths 汇总所有关联文件\n\n")

	for i, t := range candidates {
		b.WriteString(fmt.Sprintf("%d. 主题「%s」: %s (文件: %s)\n",
			i+1, t.Name, t.Description, strings.Join(t.SourcePaths, ", ")))
	}

	if instructions != "" {
		b.WriteString(fmt.Sprintf("\n用户指令: %s\n", instructions))
	}

	b.WriteString(`\n输出JSON: {"topics": [{"name": "merged-slug", "source_paths": ["file.md"], "description": "合并后描述"}]}`)

	systemMsg := "你是一个信息架构师。合并去重主题候选列表。"

	mergeCtx, mergeCancel := context.WithTimeout(context.Background(), 90*time.Second)
	content, err := c.llm.ChatRaw(mergeCtx, systemMsg, b.String())
	mergeCancel()
	if err != nil {
		task.AppendLog("[MERGE] LLM error: %v, using candidates directly\n", err)
		return candidates
	}

	var result struct {
		Topics []TopicInfo `json:"topics"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil || len(result.Topics) == 0 {
		task.AppendLog("[MERGE] Parse error, using candidates directly\n")
		return candidates
	}

	// Preserve source_paths from candidates for any merged topic that lost them
	candidateMap := make(map[string][]string)
	for _, c := range candidates {
		candidateMap[c.Name] = c.SourcePaths
	}
	for i := range result.Topics {
		if len(result.Topics[i].SourcePaths) == 0 {
			if paths, ok := candidateMap[result.Topics[i].Name]; ok {
				result.Topics[i].SourcePaths = paths
			}
		}
	}

	return result.Topics
}

func (c *Compiler) fallbackTopics(files []FileNode) []TopicInfo {
	groups := make(map[string][]string)
	for _, f := range files {
		dir := "root"
		if idx := strings.LastIndex(f.Path, "/"); idx > 0 {
			dir = f.Path[:idx]
		}
		groups[dir] = append(groups[dir], f.Path)
	}
	var topics []TopicInfo
	for dir, paths := range groups {
		name := strings.ReplaceAll(dir, "/", "-")
		topics = append(topics, TopicInfo{Name: name, SourcePaths: paths, Description: "Files in " + dir})
	}
	return topics
}

// ========== P0: Compile with Cross-Topic Context + On-Demand File Reading ==========

func (c *Compiler) compilePhase(task *TaskState, topic TopicInfo, allFiles []FileNode, allTopics []TopicInfo, compiled []OutputFile, outputDir string) *OutputFile {
	task.AppendLog("[COMPILE] Topic '%s' (%d sources)...\n", topic.Name, len(topic.SourcePaths))

	// Build other-topics context for cross-linking
	var otherCtx strings.Builder
	otherCtx.WriteString("其他已发现主题供交叉引用：\n")
	for _, t := range allTopics {
		if t.Name != topic.Name {
			otherCtx.WriteString(fmt.Sprintf("- [[%s]]: %s\n", t.Name, t.Description))
		}
	}

	// Build already compiled articles context — tells the LLM what's been covered
	var prevCtx strings.Builder
	if len(compiled) > 0 {
		prevCtx.WriteString("\n## 已编译的文章（防止内容重复）\n\n")
		for _, art := range compiled {
			summary := art.Content
			if len(summary) > 500 {
				summary = summary[:500] + "..."
			}
			prevCtx.WriteString(fmt.Sprintf("### %s\n```\n%s\n```\n\n", art.Path, summary))
		}
		prevCtx.WriteString("注意：以上文章已经存在。新文章的内容不得与这些文章重复。\n")
		prevCtx.WriteString("如果发现你的主题与已编译文章重叠，应当引用 [[ExistingArticle]] 而非重写。\n\n")
	}

	// Collect relevant source files for this topic
	var relevantFiles []FileNode
	topicPaths := make(map[string]bool)
	for _, p := range topic.SourcePaths {
		topicPaths[p] = true
	}
	for _, f := range allFiles {
		if topicPaths[f.Path] {
			relevantFiles = append(relevantFiles, f)
		}
	}

	// Build the prompt with all context
	prompt := c.buildCompilePrompt(topic, relevantFiles, allFiles, otherCtx.String(), prevCtx.String())

	// Call with retry + on-demand file reading
	const maxRounds = 3 // max tool-calling rounds
	for round := 0; round < maxRounds; round++ {
		content := c.chatWithRetry(task, prompt)

		if content == "" {
			return nil
		}
		content = stripJSONFence(content)

		// Check for on-demand file requests (tool calling)
		if readFile := extractReadRequest(content); readFile != "" {
			task.AppendLog("[TOOL] LLM requested additional file: %s\n", readFile)
			fileContent := c.readFileByName(allFiles, readFile)
			if fileContent != "" {
				prompt += fmt.Sprintf("\n---\nLLM requested additional context from `%s`:\n```\n%s\n```\n请基于此补充内容后输出完整文章。\n", readFile, fileContent)
				continue
			}
		}

		// Check for overlap warning marker
		if strings.Contains(content, "[OVERLAP]") {
			task.AppendLog("[TOOL] LLM detected overlap, adjusting...\n")
			prompt += "\n注意：上轮输出包含重叠内容标记。请去掉与已发表文章重复的部分，用 [[链接]] 替代。重新输出完整文章。\n"
			continue
		}

		return &OutputFile{Path: topic.Name + ".md", Content: content}
	}

	task.AppendLog("[COMPILE] Max rounds reached, using last output\n")
	return nil
}

// buildCompilePrompt assembles the compilation prompt with all context.
func (c *Compiler) buildCompilePrompt(topic TopicInfo, relevantFiles, allFiles []FileNode, otherCtx, prevCtx string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("编译主题「%s」的知识文章。\n\n", topic.Description))
	b.WriteString(otherCtx)
	b.WriteString(prevCtx)
	b.WriteString(fmt.Sprintf("主题描述：%s\n\n", topic.Description))

	// Full content of topic's source files
	for _, f := range relevantFiles {
		b.WriteString(fmt.Sprintf("### %s\n```\n%s\n```\n\n", f.Path, f.Content))
	}

	// File index for on-demand reading (tool calling)
	b.WriteString("\n## 其他源文件索引\n\n")
	b.WriteString("以下文件未包含在上述内容中。如果你需要更多上下文，可以在输出中包含标记：\n")
	b.WriteString("[READ: filename.md]\n")
	b.WriteString("系统会自动读取该文件并重新调用你。\n\n")
	for _, f := range allFiles {
		if !topicPaths(f, relevantFiles) {
			kw := strings.Join(f.Keywords, ", ")
			if kw == "" {
				kw = "(无)"
			}
			b.WriteString(fmt.Sprintf("- %s [关键词: %s]\n", f.Path, kw))
		}
	}

	return b.String()
}

func topicPaths(f FileNode, relevant []FileNode) bool {
	for _, r := range relevant {
		if r.Path == f.Path {
			return true
		}
	}
	return false
}

// extractReadRequest checks if the response contains a [READ: path] marker.
func extractReadRequest(content string) string {
	idx := strings.Index(content, "[READ:")
	if idx < 0 {
		return ""
	}
	end := strings.Index(content[idx:], "]")
	if end < 0 {
		return ""
	}
	path := strings.TrimSpace(content[idx+6 : idx+end])
	if path == "" {
		return ""
	}
	return path
}

// readFileByName finds a file by path and returns its content.
func (c *Compiler) readFileByName(files []FileNode, path string) string {
	for _, f := range files {
		if f.Path == path || f.Name == path {
			return f.Content
		}
	}
	return ""
}

// chatWithRetry sends a prompt to the LLM with timeout and retry.
func (c *Compiler) chatWithRetry(task *TaskState, prompt string) string {
	const timeout = 120 * time.Second
	const retries = 2
	for attempt := 0; attempt < retries; attempt++ {
		if attempt > 0 {
			task.AppendLog("[RETRY] Chat attempt %d\n", attempt+1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		content, err := c.llm.ChatRaw(ctx, "", prompt)
		cancel()
		if err == nil {
			return content
		}
		task.AppendLog("[WARN] LLM error: %v\n", err)
		if attempt < retries-1 {
			time.Sleep(2 * time.Second)
		}
	}
	return ""
}

// ========== Post-Compilation Consistency Review ==========

func (c *Compiler) consistencyReview(task *TaskState, articles []OutputFile) []OutputFile {
	if len(articles) < 2 {
		return articles
	}

	var b strings.Builder
	b.WriteString("审查以下已编译的知识文章，找出并修复以下问题：\n\n")
	b.WriteString("1. **内容重叠**：同一主题在不同文章中被重复描述。应保留一篇、其余用[[链接]]替代。\n")
	b.WriteString("2. **交叉引用缺失**：相关主题之间缺少[[链接]]。\n")
	b.WriteString("3. **不一致的术语**：同一概念在不同文章中用不同名称。\n\n")

	for _, art := range articles {
		summary := art.Content
		if len(summary) > 800 {
			summary = summary[:800] + "..."
		}
		b.WriteString(fmt.Sprintf("### %s\n```\n%s\n```\n\n", art.Path, summary))
	}

	b.WriteString("\n对每个有问题的文章，输出如下JSON（只需修改有问题的文章）：\n")
	b.WriteString(`{"fixes": [{"path": "article.md", "content": "修正后的完整文章内容"}]}`)
	b.WriteString("\n\n不要把同一段内容挪来挪去。如果两篇文章讲同一个东西，留下一篇，另一篇加[[链接]]指向它。")

	content := c.chatWithRetry(task, b.String())
	if content == "" {
		return articles
	}

	var result struct {
		Fixes []OutputFile `json:"fixes"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil || len(result.Fixes) == 0 {
		task.AppendLog("[REVIEW] No fixes needed\n")
		return articles
	}

	task.AppendLog("[REVIEW] Applied %d fixes\n", len(result.Fixes))
	fixMap := make(map[string]string)
	for _, fix := range result.Fixes {
		fixMap[fix.Path] = fix.Content
	}
	for i, art := range articles {
		if fix, ok := fixMap[art.Path]; ok {
			articles[i].Content = fix
		}
	}
	return articles
}

// stripJSONFence removes ```json ... ``` wrapping if present.
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		lines := strings.SplitN(s, "\n", 2)
		if len(lines) == 2 {
			s = lines[1]
		}
	}
	if strings.HasSuffix(s, "```") {
		idx := strings.LastIndex(s, "```")
		if idx >= 0 {
			s = s[:idx]
		}
	}
	return strings.TrimSpace(s)
}

// ========== P2: Concept Discovery ==========

func (c *Compiler) conceptPhase(task *TaskState, topics []TopicInfo, articles []OutputFile) []OutputFile {
	if len(articles) < 3 {
		return nil // Need at least 3 articles to find cross-cutting patterns
	}

	task.AppendLog("[CONCEPT] Analyzing %d articles for cross-topic concepts...\n", len(articles))

	var b strings.Builder
	b.WriteString("分析以下已编译的知识文章，发现跨主题的概念和模式。\n\n")
	b.WriteString("概念是指出现在3个以上主题中的跨领域模式，例如：\n")
	b.WriteString("- 多个主题共同使用的架构模式\n")
	b.WriteString("- 跨主题的决策原则\n")
	b.WriteString("- 重复出现的工程模式\n\n")

	b.WriteString("主题列表：\n")
	for _, t := range topics {
		b.WriteString(fmt.Sprintf("- [[%s]]: %s\n", t.Name, t.Description))
	}
	b.WriteString("\n文章摘要：\n")
	for _, a := range articles {
		summary := a.Content
		if len(summary) > 300 {
			summary = summary[:300]
		}
		b.WriteString(fmt.Sprintf("### %s\n```\n%s\n```\n\n", a.Path, summary))
	}

	b.WriteString("\n输出JSON格式:\n")
	b.WriteString(`{"concepts": [{"name": "概念名", "slug": "concept-slug", "description": "描述", "topics": ["topic1","topic2"]}]}`)

	systemMsg := "你是一个知识发现专家。找出跨主题的共现模式。只在确实存在3个以上相关性时创建概念。"

	content, err := c.llm.ChatRaw(context.Background(), systemMsg, b.String())
	if err != nil {
		task.AppendLog("[CONCEPT] LLM error: %v\n", err)
		return nil
	}

	var result struct {
		Concepts []struct {
			Name        string   `json:"name"`
			Slug        string   `json:"slug"`
			Description string   `json:"description"`
			Topics      []string `json:"topics"`
		} `json:"concepts"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil || len(result.Concepts) == 0 {
		task.AppendLog("[CONCEPT] No cross-topic patterns found\n")
		return nil
	}

	task.AppendLog("[CONCEPT] Found %d concepts:\n", len(result.Concepts))
	var outputs []OutputFile
	for _, cpt := range result.Concepts {
		task.AppendLog("  - [[%s]]: %s\n", cpt.Slug, cpt.Description)

		body := fmt.Sprintf("# %s\n\n## 概述\n%s\n\n## 关联主题\n", cpt.Name, cpt.Description)
		for _, t := range cpt.Topics {
			body += fmt.Sprintf("- [[%s]]\n", t)
		}
		body += "\n_由概念发现阶段自动生成_"

		outputs = append(outputs, OutputFile{
			Path:    cpt.Slug + ".md",
			Content: body,
		})
	}
	return outputs
}

// ========== P3: Quality Review ==========

func (c *Compiler) qualityReview(task *TaskState, topics []TopicInfo, articles []OutputFile) []OutputFile {
	if len(articles) < 2 {
		return articles
	}

	task.AppendLog("[REVIEW] Performing quality review...\n")

	// Count links in each article
	totalLinks := 0
	var orphans []string
	articleNames := make(map[string]bool)
	for _, a := range articles {
		name := strings.TrimSuffix(a.Path, ".md")
		articleNames[name] = true
	}

	for _, a := range articles {
		links := countWikiLinks(a.Content)
		totalLinks += links
		// Check if this article has any outward links
		name := strings.TrimSuffix(a.Path, ".md")
		hasOutbound := false
		for _, other := range articles {
			if other.Path == a.Path {
				continue
			}
			otherName := strings.TrimSuffix(other.Path, ".md")
			if strings.Contains(a.Content, "[["+otherName+"]]") {
				hasOutbound = true
				break
			}
		}
		if !hasOutbound && len(articles) > 1 {
			orphans = append(orphans, name)
		}
	}

	lowCoverage := 0
	for _, a := range articles {
		if strings.Contains(a.Content, "[coverage: low]") || strings.Contains(a.Content, "[coverage:low]") {
			lowCoverage++
		}
	}

	issues := []string{}
	if len(orphans) > 0 {
		issues = append(issues, fmt.Sprintf("孤立文章(无出站链接): %s", strings.Join(orphans, ", ")))
	}
	if lowCoverage > 0 {
		issues = append(issues, fmt.Sprintf("%d 篇文章标记了低覆盖度", lowCoverage))
	}
	if totalLinks == 0 {
		issues = append(issues, "全篇无 [[wiki链接]]")
	}

	if len(issues) > 0 {
		task.AppendLog("[REVIEW] Issues found:\n")
		for _, issue := range issues {
			task.AppendLog("  ⚠ %s\n", issue)
		}

		// Try to fix orphans by adding links
		if len(orphans) > 0 {
			task.AppendLog("[REVIEW] Attempting to add missing links...\n")
			articles = c.fixOrphans(task, articles, orphans, topics)
		}
	} else {
		task.AppendLog("[REVIEW] All checks passed\n")
	}

	return articles
}

func (c *Compiler) fixOrphans(task *TaskState, articles []OutputFile, orphans []string, topics []TopicInfo) []OutputFile {
	orphanSet := make(map[string]bool)
	for _, o := range orphans {
		orphanSet[o] = true
	}

	for i, a := range articles {
		name := strings.TrimSuffix(a.Path, ".md")
		if !orphanSet[name] {
			continue
		}

		var b strings.Builder
		b.WriteString(fmt.Sprintf("文章「%s」缺少指向其他相关文章的 [[链接]]。\n\n", name))
		b.WriteString("以下是可引用的其他文章列表:\n")
		for _, t := range topics {
			if t.Name != name {
				b.WriteString(fmt.Sprintf("- [[%s]]: %s\n", t.Name, t.Description))
			}
		}
		b.WriteString("\n文章当前内容:\n```\n")
		if len(a.Content) > 2000 {
			b.WriteString(a.Content[:2000] + "...\n")
		} else {
			b.WriteString(a.Content)
		}
		b.WriteString("\n```\n")
		b.WriteString("\n请在原文适当位置插入2-3个[[链接]]。只输出修改后的完整文章内容。")

		systemMsg := "你是一个知识库编辑。在文章正文中找到可以交叉引用的位置，插入[[链接]]。保持原文不变，只增加链接。"

		content, err := c.llm.ChatRaw(context.Background(), systemMsg, b.String())
		if err != nil {
			task.AppendLog("[REVIEW] Fix failed for '%s': %v\n", name, err)
			continue
		}
		articles[i].Content = content
		task.AppendLog("[REVIEW] Fixed links for '%s'\n", name)
	}
	return articles
}

func countWikiLinks(content string) int {
	count := 0
	for i := 0; i < len(content)-2; i++ {
		if content[i] == '[' && i+1 < len(content) && content[i+1] == '[' {
			count++
			i++
		}
	}
	return count
}

// ========== INDEX.md + log.md ==========

func (c *Compiler) generateIndex(task *TaskState, topics []TopicInfo, fileCount int, outputDir string) OutputFile {
	today := time.Now().Format("2006-01-02")
	var b strings.Builder
	b.WriteString("# Knowledge Base\n\n")
	b.WriteString(fmt.Sprintf("最后编译: %s\n", today))
	b.WriteString(fmt.Sprintf("主题数: %d | 源文件: %d\n\n", len(topics), fileCount))
	b.WriteString("## 主题\n\n| 主题 | 说明 | 来源 |\n")
	b.WriteString("|------|------|------|\n")
	for _, t := range topics {
		b.WriteString(fmt.Sprintf("| [[%s]] | %s | %d |\n", t.Name, t.Description, len(t.SourcePaths)))
	}
	b.WriteString("\n## 最近变更\n")
	b.WriteString(fmt.Sprintf("- %s: 知识编译\n", today))
	return OutputFile{Path: "INDEX.md", Content: b.String()}
}

func (c *Compiler) generateLog(task *TaskState, topics []TopicInfo, fileCount int, outputDir string) OutputFile {
	today := time.Now().Format("2006-01-02")
	var names []string
	for _, t := range topics {
		names = append(names, t.Name)
	}
	content := fmt.Sprintf("## %s\n\n**创建的主题:** %s\n**处理的源文件:** %d\n",
		today, strings.Join(names, ", "), fileCount)
	return OutputFile{Path: "log.md", Content: content}
}

// ========== File & Skills Loading ==========

func (c *Compiler) loadWorkspaceFiles(workspaceID string) ([]FileNode, error) {
	tree, err := c.fileSvc.GetCurrentTree(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("get tree: %w", err)
	}
	if tree == nil {
		return nil, fmt.Errorf("workspace not found")
	}
	var nodes []FileNode
	walkTree(tree, "", &nodes, c.fileSvc, workspaceID)
	return nodes, nil
}

func walkTree(node *entity.TreeNode, parentPath string, nodes *[]FileNode, svc *service.FileService, workspaceID string) {
	for i := range node.Children {
		child := node.Children[i]
		relPath := child.Name
		if parentPath != "" {
			relPath = parentPath + "/" + child.Name
		}
		if child.Type == "folder" {
			walkTree(&child, relPath, nodes, svc, workspaceID)
		} else {
			var content string
			if child.Location != nil && *child.Location != "" {
				data, err := svc.GetStorageData(workspaceID, *child.Location)
				if err == nil {
					content = string(data)
				}
			}
			if content == "" {
				content = "[empty]"
			}
			*nodes = append(*nodes, FileNode{ID: child.ID, Name: child.Name, Path: relPath, Content: content})
		}
	}
}

func (c *Compiler) loadSkills(workspaceID string, skillRefs []string) ([]SkillDef, error) {
	tree, err := c.fileSvc.GetCurrentTree(workspaceID)
	if err == nil && tree != nil {
		skillsFolderID := findFolderNamed(tree, ".knowledgebase")
		if skillsFolderID == "" {
			skillsFolderID = findFolderNamed(tree, "skills")
		}
		if skillsFolderID != "" {
			skillsTree, _ := c.fileSvc.GetCurrentTree(skillsFolderID)
			if skillsTree != nil {
				skills := collectSkills(skillsTree, workspaceID, skillRefs, c.fileSvc)
				if len(skills) > 0 {
					return skills, nil
				}
			}
		}
	}
	return loadLocalSkills()
}

func collectSkills(skillsTree *entity.TreeNode, workspaceID string, skillRefs []string, svc *service.FileService) []SkillDef {
	var skills []SkillDef
	match := func(name string) bool {
		if len(skillRefs) == 0 {
			return true
		}
		for _, ref := range skillRefs {
			if name == ref {
				return true
			}
		}
		return false
	}
	for _, child := range skillsTree.Children {
		if child.Type == "file" && match(child.Name) && child.Location != nil && *child.Location != "" {
			data, err := svc.GetStorageData(workspaceID, *child.Location)
			if err == nil {
				skills = append(skills, SkillDef{Name: child.Name, Content: string(data)})
			}
		}
	}
	return skills
}

func findFolderNamed(node *entity.TreeNode, name string) string {
	for _, child := range node.Children {
		if child.Type == "folder" && child.Name == name {
			return child.ID
		}
		if child.Type == "folder" {
			if id := findFolderNamed(&child, name); id != "" {
				return id
			}
		}
	}
	return ""
}

func loadLocalSkills() ([]SkillDef, error) {
	candidates := []string{"skills", "../skills", "skills/wiki-compiler", "../skills/wiki-compiler"}
	for _, dir := range candidates {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var skills []SkillDef
		for _, e := range entries {
			if !e.IsDir() && (strings.HasSuffix(e.Name(), ".md") || strings.HasSuffix(e.Name(), ".txt")) {
				data, err := os.ReadFile(filepath.Join(dir, e.Name()))
				if err != nil {
					continue
				}
				skills = append(skills, SkillDef{Name: e.Name(), Content: string(data)})
			}
		}
		if len(skills) > 0 {
			return skills, nil
		}
	}
	return nil, nil
}

// ========== Output Writing & Commit ==========

func (c *Compiler) writeOutputFiles(task *TaskState, input *CompileInput, outputDir string, files []OutputFile) ([]string, string, error) {
	srcWorkspace, err := c.fileSvc.GetFileByID(input.WorkspaceID)
	if err != nil {
		return nil, "", fmt.Errorf("get source workspace: %w", err)
	}
	rootFolder, err := c.fileSvc.GetOrCreateRootFolder(input.TenantID, input.Actor)
	if err != nil {
		return nil, "", fmt.Errorf("get root folder: %w", err)
	}

	outputName := srcWorkspace.Name + "-" + outputDir
	outputWS, err := c.fileSvc.CreateFolder(input.TenantID, rootFolder.ID, outputName, input.Actor)
	if err != nil {
		task.AppendLog("[OUTPUT] Folder '%s' may already exist\n", outputName)
		rootTree, _ := c.fileSvc.GetCurrentTree(rootFolder.ID)
		outputWS = findChildFolderByName(rootTree, outputName)
	}
	if outputWS == nil {
		return nil, "", fmt.Errorf("cannot create output workspace '%s'", outputName)
	}
	task.AppendLog("[OUTPUT] Workspace: '%s'\n", outputName)

	var created []string
	for _, f := range files {
		name := strings.TrimPrefix(f.Path, outputDir+"/")
		name = strings.TrimPrefix(name, outputDir+"\\")
		if name == "" {
			name = f.Path
		}
		task.AppendLog("[OUTPUT] Creating: %s\n", name)
		fileRec, err := c.fileSvc.CreateTextFile(input.TenantID, outputWS.ID, name, f.Content, input.Actor)
		if err != nil {
			task.AppendLog("[OUTPUT] Warning: %v\n", err)
			continue
		}
		created = append(created, fileRec.ID)
	}
	return created, outputWS.ID, nil
}

func (c *Compiler) commit(input *CompileInput, outputWorkspaceID string) (string, error) {
	msg := input.CommitMsg
	if msg == "" {
		msg = "Agent multi-phase knowledge compilation"
	}
	commit, err := c.fileSvc.CreateCommit(outputWorkspaceID, input.Actor, msg, nil)
	if err != nil {
		return "", err
	}
	return commit.ID, nil
}

func findChildFolderByName(node *entity.TreeNode, name string) *entity.File {
	for _, child := range node.Children {
		if child.Type == "folder" && child.Name == name {
			return &entity.File{ID: child.ID}
		}
	}
	return nil
}
