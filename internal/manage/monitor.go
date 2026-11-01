package manage

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// maxMonitorContains caps the text a monitored page must contain.
const maxMonitorContains = 200

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
	return ok && slices.Contains(MonitorOrigins(p.AllowedOrigins), origin)
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
	var changed []string
	if spec.Enabled != nil && *spec.Enabled != cur.Enabled {
		next.Enabled = *spec.Enabled
		changed = append(changed, fmt.Sprintf("enabled=%t", next.Enabled))
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
		if want != cur.Origin {
			next.Origin = want
			changed = append(changed, "origin="+want)
		}
	}
	if spec.Contains != nil {
		c := strings.TrimSpace(*spec.Contains)
		if utf8.RuneCountInString(c) > maxMonitorContains {
			return store.Monitor{}, fmt.Errorf("%w: contains is longer than %d characters", ErrInvalid, maxMonitorContains)
		}
		if c != cur.Contains {
			next.Contains = c
			changed = append(changed, "contains")
		}
	}
	if next.Enabled && next.Origin == "" {
		if len(eligible) == 0 {
			return store.Monitor{}, fmt.Errorf("%w: a monitor needs one exact http(s) origin in the project's allowed_origins", ErrInvalid)
		}
		if len(eligible) > 1 {
			return store.Monitor{}, fmt.Errorf("%w: pick one of the project's origins (%s)", ErrInvalid, strings.Join(eligible, ", "))
		}
		next.Origin = eligible[0]
		changed = append(changed, "origin="+next.Origin)
	}
	if next.Enabled && !slices.Contains(eligible, next.Origin) {
		return store.Monitor{}, fmt.Errorf("%w: the monitored origin %q is no longer one of the project's origins; pick one", ErrInvalid, next.Origin)
	}
	if len(changed) == 0 {
		return cur, nil
	}
	return o.St.SetMonitor(ctx, projectID, next, store.AuditEntry{
		Actor: actor, Action: "monitor.update", Subject: idSubject(projectID),
		Detail: strings.Join(changed, " ")})
}
