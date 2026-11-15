package metrics

import (
	"slices"
	"testing"

	"github.com/skudra-go/ant/dialect"
	"github.com/skudra-go/ant/dialect/mysql"
	"github.com/skudra-go/ant/dialect/postgres"
	"github.com/skudra-go/ant/dialect/sqlite"
	"github.com/skudra-go/ant/dialect/sqlserver"
	"github.com/skudra-go/ant/qb"
)

// Operation names a statement after its first word, whatever comes before it,
// and reports query when it is not sure.
func TestOperation(t *testing.T) {
	cases := map[string]string{
		// The first word, after whitespace, comments and parentheses.
		"SELECT 1":                    "select",
		"select\t1":                   "select",
		"SELECT\n* FROM users":        "select",
		"SELECT*FROM t":               "select",
		"SELECT/* x */1":              "select",
		"/* tag */ insert into x":     "insert",
		"/* a */ /* b */\tUPDATE x":   "update",
		"-- note\nSELECT 1":           "select",
		"( SELECT 1)":                 "select",
		"(( SELECT 1))":               "select",
		"/* c */ ( /* d */ select 1)": "select",
		"(SELECT 1) UNION SELECT 2":   "select",
		"delete from users":           "delete",
		"MERGE INTO t USING s ON 1=1": "merge",
		"SAVEPOINT sp1":               "savepoint",
		"ROLLBACK TO SAVEPOINT sp1":   "rollback",
		"RELEASE SAVEPOINT sp1":       "release",
		"SAVE TRANSACTION sp1":        "save",

		// Only the first word counts, never a later one.
		"PRAGMA foreign_keys = ON":                     "query",
		"PRAGMA select_x":                              "query",
		"VACUUM update_log":                            "query",
		"EXPLAIN SELECT 1":                             "query",
		"GRANT SELECT ON t TO r":                       "query",
		"CREATE TABLE t AS SELECT * FROM u":            "query",
		"CREATE TRIGGER g AFTER INSERT ON t EXECUTE f": "query",
		"ALTER TABLE orders ADD CONSTRAINT fk FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE": "query",

		// A word is a whole identifier.
		"SELECT1":     "query",
		"select_x 1":  "query",
		"selectā 1":   "query",
		"select$1":    "query",
		"$1":          "query",
		"":            "query",
		"   ":         "query",
		"(":           "query",
		"/* open":     "query",
		"-- only":     "query",
		"/* a */":     "query",
		"'select' 1":  "query",
		`"select" 1`:  "query",
		"[select] 1":  "query",
		"WITH":        "query",
		"WITH a":      "query",
		"WITH a AS (": "query",

		// A WITH statement is named after the statement its CTEs feed.
		"WITH a AS (SELECT 1) SELECT * FROM a":                        "select",
		"WITH a AS (SELECT 1) MERGE INTO t":                           "merge",
		"WITH a AS (SELECT 1) (SELECT 2)":                             "select",
		"with a as (select 1) delete from t":                          "delete",
		"WITH RECURSIVE t(n) AS (SELECT 1) SELECT n FROM t":           "select",
		"WITH a(x, y) AS (SELECT 1, 2), b AS (SELECT 3) MERGE INTO t": "merge",
		"WITH a AS MATERIALIZED (SELECT 1) SELECT 1":                  "select",
		"WITH a AS NOT MATERIALIZED (SELECT 1) UPDATE t SET x = 1":    "update",
		"WITH /* c */ a /* c */ AS -- c\n(SELECT 1) -- c\nINSERT":     "insert",

		// A CTE's name is never the operation.
		"WITH update_log AS (SELECT 1) SELECT * FROM update_log": "select",
		"WITH select_ids AS (SELECT id FROM t) DELETE FROM t":    "delete",
		"WITH _update AS (SELECT 1) SELECT 1":                    "select",
		`WITH "update" AS (SELECT 1) SELECT 1`:                   "select",
		`WITH "a""b" AS (SELECT 1) DELETE FROM t`:                "delete",
		"WITH `update` AS (SELECT 1) SELECT 1":                   "select",
		"WITH [update] AS (SELECT 1) SELECT 1":                   "select",
		"WITH [a]]b] AS (SELECT 1) DELETE FROM t":                "delete",

		// Parentheses inside quotes and comments do not end a CTE's body.
		"WITH a AS (SELECT ')' FROM t) DELETE FROM t":         "delete",
		"WITH a AS (SELECT 'it''s)' FROM t) DELETE FROM t":    "delete",
		`WITH a AS (SELECT 'C:\' FROM t) DELETE FROM t`:       "delete",
		`WITH a AS (SELECT E'it\'s)' FROM t) DELETE FROM t`:   "delete",
		`WITH a AS (SELECT ")" FROM t) DELETE FROM t`:         "delete",
		"WITH a AS (SELECT [)] FROM t) DELETE FROM t":         "delete",
		"WITH a AS (SELECT $$)$$ FROM t) DELETE FROM t":       "delete",
		"WITH a AS (SELECT $x$)$y$)$x$ FROM t) DELETE FROM t": "delete",
		"WITH a AS (SELECT $1 FROM t) DELETE FROM t":          "delete",
		"WITH a AS (SELECT /* ) */ 1) DELETE FROM t":          "delete",
		"WITH a AS (SELECT 1 -- )\n) DELETE FROM t":           "delete",
		"WITH a AS (SELECT a$b FROM t) DELETE FROM t":         "delete",
		"WITH a AS (SELECT 2-1, 4/2) DELETE FROM t":           "delete",

		// A WITH statement that does not have the expected shape is query.
		"WITH a AS (SELECT 1)":       "query",
		"WITH a AS (SELECT ')') ":    "query",
		"WITH a AS SELECT 1":         "query",
		"WITH a (SELECT 1) SELECT 1": "query",
		"WITH a AS (SELECT 1),":      "query",
		"WITH a AS (SELECT 1) SEARCH DEPTH FIRST BY x SET o SELECT 1": "query",
	}

	for sql, want := range cases {
		if got := Operation(sql); got != want {
			t.Errorf("Operation(%q) = %q, want %q", sql, got, want)
		}
	}
}

