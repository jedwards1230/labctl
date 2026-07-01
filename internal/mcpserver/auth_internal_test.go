package mcpserver

import "testing"

// TestIsLoopbackAddr pins the loopback-classification rules RequireAuth relies
// on: literal loopback IPs and "localhost" are loopback; a bare port (binds
// every interface), any other hostname/IP, and any malformed address
// (net.SplitHostPort failure) are treated as non-loopback (fail closed — no
// DNS lookups are performed, and a malformed address is never given the
// benefit of the doubt).
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
		// Regression: a malformed address that LOOKS like a loopback IP/host
		// with the port typo'd off must NOT be classified as loopback — that
		// would silently skip the auth requirement for exactly the operator
		// mistake RequireAuth exists to catch.
		{"127.0.0.1", false}, // missing port
		{"localhost", false}, // missing port
		{"[::1]", false},     // missing port, malformed bracket form
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			if got := isLoopbackAddr(tc.addr); got != tc.want {
				t.Errorf("isLoopbackAddr(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}
