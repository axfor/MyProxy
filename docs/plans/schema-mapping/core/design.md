# Session Affinity Schema Mapping Design

## 1. Goal

本设计聚焦 schema mapping 主方案里最关键的主链路约束：

1. 连接模式边界。
2. schema 状态一致性。
3. `public` fallback 默认边界。

目标是在不扩大实现范围的前提下，给出一个最小可落地的 `session_affinity` 方案。

本方案明确收窄范围：

1. 仅保证 `session_affinity` 模式下的 MySQL database 到 PostgreSQL schema 映射正确性。
2. 不保证 `pooled` 和 `hybrid` 模式下的 `USE db` / `COM_INIT_DB` 语义。
3. 本轮不处理 `SHOW DATABASES` 的暴露策略重构。

## 2. Design Summary

本方案采用如下设计原则：

1. PostgreSQL 物理 `database` 仍由启动配置固定决定，不随 MySQL 客户端切换。
2. MySQL 的逻辑 `database` 被解析为 PostgreSQL `schema`。
3. 所有 schema 切换必须走统一入口，不允许在 handler 中继续直接拼接 `SET search_path`。
4. 只有在成功将 schema 应用到 PostgreSQL 连接后，才更新代理内存中的会话状态。
5. 非 `session_affinity` 模式收到 schema 切换请求时，直接返回明确错误，而不是尝试“尽力执行”。

## 3. Scope

### 3.1 In Scope

1. 新增配置块 `database_mapping`。
2. 新增统一的 schema 解析与应用逻辑。
3. 在会话模型中区分“后端物理 database”和“当前逻辑 schema”。
4. 修正初始 database、`COM_INIT_DB`、`USE db` 的处理顺序。
5. 默认关闭 `public` fallback，只有显式启用时才附加 `public`。
6. 为上述行为补充单元测试和 handler 行为测试。

### 3.2 Out of Scope

1. 不支持 `pooled` / `hybrid` 的 schema 重放和连接状态重置。
2. 不自动创建 PostgreSQL schema。
3. 不在本轮统一重命名仓库中全部 `Database` 语义。
4. 不在本轮重构 `SHOW DATABASES` 的可见性规则。

## 4. Configuration Design

在 [internal/config/config.go](f:/go/src/github.com/aproxy/internal/config/config.go) 中新增：

```go
type DatabaseMappingConfig struct {
	DefaultSchema    string            `yaml:"default_schema"`
	FallbackToPublic bool              `yaml:"fallback_to_public"`
	Rules            map[string]string `yaml:"rules"`
}
```

顶层配置增加：

```go
DatabaseMapping DatabaseMappingConfig `yaml:"database_mapping"`
```

默认值建议：

```yaml
database_mapping:
  default_schema: public
  fallback_to_public: false
  rules: {}
```

含义如下：

1. `default_schema`：客户端未指定 database 时使用的默认 schema。
2. `fallback_to_public`：是否将 `public` 附加到 `search_path`。
3. `rules`：MySQL database 名到 PostgreSQL schema 名的显式映射表。

## 5. Session Model Design

在 [pkg/session/session.go](f:/go/src/github.com/aproxy/pkg/session/session.go) 中扩展 `Session`：

```go
type Session struct {
	ID              string
	User            string
	Database        string
	BackendDatabase string
	CurrentSchema   string
	...
}
```

字段语义：

1. `BackendDatabase`：AProxy 连接 PostgreSQL 时固定使用的物理 database。
2. `CurrentSchema`：当前 MySQL 逻辑库映射后的 PostgreSQL schema。
3. `Database`：兼容字段，短期内保留，用于减少一次性修改范围；新代码不再把它作为 schema 的唯一真相来源。

兼容策略：

1. 初始化 session 时同时设置 `BackendDatabase`。
2. schema 切换成功后同步更新 `CurrentSchema`。
3. 对仍依赖 `session.Database` 的现有调用点，可在本轮保持同步赋值，后续再逐步迁移。

## 6. Runtime Flow

### 6.1 Initial Database During Handshake

当客户端在握手阶段携带初始 database：

1. 代理记录客户端请求的逻辑 database 名。
2. 第一次获取 PostgreSQL 连接后，检查连接模式。
3. 若模式不是 `session_affinity`，直接返回明确错误。
4. 若模式是 `session_affinity`，执行：
   1. `ResolveSchema(initialDB)`
   2. `ApplySchema(conn, schema)`
   3. 成功后更新 `Session.CurrentSchema`

