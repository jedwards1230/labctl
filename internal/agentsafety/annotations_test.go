package agentsafety

import "testing"

// TestHints pins the read-only/destructive/idempotent policy for each command
// shape: read GET, additive POST/PATCH, destructive PUT/DELETE, and a non-HTTP
// write (jsonrpc call / pipeline) that stays unguessed.
func TestHints(t *testing.T) {
	tests := []struct {
		name            string
		write           bool
		method          string
		wantReadOnly    bool
		wantDestructive *bool // nil = must be unset
		wantIdempotent  bool
	}{
		{"read GET", false, "GET", true, nil, false},
		{"write POST", true, "POST", false, ptr(false), false},
		{"write PATCH", true, "PATCH", false, ptr(false), false},
		{"write DELETE", true, "DELETE", false, ptr(true), true},
		{"write PUT", true, "PUT", false, ptr(true), true},
		{"write lowercase delete", true, "delete", false, ptr(true), true},
		{"rpc write, no HTTP method", true, "pool.create", false, nil, false},
		{"pipeline write, empty method", true, "", false, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := Hints(tc.write, tc.method)
			if h.ReadOnly != tc.wantReadOnly {
				t.Errorf("ReadOnly = %v, want %v", h.ReadOnly, tc.wantReadOnly)
			}
			if h.Idempotent != tc.wantIdempotent {
				t.Errorf("Idempotent = %v, want %v", h.Idempotent, tc.wantIdempotent)
			}
			switch {
			case tc.wantDestructive == nil && h.Destructive != nil:
				t.Errorf("Destructive = %v, want unset", *h.Destructive)
			case tc.wantDestructive != nil && h.Destructive == nil:
				t.Errorf("Destructive unset, want %v", *tc.wantDestructive)
			case tc.wantDestructive != nil && *h.Destructive != *tc.wantDestructive:
				t.Errorf("Destructive = %v, want %v", *h.Destructive, *tc.wantDestructive)
			}
		})
	}
}

func ptr(b bool) *bool { return &b }
