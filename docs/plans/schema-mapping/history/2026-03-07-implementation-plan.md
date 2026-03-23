# MySQL Database to PostgreSQL Schema Mapping Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement a stable and safe mapping where one MySQL database maps to one PostgreSQL schema inside a single fixed PostgreSQL database.

**Architecture:** Keep the backend PostgreSQL database fixed in configuration, and treat the MySQL database name as session-scoped schema state. Centralize schema resolution and `search_path` application so the initial DSN database, `COM_INIT_DB`, and `USE db` all follow the same path, and make metadata/cache behavior use explicit schema semantics.

**Tech Stack:** Go, go-mysql-org/go-mysql, pgx/v5, YAML config, testify, existing unit tests under `pkg/...` and integration tests under `test/integration/...`.

---

### Task 1: Add Mapping Configuration Model

**Files:**
- Modify: `internal/config/config.go`
- Modify: `configs/config.yaml`
- Modify: `configs/config-binlog.yaml`
- Test: `internal/config/config_test.go`

**Step 1: Write the failing test**

Create `internal/config/config_test.go` with tests that assert:

```go
func TestDefaultConfigDatabaseMapping(t *testing.T) {
    cfg := DefaultConfig()

    require.Equal(t, "schema", cfg.DatabaseMapping.Mode)
    require.Equal(t, "public", cfg.DatabaseMapping.DefaultSchema)
    require.True(t, cfg.DatabaseMapping.FallbackToPublic)
    require.False(t, cfg.DatabaseMapping.CreateSchemaIfNotExists)
    require.True(t, cfg.DatabaseMapping.ValidateSchemaName)
}
```

Add a second test that validates bad configuration is rejected:

```go
func TestValidateRejectsInvalidDatabaseMappingMode(t *testing.T) {
    cfg := DefaultConfig()
    cfg.DatabaseMapping.Mode = "database"

    err := cfg.Validate()
    require.Error(t, err)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/config -run "Test(DefaultConfigDatabaseMapping|ValidateRejectsInvalidDatabaseMappingMode)" -v`

Expected: FAIL because `DatabaseMapping` does not exist yet.

**Step 3: Write minimal implementation**

In `internal/config/config.go`:

- Add `DatabaseMappingConfig` to the root `Config` struct.
- Add fields:
  - `Mode string`
  - `DefaultSchema string`
  - `FallbackToPublic bool`
  - `CreateSchemaIfNotExists bool`
  - `ValidateSchemaName bool`
  - `Rules map[string]string`
- Initialize defaults in `DefaultConfig()`.
- Validate that:
  - `Mode == "schema"`
  - `DefaultSchema != ""`
- Keep validation minimal; do not implement schema-name regex checks here.

Update `configs/config.yaml` and `configs/config-binlog.yaml` to add a `database_mapping` block with explicit defaults.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/config -run "Test(DefaultConfigDatabaseMapping|ValidateRejectsInvalidDatabaseMappingMode)" -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go configs/config.yaml configs/config-binlog.yaml
git commit -m "feat: add database mapping config"
```

### Task 2: Introduce Schema Resolution and Safe search_path Helper

**Files:**
- Create: `pkg/session/schema_mapping.go`
- Create: `pkg/session/schema_mapping_test.go`

**Step 1: Write the failing test**

Create `pkg/session/schema_mapping_test.go` with tests for resolution, validation, and generated `search_path` SQL components. Include at least:

```go
func TestResolveSchemaUsesRuleOverride(t *testing.T) {
    mapper := NewSchemaMapper(Config{
        DefaultSchema: "public",
        FallbackToPublic: true,
        ValidateSchemaName: true,
        Rules: map[string]string{"analytics": "ods_analytics"},
    })

    schema, err := mapper.ResolveSchema("analytics")
    require.NoError(t, err)
    require.Equal(t, "ods_analytics", schema)
}

func TestResolveSchemaRejectsInvalidName(t *testing.T) {
    mapper := NewSchemaMapper(Config{ValidateSchemaName: true})

    _, err := mapper.ResolveSchema("bad-name;drop schema public")
    require.Error(t, err)
}

