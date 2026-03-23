# MySQL Demo Deterministic Reset Design

## Problem

`test\mysql-demo\mysql-demo.go` creates `users` if it does not exist, but it does not clear prior rows before running the CRUD sequence. As a result, repeated executions accumulate leftover rows from earlier runs, which makes the final PostgreSQL state look like `UPDATE` changed multiple rows even though the demo only updates the current run's inserted row.

## Considered Approaches

### 1. Delete existing rows and keep the table structure

Run `DELETE FROM users` after ensuring the table exists.

Pros:
- Matches the user's chosen behavior
- Keeps the table definition intact
- Avoids recreating the table every run
- Minimal code change

Cons:
- Auto-increment IDs may continue increasing across runs

### 2. Drop and recreate the table every run

Run `DROP TABLE IF EXISTS users`, then create it again.

Pros:
- Fully deterministic table contents and IDs

Cons:
- More destructive than needed
- Changes table lifecycle behavior more than requested

### 3. Keep behavior unchanged and only explain it

Pros:
- No code change

Cons:
- Demo remains confusing and non-deterministic for repeated runs

## Chosen Design

Use approach 1. Keep the existing `users` table creation logic, then clear prior data with `DELETE FROM users` before running inserts, reads, updates, and deletes.

This keeps the demo behavior simple: each run starts from an empty logical dataset while preserving the table structure. The CRUD flow and proxy behavior stay unchanged.

## Verification

- Run the updated `mysql-demo.go`
- Confirm the demo still completes successfully
- Query the PostgreSQL target table and verify it contains only the row left by the current run, rather than accumulated rows from previous runs
