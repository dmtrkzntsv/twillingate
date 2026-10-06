package api

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Widget shares (spec 2026-10-05, D6): a frozen picture of one widget,
// public at /share/<id>. Create is REST only, since only a browser can
// capture the picture; the rest are thin adapters, like the other
// reporting tools.

const maxShareUpload = 12 << 20 // two 5 MB images and the fields

type createWidgetShareIn struct {
	WidgetID     int64  `json:"widget_id" jsonschema:"the widget to share"`
	ProjectID    int64  `json:"project_id" jsonschema:"the project the picture shows"`
	From         string `json:"from" jsonschema:"range start, YYYY-MM-DD"`
	To           string `json:"to" jsonschema:"range end, YYYY-MM-DD, not in the future"`
	ArchiveAfter string `json:"archive_after,omitempty" jsonschema:"7d, 30d, 90d, 365d or project; default 30d"`
	Image        string `json:"image" jsonschema:"1200×630 PNG file, at most 5 MB"`
	Image2x      string `json:"image_2x" jsonschema:"2400×1260 PNG file, at most 5 MB"`
	// Strings, as multipart fields are: 1, 0, true or false.
	CaptionProject string `json:"caption_project,omitempty" jsonschema:"whether the share page names the project: 1 (default) or 0; 0 when the widget does not follow the dashboard's project"`
	CaptionRange   string `json:"caption_range,omitempty" jsonschema:"whether the share page names the range: 1 (default) or 0; 0 when the widget does not follow the dashboard's range"`
}

type widgetSharesIn struct {
	WidgetID int64  `json:"widget_id,omitempty" jsonschema:"only this widget's shares; omit for every widget"`
	State    string `json:"state,omitempty" jsonschema:"live or archived; omit for both"`
}

type widgetSharesOut struct {
	Shares []reporting.WidgetShareOut `json:"shares"`
}

type widgetShareIn struct {
	ID string `json:"id" jsonschema:"share id (a UUID)"`
}

func (h *host) widgetShareImage(ctx context.Context, in widgetShareIn) ([]byte, error) {
	return h.rep.WidgetShareImage(ctx, in.ID)
}

type widgetShareDateIn struct {
	ID           string `json:"id" jsonschema:"share id (a UUID)"`
	ArchiveAfter string `json:"archive_after" jsonschema:"7d, 30d, 90d, 365d or project (no date: the share lives as long as its project)"`
}

type widgetShareRestoreIn struct {
	ID           string `json:"id" jsonschema:"share id (a UUID)"`
	ArchiveAfter string `json:"archive_after,omitempty" jsonschema:"7d, 30d, 90d, 365d or project; default 30d"`
}

// shareTextParts and shareFileParts are what create_widget_share's
// multipart body may hold; anything else is refused, as decodeRequest
// refuses an unknown field.
var (
	shareTextParts = map[string]bool{"widget_id": true, "project_id": true, "from": true, "to": true, "archive_after": true,
		"caption_project": true, "caption_range": true}
	shareFileParts = map[string]bool{"image": true, "image_2x": true}
)

