// Package opencode2 is the agent-bridge driver for OpenCode 2.x. OpenCode 2
// replaced the 1.x session/prompt HTTP API with a typed HttpApi under /api, and
// with it the message, permission, and question shapes; this package speaks
// that API directly so no generated SDK is needed.
package opencode2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// client is a thin JSON client for the OpenCode 2 HttpApi.
type client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
	localizer  *localizer
}

func newClient(baseURL, username, password string, timeout time.Duration) *client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	httpClient := &http.Client{Transport: transport}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}
	return &client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		username:   username,
		password:   password,
		httpClient: httpClient,
	}
}

// apiError is a non-2xx response from the server.
type apiError struct {
	StatusCode int
	Body       string
}

func (e *apiError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("opencode api returned status %d", e.StatusCode)
	}
	return fmt.Sprintf("opencode api returned status %d: %s", e.StatusCode, body)
}

func isNotFound(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// do issues a request and decodes the response into target when non-nil.
func (c *client) do(ctx context.Context, method, path string, query url.Values, body, target any) error {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.username != "" || c.password != "" {
		request.SetBasicAuth(c.username, c.password)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		return &apiError{StatusCode: response.StatusCode, Body: string(payload)}
	}
	if target == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	return json.NewDecoder(response.Body).Decode(target)
}

// envelope is the {"data": ...} wrapper every HttpApi response uses.
type envelope[T any] struct {
	Data T `json:"data"`
}

func (c *client) get(ctx context.Context, path string, query url.Values, target any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, target)
}

func (c *client) post(ctx context.Context, path string, query url.Values, body, target any) error {
	return c.do(ctx, http.MethodPost, path, query, body, target)
}

// locationQuery renders the deepObject location parameter the HttpApi expects.
func locationQuery(values url.Values, directory string) url.Values {
	resolved := strings.TrimSpace(directory)
	if resolved == "" {
		return values
	}
	if values == nil {
		values = url.Values{}
	}
	values.Set("location[directory]", resolved)
	return values
}

// localizer learns which working directory a session belongs to, because most
// HttpApi routes take a location but only the session payload reports it.
type localizer struct {
	directories map[string]string
}

func newLocalizer() *localizer {
	return &localizer{directories: map[string]string{}}
}

func (l *localizer) set(sessionID, directory string) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	resolvedDirectory := strings.TrimSpace(directory)
	if resolvedSessionID == "" || resolvedDirectory == "" {
		return
	}
	l.directories[resolvedSessionID] = resolvedDirectory
}

func (l *localizer) get(sessionID string) string {
	return l.directories[strings.TrimSpace(sessionID)]
}

// HttpApi payloads

type locationRef struct {
	Directory string `json:"directory"`
}

type modelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

type sessionInfo struct {
	ID       string       `json:"id"`
	ParentID string       `json:"parentID,omitempty"`
	Title    string       `json:"title,omitempty"`
	Agent    string       `json:"agent,omitempty"`
	Model    *modelRef    `json:"model,omitempty"`
	Outcome  string       `json:"outcome,omitempty"`
	Time     sessionTime  `json:"time"`
	Location *locationRef `json:"location,omitempty"`
}

type sessionTime struct {
	Created  int64 `json:"created"`
	Updated  int64 `json:"updated"`
	Busy     int64 `json:"busy,omitempty"`
	Idle     int64 `json:"idle,omitempty"`
	Archived int64 `json:"archived,omitempty"`
}

type messageInfo struct {
	ID      string           `json:"id"`
	Time    messageTime      `json:"time"`
	Type    string           `json:"type"`
	Agent   string           `json:"agent,omitempty"`
	Model   *modelRef        `json:"model,omitempty"`
	Text    string           `json:"text,omitempty"`
	Finish  string           `json:"finish,omitempty"`
	Error   *structuredError `json:"error,omitempty"`
	Outcome string           `json:"outcome,omitempty"`
	Content []messagePart    `json:"content,omitempty"`
}

type messageTime struct {
	Created   int64 `json:"created"`
	Streamed  int64 `json:"streamed,omitempty"`
	Completed int64 `json:"completed,omitempty"`
}

type structuredError struct {
	Type         string `json:"type"`
	Message      string `json:"message"`
	Status       int    `json:"status,omitempty"`
	RetryAfterMS int64  `json:"retryAfterMs,omitempty"`
}

func (e *structuredError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type messagePart struct {
	Type   string          `json:"type"`
	Text   string          `json:"text,omitempty"`
	ID     string          `json:"id,omitempty"`
	Name   string          `json:"name,omitempty"`
	State  *toolState      `json:"state,omitempty"`
	Source json.RawMessage `json:"source,omitempty"`
}

type toolState struct {
	Status  string           `json:"status"`
	Input   json.RawMessage  `json:"input,omitempty"`
	Content []toolContent    `json:"content,omitempty"`
	Error   *structuredError `json:"error,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Path string `json:"path,omitempty"`
}

type modelInfo struct {
	ID         string `json:"id"`
	ModelID    string `json:"modelID"`
	ProviderID string `json:"providerID"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
}

type agentInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Mode        string `json:"mode"`
	Hidden      bool   `json:"hidden"`
}

type permissionRequest struct {
	ID        string         `json:"id"`
	SessionID string         `json:"sessionID"`
	Action    string         `json:"action"`
	Resources []string       `json:"resources"`
	Save      []string       `json:"save"`
	Metadata  map[string]any `json:"metadata"`
	Message   string         `json:"message"`
	Source    map[string]any `json:"source"`
}

type formInfo struct {
	ID        string      `json:"id"`
	SessionID string      `json:"sessionID"`
	Title     string      `json:"title"`
	Fields    []formField `json:"fields"`
}

type formField struct {
	Key     string       `json:"key"`
	Title   string       `json:"title"`
	Type    string       `json:"type"`
	Options []formOption `json:"options,omitempty"`
}

type formOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type formReply struct {
	Answer map[string]any `json:"answer"`
}

// promptBody is the POST /api/session/{id}/prompt payload.
type promptBody struct {
	Text string `json:"text"`
}

// createSessionBody is the POST /api/session payload.
type createSessionBody struct {
	Title    *string      `json:"title,omitempty"`
	Agent    *string      `json:"agent,omitempty"`
	Model    *modelRef    `json:"model,omitempty"`
	Location *locationRef `json:"location,omitempty"`
}

func millisToTime(millis int64) time.Time {
	if millis <= 0 {
		return time.Time{}
	}
	return time.Unix(0, millis*int64(time.Millisecond))
}

// seconds returns the millisecond timestamp as Unix seconds, matching the
// CompletedAt unit the bridge polls with.
func millisToSeconds(millis int64) float64 {
	if millis <= 0 {
		return 0
	}
	return float64(millis) / 1000
}

func parseLimit(value string, fallback int) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		if _, err := strconv.Atoi(trimmed); err == nil {
			return trimmed
		}
	}
	return strconv.Itoa(fallback)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func trimStringSlice(values []string) []string {
	resolved := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			resolved = append(resolved, trimmed)
		}
	}
	return resolved
}

func loggerOrDefault(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.Default()
}
