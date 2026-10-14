package archive

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// Execer runs a statement in the engine (engine.Engine.Exec): the archive
// is the one caller allowed to write through it.
type Execer interface {
	Exec(ctx context.Context, sql string) error
}

// Archive maintains the files the Log fills.
type Archive struct {
	log    *Log
	eng    Execer
	logger *slog.Logger
}

func New(log *Log, eng Execer, logger *slog.Logger) *Archive {
	return &Archive{log: log, eng: eng, logger: logger}
}

var (
	logName     = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.log$`)
	closedName  = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.\d+\.closed$`)
	dumpingName = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})(\.\d+)?\.parquet\.dumping$`)
	anyMonth    = regexp.MustCompile(`^(\d{4}-\d{2})(-\d{2})?\.`)
	dayFileName = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})(\.\d+)?\.parquet$`)
)

// Maintain runs the file side of the daily pass for every project
// directory: rotate and dump the logs of days before today, merge every
// month whose last day is past the late window, delete months entirely
// older than retentionDays. A project's failure is logged and the next
// one goes on (the daily pass's rule); only failing to list the projects
// is returned.
func (a *Archive) Maintain(ctx context.Context, today civil.Date, lateDays, retentionDays int) error {
	projects, err := a.Projects()
	if err != nil {
		return err
	}
	for _, project := range projects {
		if err := a.maintainProject(ctx, project, today, lateDays, retentionDays); err != nil {
			a.logger.Error("archive: maintenance", "project", project, "error", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

// Projects lists, in ascending order, the ids of the projects with a
// directory under raw/.
func (a *Archive) Projects() ([]int64, error) {
	entries, err := os.ReadDir(a.log.Dir())
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	var ids []int64
	for _, e := range entries {
		if id, err := strconv.ParseInt(e.Name(), 10, 64); err == nil && e.IsDir() {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func (a *Archive) maintainProject(ctx context.Context, project int64, today civil.Date, lateDays, retentionDays int) error {
	dir := filepath.Join(a.log.Dir(), strconv.FormatInt(project, 10))
	names, err := list(dir)
	if err != nil {
		return err
	}
	// A failing step is collected and the rest still run: one bad day must
	// not hold back the project's other dumps, merges or retention.
	var errs []error
	// 1. Close every log of a day before today.
	for _, n := range names {
		if m := logName.FindStringSubmatch(n); m != nil && m[1] < today.String() {
			if _, err := a.log.Rotate(project, m[1]); err != nil {
				errs = append(errs, err)
			}
		}
	}
	// 2. Claim every closed log (the ones a crashed pass left included) for
	// its target file, then dump every claimed log.
	if names, err = list(dir); err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, n := range names {
		if m := closedName.FindStringSubmatch(n); m != nil {
			if err := a.claim(dir, n, m[1]); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if names, err = list(dir); err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, n := range names {
		if dumpingName.MatchString(n) {
			if err := a.dump(ctx, dir, n); err != nil {
				errs = append(errs, err)
			}
		}
	}
	// 3. Delete the expired months; 4. merge every final month. A month goes
	// once its last day is at least retentionDays old (D5: entirely older
	// than the window), whatever the dumps did.
	if names, err = list(dir); err != nil {
		return errors.Join(append(errs, err)...)
	}
	keep := today.AddDays(-retentionDays)
	for _, month := range months(names) {
		last := lastDay(month)
		if !keep.Before(last) {
			if err := removeMonth(dir, names, month); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if last.AddDays(lateDays).Before(today) && !open(names, month) {
			if err := a.merge(ctx, dir, names, month); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// claim ties a closed log to the file its dump will write: it picks the
// day's file, or the next free segment, and renames the log to
// <target>.dumping, so a dump that stops after writing the target is never
// repeated into another segment (dump finds the target and only removes the
// log). A log with nothing to dump is removed.
func (a *Archive) claim(dir, closed, day string) error {
	src := filepath.Join(dir, closed)
	// A log rotated after a failed cut-back can end in a torn line, which
	// read_ndjson would fail on every pass.
	if err := cutTornLine(src); err != nil {
		return err
	}
	if st, err := os.Stat(src); err == nil && st.Size() == 0 {
		if err := os.Remove(src); err != nil {
			return fmt.Errorf("archive: %w", err)
		}
		return nil
	}
	out := filepath.Join(dir, day+".parquet")
	for n := 1; exists(out) || exists(out+".dumping"); n++ {
		out = filepath.Join(dir, day+"."+strconv.Itoa(n)+".parquet")
	}
	if err := os.Rename(src, out+".dumping"); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	return syncFile(dir)
}

// dump writes a claimed log (<target>.dumping) as its target unless the
// target exists already, then deletes the log. Of an id the recent-ids
// window let through twice, the line appended first is kept (dumpQuery).
func (a *Archive) dump(ctx context.Context, dir, pending string) error {
	src := filepath.Join(dir, pending)
	out := strings.TrimSuffix(src, ".dumping")
	if !exists(out) {
		q, err := dumpQuery(src)
		if err != nil {
			return err
		}
		q = "COPY (" + q + ") TO "
		if err := a.write(ctx, q, out); err != nil {
			return err
		}
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	return syncFile(dir)
}

// dumpQuery is the dump's SELECT of a log (logSelect, strict): every line once
// per id, ordered
// by family and ts, with every column's type spelled out. DuckDB inferring
// the types from one day's values would rewrite a value for good
// ("ABCDEF01-…" read as a UUID comes back lower case, "12:30" as a TIME
// comes back "12:30:00") and leave a month whose days inferred different
// types unable to merge. The fixed keys take their type from fields, every
// other key (attr_<key>, the key as stored) is VARCHAR, and every fixed
// column is there even when no line has it.
//
// A repeated id keeps the line appended first, as SQLite keeps the first
// row it receives: a repeat need not be the same event (the server does
// not check), so a later one can differ in received_at, actor_id,
// country or attributes. The lines are numbered in file order (WITH
// ORDINALITY), the first number per id is found over the ids and numbers
// alone, and only then are the lines it names sorted (a sort spills to
// disk; a hash table of whole lines does not) and their columns extracted
// as they stream into the file. A DISTINCT ON over the lines held them all
// in a hash table and ran out of memory on smaller logs, worst with more
// threads; measure_test.go compares this with the first release's dump.
//
// Each key is extracted by JSON pointer, so its case is kept; DuckDB's
// column names are not case-sensitive, though (read_ndjson's columns={…}
// refuses attr_Plan beside attr_plan as a duplicate). Of keys equal but for
// case, the first in byte order keeps its name and each next one is named
// <key>_<n>, the first n free: attr_Plan, attr_plan_1. Which column such a
// key lands in can differ between days; no value is dropped.
func dumpQuery(path string) (string, error) {
	keys, err := logKeys(path)
	if err != nil {
		return "", err
	}
	return logSelect(path, columnsOf(keys), false), nil
}

// logSelect is a log's lines as typed rows (see dumpQuery): cols, every
// id once, the line appended first kept, ordered by family and ts. A
// strict select fails on a line that is not JSON; a tolerant one skips it,
// for a reader that may meet the torn last line of a log ingest is still
// writing (read_json_objects with ignore_errors reads such a line as a
// NULL object).
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

// column is a key of a log and the column the dump reads it into.
type column struct{ key, name, typ string }

// columnsOf lists the columns of a log whose lines hold keys: fields,
// then every other key. keys is not modified.
func columnsOf(keys map[string]bool) []column {
	taken := map[string]bool{}
	var cols []column
	add := func(key, name, typ string) {
		taken[strings.ToLower(name)] = true
		cols = append(cols, column{key, name, typ})
	}
	fixed := map[string]bool{}
	for _, f := range fields {
		add(f.name, f.name, f.typ)
		fixed[f.name] = true
	}
	var rest, later []string
	for k := range keys {
		if !fixed[k] {
			rest = append(rest, k)
		}
	}
	slices.Sort(rest)
	for _, k := range rest {
		if taken[strings.ToLower(visible(k))] {
			later = append(later, k)
			continue
		}
		add(k, visible(k), "VARCHAR")
	}
	for _, k := range later {
		name := visible(k)
		for n := 1; taken[strings.ToLower(name)]; n++ {
			name = visible(k) + "_" + strconv.Itoa(n)
		}
		add(k, name, "VARCHAR")
	}
	return cols
}

// visible is key as a column name: NUL, which SQL text cannot hold (DuckDB
// reads it as a C string, cut at the first NUL), becomes the text \u0000.
// Every other character, control ones included, is kept as stored.
func visible(key string) string { return strings.ReplaceAll(key, "\x00", `\u0000`) }

// pointer is the column's key as a JSON pointer, in SQL: a string
// literal, with each NUL spelled chr(0).
func (c column) pointer() string {
	parts := strings.Split("/"+jsonPointer.Replace(c.key), "\x00")
	for i, p := range parts {
		parts[i] = quote(p)
	}
	return strings.Join(parts, " || chr(0) || ")
}

var jsonPointer = strings.NewReplacer("~", "~0", "/", "~1")

// ident is name as a quoted SQL identifier.
func ident(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func (c column) ident() string { return ident(c.name) }

// logKeys returns every key of a log's lines. A line that is not a JSON
// object fails it, as DuckDB would.
func logKeys(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	defer f.Close()
	keys := map[string]bool{}
	dec := json.NewDecoder(bufio.NewReader(f))
	for {
		var m map[string]json.RawMessage
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			return keys, nil
		} else if err != nil {
			return nil, fmt.Errorf("archive: %s: %w", filepath.Base(path), err)
		}
		if m == nil {
			return nil, fmt.Errorf("archive: %s: a line is not an object", filepath.Base(path))
		}
		for k := range m {
			keys[k] = true
		}
	}
}

// merge writes every day file and segment of month, with the month file
// if a crashed merge left one, as the month file, then deletes the days.
func (a *Archive) merge(ctx context.Context, dir string, names []string, month string) error {
	var days []string
	for _, n := range names {
		if m := dayFileName.FindStringSubmatch(n); m != nil && strings.HasPrefix(m[1], month+"-") {
			days = append(days, n)
		}
	}
	if len(days) == 0 {
		return nil
	}
	slices.SortFunc(days, func(x, y string) int {
		mx, my := dayFileName.FindStringSubmatch(x), dayFileName.FindStringSubmatch(y)
		if c := strings.Compare(mx[1], my[1]); c != 0 {
			return c
		}
		return segment(mx[2]) - segment(my[2])
	})
	var inputs []string
	if exists(filepath.Join(dir, month+".parquet")) {
		inputs = append(inputs, filepath.Join(dir, month+".parquet"))
	}
	for _, n := range days {
		inputs = append(inputs, filepath.Join(dir, n))
	}
	if err := a.write(ctx, "COPY ("+mergeQuery(inputs)+") TO ", filepath.Join(dir, month+".parquet")); err != nil {
		return err
	}
	for _, n := range days {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return fmt.Errorf("archive: %w", err)
		}
	}
	return nil
}

// segment is a day file's segment number from its ".<n>" (0 for the day
// file itself, dumped first).
func segment(dotN string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(dotN, "."))
	return n
}

// mergeQuery is the merge's SELECT of files, given oldest first (a month
// file a crashed merge left, then each day's file and its segments in
// order): every id once, from the first file that holds it (a file holds
// an id once), ordered by family and ts. As in the dump, the first row
// per id is found over the ids and a row key alone, and the sort, which
// spills, carries the rows.
func mergeQuery(files []string) string { return mergeSelect(files, files) }

// mergeSelect reads files and keys each row by the position in order of
// its file's name, as read_parquet reports it (the path as given, today).
// A name that order does not hold would key the row NULL and the IN would
// drop it, then the merge would delete its day: it fails the query instead.
func mergeSelect(files, order []string) string {
	list := func(paths []string) string {
		q := make([]string, len(paths))
		for i, p := range paths {
			q[i] = quote(p)
		}
		return "[" + strings.Join(q, ", ") + "]"
	}
	rows := "read_parquet(" + list(files) + ", union_by_name=true, filename=true, file_row_number=true)"
	key := "coalesce(list_position(" + list(order) + ", filename), error('archive: merge input not matched: ' || filename))" +
		" * 4294967296 + file_row_number"
	firsts := "SELECT min(k) FROM (SELECT id, " + key + " AS k FROM " + rows + ") GROUP BY id"
	return "SELECT * EXCLUDE (filename, file_row_number, k) FROM (SELECT *, " + key + " AS k FROM " + rows +
		") WHERE k IN (" + firsts + ") ORDER BY family, ts"
}

// write runs copyTo (a COPY … TO, missing its target) into out.tmp, fsyncs
// it, renames it over out and fsyncs the directory: a reader sees the old
// file or the whole new one, and a crash leaves at most a .tmp.
func (a *Archive) write(ctx context.Context, copyTo, out string) error {
	tmp := out + ".tmp"
	os.Remove(tmp)
	if err := a.eng.Exec(ctx, copyTo+quote(tmp)+" (FORMAT parquet, COMPRESSION zstd)"); err != nil {
		return fmt.Errorf("archive: %s: %w", filepath.Base(out), err)
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, out); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	return syncFile(filepath.Dir(out))
}

// RemoveProject deletes a purged project's directory (raw-first D6). Its
// open logs are closed first; an event for it still in the pipeline
// recreates the directory, and the next purge pass removes it.
func (a *Archive) RemoveProject(id int64) error {
	a.log.forget(id)
	if err := os.RemoveAll(filepath.Join(a.log.Dir(), strconv.FormatInt(id, 10))); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	return nil
}

func list(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// months lists the months any file of names belongs to.
func months(names []string) []string {
	set := map[string]bool{}
	for _, n := range names {
		if m := anyMonth.FindStringSubmatch(n); m != nil {
			set[m[1]] = true
		}
	}
	var out []string
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func lastDay(month string) civil.Date {
	first, _ := time.Parse("2006-01", month)
	return civil.DateOf(first.AddDate(0, 1, -1))
}

// open reports whether month still has a log, a closed log or a claimed one
// whose dump is pending: it is not final yet.
func open(names []string, month string) bool {
	for _, n := range names {
		if (logName.MatchString(n) || closedName.MatchString(n) || dumpingName.MatchString(n)) && strings.HasPrefix(n, month+"-") {
			return true
		}
	}
	return false
}

func removeMonth(dir string, names []string, month string) error {
	for _, n := range names {
		if strings.HasPrefix(n, month) {
			if err := os.Remove(filepath.Join(dir, n)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("archive: %w", err)
			}
		}
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	return nil
}

// quote is path as a SQL string literal.
func quote(path string) string { return "'" + strings.ReplaceAll(path, "'", "''") + "'" }
