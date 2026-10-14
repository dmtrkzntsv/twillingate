# Raw Rows Migrate Implementation Plan (PR 5c)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Once per install, every raw row SQLite holds that the event files lack is copied into the files, so the files hold, per day and project, every row SQLite holds.

**Architecture:**
- **Where it runs:** the daily pass of the ingest process. It is a step right after the purge, before the SQLite rollups and before `Archive.Maintain`. The pass at start runs it in the background, so ingest answers meanwhile. A `meta` row marks it done. Until then every pass tries again.
- **Which rows:** per project and day, the rows SQLite received before this process started. A row the files already hold for that day is skipped. The cut-off keeps the copy out of the pipeline's way: every row received since the start goes to both SQLite and the log.
- **How they get in:** as a closed log, `<day>.<n>.closed`, the file `Log.Rotate` leaves. The same pass's `Maintain` claims, dumps and merges it like any other. No new kind of file, and no second writer of Parquet.
- **Fields SQLite never stored:** `install_id` is the actor id where the install was the actor. `user_name` and `group_name` are the latest names in `identities` (Decision 4).

**Tech Stack:** Go 1.25; modernc SQLite (`internal/store/sqlite`); DuckDB v1.5.6 through `internal/engine` (the child process); the PR 3–5a archive (`internal/archive`); the daily pass (`internal/jobs`).

**Spec:** `docs/superpowers/specs/2026-10-12-raw-first-design.md`:
- D19: only raw rows migrate;
- D2: the log, the dump and the merge;
- D5: retention by whole files;
- D17 and D18: the names and the install id on the row.

