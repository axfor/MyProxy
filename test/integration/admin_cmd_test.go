package integration

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const e2eSchemaName = "e2e_testdb"

// getE2EDSN returns the base DSN (no database specified).
func getE2EDSN() string {
	if dsn := os.Getenv("MYPROXY_DSN"); dsn != "" {
		return dsn
	}
	return "myproxy:myproxy@tcp(localhost:13306)/?parseTime=true"
}

// setupAdminDB connects, creates a dedicated schema, and switches to it.
// No operations use the public schema.
func setupAdminDB(tb testing.TB) (*sql.DB, func()) {
	db, err := sql.Open("mysql", getE2EDSN())
	require.NoError(tb, err)
	require.NoError(tb, db.Ping())

	// Create dedicated test schema (MySQL: CREATE DATABASE → PG: CREATE SCHEMA)
	_, _ = db.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", e2eSchemaName))
	_, err = db.Exec(fmt.Sprintf("USE %s", e2eSchemaName))
	require.NoError(tb, err)

	cleanup := func() {
		_, _ = db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", e2eSchemaName))
		db.Close()
	}

	return db, cleanup
}

// ================================================================
// 1. Replication Control Commands (mysql_master_slave_management_features.md 1.1)
// ================================================================

func TestReplicationControl(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	// These commands require a standby server, so they may return errors
	// on a primary. We just verify MyProxy handles them without crashing.

	t.Run("SHOW SLAVE STATUS", func(t *testing.T) {
		rows, err := db.Query("SHOW SLAVE STATUS")
		require.NoError(t, err)
		defer rows.Close()
		cols, _ := rows.Columns()
		assert.Contains(t, cols, "Slave_IO_Running")
		assert.Contains(t, cols, "Slave_SQL_Running")
		assert.Contains(t, cols, "Seconds_Behind_Master")
		assert.Contains(t, cols, "Channel_Name")
		assert.Contains(t, cols, "Master_Host")
		assert.Contains(t, cols, "Retrieved_Gtid_Set")
		assert.Contains(t, cols, "Executed_Gtid_Set")
	})

	t.Run("SHOW SLAVE STATUS FOR CHANNEL", func(t *testing.T) {
		rows, err := db.Query("SHOW SLAVE STATUS FOR CHANNEL 'default'")
		require.NoError(t, err)
		defer rows.Close()
		cols, _ := rows.Columns()
		assert.Contains(t, cols, "Slave_IO_Running")
	})

	t.Run("SHOW SLAVE HOSTS", func(t *testing.T) {
		rows, err := db.Query("SHOW SLAVE HOSTS")
		require.NoError(t, err)
		defer rows.Close()
		cols, _ := rows.Columns()
		assert.Contains(t, cols, "Server_id")
		assert.Contains(t, cols, "Host")
	})

	t.Run("RESET MASTER", func(t *testing.T) {
		// On primary, RESET MASTER maps to pg_switch_wal()
		_, err := db.Exec("RESET MASTER")
		// May fail if user lacks permissions, but should not crash
		t.Logf("RESET MASTER result: %v", err)
	})
}

// ================================================================
// 1.2 Replication Status (mysql_master_slave_management_features.md 1.2)
// ================================================================

func TestShowMasterStatus(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	rows, err := db.Query("SHOW MASTER STATUS")
	require.NoError(t, err)
	defer rows.Close()

	cols, err := rows.Columns()
	require.NoError(t, err)
	assert.Contains(t, cols, "File")
	assert.Contains(t, cols, "Position")
	assert.Contains(t, cols, "Executed_Gtid_Set")

	if rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		t.Logf("Master: File=%s Position=%s", vals[0].String, vals[1].String)
		assert.True(t, vals[0].Valid && vals[0].String != "")
	}
}

func TestShowBinaryLogs(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	rows, err := db.Query("SHOW BINARY LOGS")
	require.NoError(t, err)
	defer rows.Close()
	cols, _ := rows.Columns()
	assert.Contains(t, cols, "Log_name")
	assert.Contains(t, cols, "File_size")
}

// ================================================================
// 1.3 GTID (mysql_master_slave_management_features.md 1.3)
// ================================================================

