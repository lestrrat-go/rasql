package rasql_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type mutationValidationRow struct{}

func mutationValidationTables() (rasql.Table[mutationValidationRow], rasql.Table[mutationValidationRow]) {
	first := rasql.MustTableOf[mutationValidationRow](schema.TableDef{
		Name:       "first",
		PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.TextType{}, Default: "'new'"},
			{Name: "optional", Type: schema.TextType{}, Nullable: true},
		},
	})
	second := rasql.MustTableOf[mutationValidationRow](schema.TableDef{
		Name:       "second",
		PrimaryKey: []string{"id"},
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}}},
	})
	return first, second
}

func TestMutationPlanValidationErrorsAndStickyTerminals(t *testing.T) {
	first, second := mutationValidationTables()
	id := query.TypedColumnOf[mutationValidationRow, int64](first.Column("id"))
	name := query.TypedColumnOf[mutationValidationRow, string](first.Column("name"))
	otherID := query.TypedColumnOf[mutationValidationRow, int64](second.Column("id"))

	var zeroField rasql.MutationField[mutationValidationRow]
	var nilTable rasql.Table[mutationValidationRow]
	validPredicate := query.EqualValue(id, int64(1))
	tests := []struct {
		name string
		make func() error
		want string
	}{
		{"nil table", func() error { _, err := rasql.NewCreatePlan(nilTable, rasql.SetField(name, "x")); return err }, "table must not be nil"},
		{"empty create", func() error { _, err := rasql.NewCreatePlan(first); return err }, "at least one field"},
		{"zero field", func() error { _, err := rasql.NewCreatePlan(first, zeroField); return err }, "zero field"},
		{"duplicate field", func() error {
			_, err := rasql.NewCreatePlan(first, rasql.SetField(name, "a"), rasql.SetField(name, "b"))
			return err
		}, "duplicate column"},
		{"cross table field", func() error { _, err := rasql.NewCreatePlan(first, rasql.SetField(otherID, int64(1))); return err }, "belongs to another table"},
		{"zero predicate", func() error {
			_, err := rasql.NewPatchPlan(first, query.Predicate{}, rasql.SetField(name, "x"))
			return err
		}, "requires a predicate"},
		{"patch default", func() error {
			_, err := rasql.NewPatchPlan(first, validPredicate, rasql.DefaultField(name))
			return err
		}, "DEFAULT field"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.ErrorContains(t, test.make(), test.want)
		})
	}

	plan, err := rasql.NewCreatePlan(first, rasql.SetField(name, "x"), rasql.SetField(name, "y"))
	require.Error(t, err)
	_, terminalErr := rasql.ExecCreate(context.Background(), rasql.DB{}, plan)
	require.EqualError(t, terminalErr, err.Error(), "constructor errors remain sticky through terminals")

	patchPlan, err := rasql.NewPatchPlan(first, validPredicate, rasql.SetField(name, "x"))
	require.NoError(t, err)
	_, terminalErr = rasql.QueryPatchOne(context.Background(), rasql.DB{}, patchPlan)
	require.Error(t, terminalErr)

	createErrors := []struct {
		name string
		plan rasql.CreatePlan[mutationValidationRow]
		err  error
	}{}
	createPlan, createErr := rasql.NewCreatePlan(nilTable, rasql.SetField(name, "x"))
	createErrors = append(createErrors, struct {
		name string
		plan rasql.CreatePlan[mutationValidationRow]
		err  error
	}{"nil table", createPlan, createErr})
	createPlan, createErr = rasql.NewCreatePlan(first)
	createErrors = append(createErrors, struct {
		name string
		plan rasql.CreatePlan[mutationValidationRow]
		err  error
	}{"empty create", createPlan, createErr})
	createPlan, createErr = rasql.NewCreatePlan(first, zeroField)
	createErrors = append(createErrors, struct {
		name string
		plan rasql.CreatePlan[mutationValidationRow]
		err  error
	}{"zero create", createPlan, createErr})
	createPlan, createErr = rasql.NewCreatePlan(first, rasql.SetField(name, "a"), rasql.SetField(name, "b"))
	createErrors = append(createErrors, struct {
		name string
		plan rasql.CreatePlan[mutationValidationRow]
		err  error
	}{"duplicate create", createPlan, createErr})
	createPlan, createErr = rasql.NewCreatePlan(first, rasql.SetField(otherID, int64(1)))
	createErrors = append(createErrors, struct {
		name string
		plan rasql.CreatePlan[mutationValidationRow]
		err  error
	}{"cross table create", createPlan, createErr})
	for _, test := range createErrors {
		t.Run("sticky create "+test.name, func(t *testing.T) {
			require.Error(t, test.err)
			_, got := rasql.ExecCreate(context.Background(), rasql.DB{}, test.plan)
			require.EqualError(t, got, test.err.Error())
		})
	}

	patchErrors := []struct {
		name string
		plan rasql.PatchPlan[mutationValidationRow]
		err  error
	}{}
	patchPlan, patchErr := rasql.NewPatchPlan(first, validPredicate)
	patchErrors = append(patchErrors, struct {
		name string
		plan rasql.PatchPlan[mutationValidationRow]
		err  error
	}{"empty patch", patchPlan, patchErr})
	patchPlan, patchErr = rasql.NewPatchPlan(first, validPredicate, zeroField)
	patchErrors = append(patchErrors, struct {
		name string
		plan rasql.PatchPlan[mutationValidationRow]
		err  error
	}{"zero patch", patchPlan, patchErr})
	patchPlan, patchErr = rasql.NewPatchPlan(first, validPredicate, rasql.SetField(otherID, int64(1)))
	patchErrors = append(patchErrors, struct {
		name string
		plan rasql.PatchPlan[mutationValidationRow]
		err  error
	}{"cross table patch", patchPlan, patchErr})
	patchPlan, patchErr = rasql.NewPatchPlan(first, query.Predicate{}, rasql.SetField(name, "x"))
	patchErrors = append(patchErrors, struct {
		name string
		plan rasql.PatchPlan[mutationValidationRow]
		err  error
	}{"zero predicate patch", patchPlan, patchErr})
	patchPlan, patchErr = rasql.NewPatchPlan(first, validPredicate, rasql.DefaultField(name))
	patchErrors = append(patchErrors, struct {
		name string
		plan rasql.PatchPlan[mutationValidationRow]
		err  error
	}{"default patch", patchPlan, patchErr})
	for _, test := range patchErrors {
		t.Run("sticky patch "+test.name, func(t *testing.T) {
			require.Error(t, test.err)
			_, got := rasql.QueryPatchOne(context.Background(), rasql.DB{}, test.plan)
			require.EqualError(t, got, test.err.Error())
		})
	}

}

