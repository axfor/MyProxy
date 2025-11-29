// Package replication provides PostgreSQL logical replication streaming.
package replication

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"aproxy/pkg/observability"
	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"go.uber.org/zap"
)

// Checkpoint represents the saved replication state for recovery
type Checkpoint struct {
	LSN       uint64    `json:"lsn"`        // PostgreSQL LSN position
	Timestamp time.Time `json:"timestamp"`  // When checkpoint was saved
	SlotName  string    `json:"slot_name"`  // Replication slot name
}

// PGStreamer streams change events from PostgreSQL logical replication
type PGStreamer struct {
	config    *ServerConfig
	logger    *zap.Logger
	metrics   *observability.Metrics
	eventChan chan<- *ChangeEvent

	conn      *pgconn.PgConn
	adminConn *pgconn.PgConn // Separate connection for DDL operations
	mu        sync.Mutex

	ctx       context.Context
	cancel    context.CancelFunc
	running   atomic.Bool
	wg        sync.WaitGroup

	// Replication state
	clientXLogPos pglogrepl.LSN
	standbyMessageTimeout time.Duration
	nextStandbyMessageDeadline time.Time

	// Table information cache
	tableCache map[uint32]*TableInfo
	tableMu    sync.RWMutex

	// Track tables with REPLICA IDENTITY FULL already set
	replicaIdentitySet map[string]bool
	replicaIdentityMu  sync.RWMutex
}

// TableInfo contains cached table information
type TableInfo struct {
	ID       uint32
	Schema   string
	Name     string
	Columns  []ColumnInfo
}

// ColumnInfo contains column information
type ColumnInfo struct {
	Name     string
	DataType uint32
	TypeName string
	Nullable bool
}

// NewPGStreamer creates a new PostgreSQL logical replication streamer
func NewPGStreamer(config *ServerConfig, logger *zap.Logger, metrics *observability.Metrics, eventChan chan<- *ChangeEvent) (*PGStreamer, error) {
	ctx, cancel := context.WithCancel(context.Background())

	s := &PGStreamer{
		config:                 config,
		logger:                 logger,
		metrics:                metrics,
		eventChan:              eventChan,
		ctx:                    ctx,
		cancel:                 cancel,
		standbyMessageTimeout:  10 * time.Second,
		tableCache:             make(map[uint32]*TableInfo),
		replicaIdentitySet:     make(map[string]bool),
	}

	// Load checkpoint if exists
	if err := s.loadCheckpoint(); err != nil {
		logger.Warn("Failed to load checkpoint, starting from beginning", zap.Error(err))
	}

	// Start the streamer
	if err := s.Start(); err != nil {
		cancel()
		return nil, err
	}

	return s, nil
}

// loadCheckpoint loads the last saved checkpoint from disk
func (s *PGStreamer) loadCheckpoint() error {
	if s.config.CheckpointFile == "" {
		return nil
	}

	data, err := os.ReadFile(s.config.CheckpointFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No checkpoint file, start from beginning
		}
		return fmt.Errorf("failed to read checkpoint file: %w", err)
	}

	var checkpoint Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return fmt.Errorf("failed to parse checkpoint: %w", err)
	}

	// Verify slot name matches
	if checkpoint.SlotName != "" && checkpoint.SlotName != s.config.PGSlotName {
		s.logger.Warn("Checkpoint slot name mismatch, ignoring checkpoint",
			zap.String("checkpoint_slot", checkpoint.SlotName),
			zap.String("config_slot", s.config.PGSlotName))
		return nil
	}

	s.clientXLogPos = pglogrepl.LSN(checkpoint.LSN)
	s.logger.Info("Loaded checkpoint",
		zap.Uint64("lsn", checkpoint.LSN),
		zap.Time("saved_at", checkpoint.Timestamp))

	return nil
}

// saveCheckpoint saves the current LSN position to disk
func (s *PGStreamer) saveCheckpoint() error {
	if s.config.CheckpointFile == "" {
		return nil
	}

	checkpoint := Checkpoint{
		LSN:       uint64(s.clientXLogPos),
		Timestamp: time.Now(),
		SlotName:  s.config.PGSlotName,
	}

	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(s.config.CheckpointFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create checkpoint directory: %w", err)
	}

	// Write atomically using temp file
	tempFile := s.config.CheckpointFile + ".tmp"
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write checkpoint: %w", err)
	}

	if err := os.Rename(tempFile, s.config.CheckpointFile); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("failed to rename checkpoint file: %w", err)
	}

	s.logger.Debug("Saved checkpoint", zap.Uint64("lsn", checkpoint.LSN))
	return nil
}

