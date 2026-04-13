package bridge

import (
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
)

const (
	defaultConversationTTL      = 24 * time.Hour
	defaultConversationMaxItems = 1024
)

type ConversationState struct {
	ChatSessionID  string
	AgentSessionID string
	DefaultModel   string
	LastModel      agent.ModelRef
	DefaultAgent   string
	DefaultDirectory string
	BoundAt        time.Time
	LastSeenAt     time.Time
}

type ConversationStore interface {
	Get(chatSessionID string) (ConversationState, bool)
	PutBinding(chatSessionID string, agentSessionID string)
	SetDefaultModel(chatSessionID string, model string)
	SetLastModel(chatSessionID string, model agent.ModelRef)
	SetDefaultAgent(chatSessionID string, agent string)
	SetDefaultDirectory(chatSessionID string, directory string)
	Delete(chatSessionID string)
	Touch(chatSessionID string)
	List() []ConversationState
}
