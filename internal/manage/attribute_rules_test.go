package manage

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// writeAttrEvent stores one product event carrying attrs, so the keys have
// data (usage_attributes) for a rename to meet.
func writeAttrEvent(t *testing.T, st store.Store, project int64, id string, attrs map[string]string) {
	t.Helper()
	at := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	err := st.WriteEvents(context.Background(), []store.Event{{
		Family: store.FamilyProduct, ID: id, ProjectID: project, EventName: "clicked",
		TS: at, ReceivedAt: at, Kind: "app", ActorID: "a", ActorKind: store.ActorConnection,
		Attributes: attrs}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAttributeRuleRefusals(t *testing.T) {
	ops, st, _, p, _ := ruleOps(t)
	ctx := context.Background()
	writeAttrEvent(t, st, p, "e1", map[string]string{"taken": "x"})

	for name, c := range map[string]struct{ from, to string }{
		"empty from":  {"", "b"},
		"blank from":  {" ", "b"},
		"dollar from": {"$host", "b"},
		"quote from":  {`a"b`, "b"},
		"empty to":    {"a", ""},
		"blank to":    {"a", " \t"},
		"dollar to":   {"a", "$host"},
		"quote to":    {"a", `a"b`},
		"same key":    {"a", "a"},
		"long from":   {strings.Repeat("k", 65), "b"},
		"long to":     {"a", strings.Repeat("k", 65)},
	} {
		t.Run("rename "+name, func(t *testing.T) {
			if _, _, err := ops.RenameAttribute(ctx, "t", p, c.from, c.to); !errors.Is(err, ErrInvalid) {
				t.Fatalf("rename err = %v, want ErrInvalid", err)
			}
			if _, _, err := ops.MergeAttribute(ctx, "t", p, c.from, c.to); !errors.Is(err, ErrInvalid) {
				t.Fatalf("merge err = %v, want ErrInvalid", err)
			}
		})
	}
	for name, k := range map[string]string{"empty": "", "blank": " ", "dollar": "$host", "quote": `a"b`,
		"long": strings.Repeat("k", 65), "long unicode": strings.Repeat("é", 33)} {
		t.Run("block "+name, func(t *testing.T) {
			if _, err := ops.BlockAttribute(ctx, "t", p, k); !errors.Is(err, ErrInvalid) {
				t.Fatalf("block err = %v, want ErrInvalid", err)
			}
			if err := ops.RemoveAttributeRule(ctx, "t", p, k); !errors.Is(err, ErrInvalid) {
				t.Fatalf("remove err = %v, want ErrInvalid", err)
			}
		})
	}

	// The ingest limit itself is fine: such a key can arrive.
	if _, err := ops.BlockAttribute(ctx, "t", p, strings.Repeat("k", 64)); err != nil {
		t.Errorf("block a key of exactly the ingest limit: %v", err)
	}
	if err := ops.RemoveAttributeRule(ctx, "t", p, strings.Repeat("k", 64)); err != nil {
		t.Fatal(err)
	}

	if _, _, err := ops.RenameAttribute(ctx, "t", 999, "a", "b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename in unknown project: err = %v, want ErrNotFound", err)
	}
	if _, _, err := ops.MergeAttribute(ctx, "t", 999, "a", "b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("merge in unknown project: err = %v, want ErrNotFound", err)
	}
	if _, err := ops.BlockAttribute(ctx, "t", 999, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("block in unknown project: err = %v, want ErrNotFound", err)
	}
	if err := ops.RemoveAttributeRule(ctx, "t", 999, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove in unknown project: err = %v, want ErrNotFound", err)
	}
	if err := ops.RemoveAttributeRule(ctx, "t", p, "nothing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove a missing rule: err = %v, want ErrNotFound", err)
	}
	if _, _, err := ops.RenameAttribute(ctx, "t", p, "a", "taken"); !errors.Is(err, ErrConflict) {
		t.Errorf("rename onto a key with data: err = %v, want ErrConflict", err)
	}
	if _, _, err := ops.MergeAttribute(ctx, "t", p, "a", "taken"); err != nil {
		t.Errorf("merge onto a key with data: %v", err)
	}
	if got := ops.Reg.Snapshot(ctx).AttributeRuleList(p); len(got) != 1 || got[0].Key != "a" {
		t.Errorf("refusals left rules %v, want only the merge of a", got)
	}
}

func TestAttributeRulesReachSnapshot(t *testing.T) {
	ops, st, reg, p, _ := ruleOps(t)
	ctx := context.Background()
	other, err := ops.CreateProject(ctx, "t", ProjectSpec{Name: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Snapshot(ctx).AttributeRules(p); !got.Empty() {
		t.Fatalf("a project with no rules has %+v", got)
	}
	if got := reg.Snapshot(ctx).AttributeRuleList(p); len(got) != 0 {
		t.Fatalf("a project with no rules lists %v", got)
	}
	writeAttrEvent(t, st, p, "e1", map[string]string{"old": "x"})

	rule, counts, err := ops.RenameAttribute(ctx, "t", p, "old", "new")
	if err != nil {
		t.Fatal(err)
	}
	if rule.ProjectID != p || rule.Key != "old" || rule.Action != store.EventRuleRename ||
		rule.To != "new" || rule.CreatedAt == "" {
		t.Errorf("rename rule = %+v", rule)
	}
	if counts["events"] != 1 {
		t.Errorf("counts = %v, want one event rewritten", counts)
	}
	block, err := ops.BlockAttribute(ctx, "t", p, "junk")
	if err != nil {
		t.Fatal(err)
	}
	if block.ProjectID != p || block.Key != "junk" || block.Action != store.EventRuleBlock ||
		block.To != "" || block.CreatedAt == "" {
		t.Errorf("block rule = %+v", block)
	}

	snap := reg.Snapshot(ctx)
	set := snap.AttributeRules(p)
	if !reflect.DeepEqual(set.Renamed, map[string]string{"old": "new"}) ||
		!reflect.DeepEqual(set.Blocked, map[string]bool{"junk": true}) {
		t.Errorf("set = %+v", set)
	}
	if got := snap.AttributeRules(other.ID); !got.Empty() {
		t.Errorf("another project's set = %+v, want empty", got)
	}
	list := snap.AttributeRuleList(p)
	if len(list) != 2 || list[0].Key != "junk" || list[1].Key != "old" {
		t.Errorf("list = %v, want junk then old", list)
	}
	if got := snap.AttributeRuleList(other.ID); len(got) != 0 {
		t.Errorf("another project's list = %v", got)
	}

	if err := ops.RemoveAttributeRule(ctx, "t", p, "junk"); err != nil {
		t.Fatal(err)
	}
	if got := reg.Snapshot(ctx).AttributeRules(p); got.Blocked["junk"] || got.Renamed["old"] != "new" {
		t.Errorf("after removing the block: %+v", got)
	}
}

func TestAttributeRuleAudit(t *testing.T) {
	ops, st, _, p, path := ruleOps(t)
	ctx := context.Background()
	writeAttrEvent(t, st, p, "e1", map[string]string{"c": "x", "d": "y"})
	steps := []func() error{
		func() error { _, _, err := ops.RenameAttribute(ctx, "alice", p, "a", "b"); return err },
		func() error { _, _, err := ops.MergeAttribute(ctx, "alice", p, "c", "d"); return err },
		func() error { _, err := ops.BlockAttribute(ctx, "bob", p, "m"); return err },
		func() error { return ops.RemoveAttributeRule(ctx, "bob", p, "m") },
	}
	for _, s := range steps {
		if err := s(); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT actor, action, subject, COALESCE(detail, '') FROM audit_log
		WHERE action LIKE 'attribute.%' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][4]string
	for rows.Next() {
		var r [4]string
		if err := rows.Scan(&r[0], &r[1], &r[2], &r[3]); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	pid := func(rest string) string { return strconv.FormatInt(p, 10) + rest }
	want := [][4]string{
		{"alice", "attribute.rename", pid("/a"), "to b"},
		{"alice", "attribute.merge", pid("/c"), "to d"},
		{"bob", "attribute.block", pid("/m"), ""},
		{"bob", "attribute.rule_remove", pid("/m"), ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("audit rows:\n got %v\nwant %v", got, want)
	}
}

func TestRuledBreakdownsAreRefused(t *testing.T) {
	ops, _, reg, p, _ := ruleOps(t)
	ctx := context.Background()
	if _, err := ops.BlockAttribute(ctx, "t", p, "junk"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ops.RenameAttribute(ctx, "t", p, "old", "new"); err != nil {
		t.Fatal(err)
	}
	for _, keys := range [][]string{{"junk"}, {"old"}, {"plan", "junk"}, {"old", "plan"}} {
		if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: p, Attributes: keys}); !errors.Is(err, ErrConflict) {
			t.Errorf("attributes %v: err = %v, want ErrConflict", keys, err)
		}
	}
	if got := reg.Snapshot(ctx).Project(p).Attributes; len(got) != 0 {
		t.Errorf("refusals left attributes %v", got)
	}
	// The target of a rename, and keys with no rule, are fine; the list
	// comes back ordered by key.
	got, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: p, Attributes: []string{"tier", "new", "plan"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"new", "plan", "tier"}; !reflect.DeepEqual(got.Attributes, want) {
		t.Errorf("attributes = %v, want %v", got.Attributes, want)
	}
	// Lifting the block lets the key be declared again.
	if err := ops.RemoveAttributeRule(ctx, "t", p, "junk"); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: p, Attributes: []string{"junk"}}); err != nil {
		t.Errorf("declare a key whose block was lifted: %v", err)
	}
}

func TestBlockingABreakdownRemovesIt(t *testing.T) {
	ops, _, reg, p, _ := ruleOps(t)
	ctx := context.Background()
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: p, Attributes: []string{"plan", "tier"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.BlockAttribute(ctx, "t", p, "plan"); err != nil {
		t.Fatal(err)
	}
	if got := reg.Snapshot(ctx).Project(p).Attributes; !reflect.DeepEqual(got, []string{"tier"}) {
		t.Errorf("attributes = %v, want [tier]", got)
	}
}

func TestMergingBreakdownsFreesBudget(t *testing.T) {
	ops, _, reg, p, _ := ruleOps(t)
	ctx := context.Background()
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: p, Attributes: []string{"plan", "tier"}}); err != nil {
		t.Fatal(err)
	}
	if got := reg.Snapshot(ctx).BreakdownsInUse(); got != 2 {
		t.Fatalf("in use = %d, want 2", got)
	}
	if _, _, err := ops.MergeAttribute(ctx, "t", p, "plan", "tier"); err != nil {
		t.Fatal(err)
	}
	snap := reg.Snapshot(ctx)
	if got := snap.BreakdownsInUse(); got != 1 {
		t.Errorf("in use after the merge = %d, want 1", got)
	}
	if got := snap.Project(p).Attributes; !reflect.DeepEqual(got, []string{"tier"}) {
		t.Errorf("attributes = %v, want [tier]", got)
	}
}

// An update that does not send attributes leaves the catalog alone, even
// when the snapshot it merges over is stale: here it still lists plan as
// a breakdown although plan was just blocked, and writing that list back
// would mark plan again.
func TestUpdateWithoutAttributesLeavesTheCatalog(t *testing.T) {
	ops, st, reg, p, _ := ruleOps(t)
	ctx := context.Background()
	if _, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: p, Attributes: []string{"plan", "tier"}}); err != nil {
		t.Fatal(err)
	}
	stale := reg.snap.Load()
	if err := st.BlockAttribute(ctx, p, "plan", store.AuditEntry{Actor: "t", Action: "attribute.block"}); err != nil {
		t.Fatal(err)
	}
	v, err := st.ConfigVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reg.snap.Store(stale) // the block has not reached this process yet
	reg.version.Store(v)
	breakdowns := func() string {
		t.Helper()
		ps, _, err := st.LoadRegistry(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range ps {
			if row.ID == p {
				return row.Attributes
			}
		}
		t.Fatalf("project %d not loaded", p)
		return ""
	}
	if got := breakdowns(); got != `["tier"]` {
		t.Fatalf("breakdowns after the block = %s, want [\"tier\"]", got)
	}
	got, err := ops.UpdateProject(ctx, "t", ProjectSpec{ID: p, Name: "renamed"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "renamed" {
		t.Errorf("name = %q, want renamed", got.Name)
	}
	if got := breakdowns(); got != `["tier"]` {
		t.Errorf("breakdowns after a name-only update = %s, want [\"tier\"]: the stale list was written back", got)
	}
}
