package mapper

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// showGlobalStatus handles SHOW GLOBAL STATUS [WHERE ...] queries
// Maps MySQL status variables to PostgreSQL equivalents
func (se *ShowEmulator) showGlobalStatus(ctx context.Context, conn *pgx.Conn, sql string) (pgx.Rows, error) {
	upperSQL := strings.ToUpper(sql)

	// Handle WHERE clause with specific variable names
	// e.g. SHOW GLOBAL STATUS WHERE Variable_name='rpl_semi_sync_master_status' OR Variable_name='rpl_semi_sync_master_clients'
	if strings.Contains(upperSQL, "WHERE") {
		return se.showGlobalStatusWhere(ctx, conn, sql)
	}

	// Default: return common status variables
	query := `
		SELECT 'Uptime' AS "Variable_name", EXTRACT(EPOCH FROM (now() - pg_postmaster_start_time()))::int::text AS "Value"
		UNION ALL SELECT 'Threads_connected', (SELECT count(*)::text FROM pg_stat_activity WHERE state IS NOT NULL)
		UNION ALL SELECT 'Questions', '0'
		UNION ALL SELECT 'Slow_queries', '0'
		UNION ALL SELECT 'rpl_semi_sync_master_status', CASE WHEN (SELECT setting FROM pg_settings WHERE name = 'synchronous_commit') IN ('on', 'remote_write', 'remote_apply') THEN 'ON' ELSE 'OFF' END
		UNION ALL SELECT 'rpl_semi_sync_master_clients', (SELECT count(*)::text FROM pg_stat_replication WHERE sync_state = 'sync')
	`
	return conn.Query(ctx, query)
}

// showGlobalStatusWhere handles SHOW GLOBAL STATUS WHERE Variable_name = 'xxx' OR ...
func (se *ShowEmulator) showGlobalStatusWhere(ctx context.Context, conn *pgx.Conn, sql string) (pgx.Rows, error) {
	// Extract variable names from WHERE clause
	upperSQL := strings.ToUpper(sql)
	whereIdx := strings.Index(upperSQL, "WHERE")
	if whereIdx == -1 {
		return se.showGlobalStatus(ctx, conn, "SHOW GLOBAL STATUS")
	}

	wherePart := sql[whereIdx+5:]
	varNames := extractVariableNames(wherePart)

	if len(varNames) == 0 {
		return se.showGlobalStatus(ctx, conn, "SHOW GLOBAL STATUS")
	}

	// Build UNION ALL query for each requested variable
	var parts []string
	for _, name := range varNames {
		value, found, err := GetMySQLStatusValue(ctx, conn, name)
		if err != nil || !found {
			// Return empty string for unknown variables
			value = ""
		}
		parts = append(parts, fmt.Sprintf("SELECT '%s' AS \"Variable_name\", '%s' AS \"Value\"", name, value))
	}

	query := strings.Join(parts, " UNION ALL ")
	return conn.Query(ctx, query)
}

// showSlaveStatus handles SHOW SLAVE STATUS [FOR CHANNEL 'xxx']
// Maps to PostgreSQL pg_stat_wal_receiver and recovery functions
func (se *ShowEmulator) showSlaveStatus(ctx context.Context, conn *pgx.Conn, sql string) (pgx.Rows, error) {
	// Extract channel name if present: SHOW SLAVE STATUS FOR CHANNEL 'xxx'
	channelName := ""
	upperSQL := strings.ToUpper(sql)
	if idx := strings.Index(upperSQL, "FOR CHANNEL"); idx != -1 {
		rest := strings.TrimSpace(sql[idx+len("FOR CHANNEL"):])
		channelName = strings.Trim(rest, "'\"` ;")
	}

	// Check if this is a standby server by querying pg_is_in_recovery()
	var isInRecovery bool
	err := conn.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&isInRecovery)
	if err != nil {
		return nil, fmt.Errorf("failed to check recovery status: %w", err)
	}

	if !isInRecovery {
		// Not a standby: return empty result with correct columns
		query := `
			SELECT
				'' AS "Slave_IO_State",
				'' AS "Master_Host",
				0 AS "Master_Port",
				'' AS "Master_User",
				0 AS "Connect_Retry",
				'' AS "Master_Log_File",
				0 AS "Read_Master_Log_Pos",
				'' AS "Relay_Log_File",
				0 AS "Relay_Log_Pos",
				'' AS "Relay_Master_Log_File",
				'No' AS "Slave_IO_Running",
				'No' AS "Slave_SQL_Running",
				'' AS "Last_Errno",
				'' AS "Last_Error",
				0 AS "Seconds_Behind_Master",
				'' AS "Channel_Name",
				'' AS "Retrieved_Gtid_Set",
				'' AS "Executed_Gtid_Set"
			WHERE false
		`
		return conn.Query(ctx, query)
	}

	// Standby server: collect replication info from pg_stat_wal_receiver
	query := `
		SELECT
			COALESCE(wr.status, 'stopped') AS "Slave_IO_State",
			COALESCE(
				(SELECT split_part(conninfo, 'host=', 2) FROM pg_stat_wal_receiver LIMIT 1),
				''
			) AS "Master_Host",
			COALESCE(
				(SELECT split_part(split_part(conninfo, 'port=', 2), ' ', 1)::int FROM pg_stat_wal_receiver LIMIT 1),
				5432
			) AS "Master_Port",
			COALESCE(
				(SELECT split_part(split_part(conninfo, 'user=', 2), ' ', 1) FROM pg_stat_wal_receiver LIMIT 1),
				''
			) AS "Master_User",
			60 AS "Connect_Retry",
			COALESCE(pg_walfile_name(COALESCE(wr.flushed_lsn, '0/0')), '') AS "Master_Log_File",
			COALESCE(wr.flushed_lsn::text, '0/0')::text AS "Read_Master_Log_Pos",
			COALESCE(pg_walfile_name(pg_last_wal_receive_lsn()), '') AS "Relay_Log_File",
			COALESCE(pg_last_wal_receive_lsn()::text, '0/0') AS "Relay_Log_Pos",
			COALESCE(pg_walfile_name(pg_last_wal_replay_lsn()), '') AS "Relay_Master_Log_File",
			CASE WHEN wr.status = 'streaming' THEN 'Yes' ELSE 'No' END AS "Slave_IO_Running",
			CASE WHEN NOT pg_is_wal_replay_paused() THEN 'Yes' ELSE 'No' END AS "Slave_SQL_Running",
			'0' AS "Last_Errno",
			'' AS "Last_Error",
			COALESCE(
				EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp()))::int,
				0
			) AS "Seconds_Behind_Master",
			COALESCE(wr.slot_name, '') AS "Channel_Name",
			COALESCE(pg_last_wal_receive_lsn()::text, '') AS "Retrieved_Gtid_Set",
			COALESCE(pg_last_wal_replay_lsn()::text, '') AS "Executed_Gtid_Set"
		FROM (SELECT status, flushed_lsn, slot_name, conninfo FROM pg_stat_wal_receiver LIMIT 1) wr
	`

	// If channel name specified, add slot_name filter
	if channelName != "" {
		query = fmt.Sprintf(`
			SELECT * FROM (%s) sub WHERE "Channel_Name" = '%s'
		`, query, channelName)
	}

	return conn.Query(ctx, query)
}

