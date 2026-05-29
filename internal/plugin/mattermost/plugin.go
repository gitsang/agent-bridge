package mattermost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gitsang/agent-bridge/internal/bridge"
	coreplugin "github.com/gitsang/agent-bridge/internal/plugin"
	"github.com/mattermost/mattermost-server/v5/model"
	"gopkg.in/yaml.v3"
)

const (
	maxRequestBodyBytes = 1024 * 1024
	responseTimeout     = 35 * time.Second
	postIDRetention     = 24 * time.Hour
	maxRecentPostIDs    = 32
	maxSessionStates    = 1024
	reconnectInterval   = 3 * time.Second
)

const (
	ModeWebhook   = "webhook"
	ModeWebsocket = "websocket"
)

type WebhookConfig struct {
	Listen           string   `yaml:"listen"`
	Token            string   `yaml:"token"`
	ResponseURLHosts []string `yaml:"response_url_hosts"`
}

type WebsocketConfig struct {
	ServerURL   string `yaml:"server_url"`
	WSURL       string `yaml:"ws_url"`
	AccessToken string `yaml:"access_token"`
	BotUserID   string `yaml:"bot_user_id"`
}

type Config struct {
	Mode      string          `yaml:"mode"`
	Webhook   WebhookConfig   `yaml:"webhook"`
	Websocket WebsocketConfig `yaml:"websocket"`
}

type Plugin struct {
	name   string
	logger *slog.Logger
	mode   string

	webhook   *webhookPlugin
	websocket *websocketPlugin
}

type webhookPlugin struct {
	logger     *slog.Logger
	cfg        WebhookConfig
	httpClient *http.Client

	mu           sync.RWMutex
	sessionState map[string]*chatSessionState
}

type websocketPlugin struct {
	logger      *slog.Logger
	cfg         WebsocketConfig
	httpClient  *http.Client
	wsClient    *model.WebSocketClient
	version     string
	agentDriver string
}

type chatSessionState struct {
	recentPostIDs map[string]time.Time
	lastSeenAt    time.Time
}

func init() {
	constructor := func(name string, configRaw any, infra coreplugin.Infrastructure) (coreplugin.Plugin, error) {
		cfg := Config{}
		configBytes, err := yaml.Marshal(configRaw)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(configBytes, &cfg); err != nil {
			return nil, err
		}

		if infra.Logger == nil {
			return nil, fmt.Errorf("mattermost: logger is required")
		}

		return New(name, infra.Logger, cfg, infra)
	}

	coreplugin.Register(coreplugin.PluginFactory{
		Name:      "mattermost",
		Construct: constructor,
	})
}

func New(name string, logger *slog.Logger, cfg Config, infra coreplugin.Infrastructure) (*Plugin, error) {
	mode := strings.TrimSpace(strings.ToLower(cfg.Mode))
	if mode == "" {
		return nil, fmt.Errorf("mattermost: mode is required (webhook or websocket)")
	}

	p := &Plugin{
		name:   name,
		logger: logger.With("plugin_name", name, "plugin_type", "mattermost", "mode", mode),
		mode:   mode,
	}

	switch mode {
	case ModeWebhook:
		if strings.TrimSpace(cfg.Webhook.Token) == "" {
			return nil, fmt.Errorf("mattermost: webhook token is required")
		}
		p.webhook = newWebhookPlugin(logger, name, cfg.Webhook)
	case ModeWebsocket:
		if strings.TrimSpace(cfg.Websocket.ServerURL) == "" {
			return nil, fmt.Errorf("mattermost: websocket server_url is required")
		}
		if strings.TrimSpace(cfg.Websocket.WSURL) == "" {
			return nil, fmt.Errorf("mattermost: websocket ws_url is required")
		}
		if strings.TrimSpace(cfg.Websocket.AccessToken) == "" {
			return nil, fmt.Errorf("mattermost: websocket access_token is required")
		}
		p.websocket = newWebsocketPlugin(logger, name, cfg.Websocket, infra.Version, infra.AgentDriver)
	default:
		return nil, fmt.Errorf("mattermost: invalid mode %q, must be webhook or websocket", cfg.Mode)
	}

	return p, nil
}

func newWebhookPlugin(logger *slog.Logger, name string, cfg WebhookConfig) *webhookPlugin {
	if strings.TrimSpace(cfg.Listen) == "" {
		cfg.Listen = ":8194"
	}

	return &webhookPlugin{
		logger:       logger.With("plugin_name", name, "plugin_type", "mattermost-webhook"),
		cfg:          cfg,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		sessionState: map[string]*chatSessionState{},
	}
}

