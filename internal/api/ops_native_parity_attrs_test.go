// The parity harness for the analytics tools: each tool's native answer
// checked against the answer the SQL it ran before gave over the same
// files, frozen in testdata/<Test>.golden.json (oldTable). The SQL stays
// as the goldens' key and record; it is no longer run.

package api

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/reporting/sqlbind"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The SQL product_attributes, measures, list_attributes and identities
// ran before the native engine answered them, whose answers the goldens
// keep to check the native answers against
// (TestAttributeToolsMatchTheirOldSQL).

// oldReservedKeys selects each reserved key's column, empty read as NULL,
// as "attr_<key>", in key order (product_attributes' reservedKeys).
func oldReservedKeys() string {
	keys := make([]string, 0, len(native.ReservedKeyColumns))
	for k := range native.ReservedKeyColumns {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		keys[i] = "nullif(" + native.ReservedKeyColumns[k] + `, '') AS "attr_` + k + `"`
	}
	return strings.Join(keys, ",\n           ")
}

// oldProductAttributesSQL is product_attributes' SQL; cond its event
// filter (` AND event_name = :event`) or "".
func oldProductAttributesSQL(cond string) string {
	return `WITH a AS (
  UNPIVOT (
    SELECT COLUMNS('^(day|event_name|actor_id|group_id|attr_.*)$'),
           ` + oldReservedKeys() + `
    FROM product
    WHERE project_id = :project AND day BETWEEN :from AND :to` + cond + `
  )
  ON COLUMNS('^attr_') INTO NAME col VALUE attr_value
)
SELECT day, event_name, substr(col, 6) AS attr_key, attr_value, count(*) AS "count",
       count(DISTINCT actor_id) AS unique_users, count(DISTINCT group_id) AS unique_groups
FROM a
GROUP BY day, event_name, col, attr_value
ORDER BY day, "count" DESC, event_name, attr_key, attr_value`
}

// oldMeasuresSQL is measures' SQL: cond its name filter, dim, by and
// partition its attr_key breakdown's pieces (oldMeasureAttr's value).
func oldMeasuresSQL(cond, value string) string {
	dim, by, partition := "", "", ""
	if value != "" {
		dim, by, partition = " AND "+value+" IS NOT NULL", value+" AS attr_value, ", ", attr_value"
	}
	return `WITH h AS (
  SELECT event_name, measure, ` + by + `bucket,
         CASE WHEN bucket = -1000 THEN 0 ELSE 2 * pow(1.04, bucket) / 2.04 END AS approx_value,
         count(*) AS s, SUM(1 / sample_rate) AS w, SUM(value / sample_rate) AS total
  FROM measures
  WHERE project_id = :project AND day BETWEEN :from AND :to` + cond + dim + `
  GROUP BY ALL
),
c AS (
  SELECT *, SUM(w) OVER (PARTITION BY event_name, measure` + partition + ` ORDER BY bucket) AS run,
            SUM(w) OVER (PARTITION BY event_name, measure` + partition + `) AS all_w
  FROM h
)
SELECT event_name, measure` + partition + `, SUM(s) AS samples, ROUND(SUM(w), 1) AS est_count,
       ROUND(SUM(total) / SUM(w), 3) AS mean,
       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.50 * all_w), 3) AS p50,
       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.75 * all_w), 3) AS p75,
       ROUND(MIN(approx_value) FILTER (WHERE run >= 0.95 * all_w), 3) AS p95
FROM c GROUP BY event_name, measure` + partition + `
ORDER BY event_name, measure` + partition
}

// oldMeasureAttr is the SQL value measures' attr_key read: a reserved
// key's column (empty read as NULL), else the key's attr_<key> column,
// else NULL.
func oldMeasureAttr(t *testing.T, h *host, key string) string {
	t.Helper()
	if col, ok := native.ReservedKeyColumns[key]; ok {
		return "nullif(" + col + ", '')"
	}
	cols := frozen(t, "measures' columns", measuresColumns(t, h))
	if col, ok := attrColumn(cols, key); ok {
		return col
	}
	return "CAST(NULL AS VARCHAR)"
}

