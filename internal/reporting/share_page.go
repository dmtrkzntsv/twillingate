package reporting

import (
	"bytes"
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The public face of a widget share: GET /share/{file} is the page for
// <id>, its 1x image for <id>.png and its 2x image for <id>@2x.png. The
// page runs no script, so a title, which is whatever the widget's author
// typed, only ever reaches it through html/template's escaping.

//go:embed share_page.html
var sharePageSrc string

var sharePage = template.Must(template.New("share").Parse(sharePageSrc))

// The page for a share that is not there: the same frame as a live one,
// the picture's place taken by a drawing. It names no id and says the
// same for unknown, archived and due shares and for a malformed link, so
// it never tells a stranger that an id once existed.
//
//go:embed share_missing.html
var shareMissingSrc string

var shareMissing = func() []byte {
	var b bytes.Buffer
	t := template.Must(template.New("missing").Parse(shareMissingSrc))
	if err := t.Execute(&b, struct{ Icon template.HTML }{template.HTML(shareIcon)}); err != nil { //nolint:gosec // a constant
		panic("reporting: share_missing.html: " + err.Error())
	}
	return b.Bytes()
}()

const shareCSP = "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// shareIcon is the iceberg tile of the OAuth page (internal/api/oauth_page.html),
// copied because reporting cannot import api.
const shareIcon = `<svg viewBox="0 0 30 30" aria-hidden="true"><path d="M0 0H30V30H0Z" fill="#0A4D8C"/><path d="M0 0H30V13H0Z" fill="#8FD3FF"/><path d="M5.5 13L10.2 9.1L12.2 10.2L15.5 4L18 8.1L20 7L24.5 13H5.5Z" fill="#F8FCFF"/><path d="M12.2 10.2L15.5 4L16.3 10L14.4 13H9.2L12.2 10.2Z" fill="#D5F1FF"/><path d="M15.5 4L18 8.1L16.3 10Z" fill="#2E9FE5"/><path d="M18 8.1L20 7L22 13H16.3Z" fill="#62C3F3"/><path d="M5.5 13H24.5L20 20.5L15 27L10 20.5L5.5 13Z" fill="#1478C9"/><path d="M5.5 13L12.5 16L10 20.5Z" fill="#54C8F7"/><path d="M12.5 16L15 27L10 20.5Z" fill="#249FE3"/><path d="M12.5 16L17 13L20 20.5L15 27Z" fill="#0D61AD"/><path d="M17 13H24.5L20 20.5Z" fill="#073F7C"/></svg>`

// sharePageData: ProjectName is "" when the share does not caption the
// project, Meta is the caption line ("" for none), Alt the image's text.
type sharePageData struct {
	Title, ProjectName, Meta string
	URL, ImageURL            string
	Image2xPath, Alt         string
	Icon                     template.HTML
}

// SharePages serves a live share's page and images, and 404 for anything
// else: a malformed path, an unknown, archived or due share. A page's 404
// is shareMissing; an image's is plain text, which no one reads.
func (s *Service) SharePages() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, kind := r.PathValue("file"), "page"
		if v, ok := strings.CutSuffix(id, "@2x.png"); ok {
			id, kind = v, "2x"
		} else if v, ok := strings.CutSuffix(id, ".png"); ok {
			id, kind = v, "1x"
		}
		// Only the canonical lower-case form: anything else cannot be an id.
		page := kind == "page"
		if u, err := uuid.Parse(id); err != nil || u.String() != id {
			shareNotFound(w, page)
			return
		}
		sh, live, err := s.LiveWidgetShare(r.Context(), id)
		if err != nil || !live {
			shareFailed(w, err, page)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !page {
			b, err := s.shares.image(r.Context(), id, kind == "2x", s.st.WidgetShareImage)
			if err != nil {
				shareFailed(w, err, false)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("X-Robots-Tag", "noindex")
			_, _ = w.Write(b)
			return
		}
		// The captions name only what the widget follows (NewShare): a
		// pinned widget's picture does not show the dashboard's project or
		// range, so neither may its text.
		var caption []string
		project := ""
		if sh.CaptionProject {
			project = sh.ProjectName
			caption = append(caption, project)
		}
		if sh.CaptionRange {
			caption = append(caption, rangeInWords(sh.From, sh.To))
		}
		url := s.shareBase + "/share/" + sh.ID
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Content-Security-Policy", shareCSP)
		_ = sharePage.Execute(w, sharePageData{
			Title: sh.Title, ProjectName: project, Meta: strings.Join(caption, " · "),
			URL: url, ImageURL: url + ".png",
			Image2xPath: "/share/" + sh.ID + "@2x.png",
			Alt:         strings.Join(append([]string{sh.Title}, caption...), ", "),
			Icon:        template.HTML(shareIcon), //nolint:gosec // a constant
		})
	})
}

// shareNotFound answers 404: shareMissing for a page, plain text for an
// image.
func shareNotFound(w http.ResponseWriter, page bool) {
	w.Header().Set("Cache-Control", "no-store")
	if !page {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", shareCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(shareMissing)
}

// shareFailed answers a failed read: 404 for a share that is not there
// (err nil means it is there but not live), 503 for anything else, a
// busy or broken store, whose text is not for strangers.
func shareFailed(w http.ResponseWriter, err error, page bool) {
	if err == nil || errors.Is(err, store.ErrNotFound) {
		shareNotFound(w, page)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
}

// rangeInWords writes two YYYY-MM-DD days as "Sep 5 – Oct 4, 2026", with
// the year on both ends only when they differ and one day alone when they
// are the same. A value that is not a date is written as it is.
func rangeInWords(from, to string) string {
	f, err1 := civil.Parse(from)
	t, err2 := civil.Parse(to)
	if err1 != nil || err2 != nil {
		return from + " – " + to
	}
	ft, tt := f.Time(), t.Time()
	switch {
	case from == to:
		return tt.Format("Jan 2, 2006")
	case ft.Year() == tt.Year():
		return ft.Format("Jan 2") + " – " + tt.Format("Jan 2, 2006")
	default:
		return ft.Format("Jan 2, 2006") + " – " + tt.Format("Jan 2, 2006")
	}
}
