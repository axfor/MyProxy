package mapper

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type ShowEmulator struct{}

func NewShowEmulator() *ShowEmulator {
	return &ShowEmulator{}
}

func (se *ShowEmulator) HandleShowCommand(ctx context.Context, conn *pgx.Conn, sql string) (pgx.Rows, error) {
	upperSQL := strings.ToUpper(strings.TrimSpace(sql))

	if strings.HasPrefix(upperSQL, "SHOW DATABASES") {
		return se.showDatabases(ctx, conn)
	}

	if strings.HasPrefix(upperSQL, "SHOW TABLES") {
		return se.showTables(ctx, conn, sql)
	}

	if strings.HasPrefix(upperSQL, "SHOW COLUMNS") || strings.HasPrefix(upperSQL, "SHOW FIELDS") ||
		strings.HasPrefix(upperSQL, "SHOW FULL COLUMNS") || strings.HasPrefix(upperSQL, "SHOW FULL FIELDS") {
		return se.showColumns(ctx, conn, sql)
	}

	if strings.HasPrefix(upperSQL, "DESCRIBE ") || strings.HasPrefix(upperSQL, "DESC ") {
		return se.describe(ctx, conn, sql)
	}

	if strings.HasPrefix(upperSQL, "SHOW CREATE TABLE") {
		return se.showCreateTable(ctx, conn, sql)
	}

	if strings.HasPrefix(upperSQL, "SHOW INDEX") {
		return se.showIndex(ctx, conn, sql)
	}

	if strings.HasPrefix(upperSQL, "SHOW STATUS") {
		return se.showStatus(ctx, conn)
	}

	if strings.HasPrefix(upperSQL, "SHOW VARIABLES") {
		return se.showVariables(ctx, conn, sql)
	}

	if strings.HasPrefix(upperSQL, "SHOW GLOBAL VARIABLES") {
		return se.showGlobalVariables(ctx, conn, sql)
	}

	if strings.HasPrefix(upperSQL, "SHOW GLOBAL STATUS") {
		return se.showGlobalStatus(ctx, conn, sql)
	}

	if strings.HasPrefix(upperSQL, "SHOW WARNINGS") {
		return se.showWarnings(ctx, conn)
	}

	if strings.HasPrefix(upperSQL, "SHOW SLAVE STATUS") {
		return se.showSlaveStatus(ctx, conn, sql)
	}

	if strings.HasPrefix(upperSQL, "SHOW SLAVE HOSTS") || strings.HasPrefix(upperSQL, "SHOW REPLICAS") {
		return se.showSlaveHosts(ctx, conn)
	}

	if strings.HasPrefix(upperSQL, "SHOW MASTER STATUS") {
		return se.showMasterStatus(ctx, conn)
	}

	if strings.HasPrefix(upperSQL, "SHOW BINARY LOGS") || strings.HasPrefix(upperSQL, "SHOW MASTER LOGS") {
		return se.showBinaryLogs(ctx, conn)
	}

	if strings.HasPrefix(upperSQL, "SHOW PROCESSLIST") || strings.HasPrefix(upperSQL, "SHOW FULL PROCESSLIST") {
		return se.showProcessList(ctx, conn)
	}

	return nil, fmt.Errorf("unsupported SHOW command: %s", sql)
}

func (se *ShowEmulator) showDatabases(ctx context.Context, conn *pgx.Conn) (pgx.Rows, error) {
	query := `
		SELECT schema_name AS "Database"
		FROM information_schema.schemata
		WHERE schema_name NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
		ORDER BY schema_name
	`
	return conn.Query(ctx, query)
}

func (se *ShowEmulator) showTables(ctx context.Context, conn *pgx.Conn, sql string) (pgx.Rows, error) {
	var schemaName string

	upperSQL := strings.ToUpper(sql)
	if strings.Contains(upperSQL, "FROM") || strings.Contains(upperSQL, "IN") {
		parts := strings.Fields(sql)
		for i, part := range parts {
			if (strings.ToUpper(part) == "FROM" || strings.ToUpper(part) == "IN") && i+1 < len(parts) {
				schemaName = strings.Trim(parts[i+1], "`\"';")
				break
			}
		}
	}

	var query string
	if schemaName != "" {
		query = fmt.Sprintf(`
			SELECT table_name AS "Tables_in_%s"
			FROM information_schema.tables
			WHERE table_schema = '%s' AND table_type = 'BASE TABLE'
			ORDER BY table_name
		`, schemaName, schemaName)
	} else {
		query = `
			SELECT table_name AS "Tables"
			FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
			ORDER BY table_name
		`
	}

	return conn.Query(ctx, query)
}

