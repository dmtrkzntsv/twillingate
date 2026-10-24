package archive

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Engine is the slice of engine.Engine a Reader uses.
type Engine interface {
	Exec(ctx context.Context, sql string) error
	Query(ctx context.Context, sql string, maxRows int, args ...engine.Arg) (engine.Result, error)
}

// ErrLoadingLogs marks a failure to load the open logs into the child's
// memory. The engine's error is wrapped with it (errors.As finds the
// *engine.QueryError): the logs that do not fit ENGINE_MEMORY_LIMIT are
// not a query's own doing.
var ErrLoadingLogs = errors.New("archive: loading the open logs")

// Rules are the event and attribute rules the tables apply on read
// (raw-first D6): the files keep events as they were written, and a rule
// made later renames, merges or blocks them in every query.
type Rules struct {
	Events     []store.EventRule
	Attributes []store.AttributeRule
}

// Reader answers SQL over every project's files and open logs as the
// tables events, views, product and measures (raw-first D4, D8), and
// sessions, identities and aliases (D16–D18): the derived files that are
// current, the rest derived on read. The tables live in the engine's child:
// the logs loaded into _tw_hot, the Parquet files read through the view
// _tw_all, the rules applied by the view events. They are built on the first query and again whenever a
// file, a log's size or the rules changed since: _tw_state records the
// signature they were built from.
type Reader struct {
	dir  string
	eng  Engine
	keys *KeyScanner
	mu   sync.Mutex // one prepare at a time

	confinedMu sync.Mutex
	confined   map[[sha256.Size]byte]bool // SQL Confine passed, by its hash
}

// Tables are the tables a Reader answers SQL over: confined SQL
// (Scoped.Confine) reads these and nothing else.
var Tables = []string{"events", "views", "product", "measures", "sessions", "identities", "aliases"}

// maxConfined bounds the SQL a Reader remembers passing Confine: a
// dashboard's widgets run the same few statements on every load.
const maxConfined = 1024

// NewReader reads the files under dir (raw/), which the engine's child
// must be allowed to read.
func NewReader(dir string, eng Engine) (*Reader, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	return &Reader{dir: abs, eng: eng, keys: NewKeyScanner()}, nil
}

// Query runs one SELECT over the tables (engine.Query refuses anything
// else) and returns at most maxRows rows (all when maxRows ≤ 0). It is
// QueryScoped over every project, with no arguments.
func (r *Reader) Query(ctx context.Context, sql string, maxRows int, rules Rules) (engine.Result, error) {
	return r.QueryScoped(ctx, Scoped{SQL: sql, MaxRows: maxRows}, rules)
}

// Scoped is one query for QueryScoped.
type Scoped struct {
	SQL string
	// Args are bound to the $names in SQL (engine.Engine.Query).
	Args    []engine.Arg
	MaxRows int
	// Scope limits all seven tables to Project's rows; without it the
	// query reads every project's. A scope on a project with no rows (0
	// among them: no project has that id) reads none.
	Scope   bool
	Project int64
	// Confine refuses SQL that reads anything but Tables and its own WITH
	// names (enginesql.Confine) before it runs: for SQL a person wrote,
	// which Scope alone keeps neither on one project nor in raw/.
	Confine bool
}

// QueryScoped is Query with arguments and, with Scope, the seven tables
// shadowed by CTEs over Project's rows only:
//
//	WITH events AS NOT MATERIALIZED (SELECT * FROM main.events WHERE project_id = <id>),
//	     views AS NOT MATERIALIZED (SELECT * FROM events WHERE family = 'views'), …,
//	     sessions AS NOT MATERIALIZED (SELECT * FROM main.sessions WHERE project_id = <id>), …
//	SELECT * FROM (<sql>
//	)
//
// so a query written against the tables reads one project's rows without
// a filter of its own, and the project's rows pass through the rules once.
// <id> is the int64 printed here, not text from a caller. Without Scope
// the SQL runs as it is.
//
// Scoping is not isolation: the CTEs shadow the tables' names, and SQL
// that names main.events, _tw_all or read_parquet reads past them, every
// project's rows included. It keeps SQL written against the tables on
// one project; it is no boundary against SQL written to leave it. Confine
// is that boundary: it refuses those names and every table function that
// reads files, so SQL from a person runs with it.
//
// rules is the full rule set of every project, not the caller's share of
// it: the tables live in the child, which every caller of the engine
// shares, so a subset would apply to (and be read by) the other callers'
// queries too.
//
// A file the daily pass removed between the listing and the query (a dump
// deletes its log, a merge its days) fails the attempt, and so does a
// child the engine retired in between (the query lands on a fresh one,
// which has no tables). Each attempt lists the files afresh and rebuilds
// the tables if need be, so the next one sees what the pass left. A pass
// that works through months of closed logs (the boot after an upgrade)
// removes files for as long as it runs, so the query is retried up to
// maxAttempts times, backing off from retryBackoff to retryBackoffMax
// between attempts, until it succeeds, fails otherwise, or ctx is done.
// The last error is returned.
func (r *Reader) QueryScoped(ctx context.Context, q Scoped, rules Rules) (engine.Result, error) {
	return r.query(ctx, q, rules, func() (map[int64]Files, error) { return ListFiles(r.dir) }, true)
}