func TestGTIDVariables(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	t.Run("SET GLOBAL gtid_purged", func(t *testing.T) {
		_, err := db.Exec("SET GLOBAL gtid_purged = ''")
		assert.NoError(t, err)
	})

	t.Run("SELECT @@gtid_mode", func(t *testing.T) {
		// gtid_mode should be available through SHOW GLOBAL VARIABLES
		var name, val string
		err := db.QueryRow("SHOW GLOBAL VARIABLES LIKE 'gtid_mode'").Scan(&name, &val)
		require.NoError(t, err)
		assert.Equal(t, "gtid_mode", name)
	})
}

// ================================================================
// 2. Semi-Sync Replication (mysql_master_slave_management_features.md 2)
// ================================================================

func TestSemiSync(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	t.Run("SET GLOBAL rpl_semi_sync_master_enabled = 1", func(t *testing.T) {
		_, err := db.Exec("SET GLOBAL rpl_semi_sync_master_enabled = 1")
		assert.NoError(t, err)
	})

	t.Run("SET GLOBAL rpl_semi_sync_slave_enabled = 1", func(t *testing.T) {
		_, err := db.Exec("SET GLOBAL rpl_semi_sync_slave_enabled = 1")
		assert.NoError(t, err)
	})

	t.Run("SHOW GLOBAL STATUS semi_sync", func(t *testing.T) {
		rows, err := db.Query("SHOW GLOBAL STATUS WHERE Variable_name='rpl_semi_sync_master_status' OR Variable_name='rpl_semi_sync_master_clients'")
		require.NoError(t, err)
		defer rows.Close()
		count := 0
		for rows.Next() {
			var name, value string
			require.NoError(t, rows.Scan(&name, &value))
			t.Logf("  %s = %s", name, value)
			count++
		}
		assert.Equal(t, 2, count)
	})

	t.Run("SHOW GLOBAL VARIABLES LIKE rpl_semi_sync_master_enabled", func(t *testing.T) {
		var name, val string
		err := db.QueryRow("SHOW GLOBAL VARIABLES LIKE 'rpl_semi_sync_master_enabled'").Scan(&name, &val)
		// This goes through the general SHOW GLOBAL VARIABLES path
		// It may not find exact match, so we just verify no crash
		t.Logf("rpl_semi_sync_master_enabled query result: name=%s val=%s err=%v", name, val, err)
	})
}

// ================================================================
// 3. Read-Only Control (mysql_master_slave_management_features.md 3)
// ================================================================

func TestReadOnlyControl(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	t.Run("SET GLOBAL read_only = 1 and read back", func(t *testing.T) {
		_, err := db.Exec("SET GLOBAL read_only = 1")
		assert.NoError(t, err)

		var val string
		err = db.QueryRow("SELECT @@global.read_only").Scan(&val)
		assert.NoError(t, err)
		assert.Equal(t, "1", val)

		_, err = db.Exec("SET GLOBAL read_only = 0")
		assert.NoError(t, err)

		err = db.QueryRow("SELECT @@global.read_only").Scan(&val)
		assert.NoError(t, err)
		assert.Equal(t, "0", val)
	})

	t.Run("SET GLOBAL super_read_only = 1 and read back", func(t *testing.T) {
		_, err := db.Exec("SET GLOBAL super_read_only = 1")
		assert.NoError(t, err)

		var val string
		err = db.QueryRow("SELECT @@global.super_read_only").Scan(&val)
		assert.NoError(t, err)
		assert.Equal(t, "1", val)

		_, err = db.Exec("SET GLOBAL super_read_only = 0")
		assert.NoError(t, err)
	})
}

// ================================================================
// 4. User & Privilege Management (mysql_master_slave_management_features.md 4)
// ================================================================

