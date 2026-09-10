package catalogread

import (
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestUnresolvedFactUsesWrappedInspectorSentinelsAndSorts(t *testing.T) {
	missing := fmt.Errorf("wrapped missing: %w", inspect.ErrTableNotFound)
	incomplete := fmt.Errorf("wrapped incomplete: %w", inspect.ErrIncompleteMetadata)
	factMissing, ok := unresolvedFact(inspect.ObjectName{Schema: "z", Name: "missing"}, missing)
	require.True(t, ok)
	require.Equal(t, UnresolvedFact{Object: schema.ObjectName{Schema: "z", Name: "missing"}, Path: "$", Code: "object_missing", Detail: missing.Error()}, factMissing)
	factIncomplete, ok := unresolvedFact(inspect.ObjectName{Schema: "a", Name: "partial"}, incomplete)
	require.True(t, ok)
	require.Equal(t, "columns", factIncomplete.Path)
	require.Equal(t, "columns_incomplete", factIncomplete.Code)
	require.False(t, errors.Is(errors.New("operational"), inspect.ErrTableNotFound))
	facts := []UnresolvedFact{factMissing, factIncomplete}
	sort.Slice(facts, func(i, j int) bool {
		return objectKey(facts[i].Object)+facts[i].Path+facts[i].Code < objectKey(facts[j].Object)+facts[j].Path+facts[j].Code
	})
	require.Equal(t, "a", facts[0].Object.Schema)
}
