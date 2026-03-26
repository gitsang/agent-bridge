package connect

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gitsang/opencode-connect/internal/opencode"
)

type sessionClient interface {
	ListSessions(ctx context.Context, workdir string) ([]opencode.Session, error)
	ListModels(ctx context.Context, workdir string) ([]opencode.ModelInfo, error)
	GetSession(ctx context.Context, sessionID string) (*opencode.Session, error)
	CreateSession(ctx context.Context, request opencode.CreateSessionRequest) (*opencode.Session, error)
	Prompt(ctx context.Context, request opencode.PromptRequest) (*opencode.PromptResult, error)
}

type OptionFunc func(*OpencodeConnect)

type OpencodeConnect struct {
	opencodeClient    sessionClient
	conversationStore ConversationStore
}

func WithOpencodeClient(client sessionClient) OptionFunc {
	return func(target *OpencodeConnect) {
		target.opencodeClient = client
	}
}

func WithConversationStore(store ConversationStore) OptionFunc {
	return func(target *OpencodeConnect) {
		target.conversationStore = store
	}
}

func New(optfs ...OptionFunc) *OpencodeConnect {
	connector := &OpencodeConnect{}

	for _, apply := range optfs {
		if apply == nil {
			continue
		}
		apply(connector)
	}

	if connector.conversationStore == nil {
		connector.conversationStore = NewMemoryConversationStore(0, 0)
	}

	return connector
}

func (c *OpencodeConnect) Handle(ctx context.Context, req *Message) (*Message, error) {
	if req == nil {
		return nil, NewError(http.StatusBadRequest, "request is required")
	}

	if c.opencodeClient == nil {
		return nil, NewError(http.StatusInternalServerError, "opencode client is required")
	}

	parsed, err := ParseInput(req.Content)
	if err != nil {
		return nil, NewError(http.StatusBadRequest, err.Error())
	}

	if parsed.Invocation != nil {
		resp, err := c.handleCommand(ctx, req, parsed.Invocation)
		if err != nil {
			return nil, err
		}
		if resp.Chat.SessionID == "" {
			resp.Chat = req.Chat
		}
		return resp, nil
	}

	return c.handlePrompt(ctx, req, parsed.Content)
}

func (c *OpencodeConnect) handlePrompt(ctx context.Context, req *Message, content string) (*Message, error) {
	resolvedChatSessionID := strings.TrimSpace(req.Chat.SessionID)
	state, hasState := c.conversationStore.Get(resolvedChatSessionID)

	resolvedWorkdir := firstNonEmpty(strings.TrimSpace(req.Opencode.Workdir), strings.TrimSpace(state.DefaultWorkdir))
	resolvedModel := firstNonEmpty(strings.TrimSpace(req.Opencode.Model), strings.TrimSpace(state.DefaultModel))
	resolvedSessionID := firstNonEmpty(strings.TrimSpace(req.Opencode.SessionID), strings.TrimSpace(state.OpencodeSessionID))

	if resolvedSessionID == "" {
		createdSession, err := c.opencodeClient.CreateSession(ctx, opencode.CreateSessionRequest{
			Title:   strings.TrimSpace(req.Opencode.Title),
			Workdir: resolvedWorkdir,
		})
		if err != nil {
			return nil, NewError(http.StatusBadGateway, err.Error())
		}
		if createdSession == nil || strings.TrimSpace(createdSession.ID) == "" {
			return nil, NewError(http.StatusBadGateway, "created session id is required")
		}
		resolvedSessionID = strings.TrimSpace(createdSession.ID)
	}

	result, err := c.opencodeClient.Prompt(ctx, opencode.PromptRequest{
		SessionID: resolvedSessionID,
		Content:   content,
		Model:     resolvedModel,
		Workdir:   resolvedWorkdir,
	})
	if err != nil {
		return nil, NewError(http.StatusBadGateway, err.Error())
	}

	responseSessionID := strings.TrimSpace(result.SessionID)
	if responseSessionID == "" {
		responseSessionID = resolvedSessionID
	}

	if resolvedChatSessionID != "" {
		if responseSessionID != "" {
			c.conversationStore.PutBinding(resolvedChatSessionID, responseSessionID)
		}
		if resolvedModel != "" {
			c.conversationStore.SetDefaultModel(resolvedChatSessionID, resolvedModel)
		}
		if resolvedWorkdir != "" {
			c.conversationStore.SetDefaultWorkdir(resolvedChatSessionID, resolvedWorkdir)
		}
	}

	response := &Message{
		Content: strings.TrimSpace(result.Reply),
		Chat:    req.Chat,
		Opencode: OpencodeContext{
			SessionID: responseSessionID,
			Title:     firstNonEmpty(strings.TrimSpace(result.Title), strings.TrimSpace(req.Opencode.Title)),
			Model:     firstNonEmpty(strings.TrimSpace(result.Model), resolvedModel),
			Workdir:   firstNonEmpty(strings.TrimSpace(result.Workdir), resolvedWorkdir),
		},
	}

	if !hasState && response.Chat.SessionID == "" {
		response.Chat = req.Chat
	}

	return response, nil
}

