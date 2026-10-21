package enginesql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

// Querier is the slice of engine.Engine Confine uses.
type Querier interface {
	Query(ctx context.Context, sql string, maxRows int, args ...engine.Arg) (engine.Result, error)
}

// generators are the table functions SQL may call in FROM: they make
// rows from their arguments alone and read no file and no catalog.
var generators = map[string]bool{"range": true, "generate_series": true, "unnest": true}

// parseSQL has the engine's DuckDB parse its argument into its syntax
// tree, as JSON, running nothing: the parse the child's own check of one
// SELECT makes.
const parseSQL = "SELECT json_serialize_sql(CAST($tw_sql AS VARCHAR))::VARCHAR"

// Confine refuses q, one SELECT for the engine, unless everything it
// reads is one of tables or a WITH name of its own. DuckDB parses q (on
// eng, which runs nothing of it) and the syntax tree is walked, so
// comments, string literals, quoted identifiers and case read as DuckDB
// reads them. It refuses, as ErrRefused with a message that says what to
// change:
//
//   - a table function other than range, generate_series and unnest:
//     read_parquet, read_csv, glob, query and the duckdb_* functions read
//     files or the catalog;
//   - a name in FROM that is neither one of tables nor a WITH name in
//     scope there, a file's path ('/etc/passwd', which DuckDB would read)
//     and the catalog's other objects included;
//   - a name with a schema or catalog (main.events), which reads the
//     catalog's object past a CTE of the same name;
//   - SHOW over the catalog (DESCRIBE and SUMMARIZE of a query or table
//     are walked like any query);
//   - anything else in FROM the walk does not know.
//
// A WITH name is in scope in the query that defines it, its subqueries,
// and the bodies of the WITH names after it; in its own body only when it
// is recursive. Elsewhere DuckDB reads the catalog's object of that name,
// so there the name must be one of tables. A string is never a path to
// DuckDB unless a table function or FROM takes it, so a path compared as
// text passes.
//
// q that does not parse, and q that is not one SELECT, fail as the
// child's own check fails them, as an *engine.QueryError: what the walk
// cannot read never runs.
func Confine(ctx context.Context, eng Querier, q string, tables []string) error {
	res, err := eng.Query(ctx, parseSQL, 1, engine.Arg{Name: "tw_sql", Value: q})
	if err != nil {
		return err
	}
	if len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
		return errors.New("enginesql: the parse returned no single cell")
	}
	text, ok := res.Rows[0][0].(string)
	if !ok {
		return fmt.Errorf("enginesql: the parse returned %T", res.Rows[0][0])
	}
	return confineTree(text, tables)
}

// confineTree is Confine on tree, json_serialize_sql's output for q.
func confineTree(tree string, tables []string) error {
	var parsed struct {
		Error      bool   `json:"error"`
		Type       string `json:"error_type"`
		Message    string `json:"error_message"`
		Statements []any  `json:"statements"`
	}
	if err := json.Unmarshal([]byte(tree), &parsed); err != nil {
		return fmt.Errorf("enginesql: the parse: %w", err)
	}
	if parsed.Error && parsed.Type == "parser" {
		return &engine.QueryError{Msg: "engine: " + parsed.Message}
	}
	if parsed.Error || len(parsed.Statements) != 1 {
		return &engine.QueryError{Msg: "engine: a query must be one SELECT statement"}
	}
	w := walker{tables: map[string]bool{}, list: tableList(tables)}
	for _, t := range tables {
		w.tables[strings.ToLower(t)] = true
	}
	return w.walk(parsed.Statements[0], nil)
}

// tableList is tables as prose: "a, b and c".
func tableList(tables []string) string {
	if len(tables) < 2 {
		return strings.Join(tables, "")
	}
	return strings.Join(tables[:len(tables)-1], ", ") + " and " + tables[len(tables)-1]
}

// scope is the WITH names in scope at a point of the tree, lower case.
type scope struct {
	names  map[string]bool
	parent *scope
}

func (s *scope) has(name string) bool {
	for ; s != nil; s = s.parent {
		if s.names[name] {
			return true
		}
	}
	return false
}

type walker struct {
	tables map[string]bool
	list   string
}

