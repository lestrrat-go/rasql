package generate

import (
	"fmt"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/schema"
)

type CompilerInput struct {
	Catalog compilerir.PhysicalCatalog
	Legacy  *LegacyGenerationSidecar
}
type LegacyGenerationSidecar struct{ Objects []LegacyObjectSidecar }
type LegacyObjectSidecar struct {
	ID            compilerir.ObjectID
	Operations    schema.Operation
	Relationships []schema.RelationshipDef
	RowName       string
	Columns       []LegacyColumnSidecar
}
type LegacyColumnSidecar struct {
	Name      string
	GoBinding *schema.GoBinding
}

func CompilerInputFromTableDefs(engine compilerir.EngineIdentity, tables []schema.TableDef) (CompilerInput, []compilerir.Diagnostic) {
	catalog, diagnostics := compilerir.PhysicalFromTableDefs(engine, tables)
	catalog, identityDiagnostics := compilerir.AssignObjectIDs(catalog, compilerir.IdentityInput{SourceIdentity: "legacy"})
	diagnostics = append(diagnostics, identityDiagnostics...)
	sidecar := &LegacyGenerationSidecar{}
	for _, object := range catalog.Objects {
		var source schema.TableDef
		for _, table := range tables {
			if table.Schema == object.Schema && table.Name == object.Name && string(table.EffectiveKind()) == object.Kind {
				source = table
				break
			}
		}
		entry := LegacyObjectSidecar{ID: object.ID, Operations: source.Operations, RowName: source.RowName}
		entry.Relationships = append([]schema.RelationshipDef(nil), source.Relationships...)
		for _, column := range source.Columns {
			entry.Columns = append(entry.Columns, LegacyColumnSidecar{Name: column.Name, GoBinding: column.GoBinding.Clone()})
		}
		sidecar.Objects = append(sidecar.Objects, entry)
	}
	return CompilerInput{Catalog: catalog, Legacy: sidecar}, diagnostics
}

func restoreLegacy(tables []schema.TableDef, input CompilerInput) ([]schema.TableDef, error) {
	if input.Legacy == nil {
		return tables, nil
	}
	objects := map[compilerir.ObjectID]LegacyObjectSidecar{}
	for _, object := range input.Legacy.Objects {
		if _, ok := objects[object.ID]; ok {
			return nil, fmt.Errorf("generate: legacy sidecar duplicate object %q", object.ID)
		}
		objects[object.ID] = object
	}
	if len(objects) != len(tables) {
		return nil, fmt.Errorf("generate: legacy sidecar object coverage mismatch")
	}
	for i := range tables {
		id := compilerir.ObjectID("")
		for _, object := range input.Catalog.Objects {
			if object.Schema == tables[i].Schema && object.Name == tables[i].Name {
				id = object.ID
				break
			}
		}
		object, ok := objects[id]
		if !ok {
			return nil, fmt.Errorf("generate: legacy sidecar missing object %q", id)
		}
		tables[i].Operations = object.Operations
		tables[i].RowName = object.RowName
		tables[i].Relationships = append([]schema.RelationshipDef(nil), object.Relationships...)
		bindings := map[string]*schema.GoBinding{}
		for _, column := range object.Columns {
			if _, ok := bindings[column.Name]; ok {
				return nil, fmt.Errorf("generate: legacy sidecar duplicate column %q", column.Name)
			}
			bindings[column.Name] = column.GoBinding.Clone()
		}
		if len(bindings) != len(tables[i].Columns) {
			return nil, fmt.Errorf("generate: legacy sidecar column coverage mismatch for %q", tables[i].Name)
		}
		for j := range tables[i].Columns {
			binding, ok := bindings[tables[i].Columns[j].Name]
			if !ok {
				return nil, fmt.Errorf("generate: legacy sidecar missing column %q", tables[i].Columns[j].Name)
			}
			tables[i].Columns[j].GoBinding = binding.Clone()
		}
	}
	return tables, nil
}
