package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/wire"
)

// The caps, by the setting that sets each.
const (
	settingViews      = "VIEWS_DIMENSIONS_TOP_N"
	settingAttrs      = "ATTRIBUTE_VALUES_TOP_N"
	settingIdentities = "IDENTITIES_TOP_N"
)

// ---- limits ----

// Limit groups, in the order the console shows them.
const (
	groupRetention = "retention"
	groupCaps      = "caps"
	groupIngest    = "ingest"
)

type limitOut struct {
	Group       string   `json:"group" jsonschema:"retention, caps or ingest"`
	Name        string   `json:"name" jsonschema:"what is limited, for people"`
	Setting     string   `json:"setting,omitempty" jsonschema:"the environment variable that sets it; absent for a fixed limit"`
	Value       float64  `json:"value" jsonschema:"the limit in force, in unit"`
	Default     *float64 `json:"default,omitempty" jsonschema:"the setting's default; absent for a fixed limit"`
	Unit        string   `json:"unit,omitempty" jsonschema:"days, bytes, characters or seconds; absent for a count or a plain number"`
	Zero        string   `json:"zero,omitempty" jsonschema:"what a value of 0 means, when it is not the number (no cap, kept forever)"`
	Description string   `json:"description"`
}

type limitsOut struct {
	Limits []limitOut `json:"limits"`
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
// order the console shows them: retention, the caps, then the wire
// format's fixed limits.
func limitsFrom(cfg *config.Config) []limitOut {
	ret := cfg.Retention
	return []limitOut{
		setting(groupRetention, "Raw events", "RETENTION_EVENTS_RAW_DAYS", ret.Events.RawDays, config.DefaultRawDays, "days", "",
			"raw events are kept this long, then rolled up into aggregates; also the oldest client timestamp accepted, older ones are clamped to it"),
		setting(groupRetention, "Aggregates", "RETENTION_EVENTS_AGGREGATE_DAYS", ret.Events.AggregateDays, config.DefaultAggregateDays, "days", "",
			"aggregates, actors, cohorts and identities are kept this long, then deleted"),
		setting(groupRetention, "Archived items", "RETENTION_ARCHIVED_DAYS", ret.ArchivedDays, config.DefaultArchivedDays, "days", "kept forever",
			"an archived project (with its data), dashboard or widget is deleted this long after archiving"),
		setting(groupCaps, "Views breakdown values", settingViews, cfg.ViewsDimensionsTopN, config.DefaultViewsDimensionsTopN, "", "no cap",
			"values per views breakdown and kinds, per project and day; the rest fold into (other)"),
		setting(groupCaps, "Attribute values", settingAttrs, cfg.AttributeValuesTopN, config.DefaultAttributeValuesTopN, "", "no cap",
			"values per attribute key, per project, day and event; the rest fold into (other)"),
		setting(groupCaps, "Users and groups", settingIdentities, cfg.IdentitiesTopN, config.DefaultIdentitiesTopN, "", "no cap",
			"users, and groups, per project and day; the rest are dropped"),
		fixed("Request body", wire.MaxBody, "bytes", "a larger request is refused with 413"),
		fixed("Events per batch", wire.MaxBatchEvents, "", "a larger batch is refused with 413; split it and retry"),
		fixed("Attribute key length", wire.MaxAttrKey, "characters", "a custom attribute with a longer key is dropped, with a warning; the number of attributes has no limit"),
		fixed("Attribute value length", wire.MaxAttrValue, "characters", "a longer value is truncated, not rejected"),
		fixed("Timestamp ahead of the server", wire.FutureSkew.Seconds(), "seconds", "a later client timestamp is clamped to the time received"),
		fixed("Measure value", wire.MaxMeasureValue, "", "a measure with a larger (or negative) value is rejected"),
		fixed("Lowest sample rate", wire.MinSampleRate, "", "a $sample_rate outside [lowest, 1] is stored as 1"),
	}
}

func (h *host) listLimits(_ context.Context, _ struct{}) (limitsOut, error) {
	return limitsOut{Limits: h.limits}, nil
}

// capOf is the cap in force for a setting; 0 (no cap) for an unknown one.
func (h *host) capOf(setting string) int {
	for _, l := range h.limits {
		if l.Group == groupCaps && l.Setting == setting {
			return int(l.Value)
		}
	}
	return 0
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

// ---- cap_usage ----

type capUsageIn struct {
	ProjectID int64 `json:"project_id" jsonschema:"project id; call list_projects first"`
	usageRangeIn
}

type capUsageRow struct {
	Setting         string   `json:"setting"`
	Dimension       string   `json:"dimension" jsonschema:"a views breakdown (paths, …, kinds), an attribute key, or users/groups"`
	Cap             int      `json:"cap" jsonschema:"the cap in force; 0 means no cap"`
	MaxValuesPerDay int      `json:"max_values_per_day" jsonschema:"the most values one day held, the (other) row included"`
	MaxDay          string   `json:"max_day"`
	Days            int      `json:"days" jsonschema:"days with data in the range"`
	DaysCapped      int      `json:"days_capped" jsonschema:"days with an (other) row; for users and groups, days that reached the cap"`
	FoldedShare     *float64 `json:"folded_share" jsonschema:"the (other) rows' share of views, counts or samples; null for users and groups"`
}

type capUsageOut struct {
	ProjectID  int64         `json:"project_id"`
	From       string        `json:"from"`
	To         string        `json:"to"`
	Dimensions []capUsageRow `json:"dimensions"`
}

// capViews are the views breakdowns VIEWS_DIMENSIONS_TOP_N caps, each with
// the column that folds into (other). consent is not here: three values.
var capViews = []struct{ dimension, view, last string }{
	{"paths", "v_views_paths", "path"},
	{"hosts", "v_views_hosts", "host"},
	{"referrers", "v_views_referrers", "source"},
	{"utm", "v_views_utm", "utm_campaign"},
	{"countries", "v_views_countries", "country"},
	{"platforms", "v_views_platforms", "platform"},
	{"os", "v_views_os", "os_version"},
	{"browsers", "v_views_browsers", "browser_version"},
	{"app_versions", "v_views_app_versions", "app_version"},
	{"devices", "v_views_devices", "device_model"},
	{"displays", "v_views_displays", "display"},
	{"locales", "v_views_locales", "app_locale"},
	{"kinds", "v_views_daily", "kind"},
}

// dayUsage is one day of one dimension: its values, whether it folded,
// and the measure (views, count, samples) total and folded.
type dayUsage struct {
	day            string
	values         int
	folded         bool
	total, foldedN float64
}

// summarize turns a dimension's days into its row. Days come ordered by
// day, so the busiest day is the earliest of equals.
func summarize(setting, dimension string, cap int, days []dayUsage, share bool) capUsageRow {
	row := capUsageRow{Setting: setting, Dimension: dimension, Cap: cap, Days: len(days)}
	var total, folded float64
	for _, d := range days {
		if d.values > row.MaxValuesPerDay {
			row.MaxValuesPerDay, row.MaxDay = d.values, d.day
		}
		if d.folded {
			row.DaysCapped++
		}
		total += d.total
		folded += d.foldedN
	}
	if share && total > 0 {
		s := folded / total
		row.FoldedShare = &s
	}
	return row
}

// run is db.Run for the tools that fold several queries into one answer: a
// timeout and a truncated result are both refusals (invalid, narrow the
// range), never a partial answer. Each query must be shaped so its rows fit
// the row cap for any allowed range.
func (h *host) run(ctx context.Context, q string, args ...any) (readsql.Result, error) {
	res, err := h.db.Run(ctx, q, args...)
	if err != nil {
		if errors.Is(err, readsql.ErrTimeout) {
			return readsql.Result{}, invalidf("query exceeded %s; narrow the date range", h.db.Timeout())
		}
		return readsql.Result{}, err
	}
	if res.Truncated {
		return readsql.Result{}, invalidf("too many rows; narrow the date range")
	}
	return res, nil
}

func (h *host) capUsage(ctx context.Context, in capUsageIn) (capUsageOut, error) {
	if h.reg.Snapshot(ctx).Project(in.ProjectID) == nil {
		return capUsageOut{}, h.unknownProjectErr(ctx, in.ProjectID)
	}
	fromD, toD, err := usageRange(in.usageRangeIn, time.Now())
	if err != nil {
		return capUsageOut{}, err
	}
	from, to := fromD.String(), toD.String()
	out := capUsageOut{ProjectID: in.ProjectID, From: from, To: to, Dimensions: []capUsageRow{}}

	for _, v := range capViews {
		res, err := h.run(ctx, fmt.Sprintf(`SELECT day, COUNT(*), MAX(%[1]s = '(other)'),
			SUM(views), SUM(CASE WHEN %[1]s = '(other)' THEN views ELSE 0 END)
			FROM %[2]s WHERE project_id = ? AND day BETWEEN ? AND ? GROUP BY day ORDER BY day`, v.last, v.view),
			in.ProjectID, from, to)
		if err != nil {
			return capUsageOut{}, err
		}
		if days := daysOf(res.Rows); len(days) > 0 {
			out.Dimensions = append(out.Dimensions, summarize(settingViews, v.dimension, h.capOf(settingViews), days, true))
		}
	}

	// Attributes: the cap is per event (and per measure), so a day's values
	// are its busiest event's; it folded if any event did. One query per
	// key keeps each answer to one row a day, whatever the number of keys.
	keys, err := h.run(ctx, `SELECT attr_key FROM v_product_attrs WHERE project_id = ? AND day BETWEEN ? AND ?
		UNION
		SELECT attr_key FROM v_measures_attrs WHERE project_id = ? AND day BETWEEN ? AND ?
		ORDER BY attr_key`,
		in.ProjectID, from, to, in.ProjectID, from, to)
	if err != nil {
		return capUsageOut{}, err
	}
	for _, k := range keys.Rows {
		res, err := h.run(ctx, `SELECT day, MAX(n), MAX(other), SUM(c), SUM(oc) FROM (
			  SELECT day, COUNT(*) AS n, MAX(attr_value = '(other)') AS other,
			         SUM(count) AS c, SUM(CASE WHEN attr_value = '(other)' THEN count ELSE 0 END) AS oc
			  FROM v_product_attrs WHERE project_id = ? AND day BETWEEN ? AND ? AND attr_key = ?
			  GROUP BY day, event_name
			  UNION ALL
			  SELECT day, COUNT(DISTINCT attr_value), MAX(attr_value = '(other)'),
			         SUM(samples), SUM(CASE WHEN attr_value = '(other)' THEN samples ELSE 0 END)
			  FROM v_measures_attrs WHERE project_id = ? AND day BETWEEN ? AND ? AND attr_key = ?
			  GROUP BY day, event_name, measure
			) GROUP BY day ORDER BY day`,
			in.ProjectID, from, to, k[0], in.ProjectID, from, to, k[0])
		if err != nil {
			return capUsageOut{}, err
		}
		out.Dimensions = append(out.Dimensions, summarize(settingAttrs, k[0], h.capOf(settingAttrs), daysOf(res.Rows), true))
	}

	// Identities keep no (other) row: a day is capped when it reached the cap.
	idCap := h.capOf(settingIdentities)
	for _, kind := range []struct{ kind, dimension string }{{"user", "users"}, {"group", "groups"}} {
		res, err := h.run(ctx, `SELECT day, COUNT(*) FROM v_identity_daily
			WHERE project_id = ? AND day BETWEEN ? AND ? AND kind = ? GROUP BY day ORDER BY day`,
			in.ProjectID, from, to, kind.kind)
		if err != nil {
			return capUsageOut{}, err
		}
		var days []dayUsage
		for _, r := range res.Rows {
			n, _ := strconv.Atoi(r[1])
			days = append(days, dayUsage{day: r[0], values: n, folded: idCap > 0 && n >= idCap})
		}
		if len(days) > 0 {
			out.Dimensions = append(out.Dimensions, summarize(settingIdentities, kind.dimension, idCap, days, false))
		}
	}
	return out, nil
}

// daysOf parses rows of (day, values, folded, total, folded total).
func daysOf(rows [][]string) []dayUsage {
	out := make([]dayUsage, 0, len(rows))
	for _, r := range rows {
		n, _ := strconv.Atoi(r[1])
		total, _ := strconv.ParseFloat(r[3], 64)
		folded, _ := strconv.ParseFloat(r[4], 64)
		out = append(out, dayUsage{day: r[0], values: n, folded: r[2] == "1", total: total, foldedN: folded})
	}
	return out
}
