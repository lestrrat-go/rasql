package changeplan

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/schema"
)

type Catalog struct {
	physical       compilerir.PhysicalCatalog
	sourceIdentity string
}

type CatalogObject struct {
	id         ObjectID
	definition schema.TableDef
}

func NewCatalogObject(id ObjectID, definition schema.TableDef) (CatalogObject, error) {
	if id == "" {
		return CatalogObject{}, fmt.Errorf("%w: catalog object ID is required", ErrInvalidIdentity)
	}
	definition = definition.Clone()
	if err := definition.Validate(); err != nil {
		return CatalogObject{}, fmt.Errorf("%w: catalog object: %v", ErrInvalidIdentity, err)
	}
	return CatalogObject{id: id, definition: definition}, nil
}

func (o CatalogObject) ID() ObjectID { return o.id }

func (o CatalogObject) Definition() schema.TableDef { return o.definition.Clone() }

// NewCatalogFromPhysical builds a Catalog from a compilerir.PhysicalCatalog
// whose objects already carry their assigned IDs, such as a live database
// read through compilerir.AssignObjectIDs. The caller is expected to have
// run compilerir.AssignObjectIDs (or otherwise minted the IDs on
// catalog.Objects) before calling this; NewCatalogFromPhysical carries those
// IDs forward rather than assigning its own.
func NewCatalogFromPhysical(catalog compilerir.PhysicalCatalog, sourceIdentity string) (Catalog, error) {
	definitions, diagnostics := compilerir.TableDefsFromPhysical(catalog)
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return Catalog{}, fmt.Errorf("%w: catalog conversion: %s", ErrInvalidIdentity, diagnostic.Message)
		}
	}
	if len(definitions) != len(catalog.Objects) {
		return Catalog{}, fmt.Errorf("%w: catalog conversion dropped an object", ErrInvalidIdentity)
	}
	objects := make([]CatalogObject, len(definitions))
	for i, definition := range definitions {
		object, err := NewCatalogObject(ObjectID(catalog.Objects[i].ID), definition)
		if err != nil {
			return Catalog{}, err
		}
		objects[i] = object
	}
	return newCatalogPhysical(catalog.Engine, sourceIdentity, objects)
}

// NewCatalog builds a Catalog from objects under sourceIdentity, using the
// engine identity of the Profile built from source.
//
// `source` must not be nil.
func NewCatalog(source ProfileSource, sourceIdentity string, objects []CatalogObject) (Catalog, error) {
	profile, err := NewProfile(source)
	if err != nil {
		return Catalog{}, err
	}
	return newCatalog(profile, sourceIdentity, objects)
}

func NewCatalogLike(basis Catalog, objects []CatalogObject) (Catalog, error) {
	if err := basis.validate(); err != nil {
		return Catalog{}, err
	}
	return newCatalogPhysical(basis.physical.Engine, basis.sourceIdentity, objects)
}

func newCatalog(profile Profile, sourceIdentity string, objects []CatalogObject) (Catalog, error) {
	return newCatalogPhysical(engineIdentity(profile), sourceIdentity, objects)
}

