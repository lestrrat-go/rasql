package mysqlerr_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql/dberror"
	"github.com/lestrrat-go/rasql/dberror/mysqlerr"
	"github.com/stretchr/testify/require"
)

func TestClassifyMappingsAndWrappers(t *testing.T) {
	tests := []struct {
		number   uint16
		category dberror.Category
	}{
		{1062, dberror.UniqueViolation}, {1451, dberror.ForeignKeyViolation}, {1452, dberror.ForeignKeyViolation},
		{1048, dberror.NotNullViolation}, {3819, dberror.CheckViolation},
		{1205, dberror.TransactionConflict}, {1213, dberror.TransactionConflict},
	}
	for _, testCase := range tests {
		native := &mysql.MySQLError{Number: testCase.number, SQLState: [5]byte{'4', '2', '0', '0', '0'}}
		metadata, ok := dberror.Classify(fmt.Errorf("exec: %w", native), mysqlerr.New())
		require.True(t, ok)
		require.Equal(t, testCase.category, metadata.Category)
		require.Equal(t, fmt.Sprint(testCase.number), metadata.NativeCode)
		require.Equal(t, "42000", metadata.SQLState)
		var recovered *mysql.MySQLError
		require.True(t, errors.As(fmt.Errorf("exec: %w", native), &recovered))
		require.Same(t, native, recovered)
	}
	native := &mysql.MySQLError{Number: 1062}
	joined := errors.Join(context.Canceled, fmt.Errorf("exec: %w", native))
	metadata, ok := dberror.Classify(joined, mysqlerr.New())
	require.True(t, ok)
	require.Equal(t, dberror.UniqueViolation, metadata.Category)
	require.ErrorIs(t, joined, context.Canceled)
	var recovered *mysql.MySQLError
	require.True(t, errors.As(joined, &recovered))
	require.Same(t, native, recovered)
	metadata, ok = dberror.Classify(context.Canceled, mysqlerr.New())
	require.False(t, ok)
	require.Equal(t, dberror.Metadata{}, metadata)
	sentinel := errors.New("sentinel")
	wrapped := fmt.Errorf("exec: %w", sentinel)
	metadata, ok = dberror.Classify(wrapped, mysqlerr.New())
	require.False(t, ok)
	require.ErrorIs(t, wrapped, sentinel)
	metadata, ok = dberror.Classify(&mysql.MySQLError{Number: 9999}, mysqlerr.New())
	require.False(t, ok)
	require.Equal(t, dberror.Metadata{}, metadata)
}
