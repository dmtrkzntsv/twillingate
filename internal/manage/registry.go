// Package manage owns the project registry (managed-config spec §3–§4):
// an immutable snapshot behind an atomic pointer for the ingest hot path,
// and the audited operations that mutate it. MCP tools and CLI subcommands
// are thin frontends over this package.
package manage

import (
	"cmp"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

type Project struct {
	ID             int64
	Name           string
	AllowedOrigins []string
	Archived       bool
	ArchivedAt     string // RFC 3339 UTC, "" = live
	SortKey        string // its place in the order
}

type keyOwner struct {
	key     string
	project *Project
	label   string
}

// Snapshot is an immutable view of the registry. Readers pay one atomic
// load; every mutation builds a fresh one.
type Snapshot struct {
	byID    map[int64]*Project
	ordered []*Project // by sort key, then id
	keys    []keyOwner // active keys of non-archived projects only
	origins map[int64]originSet
	rules   map[int64]map[ruleKey]store.EventRule // event rules by project (event rules D9)
	// attrRules and attrRuleList are the attribute rules by project: the
	// maps the write path applies, and the rules in key order. A project
	// without rules has no entry in either.
	attrRules    map[int64]store.AttributeRuleSet
	attrRuleList map[int64][]store.AttributeRule
}

type ruleKey struct {
	family store.Family
	name   string
}

// pollInterval bounds how often the hot path re-reads config_version to
// notice out-of-process writes (CLI, second serve process). Spec §3.3.
const pollInterval = time.Second

type Registry struct {
	st     Store
	logger *slog.Logger

	// publish serializes storing snap and version as a pair, so a reload
	// and a withhold cannot interleave into a snapshot and version that
	// disagree.
	publish   sync.Mutex
	snap      atomic.Pointer[Snapshot]
	version   atomic.Int64
	lastCheck atomic.Int64 // UnixNano of the last version poll
}

// unknownVersion is never a real config_version, so a poll that compares
// against it always reloads.
const unknownVersion = -1

func New(st Store, logger *slog.Logger) *Registry {
	r := &Registry{st: st, logger: logger}
	r.snap.Store(&Snapshot{byID: map[int64]*Project{}, origins: map[int64]originSet{},
		rules: map[int64]map[ruleKey]store.EventRule{}, attrRules: map[int64]store.AttributeRuleSet{},
		attrRuleList: map[int64][]store.AttributeRule{}})
	return r
}

// Reload rebuilds the snapshot from the database. Called at boot, after
// every in-process write, and by the poll when config_version moves.
func (r *Registry) Reload(ctx context.Context) error {
	ps, ks, err := r.st.LoadRegistry(ctx)
	if err != nil {
		return fmt.Errorf("manage: load registry: %w", err)
	}
	rules, err := r.st.LoadEventRules(ctx)
	if err != nil {
		return fmt.Errorf("manage: load event rules: %w", err)
	}
	attrRules, err := r.st.LoadAttributeRules(ctx)
	if err != nil {
		return fmt.Errorf("manage: load attribute rules: %w", err)
	}
	v, err := r.st.ConfigVersion(ctx)
	if err != nil {
		return fmt.Errorf("manage: config version: %w", err)
	}
	s := &Snapshot{
		byID:         make(map[int64]*Project, len(ps)),
		origins:      make(map[int64]originSet, len(ps)),
		rules:        make(map[int64]map[ruleKey]store.EventRule),
		attrRules:    make(map[int64]store.AttributeRuleSet),
		attrRuleList: make(map[int64][]store.AttributeRule),
	}
	for _, ar := range attrRules {
		set := s.attrRules[ar.ProjectID]
		switch ar.Action {
		case store.EventRuleRename:
			if set.Renamed == nil {
				set.Renamed = map[string]string{}
			}
			set.Renamed[ar.Key] = ar.To
		case store.EventRuleBlock:
			if set.Blocked == nil {
				set.Blocked = map[string]bool{}
			}
			set.Blocked[ar.Key] = true
		}
		s.attrRules[ar.ProjectID] = set
		s.attrRuleList[ar.ProjectID] = append(s.attrRuleList[ar.ProjectID], ar)
	}
	for _, er := range rules {
		if s.rules[er.ProjectID] == nil {
			s.rules[er.ProjectID] = map[ruleKey]store.EventRule{}
		}
		s.rules[er.ProjectID][ruleKey{er.Family, er.Name}] = er
	}
	for _, rp := range ps {
		p := &Project{ID: rp.ID, Name: rp.Name, Archived: rp.Archived, ArchivedAt: rp.ArchivedAt, SortKey: rp.SortKey}
		if rp.AllowedOrigins != "" {
			if err := json.Unmarshal([]byte(rp.AllowedOrigins), &p.AllowedOrigins); err != nil {
				return fmt.Errorf("manage: project %d allowed_origins: %w", rp.ID, err)
			}
		}
		s.byID[p.ID] = p
		s.ordered = append(s.ordered, p)
		set := originSet{exact: map[string]bool{}}
		for _, o := range p.AllowedOrigins {
			if o = trimSlash(o); strings.ContainsRune(o, '*') {
				set.globs = append(set.globs, o)
				continue
			}
			set.exact[o] = true
		}
		s.origins[p.ID] = set
	}
	for _, k := range ks {
		p := s.byID[k.ProjectID]
		if p == nil || p.Archived || k.Disabled {
			continue // archived projects reject events (001_init.sql comment)
		}
		s.keys = append(s.keys, keyOwner{key: k.Key, project: p, label: k.Label})
	}
	r.publish.Lock()
	r.snap.Store(s)
	r.version.Store(v)
	r.publish.Unlock()
	r.lastCheck.Store(time.Now().UnixNano())
	return nil
}

// withhold swaps in a copy of the held snapshot that authorizes no key of
// the given projects. It is the fail-closed half of a reload that failed
// after a committed write: the held snapshot may still grant what the
// write revoked (a disabled key, an archived or deleted project), so
// those projects' events are refused until a reload succeeds. The version
// is forgotten so the next poll reloads even if the snapshot underneath
// was already current.
func (r *Registry) withhold(ids ...int64) {
	drop := make(map[int64]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	r.publish.Lock()
	defer r.publish.Unlock()
	cur := r.snap.Load()
	s := *cur
	s.keys = make([]keyOwner, 0, len(cur.keys))
	for _, k := range cur.keys {
		if !drop[k.project.ID] {
			s.keys = append(s.keys, k)
		}
	}
	r.snap.Store(&s)
	r.version.Store(unknownVersion)
	r.lastCheck.Store(0)
}

// Snapshot returns the current registry view, polling config_version at
// most once per pollInterval to notice out-of-process writes. On poll
// failure the previous snapshot keeps serving: a transient read error
// must not take down ingestion. A poll cut short because its caller went
// away (a client that closed the connection) is not a failure: it is not
// logged, and the next caller polls again.
func (r *Registry) Snapshot(ctx context.Context) *Snapshot {
	last := r.lastCheck.Load()
	now := time.Now().UnixNano()
	if now-last >= int64(pollInterval) && r.lastCheck.CompareAndSwap(last, now) {
		if v, err := r.st.ConfigVersion(ctx); errors.Is(err, context.Canceled) {
			r.lastCheck.CompareAndSwap(now, last)
		} else if err != nil {
			r.logger.Warn("registry version poll failed", "error", err)
		} else if v != r.version.Load() {
			if err := r.Reload(ctx); err != nil {
				r.logger.Warn("registry reload failed", "error", err)
			}
		}
	}
	return r.snap.Load()
}

// originSet is a project's allowed origins split at reload time, so the
// hot path pays one map lookup and only walks patterns for the projects
// that actually use them.
type originSet struct {
	exact map[string]bool
	globs []string
}

func (s originSet) match(origin string) bool {
	if s.exact[origin] {
		return true
	}
	for _, g := range s.globs {
		if matchOrigin(g, origin) {
			return true
		}
	}
	return false
}

// matchOrigin reports whether an allowed_origins entry matches an origin.
// A bare "*" allows every origin; anywhere else "*" stands for any run of
// characters that contains no "/", so "https://*.example.com" covers every
// subdomain but never another scheme, and an attacker cannot smuggle the
// allowed host into some other part of the URL.
func matchOrigin(pattern, origin string) bool {
	if pattern == "*" {
		return true
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == origin
	}
	if !strings.HasPrefix(origin, parts[0]) {
		return false
	}
	rest := origin[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		// Leftmost match: it consumes the least, leaving the most room
		// for the remaining segments.
		i := strings.Index(rest, mid)
		if i < 0 || strings.Contains(rest[:i], "/") {
			return false
		}
		rest = rest[i+len(mid):]
	}
	last := parts[len(parts)-1]
	return strings.HasSuffix(rest, last) && !strings.Contains(rest[:len(rest)-len(last)], "/")
}

func trimSlash(o string) string {
	if len(o) > 0 && o[len(o)-1] == '/' {
		return o[:len(o)-1]
	}
	return o
}

// ResolveEvent is the name an event is stored under and whether it is
// dropped (event rules D9): a rename rule maps the name once (targets are
// never renamed again, the store keeps chains collapsed), then a block on
// the result drops it.
func (s *Snapshot) ResolveEvent(projectID int64, family store.Family, name string) (string, bool) {
	rs := s.rules[projectID]
	if r, ok := rs[ruleKey{family, name}]; ok && r.Action == store.EventRuleRename {
		name = r.To
	}
	r, ok := rs[ruleKey{family, name}]
	return name, ok && r.Action == store.EventRuleBlock
}

// AttributeRules is the project's attribute rules as the write path applies
// them; the zero value (maps nothing) when it has none. The maps are the
// snapshot's own, shared by every reader: callers must not modify them.
func (s *Snapshot) AttributeRules(projectID int64) store.AttributeRuleSet {
	return s.attrRules[projectID]
}

// AttributeRuleList is the project's attribute rules ordered by key, nil
// when it has none. The slice is the snapshot's own, shared by every
// reader: callers must not modify it or its elements.
func (s *Snapshot) AttributeRuleList(projectID int64) []store.AttributeRule {
	return s.attrRuleList[projectID]
}

// Rules is every project's event rules, by project, family and name,
// and attribute rules, by project and key: what the event files' tables
// apply on read (raw-first D6). The slices are new; the rules are copies.
func (s *Snapshot) Rules() archive.Rules {
	var events []store.EventRule
	for _, rs := range s.rules {
		for _, r := range rs {
			events = append(events, r)
		}
	}
	slices.SortFunc(events, func(a, b store.EventRule) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.Family, b.Family), cmp.Compare(a.Name, b.Name))
	})
	var attrs []store.AttributeRule
	for _, list := range s.attrRuleList {
		attrs = append(attrs, list...)
	}
	slices.SortFunc(attrs, func(a, b store.AttributeRule) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.Key, b.Key))
	})
	return archive.Rules{Events: events, Attributes: attrs}
}

