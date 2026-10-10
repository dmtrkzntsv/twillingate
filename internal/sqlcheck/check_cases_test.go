package sqlcheck

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// memDB is an in-memory database with one table, t(a INTEGER, b TEXT).
func memDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // one connection: an in-memory database is per connection
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE t (a INTEGER, b TEXT)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestProblems(t *testing.T) {
	db := memDB(t)
	cases := map[string]struct {
		site site
		want string // a substring of the one problem expected; "" for none
	}{
		"clean":            {site{sqls: []string{"SELECT a, b FROM t WHERE a = ?"}, nargs: []int{1}, read: true, scans: []int{2}}, ""},
		"unknown column":   {site{sqls: []string{"SELECT c FROM t"}}, "no such column"},
		"unknown table":    {site{sqls: []string{"DELETE FROM u"}}, "no such table"},
		"second statement": {site{sqls: []string{"SELECT a FROM t; SELECT z FROM t"}}, "no such column"},
		"too few args":     {site{sqls: []string{"UPDATE t SET b = ? WHERE a = ?"}, nargs: []int{1}}, "passes 1 arguments; the statement binds 2"},
		"too many args":    {site{sqls: []string{"SELECT a FROM t WHERE a = ?1 OR b = ?1"}, nargs: []int{2}}, "passes 2 arguments; the statement binds 1"},
		"scan short":       {site{sqls: []string{"SELECT a, b FROM t"}, read: true, scans: []int{1}}, "scans 1 values; the statement returns 2 columns"},
		"limit param":      {site{sqls: []string{"SELECT a FROM t LIMIT ?"}, nargs: []int{1}, read: true, scans: []int{1}}, ""},
		"named params":     {site{sqls: []string{"SELECT a FROM t WHERE a = :n OR a = :n LIMIT :m"}, nargs: []int{2}, read: true, scans: []int{1}}, ""},
		"no statement":     {site{sqls: []string{"-- nothing"}}, "no statement"},
	}
	for name, c := range cases {
		got := problems(context.Background(), db, c.site)
		switch {
		case c.want == "" && len(got) != 0:
			t.Errorf("%s: got %q, want none", name, got)
		case c.want != "" && (len(got) != 1 || !strings.Contains(got[0], c.want)):
			t.Errorf("%s: got %q, want one containing %q", name, got, c.want)
		}
	}
}

func TestColumnsRollsBack(t *testing.T) {
	db := memDB(t)
	p, _ := bindings("SELECT a FROM t")
	if _, err := columns(context.Background(), db, "SELECT a FROM t", p); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES (1, 'x')`); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n)
	p, _ = bindings("INSERT INTO t VALUES (2, 'y') RETURNING a")
	columns(context.Background(), db, "INSERT INTO t VALUES (2, 'y') RETURNING a", p)
	var after int
	db.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&after)
	if after != n {
		t.Fatalf("columns wrote a row: %d before, %d after", n, after)
	}
}
