package app

import (
	"context"
	"log/slog"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
)

// hasEventFiles reports whether any project under dir (EVENTS_DIR) has an
// events file or log, from the file names alone (archive.LatestDays): a
// boot check that would only run on nothing asks this first rather than
// send the engine a query to find out.
func hasEventFiles(dir string) (bool, error) {
	days, err := archive.LatestDays(dir)
	return len(days) > 0, err
}

// checkUserWidgets logs every user's sql widget whose SQL fails on the
// event files (Decision 10): a widget written in SQLite's dialect before
// the switch fails its shape check here, once at start, rather than only
// on the dashboard of whoever opens it next. Nothing is rewritten or
// archived. It runs in the background: each check is a query on the
// external SQL engine sql, and the console does not wait for them. A
// check that ran out of time or memory, or that the engine was busy or
// down for, is no verdict on the widget: it is logged under its own
// message and the next start checks it again. A timeout skips only that
// widget; anything else leaves the rest unchecked too. Without an engine
// (sql nil) there is nothing to check them on, and with no event file
// under opt.EventsDir every widget would run on nothing: either way it
// checks none.
func checkUserWidgets(ctx context.Context, st reporting.Store, sql reporting.SQL, opt reporting.Options, logger *slog.Logger) {
	if sql == nil {
		return
	}
	if ok, err := hasEventFiles(opt.EventsDir); err != nil || !ok {
		if err != nil {
			logger.Error("checking the users' widgets on the event files", "error", err)
		}
		return
	}
	failed, err := reporting.CheckUserWidgets(ctx, st, sql, opt)
	for _, f := range failed {
		if f.Stopped {
			logger.Warn("could not check widget on the event files; the widgets after it go unchecked until the next start",
				"widget_id", f.WidgetID, "dashboard_id", f.DashboardID, "title", f.Title, "reason", f.Err.Error())
			continue
		}
		if f.Unchecked {
			logger.Warn("could not check widget on the event files", "widget_id", f.WidgetID,
				"dashboard_id", f.DashboardID, "title", f.Title, "reason", f.Err.Error())
			continue
		}
		logger.Warn("widget SQL fails on the event files", "widget_id", f.WidgetID,
			"dashboard_id", f.DashboardID, "title", f.Title, "error", f.Err.Error())
	}
	if err != nil && ctx.Err() == nil {
		logger.Error("checking the users' widgets on the event files", "error", err)
	}
}
