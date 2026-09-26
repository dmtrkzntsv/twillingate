// Package readsql is the one path custom SQL takes to the database (spec
// D46–47). It layers four guards, each independent of the others so a
// bug in one does not open the database to the next:
//
//  1. the connection pool is opened mode=ro with query_only and
//     _defensive pragmas, so a write cannot succeed even through a bug
//     elsewhere in this package;
//  2. Check tokenizes the text and refuses ATTACH and any read of meta
//     or a SQLite internal table or pragma view;
//  3. Query wraps the text as a subquery with the row cap in the same
//     clause, so DDL, PRAGMA and a second statement become syntax
//     errors rather than executing;
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
	// ErrRefused reports that Check refused the text: it named ATTACH, or
	// a table or pragma view custom SQL may not read.
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
}

// Open opens path read-only with its own connection pool, independent of
// any single-writer connection to the same file. query_only and
// _defensive are belt over the mode=ro brace: even a bug that finds a
// writable path is refused by the connection itself.
func Open(path string, timeout time.Duration, maxRows int) (*DB, error) {
	dsn := "file:" + path + "?mode=ro" +
		"&_pragma=query_only(1)" +
		"&_pragma=busy_timeout(5000)" +
		"&_defensive=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("readsql: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(4)
	return &DB{db: db, timeout: timeout, maxRows: maxRows}, nil
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
	var out [][]string
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
// program. It applies all four guard layers (package doc): Check first,
// then wraps the text as a subquery carrying the row cap, then Run.
func (d *DB) Query(ctx context.Context, q string, args ...any) (Result, error) {
	if _, err := Check(q); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(q) == "" {
		return Result{}, fmt.Errorf("%w: sql must not be empty", ErrRefused)
	}
	wrapped := fmt.Sprintf("SELECT * FROM (%s\n) LIMIT %d",
		strings.TrimRight(strings.TrimSpace(q), ";"), d.maxRows+1)
	return d.Run(ctx, wrapped, args...)
}
