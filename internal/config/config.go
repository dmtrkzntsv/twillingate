// Package config loads infra settings from the process environment
// (12-factor; systemd/compose/make load the env *file*, the process reads
// real env vars). It no longer reads any file: the project list lives in
// the registry (internal/manage), seeded once via `twillingate config
// import` from the legacy projects.json format.
// stdlib only: encoding/json + net/url for DSN scheme checks.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
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
	Web     RetentionClass `json:"web"`
	Product RetentionClass `json:"product"`
	App     RetentionClass `json:"app"`
}

type RetentionClassOverride struct {
	RawDays       *int `json:"raw_days"`
	AggregateDays *int `json:"aggregate_days"`
}

type RetentionOverride struct {
	Web     *RetentionClassOverride `json:"web"`
	Product *RetentionClassOverride `json:"product"`
	App     *RetentionClassOverride `json:"app"`
}

// LegacyAggregation is the pre-2026-08 product_aggregation block: an
// event-keyed map of attribute keys, an opt-in flag and a per-project
// top_n. `attributes` replaced it with one flat declared list (enabled is
// gone — rollups always run now; top_n is the global
// PRODUCT_ATTRIBUTES_TOP_N setting), but `config import` still accepts
// this shape so an unmodified pre-upgrade projects.json keeps working.
type LegacyAggregation struct {
	Enabled    bool                `json:"enabled"`
	Attributes map[string][]string `json:"attributes"`
	TopN       int                 `json:"top_n"`
}

// IngestKey is one client credential. Multiple keys per project let a
// website, an iOS app and a desktop app be retired independently. Disabled
// rather than deleted: retirement is reversible during a botched rollout
// without regenerating and redistributing.
//
// Legacy projects.json format, used only by `twillingate config import`.
type IngestKey struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled"`
}

// Project is the legacy projects.json format, used only by `twillingate
// config import` to seed the registry (internal/manage) from a pre-upgrade
// install. The running server never reads this type from a file.
type Project struct {
	Alias          string             `json:"alias"`
	Name           string             `json:"name"`
	Identity       string             `json:"identity"`
	IngestKeys     []IngestKey        `json:"ingest_keys"`
	AllowedOrigins []string           `json:"allowed_origins"`
	Retention      *RetentionOverride `json:"retention"`
	Attributes     []string           `json:"attributes"`
	// LegacyAggregation is the pre-2026-08 product_aggregation block.
	// Import still accepts it and folds its event-keyed map into a flat
	// list, so an unmodified pre-upgrade projects.json still imports.
	LegacyAggregation *LegacyAggregation `json:"product_aggregation"`
}

