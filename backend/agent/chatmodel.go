package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// DeepSeekChatModel wraps LLMClient as a BaseChatModel + ToolCallingChatModel.
// Enables the agent loop to use our existing DeepSeek API configuration.
type DeepSeekChatModel struct {
	client *LLMClient
	tools  []*schema.ToolInfo
}

// NewDeepSeekChatModel creates a ChatModel wrapping our LLMClient.
func NewDeepSeekChatModel(llm *LLMClient) *DeepSeekChatModel {
	return &DeepSeekChatModel{client: llm}
}

// Generate implements model.BaseChatModel — synchronous LLM call.
// opts are runtime options (temperature, tools, etc.). We use our underlying LLMClient config.
func (m *DeepSeekChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	// Convert schema.Messages to system + user prompt strings
	systemMsg, userMsg := messagesToPrompts(input)

	// Check if tools are configured on this instance
	if len(m.tools) > 0 {
		return m.generateWithTools(ctx, systemMsg, userMsg)
	}

	// Plain chat path
	content, err := m.client.ChatRaw(ctx, systemMsg, userMsg)
	if err != nil {
		return nil, fmt.Errorf("deepseek chat: %w", err)
	}

	return schema.AssistantMessage(content, nil), nil
}

// generateWithTools handles tool-calling mode.
func (m *DeepSeekChatModel) generateWithTools(ctx context.Context, systemMsg, userMsg string) (*schema.Message, error) {
	combined := systemMsg
	if userMsg != "" {
		combined += "\n\n" + userMsg
	}

	// Use Chat (not ChatRaw) for structured JSON output
	result, err := m.client.Chat(ctx, systemMsg, userMsg, nil)
	if err != nil {
		// Fallback: call ChatRaw
		content, err2 := m.client.ChatRaw(ctx, systemMsg, combined)
		if err2 != nil {
			return nil, fmt.Errorf("deepseek chat: %w (raw: %v)", err, err2)
		}
		return schema.AssistantMessage(content, nil), nil
	}

	if result != nil && len(result.Files) > 0 {
		content, _ := json.Marshal(result)
		return schema.AssistantMessage(string(content), nil), nil
	}

	content, err := m.client.ChatRaw(ctx, systemMsg, combined)
	if err != nil {
		return nil, fmt.Errorf("deepseek chat raw: %w", err)
	}

	return schema.AssistantMessage(content, nil), nil
}

// Stream implements model.BaseChatModel — streaming LLM call.
func (m *DeepSeekChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	// Create pipe for streaming
	sr, sw := schema.Pipe[*schema.Message](3)

	systemMsg, userMsg := messagesToPrompts(input)

	go func() {
		defer sw.Close()

		// Use our existing streaming client
		_ = m.client.ChatStream(ctx, systemMsg, userMsg)

		// Since ChatStream doesn't return stream data in the same way,
		// we send the complete result as a single chunk for now.
		// A full implementation would read from the actual stream.
		content, err := m.client.ChatRaw(ctx, systemMsg, userMsg)
		if err != nil {
			sw.Send(nil, err)
			return
		}

		sw.Send(schema.AssistantMessage(content, nil), nil)
	}()

	return sr, nil
}

// WithTools implements model.ToolCallingChatModel — returns a new instance with tools bound.
func (m *DeepSeekChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	newModel := &DeepSeekChatModel{
		client: m.client,
		tools:  append([]*schema.ToolInfo{}, tools...),
	}
	return newModel, nil
}

// BindTools sets tools on the model (mutates; for internal use).
func (m *DeepSeekChatModel) BindTools(tools []*schema.ToolInfo) {
	m.tools = tools
}

// ========== Helper: Convert schema.Messages to prompt strings ==========

// messagesToPrompts converts message list to system + user prompt strings.
func messagesToPrompts(messages []*schema.Message) (system, user string) {
	for _, msg := range messages {
		switch msg.Role {
		case schema.System:
			if system != "" {
				system += "\n"
			}
			system += msg.Content
		case schema.User:
			if user != "" {
				user += "\n"
			}
			user += msg.Content
		case schema.Assistant:
			// Tool calls or responses from previous turns
			if msg.Content != "" {
				if user != "" {
					user += "\n"
				}
				user += "[Assistant]: " + msg.Content
			}
		case schema.Tool:
			if user != "" {
				user += "\n"
			}
			user += fmt.Sprintf("[Tool %s]: %s", msg.ToolName, msg.Content)
		}
	}
	return
}
