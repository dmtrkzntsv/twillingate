package sqlbind

import (
	"errors"
	"reflect"
	"testing"
)

func TestParams(t *testing.T) {
	for _, c := range []struct {
		name, q string
		want    []string
	}{
		{"a cast", `SELECT day::DATE FROM t`, nil},
		{"a cast with arguments", `SELECT x::DECIMAL(10,2) FROM t`, nil},
		{"a named argument", `SELECT * FROM read_csv('f', header := true)`, nil},
		{"a string", `SELECT 'a :from b'`, nil},
		{"a doubled quote", `SELECT 'it''s :from'`, nil},
		{"an escape string", `SELECT E'it\'s :to'`, nil},
		{"a lower-case escape string", `SELECT e'\\' AS x, e'\' :to'`, nil},
		{"a dollar-quoted string", `SELECT $$ :project $$`, nil},
		{"a tagged dollar-quoted string", `SELECT $q$ :x $ $$ $q$`, nil},
		{"a quoted identifier", `SELECT "col :x" FROM t`, nil},
		{"a line comment", "SELECT 1 -- :x", nil},
		{"a line comment ends at a carriage return", "SELECT 1 -- x\r, :to", []string{":to"}},
		{"a block comment", `SELECT /* :x */ 1`, nil},
		{"a nested block comment", `SELECT /* a /* b */ :x */ 1`, nil},
		{"order of first use", `WHERE p = :project AND d BETWEEN :from AND :to AND :project = 1`,
			[]string{":project", ":from", ":to"}},
		{"a slice reads the upper bound", `SELECT list[a:b] FROM t`, []string{":b"}},
		{"a lambda reads its body", `SELECT list_transform(l, lambda x:x+1) FROM t`, []string{":x"}},
		{"a struct literal reads its value", `SELECT {'k':v} FROM t`, []string{":v"}},
		{"a space after the colon avoids it", `SELECT list[a: b], list_transform(l, lambda x: x+1), {'k': v} FROM t`, nil},
		{"an identifier holding a dollar", `SELECT 1 AS a$b, :to`, []string{":to"}},
		{"after a string", `SELECT 'x' || :from`, []string{":from"}},
		{"trailing semicolons and space", "SELECT 1;  ;\n", nil},
	} {
		got, err := Params(c.q)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Params(%q) = %q, want %q", c.name, c.q, got, c.want)
		}
	}
}

func TestParamsRefusals(t *testing.T) {
	for name, q := range map[string]string{
		"a positional ?":                         `SELECT ?`,
		"a positional $1":                        `SELECT $1`,
		"a named $name":                          `SELECT $name`,
		"an unmatched )":                         `SELECT 1)`,
		"an unmatched ) after a carriage return": "SELECT 1 --\r) UNION ALL (SELECT 2",
		"an unclosed (":                          `SELECT (1`,
		"a second statement":                     `SELECT 1; SELECT 2`,
		"a comment after the statement":          "SELECT 1; -- x",
		"an unterminated string":                 `SELECT 'a`,
		"an unterminated escape string":          `SELECT E'a\'`,
		"an unterminated identifier":             `SELECT "a`,
		"an unterminated $$":                     `SELECT $$ a`,
		"an unterminated $tag$":                  `SELECT $q$ a $$`,
		"an unterminated comment":                `SELECT /* a`,
		"an unterminated nested comment":         `SELECT /* a /* b */ 1`,
		"a NUL":                                  "SELECT 1\x00",
		"a NUL in a string":                      "SELECT '\x00'",
	} {
		if _, err := Params(q); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: Params(%q) err = %v, want ErrRefused", name, q, err)
		}
	}
}

func TestBind(t *testing.T) {
	q, args, err := Bind(`SELECT day FROM t WHERE day BETWEEN :from AND :to`, map[string]Value{
		":from":    {"DATE", "2026-01-01"},
		":to":      {"DATE", "2026-01-31"},
		":project": {"BIGINT", "7"}, // not used: no argument for it
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := `SELECT day FROM t WHERE day BETWEEN CAST($tw_from AS DATE) AND CAST($tw_to AS DATE)`; q != want {
		t.Errorf("q = %q, want %q", q, want)
	}
	if want := map[string]string{"tw_from": "2026-01-01", "tw_to": "2026-01-31"}; !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
}

// Bind replaces at the scanner's offsets: a name repeated, a name inside
// a string or comment, and a name glued to an identifier.
func TestBindReplacesOnlyParameters(t *testing.T) {
	q, args, err := Bind(`SELECT ':to', :to /* :to */, x:to, :to::VARCHAR`, map[string]Value{":to": {"BIGINT", "5"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT ':to', CAST($tw_to AS BIGINT) /* :to */, x CAST($tw_to AS BIGINT), CAST($tw_to AS BIGINT)::VARCHAR`
	if q != want {
		t.Errorf("q = %q, want %q", q, want)
	}
	if !reflect.DeepEqual(args, map[string]string{"tw_to": "5"}) {
		t.Errorf("args = %v", args)
	}
}

func TestBindRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		q    string
		vals map[string]Value
	}{
		"a name without a value":    {`SELECT :from, :to`, map[string]Value{":from": {"DATE", "2026-01-01"}}},
		"text Params refuses":       {`SELECT ?`, nil},
		"a type that is not a name": {`SELECT :from`, map[string]Value{":from": {"DATE) OR (1", "x"}}},
	} {
		if _, _, err := Bind(c.q, c.vals); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v, want ErrRefused", name, err)
		}
	}
}

func TestTrim(t *testing.T) {
	if got, want := Trim("SELECT 1 AS a -- one\n;; \t\n"), "SELECT 1 AS a -- one"; got != want {
		t.Errorf("Trim = %q, want %q", got, want)
	}
}
