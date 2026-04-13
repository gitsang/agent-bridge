package bridge

import (
	"log/slog"

	"github.com/gitsang/agent-bridge/internal/agent"
	"github.com/gitsang/agent-bridge/internal/bridge/conversation_store"
)

type AgentBridge struct {
	logger            *slog.Logger
	agentClient       agent.Client
	conversationStore conversation_store.ConversationStore
	modelCache        *modelCache
}

func defaultAgentBridge() *AgentBridge {
	return &AgentBridge{
		logger:            slog.Default(),
		agentClient:       nil,
		conversationStore: conversation_store.NewMemoryConversationStore(0, 0),
		modelCache:        newModelCache(),
	}
}

type OptionFunc func(*AgentBridge)

func WithLogger(logger *slog.Logger) OptionFunc {
	return func(target *AgentBridge) {
		target.logger = logger
	}
}

func WithAgentClient(client agent.Client) OptionFunc {
	return func(target *AgentBridge) {
		target.agentClient = client
	}
}

func WithConversationStore(store conversation_store.ConversationStore) OptionFunc {
	return func(target *AgentBridge) {
		target.conversationStore = store
	}
}

func New(optfs ...OptionFunc) *AgentBridge {
	connector := defaultAgentBridge()
	for _, apply := range optfs {
		if apply == nil {
			continue
		}
		apply(connector)
	}
	return connector
}
