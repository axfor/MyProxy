package mapper

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

// internalVarOverrides stores runtime overrides for internal variables
// (e.g. SET GLOBAL server_id = 2 stores "2" here)
var (
	internalVarOverrides = make(map[string]string)
	internalVarMu        sync.RWMutex
)

// SetInternalVar sets a runtime override for an internal variable
func SetInternalVar(name, value string) {
	internalVarMu.Lock()
	defer internalVarMu.Unlock()
	internalVarOverrides[strings.ToLower(name)] = value
}

// GetInternalVar gets a runtime override for an internal variable.
// Returns value and true if overridden, empty and false if not.
func GetInternalVar(name string) (string, bool) {
	internalVarMu.RLock()
	defer internalVarMu.RUnlock()
	v, ok := internalVarOverrides[strings.ToLower(name)]
	return v, ok
}

// VarScope defines the scope of a MySQL variable
type VarScope int

const (
	ScopeSession VarScope = iota
	ScopeGlobal
	ScopeInternal // stored in MyProxy only, not forwarded to PG
)

// VarMapping defines how a MySQL variable maps to PostgreSQL
type VarMapping struct {
	PGVar       string                                      // PostgreSQL equivalent variable name
	Scope       VarScope                                    // variable scope
	NeedRestart bool                                        // requires PG restart (not just reload)
	NoOp        bool                                        // no PostgreSQL action needed, just return OK
	Transform   func(mysqlVal string) (pgVal string)        // optional value transform
	Reverse     func(pgVal string) (mysqlVal string)        // optional reverse transform (PG → MySQL)
	StaticValue string                                      // if set, always return this value for reads
}

