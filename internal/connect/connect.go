package connect

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gitsang/opencode-connect/internal/opencode"
)

type AgentClient interface {
	ListSessions(ctx context.Context, workdir string) ([]opencode.Session, error)
	ListModels(ctx context.Context, workdir string) ([]opencode.ModelInfo, error)
	ListAgents(ctx context.Context, workdir string) ([]opencode.AgentInfo, error)
	GetSession(ctx context.Context, sessionID string) (*opencode.Session, error)
	GetSessionMessages(ctx context.Context, sessionID string) ([]opencode.SessionMessage, error)
	GetSessionLatestAssistantMessage(ctx context.Context, sessionID string) (*opencode.SessionMessage, error)
	CreateSession(ctx context.Context, request opencode.CreateSessionRequest) (*opencode.Session, error)
	Prompt(ctx context.Context, request opencode.PromptRequest) (*opencode.PromptHandle, error)
	PollMessagesAfter(ctx context.Context, sessionID string, afterCompletedAt float64) ([]*opencode.PromptResult, error)
}

type OptionFunc func(*AgentBridge)

type AgentBridge struct {
	logger            *slog.Logger
	agentClient       AgentClient
	conversationStore ConversationStore
}

func WithLogger(logger *slog.Logger) OptionFunc {
	return func(target *AgentBridge) {
		target.logger = logger
	}
}

func WithAgentClient(client AgentClient) OptionFunc {
	return func(target *AgentBridge) {
		target.agentClient = client
	}
}

func WithConversationStore(store ConversationStore) OptionFunc {
	return func(target *AgentBridge) {
		target.conversationStore = store
	}
}

func New(optfs ...OptionFunc) *AgentBridge {
	connector := &AgentBridge{}

	for _, apply := range optfs {
		if apply == nil {
			continue
		}
		apply(connector)
	}

	if connector.logger == nil {
		connector.logger = slog.Default()
	}

	if connector.conversationStore == nil {
		connector.conversationStore = NewMemoryConversationStore(0, 0)
	}

	return connector
}

func (c *AgentBridge) Handle(ctx context.Context, req *Message, reply ReplyFunc) error {
	if req == nil {
		return NewError(http.StatusBadRequest, "request is required")
	}

	if c.agentClient == nil {
		return NewError(http.StatusInternalServerError, "opencode client is required")
	}

	if reply == nil {
		reply = func(*Message) error { return nil }
	}

	parsed, err := ParseInput(req.Content)
	if err != nil {
		return NewError(http.StatusBadRequest, err.Error())
	}

	if parsed.Invocation != nil {
		resp, err := c.handleCommand(ctx, req, parsed.Invocation)
		if err != nil {
			return reply(&Message{
				Content: fmt.Sprintf("Error: %s", err.Error()),
				Chat:    req.Chat,
			})
		}
		if resp.Chat.SessionID == "" {
			resp.Chat = req.Chat
		}
		return reply(resp)
	}

	return c.handlePrompt(ctx, req, parsed.Content, reply)
}

