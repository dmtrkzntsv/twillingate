package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

const systemRefusal = "dashboard 3 is a system dashboard and changes only with a release; duplicate_dashboard makes an editable copy"

// --- Placement ---

func TestPlaceAfterNilFirstAndID(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "DD")
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
	if want := []string{"Zero", "DD", "One", "OneAndHalf", "Two"}; !reflect.DeepEqual(titles, want) {
		t.Errorf("dashboards = %v, want %v", titles, want)
	}
}

func TestPlaceKeysBetweenFullOrder(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "DD", note("A"), note("B"), note("C"))
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

// add_widget and copy_widget refuse an after naming an archived widget,
// and write nothing.
func TestPlaceWidgetAfterArchivedRefused(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "DD", note("A"), note("B"))
	src := mustCreate(t, svc, "Src", note("S"))
	b := d.Widgets[1].ID
	if err := svc.ArchiveWidget(ctx, "test", b); err != nil {
		t.Fatal(err)
	}
	msg := "after " + itoa(b) + " is archived; name a live widget"

	_, err := svc.AddWidget(ctx, "test", AddWidget{DashboardID: d.ID, After: &b, WidgetSpec: note("X")})
	wantRefusal(t, err, store.ErrInvalid, msg)
	_, err = svc.CopyWidget(ctx, "test", CopyWidget{ID: src.Widgets[0].ID, DashboardID: d.ID, After: &b})
	wantRefusal(t, err, store.ErrInvalid, msg)
	if got := widgetRows(t, svc, d.ID); len(got) != 2 {
		t.Errorf("widgets on D = %d, want 2 (nothing written)", len(got))
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
	_, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "XX", After: ptr(int64(3))})
	wantRefusal(t, err, store.ErrInvalid, "after 3 is not a user dashboard")
	d := mustCreate(t, svc, "DD")
	_, err = svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: d.ID, After: ptr(int64(9999))})
	wantRefusal(t, err, store.ErrInvalid, "after 9999 is not a user dashboard")
}

func TestPlaceInsertWritesOneRow(t *testing.T) {
	svc := newTestService(t)
	d := mustCreate(t, svc, "DD", note("A"), note("B"))
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

	d2 := mustCreate(t, svc, "EE")
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
	d := mustCreate(t, base, "DD", note("A"))

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
	_, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "DD", Widgets: []WidgetSpec{
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
	d := mustCreate(t, svc, "DD")
	if d.Range != "7d" {
		t.Errorf("range = %q, want 7d", d.Range)
	}
	d2, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "EE", Range: "90d"})
	if err != nil || d2.Range != "90d" {
		t.Errorf("range = %q, %v; want 90d", d2.Range, err)
	}
	_, err = svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "FF", Range: "14d"})
	wantRefusal(t, err, store.ErrInvalid, "range must be one of today, yesterday, 7d, 30d, 90d, custom")
	_, err = svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "GG", Range: "custom"})
	wantRefusal(t, err, store.ErrInvalid, "create with a preset; the viewer picks custom dates")
}

func TestCreateDashboardWidgetNames(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	named := note("Other")
	named.Name = "visitors-2"
	d := mustCreate(t, svc, "DD", note("Visitors"), named, note("Visitors"), note(""))
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
	_, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "EE", Widgets: []WidgetSpec{first, dup}})
	wantRefusal(t, err, store.ErrConflict, "widget name a is already used on this dashboard")
}

func TestCreateDashboardKeysSpread(t *testing.T) {
	svc := newTestService(t)
	d := mustCreate(t, svc, "DD", note("A"), note("B"), note("C"))
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
	d := mustCreate(t, svc, "DD", note("Default"), sized)
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
	const builtin = "dashboard 3 is a built-in dashboard and is never archived; update_dashboard {sidebar: false} takes its group out of the sidebar"
	wantRefusal(t, svc.ArchiveDashboard(ctx, "test", 3, false), store.ErrInvalid, builtin)
	wantRefusal(t, svc.RestoreDashboard(ctx, "test", 3, false), store.ErrInvalid, builtin)
	for name, op := range map[string]func() error{
		"update": func() error {
			_, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: 3, Title: "XX"})
			return err
		},
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
	if _, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: 3}); err != nil {
		t.Errorf("duplicate: %v", err)
	}
	d := mustCreate(t, svc, "DD")
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
	other := mustCreate(t, svc, "Other")
	if err := svc.ArchiveWidget(ctx, "test", src.Widgets[2].ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.st.SetDashboardView(ctx, store.Dashboard{ID: src.ID, LastProjectID: 7, LastRange: "custom", LastFrom: "2026-08-01", LastTo: "2026-08-31"}); err != nil {
		t.Fatal(err)
	}

	cp, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: src.ID, GroupID: src.GroupID})
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
	// With its own group_id, a copy joins that group right after the source.
	if cp.GroupID != src.GroupID {
		t.Errorf("copy group_id = %d, want the source's %d", cp.GroupID, src.GroupID)
	}
	if got, want := sidebar(t, svc), []string{"Src/Src", "Src (copy)/Src", "Other/Other"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sidebar = %v, want %v", got, want)
	}

	// Without group_id, a copy is a new group of one, last in the sidebar,
	// and the source's group keeps its tabs.
	own, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: src.ID})
	if err != nil {
		t.Fatal(err)
	}
	if own.GroupID != own.ID || own.Title != "Src (copy)" || len(own.Widgets) != 2 || len(own.Tabs) != 1 {
		t.Errorf("own copy = group %d (id %d) %q, %d widgets, %d tabs; want its own group of one", own.GroupID, own.ID, own.Title, len(own.Widgets), len(own.Tabs))
	}
	if got, want := sidebar(t, svc), []string{"Src/Src", "Src (copy)/Src", "Other/Other", "Src (copy)/Src (copy)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sidebar = %v, want %v", got, want)
	}

	// With another group's group_id, a copy is that group's last tab.
	into, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: src.ID, GroupID: other.GroupID})
	if err != nil {
		t.Fatal(err)
	}
	if into.GroupID != other.GroupID {
		t.Errorf("copy group_id = %d, want Other's %d", into.GroupID, other.GroupID)
	}
	if got, want := sidebar(t, svc), []string{"Src/Src", "Src (copy)/Src", "Other/Other", "Src (copy)/Other", "Src (copy)/Src (copy)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sidebar = %v, want %v", got, want)
	}

	// A group with no live user dashboard is refused, a system one included,
	// and so is group_id with whole_group.
	_, err = svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: src.ID, GroupID: 1})
	wantRefusal(t, err, store.ErrInvalid, "group 1 has no live user dashboard")
	_, err = svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: src.ID, WholeGroup: true, GroupID: src.GroupID})
	wantRefusal(t, err, store.ErrInvalid, "whole_group copies the group as a new dashboard; drop group_id")

	// A system source's copy, likewise, is a new user group, last in the sidebar.
	sys, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: 3})
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
	d := mustCreate(t, svc, "DD", note("A"))
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
	d := mustCreate(t, svc, "DD")
	if err := svc.ArchiveDashboard(ctx, "test", d.ID, false); err != nil {
		t.Fatal(err)
	}
	msg := "dashboard " + itoa(d.ID) + " is archived; restore_dashboard first"
	_, err := svc.AddWidget(ctx, "test", AddWidget{DashboardID: d.ID, WidgetSpec: note("A")})
	wantRefusal(t, err, store.ErrInvalid, msg)
	_, err = svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: d.ID, Title: "EE"})
	wantRefusal(t, err, store.ErrInvalid, msg)
	if err := svc.RestoreDashboard(ctx, "test", d.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: d.ID, Title: "EE"}); err != nil {
		t.Errorf("update after restore: %v", err)
	}
}

