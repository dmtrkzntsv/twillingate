package archive

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Engine is the slice of engine.Engine a Reader uses.
type Engine interface {
	Exec(ctx context.Context, sql string) error
	Query(ctx context.Context, sql string, maxRows int) (engine.Result, error)
}

// Rules are the event and attribute rules the tables apply on read
// (raw-first D6): the files keep events as they were written, and a rule
// made later renames, merges or blocks them in every query.
type Rules struct {
	Events     []store.EventRule
	Attributes []store.AttributeRule
}

// Reader answers SQL over every project's files and open logs as the
// tables events, views, product and measures (raw-first D4, D8). The
// tables live in the engine's child: the logs loaded into _tw_hot, the
// Parquet files read through the view _tw_all, the rules applied by the
// view events. They are built on the first query and again whenever a
// file, a log's size or the rules changed since: _tw_state records the
// signature they were built from.
type Reader struct {
	dir  string
	eng  Engine
	keys *keyScanner
	mu   sync.Mutex // one prepare at a time
}

// NewReader reads the files under dir (raw/), which the engine's child
// must be allowed to read.
func NewReader(dir string, eng Engine) (*Reader, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	return &Reader{dir: abs, eng: eng, keys: newKeyScanner()}, nil
}

// Query runs one SELECT over the tables (engine.Query refuses anything
// else) and returns at most maxRows rows (all when maxRows ≤ 0). A file
// the daily pass removed between the listing and the query (a dump
// deletes its log, a merge its days) fails the attempt; the query is
// retried once, on a fresh listing.
func (r *Reader) Query(ctx context.Context, sql string, maxRows int, rules Rules) (engine.Result, error) {
	res, err := r.query(ctx, sql, maxRows, rules)
	if vanished(err) {
		res, err = r.query(ctx, sql, maxRows, rules)
	}
	return res, err
}

func (r *Reader) query(ctx context.Context, sql string, maxRows int, rules Rules) (engine.Result, error) {
	if err := r.prepare(ctx, rules); err != nil {
		return engine.Result{}, err
	}
	return r.eng.Query(ctx, sql, maxRows)
}

// vanished reports an error naming a file that is gone.
func vanished(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var qe *engine.QueryError
	return errors.As(err, &qe) && (strings.Contains(qe.Msg, "No files found") || strings.Contains(qe.Msg, "No such file"))
}

// prepare builds the tables unless the child holds them built from the
// files and rules as they are now. A child without _tw_state (a new one)
// answers a query error, which means: build.
func (r *Reader) prepare(ctx context.Context, rules Rules) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	files, err := ListFiles(r.dir)
	if err != nil {
		return err
	}
	rules = sortRules(rules)
	sig, err := signature(files, rules)
	if err != nil {
		return err
	}
	res, err := r.eng.Query(ctx, "SELECT sig FROM _tw_state", 1)
	var qe *engine.QueryError
	switch {
	case err == nil && len(res.Rows) == 1 && res.Rows[0][0] == sig:
		return nil
	case err != nil && !errors.As(err, &qe):
		return err
	}
	return r.build(ctx, files, rules, sig)
}

