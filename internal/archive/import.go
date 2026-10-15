package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// ImportDay writes the events of evs whose id the project's files and logs
// do not hold for day as a closed log of that day (Log.WriteClosed), and
// returns how many it wrote: the migration's step for one project and day
// (raw-first D19). The next pass claims and dumps the log like any other.
// If the day's month is merged, the month file is merged again with it.
// The same import again writes nothing, because the closed log holds its
// ids. evs must all be the project's and of day.
func (a *Archive) ImportDay(ctx context.Context, project int64, day string, evs []store.Event) (int, error) {
	if len(evs) == 0 {
		return 0, nil
	}
	held, err := a.heldIDs(ctx, project, day)
	if err != nil {
		return 0, err
	}
	var missing []store.Event
	for _, e := range evs {
		if !held[e.ID] {
			missing = append(missing, e)
		}
	}
	if _, err := a.log.WriteClosed(project, day, missing); err != nil {
		return 0, err
	}
	return len(missing), nil
}

// heldIDs returns the ids the project holds for day, as ListFiles lists the
// files: the day file and its segments (or the month file, filtered on
// day, once the month is merged) and the day's logs, open, closed or
// claimed. A day with none of them costs no query. An open log's torn last
// line is skipped.
func (a *Archive) heldIDs(ctx context.Context, project int64, day string) (map[string]bool, error) {
	held := map[string]bool{}
	dir := filepath.Join(a.log.Dir(), strconv.FormatInt(project, 10))
	names, err := list(dir)
	if errors.Is(err, os.ErrNotExist) {
		return held, nil
	}
	if err != nil {
		return nil, err
	}
	pf, err := projectOf(dir, names)
	if err != nil {
		return nil, err
	}
	var parquet, logs []string
	for _, p := range pf.Parquet {
		if b := filepath.Base(p); strings.HasPrefix(b, day+".") || b == day[:7]+".parquet" {
			parquet = append(parquet, p)
		}
	}
	for _, p := range pf.Logs {
		if strings.HasPrefix(filepath.Base(p), day+".") {
			logs = append(logs, p)
		}
	}
	var parts []string
	if len(parquet) > 0 {
		parts = append(parts, "SELECT id FROM read_parquet("+sqlList(parquet)+", union_by_name=true) WHERE day = DATE "+quote(day))
	}
	if len(logs) > 0 {
		parts = append(parts, "SELECT json_extract_string(json, '/id') AS id FROM read_json_objects("+sqlList(logs)+
			", format='newline_delimited', ignore_errors=true) WHERE json IS NOT NULL")
	}
	if len(parts) == 0 {
		return held, nil
	}
	res, err := a.eng.Query(ctx, strings.Join(parts, " UNION ALL "), 0)
	if err != nil {
		return nil, fmt.Errorf("archive: ids of project %d's %s: %w", project, day, err)
	}
	for _, row := range res.Rows {
		if id, ok := row[0].(string); ok {
			held[id] = true
		}
	}
	return held, nil
}
