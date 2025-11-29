# Development and Debugging Tools

This directory contains utility tools for development, debugging, verification, and benchmarking. These are not automated tests but manual tools for developers.

## Tools

### tpcc_benchmark.sh

**Purpose**: TPC-C style OLTP benchmark optimized for aproxy testing

**Status**: ⭐ **Recommended** for aproxy-specific performance testing

**Usage**:
```bash
# Interactive mode
./tools/tpcc_benchmark.sh

# Automated mode
yes | ./tools/tpcc_benchmark.sh
```

**What it does**:
- Sets up TPC-C schema (6 tables: warehouse, customer, item, stock, orders, order_line)
- Loads configurable test data (10 warehouses, 1000 items by default)
- Runs sysbench OLTP benchmark via aproxy
- Executes custom TPC-C workload simulating realistic business transactions
- Measures TPS (transactions per second), latency, and success rate
- Validates Schema Cache performance (99% query reduction target)

**Transaction Mix**:
- New Order (45%): INSERT operations
- Payment (43%): UPDATE operations
- Order Status (4%): SELECT with JOIN
- Stock Level (4%): Aggregate queries

**When to use**:
- ✅ **Recommended**: Daily aproxy performance testing
- Validating Schema Cache effectiveness
- Comparing global cache vs session-level cache performance
- Stress testing with concurrent load
- CI/CD integration
- Before production deployment

**Prerequisites**:
- aproxy running on port 3306
- PostgreSQL accessible on port 5432
- MySQL client installed
- sysbench installed (optional, for sysbench mode)

**Configuration** (edit script):
```bash
WAREHOUSES=10      # Data scale (10 = ~10MB data)
THREADS=10         # Concurrent threads
DURATION=60        # Test duration in seconds
RAMP_TIME=10       # Warm-up time
```

**Performance Targets**:
- **TPS**: > 80 (10 threads), > 500 (100 threads)
- **P95 Latency**: < 50ms (standard), < 100ms (high load)
- **Success Rate**: > 99%

**Output Files**:
- `/tmp/aproxy_benchmark.txt` - Sysbench OLTP results
- `/tmp/tpcc_custom_results.txt` - Custom TPC-C workload results

**Compare Cache Performance**:
```bash
# Disable cache
sed -i '' 's/enabled: true/enabled: false/' configs/config.yaml
make build && killall aproxy; ./bin/aproxy &
yes | ./tools/tpcc_benchmark.sh > /tmp/cache_off.txt

# Enable cache
sed -i '' 's/enabled: false/enabled: true/' configs/config.yaml
make build && killall aproxy; ./bin/aproxy &
yes | ./tools/tpcc_benchmark.sh > /tmp/cache_on.txt

# Compare TPS
grep "TPS" /tmp/cache_off.txt
grep "TPS" /tmp/cache_on.txt
```

**Note**: This benchmark validates the generic sync.Map optimization and global Schema Cache implementation, demonstrating the 99% query reduction and 40% type assertion overhead reduction.

---

### TPC-C with go-tpc (Experimental)

**Purpose**: Industry-standard TPC-C benchmark using PingCAP's go-tpc

**Status**: ⚠️ **Experimental** - Has known limitations with aproxy (see [TPCC_LIMITATIONS.md](../docs/TPCC_LIMITATIONS.md))

**Usage**:
```bash
# Attempt to run go-tpc through aproxy
make tpcc
```

**Known Limitations**:
1. ❌ **TiDB-specific syntax**: `CLUSTERED INDEX` not supported by PostgreSQL
2. ❌ **Parser limitations**: Prepared statements with 100+ placeholders fail
3. ❌ **Complex IN clauses**: > 11 placeholders in IN clause causes parser errors
4. ❌ **Multi-row INSERT**: > 11 rows in single INSERT fails in prepare phase

**Recommended Usage**:
```bash
# Use go-tpc DIRECTLY with PostgreSQL for baseline performance
go-tpc tpcc -d postgres \
    -H localhost -P 5432 \
    -U bast -D test \
    --conn-params "sslmode=disable" \
    --warehouses 4 prepare -T 4

go-tpc tpcc -d postgres \
    -H localhost -P 5432 \
    -U bast -D test \
    --conn-params "sslmode=disable" \
    --warehouses 4 --time 60s run -T 10
```

Then use `tpcc_benchmark.sh` for aproxy-specific testing and compare results.

