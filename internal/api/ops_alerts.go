package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/alerts"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/google/jsonschema-go/jsonschema"
)

// Metric alerts (docs/twillingate.md, Alerts): their definitions, the
// audited operations on them, the backtest and the history. The runner
// that evaluates them lives in the process that writes the event files;
// these tools save, read and ask for a check, which it serves.

// alertReadIO names the number an alert reads, in and out.
type alertReadIO struct {
	Metric string `json:"metric,omitempty" jsonschema:"a stat: the metric read, by name (op, or op_column: count, actors, avg_value); required when the stat has several"`
	Volume string `json:"volume,omitempty" jsonschema:"a stat whose metric is a rate or an average (ratio, bounce_rate, avg, mean, percentile): a second metric counting the same rows (count, actors, users, groups, weight), which min_volume measures; required for those"`
	Step   string `json:"step,omitempty" jsonschema:"a funnel: the step read, by label; not the first"`
	From   string `json:"from,omitempty" jsonschema:"a funnel: the step it is divided by, start (the first, the default) or previous (the one before it)"`
}

func (r alertReadIO) store() store.AlertRead {
	return store.AlertRead{Metric: r.Metric, Volume: r.Volume, Step: r.Step, From: r.From}
}

// alertConditionIO is the line an alert holds its number against, in
// and out.
type alertConditionIO struct {
	Type      string   `json:"type" jsonschema:"threshold, change or silence"`
	Op        string   `json:"op,omitempty" jsonschema:"threshold: below or above"`
	Value     *float64 `json:"value,omitempty" jsonschema:"threshold: the line, in the read's own unit (a conversion or a bounce rate is 0-1: 0.03 for 3%)"`
	Direction string   `json:"direction,omitempty" jsonschema:"change: drop, rise or either"`
	Percent   float64  `json:"percent,omitempty" jsonschema:"change: the relative change that breaches, above 0 and at most 1000 (4.0% to 2.8% is a 30% drop)"`
	Against   string   `json:"against,omitempty" jsonschema:"change: previous (the window just before) or week_ago (the same days a week earlier; with window_days over 7 it overlaps the window)"`
	Hours     int      `json:"hours,omitempty" jsonschema:"silence: breached when no matching event was received for this many hours, 1 to 720"`
}

func (c alertConditionIO) store() store.AlertCondition {
	return store.AlertCondition{Type: c.Type, Op: c.Op, Value: c.Value, Direction: c.Direction,
		Percent: c.Percent, Against: c.Against, Hours: c.Hours}
}

func conditionIO(c store.AlertCondition) alertConditionIO {
	return alertConditionIO{Type: c.Type, Op: c.Op, Value: c.Value, Direction: c.Direction,
		Percent: c.Percent, Against: c.Against, Hours: c.Hours}
}

// alertSourceIn is an alert's one source: query with read, where (a
// silence alert's events) or template with fields.
type alertSourceIn struct {
	Query    *native.Query     `json:"query,omitempty" jsonschema:"a structured query, as query's (docs://twillingate, Structured queries): a stat or a funnel; its from, to, compare and shape are not kept, the alert decides the days. Give it with read, or give where or template instead"`
	Read     *alertReadIO      `json:"read,omitempty" jsonschema:"with query: the one number the alert reads"`
	Where    *[]native.Filter  `json:"where,omitempty" jsonschema:"a silence alert: the filters on the events it watches (an empty list watches every event); the alert builds its stat on received_at"`
	Template string            `json:"template,omitempty" jsonschema:"event_count, unique_people, conversion or no_events; give it with fields"`
	Fields   map[string]string `json:"fields,omitempty" jsonschema:"the template's fields: event_count {event, path}, unique_people {event, who}, conversion {from_event, from_path, to_event, to_path, people}, no_events {event, path}"`
}

// input is the alerts.Input of s and the rest of a definition.
func (s alertSourceIn) input(name string, c alertConditionIO, windowDays int, minVolume float64) alerts.Input {
	in := alerts.Input{Name: name, Query: s.Query, Template: s.Template, Fields: s.Fields,
		Condition: c.store(), WindowDays: windowDays, MinVolume: minVolume}
	if s.Read != nil {
		r := s.Read.store()
		in.Read = &r
	}
	if s.Where != nil {
		in.Where, in.WhereSet = *s.Where, true
	}
	return in
}

