package opencode2

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/types"
)

func TestSmokeRealOpenCode2(t *testing.T) {
	baseURL := os.Getenv("OPENCODE2_SMOKE_URL")
	if baseURL == "" {
		t.Skip("set OPENCODE2_SMOKE_URL and OPENCODE2_SMOKE_PASSWORD to run")
	}
	directory := os.Getenv("OPENCODE2_SMOKE_DIR")
	client := NewClient(baseURL,
		WithAuthentication("opencode", os.Getenv("OPENCODE2_SMOKE_PASSWORD")),
		WithTimeout(3*time.Minute),
	)
	ctx := context.Background()

	models, err := client.ListModels(ctx, directory)
	fmt.Printf("models=%d err=%v\n", len(models), err)
	if len(models) > 0 {
		fmt.Printf("  first=%s name=%s\n", models[0], models[0].ModelName)
	}
	agents, err := client.ListAgents(ctx, directory)
	fmt.Printf("agents=%d err=%v\n", len(agents), err)

	session, err := client.CreateSession(ctx, types.CreateSessionRequest{Directory: directory, Title: "bridge smoke"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	fmt.Printf("session=%s dir=%s\n", session.ID, session.Directory)

	model := types.ModelRef{ProviderID: os.Getenv("OPENCODE2_SMOKE_PROVIDER"), ModelID: os.Getenv("OPENCODE2_SMOKE_MODEL")}
	handle, err := client.Prompt(ctx, session.ID, "Reply with exactly: pong", types.PromptWithModel(model))
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
		fmt.Printf("  %q\n", message.Content)
	}
	latest, err := client.GetLatestAssistantMessage(ctx, session.ID)
	fmt.Printf("latest=%+v err=%v\n", latest, err)
}
