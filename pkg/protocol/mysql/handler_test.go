package mysql

import (
	"testing"

	"MyProxy/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUseDB(t *testing.T) {
	tests := []struct {
		name            string
		inTransaction   bool
		wantErr         string
		expectedDBAfter string
	}{
		{
			name:            "allows UseDB",
			expectedDBAfter: "tenant_db",
		},
		{
			name:            "transaction rejects UseDB",
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
				handler: &Handler{},
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
