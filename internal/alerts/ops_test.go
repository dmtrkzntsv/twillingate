package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/native"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

// fakeProjects is the project registry as a map. Its ids are real rows
// of the store, which checks the project again.
type fakeProjects struct {
	archived    map[int64]bool // every project; true when archived
	renames     map[string]string
	blocked     map[string]bool
	attrRenames map[string]string // attribute rules, by bare key
	attrBlocked map[string]bool
}

func ruleKey(f store.Family, name string) string { return string(f) + "/" + name }

func (f *fakeProjects) Project(_ context.Context, id int64) (string, bool, bool) {
	archived, ok := f.archived[id]
	return fmt.Sprintf("p%d", id), archived, ok
}

func (f *fakeProjects) ResolveEvent(_ context.Context, _ int64, family store.Family, name string) (string, bool) {
	if to, ok := f.renames[ruleKey(family, name)]; ok {
		name = to
	}
	return name, f.blocked[ruleKey(family, name)]
}

func (f *fakeProjects) ResolveAttribute(_ context.Context, _ int64, key string) (string, bool) {
	if to, ok := f.attrRenames[key]; ok {
		key = to
	}
	return key, f.attrBlocked[key]
}

func (f *fakeProjects) Rules(context.Context) archive.Rules { return archive.Rules{} }

// fakePresence says an event or attribute was sent when it is in sent.
type fakePresence struct {
	sent map[string]bool // event names, and attr_<key> for attributes
	err  error
}

func (f *fakePresence) HasEvents(_ context.Context, _ int64, _ store.Family, name string) (bool, error) {
	return f.sent[name], f.err
}

func (f *fakePresence) HasAttribute(_ context.Context, _ int64, key string) (bool, error) {
	return f.sent["attr_"+key], f.err
}

// harness is Ops over a migrated store with a live project and an
// archived one, on a clock the test moves.
type harness struct {
	o        *Ops
	st       store.Store
	path     string
	projects *fakeProjects
	presence *fakePresence
	live     int64
	archived int64
	at       time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{
		path:     t.TempDir() + "/alerts.db",
		projects: &fakeProjects{archived: map[int64]bool{}, renames: map[string]string{}, blocked: map[string]bool{}},
		presence: &fakePresence{sent: map[string]bool{"signup": true, "paid": true, "$page_view": true}},
		at:       time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC),
	}
	h.st = storetest.Open(t, h.path)
	h.live = h.project(t, "live")
	h.archived = h.project(t, "old")
	if err := h.st.SetProjectArchived(ctx, h.archived, true, store.AuditEntry{Actor: "test", Action: "project.archive"}); err != nil {
		t.Fatal(err)
	}
	h.projects.archived[h.archived] = true
	h.o = NewOps(h.st, h.projects, nil, h.presence)
	h.o.now = func() time.Time { return h.at }
	return h
}

