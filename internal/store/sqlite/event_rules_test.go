package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

var ruleAudit = store.AuditEntry{Actor: "cli", Action: "event.rename", Subject: "1/a"}

// ruleDB is a test database with projects 1 and 2 present (event_rules
// references projects).
func ruleDB(t *testing.T) *DB {
	t.Helper()
	db := newTestDB(t)
	execAll(t, db,
		`INSERT OR IGNORE INTO projects (id, name, sort_key) VALUES (1, 'p1', 'a1')`,
		`INSERT OR IGNORE INTO projects (id, name, sort_key) VALUES (2, 'p2', 'a2')`)
	return db
}

// writeNamed stores n events of one name; ids are unique per (project,
// family, name, i).
func writeNamed(t *testing.T, db *DB, project int64, family store.Family, name string, n int) {
	t.Helper()
	var evs []store.Event
	for i := 0; i < n; i++ {
		e := store.Event{Family: family, ID: fmt.Sprintf("%d-%s-%s-%d", project, family, name, i),
			ProjectID: project, EventName: name, TS: ts("2026-10-10T10:00:00Z"),
			ReceivedAt: ts("2026-10-10T10:00:00Z"), Kind: "app", ActorID: "a", ActorKind: store.ActorConnection}
		if family == store.FamilyMeasures {
			v := 1.5
			e.Value, e.Measure = &v, "m"
		}
		evs = append(evs, e)
	}
	if err := db.WriteEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

func rawNames(t *testing.T, db *DB, project int64, family store.Family) map[string]int {
	t.Helper()
	rows, err := db.db.Query(`SELECT event_name, COUNT(*) FROM events WHERE project_id=? AND family=? GROUP BY event_name`, project, string(family))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var n string
		var c int
		if err := rows.Scan(&n, &c); err != nil {
			t.Fatal(err)
		}
		out[n] = c
	}
	return out
}

func loadRules(t *testing.T, db *DB) []store.EventRule {
	t.Helper()
	rules, err := db.LoadEventRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range rules {
		rules[i].CreatedAt = ""
	}
	return rules
}

func rule(project int64, family store.Family, name, action, to string) store.EventRule {
	return store.EventRule{ProjectID: project, Family: family, Name: name, Action: action, To: to}
}

