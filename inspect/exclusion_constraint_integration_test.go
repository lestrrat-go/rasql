//go:build unix

package inspect_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestPostgreSQLInspectorRecordsExclusionConstraintAgainstLiveDatabase pins
// what a real PostgreSQL server reports back for an EXCLUDE constraint,
// because TestPostgreSQLInspectorRecordsExclusionConstraintFacts in
// inspect_test.go only asserts rasql's own mocked catalog echoes what the
// test told it to. Before this feature existed, inspecting this table
// failed outright: the exclusion constraint made the whole table
// unrepresentable, which is the exact failure a sweep over a production
// schema must not hit on its first one.
//
// The constraint is declared USING btree rather than the more common gist,
// and its elements compare an ordinary text column and an ordinary integer
// column with plain "=": PostgreSQL supports EXCLUDE constraints over any
// index access method that satisfies amgettuple, not only GiST, and the
// built-in btree operator classes for text and integer already carry "="
// as a strategy operator, so this needs neither a range type nor the
// btree_gist extension. (The PostgreSQL manual notes there is little
// practical point to a B-tree exclusion constraint over what a UNIQUE
// constraint already does, but it is exactly the formulation rasql's own
// type model and a default install can both represent end to end.)
func TestPostgreSQLInspectorRecordsExclusionConstraintAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE reservations (id integer PRIMARY KEY, room text NOT NULL, party_id integer NOT NULL, active boolean NOT NULL)")
	mustExec(t, ctx, database, "ALTER TABLE reservations ADD CONSTRAINT reservations_no_double_booking EXCLUDE USING btree (room WITH =, party_id WITH =) WHERE (active) DEFERRABLE INITIALLY DEFERRED")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "reservations")
	require.NoError(t, err)
	require.Equal(t, []schema.ExclusionDef{{
		Name: "reservations_no_double_booking",
		Elements: []schema.ExclusionElementDef{
			{Expression: "room", Operator: "="},
			{Expression: "party_id", Operator: "="},
		},
		Predicate:  "active",
		Deferrable: schema.DeferrableInitiallyDeferred,
	}}, table.ExclusionConstraints)
}
