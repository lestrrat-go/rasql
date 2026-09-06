package pgerr_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lestrrat-go/rasql/dberror"
	"github.com/lestrrat-go/rasql/dberror/pgerr"
	"github.com/stretchr/testify/require"
)

func TestClassifyMappingsAndWrappers(t *testing.T) {
	tests := []struct {
		code     string
		category dberror.Category
	}{
		{"23505", dberror.UniqueViolation}, {"23503", dberror.ForeignKeyViolation},
		{"23502", dberror.NotNullViolation}, {"23514", dberror.CheckViolation},
		{"40001", dberror.TransactionConflict}, {"40P01", dberror.TransactionConflict},
		{"55P03", dberror.TransactionConflict},
	}
	for _, testCase := range tests {
		native := &pgconn.PgError{Code: testCase.code, ConstraintName: "users_email_key", TableName: "users", ColumnName: "email"}
		metadata, ok := dberror.Classify(fmt.Errorf("exec: %w", native), pgerr.New())
		require.True(t, ok)
		require.Equal(t, testCase.category, metadata.Category)
		require.Equal(t, testCase.code, metadata.SQLState)
		require.Equal(t, testCase.code, metadata.NativeCode)
		require.Equal(t, "users_email_key", metadata.Constraint)
		require.Equal(t, "users", metadata.Table)
		require.Equal(t, "email", metadata.Column)
	}
	metadata, ok := dberror.Classify(&pgconn.PgError{Code: "99999"}, pgerr.New())
	require.False(t, ok)
	require.Equal(t, dberror.Metadata{}, metadata)
	var native *pgconn.PgError
	wrapped := fmt.Errorf("exec: %w", &pgconn.PgError{Code: "23505"})
	require.True(t, errors.As(wrapped, &native))
}
