// Package replication implements MySQL binlog replication protocol server.
// It enables MySQL CDC clients (like go-mysql/canal) to receive change events
// from PostgreSQL via the MySQL binlog protocol.
//
// Architecture:
//   MySQL CDC Client (canal) --COM_BINLOG_DUMP--> aproxy (binlog server) <--logical replication-- PostgreSQL
package replication

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"aproxy/pkg/observability"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"go.uber.org/zap"
)

// ServerConfig holds the configuration for the binlog replication server
type ServerConfig struct {
	// Enabled enables the binlog replication server
	Enabled bool `yaml:"enabled"`

	// ServerID is the MySQL server ID for replication
	ServerID uint32 `yaml:"server_id"`

	// PostgreSQL connection for logical replication
	PGHost         string `yaml:"pg_host"`
	PGPort         int    `yaml:"pg_port"`
	PGDatabase     string `yaml:"pg_database"`
	PGUser         string `yaml:"pg_user"`
	PGPassword     string `yaml:"pg_password"`
	PGSlotName     string `yaml:"pg_slot_name"`
	PGPublicationName string `yaml:"pg_publication_name"`

	// Binlog settings
	BinlogFilename string `yaml:"binlog_filename"` // e.g., "mysql-bin.000001"
	BinlogPosition uint32 `yaml:"binlog_position"`

	// BackpressureTimeout is the maximum time to wait when event channel is full
	// before dropping events. Supports Go duration format: 30m, 1h, 10m, etc.
	// Default: 30 minutes
	BackpressureTimeout time.Duration `yaml:"backpressure_timeout"`

	// Checkpoint settings for LSN persistence
	CheckpointFile     string        `yaml:"checkpoint_file"`     // File path to store LSN checkpoint
	CheckpointInterval time.Duration `yaml:"checkpoint_interval"` // Interval to save checkpoint (default: 10s)

	// Reconnect settings
	ReconnectEnabled     bool          `yaml:"reconnect_enabled"`      // Enable auto-reconnect on connection loss
	ReconnectMaxRetries  int           `yaml:"reconnect_max_retries"`  // Max reconnect attempts (0 = unlimited)
	ReconnectInitialWait time.Duration `yaml:"reconnect_initial_wait"` // Initial wait before reconnect
	ReconnectMaxWait     time.Duration `yaml:"reconnect_max_wait"`     // Max wait between reconnects
}

// DefaultServerConfig returns default configuration
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		Enabled:              false,
		ServerID:             1,
		PGHost:               "localhost",
		PGPort:               5432,
		PGDatabase:           "postgres",
		PGUser:               "postgres",
		PGSlotName:           "aproxy_cdc",
		PGPublicationName:    "aproxy_pub",
		BinlogFilename:       "mysql-bin.000001",
		BinlogPosition:       4,
		BackpressureTimeout:  30 * time.Minute,  // 30 minutes default
		CheckpointFile:       "./data/cdc_checkpoint.json",
		CheckpointInterval:   10 * time.Second,  // Save checkpoint every 10 seconds
		ReconnectEnabled:     true,              // Enable auto-reconnect by default
		ReconnectMaxRetries:  0,                 // Unlimited retries
		ReconnectInitialWait: 1 * time.Second,   // Start with 1 second
		ReconnectMaxWait:     30 * time.Second,  // Max 30 seconds between retries
	}
}

// Server implements MySQL binlog replication protocol server
type Server struct {
	config   *ServerConfig
	logger   *zap.Logger
	metrics  *observability.Metrics
	mu       sync.RWMutex

	// Binlog state
	position     mysql.Position
	serverID     uint32
	serverUUID   string

	// GTID state
	gtidEnabled   bool
	transactionID uint64 // Monotonically increasing transaction ID

	// Connected dump clients
	clients     map[uint32]*DumpClient
	clientsMu   sync.RWMutex
	nextClientID uint32

	// Event channel from PostgreSQL
	eventChan  chan *ChangeEvent

	// Lifecycle
	ctx        context.Context
	cancel     context.CancelFunc
	running    atomic.Bool
	wg         sync.WaitGroup

	// PostgreSQL logical replication
	pgStreamer *PGStreamer
}

