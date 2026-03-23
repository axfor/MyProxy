# Schema Mapping Task Status

## Usage Rules

1. 每次只执行一个 Task，或在明确批准下执行一小段强耦合 Task。
2. 开始一个 Task 前，先把 `Status` 改成 `in-progress`。
3. 完成一个 Task 后，必须更新：
   - `Status`
   - `Files changed`
   - `Tests run`
   - `Result`
   - `Notes`
   - `Next dependency`
4. 如果任务阻塞，不要跳过；把 `Status` 改成 `blocked`，并写清原因。
5. 新 agent 启动时，先读取本文件和 `implementation-plan.md`，然后只处理第一个未完成的 Task。

## Status Legend

- `not-started`: 尚未开始
- `in-progress`: 正在执行
- `blocked`: 被阻塞，等待额外信息或前置任务
- `completed`: 已完成并完成该任务要求的验证

---

## Task 1

Status: completed
Owner: Copilot
Files changed:
- internal/config/config.go
- internal/config/config_test.go
- configs/config.yaml
- configs/config-binlog.yaml

Tests run:
- go test ./internal/config -run "Test(DefaultConfig|LoadConfig).*DatabaseMapping" -v

Result:
- PASS

Notes:
- Added `DatabaseMappingConfig` to top-level config with `default_schema=public`, `fallback_to_public=false`, and YAML `rules` loading coverage.
- Added explicit `database_mapping` blocks to both sample config files.
- Did not retain a `mode` field, so no mode-specific validation was added in Task 1.

Next dependency:
- Task 2

## Task 2

Status: completed
Owner: Copilot
Files changed:
- pkg/schema/mapping.go
- pkg/schema/mapping_test.go

Tests run:
- go test ./pkg/schema -run "Test(ResolveSchema|ValidateSchemaName|BuildSearchPathSQL)" -v

Result:
- PASS

Notes:
- Added a pure schema mapping helper with `MappingConfig`, `Resolver`, `ResolveSchema`, `ValidateSchemaName`, and `BuildSearchPathSQL`.
- Validation uses a strict identifier whitelist: letters, digits, and underscores, with the first character not a digit.
- Kept scope to Task 2 only; `ApplySchema` and runtime/session wiring were not implemented.

Next dependency:
- Task 3

## Task 3

Status: completed
Owner: Copilot
Files changed:
- pkg/schema/mapping.go
- pkg/schema/mapping_test.go

Tests run:
- go test ./pkg/schema -run "Test(ApplySchema|BuildSearchPathSQL)" -v

Result:
- PASS

Notes:
- Added `ApplySchema` and a small execution interface so schema application can be tested without mocking `pgx.Conn` directly.
- `ApplySchema` now delegates SQL construction to `BuildSearchPathSQL`, rejects invalid schema names before execution, and preserves strict mode without appending `public`.
- Kept scope to Task 3 only; no handler wiring or session state changes were added.

Next dependency:
- Task 4

## Task 4

Status: completed
Owner: Copilot
Files changed:
- pkg/session/session.go
- pkg/session/session_test.go

Tests run:
- go test ./pkg/session -run "Test(NewSession|SetCurrentSchema|SchemaState)" -v

Result:
- PASS

Notes:
- Added `BackendDatabase` and `CurrentSchema` to session state.
- `NewSession` now initializes `CurrentSchema`, and the minimal setters preserve compatibility by syncing `Database` when schema changes.
- Kept scope to Task 4 only; no handler wiring or cache call-site changes were made yet.

Next dependency:
- Task 5

## Task 5

Status: completed
Owner: Copilot
Files changed:
- pkg/protocol/mysql/handler.go
- pkg/protocol/mysql/handler_test.go

Tests run:
- go test ./pkg/protocol/mysql -run "TestUseDB" -v

Result:
- PASS

