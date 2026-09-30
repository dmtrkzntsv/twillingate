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
	sameName     bool // the rival also takes the widget's name
}

func (r *racingStore) InsertWidget(ctx context.Context, w store.Widget, a store.AuditEntry) (int64, error) {
	if r.races < r.limit {
		r.races++
		rival := w
		if !r.sameName {
			rival.Name = "rival-" + itoa(int64(r.races))
		}
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
	wantRefusal(t, err, store.ErrConflict, "dashboard "+itoa(d.ID)+" changed while placing this widget; try again")

	named := New(&racingStore{Store: base.st, limit: 1, sameName: true}, base.db, Options{})
	_, err = named.AddWidget(ctx, "test", AddWidget{DashboardID: d.ID, WidgetSpec: note("Taken")})
	wantRefusal(t, err, store.ErrConflict, "widget name taken is already used on this dashboard")
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
		"archive": func() error { return svc.ArchiveDashboard(ctx, "test", 3, false) },
		"restore": func() error { return svc.RestoreDashboard(ctx, "test", 3, false) },
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
	if _, err := svc.DuplicateDashboard(ctx, "test", 3, false); err != nil {
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

	cp, err := svc.DuplicateDashboard(ctx, "test", src.ID, false)
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
	// D10: a user source's copy joins the source's group right after it.
	if cp.GroupID != src.GroupID {
		t.Errorf("copy group_id = %d, want the source's %d", cp.GroupID, src.GroupID)
	}
	if got, want := sidebar(t, svc), []string{"Src/Src", "Src (copy)/Src", "Other/Other"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sidebar = %v, want %v", got, want)
	}

	// D10: a system source's copy is a new user group, last in the sidebar.
	sys, err := svc.DuplicateDashboard(ctx, "test", 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if sys.Owner != store.OwnerUser || sys.Title != "Users (copy)" || len(sys.Widgets) != 1 || sys.Range != "7d" {
		t.Errorf("system copy = %+v", sys)
	}
	list, _ := svc.Dashboards(ctx)
	if last := list.Dashboards[len(list.Dashboards)-1]; last.ID != sys.ID {
		t.Errorf("last dashboard = %d, want the system copy %d", last.ID, sys.ID)
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
	if err := svc.ArchiveDashboard(ctx, "test", d.ID, false); err != nil {
		t.Fatal(err)
	}
	msg := "dashboard " + itoa(d.ID) + " is archived; restore_dashboard first"
	_, err := svc.AddWidget(ctx, "test", AddWidget{DashboardID: d.ID, WidgetSpec: note("A")})
	wantRefusal(t, err, store.ErrInvalid, msg)
	_, err = svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: d.ID, Title: "E"})
	wantRefusal(t, err, store.ErrInvalid, msg)
	if err := svc.RestoreDashboard(ctx, "test", d.ID, false); err != nil {
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
	cp, err := svc.DuplicateDashboard(ctx, "mcp", d.ID, false)
	must(err)
	must(svc.ArchiveDashboard(ctx, "rest", cp.ID, false))
	must(svc.RestoreDashboard(ctx, "mcp", cp.ID, false))
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
		"duplicate": func() error { _, err := svc.DuplicateDashboard(ctx, "test", 9999, false); return err },
		"archive":   func() error { return svc.ArchiveDashboard(ctx, "test", 9999, false) },
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

// --- Review fixes ---

func TestUpdateWidgetRefusesClearingComponent(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	msg := "component must not be empty; list_components names the ones there are"
	d := mustCreate(t, svc, "D", note("A"))
	_, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: d.Widgets[0].ID, Component: ptr("")})
	wantRefusal(t, err, store.ErrInvalid, msg)
	if w, _ := svc.st.GetWidget(ctx, d.Widgets[0].ID); w.Component != "markdown" {
		t.Errorf("component = %q, want markdown kept", w.Component)
	}
	_, id := removedWidget(t, svc)
	_, err = svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Component: ptr(""), Width: ptr(4)})
	wantRefusal(t, err, store.ErrInvalid, msg)
}

