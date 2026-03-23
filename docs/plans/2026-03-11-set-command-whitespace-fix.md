# SET Command Whitespace Fix Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix the proxy's `SET` command parser so MySQL clients that send `SET NAMES utf8mb4` with leading whitespace or line breaks connect successfully on Windows, then verify the fix with the demo client.

**Architecture:** Keep the fix narrowly scoped to `pkg\mapper\show.go` where `SET` statements are parsed today. Normalize the incoming SQL once for parsing, preserve the original SQL for error reporting, and lock the behavior down with focused unit tests before running an end-to-end verification through `test\mysql-demo`.

**Tech Stack:** Go 1.25, `testing`, `stretchr/testify`, `database/sql`, `github.com/go-sql-driver/mysql`

---

## Problem Summary

`test\mysql-demo\mysql-demo.go` uses a DSN with `charset=utf8mb4`. The MySQL driver sends `SET NAMES utf8mb4` immediately after connect. The proxy intercepts this in `pkg\protocol\mysql\handler.go` and parses it in `pkg\mapper\show.go`.

The current parser checks `strings.TrimSpace(sql)` to decide whether the statement starts with `SET`, but later strips the prefix from the original untrimmed SQL:

```go
upperSQL := strings.ToUpper(strings.TrimSpace(sql))
assignment := strings.TrimPrefix(sql, "SET ")
assignment = strings.TrimPrefix(assignment, "set ")
```

That means inputs like `" SET NAMES utf8mb4"`, `"\r\nSET NAMES utf8mb4"`, or `"\tSET NAMES utf8mb4"` are recognized as `SET` commands and then rejected as `invalid SET syntax`.

## Constraints and Guardrails

- Do not modify unrelated files in the dirty worktree.
- Keep the behavioral fix local to `SET` parsing; do not change PostgreSQL connection behavior.
- Preserve the current error style by returning the original SQL in failure messages.
- Use TDD: add/expand failing tests first, then implement the minimal parser change.
- Treat `test\mysql-demo\mysql-demo.go` as the final smoke test, not the first signal.

## Preconditions for Verification

- The proxy must already be running on `localhost:3306`.
- The credentials in `test\mysql-demo\config.json` must remain valid.
- PostgreSQL must already be reachable by the proxy's configured backend.

### Task 1: Lock in the parser failure with focused tests

**Files:**
- Modify: `pkg\mapper\show_test.go:11-30`
- Reference: `pkg\mapper\show.go:459-506`

**Step 1: Write the failing tests**

Extend `pkg\mapper\show_test.go` with table-driven coverage for whitespace and collation cases. Keep the existing success cases, but add inputs that currently fail:

```go
func TestShowEmulatorHandleSetCommand_SetNamesWhitespaceVariants(t *testing.T) {
	se := NewShowEmulator()

	testCases := []struct {
		name              string
		sql               string
		expectedCharset   string
		expectedCollation string
	}{
		{
			name:            "leading space",
			sql:             " SET NAMES utf8mb4",
			expectedCharset: "utf8mb4",
		},
		{
			name:            "leading crlf",
			sql:             "\r\nSET NAMES utf8mb4",
			expectedCharset: "utf8mb4",
		},
		{
			name:            "leading tab",
			sql:             "\tSET NAMES utf8mb4",
			expectedCharset: "utf8mb4",
		},
		{
			name:              "with collate",
			sql:               "  SET NAMES utf8mb4 COLLATE utf8mb4_general_ci",
			expectedCharset:   "utf8mb4",
			expectedCollation: "utf8mb4_general_ci",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sessionVars := make(map[string]interface{})

			err := se.HandleSetCommand(context.Background(), tc.sql, sessionVars)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedCharset, sessionVars["names"])
			assert.Equal(t, tc.expectedCharset, sessionVars["character_set_client"])
			assert.Equal(t, tc.expectedCharset, sessionVars["character_set_connection"])
			assert.Equal(t, tc.expectedCharset, sessionVars["character_set_results"])

			if tc.expectedCollation != "" {
				assert.Equal(t, tc.expectedCollation, sessionVars["collation_connection"])
			}
		})
	}
}
```

Also keep the current assignment-style test for `SET autocommit = 1` to guard against regressions.

**Step 2: Run the targeted tests to verify they fail**

Run:

```powershell
go test .\pkg\mapper -run TestShowEmulatorHandleSetCommand -v
```

