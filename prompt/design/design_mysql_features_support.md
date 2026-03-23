# MyProxy MySQL 特性全量支持设计方案

基于 `MySQL特性使用分析.md` 中列出的所有命令，对照 MyProxy 当前实现状态，制定分阶段实现方案。

---

## 一、现状总览（已全部实现 ✅）

| 类别 | 需求总数 | 已支持 | 实现文件 |
|------|---------|--------|---------|
| 主从复制命令 | 7 | 7 ✅ | pkg/admin/replication.go |
| 复制状态查询 | 2 | 2 ✅ | pkg/mapper/show_admin.go |
| GTID 复制 | 3 | 3 ✅ | pkg/mapper/variables.go (gtid_purged/gtid_mode/gtid_executed) |
| 半同步复制 | 5 | 5 ✅ | pkg/mapper/variables.go + show_admin.go |
| 只读/可写控制 | 4 | 4 ✅ | pkg/mapper/variables.go |
| 用户权限管理 | 5 | 5 ✅ | pkg/sqlrewrite/rewrite_acl.go (AST) |
| 备份恢复 | 3 | 3 ✅ | pkg/admin/backup.go |
| 配置管理 | 6 | 6 ✅ | pkg/mapper/variables.go (server_id/report_host/max_connections 等) |
| 服务器状态查询 | 5 | 5 ✅ | pkg/mapper/variables.go + show_admin.go + handler.go |
| Binlog 管理 | 3 | 3 ✅ | pkg/mapper/variables.go + show_admin.go + replication.go |
| 其他特性 | 4 | 4 ✅ | handler.go (TABLESPACE/FLUSH/foreign_key_checks/KILL) |
| 数据库管理 | 3 | 3 ✅ | handler.go (CREATE/DROP DATABASE→SCHEMA, USE db→search_path) |
| 会话命令 | 2 | 2 ✅ | show.go (SET NAMES, ensurePGConn search_path) |
| 变量查询统一 | 2 | 2 ✅ | show.go (SHOW [GLOBAL] VARIABLES LIKE → 映射表优先查找) |

---

## 二、分阶段实现计划

### Phase 1: 核心管理命令（优先级：高）

这些命令是 MySQL 主从管理系统的核心，直接对应 PostgreSQL 流复制管理。

#### 1.1 复制控制命令 → PostgreSQL 流复制映射

| MySQL 命令 | PostgreSQL 等价实现 | 实现方式 |
|------------|-------------------|---------|
| `START SLAVE` | `SELECT pg_wal_replay_resume()` | 拦截转发 |
| `STOP SLAVE` | `SELECT pg_wal_replay_pause()` | 拦截转发 |
| `START SLAVE SQL_THREAD` | `SELECT pg_wal_replay_resume()` | 拦截转发（PG 不区分 IO/SQL 线程） |
| `STOP SLAVE SQL_THREAD` | `SELECT pg_wal_replay_pause()` | 拦截转发 |
| `STOP SLAVE IO_THREAD` | 修改 `primary_conninfo` + reload | 拦截 + 系统命令 |
| `CHANGE MASTER TO` | 修改 `primary_conninfo` in `postgresql.auto.conf` + restart | 拦截 + 配置管理 |
| `RESET SLAVE` | `SELECT pg_replication_origin_drop(...)` + 清理 slot | 拦截转发 |

**实现位置**: `pkg/protocol/mysql/handler.go` 新增 `handleReplicationCommand()`

**设计要点**:
- 在 SQL 解析层识别 `START/STOP SLAVE`、`CHANGE MASTER TO`、`RESET SLAVE`
- 新建 `pkg/replication/admin.go` 封装 PostgreSQL 复制管理操作
- `CHANGE MASTER TO` 参数映射:
  ```
  MASTER_HOST       → primary_conninfo 中的 host
  MASTER_PORT       → primary_conninfo 中的 port
  MASTER_USER       → primary_conninfo 中的 user
  MASTER_PASSWORD   → primary_conninfo 中的 password
  MASTER_AUTO_POSITION=1 → 使用 WAL LSN 自动定位（默认行为）
  ```

#### 1.2 复制状态查询

