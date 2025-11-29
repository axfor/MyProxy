package sqlrewrite

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Rewriter is the main SQL rewriter using AST-based rewriting
type Rewriter struct {
	enabled             bool
	astRewriter         *ASTRewriter
	unsupportedDetector *UnsupportedDetector
	debugTiming         bool // Enable timing logs for debugging performance
	slowThreshold       time.Duration // Log SQL if rewrite takes longer than this
}

// NewRewriter creates a rewriter with AST rewriter
func NewRewriter(enabled bool) *Rewriter {
	return &Rewriter{
		enabled:             enabled,
		astRewriter:         NewASTRewriter(),
		unsupportedDetector: NewUnsupportedDetector(),
		debugTiming:         false,                   // Set to true to enable timing logs
		slowThreshold:       100 * time.Millisecond, // Log SQL if rewrite takes longer than this
	}
}

// EnableDebugTiming enables timing logs for debugging performance
func (r *Rewriter) EnableDebugTiming(threshold time.Duration) {
	r.debugTiming = true
	if threshold > 0 {
		r.slowThreshold = threshold
	}
}

// DisableDebugTiming disables timing logs
func (r *Rewriter) DisableDebugTiming() {
	r.debugTiming = false
}

// Rewrite rewrites a MySQL SQL statement to PostgreSQL using AST rewriter
func (r *Rewriter) Rewrite(sql string) (string, error) {
	if !r.enabled {
		return sql, nil
	}

	var start time.Time
	if r.debugTiming {
		start = time.Now()
	}

	sql = strings.TrimSpace(sql)
	sqlLen := len(sql)

	// Pre-process: Strip TiDB-specific syntax
	sql = r.stripTiDBSyntax(sql)

	var result string
	var err error
	var method string

	if r.astRewriter != nil {
		// Use AST rewriter
		rewritten, astErr := r.astRewriter.Rewrite(sql)
		if astErr == nil {
			result = rewritten
			method = "ast"
		} else {
			// Parser failed - fall back to basic string substitution
			fmt.Fprintf(os.Stderr, "AST rewriter failed: %v, falling back to basic string substitution\n", astErr)
			result = r.fastPathRewrite(sql)
			method = "fallback"
		}
	} else {
		result = sql
		method = "none"
	}

	// Log timing info for slow queries or all queries if debug enabled
	if r.debugTiming {
		elapsed := time.Since(start)
		if elapsed >= r.slowThreshold {
			fmt.Fprintf(os.Stderr, "[SLOW_REWRITE] method=%s len=%d time=%v sql_prefix=%.100s\n",
				method, sqlLen, elapsed, sql)
		}
	}

	return result, err
}

// fastPathRewrite performs fast string-based SQL rewriting without AST parsing
// Used as fallback when AST parser fails
func (r *Rewriter) fastPathRewrite(sql string) string {
	// Convert backticks to double quotes for identifiers
	result := strings.ReplaceAll(sql, "`", "\"")

	// Remove charset prefixes (e.g., _UTF8MB4'text' -> 'text')
	result = r.removeCharsetPrefixes(result)

	// Convert ? placeholders to $n format
	result = r.convertPlaceholders(result)

	return result
}

// removeCharsetPrefixes removes MySQL charset prefixes like _UTF8MB4, _UTF8, etc.
func (r *Rewriter) removeCharsetPrefixes(sql string) string {
	// Quick check: if no underscore, nothing to do
	if !strings.Contains(sql, "_") {
		return sql
	}

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

	var result strings.Builder
	result.Grow(len(sql))

	i := 0
	for i < len(sql) {
		if sql[i] == '_' && i+2 < len(sql) {
			matched := false
			for _, prefix := range prefixes {
				if len(sql)-i > len(prefix) && strings.HasPrefix(sql[i:], prefix) {
					nextChar := sql[i+len(prefix)]
					if nextChar == '\'' || nextChar == '"' {
						// Skip the charset prefix
						i += len(prefix)
						matched = true
						break
					}
				}
			}
			if matched {
				continue
			}
		}
		result.WriteByte(sql[i])
		i++
	}

	return result.String()
}

