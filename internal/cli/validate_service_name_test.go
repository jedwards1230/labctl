package cli

import "testing"

// TestValidateServiceName exercises validateServiceName directly — the
// traversal-safety invariant that was previously only covered indirectly
// through catalog edit/vendor end-to-end tests.
func TestValidateServiceName(t *testing.T) {
	valid := []string{"radarr", "n8n", "ts", "my-service", "svc_1", "a", "abc123"}
	for _, name := range valid {
		t.Run("valid/"+name, func(t *testing.T) {
			if err := validateServiceName(name); err != nil {
				t.Errorf("validateServiceName(%q) = %v, want nil", name, err)
			}
		})
	}

	invalid := []string{"..", "a/b", "", ".", "-foo", "Foo", "/etc/passwd", "../../etc/passwd", "foo bar", "FOO"}
	for _, name := range invalid {
		t.Run("invalid/"+name, func(t *testing.T) {
			if err := validateServiceName(name); err == nil {
				t.Errorf("validateServiceName(%q) = nil, want an error", name)
			}
		})
	}
}
