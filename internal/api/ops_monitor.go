package api

import (
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
)

// A project's availability monitors, one per origin: their settings and last
// certificate readings, and the main domains' renewal dates they share
// (docs/twillingate.md, Monitoring). The checks themselves are run by the server;
// these tools read and edit the settings, and ask for a check, which the server's
// runner serves.

type updateMonitorIn struct {
	ProjectID  int64   `json:"project_id" jsonschema:"project id; call list_projects first"`
	Origin     string  `json:"origin" jsonschema:"the monitor's origin: one of list_monitors' origins, the project's exact http:// or https:// allowed origins"`
	Enabled    *bool   `json:"enabled,omitempty" jsonschema:"turn this origin's monitor on or off; omit to keep"`
	Contains   *string `json:"contains,omitempty" jsonschema:"text the page must contain (at most 200 characters, case-sensitive); an empty string clears it; omit to keep"`
	RedirectTo *string `json:"redirect_to,omitempty" jsonschema:"another of the project's exact http(s) origins the check must be redirected to (https://www.example.com expecting https://example.com); an empty string clears it; omit to keep"`
}

type checkMonitorIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Origin    string `json:"origin" jsonschema:"the origin of an enabled monitor"`
}

type monitorCertOut struct {
	ExpiresAt string `json:"expires_at" jsonschema:"RFC 3339 UTC; empty when unknown"`
	Issuer    string `json:"issuer"`
	Valid     bool   `json:"valid"`
	Error     string `json:"error"`
	CheckedAt string `json:"checked_at"`
}

type monitorCheckOut struct {
	RequestedAt string `json:"requested_at" jsonschema:"when the last manual check was asked for, RFC 3339 UTC; empty when none"`
	FinishedAt  string `json:"finished_at" jsonschema:"when it ended; empty while the check runs, and before requested_at only on a row from an older version"`
}

type monitorLastOut struct {
	CheckedAt string `json:"checked_at" jsonschema:"when the latest availability check, scheduled or manual, ended, RFC 3339 UTC; empty before the first"`
	Up        bool   `json:"up"`
	Status    int    `json:"status" jsonschema:"the final HTTP status; 0 without a response"`
	Reason    string `json:"reason" jsonschema:"why it was down, the same vocabulary as an uptime row's; empty when up"`
	Error     string `json:"error" jsonschema:"the failure's text; empty when up"`
	MS        int    `json:"ms" jsonschema:"response time in milliseconds"`
}

type monitorOut struct {
	Origin     string          `json:"origin"`
	Enabled    bool            `json:"enabled"`
	CheckEvery int             `json:"check_every_seconds" jsonschema:"how often this monitor is actually checked: interval_seconds times the project's enabled monitors; 0 when off, the project is archived or monitoring is paused"`
	Contains   string          `json:"contains"`
	RedirectTo string          `json:"redirect_to" jsonschema:"the origin the check must be redirected to; empty when the origin may answer itself"`
	Redirects  int             `json:"redirects" jsonschema:"redirect hops the last check that got an answer followed; 0 when none or not checked yet"`
	Cert       monitorCertOut  `json:"cert"`
	Last       monitorLastOut  `json:"last" jsonschema:"the latest availability check, scheduled or manual"`
	Check      monitorCheckOut `json:"check" jsonschema:"the last manual check's request (check_monitor); its result is in last"`
}

type monitorDomainOut struct {
	Domain    string   `json:"domain" jsonschema:"the main domain, as registered"`
	Origins   []string `json:"origins" jsonschema:"the enabled monitors' origins on it, which share this reading"`
	ExpiresAt string   `json:"expires_at" jsonschema:"YYYY-MM-DD; empty when unknown"`
	CheckedAt string   `json:"checked_at"`
}

type monitorsOut struct {
	IntervalSeconds int                `json:"interval_seconds" jsonschema:"seconds between the project's availability checks, set by MONITOR_INTERVAL; its enabled monitors take turns, see check_every_seconds"`
	TimeoutSeconds  int                `json:"timeout_seconds" jsonschema:"seconds a site has to answer a check before it counts as down, set by MONITOR_TIMEOUT"`
	Paused          bool               `json:"paused" jsonschema:"true when MONITOR_DISABLED has monitoring off: no check runs, and check_monitor is refused"`
	Monitors        []monitorOut       `json:"monitors" jsonschema:"every origin a monitor may use, in the project's order; one without settings reads as off"`
	Domains         []monitorDomainOut `json:"domains" jsonschema:"the main domains of the enabled monitors, by name"`
}