### 6.2 USE db / COM_INIT_DB

处理顺序固定为：

```text
ValidateConnectionMode
-> ValidateTransactionState
-> ResolveSchema(dbName)
-> ApplySchema(conn, schema)
-> Update Session.CurrentSchema
-> Return OK
```

约束如下：

1. 非 `session_affinity` 模式直接拒绝。
2. 事务中执行 `USE db` 或 `COM_INIT_DB` 直接拒绝。
3. 只有 `ApplySchema` 成功后，才更新代理会话状态。

## 7. Schema Resolution Design

新增统一入口，例如放在新的 schema mapping 模块中：

```go
ResolveSchema(mysqlDB string) (string, error)
ApplySchema(ctx context.Context, conn *pgx.Conn, schema string) error
```

### 7.1 ResolveSchema Rules

1. 若 `mysqlDB` 为空，返回 `default_schema`。
2. 若 `mysqlDB` 在 `rules` 中存在显式映射，返回映射值。
3. 否则默认返回 `mysqlDB` 本身。
4. 返回前必须校验 schema 标识符是否合法。

建议采用白名单校验：

1. 允许字母、数字、下划线。
2. 首字符不允许为数字。
3. 对不符合规则的 schema 名直接报错。

本轮不支持带点号、引号或复杂 PostgreSQL 标识符语法，以换取实现简单和安全边界清晰。

## 8. Schema Application Design

`ApplySchema` 负责安全地修改 PostgreSQL 连接状态。

要求如下：

1. 不允许直接把客户端输入拼进 SQL。
2. 只使用经过 `ResolveSchema` 校验后的 schema 名。
3. 默认执行：

```sql
SET search_path TO <schema>
```

4. 仅当 `fallback_to_public=true` 时执行：

```sql
SET search_path TO <schema>, public
```

虽然 `SET search_path` 无法通过普通参数化绑定标识符，但可以在白名单校验后做受控转义拼装。

## 9. Error Handling

需要新增明确错误语义：

1. 非 `session_affinity` 模式下收到 schema 切换请求：返回“当前连接模式不支持 USE db / COM_INIT_DB 语义”。
2. 事务中切换 schema：返回“cannot change database while transaction is active” 一类兼容错误。
3. schema 名非法：返回“invalid database name for schema mapping”。
4. PostgreSQL `SET search_path` 失败：直接返回底层错误，不更新 `CurrentSchema`。

## 10. Testing Strategy

### 10.1 Unit Tests

新增 schema mapping 模块测试，覆盖：

1. `rules` 命中。
2. 未命中时回退原始 db 名。
3. 空 database 名回退 `default_schema`。
4. 非法 schema 名被拒绝。
5. `fallback_to_public` 的 SQL 生成逻辑。

### 10.2 Handler Tests

在 [pkg/protocol/mysql/handler_test.go](f:/go/src/github.com/aproxy/pkg/protocol/mysql/handler_test.go) 增加：

1. `session_affinity` 下 `USE db` 成功。
2. `pooled` / `hybrid` 下 `USE db` 被拒绝。
3. 事务中 `USE db` 被拒绝。
4. `ApplySchema` 失败时 `CurrentSchema` 不更新。

### 10.3 Integration Tests

在现有集成测试基础上补充：

1. 初始 database 能正确应用到后端 schema。
2. `USE db` 后未限定 schema 的查询命中正确 schema。
3. `fallback_to_public=false` 时，不会意外命中 `public` 中同名对象。

## 11. File Impact

本设计预计涉及：

1. 修改 [internal/config/config.go](f:/go/src/github.com/aproxy/internal/config/config.go)
2. 修改 [pkg/session/session.go](f:/go/src/github.com/aproxy/pkg/session/session.go)
3. 新增 schema mapping 模块及其测试
4. 修改 [pkg/protocol/mysql/handler.go](f:/go/src/github.com/aproxy/pkg/protocol/mysql/handler.go)
5. 修改 [pkg/protocol/mysql/handler_test.go](f:/go/src/github.com/aproxy/pkg/protocol/mysql/handler_test.go)
6. 视测试需要补充集成测试文件

## 12. Recommended Execution Order

1. 先补配置和 schema mapping 单元测试。
2. 再实现 `ResolveSchema` / `ApplySchema`。
3. 然后修改 handler 流程并补行为测试。
4. 最后补集成测试并验证 `session_affinity` 路径。