type listAlertsIn struct {
	ProjectID int64 `json:"project_id" jsonschema:"project id; call list_projects first"`
	Archived  bool  `json:"archived,omitempty" jsonschema:"true lists the archived alerts instead of the live ones"`
}

type createAlertIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	Name      string `json:"name" jsonschema:"the alert's name, unique among the project's live alerts, at most 100 characters"`
	alertSourceIn
	Condition  alertConditionIO `json:"condition" jsonschema:"the line: {type: threshold, op, value}, {type: change, direction, percent, against} or {type: silence, hours}"`
	WindowDays int              `json:"window_days,omitempty" jsonschema:"threshold and change: the complete days read, ending yesterday (UTC), 1 to 30; default 1, and 7 for the conversion template; a silence alert takes none"`
	MinVolume  float64          `json:"min_volume,omitempty" jsonschema:"1 or more: under it an evaluation is no_data, never a breach; 0 or absent for none. Measures a funnel's denominator step (the first, or with read.from previous the step before the one read), a rate's volume metric, a change's comparison; a threshold on a count takes none"`
}

type updateAlertIn struct {
	ProjectID    int64             `json:"project_id" jsonschema:"project id; call list_projects first"`
	ID           int64             `json:"id" jsonschema:"the alert's id, from list_alerts"`
	Name         *string           `json:"name,omitempty" jsonschema:"a new name; omit to keep"`
	Condition    *alertConditionIO `json:"condition,omitempty" jsonschema:"a new condition, whole; omit to keep"`
	WindowDays   *int              `json:"window_days,omitempty" jsonschema:"omit to keep"`
	MinVolume    *float64          `json:"min_volume,omitempty" jsonschema:"0 removes it; omit to keep"`
	Paused       *bool             `json:"paused,omitempty" jsonschema:"true pauses the alert until false resumes it; omit to keep"`
	SnoozedUntil optionalTime      `json:"snoozed_until,omitempty" jsonschema:"an RFC 3339 time ahead: no evaluation or notification until then; null wakes it; omit to keep"`
	alertSourceIn
}

func (in updateAlertIn) patch() alerts.Patch {
	p := alerts.Patch{Name: in.Name, WindowDays: in.WindowDays, MinVolume: in.MinVolume, Paused: in.Paused,
		Query: in.Query, Where: in.Where, Fields: in.Fields}
	if in.Condition != nil {
		c := in.Condition.store()
		p.Condition = &c
	}
	if in.Read != nil {
		r := in.Read.store()
		p.Read = &r
	}
	if in.Template != "" {
		p.Template = &in.Template
	}
	if in.SnoozedUntil.set {
		at := in.SnoozedUntil.at
		p.SnoozedUntil = &at
	}
	return p
}

type previewAlertIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	ID        int64  `json:"id,omitempty" jsonschema:"a saved alert to backtest as it stands; or give a definition instead"`
	Name      string `json:"name,omitempty" jsonschema:"ignored, so create_alert's arguments preview as they are"`
	alertSourceIn
	Condition  *alertConditionIO `json:"condition,omitempty" jsonschema:"with a definition: as create_alert's"`
	WindowDays int               `json:"window_days,omitempty" jsonschema:"with a definition: as create_alert's"`
	MinVolume  float64           `json:"min_volume,omitempty" jsonschema:"with a definition: as create_alert's"`
}

type alertIDIn struct {
	ProjectID int64 `json:"project_id" jsonschema:"project id; call list_projects first"`
	ID        int64 `json:"id" jsonschema:"the alert's id, from list_alerts"`
}

type alertHistoryIn struct {
	ProjectID int64  `json:"project_id" jsonschema:"project id; call list_projects first"`
	ID        int64  `json:"id" jsonschema:"the alert's id, from list_alerts"`
	Limit     int    `json:"limit,omitempty" jsonschema:"page size, 1 to 400; default 50"`
	Cursor    string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page; omit for the first"`
}

type alertRecentOut struct {
	At      string   `json:"at"`
	Value   *float64 `json:"value"`
	Outcome string   `json:"outcome"`
}

