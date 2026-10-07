package readsql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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
// no earlier matching '(' would close QueryLimit's own wrapping paren early,
// and a ';' was never treated as ending the statement, so anything after
// it — a second statement — rode along on the same call (the driver runs
// every statement it is handed). An unterminated /* is refused for the
// same reason: concatenated with QueryLimit's own trailing "\n) LIMIT n", it
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
// with nested, balanced parens, and a trailing run of ';' and/or
// whitespace (nothing else — see TestCheckRefusesCommentAfterSemicolon),
// must still pass.
func TestCheckAllowsBalancedParensAndTrailingSemicolon(t *testing.T) {
	for _, q := range []string{
		`select * from (select 1) x`,
		`with n(i) as (values (1),(2),(3)) select i from n`,
		`select 1;`,
		`select 1 ;  `,
		"select 1; ;",
		"select 1;\n",
	} {
		t.Run(q, func(t *testing.T) {
			if _, err := Check(q); err != nil {
				t.Fatalf("Check(%q) = %v, want no error", q, err)
			}
		})
	}
}

// TestCheckRefusesCommentAfterSemicolon pins the fix for the finding
// that Check allowed a comment after a top-level ';' (e.g. "select 1;
// -- trailing"), but QueryLimit's trim only strips a trailing run of ';' and
// whitespace, leaving the ';' and comment inside the wrap — a syntax
// error there, not the clean refusal a caller can act on. Check and
// QueryLimit's trim now agree: nothing but more ';' or whitespace may follow
// the first top-level ';'.
func TestCheckRefusesCommentAfterSemicolon(t *testing.T) {
	for _, q := range []string{
		"select 1; -- trailing",
		"select 1; /* trailing */",
	} {
		t.Run(q, func(t *testing.T) {
			_, err := Check(q)
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "single SELECT or WITH statement") {
				t.Fatalf("Check(%q) = %v, want the single-statement refusal", q, err)
			}
		})
	}
}

// TestCheckRefusesUnbalancedOpenParen pins the fix requiring paren depth
// to be exactly 0 at the end: an unclosed '(' would need the wrap's own
// closing ')' to balance, changing what that ')' actually closes.
func TestCheckRefusesUnbalancedOpenParen(t *testing.T) {
	for _, q := range []string{
		`select * from (select 1`,
		`select (1`,
	} {
		t.Run(q, func(t *testing.T) {
			_, err := Check(q)
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("Check(%q) = %v, want ErrRefused", q, err)
			}
		})
	}
}

// TestCheckRefusesNULByte pins the fix for the finding that Check, which
// walks a Go string with no notion of a string terminator, would happily
// validate text past a NUL byte that SQLite's C-string-based tokenizer
// would never even see, letting the two disagree about what the
// statement even is.
func TestCheckRefusesNULByte(t *testing.T) {
	for _, q := range []string{
		"select 1\x00; drop table x",
		"select '\x00' as x",
		"select 1 -- \x00",
	} {
		t.Run(q, func(t *testing.T) {
			_, err := Check(q)
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("Check(%q) = %v, want ErrRefused", q, err)
			}
		})
	}
}

// TestCheckRefusesTclVariableForms pins the fix for the finding that
// SQLite's tokenizer folds a following "(...)" or "::NAME2" into the
// same variable token for :, @ and $ (a Tcl-variable compatibility
// form), consuming any ')' or quote inside as part of the name rather
// than as SQL — which Check has no matching rule for and would desync
// on. The # sigil (the same family) is refused unconditionally.
func TestCheckRefusesTclVariableForms(t *testing.T) {
	for _, q := range []string{
		`select $x(1)`,
		`select :x(1)`,
		`select @x(1)`,
		`select $x(')') as y`,
		`select $x::y`,
		`select :x::y`,
		`select @x::y`,
		`select #x`,
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
		"select 1 -- meta",
		"select 1 /* sqlite_master */",
		`select metadata, attachment from v_views_paths`,
		`select "it''s" as ok`,
		`select x'00'`,
		// A string literal is only refused on an exact (unescaped)
		// match against a refused name (see TestCheckRefusesStringLiteralsNamingRefusedTables):
		// one that merely contains it, as a substring or a prefix, is
		// an ordinary value, not a table reference.
		`select 'metadata' as x`,
		`select '%meta%' as x`,
		// Non-ASCII stays allowed everywhere the tokenizer never visits
		// the bytes directly: inside a string, a quoted identifier, or
		// a comment.
		`select 'Посетители' as x`,
		`select 1 as "визиты"`,
		"select 1 -- Посетители",
		"select 1 /* Посетители */",
	} {
		t.Run(q, func(t *testing.T) {
			if _, err := Check(q); err != nil {
				t.Fatalf("Check(%q) = %v, want no error", q, err)
			}
		})
	}
}

// TestCheckRefusesBOMBeforeAReservedName pins the fix for a second
// Check/SQLite desync class, closed by refusing any non-ASCII byte
// outright rather than modeling SQLite's whitespace rules: SQLite's
// tokenizer treats a leading UTF-8 BOM (EF BB BF) as whitespace before a
// token, so "select * from \ufeffmeta" reads the plain word "meta" once
// the BOM is skipped, while Check used to fold the BOM's bytes into the
// identifier it was reading — "\ufeffmeta", distinct from "meta" — and
// never refuse it. Six shapes from the finding: a bare name, one inside
// parens, one schema-qualified, and the other two refused names plus a
// pragma view.
func TestCheckRefusesBOMBeforeAReservedName(t *testing.T) {
	for _, q := range []string{
		"select * from \ufeffmeta",
		"select * from (\ufeffmeta)",
		"select * from main.\ufeffmeta",
		"select * from \ufeffsqlite_master",
		"select * from \ufeffdbstat",
		"select * from \ufeffpragma_table_info('events')",
	} {
		t.Run(q, func(t *testing.T) {
			if _, err := Check(q); !errors.Is(err, ErrRefused) {
				t.Fatalf("Check(%q) = %v, want ErrRefused", q, err)
			}
		})
	}
}

