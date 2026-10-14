# Raw Rows Import Implementation Plan (PR 5c)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A one-time command, `twillingate events import`, copies every raw row SQLite holds that the event files lack into the files, so the files hold, per day and project, every row SQLite holds.

**Architecture:**
- **When it runs:** once, by hand, with the ingest service stopped, after the upgrade. The server never runs it on its own.
  - The ingest process now holds an exclusive lock on `raw/` while it runs. With the server up, the command refuses to run.
  - On success the command writes a `meta` row, `events_imported`. A second run says when the import happened and exits; `-force` runs it again.
- **Which rows:** every project's raw rows in SQLite, day by day. A row whose id the files already hold for its day is skipped. With the service stopped nothing else writes, so no cut-off is needed.
- **How they get in:** as a closed log, `<day>.<n>.closed`, the file `Log.Rotate` leaves. The next start's pass claims, dumps and merges it like any other. There is no new kind of file and no second writer of Parquet.
- **Fields SQLite never stored:** `install_id` is the actor id where the install was the actor. `user_name` and `group_name` are the latest names in `identities` (Decision 4).

**Tech Stack:** Go 1.25; modernc SQLite (`internal/store/sqlite`); DuckDB v1.5.6 through `internal/engine` (the child process); the PR 3–5a archive (`internal/archive`); the composition root (`internal/app`) and the CLI (`cmd/twillingate`).

**Spec:** `docs/superpowers/specs/2026-10-12-raw-first-design.md`:
- D19: only raw rows migrate;
- D2: the log, the dump and the merge;
- D5: retention by whole files;
- D17 and D18: the names and the install id on the row.