type alertOut struct {
	ID               int64             `json:"id"`
	ProjectID        int64             `json:"project_id"`
	Name             string            `json:"name"`
	Kind             string            `json:"kind" jsonschema:"stat, funnel or silence; empty when the stored definition no longer holds (the alert is broken)"`
	Query            json.RawMessage   `json:"query" jsonschema:"the structured query the alert runs, defaults written out; a silence alert's is the stat it built from where"`
	Read             alertReadIO       `json:"read"`
	Where            []native.Filter   `json:"where,omitempty" jsonschema:"a silence alert's event filters; absent for the others"`
	Condition        alertConditionIO  `json:"condition"`
	WindowDays       int               `json:"window_days"`
	MinVolume        float64           `json:"min_volume" jsonschema:"0 for none"`
	Template         string            `json:"template,omitempty" jsonschema:"the template it was made from; absent when none"`
	Fields           map[string]string `json:"fields,omitempty" jsonschema:"the template's fields"`
	Paused           bool              `json:"paused"`
	SnoozedUntil     string            `json:"snoozed_until" jsonschema:"RFC 3339 UTC; empty when not snoozed"`
	State            string            `json:"state" jsonschema:"pending, ok, breached, no_data, broken, paused or snoozed"`
	Reason           string            `json:"reason" jsonschema:"why it is broken or no_data; empty otherwise"`
	LastValue        *float64          `json:"last_value" jsonschema:"the last evaluation's value (a silence alert's: hours since the newest matching event); null before the first, or when it had none"`
	LastValueText    string            `json:"last_value_text" jsonschema:"last_value as the read words it (3.4%, 1,240, last event 7h ago); — for none"`
	LastEvaluatedAt  string            `json:"last_evaluated_at" jsonschema:"RFC 3339 UTC; empty before the first evaluation, and again after a reset to pending (a new definition, a pause or snooze, a restore, an archived project)"`
	LastDay          string            `json:"last_day" jsonschema:"the last day a metric alert's window ended on, YYYY-MM-DD; empty for silence and before the first"`
	CheckRequestedAt string            `json:"check_requested_at" jsonschema:"when the last check was asked for (create, a new definition, check_alert), RFC 3339 UTC"`
	CheckPending     bool              `json:"check_pending" jsonschema:"true until an evaluation at or after check_requested_at"`
	Recent           []alertRecentOut  `json:"recent" jsonschema:"the newest evaluations, newest first, at most 14"`
	CreatedAt        string            `json:"created_at"`
	UpdatedAt        string            `json:"updated_at"`
	ArchivedAt       string            `json:"archived_at" jsonschema:"empty while live"`
	Warnings         []string          `json:"warnings,omitempty" jsonschema:"create and a new definition only: the events and attributes the project has never sent, which read no_data until they are"`
}

type alertsOut struct {
	Alerts  []alertOut `json:"alerts"`
	MaxLive int        `json:"max_live" jsonschema:"the live alerts a project may have"`
}

type alertDayOut struct {
	Day     string   `json:"day" jsonschema:"the window's last day, YYYY-MM-DD"`
	Value   *float64 `json:"value"`
	Compare *float64 `json:"compare" jsonschema:"a change's comparison value; null for a threshold"`
	Volume  *float64 `json:"volume" jsonschema:"what min_volume measured; null when nothing does"`
	Outcome string   `json:"outcome" jsonschema:"ok, breached or no_data"`
}

type alertGapOut struct {
	From  string  `json:"from" jsonschema:"RFC 3339 UTC"`
	To    string  `json:"to" jsonschema:"RFC 3339 UTC"`
	Hours float64 `json:"hours"`
}

type alertPreviewOut struct {
	Days  []alertDayOut `json:"days" jsonschema:"a metric alert's days, oldest first: 90 for a stat, 30 for a funnel, ending yesterday"`
	Gaps  []alertGapOut `json:"gaps" jsonschema:"a silence alert's gaps longer than its hours over the 30 days ending now, oldest first"`
	Fired int           `json:"fired" jsonschema:"how often it would have fired: the runs of breached days, or the gaps"`
}

type alertEvaluationOut struct {
	ID      int64    `json:"id"`
	At      string   `json:"at" jsonschema:"RFC 3339 UTC"`
	From    string   `json:"from" jsonschema:"the first day read, YYYY-MM-DD; empty for silence"`
	To      string   `json:"to" jsonschema:"the last day read; empty for silence"`
	Value   *float64 `json:"value"`
	Compare *float64 `json:"compare"`
	Volume  *float64 `json:"volume"`
	Outcome string   `json:"outcome" jsonschema:"ok, breached, no_data, broken or error"`
	Error   string   `json:"error" jsonschema:"why it was broken or failed; empty otherwise"`
}