// QueryFiles is QueryScoped over files, not over what ListFiles lists in
// the Reader's directory: for an engine answering an external SQL
// engine's request, whose table spec (ResolveTables) names the files. The
// tables are rebuilt whenever files or rules differ from what they were
// built from, as QueryScoped rebuilds them. A file that vanished fails the
// query at once (Vanished): the list is the caller's, so a retry would
// read the same one. A child replaced in between is retried as QueryScoped retries it.
func (r *Reader) QueryFiles(ctx context.Context, q Scoped, files map[int64]Files, rules Rules) (engine.Result, error) {
	return r.query(ctx, q, rules, func() (map[int64]Files, error) { return files, nil }, false)
}

// query is QueryScoped and QueryFiles: the files from list on each
// attempt, a vanished file retried when relist.
func (r *Reader) query(ctx context.Context, q Scoped, rules Rules, list func() (map[int64]Files, error), relist bool) (engine.Result, error) {
	backoff := retryBackoff
	for attempt := 1; ; attempt++ {
		res, err := r.queryScoped(ctx, q, rules, list)
		if err == nil || !(relist && Vanished(err) || lostTables(err)) || attempt == maxAttempts {
			return res, err
		}
		select {
		case <-ctx.Done():
			return res, err
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, retryBackoffMax)
	}
}

// The retry bounds of QueryScoped; variables so a test can shorten them.
var (
	maxAttempts     = 10
	retryBackoff    = 50 * time.Millisecond
	retryBackoffMax = time.Second
)

func (r *Reader) queryScoped(ctx context.Context, q Scoped, rules Rules, list func() (map[int64]Files, error)) (engine.Result, error) {
	if q.Confine {
		if err := r.confine(ctx, q.SQL); err != nil {
			return engine.Result{}, err
		}
	}
	if err := r.prepare(ctx, rules, list); err != nil {
		return engine.Result{}, err
	}
	sql := q.SQL
	if q.Scope {
		sql = scopedSQL(q.Project, sql)
	}
	return r.eng.Query(ctx, sql, q.MaxRows, q.Args...)
}

// confine is enginesql.Confine of sql over Tables, remembered once passed:
// the verdict depends on the text alone.
func (r *Reader) confine(ctx context.Context, sql string) error {
	key := sha256.Sum256([]byte(sql))
	r.confinedMu.Lock()
	ok := r.confined[key]
	r.confinedMu.Unlock()
	if ok {
		return nil
	}
	if err := enginesql.Confine(ctx, r.eng, sql, Tables); err != nil {
		return err
	}
	r.confinedMu.Lock()
	defer r.confinedMu.Unlock()
	if r.confined == nil || len(r.confined) >= maxConfined {
		r.confined = map[[sha256.Size]byte]bool{}
	}
	r.confined[key] = true
	return nil
}

