# rasql design

## Purpose

`rasql` is a Go SQL toolkit that gives applications one model for schema definitions, dynamic queries, static queries, result decoding, and database inspection. It produces parameterized SQL for PostgreSQL, MySQL, SQLite, and Google Cloud Spanner without hiding dialect differences that affect correctness.

The first release does not include a migration planner or executor. It describes schemas and queries, inspects existing schemas, and provides the pieces that another migration tool can use.

## Design decisions

* Schema definitions, generated schema code, dynamic builders, and static templates share the same public descriptors and query representation.
* Values always travel separately from SQL text. Renderers create placeholders and an ordered argument list; no public API interpolates values into SQL.
* The core model uses logical SQL types. Dialects decide how those types map to DDL, bind values, placeholders, identifiers, and capability-specific syntax.
* The runtime builds on `database/sql` first. Database-specific driver helpers can be added later without changing the core query or schema APIs.
* Result access is explicit and typed. A caller selects a typed column or destination instead of receiving a map with unchecked assertions.
* Schema inspection normalizes live metadata into the same schema descriptors used by Go definitions. Generation turns normalized descriptors into deterministic Go source.
* Generated source, rendered SQL, and errors must be deterministic. Identical inputs produce identical output regardless of map iteration order.

## Component boundaries

| Component | Responsibility | Depends on |
| --- | --- | --- |
| `schema` | Tables, columns, indexes, constraints, and logical SQL types. | Go standard library |
| `dialect` | Identifier quoting, placeholders, type mappings, and syntax capabilities. | `schema` |
| `query` | Dialect-neutral statements and expressions with validation. | `schema` |
| `render` | Converts a validated query into SQL text and ordered arguments. | `dialect`, `query` |
| `row` | Typed column descriptors and result decoding. | Go standard library |
| `runtime` | Executes rendered statements through `database/sql`. | `render`, `row` |
| `inspect` | Reads database metadata and returns normalized schema descriptors. | `schema`, `dialect` |
| `template` and `cmd/rasqlgen` | Compiles static query templates and schema snapshots into Go source. | public packages only |

The dependency flow is deliberately one-way:

```text
schema ──> dialect ──> render ──> runtime
   │                     ▲          │
   ├────> query ─────────┘          ▼
   ├────> inspect              database/sql
   └────> template / rasqlgen
                │
                ▼
            generated Go
```

`query` must not import a concrete dialect. `dialect` must not import query or runtime. Generated code must call only stable public packages, never `internal` packages.

## Public API shape

The public API starts with descriptors rather than a global registry. Applications create or generate a `schema.Table`, keep its typed columns together, and pass them to query builders. This keeps multiple schemas and test fixtures isolated in the same process.

Statements are immutable after construction. Builders may use mutable local state, but `Build` returns a validated query value that can be rendered repeatedly for any supported dialect. Rendering returns a small value containing SQL and arguments, along with an error for unsupported syntax or invalid identifiers.

Result decoding uses typed destinations or typed column descriptors. It must preserve `NULL` distinctly from a zero value. The initial supported primitives are boolean, integer, floating point, string, byte slice, time, and nullable forms. Custom types enter through explicit codecs.

Static query templates compile to Go code that constructs the same query representation as the dynamic API. Template expansion cannot inject raw values into SQL. A narrowly scoped, explicit raw-SQL escape hatch may be added only after the structured API is established, and it must be visible in the call site and testable.

## Dialects and capabilities

Each dialect defines a capability set instead of relying on a database name check scattered through builders. Capabilities include placeholder style, quoted identifier rules, limit and offset form, returning clauses, upsert form, conflict targets, and DDL type mappings.

The first query slice supports `SELECT`, predicates, joins, ordering, limit, and offset across all four dialects. Insert, update, delete, returning, and upsert follow in separate slices because their differences are more significant. A builder reports an unsupported capability before execution.

## Schema and inspection

Schema descriptors include names, columns, nullability, defaults, primary keys, foreign keys, unique constraints, checks, and indexes. They retain source positions when created from Go declarations so validation errors can identify the declaration that needs attention.

Inspectors use a small adapter for each database metadata surface. They normalize identifiers, types, and constraints into `schema` values without attempting to infer application names or Go types. The generator owns Go naming rules and writes a stable file header, imports, table descriptors, columns, and optional codecs.

## Errors and observability

Validation, rendering, decoding, inspection, and execution errors use distinct exported error types or sentinel categories. Errors include the statement operation, dialect, and affected identifier when known, but never include unredacted bound values by default.

The runtime accepts `context.Context` for every database operation. It leaves transaction ownership with the caller and provides helpers that accept `*sql.DB` or `*sql.Tx` without starting hidden transactions.

## Testing strategy

Package tests cover schema validation, query validation, codecs, and capability checks. Dialect renderers use table-driven golden tests that assert SQL and argument order. Inspection adapters run against versioned metadata fixtures before optional live-database integration tests. Generated code is formatted, compiled in a temporary test module, and compared with stable golden files.

## Focused implementation slices

Each slice should be one commit or a small series of commits that remains buildable and has tests for the new public behavior.

1. `docs: add project overview` imports the agreed project goal. This is the initial commit.
2. `docs: add initial architecture design` records boundaries, supported behavior, and the implementation order.
3. `chore: initialize Go module` adds the module, supported Go version, lint configuration, and a minimal package test.
4. `feat: add schema descriptors` adds logical types, tables, columns, constraints, validation, and tests without database access.
5. `feat: add dialect capabilities` adds dialect interfaces and isolated identifier, placeholder, and type-mapping tests.
6. `feat: add select query model` adds immutable expressions and `SELECT` statements with validation, without rendering.
7. `feat: render select statements` renders the first query slice for all four dialects with SQL and argument-order golden tests.
8. `feat: add database runtime` executes rendered statements through `database/sql` and adds typed row decoding for the initial primitives.
9. `feat: add write statements` adds insert, update, and delete in independent commits, followed by returning and upsert capability commits.
10. `feat: inspect live schemas` adds metadata adapters and normalized schema fixtures one dialect at a time.
11. `feat: generate schema source` adds deterministic Go generation from inspected schemas.
12. `feat: compile static queries` adds templates and `rasqlgen`, reusing the query model and renderer.

The work begins with a single dialect-neutral vertical slice through schema, query, rendering, runtime, and row decoding before extending all features across every dialect. This validates the public API before the dialect matrix grows.
