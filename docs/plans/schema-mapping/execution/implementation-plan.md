# Schema Mapping Execution Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 按推荐顺序实现 MySQL database 到 PostgreSQL schema 映射主链路，并在同一执行序列中吸收风险 2、风险 3 的主链路修正，最后单独落地风险 4 的 `SHOW DATABASES` 暴露策略。

**Architecture:** 以 `session_affinity` 方案为主执行入口，先落地配置模型、schema 解析与 `search_path` 应用、session 状态拆分、handler 切换流程，再补严格的状态一致性与 `public` fallback 默认边界，最后单独实现 `SHOW DATABASES` 的逻辑 database 暴露语义。主链路以 `../core/design.md` 为准，风险专题文档只作为补充约束，不作为抢先执行入口。

**Tech Stack:** Go, pgx/v5, go-mysql-org/go-mysql, YAML config, Go unit tests, focused integration tests

---

## Source Documents

实现时按以下优先级理解设计：

1. 主执行入口：`../core/design.md`
2. 主执行计划：`../core/implementation-plan.md`
3. 补充约束：
   - `../risks/state-consistency/design.md`
   - `../risks/public-fallback-boundary/design.md`
   - `../risks/show-databases-exposure/design.md`
4. 历史背景，仅作对照：
   - `../history/2026-03-07-design.md`
   - `../history/2026-03-07-implementation-plan.md`

## Execution Strategy

不要按“每个 risk 各开一轮完整实现”机械推进。正确顺序是：

1. 先完成主链路：risk 1 为主，吸收 risk 2、3 在主链路上的必需改动。
2. 再单独实现 risk 4：`SHOW DATABASES` 暴露策略。
3. 最后做一次文档和术语收尾，确认主设计、风险文档、代码行为一致。

---

### Task 1: Lock database mapping config with tests

**Files:**
- Modify: `internal/config/config.go`
- Create or modify: `internal/config/config_test.go`
- Modify: `configs/config.yaml`
- Modify: `configs/config-binlog.yaml`

**Step 1: Write the failing test**

在 `internal/config/config_test.go` 中新增测试，覆盖：

1. `DefaultConfig()` 含有 `DatabaseMapping.DefaultSchema == "public"`
2. `DatabaseMapping.FallbackToPublic == false`
3. `Rules` 能从 YAML 解析
4. 如保留 `mode` 字段，则验证仅支持 `schema`

**Step 2: Run test to verify it fails**

Run: `go test ./internal/config -run "Test(DefaultConfig|LoadConfig).*DatabaseMapping" -v`

Expected: FAIL，因为 `DatabaseMapping` 尚未完整存在。

**Step 3: Write minimal implementation**

在 `internal/config/config.go` 中：

1. 新增 `DatabaseMappingConfig`
2. 在顶层 `Config` 加入 `DatabaseMapping`
3. 设定默认值：`default_schema=public`、`fallback_to_public=false`
4. 只做最小必要校验

在两个配置样例文件中加入显式 `database_mapping` 配置块。

**Step 4: Run test to verify it passes**

Run: `go test ./internal/config -run "Test(DefaultConfig|LoadConfig).*DatabaseMapping" -v`

Expected: PASS

### Task 2: Introduce pure schema mapping helper

**Files:**
- Create: `pkg/schema/mapping.go`
- Create: `pkg/schema/mapping_test.go`

**Step 1: Write the failing test**

在 `pkg/schema/mapping_test.go` 中新增测试，覆盖：

1. `ResolveSchema` 命中 `rules`
2. `ResolveSchema` 未命中时返回原始 db 名
3. 空 db 名回退到 `default_schema`
4. 非法 schema 名被拒绝
5. `BuildSearchPathSQL` 在 strict 模式不追加 `public`
6. `BuildSearchPathSQL` 在 fallback 模式追加 `public`

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/schema -run "Test(ResolveSchema|ValidateSchemaName|BuildSearchPathSQL)" -v`

Expected: FAIL

**Step 3: Write minimal implementation**

在 `pkg/schema/mapping.go` 中实现：

1. `MappingConfig`
2. `Resolver`
3. `ResolveSchema`
4. `ValidateSchemaName`
5. `BuildSearchPathSQL`

采用严格白名单校验，默认不支持复杂 PostgreSQL identifier 语法。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/schema -run "Test(ResolveSchema|ValidateSchemaName|BuildSearchPathSQL)" -v`