func (c *AgentBridge) handlePrompt(ctx context.Context, req *Message, content string, reply ReplyFunc) error {
	resolvedChatSessionID := strings.TrimSpace(req.Chat.SessionID)
	state, _ := c.conversationStore.Get(resolvedChatSessionID)

	resolvedWorkdir := firstNonEmpty(strings.TrimSpace(req.Opencode.Workdir), strings.TrimSpace(state.DefaultWorkdir))
	resolvedModel := firstNonEmpty(strings.TrimSpace(req.Opencode.Model), strings.TrimSpace(state.DefaultModel))
	resolvedAgent := firstNonEmpty(strings.TrimSpace(req.Opencode.Agent), strings.TrimSpace(state.DefaultAgent))
	resolvedSessionID := firstNonEmpty(strings.TrimSpace(req.Opencode.SessionID), strings.TrimSpace(state.OpencodeSessionID))

	if resolvedSessionID == "" {
		createdSession, err := c.agentClient.CreateSession(ctx, opencode.CreateSessionRequest{
			Title:   strings.TrimSpace(req.Opencode.Title),
			Workdir: resolvedWorkdir,
		})
		if err != nil {
			return NewError(http.StatusBadGateway, err.Error())
		}
		if createdSession == nil || strings.TrimSpace(createdSession.ID) == "" {
			return NewError(http.StatusBadGateway, "created session id is required")
		}
		resolvedSessionID = strings.TrimSpace(createdSession.ID)
	}

	var afterCompletedAt float64
	if latest, err := c.agentClient.GetSessionLatestAssistantMessage(ctx, resolvedSessionID); err == nil && latest != nil {
		afterCompletedAt = latest.CompletedAt
	}

	handle, err := c.agentClient.Prompt(ctx, opencode.PromptRequest{
		SessionID: resolvedSessionID,
		Content:   content,
		Model:     resolvedModel,
		Agent:     resolvedAgent,
		Workdir:   resolvedWorkdir,
	})
	if err != nil {
		return NewError(http.StatusBadGateway, err.Error())
	}

	ticker := time.NewTicker(opencode.PromptPollInterval)
	defer ticker.Stop()

	var lastResult *opencode.PromptResult

	for {
		select {
		case <-ctx.Done():
			if lastResult != nil {
				return c.saveConversationState(req, resolvedChatSessionID, resolvedSessionID, resolvedModel, resolvedAgent, resolvedWorkdir, lastResult)
			}
			return ctx.Err()
		case err := <-handle.Err():
			return NewError(http.StatusBadGateway, err.Error())
		case <-handle.Done():
			results, err := c.agentClient.PollMessagesAfter(ctx, resolvedSessionID, afterCompletedAt)
			if err != nil {
				return NewError(http.StatusBadGateway, err.Error())
			}
			for _, result := range results {
				lastResult = result
				msg := c.buildReplyMessage(req, resolvedSessionID, resolvedModel, resolvedAgent, resolvedWorkdir, result)
				if err := reply(msg); err != nil {
					return err
				}
			}
			if lastResult == nil {
				return NewError(http.StatusBadGateway, "no reply received")
			}
			return c.saveConversationState(req, resolvedChatSessionID, resolvedSessionID, resolvedModel, resolvedAgent, resolvedWorkdir, lastResult)
		case <-ticker.C:
			results, err := c.agentClient.PollMessagesAfter(ctx, resolvedSessionID, afterCompletedAt)
			if err != nil {
				return NewError(http.StatusBadGateway, err.Error())
			}
			for _, result := range results {
				lastResult = result
				msg := c.buildReplyMessage(req, resolvedSessionID, resolvedModel, resolvedAgent, resolvedWorkdir, result)
				if err := reply(msg); err != nil {
					return err
				}
			}
			if lastResult != nil {
				afterCompletedAt = lastResult.CompletedAt
			}
		}
	}
}

func (c *AgentBridge) handleCommand(ctx context.Context, req *Message, invocation *Invocation) (*Message, error) {
	if invocation == nil || len(invocation.Positionals) == 0 {
		return nil, NewError(http.StatusBadRequest, "slash command is required")
	}

	switch strings.ToLower(strings.TrimSpace(invocation.Positionals[0])) {
	case "new":
		return c.handleNewCommand(ctx, req, invocation)
	case "session":
		return c.handleSessionCommand(ctx, req, invocation)
	case "sessions":
		listing, err := c.listSessions(ctx, c.resolveWorkdirForList(req))
		if err != nil {
			return nil, NewError(http.StatusBadGateway, err.Error())
		}
		return &Message{Content: listing, Chat: req.Chat}, nil
	case "model":
		return c.handleModelCommand(ctx, req, invocation)
	case "agent":
		return c.handleAgentCommand(ctx, req, invocation)
	case "workdir":
		return c.handleWorkdirCommand(req, invocation)
	case "help":
		return &Message{Content: c.helpText(invocation), Chat: req.Chat}, nil
	default:
		return nil, NewError(http.StatusBadRequest, fmt.Sprintf("unknown command: /%s", invocation.Positionals[0]))
	}
}