| MySQL 命令 | PostgreSQL 等价实现 | 实现方式 |
|------------|-------------------|---------|
| `SHOW SLAVE STATUS` | `pg_stat_replication` + `pg_stat_wal_receiver` | SHOW 命令拦截 |
| `SHOW SLAVE STATUS FOR CHANNEL 'xxx'` | 同上，增加 slot_name 过滤 | SHOW 命令拦截 |
| `SHOW SLAVE HOSTS` | `pg_stat_replication` 查询所有复制连接 | SHOW 命令拦截 |

**实现位置**: `pkg/mapper/show.go` 新增 case

**SHOW SLAVE STATUS 字段映射**:
```
Slave_IO_Running     ← receiver_status (pg_stat_wal_receiver)
Slave_SQL_Running    ← pg_is_in_recovery() && !pg_is_wal_replay_paused()
Master_Host          ← conninfo 解析
Master_Port          ← conninfo 解析
Master_Log_File      ← sender_flush_location (pg_stat_wal_receiver)
Read_Master_Log_Pos  ← received_lsn
Exec_Master_Log_Pos  ← pg_last_wal_replay_lsn()
Seconds_Behind_Master ← EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp()))
Relay_Log_File       ← pg_walfile_name(pg_last_wal_receive_lsn())
Last_Error           ← last_msg_send_time 判断
Channel_Name         ← slot_name
Retrieved_Gtid_Set   ← received_lsn (格式化为 GTID 样式)
Executed_Gtid_Set    ← pg_last_wal_replay_lsn()
```

#### 1.3 GTID 复制

| MySQL 特性 | PostgreSQL 等价实现 | 实现方式 |
|-----------|-------------------|---------|
| `master_auto_position=1` | PG 流复制默认基于 LSN 自动定位 | 直接兼容 |
| `SET GLOBAL GTID_PURGED` | 不需要（PG WAL 自管理） | 返回 OK |
| GTID 模式 | LSN 模式（自动） | 透明映射 |

**设计要点**:
- MySQL GTID 格式 `server_uuid:transaction_id` → 映射为 `LSN` 的字符串表示
- 提供 GTID ↔ LSN 转换工具函数在 `pkg/replication/gtid.go`

---

### Phase 2: 半同步复制与读写控制（优先级：高）

#### 2.1 半同步复制参数

| MySQL 参数/命令 | PostgreSQL 等价 | 实现方式 |
|----------------|----------------|---------|
| `SET GLOBAL rpl_semi_sync_master_enabled = 1` | `ALTER SYSTEM SET synchronous_commit = 'on'` | SET 拦截 |
| `SET GLOBAL rpl_semi_sync_slave_enabled = 1` | 从库无需设置（PG 自动） | 返回 OK |
| `SHOW GLOBAL STATUS ... rpl_semi_sync_master_status` | `SHOW synchronous_commit` | SHOW 拦截 |
| `SHOW GLOBAL STATUS ... rpl_semi_sync_master_clients` | `SELECT count(*) FROM pg_stat_replication WHERE sync_state = 'sync'` | SHOW 拦截 |
| `SHOW GLOBAL VARIABLES ... rpl_semi_sync_master_enabled` | `SHOW synchronous_commit` | SHOW 拦截 |

**实现位置**: `pkg/mapper/show.go` 扩展变量映射表

**设计要点**:
- 新建变量映射表 `mysqlVarToPgVar` 在 `pkg/mapper/variables.go`
- 支持 `SET GLOBAL` → `ALTER SYSTEM SET` + `SELECT pg_reload_conf()` 的转换链
- 半同步 ↔ synchronous_commit 映射:
  ```
  semi_sync enabled  → synchronous_commit = 'on' (等待 WAL flush)
  semi_sync disabled → synchronous_commit = 'local'
  ```

#### 2.2 只读/可写控制

| MySQL 命令 | PostgreSQL 等价 | 实现方式 |
|------------|----------------|---------|
| `SET GLOBAL read_only = 1` | `ALTER SYSTEM SET default_transaction_read_only = 'on'` + reload | SET 拦截 |
| `SET GLOBAL super_read_only = 1` | 同上（PG 不区分 super） | SET 拦截 |
| `SELECT @@global.read_only` | `SHOW default_transaction_read_only` | SELECT 拦截 |
| `SELECT @@global.super_read_only` | `SHOW default_transaction_read_only` | SELECT 拦截 |