type alertHistoryOut struct {
	Evaluations []alertEvaluationOut `json:"evaluations"`
	NextCursor  string               `json:"next_cursor,omitempty" jsonschema:"pass as cursor for the next page; absent on the last"`
}

func toAlertOut(v alerts.View) alertOut {
	d := v.Def
	out := alertOut{ID: v.ID, ProjectID: v.ProjectID, Name: v.Name, Kind: v.Parsed.Kind, Query: d.Query,
		Read:      alertReadIO{Metric: d.Read.Metric, Volume: d.Read.Volume, Step: d.Read.Step, From: d.Read.From},
		Condition: conditionIO(d.Condition), WindowDays: d.WindowDays, MinVolume: d.MinVolume,
		Template: d.Template, Fields: d.Fields, Paused: v.Paused, SnoozedUntil: v.SnoozedUntil,
		State: v.State, Reason: v.Reason, LastValue: v.LastValue, LastValueText: v.ValueText,
		LastEvaluatedAt: v.LastEvaluatedAt, LastDay: v.LastDay, CheckRequestedAt: v.CheckRequestedAt,
		CheckPending: v.CheckPending(), Recent: make([]alertRecentOut, 0, len(v.Recent)),
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, ArchivedAt: v.ArchivedAt, Warnings: v.Warnings}
	if v.Parsed.Kind == "silence" {
		out.Where = v.Parsed.Query.Where
	}
	for _, e := range v.Recent {
		out.Recent = append(out.Recent, alertRecentOut{At: e.At, Value: e.Value, Outcome: e.Outcome})
	}
	return out
}

// alertOps is h.alerts, refused in a process built without them.
func (h *host) alertOps() (*alerts.Ops, error) {
	if h.alerts == nil {
		return nil, invalidf("alerts are not available in this process")
	}
	return h.alerts, nil
}

func (h *host) listAlerts(ctx context.Context, in listAlertsIn) (alertsOut, error) {
	o, err := h.alertOps()
	if err != nil {
		return alertsOut{}, err
	}
	vs, err := o.List(ctx, in.ProjectID, in.Archived)
	if err != nil {
		return alertsOut{}, h.projectErr(ctx, in.ProjectID, err)
	}
	out := alertsOut{Alerts: make([]alertOut, 0, len(vs)), MaxLive: alerts.MaxLive}
	for _, v := range vs {
		out.Alerts = append(out.Alerts, toAlertOut(v))
	}
	return out, nil
}

func (h *host) createAlert(ctx context.Context, in createAlertIn) (alertOut, error) {
	o, err := h.alertOps()
	if err != nil {
		return alertOut{}, err
	}
	v, err := o.Create(ctx, actorFrom(ctx), in.ProjectID, in.input(in.Name, in.Condition, in.WindowDays, in.MinVolume))
	if err != nil {
		return alertOut{}, err
	}
	return toAlertOut(v), nil
}

func (h *host) updateAlert(ctx context.Context, in updateAlertIn) (alertOut, error) {
	o, err := h.alertOps()
	if err != nil {
		return alertOut{}, err
	}
	v, err := o.Update(ctx, actorFrom(ctx), in.ProjectID, in.ID, in.patch())
	if err != nil {
		return alertOut{}, err
	}
	return toAlertOut(v), nil
}

// alertWrite runs one of the operations that take an alert's id alone.
func (h *host) alertWrite(ctx context.Context, in alertIDIn, op func(*alerts.Ops, context.Context, string, int64, int64) (alerts.View, error)) (alertOut, error) {
	o, err := h.alertOps()
	if err != nil {
		return alertOut{}, err
	}
	v, err := op(o, ctx, actorFrom(ctx), in.ProjectID, in.ID)
	if err != nil {
		return alertOut{}, err
	}
	return toAlertOut(v), nil
}

func (h *host) archiveAlert(ctx context.Context, in alertIDIn) (alertOut, error) {
	return h.alertWrite(ctx, in, (*alerts.Ops).Archive)
}

func (h *host) restoreAlert(ctx context.Context, in alertIDIn) (alertOut, error) {
	return h.alertWrite(ctx, in, (*alerts.Ops).Restore)
}

func (h *host) checkAlert(ctx context.Context, in alertIDIn) (alertOut, error) {
	return h.alertWrite(ctx, in, (*alerts.Ops).Check)
}

