# MySQL to PostgreSQL Proxy

A high-performance MySQL protocol proxy that transparently translates MySQL client requests to PostgreSQL backend calls, enabling MySQL clients to access PostgreSQL databases without code modification.

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                        MySQL Clients                                │
│  (Any MySQL client, ORM, or application - no code changes needed)   │
└────────────────────────────┬────────────────────────────────────────┘
                             │ MySQL Protocol (3306)
                             │
┌────────────────────────────▼───────────────────────────────────────┐
│                         AProxy Layer                               │
│ ┌──────────────────────────────────────────────────────────────┐   │
│ │  MySQL Protocol Handler (pkg/protocol/mysql)                 │   │
│ │  - Handshake & Authentication                                │   │
│ │  - COM_QUERY / COM_PREPARE / COM_STMT_EXECUTE                │   │
│ │  - ResultSet Encoding (Field Packets)                        │   │
│ └────────────────────┬─────────────────────────────────────────┘   │
│                      │                                             │
│ ┌────────────────────▼─────────────────────────────────────────┐   │
│ │  SQL Rewrite Engine (pkg/sqlrewrite) - Hybrid AST + String   │   │
│ │  ┌──────────────────────────────────────────────────────┐    │   │
│ │  │ 1. SQL Parser: MySQL SQL → AST                       │    │   │
│ │  └────────────────────┬─────────────────────────────────┘    │   │
│ │  ┌────────────────────▼─────────────────────────────────┐    │   │
│ │  │ 2. AST Visitor: Semantic transformations             │    │   │
│ │  │    - Types: TINYINT→SMALLINT, DATETIME→TIMESTAMP     │    │   │
│ │  │    - Functions: NOW()→CURRENT_TIMESTAMP, IFNULL()    │    │   │
│ │  │    - Constraints: AUTO_INCREMENT→SERIAL, INDEX       │    │   │
│ │  │    - Placeholders: ? → $1, $2, $3...                 │    │   │
│ │  └────────────────────┬─────────────────────────────────┘    │   │
│ │  ┌────────────────────▼─────────────────────────────────┐    │   │
│ │  │ 3. PG Generator: AST → PostgreSQL SQL                │    │   │
│ │  └────────────────────┬─────────────────────────────────┘    │   │
│ │  ┌────────────────────▼─────────────────────────────────┐    │   │
│ │  │ 4. Post-Process: Syntactic cleanup (String-level)    │    │   │
│ │  │    - Quotes: `id` → "id"                             │    │   │
│ │  │    - LIMIT: LIMIT n,m → LIMIT m OFFSET n             │    │   │
│ │  │    - Keywords: CURRENT_TIMESTAMP() → CURRENT_TIMESTAMP│   │   │
│ │  └──────────────────────────────────────────────────────┘    │   │
│ └────────────────────┬─────────────────────────────────────────┘   │
│                      │                                             │
│ ┌────────────────────▼─────────────────────────────────────────┐   │
│ │  Type Mapper (pkg/mapper)                                    │   │
│ │  - MySQL ↔ PostgreSQL data type conversion                   │   │
│ │  - Error code mapping (PostgreSQL → MySQL Error Codes)       │   │
│ │  - SHOW/DESCRIBE command emulation                           │   │
│ └────────────────────┬─────────────────────────────────────────┘   │
│                      │                                             │
│ ┌────────────────────▼─────────────────────────────────────────┐   │
│ │  Session Manager (pkg/session)                               │   │
│ │  - Session state tracking                                    │   │
│ │  - Transaction control (BEGIN/COMMIT/ROLLBACK)               │   │
│ │  - Prepared statement caching                                │   │
│ │  - Session variable management                               │   │
│ └────────────────────┬─────────────────────────────────────────┘   │
│                      │                                             │
│ ┌────────────────────▼─────────────────────────────────────────┐   │
│ │  Schema Cache (pkg/schema) - Global Cache with Generics      │   │
│ │  - AUTO_INCREMENT column detection (schema.table key)        │   │
│ │  - Generic sync.Map (zero type assertion overhead)           │   │
│ │  - TTL-based expiration (5min default, configurable)         │   │
│ │  - DDL auto-invalidation (CREATE/ALTER/DROP TABLE)           │   │
│ │  - 99% query reduction in concurrent scenarios               │   │
│ └────────────────────┬─────────────────────────────────────────┘   │
│                      │                                             │
│ ┌────────────────────▼─────────────────────────────────────────┐   │
│ │  Connection Pool (internal/pool)                             │   │
│ │  - pgx connection pool management                            │   │
│ │  - Session affinity / pooled mode                            │   │
│ │  - Health checks                                             │   │
│ └────────────────────┬─────────────────────────────────────────┘   │
└────────────────────────┼───────────────────────────────────────────┘
                         │ PostgreSQL Protocol (pgx)
                         │
┌────────────────────────▼────────────────────────────────────────────┐
│                   PostgreSQL Database                               │
│  (Actual data storage and query execution)                          │
└─────────────────────────────────────────────────────────────────────┘

                         ┌─────────────────┐
                         │  Observability  │
                         ├─────────────────┤
                         │ Prometheus      │
                         │ (metrics :9090) │
                         ├─────────────────┤
                         │ Logging         │
                         │ (pkg/observ...) │
                         └─────────────────┘
```

### Core Processing Flow

```
MySQL Client Request
      │
      ▼
┌─────────────┐
│ 1. Protocol │  Parse MySQL Wire Protocol packets
│   Parsing   │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 2. SQL      │  Hybrid AST + String Rewriting:
│   Rewrite   │  ① Parse to AST (SQL Parser)
│             │  ② Transform AST (Semantic: types, functions, constraints)
│             │  ③ Generate PostgreSQL SQL
│             │  ④ Post-process (Syntactic: quotes, keywords)
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 3. Execute  │  Execute PostgreSQL query via pgx driver
│   Query     │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 4. Type     │  PostgreSQL types → MySQL types
│   Mapping   │  (BIGSERIAL→BIGINT, BOOLEAN→TINYINT, etc.)
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ 5. Protocol │  Encode as MySQL ResultSet format
│   Encoding  │
└──────┬──────┘
       │
       ▼
