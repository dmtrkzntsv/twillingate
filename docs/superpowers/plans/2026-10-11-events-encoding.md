# Events Encoding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store raw events with blob ids, integer timestamps and dictionary ids (text columns, attribute keys and attribute values), behind `raw_*` views that decode to today's columns, and limit widget SQL to `raw_*`, `v_*` and `agg_*`.

**Architecture:** One migration (041) rebuilds `events` encoded, creates the `strings` and `attribute_values` dictionaries, gives the attribute catalog integer ids, and recreates every view. Ingest encodes inside its existing write transaction through a per-transaction dictionary resolver; a nightly cleanup deletes dictionary rows whose `last_seen_day` is below the oldest raw day. Widget SQL runs on its own read handle where only the allowed names exist (temporary views over the database attached under a private schema name).

**Tech Stack:** Go 1.26, modernc.org/sqlite v1.57 (SQLite with `unhex`, `unixepoch`, JSON1, JSONB), React web app (only its e2e suite is touched indirectly).

**Spec:** `docs/superpowers/specs/2026-10-11-events-encoding-design.md` (decisions D1–D18; read it before any task).

## Global Constraints

- Go is not on PATH: prefix every Go/make command with `export PATH=/usr/local/go/bin:$PATH`.
- `/tmp` has a per-user quota that fills: run Go with `GOTMPDIR=$HOME/.cache/gotmp-enc` (`mkdir -p` it first).
- `make check` (vet, coverage gate, race on SQLite-free packages) must pass at the end of every task; it needs Node 22 (`make ui` runs first).
- Tests needing a migrated database copy one (`newTestDB`, `newTestDBAt`, `openSnapshot`, `storetest.Copy`); never migrate per test.
- Commits: Conventional Commits, lower case, imperative, no trailing period; scopes from CLAUDE.md (`store`, `api`, `reporting`, `jobs`, `shared`, `deploy`). End every commit message with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Do not put a model name anywhere else.
- Refusals are typed (`store.ErrInvalid`, `store.ErrConflict`, `store.ErrNotFound`) and matched with `errors.Is`.
- No new third-party Go dependency.
- Migration number is **041**, file `internal/store/sqlite/migrations/041_events_encoding.sql`, pre-step `internal/store/sqlite/migration041.go`. The migration is unreleased until this PR merges, so later tasks edit the same file.
- `''` is dictionary id 0 and has no `strings` row. Family codes: views 1, product 2, measures 3.
- Blob tags for `actor_id`, `user_id`, `group_id`, `session_id`: `x'01'` + 16 bytes for a lowercase canonical UUID (`8-4-4-4-12`), `x'02'` + bytes for non-empty even-length lowercase hex; everything else stays text. `id`: 16 untagged bytes for a lowercase canonical UUID, else text.
- `ts`/`received_at`: integer Unix seconds; `received_at = ''` encodes as 0 and decodes to `''`.
- Docs are updated in the same commit as the change they describe (CLAUDE.md "Documentation" table).

## Review Focus

1. **A late event for an old day after the nightly cleanup**: a string cached for that day must not be written as a stale id (spec D7 cutoff ≤ `today − RETENTION_RAW_DAYS − 1`). Test in Task 3.
2. **A rolled-back write batch**: ids resolved inside it must not stay in the in-memory cache (a cached id whose insert was rolled back would dangle). Test in Task 2.
3. **Hostile or odd ids**: uppercase UUID, UUID without dashes, 15-byte hex, odd-length hex, emails, `''` must read back byte-for-byte. Test in Task 1 (Go) and Task 2 (migration parity).
4. **A widget naming the private schema** in any spelling (`"tw_store".events`, `[tw_store]`, `TW_STORE.events`, `'tw_store'`) must be refused, while a column named `events` keeps working. Test in Task 6.
5. **A project purged while its events are buffered**: the batch must still commit for other projects, the purged project's rows are dropped. Test in Task 2.

---

### Task 1: Encoding primitives

**Files:**
- Create: `internal/store/sqlite/encode.go`
- Test: `internal/store/sqlite/encode_test.go`

**Interfaces:**
- Produces (package `sqlite`, unexported):
  - `func familyCode(f store.Family) int64` — views 1, product 2, measures 3; panics on anything else (callers have validated).
  - `func encodeEventID(id string) any` — `[]byte` (16) for a lowercase canonical UUID, else `id`.
  - `func encodeID(s string) any` — `[]byte{0x01, …16}` / `[]byte{0x02, …}` / `s` (Global Constraints).
  - `func decodeID(v any) string` and `func decodeEventID(v any) string` — inverses (tests, Go-side reads).
  - `func unixSeconds(t time.Time) int64`.
  - SQL fragment builders, each taking a column expression and returning a SQL expression producing today's text: `sqlFamily(col)`, `sqlEventID(col)`, `sqlID(col)`, `sqlString(col)`, `sqlTS(col)`, `sqlReceivedAt(col)`, `sqlAttributes(col)`.

- [ ] **Step 1: Write the failing tests**

```go
package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

var idCases = []string{
	"0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5f6", // lowercase UUID
	"0192F1A2-7C3B-7D4E-8F90-A1B2C3D4E5F6", // uppercase UUID: stays text
	"0192f1a27c3b7d4e8f90a1b2c3d4e5f6",     // 32 hex
	"a1b2c3d4e5f6",                         // 12 hex
	"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3",       // 30 hex (15 bytes)
	"abc",                                  // odd length
	"ABCDEF",                               // uppercase hex
	"user@example.com",
	"42",
	"",
	"0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fg", // UUID-shaped, not hex
}

func TestEncodeIDRoundTrip(t *testing.T) {
	for _, s := range idCases {
		if got := decodeID(encodeID(s)); got != s {
			t.Errorf("decodeID(encodeID(%q)) = %q", s, got)
		}
		if got := decodeEventID(encodeEventID(s)); got != s {
			t.Errorf("decodeEventID(encodeEventID(%q)) = %q", s, got)
		}
	}
	if b, ok := encodeID(idCases[0]).([]byte); !ok || len(b) != 17 || b[0] != 1 {
		t.Errorf("uuid: got %#v, want 17 bytes tagged 0x01", encodeID(idCases[0]))
	}
	if b, ok := encodeID(idCases[2]).([]byte); !ok || len(b) != 17 || b[0] != 2 {
		t.Errorf("hex: got %#v, want 17 bytes tagged 0x02", encodeID(idCases[2]))
	}
	if b, ok := encodeEventID(idCases[0]).([]byte); !ok || len(b) != 16 {
		t.Errorf("event id: got %#v, want 16 untagged bytes", encodeEventID(idCases[0]))
	}
	for _, s := range []string{idCases[1], idCases[5], idCases[6], idCases[7], "", idCases[10]} {
		if _, ok := encodeID(s).(string); !ok {
			t.Errorf("encodeID(%q) should stay text", s)
		}
	}
}

// TestSQLDecodersMatchGo runs every SQL fragment on Go-encoded values, so
// the views (which carry these fragments) decode what ingest writes.
func TestSQLDecodersMatchGo(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	one := func(expr string, arg any) string {
		t.Helper()
		var s string
		if err := db.QueryRow(`SELECT `+expr+` FROM (SELECT ? AS c)`, arg).Scan(&s); err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		return s
	}
	for _, s := range idCases {
		if got := one(sqlID("c"), encodeID(s)); got != s {
			t.Errorf("sqlID(%q) = %q", s, got)
		}
		if got := one(sqlEventID("c"), encodeEventID(s)); got != s {
			t.Errorf("sqlEventID(%q) = %q", s, got)
		}
	}
	ts := time.Date(2026, 10, 11, 21, 58, 3, 0, time.UTC)
	if got := one(sqlTS("c"), unixSeconds(ts)); got != "2026-10-11T21:58:03Z" {
		t.Errorf("sqlTS = %q", got)
	}
	if got := one(sqlReceivedAt("c"), int64(0)); got != "" {
		t.Errorf("sqlReceivedAt(0) = %q, want empty", got)
	}
	for f, name := range map[store.Family]string{store.FamilyViews: "views", store.FamilyProduct: "product", store.FamilyMeasures: "measures"} {
		if got := one(sqlFamily("c"), familyCode(f)); got != name {
			t.Errorf("sqlFamily(%d) = %q", familyCode(f), got)
		}
	}
	_ = context.Background()
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `export PATH=/usr/local/go/bin:$PATH; mkdir -p ~/.cache/gotmp-enc; GOTMPDIR=~/.cache/gotmp-enc go test ./internal/store/sqlite/ -run 'TestEncodeIDRoundTrip|TestSQLDecodersMatchGo'`
Expected: build failure, `undefined: encodeID`.

- [ ] **Step 3: Implement `encode.go`**

```go
package sqlite

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The stored forms of events' columns (spec D1–D6). Every encoding
// converts back to exactly the text that was sent; the raw_* views and
// v_events_flat decode with the sql* fragments below, and the migration
// (041) carries its own copy of the same rules, pinned by
// TestMigrationEncodesLikeIngest.

const (
	tagUUID = 0x01 // a lowercase canonical UUID, 16 bytes follow
	tagHex  = 0x02 // non-empty even-length lowercase hex, its bytes follow
)

func familyCode(f store.Family) int64 {
	switch f {
	case store.FamilyViews:
		return 1
	case store.FamilyProduct:
		return 2
	case store.FamilyMeasures:
		return 3
	}
	panic(fmt.Sprintf("familyCode: %q", f))
}

// isLowerHex reports s is non-empty lowercase hex.
func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// uuidBytes returns the 16 bytes of a lowercase canonical UUID, or nil.
func uuidBytes(s string) []byte {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return nil
	}
	h := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	if !isLowerHex(h) {
		return nil
	}
	b, _ := hex.DecodeString(h)
	return b
}

func encodeEventID(id string) any {
	if b := uuidBytes(id); b != nil {
		return b
	}
	return id
}

func encodeID(s string) any {
	if b := uuidBytes(s); b != nil {
		return append([]byte{tagUUID}, b...)
	}
	if len(s)%2 == 0 && isLowerHex(s) {
		b, _ := hex.DecodeString(s)
		return append([]byte{tagHex}, b...)
	}
	return s
}

