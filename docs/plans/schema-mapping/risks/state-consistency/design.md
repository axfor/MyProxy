# Schema State Consistency Design

## 1. Goal

本专题聚焦 schema mapping 风险审阅中的“风险 2：schema 状态一致性”，目标是消除“代理会话状态已切换，但 PostgreSQL 连接状态未切换成功”的分裂窗口。

## 2. Problem Statement

当前主设计中的推荐流程是：

```text
ResolveSchema
-> ValidateSchema
-> Update Session.CurrentSchema
-> ApplySchema
```

这个顺序的问题在于：

1. `Session.CurrentSchema` 是代理内存状态。
2. `search_path` 是 PostgreSQL 连接状态。
3. 如果 `ApplySchema` 失败，两者会立刻失去一致性。

这种不一致会污染后续日志、cache key、兼容层行为和排障路径。

## 3. Design Summary

采用“先修改后端连接状态，成功后再提交代理状态”的提交式流程：

```text
ResolveSchema
-> ValidatePreconditions
-> ApplySchemaToPG
-> CommitSchemaToSession
-> Return OK
```

设计核心：

1. PostgreSQL 连接状态是事实来源。
2. 代理会话状态只能在 PostgreSQL 连接状态变更成功后更新。
3. 失败时不做 session 状态更新，不依赖事后回滚。

## 4. Scope

### 4.1 In Scope

1. `USE db` / `COM_INIT_DB` 的 schema 切换顺序修正。
2. 初始 database 应用时的状态提交顺序。
3. 与 `CurrentSchema` 相关的 session 字段更新规则。
4. 相关 handler 测试和失败路径测试。

### 4.2 Out of Scope

1. 跨连接模式的 schema 重放。
2. 多语句事务中的复杂补偿事务设计。
3. `SHOW DATABASES`、`public` fallback 的单独策略问题。

## 5. Runtime Flow

### 5.1 Preconditions

进入 schema 切换前，先验证：

1. 当前连接模式必须允许 schema 语义。
2. 当前不在禁止切换 schema 的事务状态中。
3. 目标 database 能成功解析为合法 schema。

若任一条件不满足，直接返回错误，不触碰 session 状态。

### 5.2 Apply Then Commit

当 preconditions 全部满足时，执行固定流程：

```text
targetSchema := ResolveSchema(dbName)
ApplySchema(conn, targetSchema)
session.CurrentSchema = targetSchema
session.Database = dbName
```

这里的关键不是“更新顺序不同”这么简单，而是状态模型本身发生变化：

1. `ApplySchema` 是唯一的外部副作用步骤。
2. 只有在外部副作用成功后，才写入本地内存状态。
3. 因而失败时天然保持旧状态，不需要额外补偿逻辑。

## 6. Session State Rules

建议在 session 层明确两个语义：

1. `CurrentSchema`：当前已成功应用到 PG 连接的 schema。
2. `Database`：客户端视角下最近一次成功切换到的逻辑 database 名。

如果 `ApplySchema` 失败：

1. `CurrentSchema` 保持旧值。
2. `Database` 保持旧值。
3. 不记录“半成功”的状态。

## 7. API Shape

建议新增一个统一的切换入口，而不是在多个 handler 中手写流程：

```go
func SwitchSchema(ctx context.Context, sess *session.Session, conn *pgx.Conn, mysqlDB string) error
```

职责：

1. 检查前置条件。
2. 解析目标 schema。
3. 应用 `search_path`。
4. 成功后更新 session。

这样可以把“状态提交顺序”固化到单一入口，避免后续新代码再次写回错误顺序。

## 8. Error Handling

错误路径规则：

1. `ResolveSchema` 失败：直接返回，session 不变。
2. `ApplySchema` 失败：直接返回，session 不变。
3. `CommitSchemaToSession` 理论上不应失败；若 session setter 有错误返回，应视为编程错误并优先设计成不可失败路径。

因此推荐把 session 更新实现为轻量内存赋值，不引入额外 I/O 或复杂锁竞争逻辑。

## 9. Observability

日志和指标上需要区分两类事件：

1. `schema_switch_attempt`
2. `schema_switch_success`
3. `schema_switch_failed`

失败日志必须记录：

1. 原 schema
2. 目标 database
3. 目标 schema
4. 失败阶段，例如 `resolve` 或 `apply`

这样才能快速判断是否发生过状态分裂风险。按照本设计实现后，这类风险应被消除。

## 10. Testing Strategy

### 10.1 Unit Tests

覆盖：

1. `ApplySchema` 失败时 session 不更新。
2. `ResolveSchema` 失败时 session 不更新。
3. 成功路径下 `CurrentSchema` 和 `Database` 同步更新。

### 10.2 Handler Tests

覆盖：

1. `UseDB` 成功时状态更新顺序正确。
2. `UseDB` 失败时旧 schema 保持不变。
3. 初始 database 应用失败时连接建立失败，不进入伪成功会话。

## 11. Recommended Execution Order

1. 先写失败测试，锁定“失败不改状态”的行为。
2. 再抽出统一 `SwitchSchema` 入口。
3. 最后替换 handler 中现有分散逻辑。