MySQL Client Receives Response
```

## 📊 Compatibility Overview

| Category | Support | Test Coverage | Status |
|----------|---------|---------------|--------|
| **SQL Syntax** | 200+ conversion rules | 18 e2e test functions, 50+ subtests | ✅ Production Ready |
| **MySQL Protocol** | 8 core commands + COM_BINLOG_DUMP | Real PG e2e tested | ✅ Fully Compatible |
| **Data Types** | 34 type mappings (AST-level) | All types tested | ✅ Auto Conversion |
| **Functions** | 33 function mappings | All functions tested | ✅ Auto Mapping |
| **Admin Commands** | 42 commands (replication, ACL, variables, backup) | All e2e tested | ✅ Full Coverage |
| **Database Management** | CREATE/DROP DATABASE, USE db | e2e tested | ✅ Schema-based |
| **Unsupported Features** | 28 MySQL-specific features | Auto-detected with suggestions | ⚠️ Documented |

**Overall Compatibility**: Covers **95%+ common MySQL OLTP + DBA management scenarios**.

<details>
<summary><b>📈 Detailed Statistics</b></summary>

### ✅ Supported Capabilities

| Category | Count | Details |
|----------|-------|---------|
| DDL | 11 | CREATE/DROP/ALTER TABLE, CREATE/DROP INDEX, TRUNCATE |
| DML | 9 | SELECT, INSERT, UPDATE, DELETE, REPLACE, ON DUPLICATE KEY |
| Transaction | 7 | BEGIN, COMMIT, ROLLBACK, AUTOCOMMIT, ISOLATION LEVEL |
| Query Syntax | 16 | JOIN, subquery, GROUP BY, HAVING, LIMIT, DISTINCT, UNION, FOR UPDATE, LOCK IN SHARE MODE |
| Data Types | 34 | Integer(10), Float(3), String(6), Binary(4), DateTime(4), Special(3), AUTO_INCREMENT(2), UNSIGNED(2) |
| Functions | 33 | Date/Time(6), String(8), Math(8), Aggregate(6), Conditional(4), JSON(2) |
| Admin Commands | 42 | Replication(15), Variables(30+), ACL(9), Server(8), Backup(5) |
| Database Mgmt | 5 | CREATE/DROP DATABASE, USE db, SET search_path |
| Metadata | 19 | SHOW DATABASES/TABLES/COLUMNS/INDEX/STATUS/VARIABLES/PROCESSLIST etc. |
| Other | 12 | Prepared statements, batch, NULL→DEFAULT, ENGINE/CHARSET removal, ZEROFILL |

**Total: 200+ MySQL→PostgreSQL conversion rules**

### 🧪 Test Coverage

**Unit Tests** (5 packages, all passing):
- `pkg/mapper` - SHOW command emulation, variable mapping
- `pkg/protocol/mysql` - Handler, UseDB, KILL statement
- `pkg/replication` - CDC binlog protocol
- `pkg/schema` - Schema cache
- `pkg/sqlrewrite` - AST rewriter, ACL rewriter, statement parser

**E2E Tests** (18 test functions, 50+ subtests, real PostgreSQL 16):
- `TestReplicationControl` - START/STOP SLAVE, SHOW SLAVE STATUS, RESET MASTER
- `TestShowMasterStatus` / `TestShowBinaryLogs` - Binlog status queries
- `TestGTIDVariables` - SET GLOBAL gtid_purged, SELECT @@gtid_mode
- `TestSemiSync` - SET GLOBAL rpl_semi_sync_*, SHOW GLOBAL STATUS
- `TestReadOnlyControl` - SET GLOBAL read_only/super_read_only + read back
- `TestACL` - CREATE/DROP USER, GRANT, REVOKE, FLUSH PRIVILEGES
- `TestConfigVariables` - server_id, max_connections, wait_timeout, report_host
- `TestServerStatus` - SELECT @@version, SHOW PROCESSLIST
- `TestBinlogManagement` - SET SESSION sql_log_bin, binlog_format, log_bin
- `TestMiscFeatures` - foreign_key_checks, KILL, FLUSH TABLES, TABLESPACE
- `TestShowCommandsAdmin` - 12 SHOW commands
- `TestDDL` - CREATE TABLE (MySQL types), ALTER, INDEX, TRUNCATE
- `TestDMLAndTransactions` - INSERT, UPDATE, DELETE, BEGIN/COMMIT/ROLLBACK
- `TestFunctionConversions` - NOW, IF, IFNULL, CONCAT, RAND, math, string
- `TestQuerySyntax` - LIMIT offset,count, GROUP BY, DISTINCT, UNION, FOR UPDATE
- `TestSwitchoverCommandSequence` - Full master-slave switchover simulation
- `TestCRUD_E2E` - Complete CRUD lifecycle

**Test Environment**: `MySQL Go Client → MyProxy :13306 → PostgreSQL 16 Docker :15432`

### ⚠️ Unsupported MySQL Features (28 patterns)

- **Syntax** (9): DELETE/UPDATE LIMIT, STRAIGHT_JOIN, FORCE/USE/IGNORE INDEX, INSERT DELAYED, PARTITION, VALUES() in UPDATE
- **Functions** (13): FOUND_ROWS(), GET_LOCK(), RELEASE_LOCK(), IS_FREE_LOCK(), DATE_FORMAT(), STR_TO_DATE(), TIMESTAMPDIFF(), FORMAT(), ENCRYPT(), PASSWORD(), INET_ATON(), INET_NTOA(), LOAD_FILE()
- **Data Types** (2): SET, GEOMETRY/SPATIAL types
- **Other** (4): LOAD DATA INFILE, LOCK/UNLOCK TABLES, User variables (@var)

All unsupported features are **automatically detected** at runtime and logged with PostgreSQL alternative suggestions.

### 🎯 Use Cases

✅ **Suitable for MyProxy**:
- OLTP applications (CRUD, transactions, joins, subqueries)
- MySQL master-slave cluster management (switchover, failover, rebuild)
- DBA administration (user management, replication monitoring, server status)
- Fast migration from MySQL to PostgreSQL (zero application code changes)

❌ **Not Suitable for MyProxy**:
- Heavy use of stored procedures and triggers
- MySQL-specific spatial/geometry features
- Applications depending on MySQL SET data type

</details>

## Features

- ✅ **Full MySQL Protocol Support**: Handshake, authentication, queries, prepared statements, etc.
- ✅ **Automatic SQL Rewriting**: Converts MySQL SQL to PostgreSQL-compatible syntax
- ✅ **Session Management**: Complete session state tracking including variables, transactions, prepared statements
- ✅ **Global Schema Cache**: Generic sync.Map-based cache with DDL auto-invalidation (99% query reduction)
- ✅ **Type Mapping**: Automatic conversion between MySQL and PostgreSQL data types
- ✅ **Error Mapping**: Maps PostgreSQL error codes to MySQL error codes
- ✅ **SHOW/DESCRIBE Emulation**: Simulates MySQL metadata commands
- ✅ **Connection Pooling**: Supports session affinity and pooled modes
- ✅ **MySQL CDC (Binlog)**: Stream PostgreSQL changes as MySQL binlog events to MySQL replication clients
- ✅ **Observability**: Prometheus metrics, structured logging, health checks
- ✅ **High Performance**: Target 10,000+ QPS, P99 latency < 50ms
- ✅ **Production Ready**: Docker and Kubernetes deployment support

## Quick Start

### Prerequisites

- Go 1.21+
- PostgreSQL 12+
- Make (optional)

### Build

```bash
# Using Make
make build

# Or directly with Go
GOEXPERIMENT=greenteagc go build -o bin/aproxy ./cmd/aproxy
```

### Configuration

Copy the example configuration file and modify as needed:

```bash
cp configs/config.yaml configs/config.yaml
```

Edit `configs/config.yaml`:

```yaml
server:
  host: "0.0.0.0"
  port: 3306

postgres:
  host: "localhost"
  port: 5432
  database: "mydb"
  user: "postgres"
  password: "your-password"
```

### Run

```bash
# Using Make
make run

# Or run directly
./bin/aproxy -config configs/config.yaml
```

### Connect

Connect using any MySQL client:

```bash
# MySQL CLI
mysql -h 127.0.0.1 -P 3306 -u postgres -p