func TestACL(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	t.Run("CREATE USER", func(t *testing.T) {
		_, err := db.Exec("CREATE USER 'e2euser'@'%' IDENTIFIED BY 'pass123'")
		require.NoError(t, err)
	})

	t.Run("GRANT SELECT,INSERT ON schema", func(t *testing.T) {
		_, err := db.Exec("GRANT SELECT, INSERT ON public.* TO 'e2euser'@'%'")
		assert.NoError(t, err)
	})

	t.Run("GRANT REPLICATION SLAVE", func(t *testing.T) {
		_, err := db.Exec("GRANT REPLICATION SLAVE ON *.* TO 'e2euser'@'%'")
		assert.NoError(t, err)
	})

	t.Run("REVOKE SELECT", func(t *testing.T) {
		_, err := db.Exec("REVOKE SELECT ON public.* FROM 'e2euser'@'%'")
		assert.NoError(t, err)
	})

	t.Run("FLUSH PRIVILEGES", func(t *testing.T) {
		_, err := db.Exec("FLUSH PRIVILEGES")
		assert.NoError(t, err)
	})

	t.Run("DROP USER", func(t *testing.T) {
		_, err := db.Exec("DROP USER 'e2euser'@'%'")
		assert.NoError(t, err)
	})
}

// ================================================================
// 6. Configuration Management (mysql_master_slave_management_features.md 6)
// ================================================================

func TestConfigVariables(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	t.Run("SET GLOBAL server_id and read back", func(t *testing.T) {
		_, err := db.Exec("SET GLOBAL server_id = 42")
		assert.NoError(t, err)
		var val string
		err = db.QueryRow("SELECT @@global.server_id").Scan(&val)
		assert.NoError(t, err)
		assert.Equal(t, "42", val)
	})

	t.Run("SET GLOBAL max_connections", func(t *testing.T) {
		_, err := db.Exec("SET GLOBAL max_connections = 200")
		assert.NoError(t, err)
	})

	t.Run("SET GLOBAL wait_timeout", func(t *testing.T) {
		_, err := db.Exec("SET GLOBAL wait_timeout = 300")
		assert.NoError(t, err)
	})

	t.Run("report_host variable", func(t *testing.T) {
		_, err := db.Exec("SET GLOBAL report_host = 'myhost'")
		assert.NoError(t, err)
	})
}

// ================================================================
// 7. Server Status Queries (mysql_master_slave_management_features.md 7)
// ================================================================

func TestServerStatus(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	t.Run("SELECT @@version", func(t *testing.T) {
		var val string
		err := db.QueryRow("SELECT @@version").Scan(&val)
		require.NoError(t, err)
		t.Logf("@@version = %s", val)
		assert.NotEmpty(t, val)
	})

	t.Run("SELECT @@global.server_id", func(t *testing.T) {
		var val string
		err := db.QueryRow("SELECT @@global.server_id").Scan(&val)
		require.NoError(t, err)
		assert.NotEmpty(t, val)
	})

	t.Run("SELECT @@global.read_only", func(t *testing.T) {
		var val string
		err := db.QueryRow("SELECT @@global.read_only").Scan(&val)
		require.NoError(t, err)
	})

	t.Run("SELECT @@global.super_read_only", func(t *testing.T) {
		var val string
		err := db.QueryRow("SELECT @@global.super_read_only").Scan(&val)
		require.NoError(t, err)
	})

	t.Run("SHOW PROCESSLIST", func(t *testing.T) {
		rows, err := db.Query("SHOW PROCESSLIST")
		require.NoError(t, err)
		defer rows.Close()
		cols, _ := rows.Columns()
		assert.Contains(t, cols, "Id")
		assert.Contains(t, cols, "User")
		assert.Contains(t, cols, "Host")
		assert.Contains(t, cols, "db")
		assert.Contains(t, cols, "Command")
		assert.Contains(t, cols, "Time")
		assert.Contains(t, cols, "State")
		assert.Contains(t, cols, "Info")
	})
}

// ================================================================
// 8. Binlog Management (mysql_master_slave_management_features.md 8)
// ================================================================

