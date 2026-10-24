package archive

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

var monthFileName = regexp.MustCompile(`^(\d{4}-\d{2})\.parquet$`)

// Files is what a reader sees of one project's directory: the Parquet
// files that hold its final rows, the logs that hold the rest (open,
// rotated, or claimed by a dump not done yet), each in name order, and
// the derived files current with them (derive.go).
type Files struct {
	Parquet []string
	Logs    []string
	// Sessions are the session files no events file they come from is
	// newer than; SessionGaps the days none of them covers (a log's day, a
	// day or month whose session file is missing or stale), which a reader
	// derives from the events.
	Sessions    []string
	SessionGaps []Span
	// Identities and Aliases are the derived files when current, else "".
	Identities, Aliases string
}

// Span is a range of days, both ends included, as YYYY-MM-DD.
type Span struct{ From, To string }

// projectFiles is Files and what the daily pass has to derive.
type projectFiles struct {
	Files
	deriveDays    map[string][]string // a day whose session file is missing or stale: its day file and segments
	deriveMonths  map[string]string   // a month whose session file is missing or stale: its month file
	orphans       []string            // derived files no events file backs any more
	identitiesDue bool                // identities.parquet or aliases.parquet missing or stale
}

// ListFiles lists raw/ for a reader, by project id, leaving out what the
// daily pass would make it count twice:
//   - a day file or segment once its month's file exists (a merge that
//     stopped before deleting its days left both; the month file holds them);
//   - a claimed log (<target>.dumping) once its target exists (a dump that
//     stopped before deleting its log).
//
// A .tmp file (a write in progress), any other name and a project with
// neither events file nor log are left out. A missing raw/ lists nothing;
// a project directory that vanishes while listing (a purge) is skipped.
func ListFiles(dir string) (map[int64]Files, error) {
	return listFiles(dir, func(int64) bool { return true })
}

