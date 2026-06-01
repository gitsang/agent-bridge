package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gitsang/agent-bridge/internal/agent"
)

type ParsedInput struct {
	Content    string
	Invocation *Invocation
}

type Invocation struct {
	Tokens      []string
	Positionals []string
	Flags       map[string]string
}

func ParseInput(content string) (*ParsedInput, error) {
	resolvedContent := strings.TrimSpace(content)
	if resolvedContent == "" {
		return nil, fmt.Errorf("message content cannot be empty")
	}

	if !strings.HasPrefix(resolvedContent, "/") {
		return &ParsedInput{Content: resolvedContent}, nil
	}

	tokens, err := tokenizeSlashContent(strings.TrimSpace(strings.TrimPrefix(resolvedContent, "/")))
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("slash command is required")
	}

	invocation, err := buildInvocation(tokens)
	if err != nil {
		return nil, err
	}

	return &ParsedInput{Invocation: invocation}, nil
}

func tokenizeSlashContent(content string) ([]string, error) {
	tokens := make([]string, 0, 8)
	builder := strings.Builder{}

	var quote rune
	escaped := false

	for _, current := range content {
		if escaped {
			builder.WriteRune(current)
			escaped = false
			continue
		}

		if quote != 0 {
			if current == quote {
				quote = 0
				continue
			}
			if current == '\\' && quote == '"' {
				escaped = true
				continue
			}
			builder.WriteRune(current)
			continue
		}

		switch {
		case current == '\\':
			escaped = true
		case current == '\'' || current == '"':
			quote = current
		case unicode.IsSpace(current):
			if builder.Len() == 0 {
				continue
			}
			tokens = append(tokens, builder.String())
			builder.Reset()
		default:
			builder.WriteRune(current)
		}
	}

	if escaped {
		return nil, fmt.Errorf("invalid trailing escape in command")
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in command")
	}
	if builder.Len() > 0 {
		tokens = append(tokens, builder.String())
	}

	return tokens, nil
}

func buildInvocation(tokens []string) (*Invocation, error) {
	flags := map[string]string{}
	positionals := make([]string, 0, len(tokens))

	for index := 0; index < len(tokens); index++ {
		current := strings.TrimSpace(tokens[index])
		if current == "" {
			continue
		}

		if current == "--" {
			positionals = append(positionals, tokens[index+1:]...)
			break
		}

		if !strings.HasPrefix(current, "--") {
			positionals = append(positionals, current)
			continue
		}

		flag := strings.TrimSpace(strings.TrimPrefix(current, "--"))
		if flag == "" {
			return nil, fmt.Errorf("invalid flag syntax")
		}

		if strings.Contains(flag, "=") {
			pair := strings.SplitN(flag, "=", 2)
			name := strings.TrimSpace(strings.ToLower(pair[0]))
			value := strings.TrimSpace(pair[1])
			if name == "" {
				return nil, fmt.Errorf("invalid flag syntax: %s", current)
			}
			flags[name] = value
			continue
		}

		name := strings.TrimSpace(strings.ToLower(flag))
		if name == "" {
			return nil, fmt.Errorf("invalid flag syntax: %s", current)
		}

		value := "true"
		if index+1 < len(tokens) && !strings.HasPrefix(strings.TrimSpace(tokens[index+1]), "--") {
			value = strings.TrimSpace(tokens[index+1])
			index++
		}
		flags[name] = value
	}

	return &Invocation{Tokens: tokens, Positionals: positionals, Flags: flags}, nil
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
		if resolvedChatSessionID != "" {
			c.conversationStore.PutBinding(resolvedChatSessionID, resolvedSessionID)
		}
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

	// Prepend user identity to prompt if configured
	if c.includeUserIdentity {
		userID := strings.TrimSpace(req.Chat.UserID)
		userName := strings.TrimSpace(req.Chat.UserName)
		if userID != "" || userName != "" {
			identity := userID
			if userName != "" {
				if identity != "" {
					identity = fmt.Sprintf("%s(%s)", userName, identity)
				} else {
					identity = userName
				}
			}
			if identity != "" {
				content = fmt.Sprintf("%s 说：%s", identity, content)
			}
		}
	}

	handle, err := c.agentClient.Prompt(ctx, resolvedSessionID, content, optfs...)
	if err != nil {
		return NewError(http.StatusBadGateway, err.Error())
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastResult *agent.Message
	deliveredInteractions := map[string]struct{}{}

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
			if err := c.forwardPendingInteractions(ctx, req, resolvedSessionID, deliveredInteractions, reply); err != nil {
				return err
			}
			results, err := c.agentClient.PollMessagesAfter(ctx, resolvedSessionID, afterCompletedAt, c.messageOutputOptions)
			if err != nil {
				return NewError(http.StatusBadGateway, err.Error())
			}
			for _, result := range results {
				lastResult = result
				afterCompletedAt = advanceCompletedCursor(afterCompletedAt, result)
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
			if err := c.forwardPendingInteractions(ctx, req, resolvedSessionID, deliveredInteractions, reply); err != nil {
				return err
			}
			results, err := c.agentClient.PollMessagesAfter(ctx, resolvedSessionID, afterCompletedAt, c.messageOutputOptions)
			if err != nil {
				return NewError(http.StatusBadGateway, err.Error())
			}
			for _, result := range results {
				lastResult = result
				afterCompletedAt = advanceCompletedCursor(afterCompletedAt, result)
				msg := c.buildReplyMessage(ctx, req, resolvedSessionID, resolvedModelSpec, resolvedAgent, resolvedDirectory, result)
				if err := reply(msg); err != nil {
					return err
				}
			}
		}
	}
}

