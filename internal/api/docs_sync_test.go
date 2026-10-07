package api

// Drift tripwires: docs/twillingate.md and docs/reporting.md are the
// normative documents agents read over MCP, so their load-bearing facts are
// asserted against the source they describe. Change the reserved-key set or
// the SDK's public surface without the document and these tests fail.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/docs"
	"github.com/dmtrkzntsv/twillingate/internal/enrich"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// documentedKeys reads the reserved attribute key table out of the
// document: the rows between its heading and the next one.
func documentedKeys(t *testing.T) []string {
	t.Helper()
	const heading = "### Reserved attribute keys"
	i := strings.Index(docs.Twillingate, heading)
	if i < 0 {
		t.Fatal("docs/twillingate.md has no '### Reserved attribute keys' section")
	}
	section := docs.Twillingate[i+len(heading):]
	if j := strings.Index(section, "\n### "); j >= 0 {
		section = section[:j]
	}
	var keys []string
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		keys = append(keys, regexp.MustCompile("`(\\$[a-z_]+)`").FindAllString(line, -1)...)
	}
	if len(keys) < 10 {
		t.Fatalf("found only %d keys in the reserved-key table — did its shape change?", len(keys))
	}
	for i, k := range keys {
		keys[i] = strings.Trim(k, "`")
	}
	return keys
}

func readSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s (run tests from the package dir): %v", path, err)
	}
	return string(b)
}

// removedKeys are documented deliberately as NO LONGER existing, so the
// reverse check below must not treat them as stale. Keeping the note is the
// point: a stale client sending $url needs to find out why it is rejected.
var removedKeys = map[string]bool{"$url": true}

// TestDocumentMatchesReservedKeys extracts the reservedKeys map from
// ingest.go and requires two-way agreement with docs/twillingate.md. An
// undocumented key is a contract an agent cannot discover; a documented key
// that does not exist is one it will waste a round trip on.
func TestDocumentMatchesReservedKeys(t *testing.T) {
	src := readSource(t, "../../internal/server/ingest.go")
	re := regexp.MustCompile(`"(\$[a-z_]+)":\s*func\(`)
	inCode := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		inCode[m[1]] = true
	}
	if len(inCode) < 10 {
		t.Fatalf("extracted only %d reserved keys from ingest.go — extraction regexp broken?", len(inCode))
	}
	for k := range inCode {
		if !strings.Contains(docs.Twillingate, k) {
			t.Errorf("reserved key %s exists in ingest.go but is missing from docs/twillingate.md", k)
		}
	}
	// Reverse: every $key the document's authoritative table lists must
	// exist in code. Scoped to that table's rows rather than the whole
	// document, which also contains shell variables ($base), illustrative
	// warnings ($app_ver) and hypothetical future names ($session_start).
	for _, k := range documentedKeys(t) {
		if inCode[k] || removedKeys[k] {
			continue
		}
		t.Errorf("the reserved-key table lists %s, which ingest.go does not define", k)
	}
	for _, name := range []string{"$page_view", "$screen_view"} {
		if !strings.Contains(src, `"`+name+`"`) {
			t.Errorf("event name %s documented but not found in ingest.go", name)
		}
		if !strings.Contains(docs.Twillingate, name) {
			t.Errorf("event name %s missing from docs/twillingate.md", name)
		}
	}
	// A key the server removed must stay documented as removed, so a stale
	// client can find out why its pageviews are rejected.
	for k := range removedKeys {
		if inCode[k] {
			t.Errorf("%s is in reservedKeys again — drop it from removedKeys", k)
		}
		if !strings.Contains(docs.Twillingate, k) {
			t.Errorf("docs/twillingate.md must keep explaining that %s was removed", k)
		}
	}
}

