package pi

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gitsang/agent-bridge/internal/types"
)

// handleSessionEvent folds one pi session event into the active turn.
//
// pi separates the low-level run (agent_start/agent_end) from the session-level
// turn (agent_settled), and a run can be followed by retries, compaction
// recovery, or queued messages. The bridge therefore settles a turn on
// agent_settled, and treats message_end as the authoritative message content.
func (c *Agent) handleSessionEvent(rec record) {
	switch rec.Type {
	case "message_end":
		c.handleMessageEnd(rec.Message)
	case "session_info_changed":
		c.handleSessionInfo(rec.Name)
	case "tool_execution_update":
		c.handleToolUpdate(rec, false)
	case "tool_execution_end":
		c.handleToolUpdate(rec, true)
	case "agent_end":
		// The low-level run ended; content itself arrived in message_end.
	case "agent_settled", "turn_start", "agent_start", "turn_end", "message_start",
		"message_update", "queue_update", "compaction_start", "compaction_end":
		// Not needed for the bridge's aggregated replies.
	}
}

func (c *Agent) findSession(sessionID string) *sessionState {
	c.sessionsMu.Lock()
	defer c.sessionsMu.Unlock()
	return c.sessions[strings.TrimSpace(sessionID)]
}

// activeTurn returns the turn currently receiving output for a session.
func (c *Agent) activeTurn(sessionID string) *turnState {
	state := c.findSession(sessionID)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.active
}

// activeSessionID returns the session with a turn in flight, if any.
func (c *Agent) activeSessionID() string {
	c.sessionsMu.Lock()
	defer c.sessionsMu.Unlock()
	for _, state := range c.sessions {
		state.mu.Lock()
		active := state.active != nil
		state.mu.Unlock()
		if active {
			return state.ID
		}
	}
	return ""
}

func (c *Agent) handleMessageEnd(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var message agentMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return
	}

	switch message.Role {
	case "assistant":
		for _, turn := range c.activeTurns() {
			turn.applyAssistantMessage(message)
		}
	case "toolResult":
		for _, turn := range c.activeTurns() {
			turn.applyToolResult(message)
		}
	}
}

// activeTurns returns every turn currently receiving output. pi runs one turn
// per process, so this is normally a single turn; the loop keeps the mapping
// correct when several sessions share the driver.
func (c *Agent) activeTurns() []*turnState {
	c.sessionsMu.Lock()
	states := make([]*sessionState, 0, len(c.sessions))
	for _, state := range c.sessions {
		states = append(states, state)
	}
	c.sessionsMu.Unlock()

	turns := make([]*turnState, 0, len(states))
	for _, state := range states {
		state.mu.Lock()
		if state.active != nil {
			turns = append(turns, state.active)
		}
		state.mu.Unlock()
	}
	return turns
}

func (c *Agent) handleSessionInfo(name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	c.sessionsMu.Lock()
	defer c.sessionsMu.Unlock()
	for _, state := range c.sessions {
		state.Title = strings.TrimSpace(name)
	}
}

func (c *Agent) handleToolUpdate(rec record, finished bool) {
	toolCallID := strings.TrimSpace(rec.ToolCallID)
	if toolCallID == "" {
		return
	}
	name := strings.TrimSpace(rec.ToolName)

	var payload json.RawMessage
	if finished {
		payload = rec.Result
	} else {
		payload = rec.PartialResult
	}
	output := renderToolPayload(payload)

	for _, turn := range c.activeTurns() {
		if finished {
			turn.completeTool(toolCallID, name, output, rec.IsError)
			continue
		}
		turn.startTool(toolCallID, name, renderToolPayload(rec.Args), output)
	}
}

// applyAssistantMessage records the authoritative content of a finished
// assistant message. Streaming deltas are not needed because pi sends the
// completed message, and using one source avoids duplicated text.
func (t *turnState) applyAssistantMessage(message agentMessage) {
	answer, reasoning, tools := splitBlocks(message.blocks(), message.ToolName)

	t.mu.Lock()
	defer t.mu.Unlock()
	if answer != "" {
		t.answer.Reset()
		t.answer.WriteString(answer)
	}
	if reasoning != "" {
		t.reasoning.Reset()
		t.reasoning.WriteString(reasoning)
	}
	for _, tool := range tools {
		if t.tools.Len() > 0 {
			t.tools.WriteString("\n")
		}
		t.tools.WriteString(tool)
	}
	if message.StopReason != "" {
		t.stopReason = message.StopReason
	}
	switch message.StopReason {
	case "error", "aborted":
		detail := strings.TrimSpace(message.ErrorMessage)
		if detail == "" {
			detail = "pi run ended with stop reason " + message.StopReason
		}
		if t.diagnostics.Len() > 0 {
			t.diagnostics.WriteString("\n")
		}
		t.diagnostics.WriteString(detail)
		t.err = fmt.Errorf("%s", detail)
	}
	if provider, model := strings.TrimSpace(message.Provider), strings.TrimSpace(message.Model); provider != "" && model != "" {
		t.Model = types.ModelRef{ProviderID: provider, ModelID: model}
	}
}

