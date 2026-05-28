package ume

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gitsang/agent-bridge/internal/bridge"
	coreplugin "github.com/gitsang/agent-bridge/internal/plugin"
	"gopkg.in/yaml.v3"
)

const defaultSendURL = "https://uc.yealink.com:443/linker/robot/send"

const (
	replyFormatText = "text"
	replyFormatCard = "card"
)

const (
	messageIDRetention  = 24 * time.Hour
	maxRecentMessageIDs = 32
	maxSessionStates    = 1024
	replyTimeout        = 35 * time.Second
)

var atTagPattern = regexp.MustCompile(`(?s)<at\b[^>]*>.*?</at>\s*`)

type Config struct {
	Listen  string `yaml:"listen"`
	SendURL string `yaml:"send_url"`
	Format  string `yaml:"format"`
}

type Plugin struct {
	name       string
	logger     *slog.Logger
	cfg        Config
	httpClient *http.Client

	mu           sync.RWMutex
	sessionState map[string]*chatSessionState
}

type chatSessionState struct {
	recentMessageIDs map[string]time.Time
	lastSeenAt       time.Time
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
		replyFormat, err := normalizeReplyFormat(cfg.Format)
		if err != nil {
			return nil, err
		}
		cfg.Format = replyFormat

		if infra.Logger == nil {
			return nil, fmt.Errorf("ume infrastructure logger is required")
		}

		return New(name, infra.Logger, cfg), nil
	}

	coreplugin.Register(coreplugin.PluginFactory{
		Name:      "ume",
		Construct: constructor,
	})
}

func defaultConfig() Config {
	return Config{
		Listen:  ":8193",
		SendURL: defaultSendURL,
		Format:  replyFormatText,
	}
}

func New(name string, logger *slog.Logger, cfg Config) *Plugin {
	defaultCfg := defaultConfig()
	if strings.TrimSpace(cfg.Listen) == "" {
		cfg.Listen = defaultCfg.Listen
	}
	if strings.TrimSpace(cfg.SendURL) == "" {
		cfg.SendURL = defaultCfg.SendURL
	}
	if replyFormat, err := normalizeReplyFormat(cfg.Format); err == nil {
		cfg.Format = replyFormat
	}

	return &Plugin{
		name:       name,
		logger:     logger.With("plugin_name", name, "plugin_type", "ume"),
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},

		sessionState: map[string]*chatSessionState{},
	}
}

func (p *Plugin) Name() string {
	return p.name
}