func (se *ShowEmulator) showColumns(ctx context.Context, conn *pgx.Conn, sql string) (pgx.Rows, error) {
	tableName := se.extractTableName(sql)
	if tableName == "" {
		return nil, fmt.Errorf("table name not found in: %s", sql)
	}

	// Clean backticks from the full table name (handles `schema`.`table` format)
	tableName = strings.ReplaceAll(tableName, "`", "")

	// Handle schema.table format (e.g., public.users)
	schemaName := "current_schema()"
	if strings.Contains(tableName, ".") {
		parts := strings.SplitN(tableName, ".", 2)
		schemaName = fmt.Sprintf("'%s'", parts[0])
		tableName = parts[1]
	}

	// Check if this is SHOW FULL COLUMNS (needs additional fields)
	upperSQL := strings.ToUpper(sql)
	isFullColumns := strings.Contains(upperSQL, "FULL")

	var query string
	if isFullColumns {
		// SHOW FULL COLUMNS returns additional fields: Collation, Privileges, Comment
		// Map PostgreSQL types to MySQL types for compatibility with tools like go-mysql/canal
		query = fmt.Sprintf(`
			SELECT
				column_name AS "Field",
				CASE
					WHEN data_type = 'integer' THEN 'int(11)'
					WHEN data_type = 'smallint' THEN 'smallint(6)'
					WHEN data_type = 'bigint' THEN 'bigint(20)'
					WHEN data_type = 'serial' THEN 'int(11)'
					WHEN data_type = 'bigserial' THEN 'bigint(20)'
					WHEN data_type = 'character varying' THEN 'varchar(' || COALESCE(character_maximum_length::text, '255') || ')'
					WHEN data_type = 'character' THEN 'char(' || COALESCE(character_maximum_length::text, '1') || ')'
					WHEN data_type = 'text' THEN 'text'
					WHEN data_type = 'boolean' THEN 'tinyint(1)'
					WHEN data_type = 'numeric' THEN 'decimal(' || COALESCE(numeric_precision::text, '10') || ',' || COALESCE(numeric_scale::text, '0') || ')'
					WHEN data_type = 'real' THEN 'float'
					WHEN data_type = 'double precision' THEN 'double'
					WHEN data_type = 'date' THEN 'date'
					WHEN data_type = 'time without time zone' THEN 'time'
					WHEN data_type = 'time with time zone' THEN 'time'
					WHEN data_type = 'timestamp without time zone' THEN 'datetime'
					WHEN data_type = 'timestamp with time zone' THEN 'timestamp'
					WHEN data_type = 'bytea' THEN 'blob'
					WHEN data_type = 'json' THEN 'json'
					WHEN data_type = 'jsonb' THEN 'json'
					WHEN data_type = 'uuid' THEN 'char(36)'
					ELSE data_type
				END AS "Type",
				COALESCE(collation_name, '') AS "Collation",
				CASE WHEN is_nullable = 'YES' THEN 'YES' ELSE 'NO' END AS "Null",
				CASE
					WHEN EXISTS (
						SELECT 1 FROM information_schema.table_constraints tc
						JOIN information_schema.key_column_usage kcu
						ON tc.constraint_name = kcu.constraint_name
						AND tc.table_schema = kcu.table_schema
						WHERE tc.constraint_type = 'PRIMARY KEY'
						AND kcu.table_schema = %s
						AND kcu.table_name = '%s'
						AND kcu.column_name = c.column_name
					) THEN 'PRI'
					ELSE ''
				END AS "Key",
				COALESCE(column_default, '') AS "Default",
				CASE
					WHEN column_default LIKE 'nextval%%' THEN 'auto_increment'
					ELSE ''
				END AS "Extra",
				'select,insert,update,references' AS "Privileges",
				'' AS "Comment"
			FROM information_schema.columns c
			WHERE table_schema = %s
			  AND table_name = '%s'
			ORDER BY ordinal_position
		`, schemaName, tableName, schemaName, tableName)
	} else {
		query = fmt.Sprintf(`
			SELECT
				column_name AS "Field",
				CASE
					WHEN data_type = 'integer' THEN 'int(11)'
					WHEN data_type = 'smallint' THEN 'smallint(6)'
					WHEN data_type = 'bigint' THEN 'bigint(20)'
					WHEN data_type = 'character varying' THEN 'varchar(' || COALESCE(character_maximum_length::text, '255') || ')'
					WHEN data_type = 'character' THEN 'char(' || COALESCE(character_maximum_length::text, '1') || ')'
					WHEN data_type = 'text' THEN 'text'
					WHEN data_type = 'boolean' THEN 'tinyint(1)'
					WHEN data_type = 'numeric' THEN 'decimal(' || COALESCE(numeric_precision::text, '10') || ',' || COALESCE(numeric_scale::text, '0') || ')'
					ELSE data_type
				END AS "Type",
				CASE WHEN is_nullable = 'YES' THEN 'YES' ELSE 'NO' END AS "Null",
				COALESCE(column_default, '') AS "Default",
				'' AS "Key",
				'' AS "Extra"
			FROM information_schema.columns
			WHERE table_schema = %s
			  AND table_name = '%s'
			ORDER BY ordinal_position
		`, schemaName, tableName)
	}

	return conn.Query(ctx, query)
}

