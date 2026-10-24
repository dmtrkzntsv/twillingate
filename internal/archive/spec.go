package archive

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// ErrTables marks a table spec an engine refuses to read: a path that is
// absolute or leaves the events directory, a project listed twice, or a
// version it does not know.
var ErrTables = errors.New("archive: bad table spec")

// TableSpec is the table spec of the files under dir (raw/) for an
// external SQL engine: what ListFiles lists, each path relative to dir,
// and rules. With projects, only those projects' files and rules are in
// it (a project with no files is left out, as ListFiles leaves it out);
// nil means every project. Projects are in id order, the rules sorted as
// the Reader sorts them, and every list is empty rather than null.
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

// ResolveTables is the engine's side of TableSpec: t's files under dir,
// each path checked to be relative and inside dir, and its rules. A path
// that is not, a project listed twice, a session gap that is not two dates and a version other than
// sqlengine.TablesVersion are ErrTables.
func ResolveTables(dir string, t sqlengine.Tables) (map[int64]Files, Rules, error) {
	if t.Version != sqlengine.TablesVersion {
		return nil, Rules{}, fmt.Errorf("%w: version %d, want %d", ErrTables, t.Version, sqlengine.TablesVersion)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, Rules{}, fmt.Errorf("archive: %w", err)
	}
	resolve := func(rel string) (string, error) {
		local := filepath.FromSlash(rel)
		if !filepath.IsLocal(local) || filepath.Clean(local) == "." {
			return "", fmt.Errorf("%w: %q is not a path inside the events directory", ErrTables, rel)
		}
		return filepath.Join(abs, local), nil
	}
	resolveAll := func(rels []string) ([]string, error) {
		var out []string
		for _, r := range rels {
			p, err := resolve(r)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return out, nil
	}
	files := map[int64]Files{}
	for _, p := range t.Projects {
		if _, dup := files[p.ID]; dup {
			return nil, Rules{}, fmt.Errorf("%w: project %d is listed twice", ErrTables, p.ID)
		}
		var f Files
		if f.Parquet, err = resolveAll(p.Parquet); err != nil {
			return nil, Rules{}, err
		}
		if f.Logs, err = resolveAll(p.Logs); err != nil {
			return nil, Rules{}, err
		}
		if f.Sessions, err = resolveAll(p.Sessions); err != nil {
			return nil, Rules{}, err
		}
		for _, x := range []struct {
			rel string
			to  *string
		}{{p.Identities, &f.Identities}, {p.Aliases, &f.Aliases}} {
			if x.rel == "" {
				continue
			}
			if *x.to, err = resolve(x.rel); err != nil {
				return nil, Rules{}, err
			}
		}
		for _, g := range p.SessionGaps {
			if _, err := civil.Parse(g.From); err != nil {
				return nil, Rules{}, fmt.Errorf("%w: project %d: session gap from %q", ErrTables, p.ID, g.From)
			}
			if _, err := civil.Parse(g.To); err != nil {
				return nil, Rules{}, fmt.Errorf("%w: project %d: session gap to %q", ErrTables, p.ID, g.To)
			}
			f.SessionGaps = append(f.SessionGaps, Span{From: g.From, To: g.To})
		}
		files[p.ID] = f
	}
	var rules Rules
	for _, r := range t.Rules.Events {
		rules.Events = append(rules.Events, store.EventRule{ProjectID: r.ProjectID, Family: store.Family(r.Family),
			Name: r.Name, Action: r.Action, To: r.To})
	}
	for _, r := range t.Rules.Attributes {
		rules.Attributes = append(rules.Attributes, store.AttributeRule{ProjectID: r.ProjectID, Key: r.Key,
			Action: r.Action, To: r.To})
	}
	return files, rules, nil
}
