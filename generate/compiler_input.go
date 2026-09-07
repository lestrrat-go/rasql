package generate

import (
	"fmt"
	"slices"

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
	Kind          schema.ObjectKind
	Operations    schema.Operation
	Relationships []schema.RelationshipDef
	RowName       string
	Columns       []LegacyColumnSidecar
}
type LegacyColumnSidecar struct {
	Name      string
	GoBinding *schema.GoBinding
}

func (in CompilerInput) Clone() CompilerInput {
	out := CompilerInput{Catalog: in.Catalog.Clone()}
	if in.Legacy != nil {
		legacy := in.Legacy.Clone()
		out.Legacy = &legacy
	}
	return out
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
		entry := LegacyObjectSidecar{ID: object.ID, Kind: source.Kind, Operations: source.Operations, RowName: source.RowName}
		if source.Relationships != nil {
			entry.Relationships = make([]schema.RelationshipDef, len(source.Relationships))
			for i, relation := range source.Relationships {
				entry.Relationships[i] = relation.Clone()
			}
		}
		if source.Columns != nil {
			entry.Columns = make([]LegacyColumnSidecar, len(source.Columns))
			for i, column := range source.Columns {
				entry.Columns[i] = LegacyColumnSidecar{Name: column.Name, GoBinding: column.GoBinding.Clone()}
			}
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
	known := map[compilerir.ObjectID]struct{}{}
	for _, object := range input.Catalog.Objects {
		known[object.ID] = struct{}{}
	}
	for i, object := range input.Legacy.Objects {
		if _, ok := objects[object.ID]; ok {
			return nil, fmt.Errorf("generate: legacy.objects[%d].id duplicate %q", i, object.ID)
		}
		if _, ok := known[object.ID]; !ok {
			return nil, fmt.Errorf("generate: legacy.objects[%d].id unknown %q", i, object.ID)
		}
		objects[object.ID] = object
	}
	for i := range tables {
		id := compilerir.ObjectID("")
		var physical compilerir.PhysicalObject
		for _, object := range input.Catalog.Objects {
			if object.Schema == tables[i].Schema && object.Name == tables[i].Name {
				id = object.ID
				physical = object
				break
			}
		}
		object, ok := objects[id]
		if !ok {
			return nil, fmt.Errorf("generate: legacy.objects[%d].id missing object %q", i, id)
		}
		if string((schema.TableDef{Kind: object.Kind}).EffectiveKind()) != physical.Kind {
			return nil, fmt.Errorf("generate: legacy.objects[%d].kind does not match physical kind %q", i, physical.Kind)
		}
		tables[i].Kind = object.Kind
		tables[i].Operations = object.Operations
		tables[i].RowName = object.RowName
		tables[i].Relationships = nil
		if object.Relationships != nil {
			tables[i].Relationships = make([]schema.RelationshipDef, len(object.Relationships))
			for j, relation := range object.Relationships {
				tables[i].Relationships[j] = relation.Clone()
			}
		}
		knownColumns := make(map[string]struct{}, len(tables[i].Columns))
		for _, column := range tables[i].Columns {
			knownColumns[column.Name] = struct{}{}
		}
		bindings := map[string]*schema.GoBinding{}
		for j, column := range object.Columns {
			if _, ok := bindings[column.Name]; ok {
				return nil, fmt.Errorf("generate: legacy.objects[%d].columns[%d].name duplicate %q", i, j, column.Name)
			}
			if _, ok := knownColumns[column.Name]; !ok {
				return nil, fmt.Errorf("generate: legacy.objects[%d].columns[%d].name unknown %q", i, j, column.Name)
			}
			bindings[column.Name] = column.GoBinding.Clone()
		}
		for j := range tables[i].Columns {
			binding, ok := bindings[tables[i].Columns[j].Name]
			if !ok {
				return nil, fmt.Errorf("generate: legacy.objects[%d].columns[%d].name missing column %q", i, j, tables[i].Columns[j].Name)
			}
			tables[i].Columns[j].GoBinding = binding.Clone()
		}
	}
	return tables, nil
}

func (s LegacyGenerationSidecar) Clone() LegacyGenerationSidecar {
	out := LegacyGenerationSidecar{Objects: slices.Clone(s.Objects)}
	for i := range out.Objects {
		out.Objects[i].Relationships = nil
		if s.Objects[i].Relationships != nil {
			out.Objects[i].Relationships = make([]schema.RelationshipDef, len(s.Objects[i].Relationships))
			for j, relation := range s.Objects[i].Relationships {
				out.Objects[i].Relationships[j] = relation.Clone()
			}
		}
		out.Objects[i].Columns = slices.Clone(s.Objects[i].Columns)
		for j := range out.Objects[i].Columns {
			out.Objects[i].Columns[j].GoBinding = s.Objects[i].Columns[j].GoBinding.Clone()
		}
	}
	return out
}
