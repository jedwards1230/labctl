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

// TestValidateOAuth2FieldAliases proves the oauth2 strategy validates whichever
// of the new (token_url/client_id/client_secret) or legacy (value/username/
// password) pair is set — both forms pass, and the helpers prefer the new field
// when present (back-compat).
func TestValidateOAuth2FieldAliases(t *testing.T) {
	mk := func(a Auth) *Service {
		return &Service{Name: "x", Auth: a}
	}
	cases := []struct {
		name string
		auth Auth
		ok   bool
	}{
		{
			name: "new fields",
			auth: Auth{Strategy: "oauth2-client-credentials", TokenURL: "https://idp/token", ClientID: "cid", ClientSecret: "sec"},
			ok:   true,
		},
		{
			name: "legacy fields",
			auth: Auth{Strategy: "oauth2-client-credentials", Value: "https://idp/token", Username: "cid", Password: "sec"},
			ok:   true,
		},
		{
			name: "missing token_url",
			auth: Auth{Strategy: "oauth2-client-credentials", ClientID: "cid", ClientSecret: "sec"},
			ok:   false,
		},
		{
			name: "missing client_secret",
			auth: Auth{Strategy: "oauth2-client-credentials", TokenURL: "https://idp/token", ClientID: "cid"},
			ok:   false,
		},
		{
			// Mixed form: token_url from the new field, client_id from the legacy
			// field — each pair resolves via its own fallback, so it's valid.
			name: "mixed valid: new token_url + legacy username/password",
			auth: Auth{Strategy: "oauth2-client-credentials", TokenURL: "https://idp/token", Username: "cid", Password: "sec"},
			ok:   true,
		},
		{
			// Mixed form with a hole: new token_url + legacy username, but neither
			// client_secret nor password set — must fail (secret half is empty).
			name: "mixed invalid: new token_url + legacy username, no secret",
			auth: Auth{Strategy: "oauth2-client-credentials", TokenURL: "https://idp/token", Username: "cid"},
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(mk(tc.auth))
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
		})
	}

	// Helper precedence: the new field wins when both are set.
	a := Auth{TokenURL: "new-url", Value: "old-url", ClientID: "new-id", Username: "old-id", ClientSecret: "new-sec", Password: "old-sec"}
	if got := a.OAuth2TokenURL(); got != "new-url" {
		t.Errorf("OAuth2TokenURL() = %q, want new-url", got)
	}
	if got := a.OAuth2ClientID(); got != "new-id" {
		t.Errorf("OAuth2ClientID() = %q, want new-id", got)
	}
	if got := a.OAuth2ClientSecret(); got != "new-sec" {
		t.Errorf("OAuth2ClientSecret() = %q, want new-sec", got)
	}
	// Fallback to the legacy field when the new one is empty.
	legacy := Auth{Value: "old-url", Username: "old-id", Password: "old-sec"}
	if got := legacy.OAuth2TokenURL(); got != "old-url" {
		t.Errorf("OAuth2TokenURL() fallback = %q, want old-url", got)
	}
	if got := legacy.OAuth2ClientID(); got != "old-id" {
		t.Errorf("OAuth2ClientID() fallback = %q, want old-id", got)
	}
	if got := legacy.OAuth2ClientSecret(); got != "old-sec" {
		t.Errorf("OAuth2ClientSecret() fallback = %q, want old-sec", got)
	}

	// Secret-ref scan covers the NEW alias fields too: a {secret.X} in client_id
	// must be caught at lint time when X is undeclared (proves the clean names get
	// the same coverage as value/username/password).
	undeclared := &Service{
		Name:    "x",
		Auth:    Auth{Strategy: "oauth2-client-credentials", TokenURL: "https://idp/token", ClientID: "{secret.cid}", ClientSecret: "sec"},
		Secrets: map[string]Secret{},
	}
	if err := Validate(undeclared); err == nil {
		t.Error("Validate() = nil, want an error: {secret.cid} in client_id is undeclared")
	}
	declared := &Service{
		Name:    "x",
		Auth:    Auth{Strategy: "oauth2-client-credentials", TokenURL: "https://idp/token", ClientID: "{secret.cid}", ClientSecret: "sec"},
		Secrets: map[string]Secret{"cid": {Env: "X_CID"}},
	}
	if err := Validate(declared); err != nil {
		t.Errorf("Validate() = %v, want nil: {secret.cid} in client_id is declared", err)
	}
}

