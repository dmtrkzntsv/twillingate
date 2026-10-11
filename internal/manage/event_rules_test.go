package manage

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

// ruleOps is Ops over a migrated store with one project, and the path of
// the database file so a test can read audit_log through a second handle.
func ruleOps(t *testing.T) (ops *Ops, st store.Store, reg *Registry, project int64, path string) {
	t.Helper()
	path = t.TempDir() + "/rules.db"
	st = storetest.Open(t, path)
	reg = New(st, discard())
	if err := reg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	ops = NewOps(reg, st)
	p, err := ops.CreateProject(context.Background(), "t", ProjectSpec{Name: "blog"})
	if err != nil {
		t.Fatal(err)
	}
	return ops, st, reg, p.ID, path
}

func writeProductEvent(t *testing.T, st store.Store, project int64, name string) {
	t.Helper()
	at := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	err := st.WriteEvents(context.Background(), []store.Event{{
		Family: store.FamilyProduct, ID: "e-" + name, ProjectID: project, EventName: name,
		TS: at, ReceivedAt: at, Kind: "app", ActorID: "a", ActorKind: store.ActorConnection}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEventRuleRefusals(t *testing.T) {
	ops, st, _, p, _ := ruleOps(t)
	ctx := context.Background()
	writeProductEvent(t, st, p, "taken")

	renames := map[string]struct {
		family, from, to string
	}{
		"views family":    {"views", "a", "b"},
		"empty family":    {"", "a", "b"},
		"unknown family":  {"other", "a", "b"},
		"empty from":      {"product", "", "b"},
		"blank from":      {"product", "  ", "b"},
		"dollar from":     {"product", "$page_view", "b"},
		"empty to":        {"product", "a", ""},
		"dollar to":       {"product", "a", "$x"},
		"from equals to":  {"product", "a", "a"},
		"measures dollar": {"measures", "$vital", "b"},
	}
	for name, c := range renames {
		t.Run("rename "+name, func(t *testing.T) {
			_, _, err := ops.RenameEvent(ctx, "t", p, c.family, c.from, c.to, false)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	for name, c := range map[string]struct{ family, name string }{
		"views family": {"views", "a"},
		"empty family": {"", "a"},
		"empty name":   {"product", ""},
		"blank name":   {"product", " \t"},
		"dollar name":  {"product", "$page_view"},
	} {
		t.Run("block "+name, func(t *testing.T) {
			if _, err := ops.BlockEvent(ctx, "t", p, c.family, c.name); !errors.Is(err, ErrInvalid) {
				t.Fatalf("block err = %v, want ErrInvalid", err)
			}
			if err := ops.RemoveEventRule(ctx, "t", p, c.family, c.name); !errors.Is(err, ErrInvalid) {
				t.Fatalf("remove err = %v, want ErrInvalid", err)
			}
		})
	}

	if _, _, err := ops.RenameEvent(ctx, "t", 999, "product", "a", "b", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename in unknown project: err = %v, want ErrNotFound", err)
	}
	if _, err := ops.BlockEvent(ctx, "t", 999, "product", "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("block in unknown project: err = %v, want ErrNotFound", err)
	}
	if err := ops.RemoveEventRule(ctx, "t", 999, "product", "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove in unknown project: err = %v, want ErrNotFound", err)
	}
	if _, err := ops.ListEventRules(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("list in unknown project: err = %v, want ErrNotFound", err)
	}
	if err := ops.RemoveEventRule(ctx, "t", p, "product", "nothing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove a missing rule: err = %v, want ErrNotFound", err)
	}
	if _, _, err := ops.RenameEvent(ctx, "t", p, "product", "a", "taken", false); !errors.Is(err, ErrConflict) {
		t.Errorf("rename onto an existing name: err = %v, want ErrConflict", err)
	}
	if rules, err := ops.ListEventRules(ctx, p); err != nil || len(rules) != 0 {
		t.Errorf("refusals left rules %v (err %v), want none", rules, err)
	}
}

func TestRenameEventReachesSnapshot(t *testing.T) {
	ops, st, reg, p, _ := ruleOps(t)
	ctx := context.Background()
	writeProductEvent(t, st, p, "a")

	rule, counts, err := ops.RenameEvent(ctx, "t", p, "product", "a", "b", false)
	if err != nil {
		t.Fatal(err)
	}
	if rule.ProjectID != p || rule.Family != store.FamilyProduct || rule.Name != "a" ||
		rule.Action != store.EventRuleRename || rule.To != "b" || rule.CreatedAt == "" {
		t.Errorf("rule = %+v", rule)
	}
	if counts["events"] != 1 {
		t.Errorf("counts = %v, want one event rewritten", counts)
	}
	snap := reg.Snapshot(ctx)
	if got, blocked := snap.ResolveEvent(p, store.FamilyProduct, "a"); got != "b" || blocked {
		t.Errorf("a resolves to %q blocked=%v, want b, false", got, blocked)
	}
	if got, blocked := snap.ResolveEvent(p, store.FamilyProduct, "other"); got != "other" || blocked {
		t.Errorf("an unruled name resolves to %q blocked=%v, want itself, false", got, blocked)
	}
	if got, _ := snap.ResolveEvent(p, store.FamilyMeasures, "a"); got != "a" {
		t.Errorf("the rule leaked into measures: %q", got)
	}
	if got, _ := snap.ResolveEvent(p+1, store.FamilyProduct, "a"); got != "a" {
		t.Errorf("the rule leaked into another project: %q", got)
	}
}

func TestRenameEventMergeOntoExisting(t *testing.T) {
	ops, st, _, p, _ := ruleOps(t)
	ctx := context.Background()
	writeProductEvent(t, st, p, "a")
	writeProductEvent(t, st, p, "b")
	if _, _, err := ops.RenameEvent(ctx, "t", p, "product", "a", "b", true); err != nil {
		t.Fatalf("merge: %v", err)
	}
}

func TestBlockEventReachesSnapshot(t *testing.T) {
	ops, _, reg, p, _ := ruleOps(t)
	ctx := context.Background()

	rule, err := ops.BlockEvent(ctx, "t", p, "product", "b")
	if err != nil {
		t.Fatal(err)
	}
	if rule.ProjectID != p || rule.Family != store.FamilyProduct || rule.Name != "b" ||
		rule.Action != store.EventRuleBlock || rule.To != "" || rule.CreatedAt == "" {
		t.Errorf("rule = %+v", rule)
	}
	if got, blocked := reg.Snapshot(ctx).ResolveEvent(p, store.FamilyProduct, "b"); got != "b" || !blocked {
		t.Errorf("b resolves to %q blocked=%v, want b, true", got, blocked)
	}
	// A rename into a blocked name is refused without merge; with it the
	// name is mapped, then dropped.
	if _, _, err := ops.RenameEvent(ctx, "t", p, "product", "a", "b", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename onto a blocked name: err = %v, want ErrConflict", err)
	}
	if _, _, err := ops.RenameEvent(ctx, "t", p, "product", "a", "b", true); err != nil {
		t.Fatal(err)
	}
	if got, blocked := reg.Snapshot(ctx).ResolveEvent(p, store.FamilyProduct, "a"); got != "b" || !blocked {
		t.Errorf("a resolves to %q blocked=%v, want b, true", got, blocked)
	}
}

func TestRemoveEventRuleReachesSnapshot(t *testing.T) {
	ops, _, reg, p, _ := ruleOps(t)
	ctx := context.Background()
	if _, err := ops.BlockEvent(ctx, "t", p, "measures", "m"); err != nil {
		t.Fatal(err)
	}
	if _, blocked := reg.Snapshot(ctx).ResolveEvent(p, store.FamilyMeasures, "m"); !blocked {
		t.Fatal("setup: m is not blocked")
	}
	if err := ops.RemoveEventRule(ctx, "t", p, "measures", "m"); err != nil {
		t.Fatal(err)
	}
	if got, blocked := reg.Snapshot(ctx).ResolveEvent(p, store.FamilyMeasures, "m"); got != "m" || blocked {
		t.Errorf("m resolves to %q blocked=%v after removal, want m, false", got, blocked)
	}
}

func TestListEventRules(t *testing.T) {
	ops, _, _, p, _ := ruleOps(t)
	ctx := context.Background()
	other, err := ops.CreateProject(ctx, "t", ProjectSpec{Name: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := ops.ListEventRules(ctx, p)
	if err != nil || rules == nil || len(rules) != 0 {
		t.Fatalf("no rules: got %#v, %v; want an empty non-nil slice", rules, err)
	}
	if _, err := ops.BlockEvent(ctx, "t", p, "product", "z"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ops.RenameEvent(ctx, "t", p, "product", "a", "b", false); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.BlockEvent(ctx, "t", p, "measures", "m"); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.BlockEvent(ctx, "t", other.ID, "product", "x"); err != nil {
		t.Fatal(err)
	}
	rules, err = ops.ListEventRules(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var got [][3]string
	for _, r := range rules {
		if r.ProjectID != p {
			t.Errorf("rule of project %d listed for %d", r.ProjectID, p)
		}
		got = append(got, [3]string{string(r.Family), r.Name, r.Action})
	}
	want := [][3]string{{"measures", "m", "block"}, {"product", "a", "rename"}, {"product", "z", "block"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %v, want %v", got, want)
	}
}

func TestEventRuleAudit(t *testing.T) {
	ops, st, _, p, path := ruleOps(t)
	ctx := context.Background()
	writeProductEvent(t, st, p, "c")
	writeProductEvent(t, st, p, "d")
	steps := []func() error{
		func() error { _, _, err := ops.RenameEvent(ctx, "alice", p, "product", "a", "b", false); return err },
		func() error { _, _, err := ops.RenameEvent(ctx, "alice", p, "product", "c", "d", true); return err },
		func() error { _, err := ops.BlockEvent(ctx, "bob", p, "measures", "m"); return err },
		func() error { return ops.RemoveEventRule(ctx, "bob", p, "measures", "m") },
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
		WHERE action LIKE 'event.%' ORDER BY rowid`)
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
		{"alice", "event.rename", pid("/product/a"), "to b"},
		{"alice", "event.rename", pid("/product/c"), "to d (merge)"},
		{"bob", "event.block", pid("/measures/m"), ""},
		{"bob", "event.rule_remove", pid("/measures/m"), ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("audit rows:\n got %v\nwant %v", got, want)
	}
}