func advanceCompletedCursor(current float64, msg *agent.Message) float64 {
	if msg == nil || msg.CompletedAt <= current {
		return current
	}
	return msg.CompletedAt
}

func (c *AgentBridge) forwardPendingInteractions(ctx context.Context, req *Message, sessionID string, delivered map[string]struct{}, reply ReplyFunc) error {
	permissions, err := c.agentClient.ListPendingPermissions(ctx, sessionID)
	if err != nil {
		return NewError(http.StatusBadGateway, err.Error())
	}
	for index, request := range permissions {
		key := "permission:" + strings.TrimSpace(request.ID)
		if _, ok := delivered[key]; ok {
			continue
		}
		delivered[key] = struct{}{}
		if err := reply(&Message{Content: formatPermissionRequest(index+1, request), Chat: req.Chat, Agent: AgentContext{SessionID: sessionID}}); err != nil {
			return err
		}
	}

	questions, err := c.agentClient.ListPendingQuestions(ctx, sessionID)
	if err != nil {
		return NewError(http.StatusBadGateway, err.Error())
	}
	for index, request := range questions {
		key := "question:" + strings.TrimSpace(request.ID)
		if _, ok := delivered[key]; ok {
			continue
		}
		delivered[key] = struct{}{}
		if err := reply(&Message{Content: formatQuestionRequest(index+1, request), Chat: req.Chat, Agent: AgentContext{SessionID: sessionID}}); err != nil {
			return err
		}
	}

	return nil
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
	case "permission":
		return c.handlePermissionCommand(ctx, req, invocation)
	case "question":
		return c.handleQuestionCommand(ctx, req, invocation)
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
		modelInfo := c.modelCache.Humanize(ctx, lastModel, c.agentClient, resolvedDirectory)
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

		modelInfo := c.modelCache.Humanize(ctx, lastModel, c.agentClient, resolvedDirectory)
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
		_, hasGlobal := invocation.Flags["global"]

		if hasGlobal {
			listing, err := c.listAllSessions(ctx)
			if err != nil {
				return nil, NewError(http.StatusBadGateway, err.Error())
			}
			return &Message{Content: listing, Chat: req.Chat}, nil
		}

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

func (c *AgentBridge) handlePermissionCommand(ctx context.Context, req *Message, invocation *Invocation) (*Message, error) {
	if len(invocation.Positionals) < 2 {
		return nil, NewError(http.StatusBadRequest, "usage: /permission <once|always|reject> [id|index]")
	}

	reply := agent.PermissionReply(strings.ToLower(strings.TrimSpace(invocation.Positionals[1])))
	if reply != agent.PermissionReplyOnce && reply != agent.PermissionReplyAlways && reply != agent.PermissionReplyReject {
		return nil, NewError(http.StatusBadRequest, "permission reply must be once, always, or reject")
	}

	sessionID, err := c.resolveInteractionSessionID(req)
	if err != nil {
		return nil, err
	}

	requests, err := c.agentClient.ListPendingPermissions(ctx, sessionID)
	if err != nil {
		return nil, NewError(http.StatusBadGateway, err.Error())
	}

	targetToken := ""
	if len(invocation.Positionals) > 2 {
		targetToken = strings.TrimSpace(invocation.Positionals[2])
	}
	target, message, err := selectPermissionRequest(requests, targetToken)
	if err != nil {
		return &Message{Content: message, Chat: req.Chat}, nil
	}

	if err := c.agentClient.ReplyPermission(ctx, sessionID, target.ID, reply); err != nil {
		if errors.Is(err, agent.ErrInteractionNoLongerPending) {
			return &Message{Content: fmt.Sprintf("Permission request no longer pending: %s", target.ID), Chat: req.Chat}, nil
		}
		return nil, NewError(http.StatusBadGateway, err.Error())
	}

	c.logger.Info("permission request replied",
		"chat_session_id", strings.TrimSpace(req.Chat.SessionID),
		"agent_session_id", sessionID,
		"request_id", target.ID,
		"reply", string(reply),
	)
	return &Message{Content: fmt.Sprintf("Permission request %s replied with %s", target.ID, reply), Chat: req.Chat}, nil
}

func (c *AgentBridge) handleQuestionCommand(ctx context.Context, req *Message, invocation *Invocation) (*Message, error) {
	if len(invocation.Positionals) < 2 {
		return nil, NewError(http.StatusBadRequest, "usage: /question [id|index] <answer...> or /question reject [id|index]")
	}

	tokens := invocation.Positionals[1:]
	sessionID, err := c.resolveInteractionSessionID(req)
	if err != nil {
		if !hasExplicitQuestionTarget(tokens) {
			return nil, err
		}
		sessionID = ""
	}

	requests, err := c.agentClient.ListPendingQuestions(ctx, sessionID)
	if err != nil {
		return nil, NewError(http.StatusBadGateway, err.Error())
	}

	if strings.EqualFold(strings.TrimSpace(tokens[0]), "reject") {
		targetToken := ""
		if len(tokens) > 1 {
			targetToken = strings.TrimSpace(tokens[1])
		}
		target, message, err := selectQuestionRequest(requests, targetToken)
		if err != nil {
			return &Message{Content: message, Chat: req.Chat}, nil
		}
		targetSessionID, err := resolveQuestionRequestSessionID(sessionID, target)
		if err != nil {
			return nil, NewError(http.StatusBadGateway, err.Error())
		}
		if err := c.agentClient.RejectQuestion(ctx, targetSessionID, target.ID); err != nil {
			if errors.Is(err, agent.ErrInteractionNoLongerPending) {
				return &Message{Content: fmt.Sprintf("Question request no longer pending: %s", target.ID), Chat: req.Chat}, nil
			}
			return nil, NewError(http.StatusBadGateway, err.Error())
		}
		c.logger.Info("question request rejected",
			"chat_session_id", strings.TrimSpace(req.Chat.SessionID),
			"agent_session_id", targetSessionID,
			"request_id", target.ID,
		)
		return &Message{Content: fmt.Sprintf("Question request %s rejected", target.ID), Chat: req.Chat}, nil
	}

	targetToken := ""
	answerTokens := tokens
	if len(requests) == 0 {
		return &Message{Content: "No pending question requests", Chat: req.Chat}, nil
	}
	if isQuestionRequestID(tokens[0]) {
		targetToken = normalizeInteractionID(tokens[0])
		answerTokens = tokens[1:]
	} else if len(requests) != 1 {
		if !matchesQuestionTarget(requests, tokens[0]) {
			return &Message{Content: formatPendingQuestions("Multiple pending question requests; include an id or index:", requests), Chat: req.Chat}, nil
		}
		targetToken = strings.TrimSpace(tokens[0])
		answerTokens = tokens[1:]
	}

	target, message, err := selectQuestionRequest(requests, targetToken)
	if err != nil {
		return &Message{Content: message, Chat: req.Chat}, nil
	}
	answers, err := buildQuestionAnswers(target, answerTokens)
	if err != nil {
		return nil, NewError(http.StatusBadRequest, err.Error())
	}

	targetSessionID, err := resolveQuestionRequestSessionID(sessionID, target)
	if err != nil {
		return nil, NewError(http.StatusBadGateway, err.Error())
	}
	if err := c.agentClient.ReplyQuestion(ctx, targetSessionID, target.ID, answers); err != nil {
		if errors.Is(err, agent.ErrInteractionNoLongerPending) {
			return &Message{Content: fmt.Sprintf("Question request no longer pending: %s", target.ID), Chat: req.Chat}, nil
		}
		return nil, NewError(http.StatusBadGateway, err.Error())
	}

	c.logger.Info("question request answered",
		"chat_session_id", strings.TrimSpace(req.Chat.SessionID),
		"agent_session_id", targetSessionID,
		"request_id", target.ID,
	)
	return &Message{Content: fmt.Sprintf("Question request %s answered", target.ID), Chat: req.Chat}, nil
}

func hasExplicitQuestionTarget(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(tokens[0]), "reject") {
		return len(tokens) > 1 && isQuestionRequestID(tokens[1])
	}
	return isQuestionRequestID(tokens[0])
}

