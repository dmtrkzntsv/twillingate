package sqlcheck

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
)

// maxCandidates caps the strings one argument resolves to, so a
// concatenation of switches cannot multiply without bound.
const maxCandidates = 64

// helpers stand in for calls that build a fragment of SQL: the
// placeholder list for an IN (…) prepares the same with one ?.
var helpers = map[string]string{"placeholders": "?"}

// resolver turns the SQL argument of one function's calls into the
// strings it can hold (D3).
type resolver struct {
	consts     map[string]ast.Expr
	types      map[string]*ast.StructType
	params     map[string]bool
	assigns    map[string][]ast.Expr // local name → every value assigned
	unresolved map[string]string     // local name → why its values cannot be known
	ranges     map[string]ast.Expr   // range value name → what is ranged over
	busy       map[string]bool       // names being resolved, to stop a cycle
}

func newResolver(fn *ast.FuncDecl, consts map[string]ast.Expr, types map[string]*ast.StructType) *resolver {
	r := &resolver{consts: consts, types: types, params: map[string]bool{},
		assigns: map[string][]ast.Expr{}, unresolved: map[string]string{},
		ranges: map[string]ast.Expr{}, busy: map[string]bool{}}
	names, _, _ := paramList(fn)
	for _, n := range names {
		r.params[n] = true
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for i, l := range n.Lhs {
				id, ok := l.(*ast.Ident)
				if !ok || id.Name == "_" {
					continue
				}
				switch {
				case n.Tok != token.DEFINE && n.Tok != token.ASSIGN:
					r.unresolved[id.Name] = id.Name + " is changed with " + n.Tok.String()
				case len(n.Lhs) != len(n.Rhs):
					r.unresolved[id.Name] = id.Name + " is one of several results of a call"
				default:
					r.assigns[id.Name] = append(r.assigns[id.Name], n.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			for i, id := range n.Names {
				if i < len(n.Values) {
					r.assigns[id.Name] = append(r.assigns[id.Name], n.Values[i])
				}
			}
		case *ast.RangeStmt:
			if id, ok := n.Value.(*ast.Ident); ok {
				r.ranges[id.Name] = n.X
			}
		}
		return true
	})
	return r
}

// resolve returns the strings e can hold, or why it cannot be known.
func (r *resolver) resolve(e ast.Expr) ([]string, string) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			s, err := strconv.Unquote(e.Value)
			if err != nil {
				return nil, err.Error()
			}
			return []string{s}, ""
		}
	case *ast.ParenExpr:
		return r.resolve(e.X)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			break
		}
		left, why := r.resolve(e.X)
		if why != "" {
			return nil, why
		}
		right, why := r.resolve(e.Y)
		if why != "" {
			return nil, why
		}
		if len(left)*len(right) > maxCandidates {
			return nil, "more than " + strconv.Itoa(maxCandidates) + " candidate strings"
		}
		var out []string
		for _, a := range left {
			for _, b := range right {
				out = append(out, a+b)
			}
		}
		return out, ""
	case *ast.Ident:
		if r.busy[e.Name] {
			return nil, e.Name + " is assigned from itself"
		}
		r.busy[e.Name] = true
		defer delete(r.busy, e.Name)
		if why, ok := r.unresolved[e.Name]; ok {
			return nil, why
		}
		if vals, ok := r.assigns[e.Name]; ok {
			return r.union(vals)
		}
		if over, ok := r.ranges[e.Name]; ok {
			lit, why := r.literal(over)
			if why != "" {
				return nil, why
			}
			return r.union(lit.Elts)
		}
		if r.params[e.Name] {
			return nil, e.Name + " is a parameter"
		}
		if v, ok := r.consts[e.Name]; ok {
			return r.resolve(v)
		}
		return nil, e.Name + " is not a constant, a local or an unchanged package variable"
	case *ast.CallExpr:
		name := calleeName(e)
		if s, ok := helpers[name]; ok {
			if _, isIdent := e.Fun.(*ast.Ident); isIdent {
				return []string{s}, ""
			}
		}
		return nil, "built by a call to " + exprString(e.Fun)
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			if over, ok := r.ranges[x.Name]; ok {
				return r.field(over, e.Sel.Name)
			}
		}
		return nil, exprString(e) + " is a field or another package's name"
	}
	return nil, fmt.Sprintf("%s (%T)", exprString(e), e)
}

// union resolves every expression and joins the results.
func (r *resolver) union(es []ast.Expr) ([]string, string) {
	var out []string
	for _, v := range es {
		got, why := r.resolve(v)
		if why != "" {
			return nil, why
		}
		out = append(out, got...)
	}
	if len(out) > maxCandidates {
		return nil, "more than " + strconv.Itoa(maxCandidates) + " candidate strings"
	}
	return out, ""
}

// field resolves name in every element of the slice literal over, which
// is a range loop's operand, written inline or held by a local.
func (r *resolver) field(over ast.Expr, name string) ([]string, string) {
	lit, why := r.literal(over)
	if why != "" {
		return nil, why
	}
	arr, ok := lit.Type.(*ast.ArrayType)
	if !ok {
		return nil, "ranges over a literal that is not a slice"
	}
	var st *ast.StructType
	switch t := arr.Elt.(type) {
	case *ast.StructType:
		st = t
	case *ast.Ident:
		st = r.types[t.Name]
	}
	if st == nil {
		return nil, "ranges over elements of an unknown struct type"
	}
	index := -1
	i := 0
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name == name {
				index = i
			}
			i++
		}
	}
	if index < 0 {
		return nil, "no field " + name
	}
	var vals []ast.Expr
	for _, el := range lit.Elts {
		cl, ok := el.(*ast.CompositeLit)
		if !ok {
			return nil, "an element is not a struct literal"
		}
		var v ast.Expr
		for j, x := range cl.Elts {
			if kv, ok := x.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == name {
					v = kv.Value
				}
			} else if j == index {
				v = x
			}
		}
		if v == nil {
			return nil, "an element leaves " + name + " unset"
		}
		vals = append(vals, v)
	}
	return r.union(vals)
}

// literal is the slice literal a range loop runs over: written inline,
// held by a local assigned once, or by an unchanged package variable.
func (r *resolver) literal(over ast.Expr) (*ast.CompositeLit, string) {
	if id, ok := over.(*ast.Ident); ok {
		if vals := r.assigns[id.Name]; len(vals) == 1 {
			over = vals[0]
		} else if v, ok := r.consts[id.Name]; ok && vals == nil {
			over = v
		}
	}
	lit, ok := over.(*ast.CompositeLit)
	if !ok {
		return nil, "ranges over " + exprString(over) + ", not a slice literal"
	}
	return lit, ""
}

// exprString renders a short form of e for messages.
func exprString(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	case *ast.CallExpr:
		return exprString(e.Fun) + "(…)"
	case *ast.IndexExpr:
		return exprString(e.X) + "[…]"
	}
	return fmt.Sprintf("%T", e)
}
