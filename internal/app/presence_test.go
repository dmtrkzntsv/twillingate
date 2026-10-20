package app

import (
	"context"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// presenceFixture is the widgets fixture with a filePresence over its
// files, and send, which appends one product event of project to them.
func presenceFixture(t *testing.T) (*widgetsFixture, *filePresence, func(id string, project int64, name string, attrs map[string]string)) {
	t.Helper()
	f := newWidgetsFixture(t)
	eng, reader, err := openFiles(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	send := func(id string, project int64, name string, attrs map[string]string) {
		t.Helper()
		log, err := archive.OpenLog(f.cfg.EventsDir)
		if err != nil {
			t.Fatal(err)
		}
		defer log.Close()
		at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
		if err := log.WriteEvents(context.Background(), []store.Event{{ID: id, ProjectID: project,
			Family: store.FamilyProduct, EventName: name, TS: at, ReceivedAt: at, Kind: "app",
			ActorID: "a", ActorKind: store.ActorInstall, SampleRate: 1, Attributes: attrs}}); err != nil {
			t.Fatal(err)
		}
	}
	return f, &filePresence{reader: reader, reg: f.reg}, send
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
