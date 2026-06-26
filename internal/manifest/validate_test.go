package manifest

import "testing"

func TestValidateConfig_ExactlyOneSource(t *testing.T) {
	cases := []struct {
		name    string
		src     SecretEnvSource
		wantErr bool
	}{
		{"file-only", SecretEnvSource{File: "/etc/token"}, false},
		{"value-only", SecretEnvSource{Value: "literal"}, false},
		{"env-only", SecretEnvSource{Env: "SRC_VAR"}, false},
		{"none", SecretEnvSource{}, true},
		{"file+value", SecretEnvSource{File: "/etc/token", Value: "literal"}, true},
		{"all-three", SecretEnvSource{File: "/etc/token", Value: "literal", Env: "SRC_VAR"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Secret: SecretResolver{Env: map[string]SecretEnvSource{"OP_SERVICE_ACCOUNT_TOKEN": tc.src}}}
			err := ValidateConfig(c)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %+v", tc.src)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %+v: %v", tc.src, err)
			}
		})
	}
}

func TestValidateConfig_EmptyEnvOK(t *testing.T) {
	if err := ValidateConfig(&Config{}); err != nil {
		t.Fatalf("empty config should validate: %v", err)
	}
}
