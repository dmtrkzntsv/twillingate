package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/docs"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/version"
)

// reporting_guide is the one call an agent makes before authoring a
// dashboard, for clients that never read resources: the live parts
// (running version, components, source types, projects, dashboards) read
// on the call, the views an agent writes SQL against, and the workflow and
// rules of docs/reporting.md, sliced out of the document so the two cannot
// disagree.

// serverInstructions are sent on connect (MCP's initialize result): the
// two guides are where an agent starts, whatever it came to do.
const serverInstructions = "To integrate a site or app, call integration_guide. To build or change dashboards, call reporting_guide; reuse a built-in widget with copy_widget when one shows what is asked, else write a structured query source, and SQL only for what neither can show. To customize a system dashboard, duplicate it with whole_group, then take the original out of the sidebar with update_dashboard {sidebar: false}; add your copy to projects with add_project_tab. If widgets broke after an update, read the release notes at https://github.com/dmtrkzntsv/twillingate/releases."

// guideSections are the parts of docs/reporting.md the guide carries.
var guideSections = []string{"## Workflow", "## Rules"}

func (h *host) reportingGuide(ctx context.Context, _ struct{}) (guideOut, error) {
	comps, err := h.rep.ListedComponents(ctx)
	if err != nil {
		return guideOut{}, err
	}
	dashboards, err := h.rep.Dashboards(ctx)
	if err != nil {
		return guideOut{}, err
	}

	var b strings.Builder
	b.WriteString("# Building dashboards on this instance\n\n")
	b.WriteString("The page at /app/ shows the dashboards below; agents build them with create_dashboard, copy_widget, add_widget and update_widget. docs://reporting is the whole reference.\n\n")
	writeVersion(&b, version.Version)

	b.WriteString("## Components\n\n")
	fmt.Fprintf(&b, "Source types: %s. A widget's source is {\"type\": …, \"content\": …}. Prefer, in order: a copy of a built-in widget (copy_widget; Built-in widgets below), a query source, and sql only for what neither can show (the Workflow, step 4). A query source's content is {\"query\": …, \"rename\": …, \"labels\": …}: a structured query, the same object as the query tool's query argument (its schema is the grammar; docs://twillingate, Structured queries), its output columns renamed to the inputs ({\"day\": \"x\"}), and text columns' values relabelled ({\"label\": {\"new\": \"New users\"}}); it follows the project and the range. A sql source's SQL returns the columns the component reads, by name (alias them: day AS x).\n\n",
		strings.Join(h.rep.SourceTypes(), ", "))
	for _, c := range comps {
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", c.Name, c.Description)
		fmt.Fprintf(&b, "- accepts: %s; default %d × %d\n", strings.Join(c.Accepts, ", "), c.DefaultWidth, c.DefaultHeight)
		fmt.Fprintf(&b, "- inputs: %s\n", describeInputs(c.Inputs))
		fmt.Fprintf(&b, "- props: %s\n\n", compactJSON(c.Props))
	}

	b.WriteString("## Tables\n\n")
	b.WriteString(schemaFiles)
	b.WriteString("\n\n")

	b.WriteString("## Projects\n\nActive projects, the values :project takes:\n\n| project_id | name |\n| --- | --- |\n")
	active := 0
	for _, p := range h.reg.Snapshot(ctx).Projects() {
		if !p.Archived {
			fmt.Fprintf(&b, "| %d | %s |\n", p.ID, tableCell(p.Name))
			active++
		}
	}
	if active == 0 {
		b.WriteString("\nNone yet: a widget following the project has nothing to show until create_project makes one.\n")
	}

	b.WriteString("\n## Dashboards\n\nIn sidebar order. System dashboards are never archived and are otherwise read-only: update_dashboard {sidebar: false} takes a system group out of the sidebar, and duplicate_dashboard makes an editable copy. Rows sharing a group_id are the tabs of one sidebar entry; sidebar says whether that entry is shown.\n\n| dashboard_id | title | owner | group_id | sidebar | archived |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, d := range dashboards.Dashboards {
		sidebar, archived := "no", "no"
		if d.Sidebar {
			sidebar = "yes"
		}
		if d.ArchivedAt != "" {
			archived = d.ArchivedAt
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %d | %s | %s |\n", d.ID, tableCell(d.Title), d.Owner, d.GroupID, sidebar, archived)
	}
	fmt.Fprintf(&b, "\nDays are grouped in %s.\n", dashboards.Timezone)

	if err := h.writeBuiltinWidgets(ctx, &b, dashboards.Dashboards); err != nil {
		return guideOut{}, err
	}

	for _, heading := range guideSections {
		body, ok := section(docs.Reporting, heading)
		if !ok {
			// Embedded at build time; docs_sync and the guide test keep
			// the headings in place.
			return guideOut{}, fmt.Errorf("api: docs/reporting.md has no %q section", heading)
		}
		b.WriteString("\n" + heading + body)
	}
	return guideOut{Markdown: b.String()}, nil
}

// writeVersion names the running build and where its release notes are:
// a release links its own tag, any other build only the list.
func writeVersion(b *strings.Builder, v string) {
	tag := v
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if v == "dev" || strings.Contains(v, "-") { // a local or untagged build: no tag page to link
		fmt.Fprintf(b, "Running version: %s, not a release. Release notes: %s\n\n", v, reporting.ReleasesURL)
		return
	}
	fmt.Fprintf(b, "Running version: %s. Its release notes: %s/tag/%s; every release: %s. When widgets break after an update, read the notes of every release since the last version they worked on.\n\n",
		v, reporting.ReleasesURL, tag, reporting.ReleasesURL)
}

// tableCell keeps text inside one Markdown table cell: a pipe is escaped
// and line breaks (any run of white space) become one space.
func tableCell(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "|", `\|`)), " ")
}

