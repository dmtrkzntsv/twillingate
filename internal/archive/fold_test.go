package archive

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// foldDay is the day the fold tests' log holds.
const foldDay = "2026-10-11"

// writeLines writes project 1's log of foldDay, one line per map, each
// with project_id 1 and day foldDay.
func (f fixture) writeLines(t *testing.T, lines ...map[string]any) string {
	t.Helper()
	var sb strings.Builder
	for _, l := range lines {
		l["project_id"], l["day"] = 1, foldDay
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	f.writeFile(t, foldDay+".log", sb.String())
	return f.project1(foldDay + ".log")
}

// orNull is p's value, nil for NULL.
func orNull(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// sortedValues is f.values sorted.
func (f fixture) sortedValues(t *testing.T, sql string) []string {
	t.Helper()
	out := f.values(t, sql)
	slices.Sort(out)
	return out
}

// TestSessionizeMatchesDuckDB: the sessions Sessionize splits a day's
// views into are sessionsSelect's: a gap over 30 minutes in whole seconds
// (1800.2 seconds is 1800), declared session ids (one of them "2", one
// "") among undeclared views, an actor under two kinds, views without a
// kind or an actor, two views in one second ordered by id, a repeated id
// and a NULL one, a product event no session counts.
func TestSessionizeMatchesDuckDB(t *testing.T) {
	f := newFixture(t)
	n := 0
	view := func(kind, actor, sid, at, path string, more ...any) map[string]any {
		n++
		l := map[string]any{"id": fmt.Sprintf("v%02d", n), "family": "views", "ts": foldDay + "T" + at + "Z",
			"actor_kind": "install", "host": "a.io", "path": path}
		for k, v := range map[string]string{"kind": kind, "actor_id": actor, "session_id": sid} {
			if v != "-" {
				l[k] = v
			}
		}
		for i := 0; i < len(more); i += 2 {
			if more[i+1] == nil {
				delete(l, more[i].(string))
			} else {
				l[more[i].(string)] = more[i+1]
			}
		}
		return l
	}
	path := f.writeLines(t,
		view("web", "x0", "-", "10:00:00.7", "/a", "referrer_source", "google", "utm_source", "news", "utm_medium", "cpc"),
		view("web", "x0", "-", "10:10:00", "/b"),
		view("web", "x0", "-", "10:40:00.2", "/c"), // 1800 whole seconds on: the same session
		view("web", "x0", "-", "11:20:00", "/d", "actor_kind", "user"),
		view("web", "x0", "-", "11:25:00", "/e"),
		map[string]any{"id": "p1", "family": "product", "ts": foldDay + "T10:30:00Z", "kind": "web", "actor_id": "x0"},
		view("web", "x1", "s1", "10:00:00", "/s1a", "utm_source", "mail"),
		view("web", "x1", "-", "10:02:00", "/u1"),
		view("web", "x1", "s1", "10:05:00", "/s1b"),
		view("web", "x1", "-", "10:20:00", "/u2"),
		view("web", "x1", "2", "12:00:00", "/d2"),
		view("web", "x1", "", "13:00:00", "/u3"),
		view("web", "x1", "2", "13:10:00", "/d2b"),
		view("web", "x3", "-", "09:00:00", "/w"),
		view("app", "x3", "-", "09:05:00", "/app", "host", nil),
		view("web", "x4", "-", "14:00:00", "/second", "id", "w-b"),
		view("web", "x4", "-", "14:00:00", "/first", "id", "w-a"),
		view("web", "x4", "-", "14:00:30", "/again", "id", "w-a"), // a repeat: dropped
		view("web", "-", "-", "15:00:00", "/anon1"),
		view("web", "-", "-", "15:10:00", "/anon2"),
		view("-", "x5", "-", "16:00:00", "/nokind"),
		view("web", "x6", "-", "17:00:00", "/noid", "id", nil),
		view("web", "x6", "-", "17:05:00", "/noid2", "id", nil), // a second NULL id: dropped
	)
	want := frozen(t, "sessions", f.sortedValues(t, "SELECT kind, actor_id, actor_kind, session_id, epoch_us(started_at), epoch_us(ended_at), duration_sec, "+
		"views, entry_host, entry_path, exit_path, referrer_source, utm_source, utm_medium, utm_campaign FROM ("+
		sessionsSelect(f.logRel(t, foldDay))+")"))

	var lines []logLine
	var views []SessionView
	for _, l := range readLogLines(t, path) {
		if l.str("family") != "views" {
			continue
		}
		ts, _ := l.ts(t)
		views = append(views, SessionView{Kind: l.str("kind"), KindNull: l["kind"] == nil, Actor: l.str("actor_id"),
			ActorNull: l["actor_id"] == nil, SessionID: l.str("session_id"), ID: l.str("id"), IDNull: l["id"] == nil,
			TS: ts, Row: int32(len(lines))})
		lines = append(lines, l)
	}
	sessions := Sessionize(views)
	var got []string
	for _, s := range sessions {
		p, first, last := views[s.Partition], views[s.First], views[s.Last]
		fl, ll := lines[first.Row], lines[last.Row]
		nullable := func(s string, null bool) any {
			if null {
				return nil
			}
			return s
		}
		got = append(got, fmt.Sprint([]any{nullable(p.Kind, p.KindNull), nullable(p.Actor, p.ActorNull), orNull(fl["actor_kind"]),
			nullable(first.SessionID, !s.Declared), first.TS, last.TS, Seconds(last.TS) - Seconds(first.TS), s.Views,
			orNull(fl["host"]), orNull(fl["path"]), orNull(ll["path"]), orNull(fl["referrer_source"]), orNull(fl["utm_source"]),
			orNull(fl["utm_medium"]), orNull(fl["utm_campaign"])}))
	}
	slices.Sort(got)
	if len(want) < 10 || !eq(got, want) {
		t.Fatalf("sessions\n got %v\nwant %v", got, want)
	}
}

// TestSecondsFloors: a ts before 1970 floors to the second before it.
func TestSecondsFloors(t *testing.T) {
	for ts, want := range map[int64]int64{0: 0, 1: 0, 999_999: 0, 1_000_000: 1, -1: -1, -1_000_000: -1, -1_000_001: -2} {
		if got := Seconds(ts); got != want {
			t.Errorf("Seconds(%d) = %d, want %d", ts, got, want)
		}
	}
}

// identityLines are views, product events and a measure carrying users
// and groups, named and not: the latest named event names, an empty id
// is none, a measure neither counts nor names.
func identityLines() []map[string]any {
	ev := func(id, family, at string, kv ...string) map[string]any {
		l := map[string]any{"id": id, "family": family, "ts": foldDay + "T" + at + "Z"}
		for i := 0; i < len(kv); i += 2 {
			l[kv[i]] = kv[i+1]
		}
		return l
	}
	return []map[string]any{
		ev("v1", "views", "08:00:00", "user_id", "u1"),
		ev("p1", "product", "09:00:00", "user_id", "u1", "user_name", "Ada", "group_id", "g1", "group_name", "Acme"),
		ev("p2", "product", "10:00:00", "user_id", "u1", "user_name", "Ada L.", "group_id", "g1"),
		ev("p3", "product", "11:00:00", "user_id", "u1"),
		ev("v2", "views", "07:00:00", "user_id", "u1", "user_name", "Early"), // named first, not last
		ev("p4", "product", "12:00:00", "user_id", "u2"),
		ev("m1", "measures", "13:00:00", "user_id", "u2", "user_name", "Bo"),
		ev("p5", "product", "13:30:00", "user_id", "", "group_id", "g2"),
		ev("v3", "views", "14:00:00", "group_id", "g2", "group_name", "Beta"),
		ev("v4", "views", "14:30:00", "user_id", "u3", "user_name", ""),
		ev("p1", "product", "15:00:00", "user_id", "u9"), // a repeat: dropped
	}
}

// seeIdentities folds lines into m as the native engine does
// (eachIdentity): views and product events, a non-empty id, a ts.
func seeIdentities(t *testing.T, m IdentityMap, lines []logLine) {
	t.Helper()
	for _, l := range lines {
		fam := l.str("family")
		if fam != "views" && fam != "product" {
			continue
		}
		ts, ok := l.ts(t)
		if !ok {
			continue
		}
		for _, who := range []struct {
			id, name string
			group    bool
		}{{"user_id", "user_name", false}, {"group_id", "group_name", true}} {
			if id := l.str(who.id); id != "" {
				m.See(IdentityKey{Group: who.group, ID: id}, l.str(who.name), l[who.name] != nil, ts, fam == "views")
			}
		}
	}
}

func identityRows(m IdentityMap) []string {
	var out []string
	for k, x := range m {
		kind := "user"
		if k.Group {
			kind = "group"
		}
		var name any
		if x.Named {
			name = x.Name
		}
		out = append(out, fmt.Sprint([]any{kind, k.ID, name, x.First, x.Last, x.Views, x.Events}))
	}
	slices.Sort(out)
	return out
}

// TestIdentitiesFoldMatchesDuckDB: the identities See folds a log's
// events into are identitiesSelect's, and so are two parts' put together
// with Fold.
func TestIdentitiesFoldMatchesDuckDB(t *testing.T) {
	f := newFixture(t)
	path := f.writeLines(t, identityLines()...)
	want := frozen(t, "identities", f.sortedValues(t, "SELECT kind, id, name, epoch_us(first_seen), epoch_us(last_seen), views, events FROM ("+
		identitiesSelect(f.logRel(t, foldDay))+")"))
	lines := readLogLines(t, path)
	whole := IdentityMap{}
	seeIdentities(t, whole, lines)
	if got := identityRows(whole); len(want) < 4 || !eq(got, want) {
		t.Fatalf("identities\n got %v\nwant %v", got, want)
	}
	if whole.Bytes() <= 0 {
		t.Errorf("bytes %d", whole.Bytes())
	}
	a, b := IdentityMap{}, IdentityMap{}
	for i, l := range lines {
		seeIdentities(t, []IdentityMap{a, b}[i%2], []logLine{l})
	}
	a.Fold(b)
	if got := identityRows(a); !eq(got, want) {
		t.Fatalf("folded identities\n got %v\nwant %v", got, want)
	}
}

// TestAliasesFoldMatchesDuckDB: the aliases See folds a log's events into
// are aliasesSelect's, in its order: any family, a pair only when both
// ids are non-empty, an event without a ts counted but dating nothing
// (a pair seen only so has no first or last seen); and so are two parts'
// put together with Fold.
func TestAliasesFoldMatchesDuckDB(t *testing.T) {
	f := newFixture(t)
	ev := func(id, family, at, user, install string) map[string]any {
		l := map[string]any{"id": id, "family": family}
		if at != "" {
			l["ts"] = foldDay + "T" + at + "Z"
		}
		if user != "-" {
			l["user_id"] = user
		}
		if install != "-" {
			l["install_id"] = install
		}
		return l
	}
	path := f.writeLines(t,
		ev("p1", "product", "09:00:00", "u1", "i1"),
		ev("v1", "views", "10:00:00", "u1", "i1"),
		ev("m1", "measures", "08:00:00", "u1", "i1"),
		ev("p2", "product", "11:00:00", "u1", "i2"),
		ev("p3", "product", "12:00:00", "", "i3"),
		ev("p4", "product", "12:00:00", "-", "i3"),
		ev("p5", "product", "12:00:00", "u4", ""),
		ev("p6", "product", "", "u1", "i2"), // no ts: counted, dates nothing
		ev("p7", "product", "", "u5", "i5"), // only without a ts
		ev("p8", "product", "13:00:00", "U1", "i1"),
		ev("p1", "product", "14:00:00", "u1", "i9"), // a repeat: dropped
	)
	want := frozen(t, "aliases", f.values(t, "SELECT user_id, install_id, epoch_us(first_seen), epoch_us(last_seen), events FROM ("+
		aliasesSelect(f.logRel(t, foldDay))+")"))
	lines := readLogLines(t, path)
	see := func(m AliasMap, lines []logLine) {
		for _, l := range lines {
			ts, ok := l.ts(t)
			m.See(l.str("user_id"), l.str("install_id"), ts, ok)
		}
	}
	rows := func(m AliasMap) []string {
		var out []string
		for _, k := range m.Keys() {
			x := m[k]
			var first, last any
			if x.Timed {
				first, last = x.First, x.Last
			}
			out = append(out, fmt.Sprint([]any{k.UserID, k.InstallID, first, last, x.Events}))
		}
		return out
	}
	whole := AliasMap{}
	see(whole, lines)
	if got := rows(whole); len(want) < 4 || !eq(got, want) {
		t.Fatalf("aliases\n got %v\nwant %v", got, want)
	}
	a, b := AliasMap{}, AliasMap{}
	for i, l := range lines {
		see([]AliasMap{a, b}[i%2], []logLine{l})
	}
	a.Fold(b)
	if got := rows(a); !eq(got, want) {
		t.Fatalf("folded aliases\n got %v\nwant %v", got, want)
	}
}