# Application
# Simply point your MySQL connection string to the proxy address
```

## Docker Deployment

### Build Image

```bash
make docker-build
```

### Run Container

```bash
docker run -d \
  --name aproxy \
  -p 3306:3306 \
  -p 9090:9090 \
  -v $(pwd)/configs/config.yaml:/app/config.yaml \
  aproxy:latest
```

## Kubernetes Deployment

```bash
kubectl apply -f deployments/kubernetes/deployment.yaml
```

## Architecture

```
MySQL Clients → MySQL Protocol → Proxy → PostgreSQL Protocol → PostgreSQL
```

The proxy contains the following components:

1. **MySQL Protocol Handler**: Handles MySQL protocol handshake, authentication, and commands
2. **Session Manager**: Maintains client session state
3. **SQL Rewrite Engine**: Hybrid AST + String architecture using SQL parser for semantic transformations and post-processing for syntactic cleanup
4. **Type Mapper**: Converts between MySQL and PostgreSQL types
5. **Error Mapper**: Maps PostgreSQL errors to MySQL error codes
6. **Schema Cache**: Global cache for table schema information (AUTO_INCREMENT columns) with generic sync.Map and DDL auto-invalidation
7. **Connection Pool**: Manages connections to PostgreSQL

For detailed architecture documentation, see [DESIGN.md](docs/DESIGN.md)

## SQL Rewriting

### Rewriting Architecture

AProxy uses a **hybrid AST + String post-processing architecture** for maximum accuracy and compatibility:

1. **AST Level (Semantic)**: Type conversions, function mappings, constraint handling via SQL parser
2. **String Level (Syntactic)**: Quote conversion, keyword cleanup, formatting adjustments

This architecture ensures column names like `tinyint_col` or `now_timestamp` are handled correctly without unintended replacements.

For detailed analysis, see [AST_VS_STRING_CONVERSION.md](docs/AST_VS_STRING_CONVERSION.md)

### Conversion Rules

The proxy automatically handles the following MySQL to PostgreSQL conversions:

| MySQL                                | PostgreSQL                             | Level  |
| ------------------------------------ | -------------------------------------- | ------ |
| ``` `identifier` ```                 | `"identifier"`                         | String |
| `?` placeholders                     | `$1, $2, ...`                          | AST    |
| `AUTO_INCREMENT`                     | `SERIAL` / `BIGSERIAL`                 | AST    |
| `ENGINE=InnoDB CHARSET=utf8mb4`      | (removed)                              | AST    |
| `INSERT ... ON DUPLICATE KEY UPDATE` | `INSERT ... ON CONFLICT ... DO UPDATE` | AST    |
| `REPLACE INTO`                       | `INSERT ... ON CONFLICT ...`           | AST    |
| `NOW()`                              | `CURRENT_TIMESTAMP`                    | AST    |
| `IFNULL(a, b)`                       | `COALESCE(a, b)`                       | AST    |
| `IF(cond, a, b)`                     | `CASE WHEN cond THEN a ELSE b END`     | String |
| `GROUP_CONCAT()`                     | `STRING_AGG()`                         | AST    |
| `LAST_INSERT_ID()`                   | `lastval()`                            | AST    |
| `LOCK IN SHARE MODE`                 | `FOR SHARE`                            | Parser |
| `LIMIT n, m`                         | `LIMIT m OFFSET n`                     | String |
| `CREATE DATABASE db`                 | `CREATE SCHEMA db`                     | Handler|
| `DROP DATABASE db`                   | `DROP SCHEMA db CASCADE`               | Handler|
| `USE db`                             | `SET search_path TO db`                | Handler|
| `CREATE USER ... IDENTIFIED BY`      | `CREATE ROLE ... WITH LOGIN PASSWORD`  | AST    |
| `GRANT ... ON db.*`                  | `GRANT ... ON ALL TABLES IN SCHEMA`    | AST    |
| `SET GLOBAL read_only = 1`           | `ALTER SYSTEM SET ... + pg_reload`     | AST    |
| `SELECT @@global.xxx`                | Variable mapping table lookup          | AST    |
| `KILL [QUERY] id`                    | `pg_terminate/cancel_backend(id)`      | AST    |
| `START/STOP SLAVE`                   | `pg_wal_replay_resume/pause()`         | Handler|
| `SHOW SLAVE STATUS`                  | `pg_stat_wal_receiver` query           | Handler|

## Supported Commands

### MySQL Protocol Commands
- ✅ COM_QUERY (text protocol queries)
- ✅ COM_PREPARE (prepare statements)
- ✅ COM_STMT_EXECUTE (execute prepared statements)
- ✅ COM_STMT_CLOSE (close prepared statements)
- ✅ COM_FIELD_LIST (field list)
- ✅ COM_PING (ping)
- ✅ COM_QUIT (quit)
- ✅ COM_INIT_DB (change database)

### Metadata Commands

| MySQL | PostgreSQL | Notes |
|-------|-----------|-------|
| `SHOW DATABASES` | `SELECT schema_name FROM information_schema.schemata` | Schema = Database |
| `SHOW TABLES` | `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema()` | |
| `SHOW COLUMNS FROM t` | `SELECT column_name, data_type, ... FROM information_schema.columns` | Type mapping applied |
| `SHOW FULL COLUMNS FROM t` | Same + Collation, Privileges, Comment columns | |
| `SHOW CREATE TABLE t` | Basic structure returned | |
| `SHOW INDEX FROM t` | `SELECT ... FROM pg_index JOIN pg_class JOIN pg_attribute` | |
| `DESCRIBE t` / `DESC t` | `SELECT column_name, data_type, is_nullable, ... FROM information_schema.columns` | |
| `SHOW STATUS` | `SELECT 'Uptime', extract(epoch from (now()-pg_postmaster_start_time()))` | |
| `SHOW VARIABLES` | Static MySQL-compatible variables (version, charset, autocommit, ...) | |
| `SHOW VARIABLES LIKE 'xxx'` | Variable mapping table → fallback `pg_settings` | |
| `SHOW GLOBAL VARIABLES` | Static binlog/replication variables (binlog_format, server_id, ...) | |
| `SHOW GLOBAL VARIABLES LIKE 'xxx'` | Variable mapping table → fallback hardcoded | |
| `SHOW GLOBAL STATUS` | `SELECT 'Uptime', ... UNION SELECT 'rpl_semi_sync_master_status', ...` | |
| `SHOW GLOBAL STATUS WHERE Variable_name='xxx'` | Per-variable PG query via status mapping | |
| `SHOW WARNINGS` | Empty result set | |
| `SET @@var = value` | Store in session variables | |
| `SET GLOBAL var = value` | `ALTER SYSTEM SET pg_var = 'value'; SELECT pg_reload_conf()` | |
| `SET NAMES utf8mb4 COLLATE xxx` | Store charset/collation in session | |
| `USE mydb` | `SET search_path TO mydb` | Runtime dynamic |

### Database Management (MySQL DB = PostgreSQL Schema)

| MySQL | PostgreSQL |
|-------|-----------|
| `CREATE DATABASE mydb` | `CREATE SCHEMA mydb` |
| `CREATE DATABASE IF NOT EXISTS mydb` | `CREATE SCHEMA IF NOT EXISTS mydb` |
| `DROP DATABASE mydb` | `DROP SCHEMA mydb CASCADE` |
| `DROP DATABASE IF EXISTS mydb` | `DROP SCHEMA IF EXISTS mydb CASCADE` |
| `USE mydb` | `SET search_path TO mydb` |

### Replication Management Commands

| MySQL | PostgreSQL |
|-------|-----------|
| `START SLAVE` | `SELECT pg_wal_replay_resume()` |
| `START SLAVE SQL_THREAD` | `SELECT pg_wal_replay_resume()` |
| `STOP SLAVE` | `SELECT pg_wal_replay_pause()` |
| `STOP SLAVE SQL_THREAD` | `SELECT pg_wal_replay_pause()` |
| `STOP SLAVE IO_THREAD` | `SELECT pg_wal_replay_pause()` |
| `CHANGE MASTER TO MASTER_HOST='h', MASTER_PORT=p, MASTER_USER='u', MASTER_PASSWORD='pw'` | `ALTER SYSTEM SET primary_conninfo = 'host=h port=p user=u password=pw'; SELECT pg_reload_conf()` |
| `CHANGE MASTER TO ... MASTER_AUTO_POSITION=1` | Acknowledged (PG uses LSN auto-positioning) |
| `RESET SLAVE` | `ALTER SYSTEM RESET primary_conninfo; SELECT pg_reload_conf()` |
| `RESET MASTER` | `SELECT pg_switch_wal()` |
| `SHOW SLAVE STATUS` | Query `pg_stat_wal_receiver` + `pg_is_wal_replay_paused()` + `pg_last_xact_replay_timestamp()` |
| `SHOW SLAVE STATUS FOR CHANNEL 'ch'` | Same + filter by `slot_name = 'ch'` |
| `SHOW SLAVE HOSTS` | `SELECT pid, client_addr, application_name FROM pg_stat_replication` |
| `SHOW MASTER STATUS` | `SELECT pg_walfile_name(pg_current_wal_lsn()), pg_current_wal_lsn()` |
| `SHOW BINARY LOGS` | `SELECT name, size FROM pg_ls_waldir() ORDER BY name DESC LIMIT 20` |

### Variable Mapping (SET GLOBAL / SELECT @@)

| MySQL Variable | PostgreSQL Equivalent | SET GLOBAL Example |
|---------------|----------------------|-------------------|
| `read_only` | `default_transaction_read_only` | `ALTER SYSTEM SET default_transaction_read_only = 'on'; SELECT pg_reload_conf()` |
| `super_read_only` | `default_transaction_read_only` | Same as read_only |
| `rpl_semi_sync_master_enabled` | `synchronous_commit` | `ALTER SYSTEM SET synchronous_commit = 'on'; SELECT pg_reload_conf()` |
| `rpl_semi_sync_slave_enabled` | NoOp | Returns OK (PG automatic) |
| `rpl_semi_sync_master_status` | `SHOW synchronous_commit` | ON if sync/remote_write/remote_apply |
| `rpl_semi_sync_master_clients` | `SELECT count(*) FROM pg_stat_replication WHERE sync_state = 'sync'` | |
| `max_connections` | `max_connections` | `ALTER SYSTEM SET max_connections = N; SELECT pg_reload_conf()` (needs restart) |
| `wait_timeout = 600` | `idle_in_transaction_session_timeout` | `ALTER SYSTEM SET idle_in_transaction_session_timeout = '600000'` (s→ms) |
| `foreign_key_checks = 0` | `session_replication_role` | `SET session_replication_role = 'replica'` (disables FK) |
| `foreign_key_checks = 1` | `session_replication_role` | `SET session_replication_role = 'origin'` (enables FK) |
| `sql_log_bin = 0` | `log_statement` | `SET log_statement = 'none'` |
| `sql_log_bin = 1` | `log_statement` | `SET log_statement = 'all'` |
| `server_id` / `server_uuid` | Internal storage | Runtime writable, returns stored value |
| `report_host` | Internal storage | Runtime writable |
| `gtid_purged` | NoOp | `SET GLOBAL gtid_purged = ''` returns OK |
| `gtid_mode` | Static `OFF` | PG uses LSN, not GTID |
| `gtid_executed` | Static `''` | |
| `master_auto_position` | Acknowledged | PG always uses LSN auto-positioning |
| `binlog_format` | Static `ROW` | |
| `log_bin` | Static `ON` | |
| `binlog_checksum` | Static `CRC32` | |
| `@@version` | `SHOW server_version` | Returns `16.13-MyProxy` |
| `@@version_comment` | Static | `MyProxy (MySQL to PostgreSQL Proxy)` |
| `character_set_*` | Static `utf8mb4` | PG always uses UTF-8 |
| `collation_*` | Static `utf8mb4_general_ci` | |
| `sql_mode` | Static `TRADITIONAL` | |
| `max_allowed_packet` | Static `67108864` | |

### ACL Management (AST-based via TiDB Parser)

| MySQL | PostgreSQL |
|-------|-----------|
| `CREATE USER 'user'@'host' IDENTIFIED BY 'pass'` | `CREATE ROLE user WITH LOGIN PASSWORD 'pass'` |
| `DROP USER 'user'@'host'` | `DROP ROLE user` |
| `DROP USER IF EXISTS 'user'@'host'` | `DROP ROLE IF EXISTS user` |
| `GRANT SELECT, INSERT ON db.* TO 'user'@'%'` | `GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA db TO user` |
| `GRANT ALL PRIVILEGES ON *.* TO 'user'@'%'` | `GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO user` |
| `GRANT REPLICATION SLAVE ON *.* TO 'user'@'%'` | `ALTER ROLE user REPLICATION` |
| `REVOKE SELECT ON db.* FROM 'user'@'%'` | `REVOKE SELECT ON ALL TABLES IN SCHEMA db FROM user` |
| `REVOKE REPLICATION SLAVE ON *.* FROM 'user'@'%'` | `ALTER ROLE user NOREPLICATION` |
| `FLUSH PRIVILEGES` | NoOp (PG privileges take effect immediately) |

### Server Administration

| MySQL | PostgreSQL |
|-------|-----------|
| `SHOW PROCESSLIST` | `SELECT pid, usename, client_addr, datname, state, query FROM pg_stat_activity` |
| `SHOW FULL PROCESSLIST` | Same as above |
| `KILL 123` | `SELECT pg_terminate_backend(123)` |
| `KILL CONNECTION 123` | `SELECT pg_terminate_backend(123)` |
| `KILL QUERY 123` | `SELECT pg_cancel_backend(123)` |
| `FLUSH TABLES` | Returns OK (PG manages cache automatically) |
| `ALTER TABLE t DISCARD TABLESPACE` | Returns error (not supported in PG) |
| `ALTER TABLE t IMPORT TABLESPACE` | Returns error (not supported in PG) |

### Backup & Restore (API)

| MySQL Tool | PostgreSQL Equivalent | MyProxy API |
|-----------|----------------------|-------------|
| `xtrabackup --backup` | `pg_basebackup -D dir -Fp -Xs` | `BackupManager.PhysicalBackup()` |
| `xtrabackup --prepare` | Not needed (PG backups are immediately usable) | Skipped |
| `xtrabackup --copy-back` | `rsync -a backup/ pgdata/` | `BackupManager.Restore()` |
| `mysqldump` | `pg_dump -f dump.sql` | `BackupManager.LogicalBackup()` |
| `mysql < dump.sql` | `psql -f dump.sql` / `pg_restore` | `BackupManager.LogicalRestore()` |

### SQL Syntax Support

#### DDL (Data Definition Language)

| MySQL | PostgreSQL |
|-------|-----------|
| `CREATE TABLE t (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(100)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4` | `CREATE TABLE "t" ("id" SERIAL PRIMARY KEY, "name" VARCHAR(100))` |
| `CREATE TABLE t (id BIGINT AUTO_INCREMENT PRIMARY KEY)` | `CREATE TABLE "t" ("id" BIGSERIAL PRIMARY KEY)` |
| `CREATE TABLE t (..., INDEX idx_name (name))` | `CREATE TABLE "t" (...)` (inline INDEX removed, use CREATE INDEX separately) |
| `CREATE TABLE t (..., UNIQUE KEY uk_email (email))` | `CREATE TABLE "t" (..., UNIQUE ("email"))` |
| `DROP TABLE t` | `DROP TABLE "t"` |
| `DROP TABLE IF EXISTS t` | `DROP TABLE IF EXISTS "t"` |
| `ALTER TABLE t ADD COLUMN age INT` | `ALTER TABLE "t" ADD COLUMN "age" INT` |
| `ALTER TABLE t DROP COLUMN age` | `ALTER TABLE "t" DROP COLUMN "age"` |
| `CREATE INDEX idx_name ON t(name)` | `CREATE INDEX idx_name ON "t"("name")` |
| `DROP INDEX idx_name` | `DROP INDEX idx_name` |
| `TRUNCATE TABLE t` | `TRUNCATE TABLE "t"` |

#### DML (Data Manipulation Language)

| MySQL | PostgreSQL |
|-------|-----------|
| `SELECT * FROM t WHERE id = ?` | `SELECT * FROM "t" WHERE "id" = $1` |
| `INSERT INTO t (name) VALUES (?)` | `INSERT INTO "t" ("name") VALUES ($1) RETURNING id` (with AUTO_INCREMENT) |
| `INSERT INTO t VALUES (NULL, 'a')` | `INSERT INTO "t" VALUES (DEFAULT, 'a')` (NULL→DEFAULT for SERIAL) |
| `INSERT INTO t (a,b) VALUES (1,2),(3,4)` | `INSERT INTO "t" ("a","b") VALUES ($1,$2),($3,$4)` |
| `UPDATE t SET name = ? WHERE id = ?` | `UPDATE "t" SET "name" = $1 WHERE "id" = $2` |
| `DELETE FROM t WHERE id = ?` | `DELETE FROM "t" WHERE "id" = $1` |
| `REPLACE INTO t (id, name) VALUES (1, 'a')` | `INSERT INTO "t" ("id","name") VALUES ($1,$2) ON CONFLICT DO UPDATE SET ...` |
| `INSERT INTO t ... ON DUPLICATE KEY UPDATE name = VALUES(name)` | `INSERT INTO "t" ... ON CONFLICT ... DO UPDATE SET "name" = EXCLUDED."name"` |
| `SELECT LAST_INSERT_ID()` | `SELECT lastval()` |

#### Transaction Control

| MySQL | PostgreSQL |
|-------|-----------|
| `BEGIN` | `BEGIN` |
| `START TRANSACTION` | `BEGIN` |
| `COMMIT` | `COMMIT` |
| `ROLLBACK` | `ROLLBACK` |
| `SET AUTOCOMMIT = 0` | Proxy manages explicit BEGIN/COMMIT |
| `SET AUTOCOMMIT = 1` | Proxy stops explicit transaction management |
| `SET TRANSACTION ISOLATION LEVEL READ COMMITTED` | `SET TRANSACTION ISOLATION LEVEL READ COMMITTED` |

#### Data Type Conversion

| MySQL Type | PostgreSQL Type | Level |
|-----------|----------------|-------|
| `TINYINT` | `SMALLINT` | AST |
| `TINYINT UNSIGNED` | `SMALLINT` | AST |
| `SMALLINT` | `SMALLINT` | AST |
| `SMALLINT UNSIGNED` | `INTEGER` | AST |
| `MEDIUMINT` | `INTEGER` | AST |
| `INT` / `INTEGER` | `INTEGER` | AST |
| `INT UNSIGNED` | `BIGINT` | AST |
| `BIGINT` | `BIGINT` | AST |
| `BIGINT UNSIGNED` | `NUMERIC(20,0)` | AST |
| `YEAR` | `SMALLINT` | AST |
| `FLOAT` | `REAL` | AST |
| `DOUBLE` | `DOUBLE PRECISION` | String |
| `DECIMAL(M,D)` | `NUMERIC(M,D)` | AST |
| `CHAR(N)` | `CHAR(N)` | Direct |
| `VARCHAR(N)` | `VARCHAR(N)` | Direct |
| `TEXT` | `TEXT` | Direct |
| `TINYTEXT` | `TEXT` | String |
| `MEDIUMTEXT` | `TEXT` | String |
| `LONGTEXT` | `TEXT` | String |
| `BLOB` | `BYTEA` | String |
| `TINYBLOB` | `BYTEA` | AST |
| `MEDIUMBLOB` | `BYTEA` | AST |
| `LONGBLOB` | `BYTEA` | AST |
| `DATE` | `DATE` | Direct |
| `TIME` | `TIME` | Direct |
| `DATETIME` | `TIMESTAMP` | AST |
| `TIMESTAMP` | `TIMESTAMP WITH TIME ZONE` | AST |
| `JSON` | `JSONB` | String |
| `ENUM('a','b','c')` | `VARCHAR(50)` | AST |
| `BOOLEAN` / `TINYINT(1)` | `BOOLEAN` | AST |
| `BIT(N)` | `BIT(N)` | Direct |
| `INT AUTO_INCREMENT` | `SERIAL` | AST+String |
| `BIGINT AUTO_INCREMENT` | `BIGSERIAL` | AST+String |
| `INT UNSIGNED ZEROFILL` | `BIGINT` (ZEROFILL removed) | AST |

#### Function Conversion

| MySQL Function | PostgreSQL Function | Level |
|---------------|-------------------|-------|
| `NOW()` | `CURRENT_TIMESTAMP` | AST |
| `CURDATE()` | `CURRENT_DATE` | AST |
| `CURTIME()` | `CURRENT_TIME` | AST |
| `UNIX_TIMESTAMP()` | `EXTRACT(EPOCH FROM CURRENT_TIMESTAMP)` | AST |
| `FROM_UNIXTIME(ts)` | `TO_TIMESTAMP(ts)` | AST |
| `DATE_FORMAT(d, fmt)` | `TO_CHAR(d, fmt)` | AST |
| `STR_TO_DATE(s, fmt)` | `TO_DATE(s, fmt)` | AST |
| `LAST_INSERT_ID()` | `lastval()` | AST |
| `CONCAT(a, b, ...)` | `CONCAT(a, b, ...)` | Direct |
| `CONCAT_WS(sep, a, b)` | `CONCAT_WS(sep, a, b)` | Direct |
| `LENGTH(s)` | `LENGTH(s)` | Direct |
| `CHAR_LENGTH(s)` | `CHAR_LENGTH(s)` | Direct |
| `SUBSTRING(s, pos, len)` | `SUBSTRING(s, pos, len)` | Direct |
| `UPPER(s)` / `LOWER(s)` | `UPPER(s)` / `LOWER(s)` | Direct |
| `TRIM(s)` / `LTRIM(s)` / `RTRIM(s)` | `TRIM(s)` / `LTRIM(s)` / `RTRIM(s)` | Direct |
| `REPLACE(s, from, to)` | `REPLACE(s, from, to)` | Direct |
| `LOCATE(sub, s)` | `POSITION(sub IN s)` | AST |
| `ABS(n)` / `CEIL(n)` / `FLOOR(n)` / `ROUND(n)` | Same | Direct |
| `MOD(n, m)` | `MOD(n, m)` | Direct |
| `POWER(n, m)` / `POW(n, m)` | `POWER(n, m)` | AST |
| `SQRT(n)` | `SQRT(n)` | Direct |
| `RAND()` | `RANDOM()` | AST |
| `COUNT(*)` / `SUM()` / `AVG()` / `MAX()` / `MIN()` | Same | Direct |
| `GROUP_CONCAT(col SEPARATOR ',')` | `STRING_AGG(col::TEXT, ',')` | AST+String |
| `IF(cond, a, b)` | `CASE WHEN cond THEN a ELSE b END` | String |
| `IFNULL(a, b)` | `COALESCE(a, b)` | AST |
| `NULLIF(a, b)` | `NULLIF(a, b)` | Direct |
| `COALESCE(a, b, ...)` | `COALESCE(a, b, ...)` | Direct |
| `CAST(x AS type)` | `CAST(x AS type)` | Direct |
| `JSON_ARRAY(a, b)` | `JSON_BUILD_ARRAY(a, b)` | AST |
| `JSON_OBJECT(k, v)` | `JSON_BUILD_OBJECT(k, v)` | AST |
| `MATCH(col) AGAINST('term')` | `to_tsvector('simple', col) @@ to_tsquery('simple', 'term')` | String |

#### Query Syntax Conversion

| MySQL | PostgreSQL |
|-------|-----------|
| `` SELECT `id`, `name` FROM `users` `` | `SELECT "id", "name" FROM "users"` |
| `SELECT * FROM t LIMIT 5, 10` | `SELECT * FROM "t" LIMIT 10 OFFSET 5` |
| `SELECT * FROM t LIMIT 10` | `SELECT * FROM "t" LIMIT 10` |
| `SELECT * FROM t FOR UPDATE` | `SELECT * FROM "t" FOR UPDATE` |
| `SELECT * FROM t FOR UPDATE SKIP LOCKED` | `SELECT * FROM "t" FOR UPDATE SKIP LOCKED` |
| `SELECT * FROM t LOCK IN SHARE MODE` | `SELECT * FROM "t" FOR SHARE` |
| `SELECT * FROM a INNER JOIN b ON a.id = b.aid` | `SELECT * FROM "a" INNER JOIN "b" ON "a"."id" = "b"."aid"` |
| `SELECT * FROM a LEFT JOIN b ON a.id = b.aid` | `SELECT * FROM "a" LEFT JOIN "b" ON "a"."id" = "b"."aid"` |
| `SELECT * FROM a RIGHT JOIN b ON a.id = b.aid` | `SELECT * FROM "a" RIGHT JOIN "b" ON "a"."id" = "b"."aid"` |
| `SELECT * FROM t WHERE id IN (SELECT aid FROM b)` | `SELECT * FROM "t" WHERE "id" IN (SELECT "aid" FROM "b")` |
| `SELECT * FROM t WHERE EXISTS (SELECT 1 FROM b)` | `SELECT * FROM "t" WHERE EXISTS (SELECT 1 FROM "b")` |
| `SELECT name, COUNT(*) FROM t GROUP BY name HAVING COUNT(*) > 1` | `SELECT "name", COUNT(*) FROM "t" GROUP BY "name" HAVING COUNT(*) > 1` |
| `SELECT DISTINCT name FROM t` | `SELECT DISTINCT "name" FROM "t"` |
| `SELECT name FROM t1 UNION ALL SELECT name FROM t2` | `SELECT "name" FROM "t1" UNION ALL SELECT "name" FROM "t2"` |
| `SELECT * FROM t WHERE id = ? AND name = ?` | `SELECT * FROM "t" WHERE "id" = $1 AND "name" = $2` |
| `_UTF8MB4'text'` | `'text'` (charset prefix removed) |

#### Other Features

| MySQL | PostgreSQL | Notes |
|-------|-----------|-------|
| Prepared Statements (`COM_PREPARE` / `COM_STMT_EXECUTE`) | `$1, $2, ...` placeholders | Binary protocol supported |
| Batch INSERT `VALUES (...),(...),...` | Same | Direct pass-through |
| `NULL` in SERIAL column | `DEFAULT` | Auto-converted for AUTO_INCREMENT columns |
| `PRIMARY KEY` | `PRIMARY KEY` | Direct |
| `UNIQUE KEY uk_name (col)` | `UNIQUE ("col")` | Constraint name removed |
| `INDEX idx_name (col)` | Removed from CREATE TABLE | Must use `CREATE INDEX` separately |
| `ENGINE=InnoDB` | Removed | PG has no storage engine concept |
| `DEFAULT CHARSET=utf8mb4` | Removed | PG always uses UTF-8 |
| `AUTO_INCREMENT=100` (table option) | Removed | PG SERIAL manages its own sequence |
| `UNSIGNED` | Type promoted (INT→BIGINT) | AST-level |
| `ZEROFILL` | Removed | AST-level |
| `SIGNED` keyword | Removed | String-level |

## CDC (Change Data Capture)

AProxy supports streaming PostgreSQL changes as MySQL binlog events, enabling MySQL replication clients (like Canal, Debezium, go-mysql) to subscribe to PostgreSQL data changes.

### CDC Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                    MySQL Replication Clients                            │
│           (Canal / Debezium / go-mysql / Custom Clients)                │
└────────────────────────────┬────────────────────────────────────────────┘
                             │ MySQL Binlog Protocol (COM_BINLOG_DUMP)
                             │
┌────────────────────────────▼────────────────────────────────────────────┐
│                         AProxy CDC Server                               │
│ ┌─────────────────────────────────────────────────────────────────────┐ │
│ │  Binlog Encoder (pkg/replication/binlog_encoder.go)                 │ │
│ │  - TableMapEvent encoding (column metadata)                         │ │
│ │  - RowsEvent encoding (INSERT/UPDATE/DELETE)                        │ │
│ │  - QueryEvent encoding (DDL/TRUNCATE)                               │ │
│ │  - GTIDEvent encoding (transaction tracking)                        │ │
│ │  - DECIMAL/TIME/DATETIME binary format encoding                     │ │
│ └────────────────────┬────────────────────────────────────────────────┘ │
│                      │                                                   │
│ ┌────────────────────▼────────────────────────────────────────────────┐ │
│ │  Replication Server (pkg/replication/server.go)                     │ │
│ │  - MySQL binlog protocol server                                     │ │
│ │  - Multi-client support (COM_BINLOG_DUMP)                           │ │
│ │  - GTID-based positioning                                           │ │
│ │  - Event broadcasting to all connected clients                      │ │
│ └────────────────────┬────────────────────────────────────────────────┘ │
│                      │                                                   │
│ ┌────────────────────▼────────────────────────────────────────────────┐ │
│ │  PG Streamer (pkg/replication/pg_streamer.go)                       │ │
│ │  - PostgreSQL logical replication (pglogrepl)                       │ │
│ │  - Automatic REPLICA IDENTITY FULL setting                          │ │
│ │  - LSN checkpoint persistence (atomic file writes)                  │ │
│ │  - Auto-reconnect with exponential backoff                          │ │
│ │  - TOAST unchanged column handling                                   │ │
│ │  - 30+ PostgreSQL type mappings                                     │ │
│ └────────────────────┬────────────────────────────────────────────────┘ │
│                      │ PostgreSQL Logical Replication                   │
└──────────────────────┼──────────────────────────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────────────────────────┐
│                   PostgreSQL Database                                    │
│  - Logical replication slot (pgoutput plugin)                            │
│  - Publication for table filtering                                       │
└─────────────────────────────────────────────────────────────────────────┘
```

### CDC Event Flow

```
PostgreSQL WAL Change
        │
        ▼
┌──────────────────┐
│ 1. PG Streamer   │  Receive logical replication message
│    (pglogrepl)   │  Parse: INSERT/UPDATE/DELETE/TRUNCATE
└────────┬─────────┘
         │
         ▼
┌──────────────────┐
│ 2. Type Convert  │  PostgreSQL types → MySQL types
│                  │  (int4→INT, text→VARCHAR, etc.)
└────────┬─────────┘
         │
         ▼
┌──────────────────┐
│ 3. Binlog Encode │  Create MySQL binlog events:
│                  │  - GTIDEvent (transaction ID)
│                  │  - TableMapEvent (schema)
│                  │  - WriteRowsEvent / UpdateRowsEvent / DeleteRowsEvent
└────────┬─────────┘
         │
         ▼
┌──────────────────┐
│ 4. Broadcast     │  Send to all connected
│                  │  MySQL replication clients
└──────────────────┘
```

### CDC Configuration

Add the following to `configs/config.yaml`:

```yaml
cdc:
  enabled: true                              # Enable CDC server
  server_id: 1                               # MySQL server ID for replication

  # PostgreSQL connection for logical replication
  pg_host: "localhost"
  pg_port: 5432
  pg_database: "mydb"
  pg_user: "postgres"
  pg_password: "password"
  pg_slot_name: "aproxy_cdc"                 # Replication slot name
  pg_publication_name: "aproxy_pub"          # Publication name

  # Checkpoint persistence for crash recovery
  checkpoint_file: "./data/cdc_checkpoint.json"
  checkpoint_interval: 10s

  # Auto-reconnect on connection loss
  reconnect_enabled: true
  reconnect_max_retries: 0                   # 0 = unlimited
  reconnect_initial_wait: 1s
  reconnect_max_wait: 30s                    # Exponential backoff cap

  # Backpressure handling
  backpressure_timeout: 30m                  # Max wait when channel full
```

### PostgreSQL Setup

```sql
-- 1. Create publication for tables you want to replicate
CREATE PUBLICATION aproxy_pub FOR ALL TABLES;

-- Or for specific tables:
CREATE PUBLICATION aproxy_pub FOR TABLE users, orders, products;

-- 2. Create replication slot (optional, AProxy creates automatically)
SELECT pg_create_logical_replication_slot('aproxy_cdc', 'pgoutput');
```

### Usage with Canal

```go
import "github.com/go-mysql-org/go-mysql/canal"

cfg := canal.NewDefaultConfig()
cfg.Addr = "127.0.0.1:3306"
cfg.User = "root"
cfg.Flavor = "mysql"

c, _ := canal.NewCanal(cfg)
c.SetEventHandler(&MyEventHandler{})
c.Run()
```

### CDC Metrics

CDC exposes the following Prometheus metrics:

| Metric | Description |
|--------|-------------|
| `mysql_pg_proxy_cdc_events_total` | Total events by type (insert/update/delete/truncate) |
| `mysql_pg_proxy_cdc_replication_lag_ms` | Current replication lag in milliseconds |
| `mysql_pg_proxy_cdc_backpressure_total` | Backpressure events (channel full) |
| `mysql_pg_proxy_cdc_connected_clients` | Connected binlog dump clients |
| `mysql_pg_proxy_cdc_last_lsn` | Last processed PostgreSQL LSN |
| `mysql_pg_proxy_cdc_reconnects_total` | PostgreSQL reconnection attempts |
| `mysql_pg_proxy_cdc_events_dropped_total` | Events dropped due to timeout |

### Supported CDC Features

- ✅ **DML Events**: INSERT, UPDATE, DELETE with full row data
- ✅ **DDL Events**: TRUNCATE TABLE
- ✅ **GTID Support**: Transaction tracking with MySQL GTID format
- ✅ **Multi-client**: Multiple replication clients simultaneously
- ✅ **Crash Recovery**: LSN checkpoint persistence
- ✅ **Auto-reconnect**: Exponential backoff on connection loss
- ✅ **Type Mapping**: 30+ PostgreSQL to MySQL type conversions
- ✅ **TOAST Handling**: Unchanged large column support

## Monitoring

### Prometheus Metrics

The proxy exposes the following metrics at `:9090/metrics`:

- `mysql_pg_proxy_active_connections` - Active connections
- `mysql_pg_proxy_total_queries` - Total queries
- `mysql_pg_proxy_query_duration_seconds` - Query latency histogram
- `mysql_pg_proxy_errors_total` - Error counts
- `mysql_pg_proxy_pg_pool_size` - PostgreSQL connection pool size

### Health Checks

```bash
curl http://localhost:9090/health
```

## Performance

Target performance metrics:

- **Throughput**: 10,000+ QPS (per instance)
- **Latency**: P99 < 50ms (excluding network)
- **Connections**: 1,000+ concurrent connections
- **Memory**: < 100MB base + ~1MB/connection

## Testing

```bash
# Run all tests
make test

# Unit tests only
make test-unit

# Integration tests only
make test-integration

# Performance tests
make bench
```

### Test Coverage Details

AProxy includes **69 integration test cases** covering common MySQL syntax and operation scenarios.

<details>
<summary><b>📋 Basic Functionality Tests (46 cases)</b></summary>

#### Basic Queries
- SELECT 1
- SELECT NOW()

#### Table Operations
- Create table with AUTO_INCREMENT
- Insert single row
- Select inserted data
- Update row
- Delete row
- Verify final count

#### Prepared Statements
- Prepare and execute
- Verify inserted data

#### Transactions
- Commit transaction
- Rollback transaction

#### Metadata Commands
- SHOW DATABASES (logical database names)
- SHOW TABLES

#### Data Type Tests
- **Integer types**: Create table with integer types, Insert integer values, Select and verify integer values
- **Floating-point types**: Create table with floating point types, Insert and verify floating point values
- **String types**: Create table with string types, Insert and verify string values
- **Date/time types**: Create table with datetime types, Insert and verify datetime values

#### Aggregate Functions
- COUNT
- SUM
- AVG
- MAX
- MIN

#### JOIN Queries
- INNER JOIN
- LEFT JOIN

#### Subqueries
- Subquery with IN
- Subquery in SELECT

#### Grouping and Sorting
- GROUP BY with aggregates
- GROUP BY with HAVING
- LIMIT only
- LIMIT with OFFSET (MySQL syntax)

#### NULL Value Handling
- Insert NULL values
- Query NULL values
- IFNULL function

#### Batch Operations
- Batch insert
- Batch update
- Batch delete

#### Indexes and Constraints
- Create table with indexes
- Insert and query with indexes
- Unique constraint violation

#### Concurrent Testing
- Multiple concurrent queries

</details>

<details>
<summary><b>🎓 Student Management Scenario Tests (21 cases)</b></summary>

#### Table Management
- Create student table
- Insert 100 student records
- Query student data
- Update student data
- Delete student data

#### Aggregation and Complex Queries
- Aggregate query - statistics by grade
- Complex query - combined conditions

#### Transaction Scenarios
- Transaction commit - credit transfer
- Transaction rollback - invalid transfer
- Explicit transaction control - BEGIN/COMMIT
- Explicit transaction control - BEGIN/ROLLBACK
- START TRANSACTION syntax

#### Autocommit
- Disable autocommit and manual commit
- Enable autocommit

#### SQL Rewriting
- Data type conversion
- Function conversion (NOW(), CURDATE(), etc.)
- LIMIT syntax conversion
- Backtick conversion

#### Concurrent Scenarios
- Concurrent transfers (10 concurrent transactions)

#### Complex Business Scenarios
- Complex transaction - student course enrollment
- JOIN query - student enrollment information

</details>

<details>
<summary><b>🔄 MySQL Compatibility Tests (2 cases)</b></summary>

- COMMIT transaction
- ROLLBACK transaction

</details>

### Unsupported MySQL Features

The following MySQL features are not supported in PostgreSQL or require rewriting:

<details>
<summary><b>🚫 Completely Unsupported Features</b></summary>

#### Storage Engine Related
- MyISAM/InnoDB specific features
- FULLTEXT indexes (use PostgreSQL full-text search instead)
- SPATIAL indexes (use PostGIS instead)

#### Replication and High Availability
- ~~Binary Log~~ → ✅ Supported via CDC (PostgreSQL logical replication → MySQL binlog)
- ~~GTID (Global Transaction ID)~~ → ✅ Supported via CDC
- Master-Slave replication commands (CHANGE MASTER TO, START/STOP SLAVE)

#### Data Types
- ENUM (use custom types or CHECK constraints)
- SET (use arrays or many-to-many tables)
- YEAR type (use INTEGER or DATE)
- Integer display width like INT(11)
- UNSIGNED modifier

#### Special Syntax
- Stored procedure language (needs rewriting to PL/pgSQL)
- Trigger syntax differences
- Event Scheduler (use pg_cron)
- User variables (@variables)
- LOAD DATA INFILE (use COPY FROM)

#### Function Differences
- DATE_FORMAT() (convert to TO_CHAR)
- FOUND_ROWS()
- GET_LOCK()/RELEASE_LOCK() (use pg_advisory_lock)

</details>

For a detailed list of unsupported features and alternatives, see [PG_UNSUPPORTED_FEATURES.md](docs/PG_UNSUPPORTED_FEATURES.md)

## Known Limitations

### Unsupportable Features

1. **Storage Engine Specific**: MyISAM/InnoDB specific behaviors
2. **Replication**: ~~Binary logs, GTID~~ ✅ Now supported via CDC; master-slave admin commands still unsupported
3. **MySQL-Specific Syntax**: Some stored procedures, triggers, event syntax

### Features Requiring Migration

1. **Stored Procedures**: Need rewriting to PL/pgSQL
2. **Triggers**: Need rewriting to PostgreSQL syntax
3. **Full-Text Search**: Different syntax and functionality

For a detailed list of limitations, see [DESIGN.md](docs/DESIGN.md)

## Documentation

- [**MySQL Compatibility List**](docs/COMPATIBILITY.md) - **Complete list of supported and unsupported MySQL features**
- [Quick Start Guide](docs/QUICKSTART.md) - Quick deployment and usage tutorial
- [Design Document](docs/DESIGN.md) - Architecture design and technical decisions
- [Operations Manual](docs/RUNBOOK.md) - Deployment, configuration, and troubleshooting
- [Implementation Summary](docs/IMPLEMENTATION_SUMMARY.md) - Feature specifications and implementation details
- [AST vs String Conversion Analysis](docs/AST_VS_STRING_CONVERSION.md) - **SQL rewriting architecture analysis**
- [MySQL Protocol Technical Notes](docs/MYSQL_PROTOCOL_NOTES.md) - MySQL/PostgreSQL protocol implementation notes
- [PostgreSQL Unsupported Features](docs/PG_UNSUPPORTED_FEATURES.md) - MySQL feature compatibility checklist
- [Test Organization Strategy](docs/TEST_ORGANIZATION.md) - Test case classification and organization
- [MySQL Test Coverage](docs/mysql_test_coverage.md) - Test case coverage details
- [MySQL to PG Cases](docs/MYSQL_TO_PG_CASES.md) - SQL conversion examples
- [Regex Optimization](docs/regex_optimization.md) - SQL rewriting performance optimization

## Configuration Options

| Option                           | Description                     | Default                        |
| -------------------------------- | ------------------------------- | ------------------------------ |
| `server.port`                    | MySQL listen port               | 3306                           |
| `server.max_connections`         | Max connections                 | 1000                           |
| `postgres.connection_mode`       | Connection mode                 | session_affinity               |
| `sql_rewrite.enabled`            | Enable SQL rewrite              | true                           |
| `schema_cache.enabled`           | Enable global schema cache      | true                           |
| `schema_cache.ttl`               | Cache TTL                       | 5m                             |
| `schema_cache.max_entries`       | Max cache entries               | 100000                         |
| `schema_cache.invalidate_on_ddl` | Auto-invalidate on DDL          | true                           |
| `database_mapping.default_schema` | Default PostgreSQL schema for sessions without a logical database | public |
| `database_mapping.fallback_to_public` | Append `public` to `search_path` compatibility fallback | false |
| `cdc.enabled`                    | Enable CDC server               | false                          |
| `cdc.checkpoint_file`            | LSN checkpoint file             | ./data/cdc_checkpoint.json     |
| `cdc.reconnect_enabled`          | Auto-reconnect on connection loss | true                         |
| `observability.log_level`        | Log level                       | info                           |

### Schema Mapping Semantics

- `postgres.database` remains the fixed PostgreSQL physical database for the proxy.
- In `session_affinity` mode, a MySQL logical database maps to PostgreSQL schema state via `USE db` / `COM_INIT_DB`.
- `SHOW DATABASES` returns logical database names, not PostgreSQL physical database names.
- `database_mapping.fallback_to_public` defaults to `false`, so strict mode does not silently fall back to `public`.

For complete configuration options, see [config.yaml](configs/config.yaml)

## Contributing

Issues and Pull Requests are welcome!

## License

Apache License 2.0 - See [LICENSE](LICENSE) file for details

## Related Projects

- [go-mysql](https://github.com/go-mysql-org/go-mysql) - MySQL protocol implementation
- [pgx](https://github.com/jackc/pgx) - PostgreSQL driver
- [TiDB Parser](https://github.com/pingcap/parser) - MySQL SQL parser
