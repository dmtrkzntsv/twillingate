package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

var attrAudit = store.AuditEntry{Actor: "cli", Action: "attribute.rename", Subject: "1/a"}

// writeAttrs stores one event of the family carrying attrs, on 2026-10-10.
func writeAttrs(t *testing.T, db *DB, project int64, family store.Family, id string, attrs map[string]string) {
	t.Helper()
	e := store.Event{Family: family, ID: id, ProjectID: project, EventName: "e",
		TS: ts("2026-10-10T10:00:00Z"), ReceivedAt: ts("2026-10-10T10:00:00Z"), Kind: "app",
		ActorID: "a", ActorKind: store.ActorConnection, Attributes: attrs}
	switch family {
	case store.FamilyMeasures:
		v := 1.5
		e.Value, e.Measure = &v, "m"
	case store.FamilyViews:
		e.Kind, e.Path = "web", "/"
	}
	if err := db.WriteEvents(context.Background(), []store.Event{e}); err != nil {
		t.Fatal(err)
	}
}

// rawAttrs reads every stored row's attributes by id.
func rawAttrs(t *testing.T, db *DB) map[string]map[string]any {
	t.Helper()
	rows, err := db.db.Query(`SELECT id, attributes FROM events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]map[string]any{}
	for rows.Next() {
		var id, blob string
		if err := rows.Scan(&id, &blob); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(blob), &m); err != nil {
			t.Fatalf("row %s: %v", id, err)
		}
		out[id] = m
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func loadAttrRules(t *testing.T, db *DB) []store.AttributeRule {
	t.Helper()
	rules, err := db.LoadAttributeRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range rules {
		rules[i].CreatedAt = ""
	}
	return rules
}

func attrRule(project int64, key, action, to string) store.AttributeRule {
	return store.AttributeRule{ProjectID: project, Key: key, Action: action, To: to}
}

func renameAttr(t *testing.T, db *DB, project int64, from, to string) {
	t.Helper()
	if err := db.RenameAttribute(context.Background(), project, from, to, attrAudit); err != nil {
		t.Fatal(err)
	}
}

func blockAttr(t *testing.T, db *DB, project int64, key string) {
	t.Helper()
	if err := db.BlockAttribute(context.Background(), project, key,
		store.AuditEntry{Actor: "cli", Action: "attribute.block", Subject: key}); err != nil {
		t.Fatal(err)
	}
}

// usageRows reads usage_attributes as "project/day/key events max" lines.
func usageRows(t *testing.T, db *DB) []string {
	t.Helper()
	return queryStrings(t, db, `SELECT project_id || '/' || day || '/' || attr_key || ' ' || events || ' ' || COALESCE(max_values, 'null')
		FROM usage_attributes ORDER BY project_id, day, attr_key`)
}

// A rename writes its rule and its audit row, and touches no event and no
// catalog row: with the events table, the attribute aggregates and
// usage_attributes gone, it still succeeds.
func TestRenameAttributeWritesOnlyRules(t *testing.T) {
	db := ruleDB(t)
	execAll(t, db,
		`INSERT INTO project_attributes (project_id, key, breakdown) VALUES (1, 'a', 1), (1, 'b', 0), (2, 'a', 0)`,
		`DROP VIEW raw_product`, `DROP VIEW raw_measures`, `DROP VIEW raw_views`, `DROP TABLE events`,
		`DROP TABLE agg_product_attrs`, `DROP TABLE agg_measures_attrs`, `DROP TABLE usage_attributes`)

	renameAttr(t, db, 1, "a", "c")

	if got, want := catalogRows(t, db), []string{"1/a=1", "1/b=0", "2/a=0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("catalog = %v, want it untouched %v", got, want)
	}
	if got, want := loadAttrRules(t, db), []store.AttributeRule{attrRule(1, "a", store.EventRuleRename, "c")}; !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want %v", got, want)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='attribute.rename' AND subject='1/a'`); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
	// The event rules are another kind: none was written.
	if rules := loadRules(t, db); len(rules) != 0 {
		t.Errorf("event rules = %v, want none", rules)
	}
}

