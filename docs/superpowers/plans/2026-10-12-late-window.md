# Late Window Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A late event can never land in a day the daily pass already rolled up, whatever `RETENTION_RAW_DAYS` is set to.

**Architecture:** Ingest clamps a client timestamp to `[received − max_event_age, received + 5 min]`. Today `max_event_age` is the raw window (`RETENTION_RAW_DAYS`), so raising the window after a rollup lets a late event into a rolled-up day, and the next rollup rewrites that day's aggregates from the late row alone (confirmed by `TestAggregateProductLateRowAfterRollupRewritesDay`, skipped in the tree). This plan introduces `INGEST_LATE_DAYS` (default 1, at least 1, at most `RETENTION_RAW_DAYS`) as the clamp's bound. Rollups roll up days older than `RETENTION_RAW_DAYS`; with the late window no larger than that, a day being rolled up receives no more rows. No migration, no change to the rollup code.

**Tech Stack:** Go 1.26, modernc SQLite (unchanged), `internal/config`, `internal/server`, `internal/api` (the `limits` tool), the web `LimitsPanel`.

**Spec:** `docs/superpowers/specs/2026-10-12-raw-first-design.md`, D3 ("A late window, separate from retention"), Problem (last item), Next steps step 0; roadmap `docs/superpowers/plans/2026-10-12-raw-first-roadmap.md`, PR 1.

## Global Constraints

- `INGEST_LATE_DAYS`: default 1; refused below 1; refused above `RETENTION_RAW_DAYS` (D3, D23). Consequence: `RETENTION_RAW_DAYS=0` is refused too, since no late window fits under it (runbook).
- Clamped events are clamped and counted, never dropped (unchanged; `docs/twillingate.md` "Timestamps and idempotency").
- Conventional Commits: `feat(config): …`, `fix(server): …`, `test(store): …`, `docs: …`; scopes match the tree.
- A change to a contract page lands in the same commit as the code it describes (CLAUDE.md "Documentation"): the config change and `docs/deployment.md`'s variable table in one commit; the clamp and `docs/twillingate.md`'s timestamp paragraph in one commit. `internal/api/docs_sync_test.go` (`TestDeploymentDocumentsEveryEnvVar`) fails `make check` when the table and `config.go` disagree.
- `make check` before pushing.

## Review Focus

Inputs the spec implies but no task's test exercises, each pinned to a test in the task that owns the code:

1. `INGEST_LATE_DAYS` set and `RETENTION_RAW_DAYS` left at its default 30: must load with a 1–30 day window (Task 1, `TestLateDaysWithinDefaultRawWindow`).
2. `RETENTION_RAW_DAYS=0` ("only today raw"): must now be refused with a message naming both variables, since the pass would roll up yesterday while a 1-day window still accepts events into it (Task 1, invalid case `zero raw window under the late window`).
3. A late event exactly at the boundary, `received − INGEST_LATE_DAYS`: accepted unclamped (Task 2, `TestClampTS` keeps "exactly at the boundary", now at the late window's size).
4. The `limits` tool must list the new setting with its value in force and default, in the `ingest` group, and every entry after the settings must still be a fixed limit (Task 4, `TestLimitsReportsTheLimitsInForce`).
5. The daily pass after the upgrade: a day rolled up before, then a late event clamped to yesterday, then the pass: yesterday is raw and unrolled, the old day untouched. Covered end to end by Task 2's `TestRaisedRawWindowDoesNotMoveTheClamp` (the clamp) plus the unchanged rollup threshold; no new pass test is needed, the existing `TestRunDailyPassAggregatesEveryFamilyByOneWindow` in `internal/jobs` keeps the threshold pinned.

---

### Task 1: `INGEST_LATE_DAYS` in config

**Files:**
- Modify: `internal/config/config.go` (field near `IngestDisabled` ~line 143; defaults block ~line 131; parse ~line 238; validation ~line 350; `MaxEventAge` ~line 386)
- Modify: `internal/config/config_test.go`
- Modify: `docs/deployment.md` (variable table ~line 103)

**Interfaces:**
- Produces: `Config.IngestLateDays int`, `config.DefaultIngestLateDays = 1`, `(*Config).MaxEventAge() time.Duration` now `IngestLateDays × 24h`. Task 2 and Task 4 use these names.

- [ ] **Step 1: Write the failing tests**

In `internal/config/config_test.go`, extend `TestDefaultsApplied` after the retention check:

```go
	if c.IngestLateDays != 1 {
		t.Errorf("IngestLateDays = %d, want 1", c.IngestLateDays)
	}
```

Add to the invalid `cases` map in the test that iterates `cases` with `FromEnv` (the one holding `"negative raw_days"`):

```go
		"zero late days":                         base(map[string]string{"INGEST_LATE_DAYS": "0"}),
		"negative late days":                     base(map[string]string{"INGEST_LATE_DAYS": "-1"}),
		"late days exceed the raw window":        base(map[string]string{"RETENTION_RAW_DAYS": "3", "INGEST_LATE_DAYS": "4"}),
		"zero raw window under the late window":  base(map[string]string{"RETENTION_RAW_DAYS": "0"}),
```

Add three tests at the end of the file:

```go
// TestLateDaysWithinDefaultRawWindow: the late window alone is set, the
// raw window keeps its default 30, so any value from 1 to 30 loads.
func TestLateDaysWithinDefaultRawWindow(t *testing.T) {
	for _, v := range []string{"1", "7", "30"} {
		c, err := load(t, map[string]string{"INGEST_LATE_DAYS": v})
		if err != nil {
			t.Fatalf("INGEST_LATE_DAYS=%s: %v", v, err)
		}
		if c.Retention.Events.RawDays != 30 {
			t.Errorf("RawDays = %d, want the default 30", c.Retention.Events.RawDays)
		}
	}
}

// TestLateDaysMessageNamesBothVariables: a window larger than the raw
// window is refused by name, so the operator knows which to change.
func TestLateDaysMessageNamesBothVariables(t *testing.T) {
	_, err := load(t, map[string]string{"RETENTION_RAW_DAYS": "3", "INGEST_LATE_DAYS": "4"})
	if err == nil || !strings.Contains(err.Error(), "INGEST_LATE_DAYS") || !strings.Contains(err.Error(), "RETENTION_RAW_DAYS") {
		t.Errorf("err = %v, want a refusal naming INGEST_LATE_DAYS and RETENTION_RAW_DAYS", err)
	}
}

// TestMaxEventAgeIsTheLateWindow: the clamp follows the late window, not
// the raw window, so raising RETENTION_RAW_DAYS moves no clamp.
func TestMaxEventAgeIsTheLateWindow(t *testing.T) {
	c, err := load(t, map[string]string{"INGEST_LATE_DAYS": "2", "RETENTION_RAW_DAYS": "400"})
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxEventAge() != 48*time.Hour {
		t.Errorf("MaxEventAge = %v, want 48h", c.MaxEventAge())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config -run 'TestDefaultsApplied|TestLateDays|TestMaxEventAge|TestInvalid' -v 2>&1 | tail -20`
Expected: compile error `c.IngestLateDays undefined` (the field does not exist yet).

- [ ] **Step 3: Add the field, the default, the parse, the validation, and point `MaxEventAge` at it**

In `internal/config/config.go`:

Beside `IngestDisabled` (the field with the comment `// IngestDisabled (INGEST_DISABLED) refuses every ingest route.`), add:

```go
	// IngestLateDays (INGEST_LATE_DAYS) is how far back a client timestamp
	// may lie: older ones are clamped to received − IngestLateDays. At most
	// Retention.Events.RawDays, so a late event never reaches a day the
	// daily pass has rolled up (raw-first spec, D3).
	IngestLateDays int `json:"ingest_late_days"`
```

In the retention defaults `const` block (the one with `DefaultRawDays = 30`), add:

```go
	DefaultIngestLateDays = 1
```

Where `IngestDisabled: e.bool("INGEST_DISABLED", false),` is parsed, add the line after it:

```go
		IngestLateDays: e.num("INGEST_LATE_DAYS", DefaultIngestLateDays),
```

In the validation function, directly after the block

```go
	if rc := c.Retention.Events; rc.RawDays < 0 || rc.AggregateDays < 0 {
		return fmt.Errorf("config: retention days must not be negative: %+v", rc)
	}
```

add:

```go
	if c.IngestLateDays < 1 {
		return fmt.Errorf("config: INGEST_LATE_DAYS must be at least 1: %d", c.IngestLateDays)
	}
	if c.IngestLateDays > c.Retention.Events.RawDays {
		return fmt.Errorf("config: INGEST_LATE_DAYS (%d) exceeds RETENTION_RAW_DAYS (%d): a late event could land in a day already rolled up",
			c.IngestLateDays, c.Retention.Events.RawDays)
	}
```

Replace `MaxEventAge` and its comment:

```go
// MaxEventAge is the late window (INGEST_LATE_DAYS): how far back a client
// timestamp may lie before it is clamped. Validation keeps it within the raw
// window, so a clamped timestamp never lands in a day already rolled up.
func (c *Config) MaxEventAge() time.Duration {
	return time.Duration(c.IngestLateDays) * 24 * time.Hour
}
```

- [ ] **Step 4: Run the config tests**

Run: `go test ./internal/config 2>&1 | tail -5`
Expected: `ok`.

- [ ] **Step 5: Document the variable in the same commit**

In `docs/deployment.md`, in the variable table, replace the `RETENTION_RAW_DAYS` row and add a row before it:

```markdown
| `INGEST_LATE_DAYS` | Days back a client timestamp may lie; an older one is clamped to this edge, counted, never dropped. At least 1 and at most `RETENTION_RAW_DAYS`, so a late event never lands in a day already rolled up. Default 1. |
| `RETENTION_RAW_DAYS` | Days raw events of every family are kept before rollup. At least `INGEST_LATE_DAYS`. Default 30. |
```

- [ ] **Step 6: Run the docs binding and vet**

Run: `go test ./internal/api -run TestDeploymentDocumentsEveryEnvVar 2>&1 | tail -3 && go vet ./internal/config`
Expected: `ok` for both.

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go docs/deployment.md
git commit -m "feat(config): add INGEST_LATE_DAYS, the clamp's bound, at most the raw window"
```

---

### Task 2: the clamp follows the late window

**Files:**
- Modify: `internal/server/handlers.go:87-90` (the comment above `maxAge := s.cfg.MaxEventAge()`)
- Modify: `internal/server/ingest.go:298-303` (the comment above `clampTS`)
- Modify: `internal/server/server_test.go` (`TestClientTimestampIsUsedAndClamped`, `TestEventAgeClampUsesGlobalRawWindow`)
- Modify: `docs/twillingate.md` ("Timestamps and idempotency", ~line 1203)

**Interfaces:**
- Consumes: `Config.MaxEventAge()` from Task 1 (already the late window; `clampTS(client, received time.Time, maxAge time.Duration) (time.Time, bool)` is unchanged).
- Produces: nothing new; the behaviour change is `maxAge`'s source.

- [ ] **Step 1: Write the failing test**

In `internal/server/server_test.go`, after `TestEventAgeClampUsesGlobalRawWindow`, add:

```go
// TestRaisedRawWindowDoesNotMoveTheClamp is the raw-first spec's step 0 at
// the ingest boundary: with RETENTION_RAW_DAYS raised to 400 after days
// were rolled up, a 40-day-old event must still clamp to the late window
// (1 day by default), never into a rolled-up day.
func TestRaisedRawWindowDoesNotMoveTheClamp(t *testing.T) {
	cfg := configtest.Load(t, map[string]string{"RETENTION_RAW_DAYS": "400"})
	reg := newTestRegistry(t,
		[]manage.ProjectSpec{{Name: "Raised", AllowedOrigins: []string{testOrigin}}},
		map[int][2]string{0: {"ak_raised", "web"}})
	g, _ := geo.New("cloudflare://", t.TempDir(), slog.Default())
	q := &fakeQueue{}
	h := New(cfg, reg, q, g, fixedSalt{}, q, newFakeForms(), slog.Default())

	oldTS := time.Now().UTC().AddDate(0, 0, -40).Format(time.RFC3339)
	w := post(h, `{"key":"ak_raised","events":[{"name":"$page_view","ts":"`+oldTS+`","attributes":{"$path":"/x"}}]}`, nil)
	res := decodeResult(t, w)
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Reason, "clamped") {
		t.Fatalf("warnings = %+v, want a clamp warning", res.Warnings)
	}
	if len(q.views) != 1 {
		t.Fatalf("views = %+v", q.views)
	}
	age := time.Since(q.views[0].TS)
	if age < 24*time.Hour || age > 24*time.Hour+time.Minute {
		t.Errorf("clamped view age = %v, want ~1 day (INGEST_LATE_DAYS), not the 400-day raw window", age)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/server -run TestRaisedRawWindowDoesNotMoveTheClamp -v 2>&1 | tail -5`
Expected: with Task 1 landed this **passes** already, because `MaxEventAge` changed. Confirm by running it against the parent commit: `git stash && go test ./internal/server -run TestRaisedRawWindowDoesNotMoveTheClamp 2>&1 | tail -3; git stash pop` — expected on the parent: FAIL with `clamped view age = 9600h…, want ~1 day`. (If `git stash` is awkward, skip this and rely on `TestEventAgeClampUsesLateWindow` below, which fails until Step 3.)

- [ ] **Step 3: Rename the window test and fix the 31-day bound**

Replace `TestEventAgeClampUsesGlobalRawWindow` entirely:

```go
// TestEventAgeClampUsesLateWindow verifies the clamp follows the configured
// late window rather than a hard-coded default or the raw window: an
// operator who shortens INGEST_LATE_DAYS shortens how far back a
// clock-skewed late event can land.
func TestEventAgeClampUsesLateWindow(t *testing.T) {
	cfg := configtest.Load(t, map[string]string{"INGEST_LATE_DAYS": "3"})
	reg := newTestRegistry(t,
		[]manage.ProjectSpec{
			{Name: "Clamped", AllowedOrigins: []string{testOrigin}},
			{Name: "Normal", AllowedOrigins: []string{testOrigin}},
		},
		map[int][2]string{0: {"ak_clamped", "web"}, 1: {"ak_normal", "web"}})
	g, _ := geo.New("cloudflare://", t.TempDir(), slog.Default())
	q := &fakeQueue{}
	h := New(cfg, reg, q, g, fixedSalt{}, q, newFakeForms(), slog.Default())

	oldTS := time.Now().UTC().AddDate(0, 0, -10).Format(time.RFC3339)
	recentTS := time.Now().UTC().AddDate(0, 0, -1).Format(time.RFC3339)

	// A 10-day-old event must clamp to the 3-day window, not the 1-day
	// default and not the 30-day raw window.
	w := post(h, `{"key":"ak_clamped","events":[{"name":"$page_view","ts":"`+oldTS+`","attributes":{"$path":"/x"}}]}`, nil)
	res := decodeResult(t, w)
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Reason, "clamped") {
		t.Fatalf("warnings = %+v, want a clamp warning", res.Warnings)
	}
	if len(q.views) != 1 || q.views[0].ProjectID != 1 {
		t.Fatalf("views = %+v", q.views)
	}
	age := time.Since(q.views[0].TS)
	if age < 3*24*time.Hour || age > 3*24*time.Hour+time.Minute {
		t.Errorf("clamped view age = %v, want ~3 days (the configured late window)", age)
	}

	// Control: the window applies to every project alike, and a timestamp
	// inside it passes through as-is.
	w = post(h, `{"key":"ak_normal","events":[{"name":"$page_view","ts":"`+recentTS+`","attributes":{"$path":"/x"}}]}`, nil)
	res = decodeResult(t, w)
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none: 1 day is within the 3-day window", res.Warnings)
	}
	if len(q.views) != 2 || q.views[1].ProjectID != 2 {
		t.Fatalf("views = %+v", q.views)
	}
	got, err := time.Parse(time.RFC3339, recentTS)
	if err != nil {
		t.Fatal(err)
	}
	if !q.views[1].TS.Equal(got) {
		t.Errorf("unclamped ts = %v, want the client value %v", q.views[1].TS, got)
	}
}
```

In `TestClientTimestampIsUsedAndClamped`, replace the comment and the bound:

```go
	// Clamped, not dropped, and never older than the late window
	// (INGEST_LATE_DAYS, 1 day by default).
	if q.events[1].TS.Before(q.events[1].ReceivedAt.Add(-2 * 24 * time.Hour)) {
		t.Errorf("clamped ts = %v, older than the late window", q.events[1].TS)
	}