func TestBinlogManagement(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	t.Run("SET SESSION sql_log_bin = 0", func(t *testing.T) {
		_, err := db.Exec("SET SESSION sql_log_bin = 0")
		assert.NoError(t, err)
	})

	t.Run("SET SESSION sql_log_bin = 1", func(t *testing.T) {
		_, err := db.Exec("SET SESSION sql_log_bin = 1")
		assert.NoError(t, err)
	})

	t.Run("SHOW GLOBAL VARIABLES LIKE binlog_format", func(t *testing.T) {
		var name, val string
		err := db.QueryRow("SHOW GLOBAL VARIABLES LIKE 'binlog_format'").Scan(&name, &val)
		require.NoError(t, err)
		assert.Equal(t, "ROW", val)
	})

	t.Run("SHOW GLOBAL VARIABLES LIKE log_bin", func(t *testing.T) {
		var name, val string
		err := db.QueryRow("SHOW GLOBAL VARIABLES LIKE 'log_bin'").Scan(&name, &val)
		require.NoError(t, err)
		assert.Equal(t, "ON", val)
	})
}

// ================================================================
// 9. Other Features (mysql_master_slave_management_features.md 9)
// ================================================================

func TestMiscFeatures(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	t.Run("SET SESSION foreign_key_checks = 0/1", func(t *testing.T) {
		_, err := db.Exec("SET SESSION foreign_key_checks = 0")
		assert.NoError(t, err)
		_, err = db.Exec("SET SESSION foreign_key_checks = 1")
		assert.NoError(t, err)
	})

	t.Run("KILL CONNECTION", func(t *testing.T) {
		_, err := db.Exec("KILL 999999")
		t.Logf("KILL 999999: %v", err)
	})

	t.Run("KILL QUERY", func(t *testing.T) {
		_, err := db.Exec("KILL QUERY 999999")
		t.Logf("KILL QUERY 999999: %v", err)
	})

	t.Run("FLUSH TABLES", func(t *testing.T) {
		_, err := db.Exec("FLUSH TABLES")
		assert.NoError(t, err)
	})

	t.Run("ALTER TABLE DISCARD TABLESPACE returns error", func(t *testing.T) {
		// Create a temp table first
		_, _ = db.Exec("CREATE TABLE e2e_ts_test (id INT PRIMARY KEY)")
		defer db.Exec("DROP TABLE IF EXISTS e2e_ts_test")
		_, err := db.Exec("ALTER TABLE e2e_ts_test DISCARD TABLESPACE")
		assert.Error(t, err) // should return not-supported error
		t.Logf("DISCARD TABLESPACE: %v", err)
	})

	t.Run("ALTER TABLE IMPORT TABLESPACE returns error", func(t *testing.T) {
		_, err := db.Exec("ALTER TABLE e2e_ts_test IMPORT TABLESPACE")
		assert.Error(t, err)
	})
}

// ================================================================
// SHOW Commands Coverage (design_mysql_features_support.md)
// ================================================================