Notes:
- Added `UseDB` tests for `session_affinity`, `pooled`, `hybrid`, and in-transaction behavior.
- `UseDB` now rejects non-`session_affinity` modes and active transactions with explicit errors before attempting any schema switch.
- Kept scope to Task 5 only; no first-connection schema application or pooled/hybrid replay logic was added.

Next dependency:
- Task 6

## Task 6

Status: completed
Owner: Copilot
Files changed:
- pkg/protocol/mysql/handler.go
- pkg/protocol/mysql/handler_test.go

Tests run:
- go test ./pkg/protocol/mysql -run "Test(UseDB|SwitchSchema).*State" -v

Result:
- PASS

Notes:
- Added focused state-transition tests proving session schema state updates only after `ApplySchema` succeeds.
- Introduced a minimal shared `switchSchema` helper that performs `ResolveSchema`, then `ApplySchema`, then `SetCurrentSchema`.
- Kept failure paths state-safe: `CurrentSchema` and compatibility field `Database` remain unchanged when schema application fails.

Next dependency:
- Task 7

## Task 7

Status: completed
Owner: Copilot
Files changed:
- pkg/protocol/mysql/handler.go
- pkg/protocol/mysql/handler_test.go

Tests run:
- go test ./pkg/protocol/mysql -run "TestInitialSchema" -v

Result:
- PASS

Notes:
- Added focused tests for first-connection initial schema application, non-`session_affinity` rejection, and failure preserving `CurrentSchema`.
- Added a minimal `applyInitialSchema` hook after the first successful backend acquisition and routed it through the shared `switchSchema` path.
- Kept failure paths state-safe: failed initial schema application does not set `CurrentSchema`.

Next dependency:
- Task 8

## Task 8

Status: completed
Owner: Copilot
Files changed:
- pkg/schema/cache.go
- pkg/schema/cache_test.go
- pkg/protocol/mysql/handler.go

Tests run:
- go test ./pkg/schema -run "Test.*Cache.*Schema" -v

Result:
- PASS

Notes:
- Added focused cache tests for `schema.table` key semantics and schema-scoped invalidation.
- Introduced a shared cache-key helper and updated cache comments/metadata to use schema terminology instead of database terminology.
- Updated DDL cache invalidation call sites to use `CurrentSchema` semantics.

Next dependency:
- Task 9

## Task 9

Status: completed
Owner: Copilot
Files changed:
- internal/config/config.go
- internal/config/config_test.go
- pkg/schema/mapping.go
- pkg/schema/mapping_test.go

Tests run:
- go test ./internal/config ./pkg/schema -run "Test.*Fallback.*Public" -v

Result:
- PASS

Notes:
- Added focused config tests and schema tests that lock the strict default boundary and explicit `public` fallback enablement.
- Added `DatabaseMappingConfig.ToSchemaMappingConfig()` so config state carries `FallbackToPublic` into schema mapping config.
- Added resolver-level `BuildSearchPathSQL` behavior that uses the configured fallback boundary; no broader runtime wiring or documentation rewrite was added in this task.

Next dependency:
- Task 10

## Task 10

Status: blocked
Owner: Copilot
Files changed:
- docs/plans/schema-mapping/execution/task-status.md

Tests run:
- go test ./test/integration -run "TestSchemaMapping" -v

Result:
- FAIL: integration environment unavailable (`localhost:5432` PostgreSQL refused direct setup connection)
- FAIL: integration environment unavailable (`localhost:3306` aproxy/MySQL endpoint refused probe connection)

Notes:
- Completed part: drafted and attempted focused integration coverage for initial database application, `USE db` schema targeting, and strict-mode no-`public` fallback.
- Blocker: this environment does not currently expose the integration dependencies needed to prepare schemas and run the focused integration suite.
- Suggested next step: start the local PostgreSQL and aproxy integration stack, then rerun Task 10 from the red phase with the focused `TestSchemaMapping` coverage.

Next dependency:
- Task 10 (unblock and retry)

## Task 11

