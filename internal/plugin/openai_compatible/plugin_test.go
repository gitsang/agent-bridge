package openai_compatible

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gitsang/opencode-connect/internal/connect"
)

func TestPluginUsesUserAsChatSessionID(t *testing.T) {
	t.Parallel()

	plugin := New("test", slog.New(slog.NewTextHandler(io.Discard, nil)), defaultConfig())
	handler := plugin.newHTTPHandler(func(_ context.Context, req *connect.Message) (*connect.Message, error) {
		if got, want := req.Chat.SessionID, "chat-user"; got != want {
			t.Fatalf("chat session = %q, want %q", got, want)
		}
		return &connect.Message{Content: "ok"}, nil
	})

	body := []byte(`{"model":"opencode-connect","user":"chat-user","messages":[{"role":"user","content":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("response code = %d, want %d", resp.Code, http.StatusOK)
	}
}

func TestPluginDoesNotPersistAnonymousSession(t *testing.T) {
	t.Parallel()

	var seen []string
	plugin := New("test", slog.New(slog.NewTextHandler(io.Discard, nil)), defaultConfig())
	handler := plugin.newHTTPHandler(func(_ context.Context, req *connect.Message) (*connect.Message, error) {
		seen = append(seen, req.Chat.SessionID)
		return &connect.Message{Content: "ok"}, nil
	})

	body := []byte(`{"model":"opencode-connect","messages":[{"role":"user","content":"hello"}]}`)
	firstReq := httptest.NewRequest(http.MethodPost, "/chat/completions", bytes.NewReader(body))
	firstReq.Header.Set("Content-Type", "application/json")
	firstResp := httptest.NewRecorder()
	handler.ServeHTTP(firstResp, firstReq)

	secondReq := httptest.NewRequest(http.MethodPost, "/chat/completions", bytes.NewReader(body))
	secondReq.Header.Set("Content-Type", "application/json")
	secondResp := httptest.NewRecorder()
	handler.ServeHTTP(secondResp, secondReq)

	if got, want := len(seen), 2; got != want {
		t.Fatalf("handler call count = %d, want %d", got, want)
	}
	if seen[0] != "" || seen[1] != "" {
		t.Fatalf("anonymous sessions should remain empty, got %#v", seen)
	}

	var payload map[string]any
	if err := json.Unmarshal(secondResp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	choices := payload["choices"].([]any)
	choice := choices[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if got, want := message["content"], "ok"; got != want {
		t.Fatalf("message content = %#v, want %#v", got, want)
	}
}