// checkpointLoop periodically saves checkpoints
func (s *PGStreamer) checkpointLoop() {
	defer s.wg.Done()

	interval := s.config.CheckpointInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			// Save final checkpoint before exit
			if err := s.saveCheckpoint(); err != nil {
				s.logger.Error("Failed to save final checkpoint", zap.Error(err))
			}
			return
		case <-ticker.C:
			if err := s.saveCheckpoint(); err != nil {
				s.logger.Error("Failed to save checkpoint", zap.Error(err))
			}
		}
	}
}

// Start starts the PostgreSQL logical replication streamer
func (s *PGStreamer) Start() error {
	if !s.running.CompareAndSwap(false, true) {
		return fmt.Errorf("streamer already running")
	}

	// Connect to PostgreSQL with replication protocol
	if err := s.connect(); err != nil {
		s.running.Store(false)
		return fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	// Start replication
	s.wg.Add(1)
	go s.streamLoop()

	// Start checkpoint saver
	if s.config.CheckpointFile != "" {
		s.wg.Add(1)
		go s.checkpointLoop()
	}

	s.logger.Info("PostgreSQL logical replication streamer started",
		zap.String("host", s.config.PGHost),
		zap.Int("port", s.config.PGPort),
		zap.String("database", s.config.PGDatabase),
		zap.String("slot", s.config.PGSlotName),
		zap.Uint64("start_lsn", uint64(s.clientXLogPos)),
	)

	return nil
}

// Stop stops the streamer
func (s *PGStreamer) Stop() error {
	if !s.running.Load() {
		return nil
	}

	s.cancel()
	s.wg.Wait()

	s.mu.Lock()
	if s.conn != nil {
		s.conn.Close(context.Background())
		s.conn = nil
	}
	if s.adminConn != nil {
		s.adminConn.Close(context.Background())
		s.adminConn = nil
	}
	s.mu.Unlock()

	s.running.Store(false)
	s.logger.Info("PostgreSQL logical replication streamer stopped")
	return nil
}

// connect establishes a connection to PostgreSQL with replication protocol
func (s *PGStreamer) connect() error {
	// Build connection string without replication parameter
	// The replication parameter needs to be added to RuntimeParams manually
	// because pgconn.ParseConfig doesn't parse it from the connection string
	connString := fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s password=%s",
		s.config.PGHost,
		s.config.PGPort,
		s.config.PGDatabase,
		s.config.PGUser,
		s.config.PGPassword,
	)

	config, err := pgconn.ParseConfig(connString)
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Add replication=database to RuntimeParams for logical replication
	// This is sent in the StartupMessage to PostgreSQL
	if config.RuntimeParams == nil {
		config.RuntimeParams = make(map[string]string)
	}
	config.RuntimeParams["replication"] = "database"

	conn, err := pgconn.ConnectConfig(s.ctx, config)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	// Create replication slot if not exists
	if err := s.ensureReplicationSlot(); err != nil {
		conn.Close(s.ctx)
		return fmt.Errorf("failed to create replication slot: %w", err)
	}

	// Create publication if not exists
	if err := s.ensurePublication(); err != nil {
		conn.Close(s.ctx)
		return fmt.Errorf("failed to create publication: %w", err)
	}

	return nil
}

// ensureReplicationSlot creates the replication slot if it doesn't exist
func (s *PGStreamer) ensureReplicationSlot() error {
	// Try to create the slot - it will fail if it already exists
	_, err := pglogrepl.CreateReplicationSlot(
		s.ctx,
		s.conn,
		s.config.PGSlotName,
		"pgoutput",
		pglogrepl.CreateReplicationSlotOptions{
			Temporary: false,
		},
	)
	if err != nil {
		// Check if slot already exists
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "42710" {
			// Slot already exists, that's fine
			s.logger.Debug("Replication slot already exists", zap.String("slot", s.config.PGSlotName))
			return nil
		}
		return err
	}

	s.logger.Info("Created replication slot", zap.String("slot", s.config.PGSlotName))
	return nil
}

