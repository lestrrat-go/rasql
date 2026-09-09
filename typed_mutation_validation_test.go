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
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type mutationValidationRow struct{}

// noCallExecutor stands in for rasql.DB{} and noCallHandle in the old API: a
// terminal that reaches it before a construction error is caught is a bug, so
// every method panics.
type noCallExecutor struct{}

func (noCallExecutor) Dialect() dialect.Dialect { panic("Dialect must not be called") }
func (noCallExecutor) Query(context.Context, stmt.Statement) (rasql.ResultRows, error) {
	panic("Query must not be called")
}
func (noCallExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	panic("Exec must not be called")
}

type mutationValidationDecoder struct{ schema rasql.ResultSchema }

func (d mutationValidationDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (mutationValidationDecoder) Presence() []rasql.Presence         { return nil }
func (d mutationValidationDecoder) DecodeRow(source rasql.ScanSource, _ *mutationValidationRow) error {
	var discard int64
	return source.Scan(&discard)
}

// mutationValidationProjection is a minimal valid Projection[mutationValidationRow],
// needed because rasql.Returning refuses a zero projection before it ever
// looks at the plan; every sticky-error case below needs one just to reach
// the plan's own stored error.
func mutationValidationProjection(t *testing.T, first rasql.Table[mutationValidationRow]) rasql.Projection[mutationValidationRow] {
	t.Helper()

	relation, err := rasql.SourceOf(first, "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[mutationValidationRow, int64](relation, "id", "")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
	}, mutationValidationDecoder{schema: resultSchema})
	require.NoError(t, err)
	return projection
}

func mutationValidationTables() (rasql.Table[mutationValidationRow], rasql.Table[mutationValidationRow]) {
	first := rasql.MustTableOf[mutationValidationRow](schema.TableDef{
		Name:       "first",
		PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.TextType{}, Default: "'new'"},
			{Name: "optional", Type: schema.TextType{}, Nullable: true},
		},
		PrimaryKeyAutoincrement: true,
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.ErrorContains(t, test.make(), test.want)
		})
	}

	projection := mutationValidationProjection(t, first)

	plan, err := rasql.NewCreatePlan(first, rasql.SetField(name, "x"), rasql.SetField(name, "y"))
	require.Error(t, err)
	_, terminalErr := rasql.ExecMutation(context.Background(), noCallExecutor{}, plan)
	require.EqualError(t, terminalErr, err.Error(), "constructor errors remain sticky through terminals")

	patchPlan, err := rasql.NewPatchPlan(first, validPredicate, rasql.SetField(name, "x"))
	require.NoError(t, err)
	patchQuery, terminalErr := rasql.Returning(patchPlan, projection)
	require.NoError(t, terminalErr, "a valid plan builds a valid RETURNING query")
	// The plan is valid; what must still fail gracefully, rather than panic,
	// is handing that valid query an invalid database -- rasql.DB{} in place
	// of the old zero rasql.DB.
	invalidProfile, err := rasql.EngineProfileFromVersion("mysql-8.4", 8, 4, 0)
	require.NoError(t, err)
	_, terminalErr = rasql.AsExecutor(rasql.DB{}, invalidProfile)
	require.Error(t, terminalErr)
	_ = patchQuery

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
			_, got := rasql.ExecMutation(context.Background(), noCallExecutor{}, test.plan)
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
	for _, test := range patchErrors {
		t.Run("sticky patch "+test.name, func(t *testing.T) {
			require.Error(t, test.err)
			// Returning alone reaches the plan's own mutationPlan() and its
			// stored error, with no executor and no database involved at all,
			// which is a strictly stronger version of "the terminal never
			// touches the database" than the old QueryPatchOne(rasql.DB{}, ...) call.
			_, got := rasql.Returning(test.plan, projection)
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

// TestQueryMutationPreflightsReturningBeforeDatabaseAccess proves a RETURNING
// query against a dialect that cannot render RETURNING (MySQL) fails during
// compilation, before the executor's Query or Exec is ever called: noCallHandle's
// methods panic if reached, and none of them do. The canonical compiler
// reports this as "RETURNING is not supported" rather than the old
// "does not support RETURNING" wording, but the same thing is being proved:
// the refusal happens at compile time, never at the database.
func TestQueryMutationPreflightsReturningBeforeDatabaseAccess(t *testing.T) {
	first, _ := mutationValidationTables()
	name := query.TypedColumnOf[mutationValidationRow, string](first.Column("name"))
	id := query.TypedColumnOf[mutationValidationRow, int64](first.Column("id"))
	plan, err := rasql.NewCreatePlan(first, rasql.SetField(name, "x"))
	require.NoError(t, err)
	db, err := rasql.New(noCallHandle{}, dialect.MySQL())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("mysql-8.4", 8, 4, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	projection := mutationValidationProjection(t, first)

	createQuery, err := rasql.Returning(plan, projection)
	require.NoError(t, err)
	_, err = rasql.One(context.Background(), executor, createQuery)
	require.ErrorContains(t, err, "RETURNING is not supported")

	patch, err := rasql.NewPatchPlan(first, query.EqualValue(id, int64(1)), rasql.SetField(name, "x"))
	require.NoError(t, err)
	patchQuery, err := rasql.Returning(patch, projection)
	require.NoError(t, err)
	_, err = rasql.All(context.Background(), executor, patchQuery)
	require.ErrorContains(t, err, "RETURNING is not supported")
	require.False(t, errors.Is(err, sql.ErrNoRows))
	_, err = rasql.One(context.Background(), executor, patchQuery)
	require.ErrorContains(t, err, "RETURNING is not supported")
	require.False(t, errors.Is(err, sql.ErrNoRows))
}