func newWebsocketPlugin(logger *slog.Logger, name string, cfg WebsocketConfig, version, agentDriver string) *websocketPlugin {
	return &websocketPlugin{
		logger:      logger.With("plugin_name", name, "plugin_type", "mattermost-websocket"),
		cfg:         cfg,
		httpClient:  &http.Client{Timeout: responseTimeout},
		version:     version,
		agentDriver: agentDriver,
	}
}

func (p *Plugin) Name() string {
	return p.name
}

func (p *Plugin) Serve(ctx context.Context, handle coreplugin.HandleFunc) error {
	switch p.mode {
	case ModeWebhook:
		return p.webhook.Serve(ctx, handle)
	case ModeWebsocket:
		return p.websocket.Serve(ctx, handle)
	default:
		return fmt.Errorf("mattermost: invalid mode %q", p.mode)
	}
}

func (p *Plugin) Send(ctx context.Context, msg *bridge.Message) (*bridge.Message, error) {
	switch p.mode {
	case ModeWebhook:
		return p.webhook.Send(ctx, msg)
	case ModeWebsocket:
		return p.websocket.Send(ctx, msg)
	default:
		return nil, fmt.Errorf("mattermost: invalid mode %q", p.mode)
	}
}

// webhookPlugin implementation

func (p *webhookPlugin) Serve(ctx context.Context, handle coreplugin.HandleFunc) error {
	if handle == nil {
		return fmt.Errorf("mattermost handle is required")
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

	return fmt.Errorf("listen mattermost http server: %w", err)
}

func (p *webhookPlugin) Send(_ context.Context, _ *bridge.Message) (*bridge.Message, error) {
	return nil, fmt.Errorf("mattermost webhook plugin does not support proactive send")
}

func (p *webhookPlugin) newHTTPHandler(handle coreplugin.HandleFunc) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

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
			logger.Info("mattermost webhook receive",
				"status_code", statusCode,
				"duration_ms", time.Since(startedAt).Milliseconds(),
			)
		}()

		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			statusCode = http.StatusMethodNotAllowed
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}

		request, err := decodeRequest(r)
		if err != nil {
			statusCode = http.StatusBadRequest
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if err := p.checkToken(r.Method, request.Token, r.Header.Get("Authorization")); err != nil {
			statusCode = http.StatusUnauthorized
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
			return
		}

		content := strings.TrimSpace(request.Text)
		if content == "" {
			statusCode = http.StatusBadRequest
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "text is required"})
			return
		}

		connectReq := bridge.Message{
			Content: content,
			Chat: bridge.ChatContext{
				SessionID: request.SessionID(),
			},
		}
		if p.markDuplicate(connectReq.Chat.SessionID, request.PostID) {
			writeJSON(w, http.StatusOK, MattermostResponse{ResponseType: "ephemeral", Text: "duplicate request ignored"})
			return
		}

		if strings.TrimSpace(request.ResponseURL) != "" {
			if err := p.validateResponseURL(request.ResponseURL); err != nil {
				statusCode = http.StatusBadRequest
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			go p.handleAsync(request, &connectReq, handle)
			writeJSON(w, http.StatusOK, MattermostResponse{ResponseType: "ephemeral", Text: "request accepted"})
			return
		}

		var last *bridge.Message
		err = handle(r.Context(), &connectReq, func(msg *bridge.Message) error {
			last = msg
			return nil
		})
		if err != nil {
			status, message := responseError(err)
			statusCode = status
			writeJSON(w, status, MattermostResponse{ResponseType: "ephemeral", Text: message})
			return
		}
		if last == nil {
			statusCode = http.StatusInternalServerError
			writeJSON(w, http.StatusInternalServerError, MattermostResponse{ResponseType: "ephemeral", Text: "Error: no reply received"})
			return
		}

		resp, err := buildResponse(last)
		if err != nil {
			statusCode = http.StatusInternalServerError
			writeJSON(w, http.StatusInternalServerError, MattermostResponse{ResponseType: "ephemeral", Text: "Error: internal error"})
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})

	return mux
}

func (p *webhookPlugin) validateResponseURL(rawURL string) error {
	endpoint, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid response_url")
	}
	if endpoint.Scheme != "https" && endpoint.Scheme != "http" {
		return fmt.Errorf("response_url scheme is not allowed")
	}
	if endpoint.Host == "" {
		return fmt.Errorf("response_url host is required")
	}
	allowedHosts := p.allowedResponseURLHosts()
	if len(allowedHosts) == 0 {
		return fmt.Errorf("response_url host is not configured")
	}
	if _, ok := allowedHosts[strings.ToLower(endpoint.Host)]; !ok {
		return fmt.Errorf("response_url host is not allowed")
	}
	return nil
}

