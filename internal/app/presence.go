package app

import (
	"context"
	"strconv"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// filePresence is manage.Presence over the event files: whether a rename's
// target already has events, or values, in the project's whole history,
// with the registry's rules as they stand when asked. A name another rule
// maps there therefore has events, and a rename onto it is a merge.
type filePresence struct {
	reader *archive.Reader
	reg    *manage.Registry
	// timeout bounds each query (CONSOLE_QUERY_TIMEOUT); 0 is none.
	timeout time.Duration
}

var _ manage.Presence = (*filePresence)(nil)

// hasEventsSQL is one event of the family under name in the project.
const hasEventsSQL = `SELECT 1 FROM events WHERE project_id = CAST($p AS BIGINT) AND family = $f AND event_name = $n LIMIT 1`

func (p *filePresence) HasEvents(ctx context.Context, projectID int64, family store.Family, name string) (bool, error) {
	res, err := p.query(ctx, hasEventsSQL, engine.Arg{Name: "p", Value: strconv.FormatInt(projectID, 10)},
		engine.Arg{Name: "f", Value: string(family)}, engine.Arg{Name: "n", Value: name})
	return len(res.Rows) > 0, err
}

// HasAttribute looks for a value of key in the project's events. The
// files hold a key as the column attr_<key>, which exists once any
// project sent it: no column is no value.
func (p *filePresence) HasAttribute(ctx context.Context, projectID int64, key string) (bool, error) {
	res, err := p.query(ctx, "SELECT column_name FROM (DESCRIBE SELECT * FROM events)")
	if err != nil {
		return false, err
	}
	cols := make([]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		if c, ok := row[0].(string); ok {
			cols = append(cols, c)
		}
	}
	col, ok := enginesql.AttrColumn(cols, key)
	if !ok {
		return false, nil
	}
	res, err = p.query(ctx, "SELECT 1 FROM events WHERE project_id = CAST($p AS BIGINT) AND "+col+" IS NOT NULL LIMIT 1",
		engine.Arg{Name: "p", Value: strconv.FormatInt(projectID, 10)})
	return len(res.Rows) > 0, err
}

func (p *filePresence) query(ctx context.Context, sql string, args ...engine.Arg) (engine.Result, error) {
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	events, attrs := p.reg.Snapshot(ctx).RuleLists()
	return p.reader.QueryScoped(ctx, archive.Scoped{SQL: sql, Args: args},
		archive.Rules{Events: events, Attributes: attrs})
}
