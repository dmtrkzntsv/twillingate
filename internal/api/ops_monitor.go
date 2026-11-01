package api

import (
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The availability monitor's settings and last certificate and domain
// readings (docs/twillingate.md, Monitoring). The checks themselves are
// run by the server; these tools read and edit the settings, and ask for a
// check, which the server's runner serves.

type updateMonitorIn struct {
	ProjectID int64   `json:"project_id" jsonschema:"project id; call list_projects first"`
	Enabled   *bool   `json:"enabled,omitempty" jsonschema:"turn the monitor on or off; omit to keep"`
	Origin    *string `json:"origin,omitempty" jsonschema:"the origin to check, one of eligible_origins; omit to keep. Enabling with none set picks the project's only eligible origin"`
	Contains  *string `json:"contains,omitempty" jsonschema:"text the page must contain (at most 200 characters, case-sensitive); an empty string clears it; omit to keep"`
}

type monitorCertOut struct {
	ExpiresAt string `json:"expires_at" jsonschema:"RFC 3339 UTC; empty when unknown"`
	Issuer    string `json:"issuer"`
	Valid     bool   `json:"valid"`
	Error     string `json:"error"`
	CheckedAt string `json:"checked_at"`
}

type monitorDomainOut struct {
	ExpiresAt string `json:"expires_at" jsonschema:"YYYY-MM-DD; empty when unknown"`
	CheckedAt string `json:"checked_at"`
}

type monitorCheckOut struct {
	RequestedAt string `json:"requested_at" jsonschema:"when the last manual check was asked for, RFC 3339 UTC; empty when none"`
	FinishedAt  string `json:"finished_at" jsonschema:"when it ended; the check is still running while this is before requested_at"`
	Up          bool   `json:"up"`
	Status      int    `json:"status" jsonschema:"the final HTTP status; 0 without a response"`
	Reason      string `json:"reason" jsonschema:"why it was down, the same vocabulary as an uptime row's; empty when up or when it did not run"`
	Error       string `json:"error" jsonschema:"the failure's text; \"the check did not run\" when it was never served"`
	MS          int    `json:"ms" jsonschema:"response time in milliseconds"`
}

type monitorOut struct {
	ProjectID       int64            `json:"project_id"`
	Enabled         bool             `json:"enabled"`
	Origin          string           `json:"origin"`
	Contains        string           `json:"contains"`
	EligibleOrigins []string         `json:"eligible_origins" jsonschema:"the project's allowed origins a monitor may use: exact http:// or https:// ones"`
	IntervalSeconds int              `json:"interval_seconds" jsonschema:"seconds between checks, set by MONITOR_INTERVAL"`
	TimeoutSeconds  int              `json:"timeout_seconds" jsonschema:"seconds a site has to answer a check before it counts as down, set by MONITOR_TIMEOUT"`
	Redirects       int              `json:"redirects" jsonschema:"redirect hops the last check that got an answer followed; 0 when none or not checked yet"`
	Cert            monitorCertOut   `json:"cert"`
	Domain          monitorDomainOut `json:"domain"`
	Check           monitorCheckOut  `json:"check" jsonschema:"the last manual check (check_monitor)"`
}

func (h *host) monitorOut(ctx context.Context, m store.Monitor) monitorOut {
	eligible := []string{}
	if p := h.reg.Snapshot(ctx).Project(m.ProjectID); p != nil {
		eligible = append(eligible, manage.MonitorOrigins(p.AllowedOrigins)...)
	}
	return monitorOut{ProjectID: m.ProjectID, Enabled: m.Enabled, Origin: m.Origin, Contains: m.Contains,
		EligibleOrigins: eligible, IntervalSeconds: int(h.monitorInterval.Seconds()), TimeoutSeconds: int(h.monitorTimeout.Seconds()), Redirects: m.Redirects,
		Cert: monitorCertOut{ExpiresAt: m.CertExpiresAt, Issuer: m.CertIssuer, Valid: m.CertValid,
			Error: m.CertError, CheckedAt: m.CertCheckedAt},
		Domain: monitorDomainOut{ExpiresAt: m.DomainExpiresAt, CheckedAt: m.DomainCheckedAt},
		Check: monitorCheckOut{RequestedAt: m.CheckRequestedAt, FinishedAt: m.CheckFinishedAt, Up: m.CheckUp,
			Status: m.CheckStatus, Reason: m.CheckReason, Error: m.CheckError, MS: m.CheckMS}}
}

func (h *host) getMonitor(ctx context.Context, in idIn) (monitorOut, error) {
	m, err := h.ops.Monitor(ctx, in.ProjectID)
	if err != nil {
		return monitorOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	return h.monitorOut(ctx, m), nil
}

func (h *host) updateMonitor(ctx context.Context, in updateMonitorIn) (monitorOut, error) {
	m, err := h.ops.SetMonitor(ctx, actorFrom(ctx), in.ProjectID,
		manage.MonitorSpec{Enabled: in.Enabled, Origin: in.Origin, Contains: in.Contains})
	if err != nil {
		return monitorOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	return h.monitorOut(ctx, m), nil
}

func (h *host) checkMonitor(ctx context.Context, in idIn) (monitorOut, error) {
	m, err := h.ops.CheckMonitor(ctx, actorFrom(ctx), in.ProjectID)
	if err != nil {
		return monitorOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	return h.monitorOut(ctx, m), nil
}

func (h *host) registerMonitor(r *registrar) {
	expose(r, spec{Name: "get_monitor", Annotations: ro, Method: "GET", Path: "/api/projects/{project_id}/monitor",
		Description: "A project's availability monitor: enabled, the origin checked, the text the page must contain, eligible_origins (the project's exact http(s) origins a monitor may use), interval_seconds (set by MONITOR_INTERVAL), timeout_seconds (set by MONITOR_TIMEOUT), and the last certificate reading (expires_at, issuer, valid, error, checked_at) domain renewal date (expires_at, checked_at) and manual check (check: requested_at, finished_at, up, status, reason, error, ms); empty strings are unknown. The checks themselves are read from the checks table of query."},
		h.getMonitor)
	expose(r, spec{Name: "update_monitor", Annotations: idem, Method: "PATCH", Path: "/api/projects/{project_id}/monitor",
		Description: "Change a project's availability monitor: enabled, origin (one of eligible_origins) and contains (text the page must contain). Fields you omit are left unchanged; enabling with no origin set picks the project's only eligible one. Returns the monitor as get_monitor."},
		h.updateMonitor)
	expose(r, spec{Name: "check_monitor", Annotations: write, Method: "POST", Path: "/api/projects/{project_id}/monitor/check",
		Description: "Ask for a check of a project's enabled monitor now: availability, certificate and domain together, outside its schedule. Returns the monitor right away, before the check ran; call get_monitor until check.finished_at is at or past check.requested_at, then read check.up, status, reason, error and ms, and the fresh cert and domain. Refused while one is pending and within 60 seconds of the last request."},
		h.checkMonitor)
}