// TestCheckRefusesUnquotedNonASCIIIdentifiers pins the general rule
// TestCheckRefusesBOMBeforeAReservedName is one instance of: an
// unquoted identifier is refused the moment it contains a non-ASCII
// byte, whether or not the resulting name happens to be a reserved one.
func TestCheckRefusesUnquotedNonASCIIIdentifiers(t *testing.T) {
	for _, q := range []string{
		`select 1 as méta`,
		`select визиты from events`,
	} {
		t.Run(q, func(t *testing.T) {
			if _, err := Check(q); !errors.Is(err, ErrRefused) {
				t.Fatalf("Check(%q) = %v, want ErrRefused", q, err)
			}
		})
	}
}

// TestCheckRefusesStringLiteralsNamingRefusedTables pins the fix for the
// finding that SQLite's grammar accepts a STRING wherever a NAME is
// expected (nm ::= STRING), so a single-quoted string exactly naming a
// refused table reads it just as an unquoted or double-quoted name
// would — Check used to treat '...' purely as an opaque string literal
// and never asked checkName about its content. The seven shapes from
// the finding: a bare string table name, one schema-qualified, one
// parenthesized, three more refused names as strings, and a
// string-quoted pragma view called like a function.
func TestCheckRefusesStringLiteralsNamingRefusedTables(t *testing.T) {
	for _, q := range []string{
		`select * from 'meta'`,
		`select * from main.'meta'`,
		`select * from (select * from 'meta')`,
		`select * from 'sqlite_master'`,
		`select * from 'sqlite_schema'`,
		`select * from 'dbstat'`,
		`select * from 'pragma_table_info'('events')`,
	} {
		t.Run(q, func(t *testing.T) {
			if _, err := Check(q); !errors.Is(err, ErrRefused) {
				t.Fatalf("Check(%q) = %v, want ErrRefused", q, err)
			}
		})
	}
}

// TestScanNumberSplitsLikeSQLite pins hardening for Check's number scan:
// it used to consume any run of identifier characters and dots after a
// leading digit, so "1.5.meta" or "1..meta" were swallowed whole as one
// "number" token, hiding "meta" from checkName even though SQLite's own
// lexer stops extending a number at (at most) one '.' and reads "meta"
// as a separate, ordinary identifier — refused on its own once Check's
// number scan agrees on where the number ends.
func TestScanNumberSplitsLikeSQLite(t *testing.T) {
	for _, q := range []string{
		`select * from 1.5.meta`,
		`select * from 1..meta`,
	} {
		t.Run(q, func(t *testing.T) {
			if _, err := Check(q); !errors.Is(err, ErrRefused) {
				t.Fatalf("Check(%q) = %v, want ErrRefused (meta named after the number)", q, err)
			}
		})
	}
	// A number's own shape is unaffected: a decimal with one '.', an
	// exponent, and a hex literal all still scan as a single token and
	// pass, with no identifier the number could have swallowed.
	for _, q := range []string{
		`select 1.5 as x`,
		`select 1.5e10 as x`,
		`select 1e-5 as x`,
		`select 0x1F as x`,
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

// TestHandleRefusesItsExtraNames pins the names a handle is opened to
// refuse: every spelling of one is refused by Check, Query and QueryPage
// on that handle (before SQLite sees the text, so the table need not
// exist), while the package-level Check and a handle opened without the
// name still accept them. Another name on the same handle stays readable.
func TestHandleRefusesItsExtraNames(t *testing.T) {
	_, path := newTestDB(t, 2*time.Second, 1000)
	strict, err := Open(path, 2*time.Second, 1000, "secrets")
	if err != nil {
		t.Fatal(err)
	}
	defer strict.Close()
	plain, err := Open(path, 2*time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()

	for _, q := range []string{
		`select * from secrets`,
		`select * from "secrets"`,
		`select * from 'secrets'`,
		`select * from SECRETS`,
		`select * from main.secrets`,
		`select * from (select * from [Secrets])`,
	} {
		t.Run(q, func(t *testing.T) {
			ctx := context.Background()
			if _, err := strict.Check(q); !errors.Is(err, ErrRefused) || !strings.Contains(strings.ToLower(err.Error()), "secrets") {
				t.Errorf("strict.Check = %v, want ErrRefused naming secrets", err)
			}
			if _, err := strict.Query(ctx, q); !errors.Is(err, ErrRefused) {
				t.Errorf("strict.Query = %v, want ErrRefused", err)
			}
			if _, err := strict.QueryLimit(ctx, q, 0); !errors.Is(err, ErrRefused) {
				t.Errorf("strict.QueryLimit = %v, want ErrRefused", err)
			}
			if _, err := strict.QueryPage(ctx, q, Page{}); !errors.Is(err, ErrRefused) {
				t.Errorf("strict.QueryPage = %v, want ErrRefused", err)
			}
			if _, err := Check(q); err != nil {
				t.Errorf("package Check = %v, want it to accept (base rules only)", err)
			}
			if _, err := plain.Check(q); err != nil {
				t.Errorf("plain.Check = %v, want it to accept", err)
			}
		})
	}

	if _, err := strict.Check(`select * from events`); err != nil {
		t.Errorf("strict.Check(events) = %v, want it to accept", err)
	}
	// The base rules still hold on a handle with extras.
	if _, err := strict.Check(`select * from meta`); !errors.Is(err, ErrRefused) {
		t.Errorf("strict.Check(meta) = %v, want ErrRefused", err)
	}
}
