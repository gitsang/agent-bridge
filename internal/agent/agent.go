package agent

import "context"

type Session struct {
	ID        string
	Title     string
	Directory string
}

type CreateSessionRequest struct {
	Title   string
	Workdir string
}

type Message struct {
	ID string

	Workdir string

	SessionID string
	Title     string

	Role       string
	Mode       string
	Agent      string
	Model      string
	ProviderID string
	ModelID    string

	Content     string
	CompletedAt float64
}

type PromptHandle struct {
	done <-chan struct{}
	err  <-chan error
}

func NewPromptHandle(done <-chan struct{}, err <-chan error) *PromptHandle {
	return &PromptHandle{done: done, err: err}
}

func (h *PromptHandle) Done() <-chan struct{} {
	return h.done
}

func (h *PromptHandle) Err() <-chan error {
	return h.err
}

type ModelInfo struct {
	ProviderID string
	ModelID    string
	Name       string
}

type AgentInfo struct {
	Name        string
	Description string
	Mode        string
}

type Client interface {
	// Model
	ListModels(ctx context.Context, workdir string) ([]ModelInfo, error)
	ListAgents(ctx context.Context, workdir string) ([]AgentInfo, error)

	// Session
	ListSessions(ctx context.Context, workdir string) ([]Session, error)
	GetSession(ctx context.Context, sessionID string) (*Session, error)
	CreateSession(ctx context.Context, request CreateSessionRequest) (*Session, error)

	// Message
	GetSessionMessages(ctx context.Context, sessionID string) ([]Message, error)
	GetSessionLatestAssistantMessage(ctx context.Context, sessionID string) (*Message, error)
	Prompt(ctx context.Context, request Message) (*PromptHandle, error)
	PollMessagesAfter(ctx context.Context, sessionID string, afterCompletedAt float64) ([]*Message, error)
}