// DumpClient represents a connected MySQL binlog dump client
type DumpClient struct {
	ID          uint32
	ServerID    uint32    // Client's server ID from DUMP request
	Conn        net.Conn
	Position    mysql.Position
	StartTime   time.Time
	EventsSent  uint64
	seqNum      uint8     // MySQL packet sequence number
	mu          sync.Mutex
	done        chan struct{} // Signals when streaming is complete
}

// ChangeEvent represents a change event from PostgreSQL
type ChangeEvent struct {
	Type       ChangeType
	Timestamp  time.Time
	Database   string
	Schema     string
	Table      string
	TableID    uint64
	Columns    []Column
	OldValues  []interface{}
	NewValues  []interface{}
	LSN        uint64 // PostgreSQL Log Sequence Number
}

// ChangeType represents the type of change
type ChangeType uint8

const (
	ChangeTypeInsert ChangeType = iota
	ChangeTypeUpdate
	ChangeTypeDelete
	ChangeTypeBegin
	ChangeTypeCommit
	ChangeTypeDDL
	ChangeTypeTruncate
)

// changeTypeToString converts ChangeType to string for logging
func changeTypeToString(ct ChangeType) string {
	switch ct {
	case ChangeTypeInsert:
		return "INSERT"
	case ChangeTypeUpdate:
		return "UPDATE"
	case ChangeTypeDelete:
		return "DELETE"
	case ChangeTypeBegin:
		return "BEGIN"
	case ChangeTypeCommit:
		return "COMMIT"
	case ChangeTypeDDL:
		return "DDL"
	case ChangeTypeTruncate:
		return "TRUNCATE"
	default:
		return "UNKNOWN"
	}
}

// Column represents a table column
type Column struct {
	Name     string
	Type     uint8 // MySQL column type
	Nullable bool
	Unsigned bool
}

