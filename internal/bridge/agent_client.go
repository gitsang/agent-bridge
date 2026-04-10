package bridge

import (
	"context"
	"time"
)

const promptPollInterval = 2 * time.Second

// Session represents an agent session.
type Session struct {
	ID        string
	Title     string
	Directory string
}

// CreateSessionRequest is the request to create a new agent session.
type CreateSessionRequest struct {
	Title   string
	Workdir string
}

// PromptRequest is the request to send a prompt to an agent session.
type PromptRequest struct {
	SessionID string
	Content   string
	Model     string
	Agent     string
	Workdir   string
}

// PromptHandle represents an in-flight prompt operation.
type PromptHandle struct {
	done <-chan struct{}
	err  <-chan error
}

// NewPromptHandle creates a PromptHandle from the given channels.
func NewPromptHandle(done <-chan struct{}, err <-chan error) *PromptHandle {
	return &PromptHandle{done: done, err: err}
}

// Done returns a channel that is closed when the prompt completes.
func (h *PromptHandle) Done() <-chan struct{} {
	return h.done
}

// Err returns a channel that receives any error from the prompt.
func (h *PromptHandle) Err() <-chan error {
	return h.err
}

// PromptResult is the result of a completed prompt.
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

// SessionMessage is a message in an agent session.
type SessionMessage struct {
	ID          string
	ProviderID  string
	ModelID     string
	Mode        string
	Role        string
	CompletedAt float64
}

// ModelInfo describes an available model.
type ModelInfo struct {
	ProviderID string
	ModelID    string
	Name       string
}

// AgentInfo describes an available agent.
type AgentInfo struct {
	Name        string
	Description string
	Mode        string
}

// AgentClient is the interface for interacting with an underlying agent backend.
type AgentClient interface {
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