```

Also change its first comment, which says the fixed date "would age out of the raw window", to "would age out of the late window".

- [ ] **Step 4: Update the two comments in the code**

`internal/server/handlers.go`, replace

```go
	// The clamp is the raw window (RETENTION_RAW_DAYS): a clamped
	// event can never target a day the daily pass already aggregated and
	// deleted.
```

with

```go
	// The clamp is the late window (INGEST_LATE_DAYS), at most the raw
	// window: a clamped event can never target a day the daily pass already
	// aggregated and deleted.
```

`internal/server/ingest.go`, replace the sentence in `clampTS`'s comment

```go
// Out-of-range values are clamped and counted, never dropped: a device with
// a broken clock still contributes. The lower bound is the raw window
// (RETENTION_RAW_DAYS), which is what guarantees a clamped event can
// never target a day that has already been aggregated and had its raw rows
// deleted.
```

with

```go
// Out-of-range values are clamped and counted, never dropped: a device with
// a broken clock still contributes. The lower bound is the late window
// (INGEST_LATE_DAYS), kept within the raw window by config, which is what
// guarantees a clamped event can never target a day that has already been
// aggregated and had its raw rows deleted.
```

- [ ] **Step 5: Run the server tests**

Run: `go test ./internal/server 2>&1 | tail -5`
Expected: `ok`. `TestClampTS` is unchanged: it passes its own `maxAge` and its "exactly at the boundary" case still holds (Review Focus 3).

- [ ] **Step 6: Update the contract page in the same commit**

In `docs/twillingate.md`, "Timestamps and idempotency", replace

```markdown
The server clamps it to `[received − max_event_age,
received + 5 minutes]`, records the server clock separately, and **clamps and
counts, never drops**, out-of-range values; `max_event_age` equals
`RETENTION_RAW_DAYS` (30 days by default), so a clamped event can never
target a day already rolled up.
```

with

```markdown
The server clamps it to `[received − max_event_age,
received + 5 minutes]`, records the server clock separately, and **clamps and
counts, never drops**, out-of-range values; `max_event_age` is
`INGEST_LATE_DAYS` (1 day by default, at most `RETENTION_RAW_DAYS`), so a
clamped event can never target a day already rolled up.
```

- [ ] **Step 7: Commit**

```bash
git add internal/server/handlers.go internal/server/ingest.go internal/server/server_test.go docs/twillingate.md
git commit -m "fix(server): clamp late events to INGEST_LATE_DAYS, not the raw window

