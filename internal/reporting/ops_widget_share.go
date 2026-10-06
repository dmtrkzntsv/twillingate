package reporting

import (
	"bytes"
	"context"
	"image/png"
	"time"

	"github.com/google/uuid"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Widget shares: a frozen picture of one widget, two PNGs and the text
// they show, served publicly by id (spec 2026-10-05, D1, D6, D7).

const (
	shareStamp   = "2006-01-02T15:04:05Z"
	maxShareSize = 5 << 20
)

var archivePeriods = map[string]int{"7d": 7, "30d": 30, "90d": 90, "365d": 365}

// NewShare is what CreateWidgetShare takes: the widget and project the
// picture shows, the days it covers, when it archives, and its two PNGs
// (1200×630 and 2400×1260).
type NewShare struct {
	WidgetID, ProjectID int64
	From, To            string // YYYY-MM-DD
	ArchiveAfter        string // 7d|30d|90d|365d|project; "" = 30d
	Image, Image2x      []byte
}

// WidgetShareOut is a share as the surfaces above report it. ArchivedAt
// also reports a share that is due (archive_at passed, daily pass not yet
// run) as archived, as its page does.
type WidgetShareOut struct {
	ID             string  `json:"id"`
	URL            string  `json:"url"`
	ImageURL       string  `json:"image_url"`
	Image2xURL     string  `json:"image_2x_url"`
	WidgetID       *int64  `json:"widget_id"`
	DashboardID    *int64  `json:"dashboard_id"`
	DashboardTitle *string `json:"dashboard_title"`
	ProjectID      int64   `json:"project_id"`
	ProjectName    string  `json:"project_name"`
	From           string  `json:"from"`
	To             string  `json:"to"`
	Title          string  `json:"title"`
	CreatedAt      string  `json:"created_at"`
	ArchiveAt      *string `json:"archive_at"`
	ArchivedAt     *string `json:"archived_at"`
}

// archiveAtFor turns an archive_after value into archive_at: "" for
// project lifetime ("project"), else now plus the period.
func archiveAtFor(v string, now time.Time) (string, error) {
	if v == "project" {
		return "", nil
	}
	days, ok := archivePeriods[v]
	if !ok {
		return "", store.Refuse(store.ErrInvalid, "archive_after %q: want 7d, 30d, 90d, 365d or project", v)
	}
	return now.UTC().AddDate(0, 0, days).Format(shareStamp), nil
}

// checkPNG refuses anything but a PNG of exactly w×h, at most 5 MB.
func checkPNG(name string, b []byte, w, h int) error {
	if len(b) > maxShareSize {
		return store.Refuse(store.ErrInvalid, "%s: %d bytes; at most 5 MB", name, len(b))
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return store.Refuse(store.ErrInvalid, "%s: not a PNG", name)
	}
	if cfg.Width != w || cfg.Height != h {
		return store.Refuse(store.ErrInvalid, "%s: %d×%d; want %d×%d", name, cfg.Width, cfg.Height, w, h)
	}
	return nil
}

func newShareID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

// CreateWidgetShare stores a share of widget in.WidgetID. Widgets are not
// bound to projects (the project is a dashboard parameter), so the widget
// and the project are checked on their own.
func (s *Service) CreateWidgetShare(ctx context.Context, actor string, in NewShare) (WidgetShareOut, error) {
	if s.shareBase == "" {
		return WidgetShareOut{}, store.Refuse(store.ErrInvalid, "set CONSOLE_URL to share widgets")
	}
	w, err := s.st.GetWidget(ctx, in.WidgetID)
	if err != nil {
		return WidgetShareOut{}, err
	}
	if w.ArchivedAt != "" {
		return WidgetShareOut{}, store.Refuse(store.ErrNotFound, "widget %d: not found", in.WidgetID)
	}
	if err := checkDates(in.From, in.To); err != nil {
		return WidgetShareOut{}, err
	}
	if to, err := civil.Parse(in.To); err != nil || civil.DateOf(s.now().UTC()).Before(to) {
		return WidgetShareOut{}, store.Refuse(store.ErrInvalid, "to %s is after today", in.To)
	}
	if err := checkPNG("image", in.Image, 1200, 630); err != nil {
		return WidgetShareOut{}, err
	}
	if err := checkPNG("image_2x", in.Image2x, 2400, 1260); err != nil {
		return WidgetShareOut{}, err
	}
	archiveAfter := in.ArchiveAfter
	if archiveAfter == "" {
		archiveAfter = "30d"
	}
	archiveAt, err := archiveAtFor(archiveAfter, s.now())
	if err != nil {
		return WidgetShareOut{}, err
	}
	title := w.Title
	if title == "" {
		title = w.Name
	}
	row, err := s.st.InsertWidgetShare(ctx, store.NewWidgetShare{
		ID: newShareID(), WidgetID: in.WidgetID, ProjectID: in.ProjectID,
		From: in.From, To: in.To, Title: title,
		Image: in.Image, Image2x: in.Image2x, ArchiveAt: archiveAt,
	}, store.AuditEntry{Actor: actor, Action: "widget_share.create"})
	if err != nil {
		return WidgetShareOut{}, err
	}
	return s.shareOut(row), nil
}

// ListWidgetShares lists the shares of widgetID (0 = every widget) in
// state "", "live" or "archived".
func (s *Service) ListWidgetShares(ctx context.Context, widgetID int64, state string) ([]WidgetShareOut, error) {
	rows, err := s.st.ListWidgetShares(ctx, store.WidgetShareFilter{
		WidgetID: widgetID, State: state, Now: s.now().UTC().Format(shareStamp),
	})
	if err != nil {
		return nil, err
	}
	out := make([]WidgetShareOut, len(rows))
	for i, r := range rows {
		out[i] = s.shareOut(r)
	}
	return out, nil
}

// UpdateWidgetShare changes when a live share archives. The value is
// required; an archived or due share is a conflict (restore it instead).
func (s *Service) UpdateWidgetShare(ctx context.Context, actor, id, archiveAfter string) (WidgetShareOut, error) {
	if archiveAfter == "" {
		return WidgetShareOut{}, store.Refuse(store.ErrInvalid, "archive_after is required: 7d, 30d, 90d, 365d or project")
	}
	at, err := archiveAtFor(archiveAfter, s.now())
	if err != nil {
		return WidgetShareOut{}, err
	}
	row, err := s.st.SetWidgetShareArchiveAt(ctx, id, at, s.now().UTC().Format(shareStamp),
		store.AuditEntry{Actor: actor, Action: "widget_share.update"})
	if err != nil {
		return WidgetShareOut{}, err
	}
	return s.shareOut(row), nil
}

// WidgetShareImage is a share's 1x PNG in any state, live, due or archived,
// for the console's own pages: unlike /share/<id>.png, which answers 404 once
// a share is archived. An unknown id is ErrNotFound.
func (s *Service) WidgetShareImage(ctx context.Context, id string) ([]byte, error) {
	return s.st.WidgetShareImage(ctx, id, false)
}

// ArchiveWidgetShare archives a share now; archiving one again is a no-op.
func (s *Service) ArchiveWidgetShare(ctx context.Context, actor, id string) (WidgetShareOut, error) {
	row, err := s.st.SetWidgetShareArchived(ctx, id, true, "",
		store.AuditEntry{Actor: actor, Action: "widget_share.archive"})
	if err != nil {
		return WidgetShareOut{}, err
	}
	return s.shareOut(row), nil
}

// RestoreWidgetShare makes a share live again and sets when it archives
// next (default 30d). A share that is already live just gets the date.
func (s *Service) RestoreWidgetShare(ctx context.Context, actor, id, archiveAfter string) (WidgetShareOut, error) {
	if archiveAfter == "" {
		archiveAfter = "30d"
	}
	at, err := archiveAtFor(archiveAfter, s.now())
	if err != nil {
		return WidgetShareOut{}, err
	}
	row, err := s.st.SetWidgetShareArchived(ctx, id, false, at,
		store.AuditEntry{Actor: actor, Action: "widget_share.restore"})
	if err != nil {
		return WidgetShareOut{}, err
	}
	return s.shareOut(row), nil
}

// LiveWidgetShare reads a share and whether it is live: not archived and
// not past its archive_at. A due share is not live even before the daily
// pass has archived it.
func (s *Service) LiveWidgetShare(ctx context.Context, id string) (store.WidgetShare, bool, error) {
	row, err := s.st.GetWidgetShare(ctx, id)
	if err != nil {
		return store.WidgetShare{}, false, err
	}
	return row, shareLive(row, s.now()), nil
}

func shareLive(r store.WidgetShare, now time.Time) bool {
	return r.ArchivedAt == "" && (r.ArchiveAt == "" || r.ArchiveAt > now.UTC().Format(shareStamp))
}

func (s *Service) shareOut(r store.WidgetShare) WidgetShareOut {
	url := s.shareBase + "/share/" + r.ID
	out := WidgetShareOut{
		ID: r.ID, URL: url, ImageURL: url + ".png", Image2xURL: url + "@2x.png",
		ProjectID: r.ProjectID, ProjectName: r.ProjectName,
		From: r.From, To: r.To, Title: r.Title, CreatedAt: r.CreatedAt,
	}
	if r.WidgetID != 0 {
		out.WidgetID = &r.WidgetID
	}
	if r.DashboardID != 0 {
		out.DashboardID = &r.DashboardID
	}
	if r.DashboardTitle != "" {
		out.DashboardTitle = &r.DashboardTitle
	}
	if r.ArchiveAt != "" {
		out.ArchiveAt = &r.ArchiveAt
	}
	archived := r.ArchivedAt
	if archived == "" && r.ArchiveAt != "" && r.ArchiveAt <= s.now().UTC().Format(shareStamp) {
		archived = r.ArchiveAt
	}
	if archived != "" {
		out.ArchivedAt = &archived
	}
	return out
}