func (c *AgentBridge) handleNewCommand(ctx context.Context, req *Message, invocation *Invocation) (*Message, error) {
	workdir := strings.TrimSpace(invocation.Flags["work-dir"])
	model := strings.TrimSpace(invocation.Flags["model"])
	agent := strings.TrimSpace(invocation.Flags["agent"])
	title := strings.TrimSpace(invocation.Flags["title"])

	createdSession, err := c.agentClient.CreateSession(ctx, opencode.CreateSessionRequest{Title: title, Workdir: workdir})
	if err != nil {
		return nil, NewError(http.StatusBadGateway, err.Error())
	}
	if createdSession == nil || strings.TrimSpace(createdSession.ID) == "" {
		return nil, NewError(http.StatusBadGateway, "created session id is required")
	}

	resolvedChatSessionID := strings.TrimSpace(req.Chat.SessionID)
	if resolvedChatSessionID != "" {
		c.conversationStore.PutBinding(resolvedChatSessionID, strings.TrimSpace(createdSession.ID))
		if model != "" {
			c.conversationStore.SetDefaultModel(resolvedChatSessionID, model)
		}
		if agent != "" {
			c.conversationStore.SetDefaultAgent(resolvedChatSessionID, agent)
		}
		if workdir != "" {
			c.conversationStore.SetDefaultWorkdir(resolvedChatSessionID, workdir)
		}
	}

	return &Message{
		Content: fmt.Sprintf("Created new session: %s", strings.TrimSpace(createdSession.ID)),
		Chat:    req.Chat,
		Opencode: OpencodeContext{
			SessionID: strings.TrimSpace(createdSession.ID),
			Title:     firstNonEmpty(strings.TrimSpace(createdSession.Title), title),
			Model:     model,
			Agent:     agent,
			Workdir:   firstNonEmpty(strings.TrimSpace(createdSession.Directory), workdir),
		},
	}, nil
}

