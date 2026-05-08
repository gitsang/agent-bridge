package ume

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/gitsang/agent-bridge/internal/bridge"
)

func TestPluginBuildSendRequestFormats(t *testing.T) {
	msg := &bridge.Message{
		Content: "hello",
		Agent: bridge.AgentContext{
			SessionID: "session-1",
			Title:     "Session Title",
			Model:     "gpt-test",
			Directory: "/tmp/project",
		},
	}

	tests := []struct {
		name    string
		format  string
		wantErr bool
		check   func(t *testing.T, request UmeSendRequest)
	}{
		{
			name:   "text",
			format: replyFormatText,
			check: func(t *testing.T, request UmeSendRequest) {
				if got, want := request.MsgType, "text"; got != want {
					t.Fatalf("MsgType = %q, want %q", got, want)
				}
				if !strings.Contains(request.Body, "hello") {
					t.Fatalf("Body = %q, want content", request.Body)
				}
				if !strings.Contains(request.Body, "Directory: /tmp/project") {
					t.Fatalf("Body = %q, want directory", request.Body)
				}
			},
		},
		{
			name:   "card",
			format: replyFormatCard,
			check: func(t *testing.T, request UmeSendRequest) {
				if got, want := request.MsgType, "card"; got != want {
					t.Fatalf("MsgType = %q, want %q", got, want)
				}

				payloadBytes, err := json.Marshal(request)
				if err != nil {
					t.Fatalf("marshal request: %v", err)
				}
				var payload map[string]any
				if err := json.Unmarshal(payloadBytes, &payload); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				bodyString, ok := payload["body"].(string)
				if !ok {
					t.Fatalf("payload body type = %T, want string", payload["body"])
				}

				var body struct {
					Header struct {
						Title struct {
							Tag     string `json:"tag"`
							Content string `json:"content"`
						} `json:"title"`
						Theme string `json:"theme"`
					} `json:"header"`
					Elements []struct {
						Tag     string `json:"tag"`
						Content string `json:"content"`
					} `json:"elements"`
					Link struct {
						Tag string `json:"tag"`
						URL string `json:"url"`
					} `json:"link"`
				}
				if err := json.Unmarshal([]byte(bodyString), &body); err != nil {
					t.Fatalf("card Body is not json string: %v", err)
				}
				if got, want := body.Header.Title.Content, "Session Title"; got != want {
					t.Fatalf("header title = %q, want %q", got, want)
				}
				if got, want := body.Elements[0].Content, "hello"; got != want {
					t.Fatalf("first element content = %q, want %q", got, want)
				}
				if got, want := body.Elements[2].Content, "- *Directory: /tmp/project*\n- *Model: gpt-test*\n- *Session: session-1*"; got != want {
					t.Fatalf("metadata content = %q, want %q", got, want)
				}
				if got, want := body.Link.Tag, "url"; got != want {
					t.Fatalf("link tag = %q, want %q", got, want)
				}
				if got := body.Link.URL; !strings.Contains(got, "directory=%2Ftmp%2Fproject") || !strings.Contains(got, "model=gpt-test") || !strings.Contains(got, "sessionId=session-1") {
					t.Fatalf("link url = %q, want query params", got)
				}
			},
		},
		{
			name:    "invalid",
			format:  "html",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := New("ume-test", slog.Default(), Config{Format: tt.format})
			request, err := plugin.buildSendRequest(msg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("buildSendRequest() error is nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("buildSendRequest() error = %v", err)
			}
			tt.check(t, request)
		})
	}
}