func (h *host) previewAlert(ctx context.Context, in previewAlertIn) (alertPreviewOut, error) {
	o, err := h.alertOps()
	if err != nil {
		return alertPreviewOut{}, err
	}
	var def *alerts.Input
	definition := in.Query != nil || in.Read != nil || in.Where != nil || in.Template != "" || in.Fields != nil ||
		in.Condition != nil || in.WindowDays != 0 || in.MinVolume != 0
	switch {
	case in.ID != 0 && definition:
		return alertPreviewOut{}, invalidf("give a definition or the id of a saved alert, not both")
	case in.ID == 0:
		var c alertConditionIO
		if in.Condition != nil {
			c = *in.Condition
		}
		d := in.input("", c, in.WindowDays, in.MinVolume)
		def = &d
	}
	bt, err := o.Preview(ctx, in.ProjectID, def, in.ID)
	if err != nil {
		return alertPreviewOut{}, err
	}
	out := alertPreviewOut{Days: make([]alertDayOut, 0, len(bt.Days)), Gaps: make([]alertGapOut, 0, len(bt.Gaps)), Fired: bt.Fired}
	for _, d := range bt.Days {
		out.Days = append(out.Days, alertDayOut{Day: d.Day.String(), Value: d.Value, Compare: d.Compare, Volume: d.Volume, Outcome: d.Outcome})
	}
	for _, g := range bt.Gaps {
		out.Gaps = append(out.Gaps, alertGapOut{From: g.From.UTC().Format(time.RFC3339), To: g.To.UTC().Format(time.RFC3339), Hours: g.Hours})
	}
	return out, nil
}

func (h *host) alertHistory(ctx context.Context, in alertHistoryIn) (alertHistoryOut, error) {
	o, err := h.alertOps()
	if err != nil {
		return alertHistoryOut{}, err
	}
	es, next, err := o.History(ctx, in.ProjectID, in.ID, in.Limit, in.Cursor)
	if err != nil {
		return alertHistoryOut{}, err
	}
	out := alertHistoryOut{Evaluations: make([]alertEvaluationOut, 0, len(es)), NextCursor: next}
	for _, e := range es {
		out.Evaluations = append(out.Evaluations, alertEvaluationOut{ID: e.ID, At: e.At, From: e.From, To: e.To,
			Value: e.Value, Compare: e.Compare, Volume: e.Volume, Outcome: e.Outcome, Error: e.Error})
	}
	return out, nil
}

// constrainAlert is queryEnums on query, and the filter enums on where.
func constrainAlert(in *jsonschema.Schema) {
	queryEnums(in)
	filterEnums(in.Properties["where"].Items)
}

// The rules every definition follows, which the three tools taking one
// say alike.
const (
	alertReadRule      = "read names the number: a stat's metric (required when the stat has several) and, for a rate or an average (ratio, bounce_rate, avg, mean, percentile), volume, a second metric counting the same rows (count, actors, users, groups, weight); a funnel's step (not the first) and from (start, the default, or previous)."
	alertConditionRule = "condition is {type: threshold, op: below|above, value} or {type: change, direction: drop|rise|either, percent, against: previous|week_ago} over the last window_days complete days ending yesterday (UTC), or {type: silence, hours} on the time events were received, which takes where or the no_events template and no window_days or min_volume."
	alertTemplateRule  = "template with fields instead of query: event_count {event, path: a $page_view's page}, unique_people {event: optional, every event without; who: actors (default) or users}, conversion {from_event, from_path, to_event, to_path, people: actor (default) or user}, no_events {event, path} (silence)."
)

