package agent

import (
	"context"
	"fmt"
	"strings"
)

// Model

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

type ModelInfo struct {
	ModelRef
	ProviderName string
	ModelName    string
}

// Agent

type AgentInfo struct {
	Name        string
	Description string
	Mode        string
}

// Session

type Session struct {
	ID        string
	Title     string
	Directory string
}

type CreateSessionRequest struct {
	Title     string
	Directory string
}

// Message

type Message struct {
	ID          string
	SessionID   string
	Role        string
	Content     string
	CompletedAt float64

	Agent string
	Model ModelRef
}

type MessageContentKind string

const (
	MessageContentAnswer        MessageContentKind = "answer"
	MessageContentReasoning     MessageContentKind = "reasoning"
	MessageContentAction        MessageContentKind = "action"
	MessageContentActionTool    MessageContentKind = "action.tool"
	MessageContentActionAgent   MessageContentKind = "action.agent"
	MessageContentArtifact      MessageContentKind = "artifact"
	MessageContentArtifactFile  MessageContentKind = "artifact.file"
	MessageContentArtifactPatch MessageContentKind = "artifact.patch"
	MessageContentArtifactState MessageContentKind = "artifact.state"
	MessageContentDiagnostic    MessageContentKind = "diagnostic"
)

type MessageOutputOptions struct {
	Include []MessageContentKind `json:"include" yaml:"include" mapstructure:"include"`
}

func (o MessageOutputOptions) Includes(kind MessageContentKind) bool {
	resolvedKind := strings.TrimSpace(string(kind))
	if resolvedKind == "" {
		return false
	}
	if len(o.Include) == 0 {
		return true
	}

	for _, candidate := range o.Include {
		resolvedCandidate := strings.TrimSpace(string(candidate))
		if resolvedCandidate == "" {
			continue
		}
		if resolvedKind == resolvedCandidate || strings.HasPrefix(resolvedKind, resolvedCandidate+".") {
			return true
		}
	}
	return false
}

// Prompt

type PromptOptions struct {
	Directory string
	Agent     string
	Model     ModelRef
}

type PromptOptionFunc func(*PromptOptions)

func PromptWithDirectory(directory string) PromptOptionFunc {
	return func(target *PromptOptions) {
		target.Directory = directory
	}
}

func PromptWithAgent(agent string) PromptOptionFunc {
	return func(target *PromptOptions) {
		target.Agent = agent
	}
}

func PromptWithModel(model ModelRef) PromptOptionFunc {
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

type Client interface {
	// Model
	ListModels(ctx context.Context, directory string) ([]ModelInfo, error)
	ResolveModel(ctx context.Context, spec, directory string) (ModelRef, error)

	// Agents
	ListAgents(ctx context.Context, directory string) ([]AgentInfo, error)

	// Session
	ListSessions(ctx context.Context, directory string) ([]Session, error)
	GetSession(ctx context.Context, sessionID string) (*Session, error)
	CreateSession(ctx context.Context, request CreateSessionRequest) (*Session, error)

	// Message
	GetMessages(ctx context.Context, sessionID string) ([]Message, error)
	GetLatestAssistantMessage(ctx context.Context, sessionID string) (*Message, error)
	Prompt(ctx context.Context, sessionID string, prompt string, optfs ...PromptOptionFunc) (*PromptHandle, error)
	PollMessagesAfter(ctx context.Context, sessionID string, afterCompletedAt float64, output MessageOutputOptions) ([]*Message, error)
}
