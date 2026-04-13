package bridge

import (
	"context"
	"strings"

	"github.com/gitsang/agent-bridge/internal/agent"
	"github.com/gitsang/agent-bridge/internal/bridge/conversation_store"
)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		resolved := strings.TrimSpace(value)
		if resolved != "" {
			return resolved
		}
	}
	return ""
}

func formatCurrentState(state conversation_store.ConversationState) string {
	builder := strings.Builder{}
	builder.WriteString("Conversation state:")
	builder.WriteString("\n- chat session: ")
	builder.WriteString(strings.TrimSpace(state.ChatSessionID))
	builder.WriteString("\n- agent session: ")
	if strings.TrimSpace(state.AgentSessionID) == "" {
		builder.WriteString("(none)")
	} else {
		builder.WriteString(strings.TrimSpace(state.AgentSessionID))
	}
	builder.WriteString("\n- default model: ")
	if strings.TrimSpace(state.DefaultModel) == "" {
		builder.WriteString("(none)")
	} else {
		builder.WriteString(strings.TrimSpace(state.DefaultModel))
	}
	builder.WriteString("\n- default agent: ")
	if strings.TrimSpace(state.DefaultAgent) == "" {
		builder.WriteString("(none)")
	} else {
		builder.WriteString(strings.TrimSpace(state.DefaultAgent))
	}
	builder.WriteString("\n- default directory: ")
	if strings.TrimSpace(state.DefaultDirectory) == "" {
		builder.WriteString("(none)")
	} else {
		builder.WriteString(strings.TrimSpace(state.DefaultDirectory))
	}

	return builder.String()
}

func (c *AgentBridge) resolveDirectoryForList(req *Message) string {
	resolvedDirectory := strings.TrimSpace(req.Agent.Directory)
	if resolvedDirectory != "" {
		return resolvedDirectory
	}

	resolvedChatSessionID := strings.TrimSpace(req.Chat.SessionID)
	if resolvedChatSessionID == "" {
		return ""
	}
	state, ok := c.conversationStore.Get(resolvedChatSessionID)
	if !ok {
		return ""
	}
	return strings.TrimSpace(state.DefaultDirectory)
}

func (c *AgentBridge) buildReplyMessage(ctx context.Context, req *Message, resolvedSessionID, resolvedModelSpec, resolvedAgent, resolvedDirectory string, result *agent.Message) *Message {
	sessionID := firstNonEmpty(strings.TrimSpace(result.SessionID), resolvedSessionID)
	modelInfo := firstNonEmpty(c.humanizeModel(ctx, result.Model, resolvedDirectory), resolvedModelSpec)
	resolvedTitle := strings.TrimSpace(req.Agent.Title)
	finalDirectory := resolvedDirectory

	if sessionID != "" {
		session, err := c.agentClient.GetSession(ctx, sessionID)
		if err == nil && session != nil {
			resolvedTitle = firstNonEmpty(strings.TrimSpace(session.Title), resolvedTitle)
			finalDirectory = firstNonEmpty(strings.TrimSpace(session.Directory), finalDirectory)
		}
	}

	return &Message{
		Content: strings.TrimSpace(result.Content),
		Chat:    req.Chat,
		Agent: AgentContext{
			SessionID: sessionID,
			Title:     resolvedTitle,
			Model:     modelInfo,
			Agent:     resolvedAgent,
			Directory: finalDirectory,
		},
	}
}

func (c *AgentBridge) saveConversationState(_ *Message, resolvedChatSessionID, resolvedSessionID, resolvedModelSpec, resolvedAgent, resolvedDirectory string, result *agent.Message) error {
	responseSessionID := firstNonEmpty(strings.TrimSpace(result.SessionID), resolvedSessionID)
	if resolvedChatSessionID != "" {
		if responseSessionID != "" {
			c.conversationStore.PutBinding(resolvedChatSessionID, responseSessionID)
		}
		if resolvedModelSpec != "" {
			c.conversationStore.SetDefaultModel(resolvedChatSessionID, resolvedModelSpec)
		}
		if resolvedAgent != "" {
			c.conversationStore.SetDefaultAgent(resolvedChatSessionID, resolvedAgent)
		}
		if resolvedDirectory != "" {
			c.conversationStore.SetDefaultDirectory(resolvedChatSessionID, resolvedDirectory)
		}
		if !result.Model.IsZero() {
			c.conversationStore.SetLastModel(resolvedChatSessionID, result.Model)
		}
	}
	return nil
}
