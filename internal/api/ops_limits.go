package api

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// The caps, by the setting that sets each.
const (
	settingViews      = "VIEWS_DIMENSIONS_TOP_N"
	settingAttrs      = "PRODUCT_ATTRIBUTES_TOP_N"
	settingIdentities = "IDENTITIES_TOP_N"
)

// ---- limits ----

type limitOut struct {
	Setting string `json:"setting"`
	Value   int    `json:"value" jsonschema:"the cap in force; 0 means no cap"`
	Default int    `json:"default"`
	Caps    string `json:"caps" jsonschema:"what the setting caps"`
}

type limitsOut struct {
	Limits []limitOut `json:"limits"`
}

// limitsFrom reads the caps in force from the running config, in the order
// the console shows them.
func limitsFrom(cfg *config.Config) []limitOut {
	return []limitOut{
		{settingViews, cfg.ViewsDimensionsTopN, config.DefaultViewsDimensionsTopN,
			"values per views breakdown and kinds, per project and day; the rest fold into (other)"},
		{settingAttrs, cfg.ProductAttributesTopN, config.DefaultProductAttributesTopN,
			"values per attribute key, per project, day and event; the rest fold into (other)"},
		{settingIdentities, cfg.IdentitiesTopN, config.DefaultIdentitiesTopN,
			"users, and groups, per project and day; the rest are dropped"},
	}
}

func (h *host) listLimits(_ context.Context, _ struct{}) (limitsOut, error) {
	return limitsOut{Limits: h.limits}, nil
}

// capOf is the cap in force for a setting; 0 (no cap) for an unknown one.
func (h *host) capOf(setting string) int {
	for _, l := range h.limits {
		if l.Setting == setting {
			return l.Value
		}
	}
	return 0
}

// ---- the usage tools' range ----

type usageRangeIn struct {
	From string `json:"from,omitempty" jsonschema:"start day inclusive, YYYY-MM-DD; default 29 days before to"`
	To   string `json:"to,omitempty" jsonschema:"end day inclusive, YYYY-MM-DD; default today (UTC)"`
}

// maxUsageDays bounds a usage range: a year and a bit, so a 365-day range
// fits whatever day it starts.
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
	if from.AddDays(maxUsageDays).Before(to) {
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
		res, err := h.db.Run(ctx, fmt.Sprintf(`SELECT day, COUNT(*), MAX(%[1]s = '(other)'),
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
	// are its busiest event's; it folded if any event did.
	res, err := h.db.Run(ctx, `SELECT attr_key, day, MAX(n), MAX(other), SUM(c), SUM(oc) FROM (
		  SELECT attr_key, day, COUNT(*) AS n, MAX(attr_value = '(other)') AS other,
		         SUM(count) AS c, SUM(CASE WHEN attr_value = '(other)' THEN count ELSE 0 END) AS oc
		  FROM v_product_attrs WHERE project_id = ? AND day BETWEEN ? AND ?
		  GROUP BY attr_key, day, event_name
		  UNION ALL
		  SELECT attr_key, day, COUNT(DISTINCT attr_value), MAX(attr_value = '(other)'),
		         SUM(samples), SUM(CASE WHEN attr_value = '(other)' THEN samples ELSE 0 END)
		  FROM v_measures_attrs WHERE project_id = ? AND day BETWEEN ? AND ?
		  GROUP BY attr_key, day, event_name, measure
		) GROUP BY attr_key, day ORDER BY attr_key, day`,
		in.ProjectID, from, to, in.ProjectID, from, to)
	if err != nil {
		return capUsageOut{}, err
	}
	byKey := map[string][]dayUsage{}
	var keys []string
	for _, r := range res.Rows {
		if _, seen := byKey[r[0]]; !seen {
			keys = append(keys, r[0])
		}
		byKey[r[0]] = append(byKey[r[0]], daysOf([][]string{r[1:]})...)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out.Dimensions = append(out.Dimensions, summarize(settingAttrs, k, h.capOf(settingAttrs), byKey[k], true))
	}

	// Identities keep no (other) row: a day is capped when it reached the cap.
	idCap := h.capOf(settingIdentities)
	for _, kind := range []struct{ kind, dimension string }{{"user", "users"}, {"group", "groups"}} {
		res, err := h.db.Run(ctx, `SELECT day, COUNT(*) FROM v_identity_daily
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
