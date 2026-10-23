//go:build measure

// Form attribution's time on a bench dataset (native engine step 4): a
// batch of 100 lookups, as the jobs' minute tick asks them, natively and
// through the old SQL. Not part of make check.
//
//	MEASURE_DIR=~/.cache/twbench-native/3f/1m TMPDIR=~/.cache/gotmp \
//	  go test -tags measure -run TestMeasureAttribution -v ./internal/native
//
// MEASURE_DIR's raw/ is copied, and a day's open log of MEASURE_LOG_VIEWS
// views (default 33,000: a month's million a day) is added on the day
// after its last, so the window reads stored sessions and a live gap.
package native

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/archive/archivetest"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestMeasureAttribution(t *testing.T) {
	src := os.Getenv("MEASURE_DIR")
	if src == "" {
		t.Skip("MEASURE_DIR unset")
	}
	data := t.TempDir()
	dir := filepath.Join(data, "raw")
	archivetest.CopyDir(t, filepath.Join(src, "raw"), dir)
	latest, err := archive.LatestDays(dir)
	if err != nil {
		t.Fatal(err)
	}
	last, err := time.Parse("2006-01-02", latest[1])
	if err != nil {
		t.Fatal(err)
	}
	logDay := last.AddDate(0, 0, 1)

	self, _ := os.Executable()
	eng, err := engine.New(engine.Options{Path: self, Env: []string{archivetest.ChildEnv + "=1"},
		AllowedDirs: []string{dir}, SpillRoot: filepath.Join(data, "spill"), SlotDir: filepath.Join(data, "slots"),
		MaxChildren: 1, MemoryLimit: "512MB", Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	reader, err := archive.NewReader(dir, eng)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// The actors: the stored views' last days, users among them.
	res, err := reader.Query(ctx, fmt.Sprintf(`SELECT actor_kind, actor_id, any_value(install_id) FROM views
WHERE ts >= TIMESTAMP '%s' GROUP BY ALL ORDER BY actor_kind DESC, actor_id LIMIT 100`, last.AddDate(0, 0, -3).Format("2006-01-02")), 0, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	at := logDay.Add(18 * time.Hour)
	var ls []archive.Lookup
	for i, row := range res.Rows {
		kind, _ := row[0].(string)
		id, _ := row[1].(string)
		install, _ := row[2].(string)
		ls = append(ls, archive.Lookup{ProjectID: 1, ID: "sub-" + strconv.Itoa(i), ActorKind: kind, ActorID: id,
			InstallID: install, ConnectionID: "conn-" + strconv.Itoa(i), At: at})
	}
	// A fifth of them users the aliases pair with installs, whose
	// lookups read the aliases too.
	res, err = reader.Query(ctx, "SELECT DISTINCT user_id FROM aliases ORDER BY user_id LIMIT 20", 0, archive.Rules{})
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range res.Rows {
		id, _ := row[0].(string)
		ls[i*5] = archive.Lookup{ProjectID: 1, ID: ls[i*5].ID, ActorKind: store.ActorUser, ActorID: id, At: at}
	}
	users := 0
	for _, l := range ls {
		if l.ActorKind == store.ActorUser {
			users++
		}
	}

	// The open log: views of the same actors, and others.
	n := 33_000
	if s := os.Getenv("MEASURE_LOG_VIEWS"); s != "" {
		n, _ = strconv.Atoi(s)
	}
	lg, err := archive.OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	evs := make([]store.Event, 0, n)
	for i := range n {
		l := ls[i%len(ls)]
		ts := logDay.Add(time.Duration(i) * (17 * time.Hour / time.Duration(n)))
		actor := l.ActorID
		if i%3 != 0 {
			actor = "other-" + strconv.Itoa(i%5000)
		}
		evs = append(evs, store.Event{ID: fmt.Sprintf("log-%06d", i), ProjectID: 1, Family: store.FamilyViews,
			EventName: "$page_view", TS: ts, ReceivedAt: ts, Kind: "web", ActorKind: l.ActorKind, ActorID: actor,
			Path: "/p" + strconv.Itoa(i%50), ReferrerSource: []string{"", "google", ""}[i%3]})
	}
	if err := lg.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	lg.Close()

	e := New(Options{Dir: dir})
	// grow appends one view to the open log, as live ingest does between
	// two minute ticks: the reader's tables are then built again.
	grown := 0
	grow := func() {
		lg, err := archive.OpenLog(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer lg.Close()
		ts := logDay.Add(17*time.Hour + time.Duration(grown)*time.Second)
		grown++
		if err := lg.WriteEvents(ctx, []store.Event{{ID: fmt.Sprintf("grow-%d", grown), ProjectID: 1, Family: store.FamilyViews,
			EventName: "$page_view", TS: ts, ReceivedAt: ts, Kind: "web", ActorKind: "install", ActorID: "grower", Path: "/"}}); err != nil {
			t.Fatal(err)
		}
	}
	run := func(name string, f func() ([]archive.Found, error)) []time.Duration {
		var ds []time.Duration
		for i := range 8 {
			if i >= 4 {
				grow()
			}
			start := time.Now()
			found, err := f()
			if err != nil {
				t.Fatal(err)
			}
			ds = append(ds, time.Since(start))
			if len(found) != len(ls) {
				t.Fatalf("%s: %d found", name, len(found))
			}
		}
		t.Logf("%s: first %v, then %v; each after the log grew %v", name, ds[0], ds[1:4], ds[4:])
		return ds
	}
	t.Logf("%d lookups (%d users), window 7 days to %s, open log of %d views", len(ls), users, at.Format(time.RFC3339), n)
	var got, want []archive.Found
	run("native", func() (f []archive.Found, err error) {
		got, err = e.Attribute(ctx, ls, 7, archive.Rules{})
		return got, err
	})
	run("duckdb", func() (f []archive.Found, err error) {
		want, err = oracleAttribute(ctx, reader, dir, ls, 7, archive.Rules{})
		return want, err
	})
	sortFound(got)
	sortFound(want)
	sourced := 0
	for _, f := range got {
		if !reflect.DeepEqual(f.Source, store.Event{}) {
			sourced++
		}
	}
	t.Logf("%d of %d sourced", sourced, len(got))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("native and SQL differ:\n%+v\n%+v", got, want)
	}
}
