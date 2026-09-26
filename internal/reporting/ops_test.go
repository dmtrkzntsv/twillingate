package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const systemRefusal = "dashboard 3 is a system dashboard and changes only with a release; duplicate_dashboard makes an editable copy"

// --- Placement ---

func TestPlaceAfterNilFirstAndID(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "D")
	a := mustAdd(t, svc, d.ID, nil, note("A"))
	mustAdd(t, svc, d.ID, nil, note("B"))
	mustAdd(t, svc, d.ID, ptr(int64(0)), note("C"))
	mustAdd(t, svc, d.ID, &a.ID, note("D"))
	if got, want := liveNames(t, svc, d.ID), []string{"c", "a", "d", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("widgets = %v, want %v", got, want)
	}

	one := mustCreate(t, svc, "One")
	mustCreate(t, svc, "Two")
	if _, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "Zero", After: ptr(int64(0))}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "OneAndHalf", After: &one.ID}); err != nil {
		t.Fatal(err)
	}
	list, err := svc.Dashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, x := range list.Dashboards {
		titles = append(titles, x.Title)
	}
	if want := []string{"Zero", "D", "One", "OneAndHalf", "Two"}; !reflect.DeepEqual(titles, want) {
		t.Errorf("dashboards = %v, want %v", titles, want)
	}
}

func TestPlaceKeysBetweenFullOrder(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "D", note("A"), note("B"), note("C"))
	a, b := d.Widgets[0], d.Widgets[1]
	if err := svc.ArchiveWidget(ctx, "test", b.ID); err != nil {
		t.Fatal(err)
	}
	before := widgetRows(t, svc, d.ID)
	w := mustAdd(t, svc, d.ID, &a.ID, note("X"))
	want, err := sortkey.Between(before[0].SortKey, before[1].SortKey) // after a, before archived b
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.st.GetWidget(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SortKey != want {
		t.Errorf("key = %q, want Between(%q, %q) = %q", got.SortKey, before[0].SortKey, before[1].SortKey, want)
	}
}

func TestPlaceWidgetAfterMustBeOnSameDashboard(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d1 := mustCreate(t, svc, "One")
	d2 := mustCreate(t, svc, "Two", note("Elsewhere"))
	other := d2.Widgets[0].ID
	_, err := svc.AddWidget(ctx, "test", AddWidget{DashboardID: d1.ID, After: &other, WidgetSpec: note("X")})
	wantRefusal(t, err, store.ErrInvalid, "after "+itoa(other)+" is not a widget on dashboard "+itoa(d1.ID))
}

func TestPlaceDashboardAfterMustBeUserDashboard(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemDashboard())
	ctx := context.Background()
	_, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "X", After: ptr(int64(3))})
	wantRefusal(t, err, store.ErrInvalid, "after 3 is not a user dashboard")
	d := mustCreate(t, svc, "D")
	_, err = svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: d.ID, After: ptr(int64(9999))})
	wantRefusal(t, err, store.ErrInvalid, "after 9999 is not a user dashboard")
}

