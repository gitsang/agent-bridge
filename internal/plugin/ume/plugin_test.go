package ume

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitsang/opencode-connect/internal/connect"
)

func TestPluginStripsMentionAndReusesSession(t *testing.T) {
	t.Parallel()

	sendRecorder := newSendRecorderServer()
	defer sendRecorder.Close()

	plugin := New("test", slog.New(slog.NewTextHandler(io.Discard, nil)), Config{SendURL: sendRecorder.URL})

	var handleCalls atomic.Int32
	handler := plugin.newHTTPHandler(func(_ context.Context, req *connect.Message) (*connect.Message, error) {
		handleCount := handleCalls.Add(1)
		switch handleCount {
		case 1:
			if got, want := req.Message, "hi"; got != want {
				t.Fatalf("first request message = %q, want %q", got, want)
			}
			if got, want := req.SessionID, ""; got != want {
				t.Fatalf("first request session = %q, want %q", got, want)
			}
			return &connect.Message{Message: "first-reply", SessionID: "ses_created"}, nil
		case 2:
			if got, want := req.Message, "follow up"; got != want {
				t.Fatalf("second request message = %q, want %q", got, want)
			}
			if got, want := req.SessionID, "ses_created"; got != want {
				t.Fatalf("second request session = %q, want %q", got, want)
			}
			return &connect.Message{Message: req.SessionID, SessionID: req.SessionID}, nil
		default:
			t.Fatalf("unexpected handle call count: %d", handleCount)
			return nil, nil
		}
	})

	firstReq := httptest.NewRequest(http.MethodPost, "/?access_token=test-token", bytes.NewReader([]byte(`[{
		"body":"<at id=\"6943cf64f5e6479b808ce93de9c9b47c\">Opencode</at> hi",
		"msgId":742841436585590784,
		"msgType":"text",
		"sessionId":742105222021128192
	}]`)))
	firstReq.Header.Set("Content-Type", "application/json")
	firstResp := httptest.NewRecorder()
	handler.ServeHTTP(firstResp, firstReq)
	if got, want := firstResp.Code, http.StatusOK; got != want {
		t.Fatalf("first response code = %d, want %d", got, want)
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/?access_token=test-token", bytes.NewReader([]byte(`[{
		"body":"follow up",
		"msgId":742841436585590785,
		"msgType":"text",
		"sessionId":742105222021128192
	}]`)))
	secondReq.Header.Set("Content-Type", "application/json")
	secondResp := httptest.NewRecorder()
	handler.ServeHTTP(secondResp, secondReq)
	if got, want := secondResp.Code, http.StatusOK; got != want {
		t.Fatalf("second response code = %d, want %d", got, want)
	}

	waitForCondition(t, func() bool {
		return handleCalls.Load() == 2
	}, "handle two webhook messages")

	if got, want := int(handleCalls.Load()), 2; got != want {
		t.Fatalf("handle calls = %d, want %d", got, want)
	}

	waitForCondition(t, func() bool {
		return len(sendRecorder.Requests()) == 2
	}, "record two send requests")

	requests := sendRecorder.Requests()
	if got, want := len(requests), 2; got != want {
		t.Fatalf("send requests = %d, want %d", got, want)
	}
	if got, want := requests[0].Token, "test-token"; got != want {
		t.Fatalf("first send token = %q, want %q", got, want)
	}
	if got, want := requests[0].Payload.Body, "first-reply"; got != want {
		t.Fatalf("first send body = %q, want %q", got, want)
	}
	if got, want := requests[1].Payload.Body, "ses_created"; got != want {
		t.Fatalf("second send body = %q, want %q", got, want)
	}
}

func TestPluginIgnoresDuplicateMessageID(t *testing.T) {
	t.Parallel()

	sendRecorder := newSendRecorderServer()
	defer sendRecorder.Close()

	plugin := New("test", slog.New(slog.NewTextHandler(io.Discard, nil)), Config{SendURL: sendRecorder.URL})

	var handleCalls atomic.Int32
	handler := plugin.newHTTPHandler(func(_ context.Context, req *connect.Message) (*connect.Message, error) {
		handleCalls.Add(1)
		return &connect.Message{Message: "reply", SessionID: "ses_created"}, nil
	})

	body := []byte(`[{
		"body":"hello",
		"msgId":742841436585590784,
		"msgType":"text",
		"sessionId":742105222021128192
	}]`)

	firstReq := httptest.NewRequest(http.MethodPost, "/?access_token=test-token", bytes.NewReader(body))
	firstReq.Header.Set("Content-Type", "application/json")
	firstResp := httptest.NewRecorder()
	handler.ServeHTTP(firstResp, firstReq)
	if got, want := firstResp.Code, http.StatusOK; got != want {
		t.Fatalf("first response code = %d, want %d", got, want)
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/?access_token=test-token", bytes.NewReader(body))
	secondReq.Header.Set("Content-Type", "application/json")
	secondResp := httptest.NewRecorder()
	handler.ServeHTTP(secondResp, secondReq)
	if got, want := secondResp.Code, http.StatusOK; got != want {
		t.Fatalf("second response code = %d, want %d", got, want)
	}

	waitForCondition(t, func() bool {
		return handleCalls.Load() == 1
	}, "handle initial webhook message once")

	if got, want := int(handleCalls.Load()), 1; got != want {
		t.Fatalf("handle calls = %d, want %d", got, want)
	}

	waitForCondition(t, func() bool {
		return len(sendRecorder.Requests()) == 1
	}, "record one send request")

	requests := sendRecorder.Requests()
	if got, want := len(requests), 1; got != want {
		t.Fatalf("send requests = %d, want %d", got, want)
	}

	var payload map[string]any
	if err := json.Unmarshal(secondResp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got, want := int(payload["ignored"].(float64)), 1; got != want {
		t.Fatalf("ignored = %d, want %d", got, want)
	}
}

func TestPluginIgnoresRetriedOlderMessageID(t *testing.T) {
	t.Parallel()

	sendRecorder := newSendRecorderServer()
	defer sendRecorder.Close()

	plugin := New("test", slog.New(slog.NewTextHandler(io.Discard, nil)), Config{SendURL: sendRecorder.URL})

	var handleCalls atomic.Int32
	handler := plugin.newHTTPHandler(func(_ context.Context, req *connect.Message) (*connect.Message, error) {
		handleCalls.Add(1)
		return &connect.Message{Message: req.Message, SessionID: "ses_created"}, nil
	})

	requests := [][]byte{
		[]byte(`[{"body":"first","msgId":1001,"msgType":"text","sessionId":2001}]`),
		[]byte(`[{"body":"second","msgId":1002,"msgType":"text","sessionId":2001}]`),
		[]byte(`[{"body":"first retry","msgId":1001,"msgType":"text","sessionId":2001}]`),
	}

	for index, body := range requests {
		req := httptest.NewRequest(http.MethodPost, "/?access_token=test-token", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if got, want := resp.Code, http.StatusOK; got != want {
			t.Fatalf("response %d code = %d, want %d", index, got, want)
		}
	}

	waitForCondition(t, func() bool {
		return handleCalls.Load() == 2
	}, "handle two unique webhook messages")

	if got, want := int(handleCalls.Load()), 2; got != want {
		t.Fatalf("handle calls = %d, want %d", got, want)
	}

	waitForCondition(t, func() bool {
		return len(sendRecorder.Requests()) == 2
	}, "record two send requests for unique messages")

	sent := sendRecorder.Requests()
	if got, want := len(sent), 2; got != want {
		t.Fatalf("send requests = %d, want %d", got, want)
	}
	if got, want := sent[0].Payload.Body, "first"; got != want {
		t.Fatalf("first send body = %q, want %q", got, want)
	}
	if got, want := sent[1].Payload.Body, "second"; got != want {
		t.Fatalf("second send body = %q, want %q", got, want)
	}

	plugin.mu.RLock()
	state := plugin.sessionState["2001"]
	plugin.mu.RUnlock()
	if state == nil {
		t.Fatal("session state = nil, want existing state")
	}
	if got, want := len(state.recentMessageIDs), 2; got != want {
		t.Fatalf("tracked recent message ids = %d, want %d", got, want)
	}
}

func TestSanitizeMessageRemovesAtTag(t *testing.T) {
	t.Parallel()

	input := `<at id="6943cf64f5e6479b808ce93de9c9b47c">Opencode</at> hi`
	if got, want := sanitizeMessage(input), "hi"; got != want {
		t.Fatalf("sanitizeMessage() = %q, want %q", got, want)
	}
}

func waitForCondition(t *testing.T, condition func() bool, description string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting to %s", description)
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
		defer r.Body.Close()

		var payload UmeSendRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		recorder.requests = append(recorder.requests, sendRecorderRequest{
			Token:   r.URL.Query().Get("access_token"),
			Payload: payload,
		})

		w.WriteHeader(http.StatusOK)
	}))
	return recorder
}

func (s *sendRecorderServer) Requests() []sendRecorderRequest {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]sendRecorderRequest, len(s.requests))
	copy(out, s.requests)
	return out
}
