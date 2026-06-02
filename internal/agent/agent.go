package agent

import (
	"context"

	"github.com/gitsang/agent-bridge/internal/types"
)

type Agent interface {
	ListModels(ctx context.Context, directory string) ([]types.ModelInfo, error)
	ResolveModel(ctx context.Context, spec, directory string) (types.ModelRef, error)

	ListAgents(ctx context.Context, directory string) ([]types.AgentInfo, error)

	ListSessions(ctx context.Context, directory string) ([]types.Session, error)
	ListAllSessions(ctx context.Context) ([]types.Session, error)
	GetSession(ctx context.Context, sessionID string) (*types.Session, error)
	CreateSession(ctx context.Context, request types.CreateSessionRequest) (*types.Session, error)

	GetMessages(ctx context.Context, sessionID string) ([]types.Message, error)
	GetLatestAssistantMessage(ctx context.Context, sessionID string) (*types.Message, error)
	Prompt(ctx context.Context, sessionID string, prompt string, opts ...types.PromptOptionFunc) (*types.PromptHandle, error)
	PollMessagesAfter(ctx context.Context, sessionID string, afterCompletedAt float64, output types.MessageOutputOptions) ([]*types.Message, error)

	ListPendingPermissions(ctx context.Context, sessionID string) ([]types.PermissionRequest, error)
	ReplyPermission(ctx context.Context, sessionID string, requestID string, reply types.PermissionReply) error
	ListPendingQuestions(ctx context.Context, sessionID string) ([]types.QuestionRequest, error)
	ReplyQuestion(ctx context.Context, sessionID string, requestID string, answers [][]string) error
	RejectQuestion(ctx context.Context, sessionID string, requestID string) error
}
