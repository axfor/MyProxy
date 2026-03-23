// Copyright (c) 2025 axfor

package sqlrewrite

import (
	"strings"

	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	driver "github.com/pingcap/tidb/pkg/parser/test_driver"
)

// ASTVisitor implements AST traversal and conversion
// Uses Visitor pattern to traverse MySQL AST and convert to PostgreSQL-compatible structure
type ASTVisitor struct {
	err              error
	typeMapper       *TypeMapper
	placeholderIndex int // Placeholder index ($1, $2, ...)
	functionMap      map[string]string
}

// NewASTVisitor creates a new AST visitor
func NewASTVisitor() *ASTVisitor {
	return &ASTVisitor{
		typeMapper:       NewTypeMapper(),
		placeholderIndex: 0,
		functionMap:      createFunctionMap(),
	}
}

// createFunctionMap creates MySQL → PostgreSQL function mapping table
func createFunctionMap() map[string]string {
	return map[string]string{
		// Date/Time functions
		"now":               "CURRENT_TIMESTAMP",
		"curdate":           "CURRENT_DATE",
		"current_date":      "CURRENT_DATE",
		"curtime":           "CURRENT_TIME",
		"current_time":      "CURRENT_TIME",
		"unix_timestamp":    "EXTRACT(EPOCH FROM CURRENT_TIMESTAMP)",
		"from_unixtime":     "TO_TIMESTAMP",
		"date_format":       "TO_CHAR",
		"str_to_date":       "TO_DATE",
		"date_add":          "", // Requires special handling
		"date_sub":          "", // Requires special handling
		"datediff":          "", // Requires special handling
		"timestampdiff":     "", // Requires special handling

		// String functions
		"concat":            "CONCAT",
		"concat_ws":         "CONCAT_WS",
		"length":            "LENGTH",
		"char_length":       "CHAR_LENGTH",
		"substring":         "SUBSTRING",
		"substr":            "SUBSTRING",
		"left":              "LEFT",
		"right":             "RIGHT",
		"upper":             "UPPER",
		"lower":             "LOWER",
		"trim":              "TRIM",
		"ltrim":             "LTRIM",
		"rtrim":             "RTRIM",
		"replace":           "REPLACE",
		"locate":            "POSITION",
		"instr":             "", // Requires special handling
		"find_in_set":       "", // Requires special handling

		// Math functions
		"abs":               "ABS",
		"ceil":              "CEIL",
		"ceiling":           "CEIL",
		"floor":             "FLOOR",
		"round":             "ROUND",
		"mod":               "MOD",
		"power":             "POWER",
		"pow":               "POWER",
		"sqrt":              "SQRT",
		"rand":              "RANDOM",

		// Aggregate functions
		"count":             "COUNT",
		"sum":               "SUM",
		"avg":               "AVG",
		"max":               "MAX",
		"min":               "MIN",
		"group_concat":      "STRING_AGG", // Requires special handling for parameter order

		// Conditional functions
		"if":                "", // Needs conversion to CASE WHEN
		"ifnull":            "COALESCE",
		"nullif":            "NULLIF",
		"coalesce":          "COALESCE",

		// Type conversion
		"cast":              "CAST",
		"convert":           "CAST", // Requires special handling

		// JSON functions
		"json_extract":      "", // -> or ->>
		"json_unquote":      "", // ->>
		"json_array":        "JSON_BUILD_ARRAY",
		"json_object":       "JSON_BUILD_OBJECT",

		// Insert/Identity functions
		"last_insert_id":    "lastval",
	}
}

// Enter implements ast.Visitor interface - called when entering a node
func (v *ASTVisitor) Enter(n ast.Node) (node ast.Node, skipChildren bool) {
	if v.err != nil {
		return n, true // If error already exists, skip further processing
	}

	switch node := n.(type) {
	case *ast.FuncCallExpr:
		return v.visitFuncCall(node)

	case *driver.ParamMarkerExpr:
		return v.visitParamMarker(node)

	case *ast.ColumnDef:
		return v.visitColumnDef(node)

	case *ast.SelectStmt:
		return v.visitSelect(node)

	case *ast.Limit:
		return v.visitLimit(node)

	case *ast.MatchAgainst:
		return v.visitMatchAgainst(node)

	case *ast.CreateTableStmt:
		return v.visitCreateTable(node)

	case *ast.InsertStmt:
		return v.visitInsert(node)

	case *ast.AggregateFuncExpr:
		return v.visitAggregateFunc(node)
	}

	return n, false
}

// Leave implements ast.Visitor interface - called when leaving a node
func (v *ASTVisitor) Leave(n ast.Node) (node ast.Node, ok bool) {
	return n, v.err == nil
}