func (c *AgentBridge) handleSessionCommand(ctx context.Context, req *Message, invocation *Invocation) (*Message, error) {
	if len(invocation.Positionals) < 2 {
		return nil, NewError(http.StatusBadRequest, "session subcommand is required")
	}

	resolvedChatSessionID := strings.TrimSpace(req.Chat.SessionID)
	subcommand := strings.ToLower(strings.TrimSpace(invocation.Positionals[1]))

	switch subcommand {
	case "attach":
		if resolvedChatSessionID == "" {
			return nil, NewError(http.StatusBadRequest, "chat session id is required for /session attach")
		}
		if len(invocation.Positionals) < 3 {
			return nil, NewError(http.StatusBadRequest, "opencode session id is required")
		}
		targetSessionID := strings.TrimSpace(invocation.Positionals[2])
		if targetSessionID == "" {
			return nil, NewError(http.StatusBadRequest, "opencode session id is required")
		}
		session, err := c.agentClient.GetSession(ctx, targetSessionID)
		if err != nil {
			return nil, NewError(http.StatusBadGateway, fmt.Sprintf("session not found: %s", targetSessionID))
		}
		c.conversationStore.PutBinding(resolvedChatSessionID, targetSessionID)

		resolvedWorkdir := ""
		resolvedTitle := ""
		var lastProviderID, lastModelID, lastMode string
		if session != nil {
			resolvedWorkdir = strings.TrimSpace(session.Directory)
			resolvedTitle = strings.TrimSpace(session.Title)
		}
		if resolvedWorkdir != "" {
			c.conversationStore.SetDefaultWorkdir(resolvedChatSessionID, resolvedWorkdir)
		}

		msg, err := c.agentClient.GetSessionLatestAssistantMessage(ctx, targetSessionID)
		if err == nil && msg != nil {
			c.logger.Debug("session latest assistant message",
				slog.String("session_id", targetSessionID),
				slog.String("id", msg.ID),
				slog.String("provider_id", msg.ProviderID),
				slog.String("model_id", msg.ModelID),
				slog.String("mode", msg.Mode),
			)
			lastProviderID = msg.ProviderID
			lastModelID = msg.ModelID
			lastMode = msg.Mode
			if lastProviderID != "" || lastModelID != "" {
				c.conversationStore.SetLastModelInfo(resolvedChatSessionID, lastProviderID, lastModelID, lastMode)
			}
		}

		state, _ := c.conversationStore.Get(resolvedChatSessionID)
		modelInfo := formatModelInfo(lastProviderID, lastModelID, lastMode)
		if modelInfo == "" {
			modelInfo = strings.TrimSpace(state.DefaultModel)
		}
		return &Message{
			Content: fmt.Sprintf("Attached conversation to session %s", targetSessionID),
			Chat:    req.Chat,
			Opencode: OpencodeContext{
				SessionID: targetSessionID,
				Title:     resolvedTitle,
				Model:     modelInfo,
				Workdir:   resolvedWorkdir,
			},
		}, nil
	case "detach":
		if resolvedChatSessionID == "" {
			return nil, NewError(http.StatusBadRequest, "chat session id is required for /session detach")
		}
		c.conversationStore.Delete(resolvedChatSessionID)
		return &Message{Content: "Detached conversation from opencode session", Chat: req.Chat}, nil
	case "current":
		if resolvedChatSessionID == "" {
			return nil, NewError(http.StatusBadRequest, "chat session id is required for /session current")
		}
		state, ok := c.conversationStore.Get(resolvedChatSessionID)
		if !ok {
			return &Message{Content: "No session binding for current conversation", Chat: req.Chat}, nil
		}

		resolvedSessionID := strings.TrimSpace(state.OpencodeSessionID)
		resolvedWorkdir := strings.TrimSpace(state.DefaultWorkdir)
		resolvedTitle := ""

		var lastProviderID, lastModelID, lastMode string
		if strings.TrimSpace(state.LastProviderID) != "" || strings.TrimSpace(state.LastModelID) != "" {
			lastProviderID = strings.TrimSpace(state.LastProviderID)
			lastModelID = strings.TrimSpace(state.LastModelID)
			lastMode = strings.TrimSpace(state.LastMode)
		}

		if resolvedSessionID != "" {
			session, err := c.agentClient.GetSession(ctx, resolvedSessionID)
			if err != nil {
				return nil, NewError(http.StatusBadGateway, fmt.Sprintf("session not found: %s", resolvedSessionID))
			}
			if session != nil {
				resolvedTitle = strings.TrimSpace(session.Title)
				resolvedWorkdir = firstNonEmpty(strings.TrimSpace(session.Directory), resolvedWorkdir)
			}

			if lastProviderID == "" && lastModelID == "" {
				messages, err := c.agentClient.GetSessionMessages(ctx, resolvedSessionID)
				if err == nil && len(messages) > 0 {
					for i := len(messages) - 1; i >= 0; i-- {
						if messages[i].Role == "assistant" {
							lastProviderID = messages[i].ProviderID
							lastModelID = messages[i].ModelID
							lastMode = messages[i].Mode
							break
						}
					}
				}
			}
		}

		currentState := state
		currentState.DefaultWorkdir = resolvedWorkdir

		modelInfo := formatModelInfo(lastProviderID, lastModelID, lastMode)
		if modelInfo == "" {
			modelInfo = strings.TrimSpace(state.DefaultModel)
		}

		return &Message{
			Content: formatCurrentState(currentState),
			Chat:    req.Chat,
			Opencode: OpencodeContext{
				SessionID: resolvedSessionID,
				Title:     resolvedTitle,
				Model:     modelInfo,
				Workdir:   resolvedWorkdir,
			},
		}, nil
	case "list":
		workdir := strings.TrimSpace(invocation.Flags["work-dir"])
		if workdir == "" {
			workdir = c.resolveWorkdirForList(req)
		}
		listing, err := c.listSessions(ctx, workdir)
		if err != nil {
			return nil, NewError(http.StatusBadGateway, err.Error())
		}
		return &Message{Content: listing, Chat: req.Chat}, nil
	default:
		return nil, NewError(http.StatusBadRequest, fmt.Sprintf("unsupported session command: %s", subcommand))
	}
}