// convertPlaceholders converts MySQL ? placeholders to PostgreSQL $n format
func (r *Rewriter) convertPlaceholders(sql string) string {
	// Quick check: if no ?, nothing to do
	if !strings.Contains(sql, "?") {
		return sql
	}

	var result strings.Builder
	result.Grow(len(sql) + len(sql)/10) // Reserve extra space for $n

	paramNum := 0
	inString := false
	stringChar := byte(0)

	for i := 0; i < len(sql); i++ {
		c := sql[i]

		// Track string literals to avoid replacing ? inside strings
		if !inString && (c == '\'' || c == '"') {
			inString = true
			stringChar = c
			result.WriteByte(c)
			continue
		}
		if inString && c == stringChar {
			// Check for escaped quote
			if i+1 < len(sql) && sql[i+1] == stringChar {
				result.WriteByte(c)
				result.WriteByte(c)
				i++
				continue
			}
			inString = false
			result.WriteByte(c)
			continue
		}

		// Replace ? with $n when not inside a string
		if !inString && c == '?' {
			paramNum++
			result.WriteString(fmt.Sprintf("$%d", paramNum))
			continue
		}

		result.WriteByte(c)
	}

	return result.String()
}

// DetectUnsupported detects unsupported MySQL features in SQL
func (r *Rewriter) DetectUnsupported(sql string) []UnsupportedFeature {
	if r.unsupportedDetector == nil {
		return nil
	}
	return r.unsupportedDetector.Detect(sql)
}

// RewritePrepared rewrites a prepared statement and returns the parameter count
func (r *Rewriter) RewritePrepared(sql string) (string, int, error) {
	rewritten, err := r.Rewrite(sql)
	if err != nil {
		return "", 0, err
	}

	// Count placeholders ($ followed by digits)
	paramCount := 0
	for i := 0; i < len(rewritten); i++ {
		if rewritten[i] == '$' && i+1 < len(rewritten) && rewritten[i+1] >= '0' && rewritten[i+1] <= '9' {
			// Found a parameter placeholder
			// Parse the number to get the highest parameter index
			j := i + 1
			num := 0
			for j < len(rewritten) && rewritten[j] >= '0' && rewritten[j] <= '9' {
				num = num*10 + int(rewritten[j]-'0')
				j++
			}
			if num > paramCount {
				paramCount = num
			}
		}
	}

	return rewritten, paramCount, nil
}

// Helper methods for statement type checking

func (r *Rewriter) IsShowStatement(sql string) bool {
	upperSQL := strings.ToUpper(strings.TrimSpace(sql))
	return strings.HasPrefix(upperSQL, "SHOW ") ||
		strings.HasPrefix(upperSQL, "DESCRIBE ") ||
		strings.HasPrefix(upperSQL, "DESC ")
}

func (r *Rewriter) IsSetStatement(sql string) bool {
	upperSQL := strings.ToUpper(strings.TrimSpace(sql))
	return strings.HasPrefix(upperSQL, "SET ")
}

func (r *Rewriter) IsUseStatement(sql string) bool {
	upperSQL := strings.ToUpper(strings.TrimSpace(sql))
	return strings.HasPrefix(upperSQL, "USE ")
}

func (r *Rewriter) IsBeginStatement(sql string) bool {
	upperSQL := strings.ToUpper(strings.TrimSpace(sql))
	return upperSQL == "BEGIN" ||
		upperSQL == "START TRANSACTION" ||
		strings.HasPrefix(upperSQL, "BEGIN ") ||
		strings.HasPrefix(upperSQL, "START TRANSACTION ")
}

func (r *Rewriter) IsCommitStatement(sql string) bool {
	upperSQL := strings.ToUpper(strings.TrimSpace(sql))
	return upperSQL == "COMMIT" ||
		strings.HasPrefix(upperSQL, "COMMIT ")
}

func (r *Rewriter) IsRollbackStatement(sql string) bool {
	upperSQL := strings.ToUpper(strings.TrimSpace(sql))
	return upperSQL == "ROLLBACK" ||
		strings.HasPrefix(upperSQL, "ROLLBACK ")
}

// stripTiDBSyntax removes TiDB-specific syntax that PostgreSQL doesn't support
func (r *Rewriter) stripTiDBSyntax(sql string) string {
	// Strip CLUSTERED INDEX syntax (TiDB-specific)
	// Example: "CREATE TABLE foo (...) CLUSTERED INDEX idx_name" -> "CREATE TABLE foo (...)"
	sql = strings.ReplaceAll(sql, " CLUSTERED ", " ")

	// Strip NONCLUSTERED (TiDB-specific)
	sql = strings.ReplaceAll(sql, " NONCLUSTERED ", " ")

	return sql
}
