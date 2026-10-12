package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func renameAttr(t *testing.T, db *DB, project int64, from, to string, merge bool) map[string]int64 {
	t.Helper()
	got, err := db.RenameAttribute(context.Background(), project, from, to, merge, attrAudit)
	if err != nil {
		t.Fatal(err)
	}
	return got
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

func TestRenameAttributeRewritesEverything(t *testing.T) {
	db := ruleDB(t)
	writeAttrs(t, db, 1, store.FamilyProduct, "p1", map[string]string{"a": "1"})
	writeAttrs(t, db, 1, store.FamilyProduct, "p2", map[string]string{"a": "1"})
	writeAttrs(t, db, 1, store.FamilyProduct, "p3", map[string]string{"a": "2", "b": "9"})
	writeAttrs(t, db, 1, store.FamilyMeasures, "m1", map[string]string{"a": "3"})
	writeAttrs(t, db, 1, store.FamilyViews, "v1", map[string]string{"a": "v"})
	writeAttrs(t, db, 2, store.FamilyProduct, "o1", map[string]string{"a": "1"})
	execAll(t, db,
		`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups) VALUES
		 (1, '2026-01-01', 'e', 'a', '1', 5, 2, 1), (1, '2026-01-01', 'e', 'a', '2', 1, 1, NULL),
		 (1, '2026-01-01', 'e', 'b', '9', 1, 1, NULL), (2, '2026-01-01', 'e', 'a', '1', 1, 1, NULL)`,
		`INSERT INTO agg_measures_attrs (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
		 VALUES (1, '2026-01-01', 'e', 'm', 'a', '3', 1, 4, 4, 6)`,
		`INSERT INTO usage_attributes (project_id, day, attr_key, events, max_values) VALUES (1, '2026-10-09', 'a', 5, 2)`,
		`UPDATE project_attributes SET breakdown = 1 WHERE project_id = 1 AND key = 'a'`)

	got := renameAttr(t, db, 1, "a", "c", false)

	if want := map[string]int64{"events": 4, "agg_product_attrs": 2, "agg_measures_attrs": 1, "usage_attributes": 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("result = %v, want %v", got, want)
	}
	wantRaw := map[string]map[string]any{
		"p1": {"c": "1"}, "p2": {"c": "1"}, "p3": {"c": "2", "b": "9"}, "m1": {"c": "3"},
		"v1": {"a": "v"}, "o1": {"a": "1"},
	}
	if got := rawAttrs(t, db); !reflect.DeepEqual(got, wantRaw) {
		t.Errorf("raw = %v, want %v", got, wantRaw)
	}
	if got, want := queryStrings(t, db, `SELECT project_id || '/' || attr_key || '=' || attr_value FROM agg_product_attrs ORDER BY 1`),
		[]string{"1/b=9", "1/c=1", "1/c=2", "2/a=1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("agg_product_attrs = %v, want %v", got, want)
	}
	if got, want := queryStrings(t, db, `SELECT attr_key FROM agg_measures_attrs`), []string{"c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("agg_measures_attrs = %v, want %v", got, want)
	}
	if got, want := usageRows(t, db), []string{
		"1/2026-10-09/c 5 2", "1/2026-10-10/b 0 null", "1/2026-10-10/c 0 null", "2/2026-10-10/a 0 null",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("usage_attributes = %v, want %v", got, want)
	}
	if got, want := catalogRows(t, db), []string{"1/b=0", "1/c=1", "2/a=0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("catalog = %v, want %v", got, want)
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

func TestRenameAttributeTargetWins(t *testing.T) {
	db := ruleDB(t)
	writeAttrs(t, db, 1, store.FamilyProduct, "p1", map[string]string{"a": "1", "b": "2"})
	writeAttrs(t, db, 1, store.FamilyProduct, "p2", map[string]string{"a": "3"})
	renameAttr(t, db, 1, "a", "b", true)
	if got, want := rawAttrs(t, db), map[string]map[string]any{"p1": {"b": "2"}, "p2": {"b": "3"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("raw = %v, want %v", got, want)
	}
}

func TestRenameAttributeRefusesTakenTarget(t *testing.T) {
	for name, seed := range map[string]string{
		"usage": `INSERT INTO usage_attributes (project_id, day, attr_key, events) VALUES (1, '2026-10-01', 'b', 1)`,
		"product aggregate": `INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
			VALUES (1, '2026-01-02', 'e', 'b', 'v', 1, 1, NULL)`,
		"measures aggregate": `INSERT INTO agg_measures_attrs (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum)
			VALUES (1, '2026-01-02', 'e', 'm', 'b', 'v', 1, 1, 1, 1)`,
	} {
		t.Run(name, func(t *testing.T) {
			db := ruleDB(t)
			writeAttrs(t, db, 1, store.FamilyProduct, "p1", map[string]string{"a": "1"})
			execAll(t, db, seed)
			wantRaw, wantUsage, wantCatalog := rawAttrs(t, db), usageRows(t, db), catalogRows(t, db)

			_, err := db.RenameAttribute(context.Background(), 1, "a", "b", false, attrAudit)
			if !errors.Is(err, store.ErrConflict) {
				t.Fatalf("err = %v, want ErrConflict", err)
			}
			if got := rawAttrs(t, db); !reflect.DeepEqual(got, wantRaw) {
				t.Errorf("raw = %v, want unchanged %v", got, wantRaw)
			}
			if got := usageRows(t, db); !reflect.DeepEqual(got, wantUsage) {
				t.Errorf("usage = %v, want unchanged %v", got, wantUsage)
			}
			if got := catalogRows(t, db); !reflect.DeepEqual(got, wantCatalog) {
				t.Errorf("catalog = %v, want unchanged %v", got, wantCatalog)
			}
			if rules := loadAttrRules(t, db); len(rules) != 0 {
				t.Errorf("rules = %v, want none", rules)
			}
			if n := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='attribute.rename'`); n != 0 {
				t.Errorf("audit rows = %d, want none", n)
			}
			// Another project's data under the name is no conflict.
			if _, err := db.RenameAttribute(context.Background(), 2, "a", "b", false, attrAudit); err != nil {
				t.Errorf("project 2: %v", err)
			}
		})
	}
}

func TestMergeAttributeSumsAggregates(t *testing.T) {
	db := ruleDB(t)
	execAll(t, db,
		// v: groups on both sides; w: NULL on one side; x: only a.
		`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups) VALUES
		 (1, '2026-01-01', 'e', 'a', 'v', 5, 2, 1), (1, '2026-01-01', 'e', 'b', 'v', 7, 3, 2),
		 (1, '2026-01-01', 'e', 'a', 'w', 1, 1, NULL), (1, '2026-01-01', 'e', 'b', 'w', 1, 1, 4),
		 (1, '2026-01-01', 'e', 'a', 'x', 2, 2, 2)`,
		`INSERT INTO agg_measures_attrs (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum) VALUES
		 (1, '2026-01-01', 'e', 'm', 'a', 'v', 1, 4, 4, 6), (1, '2026-01-01', 'e', 'm', 'b', 'v', 1, 1, 2, 3)`,
		`INSERT INTO usage_attributes (project_id, day, attr_key, events, max_values) VALUES
		 (1, '2026-10-01', 'a', 3, 2), (1, '2026-10-01', 'b', 4, 5),
		 (1, '2026-10-02', 'a', 1, 7), (1, '2026-10-02', 'b', 1, NULL),
		 (1, '2026-10-03', 'a', 2, NULL), (1, '2026-10-03', 'b', 2, 3),
		 (1, '2026-10-04', 'a', 2, NULL), (1, '2026-10-04', 'b', 2, NULL)`,
		`INSERT INTO project_attributes (project_id, key, breakdown) VALUES (1, 'a', 1), (1, 'b', 0)`)

	got := renameAttr(t, db, 1, "a", "b", true)
	if want := map[string]int64{"events": 0, "agg_product_attrs": 3, "agg_measures_attrs": 1, "usage_attributes": 4}; !reflect.DeepEqual(got, want) {
		t.Errorf("result = %v, want %v", got, want)
	}
	if got, want := queryStrings(t, db, `SELECT attr_key || '=' || attr_value || ' ' || count || ' ' || unique_users || ' ' || COALESCE(unique_groups, 'null')
		FROM agg_product_attrs ORDER BY 1`),
		[]string{"b=v 12 5 3", "b=w 2 2 null", "b=x 2 2 2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("agg_product_attrs = %v, want %v", got, want)
	}
	if got, want := queryStrings(t, db, `SELECT attr_key || '=' || attr_value || ' ' || samples || ' ' || weight || ' ' || sum FROM agg_measures_attrs`),
		[]string{"b=v 5 6.0 9.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("agg_measures_attrs = %v, want %v", got, want)
	}
	if got, want := usageRows(t, db), []string{
		"1/2026-10-01/b 7 5", "1/2026-10-02/b 2 7", "1/2026-10-03/b 4 3", "1/2026-10-04/b 4 null",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("usage_attributes = %v, want %v", got, want)
	}
	if got, want := catalogRows(t, db), []string{"1/b=1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("catalog = %v, want %v", got, want)
	}

	// Merging a key that is no breakdown into one that is keeps it.
	execAll(t, db, `INSERT INTO project_attributes (project_id, key, breakdown) VALUES (1, 'z', 0)`)
	renameAttr(t, db, 1, "z", "b", true)
	if got, want := catalogRows(t, db), []string{"1/b=1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("catalog after z>b = %v, want %v", got, want)
	}
}

func TestRenameAttributeOntoBlockedIsConflict(t *testing.T) {
	db := ruleDB(t)
	writeAttrs(t, db, 1, store.FamilyProduct, "p1", map[string]string{"a": "1"})
	blockAttr(t, db, 1, "b")
	wantRaw, wantRules := rawAttrs(t, db), loadAttrRules(t, db)
	for _, merge := range []bool{false, true} {
		_, err := db.RenameAttribute(context.Background(), 1, "a", "b", merge, attrAudit)
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("merge=%v: err = %v, want ErrConflict", merge, err)
		}
		if !strings.Contains(err.Error(), "b is blocked") {
			t.Errorf("merge=%v: err = %q, want it to say the target is blocked", merge, err)
		}
	}
	if got := rawAttrs(t, db); !reflect.DeepEqual(got, wantRaw) {
		t.Errorf("raw = %v, want unchanged %v", got, wantRaw)
	}
	if got := loadAttrRules(t, db); !reflect.DeepEqual(got, wantRules) {
		t.Errorf("rules = %v, want unchanged %v", got, wantRules)
	}
	// An event block of the same name is another kind: no conflict.
	if err := db.BlockEvent(context.Background(), 1, store.FamilyProduct, "c", ruleAudit); err != nil {
		t.Fatal(err)
	}
	renameAttr(t, db, 1, "a", "c", false)
}

func TestRenameAttributeSameKeyIsInvalid(t *testing.T) {
	db := ruleDB(t)
	if _, err := db.RenameAttribute(context.Background(), 1, "a", "a", false, attrAudit); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

// Chains collapse (D7): no target is itself renamed away, and renaming
// back drops the rule on the new target.
func TestAttributeRuleChains(t *testing.T) {
	db := ruleDB(t)
	renameAttr(t, db, 1, "a", "b", false)
	renameAttr(t, db, 1, "b", "c", false)
	want := []store.AttributeRule{
		attrRule(1, "a", store.EventRuleRename, "c"),
		attrRule(1, "b", store.EventRuleRename, "c"),
	}
	if got := loadAttrRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("after b>c: rules = %v, want %v", got, want)
	}
	renameAttr(t, db, 1, "c", "a", false)
	want = []store.AttributeRule{
		attrRule(1, "b", store.EventRuleRename, "a"),
		attrRule(1, "c", store.EventRuleRename, "a"),
	}
	if got := loadAttrRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("after c>a: rules = %v, want %v", got, want)
	}
}

// A key is addressed as itself, never as a path: a dot does not reach
// into a nested object, and slashes, spaces and accents rename as they are.
func TestAttributeKeyWithDotAndSlash(t *testing.T) {
	db := ruleDB(t)
	writeAttrs(t, db, 1, store.FamilyProduct, "dot", map[string]string{"a.b": "1", "x/y": "2", "plan type": "3", "é": "4"})
	writeAttrs(t, db, 1, store.FamilyProduct, "nested", map[string]string{"k": "v"})
	execAll(t, db, `UPDATE events SET attributes = '{"a":{"b":"z"}}' WHERE id = 'nested'`)

	if got := renameAttr(t, db, 1, "a.b", "c", false); got["events"] != 1 {
		t.Errorf("a.b>c rewrote %d rows, want 1", got["events"])
	}
	renameAttr(t, db, 1, "x/y", "checkout/v2", false)
	renameAttr(t, db, 1, "plan type", "é2", false)
	renameAttr(t, db, 1, "é", "e acute", false)
	want := map[string]map[string]any{
		"dot":    {"c": "1", "checkout/v2": "2", "é2": "3", "e acute": "4"},
		"nested": {"a": map[string]any{"b": "z"}},
	}
	if got := rawAttrs(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("raw = %v, want %v", got, want)
	}
	// The mapping at insert reads the same rules.
	writeAttrs(t, db, 1, store.FamilyProduct, "later", map[string]string{"a.b": "5", "x/y": "6"})
	if got, want := rawAttrs(t, db)["later"], map[string]any{"c": "5", "checkout/v2": "6"}; !reflect.DeepEqual(got, want) {
		t.Errorf("later = %v, want %v", got, want)
	}
}

func TestBlockAttribute(t *testing.T) {
	db := ruleDB(t)
	ctx := context.Background()
	renameAttr(t, db, 1, "z", "a", false)
	execAll(t, db, `INSERT INTO project_attributes (project_id, key, breakdown) VALUES (1, 'a', 1)
		ON CONFLICT (project_id, key) DO UPDATE SET breakdown = 1`)
	blockAttr(t, db, 1, "a")
	want := []store.AttributeRule{
		attrRule(1, "a", store.EventRuleBlock, ""),
		attrRule(1, "z", store.EventRuleRename, "a"),
	}
	if got := loadAttrRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want %v", got, want)
	}
	if got, want := catalogRows(t, db), []string{"1/a=0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("catalog = %v, want %v", got, want)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action='attribute.block'`); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}

	// Blocking a key renamed away replaces its rename: one rule per key.
	renameAttr(t, db, 1, "p", "q", false)
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
	renameAttr(t, db, 1, "a", "b", false)
	blockAttr(t, db, 1, "x")
	execAll(t, db, `DELETE FROM project_attributes`)

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
	if got, want := catalogRows(t, db), []string{"1/b=0", "1/y=0", "2/a=0", "2/x=0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("catalog = %v, want %v", got, want)
	}
}

// A rename moves the usage rows ingest remembered; the store forgets them,
// so a key that arrives again under its old name (the rule removed) is
// recorded again the same day.
func TestRenameAttributeForgetsSeenKeys(t *testing.T) {
	db := ruleDB(t)
	ctx := context.Background()
	writeAttrs(t, db, 1, store.FamilyProduct, "p1", map[string]string{"a": "1"})
	renameAttr(t, db, 1, "a", "c", false)
	if err := db.RemoveAttributeRule(ctx, 1, "a", attrAudit); err != nil {
		t.Fatal(err)
	}
	writeAttrs(t, db, 1, store.FamilyProduct, "p2", map[string]string{"a": "2"})
	if got, want := usageRows(t, db), []string{"1/2026-10-10/a 0 null", "1/2026-10-10/c 0 null"}; !reflect.DeepEqual(got, want) {
		t.Errorf("usage_attributes = %v, want %v", got, want)
	}
	if got, want := catalogRows(t, db), []string{"1/a=0", "1/c=0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("catalog = %v, want %v", got, want)
	}
}

// A batch that committed before a rename but remembers its keys after it
// remembers nothing: the rename may have moved the rows it wrote.
func TestReceivedSeenForgetWinsOverALateRemember(t *testing.T) {
	var s receivedSeen
	k := receivedKey{1, "2026-08-01", "a"}
	keys, gen := s.unseen(receivedKeys{k: {}})
	s.forget(1)
	s.remember(keys, gen)
	if got, _ := s.unseen(receivedKeys{k: {}}); len(got) != 1 {
		t.Fatalf("a batch remembered across a forget: unseen = %v", got)
	}
	keys, gen = s.unseen(receivedKeys{k: {}})
	s.remember(keys, gen)
	s.remember([]receivedKey{{2, "2026-08-01", "b"}}, gen)
	s.forget(1)
	if got, _ := s.unseen(receivedKeys{k: {}, {2, "2026-08-01", "b"}: {}}); !reflect.DeepEqual(got, []receivedKey{k}) {
		t.Fatalf("after forgetting project 1: unseen = %v, want only its key", got)
	}
}

// Aggregates of a key blocked after they were written go at the next pass
// (D12); other keys, the event aggregates and other projects stay.
func TestDropBlockedAggregatesDropsBlockedKeys(t *testing.T) {
	db := ruleDB(t)
	ctx := context.Background()
	for _, p := range []int{1, 2} {
		for _, key := range []string{"x", "y"} {
			execAll(t, db,
				fmt.Sprintf(`INSERT INTO agg_product_attrs (project_id, day, event_name, attr_key, attr_value, count, unique_users) VALUES (%d, '2026-10-01', 'e', '%s', 'v', 4, 2)`, p, key),
				fmt.Sprintf(`INSERT INTO agg_measures_attrs (project_id, day, event_name, measure, attr_key, attr_value, bucket, samples, weight, sum) VALUES (%d, '2026-10-01', 'e', 'm', '%s', 'v', 1, 4, 4, 6)`, p, key))
		}
		execAll(t, db,
			fmt.Sprintf(`INSERT INTO agg_product_daily (project_id, day, event_name, count, unique_users) VALUES (%d, '2026-10-01', 'x', 4, 2)`, p),
			fmt.Sprintf(`INSERT INTO agg_measures_daily (project_id, day, event_name, measure, bucket, samples, weight, sum) VALUES (%d, '2026-10-01', 'x', 'm', 1, 4, 4, 6)`, p))
	}
	blockAttr(t, db, 1, "x")
	for i := 0; i < 2; i++ {
		if err := db.DropBlockedAggregates(ctx, 1); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			table, key string
			project    int
			want       int
		}{
			{"agg_product_attrs", "x", 1, 0}, {"agg_measures_attrs", "x", 1, 0},
			{"agg_product_attrs", "y", 1, 1}, {"agg_measures_attrs", "y", 1, 1},
			{"agg_product_attrs", "x", 2, 1}, {"agg_measures_attrs", "x", 2, 1},
		} {
			got := countOf(t, db, `SELECT COUNT(*) FROM `+tc.table+` WHERE project_id=? AND attr_key=?`, tc.project, tc.key)
			if got != tc.want {
				t.Errorf("pass %d: %s project %d %s rows = %d, want %d", i+1, tc.table, tc.project, tc.key, got, tc.want)
			}
		}
		// An event named like the blocked key keeps its aggregates.
		for _, table := range []string{"agg_product_daily", "agg_measures_daily"} {
			if got := countOf(t, db, `SELECT COUNT(*) FROM `+table+` WHERE event_name='x'`); got != 2 {
				t.Errorf("pass %d: %s rows named x = %d, want 2", i+1, table, got)
			}
		}
	}
}