// listFiles is ListFiles of the projects keep keeps.
func listFiles(dir string, keep func(int64) bool) (map[int64]Files, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[int64]Files{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	out := map[int64]Files{}
	for _, e := range entries {
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil || !e.IsDir() || !keep(id) {
			continue
		}
		pdir := filepath.Join(dir, e.Name())
		names, err := list(pdir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		pf, err := projectOf(pdir, names)
		if err != nil {
			return nil, err
		}
		if len(pf.Parquet)+len(pf.Logs) > 0 {
			out[id] = pf.Files
		}
	}
	return out, nil
}

// projectOf is one project's directory, whose entries are names (list),
// as ListFiles describes it, with what its derived files need. A file that
// vanishes while it looks (the pass at work) fails it with an error that
// wraps os.ErrNotExist, which a reader retries.
func projectOf(pdir string, names []string) (projectFiles, error) {
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	pf := projectFiles{deriveDays: map[string][]string{}, deriveMonths: map[string]string{}}
	days := map[string][]string{}    // a day not merged: its day file and segments
	monthFile := map[string]string{} // a merged month: its file
	logDays := map[string]bool{}
	for _, n := range names {
		p := filepath.Join(pdir, n)
		switch {
		case monthFileName.MatchString(n):
			pf.Parquet = append(pf.Parquet, p)
			monthFile[n[:7]] = p
		case dayFileName.MatchString(n):
			// A month merges only once all its days are dumped and past the
			// late window, so its file holds every day file beside it.
			if day := n[:10]; !have[day[:7]+".parquet"] {
				pf.Parquet = append(pf.Parquet, p)
				days[day] = append(days[day], p)
			}
		case logName.MatchString(n), closedName.MatchString(n):
			pf.Logs = append(pf.Logs, p)
			logDays[n[:10]] = true
		case dumpingName.MatchString(n):
			if !have[strings.TrimSuffix(n, ".dumping")] {
				pf.Logs = append(pf.Logs, p)
				logDays[n[:10]] = true
			}
		}
	}
	return pf, pf.derived(pdir, have, days, monthFile, logDays)
}

// derived fills in the derived files' side of pf (see Files and projectFiles).
func (pf *projectFiles) derived(pdir string, have map[string]bool, days map[string][]string, monthFile map[string]string, logDays map[string]bool) error {
	sdir := filepath.Join(pdir, sessionsDir)
	snames, err := list(sdir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var gaps []Span
	for _, month := range keysOf(monthFile) {
		s := filepath.Join(sdir, month+".parquet")
		ok, err := current(s, []string{monthFile[month]})
		if err != nil {
			return err
		}
		// A log in a merged month should not happen (a month merges once no
		// log is left and the late window is past); if one does, the month
		// is read from its rows, not from a file that misses them.
		late := false
		for day := range logDays {
			late = late || strings.HasPrefix(day, month+"-")
		}
		switch {
		case ok && !late:
			pf.Sessions = append(pf.Sessions, s)
		default:
			gaps = append(gaps, Span{month + "-01", lastDay(month).String()})
			if !ok && !late {
				pf.deriveMonths[month] = monthFile[month]
			}
		}
	}
	for _, day := range keysOf(days) {
		if logDays[day] {
			gaps = append(gaps, Span{day, day})
			continue
		}
		s := filepath.Join(sdir, day+".parquet")
		ok, err := current(s, days[day])
		if err != nil {
			return err
		}
		if ok {
			pf.Sessions = append(pf.Sessions, s)
		} else {
			gaps = append(gaps, Span{day, day})
			pf.deriveDays[day] = days[day]
		}
	}
	for day := range logDays {
		if _, ok := days[day]; !ok && monthFile[day[:7]] == "" {
			gaps = append(gaps, Span{day, day})
		}
	}
	pf.SessionGaps = joinSpans(gaps)
	for _, n := range snames {
		p := filepath.Join(sdir, n)
		switch {
		case monthFileName.MatchString(n):
			if monthFile[n[:7]] == "" {
				pf.orphans = append(pf.orphans, p)
			}
		case dayFileName.MatchString(n) && len(n) == len("2006-01-02.parquet"):
			if _, ok := days[n[:10]]; !ok {
				pf.orphans = append(pf.orphans, p)
			}
		}
	}
	for _, x := range []struct {
		name string
		to   *string
	}{{aliasesFile, &pf.Aliases}, {identitiesFile, &pf.Identities}} {
		p := filepath.Join(pdir, x.name)
		if len(pf.Parquet) == 0 {
			if have[x.name] {
				pf.orphans = append(pf.orphans, p)
			}
			continue
		}
		ok, err := current(p, pf.Parquet)
		if err != nil {
			return err
		}
		if ok {
			*x.to = p
		} else {
			pf.identitiesDue = true
		}
	}
	return nil
}

// current reports whether derived exists and no input is newer than it.
func current(derived string, inputs []string) (bool, error) {
	d, err := os.Stat(derived)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("archive: %w", err)
	}
	for _, in := range inputs {
		st, err := os.Stat(in)
		if err != nil {
			return false, fmt.Errorf("archive: %w", err)
		}
		if st.ModTime().After(d.ModTime()) {
			return false, nil
		}
	}
	return true, nil
}

// joinSpans sorts spans and joins those that overlap or touch.
func joinSpans(spans []Span) []Span {
	slices.SortFunc(spans, func(a, b Span) int { return strings.Compare(a.From, b.From) })
	var out []Span
	for _, s := range spans {
		if n := len(out); n > 0 && s.From <= nextDay(out[n-1].To) {
			out[n-1].To = max(out[n-1].To, s.To)
			continue
		}
		out = append(out, s)
	}
	return out
}

func nextDay(day string) string {
	d, err := civil.Parse(day)
	if err != nil {
		return day
	}
	return d.AddDays(1).String()
}

// keysOf is m's keys in order.
func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// LatestDays is each project's newest day with events in dir (YYYY-MM-DD),
// from the file names alone: a month file counts as its last day, a day
// file, segment, log or claimed log as its day. A project with none is
// left out, as is a missing dir's; a project directory that vanishes
// while listing (a purge) is skipped.
func LatestDays(dir string) (map[int64]string, error) {
	return projectDays(dir, func(name string) string { return dayOf(name, true) },
		func(day, cur string) bool { return day > cur })
}

// OldestDays is LatestDays' other end: each project's oldest day with
// events in dir, where a month file counts as its first day (the names
// say no more; its first event may be later).
func OldestDays(dir string) (map[int64]string, error) {
	return projectDays(dir, func(name string) string { return dayOf(name, false) },
		func(day, cur string) bool { return cur == "" || day < cur })
}

// projectDays is, for each project directory in dir, the day of its file
// names (day) that wins over every other (better), a name with no day
// left out.
func projectDays(dir string, day func(string) string, better func(day, cur string) bool) (map[int64]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[int64]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	out := map[int64]string{}
	for _, e := range entries {
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil || !e.IsDir() {
			continue
		}
		names, err := list(filepath.Join(dir, e.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			if d := day(n); d != "" && better(d, out[id]) {
				out[id] = d
			}
		}
	}
	return out, nil
}

// dayOf is the day name holds events of, or "" when it is no events file
// or log: for a month file, its last day (last) or its first.
func dayOf(name string, last bool) string {
	if m := monthFileName.FindStringSubmatch(name); m != nil {
		first, err := civil.Parse(m[1] + "-01")
		if err != nil {
			return ""
		}
		if !last {
			return first.String()
		}
		return first.Time().AddDate(0, 1, -1).Format("2006-01-02")
	}
	for _, re := range []*regexp.Regexp{dayFileName, logName, closedName, dumpingName} {
		if m := re.FindStringSubmatch(name); m != nil {
			return m[1]
		}
	}
	return ""
}
