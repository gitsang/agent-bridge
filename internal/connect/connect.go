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
	ListSessions(ctx context.Context) ([]opencode.Session, error)
	GetSession(ctx context.Context, sessionID string) (*opencode.Session, error)
	Prompt(ctx context.Context, sessionID string, message string) (*opencode.PromptResult, error)
}

type OptionFunc func(*OpencodeConnect)

type OpencodeConnect struct {
	opencodeClient sessionClient
}

func WithOpencodeClient(client sessionClient) OptionFunc {
	return func(target *OpencodeConnect) {
		target.opencodeClient = client
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

	return connector
}

func (c *OpencodeConnect) Handle(ctx context.Context, req *Message) (*Message, error) {
	if req == nil {
		return nil, NewError(http.StatusBadRequest, "request is required")
	}

	if strings.TrimSpace(req.SessionID) == "" {
		return nil, NewError(http.StatusBadRequest, "session_id is required")
	}
	if c.opencodeClient == nil {
		return nil, NewError(http.StatusInternalServerError, "opencode client is required")
	}

	parsed, err := ParseMessage(req.Message)
	if err != nil {
		return nil, NewError(http.StatusBadRequest, err.Error())
	}

	if parsed.SlashCommand == slashSessions {
		listing, err := c.listSessions(ctx)
		if err != nil {
			return nil, NewError(http.StatusBadGateway, err.Error())
		}

		return &Message{
			SessionID: req.SessionID,
			Message:   listing,
			Command:   slashSessions,
		}, nil
	}

	targetOpencodeSessionID := strings.TrimSpace(req.SessionID)
	if parsed.SessionCommand != "" {
		targetOpencodeSessionID = strings.TrimSpace(parsed.SessionCommand)
		if _, err := c.opencodeClient.GetSession(ctx, targetOpencodeSessionID); err != nil {
			return nil, NewError(http.StatusBadGateway, fmt.Sprintf("session not found: %s", targetOpencodeSessionID))
		}
	}

	result, err := c.opencodeClient.Prompt(ctx, targetOpencodeSessionID, parsed.Body)
	if err != nil {
		return nil, NewError(http.StatusBadGateway, err.Error())
	}

	responseSessionID := targetOpencodeSessionID
	if strings.TrimSpace(result.OpencodeSessionID) != "" {
		responseSessionID = strings.TrimSpace(result.OpencodeSessionID)
	}

	return &Message{
		SessionID: responseSessionID,
		Message:   result.Reply,
	}, nil
}

func (c *OpencodeConnect) listSessions(ctx context.Context) (string, error) {
	sessions, err := c.opencodeClient.ListSessions(ctx)
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
