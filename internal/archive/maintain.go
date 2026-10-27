package archive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
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

// Archive maintains the files the Log fills and derives the sessions,
// identities and aliases from them. It runs in the calling process, in Go:
// the daily pass needs no SQL engine.
type Archive struct {
	log    *Log
	logger *slog.Logger
}

func New(log *Log, logger *slog.Logger) *Archive {
	return &Archive{log: log, logger: logger}
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
// older than retentionDays, and derive the sessions, identities and aliases
// (derive.go). A project's failure is logged and the next
// one goes on (the daily pass's rule); only failing to list the projects
// is returned.
func (a *Archive) Maintain(ctx context.Context, today civil.Date, lateDays, retentionDays int) error {
	began := time.Now()
	projects, err := a.Projects()
	if err != nil {
		return err
	}
	var st passStats
	for _, project := range projects {
		if err := a.maintainProject(ctx, project, today, lateDays, retentionDays, &st); err != nil {
			st.failed++
			a.logger.Error("archive: maintenance", "project", project, "error", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	a.logger.Info("archive: pass", "projects", len(projects), "dumped", st.dumped, "merged", st.merged,
		"expired_months", st.expired, "derived", st.derived, "failed_projects", st.failed,
		"took", time.Since(began).Round(time.Millisecond))
	return nil
}

// passStats is what one Maintain did, for its log line: logs dumped, months
// merged, months deleted by retention, derived files written, and projects
// with a failed step.
type passStats struct{ dumped, merged, expired, derived, failed int }

// Projects lists, in ascending order, the ids of the projects with a
// directory under data/.
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

func (a *Archive) maintainProject(ctx context.Context, project int64, today civil.Date, lateDays, retentionDays int, st *passStats) error {
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
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
			if err := a.dump(ctx, dir, n); err != nil {
				errs = append(errs, err)
			} else {
				st.dumped++
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
	removed := false
	for _, month := range months(names) {
		last := lastDay(month)
		if !keep.Before(last) {
			if err := removeMonth(dir, names, month); err != nil {
				errs = append(errs, err)
			} else {
				st.expired++
			}
			removed = true
			continue
		}
		if last.AddDays(lateDays).Before(today) && !open(names, month) {
			if merged, err := a.merge(ctx, dir, names, month); err != nil {
				errs = append(errs, err)
			} else if merged {
				st.merged++
			}
		}
	}
	// 5. Derive the sessions, identities and aliases (raw-first D16–D18)
	// from the files as the steps above left them.
	if err := a.derive(ctx, dir, removed, st); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// derive writes every derived file projectOf finds missing or stale and
// removes the ones no events file backs. force rewrites identities and
// aliases even when current: the pass deleted files (retention), which no
// modification time shows. A failed write is collected and the rest go on.
func (a *Archive) derive(ctx context.Context, dir string, force bool, st *passStats) error {
	names, err := list(dir)
	if err != nil {
		return err
	}
	pf, err := projectOf(dir, names)
	if err != nil {
		return err
	}
	var errs []error
	sdir := filepath.Join(dir, sessionsDir)
	if len(pf.deriveDays)+len(pf.deriveMonths) > 0 {
		if err := os.MkdirAll(sdir, 0o750); err != nil {
			return fmt.Errorf("archive: %w", err)
		}
		if err := syncFile(dir); err != nil {
			return err
		}
	}
	for _, day := range slices.Sorted(maps.Keys(pf.deriveDays)) {
		if err := deriveSessions(ctx, pf.deriveDays[day], filepath.Join(sdir, day+".parquet")); err != nil {
			errs = append(errs, fmt.Errorf("derive %s/%s.parquet: %w", sessionsDir, day, err))
		} else {
			st.derived++
		}
	}
	for _, month := range slices.Sorted(maps.Keys(pf.deriveMonths)) {
		if err := deriveSessions(ctx, []string{pf.deriveMonths[month]}, filepath.Join(sdir, month+".parquet")); err != nil {
			errs = append(errs, fmt.Errorf("derive %s/%s.parquet: %w", sessionsDir, month, err))
		} else {
			st.derived++
		}
	}
	for _, p := range pf.orphans {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("archive: %w", err))
		}
	}
	if len(pf.Parquet) > 0 && (force || pf.identitiesDue) {
		// One read of every events file folds both: an error reading them
		// fails both files.
		fold, foldErr := foldIdentities(ctx, pf.Parquet)
		for _, d := range []struct {
			file  string
			write func(*identityFold, string) error
		}{{identitiesFile, (*identityFold).writeIdentities}, {aliasesFile, (*identityFold).writeAliases}} {
			out := filepath.Join(dir, d.file)
			err := foldErr
			if err == nil {
				err = d.write(fold, out)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("derive %s: %w", d.file, err))
				// The old file is newer than every events file left after
				// retention, so it would keep reading as current and keep
				// expired users' names; missing, it is derived on read and
				// rewritten by the next pass.
				if err := os.Remove(out); err != nil && !errors.Is(err, os.ErrNotExist) {
					errs = append(errs, fmt.Errorf("archive: %w", err))
				}
				continue
			}
			st.derived++
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
	// would fail the dump (dumpLog) on every pass.
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
// target exists already, then deletes the log. It runs in Go, without the
// engine (dumpLog); of an id the recent-ids window let through twice, the
// line appended first is kept.
func (a *Archive) dump(ctx context.Context, dir, pending string) error {
	src := filepath.Join(dir, pending)
	out := strings.TrimSuffix(src, ".dumping")
	if !exists(out) {
		if err := dumpLog(ctx, src, out); err != nil {
			return err
		}
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	return syncFile(dir)
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

// LogColumn is a key of a log's lines and the column a reader reads it
// into.
type LogColumn struct{ Key, Name string }

// LogColumns is columnsOf for a reader of the logs (internal/native), so
// its columns are named as the dump names them: fields, then every other
// key, a key equal to an earlier one but for case suffixed _<n>. A reader
// unions them with other files' by name, ignoring case, so a suffixed
// name holds only within the log.
func LogColumns(keys map[string]bool) []LogColumn {
	cols := columnsOf(keys)
	out := make([]LogColumn, len(cols))
	for i, c := range cols {
		out[i] = LogColumn{c.key, c.name}
	}
	return out
}

// visible is key as a column name: NUL, which SQL text cannot hold (an
// engine reads it as a C string, cut at the first NUL), becomes the text
// \u0000. Every other character, control ones included, is kept as
// stored.
func visible(key string) string { return strings.ReplaceAll(key, "\x00", `\u0000`) }

// merge writes every day file and segment of month, with the month file
// if a crashed merge left one, as the month file, then deletes the days.
// It runs in Go, without the engine (mergeFiles). It reports whether the
// month had days to merge.
func (a *Archive) merge(ctx context.Context, dir string, names []string, month string) (bool, error) {
	var days []string
	for _, n := range names {
		if m := dayFileName.FindStringSubmatch(n); m != nil && strings.HasPrefix(m[1], month+"-") {
			days = append(days, n)
		}
	}
	if len(days) == 0 {
		return false, nil
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
	if err := mergeFiles(ctx, inputs, filepath.Join(dir, month+".parquet")); err != nil {
		return false, err
	}
	for _, n := range days {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return false, fmt.Errorf("archive: %w", err)
		}
	}
	return true, nil
}

// segment is a day file's segment number from its ".<n>" (0 for the day
// file itself, dumped first).
func segment(dotN string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(dotN, "."))
	return n
}

// Sizes is Sizes over the log's directory: each project's file bytes.
func (a *Archive) Sizes() (map[int64]int64, error) { return Sizes(a.log.Dir()) }

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
