package archive

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
//
// The pass writes them in Go (derived.go), as defined below, and native
// derives what the files lack the same way (internal/native).
const (
	sessionsDir    = "sessions"
	identitiesFile = "identities.parquet"
	aliasesFile    = "aliases.parquet"
)

// sessionGapSeconds is the silence after which an actor's next view starts
// a session when the client declared none: agg_views_daily's 30 minutes.
const sessionGapSeconds = 1800

// The sessions of a day's views: views split as viewSessionsCTE
// (store/sqlite/aggregate_views.go) splits them. A view with a session id
// belongs to that id's session; any other starts a session when it is the
// actor's first view of the day or comes more than 30 minutes after the
// actor's previous view, whichever session that was in. The day is part
// of the key, so midnight UTC cuts a session. One row per session: its
// first and last view, the whole seconds between them, its views, the
// entry view's host, path, referrer and UTMs and the exit view's path,
// views at the same second ordered by id. The 'id:' and 'gap:' prefixes of
// the session keys deliberately differ from viewSessionsCTE, which keys a
// declared session id and a gap counter in one namespace (a declared id "2"
// would merge with gap session 2 there, not here). Rows are ordered by
// project, day, start and actor.
//
// The identities in views and product events (v_identity_daily's two
// families), one row per (project, kind, id): kind user for a user_id,
// group for a group_id; the name sent with the id's latest event that
// carried one; its first and last event; its views and its product
// events. Rows from two sets fold into one per (project, kind, id): the
// name of the newest set that has one, the earliest first_seen, the latest
// last_seen, the counts added.
//
// The aliases: every (user_id, install_id) pair sent on one event, any
// family (raw-first D18), with its first and last seen and the events that
// carried it, ordered by project, user_id and install_id; folded as the
// identities are.
