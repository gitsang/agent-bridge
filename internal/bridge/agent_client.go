package bridge

import (
	"context"

	"github.com/gitsang/agent-bridge/internal/agent"
)

// AgentClient defines the interface for interacting with an agent
type AgentClient interface {
	// Model operations
	ListModels(ctx context.Context, directory string) ([]agent.ModelInfo, error)
	ResolveModel(ctx context.Context, spec, directory string) (agent.ModelRef, error)

	// Agent operations
	ListAgents(ctx context.Context, directory string) ([]agent.AgentInfo, error)

	// Session operations
	ListSessions(ctx context.Context, directory string) ([]agent.Session, error)
	ListAllSessions(ctx context.Context) ([]agent.Session, error)
	GetSession(ctx context.Context, sessionID string) (*agent.Session, error)
	CreateSession(ctx context.Context, request agent.CreateSessionRequest) (*agent.Session, error)

	// Message operations
	GetMessages(ctx context.Context, sessionID string) ([]agent.Message, error)
	GetLatestAssistantMessage(ctx context.Context, sessionID string) (*agent.Message, error)
	Prompt(ctx context.Context, sessionID string, prompt string, opts ...agent.PromptOptionFunc) (*agent.PromptHandle, error)
	PollMessagesAfter(ctx context.Context, sessionID string, afterCompletedAt float64, output agent.MessageOutputOptions) ([]*agent.Message, error)

	// Interaction operations
	ListPendingPermissions(ctx context.Context, sessionID string) ([]agent.PermissionRequest, error)
	ReplyPermission(ctx context.Context, sessionID string, requestID string, reply agent.PermissionReply) error
	ListPendingQuestions(ctx context.Context, sessionID string) ([]agent.QuestionRequest, error)
	ReplyQuestion(ctx context.Context, sessionID string, requestID string, answers [][]string) error
	RejectQuestion(ctx context.Context, sessionID string, requestID string) error
}