// ensurePublication creates the publication if it doesn't exist
func (s *PGStreamer) ensurePublication() error {
	// Create admin connection for DDL operations (kept alive for setting REPLICA IDENTITY)
	connString := fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s password=%s",
		s.config.PGHost,
		s.config.PGPort,
		s.config.PGDatabase,
		s.config.PGUser,
		s.config.PGPassword,
	)

	adminConn, err := pgconn.Connect(s.ctx, connString)
	if err != nil {
		return fmt.Errorf("failed to connect for publication: %w", err)
	}
	s.adminConn = adminConn

	// Create publication for all tables
	createSQL := fmt.Sprintf("CREATE PUBLICATION %s FOR ALL TABLES", s.config.PGPublicationName)
	result := adminConn.Exec(s.ctx, createSQL)
	_, err = result.ReadAll()
	if err != nil {
		// Check if publication already exists
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "42710" {
			s.logger.Debug("Publication already exists", zap.String("publication", s.config.PGPublicationName))
		} else {
			return fmt.Errorf("failed to create publication: %w", err)
		}
	} else {
		s.logger.Info("Created publication", zap.String("publication", s.config.PGPublicationName))
	}

	// Set REPLICA IDENTITY FULL for all existing tables
	if err := s.setReplicaIdentityForAllTables(); err != nil {
		s.logger.Warn("Failed to set REPLICA IDENTITY FULL for existing tables", zap.Error(err))
		// Not a fatal error - tables will be handled individually as they're encountered
	}

	return nil
}

// setReplicaIdentityForAllTables sets REPLICA IDENTITY FULL for all user tables
func (s *PGStreamer) setReplicaIdentityForAllTables() error {
	if s.adminConn == nil {
		return fmt.Errorf("admin connection not available")
	}

	// Query all user tables (excluding system schemas)
	query := `
		SELECT schemaname, tablename
		FROM pg_tables
		WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
	`
	result := s.adminConn.Exec(s.ctx, query)
	results, err := result.ReadAll()
	if err != nil {
		return fmt.Errorf("failed to query tables: %w", err)
	}

	for _, r := range results {
		for _, row := range r.Rows {
			if len(row) >= 2 {
				schema := string(row[0])
				table := string(row[1])
				fullName := fmt.Sprintf("%s.%s", schema, table)

				// Set REPLICA IDENTITY FULL
				alterSQL := fmt.Sprintf("ALTER TABLE %q.%q REPLICA IDENTITY FULL", schema, table)
				alterResult := s.adminConn.Exec(s.ctx, alterSQL)
				_, err := alterResult.ReadAll()
				if err != nil {
					s.logger.Warn("Failed to set REPLICA IDENTITY FULL",
						zap.String("table", fullName),
						zap.Error(err))
				} else {
					s.replicaIdentityMu.Lock()
					s.replicaIdentitySet[fullName] = true
					s.replicaIdentityMu.Unlock()
					s.logger.Debug("Set REPLICA IDENTITY FULL", zap.String("table", fullName))
				}
			}
		}
	}

	return nil
}

// ensureReplicaIdentityFull ensures a table has REPLICA IDENTITY FULL
func (s *PGStreamer) ensureReplicaIdentityFull(schema, table string) {
	fullName := fmt.Sprintf("%s.%s", schema, table)

	// Check if already set
	s.replicaIdentityMu.RLock()
	alreadySet := s.replicaIdentitySet[fullName]
	s.replicaIdentityMu.RUnlock()

	if alreadySet {
		return
	}

	// Set REPLICA IDENTITY FULL
	if s.adminConn == nil {
		s.logger.Warn("Admin connection not available, cannot set REPLICA IDENTITY FULL",
			zap.String("table", fullName))
		return
	}

	alterSQL := fmt.Sprintf("ALTER TABLE %q.%q REPLICA IDENTITY FULL", schema, table)
	result := s.adminConn.Exec(s.ctx, alterSQL)
	_, err := result.ReadAll()
	if err != nil {
		s.logger.Warn("Failed to set REPLICA IDENTITY FULL",
			zap.String("table", fullName),
			zap.Error(err))
	} else {
		s.replicaIdentityMu.Lock()
		s.replicaIdentitySet[fullName] = true
		s.replicaIdentityMu.Unlock()
		s.logger.Info("Set REPLICA IDENTITY FULL for table", zap.String("table", fullName))
	}
}