func (p *webhookPlugin) allowedResponseURLHosts() map[string]struct{} {
	allowed := map[string]struct{}{}
	for _, host := range p.cfg.ResponseURLHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" {
			continue
		}
		allowed[host] = struct{}{}
	}
	return allowed
}

func (p *webhookPlugin) checkToken(method string, token string, authorization string) error {
	configuredToken := strings.TrimSpace(p.cfg.Token)
	if configuredToken == "" {
		return fmt.Errorf("token is not configured")
	}
	requestToken := ""
	if method != http.MethodGet {
		requestToken = strings.TrimSpace(token)
	}
	authorization = strings.TrimSpace(authorization)
	if strings.HasPrefix(strings.ToLower(authorization), "token ") {
		requestToken = strings.TrimSpace(authorization[len("Token "):])
	}
	if !sameToken(requestToken, configuredToken) {
		return fmt.Errorf("invalid token")
	}
	return nil
}

func sameToken(requestToken string, configuredToken string) bool {
	requestSum := sha256.Sum256([]byte(requestToken))
	configuredSum := sha256.Sum256([]byte(configuredToken))
	return subtle.ConstantTimeCompare(requestSum[:], configuredSum[:]) == 1
}

func (p *webhookPlugin) handleAsync(request MattermostRequest, connectReq *bridge.Message, handle coreplugin.HandleFunc) {
	replyLogger := p.logger.With(
		"channel_id", strings.TrimSpace(request.ChannelID),
		"user_id", strings.TrimSpace(request.UserID),
		"response_url_present", strings.TrimSpace(request.ResponseURL) != "",
	)
	var replyErr error
	defer func() {
		replyLogger.Debug("mattermost message handled and replied", "error", replyErr)
	}()

	sendCount := 0
	reply := func(msg *bridge.Message) error {
		sendCount++
		ctx, cancel := context.WithTimeout(context.Background(), responseTimeout)
		defer cancel()
		return p.sendResponse(ctx, request.ResponseURL, msg)
	}

	if err := handle(context.Background(), connectReq, reply); err != nil {
		replyErr = fmt.Errorf("handle: %w", err)
		var connectError *bridge.Error
		if sendCount > 0 && errors.As(err, &connectError) && connectError.StatusCode == http.StatusBadGateway && strings.Contains(err.Error(), "prompt failed: MessageAbortedError") {
			replyLogger.Debug("skip sending mattermost error reply after partial response", "send_count", sendCount)
			return
		}

		_, message := responseError(err)
		errorResp := &bridge.Message{Content: message, Chat: connectReq.Chat}
		ctx, cancel := context.WithTimeout(context.Background(), responseTimeout)
		defer cancel()
		if sendErr := p.sendResponse(ctx, request.ResponseURL, errorResp); sendErr != nil {
			replyLogger = replyLogger.With("send_error_reply_err", sendErr)
		}
		return
	}

	replyLogger = replyLogger.With("send_count", sendCount)
}

func responseError(err error) (int, string) {
	status := http.StatusInternalServerError
	message := "Error: internal error"
	var connectError *bridge.Error
	if errors.As(err, &connectError) {
		status = connectError.StatusCode
		if connectError.StatusCode >= http.StatusBadRequest && connectError.StatusCode < http.StatusInternalServerError {
			message = fmt.Sprintf("Error: %s", connectError.Error())
		}
	}
	return status, message
}

func (p *webhookPlugin) sendResponse(ctx context.Context, responseURL string, message *bridge.Message) error {
	payload, err := buildResponse(message)
	if err != nil {
		return err
	}

	endpoint := strings.TrimSpace(responseURL)
	if endpoint == "" {
		return fmt.Errorf("response_url is required")
	}

	startedAt := time.Now()
	var sendErr error
	var statusCode int
	logger := p.logger.With("body_length", len(payload.Text))
	defer func() {
		logger.Debug("mattermost reply sent",
			"duration_ms", time.Since(startedAt).Milliseconds(),
			"status_code", statusCode,
			"error", sendErr,
		)
	}()

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		sendErr = fmt.Errorf("marshal mattermost reply: %w", err)
		return sendErr
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		sendErr = fmt.Errorf("build mattermost reply request: %w", err)
		return sendErr
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		sendErr = fmt.Errorf("send mattermost reply: %w", err)
		return sendErr
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	statusCode = httpResp.StatusCode
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		logger = logger.With("response_body", strings.TrimSpace(string(body)))
		sendErr = fmt.Errorf("mattermost reply failed: status=%d body=%s", httpResp.StatusCode, strings.TrimSpace(string(body)))
		return sendErr
	}

	return nil
}

