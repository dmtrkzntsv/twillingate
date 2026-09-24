# The server stores what it is sent — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the project's server-side identity mode so the collector stores `$user_id`, `$user_name` and `$install_id` exactly as a client sends them, runs retention for every project, and logs once per project and id kind when ids first arrive.

**Architecture:** The mode is stripped from its consumers first (ingest, daily pass, private API, snippet) while the field still exists, so the tree compiles after every task; the field, column, constants and CLI flag go last; dashboards and docs close. Migration 017 is one `ALTER TABLE … DROP COLUMN`. Visibility is a `sync.Map` on the ingest `Server` keyed by project and kind.

**Tech Stack:** Go 1.26 (`/usr/local/go/bin/go`, not on PATH), modernc SQLite v1.57, Evidence markdown pages, no SDK change.

**Spec:** `docs/superpowers/specs/2026-09-23-server-stores-what-it-is-sent-design.md`

## Global Constraints

- No validation in the database: SQL is structure only; value work stays in Go.
- Every migration test pins its ceiling with `migrateThrough(ctx, N)`; never call `Migrate` unbounded in a migration test.
- The tag carries exactly eight `data-` attributes (key, identity, auto, mask-url, routing, kind, consent, instance); this plan adds none and removes none. The SDK (`sdk/`) is untouched.
- `internal/archtest` forbids upward or sideways imports; no new packages.
- Refusals are typed (`manage.ErrInvalid` etc.) and matched with `errors.Is`.
- Run Go as `/usr/local/go/bin/go`. Package tests: `/usr/local/go/bin/go test ./internal/<pkg>/`. `make check` takes ~7 minutes and runs once, in Task 5.
- Implementers do not commit; the controller commits after each task's review. Conventional Commits, scope from the tree.
- Docs change in the same PR as the code (`docs/twillingate.md`, `README.md`, `deploy/UPGRADES.md`); `internal/api/docs_sync_test.go` must stay green.
- The identity mode is deleted, not deprecated: no compatibility shim, no hidden default, no `store_ids` switch (declined twice in the spec).

---

### Task 1: Ingest stores what it is sent and logs the first ids

**Files:**
- Modify: `internal/server/handlers.go` (`resolveIdentity`, `identityNames`, the loop in `handleEvents`, the `config` import)
- Modify: `internal/server/server.go` (`Server` gains `idsSeen sync.Map`; new method `noteIDs`)
- Modify: `internal/identity/identity.go` (delete `ActorHash`)
- Modify: `internal/identity/identity_test.go` (delete `TestActorHashIsStableAndSalted`, `TestActorHashSeparatesFields`)
- Modify: `internal/server/server_test.go` (helpers lose the mode; identity-mode tests rewritten)
- Modify: `internal/server/twillingate_script_test.go` (`Identity: "anonymous"` removed from the spec literal)

**Interfaces:**
- Consumes: `identity.VisitorHash(salt, ip, ua, project string) string`, `store.ActorUser/ActorInstall/ActorConnection`, `store.KindUser/KindGroup`.
- Produces: `func resolveIdentity(rv resolved, salt, ip, ua, hashKey string) (actor, actorKind, user, group string)`; `func identityNames(rv resolved) []store.Identity`; `func (s *Server) noteIDs(projectID int64, kind string)`; test helpers `newServer(t) (*fakeQueue, *Server)`, `testServer(t) (*fakeQueue, http.Handler)`, `newTestServer(t) *Server`, `newLoggingServer(t) (*fakeQueue, *Server, *bytes.Buffer)`.

- [ ] **Step 1: Rewrite the test helpers so no test names a mode**

In `internal/server/server_test.go` replace `testServer`, `testServerWithIdentity`, `newServerWithIdentity` and `newTestServer` with:

```go
func testServer(t *testing.T) (*fakeQueue, http.Handler) {
	q, s := newServer(t)
	return q, s
}

// newServer builds a *Server over one project with one key, logging to
// slog.Default(). newLoggingServer is the same with a captured log.
func newServer(t *testing.T) (*fakeQueue, *Server) {
	t.Helper()
	q, s, _ := newServerWithLogger(t, slog.Default())
	return q, s
}

func newLoggingServer(t *testing.T) (*fakeQueue, *Server, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	q, s, _ := newServerWithLogger(t, slog.New(slog.NewTextHandler(&buf, nil)))
	return q, s, &buf
}

func newServerWithLogger(t *testing.T, logger *slog.Logger) (*fakeQueue, *Server, *manage.Registry) {
	t.Helper()
	cfg := configtest.Load(t, nil)
	reg := newTestRegistry(t,
		[]manage.ProjectSpec{{
			Name:           "App",
			AllowedOrigins: []string{testOrigin},
		}},
		map[int][2]string{0: {testKey, "web"}})
	g, _ := geo.New("cloudflare://", t.TempDir(), slog.Default())
	q := &fakeQueue{}
	return q, New(cfg, reg, q, g, fixedSalt{}, q, logger), reg
}

// newTestServer is newServer for a test that only needs the server.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	_, s := newServer(t)
	return s
}
```

Add `"bytes"` to the test file's imports. Replace every `testServerWithIdentity(t, "identified")` in the file with `testServer(t)`. In `internal/server/twillingate_script_test.go` remove `Identity: "anonymous",` from the `manage.ProjectSpec` literal (keep the rest of the literal).

- [ ] **Step 2: Replace the identity-mode tests**

Delete `TestAnonymousModeHashesIdentifiers`, `TestIdentifiedModeStoresRawIdentifiers` and `TestIdentifiedModeFallsBackToInstallThenHash` under `// --- identity modes ---` and put these in their place (rename the section comment to `// --- ids are stored as sent ---`):

