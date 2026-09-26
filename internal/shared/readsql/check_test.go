package readsql

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckRefusesReadsOfMetaAndSQLiteInternals(t *testing.T) {
	for _, q := range []string{
		`SELECT * FROM meta`,
		`select * from "meta"`,
		"select * from `meta`",
		`select * from [meta]`,
		`select * from main.meta`,
		`SELECT * FROM META`,
		`select * from sqlite_master`,
		`select * from sqlite_schema`,
		`select * from sqlite_sequence`,
		`select * from sqlite_stat1`,
		`select * from sqlite_dbpage`,
		`select * from pragma_table_info('events')`,
		`select * from dbstat`,
	} {
		t.Run(q, func(t *testing.T) {
			_, err := Check(q)
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("Check(%q) = %v, want ErrRefused", q, err)
			}
		})
	}
}

func TestCheckRefusesAttach(t *testing.T) {
	for _, q := range []string{
		`ATTACH 'x.db' AS y`,
		`select 1; attach 'x' as y`,
	} {
		t.Run(q, func(t *testing.T) {
			_, err := Check(q)
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("Check(%q) = %v, want ErrRefused", q, err)
			}
		})
	}
}

func TestCheckMessageNamesTheIdentifier(t *testing.T) {
	_, err := Check(`select * from meta`)
	if err == nil || !strings.Contains(err.Error(), "meta") {
		t.Fatalf("Check error = %v, want it to name meta", err)
	}
}

func TestCheckPasses(t *testing.T) {
	for _, q := range []string{
		`select 'meta' as x`,
		"select 1 -- meta",
		"select 1 /* sqlite_master */",
		`select metadata, attachment from v_views_paths`,
		`select "it''s" as ok`,
		`select x'00'`,
	} {
		t.Run(q, func(t *testing.T) {
			if _, err := Check(q); err != nil {
				t.Fatalf("Check(%q) = %v, want no error", q, err)
			}
		})
	}
}

func TestCheckReturnsNamedParametersInFirstUseOrder(t *testing.T) {
	got, err := Check(`select :project, :from, @x, $y, ?, ?2, :project`)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	want := []string{":project", ":from", "@x", "$y", "?", "?2"}
	if len(got) != len(want) {
		t.Fatalf("params = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("params = %v, want %v", got, want)
		}
	}
}