// oldListAttributesSQL is list_attributes' SQL, keep its QUALIFY.
func oldListAttributesSQL(keep string) string {
	return `WITH a AS (
  UNPIVOT (
    SELECT COLUMNS('^(day|attr_.*)$'), CAST(NULL AS VARCHAR) AS "attr_$"
    FROM events
    WHERE family IN ('product', 'measures') AND day BETWEEN :from AND :to
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
QUALIFY ` + keep + `
ORDER BY events DESC, attr_key`
}

// oldIdentitiesSQL is identities' SQL for kind user or group.
func oldIdentitiesSQL(kind string) string {
	id, users := "user_id", "1"
	if kind == "group" {
		id, users = "group_id", "count(DISTINCT user_id)"
	}
	return `WITH d AS (
  SELECT ` + id + ` AS id, day, count(DISTINCT actor_id) AS actors, ` + users + ` AS users,
         count(*) FILTER (WHERE family = 'views') AS views,
         count(*) FILTER (WHERE family = 'product') AS events
  FROM events
  WHERE project_id = :project AND family IN ('views', 'product') AND ` + id + ` <> ''
    AND day BETWEEN :from AND :to
  GROUP BY 1, day
)
SELECT d.id, COALESCE(i.name, '') AS name, SUM(d.actors) AS actors, SUM(d.users) AS users,
       SUM(d.views) AS views, SUM(d.events) AS events
FROM d
LEFT JOIN identities i ON i.project_id = :project AND i.kind = :kind AND i.id = d.id
GROUP BY d.id, i.name
ORDER BY SUM(d.views) + SUM(d.events) DESC, d.id
LIMIT :limit`
}