// TestDocumentMatchesSDK asserts every documented SDK symbol exists in the
// source (sdk/src/twillingate.ts — the shipped bundle is compiled from it
// and separately drift-checked in CI).
func TestDocumentMatchesSDK(t *testing.T) {
	src := readSource(t, "../../sdk/src/twillingate.ts") + readSource(t, "../../sdk/src/factory.ts") + readSource(t, "../../sdk/src/runtime.ts")
	for _, symbol := range []string{
		"data-key", "data-identity", "data-auto",
		"data-mask-url", "data-routing", "data-consent", "data-instance",
		"data-kind",
		"init", "page", "onPage(", "onEvent(", "screen", "track", "measure(", "attrs", "identify", "group", "installId(",
		"reset", "flush", "consent", "optOut(", "debug(", ".create(", ".get(", "storage", "taggedEvents",
		"detectOS", "detectBrowser", "detectDevice", "ClientSignals",
		"twillingate_ignore", "twillingate_debug",
		"pushState", "popstate", "hashchange",
		"$page_view", "$screen_view", "$install_id", "$kind", "$platform", "$os", "$os_name",
		"$browser", "$browser_version", "$device", "$consent",
		"$os_version", "$display_width", "$display_height", "$browser_locale", "$app_locale", "appLocale",
		"autoAttributes",
		"data-vitals", "vitals", "$sample_rate", "$lcp",
		"data-twillingate-form", "submitForm(", "twillingate:form",
	} {
		if !strings.Contains(src, symbol) {
			t.Errorf("docs/twillingate.md documents %q but the SDK source does not contain it", symbol)
		}
		if !strings.Contains(docs.Twillingate, symbol) {
			t.Errorf("%q missing from docs/twillingate.md", symbol)
		}
	}
	for _, gone := range []string{"data-user", "data-group", "data-debug", "data-storage"} {
		if strings.Contains(src, `getAttribute("`+gone+`")`) {
			t.Errorf("the SDK still reads %s, which the spec removed", gone)
		}
	}
	// util is the documented namespace for the path helpers.
	for _, symbol := range []string{"maskIds", "withQuery"} {
		if !strings.Contains(readSource(t, "../../sdk/src/util.ts"), symbol) {
			t.Errorf("util.%s documented but missing from sdk/src/util.ts", symbol)
		}
		if !strings.Contains(docs.Twillingate, symbol) {
			t.Errorf("util.%s missing from docs/twillingate.md", symbol)
		}
	}
}

// TestDocumentCoversEveryViewsDimension keeps the query guidance honest: a
// new breakdown dimension that nobody documents is one an agent never uses.
func TestDocumentCoversEveryViewsDimension(t *testing.T) {
	for _, d := range viewsDimensions {
		if !strings.Contains(docs.Twillingate, d.view) {
			t.Errorf("view %s is queryable but not named in docs/twillingate.md", d.view)
		}
		if !strings.Contains(schemaViews, d.view) {
			t.Errorf("view %s is queryable but not named in schema://views", d.view)
		}
	}
}

// TestDocumentNamesEveryTool binds the tool tables in docs/twillingate.md
// and docs/reporting.md to the tools actually registered. A tool nobody
// documents is one an agent never reaches for; a documented tool that does
// not exist is a failed call.
func TestDocumentNamesEveryTool(t *testing.T) {
	h, _ := newTestHost(t)
	registered := map[string]bool{}
	for _, s := range newTestRegistrar(t, h).specs {
		if !s.RESTOnly { // a route with no tool is bound by TestDocumentMatchesRoutes
			registered[s.Name] = true
		}
	}
	// Scoped to the tool tables and the "Managing" list, not the whole
	// document: `identities` is also a table name in the views prose, so a
	// document-wide Contains would pass for the wrong reason.
	documented := documentedTools()
	for name := range registered {
		if !documented[name] {
			t.Errorf("tool %s is registered but not listed in a tool table in docs/twillingate.md or docs/reporting.md", name)
		}
	}
	// No reverse check: these table rows also carry parameter names and
	// dimension values, so "documented but not registered" is noise. The
	// count assertion below is what catches a tool that quietly went away.
	// The document states a count; keep it honest.
	if !strings.Contains(docs.Twillingate, spellOut(len(registered))+" tools") {
		t.Errorf("docs/twillingate.md does not say %q tools (there are %d)",
			spellOut(len(registered)), len(registered))
	}
}

// documentedTools reads tool names out of both documents' tables and
// docs/twillingate.md's "Managing" list — the places that claim a tool
// exists, as opposed to prose that may mention the same word for
// something else.
func documentedTools() map[string]bool {
	out := map[string]bool{}
	tick := regexp.MustCompile("`([a-z][a-z_]+)`")
	for _, line := range strings.Split(docs.Twillingate+"\n"+docs.Reporting, "\n") {
		managing := strings.HasPrefix(line, "**Managing**") ||
			strings.HasPrefix(line, "`restore_project`") ||
			strings.HasPrefix(line, "`enable_ingest_key`")
		if !strings.HasPrefix(line, "|") && !managing {
			continue
		}
		for _, m := range tick.FindAllStringSubmatch(line, -1) {
			out[m[1]] = true
		}
	}
	return out
}

