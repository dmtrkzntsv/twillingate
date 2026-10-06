# Widget Shares Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A console user can share one widget as a designed PNG card. It gets an unlisted, public `CONSOLE_URL/share/<id>` page with social preview tags, embed code, a Download PNG, a Shares page, archived shares on the Archive page, and an **Archive after** date.

**Architecture:**
- **Capture.** The browser lays the widget out on a fixed 1200×630 card (card mode, a bundled Inter font, the sharer's theme). `html-to-image` captures it at pixel ratios 1 and 2 and uploads both PNGs.
- **Storage.** The Go server stores them in `widget_shares` (migration 032) under a UUIDv7.
- **Public routes.** `reporting` serves `/share/{id}`, `/share/{id}.png` and `/share/{id}@2x.png` without auth, through the console mux.
- **Daily pass.** It archives shares whose `archive_at` has passed and purges long-archived ones. Deleting a project deletes its shares; deleting a widget does not.

**Tech Stack:**
- Go: stdlib `net/http`, `html/template`, `image/png`, `mime/multipart`; `google/uuid` (already a dependency); modernc SQLite.
- Web: React 19, TypeScript, TanStack Query 5, shadcn/radix, recharts 3, `html-to-image` and `@fontsource/inter` (new; frontend dependencies are allowed). Tests use vitest and Playwright.

**Spec:** `docs/superpowers/specs/2026-10-05-widget-shares-design.md`. Read it before any task: D1–D9 are the requirements.

## Global Constraints

- Layering (`internal/archtest`): `reporting` and `manage` are rank 1 and do not import each other. `api` is rank 2. `app` wires them. No new Go dependency.
- Refusals are typed: `store.Refuse(store.ErrInvalid|ErrNotFound|ErrConflict, …)`, matched with `errors.Is`. The REST layer maps them to 400/404/409.
- Ids are `uuid.NewV7()`, falling back to `uuid.NewString()`, in canonical 36-character form.
- Timestamps in `widget_shares` are `strftime('%Y-%m-%dT%H:%M:%SZ','now')` text. Go writes the same layout: `"2006-01-02T15:04:05Z"` in UTC.
- `archive_after` values are exactly `7d`, `30d`, `90d`, `365d` and `project`; the default is `30d`. `project` stores `archive_at = NULL`. UI labels: 1 week, 1 month, 3 months, 1 year, Project lifetime.
- Images: `image` is exactly 1200×630 and `image_2x` exactly 2400×1260. Each must be a PNG of at most 5 MB.
- Names: table `widget_shares`. Tools `list_widget_shares`, `update_widget_share`, `archive_widget_share`, `restore_widget_share`. REST under `/api/widget-shares`. The REST-only create spec is named `create_widget_share`. Audit actions are `widget_share.create|update|archive|restore|purge` with subject `widget_share/<id>`.
- Public URLs come from `cfg.Console.URL`, which defaults to `PUBLIC_URL`, with any trailing `/` trimmed. Create refuses with `ErrInvalid` "set CONSOLE_URL to share widgets" when it is empty.
- The share page runs no script. Its CSP is `default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`. Every share page footer reads `Built with <a href="https://twillingate.dev" rel="noopener">twillingate.dev</a>`.
- The web rules from CLAUDE.md apply:
  - Clickable things are `button`, `a` or `role="button"`, never a `cursor-pointer` class.
  - Nothing scrolls sideways at 360px.
  - A single-column grid says `grid-cols-1`.
  - Tables hide columns below `sm`.
- Docs change in the same commit as the code they describe, following the CLAUDE.md table. `internal/api/docs_sync_test.go` binds tools, routes and env vars.
- Commits use Conventional Commits with these scopes: `store`, `reporting`, `api`, `jobs`, `web`. Each commit message ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Go is at `/usr/local/go/bin/go`; it is not on PATH, so run `export PATH=$PATH:/usr/local/go/bin` first. Node 22. `make check` must pass before the final push. On this machine, vitest may need `--testTimeout=30000`.

## Review Focus

1. **Untrusted text in the page.** A widget title or project name containing `<`, `"` or `&` must be escaped in the body and in every `og:*` attribute. No raw HTML may reach the page. Task 3 pins this with a title of `<script>alert(1)</script> "q" & co`.
2. **`archive_at` passed, daily pass not yet run.**
   - The page and both images answer 404 at once (Task 3).
   - `list_widget_shares {state:"live"}` leaves the share out and `{state:"archived"}` includes it (Task 1).
   - `update_widget_share` answers 409 (Task 2).
3. **Malformed share paths answer 404, never 500 or a stack trace.** Examples: `/share/abc`, `/share/<id>.png.png`, `/share/<id>@3x.png`, `/share/<ID-in-uppercase>`, `/share/` (Task 3).
4. **Bad uploads answer 400 `invalid`, never 500.** Examples: JPEG bytes, a PNG of 1199×630, the two parts swapped, a missing part, a 6 MB part, a 13 MB body, a non-numeric `widget_id` (Task 4).
5. **Long names on a phone.** A 120-character widget title and the e2e project with an overlong name must not push the Shares page, the Archive section or the share page wider than 360px. The card truncates the title to two lines (Tasks 7, 9, 10 and 11).

---

## File Map

**Go**
- Create `internal/store/sqlite/migrations/032_widget_shares.sql`: the table and its indexes.
- Modify `internal/store/reporting.go`: `WidgetShare`, `NewWidgetShare`, `WidgetShareFilter`, and `PurgeResult.WidgetShares`.
- Modify `internal/store/store.go`: the seven new `Store` methods.
- Create `internal/store/sqlite/widget_shares.go`: their SQL.
- Modify `internal/store/sqlite/registry.go:228`: add `"widget_shares"` to `projectTables`.
- Modify `internal/store/sqlite/purge.go`: a shares phase.
- Create `internal/store/sqlite/widget_shares_test.go` and `migration032_test.go`.
- Modify `internal/reporting/reporting.go`: the `Store` slice and `Options.ShareBaseURL`.
- Create `internal/reporting/ops_widget_share.go`: create, list, update, archive and restore, `archiveAtFor`, `checkPNG` and the `WidgetShareOut` type.
- Create `internal/reporting/share_page.go`, `share_page.html` and `share_page_test.go`: the public routes.
- Create `internal/reporting/ops_widget_share_test.go`.
- Modify `internal/api/expose.go`: `spec.Multipart` and `restRaw`.
- Modify `internal/api/openapi.go`: a multipart request body.
- Modify `internal/api/server.go`: pass `ShareBaseURL`, mount `GET /share/{file}` on Build's mux and `GET /share/` in `RegisterOn`.
- Create `internal/api/ops_widget_shares.go` and `internal/api/widget_shares_test.go`.
- Modify `internal/api/docs_sync_test.go`: widen `spellOut`.
- Modify `internal/jobs/jobs.go` and `jobs_test.go`: archive due shares and log the purge count.
- Modify `internal/app/migrate.go` only if the interface assertion needs it (it shouldn't).

**Docs:** `docs/reporting.md`, `docs/twillingate.md`, `docs/deployment.md`, `deploy/UPGRADES.md`.

**Web**
- Modify `web/package.json`: add `html-to-image` and `@fontsource/inter`.
- Modify `web/src/lib/api.ts`: types and endpoints.
- Modify `web/src/lib/queries.ts`: `widgetSharesQuery`.
- Create `web/src/lib/share.ts`: `rangeInWords`, `ARCHIVE_AFTER`, `archiveLabel` and `isLive`.
- Create `web/src/lib/capture.ts`: `captureCard`.
- Create `web/src/hooks/use-widget-share-actions.ts`.
- Create `web/src/components/share/card-mode.ts`, `ShareCard.tsx`, `OffscreenCard.tsx`, `ShareDialog.tsx` and `ArchiveAfterSelect.tsx`.
- Modify `web/src/components/WidgetCard.tsx`, `WidgetGrid.tsx` and `pages/Dashboard.tsx`: the widget menu and the share context.
- Modify `web/src/components/widgets/table.tsx` and `stat.tsx`: card mode.
- Modify `web/src/index.css`: `.share-card` rules.
- Create `web/src/pages/Shares.tsx` and `Shares.test.tsx`.
- Modify `web/src/pages/Archive.tsx` and `Archive.test.tsx`.
- Modify `web/src/App.tsx` and `components/AppSidebar.tsx`.
- Modify `web/src/pages/gallery/ComponentsGallery.tsx` and `ComponentEntry.tsx`: the Share card view.
- Create `web/e2e/png.ts`, `web/e2e/share.spec.ts` and `web/e2e/share-cards.spec.ts`.
- Modify `web/e2e/cursor.spec.ts` and `web/e2e/phone.spec.ts`.

---

### Task 1: Migration 032 and the store

**Files:**
- Create: `internal/store/sqlite/migrations/032_widget_shares.sql`, `internal/store/sqlite/widget_shares.go`, `internal/store/sqlite/widget_shares_test.go`, `internal/store/sqlite/migration032_test.go`
- Modify: `internal/store/reporting.go`, `internal/store/store.go`, `internal/store/sqlite/registry.go:228-239`, `internal/store/sqlite/purge.go`, `deploy/UPGRADES.md`

**Interfaces:**
- Produces (package `store`):

```go
// WidgetShare is a widget_shares row without its images, joined to its
// widget's dashboard while the widget exists.
type WidgetShare struct {
	ID             string
	WidgetID       int64  // 0 once the widget is deleted
	DashboardID    int64  // 0 once the widget is deleted
	DashboardTitle string // "" once the widget is deleted
	ProjectID      int64
	ProjectName    string // copied at capture
	From, To       string // YYYY-MM-DD
	Title          string // copied at capture
	CreatedAt      string // 2006-01-02T15:04:05Z
	ArchiveAt      string // "" = project lifetime
	ArchivedAt     string // "" = not archived by hand or by the daily pass
}

type NewWidgetShare struct {
	ID                  string
	WidgetID, ProjectID int64
	From, To, Title     string
	Image, Image2x      []byte
	ArchiveAt           string // "" = project lifetime
}

// WidgetShareFilter: State "" lists every share, "live" those with no
// archived_at and no archive_at at or before Now, "archived" the rest.
type WidgetShareFilter struct {
	WidgetID int64 // 0 = every widget
	State    string
	Now      string // 2006-01-02T15:04:05Z
}
```

  `PurgeResult` gains `WidgetShares []string`.

  New `store.Store` methods (also implemented on `*sqlite.DB`):

```go
InsertWidgetShare(ctx context.Context, n NewWidgetShare, a AuditEntry) (WidgetShare, error)
GetWidgetShare(ctx context.Context, id string) (WidgetShare, error)
WidgetShareImage(ctx context.Context, id string, twoX bool) ([]byte, error)
ListWidgetShares(ctx context.Context, f WidgetShareFilter) ([]WidgetShare, error)
SetWidgetShareArchiveAt(ctx context.Context, id, archiveAt, now string, a AuditEntry) (WidgetShare, error)
SetWidgetShareArchived(ctx context.Context, id string, archived bool, archiveAt string, a AuditEntry) (WidgetShare, error)
ArchiveDueWidgetShares(ctx context.Context, now string) (int, error)
```

- [ ] **Step 1: Write the migration**

`internal/store/sqlite/migrations/032_widget_shares.sql`:

```sql
-- 032: widget shares (docs/superpowers/specs/2026-10-05-widget-shares-design.md).
-- A share is a frozen picture of one widget: two PNGs and the text they
-- show, copied at capture so the public page matches the picture after a
-- rename. It lives as long as its project (deleteProject removes it, via
-- projectTables) and outlives its widget (widget_id goes NULL). archive_at
-- NULL = project lifetime; archived_at NULL = not archived yet.
CREATE TABLE widget_shares (
    id           TEXT PRIMARY KEY,
    widget_id    INTEGER REFERENCES widgets(id) ON DELETE SET NULL,
    project_id   INTEGER NOT NULL REFERENCES projects(id),
    range_from   TEXT NOT NULL,
    range_to     TEXT NOT NULL,
    title        TEXT NOT NULL,
    project_name TEXT NOT NULL,
    image        BLOB NOT NULL,
    image_2x     BLOB NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    archive_at   TEXT,
    archived_at  TEXT
);
CREATE INDEX idx_widget_shares_widget ON widget_shares(widget_id);
CREATE INDEX idx_widget_shares_archive ON widget_shares(archive_at) WHERE archive_at IS NOT NULL;
```

- [ ] **Step 2: Add the types and the interface methods**

Put the types above into `internal/store/reporting.go`, next to `Widget`. Add `WidgetShares []string` to `PurgeResult`. Add the seven methods to `store.Store` in `store.go`, after `SetWidgetArchived`. Add `"widget_shares"` to `projectTables` in `registry.go`. The existing `TestProjectTablesMatchesSchema` then passes; without it, that test fails.

- [ ] **Step 3: Write the failing store tests**

`internal/store/sqlite/widget_shares_test.go`. Use `newTestDB(t)`, and seed a project, a dashboard and a widget with the helpers the existing reporting tests use (see `reporting_test.go`: `InsertDashboard`, `InsertWidget`; `registry_test.go` for creating a project). Write these tests:

```go
func png1(t *testing.T) []byte { return []byte("\x89PNG-1x") } // the store doesn't decode
func png2(t *testing.T) []byte { return []byte("\x89PNG-2x") }

func TestInsertWidgetShareCopiesProjectName(t *testing.T) {
	db, pid, wid := shareFixture(t) // project "blog", one widget on a user dashboard
	got, err := db.InsertWidgetShare(ctx, store.NewWidgetShare{ID: "0190a000-0000-7000-8000-000000000001",
		WidgetID: wid, ProjectID: pid, From: "2026-09-01", To: "2026-09-30", Title: "Visitors",
		Image: png1(t), Image2x: png2(t), ArchiveAt: "2026-11-01T00:00:00Z"},
		store.AuditEntry{Actor: "api", Action: "widget_share.create"})
	if err != nil { t.Fatal(err) }
	if got.ProjectName != "blog" || got.WidgetID != wid || got.DashboardID == 0 || got.ArchiveAt != "2026-11-01T00:00:00Z" {
		t.Fatalf("got %+v", got)
	}
	// audit row written with subject widget_share/<id>
}

func TestInsertWidgetShareUnknownProject(t *testing.T) // ErrNotFound, nothing inserted
func TestWidgetShareImage(t *testing.T)                // 1x and 2x bytes back; unknown id → ErrNotFound
func TestListWidgetSharesState(t *testing.T)
// shares: A live (archive_at future), B archive_at past but archived_at NULL,
// C archived_at set, D project lifetime. Now = "2026-10-06T00:00:00Z".
// State "live" → [D? A?] (order created_at DESC), "archived" → B and C, "" → all four.
// WidgetID filter narrows.
func TestSetWidgetShareArchiveAt(t *testing.T)
// live share: ok, archive_at updated ("" → project lifetime); archived share → ErrConflict;
// due share (archive_at <= now) → ErrConflict; unknown → ErrNotFound.
func TestSetWidgetShareArchived(t *testing.T)
// archive live → archived_at set; archive again → no error, unchanged (idempotent);
// restore with archiveAt "2026-12-01T00:00:00Z" → archived_at "", archive_at set;
// restore a due-but-unarchived share also takes the new archive_at.
func TestArchiveDueWidgetShares(t *testing.T)
// only shares with archive_at <= now and archived_at NULL are archived, archived_at = archive_at,
// one widget_share.archive audit row each with actor "retention"; returns the count.
func TestWidgetShareOutlivesWidget(t *testing.T)
// DELETE FROM widgets (via the purge of an archived widget) → share still there, WidgetID 0, DashboardID 0.
func TestDeleteProjectDeletesWidgetShares(t *testing.T)
// DeleteProjectData(pid) removes its shares (live and archived); another project's share stays.
func TestPurgeArchivedWidgetShares(t *testing.T)
// a share archived 40 days ago (backdate archived_at with rawExec), one archived 5 days ago;
// PurgeArchived(ctx, 30) → res.WidgetShares == [old id], audit widget_share.purge actor retention.
```

Write each body out fully in the file. The comments above are the assertions to make.

- [ ] **Step 4: Run them to verify they fail**

Run: `cd internal/store/sqlite && go test -run 'WidgetShare|ProjectTables' ./...`
Expected: compile errors for the undefined methods. After Step 2 alone, `TestProjectTablesMatchesSchema` passes and the others fail.

- [ ] **Step 5: Implement `widget_shares.go`**

```go
package sqlite

const shareStamp = "2006-01-02T15:04:05Z"

const shareCols = `s.id, COALESCE(s.widget_id,0), COALESCE(w.dashboard_id,0), COALESCE(d.title,''),
	s.project_id, s.project_name, s.range_from, s.range_to, s.title, s.created_at,
	COALESCE(s.archive_at,''), COALESCE(s.archived_at,'')`

const shareFrom = ` FROM widget_shares s
	LEFT JOIN widgets w ON w.id = s.widget_id
	LEFT JOIN dashboards d ON d.id = w.dashboard_id`

func scanShare(r rowScanner) (store.WidgetShare, error) {
	var s store.WidgetShare
	err := r.Scan(&s.ID, &s.WidgetID, &s.DashboardID, &s.DashboardTitle, &s.ProjectID, &s.ProjectName,
		&s.From, &s.To, &s.Title, &s.CreatedAt, &s.ArchiveAt, &s.ArchivedAt)
	return s, err
}

func getShare(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (store.WidgetShare, error) {
	s, err := scanShare(q.QueryRowContext(ctx, `SELECT `+shareCols+shareFrom+` WHERE s.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return s, store.Refuse(store.ErrNotFound, "widget share %s: not found", id)
	}
	return s, err
}

