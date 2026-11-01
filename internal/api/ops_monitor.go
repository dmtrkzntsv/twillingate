package api

import (
	"context"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The availability monitor's settings and last certificate and domain
// readings (docs/twillingate.md, Monitoring). The checks themselves are
// run by the server; these tools only read and edit the settings.

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

type monitorOut struct {
	ProjectID       int64            `json:"project_id"`
	Enabled         bool             `json:"enabled"`
	Origin          string           `json:"origin"`
	Contains        string           `json:"contains"`
	EligibleOrigins []string         `json:"eligible_origins" jsonschema:"the project's allowed origins a monitor may use: exact http:// or https:// ones"`
	IntervalSeconds int              `json:"interval_seconds" jsonschema:"seconds between checks, set by MONITOR_INTERVAL"`
	Redirects       int              `json:"redirects" jsonschema:"redirect hops the last check that got an answer followed; 0 when none or not checked yet"`
	Cert            monitorCertOut   `json:"cert"`
	Domain          monitorDomainOut `json:"domain"`
}

func (h *host) monitorOut(ctx context.Context, m store.Monitor) monitorOut {
	eligible := []string{}
	if p := h.reg.Snapshot(ctx).Project(m.ProjectID); p != nil {
		eligible = append(eligible, manage.MonitorOrigins(p.AllowedOrigins)...)
	}
	return monitorOut{ProjectID: m.ProjectID, Enabled: m.Enabled, Origin: m.Origin, Contains: m.Contains,
		EligibleOrigins: eligible, IntervalSeconds: int(h.monitorInterval.Seconds()), Redirects: m.Redirects,
		Cert: monitorCertOut{ExpiresAt: m.CertExpiresAt, Issuer: m.CertIssuer, Valid: m.CertValid,
			Error: m.CertError, CheckedAt: m.CertCheckedAt},
		Domain: monitorDomainOut{ExpiresAt: m.DomainExpiresAt, CheckedAt: m.DomainCheckedAt}}
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

func (h *host) registerMonitor(r *registrar) {
	expose(r, spec{Name: "get_monitor", Annotations: ro, Method: "GET", Path: "/api/projects/{project_id}/monitor",
		Description: "A project's availability monitor: enabled, the origin checked, the text the page must contain, eligible_origins (the project's exact http(s) origins a monitor may use), interval_seconds (set by MONITOR_INTERVAL), and the last certificate reading (expires_at, issuer, valid, error, checked_at) and domain renewal date (expires_at, checked_at); empty strings are unknown. The checks themselves are read from the checks table of query."},
		h.getMonitor)
	expose(r, spec{Name: "update_monitor", Annotations: idem, Method: "PATCH", Path: "/api/projects/{project_id}/monitor",
		Description: "Change a project's availability monitor: enabled, origin (one of eligible_origins) and contains (text the page must contain). Fields you omit are left unchanged; enabling with no origin set picks the project's only eligible one. Returns the monitor as get_monitor."},
		h.updateMonitor)
}
