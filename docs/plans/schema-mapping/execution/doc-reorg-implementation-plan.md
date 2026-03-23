# Schema Mapping Doc Reorg Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 将 schema mapping 相关设计、风险、执行和补丁文档做物理归组，并清理对旧路径的强依赖。

**Architecture:** 新建统一的 `docs/plans/schema-mapping` 目录，按 `core`、`risks`、`execution`、`fixes`、`history` 分层归档现有文档。迁移后补一个总索引，并把旧文档中的绝对路径和跨日期路径改成新目录内的相对导航或直接写入必要上下文，避免阅读依赖旧位置。

**Tech Stack:** Markdown, PowerShell file moves, ripgrep search, focused document edits

---

### Task 1: Create target structure

**Files:**
- Create: `docs/plans/schema-mapping/`
- Create: `docs/plans/schema-mapping/core/`
- Create: `docs/plans/schema-mapping/risks/`
- Create: `docs/plans/schema-mapping/execution/`
- Create: `docs/plans/schema-mapping/fixes/`
- Create: `docs/plans/schema-mapping/history/`

**Step 1: Create directories**

Create the target directory tree for grouped schema mapping docs.

**Step 2: Verify layout**

Run: `Get-ChildItem docs/plans/schema-mapping -Recurse`

Expected: Target folders exist before any files are moved.

### Task 2: Move grouped documents

**Files:**
- Move into `docs/plans/schema-mapping/core/`: canonical design and main implementation plan
- Move into `docs/plans/schema-mapping/risks/`: risk overview and per-risk topic docs
- Move into `docs/plans/schema-mapping/execution/`: execution plan and task status docs
- Move into `docs/plans/schema-mapping/fixes/`: focused follow-up fix designs
- Move into `docs/plans/schema-mapping/history/`: older superseded design artifacts

**Step 1: Move canonical docs into grouped folders**

Place main design docs under `core`, risk docs under `risks`, execution docs under `execution`, and the LastInsertId fix under `fixes`.

**Step 2: Preserve historical docs**

Place the older 2026-03-07 design and implementation plan under `history`.

**Step 3: Verify moved files**

Run: `Get-ChildItem docs/plans/schema-mapping -Recurse | Select-Object FullName`

Expected: All schema mapping docs are grouped under the new tree.

### Task 3: Add index and normalize references

**Files:**
- Create: `docs/plans/schema-mapping/README.md`
- Modify: moved markdown files under `docs/plans/schema-mapping/**`

**Step 1: Write README index**

Document canonical entry points, folder meanings, and reading order.

**Step 2: Remove old path coupling**

Replace absolute paths and old root-level references with local relative navigation or self-contained context.

**Step 3: Keep docs readable standalone**

Add one or two sentences of context where a doc previously relied on another doc for basic meaning.

### Task 4: Validate references

**Files:**
- Verify: `docs/plans/schema-mapping/**`

**Step 1: Search for stale references**

Run a stale-reference search over `docs/plans/schema-mapping` for old root-level file names and absolute `docs/plans` paths.

Expected: No stale references to old absolute or root-level paths remain except where intentionally preserved as historical text.

**Step 2: Spot-check structure**

Run: `Get-ChildItem docs/plans -Recurse | Select-Object FullName`

Expected: Schema mapping docs are physically grouped and discoverable from the new README.