// NewServer creates a new binlog replication server
func NewServer(config *ServerConfig, logger *zap.Logger, metrics *observability.Metrics) (*Server, error) {
	if config == nil {
		config = DefaultServerConfig()
	}

	if !config.Enabled {
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	s := &Server{
		config:        config,
		logger:        logger,
		metrics:       metrics,
		serverID:      config.ServerID,
		serverUUID:    generateUUID(),
		gtidEnabled:   true, // Enable GTID by default
		transactionID: 0,
		clients:       make(map[uint32]*DumpClient),
		eventChan:     make(chan *ChangeEvent, 10000),
		ctx:           ctx,
		cancel:        cancel,
		position: mysql.Position{
			Name: config.BinlogFilename,
			Pos:  config.BinlogPosition,
		},
	}

	logger.Info("Binlog replication server initialized",
		zap.Uint32("server_id", s.serverID),
		zap.String("server_uuid", s.serverUUID),
		zap.Bool("gtid_enabled", s.gtidEnabled),
	)

	return s, nil
}

// Start starts the binlog replication server
func (s *Server) Start() error {
	if s == nil || !s.config.Enabled {
		return nil
	}

	if !s.running.CompareAndSwap(false, true) {
		return fmt.Errorf("server already running")
	}

	// Start PostgreSQL logical replication streamer
	streamer, err := NewPGStreamer(s.config, s.logger, s.metrics, s.eventChan)
	if err != nil {
		s.running.Store(false)
		return fmt.Errorf("failed to create PG streamer: %w", err)
	}
	s.pgStreamer = streamer

	s.wg.Add(1)
	go s.eventProcessor()

	s.logger.Info("Binlog replication server started")
	return nil
}

// Stop stops the binlog replication server
func (s *Server) Stop() error {
	if s == nil || !s.running.Load() {
		return nil
	}

	s.cancel()

	if s.pgStreamer != nil {
		s.pgStreamer.Stop()
	}

	// Close all dump clients
	s.clientsMu.Lock()
	for _, client := range s.clients {
		client.Conn.Close()
	}
	s.clients = make(map[uint32]*DumpClient)
	s.clientsMu.Unlock()

	s.wg.Wait()
	s.running.Store(false)

	s.logger.Info("Binlog replication server stopped")
	return nil
}

// HandleBinlogDump handles COM_BINLOG_DUMP request from MySQL client
// IMPORTANT: This function MUST block until streaming is complete.
// If it returns early, go-mysql library will send an OK packet which conflicts
// with our binlog events and causes "invalid sequence" errors.
func (s *Server) HandleBinlogDump(conn net.Conn, serverID uint32, pos mysql.Position) error {
	if s == nil {
		return fmt.Errorf("replication server not initialized")
	}

	clientID := atomic.AddUint32(&s.nextClientID, 1)

	client := &DumpClient{
		ID:        clientID,
		ServerID:  serverID,
		Conn:      conn,
		Position:  pos,
		StartTime: time.Now(),
		seqNum:    1, // Client sends COM_BINLOG_DUMP with seq 0, increments to 1, expects first response with seq 1
		done:      make(chan struct{}), // Initialize done channel for blocking
	}

	s.clientsMu.Lock()
	s.clients[clientID] = client
	s.clientsMu.Unlock()

	// Track connected clients
	if s.metrics != nil {
		s.metrics.IncCDCConnectedClients()
	}

	s.logger.Info("New dump client connected",
		zap.Uint32("client_id", clientID),
		zap.Uint32("server_id", serverID),
		zap.String("position", pos.String()),
	)

	// Send initial events
	if err := s.sendFormatDescriptionEvent(client); err != nil {
		s.removeDumpClient(clientID)
		return fmt.Errorf("failed to send format description: %w", err)
	}

	// Start streaming events to this client in a goroutine
	go s.streamToClient(client)

	// Block until streaming is complete
	// This prevents go-mysql from sending an OK packet after we return
	<-client.done

	return nil
}

// eventProcessor processes events from PostgreSQL and distributes to clients
func (s *Server) eventProcessor() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		case event := <-s.eventChan:
			s.processChangeEvent(event)
		}
	}
}

// processChangeEvent converts PostgreSQL change event to MySQL binlog event
func (s *Server) processChangeEvent(event *ChangeEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Track event metrics
	if s.metrics != nil {
		s.metrics.IncCDCEvents(changeTypeToString(event.Type))
		s.metrics.SetCDCLastLSN(float64(event.LSN))
	}

	// Debug: log change event details
	s.logger.Debug("Processing change event",
		zap.String("type", changeTypeToString(event.Type)),
		zap.String("schema", event.Schema),
		zap.String("table", event.Table),
		zap.Int("columns", len(event.Columns)),
		zap.Any("newValues", event.NewValues),
	)

	// Convert to MySQL binlog events
	binlogEvents := s.convertToMySQLEvents(event)

	// Broadcast to all dump clients
	s.clientsMu.RLock()
	for _, client := range s.clients {
		for _, evt := range binlogEvents {
			if err := s.sendBinlogEvent(client, evt); err != nil {
				s.logger.Error("Failed to send event to client",
					zap.Uint32("client_id", client.ID),
					zap.Error(err),
				)
			} else {
				atomic.AddUint64(&client.EventsSent, 1)
			}
		}
	}
	s.clientsMu.RUnlock()

	// Update position
	s.position.Pos += uint32(len(binlogEvents) * 100) // Approximate position increment
}

