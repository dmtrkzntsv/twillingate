package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/docs"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/version"
)

// reportingGuide calls reporting_guide and returns its markdown.
func reportingGuide(t *testing.T, args map[string]any) string {
	t.Helper()
	_, cs := newTestHost(t)
	if args != nil {
		toolJSON(t, cs, "create_dashboard", args, nil)
	}
	var out guideOut
	toolJSON(t, cs, "reporting_guide", map[string]any{}, &out)
	return out.Markdown
}

// TestReportingGuideIsLive: one call carries what an agent authors
// against — the running version and where its release notes are, the
// components and source types, the views, the projects and the
// dashboards — read when it is called, so a dashboard made just before
// is in it.
func TestReportingGuideIsLive(t *testing.T) {
	saved := version.Version
	version.Version = "v0.12.3"
	t.Cleanup(func() { version.Version = saved })
	md := reportingGuide(t, map[string]any{"title": "Signups this quarter"})

	comps, err := reporting.ParseManifest(reporting.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range comps {
		if !strings.Contains(md, "### "+c.Name+"\n") {
			t.Errorf("the guide has no section for component %s", c.Name)
		}
		if !strings.Contains(md, c.Description) {
			t.Errorf("the guide lacks %s's description", c.Name)
		}
	}
	for _, want := range []string{
		"v0.12.3", reporting.ReleasesURL, reporting.ReleasesURL + "/tag/v0.12.3",
		"Source types: md, sql",
		"v_views_daily(project_id, day",
		"| 1 | blog |", "| 2 | docs |",
		"Signups this quarter", "| 1 | Views | system |",
		`"format"`, // props schemas, verbatim
		"default 6 × 8",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("the guide lacks %q", want)
		}
	}
}

// TestReportingGuideCarriesTheWorkflowAndRules: the two sections of
// docs/reporting.md an agent must follow travel with the tool, byte for
// byte, so the guide and the document cannot disagree.
func TestReportingGuideCarriesTheWorkflowAndRules(t *testing.T) {
	md := reportingGuide(t, nil)
	for _, heading := range []string{"## Workflow", "## Rules"} {
		body, ok := section(docs.Reporting, heading)
		if !ok || strings.TrimSpace(body) == "" {
			t.Fatalf("docs/reporting.md has no %q section", heading)
		}
		if !strings.Contains(md, heading+body) {
			t.Errorf("the guide does not carry docs/reporting.md's %s section verbatim", heading)
		}
	}
}

// TestReportingGuideOnADevBuild: a build that is not a release links only
// to the list of releases, since there is no tag to link.
func TestReportingGuideOnADevBuild(t *testing.T) {
	saved := version.Version
	version.Version = "dev"
	t.Cleanup(func() { version.Version = saved })
	md := reportingGuide(t, nil)
	if !strings.Contains(md, reporting.ReleasesURL) || strings.Contains(md, "/tag/") {
		t.Errorf("a dev build's guide should link the releases and no tag:\n%s", md[:min(len(md), 600)])
	}
}

// TestServerInstructionsNameBothGuides: every client gets the pointer on
// connect, whether it reads resources or not — on the test host and on
// the handler Build assembles.
func TestServerInstructionsNameBothGuides(t *testing.T) {
	_, cs := newTestHost(t)
	got := cs.InitializeResult().Instructions
	for _, want := range []string{"integration_guide", "reporting_guide", reporting.ReleasesURL} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions %q do not name %s", got, want)
		}
	}

	h := newHandlerFixture(t, nil)
	req := initReq()
	req.Header.Set("Authorization", "Bearer ar_testtoken")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize = %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if i := strings.Index(body, "{"); i >= 0 { // an SSE frame: "event: message\ndata: {…}"
		body = body[i:]
	}
	var msg struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&msg); err != nil {
		t.Fatalf("initialize body %q: %v", rec.Body.String(), err)
	}
	if msg.Result.Instructions != serverInstructions {
		t.Errorf("Build's server instructions = %q, want serverInstructions", msg.Result.Instructions)
	}
}

// TestAppMountedBesideTheAPI: the dashboards are served, without a login,
// on whichever mux the API is mounted on; / and /app redirect to /app/,
// and only the exact root does.
func TestAppMountedBesideTheAPI(t *testing.T) {
	h := newHandlerFixture(t, nil)
	for target, want := range map[string]int{
		"/app/":             http.StatusOK,
		"/app/dashboards/3": http.StatusOK,
		"/app":              http.StatusMovedPermanently,
		"/":                 http.StatusFound,
		"/nothing-here":     http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", target, rec.Code, want)
		}
		if (want == http.StatusMovedPermanently || want == http.StatusFound) && rec.Header().Get("Location") != "/app/" {
			t.Errorf("GET %s redirects to %q, want /app/", target, rec.Header().Get("Location"))
		}
		if want == http.StatusOK && !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("GET %s Content-Type = %q", target, rec.Header().Get("Content-Type"))
		}
	}
}

// TestReportingGuideEscapesTableCells: a title or project name holding a
// pipe or a line break stays inside its table cell.
func TestReportingGuideEscapesTableCells(t *testing.T) {
	_, cs := newTestHost(t)
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Web | App\nweekly"}, nil)
	toolJSON(t, cs, "create_project", map[string]any{"name": "shop|eu"}, nil)
	var out guideOut
	toolJSON(t, cs, "reporting_guide", map[string]any{}, &out)
	for _, want := range []string{`| Web \| App weekly | user |`, `| shop\|eu |`} {
		if !strings.Contains(out.Markdown, want) {
			t.Errorf("the guide lacks the escaped row %q", want)
		}
	}
}
