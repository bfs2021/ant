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

// Operation reports the operation in lower case for the tracing interceptor.
func TestOperation(t *testing.T) {
	cases := map[string]string{
		"SELECT 1":                          "select",
		"/* tag */ insert into x":           "insert",
		"( SELECT 1)":                       "select",
		"(( SELECT 1))":                     "select",
		"-- note\nSELECT 1":                 "select",
		"/* c */ ( /* d */ select 1)":       "select",
		"WITH a AS (SELECT 1) SELECT 1":     "select",
		"WITH a AS (SELECT 1) MERGE INTO t": "merge",
		"PRAGMA foreign_keys = ON":          "query",
		"SAVEPOINT sp1":                     "savepoint",
		"ROLLBACK TO SAVEPOINT sp1":         "rollback",
		"RELEASE SAVEPOINT sp1":             "release",
		// The name of a CTE must not be mistaken for the operation.
		"WITH update_log AS (SELECT 1) SELECT * FROM update_log":      "select",
		"WITH select_ids AS (SELECT id FROM t) DELETE FROM t":         "delete",
		"WITH delete_rows AS (SELECT 1) UPDATE t SET x = 1":           "update",
		"WITH RECURSIVE t(n) AS (SELECT 1) SELECT n FROM t":           "select",
		"WITH a(x, y) AS (SELECT 1, 2), b AS (SELECT 3) MERGE INTO t": "merge",
		"WITH a AS MATERIALIZED (SELECT 1) SELECT 1":                  "select",
		// Bracketed identifiers are quotes in the SQL Server dialect.
		"WITH a AS (SELECT [update] FROM t) SELECT 1":    "select",
		"WITH a AS (SELECT [b]]c] FROM t) DELETE FROM t": "delete",
		// A backslash is an ordinary character in standard SQL and PostgreSQL.
		`WITH a AS (SELECT 'C:\') SELECT 1`:     "select",
		`WITH a AS (SELECT 'it''s) ok') DELETE`: "delete",
	}

	for sql, want := range cases {
		if got := Operation(sql); got != want {
			t.Errorf("Operation(%q) = %q, want %q", sql, got, want)
		}
	}
}
