package acp

import "encoding/json"

// Minimal Agent Client Protocol type set. Only the fields agent-bridge maps
// onto its own session/message model are modelled here; unknown fields are
// ignored so an agent adding fields does not break the driver.

type clientCapabilities struct {
	FS       *fsCapabilities `json:"fs,omitempty"`
	Terminal *bool           `json:"terminal,omitempty"`
}

type fsCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type initializeRequest struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
	ClientInfo         *implementation    `json:"clientInfo,omitempty"`
}

type initializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities agentCapabilities `json:"agentCapabilities"`
	AgentInfo         *implementation   `json:"agentInfo,omitempty"`
	AuthMethods       []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"authMethods,omitempty"`
}

type agentCapabilities struct {
	LoadSession        bool `json:"loadSession"`
	PromptCapabilities struct {
		Image           bool `json:"image"`
		Audio           bool `json:"audio"`
		EmbeddedContext bool `json:"embeddedContext"`
	} `json:"promptCapabilities"`
	SessionCapabilities struct {
		List   *sessionCapability `json:"list,omitempty"`
		Resume *sessionCapability `json:"resume,omitempty"`
		Close  *sessionCapability `json:"close,omitempty"`
		Delete *sessionCapability `json:"delete,omitempty"`
	} `json:"sessionCapabilities"`
}

// sessionCapability marks a supported optional session method. Agents advertise
// support by sending an (empty) object, so presence is what matters.
type sessionCapability struct{}

func (s *sessionCapability) enabled() bool { return s != nil }

type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
}

func textBlock(text string) contentBlock {
	return contentBlock{Type: "text", Text: text}
}

type newSessionRequest struct {
	CWD        string `json:"cwd"`
	MCPServers []any  `json:"mcpServers"`
}

type newSessionResponse struct {
	SessionID     string         `json:"sessionId"`
	ConfigOptions []configOption `json:"configOptions,omitempty"`
}

type resumeSessionRequest struct {
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	MCPServers []any  `json:"mcpServers"`
}

type closeSessionRequest struct {
	SessionID string `json:"sessionId"`
}

type listSessionsRequest struct {
	CWD    string `json:"cwd,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type listSessionsResponse struct {
	Sessions []struct {
		SessionID string `json:"sessionId"`
		CWD       string `json:"cwd"`
		Title     string `json:"title,omitempty"`
		UpdatedAt string `json:"updatedAt,omitempty"`
	} `json:"sessions"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type promptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

type promptResponse struct {
	StopReason string `json:"stopReason"`
}

type cancelNotification struct {
	SessionID string `json:"sessionId"`
}

type setConfigOptionRequest struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

type setConfigOptionResponse struct {
	ConfigOptions []configOption `json:"configOptions,omitempty"`
}

type configOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Category     string `json:"category,omitempty"`
	Type         string `json:"type"`
	CurrentValue string `json:"currentValue,omitempty"`
	Options      []any  `json:"options,omitempty"`
}

// sessionUpdate is the payload of a session/update notification.
type sessionUpdate struct {
	SessionID string        `json:"sessionId"`
	Update    updatePayload `json:"update"`
}

// updatePayload is the union of the ACP session update shapes. Fields shared by
// several shapes with different types are kept raw and decoded by the driver.
type updatePayload struct {
	SessionUpdate string `json:"sessionUpdate"`

	// content chunks (agent_message_chunk, agent_thought_chunk, ...)
	MessageID string          `json:"messageId,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`

	// tool_call / tool_call_update, and session_info_update title
	ToolCallID string          `json:"toolCallId,omitempty"`
	Title      string          `json:"title,omitempty"`
	Name       string          `json:"name,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Status     string          `json:"status,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage `json:"rawOutput,omitempty"`

	// plan
	Entries []planEntry `json:"entries,omitempty"`

	// usage_update
	Used int64 `json:"used,omitempty"`
	Size int64 `json:"size,omitempty"`

	// config_option_update
	ConfigOptions []configOption `json:"configOptions,omitempty"`

	// session_info_update
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type planEntry struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority,omitempty"`
}

// chunkContent decodes the content block of a streamed content chunk.
func (u updatePayload) chunkContent() *contentBlock {
	if len(u.Content) == 0 {
		return nil
	}
	var block contentBlock
	if err := json.Unmarshal(u.Content, &block); err != nil {
		return nil
	}
	return &block
}

// toolContentItems decodes the content a tool call produced.
func (u updatePayload) toolContentItems() []toolCallContent {
	if len(u.Content) == 0 {
		return nil
	}
	var items []toolCallContent
	if err := json.Unmarshal(u.Content, &items); err != nil {
		return nil
	}
	return items
}

type toolCallContent struct {
	Type    string        `json:"type"`
	Content *contentBlock `json:"content,omitempty"`
	Path    string        `json:"path,omitempty"`
	OldText string        `json:"oldText,omitempty"`
	NewText string        `json:"newText,omitempty"`
}

type permissionRequest struct {
	SessionID string                    `json:"sessionId"`
	ToolCall  map[string]any            `json:"toolCall"`
	Options   []permissionRequestOption `json:"options"`
}

type permissionRequestOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type permissionResponse struct {
	Outcome permissionOutcome `json:"outcome"`
}

type permissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}
