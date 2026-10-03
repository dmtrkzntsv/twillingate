package readsql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

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

// valuesSQL renders the cases' rows as one SELECT ... UNION ALL ...
// with SQL-typed literals, aliased to the cases' column names, so the
// wrap sees the same shapes a widget query produces.
func valuesSQL(t *testing.T, c pageCases) string {
	t.Helper()
	var sel []string
	for ri, row := range c.Rows {
		var cols []string
		for ci, raw := range row {
			lit := "NULL"
			s := string(raw)
			switch {
			case s == "null":
			case strings.HasPrefix(s, `"`):
				var v string
				if err := json.Unmarshal(raw, &v); err != nil {
					t.Fatal(err)
				}
				lit = "'" + strings.ReplaceAll(v, "'", "''") + "'"
			case strings.ContainsAny(s, ".eE"):
				lit = "CAST(" + s + " AS REAL)"
			default:
				lit = s
			}
			if ri == 0 {
				lit += ` AS "` + strings.ReplaceAll(c.Columns[ci], `"`, `""`) + `"`
			}
			cols = append(cols, lit)
		}
		sel = append(sel, "SELECT "+strings.Join(cols, ", "))
	}
	return strings.Join(sel, "\nUNION ALL\n")
}

func toFilters(in []map[string]any) []Filter {
	var out []Filter
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
		out = append(out, Filter{Column: f["column"].(string), Op: f["op"].(string), Values: vals})
	}
	return out
}

func TestQueryPageCases(t *testing.T) {
	c := loadPageCases(t)
	db, _ := newTestDB(t, 5*time.Second, 1000)
	ctx := context.Background()
	q := valuesSQL(t, c)

	all, err := db.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all.Rows, c.Cells) {
		t.Fatalf("the cases file's cells disagree with the scan:\n got  %q\n want %q", all.Rows, c.Cells)
	}
	for _, tc := range c.Cases {
		p := Page{Filters: toFilters(tc.Filters)}
		if tc.Sort != nil {
			p.Sort = &Sort{Column: tc.Sort.Column, Desc: tc.Sort.Dir == "desc"}
		}
		got, err := db.QueryPage(ctx, q, p)
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
		got, err := db.QueryPage(ctx, q, Page{Distinct: tc.Column, Filters: toFilters(tc.Filters)})
		if err != nil {
			t.Errorf("%s: %v", tc.Name, err)
			continue
		}
		var want [][]string
		for _, e := range tc.Expect {
			want = append(want, []string{e[0].(string), strconv.Itoa(int(e[1].(float64)))})
		}
		if !reflect.DeepEqual(got.Columns, []string{"value", "rows"}) || !reflect.DeepEqual(got.Rows, want) {
			t.Errorf("%s:\n got  %v %q\n want %q", tc.Name, got.Columns, got.Rows, want)
		}
	}
}

// Joined pages equal the whole, for page sizes that do and do not
// divide the total, so a viewer paging through never sees a row twice
// or misses one.
func TestQueryPageJoinedPagesEqualTheWhole(t *testing.T) {
	c := loadPageCases(t)
	db, _ := newTestDB(t, 5*time.Second, 1000)
	ctx := context.Background()
	q := valuesSQL(t, c)
	sort := &Sort{Column: "Count", Desc: true}
	whole, err := db.QueryPage(ctx, q, Page{Sort: sort})
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 3, 7, len(c.Rows)} {
		var joined [][]string
		for off := 0; off < len(c.Rows); off += size {
			p, err := db.QueryPage(ctx, q, Page{Sort: sort, Offset: off, Limit: size})
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

func TestQueryPageZeroMatchesStillCounts(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 1000)
	got, err := db.QueryPage(context.Background(), `SELECT 1 AS a UNION ALL SELECT 2`,
		Page{Filters: []Filter{{Column: "a", Op: ">", Values: []string{"5"}}}})
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
func TestQueryPageOffsetPastTheEndStillCounts(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 1000)
	ctx := context.Background()
	q := `SELECT 1 AS a UNION ALL SELECT 2 UNION ALL SELECT 3`
	got, err := db.QueryPage(ctx, q, Page{
		Filters: []Filter{{Column: "a", Op: ">", Values: []string{"1"}}},
		Offset:  5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 0 || got.Matched != 2 || got.Total != 3 || got.Truncated {
		t.Errorf("got %+v", got)
	}
	got, err = db.QueryPage(ctx, q, Page{Distinct: "a", Offset: 9})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 0 || got.Matched != 3 || got.Total != 3 {
		t.Errorf("distinct past the end: got %+v", got)
	}
}

// Names and values are inert: a column alias with a quote in it filters
// and sorts like any other, and a value is bound, never spliced.
func TestQueryPageNamesAndValuesAreInert(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 1000)
	ctx := context.Background()
	q := `SELECT 'x''; DROP TABLE meta; --' AS "a""b", 3 AS "Users (at least)" UNION ALL SELECT 'y', 1`
	got, err := db.QueryPage(ctx, q, Page{
		Filters: []Filter{{Column: `a"b`, Op: "in", Values: []string{"x'; DROP TABLE meta; --", "z"}}},
		Sort:    &Sort{Column: "Users (at least)"},
	})
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

// A widget column named like a counting column must not collide.
func TestQueryPageCountingColumnsDoNotCollide(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 1000)
	got, err := db.QueryPage(context.Background(),
		`SELECT 1 AS __tw_total, 2 AS __tw_matched, 3 AS __tw_total_1`, Page{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Rows, [][]string{{"1", "2", "3"}}) || got.Total != 1 || got.Matched != 1 {
		t.Errorf("got %+v", got)
	}
}

// Comparisons go through tw_cell and tw_num, not SQLite's affinity:
// projects.name is a TEXT column, so SQLite itself would turn the bound
// 5 into '5' and match 'blog' > '5'. A decimal value compares as a
// number, and a cell that is not one never matches.
func TestQueryPageIgnoresColumnAffinity(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 1000)
	ctx := context.Background()
	q := `SELECT name AS n FROM projects`
	for _, tc := range []struct {
		value string
		want  int
	}{{"5", 0}, {"a", 1}} {
		got, err := db.QueryPage(ctx, q, Page{Filters: []Filter{{Column: "n", Op: ">", Values: []string{tc.value}}}})
		if err != nil {
			t.Fatal(err)
		}
		if got.Matched != tc.want || len(got.Rows) != tc.want {
			t.Errorf("n > %s: matched %d rows %d, want %d", tc.value, got.Matched, len(got.Rows), tc.want)
		}
	}
}

func TestQueryPageRefusals(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 10)
	ctx := context.Background()
	q := `SELECT 1 AS a, 'x' AS b`
	for name, p := range map[string]Page{
		"unknown column lists valid ones":  {Filters: []Filter{{Column: "c", Op: "=", Values: []string{"1"}}}},
		"unknown operator":                 {Filters: []Filter{{Column: "a", Op: "~", Values: []string{"1"}}}},
		"list for a single-value operator": {Filters: []Filter{{Column: "a", Op: "=", Values: []string{"1", "2"}}}},
		"empty in":                         {Filters: []Filter{{Column: "a", Op: "in"}}},
		"unknown sort column":              {Sort: &Sort{Column: "c"}},
		"unknown distinct column":          {Distinct: "c"},
		"distinct with sort":               {Distinct: "a", Sort: &Sort{Column: "a"}},
		"negative offset":                  {Offset: -1},
		"limit above the cap":              {Limit: 11},
		"negative limit":                   {Limit: -1},
	} {
		_, err := db.QueryPage(ctx, q, p)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v, want ErrRefused", name, err)
		}
		if name == "unknown column lists valid ones" && (err == nil || !strings.Contains(err.Error(), "a, b")) {
			t.Errorf("%s: message %v does not list a, b", name, err)
		}
	}
	// What Check refuses, QueryPage refuses too: the wrap is no way around it.
	if _, err := db.QueryPage(ctx, `SELECT * FROM meta`, Page{}); !errors.Is(err, ErrRefused) {
		t.Errorf("meta: err = %v, want ErrRefused", err)
	}
	if _, err := db.QueryPage(ctx, `SELECT 1) UNION SELECT key FROM meta --`, Page{}); err == nil {
		t.Error("an escape from the wrap ran")
	}
}

