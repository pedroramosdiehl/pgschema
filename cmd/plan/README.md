# Plan command

Compare a desired-state SQL file with the live database and print migration DDL (human, JSON, and/or SQL).

## Usage

```bash
pgschema plan \
  --host localhost \
  --port 5432 \
  --db mydb \
  --user postgres \
  --schema public \
  --file schema.sql \
  --output-human stdout
```

- **`--file`**: path to the SQL that describes the desired schema (required).
- **`--schema`**: see [Single schema](#single-schema) and [Multi-schema](#multi-schema) below.
- **Outputs**: `--output-human`, `--output-json`, `--output-sql` (each can be `stdout` or a file path). If none are set, human output goes to stdout.

Optional **plan database** (instead of the default embedded Postgres): `--plan-host`, `--plan-port`, `--plan-db`, `--plan-user`, `--plan-password`, `--plan-sslmode`, or `PGSCHEMA_PLAN_*` env vars. See the main project docs for details.

## How plan works

`pgschema plan` is inspector-based on both sides:

1. **Read current state from target database**  
   The schema(s) from `--schema` are introspected directly from the target DB.

2. **Materialize desired state on a provider database**  
   Your `--file` (including `\i` chunks) is applied to:
   - embedded Postgres by default, or
   - an external plan DB when `--plan-host` is set.

3. **Inspect desired state from provider database**  
   Temporary schema names used during planning are normalized back to the target schema names.

4. **Diff current -> desired and build execution plan**  
   The diff engine emits dependency-aware DDL with deterministic ordering.

### Dependency ordering and foreign keys

The generated migration SQL keeps this high-level order:

1. `DROP` phase
2. `CREATE` phase
3. `MODIFY` phase
4. **Deferred foreign-key flush**

Foreign keys are deferred to the final flush when needed, including:

- referenced table is not yet created in the current create batch
- referenced PK/UNIQUE is added later in the same migration (`MODIFY` phase), even if the referenced table already exists

This avoids the classic "chicken-and-egg" failure for cyclic/cross-schema FK dependencies while preserving inline FK creation for non-problematic cases.

## Single schema

`--schema` defaults to `public`. Only that PostgreSQL namespace is loaded from the target database and from the temporary plan database after your SQL is applied.

## Multi-schema

Pass a **comma-separated** list of schema names (spaces trimmed, duplicates removed):

```bash
pgschema plan \
  --schema public,app \
  --file schema.sql \
  ...
```

### Behaviour

1. **Target (current) state**  
   All listed schemas are introspected and merged into one IR, so the diff can see tables, views, functions, etc. in `public`, `app`, and any other name you include.

2. **Desired state**  
   - The **first** name in the list is the *primary* schema: your `--file` SQL is applied in the temporary plan database with that schema as the strip/normalize target (same as single-schema plan).  
   - After that, the temporary schema **and** every other listed schema are introspected on the plan database. That way, objects you created with explicit qualification (e.g. `app.some_table`) appear in the desired IR as long as `app` is included after the comma.

3. **Generated DDL**  
   Diffing still uses the primary schema for name normalization where applicable; cross-schema references in the IR are preserved as in single-schema mode.

### When to use it

Use multi-schema when a single migration touches more than one namespace (e.g. `public` facts and `app` dimensions) or when foreign keys span schemas you want in the same plan.

### Caveats

- **`dump` / `apply`**: today their `--schema` flag is still a **single** schema name for connection defaults and fingerprinting. A plan built with `--schema public,app` can include DDL for multiple namespaces; applying it may require running `apply` with a workflow that matches your process (for example separate apply runs per schema if you rely on `search_path`), or extending apply in the future.
- **Order matters**: always put the schema where the bulk of unqualified DDL in `--file` lives **first**.

## Running tests

```bash
# All plan tests
go test -v ./cmd/plan/

# Specific plan tests
go test -v ./cmd/plan/ -run "TestPlanCommand_FileToDatabase"
```
