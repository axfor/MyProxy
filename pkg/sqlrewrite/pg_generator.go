// Copyright (c) 2025 axfor

package sqlrewrite

import (
	"fmt"
	"strings"

	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/format"
)

// PGGenerator generates PostgreSQL SQL
// Generates PostgreSQL-compatible SQL statements based on converted AST
type PGGenerator struct {
	typeMapper       *TypeMapper
	placeholderIndex int
}

// NewPGGenerator creates a new PostgreSQL SQL generator
func NewPGGenerator() *PGGenerator {
	return &PGGenerator{
		typeMapper:       NewTypeMapper(),
		placeholderIndex: 0,
	}
}

// Generate generates PostgreSQL SQL from AST
func (g *PGGenerator) Generate(node ast.StmtNode) (string, error) {
	// Reset placeholder counter
	g.placeholderIndex = 0

	// Use custom RestoreCtx to generate SQL
	var sb strings.Builder
	ctx := format.NewRestoreCtx(format.DefaultRestoreFlags, &sb)

	// Use custom placeholder formatter
	ctx = g.createPGRestoreCtx(&sb)

	if err := node.Restore(ctx); err != nil {
		return "", fmt.Errorf("failed to restore AST to SQL: %w", err)
	}

	return sb.String(), nil
}

// createPGRestoreCtx creates PostgreSQL-specific RestoreCtx
// This automatically handles placeholder conversion (? → $1, $2, ...)
func (g *PGGenerator) createPGRestoreCtx(sb *strings.Builder) *format.RestoreCtx {
	// Use PostgreSQL compatible flags
	// RestoreTiDBSpecialComment - Remove TiDB special comments
	// RestoreStringWithoutCharset - Remove charset declarations
	// RestoreNameBackQuotes - Use double quotes instead of backticks
	flags := format.RestoreStringSingleQuotes |
		format.RestoreKeyWordUppercase |
		format.RestoreNameBackQuotes

	ctx := format.NewRestoreCtx(flags, sb)

	return ctx
}

// GenerateWithPlaceholders generates SQL with PostgreSQL-style placeholders
// Returns SQL and placeholder count
func (g *PGGenerator) GenerateWithPlaceholders(node ast.StmtNode) (sql string, paramCount int, err error) {
	sql, err = g.Generate(node)
	if err != nil {
		return "", 0, err
	}

	// Convert placeholders: ? → $1, $2, $3, ...
	sql, paramCount = g.convertPlaceholders(sql)

	return sql, paramCount, nil
}

// convertPlaceholders converts MySQL-style ? placeholders to PostgreSQL-style $1, $2, ...
func (g *PGGenerator) convertPlaceholders(sql string) (string, int) {
	paramIndex := 0
	var result strings.Builder
	result.Grow(len(sql))

	inString := false
	stringChar := byte(0)
	escaped := false

	for i := 0; i < len(sql); i++ {
		ch := sql[i]

		// Handle escape characters
		if escaped {
			result.WriteByte(ch)
			escaped = false
			continue
		}

		if ch == '\\' {
			result.WriteByte(ch)
			escaped = true
			continue
		}

		// Handle string literals
		if ch == '\'' || ch == '"' {
			if !inString {
				inString = true
				stringChar = ch
			} else if ch == stringChar {
				inString = false
				stringChar = 0
			}
			result.WriteByte(ch)
			continue
		}

		// Only convert placeholders outside of strings
		if !inString && ch == '?' {
			paramIndex++
			result.WriteString(fmt.Sprintf("$%d", paramIndex))
		} else {
			result.WriteByte(ch)
		}
	}

	return result.String(), paramIndex
}

// ConvertDataType converts data type definitions (for CREATE TABLE)
// This method is used to replace type names when generating DDL
func (g *PGGenerator) ConvertDataType(mysqlType string) string {
	return g.typeMapper.MySQLToPostgreSQLString(mysqlType)
}

// ConvertCreateTable handles CREATE TABLE statement conversion specifically
func (g *PGGenerator) ConvertCreateTable(node *ast.CreateTableStmt) (string, error) {
	// Generate basic SQL first
	sql, err := g.Generate(node)
	if err != nil {
		return "", err
	}

	// Handle type conversion
	// More complex logic needed here to replace type names
	// For now, return basic SQL
	return sql, nil
}

// ConvertFunctionCall converts function calls
// This method can be used for additional function conversions when needed
func (g *PGGenerator) ConvertFunctionCall(funcName string, args []string) string {
	funcName = strings.ToLower(funcName)

	// Special function handling
	switch funcName {
	case "now":
		return "CURRENT_TIMESTAMP"
	case "curdate":
		return "CURRENT_DATE"
	case "curtime":
		return "CURRENT_TIME"
	case "ifnull":
		if len(args) == 2 {
			return fmt.Sprintf("COALESCE(%s, %s)", args[0], args[1])
		}
	case "if":
		if len(args) == 3 {
			return fmt.Sprintf("CASE WHEN %s THEN %s ELSE %s END", args[0], args[1], args[2])
		}
	}

	// Default: keep as is
	return fmt.Sprintf("%s(%s)", strings.ToUpper(funcName), strings.Join(args, ", "))
}

