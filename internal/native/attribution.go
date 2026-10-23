package native

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// sourceColumns and newestColumns are what a Found reads off a session
// and a view, in archive.Found's order.
var (
	sourceColumns = []string{"referrer_source", "utm_source", "utm_medium", "utm_campaign"}
	newestColumns = []string{"kind", "group_id", "session_id", "platform", "os", "os_version", "os_name",
		"browser", "browser_version", "browser_locale", "app_version", "app_locale", "device", "device_model",
		"display_width", "display_height", "consent"}
)

// Attribute answers where each of ls came from, as the jobs' attributor
// asks it. For each lookup it reads the sessions (no rule applies) and the
// views (the rules applied) from `days` days before its arrival (At, to
// the second) up to it, of the lookup's actor, of the install id it was
// sent with, of its connection hash (the views sent before consent gave
// the visitor an id) and, for a user, of every install id the project's
// aliases pair with the user. Source is the newest session with any
// referrer or UTM (started_at descending, then actor_kind and actor_id),
// Newest the newest view (ts descending, then id descending, a NULL id
// last). One Found per lookup, in no particular order.
//
// A lookup whose project has no file as new as its window's first day
// finds nothing, which the file names tell (archive.LatestDays) without
// reading a file. Attribute waits for one of MaxScans turns, and runs
// again on a file the daily pass removes under it, as Answer does.
func (e *Engine) Attribute(ctx context.Context, ls []archive.Lookup, days int, rules archive.Rules) ([]archive.Found, error) {
	if len(ls) == 0 {
		return nil, nil
	}
	latest, err := archive.LatestDays(e.dir)
	if err != nil {
		return nil, err
	}
	var out []archive.Found
	byProject := map[int64][]archive.Lookup{}
	var projects []int64
	for _, l := range ls {
		if day, ok := latest[l.ProjectID]; ok && day >= archive.Day(l.At.AddDate(0, 0, -days)) {
			if byProject[l.ProjectID] == nil {
				projects = append(projects, l.ProjectID)
			}
			byProject[l.ProjectID] = append(byProject[l.ProjectID], l)
		} else {
			out = append(out, archive.Found{ProjectID: l.ProjectID, ID: l.ID})
		}
	}
	if len(projects) == 0 {
		return out, nil
	}
	err = e.turn(ctx, func() error {
		found := make([]archive.Found, 0, len(ls))
		for _, p := range projects {
			f, err := e.attributeProject(ctx, p, byProject[p], days, rules)
			if err != nil {
				return err
			}
			found = append(found, f...)
		}
		out = append(out, found...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// attributionViewColumns are the columns attributionViews reads.
var attributionViewColumns = append([]string{"actor_kind", "actor_id", "ts", "id"}, newestColumns...)

// whoKey is one actor a lookup follows.
type whoKey struct{ kind, id string }

// attribution is one project's lookups: each one's window and the
// actors each follows.
type attribution struct {
	ls       []archive.Lookup
	from, to []int64            // each lookup's window, unix microseconds, both ends in
	who      map[whoKey][]int32 // the lookups following an actor
	r        Range              // the days of every window
}

// attributeProject answers project's lookups ls.
func (e *Engine) attributeProject(ctx context.Context, project int64, ls []archive.Lookup, days int, rules archive.Rules) ([]archive.Found, error) {
	all, err := archive.ListFiles(e.dir)
	if err != nil {
		return nil, err
	}
	files := all[project]
	a := attribution{ls: ls, from: make([]int64, len(ls)), to: make([]int64, len(ls)), who: map[whoKey][]int32{}}
	window := int64(days) * 24 * int64(time.Hour/time.Microsecond)
	var users map[string][]int32 // the user lookups, by actor id
	follow := func(k whoKey, i int32) {
		list := a.who[k]
		if len(list) == 0 || list[len(list)-1] != i {
			a.who[k] = append(list, i)
		}
	}
	for i, l := range ls {
		arrived := l.At.UTC().Truncate(time.Second)
		a.to[i] = arrived.UnixMicro()
		a.from[i] = a.to[i] - window
		from := civil.DateOf(time.UnixMicro(a.from[i]).UTC())
		to := civil.DateOf(arrived)
		if i == 0 || from.Before(a.r.From) {
			a.r.From = from
		}
		if i == 0 || a.r.To.Before(to) {
			a.r.To = to
		}
		follow(whoKey{l.ActorKind, l.ActorID}, int32(i))
		if l.InstallID != "" {
			follow(whoKey{store.ActorInstall, l.InstallID}, int32(i))
		}
		if l.ConnectionID != "" {
			follow(whoKey{store.ActorConnection, l.ConnectionID}, int32(i))
		}
		if l.ActorKind == store.ActorUser {
			if users == nil {
				users = map[string][]int32{}
			}
			users[l.ActorID] = append(users[l.ActorID], int32(i))
		}
	}
	charge := e.charge()
	rs, err := e.ruleSet(project, rules)
	if err != nil {
		return nil, err
	}
	// The open logs are read through the engine's cache, which decodes a
	// log once for the aliases, the sessions of its day and the views alike.
	logs := e.readLog
	if users != nil {
		installs, err := e.aliasedInstalls(ctx, files, users, logs)
		if err != nil {
			return nil, err
		}
		for _, in := range installs {
			follow(whoKey{store.ActorInstall, in.install}, in.lookup)
		}
	}

	sources, err := e.attributionSources(ctx, project, &a, logs, charge)
	if err != nil {
		return nil, err
	}
	s := scan{e: e, q: Query{Table: "views"}, project: project, rules: rs, logs: logs}
	newest, err := e.attributionViews(ctx, s, files, &a, charge)
	if err != nil {
		return nil, err
	}
	out := make([]archive.Found, len(ls))
	for i, l := range ls {
		f := archive.Found{ProjectID: l.ProjectID, ID: l.ID}
		if s := sources[i]; s.ok {
			v := s.vals
			f.Source = store.Event{ReferrerSource: v[0], UTMSource: v[1], UTMMedium: v[2], UTMCampaign: v[3]}
		}
		if n := newest[i]; n.ok {
			v := n.vals
			f.Newest = store.Event{Kind: v[0], GroupID: v[1], SessionID: v[2], Platform: v[3], OS: v[4], OSVersion: v[5],
				OSName: v[6], Browser: v[7], BrowserVersion: v[8], BrowserLocale: v[9], AppVersion: v[10], AppLocale: v[11],
				Device: v[12], DeviceModel: v[13]}
			f.Newest.DisplayWidth, _ = strconv.Atoi(v[14])
			f.Newest.DisplayHeight, _ = strconv.Atoi(v[15])
			switch v[16] {
			case "1":
				f.Newest.Consent = store.ConsentGiven
			case "0":
				f.Newest.Consent = store.ConsentNone
			}
		}
		out[i] = f
	}
	return out, nil
}

// aliasedInstall is an install id a user lookup follows.
type aliasedInstall struct {
	lookup  int32
	install string
}

// aliasedInstalls is the install ids the aliases of f pair with users'
// ids, for each lookup of the user: the current aliases file's pairs and
// those of the open logs, or, while the file is missing or stale, those of
// every events file and log, as the reader's aliases table holds them (a
// pair is an event of any family with both ids not empty, no rule
// applied).
// The logs are read through logs.
func (e *Engine) aliasedInstalls(ctx context.Context, f archive.Files, users map[string][]int32, logs logReader) ([]aliasedInstall, error) {
	seen := map[archive.AliasKey]bool{}
	var mu sync.Mutex // the workers' batches arrive at once
	see := func(b *Batch) {
		_, uc := b.col("user_id")
		_, ic := b.col("install_id")
		for i := range b.N {
			u, _ := textAt(uc, i)
			in, _ := textAt(ic, i)
			if u == "" || in == "" || users[u] == nil {
				continue
			}
			mu.Lock()
			seen[archive.AliasKey{UserID: u, InstallID: in}] = true
			mu.Unlock()
		}
	}
	cols := []string{"user_id", "install_id"}
	parquet := f.Parquet
	if f.Aliases != "" {
		parquet = nil
		if err := readIdentityFile(f.Aliases, cols, func(b *Batch) error { see(b); return nil }); err != nil {
			return nil, err
		}
	}
	if err := e.scanIdentityEvents(ctx, parquet, nil, cols, func(_ int, b *Batch) error { see(b); return nil }); err != nil {
		return nil, err
	}
	for _, path := range f.Logs {
		bs, err := logs(ctx, path, cols, "", everything)
		if err != nil {
			return nil, err
		}
		for _, b := range bs {
			see(b)
		}
	}
	var out []aliasedInstall
	for k := range seen {
		for _, i := range users[k.UserID] {
			out = append(out, aliasedInstall{lookup: i, install: k.InstallID})
		}
	}
	return out, nil
}

// attributionPick is the best row a lookup has found so far: its order
// keys and its values.
type attributionPick struct {
	ok     bool
	ts     int64
	k1, k2 string // sessions: actor_kind, actor_id; views: id
	k2Null bool   // views: a NULL id
	vals   []string
}

// attributionSources is, for each of a's lookups, the newest session of
// its actors in its window with any referrer or UTM.
func (e *Engine) attributionSources(ctx context.Context, project int64, a *attribution, logs logReader, charge func(int64) error) ([]attributionPick, error) {
	cols := append([]string{"actor_kind", "actor_id", "started_at"}, sourceColumns...)
	parts := make([][]attributionPick, e.workers)
	for w := range parts {
		parts[w] = make([]attributionPick, len(a.ls))
	}
	err := e.scanSessionsWith(ctx, project, a.r, cols, logs, charge, func(w int, b *Batch) error {
		_, kc := b.col("actor_kind")
		_, ic := b.col("actor_id")
		_, tc := b.col("started_at")
		src := make([]*Column, len(sourceColumns))
		for j, c := range sourceColumns {
			_, src[j] = b.col(c)
		}
		best := parts[w]
		for i := range b.N {
			ts, ok := timeAt(tc, i)
			if !ok {
				continue
			}
			kind, ok1 := textAt(kc, i)
			id, ok2 := textAt(ic, i)
			if !ok1 || !ok2 {
				continue
			}
			list := a.who[whoKey{kind, id}]
			if len(list) == 0 {
				continue
			}
			has := false
			for _, c := range src {
				if s, _ := textAt(c, i); s != "" {
					has = true
					break
				}
			}
			if !has {
				continue
			}
			for _, l := range list {
				if ts < a.from[l] || ts > a.to[l] {
					continue
				}
				cand := attributionPick{ok: true, ts: ts, k1: kind, k2: id}
				if !sessionBefore(cand, best[l]) {
					continue
				}
				cand.vals = make([]string, len(src))
				for j, c := range src {
					cand.vals[j], _ = textAt(c, i)
				}
				best[l] = cand
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := parts[0]
	for _, p := range parts[1:] {
		for l, cand := range p {
			if cand.ok && sessionBefore(cand, out[l]) {
				out[l] = cand
			}
		}
	}
	return out, nil
}

// sessionBefore says whether session a ranks before b (none: a always
// does): started_at descending, then actor_kind and actor_id ascending.
func sessionBefore(a, b attributionPick) bool {
	switch {
	case !b.ok:
		return true
	case a.ts != b.ts:
		return a.ts > b.ts
	case a.k1 != b.k1:
		return a.k1 < b.k1
	}
	return a.k2 < b.k2
}

// viewBefore says whether view a ranks before b (none: a always does): ts
// descending, then id descending, a NULL id last.
func viewBefore(a, b attributionPick) bool {
	switch {
	case !b.ok:
		return true
	case a.ts != b.ts:
		return a.ts > b.ts
	case a.k2Null != b.k2Null:
		return b.k2Null
	}
	return a.k2 > b.k2
}

// attributionViews is, for each of a's lookups, the newest view of its
// actors in its window, read through s (the views, the rules applied).
func (e *Engine) attributionViews(ctx context.Context, s scan, files archive.Files, a *attribution, charge func(int64) error) ([]attributionPick, error) {
	cols := attributionViewColumns
	parts := make([][]attributionPick, e.workers)
	for w := range parts {
		parts[w] = make([]attributionPick, len(a.ls))
	}
	err := s.scanEvents(ctx, files, a.r, cols, nil, charge, func(w int, b *Batch) error {
		_, kc := b.col("actor_kind")
		_, ac := b.col("actor_id")
		_, tc := b.col("ts")
		_, idc := b.col("id")
		vals := make([]*Column, len(newestColumns))
		for j, c := range newestColumns {
			_, vals[j] = b.col(c)
		}
		best := parts[w]
		for i := range b.N {
			ts, ok := timeAt(tc, i)
			if !ok {
				continue
			}
			kind, ok1 := textAt(kc, i)
			actor, ok2 := textAt(ac, i)
			if !ok1 || !ok2 {
				continue
			}
			list := a.who[whoKey{kind, actor}]
			if len(list) == 0 {
				continue
			}
			id, idOK := textAt(idc, i)
			for _, l := range list {
				if ts < a.from[l] || ts > a.to[l] {
					continue
				}
				cand := attributionPick{ok: true, ts: ts, k2: id, k2Null: !idOK}
				if !viewBefore(cand, best[l]) {
					continue
				}
				cand.vals = make([]string, len(vals))
				for j, c := range vals {
					cand.vals[j] = cellString(c, i)
				}
				best[l] = cand
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := parts[0]
	for _, p := range parts[1:] {
		for l, cand := range p {
			if cand.ok && viewBefore(cand, out[l]) {
				out[l] = cand
			}
		}
	}
	return out, nil
}

// cellString is c's value at row i as CAST(… AS VARCHAR) reads a text or
// an integer, "" for NULL.
func cellString(c *Column, i int) string {
	if c == nil {
		return ""
	}
	if c.Idx != nil {
		s, _ := textAt(c, i)
		return s
	}
	if n, ok := intAt(c, i); ok {
		return strconv.FormatInt(n, 10)
	}
	return ""
}