// TestValidateAuthParamsSecretRefs proves the {secret.X} scan covers ws-login
// auth.params (e.g. truenas: params: ["{secret.api_key}"]) — a declared ref
// passes, an undeclared one fails lint instead of only blowing up at runtime.
func TestValidateAuthParamsSecretRefs(t *testing.T) {
	mk := func(params []string, secrets map[string]Secret) *Service {
		return &Service{
			Name:      "x",
			Transport: "jsonrpc-ws",
			Auth:      Auth{Strategy: "ws-login", Method: "auth.login_with_api_key", Params: params},
			Secrets:   secrets,
		}
	}
	// Declared secret → passes.
	if err := Validate(mk([]string{"{secret.api_key}"}, map[string]Secret{"api_key": {Env: "X_API_KEY"}})); err != nil {
		t.Fatalf("Validate() with a declared params secret = %v, want nil", err)
	}
	// Undeclared secret → fails.
	if err := Validate(mk([]string{"{secret.api_key}"}, map[string]Secret{})); err == nil {
		t.Fatal("Validate() with an undeclared params secret = nil, want an error")
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

// TestValidateUI covers the optional ui: hint block (Phase 2): a portable
// manifest with a valid block (or none at all) passes, a bad view/sort.dir
// value is rejected, and an arbitrary drilldown string is accepted leniently
// (labctl has no warning channel, so a forward/cross reference is not a hard
// failure).
func TestValidateUI(t *testing.T) {
	mk := func(ui UI) *Service {
		return &Service{
			Name:     "x",
			Commands: map[string]Command{"list": {Method: "GET", Path: "/list", UI: ui}},
		}
	}
	tests := []struct {
		name    string
		ui      UI
		wantErr bool
	}{
		{"no ui block (zero value)", UI{}, false},
		{"view table", UI{View: "table"}, false},
		{"view record", UI{View: "record"}, false},
		{"view tree", UI{View: "tree"}, false},
		{"full hints", UI{
			View:      "table",
			Columns:   []string{"id", "name"},
			Primary:   "name",
			Badges:    map[string]string{"monitored": "bool"},
			Sort:      &UISort{By: "name", Dir: "asc"},
			Drilldown: "get_by_id",
		}, false},
		{"bad view", UI{View: "chart"}, true},
		{"bad sort dir", UI{Sort: &UISort{By: "name", Dir: "sideways"}}, true},
		{"sort with empty dir is fine (no override)", UI{Sort: &UISort{By: "name"}}, false},
		{"drilldown naming a not-yet-declared/cross-service command is lenient", UI{Drilldown: "whatever_anything"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(mk(tc.ui))
			if (err != nil) != tc.wantErr {
				t.Fatalf("ui=%+v: wantErr=%v, got %v", tc.ui, tc.wantErr, err)
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

// TestValidateUIPortableNoInManifestBindingUnaffected proves the ui: block
// carries no base_url/secret-ref portability concern: a manifest with an
// in-manifest base_url is rejected for THAT reason regardless of ui:, and a
// manifest with ui: but no base_url/secret-ref passes the binding check
// cleanly — ui: never trips validateNoInManifestBinding.
func TestValidateUIPortableNoInManifestBindingUnaffected(t *testing.T) {
	portable := &Service{
		Name: "x",
		Commands: map[string]Command{
			"list": {Method: "GET", Path: "/list", UI: UI{View: "table", Columns: []string{"id"}}},
		},
	}
	if err := Validate(portable); err != nil {
		t.Fatalf("portable manifest with ui: block should validate clean, got %v", err)
	}
}
