package native

import (
	"fmt"
	"maps"
	"regexp"
	"strings"
)

// Define is a text column a query defines for itself, named def_<name>:
// per row, the label of the first case whose filters it passes, else
// Else (NULL when empty). A dimension, a series and a filter, the
// query's or a metric's, may name it as any text column.
type Define struct {
	Name  string `json:"name" jsonschema:"def_ and a lower-case name (def_feature, def_deployment): the column's name, as dimensions, series and filters name it"`
	Cases []Case `json:"cases" jsonschema:"1 to 20 cases, in order: a row reads the label of the first whose filters it passes"`
	Else  string `json:"else,omitempty" jsonschema:"the label of a row no case takes; omitted, NULL (empty)"`
}

// Case is one of a defined column's labels and the rows that read it.
type Case struct {
	Label string   `json:"label" jsonschema:"the value the column reads"`
	Where []Filter `json:"where" jsonschema:"1 or more filters, as the query's where (an any_of among them for an OR), all of which a row passes to read label; none names a def_ column"`
}

// The bounds of a query's defined columns.
const (
	maxDefines = 5
	maxCases   = 20
)

// defPrefix starts every defined column's name; no file's column does.
const defPrefix = "def_"

var defName = regexp.MustCompile(`^def_[a-z][a-z0-9_]{0,39}$`)

// isDefined says whether column names a defined column (def_…).
func isDefined(column string) bool { return strings.HasPrefix(column, defPrefix) }

// validateDefines checks q's defined columns, and that every def_ column
// q names is one of them.
func (q Query) validateDefines() error {
	if len(q.Define) > 0 && q.Kind != "stat" && q.Kind != "trend" && q.Kind != "breakdown" {
		return invalid("define", "only a stat, a trend or a breakdown defines columns")
	}
	if len(q.Define) > maxDefines {
		return invalid("define", "at most %d, got %d", maxDefines, len(q.Define))
	}
	names := map[string]bool{}
	for i, d := range q.Define {
		p := fmt.Sprintf("define[%d]", i)
		switch n := len(d.Cases); {
		case !defName.MatchString(d.Name):
			return invalid(p+".name", "%q is not def_ and a lower-case name of letters, digits and _", d.Name)
		case names[d.Name]:
			return invalid(p+".name", "%q is defined twice", d.Name)
		case n < 1 || n > maxCases:
			return invalid(p+".cases", "1 to %d, got %d", maxCases, n)
		}
		names[d.Name] = true
		for j, c := range d.Cases {
			cp := fmt.Sprintf("%s.cases[%d]", p, j)
			if c.Label == "" {
				return invalid(cp+".label", "a case's label is not empty")
			}
			if len(c.Where) == 0 {
				return invalid(cp+".where", "a case takes 1 or more filters")
			}
			for k, f := range c.Where {
				fp := fmt.Sprintf("%s.where[%d]", cp, k)
				if col, ok := namesDefined(f); ok {
					return invalid(fp, "%s is a defined column; a case's filters name the table's", col)
				}
				if err := f.validate(q.Table, fp); err != nil {
					return err
				}
			}
		}
	}
	used := func(col, p string) error {
		if isDefined(col) && !names[col] {
			return invalid(p, "%q is not defined; define lists the def_ columns", col)
		}
		return nil
	}
	for i, d := range q.Dimensions {
		if err := used(d, fmt.Sprintf("dimensions[%d]", i)); err != nil {
			return err
		}
	}
	if err := used(q.Series, "series"); err != nil {
		return err
	}
	if err := usedIn(q.Where, "where", used); err != nil {
		return err
	}
	var metric func(m Metric, p string) error
	metric = func(m Metric, p string) error {
		if err := usedIn(m.Where, p+".where", used); err != nil {
			return err
		}
		for side, s := range map[string]*Metric{"num": m.Num, "den": m.Den} {
			if s != nil {
				if err := metric(*s, p+"."+side); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for i, m := range q.Metrics {
		if err := metric(m, fmt.Sprintf("metrics[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}

// usedIn checks each column the filters name with used, under p.
func usedIn(where []Filter, p string, used func(col, p string) error) error {
	for i, f := range where {
		fp := fmt.Sprintf("%s[%d]", p, i)
		if err := used(f.Column, fp+".column"); err != nil {
			return err
		}
		for j, c := range f.Columns {
			if err := used(c, fmt.Sprintf("%s.columns[%d]", fp, j)); err != nil {
				return err
			}
		}
		if err := usedIn(f.Any, fp+".any", used); err != nil {
			return err
		}
	}
	return nil
}

// namesDefined is the first def_ column f names, any_set's and any_of's
// included.
func namesDefined(f Filter) (string, bool) {
	if isDefined(f.Column) {
		return f.Column, true
	}
	for _, c := range f.Columns {
		if isDefined(c) {
			return c, true
		}
	}
	for _, g := range f.Any {
		if c, ok := namesDefined(g); ok {
			return c, true
		}
	}
	return "", false
}

// defined is a defined column compiled: its cases' filters.
type defined struct {
	name   string
	labels []string
	cases  [][]filter
	other  string
}

// compileDefines compiles q's defined columns; Validate has checked them.
func compileDefines(defs []Define) ([]defined, error) {
	out := make([]defined, len(defs))
	for i, d := range defs {
		out[i] = defined{name: d.Name, other: d.Else, labels: make([]string, len(d.Cases)), cases: make([][]filter, len(d.Cases))}
		for j, c := range d.Cases {
			fs, err := compileFilters(c.Where, fmt.Sprintf("define[%d].cases[%d].where", i, j))
			if err != nil {
				return nil, err
			}
			out[i].labels[j], out[i].cases[j] = c.Label, fs
		}
	}
	return out, nil
}

// withDefined is b with the defined columns added: a new batch sharing
// b's columns, since a shared scan hands b to other queries too.
func withDefined(b *Batch, defs []defined) (*Batch, error) {
	out := &Batch{N: b.N, Cols: maps.Clone(b.Cols)}
	for _, d := range defs {
		pass := make([][]bool, len(d.cases))
		for j, fs := range d.cases {
			keep, err := passRows(b, fs, nil)
			if err != nil {
				return nil, err
			}
			pass[j] = keep
		}
		out.Cols[d.name] = textColumn(b.N, func(i int) (string, bool) {
			for j := range pass {
				if pass[j][i] {
					return d.labels[j], true
				}
			}
			return d.other, d.other != ""
		})
	}
	return out, nil
}

// definedColumns are the columns a scan reads for the defined columns:
// their cases' filters'.
func definedColumns(defs []defined) []string {
	var out []string
	for _, d := range defs {
		for _, fs := range d.cases {
			for _, f := range fs {
				out = append(out, f.columns()...)
			}
		}
	}
	return out
}
