# Record whether consent was given

Status: draft
Date: 2026-09-23

## Sequencing

Third spec after the SDK factory (#48) and the identity-mode removal
(`2026-09-23-server-stores-what-it-is-sent-design.md`, PR #50, migration
017). It owns migration `018_consent.sql` and lands after #50 so the
migration numbers stay in order. Nothing here depends on #50's content: the
consent flag is orthogonal to how ids are stored.

## Problem

The SDK decides at every storage decision whether it may keep anything on
the device (`consent`, declared on the tag or answered live by a consent
manager), and persists the visitor id, user and group only when the answer
is yes. The server never learns that answer. So two questions have no
data behind them:

- **Audit.** "Every identifier this collector holds was persisted with
  consent" is a claim the operator makes about the SDK's behaviour, not
  something the stored rows can show. A row carrying `$install_id` says
  nothing about whether the consent manager had said yes.
- **Product.** How many visitors give consent, and does the rate move
  when the banner changes, is a question every site with a banner asks
  and none can answer from twillingate today.

## Decisions

1. **A reserved batch attribute `$consent`, values `1` and `0`.** The SDK
   sends it on every batch with the effective consent at flush time. A
   hand-built client may send it too. Absent means unknown, which is what
   every row stored before this migration is.
2. **Stored on every view and product event** as an integer column that is
   `1`, `0` or `NULL`. Column, not attribute JSON: it is a system fact
   that must survive rollup and drive a breakdown.
3. **A views dimension `consent`** with the three values `given`, `none`
   and `unknown`, aggregated daily like every other dimension, so the rate
   outlives the raw window. Product events carry the column for
   row-level audit and `v_events_flat`; they get no breakdown, the same
   split every environment key already follows.
4. **Validation in Go, none in the database** (the standing rule). `1`,
   `0`, `true`, `false`, as JSON booleans, numbers or strings, are the
   accepted spellings; anything else is warned about in the response and
   stored as unknown, never rejected.

## Design

### SDK

`batchAttributes()` (`sdk/src/twillingate.ts`) adds `a.$consent =
this.mayStore() ? 1 : 0`. `mayStore()` is already the decision point
consulted at every read and write, so the batch carries the same answer
the storage code acted on, including a pin from `consent(true|false)` and
a consent manager that answered after page load. Both identity modes send
it: for an anonymous instance it states whether the retry queue may touch
the device, which is still a consent fact.

Docs: the batch-attribute list in "Consent and storage" gains one
sentence; the envelope sample in "The wire format" gains the key.

### Wire format

`$consent` joins the Identity group of reserved keys
(`internal/server/ingest.go` `reservedKeys`), landing on
`resolved.Consent`. A batch-level value applies to every event in the
batch; a per-event value overrides it, by the existing merge rule.

Parsing, in Go beside the other declared values (`handlers.go`):

| Received | Stored |
| --- | --- |
| `1`, `"1"`, `true`, `"true"` | `1` |
| `0`, `"0"`, `false`, `"false"` | `0` |
| absent | `NULL` |
| anything else | `NULL`, with a warning `unknown $consent value, ignored` in the response |

Trimmed and case-folded before matching, like the environment values.

### Storage — migration `018_consent.sql`

```sql
-- Whether the client had consent to keep anything on the device when the
-- event was sent: 1, 0, or NULL for unknown. NULL is every row stored
-- before this migration and every row a client sends without $consent;
-- there is nothing to backfill from, and 0 would claim a refusal that was
-- never recorded.
ALTER TABLE views  ADD COLUMN consent INTEGER;
ALTER TABLE events ADD COLUMN consent INTEGER;

-- Daily rollup, modelled on agg_views_platforms. consent here is the
-- breakdown value, not the raw flag.
CREATE TABLE agg_views_consent (
    project_id INTEGER NOT NULL, day TEXT NOT NULL, consent TEXT NOT NULL,
    visitors INTEGER NOT NULL, views INTEGER NOT NULL,
    PRIMARY KEY (project_id, day, consent)
);
-- Seeded from the daily totals as 'unknown', the way 015 seeded platforms,
-- so a range covering rolled-up days shows a full 'unknown' bar rather
-- than nothing.
INSERT INTO agg_views_consent (project_id, day, consent, visitors, views)
SELECT project_id, day, 'unknown', SUM(visitors), SUM(views)
FROM agg_views_daily GROUP BY project_id, day;

CREATE VIEW v_views_consent AS
SELECT project_id, day, consent, visitors, views FROM agg_views_consent
UNION ALL
SELECT project_id, day,
       CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END,
       COUNT(DISTINCT actor_id), COUNT(*)
FROM views
GROUP BY project_id, day, CASE consent WHEN 1 THEN 'given' WHEN 0 THEN 'none' ELSE 'unknown' END;
```

Three values need no top-500 cap, so the view is the plain
aggregate-plus-live-half shape. The seed sums `visitors` across kinds,
which over-counts an actor seen on two kinds in one day; 015 accepted the
same for platforms, and the seeded rows are all `unknown` anyway.

`store.View` and `store.ProductEvent` gain `Consent int8` (`1`, `0`, `-1`
for unknown); `write.go` writes `NULL` for `-1`. `flatViewBaseColumns`
gains `consent`, so `v_events_flat` exposes it beside `actor_id`.

### Daily pass

`viewDimensions` (`aggregate_views.go`) gains
`{table: "agg_views_consent", keys: []string{"consent"}, exprs:
[]string{consentSQL}}` where `consentSQL` is the `CASE` above, the same
mechanism `referrers` and `displays` use for a derived key. The rollup
writes `given`/`none`/`unknown` from the raw flag. `prune.go` and the
registry's table list gain `agg_views_consent`.

### Private API

- `viewsDimensions` (`ops_read.go`) gains `"consent": {"v_views_consent",
  []string{"consent"}}`; the `dimension` enum in `breakdownIn` lists it.
- `schemaViews` (`resources.go`) gains
  `v_views_consent(project_id, day, consent, visitors, views)  --
  consent: 'given'|'none'|'unknown'; unknown is every row stored before
  migration 018 or sent without $consent` and `v_events_flat`'s row
  names `consent`.

### Reporting

`evidence/sources/twillingate/v_views_consent.sql` (with the empty-source
sentinel every source carries) and, on `evidence/pages/views/
[project].md`, a "Consent" block beside the other breakdowns: a
three-row table of visitors and views by value for the range, and a
consent rate line (`given / (given + none)`, unknown excluded, blank when
both are zero).

### Documentation

`docs/twillingate.md`:

- "Reserved attribute keys": `$consent` in the Identity row.
- "Consent and storage": one sentence that every batch carries `$consent`
  with the effective answer, and that the dimension exists.
- "The wire format" envelope: `"$consent": 1` in the batch attributes.
- "Answer questions with the data": `consent` in the `views_breakdown`
  dimension list; `v_views_consent` in the views family sentence, with
  the meaning of `unknown`.

`deploy/UPGRADES.md` gains "### Upgrading to the consent flag (migration
018)": nothing to check first; every existing row reads `unknown`; the
`unknown` bar shrinks as clients pick up the new SDK; a hand-built client
sends `$consent` itself or stays `unknown`.

