package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// processTransport runs an ACP agent as a child process and speaks JSONL over
// its stdin/stdout. stdout carries protocol records only; stderr is logged.
//
// The process outlives the context that started it: only Close stops it, so a
// short initialize or prompt deadline cannot kill an otherwise healthy agent.
type processTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	logger *slog.Logger
	outMu  sync.Mutex
	inMu   sync.Mutex
}

func newProcessTransport(_ context.Context, logger *slog.Logger, command string, args []string, env map[string]string, directory string) (*processTransport, error) {
	resolvedCommand := strings.TrimSpace(command)
	if resolvedCommand == "" {
		return nil, fmt.Errorf("acp command is required")
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
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		logger.Debug("acp agent stderr", slog.String("line", line))
	}
}

func (t *processTransport) ReadMessage(ctx context.Context) (json.RawMessage, error) {
	t.outMu.Lock()
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			t.outMu.Unlock()
			return nil, err
		}
	}
	line, err := t.stdout.ReadBytes('\n')
	t.outMu.Unlock()
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return t.ReadMessage(ctx)
	}
	return json.RawMessage(trimmed), nil
}

func (t *processTransport) WriteMessage(ctx context.Context, msg json.RawMessage) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	t.inMu.Lock()
	defer t.inMu.Unlock()
	_, err := t.stdin.Write(append(msg, '\n'))
	return err
}

func (t *processTransport) Close() error {
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
	return nil
}
