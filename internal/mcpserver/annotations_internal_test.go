package mcpserver

import (
	"testing"

	"github.com/jedwards1230/labctl/internal/command"
)

// TestBuildAnnotations asserts the derived MCP annotations for representative
// command shapes: a read GET, write POST/PATCH (additive), write PUT/DELETE
// (destructive + idempotent), and an RPC/pipeline write with no HTTP method.
func TestBuildAnnotations(t *testing.T) {
	tests := []struct {
		name            string
		cmd             *command.Command
		wantReadOnly    bool
		wantDestructive *bool // nil = must be unset
		wantIdempotent  bool
	}{
		{
			name:         "read GET",
			cmd:          &command.Command{Method: "GET", Write: false},
			wantReadOnly: true,
		},
		{
			name:            "write POST",
			cmd:             &command.Command{Method: "POST", Write: true},
			wantReadOnly:    false,
			wantDestructive: boolPtr(false),
			wantIdempotent:  false,
		},
		{
			name:            "write PATCH",
			cmd:             &command.Command{Method: "PATCH", Write: true},
			wantReadOnly:    false,
			wantDestructive: boolPtr(false),
			wantIdempotent:  false,
		},
		{
			name:            "write DELETE",
			cmd:             &command.Command{Method: "DELETE", Write: true},
			wantReadOnly:    false,
			wantDestructive: boolPtr(true),
			wantIdempotent:  true,
		},
		{
			name:            "write PUT",
			cmd:             &command.Command{Method: "PUT", Write: true},
			wantReadOnly:    false,
			wantDestructive: boolPtr(true),
			wantIdempotent:  true,
		},
		{
			name:            "RPC write, no HTTP method",
			cmd:             &command.Command{Method: "pool.create", Write: true},
			wantReadOnly:    false,
			wantDestructive: nil, // can't infer for a non-HTTP write
			wantIdempotent:  false,
		},
		{
			name:            "pipeline write, empty method",
			cmd:             &command.Command{Method: "", Write: true},
			wantReadOnly:    false,
			wantDestructive: nil,
			wantIdempotent:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ann := buildAnnotations("svc", "cmd", tc.cmd)
			if ann == nil {
				t.Fatal("annotations are nil")
			}
			if ann.ReadOnlyHint != tc.wantReadOnly {
				t.Errorf("ReadOnlyHint = %v, want %v", ann.ReadOnlyHint, tc.wantReadOnly)
			}
			// OpenWorldHint is true on every tool (labctl calls external services).
			if ann.OpenWorldHint == nil || !*ann.OpenWorldHint {
				t.Errorf("OpenWorldHint = %v, want true", ann.OpenWorldHint)
			}
			if ann.Title == "" {
				t.Error("Title is empty, want a human-readable label")
			}
			switch {
			case tc.wantDestructive == nil && ann.DestructiveHint != nil:
				t.Errorf("DestructiveHint = %v, want unset", *ann.DestructiveHint)
			case tc.wantDestructive != nil && ann.DestructiveHint == nil:
				t.Errorf("DestructiveHint unset, want %v", *tc.wantDestructive)
			case tc.wantDestructive != nil && *ann.DestructiveHint != *tc.wantDestructive:
				t.Errorf("DestructiveHint = %v, want %v", *ann.DestructiveHint, *tc.wantDestructive)
			}
			if ann.IdempotentHint != tc.wantIdempotent {
				t.Errorf("IdempotentHint = %v, want %v", ann.IdempotentHint, tc.wantIdempotent)
			}
		})
	}
}

// TestToolTitle covers the human title derivation (help clause vs fallback).
func TestToolTitle(t *testing.T) {
	tests := []struct {
		svc, cmd, help, want string
	}{
		{"radarr", "list", "library list (movies), filtered", "Radarr: library list (movies)"},
		{"radarr", "list", "", "radarr list"},
		{"tdarr", "status", "server status", "Tdarr: server status"},
	}
	for _, tc := range tests {
		if got := toolTitle(tc.svc, tc.cmd, tc.help); got != tc.want {
			t.Errorf("toolTitle(%q,%q,%q) = %q, want %q", tc.svc, tc.cmd, tc.help, got, tc.want)
		}
	}
}
