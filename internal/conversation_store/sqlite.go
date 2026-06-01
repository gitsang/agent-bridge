package conversation_store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/gitsang/agent-bridge/internal/types"
	_ "github.com/mattn/go-sqlite3"
)

type SQLiteConversationStore struct {
	db       *sql.DB
	ttl      time.Duration
	maxItems int
}

func NewSQLiteConversationStore(dbPath string, ttl time.Duration, maxItems int) (*SQLiteConversationStore, error) {
	resolvedDBPath := strings.TrimSpace(dbPath)
	if resolvedDBPath == "" {
		return nil, fmt.Errorf("sqlite conversation store db path is required")
	}

	resolvedTTL := resolveConversationTTL(ttl)
	resolvedMaxItems := resolveConversationMaxItems(maxItems)

	db, err := sql.Open("sqlite3", resolvedDBPath+"?_journal=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite conversation store: %w", err)
	}

	store := &SQLiteConversationStore{
		db:       db,
		ttl:      resolvedTTL,
		maxItems: resolvedMaxItems,
	}

	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate sqlite conversation store: %w", err)
	}

	return store, nil
}

func (s *SQLiteConversationStore) migrate() error {
	query := `
	CREATE TABLE IF NOT EXISTS conversations (
		chat_session_id TEXT PRIMARY KEY,
		agent_session_id TEXT NOT NULL DEFAULT '',
		default_model TEXT NOT NULL DEFAULT '',
		last_model_provider_id TEXT NOT NULL DEFAULT '',
		last_model_id TEXT NOT NULL DEFAULT '',
		default_agent TEXT NOT NULL DEFAULT '',
		default_directory TEXT NOT NULL DEFAULT '',
		bound_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_conversations_last_seen_at ON conversations(last_seen_at);
	`
	_, err := s.db.Exec(query)
	return err
}

func (s *SQLiteConversationStore) Get(chatSessionID string) (ConversationState, bool) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return ConversationState{}, false
	}

	now := time.Now()
	s.cleanupExpired(now)

	var state ConversationState
	err := s.db.QueryRow(`
		SELECT chat_session_id, agent_session_id, default_model, 
		       last_model_provider_id, last_model_id, default_agent,
		       default_directory, bound_at, last_seen_at
		FROM conversations
		WHERE chat_session_id = ?
	`, resolvedChatSessionID).Scan(
		&state.ChatSessionID, &state.AgentSessionID, &state.DefaultModel,
		&state.LastModel.ProviderID, &state.LastModel.ModelID, &state.DefaultAgent,
		&state.DefaultDirectory, &state.BoundAt, &state.LastSeenAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return ConversationState{}, false
		}
		return ConversationState{}, false
	}

	_, _ = s.db.Exec(`UPDATE conversations SET last_seen_at = ? WHERE chat_session_id = ?`, now, resolvedChatSessionID)
	state.LastSeenAt = now

	return state, true
}

func (s *SQLiteConversationStore) PutBinding(chatSessionID string, agentSessionID string) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	resolvedAgentSessionID := strings.TrimSpace(agentSessionID)
	if resolvedChatSessionID == "" || resolvedAgentSessionID == "" {
		return
	}

	now := time.Now()
	s.cleanupExpired(now)
	s.limitSize(now)

	_, err := s.db.Exec(`
		INSERT INTO conversations (chat_session_id, agent_session_id, bound_at, last_seen_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(chat_session_id) DO UPDATE SET
			agent_session_id = CASE 
				WHEN conversations.agent_session_id = '' THEN excluded.agent_session_id
				ELSE conversations.agent_session_id
			END,
			bound_at = CASE 
				WHEN conversations.agent_session_id = '' THEN excluded.bound_at
				ELSE conversations.bound_at
			END,
			last_seen_at = excluded.last_seen_at
	`, resolvedChatSessionID, resolvedAgentSessionID, now, now)
	if err != nil {
		return
	}
}

func (s *SQLiteConversationStore) SetDefaultModel(chatSessionID string, model string) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return
	}

	now := time.Now()
	s.cleanupExpired(now)
	s.limitSize(now)

	_, _ = s.db.Exec(`
		INSERT INTO conversations (chat_session_id, default_model, last_seen_at)
		VALUES (?, ?, ?)
		ON CONFLICT(chat_session_id) DO UPDATE SET
			default_model = excluded.default_model,
			last_seen_at = excluded.last_seen_at
	`, resolvedChatSessionID, strings.TrimSpace(model), now)
}

