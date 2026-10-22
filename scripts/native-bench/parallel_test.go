package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// scanRows runs ScanParallel on a fresh source and returns the multiset of
// (day, family, actor_id, country) rows, sorted, and the row groups read.
func scanRows(t *testing.T, mk func() Source, files []string, family string, from, to int64, workers int) ([]string, int) {
	t.Helper()
	src := mk()
	per := make([][]string, workers)
	err := ScanParallel(src, files, []string{"day", "family", "actor_id", "country"}, family, from, to, workers,
		func(w int, b *Batch) error {
			str := func(c *Column, i int) string {
				if c.Idx[i] < 0 {
					return "NULL"
				}
				return c.Dict[c.Idx[i]]
			}
			for i := 0; i < b.N; i++ {
				d, _ := intAt(b.Cols["day"], i)
				per[w] = append(per[w], fmt.Sprintf("%d|%s|%s|%s", d,
					str(b.Cols["family"], i), str(b.Cols["actor_id"], i), str(b.Cols["country"], i)))
			}
			return nil
		})
	if err != nil {
		t.Fatalf("%s workers %d: %v", src.Name(), workers, err)
	}
	var all []string
	for _, p := range per {
		all = append(all, p...)
	}
	sort.Strings(all)
	return all, OpenedRowGroups(src)
}

func TestScanParallelMatchesSerial(t *testing.T) {
	db := duck(t)
	files := monthFiles(t, db)
	ranges := [][2]string{
		{"2026-08-01", "2026-09-30"},
		{"2026-08-10", "2026-09-05"},
		{"2026-09-03", "2026-09-03"},
	}
	for _, mk := range []func() Source{NewArrowSource, NewPQGoSource} {
		for _, r := range ranges {
			from, to := day(r[0]), day(r[1])
			one, n1 := scanRows(t, mk, files, "product", from, to, 1)
			four, n4 := scanRows(t, mk, files, "product", from, to, 4)
			name := mk().Name()
			if len(one) == 0 {
				t.Fatalf("%s %v: no rows; the fixture tests nothing", name, r)
			}
			if !reflect.DeepEqual(one, four) {
				t.Fatalf("%s %v: 1 worker read %d rows, 4 workers %d, or they differ", name, r, len(one), len(four))
			}
			if n1 != n4 || n1 < 1 {
				t.Fatalf("%s %v: opened %d row groups with 1 worker, %d with 4", name, r, n1, n4)
			}
			want := rows(t, db, fmt.Sprintf("SELECT count(*)::VARCHAR FROM (%s)", filtered(files, "product", from, to)))
			if fmt.Sprint(len(one)) != want[0][0] {
				t.Fatalf("%s %v: %d rows, want %s", name, r, len(one), want[0][0])
			}
		}
	}
}

func TestGroupsPrunes(t *testing.T) {
	db := duck(t)
	f := filepath.Join(t.TempDir(), "days.parquet")
	copyTo(t, db, `SELECT i AS id, 'product' AS family, DATE '2026-08-01' + CAST(i // 2048 AS INTEGER) AS day,
		'a' || (i % 97) AS actor_id FROM range(30 * 2048) t(i) ORDER BY day`, f, 2048)
	target := "2026-08-15"
	stats := rows(t, db, fmt.Sprintf(`SELECT count(*)::VARCHAR,
		count(*) FILTER (WHERE stats_min::DATE <= DATE '%s' AND stats_max::DATE >= DATE '%s')::VARCHAR
		FROM parquet_metadata('%s') WHERE path_in_schema = 'day'`, target, target, f))
	var total, overlapping int
	fmt.Sscan(stats[0][0], &total)
	fmt.Sscan(stats[0][1], &overlapping)
	if total != 30 || overlapping < 1 {
		t.Fatalf("want 30 row groups and one overlapping the day, got %d and %d", total, overlapping)
	}
	d := day(target)
	for _, src := range sources() {
		gs, err := src.Groups([]string{f}, "product", d, d)
		if err != nil {
			t.Fatal(err)
		}
		if len(gs) < 1 || len(gs) > overlapping {
			t.Fatalf("%s: %d groups for one day, %d overlap it", src.Name(), len(gs), overlapping)
		}
		gs, err = src.Groups([]string{f}, "product", day("2026-08-01"), day("2026-08-30"))
		if err != nil {
			t.Fatal(err)
		}
		if len(gs) != total {
			t.Fatalf("%s: %d groups for the full range, want %d", src.Name(), len(gs), total)
		}
		for i, g := range gs {
			if g.File != f || g.Index != i || g.Rows != 2048 || !g.All {
				t.Fatalf("%s: group %d is %+v", src.Name(), i, g)
			}
		}
		if n := OpenedRowGroups(src); n != 0 {
			t.Fatalf("%s: Groups read %d row groups", src.Name(), n)
		}
	}
}