func (d *DB) InsertWidgetShare(ctx context.Context, n store.NewWidgetShare, a store.AuditEntry) (store.WidgetShare, error) {
	var out store.WidgetShare
	err := d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO widget_shares
			(id, widget_id, project_id, range_from, range_to, title, project_name, image, image_2x, archive_at)
			SELECT ?, ?, id, ?, ?, ?, name, ?, ?, NULLIF(?, '') FROM projects WHERE id = ?`,
			n.ID, n.WidgetID, n.From, n.To, n.Title, n.Image, n.Image2x, n.ArchiveAt, n.ProjectID)
		if err != nil { return err }
		if k, _ := res.RowsAffected(); k == 0 {
			return store.Refuse(store.ErrNotFound, "project %d: not found", n.ProjectID)
		}
		a.Subject = "widget_share/" + n.ID
		if err := audit(ctx, tx, a); err != nil { return err }
		out, err = getShare(ctx, tx, n.ID)
		return err
	})
	return out, err
}
```

The remaining methods follow the same pattern:

- `GetWidgetShare`: `getShare(ctx, d.<read handle>, id)`. Use the handle the other `Get*` methods use, for example `d.db`.
- `WidgetShareImage`: `SELECT image` or `SELECT image_2x` `FROM widget_shares WHERE id=?`. `sql.ErrNoRows` becomes `ErrNotFound`.
- `ListWidgetShares`:

```go
q := `SELECT ` + shareCols + shareFrom + `
	WHERE (?1 = 0 OR s.widget_id = ?1)
	  AND (?2 = '' OR (?2 = 'live') = (s.archived_at IS NULL AND (s.archive_at IS NULL OR s.archive_at > ?3)))
	ORDER BY COALESCE(s.archived_at, CASE WHEN s.archive_at <= ?3 THEN s.archive_at END, s.created_at) DESC, s.id DESC`
```

  Refuse a `State` other than `""`, `live` or `archived` with `ErrInvalid`.
- `SetWidgetShareArchiveAt`:

```sql
UPDATE widget_shares SET archive_at = NULLIF(?, '')
WHERE id = ? AND archived_at IS NULL AND (archive_at IS NULL OR archive_at > ?)
```

  With zero rows: an id that doesn't exist is `ErrNotFound`. An id that exists is `store.Refuse(store.ErrConflict, "widget share %s is archived; restore_widget_share instead", id)`. Write the audit row (subject `widget_share/<id>`) and return `getShare`.
- `SetWidgetShareArchived(archived=true)`:

```sql
UPDATE widget_shares SET archived_at = strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id = ? AND archived_at IS NULL
```

  Zero rows and the id doesn't exist: `ErrNotFound`. Zero rows and the id exists: no error and no audit, since it is idempotent.

  With `archived=false`:

```sql
UPDATE widget_shares SET archived_at = NULL, archive_at = NULLIF(?, '') WHERE id = ?
```

  Zero rows: `ErrNotFound`. Write the audit row and return `getShare`.
- `ArchiveDueWidgetShares` runs in one `d.tx`:

```sql
SELECT id FROM widget_shares WHERE archived_at IS NULL AND archive_at IS NOT NULL AND archive_at <= ?
```

  Then `UPDATE widget_shares SET archived_at = archive_at WHERE id = ?`, and `audit(ctx, tx, store.AuditEntry{Actor: "retention", Action: "widget_share.archive", Subject: "widget_share/" + id})` for each id. Return the count.

- [ ] **Step 6: Add the purge phase**

In `purge.go` `PurgeArchived`, after the widgets phase, follow the existing loop shape:

```go
shareIDs, err := d.purgeableStrings(ctx, `SELECT id FROM widget_shares
	 WHERE archived_at IS NOT NULL AND julianday(archived_at) < julianday('now') - ?`, days)
// errors.Join on failure; for each id: d.tx(DELETE FROM widget_shares WHERE id=?; audit{Actor:"retention",
// Action:"widget_share.purge", Subject:"widget_share/"+id}); append to res.WidgetShares on success.
```

`purgeableStrings` is a string twin of `purgeableIDs`; add it beside that function.

- [ ] **Step 7: Add the migration test**

`migration032_test.go`: `db := newTestDBAt(t, 32)`. Assert that `widget_shares` exists with the columns listed in Step 1. Then assert, through `execAll`, that deleting a widget sets `widget_id` to NULL; this needs `PRAGMA foreign_keys` on, as the runtime has it.

- [ ] **Step 8: Run the tests to verify they pass**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/store/...`
Expected: PASS.

- [ ] **Step 9: Add the UPGRADES entry and commit**

Append to `deploy/UPGRADES.md`, after the 031 section:

```markdown
### Upgrading to widget shares (migration 032)

No pre-checks. Migration 032 adds `widget_shares`, where a shared
widget's two PNGs and the text they show are kept. Nothing existing
changes. Rollback: an older binary ignores the table, and share links
answer 404 until the newer binary is back.
```

```bash
git add internal/store deploy/UPGRADES.md
git commit -m "feat(store): widget_shares table and its store methods"
```

---

### Task 2: Reporting operations

**Files:**
- Create: `internal/reporting/ops_widget_share.go`, `internal/reporting/ops_widget_share_test.go`
- Modify: `internal/reporting/reporting.go` (the `Store` slice, `Options`, `Service`)

**Interfaces:**
- Consumes: the Task 1 `store` methods and types.
- Produces (package `reporting`):

```go
type Options struct { /* existing */ ShareBaseURL string } // no trailing slash; "" disables Create

type NewShare struct {
	WidgetID, ProjectID int64
	From, To            string // YYYY-MM-DD
	ArchiveAfter        string // 7d|30d|90d|365d|project; "" = 30d
	Image, Image2x      []byte
}

type WidgetShareOut struct {
	ID             string  `json:"id"`
	URL            string  `json:"url"`
	ImageURL       string  `json:"image_url"`
	Image2xURL     string  `json:"image_2x_url"`
	WidgetID       *int64  `json:"widget_id"`
	DashboardID    *int64  `json:"dashboard_id"`
	DashboardTitle *string `json:"dashboard_title"`
	ProjectID      int64   `json:"project_id"`
	ProjectName    string  `json:"project_name"`
	From           string  `json:"from"`
	To             string  `json:"to"`
	Title          string  `json:"title"`
	CreatedAt      string  `json:"created_at"`
	ArchiveAt      *string `json:"archive_at"`
	ArchivedAt     *string `json:"archived_at"`
}

func (s *Service) CreateWidgetShare(ctx context.Context, actor string, in NewShare) (WidgetShareOut, error)
func (s *Service) ListWidgetShares(ctx context.Context, widgetID int64, state string) ([]WidgetShareOut, error)
func (s *Service) UpdateWidgetShare(ctx context.Context, actor, id, archiveAfter string) (WidgetShareOut, error)
func (s *Service) ArchiveWidgetShare(ctx context.Context, actor, id string) (WidgetShareOut, error)
func (s *Service) RestoreWidgetShare(ctx context.Context, actor, id, archiveAfter string) (WidgetShareOut, error)
func (s *Service) LiveWidgetShare(ctx context.Context, id string) (store.WidgetShare, bool, error) // for Task 3
```

`ArchivedAt` in `WidgetShareOut`: when the row's `archived_at` is empty but `archive_at` is at or before now, report `archive_at`. Clients then see a due share as archived, which is what the page does.

- [ ] **Step 1: Write the failing tests**

`ops_widget_share_test.go`. Use the package's existing service test helper (see `helpers_test.go` / `testutil_test.go`) with a real sqlite store, `Options{ShareBaseURL: "https://c.example", ArchivedDays: 30, Now: fixed(2026-10-06T12:00:00Z)}`. Generate real PNGs with stdlib:

```go
func pngOf(t *testing.T, w, h int) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil { t.Fatal(err) }
	return b.Bytes()
}
```

Tests:

```go
func TestCreateWidgetShare(t *testing.T)
// 1200×630 + 2400×1260, archive_after "" → URL "https://c.example/share/<uuid>", ImageURL +".png",
// Image2xURL +"@2x.png", ArchiveAt "2026-11-05T12:00:00Z", id parses with uuid.Parse and has Version()==7.
func TestCreateWidgetShareArchiveAfter(t *testing.T)
// table: 7d → +7d, 90d, 365d, project → ArchiveAt nil; "2w" → ErrInvalid.
func TestCreateWidgetShareRefusals(t *testing.T)
// each → errors.Is(err, store.ErrX):
//  ShareBaseURL "" → ErrInvalid (message contains "CONSOLE_URL")
//  unknown widget → ErrNotFound; archived widget → ErrNotFound
//  image not a PNG (jpeg bytes / "hello") → ErrInvalid
//  image 1199×630 → ErrInvalid; image_2x 2400×1259 → ErrInvalid; parts swapped → ErrInvalid
//  image over 5 MB (len > 5<<20, e.g. a valid PNG padded? use a 5<<20+1 byte slice starting with the PNG magic) → ErrInvalid
//  from "2026-13-01" → ErrInvalid; from > to → ErrInvalid; span > 365 days → ErrInvalid
//  to "2026-10-07" (tomorrow) → ErrInvalid
//  unknown project → ErrNotFound
func TestCreateWidgetShareSystemWidget(t *testing.T) // a widget on system dashboard 1 can be shared
func TestUpdateWidgetShare(t *testing.T)
// "90d" → archive_at now+90d; "project" → nil; on archived → ErrConflict; on a due share
// (Now moved past archive_at) → ErrConflict; "" → ErrInvalid (update requires a value).
func TestArchiveRestoreWidgetShare(t *testing.T)
// archive → ArchivedAt set; restore "" → archive_at now+30d, ArchivedAt nil; restore "project" → nil.
func TestListWidgetSharesReportsDueAsArchived(t *testing.T)
// a share whose archive_at < now and archived_at NULL: state "live" excludes it, "archived" includes it
// with ArchivedAt == its archive_at; state "x" → ErrInvalid.
func TestWidgetShareOutAfterWidgetDeleted(t *testing.T)
// delete the widget row → WidgetID, DashboardID, DashboardTitle are nil in the output.
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/reporting -run WidgetShare`
Expected: compile errors.

- [ ] **Step 3: Implement**

Add the seven Task 1 methods, except `ArchiveDueWidgetShares`, to `reporting.Store`. Add `ShareBaseURL` to `Options` and a `shareBase string` to `Service`, set in `New` with `strings.TrimRight(opt.ShareBaseURL, "/")`.

`ops_widget_share.go`:

```go
const (
	shareStamp   = "2006-01-02T15:04:05Z"
	maxShareSize = 5 << 20
)

