package bridge

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
)

type MemoryConversationStore struct {
	mu            sync.RWMutex
	conversations map[string]ConversationState
	ttl           time.Duration
	maxItems      int
}

func NewMemoryConversationStore(ttl time.Duration, maxItems int) *MemoryConversationStore {
	resolvedTTL := resolveConversationTTL(ttl)
	resolvedMaxItems := resolveConversationMaxItems(maxItems)

	return &MemoryConversationStore{
		conversations: map[string]ConversationState{},
		ttl:           resolvedTTL,
		maxItems:      resolvedMaxItems,
	}
}

func resolveConversationTTL(ttl time.Duration) time.Duration {
	resolvedTTL := ttl
	if resolvedTTL <= 0 {
		resolvedTTL = defaultConversationTTL
	}
	return resolvedTTL
}

func resolveConversationMaxItems(maxItems int) int {
	resolvedMaxItems := maxItems
	if resolvedMaxItems <= 0 {
		resolvedMaxItems = defaultConversationMaxItems
	}
	return resolvedMaxItems
}

func (s *MemoryConversationStore) Get(chatSessionID string) (ConversationState, bool) {
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
	return state, true
}

func (s *MemoryConversationStore) PutBinding(chatSessionID string, agentSessionID string) {
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
}

func (s *MemoryConversationStore) SetDefaultModel(chatSessionID string, model string) {
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
}

func (s *MemoryConversationStore) SetDefaultAgent(chatSessionID string, agent string) {
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
}

func (s *MemoryConversationStore) SetDefaultDirectory(chatSessionID string, directory string) {
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
}

func (s *MemoryConversationStore) SetLastModel(chatSessionID string, model agent.ModelRef, mode string) {
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
	state.LastMode = strings.TrimSpace(mode)
	state.LastSeenAt = now
	s.conversations[resolvedChatSessionID] = state
}

func (s *MemoryConversationStore) Delete(chatSessionID string) {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	if resolvedChatSessionID == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conversations, resolvedChatSessionID)
}

func (s *MemoryConversationStore) Touch(chatSessionID string) {
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
}

func (s *MemoryConversationStore) List() []ConversationState {
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

func (s *MemoryConversationStore) ensureStateLocked(chatSessionID string, now time.Time) ConversationState {
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

func (s *MemoryConversationStore) cleanupExpiredLocked(now time.Time) {
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

func (s *MemoryConversationStore) limitSizeLocked(now time.Time) {
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
