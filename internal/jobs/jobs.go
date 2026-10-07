// Package jobs schedules the daily maintenance work (spec §9): salt
// rotation at 00:00 UTC, aggregation+prune at 03:00 UTC, with a catch-up
// pass on boot so downtime never skips days.
package jobs

import (
	"context"
	"log/slog"
	"sort"
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

// Store is the slice of store.Store the daily pass drives: enumerate the
// raw days behind the window, roll each up, prune what has aged out, and
// reclaim pages. Declared here so the ingest and registry halves of the
// store can change without touching this package.
type Store interface {
	ProjectIDs(ctx context.Context) ([]int64, error)
	ViewDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	ProductDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	AggregateViewDay(ctx context.Context, projectID int64, day civil.Date, topN int) error
	AggregateProductDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error
	MeasureDaysBefore(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error)
	AggregateMeasureDay(ctx context.Context, projectID int64, day civil.Date, attrs []string, topN int) error
	UpsertActors(ctx context.Context, projectID int64, day civil.Date) error
	AggregateRetentionDay(ctx context.Context, projectID int64, day civil.Date) error
	PruneActors(ctx context.Context, projectID int64, before civil.Date) error
	AggregateIdentityDay(ctx context.Context, projectID int64, day civil.Date, topN int) error
	PruneIdentities(ctx context.Context, projectID int64, before civil.Date) error
	PruneAggregates(ctx context.Context, projectID int64, before civil.Date) error
	RebuildFlatView(ctx context.Context, keys []string) error
	IncrementalVacuum(ctx context.Context) error
	// RecordUsageHistory stores the usage_history measurements (each
	// project's disk use) as of now.
	RecordUsageHistory(ctx context.Context, now time.Time) error
	// PurgeArchived deletes every project, dashboard, widget and widget
	// share archived more than days ago. days <= 0 purges nothing.
	PurgeArchived(ctx context.Context, days int) (store.PurgeResult, error)
	// ArchiveDueWidgetShares archives every live share whose archive_at is
	// at or before now (a "2006-01-02T15:04:05Z" UTC timestamp) and returns
	// how many.
	ArchiveDueWidgetShares(ctx context.Context, now string) (int, error)
}

type Runner struct {
	store  Store
	cfg    *config.Config
	reg    *manage.Registry
	salt   Rotator
	logger *slog.Logger
	now    func() time.Time

	// Last UTC day on which each job fired, so a job runs at most once per
	// day however often the ticker lands inside its hour.
	lastSaltDay string
	lastAggDay  string

	// topN caps distinct client-supplied values kept per day
	// (ATTRIBUTE_VALUES_TOP_N): per views breakdown, kinds included, and
	// per attribute key; the operator picks the key, clients pick the
	// values.
	topN int
}

func New(st Store, cfg *config.Config, reg *manage.Registry, salt Rotator, logger *slog.Logger, now func() time.Time) *Runner {
	return &Runner{store: st, cfg: cfg, reg: reg, salt: salt, logger: logger, now: now,
		topN: cfg.AttributeValuesTopN}
}

// RunDailyPass rolls up every day that has aged out of the raw window,
// prunes aggregates past their retention, refreshes the flat view,
// reclaims free pages and measures the server's stats (project sizes).
//
// Per-project failures are logged and skipped rather than returned: one
// broken project must not stop maintenance for the rest. Only failures to
// enumerate work at all are treated as fatal to the pass.
func (r *Runner) RunDailyPass(ctx context.Context) error {
	today := civil.Today(r.now())
	// Retention is global: the same windows apply to every project.
	ret := r.cfg.Retention

	// Shares past their date are archived on every pass, whatever
	// RETENTION_ARCHIVED_DAYS is: that setting only gates the purge below.
	if n, err := r.store.ArchiveDueWidgetShares(ctx, r.now().UTC().Format("2006-01-02T15:04:05Z")); err != nil {
		r.logger.Error("archive due widget shares failed", "error", err)
	} else if n > 0 {
		r.logger.Info("archive due widget shares", "shares", n)
	}

	// Purge first: a project purged this pass must not then be rolled up
	// or pruned below, and the registry (which still lists it until this
	// reloads) must not hand it out to a request arriving mid-pass.
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
			"forms", len(purged.Forms))
		if len(purged.Projects) > 0 {
			if err := r.reg.Reload(ctx); err != nil {
				r.logger.Error("registry reload after purge failed", "error", err)
			}
		}
	}

	// store.ProjectIDs (all rows, including archived) is the complete list.
	ids, err := r.store.ProjectIDs(ctx)
	if err != nil {
		return err
	}
	snap := r.reg.Snapshot(ctx)
	for _, id := range ids {
		// Cohorts, actors and identity rollups read raw rows across both
		// raw tables and never delete them, so they run over every day
		// still present -- not just the aged-out ones. Restricting them to
		// aged-out days would leave the users, groups and retention pages a
		// whole raw window stale. Every write is keyed and recomputed, so
		// re-running a day is safe.
		identityDays, err := r.allRawDays(ctx, id, today.AddDays(1))
		if err != nil {
			return err
		}
		// Actors and cohorts run for every project. UpsertActors keeps
		// user- and install-identified actors only, so a project whose
		// clients send no ids gets empty cohorts at no cost.
		for _, day := range identityDays {
			if err := r.store.UpsertActors(ctx, id, day); err != nil {
				r.logger.Error("upsert actors failed", "project", id, "day", day.String(), "error", err)
			}
			if err := r.store.AggregateRetentionDay(ctx, id, day); err != nil {
				r.logger.Error("aggregate retention failed", "project", id, "day", day.String(), "error", err)
			}
			// Today is still arriving. v_identity_daily prefers an
			// aggregated day over its raw rows, so rolling today up would
			// freeze whatever had landed by 03:00; the view's live half
			// serves it instead.
			if day == today {
				continue
			}
			if err := r.store.AggregateIdentityDay(ctx, id, day, r.cfg.IdentitiesTopN); err != nil {
				r.logger.Error("aggregate identity failed", "project", id, "day", day.String(), "error", err)
			}
		}

		days, err := r.store.ViewDaysBefore(ctx, id, today.AddDays(-ret.Events.RawDays))
		if err != nil {
			return err
		}
		for _, day := range days {
			if err := r.store.AggregateViewDay(ctx, id, day, r.topN); err != nil {
				r.logger.Error("aggregate views failed", "project", id, "day", day.String(), "error", err)
			}
		}

		prodDays, err := r.store.ProductDaysBefore(ctx, id, today.AddDays(-ret.Events.RawDays))
		if err != nil {
			return err
		}
		attrs := snap.AttributesFor(id)
		for _, day := range prodDays {
			if err := r.store.AggregateProductDay(ctx, id, day, attrs, r.topN); err != nil {
				r.logger.Error("aggregate product failed", "project", id, "day", day.String(), "error", err)
			}
		}

		// Measures share the product family's raw window but never feed
		// allRawDays above (spec decision 17): a backend's connection hash
		// is a server, not a visitor, so a measure alone must not create an
		// actor, a cohort or an identity rollup.
		measureDays, err := r.store.MeasureDaysBefore(ctx, id, today.AddDays(-ret.Events.RawDays))
		if err != nil {
			return err
		}
		for _, day := range measureDays {
			if err := r.store.AggregateMeasureDay(ctx, id, day, attrs, r.topN); err != nil {
				r.logger.Error("aggregate measures failed", "project", id, "day", day.String(), "error", err)
			}
		}

		if err := r.store.PruneAggregates(ctx, id, today.AddDays(-ret.Events.AggregateDays)); err != nil {
			r.logger.Error("prune failed", "project", id, "error", err)
		}
		if err := r.store.PruneActors(ctx, id, today.AddDays(-ret.Events.AggregateDays)); err != nil {
			r.logger.Error("prune actors failed", "project", id, "error", err)
		}
		if err := r.store.PruneIdentities(ctx, id, today.AddDays(-ret.Events.AggregateDays)); err != nil {
			r.logger.Error("prune identities failed", "project", id, "error", err)
		}
	}

	if err := r.store.RebuildFlatView(ctx, snap.DeclaredAttributeKeys()); err != nil {
		r.logger.Error("flat view rebuild failed", "error", err)
	}
	if err := r.store.IncrementalVacuum(ctx); err != nil {
		r.logger.Error("incremental vacuum failed", "error", err)
	}
	// Last, so the sizes are those left after the pruning and the vacuum.
	// Run calls this pass at boot, in the background, so sizes exist shortly
	// after every start, not only after the first 03:00.
	if err := r.store.RecordUsageHistory(ctx, r.now()); err != nil {
		r.logger.Error("record usage history", "error", err)
	}
	return nil
}

