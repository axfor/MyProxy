# 角色：
 你是资深 Go 协议与数据库工程师（或高阶工程助理模型）。任务：用 Go 实现一个“**MySQL 协议前端 → PostgreSQL 协议后端**”的**透明代理（proxy）**，要求使不修改客户端代码的 MySQL SDK/驱动能够**平滑**访问 PostgreSQL。下面是详细、逐条且可执行的规范、实现步骤、注意点和验收标准。请严格遵照并把实现细分为可在 Sprint/Issue 中执行的任务（含单元与集成测试）。

# 目标：
 目标是实现一个 **MySQL 协议（客户端）—> PostgreSQL（服务端）** 的代理（proxy），使得 MySQL 客户端/SDK 无需改动、平滑访问 PostgreSQL。该 prompt 很长很严谨，覆盖协议层、SQL 语义层、攻击/异常处理、测试用例、验收标准、交付物等细节——你可以把它直接复制使用或进一步修改。

> 重要说明（必须加入到任何实现与验收讨论中）
>
> * **在“线协议（wire protocol）层面”**：代理可以做到在 MySQL 客户端眼里“像一个 MySQL 服务器”那样完全兼容（握手、认证、OK/ERR/Resultset 包等）。
> * **在“SQL 语义层面”**：MySQL 与 PostgreSQL 在 SQL 语法、内建函数、事务隔离语义、引擎行为、系统变量、存储过程、某些 DDL/DDL 选项上存在本质差异。任何要求“100% 等价”的承诺必须明确范围与可接受的折衷（见下面的“局限与策略”）。Prompt 会要求记录所有已实现/未实现差异并提供可选的兼容层/替代方案（如 SQL 重写、代理级 session 附加层、developer migration guide 等）。

---

# 目标（Goal / Acceptance Criteria）

* 实现一个对外表现为 **MySQL 协议服务端** 的代理（支持 MySQL 官方驱动与常见 SDK），并在内部以 **PostgreSQL 客户端** 协议与 Postgres 交互。外部 MySQL 客户端无需修改代码或驱动配置（除了连接 host\:port）。
* 必须实现下列**最小可验收功能集**（client-visible 行为与 MySQL 官方 server 等价）：

  1. MySQL 握手与认证（包括常见 auth plugins 支持/降级策略），支持 TLS（来自客户端）与 TLS 到 PG。
  2. 支持 COM\_QUERY、COM\_PREPARE/COM\_EXECUTE（预编译/二进制协议）、COM\_STMT\_CLOSE、COM\_FIELD\_LIST、COM\_PING、COM\_QUIT 等常见 MySQL 命令。
  3. 支持文本与二进制协议的数据行返回（对客户端可透明处理）。
  4. 正确地将结果集、error/ok/warning、affected\_rows、insert\_id 等字段返回给客户端，语义上保持与 MySQL 客户端期望一致。
  5. 支持事务控制（BEGIN/COMMIT/ROLLBACK、SAVEPOINT/ROLLBACK TO）、autocommit 模式映射。
  6. 支持常见 SQL 语法的自动重写（例如 `?` 占位符→`$1`、`INSERT ... ON DUPLICATE KEY UPDATE` → `INSERT ... ON CONFLICT ... DO UPDATE` 等），并提供可插拔的 SQL 重写规则集合。
  7. 提供完善的测试套件，覆盖协议、prepared statements、binary 参数、DDL/DML/事务、SHOW/DESCRIBE、错误映射与边界情况。
  8. 提供详尽的安全策略（限流、最大包大小、恶意包检测、拒绝危险的 MySQL 专有命令等）并在发现攻击行为时返回合适的 MySQL 错误码。
  9. 提供性能与可观测性（metrics、日志、追踪）以及运行时配置（连接池、超时、最大并发会话数）。
  10. 全集的不同mysql 到 pg的测试 case集合，并使用专门的mysql_to_pg_case.md文档都记录兼容情况

验收通过条件：在标准 MySQL 客户端（mysql CLI、mysql2 Node.js、Python MySQLdb / mysql-connector、JDBC 等）上运行全部集成测试（见测试清单），结果与直连 MySQL（等价场景）或与预期 Postgres 映射一致（差异明确定义并记录）。

