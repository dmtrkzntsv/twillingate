package api

import (
	"context"
	"errors"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/wire"
)

// ---- limits ----

// Limit groups, in the order the console shows them.
const (
	groupRetention = "retention"
	groupIngest    = "ingest"
)

type limitOut struct {
	Group       string   `json:"group" jsonschema:"retention or ingest"`
	Name        string   `json:"name" jsonschema:"what is limited, for people"`
	Setting     string   `json:"setting,omitempty" jsonschema:"the environment variable that sets it; absent for a fixed limit"`
	Value       float64  `json:"value" jsonschema:"the limit in force, in unit"`
	Default     *float64 `json:"default,omitempty" jsonschema:"the setting's default; absent for a fixed limit"`
	Unit        string   `json:"unit,omitempty" jsonschema:"days, bytes, characters or seconds; absent for a count or a plain number"`
	Zero        string   `json:"zero,omitempty" jsonschema:"what a value of 0 means, when it is not the number (kept forever)"`
	Description string   `json:"description"`
}

// events30dOut is the events of the last 30 days (raw-first D20): what
// the server's disk and every dashboard's scan of a month follow.
type events30dOut struct {
	Count int64  `json:"count" jsonschema:"events in the event files from from through to, of every family and project, rules applied"`
	From  string `json:"from" jsonschema:"the window's first day, 29 days before to"`
	To    string `json:"to" jsonschema:"the window's last day, today (UTC)"`
}

type limitsOut struct {
	Limits    []limitOut    `json:"limits"`
	Events30d *events30dOut `json:"events_30d,omitempty" jsonschema:"the events of the last 30 days; absent when they cannot be counted"`
	// IngestDisabled is a switch, not a limit: no value, unit or cap to
	// read against, so a field of its own rather than a row of limits.
	IngestDisabled bool `json:"ingest_disabled" jsonschema:"INGEST_DISABLED: true when every ingest route refuses events and form submissions with 429"`
}

// setting is a limit the environment sets.
func setting(group, name, env string, value, def int, unit, zero, description string) limitOut {
	d := float64(def)
	return limitOut{Group: group, Name: name, Setting: env, Value: float64(value), Default: &d,
		Unit: unit, Zero: zero, Description: description}
}

// fixed is a limit of the wire format (internal/wire), the same on every server.
func fixed(name string, value float64, unit, description string) limitOut {
	return limitOut{Group: groupIngest, Name: name, Value: value, Unit: unit, Description: description}
}

// limitsFrom reads the limits in force from the running config, in the
// order the console shows them: retention, the late window, then the wire
// format's fixed limits.
func limitsFrom(cfg *config.Config) []limitOut {
	ret := cfg.Retention
	return []limitOut{
		setting(groupRetention, "Event files", "RETENTION_DAYS", ret.Days, config.DefaultRetentionDays, "days", "",
			"the event files under DATA_DIR are kept this long, in whole months, then deleted"),
		setting(groupRetention, "Archived items", "RETENTION_ARCHIVED_DAYS", ret.ArchivedDays, config.DefaultArchivedDays, "days", "kept forever",
			"an archived project (with its data), dashboard or widget is deleted this long after archiving"),
		setting(groupRetention, "Form drafts", "RETENTION_FORMS_DRAFT_DAYS", cfg.Forms.DraftDays, config.DefaultFormDraftDays, "days", "",
			"a form created by its first submission accepts submissions as a draft this long, then is archived unless approved"),
		setting(groupIngest, "Late events", "INGEST_LATE_DAYS", cfg.IngestLateDays, config.DefaultIngestLateDays, "days", "",
			"days back a client timestamp may lie; an older one is clamped to this edge"),
		fixed("Request body", wire.MaxBody, "bytes", "a larger request is refused with 413"),
		fixed("Events per batch", wire.MaxBatchEvents, "", "a larger batch is refused with 413; split it and retry"),
		fixed("Attribute key length", wire.MaxAttrKey, "characters", "a custom attribute with a longer key is dropped, with a warning; the number of attributes has no limit"),
		fixed("Attribute value length", wire.MaxAttrValue, "characters", "a longer value is truncated, not rejected"),
		fixed("Timestamp ahead of the server", wire.FutureSkew.Seconds(), "seconds", "a later client timestamp is clamped to the time received"),
		fixed("Measure value", wire.MaxMeasureValue, "", "a measure with a larger (or negative) value is rejected"),
		fixed("Lowest sample rate", wire.MinSampleRate, "", "a $sample_rate outside [lowest, 1] is stored as 1"),
		fixed("Form body", wire.MaxFormBody, "bytes", "a larger form submission is refused (413, or the error redirect), file parts included"),
		fixed("Form fields", wire.MaxFormFields, "", "fields past this many, in name order, are dropped"),
		fixed("Form field name length", wire.MaxFormFieldName, "characters", "a field with a longer name is dropped"),
		fixed("Form value length", wire.MaxFormValue, "bytes", "a longer field value is truncated, not rejected"),
	}
}