Roadmap: `docs/superpowers/plans/2026-10-12-raw-first-roadmap.md`, PR 5c. It is stacked on PR 5a (#182, `feat/raw-derived`), not on 5b: 5c touches nothing 5b changes (the SDK, `SessionAt`). Earlier plans for the conventions this one follows:
- `2026-10-12-raw-log.md`: the line, the claim, the dump, the merge;
- `2026-10-13-raw-derived.md`: the on-row fields and the parity tests in `internal/app`.

## Facts this plan rests on (read in the code, 2026-10-14)

- **SQLite has one connection** (`db.SetMaxOpenConns(1)`, `sqlite.go`). A cursor held open while files are written would stall every ingest flush. So `RawEvents` returns a day as a slice, and the cursor closes before any file work.
- **The pipeline writes SQLite first, then the log** (`pipeline.Sinks`). A row can be in SQLite and not yet in the log for the length of one flush. The `received_at` cut-off at the process start closes that window.
- **`jobs.New` runs before any listener binds** (`app.Serve`). The process receives nothing before `Runner.started`, and the previous process drained its buffer when it stopped.
- **`received_at` is `"2006-01-02T15:04:05Z"` text**, or `''` on rows older than the column. Comparing the text compares the times, and `''` sorts before every time.
- **The events key is `(family, project_id, day, id)`.** A filter `family IN ('views', 'product', 'measures') AND project_id = ? AND day = ?` seeks the key. Without `family`, it scans the table (spec, "Found along the way").
- **A month file hides its days.** `ListFiles` leaves out a day file once its month's file exists. `maintainProject` merges every final, non-open month on every pass: a month with a new day file beside its month file is merged again, the month file first (`mergeQuery`: first file wins). So a closed log of a merged month's day is in the month file at the end of the pass that dumps it.
- **`claim` names the dump's target in name order:** the day file if missing, else the next free segment. A migrated closed log of a day with no file becomes the day file.
- **`removeMonth` deletes a month once its last day is `RETENTION_DAYS` old,** closed logs included (it matches the name prefix).
- **`ViewDaysBefore`, `ProductDaysBefore` and `MeasureDaysBefore` exist** on `store.Store` and `jobs.Store`, each `SELECT DISTINCT day … WHERE project_id=? AND day < ?` over one family.

## Global Constraints

- **SQLite's schema is unchanged.** No migration and no new column: the migration only reads `events` and `identities`, and writes one `meta` row. SQLite keeps writing `events` until PR 6.
- **The line format stays `v: 1`.** Migrated lines come from `line()`, the function ingest's lines come from.
- **Files are written only by the paths that write them today.** The migration writes closed logs; Parquet comes only from `Archive.write` (dump, merge, derive).
- **`internal/archive` stays a leaf.** It imports only `engine`, `store` and `shared/civil`. `Exec` stays the archive's own; the archive's new reads go through `Query`.
- **The migration runs only where the archive does:** the ingest process (`Runner.archive != nil`). A console-only process never runs it.
- **The tables' columns are a contract** (PR 4): no column changes. What the columns hold on migrated rows is documented in the same commit as the code that writes them (CLAUDE.md, Documentation).
- **Commits and checks.** Conventional Commits with CLAUDE.md's scopes. Run `make check` before every push.
- **Toolchain.** Go is at `/usr/local/go/bin`, not on `PATH`. Set `GOTMPDIR=$HOME/.cache/gotmp`, because `/tmp`'s quota fills. Every command below assumes:
  `export PATH=$PATH:/usr/local/go/bin GOTMPDIR=$HOME/.cache/gotmp`.

## Decisions this plan takes (review them)

1. **It runs in the daily pass, not before the server starts.** D19 says "on the first boot"; the pass at start is that boot, in the background.
   - Ingest and the console answer from the first second, and the migration shares the pass's goroutine with `Maintain`, so nothing else touches `raw/` while it reads.
   - It comes right after the purge, before the SQLite rollups and their prune. The day leaving the raw window at this pass is copied before it goes.
   - Its price is a `received_at` cut-off (Decision 3), instead of nothing running beside it.
2. **It writes closed logs, not Parquet.** D19: "the engine dumps them into day files like any other day; nothing new is written for it."
   - The claim, the dump with its types, the dedup and the merge stay single code paths.
   - The pass's `Maintain` dumps them minutes later. Until then the reader reads them as logs, which is slower and gives the same answers.
3. **Rows received since this process started are not migrated.** The pipeline writes those to both stores. A row received before is copied unless the files already hold its id for its day.
   - This covers both kinds of missing row: the history from before PR 3's first start, and a batch the log missed since (a full disk: the pipeline drops a batch after its retries, and SQLite may hold it already).
   - A row stored with no `received_at` counts as received before.
4. **Copied rows get what SQLite can say about the three fields it never stored** (5a):
   - **`install_id`** is `actor_id` when `actor_kind` is `install`. An `$install_id` outranked by a `$user_id` was never stored, so it stays NULL: such a row adds no `aliases` pair.
   - **`user_name` and `group_name`** are the names `identities` holds for the row's `user_id` and `group_id`: the latest name, not the one sent with that row. Without them, 5d's `identities` would lose the name of every user not seen since the files began.

   The alternative is NULL for all three, as 5a's UPGRADES entry promised. It is one line in the store's SELECT, so it is easy to drop in review.
5. **Held ids are checked per project and day, in the engine.**
   - The engine reads the ids from the day's file and segments, its month file (filtered on `day`) and its logs (open, closed, claimed).
   - A day with no file and no log needs no query. That is every day before PR 3's first start.
   - A day's ids fit in memory: about 33,000 at the 1M-a-month cap.
6. **Days `RETENTION_DAYS` would delete are skipped:** `day <= today - RETENTION_DAYS`. This matters only where `RETENTION_RAW_DAYS` is larger than `RETENTION_DAYS`. A day in a month that is only partly expired is still copied; its month goes whole, later (D5).
7. **One `meta` row, `events_files_migrated`, holds the time the migration finished.** It is written only when every day of every project succeeded.
   - A failed day is logged, the rest go on, and the next pass runs the whole migration again. Days already copied write nothing (Decision 5).
   - To copy again, for example after restoring an older `raw/`: stop the server, delete the row, start it (`deploy/UPGRADES.md`).
8. **Archived projects migrate; purged ones have no rows.** The files keep an archived project until it is purged, as SQLite does.

## Review Focus

1. **A month merged before the migration, missing rows SQLite has.** For example, an install that ran PR 3 for over a month with a gap in its log, or that started PR 3 mid-month.
   - The copied day file sits hidden beside the month file until the same pass merges the month again.
   - After the pass, the month file holds every row once, and no day file is left.

   Pinned by Task 3 `TestImportedDayOfAMergedMonthIsMergedIn` and Task 5.
2. **A day split by PR 3's first start:** the rows before it are only in SQLite, the rows after it are in both.
   - Only the missing rows are written.
   - Per day the files count what SQLite counts, and no id appears twice.

   Pinned by Task 3 `TestImportDaySkipsWhatTheFilesHold` and Task 5.
3. **A migration cut short and run again:** a shutdown mid-pass, or a failing day.
   - Nothing is written twice.
   - `meta` is written only once the whole migration succeeds.

   Pinned by Task 3 `TestImportDayTwiceWritesOnce` and Task 4 `TestMigrationRetriesAfterAFailedDay`.
4. **A row this process received,** maybe still on its way to the log, is never copied. Pinned by Task 4 `TestDailyPassMigratesRawRowsOnce` (its row received after the start).
5. **Rows SQLite holds in shapes ingest no longer writes:**
   - `received_at` empty;
   - attribute values that are JSON numbers or `null`;
   - a `ts` with fractional seconds.

   They copy without failing the day. Pinned by Task 1 `TestRawEventsReadsOlderRows`.

---

### Task 1: a day's raw rows, as the event files hold them

**Files:**
- Create: `internal/store/sqlite/raw_events.go`
- Modify: `internal/store/store.go` (`Store` interface: add `RawEvents` next to `MeasureDaysBefore`)
- Test: `internal/store/sqlite/raw_events_test.go`

**Interfaces:**
- Produces: `func (d *DB) RawEvents(ctx context.Context, projectID int64, day civil.Date, before time.Time) ([]store.Event, error)`, also on `store.Store`. It returns the rows in `(ts, id)` order.
  - `InstallID` is `ActorID` when `ActorKind == store.ActorInstall`.
  - `UserName` and `GroupName` come from `identities`.
  - `ReceivedAt` is zero when `received_at` is `''`.
  - `Attributes` is nil when the row has none.
  - `SampleRate` is as stored (1 by default).

- [ ] **Step 1: Write the failing tests**

Create `internal/store/sqlite/raw_events_test.go`:

```go
package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func rawAt(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestRawEventsReadsBackWhatWasWritten: a day's rows come back as the
// events WriteEvents stored, in (ts, id) order, with the install id where
// the install was the actor and the latest names identities holds
// (raw-first D19, PR 5c Decision 4).
func TestRawEventsReadsBackWhatWasWritten(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	v := 12.5
	evs := []store.Event{
		{ID: "b", ProjectID: 1, Family: store.FamilyProduct, EventName: "signup",
			TS: rawAt("2026-10-11T10:00:00Z"), ReceivedAt: rawAt("2026-10-11T10:00:01Z"),
			ActorID: "u1", ActorKind: store.ActorUser, UserID: "u1", GroupID: "g1",
			Platform: "web", Country: "DE", Consent: store.ConsentGiven, SampleRate: 1,
			Attributes: map[string]string{"plan": "pro"}},
		{ID: "a", ProjectID: 1, Family: store.FamilyViews, EventName: "$page_view",
			TS: rawAt("2026-10-11T09:00:00Z"), ReceivedAt: rawAt("2026-10-11T09:00:00Z"), Kind: "web",
			ActorID: "i1", ActorKind: store.ActorInstall, Host: "x.io", Path: "/", Platform: "web",
			DisplayWidth: 390, DisplayHeight: 844, Consent: store.ConsentNone, SampleRate: 1},
		{ID: "c", ProjectID: 1, Family: store.FamilyMeasures, EventName: "load",
			TS: rawAt("2026-10-11T11:00:00Z"), ReceivedAt: rawAt("2026-10-11T11:00:00Z"),
			ActorID: "h", ActorKind: store.ActorConnection, Platform: "web",
			Value: &v, Measure: store.MeasureTime, SampleRate: 0.5},
		// Another day and another project: not read.
		{ID: "d", ProjectID: 1, Family: store.FamilyProduct, EventName: "x", TS: rawAt("2026-10-12T00:00:00Z"),
			ReceivedAt: rawAt("2026-10-12T00:00:00Z"), ActorID: "h", Platform: "web", SampleRate: 1},
		{ID: "e", ProjectID: 2, Family: store.FamilyProduct, EventName: "x", TS: rawAt("2026-10-11T09:00:00Z"),
			ReceivedAt: rawAt("2026-10-11T09:00:00Z"), ActorID: "h", Platform: "web", SampleRate: 1},
	}
	if err := d.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertIdentities(ctx, []store.Identity{
		{ProjectID: 1, Kind: store.KindUser, ID: "u1", Name: "Ada"},
		{ProjectID: 1, Kind: store.KindGroup, ID: "g1", Name: "Acme"},
	}); err != nil {
		t.Fatal(err)
	}
	day, _ := civil.Parse("2026-10-11")
	got, err := d.RawEvents(ctx, 1, day, rawAt("2026-10-12T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := evs[1], evs[0], evs[2]
	a.InstallID = "i1"
	b.UserName, b.GroupName = "Ada", "Acme"
	if want := []store.Event{a, b, c}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RawEvents =\n%+v\nwant\n%+v", got, want)
	}
}

// TestRawEventsStopsAtBefore: a row received at or after before is left
// out; a row stored without received_at counts as received before.
func TestRawEventsStopsAtBefore(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	mk := func(id, received string) store.Event {
		return store.Event{ID: id, ProjectID: 1, Family: store.FamilyProduct, EventName: "x",
			TS: rawAt("2026-10-11T09:00:00Z"), ReceivedAt: rawAt(received), ActorID: "h", Platform: "web", SampleRate: 1}
	}
	if err := d.WriteEvents(ctx, []store.Event{
		mk("early", "2026-10-11T09:59:59Z"), mk("at", "2026-10-11T10:00:00Z"), mk("old", "2026-10-11T11:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE events SET received_at = '' WHERE id = 'old'`); err != nil {
		t.Fatal(err)
	}
	day, _ := civil.Parse("2026-10-11")
	got, err := d.RawEvents(ctx, 1, day, rawAt("2026-10-11T10:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	if want := []string{"early", "old"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if !got[1].ReceivedAt.IsZero() {
		t.Fatalf("ReceivedAt of a row without one = %v, want zero", got[1].ReceivedAt)
	}
}

