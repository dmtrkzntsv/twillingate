package sqlcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sink is a function or method that runs SQL: the argument at index sql
// is the statement, and the arguments from index args on are its
// parameters (args < 0: the parameters are not counted).
type sink struct{ sql, args int }

// baseSinks are the database/sql methods, matched by name on any receiver.
var baseSinks = map[string]sink{
	"ExecContext":     {sql: 1, args: 2},
	"QueryContext":    {sql: 1, args: 2},
	"QueryRowContext": {sql: 1, args: 2},
	"PrepareContext":  {sql: 1, args: -1},
}

// readers are the sinks whose rows a Scan reads (D6).
var readers = map[string]bool{"QueryContext": true, "QueryRowContext": true}

// stmtCalls are the methods of a prepared *sql.Stmt that bind its
// parameters: every argument after the context.
var stmtCalls = map[string]bool{"ExecContext": true, "QueryContext": true, "QueryRowContext": true}

// site is one call that carries SQL.
type site struct {
	pos   string   // file:line, the file relative to the scanned root
	key   string   // file:Func, the dynamic list's key
	sqls  []string // what the SQL argument resolves to; nil when dynamic
	why   string   // for a dynamic site, what did not resolve
	nargs []int    // argument counts passed for the parameters, one per call checked
	read  bool     // a database/sql read: its rows can be scanned
	scans []int    // Scan destination counts found for its rows
}

type file struct {
	rel string
	ast *ast.File
}

// scanSource parses every non-test .go file under root, keeping each
// directory's files together as a package, and returns their SQL sites.
func scanSource(root string) ([]site, error) {
	src := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (name == "testdata" || name == "node_modules" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		src[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return scanFiles(src)
}

// scanFiles is scanSource over sources in memory, keyed by slash path:
// the files of one directory form one package.
func scanFiles(src map[string]string) ([]site, error) {
	fset := token.NewFileSet()
	pkgs := map[string][]file{}
	names := make([]string, 0, len(src))
	for name := range src {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, src[name], parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		dir := pathDir(name)
		pkgs[dir] = append(pkgs[dir], file{rel: name, ast: f})
	}

	sinks := map[string]sink{}
	for k, v := range baseSinks {
		sinks[k] = v
	}
	passThrough := discoverSinks(pkgs, sinks)

	var out []site
	for _, dir := range sortedDirs(pkgs) {
		files := pkgs[dir]
		consts, types := packageDecls(files)
		for _, f := range files {
			for _, d := range f.ast.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				out = append(out, funcSites(fset, f.rel, fn, sinks, passThrough[fn], consts, types)...)
			}
		}
	}
	return out, nil
}

func pathDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}

// calleeName is the name a call is made by: f(…) or x.f(…).
func calleeName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// paramList flattens a function's parameters to their names, in order,
// and reports which are strings and whether the last is variadic.
func paramList(fn *ast.FuncDecl) (names []string, isString []bool, variadic bool) {
	for _, f := range fn.Type.Params.List {
		id, str := f.Type.(*ast.Ident)
		_, variadic = f.Type.(*ast.Ellipsis)
		for _, n := range f.Names {
			names = append(names, n.Name)
			isString = append(isString, str && id.Name == "string")
		}
	}
	return names, isString, variadic
}

