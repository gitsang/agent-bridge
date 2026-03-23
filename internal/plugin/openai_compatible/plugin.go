package openai_compatible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gitsang/opencode-connect/internal/connect"
	coreplugin "github.com/gitsang/opencode-connect/internal/plugin"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen string `yaml:"listen"`
}

type Plugin struct {
	name          string
	logger        *slog.Logger
	cfg           Config
	mu            sync.RWMutex
	lastSessionID string
}

func init() {
	constructor := func(name string, configRaw any, infra coreplugin.Infrastructure) (coreplugin.Plugin, error) {
		cfg := defaultConfig()
		configBytes, err := yaml.Marshal(configRaw)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(configBytes, &cfg); err != nil {
			return nil, err
		}

		if infra.Logger == nil {
			return nil, fmt.Errorf("openai-compatible infrastructure logger is required")
		}

		return New(name, infra.Logger, cfg), nil
	}

	coreplugin.Register(coreplugin.PluginFactory{
		Name:      "openai-compatible",
		Construct: constructor,
	})
}

func defaultConfig() Config {
	return Config{Listen: ":8192"}
}

func New(name string, logger *slog.Logger, cfg Config) *Plugin {
	return &Plugin{
		name:   name,
		logger: logger.With("plugin_name", name, "plugin_type", "openai-compatible"),
		cfg:    cfg,
	}
}

func (p *Plugin) Name() string {
	return p.name
}

func (p *Plugin) Serve(ctx context.Context, handle coreplugin.HandleFunc) error {
	if handle == nil {
		return fmt.Errorf("openai-compatible handle is required")
	}

	serverConfig := p.cfg
	server := &http.Server{
		Addr:    serverConfig.Listen,
		Handler: p.newHTTPHandler(handle),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	err := <-errCh
	if err == nil || err == http.ErrServerClosed {
		return nil
	}

	return fmt.Errorf("listen openai-compatible http server: %w", err)
}

func (p *Plugin) Send(_ context.Context, _ *connect.Message) (*connect.Message, error) {
	return nil, fmt.Errorf("openai-compatible plugin does not support proactive send")
}

func (p *Plugin) newHTTPHandler(handle coreplugin.HandleFunc) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)

		var req openAIChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid json")
			return
		}

		message, err := req.UserMessage()
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, err.Error())
			return
		}

		requestSessionID := strings.TrimSpace(req.User)
		sessionID := requestSessionID
		if sessionID == "" {
			sessionID = p.getLastSessionID()
		}

		connectReq := connect.Message{
			Message:   message,
			SessionID: sessionID,
		}

		resp, err := handle(r.Context(), &connectReq)
		if err != nil {
			status := http.StatusInternalServerError
			var connectError *connect.Error
			if errors.As(err, &connectError) {
				status = connectError.StatusCode
			}
			writeOpenAIError(w, status, err.Error())
			return
		}

		if requestSessionID == "" && strings.TrimSpace(resp.SessionID) != "" {
			p.setLastSessionID(resp.SessionID)
		}

		writeJSON(w, http.StatusOK, newOpenAIChatCompletionResponse(req.Model, resp.Message))
	})

	return mux
}

func (p *Plugin) getLastSessionID() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastSessionID
}

func (p *Plugin) setLastSessionID(sessionID string) {
	resolved := strings.TrimSpace(sessionID)
	if resolved == "" {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastSessionID = resolved
}

type openAIChatCompletionRequest struct {
	Model    string                 `json:"model"`
	Messages []openAIRequestMessage `json:"messages"`
	User     string                 `json:"user,omitempty"`
}

type openAIRequestMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func (r openAIChatCompletionRequest) UserMessage() (string, error) {
	if len(r.Messages) == 0 {
		return "", fmt.Errorf("messages is required")
	}

	for i := len(r.Messages) - 1; i >= 0; i-- {
		msg := r.Messages[i]
		if strings.TrimSpace(msg.Role) != "user" {
			continue
		}

		content, err := parseOpenAIMessageContent(msg.Content)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(content) == "" {
			return "", fmt.Errorf("user message content is required")
		}
		return content, nil
	}

	return "", fmt.Errorf("at least one user role message is required")
}

func parseOpenAIMessageContent(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("message content must be a string or text parts array")
	}

	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part.Type) != "text" {
			continue
		}
		if strings.TrimSpace(part.Text) == "" {
			continue
		}
		segments = append(segments, part.Text)
	}

	if len(segments) == 0 {
		return "", fmt.Errorf("user message content is required")
	}

	return strings.Join(segments, "\n"), nil
}

func newOpenAIChatCompletionResponse(model, content string) map[string]any {
	if strings.TrimSpace(model) == "" {
		model = "opencode-connect"
	}

	return map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	}
}

func writeOpenAIError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    statusCode,
		},
	})
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}
