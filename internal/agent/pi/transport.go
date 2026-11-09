package pi

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// pi reserves stdout for JSONL protocol records and writes diagnostics to
// stderr. Records are split on LF only: JSON strings may legally contain
// U+2028/U+2029, so a Unicode-aware line reader would corrupt them.

// record is one stdout record. All record families share a "type" field, so the
// fields of every variant live on one struct and are ignored when absent.
type record struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Command string          `json:"command,omitempty"`
	Success bool            `json:"success,omitempty"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`

	// Session events.
	Message        json.RawMessage `json:"message,omitempty"`
	Messages       []agentMessage  `json:"messages,omitempty"`
	AssistantEvent *assistantEvent `json:"assistantMessageEvent,omitempty"`
	ToolCallID     string          `json:"toolCallId,omitempty"`
	ToolName       string          `json:"toolName,omitempty"`
	Args           json.RawMessage `json:"args,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	PartialResult  json.RawMessage `json:"partialResult,omitempty"`
	IsError        bool            `json:"isError,omitempty"`
	Aborted        bool            `json:"aborted,omitempty"`
	Name           string          `json:"name,omitempty"`

	// Extension UI requests.
	Method    string   `json:"method,omitempty"`
	Title     string   `json:"title,omitempty"`
	Text      string   `json:"text,omitempty"`
	Options   []string `json:"options,omitempty"`
	TimeoutMS int64    `json:"timeout,omitempty"`
}

type assistantEvent struct {
	Type         string          `json:"type"`
	ContentIndex int             `json:"contentIndex,omitempty"`
	Delta        string          `json:"delta,omitempty"`
	Content      string          `json:"content,omitempty"`
	ToolCall     json.RawMessage `json:"toolCall,omitempty"`
	Reason       string          `json:"reason,omitempty"`
}

// agentMessage is a pi AgentMessage, reduced to the fields the bridge maps.
type agentMessage struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content,omitempty"`
	Provider     string          `json:"provider,omitempty"`
	Model        string          `json:"model,omitempty"`
	StopReason   string          `json:"stopReason,omitempty"`
	ErrorMessage string          `json:"errorMessage,omitempty"`
	Timestamp    int64           `json:"timestamp,omitempty"`
	ToolCallID   string          `json:"toolCallId,omitempty"`
	ToolName     string          `json:"toolName,omitempty"`
	IsError      bool            `json:"isError,omitempty"`
}

// contentBlock is one entry of a message content array.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// blocks decodes a content field that is either a plain string or a block list.
func (m agentMessage) blocks() []contentBlock {
	if len(m.Content) == 0 {
		return nil
	}
	var text string
	if err := json.Unmarshal(m.Content, &text); err == nil {
		return []contentBlock{{Type: "text", Text: text}}
	}
	var blocks []contentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil
	}
	return blocks
}

// processTransport runs `pi --mode rpc` and exchanges JSONL records with it.
type processTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	logger *slog.Logger

	outMu sync.Mutex
	inMu  sync.Mutex
}

func newProcessTransport(logger *slog.Logger, command string, args []string, env map[string]string, directory string) (*processTransport, error) {
	resolvedCommand := strings.TrimSpace(command)
	if resolvedCommand == "" {
		return nil, errors.New("pi command is required")
	}

	cmd := exec.Command(resolvedCommand, args...)
	if directory = strings.TrimSpace(directory); directory != "" {
		cmd.Dir = directory
	}
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, value))
		}
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	go logStderr(logger, stderr)

	return &processTransport{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 1<<20), logger: logger}, nil
}

func logStderr(logger *slog.Logger, stderr io.Reader) {
	reader := bufio.NewReader(stderr)
	for {
		line, err := reader.ReadString('\n')
		if text := strings.TrimSpace(line); text != "" {
			logger.Debug("pi stderr", slog.String("line", text))
		}
		if err != nil {
			return
		}
	}
}

// read returns the next record, blocking until one is available.
func (t *processTransport) read() (record, error) {
	t.outMu.Lock()
	defer t.outMu.Unlock()
	for {
		line, err := t.stdout.ReadBytes('\n')
		if err != nil {
			return record{}, err
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			continue
		}
		var decoded record
		if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
			// A non-JSON line is a protocol violation, not a record.
			return record{}, fmt.Errorf("pi wrote a non-JSON line: %s", trimmed)
		}
		return decoded, nil
	}
}

func (t *processTransport) write(payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	t.inMu.Lock()
	defer t.inMu.Unlock()
	_, err = t.stdin.Write(append(encoded, '\n'))
	return err
}

func (t *processTransport) close() error {
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