// visitFuncCall handles function calls
func (v *ASTVisitor) visitFuncCall(node *ast.FuncCallExpr) (ast.Node, bool) {
	funcName := strings.ToLower(node.FnName.L)

	// Look up function mapping
	if pgFunc, exists := v.functionMap[funcName]; exists {
		if pgFunc != "" {
			// Simple function name replacement
			node.FnName = ast.NewCIStr(pgFunc)
			return node, false
		}

		// Functions requiring special handling
		switch funcName {
		case "if":
			return v.transformIF(node)
		case "date_add", "date_sub":
			return v.transformDateAddSub(node)
		case "group_concat":
			return v.transformGroupConcat(node)
		case "unix_timestamp":
			return v.transformUnixTimestamp(node)
		}
	}

	return node, false
}

// visitParamMarker handles placeholders (? → $1, $2, ...)
func (v *ASTVisitor) visitParamMarker(node *driver.ParamMarkerExpr) (ast.Node, bool) {
	// Placeholder index starts from 1
	v.placeholderIndex++
	node.Order = v.placeholderIndex
	return node, false
}

// visitColumnDef handles column definitions (type conversion for CREATE TABLE)
func (v *ASTVisitor) visitColumnDef(node *ast.ColumnDef) (ast.Node, bool) {
	if node.Tp == nil {
		return node, false
	}

	// Use TypeMapper to convert types
	// Note: pgType conversion is handled at SQL generation stage
	// Here we just traverse the AST without modifying type definitions
	_ = v.typeMapper.MySQLToPostgreSQLBoolean(node.Tp)

	return node, false
}

// visitSelect handles SELECT statements
// Note: LOCK IN SHARE MODE is already parsed as SelectLockForShare by TiDB parser
func (v *ASTVisitor) visitSelect(node *ast.SelectStmt) (ast.Node, bool) {
	return node, false
}

// visitLimit handles LIMIT clause
func (v *ASTVisitor) visitLimit(node *ast.Limit) (ast.Node, bool) {
	// MySQL: LIMIT offset, count
	// PostgreSQL: LIMIT count OFFSET offset

	// If there's an Offset, ensure correct conversion
	// TiDB Parser can already parse correctly, just need to confirm here
	return node, false
}

// transformIF handles IF(condition, true_val, false_val)
// Note: Cannot replace FuncCallExpr with CaseExpr at AST level because TiDB's
// visitor framework expects the return type to match the input type.
// The conversion to CASE WHEN is done in PostProcess instead.
// Here we just skip children so they are not double-processed.
func (v *ASTVisitor) transformIF(node *ast.FuncCallExpr) (ast.Node, bool) {
	return node, false
}

// transformDateAddSub converts DATE_ADD/DATE_SUB
// MySQL: DATE_ADD(date, INTERVAL expr unit)
// PostgreSQL: date + INTERVAL 'expr unit'
func (v *ASTVisitor) transformDateAddSub(node *ast.FuncCallExpr) (ast.Node, bool) {
	// This requires special expression building, keep as-is for now
	// In actual use, recommend users to use PostgreSQL syntax directly
	return node, false
}

// transformGroupConcat converts GROUP_CONCAT
// MySQL: GROUP_CONCAT(expr ORDER BY ... SEPARATOR ',')
// PostgreSQL: STRING_AGG(expr, ',')
func (v *ASTVisitor) transformGroupConcat(node *ast.FuncCallExpr) (ast.Node, bool) {
	// Modify function name
	node.FnName = ast.NewCIStr("STRING_AGG")

	// Parameter order needs adjustment, requires more complex handling
	// For now, simple function name replacement
	return node, false
}

// transformUnixTimestamp converts UNIX_TIMESTAMP
// MySQL: UNIX_TIMESTAMP() or UNIX_TIMESTAMP(date)
// PostgreSQL: EXTRACT(EPOCH FROM CURRENT_TIMESTAMP) or EXTRACT(EPOCH FROM date)
func (v *ASTVisitor) transformUnixTimestamp(node *ast.FuncCallExpr) (ast.Node, bool) {
	// This needs to build EXTRACT expression
	// Keep as-is for now, handle at SQL generation stage
	return node, false
}

// GetError returns any errors encountered during traversal
func (v *ASTVisitor) GetError() error {
	return v.err
}

// GetPlaceholderCount returns total placeholder count
func (v *ASTVisitor) GetPlaceholderCount() int {
	return v.placeholderIndex
}

// ResetPlaceholders resets the placeholder counter
func (v *ASTVisitor) ResetPlaceholders() {
	v.placeholderIndex = 0
}

