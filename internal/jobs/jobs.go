// Package jobs schedules the daily maintenance work (spec §9): salt
// rotation at 00:00 UTC, the daily pass at 03:00 UTC, with a catch-up pass
// on boot so downtime never skips days.
package jobs

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Rotator is the slice of identity.Salter the scheduler needs.
type Rotator interface {
	Rotate(ctx context.Context) error
	Current(ctx context.Context) (string, error)
}

// Store is the slice of store.Store the daily pass and form attribution
// drive: archive the shares and drafts past their date, purge what was
// archived long enough ago, reclaim pages and measure the database.
// Declared here so the ingest and registry halves of the store can change
// without touching this package.
type Store interface {
	IncrementalVacuum(ctx context.Context) error
	// RecordUsageHistory stores the usage_history measurements as of now:
	// the database's size, and each project's file bytes when fileBytes
	// is non-nil.
	RecordUsageHistory(ctx context.Context, now time.Time, fileBytes map[int64]int64) error
	// PurgeArchived deletes every project, dashboard, widget and widget
	// share archived more than days ago. days <= 0 purges nothing.
	PurgeArchived(ctx context.Context, days int) (store.PurgeResult, error)
	// ArchiveDueWidgetShares archives every live share whose archive_at is
	// at or before now and returns how many.
	ArchiveDueWidgetShares(ctx context.Context, now time.Time) (int, error)
	// ExpireDrafts archives every active draft form whose draft_until is
	// at or before now and returns how many.
	ExpireDrafts(ctx context.Context, now time.Time) (int, error)
	// PendingSubmissions lists up to limit submissions received at or
	// before `before` and not yet attributed, oldest first. A row whose
	// stored event does not read is listed with Err set and a zero Event,
	// and counts toward limit; only a failed read is an error.
	PendingSubmissions(ctx context.Context, before time.Time, limit int) ([]store.PendingSubmission, error)
	// AttributeSubmission stores ev's referrer and UTMs as p's attribution
	// and, when p.WriteEvent, leaves ev in the outbox (UnloggedEvents),
	// all in one write. A submission no longer pending (deleted, or
	// attributed already) changes nothing.
	AttributeSubmission(ctx context.Context, p store.PendingSubmission, ev store.Event) error
	// UnloggedEvents lists up to limit events AttributeSubmission wrote
	// and MarkLogged has not cleared yet, oldest first. A row that does
	// not read is listed with Err set and a zero Event.
	UnloggedEvents(ctx context.Context, limit int) ([]store.UnloggedEvent, error)
	// UnloggedCount is how many events wait in the outbox: the backlog
	// a failed log write reports.
	UnloggedCount(ctx context.Context) (int, error)
	// MarkLogged takes these (ProjectID, ID) events out of the outbox, in
	// one transaction, once they are in the event files.
	MarkLogged(ctx context.Context, keys []store.UnloggedEvent) error
}

// Archive is the file side of the pass (internal/archive): it runs only in
// a process that ingests, which owns the log.
type Archive interface {
	// Projects lists the projects with a directory under data/.
	Projects() ([]int64, error)
	Maintain(ctx context.Context, today civil.Date, lateDays, retentionDays int) error
	// Sizes is each project's file bytes, by project id.
	Sizes() (map[int64]int64, error)
	RemoveProject(id int64) error
}

// archiveJitter spreads the file work of every process on a host over half
// an hour rather than all at 03:00 (raw-first D8).
const archiveJitter = 30 * time.Minute

type Runner struct {
	store  Store
	cfg    *config.Config
	reg    *manage.Registry
	salt   Rotator
	logger *slog.Logger
	now    func() time.Time

	// archive is nil in a process that does not ingest.
	archive Archive
	// attributor and eventLog attribute form submissions; nil in a
	// process that does not ingest (attribution.go).
	attributor Attributor
	eventLog   EventLog
	// attributionTimeout bounds one lookup and attributionTickBudget one
	// minute's batches; fields so a test can shorten them.
	attributionTimeout    time.Duration
	attributionTickBudget time.Duration
	// jitter and sleep delay a scheduled pass; fields so a test need not wait.
	jitter func() time.Duration
	sleep  func(context.Context, time.Duration)

	// Last UTC day on which each job fired, so a job runs at most once per
	// day however often the ticker lands inside its hour.
	lastSaltDay string
	lastPassDay string
}

func New(st Store, cfg *config.Config, reg *manage.Registry, salt Rotator, logger *slog.Logger, now func() time.Time) *Runner {
	return &Runner{store: st, cfg: cfg, reg: reg, salt: salt, logger: logger, now: now,
		attributionTimeout:    attributionTimeout,
		attributionTickBudget: attributionTickBudget,
		jitter:                func() time.Duration { return rand.N(archiveJitter) },
		sleep: func(ctx context.Context, d time.Duration) {
			select {
			case <-time.After(d):
			case <-ctx.Done():
			}
		}}
}

// WithArchive makes the pass maintain the archive's files.
func (r *Runner) WithArchive(a Archive) *Runner { r.archive = a; return r }

