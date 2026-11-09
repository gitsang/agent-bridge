package pi

import (
	"fmt"
	"strings"

	"github.com/gitsang/agent-bridge/internal/types"
)

// handleDialogRequest records a blocking extension dialog (select, confirm,
// input, editor) as a bridge question. Fire-and-forget methods (notify,
// setStatus, setWidget, setTitle, set_editor_text) need no answer and are
// ignored.
func (c *Agent) handleDialogRequest(rec record) {
	switch rec.Method {
	case "select", "confirm", "input", "editor":
	default:
		return
	}

	// pi sends dialogs without a session id; attribute them to the session with
	// a turn in flight so /question can match them.
	sessionID := c.activeSessionID()

	id := fmt.Sprintf("dialog-%d", c.dialogSeq.Add(1))
	question := types.Question{
		Text:     firstNonEmpty(strings.TrimSpace(rec.Title), strings.TrimSpace(rec.Text), rec.Method),
		Options:  trimOptions(rec.Options),
		Multiple: false,
	}
	if rec.Method == "confirm" {
		question.Options = []string{"yes", "no"}
	}

	c.dialogsMu.Lock()
	c.dialogs[id] = pendingDialog{
		request: types.QuestionRequest{
			ID:        id,
			SessionID: sessionID,
			Questions: []types.Question{question},
		},
		dialogType: rec.Method,
		options:    trimOptions(rec.Options),
	}
	c.dialogsMu.Unlock()
}

func trimOptions(values []string) []string {
	resolved := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			resolved = append(resolved, trimmed)
		}
	}
	return resolved
}