func (se *ShowEmulator) describe(ctx context.Context, conn *pgx.Conn, sql string) (pgx.Rows, error) {
	parts := strings.Fields(sql)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid DESCRIBE command: %s", sql)
	}

	tableName := strings.Trim(parts[1], "`\"';")

	query := fmt.Sprintf(`
		SELECT
			column_name AS "Field",
			data_type AS "Type",
			is_nullable AS "Null",
			column_default AS "Default",
			CASE
				WHEN EXISTS (
					SELECT 1 FROM information_schema.key_column_usage kcu
					WHERE kcu.table_schema = current_schema()
					  AND kcu.table_name = c.table_name
					  AND kcu.column_name = c.column_name
				) THEN 'PRI'
				ELSE ''
			END AS "Key",
			CASE
				WHEN column_default LIKE 'nextval%%' THEN 'auto_increment'
				ELSE ''
			END AS "Extra"
		FROM information_schema.columns c
		WHERE c.table_schema = current_schema()
		  AND c.table_name = '%s'
		ORDER BY c.ordinal_position
	`, tableName)

	return conn.Query(ctx, query)
}

func (se *ShowEmulator) showCreateTable(ctx context.Context, conn *pgx.Conn, sql string) (pgx.Rows, error) {
	parts := strings.Fields(sql)
	if len(parts) < 4 {
		return nil, fmt.Errorf("invalid SHOW CREATE TABLE command: %s", sql)
	}

	tableName := strings.Trim(parts[3], "`\"';")

	query := fmt.Sprintf(`
		SELECT
			'%s' AS "Table",
			'CREATE TABLE %s (...) ' AS "Create Table"
	`, tableName, tableName)

	return conn.Query(ctx, query)
}

func (se *ShowEmulator) showIndex(ctx context.Context, conn *pgx.Conn, sql string) (pgx.Rows, error) {
	tableName := se.extractTableName(sql)
	if tableName == "" {
		return nil, fmt.Errorf("table name not found in: %s", sql)
	}

	// Clean backticks from the full table name (handles `schema`.`table` format)
	tableName = strings.ReplaceAll(tableName, "`", "")

	// Handle schema.table format
	schemaName := "current_schema()"
	if strings.Contains(tableName, ".") {
		parts := strings.SplitN(tableName, ".", 2)
		schemaName = fmt.Sprintf("'%s'", parts[0])
		tableName = parts[1]
	}

	// Return MySQL-compatible SHOW INDEX output with proper column information
	// This includes PRIMARY KEY and other indexes
	// Map PostgreSQL primary key index (_pkey suffix) to MySQL's PRIMARY
	query := fmt.Sprintf(`
		SELECT
			'%s' AS "Table",
			CASE WHEN i.indisunique THEN 0 ELSE 1 END AS "Non_unique",
			CASE WHEN i.indisprimary THEN 'PRIMARY' ELSE ic.relname END AS "Key_name",
			a.attnum AS "Seq_in_index",
			a.attname AS "Column_name",
			'A' AS "Collation",
			0::bigint AS "Cardinality",
			NULL AS "Sub_part",
			NULL AS "Packed",
			CASE WHEN a.attnotnull THEN '' ELSE 'YES' END AS "Null",
			'BTREE' AS "Index_type",
			'' AS "Comment",
			'' AS "Index_comment",
			'YES' AS "Visible"
		FROM pg_index i
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN pg_class ic ON ic.oid = i.indexrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(i.indkey)
		WHERE n.nspname = %s
		  AND t.relname = '%s'
		ORDER BY
			CASE WHEN i.indisprimary THEN 0 ELSE 1 END,
			ic.relname, a.attnum
	`, tableName, schemaName, tableName)

	return conn.Query(ctx, query)
}