// createWidgetShare reads the multipart upload and stores the share as
// actor "api". Every malformed body is a 400 invalid, never a 500.
func (h *host) createWidgetShare(w http.ResponseWriter, r *http.Request) {
	in, err := readShareUpload(w, r)
	if err != nil {
		writeError(w, h.logger, r, err)
		return
	}
	out, err := h.rep.CreateWidgetShare(r.Context(), "api", in)
	if err != nil {
		writeError(w, h.logger, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func readShareUpload(w http.ResponseWriter, r *http.Request) (reporting.NewShare, error) {
	const want = "expected multipart/form-data with widget_id, project_id, from, to, image and image_2x"
	if r.URL.RawQuery != "" {
		return reporting.NewShare{}, invalidf("POST takes a multipart body, not query parameters")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxShareUpload)
	if err := r.ParseMultipartForm(maxShareUpload); err != nil {
		return reporting.NewShare{}, invalidf("%s: %v", want, err)
	}
	defer r.MultipartForm.RemoveAll()
	form := r.MultipartForm
	for name := range form.Value {
		if !shareTextParts[name] {
			return reporting.NewShare{}, invalidf("unknown field %q; valid: %s", name, shareFieldNames())
		}
	}
	for name := range form.File {
		if !shareFileParts[name] {
			return reporting.NewShare{}, invalidf("unknown file %q; valid: image, image_2x", name)
		}
	}
	var in reporting.NewShare
	for name, dst := range map[string]*int64{"widget_id": &in.WidgetID, "project_id": &in.ProjectID} {
		n, err := strconv.ParseInt(r.FormValue(name), 10, 64)
		if err != nil {
			return reporting.NewShare{}, invalidf("%s must be an integer, got %q", name, r.FormValue(name))
		}
		*dst = n
	}
	in.From, in.To, in.ArchiveAfter = r.FormValue("from"), r.FormValue("to"), r.FormValue("archive_after")
	for name, dst := range map[string]**bool{"caption_project": &in.CaptionProject, "caption_range": &in.CaptionRange} {
		v, ok := form.Value[name]
		if !ok {
			continue // nil: captioned
		}
		var b bool
		switch v[0] {
		case "1", "true":
			b = true
		case "0", "false":
		default:
			return reporting.NewShare{}, invalidf("%s must be 1, 0, true or false, got %q", name, v[0])
		}
		*dst = &b
	}
	for name, dst := range map[string]*[]byte{"image": &in.Image, "image_2x": &in.Image2x} {
		f, _, err := r.FormFile(name)
		if err != nil {
			return reporting.NewShare{}, invalidf("%s is required: a PNG file", name)
		}
		// One byte past the limit is enough for the service to refuse it.
		b, err := io.ReadAll(io.LimitReader(f, 5<<20+1))
		f.Close()
		if err != nil {
			return reporting.NewShare{}, invalidf("reading %s: %v", name, err)
		}
		*dst = b
	}
	return in, nil
}

func shareFieldNames() string {
	names := make([]string, 0, len(shareTextParts)+len(shareFileParts))
	for k := range shareTextParts {
		names = append(names, k)
	}
	for k := range shareFileParts {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (h *host) listWidgetShares(ctx context.Context, in widgetSharesIn) (widgetSharesOut, error) {
	shares, err := h.rep.ListWidgetShares(ctx, in.WidgetID, in.State)
	return widgetSharesOut{Shares: shares}, err
}

func (h *host) updateWidgetShare(ctx context.Context, in widgetShareDateIn) (reporting.WidgetShareOut, error) {
	return h.rep.UpdateWidgetShare(ctx, actorFrom(ctx), in.ID, in.ArchiveAfter)
}

func (h *host) archiveWidgetShare(ctx context.Context, in widgetShareIn) (reporting.WidgetShareOut, error) {
	return h.rep.ArchiveWidgetShare(ctx, actorFrom(ctx), in.ID)
}

func (h *host) restoreWidgetShare(ctx context.Context, in widgetShareRestoreIn) (reporting.WidgetShareOut, error) {
	return h.rep.RestoreWidgetShare(ctx, actorFrom(ctx), in.ID, in.ArchiveAfter)
}

// registerWidgetShares exposes the share operations: create as a REST-only
// multipart upload, the image as a REST-only PNG, the rest on both
// transports.
func (h *host) registerWidgetShares(r *registrar) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	no := false
	idem := &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}
	const p = "/api/widget-shares"

	restRaw[createWidgetShareIn, reporting.WidgetShareOut](r, spec{Name: "create_widget_share", Method: "POST", Path: p, Status: http.StatusCreated,
		Description: "Share a widget: store the two PNGs the web app captured of it (image 1200×630, image_2x 2400×1260, each at most 5 MB) as a public, frozen picture at /share/<id>, with the widget's title, the project and the range they show. multipart/form-data. caption_project and caption_range (1 or 0, default 1) say whether the page names the project and the range; the web sends 0 for what the widget does not follow. archive_after (7d, 30d, 90d, 365d or project; default 30d) sets when the share archives itself; project gives it no date, so it lives as long as its project. Needs CONSOLE_URL (or PUBLIC_URL) for its links. REST only: an agent has no browser to capture with."},
		h.createWidgetShare)
	expose(r, spec{Name: "list_widget_shares", Annotations: ro, Method: "GET", Path: p,
		Description: "Widget shares, newest first: each one's id, its public page url (/share/<id>) and image URLs, the widget and dashboard it came from (null once the widget is gone), the project, range and title it shows, created_at, archive_at (when it archives itself; null: it lives as long as its project) and archived_at (null: live). Filter by widget_id, and by state: live, or archived (which includes a share whose archive_at has passed and that the daily pass has not yet archived)."},
		h.listWidgetShares)
	restImage(r, spec{Name: "widget_share_image", Method: "GET", Path: p + "/{id}/image",
		Description: "A share's 1200×630 picture as image/png, whatever its state: unlike the public /share/<id>.png, it still answers once the share is archived. Authenticated, so the console's Archive page can show it. REST only: it is a picture, not data."},
		h.widgetShareImage)
	expose(r, spec{Name: "update_widget_share", Annotations: idem, Method: "PATCH", Path: p + "/{id}",
		Description: "Change when a live share archives itself: archive_after is 7d, 30d, 90d or 365d from now, or project for no date (it then lives as long as its project). An archived share is a conflict: restore_widget_share it instead. The picture itself never changes; a new one is a new share."},
		h.updateWidgetShare)
	expose(r, spec{Name: "archive_widget_share", Annotations: idem, Method: "POST", Path: p + "/{id}/archive",
		Description: "Take a share down now: its page and images answer 404. Feeds that already unfurled the link keep their copy of the image. Reversible with restore_widget_share; purged RETENTION_ARCHIVED_DAYS (default 30) after archiving unless restored."},
		h.archiveWidgetShare)
	expose(r, spec{Name: "restore_widget_share", Annotations: idem, Method: "POST", Path: p + "/{id}/restore",
		Description: "Put an archived share back up at its old URL, with a new archive date: archive_after is 7d, 30d, 90d or 365d from now, or project (no date); default 30d, since the old date has usually passed."},
		h.restoreWidgetShare)
}