func formatUUID(b []byte) string {
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func decodeEventID(v any) string {
	if b, ok := v.([]byte); ok {
		return formatUUID(b)
	}
	return fmt.Sprint(v)
}

func decodeID(v any) string {
	b, ok := v.([]byte)
	if !ok {
		return fmt.Sprint(v)
	}
	if b[0] == tagUUID {
		return formatUUID(b[1:])
	}
	return hex.EncodeToString(b[1:])
}

func unixSeconds(t time.Time) int64 { return t.UTC().Unix() }

// sqlUUIDText formats a 32-character lowercase hex expression with dashes.
func sqlUUIDText(h string) string {
	return fmt.Sprintf(`substr(%[1]s, 1, 8) || '-' || substr(%[1]s, 9, 4) || '-' || substr(%[1]s, 13, 4) || '-' || substr(%[1]s, 17, 4) || '-' || substr(%[1]s, 21, 12)`, h)
}

func sqlFamily(col string) string {
	return fmt.Sprintf(`CASE %s WHEN 1 THEN 'views' WHEN 2 THEN 'product' WHEN 3 THEN 'measures' END`, col)
}

func sqlEventID(col string) string {
	return fmt.Sprintf(`CASE WHEN typeof(%[1]s) = 'blob' THEN %[2]s ELSE %[1]s END`,
		col, sqlUUIDText(fmt.Sprintf(`lower(hex(%s))`, col)))
}

func sqlID(col string) string {
	h := fmt.Sprintf(`lower(hex(substr(%s, 2)))`, col)
	return fmt.Sprintf(`CASE WHEN typeof(%[1]s) <> 'blob' THEN %[1]s WHEN substr(%[1]s, 1, 1) = x'01' THEN %[2]s ELSE %[3]s END`,
		col, sqlUUIDText(h), h)
}

// sqlString decodes a strings id; id 0 has no row and reads as ''.
func sqlString(col string) string {
	return fmt.Sprintf(`COALESCE((SELECT value FROM strings WHERE id = %s), '')`, col)
}

func sqlTS(col string) string {
	return fmt.Sprintf(`strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ', %s, 'unixepoch')`, col)
}

func sqlReceivedAt(col string) string {
	return fmt.Sprintf(`CASE WHEN %[1]s = 0 THEN '' ELSE %[2]s END`, col, sqlTS(col))
}

// sqlAttributes rebuilds {"key":"value"} from {"<key id>":<value number>};
// a value stored as a string (none today) passes through.
func sqlAttributes(col string) string {
	return fmt.Sprintf(`(SELECT json_group_object(k.key, COALESCE(v.value, j.value))
  FROM json_each(%s) j
  JOIN project_attributes k ON k.id = CAST(j.key AS INTEGER)
  LEFT JOIN attribute_values v ON v.key_id = k.id AND v.n = j.value AND j.type = 'integer')`, col)
}

var _ = strings.TrimSpace
```

Remove the trailing `var _ = strings.TrimSpace` and the `strings` import if unused.

- [ ] **Step 4: Run the tests**

Run: same command as Step 2. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlite/encode.go internal/store/sqlite/encode_test.go
git commit -m "refactor(store): add the encodings for raw event columns

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: The switch — migration 041, encoding ingest, decoding views

This is the one task that changes the schema; everything that writes `events` changes with it so the suite stays green. It may land as several commits, the suite green at the last one.

**Files:**
- Create: `internal/store/sqlite/migrations/041_events_encoding.sql`
- Create: `internal/store/sqlite/migration041.go` (pre-step `prepareEncoding`)
- Create: `internal/store/sqlite/dict.go` (dictionary resolver)
- Modify: `internal/store/sqlite/migrate.go` (add `preSteps`, run before the version's SQL)
- Modify: `internal/store/sqlite/sqlite.go` (`DB` gains `dict *dict`, created in `openAt`)
- Modify: `internal/store/sqlite/write.go` (`insertEvents` encodes)
- Modify: `internal/store/sqlite/usage_attributes.go` (`writeReceived` no longer inserts catalog rows — the resolver did)
- Modify: `internal/store/sqlite/aggregate_views.go:205`, `aggregate_product.go:34,52`, `aggregate_measures.go:25,43` (family codes; blocked names by `strings` id)
- Modify: `internal/store/sqlite/event_rules.go:70` (rename/merge by `strings` id), `attribute_rules.go:86` + `renameCatalog` (D9), `forms.go:491` (encoded id, name)
- Modify: `internal/store/sqlite/flatview.go` (decode; `attr_*` by catalog id; `TestFlatViewCoversEveryEventsColumn` follows)
- Modify: `internal/reporting/source.go:296-302` (`sampleProject` reads `raw_*`)
- Modify: `internal/sqlcheck/sqlcheck_test.go` (`migrationSteps` gains `store/sqlite/migration041.go`)
- Modify tests that insert into or read `events` directly: `internal/store/sqlite/registry_test.go`, `coverage_test.go`, `internal/api/seed_test.go`, `internal/api/ops_activity_test.go`, `internal/api/ops_event_rules_test.go`, and every non-migration test matching `grep -rnE "(FROM|UPDATE|INTO) events" --include=*_test.go internal` (migration tests pinned below 41 keep their text SQL).
- Test: `internal/store/sqlite/migration041_test.go`, `internal/store/sqlite/dict_test.go`
- Docs: `docs/twillingate.md` (raw table section around line 906 and line 1588: encoding table, `attr_*` advice, `v_identities`), `internal/api/resources.go` (`schemaViews` gains `v_identities`), `deploy/UPGRADES.md` (041 entry)

**Interfaces:**
- Consumes (Task 1): `familyCode`, `encodeEventID`, `encodeID`, `unixSeconds`, `sqlFamily`, `sqlEventID`, `sqlID`, `sqlString`, `sqlTS`, `sqlReceivedAt`, `sqlAttributes`.
- Produces:
  - Tables `strings`, `attribute_values`, rebuilt `project_attributes` (spec D2, D6, D8; exact DDL below), trigger `attribute_values_next`.
  - `type dict struct` with `func newDict() *dict`; `func (d *dict) begin(ctx context.Context, tx *sql.Tx) (*txDict, error)`; `func (t *txDict) str(ctx context.Context, value, day string) (int64, error)` (0 for `""`); `func (t *txDict) key(ctx context.Context, projectID int64, key, day string) (int64, error)`; `func (t *txDict) value(ctx context.Context, keyID int64, value, day string) (int64, error)`; `func (t *txDict) close()`; `func (t *txDict) commit()` (merge learned ids into the shared cache; call only after the transaction committed).
  - `preSteps map[int]func(context.Context, *sql.Tx) error` in `migrate.go`.
  - View `v_identities` (`SELECT project_id, kind, id, name, last_seen_day, updated_at FROM identities`).

- [ ] **Step 1: Dump today's view definitions**

Write a throwaway test in `internal/store/sqlite` (do not commit it):

```go
func TestDumpViews(t *testing.T) {
	db := newTestDB(t)
	rows, _ := db.db.Query(`SELECT name, sql FROM sqlite_schema WHERE type='view' AND name <> 'v_events_flat' ORDER BY name`)
	defer rows.Close()
	for rows.Next() {
		var n, s string
		rows.Scan(&n, &s)
		fmt.Printf("-- %s\n%s;\n\n", n, s)
	}
}
```

Run it with `-run TestDumpViews -v` and keep the output: every `v_*` view except `v_retention` (it reads only `agg_retention`) is recreated **byte-for-byte** in 041 (they read `raw_*`, which keep their columns). Delete the test.

- [ ] **Step 2: Write the failing migration test**

`internal/store/sqlite/migration041_test.go`. Seed at version 40 with text SQL covering every case, snapshot every `raw_*` and `v_*` row, migrate to 41, compare:

```go
package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// seed040 inserts raw rows at schema 40 covering every encoding case:
// each id form, '' in every text column, all three families, a
// received_at of '', attributes on views and product rows, and a key
// with no catalog row.
func seed040(t *testing.T, db *DB) {
	t.Helper()
	execAll(t, db,
		`INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]'), (2, 'q', '[]')`,
		`INSERT INTO project_attributes (project_id, key, breakdown) VALUES (1, 'plan', 1)`,
		`INSERT INTO events (family, project_id, day, id, event_name, ts, received_at, kind, actor_id, actor_kind,
		   user_id, group_id, session_id, host, path, referrer_source, platform, os, browser, country, attributes)
		 VALUES
		 ('views', 1, '2026-10-10', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5f6', '$page_view', '2026-10-10T10:00:00Z', '2026-10-10T10:00:01Z', 'web',
		  '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5f7', 'install', '', '', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5f8', 'x.com', '/a', 'google', 'web', 'Linux', 'Firefox', 'DE', '{"plan":"pro","tier":"a"}'),
		 ('views', 1, '2026-10-10', '0192F1A2-7C3B-7D4E-8F90-A1B2C3D4E5F9', '$page_view', '2026-10-10T11:00:00Z', '', 'web',
		  'ABCDEF', 'install', 'user@example.com', 'a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3', 'abc', 'x.com', '', '', 'unknown', '', '', '', '{}'),
		 ('product', 1, '2026-10-11', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa', 'signup', '2026-10-11T09:00:00Z', '2026-10-11T09:00:00Z', 'app',
		  '0192f1a27c3b7d4e8f90a1b2c3d4e5f6', 'user', '42', 'a1b2c3d4e5f6', '', '', '', '', 'ios', 'iOS', '', 'FR', '{"plan":"free","seats":"3"}')`,
		`INSERT INTO events (family, project_id, day, id, event_name, ts, received_at, kind, actor_id, actor_kind, measure, value, sample_rate, attributes)
		 VALUES ('measures', 1, '2026-10-11', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fb', '$lcp', '2026-10-11T09:00:05Z', '2026-10-11T09:00:05Z', 'web',
		  '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5f7', 'install', 'time', 1234.5, 0.5, '{"plan":"pro"}')`,
	)
}

// snapshotRows reads every row of every raw_* and v_* view (v_events_flat
// is process-owned and skipped), ordered, as strings; attributes columns
// are normalized as parsed JSON (key order is not contract, spec D6).
func snapshotRows(t *testing.T, db *DB) map[string][]string {
	t.Helper()
	ctx := context.Background()
	names := []string{}
	rows, err := db.db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type='view'
		AND (name LIKE 'raw\_%' ESCAPE '\' OR name LIKE 'v\_%' ESCAPE '\') AND name <> 'v_events_flat' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	out := map[string][]string{}
	for _, n := range names {
		r, err := db.db.QueryContext(ctx, `SELECT * FROM `+n)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			parts := make([]string, len(cols))
			for i, v := range vals {
				s := fmt.Sprint(v)
				if b, ok := v.([]byte); ok {
					s = string(b)
				}
				if cols[i] == "attributes" {
					var m map[string]any
					if json.Unmarshal([]byte(s), &m) == nil {
						keys := make([]string, 0, len(m))
						for k := range m {
							keys = append(keys, k)
						}
						sort.Strings(keys)
						var b strings.Builder
						for _, k := range keys {
							fmt.Fprintf(&b, "%s=%v;", k, m[k])
						}
						s = b.String()
					}
				}
				parts[i] = cols[i] + ":" + s
			}
			out[n] = append(out[n], strings.Join(parts, "|"))
		}
		r.Close()
		sort.Strings(out[n])
	}
	return out
}

func TestMigration041KeepsEveryViewRow(t *testing.T) {
	db := newTestDBAt(t, 40)
	seed040(t, db)
	before := snapshotRows(t, db)
	if err := db.migrateThrough(context.Background(), 41); err != nil {
		t.Fatal(err)
	}
	after := snapshotRows(t, db)
	delete(after, "v_identities") // new in 041
	if !reflect.DeepEqual(before, after) {
		for n := range before {
			if !reflect.DeepEqual(before[n], after[n]) {
				t.Errorf("%s:\nbefore %v\nafter  %v", n, before[n], after[n])
			}
		}
	}
}

func TestMigration041StoresEncoded(t *testing.T) {
	db := newTestDBAt(t, 40)
	seed040(t, db)
	if err := db.migrateThrough(context.Background(), 41); err != nil {
		t.Fatal(err)
	}
	var typ string
	if err := db.db.QueryRow(`SELECT typeof(id) || typeof(actor_id) || typeof(ts) || typeof(event_name) FROM events WHERE family = 2`).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != "blobblobintegerinteger" {
		t.Errorf("stored types = %s", typ)
	}
	var attrs string
	db.db.QueryRow(`SELECT attributes FROM events WHERE family = 2`).Scan(&attrs)
	if strings.Contains(attrs, "plan") || strings.Contains(attrs, "free") {
		t.Errorf("attributes still carry text: %s", attrs)
	}
	var missing int // 'tier' and 'seats' had no catalog row before
	db.db.QueryRow(`SELECT COUNT(*) FROM project_attributes WHERE project_id = 1 AND key IN ('tier', 'seats')`).Scan(&missing)
	if missing != 2 {
		t.Errorf("catalog rows added by the pre-step = %d, want 2", missing)
	}
}

func TestMigration041RefusesNonStringValues(t *testing.T) {
	db := newTestDBAt(t, 40)
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`,
		`INSERT INTO events (family, project_id, day, id, event_name, ts, actor_id, attributes)
		 VALUES ('product', 1, '2026-10-11', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa', 'e', '2026-10-11T09:00:00Z', 'a', '{"n":3}')`)
	err := db.migrateThrough(context.Background(), 41)
	if err == nil || !strings.Contains(err.Error(), "not a string") {
		t.Fatalf("err = %v, want a refusal naming non-string values", err)
	}
}

func TestMigration041DropsOrphanRows(t *testing.T) {
	db := newTestDBAt(t, 40)
	execAll(t, db, `INSERT INTO events (family, project_id, day, id, event_name, ts, actor_id, attributes)
		 VALUES ('product', 99, '2026-10-11', '0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa', 'e', '2026-10-11T09:00:00Z', 'a', '{"k":"v"}')`)
	if err := db.migrateThrough(context.Background(), 41); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	if n != 0 {
		t.Errorf("orphan rows kept: %d", n)
	}
}
```

Adjust the `projects` insert to the columns migration 040 requires (read `040_project_attributes.sql` and earlier `projects` DDL; add `sort_key` etc. if NOT NULL without default).

- [ ] **Step 3: Run it to see it fail**

Run: `GOTMPDIR=~/.cache/gotmp-enc go test ./internal/store/sqlite/ -run TestMigration041`
Expected: FAIL (no migration 41: `migrateThrough` is a no-op, `typeof` returns `texttexttexttext`).

- [ ] **Step 4: Add `preSteps` to the migrator**

In `migrate.go`, beside `dataSteps`:

```go
// preSteps run before their version's SQL, in the same transaction: checks
// that must refuse a migration before it changes anything, and repairs the
// SQL relies on (041 adds the catalog rows its encoding joins on).
var preSteps = map[int]func(context.Context, *sql.Tx) error{
	41: prepareEncoding,
}
```

and in `migrateThrough`, right after the `processOwned` drops and before `tx.ExecContext(ctx, string(body))`:

```go
		if step, ok := preSteps[version]; ok {
			if err := step(ctx, tx); err != nil {
				tx.Rollback()
				return fmt.Errorf("sqlite: migration %s pre-step: %w", name, err)
			}
		}
```

- [ ] **Step 5: Write the pre-step `migration041.go`**

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// prepareEncoding runs before 041's SQL (spec D17): it refuses attributes
// the encoding cannot carry, drops raw rows whose project is gone (their
// keys cannot get catalog rows), and adds the catalog row of every key a
// raw row carries, views included, which the encoding joins on.
func prepareEncoding(ctx context.Context, tx *sql.Tx) error {
	var invalid, nonString int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE NOT json_valid(attributes)
		OR json_type(attributes) <> 'object'`).Scan(&invalid); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events e, json_each(e.attributes) j
		WHERE json_valid(e.attributes) AND j.type <> 'text'`).Scan(&nonString); err != nil {
		return err
	}
	if invalid > 0 || nonString > 0 {
		return fmt.Errorf("events: %d rows with attributes that are not a JSON object, %d attribute values that are not a string; fix them before upgrading (deploy/UPGRADES.md, 041)", invalid, nonString)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE project_id NOT IN (SELECT id FROM projects)`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_attributes (project_id, key)
		SELECT DISTINCT e.project_id, j.key FROM events e, json_each(e.attributes) j`)
	return err
}
```

Add `"store/sqlite/migration041.go": true` to `migrationSteps` in `internal/sqlcheck/sqlcheck_test.go`.

- [ ] **Step 6: Write `041_events_encoding.sql`**

Structure, in this order (each block commented in the file's style: a header comment naming the spec and decision numbers):

1. `DROP VIEW` every view that reads `events` or a `raw_*` view: `raw_views`, `raw_product`, `raw_measures`, `v_identity_daily`, `v_measures_attrs`, `v_measures_daily`, `v_product_attrs`, `v_product_daily`, `v_product_totals`, and the 14 `v_views_*` (`app_versions`, `browsers`, `consent`, `countries`, `daily`, `devices`, `displays`, `hosts`, `locales`, `os`, `paths`, `platforms`, `referrers`, `utm`). `v_retention` stays.
2. The catalog, rebuilt with ids, most-used keys first:

```sql
CREATE TABLE project_attributes_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    INTEGER NOT NULL REFERENCES projects(id),
    key           TEXT    NOT NULL,
    breakdown     INTEGER NOT NULL DEFAULT 0 CHECK (breakdown IN (0, 1)),
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    last_seen_day TEXT    NOT NULL DEFAULT '',
    values_next   INTEGER NOT NULL DEFAULT 1,
    UNIQUE (project_id, key)
);
INSERT INTO project_attributes_new (project_id, key, breakdown, created_at, last_seen_day)
  SELECT a.project_id, a.key, a.breakdown, a.created_at,
         COALESCE(u.last_day, '')
  FROM project_attributes a
  LEFT JOIN (SELECT e.project_id, j.key, MAX(e.day) AS last_day, COUNT(*) AS n
             FROM events e, json_each(e.attributes) j GROUP BY e.project_id, j.key) u
    ON u.project_id = a.project_id AND u.key = a.key
  ORDER BY COALESCE(u.n, 0) DESC, a.project_id, a.key;
DROP TABLE project_attributes;
ALTER TABLE project_attributes_new RENAME TO project_attributes;
```

3. Values (spec D6), with the counter trigger:

```sql
CREATE TABLE attribute_values (
    key_id        INTEGER NOT NULL REFERENCES project_attributes(id) ON DELETE CASCADE,
    n             INTEGER NOT NULL,
    value         TEXT    NOT NULL,
    last_seen_day TEXT    NOT NULL,
    PRIMARY KEY (key_id, n),
    UNIQUE (key_id, value)
) WITHOUT ROWID;
-- n is never reused within a key: the counter on the catalog row only grows.
CREATE TRIGGER attribute_values_next AFTER INSERT ON attribute_values BEGIN
  UPDATE project_attributes SET values_next = NEW.n + 1 WHERE id = NEW.key_id AND values_next <= NEW.n;
END;
INSERT INTO attribute_values (key_id, n, value, last_seen_day)
  SELECT k.id, ROW_NUMBER() OVER (PARTITION BY k.id ORDER BY COUNT(*) DESC, j.value), j.value, MAX(e.day)
  FROM events e, json_each(e.attributes) j
  JOIN project_attributes k ON k.project_id = e.project_id AND k.key = j.key
  GROUP BY k.id, j.value;
```

4. `strings` (spec D2), most frequent first, from every encoded text column (`event_name`, `measure`, `kind`, `actor_kind`, `host`, `path`, `referrer_source`, `utm_source`, `utm_medium`, `utm_campaign`, `platform`, `os`, `os_version`, `os_name`, `browser`, `browser_version`, `browser_locale`, `app_version`, `app_locale`, `device`, `device_model`, `country`):

```sql
CREATE TABLE strings (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    value         TEXT NOT NULL UNIQUE,
    last_seen_day TEXT NOT NULL
);
INSERT INTO strings (value, last_seen_day)
  SELECT v, MAX(day) FROM (
    SELECT event_name AS v, day FROM events
    UNION ALL SELECT measure, day FROM events
    -- … one arm per column listed above …
  ) WHERE v <> '' GROUP BY v ORDER BY COUNT(*) DESC, v;
```

5. `events_new` with encoded types and the copy. Columns keep today's order; id-like columns are declared `BLOB` (no affinity, so text stays text); dictionary columns `INTEGER NOT NULL DEFAULT 0`; `ts INTEGER NOT NULL`, `received_at INTEGER NOT NULL DEFAULT 0`; `platform INTEGER NOT NULL DEFAULT 0`; `bucket` regenerated exactly as in `024_measures.sql`; `PRIMARY KEY (family, project_id, day, id) WITHOUT ROWID`. The copy uses these expressions (they must match `encode.go`; Step 9 pins it):

```sql
-- a lowercase canonical UUID: 36 chars, dashes at 9/14/19/24, lowercase hex elsewhere
-- (written once per column; X is the column)
CASE WHEN length(X) = 36 AND substr(X,9,1) = '-' AND substr(X,14,1) = '-' AND substr(X,19,1) = '-' AND substr(X,24,1) = '-'
          AND replace(X, '-', '') NOT GLOB '*[^0-9a-f]*' AND length(replace(X, '-', '')) = 32
     THEN x'01' || unhex(replace(X, '-', ''))
     WHEN X <> '' AND length(X) % 2 = 0 AND X NOT GLOB '*[^0-9a-f]*'
     THEN x'02' || unhex(X)
     ELSE X END
-- id: the same UUID test, THEN unhex(replace(id, '-', '')) with no tag, ELSE id
-- family: CASE family WHEN 'views' THEN 1 WHEN 'product' THEN 2 WHEN 'measures' THEN 3 END
-- ts: unixepoch(ts); received_at: CASE WHEN received_at = '' THEN 0 ELSE unixepoch(received_at) END
-- a text column C: COALESCE((SELECT id FROM strings WHERE value = C), 0)
-- attributes:
(SELECT COALESCE(json_group_object(k.id, v.n), '{}')
   FROM json_each(e.attributes) j
   JOIN project_attributes k ON k.project_id = e.project_id AND k.key = j.key
   JOIN attribute_values v ON v.key_id = k.id AND v.value = j.value)
```

Then `DROP TABLE events; ALTER TABLE events_new RENAME TO events;`.

6. The decoding `raw_*` views (spec D10), each `SELECT <decoded columns in events' column order> FROM events e WHERE e.family = <code>`. Column `day`, `project_id`, `display_*`, `consent`, `value`, `sample_rate`, `bucket` pass through; the rest use the expressions `encode.go`'s `sql*` builders produce (generate the text once with a throwaway Go test printing `sqlID("e.actor_id")` etc., and paste it; the migration's text is fixed at its version). Example of the shape:

```sql
CREATE VIEW raw_views AS
SELECT 'views' AS family, e.project_id AS project_id, e.day AS day,
       <sqlEventID("e.id")> AS id,
       <sqlString("e.event_name")> AS event_name,
       <sqlTS("e.ts")> AS ts,
       <sqlReceivedAt("e.received_at")> AS received_at,
       <sqlString("e.kind")> AS kind,
       <sqlID("e.actor_id")> AS actor_id,
       … every column of events, in order …
       <sqlAttributes("e.attributes")> AS attributes,
       e.value AS value, <sqlString("e.measure")> AS measure, e.sample_rate AS sample_rate, e.bucket AS bucket
FROM events e WHERE e.family = 1;
```

7. Every `v_*` view from Step 1, byte-for-byte, plus:

```sql
-- v_identities: identities as stored, for widget SQL (spec D15).
CREATE VIEW v_identities AS
SELECT project_id, kind, id, name, last_seen_day, updated_at FROM identities;
```

(Check `identities`' actual columns and list all of them.)

- [ ] **Step 7: Run the migration tests**

Run: `GOTMPDIR=~/.cache/gotmp-enc go test ./internal/store/sqlite/ -run TestMigration041`
Expected: PASS. Fix the SQL until it does; the parity test is the contract.

- [ ] **Step 8: Write the failing ingest tests (`dict_test.go`)**

```go
package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestWriteEventsReadsBackAsSent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	ts := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	ev := store.Event{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa", ProjectID: 1, Family: store.FamilyProduct,
		EventName: "signup", TS: ts, ReceivedAt: ts, Kind: "app", ActorID: "0192f1a27c3b7d4e8f90a1b2c3d4e5f6",
		ActorKind: "user", UserID: "user@example.com", Platform: "ios",
		Attributes: map[string]string{"plan": "pro", "seats": "3"}}
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil { // retry: ignored
		t.Fatal(err)
	}
	var n int
	var id, actor, user, name, tsText, attrs string
	db.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	db.db.QueryRow(`SELECT id, actor_id, user_id, event_name, ts, attributes FROM raw_product`).
		Scan(&id, &actor, &user, &name, &tsText, &attrs)
	if n != 1 || id != ev.ID || actor != ev.ActorID || user != ev.UserID || name != "signup" ||
		tsText != "2026-10-11T09:00:00Z" || attrs != `{"plan":"pro","seats":"3"}` {
		t.Errorf("got n=%d id=%s actor=%s user=%s name=%s ts=%s attrs=%s", n, id, actor, user, name, tsText, attrs)
	}
}

func TestWriteEventsRollbackForgetsIDs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	ts := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	good := store.Event{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa", ProjectID: 1, Family: store.FamilyProduct,
		EventName: "fresh-name", TS: ts, ReceivedAt: ts, ActorID: "a"}
	bad := good
	bad.ID, bad.Family = "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fb", "nope"
	// A batch refused after resolving "fresh-name" must not leave its id cached.
	if err := db.WriteEvents(ctx, []store.Event{good, bad}); err == nil {
		t.Fatal("want the bad family refused")
	}
	// Force a failure mid-transaction: a trigger refusing the second insert.
	execAll(t, db, `CREATE TEMP TRIGGER refuse BEFORE INSERT ON events WHEN NEW.id = x'0192f1a27c3b7d4e8f90a1b2c3d4e5fc'
		BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	third := good
	third.ID, third.EventName = "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fc", "another-fresh"
	if err := db.WriteEvents(ctx, []store.Event{good, third}); err == nil {
		t.Fatal("want the trigger to refuse")
	}
	execAll(t, db, `DROP TRIGGER refuse`)
	if err := db.WriteEvents(ctx, []store.Event{third}); err != nil {
		t.Fatal(err)
	}
	var name string
	db.db.QueryRow(`SELECT event_name FROM raw_product WHERE id = ?`, third.ID).Scan(&name)
	if name != "another-fresh" {
		t.Errorf("event_name = %q: a rolled-back id stayed cached", name)
	}
	_ = errors.New
}

func TestWriteEventsDropsPurgedProjectRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	ts := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	kept := store.Event{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fa", ProjectID: 1, Family: store.FamilyProduct,
		EventName: "e", TS: ts, ReceivedAt: ts, ActorID: "a", Attributes: map[string]string{"k": "v"}}
	gone := kept
	gone.ID, gone.ProjectID = "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fb", 99
	if err := db.WriteEvents(ctx, []store.Event{kept, gone}); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	if n != 1 {
		t.Errorf("rows = %d, want only project 1's", n)
	}
}
```

Adjust `projects` inserts as in Step 2.

- [ ] **Step 9: Write the failing equality test (Go encoder vs migration SQL)**

Append to `migration041_test.go`:

```go
// TestMigrationEncodesLikeIngest writes the same event through ingest on
// a migrated database and through the migration on a seeded one, and
// requires byte-equal id columns and equal decoded rows.
func TestMigrationEncodesLikeIngest(t *testing.T) {
	ctx := context.Background()
	migrated := newTestDBAt(t, 40)
	seed040(t, migrated)
	if err := migrated.migrateThrough(ctx, 41); err != nil {
		t.Fatal(err)
	}
	ingested := newTestDB(t)
	execAll(t, ingested, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]'), (2, 'q', '[]')`)
	rows, err := migrated.db.QueryContext(ctx, `SELECT family, project_id, day, id, event_name, ts, received_at, kind, actor_id,
		actor_kind, user_id, group_id, session_id, host, path, referrer_source, platform, os, browser, country, attributes,
		measure, value, sample_rate FROM (SELECT * FROM raw_views UNION ALL SELECT * FROM raw_product UNION ALL SELECT * FROM raw_measures)`)
	if err != nil {
		t.Fatal(err)
	}
	var evs []store.Event
	for rows.Next() {
		// Scan each column into store.Event fields (parse ts/received_at with tsFormat,
		// attributes with json.Unmarshal, value into a *float64).
		// … build ev …
		evs = append(evs, ev)
	}
	rows.Close()
	if err := ingested.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	q := `SELECT hex(id), hex(actor_id), hex(user_id), hex(group_id), hex(session_id), ts, received_at FROM events ORDER BY hex(id)`
	a, b := allRows(t, migrated, q), allRows(t, ingested, q)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("migration %v\ningest    %v", a, b)
	}
	if x, y := snapshotRows(t, migrated)["raw_product"], snapshotRows(t, ingested)["raw_product"]; !reflect.DeepEqual(x, y) {
		t.Errorf("decoded rows differ:\n%v\n%v", x, y)
	}
}
```

Write the small helper `allRows(t, db, q) [][]string` in the same file, and fill in the scan (no placeholder may remain).

- [ ] **Step 10: Implement `dict.go`**

```go
package sqlite