Expected: PASS

### Task 3: Add ApplySchema behavior and isolate SQL generation

**Files:**
- Modify: `pkg/schema/mapping.go`
- Modify: `pkg/schema/mapping_test.go`

**Step 1: Write the failing test**

新增测试验证：

1. `ApplySchema` 调用时使用 `BuildSearchPathSQL`
2. 非法 schema 名不会进入执行层
3. 关闭 fallback 时 SQL 只含目标 schema

如果直接 mock `pgx.Conn` 成本高，则先抽出一个小接口或通过测试先锁定 SQL 生成功能。

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/schema -run "Test(ApplySchema|BuildSearchPathSQL)" -v`

Expected: FAIL

**Step 3: Write minimal implementation**

在 `pkg/schema/mapping.go` 中补 `ApplySchema(ctx, conn, schema)`，保证 handler 不再自行拼接 `SET search_path`。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/schema -run "Test(ApplySchema|BuildSearchPathSQL)" -v`

Expected: PASS

### Task 4: Split session schema state safely

**Files:**
- Modify: `pkg/session/session.go`
- Create or modify: `pkg/session/session_test.go`

**Step 1: Write the failing test**

新增测试覆盖：

1. `NewSession` 初始化 `CurrentSchema`
2. `BackendDatabase` 可被设置并保留
3. 如保留兼容字段 `Database`，验证同步更新逻辑

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/session -run "Test(NewSession|SetCurrentSchema|SchemaState)" -v`

Expected: FAIL

**Step 3: Write minimal implementation**

在 `pkg/session/session.go` 中：

1. 添加 `BackendDatabase`
2. 添加 `CurrentSchema`
3. 提供最小 setter，保证 schema 更新为单一路径

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/session -run "Test(NewSession|SetCurrentSchema|SchemaState)" -v`

Expected: PASS

### Task 5: Lock handler mode gating and transaction guard

**Files:**
- Modify: `pkg/protocol/mysql/handler_test.go`
- Modify: `pkg/protocol/mysql/handler.go`

**Step 1: Write the failing test**

新增测试覆盖：

1. `session_affinity` 下允许 `UseDB`
2. `pooled` 下拒绝 `UseDB`
3. `hybrid` 下拒绝 `UseDB`
4. 事务中拒绝 `UseDB`

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/protocol/mysql -run "TestUseDB" -v`

Expected: FAIL

**Step 3: Write minimal implementation**

在 `handler.go` 中加入：

1. 连接模式检查
2. 事务状态检查
3. 明确错误信息

此时先不要做全部首次建连应用逻辑，只修正 `UseDB` 行为门禁。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/protocol/mysql -run "TestUseDB" -v`

Expected: PASS

### Task 6: Implement apply-before-commit state transition

**Files:**
- Modify: `pkg/protocol/mysql/handler_test.go`
- Modify: `pkg/protocol/mysql/handler.go`

**Step 1: Write the failing test**

新增测试覆盖：

1. `ApplySchema` 成功后才更新 `CurrentSchema`
2. `ApplySchema` 失败时 `CurrentSchema` 保持旧值
3. `ApplySchema` 失败时兼容字段也不更新

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/protocol/mysql -run "Test(UseDB|SwitchSchema).*State" -v`

Expected: FAIL

**Step 3: Write minimal implementation**

抽出统一切换 helper，固定顺序：

1. `ResolveSchema`
2. `ApplySchema`
3. `Update session state`

不要在失败路径上做“先写后回滚”。直接保持旧状态。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/protocol/mysql -run "Test(UseDB|SwitchSchema).*State" -v`

Expected: PASS

### Task 7: Apply initial schema on first backend connection

**Files:**
- Modify: `pkg/protocol/mysql/handler_test.go`
- Modify: `pkg/protocol/mysql/handler.go`

