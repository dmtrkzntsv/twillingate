package enginesql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

var sevenTables = []string{"events", "views", "product", "measures", "sessions", "identities", "aliases"}

// TestConfineRefusesWhatReadsPastTheTables: a table function that takes a
// path, a file named in FROM, a schema- or catalog-qualified name, an
// object of the catalog that is none of the tables (the reader's own
// _tw_* among them), SHOW over the catalog, and a WITH name used where it
// is not in scope (its own body, an earlier sibling's, a subquery's
// outside it), each in any case, quoted or not, behind comments, in a
// subquery, a CTE, a join or a set operation, are refused as ErrRefused
// with a message naming the thing refused.
func TestConfineRefusesWhatReadsPastTheTables(t *testing.T) {
	e := newEngine(t)
	for _, c := range []struct{ sql, want string }{
		{`SELECT * FROM read_parquet('/etc/passwd')`, "read_parquet()"},
		{`SELECT * FROM READ_CSV_AUTO('/etc/passwd')`, "read_csv_auto()"},
		{`SELECT * FROM read_json('x.json')`, "read_json()"},
		{`SELECT * FROM glob('/*')`, "glob()"},
		{`SELECT * FROM read_text('/etc/hostname')`, "read_text()"},
		{`SELECT * FROM query('SELECT 1')`, "query()"},
		{`SELECT * FROM duckdb_tables()`, "duckdb_tables()"},
		{`SELECT * FROM main.range(3)`, "main.range()"},
		{`SELECT * FROM '/etc/passwd'`, `"/etc/passwd"`},
		{`FROM 'raw/1/2026-10.parquet'`, `"raw/1/2026-10.parquet"`},
		{`SELECT * FROM main.events`, "main.events"},
		{`SELECT * FROM "main"."events"`, "main.events"},
		{`SELECT * FROM MAIN . Events`, "MAIN.Events"},
		{`SELECT * FROM memory.main.events`, "memory.main.events"},
		{`SELECT * FROM information_schema.tables`, "information_schema.tables"},
		{`SELECT * FROM _tw_all`, `"_tw_all"`},
		{`SELECT * FROM "_TW_HOT"`, `"_TW_HOT"`},
		{`SELECT * FROM duckdb_settings`, `"duckdb_settings"`},
		{`SHOW TABLES`, "SHOW"},
		{`SHOW ALL TABLES`, "SHOW"},
		{`SELECT 1 /* FROM events */ FROM -- events
			read_parquet('/etc/passwd')`, "read_parquet()"},
		{`SELECT count(*) FROM events WHERE id IN (SELECT id FROM _tw_hot)`, `"_tw_hot"`},
		{`SELECT (SELECT max(day) FROM read_csv('/etc/passwd')) FROM events`, "read_csv()"},
		{`SELECT * FROM events e JOIN main.sessions s USING (project_id)`, "main.sessions"},
		{`SELECT id FROM events UNION ALL SELECT name FROM _tw_state`, `"_tw_state"`},
		{`SELECT * FROM events, LATERAL (SELECT * FROM glob(events.id))`, "glob()"},
		{`DESCRIBE SELECT * FROM _tw_all`, `"_tw_all"`},
		{`SUMMARIZE main.events`, "main.events"},
		// A WITH name reads the catalog where it is not in scope.
		{`WITH _tw_all AS (SELECT * FROM _tw_all) SELECT * FROM _tw_all`, `"_tw_all"`},
		{`WITH a AS (SELECT * FROM b), b AS (SELECT 1) SELECT * FROM a`, `"b"`},
		{`SELECT * FROM (WITH x AS (SELECT 1) SELECT * FROM x), x`, `"x"`},
		{`WITH x AS (SELECT * FROM read_parquet('/etc/passwd')) SELECT * FROM x`, "read_parquet()"},
	} {
		err := Confine(context.Background(), e, c.sql, sevenTables)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("Confine(%q) = %v; want ErrRefused", c.sql, err)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("Confine(%q) = %q; want it to name %s", c.sql, err, c.want)
		}
	}
}