import (
	"context"
	"database/sql"
	"sync"
)

// dict resolves text to its dictionary id (spec D2, D6, D8) inside ingest's
// write transaction and remembers, per id, the newest day it raised
// last_seen_day to (D7), so a known id costs one upsert per day, not per
// event. An id learned in a transaction reaches the shared maps only after
// that transaction committed (txDict.commit): a rolled-back insert would
// otherwise leave a cached id with no row.
type dict struct {
	mu   sync.Mutex
	strs map[string]seenID
	keys map[keyRef]seenID
	vals map[valRef]seenID
}

type seenID struct {
	id  int64
	day string
}

type keyRef struct {
	project int64
	key     string
}

type valRef struct {
	key   int64
	value string
}

func newDict() *dict {
	return &dict{strs: map[string]seenID{}, keys: map[keyRef]seenID{}, vals: map[valRef]seenID{}}
}

// txDict is one write transaction's resolver.
type txDict struct {
	d                  *dict
	strStmt, keyStmt   *sql.Stmt
	valStmt            *sql.Stmt
	strs               map[string]seenID
	keys               map[keyRef]seenID
	vals               map[valRef]seenID
}

func (d *dict) begin(ctx context.Context, tx *sql.Tx) (*txDict, error) {
	t := &txDict{d: d, strs: map[string]seenID{}, keys: map[keyRef]seenID{}, vals: map[valRef]seenID{}}
	var err error
	// One upsert resolves and touches (D7): it returns the id of the text,
	// inserting it again if the cleanup deleted it.
	if t.strStmt, err = tx.PrepareContext(ctx, `INSERT INTO strings (value, last_seen_day) VALUES (?1, ?2)
		ON CONFLICT (value) DO UPDATE SET last_seen_day = max(last_seen_day, excluded.last_seen_day)
		RETURNING id`); err != nil {
		return nil, err
	}
	if t.keyStmt, err = tx.PrepareContext(ctx, `INSERT INTO project_attributes (project_id, key, last_seen_day) VALUES (?1, ?2, ?3)
		ON CONFLICT (project_id, key) DO UPDATE SET last_seen_day = max(last_seen_day, excluded.last_seen_day)
		RETURNING id`); err != nil {
		t.close()
		return nil, err
	}
	if t.valStmt, err = tx.PrepareContext(ctx, `INSERT INTO attribute_values (key_id, n, value, last_seen_day)
		VALUES (?1, (SELECT values_next FROM project_attributes WHERE id = ?1), ?2, ?3)
		ON CONFLICT (key_id, value) DO UPDATE SET last_seen_day = max(last_seen_day, excluded.last_seen_day)
		RETURNING n`); err != nil {
		t.close()
		return nil, err
	}
	return t, nil
}

