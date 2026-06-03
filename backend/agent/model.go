package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// LogCallback receives real-time log lines during execution.
type LogCallback func(format string, args ...interface{})

// LLMClient wraps the OpenAI-compatible API for LLM calls.
type LLMClient struct {
	client    *openai.Client
	model     string
	log       LogCallback
	mu        sync.Mutex
}

// NewLLMClient creates a new LLM client with the given config.
func NewLLMClient(apiKey, baseURL, model string) *LLMClient {
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = baseURL
	client := openai.NewClientWithConfig(config)
	return &LLMClient{
		client: client,
		model:  model,
	}
}

// SetLogCallback sets the log callback for recording LLM interactions.
func (l *LLMClient) SetLogCallback(cb LogCallback) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.log = cb
}

func (l *LLMClient) logf(format string, args ...interface{}) {
	l.mu.Lock()
	cb := l.log
	l.mu.Unlock()
	if cb != nil {
		cb(format, args...)
	}
}

// Chat sends a chat completion request with optional streaming logs.
func (l *LLMClient) Chat(ctx context.Context, systemPrompt, userPrompt string, tools []openai.Tool) (*LLMResult, error) {
	l.logf("[LLM] === Request ===\n")
	l.logf("[LLM] Model: %s\n", l.model)
	l.logf("[LLM] System Prompt (%d chars):\n%s\n", len(systemPrompt), truncate(systemPrompt, 500))
	l.logf("[LLM] User Prompt (%d chars):\n%s\n", len(userPrompt), truncate(userPrompt, 500))

	var messages []openai.ChatCompletionMessage
	if systemPrompt != "" {
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: systemPrompt})
	}
	messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: userPrompt})

	req := openai.ChatCompletionRequest{
		Model:       l.model,
		Messages:    messages,
		Temperature: 0.3,
		MaxTokens:   16384,
		Tools:       tools,
	}

	l.logf("[LLM] Sending request...\n")
	start := time.Now()

	resp, err := l.client.CreateChatCompletion(ctx, req)
	if err != nil {
		l.logf("[LLM] Error: %v\n", err)
		return nil, fmt.Errorf("chat completion: %w", err)
	}

	elapsed := time.Since(start)
	l.logf("[LLM] Response received in %v\n", elapsed)
	l.logf("[LLM] Usage: %d prompt tokens, %d completion tokens\n",
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens)

	if len(resp.Choices) == 0 {
		l.logf("[LLM] No choices in response\n")
		return nil, fmt.Errorf("no choices in response")
	}

	content := resp.Choices[0].Message.Content
	l.logf("[LLM] Response (%d chars):\n%s\n", len(content), truncate(content, 1000))

	// Try to parse as structured JSON output
	var result LLMResult
	if err := json.Unmarshal([]byte(content), &result); err == nil && len(result.Files) > 0 {
		l.logf("[COMPILER] Parsed %d output files from JSON response\n", len(result.Files))
		return &result, nil
	}

	// Fallback: wrap raw content as single file
	l.logf("[COMPILER] Response not JSON, wrapping as single file\n")
	return &LLMResult{
		Files: []OutputFile{
			{Path: "compiled.md", Content: content},
		},
	}, nil
}

// ChatStream sends a request with streaming response for real-time UI.
func (l *LLMClient) ChatStream(ctx context.Context, systemPrompt, userPrompt string) error {
	var messages []openai.ChatCompletionMessage
	if systemPrompt != "" {
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: systemPrompt})
	}
	messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: userPrompt})

	req := openai.ChatCompletionRequest{
		Model:       l.model,
		Messages:    messages,
		Temperature: 0.3,
		MaxTokens:   16384,
		Stream:      true,
	}

	l.logf("[LLM] Streaming request...\n")
	stream, err := l.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		l.logf("[LLM] Stream error: %v\n", err)
		return err
	}
	defer stream.Close()

	var fullContent string
	for {
		chunk, err := stream.Recv()
		if err != nil {
			break
		}
		if len(chunk.Choices) > 0 {
			fullContent += chunk.Choices[0].Delta.Content
		}
	}

	l.logf("[LLM] Stream complete (%d chars)\n", len(fullContent))

	var result LLMResult
	if err := json.Unmarshal([]byte(fullContent), &result); err == nil && len(result.Files) > 0 {
		l.logf("[COMPILER] Parsed %d output files\n", len(result.Files))
	}
	return nil
}

// ChatRaw sends a chat request and returns the raw response content string.
func (l *LLMClient) ChatRaw(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	l.logf("[LLM] ChatRaw — system(%d chars), user(%d chars)\n", len(systemPrompt), len(userPrompt))

	var messages []openai.ChatCompletionMessage
	if systemPrompt != "" {
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: systemPrompt})
	}
	messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: userPrompt})

	req := openai.ChatCompletionRequest{
		Model:       l.model,
		Messages:    messages,
		Temperature: 0.3,
		MaxTokens:   16384,
	}

	start := time.Now()
	resp, err := l.client.CreateChatCompletion(ctx, req)
	if err != nil {
		l.logf("[LLM] Error: %v\n", err)
		return "", fmt.Errorf("chat completion: %w", err)
	}

	elapsed := time.Since(start)
	if len(resp.Choices) == 0 {
		l.logf("[LLM] No choices\n")
		return "", fmt.Errorf("no choices in response")
	}

	content := resp.Choices[0].Message.Content
	l.logf("[LLM] Response received in %v (%d chars)\n", elapsed, len(content))
	l.logf("[LLM] Usage: %d prompt + %d completion tokens\n",
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens)

	return content, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