// convertToMySQLEvents converts a PostgreSQL change event to MySQL binlog events
func (s *Server) convertToMySQLEvents(event *ChangeEvent) []*replication.BinlogEvent {
	var events []*replication.BinlogEvent

	switch event.Type {
	case ChangeTypeBegin:
		// Increment transaction ID for new transaction
		s.transactionID++

		// Add GTID event before BEGIN if enabled
		if s.gtidEnabled {
			events = append(events, s.createGTIDEvent())
		}

		// Create QueryEvent for BEGIN
		events = append(events, s.createQueryEvent("BEGIN"))

	case ChangeTypeCommit:
		// Create XIDEvent for COMMIT
		events = append(events, s.createXIDEvent())

	case ChangeTypeInsert, ChangeTypeUpdate, ChangeTypeDelete:
		// Create TableMapEvent
		tableMapEvt := s.createTableMapEvent(event)
		events = append(events, tableMapEvt)

		// Create RowsEvent
		rowsEvt := s.createRowsEvent(event)
		events = append(events, rowsEvt)

	case ChangeTypeDDL:
		// Increment transaction ID for DDL
		s.transactionID++

		// Add GTID event before DDL if enabled
		if s.gtidEnabled {
			events = append(events, s.createGTIDEvent())
		}

		// Create QueryEvent for DDL
		events = append(events, s.createQueryEvent("-- DDL event"))

	case ChangeTypeTruncate:
		// Increment transaction ID for TRUNCATE
		s.transactionID++

		// Add GTID event before TRUNCATE if enabled
		if s.gtidEnabled {
			events = append(events, s.createGTIDEvent())
		}

		// Create QueryEvent for TRUNCATE TABLE
		// MySQL binlog represents TRUNCATE as a QUERY_EVENT
		query := fmt.Sprintf("TRUNCATE TABLE `%s`.`%s`", event.Schema, event.Table)
		events = append(events, s.createQueryEvent(query))
	}

	return events
}

// createQueryEvent creates a MySQL QueryEvent
func (s *Server) createQueryEvent(query string) *replication.BinlogEvent {
	event := &replication.BinlogEvent{
		Header: &replication.EventHeader{
			Timestamp: uint32(time.Now().Unix()),
			EventType: replication.QUERY_EVENT,
			ServerID:  s.serverID,
			LogPos:    s.position.Pos,
		},
		Event: &replication.QueryEvent{
			SlaveProxyID:  0,
			ExecutionTime: 0,
			ErrorCode:     0,
			Schema:        []byte(""),
			Query:         []byte(query),
		},
	}
	return event
}

// createXIDEvent creates a MySQL XIDEvent (transaction commit)
func (s *Server) createXIDEvent() *replication.BinlogEvent {
	event := &replication.BinlogEvent{
		Header: &replication.EventHeader{
			Timestamp: uint32(time.Now().Unix()),
			EventType: replication.XID_EVENT,
			ServerID:  s.serverID,
			LogPos:    s.position.Pos,
		},
		Event: &replication.XIDEvent{
			XID: uint64(time.Now().UnixNano()),
		},
	}
	return event
}

// createGTIDEvent creates a MySQL GTID event
// GTID format: server_uuid:transaction_id (e.g., "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1")
func (s *Server) createGTIDEvent() *replication.BinlogEvent {
	// Build GTID string: server_uuid:transaction_id
	gtid := fmt.Sprintf("%s:%d", s.serverUUID, s.transactionID)

	// Convert UUID string to 16-byte binary format
	sid := uuidStringToBytes(s.serverUUID)

	event := &replication.BinlogEvent{
		Header: &replication.EventHeader{
			Timestamp: uint32(time.Now().Unix()),
			EventType: replication.GTID_EVENT, // Event type 33
			ServerID:  s.serverID,
			LogPos:    s.position.Pos,
		},
		Event: &replication.GTIDEvent{
			CommitFlag:     1,                        // 1 = committed transaction
			SID:            sid,                      // Server UUID as 16-byte binary
			GNO:            int64(s.transactionID),   // Transaction number (GTID sequence)
			LastCommitted:  int64(s.transactionID - 1), // Previous transaction
			SequenceNumber: int64(s.transactionID),   // Sequence number for parallel replication
		},
	}

	s.logger.Debug("Created GTID event",
		zap.String("gtid", gtid),
		zap.Uint64("transaction_id", s.transactionID),
	)

	return event
}