func (t *txDict) close() {
	for _, s := range []*sql.Stmt{t.strStmt, t.keyStmt, t.valStmt} {
		if s != nil {
			s.Close()
		}
	}
}

// cached returns the id known for ref if it was already touched for day or
// a later one, from this transaction first, then the shared map.
func cached[K comparable](local, shared map[K]seenID, mu *sync.Mutex, ref K, day string) (int64, bool) {
	if s, ok := local[ref]; ok && s.day >= day {
		return s.id, true
	}
	mu.Lock()
	s, ok := shared[ref]
	mu.Unlock()
	if ok && s.day >= day {
		return s.id, true
	}
	return 0, false
}

func (t *txDict) str(ctx context.Context, value, day string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	if id, ok := cached(t.strs, t.d.strs, &t.d.mu, value, day); ok {
		return id, nil
	}
	var id int64
	if err := t.strStmt.QueryRowContext(ctx, value, day).Scan(&id); err != nil {
		return 0, err
	}
	t.strs[value] = seenID{id, day}
	return id, nil
}

func (t *txDict) key(ctx context.Context, projectID int64, key, day string) (int64, error) {
	ref := keyRef{projectID, key}
	if id, ok := cached(t.keys, t.d.keys, &t.d.mu, ref, day); ok {
		return id, nil
	}
	var id int64
	if err := t.keyStmt.QueryRowContext(ctx, projectID, key, day).Scan(&id); err != nil {
		return 0, err
	}
	t.keys[ref] = seenID{id, day}
	return id, nil
}

