package sqlcheck

import (
	"reflect"
	"testing"
)

func TestStatements(t *testing.T) {
	cases := map[string]struct {
		sql  string
		want []string
	}{
		"one":                {"SELECT 1", []string{"SELECT 1"}},
		"trailing semicolon": {"SELECT 1;  -- done\n", []string{"SELECT 1"}},
		"two":                {"INSERT INTO t VALUES (?); DELETE FROM t WHERE a = ?;", []string{"INSERT INTO t VALUES (?)", "DELETE FROM t WHERE a = ?"}},
		"semicolon quoted":   {"SELECT ';' || \"a;b\" FROM t -- ;\n/* ; */", []string{"SELECT ';' || \"a;b\" FROM t -- ;\n/* ; */"}},
		"comment only":       {"-- nothing\n/* here */", nil},
	}
	for name, c := range cases {
		got, err := statements(c.sql)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestStatementsRefuses(t *testing.T) {
	for name, sql := range map[string]string{
		"unterminated quote":   "SELECT 'abc",
		"unterminated comment": "SELECT 1 /* abc",
		"trigger":              "CREATE TRIGGER x AFTER INSERT ON t BEGIN SELECT 1; END",
		"temp trigger":         "create temp trigger x after insert on t begin select 1; end",
	} {
		if _, err := statements(sql); err == nil {
			t.Errorf("%s: accepted %q", name, sql)
		}
	}
}

func TestBindings(t *testing.T) {
	cases := map[string]struct {
		stmt  string
		max   int
		names map[int]string
	}{
		"none":              {"SELECT 1", 0, nil},
		"anonymous":         {"SELECT ?, ?", 2, nil},
		"numbered reused":   {"SELECT ?1, ?2, ?1", 2, nil},
		"numbered gap":      {"SELECT ?3", 3, nil},
		"anonymous after":   {"SELECT ?, ?5, ?", 6, nil},
		"named":             {"SELECT :n, :n, @m, $o", 3, map[int]string{1: "n", 2: "m", 3: "o"}},
		"json path":         {`SELECT json_extract(a, '$."k"') FROM t WHERE x = ?`, 1, nil},
		"time format":       {`SELECT strftime('%Y-%m-%dT%H:%M:%SZ', 'now')`, 0, nil},
		"quoted and remark": {"SELECT '?' || \"a?\" -- ?\n FROM t /* :x */ WHERE b = ?", 1, nil},
		"escaped quote":     {"SELECT 'it''s ?'", 0, nil},
		"dollar in name":    {"SELECT a$b FROM t", 0, nil},
		"like escape":       {`SELECT a FROM t WHERE b LIKE ? ESCAPE '\'`, 1, nil},
	}
	for name, c := range cases {
		got, err := bindings(c.stmt)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if c.names == nil {
			c.names = map[int]string{}
		}
		if got.max != c.max || !reflect.DeepEqual(got.names, c.names) {
			t.Errorf("%s: got %d %v, want %d %v", name, got.max, got.names, c.max, c.names)
		}
	}
}
