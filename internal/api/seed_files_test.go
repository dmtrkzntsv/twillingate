package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/reporting"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/sqlengine"
)

// hostFiles is a test host's event files: a reader over them, for the
// presence checks, and a test SQL engine serving them
// (archivetest.ServeSQL), where the query tool's sql and the sql widgets
// run, as app's client reaches the console's raw/.
type hostFiles struct {
	reader *archive.Reader
	dir    string
	reg    *manage.Registry
	sqle   *archivetest.SQLEngine
	client *sqlengine.Client
}

// query runs q over the files with the registry's rules, for the presence
// checks: the engine's own question, no person's SQL.
func (f *hostFiles) query(ctx context.Context, q archive.Scoped) (engine.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	events, attrs := f.reg.Snapshot(ctx).RuleLists()
	return f.reader.QueryScoped(ctx, q, archive.Rules{Events: events, Attributes: attrs})
}

// options is opt with what a host's widgets read with: the registry's
// rules and live projects, the files and the guards.
func (f *hostFiles) options(opt reporting.Options) reporting.Options {
	opt.Rules, opt.Live = registryRules(f.reg), registryLive(f.reg)
	opt.EventsDir, opt.QueryTimeout, opt.QueryMaxRows = f.dir, 30*time.Second, 1000
	return opt
}

// hostPresence is manage.Presence over a test host's event files, as
// app's filePresence is over the console's: the target in the project's
// whole history, with the registry's rules as they stand when asked.
type hostPresence struct{ files *hostFiles }

func (p hostPresence) HasEvents(ctx context.Context, projectID int64, family store.Family, name string) (bool, error) {
	res, err := p.files.query(ctx, archive.Scoped{SQL: `SELECT 1 FROM events WHERE project_id = CAST($p AS BIGINT) AND family = $f AND event_name = $n LIMIT 1`,
		Args: []engine.Arg{{Name: "p", Value: strconv.FormatInt(projectID, 10)}, {Name: "f", Value: string(family)}, {Name: "n", Value: name}}})
	return len(res.Rows) > 0, err
}

func (p hostPresence) HasAttribute(ctx context.Context, projectID int64, key string) (bool, error) {
	res, err := p.files.query(ctx, archive.Scoped{SQL: "SELECT column_name FROM (DESCRIBE SELECT * FROM events)"})
	if err != nil {
		return false, err
	}
	var cols []string
	for _, row := range res.Rows {
		if c, ok := row[0].(string); ok {
			cols = append(cols, c)
		}
	}
	col, ok := enginesql.AttrColumn(cols, key)
	if !ok {
		return false, nil
	}
	res, err = p.files.query(ctx, archive.Scoped{SQL: "SELECT 1 FROM events WHERE project_id = CAST($p AS BIGINT) AND " + col + " IS NOT NULL LIMIT 1",
		Args: []engine.Arg{{Name: "p", Value: strconv.FormatInt(projectID, 10)}}})
	return len(res.Rows) > 0, err
}

// hostEngine is an engine on this test binary (TestMain serves the child,
// archivetest.Child) over raw, its spill and slots under dir. It starts no
// child until a query runs.
func hostEngine(dir, raw string) (*engine.Engine, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return engine.New(engine.Options{Path: self, Env: []string{archivetest.ChildEnv + "=1"},
		AllowedDirs: []string{raw}, SpillRoot: filepath.Join(dir, "spill"), SlotDir: filepath.Join(dir, "slots"),
		MaxChildren: 1, MemoryLimit: "256MB", Threads: 1})
}

// newHostFiles copies the template's event files (hostTemplateRaw) into a
// directory of t's and returns them for reg, with a reader and a test SQL
// engine over the copy, closed with t: each host may add events of its
// own.
func newHostFiles(t *testing.T, reg *manage.Registry) *hostFiles {
	t.Helper()
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw")
	archivetest.CopyDir(t, hostTemplateRaw, raw) // keeps the times the derived files' freshness reads
	return serveHostFiles(t, dir, raw, reg)
}