// compactJSON prints a props schema on one line; the database keeps it
// as the manifest indented it.
func compactJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

// describeInputs spells out the columns a component reads.
func describeInputs(in reporting.Inputs) string {
	if in.Open {
		return "any columns, shown in query order"
	}
	if len(in.Columns) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(in.Columns))
	for _, c := range in.Columns {
		p := c.Name + " (" + strings.Join(c.Types, " or ") + ")"
		if c.Optional {
			p += ", optional"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}

// section returns the text under a Markdown heading (without the heading
// line), up to the next heading at the same level, and whether the
// heading was found.
func section(doc, heading string) (string, bool) {
	i := strings.Index(doc, "\n"+heading+"\n")
	if i < 0 {
		return "", false
	}
	rest := doc[i+1+len(heading):]
	level := strings.Repeat("#", len(heading)-len(strings.TrimLeft(heading, "#")))
	if j := strings.Index(rest, "\n"+level+" "); j >= 0 {
		rest = rest[:j]
	}
	return rest, true
}

// writeBuiltinWidgets lists the system dashboards' live widgets with what
// each one reads, so an agent finds one to copy_widget before writing
// its own: the catalog reuse starts from.
func (h *host) writeBuiltinWidgets(ctx context.Context, b *strings.Builder, ds []reporting.DashboardInfo) error {
	titles := map[int64]string{}
	for _, d := range ds {
		if d.Owner == "system" {
			titles[d.ID] = d.Title
		}
	}
	ws, err := h.rep.Widgets(ctx, 0, "")
	if err != nil {
		return err
	}
	b.WriteString("\n## Built-in widgets\n\nThe system dashboards' widgets, each ready for copy_widget {widget_id, dashboard_id}: it lands on your dashboard as it is, following the project and range switchers. Prefer one of these to writing a widget of your own.\n\n| widget_id | dashboard | title | component | reads |\n| --- | --- | --- | --- | --- |\n")
	for _, w := range ws {
		dash, ok := titles[w.DashboardID]
		if !ok || w.ArchivedAt != "" || w.Component == nil || w.Source.Type == "md" {
			continue
		}
		fmt.Fprintf(b, "| %d | %s | %s | %s | %s |\n", w.ID, tableCell(dash), tableCell(w.Title), *w.Component, tableCell(describeSource(w.Source)))
	}
	return nil
}

// describeSource says in a few words what a widget's source reads: a
// structured query's kind, table, split and metrics (with the names
// they are given); sql as such.
func describeSource(s reporting.Source) string {
	if s.Type != "query" {
		return s.Type
	}
	var c struct {
		Query struct {
			Kind       string   `json:"kind"`
			Table      string   `json:"table"`
			Dimensions []string `json:"dimensions"`
			Series     string   `json:"series"`
			Output     string   `json:"output"`
			Metrics    []struct {
				Op     string `json:"op"`
				Of     string `json:"of"`
				Column string `json:"column"`
				As     string `json:"as"`
			} `json:"metrics"`
		} `json:"query"`
	}
	if err := json.Unmarshal([]byte(s.Content), &c); err != nil {
		return "query"
	}
	q := c.Query
	parts := []string{q.Kind}
	if q.Table != "" {
		parts = append(parts, "of "+q.Table)
	}
	if len(q.Dimensions) > 0 {
		parts = append(parts, "by "+strings.Join(q.Dimensions, ", "))
	}
	if q.Series != "" {
		parts = append(parts, "per "+q.Series)
	}
	if q.Output != "" {
		parts = append(parts, "as "+q.Output)
	}
	ms := make([]string, 0, len(q.Metrics))
	for _, m := range q.Metrics {
		name := m.Op
		if arg := m.Of + m.Column; arg != "" {
			name += "(" + arg + ")"
		}
		if m.As != "" {
			name += " as " + m.As
		}
		ms = append(ms, name)
	}
	if len(ms) > 0 {
		parts = append(parts, ": "+strings.Join(ms, ", "))
	}
	return strings.Replace(strings.Join(parts, " "), " :", ":", 1)
}
