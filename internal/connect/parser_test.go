package connect

import "testing"

func TestParseInputPlain(t *testing.T) {
	parsed, err := ParseInput("hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Invocation != nil {
		t.Fatalf("invocation should be nil for plain input")
	}
	if got, want := parsed.Content, "hello world"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestParseInputSlashCommand(t *testing.T) {
	parsed, err := ParseInput(`/new --model "openai/gpt-5.4" --agent quick --work-dir '/tmp/demo dir'`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Invocation == nil {
		t.Fatalf("invocation is required for slash commands")
	}
	if got, want := parsed.Invocation.Positionals[0], "new"; got != want {
		t.Fatalf("root command = %q, want %q", got, want)
	}
	if got, want := parsed.Invocation.Flags["model"], "openai/gpt-5.4"; got != want {
		t.Fatalf("model flag = %q, want %q", got, want)
	}
	if got, want := parsed.Invocation.Flags["agent"], "quick"; got != want {
		t.Fatalf("agent flag = %q, want %q", got, want)
	}
	if got, want := parsed.Invocation.Flags["work-dir"], "/tmp/demo dir"; got != want {
		t.Fatalf("work-dir flag = %q, want %q", got, want)
	}
}

func TestParseInputInvalidQuote(t *testing.T) {
	_, err := ParseInput(`/new --work-dir '/tmp/demo`)
	if err == nil {
		t.Fatalf("expected unterminated quote error")
	}
}
