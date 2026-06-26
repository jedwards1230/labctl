package manifest

import (
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

// TestNormalizeSecretsLegacy proves the legacy secret: block is folded into a
// single op provider, and an empty config still yields a default op provider.
func TestNormalizeSecretsLegacy(t *testing.T) {
	t.Run("empty config synthesizes op provider", func(t *testing.T) {
		sc := NormalizeSecrets(Config{})
		p, ok := sc.Providers["onepassword"]
		if !ok {
			t.Fatalf("want an onepassword provider, got %v", sc.Providers)
		}
		if p.Scheme != "op" {
			t.Fatalf("scheme = %q, want op", p.Scheme)
		}
		if strings.Join(p.Command, " ") != "op read {ref}" {
			t.Fatalf("command = %v", p.Command)
		}
	})

	t.Run("legacy block carries its command and env_override", func(t *testing.T) {
		cfg := Config{Secret: SecretResolver{
			Command:     []string{"op", "read", "--no-newline", "{ref}"},
			EnvOverride: true,
		}}
		sc := NormalizeSecrets(cfg)
		if sc.EnvOverride == nil || !*sc.EnvOverride {
			t.Fatalf("env_override should normalize from legacy block")
		}
		p := sc.Providers["onepassword"]
		if strings.Join(p.Command, " ") != "op read --no-newline {ref}" {
			t.Fatalf("command = %v", p.Command)
		}
	})
}

// TestNormalizeSecretsProviders covers the new secrets.providers path, including
// scheme defaulting from the map key and env_override precedence.
func TestNormalizeSecretsProviders(t *testing.T) {
	cfg := Config{
		Secret: SecretResolver{EnvOverride: true}, // should be overridden by Secrets.EnvOverride
		Secrets: SecretsConfig{
			EnvOverride: boolPtr(false),
			Providers: map[string]ProviderConfig{
				"onepassword": {}, // scheme + command default
			},
		},
	}
	sc := NormalizeSecrets(cfg)
	if sc.EnvOverride == nil || *sc.EnvOverride {
		t.Fatalf("Secrets.EnvOverride (false) should win over legacy (true)")
	}
	p := sc.Providers["onepassword"]
	if p.Scheme != "op" {
		t.Fatalf("scheme should default from alias to op, got %q", p.Scheme)
	}
	if strings.Join(p.Command, " ") != "op read {ref}" {
		t.Fatalf("command should default, got %v", p.Command)
	}
}

func TestNormalizeSecretsIdempotent(t *testing.T) {
	cfg := Config{Secret: SecretResolver{Command: []string{"op", "read", "{ref}"}}}
	once := NormalizeSecrets(cfg)
	twice := NormalizeSecrets(Config{Secrets: once})
	if len(twice.Providers) != 1 {
		t.Fatalf("re-normalizing changed provider count: %v", twice.Providers)
	}
	p := twice.Providers["onepassword"]
	if p.Scheme != "op" || strings.Join(p.Command, " ") != "op read {ref}" {
		t.Fatalf("re-normalize not idempotent: %+v", p)
	}
}

func TestValidateConfig(t *testing.T) {
	t.Run("unknown scheme rejected", func(t *testing.T) {
		c := &Config{Secrets: SecretsConfig{Providers: map[string]ProviderConfig{
			"vault": {Scheme: "vault"},
		}}}
		if err := ValidateConfig(c); err == nil {
			t.Fatal("expected error for unknown scheme")
		}
	})

	t.Run("two-source token rejected", func(t *testing.T) {
		c := &Config{Secrets: SecretsConfig{Providers: map[string]ProviderConfig{
			"onepassword": {Scheme: "op", Auth: ProviderAuth{
				ServiceAccountToken: &SecretSource{File: "/x", Env: "Y"},
			}},
		}}}
		err := ValidateConfig(c)
		if err == nil || !strings.Contains(err.Error(), "exactly one of file|value|env") {
			t.Fatalf("expected exactly-one-of error, got %v", err)
		}
	})

	t.Run("valid single-source token accepted", func(t *testing.T) {
		c := &Config{Secrets: SecretsConfig{Providers: map[string]ProviderConfig{
			"onepassword": {Scheme: "op", Auth: ProviderAuth{
				ServiceAccountToken: &SecretSource{File: "/x"},
			}},
		}}}
		if err := ValidateConfig(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