func TestRestoreWidgetKeepsKey(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "DD", note("A"), note("B"), note("C"))
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
	d := mustCreate(t, svc, "DD", note("A"), note("B"))
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
	d := mustCreate(t, svc, "DD", WidgetSpec{
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
	d := mustCreate(t, svc, "DD", note("A"), note("B"))
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

// update_widget's after moves a widget among its dashboard's live ones,
// the archived one between them keeping its key.
func TestUpdateWidgetMoves(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "DD", note("A"), note("B"), note("C"), note("D"))
	other := mustCreate(t, svc, "Other", note("Z"))
	a, b, c, dd := d.Widgets[0].ID, d.Widgets[1].ID, d.Widgets[2].ID, d.Widgets[3].ID
	if err := svc.ArchiveWidget(ctx, "test", b); err != nil {
		t.Fatal(err)
	}
	move := func(id, after int64) error {
		_, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, After: &after})
		return err
	}

	if err := move(dd, 0); err != nil {
		t.Fatal(err)
	}
	if got, want := liveNames(t, svc, d.ID), []string{"d", "a", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("d first: %v, want %v", got, want)
	}
	if err := move(dd, c); err != nil {
		t.Fatal(err)
	}
	if got, want := liveNames(t, svc, d.ID), []string{"a", "c", "d"}; !reflect.DeepEqual(got, want) {
		t.Errorf("d after c: %v, want %v", got, want)
	}
	// Moved and resized in one call.
	w, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: a, After: &c, Width: ptr(4), Height: ptr(3)})
	if err != nil {
		t.Fatal(err)
	}
	if w.Width != 4 || w.Height != 3 {
		t.Errorf("size = %dx%d, want 4x3", w.Width, w.Height)
	}
	if got, want := liveNames(t, svc, d.ID), []string{"c", "a", "d"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a after c: %v, want %v", got, want)
	}

	if err := move(a, a); err != nil {
		t.Errorf("after itself: %v, want a no-op", err)
	}
	wantRefusal(t, move(a, b), store.ErrInvalid, "after "+itoa(b)+" is archived; name a live widget")
	z := other.Widgets[0].ID
	wantRefusal(t, move(a, z), store.ErrInvalid, "after "+itoa(z)+" is not a widget on dashboard "+itoa(d.ID))
	if got, want := liveNames(t, svc, d.ID), []string{"c", "a", "d"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after refusals: %v, want %v", got, want)
	}
}

// A change of only the size or the place runs no query: a widget whose
// SQL no longer runs can still be moved and resized, though any other
// change to it is refused.
func TestUpdateWidgetLayoutRunsNoQuery(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "DD", note("A"), WidgetSpec{Component: "table", Title: "T", Source: Source{Type: "sql", Content: "SELECT 1 AS n"}})
	id := d.Widgets[1].ID
	w, err := svc.st.GetWidget(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	w.Source = "SELECT n FROM nowhere"
	if err := svc.st.UpdateWidget(ctx, w, store.WidgetColumns{Content: true}, store.AuditEntry{Actor: "test", Action: "test.break"}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Width: ptr(6), Height: ptr(5), After: ptr(int64(0))}); err != nil {
		t.Errorf("layout change of a broken widget: %v", err)
	}
	if got, want := liveNames(t, svc, d.ID), []string{"t", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	if _, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Title: ptr("New")}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("title change of a broken widget: err = %v, want ErrInvalid", err)
	}
	_, err = svc.UpdateWidget(ctx, "test", UpdateWidget{ID: id, Width: ptr(0)})
	wantRefusal(t, err, store.ErrInvalid, "width is columns out of 12, from 1 to 12")
}

// editingStore lets a rival write land between UpdateWidget's read of the
// widget and its write: before any widget write passes through, edit
// runs against the store, once, as another writer would.
type editingStore struct {
	Store
	edit func(ctx context.Context, w store.Widget) store.Widget
	cols store.WidgetColumns // what the rival writes
	done bool
}

func (e *editingStore) UpdateWidget(ctx context.Context, w store.Widget, cols store.WidgetColumns, a store.AuditEntry) error {
	if !e.done {
		e.done = true
		cur, err := e.Store.GetWidget(ctx, w.ID)
		if err != nil {
			return err
		}
		err = e.Store.UpdateWidget(ctx, e.edit(ctx, cur), e.cols, store.AuditEntry{Actor: "rival", Action: "rival.edit"})
		if err != nil {
			return err
		}
	}
	return e.Store.UpdateWidget(ctx, w, cols, a)
}

