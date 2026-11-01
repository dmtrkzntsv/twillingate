package native

import (
	"path/filepath"
	"regexp"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/shared/civil"
)

// The day or month in an events file's name (archive's names): a month
// file YYYY-MM.parquet; a day file or segment YYYY-MM-DD(.n).parquet, a
// log YYYY-MM-DD.log or .n.closed and a claimed log ….parquet.dumping all
// start with their day.
var (
	monthName = regexp.MustCompile(`^(\d{4}-\d{2})\.parquet$`)
	dayName   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.`)
)

// filesIn is f's Parquet files and logs that may hold rows in r.
func filesIn(f archive.Files, r dayRange) (parquet, logs []string) {
	for _, path := range f.Parquet {
		if overlaps(filepath.Base(path), r) {
			parquet = append(parquet, path)
		}
	}
	for _, path := range f.Logs {
		if overlaps(filepath.Base(path), r) {
			logs = append(logs, path)
		}
	}
	return parquet, logs
}

// sessionSources is what project p's sessions in r are read from, from
// archive.ListFiles(dir)[p]: the current session files whose day or month
// overlaps r, the days of r no current session file covers (its
// SessionGaps), in order, and the project's events files, which those
// days' sessions are derived from.
func sessionSources(dir string, p int64, r dayRange) (files []string, gaps []civil.Date, events archive.Files, err error) {
	all, err := archive.ListFiles(dir)
	if err != nil {
		return nil, nil, archive.Files{}, err
	}
	events = all[p]
	for _, path := range events.Sessions {
		if overlaps(filepath.Base(path), r) {
			files = append(files, path)
		}
	}
	for _, g := range events.SessionGaps {
		from, err := civil.Parse(g.From)
		if err != nil {
			return nil, nil, archive.Files{}, err
		}
		to, err := civil.Parse(g.To)
		if err != nil {
			return nil, nil, archive.Files{}, err
		}
		if from.Before(r.From) {
			from = r.From
		}
		if r.To.Before(to) {
			to = r.To
		}
		for d := from; !to.Before(d); d = d.AddDays(1) {
			gaps = append(gaps, d)
		}
	}
	return files, gaps, events, nil
}

// overlaps says whether the file named name may hold rows in r: a month
// file when any day of its month is in r, any other when its day is. A
// name it cannot read is kept, since the rows are filtered by day anyway.
func overlaps(name string, r dayRange) bool {
	if m := monthName.FindStringSubmatch(name); m != nil {
		first, err := civil.Parse(m[1] + "-01")
		if err != nil {
			return true
		}
		last := civil.DateOf(first.Time().AddDate(0, 1, -1))
		return !last.Before(r.From) && !r.To.Before(first)
	}
	if m := dayName.FindStringSubmatch(name); m != nil {
		d, err := civil.Parse(m[1])
		if err != nil {
			return true
		}
		return !d.Before(r.From) && !r.To.Before(d)
	}
	return true
}