// streamLoop is the main loop for streaming replication events with auto-reconnect
func (s *PGStreamer) streamLoop() {
	defer s.wg.Done()

	retryCount := 0
	waitTime := s.config.ReconnectInitialWait
	if waitTime <= 0 {
		waitTime = 1 * time.Second
	}

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		// Run the actual streaming
		err := s.doStream()
		if err == nil {
			return // Normal shutdown
		}

		// Check if context was cancelled
		if s.ctx.Err() != nil {
			return
		}

		// Handle reconnection
		if !s.config.ReconnectEnabled {
			s.logger.Error("Streaming failed, reconnect disabled", zap.Error(err))
			return
		}

		retryCount++
		if s.config.ReconnectMaxRetries > 0 && retryCount > s.config.ReconnectMaxRetries {
			s.logger.Error("Max reconnect attempts reached",
				zap.Int("attempts", retryCount),
				zap.Error(err))
			return
		}

		// Track reconnection attempts
		if s.metrics != nil {
			s.metrics.IncCDCReconnects()
		}

		s.logger.Warn("Streaming connection lost, attempting reconnect",
			zap.Error(err),
			zap.Int("attempt", retryCount),
			zap.Duration("wait", waitTime))

		// Wait before reconnecting
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(waitTime):
		}

		// Exponential backoff
		waitTime = waitTime * 2
		maxWait := s.config.ReconnectMaxWait
		if maxWait <= 0 {
			maxWait = 30 * time.Second
		}
		if waitTime > maxWait {
			waitTime = maxWait
		}

		// Attempt reconnection
		if err := s.reconnect(); err != nil {
			s.logger.Error("Reconnection failed", zap.Error(err))
			continue
		}

		// Reset retry count on successful reconnection
		s.logger.Info("Reconnected successfully",
			zap.Int("attempts", retryCount),
			zap.Uint64("resume_lsn", uint64(s.clientXLogPos)))
		retryCount = 0
		waitTime = s.config.ReconnectInitialWait
		if waitTime <= 0 {
			waitTime = 1 * time.Second
		}
	}
}

// reconnect closes existing connections and establishes new ones
func (s *PGStreamer) reconnect() error {
	s.mu.Lock()
	// Close existing connections
	if s.conn != nil {
		s.conn.Close(context.Background())
		s.conn = nil
	}
	if s.adminConn != nil {
		s.adminConn.Close(context.Background())
		s.adminConn = nil
	}
	s.mu.Unlock()

	// Re-establish connection
	return s.connect()
}

// doStream performs the actual streaming work
func (s *PGStreamer) doStream() error {
	// Start replication
	err := pglogrepl.StartReplication(
		s.ctx,
		s.conn,
		s.config.PGSlotName,
		s.clientXLogPos,
		pglogrepl.StartReplicationOptions{
			PluginArgs: []string{
				"proto_version '1'",
				fmt.Sprintf("publication_names '%s'", s.config.PGPublicationName),
			},
		},
	)
	if err != nil {
		return fmt.Errorf("failed to start replication: %w", err)
	}

	s.nextStandbyMessageDeadline = time.Now().Add(s.standbyMessageTimeout)

	for {
		select {
		case <-s.ctx.Done():
			return nil // Normal shutdown
		default:
		}

		// Send standby status update if needed
		if time.Now().After(s.nextStandbyMessageDeadline) {
			err = pglogrepl.SendStandbyStatusUpdate(
				s.ctx,
				s.conn,
				pglogrepl.StandbyStatusUpdate{
					WALWritePosition: s.clientXLogPos,
				},
			)
			if err != nil {
				return fmt.Errorf("failed to send standby status: %w", err)
			}
			s.nextStandbyMessageDeadline = time.Now().Add(s.standbyMessageTimeout)
		}

		// Receive message with timeout
		ctx, cancel := context.WithDeadline(s.ctx, s.nextStandbyMessageDeadline)
		rawMsg, err := s.conn.ReceiveMessage(ctx)
		cancel()

		if err != nil {
			if pgconn.Timeout(err) {
				continue
			}
			if s.ctx.Err() != nil {
				return nil // Normal shutdown
			}
			return fmt.Errorf("failed to receive message: %w", err)
		}

		// Handle the message
		if errMsg, ok := rawMsg.(*pgproto3.ErrorResponse); ok {
			s.logger.Error("Received error from PostgreSQL",
				zap.String("severity", errMsg.Severity),
				zap.String("message", errMsg.Message),
			)
			// Continue processing, don't return on PostgreSQL errors
			continue
		}

		copyData, ok := rawMsg.(*pgproto3.CopyData)
		if !ok {
			continue
		}

		switch copyData.Data[0] {
		case pglogrepl.PrimaryKeepaliveMessageByteID:
			s.handleKeepalive(copyData.Data[1:])

		case pglogrepl.XLogDataByteID:
			s.handleXLogData(copyData.Data[1:])
		}
	}
}

// handleKeepalive handles primary keepalive messages
func (s *PGStreamer) handleKeepalive(data []byte) {
	pkm, err := pglogrepl.ParsePrimaryKeepaliveMessage(data)
	if err != nil {
		s.logger.Error("Failed to parse keepalive", zap.Error(err))
		return
	}

	if pkm.ReplyRequested {
		s.nextStandbyMessageDeadline = time.Time{}
	}
}