var archivePeriods = map[string]int{"7d": 7, "30d": 30, "90d": 90, "365d": 365}

// archiveAtFor turns an archive_after value into archive_at: "" for
// project lifetime ("project"), else now plus the period.
func archiveAtFor(v string, now time.Time) (string, error) {
	if v == "project" { return "", nil }
	days, ok := archivePeriods[v]
	if !ok {
		return "", store.Refuse(store.ErrInvalid, "archive_after %q: want 7d, 30d, 90d, 365d or project", v)
	}
	return now.UTC().AddDate(0, 0, days).Format(shareStamp), nil
}

// checkPNG refuses anything but a PNG of exactly w×h, at most 5 MB.
func checkPNG(name string, b []byte, w, h int) error {
	if len(b) > maxShareSize {
		return store.Refuse(store.ErrInvalid, "%s: %d bytes; at most 5 MB", name, len(b))
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil { return store.Refuse(store.ErrInvalid, "%s: not a PNG", name) }
	if cfg.Width != w || cfg.Height != h {
		return store.Refuse(store.ErrInvalid, "%s: %d×%d; want %d×%d", name, cfg.Width, cfg.Height, w, h)
	}
	return nil
}

func newShareID() string {
	if id, err := uuid.NewV7(); err == nil { return id.String() }
	return uuid.NewString()
}
```

`CreateWidgetShare`:
1. Refuse when `s.shareBase == ""`.
2. `w, err := s.st.GetWidget`. A widget with `w.ArchivedAt != ""` is `ErrNotFound`.
3. `civil.Parse` both dates. An error, `from > to` or `to` after today is `ErrInvalid`. For the span use the existing `checkDates(from, to)` in `ops_dashboard.go`; for today use `civil.DateOf(s.now())`.
4. Run `checkPNG("image", …, 1200, 630)` and `checkPNG("image_2x", …, 2400, 1260)`.
5. `archiveAtFor(orDefault(in.ArchiveAfter, "30d"), s.now())`.
6. The title is `w.Title`, falling back to `w.Name`.
7. `s.st.InsertWidgetShare(ctx, store.NewWidgetShare{...}, store.AuditEntry{Actor: actor, Action: "widget_share.create"})`.
8. Return `s.shareOut(row)`.

`shareOut` builds the URLs from `s.shareBase + "/share/" + id` and turns zero values into nil pointers. It applies the due rule above: a row with an empty `ArchivedAt` and `ArchiveAt <= now` reports `ArchiveAt` as `ArchivedAt`.

`ListWidgetShares` passes `store.WidgetShareFilter{WidgetID, State, Now: s.now().UTC().Format(shareStamp)}`.

`UpdateWidgetShare` requires a non-empty `archiveAfter`, then calls `SetWidgetShareArchiveAt(ctx, id, at, now, AuditEntry{Action: "widget_share.update"})`.

`ArchiveWidgetShare` calls `SetWidgetShareArchived(ctx, id, true, "", …"widget_share.archive")`.

`RestoreWidgetShare` resolves `archiveAfter` (default 30d), then calls `SetWidgetShareArchived(ctx, id, false, at, …"widget_share.restore")`.

`LiveWidgetShare` returns the row and `live = row.ArchivedAt == "" && (row.ArchiveAt == "" || row.ArchiveAt > now)`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/reporting/... ./internal/archtest/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/reporting
git commit -m "feat(reporting): create, list, archive and restore widget shares"
```

---

### Task 3: The public share page and images

**Files:**
- Create: `internal/reporting/share_page.go`, `internal/reporting/share_page.html`, `internal/reporting/share_page_test.go`

**Interfaces:**
- Consumes: `Service.LiveWidgetShare`, `store.WidgetShareImage` (through `s.st`), and `s.shareBase`.
- Produces: `func (s *Service) SharePages() http.Handler`, serving the pattern `GET /share/{file}`. The mounting is Task 4.

- [ ] **Step 1: Write the failing tests**

`share_page_test.go` uses `httptest` against `s.SharePages()` mounted on a `http.ServeMux` at `GET /share/{file}`. Seed one share with the title `<script>alert(1)</script> "q" & co` and the project `blog`.

```go
func TestSharePage(t *testing.T)
// GET /share/<id> → 200, Content-Type text/html; charset=utf-8, Cache-Control public, max-age=300,
// CSP exactly the Global Constraints value, X-Content-Type-Options nosniff,
// body contains: <meta property="og:image" content="https://c.example/share/<id>.png">,
// og:image:width 1200, og:image:height 630, og:url, og:title (escaped: &lt;script&gt;… &#34;q&#34; &amp; co),
// <meta name="twitter:card" content="summary_large_image">, <meta name="robots" content="noindex">,
// <img … src="/share/<id>@2x.png" … width="1200" height="630">, the range "Sep 1 – Sep 30, 2026",
// <a href="https://twillingate.dev" rel="noopener">twillingate.dev</a>, and NOT "<script" (case-insensitive).
func TestShareImages(t *testing.T)
// /share/<id>.png → 200 image/png, bytes == the 1x upload, Cache-Control public, max-age=3600,
// X-Robots-Tag noindex; /share/<id>@2x.png → the 2x bytes.
func TestShareNotFound(t *testing.T)
// 404 with Cache-Control no-store for: unknown uuid, "abc", "<id>.png.png", "<id>@3x.png",
// "<id>@2x", "<ID upper-cased>", an archived share (page, .png, @2x.png),
// a share whose archive_at passed (Now after it, archived_at NULL) — all three routes.
func TestRangeInWords(t *testing.T)
// ("2026-09-05","2026-10-04") → "Sep 5 – Oct 4, 2026"; ("2025-12-20","2026-01-10") → "Dec 20, 2025 – Jan 10, 2026";
// ("2026-09-05","2026-09-05") → "Sep 5, 2026".
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/reporting -run 'Share(Page|Images|NotFound)|RangeInWords'`
Expected: compile errors.

- [ ] **Step 3: Implement**

`share_page.go`:

```go
//go:embed share_page.html
var sharePageSrc string

var sharePage = template.Must(template.New("share").Parse(sharePageSrc))

const shareCSP = "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func (s *Service) SharePages() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file := r.PathValue("file")
		id, kind := file, "page"
		if strings.HasSuffix(id, "@2x.png") { id, kind = strings.TrimSuffix(id, "@2x.png"), "2x" } else if strings.HasSuffix(id, ".png") { id, kind = strings.TrimSuffix(id, ".png"), "1x" }
		if u, err := uuid.Parse(id); err != nil || u.String() != id { shareNotFound(w); return }
		sh, live, err := s.LiveWidgetShare(r.Context(), id)
		if err != nil || !live { shareNotFound(w); return } // log non-NotFound errors via the package logger if there is one
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if kind != "page" {
			b, err := s.st.WidgetShareImage(r.Context(), id, kind == "2x")
			if err != nil { shareNotFound(w); return }
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("X-Robots-Tag", "noindex")
			w.Write(b)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Content-Security-Policy", shareCSP)
		sharePage.Execute(w, sharePageData{ /* fields below */ })
	})
}

func shareNotFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "not found", http.StatusNotFound)
}

// rangeInWords: "Sep 5 – Oct 4, 2026"; the year on both ends only when they differ.
func rangeInWords(from, to string) string
```

`sharePageData` holds `Title, ProjectName, Range, URL, ImageURL, Image2xPath, Alt` and an `Icon template.HTML`. `Icon` is the iceberg SVG copied verbatim from `internal/api/oauth_page.html` (around line 145) into a Go const. Copying is fine: `reporting` cannot import `api`.

`share_page.html` is a complete HTML document:
- **head:** `<meta charset>`, the viewport, `<title>{{.Title}} · {{.ProjectName}}</title>`, the og and twitter tags from the test, robots noindex, and a `<style>`.
- **style:**
  - The body is centred with `max-width:1200px` and `margin:0 auto`, padded `16px`.
  - The img gets `width:100%;height:auto;border-radius:12px;box-shadow`.
  - The h1 gets `font:600 1.5rem/1.3 system-ui` and `overflow-wrap:anywhere`.
  - A muted line holds the project and range.
  - The footer is small and muted, with the link.
  - `@media (prefers-color-scheme: dark)` sets a dark background and light text.
  - Nothing may be wider than the viewport.
- **body:** `<main>`, then `<h1>`, then `<p class=meta>{{.ProjectName}} · {{.Range}}</p>`, then `<img src="{{.Image2xPath}}" width=1200 height=630 alt="{{.Alt}}">`. After it, `<footer>Built with <a href="https://twillingate.dev" rel="noopener">twillingate.dev</a></footer>`, preceded by the inline icon.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/reporting/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/reporting
git commit -m "feat(reporting): serve shared widgets as a public page and images"
```

---

### Task 4: API routes, MCP tools, mounting, docs

**Files:**
- Create: `internal/api/ops_widget_shares.go`, `internal/api/widget_shares_test.go`
- Modify: `internal/api/expose.go`, `internal/api/openapi.go`, `internal/api/server.go:38-130`, `internal/api/ops_reporting.go` (call `h.registerWidgetShares(r)` from `registerReporting`), `internal/api/docs_sync_test.go` (`spellOut` up to 50), `docs/reporting.md`, `docs/twillingate.md`, `docs/deployment.md`

**Interfaces:**
- Consumes: the Task 2 `Service` methods and Task 3 `SharePages()`.
- Produces: the REST routes and MCP tools named in Global Constraints.

- [ ] **Step 1: Write the failing tests**

`widget_shares_test.go`. Follow the existing api tests that build the full handler with a token, for example the ones exercising `/api/widgets/{id}/archive`. Set `PUBLIC_URL=https://c.example` in their config. Build multipart bodies with `mime/multipart` and PNGs with `image/png`, as in Task 2.

```go
func TestCreateWidgetShareREST(t *testing.T)
// POST /api/widget-shares multipart {widget_id, project_id, from, to, archive_after, image, image_2x}
// → 201, JSON has id, url "https://c.example/share/<id>", archive_at.
func TestCreateWidgetShareRESTRefusals(t *testing.T)
// 400 {"error":{"code":"invalid"}} for: JSON body instead of multipart, missing image, missing image_2x,
// widget_id "x", a 6 MB image part, a 13 MB body; 404 not_found for unknown widget.
func TestWidgetShareRoutes(t *testing.T)
// GET /api/widget-shares?state=live lists it; PATCH /api/widget-shares/{id} {"archive_after":"7d"} → 200;
// POST …/{id}/archive → 200 archived_at set; GET ?state=archived lists it; POST …/{id}/restore {} → archive_at +30d.
func TestWidgetShareToolsOverMCP(t *testing.T)
// the four tools are registered (list via the server's tool list helper used elsewhere) and
// list_widget_shares returns the created share.
func TestSharePagesArePublic(t *testing.T)
// on NewHandler's mux: GET /share/<id> without a token → 200; GET /api/widget-shares without a token → 401;
// GET /share/<id>.png without a token → 200 image/png.
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/api -run 'WidgetShare|SharePages'`
Expected: FAIL (404s and compile errors).

- [ ] **Step 3: Add `restRaw` and the multipart OpenAPI body**

In `expose.go`, add `Multipart bool` to `spec`, then:

```go
// restRaw registers a REST-only route whose handler reads the request
// itself (a multipart upload). In and Out still describe it, for the
// OpenAPI document and the docs tests.
func restRaw[In, Out any](r *registrar, s spec, h http.HandlerFunc) {
	s.RESTOnly, s.Multipart = true, true
	s.in, s.out = schemaFor[In](), schemaFor[Out]()
	r.specs = append(r.specs, s)
	if r.rest != nil { r.rest.HandleFunc(s.Method+" "+s.Path, h) }
}
```

In `openapi.go`, where the request body is built from `s.in`, use the content type `multipart/form-data` when `s.Multipart` is set (properties from `s.in`, with `image` and `image_2x` as `type: string, format: binary`). Keep `application/json` otherwise.

- [ ] **Step 4: Add the handlers**

`ops_widget_shares.go`:

```go
const maxShareUpload = 12 << 20 // two 5 MB images and the fields

type createWidgetShareIn struct {
	WidgetID     int64  `json:"widget_id" jsonschema:"the widget to share"`
	ProjectID    int64  `json:"project_id" jsonschema:"the project the picture shows"`
	From         string `json:"from" jsonschema:"range start, YYYY-MM-DD"`
	To           string `json:"to" jsonschema:"range end, YYYY-MM-DD, not in the future"`
	ArchiveAfter string `json:"archive_after,omitempty" jsonschema:"7d, 30d, 90d, 365d or project; default 30d"`
	Image        string `json:"image" jsonschema:"1200×630 PNG file, at most 5 MB"`
	Image2x      string `json:"image_2x" jsonschema:"2400×1260 PNG file, at most 5 MB"`
}
type widgetSharesIn struct {
	WidgetID int64  `json:"widget_id,omitempty" jsonschema:"only this widget's shares; omit for every widget"`
	State    string `json:"state,omitempty" jsonschema:"live or archived; omit for both"`
}
type widgetSharesOut struct { Shares []reporting.WidgetShareOut `json:"shares"` }
type widgetShareIn struct { ID string `json:"id" jsonschema:"share id (a UUID)"` }
type widgetShareDateIn struct {
	ID           string `json:"id" jsonschema:"share id (a UUID)"`
	ArchiveAfter string `json:"archive_after" jsonschema:"7d, 30d, 90d, 365d or project (no date: the share lives as long as its project)"`
}
type widgetShareRestoreIn struct {
	ID           string `json:"id" jsonschema:"share id (a UUID)"`
	ArchiveAfter string `json:"archive_after,omitempty" jsonschema:"7d, 30d, 90d, 365d or project; default 30d"`
}

func (h *host) registerWidgetShares(r *registrar) {
	const p = "/api/widget-shares"
	restRaw[createWidgetShareIn, reporting.WidgetShareOut](r, spec{Name: "create_widget_share", Method: "POST", Path: p,
		Status: http.StatusCreated, Description: "Share a widget: two PNGs the web app captured. multipart/form-data."},
		h.createWidgetShare)
	expose(r, spec{Name: "list_widget_shares", Annotations: ro, Method: "GET", Path: p, Description: "…"}, h.listWidgetShares)
	expose(r, spec{Name: "update_widget_share", Annotations: idem, Method: "PATCH", Path: p + "/{id}", Description: "…"}, h.updateWidgetShare)
	expose(r, spec{Name: "archive_widget_share", Annotations: idem, Method: "POST", Path: p + "/{id}/archive", Description: "…"}, h.archiveWidgetShare)
	expose(r, spec{Name: "restore_widget_share", Annotations: idem, Method: "POST", Path: p + "/{id}/restore", Description: "…"}, h.restoreWidgetShare)
}
```

Write each `Description` in full, in the style of the neighbouring tools. Say what the operation does, that a share is a public frozen image at `/share/<id>`, and what the archive date means.

`createWidgetShare(w, r)`:
1. `r.Body = http.MaxBytesReader(w, r.Body, maxShareUpload)`.
2. `r.ParseMultipartForm(maxShareUpload)`. An error, or a Content-Type that is not multipart, goes to `writeError(w, h.logger, r, invalidf("expected multipart/form-data with widget_id, project_id, from, to, image and image_2x: %v", err))`.
3. Parse the ints with `strconv.ParseInt`; failures become `invalidf`.
4. Read each file with `r.FormFile`, then `io.ReadAll(io.LimitReader(f, 5<<20+1))`. A missing part is `invalidf("image is required")`.
5. Call `h.rep.CreateWidgetShare(r.Context(), "api", reporting.NewShare{…})`. Errors go to `writeError`.
6. On success, `writeJSON(w, http.StatusCreated, out)`.

The other handlers are thin wrappers that use `actorFrom(ctx)`.

- [ ] **Step 5: Mount the public routes**

In `server.go` `Build`, pass `ShareBaseURL: cfg.Console.URL` in `reporting.Options`. After the docs route, add:

```go
	// Shared widgets are public by design: unlisted frozen images
	// (docs/superpowers/specs/2026-10-05-widget-shares-design.md, D2).
	protected.Handle("GET /share/{file}", rep.SharePages())
```

In `RegisterOn`, add `mux.Handle("GET /share/", protected)` and extend the doc comments that list routes. Shared listeners use `RegisterOn` too, so nothing else changes.

- [ ] **Step 6: Docs (same commit)**

- **`docs/reporting.md` `## Tools`:** add four rows.
  - `| `list_widget_shares` | `widget_id`?, `state`? (`live`/`archived`) | every share, newest first, with its URLs and archive date |`
  - `update_widget_share`, `archive_widget_share` and `restore_widget_share`, in the same style.
- **`docs/reporting.md` `## HTTP API`:** add five rows.
  - `| `POST` | `/api/widget-shares` | `create_widget_share`, REST only: no MCP tool | multipart: `widget_id`, `project_id`, `from`, `to`, `archive_after`, `image`, `image_2x` → 201 |`
  - `GET`, `PATCH /api/widget-shares/{id}`, `POST …/{id}/archive` and `POST …/{id}/restore`, each with its tool in the Mirrors cell.
  - Update the prose that says only one route has no tool: it is now two, `view` and `create_widget_share`.
- **`docs/reporting.md`, new section `## Sharing a widget`** (before `## Archiving and the purge`):
  - what a share is (D1),
  - the card and its watermark (D4),
  - the Share dialog and Download PNG,
  - the Shares page (D8),
  - Archive after and its five choices (D7),
  - that a share lives as long as its project and outlives its widget (D5),
  - that feeds keep their preview copy.
- **`docs/reporting.md` `## Archiving and the purge`:** add archived shares on the Archive page (D9), and extend the purge sentence to "projects, dashboards, widgets and widget shares".
- **`docs/twillingate.md`:**
  - The `serve -console` row (:37) becomes: "The console: MCP at `/mcp`, REST at `/api/`, the login, the dashboards at `/app/`, and shared widgets at `/share/` (public)".
  - Change the tool-count sentence (:905) to forty-two, with "twenty that build the dashboards".
  - Next to the "Neither needs a token" paragraph (:951-956), add that `/share/<id>`, `/share/<id>.png` and `/share/<id>@2x.png` need no token either: they serve shared images, which are public by design.
- **`docs/deployment.md`:**
  - Extend the `RETENTION_ARCHIVED_DAYS` row to "…a project (with all its data and its widget shares), a dashboard, a widget or a widget share…".
  - Under `## Dashboards at /app/` (:291), add a short subsection "Shared widgets on a private console" with this Caddy example:

```
console.example.com {
    @share path /share/*
    handle @share {
        reverse_proxy 127.0.0.1:8080
    }
    respond 404
}
```

  Its text: expose only `/share/*` when the console otherwise stays on a LAN or tailnet. A console that is already public needs nothing.
- **`docs_sync_test.go`:** extend `spellOut`'s table to cover up to fifty ("forty-one" … "fifty").

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/api/... ./internal/reporting/... ./internal/archtest/...`
Expected: PASS, including `TestDocumentNamesEveryTool` and `TestDocumentMatchesRoutes`.

- [ ] **Step 8: Commit**

```bash
git add internal/api docs
git commit -m "feat(api): widget share routes and tools, public /share/ pages"
```

---

### Task 5: The daily pass

**Files:**
- Modify: `internal/jobs/jobs.go:28-50` (`Store` gains `ArchiveDueWidgetShares(ctx, now string) (int, error)`) and `RunDailyPass` (`:84`), `internal/jobs/jobs_test.go`

**Interfaces:**
- Consumes: `store.ArchiveDueWidgetShares` and `PurgeResult.WidgetShares` from Task 1.

- [ ] **Step 1: Write the failing tests**

In `jobs_test.go`, follow `TestRunDailyPassPurgesArchivedAndReloadsRegistryOnProjectPurge` (`:396`), with the clock at `2026-08-22T04:00Z` from `setup`:

```go
func TestRunDailyPassArchivesDueWidgetShares(t *testing.T)
// insert shares (rawExec, image bytes x'00'): due (archive_at 2026-08-21T00:00:00Z), future, project-lifetime;
// RunDailyPass → only the due one has archived_at = '2026-08-21T00:00:00Z'.
func TestRunDailyPassPurgesOldArchivedWidgetShares(t *testing.T)
// share archived 2026-07-01 with RETENTION_ARCHIVED_DAYS=30 → gone after the pass.
func TestRunDailyPassKeepsArchivedWidgetSharesWhenArchivedDaysIsZero(t *testing.T)
// RETENTION_ARCHIVED_DAYS=0 → archived share stays; a due share is still archived (archiving does not depend on the purge).
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/jobs -run WidgetShares`
Expected: FAIL.

- [ ] **Step 3: Implement**

At the start of `RunDailyPass`, before the purge block, run this whatever `ArchivedDays` is:

```go
	if n, err := j.st.ArchiveDueWidgetShares(ctx, j.now().UTC().Format("2006-01-02T15:04:05Z")); err != nil {
		j.logger.Error("archive due widget shares", "err", err)
	} else if n > 0 {
		j.logger.Info("archive due widget shares", "shares", n)
	}
```

Add `"widget_shares", len(res.WidgetShares)` to the existing `"purge archived"` log line. Use the field and method names the file actually uses for its clock and logger.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/jobs/... ./internal/app/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jobs
git commit -m "feat(jobs): archive widget shares at their date, purge them with the archive"
```

---

### Task 6: Web client, helpers and actions

**Files:**
- Modify: `web/src/lib/api.ts`, `web/src/lib/queries.ts`
- Create: `web/src/lib/share.ts`, `web/src/lib/share.test.ts`, `web/src/hooks/use-widget-share-actions.ts`, `web/src/hooks/use-widget-share-actions.test.tsx`

**Interfaces:**
- Produces:

```ts
// lib/api.ts
export type ArchiveAfter = '7d' | '30d' | '90d' | '365d' | 'project'
export type ShareState = 'live' | 'archived'
export interface WidgetShare {
  id: string; url: string; image_url: string; image_2x_url: string
  widget_id: number | null; dashboard_id: number | null; dashboard_title: string | null
  project_id: number; project_name: string; from: string; to: string; title: string
  created_at: string; archive_at: string | null; archived_at: string | null
}
// in endpoints:
widgetShares: (q: { widget_id?: number; state?: ShareState }) => api<{ shares: WidgetShare[] }>(`/api/widget-shares${toQuery(q)}`),
createWidgetShare: (form: FormData) => api<WidgetShare>('/api/widget-shares', { method: 'POST', body: form }),
updateWidgetShare: (id: string, archive_after: ArchiveAfter) => api<WidgetShare>(`/api/widget-shares/${id}`, json('PATCH', { archive_after })),
archiveWidgetShare: (id: string) => api<WidgetShare>(`/api/widget-shares/${id}/archive`, json('POST', {})),
restoreWidgetShare: (id: string, archive_after: ArchiveAfter) => api<WidgetShare>(`/api/widget-shares/${id}/restore`, json('POST', { archive_after })),

// lib/queries.ts
export const widgetSharesQuery = (q: { widget_id?: number; state?: ShareState }) => ({
  queryKey: ['widget-shares', q.state ?? 'all', q.widget_id ?? 'all'] as const,
  queryFn: () => endpoints.widgetShares(q),
})

// lib/share.ts
export const ARCHIVE_AFTER: { value: ArchiveAfter; label: string }[] = [
  { value: '7d', label: '1 week' }, { value: '30d', label: '1 month' }, { value: '90d', label: '3 months' },
  { value: '365d', label: '1 year' }, { value: 'project', label: 'Project lifetime' },
]
export const DEFAULT_ARCHIVE_AFTER: ArchiveAfter = '30d'
export function rangeInWords(from: string, to: string): string   // en-US, UTC: "Sep 5 – Oct 4, 2026"
export function archiveLabel(share: WidgetShare): string          // "Nov 4" (en-US, UTC) or "Project lifetime"
export function embedCode(share: WidgetShare): string
// `<a href="${url}"><img src="${image_url}" srcset="${image_2x_url} 2x" alt="${escaped title}" width="600" height="315"></a>`

// hooks/use-widget-share-actions.ts
export interface WidgetShareActions {
  pending: boolean
  create(form: FormData): Promise<WidgetShare | undefined>
  setArchiveAfter(id: string, v: ArchiveAfter): Promise<WidgetShare | undefined>
  archive(id: string): Promise<WidgetShare | undefined>
  restore(id: string, v: ArchiveAfter): Promise<WidgetShare | undefined>
}
export function useWidgetShareActions(): WidgetShareActions
```

The hook copies `run()` from `hooks/use-project-actions.ts:36-58`: it sets pending, awaits, shows a toast and invalidates `['widget-shares']`. Toasts: "Link created", "Archive date changed", "Share archived", "Share restored".

- [ ] **Step 1: Write the failing tests**

`share.test.ts`:
- `rangeInWords('2026-09-05','2026-10-04') === 'Sep 5 – Oct 4, 2026'`, plus the cross-year and single-day cases with the Go wording from Task 3.
- `archiveLabel` gives `'Project lifetime'` for a null `archive_at` and `'Nov 4'` for `'2026-11-04T10:00:00Z'`.
- `embedCode` escapes `"` and `<` in the title.

`use-widget-share-actions.test.tsx`: spy on `endpoints.createWidgetShare`, render the hook with `renderWithProviders`, and assert the toast and that `['widget-shares']` is invalidated (spy on `QueryClient.invalidateQueries`).

- [ ] **Step 2: Run them to verify they fail**

Run: `cd web && npx vitest run src/lib/share.test.ts src/hooks/use-widget-share-actions.test.tsx --testTimeout=30000`
Expected: FAIL (module not found).

- [ ] **Step 3: Implement**

Implement as specified above. `rangeInWords` and `archiveLabel` use `toLocaleDateString('en-US', { month: 'short', day: 'numeric', year?: 'numeric', timeZone: 'UTC' })`. For the cross-year form, mirror the Go `rangeInWords`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/lib src/hooks --testTimeout=30000 && npm run typecheck`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/lib web/src/hooks
git commit -m "feat(web): widget share client, helpers and actions"
```

---

### Task 7: The share card, card mode and capture

**Files:**
- Modify: `web/package.json`, `web/package-lock.json` (`npm install html-to-image @fontsource/inter`), `web/src/index.css`, `web/src/components/widgets/table.tsx`, `web/src/components/widgets/stat.tsx`, `web/src/pages/gallery/ComponentsGallery.tsx`, `web/src/pages/gallery/ComponentEntry.tsx`
- Create: `web/src/components/share/card-mode.ts`, `web/src/components/share/ShareCard.tsx`, `web/src/components/share/ShareCard.test.tsx`, `web/src/components/share/OffscreenCard.tsx`, `web/src/lib/capture.ts`

**Interfaces:**
- Consumes: `rangeInWords` (Task 6) and the `widgets` registry (`componentOf`, `WidgetModule`).
- Produces:

```ts
// components/share/card-mode.ts
export const CardMode = createContext(false)
export const useCardMode = () => useContext(CardMode)
export const CARD = { width: 1200, height: 630 } as const

// components/share/ShareCard.tsx
export interface ShareCardProps {
  component: string; data: unknown; props: Record<string, unknown>
  title: string; projectName: string; from: string; to: string
}
export const ShareCard: React.ForwardRefExoticComponent<ShareCardProps & React.RefAttributes<HTMLDivElement>>

// components/share/OffscreenCard.tsx — renders ShareCard in a portal at left:-10000px, calls onNode once mounted
export function OffscreenCard(p: ShareCardProps & { onNode(node: HTMLDivElement): void }): JSX.Element

// lib/capture.ts
export async function captureCard(node: HTMLElement): Promise<{ image: Blob; image2x: Blob }>
export function downloadBlob(blob: Blob, filename: string): void
```

- [ ] **Step 1: Write the failing tests**

`ShareCard.test.tsx`:
- Renders `stat` and `table` examples from `widgets.stat.examples[0]` and `widgets.table.examples[0]`.
- The root has `data-share-card`, its inline style is `width:1200px; height:630px`, and it has the `share-card` class.
- The title is in an element that clamps to two lines (class `line-clamp-2`).
- The project and range line reads `blog · Sep 5 – Oct 4, 2026`.
- The watermark text `twillingate.dev` and an `svg` (IcebergLogo) are present.
- In card mode, the table renders at most `CARD_ROWS` (8) rows, then `and 4 more` for a 12-row fixture, with no pagination or filter controls (no `button` named Next or Filter).
- No `.recharts-tooltip-wrapper` is visible.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd web && npx vitest run src/components/share --testTimeout=30000`
Expected: FAIL.

- [ ] **Step 3: Implement the card**

`ShareCard.tsx` (imports `@fontsource/inter/400.css` and `@fontsource/inter/600.css`):

```tsx
export const ShareCard = forwardRef<HTMLDivElement, ShareCardProps>(function ShareCard(p, ref) {
  const Component = widgets[p.component]?.default
  return (
    <div ref={ref} data-share-card className="share-card flex flex-col bg-background text-foreground"
      style={{ width: CARD.width, height: CARD.height, padding: 56 }}>
      <h2 className="line-clamp-2 text-[44px] leading-[1.15] font-semibold tracking-tight">{p.title}</h2>
      <p className="mt-2 truncate text-[22px] text-muted-foreground">{p.projectName} · {rangeInWords(p.from, p.to)}</p>
      <div className="relative mt-6 min-h-0 flex-1">
        <CardMode.Provider value={true}>
          <Suspense fallback={null}>{Component && <Component data={p.data as never} props={p.props} />}</Suspense>
        </CardMode.Provider>
      </div>
      <div className="mt-4 flex items-center justify-end gap-2 text-[16px] text-muted-foreground opacity-55">
        <IcebergLogo className="size-5" /> twillingate.dev
      </div>
    </div>
  )
})
```

The chart area is `flex-1`: about 1088 wide and roughly 400 high once the title, meta and watermark have taken their share. The watermark sits in its own row below the chart, so it never overlaps it.

Add to `index.css`, in the components layer:

```css
.share-card { font-family: 'Inter', ui-sans-serif, system-ui, sans-serif; }
.share-card .recharts-cartesian-axis-tick text,
.share-card .recharts-polar-angle-axis-tick text,
.share-card .recharts-legend-item-text,
.share-card .recharts-label { font-size: 15px; }
.share-card .recharts-tooltip-wrapper { display: none; }
```

15px is about 1.4 times the dashboard's 11px. Remove the tooltip CSS line if the test proves tooltips never render without hover.

**Card mode in components.** `table.tsx` reads `useCardMode()`. In card mode it renders the first `CARD_ROWS = 8` rows of what it has, drops its pager, filters and sort controls, and adds a muted row `and {total - 8} more` when there are more (`total` is `page.matched` when known, else the row count). `stat.tsx` reads `useCardMode()` and renders its value at `text-[96px]`, centred, with the label under it. Then go through the other 16 components in the gallery Share card view (next step). Where one clips or looks wrong at card size, add a small `useCardMode()` branch in that component. Never change `contract`: `components.json` must not change. Every chart in `components/widgets` already sets `isAnimationActive={false}`; keep it that way.

`capture.ts`:

```ts
import { toBlob } from 'html-to-image'
const frames = (n: number) => new Promise<void>((r) => { const step = (k: number) => (k ? requestAnimationFrame(() => step(k - 1)) : r()); step(n) })
export async function captureCard(node: HTMLElement) {
  await document.fonts.ready
  await frames(2)
  const opts = { width: 1200, height: 630, cacheBust: true, backgroundColor: getComputedStyle(node).backgroundColor }
  const [image, image2x] = await Promise.all([toBlob(node, { ...opts, pixelRatio: 1 }), toBlob(node, { ...opts, pixelRatio: 2 })])
  if (!image || !image2x) throw new Error('Could not capture the card')
  return { image, image2x }
}
export function downloadBlob(blob: Blob, filename: string) {
  const a = document.createElement('a'); a.href = URL.createObjectURL(blob); a.download = filename
  a.click(); setTimeout(() => URL.revokeObjectURL(a.href), 1000)
}
```

`OffscreenCard.tsx`: `createPortal(<div aria-hidden style={{ position: 'fixed', left: -10000, top: 0, pointerEvents: 'none' }}><ShareCard ref={…} {...p} /></div>, document.body)`. It calls `onNode(node)` from `useEffect` once the ref is set. The node is inside `<html class="dark">` when the system is dark, so the card takes the sharer's theme.

- [ ] **Step 4: Add the gallery's Share card view**

In `ComponentsGallery.tsx`, add a two-option `ToggleGroup` (`ui/toggle-group`) with "Tiles" and "Share cards", kept in `?view=cards` through `useSearchParams`.

In card view, `ComponentEntry` renders each example as a `ShareCard` (title `example.title`, project `example project`, range `2026-09-05`–`2026-10-04`). The card sits in a wrapper that keeps the 1200:630 ratio and scales to the column width with `transform: scale(w/1200)` (measure `w` with a `ResizeObserver`), so nothing scrolls sideways.

Each card gets two buttons:
- **PNG 1x**, `data-testid="card-png-1x"`: captures the card and downloads `<component>-<n>-1x.png`.
- **PNG 2x**, `data-testid="card-png-2x"`: the same at 2x.

Capture from an `OffscreenCard` copy at full size, not from the scaled preview.

- [ ] **Step 5: Run the tests and look at the cards**

Run: `cd web && npx vitest run --testTimeout=30000 && npm run typecheck && npm run build && git diff --exit-code ../internal/reporting/ui/components.json`
Expected: PASS, and no manifest drift.

Then run the app, using the `twillingate-test-instance` skill or `cd web && npm run dev` against a test instance. Open `/app/gallery/components?view=cards` in light and in dark (Playwright `emulateMedia`). Take a screenshot of every component's card and fix whatever clips, overflows or reads too small. Save the screenshots in `web/test-results/share-cards/` (git-ignored) for the final report.

- [ ] **Step 6: Commit**

```bash
git add web/package.json web/package-lock.json web/src
git commit -m "feat(web): share card with watermark, card mode, capture, gallery share card view"
```

---

### Task 8: Widget menu, Share dialog, Download PNG

**Files:**
- Modify: `web/src/components/WidgetCard.tsx`, `web/src/components/WidgetCard.test.tsx`, `web/src/components/WidgetGrid.tsx`, `web/src/pages/Dashboard.tsx`
- Create: `web/src/components/share/ShareDialog.tsx`, `web/src/components/share/ShareDialog.test.tsx`, `web/src/components/share/ArchiveAfterSelect.tsx`

**Interfaces:**
- Consumes: Tasks 6 and 7.
- Produces:

```ts
// passed Dashboard → WidgetGrid → WidgetCard; undefined in read-only mode or with no project selected
export interface ShareContext { projectId: number; projectName: string; from: string; to: string; writable: boolean }

export function ArchiveAfterSelect(p: { value: ArchiveAfter; onChange(v: ArchiveAfter): void; id?: string; 'aria-label'?: string }): JSX.Element
// ui/native-select with ARCHIVE_AFTER options

export function ShareDialog(p: { open: boolean; onOpenChange(o: boolean): void; widget: Widget; data: unknown; share: ShareContext }): JSX.Element
```

- [ ] **Step 1: Write the failing tests**

`ShareDialog.test.tsx`:
- Mock `@/lib/capture` (`captureCard` resolves two `Blob`s) and spy on `endpoints.createWidgetShare` and `endpoints.widgetShares`.
- Opening shows a preview `img` and **Archive after** defaulting to "1 month", with the note "Feeds keep the preview they already fetched."
- **Create link** sends a `FormData` with `widget_id`, `project_id`, `from`, `to`, `archive_after=30d`, `image` and `image_2x` (assert with `form.get`).
- It then shows the link in a read-only input, **Copy link**, **Copy embed code** and an **Open** link (`target=_blank`, `rel=noopener`).
- With the list returning 2 other shares, it shows "This widget has 2 other links", linking to `/shares?widget=<id>`.

`WidgetCard.test.tsx` additions:
- The "Widget actions" menu button shows **Share…** and **Download PNG** when `share.writable`, and only **Download PNG** when it is false.
- No menu at all when `share` is undefined.
- **Download PNG** calls `captureCard` and `downloadBlob` with `<widget.name>-<from>-<to>.png` and the 2x blob.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd web && npx vitest run src/components/share src/components/WidgetCard.test.tsx --testTimeout=30000`
Expected: FAIL.

- [ ] **Step 3: Implement**

**Dashboard.** In `Dashboard.tsx`, compute `share: ShareContext | undefined`. It comes from the selection `sel`: the project id and the resolved range (`resolve()` from `lib/ranges`, as `useDashboardSelection` does). The project name comes from `useQuery(projectsQuery)`. `writable` is `list.data?.dev !== true`. Pass it to `WidgetGrid` as `share`, and from there to each `WidgetCard`. When no project is selected, `share` is undefined.

**WidgetCard.** In `WidgetFrame`'s `actions`, next to the refresh button, add a `DropdownMenu` (pattern from `DashboardMenu.tsx:99-120`). Its trigger is a ghost icon `Button` with `aria-label="Widget actions"` and a `MoreHorizontalIcon`. The items:
- **Share…** (`Share2Icon`) opens `ShareDialog`, with the dialog state lifted into WidgetCard.
- **Download PNG** (`DownloadIcon`) mounts an `OffscreenCard`. When its `onNode` fires it calls `captureCard`, then `downloadBlob(image2x, …)`, then unmounts it. A failure shows `toast.error`.

Use the widget's current `answer.data`. Disable both items while the data is loading or in error.

**ShareDialog** follows `IssueKeyDialog.tsx`:
- `Dialog` → `DialogContent className="sm:max-w-lg"` → `DialogHeader` ("Share widget" and a description).
- **On open:** mount an `OffscreenCard`, capture it, and show `<img className="w-full rounded-md border" src={objectURL(image2x)}>`.
- **The form:** a `<label>` "Archive after" with `ArchiveAfterSelect`, the note, and a `DialogFooter` with the **Create link** submit button, disabled until the capture is done.
- **On submit:** build the `FormData`, using `new File([image], 'image.png', {type: 'image/png'})` for both images, and call `actions.create`.
- **On success:** swap the form for the result view. It has `<Input readOnly value={share.url}>`, a `CopyButton value={share.url} label="Copy link"`, a `CopyButton value={embedCode(share)} label="Copy embed code"`, and `<Button asChild variant="outline"><a href={share.url} target="_blank" rel="noopener">Open</a></Button>`.
- **Other links:** `useQuery(widgetSharesQuery({ widget_id, state: 'live' }))`. When there are others, show a `Link` to `/shares?widget=<id>`.
- **On close:** reset all state and revoke the object URLs.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run --testTimeout=30000 && npm run typecheck`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): share a widget or download it as a PNG from its menu"
```

---

### Task 9: The Shares page

**Files:**
- Create: `web/src/pages/Shares.tsx`, `web/src/pages/Shares.test.tsx`
- Modify: `web/src/App.tsx` (route), `web/src/components/AppSidebar.tsx:120-134` (sidebar item), `web/src/components/AppSidebar.test.tsx`

**Interfaces:**
- Consumes: `widgetSharesQuery`, `useWidgetShareActions`, `ArchiveAfterSelect`, `archiveLabel`, `rangeInWords`, `embedCode`, `CopyButton`.

- [ ] **Step 1: Write the failing tests**

`Shares.test.tsx`: wrap in `MemoryRouter initialEntries={['/shares']}`, spy on `endpoints.widgetShares` and `endpoints.dashboards`, and `vi.mock` the actions hook.
- Lists two live shares, newest first: thumbnail `img` with `src=image_url` and `loading="lazy"`, title, `dashboard_title` linking to `/dashboards/<dashboard_id>`, project name, range, created date, and the archive select showing "1 month"-style labels with the current value.
- A share with `widget_id: null` shows its title and project name with no dashboard link.
- Changing the select calls `setArchiveAfter(id, '90d')`. **Archive** calls `archive(id)` with no confirmation dialog.
- `?widget=7` calls `endpoints.widgetShares` with `{ widget_id: 7, state: 'live' }` and shows a "Widget 7 ×" chip; clearing it drops the filter.
- The empty state reads "No shared widgets. Share one from a widget's menu: Share…".
- The Range, Created and Archive after header cells have the `hidden sm:table-cell` class, and a `sm:hidden` folded line under the title carries the same values.

`AppSidebar.test.tsx`: a "Shares" link to `/shares` exists when the sidebar is not read-only, and is absent when it is.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd web && npx vitest run src/pages/Shares.test.tsx src/components/AppSidebar.test.tsx --testTimeout=30000`
Expected: FAIL.

- [ ] **Step 3: Implement**

**Page skeleton.** Copy it from `Archive.tsx`: `AppShell`, `TopBar` and `Crumbs [{label:'Shares'}]`, the `max-w-[1600px]` container, and the header with `h1` "Shares" and the description "Links to shared widgets. Anyone with a link sees its image; archive a link to take it down."

**Table.** Use `ui/table`, wrapped in `<div className="min-w-0">`:

| Column | Classes | Content |
| --- | --- | --- |
| Image | `w-28` | thumbnail `<a href={share.url} target=_blank rel=noopener><img className="aspect-[1200/630] w-24 rounded border object-cover" …></a>` |
| Widget | `min-w-0` | title in `<div className="truncate font-medium">`, then `dashboard · project` muted and truncated; the dashboard is a `Link` when it exists; then the `sm:hidden` folded line (range · created · archive label) |
| Range | `hidden sm:table-cell` | `rangeInWords` |
| Created | `hidden sm:table-cell` | formatted date |
| Archive after | `hidden sm:table-cell` | `ArchiveAfterSelect`, showing the label for the current value; for a dated share, a muted `archives Nov 4` under it |
| Actions | `text-right` | `CopyButton` (link), `CopyButton` (embed), `Button variant="outline" size="sm"` **Archive** |

The select's value for an existing dated share: no period matches a date exactly, so the select shows an extra first option, `Nov 4`, standing for the current value. Picking any other option calls `setArchiveAfter`.

**Wiring.**
- Route: `<Route path="/shares" element={<OnlineOnly><Shares/></OnlineOnly>}/>` in `App.tsx`.
- Sidebar: a second `SidebarMenuItem` after Projects with `Share2Icon`, label "Shares", `isActive={pathname.startsWith('/shares')}`, under the same `!readOnly` guard.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run --testTimeout=30000 && npm run typecheck`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): a Shares page for live share links"
```

---

### Task 10: Archived shares on the Archive page

**Files:**
- Modify: `web/src/pages/Archive.tsx`, `web/src/pages/Archive.test.tsx`
- Create: `web/src/components/share/RestoreShareDialog.tsx`

**Interfaces:**
- Consumes: `widgetSharesQuery({ state: 'archived' })`, `useWidgetShareActions().restore`, `purgeDate` (`lib/arrange.ts:97`), `formatPurgeDate` (`lib/time.ts:36`), `ArchiveAfterSelect`.

- [ ] **Step 1: Write the failing tests**

In `Archive.test.tsx`, extend the existing mocks: spy on `endpoints.widgetShares` and mock `useWidgetShareActions`.
- With two archived shares and `purge_after_days: 30`, a "Shares" `h2` section appears below the dashboards. Each row shows its thumbnail, title, project, and `archived · deleted on 4 Nov`, using `archived_at`.
- **Restore** opens a dialog with **Archive after** defaulting to 1 month; confirming calls `restore(id, '30d')`.
- With no archived shares, there is no "Shares" heading.
- The intro paragraph contains "Archived shares answer 404 until restored."
- In read-only mode (`dev: true`), there are no Restore buttons.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd web && npx vitest run src/pages/Archive.test.tsx --testTimeout=30000`
Expected: FAIL.

