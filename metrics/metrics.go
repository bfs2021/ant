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
// word of its SQL, after any leading comments, parentheses, and whitespace. It
// names a common table expression after the statement the CTEs feed, and
// returns query when that word is not one ant emits.
func Operation(text string) string {
	rest, word := cutWord(skipLeading(text))

	if word == "with" {
		word = statementWord(rest)
	}

	if !known[word] {
		return "query"
	}

	return word
}

// skipLeading drops the comments, parentheses, and whitespace before the
// statement's first word.
func skipLeading(text string) string {
	for {
		text = strings.TrimLeft(text, " \t\n\r\f(")

		switch {
		case strings.HasPrefix(text, "--"):
			nl := strings.IndexByte(text, '\n')
			if nl < 0 {
				return ""
			}

			text = text[nl+1:]
		case strings.HasPrefix(text, "/*"):
			end := strings.Index(text, "*/")
			if end < 0 {
				return ""
			}

			text = text[end+2:]
		default:
			return text
		}
	}
}

// cutWord returns text after a leading word of letters, and that word in lower
// case. A text that does not start with a letter has no word.
func cutWord(text string) (string, string) {
	i := 0

	for i < len(text) && isLetter(text[i]) {
		i++
	}

	if i == 0 {
		return text, ""
	}

	return text[i:], strings.ToLower(text[:i])
}

// statementWord returns the word of the statement the CTEs feed, skipping the
// words inside parentheses, quotes, and comments, which belong to the CTEs.
func statementWord(text string) string {
	depth := 0

	for i := 0; i < len(text); {
		switch c := text[i]; {
		case c == '(':
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}

			i++
		case c == '\'' || c == '"' || c == '`':
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
// doubled quote stands for itself, and a backslash escapes the next character
// in a quote that is not a backtick.
func skipQuoted(text string, i int) int {
	quote := text[i]
	i++

	for i < len(text) {
		if text[i] == quote {
			if i+1 < len(text) && text[i+1] == quote {
				i += 2
				continue
			}

			return i + 1
		}

		if text[i] == '\\' && quote != '`' {
			i += 2
			continue
		}

		i++
	}

	return i
}

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

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