func TestPlaceInsertWritesOneRow(t *testing.T) {
	svc := newTestService(t)
	d := mustCreate(t, svc, "D", note("A"), note("B"))
	before := widgetRows(t, svc, d.ID)
	mustAdd(t, svc, d.ID, &d.Widgets[0].ID, note("X"))
	after := widgetRows(t, svc, d.ID)
	if len(after) != len(before)+1 {
		t.Fatalf("rows = %d, want %d", len(after), len(before)+1)
	}
	keys := map[int64]string{}
	for _, w := range after {
		keys[w.ID] = w.SortKey
	}
	for _, w := range before {
		if keys[w.ID] != w.SortKey {
			t.Errorf("widget %d key %q changed to %q", w.ID, w.SortKey, keys[w.ID])
		}
	}

	d2 := mustCreate(t, svc, "E")
	beforeD, err := svc.st.GetDashboard(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateDashboard(context.Background(), "test", UpdateDashboard{ID: d2.ID, After: ptr(int64(0))}); err != nil {
		t.Fatal(err)
	}
	afterD, err := svc.st.GetDashboard(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterD.SortKey != beforeD.SortKey {
		t.Errorf("neighbour dashboard key %q changed to %q", beforeD.SortKey, afterD.SortKey)
	}
}

// racingStore makes every InsertWidget lose a race: before passing the
// insert through, a rival widget takes the same sort key.
type racingStore struct {
	Store
	races, limit int
}

func (r *racingStore) InsertWidget(ctx context.Context, w store.Widget, a store.AuditEntry) (int64, error) {
	if r.races < r.limit {
		r.races++
		rival := w
		rival.Name = "rival-" + itoa(int64(r.races))
		if _, err := r.Store.InsertWidget(ctx, rival, store.AuditEntry{Actor: "rival", Action: "rival.add"}); err != nil {
			return 0, err
		}
	}
	return r.Store.InsertWidget(ctx, w, a)
}

func TestPlaceRetriesOnceOnConflict(t *testing.T) {
	base := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, base, "D", note("A"))

	once := New(&racingStore{Store: base.st, limit: 1}, base.db, Options{})
	w, err := once.AddWidget(ctx, "test", AddWidget{DashboardID: d.ID, WidgetSpec: note("Mine")})
	if err != nil {
		t.Fatalf("AddWidget after one lost race: %v", err)
	}
	if w.Name != "mine" {
		t.Errorf("name = %q", w.Name)
	}
	if got, want := liveNames(t, base, d.ID), []string{"a", "rival-1", "mine"}; !reflect.DeepEqual(got, want) {
		t.Errorf("widgets = %v, want %v", got, want)
	}

	rs := &racingStore{Store: base.st, limit: 10}
	always := New(rs, base.db, Options{})
	_, err = always.AddWidget(ctx, "test", AddWidget{DashboardID: d.ID, WidgetSpec: note("Again")})
	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
	if rs.races != 2 {
		t.Errorf("attempts = %d, want 2 (one retry)", rs.races)
	}
}

// --- Create ---

func TestCreateDashboardAllOrNothing(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "D", Widgets: []WidgetSpec{
		note("Good"),
		{Component: "markdown", Title: "Empty", Source: md("  ")},
	}})
	if !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), "markdown text is empty") {
		t.Fatalf("err = %v, want the empty markdown refused", err)
	}
	list, err := svc.Dashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Dashboards) != 0 {
		t.Errorf("dashboards = %+v, want none", list.Dashboards)
	}
	if rows, err := svc.st.ListWidgets(ctx, 0); err != nil || len(rows) != 0 {
		t.Errorf("widgets = %v, %v; want none", rows, err)
	}
}

func TestCreateDashboardRange(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "D")
	if d.Range != "7d" {
		t.Errorf("range = %q, want 7d", d.Range)
	}
	d2, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "E", Range: "90d"})
	if err != nil || d2.Range != "90d" {
		t.Errorf("range = %q, %v; want 90d", d2.Range, err)
	}
	_, err = svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "F", Range: "14d"})
	wantRefusal(t, err, store.ErrInvalid, "range must be one of today, yesterday, 7d, 30d, 90d, custom")
	_, err = svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "G", Range: "custom"})
	wantRefusal(t, err, store.ErrInvalid, "create with a preset; the viewer picks custom dates")
}

func TestCreateDashboardWidgetNames(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	named := note("Other")
	named.Name = "visitors-2"
	d := mustCreate(t, svc, "D", note("Visitors"), named, note("Visitors"), note(""))
	var names []string
	for _, w := range d.Widgets {
		names = append(names, w.Name)
	}
	if want := []string{"visitors", "visitors-2", "visitors-3", "widget"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}

	dup := note("B")
	dup.Name = "a"
	first := note("A")
	first.Name = "a"
	_, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "E", Widgets: []WidgetSpec{first, dup}})
	wantRefusal(t, err, store.ErrConflict, "widget name a is already used on this dashboard")
}

