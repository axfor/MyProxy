# SHOW DATABASES Exposure Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 把 `SHOW DATABASES` 定义为受控的“逻辑 database 列表”，而不是底层 PostgreSQL schema 枚举结果。

**Architecture:** 引入显式暴露候选集合和权限过滤逻辑，由配置和 PG 可访问性共同决定返回结果；`SHOW DATABASES` 最终只输出逻辑 database 名。

**Tech Stack:** Go, YAML config, PostgreSQL information schema queries, Go tests

---

### Task 1: Design config surface with tests

**Files:**
- Modify: `internal/config/config.go`
- Create or modify: `internal/config/config_test.go`

**Step 1: Write the failing test**

新增测试覆盖：

1. `expose_mode` 和 `exposed_databases` 能解析
2. 默认暴露策略符合设计

**Step 2: Run test to verify it fails**

Run: `go test ./internal/config`

Expected: FAIL

**Step 3: Write minimal implementation**

补配置结构和默认值。

**Step 4: Run test to verify it passes**

Run: `go test ./internal/config`

Expected: PASS

### Task 2: Lock logical database candidate generation

**Files:**
- Create or modify: `pkg/mapper/show_databases.go`
- Create or modify: `pkg/mapper/show_databases_test.go`

**Step 1: Write the failing test**

新增测试覆盖：

1. 从 `rules` 生成逻辑 database 候选集合
2. `exposed_databases` 覆盖默认候选集合
3. 结果去重并排序

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/mapper -run 'TestShowDatabasesCandidates'`

Expected: FAIL

**Step 3: Write minimal implementation**

实现候选集合构建函数。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/mapper -run 'TestShowDatabasesCandidates'`

Expected: PASS

### Task 3: Add permission filtering

**Files:**
- Modify: `pkg/mapper/show.go` or dedicated helper file
- Modify: related mapper tests

**Step 1: Write the failing test**

新增测试覆盖：

1. 无 `USAGE` 权限的 schema 被过滤
2. 有权限的 schema 保留

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/mapper -run 'TestFilterAccessibleSchemas'`

Expected: FAIL

**Step 3: Write minimal implementation**

实现权限过滤函数。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/mapper -run 'TestFilterAccessibleSchemas'`

Expected: PASS

### Task 4: Replace SHOW DATABASES behavior

**Files:**
- Modify: `pkg/mapper/show.go`
- Modify: `pkg/mapper/show_test.go`

**Step 1: Write the failing test**

新增测试覆盖：

1. `SHOW DATABASES` 返回逻辑 database 名
2. 不直接枚举所有非系统 schema

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/mapper -run 'TestShowDatabases'`

Expected: FAIL

**Step 3: Write minimal implementation**

替换 `showDatabases` 实现。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/mapper -run 'TestShowDatabases'`

Expected: PASS

### Task 5: Add focused integration coverage

**Files:**
- Create or modify: `test/integration/mysql_compat_test.go` or focused test file

**Step 1: Write the failing test**

覆盖：

1. 仅暴露配置允许的逻辑 database
2. 无权限 schema 不出现

**Step 2: Run test to verify it fails**

Run: `go test ./test/integration -run 'TestShowDatabasesExposure'`

Expected: FAIL

**Step 3: Write minimal implementation**

补足真实路径上的数据构造和查询行为。

**Step 4: Run test to verify it passes**

Run: `go test ./test/integration -run 'TestShowDatabasesExposure'`

Expected: PASS
