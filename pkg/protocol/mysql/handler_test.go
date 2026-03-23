package mysql

import (
	"context"
	"errors"
	"testing"

	"MyProxy/internal/pool"
	"MyProxy/pkg/schema"
	"MyProxy/pkg/session"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSchemaExecutor struct {
	err     error
	calls   int
	lastSQL string
}

func (f *fakeSchemaExecutor) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.calls++
	f.lastSQL = sql
	return pgconn.CommandTag{}, f.err
}

func TestUseDB(t *testing.T) {
	tests := []struct {
		name            string
		mode            pool.ConnectionMode
		inTransaction   bool
		wantErr         string
		expectedDBAfter string
	}{
		{
			name:            "session_affinity allows UseDB",
			mode:            pool.ModeSessionAffinity,
			expectedDBAfter: "tenant_db",
		},
		{
			name:            "pooled rejects UseDB",
			mode:            pool.ModePooled,
			wantErr:         "does not support USE db / COM_INIT_DB semantics",
			expectedDBAfter: "initial_db",
		},
		{
			name:            "hybrid rejects UseDB",
			mode:            pool.ModeHybrid,
			wantErr:         "does not support USE db / COM_INIT_DB semantics",
			expectedDBAfter: "initial_db",
		},
		{
			name:            "transaction rejects UseDB",
			mode:            pool.ModeSessionAffinity,
			inTransaction:   true,
			wantErr:         "cannot change database while transaction is active",
			expectedDBAfter: "initial_db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := session.NewSession("alice", "initial_db", "127.0.0.1")
			sess.InTransaction = tt.inTransaction

			handler := &ConnectionHandler{
				handler: &Handler{
					connectionMode: tt.mode,
				},
				session: sess,
			}

			err := handler.UseDB("tenant_db")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.expectedDBAfter, sess.Database)
		})
	}
}

func TestSwitchSchemaUpdatesStateAfterApplyState(t *testing.T) {
	sess := session.NewSession("alice", "initial_db", "127.0.0.1")
	executor := &fakeSchemaExecutor{}
	ch := &ConnectionHandler{
		handler: &Handler{
			schemaResolver: schema.NewResolver(schema.MappingConfig{DefaultSchema: "public"}),
		},
		session: sess,
	}

	err := ch.switchSchema(context.Background(), executor, "tenant_db")
	require.NoError(t, err)
	assert.Equal(t, 1, executor.calls)
	assert.Equal(t, "tenant_db", sess.CurrentSchema)
	assert.Equal(t, "tenant_db", sess.Database)
}

func TestSwitchSchemaPreservesCurrentSchemaStateOnApplyFailure(t *testing.T) {
	sess := session.NewSession("alice", "initial_db", "127.0.0.1")
	executor := &fakeSchemaExecutor{err: errors.New("apply failed")}
	ch := &ConnectionHandler{
		handler: &Handler{
			schemaResolver: schema.NewResolver(schema.MappingConfig{DefaultSchema: "public"}),
		},
		session: sess,
	}

	err := ch.switchSchema(context.Background(), executor, "tenant_db")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apply failed")
	assert.Equal(t, 1, executor.calls)
	assert.Equal(t, "initial_db", sess.CurrentSchema)
}

func TestSwitchSchemaPreservesCompatibilityDatabaseStateOnApplyFailure(t *testing.T) {
	sess := session.NewSession("alice", "initial_db", "127.0.0.1")
	executor := &fakeSchemaExecutor{err: errors.New("apply failed")}
	ch := &ConnectionHandler{
		handler: &Handler{
			schemaResolver: schema.NewResolver(schema.MappingConfig{DefaultSchema: "public"}),
		},
		session: sess,
	}

	err := ch.switchSchema(context.Background(), executor, "tenant_db")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apply failed")
	assert.Equal(t, "initial_db", sess.Database)
}

