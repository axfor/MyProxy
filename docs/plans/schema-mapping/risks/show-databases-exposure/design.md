# SHOW DATABASES Exposure Design

## 1. Goal

本专题聚焦 schema mapping 风险审阅中的“风险 4：SHOW DATABASES 暴露边界”，目标是明确数据来源、权限过滤和对外语义，避免把 PostgreSQL schema 列表错误暴露成“真实 database 列表”。

## 2. Problem Statement

当前实现中的 `SHOW DATABASES` 倾向于直接列出可见 schema。

如果不加约束，会出现三类问题：

1. 用户把结果误解为 PostgreSQL 物理 database 列表。
2. 非业务 schema 或内部 schema 被暴露。
3. 返回集合与 mapping 规则、权限模型和文档表述不一致。

## 3. Design Summary

`SHOW DATABASES` 在 AProxy 中不表示“PostgreSQL 物理 database 列表”，而表示“当前代理允许暴露给 MySQL 客户端的逻辑 database 列表”。

因此，结果集必须由两层规则共同决定：

1. 映射层允许暴露什么。
2. 当前 PostgreSQL 用户实际有权访问什么。

## 4. Source of Truth

建议把 `SHOW DATABASES` 的候选集合定义为：

1. `database_mapping.rules` 中显式声明的逻辑 database 名
2. `default_schema` 对应的默认逻辑 database，仅当策略允许暴露时

不建议直接扫描所有非系统 schema 作为最终结果来源。

理由：

1. 扫描 schema 是“数据库现状”，不是“代理对外契约”。
2. 代理需要控制用户看见什么，而不是机械转发底层目录。

## 5. Exposure Policy

建议新增显式暴露策略，例如：

```yaml
database_mapping:
  expose_mode: explicit
  exposed_databases:
    - test
    - analytics
```

推荐默认：

1. 若配置了 `rules`，则默认仅暴露 `rules` 键集合。
2. 若未配置 `rules`，则至少不要把所有 schema 无差别暴露给客户端。

## 6. Permission Filter

在得到候选逻辑 database 列表后，还应进行一次权限过滤：

1. 把逻辑 database 解析到目标 schema。
2. 检查当前 PostgreSQL 用户对该 schema 是否具有最低访问能力，例如 `USAGE`。
3. 无权限的逻辑 database 不返回。

这能避免“代理说可以看见，但实际一访问就权限错误”的体验割裂。

## 7. Result Semantics

文档和实现都应统一用词：

1. 结果中的 `Database` 是逻辑 database 名。
2. 它背后可能映射到 PostgreSQL schema。
3. 它不是 PostgreSQL 物理 database 名。

如果未来需要增强兼容性，可以在内部保留 schema 元数据，但对 MySQL 客户端只输出逻辑名。

## 8. Runtime Flow

建议流程：

```text
Load exposed logical database candidates
-> Resolve each logical database to schema
-> Filter by PG access permission
-> Return sorted logical database names
```

这个流程确保：

1. 结果来源受控
2. 暴露结果可解释
3. 权限模型一致

## 9. Backward Compatibility

如果当前实现已经把所有非系统 schema 暴露给客户端，切换到显式暴露可能影响依赖旧行为的使用者。

建议迁移策略：

1. 先在文档中声明旧行为不再推荐。
2. 新设计默认使用显式暴露。
3. 如确有兼容需要，可提供临时 `expose_mode: all-accessible-schemas` 作为过渡选项，但不作为默认值。

## 10. Testing Strategy

### 10.1 Unit Tests

覆盖：

1. `rules` 键集合转为逻辑 database 候选集合
2. 权限过滤前后结果变化
3. 输出结果按逻辑 database 名排序

### 10.2 Integration Tests

覆盖：

1. 显式暴露列表生效
2. 未暴露 schema 不出现在 `SHOW DATABASES`
3. 无 `USAGE` 权限的目标 schema 不出现在结果中

## 11. Recommended Execution Order

1. 先确定配置模型和默认暴露策略。
2. 再实现候选集合与权限过滤逻辑。
3. 最后替换 `SHOW DATABASES` 查询实现并补测试。
