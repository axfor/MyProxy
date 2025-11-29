package mysql

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"

	"aproxy/internal/pool"
	"aproxy/pkg/mapper"
	"aproxy/pkg/observability"
	"aproxy/pkg/replication"
	"aproxy/pkg/reqtrack"
	"aproxy/pkg/schema"
	"aproxy/pkg/session"
	"aproxy/pkg/sqlrewrite"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/server"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"
)

type Handler struct {
	pgPool       *pool.Pool
	sessionMgr   *session.Manager
	rewriter     *sqlrewrite.Rewriter
	typeMapper   *mapper.TypeMapper
	errorMapper  *mapper.ErrorMapper
	showEmulator *mapper.ShowEmulator
	metrics      *observability.Metrics
	logger       *observability.Logger
	debugSQL     bool
	replServer   *replication.Server // MySQL binlog replication server
}

func NewHandler(
	pgPool *pool.Pool,
	sessionMgr *session.Manager,
	rewriter *sqlrewrite.Rewriter,
	metrics *observability.Metrics,
	logger *observability.Logger,
	debugSQL bool,
	replServer *replication.Server,
) *Handler {
	return &Handler{
		pgPool:       pgPool,
		sessionMgr:   sessionMgr,
		rewriter:     rewriter,
		typeMapper:   mapper.NewTypeMapper(),
		errorMapper:  mapper.NewErrorMapper(),
		showEmulator: mapper.NewShowEmulator(),
		metrics:      metrics,
		logger:       logger,
		debugSQL:     debugSQL,
		replServer:   replServer,
	}
}

// SetReplicationServer sets the replication server for handling COM_BINLOG_DUMP
func (h *Handler) SetReplicationServer(server *replication.Server) {
	h.replServer = server
}

func (h *Handler) NewConnection(conn net.Conn) (server.Handler, error) {
	remoteAddr := conn.RemoteAddr().String()
	host, _, _ := net.SplitHostPort(remoteAddr)

	sess := session.NewSession("", "", host)
	h.sessionMgr.AddSession(sess)
	h.metrics.IncActiveConnections()

	return &ConnectionHandler{
		handler: h,
		session: sess,
		conn:    conn,
	}, nil
}

type ConnectionHandler struct {
	handler *Handler
	session *session.Session
	conn    net.Conn
	pgConn  *pgx.Conn
}

func (ch *ConnectionHandler) UseDB(dbName string) error {
	ch.session.Database = dbName

	if ch.pgConn != nil {
		ctx := context.Background()
		_, err := ch.pgConn.Exec(ctx, fmt.Sprintf("SET search_path TO %s", dbName))
		return err
	}

	return nil
}

