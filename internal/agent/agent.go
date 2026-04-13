package agent

import (
	"context"
	"fmt"
)

type ModelRef struct {
	ProviderID string
	ModelID    string
}

func (m ModelRef) IsZero() bool {
	return m.ProviderID == "" || m.ModelID == ""
}

func (m ModelRef) String() string {
	return fmt.Sprintf("%s/%s", m.ProviderID, m.ModelID)
}

type Session struct {
	ID        string
	Title     string
	Directory string
}

type CreateSessionRequest struct {
	Title     string
	Directory string
}

type Message struct {
	ID          string
	SessionID   string
	Role        string
	Content     string
	CompletedAt float64

	Agent string
	Model ModelRef
}

type PromptOptions struct {
	Directory string
	Agent     string
	Model     ModelRef
}

type PromptOptionFunc func(*PromptOptions)

func WithPromptDirectory(directory string) PromptOptionFunc {
	return func(target *PromptOptions) {
		target.Directory = directory
	}
}

func WithPromptAgent(agent string) PromptOptionFunc {
	return func(target *PromptOptions) {
		target.Agent = agent
	}
}

func WithPromptModel(model ModelRef) PromptOptionFunc {
	return func(target *PromptOptions) {
		target.Model = model
	}
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
	ModelRef
	ProviderName string
	ModelName    string
}

type AgentInfo struct {
	Name        string
	Description string
	Mode        string
}

type Client interface {
	// Model
	ListModels(ctx context.Context, directory string) ([]ModelInfo, error)
	ResolveModel(ctx context.Context, spec, directory string) (ModelRef, error)
	ListAgents(ctx context.Context, directory string) ([]AgentInfo, error)

	// Session
	ListSessions(ctx context.Context, directory string) ([]Session, error)
	GetSession(ctx context.Context, sessionID string) (*Session, error)
	CreateSession(ctx context.Context, request CreateSessionRequest) (*Session, error)

	// Message
	GetSessionMessages(ctx context.Context, sessionID string) ([]Message, error)
	GetSessionLatestAssistantMessage(ctx context.Context, sessionID string) (*Message, error)
	Prompt(ctx context.Context, sessionID string, prompt string, optfs ...PromptOptionFunc) (*PromptHandle, error)
	PollMessagesAfter(ctx context.Context, sessionID string, afterCompletedAt float64) ([]*Message, error)
}