func (p *webhookPlugin) markDuplicate(chatSessionID string, postID string) bool {
	resolvedChatSessionID := strings.TrimSpace(chatSessionID)
	resolvedPostID := strings.TrimSpace(postID)
	if resolvedChatSessionID == "" || resolvedPostID == "" {
		return false
	}

	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanupExpiredSessionsLocked(now)
	state := p.ensureSessionStateLocked(resolvedChatSessionID, now)
	p.cleanupExpiredPostIDsLocked(state, now)
	if _, ok := state.recentPostIDs[resolvedPostID]; ok {
		state.lastSeenAt = now
		return true
	}
	state.recentPostIDs[resolvedPostID] = now
	state.lastSeenAt = now
	p.limitRecentPostIDsLocked(state)
	return false
}

func (p *webhookPlugin) ensureSessionStateLocked(chatSessionID string, now time.Time) *chatSessionState {
	state, ok := p.sessionState[chatSessionID]
	if !ok {
		p.limitSessionStatesLocked(now)
		state = &chatSessionState{recentPostIDs: map[string]time.Time{}}
		p.sessionState[chatSessionID] = state
	}
	if state.recentPostIDs == nil {
		state.recentPostIDs = map[string]time.Time{}
	}
	state.lastSeenAt = now
	return state
}

func (p *webhookPlugin) cleanupExpiredSessionsLocked(now time.Time) {
	for chatSessionID, state := range p.sessionState {
		if state == nil {
			delete(p.sessionState, chatSessionID)
			continue
		}
		p.cleanupExpiredPostIDsLocked(state, now)
		if len(state.recentPostIDs) == 0 {
			delete(p.sessionState, chatSessionID)
		}
	}
}

func (p *webhookPlugin) cleanupExpiredPostIDsLocked(state *chatSessionState, now time.Time) {
	for postID, seenAt := range state.recentPostIDs {
		if now.Sub(seenAt) > postIDRetention {
			delete(state.recentPostIDs, postID)
		}
	}
}

func (p *webhookPlugin) limitRecentPostIDsLocked(state *chatSessionState) {
	for len(state.recentPostIDs) > maxRecentPostIDs {
		oldestPostID := ""
		var oldestSeenAt time.Time
		for postID, seenAt := range state.recentPostIDs {
			if oldestPostID == "" || seenAt.Before(oldestSeenAt) {
				oldestPostID = postID
				oldestSeenAt = seenAt
			}
		}
		if oldestPostID == "" {
			return
		}
		delete(state.recentPostIDs, oldestPostID)
	}
}

func (p *webhookPlugin) limitSessionStatesLocked(now time.Time) {
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

// websocketPlugin implementation

func (p *websocketPlugin) Serve(ctx context.Context, handle coreplugin.HandleFunc) error {
	if handle == nil {
		return fmt.Errorf("mattermost-ws: handle is required")
	}

	if p.cfg.BotUserID == "" {
		userID, err := p.fetchBotUserID()
		if err != nil {
			return fmt.Errorf("mattermost-ws: fetch bot user id: %w", err)
		}
		p.cfg.BotUserID = userID
		p.logger.Info("fetched bot user id", "bot_user_id", userID)
	}

	p.logger.Info("starting mattermost websocket plugin",
		"server_url", p.cfg.ServerURL,
		"ws_url", p.cfg.WSURL,
		"bot_user_id", p.cfg.BotUserID,
	)

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("mattermost websocket plugin stopped")
			return nil
		default:
		}

		if err := p.connectAndListen(ctx, handle); err != nil {
			p.logger.Error("websocket connection failed, reconnecting...",
				"error", err,
				"reconnect_in", reconnectInterval,
			)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(reconnectInterval):
		}
	}
}

func (p *websocketPlugin) fetchBotUserID() (string, error) {
	url := fmt.Sprintf("%s/api/v4/users/me", p.cfg.ServerURL)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.AccessToken)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("status=%d body=%s", resp.StatusCode, string(body))
	}

	var user struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", err
	}
	if user.ID == "" {
		return "", fmt.Errorf("empty user id in response")
	}
	return user.ID, nil
}