func TestBuildSearchPathSQLWithPublicFallback(t *testing.T) {
    sql, err := BuildSearchPathSQL("tenant_a", true)
    require.NoError(t, err)
    require.Equal(t, `SET search_path TO "tenant_a", public`, sql)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/session -run "Test(ResolveSchemaUsesRuleOverride|ResolveSchemaRejectsInvalidName|BuildSearchPathSQLWithPublicFallback)" -v`

Expected: FAIL because helper types/functions do not exist.

**Step 3: Write minimal implementation**

In `pkg/session/schema_mapping.go`:

- Add a small mapper type dedicated to schema mapping.
- Implement:
  - `ResolveSchema(mysqlDB string) (string, error)`
  - `BuildSearchPathSQL(schema string, fallbackToPublic bool) (string, error)`
  - `ValidateSchemaName(name string) error`
- Use a strict regex such as `^[A-Za-z_][A-Za-z0-9_]*$`.
- Quote schema identifiers deterministically with double quotes.
- If MySQL database is empty, return `DefaultSchema`.

Keep this helper pure; it should not talk to PostgreSQL yet.

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/session -run "Test(ResolveSchemaUsesRuleOverride|ResolveSchemaRejectsInvalidName|BuildSearchPathSQLWithPublicFallback)" -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add pkg/session/schema_mapping.go pkg/session/schema_mapping_test.go
git commit -m "feat: add schema mapping helper"
```

### Task 3: Split Session State into Backend Database and Current Schema

**Files:**
- Modify: `pkg/session/session.go`
- Modify: `pkg/protocol/mysql/handler.go`
- Test: `pkg/session/session_test.go`

**Step 1: Write the failing test**

Create `pkg/session/session_test.go` with coverage for the new session fields and defaults:

```go
func TestNewSessionInitializesSchemaState(t *testing.T) {
    sess := NewSession("root", "tenant_a", "127.0.0.1")

    require.Equal(t, "tenant_a", sess.CurrentSchema)
    require.Equal(t, "", sess.BackendDatabase)
}
```

Add another test for setter behavior if you introduce one:

```go
func TestSessionSetCurrentSchema(t *testing.T) {
    sess := NewSession("root", "tenant_a", "127.0.0.1")
    sess.SetCurrentSchema("tenant_b")
    require.Equal(t, "tenant_b", sess.CurrentSchema)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/session -run "Test(NewSessionInitializesSchemaState|SessionSetCurrentSchema)" -v`

Expected: FAIL because `CurrentSchema` and `BackendDatabase` do not exist.

**Step 3: Write minimal implementation**

In `pkg/session/session.go`:

- Replace `Database string` with:
  - `BackendDatabase string`
  - `CurrentSchema string`
- Update `NewSession` so the second argument seeds `CurrentSchema`.
- If needed, add simple getter/setter helpers for current schema.

In `pkg/protocol/mysql/handler.go`:

- Replace direct `session.Database` access with `session.CurrentSchema` where the value is used as schema state.
- Do not change behavior yet beyond field renaming.

**Step 4: Run focused tests to verify it passes**

Run: `go test ./pkg/session ./pkg/protocol/mysql -run "Test(NewSessionInitializesSchemaState|SessionSetCurrentSchema|TestIsKillStatement|TestExtractInsertTableName)" -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add pkg/session/session.go pkg/session/session_test.go pkg/protocol/mysql/handler.go
git commit -m "refactor: split backend database and current schema"
```

### Task 4: Apply Schema on First PG Connection and on USE/COM_INIT_DB

**Files:**
- Modify: `pkg/protocol/mysql/handler.go`
- Modify: `pkg/mapper/show.go`
- Test: `pkg/protocol/mysql/handler_test.go`
- Test: `pkg/mapper/show_test.go`

**Step 1: Write the failing test**

Add tests that pin the new control flow rather than doing full database integration.

In `pkg/protocol/mysql/handler_test.go`, add a small unit around schema switching decisions. If current code is hard to test, first introduce a narrow seam, such as a function that returns SQL for applying schema:

```go
func TestUseDBRejectsSchemaSwitchInsideTransaction(t *testing.T) {
    sess := session.NewSession("root", "tenant_a", "127.0.0.1")
    sess.InTransaction = true

    ch := &ConnectionHandler{session: sess}
    err := ch.UseDB("tenant_b")
    require.Error(t, err)
}
```

In `pkg/mapper/show_test.go`, add a test that the USE handler no longer blindly concatenates the raw input and instead rejects invalid schema names.

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/protocol/mysql ./pkg/mapper -run "Test(UseDBRejectsSchemaSwitchInsideTransaction|ShowEmulator.*Use.*)" -v`

Expected: FAIL because current code allows direct string concatenation and has no transaction guard.

**Step 3: Write minimal implementation**

In `pkg/protocol/mysql/handler.go`:

- Initialize `session.BackendDatabase` from proxy config when the session or connection handler is created.
- On first PG connection acquisition, resolve and apply `session.CurrentSchema` before running user SQL.
- In `UseDB(dbName string)`:
  - Reject schema switch if `session.InTransaction` is true.
  - Resolve the MySQL database name to mapped schema.
  - Update `session.CurrentSchema`.
  - Apply schema safely if `pgConn` already exists.

In `pkg/mapper/show.go`:

- Remove direct `fmt.Sprintf("SET search_path TO %s", dbName)` behavior.
- Reuse the same resolver/apply helper as protocol handler, or narrow `show.go` to only parse the command and return the db name to the caller.

Keep all schema-application logic centralized; do not leave one-off SQL generation in both files.

**Step 4: Run focused tests to verify it passes**

Run: `go test ./pkg/protocol/mysql ./pkg/mapper -run "Test(UseDBRejectsSchemaSwitchInsideTransaction|ShowEmulator.*Use.*|TestShowEmulatorHandleSetCommand_.*)" -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add pkg/protocol/mysql/handler.go pkg/protocol/mysql/handler_test.go pkg/mapper/show.go pkg/mapper/show_test.go
git commit -m "feat: apply mapped schema during session lifecycle"
```

### Task 5: Update Schema Cache and Metadata Semantics

**Files:**
- Modify: `pkg/schema/cache.go`
- Modify: `pkg/protocol/mysql/handler.go`
- Modify: `pkg/mapper/show.go`
- Test: `pkg/schema/cache_test.go`
- Test: `test/integration/basic_test.go`

**Step 1: Write the failing test**

Create `pkg/schema/cache_test.go` with focused tests for key semantics:

```go
func TestCacheUsesSchemaQualifiedKey(t *testing.T) {
    cache := InitGlobalCache(time.Minute)
    cache.InvalidateTable("tenant_a", "users")
    // Assert helper methods build "tenant_a.users" keys consistently.
}
```

Then extend `test/integration/basic_test.go` with integration assertions that make the schema behavior visible:

- `SHOW DATABASES` still returns at least one logical database.
- `USE test` followed by `CREATE TABLE` creates the table in schema `test`.
- `SHOW TABLES` only lists tables from the current schema.

If an existing test helper can query PostgreSQL directly, use it to verify schema placement. If not, add the smallest helper needed in this file rather than creating a new integration harness.

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/schema -run TestCacheUsesSchemaQualifiedKey -v`

Run: `go test ./test/integration/... -run "TestShowCommands" -v`

Expected: unit test FAIL because no schema-focused cache helper exists; integration test should either fail or lack the new assertions.

**Step 3: Write minimal implementation**

In `pkg/schema/cache.go`:

- Rename internal comments and helper semantics from `database.table` to `schema.table`.
- If helpful, add a tiny helper like `buildCacheKey(schemaName, tableName string) string` so semantics stay consistent.

In `pkg/protocol/mysql/handler.go`:

- Ensure DDL invalidation uses `session.CurrentSchema`.

In `pkg/mapper/show.go`:

- Keep `SHOW TABLES` and column metadata logic aligned with current schema semantics.

**Step 4: Run tests to verify it passes**

Run: `go test ./pkg/schema -run TestCacheUsesSchemaQualifiedKey -v`

Run: `go test ./test/integration/... -run "TestShowCommands" -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add pkg/schema/cache.go pkg/schema/cache_test.go pkg/protocol/mysql/handler.go pkg/mapper/show.go test/integration/basic_test.go
git commit -m "refactor: align cache and metadata with schema semantics"
```

### Task 6: Document Behavior and Update User-Facing Terminology

**Files:**
- Modify: `README.md`
- Modify: `docs/RUNBOOK.md`
- Modify: `docs/plans/schema-mapping/history/2026-03-07-design.md`

**Step 1: Write the failing doc checklist**

Add a short checklist to your working notes and verify these statements are not yet true in docs:

- README explicitly says MySQL database maps to PostgreSQL schema.
- README explains `SHOW DATABASES` returns logical schema-backed databases.
- RUNBOOK explains why tables may appear under the wrong PostgreSQL schema when no database/schema is set.

This is a manual failing step: open the docs and confirm the wording is still ambiguous.

**Step 2: Run manual verification to confirm gap**

Run: `rg -n "SHOW DATABASES|USE database|COM_INIT_DB|schema|search_path" README.md docs/RUNBOOK.md`

Expected: existing docs mention support but do not clearly state the fixed-DB plus schema-mapping model.

**Step 3: Write minimal documentation updates**

Update `README.md` to state:

- PostgreSQL database is fixed by proxy config.
- MySQL database names map to PostgreSQL schemas.
- Initial DSN database and `USE db` are equivalent.

Update `docs/RUNBOOK.md` to add a troubleshooting note for “表创建成功但在 PostgreSQL 看错位置”.

Update the design doc only if implementation details changed from the accepted design.

**Step 4: Run manual verification to confirm docs are clear**

Run: `rg -n "maps to PostgreSQL schema|fixed PostgreSQL database|search_path" README.md docs/RUNBOOK.md docs/plans/schema-mapping/history/2026-03-07-design.md`

Expected: all three concepts are discoverable from docs.

**Step 5: Commit**

```bash
git add README.md docs/RUNBOOK.md docs/plans/schema-mapping/history/2026-03-07-design.md
git commit -m "docs: clarify mysql database to pg schema mapping"
```

### Task 7: Run Full Verification Before Merge

**Files:**
- Modify: none
- Test: `internal/config/config_test.go`
- Test: `pkg/session/schema_mapping_test.go`
- Test: `pkg/session/session_test.go`
- Test: `pkg/protocol/mysql/handler_test.go`
- Test: `pkg/mapper/show_test.go`
- Test: `pkg/schema/cache_test.go`
- Test: `test/integration/basic_test.go`

**Step 1: Run focused unit tests**

Run: `go test ./internal/config ./pkg/session ./pkg/protocol/mysql ./pkg/mapper ./pkg/schema -v`

Expected: PASS.

**Step 2: Run targeted integration tests**

Run: `go test ./test/integration/... -run "Test(BasicCRUD|ShowCommands)" -v`

Expected: PASS with schema-aware assertions.

**Step 3: Run broader regression suite**

Run: `go test ./pkg/... ./internal/... -v`

Expected: PASS.

**Step 4: Review diff for terminology drift**

Run: `rg -n "database\.table|CurrentSchema|BackendDatabase|search_path" pkg internal README.md docs`

Expected: references to schema semantics are consistent; no stale comments remain claiming cache keys are `database.table` unless intentionally kept for protocol-facing wording.

**Step 5: Commit final verification snapshot**

```bash
git add -A
git commit -m "test: verify schema mapping implementation"
```

## Notes for the Implementer

- Keep the backend PostgreSQL database fixed. Do not add code that reconnects to a different PostgreSQL database based on MySQL `USE`.
- Prefer centralizing schema mapping under one helper instead of scattering `SET search_path` SQL building across handler and mapper code.
- Keep the first implementation strict: reject invalid schema names rather than trying to support every PostgreSQL identifier edge case.
- Do not widen scope to schema auto-creation unless config explicitly enables it.
- When adding tests, prefer narrow unit tests first, then one or two high-value integration assertions in `test/integration/basic_test.go`.

Plan complete and saved to `docs/plans/schema-mapping/history/2026-03-07-implementation-plan.md`. Two execution options:

**1. Subagent-Driven (this session)** - I dispatch fresh subagent per task, review between tasks, fast iteration

**2. Parallel Session (separate)** - Open new session with executing-plans, batch execution with checkpoints

Which approach?