func (p *Plugin) Serve(ctx context.Context, handle coreplugin.HandleFunc) error {
	if handle == nil {
		return fmt.Errorf("ume handle is required")
	}

	server := &http.Server{
		Addr:    p.cfg.Listen,
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

	return fmt.Errorf("listen ume http server: %w", err)
}

func (p *Plugin) Send(_ context.Context, _ *bridge.Message) (*bridge.Message, error) {
	return nil, fmt.Errorf("ume plugin does not support proactive send")
}

func (p *Plugin) newHTTPHandler(handle coreplugin.HandleFunc) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		statusCode := http.StatusOK
		logger := p.logger.With(
			"method", r.Method,
			"path", r.URL.Path,
			"content_length", r.ContentLength,
			"remote_addr", r.RemoteAddr,
			"user_agent", r.UserAgent(),
			"x_forwarded_for", strings.TrimSpace(r.Header.Get("X-Forwarded-For")),
			"x_real_ip", strings.TrimSpace(r.Header.Get("X-Real-IP")),
		)
		defer func() {
			logger.Info("ume webhook receive",
				"status_code", statusCode,
				"duration_ms", time.Since(startedAt).Milliseconds(),
				"access_token", r.URL.Query().Get("access_token"),
				"access_token_present", strings.TrimSpace(r.URL.Query().Get("access_token")) != "",
			)
		}()

		if r.Method != http.MethodPost {
			statusCode = http.StatusMethodNotAllowed
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}

		token := strings.TrimSpace(r.URL.Query().Get("access_token"))
		if token == "" {
			statusCode = http.StatusBadRequest
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "access_token is required"})
			return
		}

		request, err := decodeWebhookRequest(r.Body)
		if err != nil {
			statusCode = http.StatusBadRequest
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		messageType := request.MsgType
		if messageType != "" && messageType != "text" {
			p.logger.Debug("ignore unsupported ume message type", "msg_type", messageType)
			writeJSON(w, http.StatusOK, map[string]any{"ignored": 1})
			return
		}

		chatSessionID := request.SessionID.String()
		if chatSessionID == "" {
			statusCode = http.StatusBadRequest
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "sessionId is required"})
			return
		}

		msgID := request.MsgID.String()
		if p.markDuplicate(chatSessionID, msgID) {
			p.logger.Debug("ignore duplicate ume message", "session_id", chatSessionID, "msg_id", msgID)
			writeJSON(w, http.StatusOK, map[string]any{"ignored": 1})
			return
		}

		message := sanitizeMessage(request.Body)
		if message == "" {
			p.logger.Debug("ignore empty ume message after cleanup", "session_id", chatSessionID, "msg_id", msgID)
			writeJSON(w, http.StatusOK, map[string]any{"ignored": 1})
			return
		}

		go func() {
			replyLogger := p.logger.With(
				"session_id", chatSessionID,
				"msg_id", msgID,
				"request_message_length", len(message),
			)
			var replyErr error
			defer func() {
				replyLogger.Debug("message handled and replied", "error", replyErr)
			}()

			connectReq := bridge.Message{
				Content: message,
				Chat: bridge.ChatContext{
					SessionID: chatSessionID,
				},
			}

			sendCount := 0
			reply := func(msg *bridge.Message) error {
				sendCount++
				replyCtx, replyCancel := context.WithTimeout(context.Background(), replyTimeout)
				defer replyCancel()
				return p.sendReply(replyCtx, token, msg)
			}

			err := handle(context.Background(), &connectReq, reply)
			if err != nil {
				var connectError *bridge.Error
				if errors.As(err, &connectError) {
					replyLogger = replyLogger.With("connect_status_code", connectError.StatusCode)
				}

				if sendCount > 0 && errors.As(err, &connectError) && connectError.StatusCode == http.StatusBadGateway && strings.Contains(err.Error(), "prompt failed: MessageAbortedError") {
					replyLogger.Debug("skip sending ume error reply after partial response", "send_count", sendCount)
					return
				}

				errorResp := &bridge.Message{
					Content: fmt.Sprintf("Error: %s", err.Error()),
					Chat: bridge.ChatContext{
						SessionID: chatSessionID,
					},
				}
				replyErr = fmt.Errorf("handle: %w", err)
				replyCtx, replyCancel := context.WithTimeout(context.Background(), replyTimeout)
				defer replyCancel()
				if sendErr := p.sendReply(replyCtx, token, errorResp); sendErr != nil {
					replyLogger = replyLogger.With("send_error_reply_err", sendErr)
				}
				return
			}

			replyLogger = replyLogger.With("send_count", sendCount)
		}()

		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
		})
	})

	return mux
}

func (p *Plugin) markDuplicate(chatSessionID, msgID string) bool {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	resolvedMsgID := strings.TrimSpace(msgID)
	if resolvedChatSessionID == "" || resolvedMsgID == "" {
		return false
	}

	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanupExpiredSessionsLocked(now)
	state := p.ensureSessionStateLocked(resolvedChatSessionID, now)
	p.cleanupExpiredMessageIDsLocked(state, now)
	if _, ok := state.recentMessageIDs[resolvedMsgID]; ok {
		state.lastSeenAt = now
		return true
	}
	state.recentMessageIDs[resolvedMsgID] = now
	state.lastSeenAt = now
	p.limitRecentMessageIDsLocked(state)
	return false
}

func (p *Plugin) ensureSessionStateLocked(chatSessionID string, now time.Time) *chatSessionState {
	state, ok := p.sessionState[chatSessionID]
	if !ok {
		p.limitSessionStatesLocked(now)
		state = &chatSessionState{recentMessageIDs: map[string]time.Time{}}
		p.sessionState[chatSessionID] = state
	}
	if state.recentMessageIDs == nil {
		state.recentMessageIDs = map[string]time.Time{}
	}
	state.lastSeenAt = now
	return state
}

func (p *Plugin) cleanupExpiredSessionsLocked(now time.Time) {
	for chatSessionID, state := range p.sessionState {
		if state == nil {
			delete(p.sessionState, chatSessionID)
			continue
		}
		p.cleanupExpiredMessageIDsLocked(state, now)
		if len(state.recentMessageIDs) == 0 {
			delete(p.sessionState, chatSessionID)
		}
	}
}

func (p *Plugin) cleanupExpiredMessageIDsLocked(state *chatSessionState, now time.Time) {
	for msgID, seenAt := range state.recentMessageIDs {
		if now.Sub(seenAt) > messageIDRetention {
			delete(state.recentMessageIDs, msgID)
		}
	}
}

