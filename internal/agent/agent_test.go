package agent

import "testing"

func TestMessageOutputOptionsIncludes(t *testing.T) {
	tests := []struct {
		name    string
		options MessageOutputOptions
		kind    MessageContentKind
		want    bool
	}{
		{
			name:    "empty include allows non empty kind",
			options: MessageOutputOptions{},
			kind:    MessageContentActionTool,
			want:    true,
		},
		{
			name: "exact match",
			options: MessageOutputOptions{
				Include: []MessageContentKind{MessageContentAnswer},
			},
			kind: MessageContentAnswer,
			want: true,
		},
		{
			name: "parent match",
			options: MessageOutputOptions{
				Include: []MessageContentKind{MessageContentAction},
			},
			kind: MessageContentActionTool,
			want: true,
		},
		{
			name: "sibling mismatch",
			options: MessageOutputOptions{
				Include: []MessageContentKind{MessageContentArtifact},
			},
			kind: MessageContentActionTool,
			want: false,
		},
		{
			name:    "empty kind never matches",
			options: MessageOutputOptions{},
			kind:    "",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.options.Includes(tt.kind); got != tt.want {
				t.Fatalf("Includes() = %t, want %t", got, tt.want)
			}
		})
	}
}