func (c *AgentBridge) handleModelCommand(ctx context.Context, req *Message, invocation *Invocation) (*Message, error) {
	if len(invocation.Positionals) < 2 {
		return nil, NewError(http.StatusBadRequest, "model subcommand is required")
	}

	resolvedChatSessionID := strings.TrimSpace(req.Chat.SessionID)
	subcommand := strings.ToLower(strings.TrimSpace(invocation.Positionals[1]))

	switch subcommand {
	case "set":
		if resolvedChatSessionID == "" {
			return nil, NewError(http.StatusBadRequest, "chat session id is required for /model set")
		}
		if len(invocation.Positionals) < 3 {
			return nil, NewError(http.StatusBadRequest, "model is required")
		}
		resolvedModel := strings.TrimSpace(invocation.Positionals[2])
		if resolvedModel == "" {
			return nil, NewError(http.StatusBadRequest, "model is required")
		}
		c.conversationStore.SetDefaultModel(resolvedChatSessionID, resolvedModel)
		return &Message{Content: fmt.Sprintf("Default model set to %s", resolvedModel), Chat: req.Chat, Opencode: OpencodeContext{Model: resolvedModel}}, nil
	case "list":
		models, err := c.listModels(ctx, c.resolveWorkdirForList(req))
		if err != nil {
			return nil, NewError(http.StatusBadGateway, err.Error())
		}
		return &Message{Content: models, Chat: req.Chat}, nil
	default:
		return nil, NewError(http.StatusBadRequest, fmt.Sprintf("unsupported model command: %s", subcommand))
	}
}

func (c *AgentBridge) handleAgentCommand(ctx context.Context, req *Message, invocation *Invocation) (*Message, error) {
	if len(invocation.Positionals) < 2 {
		return nil, NewError(http.StatusBadRequest, "agent subcommand is required")
	}

	resolvedChatSessionID := strings.TrimSpace(req.Chat.SessionID)
	subcommand := strings.ToLower(strings.TrimSpace(invocation.Positionals[1]))

	switch subcommand {
	case "set":
		if resolvedChatSessionID == "" {
			return nil, NewError(http.StatusBadRequest, "chat session id is required for /agent set")
		}
		if len(invocation.Positionals) < 3 {
			return nil, NewError(http.StatusBadRequest, "agent is required")
		}
		resolvedAgent := strings.TrimSpace(invocation.Positionals[2])
		if resolvedAgent == "" {
			return nil, NewError(http.StatusBadRequest, "agent is required")
		}
		c.conversationStore.SetDefaultAgent(resolvedChatSessionID, resolvedAgent)
		return &Message{Content: fmt.Sprintf("Default agent set to %s", resolvedAgent), Chat: req.Chat, Opencode: OpencodeContext{Agent: resolvedAgent}}, nil
	case "list":
		agents, err := c.listAgents(ctx, c.resolveWorkdirForList(req))
		if err != nil {
			return nil, NewError(http.StatusBadGateway, err.Error())
		}
		return &Message{Content: agents, Chat: req.Chat}, nil
	default:
		return nil, NewError(http.StatusBadRequest, fmt.Sprintf("unsupported agent command: %s", subcommand))
	}
}

func (c *AgentBridge) handleWorkdirCommand(req *Message, invocation *Invocation) (*Message, error) {
	if len(invocation.Positionals) < 2 {
		return nil, NewError(http.StatusBadRequest, "workdir subcommand is required")
	}

	resolvedChatSessionID := strings.TrimSpace(req.Chat.SessionID)
	if resolvedChatSessionID == "" {
		return nil, NewError(http.StatusBadRequest, "chat session id is required for /workdir set")
	}

	subcommand := strings.ToLower(strings.TrimSpace(invocation.Positionals[1]))
	if subcommand != "set" {
		return nil, NewError(http.StatusBadRequest, fmt.Sprintf("unsupported workdir command: %s", subcommand))
	}

	if len(invocation.Positionals) < 3 {
		return nil, NewError(http.StatusBadRequest, "workdir is required")
	}

	workdir := strings.TrimSpace(invocation.Positionals[2])
	if workdir == "" {
		return nil, NewError(http.StatusBadRequest, "workdir is required")
	}

	c.conversationStore.SetDefaultWorkdir(resolvedChatSessionID, workdir)
	return &Message{Content: fmt.Sprintf("Default workdir set to %s", workdir), Chat: req.Chat, Opencode: OpencodeContext{Workdir: workdir}}, nil
}

