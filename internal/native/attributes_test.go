package native

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// attrFrom is the attributes fixture's first day: August becomes a month
// file, September's days day files and today a log.
const attrFrom = "2026-08-20"

// attrRules are project 1's rules for the attributes fixture: colour
// renamed onto color, secret blocked, ev1 renamed signup and ev2 blocked.
var attrRules = archive.Rules{
	Events: []store.EventRule{
		evRule(1, store.FamilyProduct, "ev1", store.EventRuleRename, "signup"),
		evRule(1, store.FamilyProduct, "ev2", store.EventRuleBlock, ""),
	},
	Attributes: []store.AttributeRule{
		attrRule(1, "colour", store.EventRuleRename, "color"),
		attrRule(1, "secret", store.EventRuleBlock, ""),
	},
}

// attrDay is one day's (index d) product and measures events (and a few
// views, with attributes of their own) of project 1
// with attributes: plan (sent as Plan on today's, so the month and day
// files' attr_plan is the log's attr_Plan), color and colour, secret, an
// item per event (many values), note (an empty value), and beta on
// today's only; some events have no actor, some a group.
func attrDay(t testing.TB, day string, d int) []store.Event {
	t.Helper()
	base := date(t, day).Time().Add(9 * time.Hour)
	live := day == today
	var out []store.Event
	for k := range 14 {
		e := store.Event{ID: fmt.Sprintf("x-%s-%d", day, k), ProjectID: 1, Family: store.FamilyProduct,
			EventName: fmt.Sprintf("ev%d", k%4), TS: base.Add(time.Duration(k) * time.Second), Host: "example.com",
			Path: fmt.Sprintf("/p%d", k%3), Country: []string{"DE", "", "FR"}[(k+d)%3],
			Platform: []string{"web", "ios"}[k%2], AppVersion: []string{"", "1.0", "1.1"}[(k+d)%3]}
		if k%5 != 4 {
			e.ActorID = fmt.Sprintf("a%d", (k+d)%6)
		}
		if k%3 == 0 && e.ActorID != "" {
			e.UserID = fmt.Sprintf("u%d", (k+d)%2) // several actors share a user
		}
		if k%4 == 1 {
			e.GroupID = fmt.Sprintf("g%d", (k+d)%3)
		}
		e.Attributes = map[string]string{"item": fmt.Sprintf("i%d", (k*d)%9)}
		plan := "plan"
		if live {
			plan = "Plan"
		}
		if k%6 != 5 {
			e.Attributes[plan] = []string{"free", "pro", "team"}[(k+d)%3]
		}
		switch k % 6 {
		case 1:
			e.Attributes["color"] = "red"
		case 2:
			e.Attributes["colour"] = "blue"
		case 3:
			e.Attributes["color"], e.Attributes["colour"] = "green", "grey"
		}
		if k%2 == 0 {
			e.Attributes["secret"] = fmt.Sprintf("s%d", k)
		}
		if k%7 == 3 {
			e.Attributes["note"] = ""
		}
		if live && k%3 == 0 {
			e.Attributes["beta"] = []string{"on", "off"}[k%2]
		}
		out = append(out, e)
	}
	for k := range 4 {
		v := float64(k) + 0.5
		e := store.Event{ID: fmt.Sprintf("m-%s-%d", day, k), ProjectID: 1, Family: store.FamilyMeasures,
			EventName: "$lcp", Measure: "web", TS: base.Add(time.Duration(100+k) * time.Second), Value: &v,
			Platform: "web", Attributes: map[string]string{"plan": []string{"free", "pro"}[(k+d)%2], "secret": "m",
				"conn": fmt.Sprintf("c%d", (k*d)%7)}}
		if k != 3 {
			e.ActorID = fmt.Sprintf("a%d", k)
		}
		out = append(out, e)
	}
	// Views, whose attributes an attributes never reads.
	for k := range 3 {
		out = append(out, store.Event{ID: fmt.Sprintf("v-%s-%d", day, k), ProjectID: 1, Family: store.FamilyViews,
			EventName: "$page_view", TS: base.Add(time.Duration(200+k) * time.Second), Host: "example.com", Path: "/",
			ActorID: fmt.Sprintf("a%d", k), Attributes: map[string]string{"ref": fmt.Sprintf("r%d", k)}})
	}
	return out
}

