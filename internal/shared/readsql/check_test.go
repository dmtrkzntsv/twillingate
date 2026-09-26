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

// TestCheckRefusesEscapesFromTheWrap pins the fix for the finding that
// Check did not track parens or statement boundaries: a stray ')' with
// no earlier matching '(' would close Query's own wrapping paren early,
// and a ';' was never treated as ending the statement, so anything after
// it — a second statement — rode along on the same call (the driver runs
// every statement it is handed). An unterminated /* is refused for the
// same reason: concatenated with Query's own trailing "\n) LIMIT n", it
// would swallow that suffix.
func TestCheckRefusesEscapesFromTheWrap(t *testing.T) {
	for _, q := range []string{
		`select 1); select 2`,
		`select 1); select 2; select * from (select 3`,
		`select 1 where 0); PRAGMA query_only=0; VACUUM INTO 'x.db'; select * from (select 1`,
		`select 1); CREATE TEMP VIEW v AS SELECT 1; select * from (select 1`,
		`select 1 /* unterminated`,
	} {
		t.Run(q, func(t *testing.T) {
			_, err := Check(q)
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("Check(%q) = %v, want ErrRefused", q, err)
			}
		})
	}
}

// TestCheckAllowsBalancedParensAndTrailingSemicolon guards against the
// escape-detection above being too strict: legitimate single statements
// with nested parens, and exactly one trailing semicolon (optionally
// followed by whitespace or a comment), must still pass.
func TestCheckAllowsBalancedParensAndTrailingSemicolon(t *testing.T) {
	for _, q := range []string{
		`select * from (select 1) x`,
		`with n(i) as (values (1),(2),(3)) select i from n`,
		`select 1;`,
		`select 1 ;  `,
		"select 1; -- trailing",
	} {
		t.Run(q, func(t *testing.T) {
			if _, err := Check(q); err != nil {
				t.Fatalf("Check(%q) = %v, want no error", q, err)
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