func TestWidgetNamesTrimmedAndNotBlank(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	spec := note("A")
	spec.Name = "  spaced  "
	d := mustCreate(t, svc, "D", spec)
	if d.Widgets[0].Name != "spaced" {
		t.Errorf("name = %q, want trimmed", d.Widgets[0].Name)
	}
	blank := note("B")
	blank.Name = "   "
	_, err := svc.AddWidget(ctx, "test", AddWidget{DashboardID: d.ID, WidgetSpec: blank})
	wantRefusal(t, err, store.ErrInvalid, "name must not be blank")
	for _, name := range []string{"", "  "} {
		_, err = svc.UpdateWidget(ctx, "test", UpdateWidget{ID: d.Widgets[0].ID, Name: ptr(name)})
		wantRefusal(t, err, store.ErrInvalid, "name must not be blank")
	}
	w, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: d.Widgets[0].ID, Name: ptr(" renamed ")})
	if err != nil || w.Name != "renamed" {
		t.Errorf("rename = %q, %v; want renamed", w.Name, err)
	}
}

func TestUpdateDashboardNothingToUpdate(t *testing.T) {
	svc := newTestService(t)
	d := mustCreate(t, svc, "D")
	before := len(auditRows(t, svc))
	_, err := svc.UpdateDashboard(context.Background(), "test", UpdateDashboard{ID: d.ID})
	wantRefusal(t, err, store.ErrInvalid, "nothing to update; give title, after or group_id")
	if got := len(auditRows(t, svc)); got != before {
		t.Errorf("audit rows %d -> %d, want none written", before, got)
	}
}

func TestSetViewRefusesNegativeProject(t *testing.T) {
	svc := newTestService(t)
	d := switcherDashboard(t, svc, true, false)
	wantRefusal(t, svc.SetView(context.Background(), View{DashboardID: d.ID, ProjectID: -1}),
		store.ErrInvalid, "project_id must be a positive id")
}

// Every tool that takes after refuses one naming an archived dashboard,
// and moves or writes nothing (spec decision 8).
func TestAfterArchivedDashboardRefused(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	a := mustCreate(t, svc, "A")
	b := mustJoin(t, svc, "B", a.ID)
	c := mustJoin(t, svc, "C", a.ID)
	x := mustCreate(t, svc, "X")
	y := mustCreate(t, svc, "Y")
	if err := svc.ArchiveDashboard(ctx, "test", b.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.ArchiveDashboard(ctx, "test", y.ID, false); err != nil {
		t.Fatal(err)
	}
	before := userRows(t, svc)
	msg := func(id int64) string { return "after " + itoa(id) + " is archived; name a live dashboard" }

	_, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: c.ID, After: &b.ID})
	wantRefusal(t, err, store.ErrInvalid, msg(b.ID)) // a tab of its own group
	_, err = svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: x.ID, After: &b.ID})
	wantRefusal(t, err, store.ErrInvalid, msg(b.ID)) // another group's tab: a group move
	_, err = svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: x.ID, GroupID: &a.GroupID, After: &b.ID})
	wantRefusal(t, err, store.ErrInvalid, msg(b.ID)) // joining a group
	zero := int64(0)
	_, err = svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: c.ID, GroupID: &zero, After: &y.ID})
	wantRefusal(t, err, store.ErrInvalid, msg(y.ID)) // leaving a group
	_, err = svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "N", After: &y.ID})
	wantRefusal(t, err, store.ErrInvalid, msg(y.ID))
	_, err = svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "N", GroupID: a.GroupID, After: &b.ID})
	wantRefusal(t, err, store.ErrInvalid, msg(b.ID))

	if after := userRows(t, svc); !sameKeys(before, after) {
		t.Errorf("rows changed after refused placements:\n before %+v\n after  %+v", before, after)
	}
}

func TestUpdateDashboardAfterItselfOrFirstStays(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	first := mustCreate(t, svc, "First")
	second := mustCreate(t, svc, "Second")
	key := func(id int64) string {
		t.Helper()
		d, err := svc.st.GetDashboard(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return d.SortKey
	}
	before := key(second.ID)
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: second.ID, After: &second.ID}); err != nil {
		t.Fatal(err)
	}
	if got := key(second.ID); got != before {
		t.Errorf("after itself: key %q became %q", before, got)
	}
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: first.ID, After: ptr(int64(0))}); err != nil {
		t.Fatal(err)
	}
	list, _ := svc.Dashboards(ctx)
	if len(list.Dashboards) != 2 || list.Dashboards[0].ID != first.ID || list.Dashboards[1].ID != second.ID {
		t.Errorf("order = %+v, want First, Second", list.Dashboards)
	}
}