// handleXLogData handles XLog data messages
func (s *PGStreamer) handleXLogData(data []byte) {
	xld, err := pglogrepl.ParseXLogData(data)
	if err != nil {
		s.logger.Error("Failed to parse XLog data", zap.Error(err))
		return
	}

	// Parse logical replication message
	s.parseLogicalReplicationMessage(xld.WALData)

	// Update position
	s.clientXLogPos = xld.WALStart + pglogrepl.LSN(len(xld.WALData))
}

// parseLogicalReplicationMessage parses a logical replication message
func (s *PGStreamer) parseLogicalReplicationMessage(data []byte) {
	if len(data) == 0 {
		return
	}

	msgType := data[0]
	switch msgType {
	case 'R': // Relation
		s.handleRelationMessage(data)
	case 'B': // Begin
		s.handleBeginMessage(data)
	case 'C': // Commit
		s.handleCommitMessage(data)
	case 'I': // Insert
		s.handleInsertMessage(data)
	case 'U': // Update
		s.handleUpdateMessage(data)
	case 'D': // Delete
		s.handleDeleteMessage(data)
	case 'T': // Truncate
		s.handleTruncateMessage(data)
	case 'O': // Origin
		// Handle origin if needed
	case 'Y': // Type
		// Handle type if needed
	}
}

// handleRelationMessage handles relation (table) messages
func (s *PGStreamer) handleRelationMessage(data []byte) {
	rel, err := pglogrepl.Parse(data)
	if err != nil {
		s.logger.Error("Failed to parse relation message", zap.Error(err))
		return
	}

	relMsg, ok := rel.(*pglogrepl.RelationMessage)
	if !ok {
		return
	}

	// Cache table information
	tableInfo := &TableInfo{
		ID:      relMsg.RelationID,
		Schema:  relMsg.Namespace,
		Name:    relMsg.RelationName,
		Columns: make([]ColumnInfo, len(relMsg.Columns)),
	}

	for i, col := range relMsg.Columns {
		tableInfo.Columns[i] = ColumnInfo{
			Name:     col.Name,
			DataType: col.DataType,
		}
	}

	s.tableMu.Lock()
	s.tableCache[relMsg.RelationID] = tableInfo
	s.tableMu.Unlock()

	// Ensure REPLICA IDENTITY FULL is set for this table (async to not block replication)
	go s.ensureReplicaIdentityFull(relMsg.Namespace, relMsg.RelationName)

	s.logger.Debug("Cached table info",
		zap.String("schema", tableInfo.Schema),
		zap.String("table", tableInfo.Name),
	)
}

// handleBeginMessage handles transaction begin messages
func (s *PGStreamer) handleBeginMessage(data []byte) {
	event := &ChangeEvent{
		Type:      ChangeTypeBegin,
		Timestamp: time.Now(),
	}
	s.sendEvent(event)
}

// handleCommitMessage handles transaction commit messages
func (s *PGStreamer) handleCommitMessage(data []byte) {
	event := &ChangeEvent{
		Type:      ChangeTypeCommit,
		Timestamp: time.Now(),
	}
	s.sendEvent(event)
}

// handleInsertMessage handles insert messages
func (s *PGStreamer) handleInsertMessage(data []byte) {
	msg, err := pglogrepl.Parse(data)
	if err != nil {
		s.logger.Error("Failed to parse insert message", zap.Error(err))
		return
	}

	insertMsg, ok := msg.(*pglogrepl.InsertMessage)
	if !ok {
		return
	}

	tableInfo := s.getTableInfo(insertMsg.RelationID)
	if tableInfo == nil {
		s.logger.Warn("Unknown table in insert", zap.Uint32("relation_id", insertMsg.RelationID))
		return
	}

	event := &ChangeEvent{
		Type:      ChangeTypeInsert,
		Timestamp: time.Now(),
		Schema:    tableInfo.Schema,
		Table:     tableInfo.Name,
		TableID:   uint64(insertMsg.RelationID),
		Columns:   s.convertColumns(tableInfo),
		NewValues: s.decodeTupleData(insertMsg.Tuple, tableInfo, nil),
	}
	s.sendEvent(event)
}