// sortedDirs returns the directories of pkgs in order.
func sortedDirs(pkgs map[string][]file) []string {
	dirs := make([]string, 0, len(pkgs))
	for dir := range pkgs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

// discoverSinks adds to sinks every function that hands one of its own
// string parameters to a sink as the SQL, repeating until nothing
// changes, and returns, per such function, the parameter that carries it:
// a call inside it on that parameter is not a site, its callers' are.
func discoverSinks(pkgs map[string][]file, sinks map[string]sink) map[*ast.FuncDecl]string {
	through := map[*ast.FuncDecl]string{}
	dirs := sortedDirs(pkgs)
	for changed := true; changed; {
		changed = false
		for _, dir := range dirs { // in order, so the first of two wrappers with a name is always the same
			for _, f := range pkgs[dir] {
				for _, d := range f.ast.Decls {
					fn, ok := d.(*ast.FuncDecl)
					if !ok || fn.Body == nil {
						continue
					}
					if _, done := through[fn]; done {
						continue
					}
					names, isString, variadic := paramList(fn)
					ast.Inspect(fn.Body, func(n ast.Node) bool {
						c, ok := n.(*ast.CallExpr)
						if !ok {
							return true
						}
						s, ok := sinks[calleeName(c)]
						if !ok || len(c.Args) <= s.sql {
							return true
						}
						id, ok := c.Args[s.sql].(*ast.Ident)
						if !ok {
							return true
						}
						for p, name := range names {
							if name != id.Name || !isString[p] {
								continue
							}
							args := -1
							if variadic && s.args >= 0 && c.Ellipsis.IsValid() {
								if last, ok := c.Args[len(c.Args)-1].(*ast.Ident); ok && last.Name == names[len(names)-1] {
									args = len(names) - 1
								}
							}
							if _, known := sinks[fn.Name.Name]; !known {
								sinks[fn.Name.Name] = sink{sql: p, args: args}
							}
							through[fn] = name
							changed = true
							return false
						}
						return true
					})
				}
			}
		}
	}
	return through
}

// packageDecls collects what a package declares at its top level that a
// SQL argument may name: constants, variables that nothing in the package
// assigns again, and struct types.
func packageDecls(files []file) (map[string]ast.Expr, map[string]*ast.StructType) {
	globals := map[string]ast.Expr{}
	vars := map[string]ast.Expr{}
	types := map[string]*ast.StructType{}
	assigned := map[string]bool{}
	for _, f := range files {
		ast.Inspect(f.ast, func(n ast.Node) bool {
			if a, ok := n.(*ast.AssignStmt); ok && a.Tok != token.DEFINE {
				for _, l := range a.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						assigned[id.Name] = true
					}
				}
			}
			return true
		})
		for _, d := range f.ast.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, s := range g.Specs {
				switch s := s.(type) {
				case *ast.ValueSpec:
					into := globals
					if g.Tok == token.VAR {
						into = vars
					}
					for i, n := range s.Names {
						if i < len(s.Values) {
							into[n.Name] = s.Values[i]
						}
					}
				case *ast.TypeSpec:
					if st, ok := s.Type.(*ast.StructType); ok {
						types[s.Name.Name] = st
					}
				}
			}
		}
	}
	for name, v := range vars {
		if !assigned[name] {
			globals[name] = v
		}
	}
	return globals, types
}

// funcSites returns the SQL sites in one function.
func funcSites(fset *token.FileSet, rel string, fn *ast.FuncDecl, sinks map[string]sink, through string, consts map[string]ast.Expr, types map[string]*ast.StructType) []site {
	r := newResolver(fn, consts, types)
	key := rel + ":" + fn.Name.Name

	// Locals bound to a prepared statement or to a query's rows, and the
	// calls they came from.
	stmtOf := map[string]*ast.CallExpr{}
	rowsOf := map[string]*ast.CallExpr{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		a, ok := n.(*ast.AssignStmt)
		if !ok || len(a.Rhs) != 1 || len(a.Lhs) == 0 {
			return true
		}
		c, ok := a.Rhs[0].(*ast.CallExpr)
		id, isID := a.Lhs[0].(*ast.Ident)
		if !ok || !isID {
			return true
		}
		switch calleeName(c) {
		case "PrepareContext":
			stmtOf[id.Name] = c
		case "QueryContext":
			rowsOf[id.Name] = c
		}
		return true
	})

	byCall := map[*ast.CallExpr]*site{}
	var order []*ast.CallExpr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(c)
		if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok {
				if prep, ok := stmtOf[x.Name]; ok && stmtCalls[name] {
					if s := byCall[prep]; s != nil && !c.Ellipsis.IsValid() {
						s.nargs = append(s.nargs, len(c.Args)-1)
					}
					return true
				}
				if q, ok := rowsOf[x.Name]; ok && name == "Scan" {
					if s := byCall[q]; s != nil && !c.Ellipsis.IsValid() {
						s.scans = append(s.scans, len(c.Args))
					}
					return true
				}
			}
		}
		sk, ok := sinks[name]
		if !ok || len(c.Args) <= sk.sql {
			return true
		}
		if id, ok := c.Args[sk.sql].(*ast.Ident); ok && through != "" && id.Name == through {
			return true // passes its caller's SQL on; the callers are the sites
		}
		s := &site{pos: fmt.Sprintf("%s:%d", rel, fset.Position(c.Pos()).Line), key: key}
		s.sqls, s.why = r.resolve(c.Args[sk.sql])
		if sk.args >= 0 && !c.Ellipsis.IsValid() {
			s.nargs = append(s.nargs, len(c.Args)-sk.args)
		}
		_, base := baseSinks[name]
		s.read = base && readers[name]
		byCall[c] = s
		order = append(order, c)
		return true
	})
	// A chained x.QueryRowContext(…).Scan(…) is the parent of its query in
	// the tree, so it is visited first: match those once every site is known.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || calleeName(c) != "Scan" || c.Ellipsis.IsValid() {
			return true
		}
		if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
			if q, ok := sel.X.(*ast.CallExpr); ok {
				if s := byCall[q]; s != nil {
					s.scans = append(s.scans, len(c.Args))
				}
			}
		}
		return true
	})
	out := make([]site, 0, len(order))
	for _, c := range order {
		out = append(out, *byCall[c])
	}
	return out
}
