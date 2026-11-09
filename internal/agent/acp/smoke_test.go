package acp

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/types"
)

func TestSmokeRealACP(t *testing.T) {
	if os.Getenv("ACP_SMOKE") == "" {
		t.Skip("set ACP_SMOKE=1 to run")
	}
	client := NewClient(
		WithCommand("dsh", "--profile", "acp"),
		WithDirectory(os.Getenv("ACP_SMOKE_DIR")),
		WithTimeout(2*time.Minute),
	)
	ctx := context.Background()
	models, err := client.ListModels(ctx, os.Getenv("ACP_SMOKE_DIR"))
	fmt.Printf("models=%d err=%v\n", len(models), err)
	for _, m := range models {
		fmt.Printf("  model %s (%s)\n", m, m.ModelName)
	}
	session, err := client.CreateSession(ctx, types.CreateSessionRequest{Directory: os.Getenv("ACP_SMOKE_DIR")})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	fmt.Printf("session=%s\n", session.ID)
	handle, err := client.Prompt(ctx, session.ID, "Reply with exactly: pong",
		types.PromptWithModel(types.ModelRef{ProviderID: "saigw", ModelID: "deepseek-flash"}))
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	select {
	case <-handle.Done():
		fmt.Println("done")
	case err := <-handle.Err():
		fmt.Printf("prompt err: %v\n", err)
	case <-time.After(2 * time.Minute):
		t.Fatal("timeout")
	}
	msgs, err := client.PollMessagesAfter(ctx, session.ID, 0, types.MessageOutputOptions{})
	fmt.Printf("messages=%d err=%v\n", len(msgs), err)
	for _, m := range msgs {
		fmt.Printf("  %q\n", m.Content)
	}
}