func attrFixture(t testing.TB) *archivetest.Files {
	t.Helper()
	f := archivetest.New(t)
	start := date(t, attrFrom).Time()
	for d := 0; ; d++ {
		day := start.AddDate(0, 0, d).Format(time.DateOnly)
		if day == today {
			break
		}
		f.Write(t, attrDay(t, day, d)...)
	}
	f.Maintain(t, today)
	f.Write(t, attrDay(t, today, 21)...)
	return f
}

// attrWidgetSQL is the Product dashboard's attribute values SQL, kept in testdata,
// over table, its parameters spelled for goldenTable.
func attrWidgetSQL(t *testing.T, table string) string {
	t.Helper()
	b, err := os.ReadFile("../reporting/testdata/system-sql/product/attribute-values.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.NewReplacer(":project", "1", ":from", "DATE '%[1]s'", ":to", "DATE '%[2]s'").Replace(string(b))
	if table != "product" {
		s = strings.Replace(s, "FROM product", "FROM "+table, 1)
	}
	return s
}

// apiReservedKeys is internal/api's reservedKeys (ops_product.go), over
// the keys it shares with the engine.
func apiReservedKeys() string {
	keys := make([]string, 0, len(ReservedKeyColumns))
	for k := range ReservedKeyColumns {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		keys[i] = "nullif(" + ReservedKeyColumns[k] + `, '') AS "attr_` + k + `"`
	}
	return strings.Join(keys, ",\n           ")
}

// productAttributesSQL is product_attributes' query (ops_product.go), cond
// its event filter.
func productAttributesSQL(cond string) string {
	return `WITH a AS (
  UNPIVOT (
    SELECT COLUMNS('^(day|event_name|actor_id|group_id|attr_.*)$'),
           ` + apiReservedKeys() + `
    FROM product
    WHERE project_id = 1 AND day BETWEEN DATE '%[1]s' AND DATE '%[2]s'` + cond + `
  )
  ON COLUMNS('^attr_') INTO NAME col VALUE attr_value
)
SELECT day, event_name, substr(col, 6) AS attr_key, attr_value, count(*) AS "count",
       count(DISTINCT actor_id) AS unique_users, count(DISTINCT group_id) AS unique_groups
FROM a
GROUP BY day, event_name, col, attr_value
ORDER BY day, "count" DESC, event_name, attr_key, attr_value`
}

func TestAttributesMatchDuckDB(t *testing.T) {
	f := attrFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	widget := Query{Kind: "attributes", Table: "product", Output: "values",
		Metrics: []Metric{{Op: "count"}, {Op: "actors_or_null", As: "users"}, {Op: "groups"}}}
	measures := widget
	measures.Table = "measures"
	perDay := Query{Kind: "attributes", Table: "product", Output: "values", Dimensions: []string{"day", "event_name"},
		Reserved: true, Metrics: []Metric{{Op: "count"}, {Op: "actors"}, {Op: "groups"}}}
	people := Query{Kind: "attributes", Table: "product", Output: "values", Dimensions: []string{"event_name"},
		Metrics: []Metric{{Op: "actors"}, {Op: "users"}, {Op: "count"}}}
	signup := perDay
	signup.Where = []Filter{{Column: "event_name", Op: "eq", Values: []string{"signup"}}}
	cases := []struct {
		name     string
		q        Query
		from, to string
		sql      string
	}{
		{"widget", widget, attrFrom, today, attrWidgetSQL(t, "product")},
		{"widget september", widget, "2026-09-01", today, attrWidgetSQL(t, "product")},
		{"widget august", widget, attrFrom, "2026-08-31", attrWidgetSQL(t, "product")},
		{"widget measures", measures, attrFrom, today, attrWidgetSQL(t, "measures")},
		{"product_attributes", perDay, attrFrom, today, productAttributesSQL("")},
		{"actors and users", people, attrFrom, today, `SELECT event_name, substr(col, 6) AS a, attr_value,
			count(DISTINCT actor_id) AS n, count(DISTINCT user_id), count(*) FROM (
			UNPIVOT (SELECT event_name, actor_id, user_id, COLUMNS('^attr_') FROM product WHERE ` + inRange + `)
			ON COLUMNS('^attr_') INTO NAME col VALUE attr_value)
			GROUP BY ALL ORDER BY event_name, a, n DESC, attr_value`},
		{"product_attributes signup", signup, "2026-09-05", today, productAttributesSQL(` AND event_name = 'signup'`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var want [][]string
			// A batch's pseudo-rows in one chunk, and in many.
			defer func(n int) { unpivotChunk = n }(unpivotChunk)
			for _, chunk := range []int{1 << 16, 7} {
				unpivotChunk = chunk
				got, err := e.Answer(context.Background(), c.q, Params{Project: 1, From: date(t, c.from), To: date(t, c.to)}, attrRules)
				if err != nil {
					t.Fatal(err)
				}
				if want == nil {
					if want = goldenTableWith(t, attrRules, c.sql, c.from, c.to, got.Rows); len(want) < 10 {
						t.Fatalf("the SQL returns %d rows: a weak case", len(want))
					}
				}
				if !slices.Equal(got.Columns, c.q.Columns()) {
					t.Errorf("columns %v, want %v", got.Columns, c.q.Columns())
				}
				if got.Truncated || len(got.Approximate) > 0 {
					t.Errorf("truncated %v, approximate %v", got.Truncated, got.Approximate)
				}
				sameTable(t, got.Rows, want)
			}
		})
	}

	// The fixture holds what the cases are about.
	rows := goldenAnswer(t, e, 1, widget, attrRules, attrWidgetSQL(t, "product"), attrFrom, today)
	attrs := map[string]bool{}
	for _, r := range rows {
		attrs[r[0]] = true
	}
	for _, a := range []string{"Plan", "beta", "color", "note", "item"} {
		if !attrs[a] {
			t.Errorf("the widget has no attribute %q: %v", a, attrs)
		}
	}
	for _, a := range []string{"plan", "colour", "secret"} {
		if attrs[a] {
			t.Errorf("the widget has attribute %q", a)
		}
	}
	// A NULL actor counts as one in the widget, as none in product_attributes.
	q := Query{Kind: "attributes", Table: "product", Output: "values", Metrics: []Metric{{Op: "actors"}, {Op: "actors_or_null"}}}
	got, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, attrFrom), To: date(t, today)}, attrRules)
	if err != nil {
		t.Fatal(err)
	}
	differ := false
	for _, r := range got.Rows {
		differ = differ || r[2] != r[3]
	}
	if !differ {
		t.Error("actors and actors_or_null agree on every value: no NULL actor")
	}
	// Users are distinct user_id, not actors.
	q = Query{Kind: "attributes", Table: "product", Output: "values", Metrics: []Metric{{Op: "actors"}, {Op: "users"}}}
	if got, err = e.Answer(context.Background(), q, Params{Project: 1, From: date(t, attrFrom), To: date(t, today)}, attrRules); err != nil {
		t.Fatal(err)
	}
	differ = false
	for _, r := range got.Rows {
		differ = differ || (r[2] != r[3] && r[3] != "0")
	}
	if !differ {
		t.Error("actors and users agree on every value")
	}
}