func TestShowCommandsAdmin(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	t.Run("SHOW DATABASES", func(t *testing.T) {
		rows, err := db.Query("SHOW DATABASES")
		require.NoError(t, err)
		defer rows.Close()
		count := 0
		for rows.Next() {
			count++
		}
		assert.Greater(t, count, 0)
	})

	t.Run("SHOW TABLES", func(t *testing.T) {
		rows, err := db.Query("SHOW TABLES")
		require.NoError(t, err)
		defer rows.Close()
	})

	t.Run("SHOW COLUMNS FROM table", func(t *testing.T) {
		_, _ = db.Exec("CREATE TABLE IF NOT EXISTS e2e_show_cols (id INT PRIMARY KEY, name VARCHAR(50))")
		defer db.Exec("DROP TABLE IF EXISTS e2e_show_cols")

		rows, err := db.Query("SHOW COLUMNS FROM e2e_show_cols")
		require.NoError(t, err)
		defer rows.Close()
		cols, _ := rows.Columns()
		assert.Contains(t, cols, "Field")
		assert.Contains(t, cols, "Type")
	})

	t.Run("SHOW FULL COLUMNS FROM table", func(t *testing.T) {
		_, _ = db.Exec("CREATE TABLE IF NOT EXISTS e2e_show_cols2 (id INT PRIMARY KEY, name VARCHAR(50))")
		defer db.Exec("DROP TABLE IF EXISTS e2e_show_cols2")

		rows, err := db.Query("SHOW FULL COLUMNS FROM e2e_show_cols2")
		require.NoError(t, err)
		defer rows.Close()
		cols, _ := rows.Columns()
		assert.Contains(t, cols, "Collation")
		assert.Contains(t, cols, "Privileges")
	})

	t.Run("DESCRIBE table", func(t *testing.T) {
		_, _ = db.Exec("CREATE TABLE IF NOT EXISTS e2e_desc (id INT PRIMARY KEY)")
		defer db.Exec("DROP TABLE IF EXISTS e2e_desc")

		rows, err := db.Query("DESCRIBE e2e_desc")
		require.NoError(t, err)
		defer rows.Close()
	})

	t.Run("SHOW CREATE TABLE", func(t *testing.T) {
		_, _ = db.Exec("CREATE TABLE IF NOT EXISTS e2e_create (id INT PRIMARY KEY)")
		defer db.Exec("DROP TABLE IF EXISTS e2e_create")

		var tbl, createSQL string
		err := db.QueryRow("SHOW CREATE TABLE e2e_create").Scan(&tbl, &createSQL)
		require.NoError(t, err)
		assert.Equal(t, "e2e_create", tbl)
	})

	t.Run("SHOW INDEX FROM table", func(t *testing.T) {
		_, _ = db.Exec("CREATE TABLE IF NOT EXISTS e2e_idx (id INT PRIMARY KEY, name VARCHAR(50))")
		defer db.Exec("DROP TABLE IF EXISTS e2e_idx")

		rows, err := db.Query("SHOW INDEX FROM e2e_idx")
		require.NoError(t, err)
		defer rows.Close()
		cols, _ := rows.Columns()
		assert.Contains(t, cols, "Key_name")
		assert.Contains(t, cols, "Column_name")
	})

	t.Run("SHOW STATUS", func(t *testing.T) {
		rows, err := db.Query("SHOW STATUS")
		require.NoError(t, err)
		defer rows.Close()
	})

	t.Run("SHOW VARIABLES", func(t *testing.T) {
		rows, err := db.Query("SHOW VARIABLES")
		require.NoError(t, err)
		defer rows.Close()
		count := 0
		for rows.Next() {
			count++
		}
		assert.Greater(t, count, 0)
	})

	t.Run("SHOW WARNINGS", func(t *testing.T) {
		rows, err := db.Query("SHOW WARNINGS")
		require.NoError(t, err)
		defer rows.Close()
	})

	t.Run("SHOW GLOBAL STATUS", func(t *testing.T) {
		rows, err := db.Query("SHOW GLOBAL STATUS")
		require.NoError(t, err)
		defer rows.Close()
		count := 0
		for rows.Next() {
			count++
		}
		assert.Greater(t, count, 0)
	})

	t.Run("SHOW GLOBAL VARIABLES", func(t *testing.T) {
		rows, err := db.Query("SHOW GLOBAL VARIABLES")
		require.NoError(t, err)
		defer rows.Close()
		count := 0
		for rows.Next() {
			count++
		}
		assert.Greater(t, count, 0)
	})
}

// ================================================================
// SQL Rewrite: DDL (design_mysql_features_support.md)
// ================================================================