// A move or resize writes only the layout, so an edit that landed after
// the widget was read survives it (the page's drag racing an agent).
func TestUpdateWidgetLayoutKeepsAConcurrentEdit(t *testing.T) {
	base := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, base, "DD", note("A"), note("B"))
	a, b := d.Widgets[0].ID, d.Widgets[1].ID
	retitle := func(_ context.Context, w store.Widget) store.Widget { w.Title = "Edited"; return w }
	svc := New(&editingStore{Store: base.st, edit: retitle, cols: store.WidgetColumns{Content: true}}, base.db, Options{})

	w, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: a, Width: ptr(6), Height: ptr(4)})
	if err != nil {
		t.Fatal(err)
	}
	if w.Title != "Edited" || w.Width != 6 || w.Height != 4 {
		t.Errorf("resized = %q %dx%d, want the edited title at 6x4", w.Title, w.Width, w.Height)
	}
	svc = New(&editingStore{Store: base.st, edit: retitle, cols: store.WidgetColumns{Content: true}}, base.db, Options{})
	w, err = svc.UpdateWidget(ctx, "test", UpdateWidget{ID: b, After: ptr(int64(0))})
	if err != nil {
		t.Fatal(err)
	}
	if w.Title != "Edited" {
		t.Errorf("moved: title = %q, want the edited one", w.Title)
	}
	if got, want := liveNames(t, base, d.ID), []string{"b", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// The mirror: a content change writes only the content, so a drag that
// landed while the new content was being validated is not reverted.
func TestUpdateWidgetContentKeepsAConcurrentLayout(t *testing.T) {
	base := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, base, "DD", note("A"), note("B"))
	a := d.Widgets[0].ID
	drag := func(_ context.Context, w store.Widget) store.Widget {
		w.Width, w.Height, w.SortKey = 5, 3, "zz"
		return w
	}
	svc := New(&editingStore{Store: base.st, edit: drag,
		cols: store.WidgetColumns{SortKey: true, Width: true, Height: true}}, base.db, Options{})

	w, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: a, Title: ptr("New")})
	if err != nil {
		t.Fatal(err)
	}
	if w.Title != "New" || w.Width != 5 || w.Height != 3 {
		t.Errorf("retitled = %q %dx%d, want the new title at the dragged 5x3", w.Title, w.Width, w.Height)
	}
	if got, want := liveNames(t, base, d.ID), []string{"b", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want the dragged place kept (%v)", got, want)
	}
}

// A resize of one side leaves the other as a concurrent save made it.
func TestUpdateWidgetWidthKeepsAConcurrentHeight(t *testing.T) {
	base := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, base, "DD", note("A"))
	a := d.Widgets[0].ID
	taller := func(_ context.Context, w store.Widget) store.Widget { w.Height = 7; return w }
	svc := New(&editingStore{Store: base.st, edit: taller, cols: store.WidgetColumns{Height: true}}, base.db, Options{})

	w, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: a, Width: ptr(4)})
	if err != nil {
		t.Fatal(err)
	}
	if w.Width != 4 || w.Height != 7 {
		t.Errorf("resized = %dx%d, want 4x7", w.Width, w.Height)
	}
}

// A move writes only the place, so a resize saved between the move's read
// and its write keeps both sides (the page's drag racing an agent).
func TestUpdateWidgetMoveKeepsAConcurrentSize(t *testing.T) {
	base := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, base, "DD", note("A"), note("B"))
	b := d.Widgets[1].ID
	resize := func(_ context.Context, w store.Widget) store.Widget { w.Width, w.Height = 5, 7; return w }
	svc := New(&editingStore{Store: base.st, edit: resize,
		cols: store.WidgetColumns{Width: true, Height: true}}, base.db, Options{})

	w, err := svc.UpdateWidget(ctx, "test", UpdateWidget{ID: b, After: ptr(int64(0))})
	if err != nil {
		t.Fatal(err)
	}
	if w.Width != 5 || w.Height != 7 {
		t.Errorf("moved = %dx%d, want the rival's 5x7", w.Width, w.Height)
	}
	if got, want := liveNames(t, base, d.ID), []string{"b", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// An update that names nothing writes nothing: the widget comes back as
// it is and no audit row is made.
func TestUpdateWidgetWithNothingToChangeWritesNothing(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "DD", note("A"))
	a := d.Widgets[0].ID
	before := auditRows(t, svc)
	for _, in := range []UpdateWidget{{ID: a}, {ID: a, After: ptr(a)}} {
		w, err := svc.UpdateWidget(ctx, "test", in)
		if err != nil {
			t.Fatalf("%+v: %v", in, err)
		}
		if w.ID != a || w.Name != "a" {
			t.Errorf("%+v returned %+v", in, w)
		}
	}
	if got := auditRows(t, svc); !reflect.DeepEqual(got, before) {
		t.Errorf("an empty update changed the audit log: %v -> %v", before, got)
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
	return mustCreate(t, svc, "DD", specs...)
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
	d, err := svc.CreateDashboard(ctx, "mcp", CreateDashboard{Title: "DD", Widgets: []WidgetSpec{note("A")}})
	must(err)
	_, err = svc.UpdateDashboard(ctx, "rest", UpdateDashboard{ID: d.ID, Title: "EE"})
	must(err)
	cp, err := svc.DuplicateDashboard(ctx, "mcp", DuplicateDashboard{ID: d.ID})
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
		"duplicate": func() error { _, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: 9999}); return err },
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
	if !errors.Is(err, store.ErrInvalid) {
		t.Errorf("blank title: err = %v, want ErrInvalid", err)
	}
}

// --- Review fixes ---

