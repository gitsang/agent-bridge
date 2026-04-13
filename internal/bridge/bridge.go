package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
)

type OptionFunc func(*AgentBridge)

type modelCache struct {
	mu      sync.RWMutex
	entries map[agent.ModelRef]agent.ModelInfo
}

func newModelCache() *modelCache {
	return &modelCache{entries: map[agent.ModelRef]agent.ModelInfo{}}
}

func (c *modelCache) lookup(ref agent.ModelRef) (agent.ModelInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	info, ok := c.entries[ref]
	return info, ok
}

func (c *modelCache) refresh(ctx context.Context, client agent.Client, directory string) error {
	models, err := client.ListModels(ctx, directory)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range models {
		c.entries[m.ModelRef] = m
	}
	return nil
}

type AgentBridge struct {
	logger            *slog.Logger
	agentClient       agent.Client
	conversationStore ConversationStore
	modelCache        *modelCache
}

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

func WithConversationStore(store ConversationStore) OptionFunc {
	return func(target *AgentBridge) {
		target.conversationStore = store
	}
}

func New(optfs ...OptionFunc) *AgentBridge {
	connector := &AgentBridge{
		modelCache: newModelCache(),
	}

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

func (c *AgentBridge) humanizeModel(ctx context.Context, ref agent.ModelRef, directory string) string {
	if ref.IsZero() {
		return ""
	}
	info, ok := c.modelCache.lookup(ref)
	if !ok {
		_ = c.modelCache.refresh(ctx, c.agentClient, directory)
		info, ok = c.modelCache.lookup(ref)
	}
	if ok && info.ModelName != "" {
		name := info.ModelName
		if info.ProviderName != "" {
			name = info.ProviderName + "/" + name
		}
		return name
	}
	return ref.String()
}

func (c *AgentBridge) Handle(ctx context.Context, req *Message, reply ReplyFunc) error {
	if req == nil {
		return NewError(http.StatusBadRequest, "request is required")
	}

	if c.agentClient == nil {
		return NewError(http.StatusInternalServerError, "agent client is required")
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

	resolvedDirectory := firstNonEmpty(strings.TrimSpace(req.Agent.Directory), strings.TrimSpace(state.DefaultDirectory))
	resolvedModelSpec := firstNonEmpty(strings.TrimSpace(req.Agent.Model), strings.TrimSpace(state.DefaultModel))
	resolvedAgent := firstNonEmpty(strings.TrimSpace(req.Agent.Agent), strings.TrimSpace(state.DefaultAgent))
	resolvedSessionID := firstNonEmpty(strings.TrimSpace(req.Agent.SessionID), strings.TrimSpace(state.AgentSessionID))

	if resolvedSessionID == "" {
		createdSession, err := c.agentClient.CreateSession(ctx, agent.CreateSessionRequest{
			Title:     strings.TrimSpace(req.Agent.Title),
			Directory: resolvedDirectory,
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

	var resolvedModelRef agent.ModelRef
	if resolvedModelSpec != "" {
		ref, err := c.agentClient.ResolveModel(ctx, resolvedModelSpec, resolvedDirectory)
		if err != nil {
			return NewError(http.StatusBadRequest, err.Error())
		}
		resolvedModelRef = ref
	}

	optfs := make([]agent.PromptOptionFunc, 0, 3)
	if resolvedDirectory != "" {
		optfs = append(optfs, agent.WithPromptDirectory(resolvedDirectory))
	}
	if resolvedAgent != "" {
		optfs = append(optfs, agent.WithPromptAgent(resolvedAgent))
	}
	if !resolvedModelRef.IsZero() {
		optfs = append(optfs, agent.WithPromptModel(resolvedModelRef))
	}

	handle, err := c.agentClient.Prompt(ctx, resolvedSessionID, content, optfs...)
	if err != nil {
		return NewError(http.StatusBadGateway, err.Error())
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastResult *agent.Message

	for {
		select {
		case <-ctx.Done():
			if lastResult != nil {
				return c.saveConversationState(req, resolvedChatSessionID, resolvedSessionID, resolvedModelSpec, resolvedAgent, resolvedDirectory, lastResult)
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
				msg := c.buildReplyMessage(ctx, req, resolvedSessionID, resolvedModelSpec, resolvedAgent, resolvedDirectory, result)
				if err := reply(msg); err != nil {
					return err
				}
			}
			if lastResult == nil {
				return NewError(http.StatusBadGateway, "no reply received")
			}
			return c.saveConversationState(req, resolvedChatSessionID, resolvedSessionID, resolvedModelSpec, resolvedAgent, resolvedDirectory, lastResult)
		case <-ticker.C:
			results, err := c.agentClient.PollMessagesAfter(ctx, resolvedSessionID, afterCompletedAt)
			if err != nil {
				return NewError(http.StatusBadGateway, err.Error())
			}
			for _, result := range results {
				lastResult = result
				msg := c.buildReplyMessage(ctx, req, resolvedSessionID, resolvedModelSpec, resolvedAgent, resolvedDirectory, result)
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
		listing, err := c.listSessions(ctx, c.resolveDirectoryForList(req))
		if err != nil {
			return nil, NewError(http.StatusBadGateway, err.Error())
		}
		return &Message{Content: listing, Chat: req.Chat}, nil
	case "model":
		return c.handleModelCommand(ctx, req, invocation)
	case "agent":
		return c.handleAgentCommand(ctx, req, invocation)
	case "directory":
		return c.handleDirectoryCommand(req, invocation)
	case "help":
		return &Message{Content: c.helpText(invocation), Chat: req.Chat}, nil
	default:
		return nil, NewError(http.StatusBadRequest, fmt.Sprintf("unknown command: /%s", invocation.Positionals[0]))
	}
}

func (c *AgentBridge) handleNewCommand(ctx context.Context, req *Message, invocation *Invocation) (*Message, error) {
	directory := strings.TrimSpace(invocation.Flags["directory"])
	model := strings.TrimSpace(invocation.Flags["model"])
	agentName := strings.TrimSpace(invocation.Flags["agent"])
	title := strings.TrimSpace(invocation.Flags["title"])

	createdSession, err := c.agentClient.CreateSession(ctx, agent.CreateSessionRequest{Title: title, Directory: directory})
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
		if agentName != "" {
			c.conversationStore.SetDefaultAgent(resolvedChatSessionID, agentName)
		}
		if directory != "" {
			c.conversationStore.SetDefaultDirectory(resolvedChatSessionID, directory)
		}
	}

	return &Message{
		Content: fmt.Sprintf("Created new session: %s", strings.TrimSpace(createdSession.ID)),
		Chat:    req.Chat,
		Agent: AgentContext{
			SessionID: strings.TrimSpace(createdSession.ID),
			Title:     firstNonEmpty(strings.TrimSpace(createdSession.Title), title),
			Model:     model,
			Agent:     agentName,
			Directory: firstNonEmpty(strings.TrimSpace(createdSession.Directory), directory),
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
			return nil, NewError(http.StatusBadRequest, "agent session id is required")
		}
		targetSessionID := strings.TrimSpace(invocation.Positionals[2])
		if targetSessionID == "" {
			return nil, NewError(http.StatusBadRequest, "agent session id is required")
		}
		session, err := c.agentClient.GetSession(ctx, targetSessionID)
		if err != nil {
			return nil, NewError(http.StatusBadGateway, fmt.Sprintf("session not found: %s", targetSessionID))
		}
		c.conversationStore.PutBinding(resolvedChatSessionID, targetSessionID)

		resolvedDirectory := ""
		resolvedTitle := ""
		var lastModel agent.ModelRef
		if session != nil {
			resolvedDirectory = strings.TrimSpace(session.Directory)
			resolvedTitle = strings.TrimSpace(session.Title)
		}
		if resolvedDirectory != "" {
			c.conversationStore.SetDefaultDirectory(resolvedChatSessionID, resolvedDirectory)
		}

		msg, err := c.agentClient.GetSessionLatestAssistantMessage(ctx, targetSessionID)
		if err == nil && msg != nil {
			c.logger.Debug("session latest assistant message",
				slog.String("session_id", targetSessionID),
				slog.String("id", msg.ID),
				slog.String("provider_id", msg.Model.ProviderID),
				slog.String("model_id", msg.Model.ModelID),
			)
			lastModel = msg.Model
			if !lastModel.IsZero() {
				c.conversationStore.SetLastModel(resolvedChatSessionID, lastModel)
			}
		}

		state, _ := c.conversationStore.Get(resolvedChatSessionID)
		modelInfo := c.humanizeModel(ctx, lastModel, resolvedDirectory)
		if modelInfo == "" {
			modelInfo = strings.TrimSpace(state.DefaultModel)
		}
		return &Message{
			Content: fmt.Sprintf("Attached conversation to session %s", targetSessionID),
			Chat:    req.Chat,
			Agent: AgentContext{
				SessionID: targetSessionID,
				Title:     resolvedTitle,
				Model:     modelInfo,
				Directory: resolvedDirectory,
			},
		}, nil
	case "detach":
		if resolvedChatSessionID == "" {
			return nil, NewError(http.StatusBadRequest, "chat session id is required for /session detach")
		}
		c.conversationStore.Delete(resolvedChatSessionID)
		return &Message{Content: "Detached conversation from agent session", Chat: req.Chat}, nil
	case "current":
		if resolvedChatSessionID == "" {
			return nil, NewError(http.StatusBadRequest, "chat session id is required for /session current")
		}
		state, ok := c.conversationStore.Get(resolvedChatSessionID)
		if !ok {
			return &Message{Content: "No session binding for current conversation", Chat: req.Chat}, nil
		}

		resolvedSessionID := strings.TrimSpace(state.AgentSessionID)
		resolvedDirectory := strings.TrimSpace(state.DefaultDirectory)
		resolvedTitle := ""

		lastModel := state.LastModel

		if resolvedSessionID != "" {
			session, err := c.agentClient.GetSession(ctx, resolvedSessionID)
			if err != nil {
				return nil, NewError(http.StatusBadGateway, fmt.Sprintf("session not found: %s", resolvedSessionID))
			}
			if session != nil {
				resolvedTitle = strings.TrimSpace(session.Title)
				resolvedDirectory = firstNonEmpty(strings.TrimSpace(session.Directory), resolvedDirectory)
			}

			if lastModel.IsZero() {
				messages, err := c.agentClient.GetSessionMessages(ctx, resolvedSessionID)
				if err == nil && len(messages) > 0 {
					for i := len(messages) - 1; i >= 0; i-- {
						if messages[i].Role == "assistant" {
							lastModel = messages[i].Model
							break
						}
					}
				}
			}
		}

		currentState := state
		currentState.DefaultDirectory = resolvedDirectory

		modelInfo := c.humanizeModel(ctx, lastModel, resolvedDirectory)
		if modelInfo == "" {
			modelInfo = strings.TrimSpace(state.DefaultModel)
		}

		return &Message{
			Content: formatCurrentState(currentState),
			Chat:    req.Chat,
			Agent: AgentContext{
				SessionID: resolvedSessionID,
				Title:     resolvedTitle,
				Model:     modelInfo,
				Directory: resolvedDirectory,
			},
		}, nil
	case "list":
		directory := strings.TrimSpace(invocation.Flags["directory"])
		if directory == "" {
			directory = c.resolveDirectoryForList(req)
		}
		listing, err := c.listSessions(ctx, directory)
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
		return &Message{Content: fmt.Sprintf("Default model set to %s", resolvedModel), Chat: req.Chat, Agent: AgentContext{Model: resolvedModel}}, nil
	case "list":
		models, err := c.listModels(ctx, c.resolveDirectoryForList(req))
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
		return &Message{Content: fmt.Sprintf("Default agent set to %s", resolvedAgent), Chat: req.Chat, Agent: AgentContext{Agent: resolvedAgent}}, nil
	case "list":
		agents, err := c.listAgents(ctx, c.resolveDirectoryForList(req))
		if err != nil {
			return nil, NewError(http.StatusBadGateway, err.Error())
		}
		return &Message{Content: agents, Chat: req.Chat}, nil
	default:
		return nil, NewError(http.StatusBadRequest, fmt.Sprintf("unsupported agent command: %s", subcommand))
	}
}

func (c *AgentBridge) handleDirectoryCommand(req *Message, invocation *Invocation) (*Message, error) {
	if len(invocation.Positionals) < 2 {
		return nil, NewError(http.StatusBadRequest, "directory subcommand is required")
	}

	resolvedChatSessionID := strings.TrimSpace(req.Chat.SessionID)
	if resolvedChatSessionID == "" {
		return nil, NewError(http.StatusBadRequest, "chat session id is required for /directory set")
	}

	subcommand := strings.ToLower(strings.TrimSpace(invocation.Positionals[1]))
	if subcommand != "set" {
		return nil, NewError(http.StatusBadRequest, fmt.Sprintf("unsupported directory command: %s", subcommand))
	}

	if len(invocation.Positionals) < 3 {
		return nil, NewError(http.StatusBadRequest, "directory is required")
	}

	directory := strings.TrimSpace(invocation.Positionals[2])
	if directory == "" {
		return nil, NewError(http.StatusBadRequest, "directory is required")
	}

	c.conversationStore.SetDefaultDirectory(resolvedChatSessionID, directory)
	return &Message{Content: fmt.Sprintf("Default directory set to %s", directory), Chat: req.Chat, Agent: AgentContext{Directory: directory}}, nil
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

func (c *AgentBridge) listSessions(ctx context.Context, directory string) (string, error) {
	sessions, err := c.agentClient.ListSessions(ctx, strings.TrimSpace(directory))
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

func (c *AgentBridge) listModels(ctx context.Context, directory string) (string, error) {
	models, err := c.agentClient.ListModels(ctx, strings.TrimSpace(directory))
	if err != nil {
		return "", err
	}
	if len(models) == 0 {
		return "- (no models)", nil
	}

	builder := strings.Builder{}
	for _, model := range models {
		builder.WriteString("- ")
		builder.WriteString(model.String())
		if model.ModelName != "" {
			builder.WriteString(" (")
			builder.WriteString(model.ModelName)
			builder.WriteString(")")
		}
		builder.WriteString("\n")
	}

	return strings.TrimSpace(builder.String()), nil
}

func (c *AgentBridge) listAgents(ctx context.Context, directory string) (string, error) {
	agents, err := c.agentClient.ListAgents(ctx, strings.TrimSpace(directory))
	if err != nil {
		return "", err
	}
	if len(agents) == 0 {
		return "- (no agents)", nil
	}

	builder := strings.Builder{}
	for _, agentItem := range agents {
		builder.WriteString("- ")
		builder.WriteString(strings.TrimSpace(agentItem.Name))
		if strings.TrimSpace(agentItem.Mode) != "" {
			builder.WriteString(" (")
			builder.WriteString(strings.TrimSpace(agentItem.Mode))
			builder.WriteString(")")
		}
		if strings.TrimSpace(agentItem.Description) != "" {
			builder.WriteString(": ")
			builder.WriteString(strings.TrimSpace(agentItem.Description))
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
			return "Usage: /new [--model <provider/model|model>] [--agent <name>] [--directory <path>] [--title <title>]"
		case "session":
			return "Usage: /session <attach|detach|current|list> [args] [--directory <path>]"
		case "model":
			return "Usage: /model <set|list> [model]"
		case "agent":
			return "Usage: /agent <set|list> [name]"
		case "directory":
			return "Usage: /directory set <path>"
		}
	}

	return strings.Join([]string{
		"Available commands:",
		"- /new [--model <provider/model|model>] [--agent <name>] [--directory <path>] [--title <title>]",
		"- /session attach <agent-session-id>",
		"- /session detach",
		"- /session current",
		"- /session list [--directory <path>]",
		"- /model set <provider/model|model>",
		"- /model list",
		"- /agent set <name>",
		"- /agent list",
		"- /directory set <path>",
		"- /help [new|session|model|agent|directory]",
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
