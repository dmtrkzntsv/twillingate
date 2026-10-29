package archive

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// ErrTables marks a table spec TableSpec refuses to write: a path that
// leaves the events directory.
var ErrTables = errors.New("archive: bad table spec")

// TableSpec is the table spec of the files under dir (data/) for an
// external SQL engine: what ListFiles lists, each path relative to dir,
// and rules. With projects, only those projects' files and rules are in
// it (a project with no files is left out, as ListFiles leaves it out);
// nil means every project. Projects are in id order, the rules sorted
// (sortRules), and every list is empty rather than null.
func TableSpec(dir string, projects []int64, rules Rules) (sqlengine.Tables, error) {
	keep := func(int64) bool { return true }
	if projects != nil {
		keep = func(id int64) bool { return slices.Contains(projects, id) }
	}
	files, err := listFiles(dir, keep)
	if err != nil {
		return sqlengine.Tables{}, err
	}
	out := sqlengine.Tables{Version: sqlengine.TablesVersion, Projects: []sqlengine.Project{},
		Rules: sqlengine.Rules{Events: []sqlengine.EventRule{}, Attributes: []sqlengine.AttributeRule{}}}
	for _, id := range sortedIDs(files) {
		p, err := projectSpec(dir, id, files[id])
		if err != nil {
			return sqlengine.Tables{}, err
		}
		out.Projects = append(out.Projects, p)
	}
	rules = sortRules(rules)
	for _, r := range rules.Events {
		if keep(r.ProjectID) {
			out.Rules.Events = append(out.Rules.Events, sqlengine.EventRule{ProjectID: r.ProjectID,
				Family: string(r.Family), Name: r.Name, Action: r.Action, To: r.To})
		}
	}
	for _, r := range rules.Attributes {
		if keep(r.ProjectID) {
			out.Rules.Attributes = append(out.Rules.Attributes, sqlengine.AttributeRule{ProjectID: r.ProjectID,
				Key: r.Key, Action: r.Action, To: r.To})
		}
	}
	return out, nil
}

// projectSpec is one project's Files as the spec lists them.
func projectSpec(dir string, id int64, f Files) (sqlengine.Project, error) {
	p := sqlengine.Project{ID: id, SessionGaps: []sqlengine.Gap{}}
	for _, x := range []struct {
		from []string
		to   *[]string
	}{{f.Parquet, &p.Parquet}, {f.Logs, &p.Logs}, {f.Sessions, &p.Sessions}} {
		*x.to = []string{}
		for _, path := range x.from {
			r, err := relPath(dir, path)
			if err != nil {
				return p, err
			}
			*x.to = append(*x.to, r)
		}
	}
	for _, x := range []struct {
		from string
		to   *string
	}{{f.Identities, &p.Identities}, {f.Aliases, &p.Aliases}} {
		if x.from == "" {
			continue
		}
		r, err := relPath(dir, x.from)
		if err != nil {
			return p, err
		}
		*x.to = r
	}
	for _, g := range f.SessionGaps {
		p.SessionGaps = append(p.SessionGaps, sqlengine.Gap{From: g.From, To: g.To})
	}
	return p, nil
}

// relPath is path relative to dir, with forward slashes, refused when it
// is not inside dir.
func relPath(dir, path string) (string, error) {
	r, err := filepath.Rel(dir, path)
	if err != nil || !filepath.IsLocal(r) || r == "." {
		return "", fmt.Errorf("%w: %s is not inside %s", ErrTables, path, dir)
	}
	return filepath.ToSlash(r), nil
}
