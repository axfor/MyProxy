# MySQL Database 到 PostgreSQL Schema 映射设计风险审阅

## 1. 目的

本文档集中审阅 schema mapping 主方案在进入实现前暴露出的关键风险点，避免这些问题混在主设计结论里被忽略。

本文不替代主设计文档，只聚焦于：

1. 当前设计的隐含前提。
2. 这些前提不成立时的行为风险。
3. 建议补充到主设计中的约束与修订方向。

如需回看更早的背景方案，可参考 `../history/2026-03-07-design.md`；但本文本身应可独立阅读。

## 2. 结论摘要

当前设计方向总体合理，但还存在 4 个需要在主设计中显式补足的问题：

1. `search_path` 是 PostgreSQL 连接级状态，而设计文档按会话级状态描述，未明确连接模式约束。
2. schema 切换流程先更新代理会话状态、后更新 PG 连接状态，失败时会造成状态分裂。
3. 默认启用 `public` fallback 会削弱 MySQL 当前 database 语义和隔离性。
4. `SHOW DATABASES` 的暴露范围未定义清楚，容易与权限模型或用户认知冲突。

其中第 1 项是最高风险问题，因为它决定整套方案是否在不同连接模式下成立。

## 3. 风险一：连接模式前提未被写成硬约束

### 3.1 问题描述

主设计文档将 MySQL `database` 到 PostgreSQL `schema` 的映射建立在 `search_path` 之上，并把它描述为稳定的“会话语义”。

但 PostgreSQL 的 `search_path` 实际是连接级状态，不是代理内存中的逻辑会话状态。只有在一个 MySQL 会话稳定绑定一条 PostgreSQL 连接时，这两个概念才等价。

仓库当前明确支持三种连接模式：

- `session_affinity`
- `pooled`
- `hybrid`