func (c *OpencodeConnect) handleCommand(ctx context.Context, req *Message, invocation *Invocation) (*Message, error) {
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
	case "workdir":
		return c.handleWorkdirCommand(req, invocation)
	case "help":
		return &Message{Content: c.helpText(invocation), Chat: req.Chat}, nil
	default:
		return nil, NewError(http.StatusBadRequest, fmt.Sprintf("unknown command: /%s", invocation.Positionals[0]))
	}
}

func (c *OpencodeConnect) handleNewCommand(ctx context.Context, req *Message, invocation *Invocation) (*Message, error) {
	workdir := strings.TrimSpace(invocation.Flags["work-dir"])
	model := strings.TrimSpace(invocation.Flags["model"])
	title := strings.TrimSpace(invocation.Flags["title"])

	createdSession, err := c.opencodeClient.CreateSession(ctx, opencode.CreateSessionRequest{Title: title, Workdir: workdir})
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
			Workdir:   firstNonEmpty(strings.TrimSpace(createdSession.Directory), workdir),
		},
	}, nil
}

func (c *OpencodeConnect) handleSessionCommand(ctx context.Context, req *Message, invocation *Invocation) (*Message, error) {
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
		if _, err := c.opencodeClient.GetSession(ctx, targetSessionID); err != nil {
			return nil, NewError(http.StatusBadGateway, fmt.Sprintf("session not found: %s", targetSessionID))
		}
		c.conversationStore.PutBinding(resolvedChatSessionID, targetSessionID)
		return &Message{Content: fmt.Sprintf("Attached conversation to session %s", targetSessionID), Chat: req.Chat, Opencode: OpencodeContext{SessionID: targetSessionID}}, nil
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
		return &Message{Content: formatCurrentState(state), Chat: req.Chat, Opencode: OpencodeContext{SessionID: strings.TrimSpace(state.OpencodeSessionID), Model: strings.TrimSpace(state.DefaultModel), Workdir: strings.TrimSpace(state.DefaultWorkdir)}}, nil
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

func (c *OpencodeConnect) handleModelCommand(ctx context.Context, req *Message, invocation *Invocation) (*Message, error) {
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

func (c *OpencodeConnect) handleWorkdirCommand(req *Message, invocation *Invocation) (*Message, error) {
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

func (c *OpencodeConnect) resolveWorkdirForList(req *Message) string {
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

func (c *OpencodeConnect) listSessions(ctx context.Context, workdir string) (string, error) {
	sessions, err := c.opencodeClient.ListSessions(ctx, strings.TrimSpace(workdir))
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

func (c *OpencodeConnect) listModels(ctx context.Context, workdir string) (string, error) {
	models, err := c.opencodeClient.ListModels(ctx, strings.TrimSpace(workdir))
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

func (c *OpencodeConnect) helpText(invocation *Invocation) string {
	if invocation != nil && len(invocation.Positionals) > 1 {
		topic := strings.ToLower(strings.TrimSpace(invocation.Positionals[1]))
		switch topic {
		case "new":
			return "Usage: /new [--model <provider/model|model>] [--work-dir <path>] [--title <title>]"
		case "session":
			return "Usage: /session <attach|detach|current|list> [args] [--work-dir <path>]"
		case "model":
			return "Usage: /model <set|list> [model]"
		case "workdir":
			return "Usage: /workdir set <path>"
		}
	}

	return strings.Join([]string{
		"Available commands:",
		"- /new [--model <provider/model|model>] [--work-dir <path>] [--title <title>]",
		"- /session attach <opencode-session-id>",
		"- /session detach",
		"- /session current",
		"- /session list [--work-dir <path>]",
		"- /model set <provider/model|model>",
		"- /model list",
		"- /workdir set <path>",
		"- /help [new|session|model|workdir]",
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
	builder.WriteString("\n- default workdir: ")
	if strings.TrimSpace(state.DefaultWorkdir) == "" {
		builder.WriteString("(none)")
	} else {
		builder.WriteString(strings.TrimSpace(state.DefaultWorkdir))
	}

	return builder.String()
}
