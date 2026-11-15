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
		"SELECT 1":                     "SELECT",
		"/* tag */ insert into x":      "INSERT",
		"(SELECT 1) UNION SELECT 2":    "SELECT",
		"delete from users":            "DELETE",
		"PRAGMA foreign_keys = ON":     "QUERY",
		"":                             "QUERY",
		"  ( ( SELECT 1) )":            "SELECT",
		"-- pick one\nSELECT 1":        "SELECT",
		"/* a */ /* b */  ( select 1":  "SELECT",
		"MERGE INTO t USING s ON 1=1":  "MERGE",
		"SAVEPOINT ant_sp_1":           "SAVEPOINT",
		"ROLLBACK TO SAVEPOINT ant_sp": "ROLLBACK",
		"RELEASE SAVEPOINT ant_sp_1":   "RELEASE",
		"SAVE TRANSACTION ant_sp_1":    "SAVE",

		// A CTE is named after the statement it feeds, not its own SELECT.
		"WITH a AS (SELECT 1) SELECT * FROM a":              "SELECT",
		"WITH RECURSIVE a AS (SELECT 1) UPDATE t SET x = 1": "UPDATE",
		`WITH "select" AS (SELECT 1) DELETE FROM t`:         "DELETE",
		"WITH a AS (SELECT 1)":                              "QUERY",
	}

	for sql, want := range cases {
		if got := kind(sql); got != want {
			t.Errorf("kind(%q) = %q, want %q", sql, got, want)
		}
	}
}
