package pi

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/types"
)

func TestSmokeRealPi(t *testing.T) {
	if os.Getenv("PI_SMOKE") == "" {
		t.Skip("set PI_SMOKE=1 to run")
	}
	client := NewClient(WithTimeout(3 * time.Minute))
	ctx := context.Background()

	models, err := client.ListModels(ctx, "")
	fmt.Printf("models=%d err=%v\n", len(models), err)
	if len(models) > 0 {
		fmt.Printf("  first=%s name=%s\n", models[0], models[0].ModelName)
	}

	session, err := client.CreateSession(ctx, types.CreateSessionRequest{Directory: "/tmp", Title: "bridge smoke"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	fmt.Printf("session=%s title=%s\n", session.ID, session.Title)

	handle, err := client.Prompt(ctx, session.ID, "Reply with exactly: pong")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	select {
	case <-handle.Done():
	case err := <-handle.Err():
		fmt.Printf("prompt err: %v\n", err)
	case <-time.After(3 * time.Minute):
		t.Fatal("timeout")
	}

	messages, err := client.PollMessagesAfter(ctx, session.ID, 0, types.MessageOutputOptions{})
	fmt.Printf("messages=%d err=%v\n", len(messages), err)
	for _, message := range messages {
		fmt.Printf("  answer=%q tools=%q\n", message.Content, message.Tools)
	}
}
