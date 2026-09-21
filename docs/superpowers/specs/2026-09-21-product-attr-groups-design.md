# Groups in the product attribute breakdown

Status: proposed
Date: 2026-09-21

## Sequencing

Fourth of four specs that land in order. It is written against the schema the
first two leave behind — `project_id INTEGER` on every table, the raw product
table renamed to `events`, and a `v_product_attrs` that already carries a
`$platform` arm — and it owns migration `016`:

1. `2026-09-19-project-ids-design.md` — `014_project_ids.sql`
2. `2026-09-20-os-and-platform-design.md` — `015_environment.sql`
3. `2026-09-20-sdk-consent-and-instances-design.md` — no migration
4. **this spec** — `016_product_attr_groups.sql`

Nothing here depends on the third spec. It is placed last because it is the
smallest and because recreating `v_product_attrs` twice in a row — once in 015
for `$platform`, once here — is avoided by letting 015 go first.

## Problem

The product page reports an attribute breakdown: for each declared attribute
key, the values it took, how often each appeared, and how many people were
behind them. The last of those is one number, `unique_users`, and for a
product sold to companies it is the less useful one. "Which plan tier fires
this event" is answered by accounts, not by seats — thirty events from one
customer and thirty events from thirty customers are the same row today.

The group is already on the raw row. `events.group_id` has existed since
`003_app.sql` (`TEXT NOT NULL DEFAULT ''`), the SDK sets it through
`group()` and `data-group`, and `v_identity_daily` already breaks activity
down by `kind='group'`. What is missing is the intersection: no table
anywhere records how many distinct groups sat behind a given attribute value.
`agg_product_attrs` is keyed
`(project_id, day, event_name, attr_key, attr_value)` and carries exactly
`count` and `unique_users`, so the figure cannot be recovered by querying
differently — only by measuring it.

One inherited wrinkle, named here so the new column is not read against the
wrong neighbour: `unique_users` counts `DISTINCT actor_id`, not
`DISTINCT user_id`. It is distinct *actors*. Renaming it is out of scope; the
new column is named for what it actually counts.

## Decisions

1. `agg_product_attrs` gains `unique_groups`, counting
   `DISTINCT NULLIF(group_id,'')` — distinct non-empty groups among the rows
   carrying that attribute value.
2. **The column is nullable, and NULL means "not measured".** Every day
   aggregated before 016 keeps NULL forever. See below.
3. History is not backfilled, because it cannot be.
4. Ranking is untouched. The top-N cap and the `(other)` tail are still
   decided by `count`, so which values survive the cap does not change.
5. Both Evidence tables that read this view gain the column — the attribute
   breakdown and the app-version summary — with the same "at least" floor
   caveat the users column already carries.

## Why history stays NULL

`AggregateProductDay` deletes the day's raw rows in the same transaction that
writes the rollup (`internal/store/sqlite/aggregate_product.go:47`). Once a
day is aggregated its `events` rows are gone, so there is nothing left to
count groups from. No backfill is possible for any day already rolled up, and
re-running the daily pass will not resurrect one: it rebuilds from raw, and
raw is what is missing.

That leaves a choice about how the gap reads. `NOT NULL DEFAULT 0` — the
convention every other column in these aggregates follows — would render the
gap as a confident zero. For a project that has used groups since the day it
was created, every historical row would claim no groups were involved, and
nothing would distinguish that claim from a value genuinely fired by no
group.

So the column is nullable and the convention is broken deliberately:

- The live half always produces an integer. `COUNT(DISTINCT NULLIF(...))`
  returns `0`, not NULL, when no row carries a group — so `0` is a real
  measurement meaning "measured, none".
- Only pre-016 aggregate rows are NULL, and NULL therefore has exactly one
  meaning: this day was rolled up before the column existed.
- `MAX()` skips NULLs, so the Evidence aggregation needs no special casing.
  A range covering only pre-016 days yields NULL and renders as an empty
  cell rather than a zero.