// listLimits answers the limits in force and the events of the last 30
// days, counted at most every eventCountTTL (eventcount.go). A count that
// cannot be read, a timeout included, is left out (and was logged when it
// failed), as it is in a process with no engine; the limits still answer.
func (h *host) listLimits(ctx context.Context, _ struct{}) (limitsOut, error) {
	out := limitsOut{Limits: h.limits, IngestDisabled: h.ingestDisabled}
	if h.events == nil {
		return out, nil
	}
	if n, err := h.events.get(ctx); err == nil {
		to := civil.Today(time.Now())
		out.Events30d = &events30dOut{Count: n, From: to.AddDays(-29).String(), To: to.String()}
	}
	return out, nil
}

// ---- the usage tools' range ----

type usageRangeIn struct {
	From string `json:"from,omitempty" jsonschema:"start day inclusive, YYYY-MM-DD; default 29 days before to"`
	To   string `json:"to,omitempty" jsonschema:"end day inclusive, YYYY-MM-DD; default today (UTC)"`
}

// maxUsageDays bounds a usage range, both ends included: a year and a bit,
// so a 365-day range fits whatever day it starts.
const maxUsageDays = 400

// usageRange resolves the usage tools' range: to defaults to today (UTC),
// from to 29 days before to, so the default is the last 30 days.
func usageRange(in usageRangeIn, now time.Time) (civil.Date, civil.Date, error) {
	to := civil.Today(now)
	if in.To != "" {
		d, err := civil.Parse(in.To)
		if err != nil || !dayRe.MatchString(in.To) {
			return civil.Date{}, civil.Date{}, invalidf("to must be YYYY-MM-DD, got %q", in.To)
		}
		to = d
	}
	from := to.AddDays(-29)
	if in.From != "" {
		d, err := civil.Parse(in.From)
		if err != nil || !dayRe.MatchString(in.From) {
			return civil.Date{}, civil.Date{}, invalidf("from must be YYYY-MM-DD, got %q", in.From)
		}
		from = d
	}
	if to.Before(from) {
		return civil.Date{}, civil.Date{}, invalidf("from %s is after to %s", from, to)
	}
	if from.AddDays(maxUsageDays - 1).Before(to) {
		return civil.Date{}, civil.Date{}, invalidf("a range runs at most %d days; %s..%s is longer", maxUsageDays, from, to)
	}
	return from, to, nil
}

// run is db.Run for the tools that fold several queries into one answer: a
// timeout and a truncated result are both refusals (invalid, narrow the
// range), never a partial answer. Each query must be shaped so its rows fit
// the row cap for any allowed range.
func (h *host) run(ctx context.Context, q string, args ...any) (readsql.Result, error) {
	return runOn(ctx, h.db, q, args...)
}

// runSubs is run on h.subs, the handle that may read submissions.
func (h *host) runSubs(ctx context.Context, q string, args ...any) (readsql.Result, error) {
	return runOn(ctx, h.subs, q, args...)
}

func runOn(ctx context.Context, db *readsql.DB, q string, args ...any) (readsql.Result, error) {
	res, err := db.Run(ctx, q, args...)
	if err != nil {
		if errors.Is(err, readsql.ErrTimeout) {
			return readsql.Result{}, invalidf("query exceeded %s; narrow the date range", db.Timeout())
		}
		return readsql.Result{}, err
	}
	if res.Truncated {
		return readsql.Result{}, invalidf("too many rows; narrow the date range")
	}
	return res, nil
}