**Why the limitations?**:
- go-tpc is optimized for TiDB, which uses MySQL protocol but has TiDB-specific extensions
- The TiDB Parser used by aproxy has limitations with extreme prepared statement patterns
- PostgreSQL doesn't support TiDB-specific syntax like `CLUSTERED INDEX`

**See**: [docs/TPCC_LIMITATIONS.md](../docs/TPCC_LIMITATIONS.md) for full details

---

### debug_field_dump.go

**Purpose**: Debug MySQL protocol field packets and row data serialization

**Usage**:
```bash
go run tools/debug_field_dump.go
```

**What it does**:
- Creates MySQL Resultset with sample fields (id, name, price)
- Dumps field definitions in hexadecimal format
- Shows RowData serialization
- Useful for debugging MySQL protocol implementation issues

**When to use**:
- Debugging field packet encoding issues
- Verifying MySQL protocol compatibility
- Understanding field type flags and charsets

**Output example**:
```
=== Field Definitions ===
Field 0: id
  Type: 8 (MYSQL_TYPE_LONGLONG)
  Charset: 63
  ColumnLength: 20
  Flag: 128 (BINARY_FLAG)

=== Field Packets (Hex Dump) ===
Field 0 packet (len=26): 03 64 65 66 ...
```

---

### verify_pg_text_format.go

**Purpose**: Verify PostgreSQL Text Format configuration for pgx driver

**Usage**:
```bash
# Make sure PostgreSQL is running and configured
go run tools/verify_pg_text_format.go
```

**What it does**:
- Connects to PostgreSQL with Simple Query Protocol
- Creates a test table and inserts data
- Verifies field format is Text (Format == 0) not Binary (Format == 1)
- Tests both direct connection and connection pool modes

**When to use**:
- Before deploying AProxy to ensure PostgreSQL is configured correctly
- Debugging type conversion issues (binary vs text format)
- Verifying pgx driver configuration

**Requirements**:
- PostgreSQL server running on `localhost:5432`
- Database `aproxy_test` exists
- User `aproxy_user` with password `aproxy_pass` (or modify connection string)

**Output example**:
```
=== Verify Text Format Configuration ===

Test 1: Direct Simple Query Protocol
DefaultQueryExecMode set to: 0

Field descriptions:
  [0] Name: id, DataTypeOID: 23, Format: 0 (Text Format ✓)
  [1] Name: name, DataTypeOID: 1043, Format: 0 (Text Format ✓)
  [2] Name: price, DataTypeOID: 1700, Format: 0 (Text Format ✓)
```

**Note**: This tool connects directly to PostgreSQL, not through AProxy. It's for verifying the backend database configuration.

---

### debug_resultset.go

**Purpose**: Test the BuildSimpleTextResultset function for MySQL protocol resultset generation

**Usage**:
```bash
go run tools/debug_resultset.go
```

**What it does**:
- Tests BuildSimpleTextResultset with sample data (id, name, price)
- Dumps field definitions and properties
- Shows hexadecimal dump of RowData serialization
- Verifies resultset building logic

**When to use**:
- Debugging BuildSimpleTextResultset function implementation
- Verifying MySQL protocol resultset structure
- Understanding field encoding and row data serialization
- Testing changes to resultset building code

**Output example**:
```
=== Testing BuildSimpleTextResultset ===

Fields:
Field 0: id
  Type: 8 (MYSQL_TYPE_LONGLONG)
  Flag: 128
  Charset: 63

Field 1: name
  Type: 253 (MYSQL_TYPE_VARCHAR)
  Flag: 0
  Charset: 33

=== RowData Hex Dump ===
Row 0 (len=28): 01 31 09 50 72 6f 64 75 63 74 20 31 ...
```

**Note**: This tool is for testing the internal resultset building logic, not for testing AProxy's proxy functionality.

---

## Adding New Tools

When adding new tools to this directory:

1. Use descriptive filenames (e.g., `debug_xxx.go`, `verify_xxx.go`)
2. Add documentation to this README
3. Include usage examples and sample output
4. Add comment headers to explain the tool's purpose
5. Use `package main` and provide a `main()` function

## Why Not Test Cases?

These tools are kept separate from automated tests because:

- **debug_field_dump.go**: Outputs hex dumps that require manual inspection, not suitable for automated assertions
- **verify_pg_text_format.go**: Tests PostgreSQL configuration directly, not AProxy functionality
- **debug_resultset.go**: Outputs hex dumps for manual verification of resultset building logic

For automated testing of AProxy functionality, see `test/integration/` directory.