func isQuestionRequestID(token string) bool {
	resolved := normalizeInteractionID(token)
	return strings.HasPrefix(resolved, "que") || strings.HasPrefix(resolved, "question-")
}

func normalizeInteractionID(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
}

func resolveQuestionRequestSessionID(fallback string, request agent.QuestionRequest) (string, error) {
	resolved := strings.TrimSpace(fallback)
	if resolved != "" {
		return resolved, nil
	}
	resolved = strings.TrimSpace(request.SessionID)
	if resolved == "" {
		return "", fmt.Errorf("question request session id is required")
	}
	return resolved, nil
}

func (c *AgentBridge) resolveInteractionSessionID(req *Message) (string, error) {
	resolvedSessionID := strings.TrimSpace(req.Agent.SessionID)
	resolvedChatSessionID := strings.TrimSpace(req.Chat.SessionID)
	if resolvedSessionID != "" {
		if resolvedChatSessionID != "" {
			if state, ok := c.conversationStore.Get(resolvedChatSessionID); ok {
				boundSessionID := strings.TrimSpace(state.AgentSessionID)
				if boundSessionID != "" && boundSessionID != resolvedSessionID {
					return "", NewError(http.StatusBadRequest, "request session does not match current conversation binding")
				}
			}
		}
		return resolvedSessionID, nil
	}

	if resolvedChatSessionID == "" {
		return "", NewError(http.StatusBadRequest, "chat session id is required")
	}
	state, ok := c.conversationStore.Get(resolvedChatSessionID)
	if !ok || strings.TrimSpace(state.AgentSessionID) == "" {
		return "", NewError(http.StatusBadRequest, "no agent session bound to current conversation")
	}
	return strings.TrimSpace(state.AgentSessionID), nil
}

