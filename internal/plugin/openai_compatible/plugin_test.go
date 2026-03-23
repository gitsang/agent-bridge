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

func TestPluginReusesAnonymousSessionID(t *testing.T) {
	t.Parallel()

	plugin := New("test", slog.New(slog.NewTextHandler(io.Discard, nil)), defaultConfig())
	handler := plugin.newHTTPHandler(func(_ context.Context, req *connect.Message) (*connect.Message, error) {
		if req.SessionID == "" {
			return &connect.Message{Message: "first", SessionID: "ses_first"}, nil
		}
		return &connect.Message{Message: req.SessionID, SessionID: req.SessionID}, nil
	})

	firstBody := []byte(`{"model":"opencode-connect","messages":[{"role":"user","content":"hello"}]}`)
	firstReq := httptest.NewRequest(http.MethodPost, "/chat/completions", bytes.NewReader(firstBody))
	firstReq.Header.Set("Content-Type", "application/json")
	firstResp := httptest.NewRecorder()
	handler.ServeHTTP(firstResp, firstReq)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first response code = %d, want %d", firstResp.Code, http.StatusOK)
	}

	secondBody := []byte(`{"model":"opencode-connect","messages":[{"role":"user","content":"hello again"}]}`)
	secondReq := httptest.NewRequest(http.MethodPost, "/chat/completions", bytes.NewReader(secondBody))
	secondReq.Header.Set("Content-Type", "application/json")
	secondResp := httptest.NewRecorder()
	handler.ServeHTTP(secondResp, secondReq)
	if secondResp.Code != http.StatusOK {
		t.Fatalf("second response code = %d, want %d", secondResp.Code, http.StatusOK)
	}

	var payload map[string]any
	if err := json.Unmarshal(secondResp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	choices, ok := payload["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("choices = %#v, want non-empty array", payload["choices"])
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		t.Fatalf("choice = %#v, want object", choices[0])
	}
	message, ok := choice["message"].(map[string]any)
	if !ok {
		t.Fatalf("message = %#v, want object", choice["message"])
	}
	if got, want := message["content"], "ses_first"; got != want {
		t.Fatalf("message content = %#v, want %#v", got, want)
	}
}

func TestPluginDoesNotOverwriteExplicitUser(t *testing.T) {
	t.Parallel()

	plugin := New("test", slog.New(slog.NewTextHandler(io.Discard, nil)), defaultConfig())
	handler := plugin.newHTTPHandler(func(_ context.Context, req *connect.Message) (*connect.Message, error) {
		return &connect.Message{Message: req.SessionID, SessionID: req.SessionID}, nil
	})

	body := []byte(`{"model":"opencode-connect","user":"ses_explicit","messages":[{"role":"user","content":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("response code = %d, want %d", resp.Code, http.StatusOK)
	}
	if got := plugin.getLastSessionID(); got != "" {
		t.Fatalf("stored session = %q, want empty", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	choices := payload["choices"].([]any)
	choice := choices[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if got, want := message["content"], "ses_explicit"; got != want {
		t.Fatalf("message content = %#v, want %#v", got, want)
	}
}
