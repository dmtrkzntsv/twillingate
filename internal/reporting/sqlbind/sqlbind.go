// Package sqlbind is the text side of the SQL a widget or a tool sends to
// the external SQL engine (SQL_ENGINE_SOCKET), as readsql's is for SQLite:
// it finds the :name parameters the SQL uses and rewrites them into bound
// ones, and words the advice that follows the engine's failures. The
// scanner guards the engine's wraps around the query (rows as text, the
// shape check, a remote table's page), not the files: the engine is what
// refuses anything but one SELECT, and what keeps it on the tables it is
// given.
package sqlbind

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrRefused reports text the scanner refuses, or a page it cannot
// build. The wrapped message names what to change.
var ErrRefused = errors.New("refused")

// Params returns the :name parameters q uses, sigil included, in order of
// first use. It refuses:
//
//   - a NUL byte, which the engine's C-string parser would stop at while
//     the scan here went on;
//   - an unterminated string, quoted identifier, dollar-quoted string or
//     block comment, which would swallow the wrap's own text after q;
//   - a ')' with no '(' before it and a '(' never closed: either would
//     move the wrap's own parens, so the wrap would filter or count
//     something other than q's rows;
//   - ';' followed by anything but ';' and whitespace, which Trim could
//     not strip;
//   - '?', '$n' and '$name' parameters: widget SQL names its values
//     :name, and the $ names belong to Bind and the page.
//
// It never reads a parameter inside '…', E'…', $tag$…$tag$, "…" or a
// comment (block comments nest and a line comment ends at '\n' or '\r',
// as in the engine's dialect), nor in '::' (a cast) or ':=' (a named
// argument). Any other ':' before a letter starts one, so three of the
// dialect's forms read as
// parameters: the slice list[a:b] reads :b, the Python-style lambda
// lambda x:x+1 reads :x, and the struct literal {'k':v} reads :v. A space
// after the colon avoids it: list[a: b], lambda x: x+1, {'k': v}.
func Params(q string) ([]string, error) {
	occ, err := scan(q)
	if err != nil {
		return nil, err
	}
	var params []string
	seen := map[string]bool{}
	for _, o := range occ {
		if !seen[o.name] {
			seen[o.name] = true
			params = append(params, o.name)
		}
	}
	return params, nil
}

// Value is what Bind puts in place of one :name: Text, bound as a string
// argument and cast to Type (BIGINT, DATE, VARCHAR…) in the SQL, since
// the engine's protocol binds strings only.
type Value struct{ Type, Text string }

// typeName is what Bind accepts as a Value's Type: a bare type name, so
// the text spliced around the bound parameter is only ever a cast.
var typeName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// Bind rewrites each :name of q into CAST($tw_name AS Type), with the
// Value vals has for it (keyed by the name, sigil included), and returns
// the engine arguments (sqlengine.Request's Args: tw_name, without the
// dollar sign), one per name q uses. A
// :name vals lacks is refused (the caller has already refused names
// outside its set); a value q does not use gets no argument, since the
// engine refuses an argument the SQL does not use. It replaces at the
// scanner's offsets, never by string search, so a :name inside a string
// or comment stays as written.
func Bind(q string, vals map[string]Value) (string, map[string]string, error) {
	occ, err := scan(q)
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	args := map[string]string{}
	last := 0
	for _, o := range occ {
		v, ok := vals[o.name]
		if !ok {
			return "", nil, fmt.Errorf("%w: parameter %s has no value", ErrRefused, o.name)
		}
		if !typeName.MatchString(v.Type) {
			return "", nil, fmt.Errorf("%w: %q is not a type name for %s", ErrRefused, v.Type, o.name)
		}
		arg := "tw_" + o.name[1:]
		args[arg] = v.Text
		b.WriteString(q[last:o.start])
		// x:to would otherwise read as the identifier xCAST.
		if o.start > 0 && isIdent(q[o.start-1]) {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "CAST($%s AS %s)", arg, v.Type)
		last = o.end
	}
	b.WriteString(q[last:])
	return b.String(), args, nil
}

// occurrence is one parameter in the text: its name, sigil included, and
// the byte span it covers.
type occurrence struct {
	name       string
	start, end int
}