```go
func TestIdsAreStoredAsSent(t *testing.T) {
	q, h := testServer(t)
	post(h, `{"key":"`+testKey+`","attributes":{"$user_id":"u1","$group_id":"org9","$user_name":"Ada","$group_name":"Acme","$install_id":"i1"},
	  "events":[{"name":"$screen_view","attributes":{"$screen":"/x"}}]}`, nil)

	if len(q.views) != 1 {
		t.Fatalf("views = %+v", q.views)
	}
	v := q.views[0]
	if v.UserID != "u1" || v.ActorID != "u1" || v.ActorKind != store.ActorUser {
		t.Errorf("view identity = actor %q/%s user %q; want raw u1 as the user actor", v.ActorID, v.ActorKind, v.UserID)
	}
	if v.GroupID != "org9" {
		t.Errorf("group_id = %q; groups are stored raw", v.GroupID)
	}
	if len(q.identities) != 2 {
		t.Fatalf("identities = %+v; want the user and the group name", q.identities)
	}
	var user, group bool
	for _, id := range q.identities {
		switch {
		case id.Kind == store.KindUser && id.ID == "u1" && id.Name == "Ada" && id.ProjectID == 1:
			user = true
		case id.Kind == store.KindGroup && id.ID == "org9" && id.Name == "Acme" && id.ProjectID == 1:
			group = true
		}
	}
	if !user || !group {
		t.Errorf("identities = %+v; want Ada for u1 and Acme for org9", q.identities)
	}
}

func TestInstallIdThenConnectionHash(t *testing.T) {
	q, h := testServer(t)
	post(h, `{"key":"`+testKey+`","attributes":{"$install_id":"i1"},
	  "events":[{"name":"a"}]}`, nil)
	post(h, envelopeOf(`{"name":"b"}`), nil)

	if q.events[0].ActorID != "i1" || q.events[0].ActorKind != store.ActorInstall {
		t.Errorf("actor with install only = %q/%s, want i1/install", q.events[0].ActorID, q.events[0].ActorKind)
	}
	want := identity.VisitorHash("test-salt", "192.0.2.1", chromeUA, "1")
	if q.events[1].ActorID != want || q.events[1].ActorKind != store.ActorConnection {
		t.Errorf("actor with no identifier = %q/%s, want the connection hash %q", q.events[1].ActorID, q.events[1].ActorKind, want)
	}
}

func TestUserNameNeedsAUserId(t *testing.T) {
	q, h := testServer(t)
	post(h, `{"key":"`+testKey+`","attributes":{"$user_name":"Ada","$group_id":"g","$group_name":"G"},
	  "events":[{"name":"a"}]}`, nil)
	if len(q.identities) != 1 || q.identities[0].Kind != store.KindGroup {
		t.Errorf("identities = %+v; a name without an id names nothing", q.identities)
	}
}

func TestFirstIdsAreLoggedOncePerKind(t *testing.T) {
	_, s, buf := newLoggingServer(t)
	userBatch := `{"key":"` + testKey + `","attributes":{"$user_id":"u1"},"events":[{"name":"a"}]}`
	post(s, userBatch, nil)
	post(s, userBatch, nil)
	post(s, `{"key":"`+testKey+`","attributes":{"$install_id":"i1"},"events":[{"name":"b"}]}`, nil)
	post(s, envelopeOf(`{"name":"c"}`), nil)

	out := buf.String()
	if n := strings.Count(out, "project receives ids"); n != 2 {
		t.Fatalf("log has %d 'project receives ids' lines, want 2 (one per kind):\n%s", n, out)
	}
	for _, want := range []string{"project=1 kind=user", "project=1 kind=install"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "kind=connection") {
		t.Errorf("a connection-hash actor must not be logged as an id:\n%s", out)
	}
}
```

Update the comment on `TestHashInputIsTheProjectId` from "an anonymous actor is hashed" to "a connection-hash actor is hashed". `TestActorKindRecorded` and `TestBatchNamesAreDedupedAcrossEvents` keep their bodies; only the helper call changes.

- [ ] **Step 3: Run the server tests to see them fail**

Run: `/usr/local/go/bin/go test ./internal/server/ -run 'TestIdsAreStoredAsSent|TestInstallIdThenConnectionHash|TestUserNameNeedsAUserId|TestFirstIdsAreLoggedOncePerKind' 2>&1 | tail -20`
Expected: compile error — `newServerWithIdentity`/`Identity` references in `server.go`/`handlers.go` still exist and `noteIDs` does not; or, once compiling, `TestIdsAreStoredAsSent` fails because `u1` is hashed.

- [ ] **Step 4: Collapse `resolveIdentity` and `identityNames`**

In `internal/server/handlers.go` replace the whole `resolveIdentity` function and its comment, and `identityNames` and its comment, with:

```go
// resolveIdentity picks the actor for a row: the client's $user_id, else
// its $install_id, else a hash of the connection under the daily salt.
// actorKind records which one it was, so cohorts can be built on it later.
// Ids are stored as sent: the served SDK's identity mode decides what is
// sent (docs/twillingate.md, Identity), and a client that posts by hand
// decides by what it posts.
//
// group_id stays raw: it identifies an organization, not a natural person.
//
// hashKey is the project id as a decimal string — the id never changes, so
// a project's hash input never changes.
func resolveIdentity(rv resolved, salt, ip, ua, hashKey string) (actor, actorKind, user, group string) {
	actor, actorKind = rv.UserID, store.ActorUser
	if actor == "" {
		actor, actorKind = rv.InstallID, store.ActorInstall
	}
	if actor == "" {
		// No client identifier at all: fall back to the rotating hash
		// rather than dropping the event.
		actor, actorKind = identity.VisitorHash(salt, ip, ua, hashKey), store.ActorConnection
	}
	return actor, actorKind, rv.UserID, rv.GroupID
}

// identityNames collects display names to upsert. A name is kept only
// beside the id it names: $user_name without $user_id names nothing.
func identityNames(rv resolved) []store.Identity {
	var out []store.Identity
	if rv.GroupID != "" && rv.GroupName != "" {
		out = append(out, store.Identity{Kind: store.KindGroup, ID: rv.GroupID, Name: rv.GroupName})
	}
	if rv.UserID != "" && rv.UserName != "" {
		out = append(out, store.Identity{Kind: store.KindUser, ID: rv.UserID, Name: rv.UserName})
	}
	return out
}
```

In `handleEvents` change the two call sites to `resolveIdentity(rv, salt, ip, ua, hashKey)` and `identityNames(rv)`. Remove the `"github.com/dmtrkzntsv/twillingate/internal/config"` import from `handlers.go` (it has no other use there; `server.go` keeps its own).

- [ ] **Step 5: Add the one-time log line**

In `internal/server/server.go` add to the `Server` struct, after `counters *keyCounters`:

```go
	// idsSeen holds "<project id>/<actor kind>" for every project and kind
	// that has sent an id since the process started. Nothing on the server
	// decides whether ids are stored, so this is how an operator sees that
	// they are: one Info line per project per kind per process.
	idsSeen sync.Map
```

and this method after `Counters()`:

```go
// noteIDs logs "project receives ids" the first time a batch for the
// project resolves an actor of kind (user or install) in this process.
func (s *Server) noteIDs(projectID int64, kind string) {
	key := strconv.FormatInt(projectID, 10) + "/" + kind
	if _, loaded := s.idsSeen.LoadOrStore(key, struct{}{}); !loaded {
		s.logger.Info("project receives ids", "project", projectID, "kind", kind)
	}
}
```

Add `"strconv"` to `server.go`'s imports. In `handlers.go`, inside the event loop directly after the `resolveIdentity` call, add:

```go
		switch actorKind {
		case store.ActorUser:
			sawUser = true
		case store.ActorInstall:
			sawInstall = true
		}
```