func TestCopyWidgetIntoArchivedDashboard(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	src := mustCreate(t, svc, "Src", note("A"))
	dst := mustCreate(t, svc, "Dst")
	if err := svc.ArchiveDashboard(ctx, "test", dst.ID, false); err != nil {
		t.Fatal(err)
	}
	_, err := svc.CopyWidget(ctx, "test", CopyWidget{ID: src.Widgets[0].ID, DashboardID: dst.ID})
	wantRefusal(t, err, store.ErrInvalid, "dashboard "+itoa(dst.ID)+" is archived; restore_dashboard first")
}

// --- Groups ---

// mustJoin creates a user dashboard titled title as the last tab of
// group g.
func mustJoin(t *testing.T, svc *Service, title string, g int64) DashboardDetail {
	t.Helper()
	d, err := svc.CreateDashboard(context.Background(), "test", CreateDashboard{Title: title, GroupID: g})
	if err != nil {
		t.Fatalf("CreateDashboard(%q, group %d): %v", title, g, err)
	}
	return d
}

// userRows is the user dashboard rows in sidebar order, archived ones
// included, straight from the store.
func userRows(t *testing.T, svc *Service) []store.Dashboard {
	t.Helper()
	ds, err := svc.st.ListDashboards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return userOrder(ds)
}

// sidebar is the user dashboards' titles in sidebar order, archived
// ones included, each as "title/group title" where group title is the
// title of the dashboard whose id is the group's id ("" for a reserved
// group id) — enough to read both the order and the grouping at a
// glance.
func sidebar(t *testing.T, svc *Service) []string {
	t.Helper()
	rows := userRows(t, svc)
	titles := map[int64]string{}
	for _, d := range rows {
		titles[d.ID] = d.Title
	}
	out := make([]string, len(rows))
	for i, d := range rows {
		out[i] = d.Title + "/" + titles[d.GroupID]
	}
	return out
}

// wantContiguous fails unless every group's rows are adjacent in the
// user order, archived ones included (spec decision 3).
func wantContiguous(t *testing.T, svc *Service) {
	t.Helper()
	done := map[int64]bool{}
	var cur int64
	for _, d := range userRows(t, svc) {
		if d.GroupID == cur {
			continue
		}
		if done[d.GroupID] {
			t.Errorf("group %d is split: %v", d.GroupID, sidebar(t, svc))
			return
		}
		done[cur] = true
		cur = d.GroupID
	}
}

// tabIDs is the ids of get_dashboard(id).Tabs.
func tabIDs(t *testing.T, svc *Service, id int64) []int64 {
	t.Helper()
	d, err := svc.Dashboard(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Tabs == nil {
		t.Fatalf("dashboard %d: Tabs is nil, want non-nil", id)
	}
	out := []int64{}
	for _, tab := range d.Tabs {
		out = append(out, tab.ID)
	}
	return out
}

func TestDashboardGroupAndTabs(t *testing.T) {
	ctx := context.Background()

	t.Run("a new dashboard is its own group of one", func(t *testing.T) {
		svc := newTestService(t)
		d := mustCreate(t, svc, "D")
		if d.GroupID != d.ID {
			t.Errorf("group_id = %d, want its own id %d", d.GroupID, d.ID)
		}
		if len(d.Tabs) != 1 || d.Tabs[0] != (Tab{ID: d.ID, Title: "D"}) {
			t.Errorf("tabs = %+v, want just itself", d.Tabs)
		}
		list, err := svc.Dashboards(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if list.Dashboards[0].GroupID != d.ID {
			t.Errorf("list group_id = %d, want %d", list.Dashboards[0].GroupID, d.ID)
		}
	})

	t.Run("every member answers the same live tabs", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "A")
		b := mustJoin(t, svc, "B", a.ID)
		c := mustJoin(t, svc, "C", a.ID)
		mustCreate(t, svc, "Other")
		if err := svc.ArchiveDashboard(ctx, "test", b.ID, false); err != nil {
			t.Fatal(err)
		}
		want := []int64{a.ID, c.ID}
		for _, id := range []int64{a.ID, b.ID, c.ID} {
			if got := tabIDs(t, svc, id); !reflect.DeepEqual(got, want) {
				t.Errorf("tabs from %d = %v, want %v", id, got, want)
			}
		}
	})

	t.Run("a group with no live member answers no tabs", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "A")
		if err := svc.ArchiveDashboard(ctx, "test", a.ID, false); err != nil {
			t.Fatal(err)
		}
		if got := tabIDs(t, svc, a.ID); len(got) != 0 {
			t.Errorf("tabs = %v, want none", got)
		}
	})
}

