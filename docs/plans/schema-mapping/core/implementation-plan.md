# Session Affinity Schema Mapping Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 在 `session_affinity` 模式下，为 MySQL database 到 PostgreSQL schema 的映射补齐统一解析、统一应用、模式约束和失败保护。

**Architecture:** 通过新增独立的 schema mapping 逻辑，统一处理 schema 解析与 `search_path` 应用；handler 只负责调用统一入口，不再直接拼接 SQL。会话状态在 `ApplySchema` 成功后再更新，从而避免代理状态与 PostgreSQL 连接状态分裂。

**Tech Stack:** Go, pgx/v5, go-mysql-org/go-mysql, YAML config, Go unit tests

---

### Task 1: Add database mapping config

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config.go`

**Step 1: Write the failing test**

在 `config.go` 相邻测试文件中新增配置默认值与反序列化测试，覆盖：

1. `DefaultConfig()` 包含 `database_mapping.default_schema=public`
2. `fallback_to_public=false`
3. `rules` 可被 YAML 正常解析

**Step 2: Run test to verify it fails**

Run: `go test ./internal/config`

Expected: FAIL，因为 `DatabaseMappingConfig` 尚不存在。

**Step 3: Write minimal implementation**

在 `internal/config/config.go` 中：

1. 新增 `DatabaseMappingConfig`
2. 在顶层 `Config` 加入 `DatabaseMapping`
3. 在 `DefaultConfig()` 中补默认值

**Step 4: Run test to verify it passes**

Run: `go test ./internal/config`

Expected: PASS

### Task 2: Add schema resolver tests

**Files:**
- Create: `pkg/schema/mapping.go`
- Create: `pkg/schema/mapping_test.go`

**Step 1: Write the failing test**

在 `pkg/schema/mapping_test.go` 中新增测试：

1. `ResolveSchema` 命中 `rules`
2. `ResolveSchema` 未命中时返回原始 db 名
3. 空 db 名返回 `default_schema`
4. 非法名称返回错误

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/schema -run 'TestResolveSchema|TestValidateSchemaName'`

Expected: FAIL，因为映射实现尚不存在。

**Step 3: Write minimal implementation**

在 `pkg/schema/mapping.go` 中实现：

1. `MappingConfig`
2. `Resolver`
3. `ResolveSchema`
4. 内部合法性校验函数

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/schema -run 'TestResolveSchema|TestValidateSchemaName'`

Expected: PASS

### Task 3: Add search_path application tests

**Files:**
- Modify: `pkg/schema/mapping.go`
- Modify: `pkg/schema/mapping_test.go`

**Step 1: Write the failing test**

为 `ApplySchema` 补测试，至少验证：

1. 默认生成 `SET search_path TO <schema>`
2. `fallback_to_public=true` 时生成 `SET search_path TO <schema>, public`
3. schema 名会经过受控转义，不接受非法输入

如直接 mock `pgx.Conn` 不方便，可先抽出生成 SQL 的辅助函数并对该函数做测试。

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/schema -run 'TestBuildSearchPathSQL|TestApplySchema'`

Expected: FAIL

**Step 3: Write minimal implementation**

在 `pkg/schema/mapping.go` 中补：

1. `BuildSearchPathSQL(schema string) string`
2. `ApplySchema(ctx, conn, schema)`

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/schema -run 'TestBuildSearchPathSQL|TestApplySchema'`

Expected: PASS

### Task 4: Extend session model safely

**Files:**
- Modify: `pkg/session/session.go`

**Step 1: Write the failing test**

为 session 新增测试，验证：

1. `NewSession` 初始化后可持有 `BackendDatabase`
2. schema 切换成功后可更新 `CurrentSchema`
3. 兼容字段仍可保留同步

如果当前包无测试文件，则新增 `pkg/session/session_test.go`。

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/session`

Expected: FAIL

**Step 3: Write minimal implementation**

在 `pkg/session/session.go` 中：

1. 为 `Session` 增加 `BackendDatabase`、`CurrentSchema`
2. 根据当前构造方式调整 `NewSession`
3. 如有必要，增加一个小的 setter 来统一更新 schema 相关字段

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/session`

Expected: PASS

### Task 5: Add handler behavior tests first

**Files:**
- Modify: `pkg/protocol/mysql/handler_test.go`

**Step 1: Write the failing test**

新增 handler 行为测试，覆盖：

1. `session_affinity` 下 `UseDB` 成功应用 schema
2. `pooled` 下 `UseDB` 返回明确错误
3. `hybrid` 下 `UseDB` 返回明确错误
4. 事务中 `UseDB` 返回明确错误
5. `ApplySchema` 失败时不会更新 `CurrentSchema`

必要时通过小接口或可替换函数注入 schema resolver，避免真实连库。

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/protocol/mysql -run 'TestUseDB|TestHandleUseCommand'`

Expected: FAIL

**Step 3: Write minimal implementation**

先不要改全部查询逻辑，只把支撑测试所需的接口和占位依赖准备好。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/protocol/mysql -run 'TestUseDB|TestHandleUseCommand'`

Expected: PASS

### Task 6: Refactor handler to use schema mapping

**Files:**
- Modify: `pkg/protocol/mysql/handler.go`

**Step 1: Write the failing test**

如果 Task 5 覆盖不完整，再补一个失败测试，验证：

1. 首次获取 PG 连接后会应用初始 schema
2. 非 `session_affinity` 模式下若客户端指定初始 database，返回明确错误

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/protocol/mysql -run 'TestInitialSchema'`

Expected: FAIL

**Step 3: Write minimal implementation**

在 `handler.go` 中完成：

1. 注入 schema resolver / applier
2. 修改 `UseDB` 逻辑为统一入口
3. 修正状态更新顺序
4. 在首次建连时应用初始 schema

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/protocol/mysql`

Expected: PASS

### Task 7: Add focused integration coverage

**Files:**
- Modify: `test/integration/mysql_compat_test.go` or create a focused integration test file

**Step 1: Write the failing test**

新增集成测试，覆盖：

1. 带初始 database 建连后，未限定 schema 的查询进入目标 schema
2. `USE db` 后查询进入新 schema
3. `fallback_to_public=false` 时，不会误查到 `public`

**Step 2: Run test to verify it fails**

Run: `go test ./test/integration -run 'TestSchemaMapping'`

Expected: FAIL

**Step 3: Write minimal implementation**

只补足测试所需最小代码，不顺手扩展到 `SHOW DATABASES` 或 `pooled` / `hybrid` 语义。

**Step 4: Run test to verify it passes**

Run: `go test ./test/integration -run 'TestSchemaMapping'`

Expected: PASS

### Task 8: Run verification suite

**Files:**
- No file changes

**Step 1: Run focused package tests**

Run:

```bash
go test ./internal/config ./pkg/schema ./pkg/session ./pkg/protocol/mysql
```

Expected: PASS

**Step 2: Run focused integration tests**

Run:

```bash
go test ./test/integration -run 'TestSchemaMapping|TestMySQLCompatibility'
```

Expected: PASS for the targeted coverage that does not rely on unsupported connection modes.

**Step 3: Review for scope control**

确认未引入以下超范围变更：

1. `pooled` / `hybrid` schema 重放
2. `SHOW DATABASES` 大规模语义重构
3. 自动建 schema
