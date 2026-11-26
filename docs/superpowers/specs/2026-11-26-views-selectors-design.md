# Selectors on the built-in Views dashboard

Date: 2026-11-26. Status: approved.

## Intent

Dashboard selectors shipped in v0.27.0, but only on user dashboards. The owner wants the built-in **Views** tab to come with them, so it can be narrowed by host and UTM without duplicating it.

## Goals

- Views gets four selectors:
  - **Host** (`host`, name `host`)
  - **Source** (`utm_source`, name `source`)
  - **Medium** (`utm_medium`, name `medium`)
  - **Campaign** (`utm_campaign`, name `campaign`)
- Every Views widget narrows by each of them. That includes the three widgets that read `sessions` (bounce rate, average session and session length).

## Non-goals

- The other built-in tabs (Product, Users, Groups, Retention, Web Vitals, Measures, Availability).
- Existing user copies of Views.

## Anti-goals

- Views at "All" showing anything different from today.
- A built-in definition with a broken selector reaching a running server.

## Constraints

- Built-ins are defined in `internal/reporting/system/<dir>/dashboard.json` and loaded by `internal/reporting/files.go`. The release-owned sync (`internal/store/sqlite/reporting_sync.go`, `SyncReporting`) writes them to SQLite on start, and today it leaves `dashboards.selectors` at its default `[]`.
- The selector rules are those of user dashboards (`checkSelectors` in `internal/reporting/selectors.go`).
- The API keeps refusing selector edits on system dashboards. Only the release sets them.

## Approaches

| | Approach | Verdict |
| --- | --- | --- |
| **A (chosen)** | Selectors are declared in `dashboard.json`, and the sync writes them. | Approved. |
| B | Rewrite the Views widgets as SQL that reads `:host` and so on. | Dropped: it gives up the structured queries, which already narrow on their own. |
| C | Leave the session widgets unfiltered by Host, with the "Not filtered" note. | Dropped: the headline bounce and session numbers would disagree with the rest of the tab. |

## Design

- **The definition.** `dashboard.json` takes an optional `selectors` array with the same shape as the API's (`name`, `label`, `column`).
  - `loadDashboard` parses it and runs `checkSelectors`, so a bad entry fails loading, and with it the package tests.
  - `system/views/dashboard.json` declares the four selectors above.
- **The sync.** The system row's `selectors` is written from the definition on every sync, like its title. A release that changes or removes them takes effect on the next start.
- **Sessions and host.** On the `sessions` table, a selector whose column is `host` filters `entry_host`, the host the session started on.
  - This adds `host` to the sessions allow-list, mapped to `entry_host`.
  - It applies on every dashboard, not only Views.
  - The cache key keeps the selector's own column (`host`), which is stable.
- **Tests:**
  - a loaded definition carries its selectors, and a malformed one is refused;
  - the sync writes them, and a changed definition replaces them;
  - a `sessions` query follows `host` and filters `entry_host`;
  - `TestSystemDashboards` runs once at All and once with `host` picked.
- **Docs.** `docs/reporting.md`:
  - the system dashboards paragraph says Views ships the four selectors;
  - the following rules gain `host` → `entry_host` on `sessions`.

## Decided

- The label is "Host", not "Domain". The column holds full hostnames, each subdomain its own value, and the Views "Hosts" table already says "Host".
