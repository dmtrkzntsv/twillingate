package archive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
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
	entries, err := os.ReadDir(a.log.Dir())
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	for _, e := range entries {
		project, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil || !e.IsDir() {
			continue
		}
		if err := a.maintainProject(ctx, project, today, lateDays, retentionDays); err != nil {
			a.logger.Error("archive: maintenance", "project", project, "error", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
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
// target exists already, then deletes the log. DISTINCT ON (id) drops what
// the recent-ids window let through; DuckDB infers the columns from every
// line (raw-first D2).
func (a *Archive) dump(ctx context.Context, dir, pending string) error {
	src := filepath.Join(dir, pending)
	out := strings.TrimSuffix(src, ".dumping")
	if !exists(out) {
		q := "COPY (SELECT DISTINCT ON (id) * FROM read_ndjson(" + quote(src) + ", sample_size=-1) ORDER BY family, ts) TO "
		if err := a.write(ctx, q, out); err != nil {
			return err
		}
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	return syncFile(dir)
}

// merge writes every day file and segment of month, with the month file
// if a crashed merge left one, as the month file, then deletes the days.
func (a *Archive) merge(ctx context.Context, dir string, names []string, month string) error {
	var inputs, days []string
	for _, n := range names {
		if m := dayFileName.FindStringSubmatch(n); m != nil && strings.HasPrefix(m[1], month+"-") {
			inputs = append(inputs, quote(filepath.Join(dir, n)))
			days = append(days, n)
		}
	}
	if len(days) == 0 {
		return nil
	}
	if exists(filepath.Join(dir, month+".parquet")) {
		inputs = append(inputs, quote(filepath.Join(dir, month+".parquet")))
	}
	q := "COPY (SELECT DISTINCT ON (id) * FROM read_parquet([" + strings.Join(inputs, ", ") + "], union_by_name=true) ORDER BY family, ts) TO "
	if err := a.write(ctx, q, filepath.Join(dir, month+".parquet")); err != nil {
		return err
	}
	for _, n := range days {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return fmt.Errorf("archive: %w", err)
		}
	}
	return nil
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