// spellOut covers the range the tool count plausibly moves through. A count
// outside it returns "" so the assertion above fails loudly rather than
// silently passing on a substring that happens to match.
func spellOut(n int) string {
	if n < 15 || n > 59 {
		return ""
	}
	teens := map[int]string{15: "fifteen", 16: "sixteen", 17: "seventeen", 18: "eighteen", 19: "nineteen"}
	if w, ok := teens[n]; ok {
		return w
	}
	tens := map[int]string{2: "twenty", 3: "thirty", 4: "forty", 5: "fifty"}[n/10]
	units := []string{"", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}[n%10]
	if units == "" {
		return tens
	}
	return tens + "-" + units
}

// TestDeploymentDocumentsEveryEnvVar binds docs/deployment.md to the
// variables config actually reads. An undocumented variable is one an
// operator cannot discover; a documented one that nothing reads sends them
// chasing a setting with no effect.
func TestDeploymentDocumentsEveryEnvVar(t *testing.T) {
	src := readSource(t, "../config/config.go")
	re := regexp.MustCompile(`\.(?:str|num|dur|bool)\("([A-Z][A-Z0-9_]+)"`)
	inCode := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		inCode[m[1]] = true
	}
	if len(inCode) < 20 {
		t.Fatalf("extracted only %d env vars from config.go — extraction regexp broken?", len(inCode))
	}
	// Both directions read the variable TABLE, not the whole document: the
	// prose names GEO_DSN and BUFFER_FLUSH_INTERVAL again in the tuning
	// advice, so a document-wide match would pass for a variable that had
	// fallen out of the table.
	documented := map[string]bool{}
	for _, k := range documentedEnvVars(t) {
		documented[k] = true
	}
	for k := range inCode {
		if !documented[k] {
			t.Errorf("config reads %s but the variable table in docs/deployment.md omits it", k)
		}
	}
	for k := range documented {
		if !inCode[k] {
			t.Errorf("docs/deployment.md documents %s, which config.go does not read", k)
		}
	}
}

// documentedEnvVars reads the variable table out of deployment.md: the rows
// under its heading, up to the next one.
func documentedEnvVars(t *testing.T) []string {
	t.Helper()
	const heading = "## Configure the collector"
	i := strings.Index(docs.Deployment, heading)
	if i < 0 {
		t.Fatal("docs/deployment.md has no '## Configure the collector' section")
	}
	section := docs.Deployment[i+len(heading):]
	if j := strings.Index(section, "\n## "); j >= 0 {
		section = section[:j]
	}
	var keys []string
	tick := regexp.MustCompile("`([A-Z][A-Z0-9_]+)`")
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		for _, m := range tick.FindAllStringSubmatch(line, -1) {
			keys = append(keys, m[1])
		}
	}
	if len(keys) < 20 {
		t.Fatalf("found only %d variables in the table — did its shape change?", len(keys))
	}
	return keys
}

// TestDeploymentResourceServed is the counterpart to the twillingate one:
// an operator-facing document nobody can read is not serving anyone.
func TestDeploymentResourceServed(t *testing.T) {
	_, cs := newTestHost(t)
	res, err := cs.ReadResource(context.Background(),
		&mcp.ReadResourceParams{URI: "docs://deployment"})
	if err != nil {
		t.Fatalf("docs://deployment: %v", err)
	}
	body := res.Contents[0].Text
	for _, want := range []string{
		"CONSOLE_AUTH_DSN", "/app/", "install.sh", "DATABASE_DSN",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("docs://deployment missing %q", want)
		}
	}
}

