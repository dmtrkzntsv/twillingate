package app

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
)

// widgetFiles is reporting.Files over the console's event files: each
// query under CONSOLE_QUERY_TIMEOUT, with the registry's rules, scoped to
// the widget's project, and pages of at most CONSOLE_QUERY_ROWS_MAX rows,
// as the query tool reads the same files.
type widgetFiles struct {
	reader  *archive.Reader
	dir     string // EVENTS_DIR, where SampleProject reads the file names
	timeout time.Duration
	maxRows int
	// reg gives the rules and which projects are live. nil (reporting
	// dev, which has no registry) applies no rules and counts every
	// project with files as live.
	reg *manage.Registry
}

var _ reporting.Files = (*widgetFiles)(nil)

func newWidgetFiles(cfg *config.Config, reader *archive.Reader, reg *manage.Registry) *widgetFiles {
	return &widgetFiles{reader: reader, dir: cfg.EventsDir, timeout: cfg.Console.QueryTimeout,
		maxRows: cfg.Console.QueryMaxRows, reg: reg}
}

// Query runs sql under its own deadline: reporting runs a widget's load
// detached from the request that asked for it, so this is the only one.
func (f *widgetFiles) Query(ctx context.Context, sql string, args []engine.Arg, maxRows int, project int64) (engine.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	var rules archive.Rules
	if f.reg != nil {
		rules.Events, rules.Attributes = f.reg.Snapshot(ctx).RuleLists()
	}
	return f.reader.QueryScoped(ctx, archive.Scoped{SQL: sql, Args: args, MaxRows: maxRows, Project: project}, rules)
}

// SampleProject is the live project whose files hold the newest day (a
// tie goes to the lowest id), from the file names alone, so it costs no
// query; 0 when no live project has any.
func (f *widgetFiles) SampleProject(ctx context.Context) (int64, error) {
	days, err := archive.LatestDays(f.dir)
	if err != nil {
		return 0, err
	}
	live := func(int64) bool { return true }
	if f.reg != nil {
		snap := f.reg.Snapshot(ctx)
		live = func(id int64) bool { p := snap.Project(id); return p != nil && !p.Archived }
	}
	var id int64
	var newest string
	for pid, day := range days {
		if live(pid) && (day > newest || day == newest && pid < id) {
			id, newest = pid, day
		}
	}
	return id, nil
}

func (f *widgetFiles) Timeout() time.Duration { return f.timeout }
func (f *widgetFiles) MaxRows() int           { return f.maxRows }

// openFiles builds the engine on this binary (EngineOptions) and a reader
// over EVENTS_DIR. Neither starts anything: the engine spawns its child on
// the first query, so a caller that never queries never spawns one. The
// caller closes the engine.
func openFiles(cfg *config.Config) (*engine.Engine, *archive.Reader, error) {
	opts, err := EngineOptions(cfg)
	if err != nil {
		return nil, nil, err
	}
	eng, err := engine.New(opts)
	if err != nil {
		return nil, nil, err
	}
	reader, err := archive.NewReader(cfg.EventsDir, eng)
	if err != nil {
		eng.Close()
		return nil, nil, err
	}
	return eng, reader, nil
}

// checkUserWidgets logs every user's sql widget whose SQL fails on the
// event files (Decision 10): a widget written in SQLite's dialect before
// the switch fails its shape check here, once at start, rather than only
// on the dashboard of whoever opens it next. Nothing is rewritten or
// archived. It runs in the background: each check is a query, and the
// console does not wait for them.
func checkUserWidgets(ctx context.Context, st reporting.Store, files reporting.Files, logger *slog.Logger) {
	failed, err := reporting.CheckUserWidgets(ctx, st, files)
	for _, f := range failed {
		logger.Warn("widget SQL fails on the event files", "widget_id", f.WidgetID,
			"dashboard_id", f.DashboardID, "title", f.Title, "error", f.Err.Error())
	}
	if err != nil && ctx.Err() == nil {
		logger.Error("checking the users' widgets on the event files", "error", err)
	}
}

// importMarks is the slice of the store warnImport reads.
type importMarks interface {
	GetMeta(ctx context.Context, key string) (string, error)
	OldestRawDays(ctx context.Context) (map[int64]string, error)
}

// warnImport logs, at start, the projects whose raw rows in SQLite reach
// further back than the event files in eventsDir (Decision 13): an oldest
// raw day before the files' oldest day, or raw rows and no file at all.
// The dashboards read only the files, so until `twillingate events import`
// copies those days in they show only what the files hold. A fresh
// install, whose files began with its first event, has nothing to warn
// about; once the import has run (the events_imported row) nothing is
// checked. A month file counts from its first day, so a project whose
// oldest file is a month file warns only for days before that month.
// It is advice: a failure to read either side is logged, not returned.
func warnImport(ctx context.Context, st importMarks, eventsDir string, logger *slog.Logger) {
	behind, err := importBehind(ctx, st, eventsDir)
	if err != nil {
		logger.Error("checking whether SQLite holds events the event files lack", "error", err)
		return
	}
	if len(behind) > 0 {
		logger.Warn("SQLite holds raw events older than the event files: dashboards show only what the files hold; "+
			"run `twillingate events import` with ingest stopped", "projects", behind)
	}
}

// importBehind is warnImport's list of projects, in id order.
func importBehind(ctx context.Context, st importMarks, eventsDir string) ([]int64, error) {
	if done, err := st.GetMeta(ctx, eventsImportedKey); err != nil || done != "" {
		return nil, err
	}
	held, err := st.OldestRawDays(ctx)
	if err != nil {
		return nil, err
	}
	files, err := archive.OldestDays(eventsDir)
	if err != nil {
		return nil, err
	}
	var behind []int64
	for id, day := range held {
		if first, ok := files[id]; !ok || day < first {
			behind = append(behind, id)
		}
	}
	slices.Sort(behind)
	return behind, nil
}
