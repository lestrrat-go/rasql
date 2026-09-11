package dberror_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql/dberror"
	"github.com/stretchr/testify/require"
)

func TestCategoryStrings(t *testing.T) {
	for category, want := range map[dberror.Category]string{
		dberror.Unknown:             "unknown",
		dberror.UniqueViolation:     "unique_violation",
		dberror.ForeignKeyViolation: "foreign_key_violation",
		dberror.NotNullViolation:    "not_null_violation",
		dberror.CheckViolation:      "check_violation",
		dberror.TransactionConflict: "transaction_conflict",
		dberror.Category(255):       "unknown",
	} {
		require.Equal(t, want, category.String())
	}
}

type testClassifier struct {
	metadata dberror.Metadata
	ok       bool
}

func (c testClassifier) Classify(error) (dberror.Metadata, bool) { return c.metadata, c.ok }

func TestClassifyOrderingNilAndUnknowns(t *testing.T) {
	native := errors.New("native")
	wrapped := fmt.Errorf("wrapped: %w", native)
	first := testClassifier{metadata: dberror.Metadata{Category: dberror.UniqueViolation}, ok: true}
	second := testClassifier{metadata: dberror.Metadata{Category: dberror.CheckViolation}, ok: true}
	metadata, ok := dberror.Classify(wrapped, first, second)
	require.True(t, ok)
	require.Equal(t, dberror.UniqueViolation, metadata.Category)

	metadata, ok = dberror.Classify(wrapped, testClassifier{ok: true}, second)
	require.True(t, ok)
	require.Equal(t, dberror.CheckViolation, metadata.Category)
	metadata, ok = dberror.Classify(wrapped, nil, second)
	require.True(t, ok)
	require.Equal(t, dberror.CheckViolation, metadata.Category)

	metadata, ok = dberror.Classify(nil, first)
	require.False(t, ok)
	require.Equal(t, dberror.Metadata{}, metadata)
	metadata, ok = dberror.Classify(wrapped, testClassifier{metadata: dberror.Metadata{}, ok: true})
	require.False(t, ok)
	require.Equal(t, dberror.Metadata{}, metadata)
}
