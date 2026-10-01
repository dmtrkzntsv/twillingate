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
const serverInstructions = "To integrate a site or app, call integration_guide. To build or change dashboards, call reporting_guide. To customize a system dashboard, duplicate it with whole_group, then archive the original with archive_dashboard whole_group to take it out of the sidebar. If widgets broke after an update, read the release notes at https://github.com/dmtrkzntsv/twillingate/releases."

// guideSections are the parts of docs/reporting.md the guide carries.
var guideSections = []string{"## Workflow", "## Rules"}

func (h *host) reportingGuide(ctx context.Context, _ struct{}) (guideOut, error) {
	comps, err := h.rep.Components(ctx)
	if err != nil {
		return guideOut{}, err
	}
	dashboards, err := h.rep.Dashboards(ctx)
	if err != nil {
		return guideOut{}, err
	}

	var b strings.Builder
	b.WriteString("# Building dashboards on this instance\n\n")
	b.WriteString("The page at /app/ shows the dashboards below; agents build them with create_dashboard, add_widget and update_widget. docs://reporting is the whole reference.\n\n")
	writeVersion(&b, version.Version)

	b.WriteString("## Components\n\n")
	fmt.Fprintf(&b, "Source types: %s. A widget's source is {\"type\": …, \"content\": …}; its SQL returns the columns the component reads, by name (alias them: day AS x).\n\n",
		strings.Join(h.rep.SourceTypes(), ", "))
	for _, c := range comps {
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", c.Name, c.Description)
		fmt.Fprintf(&b, "- accepts: %s; default %d × %d\n", strings.Join(c.Accepts, ", "), c.DefaultWidth, c.DefaultHeight)
		fmt.Fprintf(&b, "- inputs: %s\n", describeInputs(c.Inputs))
		fmt.Fprintf(&b, "- props: %s\n\n", compactJSON(c.Props))
	}

	b.WriteString("## Views\n\n")
	b.WriteString(schemaViews)
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

	b.WriteString("\n## Dashboards\n\nIn sidebar order. A system group is archived and restored whole (whole_group) and is otherwise read-only; duplicate_dashboard makes an editable copy; archive the system group whole to take it out of the sidebar. Rows sharing a group_id are the tabs of one sidebar entry.\n\n| dashboard_id | title | owner | group_id | archived |\n| --- | --- | --- | --- | --- |\n")
	for _, d := range dashboards.Dashboards {
		archived := "no"
		if d.ArchivedAt != "" {
			archived = d.ArchivedAt
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %d | %s |\n", d.ID, tableCell(d.Title), d.Owner, d.GroupID, archived)
	}
	fmt.Fprintf(&b, "\nDays are grouped in %s.\n", dashboards.Timezone)

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
