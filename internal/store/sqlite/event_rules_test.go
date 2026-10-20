package sqlite

import (
	"context"
	"errors"
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

// A rename writes its rule and audit row, and nothing else.
func TestRenameEventWritesOnlyRules(t *testing.T) {
	db := ruleDB(t)
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