// uuidStringToBytes converts a UUID string (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx) to 16 bytes
func uuidStringToBytes(uuid string) []byte {
	// Remove dashes and convert hex to bytes
	cleaned := ""
	for _, c := range uuid {
		if c != '-' {
			cleaned += string(c)
		}
	}

	result := make([]byte, 16)
	for i := 0; i < 16 && i*2+1 < len(cleaned); i++ {
		var b byte
		fmt.Sscanf(cleaned[i*2:i*2+2], "%02x", &b)
		result[i] = b
	}
	return result
}

// createTableMapEvent creates a MySQL TableMapEvent
func (s *Server) createTableMapEvent(event *ChangeEvent) *replication.BinlogEvent {
	columnCount := len(event.Columns)
	columnTypes := make([]byte, columnCount)
	columnMeta := make([]uint16, columnCount)
	columnNames := make([][]byte, columnCount)
	nullBitmap := make([]byte, (columnCount+7)/8)

	for i, col := range event.Columns {
		columnTypes[i] = col.Type
		// Set metadata based on column type
		columnMeta[i] = getColumnMeta(col.Type)
		// Set column name
		columnNames[i] = []byte(col.Name)
		// Set nullable bit
		if col.Nullable {
			nullBitmap[i/8] |= 1 << uint(i%8)
		}
	}

	evt := &replication.BinlogEvent{
		Header: &replication.EventHeader{
			Timestamp: uint32(time.Now().Unix()),
			EventType: replication.TABLE_MAP_EVENT,
			ServerID:  s.serverID,
			LogPos:    s.position.Pos,
		},
		Event: &replication.TableMapEvent{
			TableID:     event.TableID,
			Schema:      []byte(event.Schema),
			Table:       []byte(event.Table),
			ColumnType:  columnTypes,
			ColumnMeta:  columnMeta,
			ColumnCount: uint64(columnCount),
			NullBitmap:  nullBitmap,
			ColumnName:  columnNames, // Optional metadata: column names
		},
	}
	return evt
}

// getColumnMeta returns the metadata for a MySQL column type
func getColumnMeta(colType uint8) uint16 {
	switch colType {
	case mysql.MYSQL_TYPE_VARCHAR, mysql.MYSQL_TYPE_VAR_STRING:
		return 65535 // max length for VARCHAR
	case mysql.MYSQL_TYPE_STRING:
		return 255 // max length for CHAR
	case mysql.MYSQL_TYPE_BLOB:
		return 2 // 2 bytes for length prefix (BLOB)
	case mysql.MYSQL_TYPE_JSON:
		return 4 // 4 bytes for length prefix
	case mysql.MYSQL_TYPE_NEWDECIMAL:
		return (10 << 8) | 2 // precision 10, scale 2
	case mysql.MYSQL_TYPE_DATETIME2, mysql.MYSQL_TYPE_TIMESTAMP2, mysql.MYSQL_TYPE_TIME2:
		return 0 // no fractional seconds
	case mysql.MYSQL_TYPE_FLOAT:
		return 4 // 4 bytes
	case mysql.MYSQL_TYPE_DOUBLE:
		return 8 // 8 bytes
	default:
		return 0 // no metadata needed
	}
}

