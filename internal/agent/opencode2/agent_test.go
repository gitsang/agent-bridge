package opencode2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/types"
)

// fakeServer is a scripted OpenCode 2 HttpApi.
type fakeServer struct {
	mu sync.Mutex

	sessions   []sessionInfo
	messages   map[string][]messageInfo
	permission map[string][]permissionRequest
	forms      map[string][]formInfo

	// prompted flips busy on so a turn is in flight; turns controls how many
	// polls stay busy before the session becomes idle again.
	turns     int
	pending   map[string]int
	prompted  int
	authUser  string
	authPass  string
	interrupt int
	replies   []map[string]string
	formReply []map[string]any
	lastQuery map[string]string
}

func newFakeServer() *fakeServer {
	return &fakeServer{
		messages:   map[string][]messageInfo{},
		permission: map[string][]permissionRequest{},
		forms:      map[string][]formInfo{},
		pending:    map[string]int{},
		lastQuery:  map[string]string{},
	}
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/model", func(w http.ResponseWriter, r *http.Request) {
		f.recordQuery(r)
		writeEnvelope(w, []modelInfo{
			{ID: "m1", ModelID: "m1", ProviderID: "prov", Name: "Model One"},
			{ID: "m2", ModelID: "m2", ProviderID: "prov", Name: "Model Two"},
		})
	})
	mux.HandleFunc("/api/agent", func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, []agentInfo{{ID: "build", Name: "Build", Description: "default", Mode: "primary"}})
	})
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{"info": map[string]any{"providers": map[string]any{
			"prov": map[string]any{"name": "Provider One"},
		}}}})
	})
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		f.recordQuery(r)
		if r.Method == http.MethodPost {
			var body createSessionBody
			_ = json.NewDecoder(r.Body).Decode(&body)
			session := sessionInfo{ID: "ses_new", Time: sessionTime{Created: 1000, Updated: 1000}}
			if body.Title != nil {
				session.Title = *body.Title
			}
			if body.Location != nil {
				session.Location = body.Location
			}
			f.mu.Lock()
			f.sessions = append(f.sessions, session)
			f.mu.Unlock()
			writeEnvelope(w, session)
			return
		}
		f.mu.Lock()
		sessions := append([]sessionInfo(nil), f.sessions...)
		f.mu.Unlock()
		writeEnvelope(w, sessions)
	})
	mux.HandleFunc("/api/session/", func(w http.ResponseWriter, r *http.Request) {
		f.recordQuery(r)
		path := strings.TrimPrefix(r.URL.Path, "/api/session/")
		parts := strings.Split(path, "/")
		sessionID := parts[0]
		f.mu.Lock()
		session := f.findSessionLocked(sessionID)
		f.mu.Unlock()
		if session == nil {
			writeError(w, http.StatusNotFound, "SessionNotFound")
			return
		}

		rest := strings.Join(parts[1:], "/")
		switch {
		case rest == "" && r.Method == http.MethodGet:
			f.mu.Lock()
			busy := f.pending[sessionID]
			if busy > 0 {
				f.pending[sessionID] = busy - 1
				session.Time.Busy = 2000
				session.Time.Idle = 1000
			} else {
				session.Time.Busy = 1000
				session.Time.Idle = 2000
				if f.prompted > 0 {
					f.messages[sessionID] = append(f.messages[sessionID], messageInfo{
						ID:    "msg_assistant",
						Type:  "assistant",
						Time:  messageTime{Created: 3000, Completed: 3000},
						Model: &modelRef{ID: "m1", ProviderID: "prov"},
						Content: []messagePart{{
							Type: "text",
							Text: "pong",
						}},
					})
					f.prompted = 0
				}
			}
			snapshot := *session
			f.mu.Unlock()
			writeEnvelope(w, snapshot)
		case rest == "message" && r.Method == http.MethodGet:
			f.mu.Lock()
			messages := append([]messageInfo(nil), f.messages[sessionID]...)
			f.mu.Unlock()
			writeEnvelope(w, messages)
		case rest == "prompt" && r.Method == http.MethodPost:
			var body promptBody
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.pending[sessionID] = f.turns
			f.prompted++
			f.messages[sessionID] = append(f.messages[sessionID], messageInfo{
				ID:   "msg_user",
				Type: "user",
				Time: messageTime{Created: 2000},
				Text: body.Text,
			})
			f.mu.Unlock()
			writeEnvelope(w, map[string]any{"id": "msg_user"})
		case rest == "interrupt" && r.Method == http.MethodPost:
			f.mu.Lock()
			f.interrupt++
			f.pending[sessionID] = 0
			session.Time.Busy = 1000
			session.Time.Idle = 2000
			f.mu.Unlock()
			writeJSON(w, map[string]bool{"interrupted": true})
		case rest == "permission" && r.Method == http.MethodGet:
			f.mu.Lock()
			requests := append([]permissionRequest(nil), f.permission[sessionID]...)
			f.mu.Unlock()
			writeEnvelope(w, requests)
		case strings.HasPrefix(rest, "permission/") && strings.HasSuffix(rest, "/reply") && r.Method == http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			found := false
			for _, request := range f.permission[sessionID] {
				if request.ID == strings.TrimSuffix(strings.TrimPrefix(rest, "permission/"), "/reply") {
					found = true
				}
			}
			if found {
				f.replies = append(f.replies, body)
				f.permission[sessionID] = nil
			} else {
				// A request that already settled is reported as missing.
				f.mu.Unlock()
				writeError(w, http.StatusNotFound, "PermissionNotFound")
				return
			}
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case rest == "form" && r.Method == http.MethodGet:
			f.mu.Lock()
			forms := append([]formInfo(nil), f.forms[sessionID]...)
			f.mu.Unlock()
			writeEnvelope(w, forms)
		case strings.HasPrefix(rest, "form/") && strings.HasSuffix(rest, "/reply") && r.Method == http.MethodPost:
			var body formReply
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.formReply = append(f.formReply, body.Answer)
			f.forms[sessionID] = nil
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(rest, "form/") && r.Method == http.MethodGet:
			f.mu.Lock()
			forms := f.forms[sessionID]
			f.mu.Unlock()
			if len(forms) == 0 {
				writeError(w, http.StatusNotFound, "FormNotFound")
				return
			}
			writeEnvelope(w, forms[0])
		case strings.HasPrefix(rest, "form/") && r.Method == http.MethodDelete:
			f.mu.Lock()
			f.forms[sessionID] = nil
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case rest == "model" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		case rest == "agent" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		default:
			writeError(w, http.StatusNotFound, "RouteNotFound")
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.authPass != "" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != f.authUser || pass != f.authPass {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (f *fakeServer) recordQuery(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key := range r.URL.Query() {
		f.lastQuery[key] = r.URL.Query().Get(key)
	}
}

func (f *fakeServer) findSessionLocked(sessionID string) *sessionInfo {
	for index := range f.sessions {
		if f.sessions[index].ID == sessionID {
			return &f.sessions[index]
		}
	}
	return nil
}

func writeEnvelope(w http.ResponseWriter, data any) {
	writeJSON(w, map[string]any{"data": data})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, kind string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"_tag": kind, "message": kind})
}

func startFake(t *testing.T, server *fakeServer) *Agent {
	t.Helper()
	httpServer := httptest.NewServer(server.handler())
	t.Cleanup(httpServer.Close)
	return NewClient(httpServer.URL,
		WithTimeout(10*time.Second),
		WithPollInterval(2*time.Millisecond),
	)
}

func TestListModelsUsesProviderDisplayName(t *testing.T) {
	client := startFake(t, newFakeServer())

	models, err := client.ListModels(context.Background(), "/tmp/project")
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("ListModels() returned %d models, want 2", len(models))
	}
	if models[0].String() != "prov/m1" {
		t.Fatalf("models[0] = %s, want prov/m1", models[0])
	}
	if models[0].ProviderName != "Provider One" {
		t.Fatalf("ProviderName = %q, want Provider One", models[0].ProviderName)
	}
	if models[0].ModelName != "Model One" {
		t.Fatalf("ModelName = %q, want Model One", models[0].ModelName)
	}
}

func TestCreateSessionSendsLocationAndTitle(t *testing.T) {
	server := newFakeServer()
	client := startFake(t, server)

	session, err := client.CreateSession(context.Background(), types.CreateSessionRequest{
		Directory: "/tmp/project",
		Title:     "bridge",
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if session.ID != "ses_new" {
		t.Fatalf("session ID = %q, want ses_new", session.ID)
	}
	if session.Directory != "/tmp/project" {
		t.Fatalf("session Directory = %q, want /tmp/project", session.Directory)
	}
	if session.Title != "bridge" {
		t.Fatalf("session Title = %q, want bridge", session.Title)
	}
}

func TestPromptWaitsForSettledTurnAndReportsAnswer(t *testing.T) {
	server := newFakeServer()
	server.turns = 2
	client := startFake(t, server)

	session, err := client.CreateSession(context.Background(), types.CreateSessionRequest{Directory: "/tmp/project"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	handle, err := client.Prompt(context.Background(), session.ID, "hi")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	select {
	case <-handle.Done():
	case err := <-handle.Err():
		t.Fatalf("Prompt() failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt() did not settle")
	}

	messages, err := client.PollMessagesAfter(context.Background(), session.ID, 0, types.MessageOutputOptions{})
	if err != nil {
		t.Fatalf("PollMessagesAfter() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("PollMessagesAfter() returned %d messages, want 1", len(messages))
	}
	if messages[0].Content != "pong" {
		t.Fatalf("Content = %q, want pong", messages[0].Content)
	}
	if messages[0].Model.String() != "prov/m1" {
		t.Fatalf("Model = %s, want prov/m1", messages[0].Model)
	}

	latest, err := client.GetLatestAssistantMessage(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("GetLatestAssistantMessage() error = %v", err)
	}
	if latest == nil || latest.CompletedAt <= 0 {
		t.Fatalf("latest = %+v, want a completed assistant message", latest)
	}
}

func TestPromptCancellationInterruptsSession(t *testing.T) {
	server := newFakeServer()
	server.turns = 1000 // stays busy until interrupted
	client := startFake(t, server)

	session, err := client.CreateSession(context.Background(), types.CreateSessionRequest{Directory: "/tmp/project"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	handle, err := client.Prompt(ctx, session.ID, "hang")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	cancel()
	select {
	case err := <-handle.Err():
		if err == nil {
			t.Fatal("expected an error after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt() did not abort")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		server.mu.Lock()
		interrupted := server.interrupt
		server.mu.Unlock()
		if interrupted > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("session was not interrupted")
}

func TestPermissionReplyMapping(t *testing.T) {
	server := newFakeServer()
	server.sessions = append(server.sessions, sessionInfo{ID: "ses_perm", Time: sessionTime{Created: 1, Updated: 1}})
	server.permission["ses_perm"] = []permissionRequest{{
		ID:        "per_1",
		SessionID: "ses_perm",
		Action:    "bash",
		Resources: []string{"make test"},
		Message:   "Run make test",
		Metadata:  map[string]any{"callID": "call-1"},
	}}
	client := startFake(t, server)

	requests, err := client.ListPendingPermissions(context.Background(), "ses_perm")
	if err != nil {
		t.Fatalf("ListPendingPermissions() error = %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("returned %d requests, want 1", len(requests))
	}
	if requests[0].Permission != "Run make test" {
		t.Fatalf("Permission = %q, want Run make test", requests[0].Permission)
	}
	if requests[0].Tool.CallID != "call-1" {
		t.Fatalf("CallID = %q, want call-1", requests[0].Tool.CallID)
	}

	if err := client.ReplyPermission(context.Background(), "ses_perm", "per_1", types.PermissionReplyAlways); err != nil {
		t.Fatalf("ReplyPermission() error = %v", err)
	}
	server.mu.Lock()
	replies := append([]map[string]string(nil), server.replies...)
	server.mu.Unlock()
	if len(replies) != 1 || replies[0]["decision"] != "always" {
		t.Fatalf("replies = %+v, want decision always", replies)
	}

	if err := client.ReplyPermission(context.Background(), "ses_perm", "per_1", types.PermissionReplyOnce); !errors.Is(err, types.ErrInteractionNoLongerPending) {
		t.Fatalf("second ReplyPermission() error = %v, want ErrInteractionNoLongerPending", err)
	}
}

func TestPermissionReplyOnMissingRequestIsNoLongerPending(t *testing.T) {
	server := newFakeServer()
	server.sessions = append(server.sessions, sessionInfo{ID: "ses_perm", Time: sessionTime{Created: 1, Updated: 1}})
	client := startFake(t, server)

	err := client.ReplyPermission(context.Background(), "ses_perm", "per_missing", types.PermissionReplyOnce)
	if !errors.Is(err, types.ErrInteractionNoLongerPending) {
		t.Fatalf("ReplyPermission() error = %v, want ErrInteractionNoLongerPending", err)
	}
}

func TestQuestionsMapFormsToQuestions(t *testing.T) {
	server := newFakeServer()
	server.sessions = append(server.sessions, sessionInfo{ID: "ses_form", Time: sessionTime{Created: 1, Updated: 1}})
	server.forms["ses_form"] = []formInfo{{
		ID:        "frm_1",
		SessionID: "ses_form",
		Title:     "Pick a target",
		Fields: []formField{
			{Key: "env", Title: "Environment", Type: "string", Options: []formOption{
				{Value: "dev", Label: "Development"},
				{Value: "prod", Label: "Production"},
			}},
			{Key: "tags", Title: "Tags", Type: "multiselect", Options: []formOption{
				{Value: "a", Label: "A"},
			}},
		},
	}}
	client := startFake(t, server)

	requests, err := client.ListPendingQuestions(context.Background(), "ses_form")
	if err != nil {
		t.Fatalf("ListPendingQuestions() error = %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("returned %d requests, want 1", len(requests))
	}
	if len(requests[0].Questions) != 2 {
		t.Fatalf("returned %d questions, want 2", len(requests[0].Questions))
	}
	if requests[0].Questions[0].Text != "Environment" {
		t.Fatalf("question text = %q, want Environment", requests[0].Questions[0].Text)
	}
	if len(requests[0].Questions[0].Options) != 2 {
		t.Fatalf("question options = %v, want 2", requests[0].Questions[0].Options)
	}
	if !requests[0].Questions[1].Multiple {
		t.Fatal("multiselect field should be Multiple")
	}

	if err := client.ReplyQuestion(context.Background(), "ses_form", "frm_1", [][]string{{"Production"}, {"a"}}); err != nil {
		t.Fatalf("ReplyQuestion() error = %v", err)
	}
	server.mu.Lock()
	answers := append([]map[string]any(nil), server.formReply...)
	server.mu.Unlock()
	if len(answers) != 1 {
		t.Fatalf("form replies = %d, want 1", len(answers))
	}
	if answers[0]["env"] != "Production" {
		t.Fatalf("env answer = %v, want Production", answers[0]["env"])
	}
	tags, ok := answers[0]["tags"].([]any)
	if !ok || len(tags) != 1 || tags[0] != "a" {
		t.Fatalf("tags answer = %v, want [a]", answers[0]["tags"])
	}
}

func TestListSessionsReadsLocationDirectory(t *testing.T) {
	server := newFakeServer()
	server.sessions = append(server.sessions, sessionInfo{
		ID:       "ses_listed",
		Title:    "Listed",
		Time:     sessionTime{Created: 1000, Updated: 5000},
		Location: &locationRef{Directory: "/tmp/project"},
	})
	client := startFake(t, server)

	sessions, err := client.ListSessions(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("returned %d sessions, want 1", len(sessions))
	}
	if sessions[0].Directory != "/tmp/project" {
		t.Fatalf("Directory = %q, want /tmp/project", sessions[0].Directory)
	}
	if sessions[0].UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt was not parsed")
	}

	// The learned directory is then used as the location for session routes.
	if _, err := client.GetSession(context.Background(), "ses_listed"); err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	server.mu.Lock()
	directory := server.lastQuery["location[directory]"]
	server.mu.Unlock()
	if directory != "/tmp/project" {
		t.Fatalf("location directory = %q, want /tmp/project", directory)
	}
}

func TestExtractContentHonoursOutputFilter(t *testing.T) {
	parts := []messagePart{
		{Type: "text", Text: "answer"},
		{Type: "reasoning", Text: "thought"},
		{Type: "tool", Name: "bash", State: &toolState{
			Status:  "completed",
			Input:   json.RawMessage(`{"command":"ls"}`),
			Content: []toolContent{{Type: "text", Text: "file.txt"}},
		}},
	}

	all := extractContent(parts, types.MessageOutputOptions{})
	if all.Answer != "answer" || all.Reasoning != "thought" || !strings.Contains(all.Tools, "bash") {
		t.Fatalf("unexpected full extraction: %+v", all)
	}

	filtered := extractContent(parts, types.MessageOutputOptions{Include: []types.MessageContentKind{types.MessageContentAnswer}})
	if filtered.Answer != "answer" {
		t.Fatalf("Answer = %q, want answer", filtered.Answer)
	}
	if filtered.Reasoning != "" || filtered.Tools != "" {
		t.Fatalf("expected reasoning and tools to be filtered: %+v", filtered)
	}
}

func TestBasicAuthIsRequiredWhenConfigured(t *testing.T) {
	server := newFakeServer()
	server.authUser = "opencode"
	server.authPass = "secret"
	httpServer := httptest.NewServer(server.handler())
	t.Cleanup(httpServer.Close)

	unauthorized := NewClient(httpServer.URL, WithTimeout(5*time.Second))
	if _, err := unauthorized.ListModels(context.Background(), ""); err == nil {
		t.Fatal("expected an error without credentials")
	}

	authorized := NewClient(httpServer.URL, WithAuthentication("opencode", "secret"), WithTimeout(5*time.Second))
	if _, err := authorized.ListModels(context.Background(), ""); err != nil {
		t.Fatalf("ListModels() with credentials error = %v", err)
	}
}
