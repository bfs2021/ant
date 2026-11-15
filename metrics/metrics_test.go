package metrics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/skudra-go/ant"
)

func TestCounts(t *testing.T) {
	m := New()
	ctx := context.Background()
	m.After(
		ctx,
		&ant.Statement{SQL: "/* tag */ SELECT 1"},
		ant.Outcome{Duration: 2 * time.Millisecond},
	)

	m.After(ctx, &ant.Statement{SQL: "insert into x"}, ant.Outcome{Err: errors.New("no")})

	var b strings.Builder
	m.WritePrometheus(&b)

	for _, want := range []string{
		`ant_statements_total{kind="SELECT"} 1`,
		`ant_statement_errors_total{kind="INSERT"} 1`,
		`ant_statement_seconds_bucket{kind="SELECT",le="0.005"} 1`,
		`ant_statement_seconds_bucket{kind="SELECT",le="0.001"} 0`,
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %s in\n%s", want, b.String())
		}
	}

	if len(m.Snapshot()) != 2 {
		t.Fatalf("snapshot: %+v", m.Snapshot())
	}
}

// A kind is the statement's operation, whatever precedes the first word.
func TestKind(t *testing.T) {
	cases := map[string]string{
		"SELECT 1":                  "SELECT",
		"/* tag */ insert into x":   "INSERT",
		"(SELECT 1) UNION SELECT 2": "SELECT",
		"delete from users":         "DELETE",
		"PRAGMA foreign_keys = ON":  "QUERY",
		"":                          "QUERY",
	}

	for sql, want := range cases {
		if got := kind(sql); got != want {
			t.Errorf("kind(%q) = %q, want %q", sql, got, want)
		}
	}
}