declare `var sawUser, sawInstall bool` beside `var names []store.Identity`, and beside the existing `s.counters.record(label, res.Accepted, res.Rejected)` add:

```go
	if sawUser {
		s.noteIDs(p.ID, store.ActorUser)
	}
	if sawInstall {
		s.noteIDs(p.ID, store.ActorInstall)
	}
```

- [ ] **Step 6: Delete `ActorHash`**

In `internal/identity/identity.go` delete the `ActorHash` function and its comment block. If `crypto/sha256` or `encoding/hex` is then unused in that file, remove the import (check with `go vet`; `VisitorHash` uses both, so they most likely stay). In `internal/identity/identity_test.go` delete `TestActorHashIsStableAndSalted` and `TestActorHashSeparatesFields` (and the comment line above the second).

- [ ] **Step 7: Run the server and identity tests**

Run: `/usr/local/go/bin/go vet ./internal/server/ ./internal/identity/ && /usr/local/go/bin/go test ./internal/server/ ./internal/identity/ 2>&1 | tail -20`
Expected: PASS for both packages. `grep -rn "ActorHash\|testServerWithIdentity\|newServerWithIdentity" internal/` returns nothing.

- [ ] **Step 8: Report**

No commit. The controller commits as `feat(server)!: store ids as sent and log the first ones per project`.

---

### Task 2: The daily pass builds actors and retention for every project

**Files:**
- Modify: `internal/jobs/jobs.go:95-110`
- Modify: `internal/jobs/jobs_test.go` (`identifiedProjectSpecs`, `anonymousProjectSpecs`, `TestRunDailyPassSkipsCohortsForAnonymousProjects`)
- Modify: `internal/jobs/errors_test.go:213-215` (`identifiedJobsSpecs`)

**Interfaces:**
- Consumes: `Store.UpsertActors(ctx, projectID, day)` (keeps only `actor_kind IN ('user','install')`), `Store.AggregateRetentionDay`.
- Produces: nothing new; the `identified` gate is gone.

- [ ] **Step 1: Rewrite the test fixtures and the anonymous test**

In `internal/jobs/jobs_test.go` replace the two spec vars with one:

```go
var appProjectSpecs = []manage.ProjectSpec{
	{Name: "App", AllowedOrigins: []string{"https://a.com"}},
}
```

Replace every `identifiedProjectSpecs` in the file with `appProjectSpecs`, and replace `TestRunDailyPassSkipsCohortsForAnonymousProjects` with:

```go
// A project whose clients send no ids has connection-hash actors only.
// UpsertActors keeps user and install kinds, so it gets no actors and no
// cohorts, while rollups and identity aggregates still run.
func TestRunDailyPassBuildsNoActorsWithoutIds(t *testing.T) {
	st, r, db := setupApp(t, appProjectSpecs)
	ctx := context.Background()
	ts := mustTime("2026-08-10T10:00:00Z")
	if err := st.WriteViews(ctx, []store.View{{
		ID: "vconn", ProjectID: 1, TS: ts, ReceivedAt: ts,
		Kind: "app", ActorID: "hash1", ActorKind: store.ActorConnection,
		GroupID: "org9", Path: "/home", OS: "iOS", AppVersion: "2.4.1",
	}}); err != nil {
		t.Fatal(err)
	}

	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}

	if n := count(t, db, `SELECT COUNT(*) FROM actors`); n != 0 {
		t.Errorf("actors = %d; a connection-hash actor is never cohorted", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_retention`); n != 0 {
		t.Errorf("agg_retention rows = %d, want 0", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_views_daily WHERE kind='app'`); n != 1 {
		t.Errorf("agg_views_daily rows = %d, want 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_identity_daily WHERE kind='group'`); n != 1 {
		t.Errorf("group aggregate rows = %d, want 1", n)
	}
}
```

`TestRunDailyPassAggregatesAppDays` already asserts that a project whose views carry `$user_id` gets two actors and a cohort row; it now runs against `appProjectSpecs`, which is the "every project" half of the spec's test. In `internal/jobs/errors_test.go` change `identifiedJobsSpecs` to `{Name: "App", AllowedOrigins: []string{"https://a.com"}}` (drop `Identity:`); keep the var name. Remove the `config` import from `errors_test.go` if that was its only use.

- [ ] **Step 2: Run the jobs tests to see the gate fail them**

Run: `/usr/local/go/bin/go test ./internal/jobs/ -run 'TestRunDailyPassAggregatesAppDays|TestRunDailyPassBuildsNoActorsWithoutIds' 2>&1 | tail -20`
Expected: `TestRunDailyPassAggregatesAppDays` FAILS with `actors = 0, want 2` (the project defaults to anonymous and the gate skips it).

- [ ] **Step 3: Drop the gate**

In `internal/jobs/jobs.go` replace the block from `// Retention is undefined for anonymous projects` through the closing brace of `if identified { … }` with:

```go
		// Actors and cohorts run for every project. UpsertActors keeps
		// user- and install-identified actors only, so a project whose
		// clients send no ids gets empty cohorts at no cost.
		for _, day := range identityDays {
			if err := r.store.UpsertActors(ctx, id, day); err != nil {
				r.logger.Error("upsert actors failed", "project", id, "day", day.String(), "error", err)
			}
			if err := r.store.AggregateRetentionDay(ctx, id, day); err != nil {
				r.logger.Error("aggregate retention failed", "project", id, "day", day.String(), "error", err)
			}
```

leaving the `if day == today { continue }` and `AggregateIdentityDay` part of the loop as it is. Delete the `p := snap.Project(id)` and `identified := …` lines; `snap` is still used by `snap.AttributesFor(id)`. The `config` import stays (`cfg *config.Config`).

- [ ] **Step 4: Run the jobs tests**

Run: `/usr/local/go/bin/go vet ./internal/jobs/ && /usr/local/go/bin/go test ./internal/jobs/ 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Report**

No commit. The controller commits as `feat(jobs): build actors and retention for every project`.

---

### Task 3: The private API and the snippet lose the mode

**Files:**
- Modify: `internal/manage/ops.go` (`Snippet` signature) and `internal/manage/ops_test.go:172-180`, `internal/manage/coverage_test.go:212-218` (the two `Snippet` tests)
- Modify: `cmd/twillingate/key.go:56-57`, `cmd/twillingate/keygen.go:75-77`, `cmd/twillingate/commands_test.go:154`
- Modify: `internal/api/ops_manage.go`, `internal/api/ops_read.go`, `internal/api/ops_product.go`, `internal/api/resources.go`, `internal/api/guide.go`
- Modify tests: `internal/api/seed_test.go`, `readdb_test.go`, `ops_read_test.go`, `ops_manage_test.go`, `ops_product_test.go`, `guide_test.go`, `resources_test.go`, `coverage_test.go`, `rest_test.go`

**Interfaces:**
- Consumes: `manage.Project` still has `Identity` (removed in Task 4); nothing in `internal/api` or `cmd` may read it after this task.
- Produces: `manage.Snippet(base, key string) string`; `createProjectIn`/`updateProjectIn`/`projectToolOut`/`projectOut`/`pj` without `Identity`.

- [ ] **Step 1: Write the failing API tests**

`internal/api/seed_test.go`: the seeding loop becomes

```go
	for _, name := range []string{"blog", "docs"} {
		if _, err := st.CreateProject(ctx, store.RegistryProject{
			Name: name, AllowedOrigins: "[]", Attributes: "[]"},
			store.AuditEntry{Actor: "test", Action: "project.create"}); err != nil {
			t.Fatal(err)
		}
	}