func (se *ShowEmulator) showStatus(ctx context.Context, conn *pgx.Conn) (pgx.Rows, error) {
	query := `
		SELECT 'Uptime' AS "Variable_name", '0' AS "Value"
		UNION ALL
		SELECT 'Threads_connected', '1'
		UNION ALL
		SELECT 'Questions', '0'
		UNION ALL
		SELECT 'Slow_queries', '0'
	`
	return conn.Query(ctx, query)
}

func (se *ShowEmulator) showVariables(ctx context.Context, conn *pgx.Conn, sql string) (pgx.Rows, error) {
	upperSQL := strings.ToUpper(sql)

	if strings.Contains(upperSQL, "LIKE") {
		parts := strings.Split(sql, "LIKE")
		if len(parts) > 1 {
			pattern := strings.TrimSpace(parts[1])
			pattern = strings.Trim(pattern, "'\"")
			pattern = strings.ReplaceAll(pattern, "%", "%%")

			query := fmt.Sprintf(`
				SELECT name AS "Variable_name", setting AS "Value"
				FROM pg_settings
				WHERE name LIKE '%s'
				ORDER BY name
			`, pattern)
			return conn.Query(ctx, query)
		}
	}

	query := `
		SELECT
			'version' AS "Variable_name",
			version() AS "Value"
		UNION ALL
		SELECT 'character_set_client', 'utf8mb4'
		UNION ALL
		SELECT 'character_set_connection', 'utf8mb4'
		UNION ALL
		SELECT 'character_set_results', 'utf8mb4'
		UNION ALL
		SELECT 'character_set_server', 'utf8mb4'
		UNION ALL
		SELECT 'collation_connection', 'utf8mb4_general_ci'
		UNION ALL
		SELECT 'collation_server', 'utf8mb4_general_ci'
		UNION ALL
		SELECT 'autocommit', 'ON'
		UNION ALL
		SELECT 'max_allowed_packet', '67108864'
		UNION ALL
		SELECT 'sql_mode', 'TRADITIONAL'
	`
	return conn.Query(ctx, query)
}

func (se *ShowEmulator) showWarnings(ctx context.Context, conn *pgx.Conn) (pgx.Rows, error) {
	query := `
		SELECT
			'Warning' AS "Level",
			0 AS "Code",
			'' AS "Message"
		LIMIT 0
	`
	return conn.Query(ctx, query)
}

func (se *ShowEmulator) extractTableName(sql string) string {
	upperSQL := strings.ToUpper(sql)

	keywords := []string{"FROM", "IN", "COLUMNS", "FIELDS", "INDEX"}
	for _, keyword := range keywords {
		if idx := strings.Index(upperSQL, keyword); idx != -1 {
			remaining := sql[idx+len(keyword):]
			parts := strings.Fields(remaining)
			if len(parts) > 0 {
				tableName := strings.Trim(parts[0], "`\"';")
				return tableName
			}
		}
	}

	parts := strings.Fields(sql)
	if len(parts) >= 2 {
		return strings.Trim(parts[len(parts)-1], "`\"';")
	}

	return ""
}

