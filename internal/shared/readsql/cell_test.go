package readsql

import (
	"context"
	"testing"
	"time"
)

// TestCellMatchesScan: tw_cell must render a value exactly as Run's scan
// does (a sql.NullString per cell), or a filter would compare against
// text the table never shows.
func TestCellMatchesScan(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 100)
	q := `SELECT 100, 1.5, 0.1 + 0.2, 1e21, 0.000001, -3, 'abc', '', NULL, x'6869'`
	res, err := db.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	cells, err := db.Query(context.Background(),
		`SELECT tw_cell(100), tw_cell(1.5), tw_cell(0.1 + 0.2), tw_cell(1e21), tw_cell(0.000001),
		        tw_cell(-3), tw_cell('abc'), tw_cell(''), tw_cell(NULL), tw_cell(x'6869')`)
	if err != nil {
		t.Fatal(err)
	}
	for i := range res.Rows[0] {
		if got, want := cells.Rows[0][i], res.Rows[0][i]; got != want {
			t.Errorf("column %d: tw_cell = %q, scan = %q", i, got, want)
		}
	}
}

func TestCellNumber(t *testing.T) {
	for s, want := range map[string]bool{
		"0": true, "-12": true, "1.5": true, "1e21": true, "1e+21": true, "1e-06": true, "-0.25E3": true,
		"": false, " 1": false, "1 ": false, "+1": false, "1.": false, ".5": false, "0x10": false,
		"1e999": false, "NaN": false, "Infinity": false, "12abc": false, "2026-09-25": false, "v1.5.1": false,
	} {
		if _, ok := CellNumber(s); ok != want {
			t.Errorf("CellNumber(%q) ok = %v, want %v", s, ok, want)
		}
	}
	if n, _ := CellNumber("-0.25E3"); n != -250 {
		t.Errorf("CellNumber(-0.25E3) = %v", n)
	}
}

func TestTwNumInSQL(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 100)
	res, err := db.Query(context.Background(),
		`SELECT tw_num(100), tw_num('2026'), tw_num('12abc'), tw_num(NULL), tw_num(''), typeof(tw_num('x'))`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"100", "2026", "", "", "", "null"}
	for i, w := range want {
		if res.Rows[0][i] != w {
			t.Errorf("column %d = %q, want %q", i, res.Rows[0][i], w)
		}
	}
}
