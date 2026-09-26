package readsql

import (
	"fmt"
	"strings"
)

// Check tokenizes custom SQL and refuses what may not appear:
//
//   - the ATTACH keyword (mode=ro does not stop attaching another file);
//   - names of tables custom SQL may not read — meta, which holds the
//     visitor salt, and SQLite's internals;
//   - a second statement. The driver runs every statement it is given
//     (Query.wrapped embeds the text inside "SELECT * FROM (…\n) LIMIT
//     n", which only turns a single non-query statement — DDL, PRAGMA —
//     into a syntax error; it does nothing to stop a second, textually
//     separate statement from executing on the same call). Check refuses
//     this directly: a ')' that has no matching '(' earlier in the text
//     would close the wrapper's own paren early and let whatever follows
//     run outside it, so unmatched depth is refused; and a ';' may only
//     be followed by more ';', whitespace or comments — anything else
//     refuses, so "select 1; drop table x" cannot ride the same call as
//     the SELECT it looks like;
//   - an unterminated /* comment, which would otherwise swallow the
//     wrap's own trailing "\n) LIMIT n" once concatenated, and everything
//     Check itself would have skipped over unread.
//
// A tokenizer rather than a substring search, so 'meta' in a string, a
// comment, or a column called metadata passes. Without an authorizer (the
// driver exposes none) this is sound because identifiers have no escapes,
// a checked statement cannot create a view naming meta or a SQLite
// internal (an existing view is pinned by a test on every view in the
// schema), and — with the paren and statement-boundary rules above — the
// text Check accepts is always exactly the one statement it examined.
//
// It returns the named parameters the text uses, sigil included, in
// order of first use, so a caller can refuse ones it does not bind.
func Check(q string) ([]string, error) {
	var params []string
	seen := map[string]bool{}
	depth := 0
	stmtEnded := false
	for i := 0; i < len(q); {
		c := q[i]
		if stmtEnded {
			switch {
			case c == ' ' || c == '\t' || c == '\n' || c == '\r':
				i++
			case c == ';':
				i++
			case c == '-' && strings.HasPrefix(q[i:], "--"):
				i = skipLineComment(q, i)
			case c == '/' && strings.HasPrefix(q[i:], "/*"):
				end, err := skipBlockComment(q, i)
				if err != nil {
					return nil, err
				}
				i = end
			default:
				return nil, fmt.Errorf("%w: sql must be a single SELECT or WITH statement", ErrRefused)
			}
			continue
		}
		switch {
		case c == ';':
			stmtEnded = true
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
		case c == '\'':
			i = skipQuoted(q, i, '\'')
		case c == '-' && strings.HasPrefix(q[i:], "--"):
			i = skipLineComment(q, i)
		case c == '/' && strings.HasPrefix(q[i:], "/*"):
			end, err := skipBlockComment(q, i)
			if err != nil {
				return nil, err
			}
			i = end
		case c == '"' || c == '`':
			end := skipQuoted(q, i, c)
			inner := q[i+1 : max(i+1, end-1)]
			if err := checkName(strings.ReplaceAll(inner, string([]byte{c, c}), string(c))); err != nil {
				return nil, err
			}
			i = end
		case c == '[':
			end := len(q)
			if j := strings.IndexByte(q[i:], ']'); j >= 0 {
				end = i + j + 1
			}
			if err := checkName(q[i+1 : max(i+1, end-1)]); err != nil {
				return nil, err
			}
			i = end
		case c == '?':
			j := i + 1 // SQLite's own grammar: ?NNN is digits only, unlike :name/@name/$name
			for j < len(q) && q[j] >= '0' && q[j] <= '9' {
				j++
			}
			if p := q[i:j]; !seen[p] {
				seen[p] = true
				params = append(params, p)
			}
			i = j
		case c == ':' || c == '@' || c == '$':
			j := i + 1
			for j < len(q) && isIdent(q[j]) {
				j++
			}
			if j > i+1 {
				if p := q[i:j]; !seen[p] {
					seen[p] = true
					params = append(params, p)
				}
			}
			i = j
		case c >= '0' && c <= '9':
			j := i + 1 // numbers like 1e5 or 0x1F are not identifiers
			for j < len(q) && (isIdent(q[j]) || q[j] == '.') {
				j++
			}
			i = j
		case isIdentStart(c):
			j := i + 1
			for j < len(q) && isIdent(q[j]) {
				j++
			}
			word := q[i:j]
			if strings.EqualFold(word, "attach") {
				return nil, fmt.Errorf("%w: ATTACH is not allowed", ErrRefused)
			}
			if err := checkName(word); err != nil {
				return nil, err
			}
			i = j
		default:
			i++
		}
	}
	return params, nil
}

// skipQuoted returns the index after the closing quote, treating a
// doubled quote as an escaped one; an unterminated quote runs to the end.
func skipQuoted(q string, i int, quote byte) int {
	for j := i + 1; j < len(q); j++ {
		if q[j] == quote {
			if j+1 < len(q) && q[j+1] == quote {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(q)
}

// skipLineComment returns the index after the newline that ends a --
// comment, or the end of the text if it never appears.
func skipLineComment(q string, i int) int {
	if j := strings.IndexByte(q[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(q)
}

// skipBlockComment returns the index after a /* */ comment's closing
// */. Unterminated is refused rather than treated as running to the end
// of the text: once Query concatenates its own trailing "\n) LIMIT n"
// after the text Check saw, an unterminated comment here would swallow
// that suffix too, and whatever Check would have read as SQL after it.
func skipBlockComment(q string, i int) (int, error) {
	if j := strings.Index(q[i+2:], "*/"); j >= 0 {
		return i + j + 4, nil
	}
	return 0, fmt.Errorf("%w: unterminated /* comment", ErrRefused)
}

func checkName(name string) error {
	n := strings.ToLower(name)
	if n == "meta" || n == "dbstat" || strings.HasPrefix(n, "sqlite_") || strings.HasPrefix(n, "pragma_") {
		return fmt.Errorf("%w: sql reads %s, which custom SQL may not read", ErrRefused, name)
	}
	return nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 0x80 || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func isIdent(c byte) bool { return isIdentStart(c) || c == '$' || (c >= '0' && c <= '9') }