---

# 其他约束：

1. **接口兼容性**：100%建议mysql 到 pg的语法转换。
2. **项目布局**：整个仓库必须遵守 `golang-standards/project-layout` 的目录规范（cmd/, pkg/, internal/, api/, configs/, docs/ 等）。
3. **质量优先**：实现需采用最佳实践（清晰接口/文档、完善单元与集成测试、CI、可观测性、错误处理、一致性文档），不能偷工减料。
4. **兼容性声明**：任何无法在当前架构下保证的 etcd 行为/语义须在设计文档中逐条列出并给出替代方案或实现路线（不得模糊带过）。
5. **git提交约束**：整个过程中严格遵守，使用git commit 提交是不能出现claude任何签名与字眼描述
6. **编译**：不能添加如//go:build cgo编译标志，需要保障内存引擎与rocksdb引擎2种存储引擎所有测试案例都通过
7. **编译参数1**: 新增make 编译与性能测试等选项，golang build添加 GOEXPERIMENT=greenteagc 参数
8. **编译参数1**: 不能包含 go:build 条件编译，默认都会编译rocksdb版本
9. **性能1**: 如果必须使用 regexp.MustCompile将其转换为全局对象，提升性能
10. **特性满足的1**: 过程中发现mysql有，但是pg没有的特性，请列出来
11. 对话使用中文，但git commit不能含claude签名，commit使用英语不能含中文，代码与代码注释使用英文不能含中文，文档先使用中文，后面统一翻译成英文

--

# 局限 / 风险声明（Must be included in prompt）

请在实现前提醒 Stakeholders：

1. SQL 语义的 100% 等价通常不可行（存储过程、触发器、某些 MySQL 特有函数、MyISAM 行为、InnoDB 的隐式事务隔离细节等）。必须决定：是采用**代理重写/仿真**尽量兼容，还是对某些功能标记为“不支持/需迁移”。
2. 某些 driver-specific 较低层特性（例如 MySQL 的复制/二进制日志控制命令、某些 auth plugin）不适合或不应该被代理实现——需要以高层拒绝并返回合理错误。
3. 对于安全/合规（例如 SQL 审计、PII redact），代理内可能需要访问明文 SQL 与参数；需要在设计中考虑合规与日志脱敏。

---

# 建议技术栈（参考）

* 语言：**Go**（性能好、生态成熟）。
* MySQL 协议服务端实现参考： `github.com/go-mysql-org/go-mysql`（你已经提到）。
* MySQL SQL 解析/重写：`pingcap/parser`（TiDB parser）或 `vitess` 的 SQL parser（能解析 MySQL 方言）。
* Postgres 客户端库：`github.com/jackc/pgx` 或 `pgx/v4`（支持原生 protocol、binary/text 格式、prepared/extended protocol）。
* 可选：使用 `pgproto3`（若需要更低层次控制 Postgres 协议）。
* 测试：Go 的 `testing`、integration 用 Docker Compose / Testcontainers 启动真实 PG 实例与 MySQL 客户端脚本。
  （如果你用其他语言/库，可替换；Prompt 需保持“可插拔”）

---

# 架构概览（High-level）