// signature identifies the files (paths, sizes, modification times) and
// the rules the tables are built from: a log that grew, a file the daily
// pass wrote or removed and a rule made since each change it.
func signature(files map[int64]Files, rules Rules) (string, error) {
	h := sha256.New()
	for _, id := range sortedIDs(files) {
		for _, p := range append(slices.Clone(files[id].Parquet), files[id].Logs...) {
			st, err := os.Stat(p)
			if err != nil {
				return "", fmt.Errorf("archive: %w", err)
			}
			fmt.Fprintf(h, "%s\x00%d\x00%d\n", p, st.Size(), st.ModTime().UnixNano())
		}
	}
	fmt.Fprintf(h, "%v\n%v\n", rules.Events, rules.Attributes)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// build loads the logs into _tw_hot, defines _tw_all over the Parquet
// files and _tw_hot, then events (with the rules), views, product and
// measures, and records sig. Every fixed column is there at its type even
// with no file and no log (emptyRow); v, the line format's version, is not.
func (r *Reader) build(ctx context.Context, files map[int64]Files, rules Rules, sig string) error {
	var parquet, logs []string
	for _, id := range sortedIDs(files) {
		parquet = append(parquet, files[id].Parquet...)
		logs = append(logs, files[id].Logs...)
	}
	hot := []string{emptyRow()}
	for _, p := range logs {
		keys, err := r.keys.keys(p)
		if err != nil {
			return err
		}
		hot = append(hot, "SELECT * FROM ("+logSelect(p, columnsOf(keys), true)+")")
	}
	r.keys.keep(logs)
	all := []string{emptyRow(), "SELECT * FROM _tw_hot"}
	if len(parquet) > 0 {
		all = append(all, "SELECT * FROM read_parquet("+sqlList(parquet)+", union_by_name=true)")
	}
	if err := r.eng.Exec(ctx, "CREATE OR REPLACE TABLE _tw_hot AS "+strings.Join(hot, " UNION ALL BY NAME ")+";\n"+
		"CREATE OR REPLACE VIEW _tw_all AS SELECT * EXCLUDE (v) FROM ("+strings.Join(all, " UNION ALL BY NAME ")+")"); err != nil {
		return err
	}
	res, err := r.eng.Query(ctx, "SELECT column_name FROM (DESCRIBE _tw_all)", 0)
	if err != nil {
		return err
	}
	cols := make([]string, len(res.Rows))
	for i, row := range res.Rows {
		cols[i], _ = row[0].(string)
	}
	return r.eng.Exec(ctx, "CREATE OR REPLACE VIEW events AS "+eventsView(cols, rules)+";\n"+
		"CREATE OR REPLACE VIEW views AS SELECT * FROM events WHERE family = 'views';\n"+
		"CREATE OR REPLACE VIEW product AS SELECT * FROM events WHERE family = 'product';\n"+
		"CREATE OR REPLACE VIEW measures AS SELECT * FROM events WHERE family = 'measures';\n"+
		"CREATE OR REPLACE TABLE _tw_state AS SELECT "+quote(sig)+" AS sig")
}

// emptyRow is a SELECT of no rows with every fixed column at its type.
func emptyRow() string {
	cols := make([]string, len(fields))
	for i, f := range fields {
		cols[i] = "CAST(NULL AS " + f.typ + ") AS " + ident(f.name)
	}
	return "SELECT " + strings.Join(cols, ", ") + " WHERE false"
}

// eventsView is the SELECT of the events table: _tw_all, whose columns
// are cols, with the rules applied (raw-first D6), as ingest applies them
// to what it receives:
//   - an event renamed or merged reads under its new name; a blocked one,
//     and one renamed to a blocked name, is left out;
//   - an attribute key renamed or merged reads in its target's column,
//     the target's own value first, then the renamed keys' in key order;
//     the key itself reads NULL, as a blocked key does;
//   - attribute rules cover the project's product and measures events.
//
// A target column no file has yet is added. Column names match
// case-insensitively, as DuckDB's do. rules are sorted (sortRules).
func eventsView(cols []string, rules Rules) string {
	have := map[string]string{} // a column's name, by its lower-case name
	for _, c := range cols {
		have[strings.ToLower(c)] = c
	}
	type nameKey struct {
		project int64
		family  store.Family
		name    string
	}
	blocked := map[nameKey]bool{}
	for _, r := range rules.Events {
		if r.Action == store.EventRuleBlock {
			blocked[nameKey{r.ProjectID, r.Family, r.Name}] = true
		}
	}
	var renames, drops []string
	for _, r := range rules.Events {
		cond := fmt.Sprintf("project_id = %d AND family = %s AND event_name = %s", r.ProjectID, quote(string(r.Family)), quote(r.Name))
		if r.Action == store.EventRuleBlock || blocked[nameKey{r.ProjectID, r.Family, r.To}] {
			drops = append(drops, "("+cond+")")
		} else {
			renames = append(renames, "WHEN "+cond+" THEN "+quote(r.To))
		}
	}
	var replace, add []string
	if len(renames) > 0 {
		replace = append(replace, "CASE "+strings.Join(renames, " ")+" ELSE event_name END AS event_name")
	}

	type keyKey struct {
		project int64
		key     string
	}
	keyBlocked := map[keyKey]bool{}
	for _, r := range rules.Attributes {
		if r.Action == store.EventRuleBlock {
			keyBlocked[keyKey{r.ProjectID, r.Key}] = true
		}
	}
	scope := func(project int64) string {
		return fmt.Sprintf("project_id = %d AND family IN ('product', 'measures')", project)
	}
	arms := map[string][]string{} // a column's CASE arms, by its lower-case name
	names := map[string]string{}  // the column's name, by its lower-case name
	var order []string
	arm := func(name, when, then string) {
		low := strings.ToLower(name)
		if _, ok := names[low]; !ok {
			names[low] = name
			order = append(order, low)
		}
		arms[low] = append(arms[low], "WHEN "+when+" THEN "+then)
	}
	sources := map[keyKey][]string{} // the columns renamed into a target, in key order
	var targets []keyKey
	for _, r := range rules.Attributes {
		col, ok := have[strings.ToLower("attr_"+r.Key)]
		if !ok {
			continue
		}
		arm(col, scope(r.ProjectID), "NULL")
		if r.Action != store.EventRuleRename || keyBlocked[keyKey{r.ProjectID, r.To}] {
			continue
		}
		t := keyKey{r.ProjectID, r.To}
		if sources[t] == nil {
			targets = append(targets, t)
		}
		sources[t] = append(sources[t], ident(col))
	}
	for _, t := range targets {
		name, vals := "attr_"+t.key, sources[t]
		if col, ok := have[strings.ToLower(name)]; ok {
			name, vals = col, append([]string{ident(col)}, vals...)
		}
		arm(name, scope(t.project), "coalesce("+strings.Join(vals, ", ")+")")
	}
	for _, low := range order {
		name := names[low]
		_, exists := have[low]
		otherwise := "NULL"
		if exists {
			otherwise = ident(name)
		}
		e := "CASE " + strings.Join(arms[low], " ") + " ELSE " + otherwise + " END AS " + ident(name)
		if exists {
			replace = append(replace, e)
		} else {
			add = append(add, e)
		}
	}
	sel := "SELECT *"
	if len(replace) > 0 {
		sel += " REPLACE (" + strings.Join(replace, ", ") + ")"
	}
	for _, e := range add {
		sel += ", " + e
	}
	sel += " FROM _tw_all"
	if len(drops) > 0 {
		sel += " WHERE NOT (" + strings.Join(drops, " OR ") + ")"
	}
	return sel
}

// sortRules orders copies of the rules by project, then family and name
// (events) or key (attributes): the signature and the view must not
// depend on the order the caller listed them in.
func sortRules(r Rules) Rules {
	ev := slices.Clone(r.Events)
	slices.SortFunc(ev, func(a, b store.EventRule) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.Family, b.Family), cmp.Compare(a.Name, b.Name))
	})
	at := slices.Clone(r.Attributes)
	slices.SortFunc(at, func(a, b store.AttributeRule) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.Key, b.Key))
	})
	return Rules{Events: ev, Attributes: at}
}

func sortedIDs(files map[int64]Files) []int64 {
	ids := make([]int64, 0, len(files))
	for id := range files {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
