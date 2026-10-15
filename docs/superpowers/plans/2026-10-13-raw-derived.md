# Raw Derived Tables Implementation Plan (PR 5a)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every event in the files keeps the client's `install_id` and the user's and group's names. The daily pass derives three tables from the files: `sessions`, `identities` and `aliases`. The `query` tool's files source reads them, with today's part computed on read.

**Architecture:**
- **On the row:** `store.Event` gains `InstallID`, `UserName` and `GroupName`. Ingest and the form handler fill them, and the archive log writes them; SQLite ignores them.
- **Derived files:** `internal/archive` gains:
  - the derived tables' SELECTs (`derive.go`);
  - a listing that tells which derived files are current with the events files they come from (`files.go`);
  - a fifth step in each project's maintenance that writes the stale ones;
  - three more views in the reader's child.
- **Freshness** is by modification time, with no index, as the rest of `raw/` (D2).
- **What no current file covers** is derived on read: the open logs, and a day dumped but not derived yet.

**Tech Stack:** Go 1.25, DuckDB v1.5.6 through `internal/engine` (the child process), the PR 3/4 archive (`internal/archive`), modernc SQLite only in the parity test.

**Spec:** `docs/superpowers/specs/2026-10-12-raw-first-design.md`:
- D16: sessions;
- D17: everything on the event, identities derived;
- D18: aliases;
- D2 and D6: the layout, no index, rules on read.

Roadmap: `docs/superpowers/plans/2026-10-12-raw-first-roadmap.md`, PR 5a, including its two corrections to the spec. Earlier plans for the conventions this one follows:
- `2026-10-12-raw-log.md`: the line, the dump, the merge;
- `2026-10-13-raw-query.md`: the reader, `_tw_hot`, `_tw_all`, the signature.

## Facts this plan rests on (probed 2026-10-13, DuckDB v1.5.6 through the engine child)

- `epoch(TIMESTAMP)` is a DOUBLE. `CAST(floor(epoch(ts)) AS BIGINT)` gives whole seconds, as SQLite's `CAST(strftime('%s', ts) AS INTEGER)`. The log writes `ts` to the second anyway.
- **Ordered aggregates work.** `first(p ORDER BY t, id)` and `last(p ORDER BY t, id)` pick by `t`, then by `id`.
- `arg_max(n, t) FILTER (WHERE n IS NOT NULL)` skips the NULL names.
- **Integer types.** `sum(BIGINT)` is HUGEINT, so cast it to BIGINT. `count(*) FILTER (…)` is BIGINT.
- `sum(x) OVER (PARTITION BY k ORDER BY t)` counts the rows tied on `t` together (RANGE frame), as SQLite does. `lag(t)` of a tied row is the tied value, so its gap is 0 either way.

## Global Constraints

- **SQLite is unchanged.**
  - No migration.
  - `events` gets no new column.
  - `UpsertIdentities` keeps writing `identities`: the widgets and tools still read SQLite until 5d.
  - Every widget, system dashboard and tool other than `query`'s files source behaves as today.
- **The line format stays `v: 1`.** The three new keys are additive: a line or file without them reads NULL.
  - `dumpQuery` types every key of `fields` whether a line has it or not, so old logs dump as before.
  - PR 3's `maintain_test.go` and PR 4's `reader_test.go` stay green, with only the `files` helper changed (Task 4).
- **Derived files are disposable.** Deleting any of them loses nothing: the next pass rewrites it, and until then a reader derives it on read. Nothing but the archive writes them, through `Archive.write` (tmp, fsync, rename).
- **`internal/archive` stays a leaf.** It imports only `engine`, `store` and `shared/civil`. `Exec` stays the archive's own (PR 3 decision 5); user SQL reaches the engine only through `Query`.
- **The tables' columns are a contract** (PR 4): `events` gains three columns, and `sessions`, `identities` and `aliases` are new. Docs change in the same commit as the code that exposes them (CLAUDE.md, Documentation).
- **Commits and checks.** Conventional Commits with CLAUDE.md's scopes. Run `make check` before every push.
- **Toolchain.** Go is at `/usr/local/go/bin`, not on `PATH`. Set `GOTMPDIR=$HOME/.cache/gotmp`, because `/tmp`'s quota fills. Every command below assumes:
  `export PATH=$PATH:/usr/local/go/bin GOTMPDIR=$HOME/.cache/gotmp`.

## Decisions this plan takes (review them)

1. **The derived files live in the project's directory**, so a project stays one directory to back up, hand over or delete (D2):
   - `raw/<project>/sessions/<day>.parquet`, then `sessions/<month>.parquet`;
   - `raw/<project>/identities.parquet`;
   - `raw/<project>/aliases.parquet`.

   The spec wrote `identities/<project>.parquet`.
2. **A derived file is current when it is no older than every events file it comes from.** Each is checked by modification time, with no manifest (D2: "no index").
   - Retention deletes files, which no modification time shows. So a pass that deleted a month rewrites `identities` and `aliases` anyway.
   - A pass that crashes between a dump and the derive leaves a stale file. The next pass sees it, and a reader meanwhile derives the day on read.
3. **The reader reads a derived file only while it is current, and derives the rest on read:**
   - **`sessions`:** the days with a log, plus the days whose session file is missing or stale, sessionized from `_tw_all`.
   - **`identities` and `aliases` when current:** the stored file, plus the project's open logs (`_tw_hot`), folded into one row per key.
   - **`identities` and `aliases` when stale:** every event of the project.

   This is D16's and D17's "today's part live from the log", extended to whatever the pass has not caught up with. No row counts twice and none is missing.
4. **`sessions` carries more than D16 lists.** Besides actor, kind, start, end, views and entry and exit path, it has:
   - `duration_sec`;
   - `session_id`, the declared id or NULL;
   - the entry view's host, `referrer_source` and UTMs.

   5d's widgets need sessions by source, and adding a column later means rewriting every session file.
5. **Sessions split exactly as `agg_views_daily` splits them** (`viewSessionsCTE`):
   - a view with a `session_id` belongs to that id's session;
   - any other view starts a session when it is the actor's first of the day, or comes more than 30 minutes after the actor's previous view, whichever session that view was in;
   - midnight UTC cuts sessions.

   The parity test (Task 6) holds the two together. The cap on kinds (`(other)`) does not apply: the caps go in PR 6.
6. **`identities` counts what `v_identity_daily` counts.**
   - `views` and `events` are the id's rows of the views and product families. Measures are not counted.
   - `name` is the name sent with the id's latest event that carried one.
   - `first_seen` and `last_seen` are bounded by the files' history: since the upgrade that began writing `raw/` (5c migrates the raw window), and within `RETENTION_DAYS`.
7. **`aliases` pairs a `user_id` with an `install_id` sent on the same event, in every family.** There is no `visitor_id` (roadmap correction to D18).
8. **`install_id` is on every row whose client sent `$install_id`,** also when it is the actor (then it equals `actor_id`). A name is kept only beside its id, as `identities` does today: `$user_name` without `$user_id` is dropped.
9. **Rules (D6) do not apply to the derived tables.** They describe the events as stored, and a rule changes names, not who did what or when.

## Review Focus

1. **A late event for a closed day,** dumped as a segment after the day's session file was written. The day's sessions must count each view once:
   - on read before the pass derives the day again;
   - after it does.

   Pinned by Task 4 `TestALateEventRederivesItsDay` and Task 5 `TestReaderDerivesAStaleDayOnRead`.
2. **An install upgraded from v0.18.0**, whose day and month files exist without any derived file:
   - the first pass derives every one, including the merged months;
   - a query before it answers the same.

   Pinned by Task 4 `TestAMonthMergedBeforeSessionsExistedGetsThem` and Task 5 `TestReaderWithoutDerivedFiles`.
3. **Retention deletes a month.**
   - Its session files go.
   - `identities` and `aliases` lose its users although no events file is newer.
   - A project left with no events file keeps no derived file.

   Pinned by Task 4 `TestRetentionTakesTheDerivedFilesAlong` and `TestNoEventsFileNoDerivedFile`.
