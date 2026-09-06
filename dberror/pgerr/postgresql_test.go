package pgerr_test

import (
	"context"
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
		var recovered *pgconn.PgError
		require.True(t, errors.As(fmt.Errorf("exec: %w", native), &recovered))
		require.Same(t, native, recovered)
	}
	native := &pgconn.PgError{Code: "23505"}
	joined := errors.Join(context.Canceled, fmt.Errorf("exec: %w", native))
	metadata, ok := dberror.Classify(joined, pgerr.New())
	require.True(t, ok)
	require.Equal(t, dberror.UniqueViolation, metadata.Category)
	require.ErrorIs(t, joined, context.Canceled)
	var recovered *pgconn.PgError
	require.True(t, errors.As(joined, &recovered))
	require.Same(t, native, recovered)
	metadata, ok = dberror.Classify(context.Canceled, pgerr.New())
	require.False(t, ok)
	require.Equal(t, dberror.Metadata{}, metadata)
	sentinel := errors.New("sentinel")
	wrapped := fmt.Errorf("exec: %w", sentinel)
	metadata, ok = dberror.Classify(wrapped, pgerr.New())
	require.False(t, ok)
	require.ErrorIs(t, wrapped, sentinel)
	metadata, ok = dberror.Classify(&pgconn.PgError{Code: "99999"}, pgerr.New())
	require.False(t, ok)
	require.Equal(t, dberror.Metadata{}, metadata)
}
