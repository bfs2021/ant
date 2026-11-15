package otel

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/skudra-go/ant"
	"github.com/skudra-go/ant/dialect"
)

func TestSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := New(provider.Tracer("test"))

	statement := &ant.Statement{SQL: "SELECT * FROM users WHERE id = $1", Args: []any{"secret"}}

	ctx, err := tracer.Before(context.Background(), statement)
	if err != nil {
		t.Fatal(err)
	}

	tracer.After(ctx, statement, ant.Outcome{Err: errors.New("boom")})

	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "db.select" {
		t.Fatalf("spans: %+v", spans)
	}

	if spans[0].Status().Code != codes.Error {
		t.Fatalf("status: %+v", spans[0].Status())
	}

	for _, attribute := range spans[0].Attributes() {
		if attribute.Value.AsString() == "secret" {
			t.Fatal("a value reached a span")
		}
	}
}

// fake is a dialect with only a name, which is all WithDialect reads. The names
// are those ant's dialects give: postgres, mysql, mariadb, sqlite, sqlserver.
type fake struct {
	dialect.Dialect
	name string
}

func (f fake) Name() string { return f.name }

func named(name string) dialect.Dialect { return fake{name: name} }

// db.system names the database the dialect talks to, as OpenTelemetry spells it.
func TestSystem(t *testing.T) {
	cases := []struct {
		name    string
		options []Option
		want    string
	}{
		{"default", nil, "postgresql"},
		{"postgres", []Option{WithDialect(named("postgres"))}, "postgresql"},
		{"mysql", []Option{WithDialect(named("mysql"))}, "mysql"},
		{"mariadb", []Option{WithDialect(named("mariadb"))}, "mariadb"},
		{"sqlite", []Option{WithDialect(named("sqlite"))}, "sqlite"},
		{"sqlserver", []Option{WithDialect(named("sqlserver"))}, "mssql"},
		{"other dialect", []Option{WithDialect(named("cockroach"))}, "cockroach"},
		{"nil dialect", []Option{WithDialect(nil)}, "postgresql"},
		{"named", []Option{WithSystem("cockroachdb")}, "cockroachdb"},
		{"last wins", []Option{WithDialect(named("mysql")), WithSystem("tidb")}, "tidb"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			tracer := New(provider.Tracer("test"), c.options...)

			statement := &ant.Statement{SQL: "SELECT 1"}

			ctx, err := tracer.Before(context.Background(), statement)
			if err != nil {
				t.Fatal(err)
			}

			tracer.After(ctx, statement, ant.Outcome{})

			spans := recorder.Ended()
			if len(spans) != 1 {
				t.Fatalf("ended spans: %d", len(spans))
			}

			got := ""

			for _, a := range spans[0].Attributes() {
				if a.Key == "db.system" {
					got = a.Value.AsString()
				}
			}

			if got != c.want {
				t.Fatalf("db.system = %q, want %q", got, c.want)
			}
		})
	}
}

// A span is named after the operation, not the first chunk of SQL.
func TestSpanName(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT * FROM users", "db.select"},
		{"/* users */ SELECT 1", "db.select"},
		{"SELECT\n* FROM users", "db.select"},
		{"select\t1", "db.select"},
		{"(SELECT 1) UNION SELECT 2", "db.select"},
		{"( ( SELECT 1) )", "db.select"},
		{"-- pick one\nSELECT 1", "db.select"},
		{"WITH a AS (SELECT 1) SELECT * FROM a", "db.select"},
		{"MERGE INTO t USING s ON 1 = 1", "db.merge"},
		{"SAVEPOINT ant_sp_1", "db.savepoint"},
		{"PRAGMA foreign_keys = ON", "db.query"},
		{"   ", "db.query"},
	}

	for _, c := range cases {
		recorder := tracetest.NewSpanRecorder()
		provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
		tracer := New(provider.Tracer("test"))

		ctx, err := tracer.Before(context.Background(), &ant.Statement{SQL: c.sql})
		if err != nil {
			t.Fatal(err)
		}

		tracer.After(ctx, &ant.Statement{SQL: c.sql}, ant.Outcome{})

		spans := recorder.Ended()
		if len(spans) != 1 {
			t.Fatalf("ended spans for %q: %d", c.sql, len(spans))
		}

		if got := spans[0].Name(); got != c.want {
			t.Errorf("span name for %q = %q, want %q", c.sql, got, c.want)
		}
	}
}