// TestConfineAcceptsSQLOverTheTables: the seven tables in any case, plain
// or quoted, their own WITH names (recursive ones too, and one named like
// a table, which reads it), subqueries, joins, set operations, VALUES,
// DESCRIBE and SUMMARIZE of a table, the generator table functions, and a
// path or a schema's name inside a string compared as text: none of them
// reads a file or another table.
func TestConfineAcceptsSQLOverTheTables(t *testing.T) {
	e := newEngine(t)
	for _, sql := range []string{
		`SELECT 1`,
		`SELECT count(*) FROM events`,
		`SELECT * FROM "Events" e JOIN VIEWS v USING (id) JOIN product p ON p.id = e.id`,
		`SELECT * FROM measures, sessions, identities, aliases`,
		`SELECT count(*) FROM events WHERE kind = '/etc/passwd' OR event_name = 'main.events' OR id = 'read_parquet(''x'')'`,
		`SELECT 'SELECT * FROM read_parquet(''/etc/passwd'')' AS text`,
		`-- read_parquet('/etc/passwd')
		SELECT /* main.events */ day FROM views`,
		`WITH events AS (SELECT * FROM events WHERE family = 'views') SELECT count(*) FROM events`,
		`WITH a AS (SELECT * FROM views), b AS (SELECT * FROM a) SELECT * FROM b`,
		`WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r WHERE n < 3) SELECT * FROM r`,
		`WITH x AS (SELECT 1 AS n) SELECT * FROM x WHERE n IN (SELECT n FROM x)`,
		`SELECT * FROM (WITH x AS (SELECT * FROM product) SELECT * FROM x) y`,
		`SELECT day FROM views UNION ALL SELECT day FROM product EXCEPT SELECT day FROM measures`,
		`SELECT * FROM range(0, 46) AS o(day_offset), generate_series(1, 3), unnest([1, 2])`,
		`SELECT * FROM events, LATERAL (SELECT * FROM unnest([events.id]))`,
		`SELECT (SELECT max(day) FROM views), EXISTS (FROM product) FROM sessions`,
		`VALUES (1), (2)`,
		`FROM events`,
		`DESCRIBE events`,
		`DESCRIBE SELECT * FROM views`,
		`SUMMARIZE measures`,
		`SELECT tw_cell(COLUMNS(*)) FROM (SELECT * FROM events LIMIT 0)`,
		`SELECT * FROM events WHERE day BETWEEN CAST($tw_from AS DATE) AND CAST($tw_to AS DATE)`,
	} {
		if err := Confine(context.Background(), e, sql, sevenTables); err != nil {
			t.Errorf("Confine(%q) = %v; want nil", sql, err)
		}
	}
}

// TestConfineRefusesWhatDoesNotParse as the child does: a syntax error is
// DuckDB's own (an engine.QueryError, which callers word as before), and
// anything but one SELECT is refused, so no text the check could not read
// reaches the child.
func TestConfineRefusesWhatDoesNotParse(t *testing.T) {
	e := newEngine(t)
	for _, c := range []struct{ sql, want string }{
		{`SELEC 1`, "syntax error"},
		{`SELECT 1; SELECT 2`, "one SELECT"},
		{`COPY events TO '/tmp/x.csv'`, "one SELECT"},
		{`PIVOT events ON kind USING count(*)`, "one SELECT"},
		{`ATTACH '/tmp/x.db'`, "one SELECT"},
	} {
		err := Confine(context.Background(), e, c.sql, sevenTables)
		var qe *engine.QueryError
		if !errors.As(err, &qe) || !strings.Contains(qe.Msg, c.want) {
			t.Errorf("Confine(%q) = %v; want an engine.QueryError saying %q", c.sql, err, c.want)
		}
	}
}