func TestAttributesOrderAndLimit(t *testing.T) {
	f := attrFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 2})
	p := Params{Project: 1, From: date(t, attrFrom), To: date(t, today)}
	q := Query{Kind: "attributes", Table: "product", Output: "values", Metrics: []Metric{{Op: "count"}}, Order: "count", Limit: 5}
	got, err := e.Answer(context.Background(), q, p, attrRules)
	if err != nil {
		t.Fatal(err)
	}
	want := goldenTableWith(t, attrRules, `SELECT substr(col, 6) AS a, attr_value, count(*) AS c FROM (
		UNPIVOT (SELECT COLUMNS('^attr_') FROM product WHERE `+inRange+`) ON COLUMNS('^attr_') INTO NAME col VALUE attr_value)
		GROUP BY ALL ORDER BY c DESC, a, attr_value LIMIT 5`, attrFrom, today, got.Rows)
	sameTable(t, got.Rows, want)

	q = Query{Kind: "attributes", Table: "product", Output: "values", Dimensions: []string{"event_name"}, Metrics: []Metric{{Op: "count"}}}
	got, err = e.Answer(context.Background(), q, p, attrRules)
	if err != nil {
		t.Fatal(err)
	}
	want = goldenTableWith(t, attrRules, `SELECT event_name, substr(col, 6) AS a, attr_value, count(*) AS c FROM (
		UNPIVOT (SELECT event_name, COLUMNS('^attr_') FROM product WHERE `+inRange+`) ON COLUMNS('^attr_') INTO NAME col VALUE attr_value)
		GROUP BY ALL ORDER BY event_name, a, c DESC, attr_value`, attrFrom, today, got.Rows)
	sameTable(t, got.Rows, want)
}