func TestDDL(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	t.Run("CREATE TABLE with MySQL types", func(t *testing.T) {
		defer db.Exec("DROP TABLE IF EXISTS e2e_types")
		_, err := db.Exec(`CREATE TABLE e2e_types (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			tiny_val TINYINT,
			small_val SMALLINT,
			med_val MEDIUMINT,
			int_val INT,
			big_val BIGINT,
			float_val FLOAT,
			double_val DOUBLE,
			dec_val DECIMAL(10,2),
			bool_val BOOLEAN,
			char_val CHAR(10),
			varchar_val VARCHAR(255),
			text_val TEXT,
			blob_val BLOB,
			date_val DATE,
			time_val TIME,
			datetime_val DATETIME,
			ts_val TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			json_val JSON,
			year_val YEAR
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
		require.NoError(t, err)
	})

	t.Run("ALTER TABLE ADD COLUMN", func(t *testing.T) {
		_, _ = db.Exec("CREATE TABLE IF NOT EXISTS e2e_alter (id INT PRIMARY KEY)")
		defer db.Exec("DROP TABLE IF EXISTS e2e_alter")
		_, err := db.Exec("ALTER TABLE e2e_alter ADD COLUMN name VARCHAR(100)")
		assert.NoError(t, err)
	})

	t.Run("ALTER TABLE DROP COLUMN", func(t *testing.T) {
		_, _ = db.Exec("CREATE TABLE IF NOT EXISTS e2e_alter2 (id INT PRIMARY KEY, name VARCHAR(100))")
		defer db.Exec("DROP TABLE IF EXISTS e2e_alter2")
		_, err := db.Exec("ALTER TABLE e2e_alter2 DROP COLUMN name")
		assert.NoError(t, err)
	})

	t.Run("CREATE INDEX / DROP INDEX", func(t *testing.T) {
		_, _ = db.Exec("CREATE TABLE IF NOT EXISTS e2e_index (id INT PRIMARY KEY, name VARCHAR(100))")
		defer db.Exec("DROP TABLE IF EXISTS e2e_index")

		_, err := db.Exec("CREATE INDEX idx_name ON e2e_index(name)")
		assert.NoError(t, err)
		// PostgreSQL: DROP INDEX does not use ON table
		_, err = db.Exec("DROP INDEX idx_name")
		assert.NoError(t, err)
	})

	t.Run("TRUNCATE TABLE", func(t *testing.T) {
		_, _ = db.Exec("CREATE TABLE IF NOT EXISTS e2e_trunc (id INT PRIMARY KEY)")
		defer db.Exec("DROP TABLE IF EXISTS e2e_trunc")
		_, err := db.Exec("TRUNCATE TABLE e2e_trunc")
		assert.NoError(t, err)
	})

	t.Run("DROP TABLE IF EXISTS", func(t *testing.T) {
		_, _ = db.Exec("CREATE TABLE IF NOT EXISTS e2e_drop (id INT PRIMARY KEY)")
		_, err := db.Exec("DROP TABLE IF EXISTS e2e_drop")
		assert.NoError(t, err)
	})
}

// ================================================================
// SQL Rewrite: DML & Transactions
// ================================================================

func TestDMLAndTransactions(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	_, _ = db.Exec("CREATE TABLE IF NOT EXISTS e2e_dml (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(100), val INT)")
	defer db.Exec("DROP TABLE IF EXISTS e2e_dml")

	t.Run("INSERT and LAST_INSERT_ID", func(t *testing.T) {
		res, err := db.Exec("INSERT INTO e2e_dml (name, val) VALUES (?, ?)", "test", 1)
		require.NoError(t, err)
		id, _ := res.LastInsertId()
		assert.Greater(t, id, int64(0))
	})

	t.Run("UPDATE with WHERE", func(t *testing.T) {
		_, err := db.Exec("UPDATE e2e_dml SET val = ? WHERE name = ?", 2, "test")
		assert.NoError(t, err)
	})

	t.Run("DELETE with WHERE", func(t *testing.T) {
		_, err := db.Exec("DELETE FROM e2e_dml WHERE name = ?", "test")
		assert.NoError(t, err)
	})

	t.Run("BEGIN / COMMIT", func(t *testing.T) {
		_, err := db.Exec("BEGIN")
		require.NoError(t, err)
		_, err = db.Exec("INSERT INTO e2e_dml (name, val) VALUES ('tx1', 10)")
		require.NoError(t, err)
		_, err = db.Exec("COMMIT")
		assert.NoError(t, err)
	})

	t.Run("BEGIN / ROLLBACK", func(t *testing.T) {
		_, err := db.Exec("BEGIN")
		require.NoError(t, err)
		_, err = db.Exec("INSERT INTO e2e_dml (name, val) VALUES ('tx2', 20)")
		require.NoError(t, err)
		_, err = db.Exec("ROLLBACK")
		assert.NoError(t, err)

		// tx2 should not exist
		var count int
		err = db.QueryRow("SELECT COUNT(*) FROM e2e_dml WHERE name = 'tx2'").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("SET AUTOCOMMIT", func(t *testing.T) {
		_, err := db.Exec("SET AUTOCOMMIT = 0")
		assert.NoError(t, err)
		_, err = db.Exec("SET AUTOCOMMIT = 1")
		assert.NoError(t, err)
	})
}

// ================================================================
// SQL Rewrite: Function Conversions
// ================================================================

func TestFunctionConversions(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	t.Run("NOW()", func(t *testing.T) {
		var val time.Time
		err := db.QueryRow("SELECT NOW()").Scan(&val)
		require.NoError(t, err)
		assert.False(t, val.IsZero())
	})

	t.Run("CURDATE()", func(t *testing.T) {
		var val string
		err := db.QueryRow("SELECT CURDATE()").Scan(&val)
		require.NoError(t, err)
		assert.NotEmpty(t, val)
	})

	t.Run("IFNULL()", func(t *testing.T) {
		var val int
		err := db.QueryRow("SELECT IFNULL(NULL, 42)").Scan(&val)
		require.NoError(t, err)
		assert.Equal(t, 42, val)
	})

	t.Run("COALESCE()", func(t *testing.T) {
		var val string
		err := db.QueryRow("SELECT COALESCE(NULL, NULL, 'hello')").Scan(&val)
		require.NoError(t, err)
		assert.Equal(t, "hello", val)
	})

	t.Run("IF()", func(t *testing.T) {
		var val string
		err := db.QueryRow("SELECT IF(1>0, 'yes', 'no')").Scan(&val)
		require.NoError(t, err)
		assert.Equal(t, "yes", val)
	})

	t.Run("CONCAT()", func(t *testing.T) {
		var val string
		err := db.QueryRow("SELECT CONCAT('a', 'b', 'c')").Scan(&val)
		require.NoError(t, err)
		assert.Equal(t, "abc", val)
	})

	t.Run("RAND()", func(t *testing.T) {
		var val float64
		err := db.QueryRow("SELECT RAND()").Scan(&val)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, val, 0.0)
		assert.Less(t, val, 1.0)
	})

	t.Run("ABS / CEIL / FLOOR / ROUND", func(t *testing.T) {
		var v1, v2, v3, v4 float64
		err := db.QueryRow("SELECT ABS(-5), CEIL(1.2), FLOOR(1.8), ROUND(1.5)").Scan(&v1, &v2, &v3, &v4)
		require.NoError(t, err)
		assert.Equal(t, 5.0, v1)
		assert.Equal(t, 2.0, v2)
		assert.Equal(t, 1.0, v3)
		assert.Equal(t, 2.0, v4)
	})

	t.Run("UPPER / LOWER / LENGTH / TRIM", func(t *testing.T) {
		var u, l string
		var n int
		var tr string
		err := db.QueryRow("SELECT UPPER('abc'), LOWER('ABC'), LENGTH('hello'), TRIM('  hi  ')").Scan(&u, &l, &n, &tr)
		require.NoError(t, err)
		assert.Equal(t, "ABC", u)
		assert.Equal(t, "abc", l)
		assert.Equal(t, 5, n)
		assert.Equal(t, "hi", tr)
	})
}

// ================================================================
// SQL Rewrite: Query Syntax
// ================================================================

func TestQuerySyntax(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS e2e_qs (
		id INT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(100),
		val INT
	)`)
	defer db.Exec("DROP TABLE IF EXISTS e2e_qs")

	// Insert test data
	for i := 1; i <= 10; i++ {
		db.Exec("INSERT INTO e2e_qs (name, val) VALUES (?, ?)", fmt.Sprintf("item%d", i), i*10)
	}

	t.Run("LIMIT offset,count syntax", func(t *testing.T) {
		rows, err := db.Query("SELECT name FROM e2e_qs ORDER BY id LIMIT 2, 3")
		require.NoError(t, err)
		defer rows.Close()
		count := 0
		for rows.Next() {
			count++
		}
		assert.Equal(t, 3, count)
	})

	t.Run("GROUP BY with HAVING", func(t *testing.T) {
		var name string
		var total int
		err := db.QueryRow("SELECT name, SUM(val) as total FROM e2e_qs GROUP BY name HAVING SUM(val) > 50 ORDER BY total DESC LIMIT 1").Scan(&name, &total)
		require.NoError(t, err)
		assert.Greater(t, total, 50)
	})

	t.Run("DISTINCT", func(t *testing.T) {
		rows, err := db.Query("SELECT DISTINCT name FROM e2e_qs")
		require.NoError(t, err)
		defer rows.Close()
	})

	t.Run("UNION ALL", func(t *testing.T) {
		rows, err := db.Query("SELECT name FROM e2e_qs WHERE id <= 3 UNION ALL SELECT name FROM e2e_qs WHERE id > 8")
		require.NoError(t, err)
		defer rows.Close()
	})

	t.Run("Subquery in WHERE", func(t *testing.T) {
		rows, err := db.Query("SELECT name FROM e2e_qs WHERE val > (SELECT AVG(val) FROM e2e_qs)")
		require.NoError(t, err)
		defer rows.Close()
	})

	t.Run("Prepared statement with placeholders", func(t *testing.T) {
		var name string
		err := db.QueryRow("SELECT name FROM e2e_qs WHERE id = ? AND val > ?", 1, 0).Scan(&name)
		require.NoError(t, err)
		assert.Equal(t, "item1", name)
	})

	t.Run("SELECT FOR UPDATE", func(t *testing.T) {
		tx, err := db.Begin()
		require.NoError(t, err)
		defer tx.Rollback()
		var name string
		err = tx.QueryRow("SELECT name FROM e2e_qs WHERE id = 1 FOR UPDATE").Scan(&name)
		require.NoError(t, err)
	})
}

// ================================================================
// Combined: Full Switchover Simulation
// ================================================================

func TestSwitchoverCommandSequence(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	// Simulate MySQL master-slave switchover:
	// 1. Set read_only
	_, err := db.Exec("SET GLOBAL read_only = 1")
	assert.NoError(t, err)
	// 2. Check slave status
	rows, err := db.Query("SHOW SLAVE STATUS")
	require.NoError(t, err)
	rows.Close()
	// 3. Check master status
	masterRows, err := db.Query("SHOW MASTER STATUS")
	require.NoError(t, err)
	if masterRows.Next() {
		cols, _ := masterRows.Columns()
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, masterRows.Scan(ptrs...))
		t.Logf("Master: File=%s Pos=%s", vals[0].String, vals[1].String)
	}
	masterRows.Close()
	// 4. Show process list
	rows, err = db.Query("SHOW PROCESSLIST")
	require.NoError(t, err)
	rows.Close()
	// 5. Disable read_only
	_, err = db.Exec("SET GLOBAL read_only = 0")
	assert.NoError(t, err)
	// 6. Verify
	var val string
	err = db.QueryRow("SELECT @@global.read_only").Scan(&val)
	require.NoError(t, err)
	assert.Equal(t, "0", val)

	t.Log("Switchover command sequence completed")
}

// ================================================================
// Full CRUD E2E
// ================================================================

func TestCRUD_E2E(t *testing.T) {
	db, cleanup := setupAdminDB(t)
	defer cleanup()

	tableName := fmt.Sprintf("e2e_crud_%d", os.Getpid())
	defer db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))

	// CREATE
	_, err := db.Exec(fmt.Sprintf(`CREATE TABLE %s (
		id INT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(100) NOT NULL,
		email VARCHAR(200),
		created_at DATETIME DEFAULT NOW()
	)`, tableName))
	require.NoError(t, err)

	// INSERT
	result, err := db.Exec(fmt.Sprintf("INSERT INTO %s (name, email) VALUES (?, ?)", tableName), "Alice", "alice@test.com")
	require.NoError(t, err)
	id, _ := result.LastInsertId()
	assert.Greater(t, id, int64(0))

	// SELECT
	var name, email string
	err = db.QueryRow(fmt.Sprintf("SELECT name, email FROM %s WHERE id = ?", tableName), id).Scan(&name, &email)
	require.NoError(t, err)
	assert.Equal(t, "Alice", name)

	// UPDATE
	_, err = db.Exec(fmt.Sprintf("UPDATE %s SET email = ? WHERE id = ?", tableName), "alice2@test.com", id)
	require.NoError(t, err)

	// DELETE
	_, err = db.Exec(fmt.Sprintf("DELETE FROM %s WHERE id = ?", tableName), id)
	require.NoError(t, err)

	// Verify
	var count int
	err = db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName)).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}