// createRowsEvent creates a MySQL RowsEvent
func (s *Server) createRowsEvent(event *ChangeEvent) *replication.BinlogEvent {
	var eventType replication.EventType
	var needBitmap2 bool
	switch event.Type {
	case ChangeTypeInsert:
		eventType = replication.WRITE_ROWS_EVENTv2
	case ChangeTypeUpdate:
		eventType = replication.UPDATE_ROWS_EVENTv2
		needBitmap2 = true
	case ChangeTypeDelete:
		eventType = replication.DELETE_ROWS_EVENTv2
	}

	rows := make([][]interface{}, 0)
	if event.Type == ChangeTypeUpdate {
		// For UPDATE, include both old and new values
		// If OldValues is nil (PostgreSQL REPLICA IDENTITY not FULL), use NewValues as fallback
		oldVals := event.OldValues
		if oldVals == nil {
			oldVals = event.NewValues
		}
		rows = append(rows, oldVals)
		rows = append(rows, event.NewValues)
	} else if event.Type == ChangeTypeInsert {
		rows = append(rows, event.NewValues)
	} else {
		// For DELETE, use OldValues if available, otherwise use NewValues
		oldVals := event.OldValues
		if oldVals == nil {
			oldVals = event.NewValues
		}
		rows = append(rows, oldVals)
	}

	// Build column bitmap (all columns present)
	columnCount := uint64(len(event.Columns))
	bitmapSize := (int(columnCount) + 7) / 8
	columnBitmap := make([]byte, bitmapSize)
	for i := 0; i < bitmapSize; i++ {
		columnBitmap[i] = 0xFF
	}

	// Build TableMapEvent reference for row encoding
	columnTypes := make([]byte, len(event.Columns))
	columnMeta := make([]uint16, len(event.Columns))
	for i, col := range event.Columns {
		columnTypes[i] = col.Type
		columnMeta[i] = getColumnMeta(col.Type)
	}
	tableMap := &replication.TableMapEvent{
		TableID:     event.TableID,
		Schema:      []byte(event.Schema),
		Table:       []byte(event.Table),
		ColumnType:  columnTypes,
		ColumnMeta:  columnMeta,
		ColumnCount: columnCount,
	}

	rowsEvent := &replication.RowsEvent{
		Version:       2,
		TableID:       event.TableID,
		Flags:         replication.RowsEventStmtEndFlag,
		ColumnCount:   columnCount,
		ColumnBitmap1: columnBitmap,
		Rows:          rows,
		Table:         tableMap,
	}

	if needBitmap2 {
		rowsEvent.ColumnBitmap2 = columnBitmap
	}

	evt := &replication.BinlogEvent{
		Header: &replication.EventHeader{
			Timestamp: uint32(time.Now().Unix()),
			EventType: eventType,
			ServerID:  s.serverID,
			LogPos:    s.position.Pos,
		},
		Event: rowsEvent,
	}
	return evt
}

