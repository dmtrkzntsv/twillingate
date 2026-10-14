package app

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/pipeline"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

// TestImportFillsTheFilesFromSQLite: an install whose database holds rows
// the files lack gets every one of them into the files, once (raw-first
// D19, PR 5c Review Focus 1–3). The rows come from before the files began,
// from a day split by that start, and from a month merged without them.
// After the import and the next pass, per day the files count what SQLite
// counts with no id twice; September is one month file again; the imported
// rows carry the install id and the user's name; and importing again
// writes nothing.
func TestImportFillsTheFilesFromSQLite(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	st := storetest.Open(t, filepath.Join(data, "twillingate.db"))
	reg := manage.New(st, slog.Default())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manage.NewOps(reg, st).CreateProject(ctx, "test", manage.ProjectSpec{Name: "App"}); err != nil {
		t.Fatal(err)
	}
	log, err := archive.OpenLog(filepath.Join(data, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	self, _ := os.Executable()
	eng, err := engine.New(engine.Options{Path: self, Env: []string{appEngineChild + "=1"},
		AllowedDirs: []string{log.Dir()}, SpillRoot: filepath.Join(data, "engine", "spill"), SlotDir: filepath.Join(data, "engine", "slots"),
		MaxChildren: 1, MemoryLimit: "256MB", Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	a := archive.New(log, eng, slog.Default())
	pass := func(day string) {
		t.Helper()
		d, _ := civil.Parse(day)
		if err := a.Maintain(ctx, d, 1, 400); err != nil {
			t.Fatal(err)
		}
	}

	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	row := func(id, ts string) store.Event {
		return store.Event{ID: id, ProjectID: 1, Family: store.FamilyProduct, EventName: "x",
			TS: at(ts), ReceivedAt: at(ts), ActorID: "h", ActorKind: store.ActorConnection, Platform: "web"}
	}
	s1 := row("s1", "2026-09-29T10:00:00Z")
	s1.ActorID, s1.ActorKind, s1.UserID = "u1", store.ActorUser, "u1"
	o1 := row("o1", "2026-10-01T10:00:00Z")
	o1.ActorID, o1.ActorKind = "i1", store.ActorInstall

	both := pipeline.Sinks{st, log}
	// September 30 reached both stores and September was merged without s1.
	if err := both.WriteEvents(ctx, []store.Event{row("s2", "2026-09-30T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	pass("2026-10-05")
	// October 1 and 11 are split: one row in both stores, one only in SQLite.
	if err := both.WriteEvents(ctx, []store.Event{row("o2", "2026-10-01T11:00:00Z"), row("t2", "2026-10-11T02:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteEvents(ctx, []store.Event{s1, o1, row("t1", "2026-10-11T01:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertIdentities(ctx, []store.Identity{{ProjectID: 1, Kind: store.KindUser, ID: "u1", Name: "Ada"}}); err != nil {
		t.Fatal(err)
	}

	now := at("2026-10-11T04:00:00Z")
	var out bytes.Buffer
	if err := importEvents(ctx, st, a, 400, now, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("imported 3 of 6 rows")) {
		t.Fatalf("output %q: want 3 of the 6 rows written", out.String())
	}
	pass("2026-10-11") // the next start's pass

	raw, err := sql.Open("sqlite", "file:"+filepath.Join(data, "twillingate.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var want []string
	rows, err := raw.Query(`SELECT day, count(*) FROM events WHERE project_id = 1 GROUP BY day ORDER BY day`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			t.Fatal(err)
		}
		want = append(want, fmt.Sprintf("[%s %d]", day, n))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(want) != "[[2026-09-29 1] [2026-09-30 1] [2026-10-01 2] [2026-10-11 2]]" {
		t.Fatalf("SQLite per day = %v", want)
	}
	files, err := archive.NewReader(log.Dir(), eng)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(q string) string {
		t.Helper()
		res, err := files.Query(ctx, q, 0, archive.Rules{})
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(res.Rows)
	}
	perDay := "SELECT CAST(day AS VARCHAR), count(*) FROM events WHERE project_id = 1 GROUP BY day ORDER BY day"
	if got := ask(perDay); got != fmt.Sprint(want) {
		t.Fatalf("files per day = %s, SQLite %v", got, want)
	}
	if got := ask("SELECT count(*) - count(DISTINCT id) FROM events"); got != "[[0]]" {
		t.Fatalf("ids held twice: %s", got)
	}
	if got := ask("SELECT install_id FROM events WHERE id = 'o1'"); got != "[[i1]]" {
		t.Fatalf("o1's install_id = %s, want i1", got)
	}
	if got := ask("SELECT name FROM identities WHERE project_id = 1 AND kind = 'user' AND id = 'u1'"); got != "[[Ada]]" {
		t.Fatalf("u1's name = %s, want Ada", got)
	}

	eventsFiles := func() []string {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(log.Dir(), "1"))
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && e.Name() != "identities.parquet" && e.Name() != "aliases.parquet" {
				names = append(names, e.Name())
			}
		}
		slices.Sort(names)
		return names
	}
	wantFiles := []string{"2026-09.parquet", "2026-10-01.1.parquet", "2026-10-01.parquet", "2026-10-11.log", "2026-10-11.parquet"}
	if got := eventsFiles(); !slices.Equal(got, wantFiles) {
		t.Fatalf("files = %v, want %v", got, wantFiles)
	}
	out.Reset()
	if err := importEvents(ctx, st, a, 400, now, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("imported 0 of 6 rows")) {
		t.Fatalf("a second import: %q, want nothing written", out.String())
	}
	if got := eventsFiles(); !slices.Equal(got, wantFiles) {
		t.Fatalf("files after a second import = %v, want %v", got, wantFiles)
	}
}
