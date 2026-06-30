// Package manifest defines the on-disk configuration model — a global
// config.yaml plus one services/<name>.yaml per service — and loads it from the
// XDG config dirs. The binary knows nothing service-specific; every service is a
// manifest. See docs: the PRD/plan in jedwards1230/home-orchestration.
package manifest

// Config is the global config.yaml. Every field has a sane zero-value default,
// so an absent config.yaml is valid.
type Config struct {
	Version  int            `yaml:"version"`
	Defaults Defaults       `yaml:"defaults"`
	Secret   SecretResolver `yaml:"secret"`  // legacy single-resolver block (permanent alias)
	Secrets  SecretsConfig  `yaml:"secrets"` // scheme-dispatched providers (supersedes Secret when set)
}

// Defaults are global fallbacks applied to a service when it leaves a field unset.
type Defaults struct {
	Timeout string `yaml:"timeout"` // Go duration string, e.g. "60s"
	Output  string `yaml:"output"`  // json | raw | scalar (table deferred)
	// MaxResponseBytes bounds an HTTP response body (bytes). 0 = use the built-in
	// default (64 MiB). Guards against a hostile/broken endpoint exhausting memory.
	MaxResponseBytes int64 `yaml:"max_response_bytes"`
}

// SecretResolver describes the external tool that turns a ref into a value.
// Default: `op read {ref}`. This is the legacy single-resolver block; it is kept
// as a permanent alias and normalized into an equivalent op provider (see
// SecretsConfig / NormalizeSecrets).
type SecretResolver struct {
	Resolver    string   `yaml:"resolver"`     // label only ("op")
	Command     []string `yaml:"command"`      // argv; {ref} is substituted
	EnvOverride bool     `yaml:"env_override"` // allow <PREFIX>_<FIELD> env to skip the resolver
}

// SecretsConfig is the scheme-dispatched secrets configuration. When it declares
// providers they supersede the legacy `secret:` block; otherwise that block is
// normalized into a single op provider (see NormalizeSecrets). A ref is routed to
// a provider by its URI scheme (op:// → the op provider).
type SecretsConfig struct {
	// EnvOverride allows <PREFIX>_<SECRET> env vars to skip resolution. Pointer so
	// "unset" (nil) can fall back to the legacy secret.env_override.
	EnvOverride *bool `yaml:"env_override"`
	// Providers are keyed by name; the map key supplies a default Scheme.
	Providers map[string]ProviderConfig `yaml:"providers"`
}

// ProviderConfig configures one secrets provider.
type ProviderConfig struct {
	Scheme  string       `yaml:"scheme"`  // URI scheme handled (op); defaults from the map key alias
	Command []string     `yaml:"command"` // argv; {ref} substituted (op default: op read {ref})
	Auth    ProviderAuth `yaml:"auth"`    // optional credentials for the backing tool
}

// ProviderAuth holds optional credentials a provider injects into its backing
// tool's subprocess environment — never into argv, never globally exported.
type ProviderAuth struct {
	ServiceAccountToken *SecretSource `yaml:"service_account_token"` // op service-account token (OP_SERVICE_ACCOUNT_TOKEN)
}

// SecretSource is exactly one of file|value|env — where a token value comes from.
type SecretSource struct {
	File  string `yaml:"file"`  // path to a file holding the token (~ and $HOME expanded)
	Value string `yaml:"value"` // literal token (discouraged; prefer file/env)
	Env   string `yaml:"env"`   // env var holding the token
}

// Service is one services/<name>.yaml manifest.
type Service struct {
	Name        string              `yaml:"name"` // selector; defaults to filename stem
	Description string              `yaml:"description"`
	BaseURL     string              `yaml:"base_url"`
	EnvPrefix   string              `yaml:"env_prefix"`
	Transport   string              `yaml:"transport"`    // http (default) | jsonrpc-ws
	TLSInsecure bool                `yaml:"tls_insecure"` // curl -k equivalent
	Vars        map[string]string   `yaml:"vars"`         // template vars (host, …), env-overridable
	Endpoints   map[string]Endpoint `yaml:"endpoints"`    // multi-endpoint services
	Auth        Auth                `yaml:"auth"`
	Secrets     map[string]Secret   `yaml:"secrets"`
	Spec        string              `yaml:"spec"`        // OpenAPI URL (relative to base_url) — Phase 2
	SpecFilter  SpecFilter          `yaml:"spec_filter"` // Phase 2
	PathRules   PathRules           `yaml:"path_rules"`
	Pagination  Pagination          `yaml:"pagination"`
	Output      Output              `yaml:"output"`
	Commands    map[string]Command  `yaml:"commands"`

	// timeout resolved from global defaults at load time (not a YAML field).
	Timeout string `yaml:"-"`
}

