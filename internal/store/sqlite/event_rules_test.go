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

// ruleDB is a test database with projects 1 and 2 present (rules
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

func rename(t *testing.T, db *DB, project int64, family store.Family, from, to string) {
	t.Helper()
	if err := db.RenameEvent(context.Background(), project, family, from, to, ruleAudit); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRenameEventChainsCollapseAndRenameBack(t *testing.T) {
	db := ruleDB(t)
	p := store.FamilyProduct
	rename(t, db, 1, p, "A", "B")
	rename(t, db, 1, p, "B", "C")
	want := []store.EventRule{
		rule(1, p, "A", store.EventRuleRename, "C"),
		rule(1, p, "B", store.EventRuleRename, "C"),
	}
	if got := loadRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("after A>B, B>C: rules = %v, want %v", got, want)
	}
	rename(t, db, 1, p, "C", "A")
	want = []store.EventRule{
		rule(1, p, "B", store.EventRuleRename, "A"),
		rule(1, p, "C", store.EventRuleRename, "A"),
	}
	if got := loadRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("after C>A: rules = %v, want %v", got, want)
	}
}

// A target renamed to a third name is refused: collapsing the chain would
// delete that rule, and the history stored before it would read under the
// target again. Renaming back onto the name it maps to is allowed.
func TestRenameEventRefusesARenamedTarget(t *testing.T) {
	db := ruleDB(t)
	p := store.FamilyProduct
	rename(t, db, 1, p, "a", "b")
	rename(t, db, 1, p, "b", "x")
	want := loadRules(t, db)
	audit := count(t, db, `SELECT COUNT(*) FROM audit_log`)
	err := db.RenameEvent(context.Background(), 1, p, "c", "b", ruleAudit)
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "b is renamed to x: rename into x, or remove that rule first") {
		t.Fatalf("err = %v, want ErrConflict naming x", err)
	}
	if got := loadRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want unchanged %v", got, want)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log`); n != audit {
		t.Errorf("audit rows = %d, want %d", n, audit)
	}
	// Another family's rename of b does not count.
	rename(t, db, 1, store.FamilyMeasures, "c", "b")
	// Renaming x back to b drops b's rule, as the chain tests show.
	rename(t, db, 1, p, "x", "b")
}

// Renaming a blocked name replaces its block; blocking a rename's target
// later leaves the rename pointing at it (ingest then drops both names).
func TestRenameEventReplacesBlockAndKeepsTargetBlock(t *testing.T) {
	db := ruleDB(t)
	p := store.FamilyProduct
	ctx := context.Background()
	if err := db.BlockEvent(ctx, 1, p, "X", ruleAudit); err != nil {
		t.Fatal(err)
	}
	rename(t, db, 1, p, "X", "Y")
	rename(t, db, 1, p, "W", "Z")
	if err := db.BlockEvent(ctx, 1, p, "Z", ruleAudit); err != nil {
		t.Fatal(err)
	}
	want := []store.EventRule{
		rule(1, p, "W", store.EventRuleRename, "Z"),
		rule(1, p, "X", store.EventRuleRename, "Y"),
		rule(1, p, "Z", store.EventRuleBlock, ""),
	}
	if got := loadRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want %v", got, want)
	}
}

// A blocked target is refused: the files read none of a blocked name, so
// a rename onto it would drop what it moves.
func TestRenameEventRefusesBlockedTarget(t *testing.T) {
	db := ruleDB(t)
	p := store.FamilyProduct
	ctx := context.Background()
	if err := db.BlockEvent(ctx, 1, p, "b", ruleAudit); err != nil {
		t.Fatal(err)
	}
	wantRules := loadRules(t, db)
	auditRows := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='event.rename'`)

	err := db.RenameEvent(ctx, 1, p, "a", "b", ruleAudit)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "b is blocked") {
		t.Errorf("err = %q, want it to say the target is blocked", err)
	}
	if got := loadRules(t, db); !reflect.DeepEqual(got, wantRules) {
		t.Errorf("rules = %v, want unchanged %v", got, wantRules)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='event.rename'`); n != auditRows {
		t.Errorf("audit rows = %d, want %d", n, auditRows)
	}

	// Another family's block does not count.
	if err := db.RenameEvent(ctx, 1, store.FamilyMeasures, "a", "b", ruleAudit); err != nil {
		t.Errorf("measures rename with only a product block: %v", err)
	}
}

// A rename writes its rule and audit row and touches no event: with the
// events table and every view over it gone, it still succeeds.
func TestRenameEventWritesOnlyRules(t *testing.T) {
	db := ruleDB(t)
	execAll(t, db, `DROP VIEW raw_product`, `DROP VIEW raw_measures`, `DROP VIEW raw_views`, `DROP TABLE events`,
		`DROP TABLE agg_product_daily`, `DROP TABLE agg_product_attrs`,
		`DROP TABLE agg_measures_daily`, `DROP TABLE agg_measures_attrs`)
	rename(t, db, 1, store.FamilyProduct, "a", "b")
	rename(t, db, 1, store.FamilyMeasures, "m", "n")
	want := []store.EventRule{
		rule(1, store.FamilyMeasures, "m", store.EventRuleRename, "n"),
		rule(1, store.FamilyProduct, "a", store.EventRuleRename, "b"),
	}
	if got := loadRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want %v", got, want)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='event.rename'`); n != 2 {
		t.Errorf("audit rows = %d, want 2", n)
	}
}

func TestBlockEventIsIdempotentAndReplacesRename(t *testing.T) {
	db := ruleDB(t)
	p := store.FamilyProduct
	ctx := context.Background()
	if err := db.BlockEvent(ctx, 1, p, "N", ruleAudit); err != nil {
		t.Fatal(err)
	}
	execAll(t, db, `UPDATE rules SET created_at='2026-01-01T00:00:00Z'`)
	if err := db.BlockEvent(ctx, 1, p, "N", ruleAudit); err != nil {
		t.Fatal(err)
	}
	var created string
	if err := db.db.QueryRow(`SELECT created_at FROM rules WHERE kind='event' AND name='N'`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if n := len(loadRules(t, db)); n != 1 || created != "2026-01-01T00:00:00Z" {
		t.Errorf("rules = %d, created_at = %s, want one rule, unchanged", n, created)
	}

	rename(t, db, 1, p, "P", "Q")
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
	rename(t, db, 1, store.FamilyProduct, "a", "c")
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

func TestDeleteProjectDataRemovesEventRules(t *testing.T) {
	db := ruleDB(t)
	rename(t, db, 2, store.FamilyProduct, "a", "c")
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
