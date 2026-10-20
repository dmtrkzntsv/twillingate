package reporting

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"math"
	"math/rand/v2"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// paritySQLite holds, by dashboard directory, the system widgets' SQL as
// it ran on SQLite before the switch to the event files (commit 8c7cd65):
// the reference TestSystemWidgetsMatchSQLite holds the files' SQL to.
const paritySQLite = "testdata/system-sqlite"

// parityDashboards is each system dashboard directory's id.
var parityDashboards = map[string]int64{
	"views": 1, "product": 2, "users": 3, "groups": 4, "retention": 5, "vitals": 6, "measures": 7,
}

// parityTies names the widgets ("dir/name") whose SQLite ORDER BY leaves
// ties: the output columns it sorts by. SQLite's order among rows equal on
// those is its own, and the files' SQL breaks the tie on the group key,
// so rows that tie are compared as a set, in place; the keys must still
// come in the same order. An empty list is a widget with no ORDER BY at
// all (a map). Every other widget's order is total and compared row by
// row.
var parityTies = map[string][]string{
	"groups/top-groups":            {"Actions"},
	"users/top-users":              {"Actions"},
	"product/app-versions":         {"Events"},
	"product/events":               {"Total"},
	"product/users-by-app-version": {"x", "y"},
	"views/campaigns":              {"Visitors"},
	"views/consent-states":         {"Visitors"},
	"views/countries":              {"value"},
	"views/devices":                {"value"},
	"views/display-resolutions":    {"value"},
	"views/hosts":                  {"Views"},
	"views/referrers":              {"value"},
	"views/top-pages":              {"Views"},
	"views/visitors-by-country":    {},
}

// parityPresets is the presets TestSystemWidgetsMatchSQLite compares:
// every one (parity_race_test.go narrows them under -race).
var parityPresets = systemPresets

// parityClock is Retention's "reached" on SQLite, which lagged the clock
// by three hours so yesterday counted only once the 03:00 UTC pass had
// recounted it; on the files a day counts from the start of the next
// (plan Decision 6). Between 00:00 and 03:00 UTC the two differ at the
// youngest offset, so the reference reads today's date, as the files do,
// and the test passes at any hour.
var parityClock = strings.NewReplacer("date('now', '-3 hours')", "date('now')")