func (p *Plugin) limitRecentMessageIDsLocked(state *chatSessionState) {
	for len(state.recentMessageIDs) > maxRecentMessageIDs {
		oldestMsgID := ""
		var oldestSeenAt time.Time
		for msgID, seenAt := range state.recentMessageIDs {
			if oldestMsgID == "" || seenAt.Before(oldestSeenAt) {
				oldestMsgID = msgID
				oldestSeenAt = seenAt
			}
		}
		if oldestMsgID == "" {
			return
		}
		delete(state.recentMessageIDs, oldestMsgID)
	}
}

func (p *Plugin) limitSessionStatesLocked(now time.Time) {
	if len(p.sessionState) < maxSessionStates {
		return
	}

	oldestChatSessionID := ""
	var oldestSeenAt time.Time
	for chatSessionID, state := range p.sessionState {
		currentSeenAt := now
		if state != nil && !state.lastSeenAt.IsZero() {
			currentSeenAt = state.lastSeenAt
		}
		if oldestChatSessionID == "" || currentSeenAt.Before(oldestSeenAt) {
			oldestChatSessionID = chatSessionID
			oldestSeenAt = currentSeenAt
		}
	}
	if oldestChatSessionID != "" {
		delete(p.sessionState, oldestChatSessionID)
	}
}

func (p *Plugin) sendReply(ctx context.Context, token string, message *bridge.Message) error {
	payload, err := p.buildSendRequest(message)
	if err != nil {
		return err
	}
	if strings.TrimSpace(payload.Body) == "" {
		return fmt.Errorf("reply body is required")
	}

	endpoint, err := url.Parse(p.cfg.SendURL)
	if err != nil {
		return fmt.Errorf("parse ume send url: %w", err)
	}

	query := endpoint.Query()
	query.Set("access_token", strings.TrimSpace(token))
	endpoint.RawQuery = query.Encode()

	startedAt := time.Now()
	var sendErr error
	var statusCode int
	logger := p.logger.With(
		"send_scheme", endpoint.Scheme,
		"send_host", endpoint.Host,
		"send_path", endpoint.Path,
		"msg_type", payload.MsgType,
		"body_length", len(payload.Body),
		"access_token_present", strings.TrimSpace(token) != "",
	)
	defer func() {
		logger.Debug("ume reply sended",
			"duration_ms", time.Since(startedAt).Milliseconds(),
			"status_code", statusCode,
			"error", sendErr,
		)
	}()

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		sendErr = fmt.Errorf("marshal ume reply: %w", err)
		return sendErr
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payloadBytes))
	if err != nil {
		sendErr = fmt.Errorf("build ume reply request: %w", err)
		return sendErr
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		sendErr = fmt.Errorf("send ume reply: %w", err)
		return sendErr
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	statusCode = httpResp.StatusCode
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		logger = logger.With("response_body", strings.TrimSpace(string(body)))
		sendErr = fmt.Errorf("ume reply failed: status=%d body=%s", httpResp.StatusCode, strings.TrimSpace(string(body)))
		return sendErr
	}

	return nil
}

func (p *Plugin) buildSendRequest(message *bridge.Message) (UmeSendRequest, error) {
	if message == nil {
		return UmeSendRequest{}, fmt.Errorf("reply message is required")
	}

	replyFormat, err := normalizeReplyFormat(p.cfg.Format)
	if err != nil {
		return UmeSendRequest{}, err
	}

	switch replyFormat {
	case replyFormatText:
		return UmeSendRequest{
			MsgType: replyFormatText,
			Body:    formatReply(message),
		}, nil
	case replyFormatCard:
		body, err := formatCardReply(message)
		if err != nil {
			return UmeSendRequest{}, err
		}
		return UmeSendRequest{
			MsgType: replyFormatCard,
			Body:    body,
		}, nil
	default:
		return UmeSendRequest{}, fmt.Errorf("unsupported ume format %q", p.cfg.Format)
	}
}

func formatReply(message *bridge.Message) string {
	title := strings.TrimSpace(message.Agent.Title)
	content := strings.TrimSpace(message.Content)
	directory := strings.TrimSpace(message.Agent.Directory)
	sessionID := strings.TrimSpace(message.Agent.SessionID)
	model := strings.TrimSpace(message.Agent.Model)

	builder := strings.Builder{}
	builder.WriteString(content)
	builder.WriteString("\n\n---\n\n")
	builder.WriteString("Directory: ")
	builder.WriteString(directory)
	builder.WriteString("\nSession: ")
	fmt.Fprintf(&builder, "%s (%s)", title, sessionID)
	builder.WriteString("\nModel: ")
	builder.WriteString(model)

	return builder.String()
}

