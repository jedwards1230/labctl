package mcpserver

import "testing"

// TestIsLoopbackAddr pins the loopback-classification rules RequireAuth relies
// on: literal loopback IPs and "localhost" are loopback; a bare port (binds
// every interface) and any other hostname/IP are treated as non-loopback
// (fail closed — no DNS lookups are performed).
func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{":9000", false}, // bare port binds every interface
		{"0.0.0.0:9000", false},
		{"127.0.0.1:9000", true},
		{"127.0.0.2:9000", true}, // whole 127.0.0.0/8 is loopback
		{"[::1]:9000", true},
		{"localhost:9000", true},
		{"LOCALHOST:9000", true}, // case-insensitive
		{"192.168.1.5:9000", false},
		{"example.com:9000", false}, // arbitrary hostname: no DNS lookup, fail closed
		{"not-a-valid-addr", false}, // malformed (no colon): fail closed
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			if got := isLoopbackAddr(tc.addr); got != tc.want {
				t.Errorf("isLoopbackAddr(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}