func (h *harness) project(t *testing.T, name string) int64 {
	t.Helper()
	id, err := h.st.CreateProject(context.Background(), store.RegistryProject{Name: name, AllowedOrigins: "[]"},
		store.AuditEntry{Actor: "test", Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	h.projects.archived[id] = false
	return id
}

// countIn is a count of signups held below 5, named name.
func countIn(name string) Input {
	in := statIn(store.AlertRead{}, below5, native.Metric{Op: "count"})
	in.Name = name
	return in
}

func (h *harness) create(t *testing.T, in Input) View {
	t.Helper()
	v, err := h.o.Create(context.Background(), "tester", h.live, in)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (h *harness) update(t *testing.T, id int64, p Patch) View {
	t.Helper()
	v, err := h.o.Update(context.Background(), "tester", h.live, id, p)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// evaluate records an evaluation of a at the harness's clock, as the
// runner would, serving its check request.
func (h *harness) evaluate(t *testing.T, a store.Alert, v float64) {
	t.Helper()
	at := h.at.UTC().Format(time.RFC3339)
	ok, err := h.st.RecordAlertEvaluation(context.Background(), store.AlertResult{AlertID: a.ID, Revision: a.Revision,
		Eval: store.AlertEvaluation{AlertID: a.ID, At: at, Value: &v, Outcome: store.AlertOK}, State: store.AlertOK})
	if err != nil || !ok {
		t.Fatalf("RecordAlertEvaluation = %v, %v", ok, err)
	}
}

func TestCreateAlertRefusesRenamedEvent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// An events-table query resolves its events as product and measures.
	h.projects.renames[ruleKey(store.FamilyProduct, "signup")] = "sign_up"
	_, err := h.o.Create(ctx, "tester", h.live, countIn("traffic"))
	mustInvalid(t, err, "`signup` is now `sign_up`", "alert on `sign_up`")

	clear(h.projects.renames)
	h.projects.blocked[ruleKey(store.FamilyMeasures, "signup")] = true
	_, err = h.o.Create(ctx, "tester", h.live, countIn("traffic"))
	mustInvalid(t, err, "`signup`", "blocked")

	// A $ event is never resolved; a views-table event is not either.
	clear(h.projects.blocked)
	h.projects.blocked[ruleKey(store.FamilyProduct, "$page_view")] = true
	h.projects.blocked[ruleKey(store.FamilyViews, "pricing")] = true
	in := statIn(store.AlertRead{}, below5, native.Metric{Op: "count"})
	in.Name, in.Query.Where = "views", []native.Filter{evt("$page_view")}
	h.create(t, in)
	views := Input{Name: "table views", Condition: below5, Query: &native.Query{Kind: "stat", Table: "views",
		Metrics: []native.Metric{{Op: "count"}}, Where: []native.Filter{evt("pricing")}}}
	h.create(t, views)

	// A product-table event resolves as a product event only.
	h.projects.blocked[ruleKey(store.FamilyMeasures, "paid")] = true
	product := Input{Name: "paid", Condition: below5, Query: &native.Query{Kind: "stat", Table: "product",
		Metrics: []native.Metric{{Op: "count"}}, Where: []native.Filter{evt("paid")}}}
	h.create(t, product)

	list, err := h.o.List(ctx, h.live, false)
	if err != nil || len(list) != 3 {
		t.Fatalf("List = %d alerts, %v; want 3", len(list), err)
	}
}

func TestCreateAlertRefusesRenamedAttribute(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	in := countIn("pro signups")
	in.Query.Where = append(in.Query.Where, native.Filter{Column: "attr_plan", Op: "eq", Values: []string{"pro"}})
	h.presence.sent["attr_plan"] = true

	h.projects.attrRenames = map[string]string{"plan": "tier"}
	_, err := h.o.Create(ctx, "tester", h.live, in)
	mustInvalid(t, err, "`attr_plan` is now `attr_tier` (an attribute rule)", "alert on `attr_tier`")

	h.projects.attrRenames = nil
	h.projects.attrBlocked = map[string]bool{"plan": true}
	_, err = h.o.Create(ctx, "tester", h.live, in)
	mustInvalid(t, err, "`attr_plan` is blocked by an attribute rule")

	// An attribute a metric's own where names is resolved too.
	h.projects.attrBlocked = nil
	h.projects.attrRenames = map[string]string{"price": "amount"}
	priced := Input{Name: "priced", Condition: below5, Query: &native.Query{Kind: "stat", Table: "events",
		Metrics: []native.Metric{{Op: "count", Where: []native.Filter{{Column: "attr_price", Op: "is_set"}}}},
		Where:   []native.Filter{evt("paid")}}}
	_, err = h.o.Create(ctx, "tester", h.live, priced)
	mustInvalid(t, err, "`attr_price` is now `attr_amount`")

	// An edit of the definition is checked as a create is; the event rule
	// wording stays the events'.
	h.projects.attrRenames = nil
	v := h.create(t, in)
	h.projects.attrRenames = map[string]string{"plan": "tier"}
	_, err = h.o.Update(ctx, "tester", h.live, v.ID, Patch{WindowDays: ptr(3)})
	mustInvalid(t, err, "(an attribute rule)")
	h.projects.attrRenames = nil
	h.projects.renames[ruleKey(store.FamilyProduct, "signup")] = "sign_up"
	_, err = h.o.Update(ctx, "tester", h.live, v.ID, Patch{WindowDays: ptr(3)})
	mustInvalid(t, err, "`signup` is now `sign_up` (an event rule)")
}

// TestUpdateAlertAcrossSilence: a condition crossing into or out of
// silence drops window_days and min_volume, which the other kind takes
// and silence refuses, unless the patch sets them.
func TestUpdateAlertAcrossSilence(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	in := countIn("signups")
	in.Condition, in.WindowDays, in.MinVolume = drop30, 7, 10
	v := h.create(t, in)

	where := []native.Filter{evt("signup")}
	s := h.update(t, v.ID, Patch{Condition: &silent6, Where: &where})
	if s.Def.Condition.Type != "silence" || s.Def.WindowDays != 1 || s.Def.MinVolume != 0 {
		t.Fatalf("into silence: %+v", s.Def)
	}
	// Set in the patch, they are still checked.
	_, err := h.o.Update(ctx, "tester", h.live, v.ID, Patch{Condition: &drop30, Query: in.Query, WindowDays: ptr(40)})
	mustInvalid(t, err, "window_days")

	// Out of silence the template's default window applies, not silence's 1.
	m := h.update(t, v.ID, Patch{Condition: &drop30, Template: ptr("conversion"),
		Fields: map[string]string{"from_event": "signup", "to_event": "paid"}})
	if m.Def.Condition.Type != "change" || m.Def.WindowDays != conversionWindowDays || m.Def.MinVolume != 0 {
		t.Fatalf("out of silence: %+v", m.Def)
	}
	// A patch's own window and volume are kept.
	s = h.update(t, v.ID, Patch{Condition: &silent6, Where: &where})
	m = h.update(t, v.ID, Patch{Condition: &drop30, Query: in.Query, WindowDays: ptr(3), MinVolume: ptr(20.0)})
	if m.Def.WindowDays != 3 || m.Def.MinVolume != 20 {
		t.Fatalf("out of silence with its own window: %+v", m.Def)
	}
	// A condition staying on its side keeps them.
	m = h.update(t, v.ID, Patch{Condition: &store.AlertCondition{Type: "change", Direction: "rise", Percent: 50, Against: "previous"}})
	if m.Def.WindowDays != 3 || m.Def.MinVolume != 20 {
		t.Fatalf("change to change: %+v", m.Def)
	}
}

// TestCreateAlertNeverFilterWarnsNothing: what a never filter names need
// not have been sent, so it is not warned of; a rule on it is still
// refused.
func TestCreateAlertNeverFilterWarnsNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.presence.sent["signup"] = true
	in := countIn("no refunds")
	in.Query.Where = append(in.Query.Where, native.Filter{Op: "never", Where: []native.Filter{
		evt("refund"), {Column: "attr_coupon", Op: "is_set"}}})
	if v := h.create(t, in); len(v.Warnings) != 0 {
		t.Errorf("Warnings = %q, want none", v.Warnings)
	}

	h.projects.renames[ruleKey(store.FamilyProduct, "refund")] = "refunded"
	in.Name = "second"
	_, err := h.o.Create(ctx, "tester", h.live, in)
	mustInvalid(t, err, "`refund` is now `refunded`")
}

func TestCreateAlertWarnsNeverSent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	in := countIn("signups done")
	in.Query.Where = []native.Filter{evt("signup_done"), {Column: "attr_plan", Op: "eq", Values: []string{"pro"}}}
	v := h.create(t, in)
	if v.ID == 0 || v.Name != "signups done" {
		t.Fatalf("created = %+v", v.Alert)
	}
	want := []string{
		"`signup_done` has never been sent by this project; the alert reads no_data until it is",
		"`attr_plan` has never been sent by this project; the alert reads no_data until it is",
	}
	if strings.Join(v.Warnings, "\n") != strings.Join(want, "\n") {
		t.Errorf("Warnings = %q, want %q", v.Warnings, want)
	}

	// A presence error warns too, and refuses nothing.
	h.presence.err = errors.New("disk on fire")
	in.Name = "second"
	v = h.create(t, in)
	if len(v.Warnings) != 2 || v.Warnings[0] != "could not check whether `signup_done` was sent: disk on fire" {
		t.Errorf("Warnings = %q", v.Warnings)
	}

	// Without presence, nothing is asked.
	o := NewOps(h.st, h.projects, nil, nil)
	in.Name = "third"
	if v, err := o.Create(ctx, "tester", h.live, in); err != nil || v.Warnings != nil {
		t.Errorf("Create without presence = %q, %v", v.Warnings, err)
	}
}

func TestCreateOnUnknownOrArchivedProject(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.o.Create(ctx, "tester", 999, countIn("traffic")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown project: %v, want ErrNotFound", err)
	}
	_, err := h.o.Create(ctx, "tester", h.archived, countIn("traffic"))
	mustInvalid(t, err, "restore the project first")

	// The name is trimmed, then checked.
	v := h.create(t, countIn("  traffic \t"))
	if v.Name != "traffic" {
		t.Errorf("name = %q, want trimmed", v.Name)
	}
	_, err = h.o.Create(ctx, "tester", h.live, countIn("   "))
	mustInvalid(t, err, "name")
	_, err = h.o.Create(ctx, "tester", h.live, Input{Name: "bad", Condition: below5})
	mustInvalid(t, err, "exactly one of")
	if _, err := h.o.Create(ctx, "tester", h.live, countIn("traffic")); !errors.Is(err, store.ErrConflict) {
		t.Errorf("taken name: %v, want ErrConflict", err)
	}

	// Every other operation refuses an unknown project too.
	for name, call := range map[string]func() error{
		"list":    func() error { _, err := h.o.List(ctx, 999, false); return err },
		"update":  func() error { _, err := h.o.Update(ctx, "tester", 999, v.ID, Patch{}); return err },
		"archive": func() error { _, err := h.o.Archive(ctx, "tester", 999, v.ID); return err },
		"restore": func() error { _, err := h.o.Restore(ctx, "tester", 999, v.ID); return err },
		"check":   func() error { _, err := h.o.Check(ctx, "tester", 999, v.ID); return err },
		"history": func() error { _, _, err := h.o.History(ctx, 999, v.ID, 0, ""); return err },
	} {
		if err := call(); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s on an unknown project: %v, want ErrNotFound", name, err)
		}
	}
}