```

and the helper comment says "blog (id 1) then docs (id 2)". `readdb_test.go:25`: drop `Identity: "identified",`.

`ops_read_test.go` `TestListProjects`: the wanted strings become `` `"project_id":1`, "blog", `"project_id":2`, "docs", "https://blog.example.com" `` and add after the loop:

```go
	if strings.Contains(out, `"identity"`) {
		t.Errorf("list_projects still carries an identity field: %s", out)
	}
```

`ops_manage_test.go` `TestCreateProjectToolReturnsSnippet`: the wanted list becomes `"twillingate.js", "ak_", "data-key"` and add `if strings.Contains(out, "data-identity") { t.Errorf("snippet still prints data-identity: %s", out) }`. In the merge test (around line 170) the `row` struct becomes `ProjectID int64; Name string; AllowedOrigins []string` (json tags `project_id`, `name`, `allowed_origins`); replace the `blog.Identity != "identified"` check with `if len(blog.AllowedOrigins) != 1 || blog.AllowedOrigins[0] != "https://blog.example.com" { t.Errorf("blog origins not preserved by merge, got %v", blog.AllowedOrigins) }` and reword its comment ("…a blind overwrite that would have reset the origins to nil and the name to """).

`ops_product_test.go`: replace `TestRetentionOnAnonymousProjectExplains` with

```go
// docs (project 2) has no cohorts because its clients send no ids: the
// answer is an empty table, not a refusal.
func TestRetentionWithoutIdsIsEmpty(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "retention", map[string]any{
		"project_id": 2, "actor": "user", "from": "2026-07-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("retention on a project without ids must not error: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, `"rows":[]`) && !strings.Contains(out, `"rows":null`) {
		t.Errorf("want an empty rows array, got %s", out)
	}
}
```

(read `tableOut` in `ops_read.go`/`ops_product.go` and adjust the empty-rows spelling to what the encoder emits; the assertion must be on the JSON, not on `len`).

`guide_test.go`: rename the first test `TestIntegrationGuideWebUsesCollectorURL`; the heading want becomes `"# Integrating blog (project 1; web)"`; the guidance wants become `` `data-identity="identified"`, "twillingate.identify", "twillingate.reset", "optOut", "keyed by the build rather than counted as web" `` and add `for _, gone := range []string{"IDENTIFIED", "ANONYMOUS", "identity="} { if strings.Contains(out, gone) { t.Errorf("guide still speaks of a project mode %q: %s", gone, out) } }`. Rename `TestIntegrationGuideAnonymousAndPlatforms` to `TestIntegrationGuidePlatforms` and add to its mobile check: `|| strings.Contains(out, "anonymous identity")` → error "mobile guide still describes salting".

`resources_test.go`: in the `schema://views` wants replace `"identified"` with `"$user_id"`; after the `schema://projects` read add `if strings.Contains(pres.Contents[0].Text, "Identity") || strings.Contains(pres.Contents[0].Text, "identity") { t.Errorf("schema://projects still lists identity: %s", pres.Contents[0].Text) }`.

`coverage_test.go` `TestCreateProjectToolValidationError` becomes:

```go
// identity was a project field until migration 017; sending it now is an
// unknown field, refused like any other.
func TestCreateProjectToolRejectsIdentity(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "create_project", map[string]any{
		"name": "bad", "identity": "identified"})
	if !res.IsError {
		t.Fatal("create_project accepted the removed identity field")
	}
	res = callTool(t, cs, "update_project", map[string]any{
		"project_id": 1, "identity": "identified"})
	if !res.IsError {
		t.Fatal("update_project accepted the removed identity field")
	}
}
```

`rest_test.go:258`: the body becomes `{"name":"bad","identity":"identified"}` and the message `"removed identity field = %d %s"`; keep the 400 expectation (`rest.go` uses `DisallowUnknownFields`).

- [ ] **Step 2: Run the API tests to see them fail**

Run: `/usr/local/go/bin/go test ./internal/api/ 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -20`
Expected: FAIL on `TestListProjects`, `TestCreateProjectToolReturnsSnippet`, `TestRetentionWithoutIdsIsEmpty`, `TestIntegrationGuideWebUsesCollectorURL`, `TestIntegrationGuidePlatforms`, `TestSchemaResources…`, `TestCreateProjectToolRejectsIdentity` (the MCP half may pass or fail — see Step 7).

- [ ] **Step 3: `Snippet` takes two arguments**

In `internal/manage/ops.go` replace `Snippet`:

```go
// Snippet renders the paste-ready embed tag returned by create_project,
// issue_ingest_key and `twillingate key issue`. base is the COLLECTOR's
// public URL (twillingate.js and /ingest/events live there) — never the
// customer's site origin. The tag is anonymous by default; a signed-in app
// adds data-identity="identified" itself (docs/twillingate.md, Identity).
func Snippet(base, key string) string {
	if base == "" {
		base = SnippetPlaceholderBase
	}
	return fmt.Sprintf(`<script defer src="%s/js/twillingate.js"
        data-key=%q></script>`, base, key)
}
```

`internal/manage/ops_test.go` (around line 174): `snip := Snippet("https://blog.example.com", "ak_x")`, wants `"twillingate.js", `+"`"+`data-key="ak_x"`+"`"; add `if strings.Contains(snip, "data-identity") { t.Error("snippet must not print data-identity") }`. `internal/manage/coverage_test.go` (around line 214): `Snippet("", "ak_x")` and keep its placeholder assertion.

`cmd/twillingate/key.go:57`: `manage.Snippet(cfg.PublicURL, key)`. `cmd/twillingate/keygen.go`: the snippet block becomes

```go
	fmt.Fprintln(stdout, `  <script defer src="https://twillingate.example.com/js/twillingate.js"`)
	fmt.Fprintf(stdout, "          data-key=%q></script>\n", keys[0])
```

`cmd/twillingate/commands_test.go:154`: `if !strings.Contains(s, "data-key") || strings.Contains(s, "data-identity")` with message "output should include a ready-to-paste snippet without data-identity".

