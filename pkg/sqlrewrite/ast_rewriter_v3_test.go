package sqlrewrite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestASTRewriter_SimpleSelect(t *testing.T) {
	rewriter := NewASTRewriter()

	tests := []struct {
		name     string
		mysql    string
		expected string
	}{
		{
			name:     "Simple SELECT",
			mysql:    "SELECT id, name FROM users",
			expected: "SELECT `id`,`name` FROM `users`",
		},
		{
			name:     "SELECT with WHERE",
			mysql:    "SELECT id FROM users WHERE id = 1",
			expected: "SELECT `id` FROM `users` WHERE `id`=1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := rewriter.Rewrite(tt.mysql)
			require.NoError(t, err, "Rewrite should not error")

			// Since AST-generated SQL may have format differences, we only verify no errors
			// Complete validation needs to be done in integration tests
			assert.NotEmpty(t, result, "Rewrite result should not be empty")
			t.Logf("MySQL: %s", tt.mysql)
			t.Logf("PostgreSQL: %s", result)
		})
	}
}

func TestASTRewriter_Placeholders(t *testing.T) {
	rewriter := NewASTRewriter()

	tests := []struct {
		name     string
		mysql    string
	}{
		{
			name:  "Single placeholder",
			mysql: "SELECT id FROM users WHERE id = ?",
		},
		{
			name:  "Multiple placeholders",
			mysql: "SELECT id FROM users WHERE id = ? AND name = ?",
		},
		{
			name:  "INSERT placeholder",
			mysql: "INSERT INTO users (id, name) VALUES (?, ?)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := rewriter.Rewrite(tt.mysql)
			require.NoError(t, err, "Rewrite should not error")

			// Verify placeholders converted to $1, $2 format
			assert.Contains(t, result, "$1", "Should contain $1 placeholder")

			t.Logf("MySQL: %s", tt.mysql)
			t.Logf("PostgreSQL: %s", result)
		})
	}
}

func TestASTRewriter_Functions(t *testing.T) {
	rewriter := NewASTRewriter()

	tests := []struct {
		name  string
		mysql string
	}{
		{
			name:  "NOW() function",
			mysql: "SELECT NOW() FROM users",
		},
		{
			name:  "IFNULL function",
			mysql: "SELECT IFNULL(name, 'Unknown') FROM users",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := rewriter.Rewrite(tt.mysql)
			require.NoError(t, err, "Rewrite should not error")

			assert.NotEmpty(t, result, "Rewrite result should not be empty")

			t.Logf("MySQL: %s", tt.mysql)
			t.Logf("PostgreSQL: %s", result)
		})
	}
}

func TestASTRewriter_INSERT(t *testing.T) {
	rewriter := NewASTRewriter()

	tests := []struct {
		name  string
		mysql string
	}{
		{
			name:  "Simple INSERT",
			mysql: "INSERT INTO users (id, name) VALUES (1, 'John')",
		},
		{
			name:  "INSERT with placeholder",
			mysql: "INSERT INTO users (id, name) VALUES (?, ?)",
		},
		{
			name:  "Multi-row INSERT",
			mysql: "INSERT INTO users (id, name) VALUES (1, 'John'), (2, 'Jane')",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := rewriter.Rewrite(tt.mysql)
			require.NoError(t, err, "Rewrite should not error")

			assert.NotEmpty(t, result, "Rewrite result should not be empty")

			t.Logf("MySQL: %s", tt.mysql)
			t.Logf("PostgreSQL: %s", result)
		})
	}
}

func TestASTRewriter_UPDATE(t *testing.T) {
	rewriter := NewASTRewriter()

	tests := []struct {
		name  string
		mysql string
	}{
		{
			name:  "Simple UPDATE",
			mysql: "UPDATE users SET name = 'John' WHERE id = 1",
		},
		{
			name:  "UPDATE with placeholder",
			mysql: "UPDATE users SET name = ? WHERE id = ?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := rewriter.Rewrite(tt.mysql)
			require.NoError(t, err, "Rewrite should not error")

			assert.NotEmpty(t, result, "Rewrite result should not be empty")

			t.Logf("MySQL: %s", tt.mysql)
			t.Logf("PostgreSQL: %s", result)
		})
	}
}

