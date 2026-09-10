package generate

import (
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
)

// descriptorSourceIdentity seeds AssignObjectIDs for DescriptorSource. Object
// IDs never reach rendered source (declarations are named from the table's
// own name, not from its ID), so this needs no meaning beyond being the same
// value on every call: that is what makes two DescriptorSource calls over
// the same tables produce byte-identical output.
const descriptorSourceIdentity = "generate.DescriptorSource"

// DescriptorSource renders packageName's compact descriptors for tables and
// returns the concatenated Go source RenderCompact would write for them: for
// every table, its row type, table descriptor, and accessors, plus the
// package's shared runtime helpers. It drives the same pipeline rasqlgen
// does -- PhysicalFromTableDefs, AssignObjectIDs, BuildSemantic, BuildGo,
// NewEmitterInput, RenderCompact -- entirely in memory and touches no
// filesystem, so a consumer module that cannot import internal/compilerir or
// internal/schemagen directly can still re-render a descriptor and diff it
// against an earlier generation.
//
// This replaces the deleted legacy-emitter DescriptorSource, which returned
// only a package's descriptor-only file. The compact emitter has no such
// file -- a table's row type and its descriptor are declared in the same
// file -- so this returns every file compact would write instead of one.
func DescriptorSource(packageName string, tables []schema.TableDef) ([]byte, error) {
	engine := compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}
	catalog, diagnostics := compilerir.PhysicalFromTableDefs(engine, tables)
	if err := firstDiagnosticError("physical", diagnostics); err != nil {
		return nil, err
	}
	catalog, diagnostics = compilerir.AssignObjectIDs(catalog, compilerir.IdentityInput{SourceIdentity: descriptorSourceIdentity})
	if err := firstDiagnosticError("identity", diagnostics); err != nil {
		return nil, err
	}
	semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	if err := firstDiagnosticError("semantic", diagnostics); err != nil {
		return nil, err
	}
	objects := make([]compilerir.ObjectGoName, len(catalog.Objects))
	var columnBindings []compilerir.ColumnGoBinding
	for i, object := range catalog.Objects {
		objects[i] = compilerir.ObjectGoName{ID: object.ID, File: strings.ToLower(object.Name) + "_gen.go"}
		table, ok := findCompactTable(tables, object)
		if !ok {
			continue
		}
		bound, err := schemagen.TableColumnGoBindings(object.ID, table, schemagen.BindingSetOptions{})
		if err != nil {
			return nil, fmt.Errorf("generate: descriptor source: %w", err)
		}
		columnBindings = append(columnBindings, bound...)
	}
	config := compilerir.GoConfig{Package: packageName, Output: ".", Emitter: "compact", Objects: objects, ColumnBindings: columnBindings}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	if err := firstDiagnosticError("go", diagnostics); err != nil {
		return nil, err
	}
	in, err := NewEmitterInput(catalog, semantic, model, config, compilerir.MappingConfig{})
	if err != nil {
		return nil, err
	}
	store, err := RenderCompact(in)
	if err != nil {
		return nil, err
	}
	var source []byte
	for _, file := range store.compact.files {
		source = append(source, file.source...)
	}
	return source, nil
}

func firstDiagnosticError(stage string, diagnostics []compilerir.Diagnostic) error {
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return fmt.Errorf("generate: descriptor source: %s: %s", stage, diagnostic.Message)
		}
	}
	return nil
}