// writeRawEvents appends evs to h's open logs as they are, an empty
// actor_id left NULL (writeHostEvents would give it one).
func writeRawEvents(t *testing.T, h *host, evs ...store.Event) {
	t.Helper()
	log, err := archive.OpenLog(h.eventsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	for i := range evs {
		e := &evs[i]
		e.ID = fmt.Sprintf("0190cccc-0000-7000-8000-%012d", hostEventN.Add(1))
		if e.SampleRate == 0 {
			e.SampleRate = 1
		}
		e.ReceivedAt = e.TS
	}
	if err := log.WriteEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

// writeAttributeEdges adds to the host's files (whose Parquet days carry
// the reserved keys, plan on signup, tier on a measure, u1 Jane Doe of
// Acme Inc in g1, u2 without a name in g2) the cases the four tools must
// agree on with their old SQL, all in the open log of 2026-08-21:
//   - upgrade events carrying fresh (a key only the open log has), an
//     empty value, old_key (renamed to new_key) and noisy (blocked), one
//     of them without an actor, by u5 (no name) in g3;
//   - u7, whose display name is its id (the one identities difference);
//   - render measures: one sampled at 0.25, one with an empty tier, one
//     without a tier, on safari.
//
// It also sets the two attribute rules.
func writeAttributeEdges(t *testing.T, h *host) {
	t.Helper()
	at := time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC)
	up := func(sec int, actor, user, group string, attrs map[string]string) store.Event {
		e := store.Event{ProjectID: 1, Family: store.FamilyProduct, EventName: "upgrade", TS: at.Add(time.Duration(sec) * time.Second),
			Kind: "web", Platform: "web", Path: "/billing", ActorID: actor, UserID: user, GroupID: group, Attributes: attrs}
		if actor != "" {
			e.ActorKind = store.ActorInstall
			if user != "" {
				e.ActorKind = store.ActorUser
			}
		}
		return e
	}
	v1, v2, v3 := 120.0, 80.0, 40.0
	render := func(sec int, rate float64, v *float64, attrs map[string]string) store.Event {
		return store.Event{ProjectID: 1, Family: store.FamilyMeasures, EventName: "render", TS: at.Add(time.Duration(sec) * time.Second),
			Kind: "web", Platform: "web", Browser: "safari", ActorID: "r1", ActorKind: store.ActorInstall,
			Measure: store.MeasureTime, Value: v, SampleRate: rate, Attributes: attrs}
	}
	writeRawEvents(t, h,
		up(0, "u5", "u5", "g3", map[string]string{"fresh": "a", "old_key": "x", "noisy": "1", "blank": ""}),
		up(1, "", "u5", "g3", map[string]string{"fresh": "b", "blank": ""}),
		up(2, "", "", "", map[string]string{"fresh": "a"}),
		up(3, "u7", "u7", "g1", map[string]string{"plan": "team"}),
		render(4, 0.25, &v1, map[string]string{"tier": "silver"}),
		render(5, 1, &v2, map[string]string{"tier": ""}),
		render(6, 1, &v3, nil),
	)
	writeRawEvents(t, h, func() store.Event {
		e := up(7, "u7", "u7", "", nil)
		e.UserName = "u7"
		return e
	}())
	ctx := context.Background()
	if _, err := h.ops.RenameAttribute(ctx, "test", 1, "old_key", "new_key"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ops.BlockAttribute(ctx, "test", 1, "noisy"); err != nil {
		t.Fatal(err)
	}
}

// TestAttributeToolsMatchTheirOldSQL runs product_attributes, measures,
// list_attributes and identities natively and checks each answer against
// the tool's old SQL over the same files: the template's Parquet days and
// writeAttributeEdges' open log, rules applied. The one difference
// allowed is named where it is checked: identities reads an id whose
// name is the id itself as unnamed.
func TestAttributeToolsMatchTheirOldSQL(t *testing.T) {
	h, _ := newTestHost(t)
	writeAttributeEdges(t, h)
	ctx := context.Background()
	old := func(in rangeIn, q string, vals map[string]sqlbind.Value, got tableOut) tableOut {
		t.Helper()
		return oldTable(t, h, in, q, vals, got, nil)
	}
	ranges := []rangeIn{
		{ProjectID: 1, From: "2026-08-01", To: "2026-08-31"},
		{ProjectID: 1, From: "2026-08-20", To: "2026-08-21"},
		{ProjectID: 1, From: "2026-08-21", To: "2026-08-21"},
		{ProjectID: 1, From: "2026-07-01", To: "2026-07-31"}, // no data
		{ProjectID: 1, From: "2026-08-21", To: "2026-08-20"}, // to before from
	}
	text := func(s string) sqlbind.Value { return sqlbind.Value{Type: "VARCHAR", Text: s} }

	t.Run("product_attributes", func(t *testing.T) {
		sawFresh, sawRenamed := false, false
		for _, r := range ranges {
			for _, event := range []string{"", "signup", "upgrade", "nope"} {
				got, err := h.productAttributes(ctx, productEventsIn{rangeIn: r, Event: event})
				if err != nil {
					t.Fatal(err)
				}
				var want tableOut
				if event == "" {
					want = old(r, oldProductAttributesSQL(""), nil, toolTable(t, got))
				} else {
					want = old(r, oldProductAttributesSQL(` AND event_name = :event`), map[string]sqlbind.Value{":event": text(event)}, toolTable(t, got))
				}
				for _, row := range want.Rows {
					sawFresh = sawFresh || row[2] == "fresh"
					sawRenamed = sawRenamed || row[2] == "new_key"
					if row[2] == "noisy" || row[2] == "old_key" {
						t.Errorf("the old SQL reads %s: the rules are not applied", row[2])
					}
				}
				sameNumbers(t, fmt.Sprintf("%s..%s event %q", r.From, r.To, event), toolTable(t, got), want, 0)
			}
		}
		if !sawFresh || !sawRenamed {
			t.Error("the fixture's open-log key or renamed key never shows: a weak case")
		}
	})

	t.Run("measures", func(t *testing.T) {
		for _, r := range ranges {
			for _, name := range []string{"", "checkout_api", "render", "nope"} {
				for _, key := range []string{"", "tier", "$browser", "$path", "blank_none", "$nope"} {
					got, err := h.measures(ctx, measuresIn{rangeIn: r, Name: name, AttrKey: key})
					if err != nil {
						t.Fatal(err)
					}
					cond, vals := "", map[string]sqlbind.Value(nil)
					if name != "" {
						cond, vals = ` AND event_name = :event`, map[string]sqlbind.Value{":event": text(name)}
					}
					value := ""
					if key != "" {
						value = oldMeasureAttr(t, h, key)
					}
					sameNumbers(t, fmt.Sprintf("%s..%s name %q attr_key %q", r.From, r.To, name, key), toolTable(t, got),
						old(r, oldMeasuresSQL(cond, value), vals, toolTable(t, got)), 1e-9)
				}
			}
		}
		// The empty tier is a value of its own, as the SQL's IS NOT NULL
		// keeps it, and the sampled render weighs 4.
		got, err := h.measures(ctx, measuresIn{rangeIn: ranges[2], Name: "render", AttrKey: "tier"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Rows) != 2 || got.Rows[0][2] != "" || got.Rows[1][2] != "silver" || got.Rows[1][4] != "4" {
			t.Errorf("render by tier = %q, want the empty tier, then silver weighing 4", got.Rows)
		}
	})

	t.Run("list_attributes", func(t *testing.T) {
		defer func(n int) { attributeKeysLimit = n }(attributeKeysLimit)
		for _, limit := range []int{500, 2} {
			attributeKeysLimit = limit
			for _, r := range ranges[:4] {
				got, err := h.listAttributes(ctx, attributesIn{ProjectID: 1, usageRangeIn: usageRangeIn{From: r.From, To: r.To}})
				if err != nil {
					t.Fatal(err)
				}
				var rows [][]string
				var total int64
				table := tableOut{Columns: []string{"attr_key", "events", "values", "keys_total"}}
				for _, a := range got.Attributes {
					if a.Received {
						rows = append(rows, []string{a.Key, strconv.FormatInt(a.Events, 10), strconv.FormatInt(a.Values, 10)})
						table.Rows = append(table.Rows, append(slices.Clone(rows[len(rows)-1]), strconv.FormatInt(got.KeysTotal, 10)))
					}
				}
				// The rules name new_key and noisy (blocked).
				want := old(r, oldListAttributesSQL(`row_number() OVER (ORDER BY events DESC, attr_key) <= :limit OR attr_key IN (:k0, :k1)`),
					map[string]sqlbind.Value{":limit": {Type: "BIGINT", Text: strconv.Itoa(limit)}, ":k0": text("new_key"), ":k1": text("noisy")}, table)
				var wantRows [][]string
				for _, row := range want.Rows {
					wantRows = append(wantRows, row[:3])
					total, _ = strconv.ParseInt(row[3], 10, 64)
				}
				name := fmt.Sprintf("limit %d %s..%s", limit, r.From, r.To)
				if !slices.EqualFunc(rows, wantRows, slices.Equal) || got.KeysTotal != total {
					t.Errorf("%s:\n got %q (%d)\nwant %q (%d)", name, rows, got.KeysTotal, wantRows, total)
				}
				if !slices.ContainsFunc(got.Attributes, func(a attributeOut) bool { return a.Key == "noisy" && a.Blocked && !a.Received }) {
					t.Errorf("%s: the blocked key is not listed: %+v", name, got.Attributes)
				}
			}
		}
	})

	t.Run("identities", func(t *testing.T) {
		sawNameIsID := false
		for _, r := range ranges {
			for _, kind := range []string{"user", "group"} {
				for _, limit := range []int{0, 1, 2} {
					got, err := h.identities(ctx, identitiesIn{rangeIn: r, Kind: kind, Limit: limit})
					if err != nil {
						t.Fatal(err)
					}
					n := limit
					if n == 0 {
						n = 50
					}
					// The difference allowed: a name that is the id itself
					// reads as none (user_label gives the id for an id
					// without a name).
					nameIsID := func(o tableOut) tableOut {
						o = cloneTable(o)
						for _, row := range o.Rows {
							if row[1] != "" && row[1] == row[0] {
								row[1], sawNameIsID = "", true
							}
						}
						return o
					}
					want := nameIsID(oldTable(t, h, r, oldIdentitiesSQL(kind), map[string]sqlbind.Value{":kind": text(kind), ":limit": {Type: "BIGINT", Text: strconv.Itoa(n)}},
						toolTable(t, got), nameIsID))
					sameNumbers(t, fmt.Sprintf("%s..%s %s limit %d", r.From, r.To, kind, limit), toolTable(t, got), want, 0)
				}
			}
		}
		if !sawNameIsID {
			t.Error("no row of the fixture shows the name-is-id difference: the allowance checks nothing")
		}
	})
}

// Under a row cap below their answers the four tools say the table is
// cut, as the old SQL did; identities only when its own limit is past the
// cap.
func TestAttributeToolsUnderTheRowCap(t *testing.T) {
	h, _ := newTestHost(t)
	setGuards(t, h, h.queryTimeout, 1)
	ctx := context.Background()
	blog := rangeIn{ProjectID: 1, From: "2026-08-01", To: "2026-08-31"}
	partial := func(name string, out tableOut, err error) {
		t.Helper()
		if err != nil || !out.Truncated || !strings.Contains(out.Note, "PARTIAL") || len(out.Rows) != 1 {
			t.Errorf("%s = %+v, %v; want 1 row, truncated, noted", name, out, err)
		}
	}
	pa, err := h.productAttributes(ctx, productEventsIn{rangeIn: blog})
	partial("product_attributes", pa, err)
	me, err := h.measures(ctx, measuresIn{rangeIn: blog})
	partial("measures", me, err)
	id, err := h.identities(ctx, identitiesIn{rangeIn: blog, Kind: "user", Limit: 2})
	partial("identities past the cap", id, err)
	if id, err = h.identities(ctx, identitiesIn{rangeIn: blog, Kind: "user", Limit: 1}); err != nil || id.Truncated || len(id.Rows) != 1 {
		t.Errorf("identities at its own limit = %+v, %v; want 1 row, whole", id, err)
	}
	h.native = nil
	for name, call := range map[string]func() error{
		"product_attributes": func() error { _, err := h.productAttributes(ctx, productEventsIn{rangeIn: blog}); return err },
		"measures":           func() error { _, err := h.measures(ctx, measuresIn{rangeIn: blog}); return err },
		"identities":         func() error { _, err := h.identities(ctx, identitiesIn{rangeIn: blog, Kind: "group"}); return err },
		"list_attributes": func() error {
			_, err := h.listAttributes(ctx, attributesIn{ProjectID: 1})
			return err
		},
	} {
		if call() == nil {
			t.Errorf("%s answered without an engine", name)
		}
	}
}

// attrColumn is the column of cols (an event file's columns) that holds
// the attribute key, quoted for the SQL engine: attr_<key> as the files
// name it, a NUL in the key written as the six characters \u0000, matched
// without regard to case as the engine's column names are (attr_Plan is
// attr_plan). ok is false when no column holds the key.
func attrColumn(cols []string, key string) (col string, ok bool) {
	if key == "" {
		return "", false
	}
	name := "attr_" + strings.ReplaceAll(key, "\x00", `\u0000`)
	for _, c := range cols {
		if strings.EqualFold(c, name) {
			return `"` + strings.ReplaceAll(c, `"`, `""`) + `"`, true
		}
	}
	return "", false
}
