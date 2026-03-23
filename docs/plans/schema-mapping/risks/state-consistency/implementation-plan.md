# Schema State Consistency Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 修正 schema 切换顺序，保证 PostgreSQL `search_path` 成功切换后才更新 session 内存状态。

**Architecture:** 抽出统一的 schema 切换入口，把 resolve、apply、commit 三步封装起来；handler 只调用该入口，不再直接操作 `CurrentSchema`。失败路径保持旧状态，无补偿回滚。

**Tech Stack:** Go, pgx/v5, Go unit tests

---

### Task 1: Lock in failure behavior with tests

**Files:**
- Modify: `pkg/protocol/mysql/handler_test.go`
- Possibly create: `pkg/session/session_test.go`

**Step 1: Write the failing test**

新增测试覆盖：

1. `ApplySchema` 失败时 `CurrentSchema` 不变
2. `ResolveSchema` 失败时 `CurrentSchema` 不变
3. 成功时才更新 `CurrentSchema`

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/protocol/mysql ./pkg/session`

Expected: FAIL

**Step 3: Write minimal implementation**

为测试准备最小替换点或 helper，方便注入 schema apply 失败。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/protocol/mysql ./pkg/session`

Expected: PASS

### Task 2: Introduce unified schema switch helper

**Files:**
- Modify: `pkg/schema/mapping.go` or create dedicated helper
- Modify: `pkg/protocol/mysql/handler.go`

**Step 1: Write the failing test**

为统一 helper 增加测试：

1. 成功时按 apply-before-commit 顺序执行
2. apply 失败时不写 session

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/schema ./pkg/protocol/mysql -run 'TestSwitchSchema'`

Expected: FAIL

**Step 3: Write minimal implementation**

实现 `SwitchSchema` 或等价 helper。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/schema ./pkg/protocol/mysql -run 'TestSwitchSchema'`

Expected: PASS

### Task 3: Replace handler call sites

**Files:**
- Modify: `pkg/protocol/mysql/handler.go`

**Step 1: Write the failing test**

补测试覆盖：

1. `UseDB` 走统一入口
2. 初始 database 应用走统一入口

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/protocol/mysql -run 'TestUseDB|TestInitialSchema'`

Expected: FAIL

**Step 3: Write minimal implementation**

移除直接更新 schema 状态的路径，全部改用 helper。

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/protocol/mysql`

Expected: PASS

### Task 4: Run focused verification

**Files:**
- No file changes

**Step 1: Run focused tests**

Run: `go test ./pkg/schema ./pkg/session ./pkg/protocol/mysql`

Expected: PASS

**Step 2: Confirm scope**

确认未顺手引入：

1. `SHOW DATABASES` 语义改造
2. `public` fallback 默认值改造
3. `pooled` / `hybrid` 连接重放逻辑