func TestAttributesTruncate(t *testing.T) {
	f := attrFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 2, MaxGroups: 5})
	q := Query{Kind: "attributes", Table: "product", Output: "values", Metrics: []Metric{{Op: "count"}}}
	got, err := e.Answer(context.Background(), q, Params{Project: 1, From: date(t, attrFrom), To: date(t, today)}, attrRules)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Rows) != 5 {
		t.Errorf("truncated %v, %d rows", got.Truncated, len(got.Rows))
	}
}

// listAttributesSQL is list_attributes' query (ops_attributes.go) over
// families, project 1's range, its :limit and keep list bound and cond
// added to its filter.
func listAttributesSQL(families string, limit int, keep []string, cond string) string {
	q := `row_number() OVER (ORDER BY events DESC, attr_key) <= ` + fmt.Sprint(limit)
	if len(keep) > 0 {
		lits := make([]string, len(keep))
		for i, k := range keep {
			lits[i] = "'" + strings.ReplaceAll(k, "'", "''") + "'"
		}
		q += ` OR attr_key IN (` + strings.Join(lits, ", ") + `)`
	}
	return `WITH a AS (
  UNPIVOT (
    SELECT COLUMNS('^(day|attr_.*)$'), CAST(NULL AS VARCHAR) AS "attr_$"
    FROM events
    WHERE family IN (` + families + `) AND ` + inRange + cond + `
  )
  ON COLUMNS('^attr_') INTO NAME col VALUE attr_value
),
k AS (
  SELECT substr(col, 6) AS attr_key, count(*) AS events, count(DISTINCT attr_value) AS "values"
  FROM a
  GROUP BY col
)
SELECT attr_key, events, "values", count(*) OVER () AS keys_total
FROM k
QUALIFY ` + q + `
ORDER BY events DESC, attr_key`
}