1. **Listener（MySQL-facing）**：监听 MySQL 客户端连接，完成 MySQL 握手/认证，维护 session-state。基于 `go-mysql` 可以做。
2. **Session Manager**：为每个 MySQL client session 建立会话数据（session variables、user variables、prepared statement map、last\_insert\_id、transaction state、temp tables mapping 等）。
3. **PG Connection Manager / Pool**：对 Postgres 做连接管理。对于**需要 session 语义隔离**的 MySQL 连接（例如使用临时表或长事务），建议一 MySQL 会话绑定一条或多条后端 PG 连接（session-affinity）。可配置为“每 MySQL session 一对一绑定 PG 连接”或“池化但对事务进行 pinning”。
4. **SQL Translate/Rewrite Engine**：把 MySQL 文本 SQL（或预编译的）转换为等价的 Postgres SQL（包括占位符重写、函数/语法替换、DDL 翻译等）。采用 AST 解析后重写的方式最稳健。
5. **Protocol Mapper**：负责把 MySQL 协议命令（COM\_QUERY、COM\_STMT\_PREPARE、COM\_STMT\_EXECUTE 等）映射为 Postgres extended protocol（Parse/Bind/Execute）或简单 Query。负责参数类型转换、binary/text 编码转换、结果集列元数据转换。
6. **Response Mapper**：将 PG 返回的 DataRow/CommandComplete/Error 转换为 MySQL 的 Resultset/OK/ERR/Warn packet，确保 affected\_rows、insert\_id、warnings 等行为正确。
7. **Security & Policy Layer**：限流、黑白名单、SQL 白名单/黑名单、审计日志、异常行为检测。
8.  MySQL 客户端连接 (ConnectionHandler) 都有一个专用的 PostgreSQL 连接 (pgConn)是一对一的关系，不是连接池这样的设计正好满足你说的要求：client -> aproxy -> pg 是一一对应的。 让我检查一下连接的创建和管理
9.  LAST_INSERT_ID这个实现需要注意 aproxy不需要缓存，直接从后端的pg链接取LAST_INSERT_ID



# 架构实现要求  
1. 我们的 aproxy 应该是基于抽象语法树AST转换，把 mysql 语法映射到 pg 语法，而不是硬编码规则（如正则等方式）转换，最好的是基于AST，用“SQL 抽象语法树（AST）+ 重写”的方式，而不是字符串替换
2. 不要基于文本模式匹配的方法，需要真正利用AST的结构化信息转换，防止类似案例：
  ```
     如表名 test_indexes 中包含 "indexes",代码错误地将其识别为 INDEX 关键字解决: 添加单词边界检查,确保前后字符不是字母/数字/下划线/反引号
     这样会出现 “假如用户的表里面字段名  indexes呢， 你应该结构化替换， AST应该可以解析出sql的每个部分吧，比如， 是不是表名， 而不是通过关键字”
  ```

---

# 详细协议转换要点（核心 - 必读）

下面给出最常用命令的**逐步转换流程**与关键实现注意事项。实现时把这些作为单元/集成测试场景。

## 1) 握手与认证

* **MySQL client → proxy**：proxy 必须按 MySQL 协议返回初次 handshake 包（包含 server capabilities、auth plugin 等）。
* **认证模式选择**（设计选项）：

  * **Pass-through（推荐）**：用客户端用户名/密码去连接 PG（要求在 PG 上为每个 MySQL 用户创建对应 role，或使用外部 auth）。优点：最少变动；缺点：需要同步用户/权限。
  * **Proxy-auth（常用）**：代理验证客户端（例如基于本地用户库、LDAP、MySQL auth plugin），再用统一后端 PG 用户或按映射的 PG 用户连接后端。优点：可集中控制；缺点：需权限映射。
  * 对于 `caching_sha2_password`、`mysql_native_password` 等 MySQL 插件，proxy 必须实现相应握手与交换或返回兼容结果（可以选择降级到明文 over TLS 或拒绝）。
* **TLS**：支持 `CLIENT_SSL` handshake；若启用需要在 proxy 与 PG 之间也建立 TLS（或按配置决定是否明文）。

## 2) COM\_QUERY（普通文本查询）

* 接收到 COM\_QUERY(sql):

  1. 若 SQL 为 MySQL 专有命令（SHOW、DESCRIBE、SET、USE、SELECT LAST\_INSERT\_ID()、SHOW STATUS），先进入专用处理分支（由 Query Translator 或 Emulation 层处理）。
  2. 否则送到 SQL Rewrite 引擎（将 mysql-specific tokens/backticks/`?` 占位符等替换）。
  3. 发送给 PG：使用 `pgx` 的简单 Query（or extended Parse+Bind+Execute for parameter type hints）。
  4. 从 PG 获取结果，调用 Resultset → MySQL FieldPacket + RowPackets 的转换函数（包括类型映射、长度、charset、flags、decimals）。
  5. 返回 EOF/OK/WARN 等包按 MySQL 协议。
