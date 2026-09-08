package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
)

type planCatalogCandidates struct {
	before, after             changeplan.Catalog
	beforeDigest, afterDigest changeplan.Digest
	beforeErr, afterErr       error
}

func readPlanCatalogTx(
	ctx context.Context,
	tx *sql.Tx,
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	prefix int,
) (changeplan.Catalog, changeplan.Digest, error) {
	result, err := catalogread.ReadTx(ctx, tx, prepared.profile, planCatalogScope(prepared, history))
	if err != nil {
		return changeplan.Catalog{}, changeplan.Digest{}, err
	}
	return planCatalogFromRead(prepared, history, prefix, result)
}

func readPlanCatalogConnTx(
	ctx context.Context,
	connection *sql.Conn,
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	prefix int,
) (changeplan.Catalog, changeplan.Digest, error) {
	result, err := catalogread.ReadConn(ctx, connection, prepared.profile, planCatalogScope(prepared, history))
	if err != nil {
		return changeplan.Catalog{}, changeplan.Digest{}, err
	}
	return planCatalogFromRead(prepared, history, prefix, result)
}

func readPlanCatalogSnapshot(
	ctx context.Context,
	connection *sql.Conn,
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	prefix int,
) (changeplan.Catalog, changeplan.Digest, error) {
	result, err := catalogread.Read(ctx, connection, prepared.profile, planCatalogScope(prepared, history))
	if err != nil {
		return changeplan.Catalog{}, changeplan.Digest{}, err
	}
	return planCatalogFromRead(prepared, history, prefix, result)
}

func planCatalogScope(prepared preparedChangePlan, history resolvedPlanHistory) catalogread.Scope {
	includeViews := false
	for _, object := range prepared.baseline.Objects() {
		if object.Kind() == string(schema.ObjectView) {
			includeViews = true
			break
		}
	}
	return catalogread.Scope{
		HistoryTable: history.historyTable,
		Exclude:      []schema.ObjectName{history.legacyProgressTable, history.planProgressTable},
		IncludeViews: includeViews,
	}
}

func planCatalogFromRead(
	prepared preparedChangePlan,
	_ resolvedPlanHistory,
	prefix int,
	result catalogread.Result,
) (changeplan.Catalog, changeplan.Digest, error) {
	if result.Observed != prepared.profile {
		return changeplan.Catalog{}, changeplan.Digest{}, fmt.Errorf("migrate: observed profile differs from change plan")
	}
	if len(result.Unresolved) != 0 {
		return changeplan.Catalog{}, changeplan.Digest{}, fmt.Errorf("migrate: live catalog contains unresolved facts")
	}
	prior, err := priorObjectsForPrefix(prepared, prefix)
	if err != nil {
		return changeplan.Catalog{}, changeplan.Digest{}, err
	}
	physical, diagnostics := compilerir.PhysicalFromTableDefs(planCompilerEngine(prepared.profile), result.Tables)
	if err := planCatalogDiagnostics("convert", diagnostics); err != nil {
		return changeplan.Catalog{}, changeplan.Digest{}, err
	}
	assigned, diagnostics := compilerir.AssignObjectIDs(physical, compilerir.IdentityInput{
		SourceIdentity: prepared.baseline.SourceIdentity(),
		Prior:          prior,
	})
	if err := planCatalogDiagnostics("assign identity", diagnostics); err != nil {
		return changeplan.Catalog{}, changeplan.Digest{}, err
	}
	if err := assigned.Validate(); err != nil {
		return changeplan.Catalog{}, changeplan.Digest{}, fmt.Errorf("migrate: validate live catalog: %w", err)
	}
	if len(assigned.Objects) != len(prior) || len(result.Tables) != len(prior) {
		return changeplan.Catalog{}, changeplan.Digest{}, fmt.Errorf("migrate: live catalog object count differs from plan prefix")
	}
	expected := make(map[compilerir.ObjectID]compilerir.PriorObject, len(prior))
	for _, object := range prior {
		expected[object.ID] = object
	}
	definitions := make(map[string]schema.TableDef, len(result.Tables))
	for _, table := range result.Tables {
		definitions[planObjectKey(string(table.EffectiveKind()), table.Schema, table.Name)] = table
	}
	objects := make([]changeplan.CatalogObject, 0, len(assigned.Objects))
	for _, object := range assigned.Objects {
		want, exists := expected[object.ID]
		if !exists || want.Kind != object.Kind || want.Name.Schema != object.Schema || want.Name.Name != object.Name {
			return changeplan.Catalog{}, changeplan.Digest{}, fmt.Errorf("migrate: live object %q does not match plan prefix", object.ID)
		}
		definition, exists := definitions[planObjectKey(object.Kind, object.Schema, object.Name)]
		if !exists {
			return changeplan.Catalog{}, changeplan.Digest{}, fmt.Errorf("migrate: live object %q has no source definition", object.ID)
		}
		value, err := changeplan.NewCatalogObject(changeplan.ObjectID(object.ID), definition)
		if err != nil {
			return changeplan.Catalog{}, changeplan.Digest{}, err
		}
		objects = append(objects, value)
	}
	catalog, err := changeplan.NewCatalog(planProfileAdapter{prepared.profile}, prepared.baseline.SourceIdentity(), objects)
	if err != nil {
		return changeplan.Catalog{}, changeplan.Digest{}, err
	}
	digest, err := changeplan.CatalogDigest(catalog)
	if err != nil {
		return changeplan.Catalog{}, changeplan.Digest{}, err
	}
	return catalog, digest, nil
}