func TestRenameAttributeOntoBlockedIsConflict(t *testing.T) {
	db := ruleDB(t)
	blockAttr(t, db, 1, "b")
	wantRules := loadAttrRules(t, db)
	err := db.RenameAttribute(context.Background(), 1, "a", "b", attrAudit)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "b is blocked") {
		t.Errorf("err = %q, want it to say the target is blocked", err)
	}
	if got := loadAttrRules(t, db); !reflect.DeepEqual(got, wantRules) {
		t.Errorf("rules = %v, want unchanged %v", got, wantRules)
	}
	// An event block of the same name is another kind: no conflict.
	if err := db.BlockEvent(context.Background(), 1, store.FamilyProduct, "c", ruleAudit); err != nil {
		t.Fatal(err)
	}
	renameAttr(t, db, 1, "a", "c")
}

// A target renamed to a third key is refused, as for event names; renaming
// back onto the key it maps to is allowed.
func TestRenameAttributeRefusesARenamedTarget(t *testing.T) {
	db := ruleDB(t)
	renameAttr(t, db, 1, "a", "b")
	renameAttr(t, db, 1, "b", "x")
	want := loadAttrRules(t, db)
	audit := count(t, db, `SELECT COUNT(*) FROM audit_log`)
	err := db.RenameAttribute(context.Background(), 1, "c", "b", attrAudit)
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "b is renamed to x: rename into x, or remove that rule first") {
		t.Fatalf("err = %v, want ErrConflict naming x", err)
	}
	if got := loadAttrRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want unchanged %v", got, want)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log`); n != audit {
		t.Errorf("audit rows = %d, want %d", n, audit)
	}
	// Another project's rename of b does not count.
	renameAttr(t, db, 2, "c", "b")
	renameAttr(t, db, 1, "x", "b")
}

func TestRenameAttributeSameKeyIsInvalid(t *testing.T) {
	db := ruleDB(t)
	if err := db.RenameAttribute(context.Background(), 1, "a", "a", attrAudit); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

// Chains collapse (D7): no target is itself renamed away, and renaming
// back drops the rule on the new target.
func TestAttributeRuleChains(t *testing.T) {
	db := ruleDB(t)
	renameAttr(t, db, 1, "a", "b")
	renameAttr(t, db, 1, "b", "c")
	want := []store.AttributeRule{
		attrRule(1, "a", store.EventRuleRename, "c"),
		attrRule(1, "b", store.EventRuleRename, "c"),
	}
	if got := loadAttrRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("after b>c: rules = %v, want %v", got, want)
	}
	renameAttr(t, db, 1, "c", "a")
	want = []store.AttributeRule{
		attrRule(1, "b", store.EventRuleRename, "a"),
		attrRule(1, "c", store.EventRuleRename, "a"),
	}
	if got := loadAttrRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("after c>a: rules = %v, want %v", got, want)
	}
}

// A key is addressed as itself, never as a path: the mapping at insert
// renames a key with a dot, a slash, a space, an accent, a backslash or a
// double quote as it is.
func TestAttributeKeyWithDotAndSlash(t *testing.T) {
	db := ruleDB(t)
	renameAttr(t, db, 1, "a.b", "c")
	renameAttr(t, db, 1, "x/y", "checkout/v2")
	renameAttr(t, db, 1, "plan type", "é2")
	renameAttr(t, db, 1, "é", "e acute")
	renameAttr(t, db, 1, `a\b`, `back\slash`)
	renameAttr(t, db, 1, `q"r`, `quote"d`)
	writeAttrs(t, db, 1, store.FamilyProduct, "later", map[string]string{"a.b": "1", "x/y": "2", "plan type": "3", "é": "4",
		`a\b`: "5", `q"r`: "6"})
	want := map[string]any{"c": "1", "checkout/v2": "2", "é2": "3", "e acute": "4", `back\slash`: "5", `quote"d`: "6"}
	if got := rawAttrs(t, db)["later"]; !reflect.DeepEqual(got, want) {
		t.Errorf("later = %v, want %v", got, want)
	}
}