func (t *txDict) value(ctx context.Context, keyID int64, value, day string) (int64, error) {
	ref := valRef{keyID, value}
	if n, ok := cached(t.vals, t.d.vals, &t.d.mu, ref, day); ok {
		return n, nil
	}
	var n int64
	if err := t.valStmt.QueryRowContext(ctx, keyID, value, day).Scan(&n); err != nil {
		return 0, err
	}
	t.vals[ref] = seenID{n, day}
	return n, nil
}

// commit merges what the committed transaction learned, keeping the later
// day where both know an id.
func (t *txDict) commit() {
	t.d.mu.Lock()
	defer t.d.mu.Unlock()
	for k, v := range t.strs {
		if cur, ok := t.d.strs[k]; !ok || cur.day < v.day || cur.id != v.id {
			t.d.strs[k] = v
		}
	}
	for k, v := range t.keys {
		if cur, ok := t.d.keys[k]; !ok || cur.day < v.day || cur.id != v.id {
			t.d.keys[k] = v
		}
	}
	for k, v := range t.vals {
		if cur, ok := t.d.vals[k]; !ok || cur.day < v.day || cur.id != v.id {
			t.d.vals[k] = v
		}
	}
}
```

Wire it: `DB` gets `dict *dict` (`openAt` sets `dict: newDict()`). In `WriteEvents`, `insertEvents` takes the `*txDict`; `WriteEvents` calls `t.commit()` only when `d.tx` returned nil (beside `d.seen.remember`). `WriteSubmission` (forms.go), which shares `insertEvents`, does the same.

- [ ] **Step 11: Encode in `insertEvents`**

Replace the INSERT with one taking encoded values. Before the loop:
- read the event rename rules for the batch's product/measures projects in this transaction (`SELECT project_id, family, name, to_name FROM rules WHERE kind='event' AND action='rename' AND project_id IN (…)`, or once per project) into a map, replacing the SQL `COALESCE((SELECT to_name …))` (keep the doc comment's reasoning: read in the flush's own transaction);
- find which projects of the batch exist (`SELECT id FROM projects WHERE id IN (…)`); skip events of the others (Review Focus 5).

Per event: `day := e.TS.UTC().Format("2006-01-02")`; encode `id` with `encodeEventID`, the four ids with `encodeID`, `ts`/`received_at` with `unixSeconds` (`received_at` 0 when `e.ReceivedAt.IsZero()`), every text column through `t.str(ctx, v, day)`; `platform` as sent (ingest sets `'unknown'`); attributes after `store.MapAttributes` (product/measures only, as today): build `map[string]int64` keyed by `strconv.FormatInt(keyID, 10)` from `t.key(ctx, e.ProjectID, k, day)` and `t.value(ctx, keyID, v, day)`, then `json.Marshal`. `received.add(e, day)` stays as is (it records key names for `usage_attributes`).

In `usage_attributes.go`, `writeReceived` stops inserting catalog rows (the resolver created them, with ids, before the row): delete the `catalog` statement and update its doc comment.

- [ ] **Step 12: Writers on encoded columns**

- `aggregate_views.go:205`, `aggregate_product.go:34,52`, `aggregate_measures.go:25,43`: `family='views'` → `family=1` (product 2, measures 3); the blocked-name deletes become `AND event_name IN (SELECT s.id FROM strings s JOIN rules r ON r.name = s.value WHERE r.project_id=? AND r.kind='event' AND r.family='product' AND r.action='block')`.
- `event_rules.go:70`: resolve `to` to its `strings` id in the transaction (`INSERT … ON CONFLICT … RETURNING id`, the resolver's statement text, with today's date as `last_seen_day`), and `from` by `SELECT id FROM strings WHERE value=?` (no row → nothing to rewrite); `UPDATE events SET event_name=?toID WHERE family=?code AND project_id=? AND event_name=?fromID`. Raise the target's `last_seen_day` to `MAX(day)` of the rewritten rows (spec D7.5). Check every other SQL in `event_rules.go` for `events`/`raw_*` text comparisons (e.g. existence checks) and keep them on `raw_*` or encode their parameters.
- `attribute_rules.go` `RenameAttribute` (spec D9): resolve `fromID` (`SELECT id FROM project_attributes WHERE project_id=? AND key=?`; none → no raw rows to rewrite) and `toID` (upsert); copy `from`'s values under `to` in Go, one `INSERT INTO attribute_values … ON CONFLICT DO UPDATE … RETURNING n` per distinct value (so the trigger numbers them), building a temp table `rename_map(old_n INTEGER PRIMARY KEY, new_n INTEGER)`; then

```sql
UPDATE events SET attributes = CASE
  WHEN json_type(attributes, ?toPath) IS NULL
    THEN json_set(json_remove(attributes, ?fromPath), ?toPath,
                  (SELECT new_n FROM temp.rename_map WHERE old_n = json_extract(attributes, ?fromPath)))
  ELSE json_remove(attributes, ?fromPath) END
WHERE project_id = ?1 AND family IN (2, 3) AND json_type(attributes, ?fromPath) IS NOT NULL
```

with paths `$."<id>"`; raise `to`'s and its values' `last_seen_day` to the newest rewritten day; drop the temp table. `renameCatalog` no longer deletes `from`'s row: it moves the breakdown flag to `to` and clears it on `from`.
- `forms.go:491`: `DELETE FROM events WHERE family=2 AND project_id=? AND day=? AND id=? AND event_name=(SELECT id FROM strings WHERE value=?)` with `encodeEventID(id)`.
- `flatview.go`: the view selects the decoded base columns from `events e` with the `sql*` builders (`family` via `sqlFamily("e.family")` …, `attributes` via `sqlAttributes("e.attributes")`), and each `attr_<key>` as

```go
fmt.Sprintf(`(SELECT v.value FROM project_attributes k JOIN attribute_values v ON v.key_id = k.id
  AND v.n = json_extract(e.attributes, '$."' || k.id || '"') WHERE k.project_id = e.project_id AND k.key = '%s') AS %s`, keyLit, alias)