func TestUpdateWidgetRefusesClearingComponent(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	msg := "component must not be empty; list_components names the ones there are"
	d := mustCreate(t, svc, "DD", note("A"))
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
	d := mustCreate(t, svc, "DD", spec)
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
	d := mustCreate(t, svc, "DD")
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
	a := mustCreate(t, svc, "AA")
	b := mustJoin(t, svc, "BB", a.ID)
	c := mustJoin(t, svc, "CC", a.ID)
	x := mustCreate(t, svc, "XX")
	y := mustCreate(t, svc, "YY")
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
	_, err = svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "NN", After: &y.ID})
	wantRefusal(t, err, store.ErrInvalid, msg(y.ID))
	_, err = svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "NN", GroupID: a.GroupID, After: &b.ID})
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
		d := mustCreate(t, svc, "DD")
		if d.GroupID != d.ID {
			t.Errorf("group_id = %d, want its own id %d", d.GroupID, d.ID)
		}
		if len(d.Tabs) != 1 || d.Tabs[0] != (Tab{ID: d.ID, Title: "DD"}) {
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
		a := mustCreate(t, svc, "AA")
		b := mustJoin(t, svc, "BB", a.ID)
		c := mustJoin(t, svc, "CC", a.ID)
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
		a := mustCreate(t, svc, "AA")
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
		a := mustCreate(t, svc, "AA")
		mustCreate(t, svc, "Other")
		b := mustJoin(t, svc, "BB", a.ID)
		c := mustJoin(t, svc, "CC", a.ID)
		if b.GroupID != a.ID || c.GroupID != a.ID {
			t.Errorf("group ids = %d, %d, want %d", b.GroupID, c.GroupID, a.ID)
		}
		if got, want := sidebar(t, svc), []string{"AA/AA", "BB/AA", "CC/AA", "Other/Other"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
		if got, want := c.Tabs, []Tab{{a.ID, "AA"}, {b.ID, "BB"}, {c.ID, "CC"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("tabs = %+v, want %+v", got, want)
		}
	})

	t.Run("after 0 makes it the first tab", func(t *testing.T) {
		svc := newTestService(t)
		mustCreate(t, svc, "Before")
		a := mustCreate(t, svc, "AA")
		mustJoin(t, svc, "BB", a.ID)
		if _, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "ZZ", GroupID: a.ID, After: ptr(int64(0))}); err != nil {
			t.Fatal(err)
		}
		if got, want := sidebar(t, svc), []string{"Before/Before", "ZZ/AA", "AA/AA", "BB/AA"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
	})

	t.Run("after a member places it right after that tab", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "AA")
		mustJoin(t, svc, "BB", a.ID)
		if _, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "ZZ", GroupID: a.ID, After: &a.ID}); err != nil {
			t.Fatal(err)
		}
		if got, want := sidebar(t, svc), []string{"AA/AA", "ZZ/AA", "BB/AA"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
	})

	t.Run("after a non-member is refused", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "AA")
		other := mustCreate(t, svc, "Other")
		_, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "ZZ", GroupID: a.ID, After: &other.ID})
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
			_, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "ZZ", GroupID: g})
			wantRefusal(t, err, store.ErrInvalid, "group "+itoa(g)+" has no live user dashboard")
		}
		if got := len(userRows(t, svc)); got != 1 {
			t.Errorf("user rows = %d, want 1 (nothing created)", got)
		}
	})

	t.Run("without group_id, after a tab lands after the whole group", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "AA")
		mustJoin(t, svc, "BB", a.ID)
		mustCreate(t, svc, "Other")
		if _, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: "ZZ", After: &a.ID}); err != nil {
			t.Fatal(err)
		}
		if got, want := sidebar(t, svc), []string{"AA/AA", "BB/AA", "ZZ/ZZ", "Other/Other"}; !reflect.DeepEqual(got, want) {
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
		mustCreate(t, svc, "XX")
		a := mustCreate(t, svc, "AA")
		b := mustJoin(t, svc, "BB", a.ID)
		c := mustJoin(t, svc, "CC", a.ID)
		mustCreate(t, svc, "YY")
		update(t, svc, UpdateDashboard{ID: c.ID, After: &a.ID})
		if got, want := sidebar(t, svc), []string{"XX/XX", "AA/AA", "CC/AA", "BB/AA", "YY/YY"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
		if got, want := tabIDs(t, svc, b.ID), []int64{a.ID, c.ID, b.ID}; !reflect.DeepEqual(got, want) {
			t.Errorf("tabs = %v, want %v", got, want)
		}
	})

	t.Run("after another group's member moves the whole group, archived members too", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "AA")
		b := mustJoin(t, svc, "BB", a.ID)
		c := mustJoin(t, svc, "CC", a.ID)
		mustCreate(t, svc, "Solo")
		x := mustCreate(t, svc, "XX")
		mustJoin(t, svc, "X2", x.ID)
		mustCreate(t, svc, "Last")
		if err := svc.ArchiveDashboard(ctx, "test", b.ID, false); err != nil {
			t.Fatal(err)
		}
		got := update(t, svc, UpdateDashboard{ID: c.ID, After: &x.ID, Title: "C2"})
		if got.Title != "C2" || got.GroupID != a.ID {
			t.Errorf("answer = %+v, want title C2 in group %d", got, a.ID)
		}
		if got, want := sidebar(t, svc), []string{"Solo/Solo", "XX/XX", "X2/XX", "AA/AA", "BB/AA", "C2/AA", "Last/Last"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
		wantContiguous(t, svc)
	})

	t.Run("after 0 moves a tab's whole group to the top", func(t *testing.T) {
		svc := newTestService(t)
		mustCreate(t, svc, "XX")
		a := mustCreate(t, svc, "AA")
		b := mustJoin(t, svc, "BB", a.ID)
		update(t, svc, UpdateDashboard{ID: b.ID, After: ptr(int64(0))})
		if got, want := sidebar(t, svc), []string{"AA/AA", "BB/AA", "XX/XX"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
	})

	t.Run("after itself changes nothing", func(t *testing.T) {
		svc := newTestService(t)
		mustCreate(t, svc, "XX")
		a := mustCreate(t, svc, "AA")
		b := mustJoin(t, svc, "BB", a.ID)
		mustCreate(t, svc, "YY")
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
		x := mustCreate(t, svc, "XX")
		x2 := mustJoin(t, svc, "X2", x.ID)
		a := mustCreate(t, svc, "AA")
		mustJoin(t, svc, "BB", a.ID)
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
		a := mustCreate(t, svc, "AA")
		b := mustJoin(t, svc, "BB", a.ID)
		c := mustJoin(t, svc, "CC", a.ID)
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
		a := mustCreate(t, svc, "AA")
		b := mustJoin(t, svc, "BB", a.ID)
		x := mustCreate(t, svc, "XX")
		mustJoin(t, svc, "X2", x.ID)
		got := update(t, svc, UpdateDashboard{ID: b.ID, GroupID: &x.ID, After: &x.ID})
		if got.GroupID != x.ID {
			t.Errorf("group_id = %d, want %d", got.GroupID, x.ID)
		}
		if got, want := sidebar(t, svc), []string{"AA/AA", "XX/XX", "BB/XX", "X2/XX"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
		_, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: a.ID, GroupID: &x.ID, After: &a.ID})
		wantRefusal(t, err, store.ErrInvalid, "after "+itoa(a.ID)+" is not a member of group "+itoa(x.ID))
	})

	t.Run("group_id 0 leaves as a group of one right after the old group", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "AA")
		b := mustJoin(t, svc, "BB", a.ID)
		mustJoin(t, svc, "CC", a.ID)
		mustCreate(t, svc, "YY")
		got := update(t, svc, UpdateDashboard{ID: b.ID, GroupID: ptr(int64(0))})
		if got.GroupID != b.ID {
			t.Errorf("group_id = %d, want its own id %d", got.GroupID, b.ID)
		}
		if got, want := sidebar(t, svc), []string{"AA/AA", "CC/AA", "BB/BB", "YY/YY"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
	})

	t.Run("group_id 0 with after places the new group in the sidebar", func(t *testing.T) {
		svc := newTestService(t)
		y := mustCreate(t, svc, "YY")
		a := mustCreate(t, svc, "AA")
		b := mustJoin(t, svc, "BB", a.ID)
		update(t, svc, UpdateDashboard{ID: b.ID, GroupID: ptr(int64(0)), After: ptr(int64(0))})
		if got, want := sidebar(t, svc), []string{"BB/BB", "YY/YY", "AA/AA"}; !reflect.DeepEqual(got, want) {
			t.Errorf("after 0: sidebar = %v, want %v", got, want)
		}
		update(t, svc, UpdateDashboard{ID: b.ID, GroupID: ptr(int64(0)), After: &y.ID})
		if got, want := sidebar(t, svc), []string{"YY/YY", "BB/BB", "AA/AA"}; !reflect.DeepEqual(got, want) {
			t.Errorf("after Y: sidebar = %v, want %v", got, want)
		}
	})

	t.Run("group_id 0 alone in its group changes only the title", func(t *testing.T) {
		svc := newTestService(t)
		mustCreate(t, svc, "XX")
		a := mustCreate(t, svc, "AA")
		mustCreate(t, svc, "YY")
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

	t.Run("group_id 0 on the dashboard whose id the group uses hands the group over", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "AA")
		b := mustJoin(t, svc, "BB", a.ID)
		mustCreate(t, svc, "YY")
		got := update(t, svc, UpdateDashboard{ID: a.ID, GroupID: ptr(int64(0))})
		if got.GroupID != a.ID {
			t.Errorf("A group_id = %d, want its own id %d", got.GroupID, a.ID)
		}
		if got, want := sidebar(t, svc), []string{"BB/BB", "AA/AA", "YY/YY"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v (B's group handed to B's id)", got, want)
		}
		if got, want := tabIDs(t, svc, b.ID), []int64{b.ID}; !reflect.DeepEqual(got, want) {
			t.Errorf("old group's tabs = %v, want %v", got, want)
		}
		// Having left, it can come back without merging into anything.
		update(t, svc, UpdateDashboard{ID: a.ID, GroupID: &b.ID, After: ptr(int64(0))})
		if got, want := tabIDs(t, svc, b.ID), []int64{a.ID, b.ID}; !reflect.DeepEqual(got, want) {
			t.Errorf("rejoined tabs = %v, want %v", got, want)
		}
		wantContiguous(t, svc)
	})

	t.Run("the handover goes to the first live dashboard left, archived ones repointed too", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "AA")
		b := mustJoin(t, svc, "BB", a.ID)
		c := mustJoin(t, svc, "CC", a.ID)
		if err := svc.ArchiveDashboard(ctx, "test", b.ID, false); err != nil {
			t.Fatal(err)
		}
		update(t, svc, UpdateDashboard{ID: a.ID, GroupID: ptr(int64(0))})
		for _, d := range userRows(t, svc) {
			if (d.ID == b.ID || d.ID == c.ID) && d.GroupID != c.ID {
				t.Errorf("dashboard %d group_id = %d, want C's id %d (B archived, so C is the heir)", d.ID, d.GroupID, c.ID)
			}
		}
		wantContiguous(t, svc)
	})

	t.Run("group_id 0 alone keeps its group id", func(t *testing.T) {
		svc := newTestService(t)
		a := mustCreate(t, svc, "AA")
		b := mustJoin(t, svc, "BB", a.ID)
		y := mustCreate(t, svc, "YY")
		update(t, svc, UpdateDashboard{ID: a.ID, GroupID: &y.ID}) // joining hands A's group to B
		before := userRows(t, svc)
		if got := update(t, svc, UpdateDashboard{ID: b.ID, GroupID: ptr(int64(0))}); got.GroupID != b.ID {
			t.Errorf("group_id = %d, want the one it had, %d", got.GroupID, b.ID)
		}
		if got := userRows(t, svc); !sameKeys(got, before) {
			t.Errorf("rows moved:\n%+v\nwas\n%+v", got, before)
		}
		if got := update(t, svc, UpdateDashboard{ID: b.ID, GroupID: ptr(int64(0)), After: &y.ID}); got.GroupID != b.ID {
			t.Errorf("with after: group_id = %d, want %d", got.GroupID, b.ID)
		}
		if got, want := sidebar(t, svc), []string{"YY/YY", "AA/YY", "BB/BB"}; !reflect.DeepEqual(got, want) {
			t.Errorf("sidebar = %v, want %v", got, want)
		}
	})

	t.Run("a system dashboard or group is refused and nothing moves", func(t *testing.T) {
		svc := newTestService(t)
		syncReporting(t, svc, nil, systemDashboard())
		mustCreate(t, svc, "XX")
		a := mustCreate(t, svc, "AA")
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

	cp, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: 12, WholeGroup: true}) // from Users, a middle member
	if err != nil {
		t.Fatal(err)
	}
	// The returned dashboard is the copy of the one named (Users), at
	// its own position among the copies, not always the group's first.
	if cp.Title != "Users" || cp.Owner != store.OwnerUser {
		t.Errorf("copy = %q owned by %q, want \"Users\" owned by user", cp.Title, cp.Owner)
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
	a := mustCreate(t, svc, "AA")
	b := mustJoin(t, svc, "BB", a.ID)
	mustJoin(t, svc, "CC", a.ID)
	if err := svc.ArchiveDashboard(ctx, "test", b.ID, false); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, svc, "ZZ")

	cp, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: a.ID, WholeGroup: true})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, tab := range cp.Tabs {
		titles = append(titles, tab.Title)
	}
	if want := []string{"AA (copy)", "CC"}; !reflect.DeepEqual(titles, want) {
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
	a := mustCreate(t, svc, "AA")
	b := mustJoin(t, svc, "BB", a.ID)
	if err := svc.ArchiveDashboard(ctx, "test", a.ID, true); err != nil {
		t.Fatal(err)
	}
	before := userRows(t, svc)

	for _, whole := range []bool{false, true} {
		_, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: b.ID, WholeGroup: whole})
		wantRefusal(t, err, store.ErrInvalid, "dashboard "+itoa(b.ID)+" is archived; restore_dashboard first")
	}
	if after := userRows(t, svc); len(after) != len(before) {
		t.Errorf("dashboards after refused duplicates = %d, want %d (nothing written)", len(after), len(before))
	}
}

