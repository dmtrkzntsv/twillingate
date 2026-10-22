package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

func sources() []Source { return []Source{NewArrowSource(), NewPQGoSource()} }

func duck(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func exec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}

// rows runs q and returns every value as a string ("NULL" for null).
func rows(t *testing.T, db *sql.DB, q string) []Row {
	t.Helper()
	rs, err := db.Query(q)
	if err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	out := []Row{}
	for rs.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := make(Row, len(cols))
		for i, v := range vals {
			row[i] = v.String
			if !v.Valid {
				row[i] = "NULL"
			}
		}
		out = append(out, row)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// eventsSQL is a deterministic event set over n rows starting at `start`,
// spread over `days` days; extra adds attribute columns.
func eventsSQL(n, days int, start, actor, extra string) string {
	return fmt.Sprintf(`
SELECT i AS id, 1 AS project_id,
  ['views', 'product', 'measures'][CAST(hash(i, 'f') %% 3 AS BIGINT) + 1] AS family,
  'ev' || (hash(i, 'e') %% 4) AS event_name,
  CAST(DATE '%[3]s' + CAST(i %% %[2]d AS INTEGER) AS TIMESTAMP) + to_seconds(CAST(hash(i, 't') %% 86400 AS BIGINT)) AS ts,
  DATE '%[3]s' + CAST(i %% %[2]d AS INTEGER) AS day,
  CASE WHEN hash(i, 'n') %% 17 = 0 THEN NULL ELSE %[4]s END AS actor_id,
  CASE hash(i, 'g') %% 5 WHEN 0 THEN '' WHEN 1 THEN NULL ELSE 'g' || (hash(i, 'g') %% 7) END AS group_id,
  CASE hash(i, 'c') %% 11 WHEN 0 THEN '' WHEN 1 THEN NULL
    ELSE ['DE', 'US', 'FR', 'GB', 'JP', 'BR'][CAST(hash(i, 'c') %% 6 AS BIGINT) + 1] END AS country,
  CASE WHEN hash(i, 'p') %% 4 = 0 THEN NULL ELSE ['free', 'pro', 'team'][CAST(hash(i, 'p') %% 3 AS BIGINT) + 1] END AS attr_plan
  %[5]s
FROM range(%[1]d) t(i)
ORDER BY family, ts`, n, days, start, actor, extra)
}

func copyTo(t *testing.T, db *sql.DB, query, path string, groupSize int) {
	t.Helper()
	exec(t, db, fmt.Sprintf("COPY (%s) TO '%s' (FORMAT parquet, ROW_GROUP_SIZE %d)", query, path, groupSize))
}

// monthFiles writes two months: August with attr_variant and a
// low-cardinality actor, September without attr_variant, with attr_color
// and a near-unique actor (so DuckDB may write it plain, not dictionary).
func monthFiles(t *testing.T, db *sql.DB) []string {
	t.Helper()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "2026-08.parquet"), filepath.Join(dir, "2026-09.parquet")
	copyTo(t, db, eventsSQL(6000, 31, "2026-08-01", "'a' || (hash(i, 'a') % 53)",
		", CASE WHEN hash(i, 'v') % 9 = 0 THEN NULL WHEN hash(i, 'v') % 2 = 0 THEN 'A' ELSE 'B' END AS attr_variant"), a, 1000)
	copyTo(t, db, eventsSQL(5000, 30, "2026-09-01", "'u' || (i % 4000)",
		", CASE WHEN hash(i, 'k') % 3 = 0 THEN NULL ELSE ['red', 'blue'][CAST(hash(i, 'k') % 2 AS BIGINT) + 1] END AS attr_color"), b, 1000)
	for _, f := range []string{a, b} {
		for _, r := range rows(t, db, fmt.Sprintf(`SELECT path_in_schema, string_agg(DISTINCT encodings, ' | '), count(*)
			FROM parquet_metadata('%s') WHERE path_in_schema IN ('actor_id', 'country', 'day') GROUP BY 1 ORDER BY 1`, f)) {
			t.Logf("%s: %v", filepath.Base(f), r)
		}
	}
	return []string{a, b}
}