**Step 1: Write the failing test**

新增测试覆盖：

1. 初始 database 在首次获取 PG 连接后被应用
2. 非 `session_affinity` 模式下带初始 database 时直接报错
3. 首次应用失败时不产生伪成功 session 状态

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/protocol/mysql -run "TestInitialSchema" -v`

Expected: FAIL

**Step 3: Write minimal implementation**

在首次 `AcquireForSession` 后调用统一 schema 切换入口，保证 DSN 初始 database 与 `USE db` 语义一致。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/protocol/mysql -run "TestInitialSchema" -v`

Expected: PASS

### Task 8: Align DDL invalidation and metadata with CurrentSchema

**Files:**
- Modify: `pkg/protocol/mysql/handler.go`
- Modify: `pkg/schema/cache.go`
- Create or modify: `pkg/schema/cache_test.go`

**Step 1: Write the failing test**

新增测试覆盖：

1. DDL cache invalidation 使用当前 schema
2. cache key 语义明确为 `schema.table`

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/schema -run "Test.*Cache.*Schema" -v`

Expected: FAIL

**Step 3: Write minimal implementation**

把 handler 中涉及 schema cache 的调用改成 `CurrentSchema` 语义，必要时补内部 helper。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/schema -run "Test.*Cache.*Schema" -v`

Expected: PASS

### Task 9: Finish public fallback boundary on the main path

**Files:**
- Modify: `internal/config/config.go`
- Modify: `pkg/schema/mapping.go`
- Modify: `pkg/schema/mapping_test.go`
- Optionally modify: startup logging path

**Step 1: Write the failing test**

新增测试覆盖：

1. 默认 strict 模式不追加 `public`
2. 显式开启时才追加 `public`
3. 如实现启动提示，验证 fallback 开启时会输出一次说明

**Step 2: Run test to verify it fails**

Run: `go test ./internal/config ./pkg/schema -run "Test.*Fallback.*Public" -v`

Expected: FAIL

**Step 3: Write minimal implementation**

完成 risk 3 在主链路中的剩余部分，不扩展到其他元数据行为。

**Step 4: Run test to verify it passes**

Run: `go test ./internal/config ./pkg/schema -run "Test.*Fallback.*Public" -v`

Expected: PASS

### Task 10: Add focused integration coverage for the main path

**Files:**
- Create or modify: `test/integration/mysql_compat_test.go`
- Possibly modify: `test/integration/basic_test.go`

**Step 1: Write the failing test**

新增集成测试覆盖：

1. DSN 初始 database 生效
2. `USE db` 后未限定 schema 的查询命中目标 schema
3. strict 模式下不会意外命中 `public`

**Step 2: Run test to verify it fails**

Run: `go test ./test/integration -run "TestSchemaMapping" -v`

Expected: FAIL

**Step 3: Write minimal implementation**

只补测试所需最小实现，不提前做 `SHOW DATABASES` 重构。

**Step 4: Run test to verify it passes**

Run: `go test ./test/integration -run "TestSchemaMapping" -v`

Expected: PASS

### Task 11: Implement SHOW DATABASES candidate model

**Files:**
- Modify: `internal/config/config.go`
- Create or modify: `pkg/mapper/show_databases.go`
- Create or modify: `pkg/mapper/show_databases_test.go`

**Step 1: Write the failing test**

新增测试覆盖：

1. 从 `rules` 生成逻辑 database 候选集合
2. `exposed_databases` 可覆盖默认候选集合
3. 结果去重并排序

**Step 2: Run test to verify it fails**

Run: `go test ./internal/config ./pkg/mapper -run "Test(ShowDatabasesCandidates|DatabaseExposure)" -v`

Expected: FAIL

**Step 3: Write minimal implementation**

实现 risk 4 所需最小配置面与候选集合构建逻辑。

**Step 4: Run test to verify it passes**

Run: `go test ./internal/config ./pkg/mapper -run "Test(ShowDatabasesCandidates|DatabaseExposure)" -v`

Expected: PASS

### Task 12: Implement SHOW DATABASES permission filtering and response