// Every statement ant renders is named after its operation, on every dialect.
func TestOperationRendered(t *testing.T) {
	users := &qb.TableRef{Name: "users"}
	age := qb.Column{Name: "age"}
	sub := &qb.Select{
		Columns: []qb.Selected{{Value: age}},
		From:    qb.Source{Table: users},
	}

	cases := []struct {
		name string
		stmt qb.Statement
		want string
	}{
		{
			"select with a comment and a hint",
			&qb.Select{
				Comment: "list users",
				Hint:    "INDEX(users)",
				Columns: []qb.Selected{{Value: age}},
				From:    qb.Source{Table: users},
			},
			"select",
		},
		{
			"recursive CTE whose name is a keyword",
			&qb.Select{
				With: []qb.CTE{
					{
						Name:   "update_log",
						Select: sub,
						Key:    []string{"age"},
					},
					{
						Name:   "delete",
						Select: sub,
					},
				},
				Recursive: true,
				Columns:   []qb.Selected{{Value: qb.Star{}}},
				From:      qb.Source{Name: "update_log"},
			},
			"select",
		},
		{
			"delete",
			&qb.Delete{Table: users},
			"delete",
		},
	}

	dialects := map[string]dialect.Dialect{
		"postgres":  postgres.Dialect,
		"mysql":     mysql.Dialect,
		"mariadb":   mysql.MariaDB,
		"sqlite":    sqlite.Dialect,
		"sqlserver": sqlserver.Dialect,
	}

	names := slices.Sorted(func(yield func(string) bool) {
		for name := range dialects {
			if !yield(name) {
				return
			}
		}
	})

	for _, name := range names {
		for _, c := range cases {
			sql, _, err := dialects[name].Render(c.stmt)
			if err != nil {
				t.Fatalf("%s: %s: %v", name, c.name, err)
			}

			if got := Operation(sql); got != c.want {
				t.Errorf("%s: %s: Operation(%q) = %q, want %q", name, c.name, sql, got, c.want)
			}
		}
	}
}

// Operation never panics, and always returns a known operation or query.
func FuzzOperation(f *testing.F) {
	for _, seed := range []string{
		"SELECT 1",
		"/* c */ (SELECT 1)",
		"WITH a AS (SELECT ')') DELETE FROM t",
		"WITH a AS (SELECT $x$)$x$) SELECT 1",
		`WITH [a]]b] AS (SELECT E'\'') MERGE INTO t`,
		"WITH a AS (",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, sql string) {
		if got := Operation(sql); got != "query" && !known[got] {
			t.Fatalf("Operation(%q) = %q", sql, got)
		}
	})
}
