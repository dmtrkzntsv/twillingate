package sqlcheck

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// params is what one statement binds, numbered the way SQLite numbers
// them: ?NNN takes N, a bare ? or a new name takes one more than the
// highest number so far, and a name seen before keeps its number.
type params struct {
	max   int            // sqlite3_bind_parameter_count: arguments the statement needs
	names map[int]string // number → name without its prefix, for :x @x $x
}

// lex walks sql, skipping quoted text ('…' "…" `…` […]) and comments
// (-- and /* */), and calls visit for every other byte. visit returns the
// offset to continue from, so a caller can consume a token of several
// bytes. An unterminated quote or comment is an error.
func lex(sql string, visit func(i int) int) error {
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for {
				k := strings.IndexByte(sql[j:], c)
				if k < 0 {
					return fmt.Errorf("unterminated %c at offset %d", c, i)
				}
				j += k + 1
				if j < len(sql) && sql[j] == c { // doubled quote: an escaped one
					j++
					continue
				}
				break
			}
			i = j
		case c == '[':
			k := strings.IndexByte(sql[i:], ']')
			if k < 0 {
				return fmt.Errorf("unterminated [ at offset %d", i)
			}
			i += k + 1
		case strings.HasPrefix(sql[i:], "--"):
			k := strings.IndexByte(sql[i:], '\n')
			if k < 0 {
				return nil
			}
			i += k + 1
		case strings.HasPrefix(sql[i:], "/*"):
			k := strings.Index(sql[i+2:], "*/")
			if k < 0 {
				return fmt.Errorf("unterminated /* at offset %d", i)
			}
			i += k + 4
		default:
			i = visit(i)
		}
	}
	return nil
}

// visible is sql with quoted text and comments blanked out.
func visible(sql string) (string, error) {
	var b strings.Builder
	err := lex(sql, func(i int) int { b.WriteByte(sql[i]); return i + 1 })
	return b.String(), err
}

var createTrigger = regexp.MustCompile(`(?i)\bCREATE\s+(TEMP\s+|TEMPORARY\s+)?TRIGGER\b`)

// statements splits sql on the semicolons outside quotes and comments and
// drops the empty pieces. A CREATE TRIGGER is refused: its body holds
// semicolons of its own, and no Go SQL string needs one.
func statements(sql string) ([]string, error) {
	var out []string
	start := 0
	add := func(s string) error {
		v, err := visible(s)
		if err != nil {
			return err
		}
		if strings.TrimSpace(v) == "" {
			return nil
		}
		if createTrigger.MatchString(v) {
			return fmt.Errorf("CREATE TRIGGER in a Go SQL string: its body holds ';', which this check cannot split")
		}
		out = append(out, strings.TrimSpace(s))
		return nil
	}
	var addErr error
	err := lex(sql, func(i int) int {
		if sql[i] == ';' && addErr == nil {
			addErr = add(sql[start:i])
			start = i + 1
		}
		return i + 1
	})
	if err != nil {
		return nil, err
	}
	if addErr != nil {
		return nil, addErr
	}
	if err := add(sql[start:]); err != nil {
		return nil, err
	}
	return out, nil
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func isIdent(c byte) bool {
	return c == '_' || c == '$' || isDigit(c) || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// bindings reports the parameters one statement binds.
func bindings(stmt string) (params, error) {
	p := params{names: map[int]string{}}
	byName := map[string]int{}
	err := lex(stmt, func(i int) int {
		c := stmt[i]
		if c == '$' && i > 0 && isIdent(stmt[i-1]) { // inside an identifier such as a$b
			return i + 1
		}
		switch c {
		case '?':
			j := i + 1
			for j < len(stmt) && isDigit(stmt[j]) {
				j++
			}
			if j == i+1 {
				p.max++
				return j
			}
			if n, _ := strconv.Atoi(stmt[i+1 : j]); n > p.max {
				p.max = n
			}
			return j
		case ':', '@', '$':
			j := i + 1
			for j < len(stmt) && isIdent(stmt[j]) && stmt[j] != '$' {
				j++
			}
			if j == i+1 {
				return j
			}
			name := stmt[i+1 : j]
			if _, ok := byName[name]; !ok {
				p.max++
				byName[name] = p.max
				p.names[p.max] = name
			}
			return j
		}
		return i + 1
	})
	return p, err
}