// mysqlToPostgresVars maps MySQL variable names to PostgreSQL equivalents
var mysqlToPostgresVars = map[string]VarMapping{
	// Read-only control
	"read_only": {
		PGVar: "default_transaction_read_only",
		Scope: ScopeGlobal,
		Transform: func(v string) string {
			if v == "1" || strings.ToUpper(v) == "ON" || strings.ToUpper(v) == "TRUE" {
				return "on"
			}
			return "off"
		},
		Reverse: func(v string) string {
			if v == "on" {
				return "1"
			}
			return "0"
		},
	},
	"super_read_only": {
		PGVar: "default_transaction_read_only",
		Scope: ScopeGlobal,
		Transform: func(v string) string {
			if v == "1" || strings.ToUpper(v) == "ON" || strings.ToUpper(v) == "TRUE" {
				return "on"
			}
			return "off"
		},
		Reverse: func(v string) string {
			if v == "on" {
				return "1"
			}
			return "0"
		},
	},

	// Semi-sync replication
	"rpl_semi_sync_master_enabled": {
		PGVar: "synchronous_commit",
		Scope: ScopeGlobal,
		Transform: func(v string) string {
			if v == "1" || strings.ToUpper(v) == "ON" {
				return "on"
			}
			return "local"
		},
		Reverse: func(v string) string {
			if v == "on" || v == "remote_write" || v == "remote_apply" {
				return "1"
			}
			return "0"
		},
	},
	"rpl_semi_sync_slave_enabled": {
		Scope: ScopeGlobal,
		NoOp:  true,
	},
	"rpl_semi_sync_master_timeout": {
		PGVar: "synchronous_commit",
		Scope: ScopeGlobal,
		NoOp:  true, // PG doesn't have per-timeout, controlled by synchronous_standby_names
	},

	// Connection/session
	"max_connections": {
		PGVar:       "max_connections",
		Scope:       ScopeGlobal,
		NeedRestart: true,
	},
	"wait_timeout": {
		PGVar: "idle_in_transaction_session_timeout",
		Scope: ScopeGlobal,
		Transform: func(v string) string {
			// MySQL wait_timeout is in seconds, PG idle_in_transaction_session_timeout is in ms
			return v + "000"
		},
	},

	// Foreign key checks
	"foreign_key_checks": {
		PGVar: "session_replication_role",
		Scope: ScopeSession,
		Transform: func(v string) string {
			if v == "0" || strings.ToUpper(v) == "OFF" {
				return "replica" // disables FK checks
			}
			return "origin" // enables FK checks
		},
		Reverse: func(v string) string {
			if v == "replica" {
				return "0"
			}
			return "1"
		},
	},

	// Binlog control
	"sql_log_bin": {
		PGVar: "log_statement",
		Scope: ScopeSession,
		Transform: func(v string) string {
			if v == "0" || strings.ToUpper(v) == "OFF" {
				return "none"
			}
			return "all"
		},
		Reverse: func(v string) string {
			if v == "none" {
				return "0"
			}
			return "1"
		},
	},

	// Version info (query PostgreSQL for real version)
	"version": {
		PGVar: "server_version",
		Scope: ScopeGlobal,
		Reverse: func(v string) string {
			// Return PG version in MySQL-like format
			return v + "-MyProxy"
		},
	},
	"version_comment": {
		Scope:       ScopeInternal,
		StaticValue: "MyProxy (MySQL to PostgreSQL Proxy)",
	},

	// Server identity (internal to MyProxy, writable)
	"server_id": {
		Scope:       ScopeInternal,
		NoOp:        true, // SET GLOBAL server_id just stores internally
		StaticValue: "1",
	},
	"server_uuid": {
		Scope:       ScopeInternal,
		StaticValue: "38db14f0-9bcc-487a-8001-9bcc38db18d8",
	},
	"report_host": {
		Scope:       ScopeInternal,
		NoOp:        true,
		StaticValue: "",
	},

	// GTID variables (PG uses LSN, these are compatibility stubs)
	"gtid_mode": {
		Scope:       ScopeInternal,
		StaticValue: "OFF",
	},
	"gtid_purged": {
		Scope: ScopeInternal,
		NoOp:  true, // SET GLOBAL GTID_PURGED = '' just returns OK
	},
	"gtid_executed": {
		Scope:       ScopeInternal,
		StaticValue: "",
	},

	// Character set (internal, PG always uses UTF-8)
	"character_set_client":     {Scope: ScopeInternal, NoOp: true, StaticValue: "utf8mb4"},
	"character_set_connection": {Scope: ScopeInternal, NoOp: true, StaticValue: "utf8mb4"},
	"character_set_results":    {Scope: ScopeInternal, NoOp: true, StaticValue: "utf8mb4"},
	"character_set_server":     {Scope: ScopeInternal, NoOp: true, StaticValue: "utf8mb4"},
	"character_set_database":   {Scope: ScopeInternal, NoOp: true, StaticValue: "utf8mb4"},
	"collation_connection":     {Scope: ScopeInternal, NoOp: true, StaticValue: "utf8mb4_general_ci"},
	"collation_server":         {Scope: ScopeInternal, NoOp: true, StaticValue: "utf8mb4_general_ci"},
	"collation_database":       {Scope: ScopeInternal, NoOp: true, StaticValue: "utf8mb4_general_ci"},

	// SQL mode
	"sql_mode": {Scope: ScopeInternal, NoOp: true, StaticValue: "TRADITIONAL"},

	// Misc
	"max_allowed_packet": {Scope: ScopeInternal, NoOp: true, StaticValue: "67108864"},
	"net_write_timeout":  {Scope: ScopeInternal, NoOp: true},
	"net_read_timeout":   {Scope: ScopeInternal, NoOp: true},
	"interactive_timeout": {Scope: ScopeInternal, NoOp: true},
}

// mysqlStatusVars maps MySQL SHOW GLOBAL STATUS variable names to PostgreSQL queries
var mysqlStatusVars = map[string]StatusVarMapping{
	"rpl_semi_sync_master_status": {
		PGQuery: "SHOW synchronous_commit",
		Transform: func(v string) string {
			if v == "on" || v == "remote_write" || v == "remote_apply" {
				return "ON"
			}
			return "OFF"
		},
	},
	"rpl_semi_sync_master_clients": {
		PGQuery: "SELECT count(*)::text FROM pg_stat_replication WHERE sync_state = 'sync'",
	},
	"rpl_semi_sync_slave_enabled": {
		StaticValue: "OFF",
	},
}

// StatusVarMapping defines how a MySQL status variable maps to PostgreSQL
type StatusVarMapping struct {
	PGQuery     string
	Transform   func(pgVal string) string
	StaticValue string // if set, always return this value
}

// GetVarMapping returns the variable mapping for a MySQL variable name.
// Returns the mapping and true if found, zero value and false if not found.
func GetVarMapping(mysqlVar string) (VarMapping, bool) {
	m, ok := mysqlToPostgresVars[strings.ToLower(mysqlVar)]
	return m, ok
}

