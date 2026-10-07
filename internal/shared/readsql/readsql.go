// Package readsql is the one path custom SQL takes to the database (spec
// D46–47). It layers four guards, each independent of the others so a
// bug in one does not open the database to the next:
//
//  1. the connection pool is opened mode=ro with query_only and
//     _defensive pragmas, so a write cannot succeed even through a bug
//     elsewhere in this package;
//  2. Check tokenizes the text and refuses ATTACH, any read of meta or a
//     SQLite internal table or pragma view (and any further table names
//     the handle was opened to refuse), and a second statement —
//     the driver runs every statement it is given, so Check tracks paren
//     depth and statement boundaries itself rather than relying on the
//     wrap below to contain one;
//  3. QueryLimit (which Query is, at this DB's own row cap) wraps the
//     checked text as a subquery with a caller-chosen LIMIT in the same
//     clause, so a single statement that is not itself a query — DDL,
//     PRAGMA — becomes a syntax error instead of executing (QueryPage
//     extends the same wrap with filters, an order and counts, after the
//     same Check);
//  4. Run enforces a deadline on every query, custom or not.
package readsql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	// ErrRefused reports text Check refused (see Check for the rules and
	// why each exists), or empty text or a negative limit refused by
	// QueryLimit. The wrapped message names what to change.
	ErrRefused = errors.New("refused")
	// ErrTimeout reports that a query's deadline passed before it finished.
	ErrTimeout = errors.New("query timed out")
)

// Result is a query's answer, shaped for JSON: Rows is a matrix of
// strings, a NULL reading as "".
type Result struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated"`
}

// DB is a read-only handle on one SQLite file, with its own deadline and
// row cap applied to every query it runs.
type DB struct {
	db      *sql.DB
	timeout time.Duration
	maxRows int
	// refused holds the lower-cased table names Check refuses on this
	// handle beyond its base rules; nil for none.
	refused map[string]bool
}

// Open opens path read-only with its own connection pool, independent of
// any single-writer connection to the same file. query_only and
// _defensive are belt over the mode=ro brace: even a bug that finds a
// writable path is refused by the connection itself. refused names
// further tables this handle's Check refuses, on top of the base rules
// (meta and SQLite's internals), so a caller can keep a table out of
// custom SQL without this package knowing about it.
func Open(path string, timeout time.Duration, maxRows int, refused ...string) (*DB, error) {
	dsn := "file:" + path + "?mode=ro" +
		"&_pragma=query_only(1)" +
		"&_pragma=busy_timeout(5000)" +
		"&_defensive=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("readsql: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(4)
	var names map[string]bool
	for _, n := range refused {
		if names == nil {
			names = map[string]bool{}
		}
		names[strings.ToLower(n)] = true
	}
	return &DB{db: db, timeout: timeout, maxRows: maxRows, refused: names}, nil
}

// Close closes the underlying connection pool.
func (d *DB) Close() error { return d.db.Close() }

// Timeout is the deadline applied to every query.
func (d *DB) Timeout() time.Duration { return d.timeout }

// MaxRows is the row cap applied to every query.
func (d *DB) MaxRows() int { return d.maxRows }

// Run executes SQL written in Go — trusted text, not passed through
// Check — applying only the deadline and the row cap.
func (d *DB) Run(ctx context.Context, q string, args ...any) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Result{}, wrapTimeout(ctx, d.timeout, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return Result{}, wrapTimeout(ctx, d.timeout, err)
	}
	// Non-nil even with zero matches: the web app's WidgetCard reads
	// data.rows.length unconditionally (a query that matches nothing is
	// the ordinary "no data for this range" case, not an absent field),
	// and a nil slice would encode as JSON null rather than [].
	out := [][]string{}
	truncated := false
	for rows.Next() {
		if len(out) == d.maxRows {
			truncated = true
			break
		}
		vals := make([]any, len(cols))
		for i := range vals {
			vals[i] = new(sql.NullString)
		}
		if err := rows.Scan(vals...); err != nil {
			return Result{}, wrapTimeout(ctx, d.timeout, err)
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			ns := v.(*sql.NullString)
			if ns.Valid {
				row[i] = ns.String
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return Result{}, wrapTimeout(ctx, d.timeout, err)
	}
	return Result{Columns: cols, Rows: out, Truncated: truncated}, nil
}

// wrapTimeout maps a context deadline to ErrTimeout; every other error
// passes through unchanged.
func wrapTimeout(ctx context.Context, timeout time.Duration, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return fmt.Errorf("%w after %s", ErrTimeout, timeout)
	}
	return err
}

// Query runs custom SQL: text a person or a model wrote, not this
// program, capped at maxRows+1 (Run's own truncation then reports
// Truncated once that many rows come back). It is QueryLimit with this
// DB's own row cap.
func (d *DB) Query(ctx context.Context, q string, args ...any) (Result, error) {
	return d.QueryLimit(ctx, q, d.maxRows+1, args...)
}

// QueryLimit runs custom SQL wrapped as a subquery capped at limit rows —
// the security-relevant wrap, shared by Query's own cap and by a caller
// that needs a different one (reporting's LIMIT 0 shape check and LIMIT 5
// sample, in particular). It applies all four guard layers (package
// doc): Check first, then wraps the text as a subquery carrying limit,
// then Run.
func (d *DB) QueryLimit(ctx context.Context, q string, limit int, args ...any) (Result, error) {
	if limit < 0 {
		return Result{}, fmt.Errorf("%w: limit must not be negative", ErrRefused)
	}
	if _, err := d.Check(q); err != nil {
		return Result{}, err
	}
	// Exactly the set Check accepts after a statement's end: ASCII only,
	// never strings.TrimSpace, which also strips Unicode spaces and would
	// let SQLite parse text other than the one Check examined.
	trimmed := strings.TrimRight(q, "; \t\r\n\f")
	// A clean refusal rather than the wrap's syntax error. TrimSpace only
	// decides; trimmed reaches SQLite unchanged.
	if strings.TrimSpace(trimmed) == "" {
		return Result{}, fmt.Errorf("%w: sql must not be empty", ErrRefused)
	}
	wrapped := fmt.Sprintf("SELECT * FROM (%s\n) LIMIT %d", trimmed, limit)
	return d.Run(ctx, wrapped, args...)
}
