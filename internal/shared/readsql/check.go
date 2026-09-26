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
//     (QueryLimit embeds the text inside "SELECT * FROM (…\n) LIMIT n",
//     which only turns a single non-query statement — DDL, PRAGMA —
//     into a syntax error; it does nothing to stop a second, textually
//     separate statement from executing on the same call). Check refuses
//     this directly: a ')' that has no matching '(' earlier in the text
//     would close the wrapper's own paren early and let whatever follows
//     run outside it, so unmatched depth going negative is refused, and
//     so is depth left positive at the end (an unclosed '(' would need
//     the wrap's own closing ')' to balance, changing what it encloses);
//     a ';' may only be followed by more ';' or whitespace — nothing else,
//     not even a comment, since QueryLimit's own trim (a run of ';' and
//     ASCII whitespace only — never Unicode whitespace; see QueryLimit's
//     own comment for why) would otherwise leave a comment, or the ';'
//     itself, inside the wrap;
//   - an unterminated /* comment, which would otherwise swallow the
//     wrap's own trailing "\n) LIMIT n" once concatenated, and everything
//     Check itself would have skipped over unread;
//   - a NUL byte, anywhere: SQLite's tokenizer is C-string based and
//     stops at the first one, so text after it would never reach SQLite
//     as SQL at all, while Check — operating on a Go string, which has no
//     such limit — would have gone on validating it as if it would;
//   - the $NAME(...) and $NAME::NAME2 (and the same for :, @) forms
//     SQLite accepts for compatibility with Tcl variable references: its
//     tokenizer folds the "(...)" or "::NAME2" suffix into the single
//     variable token, consuming any ')' or quote inside it as part of
//     the name rather than as SQL, which Check has no matching rule for
//     and would desync on; refused outright rather than modeled. The #
//     sigil (also a variable form) is refused unconditionally for the
//     same reason, having no legitimate use here;
//   - any byte >= 0x80 outside a string, a quoted identifier or a
//     comment. SQLite's tokenizer treats a leading UTF-8 BOM (EF BB BF)
//     as whitespace before a token, so "select * from \ufeffmeta" reads
//     the plain word "meta" once the BOM is skipped; Check's own
//     identifier scan used to fold any byte >= 0x80 into the identifier
//     it was reading, so it saw one token, "\ufeffmeta", distinct from
//     "meta" and so never refused by checkName below — the same
//     Check/SQLite desync class as the Tcl-variable forms above, closed
//     the same way: refused outright, unconditionally, rather than
//     modeling SQLite's whitespace rules for every possible non-ASCII
//     codepoint. Since the tokenizer never visits the bytes inside a
//     string, quoted identifier or comment (skipQuoted/skipLineComment/
//     skipBlockComment jump straight past them), non-ASCII text stays
//     allowed there — 'Посетители' as a string value, "визиты" as a
//     quoted column name, a comment in any language — only a bare,
//     unquoted appearance is refused;
//   - a single-quoted string whose whole (unescaped) content is a
//     refused name. SQLite's grammar accepts a STRING wherever a NAME
//     is expected (nm ::= STRING), so "select * from 'meta'",
//     "main.'meta'" and "'pragma_table_info'('events')" all read a
//     table Check exists to refuse, string quoting and all, while the
//     tokenizer had filed 'meta' as an ordinary string literal and
//     never asked checkName about it. checkName now runs on a
//     single-quoted string's content exactly as it does on a
//     double-quoted or bracketed one. This over-refuses an exact-match
//     string value unrelated to any table name (comparing a column
//     against the literal 'meta') — accepted, since the text needed to
//     name a table this way is indistinguishable from the text needed
//     to compare against it, and only an exact match is refused:
//     'metadata' and '%meta%' still pass, the same as an unquoted
//     identifier only a prefix of one of these names would.
//
// A tokenizer rather than a substring search, so 'metadata' or '%meta%',
// a comment, or a column called metadata passes. Without an authorizer
// (the driver exposes none) this is sound because identifiers have no
// escapes, a checked statement cannot create a view naming meta or a
// SQLite internal (an existing view is pinned by a test on every view in
// the schema), and — with the rules above — the text Check accepts is
// always exactly the one statement it examined, with nothing in it
// SQLite would tokenize, or resolve as a name, differently than Check
// just did.
//
// It returns the named parameters the text uses, sigil included, in
// order of first use, so a caller can refuse ones it does not bind.
func Check(q string) ([]string, error) {
	// Checked once, up front, over the whole string: a NUL can sit inside
	// a span (a quoted string, a comment) that the loop below jumps over
	// in one step without visiting each byte, but SQLite's C-string-based
	// tokenizer stops at the first one regardless of what token it falls
	// inside, so its notion of "the rest of the text" can differ from
	// Check's no matter where the NUL is.
	if strings.IndexByte(q, 0) >= 0 {
		return nil, fmt.Errorf("%w: sql must not contain a NUL byte", ErrRefused)
	}
	var params []string
	seen := map[string]bool{}
	depth := 0
	stmtEnded := false
	for i := 0; i < len(q); {
		c := q[i]
		if stmtEnded {
			switch c {
			case ' ', '\t', '\n', '\r', '\f':
				i++
			case ';':
				i++
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
			end := skipQuoted(q, i, '\'')
			inner := q[i+1 : max(i+1, end-1)]
			if err := checkName(strings.ReplaceAll(inner, "''", "'")); err != nil {
				return nil, err
			}
			i = end
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
		case c == '#':
			return nil, fmt.Errorf("%w: the # variable sigil is not allowed", ErrRefused)
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
			// SQLite folds a following "(...)" or "::NAME" into the same
			// variable token (a Tcl-variable compatibility form), which
			// would let a ')' or a quote inside it desync this tokenizer
			// from SQLite's own. Refused outright rather than modeled.
			if j < len(q) && q[j] == '(' {
				return nil, fmt.Errorf("%w: a %c(...) variable is not allowed", ErrRefused, c)
			}
			if j+1 < len(q) && q[j] == ':' && q[j+1] == ':' {
				return nil, fmt.Errorf("%w: a %c...::... variable is not allowed", ErrRefused, c)
			}
			if j > i+1 {
				if p := q[i:j]; !seen[p] {
					seen[p] = true
					params = append(params, p)
				}
			}
			i = j
		case c >= '0' && c <= '9':
			i = scanNumber(q, i)
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
		case c >= 0x80:
			// Reached only outside a string, quoted identifier or
			// comment (those spans are jumped over above without
			// visiting their bytes through this switch at all) and
			// outside any identifier this loop was already reading (an
			// identifier's own scan stops at the first non-ASCII byte,
			// landing back here on the very next iteration) — so this is
			// always a bare, unquoted non-ASCII byte, the BOM-splicing
			// class the package doc explains.
			return nil, fmt.Errorf("%w: sql may use non-ASCII characters only inside quotes or comments", ErrRefused)
		default:
			i++
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("%w: sql has an unmatched (", ErrRefused)
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
// of the text: once QueryLimit concatenates its own trailing "\n) LIMIT
// n" after the text Check saw, an unterminated comment here would
// swallow that suffix too, and whatever Check would have read as SQL
// after it.
func skipBlockComment(q string, i int) (int, error) {
	if j := strings.Index(q[i+2:], "*/"); j >= 0 {
		return i + j + 4, nil
	}
	return 0, fmt.Errorf("%w: unterminated /* comment", ErrRefused)
}

// scanNumber returns the index just past a numeric literal starting at
// i (q[i] is a decimal digit), following SQLite's own grammar: a 0x/0X
// hex literal, or decimal digits with at most one '.' and an optional
// e/E exponent (itself an optional sign followed by digits — without a
// digit there, the 'e'/'E' is not part of the number at all). A looser
// rule here — consuming any run of identifier characters and dots, as
// this once did — would let a number swallow a following identifier
// whole ("1.5.meta" as one token), hiding it from checkName below even
// though SQLite's own lexer stops extending the number at the same
// point and reads "meta" as a separate, ordinary identifier.
func scanNumber(q string, i int) int {
	if q[i] == '0' && i+1 < len(q) && (q[i+1] == 'x' || q[i+1] == 'X') {
		j := i + 2
		for j < len(q) && isHexDigit(q[j]) {
			j++
		}
		return j
	}
	j := i
	for j < len(q) && q[j] >= '0' && q[j] <= '9' {
		j++
	}
	if j < len(q) && q[j] == '.' {
		j++
		for j < len(q) && q[j] >= '0' && q[j] <= '9' {
			j++
		}
	}
	if j < len(q) && (q[j] == 'e' || q[j] == 'E') {
		k := j + 1
		if k < len(q) && (q[k] == '+' || q[k] == '-') {
			k++
		}
		if k < len(q) && q[k] >= '0' && q[k] <= '9' {
			for k < len(q) && q[k] >= '0' && q[k] <= '9' {
				k++
			}
			j = k
		}
	}
	return j
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c|0x20 >= 'a' && c|0x20 <= 'f')
}

func checkName(name string) error {
	n := strings.ToLower(name)
	if n == "meta" || n == "dbstat" || strings.HasPrefix(n, "sqlite_") || strings.HasPrefix(n, "pragma_") {
		return fmt.Errorf("%w: sql reads %s, which custom SQL may not read", ErrRefused, name)
	}
	return nil
}

// isIdentStart is pure ASCII: a byte >= 0x80 is never part of an
// unquoted identifier here (see the package doc's non-ASCII bullet), so
// an identifier's scan stops at one instead of folding it in.
func isIdentStart(c byte) bool {
	return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func isIdent(c byte) bool { return isIdentStart(c) || c == '$' || (c >= '0' && c <= '9') }
