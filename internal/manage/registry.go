// Package manage owns the project registry (managed-config spec §3–§4):
// an immutable snapshot behind an atomic pointer for the ingest hot path,
// and the audited operations that mutate it. MCP tools and CLI subcommands
// are thin frontends over this package.
package manage

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Project struct {
	ID             int64
	Name, Identity string
	AllowedOrigins []string
	Attributes     []string
	Archived       bool
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
	ordered []*Project // ascending id
	keys    []keyOwner // active keys of non-archived projects only
	origins map[int64]originSet
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
	r.snap.Store(&Snapshot{byID: map[int64]*Project{}, origins: map[int64]originSet{}})
	return r
}

// Reload rebuilds the snapshot from the database. Called at boot, after
// every in-process write, and by the poll when config_version moves.
func (r *Registry) Reload(ctx context.Context) error {
	ps, ks, err := r.st.LoadRegistry(ctx)
	if err != nil {
		return fmt.Errorf("manage: load registry: %w", err)
	}
	v, err := r.st.ConfigVersion(ctx)
	if err != nil {
		return fmt.Errorf("manage: config version: %w", err)
	}
	s := &Snapshot{
		byID:    make(map[int64]*Project, len(ps)),
		origins: make(map[int64]originSet, len(ps)),
	}
	for _, rp := range ps {
		p := &Project{ID: rp.ID, Name: rp.Name, Identity: rp.Identity, Archived: rp.Archived}
		if rp.AllowedOrigins != "" {
			if err := json.Unmarshal([]byte(rp.AllowedOrigins), &p.AllowedOrigins); err != nil {
				return fmt.Errorf("manage: project %d allowed_origins: %w", rp.ID, err)
			}
		}
		if rp.Attributes != "" {
			if err := json.Unmarshal([]byte(rp.Attributes), &p.Attributes); err != nil {
				return fmt.Errorf("manage: project %d attributes: %w", rp.ID, err)
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
// write revoked (a disabled key, an archived or deleted project, a switch
// to anonymous identity), so those projects' events are refused until a
// reload succeeds. The version is forgotten so the next poll reloads even
// if the snapshot underneath was already current.
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
// must not take down ingestion.
func (r *Registry) Snapshot(ctx context.Context) *Snapshot {
	last := r.lastCheck.Load()
	now := time.Now().UnixNano()
	if now-last >= int64(pollInterval) && r.lastCheck.CompareAndSwap(last, now) {
		if v, err := r.st.ConfigVersion(ctx); err != nil {
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

func (s *Snapshot) Project(id int64) *Project { return s.byID[id] }

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

// AttributesFor returns the project's declared attribute keys. Unknown ids
// return nil, matching the archived-project fallback.
func (s *Snapshot) AttributesFor(id int64) []string {
	p := s.byID[id]
	if p == nil {
		return nil
	}
	return p.Attributes
}

// DeclaredAttributeKeys is the sorted, deduplicated union of every
// project's declared keys — the column set of v_events_flat. Archived
// projects are included: archiving keeps their data queryable.
func (s *Snapshot) DeclaredAttributeKeys() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range s.ordered {
		for _, k := range p.Attributes {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}