func formatCardReply(message *bridge.Message) (string, error) {
	title := strings.TrimSpace(message.Agent.Title)
	content := html.EscapeString(strings.TrimSpace(message.Content))
	directory := strings.TrimSpace(message.Agent.Directory)
	sessionID := strings.TrimSpace(message.Agent.SessionID)
	model := strings.TrimSpace(message.Agent.Model)

	card := umeCardBody{
		Header: umeCardHeader{
			Title: umeCardText{
				Tag:     "plainText",
				Content: title,
			},
			Theme: "main",
		},
		Elements: []umeCardElement{
			{
				Tag:     "markdown",
				Content: content,
			},
			{
				Tag: "hr",
			},
			{
				Tag:     "markdown",
				Content: fmt.Sprintf("- *Directory: %s*\n- *Model: %s*\n- *Session: %s*", directory, model, sessionID),
			},
		},
		Link: umeCardLink{
			Tag: "url",
			URL: formatCardLink(directory, model, sessionID),
		},
	}

	bodyBytes, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("marshal ume card body: %w", err)
	}
	return string(bodyBytes), nil
}

func formatCardLink(directory, model, sessionID string) string {
	values := url.Values{}
	values.Set("directory", directory)
	values.Set("model", model)
	values.Set("sessionId", sessionID)
	return "http://localhost?" + values.Encode()
}

func normalizeReplyFormat(format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", replyFormatText:
		return replyFormatText, nil
	case replyFormatCard:
		return replyFormatCard, nil
	default:
		return "", fmt.Errorf("unsupported ume format %q", format)
	}
}

func sanitizeMessage(message string) string {
	cleaned := atTagPattern.ReplaceAllString(message, "")
	return strings.TrimSpace(cleaned)
}

func decodeWebhookRequest(body io.Reader) (UmeWebhookRequest, error) {
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return UmeWebhookRequest{}, fmt.Errorf("read ume webhook body: %w", err)
	}

	var request UmeWebhookRequest
	if err := json.Unmarshal(bodyBytes, &request); err == nil {
		return request, nil
	}

	var requests []UmeWebhookRequest
	if err := json.Unmarshal(bodyBytes, &requests); err != nil {
		return UmeWebhookRequest{}, err
	}
	if len(requests) == 0 {
		return UmeWebhookRequest{}, fmt.Errorf("ume webhook body is empty")
	}

	return requests[0], nil
}

type UmeWebhookRequest struct {
	Body                      string         `json:"body"`
	MsgID                     flexibleString `json:"msgId"`
	MsgType                   string         `json:"msgType"`
	SenderId                  string         `json:"senderId"`
	SessionID                 flexibleString `json:"sessionId"`
	SessionWebhook            string         `json:"sessionWebhook"`
	SessionWebhookExpiredTime int64          `json:"sessionWebhookExpiredTime"`
	Timestamp                 int64          `json:"timestamp"`
}

type UmeSendRequest struct {
	MsgType string `json:"msgType"`
	Body    string `json:"body"`
}

type umeCardBody struct {
	Header   umeCardHeader    `json:"header"`
	Elements []umeCardElement `json:"elements"`
	Link     umeCardLink      `json:"link"`
}

type umeCardHeader struct {
	Title umeCardText `json:"title"`
	Theme string      `json:"theme"`
}

type umeCardText struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

type umeCardElement struct {
	Tag     string `json:"tag"`
	Content string `json:"content,omitempty"`
}

type umeCardLink struct {
	Tag string `json:"tag"`
	URL string `json:"url"`
}

type flexibleString string

func (s *flexibleString) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*s = ""
		return nil
	}

	var stringValue string
	if err := json.Unmarshal(data, &stringValue); err == nil {
		*s = flexibleString(strings.TrimSpace(stringValue))
		return nil
	}

	var intValue int64
	if err := json.Unmarshal(data, &intValue); err == nil {
		*s = flexibleString(strconv.FormatInt(intValue, 10))
		return nil
	}

	return fmt.Errorf("invalid flexible string value: %s", trimmed)
}

func (s flexibleString) String() string {
	return strings.TrimSpace(string(s))
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}