// racingStore runs race once, after GetAlert reads, as a concurrent edit
// landing between Update's read and its write.
type racingStore struct {
	Store
	race func()
}

func (r *racingStore) GetAlert(ctx context.Context, projectID, id int64) (store.Alert, error) {
	a, err := r.Store.GetAlert(ctx, projectID, id)
	if r.race != nil {
		race := r.race
		r.race = nil
		race()
	}
	return a, err
}

// TestUpdateRefusesALostUpdate: an edit merged over an alert that changed
// since Update read it is refused (store.ErrConflict), and the other edit
// stands.
func TestUpdateRefusesALostUpdate(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	v := h.create(t, countIn("signups"))
	other := countIn("signups")
	other.WindowDays = 5
	h.o.st = &racingStore{Store: h.st, race: func() {
		if _, err := h.st.UpdateAlert(ctx, h.live, v.ID, store.AlertEdit{Def: ptr(mustBuild(t, other))}, h.at, auditOf("other", "update", v.ID)); err != nil {
			t.Fatal(err)
		}
	}}
	above5 := store.AlertCondition{Type: "threshold", Op: "above", Value: ptr(5.0)}
	_, err := h.o.Update(ctx, "tester", h.live, v.ID, Patch{Name: ptr("renamed"), Condition: &above5})
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "read it again") {
		t.Fatalf("err = %v, want ErrConflict asking to read it again", err)
	}
	got, err := h.st.GetAlert(ctx, h.live, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "signups" || got.Def.WindowDays != 5 || got.Def.Condition.Op != below5.Op {
		t.Errorf("stored %q, window %d, op %q; want the other edit alone", got.Name, got.Def.WindowDays, got.Def.Condition.Op)
	}
	// Read again, it applies over the other edit.
	u, err := h.o.Update(ctx, "tester", h.live, v.ID, Patch{Name: ptr("renamed"), Condition: &above5})
	if err != nil || u.Name != "renamed" || u.Def.WindowDays != 5 || u.Def.Condition.Op != above5.Op {
		t.Errorf("the edit again = %q, window %d, op %q, %v", u.Name, u.Def.WindowDays, u.Def.Condition.Op, err)
	}
}

