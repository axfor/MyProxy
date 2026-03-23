# Database Mapping LastInsertId 最小修复设计

## 1. 背景

当前 AProxy 已支持将 MySQL `database` 映射为 PostgreSQL `schema`，并在 `session_affinity` 模式下通过 `SET search_path` 将会话切换到目标 schema。

在未启用 `database_mapping` 时，业务表位于默认 `public` schema，`INSERT` 后通过 MySQL 客户端读取 `result.LastInsertId()` 可以正常得到自增主键。

在启用如下映射后：

```yaml
database_mapping:
  default_schema: "public"
  fallback_to_public: false
  rules:
    test: "test"
```

业务数据能够正确写入 PostgreSQL `test` schema，但 MySQL 客户端读取到的 `LastInsertId()` 为 `0`，随后应用再使用 `id=0` 执行查询时失败。

## 2. 问题定义

本问题只针对以下场景：

1. 启用 `database_mapping`。
2. 使用 `session_affinity` 连接模式。
3. MySQL 客户端执行 `INSERT` 后依赖 `result.LastInsertId()` 获取自增主键。

本文不处理更大范围的 schema 元数据一致性重构，也不扩展到 `pooled` 或 `hybrid` 模式。

## 3. 现象与根因

### 3.1 现象

当前故障表现为：

1. `INSERT` 已成功写入映射后的 PostgreSQL schema。
2. AProxy 返回给 MySQL 客户端的 `InsertId` 为 `0`。
3. 应用使用 `id=0` 继续查询，导致“未找到用户”。

### 3.2 根因

`LastInsertId()` 的错误并不是由后续 `SELECT` 引起，而是在 `INSERT` 结果返回阶段就已经产生。

当前实现中，AProxy 只有在事先识别出目标表存在 auto increment 列时，才会在 `INSERT` 路径拼接：

```sql
INSERT ... RETURNING <auto_increment_column>
```

再将返回值写入 MySQL OK 包中的 `InsertId`。

问题在于 auto increment 列识别逻辑目前依赖 PostgreSQL 的隐式 schema 上下文：

- schema cache 使用 `schema.table` 作为键。
- 但查询 auto increment 列时，SQL 条件使用 `table_schema = current_schema()`。

这意味着：

1. cache key 是显式 schema 的。
2. 实际数据库查询却依赖连接当前 `search_path` 推导出的 `current_schema()`。

在 `database_mapping` 场景下，只要这两者有任何时序或语义偏差，自增列识别就可能失效，导致：

1. `GetAutoIncrementColumn()` 返回空字符串。
2. `INSERT` 路径不再拼接 `RETURNING`。
3. `lastInsertID` 保持默认值 `0`。

因此，本问题的直接根因是：

在映射 schema 场景下，AProxy 的 auto increment 列识别仍依赖隐式 `current_schema()`，没有稳定使用映射后的目标 schema 进行显式查询。

## 4. 设计目标

本次最小修复需要满足以下目标：

1. 在 `database_mapping + session_affinity` 场景下，`INSERT` 后的 `LastInsertId()` 返回正确的自增主键。
2. 修复范围仅限于 `LastInsertId` 相关链路，不做全仓 schema 元数据统一改造。
3. 保持当前 MySQL 协议行为、`INSERT` 处理流程和 schema cache 主体结构不变。
4. 修复后通过测试明确覆盖映射 schema 场景。

## 5. 非目标

本次修复明确不包含以下内容：

1. 不统一所有依赖 `current_schema()` 的元数据查询。
2. 不重构 `USE db`、`COM_INIT_DB` 或 `search_path` 的整体机制。
3. 不修改 `pooled`、`hybrid` 模式下的 schema 生命周期。
4. 不顺手重构会话中全部 `database` 与 `schema` 的兼容命名。

## 6. 方案设计

### 6.1 总体策略

只修复 `LastInsertId` 链路上的 auto increment 列识别方式。

核心思路是：

将“依赖 `current_schema()` 隐式解析目标 schema”改为“显式使用已解析的当前 schema 查询目标表的 auto increment 列”。

