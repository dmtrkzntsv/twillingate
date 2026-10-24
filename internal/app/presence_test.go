package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// presence is manage.Presence, native or the SQL reference.
type presence interface {
	HasEvents(ctx context.Context, projectID int64, family store.Family, name string) (bool, error)
	HasAttribute(ctx context.Context, projectID int64, key string) (bool, error)
}

// bothPresence asks the native filePresence and the SQL reference, and
// fails the test unless they agree, and agree with the reference's
// answer frozen in testdata/<Test>.golden.json, by the order asked (the
// files and rules change between asks).
type bothPresence struct {
	t        *testing.T
	nat, sql presence
	asked    *int
}

// frozen is the golden answer to the next question, what, the SQL
// reference's (want) checked against it.
func (b bothPresence) frozen(what string, want bool) bool {
	b.t.Helper()
	*b.asked++
	return frozen(b.t, fmt.Sprintf("%03d %s", *b.asked, what), want)
}

func (b bothPresence) HasEvents(ctx context.Context, projectID int64, family store.Family, name string) (bool, error) {
	got, err := b.nat.HasEvents(ctx, projectID, family, name)
	want, werr := b.sql.HasEvents(ctx, projectID, family, name)
	what := fmt.Sprintf("HasEvents(%d, %s, %q)", projectID, family, name)
	if err != nil || werr != nil || got != want || got != b.frozen(what, want) {
		b.t.Errorf("%s: native %v, %v; SQL %v, %v", what, got, err, want, werr)
	}
	return got, err
}

func (b bothPresence) HasAttribute(ctx context.Context, projectID int64, key string) (bool, error) {
	got, err := b.nat.HasAttribute(ctx, projectID, key)
	want, werr := b.sql.HasAttribute(ctx, projectID, key)
	what := fmt.Sprintf("HasAttribute(%d, %q)", projectID, key)
	if err != nil || werr != nil || got != want || got != b.frozen(what, want) {
		b.t.Errorf("%s: native %v, %v; SQL %v, %v", what, got, err, want, werr)
	}
	return got, err
}