// visitMatchAgainst handles MATCH...AGAINST full-text search expressions
// MySQL: MATCH(title, content) AGAINST('MySQL' IN BOOLEAN MODE)
// PostgreSQL: to_tsvector('simple', title || ' ' || content) @@ to_tsquery('simple', 'MySQL')
// Note: Conversion is done in PostProcess stage, here we just traverse without modification
func (v *ASTVisitor) visitMatchAgainst(node *ast.MatchAgainst) (ast.Node, bool) {
	// Just traverse the node, don't do any conversion
	// Actual MATCH...AGAINST conversion is done in PGGenerator.PostProcess()
	// This avoids issues caused by type conversion
	return node, false
}

// visitCreateTable handles CREATE TABLE statements
// Removes INDEX/KEY constraints, MySQL table options, converts column types at AST level
func (v *ASTVisitor) visitCreateTable(node *ast.CreateTableStmt) (ast.Node, bool) {
	// Filter out INDEX and KEY constraints
	filteredConstraints := make([]*ast.Constraint, 0, len(node.Constraints))
	for _, constraint := range node.Constraints {
		if constraint.Tp != ast.ConstraintIndex && constraint.Tp != ast.ConstraintKey {
			if constraint.Tp == ast.ConstraintUniq {
				constraint.Name = ""
			}
			filteredConstraints = append(filteredConstraints, constraint)
		}
	}
	node.Constraints = filteredConstraints

	// Remove MySQL-specific table options at AST level
	// (ENGINE, CHARSET, COLLATE, AUTO_INCREMENT value, ROW_FORMAT, etc.)
	if len(node.Options) > 0 {
		filtered := make([]*ast.TableOption, 0)
		for _, opt := range node.Options {
			switch opt.Tp {
			case ast.TableOptionEngine,
				ast.TableOptionCharset,
				ast.TableOptionCollate,
				ast.TableOptionAutoIncrement,
				ast.TableOptionRowFormat,
				ast.TableOptionKeyBlockSize,
				ast.TableOptionCompression:
				// Skip MySQL-specific options
			case ast.TableOptionComment:
				// Keep COMMENT (PostgreSQL supports it via separate command, but we keep it for reference)
				filtered = append(filtered, opt)
			default:
				filtered = append(filtered, opt)
			}
		}
		node.Options = filtered
	}

	// Convert column types at AST level
	for _, col := range node.Cols {
		v.convertColumnType(col)
	}

	return node, false
}