func (c *AgentBridge) resolveWorkdirForList(req *Message) string {
	resolvedWorkdir := strings.TrimSpace(req.Opencode.Workdir)
	if resolvedWorkdir != "" {
		return resolvedWorkdir
	}

	resolvedChatSessionID := strings.TrimSpace(req.Chat.SessionID)
	if resolvedChatSessionID == "" {
		return ""
	}
	state, ok := c.conversationStore.Get(resolvedChatSessionID)
	if !ok {
		return ""
	}
	return strings.TrimSpace(state.DefaultWorkdir)
}

func (c *AgentBridge) listSessions(ctx context.Context, workdir string) (string, error) {
	sessions, err := c.agentClient.ListSessions(ctx, strings.TrimSpace(workdir))
	if err != nil {
		return "", err
	}

	if len(sessions) == 0 {
		return "- (no sessions)", nil
	}

	byDirectory := map[string][]string{}
	for _, currentSession := range sessions {
		directory := strings.TrimSpace(currentSession.Directory)
		if directory == "" {
			directory = "."
		}

		title := strings.TrimSpace(currentSession.Title)
		if title == "" {
			title = "Untitled"
		}

		line := fmt.Sprintf("  - %s (%s)", title, currentSession.ID)
		byDirectory[directory] = append(byDirectory[directory], line)
	}

	directories := make([]string, 0, len(byDirectory))
	for directory := range byDirectory {
		directories = append(directories, directory)
	}
	sort.Strings(directories)

	builder := strings.Builder{}
	for index, directory := range directories {
		if index > 0 {
			builder.WriteString("\n")
		}

		builder.WriteString("- ")
		builder.WriteString(directory)
		builder.WriteString("\n")

		items := byDirectory[directory]
		sort.Strings(items)
		for _, item := range items {
			builder.WriteString(item)
			builder.WriteString("\n")
		}
	}

	return strings.TrimSpace(builder.String()), nil
}

func (c *AgentBridge) listModels(ctx context.Context, workdir string) (string, error) {
	models, err := c.agentClient.ListModels(ctx, strings.TrimSpace(workdir))
	if err != nil {
		return "", err
	}
	if len(models) == 0 {
		return "- (no models)", nil
	}

	builder := strings.Builder{}
	for _, model := range models {
		builder.WriteString("- ")
		builder.WriteString(model.ProviderID)
		builder.WriteString("/")
		builder.WriteString(model.ModelID)
		if strings.TrimSpace(model.Name) != "" {
			builder.WriteString(" (")
			builder.WriteString(model.Name)
			builder.WriteString(")")
		}
		builder.WriteString("\n")
	}

	return strings.TrimSpace(builder.String()), nil
}

func (c *AgentBridge) listAgents(ctx context.Context, workdir string) (string, error) {
	agents, err := c.agentClient.ListAgents(ctx, strings.TrimSpace(workdir))
	if err != nil {
		return "", err
	}
	if len(agents) == 0 {
		return "- (no agents)", nil
	}

	builder := strings.Builder{}
	for _, agent := range agents {
		builder.WriteString("- ")
		builder.WriteString(strings.TrimSpace(agent.Name))
		if strings.TrimSpace(agent.Mode) != "" {
			builder.WriteString(" (")
			builder.WriteString(strings.TrimSpace(agent.Mode))
			builder.WriteString(")")
		}
		if strings.TrimSpace(agent.Description) != "" {
			builder.WriteString(": ")
			builder.WriteString(strings.TrimSpace(agent.Description))
		}
		builder.WriteString("\n")
	}

	return strings.TrimSpace(builder.String()), nil
}