- [ ] **Step 4: Management tools**

`internal/api/ops_manage.go`: delete the `Identity` field from `createProjectIn`, `updateProjectIn` and `projectToolOut`; `createProject` builds `manage.ProjectSpec{Name: in.Name, AllowedOrigins: in.AllowedOrigins, Attributes: in.Attributes}` and returns `projectToolOut{ProjectID: p.ID, Key: key}`; `updateProject` passes `manage.ProjectSpec{ID: in.ProjectID, Name: in.Name, AllowedOrigins: in.AllowedOrigins, Attributes: in.Attributes}` and returns `projectToolOut{ProjectID: p.ID}`; every `manage.Snippet(h.publicURL, key, p.Identity)` becomes `manage.Snippet(h.publicURL, key)`.

`internal/api/ops_read.go`: delete `Identity` from `projectOut` and from the literal in `listProjects`. In `register` the descriptions become:

- `list_projects`: `"List projects with id, name and data coverage. Call this first: every other tool takes a project_id from here."`
- `retention`: `"D1/D7/D30-style cohort curves, cohorted by how the actor was identified: actor=user or actor=install. Returns aggregated_through: cohorts after it are absent (refreshed 03:00 UTC), not zero. Empty for a project whose clients send neither $user_id nor $install_id."`
- `identities`: `"Per-user or per-group activity with display names. This surfaces personal data on projects whose clients send ids."`
- `create_project`: `"Create a project and (by default) its first ingest key; returns a paste-ready embed snippet (confirm the collector hostname with the user). Set skip_key to suppress the key."` (unchanged text; verify it carries no "privacy-significant").
- `update_project`: `"Update a project's name, allowed origins and/or declared product-event attributes (breakdown keys for flat-view columns and attribute rollups). Fields you omit are left unchanged; allowed_origins and attributes replace the whole list when given, and an explicit empty allowed_origins clears it."`

`internal/api/ops_product.go`: delete the `if p.Identity != "identified" { … }` block in `retention`. Keep the `p == nil` unknown-project check.

- [ ] **Step 5: Resources**

`internal/api/resources.go`: caveat 3 of `schemaViews` becomes

```
3. EXCEPTION: v_retention has no live half. It refreshes at the 03:00 UTC
   daily pass; cohort days after that are ABSENT, not zero. It holds cohorts
   only for actors identified by $user_id or $install_id; a project whose
   clients send neither has none.
```

The `schema://projects` resource description becomes `"Current projects and their settings."`; `pj` becomes

```go
		type pj struct {
			ProjectID      int64 `json:"project_id"`
			Name           string
			Archived       bool     `json:",omitempty"`
			AllowedOrigins []string `json:"allowed_origins"`
		}
```

with the literal `pj{p.ID, p.Name, p.Archived, p.AllowedOrigins}`.

- [ ] **Step 6: The guide**

`internal/api/guide.go`: the file comment "keys, identity mode, aggregation, PUBLIC_URL" becomes "keys, attributes, PUBLIC_URL". The header line becomes

```go
	fmt.Fprintf(&b, "# Integrating %s (project %d; %s)\n%s%s%s\n", p.Name, p.ID, in.Platform, baseNote, hostNote, keyLine)
```

The web/spa case: the snippet call becomes `manage.Snippet(h.publicURL, key)`; the sentence `". Nothing is filtered on the client: a localhost page reports too, so keep development traffic out by not loading the tag there.\n\n"` becomes `". Nothing is filtered on the client: a localhost page reports too, so keep development traffic out with init({ optOut: () => location.hostname === \"localhost\" }).\n\n"`; the whole `if p.Identity == config.IdentityIdentified { … } else { … }` becomes one write:

```go
		b.WriteString("The tag decides what is sent. The printed tag is anonymous: it sends no\n$user_id, $user_name or $install_id, and identify() is inert. For a\nsigned-in app add data-identity=\"identified\" (or identity: \"identified\" in\ncode), call twillingate.identify(userId, userName) and group(groupId) after\nlogin and twillingate.reset() on logout; ids are then stored as sent.\nNothing is kept on the device unless the tag declares consent.\n\n")
```

The mobile case: `"- $install_id: generate once per install, store locally, send on every\n  batch. Under anonymous identity it is salted and rotated daily.\n"` becomes `"- $install_id: generate once per install, store locally, send on every\n  batch. It is stored as sent and is what install cohorts are built on.\n"`. Remove the `config` import from `guide.go`.

- [ ] **Step 7: Run the API, manage and cmd tests**

Run: `/usr/local/go/bin/go vet ./internal/api/ ./internal/manage/ ./cmd/... && /usr/local/go/bin/go test ./internal/api/ ./internal/manage/ ./cmd/... 2>&1 | tail -20`
Expected: PASS. If the MCP half of `TestCreateProjectToolRejectsIdentity` fails because the go-sdk accepts unknown properties, report it as a concern with the observed behaviour and leave the REST half in place; do not add a manual unknown-field check.

`grep -rn "Identity\|identity" internal/api/*.go cmd/twillingate/*.go | grep -v "_test.go" | grep -v "identities\|Identities\|identity_daily\|v_identity\|identity provider\|identity\.\|internal/identity"` must return nothing.

- [ ] **Step 8: Report**

No commit. The controller commits as `feat(api)!: drop the identity mode from the tools, the snippet and the guide`.

---

### Task 4: Migration 017, the registry, the CLI and the constants

**Files:**
- Create: `internal/store/sqlite/migrations/017_drop_identity.sql`, `internal/store/sqlite/migration017_test.go`
- Modify: `internal/store/store.go:76-82`, `internal/store/sqlite/registry.go` (`LoadRegistry`, `insertProject`, `UpdateProject`)
- Modify: `internal/manage/registry.go:20-26,86`, `internal/manage/ops.go` (`written`, `ProjectSpec`, `validate`, `row`, `UpdateProject`)
- Modify: `internal/config/config.go:16-22`
- Modify: `cmd/twillingate/project.go` (create/update flags, usage lines, list format)
- Modify: `scripts/smoke.sh:26`
- Modify: `deploy/UPGRADES.md` (new section at the end)
- Modify tests: `internal/store/sqlite/sqlite_test.go:126`, `registry_test.go:236-245,258`, and every `Identity: "…"` literal in `internal/store/sqlite/*_test.go`, `internal/manage/*_test.go`, `cmd/twillingate/project_test.go`; delete `TestValidateDefaultsIdentity`, the two "bad identity" cases in `internal/manage/typed_errors_test.go`, and the identity half of `TestUpdateProjectAppliesIdentityAndAttributes`.

