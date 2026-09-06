package schemagen_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestPackageSourceEmitsNativeTypeAndOpaqueType(t *testing.T) {
	source, err := schemagen.PackageSource("generated", schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{{Name: "status", Type: schema.OpaqueType{}, NativeType: &schema.NativeTypeDef{
			Dialect: "postgresql", Schema: "public", Name: "status", Kind: schema.NativeEnum,
		}}},
	})
	require.NoError(t, err)
	text := string(source)
	require.True(t, strings.Contains(text, "schema.OpaqueType{}"))
	require.True(t, strings.Contains(text, "NativeType: &schema.NativeTypeDef"))
	require.True(t, strings.Contains(text, `Name: "status"`))
}

func TestNativeConsumerCompilesAndExercisesGeneratedRuntime(t *testing.T) {
	table := schema.TableDef{Name: "events", Columns: []schema.ColumnDef{
		{Name: "mood", Type: schema.OpaqueType{}, NativeType: &schema.NativeTypeDef{Dialect: "postgresql", Schema: "public", Name: "mood", Kind: schema.NativeEnum, Arguments: []string{"sad", "happy"}}},
		{Name: "moods", Type: schema.OpaqueType{}, NativeType: &schema.NativeTypeDef{Dialect: "postgresql", Schema: "public", Name: "mood", Kind: schema.NativeArray, Element: &schema.NativeTypeDef{Dialect: "postgresql", Schema: "public", Name: "mood", Kind: schema.NativeEnum}}},
		{Name: "role", Type: schema.OpaqueType{}, NativeType: &schema.NativeTypeDef{Dialect: "mysql", Name: "set", Kind: schema.NativeSet, Arguments: []string{"admin", "reader"}}},
	}}
	source, err := schemagen.PackageSource("generated", table)
	require.NoError(t, err)
	directory := t.TempDir()
	repository, err := filepath.Abs("../..")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema.go"), source, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => "+filepath.ToSlash(repository)+"\n"), 0o600))
	usage := `package generated_test

import (
 "testing"
 "example.com/generated"
 "github.com/lestrrat-go/rasql/query"
 "github.com/stretchr/testify/require"
)
type source struct{}
func (source) Scan(destinations ...any) error { *destinations[0].(*any) = "happy"; *destinations[1].(*any) = []string{"sad"}; *destinations[2].(*any) = "admin"; return nil }
func TestNativeRuntime(t *testing.T) {
 row := &generated.EventsRow{}
 require.NoError(t, row.ScanRow(source{}))
 require.Equal(t, "happy", row.Mood)
 destinations, err := row.ScanDestinations([]string{"role", "mood"}); require.NoError(t, err)
 *destinations[0].(*any) = "reader"; *destinations[1].(*any) = nil
 require.Nil(t, row.Mood)
 value, ok := row.ColumnValue("role"); require.True(t, ok); require.Equal(t, "reader", value)
 _ = query.Equal(generated.Events().Role(), query.Bind("admin"))
}
`
	require.NoError(t, os.WriteFile(filepath.Join(directory, "native_test.go"), []byte(usage), 0o600))
	command := exec.CommandContext(t.Context(), "go", "mod", "tidy")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "generated native tidy output:\n%s", output)
	command = exec.CommandContext(t.Context(), "go", "test", ".")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.NoErrorf(t, err, "generated native consumer output:\n%s", output)
}

func TestNativeTypeGenerationKeepsOpaqueRuntimeBindingAndNestedValues(t *testing.T) {
	native := &schema.NativeTypeDef{Dialect: "postgresql", Schema: "app", Name: "mood", Kind: schema.NativeEnum, Arguments: []string{}}
	table := schema.TableDef{Name: "events", Columns: []schema.ColumnDef{{Name: "mood", Type: schema.OpaqueType{}, NativeType: native}}}
	require.Equal(t, "any", schemagen.ColumnGoType(table.Columns[0]))
	packageSource, err := schemagen.PackageSource("generated", table)
	require.NoError(t, err)
	tableSource, err := schemagen.TableSource("generated", table)
	require.NoError(t, err)
	descriptorSource, err := schemagen.DescriptorSource("generated", table)
	require.NoError(t, err)
	for _, source := range [][]byte{packageSource, tableSource, descriptorSource} {
		require.Contains(t, string(source), `Arguments: []string{}`)
		require.Contains(t, string(source), `Name: "mood"`)
	}
	require.Contains(t, string(tableSource), "func (r *EventsRow) ScanRow(src rasql.ScanSource) error")
	require.Contains(t, string(tableSource), "func (r *EventsRow) ScanDestinations(columns []string) ([]any, error)")
	require.Contains(t, string(tableSource), "func (r EventsRow) ColumnValue(name string) (any, bool)")
	encoded, err := json.Marshal(native)
	require.NoError(t, err)
	var decoded schema.NativeTypeDef
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.NotNil(t, decoded.Arguments)
	require.Empty(t, decoded.Arguments)
	_, err = schemagen.PackageSource("generated", schema.TableDef{Name: "broken", Columns: []schema.ColumnDef{{Name: "value", Type: schema.OpaqueType{}}}})
	require.Error(t, err)
}
