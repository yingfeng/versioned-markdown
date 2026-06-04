package agent

import (
	"fmt"
	"strings"
)

// ========== Section Operations for Incremental Wiki Updates ==========
// Modeled after Atomic's WikiSectionOp pattern.
// Instead of full rewrites, LLM emits structured operations against existing articles.
// Untouched sections stay byte-identical, preserving citation graphs and making diffs clean.

// SectionOpKind identifies the type of section operation.
type SectionOpKind string

const (
	SectionOpNoChange SectionOpKind = "no_change"
	SectionOpAppend   SectionOpKind = "append"
	SectionOpReplace  SectionOpKind = "replace"
	SectionOpInsert   SectionOpKind = "insert"
)

// SectionOp is a single operation against an existing wiki article.
type SectionOp struct {
	Op           SectionOpKind `json:"op"`
	Heading      string        `json:"heading,omitempty"`       // exact ## heading (trimmed, case-sensitive)
	AfterHeading string        `json:"after_heading,omitempty"` // for insert: insert after this heading
	Content      string        `json:"content,omitempty"`       // new content
}

// getSectionOps calls the LLM to determine what changes are needed.
// Given existing article + new source content, LLM outputs structured ops.
func (c *Compiler) getSectionOps(task *TaskState, existing, topicName string, newSources []FileNode) []SectionOp {
	var b strings.Builder
	b.WriteString("你之前编译了以下知识文章。现在有新的源文件内容需要整合。\n\n")
	b.WriteString("## 现有文章\n\n")
	b.WriteString(fmt.Sprintf("```\n%s\n```\n\n", truncate(existing, 4000)))
	b.WriteString("## 新的源文件内容\n\n")
	for _, f := range newSources {
		b.WriteString(fmt.Sprintf("### %s\n```\n%s\n```\n\n", f.Path, f.Content))
	}
	b.WriteString(`请根据新内容输出 JSON 操作列表。可能的操作：

- {"op": "no_change"} — 不需要修改
- {"op": "append", "heading": "## 现有章节名", "content": "追加内容"} — 追加到现有章节
- {"op": "replace", "heading": "## 现有章节名", "content": "新内容"} — 替换现有章节
- {"op": "insert", "after_heading": "## 后接章节", "heading": "## 新章节", "content": "内容"} — 插入新章节

注意：
1. heading 必须与现有文章中的 ## 标题完全一致（trim 后）
2. 如果不需要修改，只返回 [{"op": "no_change"}]
3. 保留所有已有的 [[链接]] 和 [coverage:] 标记

输出格式：纯 JSON 数组。`)

	systemMsg := fmt.Sprintf("你是一个知识库编辑。根据新内容对现有知识文章做精确的增量更新。主题：%s", topicName)

	content := c.chatWithRetry(task, systemMsg, b.String())
	if content == "" {
		return []SectionOp{{Op: SectionOpNoChange}}
	}

	content = stripJSONFence(content)

	var ops []SectionOp
	if err := unmarshalLenient(content, &ops); err != nil || len(ops) == 0 {
		task.AppendLog("[SECTION_OPS] Parse failed, treating as no change\n")
		return []SectionOp{{Op: SectionOpNoChange}}
	}

	return ops
}

// applySectionOps applies a list of section operations to an existing article.
// Returns the modified article. Invalid ops (heading not found) are silently skipped.
func applySectionOps(article string, ops []SectionOp) string {
	for _, op := range ops {
		switch op.Op {
		case SectionOpNoChange:
			continue
		case SectionOpAppend:
			article = applyAppend(article, op.Heading, op.Content)
		case SectionOpReplace:
			article = applyReplace(article, op.Heading, op.Content)
		case SectionOpInsert:
			article = applyInsert(article, op.AfterHeading, op.Heading, op.Content)
		}
	}
	return article
}

// applyAppend appends content to an existing section's body.
func applyAppend(article, heading, content string) string {
	lines := strings.Split(article, "\n")
	var result []string
	inTarget := false
	appended := false

	for i, line := range lines {
		result = append(result, line)
		trimmed := strings.TrimSpace(line)

		if trimmed == heading {
			inTarget = true
			continue
		}

		if inTarget {
			// Check if next section starts
			if isHeading(line) {
				if !appended {
					result = append(result, content)
					appended = true
				}
				inTarget = false
				continue
			}
			// Last line of article
			if i == len(lines)-1 && !appended {
				result = append(result, content)
				appended = true
			}
		}
	}

	// If heading was the last section and nothing was appended yet
	if inTarget && !appended {
		result = append(result, content)
	}

	return strings.Join(result, "\n")
}

// applyReplace replaces the body of an existing section.
func applyReplace(article, heading, content string) string {
	lines := strings.Split(article, "\n")
	var result []string
	inTarget := false
	replaced := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == heading {
			result = append(result, line)
			inTarget = true
			continue
		}

		if inTarget {
			// Skip original content until next heading or end
			if isHeading(line) || i == len(lines)-1 {
				result = append(result, content)
				if i == len(lines)-1 && !isHeading(line) {
					result = append(result, line)
				}
				if isHeading(line) {
					result = append(result, line)
				}
				inTarget = false
				replaced = true
				continue
			}
			// Skip original content lines
			continue
		}
		result = append(result, line)
	}

	if inTarget && !replaced {
		result = append(result, content)
	}

	return strings.Join(result, "\n")
}

// applyInsert inserts a new section after the specified heading.
func applyInsert(article, afterHeading, newHeading, content string) string {
	lines := strings.Split(article, "\n")
	var result []string
	inserted := false

	for i, line := range lines {
		result = append(result, line)
		trimmed := strings.TrimSpace(line)

		if trimmed == afterHeading {
			// Insert after this section — find end of section
			for j := i + 1; j < len(lines); j++ {
				if isHeading(lines[j]) || j == len(lines)-1 {
					if !inserted {
						result = append(result, "")
						result = append(result, newHeading)
						result = append(result, content)
						inserted = true
					}
					if j == len(lines)-1 && !isHeading(lines[j]) {
						result = append(result, lines[j])
					}
					// Remaining lines will be added by the main loop
					for k := j; k < len(lines); k++ {
						if k != j && k > j {
							result = append(result, lines[k])
						}
					}
					return strings.Join(result, "\n")
				}
			}
		}
	}

	if !inserted {
		// afterHeading not found: append to end
		result = append(result, "")
		result = append(result, newHeading)
		result = append(result, content)
	}

	return strings.Join(result, "\n")
}

// isHeading checks if a line looks like a Markdown heading.
func isHeading(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "# ") ||
		strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "#### ")
}

// existingSection extracts an existing section's content by heading.
// Returns (found, content). Content excludes the heading line.
func existingSection(article, heading string) (bool, string) {
	lines := strings.Split(article, "\n")
	inTarget := false
	var content []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inTarget {
			if isHeading(line) {
				return true, strings.TrimSpace(strings.Join(content, "\n"))
			}
			content = append(content, line)
		}
		if trimmed == heading {
			inTarget = true
		}
	}

	if inTarget {
		return true, strings.TrimSpace(strings.Join(content, "\n"))
	}
	return false, ""
}

// sectionCount returns the number of ## sections in an article.
func sectionCount(article string) int {
	count := 0
	for _, line := range strings.Split(article, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			count++
		}
	}
	return count
}
