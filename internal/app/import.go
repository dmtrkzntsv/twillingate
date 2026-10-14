package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"syscall"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// geteuid is the effective uid ImportEvents checks against the database
// file's owner; a test replaces it.
var geteuid = os.Geteuid

// eventsImportedKey is the meta row ImportEvents writes when done: the
// time it finished (RFC 3339, UTC).
const eventsImportedKey = "events_imported"

// importStore is the slice of the store the import reads and marks.
type importStore interface {
	ProjectIDs(ctx context.Context) ([]int64, error)
	ViewDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	ProductDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	MeasureDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	RawEvents(ctx context.Context, projectID int64, day civil.Date) ([]store.Event, error)
	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}

// dayImporter writes the events of one project's day the files do not
// hold (archive.Archive.ImportDay).
type dayImporter interface {
	ImportDay(ctx context.Context, project int64, day string, evs []store.Event) (int, error)
}

// ImportEvents is `twillingate events import` (raw-first D19, PR 5c): it
// copies into the event files every raw row SQLite holds that they do not,
// once. With events_imported set it says when that was and returns, unless
// force. Otherwise it goes in this order. First it refuses when run as
// anyone but the database file's owner: files it wrote as root would lock
// the service user out of raw/. Then it takes the events directory as the
// ingest server does, so it refuses (archive.ErrLocked) while the server
// runs: nothing else may write events or raw/ while it reads. Only then does
// it migrate the schema, as `migrate` does, so it can run before the new
// binary's first start. A run refused for either reason has written nothing
// to raw/ (not even the directory or its lock) or to the database. The
// closed logs it leaves are the next start's pass to dump.
func ImportEvents(ctx context.Context, cfg *config.Config, st store.Store, force bool, out io.Writer) error {
	// A database older than the meta table cannot answer yet; the check
	// after the migration does.
	if done, err := st.GetMeta(ctx, eventsImportedKey); err == nil && imported(done, force, out) {
		return nil
	}
	// Before OpenLog, which creates raw/ and raw/.lock: a run checkOwner
	// refuses must not leave files the service user cannot open.
	if err := checkOwner(DatabasePath(cfg.Database), geteuid()); err != nil {
		return err
	}
	log, err := archive.OpenLog(cfg.EventsDir)
	if errors.Is(err, archive.ErrLocked) {
		return fmt.Errorf("%w: stop the ingest service first", err)
	}
	if err != nil {
		return err
	}
	defer log.Close()
	if err := Migrate(ctx, cfg, st); err != nil {
		return err
	}
	if done, err := st.GetMeta(ctx, eventsImportedKey); err != nil {
		return err
	} else if imported(done, force, out) {
		return nil
	}
	opts, err := EngineOptions(cfg)
	if err != nil {
		return err
	}
	eng, err := engine.New(opts)
	if err != nil {
		return err
	}
	defer eng.Close()
	a := archive.New(log, eng, slog.New(slog.NewTextHandler(out, nil)))
	return importEvents(ctx, st, a, cfg.Retention.Days, time.Now(), out)
}

// imported says, when done holds the time an import finished and force is
// off, when that was and how to run it again, and reports true.
func imported(done string, force bool, out io.Writer) bool {
	if done == "" || force {
		return false
	}
	fmt.Fprintf(out, "events already imported on %s; -force imports again (what the files hold is skipped)\n", done)
	return true
}

// checkOwner refuses a run whose effective uid is not the owner of the
// database file at path: the closed logs, project directories and engine
// directories the import creates would belong to that user, and the
// service user could not write them afterwards.
func checkOwner(path string, euid int) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) == euid {
		return nil
	}
	return fmt.Errorf("%s belongs to uid %d, not %d: run it as the service user, as in sudo -u twillingate twillingate events import", path, st.Uid, euid)
}

// importEvents hands every project's days of raw rows to imp, in order,
// and records the import in meta once every day succeeded. Days
// RETENTION_DAYS would delete are skipped (today - retentionDays and
// before). A failed day is printed and the rest go on; the call then
// fails, and a run again writes nothing the files already hold.
func importEvents(ctx context.Context, st importStore, imp dayImporter, retentionDays int, now time.Time, out io.Writer) error {
	ids, err := st.ProjectIDs(ctx)
	if err != nil {
		return err
	}
	today := civil.Today(now)
	keep := today.AddDays(-retentionDays)
	var failed, rows, written int
	for _, id := range ids {
		// Past today: ingest accepts a ts up to wire.FutureSkew ahead.
		days, err := rawDays(ctx, st, id, today.AddDays(2))
		if err != nil {
			fmt.Fprintf(out, "project %d: %v\n", id, err)
			failed++
			continue
		}
		var pDays, pRows, pWritten int
		for _, day := range days {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !keep.Before(day) {
				continue
			}
			evs, err := st.RawEvents(ctx, id, day)
			var n int
			if err == nil {
				n, err = imp.ImportDay(ctx, id, day.String(), evs)
			}
			if err != nil {
				fmt.Fprintf(out, "project %d, %s: %v\n", id, day, err)
				failed++
				continue
			}
			pDays++
			pRows += len(evs)
			pWritten += n
		}
		if pDays > 0 {
			fmt.Fprintf(out, "project %d: %d days, %d rows, %d written\n", id, pDays, pRows, pWritten)
		}
		rows += pRows
		written += pWritten
	}
	if failed > 0 {
		return fmt.Errorf("events import: %d failed; run it again (what the files hold is skipped)", failed)
	}
	if err := st.SetMeta(ctx, eventsImportedKey, now.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	fmt.Fprintf(out, "imported %d of %d rows; the next start dumps them into day files\n", written, rows)
	return nil
}

// rawDays lists, in order, the days before before on which the project
// has raw rows of any family.
func rawDays(ctx context.Context, st importStore, projectID int64, before civil.Date) ([]civil.Date, error) {
	seen := map[string]bool{}
	var out []civil.Date
	for _, fn := range []func(context.Context, int64, civil.Date) ([]civil.Date, error){
		st.ViewDaysBefore, st.ProductDaysBefore, st.MeasureDaysBefore,
	} {
		days, err := fn(ctx, projectID, before)
		if err != nil {
			return nil, err
		}
		for _, d := range days {
			if !seen[d.String()] {
				seen[d.String()] = true
				out = append(out, d)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}
