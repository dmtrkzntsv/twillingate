package api

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/sqlengine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSchemaResources(t *testing.T) {
	_, cs := newTestHost(t)
	res, err := cs.ReadResource(context.Background(),
		&mcp.ReadResourceParams{URI: "schema://views"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Contents[0].Text
	for _, want := range []string{
		"events(id, project_id", "measures(", ", bucket)", "attr_<key>", "NULL, not ''", "DATE", "raw history",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("schema://views missing %q", want)
		}
	}
	pres, err := cs.ReadResource(context.Background(),
		&mcp.ReadResourceParams{URI: "schema://projects"})
	if err != nil {
		t.Fatal(err)
	}
	if text := pres.Contents[0].Text; !strings.Contains(text, "blog") || !strings.Contains(text, `"project_id": 1`) {
		t.Errorf("schema://projects missing project or its id: %s", text)
	}
	if strings.Contains(pres.Contents[0].Text, "Identity") || strings.Contains(pres.Contents[0].Text, "identity") {
		t.Errorf("schema://projects still lists identity: %s", pres.Contents[0].Text)
	}
}

// TestSchemaViewsIsTheFilesOnly: schema://views and its route describe the
// event files alone; SQLite's v_* views and their deprecation are gone.
func TestSchemaViewsIsTheFilesOnly(t *testing.T) {
	h, cs := newTestHost(t)
	if schemaViews != schemaFiles {
		t.Error("schemaViews is not schemaFiles")
	}
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "schema://views"})
	if err != nil {
		t.Fatal(err)
	}
	rec := serveREST(t, newTestRegistrar(t, h), "GET", "/api/schema/views", "")
	for name, text := range map[string]string{"resource": res.Contents[0].Text, "route": rec.Body.String()} {
		if text != schemaFiles {
			t.Errorf("%s is not the files' schema:\n%s", name, text)
		}
		for _, gone := range []string{"v_views_daily", "agg_", "Deprecated", "source sqlite"} {
			if strings.Contains(text, gone) {
				t.Errorf("%s names %q", name, gone)
			}
		}
	}
}

// tableLines reads schemaViews' table lines as the console's editor does
// (web/src/lib/schema-views.ts): an indented name(col, col, …).
func tableLines(text string) map[string][]string {
	line := regexp.MustCompile(`^\s+([a-z_][a-z0-9_]*)\(([^)]*)\)`)
	out := map[string][]string{}
	for _, l := range strings.Split(text, "\n") {
		m := line.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		var cols []string
		for _, c := range strings.Split(m[2], ",") {
			if c = strings.TrimSpace(c); c != "" {
				cols = append(cols, c)
			}
		}
		out[m[1]] = cols
	}
	return out
}

// TestSchemaViewsMatchesTheFiles binds the files' part of schema://views
// to the tables the reader defines: each of the seven, with exactly its
// fixed columns in order (the attr_<key> columns, which vary, aside), and
// no other table before the deprecated part.
func TestSchemaViewsMatchesTheFiles(t *testing.T) {
	h, _ := newTestHost(t)
	listed := tableLines(schemaFiles)
	tables := []string{"events", "views", "product", "measures", "sessions", "identities", "aliases"}
	if len(listed) != len(tables) {
		t.Errorf("schemaFiles lists %d tables, want the files' %d: %v", len(listed), len(tables), listed)
	}
	for _, table := range tables {
		res, err := h.runFiles(context.Background(), sqlengine.Request{SQL: "SELECT column_name FROM (DESCRIBE SELECT * FROM " + table + ")"})
		if err != nil {
			t.Fatal(err)
		}
		var cols []string
		for _, row := range res.Rows {
			if c, _ := row[0].(string); !strings.HasPrefix(c, "attr_") {
				cols = append(cols, c)
			}
		}
		if !slices.Equal(listed[table], cols) {
			t.Errorf("schemaFiles lists %s(%s), the files have (%s)", table, strings.Join(listed[table], ", "), strings.Join(cols, ", "))
		}
	}
	if !strings.HasPrefix(schemaViews, schemaFiles) {
		t.Error("schema://views does not lead with schemaFiles")
	}
}
