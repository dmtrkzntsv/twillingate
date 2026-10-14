package enginesql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
)

// childEnv makes this test binary act as `twillingate engine` when an
// Engine spawns it, so the pages run on a real DuckDB child.
const childEnv = "ENGINESQL_TEST_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		if err := engine.ServeChild(os.Args[1:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func newEngine(t testing.TB) *engine.Engine {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	e, err := engine.New(engine.Options{Path: self, Env: []string{childEnv + "=1"},
		AllowedDirs: []string{filepath.Join(dir, "raw")}, SpillRoot: filepath.Join(dir, "spill"),
		SlotDir: filepath.Join(dir, "slots"), MaxChildren: 1, MemoryLimit: "256MB", Threads: 2,
		IdleExit: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// cells reads an engine row of text. Text's cells and the page's own
// columns are tw_cell's text, NULL included as "": a number or a NULL here
// means a column skipped it.
func cells(t testing.TB, row []any) []string {
	t.Helper()
	out := make([]string, len(row))
	for i, v := range row {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("cell %d is %T %v, not text", i, v, v)
		}
		out[i] = s
	}
	return out
}

func count(v any) (int, error) { return strconv.Atoi(fmt.Sprint(v)) }

// runPage is what QueryPage does, on the engine: the shape for the
// columns, the page, and the counts when the page has no row to carry
// them. q is bound text and args its arguments.
func runPage(t testing.TB, e *engine.Engine, q string, args []engine.Arg, p readsql.Page, maxRows int) (readsql.PageResult, error) {
	t.Helper()
	ctx := context.Background()
	shape, err := e.Query(ctx, Shape(q), 0, args...)
	if err != nil {
		return readsql.PageResult{}, err
	}
	cols := shape.Columns
	st, err := Page(q, args, cols, p, maxRows)
	if err != nil {
		return readsql.PageResult{}, err
	}
	res, err := e.Query(ctx, st.SQL, 0, st.Args...)
	if err != nil {
		return readsql.PageResult{}, fmt.Errorf("%w\n%s", err, st.SQL)
	}
	out := readsql.PageResult{Result: readsql.Result{Columns: cols, Rows: [][]string{}}}
	if p.Distinct != "" {
		out.Columns = []string{"value", "rows"}
	}
	keep := len(out.Columns)
	if len(res.Rows) > 0 {
		if out.Matched, err = count(res.Rows[0][keep]); err != nil {
			return out, err
		}
		if out.Total, err = count(res.Rows[0][keep+1]); err != nil {
			return out, err
		}
		for _, row := range res.Rows {
			out.Rows = append(out.Rows, cells(t, row[:keep]))
		}
	} else {
		total, matched, err := Count(q, args, cols, p)
		if err != nil {
			return out, err
		}
		res, err := e.Query(ctx, total.SQL, 0, total.Args...)
		if err != nil {
			return out, fmt.Errorf("%w\n%s", err, total.SQL)
		}
		if out.Total, err = count(res.Rows[0][0]); err != nil {
			return out, err
		}
		if matched.SQL != "" {
			res, err := e.Query(ctx, matched.SQL, 0, matched.Args...)
			if err != nil {
				return out, fmt.Errorf("%w\n%s", err, matched.SQL)
			}
			if out.Matched, err = count(res.Rows[0][0]); err != nil {
				return out, err
			}
		}
	}
	out.Truncated = out.Matched > p.Offset+len(out.Rows)
	return out, nil
}

type pageCases struct {
	Columns []string            `json:"columns"`
	Rows    [][]json.RawMessage `json:"rows"`
	Cells   [][]string          `json:"cells"`
	Cases   []struct {
		Name    string                        `json:"name"`
		Filters []map[string]any              `json:"filters"`
		Sort    *struct{ Column, Dir string } `json:"sort"`
		Expect  []int                         `json:"expect"`
	} `json:"cases"`
	Distinct []struct {
		Name    string           `json:"name"`
		Column  string           `json:"column"`
		Filters []map[string]any `json:"filters"`
		Expect  [][]any          `json:"expect"`
	} `json:"distinct"`
}