func TestCreateDashboardKeysSpread(t *testing.T) {
	svc := newTestService(t)
	d := mustCreate(t, svc, "D", note("A"), note("B"), note("C"))
	want, err := sortkey.Spread("", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, w := range widgetRows(t, svc, d.ID) {
		got = append(got, w.SortKey)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
}

func TestCreateDashboardSizesDefault(t *testing.T) {
	svc := newTestService(t)
	sized := note("Sized")
	sized.Width = 4
	d := mustCreate(t, svc, "D", note("Default"), sized)
	if w := d.Widgets[0]; w.Width != 12 || w.Height != 2 {
		t.Errorf("default size = %dx%d, want 12x2 (markdown's)", w.Width, w.Height)
	}
	if w := d.Widgets[1]; w.Width != 4 || w.Height != 2 {
		t.Errorf("size = %dx%d, want 4x2", w.Width, w.Height)
	}
}

// --- System dashboards ---

func TestSystemDashboardRefusesWrites(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemDashboard())
	ctx := context.Background()
	sysWidget := widgetRows(t, svc, 3)[0].ID
	for name, op := range map[string]func() error{
		"update": func() error {
			_, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: 3, Title: "X"})
			return err
		},
		"archive": func() error { return svc.ArchiveDashboard(ctx, "test", 3) },
		"restore": func() error { return svc.RestoreDashboard(ctx, "test", 3) },
		"add": func() error {
			_, err := svc.AddWidget(ctx, "test", AddWidget{DashboardID: 3, WidgetSpec: note("X")})
			return err
		},
		"update widget": func() error {
			_, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: sysWidget, Title: ptr("X")})
			return err
		},
		"archive widget": func() error { return svc.ArchiveWidget(ctx, "test", sysWidget) },
		"restore widget": func() error { return svc.RestoreWidget(ctx, "test", sysWidget) },
		"copy into": func() error {
			_, err := svc.CopyWidget(ctx, "test", CopyWidget{ID: sysWidget, DashboardID: 3})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) { wantRefusal(t, op(), store.ErrInvalid, systemRefusal) })
	}
	if rows := auditRows(t, svc); len(rows) != 0 {
		t.Errorf("audit = %v, want none", rows)
	}
}

func TestSystemDashboardAllowsDuplicateCopyAndView(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemDashboard())
	ctx := context.Background()
	if _, err := svc.DuplicateDashboard(ctx, "test", 3); err != nil {
		t.Errorf("duplicate: %v", err)
	}
	d := mustCreate(t, svc, "D")
	if _, err := svc.CopyWidget(ctx, "test", CopyWidget{ID: widgetRows(t, svc, 3)[0].ID, DashboardID: d.ID}); err != nil {
		t.Errorf("copy from: %v", err)
	}
	if err := svc.SetView(ctx, View{DashboardID: 3}); err != nil {
		t.Errorf("set view: %v", err)
	}
}

// --- Duplicate ---

