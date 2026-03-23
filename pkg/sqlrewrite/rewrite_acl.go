package sqlrewrite

import (
	"fmt"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

// ACLRewriter rewrites MySQL ACL (user/privilege) commands to PostgreSQL
// using AST parsing via TiDB parser.
type ACLRewriter struct {
	parser *parser.Parser
}

// NewACLRewriter creates a new ACL rewriter
func NewACLRewriter() *ACLRewriter {
	return &ACLRewriter{
		parser: parser.New(),
	}
}

// IsACLCommand returns true if the SQL is a user/privilege management command
func IsACLCommand(sql string) bool {
	upper := strings.ToUpper(strings.TrimSpace(sql))
	prefixes := []string{
		"CREATE USER",
		"DROP USER",
		"GRANT ",
		"REVOKE ",
		"FLUSH PRIVILEGES",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// RewriteACL rewrites MySQL ACL commands to PostgreSQL using AST parsing.
// Returns the rewritten SQL and true if handled, or original SQL and false if not.
func RewriteACL(sql string) (string, bool) {
	r := NewACLRewriter()
	return r.Rewrite(sql)
}

// Rewrite parses the MySQL ACL statement into AST and generates PostgreSQL equivalent.
func (r *ACLRewriter) Rewrite(sql string) (string, bool) {
	stmts, _, err := r.parser.Parse(sql, "", "")
	if err != nil {
		// Parser failed, not a valid ACL command
		return sql, false
	}

	if len(stmts) == 0 {
		return sql, false
	}

	switch stmt := stmts[0].(type) {
	case *ast.CreateUserStmt:
		return r.rewriteCreateUser(stmt), true
	case *ast.DropUserStmt:
		return r.rewriteDropUser(stmt), true
	case *ast.GrantStmt:
		return r.rewriteGrant(stmt), true
	case *ast.RevokeStmt:
		return r.rewriteRevoke(stmt), true
	case *ast.FlushStmt:
		if stmt.Tp == ast.FlushPrivileges {
			// PostgreSQL doesn't need FLUSH PRIVILEGES
			return "", true
		}
		return sql, false
	default:
		return sql, false
	}
}

// rewriteCreateUser converts AST CreateUserStmt to PostgreSQL CREATE ROLE
// MySQL:  CREATE USER 'user'@'host' IDENTIFIED BY 'password'
// PG:     CREATE ROLE user WITH LOGIN PASSWORD 'password'
func (r *ACLRewriter) rewriteCreateUser(stmt *ast.CreateUserStmt) string {
	if len(stmt.Specs) == 0 {
		return "CREATE ROLE WITH LOGIN"
	}

	// Process first user spec (PostgreSQL handles one role at a time for complex cases)
	spec := stmt.Specs[0]
	username := spec.User.Username

	var parts []string
	parts = append(parts, "CREATE ROLE")

	if stmt.IfNotExists {
		// PostgreSQL doesn't support IF NOT EXISTS for CREATE ROLE directly
		// Use DO block or just include as comment; for simplicity, we'll try the approach
		// Actually PG 9.x+ doesn't have IF NOT EXISTS for CREATE ROLE,
		// but we generate it and let PG handle the error or use a wrapper
		// For now, generate simple CREATE ROLE
	}

	parts = append(parts, quoteIdentifier(username))
	parts = append(parts, "WITH LOGIN")

	// Extract password from auth options
	if spec.AuthOpt != nil && spec.AuthOpt.ByAuthString {
		parts = append(parts, fmt.Sprintf("PASSWORD '%s'", escapeString(spec.AuthOpt.AuthString)))
	}

	return strings.Join(parts, " ")
}

// rewriteDropUser converts AST DropUserStmt to PostgreSQL DROP ROLE
// MySQL:  DROP USER [IF EXISTS] 'user'@'host'
// PG:     DROP ROLE [IF EXISTS] user
func (r *ACLRewriter) rewriteDropUser(stmt *ast.DropUserStmt) string {
	var parts []string
	parts = append(parts, "DROP ROLE")

	if stmt.IfExists {
		parts = append(parts, "IF EXISTS")
	}

	var users []string
	for _, user := range stmt.UserList {
		users = append(users, quoteIdentifier(user.Username))
	}
	parts = append(parts, strings.Join(users, ", "))

	return strings.Join(parts, " ")
}

// rewriteGrant converts AST GrantStmt to PostgreSQL GRANT
// MySQL:  GRANT priv [, priv] ON db.table TO 'user'@'host'
// PG:     GRANT priv [, priv] ON target TO user
func (r *ACLRewriter) rewriteGrant(stmt *ast.GrantStmt) string {
	if len(stmt.Users) == 0 {
		return "-- empty GRANT (no users)"
	}

	// Extract username from first user spec
	username := stmt.Users[0].User.Username

	// Check for replication privileges → map to ALTER ROLE attributes
	if hasReplicationPriv(stmt.Privs) {
		return fmt.Sprintf("ALTER ROLE %s REPLICATION", quoteIdentifier(username))
	}

	// Map privileges
	pgPrivs := mapPrivsFromAST(stmt.Privs)

	// Map grant target (db.table → SCHEMA/TABLE)
	pgTarget := mapGrantLevelFromAST(stmt.Level)

	// Map users
	var pgUsers []string
	for _, spec := range stmt.Users {
		pgUsers = append(pgUsers, quoteIdentifier(spec.User.Username))
	}

	return fmt.Sprintf("GRANT %s ON %s TO %s", pgPrivs, pgTarget, strings.Join(pgUsers, ", "))
}

// rewriteRevoke converts AST RevokeStmt to PostgreSQL REVOKE
// MySQL:  REVOKE priv ON db.table FROM 'user'@'host'
// PG:     REVOKE priv ON target FROM user
func (r *ACLRewriter) rewriteRevoke(stmt *ast.RevokeStmt) string {
	if len(stmt.Users) == 0 {
		return "-- empty REVOKE (no users)"
	}

	username := stmt.Users[0].User.Username

	// Replication privileges → ALTER ROLE NOREPLICATION
	if hasReplicationPriv(stmt.Privs) {
		return fmt.Sprintf("ALTER ROLE %s NOREPLICATION", quoteIdentifier(username))
	}

	pgPrivs := mapPrivsFromAST(stmt.Privs)
	pgTarget := mapGrantLevelFromAST(stmt.Level)

	var pgUsers []string
	for _, spec := range stmt.Users {
		pgUsers = append(pgUsers, quoteIdentifier(spec.User.Username))
	}

	return fmt.Sprintf("REVOKE %s ON %s FROM %s", pgPrivs, pgTarget, strings.Join(pgUsers, ", "))
}

// hasReplicationPriv checks if any privilege is a replication type
func hasReplicationPriv(privs []*ast.PrivElem) bool {
	for _, p := range privs {
		if p.Priv == mysql.ReplicationSlavePriv || p.Priv == mysql.ReplicationClientPriv {
			return true
		}
	}
	return false
}

// mapPrivsFromAST maps MySQL AST privilege types to PostgreSQL privilege strings
func mapPrivsFromAST(privs []*ast.PrivElem) string {
	var pgPrivs []string

	for _, p := range privs {
		switch p.Priv {
		case mysql.AllPriv:
			pgPrivs = append(pgPrivs, "ALL PRIVILEGES")
		case mysql.SelectPriv:
			pgPrivs = append(pgPrivs, "SELECT")
		case mysql.InsertPriv:
			pgPrivs = append(pgPrivs, "INSERT")
		case mysql.UpdatePriv:
			pgPrivs = append(pgPrivs, "UPDATE")
		case mysql.DeletePriv:
			pgPrivs = append(pgPrivs, "DELETE")
		case mysql.CreatePriv:
			pgPrivs = append(pgPrivs, "CREATE")
		case mysql.DropPriv:
			pgPrivs = append(pgPrivs, "DROP")
		case mysql.AlterPriv:
			pgPrivs = append(pgPrivs, "ALTER")
		case mysql.IndexPriv:
			pgPrivs = append(pgPrivs, "CREATE") // PG uses CREATE for index creation
		case mysql.ExecutePriv:
			pgPrivs = append(pgPrivs, "EXECUTE")
		case mysql.ReferencesPriv:
			pgPrivs = append(pgPrivs, "REFERENCES")
		case mysql.TriggerPriv:
			pgPrivs = append(pgPrivs, "TRIGGER")
		case mysql.UsagePriv:
			pgPrivs = append(pgPrivs, "USAGE")
		case mysql.GrantPriv:
			// WITH GRANT OPTION is handled separately in PG
			pgPrivs = append(pgPrivs, "ALL PRIVILEGES")
		case mysql.ShowDBPriv:
			pgPrivs = append(pgPrivs, "CONNECT")
		case mysql.CreateUserPriv:
			pgPrivs = append(pgPrivs, "CREATEROLE")
		case mysql.ProcessPriv:
			pgPrivs = append(pgPrivs, "pg_read_all_stats")
		case mysql.ReloadPriv:
			pgPrivs = append(pgPrivs, "EXECUTE") // for pg_reload_conf()
		case mysql.FilePriv:
			pgPrivs = append(pgPrivs, "pg_read_server_files")
		case mysql.SuperPriv:
			pgPrivs = append(pgPrivs, "SUPERUSER")
		case mysql.ReplicationSlavePriv, mysql.ReplicationClientPriv:
			// Handled by hasReplicationPriv → ALTER ROLE REPLICATION
			pgPrivs = append(pgPrivs, "REPLICATION")
		default:
			// Pass through the privilege name from MySQL
			privStr, ok := mysql.Priv2Str[p.Priv]
			if ok {
				pgPrivs = append(pgPrivs, strings.ToUpper(privStr))
			}
		}
	}

	if len(pgPrivs) == 0 {
		return "USAGE"
	}
	return strings.Join(pgPrivs, ", ")
}

// mapGrantLevelFromAST maps MySQL AST GrantLevel to PostgreSQL target
func mapGrantLevelFromAST(level *ast.GrantLevel) string {
	if level == nil {
		return "ALL TABLES IN SCHEMA public"
	}

	switch level.Level {
	case ast.GrantLevelGlobal:
		// *.* → ALL TABLES IN SCHEMA public
		return "ALL TABLES IN SCHEMA public"

	case ast.GrantLevelDB:
		// db.* → ALL TABLES IN SCHEMA db
		if level.DBName != "" {
			return fmt.Sprintf("ALL TABLES IN SCHEMA %s", quoteIdentifier(level.DBName))
		}
		return "ALL TABLES IN SCHEMA public"

	case ast.GrantLevelTable:
		// db.table → TABLE db.table
		if level.DBName != "" && level.TableName != "" {
			return fmt.Sprintf("TABLE %s.%s", quoteIdentifier(level.DBName), quoteIdentifier(level.TableName))
		}
		if level.TableName != "" {
			return fmt.Sprintf("TABLE %s", quoteIdentifier(level.TableName))
		}
		return "ALL TABLES IN SCHEMA public"

	default:
		return "ALL TABLES IN SCHEMA public"
	}
}

// quoteIdentifier quotes a PostgreSQL identifier if needed
func quoteIdentifier(name string) string {
	// If name contains special chars or is a reserved word, quote it
	// For simplicity, only quote if it contains non-alphanumeric chars
	name = strings.Trim(name, "`'\"")
	if name == "" {
		return name
	}
	// Don't quote simple identifiers
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return fmt.Sprintf("\"%s\"", name)
		}
	}
	return name
}

// escapeString escapes single quotes in a string for PostgreSQL
func escapeString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