func (s *Snapshot) Project(id int64) *Project { return s.byID[id] }

// Projects is every project, archived ones included, in the order the
// console shows them (MoveProject).
func (s *Snapshot) Projects() []*Project { return s.ordered }

// ProjectByKey preserves the constant-time contract of the old
// config.ProjectByKey: every candidate compared, no early return.
func (s *Snapshot) ProjectByKey(key string) (*Project, string, bool) {
	if key == "" {
		return nil, "", false
	}
	match := -1
	kb := []byte(key)
	for i := range s.keys {
		if subtle.ConstantTimeCompare([]byte(s.keys[i].key), kb) == 1 {
			match = i
		}
	}
	if match < 0 {
		return nil, "", false
	}
	return s.keys[match].project, s.keys[match].label, true
}

func (s *Snapshot) OriginAllowed(id int64, origin string) bool {
	set, ok := s.origins[id]
	return ok && set.match(trimSlash(origin))
}

// RedirectAllowed reports whether target may be where a form submission is
// sent back to: an absolute http(s) URL with a host and no userinfo, whose
// origin passes the project's allowed_origins. An origin that passes only
// through a bare "*" entry must also equal reqOrigin (the request's Origin
// header), so "*" admits the page the post came from but never makes the
// collector an open redirect.
func (s *Snapshot) RedirectAllowed(projectID int64, target, reqOrigin string) bool {
	origin, ok := urlOrigin(target)
	if !ok {
		return false
	}
	set, ok := s.origins[projectID]
	if !ok {
		return false
	}
	if set.exact[origin] {
		return true
	}
	bare := false
	for _, g := range set.globs {
		if g == "*" {
			bare = true
			continue
		}
		if matchOrigin(g, origin) {
			return true
		}
	}
	return bare && origin == trimSlash(reqOrigin)
}

// urlOrigin is target's origin as a browser writes it in an Origin header
// (scheme://host[:port], host lowercased, the default port dropped), or
// false when target is not an absolute http(s) URL with a host. A
// backslash anywhere is refused: browsers read it as a slash, Go does not,
// so the two could disagree about the host.
func urlOrigin(target string) (string, bool) {
	if strings.ContainsRune(target, '\\') {
		return "", false
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Opaque != "" || u.User != nil || u.Hostname() == "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") { // IPv6; Hostname() strips the brackets
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && !((u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80")) {
		host += ":" + port
	}
	return u.Scheme + "://" + host, true
}

func (s *Snapshot) AnyOriginAllowed(origin string) bool {
	o := trimSlash(origin)
	for _, set := range s.origins {
		if set.match(o) {
			return true
		}
	}
	return false
}

// KeylessProjects lists active projects with no active key: a legitimate
// retired state, so callers warn rather than fail.
func (s *Snapshot) KeylessProjects() []*Project {
	withKey := map[int64]bool{}
	for _, k := range s.keys {
		withKey[k.project.ID] = true
	}
	var out []*Project
	for _, p := range s.ordered {
		if !p.Archived && !withKey[p.ID] {
			out = append(out, p)
		}
	}
	return out
}