func TestDuplicateDashboard(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemDashboard())
	ctx := context.Background()
	sized := note("B")
	sized.Width, sized.Height = 5, 7
	src := mustCreate(t, svc, "Src", note("A"), sized, note("C"))
	mustCreate(t, svc, "Other")
	if err := svc.ArchiveWidget(ctx, "test", src.Widgets[2].ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.st.SetDashboardView(ctx, store.Dashboard{ID: src.ID, LastProjectID: 7, LastRange: "custom", LastFrom: "2026-08-01", LastTo: "2026-08-31"}); err != nil {
		t.Fatal(err)
	}

	cp, err := svc.DuplicateDashboard(ctx, "test", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Title != "Src (copy)" || cp.Owner != store.OwnerUser {
		t.Errorf("copy = %q owned by %q", cp.Title, cp.Owner)
	}
	if cp.ProjectID != 7 || cp.Range != "custom" || cp.From != "2026-08-01" || cp.To != "2026-08-31" {
		t.Errorf("selection = %d %s %s %s, want the source's", cp.ProjectID, cp.Range, cp.From, cp.To)
	}
	if len(cp.Widgets) != 2 {
		t.Fatalf("widgets = %d, want the 2 live ones", len(cp.Widgets))
	}
	for i, w := range cp.Widgets {
		s := src.Widgets[i]
		if w.ID == s.ID || w.Name != s.Name || w.Width != s.Width || w.Height != s.Height ||
			w.Source != s.Source || *w.Component != *s.Component || w.Title != s.Title {
			t.Errorf("widget %d = %+v, want a copy of %+v", i, w, s)
		}
	}
	want, _ := sortkey.Spread("", "", 2)
	var keys []string
	for _, w := range widgetRows(t, svc, cp.ID) {
		keys = append(keys, w.SortKey)
	}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want fresh %v", keys, want)
	}
	list, _ := svc.Dashboards(ctx)
	if last := list.Dashboards[len(list.Dashboards)-1]; last.ID != cp.ID {
		t.Errorf("last dashboard = %d, want the copy %d", last.ID, cp.ID)
	}

	sys, err := svc.DuplicateDashboard(ctx, "test", 3)
	if err != nil {
		t.Fatal(err)
	}
	if sys.Owner != store.OwnerUser || sys.Title != "Users (copy)" || len(sys.Widgets) != 1 || sys.Range != "7d" {
		t.Errorf("system copy = %+v", sys)
	}
}

// --- Archived items ---

func TestArchivedWidgetRefusesUpdateAndCopy(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "D", note("A"))
	id := d.Widgets[0].ID
	if err := svc.ArchiveWidget(ctx, "test", id); err != nil {
		t.Fatal(err)
	}
	msg := "widget " + itoa(id) + " is archived; restore_widget first"
	_, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Title: ptr("X")})
	wantRefusal(t, err, store.ErrInvalid, msg)
	_, err = svc.CopyWidget(ctx, "test", CopyWidget{ID: id, DashboardID: d.ID})
	wantRefusal(t, err, store.ErrInvalid, msg)
}

func TestArchivedDashboardRefusesAddAndUpdate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "D")
	if err := svc.ArchiveDashboard(ctx, "test", d.ID); err != nil {
		t.Fatal(err)
	}
	msg := "dashboard " + itoa(d.ID) + " is archived; restore_dashboard first"
	_, err := svc.AddWidget(ctx, "test", AddWidget{DashboardID: d.ID, WidgetSpec: note("A")})
	wantRefusal(t, err, store.ErrInvalid, msg)
	_, err = svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: d.ID, Title: "E"})
	wantRefusal(t, err, store.ErrInvalid, msg)
	if err := svc.RestoreDashboard(ctx, "test", d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: d.ID, Title: "E"}); err != nil {
		t.Errorf("update after restore: %v", err)
	}
}

func TestRestoreWidgetKeepsKey(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "D", note("A"), note("B"), note("C"))
	b := d.Widgets[1].ID
	before, _ := svc.st.GetWidget(ctx, b)
	if err := svc.ArchiveWidget(ctx, "test", b); err != nil {
		t.Fatal(err)
	}
	if got := liveNames(t, svc, d.ID); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("live = %v", got)
	}
	if err := svc.RestoreWidget(ctx, "test", b); err != nil {
		t.Fatal(err)
	}
	after, _ := svc.st.GetWidget(ctx, b)
	if after.SortKey != before.SortKey {
		t.Errorf("key %q became %q", before.SortKey, after.SortKey)
	}
	if got := liveNames(t, svc, d.ID); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("live = %v, want b back in place", got)
	}
}