Expected before the fix:

- New whitespace cases fail.
- Failure text contains `invalid SET syntax`.

**Step 3: Commit the red test checkpoint**

```powershell
git add pkg\mapper\show_test.go
git commit -m "test: capture SET NAMES whitespace parsing regression"
```

### Task 2: Normalize `SET` parsing without changing error semantics

**Files:**
- Modify: `pkg\mapper\show.go:459-506`
- Test: `pkg\mapper\show_test.go:11-30`

**Step 1: Write the minimal implementation**

Normalize once at the start of `HandleSetCommand` and parse from the normalized string instead of the raw input:

```go
func (se *ShowEmulator) HandleSetCommand(ctx context.Context, sql string, sessionVars map[string]interface{}) error {
	normalizedSQL := strings.TrimSpace(sql)
	upperSQL := strings.ToUpper(normalizedSQL)

	if !strings.HasPrefix(upperSQL, "SET ") {
		return fmt.Errorf("not a SET command: %s", sql)
	}

	assignment := strings.TrimSpace(normalizedSQL[len("SET "):])
	fields := strings.Fields(assignment)

	if len(fields) >= 2 && strings.EqualFold(fields[0], "NAMES") {
		charset := strings.Trim(fields[1], "'\"")
		sessionVars["names"] = charset
		sessionVars["character_set_client"] = charset
		sessionVars["character_set_connection"] = charset
		sessionVars["character_set_results"] = charset

		if len(fields) >= 4 && strings.EqualFold(fields[2], "COLLATE") {
			sessionVars["collation_connection"] = strings.Trim(fields[3], "'\"")
		}

		return nil
	}

	parts := strings.SplitN(assignment, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid SET syntax: %s", sql)
	}

	// Keep the remaining assignment parsing unchanged.
}
```

Implementation notes:

- Use the normalized string only for parsing.
- Keep `sql` in all returned errors so logs still show the original payload exactly as received.
- Do not touch `pkg\protocol\mysql\handler.go` unless the parser change proves insufficient.

**Step 2: Run the targeted tests to verify they pass**

Run:

```powershell
go test .\pkg\mapper -run TestShowEmulatorHandleSetCommand -v
```

Expected after the fix:

- All `HandleSetCommand` tests pass.
- `SET NAMES ... COLLATE ...` populates `collation_connection`.
- `SET autocommit = 1` still passes.

**Step 3: Run a broader mapper package regression**

Run:

```powershell
go test .\pkg\mapper\... -v
```

Expected:

- Package tests pass without introducing unrelated regressions.

**Step 4: Commit the parser fix**

```powershell
git add pkg\mapper\show.go pkg\mapper\show_test.go
git commit -m "fix: normalize SET command parsing"
```

### Task 3: Verify the fix through the Windows demo client

**Files:**
- Verify: `test\mysql-demo\mysql-demo.go`
- Verify config: `test\mysql-demo\config.json`
- Reference: `pkg\protocol\mysql\handler.go:1006-1041`

**Step 1: Re-run the demo client against the running proxy**

Run:

```powershell
Set-Location F:\go\src\github.com\aproxy\test\mysql-demo
go run .
```

Expected after the fix:

- Output includes `✅ 成功连接到 MySQL 数据库!`
- Output continues through table creation and CRUD operations
- Final line includes `🎉 所有 CRUD 操作执行完毕!`

**Step 2: If the demo still fails, capture the exact query shape**

If the run still errors, enable or inspect the proxy's SQL debug output and compare the exact incoming SQL against the normalized parser expectations. Only then consider a follow-up change in `pkg\protocol\mysql\handler.go`.

**Step 3: Run one final focused regression**

Run:

```powershell
go test .\pkg\mapper -run TestShowEmulatorHandleSetCommand -v
```

Expected:

- Tests still pass after the demo run.

**Step 4: Commit the verification checkpoint**

```powershell
git add pkg\mapper\show.go pkg\mapper\show_test.go
git commit -m "test: verify SET parsing with mysql demo"
```

## Notes for the Implementer

- Prefer a tiny, readable normalization change over introducing a new parser package.
- Resist the urge to “improve” unrelated `SHOW` or `USE` handling in the same change.
- The worktree is already dirty; inspect diffs carefully and stage only the touched files.
- If `test\mysql-demo` depends on an external running proxy, treat connection availability issues as environment problems, not code regressions.