func (p *websocketPlugin) connectAndListen(ctx context.Context, handle coreplugin.HandleFunc) error {
	wsClient, err := model.NewWebSocketClient4(p.cfg.WSURL, p.cfg.AccessToken)
	if err != nil {
		return fmt.Errorf("create websocket client: %w", err)
	}
	p.wsClient = wsClient

	defer func() {
		wsClient.Close()
		p.wsClient = nil
	}()

	wsClient.Listen()
	p.logger.Info("websocket connected")

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-wsClient.EventChannel:
			if !ok {
				return fmt.Errorf("websocket channel closed: %v", wsClient.ListenError)
			}
			p.handleEvent(event, handle)
		}
	}
}

func (p *websocketPlugin) handleEvent(event *model.WebSocketEvent, handle coreplugin.HandleFunc) {
	if event.EventType() != model.WEBSOCKET_EVENT_POSTED {
		return
	}

	if event.EventType() == model.WEBSOCKET_EVENT_STATUS_CHANGE {
		if event.GetBroadcast().UserId == p.cfg.BotUserID {
			status, ok := event.GetData()["status"].(string)
			if ok && status == "away" {
				p.logger.Debug("bot status away, will reconnect")
				return
			}
		}
	}

	post, valid := p.validatePostEvent(event)
	if !valid {
		return
	}

	p.logger.Info("received message",
		"channel_id", post.ChannelId,
		"user_id", post.UserId,
		"message", truncate(post.Message, 100),
	)

	sessionID := fmt.Sprintf("%s:%s:%s",
		event.GetData()["team_id"],
		post.ChannelId,
		post.UserId,
	)

	req := &bridge.Message{
		Content: post.Message,
		Chat: bridge.ChatContext{
			SessionID: sessionID,
		},
	}

	reply := func(msg *bridge.Message) error {
		return p.sendMessage(post.ChannelId, msg)
	}

	go func() {
		if err := handle(context.Background(), req, reply); err != nil {
			p.logger.Error("handle message failed",
				"error", err,
				"channel_id", post.ChannelId,
			)
			p.sendError(post.ChannelId, err)
		}
	}()
}

func (p *websocketPlugin) validatePostEvent(event *model.WebSocketEvent) (*model.Post, bool) {
	channelType, ok := event.GetData()["channel_type"].(string)
	if !ok {
		return nil, false
	}

	if channelType != model.CHANNEL_DIRECT {
		mentionsStr, ok := event.GetData()["mentions"].(string)
		if !ok {
			return nil, false
		}

		var mentions []string
		if err := json.Unmarshal([]byte(mentionsStr), &mentions); err != nil {
			return nil, false
		}

		mentioned := false
		for _, m := range mentions {
			if m == p.cfg.BotUserID {
				mentioned = true
				break
			}
		}
		if !mentioned {
			return nil, false
		}
	}

	postBytes, ok := event.GetData()["post"].(string)
	if !ok {
		return nil, false
	}
	post := model.PostFromJson(strings.NewReader(postBytes))
	if post == nil {
		return nil, false
	}

	if post.UserId == p.cfg.BotUserID {
		return nil, false
	}

	return post, true
}

func (p *websocketPlugin) sendMessage(channelID string, message *bridge.Message) error {
	attachment := p.buildAttachment(message)

	post := &model.Post{
		ChannelId: channelID,
		Props: map[string]any{
			"attachments": []*model.SlackAttachment{attachment},
		},
	}

	postBytes, err := json.Marshal(post)
	if err != nil {
		return fmt.Errorf("marshal post: %w", err)
	}

	url := fmt.Sprintf("%s/api/v4/posts", p.cfg.ServerURL)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(postBytes))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.cfg.AccessToken)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("send message failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	return nil
}

func (p *websocketPlugin) sendError(channelID string, err error) {
	attachment := &model.SlackAttachment{
		Fallback: "Error: " + err.Error(),
		Color:    "#FF0000",
		Text:     "Error: " + err.Error(),
		Footer:   fmt.Sprintf("agent-bridge %s (%s)", p.version, p.agentDriver),
	}

	post := &model.Post{
		ChannelId: channelID,
		Props: map[string]any{
			"attachments": []*model.SlackAttachment{attachment},
		},
	}

	postBytes, err := json.Marshal(post)
	if err != nil {
		p.logger.Error("marshal error post failed", "error", err)
		return
	}

	url := fmt.Sprintf("%s/api/v4/posts", p.cfg.ServerURL)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(postBytes))
	if err != nil {
		p.logger.Error("create error request failed", "error", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.cfg.AccessToken)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		p.logger.Error("send error message failed", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		p.logger.Error("send error message failed",
			"status_code", resp.StatusCode,
			"response_body", string(body),
		)
	}
}

