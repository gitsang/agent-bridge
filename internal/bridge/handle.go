package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
)

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
	if latest, err := c.agentClient.GetLatestAssistantMessage(ctx, resolvedSessionID); err == nil && latest != nil {
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
		optfs = append(optfs, agent.PromptWithDirectory(resolvedDirectory))
	}
	if resolvedAgent != "" {
		optfs = append(optfs, agent.PromptWithAgent(resolvedAgent))
	}
	if !resolvedModelRef.IsZero() {
		optfs = append(optfs, agent.PromptWithModel(resolvedModelRef))
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

		msg, err := c.agentClient.GetLatestAssistantMessage(ctx, targetSessionID)
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
				messages, err := c.agentClient.GetMessages(ctx, resolvedSessionID)
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
