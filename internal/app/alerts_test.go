package app

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/alerts"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

// TestAlertProjectsReadsTheRegistry: the adapter answers from the
// registry's current snapshot: a project's name and archived flag, the
// name a renamed event is stored under, and the event rules; an unknown
// project is not ok.
func TestAlertProjectsReadsTheRegistry(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t, filepath.Join(t.TempDir(), "twillingate.db"))
	reg := manage.New(st, slog.New(slog.DiscardHandler))
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := manage.NewOps(reg, st)
	live, err := ops.CreateProject(ctx, "test", manage.ProjectSpec{Name: "App"})
	if err != nil {
		t.Fatal(err)
	}
	old, err := ops.CreateProject(ctx, "test", manage.ProjectSpec{Name: "Old"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ops.ArchiveProject(ctx, "test", old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.RenameEvent(ctx, "test", live.ID, string(store.FamilyProduct), "signup", "sign_up"); err != nil {
		t.Fatal(err)
	}

	if _, err := ops.RenameAttribute(ctx, "test", live.ID, "plan", "tier"); err != nil {
		t.Fatal(err)
	}

	p := alertProjects{reg}
	if name, archived, ok := p.Project(ctx, live.ID); name != "App" || archived || !ok {
		t.Errorf("Project(live) = %q, %v, %v; want App, false, true", name, archived, ok)
	}
	if name, archived, ok := p.Project(ctx, old.ID); name != "Old" || !archived || !ok {
		t.Errorf("Project(archived) = %q, %v, %v; want Old, true, true", name, archived, ok)
	}
	if _, _, ok := p.Project(ctx, 999); ok {
		t.Error("Project(999) is ok; want no project")
	}
	if to, blocked := p.ResolveEvent(ctx, live.ID, store.FamilyProduct, "signup"); to != "sign_up" || blocked {
		t.Errorf("ResolveEvent(signup) = %q, %v; want sign_up, false", to, blocked)
	}
	if to, blocked := p.ResolveAttribute(ctx, live.ID, "plan"); to != "tier" || blocked {
		t.Errorf("ResolveAttribute(plan) = %q, %v; want tier, false", to, blocked)
	}
	rules := p.Rules(ctx).Events
	if len(rules) != 1 || rules[0].ProjectID != live.ID || rules[0].Name != "signup" || rules[0].To != "sign_up" {
		t.Errorf("Rules().Events = %+v; want the one rename of signup to sign_up", rules)
	}
}

// TestAlertAttributeRulesMatchAnyCase: the event files match attribute
// columns case-insensitively, so a rule on plan covers an alert on
// attr_Plan: refused at save, broken at evaluation; a key no rule
// matches is answered as given.
func TestAlertAttributeRulesMatchAnyCase(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t, filepath.Join(t.TempDir(), "twillingate.db"))
	reg := manage.New(st, slog.New(slog.DiscardHandler))
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := manage.NewOps(reg, st)
	p, err := ops.CreateProject(ctx, "test", manage.ProjectSpec{Name: "App"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.RenameAttribute(ctx, "test", p.ID, "plan", "tier"); err != nil {
		t.Fatal(err)
	}
	in := alerts.Input{Name: "pro", Condition: store.AlertCondition{Type: "threshold", Op: "below", Value: new(float64)},
		Query: &native.Query{Kind: "stat", Table: "product", Metrics: []native.Metric{{Op: "count"}},
			Where: []native.Filter{{Column: "attr_Plan", Op: "eq", Values: []string{"pro"}}}}}

	projects := alertProjects{reg}
	if to, _ := projects.ResolveAttribute(ctx, p.ID, "Mixed"); to != "Mixed" {
		t.Errorf("ResolveAttribute(Mixed) = %q, want it as given", to)
	}
	_, err = alerts.NewOps(st, projects, nil, nil).Create(ctx, "test", p.ID, in)
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("save on attr_Plan: %v, want ErrInvalid", err)
	}
	def, err := alerts.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	o := alerts.NewEvaluator(nil, projects, nil, time.Second).Evaluate(ctx, store.Alert{ProjectID: p.ID, Def: def}, time.Now())
	if o.Outcome != store.AlertBroken || o.Reason != "Renamed to attr_tier: attr_Plan" {
		t.Errorf("evaluation on attr_Plan = %s %q, want broken by the rename", o.Outcome, o.Reason)
	}
}
