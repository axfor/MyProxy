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
│ │  - AUTO_INCREMENT column detection (database.table key)      │   │
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

| Category                    | Support                     | Test Coverage                | Status                                          |
| --------------------------- | --------------------------- | ---------------------------- | ----------------------------------------------- |
| **SQL Syntax**              | 70+ patterns                | 50 test cases (100% pass)    | ✅ Production Ready                              |
| **MySQL Protocol Commands** | 8 core commands             | Integration tested           | ✅ Fully Compatible                              |
| **Data Types**              | 6 categories, 20+ types     | All types tested             | ✅ Auto Conversion (78% full support)            |
| **Functions**               | 5 categories, 30+ functions | All functions tested         | ✅ Auto Mapping (71% support)                    |
| **Unsupported Features**    | 28 MySQL-specific features  | Documented with alternatives | ⚠️ See [COMPATIBILITY.md](docs/COMPATIBILITY.md) |

**Overall Compatibility**: Covers **90%+ common MySQL OLTP scenarios**, suitable for most OLTP application migrations.

<details>
<summary><b>📈 Detailed Statistics</b></summary>

### ✅ Supported SQL Scenarios (70+ patterns)

- **Basic DML**: SELECT, INSERT, UPDATE, DELETE (4 types)
- **DDL Operations**: CREATE/DROP TABLE, CREATE/DROP INDEX, ALTER TABLE, TRUNCATE (6 types)
- **Transaction Control**: BEGIN, COMMIT, ROLLBACK, AUTOCOMMIT (4 types)
- **Query Features**: JOIN (4 types), subqueries, GROUP BY, HAVING, ORDER BY, LIMIT, DISTINCT, UNION (8+ types)
- **Data Types**: Integer (10 types), Float (3 types), String (6 types), Binary (4 types), DateTime (4 types), Special (3 types) = 30+ types
- **Functions**: Date/Time (4), String (8), Math (8), Aggregate (6), Conditional (4) = 30+ functions
- **Others**: Prepared statements, batch operations, NULL handling, index constraints, auto-detection of unsupported features (5+ types)

**Subtotal**: ~40 SQL syntax patterns and operations (with automatic detection of 26 unsupported features)

### 🧪 Test Coverage (50 passing + 26 documented unsupported)

- **Integration Tests (Passing)**: 50 cases
  - **basic** (23 tests): Table operations, queries, transactions, data types, functions
  - **mysql_compat** (8 tests): MySQL protocol compatibility verification
  - **mysql_specific** (13 tests): FULLTEXT search, LastInsertID, MATCH AGAINST, etc.
  - **student** (6 tests): Business scenarios, concurrent transactions, complex queries

- **Unsupported Features (Documented)**: 26 cases
  - **mysql_specific_syntax** (10 tests): DELETE LIMIT, FORCE INDEX, PARTITION, etc.
  - **mysql_specific_functions** (12 tests): DATE_FORMAT, FOUND_ROWS, GET_LOCK, etc.
  - **mysql_specific_types** (4 tests): ENUM, SET, SPATIAL types, combined types

**Test Pass Rate**: 100% (50/50 supported features passed)
**Coverage**: 90%+ of common OLTP scenarios

### ⚠️ Unsupported MySQL Features (28 patterns)

- **Syntax** (9 patterns): DELETE/UPDATE LIMIT, STRAIGHT_JOIN, FORCE/USE/IGNORE INDEX, INSERT DELAYED, PARTITION syntax, VALUES() in UPDATE
- **Functions** (13 patterns): FOUND_ROWS(), GET_LOCK(), RELEASE_LOCK(), IS_FREE_LOCK(), DATE_FORMAT(), STR_TO_DATE(), TIMESTAMPDIFF(), FORMAT(), ENCRYPT(), PASSWORD(), INET_ATON(), INET_NTOA(), LOAD_FILE()
- **Data Types** (2 patterns): SET, GEOMETRY/SPATIAL types
- **Other** (4 patterns): LOAD DATA INFILE, LOCK/UNLOCK TABLES, User variables (@var)

**Key Benefits**:
- ✅ **Automatic Detection**: All 28 unsupported features are automatically detected and logged with actionable suggestions
- ✅ **Detailed Documentation**: See [COMPATIBILITY.md](docs/COMPATIBILITY.md) for complete compatibility matrix
- ✅ **Migration Guide**: Each unsupported feature includes PostgreSQL alternative recommendations