// DeclaredAttributes returns the declared keys, folding the legacy
// event-keyed map into a sorted DISTINCT union when only it is present.
func (p *Project) DeclaredAttributes() []string {
	if len(p.Attributes) > 0 || p.LegacyAggregation == nil {
		return p.Attributes
	}
	seen := map[string]bool{}
	var out []string
	for _, keys := range p.LegacyAggregation.Attributes {
		for _, k := range keys {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
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

// MCPConfig carries the -mcp surface settings. Authentication comes from
// the single MCP_AUTH_DSN; parsing fans it out into the mode-specific
// fields the verifiers consume:
//
//	token://<token>[?password=<pw>&redirect=<uri>[&redirect=<uri>…][&resource=<url>]]
//	oauth://<issuer-host>[/path][?resource=<url>][&audience=<aud>]
//
// Any redirect= on token:// turns on the browser login server, which
// requires password= and a resource URL. In token and oauth modes resource
// defaults to PUBLIC_URL + "/mcp"; the oauth issuer is https://<host>[/path]
// and audience defaults to the resource URL. oauth+insecure produces an
// http issuer for local IdPs and tests.
type MCPConfig struct {
	Addr         string // MCP_ADDR, defaults to Listen
	DBPath       string // MCP_DB_PATH, defaults to DATABASE_DSN path
	AuthDSN      string // MCP_AUTH_DSN, verbatim
	AuthMode     string // "oauth" | "token", from the DSN scheme
	ResourceURL  string
	Issuer       string
	Audience     string
	Token        string
	RedirectURIs []string      // token:// redirect=; any turns on the login server
	Password     string        // token:// password=, the login page's secret
	QueryTimeout time.Duration // MCP_QUERY_TIMEOUT, default 10s
	QueryMaxRows int           // MCP_QUERY_MAX_ROWS, default 1000

	// authErr holds the DSN parse failure until ValidateMCP reports it:
	// bare `serve` must stay lenient (warn and skip MCP), so FromEnv
	// cannot fail on a broken auth DSN.
	authErr error
}

type Config struct {
	Listen                string
	Database              string
	Geo                   string
	PublicURL             string
	Log                   LogConfig
	Buffer                BufferConfig
	Retention             Retention
	ProductAttributesTopN int
	Dashboards            DashboardsConfig
	MCP                   MCPConfig
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
	e := &env{lookup: lookup}
	c := &Config{
		Listen:   e.str("LISTEN_ADDR", "127.0.0.1:8080"),
		Database: e.str("DATABASE_DSN", ""),
		Geo:      e.str("GEO_DSN", "cloudflare://"),
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
			Web: RetentionClass{
				RawDays:       e.num("RETENTION_WEB_RAW_DAYS", 7),
				AggregateDays: e.num("RETENTION_WEB_AGGREGATE_DAYS", 365),
			},
			Product: RetentionClass{
				RawDays:       e.num("RETENTION_PRODUCT_RAW_DAYS", 30),
				AggregateDays: e.num("RETENTION_PRODUCT_AGGREGATE_DAYS", 365),
			},
			App: RetentionClass{
				RawDays:       e.num("RETENTION_APP_RAW_DAYS", 30),
				AggregateDays: e.num("RETENTION_APP_AGGREGATE_DAYS", 365),
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
	c.MCP = MCPConfig{
		Addr:         e.str("MCP_ADDR", c.Listen),
		DBPath:       e.str("MCP_DB_PATH", strings.TrimPrefix(c.Database, "sqlite://")),
		AuthDSN:      e.str("MCP_AUTH_DSN", ""),
		QueryTimeout: e.dur("MCP_QUERY_TIMEOUT", 10*time.Second),
		QueryMaxRows: e.num("MCP_QUERY_MAX_ROWS", 1000),
	}
	if c.MCP.AuthDSN != "" {
		c.MCP.authErr = c.parseMCPAuthDSN()
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

// ParseProjects reads a projects.json: a bare JSON array of projects.
//
// Legacy projects.json format, used only by `twillingate config import`.
func ParseProjects(r io.Reader) ([]Project, error) {
	var ps []Project
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ps); err != nil {
		return nil, fmt.Errorf("projects: %w", err)
	}
	return ps, nil
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
	for _, rc := range []RetentionClass{c.Retention.Web, c.Retention.Product, c.Retention.App} {
		if rc.RawDays < 0 || rc.AggregateDays < 0 {
			return fmt.Errorf("config: retention days must not be negative: %+v", rc)
		}
	}
	return nil
}

// MaxEventAge is derived from the app raw window rather than separately
// configurable: the two must agree or a clamped timestamp could land in an
// already-aggregated day.
func (c *Config) MaxEventAge() time.Duration {
	return time.Duration(c.Retention.App.RawDays) * 24 * time.Hour
}

// parseMCPAuthDSN fans MCP_AUTH_DSN out into the mode-specific MCPConfig
// fields. Called from parse once the rest of the config (PUBLIC_URL for
// the oauth resource default) is known.
func (c *Config) parseMCPAuthDSN() error {
	m := &c.MCP
	scheme, rest, ok := strings.Cut(m.AuthDSN, "://")
	if !ok {
		return fmt.Errorf("config: invalid MCP_AUTH_DSN %q (token://<token> or oauth://<issuer-host>)", m.AuthDSN)
	}
	switch scheme {
	case "token":
		// Not URL-parsed: the token is opaque and must survive verbatim.
		// Everything after the first '?' configures the login server.
		token, query, hasQuery := strings.Cut(rest, "?")
		m.AuthMode = "token"
		m.Token = token
		if m.Token == "" {
			return fmt.Errorf("config: MCP_AUTH_DSN token:// requires a token (mint with `twillingate keygen -mcp`)")
		}
		if hasQuery {
			return c.parseTokenLogin(query)
		}
	case "oauth", "oauth+insecure":
		u, err := url.Parse(m.AuthDSN)
		if err != nil {
			return fmt.Errorf("config: invalid MCP_AUTH_DSN: %v", err)
		}
		if u.Host == "" {
			return fmt.Errorf("config: MCP_AUTH_DSN oauth:// requires an issuer host (oauth://idp.example.com)")
		}
		m.AuthMode = "oauth"
		issuerScheme := "https"
		if scheme == "oauth+insecure" {
			issuerScheme = "http"
		}
		m.Issuer = issuerScheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/")
		q := u.Query()
		m.ResourceURL = q.Get("resource")
		if m.ResourceURL == "" && c.PublicURL != "" {
			m.ResourceURL = c.PublicURL + "/mcp"
		}
		if m.ResourceURL == "" {
			return fmt.Errorf("config: MCP_AUTH_DSN oauth:// requires ?resource=<url> or PUBLIC_URL to derive it from")
		}
		m.Audience = q.Get("audience")
		if m.Audience == "" {
			m.Audience = m.ResourceURL
		}
	default:
		return fmt.Errorf("config: unknown MCP_AUTH_DSN scheme %q (token or oauth)", scheme)
	}
	return nil
}

// parseTokenLogin reads the token:// query that turns on the browser login
// server: repeated redirect=, password= and resource=.
func (c *Config) parseTokenLogin(query string) error {
	m := &c.MCP
	q, err := url.ParseQuery(query)
	if err != nil {
		return fmt.Errorf("config: invalid MCP_AUTH_DSN token:// query: %v", err)
	}
	for k := range q {
		if k != "redirect" && k != "password" && k != "resource" {
			return fmt.Errorf("config: MCP_AUTH_DSN token:// has unknown parameter %q (redirect, password or resource)", k)
		}
	}
	m.RedirectURIs = q["redirect"]
	m.Password = q.Get("password")
	m.ResourceURL = q.Get("resource")
	if len(m.RedirectURIs) == 0 {
		return fmt.Errorf("config: MCP_AUTH_DSN token:// password= and resource= only apply with at least one redirect=")
	}
	if m.Password == "" {
		return fmt.Errorf("config: MCP_AUTH_DSN token:// redirect= requires a password=")
	}
	for _, r := range m.RedirectURIs {
		if err := checkLoginURL(r); err != nil {
			return fmt.Errorf("config: MCP_AUTH_DSN token:// redirect=%q %v", r, err)
		}
	}
	if m.ResourceURL == "" && c.PublicURL != "" {
		m.ResourceURL = c.PublicURL + "/mcp"
	}
	if m.ResourceURL == "" {
		return fmt.Errorf("config: MCP_AUTH_DSN token:// redirect= requires resource=<url> or PUBLIC_URL to derive it from")
	}
	if err := checkLoginURL(m.ResourceURL); err != nil {
		return fmt.Errorf("config: MCP_AUTH_DSN token:// resource=%q %v", m.ResourceURL, err)
	}
	return nil
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

// ValidateMCP fail-fasts the -mcp surface (endpoint spec §4): there is no
// unauthenticated mode and no way to reach one by omission.
func (c *Config) ValidateMCP() error {
	m := c.MCP
	if m.AuthDSN == "" {
		return fmt.Errorf("config: -mcp requires MCP_AUTH_DSN (token://<token> or oauth://<issuer-host>)")
	}
	return m.authErr
}