Raising RETENTION_RAW_DAYS after a rollup let a late event into a
rolled-up day, and the next rollup rewrote that day's aggregates from
the late row alone."
```

---

### Task 3: retire the reproduction test, pin the invariant where the rollup reads it

**Files:**
- Modify: `internal/store/sqlite/aggregate_product_test.go` (delete `TestAggregateProductLateRowAfterRollupRewritesDay`, the last test in the file)
- Modify: `internal/store/sqlite/aggregate_product.go:39-47` (a comment above the raw count)

**Interfaces:** none.

- [ ] **Step 1: Delete the skipped test**

The test landed with the spec PR (#176, branch `feat/raw-archive`), so this plan's branch starts from `feat/raw-archive` (or from `main` once #176 has merged); if the test is absent, this step is done. Remove `TestAggregateProductLateRowAfterRollupRewritesDay` and its doc comment from `internal/store/sqlite/aggregate_product_test.go`. The scenario it reproduced (a raw row written into a rolled-up day) can no longer be produced through ingest; `TestRaisedRawWindowDoesNotMoveTheClamp` (Task 2) pins the fix at the boundary that matters. The store-level rewrite itself is unchanged and goes with the rollups in PR 6.

- [ ] **Step 2: Say why the count check is enough**

In `internal/store/sqlite/aggregate_product.go`, above the `SELECT COUNT(*) FROM raw_product …` check whose `n == 0` means "already aggregated (or empty day)", add:

```go
	// A day older than the raw window receives no new rows: ingest clamps
	// client timestamps to INGEST_LATE_DAYS, which config keeps within
	// RETENTION_RAW_DAYS. So "no raw rows" is the only "already done" this
	// rollup needs; a row arriving here after a rollup would otherwise
	// replace the day's aggregates with its own counts.