func TestAttributeKeysMatchDuckDB(t *testing.T) {
	f := attrFixture(t)
	e := New(Options{Dir: f.Dir, Workers: 3})
	keys := func(table string, limit int, keep ...string) Query {
		return Query{Kind: "attributes", Table: table, Output: "keys", Limit: limit, Keep: keep}
	}
	both := "'product', 'measures'"
	web := keys("events", 2, "note")
	web.Where = []Filter{{Column: "platform", Op: "eq", Values: []string{"web"}}}
	cases := []struct {
		name     string
		q        Query
		from, to string
		sql      string
	}{
		{"default", Query{Kind: "attributes", Output: "keys"}, attrFrom, today, listAttributesSQL(both, 50, nil, "")},
		{"all", keys("events", 1000), attrFrom, today, listAttributesSQL(both, 1000, nil, "")},
		// limit under the keys, a kept key outside it, kept keys with no
		// events (blocked, renamed away, never sent, and a spelling of
		// a key the files spell otherwise).
		{"limit and keep", keys("events", 2, "note", "secret", "colour", "nothing", "plan", "conn"), attrFrom, today,
			listAttributesSQL(both, 2, []string{"note", "secret", "colour", "nothing", "plan", "conn"}, "")},
		{"august", keys("events", 3, "beta"), attrFrom, "2026-08-31", listAttributesSQL(both, 3, []string{"beta"}, "")},
		{"product", keys("product", 4, "conn"), attrFrom, today, listAttributesSQL("'product'", 4, []string{"conn"}, "")},
		{"measures", keys("measures", 1, "Plan"), "2026-09-01", today, listAttributesSQL("'measures'", 1, []string{"Plan"}, "")},
		{"where", web, attrFrom, today, listAttributesSQL(both, 2, []string{"note"}, ` AND platform = 'web'`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var want [][]string
			defer func(n int) { unpivotChunk = n }(unpivotChunk)
			for _, chunk := range []int{1 << 16, 7} {
				unpivotChunk = chunk
				got, err := e.Answer(context.Background(), c.q, Params{Project: 1, From: date(t, c.from), To: date(t, c.to)}, attrRules)
				if err != nil {
					t.Fatal(err)
				}
				if want == nil {
					if want = goldenTableWith(t, attrRules, c.sql, c.from, c.to, got.Rows); len(want) < 2 {
						t.Fatalf("the SQL returns %d rows: a weak case", len(want))
					}
				}
				if !slices.Equal(got.Columns, c.q.Columns()) {
					t.Errorf("columns %v, want %v", got.Columns, c.q.Columns())
				}
				if got.Truncated || len(got.Approximate) > 0 {
					t.Errorf("truncated %v, approximate %v", got.Truncated, got.Approximate)
				}
				sameTable(t, got.Rows, want)
			}
		})
	}

	// The fixture holds what the cases are about: more keys than the
	// limit, a kept key past it, measures' own key, and views' left out.
	rows := goldenAnswer(t, e, 1, keys("events", 2, "note", "conn"), attrRules, listAttributesSQL(both, 2, []string{"note", "conn"}, ""), attrFrom, today)
	got := map[string]bool{}
	for _, r := range rows {
		got[r[0]] = true
		if r[3] == "2" || r[3] == "3" {
			t.Errorf("keys_total %s: no more keys than the limit", r[3])
		}
	}
	if len(rows) != 4 || !got["note"] || !got["conn"] {
		t.Errorf("the kept keys are not past the limit: %v", rows)
	}
	// The views' keys, which no attributes query reads: counted off their
	// rows as the SQL counts them (each key's values, and distinct ones).
	var attrCols []string
	for _, c := range eventsColumns(t, f.Dir, 1, attrRules) {
		if strings.HasPrefix(c, "attr_") {
			attrCols = append(attrCols, c)
		}
	}
	n, distinct := map[string]int{}, map[string]map[string]bool{}
	for _, r := range nativeRows(t, f, compile(t, f, 1, attrRules), append([]string{"id"}, attrCols...), "views") {
		for i, c := range attrCols {
			if v := r[i+1]; v != nullCell {
				k := strings.TrimPrefix(c, "attr_")
				if distinct[k] == nil {
					distinct[k] = map[string]bool{}
				}
				n[k]++
				distinct[k][v] = true
			}
		}
	}
	var viewKeys [][]string
	for k := range n {
		viewKeys = append(viewKeys, []string{k, strconv.Itoa(n[k]), strconv.Itoa(len(distinct[k])), strconv.Itoa(len(n))})
	}
	slices.SortFunc(viewKeys, func(a, b []string) int {
		x, _ := strconv.Atoi(a[1])
		y, _ := strconv.Atoi(b[1])
		return cmp.Or(cmp.Compare(y, x), strings.Compare(a[0], b[0]))
	})
	all := goldenTableWith(t, attrRules, listAttributesSQL("'views'", 100, nil, ""), attrFrom, today, viewKeys)
	sameTable(t, viewKeys, all)
	if len(all) == 0 || all[0][0] == "" {
		t.Error("the views carry no attribute")
	}
	for _, r := range goldenAnswer(t, e, 1, keys("events", 100), attrRules, listAttributesSQL(both, 100, nil, ""), attrFrom, today) {
		if r[0] == "ref" {
			t.Error("the keys read the views")
		}
	}
}

func TestAttributeKeysApproximate(t *testing.T) {
	f := archivetest.New(t)
	var evs []store.Event
	base := date(t, today).Time().Add(9 * time.Hour)
	for k := range 1500 {
		evs = append(evs, store.Event{ID: fmt.Sprintf("x-%d", k), ProjectID: 1, Family: store.FamilyProduct, EventName: "e",
			TS: base.Add(time.Duration(k) * time.Millisecond), Attributes: map[string]string{"item": fmt.Sprint(k), "plan": "free"}})
	}
	f.Write(t, evs...)
	e := New(Options{Dir: f.Dir, Workers: 2})
	got, err := e.Answer(context.Background(), Query{Kind: "attributes", Output: "keys"}, Params{Project: 1, From: date(t, today), To: date(t, today)}, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Approximate, []string{"values"}) {
		t.Errorf("approximate %v, want values", got.Approximate)
	}
	if len(got.Rows) != 2 || got.Rows[0][0] != "item" || got.Rows[1][2] != "1" {
		t.Errorf("rows %q", got.Rows)
	}
}