// sendFormatDescriptionEvent sends the initial format description event
func (s *Server) sendFormatDescriptionEvent(client *DumpClient) error {
	// Build a complete FORMAT_DESCRIPTION_EVENT for MySQL 5.7+
	// The FDE payload contains:
	// - 2 bytes: binlog version (4)
	// - 50 bytes: server version string
	// - 4 bytes: timestamp
	// - 1 byte: header length (19)
	// - N bytes: post-header lengths for each event type

	// FDE payload for MySQL 5.7/8.0 compatible
	serverVersion := "8.0.11-aproxy"
	versionBytes := make([]byte, 50)
	copy(versionBytes, serverVersion)

	// Post-header lengths for MySQL 8.0 (simplified, 40 event types)
	postHeaderLengths := []byte{
		0,   // UNKNOWN_EVENT (0)
		13,  // START_EVENT_V3 (1)
		0,   // QUERY_EVENT (2)
		8,   // STOP_EVENT (3)
		0,   // ROTATE_EVENT (4)
		0,   // INTVAR_EVENT (5)
		0,   // LOAD_EVENT (6)
		0,   // SLAVE_EVENT (7)
		0,   // CREATE_FILE_EVENT (8)
		0,   // APPEND_BLOCK_EVENT (9)
		0,   // EXEC_LOAD_EVENT (10)
		0,   // DELETE_FILE_EVENT (11)
		0,   // NEW_LOAD_EVENT (12)
		0,   // RAND_EVENT (13)
		0,   // USER_VAR_EVENT (14)
		0,   // FORMAT_DESCRIPTION_EVENT (15)
		8,   // XID_EVENT (16)
		0,   // BEGIN_LOAD_QUERY_EVENT (17)
		0,   // EXECUTE_LOAD_QUERY_EVENT (18)
		8,   // TABLE_MAP_EVENT (19)
		0,   // PRE_GA_WRITE_ROWS_EVENT (20)
		0,   // PRE_GA_UPDATE_ROWS_EVENT (21)
		0,   // PRE_GA_DELETE_ROWS_EVENT (22)
		10,  // WRITE_ROWS_EVENTv1 (23)
		10,  // UPDATE_ROWS_EVENTv1 (24)
		10,  // DELETE_ROWS_EVENTv1 (25)
		0,   // INCIDENT_EVENT (26)
		0,   // HEARTBEAT_LOG_EVENT (27)
		0,   // IGNORABLE_LOG_EVENT (28)
		0,   // ROWS_QUERY_LOG_EVENT (29)
		10,  // WRITE_ROWS_EVENTv2 (30)
		10,  // UPDATE_ROWS_EVENTv2 (31)
		10,  // DELETE_ROWS_EVENTv2 (32)
		0,   // GTID_LOG_EVENT (33)
		0,   // ANONYMOUS_GTID_LOG_EVENT (34)
		0,   // PREVIOUS_GTIDS_LOG_EVENT (35)
		0,   // TRANSACTION_CONTEXT_EVENT (36)
		0,   // VIEW_CHANGE_EVENT (37)
		0,   // XA_PREPARE_LOG_EVENT (38)
		0,   // PARTIAL_UPDATE_ROWS_EVENT (39)
	}

	// Build payload
	payload := make([]byte, 0, 2+50+4+1+len(postHeaderLengths)+1+4) // +1 checksum type +4 checksum
	// Binlog version (2 bytes)
	payload = append(payload, 4, 0)
	// Server version (50 bytes)
	payload = append(payload, versionBytes...)
	// Create timestamp (4 bytes)
	ts := uint32(time.Now().Unix())
	payload = append(payload, byte(ts), byte(ts>>8), byte(ts>>16), byte(ts>>24))
	// Header length (1 byte)
	payload = append(payload, 19)
	// Post-header lengths
	payload = append(payload, postHeaderLengths...)
	// Checksum type: CRC32 (1 byte)
	payload = append(payload, 1)

	// Build complete event with header
	eventSize := uint32(19 + len(payload) + 4) // header + payload + checksum
	nextPos := uint32(4) + eventSize

	event := make([]byte, eventSize)
	// Header (19 bytes)
	binary.LittleEndian.PutUint32(event[0:4], ts)
	event[4] = byte(replication.FORMAT_DESCRIPTION_EVENT)
	binary.LittleEndian.PutUint32(event[5:9], s.serverID)
	binary.LittleEndian.PutUint32(event[9:13], eventSize)
	binary.LittleEndian.PutUint32(event[13:17], nextPos)
	binary.LittleEndian.PutUint16(event[17:19], 0) // flags

	// Payload
	copy(event[19:], payload)

	// CRC32 checksum (last 4 bytes)
	// For simplicity, just use zeros (some clients ignore it when checksum is disabled)
	// A proper implementation would calculate CRC32

	return s.sendBinlogPacketToClient(client, event)
}

// sendBinlogEvent sends a binlog event to a client
func (s *Server) sendBinlogEvent(client *DumpClient, event *replication.BinlogEvent) error {
	// Encode the event
	data, err := encodeBinlogEvent(event)
	if err != nil {
		return fmt.Errorf("failed to encode event: %w", err)
	}

	return s.sendBinlogPacketToClient(client, data)
}

