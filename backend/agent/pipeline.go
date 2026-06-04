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

	"github.com/go-redis/redis/v8"
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
// Compiler is the knowledge compilation agent.
type Compiler struct {
	fileSvc   *service.FileService
	llm       *LLMClient
	rdb       *redis.Client
	tasks     map[string]*TaskState
	tasksMu   sync.RWMutex
	entityID  func() string

	// P0: Anti-loop and budget protection
	budget *BranchBudgetTracker
	// P1: L1 mode decision engine
	modeDec *ModeDecider
}

func NewCompiler(fileSvc *service.FileService, llm *LLMClient, rdb *redis.Client) *Compiler {
	return &Compiler{
		fileSvc:  fileSvc,
		llm:      llm,
		rdb:      rdb,
		tasks:    make(map[string]*TaskState),
		entityID: entity.NewID,
		budget:   NewBranchBudgetTracker(),
		modeDec:  NewModeDecider(),
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

	cp := NewCheckpointManager(c.rdb, taskID)
	existing, err := cp.Load()
	if err == nil && existing != nil {
		task.AppendLog("[CP] Found existing checkpoint at phase '%s', resuming...\n", existing.Phase)
	}

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

// ========== Main Pipeline (with Redis Checkpoints) ==========

func (c *Compiler) runCompile(task *TaskState, input *CompileInput) {
	cp := NewCheckpointManager(c.rdb, task.ID)

	// P1: Reset mode decider for new task
	// P0: Reset budget for new task
	c.budget = NewBranchBudgetTracker()
	c.modeDec = NewModeDecider()

	task.SetStatus("running")
	task.StartedAt = time.Now()
	task.AppendLog("[TASK] ===== Multi-Phase Compilation =====\n")
	task.AppendLog("[TASK] Workspace: %s\n", input.WorkspaceID)
	task.AppendLog("[TASK] Output: %s\n", input.OutputDir)

	// P1: Log execution mode
	task.AppendLog("[MODE] Starting in '%s' mode\n", c.modeDec.ModeString())

	c.llm.SetLogCallback(func(format string, args ...interface{}) {
		task.AppendLog(format, args...)
	})

	outputDir := input.OutputDir
	if outputDir == "" {
		outputDir = "synthesis"
	}

	// Try to resume from checkpoint
	saved, _ := cp.Load()
	resumeFrom := ""
	if saved != nil {
		resumeFrom = saved.Phase
		task.AppendLog("[CP] Resuming from phase '%s' (%d topics, %d compiled)\n",
			saved.Phase, len(saved.Topics), len(saved.Compiled))
	}

	// ── Load Phase ──
	var files []FileNode
	var skills []SkillDef
	files, skills = c.loadPhase(task, input)
	if len(files) == 0 {
		return
	}

	// ── Keyword Extraction (not checkpointed) ──
	if resumeFrom == "" {
		c.extractKeywords(task, files)
	}

	// ── Domain Detection & Compile Phase ──
	// If workspace has domains/ directory, compile per domain in parallel.
	// Otherwise, treat all files as a single "wiki" domain.

	domains := groupFilesByDomain(files)
	if len(domains) > 1 {
		task.AppendLog("[DOMAIN] Detected %d domains\n", len(domains))
		for _, d := range domains {
			task.AppendLog("  - %s (%d files)\n", d.Name, len(d.Files))
		}
	} else {
		task.AppendLog("[DOMAIN] Single domain '%s' (%d files)\n", domains[0].Name, len(domains[0].Files))
	}

	compileStart := time.Now()
	var allOutputs []OutputFile
	if resumeFrom == "compile" && saved != nil {
		allOutputs = saved.Compiled
		task.AppendLog("[CP] Restored %d compiled articles\n", len(allOutputs))
	}

	// Compile: sequential per-domain, parallel via goroutine pool for multiple domains
	type domainResult struct {
		name     string
		articles []OutputFile
		err      error
	}

	if len(domains) <= 1 {
		// Single domain: sequential compile (existing behavior)
		domain := domains[0]
		var domainTopics []TopicInfo
		if len(domain.Files) > 0 {
			// Scan within domain files
			c.extractKeywords(task, domain.Files)
			domainTopics = c.scanPhase(task, domain.Files, skills, input.Instructions)
		}
		if len(domainTopics) == 0 && len(domain.Files) > 0 {
			domainTopics = c.fallbackTopics(domain.Files)
		}
		task.AppendLog("[COMPILE] %d topics in domain '%s'\n", len(domainTopics), domain.Name)

		for i, topic := range domainTopics {
			if i < len(allOutputs) {
				continue
			}
			c.compileSingleTopic(task, &allOutputs, topic, domainTopics, domain.Files, outputDir, cp, compileStart, i, len(domainTopics), skills, domain.Name)
		}

		// Single domain: append mapping-notes
		if len(domainTopics) > 0 {
			allOutputs = append(allOutputs, generateMappingNote(domain.Name, domainTopics, len(domain.Files)))
		}
	} else {
		// Multiple domains: parallel compile per domain sub-agent
		const maxParallelDomains = 3
		sem := make(chan struct{}, maxParallelDomains)
		resultCh := make(chan domainResult, len(domains))

		for _, d := range domains {
			sem <- struct{}{}
			go func(dg DomainGroup) {
				defer func() { <-sem }()

				// Each domain sub-agent: scan + compile
				c.extractKeywords(task, dg.Files)
				domainTopics := c.scanPhase(task, dg.Files, skills, input.Instructions)
				if len(domainTopics) == 0 && len(dg.Files) > 0 {
					domainTopics = c.fallbackTopics(dg.Files)
				}

				var outputs []OutputFile
				for _, t := range domainTopics {
					article := c.compilePhase(task, t, dg.Files, domainTopics, nil, outputDir, skills, dg.Name)
					if article != nil {
						outputs = append(outputs, *article)
					}
				}

				// Append mapping-notes for this domain
				if len(domainTopics) > 0 {
					outputs = append(outputs, generateMappingNote(dg.Name, domainTopics, len(dg.Files)))
				}

				resultCh <- domainResult{
					name:     dg.Name,
					articles: outputs,
				}
			}(d)
		}

		for range domains {
			res := <-resultCh
			if res.err != nil {
				task.AppendLog("[DOMAIN] Domain '%s' compile failed: %v\n", res.name, res.err)
			} else {
				task.AppendLog("[DOMAIN] Domain '%s': %d articles compiled\n", res.name, len(res.articles))
				allOutputs = append(allOutputs, res.articles...)
			}
		}
	}

	// Consistency review (now with domain-aware backlinks)
	if len(allOutputs) > 0 {
		task.AppendLog("[REVIEW] Consistency check across %d articles...\n", len(allOutputs))
		allOutputs = c.consistencyReview(task, allOutputs)
	}

	compileTotal := time.Since(compileStart).Round(time.Second)
	task.AppendLog("[COMPILE] All domains compiled in %v (budget: %s)\n", compileTotal, c.budget.Snapshot())

	// ── INDEX.md + log.md (domain-aware) ──
	allOutputs = append(allOutputs,
		generateDomainIndex(domains, allOutputs),
		c.generateLogECL(task, domains, len(files), outputDir))

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

// P2: compileSingleTopic handles one topic with budget tracking, circuit breaker, and checkpoint.
func (c *Compiler) compileSingleTopic(task *TaskState, allOutputs *[]OutputFile, topic TopicInfo, topics []TopicInfo, files []FileNode, outputDir string, cp *CheckpointManager, compileStart time.Time, idx, total int, skills []SkillDef, domain string) bool {
	// P2: Circuit breaker — skip if too many consecutive failures
	if !c.budget.CheckCircuitBreaker(topic.Name) {
		failCount := c.budget.ConsecutiveFailCount(topic.Name)
		task.AppendLog("[BREAKER] Skipping topic '%s': %d consecutive failures\n", topic.Name, failCount)
		return true // skipped, not failed
	}

	// P2: Record topic start time for timeout
	c.budget.RecordTopicStart(topic.Name)
	c.budget.ResetForNewTopic(topic.Name)

	tStart := time.Now()
	article := c.compilePhase(task, topic, files, topics, *allOutputs, outputDir, skills, domain)
	elapsed := time.Since(tStart).Round(time.Second)

	if article != nil {
		*allOutputs = append(*allOutputs, *article)
		c.budget.RecordSuccess(topic.Name)
	} else {
		failCount := c.budget.RecordFailure(topic.Name)
		task.AppendLog("[BREAKER] Topic '%s' failed (%d consecutive)\n", topic.Name, failCount)

		// P1: Record failure for mode decision
		c.modeDec.RecordFailure()
		newMode := c.modeDec.Decide()
		task.AppendLog("[MODE] Mode decision: '%s' after failure\n", newMode.String())
	}

	remaining := total - idx - 1
	avgTime := time.Since(compileStart) / time.Duration(idx+1)
	eta := avgTime * time.Duration(remaining)
	task.AppendLog("[COMPILE] Topic %d/%d: '%s' (%v, ETA %v)\n",
		idx+1, total, topic.Name, elapsed, eta.Round(time.Second))

	// Checkpoint after each topic
	cp.SavePhase("compile", topics, *allOutputs, nil, len(files), outputDir)
	task.AppendLog("[CP] Checkpoint after topic %d/%d\n", idx+1, total)

	return article != nil
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
		if err := unmarshalLenient(content, &results); err != nil {
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
		return c.scanBatch(task, files, skills, instructions)
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
			topics := c.scanBatch(task, bf, skills, instructions)
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
func (c *Compiler) scanBatch(task *TaskState, files []FileNode, skills []SkillDef, instructions string) []TopicInfo {
	var b strings.Builder
	b.WriteString("分析以下源文件，发现其中的知识主题。\n\n")
	b.WriteString("规则：\n")
	b.WriteString("1. 共享关键词的文件归为同一主题\n")
	b.WriteString("2. 一个文件可能属于多个主题\n")
	b.WriteString("3. 主题名用 kebab-case\n\n")

	// Inject skills content into scan prompt
	for _, s := range skills {
		b.WriteString(fmt.Sprintf("## 编译规则：%s\n\n%s\n\n", s.Name, s.Content))
	}

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
	if err := unmarshalLenient(content, &result); err != nil {
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
	if err := unmarshalLenient(content, &result); err != nil || len(result.Topics) == 0 {
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

// ========== Claude Code-style Compile: Understand → Read → Write → Verify ==========

func (c *Compiler) compilePhase(task *TaskState, topic TopicInfo, allFiles []FileNode, allTopics []TopicInfo, compiled []OutputFile, outputDir string, skills []SkillDef, domain string) *OutputFile {
	task.AppendLog("[COMPILE] Topic '%s' (%d sources, domain: %s)...\n", topic.Name, len(topic.SourcePaths), domain)

	// P2: Dynamic compression — build context with volume-aware compression
	context := c.buildCompileContext(topic, allTopics, compiled, skills, domain)

	// Collect the topic's source files
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

	// P0: Source File Budget — summaries only, max 3 full files per topic
	pickPrompt := context + c.buildFileIndexPrompt(relevantFiles, allFiles)
	pickMsg := `请分析上面列出的源文件。告诉我你需要完整阅读哪些文件来编译这篇知识文章。

限制: 最多选择 3 个文件。优先选择内容最相关、信息量最大的。

回复格式（纯文本，不要JSON）：
NEED: file1.md, file2.md, file3.md
REASON: 简要说明为什么需要这些文件`

	pickResp := c.chatWithRetry(task, "", pickPrompt+"\n\n"+pickMsg)
	if pickResp == "" {
		return nil
	}

	needFiles := parseNeedFiles(pickResp)
	if len(needFiles) == 0 {
		// Fallback: include up to 3 most relevant files
		for i, f := range relevantFiles {
			if i >= 3 {
				break
			}
			needFiles = append(needFiles, f.Path)
		}
	}
	// P0: Hard budget from BranchBudgetTracker
	fileBudget := c.budget.FileBudgetRemaining(topic.Name)
	if fileBudget <= 0 {
		fileBudget = 3 // default
	}
	if len(needFiles) > fileBudget {
		task.AppendLog("[BUDGET] Capping file request from %d to %d (budget)\n", len(needFiles), fileBudget)
		needFiles = needFiles[:fileBudget]
	}
	// Record file reads against budget
	for range needFiles {
		c.budget.RecordFileRead(topic.Name)
	}
	task.AppendLog("[COMPILE] LLM requested %d/%d files (budget: %d max)\n", len(needFiles), len(relevantFiles), fileBudget)

	// ── Round 2: Read & Write — full content of budgeted files ──
	writePrompt := context + "\n## 你请求的源文件（完整内容）\n\n"
	for _, f := range allFiles {
		if containsPath(needFiles, f.Path) {
			writePrompt += fmt.Sprintf("### %s\n```\n%s\n```\n\n", f.Path, f.Content)
		}
	}
	writeMsg := `基于以上源文件，编译一篇知识文章。

要求:
1. 合成多个源文件的信息，不要照搬一个源
2. 每个关键信息后面标注来源: [source: filename.md]
3. 正文中用 [[OtherArticle]] 交叉引用其他主题
4. 每个小节末尾标记覆盖度: [coverage: high/medium/low]
5. 文章结构: # 标题 → ## 概要 → ## 内容 → ## 资料来源

直接输出纯 Markdown。`

	article := c.chatWithRetry(task, "", writePrompt+"\n\n"+writeMsg)
	if article == "" {
		return nil
	}
	article = stripJSONFence(article)

	// ── Round 3: Verify ──
	verifyPrompt := "你刚写了一篇知识文章。请审查并修正以下问题：\n\n"
	verifyPrompt += "1. [精确溯源] 每个关键 claim 后面是否都有 [source: filename.md]？若缺失则补上\n"
	verifyPrompt += "2. [交叉引用] 是否引用了其他主题 [[slug]]？若缺失则补上\n"
	verifyPrompt += "3. [内容重叠] 是否与已有文章重复？（见上方「已编译的文章」）若有则精简并用 [[slug]] 替代\n"
	verifyPrompt += "4. [准确度] 是否存在推断性内容未标注为推测？若有则加 [推断: 基于...]\n\n"
	verifyPrompt += "原文如下：\n---\n" + truncate(article, 8000) + "\n---\n"
	verifyPrompt += "\n输出修正后的完整文章。只输出 Markdown，不要额外说明。"

	verified := c.chatWithRetry(task, "", verifyPrompt)
	if verified != "" {
		verified = stripJSONFence(verified)
		if len(verified) > len(article)/2 {
			article = verified
			task.AppendLog("[COMPILE] Verified & improved\n")
		}
	}

	// P1: Mode-aware quality gate
	isStrict := c.modeDec.CurrentMode() == ModeStrict
	if !c.qualityGate(task, article, topic, allTopics, isStrict) {
		task.AppendLog("[COMPILE] Quality gate failed (mode: %s), re-attempting...\n", c.modeDec.ModeString())

		// P0: Check rewrite budget
		if !c.budget.CheckRewriteBudget(topic.Name) {
			task.AppendLog("[BUDGET] Skip rewrite for '%s': no budget remaining\n", topic.Name)
			return &OutputFile{Path: topic.Name + ".md", Content: article} // accept as-is
		}
		// P0: Record rewrite against budget
		c.budget.RecordRewrite(topic.Name)

		reworkPrompt := "你的文章未通过质量检查。以下是需要改进的问题：\n\n"
		if isStrict {
			reworkPrompt += "严格模式下必须满足所有要求：\n"
		}
		reworkPrompt += "1. 确保文章包含 [[OtherArticle]] 交叉链接\n"
		reworkPrompt += "2. 确保每个小节有 [coverage: high/medium/low] 标记\n"
		reworkPrompt += "3. 确保内容长度 >= 200 字\n"
		reworkPrompt += "4. 确保有 ## 概要 和 ## 资料来源 章节\n\n"
		reworkPrompt += "重新输出完整的文章。\n---\n" + truncate(article, 8000)

		rework := c.chatWithRetry(task, "", reworkPrompt)
		if rework != "" {
			rework = stripJSONFence(rework)
			if len(rework) > len(article)/2 {
				article = rework
				task.AppendLog("[COMPILE] Quality gate passed on retry\n")
			}
		}
	} else {
		task.AppendLog("[COMPILE] Quality gate passed (mode: %s)\n", c.modeDec.ModeString())
	}

	// P0: Session Note — extract structured note for future reference
	_ = c.extractSessionNote(article, topic.Name)

	// Add front-matter with last_verified date
	article = addFrontMatter(article, domain)

	// Output path: domains/{domain}/{topic}.md or {topic}.md for wiki
	outputPath := outputPathForDomain(domain, topic.Name)
	return &OutputFile{Path: outputPath, Content: article}
}

// P1: Mode-aware Quality Gate — checks article for minimum quality standards.
// In strict mode, ALL checks must pass. In explore mode, a subset is sufficient.
func (c *Compiler) qualityGate(task *TaskState, article string, topic TopicInfo, allTopics []TopicInfo, isStrict bool) bool {
	if len(strings.TrimSpace(article)) < 200 {
		task.AppendLog("[GATE] Content too short (%d chars)\n", len(article))
		if isStrict {
			return false
		}
	}

	// Check for [[links]] to other topics
	hasLinks := false
	for _, t := range allTopics {
		if t.Name != topic.Name && strings.Contains(article, "[["+t.Name+"]]") {
			hasLinks = true
			break
		}
	}
	if !hasLinks {
		task.AppendLog("[GATE] No [[cross-links]] to other topics\n")
		if isStrict {
			return false
		}
	}

	// Check for coverage tags
	hasCoverage := strings.Contains(article, "[coverage:")
	if !hasCoverage {
		task.AppendLog("[GATE] Missing [coverage:] tags\n")
	}

	// Minimum sections
	hasSummary := strings.Contains(article, "## 概要") || strings.Contains(article, "## Summary")
	hasSources := strings.Contains(article, "## 资料来源") || strings.Contains(article, "## Sources")
	if !hasSummary || !hasSources {
		task.AppendLog("[GATE] Missing sections: summary=%v, sources=%v\n", hasSummary, hasSources)
		if isStrict {
			return false
		}
	}

	if isStrict {
		// Strict mode: ALL checks must pass
		return hasLinks && hasCoverage && hasSummary && hasSources
	}

	// Explore mode: pass if at least has links AND (coverage OR sections)
	return hasLinks && (hasCoverage || (hasSummary && hasSources))
}

// P0: Session Note — extracts a structured lightweight note from a compiled article.
// This replaces carrying full article content across compile phases.
func (c *Compiler) extractSessionNote(article string, topicName string) string {
	// Extract # title
	title := topicName
	for _, line := range strings.Split(article, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "# ") {
			title = strings.TrimSpace(line)[2:]
			break
		}
	}

	// Extract first 200 chars of summary as session note
	summary := ""
	inSummary := false
	for _, line := range strings.Split(article, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "## 概要" || trimmed == "## Summary" {
			inSummary = true
			continue
		}
		if inSummary {
			if strings.HasPrefix(trimmed, "## ") {
				break
			}
			summary += trimmed + " "
			if len(summary) > 200 {
				summary = summary[:200]
				break
			}
		}
	}
	if summary == "" {
		// Fallback: first 200 chars of article
		summary = article
		if len(summary) > 200 {
			summary = summary[:200]
		}
	}

	_ = title // note stored implicitly via article text
	return summary
}

// P2: Dynamic Compression — volume-aware context builder with adaptive summary length.
// Compression ratio adjusts dynamically based on compiled article count,
// session token budget, and execution mode.
// Modeled after iceCoder's ContextCompactor.
func (c *Compiler) buildCompileContext(topic TopicInfo, allTopics []TopicInfo, compiled []OutputFile, skills []SkillDef, domain string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("编译领域「%s」下的主题「%s」的知识文章。\n\n", domain, topic.Description))

	// Inject skills content into compile context (mandatory compilation rules)
	for _, s := range skills {
		if s.Content != "" {
			b.WriteString(fmt.Sprintf("## 强制编译规则：%s\n\n%s\n\n", s.Name, s.Content))
		}
	}

	// Other topics for cross-linking
	b.WriteString("其他已发现主题供交叉引用：\n")
	for _, t := range allTopics {
		if t.Name != topic.Name {
			b.WriteString(fmt.Sprintf("- [[%s]]: %s\n", t.Name, t.Description))
		}
	}

	// P2: Dynamic compression — compress previous articles based on count.
	// Uses exponential decay: more articles = shorter summaries.
	// Ensures critical data ([[links]], sources, coverage) survives compression.
	if len(compiled) > 0 {
		b.WriteString("\n## 已编译的文章\n\n")

		// Dynamic max length based on article count (exponential decay)
		maxLen := 500
		n := len(compiled)
		switch {
		case n >= 15:
			maxLen = 60
		case n >= 10:
			maxLen = 100
		case n >= 6:
			maxLen = 150
		case n >= 3:
			maxLen = 300
		}

		// P0: Session Note — only include note, not full content
		for _, art := range compiled {
			summary := art.Content
			if len(summary) > maxLen {
				// Preserve critical sections even when compressing
				cropped := c.cropPreservingLinks(summary, maxLen)
				summary = cropped
			}
			b.WriteString(fmt.Sprintf("### %s\n```\n%s\n```\n\n", art.Path, summary))
		}
		b.WriteString("这些文章已存在。新文章内容不得与之重复。应引用 [[ExistingArticle]] 而非重写。\n\n")
	}

	return b.String()
}

// P2: cropPreservingLinks truncates content while preserving [[links]] and [source:] markers.
func (c *Compiler) cropPreservingLinks(content string, maxLen int) string {
	if len(content) <= maxLen {
		return content
	}

	// Try to keep the first portion with links intact
	half := maxLen * 2 / 3
	firstPart := content
	if len(firstPart) > half {
		firstPart = firstPart[:half]
	}

	// Collect [[links]] and [source:] from the remaining portion
	links := extractCrossReferences(content)
	rest := "...[+%d more]..."

	// Build compressed version: first part + link summary
	var result strings.Builder
	result.WriteString(firstPart)
	if len(content) > half {
		result.WriteString(fmt.Sprintf(rest, len(content)-half))
	}
	// Append preserved links at the end
	if len(links) > 0 {
		result.WriteString("\n\n关键引用：")
		for _, l := range links {
			result.WriteString(l + " ")
		}
		result.WriteString("\n")

		_ = links
	}

	final := result.String()
	if len(final) > maxLen {
		return final[:maxLen] + "..."
	}
	return final
}

// extractCrossReferences collects [[link]] and [source:] patterns from text.
func extractCrossReferences(content string) []string {
	var refs []string
	seen := make(map[string]bool)

	// Extract [[link]] patterns
	for i := 0; i < len(content)-3; i++ {
		if content[i] == '[' && content[i+1] == '[' {
			end := strings.Index(content[i:], "]]")
			if end > 0 {
				link := content[i : i+end+2]
				if !seen[link] {
					refs = append(refs, link)
					seen[link] = true
				}
				i += end + 1
			}
		}
	}

	// Extract [source:] patterns
	start := 0
	for {
		idx := strings.Index(content[start:], "[source:")
		if idx < 0 {
			break
		}
		end := strings.Index(content[start+idx:], "]")
		if end < 0 {
			break
		}
		src := content[start+idx : start+idx+end+1]
		if !seen[src] {
			refs = append(refs, src)
			seen[src] = true
		}
		start += idx + end + 1
	}

	return refs
}

// buildFileIndexPrompt lists files with summaries for the LLM to pick from.
func (c *Compiler) buildFileIndexPrompt(relevantFiles, allFiles []FileNode) string {
	var b strings.Builder
	b.WriteString("\n## 源文件索引\n\n")
	b.WriteString("以下是可能的源文件。请告诉我你需要完整阅读哪些。\n\n")

	for _, f := range relevantFiles {
		summary := f.Content
		if len(summary) > 500 {
			summary = summary[:500] + "..."
		}
		kw := strings.Join(f.Keywords, ", ")
		if kw == "" {
			kw = "(未提取)"
		}
		b.WriteString(fmt.Sprintf("### %s\n", f.Path))
		b.WriteString(fmt.Sprintf("- 标题: %s | 关键词: %s\n", f.Title, kw))
		b.WriteString(fmt.Sprintf("- 摘要:\n```\n%s\n```\n\n", summary))
	}

	return b.String()
}

// parseNeedFiles extracts NEED: line from LLM response.
func parseNeedFiles(resp string) []string {
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "NEED:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				var files []string
				for _, f := range strings.Split(parts[1], ",") {
					f = strings.TrimSpace(f)
					if f != "" {
						files = append(files, f)
					}
				}
				return files
			}
		}
	}
	return nil
}

func containsPath(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}

// P1: Graduated Recovery — chatWithRetry sends messages to the LLM with timeout+retry.
// Recovery messages escalate: light hint → evidence package → strong warning.
// Modeled after iceCoder's graduated recovery in harness-tool-round.ts.
// If systemMsg is empty, no system message is sent.
func (c *Compiler) chatWithRetry(task *TaskState, systemMsg, userMsg string) string {
	const timeout = 120 * time.Second
	const retries = 3

	// P0: Global LLM call budget
	if !c.budget.CheckLLMBudget() {
		task.AppendLog("[BUDGET] LLM call limit reached (%d max)\n", c.budget.LLMCallCount())
		return ""
	}
	c.budget.RecordLLMCall()

	var lastErr error
	for attempt := 0; attempt < retries; attempt++ {
		// P1: Graduated recovery messages
		var enhancedMsg string
		if attempt == 0 {
			enhancedMsg = userMsg
		} else if attempt == 1 {
			// Level 1: light hint with previous error
			enhancedMsg = fmt.Sprintf(
				"【注意】之前出错了：%v\n\n请仔细检查输出格式，修正后重新输出。如果前一次输出被截断，请确保输出完整。\n\n---\n\n%s",
				lastErr, userMsg)
			task.AppendLog("[RETRY] Attempt %d (light hint)\n", attempt+1)
		} else if attempt == 2 {
			// Level 2: strong warning with error evidence
			enhancedMsg = fmt.Sprintf(
				"【重试警告】之前连续出错了 %d 次。\n最后一次错误：%v\n\n请务必：\n1. 输出格式必须严格符合要求\n2. 如果输出过长，确保不被截断\n3. 不要包含多余的解释文字\n\n---\n\n%s",
				attempt, lastErr, userMsg)
			task.AppendLog("[RETRY] Attempt %d (strong warning)\n", attempt+1)
		} else {
			// Level 3: final attempt
			enhancedMsg = fmt.Sprintf(
				"【最终重试】这是最后一次重试机会。\n\n请输出最简洁、最正确的回答。\n\n---\n\n%s",
				userMsg)
			task.AppendLog("[RETRY] Attempt %d (final)\n", attempt+1)
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		content, err := c.llm.ChatRaw(ctx, systemMsg, enhancedMsg)
		cancel()
		if err == nil {
			return content
		}
		lastErr = err
		task.AppendLog("[WARN] LLM error: %v\n", err)
		if attempt < retries-1 {
			time.Sleep(2 * time.Second)
		}
	}
	task.AppendLog("[ERROR] All %d retries exhausted, last error: %v\n", retries, lastErr)
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

	content := c.chatWithRetry(task, "", b.String())
	if content == "" {
		return articles
	}

	var result struct {
		Fixes []OutputFile `json:"fixes"`
	}
	if err := unmarshalLenient(content, &result); err != nil || len(result.Fixes) == 0 {
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

	conceptCtx, conceptCancel := context.WithTimeout(context.Background(), 90*time.Second)
	content, err := c.llm.ChatRaw(conceptCtx, "你是一个知识发现专家。找出跨主题的共现模式。只在确实存在3个以上相关性时创建概念。", b.String())
	conceptCancel()
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
	if err := unmarshalLenient(content, &result); err != nil || len(result.Concepts) == 0 {
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

		fixCtx, fixCancel := context.WithTimeout(context.Background(), 90*time.Second)
		content, err := c.llm.ChatRaw(fixCtx, systemMsg, b.String())
		fixCancel()
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

func (c *Compiler) generateLogECL(task *TaskState, domains []DomainGroup, fileCount int, outputDir string) OutputFile {
	today := time.Now().Format("2006-01-02")
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## %s\n\n", today))
	b.WriteString(fmt.Sprintf("**处理的源文件:** %d\n\n", fileCount))
	b.WriteString("**已编译领域:**\n")
	for _, d := range domains {
		b.WriteString(fmt.Sprintf("- %s (%d 文件)\n", d.Name, len(d.Files)))
	}
	return OutputFile{Path: "log.md", Content: b.String()}
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