// --- Duplicating never archives ---

// Duplicating a system tab copies just that tab, as a standalone user
// dashboard (the gallery's "Copy as a dashboard"); whole_group copies
// all five; neither ever archives the source.
func TestDuplicateSystemTab(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemGroup()...)
	ctx := context.Background()

	got, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: 12}) // Users
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Users (copy)" || len(got.Tabs) != 1 || got.ArchivedAt != "" {
		t.Errorf("copy = %q tabs %d archived %q, want a live standalone 'Users (copy)'", got.Title, len(got.Tabs), got.ArchivedAt)
	}
	for id := int64(10); id <= 14; id++ {
		if d, _ := svc.st.GetDashboard(ctx, id); d.ArchivedAt != "" {
			t.Errorf("system dashboard %d archived by a duplicate, want live", id)
		}
	}

	whole, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: 12, WholeGroup: true})
	if err != nil {
		t.Fatal(err)
	}
	if whole.Owner != store.OwnerUser || whole.Title != "Users" || whole.ArchivedAt != "" {
		t.Errorf("whole_group copy = %s %q archived %q, want a live user copy of Users", whole.Owner, whole.Title, whole.ArchivedAt)
	}
	var titles []string
	for _, tab := range whole.Tabs {
		titles = append(titles, tab.Title)
	}
	if want := []string{"Views (copy)", "Product", "Users", "Groups", "Retention"}; !reflect.DeepEqual(titles, want) {
		t.Errorf("whole_group copy's tabs = %v, want %v", titles, want)
	}
	for id := int64(10); id <= 14; id++ {
		if d, _ := svc.st.GetDashboard(ctx, id); d.ArchivedAt != "" {
			t.Errorf("system dashboard %d archived by a whole_group duplicate, want live", id)
		}
	}
	// The whole_group copy's group is placed last in the sidebar, after
	// every other user dashboard.
	list, err := svc.Dashboards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if last := list.Dashboards[len(list.Dashboards)-1]; last.GroupID != whole.GroupID {
		t.Errorf("last sidebar entry's group = %d, want the copy's group %d", last.GroupID, whole.GroupID)
	}
}