func TestInitialSchemaAppliedOnFirstConnection(t *testing.T) {
	sess := session.NewSession("alice", "", "127.0.0.1")
	sess.Database = "tenant_db"
	executor := &fakeSchemaExecutor{}
	ch := &ConnectionHandler{
		handler: &Handler{
			connectionMode: pool.ModeSessionAffinity,
			schemaResolver: schema.NewResolver(schema.MappingConfig{DefaultSchema: "public"}),
		},
		session: sess,
	}

	err := ch.applyInitialSchema(context.Background(), executor)
	require.NoError(t, err)
	assert.Equal(t, 1, executor.calls)
	assert.Equal(t, "SET search_path TO tenant_db", executor.lastSQL)
	assert.Equal(t, "tenant_db", sess.CurrentSchema)
	assert.Equal(t, "tenant_db", sess.Database)
}

func TestInitialSchemaRejectsNonSessionAffinityMode(t *testing.T) {
	sess := session.NewSession("alice", "", "127.0.0.1")
	sess.Database = "tenant_db"
	executor := &fakeSchemaExecutor{}
	ch := &ConnectionHandler{
		handler: &Handler{
			connectionMode: pool.ModePooled,
			schemaResolver: schema.NewResolver(schema.MappingConfig{DefaultSchema: "public"}),
		},
		session: sess,
	}

	err := ch.applyInitialSchema(context.Background(), executor)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support USE db / COM_INIT_DB semantics")
	assert.Equal(t, 0, executor.calls)
	assert.Equal(t, "", sess.CurrentSchema)
}

func TestInitialSchemaFailureDoesNotSetCurrentSchema(t *testing.T) {
	sess := session.NewSession("alice", "", "127.0.0.1")
	sess.Database = "tenant_db"
	executor := &fakeSchemaExecutor{err: errors.New("apply failed")}
	ch := &ConnectionHandler{
		handler: &Handler{
			connectionMode: pool.ModeSessionAffinity,
			schemaResolver: schema.NewResolver(schema.MappingConfig{DefaultSchema: "public"}),
		},
		session: sess,
	}

	err := ch.applyInitialSchema(context.Background(), executor)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apply failed")
	assert.Equal(t, 1, executor.calls)
	assert.Equal(t, "", sess.CurrentSchema)
	assert.Equal(t, "tenant_db", sess.Database)
}

func TestIsKillStatement(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected bool
	}{
		// Valid KILL statements
		{
			name:     "simple KILL with connection id",
			query:    "KILL 10001",
			expected: true,
		},
		{
			name:     "KILL CONNECTION",
			query:    "KILL CONNECTION 10001",
			expected: true,
		},
		{
			name:     "KILL QUERY",
			query:    "KILL QUERY 10001",
			expected: true,
		},
		{
			name:     "lowercase kill",
			query:    "kill 10001",
			expected: true,
		},
		{
			name:     "mixed case kill",
			query:    "Kill Connection 10001",
			expected: true,
		},
		{
			name:     "KILL with leading whitespace",
			query:    "  KILL 10001",
			expected: true,
		},
		{
			name:     "KILL with trailing whitespace",
			query:    "KILL 10001  ",
			expected: true,
		},

		// Non-KILL statements
		{
			name:     "SELECT statement",
			query:    "SELECT * FROM users",
			expected: false,
		},
		{
			name:     "INSERT statement",
			query:    "INSERT INTO users (name) VALUES ('test')",
			expected: false,
		},
		{
			name:     "DELETE statement",
			query:    "DELETE FROM users WHERE id = 1",
			expected: false,
		},
		{
			name:     "UPDATE statement",
			query:    "UPDATE users SET name = 'test' WHERE id = 1",
			expected: false,
		},
		{
			name:     "CREATE TABLE statement",
			query:    "CREATE TABLE test (id INT)",
			expected: false,
		},
		{
			name:     "KILL in column name (not a KILL command)",
			query:    "SELECT kill_count FROM stats",
			expected: false,
		},
		{
			name:     "empty string",
			query:    "",
			expected: false,
		},
		{
			name:     "KILL without space (invalid syntax)",
			query:    "KILLALL",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isKillStatement(tt.query)
			if result != tt.expected {
				t.Errorf("isKillStatement(%q) = %v, expected %v", tt.query, result, tt.expected)
			}
		})
	}
}