- [ ] **Step 3: Implement**

**The section.** In `Archive.tsx`, after the dashboards list, render `<section aria-labelledby="archived-shares">` containing `<h2 id="archived-shares" className="text-base font-semibold">Shares</h2>` and a `<ul className="flex flex-col gap-2">`. Each row is a `grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3` with the thumbnail, the truncated text, and the Restore button.

**The status line.** `purgeDate(share.archived_at!, purgeDays)` → `archived · deleted on ${formatPurgeDate(d)}`, or just `archived` when that is undefined.

**The intro.** Append the sentence to the intro `<p>`, and change "Nothing archived." so it shows only when neither dashboards nor shares are archived.

**Restore.** `RestoreShareDialog` uses the same Dialog pattern. Its title is "Restore share", its body is the `ArchiveAfterSelect` (default `30d`), and its footer has the Restore button.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run --testTimeout=30000 && npm run typecheck`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): archived shares on the Archive page, restore with a new date"
```

---

### Task 11: End-to-end tests

**Files:**
- Create: `web/e2e/png.ts`, `web/e2e/share.spec.ts`, `web/e2e/share-cards.spec.ts`
- Modify: `web/e2e/cursor.spec.ts:57`, `web/e2e/phone.spec.ts:72`

**Interfaces:**
- Consumes: everything above, through a running binary (`e2e/serve.sh`, port 18080, token `e2e-token`, `PUBLIC_URL=http://127.0.0.1:18080`).
- Produces (`e2e/png.ts`):

