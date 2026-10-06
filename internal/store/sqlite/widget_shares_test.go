package sqlite

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func png1() []byte { return []byte("\x89PNG-1x") } // the store doesn't decode
func png2() []byte { return []byte("\x89PNG-2x") }

const shareNow = "2026-10-06T00:00:00Z"

// shareFixture is a project "blog" and one widget on a user dashboard.
func shareFixture(t *testing.T) (db *DB, pid, wid int64) {
	t.Helper()
	db = newTestDB(t)
	pid = createPurgeableProject(t, db, "blog")
	did := createPurgeableDashboard(t, db, store.OwnerUser, "a0", []store.Widget{
		{SortKey: "a", Width: 1, Height: 1, Name: "w1", SourceType: "events", Source: "a"}})
	ws, err := db.ListWidgets(context.Background(), did)
	if err != nil || len(ws) != 1 {
		t.Fatalf("widgets = %v, %v", ws, err)
	}
	return db, pid, ws[0].ID
}

func shareID(n int) string {
	return "0190a000-0000-7000-8000-" + string(rune('0'+n/10)) + string(rune('0'+n%10)) + "0000000000"
}

func insertShare(t *testing.T, db *DB, id string, pid, wid int64, archiveAt string) store.WidgetShare {
	t.Helper()
	got, err := db.InsertWidgetShare(context.Background(), store.NewWidgetShare{ID: id,
		WidgetID: wid, ProjectID: pid, From: "2026-09-01", To: "2026-09-30", Title: "Visitors",
		Image: png1(), Image2x: png2(), ArchiveAt: archiveAt},
		store.AuditEntry{Actor: "api", Action: "widget_share.create"})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func shareIDs(shares []store.WidgetShare) []string {
	ids := []string{}
	for _, s := range shares {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestInsertWidgetShareCopiesProjectName(t *testing.T) {
	db, pid, wid := shareFixture(t)
	id := "0190a000-0000-7000-8000-000000000001"
	got, err := db.InsertWidgetShare(context.Background(), store.NewWidgetShare{ID: id,
		WidgetID: wid, ProjectID: pid, From: "2026-09-01", To: "2026-09-30", Title: "Visitors",
		Image: png1(), Image2x: png2(), ArchiveAt: "2026-11-01T00:00:00Z"},
		store.AuditEntry{Actor: "api", Action: "widget_share.create"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.ProjectName != "blog" || got.ProjectID != pid || got.WidgetID != wid ||
		got.DashboardID == 0 || got.DashboardTitle == "" || got.ArchiveAt != "2026-11-01T00:00:00Z" ||
		got.From != "2026-09-01" || got.To != "2026-09-30" || got.Title != "Visitors" ||
		got.CreatedAt == "" || got.ArchivedAt != "" {
		t.Fatalf("got %+v", got)
	}
	rows := auditRows(t, db, "widget_share.create")
	if len(rows) != 1 || rows[0].Actor != "api" || rows[0].Subject != "widget_share/"+id {
		t.Fatalf("audit = %+v", rows)
	}
	// The name is a copy: renaming the project leaves the share alone.
	if _, err := db.ExecForTest(`UPDATE projects SET name='renamed' WHERE id=?`, pid); err != nil {
		t.Fatal(err)
	}
	again, err := db.GetWidgetShare(context.Background(), id)
	if err != nil || again.ProjectName != "blog" {
		t.Fatalf("after rename = %+v, %v", again, err)
	}
}

func TestInsertWidgetShareProjectLifetime(t *testing.T) {
	db, pid, wid := shareFixture(t)
	got := insertShare(t, db, shareID(1), pid, wid, "")
	if got.ArchiveAt != "" {
		t.Fatalf("ArchiveAt = %q, want project lifetime", got.ArchiveAt)
	}
}

func TestInsertWidgetShareCaptions(t *testing.T) {
	db, pid, wid := shareFixture(t)
	for i, c := range []struct{ project, rng bool }{{true, true}, {true, false}, {false, true}, {false, false}} {
		id := shareID(i + 1)
		got, err := db.InsertWidgetShare(context.Background(), store.NewWidgetShare{ID: id,
			WidgetID: wid, ProjectID: pid, From: "2026-09-01", To: "2026-09-30", Title: "T",
			Image: png1(), Image2x: png2(), CaptionProject: c.project, CaptionRange: c.rng},
			store.AuditEntry{Actor: "api", Action: "widget_share.create"})
		if err != nil {
			t.Fatal(err)
		}
		again, err := db.GetWidgetShare(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range []store.WidgetShare{got, again} {
			if r.CaptionProject != c.project || r.CaptionRange != c.rng {
				t.Errorf("%+v: captions project=%v range=%v, want %v %v", c, r.CaptionProject, r.CaptionRange, c.project, c.rng)
			}
		}
	}
	// A row written without them (as by hand) captions both.
	if _, err := db.ExecForTest(`INSERT INTO widget_shares (id, project_id, range_from, range_to, title, project_name, image, image_2x)
		VALUES (?, ?, '2026-09-01', '2026-09-30', 'T', 'blog', x'00', x'00')`, shareID(9), pid); err != nil {
		t.Fatal(err)
	}
	if r, err := db.GetWidgetShare(context.Background(), shareID(9)); err != nil || !r.CaptionProject || !r.CaptionRange {
		t.Fatalf("defaults = %+v, %v; want both captions", r, err)
	}
}

func TestInsertWidgetShareUnknownProject(t *testing.T) {
	db, _, wid := shareFixture(t)
	_, err := db.InsertWidgetShare(context.Background(), store.NewWidgetShare{ID: shareID(1),
		WidgetID: wid, ProjectID: 9999, From: "2026-09-01", To: "2026-09-30", Title: "T",
		Image: png1(), Image2x: png2()},
		store.AuditEntry{Actor: "api", Action: "widget_share.create"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM widget_shares`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows = %d, %v", n, err)
	}
	if rows := auditRows(t, db, "widget_share.create"); len(rows) != 0 {
		t.Fatalf("audit = %+v, want none", rows)
	}
}

func TestWidgetShareImage(t *testing.T) {
	db, pid, wid := shareFixture(t)
	ctx := context.Background()
	insertShare(t, db, shareID(1), pid, wid, "")
	one, err := db.WidgetShareImage(ctx, shareID(1), false)
	if err != nil || !bytes.Equal(one, png1()) {
		t.Fatalf("1x = %q, %v", one, err)
	}
	two, err := db.WidgetShareImage(ctx, shareID(1), true)
	if err != nil || !bytes.Equal(two, png2()) {
		t.Fatalf("2x = %q, %v", two, err)
	}
	for _, twoX := range []bool{false, true} {
		if _, err := db.WidgetShareImage(ctx, shareID(2), twoX); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown id (2x=%v) = %v, want ErrNotFound", twoX, err)
		}
	}
	if _, err := db.GetWidgetShare(ctx, shareID(2)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetWidgetShare unknown = %v, want ErrNotFound", err)
	}
}

// seedShareStates makes A live (archive_at in the future), B due but not
// yet archived, C archived by hand, D project lifetime. Created and
// archived stamps are forced so the order is deterministic.
func seedShareStates(t *testing.T, db *DB, pid, wid int64) (a, b, c, d string) {
	t.Helper()
	a, b, c, d = shareID(1), shareID(2), shareID(3), shareID(4)
	insertShare(t, db, a, pid, wid, "2026-11-01T00:00:00Z")
	insertShare(t, db, b, pid, wid, "2026-10-01T00:00:00Z")
	insertShare(t, db, c, pid, wid, "")
	insertShare(t, db, d, pid, wid, "")
	for id, stamp := range map[string]string{
		a: "2026-09-01T00:00:00Z", b: "2026-09-02T00:00:00Z",
		c: "2026-09-03T00:00:00Z", d: "2026-09-04T00:00:00Z"} {
		if _, err := db.ExecForTest(`UPDATE widget_shares SET created_at=? WHERE id=?`, stamp, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecForTest(
		`UPDATE widget_shares SET archived_at='2026-10-02T00:00:00Z' WHERE id=?`, c); err != nil {
		t.Fatal(err)
	}
	return a, b, c, d
}

func TestListWidgetSharesState(t *testing.T) {
	db, pid, wid := shareFixture(t)
	ctx := context.Background()
	a, b, c, d := seedShareStates(t, db, pid, wid)

	list := func(f store.WidgetShareFilter) []string {
		t.Helper()
		f.Now = shareNow
		got, err := db.ListWidgetShares(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return shareIDs(got)
	}
	// Live: newest created first. Archived: most recently archived first
	// (C by hand on 10-02, B due on 10-01). All: that order over everything.
	if got, want := list(store.WidgetShareFilter{State: "live"}), []string{d, a}; !reflect.DeepEqual(got, want) {
		t.Errorf("live = %v, want %v", got, want)
	}
	if got, want := list(store.WidgetShareFilter{State: "archived"}), []string{c, b}; !reflect.DeepEqual(got, want) {
		t.Errorf("archived = %v, want %v", got, want)
	}
	if got := list(store.WidgetShareFilter{}); len(got) != 4 {
		t.Errorf("all = %v, want four", got)
	}

	// The widget filter narrows.
	other := createPurgeableDashboard(t, db, store.OwnerUser, "a1", []store.Widget{
		{SortKey: "a", Width: 1, Height: 1, Name: "w2", SourceType: "events", Source: "a"}})
	ws, err := db.ListWidgets(ctx, other)
	if err != nil || len(ws) != 1 {
		t.Fatalf("widgets = %v, %v", ws, err)
	}
	e := shareID(5)
	insertShare(t, db, e, pid, ws[0].ID, "")
	if got, want := list(store.WidgetShareFilter{WidgetID: ws[0].ID}), []string{e}; !reflect.DeepEqual(got, want) {
		t.Errorf("widget filter = %v, want %v", got, want)
	}
	if got := list(store.WidgetShareFilter{WidgetID: wid}); len(got) != 4 {
		t.Errorf("first widget = %v, want four", got)
	}

	if _, err := db.ListWidgetShares(ctx, store.WidgetShareFilter{State: "bogus", Now: shareNow}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("bad state = %v, want ErrInvalid", err)
	}
}

func TestSetWidgetShareArchiveAt(t *testing.T) {
	db, pid, wid := shareFixture(t)
	ctx := context.Background()
	a, b, c, _ := seedShareStates(t, db, pid, wid)
	audit := store.AuditEntry{Actor: "api", Action: "widget_share.update"}

	got, err := db.SetWidgetShareArchiveAt(ctx, a, "2026-12-01T00:00:00Z", shareNow, audit)
	if err != nil || got.ArchiveAt != "2026-12-01T00:00:00Z" {
		t.Fatalf("live = %+v, %v", got, err)
	}
	got, err = db.SetWidgetShareArchiveAt(ctx, a, "", shareNow, audit)
	if err != nil || got.ArchiveAt != "" {
		t.Fatalf("to project lifetime = %+v, %v", got, err)
	}
	rows := auditRows(t, db, "widget_share.update")
	if len(rows) != 2 || rows[0].Subject != "widget_share/"+a {
		t.Fatalf("audit = %+v", rows)
	}

	for name, id := range map[string]string{"archived": c, "due": b} {
		if _, err := db.SetWidgetShareArchiveAt(ctx, id, "2027-01-01T00:00:00Z", shareNow, audit); !errors.Is(err, store.ErrConflict) {
			t.Errorf("%s share = %v, want ErrConflict", name, err)
		}
	}
	if _, err := db.SetWidgetShareArchiveAt(ctx, shareID(9), "", shareNow, audit); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown = %v, want ErrNotFound", err)
	}
	if len(auditRows(t, db, "widget_share.update")) != 2 {
		t.Error("a refused change wrote an audit row")
	}
}

func TestSetWidgetShareArchived(t *testing.T) {
	db, pid, wid := shareFixture(t)
	ctx := context.Background()
	id := shareID(1)
	insertShare(t, db, id, pid, wid, "2026-11-01T00:00:00Z")
	archive := store.AuditEntry{Actor: "api", Action: "widget_share.archive"}

	got, err := db.SetWidgetShareArchived(ctx, id, true, "", archive)
	if err != nil || got.ArchivedAt == "" {
		t.Fatalf("archive = %+v, %v", got, err)
	}
	if _, err := db.ExecForTest(`UPDATE widget_shares SET archived_at='2026-10-01T00:00:00Z' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	const first = "2026-10-01T00:00:00Z"
	got, err = db.SetWidgetShareArchived(ctx, id, true, "", archive)
	if err != nil || got.ArchivedAt != first {
		t.Fatalf("archive again = %+v, %v; want archived_at kept %s", got, err, first)
	}
	if rows := auditRows(t, db, "widget_share.archive"); len(rows) != 1 {
		t.Fatalf("audit after idempotent archive = %+v, want one row", rows)
	}

	restore := store.AuditEntry{Actor: "api", Action: "widget_share.restore"}
	got, err = db.SetWidgetShareArchived(ctx, id, false, "2026-12-01T00:00:00Z", restore)
	if err != nil || got.ArchivedAt != "" || got.ArchiveAt != "2026-12-01T00:00:00Z" {
		t.Fatalf("restore = %+v, %v", got, err)
	}
	if rows := auditRows(t, db, "widget_share.restore"); len(rows) != 1 || rows[0].Subject != "widget_share/"+id {
		t.Fatalf("restore audit = %+v", rows)
	}

	// A share past its archive_at that the daily pass has not reached
	// yet takes the new archive_at on restore too.
	if _, err := db.ExecForTest(`UPDATE widget_shares SET archive_at='2026-10-01T00:00:00Z' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	got, err = db.SetWidgetShareArchived(ctx, id, false, "", restore)
	if err != nil || got.ArchivedAt != "" || got.ArchiveAt != "" {
		t.Fatalf("restore due = %+v, %v", got, err)
	}

	for _, archived := range []bool{true, false} {
		if _, err := db.SetWidgetShareArchived(ctx, shareID(9), archived, "", archive); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("unknown (archived=%v) = %v, want ErrNotFound", archived, err)
		}
	}
}

func TestArchiveDueWidgetShares(t *testing.T) {
	db, pid, wid := shareFixture(t)
	ctx := context.Background()
	a, b, c, d := seedShareStates(t, db, pid, wid)
	// A second due share, to count two.
	e := shareID(5)
	insertShare(t, db, e, pid, wid, "2026-10-06T00:00:00Z")

	n, err := db.ArchiveDueWidgetShares(ctx, shareNow)
	if err != nil || n != 2 {
		t.Fatalf("archived = %d, %v; want 2", n, err)
	}
	stamp := func(id string) string {
		s, err := db.GetWidgetShare(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return s.ArchivedAt
	}
	if got := stamp(b); got != "2026-10-01T00:00:00Z" {
		t.Errorf("B archived_at = %q, want its archive_at", got)
	}
	if got := stamp(e); got != "2026-10-06T00:00:00Z" {
		t.Errorf("E archived_at = %q, want its archive_at", got)
	}
	if got := stamp(c); got != "2026-10-02T00:00:00Z" {
		t.Errorf("C archived_at = %q, want untouched", got)
	}
	if stamp(a) != "" || stamp(d) != "" {
		t.Errorf("a share not yet due was archived")
	}
	rows := auditRows(t, db, "widget_share.archive")
	if len(rows) != 2 {
		t.Fatalf("audit = %+v, want two rows", rows)
	}
	for _, r := range rows {
		if r.Actor != "retention" || (r.Subject != "widget_share/"+b && r.Subject != "widget_share/"+e) {
			t.Errorf("audit row %+v", r)
		}
	}
	if n, err := db.ArchiveDueWidgetShares(ctx, shareNow); err != nil || n != 0 {
		t.Fatalf("second pass = %d, %v; want 0", n, err)
	}
}

func TestWidgetShareOutlivesWidget(t *testing.T) {
	db, pid, wid := shareFixture(t)
	ctx := context.Background()
	id := shareID(1)
	insertShare(t, db, id, pid, wid, "")
	if err := db.SetWidgetArchived(ctx, wid, true, store.AuditEntry{Actor: "agent", Action: "widget.archive"}); err != nil {
		t.Fatal(err)
	}
	archiveWidgetDaysAgo(t, db, wid, 40)
	res, err := db.PurgeArchived(ctx, 30)
	if err != nil || len(res.Widgets) != 1 {
		t.Fatalf("purge = %+v, %v", res, err)
	}
	got, err := db.GetWidgetShare(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.WidgetID != 0 || got.DashboardID != 0 || got.DashboardTitle != "" || got.Title != "Visitors" {
		t.Fatalf("share after widget purge = %+v", got)
	}
}

func TestDeleteProjectDeletesWidgetShares(t *testing.T) {
	db, pid, wid := shareFixture(t)
	ctx := context.Background()
	other := createPurgeableProject(t, db, "shop")
	insertShare(t, db, shareID(1), pid, wid, "")
	insertShare(t, db, shareID(2), pid, wid, "")
	if _, err := db.SetWidgetShareArchived(ctx, shareID(2), true, "", store.AuditEntry{Actor: "api", Action: "widget_share.archive"}); err != nil {
		t.Fatal(err)
	}
	insertShare(t, db, shareID(3), other, wid, "")

	if err := db.DeleteProjectData(ctx, pid, store.AuditEntry{Actor: "test", Action: "project.delete"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListWidgetShares(ctx, store.WidgetShareFilter{Now: shareNow})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{shareID(3)}; !reflect.DeepEqual(shareIDs(got), want) {
		t.Fatalf("shares left = %v, want %v", shareIDs(got), want)
	}
}

func TestPurgeArchivedWidgetShares(t *testing.T) {
	db, pid, wid := shareFixture(t)
	ctx := context.Background()
	oldID, recent, live := shareID(1), shareID(2), shareID(3)
	for _, id := range []string{oldID, recent, live} {
		insertShare(t, db, id, pid, wid, "")
	}
	for id, days := range map[string]int{oldID: 40, recent: 5} {
		if _, err := db.ExecForTest(
			`UPDATE widget_shares SET archived_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now', ?) WHERE id=?`,
			daysAgoModifier(days), id); err != nil {
			t.Fatal(err)
		}
	}
	res, err := db.PurgeArchived(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{oldID}; !reflect.DeepEqual(res.WidgetShares, want) {
		t.Fatalf("purged = %v, want %v", res.WidgetShares, want)
	}
	rows := auditRows(t, db, "widget_share.purge")
	if len(rows) != 1 || rows[0].Actor != "retention" || rows[0].Subject != "widget_share/"+oldID {
		t.Fatalf("audit = %+v", rows)
	}
	for _, id := range []string{recent, live} {
		if _, err := db.GetWidgetShare(ctx, id); err != nil {
			t.Errorf("share %s: %v", id, err)
		}
	}
	if _, err := db.GetWidgetShare(ctx, oldID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("old share = %v, want ErrNotFound", err)
	}
}