`docs/deployment.md`: the Evidence section needs no change beyond the
image rebuild note that already applies to every dashboard change.

All in the same commits as the code they describe, per CLAUDE.md.

## Tests

- SDK: a batch carries `$consent: 0` by default, `1` with
  `consent: true`, and flips after `consent(true)` / `consent(false)` on
  the next flush; a consent-manager function that flips is reflected at
  the next flush; the anonymous instance sends it too.
- Ingest: the parsing table above, including the warning for `"maybe"`;
  per-event override of a batch value; absent → `NULL` in both tables.
- Migration 018: build at 17, seed a view row and an `agg_views_daily`
  row, `migrateThrough(ctx, 18)` (pinned, never unbounded); the old view
  row reads `NULL`; `agg_views_consent` holds one `unknown` row with the
  daily totals; a fresh database has the column.
- Aggregation boundary: seed raw views with consent `1`, `0` and `NULL`,
  read `v_views_consent`, roll the day up, read again, rows identical.
- API: `views_breakdown` with `dimension: "consent"` returns the three
  values; the dimension enum names it; `TestDocumentCoversEveryViewsDimension`
  and `TestDocumentMatchesReservedKeys` pass without special casing.
- Evidence: the source builds against an empty database (the sentinel).

## Breaking changes

None on the wire: a client that never sends `$consent` is unchanged.
Commit as `feat:`. The release note says `$consent` exists, that the SDK
sends it, and that the `consent` breakdown reads `unknown` for history.

## Out of scope

- Recording *proof* of consent (what was shown, when, which version). The
  SDK is not a consent manager; the flag records the answer it acted on.
- A consent dimension on product events (`v_product_*`); the column is
  there for row-level audit and `v_events_flat`.
- Backfilling history: nothing to backfill from.