The exception is documented in the migration itself, next to the `ALTER`.

## Storage — migration `016_product_attr_groups.sql`

Two statements: the column, then the view.

```sql
-- Nullable on purpose, against the NOT NULL convention of every other
-- column here. Aggregation deletes the day's raw rows in the same
-- transaction that writes this table, so days rolled up before this
-- migration cannot be backfilled -- their group count is unknown, not
-- zero. The live half always writes an integer (COUNT DISTINCT returns 0,
-- not NULL, when no row carries a group), so NULL means exactly one thing:
-- aggregated before 016.
ALTER TABLE agg_product_attrs ADD COLUMN unique_groups INTEGER;
```

`v_product_attrs` is then dropped and recreated. It is 015's definition with
`group_id` threaded through: every arm of `vals` selects it, `counted` counts
it, `ranked` carries it, and all three arms of the final union project it.

```sql
DROP VIEW IF EXISTS v_product_attrs;
CREATE VIEW v_product_attrs AS
WITH cap AS (...unchanged...),
declared AS (...unchanged...),
vals AS (
  -- every arm 015 leaves in place, each gaining group_id: the declared-key
  -- arm plus the $os, $platform and $app_version system arms
  SELECT e.project_id, substr(e.ts,1,10) AS day, e.event_name,
         d.attr_key, json_extract(...) AS attr_value,
         e.actor_id, e.group_id
  FROM events e JOIN declared d ON d.project_id = e.project_id
  WHERE json_extract(...) IS NOT NULL
  UNION ALL
  SELECT project_id, substr(ts,1,10), event_name, '$os', os,
         actor_id, group_id
  FROM events WHERE os <> ''
  -- ... $platform and $app_version arms likewise
),
counted AS (
  SELECT project_id, day, event_name, attr_key, attr_value,
         COUNT(*) AS c,
         COUNT(DISTINCT actor_id) AS u,
         COUNT(DISTINCT NULLIF(group_id,'')) AS g
  FROM vals
  GROUP BY project_id, day, event_name, attr_key, attr_value
),
ranked AS (
  SELECT project_id, day, event_name, attr_key, attr_value, c, u, g,
         ROW_NUMBER() OVER (PARTITION BY project_id, day, event_name, attr_key
                            ORDER BY c DESC, attr_value) AS rn
  FROM counted
)
SELECT project_id, day, event_name, attr_key, attr_value,
       count, unique_users, unique_groups
FROM agg_product_attrs
UNION ALL
SELECT project_id, day, event_name, attr_key, attr_value, c, u, g
FROM ranked WHERE rn <= (SELECT n FROM cap)
UNION ALL
SELECT v.project_id, v.day, v.event_name, v.attr_key, '(other)',
       COUNT(*), COUNT(DISTINCT v.actor_id),
       COUNT(DISTINCT NULLIF(v.group_id,''))
FROM vals v
WHERE NOT EXISTS (...unchanged...)
GROUP BY v.project_id, v.day, v.event_name, v.attr_key;
```

The `ORDER BY c DESC, attr_value` in `ranked` is deliberately left alone. A
value's place in the top N is still decided by how often it appeared, so a
rare value fired by many groups does not displace a common one, and the set
of rows the view returns is identical to 015's.

`(other)` gets its own group count from raw, not a sum of the tail's counts —
the same reason the users column is computed there rather than added up.

### Cost

One extra `COUNT(DISTINCT)` over rows already being scanned and grouped, in
both the view and the rollup. The aggregate table gains one integer column;
row count is unchanged, since the key is unchanged.

## Rollup

`rollupAttrValue` (`internal/store/sqlite/aggregate_product.go`) writes both
statements and both need the column.

- The top-N statement: `counted` gains
  `COUNT(DISTINCT NULLIF(group_id,'')) AS g`, `ranked` carries `g`, and the
  `INSERT OR REPLACE` column list and its `SELECT` each gain one entry.
