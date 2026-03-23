# Public Fallback Boundary Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 将 `public` fallback 从默认行为改为显式可选行为，恢复 MySQL 逻辑 database 的默认解析边界。

**Architecture:** 通过配置默认值和 `search_path` 生成逻辑控制 fallback 行为；默认严格模式只设置当前 schema，显式配置时才追加 `public`。

**Tech Stack:** Go, YAML config, pgx/v5, Go tests

---

### Task 1: Lock default config with tests

**Files:**
- Modify: `internal/config/config.go`
- Create or modify: `internal/config/config_test.go`

**Step 1: Write the failing test**

新增测试覆盖：

1. `fallback_to_public` 默认是 `false`
2. YAML 能正确解析 `true` / `false`

**Step 2: Run test to verify it fails**

Run: `go test ./internal/config`

Expected: FAIL

**Step 3: Write minimal implementation**

补配置默认值和解析结构。

**Step 4: Run test to verify it passes**

Run: `go test ./internal/config`

Expected: PASS

### Task 2: Lock search_path generation behavior

**Files:**
- Modify: `pkg/schema/mapping.go`
- Modify: `pkg/schema/mapping_test.go`

**Step 1: Write the failing test**

新增测试覆盖：

1. 关闭 fallback 时生成 `SET search_path TO <schema>`
2. 开启 fallback 时生成 `SET search_path TO <schema>, public`

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/schema -run 'TestBuildSearchPathSQL'`

Expected: FAIL

**Step 3: Write minimal implementation**

更新 SQL 生成逻辑。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/schema -run 'TestBuildSearchPathSQL'`

Expected: PASS

### Task 3: Add integration coverage for strict vs fallback mode

**Files:**
- Modify: `test/integration/mysql_compat_test.go` or create focused test file

**Step 1: Write the failing test**

构造两个 schema 场景：

1. `tenant_a` 缺失目标表，`public` 存在同名表
2. 严格模式查询失败
3. fallback 模式查询成功

**Step 2: Run test to verify it fails**

Run: `go test ./test/integration -run 'TestPublicFallbackBoundary'`

Expected: FAIL

**Step 3: Write minimal implementation**

只补足实现，不扩展到其他元数据行为。

**Step 4: Run test to verify it passes**

Run: `go test ./test/integration -run 'TestPublicFallbackBoundary'`

Expected: PASS

### Task 4: Add operator-facing warning

**Files:**
- Modify: logging or startup config validation path

**Step 1: Write the failing test**

新增测试验证：启用 `fallback_to_public` 时会输出一次明确警告或说明。

**Step 2: Run test to verify it fails**

Run: `go test ./...`

Expected: FAIL if logging behavior is covered; otherwise document and skip implementation if testability is poor.

**Step 3: Write minimal implementation**

在启动时打印一次低噪声警告。

**Step 4: Run test to verify it passes**

Run: `go test ./...`

Expected: PASS or documented skip with rationale.
