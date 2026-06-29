package manifest

import (
	"errors"
	"testing"
)

// TestValidateWrapsConfigError proves a structural validation failure is wrapped
// in *ConfigError so callers classify it to the usage exit code (2).
func TestValidateWrapsConfigError(t *testing.T) {
	// An unknown transport — a structural error (missing base_url is now a
	// completeness concern checked by ValidateComplete, not Validate).
	err := Validate(&Service{Name: "x", Transport: "carrier-pigeon"})
	if err == nil {
		t.Fatal("expected a validation error for an unknown transport")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("validation failure should be a *ConfigError, got %T: %v", err, err)
	}
}

// TestValidateSteps covers pipeline (composed-command) step validation: endpoint
// references, path-or-endpoint presence, and jq parseability (incl. recursive
// on_error), each wrapped as *ConfigError so it classifies to exit 2.
func TestValidateSteps(t *testing.T) {
	withStep := func(s Step) *Service {
		return &Service{
			Name:     "x",
			Commands: map[string]Command{"go": {Steps: []Step{s}}},
		}
	}
	tests := []struct {
		name    string
		svc     *Service
		wantErr bool
	}{
		{"valid step", withStep(Step{ID: "a", Path: "/v1/thing", Extract: map[string]string{"id": ".id"}}), false},
		{"unknown endpoint", withStep(Step{ID: "a", Endpoint: "nope"}), true},
		{"no path or endpoint", withStep(Step{ID: "a"}), true},
		{"bad jq extract", withStep(Step{ID: "a", Path: "/x", Extract: map[string]string{"v": "{"}}), true},
		{"bad jq when", withStep(Step{ID: "a", Path: "/x", When: "{"}), true},
		{"bad jq body_transform", withStep(Step{ID: "a", Path: "/x", BodyTransform: "{"}), true},
		{"bad jq in on_error", withStep(Step{ID: "a", Path: "/x", OnError: &Step{ID: "h", Path: "/y", When: "{"}}), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.svc)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected a validation error")
				}
				var cfgErr *ConfigError
				if !errors.As(err, &cfgErr) {
					t.Fatalf("step validation failure should be a *ConfigError, got %T: %v", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestValidateJSONRPCParams proves a jsonrpc-ws command's params must be a JSON
// array, but template tokens (valid per the grammar yet not valid JSON) are
// tolerated so [{arg.0}] passes while a non-array fails.
func TestValidateJSONRPCParams(t *testing.T) {
	mk := func(params string) *Service {
		return &Service{
			Name:      "x",
			Transport: "jsonrpc-ws",
			Commands:  map[string]Command{"go": {Method: "core.ping", Params: params}},
		}
	}
	tests := []struct {
		name    string
		params  string
		wantErr bool
	}{
		{"empty array", `[]`, false},
		{"quoted template", `["{arg.0}"]`, false},
		{"unquoted template", `[{arg.0}]`, false},
		{"object not array", `{"a":1}`, true},
		{"not an array", `{not an array}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(mk(tc.params))
			if (err != nil) != tc.wantErr {
				t.Fatalf("params=%q: wantErr=%v, got %v", tc.params, tc.wantErr, err)
			}
			if tc.wantErr {
				var cfgErr *ConfigError
				if !errors.As(err, &cfgErr) {
					t.Fatalf("want *ConfigError, got %T: %v", err, err)
				}
			}
		})
	}
}