type noCallHandle struct{}

func (noCallHandle) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("QueryContext must not be called")
}

func (noCallHandle) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("ExecContext must not be called")
}

func TestQueryMutationPreflightsReturningBeforeDatabaseAccess(t *testing.T) {
	first, _ := mutationValidationTables()
	name := query.TypedColumnOf[mutationValidationRow, string](first.Column("name"))
	id := query.TypedColumnOf[mutationValidationRow, int64](first.Column("id"))
	plan, err := rasql.NewCreatePlan(first, rasql.SetField(name, "x"))
	require.NoError(t, err)
	db, err := rasql.New(noCallHandle{}, dialect.MySQL())
	require.NoError(t, err)
	_, err = rasql.QueryCreate(context.Background(), db, plan)
	require.ErrorContains(t, err, "does not support RETURNING")

	patch, err := rasql.NewPatchPlan(first, query.EqualValue(id, int64(1)), rasql.SetField(name, "x"))
	require.NoError(t, err)
	_, err = rasql.QueryPatchAll(context.Background(), db, patch)
	require.ErrorContains(t, err, "does not support RETURNING")
	require.False(t, errors.Is(err, sql.ErrNoRows))
	_, err = rasql.QueryPatchOne(context.Background(), db, patch)
	require.ErrorContains(t, err, "does not support RETURNING")
	require.False(t, errors.Is(err, sql.ErrNoRows))
}
