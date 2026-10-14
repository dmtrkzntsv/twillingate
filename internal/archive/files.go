package archive

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var monthFileName = regexp.MustCompile(`^(\d{4}-\d{2})\.parquet$`)

// Files is what a reader sees of one project's directory: the Parquet
// files that hold its final rows and the logs that hold the rest (open,
// rotated, or claimed by a dump not done yet), each in name order.
type Files struct {
	Parquet []string
	Logs    []string
}

// ListFiles lists raw/ for a reader, by project id, leaving out what the
// daily pass would make it count twice:
//   - a day file or segment once its month's file exists (a merge that
//     stopped before deleting its days left both; the month file holds them);
//   - a claimed log (<target>.dumping) once its target exists (a dump that
//     stopped before deleting its log).
//
// A .tmp file (a write in progress), any other name and a project with
// nothing listed are left out. A missing raw/ lists nothing; a project
// directory that vanishes while listing (a purge) is skipped.
func ListFiles(dir string) (map[int64]Files, error) {
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
		if err != nil || !e.IsDir() {
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
		have := map[string]bool{}
		for _, n := range names {
			have[n] = true
		}
		var f Files
		for _, n := range names {
			p := filepath.Join(pdir, n)
			switch {
			case monthFileName.MatchString(n):
				f.Parquet = append(f.Parquet, p)
			case dayFileName.MatchString(n):
				// A month merges only once all its days are dumped and past the
				// late window, so its file holds every day file beside it.
				if !have[dayFileName.FindStringSubmatch(n)[1][:7]+".parquet"] {
					f.Parquet = append(f.Parquet, p)
				}
			case logName.MatchString(n), closedName.MatchString(n):
				f.Logs = append(f.Logs, p)
			case dumpingName.MatchString(n):
				if !have[strings.TrimSuffix(n, ".dumping")] {
					f.Logs = append(f.Logs, p)
				}
			}
		}
		if len(f.Parquet)+len(f.Logs) > 0 {
			out[id] = f
		}
	}
	return out, nil
}