// RunDailyPass archives the shares and draft forms past their date,
// purges what was archived RETENTION_ARCHIVED_DAYS ago, removes the files
// of projects gone, reclaims free pages, maintains the archive and
// measures the database's size and each project's file bytes. Events live
// in the event files only (raw-first PR 6): the pass rolls nothing up.
//
// Every failure is logged and the rest of the pass goes on: one broken
// step must not stop maintenance for the rest.
func (r *Runner) RunDailyPass(ctx context.Context) {
	today := civil.Today(r.now())
	ret := r.cfg.Retention

	// Shares past their date are archived on every pass, whatever
	// RETENTION_ARCHIVED_DAYS is: that setting only gates the purge below.
	if n, err := r.store.ArchiveDueWidgetShares(ctx, r.now()); err != nil {
		r.logger.Error("archive due widget shares failed", "error", err)
	} else if n > 0 {
		r.logger.Info("archive due widget shares", "shares", n)
	}

	// Drafts past their date are archived on every pass, whatever
	// RETENTION_ARCHIVED_DAYS is, and before the purge below, which then
	// ages them like any archived form.
	if n, err := r.store.ExpireDrafts(ctx, r.now()); err != nil {
		r.logger.Error("expire draft forms failed", "error", err)
	} else if n > 0 {
		r.logger.Info("expire draft forms", "forms", n)
	}

	// Purge first: the registry (which still lists a purged project until
	// this reloads) must not hand it out to a request arriving mid-pass.
	//
	// PurgeArchived returns its partial result alongside an error when one
	// item among several failed (it keeps going rather than abort the rest
	// of the pass), so the reload below checks purged.Projects regardless
	// of err: a project already deleted must not be left in the registry
	// just because a sibling item's purge failed.
	if ret.ArchivedDays > 0 {
		purged, err := r.store.PurgeArchived(ctx, ret.ArchivedDays)
		if err != nil {
			r.logger.Error("purge archived failed", "error", err)
		}
		r.logger.Info("purge archived",
			"projects", len(purged.Projects), "dashboards", len(purged.Dashboards),
			"widgets", len(purged.Widgets), "widget_shares", len(purged.WidgetShares),
			"forms", len(purged.Forms), "submissions", purged.Submissions)
		if len(purged.Projects) > 0 {
			if err := r.reg.Reload(ctx); err != nil {
				r.logger.Error("registry reload after purge failed", "error", err)
			}
			if r.archive != nil {
				for _, id := range purged.Projects {
					if err := r.archive.RemoveProject(id); err != nil {
						r.logger.Error("archive: remove purged project", "project", id, "error", err)
					}
				}
			}
		}
	}
	r.removeOrphanedArchives(ctx)

	if err := r.store.IncrementalVacuum(ctx); err != nil {
		r.logger.Error("incremental vacuum failed", "error", err)
	}
	// The sizes last, so they are those left after the vacuum and the
	// archive's dumps, merges and expiry. Run calls this pass at boot, in
	// the background, so sizes exist shortly after every start, not only
	// after the first 03:00. Without an archive (a process that keeps no
	// event files) only the database is measured; a failed measurement of
	// the files leaves them out the same way.
	var fileBytes map[int64]int64
	if r.archive != nil {
		if err := r.archive.Maintain(ctx, today, r.cfg.IngestLateDays, r.cfg.Retention.Days); err != nil {
			r.logger.Error("archive maintenance", "error", err)
		}
		sizes, err := r.archive.Sizes()
		if err != nil {
			r.logger.Error("archive sizes", "error", err)
		} else {
			fileBytes = sizes
		}
	}
	if err := r.store.RecordUsageHistory(ctx, r.now(), fileBytes); err != nil {
		r.logger.Error("record usage history", "error", err)
	}
}

// removeOrphanedArchives removes the data/ directory of every project the
// store no longer has. The purge above runs in every process's pass but the
// files belong to the ingest process, so a purge made by another process
// (the console's) is found here by what is left on disk. Archived projects
// keep their files until they are purged.
//
// The directories are listed before the registry is reloaded: a directory
// seen here was created for a project the ingest registry knew, so the
// reload that follows knows it too. With no fresh registry nothing is
// removed.
func (r *Runner) removeOrphanedArchives(ctx context.Context) {
	if r.archive == nil {
		return
	}
	dirs, err := r.archive.Projects()
	if err != nil {
		r.logger.Error("archive: list projects", "error", err)
		return
	}
	if len(dirs) == 0 {
		return
	}
	if err := r.reg.Reload(ctx); err != nil {
		r.logger.Error("registry reload before the archive sweep failed", "error", err)
		return
	}
	snap := r.reg.Snapshot(ctx)
	for _, id := range dirs {
		if snap.Project(id) != nil {
			continue
		}
		if err := r.archive.RemoveProject(id); err != nil {
			r.logger.Error("archive: remove purged project", "project", id, "error", err)
		}
	}
}

// runScheduled fires whichever jobs are due at the current time. Split out
// from Run so the day-boundary guards are testable without a real clock.
func (r *Runner) runScheduled(ctx context.Context) {
	// Form attribution runs on every tick (each minute).
	r.attributeSubmissions(ctx)
	now := r.now().UTC()
	day := civil.Today(now).String()
	if now.Hour() == 0 && r.lastSaltDay != day {
		r.lastSaltDay = day
		if err := r.salt.Rotate(ctx); err != nil {
			r.logger.Error("salt rotation", "error", err)
		}
	}
	if now.Hour() == 3 && r.lastPassDay != day {
		// Every process on a host runs its pass at 03:00; the file work
		// moves to a random point in the following half hour. The pass is
		// one unit, so the SQLite work waits too, and so does the loop
		// (the salt rotation is at 00:xx, so nothing is missed).
		if r.archive != nil {
			r.sleep(ctx, r.jitter())
			// A shutdown cut the wait short: nothing runs on a dead
			// context, and the day stays open for the next boot's pass.
			if ctx.Err() != nil {
				return
			}
		}
		r.lastPassDay = day
		r.RunDailyPass(ctx)
	}
}

// Run blocks until ctx is cancelled, doing a catch-up pass on entry.
func (r *Runner) Run(ctx context.Context) {
	if _, err := r.salt.Current(ctx); err != nil {
		r.logger.Error("initial salt", "error", err)
	}
	r.RunDailyPass(ctx)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.runScheduled(ctx)
		}
	}
}