```

- [ ] **Step 3: Run the store tests**

Run: `go test ./internal/store/sqlite -run 'TestAggregateProduct' 2>&1 | tail -3`
Expected: `ok`, and `go vet ./internal/store/...` clean.

- [ ] **Step 4: Commit**

```bash
git add internal/store/sqlite/aggregate_product_test.go internal/store/sqlite/aggregate_product.go
git commit -m "test(store): drop the late-row rollup reproduction; the late window prevents it"
```

---

### Task 4: `limits` reports the late window

**Files:**
- Modify: `internal/api/ops_limits.go:73-78` (`limitsFrom`)
- Modify: `internal/api/ops_limits_test.go:27-49`
- Modify: `web/src/components/projects/LimitsPanel.tsx:6` (the `ingest` group's note)
- Modify: `docs/twillingate.md` (the `limits` row of the tools table, ~line 1425)

**Interfaces:**
- Consumes: `cfg.IngestLateDays`, `config.DefaultIngestLateDays` (Task 1); `setting(group, name, env string, value, def int, unit, zero, description string) limitOut`; `groupIngest`.

- [ ] **Step 1: Write the failing test**

In `internal/api/ops_limits_test.go`, `TestLimitsReportsTheLimitsInForce`: set the late window on the config and expect it as the last setting, before the fixed limits.

Change the config construction to:

```go
	cfg := &config.Config{AttributeValuesTopN: 7, IdentitiesTopN: 2000, AttributeBreakdownsMax: 12,
		IngestLateDays: 2, Forms: config.FormsConfig{DraftDays: 3}}