* **注意**：若 SQL 包含多语句（CLIENT\_MULTI\_STATEMENTS flag），需要拆分并依序执行并返回多个结果集。

## 3) 预处理/二进制协议（COM\_STMT\_PREPARE / COM\_STMT\_EXECUTE）

* **PREPARE**：

  1. 接 MySQL 的 COM\_PREPARE，proxy 解析并记录 MySQL stmt\_id → 生成唯一 PG prepared name（例如 `proxy_s{session}_{stmt_id}`）。
  2. 在 PG 上执行 `Parse`（把 `?` 替换成 `$1..$n`）并 `Describe`，保留 parameter count/types 和 resultset metadata。如果参数类型未知，默认 text/binary 并在 bind 时转换。
  3. 返回 MySQL 的 prepare OK 包（statement id, param\_count, column\_count, etc）。
* **EXECUTE**：

  1. 接收 COM\_STMT\_EXECUTE（含 binary/text parameters）时，将每个参数按照先前 prepared 的 param type 映射并序列化为 PG 的参数（binary/text 分别转换）。
  2. 对于特殊类型（日期/时间/bit/geometry/blob），进行恰当的格式转换。
  3. 在 PG 上执行 Bind+Execute（或使用 `pgx` 的 Prepare+Exec），获取 DataRow 并转回 MySQL 行协议（binary/text 与 driver 设置保持兼容）。
  4. 处理 EOF/OK/WARN/ERROR 映射。
* **关闭**：COM\_STMT\_CLOSE 时，从 proxy 的 map 清除，并可在 PG 上 `DEALLOCATE` 对应 prepared name（或周期性回收）。

## 4) 参数占位符 & 占位符重写

* MySQL prepared 使用 `?`，位置按顺序。PG 使用 `$1,$2,...`。**必须在 PREPARE 阶段把 SQL 从 `?` 改写成 `$1..$n`** （基于 AST 更稳健），并把 param count 与位置一一对应。
* 对于动态 SQL（client 使用直接文本的 `?` 占位），有些 driver 会在客户端侧转义为 literal；代理必须能处理两种情况。

## 5) 类型映射（常用）

在实现时必须有一个完整的 mapping table（在这里给出常用映射）：

| MySQL 类型         |                       PostgreSQL 类型 (建议) | 转换注意事项                                              |
| ------------------ | -------------------------------------------: | --------------------------------------------------------- |
| TINYINT(1)         |                          boolean 或 smallint | MySQL 将 TINYINT(1) 常作为 boolean，需按 schema/flag 决定 |
| TINYINT, SMALLINT  |                                     smallint |                                                           |
| MEDIUMINT, INT     |                                      integer |                                                           |
| BIGINT             |                             bigint / numeric | big int 超过 PG bigint 需 numeric                         |
| FLOAT              |                                         real |                                                           |
| DOUBLE             |                             double precision |                                                           |
| DECIMAL            |                                      numeric | 精度需按列定义                                            |
| CHAR/VARCHAR       |                                 varchar/text | 长度限制要保留或转换                                      |
| TEXT/BLOB          |                                   text/bytea | BLOB → bytea（binary），TEXT → text                       |
| DATETIME/TIMESTAMP | timestamp without time zone / with time zone | MySQL 无时区概念，需注意时区转换                          |
| DATE               |                                         date |                                                           |
| TIME               |                                         time |                                                           |
| ENUM/SET           |                        text 或 domain / enum | 可创建 domain 或映射为 text                               |
| JSON               |                                        jsonb | PG 的 jsonb 更佳                                          |
| BIT                |                        bit / boolean / bytea | 取决于位长度                                              |

务必在代码中实现二进制序列化/反序列化逻辑（MySQL length-encoded strings vs PG text/binary），并对 null 值统一处理。

## 6) 自增 / last\_insert\_id()