// handleUpdateMessage handles update messages
func (s *PGStreamer) handleUpdateMessage(data []byte) {
	msg, err := pglogrepl.Parse(data)
	if err != nil {
		s.logger.Error("Failed to parse update message", zap.Error(err))
		return
	}

	updateMsg, ok := msg.(*pglogrepl.UpdateMessage)
	if !ok {
		return
	}

	tableInfo := s.getTableInfo(updateMsg.RelationID)
	if tableInfo == nil {
		s.logger.Warn("Unknown table in update", zap.Uint32("relation_id", updateMsg.RelationID))
		return
	}

	// Decode old tuple first (if available with REPLICA IDENTITY FULL)
	var oldValues []interface{}
	if updateMsg.OldTuple != nil {
		oldValues = s.decodeTupleData(updateMsg.OldTuple, tableInfo, nil)
	}

	// Decode new tuple, using old values as fallback for unchanged TOAST columns
	newValues := s.decodeTupleData(updateMsg.NewTuple, tableInfo, oldValues)

	event := &ChangeEvent{
		Type:      ChangeTypeUpdate,
		Timestamp: time.Now(),
		Schema:    tableInfo.Schema,
		Table:     tableInfo.Name,
		TableID:   uint64(updateMsg.RelationID),
		Columns:   s.convertColumns(tableInfo),
		OldValues: oldValues,
		NewValues: newValues,
	}

	s.sendEvent(event)
}

// handleDeleteMessage handles delete messages
func (s *PGStreamer) handleDeleteMessage(data []byte) {
	msg, err := pglogrepl.Parse(data)
	if err != nil {
		s.logger.Error("Failed to parse delete message", zap.Error(err))
		return
	}

	deleteMsg, ok := msg.(*pglogrepl.DeleteMessage)
	if !ok {
		return
	}

	tableInfo := s.getTableInfo(deleteMsg.RelationID)
	if tableInfo == nil {
		s.logger.Warn("Unknown table in delete", zap.Uint32("relation_id", deleteMsg.RelationID))
		return
	}

	event := &ChangeEvent{
		Type:      ChangeTypeDelete,
		Timestamp: time.Now(),
		Schema:    tableInfo.Schema,
		Table:     tableInfo.Name,
		TableID:   uint64(deleteMsg.RelationID),
		Columns:   s.convertColumns(tableInfo),
		OldValues: s.decodeTupleData(deleteMsg.OldTuple, tableInfo, nil),
	}
	s.sendEvent(event)
}

// handleTruncateMessage handles truncate messages
func (s *PGStreamer) handleTruncateMessage(data []byte) {
	msg, err := pglogrepl.Parse(data)
	if err != nil {
		s.logger.Error("Failed to parse truncate message", zap.Error(err))
		return
	}

	truncateMsg, ok := msg.(*pglogrepl.TruncateMessage)
	if !ok {
		return
	}

	// TruncateMessage contains multiple relation IDs
	// Send a truncate event for each affected table
	for _, relID := range truncateMsg.RelationIDs {
		tableInfo := s.getTableInfo(relID)
		if tableInfo == nil {
			s.logger.Warn("Unknown table in truncate", zap.Uint32("relation_id", relID))
			continue
		}

		event := &ChangeEvent{
			Type:      ChangeTypeTruncate,
			Timestamp: time.Now(),
			Schema:    tableInfo.Schema,
			Table:     tableInfo.Name,
			TableID:   uint64(relID),
		}

		s.logger.Debug("Sending truncate event",
			zap.String("schema", tableInfo.Schema),
			zap.String("table", tableInfo.Name),
			zap.Uint8("option", truncateMsg.Option),
		)

		s.sendEvent(event)
	}
}

// getTableInfo retrieves cached table information
func (s *PGStreamer) getTableInfo(relationID uint32) *TableInfo {
	s.tableMu.RLock()
	defer s.tableMu.RUnlock()
	return s.tableCache[relationID]
}

// convertColumns converts table columns to the format expected by MySQL
func (s *PGStreamer) convertColumns(tableInfo *TableInfo) []Column {
	columns := make([]Column, len(tableInfo.Columns))
	for i, col := range tableInfo.Columns {
		columns[i] = Column{
			Name:     col.Name,
			Type:     s.pgTypeToMySQLType(col.DataType),
			Nullable: col.Nullable,
		}
	}
	return columns
}