func TestBlockAttribute(t *testing.T) {
	db := ruleDB(t)
	ctx := context.Background()
	renameAttr(t, db, 1, "z", "a")
	execAll(t, db, `INSERT INTO project_attributes (project_id, key, breakdown) VALUES (1, 'a', 1)`)
	blockAttr(t, db, 1, "a")
	want := []store.AttributeRule{
		attrRule(1, "a", store.EventRuleBlock, ""),
		attrRule(1, "z", store.EventRuleRename, "a"),
	}
	if got := loadAttrRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want %v", got, want)
	}
	if got, want := catalogRows(t, db), []string{"1/a=1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("catalog = %v, want it untouched %v", got, want)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='attribute.block'`); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}

	// Blocking a key renamed away replaces its rename: one rule per key.
	renameAttr(t, db, 1, "p", "q")
	blockAttr(t, db, 1, "p")
	want = []store.AttributeRule{
		attrRule(1, "a", store.EventRuleBlock, ""),
		attrRule(1, "p", store.EventRuleBlock, ""),
		attrRule(1, "z", store.EventRuleRename, "a"),
	}
	if got := loadAttrRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("after blocking p: rules = %v, want %v", got, want)
	}

	// Removing a rule deletes it alone; no rule is not found, and not audited.
	remove := store.AuditEntry{Actor: "cli", Action: "attribute.remove_rule", Subject: "1/p"}
	if err := db.RemoveAttributeRule(ctx, 1, "p", remove); err != nil {
		t.Fatal(err)
	}
	want = []store.AttributeRule{
		attrRule(1, "a", store.EventRuleBlock, ""),
		attrRule(1, "z", store.EventRuleRename, "a"),
	}
	if got := loadAttrRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("after removing p: rules = %v, want %v", got, want)
	}
	if err := db.RemoveAttributeRule(ctx, 1, "p", remove); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	// An event rule of the same name is not an attribute rule.
	if err := db.BlockEvent(ctx, 1, store.FamilyProduct, "n", ruleAudit); err != nil {
		t.Fatal(err)
	}
	if err := db.RemoveAttributeRule(ctx, 1, "n", remove); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("event rule name: err = %v, want ErrNotFound", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='attribute.remove_rule'`); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
}

func TestWriteEventsMapsAttributes(t *testing.T) {
	db := ruleDB(t)
	renameAttr(t, db, 1, "a", "b")
	blockAttr(t, db, 1, "x")
	writeAttrs(t, db, 1, store.FamilyProduct, "p1", map[string]string{"a": "1", "x": "2", "y": "3"})
	writeAttrs(t, db, 1, store.FamilyMeasures, "m1", map[string]string{"a": "4"})
	writeAttrs(t, db, 1, store.FamilyViews, "v1", map[string]string{"a": "5", "x": "6"})
	writeAttrs(t, db, 2, store.FamilyProduct, "o1", map[string]string{"a": "7", "x": "8"})

	want := map[string]map[string]any{
		"p1": {"b": "1", "y": "3"},
		"m1": {"b": "4"},
		"v1": {"a": "5", "x": "6"}, // views are left alone
		"o1": {"a": "7", "x": "8"}, // no rules in project 2
	}
	if got := rawAttrs(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("raw = %v, want %v", got, want)
	}
	if got, want := usageRows(t, db), []string{
		"1/2026-10-10/b 0 null", "1/2026-10-10/y 0 null", "2/2026-10-10/a 0 null", "2/2026-10-10/x 0 null",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("usage_attributes = %v, want %v", got, want)
	}
	if got := catalogRows(t, db); len(got) != 0 {
		t.Errorf("catalog = %v, want no rows written", got)
	}
}