4. **A user named again today:** today's name wins over the stored one, and a day that sent no name keeps the old one. Counts add up, and `first_seen` stays the stored one. Pinned by Task 2 `TestIdentitiesCombineKeepsTheLatestName` and Task 5 `TestReaderIdentitiesFoldStoredAndToday`.
5. **Files written before this release,** with no `install_id`, `user_name` or `group_name` column, mixed with new ones: they derive and read without a binder error, the missing columns as NULL. Pinned by Task 4 `TestDeriveReadsFilesWrittenBeforeTheNewColumns` (the reader's `_tw_all` already unions `emptyRow()`, which Task 1 extends).

---

### Task 1: install_id and the names on every event

**Files:**
- Modify: `internal/store/store.go:66-87` (`Event`)
- Modify: `internal/server/handlers.go:269-281` (the `store.Event` literal in the ingest loop)
- Modify: `internal/server/forms.go:407` (the form's event)
- Modify: `internal/archive/log.go:285-353` (`fields`, `line`)
- Test: `internal/server/server_test.go`, `internal/server/forms_test.go:510-550`, `internal/archive/log_test.go:94-131`
- Docs: `docs/twillingate.md` ("Writing SQL against the event files" column table, "Identity")

**Interfaces:**
- Produces: `store.Event.InstallID`, `store.Event.UserName`, `store.Event.GroupName` (strings). The log line and the tables gain the VARCHAR columns `install_id`, `user_name`, `group_name`, in `fields` right after `session_id`. Tasks 2–6 read these columns by name.

- [ ] **Step 1: Write the failing server test**

Add to `internal/server/server_test.go`, after `TestBatchNamesAreDedupedAcrossEvents`:

```go
// TestEventsCarryIdsAndNames: every stored event keeps the client's
// $install_id, also when a $user_id outranks it as the actor, and each name
// beside the id it names (raw-first D17, D18). A name without its id is
// dropped, as identities drops it.
func TestEventsCarryIdsAndNames(t *testing.T) {
	q, h := testServer(t)
	post(h, `{"key":"`+testKey+`","attributes":{"$user_id":"u1","$user_name":"Ada","$install_id":"i1","$group_id":"g1","$group_name":"Acme"},
	  "events":[{"name":"a"},{"name":"b","attributes":{"$user_id":null}}]}`, nil)
	if len(q.events) != 2 {
		t.Fatalf("events = %d, want 2", len(q.events))
	}
	a, b := q.events[0], q.events[1]
	if a.ActorID != "u1" || a.InstallID != "i1" || a.UserName != "Ada" || a.GroupName != "Acme" {
		t.Errorf("event a = actor %q install %q user name %q group name %q; want u1, i1, Ada, Acme", a.ActorID, a.InstallID, a.UserName, a.GroupName)
	}
	if b.ActorID != "i1" || b.InstallID != "i1" || b.UserID != "" || b.UserName != "" || b.GroupName != "Acme" {
		t.Errorf("event b = actor %q install %q user %q user name %q group name %q; want i1, i1, no user and no user name, Acme",
			b.ActorID, b.InstallID, b.UserID, b.UserName, b.GroupName)
	}
}
```

In `internal/server/forms_test.go` `TestFormEventPrefilled`:
- add `"$install_id": {"i1"},` to the posted `url.Values`;
- add `InstallID: "i1",` to `want` after `SessionID: "s1",`.

In `internal/archive/log_test.go` `TestLineWritesTheFields`, add `InstallID: "in", UserName: "un", GroupName: "gn",` to the event literal, after `SessionID: "s",`. Its reflection check otherwise fails on the new fields.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/server -run 'TestEventsCarryIdsAndNames|TestFormEventPrefilled' -count=1 && go test ./internal/archive -run TestLineWritesTheFields -count=1`
Expected: FAIL to compile, `a.InstallID undefined (type store.Event has no field or method InstallID)`.

- [ ] **Step 3: Add the fields and fill them**

`internal/store/store.go`, in `Event`, after the `ActorID, ActorKind, UserID, GroupID, SessionID string` line:

```go
	// InstallID is the client's $install_id, also when a $user_id outranks
	// it as the actor; UserName and GroupName name UserID and GroupID, each
	// kept only beside its id. SQLite's events table does not store them
	// (its identities table holds the latest names); the event files do
	// (raw-first D17, D18).
	InstallID, UserName, GroupName string
```

`internal/server/handlers.go`, right before `e := store.Event{` in the ingest loop:

```go
		// A name is kept only beside the id it names, as identityNames keeps it.
		var userName, groupName string
		if rv.UserID != "" {
			userName = rv.UserName
		}
		if rv.GroupID != "" {
			groupName = rv.GroupName
		}
```

and in the literal, extend the actor line:

```go
			ActorID: actor, ActorKind: actorKind, UserID: user, GroupID: group, SessionID: rv.SessionID,
			InstallID: rv.InstallID, UserName: userName, GroupName: groupName,
```

`internal/server/forms.go`, replace `ev.ActorID, ev.ActorKind, ev.UserID = actor, actorKind, user` with:

```go
	ev.ActorID, ev.ActorKind, ev.UserID, ev.InstallID = actor, actorKind, user, fc.InstallID
```

`internal/archive/log.go`, in `fields`, after `{"session_id", "VARCHAR"},`:

```go
	{"install_id", "VARCHAR"}, {"user_name", "VARCHAR"}, {"group_name", "VARCHAR"},
```

and in `line`, add three entries to the string map:

```go
		"install_id": e.InstallID, "user_name": e.UserName, "group_name": e.GroupName,
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/server ./internal/archive -count=1`
Expected: PASS.
- `TestReaderOnAnEmptyDirectory` builds its column list from `fields`, so it covers the three new columns.
- If it lists the columns literally instead, add `install_id`, `user_name` and `group_name` at VARCHAR after `session_id`.

- [ ] **Step 5: Document the columns**

`docs/twillingate.md`, in "Writing SQL against the event files", replace the row
`` | `kind`, `actor_id`, `actor_kind`, `user_id`, `group_id`, `session_id` | VARCHAR | | ``
with:

```markdown
| `kind`, `actor_id`, `actor_kind`, `user_id`, `group_id`, `session_id` | VARCHAR | |
| `install_id` | VARCHAR | the client's `$install_id`, also when a `$user_id` is the actor |
| `user_name`, `group_name` | VARCHAR | the names sent beside `user_id` and `group_id` (see [Identity](#identity)) |
```

In "Identity", after the sentence ending `` `$user_name` is kept only beside a `$user_id`. ``, add:

```markdown
In the event files every event also keeps the `$install_id`, `$user_name`
and `$group_name` it was sent with, for as long as `RETENTION_DAYS` keeps the
event (`query` with `source: "files"`): the names are personal data there
too.
```

- [ ] **Step 6: Commit**

```bash
git add internal/store/store.go internal/server/handlers.go internal/server/forms.go internal/server/server_test.go internal/server/forms_test.go internal/archive/log.go internal/archive/log_test.go docs/twillingate.md
git commit -m "feat(server): keep install_id and the user's and group's names on every event in the files"
```

---

### Task 2: the derived tables' SELECTs

**Files:**
- Create: `internal/archive/derive.go`
- Test: `internal/archive/derive_test.go`

**Interfaces:**
- Consumes:
  - `emptyRow()`, `sqlList(paths)`, `logSelect(path, cols, tolerant)`, `columnsOf(keys)` and `logKeys(path)` (existing);
  - `fixture`, `f.write`, `f.values`, `evOf`, `ts` and `f.project1` (existing test helpers).
- Produces (Tasks 4 and 5 call these):
  - `const sessionsDir = "sessions"`, `identitiesFile = "identities.parquet"`, `aliasesFile = "aliases.parquet"`;
  - `func sessionsSelect(src string) string`;
  - `func identitiesSelect(src string) string`;
  - `func aliasesSelect(src string) string`;
  - `func identitiesCombine(parts string) string`;
  - `func aliasesCombine(parts string) string`;
  - `func eventsOf(files []string) string`.

  `src` is a relation usable after `FROM` (a parenthesized SELECT) with the events' columns; `parts` is the body of a SELECT whose rows have the derived table's columns.

The tables' columns, in order:

- **`sessions`:**
  - `project_id` BIGINT, `day` DATE, `kind` VARCHAR, `actor_id` VARCHAR, `actor_kind` VARCHAR, `session_id` VARCHAR;
  - `started_at` TIMESTAMP, `ended_at` TIMESTAMP, `duration_sec` BIGINT, `views` BIGINT;
  - `entry_host`, `entry_path`, `exit_path`, `referrer_source`, `utm_source`, `utm_medium`, `utm_campaign` (all VARCHAR).
- **`identities`:** `project_id` BIGINT, `kind` VARCHAR, `id` VARCHAR, `name` VARCHAR, `first_seen` TIMESTAMP, `last_seen` TIMESTAMP, `views` BIGINT, `events` BIGINT.
- **`aliases`:** `project_id` BIGINT, `user_id` VARCHAR, `install_id` VARCHAR, `first_seen` TIMESTAMP, `last_seen` TIMESTAMP, `events` BIGINT.

- [ ] **Step 1: Write the failing tests**

Create `internal/archive/derive_test.go`:

```go
package archive

import (
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// pageView is a web page view of project 1 by actor (a connection hash) at at.
func pageView(id, actor, at, path string) store.Event {
	return store.Event{ID: id, ProjectID: 1, Family: store.FamilyViews, EventName: "$page_view", Kind: "web",
		ActorID: actor, ActorKind: store.ActorConnection, TS: ts(at), ReceivedAt: ts(at), Host: "a.io", Path: path}
}

// logRel is project 1's log of day as a relation with the events' columns.
func (f fixture) logRel(t *testing.T, day string) string {
	t.Helper()
	p := f.project1(day + ".log")
	keys, err := logKeys(p)
	if err != nil {
		t.Fatal(err)
	}
	return "(" + logSelect(p, columnsOf(keys), false) + ")"
}

// TestSessionsSplitAsAggViewsDailyDoes: a gap over 30 minutes starts a
// session, a declared session id holds one together whatever the gap, a
// lone view is a session of one; product events are not views. The entry
// view gives the path, referrer and UTMs, the exit view its path.
func TestSessionsSplitAsAggViewsDailyDoes(t *testing.T) {
	f := newFixture(t)
	a1 := pageView("a1", "a", "2026-10-11T10:00:00Z", "/")
	a1.ReferrerSource, a1.UTMSource = "google", "nl"
	s1 := pageView("s1", "s", "2026-10-11T11:00:00Z", "/app")
	s2 := pageView("s2", "s", "2026-10-11T13:00:00Z", "/app/2")
	s1.SessionID, s2.SessionID = "x", "x"
	f.write(t, a1,
		pageView("a2", "a", "2026-10-11T10:10:00Z", "/pricing"),
		pageView("a3", "a", "2026-10-11T10:50:00Z", "/"), // 40 minutes on: a new session
		s1, s2, // one declared session, two hours long
		pageView("b1", "b", "2026-10-11T12:00:00Z", "/"),
		evOf(1, "p1", "2026-10-11T10:05:00Z"))
	got := f.values(t, "SELECT actor_id, views, duration_sec, entry_path, exit_path, referrer_source, utm_source, session_id FROM ("+
		sessionsSelect(f.logRel(t, "2026-10-11"))+") ORDER BY actor_id, started_at")
	want := []string{
		"[a 2 600 / /pricing google nl <nil>]",
		"[a 1 0 / / <nil> <nil> <nil>]",
		"[b 1 0 / / <nil> <nil> <nil>]",
		"[s 2 7200 /app /app/2 <nil> <nil> x]",
	}
	if !eq(got, want) {
		t.Fatalf("sessions = %v\nwant       %v", got, want)
	}
}

// TestSessionsCutAtMidnight: an actor's views either side of midnight UTC
// are two sessions, one per day, as the daily rollup has them.
func TestSessionsCutAtMidnight(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-10-11T23:55:00Z", "/"), pageView("a2", "a", "2026-10-12T00:05:00Z", "/"))
	rel := "(SELECT * FROM " + f.logRel(t, "2026-10-11") + " UNION ALL BY NAME SELECT * FROM " + f.logRel(t, "2026-10-12") + ")"
	got := f.values(t, "SELECT CAST(day AS VARCHAR), views FROM ("+sessionsSelect(rel)+") ORDER BY day")
	if !eq(got, []string{"[2026-10-11 1]", "[2026-10-12 1]"}) {
		t.Fatalf("sessions = %v, want one per day", got)
	}
}

func named(e store.Event, user, userName, group, groupName string) store.Event {
	e.UserID, e.UserName, e.GroupID, e.GroupName = user, userName, group, groupName
	return e
}

// TestIdentitiesKeepTheLatestNameAndCount: one row per user and per group;
// the name of the latest event that carried one; first and last seen;
// views and product events counted, measures not.
func TestIdentitiesKeepTheLatestNameAndCount(t *testing.T) {
	f := newFixture(t)
	m := evOf(1, "m1", "2026-10-11T13:00:00Z")
	m.Family = store.FamilyMeasures
	f.write(t,
		named(pageView("v1", "u1", "2026-10-11T08:00:00Z", "/"), "u1", "", "", ""),
		named(evOf(1, "p1", "2026-10-11T09:00:00Z"), "u1", "Ada", "g1", "Acme"),
		named(evOf(1, "p2", "2026-10-11T10:00:00Z"), "u1", "Ada L.", "g1", ""),
		named(evOf(1, "p3", "2026-10-11T11:00:00Z"), "u1", "", "", ""), // no name: the last one stays
		named(evOf(1, "p4", "2026-10-11T12:00:00Z"), "u2", "", "", ""), // never named
		named(m, "u2", "Bo", "", ""), // a measure: neither counted nor naming
	)
	got := f.values(t, "SELECT kind, id, name, CAST(first_seen AS VARCHAR), CAST(last_seen AS VARCHAR), views, events FROM ("+
		identitiesSelect(f.logRel(t, "2026-10-11"))+")")
	want := []string{
		"[group g1 Acme 2026-10-11 09:00:00 2026-10-11 10:00:00 0 2]",
		"[user u1 Ada L. 2026-10-11 08:00:00 2026-10-11 11:00:00 1 3]",
		"[user u2 <nil> 2026-10-11 12:00:00 2026-10-11 12:00:00 0 1]",
	}
	if !eq(got, want) {
		t.Fatalf("identities = %v\nwant         %v", got, want)
	}
}

// TestIdentitiesCombineKeepsTheLatestName is Review Focus 4: folding two
// sets of rows (a stored file and today's), the newer name wins, a newer
// set with no name keeps the older one, counts add, first_seen is the
// earliest.
func TestIdentitiesCombineKeepsTheLatestName(t *testing.T) {
	f := newFixture(t)
	f.write(t,
		named(evOf(1, "p1", "2026-10-11T09:00:00Z"), "u1", "Ada", "", ""),
		named(evOf(1, "p2", "2026-10-11T09:00:00Z"), "u2", "Bo", "", ""),
		named(evOf(1, "p3", "2026-10-12T09:00:00Z"), "u1", "Ada L.", "", ""),
		named(evOf(1, "p4", "2026-10-12T09:00:00Z"), "u2", "", "", ""))
	parts := "SELECT * FROM (" + identitiesSelect(f.logRel(t, "2026-10-11")) + ") UNION ALL BY NAME SELECT * FROM (" +
		identitiesSelect(f.logRel(t, "2026-10-12")) + ")"
	got := f.values(t, "SELECT id, name, CAST(first_seen AS VARCHAR), events FROM ("+identitiesCombine(parts)+") ORDER BY id")
	want := []string{"[u1 Ada L. 2026-10-11 09:00:00 2]", "[u2 Bo 2026-10-11 09:00:00 2]"}
	if !eq(got, want) {
		t.Fatalf("combined = %v, want %v", got, want)
	}
}

// TestAliasesPairUserAndInstall: every (user_id, install_id) sent on one
// event, with first seen and the events; an install never signed in pairs
// with nothing. Combining two sets adds them.
func TestAliasesPairUserAndInstall(t *testing.T) {
	f := newFixture(t)
	pair := func(e store.Event, user, install string) store.Event {
		e.UserID, e.InstallID = user, install
		return e
	}
	f.write(t,
		pair(evOf(1, "p1", "2026-10-11T09:00:00Z"), "u1", "i1"),
		pair(evOf(1, "p2", "2026-10-11T10:00:00Z"), "u1", "i1"),
		pair(evOf(1, "p3", "2026-10-11T11:00:00Z"), "u1", "i2"), // a second device
		pair(evOf(1, "p4", "2026-10-11T12:00:00Z"), "", "i3"),
		pair(evOf(1, "p5", "2026-10-12T12:00:00Z"), "u1", "i1"))
	got := f.values(t, "SELECT user_id, install_id, CAST(first_seen AS VARCHAR), events FROM ("+aliasesSelect(f.logRel(t, "2026-10-11"))+")")
	if want := []string{"[u1 i1 2026-10-11 09:00:00 2]", "[u1 i2 2026-10-11 11:00:00 1]"}; !eq(got, want) {
		t.Fatalf("aliases = %v, want %v", got, want)
	}
	parts := "SELECT * FROM (" + aliasesSelect(f.logRel(t, "2026-10-11")) + ") UNION ALL BY NAME SELECT * FROM (" +
		aliasesSelect(f.logRel(t, "2026-10-12")) + ")"
	got = f.values(t, "SELECT user_id, install_id, CAST(last_seen AS VARCHAR), events FROM ("+aliasesCombine(parts)+") ORDER BY install_id")
	if want := []string{"[u1 i1 2026-10-12 12:00:00 3]", "[u1 i2 2026-10-11 11:00:00 1]"}; !eq(got, want) {
		t.Fatalf("combined aliases = %v, want %v", got, want)
	}
}

// TestDerivedSelectsOverNothingAreTyped: over a relation with no rows each
// SELECT gives its columns at their types, which a reader's empty tables
// rely on.
func TestDerivedSelectsOverNothingAreTyped(t *testing.T) {
	f := newFixture(t)
	empty := "(" + emptyRow() + ")"
	for name, tc := range map[string]struct{ sql, want string }{
		"sessions": {sessionsSelect(empty), "[[project_id BIGINT] [day DATE] [kind VARCHAR] [actor_id VARCHAR] [actor_kind VARCHAR] [session_id VARCHAR] " +
			"[started_at TIMESTAMP] [ended_at TIMESTAMP] [duration_sec BIGINT] [views BIGINT] [entry_host VARCHAR] [entry_path VARCHAR] " +
			"[exit_path VARCHAR] [referrer_source VARCHAR] [utm_source VARCHAR] [utm_medium VARCHAR] [utm_campaign VARCHAR]]"},
		"identities": {identitiesCombine(identitiesSelect(empty)), "[[project_id BIGINT] [kind VARCHAR] [id VARCHAR] [name VARCHAR] " +
			"[first_seen TIMESTAMP] [last_seen TIMESTAMP] [views BIGINT] [events BIGINT]]"},
		"aliases": {aliasesCombine(aliasesSelect(empty)), "[[project_id BIGINT] [user_id VARCHAR] [install_id VARCHAR] " +
			"[first_seen TIMESTAMP] [last_seen TIMESTAMP] [events BIGINT]]"},
	} {
		got := f.values(t, "SELECT column_name, column_type FROM (DESCRIBE SELECT * FROM ("+tc.sql+"))")
		if fmt.Sprint(got) != tc.want {
			t.Errorf("%s columns = %v\nwant %s", name, got, tc.want)
		}
	}
}
```

Add `"fmt"` to the imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/archive -run 'TestSessions|TestIdentities|TestAliases|TestDerivedSelects' -count=1`
Expected: FAIL to compile, `undefined: sessionsSelect`.

- [ ] **Step 3: Write `derive.go`**

```go
package archive

import "fmt"

// The tables derived from the events (raw-first D16–D18), in each
// project's directory beside its events files:
//
//	sessions/<YYYY-MM-DD>.parquet  the sessions of a day not merged yet
//	sessions/<YYYY-MM>.parquet     the sessions of a merged month
//	identities.parquet             one row per user and per group
//	aliases.parquet                one row per (user_id, install_id) pair
//
// The daily pass rewrites one whenever it is missing or older than an
// events file it comes from (projectOf); a reader reads only the current
// ones and derives what they lack from the events (the open logs, a day
// dumped since). They describe the events as stored: rules (D6) do not
// apply to them. Deleting one loses nothing.
const (
	sessionsDir    = "sessions"
	identitiesFile = "identities.parquet"
	aliasesFile    = "aliases.parquet"
)

// sessionGapSeconds is the silence after which an actor's next view starts
// a session when the client declared none: agg_views_daily's 30 minutes.
const sessionGapSeconds = 1800

// sessionsSelect is the SELECT of the sessions of src's views, src being a
// relation with the events' columns. Views split as viewSessionsCTE
// (store/sqlite/aggregate_views.go) splits them: a view with a session id
// belongs to that id's session; any other starts a session when it is the
// actor's first view of the day or comes more than 30 minutes after the
// actor's previous view, whichever session that was in. The day is part
// of the key, so midnight UTC cuts a session. One row per session: its
// first and last view, the whole seconds between them, its views, the
// entry view's host, path, referrer and UTMs and the exit view's path,
// views at the same second ordered by id.
func sessionsSelect(src string) string {
	return fmt.Sprintf(`WITH v AS (
  SELECT project_id, day, kind, actor_id, actor_kind, session_id, id, ts, host, path,
         referrer_source, utm_source, utm_medium, utm_campaign,
         CAST(floor(epoch(ts)) AS BIGINT) AS t
  FROM %s WHERE family = 'views'
), marked AS (
  SELECT *, CASE WHEN coalesce(session_id, '') <> '' THEN 0
                 WHEN lag(t) OVER w IS NULL OR t - lag(t) OVER w > %d THEN 1
                 ELSE 0 END AS new_session
  FROM v WINDOW w AS (PARTITION BY project_id, day, kind, actor_id ORDER BY t)
), keyed AS (
  SELECT *, CASE WHEN coalesce(session_id, '') <> '' THEN 'id:' || session_id
                 ELSE 'gap:' || CAST(sum(new_session) OVER (PARTITION BY project_id, day, kind, actor_id ORDER BY t) AS VARCHAR)
            END AS skey
  FROM marked
)
SELECT project_id, day, kind, actor_id,
       first(actor_kind ORDER BY ts, id) AS actor_kind,
       nullif(max(session_id), '') AS session_id,
       min(ts) AS started_at, max(ts) AS ended_at,
       max(t) - min(t) AS duration_sec,
       count(*) AS views,
       first(host ORDER BY ts, id) AS entry_host,
       first(path ORDER BY ts, id) AS entry_path,
       last(path ORDER BY ts, id) AS exit_path,
       first(referrer_source ORDER BY ts, id) AS referrer_source,
       first(utm_source ORDER BY ts, id) AS utm_source,
       first(utm_medium ORDER BY ts, id) AS utm_medium,
       first(utm_campaign ORDER BY ts, id) AS utm_campaign
FROM keyed
GROUP BY project_id, day, kind, actor_id, skey
ORDER BY project_id, day, started_at, actor_id`, src, sessionGapSeconds)
}

// identitiesSelect is the SELECT of the users and groups in src's views and
// product events (v_identity_daily's two families), one row per (project,
// kind, id): kind user for a user_id, group for a group_id; the name sent
// with the id's latest event that carried one; its first and last event;
// its views and its product events.
func identitiesSelect(src string) string {
	return `WITH src AS (SELECT * FROM ` + src + ` WHERE family IN ('views', 'product')),
ids AS (
  SELECT project_id, 'user' AS kind, user_id AS id, user_name AS name, ts, family FROM src WHERE user_id <> ''
  UNION ALL
  SELECT project_id, 'group' AS kind, group_id AS id, group_name AS name, ts, family FROM src WHERE group_id <> ''
)
SELECT project_id, kind, id,
       arg_max(name, ts) FILTER (WHERE name IS NOT NULL) AS name,
       min(ts) AS first_seen, max(ts) AS last_seen,
       count(*) FILTER (WHERE family = 'views') AS views,
       count(*) FILTER (WHERE family = 'product') AS events
FROM ids
GROUP BY project_id, kind, id
ORDER BY project_id, kind, id`
}

// aliasesSelect is the SELECT of every (user_id, install_id) pair sent on
// one event of src, any family (raw-first D18): first and last seen, and
// the events that carried it.
func aliasesSelect(src string) string {
	return `SELECT project_id, user_id, install_id, min(ts) AS first_seen, max(ts) AS last_seen, count(*) AS events
FROM ` + src + `
WHERE user_id <> '' AND install_id <> ''
GROUP BY project_id, user_id, install_id
ORDER BY project_id, user_id, install_id`
}

// identitiesCombine folds rows of identities (a stored file's and those
// derived on read) into one per (project, kind, id): the name of the
// newest set that has one, the earliest first_seen, the latest last_seen,
// the counts added.
func identitiesCombine(parts string) string {
	return `SELECT project_id, kind, id,
       arg_max(name, last_seen) FILTER (WHERE name IS NOT NULL) AS name,
       min(first_seen) AS first_seen, max(last_seen) AS last_seen,
       CAST(sum(views) AS BIGINT) AS views, CAST(sum(events) AS BIGINT) AS events
FROM (` + parts + `)
GROUP BY project_id, kind, id`
}

// aliasesCombine folds rows of aliases into one per (project, user_id,
// install_id), as identitiesCombine does.
func aliasesCombine(parts string) string {
	return `SELECT project_id, user_id, install_id, min(first_seen) AS first_seen, max(last_seen) AS last_seen,
       CAST(sum(events) AS BIGINT) AS events
FROM (` + parts + `)
GROUP BY project_id, user_id, install_id`
}

// eventsOf is files read as one relation with every fixed column at its
// type: a column a file lacks (written before it existed) reads NULL.
func eventsOf(files []string) string {
	return "(" + emptyRow() + " UNION ALL BY NAME SELECT * FROM read_parquet(" + sqlList(files) + ", union_by_name=true))"
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/archive -run 'TestSessions|TestIdentities|TestAliases|TestDerivedSelects' -count=1`
Expected: PASS. If `TestDerivedSelectsOverNothingAreTyped` shows `count_star()` columns as `BIGINT` but `duration_sec` as something else, check the `CAST(floor(epoch(ts)) AS BIGINT)` in `v`: `max(t) - min(t)` is BIGINT only if `t` is.

- [ ] **Step 5: Commit**

```bash
git add internal/archive/derive.go internal/archive/derive_test.go
git commit -m "feat(archive): the SELECTs of the sessions, identities and aliases tables"
```

---

### Task 3: which derived files are current

**Files:**
- Modify: `internal/archive/files.go` (`Files`, `ListFiles`; new `projectOf`, `current`, `joinSpans`, `Span`)
- Test: `internal/archive/files_test.go`

**Interfaces:**
- Consumes:
  - `sessionsDir`, `identitiesFile`, `aliasesFile` (Task 2);
  - `list`, `lastDay`, `dayFileName`, `monthFileName`, `logName`, `closedName`, `dumpingName` (existing).
- Produces:
  - `type Span struct{ From, To string }`: days, both ends included, `YYYY-MM-DD`.
  - `Files` gains:
    - `Sessions []string`: current session files;
    - `SessionGaps []Span`: days no current session file covers, joined where adjacent, in order;
    - `Identities, Aliases string`: the file when current, else `""`.
  - `type projectFiles struct { Files; deriveDays map[string][]string; deriveMonths map[string]string; orphans []string; identitiesDue bool }`.
  - `func projectOf(pdir string, names []string) (projectFiles, error)`. Task 4 uses its unexported fields; Task 5 uses `Files`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/archive/files_test.go` (imports gain `"time"`):

```go
// age sets the modification time of each name under dir to base plus its
// offset in minutes.
func age(t *testing.T, dir string, base time.Time, names map[string]int) {
	t.Helper()
	for n, m := range names {
		at := base.Add(time.Duration(m) * time.Minute)
		if err := os.Chtimes(filepath.Join(dir, n), at, at); err != nil {
			t.Fatal(err)
		}
	}
}

// TestListFilesKnowsWhichDerivedFilesAreCurrent: a derived file is read
// only while no events file it comes from is newer; every day it does not
// cover is a gap, adjacent gaps joined; a session file no events file
// backs is an orphan; identities and aliases are due when missing or
// older than any events file.
func TestListFilesKnowsWhichDerivedFilesAreCurrent(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir,
		"1/2026-08.parquet", "1/sessions/2026-08.parquet", // current
		"1/2026-09.parquet", "1/sessions/2026-09.parquet", // the month file is newer: stale
		"1/sessions/2026-09-30.parquet", // a day of a merged month: orphan
		"1/2026-10-01.parquet", "1/sessions/2026-10-01.parquet", // current
		"1/2026-10-02.parquet", "1/2026-10-02.1.parquet", "1/sessions/2026-10-02.parquet", // a segment is newer: stale
		"1/2026-10-03.parquet", // never derived
		"1/2026-10-04.log",     // a log: computed on read, never derived
		"1/2026-10-05.parquet", "1/2026-10-05.log", "1/sessions/2026-10-05.parquet", // a late log: the day is a gap
		"1/sessions/2026-07.parquet", // its month is gone: orphan
		"1/identities.parquet", "1/aliases.parquet",
	)
	p := filepath.Join(dir, "1")
	base := time.Now().Add(-24 * time.Hour) // older than every file touch left unaged
	age(t, p, base, map[string]int{
		"2026-08.parquet": 0, "sessions/2026-08.parquet": 1,
		"2026-09.parquet": 5, "sessions/2026-09.parquet": 1,
		"2026-10-01.parquet": 0, "sessions/2026-10-01.parquet": 1,
		"2026-10-02.parquet": 0, "sessions/2026-10-02.parquet": 1, "2026-10-02.1.parquet": 2,
		"2026-10-05.parquet": 0, "sessions/2026-10-05.parquet": 1,
		"identities.parquet": 3, "aliases.parquet": 10,
		"2026-10-03.parquet": 4,
	})
	names, err := list(p)
	if err != nil {
		t.Fatal(err)
	}
	pf, err := projectOf(p, names)
	if err != nil {
		t.Fatal(err)
	}
	in := func(names ...string) []string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = filepath.Join(p, n)
		}
		return out
	}
	if want := in("sessions/2026-08.parquet", "sessions/2026-10-01.parquet"); !eq(pf.Sessions, want) {
		t.Errorf("Sessions = %v, want %v", pf.Sessions, want)
	}
	if want := []Span{{"2026-09-01", "2026-09-30"}, {"2026-10-02", "2026-10-05"}}; fmt.Sprint(pf.SessionGaps) != fmt.Sprint(want) {
		t.Errorf("SessionGaps = %v, want %v", pf.SessionGaps, want)
	}
	if fmt.Sprint(pf.deriveMonths) != fmt.Sprint(map[string]string{"2026-09": filepath.Join(p, "2026-09.parquet")}) {
		t.Errorf("deriveMonths = %v, want 2026-09 only", pf.deriveMonths)
	}
	wantDays := map[string][]string{
		"2026-10-02": in("2026-10-02.1.parquet", "2026-10-02.parquet"),
		"2026-10-03": in("2026-10-03.parquet"),
	}
	if fmt.Sprint(pf.deriveDays) != fmt.Sprint(wantDays) {
		t.Errorf("deriveDays = %v, want %v", pf.deriveDays, wantDays)
	}
	if want := in("sessions/2026-07.parquet", "sessions/2026-09-30.parquet"); !eq(pf.orphans, want) {
		t.Errorf("orphans = %v, want %v", pf.orphans, want)
	}
	// identities.parquet is older than 2026-10-03.parquet; aliases.parquet is newer than every events file.
	if pf.Identities != "" || pf.Aliases != filepath.Join(p, "aliases.parquet") || !pf.identitiesDue {
		t.Errorf("Identities %q, Aliases %q, due %v; want stale, current, due", pf.Identities, pf.Aliases, pf.identitiesDue)
	}
}

// TestListFilesDerivedWithoutEvents: a project with only logs has every
// log's day as a gap and nothing to derive; derived files of a project
// with no events file left are orphans.
func TestListFilesDerivedWithoutEvents(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "1/2026-10-05.log", "1/2026-10-06.log", "1/identities.parquet", "1/aliases.parquet")
	p := filepath.Join(dir, "1")
	names, err := list(p)
	if err != nil {
		t.Fatal(err)
	}
	pf, err := projectOf(p, names)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(pf.SessionGaps) != fmt.Sprint([]Span{{"2026-10-05", "2026-10-06"}}) || len(pf.deriveDays)+len(pf.deriveMonths) != 0 {
		t.Errorf("gaps %v, derive %v %v; want one span and nothing to derive", pf.SessionGaps, pf.deriveDays, pf.deriveMonths)
	}
	if pf.identitiesDue || !eq(pf.orphans, []string{filepath.Join(p, "aliases.parquet"), filepath.Join(p, "identities.parquet")}) {
		t.Errorf("due %v, orphans %v; want not due, both files orphans", pf.identitiesDue, pf.orphans)
	}
}
```

Add `"fmt"` to the imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/archive -run 'TestListFiles' -count=1`
Expected: FAIL to compile, `undefined: projectOf`.

- [ ] **Step 3: Write the listing**

Replace `internal/archive/files.go`'s `Files` and `ListFiles` with the following, and add the helpers. Imports: `errors`, `fmt`, `os`, `path/filepath`, `regexp`, `slices`, `strconv`, `strings`, `github.com/dmtrkzntsv/twillingate/internal/shared/civil`.

```go
// Files is what a reader sees of one project's directory: the Parquet
// files that hold its final rows, the logs that hold the rest (open,
// rotated, or claimed by a dump not done yet), each in name order, and
// the derived files current with them (derive.go).
type Files struct {
	Parquet []string
	Logs    []string
	// Sessions are the session files no events file they come from is
	// newer than; SessionGaps the days none of them covers (a log's day, a
	// day or month whose session file is missing or stale), which a reader
	// derives from the events.
	Sessions    []string
	SessionGaps []Span
	// Identities and Aliases are the derived files when current, else "".
	Identities, Aliases string
}

// Span is a range of days, both ends included, as YYYY-MM-DD.
type Span struct{ From, To string }

// projectFiles is Files and what the daily pass has to derive (Archive.derive).
type projectFiles struct {
	Files
	deriveDays    map[string][]string // a day whose session file is missing or stale: its day file and segments
	deriveMonths  map[string]string   // a month whose session file is missing or stale: its month file
	orphans       []string            // derived files no events file backs any more
	identitiesDue bool                // identities.parquet or aliases.parquet missing or stale
}

// ListFiles lists raw/ for a reader, by project id, leaving out what the
// daily pass would make it count twice:
//   - a day file or segment once its month's file exists (a merge that
//     stopped before deleting its days left both; the month file holds them);
//   - a claimed log (<target>.dumping) once its target exists (a dump that
//     stopped before deleting its log).
//
// A .tmp file (a write in progress), any other name and a project with
// neither events file nor log are left out. A missing raw/ lists nothing;
// a project directory that vanishes while listing (a purge) is skipped.
func ListFiles(dir string) (map[int64]Files, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[int64]Files{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	out := map[int64]Files{}
	for _, e := range entries {
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil || !e.IsDir() {
			continue
		}
		pdir := filepath.Join(dir, e.Name())
		names, err := list(pdir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		pf, err := projectOf(pdir, names)
		if err != nil {
			return nil, err
		}
		if len(pf.Parquet)+len(pf.Logs) > 0 {
			out[id] = pf.Files
		}
	}
	return out, nil
}

// projectOf is one project's directory, whose entries are names (list),
// as ListFiles describes it, with what its derived files need. A file that
// vanishes while it looks (the pass at work) fails it with an error that
// wraps os.ErrNotExist, which a reader retries.
func projectOf(pdir string, names []string) (projectFiles, error) {
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	pf := projectFiles{deriveDays: map[string][]string{}, deriveMonths: map[string]string{}}
	days := map[string][]string{}   // a day not merged: its day file and segments
	monthFile := map[string]string{} // a merged month: its file
	logDays := map[string]bool{}
	for _, n := range names {
		p := filepath.Join(pdir, n)
		switch {
		case monthFileName.MatchString(n):
			pf.Parquet = append(pf.Parquet, p)
			monthFile[n[:7]] = p
		case dayFileName.MatchString(n):
			// A month merges only once all its days are dumped and past the
			// late window, so its file holds every day file beside it.
			if day := n[:10]; !have[day[:7]+".parquet"] {
				pf.Parquet = append(pf.Parquet, p)
				days[day] = append(days[day], p)
			}
		case logName.MatchString(n), closedName.MatchString(n):
			pf.Logs = append(pf.Logs, p)
			logDays[n[:10]] = true
		case dumpingName.MatchString(n):
			if !have[strings.TrimSuffix(n, ".dumping")] {
				pf.Logs = append(pf.Logs, p)
				logDays[n[:10]] = true
			}
		}
	}
	return pf, pf.derived(pdir, have, days, monthFile, logDays)
}

// derived fills in the derived files' side of pf (see Files and projectFiles).
func (pf *projectFiles) derived(pdir string, have map[string]bool, days map[string][]string, monthFile map[string]string, logDays map[string]bool) error {
	sdir := filepath.Join(pdir, sessionsDir)
	snames, err := list(sdir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var gaps []Span
	for _, month := range keysOf(monthFile) {
		s := filepath.Join(sdir, month+".parquet")
		ok, err := current(s, []string{monthFile[month]})
		if err != nil {
			return err
		}
		// A log in a merged month should not happen (a month merges once no
		// log is left and the late window is past); if one does, the month
		// is read from its rows, not from a file that misses them.
		late := false
		for day := range logDays {
			late = late || strings.HasPrefix(day, month+"-")
		}
		switch {
		case ok && !late:
			pf.Sessions = append(pf.Sessions, s)
		default:
			gaps = append(gaps, Span{month + "-01", lastDay(month).String()})
			if !ok && !late {
				pf.deriveMonths[month] = monthFile[month]
			}
		}
	}
	for _, day := range keysOf(days) {
		if logDays[day] {
			gaps = append(gaps, Span{day, day})
			continue
		}
		s := filepath.Join(sdir, day+".parquet")
		ok, err := current(s, days[day])
		if err != nil {
			return err
		}
		if ok {
			pf.Sessions = append(pf.Sessions, s)
		} else {
			gaps = append(gaps, Span{day, day})
			pf.deriveDays[day] = days[day]
		}
	}
	for day := range logDays {
		if _, ok := days[day]; !ok && monthFile[day[:7]] == "" {
			gaps = append(gaps, Span{day, day})
		}
	}
	pf.SessionGaps = joinSpans(gaps)
	for _, n := range snames {
		p := filepath.Join(sdir, n)
		switch {
		case monthFileName.MatchString(n):
			if monthFile[n[:7]] == "" {
				pf.orphans = append(pf.orphans, p)
			}
		case dayFileName.MatchString(n) && len(n) == len("2006-01-02.parquet"):
			if _, ok := days[n[:10]]; !ok {
				pf.orphans = append(pf.orphans, p)
			}
		}
	}
	for _, x := range []struct {
		name string
		to   *string
	}{{aliasesFile, &pf.Aliases}, {identitiesFile, &pf.Identities}} {
		p := filepath.Join(pdir, x.name)
		if len(pf.Parquet) == 0 {
			if have[x.name] {
				pf.orphans = append(pf.orphans, p)
			}
			continue
		}
		ok, err := current(p, pf.Parquet)
		if err != nil {
			return err
		}
		if ok {
			*x.to = p
		} else {
			pf.identitiesDue = true
		}
	}
	return nil
}

// current reports whether derived exists and no input is newer than it.
func current(derived string, inputs []string) (bool, error) {
	d, err := os.Stat(derived)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("archive: %w", err)
	}
	for _, in := range inputs {
		st, err := os.Stat(in)
		if err != nil {
			return false, fmt.Errorf("archive: %w", err)
		}
		if st.ModTime().After(d.ModTime()) {
			return false, nil
		}
	}
	return true, nil
}

// joinSpans sorts spans and joins those that overlap or touch.
func joinSpans(spans []Span) []Span {
	slices.SortFunc(spans, func(a, b Span) int { return strings.Compare(a.From, b.From) })
	var out []Span
	for _, s := range spans {
		if n := len(out); n > 0 && s.From <= nextDay(out[n-1].To) {
			out[n-1].To = max(out[n-1].To, s.To)
			continue
		}
		out = append(out, s)
	}
	return out
}

func nextDay(day string) string {
	d, err := civil.Parse(day)
	if err != nil {
		return day
	}
	return d.AddDays(1).String()
}

// keysOf is m's keys in order.
func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
```

Notes for the implementer:
- `dayFileName` matches both `2026-10-02.parquet` and `2026-10-02.1.parquet`. A session file is never a segment, so the orphan check also requires the plain length.
- `removeMonth` (`maintain.go`) deletes by prefix in the project directory only. Session files of a month it removed become orphans, which Task 4's derive deletes in the same pass.
- The orphans are listed in name order: `snames`, then the two top-level files with `aliases.parquet` first. The tests rely on that.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/archive -run 'TestListFiles|TestReader' -count=1`
Expected: PASS. The reader's tests still pass: `signature` and `build` read only `Parquet` and `Logs` so far.

- [ ] **Step 5: Commit**

```bash
git add internal/archive/files.go internal/archive/files_test.go
git commit -m "feat(archive): list which derived files are current with their events"
```

---

### Task 4: the daily pass derives

**Files:**
- Modify: `internal/archive/maintain.go` (`maintainProject`: a fifth step; new `derive`)
- Modify: `internal/archive/maintain_test.go:83-96` (`files` leaves the derived files out; new `derived`)
- Test: `internal/archive/derive_test.go`

**Interfaces:**
- Consumes:
  - `projectOf`, `projectFiles` (Task 3);
  - `sessionsSelect`, `identitiesSelect`, `aliasesSelect`, `eventsOf` and the file-name constants (Task 2);
  - `Archive.write`, `syncFile`, `list` (existing).
- Produces: `func (a *Archive) derive(ctx context.Context, dir string, force bool) error`, called from `maintainProject`. The file layout of Decision 1, which Task 5 reads.

- [ ] **Step 1: Change the `files` helper and write the failing tests**

In `internal/archive/maintain_test.go`, replace `files` with:

```go
// files lists project 1's events files and logs: the derived ones
// (derive.go) are derived's.
func (f fixture) files(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.log.Dir(), "1"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if n := e.Name(); n != sessionsDir && n != identitiesFile && n != aliasesFile {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// derived lists project 1's derived files, sessions/ ones prefixed.
func (f fixture) derived(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, n := range []string{aliasesFile, identitiesFile} {
		if exists(f.project1(n)) {
			names = append(names, n)
		}
	}
	entries, err := os.ReadDir(f.project1(sessionsDir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		names = append(names, sessionsDir+"/"+e.Name())
	}
	return names
}
```

Append to `internal/archive/derive_test.go` (imports gain `"context"`, `"os"`, `"time"`):

```go
// TestMaintainDerivesTheClosedDays: the pass writes a session file per
// dumped day, not for today's open log, and identities and aliases.
func TestMaintainDerivesTheClosedDays(t *testing.T) {
	f := newFixture(t)
	f.write(t,
		pageView("a1", "a", "2026-10-10T10:00:00Z", "/"), pageView("a2", "a", "2026-10-10T10:10:00Z", "/x"),
		named(pageView("b1", "u1", "2026-10-11T10:00:00Z", "/"), "u1", "Ada", "", ""),
		pageView("c1", "c", "2026-10-12T09:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	want := []string{aliasesFile, identitiesFile, "sessions/2026-10-10.parquet", "sessions/2026-10-11.parquet"}
	if got := f.derived(t); !eq(got, want) {
		t.Fatalf("derived = %v, want %v", got, want)
	}
	if got := f.values(t, "SELECT actor_id, views FROM read_parquet('"+f.project1("sessions/2026-10-10.parquet")+"')"); !eq(got, []string{"[a 2]"}) {
		t.Fatalf("sessions of 2026-10-10 = %v", got)
	}
	if got := f.values(t, "SELECT id, name, views FROM read_parquet('"+f.project1(identitiesFile)+"')"); !eq(got, []string{"[u1 Ada 1]"}) {
		t.Fatalf("identities = %v", got)
	}
}

// TestALateEventRederivesItsDay is Review Focus 1 (the pass's half): a
// segment newer than its day's session file makes the next pass rewrite
// it from the day file and the segment together.
func TestALateEventRederivesItsDay(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-10-11T10:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(f.project1("sessions/2026-10-11.parquet"), old, old); err != nil {
		t.Fatal(err)
	}
	f.write(t, pageView("b1", "b", "2026-10-11T23:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	if got := f.files(t); !eq(got, []string{"2026-10-11.1.parquet", "2026-10-11.parquet"}) {
		t.Fatalf("files = %v, want the day file and a segment", got)
	}
	if got := f.values(t, "SELECT actor_id FROM read_parquet('"+f.project1("sessions/2026-10-11.parquet")+"') ORDER BY actor_id"); !eq(got, []string{"[a]", "[b]"}) {
		t.Fatalf("sessions of 2026-10-11 = %v, want a's and the late b's", got)
	}
}

// TestAMergedMonthHasOneSessionsFile: when a month merges, its session
// days give way to one session file of the month.
func TestAMergedMonthHasOneSessionsFile(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-09-29T10:00:00Z", "/"), pageView("b1", "b", "2026-09-30T10:00:00Z", "/"))
	f.maintain(t, "2026-10-01") // dumps both days; September is not past the late window yet
	if got := f.derived(t); !eq(got, []string{aliasesFile, identitiesFile, "sessions/2026-09-29.parquet", "sessions/2026-09-30.parquet"}) {
		t.Fatalf("derived after the dump = %v", got)
	}
	f.maintain(t, "2026-10-12")
	if got := f.derived(t); !eq(got, []string{aliasesFile, identitiesFile, "sessions/2026-09.parquet"}) {
		t.Fatalf("derived after the merge = %v", got)
	}
	if got := f.values(t, "SELECT count(*) FROM read_parquet('"+f.project1("sessions/2026-09.parquet")+"')"); !eq(got, []string{"[2]"}) {
		t.Fatalf("sessions of September = %v, want 2", got)
	}
}

// TestAMonthMergedBeforeSessionsExistedGetsThem is Review Focus 2: an
// install upgraded from v0.18.0 has month and day files and no derived
// file; the first pass derives every one.
func TestAMonthMergedBeforeSessionsExistedGetsThem(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-09-29T10:00:00Z", "/"), pageView("b1", "b", "2026-10-10T10:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	for _, n := range []string{sessionsDir, identitiesFile, aliasesFile} {
		if err := os.RemoveAll(f.project1(n)); err != nil {
			t.Fatal(err)
		}
	}
	f.maintain(t, "2026-10-12")
	if got := f.derived(t); !eq(got, []string{aliasesFile, identitiesFile, "sessions/2026-09.parquet", "sessions/2026-10-10.parquet"}) {
		t.Fatalf("derived = %v", got)
	}
}

// TestRetentionTakesTheDerivedFilesAlong is Review Focus 3: a month that
// retention deletes takes its session file along, and identities lose its
// users although no events file is newer than them.
func TestRetentionTakesTheDerivedFilesAlong(t *testing.T) {
	f := newFixture(t)
	f.write(t, named(pageView("a1", "u1", "2026-07-10T10:00:00Z", "/"), "u1", "", "", ""),
		named(pageView("b1", "u2", "2026-10-10T10:00:00Z", "/"), "u2", "", "", ""))
	f.maintain(t, "2026-10-12")
	if got := f.values(t, "SELECT id FROM read_parquet('"+f.project1(identitiesFile)+"') ORDER BY id"); !eq(got, []string{"[u1]", "[u2]"}) {
		t.Fatalf("identities before = %v", got)
	}
	d, _ := civil.Parse("2026-10-12")
	if err := f.a.Maintain(context.Background(), d, 1, 60); err != nil {
		t.Fatal(err)
	}
	if got := f.derived(t); !eq(got, []string{aliasesFile, identitiesFile, "sessions/2026-10-10.parquet"}) {
		t.Fatalf("derived = %v, want July's sessions gone", got)
	}
	if got := f.values(t, "SELECT id FROM read_parquet('"+f.project1(identitiesFile)+"')"); !eq(got, []string{"[u2]"}) {
		t.Fatalf("identities after = %v, want u1 gone with July", got)
	}
}

// TestNoEventsFileNoDerivedFile: a project whose every month retention
// deleted keeps no derived file.
func TestNoEventsFileNoDerivedFile(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-07-10T10:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	d, _ := civil.Parse("2026-10-12")
	if err := f.a.Maintain(context.Background(), d, 1, 60); err != nil {
		t.Fatal(err)
	}
	if got := f.derived(t); len(got) != 0 {
		t.Fatalf("derived = %v, want none", got)
	}
}

// TestDeriveReadsFilesWrittenBeforeTheNewColumns is Review Focus 5: a day
// file without install_id, user_name and group_name (written by v0.18.0)
// beside a new one derives without a binder error, its names NULL.
func TestDeriveReadsFilesWrittenBeforeTheNewColumns(t *testing.T) {
	f := newFixture(t)
	f.write(t, named(evOf(1, "p1", "2026-10-10T10:00:00Z"), "u1", "", "", ""),
		named(evOf(1, "p2", "2026-10-11T10:00:00Z"), "u1", "Ada", "", ""))
	f.maintain(t, "2026-10-12")
	old := f.project1("2026-10-10.parquet")
	if err := f.eng.Exec(context.Background(), "COPY (SELECT * EXCLUDE (install_id, user_name, group_name) FROM read_parquet('"+old+
		"')) TO '"+old+".tmp' (FORMAT parquet)"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(old+".tmp", old); err != nil {
		t.Fatal(err)
	}
	f.maintain(t, "2026-10-12")
	if got := f.values(t, "SELECT id, name, events FROM read_parquet('"+f.project1(identitiesFile)+"')"); !eq(got, []string{"[u1 Ada 2]"}) {
		t.Fatalf("identities = %v", got)
	}
}
```

Add `"github.com/dmtrkzntsv/twillingate/internal/shared/civil"` to the imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/archive -run 'TestMaintainDerives|TestALateEvent|TestAMergedMonth|TestAMonthMerged|TestRetentionTakes|TestNoEventsFile|TestDeriveReads' -count=1`
Expected: FAIL. `TestMaintainDerivesTheClosedDays` reports `derived = [], want [aliases.parquet identities.parquet sessions/…]`.

- [ ] **Step 3: Derive in the pass**

In `internal/archive/maintain.go` `maintainProject`, note when retention removes a month, and add the fifth step before the final `return`:

```go
	keep := today.AddDays(-retentionDays)
	removed := false
	for _, month := range months(names) {
		last := lastDay(month)
		if !keep.Before(last) {
			if err := removeMonth(dir, names, month); err != nil {
				errs = append(errs, err)
			}
			removed = true
			continue
		}
		if last.AddDays(lateDays).Before(today) && !open(names, month) {
			if err := a.merge(ctx, dir, names, month); err != nil {
				errs = append(errs, err)
			}
		}
	}
	// 5. Derive the sessions, identities and aliases (raw-first D16–D18)
	// from the files as the steps above left them.
	if err := a.derive(ctx, dir, removed); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
```

Add `derive` to `maintain.go` (imports gain `maps`):

```go
// derive writes every derived file projectOf finds missing or stale and
// removes the ones no events file backs. force rewrites identities and
// aliases even when current: the pass deleted files (retention), which no
// modification time shows. A failed write is collected and the rest go on.
func (a *Archive) derive(ctx context.Context, dir string, force bool) error {
	names, err := list(dir)
	if err != nil {
		return err
	}
	pf, err := projectOf(dir, names)
	if err != nil {
		return err
	}
	var errs []error
	sdir := filepath.Join(dir, sessionsDir)
	if len(pf.deriveDays)+len(pf.deriveMonths) > 0 {
		if err := os.MkdirAll(sdir, 0o750); err != nil {
			return fmt.Errorf("archive: %w", err)
		}
		if err := syncFile(dir); err != nil {
			return err
		}
	}
	for _, day := range slices.Sorted(maps.Keys(pf.deriveDays)) {
		if err := a.write(ctx, "COPY ("+sessionsSelect(eventsOf(pf.deriveDays[day]))+") TO ", filepath.Join(sdir, day+".parquet")); err != nil {
			errs = append(errs, err)
		}
	}
	for _, month := range slices.Sorted(maps.Keys(pf.deriveMonths)) {
		if err := a.write(ctx, "COPY ("+sessionsSelect(eventsOf([]string{pf.deriveMonths[month]}))+") TO ", filepath.Join(sdir, month+".parquet")); err != nil {
			errs = append(errs, err)
		}
	}
	for _, p := range pf.orphans {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("archive: %w", err))
		}
	}
	if len(pf.Parquet) > 0 && (force || pf.identitiesDue) {
		src := eventsOf(pf.Parquet)
		if err := a.write(ctx, "COPY ("+identitiesSelect(src)+") TO ", filepath.Join(dir, identitiesFile)); err != nil {
			errs = append(errs, err)
		}
		if err := a.write(ctx, "COPY ("+aliasesSelect(src)+") TO ", filepath.Join(dir, aliasesFile)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
```

Also update `Archive`'s and `Maintain`'s doc comments to name the fifth step: "…delete months entirely older than retentionDays, and derive the sessions, identities and aliases (derive.go)".

- [ ] **Step 4: Run the archive tests to verify they pass**

Run: `go test ./internal/archive -count=1`
Expected: PASS, the PR 3 and PR 4 tests included.
- `TestOneFailingDumpStopsNothingElse` expects `maintainProject` to return an error and its listed `files`. Its derive runs too, and the listing leaves derived files out.
- If a PR 3 test lists `f.files` and fails on `sessions`, it bypassed the helper. Use `f.files` there.

- [ ] **Step 5: Commit**

```bash
git add internal/archive/maintain.go internal/archive/maintain_test.go internal/archive/derive_test.go
git commit -m "feat(archive): derive sessions, identities and aliases in the daily pass"
```

---

### Task 5: the reader reads sessions, identities and aliases

**Files:**
- Modify: `internal/archive/reader.go` (`signature`, `build`, `missingTable`; new `sessionsView`, `derivedView`, `inProjects`)
- Modify: `internal/archive/archive.go` (package doc)
- Test: `internal/archive/reader_test.go`
- Docs:
  - `docs/twillingate.md`: "Writing SQL against the event files", and the `query` row of the tools table;
  - `docs/deployment.md`: the event files paragraph (line ~426) and "Back up and restore";
  - `CLAUDE.md`: the `internal/archive/` line of the Layout.

**Interfaces:**
- Consumes:
  - `Files.Sessions`, `Files.SessionGaps`, `Files.Identities`, `Files.Aliases`, `Span` (Task 3);
  - `sessionsSelect`, `identitiesSelect`, `aliasesSelect`, `identitiesCombine`, `aliasesCombine` (Task 2).
- Produces: the views `sessions`, `identities` and `aliases` in the child, beside `events`, `views`, `product` and `measures`, with the columns of Task 2. 5d's widgets read them by these names.

- [ ] **Step 1: Write the failing tests**

Append to `internal/archive/reader_test.go` (imports gain `"time"`):

```go
// TestReaderSessionsJoinStoredAndLive: a dumped and derived day reads from
// its session file, today from the open log.
func TestReaderSessionsJoinStoredAndLive(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-10-11T10:00:00Z", "/"), pageView("a2", "a", "2026-10-11T11:00:00Z", "/"),
		pageView("c1", "c", "2026-10-12T09:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	r := f.reader(t)
	if got := ask(t, r, Rules{}, "SELECT CAST(day AS VARCHAR), count(*) FROM sessions GROUP BY day ORDER BY day"); got != "[[2026-10-11 2] [2026-10-12 1]]" {
		t.Fatalf("sessions per day = %s", got)
	}
}

// TestReaderDerivesAStaleDayOnRead is Review Focus 1 (the reader's half): a
// day whose session file is older than its events, and a day with a late
// log beside its file, read from their events, each view once.
func TestReaderDerivesAStaleDayOnRead(t *testing.T) {
	f := newFixture(t)
	f.write(t, pageView("a1", "a", "2026-10-10T10:00:00Z", "/"), pageView("b1", "b", "2026-10-11T10:00:00Z", "/"))
	f.maintain(t, "2026-10-12")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(f.project1("sessions/2026-10-10.parquet"), old, old); err != nil {
		t.Fatal(err)
	}
	f.write(t, pageView("b2", "b2", "2026-10-11T12:00:00Z", "/")) // late: a log beside the day file
	r := f.reader(t)
	if got := ask(t, r, Rules{}, "SELECT CAST(day AS VARCHAR), count(*) FROM sessions GROUP BY day ORDER BY day"); got != "[[2026-10-10 1] [2026-10-11 2]]" {
		t.Fatalf("sessions per day = %s, want each view once", got)
	}
}

// TestReaderWithoutDerivedFiles is Review Focus 2 (the reader's half): with
// day and month files and no derived file yet, the tables read the same as
// after the pass derived them.
func TestReaderWithoutDerivedFiles(t *testing.T) {
	f := newFixture(t)
	f.write(t, named(pageView("a1", "u1", "2026-09-29T10:00:00Z", "/"), "u1", "Ada", "", ""),
		named(pageView("b1", "u2", "2026-10-10T10:00:00Z", "/"), "u2", "", "", ""))
	f.maintain(t, "2026-10-12")
	q := []string{
		"SELECT CAST(day AS VARCHAR), actor_id FROM sessions ORDER BY day",
		"SELECT id, name, views FROM identities ORDER BY id",
	}
	r := f.reader(t)
	var after []string
	for _, s := range q {
		after = append(after, ask(t, r, Rules{}, s))
	}
	for _, n := range []string{sessionsDir, identitiesFile, aliasesFile} {
		if err := os.RemoveAll(f.project1(n)); err != nil {
			t.Fatal(err)
		}
	}
	for i, s := range q {
		if got := ask(t, r, Rules{}, s); got != after[i] {
			t.Errorf("%s: %s without derived files, %s with them", s, got, after[i])
		}
	}
	if after[0] != "[[2026-09-29 u1] [2026-10-10 u2]]" || after[1] != "[[u1 Ada 1] [u2 <nil> 1]]" {
		t.Errorf("with derived files: %v", after)
	}
}

// TestReaderIdentitiesFoldStoredAndToday is Review Focus 4: the stored file
// and today's log fold into one row per id; today's name wins, an id
// named only before keeps its name, counts add, first_seen stays.
func TestReaderIdentitiesFoldStoredAndToday(t *testing.T) {
	f := newFixture(t)
	f.write(t, named(evOf(1, "p1", "2026-10-11T09:00:00Z"), "u1", "Ada", "", ""),
		named(evOf(1, "p2", "2026-10-11T09:00:00Z"), "u2", "Bo", "", ""))
	f.maintain(t, "2026-10-12")
	f.write(t, named(evOf(1, "p3", "2026-10-12T09:00:00Z"), "u1", "Ada L.", "", ""),
		named(evOf(1, "p4", "2026-10-12T09:00:00Z"), "u2", "", "", ""))
	r := f.reader(t)
	got := ask(t, r, Rules{}, "SELECT id, name, CAST(first_seen AS VARCHAR), CAST(last_seen AS VARCHAR), events FROM identities ORDER BY id")
	want := "[[u1 Ada L. 2026-10-11 09:00:00 2026-10-12 09:00:00 2] [u2 Bo 2026-10-11 09:00:00 2026-10-12 09:00:00 2]]"
	if got != want {
		t.Fatalf("identities = %s\nwant         %s", got, want)
	}
	// A stale file is not read: the project's every event is.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(f.project1(identitiesFile), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f.project1("2026-10-11.parquet"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := ask(t, r, Rules{}, "SELECT id, name, CAST(first_seen AS VARCHAR), CAST(last_seen AS VARCHAR), events FROM identities ORDER BY id"); got != want {
		t.Fatalf("identities from a stale file = %s\nwant                          %s", got, want)
	}
}

// TestReaderAliases: stored pairs and today's fold into one row per pair.
func TestReaderAliases(t *testing.T) {
	f := newFixture(t)
	pair := func(e store.Event) store.Event { e.UserID, e.InstallID = "u1", "i1"; return e }
	f.write(t, pair(evOf(1, "p1", "2026-10-11T09:00:00Z")))
	f.maintain(t, "2026-10-12")
	f.write(t, pair(evOf(1, "p2", "2026-10-12T09:00:00Z")))
	if got := ask(t, f.reader(t), Rules{}, "SELECT user_id, install_id, events FROM aliases"); got != "[[u1 i1 2]]" {
		t.Fatalf("aliases = %s", got)
	}
}

// TestReaderDerivedTablesOnAnEmptyDirectory: with nothing under raw/ the
// three tables exist, empty, with their columns.
func TestReaderDerivedTablesOnAnEmptyDirectory(t *testing.T) {
	f := newFixture(t)
	r := f.reader(t)
	for _, table := range []string{"sessions", "identities", "aliases"} {
		if got := ask(t, r, Rules{}, "SELECT count(*) FROM "+table); got != "[[0]]" {
			t.Errorf("%s: %s, want [[0]]", table, got)
		}
	}
	if got := ask(t, r, Rules{}, "SELECT count(*) FROM (DESCRIBE sessions)"); got != "[[17]]" {
		t.Errorf("sessions has %s columns, want 17", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/archive -run 'TestReaderSessions|TestReaderDerives|TestReaderWithout|TestReaderIdentities|TestReaderAliases|TestReaderDerivedTables' -count=1`
Expected: FAIL with `Catalog Error: Table with name sessions does not exist!`.

- [ ] **Step 3: Build the three views**

In `internal/archive/reader.go`:

`missingTable` names the new views, so a replaced child's error is retried:

```go
var missingTable = regexp.MustCompile(`(?:Table|View) with name "?(?:events|views|product|measures|sessions|identities|aliases|_tw_[A-Za-z0-9_]*)"? does not exist`)
```

`signature` also covers the derived files (a pass that wrote one changes what the tables read):

```go
	for _, id := range sortedIDs(files) {
		f := files[id]
		paths := append(slices.Clone(f.Parquet), f.Logs...)
		paths = append(paths, f.Sessions...)
		for _, p := range []string{f.Identities, f.Aliases} {
			if p != "" {
				paths = append(paths, p)
			}
		}
		for _, p := range paths {
			st, err := os.Stat(p)
			if err != nil {
				return "", fmt.Errorf("archive: %w", err)
			}
			fmt.Fprintf(h, "%s\x00%d\x00%d\n", p, st.Size(), st.ModTime().UnixNano())
		}
	}
```

`build` collects the derived side with the rest and defines the views in its transaction. Replace its first loop with:

```go
	var parquet, logs, sessions, idFiles, alFiles, gaps []string
	var idCurrent, idStale, alCurrent, alStale []int64
	for _, id := range sortedIDs(files) {
		f := files[id]
		parquet = append(parquet, f.Parquet...)
		logs = append(logs, f.Logs...)
		sessions = append(sessions, f.Sessions...)
		for _, g := range f.SessionGaps {
			gaps = append(gaps, fmt.Sprintf("(project_id = %d AND day BETWEEN DATE %s AND DATE %s)", id, quote(g.From), quote(g.To)))
		}
		if f.Identities != "" {
			idFiles, idCurrent = append(idFiles, f.Identities), append(idCurrent, id)
		} else {
			idStale = append(idStale, id)
		}
		if f.Aliases != "" {
			alFiles, alCurrent = append(alFiles, f.Aliases), append(alCurrent, id)
		} else {
			alStale = append(alStale, id)
		}
	}
```

and in the final `Exec`, after the `measures` view:

```go
		"CREATE OR REPLACE VIEW sessions AS "+sessionsView(sessions, gaps)+";\n"+
		"CREATE OR REPLACE VIEW identities AS "+derivedView(identitiesSelect, identitiesCombine, idFiles, idCurrent, idStale)+";\n"+
		"CREATE OR REPLACE VIEW aliases AS "+derivedView(aliasesSelect, aliasesCombine, alFiles, alCurrent, alStale)+";\n"+
```

Add the helpers (imports gain `strconv`):

```go
// sessionsView is the sessions table: the current session files, and the
// sessions of the gaps (the days no current file covers, as conditions on
// project_id and day) derived from _tw_all. The typed empty SELECT fixes
// every column's type when there is neither.
func sessionsView(files, gaps []string) string {
	union := []string{"SELECT * FROM (" + sessionsSelect("("+emptyRow()+")") + ")"}
	if len(files) > 0 {
		union = append(union, "SELECT * FROM read_parquet("+sqlList(files)+", union_by_name=true)")
	}
	if len(gaps) > 0 {
		union = append(union, "SELECT * FROM ("+sessionsSelect("(SELECT * FROM _tw_all WHERE "+strings.Join(gaps, " OR ")+")")+")")
	}
	return strings.Join(union, " UNION ALL BY NAME ")
}

// derivedView is identities or aliases (sel and combine, derive.go): the
// current files, one per project in current; what they lack, derived on
// read from the open logs of those projects and from every event of the
// projects in stale; folded into one row per key.
func derivedView(sel, combine func(string) string, files []string, current, stale []int64) string {
	parts := []string{"SELECT * FROM (" + sel("("+emptyRow()+")") + ")"}
	if len(files) > 0 {
		parts = append(parts, "SELECT * FROM read_parquet("+sqlList(files)+", union_by_name=true)")
	}
	live := "(SELECT * FROM _tw_all WHERE " + inProjects(stale) +
		" UNION ALL BY NAME SELECT * EXCLUDE (v) FROM _tw_hot WHERE " + inProjects(current) + ")"
	parts = append(parts, "SELECT * FROM ("+sel(live)+")")
	return combine(strings.Join(parts, " UNION ALL BY NAME "))
}

// inProjects is a condition on project_id: one of ids (false for none).
func inProjects(ids []int64) string {
	if len(ids) == 0 {
		return "false"
	}
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return "project_id IN (" + strings.Join(s, ", ") + ")"
}
```

Update the `Reader` doc comment: "…as the tables events, views, product and measures (raw-first D4, D8), and sessions, identities and aliases (D16–D18): the derived files that are current, the rest derived on read…".

Update `archive.go`'s package doc: add the derived files to the layout block, and change "Reader answers SQL over the files and open logs for the query tool's files source" to include "and the tables derived from them".

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/archive ./internal/api ./internal/app -count=1`
Expected: PASS.

- [ ] **Step 5: Document the tables**

`docs/twillingate.md`, "Writing SQL against the event files": replace the paragraph starting "Four tables: `events`, every event of every family…" (up to "Every table has:") with:

```markdown
Seven tables. `events` holds every event of every family; `views`,
`product` and `measures` hold its rows of one family each. Three more are
derived from the events (below the column list). The `_tw_*` tables are
the reader's own and hold rows before the rules apply: query these seven.
`events`, `views`, `product` and `measures` have:
```

After the bullet list that ends with "**Filter on `project_id` and `day` or `ts`:** …", add:

```markdown
The derived tables are rebuilt by the daily pass from the files, and
today's part (and any day the pass has not caught up with) is computed as
you query. They describe the events as stored: rules do not apply to them.

`sessions`, one row per session of views:

| Column | Type | |
| --- | --- | --- |
| `project_id`, `day` | BIGINT, DATE | sessions are cut at midnight UTC |
| `kind`, `actor_id`, `actor_kind` | VARCHAR | as on the views |
| `session_id` | VARCHAR | the id the client declared, else NULL |
| `started_at`, `ended_at` | TIMESTAMP | the first and last view |
| `duration_sec`, `views` | BIGINT | seconds between them; 1 view is a bounce |
| `entry_host`, `entry_path`, `exit_path` | VARCHAR | the first view's host and path, the last view's path |
| `referrer_source`, `utm_source`, `utm_medium`, `utm_campaign` | VARCHAR | the first view's |

A view with a `session_id` belongs to that session; any other starts a
new one after 30 minutes without a view by the same actor, as the Views
dashboard counts them.

`identities`, one row per user (`kind` `user`, from `user_id`) and per
group (`kind` `group`, from `group_id`): `project_id`, `kind`, `id`,
`name` (sent with the latest event that carried one, NULL if never),
`first_seen`, `last_seen` (TIMESTAMP), `views` and `events` (its views and
product events). First and last seen reach back as far as the files do.

`aliases`, one row per `user_id` and `install_id` sent on the same event:
`project_id`, `user_id`, `install_id`, `first_seen`, `last_seen`, `events`.
A person's events across devices, before and after signing in:
`WHERE user_id = 'u1' OR install_id IN (SELECT install_id FROM aliases WHERE user_id = 'u1')`.
```

In the tools table, change the `query` row's last sentence to "`files` reads the raw events and the tables derived from them through DuckDB: see [Writing SQL against the event files](#writing-sql-against-the-event-files)".

`docs/deployment.md`, the paragraph starting "The ingest process also keeps every event as files under `EVENTS_DIR`": after "…into a month file once the month is final.", add:

```markdown
The same pass derives three small tables from them in each project's directory, `sessions/`, `identities.parquet` and `aliases.parquet`, rewritten when the events under them change.
```

In "Back up and restore", after the sentence that ends "…then `raw/` with any tool (`cp -a`, `rsync`, `restic`).", add:

```markdown
The derived files (`sessions/`, `identities.parquet`, `aliases.parquet` in each project's directory) may be left out: the next pass rebuilds what is missing.
```

`CLAUDE.md`, Layout: change the `internal/archive/` line to

```
internal/archive/    raw events as files under raw/: the per-day NDJSON log, the dump to Parquet, the month merge, retention by file, the derived sessions, identities and aliases, and the tables query's files source reads
```

- [ ] **Step 6: Commit**

```bash
git add internal/archive/reader.go internal/archive/reader_test.go internal/archive/archive.go docs/twillingate.md docs/deployment.md CLAUDE.md
git commit -m "feat(archive): read sessions, identities and aliases through the files source"
```

---

### Task 6: the derived tables give SQLite's numbers; upgrade notes

**Files:**
- Test: `internal/app/archive_parity_test.go` (new `TestDerivedTablesMatchSQLite`)
- Docs: `deploy/UPGRADES.md`

**Interfaces:**
- Consumes:
  - `archive.OpenLog`, `archive.New(...).Maintain` and `archive.NewReader(...).Query` (existing);
  - `pipeline.Sinks`;
  - `store.Store.UpsertIdentities` and `AggregateViewDay`;
  - the `sessions` and `identities` tables (Task 5).
- Produces: nothing new. This pins the 5a "Done when" of the roadmap.

- [ ] **Step 1: Write the test**

Append to `internal/app/archive_parity_test.go`:

```go
// TestDerivedTablesMatchSQLite: the sessions the files give a day are the
// ones agg_views_daily counts (sessions, bounces, seconds per kind), and
// identities name and count each user and group as identities and
// v_identity_daily do; before the pass (from the log) and after it (from
// the derived files).
func TestDerivedTablesMatchSQLite(t *testing.T) {
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

	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	n := 0
	ev := func(fam store.Family, kind, actor, user, session string, at time.Duration) store.Event {
		n++
		e := store.Event{ID: fmt.Sprintf("00000000-0000-4000-b000-%012d", n), ProjectID: 1, Family: fam, Kind: kind,
			ActorID: actor, ActorKind: store.ActorConnection, SessionID: session, TS: day.Add(at), ReceivedAt: day.Add(at)}
		if user != "" {
			e.ActorKind, e.UserID, e.UserName = store.ActorUser, user, "Name of "+user
		}
		if fam == store.FamilyViews {
			e.EventName, e.Path = "$page_view", "/"
		} else {
			e.EventName = "signup"
		}
		return e
	}
	h, m := time.Hour, time.Minute
	grouped := ev(store.FamilyProduct, "ios", "u1", "u1", "", 9*h+m)
	grouped.GroupID, grouped.GroupName = "g1", "Acme"
	evs := []store.Event{
		ev(store.FamilyViews, "web", "a", "", "", 10*h), ev(store.FamilyViews, "web", "a", "", "", 10*h+10*m),
		ev(store.FamilyViews, "web", "a", "", "", 10*h+50*m), // a new session after 40 minutes
		ev(store.FamilyViews, "web", "b", "", "", 12*h), // a bounce
		ev(store.FamilyViews, "ios", "u1", "u1", "s1", 9*h), ev(store.FamilyViews, "ios", "u1", "u1", "s1", 11*h),
		grouped,
		ev(store.FamilyProduct, "web", "u2", "u2", "", 14*h),
	}
	if err := (pipeline.Sinks{st, log}).WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	// As ingest does beside the events (handlers.go, identityNames).
	if err := st.UpsertIdentities(ctx, []store.Identity{
		{ProjectID: 1, Kind: store.KindUser, ID: "u1", Name: "Name of u1"},
		{ProjectID: 1, Kind: store.KindUser, ID: "u2", Name: "Name of u2"},
		{ProjectID: 1, Kind: store.KindGroup, ID: "g1", Name: "Acme"},
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", "file:"+filepath.Join(data, "twillingate.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	sqliteRows := func(q string) string {
		t.Helper()
		rows, err := raw.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var out [][]any
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				if b, ok := v.([]byte); ok {
					vals[i] = string(b)
				}
			}
			out = append(out, vals)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(out)
	}
	// v_identity_daily's live half reads the raw rows, which the views
	// rollup below deletes: read it first.
	wantIdentities := sqliteRows(`SELECT d.kind, d.id, i.name, SUM(d.views), SUM(d.events)
		FROM v_identity_daily d LEFT JOIN identities i ON i.project_id = d.project_id AND i.kind = d.kind AND i.id = d.id
		WHERE d.project_id = 1 GROUP BY d.kind, d.id, i.name ORDER BY d.kind, d.id`)
	if wantIdentities != "[[group g1 Acme 0 1] [user u1 Name of u1 2 1] [user u2 Name of u2 0 1]]" {
		t.Fatalf("SQLite identities = %s", wantIdentities)
	}
	if err := st.AggregateViewDay(ctx, 1, civil.DateOf(day), 0); err != nil {
		t.Fatal(err)
	}
	wantSessions := sqliteRows(`SELECT kind, sessions, bounces, duration_sec FROM agg_views_daily
		WHERE project_id = 1 AND day = '2026-10-11' ORDER BY kind`)
	if wantSessions != "[[ios 1 0 7200] [web 3 2 600]]" {
		t.Fatalf("SQLite sessions = %s", wantSessions)
	}

	self, _ := os.Executable()
	eng, err := engine.New(engine.Options{Path: self, Env: []string{appEngineChild + "=1"},
		AllowedDirs: []string{log.Dir()}, SpillRoot: filepath.Join(data, "engine", "spill"), SlotDir: filepath.Join(data, "engine", "slots"),
		MaxChildren: 1, MemoryLimit: "256MB", Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
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
	check := func(when string) {
		t.Helper()
		got := ask(`SELECT kind, count(*), count(*) FILTER (WHERE views = 1), CAST(sum(duration_sec) AS BIGINT)
			FROM sessions WHERE project_id = 1 AND day = DATE '2026-10-11' GROUP BY kind ORDER BY kind`)
		if got != wantSessions {
			t.Errorf("%s: files sessions = %s, SQLite %s", when, got, wantSessions)
		}
		got = ask(`SELECT kind, id, name, views, events FROM identities WHERE project_id = 1 ORDER BY kind, id`)
		if got != wantIdentities {
			t.Errorf("%s: files identities = %s, SQLite %s", when, got, wantIdentities)
		}
	}
	check("from the log")
	pass, _ := civil.Parse("2026-10-12")
	if err := archive.New(log, eng, slog.Default()).Maintain(ctx, pass, 1, 400); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(log.Dir(), "1", "sessions", "2026-10-11.parquet")); err != nil {
		t.Fatalf("the pass derived no session file: %v", err)
	}
	check("from the derived files")
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/app -run TestDerivedTablesMatchSQLite -count=1 -v`
Expected: PASS.
- A failure on `wantIdentities` or `wantSessions` means the seeded rows don't give the numbers written above. Fix the seed or the literal after reading `v_identity_daily` (`025_live_halves.sql`) and `viewSessionsCTE`, never the derived SQL.
- A failure in `check` is a real divergence between `sessionsSelect` or `identitiesSelect` and SQLite. Fix the derived SQL.
- If the numbers print as `json.Number`, they still print bare (`fmt.Sprint`). A HUGEINT that prints quoted needs the `CAST(… AS BIGINT)` the query already has; add it wherever one is missing.

- [ ] **Step 3: Write the upgrade notes**

Append to `deploy/UPGRADES.md`:

```markdown
### Upgrading to the derived tables (no migration)

No migration, nothing to run. What changes:

- The event files keep three more fields on every event: `install_id`,
  `user_name` and `group_name`. The names your clients send beside
  `$user_id` and `$group_id` now stay with each event under `raw/` for
  `RETENTION_DAYS`, not only as the latest name in the database: count
  them as personal data in the files too.
- The daily pass derives `sessions/`, `identities.parquet` and
  `aliases.parquet` in each project's directory under `raw/`. The first
  pass after the upgrade derives them for every file already there, a few
  seconds per month of events at the cap. They need no backup: a pass
  rebuilds what is missing.
- `query` with `source: "files"` reads three more tables, `sessions`,
  `identities` and `aliases` (`docs/twillingate.md`, "Writing SQL against
  the event files"). Events stored before the upgrade have no install id
  or names, so `aliases` and the names in `identities` start from the
  upgrade.

Rollback: an older binary ignores the derived files and reads the new
fields as extra columns; delete the derived files if you go back for good.
```

- [ ] **Step 4: Run the whole check**

Run: `make check`
Expected: PASS (vet, the coverage gate, the SQLite-free packages under `-race`). A coverage drop below the gate comes from untested error branches in `derive` or `projectOf`. Add a test for the branch the report names, for example a `sessions` path that is a file, not a directory, which makes `list` fail.

- [ ] **Step 5: Commit**

```bash
git add internal/app/archive_parity_test.go deploy/UPGRADES.md
git commit -m "test(app): the derived sessions and identities give SQLite's numbers"
```

- [ ] **Step 6: Push and open the draft PR**

Title (the squash subject is the changelog line):
`feat: keep ids and names on every event and derive sessions, identities and aliases from the files`.

The body leads with the problem: 5d's dashboards need sessions without sorting a year of views per load (D16), and identities without SQLite (D17, D18). Then come this plan's Decisions, for review.