```

where `keyLit` is the original key with `'` doubled. Keep the no-op-when-identical rebuild.
- `internal/reporting/source.go:296-302` `sampleProject`: read `raw_views`, `raw_product`, `raw_measures` instead of `events` (`SELECT MAX(day) FROM raw_views WHERE project_id=p.id`, …); update its doc comment.

- [ ] **Step 13: Update the tests that touch `events` directly**

Run `grep -rnE "(FROM|UPDATE|INTO|DELETE FROM) events" --include=*_test.go internal | grep -v migration0` and convert each: seeds go through `WriteEvents` (or a small `insertRawEvent(t, db, store.Event)` helper in `sqlite_test.go` that calls `WriteEvents`); reads go through `raw_*` or `v_events_flat`. In `internal/api` tests, seed through the store (`st.WriteEvents`). Keep each test's intent; do not delete assertions.

- [ ] **Step 14: Run the package and the whole check**

Run: `GOTMPDIR=~/.cache/gotmp-enc go test ./internal/store/... ./internal/api/... ./internal/reporting/... ./internal/jobs/... ./internal/sqlcheck/...` then `GOTMPDIR=~/.cache/gotmp-enc make check`.
Expected: PASS. `TestFlatViewCoversEveryEventsColumn`, `TestMigrationsNeverNameProcessOwnedObjects`, `TestSystemDashboards`, the docs sync test and `sqlcheck` must all pass.

- [ ] **Step 15: Docs**

- `docs/twillingate.md`, the raw table section (search "All three families are stored in one raw table") and the views list near line 1588: `events` is stored encoded; read it through `raw_views`, `raw_product`, `raw_measures` and `v_events_flat`, which decode to the columns listed; a table for agents reading `events` directly through `query`: `family` 1/2/3, `id` 16-byte UUID, the four tagged ids, `ts`/`received_at` Unix seconds, text columns as `strings.id` (0 = `''`), `attributes` as `{"<project_attributes.id>": <attribute_values.n>}`; read declared attributes through `attr_*` columns of `v_events_flat`, faster than `json_extract(attributes, …)` on `raw_product`; `v_identities` in the views table.
- `internal/api/resources.go` `schemaViews`: add `v_identities` and the `attr_*` advice.
- `deploy/UPGRADES.md`: a 041 section (spec D17): back up first; needs about twice the size of `events` free while it runs; seconds at tens of thousands of rows, about a minute at 1.3M; the pre-step refuses attributes that are not a JSON object of strings (with the counts) and drops raw rows of deleted projects; the file shrinks as incremental vacuum reclaims pages; `events` is now encoded for anyone reading it through `query`.

- [ ] **Step 16: Commit**