// pgTypeToMySQLType converts PostgreSQL data types to MySQL types
func (s *PGStreamer) pgTypeToMySQLType(pgType uint32) uint8 {
	// Full mapping of PostgreSQL OIDs to MySQL types
	// Reference: https://www.postgresql.org/docs/current/catalog-pg-type.html
	// Reference: https://dev.mysql.com/doc/internals/en/com-query-response.html#packet-Protocol::ColumnType
	switch pgType {
	// Boolean
	case 16: // bool
		return 1 // MYSQL_TYPE_TINY

	// Integer types
	case 21: // int2/smallint
		return 2 // MYSQL_TYPE_SHORT
	case 23: // int4/integer
		return 3 // MYSQL_TYPE_LONG
	case 20: // int8/bigint
		return 8 // MYSQL_TYPE_LONGLONG
	case 26: // oid
		return 3 // MYSQL_TYPE_LONG

	// Floating point types
	case 700: // float4/real
		return 4 // MYSQL_TYPE_FLOAT
	case 701: // float8/double precision
		return 5 // MYSQL_TYPE_DOUBLE
	case 1700: // numeric/decimal
		return 246 // MYSQL_TYPE_NEWDECIMAL

	// Date/Time types
	case 1082: // date
		return 10 // MYSQL_TYPE_DATE
	case 1083: // time
		return 11 // MYSQL_TYPE_TIME
	case 1266: // timetz
		return 11 // MYSQL_TYPE_TIME
	case 1114: // timestamp
		return 12 // MYSQL_TYPE_DATETIME
	case 1184: // timestamptz
		return 12 // MYSQL_TYPE_DATETIME
	case 1186: // interval
		return 253 // MYSQL_TYPE_VAR_STRING (no direct MySQL equivalent)

	// String types
	case 18: // char (single character)
		return 254 // MYSQL_TYPE_STRING
	case 25: // text
		return 252 // MYSQL_TYPE_BLOB (MySQL TEXT)
	case 1042: // bpchar (blank-padded char)
		return 254 // MYSQL_TYPE_STRING
	case 1043: // varchar
		return 253 // MYSQL_TYPE_VAR_STRING
	case 19: // name (internal PostgreSQL type)
		return 253 // MYSQL_TYPE_VAR_STRING

	// Binary types
	case 17: // bytea
		return 252 // MYSQL_TYPE_BLOB

	// UUID type
	case 2950: // uuid
		return 254 // MYSQL_TYPE_STRING (36 char string)

	// JSON types
	case 114: // json
		return 245 // MYSQL_TYPE_JSON
	case 3802: // jsonb
		return 245 // MYSQL_TYPE_JSON

	// Network types
	case 869: // inet
		return 253 // MYSQL_TYPE_VAR_STRING
	case 650: // cidr
		return 253 // MYSQL_TYPE_VAR_STRING
	case 829: // macaddr
		return 253 // MYSQL_TYPE_VAR_STRING
	case 774: // macaddr8
		return 253 // MYSQL_TYPE_VAR_STRING

	// Geometric types (serialize as text)
	case 600: // point
		return 253 // MYSQL_TYPE_VAR_STRING
	case 601: // lseg
		return 253 // MYSQL_TYPE_VAR_STRING
	case 602: // path
		return 253 // MYSQL_TYPE_VAR_STRING
	case 603: // box
		return 253 // MYSQL_TYPE_VAR_STRING
	case 604: // polygon
		return 253 // MYSQL_TYPE_VAR_STRING
	case 718: // circle
		return 253 // MYSQL_TYPE_VAR_STRING

	// Bit string types
	case 1560: // bit
		return 16 // MYSQL_TYPE_BIT
	case 1562: // varbit
		return 16 // MYSQL_TYPE_BIT

	// Array types (common ones)
	case 1000: // bool[]
		return 245 // MYSQL_TYPE_JSON
	case 1005: // int2[]
		return 245 // MYSQL_TYPE_JSON
	case 1007: // int4[]
		return 245 // MYSQL_TYPE_JSON
	case 1016: // int8[]
		return 245 // MYSQL_TYPE_JSON
	case 1009: // text[]
		return 245 // MYSQL_TYPE_JSON
	case 1015: // varchar[]
		return 245 // MYSQL_TYPE_JSON
	case 1021: // float4[]
		return 245 // MYSQL_TYPE_JSON
	case 1022: // float8[]
		return 245 // MYSQL_TYPE_JSON

	// XML type
	case 142: // xml
		return 253 // MYSQL_TYPE_VAR_STRING

	// Money type
	case 790: // money
		return 246 // MYSQL_TYPE_NEWDECIMAL

	default:
		return 253 // Default to VARCHAR for unknown types
	}
}

// decodeTupleData decodes tuple data from PostgreSQL format
// fallbackValues is used for 'unchanged' columns (TOAST) - typically from old tuple
func (s *PGStreamer) decodeTupleData(tuple *pglogrepl.TupleData, tableInfo *TableInfo, fallbackValues []interface{}) []interface{} {
	if tuple == nil {
		return nil
	}

	values := make([]interface{}, len(tuple.Columns))
	for i, col := range tuple.Columns {
		switch col.DataType {
		case 'n': // null
			values[i] = nil
		case 'u': // unchanged (TOAST column not modified)
			// Use value from fallback (old tuple) if available
			if fallbackValues != nil && i < len(fallbackValues) {
				values[i] = fallbackValues[i]
			} else {
				values[i] = nil
			}
		case 't': // text
			// Convert text to appropriate type based on column data type
			if i < len(tableInfo.Columns) {
				values[i] = s.convertPGValue(string(col.Data), tableInfo.Columns[i].DataType)
			} else {
				values[i] = string(col.Data)
			}
		default:
			values[i] = col.Data
		}
	}
	return values
}

