package conversation_store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gitsang/agent-bridge/internal/types"
)

type FileConversationStore struct {
	mu            sync.RWMutex
	conversations map[string]ConversationState
	ttl           time.Duration
	maxItems      int
	filePath      string
}

type persistedConversations struct {
	Conversations map[string]ConversationState `json:"conversations"`
}

func NewFileConversationStore(filePath string, ttl time.Duration, maxItems int) (*FileConversationStore, error) {
	resolvedFilePath := strings.TrimSpace(filePath)
	if resolvedFilePath == "" {
		return nil, fmt.Errorf("conversation store file path is required")
	}

	store := &FileConversationStore{
		conversations: map[string]ConversationState{},
		ttl:           resolveConversationTTL(ttl),
		maxItems:      resolveConversationMaxItems(maxItems),
		filePath:      resolvedFilePath,
	}

	if err := store.load(); err != nil {
		return nil, err
	}

	return store, nil
}

func (s *FileConversationStore) Get(chatSessionID string) (ConversationState, bool) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return ConversationState{}, false
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)

	state, ok := s.conversations[resolvedChatSessionID]
	if !ok {
		return ConversationState{}, false
	}

	state.LastSeenAt = now
	s.conversations[resolvedChatSessionID] = state
	s.persistLocked()

	return state, true
}

func (s *FileConversationStore) PutBinding(chatSessionID string, agentSessionID string) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	resolvedAgentSessionID := strings.TrimSpace(agentSessionID)
	if resolvedChatSessionID == "" || resolvedAgentSessionID == "" {
		return
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)

	state := s.ensureStateLocked(resolvedChatSessionID, now)
	if strings.TrimSpace(state.AgentSessionID) == "" {
		state.BoundAt = now
	}
	state.AgentSessionID = resolvedAgentSessionID
	state.LastSeenAt = now
	s.conversations[resolvedChatSessionID] = state
	s.persistLocked()
}

func (s *FileConversationStore) SetDefaultModel(chatSessionID string, model string) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)

	state := s.ensureStateLocked(resolvedChatSessionID, now)
	state.DefaultModel = strings.TrimSpace(model)
	state.LastSeenAt = now
	s.conversations[resolvedChatSessionID] = state
	s.persistLocked()
}

func (s *FileConversationStore) SetDefaultAgent(chatSessionID string, agent string) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)

	state := s.ensureStateLocked(resolvedChatSessionID, now)
	state.DefaultAgent = strings.TrimSpace(agent)
	state.LastSeenAt = now
	s.conversations[resolvedChatSessionID] = state
	s.persistLocked()
}

func (s *FileConversationStore) SetDefaultDirectory(chatSessionID string, directory string) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)

	state := s.ensureStateLocked(resolvedChatSessionID, now)
	state.DefaultDirectory = strings.TrimSpace(directory)
	state.LastSeenAt = now
	s.conversations[resolvedChatSessionID] = state
	s.persistLocked()
}

func (s *FileConversationStore) SetLastModel(chatSessionID string, model types.ModelRef) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)

	state := s.ensureStateLocked(resolvedChatSessionID, now)
	state.LastModel = model
	state.LastSeenAt = now
	s.conversations[resolvedChatSessionID] = state
	s.persistLocked()
}

func (s *FileConversationStore) Delete(chatSessionID string) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conversations, resolvedChatSessionID)
	s.persistLocked()
}

func (s *FileConversationStore) Touch(chatSessionID string) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)

	state, ok := s.conversations[resolvedChatSessionID]
	if !ok {
		return
	}
	state.LastSeenAt = now
	s.conversations[resolvedChatSessionID] = state
	s.persistLocked()
}

func (s *FileConversationStore) List() []ConversationState {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)

	items := make([]ConversationState, 0, len(s.conversations))
	for _, state := range s.conversations {
		items = append(items, state)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].ChatSessionID < items[j].ChatSessionID
	})

	return items
}

func (s *FileConversationStore) ListActive(since time.Time) []ConversationState {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)

	items := make([]ConversationState, 0, len(s.conversations))
	for _, state := range s.conversations {
		if state.LastSeenAt.After(since) || state.LastSeenAt.Equal(since) {
			items = append(items, state)
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].LastSeenAt.After(items[j].LastSeenAt)
	})

	return items
}

func (s *FileConversationStore) ensureStateLocked(chatSessionID string, now time.Time) ConversationState {
	state, ok := s.conversations[chatSessionID]
	if !ok {
		s.limitSizeLocked(now)
		state = ConversationState{ChatSessionID: chatSessionID, LastSeenAt: now}
		s.conversations[chatSessionID] = state
	}
	if state.ChatSessionID == "" {
		state.ChatSessionID = chatSessionID
	}
	if state.LastSeenAt.IsZero() {
		state.LastSeenAt = now
	}
	return state
}

func (s *FileConversationStore) cleanupExpiredLocked(now time.Time) {
	for key, state := range s.conversations {
		seenAt := state.LastSeenAt
		if seenAt.IsZero() {
			seenAt = state.BoundAt
		}
		if seenAt.IsZero() || now.Sub(seenAt) > s.ttl {
			delete(s.conversations, key)
		}
	}
}

func (s *FileConversationStore) limitSizeLocked(now time.Time) {
	if len(s.conversations) < s.maxItems {
		return
	}

	oldestKey := ""
	var oldestSeenAt time.Time
	for key, state := range s.conversations {
		seenAt := state.LastSeenAt
		if seenAt.IsZero() {
			seenAt = state.BoundAt
		}
		if seenAt.IsZero() {
			seenAt = now
		}
		if oldestKey == "" || seenAt.Before(oldestSeenAt) {
			oldestKey = key
			oldestSeenAt = seenAt
		}
	}

	if oldestKey != "" {
		delete(s.conversations, oldestKey)
	}
}

func (s *FileConversationStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read conversation store file: %w", err)
	}

	persisted := persistedConversations{}
	if err := json.Unmarshal(data, &persisted); err != nil {
		return fmt.Errorf("decode conversation store file: %w", err)
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if persisted.Conversations == nil {
		s.conversations = map[string]ConversationState{}
	} else {
		s.conversations = persisted.Conversations
	}
	s.cleanupExpiredLocked(now)
	for len(s.conversations) > s.maxItems {
		s.limitSizeLocked(now)
	}
	return nil
}

func (s *FileConversationStore) persistLocked() {
	persisted := persistedConversations{Conversations: s.conversations}
	data, err := json.Marshal(persisted)
	if err != nil {
		return
	}

	parentDir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return
	}

	tmpFile := s.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmpFile, s.filePath)
}