// TestSystemWidgetsMatchSQLite is the switch's parity check: the same
// events go into SQLite (raw rows, identities, and agg_retention passed
// day by day through yesterday) and into the event files (day files for
// the past, today's log open), and every system widget, for every preset
// and both projects, reads the same columns and rows from the files as
// its SQL before the switch read from SQLite: numbers within 1e-9
// relative, text exactly.
//
// SQLite's views stitch an aggregate half to a live half over raw rows;
// no rollup runs here, so every day is raw and the views read the raw
// rows only. The differences the switch accepts (plan Decisions 4-7) are
// kept from arising rather than skipped:
//
//   - No (other) rows: SQLite runs with no values or identities cap, and
//     the generator stays below the default caps besides (100 values per
//     day and dimension, 1000 identities per day).
//   - Every attribute breaks down on the files, only declared ones on
//     SQLite: the one custom attribute sent, plan, is declared.
//   - first_seen from the files' identities, not SQLite's retained days:
//     both hold every day of the generator's.
//   - Retention counts yesterday from 00:00 UTC (parityClock).
//
// And the ones the switch does not change, but the two stores show
// differently:
//
//   - Row order among ties (parityTies).
//   - Session ids that are small integers: SQLite keyed a declared id "2"
//     and the second gap session of the same actor alike; the generator's
//     ids are never integers.
//   - Actors PruneActors drops from agg_retention past the aggregate
//     window: no prune runs here, and the files keep every day.
//   - The widgets' LIMITs: no dimension has as many values as its widget
//     shows, and no range as many users or groups as the top 100, so no
//     cut falls through a tie.
func TestSystemWidgetsMatchSQLite(t *testing.T) {
	ctx := context.Background()
	st, dbPath := newTestStore(t)
	files := &testFiles{Files: archivetest.New(t), st: st, timeout: 60 * time.Second, maxRows: 10000}
	if err := Migrate(ctx, st, files); err != nil {
		t.Fatal(err)
	}
	// No cap, as ATTRIBUTE_VALUES_TOP_N=0 and IDENTITIES_TOP_N=0 write
	// them: the files fold nothing into (other) (plan Decision 4), and
	// without a cap the views build none of the arrays an (other) row is
	// counted from.
	for _, key := range []string{"attributes_top_n", "identities_top_n"} {
		if err := st.SetMeta(ctx, key, "0"); err != nil {
			t.Fatal(err)
		}
	}
	today := civil.Today(time.Now().UTC())
	var projects []int64
	var evs []store.Event
	for seed, name := range []string{"parity-a", "parity-b"} {
		id, err := st.CreateProject(ctx,
			store.RegistryProject{Name: name, AllowedOrigins: "[]"},
			store.AuditEntry{Actor: "test", Action: "project.create"})
		if err != nil {
			t.Fatal(err)
		}
		projects = append(projects, id)
		evs = append(evs, parityEvents(id, uint64(seed+1), today)...)
	}

	start := time.Now()
	if err := st.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertIdentities(ctx, parityIdentities(evs)); err != nil {
		t.Fatal(err)
	}
	type retention interface {
		UpsertActors(context.Context, int64, civil.Date) error
		AggregateRetentionDay(context.Context, int64, civil.Date) error
	}
	for _, pid := range projects {
		for d := today.AddDays(-parityDays + 1); d.Before(today); d = d.AddDays(1) {
			if err := st.(retention).UpsertActors(ctx, pid, d); err != nil {
				t.Fatal(err)
			}
			if err := st.(retention).AggregateRetentionDay(ctx, pid, d); err != nil {
				t.Fatal(err)
			}
		}
	}
	files.Write(t, evs...)
	files.Maintain(t, today.String())
	t.Logf("%d events into SQLite and the files in %s", len(evs), time.Since(start).Round(time.Millisecond))

	old, err := readsql.Open(dbPath, 60*time.Second, 100000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { old.Close() })
	svc := New(st, files, Options{})

	// Every system widget with SQL has its reference, and every reference
	// its widget.
	have, err := fs.Glob(systemFS, "system/*/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range have {
		names = append(names, strings.TrimPrefix(n, "system/"))
	}
	var refs []string
	for dir := range parityDashboards {
		got, err := fs.Glob(os.DirFS(paritySQLite), dir+"/*.sql")
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, got...)
	}
	slices.Sort(names)
	slices.Sort(refs)
	if !slices.Equal(names, refs) {
		t.Fatalf("system widgets with SQL %v; references %v", names, refs)
	}
	for key := range parityTies {
		if !slices.Contains(refs, key+".sql") {
			t.Errorf("parityTies names %s, which is not a system widget", key)
		}
	}

	start = time.Now()
	compared, rows := 0, 0
	for _, ref := range refs {
		key := strings.TrimSuffix(ref, ".sql")
		dir, name := path.Split(key)
		b, err := os.ReadFile(path.Join(paritySQLite, ref))
		if err != nil {
			t.Fatal(err)
		}
		q := parityClock.Replace(string(b))
		if strings.Contains(q, "hours") {
			t.Fatalf("%s: a clock parityClock does not freeze", key)
		}
		params, err := old.Check(q)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		id := systemWidgetOf(t, svc, parityDashboards[strings.TrimSuffix(dir, "/")], name)
		ties, tied := parityTies[key]
		t.Run(key, func(t *testing.T) {
			for _, pid := range projects {
				for _, preset := range parityPresets {
					from, to := presetDates(preset, today)
					want, err := old.Query(ctx, q, parityArgs(params, pid, from, to)...)
					if err != nil {
						t.Fatalf("SQLite %s: %v", preset, err)
					}
					data, err := svc.WidgetData(ctx, DataRequest{WidgetID: id, ProjectID: pid, From: from, To: to})
					if err != nil {
						t.Fatalf("files %s: %v", preset, err)
					}
					got := data.Data.(readsql.Result)
					if data.Page != nil && data.Page.Matched != len(got.Rows) {
						t.Fatalf("%s: a page of %d rows of %d", preset, len(got.Rows), data.Page.Matched)
					}
					if preset == "7d" && len(want.Rows) == 0 {
						t.Errorf("project %d, 7d: no rows; the generator does not exercise it", pid)
					}
					if err := matchResults(want, got, ties, tied); err != nil {
						t.Errorf("project %d, %s (%s to %s): %v", pid, preset, from, to, err)
					}
					compared++
					rows += len(want.Rows)
				}
			}
		})
	}
	t.Logf("%d results, %d rows, compared in %s", compared, rows, time.Since(start).Round(time.Millisecond))
}