**Files:**
- Modify: `pkg/mapper/show.go`
- Modify: `pkg/mapper/show_test.go`
- Modify: `pkg/mapper/show_databases.go`

**Step 1: Write the failing test**

新增测试覆盖：

1. 只返回逻辑 database 名
2. 无 `USAGE` 权限的 schema 被过滤
3. 不再直接枚举所有非系统 schema

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/mapper -run "TestShowDatabases" -v`

Expected: FAIL

**Step 3: Write minimal implementation**

替换 `SHOW DATABASES` 实现，让它基于受控候选集合和权限过滤返回逻辑 database 名。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/mapper -run "TestShowDatabases" -v`

Expected: PASS

### Task 13: Add focused integration coverage for SHOW DATABASES exposure

**Files:**
- Create or modify: `test/integration/mysql_compat_test.go` or focused test file

**Step 1: Write the failing test**

新增集成测试覆盖：

1. 仅暴露配置允许的逻辑 database
2. 无权限 schema 不出现在结果中

**Step 2: Run test to verify it fails**

Run: `go test ./test/integration -run "TestShowDatabasesExposure" -v`

Expected: FAIL

**Step 3: Write minimal implementation**

只补足真实路径所需代码，不顺手扩展其他 `SHOW` 语义。

**Step 4: Run test to verify it passes**

Run: `go test ./test/integration -run "TestShowDatabasesExposure" -v`

Expected: PASS

### Task 14: Update documentation and terminology

**Files:**
- Modify: `README.md`
- Modify: `docs/RUNBOOK.md`
- Modify if needed: `docs/plans/schema-mapping/history/2026-03-07-design.md`

**Step 1: Write the failing doc checklist**

手动确认以下信息尚未全部清晰表达：

1. PostgreSQL physical database 固定
2. MySQL database 映射到 PostgreSQL schema
3. `SHOW DATABASES` 返回逻辑 database 列表
4. `public` fallback 不是默认边界

**Step 2: Run manual verification to confirm the gap**

Run: `rg -n "search_path|SHOW DATABASES|schema|fixed PostgreSQL database|fallback_to_public" README.md docs/RUNBOOK.md docs/plans`

Expected: 能看到旧表述仍有歧义或分散。

**Step 3: Write minimal documentation updates**

把用户可见语义更新到 README / RUNBOOK，必要时回写主设计文档中的过时默认值或执行顺序。

**Step 4: Run manual verification to confirm clarity**

Run: `rg -n "maps to PostgreSQL schema|logical database|fallback_to_public|session_affinity" README.md docs/RUNBOOK.md docs/plans`

Expected: 关键语义都能被检索到。

### Task 15: Run final verification suite

**Files:**
- No file changes

**Step 1: Run focused package tests**

Run:

```bash
go test ./internal/config ./pkg/schema ./pkg/session ./pkg/protocol/mysql ./pkg/mapper
```

Expected: PASS

**Step 2: Run focused integration tests**

Run:

```bash
go test ./test/integration -run "TestSchemaMapping|TestShowDatabasesExposure|TestMySQLCompatibility"
```

Expected: PASS

**Step 3: Run broader regression check**

Run:

```bash
go test ./pkg/... ./internal/...
```

Expected: PASS

**Step 4: Review scope control**

确认未超出以下边界：

1. 没有实现 `pooled` / `hybrid` schema 重放
2. 没有引入 schema 自动创建，除非设计被重新批准
3. 没有把风险 4 的逻辑扩大到所有 `SHOW` 命令

## Notes for the Implementer

1. 第一执行入口始终是 `docs/plans/schema-mapping/core/implementation-plan.md`，不要先独立执行 risk 2 或 risk 3 计划。
2. risk 2 和 risk 3 的文档用于校验主链路是否真的补上了状态一致性和 fallback 边界，不是优先于主链路的独立入口。
3. risk 4 是真正适合后置独立实现的专题。
4. 每一轮改动结束前，都要先跑该任务自己的 focused tests，再进入下一个任务。
5. 任何时候如果发现旧设计文档与新专题文档冲突，以 2026-03-09 目录下的专题文档为准，并把冲突同步回写到旧主设计文档。
