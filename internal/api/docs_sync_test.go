package api

// Drift tripwires: docs/twillingate.md is the single normative document and
// the only prose the MCP endpoint serves, so its load-bearing facts are
// asserted against the source they describe. Change the reserved-key set or
// the SDK's public surface without the document and these tests fail.

import (
	"context"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/docs"
	"github.com/dmtrkzntsv/twillingate/internal/enrich"
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
		"init", "page", "onPage(", "onEvent(", "screen", "track", "attrs", "identify", "group", "installId(",
		"reset", "flush", "consent", "optOut(", "debug(", ".create(", ".get(", "storage", "taggedEvents",
		"detectOS", "detectBrowser", "detectDevice", "ClientSignals",
		"twillingate_ignore", "twillingate_debug",
		"pushState", "popstate", "hashchange",
		"$page_view", "$screen_view", "$install_id", "$kind", "$platform", "$os", "$os_name",
		"$browser", "$browser_version", "$device", "$consent",
		"$os_version", "$display_width", "$display_height", "$browser_locale", "$app_locale", "appLocale",
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

// TestDocumentNamesEveryTool binds the tool tables in docs/twillingate.md to
// the tools actually registered. A tool nobody documents is one an agent
// never reaches for; a documented tool that does not exist is a failed call.
func TestDocumentNamesEveryTool(t *testing.T) {
	h, _ := newTestHost(t)
	registered := map[string]bool{}
	for _, s := range newTestRegistrar(t, h).specs {
		registered[s.Name] = true
	}
	// Scoped to the tool tables and the "Managing" list, not the whole
	// document: `identities` is also a table name in the views prose, so a
	// document-wide Contains would pass for the wrong reason.
	documented := documentedTools()
	for name := range registered {
		if !documented[name] {
			t.Errorf("tool %s is registered but not listed in a tool table in docs/twillingate.md", name)
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

// documentedTools reads tool names out of the document's tables and its
// "Managing" list — the places that claim a tool exists, as opposed to
// prose that may mention the same word for something else.
func documentedTools() map[string]bool {
	out := map[string]bool{}
	tick := regexp.MustCompile("`([a-z][a-z_]+)`")
	for _, line := range strings.Split(docs.Twillingate, "\n") {
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
	words := map[int]string{
		15: "fifteen", 16: "sixteen", 17: "seventeen", 18: "eighteen",
		19: "nineteen", 20: "twenty", 21: "twenty-one", 22: "twenty-two",
		23: "twenty-three", 24: "twenty-four", 25: "twenty-five",
	}
	return words[n]
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
	// advice, and litestream's own credentials besides, so a document-wide
	// match would pass for a variable that had fallen out of the table.
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
		"API_AUTH_DSN", "litestream", "install.sh", "DATABASE_DSN",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("docs://deployment missing %q", want)
		}
	}
}

// TestDocumentMatchesRoutes binds the HTTP API table in docs/twillingate.md
// to the registered routes, in both directions: method, path and the tool
// each route mirrors.
func TestDocumentMatchesRoutes(t *testing.T) {
	h, _ := newTestHost(t)
	inCode := map[string]string{} // "GET /api/projects" -> tool
	for _, s := range newTestRegistrar(t, h).specs {
		if s.Method != "" {
			inCode[s.Method+" "+s.Path] = s.Name
		}
	}
	inCode["GET /api/schema/views"] = "schema://views"

	const heading = "### HTTP API"
	i := strings.Index(docs.Twillingate, heading)
	if i < 0 {
		t.Fatal("docs/twillingate.md has no '### HTTP API' section")
	}
	section := docs.Twillingate[i+len(heading):]
	if j := strings.Index(section, "\n### "); j >= 0 {
		section = section[:j]
	}
	row := regexp.MustCompile("^\\| `(GET|POST|PATCH)` \\| `(/api/[^`]*)` \\| `([a-z_:/]+)` \\|")
	documented := map[string]string{}
	for _, line := range strings.Split(section, "\n") {
		if m := row.FindStringSubmatch(line); m != nil {
			documented[m[1]+" "+m[2]] = m[3]
		}
	}
	for route, tool := range inCode {
		if documented[route] != tool {
			t.Errorf("route %s (%s) is registered but the HTTP API table says %q", route, tool, documented[route])
		}
	}
	for route := range documented {
		if _, ok := inCode[route]; !ok {
			t.Errorf("the HTTP API table lists %s, which is not registered", route)
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
// same level.
func docSection(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, heading)
	if i < 0 {
		t.Fatalf("no %q section", heading)
	}
	section := doc[i+len(heading):]
	level := strings.Repeat("#", len(heading)-len(strings.TrimLeft(heading, "#")))
	if j := strings.Index(section, "\n"+level+" "); j >= 0 {
		section = section[:j]
	}
	return section
}