func (h *host) monitorsOut(l manage.MonitorList) monitorsOut {
	interval := int(h.monitorInterval.Seconds())
	every := int(l.CheckEvery(h.monitorInterval).Seconds())
	out := monitorsOut{IntervalSeconds: interval, TimeoutSeconds: int(h.monitorTimeout.Seconds()),
		Paused:   h.ops.MonitorPaused,
		Monitors: make([]monitorOut, 0, len(l.Monitors)), Domains: make([]monitorDomainOut, 0, len(l.Domains))}
	for _, m := range l.Monitors {
		checkEvery := 0
		if m.Enabled {
			checkEvery = every
		}
		out.Monitors = append(out.Monitors, monitorOut{Origin: m.Origin, Enabled: m.Enabled, CheckEvery: checkEvery, Contains: m.Expect.Contains, RedirectTo: m.Expect.RedirectTo, Redirects: m.Redirects,
			Cert: monitorCertOut{ExpiresAt: m.CertExpiresAt, Issuer: m.CertIssuer, Valid: m.CertValid,
				Error: m.CertError, CheckedAt: m.CertCheckedAt},
			Last: monitorLastOut{CheckedAt: m.Last.CheckedAt, Up: m.Last.Up, Status: m.Last.Status,
				Reason: m.Last.Reason, Error: m.Last.Error, MS: m.Last.MS},
			Check: monitorCheckOut{RequestedAt: m.CheckRequestedAt, FinishedAt: m.CheckFinishedAt}})
	}
	for _, d := range l.Domains {
		out.Domains = append(out.Domains, monitorDomainOut{Domain: d.Domain, Origins: d.Origins, ExpiresAt: d.ExpiresAt, CheckedAt: d.CheckedAt})
	}
	return out
}

func (h *host) listMonitors(ctx context.Context, in idIn) (monitorsOut, error) {
	l, err := h.ops.ListMonitors(ctx, in.ProjectID)
	if err != nil {
		return monitorsOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	return h.monitorsOut(l), nil
}

func (h *host) updateMonitor(ctx context.Context, in updateMonitorIn) (monitorsOut, error) {
	l, err := h.ops.SetMonitor(ctx, actorFrom(ctx), in.ProjectID, in.Origin,
		manage.MonitorSpec{Enabled: in.Enabled, Expect: manage.ExpectSpec{Contains: in.Contains, RedirectTo: in.RedirectTo}})
	if err != nil {
		return monitorsOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	return h.monitorsOut(l), nil
}

func (h *host) checkMonitor(ctx context.Context, in checkMonitorIn) (monitorsOut, error) {
	l, err := h.ops.CheckMonitor(ctx, actorFrom(ctx), in.ProjectID, in.Origin)
	if err != nil {
		return monitorsOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	return h.monitorsOut(l), nil
}

func (h *host) registerMonitor(r *registrar) {
	expose(r, spec{Name: "list_monitors", Annotations: ro, Method: "GET", Path: "/api/projects/{project_id}/monitors",
		Description: "A project's availability monitors, one per origin: every exact http(s) origin of the project, on or off (origin, enabled, check_every_seconds, how often it is actually checked, contains, the text the page must contain, redirect_to, the origin the check must be redirected to, redirects, the last certificate reading in cert: expires_at, issuer, valid, error, checked_at, the latest availability check, scheduled or manual, in last: checked_at, up, status, reason, error, ms, and the last manual check's request in check: requested_at, finished_at); domains, the renewal date of each enabled monitor's main domain (domain, origins, expires_at, checked_at); interval_seconds (MONITOR_INTERVAL: the project gets one availability check per interval, its enabled monitors taking turns), timeout_seconds (MONITOR_TIMEOUT) and paused (monitoring is off on this install). Empty strings are unknown. The checks themselves are read from the checks table of query."},
		h.listMonitors)
	expose(r, spec{Name: "update_monitor", Annotations: idem, Method: "PATCH", Path: "/api/projects/{project_id}/monitors",
		Description: "Change one origin's availability monitor: enabled, contains (text the page must contain) and redirect_to (another of the project's exact origins the check must end at, for a www origin that should redirect to the bare domain, or the other way round). origin is required, one of the project's exact http(s) origins; fields you omit are left unchanged. Returns the same as list_monitors."},
		h.updateMonitor)
	expose(r, spec{Name: "check_monitor", Annotations: write, Method: "POST", Path: "/api/projects/{project_id}/monitors/check",
		Description: "Ask for a check of one enabled monitor now: its origin's availability, certificate and main domain together, outside the schedule. Returns the same as list_monitors right away, before the check ran; call list_monitors until that monitor's check.finished_at is not empty and at or past check.requested_at (it is empty while the check runs, and last holds the previous result until then), then read last.up, status, reason, error and ms, and the fresh cert and domain; last.checked_at still before check.requested_at means the check did not run. Refused while one is pending on that monitor and within 60 seconds of its last request."},
		h.checkMonitor)
}
