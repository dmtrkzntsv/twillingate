package readsql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// ScopedSchema is the name the database file is attached under on a
// scoped handle. Check refuses it on that handle wherever a name can
// appear, so custom SQL reaches the file only through the temporary views
// OpenScoped creates.
const ScopedSchema = "tw_store"

// scopedDSN opens each scoped connection on its own empty in-memory
// database, _defensive as Open's are. query_only is set by Connect once
// the temporary views exist, since it would refuse creating them.
const scopedDSN = "file::memory:?_pragma=busy_timeout(5000)&_defensive=1"

// OpenScoped opens a handle for widget SQL (events encoding spec D15).
// Each connection is an empty in-memory database with the file at path
// attached read-only as ScopedSchema, and one temporary view per table or
// view of the file, after which the connection is query_only:
//
//   - a raw_*, v_* or agg_* object x reads through
//     CREATE TEMP VIEW x AS SELECT * FROM tw_store.x. The file's own views
//     resolve inside their schema, a filter on x reaches the rows under
//     it as it would on the file, and SELECT * follows column changes
//     (v_events_flat's rebuilds);
//   - any other one (events, identities, projects…) is shadowed by a
//     temporary view that fails to prepare with "no such table: main.x".
//     SQLite looks an unqualified name up in temp, then main, then the
//     attached schemas, so without the shadow events would still find
//     tw_store.events; with it, the only way to the file's other objects
//     is the qualified tw_store.x, which Check refuses.
//
// SQLite's own tables (sqlite_*) cannot be shadowed and are refused by
// Check by name, as on every handle. A pooled connection whose file
// schema changed since it was scoped (a table or view created, dropped or
// rebuilt, by this process or another) is retired before its next use and
// a fresh one scoped (scopedConn), so a new private table is shadowed too.
//
// refused names further tables this handle's Check refuses, as Open's
// does; ScopedSchema is always among them.
func OpenScoped(path string, timeout time.Duration, maxRows int, refused ...string) (*DB, error) {
	base, err := sqlite.NewConnector(scopedDSN)
	if err != nil {
		return nil, fmt.Errorf("readsql: open %s: %w", path, err)
	}
	db := sql.OpenDB(&scopedConnector{Connector: base, path: path})
	db.SetMaxOpenConns(4)
	// Making a connection runs a statement per object of the file (about
	// 20ms): keep every one open rather than database/sql's default two.
	db.SetMaxIdleConns(4)
	names := map[string]bool{strings.ToLower(ScopedSchema): true}
	for _, n := range refused {
		names[strings.ToLower(n)] = true
	}
	return &DB{db: db, timeout: timeout, maxRows: maxRows, refused: names}, nil
}

// scopedConnector is the driver's own connector (so the functions this
// package registers, tw_cell and tw_num, reach its connections) with the
// attach and the temporary views run on every new connection.
type scopedConnector struct {
	driver.Connector
	path string
}

// sqliteConn is the driver connection's methods database/sql uses, so
// scopedConn can wrap one without hiding any of them.
type sqliteConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
	driver.Pinger
	driver.SessionResetter
	driver.Validator
}

// scopedConn is a scoped connection and the file's schema version its
// views were made against. The shadows are made per connection, so a table
// created in the file afterwards (another process's migration, a CLI run)
// would resolve straight to the file on it: ResetSession, which
// database/sql calls before reusing a pooled connection, retires one whose
// file schema changed, and the next Connect scopes a fresh one. A change
// that lands while a statement is already running is seen at the next
// reuse.
type scopedConn struct {
	sqliteConn
	version int64
}

func (c *scopedConn) ResetSession(ctx context.Context) error {
	if err := c.sqliteConn.ResetSession(ctx); err != nil {
		return err
	}
	v, err := schemaVersion(ctx, c.sqliteConn)
	if err != nil || v != c.version {
		return driver.ErrBadConn
	}
	return nil
}

func (c *scopedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	raw, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	conn, ok := raw.(sqliteConn)
	if !ok {
		raw.Close()
		return nil, fmt.Errorf("readsql: driver connection %T lacks a method database/sql uses", raw)
	}
	version, err := c.scope(ctx, conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("readsql: scope %s: %w", c.path, err)
	}
	return &scopedConn{sqliteConn: conn, version: version}, nil
}