// A user source is never archived by duplicating it; an archived user
// source is still refused.
func TestDuplicateUserSourceNeverArchived(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	a := mustCreate(t, svc, "AA")
	if _, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: a.ID}); err != nil {
		t.Fatal(err)
	}
	if d, _ := svc.st.GetDashboard(ctx, a.ID); d.ArchivedAt != "" {
		t.Errorf("user source archived by a duplicate, want live")
	}
	if err := svc.ArchiveDashboard(ctx, "test", a.ID, false); err != nil {
		t.Fatal(err)
	}
	_, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: a.ID})
	wantRefusal(t, err, store.ErrInvalid, fmt.Sprintf("dashboard %d is archived; restore_dashboard first", a.ID))
}

// --- Archiving and restoring ---

func TestArchiveDashboardKeepsOtherTabs(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	a := mustCreate(t, svc, "AA")
	b := mustJoin(t, svc, "BB", a.ID)
	c := mustJoin(t, svc, "CC", a.ID)

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
	mustCreate(t, svc, "XX")
	a := mustCreate(t, svc, "AA")
	mustCreate(t, svc, "YY")
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
	a := mustCreate(t, svc, "AA")
	b := mustJoin(t, svc, "BB", a.ID)
	c := mustJoin(t, svc, "CC", a.ID)
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

// Spec 2026-10-05 D5: a built-in is never archived or restored, alone
// or with its group; update_dashboard sidebar hides it instead.
func TestArchiveDashboardRefusesBuiltin(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemGroup()...)
	ctx := context.Background()
	const builtin = "dashboard 12 is a built-in dashboard and is never archived; update_dashboard {sidebar: false} takes its group out of the sidebar"
	for _, whole := range []bool{false, true} {
		wantRefusal(t, svc.ArchiveDashboard(ctx, "test", 12, whole), store.ErrInvalid, builtin)
		wantRefusal(t, svc.RestoreDashboard(ctx, "test", 12, whole), store.ErrInvalid, builtin)
	}
	for id := int64(10); id <= 14; id++ {
		if d, _ := svc.st.GetDashboard(ctx, id); d.ArchivedAt != "" {
			t.Errorf("system dashboard %d archived, want live", id)
		}
	}
	if got := auditRows(t, svc); len(got) != 0 {
		t.Errorf("audit = %v, want none (every call refused)", got)
	}
}

