package sqlcheck

import (
	"reflect"
	"strings"
	"testing"
)

const prelude = "package a\n\nimport (\n\t\"context\"\n\t\"database/sql\"\n\t\"fmt\"\n)\n\nvar _ = fmt.Sprintf\nvar _ context.Context\nvar _ *sql.DB\n\n"

// scanOne scans one file of package a and returns its sites.
func scanOne(t *testing.T, body string) []site {
	t.Helper()
	sites, err := scanFiles(map[string]string{"a/a.go": prelude + body})
	if err != nil {
		t.Fatal(err)
	}
	return sites
}

func TestResolveConstantsAndConcatenation(t *testing.T) {
	sites := scanOne(t, `const cols = "a, b"
func F(ctx context.Context, db *sql.DB) {
	db.QueryContext(ctx, "SELECT "+cols+" FROM t WHERE id = ?", 1)
}`)
	want := site{pos: "a/a.go:15", key: "a/a.go:F", sqls: []string{"SELECT a, b FROM t WHERE id = ?"}, nargs: []int{1}, read: true}
	if len(sites) != 1 || !reflect.DeepEqual(sites[0], want) {
		t.Fatalf("got %+v, want %+v", sites, want)
	}
}

func TestResolveEveryAssignmentOfALocal(t *testing.T) {
	sites := scanOne(t, `func F(ctx context.Context, db *sql.DB, archived bool) {
	q := "UPDATE t SET archived_at = now() WHERE id = ?"
	if !archived {
		q = "UPDATE t SET archived_at = NULL WHERE id = ?"
	}
	db.ExecContext(ctx, q, 1)
}`)
	if len(sites) != 1 || len(sites[0].sqls) != 2 {
		t.Fatalf("got %+v, want one site with both statements", sites)
	}
}

func TestResolveTableDrivenLoops(t *testing.T) {
	sites := scanOne(t, `type pair struct{ key, sql string }
func F(ctx context.Context, db *sql.DB) {
	inner := "SELECT 1"
	for _, s := range []struct {
		q   string
		set func(int)
	}{
		{"SELECT a FROM t", nil},
		{q: "SELECT b FROM t", set: nil},
	} {
		db.QueryContext(ctx, s.q)
	}
	for _, p := range []pair{{"k", "SELECT (" + inner + ")"}} {
		db.ExecContext(ctx, "INSERT INTO u "+p.sql)
	}
}`)
	if len(sites) != 2 {
		t.Fatalf("got %d sites, want 2: %+v", len(sites), sites)
	}
	if want := []string{"SELECT a FROM t", "SELECT b FROM t"}; !reflect.DeepEqual(sites[0].sqls, want) {
		t.Errorf("inline struct: got %q, want %q", sites[0].sqls, want)
	}
	if want := []string{"INSERT INTO u SELECT (SELECT 1)"}; !reflect.DeepEqual(sites[1].sqls, want) {
		t.Errorf("named struct: got %q, want %q", sites[1].sqls, want)
	}
}

func TestResolvePlaceholdersHelper(t *testing.T) {
	sites := scanOne(t, `func placeholders(n int) string { return "" }
func F(ctx context.Context, db *sql.DB, ids []any) {
	db.ExecContext(ctx, "DELETE FROM t WHERE id IN ("+placeholders(len(ids))+")", ids...)
}`)
	if len(sites) != 1 || !reflect.DeepEqual(sites[0].sqls, []string{"DELETE FROM t WHERE id IN (?)"}) || sites[0].nargs != nil {
		t.Fatalf("got %+v, want one IN (?) site with no argument count", sites)
	}
}

func TestPassThroughWrappersMoveTheSiteToTheirCallers(t *testing.T) {
	sites := scanOne(t, `type H struct{ db *sql.DB }
func (h *H) run(ctx context.Context, q string, args ...any) { runOn(ctx, h.db, q, args...) }
func runOn(ctx context.Context, db *sql.DB, q string, args ...any) { db.QueryContext(ctx, q, args...) }
func G(ctx context.Context, h *H) { h.run(ctx, "SELECT ?, ?", 1, 2) }`)
	if len(sites) != 1 || sites[0].key != "a/a.go:G" || !reflect.DeepEqual(sites[0].nargs, []int{2}) {
		t.Fatalf("got %+v, want one site in G passing 2 arguments", sites)
	}
	if sites[0].read {
		t.Errorf("a wrapper's rows are not database/sql rows; got read")
	}
}

func TestResolvePackageVariablesAndStringLoops(t *testing.T) {
	sites := scanOne(t, `var tables = []string{"a", "b"}
var counts = []struct{ key, sql string }{{"n", "SELECT COUNT(*) FROM a"}}
func F(ctx context.Context, tx *sql.Tx) {
	for _, table := range tables {
		tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE project_id = ?", 1)
	}
	for _, c := range counts {
		tx.ExecContext(ctx, c.sql)
	}
}`)
	if len(sites) != 2 {
		t.Fatalf("got %d sites, want 2", len(sites))
	}
	if want := []string{"DELETE FROM a WHERE project_id = ?", "DELETE FROM b WHERE project_id = ?"}; !reflect.DeepEqual(sites[0].sqls, want) {
		t.Errorf("string loop: got %q, want %q", sites[0].sqls, want)
	}
	if want := []string{"SELECT COUNT(*) FROM a"}; !reflect.DeepEqual(sites[1].sqls, want) {
		t.Errorf("struct loop over a package variable: got %q, want %q", sites[1].sqls, want)
	}
}

