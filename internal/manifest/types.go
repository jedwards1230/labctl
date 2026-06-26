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
	Secret   SecretResolver `yaml:"secret"`
}

// Defaults are global fallbacks applied to a service when it leaves a field unset.
type Defaults struct {
	Timeout string `yaml:"timeout"` // Go duration string, e.g. "60s"
	Output  string `yaml:"output"`  // json | raw | scalar (table deferred)
}

// SecretEnvSource is one extra environment variable injected into the resolver
// subprocess only. Exactly one of File/Value/Env must be set.
type SecretEnvSource struct {
	File  string `yaml:"file"`
	Value string `yaml:"value"`
	Env   string `yaml:"env"`
}

// SecretResolver describes the external tool that turns a ref into a value.
// Default: `op read {ref}`.
type SecretResolver struct {
	Resolver    string                     `yaml:"resolver"`     // label only ("op")
	Command     []string                   `yaml:"command"`      // argv; {ref} is substituted
	EnvOverride bool                       `yaml:"env_override"` // allow <PREFIX>_<FIELD> env to skip the resolver
	Env         map[string]SecretEnvSource `yaml:"env"`          // extra env vars injected into the resolver subprocess only
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
type Auth struct {
	Strategy string   `yaml:"strategy"` // none|header-key|bearer|basic|oauth2-client-credentials|ws-login|login-flow|external-tool
	Header   string   `yaml:"header"`   // header-key
	Scheme   string   `yaml:"scheme"`   // bearer (Bearer|token|…)
	Value    string   `yaml:"value"`    // header-key/bearer token template
	Username string   `yaml:"username"` // basic
	Password string   `yaml:"password"` // basic
	Method   string   `yaml:"method"`   // ws-login jsonrpc method
	Params   []string `yaml:"params"`   // ws-login params (templated)
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
	Steps      []Step            `yaml:"steps"` // non-empty = composed command (Phase 3)
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