func rename(t *testing.T, db *DB, project int64, family store.Family, from, to string, merge bool) map[string]int64 {
	t.Helper()
	got, err := db.RenameEvent(context.Background(), project, family, from, to, merge, ruleAudit)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func count(t *testing.T, db *DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRenameEventRewritesRawAndAggregates(t *testing.T) {
	db := ruleDB(t)
	writeNamed(t, db, 1, store.FamilyProduct, "a", 3)
	writeNamed(t, db, 1, store.FamilyProduct, "b", 1)
	writeNamed(t, db, 2, store.FamilyProduct, "a", 1)
	execAll(t, db,
		`INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users) VALUES (1, '2026-01-01', 'a', 5, 2)`,
		`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
		 VALUES (1, '2026-01-01', 'a', 'k', 'v', 5, 2, 1)`)

	got := rename(t, db, 1, store.FamilyProduct, "a", "c", false)

	want := map[string]int64{"events": 3, "agg_product_daily": 1, "agg_product_attrs": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("result = %v, want %v", got, want)
	}
	if got, want := rawNames(t, db, 1, store.FamilyProduct), map[string]int{"c": 3, "b": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("project 1 raw = %v, want %v", got, want)
	}
	if got, want := rawNames(t, db, 2, store.FamilyProduct), map[string]int{"a": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("project 2 raw = %v, want %v", got, want)
	}
	for _, table := range []string{"agg_product_daily", "agg_product_attrs"} {
		if n := count(t, db, `SELECT COUNT(*) FROM `+table+` WHERE event_name='c'`); n != 1 {
			t.Errorf("%s has %d rows named c, want 1", table, n)
		}
		if n := count(t, db, `SELECT COUNT(*) FROM `+table+` WHERE event_name='a'`); n != 0 {
			t.Errorf("%s still has %d rows named a", table, n)
		}
	}
	if got, want := loadRules(t, db), []store.EventRule{rule(1, store.FamilyProduct, "a", store.EventRuleRename, "c")}; !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want %v", got, want)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='event.rename' AND subject='1/a'`); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
}

func TestRenameEventRefusesExistingTargetWithoutMerge(t *testing.T) {
	for name, seed := range map[string]string{
		"raw":       "",
		"aggregate": `INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users) VALUES (1, '2026-01-02', 'b', 1, 1)`,
		"attrs": `INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
			VALUES (1, '2026-01-02', 'b', 'k', 'v', 1, 1, NULL)`,
	} {
		t.Run(name, func(t *testing.T) {
			db := ruleDB(t)
			writeNamed(t, db, 1, store.FamilyProduct, "a", 2)
			execAll(t, db, `INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users) VALUES (1, '2026-01-01', 'a', 5, 2)`)
			if seed == "" {
				writeNamed(t, db, 1, store.FamilyProduct, "b", 1)
			} else {
				execAll(t, db, seed)
			}
			wantRaw := rawNames(t, db, 1, store.FamilyProduct)

			_, err := db.RenameEvent(context.Background(), 1, store.FamilyProduct, "a", "b", false, ruleAudit)
			if !errors.Is(err, store.ErrConflict) {
				t.Fatalf("err = %v, want ErrConflict", err)
			}
			if got := rawNames(t, db, 1, store.FamilyProduct); !reflect.DeepEqual(got, wantRaw) {
				t.Errorf("raw = %v, want unchanged %v", got, wantRaw)
			}
			if n := count(t, db, `SELECT COUNT(*) FROM agg_product_daily WHERE event_name='a'`); n != 1 {
				t.Errorf("aggregate a rows = %d, want 1", n)
			}
			if rules := loadRules(t, db); len(rules) != 0 {
				t.Errorf("rules = %v, want none", rules)
			}
			if n := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='event.rename'`); n != 0 {
				t.Errorf("audit rows = %d, want none", n)
			}
		})
	}
}

func TestRenameEventMergeSumsAggregates(t *testing.T) {
	t.Run("product", func(t *testing.T) {
		db := ruleDB(t)
		execAll(t, db,
			`INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users) VALUES
			 (1, '2026-01-01', 'a', 5, 2), (1, '2026-01-01', 'b', 7, 3), (1, '2026-01-02', 'a', 1, 1)`,
			// k/v: groups on both sides; k/w: NULL on one side; k/x: NULL on both.
			`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups) VALUES
			 (1, '2026-01-01', 'a', 'k', 'v', 5, 2, 1), (1, '2026-01-01', 'b', 'k', 'v', 7, 3, 2),
			 (1, '2026-01-01', 'a', 'k', 'w', 1, 1, NULL), (1, '2026-01-01', 'b', 'k', 'w', 1, 1, 4),
			 (1, '2026-01-01', 'a', 'k', 'x', 1, 1, NULL), (1, '2026-01-01', 'b', 'k', 'x', 1, 1, NULL)`)

		got := rename(t, db, 1, store.FamilyProduct, "a", "b", true)
		if want := map[string]int64{"events": 0, "agg_product_daily": 2, "agg_product_attrs": 3}; !reflect.DeepEqual(got, want) {
			t.Errorf("result = %v, want %v", got, want)
		}
		type row struct{ count, users int }
		daily := map[string]row{}
		rows, err := db.db.Query(`SELECT day, count, unique_users FROM agg_product_daily WHERE event_name='b'`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var d string
			var r row
			_ = rows.Scan(&d, &r.count, &r.users)
			daily[d] = r
		}
		rows.Close()
		if want := map[string]row{"2026-01-01": {12, 5}, "2026-01-02": {1, 1}}; !reflect.DeepEqual(daily, want) {
			t.Errorf("daily = %v, want %v", daily, want)
		}
		if n := count(t, db, `SELECT COUNT(*) FROM agg_product_daily WHERE event_name='a'`); n != 0 {
			t.Errorf("a rows left: %d", n)
		}
		groups := map[string]*int{}
		arows, err := db.db.Query(`SELECT attr_value, unique_groups FROM agg_product_attrs WHERE event_name='b'`)
		if err != nil {
			t.Fatal(err)
		}
		for arows.Next() {
			var v string
			var g *int
			_ = arows.Scan(&v, &g)
			groups[v] = g
		}
		arows.Close()
		if g := groups["v"]; g == nil || *g != 3 {
			t.Errorf("unique_groups v = %v, want 3", g)
		}
		if groups["w"] != nil || groups["x"] != nil {
			t.Errorf("unique_groups w=%v x=%v, want NULL (not tracked)", groups["w"], groups["x"])
		}
		if n := count(t, db, `SELECT COUNT(*) FROM agg_product_attrs WHERE event_name='a'`); n != 0 {
			t.Errorf("attrs a rows left: %d", n)
		}
	})
	t.Run("measures", func(t *testing.T) {
		db := ruleDB(t)
		execAll(t, db,
			`INSERT INTO agg_measures_daily (project_id, day, event_name, measure, bucket, samples, weight, sum) VALUES
			 (1, '2026-01-01', 'a', 'm', 3, 2, 2.5, 10), (1, '2026-01-01', 'b', 'm', 3, 4, 4.5, 20), (1, '2026-01-02', 'a', 'm', 3, 1, 1, 1)`,
			`INSERT INTO agg_measures_attrs (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum) VALUES
			 (1, '2026-01-01', 'a', 'm', 'k', 'v', 3, 2, 2.5, 10), (1, '2026-01-01', 'b', 'm', 'k', 'v', 3, 4, 4.5, 20)`)

		got := rename(t, db, 1, store.FamilyMeasures, "a", "b", true)
		if want := map[string]int64{"events": 0, "agg_measures_daily": 2, "agg_measures_attrs": 1}; !reflect.DeepEqual(got, want) {
			t.Errorf("result = %v, want %v", got, want)
		}
		var samples int
		var weight, sum float64
		if err := db.db.QueryRow(`SELECT samples, weight, sum FROM agg_measures_daily WHERE event_name='b' AND day='2026-01-01'`).
			Scan(&samples, &weight, &sum); err != nil {
			t.Fatal(err)
		}
		if samples != 6 || weight != 7 || sum != 30 {
			t.Errorf("daily = %d/%v/%v, want 6/7/30", samples, weight, sum)
		}
		if err := db.db.QueryRow(`SELECT samples, weight, sum FROM agg_measures_attrs WHERE event_name='b'`).
			Scan(&samples, &weight, &sum); err != nil {
			t.Fatal(err)
		}
		if samples != 6 || weight != 7 || sum != 30 {
			t.Errorf("attrs = %d/%v/%v, want 6/7/30", samples, weight, sum)
		}
		for _, table := range []string{"agg_measures_daily", "agg_measures_attrs"} {
			if n := count(t, db, `SELECT COUNT(*) FROM `+table+` WHERE event_name='a'`); n != 0 {
				t.Errorf("%s: a rows left: %d", table, n)
			}
		}
	})
}

func TestRenameEventChainsCollapseAndRenameBack(t *testing.T) {
	db := ruleDB(t)
	p := store.FamilyProduct
	rename(t, db, 1, p, "A", "B", false)
	rename(t, db, 1, p, "B", "C", false)
	want := []store.EventRule{
		rule(1, p, "A", store.EventRuleRename, "C"),
		rule(1, p, "B", store.EventRuleRename, "C"),
	}
	if got := loadRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("after A>B, B>C: rules = %v, want %v", got, want)
	}
	rename(t, db, 1, p, "C", "A", false)
	want = []store.EventRule{
		rule(1, p, "B", store.EventRuleRename, "A"),
		rule(1, p, "C", store.EventRuleRename, "A"),
	}
	if got := loadRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("after C>A: rules = %v, want %v", got, want)
	}
}

func TestRenameEventReplacesBlockAndKeepsTargetBlock(t *testing.T) {
	db := ruleDB(t)
	p := store.FamilyProduct
	ctx := context.Background()
	if err := db.BlockEvent(ctx, 1, p, "X", ruleAudit); err != nil {
		t.Fatal(err)
	}
	rename(t, db, 1, p, "X", "Y", false)
	if err := db.BlockEvent(ctx, 1, p, "Z", ruleAudit); err != nil {
		t.Fatal(err)
	}
	rename(t, db, 1, p, "W", "Z", true)
	want := []store.EventRule{
		rule(1, p, "W", store.EventRuleRename, "Z"),
		rule(1, p, "X", store.EventRuleRename, "Y"),
		rule(1, p, "Z", store.EventRuleBlock, ""),
	}
	if got := loadRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want %v", got, want)
	}
}

// A blocked target with no stored rows would pass the existing-rows check,
// and the next daily pass would delete the renamed history with it: refused
// unless merge says so, and then the block stays.
func TestRenameEventRefusesBlockedTargetWithoutMerge(t *testing.T) {
	db := ruleDB(t)
	p := store.FamilyProduct
	ctx := context.Background()
	writeNamed(t, db, 1, p, "a", 2)
	execAll(t, db, `INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users) VALUES (1, '2026-01-01', 'a', 5, 2)`)
	if err := db.BlockEvent(ctx, 1, p, "b", ruleAudit); err != nil {
		t.Fatal(err)
	}
	wantRules := loadRules(t, db)
	wantRaw := rawNames(t, db, 1, p)
	auditRows := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='event.rename'`)

	_, err := db.RenameEvent(ctx, 1, p, "a", "b", false, ruleAudit)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "b is blocked") {
		t.Errorf("err = %q, want it to say the target is blocked", err)
	}
	if got := rawNames(t, db, 1, p); !reflect.DeepEqual(got, wantRaw) {
		t.Errorf("raw = %v, want unchanged %v", got, wantRaw)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_product_daily WHERE event_name='a'`); n != 1 {
		t.Errorf("aggregate a rows = %d, want 1", n)
	}
	if got := loadRules(t, db); !reflect.DeepEqual(got, wantRules) {
		t.Errorf("rules = %v, want unchanged %v", got, wantRules)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='event.rename'`); n != auditRows {
		t.Errorf("audit rows = %d, want %d", n, auditRows)
	}

	// Another family's block does not count.
	if _, err := db.RenameEvent(ctx, 1, store.FamilyMeasures, "a", "b", false, ruleAudit); err != nil {
		t.Errorf("measures rename with only a product block: %v", err)
	}

	if _, err := db.RenameEvent(ctx, 1, p, "a", "b", true, ruleAudit); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := rawNames(t, db, 1, p); got["b"] != 2 || got["a"] != 0 {
		t.Errorf("raw = %v, want a's two events under b", got)
	}
	var blocks int
	for _, r := range loadRules(t, db) {
		if r.Family == p && r.Name == "b" && r.Action == store.EventRuleBlock {
			blocks++
		}
	}
	if blocks != 1 {
		t.Errorf("block on b kept = %d, want 1", blocks)
	}
}

