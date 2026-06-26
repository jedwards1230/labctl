package secret

import (
	"fmt"
	"os"
	"strings"

	"github.com/jedwards1230/labctl/internal/manifest"
)

// opAuth lazily resolves a 1Password service-account token from its configured
// source. A nil source means "no token" — the provider inherits the ambient op
// session (personal/desktop) instead. The token is injected only into the
// provider subprocess; opAuth never calls os.Setenv.
type opAuth struct {
	src *manifest.SecretSource
}

// token resolves the service-account token. Called only on the real-exec path,
// so a misconfigured token never trips a dry-run. Returns:
//   - ("", nil)     when no source is configured (inherit ambient session)
//   - *ConfigError  when the source is not exactly one of file|value|env
//   - *AuthError    when a configured source yields no usable token
func (a opAuth) token() (string, error) {
	if a.src == nil {
		return "", nil
	}
	s := *a.src
	n := 0
	if s.File != "" {
		n++
	}
	if s.Value != "" {
		n++
	}
	if s.Env != "" {
		n++
	}
	if n != 1 {
		return "", &ConfigError{Err: fmt.Errorf("service_account_token: set exactly one of file|value|env (found %d)", n)}
	}
	switch {
	case s.Value != "":
		return s.Value, nil
	case s.Env != "":
		v := os.Getenv(s.Env)
		if v == "" {
			return "", &AuthError{Err: fmt.Errorf("service_account_token: env %s is empty", s.Env)}
		}
		return v, nil
	default: // s.File
		path, err := expandHome(s.File)
		if err != nil {
			return "", &AuthError{Err: fmt.Errorf("service_account_token: %w", err)}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", &AuthError{Err: fmt.Errorf("service_account_token: read %s: %w", path, err)}
		}
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			return "", &AuthError{Err: fmt.Errorf("service_account_token: file %s is empty", path)}
		}
		return tok, nil
	}
}

// expandHome expands a leading ~ and $HOME/${HOME} to the user's home directory.
func expandHome(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if !strings.HasPrefix(path, "~") && !strings.Contains(path, "$HOME") && !strings.Contains(path, "${HOME}") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch {
	case path == "~":
		path = home
	case strings.HasPrefix(path, "~/"):
		path = home + path[1:]
	}
	path = strings.ReplaceAll(path, "${HOME}", home)
	path = strings.ReplaceAll(path, "$HOME", home)
	return path, nil
}
