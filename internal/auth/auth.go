// Package auth applies a manifest's credential strategy to an outgoing HTTP
// request. Secrets are interpolated from templates at apply time and go into
// headers (never argv). Phase 1 implements the static strategies; derived-token
// strategies (oauth2-client-credentials, login-flow) and ws-login land in later
// phases.
package auth

import (
	"fmt"
	"net/http"

	"github.com/jedwards1230/labctl/internal/manifest"
	"github.com/jedwards1230/labctl/internal/template"
)

// Applier carries a resolved auth spec and the template env used to expand it.
type Applier struct {
	auth manifest.Auth
	env  template.Env
}

// New builds an Applier. env supplies the secret resolver + vars/args/env.
func New(a manifest.Auth, env template.Env) Applier {
	return Applier{auth: a, env: env}
}

// Apply mutates req to carry the credential. For strategy "none" it is a no-op.
// noAuth (a per-command override) forces the no-op.
func (a Applier) Apply(req *http.Request, noAuth bool) error {
	if noAuth {
		return nil
	}
	switch a.auth.Strategy {
	case "", "none":
		return nil
	case "header-key":
		val, err := a.env.Expand(a.auth.Value)
		if err != nil {
			return err
		}
		req.Header.Set(a.auth.Header, val)
		return nil
	case "bearer":
		val, err := a.env.Expand(a.auth.Value)
		if err != nil {
			return err
		}
		scheme := a.auth.Scheme
		if scheme == "" {
			scheme = "Bearer"
		}
		req.Header.Set("Authorization", scheme+" "+val)
		return nil
	case "basic":
		user, err := a.env.Expand(a.auth.Username)
		if err != nil {
			return err
		}
		pass, err := a.env.Expand(a.auth.Password)
		if err != nil {
			return err
		}
		req.SetBasicAuth(user, pass)
		return nil
	case "oauth2-client-credentials", "login-flow", "ws-login", "external-tool":
		return fmt.Errorf("auth strategy %q is not yet implemented (planned for a later phase)", a.auth.Strategy)
	default:
		return fmt.Errorf("unknown auth strategy %q", a.auth.Strategy)
	}
}