func TestBlockEventIsIdempotentAndReplacesRename(t *testing.T) {
	db := ruleDB(t)
	p := store.FamilyProduct
	ctx := context.Background()
	if err := db.BlockEvent(ctx, 1, p, "N", ruleAudit); err != nil {
		t.Fatal(err)
	}
	execAll(t, db, `UPDATE event_rules SET created_at='2026-01-01T00:00:00Z'`)
	if err := db.BlockEvent(ctx, 1, p, "N", ruleAudit); err != nil {
		t.Fatal(err)
	}
	var created string
	if err := db.db.QueryRow(`SELECT created_at FROM event_rules WHERE name='N'`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if n := len(loadRules(t, db)); n != 1 || created != "2026-01-01T00:00:00Z" {
		t.Errorf("rules = %d, created_at = %s, want one rule, unchanged", n, created)
	}

	rename(t, db, 1, p, "P", "Q", false)
	if err := db.BlockEvent(ctx, 1, p, "P", ruleAudit); err != nil {
		t.Fatal(err)
	}
	want := []store.EventRule{
		rule(1, p, "N", store.EventRuleBlock, ""),
		rule(1, p, "P", store.EventRuleBlock, ""),
	}
	if got := loadRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want %v", got, want)
	}
}

func TestRemoveEventRule(t *testing.T) {
	db := ruleDB(t)
	p := store.FamilyProduct
	ctx := context.Background()
	if err := db.BlockEvent(ctx, 1, p, "N", ruleAudit); err != nil {
		t.Fatal(err)
	}
	before := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='event.remove'`)
	if err := db.RemoveEventRule(ctx, 1, p, "N", store.AuditEntry{Actor: "cli", Action: "event.remove", Subject: "1/N"}); err != nil {
		t.Fatal(err)
	}
	if rules := loadRules(t, db); len(rules) != 0 {
		t.Errorf("rules = %v, want none", rules)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='event.remove'`); n != before+1 {
		t.Errorf("audit rows = %d, want %d", n, before+1)
	}
	err := db.RemoveEventRule(ctx, 1, p, "N", store.AuditEntry{Actor: "cli", Action: "event.remove", Subject: "1/N"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='event.remove'`); n != before+1 {
		t.Errorf("a refused removal was audited: %d rows", n)
	}
}

func TestWriteEventsAppliesRenameRules(t *testing.T) {
	db := ruleDB(t)
	rename(t, db, 1, store.FamilyProduct, "a", "c", false)
	writeNamed(t, db, 1, store.FamilyProduct, "a", 1)
	writeNamed(t, db, 1, store.FamilyMeasures, "a", 1)
	writeNamed(t, db, 2, store.FamilyProduct, "a", 1)
	if got, want := rawNames(t, db, 1, store.FamilyProduct), map[string]int{"c": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("project 1 product = %v, want %v", got, want)
	}
	if got, want := rawNames(t, db, 1, store.FamilyMeasures), map[string]int{"a": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("project 1 measures = %v, want %v", got, want)
	}
	if got, want := rawNames(t, db, 2, store.FamilyProduct), map[string]int{"a": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("project 2 product = %v, want %v", got, want)
	}
}

func TestRenameEventNoRowsWritesRuleOnly(t *testing.T) {
	db := ruleDB(t)
	got := rename(t, db, 1, store.FamilyProduct, "ghost", "spirit", false)
	if want := map[string]int64{"events": 0, "agg_product_daily": 0, "agg_product_attrs": 0}; !reflect.DeepEqual(got, want) {
		t.Errorf("result = %v, want %v", got, want)
	}
	if got, want := loadRules(t, db), []store.EventRule{rule(1, store.FamilyProduct, "ghost", store.EventRuleRename, "spirit")}; !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want %v", got, want)
	}
}

func TestDeleteProjectDataRemovesEventRules(t *testing.T) {
	db := ruleDB(t)
	rename(t, db, 2, store.FamilyProduct, "a", "c", false)
	if err := db.BlockEvent(context.Background(), 2, store.FamilyMeasures, "z", ruleAudit); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteProjectData(context.Background(), 2, store.AuditEntry{Actor: "cli", Action: "project.delete", Subject: "2"}); err != nil {
		t.Fatal(err)
	}
	if rules := loadRules(t, db); len(rules) != 0 {
		t.Errorf("rules = %v, want none", rules)
	}
}

// writeWithAttr stores n events of one name carrying plan=pro, so the
// rollup has an attribute breakdown to write.
func writeWithAttr(t *testing.T, db *DB, family store.Family, name string, n int) {
	t.Helper()
	var evs []store.Event
	for i := 0; i < n; i++ {
		e := store.Event{Family: family, ID: fmt.Sprintf("%s-%s-%d", family, name, i),
			ProjectID: 1, EventName: name, TS: ts("2026-10-10T10:00:00Z"),
			ReceivedAt: ts("2026-10-10T10:00:00Z"), Kind: "app", ActorID: "a", ActorKind: store.ActorConnection,
			Attributes: map[string]string{"plan": "pro"}}
		if family == store.FamilyMeasures {
			v := 1.5
			e.Value, e.Measure = &v, "m"
		}
		evs = append(evs, e)
	}
	if err := db.WriteEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

func countOf(t *testing.T, db *DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// A blocked name is deleted with the day instead of rolled up (D10): the
// totals count only what was kept.
func TestAggregateProductDaySkipsBlocked(t *testing.T) {
	db := ruleDB(t)
	ctx := context.Background()
	writeWithAttr(t, db, store.FamilyProduct, "keep", 2)
	writeWithAttr(t, db, store.FamilyProduct, "spam", 3)
	if err := db.BlockEvent(ctx, 1, store.FamilyProduct, "spam", ruleAudit); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateProductDay(ctx, 1, day("2026-10-10"), []string{"plan"}, defaultAttrsTopN); err != nil {
		t.Fatal(err)
	}
	if got := countOf(t, db, `SELECT COUNT(*) FROM agg_product_daily WHERE project_id=1 AND day='2026-10-10' AND event_name='keep' AND count=2`); got != 1 {
		t.Errorf("keep daily rows = %d, want 1 with count 2", got)
	}
	if got := countOf(t, db, `SELECT COUNT(*) FROM agg_product_daily WHERE project_id=1 AND event_name='spam'`); got != 0 {
		t.Errorf("spam daily rows = %d, want 0", got)
	}
	if got := countOf(t, db, `SELECT total_events FROM agg_product_totals WHERE project_id=1 AND day='2026-10-10'`); got != 2 {
		t.Errorf("total_events = %d, want 2", got)
	}
	if got := countOf(t, db, `SELECT COUNT(*) FROM raw_product WHERE project_id=1 AND day='2026-10-10'`); got != 0 {
		t.Errorf("raw product rows left = %d, want 0", got)
	}
	if got := countOf(t, db, `SELECT COUNT(*) FROM agg_product_attrs WHERE project_id=1 AND event_name='spam'`); got != 0 {
		t.Errorf("spam attr rows = %d, want 0", got)
	}
	if got := countOf(t, db, `SELECT COUNT(*) FROM agg_product_attrs WHERE project_id=1 AND event_name='keep'`); got == 0 {
		t.Error("keep attr rows missing")
	}
}

// A day holding only blocked names ends with no raw rows and no totals row
// (no phantom day of zero events).
func TestAggregateProductDayAllBlocked(t *testing.T) {
	db := ruleDB(t)
	ctx := context.Background()
	writeNamed(t, db, 1, store.FamilyProduct, "spam", 3)
	if err := db.BlockEvent(ctx, 1, store.FamilyProduct, "spam", ruleAudit); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateProductDay(ctx, 1, day("2026-10-10"), nil, defaultAttrsTopN); err != nil {
		t.Fatal(err)
	}
	if got := countOf(t, db, `SELECT COUNT(*) FROM raw_product WHERE project_id=1 AND day='2026-10-10'`); got != 0 {
		t.Errorf("raw product rows left = %d, want 0", got)
	}
	if got := countOf(t, db, `SELECT COUNT(*) FROM agg_product_totals WHERE project_id=1 AND day='2026-10-10'`); got != 0 {
		t.Errorf("totals rows = %d, want 0", got)
	}
	if got := countOf(t, db, `SELECT COUNT(*) FROM agg_product_daily WHERE project_id=1`); got != 0 {
		t.Errorf("daily rows = %d, want 0", got)
	}
}

func TestAggregateMeasureDaySkipsBlocked(t *testing.T) {
	db := ruleDB(t)
	ctx := context.Background()
	writeWithAttr(t, db, store.FamilyMeasures, "fast", 2)
	writeWithAttr(t, db, store.FamilyMeasures, "slow", 3)
	if err := db.BlockEvent(ctx, 1, store.FamilyMeasures, "slow", ruleAudit); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateMeasureDay(ctx, 1, day("2026-10-10"), []string{"plan"}, defaultAttrsTopN); err != nil {
		t.Fatal(err)
	}
	if got := countOf(t, db, `SELECT SUM(samples) FROM agg_measures_daily WHERE project_id=1 AND event_name='fast'`); got != 2 {
		t.Errorf("fast samples = %d, want 2", got)
	}
	if got := countOf(t, db, `SELECT COUNT(*) FROM agg_measures_daily WHERE project_id=1 AND event_name='slow'`); got != 0 {
		t.Errorf("slow daily rows = %d, want 0", got)
	}
	if got := countOf(t, db, `SELECT COUNT(*) FROM agg_measures_attrs WHERE project_id=1 AND event_name='slow'`); got != 0 {
		t.Errorf("slow attr rows = %d, want 0", got)
	}
	if got := countOf(t, db, `SELECT COUNT(*) FROM raw_measures WHERE project_id=1 AND day='2026-10-10'`); got != 0 {
		t.Errorf("raw measure rows left = %d, want 0", got)
	}
}

// Aggregates of a name blocked after they were written go at the next pass
// (D11); other names, the totals and other projects stay.
func TestDropBlockedAggregates(t *testing.T) {
	db := ruleDB(t)
	ctx := context.Background()
	for _, p := range []int{1, 2} {
		for _, name := range []string{"spam", "keep", "slow", "fast"} {
			execAll(t, db,
				fmt.Sprintf(`INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users) VALUES (%d, '2026-10-01', '%s', 4, 2)`, p, name),
				fmt.Sprintf(`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users) VALUES (%d, '2026-10-01', '%s', 'plan', 'pro', 4, 2)`, p, name),
				fmt.Sprintf(`INSERT INTO agg_measures_daily (project_id, day, event_name, measure, bucket, samples, weight, sum) VALUES (%d, '2026-10-01', '%s', 'm', 1, 4, 4, 6)`, p, name),
				fmt.Sprintf(`INSERT INTO agg_measures_attrs (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum) VALUES (%d, '2026-10-01', '%s', 'm', 'plan', 'pro', 1, 4, 4, 6)`, p, name))
		}
		execAll(t, db, fmt.Sprintf(`INSERT INTO agg_product_totals (project_id, day, total_events, active_users) VALUES (%d, '2026-10-01', 16, 2)`, p))
	}
	if err := db.BlockEvent(ctx, 1, store.FamilyProduct, "spam", ruleAudit); err != nil {
		t.Fatal(err)
	}
	if err := db.BlockEvent(ctx, 1, store.FamilyMeasures, "slow", ruleAudit); err != nil {
		t.Fatal(err)
	}
	// Run twice: the second pass is a no-op.
	for i := 0; i < 2; i++ {
		if err := db.DropBlockedAggregates(ctx, 1); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			table, name string
			project     int
			want        int
		}{
			{"agg_product_daily", "spam", 1, 0}, {"agg_product_attrs", "spam", 1, 0},
			{"agg_measures_daily", "slow", 1, 0}, {"agg_measures_attrs", "slow", 1, 0},
			{"agg_product_daily", "keep", 1, 1}, {"agg_product_attrs", "keep", 1, 1},
			{"agg_measures_daily", "fast", 1, 1}, {"agg_measures_attrs", "fast", 1, 1},
			// The family decides: a product block does not touch a measure
			// of the same name, nor the reverse.
			{"agg_measures_daily", "spam", 1, 1}, {"agg_measures_attrs", "spam", 1, 1},
			{"agg_product_daily", "slow", 1, 1}, {"agg_product_attrs", "slow", 1, 1},
			// Project 2 has no rules.
			{"agg_product_daily", "spam", 2, 1}, {"agg_product_attrs", "spam", 2, 1},
			{"agg_measures_daily", "slow", 2, 1}, {"agg_measures_attrs", "slow", 2, 1},
		} {
			got := countOf(t, db, `SELECT COUNT(*) FROM `+tc.table+` WHERE project_id=? AND event_name=?`, tc.project, tc.name)
			if got != tc.want {
				t.Errorf("pass %d: %s project %d %s rows = %d, want %d", i+1, tc.table, tc.project, tc.name, got, tc.want)
			}
		}
		for _, p := range []int{1, 2} {
			if got := countOf(t, db, `SELECT total_events FROM agg_product_totals WHERE project_id=? AND day='2026-10-01'`, p); got != 16 {
				t.Errorf("pass %d: project %d total_events = %d, want 16", i+1, p, got)
			}
		}
	}
}