```ts
import { deflateSync, crc32 } from 'node:zlib'
// solidPng(w, h): a valid grey PNG of w×h (8-bit RGB, filter 0 rows), for seeding shares through the API.
export function solidPng(w: number, h: number): Buffer
export function pngSize(b: Buffer): { width: number; height: number } // reads IHDR bytes 16..24
export async function createShare(request: APIRequestContext, o: { widgetId: number; projectId: number; from: string; to: string }): Promise<{ id: string; url: string }>
// multipart POST /api/widget-shares with solidPng(1200,630) and solidPng(2400,1260), Bearer e2e-token
```

If `crc32` isn't exported by the installed Node's zlib, compute CRC-32 by hand (a table of 256 entries).

- [ ] **Step 1: Write the specs**

`share.spec.ts`:
1. Log in (the `login()` helper pattern from `cursor.spec.ts:28-34`) and open `/app/dashboards/1`.
2. Hover the first widget card, open "Widget actions", click **Share…** and wait for the preview `img`. Keep **Archive after** at 1 month and click **Create link**.
3. Read the link from the read-only input. It matches `/^http:\/\/127\.0\.0\.1:18080\/share\/[0-9a-f-]{36}$/`.
4. In a new page with no auth, `goto(link)`. Expect 200 and:
   - `meta[property="og:image"]` content ends with `.png`,
   - `meta[name="twitter:card"]` is `summary_large_image`,
   - the footer link `a[href="https://twillingate.dev"]` has the text `twillingate.dev`,
   - the main `img` has `naturalWidth === 2400`.
