# Table filters, local and remote

Status: draft
Date: 2026-10-02

## Problem

- **A table shows at most 1,000 rows, and a viewer cannot narrow them.**
  "Top attribute values by day" on the Product dashboard is the example:
  econumo writes about 1,200 `v_product_attrs` rows a day, so 90 days is
  about 40,000 rows. The widget had to pick which ones to show (the 100
  busiest overall, which dropped whole days, fixed by #110 with a per-day
  ranking), and a viewer who wants `auth_sso` and `plan` only cannot ask
  for them.
- **Sort orders only what was loaded.** A header click sorts the rows in
  the browser, so on a result cut at the cap it sorts a sample.
- **Most tables are small.** Top pages, referrers and the 20-row lists fit
  in one load; sending a query per click for those would make them slower
  for nothing.

## Decisions

- **D1. Filters on every table, in one of two modes.** `table` gains a
  `mode` prop: `"local"` (the default) filters, sorts and pages the rows
  it loaded, in the browser; `"remote"` sends filters, sort and page to
  the server, which applies them in SQL over the whole result. Both modes
  share one filter bar, sort cycle, footer and stored state; only the
  executor differs. One component, not two: the inputs and the UI are the
  same, and a prop is checked by the props schema.
- **D2. Remote work happens in SQL, per request, with no new cap.** The
  widget's SQL is wrapped with the filters, sort, `LIMIT` and `OFFSET`,
  and one run returns the page and its counts. No full result is held in
  memory or written anywhere. Measured at econumo's scale, a page costs
  about what the widget query costs (1.3–1.7 s, most of it
  `v_product_attrs`' live half: #111); paging adds almost nothing.
- **D3. The page size is `CONSOLE_QUERY_MAX_ROWS`.** `limit` defaults to
  it and may not exceed it. A caller that sends no paging arguments gets
  what it gets today, so nothing breaks.
- **D4. `readsql` owns the wrap.** It is the one package that wraps
  custom SQL (its guard 3). The paging wrap is a new generic function
  there, taking the checked text, filters, sort, offset and limit; it
  knows nothing about widgets.
- **D5. Every value is bound; every column name is quoted.** Names come
  from the request but are checked against the widget's own result
  columns and quoted as identifiers (`"` doubled), so any alias works,
  `"Users (at least)"` and `a"b` included.
- **D6. The local executor and the SQL wrap agree, case by case.** Both
  apply the two rules in [Semantics](#semantics), and one cases file drives
  tests of both.
- **D7. Not stored on the server.** Filters and sort are per viewer, in
  localStorage, as sort already is.

## Semantics

These hold for both executors.

**Filter.** `{column, op, value}`. `op` is one of `=`, `!=`, `<`, `>`,
`in`, `not in`. `in` and `not in` take a list of strings (at least one);
the others take one string. Filters combine with AND; a column may appear
in several (`Count > 10`, `Count < 100`).

**Two rules, defined once in Go and once in TS.** SQLite compares by
type affinity, which the browser cannot see (a `TEXT` column read
straight from a table compares with a bound number as text; the same text
from an expression orders after every number). So neither executor uses
SQLite's own comparison. `readsql` registers two deterministic SQL
functions, and the local executor implements the same two:

- `tw_cell(x)`: the cell as the table shows it, the conversion `readsql`
  already applies when it scans a row (integers in decimal, reals in
  Go's shortest `g` form, text as is, `NULL` as `''`).
- `tw_num(x)`: the cell as a number when `tw_cell(x)` is a decimal
  number (`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`, no spaces), `NULL`
  otherwise. The table's right-alignment uses the same rule, replacing
  `isNumericCell`.

**Empty cell.** `tw_cell(x) = ''`: SQL `NULL` or `''`, the cell the table
shows blank. It matches `!=` and `not in` only, never `=`, `in`, `<` or
`>`.

**Equality** (`=`, `!=`, `in`, `not in`) compares `tw_cell` with the
value, exactly and case-sensitively: an integer `100` equals `"100"`.

**Order** (`<`, `>`): when the value is a decimal number, by the same
rule, `tw_num(x) > ?` (a cell that is not a number never matches); any
other value compares `tw_cell(x) > ?` as text, by code point, which orders
`YYYY-MM-DD` correctly (`Day > 2026-09-25`).

**Sort.** `{column, dir}`, `dir` `asc` or `desc`: non-empty cells first,
then numbers (by `tw_num`) before text, numbers by value, text by code
point; empty cells last in both directions. Ties break by every column in
order, by the same rule ascending, so pages never overlap or skip.
Without a sort, the query's own order. This replaces the local sort's
`Intl.Collator` natural order: `v1.10` now sorts before `v1.9` as text,
the same in both modes. Text compares by code point, not by UTF-16 unit,
in TS too (SQLite's `BINARY` over UTF-8 is code-point order).

**Refused** (`store.ErrInvalid`, message names what to change): an unknown
column (the message lists the valid ones), an unknown operator, a list
given to a single-value operator or a single value to `in`/`not in`, an
empty `in` list, malformed `filters` JSON, a `dir` other than `asc`/`desc`,
`offset` below 0, `limit` below 1 or above `CONSOLE_QUERY_MAX_ROWS`, and
any paging argument on a widget that is not a `table` with
`mode: "remote"`. A filter that matches nothing is not refused: it returns
no rows and `matched: 0`.

## Server

**Request.** `widget_data` and `GET /api/widgets/{widget_id}/data` gain
optional arguments, all scalars (the REST GET route takes one value per
query parameter):

| argument | shape | default |
| --- | --- | --- |
| `filters` | JSON string, a list of filters | none |
| `sort` | `<column>:asc` or `<column>:desc` (split on the last `:`) | the query's order |
| `offset` | integer ≥ 0 | 0 |
| `limit` | 1 to `CONSOLE_QUERY_MAX_ROWS` | `CONSOLE_QUERY_MAX_ROWS` |
| `distinct` | a column name | none |

**Wrap.** For a remote table, the widget's checked SQL becomes, in one
statement:

```sql
SELECT <cols>, matched, total FROM (
  SELECT *, COUNT(*) OVER () AS matched FROM (
    SELECT *, COUNT(*) OVER () AS total FROM (<widget SQL>)
  ) WHERE <filters>
) ORDER BY <sort>, <every column> LIMIT ? OFFSET ?
```

The counting columns are named so they cannot collide with the widget's
own (the implementation picks names the widget's columns do not use) and
are stripped from the rows returned. With zero matched rows the counts
come from a second, count-only run, since there is no row to carry them.

**`distinct`.** Returns `[value, rows]` for that column among rows that
match the **other** filters (a filter on the same column is left out, so
the picker still offers what it would add), most frequent first, then by
value, paged by the same `offset` and `limit`. `distinct` and `sort` are
mutually exclusive (refused together).

**Envelope.** `data` is the page (`columns` as the widget returns them,
`rows`). A remote table's envelope gains

```json
"page": {"offset": 0, "limit": 1000, "matched": 5335, "total": 40240,
         "sort": "Count:desc", "filters": [...]}
```

and `data.truncated` is `matched > offset + limit`, so a caller reading
only `truncated` keeps today's meaning ("there is more"). A `distinct`
request returns `data` with columns `value` and `rows`, and the same
`page`.

**Cache.** The key gains the filters, sort, distinct, offset and limit,
canonical (filters in the order given, which is the order the viewer
added them). Each entry is one page.

**Local tables.** Unchanged on the server: the whole result up to
`CONSOLE_QUERY_MAX_ROWS`, `truncated` when cut.

**Description.** `widget_data`'s tool description and argument
descriptions say which widgets take the paging arguments and point to
`docs://reporting` for the rules.

## Page

**Who fetches.** `WidgetCard` keeps the fetch. `WidgetProps` gains
`view` (filters, sort, offset) and `onView`; the card puts `view` into the
TanStack query key and, for a remote table, into the request. Other
components ignore both. In local mode the table applies `view` itself
with the local executor and the card's request does not change.

**Filter bar**, above the header row, on every table:

- Each filter is a chip (`Attribute in $os, plan ×`, `Count > 100 ×`);
  clicking one edits it; **+ Filter** adds one; **Clear all** shows with
  two chips or more.
- The editor is a popover: column, operator, value. For `in`/`not in`, a
  multi-select combobox; for `=`/`!=`, a single-choice combobox that also
  takes typed text; for `<`/`>`, a text input. Combobox options are the
  column's distinct values with their row counts, most frequent first,
  searchable: from the loaded rows in local mode, from `distinct` in
  remote mode.
- A refusal shows the server's message under the bar and keeps the
  previous rows; the chip is marked until edited.
- A filtered table with no matching rows still shows the bar and "No rows
  match these filters", not the card's empty state. An unfiltered empty
  result keeps the card's empty state.

**Sort.** The header click and its cycle are unchanged (numbers desc
first, text A–Z first, third click back to query order). In remote mode
it is sent as `sort`.

**Footer.** Shown when there is more than one page: `1–1,000 of 5,335`
with previous and next. The page size is the server's limit in remote
mode, 1,000 rows in local mode (local rarely needs a second page). A
change of filters, sort, range or project returns to the first page.

**Loading.** A first load shows the skeleton. A later one (filter, sort,
page) keeps the current rows on screen, dimmed, until the new page
arrives.

**Stored.** Under the existing `twillingate.widget.{dashboard}.{widget}`
prefix: `.sort` (as today) and `.filters` (new). A stored filter or sort
on a column the query no longer returns is kept but not applied or sent;
its chip shows greyed, "not in this table". The page offset is not
stored.

**Local mode at the cap.** When the loaded result is `truncated`, the
footer says filters apply to the loaded rows and names `mode: "remote"`.

**What viewers will see differ.** In remote mode, a colour scale spans
the current page, and the right-alignment of a column is decided on the
current page's rows.

## The attribute table

"Top attribute values by day" becomes `mode: "remote"`. Its SQL drops the
per-day ranking (#110) and returns every (attribute, day, value) in the
range, ordered by attribute, newest day first, then count. A viewer
filtering `Attribute in $os, plan` gets those attributes complete.

## Docs

In the same commits as the code:

- `docs/reporting.md`: the `table` row of the component table (`mode`),
  a remote-table example, `widget_data`'s new arguments and the rules in
  [Semantics](#semantics).
- `internal/reporting/ui/components.json`, regenerated (the props schema
  gains `mode`).

## Tests

- **Cases file.** `internal/reporting/testdata/table-filters.json`: rows
  (integers, reals, numeric-looking text, dates, empty cells, `NULL`),
  and cases, each a list of filters and a sort with the expected rows in
  order. A Go test runs every case through the SQL wrap over a `VALUES`
  query; a web test runs every case through the local executor.
- **readsql.** `tw_cell` and `tw_num` against the scan's own conversion
  (integers, reals such as `0.1+0.2`, text, `NULL`); the wrap refuses what Check refuses; names with `"` and
  values with `'`, `--` and `;` are inert; counts with zero matches;
  joined pages equal the sorted whole for page sizes 1, 7 and the total.
- **reporting.** Paging arguments refused on local tables and other
  components; every refusal in [Semantics](#semantics); the cache key
  differs per view and is shared for the same view; the envelope's
  `page` and `truncated`; `distinct` ignores the filter on its own column.
- **System.** `TestAttributeValuesKeepEveryDay` pages through the remote
  table and finds every day of the range, with `matched` equal to the
  number of (attribute, day, value) rows in `v_product_attrs`.
- **Docs sync.** `docs_sync_test` already binds components, examples and
  tool arguments; it covers the new prop and arguments.
- **Web.** Filter bar (add, edit, remove, clear all), comboboxes in both
  modes, sort sends `sort` in remote mode, footer and page reset,
  dimmed reload, greyed stored filters, the local-mode cap note, the
  "no rows match" state.
- **e2e.** On the Product dashboard: filter the attribute table to two
  attributes, go to page 2, reload; the filters survive and the page is
  back to 1.

## Shipping

One PR, `feat(reporting): filter and page tables, in SQL for remote
tables`. Not breaking: local tables behave as today plus the filter bar,
and `widget_data` without the new arguments answers as it does now.

## Out of scope

- Server-side filters on components other than `table`.
- `OR` between filters, `contains`/prefix operators, case-insensitive
  matching.
- Speeding up `v_product_attrs` (#111).