// scan reads q by the engine's lexical rules (PostgreSQL's) and returns
// every parameter occurrence.
func scan(q string) ([]occurrence, error) {
	// Checked up front: the loop jumps over strings and comments whole.
	if strings.IndexByte(q, 0) >= 0 {
		return nil, fmt.Errorf("%w: sql must not contain a NUL byte", ErrRefused)
	}
	var occ []occurrence
	depth := 0
	ended := false
	for i := 0; i < len(q); {
		c := q[i]
		if ended {
			switch c {
			case ';', ' ', '\t', '\n', '\r', '\f':
				i++
				continue
			}
			return nil, fmt.Errorf("%w: sql must be a single statement; remove what follows the ';'", ErrRefused)
		}
		var err error
		switch {
		case c == ';':
			ended = true
			i++
		case c == '(':
			depth++
			i++
		case c == ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("%w: unmatched ) would close outside the statement", ErrRefused)
			}
			i++
		case c == '\'' || c == '"':
			i, err = skipString(q, i, false)
		case c == '-' && strings.HasPrefix(q[i:], "--"):
			// The engine ends one at a carriage return as well as a newline.
			if j := strings.IndexAny(q[i:], "\n\r"); j >= 0 {
				i += j + 1
			} else {
				i = len(q)
			}
		case c == '/' && strings.HasPrefix(q[i:], "/*"):
			i, err = skipBlockComment(q, i)
		case c == '?':
			return nil, fmt.Errorf("%w: ? is not a parameter here; use :name", ErrRefused)
		case c == '$':
			j := i + 1
			for j < len(q) && isIdent(q[j]) && q[j] != '$' {
				j++
			}
			switch {
			case j < len(q) && q[j] == '$' && (j == i+1 || isIdentStart(q[i+1])):
				i, err = skipDollarQuoted(q, i, q[i:j+1])
			case j > i+1:
				return nil, fmt.Errorf("%w: %s is not a parameter here; use :name", ErrRefused, q[i:j])
			default:
				i++
			}
		case c == ':':
			switch {
			case strings.HasPrefix(q[i:], "::"), strings.HasPrefix(q[i:], ":="):
				i += 2
			case i+1 < len(q) && isParamStart(q[i+1]):
				j := i + 2
				for j < len(q) && isParamPart(q[j]) {
					j++
				}
				occ = append(occ, occurrence{q[i:j], i, j})
				i = j
			default:
				i++
			}
		case isIdentStart(c):
			j := i + 1
			for j < len(q) && isIdent(q[j]) {
				j++
			}
			// E'…' is the one string with backslash escapes; typE'…' is
			// an identifier and a plain string, as the engine reads it.
			if j == i+1 && (c == 'E' || c == 'e') && j < len(q) && q[j] == '\'' {
				i, err = skipString(q, j, true)
			} else {
				i = j
			}
		default:
			i++
		}
		if err != nil {
			return nil, err
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("%w: sql has an unmatched (", ErrRefused)
	}
	return occ, nil
}

// skipString returns the index after the quote that closes the string or
// quoted identifier opening at i: a doubled quote is one quote, and in an
// escape string a backslash escapes the next byte.
func skipString(q string, i int, escapes bool) (int, error) {
	quote := q[i]
	for j := i + 1; j < len(q); j++ {
		switch {
		case escapes && q[j] == '\\':
			j++
		case q[j] == quote && j+1 < len(q) && q[j+1] == quote:
			j++
		case q[j] == quote:
			return j + 1, nil
		}
	}
	what := "string"
	if quote == '"' {
		what = "quoted identifier"
	}
	return 0, fmt.Errorf("%w: unterminated %s", ErrRefused, what)
}

// skipDollarQuoted returns the index after the tag that closes the
// dollar-quoted string opening at i with tag ($$ or $name$).
func skipDollarQuoted(q string, i int, tag string) (int, error) {
	if j := strings.Index(q[i+len(tag):], tag); j >= 0 {
		return i + len(tag) + j + len(tag), nil
	}
	return 0, fmt.Errorf("%w: unterminated %s string", ErrRefused, tag)
}

// skipBlockComment returns the index after the */ that closes the block
// comment opening at i. They nest, as in the engine: /* a /* b */ c */ is one.
func skipBlockComment(q string, i int) (int, error) {
	depth := 0
	for j := i; j+1 < len(q); {
		switch q[j : j+2] {
		case "/*":
			depth++
			j += 2
		case "*/":
			depth--
			j += 2
			if depth == 0 {
				return j, nil
			}
		default:
			j++
		}
	}
	return 0, fmt.Errorf("%w: unterminated /* comment", ErrRefused)
}

// isIdentStart and isIdent are the engine's identifier bytes: any byte of a
// multi-byte UTF-8 character counts, and '$' may follow the first.
func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isIdent(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' || c == '$' }

// isParamStart and isParamPart are a :name's bytes, ASCII only.
func isParamStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isParamPart(c byte) bool { return isParamStart(c) || c >= '0' && c <= '9' }

// Trim strips trailing ';' and ASCII whitespace, as readsql does, so the
// statement fits inside a wrap's parens. Only ASCII: Params lets nothing
// else follow a ';'.
func Trim(q string) string { return strings.TrimRight(q, "; \t\r\n\f") }