func (c *AgentBridge) helpText(invocation *Invocation) string {
	if invocation != nil && len(invocation.Positionals) > 1 {
		topic := strings.ToLower(strings.TrimSpace(invocation.Positionals[1]))
		switch topic {
		case "new":
			return "Usage: /new [--model <provider/model|model>] [--agent <name>] [--work-dir <path>] [--title <title>]"
		case "session":
			return "Usage: /session <attach|detach|current|list> [args] [--work-dir <path>]"
		case "model":
			return "Usage: /model <set|list> [model]"
		case "agent":
			return "Usage: /agent <set|list> [name]"
		case "workdir":
			return "Usage: /workdir set <path>"
		}
	}

	return strings.Join([]string{
		"Available commands:",
		"- /new [--model <provider/model|model>] [--agent <name>] [--work-dir <path>] [--title <title>]",
		"- /session attach <opencode-session-id>",
		"- /session detach",
		"- /session current",
		"- /session list [--work-dir <path>]",
		"- /model set <provider/model|model>",
		"- /model list",
		"- /agent set <name>",
		"- /agent list",
		"- /workdir set <path>",
		"- /help [new|session|model|agent|workdir]",
	}, "\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		resolved := strings.TrimSpace(value)
		if resolved != "" {
			return resolved
		}
	}
	return ""
}

func formatCurrentState(state ConversationState) string {
	builder := strings.Builder{}
	builder.WriteString("Conversation state:")
	builder.WriteString("\n- chat session: ")
	builder.WriteString(strings.TrimSpace(state.ChatSessionID))
	builder.WriteString("\n- opencode session: ")
	if strings.TrimSpace(state.OpencodeSessionID) == "" {
		builder.WriteString("(none)")
	} else {
		builder.WriteString(strings.TrimSpace(state.OpencodeSessionID))
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
	builder.WriteString("\n- default workdir: ")
	if strings.TrimSpace(state.DefaultWorkdir) == "" {
		builder.WriteString("(none)")
	} else {
		builder.WriteString(strings.TrimSpace(state.DefaultWorkdir))
	}

	return builder.String()
}

func formatModelInfo(providerID, modelID, mode string) string {
	parts := make([]string, 0, 2)
	if strings.TrimSpace(providerID) != "" && strings.TrimSpace(modelID) != "" {
		parts = append(parts, fmt.Sprintf("%s/%s", strings.TrimSpace(providerID), strings.TrimSpace(modelID)))
	} else if strings.TrimSpace(modelID) != "" {
		parts = append(parts, strings.TrimSpace(modelID))
	}
	if strings.TrimSpace(mode) != "" {
		parts = append(parts, fmt.Sprintf("[%s]", strings.TrimSpace(mode)))
	}
	return strings.Join(parts, " ")
}

func (c *AgentBridge) buildReplyMessage(req *Message, resolvedSessionID, resolvedModel, resolvedAgent, resolvedWorkdir string, result *opencode.PromptResult) *Message {
	sessionID := firstNonEmpty(strings.TrimSpace(result.SessionID), resolvedSessionID)
	return &Message{
		Content: strings.TrimSpace(result.Reply),
		Chat:    req.Chat,
		Opencode: OpencodeContext{
			SessionID: sessionID,
			Title:     firstNonEmpty(strings.TrimSpace(result.Title), strings.TrimSpace(req.Opencode.Title)),
			Model:     firstNonEmpty(formatModelInfo(result.ProviderID, result.ModelID, result.Mode), resolvedModel),
			Agent:     resolvedAgent,
			Workdir:   firstNonEmpty(strings.TrimSpace(result.Workdir), resolvedWorkdir),
		},
	}
}

func (c *AgentBridge) saveConversationState(req *Message, resolvedChatSessionID, resolvedSessionID, resolvedModel, resolvedAgent, resolvedWorkdir string, result *opencode.PromptResult) error {
	responseSessionID := firstNonEmpty(strings.TrimSpace(result.SessionID), resolvedSessionID)
	if resolvedChatSessionID != "" {
		if responseSessionID != "" {
			c.conversationStore.PutBinding(resolvedChatSessionID, responseSessionID)
		}
		if resolvedModel != "" {
			c.conversationStore.SetDefaultModel(resolvedChatSessionID, resolvedModel)
		}
		if resolvedAgent != "" {
			c.conversationStore.SetDefaultAgent(resolvedChatSessionID, resolvedAgent)
		}
		if resolvedWorkdir != "" {
			c.conversationStore.SetDefaultWorkdir(resolvedChatSessionID, resolvedWorkdir)
		}
		if result.ProviderID != "" || result.ModelID != "" {
			c.conversationStore.SetLastModelInfo(resolvedChatSessionID, result.ProviderID, result.ModelID, result.Mode)
		}
	}
	return nil
}
