package sqlcheck

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// problems checks one site's SQL against db: every statement prepares
// (D4), a lone statement binds as many parameters as each call passes
// (D5), and a read's rows have as many columns as each Scan reads (D6).
// It returns nothing for a dynamic site: TestDynamicSitesAreListed owns those.
func problems(ctx context.Context, db *sql.DB, s site) []string {
	var out []string
	for _, q := range s.sqls {
		stmts, err := statements(q)
		if err != nil {
			out = append(out, err.Error())
			continue
		}
		if len(stmts) == 0 {
			out = append(out, "no statement in "+strconv.Quote(q))
			continue
		}
		prepared := true
		for _, st := range stmts {
			stmt, err := db.PrepareContext(ctx, st)
			if err != nil {
				out = append(out, fmt.Sprintf("%v\n%s", err, st))
				prepared = false
				continue
			}
			stmt.Close()
		}
		// Counts are per statement: what a script binds across its
		// statements is the driver's business, and no Go SQL string is one.
		if !prepared || len(stmts) != 1 {
			continue
		}
		st := stmts[0]
		p, err := bindings(st)
		if err != nil {
			out = append(out, err.Error())
			continue
		}
		for _, n := range s.nargs {
			if n != p.max {
				out = append(out, fmt.Sprintf("passes %d arguments; the statement binds %d\n%s", n, p.max, st))
			}
		}
		if !s.read || len(s.scans) == 0 {
			continue
		}
		cols, err := columns(ctx, db, st, p)
		if err != nil {
			out = append(out, fmt.Sprintf("running it to count its columns: %v\n%s", err, st))
			continue
		}
		for _, n := range s.scans {
			if n != cols {
				out = append(out, fmt.Sprintf("scans %d values; the statement returns %d columns\n%s", n, cols, st))
			}
		}
	}
	return out
}

// columns runs a read once on db, inside a transaction it rolls back,
// and returns how many columns its rows have. Every parameter is bound to
// NULL, then to 0 if that fails (LIMIT NULL is a datatype mismatch).
func columns(ctx context.Context, db *sql.DB, stmt string, p params) (int, error) {
	try := func(zero any) (int, error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		args := make([]any, 0, p.max)
		for i := 1; i <= p.max; i++ {
			if name, ok := p.names[i]; ok {
				args = append(args, sql.Named(name, zero))
			} else {
				args = append(args, zero)
			}
		}
		rows, err := tx.QueryContext(ctx, stmt, args...)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			return 0, err
		}
		return len(cols), rows.Err()
	}
	n, err := try(nil)
	if err != nil {
		n, err = try(0)
	}
	return n, err
}
