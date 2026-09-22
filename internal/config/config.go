// Package config loads infra settings from the process environment
// (12-factor; systemd/compose/make load the env *file*, the process reads
// real env vars). It reads no file: the project list lives in the
// registry (internal/manage).
// stdlib only: net/url for DSN scheme checks.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Identity modes. anonymous salts and rotates whatever identifier the
// client supplies; identified stores it as given. The server is always the
// enforcement point: a client hint never overrides this.
const (
	IdentityAnonymous  = "anonymous"
	IdentityIdentified = "identified"
)

type LogConfig struct {
	Level  string
	Format string
	File   string
}

type BufferConfig struct {
	FlushMaxEvents int
	FlushInterval  time.Duration
	Capacity       int
}

type RetentionClass struct {
	RawDays       int `json:"raw_days"`
	AggregateDays int `json:"aggregate_days"`
}

type Retention struct {
	Views   RetentionClass `json:"views"`
	Product RetentionClass `json:"product"`
}

// DashboardsConfig configures `twillingate dashboards`: which database to
// render, where the Evidence project lives, and how often to rebuild it.
type DashboardsConfig struct {
	DBPath     string
	Addr       string
	Interval   time.Duration
	ProjectDir string
	WorkDir    string
}

// APIConfig carries the -api surface settings. Authentication comes from
// the single API_AUTH_DSN; parsing fans it out into the mode-specific
// fields the verifiers consume:
//
//	token://<token>[?password=<pw>[&redirect=<host>…][&resource=<url>]]
//	oauth://<issuer-host>[/path][?resource=<url>][&audience=<aud>]
//
// password= on token:// turns on the browser login server, which needs a
// resource URL. Callbacks are allowed by host: loopback, claude.ai and
// chatgpt.com always, and each redirect= adds one more. In token and oauth
// modes resource defaults to PUBLIC_URL and must be an origin
// (scheme://host[:port], no path): one identifier covers /mcp and /api/.
// The oauth issuer is https://<host>[/path] and audience defaults to the
// resource. oauth+insecure produces an http issuer for local IdPs and tests.
type APIConfig struct {
	Addr          string // API_ADDR, defaults to IngestAddr
	DBPath        string // API_DB_PATH, defaults to DATABASE_DSN path
	AuthDSN       string // API_AUTH_DSN, verbatim
	AuthMode      string // "oauth" | "token", from the DSN scheme
	ResourceURL   string
	Issuer        string
	Audience      string
	Token         string
	RedirectHosts []string      // token:// redirect=, callback hosts beyond loopback, claude.ai and chatgpt.com
	Password      string        // token:// password=; set, it turns on the login server
	QueryTimeout  time.Duration // API_QUERY_TIMEOUT, default 10s
	QueryMaxRows  int           // API_QUERY_MAX_ROWS, default 1000

	// audienceGiven records an explicit oauth audience=, which the verifier
	// accepts alone; the default admits the resource and resource/mcp.
	audienceGiven bool

	// authErr holds the DSN parse failure until ValidateAPI reports it:
	// bare `serve` must stay lenient (warn and skip the API), so FromEnv
	// cannot fail on a broken auth DSN.
	authErr error
}

type Config struct {
	IngestAddr            string
	Database              string
	Geo                   string
	PublicURL             string
	Log                   LogConfig
	Buffer                BufferConfig
	Retention             Retention
	ProductAttributesTopN int
	Dashboards            DashboardsConfig
	API                   APIConfig
}

// Load builds the configuration from the process environment.
func Load() (*Config, error) {
	return FromEnv(os.LookupEnv)
}

// LoadDashboards builds the configuration for `twillingate dashboards`, which
// renders whatever database it is pointed at.
func LoadDashboards() (*Config, error) {
	return FromEnvDashboards(os.LookupEnv)
}

// env reads typed values from a lookup function, remembering the first error.
type env struct {
	lookup func(string) (string, bool)
	err    error
}

func (e *env) str(key, def string) string {
	if v, ok := e.lookup(key); ok && v != "" {
		return v
	}
	return def
}

func (e *env) num(key string, def int) int {
	v, ok := e.lookup(key)
	if !ok || v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		if e.err == nil {
			e.err = fmt.Errorf("config: %s: invalid integer %q", key, v)
		}
		return def
	}
	return n
}

func (e *env) dur(key string, def time.Duration) time.Duration {
	v, ok := e.lookup(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		if e.err == nil {
			e.err = fmt.Errorf("config: %s: invalid duration %q", key, v)
		}
		return def
	}
	return d
}

// FromEnv parses the environment via lookup (os.LookupEnv in production,
// a map lookup in tests).
func FromEnv(lookup func(string) (string, bool)) (*Config, error) {
	return parse(lookup, false)
}

