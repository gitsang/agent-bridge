package bridge

import (
	"time"
)

const (
	defaultConversationTTL      = 24 * time.Hour
	defaultConversationMaxItems = 1024
)

type ConversationState struct {
	ChatSessionID  string
	AgentSessionID string
	DefaultModel   string
	DefaultAgent   string
	DefaultWorkdir string
	LastProviderID string
	LastModelID    string
	LastMode       string
	BoundAt        time.Time
	LastSeenAt     time.Time
}

type ConversationStore interface {
	Get(chatSessionID string) (ConversationState, bool)
	PutBinding(chatSessionID string, agentSessionID string)
	SetDefaultModel(chatSessionID string, model string)
	SetDefaultAgent(chatSessionID string, agent string)
	SetDefaultWorkdir(chatSessionID string, workdir string)
	SetLastModelInfo(chatSessionID string, providerID, modelID, mode string)
	Delete(chatSessionID string)
	Touch(chatSessionID string)
	List() []ConversationState
}
