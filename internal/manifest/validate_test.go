package manifest

import (
	"errors"
	"testing"
)

// TestValidateWrapsConfigError proves a structural validation failure is wrapped
// in *ConfigError so callers classify it to the usage exit code (2).
func TestValidateWrapsConfigError(t *testing.T) {
	// Missing base_url and endpoints — a structural error.
	err := Validate(&Service{Name: "x"})
	if err == nil {
		t.Fatal("expected a validation error for a service with no base_url")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("validation failure should be a *ConfigError, got %T: %v", err, err)
	}
}