// TestRawEventsReadsOlderRows: rows in shapes ingest no longer writes copy
// without failing the day: a ts with fractional seconds, attribute values
// that are JSON numbers, booleans or null (a value is its JSON text, null
// is dropped), and no attributes at all.
func TestRawEventsReadsOlderRows(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if err := d.WriteEvents(ctx, []store.Event{
		{ID: "a", ProjectID: 1, Family: store.FamilyProduct, EventName: "x", TS: rawAt("2026-10-11T09:00:00Z"),
			ReceivedAt: rawAt("2026-10-11T09:00:00Z"), ActorID: "h", Platform: "web", SampleRate: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE events SET ts = '2026-10-11T09:00:00.250Z',
		attributes = '{"n":42,"b":true,"z":null,"s":"x"}' WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	day, _ := civil.Parse("2026-10-11")
	got, err := d.RawEvents(ctx, 1, day, rawAt("2026-10-12T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if want := rawAt("2026-10-11T09:00:00.25Z"); !got[0].TS.Equal(want) {
		t.Fatalf("TS = %v, want %v", got[0].TS, want)
	}
	if want := map[string]string{"n": "42", "b": "true", "s": "x"}; !reflect.DeepEqual(got[0].Attributes, want) {
		t.Fatalf("Attributes = %v, want %v", got[0].Attributes, want)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/store/sqlite -run 'TestRawEvents' -count=1`
Expected: FAIL to compile: `d.RawEvents undefined`.

- [ ] **Step 3: Write the method**

Create `internal/store/sqlite/raw_events.go`:

```go
package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// rawEventsSQL reads one project's day of raw rows received before a
// time, with the names identities holds for each row's user and group.
// The family list makes it a seek on the events key.
const rawEventsSQL = `SELECT e.id, e.family, e.event_name, e.ts, e.received_at, e.kind,
	e.actor_id, e.actor_kind, e.user_id, e.group_id, e.session_id,
	e.host, e.path, e.referrer_source, e.utm_source, e.utm_medium, e.utm_campaign,
	e.platform, e.os, e.os_version, e.os_name, e.browser, e.browser_version, e.browser_locale,
	e.app_version, e.app_locale, e.device, e.device_model, e.display_width, e.display_height,
	e.country, e.consent, e.attributes, e.value, e.measure, e.sample_rate,
	coalesce(u.name, ''), coalesce(g.name, '')
FROM events e
LEFT JOIN identities u ON e.user_id <> '' AND u.project_id = e.project_id AND u.kind = 'user' AND u.id = e.user_id
LEFT JOIN identities g ON e.group_id <> '' AND g.project_id = e.project_id AND g.kind = 'group' AND g.id = e.group_id
WHERE e.family IN ('views', 'product', 'measures') AND e.project_id = ? AND e.day = ? AND e.received_at < ?
ORDER BY e.ts, e.id`

// RawEvents returns the project's raw rows of day received before before,
// in (ts, id) order, as the event files hold an event (raw-first D19, PR
// 5c). A row stored without received_at counts as received before, and
// its ReceivedAt is zero. The three fields SQLite never stored are what it
// can say of them: InstallID is the actor id where the install was the
// actor (an $install_id a $user_id outranked was not kept), and UserName
// and GroupName are the latest names identities holds for UserID and
// GroupID, not the ones sent with the row.
//
// The day is read whole before it returns: the store has one connection,
// and a cursor held while the caller writes files would stall ingest.
func (d *DB) RawEvents(ctx context.Context, projectID int64, day civil.Date, before time.Time) ([]store.Event, error) {
	rows, err := d.db.QueryContext(ctx, rawEventsSQL, projectID, day.String(), before.UTC().Format(tsFormat))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Event
	for rows.Next() {
		e := store.Event{ProjectID: projectID}
		var family, ts, received, attrs string
		var consent sql.NullInt64
		var value sql.NullFloat64
		if err := rows.Scan(&e.ID, &family, &e.EventName, &ts, &received, &e.Kind,
			&e.ActorID, &e.ActorKind, &e.UserID, &e.GroupID, &e.SessionID,
			&e.Host, &e.Path, &e.ReferrerSource, &e.UTMSource, &e.UTMMedium, &e.UTMCampaign,
			&e.Platform, &e.OS, &e.OSVersion, &e.OSName, &e.Browser, &e.BrowserVersion, &e.BrowserLocale,
			&e.AppVersion, &e.AppLocale, &e.Device, &e.DeviceModel, &e.DisplayWidth, &e.DisplayHeight,
			&e.Country, &consent, &attrs, &value, &e.Measure, &e.SampleRate,
			&e.UserName, &e.GroupName); err != nil {
			return nil, err
		}
		e.Family = store.Family(family)
		if e.TS, err = time.Parse(time.RFC3339, ts); err != nil {
			return nil, fmt.Errorf("event %s ts: %w", e.ID, err)
		}
		if received != "" {
			if e.ReceivedAt, err = time.Parse(time.RFC3339, received); err != nil {
				return nil, fmt.Errorf("event %s received_at: %w", e.ID, err)
			}
		}
		if consent.Valid {
			e.Consent = store.ConsentNone
			if consent.Int64 == 1 {
				e.Consent = store.ConsentGiven
			}
		}
		if value.Valid {
			v := value.Float64
			e.Value = &v
		}
		if e.Attributes, err = rawAttributes(attrs); err != nil {
			return nil, fmt.Errorf("event %s attributes: %w", e.ID, err)
		}
		if e.ActorKind == store.ActorInstall {
			e.InstallID = e.ActorID
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// rawAttributes reads an attributes blob: a string value as is, any other
// JSON value (a number or a boolean an older row may hold) as its text,
// null dropped. An empty object is nil, as an event built without
// attributes has them.
func rawAttributes(blob string) (map[string]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return nil, err
	}
	var out map[string]string
	for k, v := range raw {
		if bytes.Equal(v, []byte("null")) {
			continue
		}
		s := string(v)
		if len(v) > 0 && v[0] == '"' {
			if err := json.Unmarshal(v, &s); err != nil {
				return nil, err
			}
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = s
	}
	return out, nil
}
```

In `internal/store/store.go`, add to the `Store` interface right after `MeasureDaysBefore`:

```go
	// RawEvents returns a project's raw rows of one day received before a
	// time, as the event files hold an event (sqlite.DB.RawEvents).
	RawEvents(ctx context.Context, projectID int64, day civil.Date, before time.Time) ([]Event, error)
```

- [ ] **Step 4: Run the tests and the SQL check**

Run: `go test ./internal/store/sqlite -run 'TestRawEvents' -count=1 && go test ./internal/sqlcheck -count=1 && go build ./...`
Expected: PASS. `internal/sqlcheck` prepares `rawEventsSQL` and checks the 38 scanned columns. A type that implements `store.Store` without embedding it fails `go build`; give it the method.

If `reflect.DeepEqual` fails on `Attributes` (nil against an empty map) or on a `time.Time` location, fix the expectation, not the method: `line()` writes the same line either way, and the test should pin what `RawEvents` returns.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/sqlite/raw_events.go internal/store/sqlite/raw_events_test.go
git commit -m "feat(store): read a day's raw rows as the event files hold an event"
```

---

### Task 2: a closed log written outside ingest

**Files:**
- Modify: `internal/archive/log.go` (`Rotate`: its free-name loop becomes `nextClosed`; add `WriteClosed`)
- Modify: `internal/archive/archive.go` (package doc: the `<day>.import` name)
- Test: `internal/archive/log_test.go`

**Interfaces:**
- Consumes: `line(store.Event)`, `syncFile` (maintain.go), `Day`.
- Produces: `func (l *Log) WriteClosed(project int64, day string, evs []store.Event) (string, error)`. It writes `evs` as `<dir>/<project>/<day>.<n>.closed`, with n the next free number. It returns the path, or `""` for no events. Task 3 calls it.

- [ ] **Step 1: Write the failing tests**

Add to `internal/archive/log_test.go`, after the `Rotate` tests:

```go
// TestWriteClosedLeavesAClosedLog: WriteClosed writes the events as the
// next free <day>.<n>.closed, the lines WriteEvents writes for them, and
// Rotate then takes the number after it. Nothing is left under another
// name, and the recent ids are not touched: ingest still logs the same id.
func TestWriteClosedLeavesAClosedLog(t *testing.T) {
	l := openLog(t)
	ctx := context.Background()
	if err := l.WriteEvents(ctx, []store.Event{ev("a", 1, "2026-10-11T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Rotate(1, "2026-10-11"); err != nil {
		t.Fatal(err)
	}
	evs := []store.Event{ev("b", 1, "2026-10-11T09:00:00Z"), ev("c", 1, "2026-10-11T11:00:00Z")}
	path, err := l.WriteClosed(1, "2026-10-11", evs)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(l.Dir(), "1")
	if want := filepath.Join(dir, "2026-10-11.2.closed"); path != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
	other := openLog(t)
	if err := other.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if got, want := lines(t, path), lines(t, filepath.Join(other.Dir(), "1", "2026-10-11.log")); !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %v, want WriteEvents' %v", got, want)
	}
	if err := l.WriteEvents(ctx, []store.Event{ev("b", 1, "2026-10-11T09:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if n := len(lines(t, filepath.Join(dir, "2026-10-11.log"))); n != 1 {
		t.Fatalf("ingest's log holds %d lines, want 1: WriteClosed must not mark ids as seen", n)
	}
	if next, err := l.Rotate(1, "2026-10-11"); err != nil || next != filepath.Join(dir, "2026-10-11.3.closed") {
		t.Fatalf("Rotate after WriteClosed = %s, %v; want .3.closed", next, err)
	}
	names, _ := os.ReadDir(dir)
	var got []string
	for _, n := range names {
		got = append(got, n.Name())
	}
	if want := []string{"2026-10-11.1.closed", "2026-10-11.2.closed", "2026-10-11.3.closed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

// TestWriteClosedRefusesAnotherDayOrProject: an event the day's dump would
// file under the wrong day or project fails the call, and nothing is
// written. No events write nothing.
func TestWriteClosedRefusesAnotherDayOrProject(t *testing.T) {
	l := openLog(t)
	for _, e := range []store.Event{ev("a", 1, "2026-10-12T00:00:00Z"), ev("a", 2, "2026-10-11T10:00:00Z")} {
		if _, err := l.WriteClosed(1, "2026-10-11", []store.Event{ev("ok", 1, "2026-10-11T10:00:00Z"), e}); err == nil {
			t.Fatalf("WriteClosed accepted %s of project %d on %s", e.ID, e.ProjectID, Day(e.TS))
		}
	}
	if path, err := l.WriteClosed(1, "2026-10-11", nil); err != nil || path != "" {
		t.Fatalf("WriteClosed(nil) = %q, %v; want nothing", path, err)
	}
	if _, err := os.Stat(filepath.Join(l.Dir(), "1")); !os.IsNotExist(err) {
		t.Fatalf("project directory: %v; want none written", err)
	}
}
```

If `log_test.go` lacks the imports `reflect` or `os`, add them. `ev`, `openLog` and `lines` are the file's own helpers. If `ev` sets no `ProjectID` argument the way this test assumes, read its signature (`ev(id string, project int64, at string)`) and adapt the calls.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/archive -run 'TestWriteClosed' -count=1`
Expected: FAIL to compile: `l.WriteClosed undefined`.

- [ ] **Step 3: Write `nextClosed` and `WriteClosed`**

In `internal/archive/log.go`, replace `Rotate`'s `for n := 1; ; n++ { … }` loop with a call to a new helper, and add `WriteClosed` after `Rotate`:

```go
	dst, err := nextClosed(dir, day)
	if err != nil {
		return "", err
	}
	if err := os.Rename(src, dst); err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	return dst, nil
}

// nextClosed is dir's first free <day>.<n>.closed, n from 1. The caller
// holds l.mu, so no other rotation or WriteClosed takes the name between
// the look and the rename.
func nextClosed(dir, day string) (string, error) {
	for n := 1; ; n++ {
		dst := filepath.Join(dir, day+"."+strconv.Itoa(n)+".closed")
		_, err := os.Stat(dst)
		if err == nil {
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("archive: %w", err)
		}
		return dst, nil
	}
}

// WriteClosed writes evs, every one the project's and of day, as a closed
// log of that day, named as Rotate names one, for the next pass to claim
// and dump: the way rows that never went through ingest get into the files
// (raw-first D19, the migration). The lines are line's. They go to
// <day>.import first, which no reader or pass looks at, are fsynced, and
// the file is renamed under the log's lock: a reader or a pass sees the
// whole closed log or none. The recent ids are not touched: these are not
// ingest's. No events write nothing and return "".
func (l *Log) WriteClosed(project int64, day string, evs []store.Event) (string, error) {
	if len(evs) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	for _, e := range evs {
		if e.ProjectID != project || Day(e.TS) != day {
			return "", fmt.Errorf("archive: event %s is project %d's %s, not project %d's %s", e.ID, e.ProjectID, Day(e.TS), project, day)
		}
		b, err := line(e)
		if err != nil {
			return "", fmt.Errorf("archive: event %s: %w", e.ID, err)
		}
		buf.Write(b)
	}
	dir := filepath.Join(l.dir, strconv.FormatInt(project, 10))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	tmp := filepath.Join(dir, day+".import")
	if err := os.WriteFile(tmp, buf.Bytes(), 0o640); err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	if err := syncFile(tmp); err != nil {
		return "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	dst, err := nextClosed(dir, day)
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	return dst, syncFile(dir)
}
```

In `internal/archive/archive.go`'s package doc, add under the `<YYYY-MM-DD>.<n>.closed` line:

```go
//	<YYYY-MM-DD>.import       a closed log being written (Log.WriteClosed)
```

A crash between the write and the rename leaves a `<day>.import`. Nothing reads it, the next `WriteClosed` for the day overwrites it, and `removeMonth` deletes it with its month (its name starts with the month).

- [ ] **Step 4: Run the archive tests**

Run: `go test ./internal/archive -count=1`
Expected: PASS, the `Rotate` tests included.

- [ ] **Step 5: Commit**

```bash
git add internal/archive/log.go internal/archive/archive.go internal/archive/log_test.go
git commit -m "feat(archive): write a closed log of a day from events that never went through ingest"
```

---

### Task 3: import a day: only what the files lack

**Files:**
- Create: `internal/archive/import.go`
- Modify: `internal/archive/maintain.go` (`Archive.eng` becomes `Engine`; `Execer` goes)
- Test: `internal/archive/import_test.go`

**Interfaces:**
- Consumes: `Log.WriteClosed` (Task 2); `projectOf`, `list`, `sqlList`, `quote` (archive); `Engine` (reader.go: `Exec` and `Query`).
- Produces: `func (a *Archive) ImportDay(ctx context.Context, project int64, day string, evs []store.Event) (int, error)`. It returns how many events it wrote. `archive.New` now takes an `Engine` (`*engine.Engine` satisfies it). Task 4 declares `ImportDay` in `jobs.Archive`.

- [ ] **Step 1: Write the failing tests**

Create `internal/archive/import_test.go` (the fixture is `maintain_test.go`'s):

```go
package archive

import (
	"context"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func (f fixture) importDay(t *testing.T, day string, evs ...store.Event) int {
	t.Helper()
	n, err := f.a.ImportDay(context.Background(), 1, day, evs)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestImportDayWritesWhatTheFilesLack: of a day's events, those the
// project's files and logs hold are skipped, the others written as a
// closed log; a project with no directory gets one.
func TestImportDayWritesWhatTheFilesLack(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "a", "2026-10-11T10:00:00Z"))
	if n := f.importDay(t, "2026-10-11", evOf(1, "a", "2026-10-11T10:00:00Z"), evOf(1, "b", "2026-10-11T11:00:00Z")); n != 1 {
		t.Fatalf("wrote %d, want 1 (b)", n)
	}
	if got := f.files(t); !eq(got, []string{"2026-10-11.1.closed", "2026-10-11.log"}) {
		t.Fatalf("files = %v", got)
	}
	if got := lines(t, f.project1("2026-10-11.1.closed")); len(got) != 1 || got[0]["id"] != "b" {
		t.Fatalf("closed log = %v, want b alone", got)
	}
	n, err := f.a.ImportDay(context.Background(), 7, "2026-10-11", []store.Event{evOf(7, "c", "2026-10-11T09:00:00Z")})
	if err != nil || n != 1 {
		t.Fatalf("a project with no directory: %d, %v; want 1 written", n, err)
	}
}

// TestImportDaySkipsWhatTheFilesHold: an id in the day file, a segment, a
// closed log or the open log is held; one only another day holds is not.
func TestImportDaySkipsWhatTheFilesHold(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "day", "2026-10-11T10:00:00Z"), evOf(1, "other", "2026-10-12T10:00:00Z"))
	f.maintain(t, "2026-10-12") // 2026-10-11.parquet
	f.write(t, evOf(1, "seg", "2026-10-11T11:00:00Z"))
	f.maintain(t, "2026-10-12") // 2026-10-11.1.parquet
	f.write(t, evOf(1, "closed", "2026-10-11T12:00:00Z"))
	if _, err := f.log.Rotate(1, "2026-10-11"); err != nil {
		t.Fatal(err)
	}
	f.write(t, evOf(1, "open", "2026-10-11T13:00:00Z"))
	n := f.importDay(t, "2026-10-11",
		evOf(1, "day", "2026-10-11T10:00:00Z"), evOf(1, "seg", "2026-10-11T11:00:00Z"),
		evOf(1, "closed", "2026-10-11T12:00:00Z"), evOf(1, "open", "2026-10-11T13:00:00Z"),
		evOf(1, "new", "2026-10-11T14:00:00Z"), evOf(1, "other", "2026-10-11T15:00:00Z"))
	if n != 2 {
		t.Fatalf("wrote %d, want 2 (new, and other: held for another day only)", n)
	}
}

// TestImportDayTwiceWritesOnce: the closed log an import wrote holds its
// ids, so the same import again writes nothing.
func TestImportDayTwiceWritesOnce(t *testing.T) {
	f := newFixture(t)
	evs := []store.Event{evOf(1, "a", "2026-10-11T10:00:00Z"), evOf(1, "b", "2026-10-11T11:00:00Z")}
	if n := f.importDay(t, "2026-10-11", evs...); n != 2 {
		t.Fatalf("first import wrote %d, want 2", n)
	}
	if n := f.importDay(t, "2026-10-11", evs...); n != 0 {
		t.Fatalf("second import wrote %d, want 0", n)
	}
	f.maintain(t, "2026-10-12")
	if got := f.files(t); !eq(got, []string{"2026-10-11.parquet"}) {
		t.Fatalf("files after the pass = %v", got)
	}
	if n, ids := f.rows(t, "2026-10-11.parquet"); n != 2 || ids != 2 {
		t.Fatalf("day file: %d rows, %d ids; want 2 and 2", n, ids)
	}
}

// TestImportedDayOfAMergedMonthIsMergedIn: a day of a month already merged
// is checked against the month file, and what it lacks goes into the month
// file at the next pass: dumped as the day file, merged again, the day
// file gone.
func TestImportedDayOfAMergedMonthIsMergedIn(t *testing.T) {
	f := newFixture(t)
	f.write(t, evOf(1, "m1", "2026-09-29T10:00:00Z"))
	f.maintain(t, "2026-10-05")
	if got := f.files(t); !eq(got, []string{"2026-09.parquet"}) {
		t.Fatalf("files before = %v, want September merged", got)
	}
	if n := f.importDay(t, "2026-09-29", evOf(1, "m1", "2026-09-29T10:00:00Z"), evOf(1, "m2", "2026-09-29T11:00:00Z")); n != 1 {
		t.Fatalf("wrote %d, want 1 (m2)", n)
	}
	f.maintain(t, "2026-10-05")
	if got := f.files(t); !eq(got, []string{"2026-09.parquet"}) {
		t.Fatalf("files after = %v, want the month file alone", got)
	}
	if n, ids := f.rows(t, "2026-09.parquet"); n != 2 || ids != 2 {
		t.Fatalf("month file: %d rows, %d ids; want 2 and 2", n, ids)
	}
}
```

`lines` is `log_test.go`'s helper; `files`, `rows`, `project1`, `write` and `maintain` are the fixture's.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/archive -run 'TestImport' -count=1`
Expected: FAIL to compile: `f.a.ImportDay undefined`.

- [ ] **Step 3: Write `ImportDay` and switch the archive's engine type**

In `internal/archive/maintain.go`, delete the `Execer` interface. Make `Archive.eng` and `New`'s parameter the `Engine` interface from `reader.go`. Keep `Execer`'s point in `Archive`'s doc:

```go
// Archive maintains the files the Log fills, derives the sessions,
// identities and aliases from them, and imports rows that never went
// through ingest (import.go). It is the one caller allowed to write
// through the engine's Exec.
type Archive struct {
	log    *Log
	eng    Engine
	logger *slog.Logger
}

func New(log *Log, eng Engine, logger *slog.Logger) *Archive {
	return &Archive{log: log, eng: eng, logger: logger}
}
```

Create `internal/archive/import.go`:

```go
package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ImportDay writes the events of evs whose id the project's files and logs
// do not hold for day as a closed log of that day (Log.WriteClosed), and
// returns how many it wrote: the migration's step for one project and day
// (raw-first D19). The next pass claims and dumps the log like any other.
// If the day's month is merged, the month file is merged again with it.
// The same import again writes nothing, because the closed log holds its
// ids. evs must all be the project's and of day.
func (a *Archive) ImportDay(ctx context.Context, project int64, day string, evs []store.Event) (int, error) {
	if len(evs) == 0 {
		return 0, nil
	}
	held, err := a.heldIDs(ctx, project, day)
	if err != nil {
		return 0, err
	}
	var missing []store.Event
	for _, e := range evs {
		if !held[e.ID] {
			missing = append(missing, e)
		}
	}
	if _, err := a.log.WriteClosed(project, day, missing); err != nil {
		return 0, err
	}
	return len(missing), nil
}

// heldIDs returns the ids the project holds for day, as ListFiles lists the
// files: the day file and its segments (or the month file, filtered on
// day, once the month is merged) and the day's logs, open, closed or
// claimed. A day with none of them costs no query. An open log's torn last
// line is skipped.
func (a *Archive) heldIDs(ctx context.Context, project int64, day string) (map[string]bool, error) {
	held := map[string]bool{}
	dir := filepath.Join(a.log.Dir(), strconv.FormatInt(project, 10))
	names, err := list(dir)
	if errors.Is(err, os.ErrNotExist) {
		return held, nil
	}
	if err != nil {
		return nil, err
	}
	pf, err := projectOf(dir, names)
	if err != nil {
		return nil, err
	}
	var parquet, logs []string
	for _, p := range pf.Parquet {
		if b := filepath.Base(p); strings.HasPrefix(b, day+".") || b == day[:7]+".parquet" {
			parquet = append(parquet, p)
		}
	}
	for _, p := range pf.Logs {
		if strings.HasPrefix(filepath.Base(p), day+".") {
			logs = append(logs, p)
		}
	}
	var parts []string
	if len(parquet) > 0 {
		parts = append(parts, "SELECT id FROM read_parquet("+sqlList(parquet)+", union_by_name=true) WHERE day = DATE "+quote(day))
	}
	if len(logs) > 0 {
		parts = append(parts, "SELECT json_extract_string(json, '/id') AS id FROM read_json_objects("+sqlList(logs)+
			", format='newline_delimited', ignore_errors=true) WHERE json IS NOT NULL")
	}
	if len(parts) == 0 {
		return held, nil
	}
	res, err := a.eng.Query(ctx, strings.Join(parts, " UNION ALL "), 0)
	if err != nil {
		return nil, fmt.Errorf("archive: ids of project %d's %s: %w", project, day, err)
	}
	for _, row := range res.Rows {
		if id, ok := row[0].(string); ok {
			held[id] = true
		}
	}
	return held, nil
}
```

- [ ] **Step 4: Run the archive and app tests**

Run: `go test ./internal/archive ./internal/app -count=1`
Expected: PASS. `app.Serve` passes `*engine.Engine` to `archive.New`, which satisfies `Engine`. If a test hands `New` a type with `Exec` alone, give it a `Query` that returns an error.

- [ ] **Step 5: Commit**

```bash
git add internal/archive/import.go internal/archive/import_test.go internal/archive/maintain.go
git commit -m "feat(archive): import a day's events the files do not hold"
```

---

### Task 4: the migration in the daily pass, and its documentation

**Files:**
- Modify: `internal/jobs/jobs.go`:
  - `Store`: add `RawEvents`, `GetMeta`, `SetMeta`;
  - `Archive`: add `ImportDay`;
  - `Runner.started`;
  - `migrateEvents` and `migrationDays`, and the call in `RunDailyPass`.
- Test: `internal/jobs/jobs_test.go` (`fakeArchive`; the new tests after `TestDailyPassMaintainsTheArchive`)
- Docs:
  - `deploy/UPGRADES.md`: a new last section;
  - `docs/twillingate.md`: "Writing SQL against the event files", its column table, and "Identity";
  - `docs/deployment.md`: the event files paragraph (the one starting "The ingest process also keeps every event as files").

**Interfaces:**
- Consumes: `store.Store.RawEvents` (Task 1) and `archive.Archive.ImportDay` (Task 3).
- Produces: the `meta` key `events_files_migrated`, an RFC 3339 UTC time. Task 5 reads it.

- [ ] **Step 1: Write the failing tests**

In `internal/jobs/jobs_test.go`, give `fakeArchive` an import record and an injected failure:

```go
type fakeArchive struct {
	calls   []string
	removed []int64
	dirs    []int64 // the project directories Projects reports
	imports []string
	// importErr fails ImportDay once for a day.
	importErr map[string]error
}

func (f *fakeArchive) ImportDay(ctx context.Context, project int64, day string, evs []store.Event) (int, error) {
	if err := f.importErr[day]; err != nil {
		delete(f.importErr, day)
		return 0, err
	}
	ids := make([]string, len(evs))
	for i, e := range evs {
		ids[i] = e.ID
	}
	f.imports = append(f.imports, fmt.Sprintf("%d %s %v", project, day, ids))
	return len(evs), nil
}
```

Add after `TestDailyPassMaintainsTheArchive`:

```go
// rawRow is a raw row of project 1 of any family, received when given.
func rawRow(id string, family store.Family, at, received string) store.Event {
	e := store.Event{ID: id, ProjectID: 1, Family: family, EventName: "x", TS: mustTime(at), ReceivedAt: mustTime(received),
		ActorID: "h", ActorKind: store.ActorConnection, Platform: "web"}
	if family == store.FamilyMeasures {
		v := 1.0
		e.Measure, e.Value = store.MeasureTime, &v
	}
	return e
}

// TestDailyPassMigratesRawRowsOnce: the pass hands the archive every day
// of raw rows, every family, received before the process started (the
// runner's clock at New), and records the migration in meta; the next pass
// hands it nothing (raw-first D19).
func TestDailyPassMigratesRawRowsOnce(t *testing.T) {
	st, _, r := setup(t, jobsVars, jobsProjectSpecs) // now: 2026-08-22T04:00:00Z
	ctx := context.Background()
	if err := st.WriteEvents(ctx, []store.Event{
		rawRow("v1", store.FamilyViews, "2026-08-20T10:00:00Z", "2026-08-20T10:00:00Z"),
		rawRow("p1", store.FamilyProduct, "2026-08-21T09:00:00Z", "2026-08-21T09:00:00Z"),
		rawRow("m1", store.FamilyMeasures, "2026-08-21T09:30:00Z", "2026-08-21T09:30:00Z"),
		// Received by this process: the pipeline wrote it to the log too.
		rawRow("p2", store.FamilyProduct, "2026-08-22T03:59:00Z", "2026-08-22T04:00:01Z"),
	}); err != nil {
		t.Fatal(err)
	}
	a := &fakeArchive{}
	r.WithArchive(a)
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1 2026-08-20 [v1]", "1 2026-08-21 [p1 m1]"}; !slices.Equal(a.imports, want) {
		t.Fatalf("imports = %v, want %v", a.imports, want)
	}
	if got, _ := st.GetMeta(ctx, "events_files_migrated"); got != "2026-08-22T04:00:00Z" {
		t.Fatalf("meta = %q, want the time it finished", got)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(a.imports) != 2 {
		t.Fatalf("imports after a second pass = %v, want none added", a.imports)
	}
}

// TestMigrationSkipsDaysRetentionDeletes: a day RETENTION_DAYS would
// delete is not handed over.
func TestMigrationSkipsDaysRetentionDeletes(t *testing.T) {
	vars := map[string]string{"RETENTION_RAW_DAYS": "30", "RETENTION_AGGREGATE_DAYS": "365", "RETENTION_DAYS": "5"}
	st, _, r := setup(t, vars, jobsProjectSpecs)
	ctx := context.Background()
	if err := st.WriteEvents(ctx, []store.Event{
		rawRow("old", store.FamilyProduct, "2026-08-17T10:00:00Z", "2026-08-17T10:00:00Z"), // today - 5
		rawRow("kept", store.FamilyProduct, "2026-08-18T10:00:00Z", "2026-08-18T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	a := &fakeArchive{}
	r.WithArchive(a)
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1 2026-08-18 [kept]"}; !slices.Equal(a.imports, want) {
		t.Fatalf("imports = %v, want %v", a.imports, want)
	}
}

// TestMigrationRetriesAfterAFailedDay: a failed day leaves meta unset and
// the other days go on; the next pass runs the migration again and
// records it.
func TestMigrationRetriesAfterAFailedDay(t *testing.T) {
	st, _, r := setup(t, jobsVars, jobsProjectSpecs)
	ctx := context.Background()
	if err := st.WriteEvents(ctx, []store.Event{
		rawRow("a", store.FamilyProduct, "2026-08-20T10:00:00Z", "2026-08-20T10:00:00Z"),
		rawRow("b", store.FamilyProduct, "2026-08-21T10:00:00Z", "2026-08-21T10:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	a := &fakeArchive{importErr: map[string]error{"2026-08-20": errors.New("disk full")}}
	r.WithArchive(a)
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetMeta(ctx, "events_files_migrated"); got != "" {
		t.Fatalf("meta after a failed day = %q, want unset", got)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1 2026-08-21 [b]", "1 2026-08-20 [a]", "1 2026-08-21 [b]"}; !slices.Equal(a.imports, want) {
		t.Fatalf("imports = %v, want %v", a.imports, want)
	}
	if got, _ := st.GetMeta(ctx, "events_files_migrated"); got == "" {
		t.Fatal("meta unset after a pass where every day succeeded")
	}
}

// TestMigrationNeedsTheArchive: a process without the archive (console
// only) migrates nothing and records nothing.
func TestMigrationNeedsTheArchive(t *testing.T) {
	st, _, r := setup(t, jobsVars, jobsProjectSpecs)
	ctx := context.Background()
	if err := st.WriteEvents(ctx, []store.Event{rawRow("a", store.FamilyProduct, "2026-08-20T10:00:00Z", "2026-08-20T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetMeta(ctx, "events_files_migrated"); got != "" {
		t.Fatalf("meta = %q, want unset", got)
	}
}
```

Add `errors` and `slices` to the test file's imports if missing.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/jobs -run 'Migrat' -count=1`
Expected: FAIL. `fakeArchive` does not satisfy `Archive` until it declares `ImportDay`, or the imports stay empty.

- [ ] **Step 3: Write the migration**

In `internal/jobs/jobs.go`, add to `Store`:

```go
	// RawEvents returns a project's raw rows of one day received before a
	// time, as the event files hold an event (the migration).
	RawEvents(ctx context.Context, projectID int64, day civil.Date, before time.Time) ([]store.Event, error)
	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
```

Add to `Archive`:

```go
	// ImportDay writes the events the files do not hold for the project's
	// day as a closed log, for Maintain to dump; it returns how many.
	ImportDay(ctx context.Context, project int64, day string, evs []store.Event) (int, error)
```

Add a field to `Runner`, after `now`:

```go
	// started is the runner's clock at New, before any listener binds:
	// every row received since went through the pipeline to the log.
	started time.Time
```

In `New`, set `started: now(),` beside `now: now,`.

In `RunDailyPass`, right after `r.removeOrphanedArchives(ctx)`:

```go
	// Before the rollups and their prune: the day leaving the raw window
	// at this pass is copied before it goes.
	r.migrateEvents(ctx, today)
```

Add after `removeOrphanedArchives`:

```go
// eventsMigratedKey is the meta row the migration writes when done: the
// time it finished (RFC 3339, UTC). Deleting it runs the migration again
// at the next pass.
const eventsMigratedKey = "events_files_migrated"

// migrateEvents copies into the event files, once per install, every raw
// row SQLite received before this process started that the files do not
// hold for its day (raw-first D19, PR 5c): the history from before the
// upgrade that began writing raw/, and any batch the log missed since.
// Rows received since started went through the pipeline to both stores,
// some maybe still on their way to the log, so they are left out. Days
// RETENTION_DAYS would delete are skipped. Each day is handed to the
// archive as a closed log, which this pass's Maintain dumps (and merges
// into its month file if the month is merged). A failed day is logged and
// the rest go on. meta records the migration only when every day of every
// project succeeded; until then each pass runs it again, and the archive
// writes nothing it already holds.
func (r *Runner) migrateEvents(ctx context.Context, today civil.Date) {
	if r.archive == nil {
		return
	}
	if done, err := r.store.GetMeta(ctx, eventsMigratedKey); err != nil {
		r.logger.Error("archive: migration: read meta", "error", err)
		return
	} else if done != "" {
		return
	}
	ids, err := r.store.ProjectIDs(ctx)
	if err != nil {
		r.logger.Error("archive: migration: list projects", "error", err)
		return
	}
	r.logger.Info("archive: migrating raw rows into the event files", "projects", len(ids), "received_before", r.started.UTC().Format(time.RFC3339))
	keep := today.AddDays(-r.cfg.Retention.Days)
	failed := false
	var days, rows, written int
	for _, id := range ids {
		pdays, err := r.migrationDays(ctx, id, today.AddDays(2))
		if err != nil {
			r.logger.Error("archive: migration: list days", "project", id, "error", err)
			failed = true
			continue
		}
		var pRows, pWritten int
		for _, day := range pdays {
			if ctx.Err() != nil {
				return
			}
			if !keep.Before(day) {
				continue
			}
			evs, err := r.store.RawEvents(ctx, id, day, r.started)
			if err == nil && len(evs) == 0 {
				continue
			}
			var n int
			if err == nil {
				n, err = r.archive.ImportDay(ctx, id, day.String(), evs)
			}
			if err != nil {
				r.logger.Error("archive: migration: day", "project", id, "day", day.String(), "error", err)
				failed = true
				continue
			}
			days++
			pRows += len(evs)
			pWritten += n
		}
		rows += pRows
		written += pWritten
		r.logger.Info("archive: migration: project", "project", id, "rows", pRows, "written", pWritten)
	}
	if failed {
		r.logger.Warn("archive: migration incomplete; the next pass runs it again", "days", days, "written", written)
		return
	}
	if err := r.store.SetMeta(ctx, eventsMigratedKey, r.now().UTC().Format(time.RFC3339)); err != nil {
		r.logger.Error("archive: migration: write meta", "error", err)
		return
	}
	r.logger.Info("archive: raw rows migrated", "days", days, "rows", rows, "written", written)
}

// migrationDays lists, in order, the days before before on which the
// project has raw rows of any family.
func (r *Runner) migrationDays(ctx context.Context, projectID int64, before civil.Date) ([]civil.Date, error) {
	days, err := r.allRawDays(ctx, projectID, before)
	if err != nil {
		return nil, err
	}
	measures, err := r.store.MeasureDaysBefore(ctx, projectID, before)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, d := range days {
		seen[d.String()] = true
	}
	for _, d := range measures {
		if !seen[d.String()] {
			seen[d.String()] = true
			days = append(days, d)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].String() < days[j].String() })
	return days, nil
}
```

`today.AddDays(2)` reaches past today: ingest accepts a `ts` up to `wire.FutureSkew` ahead of its clock, so a row can sit on tomorrow.

- [ ] **Step 4: Run the jobs and app tests**

Run: `go test ./internal/jobs ./internal/app -count=1`
Expected: PASS. `TestDailyPassMaintainsTheArchive` still sees one `Maintain` call. A test that runs a pass with a `fakeArchive` and raw rows now sees imports as well, and its expectations should not change.

- [ ] **Step 5: Document it, in the same commit**

**`deploy/UPGRADES.md`.** Append after "Upgrading to the derived tables (no migration)":

```markdown
### Upgrading to the migrated event files (no migration)

No schema migration; one step runs by itself. At its first start the
ingest process copies into `raw/` every raw row the database holds that
the files do not:

- the events from before the upgrade that began writing `raw/`, back to
  the oldest day the raw window (`RETENTION_RAW_DAYS`) still holds;
- any batch the log missed since.

It runs in the pass at start, in the background, so ingest and the
console answer meanwhile. It takes about 30 s per million rows, plus
0.2 s for each copied day the same pass turns into a day file. The log
says `archive: raw rows migrated` when it is done. A failure is logged, and
the next pass (the next start, or 03:00 UTC) runs it again, skipping what
is already copied. It runs once: the `meta` row `events_files_migrated`
records when it finished.

- History older than the raw window exists in the database only as daily
  aggregates (`agg_*`). They cannot become events, so they do not migrate.
  Nothing changes today: dashboards still read the database. Once a later
  release moves the dashboards onto the files, they start at the oldest raw
  day the database holds now: `sqlite3 twillingate.db "SELECT min(day) FROM events"`.
- A copied row has an install id only where the install was the actor (an
  `$install_id` outranked by a `$user_id` was never stored). It has the
  latest name the database holds for its user and group, not the name sent
  with it.
- Check it per project: `query` with `source: "files"`,
  `SELECT day, count(*) FROM events WHERE project_id = 1 GROUP BY ALL ORDER BY day`,
  against `sqlite3 twillingate.db "SELECT day, count(*) FROM events WHERE project_id = 1 GROUP BY day"`.
  Both count today's rows up to their last flush.
- To copy again (after restoring an older `raw/`, say): stop the service,
  `sqlite3 twillingate.db "DELETE FROM meta WHERE key = 'events_files_migrated'"`,
  start it.

Rollback: an older binary reads the copied days like any other day file;
nothing to undo.
```

**`docs/twillingate.md`, "Writing SQL against the event files".** The first paragraph says the files hold events "received since the upgrade that began writing them". Replace that clause with:

```markdown
every project's, from the oldest raw day the database held when it was
copied into them (older history exists only as the `agg_*` tables),
today's included (each `BUFFER_FLUSH_INTERVAL`)
```

In the column table, extend two rows:

```markdown
| `install_id` | VARCHAR | the client's `$install_id`, also when a `$user_id` is the actor; on events copied from the database, only when the install was the actor |
| `user_name`, `group_name` | VARCHAR | the names sent beside `user_id` and `group_id` (see [Identity](#identity)); on events copied from the database, the latest name it held |
```

**`docs/twillingate.md`, "Identity".** The paragraph beginning "In the event files every event also keeps the `$install_id`, `$user_name`" ends by saying how long the names are kept. Add one sentence after it:

```markdown
Events from before the files existed were copied from the database, which
kept no names per event: they carry the latest name it held, and an
`$install_id` only where the install was the actor.
```

**`docs/deployment.md`.** In the paragraph starting "The ingest process also keeps every event as files", add after its first sentence:

```markdown
At its first start this release also copies the raw rows the database holds into them, once, in the pass at start (`deploy/UPGRADES.md`).
```

Run: `go test ./internal/api -run 'Docs' -count=1 && go test ./docs -count=1`
Expected: PASS. The sync tests bind none of these sentences, so check by reading that each one sits where the plan says.

- [ ] **Step 6: Commit**

```bash
git add internal/jobs/jobs.go internal/jobs/jobs_test.go deploy/UPGRADES.md docs/twillingate.md docs/deployment.md
git commit -m "feat(jobs): copy the raw rows SQLite holds into the event files once"
```

---

### Task 5: the files hold what SQLite holds, end to end

**Files:**
- Create: `internal/app/archive_migrate_test.go`
- Test: the same file; `TestMain` and `appEngineChild` come from `archive_parity_test.go`

**Interfaces:**
- Consumes everything above through real parts: `storetest.Open`, `archive.OpenLog`, `engine.New`, `archive.New`, `archive.NewReader`, `jobs.New(...).WithArchive(...)`, `pipeline.Sinks`, and `configtest.Load`.
- Produces: nothing new.

- [ ] **Step 1: Write the test**

Create `internal/app/archive_migrate_test.go`:

```go
package app

import (
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
	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/jobs"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/pipeline"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

// TestMigrationFillsTheFilesFromSQLite: an install whose database holds
// rows the files lack gets every one of them into the files at its first
// pass, once (raw-first D19, PR 5c Review Focus 1–3). The rows come from
// before the files began, from a day split by that start, and from a
// month merged without them. Afterwards, per day, the files count what
// SQLite counts with no id twice; September is one month file again; the
// copied rows carry the install id and the user's name; meta records the
// migration; and a second pass changes nothing.
func TestMigrationFillsTheFilesFromSQLite(t *testing.T) {
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
	merged, _ := civil.Parse("2026-10-05")
	if err := archive.New(log, eng, slog.Default()).Maintain(ctx, merged, 1, 400); err != nil {
		t.Fatal(err)
	}
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

	cfg := configtest.Load(t, map[string]string{"RETENTION_RAW_DAYS": "60", "RETENTION_AGGREGATE_DAYS": "365", "INGEST_LATE_DAYS": "1", "RETENTION_DAYS": "400"})
	now := func() time.Time { return at("2026-10-11T04:00:00Z") }
	runner := jobs.New(st, cfg, reg, identity.NewSalter(st, now), slog.Default(), now).
		WithArchive(archive.New(log, eng, slog.Default()))
	if err := runner.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}

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
	if got, _ := st.GetMeta(ctx, "events_files_migrated"); got != "2026-10-11T04:00:00Z" {
		t.Fatalf("meta = %q", got)
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
	if err := runner.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if got := eventsFiles(); !slices.Equal(got, wantFiles) {
		t.Fatalf("files after a second pass = %v, want %v", got, wantFiles)
	}
	if got := ask(perDay); got != fmt.Sprint(want) {
		t.Fatalf("files per day after a second pass = %s, SQLite %v", got, want)
	}
}
```

Why these files, in pass order:
- **The migration** runs first. It writes `2026-09-29.1.closed` (s1), `2026-10-01.1.closed` (o1; o2 is held by the open `2026-10-01.log`) and `2026-10-11.1.closed` (t1; t2 is held by today's log).
- **`Maintain` rotates** `2026-10-01.log` to `.2.closed`, then dumps each claimed log:
  - October 1's two closed logs become the day file and segment 1;
  - September 29's becomes a day file, and September merges again;
  - October 11's becomes `2026-10-11.parquet`, beside today's open log.

If the list differs, derive the expected list from that sequence before changing any code: a different order of claims is fine as long as the counts match.

- [ ] **Step 2: Run it**

Run: `go test ./internal/app -run 'TestMigrationFillsTheFilesFromSQLite' -count=1 -v`
Expected: PASS. Tasks 1–4 are in place, so this test needs no new code. If it fails, the failure is a bug in Tasks 1–4: fix it there, in a commit of its own (`fix(<scope>): …`), and rerun.

- [ ] **Step 3: Run everything**

Run: `make check`
Expected: PASS, the coverage gate included. If coverage dropped, the uncovered lines are likely `migrateEvents`' error branches. Cover the `RawEvents` error with a `countingStore`-style wrapper in `jobs_test.go` that fails `RawEvents` for one day, and assert that meta stays unset.

- [ ] **Step 4: Commit**

```bash
git add internal/app/archive_migrate_test.go
git commit -m "test(app): the migrated files hold every row SQLite holds"
```

---

## After the last task

- Push `feat/raw-migrate` (`git push -u origin feat/raw-migrate`) and open a draft PR with base `feat/raw-derived`.
  - Title: `feat: copy the raw rows SQLite holds into the event files once`.
  - Body: problems first, then the reasoning; it lists the Decisions above.
- In the roadmap's PR 5c entry, add `Plan: 2026-10-14-raw-migrate.md.` and note that it runs in the pass at start, not before the server starts. This is already done in the commit that adds this plan.
- **Measured on landing:** the migration's time on `~/.cache/twbench` (615 MB of bench data, 36M rows), loaded into SQLite with `RETENTION_RAW_DAYS` covering a month. The target is D19's 30 s per million rows.