**Interfaces:**
- Consumes: nothing outside `manage`, `store`, `config`, `cmd` reads `Identity` any more (Tasks 1–3).
- Produces: `store.RegistryProject{ID, Name, AllowedOrigins, Attributes, Archived}`, `manage.Project{ID, Name, AllowedOrigins, Attributes, Archived}`, `manage.ProjectSpec{ID, Name, AllowedOrigins, Attributes}`; `config.IdentityAnonymous`/`IdentityIdentified` no longer exist.

- [ ] **Step 1: Write the migration test**

Create `internal/store/sqlite/migration017_test.go`:

```go
package sqlite

import (
	"context"
	"testing"
)

// 017 drops projects.identity. The row's other fields survive, and a
// fresh database never has the column.
func TestMigration017DropsIdentity(t *testing.T) {
	db := newTestDBAt(t, 16)
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO projects (id, name, identity, allowed_origins, attributes)
		 VALUES (1, 'Blog', 'identified', '["https://blog.example.com"]', '["plan"]')`); err != nil {
		t.Fatal(err)
	}
	if !hasColumn(t, db, "projects", "identity") {
		t.Fatal("schema 016 must still have projects.identity")
	}
	if err := db.migrateThrough(ctx, 17); err != nil {
		t.Fatalf("migration 017: %v", err)
	}
	if hasColumn(t, db, "projects", "identity") {
		t.Fatal("projects.identity survived migration 017")
	}
	ps, _, err := db.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].ID != 1 || ps[0].Name != "Blog" ||
		ps[0].AllowedOrigins != `["https://blog.example.com"]` || ps[0].Attributes != `["plan"]` {
		t.Fatalf("registry after 017 = %+v", ps)
	}
}

func TestFreshDatabaseHasNoIdentityColumn(t *testing.T) {
	db := newTestDB(t)
	if hasColumn(t, db, "projects", "identity") {
		t.Fatal("a fresh database has projects.identity")
	}
}
```

In `internal/store/sqlite/sqlite_test.go` remove `{"projects", "identity"},` from the column list.

- [ ] **Step 2: Run it to see it fail**

Run: `/usr/local/go/bin/go test ./internal/store/sqlite/ -run 'TestMigration017|TestFreshDatabaseHasNoIdentityColumn' 2>&1 | tail -10`
Expected: FAIL — `projects.identity survived migration 017` (no 017 file yet; `migrateThrough(17)` stops at 16).

- [ ] **Step 3: Write the migration**

Create `internal/store/sqlite/migrations/017_drop_identity.sql`:

```sql
-- The project's identity mode is gone: the collector stores what a client
-- sends, and the served SDK's identity mode decides what is sent
-- (docs/twillingate.md, Identity). Data already stored is untouched: ids
-- hashed under the old anonymous mode stay hashed, and cannot be linked
-- to anything the client sends from now on.
ALTER TABLE projects DROP COLUMN identity;
```

- [ ] **Step 4: Strip the field from the store and the registry**

`internal/store/store.go`: `RegistryProject` becomes `ID int64; Name string; AllowedOrigins string; Attributes string; Archived bool` (keep the comments). `internal/store/sqlite/registry.go`: `LoadRegistry` selects `id, name, allowed_origins, attributes, archived_at IS NOT NULL` and scans `&p.ID, &p.Name, &p.AllowedOrigins, &p.Attributes, &p.Archived`; `insertProject` inserts `(name, allowed_origins, attributes) VALUES (?,?,?)` with `p.Name, p.AllowedOrigins, p.Attributes`; `UpdateProject` sets `name=?, allowed_origins=?, attributes=? WHERE id=?` with `p.Name, p.AllowedOrigins, p.Attributes, p.ID`.

`internal/manage/registry.go`: `Project` becomes `ID int64; Name string; AllowedOrigins []string; Attributes []string; Archived bool`; the `Reload` literal becomes `&Project{ID: rp.ID, Name: rp.Name, Archived: rp.Archived}`.

`internal/manage/ops.go`: `written` builds `&Project{ID: spec.ID, Name: spec.Name, AllowedOrigins: spec.AllowedOrigins, Attributes: spec.Attributes}`; `ProjectSpec` becomes `ID int64; Name string; AllowedOrigins []string; Attributes []string` and its comment drops "or Identity"; `validate` loses the default and the `switch` (keep the name and origins checks); `row` builds `store.RegistryProject{ID: sp.ID, Name: sp.Name, AllowedOrigins: …, Attributes: …}`; `UpdateProject` loses the `if spec.Identity == "" { spec.Identity = cur.Identity }` block. Remove the `config` import from `ops.go` if nothing else uses it.

`internal/config/config.go`: delete the `Identity modes` comment and the two constants.

- [ ] **Step 5: The CLI**

`cmd/twillingate/project.go`: in `create` delete the `identity` flag and pass `manage.ProjectSpec{Name: *name, AllowedOrigins: origins, Attributes: attrs}`; the usage line becomes `"usage: twillingate project create -name <name> [-origin ...] [-attr ...]"`. In `update` delete the flag, build `manage.ProjectSpec{ID: *id, Name: *name, AllowedOrigins: origins, Attributes: attrs}`, usage `"usage: twillingate project update -id <id> [-name ...] [-origin ... | -clear-origins] [-attr ...]"`. In `list` print `fmt.Fprintf(stdout, "%d\t%s%s\n", p.ID, p.Name, state)`. Check `projectUsage` (the const near the top of the file) for a `-identity` mention and remove it.

`scripts/smoke.sh:26`: `./twillingate project create -name "Smoke" \`.

- [ ] **Step 6: Sweep the remaining test literals**

Mechanical removals, then fix by hand what the compiler reports:

```bash
cd internal && for f in $(grep -rl 'Identity: "' --include='*_test.go' store manage) ../cmd/twillingate/project_test.go; do
  sed -i -E 's/, Identity: "(anonymous|identified)"//g; s/Identity: "(anonymous|identified)", //g; s/Identity: "(anonymous|identified)",\s*$//g' "$f"; done