// scopedSQL is sql over the seven tables as project's rows only. The
// newline before the closing paren ends a line comment sql may end with.
func scopedSQL(project int64, sql string) string {
	id := strconv.FormatInt(project, 10)
	family := func(name string) string {
		return name + " AS NOT MATERIALIZED (SELECT * FROM events WHERE family = '" + name + "')"
	}
	return "WITH events AS NOT MATERIALIZED (SELECT * FROM main.events WHERE project_id = " + id + "),\n" +
		family("views") + ",\n" + family("product") + ",\n" +
		"measures AS NOT MATERIALIZED (" + measuresSelect("events") + "),\n" +
		"sessions AS NOT MATERIALIZED (SELECT * FROM main.sessions WHERE project_id = " + id + "),\n" +
		"identities AS NOT MATERIALIZED (SELECT * FROM main.identities WHERE project_id = " + id + "),\n" +
		"aliases AS NOT MATERIALIZED (SELECT * FROM main.aliases WHERE project_id = " + id + ")\n" +
		"SELECT * FROM (" + enginesql.Trim(sql) + "\n)"
}

// measuresSelect is the measures table over events (a table or CTE): the
// measures rows, and bucket, the log-scale bucket of value (base 1.04, so
// buckets about 4% wide; -1000 for a value of 1e-17 or less), as the
// SQLite views compute it.
func measuresSelect(events string) string {
	return "SELECT *, CASE WHEN value > 1e-17 THEN CAST(ceil(ln(value) / ln(1.04)) AS INTEGER) ELSE -1000 END AS bucket FROM " +
		events + " WHERE family = 'measures'"
}

// missingTable matches the error of a query naming one of the reader's own
// tables or views that the child does not have. A user's typo in a table
// name is not one: only the names the reader defines.
var missingTable = regexp.MustCompile(`(?:Table|View) with name "?(?:events|views|product|measures|sessions|identities|aliases|_tw_[A-Za-z0-9_]*)"? does not exist`)

// lostTables reports an error saying the reader's tables are not there:
// the child was replaced since they were built.
func lostTables(err error) bool {
	var qe *engine.QueryError
	return errors.As(err, &qe) && missingTable.MatchString(qe.Msg)
}

// Vanished reports an error naming a file that is gone: one QueryFiles
// met because a file its caller listed was removed before the engine read
// it. An engine answers it stale (sqlengine.KindStale), so its caller
// lists the files afresh.
func Vanished(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var qe *engine.QueryError
	return errors.As(err, &qe) && (strings.Contains(qe.Msg, "No files found") || strings.Contains(qe.Msg, "No such file"))
}