func (p *websocketPlugin) buildAttachment(message *bridge.Message) *model.SlackAttachment {
	title := strings.TrimSpace(message.Agent.Title)
	content := strings.TrimSpace(message.Content)
	directory := strings.TrimSpace(message.Agent.Directory)
	sessionID := strings.TrimSpace(message.Agent.SessionID)
	modelName := strings.TrimSpace(message.Agent.Model)

	fields := []*model.SlackAttachmentField{
		{Short: true, Title: "Directory", Value: directory},
		{Short: true, Title: "Model", Value: modelName},
		{Short: false, Title: "Session", Value: fmt.Sprintf("%s (%s)", title, sessionID)},
	}

	return &model.SlackAttachment{
		Fallback: content,
		Color:    "#0066CC",
		Title:    title,
		Text:     content,
		Fields:   fields,
		Footer:   fmt.Sprintf("agent-bridge %s (%s)", p.version, p.agentDriver),
	}
}

func (p *websocketPlugin) Send(_ context.Context, _ *bridge.Message) (*bridge.Message, error) {
	return nil, fmt.Errorf("mattermost websocket plugin does not support proactive send yet")
}

// shared types and functions

func buildResponse(message *bridge.Message) (MattermostResponse, error) {
	if message == nil {
		return MattermostResponse{}, fmt.Errorf("reply message is required")
	}
	text := formatReply(message)
	if strings.TrimSpace(text) == "" {
		return MattermostResponse{}, fmt.Errorf("reply text is required")
	}
	return MattermostResponse{ResponseType: "ephemeral", Text: text}, nil
}

func formatReply(message *bridge.Message) string {
	title := strings.TrimSpace(message.Agent.Title)
	content := strings.TrimSpace(message.Content)
	directory := strings.TrimSpace(message.Agent.Directory)
	sessionID := strings.TrimSpace(message.Agent.SessionID)
	modelName := strings.TrimSpace(message.Agent.Model)

	if directory == "" && sessionID == "" && modelName == "" && title == "" {
		return content
	}

	builder := strings.Builder{}
	builder.WriteString(content)
	builder.WriteString("\n\n---\n\n")
	builder.WriteString("Directory: ")
	builder.WriteString(directory)
	builder.WriteString("\nSession: ")
	fmt.Fprintf(&builder, "%s (%s)", title, sessionID)
	builder.WriteString("\nModel: ")
	builder.WriteString(modelName)

	return builder.String()
}

func decodeRequest(r *http.Request) (MattermostRequest, error) {
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.Contains(contentType, "application/json") {
		var request MattermostRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return MattermostRequest{}, err
		}
		return request, nil
	}

	if err := r.ParseForm(); err != nil {
		return MattermostRequest{}, err
	}
	return MattermostRequest{
		Token:       r.Form.Get("token"),
		TeamID:      r.Form.Get("team_id"),
		TeamDomain:  r.Form.Get("team_domain"),
		ChannelID:   r.Form.Get("channel_id"),
		ChannelName: r.Form.Get("channel_name"),
		UserID:      r.Form.Get("user_id"),
		UserName:    r.Form.Get("user_name"),
		PostID:      r.Form.Get("post_id"),
		Command:     r.Form.Get("command"),
		Text:        r.Form.Get("text"),
		ResponseURL: r.Form.Get("response_url"),
		TriggerID:   r.Form.Get("trigger_id"),
	}, nil
}

type MattermostRequest struct {
	Token       string `json:"token"`
	TeamID      string `json:"team_id"`
	TeamDomain  string `json:"team_domain"`
	ChannelID   string `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	UserID      string `json:"user_id"`
	UserName    string `json:"user_name"`
	PostID      string `json:"post_id"`
	Command     string `json:"command"`
	Text        string `json:"text"`
	ResponseURL string `json:"response_url"`
	TriggerID   string `json:"trigger_id"`
}

func (r MattermostRequest) SessionID() string {
	parts := make([]string, 0, 3)
	for _, part := range []string{r.TeamID, r.ChannelID, r.UserID} {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, ":")
}

type MattermostResponse struct {
	ResponseType string `json:"response_type"`
	Text         string `json:"text"`
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
