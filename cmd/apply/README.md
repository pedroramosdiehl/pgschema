# Apply Command

Apply a migration plan to the target database.

You can run `apply` in two modes:

- **File mode** (`--file`): generate a plan first, then execute it.
- **Plan mode** (`--plan`): load a pre-generated JSON plan and execute it directly.

## Usage

### Apply from desired state file

```bash
pgschema apply \
  --host localhost \
  --port 5432 \
  --db mydb \
  --user postgres \
  --schema public \
  --file schema.sql
```

### Apply from pre-generated plan

```bash
pgschema apply \
  --host localhost \
  --port 5432 \
  --db mydb \
  --user postgres \
  --schema public \
  --plan plan.json
```

Useful flags:

- `--auto-approve`: skip interactive confirmation.
- `--lock-timeout`: set `lock_timeout` before execution (e.g. `30s`, `5m`).
- `--plan-host` and related flags: when using `--file`, pick external plan DB instead of embedded Postgres.

## How apply works

1. **Resolve plan input**
   - `--plan`: reads JSON plan, validates pgschema version and supported plan format.
   - `--file`: generates a plan using the same plan pipeline as `pgschema plan`.

2. **Validate source fingerprint**  
   If the plan contains a source fingerprint, apply re-inspects current DB state and aborts if drift is detected.  
   For comma-separated `--schema` (for example `comum,integracao`), fingerprint validation uses the same schema set as plan generation.

3. **Render plan and request approval**  
   Unless `--auto-approve` is set.

4. **Prepare session**  
   - optional `SET lock_timeout = ...`
   - optional `SET search_path TO <primary-schema>, public` when `--schema` is not `public`  
     (`<primary-schema>` is the first name in `--schema`, e.g. `integracao` in `integracao,comum`)

5. **Execute plan groups**
   - groups without directives are concatenated and executed in one implicit transaction
   - groups with directives are executed step-by-step

6. **Fail fast on first error**  
   Transactional groups roll back as one unit.

## Multi-schema notes

- `apply --schema` accepts comma-separated schemas (same format as `plan`), mainly for fingerprint validation scope.
- Plans can contain schema-qualified DDL for multiple schemas and apply executes the SQL exactly as emitted by `plan`.
- Final migration SQL should never contain temporary materialization schemas (`pgschema_tmp_*`). If found, treat it as a bug/regression.

## FK dependency behavior during apply

`apply` executes the order emitted by `plan`. With current diff ordering, migrations run with:

1. drop objects
2. create objects
3. modify existing objects
4. add deferred foreign keys

This means FK constraints that depend on PK/UNIQUE added in the same migration are applied only after those keys exist, reducing apply-time failures in cyclic/cross-schema dependency scenarios.

## Running Tests

```bash
# All apply tests
go test -v ./cmd/apply/

# Specific apply tests
go test -v ./cmd/apply/ -run "TestApplyCommand_TransactionRollback"
```