// convertColumnType converts MySQL column types to PostgreSQL equivalents at AST level
// This is the correct approach - modify the type structure, not string replacement
// Prevents issues where column names contain type keywords (e.g., "tinyint_value", "bigint_id")
func (v *ASTVisitor) convertColumnType(col *ast.ColumnDef) {
	if col.Tp == nil {
		return
	}

	tp := col.Tp

	// Convert MySQL types to PostgreSQL types
	// All type conversions are done at AST level to ensure accuracy and prevent
	// column name conflicts (e.g., "datetime_field" won't become "timestamp_field")
	switch tp.GetType() {
	case mysql.TypeTiny:
		// TINYINT -> SMALLINT
		tp.SetType(mysql.TypeShort)

	case mysql.TypeInt24:
		// MEDIUMINT -> INTEGER
		tp.SetType(mysql.TypeLong)

	case mysql.TypeDatetime:
		// DATETIME -> TIMESTAMP
		tp.SetType(mysql.TypeTimestamp)

	case mysql.TypeEnum:
		// ENUM -> VARCHAR(50)
		// Save original enum values for documentation
		tp.SetType(mysql.TypeVarchar)
		tp.SetFlen(50)
		// Clear enum elements
		tp.SetElems(nil)

	case mysql.TypeTinyBlob:
		// TINYBLOB -> BYTEA (PostgreSQL binary type)
		tp.SetType(mysql.TypeBlob)

	case mysql.TypeMediumBlob:
		// MEDIUMBLOB -> BYTEA
		tp.SetType(mysql.TypeBlob)

	case mysql.TypeLongBlob:
		// LONGBLOB -> BYTEA
		tp.SetType(mysql.TypeBlob)

	case mysql.TypeYear:
		// YEAR -> SMALLINT (PostgreSQL has no YEAR type)
		tp.SetType(mysql.TypeShort)

	// Note: The following types cannot be fully handled at AST level:
	//
	// TEXT types (TINYTEXT, MEDIUMTEXT, LONGTEXT):
	//   - TiDB Parser doesn't have separate type constants for these
	//   - All are parsed as TypeString with different Flen values
	//   - Cannot reliably distinguish from VARCHAR at AST level
	//   - Must remain in string-based PostProcess with replaceWord()
	//
	// Other types that remain in PostProcess (low risk):
	//   - BLOB -> BYTEA (simple 1:1 mapping)
	//   - DOUBLE -> DOUBLE PRECISION (simple suffix addition)
	//   - JSON -> JSONB (simple 1:1 mapping)
	//
	// These use replaceWord() with word boundary checking, so risk is minimal
	}

	// Remove ZEROFILL flag at AST level (PostgreSQL doesn't support it)
	if mysql.HasZerofillFlag(tp.GetFlag()) {
		tp.DelFlag(mysql.ZerofillFlag)
	}

	// Handle UNSIGNED flag
	// MySQL UNSIGNED types need larger PostgreSQL types to accommodate the range
	if mysql.HasUnsignedFlag(tp.GetFlag()) {
		switch tp.GetType() {
		case mysql.TypeShort:
			// SMALLINT UNSIGNED (0-65535) -> INTEGER
			tp.SetType(mysql.TypeLong)
			tp.DelFlag(mysql.UnsignedFlag)

		case mysql.TypeLong:
			// INT UNSIGNED (0-4294967295) -> BIGINT
			tp.SetType(mysql.TypeLonglong)
			tp.DelFlag(mysql.UnsignedFlag)

		case mysql.TypeLonglong:
			// BIGINT UNSIGNED (0-18446744073709551615) -> NUMERIC(20,0)
			tp.SetType(mysql.TypeNewDecimal)
			tp.SetFlen(20)
			tp.SetDecimal(0)
			tp.DelFlag(mysql.UnsignedFlag)
		}
	}

	// Handle AUTO_INCREMENT at AST level
	// MySQL: INT AUTO_INCREMENT -> PostgreSQL: SERIAL
	// This prevents column names like "auto_increment_id" from being modified
	for _, opt := range col.Options {
		if opt.Tp == ast.ColumnOptionAutoIncrement {
			// Convert to corresponding SERIAL type based on current type
			switch tp.GetType() {
			case mysql.TypeShort:
				// SMALLINT AUTO_INCREMENT -> SMALLSERIAL
				// Mark for SERIAL conversion (will be handled in RestoreSQL)
				// For now, we keep the type and let PostProcess handle it
				// TODO: Ideally should be handled purely in AST

			case mysql.TypeLong:
				// INT AUTO_INCREMENT -> SERIAL
				// Keep type as TypeLong, PostProcess will convert to SERIAL

			case mysql.TypeLonglong:
				// BIGINT AUTO_INCREMENT -> BIGSERIAL
				// Keep type as TypeLonglong, PostProcess will convert to BIGSERIAL
			}
			// Note: AUTO_INCREMENT conversion still needs PostProcess because
			// TiDB's RestoreSQL doesn't have a way to output SERIAL directly
			// We handle AUTO_INCREMENT flag recognition here at AST level
			// but final conversion happens in convertAutoIncrement()
		}
	}
}

// visitInsert handles INSERT statements
// Converts ON DUPLICATE KEY UPDATE → ON CONFLICT ... DO UPDATE SET at AST level
// Note: TiDB RestoreSQL outputs ON DUPLICATE KEY UPDATE (MySQL syntax),
// so the actual conversion must happen in PostProcess.
// Here we just validate the AST structure.
func (v *ASTVisitor) visitInsert(node *ast.InsertStmt) (ast.Node, bool) {
	// ON DUPLICATE KEY UPDATE is parsed into node.OnDuplicate
	// TiDB's RestoreSQL will output it in MySQL format.
	// PostProcess will convert it to ON CONFLICT DO UPDATE SET.
	// We keep the AST traversal here so child nodes (e.g., function calls
	// in assignment expressions) are still visited and transformed.
	return node, false
}

// visitAggregateFunc handles aggregate function expressions
// Converts GROUP_CONCAT to STRING_AGG at AST level
// MySQL: GROUP_CONCAT(col ORDER BY col2 SEPARATOR ',')
// PostgreSQL: STRING_AGG(col, ',' ORDER BY col2)
func (v *ASTVisitor) visitAggregateFunc(node *ast.AggregateFuncExpr) (ast.Node, bool) {
	if strings.ToLower(node.F) != "group_concat" {
		return node, false
	}

	// Change function name to STRING_AGG
	node.F = "STRING_AGG"

	// STRING_AGG requires exactly 2 arguments: (expression, separator)
	// GROUP_CONCAT has the separator embedded in the SEPARATOR clause,
	// not as an argument.
	// TiDB's RestoreSQL for AggregateFuncExpr with F="STRING_AGG" will
	// output it with the args directly. We need PostProcess to handle
	// the SEPARATOR extraction because TiDB's restore doesn't understand
	// STRING_AGG syntax.
	// However, renaming the function at AST level is still valuable.

	return node, false
}
