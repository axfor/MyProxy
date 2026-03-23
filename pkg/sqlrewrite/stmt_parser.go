package sqlrewrite

import (
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/format"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

// StmtParser provides AST-based MySQL statement parsing for the protocol handler.
// It extracts structured information from SQL statements that need special handling.
type StmtParser struct {
	parser *parser.Parser
}

// NewStmtParser creates a new statement parser
func NewStmtParser() *StmtParser {
	return &StmtParser{
		parser: parser.New(),
	}
}

// KillInfo holds parsed KILL statement information
type KillInfo struct {
	ConnectionID uint64
	QueryOnly    bool // true = KILL QUERY (cancel query only), false = KILL CONNECTION
}

// SetVarInfo holds parsed SET statement variable information
type SetVarInfo struct {
	Name     string
	Value    string
	IsGlobal bool
	IsSystem bool
}

// ParseKill parses a KILL statement and returns structured info.
// Returns nil if the SQL is not a KILL statement.
func (p *StmtParser) ParseKill(sql string) *KillInfo {
	stmts, _, err := p.parser.Parse(sql, "", "")
	if err != nil || len(stmts) == 0 {
		return nil
	}

	kill, ok := stmts[0].(*ast.KillStmt)
	if !ok {
		return nil
	}

	return &KillInfo{
		ConnectionID: kill.ConnectionID,
		QueryOnly:    kill.Query,
	}
}

// ParseSet parses a SET statement and returns variable assignments.
// Returns nil if the SQL is not a SET statement.
func (p *StmtParser) ParseSet(sql string) []*SetVarInfo {
	stmts, _, err := p.parser.Parse(sql, "", "")
	if err != nil || len(stmts) == 0 {
		return nil
	}

	setStmt, ok := stmts[0].(*ast.SetStmt)
	if !ok {
		return nil
	}

	var vars []*SetVarInfo
	for _, v := range setStmt.Variables {
		info := &SetVarInfo{
			Name:     v.Name,
			IsGlobal: v.IsGlobal,
			IsSystem: v.IsSystem,
		}

		// Extract the value as string
		if v.Value != nil {
			info.Value = exprToString(v.Value)
		}

		vars = append(vars, info)
	}

	return vars
}

// FlushInfo holds parsed FLUSH statement information
type FlushInfo struct {
	Type ast.FlushStmtType
}

// ParseFlush parses a FLUSH statement.
// Returns nil if the SQL is not a FLUSH statement.
func (p *StmtParser) ParseFlush(sql string) *FlushInfo {
	stmts, _, err := p.parser.Parse(sql, "", "")
	if err != nil || len(stmts) == 0 {
		return nil
	}

	flush, ok := stmts[0].(*ast.FlushStmt)
	if !ok {
		return nil
	}

	return &FlushInfo{Type: flush.Tp}
}

// SelectVarInfo holds information about a SELECT @@variable query
type SelectVarInfo struct {
	VarName  string
	IsGlobal bool
}

// ParseSelectVariable checks if a SQL is a simple SELECT @@variable query.
// Returns nil if it's not.
func (p *StmtParser) ParseSelectVariable(sql string) *SelectVarInfo {
	stmts, _, err := p.parser.Parse(sql, "", "")
	if err != nil || len(stmts) == 0 {
		return nil
	}

	sel, ok := stmts[0].(*ast.SelectStmt)
	if !ok || sel.From != nil {
		// Not a simple SELECT (has FROM clause)
		return nil
	}

	// Check if it's a single field that is a system variable
	if sel.Fields == nil || len(sel.Fields.Fields) != 1 {
		return nil
	}

	field := sel.Fields.Fields[0]
	if field.Expr == nil {
		return nil
	}

	// Check for VariableExpr (@@global.xxx or @@xxx)
	varExpr, ok := field.Expr.(*ast.VariableExpr)
	if !ok {
		return nil
	}

	if !varExpr.IsSystem {
		return nil // not a @@system variable
	}

	return &SelectVarInfo{
		VarName:  varExpr.Name,
		IsGlobal: varExpr.IsGlobal,
	}
}

// exprToString converts an AST expression to its string representation
func exprToString(expr ast.ExprNode) string {
	switch v := expr.(type) {
	case *ast.VariableExpr:
		return v.Name
	default:
		// Use format.RestoreCtx to get the string representation
		var sb strings.Builder
		ctx := format.NewRestoreCtx(format.DefaultRestoreFlags, &sb)
		if err := expr.Restore(ctx); err != nil {
			return ""
		}
		result := sb.String()
		// Remove surrounding quotes if present
		result = strings.Trim(result, "'\"")
		return result
	}
}
