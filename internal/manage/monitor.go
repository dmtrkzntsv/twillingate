package manage

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"github.com/dmtrkzntsv/twillingate/internal/shared/maindomain"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const (
	// maxMonitorContains caps the text a monitored page must contain.
	maxMonitorContains = 200
	// monitorCheckCooldown is the least time between manual checks of a
	// monitor, whatever the interval: it protects the site and the RDAP
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

// SameOrigin reports whether URL target is at origin: the same scheme, host
// and port, however either is spelled (a capital, the default port, the
// Unicode or the xn-- form of the host).
func SameOrigin(target, origin string) bool {
	a, ok := urlOrigin(target)
	if !ok {
		return false
	}
	b, ok := urlOrigin(origin)
	return ok && asciiOrigin(a) == asciiOrigin(b)
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

// MonitorSpec is a change to a monitor: a nil field keeps the current value.
type MonitorSpec struct {
	Enabled  *bool
	Contains *string
	// RedirectTo is the origin the check must end up at: another of the
	// project's monitor origins, "" for none.
	RedirectTo *string
}

// MonitorDomain is the last reading of a main domain, and the enabled monitors'
// origins on it, which share it. Empty strings are unknown.
type MonitorDomain struct {
	Domain               string
	Origins              []string
	ExpiresAt, CheckedAt string
}

// MonitorList is what a project's monitoring reads as.
type MonitorList struct {
	// Monitors holds every eligible origin in the order the project lists
	// them, whether or not it has a row: an origin with none reads as off.
	Monitors []store.Monitor
	// Domains holds the main domains of the enabled monitors, by name.
	Domains []MonitorDomain
	// Idle is whether nothing of it is checked: the project is archived or
	// monitoring is paused on this install.
	Idle bool
}

// Enabled counts the monitors that are on.
func (l MonitorList) Enabled() int {
	n := 0
	for _, m := range l.Monitors {
		if m.Enabled {
			n++
		}
	}
	return n
}

// CheckEvery is how often each enabled monitor is checked, given
// MONITOR_INTERVAL: the project's one check per interval goes round them.
// It is 0 when nothing is checked (Idle).
func (l MonitorList) CheckEvery(interval time.Duration) time.Duration {
	if l.Idle {
		return 0
	}
	return interval * time.Duration(max(l.Enabled(), 1))
}

// Monitor is origin's entry in the list (an exact match).
func (l MonitorList) Monitor(origin string) (store.Monitor, bool) {
	for _, m := range l.Monitors {
		if m.Origin == origin {
			return m, true
		}
	}
	return store.Monitor{}, false
}

// ListMonitors reads a project's monitoring.
func (o *Ops) ListMonitors(ctx context.Context, projectID int64) (MonitorList, error) {
	p := o.Reg.Snapshot(ctx).Project(projectID)
	if p == nil {
		return MonitorList{}, fmt.Errorf("unknown project %d: %w", projectID, ErrNotFound)
	}
	rows, err := o.St.ListMonitors(ctx, projectID)
	if err != nil {
		return MonitorList{}, err
	}
	readings, err := o.St.ListMonitorDomains(ctx, projectID)
	if err != nil {
		return MonitorList{}, err
	}
	l := MonitorList{Idle: p.Archived || o.MonitorPaused}
	byDomain := map[string]*MonitorDomain{}
	for _, origin := range MonitorOrigins(p.AllowedOrigins) {
		m := store.Monitor{ProjectID: projectID, Origin: origin}
		if i := slices.IndexFunc(rows, func(r store.Monitor) bool { return r.Origin == origin }); i >= 0 {
			m = rows[i]
		}
		l.Monitors = append(l.Monitors, m)
		if !m.Enabled {
			continue
		}
		name := maindomain.OfOrigin(origin)
		d := byDomain[name]
		if d == nil {
			d = &MonitorDomain{Domain: name}
			byDomain[name] = d
			if i := slices.IndexFunc(readings, func(r store.MonitorDomain) bool { return r.Domain == name }); i >= 0 {
				d.ExpiresAt, d.CheckedAt = readings[i].ExpiresAt, readings[i].CheckedAt
			}
		}
		d.Origins = append(d.Origins, origin)
	}
	for _, name := range slices.Sorted(maps.Keys(byDomain)) {
		l.Domains = append(l.Domains, *byDomain[name])
	}
	return l, nil
}

// liveMonitorOrigin is origin in its normalized form, refused when project
// projectID is unknown or archived or origin is not one a monitor of it may use.
func (o *Ops) liveMonitorOrigin(ctx context.Context, projectID int64, origin string) (string, error) {
	p := o.Reg.Snapshot(ctx).Project(projectID)
	if p == nil {
		return "", fmt.Errorf("unknown project %d: %w", projectID, ErrNotFound)
	}
	if p.Archived {
		return "", fmt.Errorf("%w: project %d is archived; restore it first", ErrInvalid, projectID)
	}
	// Through MonitorOrigins, so a path is refused, not dropped.
	n := MonitorOrigins([]string{strings.TrimSpace(origin)})
	if len(n) != 1 || !slices.Contains(MonitorOrigins(p.AllowedOrigins), n[0]) {
		return "", fmt.Errorf("%w: %q is not an exact http(s) origin in the project's allowed_origins", ErrInvalid, origin)
	}
	return n[0], nil
}

// monitorRow is origin's row, or the row it would be: off.
func (o *Ops) monitorRow(ctx context.Context, projectID int64, origin string) (store.Monitor, error) {
	rows, err := o.St.ListMonitors(ctx, projectID)
	if err != nil {
		return store.Monitor{}, err
	}
	if i := slices.IndexFunc(rows, func(r store.Monitor) bool { return r.Origin == origin }); i >= 0 {
		return rows[i], nil
	}
	return store.Monitor{ProjectID: projectID, Origin: origin}, nil
}

// SetMonitor merges spec over origin's monitor and writes it. The origin must
// be one of MonitorOrigins of the project's allowed origins. A change to nothing
// writes nothing.
func (o *Ops) SetMonitor(ctx context.Context, actor string, projectID int64, origin string, spec MonitorSpec) (MonitorList, error) {
	origin, err := o.liveMonitorOrigin(ctx, projectID, origin)
	if err != nil {
		return MonitorList{}, err
	}
	next := store.MonitorSettings{Enabled: spec.Enabled}
	if spec.Contains != nil {
		c := strings.TrimSpace(*spec.Contains)
		if utf8.RuneCountInString(c) > maxMonitorContains {
			return MonitorList{}, fmt.Errorf("%w: contains is longer than %d characters", ErrInvalid, maxMonitorContains)
		}
		next.Contains = &c
	}
	if spec.RedirectTo != nil {
		to, err := o.monitorRedirectTo(ctx, projectID, origin, *spec.RedirectTo)
		if err != nil {
			return MonitorList{}, err
		}
		next.RedirectTo = &to
	}
	// The store merges next over the row inside its transaction, builds the
	// audit detail from what changed and writes nothing when nothing does.
	if _, err := o.St.SetMonitor(ctx, projectID, origin, next, MonitorOrigins, store.AuditEntry{
		Actor: actor, Action: "monitor.update", Subject: idSubject(projectID)}); err != nil {
		return MonitorList{}, err
	}
	return o.ListMonitors(ctx, projectID)
}

// monitorRedirectTo is to in its normalized form, "" for none, refused unless
// it is one of the project's monitor origins other than origin itself.
func (o *Ops) monitorRedirectTo(ctx context.Context, projectID int64, origin, to string) (string, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return "", nil
	}
	p := o.Reg.Snapshot(ctx).Project(projectID)
	if p == nil {
		return "", fmt.Errorf("unknown project %d: %w", projectID, ErrNotFound)
	}
	n := MonitorOrigins([]string{to})
	if len(n) != 1 || !slices.Contains(MonitorOrigins(p.AllowedOrigins), n[0]) {
		return "", fmt.Errorf("%w: redirect_to %q is not an exact http(s) origin in the project's allowed_origins", ErrInvalid, to)
	}
	if n[0] == origin {
		return "", fmt.Errorf("%w: redirect_to is the monitor's own origin; leave it empty for a site that answers itself", ErrInvalid)
	}
	return n[0], nil
}

// CheckMonitor asks for a check of origin's monitor now, all three readings,
// which the monitor's runner serves: it may be another process, sharing only the
// database. The result comes later, on the monitor (CheckFinishedAt reaches
// CheckRequestedAt).
func (o *Ops) CheckMonitor(ctx context.Context, actor string, projectID int64, origin string) (MonitorList, error) {
	if o.MonitorPaused {
		return MonitorList{}, fmt.Errorf("%w: monitoring is paused on this install", ErrInvalid)
	}
	origin, err := o.liveMonitorOrigin(ctx, projectID, origin)
	if err != nil {
		return MonitorList{}, err
	}
	m, err := o.monitorRow(ctx, projectID, origin)
	if err != nil {
		return MonitorList{}, err
	}
	if !m.Enabled {
		return MonitorList{}, fmt.Errorf("%w: turn the monitor on first", ErrInvalid)
	}
	if m.CheckPending() {
		return MonitorList{}, fmt.Errorf("%w: a check is already running", ErrBusy)
	}
	now := o.now().UTC()
	if last, err := time.Parse(time.RFC3339, m.CheckRequestedAt); err == nil {
		if wait := monitorCheckCooldown - now.Sub(last); wait > 0 {
			return MonitorList{}, fmt.Errorf("%w: checked moments ago; try again in %d s", ErrBusy, int(wait.Round(time.Second)/time.Second))
		}
	}
	if err := o.St.RequestMonitorCheck(ctx, projectID, origin, now.Format(time.RFC3339), monitorCheckCooldown, store.AuditEntry{
		Actor: actor, Action: "monitor.check", Subject: idSubject(projectID), Detail: origin}); err != nil {
		return MonitorList{}, err
	}
	return o.ListMonitors(ctx, projectID)
}