```

Add to the end of the `settings` slice:

```go
		{groupIngest, "INGEST_LATE_DAYS", 2, config.DefaultIngestLateDays},
```

The loop after it already requires every later entry to be a fixed `ingest` limit with no setting, so the new row's position (after the caps, before the fixed limits) is pinned.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/api -run TestLimitsReportsTheLimitsInForce 2>&1 | tail -5`
Expected: FAIL: `limits[7] = {Group:ingest Name:Request body …}, want ingest INGEST_LATE_DAYS 2 (default 1)`.

- [ ] **Step 3: Add the setting and trim the raw window's description**

In `internal/api/ops_limits.go`, `limitsFrom`: change the first setting's description to

```go
			"raw events are kept this long, then rolled up into aggregates"),
```

and insert, after the `"Users and groups"` setting and before the first `fixed(…)`:

```go
		setting(groupIngest, "Late events", "INGEST_LATE_DAYS", cfg.IngestLateDays, config.DefaultIngestLateDays, "days", "",
			"days back a client timestamp may lie; an older one is clamped to this edge, so no event lands in a day already rolled up"),
```

Update the function's comment to `// order the console shows them: retention, the caps, the late window, then the wire format's fixed limits.`

- [ ] **Step 4: Run the api tests**

Run: `go test ./internal/api -run 'Limits' 2>&1 | tail -3`
Expected: `ok`.

- [ ] **Step 5: The console's note and the contract page**

`web/src/components/projects/LimitsPanel.tsx`, line 6: replace the `ingest` group's note

```ts
  { group: 'ingest', title: 'Ingest', note: 'Fixed by the wire format, the same on every server.' },