### 6.2 保持不变的部分

以下行为保持不变：

1. `INSERT` 路径继续在识别出自增列后使用 `RETURNING <column>` 返回插入主键。
2. schema cache 继续使用 `schema.table` 作为缓存键。
3. `SET search_path` 仍作为运行时 schema 切换机制。
4. `session_affinity` 仍作为本方案成立的连接模式前提。

### 6.3 需要调整的部分

#### A. auto increment 列查询函数

将当前基于：

```sql
WHERE table_name = $1
  AND table_schema = current_schema()
```

的查询改为显式 schema 参数：

```sql
WHERE table_schema = $1
  AND table_name = $2
```

这样可以保证自增列识别永远绑定到调用方传入的映射 schema，而不是依赖连接当前状态去猜。

#### B. schema cache 查询入口

保留 `GetAutoIncrementColumn(conn, schemaName, tableName)` 作为入口，但要求其内部查询函数同样接收 `schemaName`。

也就是说：

1. cache key 使用 `schemaName`。
2. cache miss 时的数据库查询也使用相同的 `schemaName`。

从而让缓存命名空间和实际查询命名空间保持一致。

#### C. session 层调用参数

会话层调用 auto increment 检测时，必须传入当前已解析后的 PostgreSQL schema，而不是继续依赖兼容字段或隐式 `search_path`。

本次最小方案优先使用当前会话中的映射 schema 语义字段。如果现有调用点仍通过兼容字段间接传递 schema，需要在这一条链路上确保最终传递的是映射后的 schema 名。

## 7. 代码改动边界

预计只涉及以下代码区域：

1. `pkg/schema/cache.go`
   - 修改 auto increment 列查询函数签名。
   - 将查询条件从 `current_schema()` 改为显式 schema 参数。

2. `pkg/session/session.go`
   - 确认 `GetAutoIncrementColumn()` 传入的是当前映射后的 schema。
   - 如有必要，仅在这一条链路上收紧 schema 来源。

3. `pkg/protocol/mysql/handler.go`
   - 原则上不改变 `INSERT ... RETURNING` 分支逻辑。
   - 仅在必要时补充注释或修正调用参数传递。

## 8. 测试方案

### 8.1 单元测试

补充 schema cache 相关测试，验证：

1. 同名表位于不同 schema 时，auto increment 列查询按显式 schema 隔离。
2. cache miss 后查询得到的 auto increment 列会写入对应 `schema.table` 键。
3. 不再依赖 `current_schema()` 的隐式结果。

### 8.2 集成测试

新增映射场景集成测试，覆盖以下路径：

1. AProxy 配置 `database_mapping.rules.test -> test`。
2. PostgreSQL 中预先创建 `test` schema。
3. MySQL 客户端连接 `test` database。
4. 创建含 `AUTO_INCREMENT` 主键的表。
5. 执行 `INSERT` 后断言 `result.LastInsertId() > 0`。
6. 再断言 `SELECT LAST_INSERT_ID()` 返回相同值。

该测试用于补齐当前测试矩阵中缺失的“映射 schema + LastInsertId”组合场景。

## 9. 风险评估

本方案风险较低，原因如下：

1. 不修改 MySQL 协议编码和 OK 包返回结构。
2. 不重写 `INSERT` 主分支，只修正其前置元数据识别。
3. 不扩展修复面到其他 schema 元数据查询。

本方案仍保留的已知限制：

1. 其他依赖 `current_schema()` 的查询路径未来仍可能暴露类似问题。
2. `pooled` 和 `hybrid` 模式下的 schema 生命周期问题不在本次修复范围内。

## 10. 结论

推荐采用本最小修复方案。

它以最小改动解决当前 `database_mapping + session_affinity + LastInsertId` 故障，并且修法本身使用显式 schema 查询，方向正确，不属于临时绕过。

如果后续需要进一步提升 schema 映射模型的一致性，再单独立项推进“所有 schema 元数据查询统一显式 schema 化”的增量设计与实施。