```bash
git add -A internal docs deploy
git commit -m "feat(store)!: store raw events with blob ids and dictionary ids behind decoding raw views

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Dictionary cleanup in the daily pass

**Files:**
- Create: `internal/store/sqlite/dictionaries.go`
- Modify: `internal/store/sqlite/usage_attributes.go` (`countReceived` stops calling `pruneCatalogs`; `pruneCatalog` moves into the cleanup with the `last_seen_day` condition)
- Modify: `internal/store/store.go` (Store interface gains the method), `internal/jobs/jobs.go` (its `Store` slice gains it; call it after `RecordUsageHistory`), any fake stores in `internal/jobs/*_test.go`
- Test: `internal/store/sqlite/dictionaries_test.go`, `internal/jobs/jobs_test.go` (call order)

**Interfaces:**
- Consumes: Task 2's tables and `dict`.
- Produces: `func (d *DB) CleanDictionaries(ctx context.Context, before civil.Date) error` — `before` is `today − RETENTION_RAW_DAYS − 1`. Per project, cutoff = min(`before`, the project's oldest `day` in `events`); for `strings`, cutoff = min(`before`, the oldest `day` in `events`). Deletes `strings` rows with `last_seen_day < cutoff`; catalog rows with `breakdown = 0`, no `usage_attributes` row, and `last_seen_day < cutoff` (their values cascade); `attribute_values` rows with `last_seen_day < cutoff`. One transaction.

- [ ] **Step 1: Write the failing tests**

```go
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func TestCleanDictionariesKeepsWhatRowsUse(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	day := func(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t.Add(9 * time.Hour) }
	write := func(id, d, name, val string) {
		t.Helper()
		if err := db.WriteEvents(ctx, []store.Event{{ID: id, ProjectID: 1, Family: store.FamilyViews, EventName: name,
			TS: day(d), ReceivedAt: day(d), ActorID: "a", Attributes: map[string]string{"k": val}}}); err != nil {
			t.Fatal(err)
		}
	}
	write("0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e501", "2026-09-01", "old-name", "old-value")
	write("0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e502", "2026-10-10", "new-name", "new-value")
	execAll(t, db, `DELETE FROM events WHERE day = '2026-09-01'`) // the rollup took the old day
	if err := db.CleanDictionaries(ctx, civil.MustParse("2026-10-01")); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int { var n int; db.db.QueryRow(q).Scan(&n); return n }
	if count(`SELECT COUNT(*) FROM strings WHERE value = 'old-name'`) != 0 {
		t.Error("old-name kept")
	}
	if count(`SELECT COUNT(*) FROM strings WHERE value = 'new-name'`) != 1 {
		t.Error("new-name deleted while a row uses it")
	}
	if count(`SELECT COUNT(*) FROM attribute_values WHERE value = 'old-value'`) != 0 ||
		count(`SELECT COUNT(*) FROM attribute_values WHERE value = 'new-value'`) != 1 {
		t.Error("values not cleaned by last_seen_day")
	}
	if count(`SELECT COUNT(*) FROM project_attributes WHERE key = 'k'`) != 1 {
		t.Error("a key carried only by view rows was pruned while those rows exist")
	}
}

// Review Focus 1: a string cached for a day before the oldest held row but
// inside the raw window survives, so a late event cannot carry a stale id.
func TestCleanDictionariesSparesTheRawWindow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	ts := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	ev := store.Event{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e503", ProjectID: 1, Family: store.FamilyProduct,
		EventName: "late", TS: ts, ReceivedAt: ts, ActorID: "a"}
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	execAll(t, db, `DELETE FROM events`) // a form deletion took the only row
	// raw window starts 2026-10-02: before = 2026-10-01
	if err := db.CleanDictionaries(ctx, civil.MustParse("2026-10-01")); err != nil {
		t.Fatal(err)
	}
	ev.ID = "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e504" // same day, cached id
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	var name string
	db.db.QueryRow(`SELECT event_name FROM raw_product WHERE id = ?`, ev.ID).Scan(&name)
	if name != "late" {
		t.Errorf("event_name = %q, want late (a stale id was written)", name)
	}
}

func TestDeletedStringComesBackWithANewID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	d1 := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	ev := store.Event{ID: "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e505", ProjectID: 1, Family: store.FamilyProduct,
		EventName: "again", TS: d1, ReceivedAt: d1, ActorID: "a"}
	db.WriteEvents(ctx, []store.Event{ev})
	execAll(t, db, `DELETE FROM events`)
	if err := db.CleanDictionaries(ctx, civil.MustParse("2026-10-01")); err != nil {
		t.Fatal(err)
	}
	d2 := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	ev.ID, ev.TS, ev.ReceivedAt = "0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e506", d2, d2
	if err := db.WriteEvents(ctx, []store.Event{ev}); err != nil {
		t.Fatal(err)
	}
	var name string
	db.db.QueryRow(`SELECT event_name FROM raw_product WHERE id = ?`, ev.ID).Scan(&name)
	if name != "again" {
		t.Errorf("event_name = %q", name)
	}
}
```

Check `civil` for the parse helper's real name (`civil.Parse` returning an error, or `MustParse`); use what exists.

- [ ] **Step 2: Run them to see them fail**

Run: `GOTMPDIR=~/.cache/gotmp-enc go test ./internal/store/sqlite/ -run 'TestCleanDictionaries|TestDeletedStringComesBack'`
Expected: build failure, `db.CleanDictionaries undefined`.

- [ ] **Step 3: Implement `dictionaries.go`**

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// CleanDictionaries deletes the dictionary rows no raw row can still use
// (spec D7): those whose last_seen_day is before the cutoff, the oldest
// day still held raw and never later than before (the day ahead of the raw
// window, which ingest can still write). The catalog keeps today's other
// conditions (a breakdown, or a usage row, keeps a key), and a key's values
// go with it. Ingest needs no lock against this: it resolves through an
// upsert that re-inserts a deleted text under a new id.
func (d *DB) CleanDictionaries(ctx context.Context, before civil.Date) error {
	b := before.String()
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM strings WHERE last_seen_day <
			min(?1, COALESCE((SELECT MIN(day) FROM events), ?1))`, b); err != nil {
			return fmt.Errorf("clean strings: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM attribute_values WHERE last_seen_day <
			min(?1, COALESCE((SELECT MIN(e.day) FROM events e WHERE e.project_id =
			  (SELECT project_id FROM project_attributes WHERE id = attribute_values.key_id)), ?1))`, b); err != nil {
			return fmt.Errorf("clean attribute values: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM project_attributes AS k WHERE k.breakdown = 0
			AND k.key NOT IN (SELECT attr_key FROM usage_attributes WHERE project_id = k.project_id)
			AND k.last_seen_day < min(?1, COALESCE((SELECT MIN(day) FROM events WHERE project_id = k.project_id), ?1))`, b); err != nil {
			return fmt.Errorf("clean attribute catalog: %w", err)
		}
		return nil
	})
}
```

`MIN(day) FROM events WHERE project_id = ?` cannot use the key (family comes first); if a test or `EXPLAIN QUERY PLAN` shows a scan per row, compute the per-project minimum once (`SELECT project_id, MIN(day) FROM events GROUP BY project_id` into a temp table, or `MIN` over the three families' key prefixes) and join it. Foreign keys are on in the store (cascade works); check `PRAGMA foreign_keys` in `openAt`, and delete values explicitly if not.

Move the catalog prune out of `countReceived`: delete `pruneCatalogs`/`pruneCatalog` and their call (the cleanup now owns the prune), keeping the heal step.

- [ ] **Step 4: Wire it into the daily pass**

Add `CleanDictionaries(ctx context.Context, before civil.Date) error` to `store.Store` and to `jobs.Store`; in `jobs.go` call it right after `RecordUsageHistory`:

```go
	// Dictionary rows no raw row can still use (events encoding spec D7):
	// after the rollups deleted expired days and usage was recounted.
	if err := r.store.CleanDictionaries(ctx, today.AddDays(-ret.Events.RawDays-1)); err != nil {
		return fmt.Errorf("clean dictionaries: %w", err)
	}
```

(match the surrounding error handling style; read `jobs.go` around line 230). Update fakes in `internal/jobs/*_test.go`, and add an assertion in the existing call-order test that `CleanDictionaries` runs after `RecordUsageHistory` with `before = today − RawDays − 1`.

- [ ] **Step 5: Run the tests and the check**

Run: `GOTMPDIR=~/.cache/gotmp-enc go test ./internal/store/... ./internal/jobs/...` then `make check`.
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal
git commit -m "feat(jobs): delete dictionary rows no raw row can still use in the daily pass

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Live halves on ids

Rewrites the `v_*` live halves in `041_events_encoding.sql` (unreleased, edited in place) to read `events` and group by ids, decoding only grouped rows (spec D13). `TestMigration041KeepsEveryViewRow` is the contract; this task extends its seed so every view has rows on both sides of a rollup.

**Files:**
- Modify: `internal/store/sqlite/migrations/041_events_encoding.sql` (the `v_*` definitions)
- Modify: `internal/store/sqlite/migration041_test.go` (richer seed)
- Test: `internal/store/sqlite/views_ids_test.go`

**Interfaces:**
- Consumes: Task 2's schema; Task 1's SQL fragments (paste their text).
- Produces: same view names and columns; no Go API.

- [ ] **Step 1: Extend the seed and add a view-level test**

Extend `seed040` so that every `v_*` view returns rows: several paths, browsers, OS, countries, locales, UTM values, referrers, hosts, devices, displays, app versions, platforms, consent values (0, 1, NULL), three days, more distinct values than the cap for `paths` (set `meta.attributes_top_n = '2'` in the seed so `(other)` folding is exercised), product events with breakdown attributes, measures with attributes, identified users and groups. Add `views_ids_test.go`:

```go
// TestViewsReadEventsDirectly pins D13: no v_* live half reads a raw_*
// view (they would decode every row before grouping).
func TestViewsReadEventsDirectly(t *testing.T) {
	db := newTestDB(t)
	rows, err := db.db.Query(`SELECT name, sql FROM sqlite_schema WHERE type = 'view' AND name LIKE 'v\_%' ESCAPE '\'
		AND name NOT IN ('v_events_flat', 'v_retention', 'v_identities')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, def string
		rows.Scan(&name, &def)
		if strings.Contains(def, "raw_views") || strings.Contains(def, "raw_product") || strings.Contains(def, "raw_measures") {
			t.Errorf("%s still reads a raw_* view", name)
		}
	}
}
```

- [ ] **Step 2: Run to see the new test fail**

Run: `GOTMPDIR=~/.cache/gotmp-enc go test ./internal/store/sqlite/ -run 'TestViewsReadEventsDirectly|TestMigration041'`
Expected: `TestViewsReadEventsDirectly` FAILS for every view; `TestMigration041KeepsEveryViewRow` PASSES on the richer seed (views still read `raw_*`).

- [ ] **Step 3: Rewrite each live half**

For each view, replace `FROM raw_views` (or `raw_product`, `raw_measures`) with `FROM events WHERE family = <code>`, and move decoding to after grouping. The rules, each needed for parity:
- group and count on the stored ids (`GROUP BY project_id, day, path` works on ids unchanged); `COUNT(DISTINCT actor_id)` works on the stored blob/text mix (a blob never equals a text);
- a rank tie-break that orders by the text (`ORDER BY COUNT(*) DESC, path`) must order by the decoded text: `ORDER BY COUNT(*) DESC, (SELECT value FROM strings WHERE id = path)`; `(other)` stays the literal;
- an output column decodes once per grouped row: `CASE WHEN rn <= cap THEN (SELECT value FROM strings WHERE id = path) ELSE '(other)' END`;
- `json_group_array(actor_id)` cannot hold a blob: use `json_group_array(quote(actor_id))` (an injective text per id) wherever actor lists are only counted with `COUNT(DISTINCT value)`;
- session splitting uses the integer `ts` directly where it computed `CAST(strftime('%s', ts) AS INTEGER)`;
- `v_identity_daily`'s id outputs (`user_id`, `group_id`, `actor_id`) decode with the `sqlID` text after grouping;
- `v_product_attrs`, `v_measures_attrs`: the breakdown keys come from `project_attributes` (`breakdown = 1`, by id), the value is `json_extract(e.attributes, '$."' || k.id || '"')` (a number), grouped by `(k.id, n)`; output `k.key` and `(SELECT value FROM attribute_values WHERE key_id = k.id AND n = …)`; the system keys (`$platform` … `$country`) read their `strings` columns as above; the tie-break orders by the decoded value.

Work one view at a time; after each, run `TestMigration041KeepsEveryViewRow` and `TestSystemDashboards`.

- [ ] **Step 4: Run everything**

Run: `GOTMPDIR=~/.cache/gotmp-enc go test ./internal/store/... ./internal/reporting/... ./internal/api/...` then `make check`.
Expected: PASS, `TestViewsReadEventsDirectly` included.

- [ ] **Step 5: Measure**

On a database of a few hundred thousand seeded rows (extend `bench_test.go` with a benchmark that seeds through `WriteEvents` and times `SELECT * FROM v_views_paths WHERE project_id = 1 AND day BETWEEN …` and `v_product_attrs` over the whole range), compare against `main` (`git stash`-free: run the benchmark on a `main` worktree). Record both numbers in the commit message body. A view slower than on `main` is a defect to fix before committing.

- [ ] **Step 6: Commit**

```bash
git add internal
git commit -m "perf(store): group the views' live halves by stored ids and decode only grouped rows

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Rollups and attribute usage on ids

The rollups and the attribute usage count read `raw_*` after Task 2, decoding every row (the attribute JSON rebuild is the slow path). Move them to `events` with ids, keeping their text outputs (spec D14).

**Files:**
- Modify: `internal/store/sqlite/aggregate_views.go`, `aggregate_product.go`, `aggregate_measures.go` (the rollup SELECTs), `usage_attributes.go` (`receivedSQL`), `usage_history.go` (the counts that read `raw_*`)
- Test: existing `aggregate_*_test.go`, `usage_attributes` tests, plus `internal/store/sqlite/rollup_ids_test.go`

**Interfaces:**
- Consumes: Task 2's schema, Task 4's patterns.
- Produces: no API change.

- [ ] **Step 1: Write a parity test**

`rollup_ids_test.go`: seed one day of rich rows through `WriteEvents` into two copies of `newTestDB`; on copy A, compute the expected aggregates by running today's rollup SQL text against `raw_*` (copy the current statements into the test as constants before changing them); on copy B run `AggregateViewDay`, `AggregateProductDay`, `AggregateMeasureDay` and `RecordUsageHistory`; require identical `agg_*` and `usage_attributes` rows. Run it: it PASSES now (both read `raw_*`), and must keep passing after Step 2.

- [ ] **Step 2: Move each rollup to `events`**

Same rules as Task 4 Step 3 (group by ids, decode outputs, tie-breaks on text, `quote()` for actor lists). `receivedSQL`: walk `json_each(e.attributes)` of encoded rows, join `project_attributes` by id for the key and count distinct `j.value` (numbers) for `max_values`; the reserved columns join `strings`. Keep each function's doc comment accurate.

- [ ] **Step 3: Run tests and check**

Run: `GOTMPDIR=~/.cache/gotmp-enc go test ./internal/store/... ./internal/jobs/...` then `make check`.
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal
git commit -m "perf(store): roll up raw days and count attribute usage on stored ids

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Widget SQL reads only `raw_*`, `v_*` and `agg_*`

**Files:**
- Create: `internal/shared/readsql/scoped.go`
- Test: `internal/shared/readsql/scoped_test.go`
- Modify: `internal/api/server.go` (open a widget handle; pass both to reporting), `internal/reporting/reporting.go` (`Options.Trusted *readsql.DB`; `sampleProject` uses it), `internal/reporting/source.go` (`sampleProject` takes the trusted handle), `internal/app/migrate.go:42` (system widgets validate on the scoped handle), `internal/app/reporting_dev.go:32` (dev handle scoped)
- Modify: `internal/reporting/system/users/top-users.sql`, `internal/reporting/system/groups/top-groups.sql` (`identities` → `v_identities`)
- Docs: `docs/reporting.md` (widget SQL rule), `docs/twillingate.md` (widget SQL vs `query`), widget tools' SQL descriptions in `internal/api/ops_reporting.go`

**Interfaces:**
- Consumes: `v_identities` (Task 2).
- Produces:
  - `const ScopedSchema = "tw_store"` (package `readsql`).
  - `func OpenScoped(path string, timeout time.Duration, maxRows int, refused ...string) (*DB, error)` — a handle whose connections see only the database's `raw_*`, `v_*` and `agg_*` objects, as temporary views over `tw_store`; its `Check` also refuses `tw_store` wherever a name can appear.
  - `reporting.Options.Trusted *readsql.DB` — the full handle for Go-written SQL (nil means the widget handle).

- [ ] **Step 1: Write the failing tests**

```go
package readsql

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func scopedFixture(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.db")
	w, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE events (project_id INTEGER, day TEXT, path TEXT, PRIMARY KEY (project_id, day, path)) WITHOUT ROWID`,
		`CREATE TABLE identities (id TEXT, name TEXT)`,
		`CREATE TABLE agg_views_paths (project_id INTEGER, day TEXT, path TEXT, views INTEGER)`,
		`CREATE VIEW raw_views AS SELECT * FROM events`,
		`CREATE VIEW v_identity_daily AS SELECT 1 AS project_id, '2026-10-11' AS day, 3 AS events, 2 AS actors`,
		`INSERT INTO events VALUES (1, '2026-10-11', '/a')`,
	} {
		if _, err := w.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	db, err := OpenScoped(path, 5*time.Second, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestScopedAllowsOnlyViewsAndAggregates(t *testing.T) {
	db := scopedFixture(t)
	ctx := context.Background()
	for _, q := range []string{
		`SELECT path FROM raw_views WHERE project_id = 1`,
		`SELECT events, actors FROM v_identity_daily`, // columns named like tables
		`SELECT * FROM agg_views_paths`,
		`WITH events AS (SELECT 1 AS x) SELECT x FROM events`, // a CTE may take any name
	} {
		if _, err := db.Query(ctx, q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
	for _, q := range []string{
		`SELECT * FROM events`,
		`SELECT * FROM identities`,
		`SELECT * FROM main.events`,
		`SELECT * FROM sqlite_temp_master`,
	} {
		if _, err := db.Query(ctx, q); err == nil {
			t.Errorf("%s: want an error", q)
		}
	}
	for _, q := range []string{
		`SELECT * FROM tw_store.events`,
		`SELECT * FROM "tw_store".events`,
		`SELECT * FROM [tw_store].events`,
		`SELECT * FROM TW_STORE.events`,
		"SELECT * FROM `tw_store`.events",
	} {
		if _, err := db.Query(ctx, q); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v, want ErrRefused", q, err)
		}
	}
}

func TestScopedFiltersReachTheTable(t *testing.T) {
	db := scopedFixture(t)
	res, err := db.Run(context.Background(), `EXPLAIN QUERY PLAN SELECT path FROM raw_views WHERE project_id = 1 AND day = '2026-10-11'`)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for _, r := range res.Rows {
		plan.WriteString(strings.Join(r, " ") + "\n")
	}
	if !strings.Contains(plan.String(), "SEARCH") {
		t.Errorf("plan scans instead of seeking:\n%s", plan.String())
	}
}
```

Note the `sqlite_temp_master` case: the temporary views are visible there; refuse it (it is already a SQLite internal name Check refuses — confirm).

- [ ] **Step 2: Run them to see them fail**

Run: `GOTMPDIR=~/.cache/gotmp-enc go test ./internal/shared/readsql/ -run TestScoped`
Expected: build failure, `undefined: OpenScoped`.

- [ ] **Step 3: Implement `scoped.go`**

```go
package readsql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// ScopedSchema is the name the database file is attached under on a
// scoped handle. Check refuses it on that handle, so custom SQL reaches
// the file only through the temporary views OpenScoped creates.
const ScopedSchema = "tw_store"

// OpenScoped opens a handle for widget SQL (events encoding spec D15):
// each connection is an empty in-memory database with the file attached
// read-only as ScopedSchema, and a temporary view per raw_*, v_* and agg_*
// object of the file (CREATE TEMP VIEW x AS SELECT * FROM tw_store.x).
// Every other table is "no such table". The file's own views resolve inside
// their schema, and SELECT * follows column changes (v_events_flat's
// rebuilds). A view added to the file after a connection opened is seen by
// the connections opened after it; migrations run before serving.
func OpenScoped(path string, timeout time.Duration, maxRows int, refused ...string) (*DB, error) {
	c := &scopedConnector{path: path}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(4)
	names := map[string]bool{strings.ToLower(ScopedSchema): true}
	for _, n := range refused {
		names[strings.ToLower(n)] = true
	}
	return &DB{db: db, timeout: timeout, maxRows: maxRows, refused: names}, nil
}

type scopedConnector struct{ path string }

func (c *scopedConnector) Driver() driver.Driver { return &sqlite.Driver{} }

func (c *scopedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := (&sqlite.Driver{}).Open("file::memory:?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	ex := conn.(driver.ExecerContext)
	q := conn.(driver.QueryerContext)
	run := func(s string) error { _, err := ex.ExecContext(ctx, s, nil); return err }
	uri := "file:" + strings.ReplaceAll(c.path, "'", "''") + "?mode=ro"
	if err := run(fmt.Sprintf(`ATTACH DATABASE '%s' AS %s`, uri, ScopedSchema)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("readsql: attach %s: %w", c.path, err)
	}
	rows, err := q.QueryContext(ctx, `SELECT name FROM `+ScopedSchema+`.sqlite_schema
		WHERE type IN ('table', 'view') AND (name LIKE 'raw\_%' ESCAPE '\' OR name LIKE 'v\_%' ESCAPE '\' OR name LIKE 'agg\_%' ESCAPE '\')`, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	var names []string
	vals := make([]driver.Value, 1)
	for rows.Next(vals) == nil {
		names = append(names, fmt.Sprint(vals[0]))
	}
	rows.Close()
	for _, n := range names {
		// n comes from the file's schema; every object name there was
		// written by a migration or RebuildFlatView, both [a-z0-9_].
		if err := run(fmt.Sprintf(`CREATE TEMP VIEW "%s" AS SELECT * FROM %s."%s"`, n, ScopedSchema, n)); err != nil {
			conn.Close()
			return nil, fmt.Errorf("readsql: scope %s: %w", n, err)
		}
	}
	for _, p := range []string{`PRAGMA query_only = 1`, `PRAGMA trusted_schema = 0`} {
		if err := run(p); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}
```

Adapt to the driver's actual API: check `modernc.org/sqlite`'s `Driver.Open` and whether its conn implements `driver.ExecerContext`/`QueryerContext` (it does in v1.57; read `$(go env GOMODCACHE)/modernc.org/sqlite@v1.57.0/sqlite.go`). Match `Open`'s `_defensive` behaviour if the driver exposes a DSN parameter for it on the in-memory connection. Refuse a name outside `[a-z0-9_]` instead of quoting it, to keep the trust argument simple.

- [ ] **Step 4: Run the readsql tests**

Run: `GOTMPDIR=~/.cache/gotmp-enc go test ./internal/shared/readsql/`
Expected: PASS. If `TestScopedFiltersReachTheTable` fails (filters not pushed through the temp view), switch to the spec's fallback: a `Check` rule refusing non-allowed names only in table positions, and record why in the commit.

- [ ] **Step 5: Wire the handles**

- `internal/api/server.go` `Build`: open `widgets, err := readsql.OpenScoped(cfg.Console.DBPath, cfg.Console.QueryTimeout, cfg.Console.QueryMaxRows, CustomSQLRefused()...)`; close it in `closeAll`; `reporting.New(rst, widgets, reporting.Options{…, Trusted: db})`. The `query` tool keeps `h.db`.
- `internal/reporting`: `Options.Trusted`; `sampleProject` runs on `Trusted` (falling back to the widget handle when nil); update the comment at `source.go:288-295`.
- `internal/app/migrate.go:42`: system widgets validate on `readsql.OpenScoped`, so a system widget naming a refused table fails the boot exactly as it would fail at run time.
- `internal/app/reporting_dev.go:32`: `OpenScoped`.
- The two system widgets: `LEFT JOIN v_identities i` in `top-users.sql` and `top-groups.sql`.

- [ ] **Step 6: Docs**

- `docs/reporting.md`: widget SQL reads `raw_*`, `v_*` and `agg_*` only; any other table is "no such table"; display names through `v_identities`.
- `docs/twillingate.md`: the custom SQL section says `query` reads every table but `meta`, SQLite's internals and `submissions`, while widget SQL reads only `raw_*`, `v_*`, `agg_*`.
- Widget tool descriptions in `internal/api/ops_reporting.go` that describe the SQL a widget may read.
- `deploy/UPGRADES.md` 041 entry: add the widget SQL restriction to the breaking changes.

- [ ] **Step 7: Run everything**

Run: `GOTMPDIR=~/.cache/gotmp-enc go test ./internal/...` then `make check`, then `cd web && npm run e2e` (needs a built binary: `make build` first; read `web/playwright.config.*` for how it starts the server; port 18080 may be held by another session's stale server — use the config's override if so).
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal docs deploy
git commit -m "feat(reporting)!: let widget sql read only the raw_, v_ and agg_ views and tables

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Size check and final verification

**Files:**
- Test: `internal/store/sqlite/size_test.go`

- [ ] **Step 1: Write the size test**

```go
// TestEncodedRowsAreSmall pins the encoding's point: a prod-shaped batch
// stores in under half today's 491 bytes per row (dbstat counts the
// table's pages).
func TestEncodedRowsAreSmall(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	execAll(t, db, `INSERT INTO projects (id, name, allowed_origins) VALUES (1, 'p', '[]')`)
	// 2,000 product events shaped like econumo's: 14 attributes of ~10-char
	// keys and ~5-char values drawn from small sets, UUID ids, 32-hex actors.
	evs := prodShapedEvents(2000)
	if err := db.WriteEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	var bytes int64
	if err := db.db.QueryRow(`SELECT SUM(pgsize) FROM dbstat WHERE name = 'events'`).Scan(&bytes); err != nil {
		t.Skipf("dbstat unavailable: %v", err)
	}
	if per := bytes / int64(len(evs)); per > 245 {
		t.Errorf("events takes %d bytes per row, want under 245", per)
	}
}
```

Write `prodShapedEvents(n int) []store.Event` in the same file (deterministic: keys from a fixed list of 14, values from 3–40 per key, paths from 50, ids from a counter formatted as UUIDs).

- [ ] **Step 2: Run it**

Run: `GOTMPDIR=~/.cache/gotmp-enc go test ./internal/store/sqlite/ -run TestEncodedRowsAreSmall -v`
Expected: PASS. If it fails, report the measured number rather than raising the threshold.

- [ ] **Step 3: Full verification**

Run: `make check`; `GOTMPDIR=~/.cache/gotmp-enc go test -race ./internal/store/sqlite/ ./internal/api/ ./internal/reporting/` with `time`, each package under 10 minutes (CLAUDE.md); `cd web && npm run e2e`.
Expected: all pass; record the race timings in the PR body.

- [ ] **Step 4: Commit**

```bash
git add internal/store/sqlite/size_test.go
git commit -m "test(store): pin the encoded events row size

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
