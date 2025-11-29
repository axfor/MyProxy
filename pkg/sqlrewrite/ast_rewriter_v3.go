package sqlrewrite

import (
	"fmt"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
)

// ASTRewriter AST-based SQL rewriter
// This is the main class that integrates TypeMapper, ASTVisitor and PGGenerator
type ASTRewriter struct {
	parser    *parser.Parser
	visitor   *ASTVisitor
	generator *PGGenerator
	enabled   bool
}

// NewASTRewriter creates a new AST rewriter
func NewASTRewriter() *ASTRewriter {
	return &ASTRewriter{
		parser:    parser.New(),
		visitor:   NewASTVisitor(),
		generator: NewPGGenerator(),
		enabled:   true,
	}
}

// Rewrite rewrites MySQL SQL to PostgreSQL SQL
// This is the main public API
func (r *ASTRewriter) Rewrite(sql string) (result string, retErr error) {
	// Recover from panics in TiDB parser (parser has bugs with extreme placeholder patterns)
	defer func() {
		if rec := recover(); rec != nil {
			retErr = fmt.Errorf("parser panic: %v", rec)
		}
	}()

	if !r.enabled {
		return sql, nil
	}

	// Step 0: Pre-process - remove MySQL charset prefixes BEFORE parsing
	// This dramatically improves TiDB parser performance for SQL with many _UTF8MB4 prefixes
	sql = r.preProcessCharsetPrefixes(sql)

	// Step 1: Parse MySQL SQL to AST
	stmts, _, err := r.parser.Parse(sql, "", "")
	if err != nil {
		return "", fmt.Errorf("failed to parse SQL: %w", err)
	}

	if len(stmts) == 0 {
		return "", fmt.Errorf("no statements found in SQL")
	}

	// Currently only handles single statement
	stmt := stmts[0]

	// Step 2: Traverse and transform AST
	// Reset visitor state
	r.visitor.ResetPlaceholders()

	// Use visitor to traverse and transform AST
	stmt.Accept(r.visitor)

	if err := r.visitor.GetError(); err != nil {
		return "", fmt.Errorf("AST transformation failed: %w", err)
	}

	// Step 3: Generate PostgreSQL SQL from transformed AST
	pgSQL, paramCount, err := r.generator.GenerateWithPlaceholders(stmt)
	if err != nil {
		return "", fmt.Errorf("SQL generation failed: %w", err)
	}

	// Step 4: Post-processing
	pgSQL = r.generator.PostProcess(pgSQL)

	// Record placeholder count (for debugging)
	_ = paramCount

	return pgSQL, nil
}

// RewriteBatch rewrites multiple SQL statements in batch
func (r *ASTRewriter) RewriteBatch(sqls []string) ([]string, error) {
	results := make([]string, len(sqls))

	for i, sql := range sqls {
		rewritten, err := r.Rewrite(sql)
		if err != nil {
			return nil, fmt.Errorf("failed to rewrite statement %d: %w", i, err)
		}
		results[i] = rewritten
	}

	return results, nil
}

// Enable activates the AST rewriter
func (r *ASTRewriter) Enable() {
	r.enabled = true
}

// Disable deactivates the AST rewriter (will return original SQL directly)
func (r *ASTRewriter) Disable() {
	r.enabled = false
}

// IsEnabled checks if the AST rewriter is enabled
func (r *ASTRewriter) IsEnabled() bool {
	return r.enabled
}

// preProcessCharsetPrefixes removes MySQL charset prefixes before AST parsing
// This dramatically improves parser performance for SQL with many _UTF8MB4 prefixes
// MySQL: _UTF8MB4'text', _UTF8'text', _LATIN1'text'
// Result: 'text'
func (r *ASTRewriter) preProcessCharsetPrefixes(sql string) string {
	// Quick check: if no underscore, nothing to do
	if !strings.Contains(sql, "_") {
		return sql
	}

	var result strings.Builder
	result.Grow(len(sql))

	i := 0
	for i < len(sql) {
		// Look for underscore that might start a charset prefix
		if sql[i] == '_' && i+2 < len(sql) {
			// Check if this is a charset prefix followed by quote
			prefixLen := r.matchCharsetPrefix(sql[i:])
			if prefixLen > 0 {
				// Skip the charset prefix, keep the quote
				i += prefixLen
				continue
			}
		}
		result.WriteByte(sql[i])
		i++
	}

	return result.String()
}

// matchCharsetPrefix checks if the string starts with a charset prefix followed by a quote
// Returns the length of the prefix (not including the quote) if matched, 0 otherwise
func (r *ASTRewriter) matchCharsetPrefix(s string) int {
	// Charset prefixes in order of most common first for TPC-C workload
	prefixes := []string{
		"_UTF8MB4", "_utf8mb4",
		"_UTF8", "_utf8",
		"_BINARY", "_binary",
		"_LATIN1", "_latin1",
		"_ASCII", "_ascii",
		"_UCS2", "_ucs2",
		"_UTF16", "_utf16",
		"_UTF32", "_utf32",
	}

	for _, prefix := range prefixes {
		if len(s) > len(prefix) && strings.HasPrefix(s, prefix) {
			nextChar := s[len(prefix)]
			if nextChar == '\'' || nextChar == '"' {
				return len(prefix)
			}
		}
	}
	return 0
}
