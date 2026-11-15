package metrics

import "strings"

// known are the operations ant generates. Every other statement is reported as
// query, which bounds the number of metric series and span names.
var known = map[string]bool{
	"select":    true,
	"insert":    true,
	"update":    true,
	"delete":    true,
	"merge":     true,
	"savepoint": true,
	"rollback":  true,
	"release":   true,
	"save":      true,
}

// Operation returns the lowercase operation a statement performs: select,
// insert, update, delete, merge, savepoint, rollback, release or save. It reads
// the statement's first word, after any whitespace, comments and opening
// parentheses. A WITH statement is named after the statement its common table
// expressions feed.
//
// Any other statement, and any statement Operation cannot read with certainty,
// is reported as query. The result therefore has a small, fixed set of values,
// which suits metric labels and span names.
func Operation(text string) string {
	word, next := cutWord(text, skipLeading(text, 0))
	if word == "with" {
		end, ok := skipCTEs(text, next)
		if !ok {
			return "query"
		}

		word, _ = cutWord(text, skipLeading(text, end))
	}

	if !known[word] {
		return "query"
	}

	return word
}

// skipCTEs returns the index after the common table expressions that follow
// WITH at i, where the statement they feed starts. Each one is a name, an
// optional column list, AS, an optional [NOT] MATERIALIZED, and a body in
// parentheses; they are separated by commas. The second result is false when
// the text does not have that shape.
func skipCTEs(text string, i int) (int, bool) {
	i = skipBlanks(text, i)

	if word, next := cutWord(text, i); word == "recursive" {
		i = next
	}

	for {
		var ok bool

		i, ok = skipName(text, skipBlanks(text, i))
		if !ok {
			return i, false
		}

		i = skipBlanks(text, i)

		if i < len(text) && text[i] == '(' {
			i, ok = skipParens(text, i)
			if !ok {
				return i, false
			}

			i = skipBlanks(text, i)
		}

		word, next := cutWord(text, i)
		if word != "as" {
			return i, false
		}

		i = skipBlanks(text, next)

		if word, next := cutWord(text, i); word == "not" {
			i = skipBlanks(text, next)
		}

		if word, next := cutWord(text, i); word == "materialized" {
			i = skipBlanks(text, next)
		}

		if i >= len(text) || text[i] != '(' {
			return i, false
		}

		i, ok = skipParens(text, i)
		if !ok {
			return i, false
		}

		i = skipBlanks(text, i)

		if i >= len(text) || text[i] != ',' {
			return i, true
		}

		i++
	}
}

// skipLeading returns the index of the first word at or after i, past
// whitespace, comments and opening parentheses.
func skipLeading(text string, i int) int {
	for {
		i = skipBlanks(text, i)

		if i >= len(text) || text[i] != '(' {
			return i
		}

		i++
	}
}

// skipBlanks returns the index of the first character at or after i that is
// neither whitespace nor part of a comment. An unterminated comment runs to the
// end of the text.
func skipBlanks(text string, i int) int {
	for i < len(text) {
		switch {
		case strings.IndexByte(" \t\n\r\v\f", text[i]) >= 0:
			i++
		case strings.HasPrefix(text[i:], "--"):
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				return len(text)
			}

			i += end + 1
		case strings.HasPrefix(text[i:], "/*"):
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				return len(text)
			}

			i += end + 4
		default:
			return i
		}
	}

	return i
}

// skipName returns the index after the identifier or quoted name at i. The
// second result is false when there is no name at i or its quote is not
// closed.
func skipName(text string, i int) (int, bool) {
	if i < len(text) && quoteEnd(text[i]) != 0 {
		return skipQuoted(text, i)
	}

	word, next := cutWord(text, i)

	return next, word != ""
}

// skipParens returns the index after the parenthesis that closes the one at
// text[i]. Parentheses inside quotes and comments do not count. The second
// result is false when the text ends first.
func skipParens(text string, i int) (int, bool) {
	depth := 0

	for i < len(text) {
		c := text[i]

		switch {
		case c == '(':
			depth++
			i++
		case c == ')':
			depth--
			i++

			if depth == 0 {
				return i, true
			}
		case quoteEnd(c) != 0:
			end, ok := skipQuoted(text, i)
			if !ok {
				return end, false
			}

			i = end
		case c == '$':
			i = skipDollarQuoted(text, i)
		case c == '-' || c == '/':
			next := skipBlanks(text, i)
			if next == i {
				next++
			}

			i = next
		case isWordByte(c):
			// Skip a whole word, so that a quote or $ inside an identifier,
			// such as PostgreSQL's a$b, does not start a quote.
			for i < len(text) && isWordByte(text[i]) {
				i++
			}
		default:
			i++
		}
	}

	return i, false
}

// quoteEnd returns the character that closes a quote opened by c, or 0 when c
// does not open one. Brackets quote names in SQL Server.
func quoteEnd(c byte) byte {
	switch c {
	case '\'', '"', '`':
		return c
	case '[':
		return ']'
	}

	return 0
}

// skipQuoted returns the index after the quoted text that starts at text[i]. A
// doubled closing character stands for itself. A backslash escapes the next
// character only in PostgreSQL's E'...' strings: standard SQL treats it as an
// ordinary character. The second result is false when the quote is not closed.
func skipQuoted(text string, i int) (int, bool) {
	end := quoteEnd(text[i])
	escapes := end == '\'' && i > 0 && (text[i-1] == 'e' || text[i-1] == 'E') &&
		(i == 1 || !isWordByte(text[i-2]))

	for i++; i < len(text); i++ {
		switch {
		case escapes && text[i] == '\\':
			i++
		case text[i] != end:
		case i+1 < len(text) && text[i+1] == end:
			i++
		default:
			return i + 1, true
		}
	}

	return len(text), false
}

// skipDollarQuoted returns the index after a PostgreSQL dollar-quoted string,
// $$...$$ or $tag$...$tag$, that starts at text[i]. When text[i] does not
// start one, as in the parameter $1, it returns i+1. An unterminated string
// runs to the end of the text.
func skipDollarQuoted(text string, i int) int {
	j := i + 1
	if j < len(text) && text[j] != '$' && !isLetter(text[j]) && text[j] != '_' {
		return i + 1
	}

	for j < len(text) && text[j] != '$' && isWordByte(text[j]) {
		j++
	}

	if j >= len(text) || text[j] != '$' {
		return i + 1
	}

	tag := text[i : j+1]

	end := strings.Index(text[j+1:], tag)
	if end < 0 {
		return len(text)
	}

	return j + 1 + end + len(tag)
}

// cutWord returns the identifier at i in lower case, and the index after it.
// The word is empty when no identifier starts at i. An identifier starts with
// a letter or underscore and goes on with letters, digits, underscores, $ and
// non-ASCII characters, so update_log is one word, never update.
func cutWord(text string, i int) (string, int) {
	if i >= len(text) || !isLetter(text[i]) && text[i] != '_' && text[i] < 0x80 {
		return "", i
	}

	start := i

	for i < len(text) && isWordByte(text[i]) {
		i++
	}

	return strings.ToLower(text[start:i]), i
}

func isWordByte(c byte) bool {
	return isLetter(c) || '0' <= c && c <= '9' || c == '_' || c == '$' || c >= 0x80
}

func isLetter(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}