func (ch *ConnectionHandler) HandleQuery(query string) (*mysql.Result, error) {
	startTime := time.Now()
	ch.handler.metrics.IncTotalQueries()

	// Track request for monitoring slow queries
	reqID := reqtrack.GetTracker().StartRequest(ch.session.ID, query)
	defer reqtrack.GetTracker().EndRequest(reqID)

	ctx := context.Background()

	if ch.pgConn == nil {
		conn, err := ch.handler.pgPool.AcquireForSession(ctx, ch.session.ID)
		if err != nil {
			ch.handler.metrics.IncErrors("connection")
			ch.handler.logger.LogError(ch.session.ID, ch.session.User, ch.session.ClientAddr, "connection", err)
			return nil, err
		}
		ch.pgConn = conn
		ch.session.SetPGConn(conn)
	}

	if ch.handler.rewriter.IsShowStatement(query) {
		return ch.handleShowCommand(ctx, query)
	}

	if ch.handler.rewriter.IsSetStatement(query) {
		return ch.handleSetCommand(ctx, query)
	}

	if ch.handler.rewriter.IsUseStatement(query) {
		return ch.handleUseCommand(ctx, query)
	}

	// Handle transaction control statements
	if ch.handler.rewriter.IsBeginStatement(query) {
		if err := ch.session.BeginTransaction(); err != nil {
			ch.handler.metrics.IncErrors("transaction")
			ch.handler.logger.LogError(ch.session.ID, ch.session.User, ch.session.ClientAddr, "begin_transaction", err)
			return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR, err.Error())
		}
		ch.handler.logger.LogQuery(ch.session.ID, ch.session.User, ch.session.ClientAddr, query, time.Since(startTime).Seconds(), 0, nil)
		return &mysql.Result{Status: 0}, nil
	}

	if ch.handler.rewriter.IsCommitStatement(query) {
		if err := ch.session.CommitTransaction(); err != nil {
			ch.handler.metrics.IncErrors("transaction")
			ch.handler.logger.LogError(ch.session.ID, ch.session.User, ch.session.ClientAddr, "commit_transaction", err)
			return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR, err.Error())
		}
		ch.handler.logger.LogQuery(ch.session.ID, ch.session.User, ch.session.ClientAddr, query, time.Since(startTime).Seconds(), 0, nil)
		return &mysql.Result{Status: 0}, nil
	}

	if ch.handler.rewriter.IsRollbackStatement(query) {
		if err := ch.session.RollbackTransaction(); err != nil {
			ch.handler.metrics.IncErrors("transaction")
			ch.handler.logger.LogError(ch.session.ID, ch.session.User, ch.session.ClientAddr, "rollback_transaction", err)
			return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR, err.Error())
		}
		ch.handler.logger.LogQuery(ch.session.ID, ch.session.User, ch.session.ClientAddr, query, time.Since(startTime).Seconds(), 0, nil)
		return &mysql.Result{Status: 0}, nil
	}

	// Handle KILL command - MySQL clients send this to terminate connections
	// PostgreSQL doesn't support KILL, so we just return OK
	if isKillStatement(query) {
		ch.handler.logger.Info("KILL command received (ignored)",
			zap.String("session_id", ch.session.ID),
			zap.String("query", query))
		ch.handler.logger.LogQuery(ch.session.ID, ch.session.User, ch.session.ClientAddr, query, time.Since(startTime).Seconds(), 0, nil)
		return &mysql.Result{Status: 0}, nil
	}

	// Detect unsupported MySQL features before rewriting
	unsupportedFeatures := ch.handler.rewriter.DetectUnsupported(query)
	if len(unsupportedFeatures) > 0 {
		for _, feature := range unsupportedFeatures {
			switch feature.Severity {
			case "error":
				ch.handler.logger.Error("Unsupported",
					zap.String("feature", feature.Feature),
					zap.String("category", feature.Category),
					zap.String("sql", feature.SQL),
					zap.String("suggestion", feature.Suggestion))
			case "warning":
				ch.handler.logger.Warn("Unsupported",
					zap.String("feature", feature.Feature),
					zap.String("category", feature.Category),
					zap.String("sql", feature.SQL),
					zap.String("suggestion", feature.Suggestion))
			case "info":
				ch.handler.logger.Info("Unsupported",
					zap.String("feature", feature.Feature),
					zap.String("category", feature.Category),
					zap.String("sql", feature.SQL),
					zap.String("suggestion", feature.Suggestion))
			}
		}
	}

	rewrittenSQL, err := ch.handler.rewriter.Rewrite(query)
	if err != nil {
		ch.handler.metrics.IncErrors("rewrite")
		return nil, err
	}

	// Debug SQL logging if enabled
	if ch.handler.debugSQL {
		wasRewritten := query != rewrittenSQL
		ch.handler.logger.Info("SQL Debug",
			zap.String("mysql", query),
			zap.String("pg", rewrittenSQL),
			zap.Bool("rewritten", wasRewritten))
	}

	// Check if this is a DDL statement (CREATE, DROP, ALTER, etc.) or DML with no result set
	upperQuery := strings.ToUpper(strings.TrimSpace(query))
	isDDL := strings.HasPrefix(upperQuery, "CREATE") ||
		strings.HasPrefix(upperQuery, "DROP") ||
		strings.HasPrefix(upperQuery, "ALTER") ||
		strings.HasPrefix(upperQuery, "TRUNCATE") ||
		strings.HasPrefix(upperQuery, "DELETE") ||
		strings.HasPrefix(upperQuery, "UPDATE") ||
		strings.HasPrefix(upperQuery, "INSERT")

	if isDDL {
		var lastInsertID uint64
		var rowsAffected int64

		// Special handling for INSERT to get last insert ID
		if strings.HasPrefix(upperQuery, "INSERT") {
			// Check if this table has an AUTO_INCREMENT column
			tableName := extractInsertTableName(query)
			autoIncrColumn := ch.session.GetAutoIncrementColumn(tableName)

			if autoIncrColumn != "" && !strings.Contains(strings.ToUpper(rewrittenSQL), "RETURNING") {
				// Table has AUTO_INCREMENT, use RETURNING to get the inserted ID
				returningSQL := rewrittenSQL + " RETURNING " + autoIncrColumn
				rows, err := ch.pgConn.Query(ctx, returningSQL)
				if err != nil {
					ch.handler.metrics.IncErrors("query")
					errorCode, errorMsg := ch.handler.errorMapper.MapError(err)
					ch.handler.logger.LogQuery(ch.session.ID, ch.session.User, ch.session.ClientAddr, query, time.Since(startTime).Seconds(), 0, err)
					return nil, mysql.NewError(errorCode, errorMsg)
				}
				defer rows.Close()
				// Get the returned ID
				if rows.Next() {
					var id int64
					if err := rows.Scan(&id); err == nil {
						lastInsertID = uint64(id)
					}
				}
				rowsAffected = 1 // INSERT with RETURNING always affects 1 row if successful
			} else {
				// Table doesn't have AUTO_INCREMENT or already has RETURNING, just execute
				cmdTag, err := ch.pgConn.Exec(ctx, rewrittenSQL)
				if err != nil {
					ch.handler.metrics.IncErrors("query")
					errorCode, errorMsg := ch.handler.errorMapper.MapError(err)
					ch.handler.logger.LogQuery(ch.session.ID, ch.session.User, ch.session.ClientAddr, query, time.Since(startTime).Seconds(), 0, err)
					return nil, mysql.NewError(errorCode, errorMsg)
				}
				rowsAffected = cmdTag.RowsAffected()
			}
		} else {
			// Use Exec for non-INSERT DDL/DML statements
			cmdTag, err := ch.pgConn.Exec(ctx, rewrittenSQL)
			if err != nil {
				ch.handler.metrics.IncErrors("query")
				errorCode, errorMsg := ch.handler.errorMapper.MapError(err)
				ch.handler.logger.LogQuery(ch.session.ID, ch.session.User, ch.session.ClientAddr, query, time.Since(startTime).Seconds(), 0, err)
				return nil, mysql.NewError(errorCode, errorMsg)
			}
			rowsAffected = cmdTag.RowsAffected()

			// Handle DDL statements: invalidate schema cache
			if strings.HasPrefix(upperQuery, "CREATE TABLE") {
				tableName := extractCreateTableName(query)

				// Invalidate cache for the new table (it will be refreshed on first INSERT)
				schema.GetGlobalCache().InvalidateTable(ch.session.Database, tableName)

				// Also mark in session for backward compatibility
				columnName := extractAutoIncrementColumn(query)
				if tableName != "" && columnName != "" {
					ch.session.MarkTableHasAutoIncrement(tableName, columnName)
				}
			} else if strings.HasPrefix(upperQuery, "ALTER TABLE") {
				// ALTER TABLE may change AUTO_INCREMENT columns
				tableName := extractAlterTableName(query)
				if tableName != "" {
					schema.GetGlobalCache().InvalidateTable(ch.session.Database, tableName)
				}
			} else if strings.HasPrefix(upperQuery, "DROP TABLE") {
				// DROP TABLE removes the table
				tableName := extractDropTableName(query)
				if tableName != "" {
					schema.GetGlobalCache().InvalidateTable(ch.session.Database, tableName)
				}
			}
		}

		// Note: Additional DDL statements are no longer needed with AST rewriter

		duration := time.Since(startTime).Seconds()
		ch.handler.metrics.ObserveQueryDuration(duration)
		ch.handler.logger.LogQuery(ch.session.ID, ch.session.User, ch.session.ClientAddr, query, duration, rowsAffected, nil)

		return &mysql.Result{
			Status:       0,
			InsertId:     lastInsertID,
			AffectedRows: uint64(rowsAffected),
		}, nil
	}

	// Use Query for SELECT statements
	rows, err := ch.pgConn.Query(ctx, rewrittenSQL)
	if err != nil {
		ch.handler.metrics.IncErrors("query")
		errorCode, errorMsg := ch.handler.errorMapper.MapError(err)
		ch.handler.logger.LogQuery(ch.session.ID, ch.session.User, ch.session.ClientAddr, query, time.Since(startTime).Seconds(), 0, err)
		return nil, mysql.NewError(errorCode, errorMsg)
	}
	defer rows.Close()

	// Use Text Protocol for regular queries
	result, err := ch.buildMySQLResult(rows, false)
	if err != nil {
		ch.handler.metrics.IncErrors("result_conversion")
		return nil, err
	}

	duration := time.Since(startTime).Seconds()
	ch.handler.metrics.ObserveQueryDuration(duration)

	rowCount := int64(0)
	if result.Resultset != nil {
		// Use RowDatas length, not Values, because BuildSimpleResultset doesn't populate Values
		rowCount = int64(len(result.Resultset.RowDatas))
	}
	ch.handler.logger.LogQuery(ch.session.ID, ch.session.User, ch.session.ClientAddr, query, duration, rowCount, nil)

	return result, nil
}

