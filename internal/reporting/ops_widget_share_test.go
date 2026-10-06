package reporting

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type shareEnv struct {
	svc       *Service
	st        store.Store
	now       *time.Time
	widgetID  int64
	projectID int64
}

func newShareEnv(t *testing.T) *shareEnv {
	t.Helper()
	e := &shareEnv{now: new(time.Time)}
	*e.now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	e.svc, e.st = newTestServiceOpts(t, Options{
		ShareBaseURL: "https://c.example", ArchivedDays: 30,
		Now: func() time.Time { return *e.now },
	}, 1000)
	e.projectID = mustCreateProject(t, e.st, "Acme")
	e.widgetID = mustCreate(t, e.svc, "Board", note("Visits")).Widgets[0].ID
	return e
}

func (e *shareEnv) input(t *testing.T) NewShare {
	t.Helper()
	return NewShare{
		WidgetID: e.widgetID, ProjectID: e.projectID,
		From: "2026-09-01", To: "2026-09-30",
		Image: pngOf(t, 1200, 630), Image2x: pngOf(t, 2400, 1260),
	}
}

func (e *shareEnv) create(t *testing.T, archiveAfter string) WidgetShareOut {
	t.Helper()
	in := e.input(t)
	in.ArchiveAfter = archiveAfter
	out, err := e.svc.CreateWidgetShare(context.Background(), "test", in)
	if err != nil {
		t.Fatalf("CreateWidgetShare: %v", err)
	}
	return out
}