func (s *SQLiteConversationStore) SetLastModel(chatSessionID string, model types.ModelRef) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return
	}

	now := time.Now()
	s.cleanupExpired(now)
	s.limitSize(now)

	_, _ = s.db.Exec(`
		INSERT INTO conversations (chat_session_id, last_model_provider_id, last_model_id, last_seen_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(chat_session_id) DO UPDATE SET
			last_model_provider_id = excluded.last_model_provider_id,
			last_model_id = excluded.last_model_id,
			last_seen_at = excluded.last_seen_at
	`, resolvedChatSessionID, model.ProviderID, model.ModelID, now)
}

func (s *SQLiteConversationStore) SetDefaultAgent(chatSessionID string, agentName string) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return
	}

	now := time.Now()
	s.cleanupExpired(now)
	s.limitSize(now)

	_, _ = s.db.Exec(`
		INSERT INTO conversations (chat_session_id, default_agent, last_seen_at)
		VALUES (?, ?, ?)
		ON CONFLICT(chat_session_id) DO UPDATE SET
			default_agent = excluded.default_agent,
			last_seen_at = excluded.last_seen_at
	`, resolvedChatSessionID, strings.TrimSpace(agentName), now)
}

func (s *SQLiteConversationStore) SetDefaultDirectory(chatSessionID string, directory string) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return
	}

	now := time.Now()
	s.cleanupExpired(now)
	s.limitSize(now)

	_, _ = s.db.Exec(`
		INSERT INTO conversations (chat_session_id, default_directory, last_seen_at)
		VALUES (?, ?, ?)
		ON CONFLICT(chat_session_id) DO UPDATE SET
			default_directory = excluded.default_directory,
			last_seen_at = excluded.last_seen_at
	`, resolvedChatSessionID, strings.TrimSpace(directory), now)
}

func (s *SQLiteConversationStore) Delete(chatSessionID string) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return
	}

	_, _ = s.db.Exec(`DELETE FROM conversations WHERE chat_session_id = ?`, resolvedChatSessionID)
}

func (s *SQLiteConversationStore) Touch(chatSessionID string) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return
	}

	now := time.Now()
	_, _ = s.db.Exec(`UPDATE conversations SET last_seen_at = ? WHERE chat_session_id = ?`, now, resolvedChatSessionID)
}

func (s *SQLiteConversationStore) List() []ConversationState {
	now := time.Now()
	s.cleanupExpired(now)

	rows, err := s.db.Query(`
		SELECT chat_session_id, agent_session_id, default_model, 
		       last_model_provider_id, last_model_id, default_agent,
		       default_directory, bound_at, last_seen_at
		FROM conversations
		ORDER BY chat_session_id
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var items []ConversationState
	for rows.Next() {
		var state ConversationState
		if err := rows.Scan(
			&state.ChatSessionID, &state.AgentSessionID, &state.DefaultModel,
			&state.LastModel.ProviderID, &state.LastModel.ModelID, &state.DefaultAgent,
			&state.DefaultDirectory, &state.BoundAt, &state.LastSeenAt,
		); err != nil {
			continue
		}
		items = append(items, state)
	}

	return items
}

func (s *SQLiteConversationStore) ListActive(since time.Time) []ConversationState {
	rows, err := s.db.Query(`
		SELECT chat_session_id, agent_session_id, default_model, 
		       last_model_provider_id, last_model_id, default_agent,
		       default_directory, bound_at, last_seen_at
		FROM conversations
		WHERE last_seen_at >= ?
		ORDER BY last_seen_at DESC
	`, since)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var items []ConversationState
	for rows.Next() {
		var state ConversationState
		if err := rows.Scan(
			&state.ChatSessionID, &state.AgentSessionID, &state.DefaultModel,
			&state.LastModel.ProviderID, &state.LastModel.ModelID, &state.DefaultAgent,
			&state.DefaultDirectory, &state.BoundAt, &state.LastSeenAt,
		); err != nil {
			continue
		}
		items = append(items, state)
	}

	return items
}

func (s *SQLiteConversationStore) cleanupExpired(now time.Time) {
	cutoff := now.Add(-s.ttl)
	_, _ = s.db.Exec(`DELETE FROM conversations WHERE last_seen_at < ?`, cutoff)
}

func (s *SQLiteConversationStore) limitSize(now time.Time) {
	var count int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&count)
	if count < s.maxItems {
		return
	}

	_, _ = s.db.Exec(`
		DELETE FROM conversations 
		WHERE chat_session_id IN (
			SELECT chat_session_id FROM conversations 
			ORDER BY last_seen_at ASC 
			LIMIT ?
		)
	`, count-s.maxItems+1)
}

func (s *SQLiteConversationStore) Close() error {
	return s.db.Close()
}