* 对于 MySQL 的 `AUTO_INCREMENT` 字段，建议将 CREATE TABLE 转换为 PG 的 `SERIAL` / `BIGSERIAL` 或 `GENERATED BY DEFAULT AS IDENTITY`，并在 INSERT 时使用 `RETURNING` 直接获取值，**并在 proxy session 中记录 last\_insert\_id** 以便 `SELECT LAST_INSERT_ID()` 返回正确值。
* 若转换后无法 `RETURNING`（例如用户的 INSERT 未被重写），可在代理层执行 `SELECT currval(pg_get_serial_sequence('schema.table','id'))` 在同一 PG session 内获取。

## 7) SHOW / DESCRIBE / INFORMATION\_SCHEMA / SET / USE

* **SHOW TABLES / SHOW DATABASES / SHOW COLUMNS / DESCRIBE**：代理必须将这些 MySQL 命令转换为等价的 PG catalog 查询或模拟结果集，使常见客户端/ORM 能正常工作（返回列名、type、Null、Key、Default、Extra 等列）。
* **SET / @@session / user variables**：需要代理内维护 session 变量 map。对 `SET NAMES` 应转换为 `SET client_encoding` 或在连接时设置编码。对 `@@autocommit` 需要映射到事务开启/关闭策略。对 `@user_var`（如 `SET @a=1; SELECT @a;`）可在代理层用替换或在 PG 使用临时表/with 子句来模拟。
* **USE db**：MySQL 的 database 切换通常映射到 PG search\_path/schema 切换或需要在 SQL 重写时为未指定 schema 的表加上 schema 前缀。必须明确映射策略。

## 8) DDL 翻译（CREATE TABLE / ALTER TABLE / INDEX）

* 实现必须包含对常见 DDL 的语法重写（去掉 ENGINE、DEFAULT CHARSET、AUTO\_INCREMENT → IDENTITY、UNSIGNED → domain/constraint、INDEX 类型映射）。
* 对于无法兼容的语句（如 MyISAM 特性），proxy 必须明确失败并返回 MySQL 风格的错误。
* DDL 执行后，代理需要刷新内部元数据 cache（若有的话）以保证 SHOW/DESCRIBE 的一致性。

## 9) 错误与警告映射

* PG 的 SQLSTATE 与错误消息需要被映射为相应的 MySQL error codes（ER\_\*）。实现必须包含一个**常见错误映射表**（至少包括重复键、外键违规、语法错误、权限拒绝、超时）。
* 同时保留 PG 原始 ErrorText（在 debug mode），并在生产中可根据配置决定是否把详细 PG 错误暴露给客户端。

## 10) 不支持或应该拒绝的命令

列出必须以 MySQL 风格错误拒绝的危险/不支持命令，例如：

* 复制/主从命令（COM\_BINLOG\_DUMP、CHANGE MASTER TO）
* FLUSH PRIVILEGES（有特殊含义的 MySQL admin 命令）
* LOAD DATA LOCAL INFILE（视实现决定是否支持；若支持需实现流式传输）
  对于这些命令，应返回明确的 MySQL 错误码与说明。

---

# 安全、健壮性与防护（必做）

* **认证策略**：支持两种模式（pass-through 与 proxy-auth），并支持对后端 PG 的最小权限原则。
* **限流 & 连接控制**：全局/每 IP/每 user 限制并发连接数与 QPS；请求包大小限制（max\_packet\_size）；长查询强制超时。
* **异常包与协议攻击防护**：对不合法/乱序/超长/非法 flag 的 MySQL 包必须立即断开或返回错误，不应 crash。
* **SQL 注入保护**：代理本身不会主动构造 SQL（除重写）；但应尽量推动参数化执行，审计敏感 SQL。在日志中支持参数脱敏配置。
* **资源隔离**：防止单个 session 占满后端连接池（长事务/锁）。对事务超时、回滚强制等策略。
* **审计与合规**：可选的 SQL 审计（谁、何时、执行语句/参数）与日志脱敏。
* **运维安全**：管理接口（metrics/health/debug）需认证与 IP 白名单，避免泄露敏感信息。

---

# 测试计划（必须包含在 Prompt）

提供一套详尽的测试计划并把测试作为强制验收条件：

## 单元与集成测试（Examples）