// GetStatusVarMapping returns the status variable mapping for a MySQL status variable.
func GetStatusVarMapping(mysqlVar string) (StatusVarMapping, bool) {
	m, ok := mysqlStatusVars[strings.ToLower(mysqlVar)]
	return m, ok
}

// HandleSetGlobal executes a SET GLOBAL command by mapping to PostgreSQL equivalent.
// Returns true if the variable was handled, false if unknown.
func HandleSetGlobal(ctx context.Context, conn *pgx.Conn, varName string, varValue string) (bool, error) {
	mapping, ok := GetVarMapping(varName)
	if !ok {
		return false, nil
	}

	// For internal or no-op variables, store the value as runtime override
	if mapping.Scope == ScopeInternal || mapping.NoOp {
		SetInternalVar(varName, varValue)
		return true, nil
	}

	// Store the MySQL value as runtime override for immediate read-back
	// (ALTER SYSTEM + pg_reload_conf may not take effect immediately in the same session)
	SetInternalVar(varName, varValue)

	pgValue := varValue
	if mapping.Transform != nil {
		pgValue = mapping.Transform(varValue)
	}

	if mapping.NeedRestart {
		_, err := conn.Exec(ctx, fmt.Sprintf("ALTER SYSTEM SET %s = '%s'", mapping.PGVar, pgValue))
		if err != nil {
			return true, fmt.Errorf("ALTER SYSTEM SET %s failed: %w", mapping.PGVar, err)
		}
		_, err = conn.Exec(ctx, "SELECT pg_reload_conf()")
		return true, err
	}

	_, err := conn.Exec(ctx, fmt.Sprintf("ALTER SYSTEM SET %s = '%s'", mapping.PGVar, pgValue))
	if err != nil {
		return true, fmt.Errorf("ALTER SYSTEM SET %s failed: %w", mapping.PGVar, err)
	}
	_, err = conn.Exec(ctx, "SELECT pg_reload_conf()")
	return true, err
}

// HandleSetSession executes a SET SESSION command by mapping to PostgreSQL equivalent.
// Returns true if the variable was handled, false if unknown.
func HandleSetSession(ctx context.Context, conn *pgx.Conn, varName string, varValue string) (bool, error) {
	mapping, ok := GetVarMapping(varName)
	if !ok {
		return false, nil
	}

	if mapping.NoOp || mapping.Scope == ScopeInternal {
		return true, nil
	}

	pgValue := varValue
	if mapping.Transform != nil {
		pgValue = mapping.Transform(varValue)
	}

	_, err := conn.Exec(ctx, fmt.Sprintf("SET %s = '%s'", mapping.PGVar, pgValue))
	return true, err
}

// GetMySQLVarValue reads a MySQL variable value by querying the PostgreSQL equivalent.
// For @@global.xxx queries.
func GetMySQLVarValue(ctx context.Context, conn *pgx.Conn, varName string) (string, bool, error) {
	mapping, ok := GetVarMapping(varName)
	if !ok {
		return "", false, nil
	}

	// Check runtime override first (from SET GLOBAL for internal vars)
	if override, ok := GetInternalVar(varName); ok {
		return override, true, nil
	}

	// Return static value if defined
	if mapping.StaticValue != "" {
		return mapping.StaticValue, true, nil
	}

	if mapping.NoOp || mapping.PGVar == "" {
		return "", true, nil
	}

	var pgValue string
	err := conn.QueryRow(ctx, fmt.Sprintf("SHOW %s", mapping.PGVar)).Scan(&pgValue)
	if err != nil {
		return "", true, err
	}

	if mapping.Reverse != nil {
		return mapping.Reverse(pgValue), true, nil
	}
	return pgValue, true, nil
}

// GetMySQLStatusValue reads a MySQL status variable from PostgreSQL.
func GetMySQLStatusValue(ctx context.Context, conn *pgx.Conn, varName string) (string, bool, error) {
	mapping, ok := GetStatusVarMapping(varName)
	if !ok {
		return "", false, nil
	}

	if mapping.StaticValue != "" {
		return mapping.StaticValue, true, nil
	}

	var pgValue string
	err := conn.QueryRow(ctx, mapping.PGQuery).Scan(&pgValue)
	if err != nil {
		return "", true, err
	}

	if mapping.Transform != nil {
		return mapping.Transform(pgValue), true, nil
	}
	return pgValue, true, nil
}
