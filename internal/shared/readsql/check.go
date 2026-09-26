package readsql

import (
	"fmt"
	"strings"
)

// Check tokenizes custom SQL and refuses what may not appear: the ATTACH
// keyword (mode=ro does not stop attaching another file), and names of
// tables custom SQL may not read — meta, which holds the visitor salt,
// and SQLite's internals. A tokenizer rather than a substring search, so
// 'meta' in a string, a comment, or a column called metadata passes.
// Without an authorizer (the driver exposes none) this is sound because
// custom SQL cannot create views, identifiers have no escapes, and the
// only indirect path, an existing view, is pinned by a test on every v_*.
//
// It returns the named parameters the text uses, sigil included, in
// order of first use, so a caller can refuse ones it does not bind.
func Check(q string) ([]string, error) {
	var params []string
	seen := map[string]bool{}
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == '\'':
			i = skipQuoted(q, i, '\'')
		case c == '-' && strings.HasPrefix(q[i:], "--"):
			if j := strings.IndexByte(q[i:], '\n'); j >= 0 {
				i += j + 1
			} else {
				i = len(q)
			}
		case c == '/' && strings.HasPrefix(q[i:], "/*"):
			if j := strings.Index(q[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = len(q)
			}
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
		case c == ':' || c == '@' || c == '$' || c == '?':
			j := i + 1
			for j < len(q) && isIdent(q[j]) {
				j++
			}
			if p := q[i:j]; c == '?' || j > i+1 {
				if !seen[p] {
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