func TestInsertNextToArchivedWidget(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "D", note("A"), note("B"))
	if err := svc.ArchiveWidget(ctx, "test", d.Widgets[1].ID); err != nil {
		t.Fatal(err)
	}
	// Last in the live order is right after a, which is where archived b sits.
	w := mustAdd(t, svc, d.ID, nil, note("X"))
	mustAdd(t, svc, d.ID, &d.Widgets[0].ID, note("Y"))
	seen := map[string]bool{}
	for _, r := range widgetRows(t, svc, d.ID) {
		if seen[r.SortKey] {
			t.Errorf("key %q repeats", r.SortKey)
		}
		seen[r.SortKey] = true
	}
	if err := svc.RestoreWidget(ctx, "test", d.Widgets[1].ID); err != nil {
		t.Fatal(err)
	}
	if got, want := liveNames(t, svc, d.ID), []string{"a", "y", "b", w.Name}; !reflect.DeepEqual(got, want) {
		t.Errorf("live = %v, want %v", got, want)
	}
}

// --- Removed component ---

// removedWidget makes a user dashboard with one pie widget, then drops
// pie from the components, leaving the widget with none.
func removedWidget(t *testing.T, svc *Service) (DashboardDetail, int64) {
	t.Helper()
	d := mustCreate(t, svc, "D", WidgetSpec{
		Component: "pie", Title: "Share",
		Source: Source{Type: "sql", Content: "SELECT 'a' AS label, 1 AS value"},
	})
	syncReporting(t, svc, []string{"pie"})
	return d, d.Widgets[0].ID
}

func TestRemovedComponentUpdate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, id := removedWidget(t, svc)
	msg := "widget " + itoa(id) + "'s component was removed; set component first"
	for name, in := range map[string]UpdateWidget{
		"name":   {ID: id, Name: ptr("x")},
		"title":  {ID: id, Title: ptr("x")},
		"props":  {ID: id, Props: json.RawMessage(`{}`)},
		"source": {ID: id, Source: &Source{Type: "sql", Content: "SELECT 1 AS x"}},
	} {
		_, err := svc.UpdateWidget(ctx, "test", in)
		t.Run(name, func(t *testing.T) { wantRefusal(t, err, store.ErrInvalid, msg) })
	}
	w, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Width: ptr(5), Height: ptr(4)})
	if err != nil || w.Width != 5 || w.Height != 4 || w.Component != nil {
		t.Errorf("resize = %+v, %v", w, err)
	}
	_, err = svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Component: ptr("pie")})
	wantRefusal(t, err, store.ErrInvalid, "component pie does not exist; list_components names the ones there are")
	w, err = svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Component: ptr("table")})
	if err != nil || w.Component == nil || *w.Component != "table" {
		t.Errorf("set component = %+v, %v", w, err)
	}
}

func TestRemovedComponentCopyRefused(t *testing.T) {
	svc := newTestService(t)
	d, id := removedWidget(t, svc)
	_, err := svc.CopyWidget(context.Background(), "test", CopyWidget{ID: id, DashboardID: d.ID})
	wantRefusal(t, err, store.ErrInvalid, "widget "+itoa(id)+"'s component was removed; set component first")
}

func TestRemovedComponentArchive(t *testing.T) {
	svc := newTestService(t)
	d, id := removedWidget(t, svc)
	if err := svc.ArchiveWidget(context.Background(), "test", id); err != nil {
		t.Fatal(err)
	}
	if got := liveNames(t, svc, d.ID); len(got) != 0 {
		t.Errorf("live = %v, want none", got)
	}
}

// --- Update ---