func TestCreateDashboardInGroup(t *testing.T) {
	ctx := context.Background()

	t.Run("group_id alone makes it the last tab", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "A")
		mustCreate(t, svc, "Other")
		b := mustJoin(t, svc, "B", a.ID)
		c := mustJoin(t, svc, "C", a.ID)
		if b.GroupID != a.ID || c.GroupID != a.ID {
			t.Errorf("group ids = %d, %d, want %d", b.GroupID, c.GroupID, a.ID)
		}
		if got, want := sidebar(t, svc), []string{"A/A", "B/A", "C/A", "Other/Other"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
		if got, want := c.Tabs, []Tab{{a.ID, "A"}, {b.ID, "B"}, {c.ID, "C"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("tabs = %+v, want %+v", got, want)
		}
	})

	t.Run("after 0 makes it the first tab", func(t *testing.T) {
		svc := newTestService(t)
		mustCreate(t, svc, "Before")
		a := mustCreate(t, svc, "A")
		mustJoin(t, svc, "B", a.ID)
		if _, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "Z", GroupID: a.ID, After: ptr(int64(0))}); err != nil {
			t.Fatal(err)
		}
		if got, want := sidebar(t, svc), []string{"Before/Before", "Z/A", "A/A", "B/A"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
	})

	t.Run("after a member places it right after that tab", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "A")
		mustJoin(t, svc, "B", a.ID)
		if _, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "Z", GroupID: a.ID, After: &a.ID}); err != nil {
			t.Fatal(err)
		}
		if got, want := sidebar(t, svc), []string{"A/A", "Z/A", "B/A"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
	})

	t.Run("after a non-member is refused", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "A")
		other := mustCreate(t, svc, "Other")
		_, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "Z", GroupID: a.ID, After: &other.ID})
		wantRefusal(t, err, store.ErrInvalid, "after "+itoa(other.ID)+" is not a member of group "+itoa(a.ID))
	})

	t.Run("a system, unknown or all-archived group is refused", func(t *testing.T) {
		svc := newTestService(t)
		syncReporting(t, svc, nil, systemDashboard())
		gone := mustCreate(t, svc, "Gone")
		if err := svc.ArchiveDashboard(ctx, "test", gone.ID, false); err != nil {
			t.Fatal(err)
		}
		for _, g := range []int64{3, 9999, gone.ID} {
			_, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "Z", GroupID: g})
			wantRefusal(t, err, store.ErrInvalid, "group "+itoa(g)+" has no live user dashboard")
		}
		if got := len(userRows(t, svc)); got != 1 {
			t.Errorf("user rows = %d, want 1 (nothing created)", got)
		}
	})

	t.Run("without group_id, after a tab lands after the whole group", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "A")
		mustJoin(t, svc, "B", a.ID)
		mustCreate(t, svc, "Other")
		if _, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "Z", After: &a.ID}); err != nil {
			t.Fatal(err)
		}
		if got, want := sidebar(t, svc), []string{"A/A", "B/A", "Z/Z", "Other/Other"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
	})
}