func (t *turnState) applyToolResult(message agentMessage) {
	text := renderContentBlocks(message.blocks())
	toolName := strings.TrimSpace(message.ToolName)
	if toolName == "" {
		toolName = "tool"
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tools.Len() > 0 {
		t.tools.WriteString("\n")
	}
	fmt.Fprintf(&t.tools, "<tool name=%q type=\"output\">\n%s\n</tool>", toolName, text)
	if message.IsError && text != "" {
		if t.diagnostics.Len() > 0 {
			t.diagnostics.WriteString("\n")
		}
		t.diagnostics.WriteString(text)
	}
}

func (t *turnState) startTool(callID, name, input, output string) {
	label := firstNonEmpty(name, "tool")
	builder := strings.Builder{}
	if input != "" {
		fmt.Fprintf(&builder, "<tool name=%q type=\"input\">\n%s\n</tool>", label, input)
	}
	if output != "" {
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		fmt.Fprintf(&builder, "<tool name=%q type=\"output\">\n%s\n</tool>", label, output)
	}
	if builder.Len() == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.toolCallIDs == nil {
		t.toolCallIDs = map[string]struct{}{}
	}
	if _, seen := t.toolCallIDs[callID]; seen {
		return
	}
	if t.tools.Len() > 0 {
		t.tools.WriteString("\n")
	}
	t.tools.WriteString(builder.String())
	if output != "" {
		t.toolCallIDs[callID] = struct{}{}
	}
}

func (t *turnState) completeTool(callID, name, output string, isError bool) {
	label := firstNonEmpty(name, "tool")

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.toolCallIDs == nil {
		t.toolCallIDs = map[string]struct{}{}
	}

	if _, seen := t.toolCallIDs[callID]; seen || output == "" {
		// Execution already rendered a partial result; only the diagnostic is
		// new.
		if isError && output != "" {
			if t.diagnostics.Len() > 0 {
				t.diagnostics.WriteString("\n")
			}
			t.diagnostics.WriteString(output)
		}
		return
	}
	t.toolCallIDs[callID] = struct{}{}

	if t.tools.Len() > 0 {
		t.tools.WriteString("\n")
	}
	fmt.Fprintf(&t.tools, "<tool name=%q type=\"output\">\n%s\n</tool>", label, output)
	if isError {
		if t.diagnostics.Len() > 0 {
			t.diagnostics.WriteString("\n")
		}
		t.diagnostics.WriteString(output)
	}
}

// splitBlocks renders assistant content blocks the bridge can present.
func splitBlocks(blocks []contentBlock, fallbackName string) (answer, reasoning string, tools []string) {
	var answers, thoughts []string
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if text := strings.TrimSpace(block.Text); text != "" {
				answers = append(answers, text)
			}
		case "thinking":
			if text := strings.TrimSpace(block.Thinking); text != "" {
				thoughts = append(thoughts, text)
			}
		case "toolCall":
			name := firstNonEmpty(strings.TrimSpace(block.Name), fallbackName, "tool")
			input := strings.TrimSpace(string(block.Arguments))
			tools = append(tools, fmt.Sprintf("<tool name=%q type=\"input\">\n%s\n</tool>", name, input))
		}
	}
	return strings.Join(answers, "\n\n"), strings.Join(thoughts, "\n\n"), tools
}

func renderContentBlocks(blocks []contentBlock) string {
	lines := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if text := strings.TrimSpace(block.Text); text != "" {
			lines = append(lines, text)
		}
	}
	return strings.Join(lines, "\n")
}

// renderToolPayload renders a tool args or result payload. pi tool results are
// {content: [...], details: ...}; args are arbitrary JSON.
func renderToolPayload(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var payload struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil && len(payload.Content) > 0 {
		var blocks []contentBlock
		if err := json.Unmarshal(payload.Content, &blocks); err == nil && len(blocks) > 0 {
			return renderContentBlocks(blocks)
		}
	}
	return strings.TrimSpace(string(raw))
}

// Turn helpers

func (t *turnState) finish(stopReason string, err error) {
	t.mu.Lock()
	t.completedAt = float64(time.Now().UnixMilli()) / 1000
	if stopReason != "" {
		t.stopReason = stopReason
	}
	if err != nil {
		t.err = err
	}
	t.mu.Unlock()
	t.doneOnce.Do(func() { close(t.done) })
}

func (t *turnState) currentAnswer() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(t.answer.String())
}

func (t *turnState) currentCompletedAt() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.completedAt
}

func (t *turnState) currentError() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *turnState) currentStopReason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopReason
}

func (t *turnState) snapshot() (answer, reasoning, tools, patches, diagnostics string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(t.answer.String()),
		strings.TrimSpace(t.reasoning.String()),
		strings.TrimSpace(t.tools.String()),
		strings.TrimSpace(t.patches.String()),
		strings.TrimSpace(t.diagnostics.String())
}