// refuse is a refusal naming what, with the rule.
func (w walker) refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s; SQL here reads only the tables %s, by their plain names, and its own WITH names",
		ErrRefused, fmt.Sprintf(format, args...), w.list)
}

// walk checks every table reference under v, the tree as encoding/json
// decodes it, with sc the WITH names in scope. Keys are walked in order,
// so the first refusal is the same on every run.
func (w walker) walk(v any, sc *scope) error {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if err := w.walk(e, sc); err != nil {
				return err
			}
		}
	case map[string]any:
		if ctes, ok := x["cte_map"]; ok {
			inner, err := w.withNames(ctes, sc)
			if err != nil {
				return err
			}
			sc = inner
		} else if isTableRef(x) {
			if err := w.tableRef(x, sc); err != nil {
				return err
			}
		}
		for _, k := range sortedKeys(x) {
			if k == "cte_map" {
				continue
			}
			if err := w.walk(x[k], sc); err != nil {
				return err
			}
		}
	}
	return nil
}

// withNames walks the bodies of a query node's WITH names (its cte_map),
// each with the names before it in scope and itself when recursive, and
// returns the scope of the node itself: all of them.
func (w walker) withNames(ctes any, sc *scope) (*scope, error) {
	m, ok := ctes.(map[string]any)
	if !ok {
		return nil, w.refuse("a WITH clause the check cannot read")
	}
	entries, _ := m["map"].([]any)
	inner := &scope{names: map[string]bool{}, parent: sc}
	for _, e := range entries {
		em, ok := e.(map[string]any)
		key, kok := em["key"].(string)
		if !ok || !kok {
			return nil, w.refuse("a WITH clause the check cannot read")
		}
		key = strings.ToLower(key)
		body := inner
		if recursive(em["value"]) {
			body = &scope{names: map[string]bool{key: true}, parent: inner}
		}
		if err := w.walk(em["value"], body); err != nil {
			return nil, err
		}
		inner.names[key] = true
	}
	return inner, nil
}

// recursive reports a WITH name's value whose query is a recursive CTE.
func recursive(v any) bool {
	m, _ := v.(map[string]any)
	q, _ := m["query"].(map[string]any)
	n, _ := q["node"].(map[string]any)
	return n["type"] == "RECURSIVE_CTE_NODE"
}

// isTableRef reports a node of FROM: DuckDB serializes every table
// reference with a sample, as a query node does too (which has
// modifiers), and an expression never (it has a class).
func isTableRef(m map[string]any) bool {
	_, sample := m["sample"]
	_, class := m["class"]
	_, modifiers := m["modifiers"]
	_, typ := m["type"].(string)
	return sample && typ && !class && !modifiers
}

// tableRef refuses one node of FROM that reads past the tables.
func (w walker) tableRef(m map[string]any, sc *scope) error {
	switch m["type"] {
	case "BASE_TABLE":
		name, _ := m["table_name"].(string)
		if q := qualified(m, "catalog_name", "schema_name"); q != "" {
			return w.refuse("%s%s names a schema", q, name)
		}
		if low := strings.ToLower(name); w.tables[low] || sc.has(low) {
			return nil
		}
		return w.refuse("%q is not a table here", name)
	case "TABLE_FUNCTION":
		fn, _ := m["function"].(map[string]any)
		name, _ := fn["function_name"].(string)
		q := qualified(fn, "catalog", "schema")
		if q == "" && generators[strings.ToLower(name)] {
			return nil
		}
		return w.refuse("%s%s() is not allowed (only range, generate_series and unnest are)", q, strings.ToLower(name))
	case "SHOW_REF":
		if m["query"] == nil {
			return w.refuse("SHOW lists the catalog; DESCRIBE a table for its columns")
		}
		return nil
	case "SUBQUERY", "JOIN", "EXPRESSION_LIST", "EMPTY", "PIVOT":
		return nil
	}
	return w.refuse("%v in FROM is not allowed", m["type"])
}

// qualified is m's catalog and schema names (by those keys) as a prefix,
// "memory.main.", or "" when it has neither.
func qualified(m map[string]any, keys ...string) string {
	var out string
	for _, k := range keys {
		if s, _ := m[k].(string); s != "" {
			out += s + "."
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
