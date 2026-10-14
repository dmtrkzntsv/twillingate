package archive

import "fmt"

// The tables derived from the events (raw-first D16–D18), in each
// project's directory beside its events files:
//
//	sessions/<YYYY-MM-DD>.parquet  the sessions of a day not merged yet
//	sessions/<YYYY-MM>.parquet     the sessions of a merged month
//	identities.parquet             one row per user and per group
//	aliases.parquet                one row per (user_id, install_id) pair
//
// The daily pass rewrites one whenever it is missing or older than an
// events file it comes from (projectOf); a reader reads only the current
// ones and derives what they lack from the events (the open logs, a day
// dumped since). They describe the events as stored: rules (D6) do not
// apply to them. Deleting one loses nothing.
const (
	sessionsDir    = "sessions"
	identitiesFile = "identities.parquet"
	aliasesFile    = "aliases.parquet"
)

// sessionGapSeconds is the silence after which an actor's next view starts
// a session when the client declared none: agg_views_daily's 30 minutes.
const sessionGapSeconds = 1800

// sessionsSelect is the SELECT of the sessions of src's views, src being a
// relation with the events' columns. Views split as viewSessionsCTE
// (store/sqlite/aggregate_views.go) splits them: a view with a session id
// belongs to that id's session; any other starts a session when it is the
// actor's first view of the day or comes more than 30 minutes after the
// actor's previous view, whichever session that was in. The day is part
// of the key, so midnight UTC cuts a session. One row per session: its
// first and last view, the whole seconds between them, its views, the
// entry view's host, path, referrer and UTMs and the exit view's path,
// views at the same second ordered by id. The 'id:' and 'gap:' prefixes of
// the session keys deliberately differ from viewSessionsCTE, which keys a
// declared session id and a gap counter in one namespace (a declared id "2"
// would merge with gap session 2 there, not here).
func sessionsSelect(src string) string {
	return fmt.Sprintf(`WITH v AS (
  SELECT project_id, day, kind, actor_id, actor_kind, session_id, id, ts, host, path,
         referrer_source, utm_source, utm_medium, utm_campaign,
         CAST(floor(epoch(ts)) AS BIGINT) AS t
  FROM %s WHERE family = 'views'
), marked AS (
  SELECT *, CASE WHEN coalesce(session_id, '') <> '' THEN 0
                 WHEN lag(t) OVER w IS NULL OR t - lag(t) OVER w > %d THEN 1
                 ELSE 0 END AS new_session
  FROM v WINDOW w AS (PARTITION BY project_id, day, kind, actor_id ORDER BY t)
), keyed AS (
  SELECT *, CASE WHEN coalesce(session_id, '') <> '' THEN 'id:' || session_id
                 ELSE 'gap:' || CAST(sum(new_session) OVER (PARTITION BY project_id, day, kind, actor_id ORDER BY t) AS VARCHAR)
            END AS skey
  FROM marked
)
SELECT project_id, day, kind, actor_id,
       first(actor_kind ORDER BY ts, id) AS actor_kind,
       nullif(max(session_id), '') AS session_id,
       min(ts) AS started_at, max(ts) AS ended_at,
       max(t) - min(t) AS duration_sec,
       count(*) AS views,
       first(host ORDER BY ts, id) AS entry_host,
       first(path ORDER BY ts, id) AS entry_path,
       last(path ORDER BY ts, id) AS exit_path,
       first(referrer_source ORDER BY ts, id) AS referrer_source,
       first(utm_source ORDER BY ts, id) AS utm_source,
       first(utm_medium ORDER BY ts, id) AS utm_medium,
       first(utm_campaign ORDER BY ts, id) AS utm_campaign
FROM keyed
GROUP BY project_id, day, kind, actor_id, skey
ORDER BY project_id, day, started_at, actor_id`, src, sessionGapSeconds)
}

// identitiesSelect is the SELECT of the users and groups in src's views and
// product events (v_identity_daily's two families), one row per (project,
// kind, id): kind user for a user_id, group for a group_id; the name sent
// with the id's latest event that carried one; its first and last event;
// its views and its product events.
func identitiesSelect(src string) string {
	return `WITH src AS (SELECT * FROM ` + src + ` WHERE family IN ('views', 'product')),
ids AS (
  SELECT project_id, 'user' AS kind, user_id AS id, user_name AS name, ts, family FROM src WHERE user_id <> ''
  UNION ALL
  SELECT project_id, 'group' AS kind, group_id AS id, group_name AS name, ts, family FROM src WHERE group_id <> ''
)
SELECT project_id, kind, id,
       arg_max(name, ts) FILTER (WHERE name IS NOT NULL) AS name,
       min(ts) AS first_seen, max(ts) AS last_seen,
       count(*) FILTER (WHERE family = 'views') AS views,
       count(*) FILTER (WHERE family = 'product') AS events
FROM ids
GROUP BY project_id, kind, id
ORDER BY project_id, kind, id`
}

// aliasesSelect is the SELECT of every (user_id, install_id) pair sent on
// one event of src, any family (raw-first D18): first and last seen, and
// the events that carried it.
func aliasesSelect(src string) string {
	return `SELECT project_id, user_id, install_id, min(ts) AS first_seen, max(ts) AS last_seen, count(*) AS events
FROM ` + src + `
WHERE user_id <> '' AND install_id <> ''
GROUP BY project_id, user_id, install_id
ORDER BY project_id, user_id, install_id`
}

// identitiesCombine folds rows of identities (a stored file's and those
// derived on read) into one per (project, kind, id): the name of the
// newest set that has one, the earliest first_seen, the latest last_seen,
// the counts added.
func identitiesCombine(parts string) string {
	return `SELECT project_id, kind, id,
       arg_max(name, last_seen) FILTER (WHERE name IS NOT NULL) AS name,
       min(first_seen) AS first_seen, max(last_seen) AS last_seen,
       CAST(sum(views) AS BIGINT) AS views, CAST(sum(events) AS BIGINT) AS events
FROM (` + parts + `)
GROUP BY project_id, kind, id`
}

// aliasesCombine folds rows of aliases into one per (project, user_id,
// install_id), as identitiesCombine does.
func aliasesCombine(parts string) string {
	return `SELECT project_id, user_id, install_id, min(first_seen) AS first_seen, max(last_seen) AS last_seen,
       CAST(sum(events) AS BIGINT) AS events
FROM (` + parts + `)
GROUP BY project_id, user_id, install_id`
}

// eventsOf is files read as one relation with every fixed column at its
// type: a column a file lacks (written before it existed) reads NULL.
func eventsOf(files []string) string {
	return "(" + emptyRow() + " UNION ALL BY NAME SELECT * FROM read_parquet(" + sqlList(files) + ", union_by_name=true))"
}