见 [internal/pool/pool.go](F:/go/src/github.com/aproxy/internal/pool/pool.go#L12) 和 [internal/pool/pool.go](F:/go/src/github.com/aproxy/internal/pool/pool.go#L88).

而主设计只在“当前实现分析”中提到 `session-affinity` 适合维持会话级 schema 状态，但没有把它写成方案成立的前置条件。

### 3.2 为什么这是设计级问题

只要存在连接复用，下面这个等式就不再天然成立：

```text
Session.CurrentSchema == PostgreSQL connection search_path
```

一旦连接会被借出、归还、重新分配，代理内存中的 `CurrentSchema` 就可能和实际执行 SQL 的 PG 连接状态不一致。

### 3.3 具体风险场景

一个典型错误路径如下：

```text
Client A: USE tenant_a
  -> PG connection X 设置为 search_path = tenant_a

connection X 被归还或复用

Client B: SELECT * FROM users
  -> 拿到 connection X
  -> 如果未重放 B 的 schema，查询仍在 tenant_a 下执行
```

由此会带来：

1. 查询命中错误 schema，结果不正确。
2. 多租户或多逻辑库之间产生状态串话。
3. 代理日志中的 `CurrentSchema` 与 PostgreSQL 实际执行环境不一致，排障困难。

### 3.4 当前代码为何支持这个判断

当前连接池的行为说明该风险是真实存在的：

1. `session_affinity` / `hybrid` 模式会把连接放入 `sessionConns`，按 `sessionID` 复用，见 [internal/pool/pool.go](F:/go/src/github.com/aproxy/internal/pool/pool.go#L89-L123)。
2. `pooled` 模式直接从 `pgxpool` 借连接，见 [internal/pool/pool.go](F:/go/src/github.com/aproxy/internal/pool/pool.go#L125-L131)。

这说明当前代码本身就承认：某些模式下必须维持连接与会话的稳定绑定，连接级状态才可安全依赖。

### 3.5 建议修订

主设计至少应二选一：

1. 收窄设计范围：明确声明 schema 映射方案仅在 `session_affinity` 模式下受支持，`pooled` 和 `hybrid` 暂不保证 `USE db` / `COM_INIT_DB` 语义。
2. 扩展设计范围：若需要支持 `pooled` 或真正的 `hybrid`，则必须补充连接生命周期规则。

若选择第二种，主设计至少需要新增以下要求：

1. 每次借出连接前，都必须按 `Session.CurrentSchema` 重放 `search_path`。
2. 每次归还连接前，都必须重置连接级状态到安全默认值。
3. 不得只在“首次获取连接”时设置一次 schema。
4. 所有依赖未限定 schema 的 SQL，都必须按“下一次可能落到另一条连接”来设计。

### 3.6 推荐结论

基于当前代码基础，推荐先将主设计范围明确限定为 `session_affinity`。这是最小改动且最容易验证的方案。

## 4. 风险二：schema 切换存在代理状态与 PG 状态分裂窗口

### 4.1 问题描述

主设计当前的运行时切换流程为：

```text
ResolveSchema(db_name)
-> 校验 schema 名和存在性
-> 若在事务中则按策略拒绝
-> 更新 Session.CurrentSchema
-> ApplySchema(CurrentSchema)
-> 返回 OK
```

这个顺序的问题是：如果 `ApplySchema` 失败，代理的内存状态已经切换，但 PostgreSQL 连接并未切换成功。

### 4.2 影响

这会导致：

1. 后续日志、cache key、诊断信息看到的是新 schema。
2. 实际 SQL 仍然在旧 `search_path` 下执行。
3. 错误表现高度偶发，难以从单条日志定位。

### 4.3 建议修订

将流程调整为：

```text
ResolveSchema
-> ValidateSchema
-> ApplySchema
-> 成功后再更新 Session.CurrentSchema
```

或者保留现有顺序，但必须显式定义失败回滚逻辑。

## 5. 风险三：默认 `public` fallback 会削弱逻辑库边界

### 5.1 问题描述

主设计当前推荐：

```sql
SET search_path TO <mapped_schema>, public;
```

并在配置示例中默认：

- `default_schema: public`
- `fallback_to_public: true`

这会使未带 schema 的对象解析在当前 schema 中找不到时继续落到 `public`。

### 5.2 风险

对 MySQL 客户端而言，`USE tenant_a` 的直觉语义是“后续未限定对象都在 tenant_a 中解析”。

如果默认开启 `public` fallback，则：

1. 缺表错误可能被 `public` 中同名对象掩盖。
2. 用户会误以为对象确实位于当前逻辑库中。
3. 租户隔离和排障透明度都会下降。

### 5.3 建议修订

更稳妥的默认值应为：

1. 默认只设置 `SET search_path TO <mapped_schema>`。
2. `public` fallback 作为显式可选项。
3. 在文档中明确标注该选项的兼容性收益和隔离性代价。

## 6. 风险四：`SHOW DATABASES` 的返回范围未定义清楚

### 6.1 问题描述

主设计提出 `SHOW DATABASES` 返回“可访问 schema 列表”，但没有定义“可访问”的判定规则。

如果不把这个边界写清楚，后续实现很容易在以下几种语义之间漂移：

1. 返回所有非系统 schema。
2. 返回当前 PostgreSQL 用户有 `USAGE` 权限的 schema。
3. 只返回 `database_mapping.rules` 中允许暴露的逻辑库。

### 6.2 风险

该规则不明确会造成：

1. 结果集合与权限模型不一致。
2. 暴露内部 schema 名称。
3. 用户将结果误解为 PostgreSQL 物理 database 列表。

### 6.3 建议修订

主设计应明确声明 `SHOW DATABASES` 的来源规则，例如：

1. 仅返回映射规则和默认 schema 中允许暴露的逻辑库。
2. 仅返回当前 PostgreSQL 用户可访问且被允许暴露的 schema。
3. 对文档、测试与兼容性说明同步使用“逻辑 database 列表”措辞。

## 7. 建议补充到主设计的约束清单

建议将以下内容直接写入主设计文档：

1. 本方案默认仅保证 `session_affinity` 模式下的正确性。
2. 若支持 `pooled` / `hybrid`，必须定义连接借出、重放 schema、归还重置的完整规则。
3. schema 应用成功后再更新代理会话状态，或显式定义失败回滚。
4. `public` fallback 不应作为默认值，除非业务显式接受隔离性下降。
5. `SHOW DATABASES` 必须定义清楚数据来源和权限过滤规则。

## 8. 后续动作建议

建议按以下顺序处理：

1. 先修订主设计文档中的连接模式约束与切换流程。
2. 再同步调整实现计划，避免后续按错误前提推进实现。
3. 最后补测试矩阵，覆盖不同连接模式下的 schema 状态一致性。