func (ch *ConnectionHandler) HandleFieldList(table string, wildcard string) ([]*mysql.Field, error) {
	ctx := context.Background()

	// Ensure we have a PostgreSQL connection
	if ch.pgConn == nil {
		conn, err := ch.handler.pgPool.AcquireForSession(ctx, ch.session.ID)
		if err != nil {
			ch.handler.metrics.IncErrors("connection")
			ch.handler.logger.LogError(ch.session.ID, ch.session.User, ch.session.ClientAddr, "connection", err)
			return nil, err
		}
		ch.pgConn = conn
		ch.session.SetPGConn(conn)
	}

	query := fmt.Sprintf(`
		SELECT column_name, data_type, character_maximum_length
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = '%s'
		ORDER BY ordinal_position
	`, table)

	rows, err := ch.pgConn.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fields []*mysql.Field
	for rows.Next() {
		var colName, dataType string
		var maxLength *int32

		if err := rows.Scan(&colName, &dataType, &maxLength); err != nil {
			return nil, err
		}

		length := uint32(255)
		if maxLength != nil {
			length = uint32(*maxLength)
		}

		field := ch.handler.typeMapper.BuildMySQLFieldPacket(colName, 0, length)
		fields = append(fields, field)
	}

	return fields, nil
}

func (ch *ConnectionHandler) HandleStmtPrepare(query string) (int, int, interface{}, error) {
	ctx := context.Background()

	// Ensure we have a PostgreSQL connection
	if ch.pgConn == nil {
		conn, err := ch.handler.pgPool.AcquireForSession(ctx, ch.session.ID)
		if err != nil {
			ch.handler.metrics.IncErrors("connection")
			ch.handler.logger.LogError(ch.session.ID, ch.session.User, ch.session.ClientAddr, "connection", err)
			return 0, 0, nil, err
		}
		ch.pgConn = conn
		ch.session.SetPGConn(conn)
	}

	rewrittenSQL, paramCount, err := ch.handler.rewriter.RewritePrepared(query)
	if err != nil {
		return 0, 0, nil, err
	}

	stmtID := uint32(ch.session.GetPreparedStatementCount() + 1)

	// Determine column count by detecting query type
	// For SELECT queries, we return a non-zero columnCount as a signal
	// The actual column metadata will be determined during execution
	columnCount := 0

	// Detect if this is a query that returns results
	trimmedUpper := strings.TrimSpace(strings.ToUpper(rewrittenSQL))
	if strings.HasPrefix(trimmedUpper, "SELECT") ||
		strings.HasPrefix(trimmedUpper, "WITH") ||
		strings.HasPrefix(trimmedUpper, "SHOW") ||
		strings.HasPrefix(trimmedUpper, "EXPLAIN") ||
		strings.HasPrefix(trimmedUpper, "DESCRIBE") ||
		strings.HasPrefix(trimmedUpper, "DESC") {
		// Use 1 as a placeholder - go-mysql will send placeholder column metadata
		// The actual columns will be sent during EXECUTE
		columnCount = 1
	}

	stmt := &session.PreparedStatement{
		ID:          stmtID,
		SQL:         rewrittenSQL,
		OriginalSQL: query,
		PGName:      "", // Not using named prepared statements
		ParamCount:  paramCount,
	}

	ch.session.AddPreparedStatement(stmt)

	return paramCount, columnCount, stmtID, nil
}