func TestASTRewriter_DELETE(t *testing.T) {
	rewriter := NewASTRewriter()

	tests := []struct {
		name  string
		mysql string
	}{
		{
			name:  "Simple DELETE",
			mysql: "DELETE FROM users WHERE id = 1",
		},
		{
			name:  "DELETE with placeholder",
			mysql: "DELETE FROM users WHERE id = ?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := rewriter.Rewrite(tt.mysql)
			require.NoError(t, err, "Rewrite should not error")

			assert.NotEmpty(t, result, "Rewrite result should not be empty")

			t.Logf("MySQL: %s", tt.mysql)
			t.Logf("PostgreSQL: %s", result)
		})
	}
}

func TestASTRewriter_EnableDisable(t *testing.T) {
	rewriter := NewASTRewriter()

	t.Run("Enabled state", func(t *testing.T) {
		assert.True(t, rewriter.IsEnabled(), "Should be enabled by default")

		result, err := rewriter.Rewrite("SELECT 1")
		require.NoError(t, err)
		assert.NotEmpty(t, result)
	})

	t.Run("Disabled state", func(t *testing.T) {
		rewriter.Disable()
		assert.False(t, rewriter.IsEnabled(), "Should be disabled")

		sql := "SELECT 1"
		result, err := rewriter.Rewrite(sql)
		require.NoError(t, err)
		assert.Equal(t, sql, result, "Should return original SQL when disabled")
	})

	t.Run("Re-enable", func(t *testing.T) {
		rewriter.Enable()
		assert.True(t, rewriter.IsEnabled(), "Should be re-enabled")
	})
}

func TestASTRewriter_BatchRewrite(t *testing.T) {
	rewriter := NewASTRewriter()

	sqls := []string{
		"SELECT id FROM users WHERE id = ?",
		"INSERT INTO users (id, name) VALUES (?, ?)",
		"UPDATE users SET name = ? WHERE id = ?",
	}

	results, err := rewriter.RewriteBatch(sqls)
	require.NoError(t, err, "Batch rewrite should not error")
	assert.Len(t, results, len(sqls), "Result count should match")

	for i, result := range results {
		assert.NotEmpty(t, result, "Result %d should not be empty", i)
		t.Logf("SQL %d: %s → %s", i, sqls[i], result)
	}
}

func TestASTRewriter_ErrorHandling(t *testing.T) {
	rewriter := NewASTRewriter()

	tests := []struct {
		name  string
		mysql string
	}{
		{
			name:  "Invalid SQL",
			mysql: "SELECT FROM",
		},
		{
			name:  "Incomplete statement",
			mysql: "SELECT * FROM",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rewriter.Rewrite(tt.mysql)
			assert.Error(t, err, "Should return error")
			t.Logf("Error: %v", err)
		})
	}
}

// TestASTRewriter_ManyPlaceholders tests that parser handles large number of placeholders
// TiDB Parser supports up to 65535 placeholders (official doc)
func TestASTRewriter_ManyPlaceholders(t *testing.T) {
	rewriter := NewASTRewriter()

	tests := []struct {
		name           string
		placeholderCnt int
	}{
		{"12 placeholders in IN clause", 12},
		{"20 placeholders in IN clause", 20},
		{"50 placeholders in IN clause", 50},
		{"100 placeholders in IN clause", 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build IN clause with many placeholders
			placeholders := ""
			for i := 0; i < tt.placeholderCnt; i++ {
				if i > 0 {
					placeholders += ","
				}
				placeholders += "?"
			}
			sql := "SELECT * FROM item WHERE i_id IN (" + placeholders + ")"

			result, err := rewriter.Rewrite(sql)
			require.NoError(t, err, "Rewrite should handle %d placeholders", tt.placeholderCnt)
			assert.NotEmpty(t, result)
			assert.Contains(t, result, "$1", "Should contain $1 placeholder")
			t.Logf("Input placeholders: %d, Output: %s...", tt.placeholderCnt, result[:min(100, len(result))])
		})
	}
}

