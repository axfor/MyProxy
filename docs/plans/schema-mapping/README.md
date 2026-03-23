# Schema Mapping Plans

本目录收拢 AProxy 中“MySQL database 映射到 PostgreSQL schema”这一条功能线的设计、风险拆解、执行计划和后续修补设计。

## Canonical Entry Points

按当前推荐阅读顺序：

1. `core/design.md`
2. `risks/overview.md`
3. `risks/state-consistency/design.md`
4. `risks/public-fallback-boundary/design.md`
5. `risks/show-databases-exposure/design.md`
6. `core/implementation-plan.md`
7. `execution/implementation-plan.md`
8. `execution/task-status.md`

若只关注一次具体故障修补：

1. `fixes/last-insert-id/design.md`

若需要回看最早的设计背景：

1. `history/2026-03-07-design.md`
2. `history/2026-03-07-implementation-plan.md`

## Folder Guide

- `core/`: 当前主设计和主实现计划。
- `risks/`: 围绕主设计拆分出的风险审阅和专题设计。
- `execution/`: 执行级编排和任务状态。
- `fixes/`: 主功能实现后的补丁型专题设计。
- `history/`: 早期版本文档，仅作背景对照。

## Documentation Rules

本目录中的文档尽量满足两条规则：

1. 单独打开任一文档时，能读懂其问题背景和结论，不依赖外部文档补齐基础上下文。
2. 文档间链接只承担导航作用，不承担关键语义说明。

因此，若后续继续新增 schema mapping 相关设计，优先放入本目录，并保持文档自解释。