func TestUpdateWidgetRevalidates(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "D", note("A"), note("B"))
	id := d.Widgets[0].ID

	_, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Props: json.RawMessage(`{"x":1}`)})
	if !errors.Is(err, store.ErrInvalid) {
		t.Errorf("props against the schema: err = %v, want ErrInvalid", err)
	}
	_, err = svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Width: ptr(13)})
	wantRefusal(t, err, store.ErrInvalid, "width is columns out of 12, from 1 to 12")
	_, err = svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Component: ptr("stat")})
	wantRefusal(t, err, store.ErrInvalid, "stat does not accept md; it accepts sql")
	_, err = svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Name: ptr("b")})
	wantRefusal(t, err, store.ErrConflict, "widget name b is already used on this dashboard")

	w, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Name: ptr("first"), Title: ptr("First"), Source: ptr(md("new text"))})
	if err != nil {
		t.Fatal(err)
	}
	if w.Name != "first" || w.Title != "First" || w.Source != md("new text") || w.Width != 12 {
		t.Errorf("updated = %+v", w)
	}
	if _, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Name: ptr("first")}); err != nil {
		t.Errorf("keeping its own name: %v", err)
	}
}

// --- Copy ---

func TestCopyWidget(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemDashboard())
	ctx := context.Background()
	sized := note("A")
	sized.Width, sized.Height = 5, 6
	src := mustCreate(t, svc, "Src", sized)
	dst := mustCreate(t, svc, "Dst", note("Z"))

	w, err := svc.CopyWidget(ctx, "test", CopyWidget{ID: src.Widgets[0].ID, DashboardID: dst.ID, After: ptr(int64(0))})
	if err != nil {
		t.Fatal(err)
	}
	if w.DashboardID != dst.ID || w.Name != "a" || w.Width != 5 || w.Height != 6 || w.Source != src.Widgets[0].Source {
		t.Errorf("copy = %+v", w)
	}
	again, err := svc.CopyWidget(ctx, "test", CopyWidget{ID: src.Widgets[0].ID, DashboardID: dst.ID})
	if err != nil || again.Name != "a-2" {
		t.Errorf("second copy name = %q, %v; want a-2", again.Name, err)
	}
	if got := liveNames(t, svc, dst.ID); !reflect.DeepEqual(got, []string{"a", "z", "a-2"}) {
		t.Errorf("dst = %v", got)
	}
	sys, err := svc.CopyWidget(ctx, "test", CopyWidget{ID: widgetRows(t, svc, 3)[0].ID, DashboardID: dst.ID})
	if err != nil || sys.Name != "note" || sys.Source != md("system text") {
		t.Errorf("copy of a system widget = %+v, %v", sys, err)
	}
}

// --- SetView ---

// switcherDashboard makes a user dashboard whose widgets follow what the
// flags say: a table widget reading :project, one reading :from/:to.
func switcherDashboard(t *testing.T, svc *Service, project, rng bool) DashboardDetail {
	t.Helper()
	specs := []WidgetSpec{note("Text")}
	if project {
		specs = append(specs, WidgetSpec{Component: "table", Title: "P", Source: Source{Type: "sql", Content: "SELECT :project AS p"}})
	}
	if rng {
		specs = append(specs, WidgetSpec{Component: "table", Title: "R", Source: Source{Type: "sql", Content: "SELECT :from AS f, :to AS t"}})
	}
	return mustCreate(t, svc, "D", specs...)
}