// presenceFixture is the widgets fixture with the native filePresence
// over its files, checked against the SQL reference on every answer, and
// send, which appends one product event of project to them, at at
// (zero: 2026-08-20 noon).
func presenceFixture(t *testing.T) (*widgetsFixture, presence, func(id string, project int64, name string, attrs map[string]string, at ...time.Time)) {
	t.Helper()
	f := newWidgetsFixture(t)
	eng, reader, err := openFiles(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	send := func(id string, project int64, name string, attrs map[string]string, when ...time.Time) {
		t.Helper()
		log, err := archive.OpenLog(f.cfg.EventsDir)
		if err != nil {
			t.Fatal(err)
		}
		defer log.Close()
		at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
		if len(when) > 0 {
			at = when[0]
		}
		if err := log.WriteEvents(context.Background(), []store.Event{{ID: id, ProjectID: project,
			Family: store.FamilyProduct, EventName: name, TS: at, ReceivedAt: at, Kind: "app",
			ActorID: "a", ActorKind: store.ActorInstall, SampleRate: 1, Attributes: attrs}}); err != nil {
			t.Fatal(err)
		}
	}
	nat := &filePresence{nat: native.New(native.Options{Dir: f.cfg.EventsDir}), reg: f.reg}
	return f, bothPresence{t: t, nat: nat, sql: &sqlPresence{reader: reader, reg: f.reg}, asked: new(int)}, send
}

// TestFilePresenceAppliesRules: the files answer through the registry's
// rules as they stand when asked, so b has events once a is renamed to
// it, and a has none; one project's events say nothing of another's.
func TestFilePresenceAppliesRules(t *testing.T) {
	f, p, send := presenceFixture(t)
	ctx := context.Background()
	send("e1", 1, "a", map[string]string{"plan": "pro"})

	has := func(project int64, family store.Family, name string) bool {
		t.Helper()
		got, err := p.HasEvents(ctx, project, family, name)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	hasKey := func(project int64, key string) bool {
		t.Helper()
		got, err := p.HasAttribute(ctx, project, key)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if !has(1, store.FamilyProduct, "a") || has(1, store.FamilyProduct, "b") {
		t.Fatal("before the rule: want a and not b")
	}
	if has(2, store.FamilyProduct, "a") || has(1, store.FamilyMeasures, "a") {
		t.Error("a read as present in another project or family")
	}
	if !hasKey(1, "plan") || hasKey(1, "level") || hasKey(2, "plan") {
		t.Fatal("before the rule: want plan in project 1 only, and no level")
	}

	if _, err := f.ops.RenameEvent(ctx, "test", 1, "product", "a", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ops.RenameAttribute(ctx, "test", 1, "plan", "level"); err != nil {
		t.Fatal(err)
	}
	if !has(1, store.FamilyProduct, "b") || has(1, store.FamilyProduct, "a") {
		t.Error("after a > b: want b and not a")
	}
	if !hasKey(1, "level") || hasKey(1, "plan") {
		t.Error("after plan > level: want level and not plan")
	}

	if _, err := f.ops.BlockEvent(ctx, "test", 1, "product", "b"); err != nil {
		t.Fatal(err)
	}
	if has(1, store.FamilyProduct, "b") {
		t.Error("a blocked name read as present")
	}
}

// TestFilePresenceAttributeColumnAbsent: a key no event file has a column
// for is absent, not an error, with files and without.
func TestFilePresenceAttributeColumnAbsent(t *testing.T) {
	_, p, send := presenceFixture(t)
	ctx := context.Background()
	if got, err := p.HasAttribute(ctx, 1, "nokey"); err != nil || got {
		t.Fatalf("no files: %v, %v; want false, nil", got, err)
	}
	if got, err := p.HasEvents(ctx, 1, store.FamilyProduct, "a"); err != nil || got {
		t.Fatalf("no files: events %v, %v; want false, nil", got, err)
	}
	send("e1", 1, "a", map[string]string{"plan": "pro"})
	if got, err := p.HasAttribute(ctx, 1, "nokey"); err != nil || got {
		t.Errorf("a key with no column: %v, %v; want false, nil", got, err)
	}
	// A column another project's events made is no value of this one.
	send("e2", 2, "a", map[string]string{"theirs": "x"})
	if got, err := p.HasAttribute(ctx, 1, "theirs"); err != nil || got {
		t.Errorf("another project's key: %v, %v; want false, nil", got, err)
	}
}

// TestFilePresenceAttributeSpellings: a key matches its column without
// regard to case, across a stored day file spelling it attr_Plan and an
// open log spelling it attr_plan, and a key only the open log holds is
// present; both as the SQL reference answers.
func TestFilePresenceAttributeSpellings(t *testing.T) {
	f, p, send := presenceFixture(t)
	ctx := context.Background()
	day := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	send("e1", 1, "a", map[string]string{"Plan": "pro"}, day)
	send("e2", 2, "a", map[string]string{"Tier": "x"}, day)
	log, err := archive.OpenLog(f.cfg.EventsDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.New(log, slog.New(slog.DiscardHandler)).Maintain(ctx, civil.DateOf(day.AddDate(0, 0, 1)), 1, 400); err != nil {
		t.Fatal(err)
	}
	log.Close()
	if !hasDayFile(t, f.cfg.EventsDir, 1, "2026-08-19.parquet") {
		t.Fatal("no day file for 2026-08-19")
	}
	send("e3", 1, "a", map[string]string{"plan": "free", "fresh": "1"})
	send("e4", 2, "a", map[string]string{"TIER": "y"})
	for _, c := range []struct {
		project int64
		key     string
		want    bool
	}{
		{1, "plan", true}, {1, "Plan", true}, {1, "PLAN", true},
		{1, "fresh", true}, {1, "FRESH", true}, {2, "fresh", false},
		{1, "tier", false}, {2, "tier", true}, {2, "Tier", true},
		{1, "nokey", false}, {1, "", false},
	} {
		got, err := p.HasAttribute(ctx, c.project, c.key)
		if err != nil || got != c.want {
			t.Errorf("HasAttribute(%d, %q) = %v, %v; want %v", c.project, c.key, got, err, c.want)
		}
	}
	// Renamed away, a stored and a live spelling read NULL alike.
	if _, err := f.ops.RenameAttribute(ctx, "test", 1, "plan", "level"); err != nil {
		t.Fatal(err)
	}
	// In a fixed order: the goldens are kept by the order asked.
	for _, c := range []struct {
		key  string
		want bool
	}{{"plan", false}, {"Plan", false}, {"level", true}, {"LEVEL", true}, {"fresh", true}} {
		if got, err := p.HasAttribute(ctx, 1, c.key); err != nil || got != c.want {
			t.Errorf("after plan > level: HasAttribute(1, %q) = %v, %v; want %v", c.key, got, err, c.want)
		}
	}
}

// hasDayFile says whether project's directory under dir holds name.
func hasDayFile(t *testing.T, dir string, project int64, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, strconv.FormatInt(project, 10), name))
	return err == nil
}

// TestFilePresenceViewsAndBlockedKeys: a view's name is present in views
// alone; a blocked key holds no value.
func TestFilePresenceViewsAndBlockedKeys(t *testing.T) {
	f, p, send := presenceFixture(t)
	ctx := context.Background()
	send("e1", 1, "a", map[string]string{"plan": "pro", "tier": "x"})
	log, err := archive.OpenLog(f.cfg.EventsDir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC)
	if err := log.WriteEvents(ctx, []store.Event{{ID: "v1", ProjectID: 1, Family: store.FamilyViews, EventName: "$page_view",
		TS: at, ReceivedAt: at, Kind: "web", ActorID: "a", ActorKind: store.ActorInstall, Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	log.Close()
	for _, c := range []struct {
		family store.Family
		name   string
		want   bool
	}{
		{store.FamilyViews, "$page_view", true}, {store.FamilyProduct, "$page_view", false},
		{store.FamilyViews, "a", false}, {store.FamilyProduct, "a", true},
	} {
		if got, err := p.HasEvents(ctx, 1, c.family, c.name); err != nil || got != c.want {
			t.Errorf("HasEvents(1, %s, %q) = %v, %v; want %v", c.family, c.name, got, err, c.want)
		}
	}
	if _, err := f.ops.BlockAttribute(ctx, "test", 1, "plan"); err != nil {
		t.Fatal(err)
	}
	if got, err := p.HasAttribute(ctx, 1, "plan"); err != nil || got {
		t.Errorf("a blocked key: %v, %v; want false", got, err)
	}
	if got, err := p.HasAttribute(ctx, 1, "tier"); err != nil || !got {
		t.Errorf("another key: %v, %v; want true", got, err)
	}
}
