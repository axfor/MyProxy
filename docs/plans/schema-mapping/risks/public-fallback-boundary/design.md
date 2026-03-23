# Public Fallback Boundary Design

## 1. Goal

本专题聚焦 schema mapping 风险审阅中的“风险 3：public fallback 边界”，目标是避免 MySQL 逻辑 database 的对象解析被 `public` 中同名对象悄悄接管。

## 2. Problem Statement

当前主设计推荐：

```sql
SET search_path TO <mapped_schema>, public;
```

这个默认值的问题在于：

1. 它把“兼容性兜底”默认打开了。
2. MySQL 用户通常把 `USE tenant_a` 理解成“后续未限定对象都在 tenant_a 中解析”。
3. 一旦 `tenant_a.users` 不存在而 `public.users` 存在，查询会落到 `public.users`，而不是报错。

这会弱化逻辑 database 边界。

## 3. Design Summary

本设计采用“默认严格、显式放宽”的策略：

1. 默认只设置目标 schema。
2. `public` fallback 必须由配置显式开启。
3. 文档和日志必须明确标注开启 fallback 的兼容性收益与隔离性代价。

## 4. Configuration Design

在 `database_mapping` 中明确：

```yaml
database_mapping:
  default_schema: public
  fallback_to_public: false
```

语义：

1. `default_schema` 决定未显式指定 database 时的初始 schema。
2. `fallback_to_public` 决定对象解析时是否允许回退到 `public`。

这两个概念必须分离，不能因为默认 schema 是 `public`，就推导出切换后也自动追加 `public`。

## 5. Runtime Rules

### 5.1 Default Behavior

当 `fallback_to_public=false`：

```sql
SET search_path TO <mapped_schema>
```

这意味着：

1. 未限定 schema 的对象只能在当前逻辑 schema 中解析。
2. 当前 schema 中不存在对象时，返回缺失错误。
3. 不会悄悄落到 `public`。

### 5.2 Explicit Compatibility Mode

只有当 `fallback_to_public=true`：

```sql
SET search_path TO <mapped_schema>, public
```

适用场景：

1. 历史系统确实依赖 `public` 中共享函数或共享表。
2. 使用者明确接受隔离性下降。

## 6. User-Facing Semantics

需要在设计与文档中明确告诉用户：

1. 严格模式下，`USE tenant_a` 就等价于“只在 `tenant_a` 中解析未限定对象”。
2. fallback 模式下，`USE tenant_a` 的语义会变成“先查 `tenant_a`，找不到再查 `public`”。

这不是一个小的兼容开关，而是对象解析边界的显式放宽。

## 7. Migration Strategy

为降低存量用户切换成本，建议：

1. 新设计默认 `fallback_to_public=false`
2. 升级文档中提供显式开启方法
3. 如有需要，在日志中对开启 fallback 的实例打印一次启动期提示

例如：

```text
database_mapping.fallback_to_public=true reduces logical database isolation
```

## 8. Error Handling

严格模式下，对象不存在应保留“显式失败”语义，而不是静默回退。

这类失败是好事，因为它能暴露：

1. 逻辑库映射不完整
2. 业务对象实际位于错误 schema
3. 使用者误以为 `public` 是透明兜底层

## 9. Testing Strategy

### 9.1 Unit Tests

覆盖：

1. 默认配置 `fallback_to_public=false`
2. SQL 生成逻辑在关闭 fallback 时不追加 `public`
3. 开启 fallback 时才追加 `public`

### 9.2 Integration Tests

覆盖：

1. `tenant_a` 下缺失表而 `public` 同名表存在时，严格模式返回错误
2. 同场景下，fallback 模式命中 `public` 对象

## 10. Recommended Execution Order

1. 先锁定默认配置和 SQL 生成测试。
2. 再修改配置默认值与 `ApplySchema` 逻辑。
3. 最后补集成测试验证严格模式与 fallback 模式差异。