func TestSetViewSwitchersRequiredAndRefused(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	both := switcherDashboard(t, svc, true, true)
	if !both.FollowsProject || !both.FollowsRange {
		t.Fatalf("follows = %v %v, want both", both.FollowsProject, both.FollowsRange)
	}
	id := itoa(both.ID)
	wantRefusal(t, svc.SetView(ctx, View{DashboardID: both.ID, Range: "7d"}),
		store.ErrInvalid, "project_id is required: dashboard "+id+" has a project switcher")
	wantRefusal(t, svc.SetView(ctx, View{DashboardID: both.ID, ProjectID: 7}),
		store.ErrInvalid, "range is required: dashboard "+id+" has a range switcher")
	wantRefusal(t, svc.SetView(ctx, View{DashboardID: both.ID, ProjectID: 7, Range: "14d"}),
		store.ErrInvalid, "range must be one of today, yesterday, 7d, 30d, 90d, custom")
	if err := svc.SetView(ctx, View{DashboardID: both.ID, ProjectID: 7, Range: "30d"}); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Dashboard(ctx, both.ID)
	if got.ProjectID != 7 || got.Range != "30d" {
		t.Errorf("stored = %d %s", got.ProjectID, got.Range)
	}

	neither := switcherDashboard(t, svc, false, false)
	nid := itoa(neither.ID)
	wantRefusal(t, svc.SetView(ctx, View{DashboardID: neither.ID, ProjectID: 7}),
		store.ErrInvalid, "dashboard "+nid+" has no project switcher; omit project_id")
	wantRefusal(t, svc.SetView(ctx, View{DashboardID: neither.ID, Range: "7d"}),
		store.ErrInvalid, "dashboard "+nid+" has no range switcher; omit range, from and to")
	if err := svc.SetView(ctx, View{DashboardID: neither.ID}); err != nil {
		t.Errorf("empty view on a fixed dashboard: %v", err)
	}
	if got, _ := svc.Dashboard(ctx, neither.ID); got.Range != "7d" {
		t.Errorf("range = %q, want the stored 7d kept", got.Range)
	}
}

func TestSetViewCustomDates(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := switcherDashboard(t, svc, false, true)
	for _, c := range []struct {
		v   View
		msg string
	}{
		{View{Range: "7d", From: "2026-08-01", To: "2026-08-02"}, "from and to go only with range custom"},
		{View{Range: "custom", From: "2026-08-01"}, "from and to are days, YYYY-MM-DD"},
		{View{Range: "custom", From: "2026-08-01", To: "31/08/2026"}, "from and to are days, YYYY-MM-DD"},
		{View{Range: "custom", From: "2026-08-02", To: "2026-08-01"}, "from 2026-08-02 is after to 2026-08-01"},
		{View{Range: "custom", From: "2025-01-01", To: "2026-01-02"}, "from 2025-01-01 to 2026-01-02 spans more than 365 days"},
	} {
		c.v.DashboardID = d.ID
		wantRefusal(t, svc.SetView(ctx, c.v), store.ErrInvalid, c.msg)
	}
	if err := svc.SetView(ctx, View{DashboardID: d.ID, Range: "custom", From: "2025-01-01", To: "2026-01-01"}); err != nil {
		t.Fatalf("365 days: %v", err)
	}
	got, _ := svc.Dashboard(ctx, d.ID)
	if got.Range != "custom" || got.From != "2025-01-01" || got.To != "2026-01-01" {
		t.Errorf("stored = %s %s %s", got.Range, got.From, got.To)
	}
}

func TestSetViewSystemDashboardNoAudit(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemDashboard())
	if err := svc.SetView(context.Background(), View{DashboardID: 3}); err != nil {
		t.Fatal(err)
	}
	d := switcherDashboard(t, svc, false, true)
	before := len(auditRows(t, svc))
	if err := svc.SetView(context.Background(), View{DashboardID: d.ID, Range: "today"}); err != nil {
		t.Fatal(err)
	}
	if got := len(auditRows(t, svc)); got != before {
		t.Errorf("audit rows %d -> %d, want none written", before, got)
	}
}

// --- Widgets filter ---