// allRawDays merges the days present in both raw tables, deduplicated
// and sorted, so a project is covered whatever mix of surfaces it uses.
func (r *Runner) allRawDays(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error) {
	seen := map[string]bool{}
	var out []civil.Date
	for _, fn := range []func(context.Context, int64, civil.Date) ([]civil.Date, error){
		r.store.ViewDaysBefore, r.store.ProductDaysBefore,
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
	// Chronological order matters: AggregateRetentionDay reads first_seen_day
	// from actors, so an earlier day must be recorded before a later one
	// references it.
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

// runScheduled fires whichever jobs are due at the current time. Split out
// from Run so the day-boundary guards are testable without a real clock.
func (r *Runner) runScheduled(ctx context.Context) {
	now := r.now().UTC()
	day := civil.Today(now).String()
	if now.Hour() == 0 && r.lastSaltDay != day {
		r.lastSaltDay = day
		if err := r.salt.Rotate(ctx); err != nil {
			r.logger.Error("salt rotation", "error", err)
		}
	}
	if now.Hour() == 3 && r.lastAggDay != day {
		r.lastAggDay = day
		if err := r.RunDailyPass(ctx); err != nil {
			r.logger.Error("daily pass", "error", err)
		}
	}
}

// Run blocks until ctx is cancelled, doing a catch-up pass on entry.
func (r *Runner) Run(ctx context.Context) {
	if _, err := r.salt.Current(ctx); err != nil {
		r.logger.Error("initial salt", "error", err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		r.logger.Error("boot catch-up pass", "error", err)
	}
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