**实现位置**: `pkg/mapper/show.go` 中 SET 处理扩展

---

### Phase 3: 用户权限管理（优先级：中）

#### 3.1 用户管理命令

| MySQL 命令 | PostgreSQL 等价 | 实现方式 |
|------------|----------------|---------|
| `CREATE USER 'user'@'host' IDENTIFIED BY 'pass'` | `CREATE ROLE user WITH LOGIN PASSWORD 'pass'` | SQL 重写 |
| `DROP USER 'user'@'host'` | `DROP ROLE user` | SQL 重写 |
| `GRANT priv ON db.* TO user` | `GRANT priv ON SCHEMA db TO user` | SQL 重写 |
| `REVOKE priv ON db.* FROM user` | `REVOKE priv ON SCHEMA db FROM user` | SQL 重写 |
| `FLUSH PRIVILEGES` | 无需操作（PG 即时生效） | 返回 OK |

**实现位置**: `pkg/sqlrewrite/` 新增 `rewrite_acl.go`

**权限映射表**:
```
REPLICATION CLIENT  → pg_monitor
REPLICATION SLAVE   → REPLICATION (角色属性)
PROCESS             → pg_read_all_stats
FILE                → 无直接等价（PG 使用 COPY）
CREATE USER         → CREATEROLE (角色属性)
RELOAD              → pg_reload_conf() 权限
SHOW DATABASES      → CONNECT（默认所有用户都有）
ALL PRIVILEGES      → ALL PRIVILEGES
SELECT/INSERT/UPDATE/DELETE → 直接映射
```

**设计要点**:
- MySQL `'user'@'host'` 模式 → PG `pg_hba.conf` + 角色分离
- host 限制通过 `pg_hba.conf` 管理，需要 `pkg/admin/hba.go` 工具
- `GRANT REPLICATION SLAVE` → `ALTER ROLE user REPLICATION`

---

### Phase 4: Binlog 与服务器管理（优先级：中）

#### 4.1 Binlog 管理

| MySQL 命令 | PostgreSQL 等价 | 实现方式 |
|------------|----------------|---------|
| `SET SESSION sql_log_bin = 0` | `SET LOCAL log_statement = 'none'`（近似） | SET 拦截 |
| `SET SESSION sql_log_bin = 1` | `RESET log_statement` | SET 拦截 |
| `RESET MASTER` | `SELECT pg_switch_wal()` + 清理旧 WAL | 拦截转发 |
| `SHOW MASTER STATUS` | `SELECT pg_current_wal_lsn(), pg_walfile_name(pg_current_wal_lsn())` | SHOW 拦截 |
| `SHOW BINARY LOGS` | `SELECT * FROM pg_ls_waldir()` | SHOW 拦截 |

**实现位置**: `pkg/mapper/show.go` 扩展

#### 4.2 服务器状态查询扩展

| MySQL 命令 | PostgreSQL 等价 | 当前状态 |
|------------|----------------|---------|
| `SELECT @@version` | `SHOW server_version` | ✅ 已支持 |
| `SELECT @@global.server_id` | 返回配置的 server_id | ⚠️ 需扩展 |
| `SHOW PROCESSLIST` | `SELECT * FROM pg_stat_activity` | ❌ 需实现 |
| `KILL CONNECTION @conn_id` | `SELECT pg_terminate_backend(pid)` | ❌ 需实现 |

**SHOW PROCESSLIST 字段映射**:
```
Id        ← pid
User      ← usename
Host      ← client_addr || ':' || client_port
db        ← datname
Command   ← state → MySQL command 映射
Time      ← EXTRACT(EPOCH FROM (now() - query_start))
State     ← wait_event_type || ': ' || wait_event
Info      ← query
```

#### 4.3 配置管理命令