// Endpoint is an additional named endpoint for multi-endpoint services.
type Endpoint struct {
	BaseURL     string `yaml:"base_url"`
	Auth        *Auth  `yaml:"auth"` // pointer: nil = inherit service auth, set = override (incl. "none")
	TLSInsecure bool   `yaml:"tls_insecure"`
	Codec       Codec  `yaml:"codec"`
}

// Auth selects and parameterizes a credential strategy. Fields are a superset
// across strategies; only those relevant to `strategy` are read.
//
// For oauth2-client-credentials, prefer the intent-revealing token_url/client_id/
// client_secret fields. The overloaded value/username/password fields are still
// read as a fallback (token_url←value, client_id←username, client_secret←password)
// so older manifests keep working — see OAuth2TokenURL/OAuth2ClientID/
// OAuth2ClientSecret.
type Auth struct {
	Strategy string   `yaml:"strategy"` // none|header-key|bearer|basic|oauth2-client-credentials|ws-login|login-flow|external-tool
	Header   string   `yaml:"header"`   // header-key
	Scheme   string   `yaml:"scheme"`   // bearer (Bearer|token|…)
	Value    string   `yaml:"value"`    // header-key/bearer token template
	Username string   `yaml:"username"` // basic
	Password string   `yaml:"password"` // basic
	Method   string   `yaml:"method"`   // ws-login jsonrpc method
	Params   []string `yaml:"params"`   // ws-login params (templated)

	// oauth2-client-credentials intent-revealing aliases (preferred over the
	// overloaded value/username/password above).
	TokenURL     string `yaml:"token_url"`     // oauth2 token endpoint (fallback: value)
	ClientID     string `yaml:"client_id"`     // oauth2 client_id (fallback: username)
	ClientSecret string `yaml:"client_secret"` // oauth2 client_secret (fallback: password)
}

// OAuth2TokenURL returns the oauth2 token endpoint, preferring the intent-revealing
// token_url field and falling back to the overloaded value field for back-compat.
func (a Auth) OAuth2TokenURL() string {
	if a.TokenURL != "" {
		return a.TokenURL
	}
	return a.Value
}

// OAuth2ClientID returns the oauth2 client_id, preferring the intent-revealing
// client_id field and falling back to the overloaded username field for back-compat.
func (a Auth) OAuth2ClientID() string {
	if a.ClientID != "" {
		return a.ClientID
	}
	return a.Username
}

// OAuth2ClientSecret returns the oauth2 client_secret, preferring the intent-revealing
// client_secret field and falling back to the overloaded password field for back-compat.
func (a Auth) OAuth2ClientSecret() string {
	if a.ClientSecret != "" {
		return a.ClientSecret
	}
	return a.Password
}

// Secret is a credential reference, never a value. Resolved at call time.
type Secret struct {
	Ref    string   `yaml:"ref"`    // op://vault/item/field — item by title OR id
	Env    string   `yaml:"env"`    // env var that overrides the resolver
	Fields []string `yaml:"fields"` // ordered field fallback (n8n: credential, password)
	Idiom  string   `yaml:"idiom"`  // read (default) | item-get | item-json
}

