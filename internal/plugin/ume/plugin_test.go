package ume

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitsang/opencode-connect/internal/connect"
)

func TestPluginStripsMentionAndPassesChatSession(t *testing.T) {
	t.Parallel()

	sendRecorder := newSendRecorderServer()
	defer sendRecorder.Close()

	plugin := New("test", slog.New(slog.NewTextHandler(io.Discard, nil)), Config{SendURL: sendRecorder.URL})

	handler := plugin.newHTTPHandler(func(_ context.Context, req *connect.Message, reply connect.ReplyFunc) error {
		if got, want := req.Chat.SessionID, "742105222021128192"; got != want {
			t.Fatalf("chat session = %q, want %q", got, want)
		}
		if got, want := req.Content, "hi"; got != want {
			t.Fatalf("content = %q, want %q", got, want)
		}
		return reply(&connect.Message{
			Content: "first-reply",
			Opencode: connect.OpencodeContext{
				SessionID: "ses_created",
				Title:     "First Title",
				Workdir:   "/workspace/one",
				Model:     "openai/gpt-5.4",
			},
		})
	})

	req := httptest.NewRequest(http.MethodPost, "/?access_token=test-token", bytes.NewReader([]byte(`[{"body":"<at id=\"x\">Opencode</at> hi","msgId":742841436585590784,"msgType":"text","sessionId":742105222021128192}]`)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if got, want := resp.Code, http.StatusOK; got != want {
		t.Fatalf("response code = %d, want %d", got, want)
	}

	waitForCondition(t, func() bool {
		return len(sendRecorder.Requests()) == 1
	}, "record one send request")

	requests := sendRecorder.Requests()
	if got, want := requests[0].Token, "test-token"; got != want {
		t.Fatalf("send token = %q, want %q", got, want)
	}
	wantBody := "First Title\n\nfirst-reply\n\n---\n\nDirectory: /workspace/one\nSession: ses_created\nModel: openai/gpt-5.4"
	if got := requests[0].Payload.Body; got != wantBody {
		t.Fatalf("reply body = %q, want %q", got, wantBody)
	}
}

func TestPluginDeduplicatesMessageID(t *testing.T) {
	t.Parallel()

	sendRecorder := newSendRecorderServer()
	defer sendRecorder.Close()

	plugin := New("test", slog.New(slog.NewTextHandler(io.Discard, nil)), Config{SendURL: sendRecorder.URL})
	var handleCalls atomic.Int32

	handler := plugin.newHTTPHandler(func(_ context.Context, req *connect.Message, reply connect.ReplyFunc) error {
		handleCalls.Add(1)
		return reply(&connect.Message{Content: req.Content})
	})

	body := []byte(`[{"body":"hello","msgId":2001,"msgType":"text","sessionId":1001}]`)
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/?access_token=test-token", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if got, want := resp.Code, http.StatusOK; got != want {
			t.Fatalf("response code = %d, want %d", got, want)
		}
	}

	waitForCondition(t, func() bool {
		return handleCalls.Load() == 1
	}, "deduplicate repeated message id")
}

func TestPluginSendsErrorReplyWhenHandlerFails(t *testing.T) {
	t.Parallel()

	sendRecorder := newSendRecorderServer()
	defer sendRecorder.Close()

	plugin := New("test", slog.New(slog.NewTextHandler(io.Discard, nil)), Config{SendURL: sendRecorder.URL})

	handler := plugin.newHTTPHandler(func(_ context.Context, _ *connect.Message, _ connect.ReplyFunc) error {
		return fmt.Errorf("context deadline exceeded")
	})

	req := httptest.NewRequest(http.MethodPost, "/?access_token=test-token", bytes.NewReader([]byte(`[{"body":"hello","msgId":3001,"msgType":"text","sessionId":1002}]`)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if got, want := resp.Code, http.StatusOK; got != want {
		t.Fatalf("response code = %d, want %d", got, want)
	}

	waitForCondition(t, func() bool {
		return len(sendRecorder.Requests()) == 1
	}, "record one error send request")

	requests := sendRecorder.Requests()
	if got, want := requests[0].Token, "test-token"; got != want {
		t.Fatalf("send token = %q, want %q", got, want)
	}
	if !strings.Contains(requests[0].Payload.Body, "Error: context deadline exceeded") {
		t.Fatalf("error reply body = %q, want contains %q", requests[0].Payload.Body, "Error: context deadline exceeded")
	}
}

func waitForCondition(t *testing.T, check func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", description)
}

type sendRecorderRequest struct {
	Token   string
	Payload UmeSendRequest
}

type sendRecorderServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []sendRecorderRequest
}

func newSendRecorderServer() *sendRecorderServer {
	recorder := &sendRecorderServer{}
	recorder.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var payload UmeSendRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, sendRecorderRequest{Token: r.URL.Query().Get("access_token"), Payload: payload})
		recorder.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	return recorder
}

func (r *sendRecorderServer) Requests() []sendRecorderRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]sendRecorderRequest, len(r.requests))
	copy(items, r.requests)
	return items
}
