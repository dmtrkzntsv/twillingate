package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestRewriteKeepsRows(t *testing.T) {
	db := duck(t)
	dir := t.TempDir()
	in := filepath.Join(dir, "in.parquet")
	const n, per = 6500, 1000
	// DuckDB groups of 2048 rows, so output groups of 1000 span input groups.
	copyTo(t, db, eventsSQL(n, 31, "2026-08-01", "'a' || (hash(i, 'a') % 53)",
		", CASE WHEN hash(i, 'v') % 9 = 0 THEN NULL ELSE 'v' || (i % 3000) END AS attr_note"), in, 2048)

	if g := rows(t, db, fmt.Sprintf(`SELECT count(DISTINCT row_group_id)::VARCHAR, max(row_group_num_rows)::VARCHAR
		FROM parquet_metadata('%s')`, in)); g[0][0] == "1" || g[0][1] == "1000" {
		t.Fatalf("want several input groups of other than 1000 rows: %v", g)
	}

	sums := func(f string) []Row {
		return rows(t, db, fmt.Sprintf(`SELECT count(*)::VARCHAR, sum(hash(id))::VARCHAR, min(ts)::VARCHAR, max(ts)::VARCHAR
			FROM read_parquet('%s')`, f))
	}
	schema := func(f string) []Row {
		return rows(t, db, fmt.Sprintf(`SELECT name, type, converted_type, logical_type, repetition_type,
			scale, precision FROM parquet_schema('%s') WHERE num_children IS NULL`, f))
	}
	want, wantSchema := sums(in), schema(in)

	for _, lib := range []string{"arrow", "pqgo"} {
		t.Run(lib, func(t *testing.T) {
			out := filepath.Join(dir, lib+".parquet")
			st, err := Rewrite(lib, in, out, per)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(out + ".tmp"); !os.IsNotExist(err) {
				t.Fatalf("%s.tmp left behind: %v", out, err)
			}
			fi, err := os.Stat(out)
			if err != nil {
				t.Fatal(err)
			}
			if st.Bytes != fi.Size() || st.Wall <= 0 {
				t.Fatalf("stats %+v, file %d bytes", st, fi.Size())
			}
			if got := sums(out); !reflect.DeepEqual(got, want) {
				t.Fatalf("sums: got %v, want %v", got, want)
			}
			// Both libraries add the logical type equivalent to a column's
			// converted type where DuckDB wrote only the converted type
			// (INT_64 → IntType(64, signed), UTF8 → StringType, DATE →
			// DateType): the same type, so only a logical type the input
			// has must match.
			got := schema(out)
			for i := range got {
				if i < len(wantSchema) && wantSchema[i][3] == "NULL" {
					got[i][3] = "NULL"
				}
			}
			if !reflect.DeepEqual(got, wantSchema) {
				t.Fatalf("schema:\ngot  %v\nwant %v", got, wantSchema)
			}
			// Every value, in order: file_row_number pairs the rows.
			for _, pair := range [][2]string{{in, out}, {out, in}} {
				diff := rows(t, db, fmt.Sprintf(`SELECT count(*)::VARCHAR FROM (
					SELECT * FROM read_parquet('%s', file_row_number = true)
					EXCEPT ALL SELECT * FROM read_parquet('%s', file_row_number = true))`, pair[0], pair[1]))
				if diff[0][0] != "0" {
					t.Fatalf("%s has %s rows (with their position) missing from %s", pair[0], diff[0][0], pair[1])
				}
			}

			groups := rows(t, db, fmt.Sprintf(`SELECT DISTINCT row_group_id, row_group_num_rows::VARCHAR
				FROM parquet_metadata('%s') ORDER BY row_group_id`, out))
			if len(groups) != (n+per-1)/per || st.RowGroups != len(groups) {
				t.Fatalf("%d row groups (stats say %d), want %d", len(groups), st.RowGroups, (n+per-1)/per)
			}
			for i, g := range groups {
				r, _ := strconv.Atoi(g[1])
				if r > per || (i < len(groups)-1 && r != per) {
					t.Fatalf("row group %d has %d rows: %v", i, r, groups)
				}
			}
			codecs := rows(t, db, fmt.Sprintf(`SELECT DISTINCT compression FROM parquet_metadata('%s')`, out))
			if !reflect.DeepEqual(codecs, []Row{{"ZSTD"}}) {
				t.Fatalf("compression %v", codecs)
			}
			undict := rows(t, db, fmt.Sprintf(`SELECT path_in_schema, encodings FROM parquet_metadata('%s')
				WHERE path_in_schema IN ('family', 'event_name', 'actor_id', 'group_id', 'country', 'attr_plan', 'attr_note')
				AND encodings NOT LIKE '%%DICTIONARY%%'`, out))
			if len(undict) != 0 {
				t.Fatalf("string columns not dictionary-encoded: %v", undict)
			}
			if got := rows(t, db, fmt.Sprintf(`SELECT count(*)::VARCHAR FROM parquet_metadata('%s') WHERE path_in_schema = 'country'`, out)); got[0][0] != strconv.Itoa(len(groups)) {
				t.Fatalf("country chunks: %v", got)
			}
		})
	}
}

func TestRewriteFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.parquet")
	junk := filepath.Join(dir, "junk.parquet")
	if err := os.WriteFile(junk, []byte("not parquet at all, PAR1"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, lib := range []string{"arrow", "pqgo"} {
		for _, in := range []string{filepath.Join(dir, "missing.parquet"), junk} {
			if _, err := Rewrite(lib, in, out, 1000); err == nil {
				t.Fatalf("%s: rewrote %s", lib, in)
			}
			for _, f := range []string{out, out + ".tmp"} {
				if _, err := os.Stat(f); !os.IsNotExist(err) {
					t.Fatalf("%s: %s exists: %v", lib, f, err)
				}
			}
		}
	}
	if _, err := Rewrite("nope", filepath.Join(dir, "missing.parquet"), out, 1000); err == nil {
		t.Fatal("unknown library accepted")
	}
}