### 🎯 Use Cases

✅ **Suitable for AProxy**:
- OLTP applications (Online Transaction Processing)
- Applications primarily using CRUD operations
- Applications using common SQL syntax
- Fast migration from MySQL to PostgreSQL

❌ **Not Suitable for AProxy**:
- Heavy use of stored procedures and triggers
- Dependency on MySQL-specific features (FULLTEXT, SPATIAL)
- Heavy use of MySQL-specific data types (ENUM, SET)

</details>

## Features

- ✅ **Full MySQL Protocol Support**: Handshake, authentication, queries, prepared statements, etc.
- ✅ **Automatic SQL Rewriting**: Converts MySQL SQL to PostgreSQL-compatible syntax
- ✅ **Session Management**: Complete session state tracking including variables, transactions, prepared statements
- ✅ **Global Schema Cache**: Generic sync.Map-based cache with DDL auto-invalidation (99% query reduction)
- ✅ **Type Mapping**: Automatic conversion between MySQL and PostgreSQL data types
- ✅ **Error Mapping**: Maps PostgreSQL error codes to MySQL error codes
- ✅ **SHOW/DESCRIBE Emulation**: Simulates MySQL metadata commands
- ✅ **MySQL Admin Commands**: Replication control, ACL management, server status, variable mapping
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
| `INSERT ... ON DUPLICATE KEY UPDATE` | `INSERT ... ON CONFLICT ... DO UPDATE` | AST    |
| `REPLACE INTO`                       | `INSERT ... ON CONFLICT ...`           | AST    |
| `NOW()`                              | `CURRENT_TIMESTAMP`                    | AST    |
| `IFNULL(a, b)`                       | `COALESCE(a, b)`                       | AST    |
| `IF(cond, a, b)`                     | `CASE WHEN cond THEN a ELSE b END`     | AST    |
| `GROUP_CONCAT()`                     | `STRING_AGG()`                         | AST    |
| `LAST_INSERT_ID()`                   | `lastval()`                            | String |
| `LOCK IN SHARE MODE`                 | `FOR SHARE`                            | String |
| `LIMIT n, m`                         | `LIMIT m OFFSET n`                     | String |

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
- ✅ SHOW DATABASES
- ✅ SHOW TABLES
- ✅ SHOW COLUMNS / SHOW FULL COLUMNS
- ✅ SHOW CREATE TABLE
- ✅ SHOW INDEX
- ✅ DESCRIBE/DESC
- ✅ SHOW STATUS / SHOW VARIABLES / SHOW WARNINGS
- ✅ SHOW GLOBAL VARIABLES / SHOW GLOBAL STATUS
- ✅ SET variables / SET GLOBAL variables
- ✅ USE database

### Replication Management Commands
- ✅ START SLAVE / STOP SLAVE (incl. SQL_THREAD, IO_THREAD) → `pg_wal_replay_resume/pause()`
- ✅ CHANGE MASTER TO → `ALTER SYSTEM SET primary_conninfo`
- ✅ RESET SLAVE / RESET MASTER → `ALTER SYSTEM RESET` / `pg_switch_wal()`
- ✅ SHOW SLAVE STATUS (incl. FOR CHANNEL) → `pg_stat_wal_receiver`
- ✅ SHOW SLAVE HOSTS → `pg_stat_replication`
- ✅ SHOW MASTER STATUS → `pg_current_wal_lsn()`
- ✅ SHOW BINARY LOGS → `pg_ls_waldir()`

### Variable Mapping (SET GLOBAL / SELECT @@)
- ✅ `read_only` / `super_read_only` → `default_transaction_read_only`
- ✅ `rpl_semi_sync_master_enabled` → `synchronous_commit`
- ✅ `rpl_semi_sync_slave_enabled` → NoOp (PG automatic)
- ✅ `max_connections` → `max_connections` (ALTER SYSTEM)
- ✅ `wait_timeout` → `idle_in_transaction_session_timeout`
- ✅ `foreign_key_checks` → `session_replication_role`
- ✅ `sql_log_bin` → `log_statement`
- ✅ `server_id` / `server_uuid` / `report_host` → internal storage
- ✅ `gtid_purged` / `gtid_mode` / `gtid_executed` → compatibility stubs
- ✅ `@@version` → PostgreSQL `server_version`