func strOf(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestCreateWidgetShare(t *testing.T) {
	e := newShareEnv(t)
	got := e.create(t, "")
	id, err := uuid.Parse(got.ID)
	if err != nil || id.Version() != 7 {
		t.Fatalf("id %q: err %v, version %v; want a v7 uuid", got.ID, err, id.Version())
	}
	if want := "https://c.example/share/" + got.ID; got.URL != want {
		t.Errorf("URL = %q, want %q", got.URL, want)
	}
	if want := "https://c.example/share/" + got.ID + ".png"; got.ImageURL != want {
		t.Errorf("ImageURL = %q, want %q", got.ImageURL, want)
	}
	if want := "https://c.example/share/" + got.ID + "@2x.png"; got.Image2xURL != want {
		t.Errorf("Image2xURL = %q, want %q", got.Image2xURL, want)
	}
	if strOf(got.ArchiveAt) != "2026-11-05T12:00:00Z" {
		t.Errorf("ArchiveAt = %s, want 2026-11-05T12:00:00Z", strOf(got.ArchiveAt))
	}
	if got.ArchivedAt != nil {
		t.Errorf("ArchivedAt = %s, want nil", strOf(got.ArchivedAt))
	}
	if got.WidgetID == nil || *got.WidgetID != e.widgetID || got.DashboardID == nil || strOf(got.DashboardTitle) != "Board" {
		t.Errorf("widget/dashboard = %v/%v/%s", got.WidgetID, got.DashboardID, strOf(got.DashboardTitle))
	}
	if got.ProjectID != e.projectID || got.ProjectName != "Acme" || got.From != "2026-09-01" || got.To != "2026-09-30" {
		t.Errorf("project/range = %d %q %s %s", got.ProjectID, got.ProjectName, got.From, got.To)
	}
	if got.Title != "Visits" {
		t.Errorf("Title = %q, want Visits", got.Title)
	}
	if got.CreatedAt == "" {
		t.Error("CreatedAt empty")
	}
	img, err := e.st.WidgetShareImage(context.Background(), got.ID, true)
	if err != nil || len(img) == 0 {
		t.Errorf("stored 2x image: %d bytes, %v", len(img), err)
	}
}

func TestCreateWidgetShareTitleFallsBackToName(t *testing.T) {
	e := newShareEnv(t)
	d := mustCreate(t, e.svc, "Untitled board", WidgetSpec{Component: "markdown", Source: md("x")})
	in := e.input(t)
	in.WidgetID = d.Widgets[0].ID
	got, err := e.svc.CreateWidgetShare(context.Background(), "test", in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title == "" || got.Title != d.Widgets[0].Name {
		t.Errorf("Title = %q, want the widget name %q", got.Title, d.Widgets[0].Name)
	}
}

func TestCreateWidgetShareArchiveAfter(t *testing.T) {
	e := newShareEnv(t)
	for _, tc := range []struct{ in, want string }{
		{"7d", "2026-10-13T12:00:00Z"},
		{"30d", "2026-11-05T12:00:00Z"},
		{"90d", "2027-01-04T12:00:00Z"},
		{"365d", "2027-10-06T12:00:00Z"},
		{"project", "<nil>"},
	} {
		got := e.create(t, tc.in)
		if strOf(got.ArchiveAt) != tc.want {
			t.Errorf("archive_after %q: ArchiveAt = %s, want %s", tc.in, strOf(got.ArchiveAt), tc.want)
		}
	}
	in := e.input(t)
	in.ArchiveAfter = "2w"
	if _, err := e.svc.CreateWidgetShare(context.Background(), "test", in); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("archive_after 2w: err = %v, want ErrInvalid", err)
	}
}

func TestCreateWidgetShareRefusals(t *testing.T) {
	jpg := func(w, h int) []byte {
		var b bytes.Buffer
		if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 5<<20)...)

	archived := func(e *shareEnv) {
		if err := e.svc.ArchiveWidget(context.Background(), "test", e.widgetID); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		mut  func(t *testing.T, e *shareEnv, in *NewShare)
		want error
		msg  string
	}{
		{"no base url", func(_ *testing.T, e *shareEnv, _ *NewShare) { e.svc.shareBase = "" }, store.ErrInvalid, "CONSOLE_URL"},
		{"unknown widget", func(_ *testing.T, _ *shareEnv, in *NewShare) { in.WidgetID = 99999 }, store.ErrNotFound, ""},
		{"archived widget", func(_ *testing.T, e *shareEnv, _ *NewShare) { archived(e) }, store.ErrNotFound, ""},
		{"jpeg image", func(_ *testing.T, _ *shareEnv, in *NewShare) { in.Image = jpg(1200, 630) }, store.ErrInvalid, ""},
		{"text image", func(_ *testing.T, _ *shareEnv, in *NewShare) { in.Image = []byte("hello") }, store.ErrInvalid, ""},
		{"image 1199 wide", func(t *testing.T, _ *shareEnv, in *NewShare) { in.Image = pngOf(t, 1199, 630) }, store.ErrInvalid, ""},
		{"image 2x 1259 high", func(t *testing.T, _ *shareEnv, in *NewShare) { in.Image2x = pngOf(t, 2400, 1259) }, store.ErrInvalid, ""},
		{"parts swapped", func(_ *testing.T, _ *shareEnv, in *NewShare) { in.Image, in.Image2x = in.Image2x, in.Image }, store.ErrInvalid, ""},
		{"image over 5 MB", func(_ *testing.T, _ *shareEnv, in *NewShare) { in.Image = big }, store.ErrInvalid, ""},
		{"image 2x over 5 MB", func(_ *testing.T, _ *shareEnv, in *NewShare) { in.Image2x = big }, store.ErrInvalid, ""},
		{"bad month", func(_ *testing.T, _ *shareEnv, in *NewShare) { in.From = "2026-13-01" }, store.ErrInvalid, ""},
		{"from after to", func(_ *testing.T, _ *shareEnv, in *NewShare) { in.From, in.To = "2026-09-30", "2026-09-01" }, store.ErrInvalid, ""},
		{"span over a year", func(_ *testing.T, _ *shareEnv, in *NewShare) { in.From, in.To = "2025-01-01", "2026-10-06" }, store.ErrInvalid, ""},
		{"to is tomorrow", func(_ *testing.T, _ *shareEnv, in *NewShare) { in.To = "2026-10-07" }, store.ErrInvalid, ""},
		{"unknown project", func(_ *testing.T, _ *shareEnv, in *NewShare) { in.ProjectID = 99999 }, store.ErrNotFound, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newShareEnv(t)
			in := e.input(t)
			tc.mut(t, e, &in)
			_, err := e.svc.CreateWidgetShare(context.Background(), "test", in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.msg != "" && !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("err = %q, want it to mention %q", err, tc.msg)
			}
			list, lerr := e.svc.ListWidgetShares(context.Background(), 0, "")
			if lerr != nil || len(list) != 0 {
				t.Errorf("after a refusal: %d shares, %v; want none", len(list), lerr)
			}
		})
	}
}