// D17, review focus 5: list_dashboards says how long an archived user
// dashboard is kept; 0 (kept forever) is omitted.
func TestDashboardsPurgeAfterDays(t *testing.T) {
	svc, _ := newTestServiceOpts(t, Options{ArchivedDays: 30}, 1000)
	got, err := svc.Dashboards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.PurgeAfterDays != 30 {
		t.Errorf("PurgeAfterDays = %d, want 30", got.PurgeAfterDays)
	}
	b, _ := json.Marshal(Dashboards{Timezone: "UTC"})
	if strings.Contains(string(b), "purge_after_days") {
		t.Errorf("0 days marshals as %s, want the field omitted", b)
	}
}

// list_dashboards says how often the page auto-refreshes: the longer of
// the two cache ages, so a reload always loads anew; 0 (both off) is
// omitted.
func TestDashboardsAutoRefreshSeconds(t *testing.T) {
	for _, c := range []struct {
		cache, refresh time.Duration
		want           int
	}{
		{15 * time.Minute, time.Minute, 900},
		{0, time.Minute, 60},
		{0, 0, 0},
	} {
		svc, _ := newTestServiceOpts(t, Options{CacheAge: c.cache, RefreshAge: c.refresh}, 1000)
		got, err := svc.Dashboards(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.AutoRefreshSeconds != c.want {
			t.Errorf("cache %v, refresh %v: AutoRefreshSeconds = %d, want %d", c.cache, c.refresh, got.AutoRefreshSeconds, c.want)
		}
	}
	b, _ := json.Marshal(Dashboards{Timezone: "UTC"})
	if strings.Contains(string(b), "auto_refresh_seconds") {
		t.Errorf("0 marshals as %s, want the field omitted", b)
	}
}

// --- Titles and group names (spec 2026-10-04 D5, D11) ---

func TestCreateDashboardTitleMinimum(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	for _, title := range []string{"a", "é", " a ", ""} {
		if _, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: title}); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("create %q: err = %v, want ErrInvalid", title, err)
		}
	}
	d, err := svc.CreateDashboard(ctx, "test", CreateDashboard{Title: " Ops "})
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Ops" {
		t.Errorf("title = %q, want the trimmed \"Ops\"", d.Title)
	}
}

func TestUpdateDashboardTitleMinimum(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "Before")
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: d.ID, Title: "é"}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("update \"é\": err = %v, want ErrInvalid", err)
	}
	got, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: d.ID, Title: "  ab "})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "ab" {
		t.Errorf("title = %q, want the trimmed \"ab\"", got.Title)
	}
	if after, _ := svc.st.GetDashboard(ctx, d.ID); after.Title != "ab" {
		t.Errorf("stored title = %q, want \"ab\"", after.Title)
	}
}

// renameGroup names id's group through the service.
func renameGroup(svc *Service, id int64, title string) (DashboardInfo, error) {
	return svc.UpdateDashboard(context.Background(), "test", UpdateDashboard{ID: id, WholeGroup: true, Title: title})
}

func TestRenameGroupNamesEveryTabAndKeepsTitles(t *testing.T) {
	svc := newTestService(t)
	a := mustCreate(t, svc, "Alpha")
	b := mustJoin(t, svc, "Beta", a.ID)

	info, err := renameGroup(svc, b.ID, "  Ops ")
	if err != nil {
		t.Fatal(err)
	}
	if info.GroupTitle != "Ops" || info.Title != "Beta" {
		t.Errorf("renamed = %q titled %q, want group \"Ops\", title \"Beta\"", info.GroupTitle, info.Title)
	}
	for id, title := range map[int64]string{a.ID: "Alpha", b.ID: "Beta"} {
		d, err := svc.Dashboard(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if d.GroupTitle != "Ops" || d.Title != title {
			t.Errorf("dashboard %d = group %q title %q, want \"Ops\" and %q", id, d.GroupTitle, d.Title, title)
		}
	}
	list, err := svc.Dashboards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range list.Dashboards {
		if x.GroupTitle != "Ops" {
			t.Errorf("list row %d group_title = %q, want \"Ops\"", x.ID, x.GroupTitle)
		}
	}
	// A second rename replaces the name.
	if info, err = renameGroup(svc, a.ID, "Platform"); err != nil || info.GroupTitle != "Platform" {
		t.Errorf("second rename = %q, %v, want \"Platform\"", info.GroupTitle, err)
	}
}

func TestRenameGroupOfOneLeavesTitle(t *testing.T) {
	svc := newTestService(t)
	d := mustCreate(t, svc, "Solo")
	info, err := renameGroup(svc, d.ID, "Team")
	if err != nil {
		t.Fatal(err)
	}
	if info.GroupTitle != "Team" || info.Title != "Solo" {
		t.Errorf("got group %q title %q, want \"Team\" and \"Solo\"", info.GroupTitle, info.Title)
	}
}

func TestRenameGroupRefusals(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d := mustCreate(t, svc, "Solo")
	if _, err := renameGroup(svc, d.ID, "Keep"); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]UpdateDashboard{
		"missing title": {ID: d.ID, WholeGroup: true},
		"blank title":   {ID: d.ID, WholeGroup: true, Title: "  "},
		"one character": {ID: d.ID, WholeGroup: true, Title: "x"},
		"with after":    {ID: d.ID, WholeGroup: true, Title: "Fine", After: ptr(int64(0))},
		"with group_id": {ID: d.ID, WholeGroup: true, Title: "Fine", GroupID: ptr(int64(0))},
	} {
		if _, err := svc.UpdateDashboard(ctx, "test", in); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if got, _ := svc.st.GetDashboard(ctx, d.ID); got.GroupTitle != "Keep" {
		t.Errorf("group title = %q, want the name kept after every refusal", got.GroupTitle)
	}

	syncReporting(t, svc, nil, systemDashboard())
	if _, err := renameGroup(svc, 3, "Nope"); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("system dashboard: err = %v, want ErrInvalid", err)
	}
	other := mustCreate(t, svc, "Gone")
	if err := svc.ArchiveDashboard(ctx, "test", other.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := renameGroup(svc, other.ID, "Nope"); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("archived dashboard: err = %v, want ErrInvalid", err)
	}
	if _, err := renameGroup(svc, 9999, "Nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown dashboard: err = %v, want ErrNotFound", err)
	}
}

