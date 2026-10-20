package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

var attrAudit = store.AuditEntry{Actor: "cli", Action: "attribute.rename", Subject: "1/a"}

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

// A rename writes its rule and its audit row, and nothing else.
func TestRenameAttributeWritesOnlyRules(t *testing.T) {
	db := ruleDB(t)

	renameAttr(t, db, 1, "a", "c")

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

// A key is stored as itself, never as a path: a key with a dot, a slash,
// a space, an accent, a backslash or a double quote reads back as given.
func TestAttributeKeyWithDotAndSlash(t *testing.T) {
	db := ruleDB(t)
	renameAttr(t, db, 1, "a.b", "c")
	renameAttr(t, db, 1, "x/y", "checkout/v2")
	renameAttr(t, db, 1, "plan type", "é2")
	renameAttr(t, db, 1, "é", "e acute")
	renameAttr(t, db, 1, `a\b`, `back\slash`)
	renameAttr(t, db, 1, `q"r`, `quote"d`)
	want := []store.AttributeRule{
		attrRule(1, "a.b", store.EventRuleRename, "c"),
		attrRule(1, `a\b`, store.EventRuleRename, `back\slash`),
		attrRule(1, "plan type", store.EventRuleRename, "é2"),
		attrRule(1, `q"r`, store.EventRuleRename, `quote"d`),
		attrRule(1, "x/y", store.EventRuleRename, "checkout/v2"),
		attrRule(1, "é", store.EventRuleRename, "e acute"),
	}
	if got := loadAttrRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want %v", got, want)
	}
}

func TestBlockAttribute(t *testing.T) {
	db := ruleDB(t)
	ctx := context.Background()
	renameAttr(t, db, 1, "z", "a")
	blockAttr(t, db, 1, "a")
	want := []store.AttributeRule{
		attrRule(1, "a", store.EventRuleBlock, ""),
		attrRule(1, "z", store.EventRuleRename, "a"),
	}
	if got := loadAttrRules(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want %v", got, want)
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