// TestDocumentMatchesRoutes binds the HTTP API tables in docs/twillingate.md
// and docs/reporting.md to the registered routes, in both directions:
// method, path and the tool each route mirrors.
func TestDocumentMatchesRoutes(t *testing.T) {
	h, _ := newTestHost(t)
	inCode := map[string]string{} // "GET /api/projects" -> tool
	for _, s := range newTestRegistrar(t, h).specs {
		if s.Method != "" {
			inCode[s.Method+" "+s.Path] = s.Name
		}
	}
	inCode["GET /api/schema/views"] = "schema://views"

	// The Mirrors cell may say more after the name ("`view`, REST only").
	row := regexp.MustCompile("^\\| `(GET|POST|PATCH|PUT)` \\| `(/api/[^`]*)` \\| `([a-z_:/]+)`[^|]* \\|")
	documented := map[string]string{}
	for _, section := range []string{
		docSection(t, docs.Twillingate, "### HTTP API"),
		docSection(t, docs.Reporting, "## HTTP API"),
	} {
		for _, line := range strings.Split(section, "\n") {
			if m := row.FindStringSubmatch(line); m != nil {
				documented[m[1]+" "+m[2]] = m[3]
			}
		}
	}
	for route, tool := range inCode {
		if documented[route] != tool {
			t.Errorf("route %s (%s) is registered but the HTTP API tables say %q", route, tool, documented[route])
		}
	}
	for route := range documented {
		if _, ok := inCode[route]; !ok {
			t.Errorf("an HTTP API table lists %s, which is not registered", route)
		}
	}
}