```

Then by hand:

- `internal/store/sqlite/registry_test.go` (around 236): read only `allowed_origins` (`var allowedOrigins string`, `SELECT allowed_origins FROM projects WHERE name='Old Project'`), delete the identity assertion; (around 258) `INSERT INTO projects (name, allowed_origins) VALUES ('Blog','[]')`; the checks `ps[0].Identity != "anonymous"` (44), `got.Identity != "identified"` (89) and the `Identity: "identified",` in the update literal (74) are deleted. Lines 296 and 344 insert into a pre-014 `projects` with `alias` and `identity` columns — those build old schemas and stay.
- `internal/manage/ops_test.go:52`: `ops.CreateProject(ctx, "test", ProjectSpec{})` (the empty-name refusal); line 60 check becomes `p.ID != 1 || p.Name != "My App"` with message "want id 1 and the name"; line 88 drops `u.Identity != "identified" ||`; line 35 `{Name: "x", Identity: "sometimes"}` case: delete that case (its table tests invalid specs; keep the others).
- `internal/manage/coverage_test.go`: delete `TestValidateDefaultsIdentity`; rename `TestUpdateProjectAppliesIdentityAndAttributes` → `TestUpdateProjectAppliesAttributes`, its spec becomes `{ID: p.ID, Attributes: []string{"plan"}}`, the check `u.Name != "b" || len(u.Attributes) != 1 || u.Attributes[0] != "plan"`, and delete the "invalid identity is rejected" block; `sp2` (around 195) loses `Identity: "identified",`; `after_write_test.go:53` drops `p.Identity != "anonymous" ||`; `registry_test.go:74` becomes `p == nil || p.Name != name` (check what `seedProject` names it) and the `seedProject` comment says "creates a project called name".
- `internal/manage/typed_errors_test.go`: delete the `"bad identity"` and `"update to bad identity"` cases.
- `cmd/twillingate/project_test.go:125-152`: the create args drop `"-identity", "identified",`; the survival check becomes `p.Name != "Updated" || len(p.AllowedOrigins) != 1`; delete the "Now explicitly change identity" block; add

```go
	// The flag is gone: the parser refuses it.
	out.Reset()
	if code := run([]string{"project", "update", "-id", "1", "-identity", "anonymous"}, &out); code != 2 {
		t.Fatalf("-identity accepted: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "list"}, &out); code != 0 || !strings.Contains(out.String(), "1\tUpdated") {
		t.Fatalf("project list = exit %d: %q, want `1\\tUpdated`", code, out.String())
	}
```

(check that `run` returns 2 on a flag-parse error — `sf.Parse` returning an error yields `return 2` in `project.go`).

- [ ] **Step 7: Build and test every package**

Run: `/usr/local/go/bin/go vet ./... && /usr/local/go/bin/go test ./internal/store/... ./internal/manage/ ./internal/config/ ./cmd/... ./internal/server/ ./internal/jobs/ ./internal/api/ 2>&1 | tail -30`
Expected: PASS everywhere. `grep -rn "IdentityAnonymous\|IdentityIdentified\|\.Identity\b\|Identity:" --include='*.go' . | grep -v "internal/identity/"` returns nothing.

- [ ] **Step 8: The upgrade runbook**

Append to `deploy/UPGRADES.md` (newest last):

```markdown
### Upgrading to storing what is sent (migration 017)

The project's identity mode is gone. The collector stores `$user_id`,
`$user_name` and `$install_id` exactly as a client sends them, for every
project; what is sent is decided by the tag (`data-identity`, anonymous by
default) or by whatever a hand-written client posts. The migration is one
`ALTER TABLE … DROP COLUMN`; no value is rewritten.

**Upgrade at least a day after the release that carries the SDK factory
(#48) has been on this collector.** The served SDK is cached for a day; a
page still running the previous SDK sends ids from `identify()` even under
an anonymous tag, and from upgrade day those would be stored raw.

There is no query to run. The check is a question: for every project that
was `anonymous`, confirm no client posts `$user_id` or `$install_id` by
hand, because from upgrade day they are stored as sent. Before the upgrade
`twillingate project list` still shows the mode column, which is how to
find those projects.

What changes on the day:

- The mode column is gone; `list_projects`, `schema://projects` and
  `twillingate project list` stop showing it, and a `create_project` or
  `update_project` call that still sends `identity` is refused as an
  unknown field.
- Retention appears for any project whose clients send `$user_id` or
  `$install_id`, and is empty for the rest.
- Ids hashed before the upgrade stay hashed and never link to ids received
  after it. There is nothing to backfill.
- The collector logs `project receives ids` (with the project id and the
  kind, `user` or `install`) once per project and kind per process, the
  first time a batch carries one. Watch for it after the upgrade on a
  project that should send none.

There is no down migration. The previous binary reads a column that no
longer exists and refuses to start against the upgraded file.
```

- [ ] **Step 9: Report**

No commit. The controller commits as `feat(store)!: drop the project identity mode (migration 017)`.

---

### Task 5: Dashboards, docs and README

**Files:**
- Modify: `evidence/sources/twillingate/projects.sql`, `evidence/pages/index.md`, `evidence/pages/users/[project].md`, `evidence/pages/retention/[project].md`, `evidence/pages/groups/[project].md`
- Modify: `docs/twillingate.md` (intro, project table and CLI sample, fields table, snippet, Identity section, tool table, HTTP API table, Writing SQL caveat)
- Modify: `README.md` "Privacy and GDPR"
- Modify: `docs/plausible/README.md:91-94`
- Modify: `docs/deployment.md:182-184`
- Verify: `internal/api/docs_sync_test.go` unchanged and green

**Interfaces:**
- Consumes: `projects` no longer has `identity` (Task 4); `retention` tool never refuses (Task 3).
- Produces: nothing programmatic.

- [ ] **Step 1: Evidence**

`evidence/sources/twillingate/projects.sql`: delete the paragraph beginning `-- identity drives the users and retention pages` (three lines) and make the query

```sql
select id, name,
       case when archived_at is null then 0 else 1 end as archived
from projects
union all
select 0, '', 0 where not exists (select 1 from projects)
```

`evidence/pages/index.md`: the query selects `id, name`; the list line becomes `- **{p.name}** — [views](/views/{p.id}) · …` (drop ` ({p.identity})`).

`evidence/pages/users/[project].md`: delete the `users_mode` query block, the `{#if users_mode[0].identity === 'identified'}` line, and everything from `{:else}` to `{/if}` at the end of the file; directly under the title add

```markdown
Per-user rows appear once this project's clients send a `$user_id`: a tag
with `data-identity="identified"` after `identify()`, or a backend that posts
one. A project whose clients send none has nothing here; group reporting
does not need ids, see [Groups](/groups/{params.project}).
```

`evidence/pages/retention/[project].md`: same surgery (`retention_mode` query, the `{#if …}`, `{:else}` … `{/if}`); under the title add

```markdown
Cohorts exist for actors identified by a `$user_id` or a stable
`$install_id`. A project whose clients send neither has none: the tables
below stay empty until they do.
```

`evidence/pages/groups/[project].md`: the paragraph "Groups work in both identity modes …" becomes "Groups need no user ids. `group_id` identifies an organization rather than a natural person and is stored as sent, so this page fills for any client that sends `$group_id`."

Check with `grep -rn "identity" evidence/ | grep -v "v_identity_daily\|identities"` — must return nothing.

- [ ] **Step 2: docs/twillingate.md**

Edit these passages (line numbers as of `770b9b3`; find them by text):

1. Intro (line 26–28): `It is cookieless by default: an `anonymous` project writes nothing to a visitor's device unless the tag declares consent, and then only its retry queue, never an identifier. Identifiers are salted with a key that rotates at midnight.` → `It is cookieless by default: the served SDK's `anonymous` default writes nothing to a visitor's device unless the tag declares consent, and then only its retry queue, never an identifier. The collector stores the identifiers it is sent; a visitor who sends none is a hash of the connection under a key that rotates at midnight.`
2. Operation table, `create_project` row: `{name, allowed_origins, attributes}`. `integration_guide` row: `with the live key filled in`.
3. CLI sample: `twillingate project create -name "My App" \` (drop `-identity anonymous`) and `twillingate project list                                 # id  name`.
4. Fields table: delete the `identity` row.
5. Snippet mode sample: drop the `data-identity="anonymous"` line so the tag ends `data-key="ak_9f3c…"></script>`.
6. `### Identity` section: replace the table and the paragraph with

```markdown
| The tag says | `$user_id`, `$install_id`, `$user_name` | `$group_id`, `$group_name` |
| --- | --- | --- |
| `anonymous` (default) | never sent | sent, stored raw |
| `identified` | sent, stored as given | sent, stored raw |
| a client posting by hand | stored as given, whatever it sends | stored raw |

The actor resolves as `$user_id` → `$install_id` → a server-side hash of the
connection; how it was identified is recorded alongside it (`user`, `install` or
`connection`) and is what retention cohorts on, so only user- and
install-identified actors are tracked. Send `$install_id` only if it survives a
page load or app restart: an id minted per load makes every load a new actor
that never returns, inflating active counts and dragging retention toward zero,
so leave it out — the connection hash then gives one actor per device per day —
and read retention from the `user` cohort. `$group_id` is stored raw because it
identifies an organization, not a natural person; single-person groups are
personal data. `$user_name` is kept only beside a `$user_id`. **The collector
stores what it is sent.** What reaches it is decided by the tag's
`data-identity` (or `identity` in code), so a project's privacy posture is the
posture of its clients. The collector logs `project receives ids` the first
time a project sends a `$user_id` or `$install_id` (once per kind per process),
which is how to confirm a marketing site's tag sends nothing.
```

7. Tool table: `list_projects` → `Every project with its `project_id`, name and data coverage. Call this first — every other tool needs a `project_id``; `retention` → append ` Empty for a project whose clients send neither `$user_id` nor `$install_id``; `identities` → `Per-user or per-group activity with display names. **Surfaces personal data on projects whose clients send ids**`.
8. HTTP API table, `POST /api/projects` row: `body: `name`, `allowed_origins`, `attributes`, `skip_key` → 201`.
9. Writing SQL caveat 3: `It is populated only for projects with `identity=identified`.` → `It holds cohorts only for actors identified by `$user_id` or `$install_id`; a project whose clients send neither has none.`

Then `grep -n "identity=\|identity mode\|anonymous project\|both modes\|project's mode" docs/twillingate.md` must return nothing; `data-identity` and the `identity` init option stay (`docs_sync` binds `data-identity`).

- [ ] **Step 3: README, plausible README, deployment note**

`README.md` "Privacy and GDPR" becomes:

```markdown
## Privacy and GDPR

**What the collector stores.** What it is sent. The served SDK's `anonymous`
default sends nothing identifying: no cookies, nothing on the device unless
the tag declares consent (`data-consent`), and then only its failed-batch
retry queue — no consent banner is needed for pageview tracking on its own.
A tag with `data-identity="identified"` sends `$user_id`, `$user_name` and
`$install_id`, and they are stored as given; `$group_id`/`$group_name` are
stored raw in every case (a group is an organization, not a person). A
visitor who sends no id is a hash of the connection under a key that rotates
every 24 hours; the previous key is overwritten, so linking across days is
impossible rather than prohibited. IPs and full User-Agents are never stored
or logged (a test scans the database file and the log output). Query strings
are stripped to a UTM allowlist, referrers reduced to a source name, bots
dropped at ingestion. Paths are stored verbatim — strip personal data from
URL schemes before it reaches the tracker.

**What the SDK keeps on the device.** Nothing without consent. With consent
(`data-consent="true"`, or the name of a global the consent manager
maintains) it persists the retry queue and, on an identified tag, the
visitor id, user and group in `localStorage` — terminal-equipment storage
under ePrivacy, the same legal category as a cookie. See
[docs/twillingate.md](docs/twillingate.md) "Consent and storage".

**Access and erasure.** Enabling the API exposes every stored id to every
valid token holder, over MCP and the REST routes alike; complete erasure is
`twillingate project delete`, deliberately CLI-only.
```

`docs/plausible/README.md` "Per-day uniques only." paragraph: `In anonymous mode the actor id is a salted hash that rotates daily.` → `The shim sends no identifiers, so the actor id is a hash of the connection that rotates daily.`; `so a multi-day funnel needs identified mode.` → `so a multi-day funnel needs a client that sends a `$user_id`.`

`docs/deployment.md` (around line 183): `personal data on `identified` projects` → `personal data on projects whose clients send ids`.

- [ ] **Step 4: Run the docs binding and the full check**

Run: `/usr/local/go/bin/go test ./internal/api/ -run 'TestDoc|TestDocs|TestSchema' 2>&1 | tail -10`
Expected: PASS.

Run: `make check 2>&1 | tail -30` (about 7 minutes)
Expected: PASS, including `internal/archtest`, coverage and the restore test.

- [ ] **Step 5: Report**

No commit. The controller commits as `docs: describe a collector that stores what it is sent`.

---

## Self-review

- **Spec coverage:** storage/migration → Task 4; ingest `resolveIdentity`/`identityNames`/`ActorHash` → Task 1; visibility log → Task 1; daily pass → Task 2; private API (`ops_manage`, `ops_read`, `ops_product`, `resources`, `rest` bodies, `guide`) → Task 3; registry, snippet, CLI, `keygen` → Tasks 3 and 4; documentation (`twillingate.md`, `README.md`, `UPGRADES.md`) → Tasks 4 and 5; every listed test → Tasks 1–4; `docs_sync` unchanged → Task 5. Evidence dashboards are not in the spec but read `projects.identity` and would fail to build after 017 → Task 5 (a spec gap, closed here).
- **Placeholder scan:** none; every edit names its text.
- **Type consistency:** `Snippet(base, key)` in Task 3 is what Task 3's `key.go`, `keygen.go`, `guide.go` and `ops_manage.go` call; `ProjectSpec` without `Identity` (Task 4) is what Task 3's API already builds and what Tasks 1–2's test fixtures already use; `noteIDs(projectID int64, kind string)` takes the `store.ActorUser`/`ActorInstall` strings the loop already has.
