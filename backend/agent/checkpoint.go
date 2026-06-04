package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/go-redis/redis/v8"
)

const (
	cpPrefix     = "llmwiki:cp:"
	cpTTL        = 24 * time.Hour
)

// Checkpoint stores compilation state for resume support.
type Checkpoint struct {
	TaskID    string        `json:"task_id"`
	Phase     string        `json:"phase"` // "scan","compile","concept","index","write","agent_turn_N"
	Topics    []TopicInfo   `json:"topics,omitempty"`
	Compiled  []OutputFile  `json:"compiled,omitempty"`
	AllOutputs []OutputFile `json:"all_outputs,omitempty"`
	FileCount int           `json:"file_count"`
	OutputDir string        `json:"output_dir"`

	// Agent loop checkpoint fields
	TurnCount   int    `json:"turn_count,omitempty"`
	MessagesRaw string `json:"messages_raw,omitempty"`  // JSON-encoded []*schema.Message
	FilesRaw    string `json:"files_raw,omitempty"`      // JSON-encoded []FileNode
	SkillsRaw   string `json:"skills_raw,omitempty"`     // JSON-encoded []SkillDef
	TopicsRaw   string `json:"topics_raw,omitempty"`     // JSON-encoded []TopicInfo
	OutputsRaw  string `json:"outputs_raw,omitempty"`    // JSON-encoded []OutputFile
}

// CheckpointManager handles save/load/resume via Redis.
type CheckpointManager struct {
	rdb    *redis.Client
	taskID string
}

func NewCheckpointManager(rdb *redis.Client, taskID string) *CheckpointManager {
	return &CheckpointManager{rdb: rdb, taskID: taskID}
}

func cpKey(taskID string) string { return cpPrefix + taskID }

func (m *CheckpointManager) key() string { return cpKey(m.taskID) }

// Save persists checkpoint to Redis with TTL.
func (m *CheckpointManager) Save(cp *Checkpoint) error {
	cp.TaskID = m.taskID
	data, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}
	return m.rdb.Set(context.Background(), m.key(), data, cpTTL).Err()
}

// Load retrieves a previous checkpoint, returns nil if none.
func (m *CheckpointManager) Load() (*Checkpoint, error) {
	data, err := m.rdb.Get(context.Background(), m.key()).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis get: %w", err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("unmarshal checkpoint: %w", err)
	}
	return &cp, nil
}

// Delete removes checkpoint after successful completion.
func (m *CheckpointManager) Delete() {
	m.rdb.Del(context.Background(), m.key())
}

// SavePhase is a convenience for saving a single phase checkpoint.
func (m *CheckpointManager) SavePhase(phase string, topics []TopicInfo, compiled, allOutputs []OutputFile, fileCount int, outputDir string) error {
	return m.Save(&Checkpoint{
		Phase:      phase,
		Topics:     topics,
		Compiled:   compiled,
		AllOutputs: allOutputs,
		FileCount:  fileCount,
		OutputDir:  outputDir,
	})
}

// ========== Agent Loop Checkpoint ==========

// SaveAgentCheckpoint saves the full agent loop state.
func (m *CheckpointManager) SaveAgentCheckpoint(turnCount int, messages []*schema.Message, state *AgentState) error {
	// Serialize messages to JSON
	msgsRaw, _ := json.Marshal(messages)
	filesRaw, _ := json.Marshal(state.Files)
	skillsRaw, _ := json.Marshal(state.Skills)
	topicsRaw, _ := json.Marshal(state.Topics)
	outputsRaw, _ := json.Marshal(state.Outputs)

	cp := &Checkpoint{
		Phase:       fmt.Sprintf("agent_turn_%d", turnCount),
		TurnCount:   turnCount,
		MessagesRaw: string(msgsRaw),
		FilesRaw:    string(filesRaw),
		SkillsRaw:   string(skillsRaw),
		TopicsRaw:   string(topicsRaw),
		OutputsRaw:  string(outputsRaw),
	}
	return m.Save(cp)
}

// LoadAgentCheckpoint restores agent loop state from checkpoint.
// Returns (turnCount, messages, restored, error).
func (m *CheckpointManager) LoadAgentCheckpoint() (int, []*schema.Message, *AgentState, error) {
	cp, err := m.Load()
	if err != nil || cp == nil {
		return 0, nil, nil, err
	}

	// Restore messages
	var messages []*schema.Message
	if cp.MessagesRaw != "" {
		if err := json.Unmarshal([]byte(cp.MessagesRaw), &messages); err != nil {
			return 0, nil, nil, fmt.Errorf("unmarshal messages: %w", err)
		}
	}

	// Restore state
	state := &AgentState{
		Input: &CompileInput{},
		Task:  &TaskState{ID: m.taskID, Status: "running", Log: ""},
	}

	if cp.FilesRaw != "" {
		json.Unmarshal([]byte(cp.FilesRaw), &state.Files)
	}
	if cp.SkillsRaw != "" {
		json.Unmarshal([]byte(cp.SkillsRaw), &state.Skills)
	}
	if cp.TopicsRaw != "" {
		json.Unmarshal([]byte(cp.TopicsRaw), &state.Topics)
	}
	if cp.OutputsRaw != "" {
		json.Unmarshal([]byte(cp.OutputsRaw), &state.Outputs)
	}

	state.appendLog("[CP] Restored from turn %d (%d messages, %d outputs)\n",
		cp.TurnCount, len(messages), len(state.Outputs))

	return cp.TurnCount, messages, state, nil
}