func (ch *ConnectionHandler) HandleStmtExecute(data interface{}, query string, args []interface{}) (*mysql.Result, error) {
	stmtID, ok := data.(uint32)
	if !ok {
		return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR, "invalid statement ID type")
	}

	stmt, ok := ch.session.GetPreparedStatement(stmtID)
	if !ok {
		return nil, mysql.NewError(mysql.ER_UNKNOWN_STMT_HANDLER, "Unknown prepared statement")
	}

	ctx := context.Background()

	// Ensure we have a PostgreSQL connection
	if ch.pgConn == nil {
		conn, err := ch.handler.pgPool.AcquireForSession(ctx, ch.session.ID)
		if err != nil {
			ch.handler.metrics.IncErrors("connection")
			ch.handler.logger.LogError(ch.session.ID, ch.session.User, ch.session.ClientAddr, "connection", err)
			return nil, err
		}
		ch.pgConn = conn
		ch.session.SetPGConn(conn)
	}

	// Check if this is a DML statement that doesn't return rows
	upperQuery := strings.ToUpper(strings.TrimSpace(stmt.OriginalSQL))
	isDML := strings.HasPrefix(upperQuery, "INSERT") ||
		strings.HasPrefix(upperQuery, "UPDATE") ||
		strings.HasPrefix(upperQuery, "DELETE")

	// Convert MySQL-encoded parameters to PostgreSQL-compatible format
	// MySQL client may send time.Time as binary-encoded bytes, but PostgreSQL expects strings
	convertedArgs := make([]interface{}, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case time.Time:
			// Convert time.Time to string format for PostgreSQL
			// Use format compatible with PostgreSQL's timestamp/date parsing
			convertedArgs[i] = v.Format("2006-01-02 15:04:05")
		case []byte:
			// MySQL might send dates/timestamps as byte arrays
			// Try to convert to string for PostgreSQL
			convertedArgs[i] = string(v)
		default:
			convertedArgs[i] = arg
		}
	}

	startTime := time.Now()

	if isDML {
		var lastInsertID uint64
		var rowsAffected int64

		// Special handling for INSERT to get last insert ID
		if strings.HasPrefix(upperQuery, "INSERT") {
			// Check if this table has an AUTO_INCREMENT column
			tableName := extractInsertTableName(stmt.OriginalSQL)
			autoIncrColumn := ch.session.GetAutoIncrementColumn(tableName)

			if autoIncrColumn != "" && !strings.Contains(strings.ToUpper(stmt.SQL), "RETURNING") {
				// Table has AUTO_INCREMENT, use RETURNING to get the inserted ID
				returningSQL := stmt.SQL + " RETURNING " + autoIncrColumn
				rows, err := ch.pgConn.Query(ctx, returningSQL, convertedArgs...)
				if err != nil {
					errorCode, errorMsg := ch.handler.errorMapper.MapError(err)
					return nil, mysql.NewError(errorCode, errorMsg)
				}
				defer rows.Close()
				// Get the returned ID
				if rows.Next() {
					var id int64
					if err := rows.Scan(&id); err == nil {
						lastInsertID = uint64(id)
					}
				}
				rowsAffected = 1 // INSERT with RETURNING always affects 1 row if successful
			} else {
				// Table doesn't have AUTO_INCREMENT or already has RETURNING, just execute
				cmdTag, err := ch.pgConn.Exec(ctx, stmt.SQL, convertedArgs...)
				if err != nil {
					errorCode, errorMsg := ch.handler.errorMapper.MapError(err)
					return nil, mysql.NewError(errorCode, errorMsg)
				}
				rowsAffected = cmdTag.RowsAffected()
			}
		} else {
			// Execute non-INSERT DML statements normally
			cmdTag, err := ch.pgConn.Exec(ctx, stmt.SQL, convertedArgs...)
			if err != nil {
				errorCode, errorMsg := ch.handler.errorMapper.MapError(err)
				return nil, mysql.NewError(errorCode, errorMsg)
			}
			rowsAffected = cmdTag.RowsAffected()
		}

		duration := time.Since(startTime).Seconds()
		ch.handler.metrics.ObserveQueryDuration(duration)
		ch.handler.logger.LogQuery(ch.session.ID, ch.session.User, ch.session.ClientAddr,
			stmt.OriginalSQL, duration, rowsAffected, nil)

		return &mysql.Result{
			Status:       0,
			InsertId:     lastInsertID,
			AffectedRows: uint64(rowsAffected),
		}, nil
	}

	// Use Query for SELECT statements
	rows, err := ch.pgConn.Query(ctx, stmt.SQL, convertedArgs...)
	if err != nil {
		errorCode, errorMsg := ch.handler.errorMapper.MapError(err)
		return nil, mysql.NewError(errorCode, errorMsg)
	}
	defer rows.Close()

	// CRITICAL: Use Binary Protocol for PreparedStatement results
	result, err := ch.buildMySQLResult(rows, true)
	if err != nil {
		return nil, err
	}

	duration := time.Since(startTime).Seconds()
	ch.handler.metrics.ObserveQueryDuration(duration)

	rowCount := int64(0)
	if result.Resultset != nil {
		// Use RowDatas length, not Values, because BuildSimpleResultset doesn't populate Values
		rowCount = int64(len(result.Resultset.RowDatas))
	}
	ch.handler.logger.LogQuery(ch.session.ID, ch.session.User, ch.session.ClientAddr,
		stmt.OriginalSQL, duration, rowCount, nil)

	return result, nil
}

