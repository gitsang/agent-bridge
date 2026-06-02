package bridge

import (
	"log/slog"

	"github.com/gitsang/agent-bridge/internal/conversation"
	"github.com/gitsang/agent-bridge/internal/model_cache"
	"github.com/gitsang/agent-bridge/internal/types"
)

type AgentBridge struct {
	logger               *slog.Logger
	agent                Agent
	messageOutputOptions types.MessageOutputOptions
	conversationStore    conversation.Store
	modelCache           *model_cache.Cache
	includeUserIdentity  bool
}

func defaultAgentBridge() *AgentBridge {
	return &AgentBridge{
		logger:               slog.Default(),
		agent:                nil,
		messageOutputOptions: types.MessageOutputOptions{},
		conversationStore:    conversation.NewMemoryConversationStore(0, 0),
		modelCache:           model_cache.New(),
	}
}

type OptionFunc func(*AgentBridge)

func WithLogger(logger *slog.Logger) OptionFunc {
	return func(target *AgentBridge) {
		target.logger = logger
	}
}

func WithAgentClient(client Agent) OptionFunc {
	return func(target *AgentBridge) {
		target.agent = client
	}
}

func WithMessageOutputOptions(options types.MessageOutputOptions) OptionFunc {
	return func(target *AgentBridge) {
		target.messageOutputOptions = options
	}
}

func WithConversationStore(store conversation.Store) OptionFunc {
	return func(target *AgentBridge) {
		target.conversationStore = store
	}
}

func WithIncludeUserIdentity(include bool) OptionFunc {
	return func(target *AgentBridge) {
		target.includeUserIdentity = include
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