// SpecFilter prunes the generated command set (Phase 2).
type SpecFilter struct {
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

// PathRules captures per-service URL quirks.
type PathRules struct {
	TrailingSlash string `yaml:"trailing_slash"` // "" | before-query (ak: path/?qs)
}

// Pagination is the service-wide default; a command may override it.
type Pagination struct {
	Style string `yaml:"style"` // none|cursor|page-number|page-until-short|fixed-query
	Param string `yaml:"param"` // query param name (page|start|cursor)
	Next  string `yaml:"next"`  // jq path to the next-cursor in the response
	Data  string `yaml:"data"`  // jq path to the item array to unwrap (.results, .data)
	Query string `yaml:"query"` // fixed-query string (bazarr: start=0&length=1000)
}

// Output controls filtering + render mode.
type Output struct {
	DefaultFilter string `yaml:"default_filter"`
	Filter        string `yaml:"filter"` // per-command alias for default_filter
	Mode          string `yaml:"mode"`   // json|raw|scalar
}

// Codec selects request/response encoding for non-JSON endpoints.
type Codec struct {
	Request  string `yaml:"request"`  // json|form
	Response string `yaml:"response"` // json|xml|hujson
}

// Command is one entry in a service's commands: map, or one pipeline step host.
type Command struct {
	Help       string            `yaml:"help"`
	Method     string            `yaml:"method"` // HTTP verb OR jsonrpc method name
	Path       string            `yaml:"path"`
	Query      string            `yaml:"query"`
	Headers    map[string]string `yaml:"headers"`
	Body       string            `yaml:"body"`     // inline JSON or @file
	Params     string            `yaml:"params"`   // jsonrpc params (templated JSON array)
	NoAuth     bool              `yaml:"noauth"`   // skip auth for this command (truenas ping)
	Codec      Codec             `yaml:"codec"`    // per-command codec override
	Endpoint   string            `yaml:"endpoint"` // named endpoint (default if empty)
	Output     Output            `yaml:"output"`
	Pagination Pagination        `yaml:"pagination"`
	MCPIgnore  bool              `yaml:"mcp_ignore"`
	UI         UI                `yaml:"ui"`    // MCP Apps result-View hints (Phase 2)
	Steps      []Step            `yaml:"steps"` // non-empty = composed command (Phase 3)
}

// UI carries optional presentation hints for the MCP Apps universal result
// View (Phase 2). Every field is optional; an absent block means "auto-detect
// the renderer by result shape" — the View reads this from
// structuredContent.labctl.ui. The block is DATA only (no HTML, no URLs, no
// base_url, no secret refs), so it carries no portability concerns and stays
// allowed in a portable manifest like every other presentation-only field.
type UI struct {
	View      string            `yaml:"view,omitempty" json:"view,omitempty"`           // table|record|tree (default: auto)
	Columns   []string          `yaml:"columns,omitempty" json:"columns,omitempty"`     // column order/subset for table view
	Primary   string            `yaml:"primary,omitempty" json:"primary,omitempty"`     // emphasized column
	Badges    map[string]string `yaml:"badges,omitempty" json:"badges,omitempty"`       // field -> badge style ("bool" only, today)
	Sort      *UISort           `yaml:"sort,omitempty" json:"sort,omitempty"`           // default table sort
	Drilldown string            `yaml:"drilldown,omitempty" json:"drilldown,omitempty"` // command id (same service) to call on row click, passed {id}
}

// UISort is the default sort column/direction for a table-view result.
type UISort struct {
	By  string `yaml:"by,omitempty" json:"by,omitempty"`
	Dir string `yaml:"dir,omitempty" json:"dir,omitempty"` // asc|desc
}

// IsZero reports whether u carries no hints at all — the empty/default value.
// Used to decide whether structuredContent.labctl.ui should be null (no
// hints) or the hints object.
func (u UI) IsZero() bool {
	return u.View == "" && len(u.Columns) == 0 && u.Primary == "" &&
		len(u.Badges) == 0 && u.Sort == nil && u.Drilldown == ""
}

// Step is one stage in a composed command (Phase 3 execution; parsed in Phase 1).
type Step struct {
	ID            string            `yaml:"id"`
	Endpoint      string            `yaml:"endpoint"`
	Method        string            `yaml:"method"`
	Path          string            `yaml:"path"`
	Query         string            `yaml:"query"`
	Headers       map[string]string `yaml:"headers"`
	Body          string            `yaml:"body"`
	Decode        string            `yaml:"decode"`
	Extract       map[string]string `yaml:"extract"`
	CaptureHeader map[string]string `yaml:"capture_header"`
	OnError       *Step             `yaml:"on_error"`
	When          string            `yaml:"when"`
	Confirm       string            `yaml:"confirm"`
	BodyTransform string            `yaml:"body_transform"`
}