func newCatalogPhysical(engine compilerir.EngineIdentity, sourceIdentity string, objects []CatalogObject) (Catalog, error) {
	if strings.TrimSpace(sourceIdentity) == "" {
		return Catalog{}, fmt.Errorf("%w: catalog source identity is required", ErrInvalidIdentity)
	}
	if objects == nil {
		return Catalog{}, fmt.Errorf("%w: catalog objects must be nonnil", ErrInvalidIdentity)
	}
	definitions := make([]schema.TableDef, len(objects))
	ids := make(map[ObjectID]struct{}, len(objects))
	keys := make(map[string]struct{}, len(objects))
	for i, object := range objects {
		if object.id == "" {
			return Catalog{}, fmt.Errorf("%w: catalog object %d has no ID", ErrInvalidIdentity, i)
		}
		if _, ok := ids[object.id]; ok {
			return Catalog{}, fmt.Errorf("%w: duplicate catalog object ID %q", ErrInvalidIdentity, object.id)
		}
		definition := object.definition.Clone()
		if err := definition.Validate(); err != nil {
			return Catalog{}, fmt.Errorf("%w: catalog object %q: %v", ErrInvalidIdentity, object.id, err)
		}
		key := catalogObjectKey(definition.Kind, definition.Schema, definition.Name)
		if _, ok := keys[key]; ok {
			return Catalog{}, fmt.Errorf("%w: duplicate catalog object name %q", ErrInvalidIdentity, definition.Name)
		}
		ids[object.id] = struct{}{}
		keys[key] = struct{}{}
		definitions[i] = definition
	}
	physical, diagnostics := compilerir.PhysicalFromTableDefs(engine, definitions)
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return Catalog{}, fmt.Errorf("%w: catalog conversion: %s", ErrInvalidIdentity, diagnostic.Message)
		}
	}
	if len(physical.Objects) != len(objects) {
		return Catalog{}, fmt.Errorf("%w: catalog conversion dropped an object", ErrInvalidIdentity)
	}
	byKey := make(map[string]compilerir.ObjectID, len(objects))
	for i, object := range objects {
		definition := definitions[i]
		byKey[catalogObjectKey(definition.EffectiveKind(), definition.Schema, definition.Name)] = compilerir.ObjectID(object.id)
	}
	for i := range physical.Objects {
		object := &physical.Objects[i]
		key := catalogObjectKey(schema.ObjectKind(object.Kind), object.Schema, object.Name)
		id, ok := byKey[key]
		if !ok {
			return Catalog{}, fmt.Errorf("%w: catalog conversion produced an unknown object", ErrInvalidIdentity)
		}
		object.ID = id
	}
	if err := physical.Validate(); err != nil {
		return Catalog{}, fmt.Errorf("%w: catalog: %v", ErrInvalidIdentity, err)
	}
	return Catalog{physical: physical, sourceIdentity: sourceIdentity}, nil
}

func (c Catalog) validate() error {
	if strings.TrimSpace(c.sourceIdentity) == "" {
		return fmt.Errorf("%w: catalog source identity is required", ErrInvalidIdentity)
	}
	if err := c.physical.Validate(); err != nil {
		return fmt.Errorf("%w: catalog: %v", ErrInvalidIdentity, err)
	}
	return nil
}

func (c Catalog) SourceIdentity() string { return c.sourceIdentity }

func (c Catalog) ObjectID(kind schema.ObjectKind, schemaName, name string) (ObjectID, bool) {
	for _, object := range c.physical.Objects {
		if object.Kind == string(kind) && object.Schema == schemaName && object.Name == name {
			return ObjectID(object.ID), true
		}
	}
	return "", false
}

func catalogObjectKey(kind schema.ObjectKind, schemaName, name string) string {
	return string(kind) + "\x00" + schemaName + "\x00" + name
}

func engineIdentity(profile Profile) compilerir.EngineIdentity {
	engine := ""
	switch profile.Engine() {
	case PostgreSQLEngine:
		engine = "postgresql"
	case MySQLEngine:
		engine = "mysql"
	case SQLiteEngine:
		engine = "sqlite"
	case CustomEngine:
		engine = "custom"
	}
	version := profile.Version()
	versionString := ""
	if version.Known {
		versionString = strconv.FormatUint(uint64(version.Major), 10) + "." + strconv.FormatUint(uint64(version.Minor), 10) + "." + strconv.FormatUint(uint64(version.Patch), 10)
	}
	return compilerir.EngineIdentity{Dialect: engine, Version: versionString, Profile: profile.ID()}
}

func sameCatalogIdentity(a, b Catalog) bool {
	return a.sourceIdentity == b.sourceIdentity && a.physical.Engine == b.physical.Engine
}

func samePhysicalObjects(a, b compilerir.PhysicalCatalog) bool {
	if a.Engine != b.Engine || len(a.Objects) != len(b.Objects) {
		return false
	}
	left, err := CatalogDigest(Catalog{physical: a, sourceIdentity: "catalog"})
	if err != nil {
		return false
	}
	right, err := CatalogDigest(Catalog{physical: b, sourceIdentity: "catalog"})
	return err == nil && left == right
}
