# R2 acceptance fixtures

Task: R2 mutation acceptance fixtures.
Branch: `feat-orm-r2-mutations`.
Worktree: `/home/lestrrat/dev/src/github.com/lestrrat-go/rasql/.worktrees/feat-orm-r2-mutations`.
Base: `1cc3341`.

The worktree contains the five assigned acceptance files. Four files were already present in the supplied branch:

- `mutation_acceptance_test.go` covers SQLite default, explicit zero, NULL, returning, and non-returning writes.
- `mutation_atomic_acceptance_test.go` covers atomic commit, rollback, event ordering, cancellation, and limit preflight.
- `mutation_codec_acceptance_test.go` covers root codecs, NULL bypass, and missing codec rejection.
- `mutation_live_acceptance_test.go` covers PostgreSQL update/delete returning and MySQL atomic batches.

I added `mutation_returning_acceptance_test.go`, which covers versioned returning cardinalities, declared zero/one/two
row behavior, early break, and aliased version columns.

Validation:

```text
GOCACHE=$PWD/.tmp/r2-gocache go test . -run '^$' -count=1
PASS
```

The focused acceptance run was:

```text
GOCACHE=$PWD/.tmp/r2-gocache go test . -run 'TestSQLiteMutationFourStatesAndReturning|TestMutationVersionedReturningCardinalityMatrix|TestMutationVersionedPatchCanonicalizesAliasedVersionColumn|TestRootMutationColumnCodecEncodesEachOccurrence|TestMissingMutationCodecFailsBeforeExecution|TestMutationAtomicCommitAndRollbackLifecycle|TestMutationAtomicPreflightRejectsNegativeLimitsWithoutExecution' -count=1
```

The focused acceptance run passed.

The live acceptance run used the explicit PostgreSQL DSN
`postgres://rasql:rasql@127.0.0.1:5432/rasql?sslmode=disable` and MySQL DSN
`root:root@tcp(127.0.0.1:3306)/rasql?parseTime=true`. Both live fixtures passed.