func selectPermissionRequest(requests []agent.PermissionRequest, targetToken string) (agent.PermissionRequest, string, error) {
	if len(requests) == 0 {
		return agent.PermissionRequest{}, "No pending permission requests", fmt.Errorf("no pending permission requests")
	}
	if strings.TrimSpace(targetToken) == "" {
		if len(requests) == 1 {
			return requests[0], "", nil
		}
		return agent.PermissionRequest{}, formatPendingPermissions("Multiple pending permission requests; include an id or index:", requests), fmt.Errorf("permission target required")
	}

	if index, ok := parseInteractionIndex(targetToken, len(requests)); ok {
		return requests[index], "", nil
	}
	for _, request := range requests {
		if strings.TrimSpace(request.ID) == strings.TrimSpace(targetToken) {
			return request, "", nil
		}
	}
	return agent.PermissionRequest{}, fmt.Sprintf("Permission request no longer pending: %s", strings.TrimSpace(targetToken)), fmt.Errorf("permission request not found")
}

func selectQuestionRequest(requests []agent.QuestionRequest, targetToken string) (agent.QuestionRequest, string, error) {
	if len(requests) == 0 {
		return agent.QuestionRequest{}, "No pending question requests", fmt.Errorf("no pending question requests")
	}
	if strings.TrimSpace(targetToken) == "" {
		if len(requests) == 1 {
			return requests[0], "", nil
		}
		return agent.QuestionRequest{}, formatPendingQuestions("Multiple pending question requests; include an id or index:", requests), fmt.Errorf("question target required")
	}

	if index, ok := parseInteractionIndex(targetToken, len(requests)); ok {
		return requests[index], "", nil
	}
	resolvedTargetToken := normalizeInteractionID(targetToken)
	for _, request := range requests {
		if normalizeInteractionID(request.ID) == resolvedTargetToken {
			return request, "", nil
		}
	}
	return agent.QuestionRequest{}, fmt.Sprintf("Question request no longer pending: %s", strings.TrimSpace(targetToken)), fmt.Errorf("question request not found")
}

