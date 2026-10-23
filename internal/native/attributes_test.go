package native

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
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

// attrDay is one day's (index d) product and measures events of project 1
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

// attrWidgetSQL is the Product dashboard's attribute values query as written,
// over table, its parameters spelled for duckTable.
func attrWidgetSQL(t *testing.T, table string) string {
	t.Helper()
	b, err := os.ReadFile("../reporting/system/product/attribute-values.sql")
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
			want := duckTableWith(t, f, attrRules, c.sql, c.from, c.to)
			if len(want) < 10 {
				t.Fatalf("the SQL returns %d rows: a weak case", len(want))
			}
			// A batch's pseudo-rows in one chunk, and in many.
			defer func(n int) { unpivotChunk = n }(unpivotChunk)
			for _, chunk := range []int{1 << 16, 7} {
				unpivotChunk = chunk
				got, err := e.Answer(context.Background(), c.q, Params{Project: 1, From: date(t, c.from), To: date(t, c.to)}, attrRules)
				if err != nil {
					t.Fatal(err)
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
	rows := duckTableWith(t, f, attrRules, attrWidgetSQL(t, "product"), attrFrom, today)
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
	want := duckTableWith(t, f, attrRules, `SELECT substr(col, 6) AS a, attr_value, count(*) AS c FROM (
		UNPIVOT (SELECT COLUMNS('^attr_') FROM product WHERE `+inRange+`) ON COLUMNS('^attr_') INTO NAME col VALUE attr_value)
		GROUP BY ALL ORDER BY c DESC, a, attr_value LIMIT 5`, attrFrom, today)
	sameTable(t, got.Rows, want)

	q = Query{Kind: "attributes", Table: "product", Output: "values", Dimensions: []string{"event_name"}, Metrics: []Metric{{Op: "count"}}}
	got, err = e.Answer(context.Background(), q, p, attrRules)
	if err != nil {
		t.Fatal(err)
	}
	want = duckTableWith(t, f, attrRules, `SELECT event_name, substr(col, 6) AS a, attr_value, count(*) AS c FROM (
		UNPIVOT (SELECT event_name, COLUMNS('^attr_') FROM product WHERE `+inRange+`) ON COLUMNS('^attr_') INTO NAME col VALUE attr_value)
		GROUP BY ALL ORDER BY event_name, a, c DESC, attr_value`, attrFrom, today)
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
