package store_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/sample/taskboard/internal/store"
)

func TestCreateSchemaUsesRasqlDDL(t *testing.T) {
	recorder := &ddlRecorder{}
	client, err := rasql.New(recorder, dialect.SQLite())
	if err != nil {
		t.Fatalf("create rasql client: %s", err)
	}

	if err := store.CreateSchema(t.Context(), client); err != nil {
		t.Fatalf("create schema: %s", err)
	}

	expected := []string{
		`CREATE TABLE "members"`,
		`CREATE TABLE "projects"`,
		`CREATE TABLE "tasks"`,
		`CREATE INDEX "tasks_open_by_project"`,
	}
	if len(recorder.statements) != len(expected) {
		t.Fatalf("DDL statements = %d, want %d: %q", len(recorder.statements), len(expected), recorder.statements)
	}
	for index, prefix := range expected {
		if !strings.HasPrefix(recorder.statements[index], prefix) {
			t.Errorf("DDL statement %d = %q, want prefix %q", index, recorder.statements[index], prefix)
		}
	}
}

type ddlRecorder struct {
	statements []string
}

func (*ddlRecorder) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, nil
}

func (recorder *ddlRecorder) ExecContext(_ context.Context, statement string, _ ...any) (sql.Result, error) {
	recorder.statements = append(recorder.statements, statement)
	return ddlResult{}, nil
}

type ddlResult struct{}

func (ddlResult) LastInsertId() (int64, error) {
	return 0, nil
}

func (ddlResult) RowsAffected() (int64, error) {
	return 0, nil
}