func TestUpdateDashboardPlacesInGroups(t *testing.T) {
	ctx := context.Background()
	update := func(t *testing.T, svc *Service, in UpdateDashboard) DashboardInfo {
		t.Helper()
		d, err := svc.UpdateDashboard(ctx, "test", in)
		if err != nil {
			t.Fatalf("UpdateDashboard(%+v): %v", in, err)
		}
		return d
	}

	t.Run("after a member reorders the tabs only", func(t *testing.T) {
		svc := newTestService(t)
		mustCreate(t, svc, "X")
		a := mustCreate(t, svc, "A")
		b := mustJoin(t, svc, "B", a.ID)
		c := mustJoin(t, svc, "C", a.ID)
		mustCreate(t, svc, "Y")
		update(t, svc, UpdateDashboard{ID: c.ID, After: &a.ID})
		if got, want := sidebar(t, svc), []string{"X/X", "A/A", "C/A", "B/A", "Y/Y"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
		if got, want := tabIDs(t, svc, b.ID), []int64{a.ID, c.ID, b.ID}; !reflect.DeepEqual(got, want) {
			t.Errorf("tabs = %v, want %v", got, want)
		}
	})

	t.Run("after another group's member moves the whole group, archived members too", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "A")
		b := mustJoin(t, svc, "B", a.ID)
		c := mustJoin(t, svc, "C", a.ID)
		mustCreate(t, svc, "Solo")
		x := mustCreate(t, svc, "X")
		mustJoin(t, svc, "X2", x.ID)
		mustCreate(t, svc, "Last")
		if err := svc.ArchiveDashboard(ctx, "test", b.ID, false); err != nil {
			t.Fatal(err)
		}
		got := update(t, svc, UpdateDashboard{ID: c.ID, After: &x.ID, Title: "C2"})
		if got.Title != "C2" || got.GroupID != a.ID {
			t.Errorf("answer = %+v, want title C2 in group %d", got, a.ID)
		}
		if got, want := sidebar(t, svc), []string{"Solo/Solo", "X/X", "X2/X", "A/A", "B/A", "C2/A", "Last/Last"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
		wantContiguous(t, svc)
	})

	t.Run("after 0 moves a tab's whole group to the top", func(t *testing.T) {
		svc := newTestService(t)
		mustCreate(t, svc, "X")
		a := mustCreate(t, svc, "A")
		b := mustJoin(t, svc, "B", a.ID)
		update(t, svc, UpdateDashboard{ID: b.ID, After: ptr(int64(0))})
		if got, want := sidebar(t, svc), []string{"A/A", "B/A", "X/X"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
	})

	t.Run("after itself changes nothing", func(t *testing.T) {
		svc := newTestService(t)
		mustCreate(t, svc, "X")
		a := mustCreate(t, svc, "A")
		b := mustJoin(t, svc, "B", a.ID)
		mustCreate(t, svc, "Y")
		before := userRows(t, svc)
		for _, in := range []UpdateDashboard{
			{ID: b.ID, After: &b.ID},
			{ID: b.ID, GroupID: &a.ID, After: &b.ID},
		} {
			update(t, svc, in)
			if got := userRows(t, svc); !sameKeys(got, before) {
				t.Errorf("%+v: rows changed:\n%+v\nwas\n%+v", in, got, before)
			}
		}
	})

	t.Run("after the group already before it changes nothing", func(t *testing.T) {
		svc := newTestService(t)
		x := mustCreate(t, svc, "X")
		x2 := mustJoin(t, svc, "X2", x.ID)
		a := mustCreate(t, svc, "A")
		mustJoin(t, svc, "B", a.ID)
		before := userRows(t, svc)
		for _, after := range []int64{x.ID, x2.ID} {
			update(t, svc, UpdateDashboard{ID: a.ID, After: &after})
			if got := userRows(t, svc); !sameKeys(got, before) {
				t.Errorf("after %d: rows changed:\n%+v\nwas\n%+v", after, got, before)
			}
		}
	})

	t.Run("group_id own with after 0 makes it the first tab, alone the last", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "A")
		b := mustJoin(t, svc, "B", a.ID)
		c := mustJoin(t, svc, "C", a.ID)
		update(t, svc, UpdateDashboard{ID: c.ID, GroupID: &a.ID, After: ptr(int64(0))})
		if got, want := tabIDs(t, svc, a.ID), []int64{c.ID, a.ID, b.ID}; !reflect.DeepEqual(got, want) {
			t.Errorf("after 0: tabs = %v, want %v", got, want)
		}
		update(t, svc, UpdateDashboard{ID: c.ID, GroupID: &a.ID})
		if got, want := tabIDs(t, svc, a.ID), []int64{a.ID, b.ID, c.ID}; !reflect.DeepEqual(got, want) {
			t.Errorf("no after: tabs = %v, want %v", got, want)
		}
		wantContiguous(t, svc)
	})

	t.Run("group_id of another group moves it there as a tab", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "A")
		b := mustJoin(t, svc, "B", a.ID)
		x := mustCreate(t, svc, "X")
		mustJoin(t, svc, "X2", x.ID)
		got := update(t, svc, UpdateDashboard{ID: b.ID, GroupID: &x.ID, After: &x.ID})
		if got.GroupID != x.ID {
			t.Errorf("group_id = %d, want %d", got.GroupID, x.ID)
		}
		if got, want := sidebar(t, svc), []string{"A/A", "X/X", "B/X", "X2/X"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
		_, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: a.ID, GroupID: &x.ID, After: &a.ID})
		wantRefusal(t, err, store.ErrInvalid, "after "+itoa(a.ID)+" is not a member of group "+itoa(x.ID))
	})

	t.Run("group_id 0 leaves as a group of one right after the old group", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "A")
		b := mustJoin(t, svc, "B", a.ID)
		mustJoin(t, svc, "C", a.ID)
		mustCreate(t, svc, "Y")
		got := update(t, svc, UpdateDashboard{ID: b.ID, GroupID: ptr(int64(0))})
		if got.GroupID == a.ID || got.GroupID == b.ID {
			t.Errorf("group_id = %d, want a fresh one (not %d, not %d)", got.GroupID, a.ID, b.ID)
		}
		// "B/": its group id is a reserved number, no dashboard's id.
		if got, want := sidebar(t, svc), []string{"A/A", "C/A", "B/", "Y/Y"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
	})

	t.Run("group_id 0 with after places the new group in the sidebar", func(t *testing.T) {
		svc := newTestService(t)
		y := mustCreate(t, svc, "Y")
		a := mustCreate(t, svc, "A")
		b := mustJoin(t, svc, "B", a.ID)
		update(t, svc, UpdateDashboard{ID: b.ID, GroupID: ptr(int64(0)), After: ptr(int64(0))})
		if got, want := sidebar(t, svc), []string{"B/", "Y/Y", "A/A"}; !reflect.DeepEqual(got, want) {
			t.Errorf("after 0: sidebar = %v, want %v", got, want)
		}
		update(t, svc, UpdateDashboard{ID: b.ID, GroupID: ptr(int64(0)), After: &y.ID})
		if got, want := sidebar(t, svc), []string{"Y/Y", "B/", "A/A"}; !reflect.DeepEqual(got, want) {
			t.Errorf("after Y: sidebar = %v, want %v", got, want)
		}
	})

	t.Run("group_id 0 alone in its group changes only the title", func(t *testing.T) {
		svc := newTestService(t)
		mustCreate(t, svc, "X")
		a := mustCreate(t, svc, "A")
		mustCreate(t, svc, "Y")
		before := userRows(t, svc)
		got := update(t, svc, UpdateDashboard{ID: a.ID, GroupID: ptr(int64(0)), Title: "A2"})
		if got.Title != "A2" {
			t.Errorf("title = %q, want A2", got.Title)
		}
		update(t, svc, UpdateDashboard{ID: a.ID, GroupID: ptr(int64(0)), After: &a.ID})
		if got := userRows(t, svc); !sameKeys(got, before) {
			t.Errorf("rows moved:\n%+v\nwas\n%+v", got, before)
		}
	})

	t.Run("group_id 0 on a group's first dashboard gets a fresh group id", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "A")
		b := mustJoin(t, svc, "B", a.ID)
		mustCreate(t, svc, "Y")
		got := update(t, svc, UpdateDashboard{ID: a.ID, GroupID: ptr(int64(0))})
		if got.GroupID == a.ID || got.GroupID == b.ID {
			t.Errorf("group_id = %d, want a fresh one (not %d, not %d)", got.GroupID, a.ID, b.ID)
		}
		rows := userRows(t, svc)
		var order []string
		for _, d := range rows {
			order = append(order, d.Title)
			if d.ID == b.ID && d.GroupID != a.ID {
				t.Errorf("B group_id = %d, want the old group's %d", d.GroupID, a.ID)
			}
		}
		if want := []string{"B", "A", "Y"}; !reflect.DeepEqual(order, want) {
			t.Errorf("order = %v, want %v", order, want)
		}
		if got, want := tabIDs(t, svc, b.ID), []int64{b.ID}; !reflect.DeepEqual(got, want) {
			t.Errorf("old group's tabs = %v, want %v", got, want)
		}
		if got, want := tabIDs(t, svc, a.ID), []int64{a.ID}; !reflect.DeepEqual(got, want) {
			t.Errorf("new group's tabs = %v, want %v", got, want)
		}
		// Having left, it can come back without merging into anything.
		update(t, svc, UpdateDashboard{ID: a.ID, GroupID: &a.ID, After: ptr(int64(0))})
		if got, want := tabIDs(t, svc, b.ID), []int64{a.ID, b.ID}; !reflect.DeepEqual(got, want) {
			t.Errorf("rejoined tabs = %v, want %v", got, want)
		}
		wantContiguous(t, svc)
	})

	t.Run("group_id 0 alone keeps its group id", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "A")
		b := mustJoin(t, svc, "B", a.ID)
		y := mustCreate(t, svc, "Y")
		update(t, svc, UpdateDashboard{ID: a.ID, GroupID: &y.ID}) // group A.ID is B alone
		before := userRows(t, svc)
		if got := update(t, svc, UpdateDashboard{ID: b.ID, GroupID: ptr(int64(0))}); got.GroupID != a.ID {
			t.Errorf("group_id = %d, want the one it had, %d", got.GroupID, a.ID)
		}
		if got := userRows(t, svc); !sameKeys(got, before) {
			t.Errorf("rows moved:\n%+v\nwas\n%+v", got, before)
		}
		if got := update(t, svc, UpdateDashboard{ID: b.ID, GroupID: ptr(int64(0)), After: &y.ID}); got.GroupID != a.ID {
			t.Errorf("with after: group_id = %d, want %d", got.GroupID, a.ID)
		}
		if got, want := sidebar(t, svc), []string{"Y/Y", "A/Y", "B/A"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
	})

	t.Run("a system dashboard or group is refused and nothing moves", func(t *testing.T) {
		svc := newTestService(t)
		syncReporting(t, svc, nil, systemDashboard())
		mustCreate(t, svc, "X")
		a := mustCreate(t, svc, "A")
		before := userRows(t, svc)
		_, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: a.ID, After: ptr(int64(3))})
		wantRefusal(t, err, store.ErrInvalid, "after 3 is not a user dashboard")
		_, err = svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: a.ID, GroupID: ptr(int64(3))})
		wantRefusal(t, err, store.ErrInvalid, "group 3 has no live user dashboard")
		if got := userRows(t, svc); !sameKeys(got, before) {
			t.Errorf("rows moved:\n%+v\nwas\n%+v", got, before)
		}
		sys, err := svc.st.GetDashboard(ctx, 3)
		if err != nil {
			t.Fatal(err)
		}
		if sys.SortKey != "a0" || sys.GroupID != 3 {
			t.Errorf("system row = %+v, want untouched", sys)
		}
	})
}