func parseInteractionIndex(token string, length int) (int, bool) {
	index, err := strconv.Atoi(strings.TrimSpace(token))
	if err != nil || index < 1 || index > length {
		return 0, false
	}
	return index - 1, true
}

func matchesQuestionTarget(requests []agent.QuestionRequest, token string) bool {
	if _, ok := parseInteractionIndex(token, len(requests)); ok {
		return true
	}
	return questionRequestIDMatches(requests, token)
}

func questionRequestIDMatches(requests []agent.QuestionRequest, token string) bool {
	resolvedToken := normalizeInteractionID(token)
	for _, request := range requests {
		if normalizeInteractionID(request.ID) == resolvedToken {
			return true
		}
	}
	return false
}

func buildQuestionAnswers(request agent.QuestionRequest, tokens []string) ([][]string, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("question answer is required")
	}
	questions := request.Questions
	if len(questions) == 0 {
		return [][]string{{strings.TrimSpace(strings.Join(tokens, " "))}}, nil
	}

	if len(questions) == 1 {
		answers := resolveSingleQuestionAnswers(questions[0], tokens)
		if len(answers) == 0 {
			return nil, fmt.Errorf("question answer is required")
		}
		return [][]string{answers}, nil
	}

	if len(tokens) != len(questions) {
		return nil, fmt.Errorf("expected %d answers", len(questions))
	}
	answers := make([][]string, 0, len(questions))
	for index, question := range questions {
		resolved := resolveQuestionTokens(question, []string{tokens[index]})
		if len(resolved) == 0 {
			return nil, fmt.Errorf("question answer is required")
		}
		answers = append(answers, resolved)
	}
	return answers, nil
}

func resolveSingleQuestionAnswers(question agent.Question, tokens []string) []string {
	if len(question.Options) == 0 {
		joined := strings.TrimSpace(strings.Join(tokens, " "))
		if joined == "" {
			return nil
		}
		return []string{joined}
	}
	return resolveQuestionTokens(question, tokens)
}

func resolveQuestionTokens(question agent.Question, tokens []string) []string {
	answers := make([]string, 0, len(tokens))
	for _, token := range tokens {
		trimmed := strings.TrimSpace(token)
		if trimmed == "" {
			continue
		}
		if optionIndex, ok := parseInteractionIndex(trimmed, len(question.Options)); ok {
			answers = append(answers, question.Options[optionIndex])
			continue
		}
		answers = append(answers, trimmed)
	}
	if len(answers) > 0 {
		return answers
	}
	joined := strings.TrimSpace(strings.Join(tokens, " "))
	if joined == "" {
		return nil
	}
	return []string{joined}
}

func formatPendingPermissions(header string, requests []agent.PermissionRequest) string {
	builder := strings.Builder{}
	builder.WriteString(header)
	for index, request := range requests {
		fmt.Fprintf(&builder, "\n%d. %s", index+1, strings.TrimSpace(request.ID))
		if strings.TrimSpace(request.Permission) != "" {
			fmt.Fprintf(&builder, " (%s)", strings.TrimSpace(request.Permission))
		}
	}
	return strings.TrimSpace(builder.String())
}