func TestPreparedStatementsCountEachCall(t *testing.T) {
	sites := scanOne(t, `func F(ctx context.Context, tx *sql.Tx) {
	stmt, _ := tx.PrepareContext(ctx, "INSERT INTO t VALUES (?, ?)")
	stmt.ExecContext(ctx, 1, 2)
	stmt.ExecContext(ctx, 1)
}`)
	if len(sites) != 1 || !reflect.DeepEqual(sites[0].nargs, []int{2, 1}) {
		t.Fatalf("got %+v, want the PrepareContext site with counts [2 1]", sites)
	}
}

func TestScansAreMatchedToTheirQuery(t *testing.T) {
	sites := scanOne(t, `func F(ctx context.Context, db *sql.DB) {
	var a, b int
	db.QueryRowContext(ctx, "SELECT a, b FROM t").Scan(&a, &b)
	rows, _ := db.QueryContext(ctx, "SELECT a FROM t")
	for rows.Next() {
		rows.Scan(&a)
	}
}`)
	if len(sites) != 2 || !reflect.DeepEqual(sites[0].scans, []int{2}) || !reflect.DeepEqual(sites[1].scans, []int{1}) {
		t.Fatalf("got %+v, want scans [2] and [1]", sites)
	}
}

func TestScansInsideAClosure(t *testing.T) {
	sites := scanOne(t, `func inTx(f func(*sql.Tx) error) error { return nil }
func F(ctx context.Context) error {
	return inTx(func(tx *sql.Tx) error {
		var a, b int
		rows, err := tx.QueryContext(ctx, "SELECT a, b FROM t WHERE id = ?", 1)
		if err != nil {
			return err
		}
		for rows.Next() {
			rows.Scan(&a, &b)
		}
		return tx.QueryRowContext(ctx, "SELECT a FROM t").Scan(&a)
	})
}`)
	if len(sites) != 2 || !reflect.DeepEqual(sites[0].scans, []int{2}) || !reflect.DeepEqual(sites[1].scans, []int{1}) {
		t.Fatalf("got %+v, want scans [2] and [1] found inside the closure", sites)
	}
}

func TestRangeNameReusedIsDynamic(t *testing.T) {
	sites := scanOne(t, `func F(ctx context.Context, db *sql.DB) {
	for _, t := range []string{"a"} {
		db.ExecContext(ctx, "DELETE FROM "+t)
	}
	for _, t := range []string{"b"} {
		db.ExecContext(ctx, "DELETE FROM "+t)
	}
	for _, s := range []struct{ q string }{{"SELECT 1"}} {
		db.QueryContext(ctx, s.q)
	}
	for _, s := range []struct{ q string }{{"SELECT 2"}} {
		db.QueryContext(ctx, s.q)
	}
}`)
	if len(sites) != 4 {
		t.Fatalf("got %d sites, want 4", len(sites))
	}
	for i, s := range sites {
		if s.sqls != nil || !strings.Contains(s.why, "more than one slice") {
			t.Errorf("site %d: got %+v, want dynamic: the name ranges over two slices", i, s)
		}
	}
}

func TestLocalDeclaredTwiceIsDynamic(t *testing.T) {
	sites := scanOne(t, `func F(ctx context.Context, db *sql.DB, x bool) {
	if x {
		q := "UPDATE t SET a = ? WHERE b = ?"
		db.ExecContext(ctx, q, 1, 2)
	} else {
		q := "DELETE FROM t WHERE b = ?"
		db.ExecContext(ctx, q, 1)
	}
}`)
	if len(sites) != 2 {
		t.Fatalf("got %d sites, want 2", len(sites))
	}
	for i, s := range sites {
		if s.sqls != nil || !strings.Contains(s.why, "declared more than once") {
			t.Errorf("site %d: got %+v, want dynamic: the name is declared twice", i, s)
		}
	}
}

func TestDynamicSitesSayWhy(t *testing.T) {
	sites := scanOne(t, `var name = "SELECT 1"
func init() { name = "SELECT 2" }
func F(ctx context.Context, db *sql.DB, table string) {
	db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s", table))
	q := "SELECT 1"
	q += " WHERE 1"
	db.QueryContext(ctx, q)
	db.QueryContext(ctx, name)
}`)
	if len(sites) != 3 {
		t.Fatalf("got %d sites, want 3", len(sites))
	}
	for i, want := range []string{"fmt.Sprintf", "+=", "unchanged package variable"} {
		if sites[i].sqls != nil || !strings.Contains(sites[i].why, want) || sites[i].key != "a/a.go:F" {
			t.Errorf("site %d: got %+v, want dynamic mentioning %q", i, sites[i], want)
		}
	}
}