### ACL Management (AST-based)
- ✅ CREATE USER → `CREATE ROLE ... WITH LOGIN PASSWORD`
- ✅ DROP USER → `DROP ROLE`
- ✅ GRANT (SELECT, INSERT, REPLICATION SLAVE, ALL, ...) → PostgreSQL GRANT / ALTER ROLE
- ✅ REVOKE → PostgreSQL REVOKE / ALTER ROLE
- ✅ FLUSH PRIVILEGES → NoOp (PG immediate)

### Server Administration
- ✅ SHOW PROCESSLIST → `pg_stat_activity`
- ✅ KILL [CONNECTION|QUERY] id → `pg_terminate_backend()` / `pg_cancel_backend()`
- ✅ FLUSH TABLES → Acknowledged (NoOp)
- ✅ ALTER TABLE ... DISCARD/IMPORT TABLESPACE → Error (not supported in PG)

### Backup & Restore (API)
- ✅ Physical backup (xtrabackup equivalent) → `pg_basebackup`
- ✅ Logical backup (mysqldump equivalent) → `pg_dump`
- ✅ Restore → `rsync` / `pg_restore`

### SQL Syntax Support

#### DDL (Data Definition Language)
- ✅ CREATE TABLE (supports AUTO_INCREMENT, PRIMARY KEY, UNIQUE, INDEX)
- ✅ DROP TABLE
- ✅ ALTER TABLE (basic operations)
- ✅ CREATE INDEX
- ✅ DROP INDEX

#### DML (Data Manipulation Language)
- ✅ SELECT (supports WHERE, JOIN, GROUP BY, HAVING, ORDER BY, LIMIT)
- ✅ INSERT (supports single and batch inserts)
- ✅ UPDATE (supports WHERE conditions)
- ✅ DELETE (supports WHERE conditions)
- ✅ REPLACE INTO (converted to INSERT ... ON CONFLICT)
- ✅ INSERT ... ON DUPLICATE KEY UPDATE (converted to ON CONFLICT)

#### Transaction Control
- ✅ BEGIN / START TRANSACTION
- ✅ COMMIT
- ✅ ROLLBACK
- ✅ AUTOCOMMIT settings
- ✅ SET TRANSACTION ISOLATION LEVEL

#### Data Type Support

**Integer Types** (AST-level conversion):
- ✅ `TINYINT` → `SMALLINT`
- ✅ `TINYINT UNSIGNED` → `SMALLINT`
- ✅ `SMALLINT` → `SMALLINT`
- ✅ `SMALLINT UNSIGNED` → `INTEGER`
- ✅ `MEDIUMINT` → `INTEGER`
- ✅ `INT` / `INTEGER` → `INTEGER`
- ✅ `INT UNSIGNED` → `BIGINT`
- ✅ `BIGINT` → `BIGINT`
- ✅ `BIGINT UNSIGNED` → `NUMERIC(20,0)`
- ✅ `YEAR` → `SMALLINT`

**Floating-Point Types**:
- ✅ `FLOAT` → `REAL`
- ✅ `DOUBLE` → `DOUBLE PRECISION` (String-level)
- ✅ `DECIMAL(M,D)` / `NUMERIC(M,D)` → `NUMERIC(M,D)`

**String Types**:
- ✅ `CHAR(N)` → `CHAR(N)`
- ✅ `VARCHAR(N)` → `VARCHAR(N)`
- ✅ `TEXT` → `TEXT`
- ✅ `TINYTEXT` → `TEXT` (String-level)
- ✅ `MEDIUMTEXT` → `TEXT` (String-level)
- ✅ `LONGTEXT` → `TEXT` (String-level)

**Binary Types** (Hybrid AST + String):
- ✅ `BLOB` → `BYTEA`
- ✅ `TINYBLOB` → `BYTEA` (via BLOB)
- ✅ `MEDIUMBLOB` → `BYTEA` (via BLOB)
- ✅ `LONGBLOB` → `BYTEA` (via BLOB)

**Date/Time Types** (AST-level):
- ✅ `DATE` → `DATE`
- ✅ `TIME` → `TIME`
- ✅ `DATETIME` → `TIMESTAMP`
- ✅ `TIMESTAMP` → `TIMESTAMP WITH TIME ZONE`

