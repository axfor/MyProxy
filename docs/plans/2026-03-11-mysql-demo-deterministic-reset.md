# MySQL Demo Deterministic Reset Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make `test\mysql-demo\mysql-demo.go` start from an empty `users` dataset on each run so repeated executions do not accumulate prior demo rows.

**Architecture:** Keep the proxy and PostgreSQL mapping behavior unchanged. Limit the fix to the demo program by clearing `users` after ensuring the table exists, then verify with the demo itself and a direct PostgreSQL query.

**Tech Stack:** Go 1.25, `database/sql`, `github.com/go-sql-driver/mysql`, `github.com/jackc/pgx/v5`

---

### Task 1: Capture the existing accumulation behavior

**Files:**
- Reference: `test\mysql-demo\mysql-demo.go:63-78`
- Reference: `test\mysql-demo\mysql-demo.go:159-227`

**Step 1: Confirm the current setup behavior**

Inspect the existing table setup code and note that it only runs:

```go
CREATE TABLE IF NOT EXISTS users (...)
```

There is no cleanup step before inserts.

**Step 2: Confirm the observed leftover rows**

Run a direct PostgreSQL query against `test.users` and verify that prior runs left multiple rows behind.

Run:

```powershell
go run <temp pgx query helper>   # query: select id, name, email, age from test.users order by id
```

Expected:

- Existing rows remain from earlier runs
- The pattern matches prior complete demo executions rather than an UPDATE-without-WHERE bug

### Task 2: Clear existing demo data before CRUD operations

**Files:**
- Modify: `test\mysql-demo\mysql-demo.go:63-78`

**Step 1: Write the minimal implementation**

After ensuring the table exists, clear existing rows before the demo inserts new ones:

```go
func createTable(db *sql.DB) error {
	createQuery := `
	CREATE TABLE IF NOT EXISTS users (
		id    INT AUTO_INCREMENT PRIMARY KEY,
		name  VARCHAR(100) NOT NULL,
		email VARCHAR(100) NOT NULL,
		age   INT NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`

	if _, err := db.Exec(createQuery); err != nil {
		return fmt.Errorf("创建表失败: %w", err)
	}

	if _, err := db.Exec("DELETE FROM users"); err != nil {
		return fmt.Errorf("清空 users 表失败: %w", err)
	}

	fmt.Println("✅ users 表已就绪，旧数据已清空")
	return nil
}
```

**Step 2: Keep the rest of the demo unchanged**

Do not change:

- insert SQL
- update SQL
- delete SQL
- connection DSN logic

### Task 3: Verify deterministic demo behavior end-to-end

**Files:**
- Verify: `test\mysql-demo\mysql-demo.go`

**Step 1: Run the demo**

Run:

```powershell
Set-Location F:\go\src\github.com\aproxy\test\mysql-demo
go run .
```

Expected:

- Connection succeeds
- CRUD flow completes
- Output still shows one updated row at the end

**Step 2: Query PostgreSQL directly**

Run a direct query on `test.users`.

Expected:

- Only the current run's remaining row is present
- No older leftover rows remain

**Step 3: Run the demo again**

Run the same demo a second time.

Expected:

- It still succeeds
- PostgreSQL still shows only one remaining row after the second run

**Step 4: Final verification**

Run:

```powershell
git --no-pager diff -- test\mysql-demo\mysql-demo.go
```

Expected:

- Only the demo setup behavior changed
