package agent

import (
	"testing"

	"github.com/gitsang/agent-bridge/internal/types"
)

func TestMessageOutputOptionsIncludes(t *testing.T) {
	tests := []struct {
		name    string
		options types.MessageOutputOptions
		kind    types.MessageContentKind
		want    bool
	}{
		{
			name:    "empty include allows non empty kind",
			options: types.MessageOutputOptions{},
			kind:    types.MessageContentActionTool,
			want:    true,
		},
		{
			name: "exact match",
			options: types.MessageOutputOptions{
				Include: []types.MessageContentKind{types.MessageContentAnswer},
			},
			kind: types.MessageContentAnswer,
			want: true,
		},
		{
			name: "parent match",
			options: types.MessageOutputOptions{
				Include: []types.MessageContentKind{types.MessageContentAction},
			},
			kind: types.MessageContentActionTool,
			want: true,
		},
		{
			name: "sibling mismatch",
			options: types.MessageOutputOptions{
				Include: []types.MessageContentKind{types.MessageContentArtifact},
			},
			kind: types.MessageContentActionTool,
			want: false,
		},
		{
			name:    "empty kind never matches",
			options: types.MessageOutputOptions{},
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