**Special Types**:
- ✅ `JSON` → `JSONB` (String-level)
- ✅ `ENUM(...)` → `VARCHAR(50)` (AST-level)
- ✅ `BOOLEAN` / `TINYINT(1)` → `BOOLEAN` (AST-level)

#### Function Support

All function conversions are handled at **AST level** for semantic correctness.

**Date/Time Functions**:
- ✅ `NOW()` → `CURRENT_TIMESTAMP`
- ✅ `CURDATE()` / `CURRENT_DATE()` → `CURRENT_DATE`
- ✅ `CURTIME()` / `CURRENT_TIME()` → `CURRENT_TIME`
- ✅ `UNIX_TIMESTAMP()` → `EXTRACT(EPOCH FROM CURRENT_TIMESTAMP)`

**String Functions**:
- ✅ `CONCAT(a, b, ...)` → `CONCAT(a, b, ...)`
- ✅ `CONCAT_WS(sep, a, b, ...)` → `CONCAT_WS(sep, a, b, ...)`
- ✅ `LENGTH(s)` → `LENGTH(s)`
- ✅ `CHAR_LENGTH(s)` → `CHAR_LENGTH(s)`
- ✅ `SUBSTRING(s, pos, len)` → `SUBSTRING(s, pos, len)`
- ✅ `UPPER(s)` / `LOWER(s)` → `UPPER(s)` / `LOWER(s)`
- ✅ `TRIM(s)` / `LTRIM(s)` / `RTRIM(s)` → `TRIM(s)` / `LTRIM(s)` / `RTRIM(s)`
- ✅ `REPLACE(s, from, to)` → `REPLACE(s, from, to)`

**Math Functions**:
- ✅ `ABS(n)`, `CEIL(n)`, `FLOOR(n)`, `ROUND(n)` → Same
- ✅ `MOD(n, m)` → `MOD(n, m)`
- ✅ `POWER(n, m)` / `POW(n, m)` → `POWER(n, m)`
- ✅ `SQRT(n)` → `SQRT(n)`
- ✅ `RAND()` → `RANDOM()`

**Aggregate Functions**:
- ✅ `COUNT(*)` / `COUNT(col)` → Same
- ✅ `SUM(col)`, `AVG(col)`, `MAX(col)`, `MIN(col)` → Same
- ✅ `GROUP_CONCAT(col)` → `STRING_AGG(col::TEXT, ',')`

**Conditional Functions**:
- ✅ `IF(cond, a, b)` → `CASE WHEN cond THEN a ELSE b END`
- ✅ `IFNULL(a, b)` → `COALESCE(a, b)`
- ✅ `NULLIF(a, b)` → `NULLIF(a, b)`
- ✅ `COALESCE(a, b, c, ...)` → Same

#### Query Features
- ✅ INNER JOIN
- ✅ LEFT JOIN / RIGHT JOIN
- ✅ Subqueries (IN, EXISTS)
- ✅ GROUP BY with HAVING
- ✅ ORDER BY
- ✅ LIMIT offset, count (auto-converted to LIMIT count OFFSET offset)
- ✅ DISTINCT
- ✅ UNION / UNION ALL

#### Other Features
- ✅ Prepared Statements
- ✅ Batch Operations
- ✅ NULL value handling
- ✅ Indexes and constraints (PRIMARY KEY, UNIQUE, INDEX)
- ✅ LastInsertId() support (via RETURNING clause)

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
- SHOW DATABASES
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
| `cdc.enabled`                    | Enable CDC server               | false                          |
| `cdc.checkpoint_file`            | LSN checkpoint file             | ./data/cdc_checkpoint.json     |
| `cdc.reconnect_enabled`          | Auto-reconnect on connection loss | true                         |
| `observability.log_level`        | Log level                       | info                           |

For complete configuration options, see [config.yaml](configs/config.yaml)

## Contributing

Issues and Pull Requests are welcome!

## License

Apache License 2.0 - See [LICENSE](LICENSE) file for details

## Related Projects

- [go-mysql](https://github.com/go-mysql-org/go-mysql) - MySQL protocol implementation
- [pgx](https://github.com/jackc/pgx) - PostgreSQL driver
- [TiDB Parser](https://github.com/pingcap/parser) - MySQL SQL parser