func TestUpdateMergesPatch(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v1 := h.create(t, countIn("traffic"))

	below10 := store.AlertCondition{Type: "threshold", Op: "below", Value: ptr(10.0)}
	v2 := h.update(t, v1.ID, Patch{Condition: &below10})
	if v2.Revision != v1.Revision+1 || *v2.Def.Condition.Value != 10 || string(v2.Def.Query) != string(v1.Def.Query) {
		t.Errorf("condition change: revision %d → %d, condition %+v, query %s → %s",
			v1.Revision, v2.Revision, v2.Def.Condition, v1.Def.Query, v2.Def.Query)
	}

	v3 := h.update(t, v1.ID, Patch{Name: ptr(" visits ")})
	if v3.Revision != v2.Revision || v3.Name != "visits" {
		t.Errorf("rename: revision %d → %d, name %q", v2.Revision, v3.Revision, v3.Name)
	}
	v4 := h.update(t, v1.ID, Patch{Condition: &below10, WindowDays: ptr(1)})
	if v4.Revision != v3.Revision || v4.Warnings != nil {
		t.Errorf("the same definition again: revision %d → %d, warnings %q", v3.Revision, v4.Revision, v4.Warnings)
	}
	_, err := h.o.Update(ctx, "tester", h.live, v1.ID, Patch{Name: ptr("\t")})
	mustInvalid(t, err, "name")

	// A switch to silence needs where.
	_, err = h.o.Update(ctx, "tester", h.live, v1.ID, Patch{Condition: &silent6})
	mustInvalid(t, err, "where")
	where := []native.Filter{evt("signup_done")}
	v5 := h.update(t, v1.ID, Patch{Condition: &silent6, Where: &where})
	if v5.Parsed.Kind != "silence" || v5.Revision != v4.Revision+1 || len(v5.Warnings) != 1 || !strings.Contains(v5.Warnings[0], "signup_done") {
		t.Errorf("to silence: kind %q, revision %d, warnings %q", v5.Parsed.Kind, v5.Revision, v5.Warnings)
	}

	// A read alone keeps the query; a query alone replaces the read.
	two := statIn(store.AlertRead{Metric: "count"}, below5, native.Metric{Op: "count"}, native.Metric{Op: "actors"})
	two.Name = "two"
	t1 := h.create(t, two)
	t2 := h.update(t, t1.ID, Patch{Read: &store.AlertRead{Metric: "actors"}})
	if t2.Def.Read.Metric != "actors" || string(t2.Def.Query) != string(t1.Def.Query) {
		t.Errorf("read alone: read %+v, query %s", t2.Def.Read, t2.Def.Query)
	}
	_, err = h.o.Update(ctx, "tester", h.live, t1.ID, Patch{Read: &store.AlertRead{Metric: "count"}, Fields: map[string]string{"event": "paid"}})
	mustInvalid(t, err, "fields")
	_, err = h.o.Update(ctx, "tester", h.live, t1.ID, Patch{Query: &native.Query{Kind: "stat", Table: "events",
		Metrics: []native.Metric{{Op: "count"}, {Op: "users"}}}})
	mustInvalid(t, err, "read.metric")

	// A template alert's fields rebuild its query; its template stays.
	tpl := Input{Name: "tpl", Template: "event_count", Fields: map[string]string{"event": "signup"}, Condition: below5}
	p1 := h.create(t, tpl)
	p2 := h.update(t, p1.ID, Patch{Fields: map[string]string{"event": "paid"}})
	if p2.Def.Template != "event_count" || p2.Def.Fields["event"] != "paid" || p2.Revision != p1.Revision+1 ||
		len(p2.Parsed.References()) != 1 || p2.Parsed.References()[0].Event != "paid" {
		t.Errorf("fields: template %q, fields %v, refs %+v", p2.Def.Template, p2.Def.Fields, p2.Parsed.References())
	}
	// A new template replaces the old one whole, fields included.
	_, err = h.o.Update(ctx, "tester", h.live, p1.ID, Patch{Template: ptr("unique_people"), Fields: map[string]string{"who": "users"}})
	if err != nil {
		t.Fatal(err)
	}
	h.projects.renames[ruleKey(store.FamilyProduct, "signup")] = "sign_up"
	_, err = h.o.Update(ctx, "tester", h.live, p1.ID, Patch{Template: ptr("event_count"), Fields: map[string]string{"event": "signup"}})
	mustInvalid(t, err, "sign_up")
	// A pause or a rename does not check the events again.
	h.update(t, p1.ID, Patch{Name: ptr("tpl renamed"), Paused: ptr(true)})
	clear(h.projects.renames)

	// Snoozes: a past time is refused, a future one normalized, nil clears.
	past := h.at.Add(-time.Minute)
	_, err = h.o.Update(ctx, "tester", h.live, t1.ID, Patch{SnoozedUntil: ptr(&past)})
	mustInvalid(t, err, "snoozed_until")
	_, err = h.o.Update(ctx, "tester", h.live, t1.ID, Patch{SnoozedUntil: ptr(&h.at)})
	mustInvalid(t, err, "snoozed_until")
	until := h.at.Add(2*time.Hour + 500*time.Millisecond).In(time.FixedZone("x", 3600))
	s := h.update(t, t1.ID, Patch{SnoozedUntil: ptr(&until)})
	if s.SnoozedUntil != "2026-11-25T12:00:00Z" || s.State != store.AlertSnoozed {
		t.Errorf("snoozed until %q, state %q", s.SnoozedUntil, s.State)
	}
	s = h.update(t, t1.ID, Patch{SnoozedUntil: ptr[*time.Time](nil)})
	if s.SnoozedUntil != "" {
		t.Errorf("cleared snooze = %q", s.SnoozedUntil)
	}

	// An archived alert, and another project's, are not edited.
	if _, err := h.o.Archive(ctx, "tester", h.live, t1.ID); err != nil {
		t.Fatal(err)
	}
	_, err = h.o.Update(ctx, "tester", h.live, t1.ID, Patch{Condition: &below10})
	mustInvalid(t, err, "archived")
	other := h.project(t, "other")
	if _, err := h.o.Update(ctx, "tester", other, v1.ID, Patch{Name: ptr("x")}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another project's alert: %v, want ErrNotFound", err)
	}
}

