# UX-O-1 acceptance check

## Verdict

FAIL at PR #324 head `576739b212ef66bf3ee9275f98439c0ba7226cb9` against accepted Q6.

The public plan API, generated facades, immutable slice copying, ordered lowering, native `RETURNING` terminals, and
Taskboard generated owners are present. The change has one confirmed generator defect and does not contain the required
generated-consumer regression proof.

## Confirmed defect

`internal/schemagen/schema.go:725` reserves `Default<M>` for every writable column during mutation-method collision
validation, while `internal/schemagen/schema.go:1151` emits that method only when the column has a database default.
This rejects valid schemas whose emitted methods do not collide.

A direct `schemagen.PackageSource` probe with non-defaulted columns `name` and `default_name` returned:

```text
generate: column "default_name" on table "users" collides with create method "DefaultName" from "name"
```

Neither column has a default, so the generated create methods would be `Name` and `DefaultName`; no
`Name.DefaultName` method is emitted from the first column. The same unconditional reservation affects patch builders.

## Missing required proof

The only committed mutation runtime test, `typed_mutation_test.go`, creates one row with an omitted default, then performs
one string patch and one NULL patch. No committed test exercises the following acceptance requirements:

- A generated SQLite consumer creates separate rows with omitted default, explicit zero, explicit false, nullable value,
  and explicit NULL, then checks persisted defaults and generated identity.
- Four separate generated patch calls prove omission, false, zero, and NULL remain distinct.
- Generated setters compile for non-null, explicit nullable-wrapper, and pointer-fallback bindings.
- Independent compile-fail consumers reject wrong setter types, non-null `ClearX`, generated or identity-always setters,
  primary-key patch setters, and stale callers after a generated column rename or type change.
- Stable package-name and builder-method collision fixtures cover only methods actually emitted.
- Runtime cases cover empty plans, zero fields, duplicate fields, cross-table fields, zero predicates, patch defaults,
  constructor-to-terminal sticky errors, and duplicate setters through generated `Create.Plan()`.
- Immutable base create and patch builders remain unchanged after variants are derived.
- Query terminals reject a dialect without `RETURNING` before database access.
- `QueryPatchOne` proves zero-row and multiple-row cardinality, and `QueryPatchAll` proves generated-row decoding.

Existing `typed_write_test.go` coverage confirms the lower-level cardinality helpers. It does not exercise the new plan
terminals or a generated mutation caller. Existing binding acceptance tests use generated query and typed-assignment
APIs, but do not call generated mutation setters.

## Verified gates

- `go test ./internal/schemagen ./generate ./examples .` passed.
- `go test ./...` passed.
- `go vet ./...` passed.
- `git diff --check 2f90e14...HEAD` passed.
- GitHub reports PR #324 open and mergeable at the exact requested head and Q6 base branch.
- GitHub CI passed `Go checks`, `golangci-lint`, and `Database integration`.
- The integration workflow runs `sample/taskboard/scripts/generate.sh -check` against PostgreSQL, so the passing
  `Database integration` job confirms the actual Taskboard generator check and generated owner files.

## Bounded repair tasks

1. Fix `validateMutationMethods` so it reserves `Default<M>` only when `column.Default != ""`; keep `Clear<M>` conditional
   on nullability and preserve the current create/patch eligibility rules. Add focused fixtures for real collisions and
   the valid `name` plus `default_name` case on both builders.
2. Add one generated scratch-consumer suite under `generate` that generates the schema and runs SQLite. Cover exact
   custom bindings, create identity/default/presence cases, four distinct patch calls, builder immutability, duplicate
   setters, `QueryPatchAll`, and `QueryPatchOne` cardinality.
3. Add isolated generated compile-fail fixtures for each forbidden setter/type case and a regenerate-then-stale-caller
   fixture. Add focused root tests for every dynamic constructor error, sticky terminal error, wrong-table field, and
   no-`RETURNING` preflight with a database spy that proves no call occurred.
4. Regenerate owner outputs if generator bytes change, run the focused suites and repository gates, then require the live
   Taskboard generator CI job to pass again.

O5 should remain blocked until these tasks pass because it depends on accepted mutation lowering.