// schemaVersion is the attached file's schema cookie, which SQLite bumps
// on every CREATE, DROP or ALTER in it.
func schemaVersion(ctx context.Context, q driver.QueryerContext) (int64, error) {
	rows, err := q.QueryContext(ctx, fmt.Sprintf(`PRAGMA %s.schema_version`, ScopedSchema), nil)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	vals := make([]driver.Value, 1)
	if err := rows.Next(vals); err != nil {
		return 0, err
	}
	v, ok := vals[0].(int64)
	if !ok {
		return 0, fmt.Errorf("schema_version is %T", vals[0])
	}
	return v, nil
}

// scope attaches the file and makes the temporary views, returning the
// file's schema version read before the objects were listed: a change
// between the two then retires the connection at its first reuse.
func (c *scopedConnector) scope(ctx context.Context, eq sqliteConn) (int64, error) {
	run := func(q string, args ...driver.NamedValue) error {
		_, err := eq.ExecContext(ctx, q, args)
		return err
	}
	// The filename is bound, not spliced; the connection was opened with
	// URI filenames on, so mode=ro applies to the attached file.
	if err := run(`ATTACH DATABASE ? AS `+ScopedSchema,
		driver.NamedValue{Ordinal: 1, Value: "file:" + c.path + "?mode=ro"}); err != nil {
		return 0, err
	}
	version, err := schemaVersion(ctx, eq)
	if err != nil {
		return 0, err
	}
	// Every object of the file whatever its type (table, view, virtual,
	// or a virtual table's shadow tables) but SQLite's own, which Check
	// refuses by name and no temporary object may shadow. The query
	// names no schema, so sqlcheck prepares it.
	rows, err := eq.QueryContext(ctx, `SELECT name FROM pragma_table_list
		WHERE schema = ? AND name NOT LIKE 'sqlite\_%' ESCAPE '\'
		ORDER BY name`, []driver.NamedValue{{Ordinal: 1, Value: ScopedSchema}})
	if err != nil {
		return 0, err
	}
	var names []string
	vals := make([]driver.Value, 1)
	for {
		err := rows.Next(vals)
		if err == io.EOF {
			break
		}
		if err != nil {
			rows.Close()
			return 0, err
		}
		names = append(names, fmt.Sprint(vals[0]))
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	var hidden []string
	for _, n := range names {
		// Every object name in the file was written by a migration or
		// RebuildFlatView, all [a-z0-9_]; anything else is refused rather
		// than escaped, so the quoting below is only against keywords.
		if !plainName(n) {
			return 0, fmt.Errorf("object name %q is not [a-z0-9_]", n)
		}
		if scopedAllowed(n) {
			if err := run(fmt.Sprintf(`CREATE TEMP VIEW "%s" AS SELECT * FROM %s."%s"`, n, ScopedSchema, n)); err != nil {
				return 0, err
			}
			continue
		}
		// The shadow (see OpenScoped): a temporary view over a main table
		// dropped below. CREATE VIEW checks the tables it reads; DROP TABLE
		// does not check the views that read one, so the view stays and
		// fails to prepare.
		if err := run(fmt.Sprintf(`CREATE TABLE main."%s" (x)`, n)); err != nil {
			return 0, err
		}
		if err := run(fmt.Sprintf(`CREATE TEMP VIEW "%s" AS SELECT * FROM main."%s"`, n, n)); err != nil {
			return 0, err
		}
		hidden = append(hidden, n)
	}
	for _, n := range hidden {
		if err := run(fmt.Sprintf(`DROP TABLE main."%s"`, n)); err != nil {
			return 0, err
		}
	}
	return version, run(`PRAGMA query_only = 1`)
}

// scopedAllowed reports whether widget SQL may read the file's object n.
func scopedAllowed(n string) bool {
	return strings.HasPrefix(n, "raw_") || strings.HasPrefix(n, "v_") || strings.HasPrefix(n, "agg_")
}

func plainName(n string) bool {
	if n == "" {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}
