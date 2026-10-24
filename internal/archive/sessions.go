package archive

import (
	"cmp"
	"slices"
)

// SessionView is one view of a day, as sessionizing reads it: its kind
// and actor (the partition, NULL one value), session id ("" for NULL or
// none), id and ts (unix microseconds). Batch and Row are the caller's:
// where to find the view's other columns.
type SessionView struct {
	Kind, Actor, SessionID, ID  string
	KindNull, ActorNull, IDNull bool
	TS                          int64
	Batch, Row                  int32
}

// SessionViewBytes is about what a SessionView costs.
const SessionViewBytes = 96

// Session is one session of a day's views, by indexes into the views
// Sessionize sorted: its first and last view by (ts, id), the first view
// of its partition (which holds its kind and actor), its views, and
// whether its views declared its session id (the first view's).
type Session struct {
	First, Last, Partition int
	Views                  int64
	Declared               bool
}

// sessionKey is a session's key within its partition: a declared session
// id ('id:' in sessionsSelect), or the run of undeclared views ('gap:').
type sessionKey struct {
	declared bool
	sid      string
	run      int
}

// Sessionize sorts one day's views of one project and splits them into
// sessions as sessionsSelect does:
//   - views are partitioned by (kind, actor_id), NULL one value and last,
//     and ordered by whole second, then ts and id (NULL ids last, as
//     DuckDB sorts them ascending);
//   - a view with a session id belongs to that id's session; any other
//     starts a session when it is the partition's first view or comes more
//     than sessionGapSeconds after the view before it, whichever session
//     that was in. Views in one second are one peer group, as the window's
//     running sum counts them together: the group's first view decides.
//
// Sessions come by partition, then by their first view. A session's entry
// columns are its first view's, its exit path its last view's, its
// duration the whole seconds between them (Seconds), its session_id the
// declared id or NULL.
func Sessionize(views []SessionView) []Session {
	slices.SortFunc(views, cmpSessionView)
	var sessions []Session
	for start := 0; start < len(views); {
		end := start + 1
		for end < len(views) && cmpPartition(views[start], views[end]) == 0 {
			end++
		}
		keys := map[sessionKey]int{}
		run := 0
		var prev int64
		for j := start; j < end; {
			t := Seconds(views[j].TS)
			k := j + 1
			for k < end && Seconds(views[k].TS) == t {
				k++
			}
			if views[j].SessionID == "" && (j == start || t-prev > sessionGapSeconds) {
				run++
			}
			for x := j; x < k; x++ {
				key := sessionKey{run: run}
				if sid := views[x].SessionID; sid != "" {
					key = sessionKey{declared: true, sid: sid}
				}
				s, ok := keys[key]
				if !ok {
					s = len(sessions)
					keys[key] = s
					sessions = append(sessions, Session{First: x, Partition: start, Declared: key.declared})
				}
				sessions[s].Last = x
				sessions[s].Views++
			}
			prev, j = t, k
		}
		start = end
	}
	return sessions
}

func cmpSessionView(a, b SessionView) int {
	if c := cmpPartition(a, b); c != 0 {
		return c
	}
	if c := cmp.Compare(a.TS, b.TS); c != 0 {
		return c
	}
	if c := cmp.Compare(boolInt(a.IDNull), boolInt(b.IDNull)); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}

func cmpPartition(a, b SessionView) int {
	if c := cmp.Compare(boolInt(a.KindNull), boolInt(b.KindNull)); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
		return c
	}
	if c := cmp.Compare(boolInt(a.ActorNull), boolInt(b.ActorNull)); c != 0 {
		return c
	}
	return cmp.Compare(a.Actor, b.Actor)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Seconds is ts's (unix microseconds) whole seconds: floor(epoch(ts)).
func Seconds(ts int64) int64 {
	s := ts / 1e6
	if ts%1e6 < 0 {
		s--
	}
	return s
}