| MySQL 命令 | PostgreSQL 等价 | 实现方式 |
|------------|----------------|---------|
| `SET GLOBAL max_connections = N` | `ALTER SYSTEM SET max_connections = N` + reload | SET 拦截 |
| `SET GLOBAL wait_timeout = N` | `ALTER SYSTEM SET idle_in_transaction_session_timeout = N` | SET 拦截 |
| `SET GLOBAL server_id = N` | 存储在 MyProxy 内部配置 | SET 拦截 |

---

### Phase 5: 备份恢复与高级特性（优先级：低）

#### 5.1 备份恢复

| MySQL 工具/命令 | PostgreSQL 等价 | 实现方式 |
|----------------|----------------|---------|
| xtrabackup --backup | `pg_basebackup` | 外部命令封装 |
| xtrabackup --prepare | 不需要（PG 备份即可用） | 跳过 |
| xtrabackup --copy-back | 直接拷贝 PGDATA | 外部命令封装 |
| mysqldump | `pg_dump` | 外部命令封装 |

**设计要点**:
- 备份恢复不走 SQL 协议，需要独立的管理 API
- 新建 `pkg/admin/backup.go` 封装 `pg_basebackup` 调用
- 通过 REST API 或管理端口暴露备份/恢复操作

#### 5.2 表空间与杂项

| MySQL 命令 | PostgreSQL 等价 | 实现方式 |
|------------|----------------|---------|
| `ALTER TABLE t DISCARD TABLESPACE` | 无直接等价 | 返回错误/忽略 |
| `ALTER TABLE t IMPORT TABLESPACE` | 无直接等价 | 返回错误/忽略 |
| `SET SESSION foreign_key_checks = 0` | `SET session_replication_role = 'replica'` | SET 拦截 |
| `SET SESSION foreign_key_checks = 1` | `SET session_replication_role = 'origin'` | SET 拦截 |
| `FLUSH TABLES` | 无直接等价（PG 自动管理） | 返回 OK |

---

## 三、架构设计

### 3.1 新增模块

```
pkg/
├── admin/                    # 新增：管理操作模块
│   ├── replication.go        # 复制管理（START/STOP SLAVE, CHANGE MASTER）
│   ├── hba.go                # pg_hba.conf 管理（host 权限控制）
│   └── backup.go             # 备份恢复封装
├── mapper/
│   ├── show.go               # 扩展：新增 SHOW SLAVE STATUS 等
│   ├── variables.go          # 新增：MySQL ↔ PG 变量映射表
│   └── errors.go             # 现有
├── sqlrewrite/
│   ├── rewrite_acl.go        # 新增：用户权限 SQL 重写
│   └── rewrite_replication.go # 新增：复制命令重写
├── replication/
│   ├── gtid.go               # 新增：GTID ↔ LSN 转换
│   └── admin.go              # 新增：复制管理 API
└── protocol/mysql/
    └── handler.go            # 扩展：新增命令路由
```

### 3.2 命令路由扩展

在 `handler.go` 的 `handleQuery()` 中新增命令检测链：

```go
func (h *Handler) handleQuery(sql string) error {
    // 现有检测
    if IsShowStatement(sql)  { return h.handleShowCommand(sql) }
    if IsSetStatement(sql)   { return h.handleSetCommand(sql) }
    if IsUseStatement(sql)   { return h.handleUseCommand(sql) }

    // 新增检测
    if IsReplicationCommand(sql) { return h.handleReplicationCommand(sql) }  // Phase 1
    if IsACLCommand(sql)         { return h.handleACLCommand(sql) }          // Phase 3
    if IsKillCommand(sql)        { return h.handleKillCommand(sql) }         // Phase 4
    if IsFlushCommand(sql)       { return h.handleFlushCommand(sql) }        // Phase 4

    // 标准 SQL 重写流程
    return h.executeRewrittenSQL(sql)
}
```

### 3.3 变量映射表设计