// --- Sidebar and project tabs (spec 2026-10-05 D3, D5, D7) ---

// mustAddTab gives project projectID a tab for dashboardID, straight
// through the store.
func mustAddTab(t *testing.T, svc *Service, projectID, dashboardID int64) {
	t.Helper()
	if err := svc.st.InsertProjectTab(context.Background(),
		store.ProjectTabRow{ProjectID: projectID, DashboardID: dashboardID, SortKey: "a0"},
		store.AuditEntry{Actor: "test", Action: "project.tab.add"}); err != nil {
		t.Fatal(err)
	}
}

// sidebarOf reads each of ids' sidebar flag from the store, and fails on
// one that is archived: hiding never archives.
func sidebarOf(t *testing.T, svc *Service, ids ...int64) []bool {
	t.Helper()
	out := make([]bool, len(ids))
	for i, id := range ids {
		d, err := svc.st.GetDashboard(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if d.ArchivedAt != "" {
			t.Errorf("dashboard %d archived, want live", id)
		}
		out[i] = d.Sidebar
	}
	return out
}

func TestUpdateDashboardSidebarHidesBuiltinGroup(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemGroup()...)
	ctx := context.Background()
	ids := []int64{10, 11, 12, 13, 14}

	info, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: 12, Sidebar: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != 12 || info.Sidebar || !info.ProjectTab {
		t.Errorf("info = %+v, want dashboard 12 out of the sidebar, still a project tab", info)
	}
	if got := sidebarOf(t, svc, ids...); !reflect.DeepEqual(got, []bool{false, false, false, false, false}) {
		t.Errorf("sidebar after hide = %v, want every member false", got)
	}

	// WholeGroup is accepted and ignored: sidebar is always the group's.
	if _, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: 10, Sidebar: ptr(true), WholeGroup: true}); err != nil {
		t.Fatal(err)
	}
	if got := sidebarOf(t, svc, ids...); !reflect.DeepEqual(got, []bool{true, true, true, true, true}) {
		t.Errorf("sidebar after show = %v, want every member true", got)
	}

	var want []string
	for _, action := range []string{"dashboard.sidebar.hide", "dashboard.sidebar.show"} {
		for range ids {
			want = append(want, "test "+action)
		}
	}
	if got := auditRows(t, svc); !reflect.DeepEqual(got, want) {
		t.Errorf("audit =\n%v\nwant\n%v", got, want)
	}
}

// D5: your own dashboards are always in the sidebar, so sidebar is
// refused on one, true or false: the field is for built-ins only.
func TestUpdateDashboardSidebarRefusedOnUser(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	a := mustCreate(t, svc, "AA")
	b := mustJoin(t, svc, "BB", a.ID)
	before := len(auditRows(t, svc))
	for _, on := range []bool{false, true} {
		_, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: b.ID, Sidebar: ptr(on)})
		wantRefusal(t, err, store.ErrInvalid,
			"dashboard "+itoa(b.ID)+" is your own: your own dashboards are always in the sidebar; archive_dashboard takes one away")
	}
	if got := sidebarOf(t, svc, a.ID, b.ID); !reflect.DeepEqual(got, []bool{true, true}) {
		t.Errorf("sidebar after refusals = %v, want both still true", got)
	}
	if after := len(auditRows(t, svc)); after != before {
		t.Errorf("audit rows = %d, want %d (nothing written)", after, before)
	}
}

func TestUpdateDashboardPlacementTakesNoTitle(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	a := mustCreate(t, svc, "AA")
	const alone = "sidebar goes on its own; give title, after or group_id in another call"
	for _, in := range []UpdateDashboard{
		{ID: a.ID, Sidebar: ptr(true), Title: "x"},
		{ID: a.ID, Sidebar: ptr(false), After: ptr[int64](0)},
		{ID: a.ID, Sidebar: ptr(true), GroupID: ptr[int64](0)},
	} {
		_, err := svc.UpdateDashboard(ctx, "test", in)
		wantRefusal(t, err, store.ErrInvalid, alone)
	}
}

// A built-in an older binary archived takes no sidebar write until the
// release sync brings it back.
func TestUpdateDashboardPlacementRefusesArchived(t *testing.T) {
	svc := newTestService(t)
	syncReporting(t, svc, nil, systemDashboard())
	ctx := context.Background()
	if err := svc.st.SetDashboardsArchived(ctx, []int64{3}, true, store.AuditEntry{Actor: "older binary", Action: "dashboard.archive"}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.UpdateDashboard(ctx, "test", UpdateDashboard{ID: 3, Sidebar: ptr(true)})
	wantRefusal(t, err, store.ErrInvalid, "dashboard 3 is archived; restore_dashboard first")
}

// D9: a copy, of a built-in or of one's own, is in the sidebar, is no
// project's tab, and is not given to new projects.
func TestDuplicateIsInSidebar(t *testing.T) {
	svc, st := newTestServiceOpts(t, Options{}, 1000)
	syncReporting(t, svc, nil, systemGroup()...)
	ctx := context.Background()
	p := mustCreateProject(t, st, "demo") // takes every built-in as a tab

	one, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: 10})
	if err != nil {
		t.Fatal(err)
	}
	whole, err := svc.DuplicateDashboard(ctx, "test", DuplicateDashboard{ID: 10, WholeGroup: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{one.ID}
	for _, tab := range whole.Tabs {
		ids = append(ids, tab.ID)
	}
	for _, id := range ids {
		d, err := svc.Dashboard(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Sidebar || d.ProjectTab {
			t.Errorf("copy %d: sidebar %v project_tab %v, want true, false", id, d.Sidebar, d.ProjectTab)
		}
	}
	tabs, err := svc.ProjectTabs(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, tab := range tabs {
		if tab.Owner != store.OwnerSystem {
			t.Errorf("project tab %+v, want built-ins only: a copy is no project's tab", tab)
		}
	}
}
