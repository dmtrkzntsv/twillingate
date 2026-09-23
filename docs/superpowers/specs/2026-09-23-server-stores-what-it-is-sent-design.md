# The server stores what it is sent

Status: draft
Date: 2026-09-23

## Sequencing

Second of two specs. The first, `2026-09-22-sdk-global-factory-design.md`
(merged as #48), made the tag's identity mode the thing that decides what
the SDK *sends*: an `anonymous` instance never sends `$user_id`,
`$user_name` or `$install_id`. This spec removes the server-side project
mode that used to hash those ids for anonymous projects, so the collector
stores what it is sent.

The order is forced by browser caching. The served SDK carries a day-long
`Cache-Control`, so for up to a day after an upgrade a page can still run
the previous SDK, whose anonymous tag sent ids when `identify()` was
called. If the server stopped hashing in the same release, those ids would
be stored raw for that day. So:

1. a release carrying #48 and #49; upgrade prod; wait a day;
2. the release carrying this spec.

Prod is still on schema 013, so step 1 also runs migrations 014–016.

## Problem

A project's `identity` mode is set twice: on the project, where it decides
whether the server hashes the ids a client sends, and on the tag, where
since #48 it decides whether the SDK sends them at all. The two say the
same thing in different places and must agree by hand. Now that the client
gate exists, the server-side one is redundant for every SDK client and
only bites clients that post to the ingest API by hand — where an operator
who sends `$user_id` has already decided to identify users.

What the mode gates today, by function:

- `resolveIdentity` (`internal/server/handlers.go`): the anonymous branch
  hashes `$user_id` / `$install_id` with the daily salt via
  `identity.ActorHash`; the identified branch stores them as sent. Both
  fall back to the connection hash when no id arrived.
- `identityNames` (same file): drops `$user_name` for anonymous projects.
- `jobs.go` (~line 99): builds actors and retention only for identified
  projects.
- `ops_product.go` `retention` (~line 90): refuses anonymous projects.
- `manage/ops.go`: validates the field on create and update; `Snippet()`
  prints it into the tag.
- `api/ops_manage.go`, `ops_read.go`, `resources.go`, `cmd/twillingate/
  project.go`: the field on the tools, the project list, `schema://projects`
  and the CLI.
- `api/guide.go`: two mode paragraphs and the printed snippet.
- `docs/twillingate.md` "Identity" and the project fields table;
  `README.md` "Privacy and GDPR".

## Decisions

1. **The project has no identity mode.** Migration 017 drops the column.
   The collector stores `$user_id`, `$user_name`, `$install_id` and
   `$group_id`/`$group_name` as sent, for every project. An event with no
   `$user_id` and no `$install_id` is attributed to the daily-rotating
   connection hash, as it is today for identified projects.
2. **Retention runs for every project.** The actors upsert already keeps
   only user- and install-kind actors, so a project whose clients send no
   ids gets empty cohorts at no cost. The `retention` tool stops refusing
   and returns what exists.
3. **The privacy posture is the client's.** The served SDK's `anonymous`
   default sends nothing identifying; `data-identity="identified"` sends
   ids. A client that posts to the ingest API by hand decides by what it
   sends. There is no server-side "never store ids" switch: that is the
   paradigm chosen in #48's brainstorm, and a switch would recreate the
   duplicated setting this spec removes.
4. **`$user_name` is stored whenever it arrives with a `$user_id`.** The
   anonymous-mode drop existed to avoid naming a hash; there is no hash.
5. **The API stays strict.** A `create_project` or `update_project` call
   that still sends `identity` gets the same 400 an unknown field gets
   today. The release note says so.
6. **Visibility replaces the gate.** Nothing on the server decides whether
   ids are stored, but the operator can see that they are: the collector
   logs once, per process and project, when the first user or install id
   arrives. An accidental `identify()` on a marketing site becomes visible
   in the log instead of silent. A `list_projects` column for the same
   fact was considered and dropped as one more thing to explain. Considered and declined: an opt-in per-project lock
   (`store_ids=false`) that would keep hashing for projects wanting a
   provable "no identifiers" claim — the point of this spec is zero
   server-side identity configuration, and a lock would be a setting to
   explain, default or not.

## Design

### Storage — migration `017_drop_identity.sql`

```sql
-- The project's identity mode is gone: the collector stores what a client
-- sends, and the served SDK's identity mode decides what is sent
-- (docs/twillingate.md, Identity). Data already stored is untouched: ids
-- hashed under the old anonymous mode stay hashed, and cannot be linked
-- to anything the client sends from now on.
ALTER TABLE projects DROP COLUMN identity;
```

SQLite 3.35 supports `DROP COLUMN`; modernc.org/sqlite v1.57 ships a newer
engine. No `dataSteps` entry, no value rewrite. `store.RegistryProject`,
`manage.Project`, `manage.ProjectSpec` and the registry's `SELECT`,
`INSERT` and `UPDATE` lose the field; `config.IdentityAnonymous` and
`config.IdentityIdentified` are deleted.

Irreversible in the sense that the old binary reads a column that is gone:
it refuses to start. `deploy/UPGRADES.md` gains a section (below).

### Ingest

`resolveIdentity` becomes the identified branch alone:

```go
func resolveIdentity(rv resolved, salt, ip, ua, hashKey string) (actor, actorKind, user, group string) {
	actor, actorKind = rv.UserID, store.ActorUser
	if actor == "" {
		actor, actorKind = rv.InstallID, store.ActorInstall
	}
	if actor == "" {
		actor, actorKind = identity.VisitorHash(salt, ip, ua, hashKey), store.ActorConnection
	}
	return actor, actorKind, rv.UserID, rv.GroupID
}
```

`identityNames` keeps a user name whenever `rv.UserID != "" && rv.UserName
!= ""`. `identity.ActorHash` has no caller and is deleted with its tests;
`VisitorHash` and the salt rotation stay for the connection fallback. The
project argument disappears from both functions; the call site in
`handleEvents` no longer needs the project for identity at all (it still
needs it for the crawler filter's `kind == "web"` and for the key lookup).

### Visibility

- The collector logs `project receives ids` with `project` and `kind`
  (`user` or `install`) the first time a batch for that project resolves
  an actor of that kind since the process started — a `sync.Map` on
  `Server`, checked beside the existing `counters.record` call in
  `handleEvents`. One line per project per kind per process, `Info`
  level, so a restart repeats it once and an operator reading the log
  after adding a tag sees it.

### Daily pass

`jobs.go` drops the `identified` gate: `UpsertActors` and
`AggregateRetentionDay` run for every project with raw days. The existing
test that asserts anonymous projects get no actors becomes one that asserts
a project whose events carry no ids gets no actors (the upsert's
`actor_kind IN` filter), and one with `$user_id` gets them.

### Private API

- `ops_manage.go`: `identity` leaves `createIn`, `updateIn` and
  `projectToolOut`; the `update_project` description loses "Switching to
  identity=identified …"; `create_project`'s loses "privacy-significant".
- `ops_read.go`: the project row loses `Identity`; `list_projects`'s
  description becomes "List projects with id, name and data coverage. Call
  this first: every other tool takes a project_id from here."; the
  `identities` description says it surfaces personal data on projects
  whose clients send ids.
- `ops_product.go`: the retention refusal is deleted.
- `resources.go`: `pj` loses `Identity`; `schema://projects` describes
  the remaining fields; the `schemaViews` comment for `v_retention` says
  cohorts exist only for actors identified by `$user_id` or `$install_id`.
- `rest.go`: no route changes; the JSON bodies lose the field.
- `guide.go`: the header line drops `identity=%s`; the two mode paragraphs
  become one: "The tag decides what is sent. The printed tag is anonymous:
  it sends no `$user_id`, `$user_name` or `$install_id`, and `identify()`
  is inert. For a signed-in app add `data-identity="identified"` (or
  `identity: "identified"` in code), call `twillingate.identify(userId,
  userName)` and `group(groupId)` after login and `reset()` on logout; ids
  are then stored as sent. Nothing is kept on the device unless the tag
  declares consent." The "keep development traffic out by not loading the
  tag" sentence becomes "keep development traffic out with
  `init({ optOut: () => location.hostname === "localhost" })`".

### Registry, snippet and CLI

- `manage.Snippet(base, key)` prints:

  ```html
  <script defer src="%s/js/twillingate.js"
          data-key=%q></script>
  ```

  `ProjectSpec` validation loses the mode `switch`; `CreateProject`
  no longer defaults it; `UpdateProject` no longer carries it over.
- `cmd/twillingate/project.go`: `create` and `update` lose `-identity`;
  `list` prints `id  name` (`archived` marker unchanged). `key.go` and
  `keygen.go` call the two-argument `Snippet`; `keygen`'s literal
  `data-identity="anonymous"` line is removed.

### Documentation

`docs/twillingate.md`:

- "Set up a project": the `identity` row leaves the fields table; the
  `create_project` sentence and the CLI sample lose the field/flag;
  `project list` prints `id  name`.
- "Identity" is rewritten around the client:

  | The tag says | `$user_id`, `$install_id`, `$user_name` | `$group_id`, `$group_name` |
  | --- | --- | --- |
  | `anonymous` (default) | never sent | sent, stored raw |
  | `identified` | sent, stored as given | sent, stored raw |
  | a client posting by hand | stored as given, whatever it sends | stored raw |

  followed by the actor resolution sentence, the `$install_id` advice and
  the groups sentence as they are, and this replacing the enforcement
  sentence: "The collector stores what it is sent. What reaches it is
  decided by the tag's `data-identity` (or `identity` in code), so a
  project's privacy posture is the posture of its clients."
- The `identities` tool row: "Surfaces personal data on projects whose
  clients send ids."
- A sentence under "Identity" says the collector logs `project receives
  ids` the first time a project receives one, which is how to confirm a
  marketing site's tag sends nothing.
- "Writing SQL": `v_retention` "is populated only for projects with
  `identity=identified`" becomes "holds cohorts only for actors identified
  by `$user_id` or `$install_id`; a project whose clients send neither has
  none".

`README.md` "Privacy and GDPR" becomes three paragraphs: **What the
collector stores** (what it is sent; the served SDK's anonymous default
sends nothing identifying; the identified tag sends ids stored as given;
groups raw; IPs and User-Agents never stored; query strings and referrers
reduced; bots dropped). **What the SDK keeps on the device** (nothing
without consent; with consent, the retry queue, and for an identified tag
the visitor id, user and group; ePrivacy note). **Access and erasure**
(the API exposes stored ids to every token holder; erasure is
`twillingate project delete`).

`deploy/UPGRADES.md` gains "### Upgrading to storing what is sent
(migration 017)": no query to run; the check is a question — for every
project that was `anonymous`, confirm no client posts `$user_id` or
`$install_id` by hand, because from upgrade day they are stored as sent;
`twillingate project list` before the upgrade shows which projects those
are. What changes on the day: the mode column is gone; `list_projects` and
the CLI stop showing it; retention appears for projects that receive ids;
ids hashed before the upgrade stay hashed and never link to ids received
after. The upgrade must follow the #48 release by at least a day (the
Sequencing section, restated for the operator).

`docs/twillingate.md` and `README.md` change in the same commit as the
server, per CLAUDE.md; `deploy/UPGRADES.md` in the same commit as the
migration.

### Tests

- `migration017_test.go`: build at 16, insert a project row with
  `identity='anonymous'`, migrate through 17 (`migrateThrough(ctx, 17)`,
  never unbounded), assert the column is gone and the row's other fields
  survive; a fresh database has no such column.
- `handlers`: every anonymous-mode case in `server_test.go` /
  `testServerWithIdentity` collapses to one behaviour; assert a batch with
  `$user_id` on any project stores it raw with `actor_kind='user'`, one
  with `$install_id` only stores `install`, one with neither stores the
  connection hash; `$user_name` is stored with a `$user_id` and dropped
  without one.
- `identity`: `ActorHash` tests deleted.
- `server`: the first batch carrying `$user_id` for a project logs
  `project receives ids` once; a second batch does not; a batch with only
  `$install_id` logs once with `kind=install`.
- `jobs`: actors and retention built for every project; a project with no
  ids yields no actors.
- `api`: `retention` on a project with no identified actors returns an
  empty table, not an error; `create_project` with `identity` → 400;
  `list_projects` rows have no `identity`; `Snippet` output has no
  `data-identity`; guide text names `data-identity="identified"` and
  `optOut`.
- `manage`/`cmd`: the flag is gone (`-identity` is an unknown flag),
  `project list` columns, `Snippet`.
- `docs_sync_test.go`: `data-identity` stays in the SDK symbol list (the
  attribute exists); nothing else changes.

## Breaking changes

`feat!:` (repo-wide: server, api, manage, cmd, store). Release note:

- Projects no longer have an identity mode. The collector stores
  `$user_id`, `$user_name` and `$install_id` as sent, for every project;
  the served SDK's `anonymous` default sends none of them.
- `create_project`, `update_project`, `list_projects`, `schema://projects`
  and the CLI lose `identity`; a request that still sends it gets a 400.
- The printed snippet has no `data-identity`; a signed-in app adds
  `data-identity="identified"` itself.
- `retention` works for every project and is empty where no ids were
  received.
- Migration 017 is irreversible; the previous binary does not start
  against the upgraded file. Upgrade at least a day after the #48 release.

## Out of scope

- A per-project "never store ids" switch (decided against twice: in #48's
  brainstorm and again for this spec, in favour of visibility only).
- Retention for connection-hash actors.
- Renaming `unique_users` (it counts actors).
- Backfilling or re-linking ids hashed under the old anonymous mode.
