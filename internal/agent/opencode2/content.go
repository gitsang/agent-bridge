package opencode2

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gitsang/agent-bridge/internal/types"
)

type extractedContent struct {
	Answer      string
	Reasoning   string
	Tools       string
	Diagnostics string
}

// extractContent renders the assistant message content the bridge asked for.
func extractContent(parts []messagePart, output types.MessageOutputOptions) extractedContent {
	var result extractedContent

	for _, part := range parts {
		switch part.Type {
		case "text":
			if !output.Includes(types.MessageContentAnswer) {
				continue
			}
			if text := strings.TrimSpace(part.Text); text != "" {
				result.Answer += "\n" + text
			}
		case "reasoning":
			if !output.Includes(types.MessageContentReasoning) {
				continue
			}
			if text := strings.TrimSpace(part.Text); text != "" {
				result.Reasoning += "\n" + text
			}
		case "tool":
			if !output.Includes(types.MessageContentActionTool) {
				continue
			}
			result.Tools += renderTool(part)
		}
	}

	result.Answer = strings.TrimSpace(result.Answer)
	result.Reasoning = strings.TrimSpace(result.Reasoning)
	result.Tools = strings.TrimSpace(result.Tools)
	result.Diagnostics = strings.TrimSpace(result.Diagnostics)
	return result
}

func renderTool(part messagePart) string {
	name := strings.TrimSpace(part.Name)
	if name == "" {
		name = "tool"
	}

	builder := strings.Builder{}
	if part.State != nil {
		if input := renderRawJSON(part.State.Input); input != "" {
			fmt.Fprintf(&builder, "\n<tool name=%q type=\"input\">\n%s\n</tool>", name, input)
		}
		output := renderToolContent(part.State.Content)
		if part.State.Error != nil {
			output = firstNonEmpty(output, part.State.Error.Message)
		}
		if output != "" {
			fmt.Fprintf(&builder, "\n<tool name=%q type=\"output\">\n%s\n</tool>", name, output)
		}
	}
	if builder.Len() == 0 {
		fmt.Fprintf(&builder, "\n<tool name=%q />", name)
	}
	return builder.String()
}

func renderToolContent(items []toolContent) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(item.Text); text != "" {
			lines = append(lines, text)
			continue
		}
		if path := strings.TrimSpace(item.Path); path != "" {
			lines = append(lines, path)
		}
	}
	return strings.Join(lines, "\n")
}

func renderRawJSON(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	return trimmed
}