func (ch *ConnectionHandler) HandleStmtClose(data interface{}) error {
	stmtID, ok := data.(uint32)
	if !ok {
		return mysql.NewError(mysql.ER_UNKNOWN_ERROR, "invalid statement ID type")
	}

	_, ok = ch.session.GetPreparedStatement(stmtID)
	if !ok {
		return nil
	}

	// Since we're not using named prepared statements,
	// just remove from session tracking
	// pgx will handle cleanup automatically
	ch.session.RemovePreparedStatement(stmtID)
	return nil
}

func (ch *ConnectionHandler) HandleOtherCommand(cmd byte, data []byte) error {
	switch cmd {
	case mysql.COM_PING:
		return nil
	case mysql.COM_INIT_DB:
		return ch.UseDB(string(data))
	case mysql.COM_QUIT:
		return ch.Close()
	case mysql.COM_BINLOG_DUMP:
		return ch.handleBinlogDump(data)
	case mysql.COM_BINLOG_DUMP_GTID:
		return ch.handleBinlogDumpGTID(data)
	case mysql.COM_REGISTER_SLAVE:
		return ch.handleRegisterSlave(data)
	default:
		return mysql.NewError(mysql.ER_UNKNOWN_COM_ERROR, fmt.Sprintf("command %d not supported", cmd))
	}
}

// handleBinlogDump handles COM_BINLOG_DUMP command from MySQL replication clients
// Protocol: https://dev.mysql.com/doc/internals/en/com-binlog-dump.html
// Format: [4 bytes pos][2 bytes flags][4 bytes server-id][string filename]
func (ch *ConnectionHandler) handleBinlogDump(data []byte) error {
	if ch.handler.replServer == nil {
		return mysql.NewError(mysql.ER_MASTER_FATAL_ERROR_READING_BINLOG, "binlog replication not enabled")
	}

	if len(data) < 10 {
		return mysql.NewError(mysql.ER_MALFORMED_PACKET, "invalid COM_BINLOG_DUMP packet")
	}

	// Parse COM_BINLOG_DUMP packet
	pos := binary.LittleEndian.Uint32(data[0:4])
	// flags := binary.LittleEndian.Uint16(data[4:6])  // Not used currently
	serverID := binary.LittleEndian.Uint32(data[6:10])
	filename := string(data[10:])

	ch.handler.logger.Info("COM_BINLOG_DUMP received",
		zap.Uint32("position", pos),
		zap.Uint32("server_id", serverID),
		zap.String("filename", filename))

	// Create mysql.Position for the replication server
	position := mysql.Position{
		Name: filename,
		Pos:  pos,
	}

	// Delegate to replication server
	// This is a blocking call that streams events to the client
	return ch.handler.replServer.HandleBinlogDump(ch.conn, serverID, position)
}

// handleBinlogDumpGTID handles COM_BINLOG_DUMP_GTID command
// Protocol: https://dev.mysql.com/doc/internals/en/com-binlog-dump-gtid.html
func (ch *ConnectionHandler) handleBinlogDumpGTID(data []byte) error {
	if ch.handler.replServer == nil {
		return mysql.NewError(mysql.ER_MASTER_FATAL_ERROR_READING_BINLOG, "binlog replication not enabled")
	}

	// For now, we don't support GTID-based replication
	// Just return an error asking client to use file-based replication
	ch.handler.logger.Warn("COM_BINLOG_DUMP_GTID not supported, use file-based replication")
	return mysql.NewError(mysql.ER_MASTER_FATAL_ERROR_READING_BINLOG,
		"GTID-based replication not supported, please use file-based replication")
}

// handleRegisterSlave handles COM_REGISTER_SLAVE command
// This is sent by MySQL slaves before they start replication
func (ch *ConnectionHandler) handleRegisterSlave(data []byte) error {
	if ch.handler.replServer == nil {
		return mysql.NewError(mysql.ER_MASTER_FATAL_ERROR_READING_BINLOG, "binlog replication not enabled")
	}

	// Parse server ID from the packet (first 4 bytes)
	if len(data) < 4 {
		return mysql.NewError(mysql.ER_MALFORMED_PACKET, "invalid COM_REGISTER_SLAVE packet")
	}

	serverID := binary.LittleEndian.Uint32(data[0:4])
	ch.handler.logger.Info("COM_REGISTER_SLAVE received", zap.Uint32("server_id", serverID))

	// Just acknowledge the registration
	// The actual replication will start when COM_BINLOG_DUMP is received
	return nil
}