```

with

```ts
  { group: 'ingest', title: 'Ingest', note: 'The late window, set in the environment, then the wire format’s limits, the same on every server.' },
```

`docs/twillingate.md`, the `limits` row: replace `and \`ingest\`, the wire format's fixed [limits](#limits) and [form limits](#form-submissions).` with `and \`ingest\`: \`INGEST_LATE_DAYS\`, then the wire format's fixed [limits](#limits) and [form limits](#form-submissions).`

- [ ] **Step 6: Run the web tests for the panel and the docs binding**

Run: `cd web && npx vitest run src/components/projects/LimitsPanel.test.tsx 2>&1 | tail -5 && cd .. && go test ./internal/api -run 'TestDocs|Documents' 2>&1 | tail -3`
Expected: both pass (the panel test renders groups generically; if it asserts the old note text, update that assertion to the new note).

- [ ] **Step 7: Commit**

```bash
git add internal/api/ops_limits.go internal/api/ops_limits_test.go web/src/components/projects/LimitsPanel.tsx docs/twillingate.md
git commit -m "feat(api): report INGEST_LATE_DAYS in limits"
```

---

### Task 5: the runbook, and the full check

**Files:**
- Modify: `deploy/UPGRADES.md` (append after "Upgrading to project attributes (migration 040)")

**Interfaces:** none.

- [ ] **Step 1: Write the runbook**

Append to `deploy/UPGRADES.md`:

```markdown
### Upgrading to the late window (no migration)

The oldest client timestamp ingest accepts is now `INGEST_LATE_DAYS` (default
1 day), no longer `RETENTION_RAW_DAYS`. Older timestamps are clamped to that
edge as before. `INGEST_LATE_DAYS` must be at least 1 and at most
`RETENTION_RAW_DAYS`, so `RETENTION_RAW_DAYS=0` is refused: set it to 1 or
more.

Why: raising `RETENTION_RAW_DAYS` on a running server moved the clamp back
over days already rolled up. A late event landing in such a day became its
only raw row, and the next daily pass rewrote that day's aggregates from
that one row. If your raw window was ever raised, find the damaged days
before upgrading (nothing can rebuild them; raw rows are gone):

```sh
sqlite3 -readonly /var/lib/twillingate/twillingate.db \
  "SELECT day, SUM(count) FROM agg_product_daily GROUP BY day ORDER BY day;"
sqlite3 -readonly /var/lib/twillingate/twillingate.db \
  "SELECT day, SUM(views) FROM agg_views_daily GROUP BY day ORDER BY day;"
```

A day with a handful of events between full days, after the date the window
was raised, is one that was rewritten. The raw-first design replaces
aggregates with raw history; until then, keep `RETENTION_RAW_DAYS` where it
is.

Rollback: an older binary ignores `INGEST_LATE_DAYS` and clamps to the raw
window again; nothing else changes.
```

- [ ] **Step 2: Run the whole check**

Run: `make check 2>&1 | tail -15`
Expected: vet clean, coverage gate met, the SQLite-free packages pass under `-race`. If the coverage gate reports `internal/config` or `internal/server` below 90%, the new tests raise it, not lower it; investigate any drop as a sign a step was skipped.

- [ ] **Step 3: Commit**

```bash
git add deploy/UPGRADES.md
git commit -m "docs: runbook for the late window and the check for days rewritten by a raised raw window"
```

- [ ] **Step 4: Open the PR**

The work was done on `fix/late-window`, branched from `feat/raw-archive` (Task 3 explains why); once #176 merges, rebase it onto `main` (`git rebase main`), push, and open the PR with the body naming the spec's D3, the reproduction (`agg_product_daily` count 3 → 1) and the runbook. `fix(server)` is the only commit the release notes publish, which is right: that is the user-visible change.
