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

// projectFiles returns the Parquet files and the logs of project p that
// may hold rows in r, from archive.ListFiles(dir)[p] (which leaves out what
// a reader would count twice) and the files' day or month names, each in
// name order. A project with no files under dir, or a missing dir, has none.
func projectFiles(dir string, p int64, r Range) (parquet, logs []string, err error) {
	all, err := archive.ListFiles(dir)
	if err != nil {
		return nil, nil, err
	}
	f := all[p]
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
	return parquet, logs, nil
}

// overlaps says whether the file named name may hold rows in r: a month
// file when any day of its month is in r, any other when its day is. A
// name it cannot read is kept, since the rows are filtered by day anyway.
func overlaps(name string, r Range) bool {
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