func TestCreateWidgetShareToday(t *testing.T) {
	e := newShareEnv(t)
	in := e.input(t)
	in.To = "2026-10-06"
	if _, err := e.svc.CreateWidgetShare(context.Background(), "test", in); err != nil {
		t.Errorf("to = today: %v", err)
	}
}

func TestCreateWidgetShareSystemWidget(t *testing.T) {
	e := newShareEnv(t)
	syncReporting(t, e.svc, nil, store.SystemDashboard{
		ID: 1, Title: "Overview", SortKey: "a0", Range: "7d",
		Widgets: []store.Widget{{
			Name: "note", Component: "markdown", SortKey: "a0", Width: 12, Height: 2,
			Props: "{}", SourceType: "md", Source: "system text",
		}},
	})
	ws, err := e.st.ListWidgets(context.Background(), 1)
	if err != nil || len(ws) != 1 {
		t.Fatalf("system widgets: %v, %v", ws, err)
	}
	in := e.input(t)
	in.WidgetID = ws[0].ID
	got, err := e.svc.CreateWidgetShare(context.Background(), "test", in)
	if err != nil {
		t.Fatalf("share a system widget: %v", err)
	}
	if got.DashboardID == nil || *got.DashboardID != 1 {
		t.Errorf("DashboardID = %v, want 1", got.DashboardID)
	}
}

func TestUpdateWidgetShare(t *testing.T) {
	ctx := context.Background()
	e := newShareEnv(t)
	s := e.create(t, "")

	got, err := e.svc.UpdateWidgetShare(ctx, "test", s.ID, "90d")
	if err != nil || strOf(got.ArchiveAt) != "2027-01-04T12:00:00Z" {
		t.Errorf("90d: %s, %v", strOf(got.ArchiveAt), err)
	}
	got, err = e.svc.UpdateWidgetShare(ctx, "test", s.ID, "project")
	if err != nil || got.ArchiveAt != nil {
		t.Errorf("project: %s, %v", strOf(got.ArchiveAt), err)
	}
	if _, err := e.svc.UpdateWidgetShare(ctx, "test", s.ID, ""); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("empty archive_after: err = %v, want ErrInvalid", err)
	}
	if _, err := e.svc.UpdateWidgetShare(ctx, "test", s.ID, "2w"); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("2w: err = %v, want ErrInvalid", err)
	}
	if _, err := e.svc.UpdateWidgetShare(ctx, "test", uuid.NewString(), "7d"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}

	if _, err := e.svc.ArchiveWidgetShare(ctx, "test", s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UpdateWidgetShare(ctx, "test", s.ID, "7d"); !errors.Is(err, store.ErrConflict) {
		t.Errorf("archived share: err = %v, want ErrConflict", err)
	}

	// Due but not yet archived by the daily pass: also a conflict.
	due := e.create(t, "7d")
	*e.now = e.now.AddDate(0, 0, 8)
	if _, err := e.svc.UpdateWidgetShare(ctx, "test", due.ID, "30d"); !errors.Is(err, store.ErrConflict) {
		t.Errorf("due share: err = %v, want ErrConflict", err)
	}
}

func TestArchiveRestoreWidgetShare(t *testing.T) {
	ctx := context.Background()
	e := newShareEnv(t)
	s := e.create(t, "7d")

	got, err := e.svc.ArchiveWidgetShare(ctx, "test", s.ID)
	if err != nil || got.ArchivedAt == nil {
		t.Fatalf("archive: %v, %v", got.ArchivedAt, err)
	}
	got, err = e.svc.RestoreWidgetShare(ctx, "test", s.ID, "")
	if err != nil || got.ArchivedAt != nil || strOf(got.ArchiveAt) != "2026-11-05T12:00:00Z" {
		t.Errorf("restore default: archived %s, archive_at %s, %v", strOf(got.ArchivedAt), strOf(got.ArchiveAt), err)
	}
	if _, err := e.svc.ArchiveWidgetShare(ctx, "test", s.ID); err != nil {
		t.Fatal(err)
	}
	got, err = e.svc.RestoreWidgetShare(ctx, "test", s.ID, "project")
	if err != nil || got.ArchivedAt != nil || got.ArchiveAt != nil {
		t.Errorf("restore project: archived %s, archive_at %s, %v", strOf(got.ArchivedAt), strOf(got.ArchiveAt), err)
	}
	// A live share can be restored too: it only sets the new date.
	got, err = e.svc.RestoreWidgetShare(ctx, "test", s.ID, "7d")
	if err != nil || strOf(got.ArchiveAt) != "2026-10-13T12:00:00Z" {
		t.Errorf("restore live: %s, %v", strOf(got.ArchiveAt), err)
	}
	if _, err := e.svc.RestoreWidgetShare(ctx, "test", s.ID, "2w"); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("restore 2w: err = %v, want ErrInvalid", err)
	}
	if _, err := e.svc.ArchiveWidgetShare(ctx, "test", uuid.NewString()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("archive unknown: err = %v, want ErrNotFound", err)
	}
}