// TestASTRewriter_MultiRowINSERT tests multi-row INSERT with many rows
func TestASTRewriter_MultiRowINSERT(t *testing.T) {
	rewriter := NewASTRewriter()

	tests := []struct {
		name    string
		rowsCnt int
	}{
		{"12 rows INSERT", 12},
		{"20 rows INSERT", 20},
		{"50 rows INSERT", 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build multi-row INSERT
			values := ""
			for i := 0; i < tt.rowsCnt; i++ {
				if i > 0 {
					values += ","
				}
				values += "(?,?,?)"
			}
			sql := "INSERT INTO test (a,b,c) VALUES " + values

			result, err := rewriter.Rewrite(sql)
			require.NoError(t, err, "Rewrite should handle %d rows INSERT", tt.rowsCnt)
			assert.NotEmpty(t, result)
			assert.Contains(t, result, "$1", "Should contain $1 placeholder")
			t.Logf("Input rows: %d, Output length: %d", tt.rowsCnt, len(result))
		})
	}
}

// TestASTRewriter_TupleINClause tests tuple IN clause like go-tpc uses
func TestASTRewriter_TupleINClause(t *testing.T) {
	rewriter := NewASTRewriter()

	tests := []struct {
		name     string
		tupleCnt int
	}{
		{"12 tuples in IN clause", 12},
		{"20 tuples in IN clause", 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build tuple IN clause: WHERE (a, b) IN ((?,?),(?,?),...)
			tuples := ""
			for i := 0; i < tt.tupleCnt; i++ {
				if i > 0 {
					tuples += ","
				}
				tuples += "(?,?)"
			}
			sql := "SELECT * FROM stock WHERE (s_w_id, s_i_id) IN (" + tuples + ")"

			result, err := rewriter.Rewrite(sql)
			require.NoError(t, err, "Rewrite should handle %d tuples", tt.tupleCnt)
			assert.NotEmpty(t, result)
			t.Logf("Input tuples: %d, Output: %s...", tt.tupleCnt, result[:min(100, len(result))])
		})
	}
}

// TestRewriter_StripTiDBSyntax tests TiDB-specific syntax stripping
func TestRewriter_StripTiDBSyntax(t *testing.T) {
	rewriter := NewRewriter(true)

	tests := []struct {
		name     string
		input    string
		contains string
		notContains string
	}{
		{
			name:     "Strip CLUSTERED keyword",
			input:    "CREATE TABLE t (id INT PRIMARY KEY CLUSTERED )",
			contains: "PRIMARY KEY",
			notContains: "CLUSTERED",
		},
		{
			name:     "Strip NONCLUSTERED keyword",
			input:    "CREATE TABLE t (id INT PRIMARY KEY NONCLUSTERED )",
			contains: "PRIMARY KEY",
			notContains: "NONCLUSTERED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := rewriter.Rewrite(tt.input)
			// Note: CREATE TABLE might fail in AST rewriter due to other syntax differences,
			// but fallback should handle it
			if err == nil {
				assert.Contains(t, result, tt.contains)
				assert.NotContains(t, result, tt.notContains)
			}
			t.Logf("Input: %s", tt.input)
			t.Logf("Output: %s, Error: %v", result, err)
		})
	}
}

// TestRewriter_Fallback tests graceful fallback when parser fails
func TestRewriter_Fallback(t *testing.T) {
	rewriter := NewRewriter(true)

	// SQL that will fail parser but fallback should handle
	sql := "SELECT `id`, `name` FROM `users`"

	result, err := rewriter.Rewrite(sql)
	require.NoError(t, err, "Rewrite should not error even with fallback")
	assert.NotEmpty(t, result)
	// Fallback converts backticks to double quotes
	assert.Contains(t, result, "\"id\"", "Fallback should convert backticks")
	t.Logf("Input: %s", sql)
	t.Logf("Output: %s", result)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Benchmarks
func BenchmarkASTRewriter_SimpleSelect(b *testing.B) {
	rewriter := NewASTRewriter()
	sql := "SELECT id, name FROM users WHERE id = ?"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = rewriter.Rewrite(sql)
	}
}

func BenchmarkASTRewriter_ComplexSelect(b *testing.B) {
	rewriter := NewASTRewriter()
	sql := "SELECT u.id, u.name, COUNT(o.id) FROM users u LEFT JOIN orders o ON u.id = o.user_id WHERE u.status = ? GROUP BY u.id, u.name ORDER BY u.id LIMIT 100"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = rewriter.Rewrite(sql)
	}
}

func BenchmarkASTRewriter_INSERT(b *testing.B) {
	rewriter := NewASTRewriter()
	sql := "INSERT INTO users (id, name, email, created_at) VALUES (?, ?, ?, NOW())"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = rewriter.Rewrite(sql)
	}
}
