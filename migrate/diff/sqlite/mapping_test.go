package sqlite

import (
	"testing"

	sqlitequery "github.com/lestrrat-go/rasql-sqlite/query"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/stretchr/testify/require"
)

func mappingColumn(name string) sqlitequery.ColumnDefinition {
	return sqlitequery.ColumnDefinition{Name: sqlitequery.Identifier{Name: name}}
}

func mappingPair(destination, source string, kind mappingSourceKind) columnMapping {
	return columnMapping{destination: sqlitequery.Identifier{Name: destination}, source: sqlitequery.Identifier{Name: source}, sourceKind: kind}
}

func TestValidateColumnMappingsTypedFailures(t *testing.T) {
	baseline := []sqlitequery.ColumnDefinition{mappingColumn("id"), mappingColumn("old"), mappingColumn("extra"), mappingColumn("legacy")}
	target := []sqlitequery.ColumnDefinition{mappingColumn("id"), mappingColumn("new"), mappingColumn("extra"), mappingColumn("legacy")}
	validForward := []columnMapping{mappingPair("id", "id", mappingBaseline), mappingPair("new", "new", mappingStagedBackfill), mappingPair("extra", "extra", mappingBaseline), mappingPair("legacy", "legacy", mappingBaseline)}
	validForward[1].backfillDecision = "backfill_new"
	validInverse := []columnMapping{mappingPair("id", "id", mappingBaseline), mappingPair("old", "new", mappingBaseline), mappingPair("extra", "extra", mappingBaseline), mappingPair("legacy", "legacy", mappingBaseline)}
	tests := []struct {
		name             string
		forward, inverse []columnMapping
		want             string
	}{
		{"missing forward", validForward[:1], validInverse, "missing forward mapping for tasks.new"},
		{"missing inverse", validForward, validInverse[:1], "missing inverse mapping for tasks.old"},
		{"duplicate forward destination", append(validForward, validForward[1]), validInverse, "duplicate forward mapping for tasks.new"},
		{"reused inverse source", validForward, append(validInverse[:3], mappingPair("legacy", "new", mappingBaseline)), "reused inverse source for tasks.new"},
		{"wrong source kind", []columnMapping{mappingPair("id", "missing", mappingBaseline), validForward[1], validForward[2]}, validInverse, "missing forward source for tasks.id"},
		{"empty target default", []columnMapping{validForward[0], {destination: sqlitequery.Identifier{Name: "new"}, sourceKind: mappingTargetDefault}, validForward[2]}, validInverse, "empty default expression for tasks.new"},
		{"staged source missing decision", []columnMapping{validForward[0], mappingPair("new", "new", mappingStagedBackfill)}, validInverse, "staged backfill mapping for tasks.new has no decision"},
		{"staged source mismatch", []columnMapping{validForward[0], {destination: sqlitequery.Identifier{Name: "new"}, source: sqlitequery.Identifier{Name: "id"}, sourceKind: mappingStagedBackfill, backfillDecision: "backfill_new"}}, validInverse, "staged backfill source does not match destination for tasks.new"},
		{"decision on baseline", []columnMapping{{destination: sqlitequery.Identifier{Name: "id"}, source: sqlitequery.Identifier{Name: "id"}, sourceKind: mappingBaseline, backfillDecision: "bad"}, validForward[1], validForward[2]}, validInverse, "backfill decision on non-staged mapping for tasks.id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateColumnMappings(sqlitequery.QualifiedName{{Name: "tasks"}}, baseline, target, test.forward, test.inverse)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestBuildRebuildCarrierRejectsDuplicateRenameDecisions(t *testing.T) {
	baseColumn := mappingColumn("old")
	otherColumn := mappingColumn("other")
	targetColumn := mappingColumn("new")
	base := tableDefinition{statement: &sqlitequery.CreateTableStatement{Name: sqlitequery.QualifiedName{{Name: "tasks"}}, Columns: []sqlitequery.ColumnDefinition{baseColumn, otherColumn}}, normalized: &sqlitequery.CreateTableStatement{Name: sqlitequery.QualifiedName{{Name: "tasks"}}, Columns: []sqlitequery.ColumnDefinition{baseColumn, otherColumn}}}
	target := tableDefinition{statement: &sqlitequery.CreateTableStatement{Name: sqlitequery.QualifiedName{{Name: "tasks"}}, Columns: []sqlitequery.ColumnDefinition{targetColumn}}, normalized: &sqlitequery.CreateTableStatement{Name: sqlitequery.QualifiedName{{Name: "tasks"}}, Columns: []sqlitequery.ColumnDefinition{targetColumn}}}
	decision := diff.RequiredDecision{Kind: diff.DecisionRename, Baseline: "old", Target: "new"}
	_, err := buildRebuildCarrier(base, target, nil, nil, []diff.RequiredDecision{decision, decision}, LiveCatalogFacts{}, true)
	require.ErrorContains(t, err, "duplicate rename baseline tasks.old")
	decision2 := diff.RequiredDecision{Kind: diff.DecisionRename, Baseline: "other", Target: "new"}
	_, err = buildRebuildCarrier(base, target, nil, nil, []diff.RequiredDecision{decision, decision2}, LiveCatalogFacts{}, true)
	require.ErrorContains(t, err, "duplicate rename target tasks.new")
}

func TestBuildRebuildCarrierPropagatesDefaultRenderError(t *testing.T) {
	baseColumn := mappingColumn("id")
	targetColumn := mappingColumn("score")
	targetColumn.Constraints = []sqlitequery.ColumnConstraint{{Kind: sqlitequery.ConstraintDefault}}
	base := tableDefinition{statement: &sqlitequery.CreateTableStatement{Name: sqlitequery.QualifiedName{{Name: "tasks"}}, Columns: []sqlitequery.ColumnDefinition{baseColumn}}, normalized: &sqlitequery.CreateTableStatement{Name: sqlitequery.QualifiedName{{Name: "tasks"}}, Columns: []sqlitequery.ColumnDefinition{baseColumn}}}
	target := tableDefinition{statement: &sqlitequery.CreateTableStatement{Name: sqlitequery.QualifiedName{{Name: "tasks"}}, Columns: []sqlitequery.ColumnDefinition{baseColumn, targetColumn}}, normalized: &sqlitequery.CreateTableStatement{Name: sqlitequery.QualifiedName{{Name: "tasks"}}, Columns: []sqlitequery.ColumnDefinition{baseColumn, targetColumn}}}
	_, err := buildRebuildCarrier(base, target, nil, nil, nil, LiveCatalogFacts{}, true)
	require.ErrorContains(t, err, "render default tasks.score")
}