// sameKeys reports whether a and b hold the same rows in the same
// order with the same group ids and sort keys.
func sameKeys(a, b []store.Dashboard) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].GroupID != b[i].GroupID || a[i].SortKey != b[i].SortKey {
			return false
		}
	}
	return true
}

// --- Whole-group duplicate ---

func TestDuplicateWholeGroupFromSystem(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemGroup()...)
	ctx := context.Background()

	// Users' one widget is archived directly at the store (the service
	// refuses archiving a system widget): proves it is not copied.
	usersWidget := widgetRows(t, svc, 12)[0].ID
	if err := svc.st.SetWidgetArchived(ctx, usersWidget, true, store.AuditEntry{Actor: "test", Action: "widget.archive"}); err != nil {
		t.Fatal(err)
	}

	cp, err := svc.DuplicateDashboard(ctx, "test", 12, true) // from Users, a middle member
	if err != nil {
		t.Fatal(err)
	}
	if cp.Title != "Views (copy)" || cp.Owner != store.OwnerUser {
		t.Errorf("first copy = %q owned by %q, want \"Views (copy)\" owned by user", cp.Title, cp.Owner)
	}
	var titles []string
	for _, tab := range cp.Tabs {
		titles = append(titles, tab.Title)
	}
	if want := []string{"Views (copy)", "Product", "Users", "Groups", "Retention"}; !reflect.DeepEqual(titles, want) {
		t.Errorf("tab titles = %v, want %v (same order, only the first retitled)", titles, want)
	}

	usersCopy, err := svc.Dashboard(ctx, cp.Tabs[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(usersCopy.Widgets) != 0 {
		t.Errorf("Users copy widgets = %d, want 0 (its only widget was archived)", len(usersCopy.Widgets))
	}
	productCopy, err := svc.Dashboard(ctx, cp.Tabs[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(productCopy.Widgets) != 1 {
		t.Errorf("Product copy widgets = %d, want 1 (its live widget)", len(productCopy.Widgets))
	}
}

func TestDuplicateWholeGroupArchivedMemberNotCopied(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	a := mustCreate(t, svc, "A")
	b := mustJoin(t, svc, "B", a.ID)
	mustJoin(t, svc, "C", a.ID)
	if err := svc.ArchiveDashboard(ctx, "test", b.ID, false); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, svc, "Z")

	cp, err := svc.DuplicateDashboard(ctx, "test", a.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, tab := range cp.Tabs {
		titles = append(titles, tab.Title)
	}
	if want := []string{"A (copy)", "C"}; !reflect.DeepEqual(titles, want) {
		t.Errorf("tab titles = %v, want %v (B, archived, not copied)", titles, want)
	}
	rows := userRows(t, svc)
	if last := rows[len(rows)-1]; last.GroupID != cp.GroupID {
		t.Errorf("last dashboard's group = %d, want the copy's group %d (new group placed last)", last.GroupID, cp.GroupID)
	}
}

// An archived dashboard is not duplicated, alone or with its group: the
// copy would otherwise revive a group whose dashboards are all archived.
func TestDuplicateArchivedDashboardRefused(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	a := mustCreate(t, svc, "A")
	b := mustJoin(t, svc, "B", a.ID)
	if err := svc.ArchiveDashboard(ctx, "test", a.ID, true); err != nil {
		t.Fatal(err)
	}
	before := userRows(t, svc)

	for _, whole := range []bool{false, true} {
		_, err := svc.DuplicateDashboard(ctx, "test", b.ID, whole)
		wantRefusal(t, err, store.ErrInvalid, "dashboard "+itoa(b.ID)+" is archived; restore_dashboard first")
	}
	if after := userRows(t, svc); len(after) != len(before) {
		t.Errorf("dashboards after refused duplicates = %d, want %d (nothing written)", len(after), len(before))
	}
}

// --- Archiving and restoring ---

func TestArchiveDashboardKeepsOtherTabs(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	a := mustCreate(t, svc, "A")
	b := mustJoin(t, svc, "B", a.ID)
	c := mustJoin(t, svc, "C", a.ID)

	if err := svc.ArchiveDashboard(ctx, "test", b.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, want := tabIDs(t, svc, a.ID), []int64{a.ID, c.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("tabs after archiving B (a middle tab) = %v, want %v", got, want)
	}

	if err := svc.ArchiveDashboard(ctx, "test", a.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, want := tabIDs(t, svc, c.ID), []int64{c.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("tabs after archiving A (the first tab) = %v, want %v (C becomes first)", got, want)
	}
}

func TestArchiveOnlyLiveMemberThenRestoreSamePosition(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	mustCreate(t, svc, "X")
	a := mustCreate(t, svc, "A")
	mustCreate(t, svc, "Y")
	before := userRows(t, svc)

	if err := svc.ArchiveDashboard(ctx, "test", a.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreDashboard(ctx, "test", a.ID, false); err != nil {
		t.Fatal(err)
	}
	if got := userRows(t, svc); !sameKeys(got, before) {
		t.Errorf("rows after archive then restore:\n%+v\nwant unchanged (same sidebar position):\n%+v", got, before)
	}
}

func TestArchiveRestoreWholeGroup(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	a := mustCreate(t, svc, "A")
	b := mustJoin(t, svc, "B", a.ID)
	c := mustJoin(t, svc, "C", a.ID)
	// C was archived on its own, before the whole-group archive.
	if err := svc.ArchiveDashboard(ctx, "test", c.ID, false); err != nil {
		t.Fatal(err)
	}

	if err := svc.ArchiveDashboard(ctx, "test", a.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a.ID, b.ID, c.ID} {
		d, err := svc.st.GetDashboard(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.ArchivedAt == "" {
			t.Errorf("dashboard %d live after archive whole_group, want archived", id)
		}
	}
	// Every member is now archived: archiving the whole group again is a
	// no-op, as the single archive is idempotent.
	if err := svc.ArchiveDashboard(ctx, "test", b.ID, true); err != nil {
		t.Errorf("archive whole_group with none live: %v", err)
	}

	if err := svc.RestoreDashboard(ctx, "test", a.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a.ID, b.ID, c.ID} {
		d, err := svc.st.GetDashboard(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.ArchivedAt != "" {
			t.Errorf("dashboard %d still archived after restore whole_group, want live (C included, archived earlier on its own)", id)
		}
	}
}

func TestArchiveRestoreWholeGroupRefusesSystem(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemDashboard())
	ctx := context.Background()
	wantRefusal(t, svc.ArchiveDashboard(ctx, "test", 3, true), store.ErrInvalid, systemRefusal)
	wantRefusal(t, svc.RestoreDashboard(ctx, "test", 3, true), store.ErrInvalid, systemRefusal)
}