// PostProcess post-processes the generated SQL
// Used to handle details that cannot be converted through AST
func (g *PGGenerator) PostProcess(sql string) string {
	// Replace backticks with double quotes (identifiers)
	sql = strings.ReplaceAll(sql, "`", "\"")

	// Convert MySQL types to PostgreSQL types (including UNSIGNED handling)
	sql = g.convertTypes(sql)

	// Remove any remaining MySQL-specific keywords (after type conversion)
	sql = strings.ReplaceAll(sql, " SIGNED", "")

	// Fix UNIQUE KEY/INDEX -> UNIQUE (PostgreSQL syntax)
	// Note: constraint names are cleared at AST level in visitCreateTable(),
	// but TiDB RestoreSQL may still output "UNIQUE KEY" or "UNIQUE INDEX"
	sql = strings.ReplaceAll(sql, "UNIQUE KEY", "UNIQUE")
	sql = strings.ReplaceAll(sql, "UNIQUE INDEX", "UNIQUE")

	// Remove MySQL character set prefixes from string literals
	// Examples: _UTF8MB4'text', _UTF8'text', _LATIN1'text', etc.
	sql = g.removeCharsetPrefixes(sql)

	// Fix PostgreSQL special keywords that should not have parentheses
	sql = strings.ReplaceAll(sql, "CURRENT_TIMESTAMP()", "CURRENT_TIMESTAMP")
	sql = strings.ReplaceAll(sql, "CURRENT_DATE()", "CURRENT_DATE")
	sql = strings.ReplaceAll(sql, "CURRENT_TIME()", "CURRENT_TIME")

	// Convert IF(cond, true, false) → CASE WHEN cond THEN true ELSE false END
	// Cannot be done at AST level because TiDB visitor doesn't allow node type change
	sql = g.convertIFFunction(sql)

	// Convert AUTO_INCREMENT to SERIAL types
	// NOTE: For CREATE TABLE, AUTO_INCREMENT is handled at AST level in visitCreateTable()
	// This is kept for ALTER TABLE and other edge cases
	sql = g.convertAutoIncrement(sql)

	// Convert NULL to DEFAULT in INSERT VALUES (for AUTO_INCREMENT/SERIAL compatibility)
	sql = g.convertInsertNullToDefault(sql)

	// Convert ENUM types to VARCHAR
	// NOTE: For CREATE TABLE, ENUM conversion is handled at AST level in visitCreateTable()
	// This is kept as fallback for edge cases
	// sql = g.convertEnum(sql)

	// Remove INDEX clauses from CREATE TABLE (PostgreSQL doesn't support them inline)
	// NOTE: This is now handled at AST level in ast_visitor.go visitCreateTable()
	// Keeping the string-based method as fallback for edge cases
	// sql = g.removeIndexClauses(sql)

	// Remove MySQL-specific table options (ENGINE, CHARSET, etc.)
	// Note: Most options are now removed at AST level in visitCreateTable(),
	// this is kept as fallback for edge cases or ALTER TABLE
	sql = g.removeTableOptions(sql)

	// Convert MATCH...AGAINST to to_tsvector/to_tsquery
	sql = g.convertMatchAgainst(sql)

	// Handle @@ full-text search operator
	// Convert @@(arg1, arg2) to arg1 @@ arg2
	sql = g.convertMatchOperator(sql)

	// Convert MySQL LIMIT syntax to PostgreSQL syntax
	// MySQL: LIMIT offset, count → PostgreSQL: LIMIT count OFFSET offset
	sql = g.convertLimitSyntax(sql)

	// Note: LOCK IN SHARE MODE is already converted to FOR SHARE by TiDB parser at parse time

	// Fix implicit cross join syntax from TiDB parser
	// TiDB outputs: FROM ("table1") JOIN "table2" WHERE ...
	// PostgreSQL requires: FROM "table1", "table2" WHERE ... (or CROSS JOIN with explicit ON)
	sql = g.fixImplicitCrossJoin(sql)

	// Note: LAST_INSERT_ID() → lastval() is now handled at AST level in functionMap
	// Keep as fallback for edge cases where AST visitor may not visit (e.g., subqueries)
	sql = strings.ReplaceAll(sql, "LAST_INSERT_ID()", "lastval()")

	// Convert MySQL GROUP_CONCAT to PostgreSQL string_agg
	// MySQL: GROUP_CONCAT(col SEPARATOR 'sep') → PostgreSQL: string_agg(col, 'sep')
	sql = g.convertGroupConcat(sql)

	// Remove unsupported type length parameters (e.g., SMALLINT(1) -> SMALLINT)
	sql = g.removeUnsupportedTypeLengths(sql)

	// Note: ZEROFILL flag is now cleared at AST level in convertColumnType()
	// Keep as fallback for edge cases
	sql = strings.ReplaceAll(sql, " ZEROFILL", "")

	// Convert MySQL's || string concatenation to PostgreSQL format
	// Note: This is already handled at AST level, this is just a backup

	return sql
}

// convertIFFunction converts MySQL IF(cond, true_val, false_val) to PostgreSQL CASE WHEN cond THEN true_val ELSE false_val END
func (g *PGGenerator) convertIFFunction(sql string) string {
	result := sql
	searchPos := 0

	for {
		upperPart := strings.ToUpper(result[searchPos:])
		ifIdx := strings.Index(upperPart, "IF(")
		if ifIdx == -1 {
			break
		}
		ifIdx += searchPos

		// Make sure IF is not part of a longer word (e.g., IFNULL)
		if ifIdx > 0 {
			prev := result[ifIdx-1]
			if (prev >= 'A' && prev <= 'Z') || (prev >= 'a' && prev <= 'z') || prev == '_' {
				searchPos = ifIdx + 3
				continue
			}
		}

		// Check if inside a string
		if g.isInString(result, ifIdx) {
			searchPos = ifIdx + 3
			continue
		}

		parenStart := ifIdx + 2 // position of '('
		parenEnd := g.findMatchingParen(result, parenStart)
		if parenEnd == -1 {
			searchPos = ifIdx + 3
			continue
		}

		content := result[parenStart+1 : parenEnd]

		// Split into 3 args respecting nested parens and strings
		args := g.splitFuncArgs(content)
		if len(args) != 3 {
			searchPos = ifIdx + 3
			continue
		}

		caseExpr := fmt.Sprintf("CASE WHEN %s THEN %s ELSE %s END",
			strings.TrimSpace(args[0]),
			strings.TrimSpace(args[1]),
			strings.TrimSpace(args[2]))

		result = result[:ifIdx] + caseExpr + result[parenEnd+1:]
		searchPos = ifIdx + len(caseExpr)
	}

	return result
}