func (ch *ConnectionHandler) Close() error {
	ch.handler.metrics.DecActiveConnections()
	ch.handler.sessionMgr.RemoveSession(ch.session.ID)

	if ch.pgConn != nil {
		ch.handler.pgPool.ReleaseForSession(ch.session.ID)
	}

	return nil
}

func (ch *ConnectionHandler) buildMySQLResult(rows pgx.Rows, binary bool) (*mysql.Result, error) {
	fieldDescs := rows.FieldDescriptions()

	// Build field names
	names := make([]string, len(fieldDescs))
	for i, fd := range fieldDescs {
		names[i] = string(fd.Name)
	}

	// Collect all rows with minimal conversion
	// BuildSimpleResultset expects native types (int, float64, string, []byte, nil)
	values := make([][]interface{}, 0)
	rowNum := 0
	for rows.Next() {
		rowValues, err := rows.Values()
		if err != nil {
			return nil, err
		}

		row := make([]interface{}, len(rowValues))
		for i, v := range rowValues {
			if v == nil {
				row[i] = nil
				continue
			}

			// Convert PostgreSQL types to Go types that BuildSimpleTextResultset understands
			switch val := v.(type) {
			case int8, int16, int32, int64, int:
				row[i] = val
			case uint8, uint16, uint32, uint64, uint:
				row[i] = val
			case float32:
				row[i] = val
			case float64:
				row[i] = val
			case string:
				// Check if this is a timestamp string in ISO 8601 or other timestamp formats
				// pgx with Simple Query Protocol may return timestamps as strings
				var t time.Time
				var parsed bool
				// Try RFC3339 (e.g., "2024-12-25T23:30:45Z")
				if parsedTime, err := time.Parse(time.RFC3339, val); err == nil {
					t = parsedTime
					parsed = true
				} else if parsedTime, err := time.Parse("2006-01-02T15:04:05", val); err == nil {
					// ISO 8601 without timezone
					t = parsedTime
					parsed = true
				}

				if parsed {
					// Convert to MySQL datetime format in local timezone
					row[i] = t.In(time.Local).Format("2006-01-02 15:04:05")
				} else {
					row[i] = val
				}
			case []byte:
				row[i] = val
			case time.Time:
				// Convert time.Time to MySQL datetime string format
				// FormatTextValue doesn't support time.Time, so we must convert to string
				// Convert to local timezone to match MySQL's NOW() behavior
				localTime := val.In(time.Local)
				// Format as "YYYY-MM-DD HH:MM:SS" for DATETIME/TIMESTAMP fields
				row[i] = localTime.Format("2006-01-02 15:04:05")
			case bool:
				// Convert bool to int for MySQL
				if val {
					row[i] = int64(1)
				} else {
					row[i] = int64(0)
				}
			case pgtype.Numeric:
				// CRITICAL FIX for "busy buffer" error:
				// BuildSimpleTextResultset's FormatTextValue() only accepts:
				// int8-64, uint8-64, float32/64, []byte, string, nil
				// pgtype.Numeric is NOT supported, so we MUST convert to string first
				if !val.Valid {
					row[i] = nil
				} else {
					// Convert using MarshalJSON which returns proper decimal string
					// e.g., {Int: 9999, Exp: -2} -> "99.99"
					if jsonBytes, err := val.MarshalJSON(); err == nil {
						// MarshalJSON returns string representation of the number
						row[i] = string(jsonBytes)
					} else {
						// Fallback: use Int.String()
						row[i] = val.Int.String()
					}
				}
			case pgtype.Time:
				// Convert pgtype.Time to MySQL TIME format "HH:MM:SS"
				// BuildSimpleTextResultset will format as string
				// Field metadata will indicate MYSQL_TYPE_TIME so client can parse properly
				if !val.Valid {
					row[i] = nil
				} else {
					// Microseconds to HH:MM:SS
					totalSeconds := val.Microseconds / 1000000
					hours := totalSeconds / 3600
					minutes := (totalSeconds % 3600) / 60
					seconds := totalSeconds % 60
					row[i] = fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
				}
			default:
				// For any other types, convert to string
				// This ensures BuildSimpleTextResultset won't encounter unsupported types
				// Check if it's a time.Time that wasn't caught above (e.g., from default case)
				if t, ok := val.(time.Time); ok {
					// Format as MySQL datetime string in local timezone
					row[i] = t.In(time.Local).Format("2006-01-02 15:04:05")
				} else {
					row[i] = fmt.Sprintf("%v", val)
				}
			}
		}

		values = append(values, row)
		rowNum++
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Use BuildSimpleResultset with binary parameter
	// binary=true: Binary Protocol (for PreparedStatements)
	// binary=false: Text Protocol (for regular queries)
	resultset, err := mysql.BuildSimpleResultset(names, values, binary)
	if err != nil {
		return nil, err
	}


	// Fix: BuildSimpleResultset doesn't populate FieldNames map or set correct types for DECIMAL
	// Manually fill these in using PostgreSQL FieldDescriptions

	// Initialize FieldNames map if it's nil
	if resultset.FieldNames == nil {
		resultset.FieldNames = make(map[string]int, len(fieldDescs))
	}

	// Map PostgreSQL OIDs to MySQL types
	// Key OIDs:
	// 1700 = NUMERIC/DECIMAL
	// 1114 = TIMESTAMP, 1184 = TIMESTAMPTZ
	// 1082 = DATE
	// 1083 = TIME, 1266 = TIMETZ
	for i, fd := range fieldDescs {
		// Populate FieldNames map
		resultset.FieldNames[string(fd.Name)] = i

		// Override field types based on PostgreSQL OID
		switch fd.DataTypeOID {
		case 1700: // NUMERIC/DECIMAL
			// BuildSimpleTextResultset inferred this as MYSQL_TYPE_VAR_STRING (from string value)
			// But MySQL clients expect MYSQL_TYPE_NEWDECIMAL for decimal columns
			resultset.Fields[i].Type = mysql.MYSQL_TYPE_NEWDECIMAL
			resultset.Fields[i].Charset = 63  // binary charset for numeric types
			resultset.Fields[i].Flag = mysql.BINARY_FLAG | mysql.NOT_NULL_FLAG

			// Parse TypeModifier to extract precision and scale
			// TypeModifier format: ((precision << 16) | scale) + 4
			if fd.TypeModifier > 0 {
				typemod := fd.TypeModifier - 4
				precision := typemod >> 16
				scale := typemod & 0xFFFF

				// MySQL ColumnLength = precision + 1 (for decimal point) if scale > 0
				// or just precision if scale = 0
				if scale > 0 {
					resultset.Fields[i].ColumnLength = uint32(precision + 1) // +1 for decimal point
				} else {
					resultset.Fields[i].ColumnLength = uint32(precision)
				}
				resultset.Fields[i].Decimal = uint8(scale)
			} else {
				// Default DECIMAL precision
				resultset.Fields[i].ColumnLength = 66 // 65 + 1 for decimal point
				resultset.Fields[i].Decimal = 0
			}

		case 1114, 1184: // TIMESTAMP, TIMESTAMPTZ
			// CRITICAL FIX: Must set correct MySQL field type for date/time parsing
			// MySQL driver's readRow() only parses datetime strings when fieldType matches:
			// fieldTypeTimestamp(7), fieldTypeDateTime(12), fieldTypeDate(10), fieldTypeNewDate(14)
			// If we keep VARCHAR(253), driver returns []byte instead of time.Time
			// Text protocol can send datetime as strings with DATETIME field type
			resultset.Fields[i].Type = mysql.MYSQL_TYPE_DATETIME
			// Keep Charset = 33 (UTF-8) as set by BuildSimpleResultset for time.Time
			// DO NOT override to 63 (binary) - that prevents MySQL client from parsing datetime strings
			resultset.Fields[i].ColumnLength = 19 // "YYYY-MM-DD HH:MM:SS"

		case 1082: // DATE
			// CRITICAL FIX: Must set MYSQL_TYPE_DATE for proper parsing
			// Text protocol can send date as strings with DATE field type
			resultset.Fields[i].Type = mysql.MYSQL_TYPE_DATE
			// Keep Charset = 33 (UTF-8) as set by BuildSimpleResultset for time.Time
			// DO NOT override to 63 (binary) - that prevents MySQL client from parsing date strings
			resultset.Fields[i].ColumnLength = 10 // "YYYY-MM-DD"

		case 1083, 1266: // TIME, TIMETZ
			// CRITICAL FIX: Must set MYSQL_TYPE_TIME for proper TIME parsing
			// Text protocol can send time as strings with TIME field type
			resultset.Fields[i].Type = mysql.MYSQL_TYPE_TIME
			// Keep Charset = 33 (UTF-8) as set by BuildSimpleResultset for string values
			// DO NOT override to 63 (binary) - that prevents MySQL client from parsing time strings
			resultset.Fields[i].ColumnLength = 8 // "HH:MM:SS"
		}
	}


	result := &mysql.Result{
		Status:    0,
		Resultset: resultset,
	}

	return result, nil
}

func (ch *ConnectionHandler) handleShowCommand(ctx context.Context, query string) (*mysql.Result, error) {
	rows, err := ch.handler.showEmulator.HandleShowCommand(ctx, ch.pgConn, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Use Text Protocol for SHOW commands
	return ch.buildMySQLResult(rows, false)
}

func (ch *ConnectionHandler) handleSetCommand(ctx context.Context, query string) (*mysql.Result, error) {
	sessionVars := make(map[string]interface{})

	err := ch.handler.showEmulator.HandleSetCommand(ctx, query, sessionVars)
	if err != nil {
		return nil, err
	}

	// Handle AUTOCOMMIT specially to manage transaction state
	for k, v := range sessionVars {
		if strings.ToLower(k) == "autocommit" {
			autocommit := false
			switch val := v.(type) {
			case string:
				autocommit = (strings.ToUpper(val) == "ON" || val == "1" || strings.ToUpper(val) == "TRUE")
			case int:
				autocommit = (val != 0)
			case bool:
				autocommit = val
			}

			if err := ch.session.SetAutocommit(autocommit); err != nil {
				ch.handler.metrics.IncErrors("transaction")
				ch.handler.logger.LogError(ch.session.ID, ch.session.User, ch.session.ClientAddr, "set_autocommit", err)
				return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR, err.Error())
			}
		}
		ch.session.SetSessionVar(k, v)
	}

	result := &mysql.Result{
		Status:       0,
		AffectedRows: 0,
	}

	return result, nil
}

func (ch *ConnectionHandler) handleUseCommand(ctx context.Context, query string) (*mysql.Result, error) {
	err := ch.handler.showEmulator.HandleUseCommand(ctx, ch.pgConn, query)
	if err != nil {
		return nil, err
	}

	result := &mysql.Result{
		Status:       0,
		AffectedRows: 0,
	}

	return result, nil
}

// isKillStatement checks if the query is a MySQL KILL command
// MySQL clients send KILL <id> to terminate connections, but PostgreSQL doesn't support this.
// We intercept and return OK to avoid errors on graceful client disconnect.
func isKillStatement(query string) bool {
	upper := strings.ToUpper(strings.TrimSpace(query))
	// Match KILL <id>, KILL CONNECTION <id>, KILL QUERY <id>
	return strings.HasPrefix(upper, "KILL ")
}

// extractInsertTableName extracts the table name from an INSERT statement
func extractInsertTableName(sql string) string {
	upper := strings.ToUpper(sql)
	idx := strings.Index(upper, "INSERT INTO")
	if idx == -1 {
		return ""
	}

	// Skip "INSERT INTO "
	start := idx + 12
	sql = strings.TrimSpace(sql[start:])

	// Remove quotes if present
	sql = strings.Trim(sql, "`\"")

	// Get the first word (table name)
	parts := strings.Fields(sql)
	if len(parts) == 0 {
		return ""
	}

	return strings.Trim(parts[0], "`\"")
}

// extractCreateTableName extracts the table name from a CREATE TABLE statement
func extractCreateTableName(sql string) string {
	upper := strings.ToUpper(sql)
	idx := strings.Index(upper, "CREATE TABLE")
	if idx == -1 {
		return ""
	}

	// Skip "CREATE TABLE "
	start := idx + 12
	sql = strings.TrimSpace(sql[start:])

	// Handle "IF NOT EXISTS" clause
	if strings.HasPrefix(strings.ToUpper(sql), "IF NOT EXISTS") {
		sql = strings.TrimSpace(sql[13:])
	}

	// Remove quotes if present
	sql = strings.Trim(sql, "`\"")

	// Get the first word (table name)
	parts := strings.Fields(sql)
	if len(parts) == 0 {
		return ""
	}

	return strings.Trim(parts[0], "`\"")
}

// extractAutoIncrementColumn extracts the column name that has AUTO_INCREMENT from a CREATE TABLE statement
func extractAutoIncrementColumn(sql string) string {
	upper := strings.ToUpper(sql)

	// Check if there's an AUTO_INCREMENT keyword
	if !strings.Contains(upper, "AUTO_INCREMENT") {
		return ""
	}

	// Find the opening parenthesis for column definitions
	openParen := strings.Index(sql, "(")
	if openParen == -1 {
		return ""
	}

	// Get the column definitions part
	remaining := sql[openParen+1:]

	// Split by comma or newline to get individual column definitions
	// Handle both single-line and multi-line CREATE TABLE statements
	columnDefs := strings.FieldsFunc(remaining, func(r rune) bool {
		return r == ',' || r == '\n'
	})

	for _, colDef := range columnDefs {
		colDef = strings.TrimSpace(colDef)
		upperColDef := strings.ToUpper(colDef)

		if strings.Contains(upperColDef, "AUTO_INCREMENT") {
			// Extract column name from the beginning
			colDef = strings.TrimLeft(colDef, " \t`\"")

			// Get the first word (column name)
			parts := strings.Fields(colDef)
			if len(parts) > 0 {
				colName := strings.Trim(parts[0], "`\",")
				return colName
			}
		}
	}

	return ""
}

// extractAlterTableName extracts the table name from an ALTER TABLE statement
func extractAlterTableName(sql string) string {
	upper := strings.ToUpper(sql)
	idx := strings.Index(upper, "ALTER TABLE")
	if idx == -1 {
		return ""
	}

	// Skip "ALTER TABLE "
	start := idx + 11
	sql = strings.TrimSpace(sql[start:])

	// Remove quotes if present
	sql = strings.Trim(sql, "`\"")

	// Get the first word (table name)
	parts := strings.Fields(sql)
	if len(parts) == 0 {
		return ""
	}

	return strings.Trim(parts[0], "`\"")
}

// extractDropTableName extracts the table name from a DROP TABLE statement
func extractDropTableName(sql string) string {
	upper := strings.ToUpper(sql)
	idx := strings.Index(upper, "DROP TABLE")
	if idx == -1 {
		return ""
	}

	// Skip "DROP TABLE "
	start := idx + 10
	sql = strings.TrimSpace(sql[start:])

	// Handle "IF EXISTS" clause
	if strings.HasPrefix(strings.ToUpper(sql), "IF EXISTS") {
		sql = strings.TrimSpace(sql[9:])
	}

	// Remove quotes if present
	sql = strings.Trim(sql, "`\"")

	// Get the first word (table name)
	parts := strings.Fields(sql)
	if len(parts) == 0 {
		return ""
	}

	return strings.Trim(parts[0], "`\"")
}
