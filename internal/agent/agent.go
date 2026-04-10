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

type PromptRequest struct {
	SessionID string
	Content   string
	Model     string
	Agent     string
	Workdir   string
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

type PromptResult struct {
	Reply       string
	SessionID   string
	Title       string
	Workdir     string
	ProviderID  string
	ModelID     string
	Mode        string
	CompletedAt float64
}

type SessionMessage struct {
	ID          string
	ProviderID  string
	ModelID     string
	Mode        string
	Role        string
	CompletedAt float64
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
	ListSessions(ctx context.Context, workdir string) ([]Session, error)
	ListModels(ctx context.Context, workdir string) ([]ModelInfo, error)
	ListAgents(ctx context.Context, workdir string) ([]AgentInfo, error)
	GetSession(ctx context.Context, sessionID string) (*Session, error)
	GetSessionMessages(ctx context.Context, sessionID string) ([]SessionMessage, error)
	GetSessionLatestAssistantMessage(ctx context.Context, sessionID string) (*SessionMessage, error)
	CreateSession(ctx context.Context, request CreateSessionRequest) (*Session, error)
	Prompt(ctx context.Context, request PromptRequest) (*PromptHandle, error)
	PollMessagesAfter(ctx context.Context, sessionID string, afterCompletedAt float64) ([]*PromptResult, error)
}