1. **Handshake & Auth**：测试多种 auth 插件、TLS 与非 TLS、错误凭证。
2. **COM\_QUERY 基线**：简单 SELECT/INSERT/UPDATE/DELETE，检查 affected\_rows、warnings、resultset 字段。
3. **Prepared Statements**：PREPARE/EXECUTE/CLOSE；参数类型边界（NULL、binary、blob、bigint、datetime）。
4. **Binary Protocol**：mysql 客户端的二进制协议读写（例如使用 `mysql` CLI 的 prepared binary 测试）。
5. **Transactions**：autocommit on/off、begin/commit/rollback、savepoints、concurrent事务隔离测试。
6. **DDL 转换**：CREATE TABLE（含 AUTO\_INCREMENT、ENGINE、CHARSET）→ PG DDL 的结果验证，SHOW/DESCRIBE 输出匹配。
7. **SHOW / DESCRIBE / INFORMATION\_SCHEMA**：与预期格式一致。
8. **LAST\_INSERT\_ID**：插入后多线程并发插入确保 session 隔离与返回正确 last\_insert\_id。
9. **Error Mapping**：触发 PK 冲突、外键冲突、语法错误、权限错误，验证 MySQL 客户端收到的 error code/message。
10. **Multi-statement**：CLIENT\_MULTI\_STATEMENTS 下多语句返回多个 resultsets。
11. **Edge fuzzing**：随机/构造的非法 MySQL 协议数据包，确认代理不会 crash 并返回安全错误。
12. **Performance**：在 target QPS 与并发连接数下压力测试（测延迟分位、吞吐、内存/CPU），同时观察 PG backend 滚动表现。
13. **Compatibility**：使用主流 driver（mysql CLI、mysql2 (Node)、mysql-connector-java、pymysql、Go mysql driver）运行典型 ORM/应用集成测试（至少 3 种语言）。

## 安全测试

* SQL 注入模拟（确保代理不会把 driver 参数错误地当作 SQL）。
* DOS/slowloris / large-payload 注入测试。
* 未授权访问管理接口测试。

---

# 部署 / 运维 / 可观测性（交付项）

* Docker 镜像与 Helm Chart / Kubernetes manifest。
* 配置项（示例 config file / env）：监听端口、后端 PG url、auth mode、连接池大小、超时、SQL rewrite 开/关、限流策略、日志级别、metrics endpoint、TLS 配置。
* Metrics（Prometheus）：active\_sessions, total\_queries, queries\_per\_second, avg\_latency\_ms, error\_rate, pool\_usage, bytes\_in/out。
* Logs：structured JSON logs（含 session\_id、client\_ip、user、statement（可选脱敏）、duration、rows\_affected、error\_code）。
* tracing：OpenTelemetry 支持（可选）。
* Runbook：常见故障（后端 PG 不可达、auth 失败、内存泄漏、高延迟）及排查步骤。
* Backward compat notes：列出 MySQL 特性未支持的清单与替代实现建议。

---

# 开发与交付清单（Checklist）

* [ ] 设计文档（协议映射、session model、错误映射表、type mapping）。
* [ ] 基础协议层：MySQL handshake、auth、packet parsing/serialization（基于 go-mysql）。
* [ ] PG 连接管理（session affinity、pool、reconnect）。
* [ ] SQL Parse & Rewrite（支持常见 rewrites）。
* [ ] Prepared statements 完整实现（含 binary/text param conversion）。
* [ ] Resultset 张贴（字段元数据 mapping）。
* [ ] SHOW/DESCRIBE/INFORMATION\_SCHEMA emulation。
* [ ] 错误映射表（常用 SQLSTATE → MySQL ER\_\*）。
* [ ] 安全模块（限流、包大小、异常检测）。
* [ ] 测试：unit + integration + fuzz + perf。
* [ ] 文档：API、配置、部署、runbook、已知限制。
* [ ] 发布：Docker image、helm chart、CI pipeline。

---

# 交付物（Deliverables）

* 源代码仓库（带模块化结构、README、LICENSE）。
* 单元测试 + 集成测试 + CI 配置。
* Docker 镜像与部署清单（K8s/helm）。
* 性能测试报告（在目标吞吐/并发下的 P50/P95/P99 延迟、CPU、内存）。
* 完整开发文档与运维 runbook（含已知不兼容清单）。
* SQL 重写规则集与可配置策略（JSON/YAML）。

