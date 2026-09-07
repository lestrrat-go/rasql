package schemagen

import (
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestValidateRejectsTypedAccessorRefCollision(t *testing.T) {
	_, err := PackageSource("generated", schema.TableDef{Name: "owners", Columns: []schema.ColumnDef{
		{Name: "owner", Type: schema.TextType{}},
		{Name: "owner_ref", Type: schema.TextType{}},
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), `columns "owner" and "owner_ref"`)
	require.Contains(t, err.Error(), `method "OwnerRef"`)
}

func TestRelationshipNameCollidingWithRefAccessorIsRenamed(t *testing.T) {
	users := schema.TableDef{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}
	orders := schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{{Name: "owner", Type: schema.IntegerType{}}}, ForeignKeys: []schema.ForeignKeyDef{{Columns: []string{"owner"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}}, Relationships: []schema.RelationshipDef{{Name: "OwnerRef", Kind: schema.RelationshipBelongsTo, Columns: []string{"owner"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}}}
	source, err := PackageSource("generated", users, orders)
	require.NoError(t, err)
	require.Contains(t, string(source), "func (t OrdersTable) OwnerRef2() OrdersTableOwnerRef2Relation")
}