```go
// pkg/mapper/variables.go
var mysqlToPostgresVars = map[string]VarMapping{
    "read_only":                       {PGVar: "default_transaction_read_only", Scope: "global"},
    "super_read_only":                 {PGVar: "default_transaction_read_only", Scope: "global"},
    "max_connections":                 {PGVar: "max_connections", Scope: "global", NeedRestart: true},
    "wait_timeout":                    {PGVar: "idle_in_transaction_session_timeout", Scope: "global"},
    "rpl_semi_sync_master_enabled":    {PGVar: "synchronous_commit", Scope: "global", Transform: semiSyncTransform},
    "rpl_semi_sync_slave_enabled":     {PGVar: "", Scope: "global", NoOp: true},
    "foreign_key_checks":             {PGVar: "session_replication_role", Scope: "session", Transform: fkCheckTransform},
    "sql_log_bin":                     {PGVar: "log_statement", Scope: "session", Transform: logBinTransform},
    "server_id":                       {PGVar: "", Scope: "internal"},
}
```

---

## 四、实现状态（全部已完成 ✅）

| 阶段 | 内容 | 状态 | 实现文件 |
|------|------|------|---------|
| **Phase 1** | 复制控制 + 状态查询 + GTID | ✅ 已完成 | pkg/admin/replication.go, pkg/mapper/show_admin.go, pkg/mapper/variables.go |
| **Phase 2** | 半同步 + 读写控制 | ✅ 已完成 | pkg/mapper/variables.go (变量映射表) |
| **Phase 3** | 用户权限管理 (AST) | ✅ 已完成 | pkg/sqlrewrite/rewrite_acl.go (TiDB AST) |
| **Phase 4** | Binlog/进程/配置管理 | ✅ 已完成 | pkg/mapper/show_admin.go, handler.go |
| **Phase 5** | 备份恢复 + 杂项 | ✅ 已完成 | pkg/admin/backup.go, handler.go |
| **追加** | 数据库管理 (CREATE/DROP DB) | ✅ 已完成 | handler.go (→ CREATE/DROP SCHEMA) |
| **追加** | SET NAMES + search_path 统一 | ✅ 已完成 | show.go, handler.go (ensurePGConn) |
| **追加** | SHOW VARIABLES LIKE 映射表优先 | ✅ 已完成 | show.go (GetMySQLVarValue 优先查找) |

---

## 五、测试状态（全部通过 ✅）

所有测试统一在 `test/integration/admin_cmd_test.go`，使用 Docker PostgreSQL 16 + MySQL Go Client 进行真实 e2e 测试：

```
test/integration/admin_cmd_test.go    # 18 个测试函数, 50+ 子测试
├── TestReplicationControl            # START/STOP SLAVE, SHOW SLAVE STATUS, RESET MASTER
├── TestShowMasterStatus              # SHOW MASTER STATUS
├── TestShowBinaryLogs                # SHOW BINARY LOGS
├── TestGTIDVariables                 # SET GLOBAL gtid_purged, SELECT @@gtid_mode
├── TestSemiSync                      # SET GLOBAL rpl_semi_sync_*, SHOW GLOBAL STATUS
├── TestReadOnlyControl               # SET GLOBAL read_only/super_read_only + read back
├── TestACL                           # CREATE/DROP USER, GRANT, REVOKE, FLUSH PRIVILEGES
├── TestConfigVariables               # server_id, max_connections, wait_timeout, report_host
├── TestServerStatus                  # SELECT @@version, SHOW PROCESSLIST
├── TestBinlogManagement              # SET SESSION sql_log_bin, binlog_format, log_bin
├── TestMiscFeatures                  # foreign_key_checks, KILL, FLUSH TABLES, TABLESPACE
├── TestShowCommandsAdmin             # 12 SHOW commands
├── TestDDL                           # CREATE TABLE (MySQL types), ALTER, INDEX, TRUNCATE
├── TestDMLAndTransactions            # INSERT, UPDATE, DELETE, BEGIN/COMMIT/ROLLBACK
├── TestFunctionConversions           # NOW, IF, IFNULL, CONCAT, RAND, math, string
├── TestQuerySyntax                   # LIMIT offset,count, GROUP BY, DISTINCT, UNION, FOR UPDATE
├── TestSwitchoverCommandSequence     # Full switchover simulation
└── TestCRUD_E2E                      # Complete CRUD lifecycle
```

测试链路: `MySQL Go Client → MyProxy :13306 → PostgreSQL 16 Docker :15432`
测试 schema: 每次创建独立 `e2e_testdb`，不使用 public schema