5. `request.get(link + '.png')`: `content-type image/png`, `pngSize` 1200×630, body under 300 KB.
6. `/app/shares`: the row with the share's title is visible. Click **Archive**.
7. `request.get(link)` is 404.
8. `/app/archive`: the Shares section shows the title. Click **Restore**, then confirm with the default.
9. `request.get(link)` is 200 again.
10. Download PNG: on the dashboard, use `page.waitForEvent('download')` and click **Download PNG**. The file name matches `/-\d{4}-\d{2}-\d{2}-\d{4}-\d{2}-\d{2}\.png$/` and `pngSize` is 2400×1260.

`share-cards.spec.ts`: for each of `['light','dark']`, call `page.emulateMedia({ colorScheme })` and open `/app/gallery/components?view=cards`. For each component section, click its first card's `card-png-1x` and `card-png-2x` (catching the downloads). Assert `pngSize` is 1200×630 and 2400×1260, and that the 1x file is under 300 * 1024 bytes. Attach the 2x files to the report (`testInfo.attach`) for review.

`cursor.spec.ts:57`: add `'/app/shares'` to the page list.

`phone.spec.ts:72`: add `'/app/shares'`. Seed a share in the spec's existing seeding block with `createShare`, using the long-named project and a widget renamed to a 120-character title (the update widget route). Add the share's public URL to the visited pages so its no-sideways-scroll check covers the share page too.