func TestListWidgetSharesReportsDueAsArchived(t *testing.T) {
	ctx := context.Background()
	e := newShareEnv(t)
	due := e.create(t, "7d")
	keep := e.create(t, "project")
	*e.now = e.now.AddDate(0, 0, 8)

	live, err := e.svc.ListWidgetShares(ctx, 0, "live")
	if err != nil || len(live) != 1 || live[0].ID != keep.ID {
		t.Fatalf("live = %+v, %v; want only %s", live, err, keep.ID)
	}
	gone, err := e.svc.ListWidgetShares(ctx, 0, "archived")
	if err != nil || len(gone) != 1 || gone[0].ID != due.ID {
		t.Fatalf("archived = %+v, %v; want only %s", gone, err, due.ID)
	}
	if strOf(gone[0].ArchivedAt) != "2026-10-13T12:00:00Z" || strOf(gone[0].ArchivedAt) != strOf(gone[0].ArchiveAt) {
		t.Errorf("ArchivedAt = %s, want its archive_at 2026-10-13T12:00:00Z", strOf(gone[0].ArchivedAt))
	}
	all, err := e.svc.ListWidgetShares(ctx, e.widgetID, "")
	if err != nil || len(all) != 2 {
		t.Errorf("all for the widget = %d, %v; want 2", len(all), err)
	}
	other, err := e.svc.ListWidgetShares(ctx, e.widgetID+100, "")
	if err != nil || len(other) != 0 {
		t.Errorf("other widget = %d, %v; want 0", len(other), err)
	}
	if _, err := e.svc.ListWidgetShares(ctx, 0, "x"); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("state x: err = %v, want ErrInvalid", err)
	}
}

func TestLiveWidgetShare(t *testing.T) {
	ctx := context.Background()
	e := newShareEnv(t)
	s := e.create(t, "7d")
	row, live, err := e.svc.LiveWidgetShare(ctx, s.ID)
	if err != nil || !live || row.ID != s.ID {
		t.Fatalf("fresh share: %+v live=%v err=%v", row, live, err)
	}
	*e.now = e.now.AddDate(0, 0, 8)
	if _, live, err = e.svc.LiveWidgetShare(ctx, s.ID); err != nil || live {
		t.Errorf("due share: live=%v err=%v; want not live", live, err)
	}
	p := e.create(t, "project")
	if _, live, err = e.svc.LiveWidgetShare(ctx, p.ID); err != nil || !live {
		t.Errorf("project-lifetime share: live=%v err=%v; want live", live, err)
	}
	if _, err := e.svc.ArchiveWidgetShare(ctx, "test", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, live, err = e.svc.LiveWidgetShare(ctx, p.ID); err != nil || live {
		t.Errorf("archived share: live=%v err=%v; want not live", live, err)
	}
	if _, _, err = e.svc.LiveWidgetShare(ctx, uuid.NewString()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
}

func TestWidgetShareOutAfterWidgetDeleted(t *testing.T) {
	e := newShareEnv(t)
	s := e.create(t, "")
	rawExec(t, e.st, `DELETE FROM widgets WHERE id = ?`, e.widgetID)
	got, err := e.svc.ListWidgetShares(context.Background(), 0, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if got[0].ID != s.ID || got[0].WidgetID != nil || got[0].DashboardID != nil || got[0].DashboardTitle != nil {
		t.Errorf("after delete: widget %v dashboard %v title %v; want all nil",
			got[0].WidgetID, got[0].DashboardID, got[0].DashboardTitle)
	}
}
