// Package metrics counts statements, with no dependencies, and writes them in
// Prometheus text format, which most monitoring systems read:
//
//	m := metrics.New()
//	db, err := ant.Open[AppDB](ctx, postgres.Dialect, dsn, ant.WithInterceptor(m))
//	http.Handle("/metrics", m)
//
// For OpenTelemetry, read Snapshot from a callback gauge, or see the tracing
// interceptor in the guide.
package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/skudra-go/ant"
)

// known are the operations ant generates. Everything else is reported as
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

// Operation returns the lowercase operation a statement performs: the first
// word of its SQL after any leading comments, parentheses, and whitespace. A
// common table expression is named after the statement the CTEs feed, and a
// word that is not one ant emits is reported as query.
func Operation(text string) string {
	i := skipLeading(text, 0)
	if word, next, ok := cutWord(text, i); ok && word == "with" {
		i = skipCTEs(text, next)
	}

	word := statementWord(text, i)
	if !known[word] {
		return "query"
	}

	return word
}

// skipLeading returns the index of the statement's first word, past the
// whitespace, comments, and opening parentheses before it.
func skipLeading(text string, i int) int {
	for {
		i = skipBlanks(text, i)
		if i < len(text) && text[i] == '(' {
			i++

			continue
		}

		return i
	}
}

// skipBlanks returns the index of the first character at or after i that is
// neither whitespace nor part of a comment.
func skipBlanks(text string, i int) int {
	for i < len(text) {
		switch {
		case strings.ContainsRune(" \t\n\r\f", rune(text[i])):
			i++
		case text[i] == '-' && strings.HasPrefix(text[i:], "--"):
			nl := strings.IndexByte(text[i:], '\n')
			if nl < 0 {
				return len(text)
			}

			i += nl + 1
		case text[i] == '/' && strings.HasPrefix(text[i:], "/*"):
			end := strings.Index(text[i:], "*/")
			if end < 0 {
				return len(text)
			}

			i += end + 2
		default:
			return i
		}
	}

	return i
}

// skipCTEs returns the index just past the common table expressions that start
// at i, that is, after the last body and before the statement they feed. A CTE
// is a name, an optional column list, AS, and a parenthesized body.
func skipCTEs(text string, i int) int {
	for {
		i = skipBlanks(text, i)

		name, next, ok := cutIdent(text, i)
		if !ok {
			return i
		}

		if name == "recursive" {
			i = next

			continue
		}

		i = skipBlanks(text, next)
		if i < len(text) && text[i] == '(' {
			end := skipParens(text, i)
			if end < 0 {
				return i
			}

			i = skipBlanks(text, end)
		}

		if word, after, ok := cutWord(text, i); ok && word == "as" {
			i = skipBlanks(text, after)
		}

		for {
			word, after, ok := cutWord(text, i)
			if !ok || word != "not" && word != "materialized" {
				break
			}

			i = skipBlanks(text, after)
		}

		if i >= len(text) || text[i] != '(' {
			return i
		}

		end := skipParens(text, i)
		if end < 0 {
			return i
		}

		i = skipBlanks(text, end)
		if i >= len(text) || text[i] != ',' {
			return i
		}

		i++
	}
}

// skipParens returns the index after the parenthesis that closes the one at
// text[i], or -1 when the text ends first.
func skipParens(text string, i int) int {
	depth := 0

	for i < len(text) {
		switch c := text[i]; {
		case c == '(':
			depth++
			i++
		case c == ')':
			depth--
			i++
			if depth == 0 {
				return i
			}
		case isQuote(c):
			i = skipQuoted(text, i)
		case c == '-' && strings.HasPrefix(text[i:], "--"):
			nl := strings.IndexByte(text[i:], '\n')
			if nl < 0 {
				return -1
			}

			i += nl + 1
		case c == '/' && strings.HasPrefix(text[i:], "/*"):
			end := strings.Index(text[i:], "*/")
			if end < 0 {
				return -1
			}

			i += end + 2
		default:
			i++
		}
	}

	return -1
}

// statementWord returns the first word of a statement at or after i, skipping
// the words inside parentheses, quotes, and comments.
func statementWord(text string, i int) string {
	depth := 0

	for i < len(text) {
		switch c := text[i]; {
		case c == '(':
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}

			i++
		case isQuote(c):
			i = skipQuoted(text, i)
		case c == '-' && strings.HasPrefix(text[i:], "--"):
			nl := strings.IndexByte(text[i:], '\n')
			if nl < 0 {
				return ""
			}

			i += nl + 1
		case c == '/' && strings.HasPrefix(text[i:], "/*"):
			end := strings.Index(text[i:], "*/")
			if end < 0 {
				return ""
			}

			i += end + 2
		case isLetter(c):
			start := i

			for i < len(text) && isLetter(text[i]) {
				i++
			}

			if word := strings.ToLower(text[start:i]); depth == 0 && known[word] {
				return word
			}
		default:
			i++
		}
	}

	return ""
}