// parityArgs is the named arguments the SQLite source bound for the
// parameters q uses: :project as an integer, :from and :to as
// YYYY-MM-DD text.
func parityArgs(params []string, project int64, from, to string) []any {
	var args []any
	for _, p := range params {
		switch p {
		case ":project":
			args = append(args, sql.Named("project", project))
		case ":from":
			args = append(args, sql.Named("from", from))
		case ":to":
			args = append(args, sql.Named("to", to))
		}
	}
	return args
}

// matchResults reports how got differs from want: other columns, rows or
// cells (sameCell). With tied, rows equal on the columns ties names are
// matched as a set within their run, which must sit at the same place in
// both.
func matchResults(want, got readsql.Result, ties []string, tied bool) error {
	if !slices.Equal(want.Columns, got.Columns) {
		return fmt.Errorf("columns %v, SQLite %v", got.Columns, want.Columns)
	}
	if want.Truncated || got.Truncated {
		return fmt.Errorf("truncated: SQLite %t, files %t", want.Truncated, got.Truncated)
	}
	if len(want.Rows) != len(got.Rows) {
		return fmt.Errorf("%d rows, SQLite %d:\n files  %v\n SQLite %v", len(got.Rows), len(want.Rows), got.Rows, want.Rows)
	}
	if !tied {
		for i := range want.Rows {
			if !sameRow(want.Rows[i], got.Rows[i]) {
				return fmt.Errorf("row %d: %v, SQLite %v", i, got.Rows[i], want.Rows[i])
			}
		}
		return nil
	}
	var idx []int
	for _, c := range ties {
		i := slices.Index(want.Columns, c)
		if i < 0 {
			return fmt.Errorf("no column %s to order ties by", c)
		}
		idx = append(idx, i)
	}
	sameKey := func(a, b []string) bool {
		for _, i := range idx {
			if !sameCell(a[i], b[i]) {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(want.Rows); {
		j := i + 1
		for j < len(want.Rows) && sameKey(want.Rows[j], want.Rows[i]) {
			j++
		}
		used := make([]bool, j-i)
		for k := i; k < j; k++ {
			if !sameKey(got.Rows[k], want.Rows[i]) {
				return fmt.Errorf("row %d: %v, SQLite has %v there", k, got.Rows[k], want.Rows[k])
			}
			found := false
			for m := i; m < j && !found; m++ {
				if !used[m-i] && sameRow(want.Rows[m], got.Rows[k]) {
					used[m-i], found = true, true
				}
			}
			if !found {
				return fmt.Errorf("row %d: %v, which SQLite lacks among rows %d-%d: %v", k, got.Rows[k], i, j-1, want.Rows[i:j])
			}
		}
		i = j
	}
	return nil
}

// sameRow is sameCell over each pair of cells.
func sameRow(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameCell(a[i], b[i]) {
			return false
		}
	}
	return true
}

// sameCell: equal text, or two numbers within 1e-9 of each other
// relative to the larger, so 2 is 2.0 and a sum's last bits may differ.
func sameCell(a, b string) bool {
	if a == b {
		return true
	}
	x, okx := readsql.CellNumber(a)
	y, oky := readsql.CellNumber(b)
	return okx && oky && math.Abs(x-y) <= 1e-9*math.Max(math.Abs(x), math.Abs(y))
}

// parityIdentities is the names evs carry, as ingest upserts them, each
// once: an id's name never changes in parityEvents.
func parityIdentities(evs []store.Event) []store.Identity {
	var out []store.Identity
	seen := map[store.Identity]bool{}
	for _, e := range evs {
		for _, id := range []store.Identity{
			{ProjectID: e.ProjectID, Kind: store.KindGroup, ID: e.GroupID, Name: e.GroupName},
			{ProjectID: e.ProjectID, Kind: store.KindUser, ID: e.UserID, Name: e.UserName},
		} {
			if id.ID != "" && id.Name != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// parityDays is how many days parityEvents covers, ending today.
const parityDays = 100

// parityEvents is parityDays days of one project's events, ending today,
// drawn from seed so both stores get the same rows and a failure
// reproduces. Every day:
//
//   - anonymous web visitors (actor kind connection), some in a group;
//   - the same user and group ids in every project, named after seed, so
//     a name read from the wrong project shows;
//   - signed-in users (actor kind user) from a pool that grows by one
//     every third day, each back on about a quarter of the days after, on
//     the web or in the app, named or not, each in one of nine groups;
//   - new app installs (actor kind install), each back on later days ever
//     more rarely up to 45 days on, one in five signing in on its third
//     return under the same actor id (the sticky user kind), in a group
//     new every ten days;
//   - for each, a visit of one to four views: no session id, with a gap
//     past 30 minutes now and then; one id; one id across a gap past 30
//     minutes; or two ids a minute apart; and product events with plan;
//   - Web Vitals at sample rates 1, 1/2 and 1/4, and server metrics of
//     every measure kind, one name sent as two kinds.
//
// No event falls in the last three hours of a day or past midnight.
func parityEvents(project int64, seed uint64, today civil.Date) []store.Event {
	rng := rand.New(rand.NewPCG(seed, 0x7477696c6c))
	pick := func(xs ...string) string { return xs[rng.IntN(len(xs))] }
	var evs []store.Event
	n := 0
	add := func(e store.Event) {
		n++
		e.ID, e.ProjectID, e.ReceivedAt = fmt.Sprintf("p%d-%d", project, n), project, e.TS
		evs = append(evs, e)
	}

	type actor struct {
		id, kind, userID, userName, groupID, groupName, installID string
		app                                                       bool
		platform, appVersion, plan                                string
		rate                                                      float64
	}
	userOf := func(k int) actor {
		a := actor{id: fmt.Sprintf("ua-%02d", k), kind: "user", userID: fmt.Sprintf("user-%02d", k),
			groupID: fmt.Sprintf("grp-%d", k%9), app: k%4 == 0, plan: []string{"free", "pro", "team"}[k%3],
			rate: []float64{1, 1, 0.5, 0.25}[k%4]}
		if k%3 != 0 {
			a.userName = fmt.Sprintf("User %d.%02d", seed, k)
		}
		if k%9%2 == 0 {
			a.groupName = fmt.Sprintf("Team %d.%d", seed, k%9)
		}
		if a.app {
			a.platform, a.appVersion = []string{"ios", "android"}[k%8/4], []string{"2.4.1", "2.5.0", "2.5.1", "3.0.0"}[k%7%4]
		}
		return a
	}

	// visit sends a's views, their Web Vitals and its product events on day,
	// back days before today, from a random time before 15:00.
	visit := func(a actor, day civil.Date, back int) {
		start := day.Time().Add(time.Duration(rng.IntN(15))*time.Hour + time.Duration(rng.IntN(60))*time.Minute + time.Duration(rng.IntN(60))*time.Second)
		base := store.Event{Kind: "web", ActorID: a.id, ActorKind: a.kind, UserID: a.userID, UserName: a.userName,
			GroupID: a.groupID, GroupName: a.groupName, InstallID: a.installID,
			Country:       pick("US", "US", "US", "DE", "DE", "FR", "GB", "CA", "IN", "BR", "JP", "NL", "SE", "ES", "PL", ""),
			Consent:       []store.Consent{store.ConsentGiven, store.ConsentGiven, store.ConsentNone, store.ConsentUnknown}[rng.IntN(4)],
			BrowserLocale: pick("en-US", "en-US", "de-DE", "fr-FR")}
		if a.app {
			base.Kind, base.Platform, base.AppVersion, base.AppLocale = "app", a.platform, a.appVersion, pick("en", "de")
			if a.platform == "ios" {
				base.OS, base.OSVersion, base.DeviceModel = "ios", pick("17.4", "18.0"), pick("iPhone15,3", "iPhone14,2")
				base.DisplayWidth, base.DisplayHeight = 390, 844
			} else {
				base.OS, base.OSVersion, base.DeviceModel = "android", "14", pick("Pixel 8", "SM-S911B")
				base.DisplayWidth, base.DisplayHeight = 412, 915
			}
			base.Device = "mobile"
		} else {
			base.Platform = "web"
			sys := [][2]string{{"macos", "14"}, {"macos", "15"}, {"windows", "11"}, {"windows", "10"}, {"linux", ""}}[rng.IntN(5)]
			br := [][2]string{{"chrome", "126"}, {"chrome", "127"}, {"chrome", "127"}, {"safari", "17"}, {"safari", "18"},
				{"firefox", "128"}, {"edge", "126"}, {"opera", "111"}, {"brave", "1.67"}}[rng.IntN(9)]
			base.OS, base.OSVersion, base.Browser, base.BrowserVersion = sys[0], sys[1], br[0], br[1]
			base.Device = pick("desktop", "desktop", "desktop", "tablet", "")
			d := [][2]int{{1920, 1080}, {1920, 1080}, {1440, 900}, {2560, 1440}, {1366, 768}, {0, 0}}[rng.IntN(6)]
			base.DisplayWidth, base.DisplayHeight = d[0], d[1]
			base.Host = pick("example.com", "example.com", "app.example.com", "docs.example.com")
			base.ReferrerSource = pick("", "", "google", "google", "twitter", "github", "news.ycombinator.com", "duckduckgo", "reddit")
			utm := [][3]string{{}, {}, {}, {"newsletter", "email", "launch"}, {"twitter", "social", ""}, {"google", "cpc", "brand"}, {"partner", "", ""}}[rng.IntN(7)]
			base.UTMSource, base.UTMMedium, base.UTMCampaign = utm[0], utm[1], utm[2]
		}

		views := 1 + rng.IntN(4)
		mode := rng.IntN(4)
		ts := start
		for v := 0; v < views; v++ {
			if v > 0 {
				gap := time.Duration(1+rng.IntN(10)) * time.Minute
				if (mode == 0 || mode == 2) && rng.IntN(4) == 0 {
					gap = time.Duration(31+rng.IntN(60)) * time.Minute
				}
				ts = ts.Add(gap)
			}
			e := base
			e.Family, e.TS = store.FamilyViews, ts
			if a.app {
				e.EventName, e.Path = "$screen_view", pick("/home", "/home", "/settings", "/feed", "")
			} else {
				e.EventName = "$page_view"
				e.Path = pick("/", "/", "/", "/pricing", "/pricing", "/docs", "/docs/start", "/blog", "/blog/launch",
					"/about", "/signup", "/login", "/account", "/changelog", "/careers", "/contact", "/terms")
			}
			switch mode {
			case 1, 2:
				e.SessionID = fmt.Sprintf("s-%s-%d", a.id, back)
			case 3:
				e.SessionID = fmt.Sprintf("s-%s-%d-%c", a.id, back, 'a'+rune(v*2/views))
			}
			add(e)

			// The first view of a web visit is a page load: its Web Vitals.
			if v == 0 && !a.app {
				for _, m := range []struct {
					name, measure string
					value         float64
				}{
					{"$lcp", "time", 600 + rng.ExpFloat64()*1600},
					{"$fcp", "time", 300 + rng.ExpFloat64()*900},
					{"$ttfb", "time", 50 + rng.ExpFloat64()*500},
					{"$inp", "time", 20 + rng.ExpFloat64()*180},
					{"$cls", "number", []float64{0, 0, rng.Float64() * 0.4}[rng.IntN(3)]},
				} {
					if rng.IntN(5) < 2 {
						continue
					}
					value := m.value
					add(store.Event{Family: store.FamilyMeasures, EventName: m.name, TS: ts.Add(2 * time.Second),
						Kind: "web", ActorID: a.id, ActorKind: a.kind, Platform: "web", Path: e.Path,
						Browser: base.Browser, BrowserVersion: base.BrowserVersion, Device: base.Device,
						Value: &value, Measure: m.measure, SampleRate: a.rate})
				}
			}
		}
		if a.app && rng.IntN(2) == 0 {
			value := 200 + rng.ExpFloat64()*400
			add(store.Event{Family: store.FamilyMeasures, EventName: "render", TS: start.Add(time.Second),
				Kind: "app", ActorID: a.id, ActorKind: a.kind, Platform: a.platform, Device: "mobile",
				Value: &value, Measure: "time", SampleRate: 1})
		}

		// Product events, busiest names first by weight.
		names := []string{"click", "click", "click", "click", "search", "search", "search",
			"open_settings", "open_settings", "share", "purchase", "export", "signup"}
		for k := rng.IntN(4); k > 0; k-- {
			e := base
			e.Family, e.TS = store.FamilyProduct, start.Add(time.Duration(rng.IntN(1800))*time.Second)
			e.EventName = names[rng.IntN(len(names))]
			e.Host, e.Path, e.ReferrerSource, e.UTMSource, e.UTMMedium, e.UTMCampaign = "", "", "", "", "", ""
			if a.plan != "" && rng.IntN(10) < 7 {
				e.Attributes = map[string]string{"plan": a.plan}
			}
			add(e)
		}
	}

	for back := parityDays - 1; back >= 0; back-- {
		day := today.AddDays(-back)

		// Anonymous web visitors, a few in a group of their own.
		for i := 3 + rng.IntN(4); i > 0; i-- {
			a := actor{id: fmt.Sprintf("c-%d-%d", back, i), kind: "connection", rate: []float64{1, 0.5, 0.25}[i%3]}
			if rng.IntN(5) == 0 {
				a.groupID = fmt.Sprintf("grp-anon-%d", rng.IntN(4))
				if a.groupID != "grp-anon-3" {
					a.groupName = fmt.Sprintf("Guests %d.%s", seed, a.groupID[len(a.groupID)-1:])
				}
			}
			if rng.IntN(3) == 0 {
				a.plan = "free"
			}
			visit(a, day, back)
		}

		// Users: user k first appears 3k days after the first day.
		for k := 0; 3*k <= parityDays-1-back; k++ {
			if 3*k == parityDays-1-back || rng.IntN(4) == 0 {
				visit(userOf(k), day, back)
			}
		}

		// Installs: new ones every day, back ever more rarely.
		for j := 1 + rng.IntN(2); j > 0; j-- {
			id := fmt.Sprintf("in-%d-%d", back, j)
			a := actor{id: id, kind: "install", installID: id, app: true, platform: pick("ios", "android"),
				appVersion: pick("2.4.1", "2.5.0", "2.5.1", "3.0.0"), plan: "free", rate: 1}
			visit(a, day, back)
			signsIn := rng.IntN(5) == 0
			returns := 0
			for off := 1; off <= 45 && off <= back; off++ {
				if rng.Float64() >= 0.6*math.Exp(-float64(off)/8) {
					continue
				}
				returns++
				if signsIn && returns == 3 {
					a.kind, a.userID, a.userName = "user", "conv-"+id, "Converted "+id
					g := 9 + back/10
					a.groupID, a.groupName = fmt.Sprintf("grp-%d", g), ""
					if g%2 == 0 {
						a.groupName = fmt.Sprintf("Team %d.%d", seed, g)
					}
				}
				visit(a, today.AddDays(-back+off), back-off)
			}
		}

		// Server metrics: every measure kind, render also as a number.
		for i := 2 + rng.IntN(3); i > 0; i-- {
			for _, m := range []struct {
				name, measure string
				value         float64
			}{
				{"checkout_api", "time", 120 + rng.ExpFloat64()*300},
				{"queue_depth", "number", float64(rng.IntN(40))},
				{"payload", "size", 1000 + rng.ExpFloat64()*50000},
				{"render", "number", 1 + float64(rng.IntN(9))},
			} {
				if rng.IntN(3) == 0 {
					continue
				}
				value := m.value
				add(store.Event{Family: store.FamilyMeasures, EventName: m.name,
					TS: day.Time().Add(time.Duration(rng.IntN(20*3600)) * time.Second), Kind: "web",
					ActorID: "server", ActorKind: "connection", Platform: "web",
					Value: &value, Measure: m.measure, SampleRate: []float64{1, 0.5}[rng.IntN(2)]})
			}
		}
	}
	return evs
}