func TestViewState(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v := h.create(t, countIn("traffic"))
	if v.State != store.AlertPending || v.ValueText != "—" || v.Parsed.Kind != "stat" {
		t.Errorf("new: state %q, value %q, kind %q", v.State, v.ValueText, v.Parsed.Kind)
	}
	if v = h.update(t, v.ID, Patch{Paused: ptr(true)}); v.State != store.AlertPaused || v.Alert.State != store.AlertPending {
		t.Errorf("paused: state %q (stored %q)", v.State, v.Alert.State)
	}
	until := h.at.Add(time.Hour)
	if v = h.update(t, v.ID, Patch{Paused: ptr(false), SnoozedUntil: ptr(&until)}); v.State != store.AlertSnoozed {
		t.Errorf("snoozed: state %q", v.State)
	}
	h.at = h.at.Add(2 * time.Hour)
	list, err := h.o.List(ctx, h.live, false)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %d, %v", len(list), err)
	}
	if list[0].State != store.AlertPending || list[0].Warnings != nil {
		t.Errorf("snooze over: state %q, warnings %q", list[0].State, list[0].Warnings)
	}
}

// auditRows reads the audit rows of action through a second handle.
func auditRows(t *testing.T, path, action string) [][3]string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.QueryContext(context.Background(), `SELECT actor, subject, COALESCE(detail, '') FROM audit_log WHERE action=? ORDER BY rowid`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][3]string
	for rows.Next() {
		var r [3]string
		if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestCheckAlert(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v := h.create(t, countIn("traffic"))
	subject := "alert/" + strconv.FormatInt(v.ID, 10)
	if got := auditRows(t, h.path, "alert.create"); len(got) != 1 || got[0] != [3]string{"tester", subject, "traffic"} {
		t.Errorf("alert.create rows = %q", got)
	}

	h.update(t, v.ID, Patch{Paused: ptr(true)})
	_, err := h.o.Check(ctx, "tester", h.live, v.ID)
	mustInvalid(t, err)
	until := h.at.Add(time.Hour)
	v = h.update(t, v.ID, Patch{Paused: ptr(false), SnoozedUntil: ptr(&until)})
	_, err = h.o.Check(ctx, "tester", h.live, v.ID)
	mustInvalid(t, err)
	v = h.update(t, v.ID, Patch{SnoozedUntil: ptr[*time.Time](nil)})

	// Clearing the snooze requested a check; serve it.
	h.evaluate(t, v.Alert, 3)
	h.at = h.at.Add(time.Minute)
	first, err := h.o.Check(ctx, "tester", h.live, v.ID)
	if err != nil || first.CheckRequestedAt != "2026-11-25T10:01:00Z" || !first.CheckPending() {
		t.Fatalf("Check = %q, %v", first.CheckRequestedAt, err)
	}
	if first.ValueText != "3" || len(first.Recent) != 1 {
		t.Errorf("value %q, recent %d", first.ValueText, len(first.Recent))
	}
	h.at = h.at.Add(time.Minute)
	second, err := h.o.Check(ctx, "tester", h.live, v.ID)
	if err != nil || second.CheckRequestedAt != first.CheckRequestedAt {
		t.Errorf("pending Check = %q, %v; want %q", second.CheckRequestedAt, err, first.CheckRequestedAt)
	}
	if got := auditRows(t, h.path, "alert.check"); len(got) != 1 || got[0] != [3]string{"tester", subject, "traffic"} {
		t.Errorf("alert.check rows = %q", got)
	}

	// An archived alert, and an alert of an archived project.
	if _, err := h.o.Archive(ctx, "tester", h.live, v.ID); err != nil {
		t.Fatal(err)
	}
	_, err = h.o.Check(ctx, "tester", h.live, v.ID)
	mustInvalid(t, err, "archived")
	def := v.Def
	old, err := h.st.CreateAlert(ctx, store.Alert{ProjectID: h.archived, Name: "old", Def: def}, MaxLive, h.at, store.AuditEntry{Actor: "test", Action: "alert.create"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.o.Check(ctx, "tester", h.archived, old.ID)
	mustInvalid(t, err)
}

func TestArchiveRestoreAlert(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v := h.create(t, countIn("traffic"))
	a, err := h.o.Archive(ctx, "tester", h.live, v.ID)
	if err != nil || a.ArchivedAt == "" || a.Revision != v.Revision+1 {
		t.Fatalf("Archive = %+v, %v", a.Alert, err)
	}
	if live, _ := h.o.List(ctx, h.live, false); len(live) != 0 {
		t.Errorf("live after archive = %d", len(live))
	}
	if gone, _ := h.o.List(ctx, h.live, true); len(gone) != 1 || gone[0].ID != v.ID {
		t.Errorf("archived list = %+v", gone)
	}
	r, err := h.o.Restore(ctx, "tester", h.live, v.ID)
	if err != nil || r.ArchivedAt != "" || r.State != store.AlertPending {
		t.Fatalf("Restore = %+v, %v", r.Alert, err)
	}
	subject := "alert/" + strconv.FormatInt(v.ID, 10)
	for _, action := range []string{"alert.archive", "alert.restore"} {
		if got := auditRows(t, h.path, action); len(got) != 1 || got[0][0] != "tester" || got[0][1] != subject {
			t.Errorf("%s rows = %q", action, got)
		}
	}
	if _, err := h.o.Archive(ctx, "tester", h.live, v.ID+100); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown alert: %v, want ErrNotFound", err)
	}
}

func TestHistoryOfAnotherProjectIsNotFound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v := h.create(t, countIn("traffic"))
	h.evaluate(t, v.Alert, 1)
	other := h.project(t, "other")
	if _, _, err := h.o.History(ctx, other, v.ID, 0, ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("History of another project's alert: %v, want ErrNotFound", err)
	}
	if got, next, err := h.o.History(ctx, h.live, v.ID, 0, ""); err != nil || len(got) != 1 || next != "" {
		t.Errorf("own History = %d, %q, %v", len(got), next, err)
	}
}

func TestHistoryCursor(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v := h.create(t, countIn("traffic"))
	for i := 1; i <= 5; i++ {
		h.at = h.at.Add(time.Minute)
		h.evaluate(t, v.Alert, float64(i))
	}
	var values []float64
	var pages int
	for cursor := ""; ; {
		got, next, err := h.o.History(ctx, h.live, v.ID, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, e := range got {
			values = append(values, *e.Value)
		}
		if next == "" {
			break
		}
		if want := strconv.FormatInt(got[len(got)-1].ID, 10); next != want {
			t.Fatalf("next = %q, want the last id %q", next, want)
		}
		cursor = next
	}
	if fmt.Sprint(values) != "[5 4 3 2 1]" || pages != 3 {
		t.Errorf("paged %v over %d pages, want [5 4 3 2 1] over 3", values, pages)
	}
	if got, next, err := h.o.History(ctx, h.live, v.ID, 5, ""); err != nil || len(got) != 5 || next != "" {
		t.Errorf("a page of exactly all = %d, %q, %v", len(got), next, err)
	}
	if got, _, err := h.o.History(ctx, h.live, v.ID, 1000, ""); err != nil || len(got) != 5 {
		t.Errorf("limit above the most = %d, %v", len(got), err)
	}
	for _, bad := range []string{"x", "0", "-3", "1.5"} {
		_, _, err := h.o.History(ctx, h.live, v.ID, 0, bad)
		mustInvalid(t, err, "cursor")
	}
}

func TestListRecent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v := h.create(t, countIn("traffic"))
	quiet := h.create(t, countIn("quiet"))
	for i := 1; i <= 20; i++ {
		h.at = h.at.Add(time.Minute)
		h.evaluate(t, v.Alert, float64(i)*100)
	}
	list, err := h.o.List(ctx, h.live, false)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %d, %v", len(list), err)
	}
	byName := map[string]View{}
	for _, l := range list {
		byName[l.Name] = l
	}
	got := byName["traffic"]
	if len(got.Recent) != 14 || *got.Recent[0].Value != 2000 || *got.Recent[13].Value != 700 {
		t.Errorf("Recent = %d evaluations, newest %v", len(got.Recent), deref(got.Recent[0].Value))
	}
	if got.ValueText != "2,000" || got.State != store.AlertOK {
		t.Errorf("value %q, state %q", got.ValueText, got.State)
	}
	if q := byName["quiet"]; q.ID != quiet.ID || q.Recent != nil {
		t.Errorf("quiet = %+v", q)
	}
}

func TestViewOfUnparseableDefinition(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	def := store.AlertDef{Query: json.RawMessage(`{"kind":"nope"}`), Condition: below5, WindowDays: 1}
	a, err := h.st.CreateAlert(ctx, store.Alert{ProjectID: h.live, Name: "broken", Def: def}, MaxLive, h.at, store.AuditEntry{Actor: "test", Action: "alert.create"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := h.o.List(ctx, h.live, false)
	if err != nil || len(list) != 1 || list[0].ID != a.ID || list[0].Parsed.Kind != "" || list[0].ValueText != "—" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	// It can still be renamed, paused and archived; a new condition is checked.
	h.update(t, a.ID, Patch{Name: ptr("renamed"), Paused: ptr(true)})
	_, err = h.o.Update(ctx, "tester", h.live, a.ID, Patch{Condition: &drop30})
	mustInvalid(t, err)
}