func planObjectKey(kind, schemaName, name string) string {
	return kind + "\x00" + schemaName + "\x00" + name
}

func planCatalogDiagnostics(stage string, diagnostics []compilerir.Diagnostic) error {
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return fmt.Errorf("migrate: %s live catalog: %s", stage, diagnostic.Message)
		}
	}
	return nil
}

func planCompilerEngine(profile engineprofile.Profile) compilerir.EngineIdentity {
	dialectName := map[engineprofile.EngineID]string{
		engineprofile.PostgreSQL: "postgresql",
		engineprofile.MySQL:      "mysql",
		engineprofile.SQLite:     "sqlite",
	}[profile.Engine]
	version := ""
	if profile.Version.Known {
		version = strconv.Itoa(int(profile.Version.Major)) + "." + strconv.Itoa(int(profile.Version.Minor)) + "." +
			strconv.Itoa(int(profile.Version.Patch))
	}
	return compilerir.EngineIdentity{Dialect: dialectName, Version: version, Profile: profile.ID}
}

type planProfileAdapter struct{ engineprofile.Profile }

func (p planProfileAdapter) ID() string                        { return p.Profile.ID }
func (p planProfileAdapter) Engine() changeplan.EngineID       { return p.Profile.Engine }
func (p planProfileAdapter) Version() changeplan.EngineVersion { return p.Profile.Version }
func (p planProfileAdapter) Capabilities() changeplan.EngineCapabilities {
	return p.Profile.Capabilities
}
func (p planProfileAdapter) Limits() changeplan.EngineLimits { return p.Profile.Limits }

func readPlanCatalogCandidatesTx(
	ctx context.Context,
	tx *sql.Tx,
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	prefix int,
) (planCatalogCandidates, error) {
	result, err := catalogread.ReadTx(ctx, tx, prepared.profile, planCatalogScope(prepared, history))
	if err != nil {
		return planCatalogCandidates{}, err
	}
	return planCatalogCandidatesFromRead(prepared, history, prefix, result), nil
}

func readPlanCatalogCandidatesConnTx(
	ctx context.Context,
	connection *sql.Conn,
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	prefix int,
) (planCatalogCandidates, error) {
	result, err := catalogread.ReadConn(ctx, connection, prepared.profile, planCatalogScope(prepared, history))
	if err != nil {
		return planCatalogCandidates{}, err
	}
	return planCatalogCandidatesFromRead(prepared, history, prefix, result), nil
}

func readPlanCatalogCandidatesSnapshot(
	ctx context.Context,
	connection *sql.Conn,
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	prefix int,
) (planCatalogCandidates, error) {
	result, err := catalogread.Read(ctx, connection, prepared.profile, planCatalogScope(prepared, history))
	if err != nil {
		return planCatalogCandidates{}, err
	}
	return planCatalogCandidatesFromRead(prepared, history, prefix, result), nil
}

func planCatalogCandidatesFromRead(
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	prefix int,
	result catalogread.Result,
) planCatalogCandidates {
	var candidates planCatalogCandidates
	if prefix < 0 || prefix >= len(prepared.operations) {
		candidates.beforeErr = fmt.Errorf("migrate: recovery prefix %d is outside operation range", prefix)
		candidates.afterErr = candidates.beforeErr
		return candidates
	}
	candidates.before, candidates.beforeDigest, candidates.beforeErr = planCatalogFromRead(prepared, history, prefix, result)
	candidates.after, candidates.afterDigest, candidates.afterErr = planCatalogFromRead(prepared, history, prefix+1, result)
	return candidates
}