// FromEnvDashboards is FromEnv, plus the DASHBOARDS_DB_PATH fallback to
// DATABASE_DSN that only the dashboards renderer needs.
func FromEnvDashboards(lookup func(string) (string, bool)) (*Config, error) {
	return parse(lookup, true)
}

func parse(lookup func(string) (string, bool), dashboards bool) (*Config, error) {
	if err := refuseRenamed(lookup); err != nil {
		return nil, err
	}
	e := &env{lookup: lookup}
	c := &Config{
		IngestAddr: e.str("INGEST_ADDR", "127.0.0.1:8080"),
		Database:   e.str("DATABASE_DSN", ""),
		Geo:        e.str("GEO_DSN", "cloudflare://"),
		// The collector's public base URL (https://twillingate.example.com).
		// Embed snippets and MCP integration guidance are built from it;
		// unset, they carry a placeholder and tell the model to ask.
		PublicURL: strings.TrimSuffix(e.str("PUBLIC_URL", ""), "/"),
		Log: LogConfig{
			Level:  e.str("LOG_LEVEL", "info"),
			Format: e.str("LOG_FORMAT", "json"),
			File:   e.str("LOG_FILE", ""),
		},
		Buffer: BufferConfig{
			FlushMaxEvents: e.num("BUFFER_FLUSH_MAX_EVENTS", 1000),
			FlushInterval:  e.dur("BUFFER_FLUSH_INTERVAL", 5*time.Second),
			Capacity:       e.num("BUFFER_CAPACITY", 10000),
		},
		Retention: Retention{
			Views: RetentionClass{
				RawDays:       e.num("RETENTION_VIEWS_RAW_DAYS", 30),
				AggregateDays: e.num("RETENTION_VIEWS_AGGREGATE_DAYS", 365),
			},
			Product: RetentionClass{
				RawDays:       e.num("RETENTION_PRODUCT_RAW_DAYS", 30),
				AggregateDays: e.num("RETENTION_PRODUCT_AGGREGATE_DAYS", 365),
			},
		},
		// Distinct client-supplied *values* per declared attribute key are
		// capped globally rather than per project (spec: the operator picks
		// the key, clients pick the values).
		ProductAttributesTopN: e.num("PRODUCT_ATTRIBUTES_TOP_N", 50),
		Dashboards: DashboardsConfig{
			DBPath:     e.str("DASHBOARDS_DB_PATH", ""),
			Addr:       e.str("DASHBOARDS_ADDR", "0.0.0.0:3000"),
			Interval:   e.dur("DASHBOARDS_INTERVAL", 15*time.Minute),
			ProjectDir: e.str("DASHBOARDS_PROJECT_DIR", "/opt/evidence"),
			WorkDir:    e.str("DASHBOARDS_WORK_DIR", "/var/lib/dashboards"),
		},
	}
	c.API = APIConfig{
		Addr:         e.str("API_ADDR", c.IngestAddr),
		DBPath:       e.str("API_DB_PATH", strings.TrimPrefix(c.Database, "sqlite://")),
		AuthDSN:      e.str("API_AUTH_DSN", ""),
		QueryTimeout: e.dur("API_QUERY_TIMEOUT", 10*time.Second),
		QueryMaxRows: e.num("API_QUERY_MAX_ROWS", 1000),
	}
	if c.API.AuthDSN != "" {
		c.API.authErr = c.parseAPIAuthDSN()
	}
	if e.err != nil {
		return nil, e.err
	}
	if dashboards {
		if c.Dashboards.DBPath == "" {
			if c.Database == "" {
				return nil, fmt.Errorf("config: DASHBOARDS_DB_PATH or DATABASE_DSN is required")
			}
			c.Dashboards.DBPath = strings.TrimPrefix(c.Database, "sqlite://")
		}
		return c, nil
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// renamed maps each variable this release retired to its replacement. A
// set old name refuses the boot: bare `serve` is lenient about API auth,
// so an unrenamed MCP_AUTH_DSN would otherwise switch the API off silently.
var renamed = []struct{ old, repl string }{
	{"LISTEN_ADDR", "INGEST_ADDR"},
	{"MCP_ADDR", "API_ADDR"},
	{"MCP_AUTH_DSN", "API_AUTH_DSN"},
	{"MCP_DB_PATH", "API_DB_PATH"},
	{"MCP_QUERY_TIMEOUT", "API_QUERY_TIMEOUT"},
	{"MCP_QUERY_MAX_ROWS", "API_QUERY_MAX_ROWS"},
	{"RETENTION_WEB_RAW_DAYS", "RETENTION_VIEWS_RAW_DAYS"},
	{"RETENTION_WEB_AGGREGATE_DAYS", "RETENTION_VIEWS_AGGREGATE_DAYS"},
	{"RETENTION_APP_RAW_DAYS", "RETENTION_VIEWS_RAW_DAYS"},
	{"RETENTION_APP_AGGREGATE_DAYS", "RETENTION_VIEWS_AGGREGATE_DAYS"},
}

// refuseRenamed treats an empty value as unset, as env.str does, so a
// leftover `MCP_ADDR=` line does not block the boot.
func refuseRenamed(lookup func(string) (string, bool)) error {
	for _, r := range renamed {
		if v, ok := lookup(r.old); ok && v != "" {
			return fmt.Errorf("config: %s was renamed to %s", r.old, r.repl)
		}
	}
	return nil
}

func (c *Config) validate() error {
	if c.Database == "" {
		return fmt.Errorf("config: DATABASE_DSN is required")
	}
	for _, dsn := range []string{c.Database, c.Geo} {
		u, err := url.Parse(dsn)
		if err != nil || u.Scheme == "" {
			return fmt.Errorf("config: invalid DSN %q", dsn)
		}
	}
	// Validate global retention (negative values only)
	for _, rc := range []RetentionClass{c.Retention.Views, c.Retention.Product} {
		if rc.RawDays < 0 || rc.AggregateDays < 0 {
			return fmt.Errorf("config: retention days must not be negative: %+v", rc)
		}
	}
	return nil
}

// MaxEventAge is derived from the views raw window rather than separately
// configurable: the two must agree or a clamped timestamp could land in
// an already-aggregated day. Retention is global, so ingest clamps against
// exactly this value.
func (c *Config) MaxEventAge() time.Duration {
	return time.Duration(c.Retention.Views.RawDays) * 24 * time.Hour
}

// parseAPIAuthDSN fans API_AUTH_DSN out into the mode-specific APIConfig
// fields. Called from parse once the rest of the config (PUBLIC_URL for
// the oauth resource default) is known.
func (c *Config) parseAPIAuthDSN() error {
	m := &c.API
	scheme, rest, ok := strings.Cut(m.AuthDSN, "://")
	if !ok {
		return fmt.Errorf("config: invalid API_AUTH_DSN %q (token://<token> or oauth://<issuer-host>)", m.AuthDSN)
	}
	switch scheme {
	case "token":
		// Not URL-parsed: the token is opaque and must survive verbatim.
		// Everything after the first '?' configures the login server.
		token, query, hasQuery := strings.Cut(rest, "?")
		m.AuthMode = "token"
		m.Token = token
		if m.Token == "" {
			return fmt.Errorf("config: API_AUTH_DSN token:// requires a token (mint with `twillingate keygen -api`)")
		}
		if hasQuery {
			return c.parseTokenLogin(query)
		}
	case "oauth", "oauth+insecure":
		u, err := url.Parse(m.AuthDSN)
		if err != nil {
			return fmt.Errorf("config: invalid API_AUTH_DSN: %v", err)
		}
		if u.Host == "" {
			return fmt.Errorf("config: API_AUTH_DSN oauth:// requires an issuer host (oauth://idp.example.com)")
		}
		m.AuthMode = "oauth"
		issuerScheme := "https"
		if scheme == "oauth+insecure" {
			issuerScheme = "http"
		}
		m.Issuer = issuerScheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/")
		q := u.Query()
		if m.ResourceURL, err = c.resource("oauth://", q.Get("resource")); err != nil {
			return err
		}
		if m.ResourceURL == "" {
			return fmt.Errorf("config: API_AUTH_DSN oauth:// requires ?resource=<origin> or PUBLIC_URL to derive it from")
		}
		m.Audience = q.Get("audience")
		m.audienceGiven = m.Audience != ""
		if m.Audience == "" {
			m.Audience = m.ResourceURL
		}
	default:
		return fmt.Errorf("config: unknown API_AUTH_DSN scheme %q (token or oauth)", scheme)
	}
	return nil
}

// parseTokenLogin reads the token:// query that turns on the browser login
// server: password=, repeated redirect= and resource=.
func (c *Config) parseTokenLogin(query string) error {
	m := &c.API
	q, err := url.ParseQuery(query)
	if err != nil {
		return fmt.Errorf("config: invalid API_AUTH_DSN token:// query: %v", err)
	}
	for k := range q {
		if k != "redirect" && k != "password" && k != "resource" {
			return fmt.Errorf("config: API_AUTH_DSN token:// has unknown parameter %q (redirect, password or resource)", k)
		}
	}
	m.Password = q.Get("password")
	if m.Password == "" {
		return fmt.Errorf("config: API_AUTH_DSN token:// redirect= and resource= need a password=, which turns the login on")
	}
	for _, r := range q["redirect"] {
		host, err := redirectHost(r)
		if err != nil {
			return fmt.Errorf("config: API_AUTH_DSN token:// redirect=%q %v", r, err)
		}
		m.RedirectHosts = append(m.RedirectHosts, host)
	}
	if m.ResourceURL, err = c.resource("token://", q.Get("resource")); err != nil {
		return err
	}
	if m.ResourceURL == "" {
		return fmt.Errorf("config: API_AUTH_DSN token:// password= requires resource=<origin> or PUBLIC_URL to derive it from")
	}
	if err := checkLoginURL(m.ResourceURL); err != nil {
		return fmt.Errorf("config: API_AUTH_DSN token:// resource=%q %v", m.ResourceURL, err)
	}
	return nil
}

// resource resolves the API resource identifier: resource= when given,
// else PUBLIC_URL, either way read as an origin. Empty when neither is set.
func (c *Config) resource(scheme, given string) (string, error) {
	switch {
	case given != "":
		r, err := resourceOrigin(given)
		if err != nil {
			return "", fmt.Errorf("config: API_AUTH_DSN %s resource=%q %v", scheme, given, err)
		}
		return r, nil
	case c.PublicURL != "":
		r, err := resourceOrigin(c.PublicURL)
		if err != nil {
			return "", fmt.Errorf("config: PUBLIC_URL=%q %v, or set resource= in API_AUTH_DSN", c.PublicURL, err)
		}
		return r, nil
	}
	return "", nil
}

// resourceOrigin reads a resource= value as the API origin: an absolute
// http(s) URL with no path beyond "/", returned without the slash. One
// identifier covers /mcp and /api/, and matches the host-rooted RFC 9728
// metadata the API serves. The host is lowercased and a default port (443
// for https, 80 for http) is dropped, so resource=https://API.example.com:443
// names the same origin a client actually connects to (case-insensitive,
// port-implicit) rather than comparing as a distinct string.
func resourceOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("must be an absolute http(s) origin such as https://api.example.com")
	}
	if (u.Path != "" && u.Path != "/") || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return "", fmt.Errorf("must be an origin with no path, userinfo, query or fragment, such as https://api.example.com (the API serves /mcp and /api/ under it)")
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") { // IPv6; Hostname() strips the brackets Host carried
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && !((u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80")) {
		host += ":" + port
	}
	return u.Scheme + "://" + host, nil
}

// redirectHost reads a redirect= value as the host it allows: a bare host
// (app.example.com, 127.0.0.1, [::1]) or, for DSNs written against exact
// callbacks, a full URL whose host is taken.
func redirectHost(raw string) (string, error) {
	if strings.Contains(raw, "://") {
		if err := checkLoginURL(raw); err != nil {
			return "", err
		}
		u, _ := url.Parse(raw)
		return strings.ToLower(u.Hostname()), nil
	}
	u, err := url.Parse("https://" + raw)
	if raw == "" || err != nil || u.Host != raw || u.Port() != "" {
		return "", fmt.Errorf("must be a host such as app.example.com, without scheme, port or path")
	}
	return strings.ToLower(u.Hostname()), nil
}

// checkLoginURL admits an absolute http(s) URL with no fragment, and plain
// http only on a loopback host.
func checkLoginURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("does not parse: %v", err)
	}
	if u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("must be an absolute http(s) URL")
	}
	if strings.Contains(raw, "#") {
		return fmt.Errorf("must not carry a fragment")
	}
	if h := u.Hostname(); u.Scheme == "http" && h != "localhost" && h != "127.0.0.1" && h != "::1" {
		return fmt.Errorf("may only use http on localhost, 127.0.0.1 or [::1]")
	}
	return nil
}

// AudienceGiven reports whether oauth:// named its audience= explicitly
// rather than defaulting it to the resource.
func (m APIConfig) AudienceGiven() bool { return m.audienceGiven }

// LoginEnabled reports whether token:// runs the browser login server.
func (m APIConfig) LoginEnabled() bool { return m.AuthMode == "token" && m.Password != "" }

// ValidateAPI fail-fasts the -api surface (endpoint spec §4): there is no
// unauthenticated mode and no way to reach one by omission.
func (c *Config) ValidateAPI() error {
	m := c.API
	if m.AuthDSN == "" {
		return fmt.Errorf("config: -api requires API_AUTH_DSN (token://<token> or oauth://<issuer-host>)")
	}
	return m.authErr
}