Roadmap: `docs/superpowers/plans/2026-10-12-raw-first-roadmap.md`, PR 5c. It is stacked on PR 5a (#182, `feat/raw-derived`), not on 5b: 5c touches nothing 5b changes (the SDK, `SessionAt`). Earlier plans for the conventions this one follows:
- `2026-10-12-raw-log.md`: the line, the claim, the dump, the merge;
- `2026-10-13-raw-derived.md`: the on-row fields and the parity tests in `internal/app`.

## Facts this plan rests on (read in the code, 2026-10-14)

- **`twillingate migrate` is the precedent** (`cmd/twillingate/migrate.go`). It loads the config, opens the store and calls an `app` function, which does the work. `cmd` is the only importer of `app`.
- **Nothing locks `raw/` today.** Two processes that both open the `Log` would each pick "the next free" closed-log name and could rename onto the same one. The new lock makes that impossible for the command and the server, and for two ingest processes too. The engine's slots are the model: `syscall.Flock` with `LOCK_EX|LOCK_NB` (`internal/engine/slots.go`). A dead process releases its lock.
- **The events key is `(family, project_id, day, id)`.** A filter `family IN ('views', 'product', 'measures') AND project_id = ? AND day = ?` seeks the key. Without `family`, it scans the table (spec, "Found along the way").
- **A month file hides its days.** `ListFiles` leaves out a day file once its month's file exists. `maintainProject` merges every final, non-open month on every pass: a month with a new day file beside its month file is merged again, the month file first (`mergeQuery`: first file wins). So a closed log of a merged month's day is in the month file at the end of the pass that dumps it.
- **`claim` names the dump's target in name order:** the day file if missing, else the next free segment. An imported closed log of a day with no file becomes the day file.
- **`removeMonth` deletes a month once its last day is `RETENTION_DAYS` old,** closed logs included (it matches the name prefix).
- **The server's pass at start runs `Maintain`** (`jobs.Runner.Run`, the catch-up pass). The closed logs the command leaves are dumped minutes after the next start; until then the reader reads them as logs.
- **`ViewDaysBefore`, `ProductDaysBefore` and `MeasureDaysBefore` exist** on `store.Store`, each `SELECT DISTINCT day … WHERE project_id=? AND day < ?` over one family.

## Global Constraints

- **SQLite's schema is unchanged.** No migration and no new column: the import only reads `events` and `identities`, and writes one `meta` row. SQLite keeps writing `events` until PR 6.
- **The line format stays `v: 1`.** Imported lines come from `line()`, the function ingest's lines come from.
- **Files are written only by the paths that write them today.** The import writes closed logs; Parquet comes only from `Archive.write` (dump, merge, derive).
- **`internal/archive` stays a leaf.** It imports only `engine`, `store` and `shared/civil`. `Exec` stays the archive's own; the archive's new reads go through `Query`.
- **The server never imports on its own.** No step is added to the daily pass or to `Serve`.
- **`cmd` only parses flags and wires;** the work is in `internal/app`, as for `migrate`.
- **The tables' columns are a contract** (PR 4): no column changes. What the columns hold on imported rows, the command, and the lock are documented in the same commit as the code (CLAUDE.md, Documentation).
- **Commits and checks.** Conventional Commits with CLAUDE.md's scopes. Run `make check` before every push.
- **Toolchain.** Go is at `/usr/local/go/bin`, not on `PATH`. Set `GOTMPDIR=$HOME/.cache/gotmp`, because `/tmp`'s quota fills. Every command below assumes:
  `export PATH=$PATH:/usr/local/go/bin GOTMPDIR=$HOME/.cache/gotmp`.

## Decisions this plan takes (review them)

1. **A one-time command, run with the service stopped** (decided with the user, 2026-10-14). D19 said "on the first boot"; this way nothing heavy runs at a start you did not ask for.
   - Stopped means nothing else writes `events` or `raw/` while it reads, so every row is copied and no `received_at` cut-off is needed.
   - The price is downtime while it runs: about 30 s per million rows. On a split install only the ingest process stops; the console never opens the log.
2. **The ingest process locks `raw/`.** `OpenLog` takes an exclusive `flock` on `raw/.lock` and holds it until `Close`, and the command opens the log the same way. While the server runs, the command fails with "stop the ingest service first". The lock also refuses a second ingest process on the same `raw/`.
3. **It writes closed logs, not Parquet.** D19: "the engine dumps them into day files like any other day; nothing new is written for it."
   - The claim, the dump with its types, the dedup and the merge stay single code paths.
   - The command does not dump. The next start's pass does, minutes after the start; until then the reader reads the closed logs, which is slower and gives the same answers.
4. **Imported rows get what SQLite can say about the three fields it never stored** (5a):
   - **`install_id`** is `actor_id` when `actor_kind` is `install`. An `$install_id` outranked by a `$user_id` was never stored, so it stays NULL: such a row adds no `aliases` pair.
   - **`user_name` and `group_name`** are the names `identities` holds for the row's `user_id` and `group_id`: the latest name, not the one sent with that row. Without them, 5d's `identities` would lose the name of every user not seen since the files began.

   The alternative is NULL for all three, as 5a's UPGRADES entry promised. It is one line in the store's SELECT, so it is easy to drop in review.
5. **Held ids are checked per project and day, in the engine.**
   - The engine reads the ids from the day's file and segments, its month file (filtered on `day`) and its logs (open, closed, claimed).
   - A day with no file and no log needs no query. That is every day before PR 3's first start.
   - A day's ids fit in memory: about 33,000 at the 1M-a-month cap.
6. **Days `RETENTION_DAYS` would delete are skipped:** `day <= today - RETENTION_DAYS`. This matters only where `RETENTION_RAW_DAYS` is larger than `RETENTION_DAYS`. A day in a month that is only partly expired is still copied; its month goes whole, later (D5).
7. **One `meta` row, `events_imported`, holds the time the import finished.** It is written only when every day of every project succeeded.
   - A failed day is printed, the rest go on, and the command exits 1. Running it again skips what is already copied (Decision 5).
   - With the row set, the command prints when the import ran and exits 0. `-force` runs it again, for example after restoring an older `raw/`.
   - 5d can warn at start when the row is missing.
8. **Archived projects are imported; purged ones have no rows.** The files keep an archived project until it is purged, as SQLite does.
9. **The command migrates the schema first,** as `migrate` does. It can run straight after installing the new binary, before the first start.

## Review Focus

1. **A month merged before the import, missing rows SQLite has.** For example, an install that ran PR 3 for over a month with a gap in its log, or that started PR 3 mid-month.
   - The imported day file sits hidden beside the month file until the next pass merges the month again.
   - After that pass, the month file holds every row once, and no day file is left.

   Pinned by Task 3 `TestImportedDayOfAMergedMonthIsMergedIn` and Task 5.
2. **A day split by PR 3's first start:** the rows before it are only in SQLite, the rows after it are in both.
   - Only the missing rows are written.
   - Per day the files count what SQLite counts, and no id appears twice.

   Pinned by Task 3 `TestImportDaySkipsWhatTheFilesHold` and Task 5.
3. **An import cut short and run again:** a killed command, or a failing day.
   - Nothing is written twice.
   - `meta` is written only once the whole import succeeds.

   Pinned by Task 3 `TestImportDayTwiceWritesOnce` and Task 4 `TestImportEventsRetriesAFailedDay`.
4. **The command run while the server is up:** it refuses before it touches anything, and says why. The server running beside a second ingest process on the same `raw/` refuses too. Pinned by Task 2 `TestOpenLogLocksTheDirectory` and Task 4 `TestImportEventsRefusesWhileTheServerRuns`.
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
- Produces: `func (d *DB) RawEvents(ctx context.Context, projectID int64, day civil.Date) ([]store.Event, error)`, also on `store.Store`. It returns the rows in `(ts, id)` order.
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
	got, err := d.RawEvents(ctx, 1, day)
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

// TestRawEventsReadsOlderRows: rows in shapes ingest no longer writes copy
// without failing the day: a ts with fractional seconds, no received_at
// (a zero ReceivedAt), and attribute values that are JSON numbers,
// booleans or null (a value is its JSON text, null is dropped).
func TestRawEventsReadsOlderRows(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if err := d.WriteEvents(ctx, []store.Event{
		{ID: "a", ProjectID: 1, Family: store.FamilyProduct, EventName: "x", TS: rawAt("2026-10-11T09:00:00Z"),
			ReceivedAt: rawAt("2026-10-11T09:00:00Z"), ActorID: "h", Platform: "web", SampleRate: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE events SET ts = '2026-10-11T09:00:00.250Z', received_at = '',
		attributes = '{"n":42,"b":true,"z":null,"s":"x"}' WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	day, _ := civil.Parse("2026-10-11")
	got, err := d.RawEvents(ctx, 1, day)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if want := rawAt("2026-10-11T09:00:00.25Z"); !got[0].TS.Equal(want) {
		t.Fatalf("TS = %v, want %v", got[0].TS, want)
	}
	if !got[0].ReceivedAt.IsZero() {
		t.Fatalf("ReceivedAt of a row without one = %v, want zero", got[0].ReceivedAt)
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

// rawEventsSQL reads one project's day of raw rows, with the names
// identities holds for each row's user and group. The family list makes
// it a seek on the events key.
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
WHERE e.family IN ('views', 'product', 'measures') AND e.project_id = ? AND e.day = ?
ORDER BY e.ts, e.id`

// RawEvents returns the project's raw rows of day, in (ts, id) order, as
// the event files hold an event (raw-first D19, PR 5c). A row stored
// without received_at has a zero ReceivedAt. The three fields SQLite never stored are what it
// can say of them: InstallID is the actor id where the install was the
// actor (an $install_id a $user_id outranked was not kept), and UserName
// and GroupName are the latest names identities holds for UserID and
// GroupID, not the ones sent with the row.
//
// The day is read whole before it returns, so no cursor stays open on the
// store's one connection while the caller writes files.
func (d *DB) RawEvents(ctx context.Context, projectID int64, day civil.Date) ([]store.Event, error) {
	rows, err := d.db.QueryContext(ctx, rawEventsSQL, projectID, day.String())
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
	// RawEvents returns a project's raw rows of one day, as the event
	// files hold an event (sqlite.DB.RawEvents).
	RawEvents(ctx context.Context, projectID int64, day civil.Date) ([]Event, error)
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

### Task 2: the log's lock, and a closed log written outside ingest

**Files:**
- Modify: `internal/archive/log.go`:
  - `OpenLog` takes an exclusive lock on `<dir>/.lock`, `Close` releases it, and `ErrLocked` is new;
  - `Rotate`: its free-name loop becomes `nextClosed`;
  - `WriteClosed` is new.
- Modify: `internal/archive/archive.go` (package doc: the `.lock` and `<day>.import` names)
- Test: `internal/archive/log_test.go`

**Interfaces:**
- Consumes: `line(store.Event)`, `syncFile` (maintain.go), `Day`.
- Produces: `archive.ErrLocked`. `OpenLog` returns an error wrapping it while another `Log` (in any process) holds the directory; Task 4 matches it with `errors.Is`.
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

Add the lock's test too:

```go
// TestOpenLogLocksTheDirectory: while one Log is open on a directory,
// opening another fails with ErrLocked (the server and the import command,
// or two ingest processes, never write raw/ at once); once it is closed,
// the next one opens.
func TestOpenLogLocksTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "raw")
	l, err := OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLog(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second OpenLog: %v, want ErrLocked", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := OpenLog(dir)
	if err != nil {
		t.Fatalf("OpenLog after Close: %v", err)
	}
	again.Close()
}
```

If `log_test.go` lacks the imports `reflect`, `os` or `errors`, add them. An existing test that opens a second `Log` on a directory whose first `Log` is still open (to stand for a restart) must now `Close` the first: a real restart's process exit releases the lock. `ev`, `openLog` and `lines` are the file's own helpers. If `ev` sets no `ProjectID` argument the way this test assumes, read its signature (`ev(id string, project int64, at string)`) and adapt the calls.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/archive -run 'TestWriteClosed|TestOpenLogLocks' -count=1`
Expected: FAIL to compile: `l.WriteClosed undefined`, `ErrLocked undefined`.

- [ ] **Step 3: Write the lock, `nextClosed` and `WriteClosed`**

In `internal/archive/log.go`, add the error and a `lock` field to `Log`, and take the lock in `OpenLog` before anything else touches the directory (before the torn-line cut, which another process's appends would race):

```go
// ErrLocked: another Log, in this process or another, holds the directory.
// One process at a time writes raw/: the ingest server, or the import
// command while the server is stopped.
var ErrLocked = errors.New("archive: the events directory is in use by another process")
```

```go
	// lock is <dir>/.lock, held with flock until Close.
	lock *os.File
```

```go
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s)", ErrLocked, dir)
		}
		return nil, fmt.Errorf("archive: lock %s: %w", dir, err)
	}
```

Close `lock` on every later error return of `OpenLog`, and set `lock: lock` in the returned `Log`. In `Close`, after closing the logs, close `l.lock` (which releases the flock) and join its error. `Projects` and `ListFiles` already skip a name that is not a number, so `.lock` is invisible to them.

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

and after the list of names under `raw/<project_id>/`, one line on `raw/.lock`:

```go
// raw/.lock is held (flock) by the one Log open on the directory.
```

A crash between the write and the rename leaves a `<day>.import`. Nothing reads it, the next `WriteClosed` for the day overwrites it, and `removeMonth` deletes it with its month (its name starts with the month).

- [ ] **Step 4: Run the archive tests**

Run: `go test ./internal/archive ./internal/app -count=1`
Expected: PASS, the `Rotate` tests included. `internal/app`'s tests open the log through `Serve` and the parity tests; none may hold two at once.

- [ ] **Step 5: Commit**

```bash
git add internal/archive/log.go internal/archive/archive.go internal/archive/log_test.go
git commit -m "feat(archive): lock the events directory to one writer, and write a closed log from outside ingest"
```

---

### Task 3: import a day: only what the files lack

**Files:**
- Create: `internal/archive/import.go`
- Modify: `internal/archive/maintain.go` (`Archive.eng` becomes `Engine`; `Execer` goes)
- Test: `internal/archive/import_test.go`

**Interfaces:**
- Consumes: `Log.WriteClosed` (Task 2); `projectOf`, `list`, `sqlList`, `quote` (archive); `Engine` (reader.go: `Exec` and `Query`).
- Produces: `func (a *Archive) ImportDay(ctx context.Context, project int64, day string, evs []store.Event) (int, error)`. It returns how many events it wrote. `archive.New` now takes an `Engine` (`*engine.Engine` satisfies it). Task 4's import calls it through an interface of its own.

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

### Task 4: `twillingate events import`, and its documentation

**Files:**
- Create: `internal/app/import.go` (`ImportEvents`, `importEvents`, `importStore`, `dayImporter`, `rawDays`)
- Create: `cmd/twillingate/events.go` (the `events` command, sub-verb `import`)
- Modify: `cmd/twillingate/main.go` (the usage line and the package doc list `events`)
- Test: `internal/app/import_test.go`, `cmd/twillingate/events_test.go`
- Docs:
  - `deploy/UPGRADES.md`: a new last section;
  - `docs/twillingate.md`: the command table near the top; "Writing SQL against the event files", its column table; and "Identity";
  - `docs/deployment.md`: the `EVENTS_DIR` row; the "Routine operations" table; the event files paragraph (the one starting "The ingest process also keeps every event as files").

**Interfaces:**
- Consumes:
  - `store.Store.RawEvents(ctx, projectID int64, day civil.Date) ([]store.Event, error)` (Task 1);
  - `archive.OpenLog` and `archive.ErrLocked` (Task 2);
  - `archive.Archive.ImportDay` (Task 3);
  - `app.EngineOptions`, `app.Migrate`, `config.Load`, `store.Open` (existing).
- Produces:
  - `func ImportEvents(ctx context.Context, cfg *config.Config, st store.Store, force bool, out io.Writer) error` in `internal/app`;
  - the `meta` key `events_imported`, an RFC 3339 UTC time;
  - `twillingate events import [-force]`.

  Task 5 calls `importEvents` (unexported, same package) with a real archive.

- [ ] **Step 1: Write the failing app tests**

Create `internal/app/import_test.go`:

```go
package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/dmtrkzntsv/twillingate/internal/store/storetest"
)

// fakeImporter records each day handed over, and fails a day once when
// told to.
type fakeImporter struct {
	days []string
	fail map[string]error
}

func (f *fakeImporter) ImportDay(ctx context.Context, project int64, day string, evs []store.Event) (int, error) {
	if err := f.fail[day]; err != nil {
		delete(f.fail, day)
		return 0, err
	}
	ids := make([]string, len(evs))
	for i, e := range evs {
		ids[i] = e.ID
	}
	f.days = append(f.days, fmt.Sprintf("%d %s %v", project, day, ids))
	return len(evs), nil
}

// importDB is a store with project 1 holding rows.
func importDB(t *testing.T, rows ...store.Event) store.Store {
	t.Helper()
	ctx := context.Background()
	st := storetest.Open(t, filepath.Join(t.TempDir(), "twillingate.db"))
	reg := manage.New(st, nil)
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manage.NewOps(reg, st).CreateProject(ctx, "test", manage.ProjectSpec{Name: "App"}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteEvents(ctx, rows); err != nil {
		t.Fatal(err)
	}
	return st
}

func importRow(id string, family store.Family, at string) store.Event {
	ts, _ := time.Parse(time.RFC3339, at)
	e := store.Event{ID: id, ProjectID: 1, Family: family, EventName: "x", TS: ts, ReceivedAt: ts,
		ActorID: "h", ActorKind: store.ActorConnection, Platform: "web"}
	if family == store.FamilyMeasures {
		v := 1.0
		e.Measure, e.Value = store.MeasureTime, &v
	}
	return e
}

var importNow = time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)

// TestImportEventsHandsEveryDayOver: every day of raw rows, every family,
// goes to the importer in order, and meta records the import.
func TestImportEventsHandsEveryDayOver(t *testing.T) {
	st := importDB(t,
		importRow("v1", store.FamilyViews, "2026-08-20T10:00:00Z"),
		importRow("p1", store.FamilyProduct, "2026-08-21T09:00:00Z"),
		importRow("m1", store.FamilyMeasures, "2026-08-21T09:30:00Z"))
	imp := &fakeImporter{}
	var out bytes.Buffer
	if err := importEvents(context.Background(), st, imp, 400, importNow, &out); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1 2026-08-20 [v1]", "1 2026-08-21 [p1 m1]"}; !slices.Equal(imp.days, want) {
		t.Fatalf("days = %v, want %v", imp.days, want)
	}
	if !strings.Contains(out.String(), "project 1: 2 days, 3 rows, 3 written") {
		t.Fatalf("output %q lacks the project's line", out.String())
	}
	if got, _ := st.GetMeta(context.Background(), "events_imported"); got != "2026-08-22T04:00:00Z" {
		t.Fatalf("meta = %q, want the time it finished", got)
	}
}

// TestImportEventsSkipsDaysRetentionDeletes: a day RETENTION_DAYS would
// delete is not handed over.
func TestImportEventsSkipsDaysRetentionDeletes(t *testing.T) {
	st := importDB(t,
		importRow("old", store.FamilyProduct, "2026-08-17T10:00:00Z"), // today - 5
		importRow("kept", store.FamilyProduct, "2026-08-18T10:00:00Z"))
	imp := &fakeImporter{}
	if err := importEvents(context.Background(), st, imp, 5, importNow, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1 2026-08-18 [kept]"}; !slices.Equal(imp.days, want) {
		t.Fatalf("days = %v, want %v", imp.days, want)
	}
}

// TestImportEventsRetriesAFailedDay: a failed day is reported, the others
// go on, the call fails and meta stays unset; run again, it succeeds and
// records the import.
func TestImportEventsRetriesAFailedDay(t *testing.T) {
	st := importDB(t,
		importRow("a", store.FamilyProduct, "2026-08-20T10:00:00Z"),
		importRow("b", store.FamilyProduct, "2026-08-21T10:00:00Z"))
	imp := &fakeImporter{fail: map[string]error{"2026-08-20": errors.New("disk full")}}
	var out bytes.Buffer
	if err := importEvents(context.Background(), st, imp, 400, importNow, &out); err == nil {
		t.Fatal("an import with a failed day succeeded")
	}
	if !strings.Contains(out.String(), "disk full") {
		t.Fatalf("output %q does not name the failure", out.String())
	}
	if got, _ := st.GetMeta(context.Background(), "events_imported"); got != "" {
		t.Fatalf("meta after a failed day = %q, want unset", got)
	}
	if err := importEvents(context.Background(), st, imp, 400, importNow, &out); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1 2026-08-21 [b]", "1 2026-08-20 [a]", "1 2026-08-21 [b]"}; !slices.Equal(imp.days, want) {
		t.Fatalf("days = %v, want %v", imp.days, want)
	}
	if got, _ := st.GetMeta(context.Background(), "events_imported"); got == "" {
		t.Fatal("meta unset after a run where every day succeeded")
	}
}

// TestImportEventsRunsOnce: with events_imported set, ImportEvents says
// when it ran and does nothing else, not even take the events directory.
func TestImportEventsRunsOnce(t *testing.T) {
	st := importDB(t)
	if err := st.SetMeta(context.Background(), "events_imported", "2026-08-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "raw")
	held, err := archive.OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	cfg := configtest.Load(t, map[string]string{"EVENTS_DIR": dir})
	var out bytes.Buffer
	if err := ImportEvents(context.Background(), cfg, st, false, &out); err != nil {
		t.Fatalf("a second run: %v, want nil", err)
	}
	if !strings.Contains(out.String(), "2026-08-01T00:00:00Z") || !strings.Contains(out.String(), "-force") {
		t.Fatalf("output %q: want when it ran and how to run it again", out.String())
	}
}

// TestImportEventsRefusesWhileTheServerRuns: while a Log holds the events
// directory (the ingest server), the import fails with ErrLocked and says
// to stop the service, before it writes anything.
func TestImportEventsRefusesWhileTheServerRuns(t *testing.T) {
	st := importDB(t, importRow("a", store.FamilyProduct, "2026-08-20T10:00:00Z"))
	dir := filepath.Join(t.TempDir(), "raw")
	held, err := archive.OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	cfg := configtest.Load(t, map[string]string{"EVENTS_DIR": dir})
	err = ImportEvents(context.Background(), cfg, st, true, &bytes.Buffer{})
	if !errors.Is(err, archive.ErrLocked) || !strings.Contains(err.Error(), "stop the ingest service") {
		t.Fatalf("err = %v, want ErrLocked telling to stop the ingest service", err)
	}
	if got, _ := st.GetMeta(context.Background(), "events_imported"); got != "" {
		t.Fatalf("meta = %q, want unset", got)
	}
}
```

If `manage.New` needs a non-nil logger, pass `slog.Default()` (and import `log/slog`). `storetest.Open` copies a migrated database; it never migrates its own (CLAUDE.md, Checks).

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/app -run 'TestImportEvents' -count=1`
Expected: FAIL to compile: `importEvents undefined`, `ImportEvents undefined`.

- [ ] **Step 3: Write the import**

Create `internal/app/import.go`:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

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
// force. It takes the events directory as the ingest server does, so it
// refuses (archive.ErrLocked) while the server runs: nothing else may
// write events or raw/ while it reads. The closed logs it leaves are the
// next start's pass to dump.
func ImportEvents(ctx context.Context, cfg *config.Config, st store.Store, force bool, out io.Writer) error {
	if done, err := st.GetMeta(ctx, eventsImportedKey); err != nil {
		return err
	} else if done != "" && !force {
		fmt.Fprintf(out, "events already imported on %s; -force imports again (what the files hold is skipped)\n", done)
		return nil
	}
	log, err := archive.OpenLog(cfg.EventsDir)
	if errors.Is(err, archive.ErrLocked) {
		return fmt.Errorf("%w: stop the ingest service first", err)
	}
	if err != nil {
		return err
	}
	defer log.Close()
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
```

- [ ] **Step 4: Run the app tests**

Run: `go test ./internal/app -run 'TestImportEvents' -count=1`
Expected: PASS.

- [ ] **Step 5: Write the command, test first**

Create `cmd/twillingate/events_test.go`:

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestEventsImport: `events` alone prints its usage; `events import` on an
// install with no rows imports nothing and records it, and a second run
// says it ran.
func TestEventsImport(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"events"}, &out); code != 2 || !strings.Contains(out.String(), "usage") {
		t.Fatalf("events: exit %d, %q; want 2 and the usage", code, out.String())
	}
	out.Reset()
	if code := run([]string{"events", "import"}, &out); code != 0 || !strings.Contains(out.String(), "imported 0 of 0 rows") {
		t.Fatalf("events import: exit %d, %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"events", "import"}, &out); code != 0 || !strings.Contains(out.String(), "already imported") {
		t.Fatalf("events import again: exit %d, %q", code, out.String())
	}
}
```

With no project, no day is handed over, so no engine child is spawned (the engine starts on its first request). `EVENTS_DIR` defaults to `raw/` beside `withDB`'s temporary database.

Run: `go test ./cmd/twillingate -run TestEventsImport -count=1`
Expected: FAIL: `events` is an unknown command (exit 2 without "usage", or the import exits non-zero).

Create `cmd/twillingate/events.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/dmtrkzntsv/twillingate/internal/app"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func init() { commands["events"] = cmdEvents }

const eventsUsage = "usage: twillingate events import [-force]"

// cmdEvents is `twillingate events import`: once, with the ingest service
// stopped, it copies the raw rows the database holds into the event files
// (app.ImportEvents). It migrates the schema first, as `migrate` does, so
// it can run before the new binary's first start.
func cmdEvents(args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "import" {
		fmt.Fprintln(stdout, eventsUsage)
		return 2
	}
	fs := flag.NewFlagSet("events import", flag.ContinueOnError)
	fs.SetOutput(stdout)
	force := fs.Bool("force", false, "import again although it ran (what the files hold is skipped)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	defer st.Close()
	ctx := context.Background()
	if err := app.Migrate(ctx, cfg, st); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if err := app.ImportEvents(ctx, cfg, st, *force, stdout); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	return 0
}
```

In `cmd/twillingate/main.go`, add `events` to the `usage` constant (`<serve|reporting|migrate|events|keygen|project|key|form|version>`) and to the package doc's list of subcommands. If `main_test.go` pins the usage string, update it.

Run: `go test ./cmd/twillingate -count=1`
Expected: PASS.

- [ ] **Step 6: Document it, in the same commit**

**`deploy/UPGRADES.md`.** Append after "Upgrading to the derived tables (no migration)":

````markdown
### Upgrading to imported event files (no migration, one command)

No schema migration. After upgrading, run one command once, with the
ingest service stopped: it copies into `raw/` every raw row the database
holds that the files do not.

- The rows it copies are the events from before the upgrade that began
  writing `raw/`, back to the oldest day the raw window
  (`RETENTION_RAW_DAYS`) still holds, and any batch the log missed since.
- It takes about 30 s per million rows. The next start's pass turns what
  it copied into day files, about 0.2 s a day.

```sh
# systemd
sudo systemctl stop twillingate
sudo -u twillingate sh -ac '. /etc/twillingate/twillingate.env; twillingate events import'
sudo systemctl start twillingate

# compose
docker compose stop twillingate
docker compose run --rm twillingate events import
docker compose start twillingate
```

On a split install, stop the ingest process; the console may keep
running. While the ingest server runs, the command refuses: the server
now holds `raw/.lock`. The command prints one line per project and
`imported N of M rows` when done. If a day fails, it says which and exits
1; run it again, and what is already copied is skipped. Once it has
succeeded, the `meta` row `events_imported` records when, and the command
only says so. `-force` copies again, for example after restoring an older
`raw/`.

- History older than the raw window exists in the database only as daily
  aggregates (`agg_*`). They cannot become events, so they are not
  imported. Nothing changes today: dashboards still read the database.
  Once a later release moves the dashboards onto the files, they start at
  the oldest raw day the database holds now:
  `sqlite3 twillingate.db "SELECT min(day) FROM events"`.
- An imported row has an install id only where the install was the actor
  (an `$install_id` outranked by a `$user_id` was never stored). It has the
  latest name the database holds for its user and group, not the name sent
  with it.
- Check it per project: `query` with `source: "files"`,
  `SELECT day, count(*) FROM events WHERE project_id = 1 GROUP BY ALL ORDER BY day`,
  against `sqlite3 twillingate.db "SELECT day, count(*) FROM events WHERE project_id = 1 GROUP BY day"`.

Rollback: an older binary reads the imported days like any other day
file, and ignores `raw/.lock`; nothing to undo.
````

**`docs/twillingate.md`, the command table** near the top: add a row after `twillingate migrate`:

```markdown
| `twillingate events import` | Once, with the ingest service stopped: copies the raw rows the database holds into the event files (`deploy/UPGRADES.md`) |
```

**`docs/twillingate.md`, "Writing SQL against the event files".** The first paragraph says the files hold events "received since the upgrade that began writing them". Replace that clause with:

```markdown
every project's, from the oldest raw day the database held when
`twillingate events import` copied it into them (older history exists only
as the `agg_*` tables; before that import, since the upgrade that began
writing them), today's included (each `BUFFER_FLUSH_INTERVAL`)
```

In the column table, extend two rows:

```markdown
| `install_id` | VARCHAR | the client's `$install_id`, also when a `$user_id` is the actor; on events imported from the database, only when the install was the actor |
| `user_name`, `group_name` | VARCHAR | the names sent beside `user_id` and `group_id` (see [Identity](#identity)); on events imported from the database, the latest name it held |
```

**`docs/twillingate.md`, "Identity".** The paragraph beginning "In the event files every event also keeps the `$install_id`, `$user_name`" ends by saying how long the names are kept. Add one sentence after it:

```markdown
Events imported from the database (`twillingate events import`), which
kept no names per event, carry the latest name it held, and an
`$install_id` only where the install was the actor.
```

**`docs/deployment.md`.**
- In the `EVENTS_DIR` row, after "On a split install, give both processes the same value.", add: `The ingest process holds `.lock` in it: a second ingest process, or `twillingate events import`, refuses while one runs.`
- In "Routine operations", add a row after "Apply migrations only": `| Import the database's raw rows into the event files (once, after the upgrade that brings it) | stop the ingest service, `twillingate events import`, start it ([UPGRADES.md](https://github.com/dmtrkzntsv/twillingate/blob/main/deploy/UPGRADES.md)) |`.
- In the paragraph starting "The ingest process also keeps every event as files", add after its first sentence: `Events from before the files began are copied in once, by hand, with `twillingate events import` (stop the ingest service first).`

Run: `go test ./internal/api -run 'Docs' -count=1 && go test ./docs -count=1`
Expected: PASS. If `docs_sync_test.go` binds the CLI command table, it now requires `events` there, which is the row added above. Check by reading that each other sentence sits where the plan says.

- [ ] **Step 7: Commit**

```bash
git add internal/app/import.go internal/app/import_test.go cmd/twillingate/events.go cmd/twillingate/events_test.go cmd/twillingate/main.go deploy/UPGRADES.md docs/twillingate.md docs/deployment.md
git commit -m "feat(cmd): import the raw rows SQLite holds into the event files once, with events import"
```

Add `cmd/twillingate/main_test.go` to the `git add` if its usage pin changed.

---

### Task 5: the files hold what SQLite holds, end to end

**Files:**
- Create: `internal/app/archive_import_test.go`
- Test: the same file; `TestMain` and `appEngineChild` come from `archive_parity_test.go`

**Interfaces:**
- Consumes everything above through real parts: `storetest.Open`, `archive.OpenLog`, `engine.New`, `archive.New`, `archive.NewReader`, `pipeline.Sinks`, and `importEvents` (Task 4).
- Produces: nothing new.

- [ ] **Step 1: Write the test**

Create `internal/app/archive_import_test.go`:

```go
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
```

Why these files, in order:
- **The import** writes `2026-09-29.1.closed` (s1), `2026-10-01.1.closed` (o1; o2 is held by the open `2026-10-01.log`) and `2026-10-11.1.closed` (t1; t2 is held by the open `2026-10-11.log`). s2 is held by the month file, so September 30 writes nothing.
- **The pass** rotates `2026-10-01.log` to `.2.closed`, then dumps each claimed log:
  - October 1's two closed logs become the day file and segment 1;
  - September 29's becomes a day file, and September merges again;
  - October 11's becomes `2026-10-11.parquet`, beside today's open log.

If the list differs, derive the expected list from that sequence before changing any code: a different order of claims is fine as long as the counts match.

- [ ] **Step 2: Run it**

Run: `go test ./internal/app -run 'TestImportFillsTheFilesFromSQLite' -count=1 -v`
Expected: PASS. Tasks 1–4 are in place, so this test needs no new code. If it fails, the failure is a bug in Tasks 1–4: fix it there, in a commit of its own (`fix(<scope>): …`), and rerun.

- [ ] **Step 3: Run everything**

Run: `make check`
Expected: PASS, the coverage gate included. If coverage dropped, the uncovered lines are likely `ImportEvents`' error branches (the engine options, a failing `GetMeta`). Cover one with a store wrapper in `import_test.go` that embeds `store.Store` and fails `GetMeta`.

- [ ] **Step 4: Commit**

```bash
git add internal/app/archive_import_test.go
git commit -m "test(app): the imported files hold every row SQLite holds"
```

---

## After the last task

- Push `feat/raw-migrate` (`git push -u origin feat/raw-migrate`) and open a draft PR with base `feat/raw-derived`.
  - Title: `feat: import the raw rows SQLite holds into the event files with twillingate events import`.
  - Body: problems first, then the reasoning; it lists the Decisions above.
- **Measured on landing:** the import's time on `~/.cache/twbench` (615 MB of bench data, 36M rows), loaded into SQLite with `RETENTION_RAW_DAYS` covering a month. The target is D19's 30 s per million rows.
