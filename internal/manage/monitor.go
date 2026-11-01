package manage

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const (
	// maxMonitorContains caps the text a monitored page must contain.
	maxMonitorContains = 200
	// monitorCheckCooldown is the least time between manual checks of a
	// project, whatever its interval: it protects the site and the RDAP
	// registries, so it applies to everyone.
	monitorCheckCooldown = 60 * time.Second
)

// MonitorOrigins is the subset of origins a monitor may use: exact http://
// or https:// origins (no path, no "*" anywhere, no zone on an IPv6 literal:
// it names an interface of this machine), in their normalized form (see
// urlOrigin), first occurrences in order.
func MonitorOrigins(origins []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, o := range origins {
		if strings.Contains(o, "*") {
			continue
		}
		u, err := url.Parse(o)
		if err != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery ||
			strings.Contains(u.Hostname(), "%") {
			continue
		}
		n, ok := urlOrigin(o)
		if !ok || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// MonitorRedirectAllowed reports whether a monitor on reqOrigin may follow a
// redirect to target: RedirectAllowed, whose exact entries are compared as
// listed, or target's origin among the project's MonitorOrigins, so an origin
// listed in another spelling (a capital, the default port) still counts as
// itself.
func (s *Snapshot) MonitorRedirectAllowed(projectID int64, target, reqOrigin string) bool {
	if s.RedirectAllowed(projectID, target, reqOrigin) {
		return true
	}
	p := s.Project(projectID)
	if p == nil {
		return false
	}
	origin, ok := urlOrigin(target)
	if !ok {
		return false
	}
	// Compared with the hosts in their A-label form, so a redirect between
	// the Unicode and the xn-- spelling of one host is the same site.
	origin = asciiOrigin(origin)
	for _, o := range MonitorOrigins(p.AllowedOrigins) {
		if asciiOrigin(o) == origin {
			return true
		}
	}
	return false
}

// asciiOrigin is a normalized origin (see urlOrigin) with its host in
// A-label form; one idna cannot convert is returned as it is.
func asciiOrigin(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return origin
	}
	host, err := idna.Lookup.ToASCII(u.Hostname())
	if err != nil {
		return origin
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p := u.Port(); p != "" {
		host += ":" + p
	}
	return u.Scheme + "://" + host
}

// MonitorSpec is a change to a project's monitor: a nil field keeps the
// current value.
type MonitorSpec struct {
	Enabled          *bool
	Origin, Contains *string
}

// Monitor reads a project's monitor; a project without one reads as the
// zero Monitor carrying its id.
func (o *Ops) Monitor(ctx context.Context, projectID int64) (store.Monitor, error) {
	if err := o.requireProject(ctx, projectID); err != nil {
		return store.Monitor{}, err
	}
	return o.St.GetMonitor(ctx, projectID)
}

// SetMonitor merges spec over the project's monitor and writes it. The
// origin must be one of MonitorOrigins of the project's allowed origins;
// enabling with none set picks the only eligible one.
func (o *Ops) SetMonitor(ctx context.Context, actor string, projectID int64, spec MonitorSpec) (store.Monitor, error) {
	p := o.Reg.Snapshot(ctx).Project(projectID)
	if p == nil {
		return store.Monitor{}, fmt.Errorf("unknown project %d: %w", projectID, ErrNotFound)
	}
	if p.Archived {
		return store.Monitor{}, fmt.Errorf("%w: project %d is archived; restore it first", ErrInvalid, projectID)
	}
	cur, err := o.St.GetMonitor(ctx, projectID)
	if err != nil {
		return store.Monitor{}, err
	}
	next := store.MonitorSettings{Enabled: cur.Enabled, Origin: cur.Origin, Contains: cur.Contains}
	if spec.Enabled != nil {
		next.Enabled = *spec.Enabled
	}
	eligible := MonitorOrigins(p.AllowedOrigins)
	if spec.Origin != nil {
		want := strings.TrimSpace(*spec.Origin)
		if want != "" {
			// Through MonitorOrigins, so a path is refused, not dropped.
			n := MonitorOrigins([]string{want})
			if len(n) != 1 || !slices.Contains(eligible, n[0]) {
				return store.Monitor{}, fmt.Errorf("%w: %q is not an exact http(s) origin in the project's allowed_origins", ErrInvalid, want)
			}
			want = n[0]
		}
		next.Origin = want
	}
	if spec.Contains != nil {
		c := strings.TrimSpace(*spec.Contains)
		if utf8.RuneCountInString(c) > maxMonitorContains {
			return store.Monitor{}, fmt.Errorf("%w: contains is longer than %d characters", ErrInvalid, maxMonitorContains)
		}
		next.Contains = c
	}
	if next.Enabled && next.Origin == "" {
		if len(eligible) == 0 {
			return store.Monitor{}, fmt.Errorf("%w: a monitor needs one exact http(s) origin in the project's allowed_origins", ErrInvalid)
		}
		if len(eligible) > 1 {
			return store.Monitor{}, fmt.Errorf("%w: pick one of the project's origins (%s)", ErrInvalid, strings.Join(eligible, ", "))
		}
		next.Origin = eligible[0]
	}
	if next.Enabled && !slices.Contains(eligible, next.Origin) {
		return store.Monitor{}, fmt.Errorf("%w: the monitored origin %q is no longer one of the project's origins; pick one", ErrInvalid, next.Origin)
	}
	// What changed is read off the outcome: an empty origin the only eligible
	// one fills back in changes nothing.
	var changed []string
	if next.Enabled != cur.Enabled {
		changed = append(changed, fmt.Sprintf("enabled=%t", next.Enabled))
	}
	if next.Origin != cur.Origin {
		changed = append(changed, "origin="+next.Origin)
	}
	if next.Contains != cur.Contains {
		changed = append(changed, "contains")
	}
	if len(changed) == 0 {
		return cur, nil
	}
	return o.St.SetMonitor(ctx, projectID, next, store.AuditEntry{
		Actor: actor, Action: "monitor.update", Subject: idSubject(projectID),
		Detail: strings.Join(changed, " ")})
}

// CheckMonitor asks for a check of all three now, which the monitor's runner
// serves: it may be another process, sharing only the database. The result
// comes later, on the monitor (CheckFinishedAt reaches CheckRequestedAt).
func (o *Ops) CheckMonitor(ctx context.Context, actor string, projectID int64) (store.Monitor, error) {
	p := o.Reg.Snapshot(ctx).Project(projectID)
	if p == nil {
		return store.Monitor{}, fmt.Errorf("unknown project %d: %w", projectID, ErrNotFound)
	}
	if p.Archived {
		return store.Monitor{}, fmt.Errorf("%w: project %d is archived; restore it first", ErrInvalid, projectID)
	}
	m, err := o.St.GetMonitor(ctx, projectID)
	if err != nil {
		return store.Monitor{}, err
	}
	if !m.Enabled {
		return store.Monitor{}, fmt.Errorf("%w: turn the monitor on first", ErrInvalid)
	}
	if m.CheckRequestedAt > m.CheckFinishedAt {
		return store.Monitor{}, fmt.Errorf("%w: a check is already running", ErrBusy)
	}
	now := o.now().UTC()
	if last, err := time.Parse(time.RFC3339, m.CheckRequestedAt); err == nil {
		if wait := monitorCheckCooldown - now.Sub(last); wait > 0 {
			return store.Monitor{}, fmt.Errorf("%w: checked moments ago; try again in %d s", ErrBusy, int(wait.Round(time.Second)/time.Second))
		}
	}
	if err := o.St.RequestMonitorCheck(ctx, projectID, now.Format(time.RFC3339), store.AuditEntry{
		Actor: actor, Action: "monitor.check", Subject: idSubject(projectID)}); err != nil {
		return store.Monitor{}, err
	}
	return o.St.GetMonitor(ctx, projectID)
}