// prepare builds the tables unless the child holds them built from the
// files and rules as they are now. A child without _tw_state (a new one)
// answers a query error, which means: build.
func (r *Reader) prepare(ctx context.Context, rules Rules, list func() (map[int64]Files, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	files, err := list()
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
		f := files[id]
		paths := append(slices.Clone(f.Parquet), f.Logs...)
		paths = append(paths, f.Sessions...)
		for _, p := range []string{f.Identities, f.Aliases} {
			if p != "" {
				paths = append(paths, p)
			}
		}
		for _, p := range paths {
			st, err := os.Stat(p)
			if err != nil {
				return "", fmt.Errorf("archive: %w", err)
			}
			fmt.Fprintf(h, "%s\x00%d\x00%d\n", p, st.Size(), st.ModTime().UnixNano())
		}
	}
	if err := json.NewEncoder(h).Encode(rules); err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// build loads the logs into _tw_hot_next, then, in one transaction, makes
// it _tw_hot and defines _tw_all over the Parquet files and _tw_hot, then
// events (with the rules), views, product and measures, sessions,
// identities and aliases, and records sig.
// A query that runs meanwhile on another connection of the child sees the
// tables as they were or as they are now, never the new logs through the
// old file list. Every fixed column is there at its type even with no file
// and no log (emptyRow); v, the line format's version, is not.
func (r *Reader) build(ctx context.Context, files map[int64]Files, rules Rules, sig string) error {
	var parquet, logs, sessions, idFiles, alFiles, gaps []string
	var idCurrent, idStale, alCurrent, alStale []int64
	for _, id := range sortedIDs(files) {
		f := files[id]
		parquet = append(parquet, f.Parquet...)
		logs = append(logs, f.Logs...)
		sessions = append(sessions, f.Sessions...)
		for _, g := range f.SessionGaps {
			gaps = append(gaps, fmt.Sprintf("(project_id = %d AND day BETWEEN DATE %s AND DATE %s)", id, quote(g.From), quote(g.To)))
		}
		if f.Identities != "" {
			idFiles, idCurrent = append(idFiles, f.Identities), append(idCurrent, id)
		} else {
			idStale = append(idStale, id)
		}
		if f.Aliases != "" {
			alFiles, alCurrent = append(alFiles, f.Aliases), append(alCurrent, id)
		} else {
			alStale = append(alStale, id)
		}
	}
	hot := []string{emptyRow()}
	for _, p := range logs {
		keys, err := r.keys.Keys(p)
		if err != nil {
			return err
		}
		hot = append(hot, "SELECT * FROM ("+logSelect(p, columnsOf(keys), true)+")")
	}
	r.keys.Keep(logs)
	all := func(hot string) string {
		union := []string{emptyRow(), "SELECT * FROM " + hot}
		if len(parquet) > 0 {
			union = append(union, "SELECT * FROM read_parquet("+sqlList(parquet)+", union_by_name=true)")
		}
		return "SELECT * EXCLUDE (v) FROM (" + strings.Join(union, " UNION ALL BY NAME ") + ")"
	}
	if err := r.eng.Exec(ctx, "CREATE OR REPLACE TABLE _tw_hot_next AS "+strings.Join(hot, " UNION ALL BY NAME ")); err != nil {
		return fmt.Errorf("%w: %w", ErrLoadingLogs, err)
	}
	res, err := r.eng.Query(ctx, "SELECT column_name FROM (DESCRIBE "+all("_tw_hot_next")+")", 0)
	if err != nil {
		return err
	}
	cols := make([]string, len(res.Rows))
	for i, row := range res.Rows {
		cols[i], _ = row[0].(string)
	}
	return r.eng.Exec(ctx, "BEGIN TRANSACTION;\n"+
		"DROP TABLE IF EXISTS _tw_hot;\n"+
		"ALTER TABLE _tw_hot_next RENAME TO _tw_hot;\n"+
		"CREATE OR REPLACE VIEW _tw_all AS "+all("_tw_hot")+";\n"+
		"CREATE OR REPLACE VIEW events AS "+eventsView(cols, rules)+";\n"+
		"CREATE OR REPLACE VIEW views AS SELECT * FROM events WHERE family = 'views';\n"+
		"CREATE OR REPLACE VIEW product AS SELECT * FROM events WHERE family = 'product';\n"+
		"CREATE OR REPLACE VIEW measures AS "+measuresSelect("events")+";\n"+
		"CREATE OR REPLACE VIEW sessions AS "+sessionsView(sessions, gaps)+";\n"+
		"CREATE OR REPLACE VIEW identities AS "+derivedView(identitiesSelect, identitiesCombine, idFiles, idCurrent, idStale)+";\n"+
		"CREATE OR REPLACE VIEW aliases AS "+derivedView(aliasesSelect, aliasesCombine, alFiles, alCurrent, alStale)+";\n"+
		"CREATE OR REPLACE TABLE _tw_state AS SELECT "+quote(sig)+" AS sig;\n"+
		"COMMIT")
}

// sessionsView is the sessions table: the current session files, and the
// sessions of the gaps (the days no current file covers, as conditions on
// project_id and day) derived from _tw_all. The typed empty SELECT fixes
// every column's type when there is neither.
func sessionsView(files, gaps []string) string {
	union := []string{"SELECT * FROM (" + sessionsSelect("("+emptyRow()+")") + ")"}
	if len(files) > 0 {
		union = append(union, "SELECT * FROM read_parquet("+sqlList(files)+", union_by_name=true)")
	}
	if len(gaps) > 0 {
		union = append(union, "SELECT * FROM ("+sessionsSelect("(SELECT * FROM _tw_all WHERE "+strings.Join(gaps, " OR ")+")")+")")
	}
	return strings.Join(union, " UNION ALL BY NAME ")
}

// derivedView is identities or aliases (sel and combine, derive.go): the
// current files, one per project in current; what they lack, derived on
// read from the open logs of those projects and from every event of the
// projects in stale; folded into one row per key.
func derivedView(sel, combine func(string) string, files []string, current, stale []int64) string {
	parts := []string{"SELECT * FROM (" + sel("("+emptyRow()+")") + ")"}
	if len(files) > 0 {
		parts = append(parts, "SELECT * FROM read_parquet("+sqlList(files)+", union_by_name=true)")
	}
	live := "(SELECT * FROM _tw_all WHERE " + inProjects(stale) +
		" UNION ALL BY NAME SELECT * EXCLUDE (v) FROM _tw_hot WHERE " + inProjects(current) + ")"
	parts = append(parts, "SELECT * FROM ("+sel(live)+")")
	return combine(strings.Join(parts, " UNION ALL BY NAME "))
}

// inProjects is a condition on project_id: one of ids (false for none).
func inProjects(ids []int64) string {
	if len(ids) == 0 {
		return "false"
	}
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return "project_id IN (" + strings.Join(s, ", ") + ")"
}

// literal is s as a SQL string literal, with each NUL spelled chr(0): SQL
// text is cut at a NUL.
func literal(s string) string {
	parts := strings.Split(s, "\x00")
	for i, p := range parts {
		parts[i] = quote(p)
	}
	return strings.Join(parts, " || chr(0) || ")
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
		cond := fmt.Sprintf("project_id = %d AND family = %s AND event_name = %s", r.ProjectID, quote(string(r.Family)), literal(r.Name))
		if r.Action == store.EventRuleBlock || blocked[nameKey{r.ProjectID, r.Family, r.To}] {
			drops = append(drops, "("+cond+")")
		} else {
			renames = append(renames, "WHEN "+cond+" THEN "+literal(r.To))
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
		if r.Action == store.EventRuleRename && strings.EqualFold(visible("attr_"+r.Key), visible("attr_"+r.To)) {
			continue // one column already: the values read there as they are
		}
		col, ok := have[strings.ToLower(visible("attr_"+r.Key))]
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
		name, vals := visible("attr_"+t.key), sources[t]
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

// logSelect is a log's lines as typed rows, as a reader of the open logs
// reads them through DuckDB (the reader's events view): cols, every id
// once, the line appended first kept, ordered by family and ts. The fixed
// keys take their type from fields, every other key (attr_<key>, the key
// as stored) is VARCHAR: DuckDB inferring the types from one day's values
// would rewrite a value ("ABCDEF01-…" read as a UUID comes back lower
// case, "12:30" as a TIME comes back "12:30:00"). A strict select fails on
// a line that is not JSON; a tolerant one skips it, for a reader that may
// meet the torn last line of a log ingest is still writing
// (read_json_objects with ignore_errors reads such a line as a NULL
// object). The dump reads the logs in Go (dumpLog); the strict select is
// its oracle in the tests (dumpQuery).
//
// The lines are numbered in file order (WITH ORDINALITY), the first number
// per id is found over the ids and numbers alone, and only then are the
// lines it names sorted (a sort spills to disk; a hash table of whole
// lines does not) and their columns extracted. Each key is extracted by
// JSON pointer, so its case is kept; DuckDB's column names are not
// case-sensitive, though, so of keys equal but for case the first in byte
// order keeps its name and each next one is named <key>_<n>, the first n
// free: attr_Plan, attr_plan_1 (columnsOf).
func logSelect(path string, cols []column, tolerant bool) string {
	paths := make([]string, len(cols))
	sel := make([]string, len(cols))
	for i, c := range cols {
		paths[i] = c.pointer()
		sel[i] = "CAST(f[" + strconv.Itoa(i+1) + "] AS " + c.typ + ") AS " + c.ident()
	}
	src := "read_json_objects(" + quote(path) + ", format='newline_delimited'"
	if tolerant {
		src += ", ignore_errors=true"
	}
	lines := "(SELECT json, ordinality FROM " + src + ") WITH ORDINALITY WHERE json IS NOT NULL)"
	firsts := "SELECT min(ordinality) FROM (SELECT json_extract_string(json, '/id') AS id, ordinality FROM " + lines + ") GROUP BY id"
	unique := "SELECT json FROM (SELECT json, ordinality, json_extract_string(json, '/family') AS family, " +
		"CAST(json_extract_string(json, '/ts') AS TIMESTAMP) AS ts FROM " + lines + ") WHERE ordinality IN (" + firsts + ") ORDER BY family, ts"
	return "SELECT " + strings.Join(sel, ", ") + " FROM (SELECT json_extract_string(json, [" + strings.Join(paths, ", ") +
		"]) AS f FROM (" + unique + "))"
}

// sqlList is paths as a SQL list of string literals.
func sqlList(paths []string) string {
	q := make([]string, len(paths))
	for i, p := range paths {
		q[i] = quote(p)
	}
	return "[" + strings.Join(q, ", ") + "]"
}
