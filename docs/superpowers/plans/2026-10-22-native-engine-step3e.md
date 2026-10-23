# Native engine, step 3e: the attribute explorer — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An `attributes` query kind answers every attribute key and value with counts, users and groups. It covers the attribute-values widget, the `product_attributes` tool and the `list_attributes` key summary, and every answer equals DuckDB's.

**Architecture:** The rows are "unpivoted" natively. A row of `product` (or `measures`) contributes one pseudo-row per non-NULL `attr_*` column, plus the reserved keys when asked. These feed the existing grouping machinery with two implicit dimensions, `attribute` and `value`, and optionally `day` and `event_name` before them. Two outputs:
- `values`: one row per (…, attribute, value) with metrics;
- `keys`: one row per attribute with events, the distinct value count and `keys_total`, limited to the busiest keys plus named ones.

**Tech Stack:** Go (`internal/native`, `internal/api`), `docs/twillingate.md`.

**Spec:** `docs/superpowers/specs/2026-10-21-native-engine-design.md` (kinds table: `attributes` — every attribute key and value with counts; covers the attribute explorer, `list_attributes`, `product_attributes`).

## Global Constraints

- **Exact semantics.** The parity targets, which the tests run as written:
  - **`values`** matches `internal/reporting/system/product/attribute-values.sql`. Per (attribute, value), over product rows in the range:
    - `count` is the events;
    - `users` is the distinct `actor_id` (a NULL actor counts as one, as GROUPING SETS gives);
    - `groups` is the distinct non-empty `group_id`.

    The attribute is the key without `attr_`. Rows are ordered by attribute asc, count desc, value asc.
  - **`values` with `dimensions: ["day", "event_name"]` and `reserved: true`** matches `product_attributes`' SQL (`internal/api/ops_product.go` ~93-107):
    - each reserved key's column, empty read as NULL, appears as `attr_$<key>` (`reservedKeys()`);
    - `count`, `users` (distinct actor_id, NULL not counted, as `count(DISTINCT)`) and `groups` (distinct group_id) are per (day, event_name, attribute, value);
    - rows are ordered by day, count desc, event_name, attribute, value.

    Note the different NULL-actor rule between the two targets. It is controlled by the metric: `users` counts distinct ids, and `actors_or_null` keeps the widget's count-NULL-as-one. The names are the builder's, but the two semantics must both be available and each match its target.
  - **`keys`** matches `list_attributes`' SQL (`internal/api/ops_attributes.go` ~98-114):
    - it reads families product and measures (table `events` restricted to those, or `table: ["product","measures"]` — the builder chooses the shape);
    - per key: `events` = count, `values` = count(DISTINCT value), `keys_total` = the number of keys;
    - it keeps the `limit` busiest keys (by events desc, key asc) plus any key named in `keep` (the rule-named keys);
    - rows are ordered by events desc, key asc.
- **Rules:** event and attribute rules apply as on the event tables, so a renamed key reads under its target and a blocked key is NULL.
- **Approximation:** `users` and `groups` over a whole range follow the engine's rule (exact up to 1,024 ids per group, then a sketch marked approximate). `values` in `keys` is exact or approximate by the same rule.
- **Bounds:** high-cardinality attributes (e.g. 20,000 item ids) must stay within `MaxGroups`/`MaxBytes`. Past them a `values` answer truncates (or is refused), as breakdowns do. The step 2 ruling that `product_attributes` is capped at 92 days exists because DuckDB ran out of memory; the native engine refuses on its own budget instead, and the tool keeps its cap until step 3f decides.
- Validate, `Columns()`/`ColumnTypes()` (attribute and value text, day a day, event_name text, metrics number), the MCP schema, and `docs/twillingate.md` "Structured queries" (bound by `TestStructuredQueryDocument`) change in the same commit.
- `make check` must pass before each commit: `PATH=/usr/local/go/bin:$PATH GOTMPDIR=~/.cache/gotmp make check`. Never use `/tmp`.

## Review Focus

1. **A key present in some files only.** It is NULL elsewhere and contributes nothing there. Pinned by the parity fixture (a key only in the open log).
2. **Two keys differing only in case** across a month file and a log. They merge as DuckDB's union_by_name does (step 2's case-insensitive resolution). Pinned by Task 1.
3. **A NULL actor.** It counts as one user in the widget target and as none in `product_attributes`. Pinned by Task 1.
4. **`keep` naming a key that has no events in range.** It does not appear, as the SQL's QUALIFY keeps only existing rows. Pinned by Task 2.
5. **`limit` smaller than the number of keys.** `keys_total` still counts all of them. Pinned by Task 2.

---

### Task 1: The `attributes` kind, output `values`

**Files:**
- Create: `internal/native/attributes.go` (the unpivot of a batch into attribute/value pseudo-rows, including the reserved keys)
- Modify: `internal/native/query.go` (kind `attributes`: `table` product or measures, `output` values or keys, `dimensions` limited to `day`/`event_name`, `reserved`, metrics count/users/groups/actors_or_null, `where`, `limit`, `order`), `internal/native/engine.go` / `aggregate.go` (grouping over pseudo-rows), the docs and schema.
- Test: parity with attribute-values.sql (as written) and with `product_attributes`' SQL (copied from ops_product.go, including `reservedKeys()`), on a fixture with case-split keys, a key only in the log, NULL actors, empty groups and renamed or blocked attributes.

- [ ] Tests first, run and see them fail, implement, run `go test -race ./internal/native/ ./internal/api/`, then `make check`. Commit: `git commit -m "feat(native): list attribute values with their counts and users"`

### Task 2: Output `keys`

**Files:**
- Modify: `internal/native/attributes.go`, `query.go` (`output: keys` with `limit` (default 50, max 1000) and `keep` []string; families product and measures), the docs and schema.
- Test: parity with `list_attributes`' SQL (copied from ops_attributes.go, with `:limit` and the keep list bound), covering limit < keys, a kept key outside the limit, a kept key with no events, and measures attributes.

- [ ] Tests first, then implement and run `make check`. Commit: `git commit -m "feat(native): summarise attribute keys"`