- The `(other)` statement: the trailing `SELECT` gains
  `COUNT(DISTINCT NULLIF(group_id,''))`.

Both already scan the same rows with the same `WHERE`, so nothing new is
read. `rollupAttrValue` stays a single function serving declared keys and
system dimensions alike — `group_id` is a column on the row, independent of
which expression is being grouped by, so neither `expr` nor `present`
changes.

`INSERT OR REPLACE` means a day re-aggregated after 016 gets a real value,
which matters only for a day whose raw rows still exist — that is, one not
yet rolled up.

## Private API

`productAttributes` (`internal/api/ops_product.go:58`) adds `unique_groups`
to its `SELECT`. It returns a generic `tableOut`, so the column flows through
to both the `product_attributes` MCP tool and the REST route without a type
change. The addition is backwards compatible: a client reading by column name
is unaffected, and one reading positionally gets the new column last.

`schemaViews` (`internal/api/resources.go:49`) is updated in the same commit,
per CLAUDE.md:

```
v_product_attrs(project_id, day, event_name, attr_key, attr_value, count, unique_users, unique_groups)
```

## Reporting

Both `v_product_attrs` consumers on `evidence/pages/product/[project].md`
gain the column. Neither can sum it — one group firing two different events
would be counted twice — so both take `max(unique_groups)`, which is a floor
on the true number, exactly as they already do for users.

- `attr_breakdowns` (the Attribute breakdowns table) gains
  `max(unique_groups) as min_groups` and a `Groups (at least)` column.
- `app_version_summary` (the App version summary table) gains the same.

The `app_versions` area chart is unchanged — it plots event counts over time
and has no room for a second measure.

The prose above the breakdown table names both floors instead of one.

## Documentation

- `docs/twillingate.md:139` — the breakdown is "counts, unique users and
  unique groups per distinct value, per event, per day".
- `docs/twillingate.md` queryable-views section — the `v_product_attrs`
  column list, matching `schemaViews`.
- Both land in the same commit as the migration, per CLAUDE.md.

`docs_sync_test.go` checks view *names*, not column lists, so nothing there
fails if this is forgotten. It is on the author.

## Testing

1. **Boundary agreement.** Extend the existing before/after test in
   `internal/store/sqlite/views_test.go`: seed raw product events carrying a
   mix of set and empty `group_id`, read every `v_product_attrs` row, run
   `AggregateProductDay`, read again, assert the rows are identical.
   `attrRow` gains the column. This is the assertion that catches a live half
   and a rollup that disagree, which is the failure mode this change most
   invites.
2. **The NULL is real and only where expected.** A migration test: build a
   database at 015, write an `agg_product_attrs` row, migrate to 016, and
   assert the row's `unique_groups` is NULL while a freshly aggregated day's
   is an integer.
3. **Zero is a measurement.** In `aggregate_product_test.go`, a day whose
   events carry no `group_id` at all rolls up to `unique_groups = 0`, not
   NULL — the distinction the whole nullable decision rests on.
4. **Distinctness.** Many events from one group count as one group; the same
   group firing two different event names counts once per event row, which is
   why Evidence takes a max rather than a sum.
5. **The tail.** With `topN` forced low, the `(other)` row's `unique_groups`
   is the distinct count across the whole tail, which for overlapping groups
   is strictly less than the sum of the tail's own counts.

## Out of scope

- **Backfilling history.** Impossible, per above.
- **Renaming `unique_users`.** It counts actors and is misnamed, but it is
  named that in the tool output, `schemaViews` and the docs, and fixing it is
  a breaking change that deserves its own commit.
- **Groups on `v_product_daily` and `v_product_totals`.** The same argument
  applies to them and they should probably follow, but the ask was the
  attribute breakdown and each is a separate column on a separate aggregate.
- **Groups in retention.** `agg_retention` cohorts by actor kind; a group
  cohort is a different design, not a column.
- **Group *names* in the breakdown.** The count is a number; resolving ids to
  the display names in `identities` is a join the breakdown does not do.