// HandleSetCommand is a simple string-based fallback for SET commands
// when AST parsing fails. The primary SET handling is done via AST in the handler.
func (se *ShowEmulator) HandleSetCommand(ctx context.Context, sql string, sessionVars map[string]interface{}) error {
	normalizedSQL := strings.TrimSpace(sql)
	upperSQL := strings.ToUpper(normalizedSQL)

	if !strings.HasPrefix(upperSQL, "SET ") {
		return fmt.Errorf("not a SET command: %s", sql)
	}

	assignment := strings.TrimSpace(normalizedSQL[len("SET "):])

	// Handle SET NAMES charset [COLLATE collation]
	fields := strings.Fields(assignment)
	if len(fields) >= 2 && strings.EqualFold(fields[0], "NAMES") {
		charset := strings.Trim(fields[1], "'\"")
		sessionVars["names"] = charset
		sessionVars["character_set_client"] = charset
		sessionVars["character_set_connection"] = charset
		sessionVars["character_set_results"] = charset

		if len(fields) >= 4 && strings.EqualFold(fields[2], "COLLATE") {
			sessionVars["collation_connection"] = strings.Trim(fields[3], "'\"")
		}

		return nil
	}

	// Strip scope prefixes
	upperAssign := strings.ToUpper(assignment)
	for _, prefix := range []string{"GLOBAL ", "SESSION ", "@@GLOBAL.", "@@SESSION.", "@@"} {
		if strings.HasPrefix(upperAssign, prefix) {
			assignment = assignment[len(prefix):]
			break
		}
	}

	parts := strings.SplitN(assignment, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid SET syntax: %s", sql)
	}

	varName := strings.TrimSpace(parts[0])
	varValue := strings.TrimSpace(parts[1])
	varValue = strings.Trim(varValue, "'\"")

	sessionVars[varName] = varValue
	return nil
}

func (se *ShowEmulator) HandleUseCommand(ctx context.Context, conn *pgx.Conn, sql string) error {
	parts := strings.Fields(sql)
	if len(parts) < 2 {
		return fmt.Errorf("invalid USE command: %s", sql)
	}

	dbName := strings.Trim(parts[1], "`\"';")

	_, err := conn.Exec(ctx, fmt.Sprintf("SET search_path TO %s", dbName))
	return err
}

// showGlobalVariables handles SHOW GLOBAL VARIABLES queries
// This is needed for MySQL replication clients like go-mysql/canal
func (se *ShowEmulator) showGlobalVariables(ctx context.Context, conn *pgx.Conn, sql string) (pgx.Rows, error) {
	upperSQL := strings.ToUpper(sql)

	// Handle LIKE patterns for specific variables
	if strings.Contains(upperSQL, "LIKE") {
		parts := strings.Split(upperSQL, "LIKE")
		if len(parts) > 1 {
			pattern := strings.TrimSpace(parts[1])
			pattern = strings.Trim(pattern, "'\"")
			pattern = strings.ToLower(pattern)
			pattern = strings.ReplaceAll(pattern, "%", "")

			// Return MySQL-compatible values for binlog replication
			switch pattern {
			case "binlog_format":
				return conn.Query(ctx, `SELECT 'binlog_format' AS "Variable_name", 'ROW' AS "Value"`)
			case "binlog_row_image":
				return conn.Query(ctx, `SELECT 'binlog_row_image' AS "Variable_name", 'FULL' AS "Value"`)
			case "server_id":
				return conn.Query(ctx, `SELECT 'server_id' AS "Variable_name", '1' AS "Value"`)
			case "server_uuid":
				return conn.Query(ctx, `SELECT 'server_uuid' AS "Variable_name", '38db14f0-9bcc-487a-8001-9bcc38db18d8' AS "Value"`)
			case "gtid_mode":
				return conn.Query(ctx, `SELECT 'gtid_mode' AS "Variable_name", 'OFF' AS "Value"`)
			case "log_bin":
				return conn.Query(ctx, `SELECT 'log_bin' AS "Variable_name", 'ON' AS "Value"`)
			case "binlog_checksum":
				return conn.Query(ctx, `SELECT 'binlog_checksum' AS "Variable_name", 'CRC32' AS "Value"`)
			}
		}
	}

	// Default: return common MySQL global variables
	query := `
		SELECT 'binlog_format' AS "Variable_name", 'ROW' AS "Value"
		UNION ALL SELECT 'binlog_row_image', 'FULL'
		UNION ALL SELECT 'server_id', '1'
		UNION ALL SELECT 'log_bin', 'ON'
		UNION ALL SELECT 'gtid_mode', 'OFF'
		UNION ALL SELECT 'binlog_checksum', 'CRC32'
		UNION ALL SELECT 'max_allowed_packet', '67108864'
		UNION ALL SELECT 'character_set_server', 'utf8mb4'
		UNION ALL SELECT 'collation_server', 'utf8mb4_general_ci'
	`
	return conn.Query(ctx, query)
}