func TestWidgetsFilter(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d1 := mustCreate(t, svc, "One", note("A"), WidgetSpec{Component: "table", Title: "T", Source: Source{Type: "sql", Content: "SELECT 1 AS n"}})
	d2 := mustCreate(t, svc, "Two", note("B"))
	if err := svc.ArchiveWidget(ctx, "test", d1.Widgets[0].ID); err != nil {
		t.Fatal(err)
	}
	names := func(dash int64, comp string) []string {
		t.Helper()
		ws, err := svc.Widgets(ctx, dash, comp)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, w := range ws {
			out = append(out, w.Dashboard.Title+"/"+w.Name+"#"+itoa(int64(w.Position)))
		}
		return out
	}
	if got, want := names(0, ""), []string{"One/a#1", "One/t#2", "Two/b#1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("all = %v, want %v", got, want)
	}
	if got, want := names(d1.ID, ""), []string{"One/a#1", "One/t#2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("dashboard = %v, want %v", got, want)
	}
	if got, want := names(0, "markdown"), []string{"One/a#1", "Two/b#1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("component = %v, want %v", got, want)
	}
	if got, want := names(d2.ID, "markdown"), []string{"Two/b#1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("both = %v, want %v", got, want)
	}
	ws, _ := svc.Widgets(ctx, d1.ID, "markdown")
	if len(ws) != 1 || ws[0].ArchivedAt == "" || ws[0].Dashboard.Widgets != 1 {
		t.Errorf("archived widget listed as %+v", ws)
	}
}

// --- Audit ---

func TestAuditActions(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	d, err := svc.CreateDashboard(ctx, "mcp", CreateDashboard{Title: "D", Widgets: []WidgetSpec{note("A")}})
	must(err)
	_, err = svc.UpdateDashboard(ctx, "rest", UpdateDashboard{ID: d.ID, Title: "E"})
	must(err)
	cp, err := svc.DuplicateDashboard(ctx, "mcp", d.ID)
	must(err)
	must(svc.ArchiveDashboard(ctx, "rest", cp.ID))
	must(svc.RestoreDashboard(ctx, "mcp", cp.ID))
	w, err := svc.AddWidget(ctx, "rest", AddWidget{DashboardID: d.ID, WidgetSpec: note("B")})
	must(err)
	_, err = svc.UpdateWidget(ctx, "mcp", UpdateWidget{ID: w.ID, Title: ptr("C")})
	must(err)
	_, err = svc.CopyWidget(ctx, "rest", CopyWidget{ID: w.ID, DashboardID: cp.ID})
	must(err)
	must(svc.ArchiveWidget(ctx, "mcp", w.ID))
	must(svc.RestoreWidget(ctx, "rest", w.ID))
	want := []string{
		"mcp dashboard.create", "rest dashboard.update", "mcp dashboard.duplicate",
		"rest dashboard.archive", "mcp dashboard.restore",
		"rest widget.add", "mcp widget.update", "rest widget.copy",
		"mcp widget.archive", "rest widget.restore",
	}
	if got := auditRows(t, svc); !reflect.DeepEqual(got, want) {
		t.Errorf("audit =\n%v\nwant\n%v", got, want)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// --- Reads and unknown ids ---

func TestSourceTypesSorted(t *testing.T) {
	if got := newTestService(t).SourceTypes(); !reflect.DeepEqual(got, []string{"md", "sql"}) {
		t.Errorf("SourceTypes = %v", got)
	}
}

func TestUnknownIDsAreNotFound(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	for name, op := range map[string]func() error{
		"dashboard": func() error { _, err := svc.Dashboard(ctx, 9999); return err },
		"widgets":   func() error { _, err := svc.Widgets(ctx, 9999, ""); return err },
		"duplicate": func() error { _, err := svc.DuplicateDashboard(ctx, "test", 9999); return err },
		"archive":   func() error { return svc.ArchiveDashboard(ctx, "test", 9999) },
		"add": func() error {
			_, err := svc.AddWidget(ctx, "test", AddWidget{DashboardID: 9999, WidgetSpec: note("A")})
			return err
		},
		"update widget":  func() error { _, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: 9999}); return err },
		"copy widget":    func() error { _, err := svc.CopyWidget(ctx, "test", CopyWidget{ID: 9999, DashboardID: 1}); return err },
		"archive widget": func() error { return svc.ArchiveWidget(ctx, "test", 9999) },
		"set view":       func() error { return svc.SetView(ctx, View{DashboardID: 9999}) },
	} {
		if err := op(); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
	_, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: " "})
	wantRefusal(t, err, store.ErrInvalid, "title must not be empty")
}
