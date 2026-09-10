package render_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestResultRendersNativeQuestionPlaceholdersAtTextualPosition(t *testing.T) {
	body, err := query.NativeResultOf("sqlite", sqltext.Text("SELECT ? AS left_value, '?' AS literal"), []any{"value"})
	require.NoError(t, err)
	result, err := query.ResultOf(body,
		query.ResultColumn{Name: "left_value", Type: schema.TextType{}},
		query.ResultColumn{Name: "literal", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	statement, err := render.Result(dialect.SQLite(), result)
	require.NoError(t, err)
	require.Equal(t, "SELECT ? AS left_value, '?' AS literal", statement.SQL())
	require.Equal(t, []any{"value"}, statement.Args())
}

func TestResultRendersPostgresPlaceholdersByReferencedArgument(t *testing.T) {
	body, err := query.NativeResultOf("postgresql", sqltext.Text("SELECT $2 AS second, '$1' AS literal, /* $2 */ $1 AS first"), []any{"one", "two"})
	require.NoError(t, err)
	result, err := query.ResultOf(body,
		query.ResultColumn{Name: "second", Type: schema.TextType{}},
		query.ResultColumn{Name: "literal", Type: schema.TextType{}},
		query.ResultColumn{Name: "first", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	statement, err := render.Result(dialect.PostgreSQL(), result)
	require.NoError(t, err)
	require.Equal(t, "SELECT $1 AS second, '$1' AS literal, /* $2 */ $2 AS first", statement.SQL())
	require.Equal(t, []any{"two", "one"}, statement.Args())
}

func TestResultRejectsDollarZeroAndOverflow(t *testing.T) {
	for _, sql := range []string{"SELECT $0", "SELECT $999999999999999999999999"} {
		body, err := query.NativeResultOf("postgresql", sqltext.Text(sql), []any{int64(1)})
		require.NoError(t, err)
		result, err := query.ResultOf(body, query.ResultColumn{Name: "value", Type: schema.IntegerType{}})
		require.NoError(t, err)
		_, err = render.Result(dialect.PostgreSQL(), result)
		require.Error(t, err, sql)
	}
}

func TestResultRejectsNativeEngineMismatchBeforeRendering(t *testing.T) {
	body, err := query.NativeResultOf("sqlite", sqltext.Text("SELECT ?"), []any{int64(1)})
	require.NoError(t, err)
	result, err := query.ResultOf(body, query.ResultColumn{Name: "value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	_, err = render.Result(dialect.PostgreSQL(), result)
	require.Error(t, err)
	var mismatch *render.NativeEngineMismatchError
	require.ErrorAs(t, err, &mismatch)
	require.True(t, errors.Is(err, render.ErrNativeEngineMismatch))
}

func TestResultRejectsWrongEnginePlaceholderSyntax(t *testing.T) {
	body, err := query.NativeResultOf("sqlite", sqltext.Text("SELECT $1"), []any{int64(1)})
	require.NoError(t, err)
	result, err := query.ResultOf(body, query.ResultColumn{Name: "value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	_, err = render.Result(dialect.SQLite(), result)
	require.Error(t, err)
}