Status: completed
Owner: Copilot
Files changed:
- internal/config/config.go
- internal/config/config_test.go
- pkg/mapper/show_databases.go
- pkg/mapper/show_databases_test.go
- docs/plans/schema-mapping/execution/task-status.md

Tests run:
- go test ./internal/config ./pkg/mapper -run "Test(ShowDatabasesCandidates|DatabaseExposure)" -v

Result:
- PASS: database exposure config defaults and `SHOW DATABASES` candidate selection behave as designed.

Notes:
- Added `database_mapping.expose_mode` / `exposed_databases` config bridging.
- Implemented logical database candidate generation without changing runtime exposure semantics yet.

Next dependency:
- Task 12

## Task 12

Status: completed
Owner: Copilot
Files changed:
- pkg/mapper/show_databases.go
- pkg/mapper/show_databases_test.go
- pkg/mapper/show.go
- pkg/protocol/mysql/handler.go
- cmd/aproxy/main.go
- docs/plans/schema-mapping/execution/task-status.md

Tests run:
- go test ./pkg/mapper -run "TestShowDatabases" -v

Result:
- PASS: `SHOW DATABASES` now filters logical database names through schema-usage permissions and returns logical names only.

Notes:
- Wired runtime `SHOW DATABASES` behavior to exposure config plus schema mapping config.
- Preserved the boundary that the proxy does not enumerate arbitrary PostgreSQL schemas for this command.

Next dependency:
- Task 13

## Task 13

Status: blocked
Owner: Copilot
Files changed:
- docs/plans/schema-mapping/execution/task-status.md

Tests run:
- none

Result:
- BLOCKED: focused integration coverage for `SHOW DATABASES` cannot run because the local PostgreSQL/aproxy integration environment is unavailable.

Notes:
- Completed part: unit coverage and runtime wiring for logical exposure semantics already landed in Tasks 11 and 12.
- Blocker: this environment does not currently expose the PostgreSQL + aproxy stack needed for the focused integration scenario.
- Suggested next step: start the local integration stack, then add and run the focused integration coverage for logical exposure and permission filtering.

Next dependency:
- Task 13 (unblock and retry)

## Task 14

Status: completed
Owner: Copilot
Files changed:
- README.md
- docs/RUNBOOK.md
- docs/plans/schema-mapping/history/2026-03-07-design.md
- docs/plans/schema-mapping/execution/task-status.md

Tests run:
- none (documentation-only task; terminology was manually re-checked in the edited docs)

Result:
- PASS: public documentation now describes fixed PostgreSQL database semantics, logical database-to-schema mapping, strict `fallback_to_public` default, and logical `SHOW DATABASES` output.

Notes:
- Updated README and RUNBOOK to match the implemented behavior.
- Corrected stale `SHOW DATABASES` wording in the earlier 2026-03-07 design doc without doing a broader historical rewrite.

Next dependency:
- Task 15

## Task 15

Status: blocked
Owner: Copilot
Files changed:
- README.md
- docs/plans/schema-mapping/execution/task-status.md

Tests run:
- go test ./internal/config ./pkg/schema ./pkg/session ./pkg/protocol/mysql ./pkg/mapper
- go test ./pkg/... ./internal/...

Result:
- PASS: focused non-integration verification succeeded for the schema-mapping and `SHOW DATABASES` packages.
- BLOCKED: integration verification still cannot run locally because PostgreSQL/aproxy dependencies are unavailable.
- FAIL: broader regression check hit pre-existing unrelated failure `pkg/replication.TestGenerateUUID` (`Expected different UUIDs`).

Notes:
- Completed part: ran the non-integration verification required for the changed areas and fixed a README formatting issue found during final review.
- Blocker: Task 15 cannot be fully closed until the local integration stack is available and the unrelated replication regression is either fixed separately or accepted as baseline.
- Suggested next step: bring up the integration environment, rerun the integration verification, and handle `pkg/replication.TestGenerateUUID` outside this schema-mapping scope if broader green CI is required.

Next dependency:
- Task 15 (unblock and retry)