func TestExtractInsertTableName(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		expected string
	}{
		{
			name:     "simple INSERT INTO",
			sql:      "INSERT INTO users (name) VALUES ('test')",
			expected: "users",
		},
		{
			name:     "INSERT INTO with schema",
			sql:      "INSERT INTO public.users (name) VALUES ('test')",
			expected: "public.users",
		},
		{
			name:     "INSERT INTO lowercase",
			sql:      "insert into users (name) values ('test')",
			expected: "users",
		},
		{
			name:     "INSERT INTO with backticks",
			sql:      "INSERT INTO `users` (name) VALUES ('test')",
			expected: "users",
		},
		{
			name:     "INSERT INTO with double quotes",
			sql:      "INSERT INTO \"users\" (name) VALUES ('test')",
			expected: "users",
		},
		{
			name:     "SELECT statement",
			sql:      "SELECT * FROM users",
			expected: "",
		},
		{
			name:     "UPDATE statement",
			sql:      "UPDATE users SET name = 'test'",
			expected: "",
		},
		{
			name:     "empty string",
			sql:      "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractInsertTableName(tt.sql)
			if result != tt.expected {
				t.Errorf("extractInsertTableName(%q) = %q, expected %q", tt.sql, result, tt.expected)
			}
		})
	}
}

func TestExtractCreateTableName(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		expected string
	}{
		{
			name:     "simple CREATE TABLE",
			sql:      "CREATE TABLE users (id INT)",
			expected: "users",
		},
		{
			name:     "CREATE TABLE IF NOT EXISTS",
			sql:      "CREATE TABLE IF NOT EXISTS users (id INT)",
			expected: "users",
		},
		{
			name:     "CREATE TABLE with schema",
			sql:      "CREATE TABLE public.users (id INT)",
			expected: "public.users",
		},
		{
			name:     "CREATE TABLE with backticks",
			sql:      "CREATE TABLE `users` (id INT)",
			expected: "users",
		},
		{
			name:     "lowercase create table",
			sql:      "create table users (id int)",
			expected: "users",
		},
		{
			name:     "SELECT statement",
			sql:      "SELECT * FROM users",
			expected: "",
		},
		{
			name:     "empty string",
			sql:      "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractCreateTableName(tt.sql)
			if result != tt.expected {
				t.Errorf("extractCreateTableName(%q) = %q, expected %q", tt.sql, result, tt.expected)
			}
		})
	}
}

func TestExtractAlterTableName(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		expected string
	}{
		{
			name:     "simple ALTER TABLE",
			sql:      "ALTER TABLE users ADD COLUMN name VARCHAR(100)",
			expected: "users",
		},
		{
			name:     "ALTER TABLE with schema",
			sql:      "ALTER TABLE public.users ADD COLUMN name VARCHAR(100)",
			expected: "public.users",
		},
		{
			name:     "ALTER TABLE with backticks",
			sql:      "ALTER TABLE `users` ADD COLUMN name VARCHAR(100)",
			expected: "users",
		},
		{
			name:     "lowercase alter table",
			sql:      "alter table users add column name varchar(100)",
			expected: "users",
		},
		{
			name:     "SELECT statement",
			sql:      "SELECT * FROM users",
			expected: "",
		},
		{
			name:     "empty string",
			sql:      "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractAlterTableName(tt.sql)
			if result != tt.expected {
				t.Errorf("extractAlterTableName(%q) = %q, expected %q", tt.sql, result, tt.expected)
			}
		})
	}
}

func TestExtractDropTableName(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		expected string
	}{
		{
			name:     "simple DROP TABLE",
			sql:      "DROP TABLE users",
			expected: "users",
		},
		{
			name:     "DROP TABLE IF EXISTS",
			sql:      "DROP TABLE IF EXISTS users",
			expected: "users",
		},
		{
			name:     "DROP TABLE with schema",
			sql:      "DROP TABLE public.users",
			expected: "public.users",
		},
		{
			name:     "DROP TABLE with backticks",
			sql:      "DROP TABLE `users`",
			expected: "users",
		},
		{
			name:     "lowercase drop table",
			sql:      "drop table users",
			expected: "users",
		},
		{
			name:     "SELECT statement",
			sql:      "SELECT * FROM users",
			expected: "",
		},
		{
			name:     "empty string",
			sql:      "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractDropTableName(tt.sql)
			if result != tt.expected {
				t.Errorf("extractDropTableName(%q) = %q, expected %q", tt.sql, result, tt.expected)
			}
		})
	}
}