func fileList(files []string) string {
	q := make([]string, len(files))
	for i, f := range files {
		q[i] = "'" + f + "'"
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func day(s string) int64 {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d.Unix() / 86400
}

// filtered is the oracle's input: the files' rows of one family in range.
func filtered(files []string, family string, from, to int64) string {
	return fmt.Sprintf(`SELECT * FROM read_parquet(%s, union_by_name = true)
		WHERE family = '%s' AND day BETWEEN DATE '%s' AND DATE '%s'`,
		fileList(files), family, formatDay(from), formatDay(to))
}

func oracleCountries(t *testing.T, db *sql.DB, files []string, from, to int64) []Row {
	return rows(t, db, fmt.Sprintf(`WITH e AS (%s),
d AS (SELECT country, count(DISTINCT actor_id) AS v FROM e WHERE country <> '' GROUP BY day, country)
SELECT country, CAST(sum(v) AS BIGINT)::VARCHAR FROM d GROUP BY country ORDER BY sum(v) DESC, country LIMIT 20`,
		filtered(files, "views", from, to)))
}

func oracleDAU(t *testing.T, db *sql.DB, files []string, from, to int64) []Row {
	return rows(t, db, fmt.Sprintf(`WITH e AS (%s)
SELECT day::VARCHAR, count(DISTINCT actor_id)::VARCHAR FROM e GROUP BY day ORDER BY day`,
		filtered(files, "product", from, to)))
}

func oracleAttributeValues(t *testing.T, db *sql.DB, files []string, from, to int64) []Row {
	return rows(t, db, fmt.Sprintf(`WITH e AS (%s),
a AS (UNPIVOT (SELECT actor_id, nullif(group_id, '') AS g, COLUMNS('^attr_') FROM e)
      ON COLUMNS('^attr_') INTO NAME k VALUE v)
SELECT substr(k, 6), v, count(*)::VARCHAR,
       (count(DISTINCT actor_id) + CAST(bool_or(actor_id IS NULL) AS INTEGER))::VARCHAR,
       count(DISTINCT g)::VARCHAR
FROM a GROUP BY k, v ORDER BY substr(k, 6), count(*) DESC, v`,
		filtered(files, "product", from, to)))
}

type query struct {
	name   string
	native func(Source, []string, int64, int64) ([]Row, error)
	oracle func(*testing.T, *sql.DB, []string, int64, int64) []Row
}

var queries = []query{
	{"Countries", Countries, oracleCountries},
	{"DAU", DAU, oracleDAU},
	{"AttributeValues", AttributeValues, oracleAttributeValues},
}

func match(t *testing.T, db *sql.DB, src Source, q query, files []string, from, to int64) []Row {
	t.Helper()
	got, err := q.native(src, files, from, to)
	if err != nil {
		t.Fatalf("%s %s: %v", src.Name(), q.name, err)
	}
	want := q.oracle(t, db, files, from, to)
	if len(want) == 0 {
		t.Fatalf("%s: the oracle returned no rows; the fixture tests nothing", q.name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s %s [%s..%s]:\ngot  %v\nwant %v", src.Name(), q.name, formatDay(from), formatDay(to), got, want)
	}
	return got
}

func TestQueriesMatchDuckDB(t *testing.T) {
	db := duck(t)
	files := monthFiles(t, db)
	ranges := [][2]string{
		{"2026-08-01", "2026-09-30"}, // both files whole
		{"2026-08-10", "2026-09-05"}, // edges inside both
		{"2026-09-03", "2026-09-03"}, // one day of the second file
	}
	for _, src := range sources() {
		for _, q := range queries {
			for _, r := range ranges {
				match(t, db, src, q, files, day(r[0]), day(r[1]))
			}
		}
	}
}

func TestRangeEdgeRowsFiltered(t *testing.T) {
	db := duck(t)
	f := filepath.Join(t.TempDir(), "edge.parquet")
	// 900 rows over 3 days, every family mixed in: one row group.
	copyTo(t, db, eventsSQL(900, 3, "2026-08-01", "'a' || (hash(i, 'a') % 40)", ""), f, 100000)
	if n := rows(t, db, fmt.Sprintf("SELECT count(DISTINCT row_group_id)::VARCHAR FROM parquet_metadata('%s')", f)); n[0][0] != "1" {
		t.Fatalf("want one row group, got %v", n)
	}
	d := day("2026-08-02")
	for _, src := range sources() {
		for _, q := range queries {
			match(t, db, src, q, []string{f}, d, d)
		}
		got, _ := DAU(src, []string{f}, d, d)
		if len(got) != 1 || got[0][0] != "2026-08-02" {
			t.Fatalf("%s: DAU over one day: %v", src.Name(), got)
		}
		n := 0
		err := src.Scan([]string{f}, []string{"day"}, "product", d, d, func(b *Batch) error {
			for i := 0; i < b.N; i++ {
				if b.Cols["day"].Ints[i] != d {
					t.Fatalf("%s: day %d outside the range", src.Name(), b.Cols["day"].Ints[i])
				}
			}
			n += b.N
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		want := rows(t, db, fmt.Sprintf("SELECT count(*)::VARCHAR FROM (%s)", filtered([]string{f}, "product", d, d)))
		if fmt.Sprint(n) != want[0][0] {
			t.Fatalf("%s: %d rows, want %s", src.Name(), n, want[0][0])
		}
	}
}

func TestMissingColumnReadsNull(t *testing.T) {
	db := duck(t)
	files := monthFiles(t, db)
	from, to := day("2026-08-01"), day("2026-09-30")
	for _, src := range sources() {
		got := match(t, db, src, queries[2], files, from, to)
		var variant, color bool
		for _, r := range got {
			variant = variant || r[0] == "variant"
			color = color || r[0] == "color"
		}
		if !variant || !color {
			t.Fatalf("%s: want both attr_variant and attr_color rows: %v", src.Name(), got)
		}
		// Named explicitly, a column the file lacks reads all null.
		rowsSeen := 0
		err := src.Scan(files[1:], []string{"attr_variant", "day"}, "product", from, to, func(b *Batch) error {
			c := b.Cols["attr_variant"]
			if c == nil || len(c.Idx) != b.N {
				t.Fatalf("%s: attr_variant missing from the batch: %+v", src.Name(), c)
			}
			for _, x := range c.Idx {
				if x != -1 {
					t.Fatalf("%s: attr_variant not null", src.Name())
				}
			}
			rowsSeen += b.N
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if rowsSeen == 0 {
			t.Fatalf("%s: no rows", src.Name())
		}
	}
}

func TestPrunesRowGroupsByDay(t *testing.T) {
	db := duck(t)
	f := filepath.Join(t.TempDir(), "days.parquet")
	copyTo(t, db, `SELECT i AS id, 'product' AS family, DATE '2026-08-01' + CAST(i // 2048 AS INTEGER) AS day,
		'a' || (i % 97) AS actor_id FROM range(30 * 2048) t(i) ORDER BY day`, f, 2048)
	target := "2026-08-15"
	stats := rows(t, db, fmt.Sprintf(`SELECT count(*)::VARCHAR,
		count(*) FILTER (WHERE stats_min::DATE <= DATE '%s' AND stats_max::DATE >= DATE '%s')::VARCHAR
		FROM parquet_metadata('%s') WHERE path_in_schema = 'day'`, target, target, f))
	groups, overlapping := stats[0][0], stats[0][1]
	t.Logf("row groups %s, overlapping %s", groups, overlapping)
	var total, limit int
	fmt.Sscan(groups, &total)
	fmt.Sscan(overlapping, &limit)
	if total < 20 || limit < 1 {
		t.Fatalf("want about 30 row groups and one overlapping the day, got %d and %d", total, limit)
	}
	d := day(target)
	for _, mk := range []func() Source{NewArrowSource, NewPQGoSource} {
		src := mk()
		got, err := DAU(src, []string{f}, d, d)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, []Row{{target, "97"}}) {
			t.Fatalf("%s: %v", src.Name(), got)
		}
		if n := OpenedRowGroups(src); n < 1 || n > limit || (limit <= 2 && n > 2) {
			t.Fatalf("%s: opened %d row groups, %s overlap the day", src.Name(), n, overlapping)
		}
	}
}
