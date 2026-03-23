package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ReplicationAdmin handles MySQL replication control commands
// by mapping them to PostgreSQL streaming replication equivalents.
type ReplicationAdmin struct{}

func NewReplicationAdmin() *ReplicationAdmin {
	return &ReplicationAdmin{}
}

// HandleReplicationCommand detects and handles MySQL replication commands.
// Returns (handled bool, err error).
func (ra *ReplicationAdmin) HandleReplicationCommand(ctx context.Context, conn *pgx.Conn, sql string) (bool, error) {
	upper := strings.ToUpper(strings.TrimSpace(sql))

	switch {
	case strings.HasPrefix(upper, "START SLAVE") || strings.HasPrefix(upper, "START REPLICA"):
		return true, ra.startSlave(ctx, conn, upper)
	case strings.HasPrefix(upper, "STOP SLAVE") || strings.HasPrefix(upper, "STOP REPLICA"):
		return true, ra.stopSlave(ctx, conn, upper)
	case strings.HasPrefix(upper, "CHANGE MASTER TO") || strings.HasPrefix(upper, "CHANGE REPLICATION SOURCE TO"):
		return true, ra.changeMaster(ctx, conn, sql)
	case strings.HasPrefix(upper, "RESET SLAVE") || strings.HasPrefix(upper, "RESET REPLICA"):
		return true, ra.resetSlave(ctx, conn)
	case strings.HasPrefix(upper, "RESET MASTER"):
		return true, ra.resetMaster(ctx, conn)
	default:
		return false, nil
	}
}

// IsReplicationCommand returns true if the SQL is a replication control command
func IsReplicationCommand(sql string) bool {
	upper := strings.ToUpper(strings.TrimSpace(sql))
	prefixes := []string{
		"START SLAVE", "START REPLICA",
		"STOP SLAVE", "STOP REPLICA",
		"CHANGE MASTER TO", "CHANGE REPLICATION SOURCE TO",
		"RESET SLAVE", "RESET REPLICA",
		"RESET MASTER",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// startSlave resumes WAL replay on a standby server
// START SLAVE → pg_wal_replay_resume()
// START SLAVE SQL_THREAD → pg_wal_replay_resume()
// START SLAVE IO_THREAD → (no direct PG equivalent, resume is enough)
func (ra *ReplicationAdmin) startSlave(ctx context.Context, conn *pgx.Conn, upperSQL string) error {
	// Check if this is actually a standby
	var isInRecovery bool
	if err := conn.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&isInRecovery); err != nil {
		return fmt.Errorf("failed to check recovery status: %w", err)
	}
	if !isInRecovery {
		return fmt.Errorf("this server is not a standby/replica")
	}

	_, err := conn.Exec(ctx, "SELECT pg_wal_replay_resume()")
	if err != nil {
		return fmt.Errorf("pg_wal_replay_resume() failed: %w", err)
	}
	return nil
}

// stopSlave pauses WAL replay on a standby server
// STOP SLAVE → pg_wal_replay_pause()
// STOP SLAVE SQL_THREAD → pg_wal_replay_pause()
// STOP SLAVE IO_THREAD → (PG doesn't separate IO from SQL thread)
func (ra *ReplicationAdmin) stopSlave(ctx context.Context, conn *pgx.Conn, upperSQL string) error {
	var isInRecovery bool
	if err := conn.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&isInRecovery); err != nil {
		return fmt.Errorf("failed to check recovery status: %w", err)
	}
	if !isInRecovery {
		return fmt.Errorf("this server is not a standby/replica")
	}

	_, err := conn.Exec(ctx, "SELECT pg_wal_replay_pause()")
	if err != nil {
		return fmt.Errorf("pg_wal_replay_pause() failed: %w", err)
	}
	return nil
}

// changeMaster handles CHANGE MASTER TO by modifying primary_conninfo via ALTER SYSTEM
// Supported parameters:
//
//	MASTER_HOST, MASTER_PORT, MASTER_USER, MASTER_PASSWORD, MASTER_AUTO_POSITION
func (ra *ReplicationAdmin) changeMaster(ctx context.Context, conn *pgx.Conn, sql string) error {
	params := parseChangeMasterParams(sql)
	if len(params) == 0 {
		return fmt.Errorf("no valid parameters in CHANGE MASTER TO")
	}

	// MASTER_AUTO_POSITION=1: PostgreSQL streaming replication always uses
	// LSN-based auto-positioning (equivalent behavior). Just acknowledge it.
	// No special action needed.

	// Build primary_conninfo string from parameters
	var parts []string
	if host, ok := params["MASTER_HOST"]; ok {
		parts = append(parts, fmt.Sprintf("host=%s", host))
	}
	if port, ok := params["MASTER_PORT"]; ok {
		parts = append(parts, fmt.Sprintf("port=%s", port))
	}
	if user, ok := params["MASTER_USER"]; ok {
		parts = append(parts, fmt.Sprintf("user=%s", user))
	}
	if password, ok := params["MASTER_PASSWORD"]; ok {
		parts = append(parts, fmt.Sprintf("password=%s", password))
	}
	// MASTER_LOG_FILE / MASTER_LOG_POS: ignored, PG uses LSN auto-positioning

	if len(parts) > 0 {
		connInfo := strings.Join(parts, " ")
		_, err := conn.Exec(ctx, fmt.Sprintf("ALTER SYSTEM SET primary_conninfo = '%s'", connInfo))
		if err != nil {
			return fmt.Errorf("ALTER SYSTEM SET primary_conninfo failed: %w", err)
		}

		// Reload configuration
		_, err = conn.Exec(ctx, "SELECT pg_reload_conf()")
		if err != nil {
			return fmt.Errorf("pg_reload_conf() failed: %w", err)
		}
	}

	return nil
}

// resetSlave clears replication state
// RESET SLAVE → drop replication origin if exists
func (ra *ReplicationAdmin) resetSlave(ctx context.Context, conn *pgx.Conn) error {
	// Remove primary_conninfo
	_, err := conn.Exec(ctx, "ALTER SYSTEM RESET primary_conninfo")
	if err != nil {
		return fmt.Errorf("ALTER SYSTEM RESET primary_conninfo failed: %w", err)
	}

	_, err = conn.Exec(ctx, "SELECT pg_reload_conf()")
	if err != nil {
		return fmt.Errorf("pg_reload_conf() failed: %w", err)
	}

	return nil
}

// resetMaster switches to a new WAL segment
// RESET MASTER → pg_switch_wal()
func (ra *ReplicationAdmin) resetMaster(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, "SELECT pg_switch_wal()")
	if err != nil {
		return fmt.Errorf("pg_switch_wal() failed: %w", err)
	}
	return nil
}

// parseChangeMasterParams extracts key=value pairs from CHANGE MASTER TO statement
// e.g. CHANGE MASTER TO MASTER_HOST='host', MASTER_PORT=3306, MASTER_USER='user'
func parseChangeMasterParams(sql string) map[string]string {
	params := make(map[string]string)

	// Remove the CHANGE MASTER TO / CHANGE REPLICATION SOURCE TO prefix
	upper := strings.ToUpper(strings.TrimSpace(sql))
	var body string
	if idx := strings.Index(upper, " TO "); idx != -1 {
		body = strings.TrimSpace(sql[idx+4:])
	} else {
		return params
	}

	// Split by comma and parse each assignment
	assignments := strings.Split(body, ",")
	for _, assign := range assignments {
		parts := strings.SplitN(assign, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(strings.ToUpper(parts[0]))
		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, "'\"")
		params[key] = value
	}

	return params
}
