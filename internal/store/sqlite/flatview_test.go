package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The view's column set now comes from the caller-supplied declared keys,
// not a scan of stored events, so an undeclared attribute must stay
// reachable only through the raw attributes JSON, never as its own column.
func TestFlatViewOnlyDeclaredKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductEvent(t, db, 1, "signup", "2026-08-01T10:00:00Z",
		map[string]string{"plan": "pro", "undeclared": "x"}, "", "")
	if err := db.RebuildFlatView(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	cols := viewColumns(t, db, "v_events_flat")
	if !cols["attr_plan"] || cols["attr_undeclared"] {
		t.Fatalf("columns = %v, want attr_plan and not attr_undeclared", cols)
	}
	// Undeclared keys stay reachable through the raw JSON base column.
	var got string
	if err := db.db.QueryRow(
		`SELECT json_extract(attributes,'$.undeclared') FROM v_events_flat`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "x" {
		t.Fatalf("undeclared via attributes = %q, want x", got)
	}
}

// A rebuild with an unchanged column set must not re-execute the
// DROP/CREATE — the daily pass runs this every night and should be a
// no-op in steady state.
func TestRebuildFlatViewIsNoOpWhenUnchanged(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.RebuildFlatView(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	before := viewSQL(t, db, "v_events_flat")
	if err := db.RebuildFlatView(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	if viewSQL(t, db, "v_events_flat") != before {
		t.Fatal("view was rebuilt despite an unchanged column set")
	}
}

// sanitizeAlias is lossy and many-to-one: "plan!" and "plan" both sanitize
// to attr_plan. A no-op check that compares only column names would treat
// fixing a typo'd declared key as unchanged and leave the view extracting
// the stale JSON path forever. The check must compare the full CREATE VIEW
// text, which embeds the json_extract path literal, so this rebuild is
// correctly detected as a real change.
func TestRebuildFlatViewDetectsRenameBehindAnUnchangedAlias(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductEvent(t, db, 1, "e", "2026-08-10T10:00:00Z",
		map[string]string{"plan": "pro", "plan!": "stale"}, "", "")
	if err := db.RebuildFlatView(ctx, []string{"plan!"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFlatView(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.db.QueryRow(`SELECT attr_plan FROM v_events_flat`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "pro" {
		t.Fatalf("attr_plan = %q, want %q — view is still extracting the stale key", got, "pro")
	}
}

func TestRebuildFlatView(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	evs := []store.Event{
		{Family: store.FamilyProduct, ID: "1", ProjectID: 1, EventName: "e", ActorID: "u1", TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro"}},
	}
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFlatView(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	var id, event, user, tsCol, plan string
	var project int64
	if err := db.db.QueryRow(
		`SELECT id, project_id, event_name, actor_id, ts, attr_plan FROM v_events_flat WHERE id='1'`).
		Scan(&id, &project, &event, &user, &tsCol, &plan); err != nil {
		t.Fatal(err)
	}
	if plan != "pro" {
		t.Fatalf("attr_plan = %q", plan)
	}
	if project != 1 || event != "e" || user != "u1" || tsCol != "2026-08-10T10:00:00Z" {
		t.Fatalf("base columns wrong: %d %q %q %q", project, event, user, tsCol)
	}

	// Rebuild with more keys replaces the view.
	if err := db.RebuildFlatView(ctx, []string{"plan", "source"}); err != nil {
		t.Fatal(err)
	}
	var src *string
	if err := db.db.QueryRow(`SELECT attr_source FROM v_events_flat WHERE id='1'`).Scan(&src); err != nil {
		t.Fatal(err)
	}
	if src != nil {
		t.Fatalf("missing attr must be NULL, got %v", *src)
	}

	// Rebuilding with no keys leaves a usable base-column view.
	if err := db.RebuildFlatView(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`SELECT id FROM v_events_flat WHERE id='1'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Query(`SELECT attr_plan FROM v_events_flat`); err == nil {
		t.Error("attr_plan should be gone after rebuilding without it")
	}
	// consent is a base column, present regardless of declared attributes.
	if _, err := db.db.Query(`SELECT consent FROM v_events_flat LIMIT 0`); err != nil {
		t.Errorf("v_events_flat has no consent column: %v", err)
	}
}

// v_events_flat holds both families; family and kind tell them apart.
func TestFlatViewHoldsBothFamilies(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyViews, ID: "v", ProjectID: 1, EventName: "$page_view", Kind: "web",
			ActorID: "a", Path: "/", TS: ts("2026-08-10T10:00:00Z")},
		{Family: store.FamilyProduct, ID: "e", ProjectID: 1, EventName: "signup", ActorID: "a",
			TS: ts("2026-08-10T10:00:00Z")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFlatView(ctx, nil); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	rows, err := db.db.Query(`SELECT id, family || ' ' || event_name || ' ' || kind FROM v_events_flat`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, v string
		if err := rows.Scan(&id, &v); err != nil {
			t.Fatal(err)
		}
		got[id] = v
	}
	if got["v"] != "views $page_view web" || got["e"] != "product signup " {
		t.Errorf("v_events_flat rows = %q", got)
	}
}

func TestRebuildFlatViewHostileKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	// Injection attempts and collisions must not break the DDL. Declared
	// keys are operator-authored now rather than client-supplied, but
	// sanitizeAlias stays the defence in depth between a fat-fingered
	// config and broken DDL, so these must keep passing unchanged.
	hostile := []string{
		`x"; DROP TABLE events; --`,
		"weird key!",
		"weird-key.", // collides with "weird key!" after sanitizing
		"1starts_with_digit",
		"漢字", // sanitizes to empty -> skipped
	}
	if err := db.RebuildFlatView(ctx, hostile); err != nil {
		t.Fatal(err)
	}
	// events must still exist (no injection).
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatalf("table gone — injection succeeded: %v", err)
	}
	cols := viewColumns(t, db, "v_events_flat")
	if !cols["attr_weirdkey"] || !cols["attr_weirdkey_2"] {
		t.Fatalf("collision suffixing failed: %v", cols)
	}
	if !cols["attr_1starts_with_digit"] {
		t.Errorf("digit-leading key not prefixed into a valid identifier: %v", cols)
	}
	// 8 base columns (id, project_id, family, event_name, actor_id, kind, consent, ts) + attributes + 4 attrs (漢字 skipped).
	if len(cols) != 9+4 {
		t.Errorf("cols = %v, want 9 base + 4 attrs (漢字 skipped)", cols)
	}
}

// A quote in the key must survive into the JSON path as data, not terminate
// the surrounding SQL string literal.
func TestRebuildFlatViewQuotedKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteEvents(ctx, []store.Event{
		{Family: store.FamilyProduct, ID: "1", ProjectID: 1, EventName: "e", ActorID: "u", TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"it's": "apostrophe", `say"hi`: "doublequote"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFlatView(ctx, []string{"it's", `say"hi`}); err != nil {
		t.Fatal(err)
	}
	var apos, dq string
	if err := db.db.QueryRow(`SELECT attr_its, attr_sayhi FROM v_events_flat WHERE id='1'`).
		Scan(&apos, &dq); err != nil {
		t.Fatal(err)
	}
	if apos != "apostrophe" {
		t.Errorf("attr_its = %q, want apostrophe", apos)
	}
	if dq != "doublequote" {
		t.Errorf("attr_sayhi = %q, want doublequote", dq)
	}
}

// Column order must not depend on map iteration or caller ordering, so the
// view is stable across restarts.
func TestRebuildFlatViewDeterministicOrder(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.RebuildFlatView(ctx, []string{"zeta", "alpha", "mu"}); err != nil {
		t.Fatal(err)
	}
	first := viewColumnList(t, db, "v_events_flat")
	if err := db.RebuildFlatView(ctx, []string{"mu", "zeta", "alpha"}); err != nil {
		t.Fatal(err)
	}
	second := viewColumnList(t, db, "v_events_flat")
	if len(first) != len(second) {
		t.Fatalf("%v vs %v", first, second)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("column order not deterministic: %v vs %v", first, second)
		}
	}
	want := []string{"id", "project_id", "family", "event_name", "actor_id", "kind", "consent", "ts", "attributes",
		"attr_alpha", "attr_mu", "attr_zeta"}
	for i := range want {
		if first[i] != want[i] {
			t.Fatalf("columns = %v, want %v", first, want)
		}
	}
}

// viewColumnList returns a view's (or table's) column names in schema
// order, via pragma_table_info — used to assert both column presence and
// deterministic ordering.
func viewColumnList(t *testing.T, db *DB, view string) []string {
	t.Helper()
	rows, err := db.db.Query(`SELECT name FROM pragma_table_info(?)`, view)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// viewColumns is viewColumnList as a set, for presence checks that don't
// care about order.
func viewColumns(t *testing.T, db *DB, view string) map[string]bool {
	t.Helper()
	cols := map[string]bool{}
	for _, c := range viewColumnList(t, db, view) {
		cols[c] = true
	}
	return cols
}

// viewSQL returns the CREATE VIEW text sqlite_master recorded for view, so
// a test can assert a rebuild did or didn't happen.
func viewSQL(t *testing.T, db *DB, view string) string {
	t.Helper()
	var sql string
	if err := db.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = ?`, view).Scan(&sql); err != nil {
		t.Fatal(err)
	}
	return sql
}

// A declared $ key is a column of the raw table already; it gets no
// attr_ column (it would only ever extract NULL from the blob).
func TestFlatViewSkipsSystemKeys(t *testing.T) {
	db := newTestDB(t)
	if err := db.RebuildFlatView(context.Background(), []string{"plan", "$path"}); err != nil {
		t.Fatal(err)
	}
	def, err := db.flatViewDefinition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def, "attr_plan") || strings.Contains(def, "attr_path") {
		t.Fatalf("v_events_flat = %s, want attr_plan and no attr_path", def)
	}
}