---

# 示例：可直接复制给工程师/LLM 的 Prompt（可粘贴使用）

下面是一个**可直接复制粘贴**的 prompt，用于指示实现团队或 LLM 生成设计/代码。**注意**复制全部内容（包含 Acceptance Criteria 与 Test Cases）:

```
目的：实现一个 MySQL 协议对外（客户端使用现有 MySQL SDK / 驱动无需改动），并在内部将请求转换为 PostgreSQL 协议与后端 PostgreSQL 交互的代理（proxy）。该代理在客户端看来就是一个 MySQL 服务器，但后端实际与 PostgreSQL 通信。目标是尽量实现在协议层与常见 SQL 行为上的兼容性，并提供完整的测试与安全策略。

关键要求（Acceptance Criteria）：
1. MySQL 协议：正确实现 MySQL handshake 与认证，支持 TLS（CLIENT_SSL），支持常见 MySQL client flags（CLIENT_PROTOCOL_41, CLIENT_PLUGIN_AUTH, CLIENT_MULTI_STATEMENTS 等）。
2. 支持 COM_QUERY、COM_PREPARE、COM_STMT_EXECUTE、COM_STMT_CLOSE、COM_FIELD_LIST、COM_PING、COM_QUIT 等命令，并在行为上与 MySQL 官方 server 等效。
3. Prepared statement 支持位置占位符转换（? -> $1..$n），binary/text 参数正确转换，支持 NULL 与 BLOB 边界。
4. 将 PostgreSQL 的结果集 / error / warning / affected_rows / insert_id 等正确转换回 MySQL client 可以理解的格式。实现 last_insert_id() 映射。
5. 提供 SQL 重写引擎（基于 MySQL 方言 parser），至少实现常见重写：backticks -> quotes, `INSERT ... ON DUPLICATE KEY UPDATE` -> `INSERT ... ON CONFLICT ... DO UPDATE`, `REPLACE INTO` -> `INSERT ... ON CONFLICT`，常用函数替换（NOW() -> CURRENT_TIMESTAMP, GROUP_CONCAT -> string_agg 等）。
6. 对 SHOW/DESCRIBE/INFORMATION_SCHEMA/SET/USE 命令提供兼容实现（查询 pg_catalog / information_schema 并返回符合 MySQL client 预期的列和格式）。
7. 事务语义支持：autocommit 映射、BEGIN/COMMIT/ROLLBACK、SAVEPOINT 支持（并记录并发事务行为差异）。
8. 错误码映射：提供 PG -> MySQL error code 的映射表（常见情况需覆盖）。
9. 安全：支持限流、最大包大小、异常包检测、拒绝危险的 MySQL 专有命令并返回 MySQL 风格错误码。
10. 测试覆盖：完整的 Unit + Integration + Fuzz + Performance 测试（包含多驱动兼容性验证）。
11. 文档：技术设计文档、配置说明、部署指南（Docker/Kubernetes）、运维 runbook、已知限制列表与迁移建议。
12. 性能：在目标负载下（需由产品给定 QPS/并发）保持合理延迟，提供 perf report。

非功能/实现细节（必须包含在实现中）：
- 推荐技术栈：Go + go-mysql-org/go-mysql（MySQL 协议），pingcap/parser 或 vitess parser（MySQL SQL 解析/重写），jackc/pgx（Postgres client）。但实现应允许替换组件。
- 每个 MySQL session 应维护 session-state（user-variables、prepared-stmts、last_insert_id、transaction state、temp-tables mapping）。
- 建议默认对每个 MySQL 客户端会话 pin 一条后端 PG 连接，以保证 session 级别行为一致（可配置为池化，事务时临时 pin）。
- 提供配置项控制：auth mode（pass-through 或 proxy-auth）、sql_rewrite_enabled、max_packet_size、connection_pool_size、per_user_rate_limit、log_sensitive=false/true。
- 记录详细日志（可选脱敏），并提供 Prometheus metrics 与 OpenTelemetry tracing 支持。

协议 & SQL 转换细节（必须在代码/文档中实现）：
- PREPARE: 将 ? 替换为 $n；在 PG 上 Parse 并 Describe，缓存 param types/result metadata；向 MySQL client 返回 prepare_ok。
- EXECUTE: 按类型转换参数（包括 binary/text），Bind+Execute；返回 DataRow -> MySQL RowPacket 映射。
- 类型映射：实现一张详尽 mapping table（见设计 doc 的 mapping 表），并处理边界/溢出情况（例如 bigints 超过 PG bigint 转 numeric）。
- last_insert_id(): 在执行插入时改写 INSERT 为带 RETURNING 的形式（若可能），或在同一 PG session 内执行 currval 查询后保存于 proxy session，并响应 MySQL 的 SELECT LAST_INSERT_ID()。
- SHOW/DESCRIBE: 以 pg_catalog/information_schema 为数据源，格式化结果以匹配 MySQL 客户端期望列。
- 对于不可翻译/不支持的语法或命令（例如某些存储过程、MyISAM 专有行为、复制命令），代理应返回明确的 MySQL 风格错误并在文档中列出。

测试用例（必需包含在 PR/验收中，示例）：
- Handshake/auth（包括错误凭证/不同 auth 插件/TLS）；
- Simple DML/DQL（SELECT, INSERT, UPDATE, DELETE）；
- Prepared statements binary/text，BLOB，NULL，datetime；
- Transactions（autocommit on/off, savepoints）；
- DDL translation（CREATE TABLE with AUTO_INCREMENT, ENGINE, CHARSET）；
- SHOW / DESCRIBE / INFORMATION_SCHEMA queries；
- Error mapping（duplicate key, FK violation, syntax error）；
- Multi-statement 执行（CLIENT_MULTI_STATEMENTS）；
- Concurrency：并发插入/并发长事务测试，确保 last_insert_id/session isolation correct；
- Fuzzing：发送 malformed / random MySQL packets，proxy 不 crash 并返回安全错误码；
- Performance：压力测试并生成 P50/P95/P99 latency report。

安全要求：
- 最大包限制（configurable），默认值应阻止恶意超大包（例如 16MB 上限，可配置）；
- 单 client 连接/用户/源 IP 的并发连接上限和 QPS 限制；
- 管理 API（metrics/health）必须受限（IP 白名单或 auth）；
- 敏感查询日志支持脱敏配置。

交付物：
- 完整源码与 README、设计文档（protocol mapping、sql rewrite rules、type mapping、error mapping）、tests、Dockerfile、helm chart、运维 runbook、perf report、known incompatibilities list。

请基于以上要求给出：
1) 一个可实现的模块化设计（component diagram + data flow）。  
2) 详细的协议转换伪代码（关键路径：COM_PREPARE->PG Parse, COM_STMT_EXECUTE->Bind/Exec，Resultset mapping）。  
3) 一份完整的测试矩阵（用例 + 预期结果）。  
4) 已知不可实现或仅能部分实现的 MySQL 特性清单（并给出替代策略）。  
5) 基于 Go 的实现建议（关键库、包结构、重要函数/接口签名）。  
6) 部署与监控建议（metrics、日志、alerts）。

按上述要求输出详细设计文档、伪码与测试矩阵。
```

---

# 补充说明与最佳实践建议（工程化经验）

* **分阶段交付**：先实现协议层与基础 COM\_QUERY（只处理简单 SELECT/INSERT/UPDATE/DELETE），再逐步加入 prepared、rewrite、SHOW/DESCRIBE、DDL 翻译与安全策略。每阶段都要上完整的集成测试。
* **使用 AST 驱动的 SQL 重写**：正则替换虽快，但容易出错。采用 parser → AST → rewrite → render 的方式能避免注入/语法歧义。
* **可配置性**：将 rewrite 规则、错误映射、用户映射表做成可热更新的配置（避免频繁重启）。
* **会话持久化**：若要实现代理重启后恢复会话状态（可选），需要更复杂的设计；通常不需要。
* **避免过度日志**：生产日志中不要默认打印 SQL 参数，提供可选的 debug mode。

---