// splitFuncArgs splits function arguments by comma, respecting nested parens and strings
func (g *PGGenerator) splitFuncArgs(content string) []string {
	var args []string
	depth := 0
	inStr := false
	strChar := byte(0)
	start := 0

	for i := 0; i < len(content); i++ {
		ch := content[i]
		if inStr {
			if ch == strChar {
				inStr = false
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			inStr = true
			strChar = ch
			continue
		}
		if ch == '(' {
			depth++
		} else if ch == ')' {
			depth--
		} else if ch == ',' && depth == 0 {
			args = append(args, content[start:i])
			start = i + 1
		}
	}
	args = append(args, content[start:])
	return args
}

// convertLimitSyntax converts MySQL LIMIT offset, count to PostgreSQL LIMIT count OFFSET offset
func (g *PGGenerator) convertLimitSyntax(sql string) string {
	result := sql

	// Pattern: LIMIT number, number
	// Use regex to find and replace
	// Look for LIMIT followed by digits, comma, digits
	searchPos := 0
	for {
		upperPart := strings.ToUpper(result[searchPos:])
		limitIdx := strings.Index(upperPart, "LIMIT")
		if limitIdx == -1 {
			break
		}

		limitIdx = searchPos + limitIdx

		// Skip past LIMIT keyword
		i := limitIdx + 5

		// Skip whitespace
		for i < len(result) && (result[i] == ' ' || result[i] == '\t' || result[i] == '\n') {
			i++
		}

		// Parse first number (offset)
		start1 := i
		for i < len(result) && result[i] >= '0' && result[i] <= '9' {
			i++
		}

		if i == start1 {
			// No number found
			searchPos = limitIdx + 5
			continue
		}

		offset := result[start1:i]

		// Skip whitespace
		for i < len(result) && (result[i] == ' ' || result[i] == '\t' || result[i] == '\n') {
			i++
		}

		// Check for comma
		if i >= len(result) || result[i] != ',' {
			// No comma, not the MySQL syntax we're looking for
			searchPos = limitIdx + 5
			continue
		}

		i++ // Skip comma

		// Skip whitespace
		for i < len(result) && (result[i] == ' ' || result[i] == '\t' || result[i] == '\n') {
			i++
		}

		// Parse second number (count)
		start2 := i
		for i < len(result) && result[i] >= '0' && result[i] <= '9' {
			i++
		}

		if i == start2 {
			// No second number found
			searchPos = limitIdx + 5
			continue
		}

		count := result[start2:i]

		// Replace LIMIT offset, count with LIMIT count OFFSET offset
		newLimit := "LIMIT " + count + " OFFSET " + offset
		result = result[:limitIdx] + newLimit + result[i:]

		// Continue searching from after the replacement
		searchPos = limitIdx + len(newLimit)
	}

	return result
}

// convertGroupConcat converts MySQL GROUP_CONCAT to PostgreSQL string_agg
// MySQL: GROUP_CONCAT(col SEPARATOR 'sep') → PostgreSQL: string_agg(col, 'sep')
// MySQL: GROUP_CONCAT(col) → PostgreSQL: string_agg(col, ',')
func (g *PGGenerator) convertGroupConcat(sql string) string {
	result := sql
	searchPos := 0

	for {
		upperPart := strings.ToUpper(result[searchPos:])
		groupConcatIdx := strings.Index(upperPart, "GROUP_CONCAT")
		if groupConcatIdx == -1 {
			break
		}

		groupConcatIdx = searchPos + groupConcatIdx

		// Find opening parenthesis
		i := groupConcatIdx + 12 // len("GROUP_CONCAT")
		for i < len(result) && (result[i] == ' ' || result[i] == '\t' || result[i] == '\n') {
			i++
		}

		if i >= len(result) || result[i] != '(' {
			searchPos = groupConcatIdx + 12
			continue
		}

		openParen := i
		i++

		// Find matching closing parenthesis, handling nested parentheses
		parenCount := 1
		for i < len(result) && parenCount > 0 {
			if result[i] == '(' {
				parenCount++
			} else if result[i] == ')' {
				parenCount--
			}
			i++
		}

		if parenCount != 0 {
			// Unmatched parentheses
			searchPos = groupConcatIdx + 12
			continue
		}

		closeParen := i - 1
		content := result[openParen+1 : closeParen]

		// Check if SEPARATOR is present
		upperContent := strings.ToUpper(content)
		separatorIdx := strings.Index(upperContent, " SEPARATOR ")

		var newFunc string
		if separatorIdx != -1 {
			// Extract column and separator
			column := strings.TrimSpace(content[:separatorIdx])
			separator := strings.TrimSpace(content[separatorIdx+11:]) // len(" SEPARATOR ") = 11
			newFunc = "string_agg(" + column + ", " + separator + ")"
		} else {
			// No SEPARATOR specified, use default comma
			column := strings.TrimSpace(content)
			newFunc = "string_agg(" + column + ", ',')"
		}

		// Replace GROUP_CONCAT(...) with string_agg(...)
		result = result[:groupConcatIdx] + newFunc + result[closeParen+1:]

		// Continue searching from after the replacement
		searchPos = groupConcatIdx + len(newFunc)
	}

	return result
}

// convertMatchAgainst converts MATCH...AGAINST to to_tsvector/to_tsquery
// MySQL: MATCH(col1, col2) AGAINST('term' IN BOOLEAN MODE)
// PostgreSQL: to_tsvector('simple', col1 || ' ' || col2) @@ to_tsquery('simple', 'term')
func (g *PGGenerator) convertMatchAgainst(sql string) string {
	result := sql
	resultUpper := strings.ToUpper(result)

	// Find all MATCH(...) AGAINST(...) patterns
	for {
		matchIdx := strings.Index(resultUpper, "MATCH")
		if matchIdx == -1 {
			break
		}

		// Check if inside a string
		if g.isInString(result, matchIdx) {
			// Inside string, skip - must update both result and resultUpper
			result = result[:matchIdx] + "xMATCH" + result[matchIdx+5:]
			resultUpper = resultUpper[:matchIdx] + "xMATCH" + resultUpper[matchIdx+5:]
			continue
		}

		// Find ( after MATCH, skipping spaces
		searchStart := matchIdx + 5 // Skip "MATCH"
		for searchStart < len(result) && result[searchStart] == ' ' {
			searchStart++
		}
		if searchStart >= len(result) || result[searchStart] != '(' {
			// No left paren found, might not be MATCH...AGAINST expression - must update both
			result = result[:matchIdx] + "xMATCH" + result[matchIdx+5:]
			resultUpper = resultUpper[:matchIdx] + "xMATCH" + resultUpper[matchIdx+5:]
			continue
		}
		matchParenStart := searchStart

		// Find matching )
		matchParenEnd := g.findMatchingParen(result, matchParenStart)
		if matchParenEnd == -1 {
			break
		}

		// Extract column list
		columns := result[matchParenStart+1 : matchParenEnd]

		// Find AGAINST
		againstIdx := strings.Index(resultUpper[matchParenEnd:], "AGAINST")
		if againstIdx == -1 {
			// No AGAINST, not a MATCH...AGAINST expression, skip - must update both
			result = result[:matchIdx] + "xMATCH" + result[matchIdx+5:]
			resultUpper = resultUpper[:matchIdx] + "xMATCH" + resultUpper[matchIdx+5:]
			continue
		}
		againstIdx += matchParenEnd

		// Find ( after AGAINST, skipping spaces
		searchAgainst := againstIdx + 7 // Skip "AGAINST"
		for searchAgainst < len(result) && result[searchAgainst] == ' ' {
			searchAgainst++
		}
		if searchAgainst >= len(result) || result[searchAgainst] != '(' {
			// No paren found - must update both
			result = result[:matchIdx] + "xMATCH" + result[matchIdx+5:]
			resultUpper = resultUpper[:matchIdx] + "xMATCH" + resultUpper[matchIdx+5:]
			continue
		}
		againstParenStart := searchAgainst

		// Find matching )
		againstParenEnd := g.findMatchingParen(result, againstParenStart)
		if againstParenEnd == -1 {
			// No matching right paren found - must update both
			result = result[:matchIdx] + "xMATCH" + result[matchIdx+5:]
			resultUpper = resultUpper[:matchIdx] + "xMATCH" + resultUpper[matchIdx+5:]
			continue
		}

		// Extract search term and mode
		againstContent := strings.TrimSpace(result[againstParenStart+1 : againstParenEnd])

		// Remove IN BOOLEAN MODE, IN NATURAL LANGUAGE MODE, etc.
		searchTerm := againstContent
		if idx := strings.Index(strings.ToUpper(againstContent), " IN "); idx != -1 {
			searchTerm = strings.TrimSpace(againstContent[:idx])
		}

		// Build PostgreSQL equivalent
		// Handle multiple columns: col1, col2, col3 -> col1 || ' ' || col2 || ' ' || col3
		pgColumns := g.joinColumnsWithConcat(columns)

		// Build final expression
		pgExpr := fmt.Sprintf("to_tsvector('simple', %s) @@ to_tsquery('simple', %s)", pgColumns, searchTerm)

		// Replace
		result = result[:matchIdx] + pgExpr + result[againstParenEnd+1:]
		resultUpper = strings.ToUpper(result)
	}

	// Restore previously marked MATCH
	result = strings.ReplaceAll(result, "xMATCH", "MATCH")

	return result
}

// isInString checks if position is inside a string literal
func (g *PGGenerator) isInString(sql string, pos int) bool {
	inString := false
	stringChar := byte(0)
	escaped := false

	for i := 0; i < pos && i < len(sql); i++ {
		ch := sql[i]

		if escaped {
			escaped = false
			continue
		}

		if ch == '\\' {
			escaped = true
			continue
		}

		if ch == '\'' || ch == '"' {
			if !inString {
				inString = true
				stringChar = ch
			} else if ch == stringChar {
				inString = false
				stringChar = 0
			}
		}
	}

	return inString
}

// findMatchingParen finds the matching right parenthesis
func (g *PGGenerator) findMatchingParen(sql string, startPos int) int {
	if startPos >= len(sql) || sql[startPos] != '(' {
		return -1
	}

	count := 1
	for i := startPos + 1; i < len(sql); i++ {
		if sql[i] == '(' {
			count++
		} else if sql[i] == ')' {
			count--
			if count == 0 {
				return i
			}
		}
	}

	return -1
}

// joinColumnsWithConcat joins multiple columns with || ' ' ||
func (g *PGGenerator) joinColumnsWithConcat(columns string) string {
	// Split column names
	parts := strings.Split(columns, ",")
	if len(parts) == 1 {
		return strings.TrimSpace(parts[0])
	}

	// Join with || ' ' ||
	var result strings.Builder
	for i, part := range parts {
		if i > 0 {
			result.WriteString(" || ' ' || ")
		}
		result.WriteString(strings.TrimSpace(part))
	}

	return result.String()
}

// convertMatchOperator converts @@(arg1, arg2) to arg1 @@ arg2
func (g *PGGenerator) convertMatchOperator(sql string) string {
	// Simple regex replacement may not be enough, need to match parentheses
	// Use string processing to find @@(...) and convert
	result := sql
	for {
		// Find position of @@(
		startIdx := strings.Index(result, "@@(")
		if startIdx == -1 {
			break
		}

		// Find matching right paren
		parenCount := 1
		i := startIdx + 3 // Skip @@(
		var commaIdx int = -1

		for i < len(result) && parenCount > 0 {
			ch := result[i]
			if ch == '(' {
				parenCount++
			} else if ch == ')' {
				parenCount--
				if parenCount == 0 {
					break
				}
			} else if ch == ',' && parenCount == 1 && commaIdx == -1 {
				// Find first comma (not in nested parens)
				commaIdx = i
			}
			i++
		}

		if commaIdx == -1 || parenCount != 0 {
			// Invalid format, skip
			break
		}

		// Extract two arguments
		arg1 := strings.TrimSpace(result[startIdx+3 : commaIdx])
		arg2 := strings.TrimSpace(result[commaIdx+1 : i])

		// Build new expression: arg1 @@ arg2
		newExpr := arg1 + " @@ " + arg2

		// Replace
		result = result[:startIdx] + newExpr + result[i+1:]
	}

	return result
}

// removeCharsetPrefixes removes MySQL character set prefixes from string literals
// MySQL: _UTF8MB4'text', _UTF8'text', _LATIN1'text'
// PostgreSQL: 'text'
// Optimized: single-pass scan instead of multiple ReplaceAll calls
func (g *PGGenerator) removeCharsetPrefixes(sql string) string {
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
			prefixLen := g.matchCharsetPrefix(sql[i:])
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
func (g *PGGenerator) matchCharsetPrefix(s string) int {
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

// convertAutoIncrement converts MySQL AUTO_INCREMENT to PostgreSQL SERIAL
// MySQL: INT AUTO_INCREMENT PRIMARY KEY
// PostgreSQL: SERIAL PRIMARY KEY
func (g *PGGenerator) convertAutoIncrement(sql string) string {
	result := sql

	// First handle INT/BIGINT/SMALLINT AUTO_INCREMENT PRIMARY (covers "PRIMARY KEY" and "PRIMARY")
	// This catches patterns like: INT AUTO_INCREMENT PRIMARY KEY or INT AUTO_INCREMENT PRIMARY
	result = strings.ReplaceAll(result, "INT AUTO_INCREMENT PRIMARY", "SERIAL PRIMARY")
	result = strings.ReplaceAll(result, "BIGINT AUTO_INCREMENT PRIMARY", "BIGSERIAL PRIMARY")
	result = strings.ReplaceAll(result, "SMALLINT AUTO_INCREMENT PRIMARY", "SMALLSERIAL PRIMARY")

	// Then handle remaining INT AUTO_INCREMENT (without PRIMARY)
	result = strings.ReplaceAll(result, "INT AUTO_INCREMENT", "SERIAL")
	result = strings.ReplaceAll(result, "BIGINT AUTO_INCREMENT", "BIGSERIAL")
	result = strings.ReplaceAll(result, "SMALLINT AUTO_INCREMENT", "SMALLSERIAL")

	return result
}

// convertInsertNullToDefault converts NULL to DEFAULT in INSERT VALUES clauses
// MySQL's AUTO_INCREMENT accepts NULL and auto-generates the next value
// PostgreSQL's SERIAL requires DEFAULT instead of NULL
// Pattern: INSERT INTO table (...) VALUES (NULL, ...) -> INSERT INTO table (...) VALUES (DEFAULT, ...)
func (g *PGGenerator) convertInsertNullToDefault(sql string) string {
	result := sql
	resultUpper := strings.ToUpper(result)

	// Only process if this is an INSERT statement
	if !strings.HasPrefix(resultUpper, "INSERT") {
		return result
	}

	// Find VALUES keyword
	valuesIdx := strings.Index(resultUpper, "VALUES")
	if valuesIdx == -1 {
		return result
	}

	// Find opening parenthesis after VALUES
	searchPos := valuesIdx + 6 // Skip "VALUES"
	for searchPos < len(result) && (result[searchPos] == ' ' || result[searchPos] == '\n' || result[searchPos] == '\t') {
		searchPos++
	}

	if searchPos >= len(result) || result[searchPos] != '(' {
		return result
	}

	// Process each VALUES clause (for multi-row INSERT)
	for searchPos < len(result) {
		// Find the opening parenthesis
		parenStart := strings.IndexByte(result[searchPos:], '(')
		if parenStart == -1 {
			break
		}
		parenStart += searchPos

		// Find the matching closing parenthesis
		parenEnd := g.findMatchingParen(result, parenStart)
		if parenEnd == -1 {
			break
		}

		// Check if the VALUES clause starts with NULL (possibly with spaces)
		valueStart := parenStart + 1
		for valueStart < parenEnd && (result[valueStart] == ' ' || result[valueStart] == '\n' || result[valueStart] == '\t') {
			valueStart++
		}

		// Check if it's NULL (case-insensitive)
		if valueStart+4 <= parenEnd {
			nullCheck := strings.ToUpper(result[valueStart : valueStart+4])
			if nullCheck == "NULL" {
				// Make sure it's a complete word (followed by space, comma, or paren)
				if valueStart+4 == parenEnd || result[valueStart+4] == ',' || result[valueStart+4] == ' ' || result[valueStart+4] == '\n' || result[valueStart+4] == '\t' || result[valueStart+4] == ')' {
					// Replace NULL with DEFAULT
					result = result[:valueStart] + "DEFAULT" + result[valueStart+4:]
					// Adjust parenEnd since we changed the length
					parenEnd = parenEnd - 4 + 7 // -4 for NULL, +7 for DEFAULT
				}
			}
		}

		// Move to next VALUES clause
		searchPos = parenEnd + 1
		// Skip to next opening paren (if exists)
		for searchPos < len(result) && result[searchPos] != '(' {
			if result[searchPos] == ';' {
				// End of statement
				break
			}
			searchPos++
		}
	}

	return result
}

// convertEnum converts MySQL ENUM types to PostgreSQL VARCHAR
// MySQL: ENUM('value1', 'value2', ...)
// PostgreSQL: VARCHAR(50)
func (g *PGGenerator) convertEnum(sql string) string {
	result := sql
	searchPos := 0

	for {
		// Search from searchPos onwards
		upperPart := strings.ToUpper(result[searchPos:])
		enumIdx := strings.Index(upperPart, "ENUM")
		if enumIdx == -1 {
			break
		}

		// Adjust to be relative to full string
		enumIdx = searchPos + enumIdx

		// Check if inside a string
		if g.isInString(result, enumIdx) {
			// Skip past this occurrence
			searchPos = enumIdx + 4
			continue
		}

		// Find ( after ENUM, skipping spaces
		searchStart := enumIdx + 4 // Skip "ENUM"
		for searchStart < len(result) && result[searchStart] == ' ' {
			searchStart++
		}
		if searchStart >= len(result) || result[searchStart] != '(' {
			// No left paren found, skip past it
			searchPos = enumIdx + 4
			continue
		}

		// Find matching )
		parenEnd := g.findMatchingParen(result, searchStart)
		if parenEnd == -1 {
			// No matching paren, skip past it
			searchPos = enumIdx + 4
			continue
		}

		// Replace ENUM(...) with VARCHAR(50)
		result = result[:enumIdx] + "VARCHAR(50)" + result[parenEnd+1:]
		// Continue searching from the same position (content shifted left)
		searchPos = enumIdx
	}

	return result
}

// removeIndexClauses removes INDEX, KEY, and UNIQUE INDEX/KEY clauses from CREATE TABLE statements
// MySQL: CREATE TABLE test (..., INDEX idx_name (col), KEY idx_x (col), UNIQUE INDEX idx_code (col))
// PostgreSQL: CREATE TABLE test (...) -- indexes must be created separately
func (g *PGGenerator) removeIndexClauses(sql string) string {
	result := sql
	resultUpper := strings.ToUpper(result)

	// Only process CREATE TABLE statements
	if !strings.Contains(resultUpper, "CREATE TABLE") {
		return result
	}

	searchPos := 0
mainLoop:
	for {

		// Search from searchPos onwards
		upperPart := strings.ToUpper(result[searchPos:])

		// Look for INDEX or KEY keyword (whichever comes first)
		indexIdx := strings.Index(upperPart, "INDEX")
		keyIdx := strings.Index(upperPart, "KEY")

		// Determine which keyword to process
		var keywordIdx int
		var keywordLen int

		if indexIdx == -1 && keyIdx == -1 {
			break // No more INDEX or KEY keywords
		} else if indexIdx == -1 {
			keywordIdx = keyIdx
			keywordLen = 3 // "KEY"
		} else if keyIdx == -1 {
			keywordIdx = indexIdx
			keywordLen = 5 // "INDEX"
		} else if keyIdx < indexIdx {
			keywordIdx = keyIdx
			keywordLen = 3 // "KEY"
		} else {
			keywordIdx = indexIdx
			keywordLen = 5 // "INDEX"
		}

		// Adjust to be relative to full string
		keywordIdx = searchPos + keywordIdx

		// Check if inside a string literal
		if g.isInString(result, keywordIdx) {
			// Inside string, skip past it
			searchPos = keywordIdx + keywordLen
			continue
		}

		// FIRST: Check if this is part of UNIQUE INDEX/KEY (before checking identifier)
		// This is important because "UNIQUE INDEX" has a letter before "INDEX"
		isUniqueKeyword := false
		originalKeywordIdx := keywordIdx
		uniqueCheckPos := keywordIdx
		// Skip back to check for UNIQUE keyword
		for uniqueCheckPos > 0 && (result[uniqueCheckPos-1] == ' ' || result[uniqueCheckPos-1] == '\n' || result[uniqueCheckPos-1] == '\t') {
			uniqueCheckPos--
		}
		if uniqueCheckPos >= 6 {
			uniqueCheck := strings.ToUpper(result[uniqueCheckPos-6 : uniqueCheckPos])
			if uniqueCheck == "UNIQUE" {
				// This is UNIQUE INDEX/KEY, start removal from UNIQUE
				// Save original position for skip calculation
				keywordIdx = uniqueCheckPos - 6
				// Update keywordLen to include UNIQUE + spaces + INDEX/KEY
				keywordLen = (originalKeywordIdx - keywordIdx) + keywordLen
				isUniqueKeyword = true
			}
		}

		// SECOND: Check if this is part of an identifier (like "test_indexes")
		// But skip this check if we already determined this is UNIQUE INDEX/KEY
		if !isUniqueKeyword {
			// Must have word boundary before and after the keyword
			// Word boundaries: space, tab, newline, comma, parentheses, quotes
			// NOT word boundaries: letters, digits, underscore, backtick (inside identifier)
			if keywordIdx > 0 {
				prevChar := result[keywordIdx-1]
				// Check if previous character is part of an identifier
				// Backtick means we're inside a quoted identifier like `test_indexes`
				if ((prevChar >= 'a' && prevChar <= 'z') || (prevChar >= 'A' && prevChar <= 'Z') ||
					(prevChar >= '0' && prevChar <= '9') || prevChar == '_' || prevChar == '`') {
					// Part of an identifier, skip past it
					searchPos = keywordIdx + keywordLen
					continue
				}
			}
			if keywordIdx+keywordLen < len(result) {
				nextChar := result[keywordIdx+keywordLen]
				if (nextChar >= 'a' && nextChar <= 'z') || (nextChar >= 'A' && nextChar <= 'Z') ||
					(nextChar >= '0' && nextChar <= '9') || nextChar == '_' {
					// Part of an identifier, skip past it
					searchPos = keywordIdx + keywordLen
					continue
				}
			}
		}

		// Also check if preceded by a comma (column separator)
		removeStart := keywordIdx
		for removeStart > 0 && (result[removeStart-1] == ' ' || result[removeStart-1] == '\n' || result[removeStart-1] == '\t') {
			removeStart--
		}
		includeComma := false
		if removeStart > 0 && result[removeStart-1] == ',' {
			removeStart--
			includeComma = true
		}

		// Find the opening parenthesis after INDEX/KEY
		parenStart := keywordIdx + keywordLen // Skip "INDEX" or "KEY"
		for parenStart < len(result) && result[parenStart] != '(' {
			// If we hit a comma or closing paren before finding opening paren, this isn't a valid INDEX/KEY clause
			if result[parenStart] == ',' || result[parenStart] == ')' || result[parenStart] == ';' {
				// Not a valid INDEX/KEY clause, skip past it
				searchPos = keywordIdx + keywordLen
				continue mainLoop
			}
			parenStart++
		}

		if parenStart >= len(result) {
			// No opening paren found
			searchPos = keywordIdx + keywordLen
			continue
		}

		// Find matching closing parenthesis
		parenEnd := g.findMatchingParen(result, parenStart)
		if parenEnd == -1 {
			// No matching paren
			searchPos = keywordIdx + keywordLen
			continue
		}

		// Find the end of the INDEX clause (including trailing comma if needed)
		removeEnd := parenEnd + 1

		// Skip trailing whitespace
		for removeEnd < len(result) && (result[removeEnd] == ' ' || result[removeEnd] == '\n' || result[removeEnd] == '\t') {
			removeEnd++
		}

		// If we didn't include a leading comma but there's a trailing comma, include it
		if !includeComma && removeEnd < len(result) && result[removeEnd] == ',' {
			removeEnd++
			// Skip whitespace after the comma
			for removeEnd < len(result) && (result[removeEnd] == ' ' || result[removeEnd] == '\n' || result[removeEnd] == '\t') {
				removeEnd++
			}
		}

		// Remove the INDEX clause
		result = result[:removeStart] + result[removeEnd:]
		// Continue searching from the same position (content shifted left)
		searchPos = removeStart
	}

	return result
}

// removeTableOptions removes MySQL-specific table options
// Examples: ENGINE=InnoDB, DEFAULT CHARSET=utf8mb4, COLLATE=utf8mb4_unicode_ci, etc.
func (g *PGGenerator) removeTableOptions(sql string) string {
	result := sql

	// Remove ENGINE=xxx (at end of CREATE TABLE or before other options)
	// Pattern: ENGINE=word followed by space or semicolon or end
	engineSearchPos := 0
	for {
		upperPart := strings.ToUpper(result[engineSearchPos:])
		idx := strings.Index(upperPart, "ENGINE")
		if idx == -1 {
			break
		}

		// Adjust idx to be relative to full string
		idx = engineSearchPos + idx

		// Check if inside a string literal
		if g.isInString(result, idx) {
			// Inside string, skip past it
			engineSearchPos = idx + 6
			continue
		}

		// Skip spaces after ENGINE
		i := idx + 6
		for i < len(result) && result[i] == ' ' {
			i++
		}

		// Should have = after ENGINE
		if i >= len(result) || result[i] != '=' {
			// Not an ENGINE= clause, might be a column name, skip past it
			engineSearchPos = idx + 6
			continue
		}

		i++ // Skip =

		// Skip spaces after =
		for i < len(result) && result[i] == ' ' {
			i++
		}

		// Find end of engine name (word boundary: space, comma, semicolon, or end)
		for i < len(result) && result[i] != ' ' && result[i] != ',' && result[i] != ';' && result[i] != ')' {
			i++
		}

		// Remove ENGINE=xxx
		result = result[:idx] + result[i:]
		// Continue searching from the same position (content shifted left)
		engineSearchPos = idx
	}

	// Remove DEFAULT CHARSET=xxx, CHARSET=xxx, DEFAULT CHARACTER SET = xxx, CHARACTER SET = xxx
	// Need to handle both CHARSET= and CHARACTER SET =
	for {
		upperResult := strings.ToUpper(result)

		// Try to find CHARACTER SET first (longer pattern)
		charSetIdx := strings.Index(upperResult, "CHARACTER SET")
		charsetIdx := strings.Index(upperResult, "CHARSET")

		idx := -1
		isCharacterSet := false
		keywordLen := 0

		if charSetIdx != -1 && (charsetIdx == -1 || charSetIdx < charsetIdx) {
			idx = charSetIdx
			isCharacterSet = true
			keywordLen = 13 // len("CHARACTER SET")
		} else if charsetIdx != -1 {
			idx = charsetIdx
			isCharacterSet = false
			keywordLen = 7 // len("CHARSET")
		}

		if idx == -1 {
			break
		}

		// Check if preceded by DEFAULT
		start := idx
		if idx >= 8 {
			// Look for DEFAULT before the keyword
			searchStart := idx - 8
			if searchStart < 0 {
				searchStart = 0
			}
			prefix := strings.TrimSpace(result[searchStart:idx])
			if strings.ToUpper(prefix) == "DEFAULT" {
				start = searchStart
			}
		}

		// Skip spaces after keyword
		i := idx + keywordLen
		for i < len(result) && result[i] == ' ' {
			i++
		}

		// Should have = after keyword
		if i >= len(result) || result[i] != '=' {
			// Not a charset definition, mark and skip
			if isCharacterSet {
				result = result[:idx] + "xCHARACTERxSETx" + result[idx+keywordLen:]
			} else {
				result = result[:idx] + "xCHARSET" + result[idx+keywordLen:]
			}
			continue
		}

		i++ // Skip =

		// Skip spaces after =
		for i < len(result) && result[i] == ' ' {
			i++
		}

		// Find end of charset name
		for i < len(result) && result[i] != ' ' && result[i] != ',' && result[i] != ';' && result[i] != ')' {
			i++
		}

		// Remove [DEFAULT] CHARSET=xxx or [DEFAULT] CHARACTER SET = xxx
		result = result[:start] + result[i:]
	}

	// Restore previously marked keywords
	result = strings.ReplaceAll(result, "xCHARSET", "CHARSET")
	result = strings.ReplaceAll(result, "xCHARACTERxSETx", "CHARACTER SET")

	// Remove COLLATE=xxx
	for {
		idx := strings.Index(strings.ToUpper(result), "COLLATE")
		if idx == -1 {
			break
		}

		// Skip spaces after COLLATE
		i := idx + 7
		for i < len(result) && result[i] == ' ' {
			i++
		}

		// Should have = after COLLATE
		if i >= len(result) || result[i] != '=' {
			result = result[:idx] + "xCOLLATE" + result[idx+7:]
			continue
		}

		i++ // Skip =

		// Skip spaces after =
		for i < len(result) && result[i] == ' ' {
			i++
		}

		// Find end of collation name
		for i < len(result) && result[i] != ' ' && result[i] != ',' && result[i] != ';' && result[i] != ')' {
			i++
		}

		// Remove COLLATE=xxx
		result = result[:idx] + result[i:]
	}

	// Restore previously marked COLLATE
	result = strings.ReplaceAll(result, "xCOLLATE", "COLLATE")

	// Clean up trailing spaces and commas before )
	// Replace pattern: space/comma before ) with just )
	for {
		oldResult := result
		result = strings.ReplaceAll(result, " )", ")")
		result = strings.ReplaceAll(result, ",)", ")")
		if result == oldResult {
			break
		}
	}

	return result
}

// convertTypes converts MySQL type names to PostgreSQL equivalents
// NOTE: Most type conversions are now handled at AST level in ast_visitor.go
// This function only handles types that don't have naming ambiguity issues
func (g *PGGenerator) convertTypes(sql string) string {
	result := sql

	// NOTE: The following conversions are now handled at AST level in visitCreateTable():
	// - TINYINT -> SMALLINT
	// - MEDIUMINT -> INTEGER
	// - ENUM -> VARCHAR(50)
	// - TINYINT UNSIGNED -> SMALLINT
	// - SMALLINT UNSIGNED -> INTEGER
	// - INT UNSIGNED -> BIGINT
	// - BIGINT UNSIGNED -> NUMERIC(20,0)
	//
	// This prevents issues where column names contain type keywords
	// Example: Column "tinyint_value" won't be changed to "smallint_value"

	// The conversions below are kept for non-CREATE TABLE statements
	// or edge cases not handled by AST

	// DOUBLE -> DOUBLE PRECISION (but be careful not to double-replace)
	// Only if not already "DOUBLE PRECISION"
	result = replaceDoubleType(result)

	// NOTE: DATETIME -> TIMESTAMP conversion is now handled at AST level in visitCreateTable()
	// result = replaceWord(result, "DATETIME", "TIMESTAMP")
	// result = replaceWord(result, "datetime", "timestamp")

	// TEXT types -> TEXT
	// PostgreSQL doesn't have TINYTEXT, MEDIUMTEXT, LONGTEXT - all map to TEXT
	result = replaceWord(result, "TINYTEXT", "TEXT")
	result = replaceWord(result, "tinytext", "text")
	result = replaceWord(result, "MEDIUMTEXT", "TEXT")
	result = replaceWord(result, "mediumtext", "text")
	result = replaceWord(result, "LONGTEXT", "TEXT")
	result = replaceWord(result, "longtext", "text")

	// NOTE: TINYBLOB, MEDIUMBLOB, LONGBLOB are converted to BLOB at AST level
	// But we still need to convert BLOB -> BYTEA at the string level
	// because TiDB RestoreSQL outputs "BLOB" not "BYTEA"
	result = replaceWord(result, "BLOB", "BYTEA")
	result = replaceWord(result, "blob", "bytea")

	// JSON -> JSONB (PostgreSQL's native JSON type)
	result = replaceWord(result, "JSON", "JSONB")
	result = replaceWord(result, "json", "jsonb")

	return result
}

// replaceWord replaces whole words only (not substrings)
func replaceWord(s, oldWord, newWord string) string {
	result := s
	searchPos := 0

	for {
		// Search from searchPos onwards
		idx := strings.Index(result[searchPos:], oldWord)
		if idx == -1 {
			break
		}

		// Adjust idx to be relative to full string
		idx = searchPos + idx

		// Check if it's a whole word (not part of another word)
		before := idx == 0 || !isAlphanumeric(result[idx-1])
		after := idx+len(oldWord) >= len(result) || !isAlphanumeric(result[idx+len(oldWord)])

		if before && after {
			// It's a whole word, replace it
			result = result[:idx] + newWord + result[idx+len(oldWord):]
			// Move search position past the replacement
			searchPos = idx + len(newWord)
		} else {
			// Not a whole word, skip past this occurrence
			searchPos = idx + len(oldWord)
		}
	}

	return result
}

// isAlphanumeric checks if a byte is alphanumeric or underscore
func isAlphanumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// replaceDoubleType carefully replaces DOUBLE with DOUBLE PRECISION
func replaceDoubleType(s string) string {
	result := s
	searchPos := 0

	for {
		upperPart := strings.ToUpper(result[searchPos:])
		idx := strings.Index(upperPart, "DOUBLE")
		if idx == -1 {
			break
		}

		// Adjust idx to be relative to the full string
		idx = searchPos + idx

		// Check if already followed by PRECISION
		followIdx := idx + 6 // len("DOUBLE")
		// Skip spaces
		for followIdx < len(result) && result[followIdx] == ' ' {
			followIdx++
		}

		// Check if followed by PRECISION
		if followIdx+9 <= len(result) && strings.ToUpper(result[followIdx:followIdx+9]) == "PRECISION" {
			// Already "DOUBLE PRECISION", skip it
			searchPos = followIdx + 9
			continue
		}

		// Check if it's a whole word
		before := idx == 0 || !isAlphanumeric(result[idx-1])
		after := idx+6 >= len(result) || !isAlphanumeric(result[idx+6])

		if before && after {
			// Replace DOUBLE with DOUBLE PRECISION
			result = result[:idx] + "DOUBLE PRECISION" + result[idx+6:]
			// Move search position past the replacement
			searchPos = idx + len("DOUBLE PRECISION")
		} else {
			// Not a whole word, skip this occurrence
			searchPos = idx + 6
		}
	}

	return result
}

// fixImplicitCrossJoin fixes TiDB parser's incorrect output for implicit cross joins
// TiDB outputs: FROM ("table1") JOIN "table2" WHERE ...
// PostgreSQL requires: FROM "table1", "table2" WHERE ... (comma syntax for cross join)
// This only fixes JOIN without ON clause (implicit cross join)
//
// TiDB also outputs: FROM ("table1", (SELECT ...) AS "q"), (SELECT ...) AS "no"
// Which has outer parentheses around the first table group that need to be removed
func (g *PGGenerator) fixImplicitCrossJoin(sql string) string {
	result := sql

	// Pattern: ) JOIN " followed by WHERE or end of FROM clause (no ON)
	// We need to convert this back to comma syntax

	// Look for patterns like: ("table") JOIN "table" WHERE
	// and convert to: "table", "table" WHERE
	searchPos := 0
	for {
		// Find FROM keyword
		upperPart := strings.ToUpper(result[searchPos:])
		fromIdx := strings.Index(upperPart, "FROM ")
		if fromIdx == -1 {
			break
		}
		fromIdx = searchPos + fromIdx

		// Find the end of FROM clause by looking for outer-level keywords
		// We need to skip keywords that are inside subqueries (nested parentheses)
		fromClauseStart := fromIdx + 5 // Skip "FROM "
		fromClauseEnd := g.findOuterClauseEnd(result, fromClauseStart)

		if fromClauseEnd <= fromClauseStart {
			searchPos = fromIdx + 5
			continue
		}

		fromClause := result[fromClauseStart:fromClauseEnd]
		fixedFromClause := fromClause

		// Check if this FROM clause contains ) JOIN " pattern (implicit cross join)
		// Pattern: ("table") JOIN "table" (no ON clause)
		// Need to check for ON at outer level only
		if strings.Contains(fromClause, ") JOIN ") && !g.hasOuterOnClause(fromClause) {
			// Fix the pattern by replacing ) JOIN with ,
			// Also need to remove the leading ( before the first table
			fixedFromClause = g.fixCrossJoinInFromClause(fromClause)
		}

		// Also check for outer parentheses pattern without JOIN:
		// ("table", (SELECT ...) AS "q"), (SELECT ...) AS "no"
		// This pattern doesn't have ) JOIN but still has unnecessary outer parentheses
		fixedFromClause = g.removeLeadingTableGroupParens(fixedFromClause)

		if fixedFromClause != fromClause {
			result = result[:fromClauseStart] + fixedFromClause + result[fromClauseEnd:]
		}

		searchPos = fromIdx + 5
	}

	return result
}

// findOuterClauseEnd finds the end of FROM clause by looking for outer-level keywords
// It skips keywords inside subqueries (inside parentheses)
func (g *PGGenerator) findOuterClauseEnd(sql string, startPos int) int {
	parenDepth := 0
	i := startPos

	for i < len(sql) {
		ch := sql[i]

		// Track parenthesis depth
		if ch == '(' {
			parenDepth++
			i++
			continue
		}
		if ch == ')' {
			parenDepth--
			i++
			continue
		}

		// Only check for clause keywords at outer level (parenDepth == 0)
		if parenDepth == 0 && ch == ' ' {
			// Check for WHERE, ORDER, GROUP, LIMIT, FOR, HAVING, UNION keywords
			remaining := strings.ToUpper(sql[i:])
			if strings.HasPrefix(remaining, " WHERE ") ||
				strings.HasPrefix(remaining, " ORDER ") ||
				strings.HasPrefix(remaining, " GROUP ") ||
				strings.HasPrefix(remaining, " LIMIT ") ||
				strings.HasPrefix(remaining, " FOR ") ||
				strings.HasPrefix(remaining, " HAVING ") ||
				strings.HasPrefix(remaining, " UNION ") {
				return i
			}
		}

		i++
	}

	return len(sql)
}

// hasOuterOnClause checks if the FROM clause has an ON clause at outer level
// This is used to distinguish implicit cross joins from explicit joins
func (g *PGGenerator) hasOuterOnClause(fromClause string) bool {
	parenDepth := 0

	for i := 0; i < len(fromClause); i++ {
		ch := fromClause[i]

		if ch == '(' {
			parenDepth++
			continue
		}
		if ch == ')' {
			parenDepth--
			continue
		}

		// Only check for ON at outer level
		if parenDepth == 0 && ch == ' ' && i+4 < len(fromClause) {
			remaining := strings.ToUpper(fromClause[i:])
			if strings.HasPrefix(remaining, " ON ") {
				return true
			}
		}
	}

	return false
}

// fixCrossJoinInFromClause fixes the FROM clause content
// Input:  ("orders") JOIN "order_line"
// Output: "orders", "order_line"
// Also handles complex cases like:
// Input:  ("district" AS "dis", (SELECT ...) AS "q") JOIN (SELECT ...) AS "no"
// Output: "district" AS "dis", (SELECT ...) AS "q", (SELECT ...) AS "no"
func (g *PGGenerator) fixCrossJoinInFromClause(fromClause string) string {
	result := fromClause

	// Handle multiple JOINs iteratively
	for {
		// Find ) JOIN pattern (note: might be followed by ( for subquery)
		joinIdx := strings.Index(result, ") JOIN ")
		if joinIdx == -1 {
			break
		}

		// Replace ) JOIN with ), (comma with closing paren kept)
		result = result[:joinIdx+1] + "," + result[joinIdx+6:]
	}

	// Remove leading outer parentheses if they wrap a table group (not a subquery)
	// Pattern: ("table", (SELECT ...)) -> "table", (SELECT ...)
	result = g.removeLeadingTableGroupParens(result)

	// Now remove simple unnecessary parentheses around single table names
	// Pattern: ("table") -> "table"
	result = g.removeTableParentheses(result)

	return result
}

// removeLeadingTableGroupParens removes outer parentheses at the start of FROM clause
// that wrap a table group (table + subqueries), but not subqueries themselves
// Input:  ("district" AS "dis", (SELECT ...) AS "q"), (SELECT ...) AS "no"
// Output: "district" AS "dis", (SELECT ...) AS "q", (SELECT ...) AS "no"
func (g *PGGenerator) removeLeadingTableGroupParens(s string) string {
	result := strings.TrimSpace(s)

	for {
		if len(result) == 0 || result[0] != '(' {
			break
		}

		// Check if this is a subquery (starts with SELECT)
		content := strings.TrimSpace(result[1:])
		if g.startsWithSelect(content) {
			// This is a subquery, don't remove its parentheses
			break
		}

		// Check if content starts with a quoted identifier (table name)
		if !strings.HasPrefix(content, "\"") {
			break
		}

		// Find the matching closing parenthesis
		closeIdx := g.findMatchingParen(result, 0)
		if closeIdx == -1 {
			break
		}

		// Check if there's content after the closing paren
		afterClose := strings.TrimSpace(result[closeIdx+1:])

		// If there's nothing after or it starts with comma, remove the outer parens
		if afterClose == "" || strings.HasPrefix(afterClose, ",") {
			// Remove the outer parentheses
			innerContent := result[1:closeIdx]
			result = innerContent + result[closeIdx+1:]
			continue
		}

		// If there's something else after (like another expression), stop
		break
	}

	return result
}

// removeTableParentheses removes unnecessary parentheses around simple table names
// ("table") -> "table"
// ("table1" AS "alias") -> "table1" AS "alias"
// But keeps:
// - (SELECT ...) - subqueries need parentheses
// - ("table", (SELECT ...)) - complex expressions with nested subqueries
func (g *PGGenerator) removeTableParentheses(s string) string {
	result := s
	i := 0

	for i < len(result) {
		if result[i] == '(' {
			// Find matching )
			closeIdx := g.findMatchingParen(result, i)
			if closeIdx == -1 {
				i++
				continue
			}

			content := result[i+1 : closeIdx]
			contentTrimmed := strings.TrimSpace(content)

			// Only remove parentheses if:
			// 1. Content starts with " (quoted identifier)
			// 2. Content is NOT a SELECT subquery
			// 3. Content does NOT contain nested parentheses (to avoid complex cases)
			if strings.HasPrefix(contentTrimmed, "\"") &&
				!g.startsWithSelect(contentTrimmed) &&
				!strings.Contains(content, "(") {
				// Remove the parentheses - this is a simple table reference
				result = result[:i] + content + result[closeIdx+1:]
				// Don't increment i, check again at same position
				continue
			}
		}
		i++
	}

	return result
}

// startsWithSelect checks if content starts with SELECT (ignoring whitespace)
func (g *PGGenerator) startsWithSelect(content string) bool {
	trimmed := strings.TrimSpace(content)
	upper := strings.ToUpper(trimmed)
	return strings.HasPrefix(upper, "SELECT")
}

// removeUnsupportedTypeLengths removes length parameters from PostgreSQL integer types
// PostgreSQL integer types (SMALLINT, INTEGER, BIGINT) don't support length parameters
// MySQL: SMALLINT(1), INT(11) -> PostgreSQL: SMALLINT, INTEGER
func (g *PGGenerator) removeUnsupportedTypeLengths(sql string) string {
	result := sql

	// List of integer types that don't support length in PostgreSQL
	intTypes := []string{"SMALLINT", "INTEGER", "BIGINT", "INT", "SERIAL", "BIGSERIAL"}

	for _, typeName := range intTypes {
		searchPos := 0
		for {
			// Find next occurrence of type name
			idx := strings.Index(strings.ToUpper(result[searchPos:]), typeName)
			if idx == -1 {
				break
			}

			idx = searchPos + idx

			// Check if it's a whole word (not part of another word)
			before := idx == 0 || !isAlphanumeric(result[idx-1])
			after := idx+len(typeName) >= len(result) || !isAlphanumeric(result[idx+len(typeName)])

			if !before || !after {
				searchPos = idx + len(typeName)
				continue
			}

			// Skip past type name and any whitespace
			i := idx + len(typeName)
			for i < len(result) && (result[i] == ' ' || result[i] == '\t' || result[i] == '\n') {
				i++
			}

			// Check if there's a length parameter (opening parenthesis)
			if i < len(result) && result[i] == '(' {
				// Find the closing parenthesis
				parenStart := i
				i++

				// Skip digits
				for i < len(result) && (result[i] >= '0' && result[i] <= '9') {
					i++
				}

				// Skip optional whitespace
				for i < len(result) && (result[i] == ' ' || result[i] == '\t' || result[i] == '\n') {
					i++
				}

				// Check if we found a closing parenthesis
				if i < len(result) && result[i] == ')' {
					parenEnd := i + 1

					// Remove the length parameter: TYPE(n) -> TYPE
					result = result[:parenStart] + result[parenEnd:]

					// Continue searching from the position after the type name
					searchPos = idx + len(typeName)
				} else {
					// Not a simple (n) pattern, skip
					searchPos = idx + len(typeName)
				}
			} else {
				// No length parameter, move on
				searchPos = idx + len(typeName)
			}
		}
	}

	return result
}
