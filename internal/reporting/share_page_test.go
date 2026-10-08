package reporting

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

const nastyTitle = `<script>alert(1)</script> "q" & co`

// sharePagesEnv is a shareEnv whose widget has the nastiest title and whose
// project is blog, with the public handler mounted as the console mounts it.
func sharePagesEnv(t *testing.T) (*shareEnv, http.Handler) {
	t.Helper()
	e := &shareEnv{now: new(time.Time)}
	*e.now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	e.svc, e.st = newTestServiceOpts(t, Options{
		ShareBaseURL: "https://c.example", ArchivedDays: 30,
		Now: func() time.Time { return *e.now },
	}, 1000)
	e.projectID = mustCreateProject(t, e.st, "blog")
	e.widgetID = mustCreate(t, e.svc, "Board", note(nastyTitle)).Widgets[0].ID
	mux := http.NewServeMux()
	mux.Handle("GET /share/{file}", e.svc.SharePages())
	return e, mux
}

func getShare(h http.Handler, file string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/share/"+file, nil))
	return rec
}

func TestSharePage(t *testing.T) {
	e, h := sharePagesEnv(t)
	sh := e.create(t, "")
	rec := getShare(h, sh.ID)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	for k, want := range map[string]string{
		"Content-Type":            "text/html; charset=utf-8",
		"Cache-Control":           "public, max-age=300",
		"Content-Security-Policy": "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	body := rec.Body.String()
	esc := `&lt;script&gt;alert(1)&lt;/script&gt; &#34;q&#34; &amp; co`
	for _, want := range []string{
		`<meta property="og:image" content="https://c.example/share/` + sh.ID + `.png">`,
		`<meta property="og:image:width" content="1200">`,
		`<meta property="og:image:height" content="630">`,
		`<meta property="og:url" content="https://c.example/share/` + sh.ID + `">`,
		`<meta property="og:title" content="` + esc + `">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<meta name="robots" content="noindex">`,
		`src="/share/` + sh.ID + `@2x.png"`,
		`width="1200"`, `height="630"`,
		"Sep 1 – Sep 30, 2026",
		`<a href="https://twillingate.dev" rel="noopener">twillingate.dev</a>`,
		"blog",
		// The picture holds the title and caption; the page names them to a screen reader only.
		`<h1 class="sr">` + esc + `</h1>`,
		`<a class="pic" href="/share/` + sh.ID + `@2x.png"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	// The store stamps created_at itself, on the real clock.
	if !regexp.MustCompile(`Snapshot taken <time datetime="\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ">[A-Z][a-z]{2} \d{1,2}, \d{4}</time>`).MatchString(body) {
		t.Errorf("page lacks the day the snapshot was taken:\n%s", body)
	}
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Errorf("page contains a script tag:\n%s", body)
	}
	if strings.Contains(body, `"q"`) {
		t.Errorf("page holds an unescaped quote:\n%s", body)
	}
}

func TestSharePageCaptions(t *testing.T) {
	e, h := sharePagesEnv(t)
	esc := `&lt;script&gt;alert(1)&lt;/script&gt; &#34;q&#34; &amp; co`
	f, tr := false, true
	for _, c := range []struct {
		name         string
		project, rng *bool
		has, lacks   []string
	}{
		{"both", &tr, &tr, []string{
			`<title>` + esc + ` · blog</title>`,
			`<meta property="og:description" content="blog · Sep 1 – Sep 30, 2026">`,
			`<meta property="og:image:alt" content="` + esc + `, blog, Sep 1 – Sep 30, 2026">`,
		}, nil},
		{"project only", &tr, &f, []string{
			`<title>` + esc + ` · blog</title>`,
			`<meta property="og:description" content="blog">`,
			`<meta property="og:image:alt" content="` + esc + `, blog">`,
		}, []string{"Sep 1"}},
		{"range only", &f, &tr, []string{
			`<title>` + esc + `</title>`,
			`<meta property="og:description" content="Sep 1 – Sep 30, 2026">`,
			`<meta property="og:image:alt" content="` + esc + `, Sep 1 – Sep 30, 2026">`,
		}, []string{"blog"}},
		{"neither", &f, &f, []string{
			`<title>` + esc + `</title>`,
			`<meta property="og:image:alt" content="` + esc + `">`,
		}, []string{"blog", "Sep 1", "og:description"}},
	} {
		in := e.input(t)
		in.CaptionProject, in.CaptionRange = c.project, c.rng
		sh, err := e.svc.CreateWidgetShare(context.Background(), "test", in)
		if err != nil {
			t.Fatal(err)
		}
		rec := getShare(h, sh.ID)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", c.name, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range c.has {
			if !strings.Contains(body, want) {
				t.Errorf("%s: page lacks %s", c.name, want)
			}
		}
		for _, bad := range c.lacks {
			if strings.Contains(body, bad) {
				t.Errorf("%s: page holds %s", c.name, bad)
			}
		}
		if strings.Contains(strings.ToLower(body), "<script") || strings.Contains(body, `"q"`) {
			t.Errorf("%s: the title is not escaped", c.name)
		}
	}
}

func TestShareImages(t *testing.T) {
	e, h := sharePagesEnv(t)
	sh := e.create(t, "")
	in := e.input(t)
	for file, want := range map[string][]byte{
		sh.ID + ".png":    in.Image,
		sh.ID + "@2x.png": in.Image2x,
	} {
		rec := getShare(h, file)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", file, rec.Code)
		}
		if !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("%s: body is not the stored image", file)
		}
		for k, v := range map[string]string{
			"Content-Type":  "image/png",
			"Cache-Control": "public, max-age=3600",
			"X-Robots-Tag":  "noindex",
		} {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s: %s = %q, want %q", file, k, got, v)
			}
		}
	}
}

func TestShareNotFound(t *testing.T) {
	e, h := sharePagesEnv(t)
	sh := e.create(t, "")
	due := e.create(t, "7d")
	archived := e.create(t, "")
	if _, err := e.svc.ArchiveWidgetShare(context.Background(), "test", archived.ID); err != nil {
		t.Fatal(err)
	}
	*e.now = e.now.AddDate(0, 0, 8) // due is past its archive_at; the daily pass has not run

	files := []string{
		"0197a2c4-0000-7000-8000-000000000000", "abc", sh.ID + ".png.png", sh.ID + "@3x.png",
		sh.ID + "@2x", strings.ToUpper(sh.ID), ".png", "@2x.png",
	}
	for _, id := range []string{archived.ID, due.ID} {
		files = append(files, id, id+".png", id+"@2x.png")
	}
	for _, f := range files {
		rec := getShare(h, f)
		if rec.Code != 404 {
			t.Errorf("/share/%s: status %d, want 404", f, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("/share/%s: Cache-Control %q, want no-store", f, got)
		}
	}
	if rec := getShare(h, sh.ID); rec.Code != 200 {
		t.Errorf("the live share: status %d, want 200", rec.Code)
	}
}

func TestRangeInWords(t *testing.T) {
	for _, c := range []struct{ from, to, want string }{
		{"2026-09-05", "2026-10-04", "Sep 5 – Oct 4, 2026"},
		{"2025-12-20", "2026-01-10", "Dec 20, 2025 – Jan 10, 2026"},
		{"2026-09-05", "2026-09-05", "Sep 5, 2026"},
	} {
		if got := rangeInWords(c.from, c.to); got != c.want {
			t.Errorf("rangeInWords(%s, %s) = %q, want %q", c.from, c.to, got, c.want)
		}
	}
}