// convertPGValue converts a PostgreSQL text value to the appropriate Go type
func (s *PGStreamer) convertPGValue(value string, pgType uint32) interface{} {
	switch pgType {
	// Boolean
	case 16: // bool
		if value == "t" || value == "true" || value == "1" {
			return int8(1)
		}
		return int8(0)

	// Integer types
	case 21: // int2
		var v int16
		fmt.Sscanf(value, "%d", &v)
		return v
	case 23, 26: // int4, oid
		var v int32
		fmt.Sscanf(value, "%d", &v)
		return v
	case 20: // int8
		var v int64
		fmt.Sscanf(value, "%d", &v)
		return v

	// Floating point types
	case 700: // float4
		var v float32
		fmt.Sscanf(value, "%f", &v)
		return v
	case 701: // float8
		var v float64
		fmt.Sscanf(value, "%f", &v)
		return v

	// Date/Time types
	case 1082: // date
		return value // Keep as string "YYYY-MM-DD"
	case 1083, 1266: // time, timetz
		return value // Keep as string "HH:MM:SS" or "HH:MM:SS+TZ"
	case 1114, 1184: // timestamp, timestamptz
		return value // Keep as string
	case 1186: // interval
		return value // Keep as string "1 year 2 mons..."

	// Numeric types
	case 1700: // numeric
		return value // Keep as string for DECIMAL
	case 790: // money
		// Remove currency symbol if present
		return value

	// String types
	case 18, 19, 25, 1042, 1043: // char, name, text, bpchar, varchar
		return value

	// Binary types
	case 17: // bytea
		// PostgreSQL sends bytea as hex string like \x01020304
		return value

	// UUID
	case 2950: // uuid
		return value // Keep as 36-char string

	// JSON types
	case 114, 3802: // json, jsonb
		return value // Keep as JSON string

	// Network types
	case 869, 650, 829, 774: // inet, cidr, macaddr, macaddr8
		return value

	// Geometric types
	case 600, 601, 602, 603, 604, 718: // point, lseg, path, box, polygon, circle
		return value

	// Bit string types
	case 1560, 1562: // bit, varbit
		return value

	// Array types - keep as PostgreSQL array notation or JSON
	case 1000, 1005, 1007, 1016, 1009, 1015, 1021, 1022:
		// Convert PostgreSQL array format {1,2,3} to JSON array [1,2,3]
		if len(value) > 0 && value[0] == '{' && value[len(value)-1] == '}' {
			return "[" + value[1:len(value)-1] + "]"
		}
		return value

	// XML type
	case 142: // xml
		return value

	default:
		return value
	}
}

// sendEvent sends an event to the event channel with backpressure handling
func (s *PGStreamer) sendEvent(event *ChangeEvent) {
	// Set LSN in event
	event.LSN = uint64(s.clientXLogPos)

	// First try non-blocking send
	select {
	case s.eventChan <- event:
		return
	case <-s.ctx.Done():
		return
	default:
		// Channel is full, apply backpressure with timeout
		if s.metrics != nil {
			s.metrics.IncCDCBackpressure()
		}
	}

	// Get backpressure timeout from config (default 30 minutes)
	backpressureTimeout := s.config.BackpressureTimeout
	if backpressureTimeout <= 0 {
		backpressureTimeout = 30 * time.Minute // Fallback default
	}

	// Blocking send with timeout to prevent indefinite blocking
	timeout := time.NewTimer(backpressureTimeout)
	defer timeout.Stop()

	select {
	case s.eventChan <- event:
		s.logger.Debug("Event sent after backpressure delay",
			zap.String("type", string(event.Type)),
			zap.String("table", event.Table))
	case <-s.ctx.Done():
		s.logger.Warn("Event dropped due to shutdown",
			zap.String("type", string(event.Type)),
			zap.String("table", event.Table))
	case <-timeout.C:
		// Track dropped events
		if s.metrics != nil {
			s.metrics.IncCDCEventsDropped()
		}
		s.logger.Error("Event dropped after timeout - consumer too slow",
			zap.String("type", string(event.Type)),
			zap.String("table", event.Table),
			zap.Duration("timeout", backpressureTimeout))
	}
}