// skipQuoted returns the index after the quoted text that starts at text[i]. A
// doubled closing quote stands for itself. Standard SQL and PostgreSQL treat a
// backslash as an ordinary character, so it does not escape anything here.
func skipQuoted(text string, i int) int {
	quote := text[i]
	closer := quote
	if quote == '[' {
		closer = ']'
	}

	i++

	for i < len(text) {
		if text[i] == closer {
			if i+1 < len(text) && text[i+1] == closer {
				i += 2

				continue
			}

			return i + 1
		}

		i++
	}

	return i
}

// cutWord returns a leading word of letters in lower case and the index after
// it. The second result is false when no letter starts at i.
func cutWord(text string, i int) (string, int, bool) {
	start := i

	for i < len(text) && isLetter(text[i]) {
		i++
	}

	if i == start {
		return "", start, false
	}

	return strings.ToLower(text[start:i]), i, true
}

// cutIdent is cutWord for an identifier, which may also contain digits and
// underscores.
func cutIdent(text string, i int) (string, int, bool) {
	start := i

	for i < len(text) && (isLetter(text[i]) || '0' <= text[i] && text[i] <= '9' || text[i] == '_') {
		i++
	}

	if i == start || !isLetter(text[start]) {
		return "", start, false
	}

	return strings.ToLower(text[start:i]), i, true
}

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func isQuote(c byte) bool { return c == '\'' || c == '"' || c == '`' || c == '[' }

// buckets are the histogram's upper bounds, in seconds.
var buckets = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5}

// Metrics is an ant.Interceptor that counts statements by kind.
type Metrics struct {
	mu     sync.Mutex
	counts map[string]*series
}

type series struct {
	total   uint64
	errors  uint64
	seconds float64
	bucket  []uint64
}

// New returns empty metrics.
func New() *Metrics { return &Metrics{counts: map[string]*series{}} }

// Before implements ant.Interceptor.
func (m *Metrics) Before(ctx context.Context, _ *ant.Statement) (context.Context, error) {
	return ctx, nil
}

// kind returns the statement's operation in upper case, which keeps the
// number of series small.
func kind(text string) string { return strings.ToUpper(Operation(text)) }

// After implements ant.Interceptor.
func (m *Metrics) After(_ context.Context, s *ant.Statement, o ant.Outcome) {
	name := kind(s.SQL)

	m.mu.Lock()
	defer m.mu.Unlock()

	c := m.counts[name]
	if c == nil {
		c = &series{bucket: make([]uint64, len(buckets))}
		m.counts[name] = c
	}

	c.total++

	if o.Err != nil {
		c.errors++
	}

	seconds := o.Duration.Seconds()
	c.seconds += seconds

	for i, bound := range buckets {
		if seconds <= bound {
			c.bucket[i]++
		}
	}
}

// Sample is one kind of statement's totals.
type Sample struct {
	Kind     string
	Count    uint64
	Errors   uint64
	Duration time.Duration
}

// Snapshot returns the current totals.
func (m *Metrics) Snapshot() []Sample {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Sample, 0, len(m.counts))

	for name, c := range m.counts {
		out = append(
			out,
			Sample{name, c.total, c.errors, time.Duration(c.seconds * float64(time.Second))},
		)
	}

	return out
}

// WritePrometheus writes the metrics in Prometheus text format.
func (m *Metrics) WritePrometheus(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Fprintln(w, "# TYPE ant_statements_total counter")

	for name, c := range m.counts {
		fmt.Fprintf(w, "ant_statements_total{kind=%q} %d\n", name, c.total)
	}

	fmt.Fprintln(w, "# TYPE ant_statement_errors_total counter")

	for name, c := range m.counts {
		fmt.Fprintf(w, "ant_statement_errors_total{kind=%q} %d\n", name, c.errors)
	}

	fmt.Fprintln(w, "# TYPE ant_statement_seconds histogram")

	for name, c := range m.counts {
		for i, bound := range buckets {
			fmt.Fprintf(
				w,
				"ant_statement_seconds_bucket{kind=%q,le=\"%g\"} %d\n",
				name,
				bound,
				c.bucket[i],
			)
		}

		fmt.Fprintf(w, "ant_statement_seconds_bucket{kind=%q,le=\"+Inf\"} %d\n", name, c.total)
		fmt.Fprintf(w, "ant_statement_seconds_sum{kind=%q} %g\n", name, c.seconds)
		fmt.Fprintf(w, "ant_statement_seconds_count{kind=%q} %d\n", name, c.total)
	}
}

// ServeHTTP makes Metrics an http.Handler for /metrics.
func (m *Metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	m.WritePrometheus(w)
}
