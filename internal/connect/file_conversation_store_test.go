package connect

import (
	"path/filepath"
	"testing"
)

func TestFileConversationStorePersistsConversationState(t *testing.T) {
	t.Parallel()

	storePath := filepath.Join(t.TempDir(), "conversation_store.json")
	store, err := NewFileConversationStore(storePath, 0, 0)
	if err != nil {
		t.Fatalf("NewFileConversationStore() error = %v", err)
	}

	store.PutBinding("chat-1", "ses-1")
	store.SetDefaultModel("chat-1", "openai/gpt-5.4")
	store.SetDefaultAgent("chat-1", "build")
	store.SetDefaultWorkdir("chat-1", "/repo/project")
	store.SetLastModelInfo("chat-1", "openai", "gpt-5.4", "quick")

	reloadedStore, err := NewFileConversationStore(storePath, 0, 0)
	if err != nil {
		t.Fatalf("NewFileConversationStore() reload error = %v", err)
	}

	state, ok := reloadedStore.Get("chat-1")
	if !ok {
		t.Fatalf("Get() conversation state missing")
	}
	if got, want := state.OpencodeSessionID, "ses-1"; got != want {
		t.Fatalf("opencode session id = %q, want %q", got, want)
	}
	if got, want := state.DefaultModel, "openai/gpt-5.4"; got != want {
		t.Fatalf("default model = %q, want %q", got, want)
	}
	if got, want := state.DefaultAgent, "build"; got != want {
		t.Fatalf("default agent = %q, want %q", got, want)
	}
	if got, want := state.DefaultWorkdir, "/repo/project"; got != want {
		t.Fatalf("default workdir = %q, want %q", got, want)
	}
	if got, want := state.LastProviderID, "openai"; got != want {
		t.Fatalf("last provider id = %q, want %q", got, want)
	}
	if got, want := state.LastModelID, "gpt-5.4"; got != want {
		t.Fatalf("last model id = %q, want %q", got, want)
	}
	if got, want := state.LastMode, "quick"; got != want {
		t.Fatalf("last mode = %q, want %q", got, want)
	}
}

func TestFileConversationStoreDeletePersists(t *testing.T) {
	t.Parallel()

	storePath := filepath.Join(t.TempDir(), "conversation_store.json")
	store, err := NewFileConversationStore(storePath, 0, 0)
	if err != nil {
		t.Fatalf("NewFileConversationStore() error = %v", err)
	}

	store.PutBinding("chat-1", "ses-1")
	store.Delete("chat-1")

	reloadedStore, err := NewFileConversationStore(storePath, 0, 0)
	if err != nil {
		t.Fatalf("NewFileConversationStore() reload error = %v", err)
	}

	if _, ok := reloadedStore.Get("chat-1"); ok {
		t.Fatalf("Get() should not find deleted conversation")
	}
}

func TestFileConversationStoreRequiresPath(t *testing.T) {
	t.Parallel()

	_, err := NewFileConversationStore("", 0, 0)
	if err == nil {
		t.Fatalf("NewFileConversationStore() error = nil, want error")
	}
	if got, want := err.Error(), "conversation store file path is required"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}
