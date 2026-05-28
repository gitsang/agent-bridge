package mattermost_ws

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gitsang/agent-bridge/internal/bridge"
	coreplugin "github.com/gitsang/agent-bridge/internal/plugin"
	"github.com/mattermost/mattermost-server/v5/model"
	"gopkg.in/yaml.v3"
)

const (
	reconnectInterval = 3 * time.Second
	responseTimeout   = 30 * time.Second
)

type Config struct {
	ServerURL   string `yaml:"server_url"`
	WSURL       string `yaml:"ws_url"`
	AccessToken string `yaml:"access_token"`
	BotUserID   string `yaml:"bot_user_id"`
}

type Plugin struct {
	name       string
	logger     *slog.Logger
	cfg        Config
	httpClient *http.Client
	wsClient   *model.WebSocketClient
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

		if strings.TrimSpace(cfg.ServerURL) == "" {
			return nil, fmt.Errorf("mattermost-ws: server_url is required")
		}
		if strings.TrimSpace(cfg.WSURL) == "" {
			return nil, fmt.Errorf("mattermost-ws: ws_url is required")
		}
		if strings.TrimSpace(cfg.AccessToken) == "" {
			return nil, fmt.Errorf("mattermost-ws: access_token is required")
		}
		if infra.Logger == nil {
			return nil, fmt.Errorf("mattermost-ws: logger is required")
		}

		return New(name, infra.Logger, cfg), nil
	}

	coreplugin.Register(coreplugin.PluginFactory{
		Name:      "mattermost-websocket",
		Construct: constructor,
	})
}

func New(name string, logger *slog.Logger, cfg Config) *Plugin {
	return &Plugin{
		name:       name,
		logger:     logger.With("plugin_name", name, "plugin_type", "mattermost-websocket"),
		cfg:        cfg,
		httpClient: &http.Client{Timeout: responseTimeout},
	}
}

func (p *Plugin) Name() string {
	return p.name
}

func (p *Plugin) fetchBotUserID() (string, error) {
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

func (p *Plugin) Serve(ctx context.Context, handle coreplugin.HandleFunc) error {
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

func (p *Plugin) connectAndListen(ctx context.Context, handle coreplugin.HandleFunc) error {
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

func (p *Plugin) handleEvent(event *model.WebSocketEvent, handle coreplugin.HandleFunc) {
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
		return p.sendMessage(post.ChannelId, msg.Content)
	}

	if err := handle(context.Background(), req, reply); err != nil {
		p.logger.Error("handle message failed",
			"error", err,
			"channel_id", post.ChannelId,
		)
		p.sendMessage(post.ChannelId, "Error: "+err.Error())
	}
}

func (p *Plugin) validatePostEvent(event *model.WebSocketEvent) (*model.Post, bool) {
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

func (p *Plugin) sendMessage(channelID, content string) error {
	reqBody := map[string]string{
		"channel_id": channelID,
		"message":    content,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v4/posts", p.cfg.ServerURL)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
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

func (p *Plugin) Send(_ context.Context, _ *bridge.Message) (*bridge.Message, error) {
	return nil, fmt.Errorf("mattermost-ws: proactive send not supported yet")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