// TestReportingDocumentMatchesComponents binds the component table in
// docs/reporting.md to the UI's manifest in both directions: every
// component is documented with its default size, and every documented one
// exists. The manifest is what the release installs, so a component added
// or resized in web/ without the document fails here.
func TestReportingDocumentMatchesComponents(t *testing.T) {
	comps, err := reporting.ParseManifest(reporting.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	inCode := map[string]string{}
	for _, c := range comps {
		inCode[c.Name] = fmt.Sprintf("%d × %d", c.DefaultWidth, c.DefaultHeight)
	}
	row := regexp.MustCompile("^\\| `([a-z_]+)` \\|.*\\| (\\d+ × \\d+) \\|$")
	documented := map[string]string{}
	for _, line := range strings.Split(docSection(t, docs.Reporting, "## Components"), "\n") {
		if m := row.FindStringSubmatch(line); m != nil {
			documented[m[1]] = m[2]
		}
	}
	for name, size := range inCode {
		if documented[name] != size {
			t.Errorf("component %s (%s) is in the manifest but the component table says %q", name, size, documented[name])
		}
	}
	for name := range documented {
		if _, ok := inCode[name]; !ok {
			t.Errorf("the component table lists %s, which the manifest does not have", name)
		}
	}
}

// TestReportingExamplesWork: docs/reporting.md's Examples section has one
// worked example per component in the manifest, and no other; each is
// accepted by add_widget as written and loads, with project 1's seeded
// rows, through widget_data. An example that stops validating after a
// view or component change fails here, not in an agent's session.
func TestReportingExamplesWork(t *testing.T) {
	_, cs := newTestHost(t)
	comps, err := reporting.ParseManifest(reporting.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	var dash struct {
		ID int64 `json:"dashboard_id"`
	}
	toolJSON(t, cs, "create_dashboard", map[string]any{"title": "Examples"}, &dash)

	block := regexp.MustCompile("(?s)```(sql|markdown)\n(.*?)```")
	props := regexp.MustCompile("(?m)^Props: `(.*)`$")
	examples := map[string]bool{}
	for _, part := range strings.Split(docSection(t, docs.Reporting, "## Examples"), "\n### `")[1:] {
		name := part[:strings.Index(part, "`")]
		examples[name] = true
		b := block.FindStringSubmatch(part)
		p := props.FindStringSubmatch(part)
		if b == nil || p == nil {
			t.Errorf("example %s needs a sql or markdown block and a Props: line", name)
			continue
		}
		source := map[string]any{"type": "sql", "content": b[2]}
		if b[1] == "markdown" {
			source["type"] = "md"
		}
		var pr map[string]any
		if err := json.Unmarshal([]byte(p[1]), &pr); err != nil {
			t.Errorf("example %s props %s: %v", name, p[1], err)
			continue
		}
		res := callTool(t, cs, "add_widget", map[string]any{"dashboard_id": dash.ID, "component": name,
			"title": name, "props": pr, "source": source})
		if res.IsError {
			t.Errorf("example %s is refused: %s", name, textOf(res))
			continue
		}
		var w struct {
			ID int64 `json:"widget_id"`
		}
		if err := json.Unmarshal([]byte(textOf(res)), &w); err != nil {
			t.Fatal(err)
		}
		if res := callTool(t, cs, "widget_data", map[string]any{"widget_id": w.ID, "project_id": 1,
			"from": "2026-08-01", "to": "2026-08-26"}); res.IsError {
			t.Errorf("example %s does not load: %s", name, textOf(res))
		}
	}
	for _, c := range comps {
		if !examples[c.Name] {
			t.Errorf("component %s has no worked example in docs/reporting.md", c.Name)
		}
	}
	if len(examples) != len(comps) {
		t.Errorf("docs/reporting.md has %d examples for %d components", len(examples), len(comps))
	}
}

// TestReportingDocumentNamesEveryPreset: the range vocabulary the stored
// selection is validated against is spelled out, id by id, where an agent
// choosing create_dashboard's range reads it.
func TestReportingDocumentNamesEveryPreset(t *testing.T) {
	section := docSection(t, docs.Reporting, "## Parameters and ranges")
	for _, p := range reporting.Presets {
		if !strings.Contains(section, "| `"+p+"` |") {
			t.Errorf("range preset %s has no row in docs/reporting.md's range table", p)
		}
	}
}

// TestDocumentMatchesVocabularies binds the two prose copies of the closed
// vocabularies to enrich's own lists. Both documents spell every value out
// — the environment table in docs/twillingate.md and the migration-015
// pre-check query in deploy/UPGRADES.md — so a value added to or dropped
// from enrich leaves a document telling clients the wrong thing. Order is
// compared too: the lists read as a ranking, and a silent reshuffle is the
// kind of drift nobody notices.
func TestDocumentMatchesVocabularies(t *testing.T) {
	for _, c := range []struct {
		key  string
		want []string
	}{
		{"$os", enrich.OSValues},
		{"$browser", enrich.BrowserValues},
		{"$device", enrich.DeviceValues},
	} {
		got := documentedVocabulary(t, c.key)
		if !slices.Equal(got, c.want) {
			t.Errorf("the environment table's %s values are\n%v\nenrich has\n%v", c.key, got, c.want)
		}
	}
	if got := precheckOSVocabulary(t); !slices.Equal(got, enrich.OSValues) {
		t.Errorf("the migration-015 pre-check query lists\n%v\nenrich.OSValues is\n%v", got, enrich.OSValues)
	}
}

// documentedVocabulary reads the Values cell of one row of the environment
// table: the row whose first cell is the given key, inside "### Declaring
// the environment". Scoped to the row, not the section, because the prose
// around it names individual values too.
func documentedVocabulary(t *testing.T, key string) []string {
	t.Helper()
	section := docSection(t, docs.Twillingate, "### Declaring the environment")
	tick := regexp.MustCompile("`([a-z_]+)`")
	for _, line := range strings.Split(section, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 4 || strings.TrimSpace(cells[1]) != "`"+key+"`" {
			continue
		}
		var values []string
		for _, m := range tick.FindAllStringSubmatch(cells[2], -1) {
			values = append(values, m[1])
		}
		return values
	}
	t.Fatalf("the environment table in docs/twillingate.md has no %s row", key)
	return nil
}

// precheckOSVocabulary reads the quoted list out of the NOT IN (…) of the
// migration-015 pre-check query: the copy an operator pastes into sqlite3
// before upgrading, which has to name exactly the values the migration
// treats as known. It lives in deploy/UPGRADES.md, where the one-time
// schema runbooks moved out of docs/deployment.md, so it is read from disk
// rather than from the embedded document.
func precheckOSVocabulary(t *testing.T) []string {
	t.Helper()
	section := docSection(t, readSource(t, "../../deploy/UPGRADES.md"),
		"### Upgrading to the declared environment (migration 015)")
	i := strings.Index(section, "NOT IN (")
	if i < 0 {
		t.Fatal("the migration-015 section has no `NOT IN (` pre-check query")
	}
	rest := section[i+len("NOT IN ("):]
	j := strings.Index(rest, ")")
	if j < 0 {
		t.Fatal("the migration-015 pre-check query's NOT IN ( is never closed")
	}
	var values []string
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(rest[:j], -1) {
		values = append(values, m[1])
	}
	return values
}

// docSection returns the text under a heading, up to the next one at the
// same level, failing the test when the heading is missing.
func docSection(t *testing.T, doc, heading string) string {
	t.Helper()
	body, ok := section(doc, heading)
	if !ok {
		t.Fatalf("no %q section", heading)
	}
	return body
}