- [ ] **Step 2: Run them**

Run: `cd web && npm run e2e -- share.spec.ts share-cards.spec.ts cursor.spec.ts phone.spec.ts`
Expected: PASS. If port 18080 is held by another session's stale server, find and stop that process first (see memory: archive-groups-pr).

- [ ] **Step 3: Commit**

```bash
git add web/e2e
git commit -m "test(web): end-to-end share flow, card sizes, cursor and phone coverage"
```

---

### Task 12: Full verification and the PR

**Files:** none new.

- [ ] **Step 1: Run everything**

```bash
export PATH=$PATH:/usr/local/go/bin
make check
cd web && npm run typecheck && npx vitest run --testTimeout=30000 && npm run build && git diff --exit-code ../internal/reporting/ui/components.json && npm run e2e
```

Expected: all PASS, with no components.json drift. Fix anything that fails in the task that owns it, as a new commit.

- [ ] **Step 2: Retitle the PR and update its description**

The PR now ships the feature. Run:

`gh pr edit 137 --title "feat(reporting): share a widget as an image link"`

Then add a "Changes" section to the body:
- what each task delivered,
- the gallery card screenshots (light and dark) from Task 7 and the e2e attachments, uploaded as PR comments or linked,
- the test commands run and their results.

Push with `git push -u origin feat/widget-shares`.