func formatPendingQuestions(header string, requests []agent.QuestionRequest) string {
	builder := strings.Builder{}
	builder.WriteString(header)
	for index, request := range requests {
		fmt.Fprintf(&builder, "\n%d. %s", index+1, strings.TrimSpace(request.ID))
		if len(request.Questions) > 0 && strings.TrimSpace(request.Questions[0].Text) != "" {
			fmt.Fprintf(&builder, " (%s)", strings.TrimSpace(request.Questions[0].Text))
		}
	}
	return strings.TrimSpace(builder.String())
}

func formatPermissionRequest(index int, request agent.PermissionRequest) string {
	builder := strings.Builder{}
	fmt.Fprintf(&builder, "Permission request %d: %s", index, strings.TrimSpace(request.ID))
	if strings.TrimSpace(request.Permission) != "" {
		fmt.Fprintf(&builder, "\nPermission: %s", strings.TrimSpace(request.Permission))
	}
	if len(request.Patterns) > 0 {
		fmt.Fprintf(&builder, "\nPatterns: %s", strings.Join(request.Patterns, ", "))
	}
	builder.WriteString("\n\nReply with:")
	fmt.Fprintf(&builder, "\n/permission once %s", strings.TrimSpace(request.ID))
	fmt.Fprintf(&builder, "\n/permission always %s", strings.TrimSpace(request.ID))
	fmt.Fprintf(&builder, "\n/permission reject %s", strings.TrimSpace(request.ID))
	return strings.TrimSpace(builder.String())
}

func formatQuestionRequest(index int, request agent.QuestionRequest) string {
	builder := strings.Builder{}
	requestID := strings.TrimSpace(request.ID)
	fmt.Fprintf(&builder, "Question request %d: %s", index, requestID)
	for questionIndex, question := range request.Questions {
		questionText := strings.TrimSpace(question.Text)
		if len(request.Questions) > 1 {
			fmt.Fprintf(&builder, "\n\nQuestion %d: %s", questionIndex+1, questionText)
		} else {
			fmt.Fprintf(&builder, "\n\n%s", questionText)
		}
		for optionIndex, option := range question.Options {
			fmt.Fprintf(&builder, "\n%d. %s", optionIndex+1, strings.TrimSpace(option))
		}
	}
	builder.WriteString("\n\nReply with:")
	if len(request.Questions) == 1 && len(request.Questions[0].Options) > 0 {
		for optionIndex := range request.Questions[0].Options {
			fmt.Fprintf(&builder, "\n/question <question-id> %d", optionIndex+1)
		}
	} else {
		builder.WriteString("\n/question <question-id> <answer>")
	}
	builder.WriteString("\n/question reject <question-id>")
	if requestID != "" {
		fmt.Fprintf(&builder, "\n\nTip: replace <question-id> with %s", requestID)
		fmt.Fprintf(&builder, "\n<question-id> = %s", requestID)
	}
	return strings.TrimSpace(builder.String())
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

func (c *AgentBridge) listAllSessions(ctx context.Context) (string, error) {
	sessions, err := c.agentClient.ListAllSessions(ctx)
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
			return "Usage: /session <attach|detach|current|list> [args] [--global] [--directory <path>]"
		case "model":
			return "Usage: /model <set|list> [model]"
		case "agent":
			return "Usage: /agent <set|list> [name]"
		case "directory":
			return "Usage: /directory set <path>"
		case "permission":
			return "Usage: /permission <once|always|reject> [id|index]"
		case "question":
			return "Usage: /question [id|index] <answer...>\n       /question reject [id|index]"
		}
	}

	return strings.Join([]string{
		"Available commands:",
		"- /new [--model <provider/model|model>] [--agent <name>] [--directory <path>] [--title <title>]",
		"- /session attach <agent-session-id>",
		"- /session detach",
		"- /session current",
		"- /session list [--global] [--directory <path>]",
		"- /model set <provider/model|model>",
		"- /model list",
		"- /agent set <name>",
		"- /agent list",
		"- /directory set <path>",
		"- /permission <once|always|reject> [id|index]",
		"- /question [id|index] <answer...>",
		"- /question reject [id|index]",
		"- /help [new|session|model|agent|directory|permission|question]",
	}, "\n")
}