func loadPageCases(t *testing.T) pageCases {
	t.Helper()
	b, err := os.ReadFile("../../reporting/testdata/table-filters.json")
	if err != nil {
		t.Fatal(err)
	}
	var c pageCases
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// valuesSQL renders the cases' rows as a VALUES relation named by the
// cases' columns. A DuckDB column has one type, so each takes the type of
// its values: all text is VARCHAR, all integers BIGINT, integers and
// reals together DOUBLE (Count: 1069 and 12.5, which tw_cell reads as
// SQLite showed them).
func valuesSQL(t *testing.T, c pageCases) string {
	t.Helper()
	types := make([]string, len(c.Columns))
	for ci := range c.Columns {
		str, num, real := false, false, false
		for _, row := range c.Rows {
			s := string(row[ci])
			switch {
			case s == "null":
			case strings.HasPrefix(s, `"`):
				str = true
			case strings.ContainsAny(s, ".eE"):
				real = true
			default:
				num = true
			}
		}
		switch {
		case str:
			types[ci] = "VARCHAR"
		case real:
			types[ci] = "DOUBLE"
		case num:
			types[ci] = "BIGINT"
		default:
			types[ci] = "VARCHAR"
		}
	}
	var rows, names []string
	for ri, row := range c.Rows {
		var vals []string
		for ci, raw := range row {
			lit := "NULL"
			if string(raw) != "null" {
				lit = "'" + strings.ReplaceAll(c.Cells[ri][ci], "'", "''") + "'"
			}
			vals = append(vals, "CAST("+lit+" AS "+types[ci]+")")
		}
		rows = append(rows, "("+strings.Join(vals, ", ")+")")
	}
	for _, col := range c.Columns {
		names = append(names, `"`+strings.ReplaceAll(col, `"`, `""`)+`"`)
	}
	return "SELECT * FROM (VALUES " + strings.Join(rows, ",\n") + ") AS t(" + strings.Join(names, ", ") + ")"
}

func toFilters(in []map[string]any) []readsql.Filter {
	var out []readsql.Filter
	for _, f := range in {
		var vals []string
		switch v := f["value"].(type) {
		case string:
			vals = []string{v}
		case []any:
			for _, x := range v {
				vals = append(vals, x.(string))
			}
		}
		out = append(out, readsql.Filter{Column: f["column"].(string), Op: f["op"].(string), Values: vals})
	}
	return out
}

// The cases the SQLite wrap and the web app's table share give the same
// rows, counts and order on DuckDB.
func TestPageCases(t *testing.T) {
	c := loadPageCases(t)
	e := newEngine(t)
	q := valuesSQL(t, c)

	all, err := e.Query(context.Background(), Text(q), 0)
	if err != nil {
		t.Fatal(err)
	}
	var scanned [][]string
	for _, row := range all.Rows {
		scanned = append(scanned, cells(t, row))
	}
	if !reflect.DeepEqual(scanned, c.Cells) || !reflect.DeepEqual(all.Columns, c.Columns) {
		t.Fatalf("the cases file's cells disagree with Text:\n got  %v %q\n want %v %q", all.Columns, scanned, c.Columns, c.Cells)
	}
	for _, tc := range c.Cases {
		p := readsql.Page{Filters: toFilters(tc.Filters)}
		if tc.Sort != nil {
			p.Sort = &readsql.Sort{Column: tc.Sort.Column, Desc: tc.Sort.Dir == "desc"}
		}
		got, err := runPage(t, e, q, nil, p, 1000)
		if err != nil {
			t.Errorf("%s: %v", tc.Name, err)
			continue
		}
		want := [][]string{}
		for _, i := range tc.Expect {
			want = append(want, c.Cells[i])
		}
		if !reflect.DeepEqual(got.Rows, want) {
			t.Errorf("%s:\n got  %q\n want %q", tc.Name, got.Rows, want)
		}
		if got.Matched != len(tc.Expect) || got.Total != len(c.Rows) {
			t.Errorf("%s: matched %d total %d, want %d %d", tc.Name, got.Matched, got.Total, len(tc.Expect), len(c.Rows))
		}
	}
	for _, tc := range c.Distinct {
		got, err := runPage(t, e, q, nil, readsql.Page{Distinct: tc.Column, Filters: toFilters(tc.Filters)}, 1000)
		if err != nil {
			t.Errorf("%s: %v", tc.Name, err)
			continue
		}
		want := [][]string{}
		for _, x := range tc.Expect {
			want = append(want, []string{x[0].(string), strconv.Itoa(int(x[1].(float64)))})
		}
		if !reflect.DeepEqual(got.Columns, []string{"value", "rows"}) || !reflect.DeepEqual(got.Rows, want) {
			t.Errorf("%s:\n got  %v %q\n want %q", tc.Name, got.Columns, got.Rows, want)
		}
		if got.Matched != len(tc.Expect) || got.Total != len(c.Rows) {
			t.Errorf("%s: matched %d total %d, want %d %d", tc.Name, got.Matched, got.Total, len(tc.Expect), len(c.Rows))
		}
	}
}

// Joined pages equal the whole, for page sizes that do and do not divide
// the total, so a viewer paging through never sees a row twice or misses one.
func TestPageJoinedPagesEqualTheWhole(t *testing.T) {
	c := loadPageCases(t)
	e := newEngine(t)
	q := valuesSQL(t, c)
	sort := &readsql.Sort{Column: "Count", Desc: true}
	whole, err := runPage(t, e, q, nil, readsql.Page{Sort: sort}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 3, 7, len(c.Rows)} {
		var joined [][]string
		for off := 0; off < len(c.Rows); off += size {
			p, err := runPage(t, e, q, nil, readsql.Page{Sort: sort, Offset: off, Limit: size}, 1000)
			if err != nil {
				t.Fatal(err)
			}
			if want := p.Matched > off+len(p.Rows); p.Truncated != want {
				t.Errorf("size %d offset %d: truncated %v, want %v", size, off, p.Truncated, want)
			}
			joined = append(joined, p.Rows...)
		}
		if !reflect.DeepEqual(joined, whole.Rows) {
			t.Errorf("size %d: joined pages differ from the whole", size)
		}
	}
}

func TestPageZeroMatchesStillCounts(t *testing.T) {
	e := newEngine(t)
	got, err := runPage(t, e, `SELECT 1 AS a UNION ALL SELECT 2`,
		nil, readsql.Page{Filters: []readsql.Filter{{Column: "a", Op: ">", Values: []string{"5"}}}}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 0 || got.Matched != 0 || got.Total != 2 || got.Truncated {
		t.Errorf("got %+v", got)
	}
	if !reflect.DeepEqual(got.Columns, []string{"a"}) {
		t.Errorf("columns %v, want [a] (no counting columns)", got.Columns)
	}
}

// A page past the last row has no row to carry the counts either.
func TestPageOffsetPastTheEndStillCounts(t *testing.T) {
	e := newEngine(t)
	q := `SELECT 1 AS a UNION ALL SELECT 2 UNION ALL SELECT 3`
	got, err := runPage(t, e, q, nil, readsql.Page{
		Filters: []readsql.Filter{{Column: "a", Op: ">", Values: []string{"1"}}},
		Offset:  5,
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 0 || got.Matched != 2 || got.Total != 3 || got.Truncated {
		t.Errorf("got %+v", got)
	}
	got, err = runPage(t, e, q, nil, readsql.Page{Distinct: "a", Offset: 9}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 0 || got.Matched != 3 || got.Total != 3 {
		t.Errorf("distinct past the end: got %+v", got)
	}
}

// Names and values are inert: a column alias with a quote in it filters
// and sorts like any other, and a value is bound, never spliced.
func TestPageNamesAndValuesAreInert(t *testing.T) {
	e := newEngine(t)
	q := `SELECT 'x''; DROP TABLE meta; --' AS "a""b", 3 AS "Users (at least)" UNION ALL SELECT 'y', 1`
	got, err := runPage(t, e, q, nil, readsql.Page{
		Filters: []readsql.Filter{{Column: `a"b`, Op: "in", Values: []string{"x'; DROP TABLE meta; --", "z"}}},
		Sort:    &readsql.Sort{Column: "Users (at least)"},
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 1 || got.Rows[0][0] != "x'; DROP TABLE meta; --" {
		t.Errorf("rows %q", got.Rows)
	}
	if !reflect.DeepEqual(got.Columns, []string{`a"b`, "Users (at least)"}) {
		t.Errorf("columns %v", got.Columns)
	}
}

// A widget column named like a counting column must not collide, in any
// case: DuckDB matches names without regard to it.
func TestPageCountingColumnsDoNotCollide(t *testing.T) {
	e := newEngine(t)
	got, err := runPage(t, e, `SELECT 1 AS __tw_total, 2 AS __TW_MATCHED, 3 AS __tw_total_1`, nil, readsql.Page{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Rows, [][]string{{"1", "2", "3"}}) || got.Total != 1 || got.Matched != 1 {
		t.Errorf("got %+v", got)
	}
}

// A sorted page adds a tw_cell and a tw_num column per result column; a
// query already using those names must neither break nor sort by them.
func TestPageSortKeysDoNotCollide(t *testing.T) {
	e := newEngine(t)
	got, err := runPage(t, e, `SELECT 1 AS __tw_num_1, 2 AS x UNION ALL SELECT 2, 1`, nil,
		readsql.Page{Sort: &readsql.Sort{Column: "x"}}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"2", "1"}, {"1", "2"}}; !reflect.DeepEqual(got.Rows, want) {
		t.Errorf("rows = %v, want %v", got.Rows, want)
	}
}

// A DOUBLE reads as SQLite showed it, 100 rather than 100.0, so "="
// matches the text a viewer sees; "<" and ">" with a number compare
// numbers. A value past DOUBLE's range is an infinity, which still orders.
func TestPageNumbersReadAsTheyDidOnSQLite(t *testing.T) {
	e := newEngine(t)
	q := `SELECT * FROM (VALUES (CAST(100 AS DOUBLE)), (12.5::DOUBLE), (3.50::DECIMAL(5,2)), (1e20::DOUBLE), (NULL)) AS t(n)`
	for _, c := range []struct {
		op, value string
		want      [][]string
	}{
		{"=", "100", [][]string{{"100"}}},
		{"=", "100.0", [][]string{}},
		{"=", "3.5", [][]string{{"3.5"}}},
		{">", "99", [][]string{{"100"}, {"1e+20"}}},
		{"<", "1e400", [][]string{{"100"}, {"12.5"}, {"3.5"}, {"1e+20"}}},
		{">", "1e400", [][]string{}},
		{"!=", "100", [][]string{{"12.5"}, {"3.5"}, {"1e+20"}, {""}}},
	} {
		got, err := runPage(t, e, q, nil, readsql.Page{Filters: []readsql.Filter{{Column: "n", Op: c.op, Values: []string{c.value}}}}, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Rows, c.want) {
			t.Errorf("n %s %s: rows %q, want %q", c.op, c.value, got.Rows, c.want)
		}
	}
}

func TestPageRefusals(t *testing.T) {
	cols := []string{"a", "b"}
	q := `SELECT 1 AS a, 'x' AS b`
	for name, p := range map[string]readsql.Page{
		"unknown column lists valid ones":  {Filters: []readsql.Filter{{Column: "c", Op: "=", Values: []string{"1"}}}},
		"unknown operator":                 {Filters: []readsql.Filter{{Column: "a", Op: "~", Values: []string{"1"}}}},
		"list for a single-value operator": {Filters: []readsql.Filter{{Column: "a", Op: "=", Values: []string{"1", "2"}}}},
		"empty in":                         {Filters: []readsql.Filter{{Column: "a", Op: "in"}}},
		"unknown sort column":              {Sort: &readsql.Sort{Column: "c"}},
		"unknown distinct column":          {Distinct: "c"},
		"distinct with sort":               {Distinct: "a", Sort: &readsql.Sort{Column: "a"}},
		"negative offset":                  {Offset: -1},
		"limit above the cap":              {Limit: 11},
		"negative limit":                   {Limit: -1},
	} {
		_, err := Page(q, nil, cols, p, 10)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v, want ErrRefused", name, err)
		}
		if name == "unknown column lists valid ones" && (err == nil || !strings.Contains(err.Error(), "a, b")) {
			t.Errorf("%s: message %v does not list a, b", name, err)
		}
		if strings.Contains(name, "limit") {
			continue // Count pages nothing
		}
		if _, _, err := Count(q, nil, cols, p); !errors.Is(err, ErrRefused) {
			t.Errorf("Count, %s: err = %v, want ErrRefused", name, err)
		}
	}
	// The scanner's refusals hold for the wrap too, and the page's own
	// parameter names are its own.
	for name, q := range map[string]string{
		"an escape from the wrap": `SELECT 1) UNION SELECT 2 --`,
		"a positional parameter":  `SELECT ? AS a`,
		"a :name not bound":       `SELECT :from AS a`,
		"the page's own names":    `SELECT CAST($tw_p1 AS BIGINT) AS a`,
		"in another case":         `SELECT CAST($TW_P2 AS BIGINT) AS a`,
	} {
		if _, err := Page(q, nil, []string{"a"}, readsql.Page{}, 10); !errors.Is(err, ErrRefused) {
			t.Errorf("Page, %s: err = %v, want ErrRefused", name, err)
		}
		if _, _, err := Count(q, nil, []string{"a"}, readsql.Page{}); !errors.Is(err, ErrRefused) {
			t.Errorf("Count, %s: err = %v, want ErrRefused", name, err)
		}
	}
}

// Each statement carries exactly the arguments it uses, which the engine
// insists on: the widget's own, bound by Bind, on every statement over
// q; the filter values only where the filters are, so not on the total
// of an empty page, nor for a filter a distinct page ignores.
func TestPageCarriesExactlyTheArgumentsEachStatementUses(t *testing.T) {
	e := newEngine(t)
	q, args, err := Bind(`SELECT :to AS t, n, :project AS p FROM range(10, 60, 10) AS r(n)
		WHERE n <= :to AND :project = 7 AND :project <> 0`, map[string]Value{
		":project": {"BIGINT", "7"}, ":from": {"DATE", "2026-01-01"}, ":to": {"BIGINT", "40"},
	})
	if err != nil {
		t.Fatal(err)
	}
	filters := []readsql.Filter{{Column: "n", Op: ">", Values: []string{"10"}}, {Column: "p", Op: "in", Values: []string{"7", "x"}}}
	sort := &readsql.Sort{Column: "n", Desc: true}
	for _, pc := range []struct {
		offset    int
		want      [][]string
		truncated bool
	}{
		{0, [][]string{{"40", "40", "7"}}, true},
		{1, [][]string{{"40", "30", "7"}}, true},
		{2, [][]string{{"40", "20", "7"}}, false},
		{5, [][]string{}, false},
	} {
		got, err := runPage(t, e, q, args, readsql.Page{Filters: filters, Sort: sort, Offset: pc.offset, Limit: 1}, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Rows, pc.want) || got.Matched != 3 || got.Total != 4 || got.Truncated != pc.truncated {
			t.Errorf("offset %d: got %+v, want rows %q matched 3 total 4 truncated %v", pc.offset, got, pc.want, pc.truncated)
		}
	}
	// The filter on p is the distinct column's own, so it is dropped with its values.
	got, err := runPage(t, e, q, args, readsql.Page{Filters: filters, Distinct: "p"}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Rows, [][]string{{"7", "3"}}) || got.Matched != 1 || got.Total != 4 {
		t.Errorf("distinct: got %+v", got)
	}
	got, err = runPage(t, e, q, args, readsql.Page{Filters: filters, Distinct: "p", Offset: 3}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 0 || got.Matched != 1 || got.Total != 4 {
		t.Errorf("distinct past the end: got %+v", got)
	}
	// Nothing matches at offset 0: the total alone, with q's arguments only.
	got, err = runPage(t, e, q, args, readsql.Page{Filters: []readsql.Filter{{Column: "n", Op: "=", Values: []string{"35"}}}}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 0 || got.Matched != 0 || got.Total != 4 {
		t.Errorf("no match: got %+v", got)
	}
}
