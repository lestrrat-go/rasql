package postgresql

import (
	"strings"
	"testing"

	pgquery "github.com/lestrrat-go/rasql-pg/query"
	"github.com/stretchr/testify/require"
)

func TestRestorePostgreSQLFactsConsumesIdentityAndForeignKeys(t *testing.T) {
	identitySQL, err := restorePostgreSQLFacts(
		"CREATE TABLE t (id bigint)",
		pgquery.QualifiedName{{Name: "t"}},
		map[string]identityMode{"2:id": identityAlways},
		nil,
	)
	require.NoError(t, err)
	require.Contains(t, identitySQL, "GENERATED ALWAYS AS IDENTITY")

	inlineKey := foreignKeyKey{table: "1:t", constraint: "1:x", inline: true}
	inlineSQL, err := restorePostgreSQLFacts(
		"CREATE TABLE t (x bigint REFERENCES p (id))",
		pgquery.QualifiedName{{Name: "t"}},
		nil,
		map[foreignKeyKey]foreignKeyActions{inlineKey: {onDelete: referenceCascade, onUpdate: referenceRestrict}},
	)
	require.NoError(t, err)
	require.Contains(t, inlineSQL, "ON DELETE CASCADE ON UPDATE RESTRICT")

	tableKey := foreignKeyKey{table: "1:t", constraint: "8:t_x_fkey"}
	tableSQL, err := restorePostgreSQLFacts(
		"CREATE TABLE t (x bigint, CONSTRAINT t_x_fkey FOREIGN KEY (x) REFERENCES p (id))",
		pgquery.QualifiedName{{Name: "t"}},
		nil,
		map[foreignKeyKey]foreignKeyActions{tableKey: {onDelete: referenceSetNull, onUpdate: referenceNoAction}},
	)
	require.NoError(t, err)
	require.Contains(t, tableSQL, "ON DELETE SET NULL ON UPDATE NO ACTION")
}

func TestRestorePostgreSQLFactsRejectsUnconsumedFacts(t *testing.T) {
	_, err := restorePostgreSQLFacts(
		"CREATE TABLE t (id bigint)",
		pgquery.QualifiedName{{Name: "t"}},
		map[string]identityMode{"7:missing": identityAlways},
		nil,
	)
	require.Error(t, err)
	require.Equal(t, "postgresql schema diff: identity fact 1:t/7:missing was consumed 0 times", err.Error())

	key := foreignKeyKey{table: "1:t", constraint: "7:missing", inline: true}
	_, err = restorePostgreSQLFacts(
		"CREATE TABLE t (id bigint)",
		pgquery.QualifiedName{{Name: "t"}},
		nil,
		map[foreignKeyKey]foreignKeyActions{key: {}},
	)
	require.Error(t, err)
	require.Equal(t, "postgresql schema diff: foreign-key action fact 1:t/7:missing/true was consumed 0 times", err.Error())

	_, err = restorePostgreSQLFacts(
		"CREATE TABLE t (id bigint, id bigint)",
		pgquery.QualifiedName{{Name: "t"}},
		map[string]identityMode{"2:id": identityAlways},
		nil,
	)
	require.Error(t, err)
	require.Equal(t, "postgresql schema diff: identity fact 1:t/2:id was consumed 2 times", err.Error())

	duplicateKey := foreignKeyKey{table: "1:t", constraint: "1:c"}
	_, err = restorePostgreSQLFacts(
		"CREATE TABLE t (x bigint, CONSTRAINT c FOREIGN KEY (x) REFERENCES p (id), CONSTRAINT c FOREIGN KEY (x) REFERENCES p (id))",
		pgquery.QualifiedName{{Name: "t"}},
		nil,
		map[foreignKeyKey]foreignKeyActions{duplicateKey: {}},
	)
	require.Error(t, err)
	require.Equal(t, "postgresql schema diff: foreign-key action fact 1:t/1:c/false was consumed 2 times", err.Error())
}

func TestRenderFragmentsScopeFactsToRequestedObject(t *testing.T) {
	table := tableDefinition{
		statement: &pgquery.CreateTableStatement{
			Name:        pgquery.QualifiedName{{Name: "t"}},
			Persistence: pgquery.PermanentRelation,
			Columns: []pgquery.ColumnDefinition{{Name: pgquery.Identifier{Name: "id"}, Type: pgquery.DataType{Words: []string{"bigint"}}}},
		},
		identities: map[string]identityMode{"1:other": identityAlways},
		foreignKeys: map[foreignKeyKey]foreignKeyActions{{table: "1:t", constraint: "1:other", inline: true}: {}},
	}
	fragment, err := renderColumnDefinition(table, table.statement.Columns[0])
	require.NoError(t, err)
	require.False(t, strings.Contains(fragment, "GENERATED"))
}