// serveHostFiles is the files in raw for reg: a reader on an engine of
// its own and a test SQL engine on another, their spill and slots under
// dir, all closed with t.
func serveHostFiles(t testing.TB, dir, raw string, reg *manage.Registry) *hostFiles {
	t.Helper()
	eng, err := hostEngine(filepath.Join(dir, "reader"), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	reader, err := archive.NewReader(raw, eng)
	if err != nil {
		t.Fatal(err)
	}
	served, err := hostEngine(filepath.Join(dir, "sql"), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { served.Close() })
	sqle := archivetest.ServeSQL(t, raw, served)
	return &hostFiles{reader: reader, dir: raw, reg: reg, sqle: sqle, client: sqle.Client(t)}
}

// hostToday is the template's today, fixed so that the pass never ages the
// goldens out (400-day retention would drop hostEvents in late 2027): the
// same way reporting pins systemToday.
var hostToday = civil.Date{Year: 2026, Month: 10, Day: 23}

// writeHostFiles writes hostEvents into raw and runs the daily pass as of
// hostToday, so every day is in its Parquet file and the sessions and
// identities are derived, as on a console whose import has run.
func writeHostFiles(ctx context.Context, raw string) error {
	log, err := archive.OpenLog(raw)
	if err != nil {
		return err
	}
	defer log.Close()
	if err := log.WriteEvents(ctx, hostEvents()); err != nil {
		return err
	}
	return archive.New(log, slog.New(slog.DiscardHandler)).Maintain(ctx, hostToday, 1, 400)
}

// hostSession is one session of hostEvents: views page views over
// seconds, the first at start.
type hostSession struct {
	views   int
	seconds int
}

// hostEvents are the template's events in the files: what its SQLite
// seeds describe, for blog (project 1), as far as the tests read them.
//   - Views per day and kind, as agg_views_daily's rows: on 2026-08-20,
//     web 10 visitors, 25 views, 12 sessions, 3 bounces, 600 s; app 6, 20,
//     8, 0, 480 s; on 2026-08-21, web 12, 30, 14, 4, 720 s. Every
//     (day, kind) has actors of its own, so visitors summed over days and
//     counted over the range agree (28 on 2026-08-20..21). The web views
//     are blog.example.com's, with a spread of paths, referrers and UTMs;
//     the app views are ios 2.4.1's.
//   - The live hit h1 on 2026-08-26 (u1, /live, consent given).
//   - A cohort of 10 users on 2026-08-01, 4 of them back on 2026-08-08.
//   - One view of shop.example.com on 2026-08-22, the project's second
//     host.
//   - User u9 views /docs on 2026-08-23 and again on 2026-08-24, and
//     actor m02 views /home on 2026-08-25 as web and as app: figures
//     summed per day differ there from a count over the range (u9 is 2
//     visitors of /docs over 08-23..24, m02 2 visitors on 08-25).
//   - signup: 5 events by 4 users on 2026-08-20 (plan pro), 2 on
//     2026-08-21 (plan team): one by u1 in group g1, Jane Doe of Acme
//     Inc, one by u2 in group g2 (on /pricing, from the newsletter), as
//     agg_product_attrs' team row (2 events, 2 users, 2 groups).
//   - Measures: checkout_api time 200 ms twice (the chrome one with tier
//     gold) and $cls 0 three times on 2026-08-20, then 300 ms (on
//     /checkout) and 0 on 2026-08-21, as the live rows.
func hostEvents() []store.Event {
	var evs []store.Event
	n := 0
	add := func(e store.Event) {
		n++
		e.ID = fmt.Sprintf("0190eeee-0000-7000-8000-%012d", n)
		e.ProjectID = 1
		if e.ActorKind == "" {
			e.ActorKind = store.ActorInstall
		}
		if e.SampleRate == 0 {
			e.SampleRate = 1
		}
		e.ReceivedAt = e.TS
		evs = append(evs, e)
	}
	at := func(day string, sec int) time.Time {
		d, err := time.Parse("2006-01-02", day)
		if err != nil {
			panic(err)
		}
		return d.Add(8*time.Hour + time.Duration(sec)*time.Second)
	}
	paths := []string{"/post-2", "/post-1", "/settings"} // /post-1 the busiest, as agg_views_paths has it
	// views writes each actor's sessions on day, two hours apart, the
	// views of one evenly over its seconds.
	views := func(day, kind, prefix string, actors [][]hostSession) {
		for i, sessions := range actors {
			actor := fmt.Sprintf("%s%02d", prefix, i+1)
			for s, ses := range sessions {
				for v := 0; v < ses.views; v++ {
					sec := s * 7200
					if ses.views > 1 {
						sec += v * ses.seconds / (ses.views - 1)
					}
					e := store.Event{Family: store.FamilyViews, EventName: "$page_view", TS: at(day, sec), Kind: kind,
						ActorID: actor, Country: "US", Consent: store.ConsentGiven, Platform: "web",
						Path: paths[(i+v)%len(paths)], BrowserLocale: "de-DE"}
					if kind == "web" {
						e.Host, e.OS, e.Browser, e.BrowserVersion = "blog.example.com", "windows", "chrome", "126"
						e.Device, e.DisplayWidth, e.DisplayHeight = "desktop", 1920, 1080
						switch i % 3 {
						case 0:
							e.UTMSource, e.UTMMedium, e.UTMCampaign = "newsletter", "email", "august"
						case 1:
							e.ReferrerSource = "google"
						}
					} else {
						e.Platform, e.OS, e.OSVersion, e.AppVersion, e.AppLocale = "ios", "ios", "17.4", "2.4.1", "en"
						e.Device, e.DeviceModel = "unknown", "iPhone15,3"
					}
					add(e)
				}
			}
		}
	}
	bounce, two, three, five, six := hostSession{1, 0}, hostSession{2, 60}, hostSession{3, 60}, hostSession{5, 120}, hostSession{6, 120}
	views("2026-08-20", "web", "w20-", [][]hostSession{
		{bounce, two}, {bounce, two}, {bounce}, {two}, {two}, {two}, {two}, {two}, {two}, {six},
	})
	views("2026-08-21", "web", "w21-", [][]hostSession{
		{bounce, two}, {bounce, two}, {bounce}, {bounce}, {two}, {two}, {two}, {two}, {two}, {two}, {five}, {five},
	})
	views("2026-08-20", "app", "a20-", [][]hostSession{
		{two, three}, {two, three}, {two}, {two}, {three}, {three},
	})
	add(store.Event{Family: store.FamilyViews, EventName: "$page_view", TS: at("2026-08-22", 0), Kind: "web",
		ActorID: "s01", Host: "shop.example.com", Path: "/cart", Platform: "web"})
	for _, day := range []string{"2026-08-23", "2026-08-24"} {
		add(store.Event{Family: store.FamilyViews, EventName: "$page_view", TS: at(day, 0), Kind: "web",
			ActorID: "u9", ActorKind: store.ActorUser, UserID: "u9", Host: "blog.example.com", Path: "/docs", Platform: "web"})
	}
	for _, kind := range []string{"web", "app"} {
		add(store.Event{Family: store.FamilyViews, EventName: "$page_view", TS: at("2026-08-25", 0), Kind: kind,
			ActorID: "m02", Path: "/home", Platform: "web"})
	}
	add(store.Event{Family: store.FamilyViews, EventName: "$page_view", TS: at("2026-08-26", 7200), Kind: "web",
		ActorID: "a1", ActorKind: store.ActorUser, UserID: "u1", Host: "blog.example.com", Path: "/live",
		Platform: "web", Consent: store.ConsentGiven})
	for i := 1; i <= 10; i++ {
		for _, day := range []string{"2026-08-01", "2026-08-08"} {
			if day == "2026-08-08" && i > 4 {
				continue
			}
			user := fmt.Sprintf("r%02d", i)
			add(store.Event{Family: store.FamilyViews, EventName: "$page_view", TS: at(day, i*60), Kind: "web",
				ActorID: user, ActorKind: store.ActorUser, UserID: user, Host: "blog.example.com", Path: "/",
				Platform: "web"})
		}
	}
	for i, user := range []string{"u1", "u2", "u3", "u4", "u4"} {
		add(store.Event{Family: store.FamilyProduct, EventName: "signup", TS: at("2026-08-20", 3600+i*60), Kind: "web",
			ActorID: user, ActorKind: store.ActorUser, UserID: user, Platform: "web",
			Attributes: map[string]string{"plan": "pro"}})
	}
	add(store.Event{Family: store.FamilyProduct, EventName: "signup", TS: at("2026-08-21", 3600), Kind: "web",
		ActorID: "u1", ActorKind: store.ActorUser, UserID: "u1", UserName: "Jane Doe", GroupID: "g1", GroupName: "Acme Inc",
		Platform: "web", Attributes: map[string]string{"plan": "team"}})
	add(store.Event{Family: store.FamilyProduct, EventName: "signup", TS: at("2026-08-21", 3660), Kind: "web",
		ActorID: "u2", ActorKind: store.ActorUser, UserID: "u2", GroupID: "g2",
		Platform: "web", Path: "/pricing", UTMSource: "newsletter", Attributes: map[string]string{"plan": "team"}})
	measure := func(day string, sec int, name, kind, browser string, value float64) {
		add(store.Event{Family: store.FamilyMeasures, EventName: name, TS: at(day, sec), Kind: "web",
			ActorID: "a1", ActorKind: store.ActorUser, UserID: "u1", Platform: "web", Browser: browser,
			Value: &value, Measure: kind})
	}
	measure("2026-08-20", 0, "checkout_api", store.MeasureTime, "chrome", 200)
	evs[len(evs)-1].Attributes = map[string]string{"tier": "gold"}
	measure("2026-08-20", 60, "checkout_api", store.MeasureTime, "firefox", 200)
	for i := range 3 {
		measure("2026-08-20", 120+i*60, "$cls", store.MeasureNumber, "chrome", 0)
	}
	measure("2026-08-21", 7200, "checkout_api", store.MeasureTime, "chrome", 300)
	evs[len(evs)-1].Path = "/checkout"
	measure("2026-08-21", 7200, "$cls", store.MeasureNumber, "chrome", 0)
	return evs
}

// templateFiles is the template's own event files while hostTemplate
// builds it, served for reporting.Migrate, until t ends.
func templateFiles(ctx context.Context, t testing.TB, dir, raw string, st store.Store) (*hostFiles, error) {
	reg := manage.New(st, slog.New(slog.DiscardHandler))
	if err := reg.Reload(ctx); err != nil {
		return nil, err
	}
	return serveHostFiles(t, dir, raw, reg), nil
}

// hostEventN numbers the events writeHostEvents writes, so no two share an id.
var hostEventN atomic.Int64

// writeHostEvents appends evs to the logs of h's event files, which the
// reader reads as the open logs of their days: each gets an id, its
// received_at, and an install actor and a sample rate of 1 when unset.
func writeHostEvents(t *testing.T, h *host, evs ...store.Event) {
	t.Helper()
	log, err := archive.OpenLog(h.eventsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	for i := range evs {
		e := &evs[i]
		e.ID = fmt.Sprintf("0190dddd-0000-7000-8000-%012d", hostEventN.Add(1))
		if e.ActorID == "" {
			e.ActorID = "a1"
		}
		if e.ActorKind == "" {
			e.ActorKind = store.ActorInstall
		}
		if e.SampleRate == 0 {
			e.SampleRate = 1
		}
		e.ReceivedAt = e.TS
	}
	if err := log.WriteEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}