func (h *host) registerAlerts(r *registrar) {
	const p = "/api/projects/{project_id}/alerts"
	expose(r, spec{Name: "list_alerts", Annotations: ro, Method: "GET", Path: p,
		Description: "A project's live alerts by name (archived: true lists the archived ones instead), and max_live, how many live alerts a project may have. Each has id, name, kind (stat, funnel or silence), query, read, where (silence), condition, window_days, min_volume, template and fields (when made from one), paused, snoozed_until, state (pending, ok, breached, no_data, broken, paused, snoozed), reason (why broken or no_data), last_value and last_value_text, last_evaluated_at, last_day (the last day its window ended on), check_requested_at and check_pending, recent (the newest 14 evaluations: at, value, outcome), created_at, updated_at and archived_at. Empty strings are none."},
		h.listAlerts)
	expose(r, spec{Name: "create_alert", Annotations: write, Method: "POST", Path: p, Status: http.StatusCreated, constrain: constrainAlert,
		Description: "Create an alert, which notifies (the inbox and NOTIFY_URL) when its number breaches a line: name, condition, and exactly one source: query (a stat or a funnel) with read, where (a silence alert's events) or template with fields. " +
			alertReadRule + " " + alertConditionRule + " window_days defaults to 1, and to 7 for conversion. " + alertTemplateRule +
			" min_volume (1 or more) makes a window under it no_data instead of a breach. An event an event rule renames or blocks is refused, as is an attribute an attribute rule renames or blocks; one the project has never sent is allowed, named in warnings (it reads no_data until sent). It starts pending, with a check requested: poll list_alerts until check_pending is false. Run preview_alert first to see how often it would have fired. At most max_live live alerts a project; names unique among them. Returns the alert."},
		h.createAlert)
	expose(r, spec{Name: "preview_alert", Annotations: ro, Method: "POST", Path: p + "/preview", constrain: constrainAlert,
		Description: "Backtest an alert without saving it: a definition as create_alert takes it (name ignored), or the id of a saved one. " +
			alertReadRule + " " + alertConditionRule + " " + alertTemplateRule +
			" Returns days, each window's day, value, compare, volume and outcome (ok, breached, no_data): the 90 days ending yesterday for a stat, 30 for a funnel; or a silence alert's gaps (from, to, hours) longer than its hours over the 30 days ending now, an estimate (the live check runs hourly, and a gap is counted from the window's start); and fired, how often it would have fired (the runs of breached days, or the gaps). Reads only; bounded by CONSOLE_QUERY_TIMEOUT."},
		h.previewAlert)
	expose(r, spec{Name: "update_alert", Annotations: write, Method: "PATCH", Path: p + "/{id}", constrain: constrainAlert,
		Description: "Change an alert: name, condition (whole), window_days, min_volume, paused, snoozed_until (an RFC 3339 time ahead; null wakes it), or the source: query (with read, or none), where, or template (with fields, or none) replaces the old one whole; read alone keeps the query, fields alone keep the template. " +
			alertReadRule + " " + alertConditionRule + " " + alertTemplateRule +
			" Fields you omit are left unchanged, except that a condition switching into or out of silence resets window_days and min_volume to their defaults unless given. A new definition is checked as at create (warnings again). A new definition, a pause or a snooze ends the state without a notification (a breach without a recovery) and sets it pending; a new definition, resuming and waking request a check. A rename does neither. An archived alert is refused. Returns the alert."},
		h.updateAlert)
	expose(r, spec{Name: "archive_alert", Annotations: idem, Method: "POST", Path: p + "/{id}/archive",
		Description: "Archive an alert: it stops, ends its state without a notification, and frees its name. Reversible with restore_alert; purged with its evaluations RETENTION_ARCHIVED_DAYS (default 30) after archiving. Returns the alert."},
		h.archiveAlert)
	expose(r, spec{Name: "restore_alert", Annotations: idem, Method: "POST", Path: p + "/{id}/restore",
		Description: "Restore an archived alert, pending. Refused (conflict) when a live alert took its name or the project already holds max_live live alerts. Returns the alert."},
		h.restoreAlert)
	expose(r, spec{Name: "check_alert", Annotations: write, Method: "POST", Path: p + "/{id}/check",
		Description: "Ask for an evaluation of the alert now, as a scheduled one (it can notify): a request the server serves within a minute. Returns the alert right away, with check_requested_at and check_pending; call list_alerts or alert_history until check_pending is false, then read state, last_value and reason. A request while one is pending answers the alert unchanged. Refused (invalid) for a paused or snoozed alert, an archived one, or one of an archived project, which are not evaluated."},
		h.checkAlert)
	expose(r, spec{Name: "alert_history", Annotations: ro, Method: "GET", Path: p + "/{id}/history",
		Description: "An alert's stored evaluations, newest first, kept 400 days: id, at, from and to (the days read; empty for silence), value, compare, volume, outcome (ok, breached, no_data, broken, or error, a failure retried) and error. Pages of limit (default 50, at most 400); pass next_cursor back as cursor."},
		h.alertHistory)
}