// sendBinlogPacketToClient sends a binlog packet to a specific client with proper sequence number
func (s *Server) sendBinlogPacketToClient(client *DumpClient, data []byte) error {
	client.mu.Lock()
	seq := client.seqNum
	client.seqNum++
	client.mu.Unlock()

	// MySQL packet format: [3 bytes length][1 byte sequence][data]
	packet := make([]byte, 4+1+len(data))

	// Length (3 bytes) - includes the OK header byte
	length := uint32(len(data) + 1)
	packet[0] = byte(length)
	packet[1] = byte(length >> 8)
	packet[2] = byte(length >> 16)

	// Sequence (1 byte) - must increment for each packet
	packet[3] = seq

	// OK header for binlog event
	packet[4] = 0x00

	// Data
	copy(packet[5:], data)

	_, err := client.Conn.Write(packet)
	return err
}

// streamToClient continuously streams events to a dump client
func (s *Server) streamToClient(client *DumpClient) {
	// Signal completion when this function returns
	// This unblocks HandleBinlogDump, allowing it to return properly
	defer close(client.done)
	defer s.removeDumpClient(client.ID)

	// Keep connection alive with heartbeats
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			// Send heartbeat event
			if err := s.sendHeartbeat(client); err != nil {
				s.logger.Error("Failed to send heartbeat",
					zap.Uint32("client_id", client.ID),
					zap.Error(err),
				)
				return
			}
		}
	}
}

// sendHeartbeat sends a heartbeat event to keep the connection alive
func (s *Server) sendHeartbeat(client *DumpClient) error {
	// Create HEARTBEAT_LOG_EVENT
	// Format: header (19 bytes) + empty payload + checksum (4 bytes) = 23 bytes minimum
	eventSize := uint32(19 + 4) // header + checksum
	data := make([]byte, eventSize)

	// Header
	binary.LittleEndian.PutUint32(data[0:4], uint32(time.Now().Unix()))
	data[4] = byte(replication.HEARTBEAT_EVENT)
	binary.LittleEndian.PutUint32(data[5:9], s.serverID)
	binary.LittleEndian.PutUint32(data[9:13], eventSize)
	binary.LittleEndian.PutUint32(data[13:17], s.position.Pos)
	binary.LittleEndian.PutUint16(data[17:19], 0) // flags
	// Checksum (4 bytes) - zeros

	return s.sendBinlogPacketToClient(client, data)
}

// removeDumpClient removes a dump client
func (s *Server) removeDumpClient(clientID uint32) {
	s.clientsMu.Lock()
	if client, ok := s.clients[clientID]; ok {
		client.Conn.Close()
		delete(s.clients, clientID)

		// Track disconnected clients
		if s.metrics != nil {
			s.metrics.DecCDCConnectedClients()
		}

		s.logger.Info("Dump client disconnected",
			zap.Uint32("client_id", clientID),
			zap.Uint64("events_sent", client.EventsSent),
		)
	}
	s.clientsMu.Unlock()
}

// GetPosition returns the current binlog position
func (s *Server) GetPosition() mysql.Position {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.position
}

// GetClientCount returns the number of connected dump clients
func (s *Server) GetClientCount() int {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	return len(s.clients)
}

// generateUUID generates a server UUID
func generateUUID() string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		time.Now().UnixNano()&0xFFFFFFFF,
		time.Now().UnixNano()>>32&0xFFFF,
		0x4000|(time.Now().UnixNano()>>48&0x0FFF),
		0x8000|(time.Now().UnixNano()>>60&0x3FFF),
		time.Now().UnixNano()&0xFFFFFFFFFFFF,
	)
}

// encodeBinlogEvent encodes a binlog event to bytes using the BinlogEncoder
// MySQL binlog event format: [header (19 bytes)][payload][checksum (4 bytes)]
func encodeBinlogEvent(event *replication.BinlogEvent) ([]byte, error) {
	encoder := NewBinlogEncoder()
	return encoder.EncodeEvent(event)
}
