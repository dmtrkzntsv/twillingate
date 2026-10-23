package app

// The DuckDB presence oracle in this file leaves with DuckDB in step 6.

import (
	"context"
	"strconv"

	"github.com/dmtrkzntsv/twillingate/internal/archive"
	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/shared/enginesql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// sqlPresence is filePresence as it was before the native engine answered
// it, through the engine child: the reference the native checks are
// compared with.
type sqlPresence struct {
	reader *archive.Reader
	reg    *manage.Registry
}

// hasEventsSQL is one event of the family under name in the project.
const hasEventsSQL = `SELECT 1 FROM events WHERE project_id = CAST($p AS BIGINT) AND family = $f AND event_name = $n LIMIT 1`

func (p *sqlPresence) HasEvents(ctx context.Context, projectID int64, family store.Family, name string) (bool, error) {
	res, err := p.query(ctx, hasEventsSQL, engine.Arg{Name: "p", Value: strconv.FormatInt(projectID, 10)},
		engine.Arg{Name: "f", Value: string(family)}, engine.Arg{Name: "n", Value: name})
	return len(res.Rows) > 0, err
}

func (p *sqlPresence) HasAttribute(ctx context.Context, projectID int64, key string) (bool, error) {
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

func (p *sqlPresence) query(ctx context.Context, sql string, args ...engine.Arg) (engine.Result, error) {
	events, attrs := p.reg.Snapshot(ctx).RuleLists()
	return p.reader.QueryScoped(ctx, archive.Scoped{SQL: sql, Args: args},
		archive.Rules{Events: events, Attributes: attrs})
}
