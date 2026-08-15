package catalog

import (
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestTableChangesFallsBackWhenTheFieldWalkFindsNothing proves the safety
// net §2.1 of the phase 4 design rests the whole verdict/explanation split
// on: a TableDrift is never constructed with an empty change list, even
// when the field-by-field walk has no line for why the verdict called a
// pair different.
//
// tableChanges deliberately does not describe Schema or Name (see its own
// doc): those are the identity Drift's table-matching step already
// guarantees equal for any pair it hands to this function, so a real
// Drift() caller can never produce a pair that differs only there. Calling
// tableChanges directly, bypassing that guarantee, is exactly how to
// reach the one gap the per-field walk has by design -- which is exactly
// what the fallback exists to cover, so the property is pinned by a test
// rather than only by a comment.
func TestTableChangesFallsBackWhenTheFieldWalkFindsNothing(t *testing.T) {
	described := schema.TableDef{Name: "a"}
	live := schema.TableDef{Name: "b"}
	require.False(t, reflect.DeepEqual(described, live), "the pair must genuinely differ, or this test would be vacuous")

	changes := tableChanges(described, live)
	require.NotEmpty(t, changes, "the field walk has no line for Name, but the two descriptors differ, so the result must not be silence")
	require.Len(t, changes, 1)
	require.Len(t, changes[0].Fields, 1)
	require.Equal(t, "descriptor", changes[0].Fields[0].Path)
	require.Equal(t, `{Name:"a"}`, changes[0].Fields[0].Was)
	require.Equal(t, `{Name:"b"}`, changes[0].Fields[0].Now)
}