// The wrap's own values must bind whatever the caller passes for q: a
// query using only some of the named arguments it is given, in any
// order, or plain ? with positional arguments.
func TestQueryPageBindsItsOwnValuesByName(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 1000)
	ctx := context.Background()
	const rows = `FROM json_each('[10,20,30,40,50]') j`
	for name, c := range map[string]struct {
		q    string
		args []any
	}{
		// :to comes first, :project twice, and :from is passed but unused.
		"named": {
			q:    `SELECT :to AS t, j.value AS n, :project AS p ` + rows + ` WHERE j.value <= :to AND :project = 'blog' AND :project <> ''`,
			args: []any{sql.Named("project", "blog"), sql.Named("from", "2026-01-01"), sql.Named("to", 40)},
		},
		"positional": {
			q:    `SELECT ? AS t, j.value AS n, ? AS p ` + rows + ` WHERE j.value <= ?`,
			args: []any{40, "blog", 40},
		},
	} {
		t.Run(name, func(t *testing.T) {
			filters := []Filter{{Column: "n", Op: ">", Values: []string{"10"}}, {Column: "p", Op: "in", Values: []string{"blog", "x"}}}
			sort := &Sort{Column: "n", Desc: true}
			for _, pc := range []struct {
				offset    int
				want      [][]string
				truncated bool
			}{
				{0, [][]string{{"40", "40", "blog"}}, true},
				{1, [][]string{{"40", "30", "blog"}}, true},
				{2, [][]string{{"40", "20", "blog"}}, false},
				{5, [][]string{}, false},
			} {
				got, err := db.QueryPage(ctx, c.q, Page{Filters: filters, Sort: sort, Offset: pc.offset, Limit: 1}, c.args...)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got.Rows, pc.want) || got.Matched != 3 || got.Total != 4 || got.Truncated != pc.truncated {
					t.Errorf("offset %d: got %+v, want rows %q matched 3 total 4 truncated %v", pc.offset, got, pc.want, pc.truncated)
				}
			}
			got, err := db.QueryPage(ctx, c.q, Page{Filters: filters, Distinct: "p"}, c.args...)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Rows, [][]string{{"blog", "3"}}) || got.Matched != 1 || got.Total != 4 {
				t.Errorf("distinct: got %+v", got)
			}
			got, err = db.QueryPage(ctx, c.q, Page{Filters: filters, Distinct: "p", Offset: 3}, c.args...)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Rows) != 0 || got.Matched != 1 || got.Total != 4 {
				t.Errorf("distinct past the end: got %+v", got)
			}
		})
	}
}

// The prefix of the wrap's parameters is the wrap's own.
func TestQueryPageRefusesTheReservedParameterPrefix(t *testing.T) {
	db, _ := newTestDB(t, 5*time.Second, 1000)
	_, err := db.QueryPage(context.Background(), `SELECT :tw_p1 AS a`, Page{}, sql.Named("tw_p1", 1))
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "tw_p") {
		t.Errorf("err = %v, want ErrRefused naming the prefix", err)
	}
}
