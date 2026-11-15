// Package opname names a statement after the operation it performs. The
// metrics and OpenTelemetry interceptors both label statements with it, so a
// statement keeps the same name wherever it is observed.
package opname

import "strings"

// known are the operations ant generates. Everything else is reported as
// query, which bounds the number of metric series and span names.
var known = map[string]bool{
	"select": true,
	"insert": true,
	"update": true,
	"delete": true,
}

// Operation returns the lowercase operation a statement performs: the first
// word of its SQL, after any leading comments, a leading parenthesis, and
// surrounding whitespace. It returns "query" when that word is not one ant
// emits.
func Operation(text string) string {
	text = strings.TrimSpace(text)

	for strings.HasPrefix(text, "/*") {
		end := strings.Index(text, "*/")
		if end < 0 {
			break
		}

		text = strings.TrimSpace(text[end+2:])
	}

	text = strings.TrimPrefix(text, "(")

	if i := strings.IndexAny(text, " \n\t\r"); i >= 0 {
		text = text[:i]
	}

	word := strings.ToLower(strings.TrimSpace(text))
	if !known[word] {
		return "query"
	}

	return word
}
