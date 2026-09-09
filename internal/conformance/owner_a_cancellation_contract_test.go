package conformance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestOwnerACancellationStages(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, SeedDatabaseForEngine(t.Context(), database, "sqlite"))
	engine, ok := EngineByName("sqlite")
	require.True(t, ok)
	raw, err := rasql.New(database, engine.Dialect)
	require.NoError(t, err)
	invocations, events := &InvocationRecorder{}, &EventRecorder{}
	raw, err = raw.WithInvocationObservers(
		rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}),
		invocations.Observer(),
	)
	require.NoError(t, err)
	profile, err := rasql.DiscoverEngineProfile(t.Context(), raw, engine.ProfileID)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(raw, profile)
	require.NoError(t, err)
	executor, err = rasql.WithEventObservers(
		executor,
		rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}),
		events.Observer(),
	)
	require.NoError(t, err)
	resetInvocationRecorder(invocations)
	resetEventRecorder(events)

	rasqlEvidence, err := rasqlCancellation(t.Context(), executor, raw, "sqlite")
	require.NoError(t, err)
	rasqlEvidence.Invocations = invocations.Snapshot()
	rasqlEvidence.Events = events.Snapshot()
	correlated, err := correlateRASQLWorkload(
		engine.ProfileID,
		"cancellation",
		rasqlEvidence.DecodedRows,
		rasqlEvidence.DecodedStatementRows,
		rasqlEvidence.Invocations,
		rasqlEvidence.Events,
	)
	require.NoError(t, err)
	rasqlEvidence.Observations = correlated.Statements
	setObservationCounters(&rasqlEvidence)

	require.NoError(t, resetConformanceDatabase(t.Context(), database, "sqlite"))
	sqlObserver := newHandwrittenObserver()
	sqlContext := context.WithValue(t.Context(), profileLimitContextKey{}, profile.Limits().MaxBindParameters)
	sqlEvidence, err := sqlCancellation(sqlContext, database, "sqlite", sqlObserver)
	require.NoError(t, err)
	sqlEvidence.Observations, err = sqlObserver.Snapshot()
	require.NoError(t, err)
	sqlEvidence.Observations = normalizeObservationParents(sqlEvidence.Observations)
	sqlEvidence.Observations, err = validateImplementationObservations(
		engine.ProfileID,
		"database/sql",
		"cancellation",
		sqlEvidence.Observations,
	)
	require.NoError(t, err)
	setObservationCounters(&sqlEvidence)
	require.NoError(t, compareParity("cancellation", rasqlEvidence, sqlEvidence))

	require.Equal(t, int64(3500), rasqlEvidence.MeasuredRowsConsumed)
	require.Equal(t, int64(3), rasqlEvidence.VerificationRowsConsumed)
	require.Equal(t, int64(3503), rasqlEvidence.RowsConsumed)
	require.Equal(t, "09c709078558111623ed9b85a94173debb3b4c08a49b7b9d728aec7602a6d095", digestBytes(rasqlEvidence.ResultJSON))
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