// showSlaveHosts handles SHOW SLAVE HOSTS / SHOW REPLICAS
// Maps to pg_stat_replication
func (se *ShowEmulator) showSlaveHosts(ctx context.Context, conn *pgx.Conn) (pgx.Rows, error) {
	query := `
		SELECT
			pid AS "Server_id",
			COALESCE(client_addr::text, '') AS "Host",
			0 AS "Port",
			0 AS "Master_id",
			COALESCE(application_name, '') AS "Slave_UUID"
		FROM pg_stat_replication
		ORDER BY pid
	`
	return conn.Query(ctx, query)
}

// showMasterStatus handles SHOW MASTER STATUS
// Maps to pg_current_wal_lsn()
func (se *ShowEmulator) showMasterStatus(ctx context.Context, conn *pgx.Conn) (pgx.Rows, error) {
	query := `
		SELECT
			pg_walfile_name(pg_current_wal_lsn()) AS "File",
			pg_current_wal_lsn()::text AS "Position",
			''::text AS "Binlog_Do_DB",
			''::text AS "Binlog_Ignore_DB",
			pg_current_wal_lsn()::text AS "Executed_Gtid_Set"
	`
	return conn.Query(ctx, query)
}

// showBinaryLogs handles SHOW BINARY LOGS / SHOW MASTER LOGS
// Maps to pg_ls_waldir()
func (se *ShowEmulator) showBinaryLogs(ctx context.Context, conn *pgx.Conn) (pgx.Rows, error) {
	query := `
		SELECT
			name AS "Log_name",
			size AS "File_size",
			'' AS "Encrypted"
		FROM pg_ls_waldir()
		ORDER BY name DESC
		LIMIT 20
	`
	return conn.Query(ctx, query)
}

// showProcessList handles SHOW PROCESSLIST / SHOW FULL PROCESSLIST
// Maps to pg_stat_activity
func (se *ShowEmulator) showProcessList(ctx context.Context, conn *pgx.Conn) (pgx.Rows, error) {
	query := `
		SELECT
			pid AS "Id",
			COALESCE(usename, 'system') AS "User",
			COALESCE(client_addr::text || ':' || client_port::text, 'local') AS "Host",
			COALESCE(datname, '') AS "db",
			CASE
				WHEN state = 'active' THEN 'Query'
				WHEN state = 'idle' THEN 'Sleep'
				WHEN state = 'idle in transaction' THEN 'Sleep'
				WHEN state = 'idle in transaction (aborted)' THEN 'Sleep'
				ELSE 'Connect'
			END AS "Command",
			COALESCE(EXTRACT(EPOCH FROM (now() - query_start))::int, 0) AS "Time",
			COALESCE(
				CASE
					WHEN state = 'active' THEN 'executing'
					WHEN wait_event IS NOT NULL THEN wait_event_type || ': ' || wait_event
					ELSE ''
				END,
				''
			) AS "State",
			COALESCE(query, '') AS "Info"
		FROM pg_stat_activity
		WHERE pid != pg_backend_pid()
		ORDER BY pid
	`
	return conn.Query(ctx, query)
}

// extractVariableNames extracts variable names from a WHERE clause like:
// Variable_name='xxx' OR Variable_name='yyy'
func extractVariableNames(where string) []string {
	var names []string
	// Simple parser: find all 'value' after Variable_name= or Variable_name =
	parts := strings.Split(strings.ToLower(where), "variable_name")
	for _, part := range parts[1:] { // skip first element (before first match)
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "=")
		part = strings.TrimSpace(part)
		// Extract quoted value
		if len(part) > 0 && (part[0] == '\'' || part[0] == '"') {
			quote := part[0]
			end := strings.IndexByte(part[1:], quote)
			if end != -1 {
				names = append(names, part[1:end+1])
			}
		}
	}
	return names
}
