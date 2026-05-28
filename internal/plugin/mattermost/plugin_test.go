package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/bridge"
	coreplugin "github.com/gitsang/agent-bridge/internal/plugin"
)

func TestHTTPHandlerHandlesSlashCommandSynchronously(t *testing.T) {
	plugin := New("mattermost-test", testLogger(), Config{Token: "secret"})
	handler := plugin.newHTTPHandler(func(_ context.Context, req *bridge.Message, reply bridge.ReplyFunc) error {
		if got, want := req.Content, "hello world"; got != want {
			t.Fatalf("request content = %q, want %q", got, want)
		}
		if got, want := req.Chat.SessionID, "team-1:channel-1:user-1"; got != want {
			t.Fatalf("request session = %q, want %q", got, want)
		}
		return reply(&bridge.Message{Content: "agent reply"})
	})

	form := url.Values{}
	form.Set("token", "secret")
	form.Set("text", "hello world")
	form.Set("team_id", "team-1")
	form.Set("channel_id", "channel-1")
	form.Set("user_id", "user-1")
	req := httptest.NewRequest(http.MethodPost, "/mattermost", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	var resp MattermostResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, want := resp.ResponseType, "ephemeral"; got != want {
		t.Fatalf("response_type = %q, want %q", got, want)
	}
	if got, want := resp.Text, "agent reply"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestHTTPHandlerAcceptsSlashCommandAuthorizationHeader(t *testing.T) {
	plugin := New("mattermost-test", testLogger(), Config{Token: "secret"})
	handler := plugin.newHTTPHandler(func(_ context.Context, req *bridge.Message, reply bridge.ReplyFunc) error {
		if got, want := req.Content, "hello from header"; got != want {
			t.Fatalf("request content = %q, want %q", got, want)
		}
		return reply(&bridge.Message{Content: "agent reply"})
	})

	form := url.Values{}
	form.Set("text", "hello from header")
	form.Set("channel_id", "channel-1")
	form.Set("user_id", "user-1")
	req := httptest.NewRequest(http.MethodPost, "/mattermost", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Token secret")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestHTTPHandlerAcceptsSlashCommandGET(t *testing.T) {
	plugin := New("mattermost-test", testLogger(), Config{Token: "secret"})
	handler := plugin.newHTTPHandler(func(_ context.Context, req *bridge.Message, reply bridge.ReplyFunc) error {
		if got, want := req.Content, "hello from get"; got != want {
			t.Fatalf("request content = %q, want %q", got, want)
		}
		return reply(&bridge.Message{Content: "agent reply"})
	})

	values := url.Values{}
	values.Set("text", "hello from get")
	values.Set("channel_id", "channel-1")
	values.Set("user_id", "user-1")
	req := httptest.NewRequest(http.MethodGet, "/mattermost?"+values.Encode(), nil)
	req.Header.Set("Authorization", "Token secret")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestHTTPHandlerRejectsSlashCommandGETQueryToken(t *testing.T) {
	plugin := New("mattermost-test", testLogger(), Config{Token: "secret"})
	handler := plugin.newHTTPHandler(func(context.Context, *bridge.Message, bridge.ReplyFunc) error {
		t.Fatalf("handle should not be called")
		return nil
	})

	values := url.Values{}
	values.Set("token", "secret")
	values.Set("text", "hello from get")
	req := httptest.NewRequest(http.MethodGet, "/mattermost?"+values.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusUnauthorized; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestSameToken(t *testing.T) {
	tests := []struct {
		name       string
		request    string
		configured string
		want       bool
	}{
		{name: "match", request: "secret", configured: "secret", want: true},
		{name: "mismatch", request: "wrong", configured: "secret"},
		{name: "prefix", request: "sec", configured: "secret"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameToken(tt.request, tt.configured); got != tt.want {
				t.Fatalf("sameToken() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestHTTPHandlerRejectsInvalidToken(t *testing.T) {
	plugin := New("mattermost-test", testLogger(), Config{Token: "secret"})
	handler := plugin.newHTTPHandler(func(context.Context, *bridge.Message, bridge.ReplyFunc) error {
		t.Fatalf("handle should not be called")
		return nil
	})

	form := url.Values{}
	form.Set("token", "wrong")
	form.Set("text", "hello")
	req := httptest.NewRequest(http.MethodPost, "/mattermost", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusUnauthorized; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestHTTPHandlerRejectsWhenTokenIsNotConfigured(t *testing.T) {
	plugin := New("mattermost-test", testLogger(), Config{})
	handler := plugin.newHTTPHandler(func(context.Context, *bridge.Message, bridge.ReplyFunc) error {
		t.Fatalf("handle should not be called")
		return nil
	})

	form := url.Values{}
	form.Set("token", "secret")
	form.Set("text", "hello")
	req := httptest.NewRequest(http.MethodPost, "/mattermost", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusUnauthorized; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestHTTPHandlerRejectsOversizedBody(t *testing.T) {
	plugin := New("mattermost-test", testLogger(), Config{Token: "secret"})
	handler := plugin.newHTTPHandler(func(context.Context, *bridge.Message, bridge.ReplyFunc) error {
		t.Fatalf("handle should not be called")
		return nil
	})

	form := url.Values{}
	form.Set("token", "secret")
	form.Set("text", strings.Repeat("x", maxRequestBodyBytes+1))
	req := httptest.NewRequest(http.MethodPost, "/mattermost", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusBadRequest; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestHTTPHandlerHidesInternalSyncErrors(t *testing.T) {
	plugin := New("mattermost-test", testLogger(), Config{Token: "secret"})
	handler := plugin.newHTTPHandler(func(context.Context, *bridge.Message, bridge.ReplyFunc) error {
		return errors.New("database password is secret")
	})

	form := url.Values{}
	form.Set("token", "secret")
	form.Set("text", "hello")
	req := httptest.NewRequest(http.MethodPost, "/mattermost", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	var resp MattermostResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, want := resp.Text, "Error: internal error"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestHTTPHandlerShowsBadRequestBridgeErrors(t *testing.T) {
	plugin := New("mattermost-test", testLogger(), Config{Token: "secret"})
	handler := plugin.newHTTPHandler(func(context.Context, *bridge.Message, bridge.ReplyFunc) error {
		return bridge.NewError(http.StatusBadRequest, "unknown slash command")
	})

	form := url.Values{}
	form.Set("token", "secret")
	form.Set("text", "/unknown")
	req := httptest.NewRequest(http.MethodPost, "/mattermost", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusBadRequest; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	var resp MattermostResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, want := resp.Text, "Error: unknown slash command"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestHTTPHandlerIgnoresDuplicatePostID(t *testing.T) {
	plugin := New("mattermost-test", testLogger(), Config{Token: "secret"})
	callCount := 0
	handler := plugin.newHTTPHandler(func(_ context.Context, _ *bridge.Message, reply bridge.ReplyFunc) error {
		callCount++
		return reply(&bridge.Message{Content: "agent reply"})
	})

	form := url.Values{}
	form.Set("token", "secret")
	form.Set("text", "hello")
	form.Set("team_id", "team-1")
	form.Set("channel_id", "channel-1")
	form.Set("user_id", "user-1")
	form.Set("post_id", "post-1")

	for index := 0; index < 2; index++ {
		req := httptest.NewRequest(http.MethodPost, "/mattermost", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if got, want := rec.Code, http.StatusOK; got != want {
			t.Fatalf("status %d = %d, want %d", index, got, want)
		}
	}
	if got, want := callCount, 1; got != want {
		t.Fatalf("handle call count = %d, want %d", got, want)
	}
}

func TestHTTPHandlerSendsRepliesToResponseURL(t *testing.T) {
	callbackCh := make(chan MattermostResponse, 2)
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read callback body: %v", err)
		}
		var payload MattermostResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode callback payload: %v", err)
		}
		callbackCh <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer callback.Close()

	plugin := New("mattermost-test", testLogger(), Config{Token: "secret", ResponseURLHosts: []string{mustURLHost(t, callback.URL)}})
	plugin.httpClient = callback.Client()
	handler := plugin.newHTTPHandler(func(_ context.Context, _ *bridge.Message, reply bridge.ReplyFunc) error {
		if err := reply(&bridge.Message{Content: "first"}); err != nil {
			return err
		}
		return reply(&bridge.Message{Content: "second"})
	})

	form := url.Values{}
	form.Set("token", "secret")
	form.Set("text", "hello")
	form.Set("channel_id", "channel-1")
	form.Set("user_id", "user-1")
	form.Set("response_url", callback.URL)
	req := httptest.NewRequest(http.MethodPost, "/mattermost", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	var immediate MattermostResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &immediate); err != nil {
		t.Fatalf("decode immediate response: %v", err)
	}
	if got, want := immediate.Text, "request accepted"; got != want {
		t.Fatalf("immediate text = %q, want %q", got, want)
	}
	callbackBodies := collectCallbackBodies(t, callbackCh, 2)
	if got, want := callbackBodies[0].Text, "first"; got != want {
		t.Fatalf("first callback text = %q, want %q", got, want)
	}
	if got, want := callbackBodies[1].Text, "second"; got != want {
		t.Fatalf("second callback text = %q, want %q", got, want)
	}
}

func TestHTTPHandlerRejectsUnallowedResponseURL(t *testing.T) {
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatalf("callback should not be called")
		w.WriteHeader(http.StatusOK)
	}))
	defer callback.Close()

	plugin := New("mattermost-test", testLogger(), Config{Token: "secret"})
	handler := plugin.newHTTPHandler(func(context.Context, *bridge.Message, bridge.ReplyFunc) error {
		t.Fatalf("handle should not be called")
		return nil
	})

	form := url.Values{}
	form.Set("token", "secret")
	form.Set("text", "hello")
	form.Set("response_url", callback.URL)
	req := httptest.NewRequest(http.MethodPost, "/mattermost", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusBadRequest; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func mustURLHost(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsed.Host
}

func collectCallbackBodies(t *testing.T, callbackCh <-chan MattermostResponse, count int) []MattermostResponse {
	t.Helper()
	bodies := make([]MattermostResponse, 0, count)
	for len(bodies) < count {
		select {
		case body := <-callbackCh:
			bodies = append(bodies, body)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for callback %d", len(bodies)+1)
		}
	}
	return bodies
}

func TestBuildResponseFormatsReply(t *testing.T) {
	msg := &bridge.Message{
		Content: "hello",
		Agent: bridge.AgentContext{
			SessionID: "session-1",
			Title:     "Session Title",
			Model:     "gpt-test",
			Directory: "/tmp/project",
		},
	}

	resp, err := buildResponse(msg)
	if err != nil {
		t.Fatalf("buildResponse() error = %v", err)
	}
	if got, want := resp.ResponseType, "ephemeral"; got != want {
		t.Fatalf("ResponseType = %q, want %q", got, want)
	}
	for _, want := range []string{"hello", "Directory: /tmp/project", "Session: Session Title (session-1)", "Model: gpt-test"} {
		if !strings.Contains(resp.Text, want) {
			t.Fatalf("Text = %q, want %q", resp.Text, want)
		}
	}
}

func TestDecodeRequestAcceptsJSON(t *testing.T) {
	body := bytes.NewBufferString(`{"token":"secret","text":"hello","channel_id":"channel-1","user_id":"user-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/mattermost", body)
	req.Header.Set("Content-Type", "application/json")

	request, err := decodeRequest(req)
	if err != nil {
		t.Fatalf("decodeRequest() error = %v", err)
	}
	if got, want := request.Text, "hello"; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
	if got, want := request.SessionID(), "channel-1:user-1"; got != want {
		t.Fatalf("SessionID() = %q, want %q", got, want)
	}
}

func TestFactoryRegistersMattermost(t *testing.T) {
	factory, ok := coreplugin.GetPluginFactory("mattermost-webhook")
	if !ok {
		t.Fatalf("mattermost-webhook factory is not registered")
	}
	plugin, err := factory.Construct("mattermost", map[string]any{"token": "secret"}, coreplugin.Infrastructure{Logger: testLogger()})
	if err != nil {
		t.Fatalf("Construct() error = %v", err)
	}
	if got, want := plugin.Name(), "mattermost"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}

func TestFactoryRequiresToken(t *testing.T) {
	factory, ok := coreplugin.GetPluginFactory("mattermost-webhook")
	if !ok {
		t.Fatalf("mattermost-webhook factory is not registered")
	}
	_, err := factory.Construct("mattermost", map[string]any{}, coreplugin.Infrastructure{Logger: testLogger()})
	if err == nil {
		t.Fatalf("Construct() error is nil")
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
