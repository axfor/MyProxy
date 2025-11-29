// Package binlog provides MySQL-like binary logging functionality for aproxy.
// It records all DML (INSERT, UPDATE, DELETE) and DDL statements for:
// - Point-in-time recovery
// - Replication to downstream systems
// - Audit logging
package binlog

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// EventType represents the type of binlog event
type EventType uint8

const (
	EventTypeUnknown EventType = iota
	EventTypeQuery             // Generic query event
	EventTypeInsert            // INSERT statement
	EventTypeUpdate            // UPDATE statement
	EventTypeDelete            // DELETE statement
	EventTypeDDL               // DDL statements (CREATE, ALTER, DROP)
	EventTypeBegin             // Transaction begin
	EventTypeCommit            // Transaction commit
	EventTypeRollback          // Transaction rollback
)

func (t EventType) String() string {
	switch t {
	case EventTypeQuery:
		return "QUERY"
	case EventTypeInsert:
		return "INSERT"
	case EventTypeUpdate:
		return "UPDATE"
	case EventTypeDelete:
		return "DELETE"
	case EventTypeDDL:
		return "DDL"
	case EventTypeBegin:
		return "BEGIN"
	case EventTypeCommit:
		return "COMMIT"
	case EventTypeRollback:
		return "ROLLBACK"
	default:
		return "UNKNOWN"
	}
}

// BinlogEvent represents a single event in the binlog
type BinlogEvent struct {
	Timestamp   time.Time `json:"timestamp"`
	Position    uint64    `json:"position"`
	EventType   EventType `json:"event_type"`
	Database    string    `json:"database"`
	Table       string    `json:"table,omitempty"`
	SessionID   string    `json:"session_id"`
	User        string    `json:"user,omitempty"`
	ClientIP    string    `json:"client_ip,omitempty"`
	MySQLQuery  string    `json:"mysql_query"`  // Original MySQL query
	PGQuery     string    `json:"pg_query"`     // Converted PostgreSQL query
	RowsAffected int64    `json:"rows_affected,omitempty"`
	ExecTime    int64     `json:"exec_time_us"` // Execution time in microseconds
}

// Config holds binlog configuration
type Config struct {
	Enabled       bool          `yaml:"enabled"`
	Dir           string        `yaml:"dir"`            // Directory to store binlog files
	MaxFileSize   int64         `yaml:"max_file_size"`  // Max size of each binlog file (bytes)
	MaxFiles      int           `yaml:"max_files"`      // Max number of binlog files to retain
	SyncMode      string        `yaml:"sync_mode"`      // "async", "sync", or "fsync"
	Format        string        `yaml:"format"`         // "json" or "binary"
	FlushInterval time.Duration `yaml:"flush_interval"` // Interval to flush buffer to disk
	BufferSize    int           `yaml:"buffer_size"`    // Write buffer size
	LogDDL        bool          `yaml:"log_ddl"`        // Log DDL statements
	LogDML        bool          `yaml:"log_dml"`        // Log DML statements
	LogSelect     bool          `yaml:"log_select"`     // Log SELECT statements (for audit)
}

// DefaultConfig returns default binlog configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:       false,
		Dir:           "./data/binlog",
		MaxFileSize:   100 * 1024 * 1024, // 100MB
		MaxFiles:      10,
		SyncMode:      "async",
		Format:        "json",
		FlushInterval: time.Second,
		BufferSize:    64 * 1024, // 64KB
		LogDDL:        true,
		LogDML:        true,
		LogSelect:     false,
	}
}

// Writer handles writing binlog events to files
type Writer struct {
	config   *Config
	logger   *zap.Logger
	mu       sync.Mutex
	file     *os.File
	buffer   []byte
	bufPos   int
	position uint64
	fileNum  uint32
	closed   atomic.Bool

	// Async write channel
	eventChan chan *BinlogEvent
	doneChan  chan struct{}
}

// NewWriter creates a new binlog writer
func NewWriter(config *Config, logger *zap.Logger) (*Writer, error) {
	if config == nil {
		config = DefaultConfig()
	}

	if !config.Enabled {
		return nil, nil
	}

	// Create binlog directory if not exists
	if err := os.MkdirAll(config.Dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create binlog directory: %w", err)
	}

	w := &Writer{
		config:    config,
		logger:    logger,
		buffer:    make([]byte, config.BufferSize),
		eventChan: make(chan *BinlogEvent, 10000),
		doneChan:  make(chan struct{}),
	}

	// Find the latest binlog file or create new one
	if err := w.openOrCreateFile(); err != nil {
		return nil, err
	}

	// Start async writer goroutine
	go w.asyncWriter()

	// Start periodic flusher
	go w.periodicFlusher()

	logger.Info("Binlog writer initialized",
		zap.String("dir", config.Dir),
		zap.String("format", config.Format),
		zap.String("sync_mode", config.SyncMode),
	)

	return w, nil
}

// openOrCreateFile opens existing binlog file or creates a new one
func (w *Writer) openOrCreateFile() error {
	// Find existing binlog files
	pattern := filepath.Join(w.config.Dir, "binlog.*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to list binlog files: %w", err)
	}

	if len(matches) > 0 {
		// Find the latest file number
		var maxNum uint32
		for _, m := range matches {
			var num uint32
			if _, err := fmt.Sscanf(filepath.Base(m), "binlog.%06d", &num); err == nil {
				if num > maxNum {
					maxNum = num
				}
			}
		}
		w.fileNum = maxNum

		// Open the latest file and get its size
		latestFile := filepath.Join(w.config.Dir, fmt.Sprintf("binlog.%06d", maxNum))
		info, err := os.Stat(latestFile)
		if err != nil {
			return fmt.Errorf("failed to stat binlog file: %w", err)
		}

		// If file is too large, rotate
		if info.Size() >= w.config.MaxFileSize {
			w.fileNum++
		} else {
			w.position = uint64(info.Size())
		}
	}

	return w.createNewFile()
}

// createNewFile creates a new binlog file
func (w *Writer) createNewFile() error {
	filename := filepath.Join(w.config.Dir, fmt.Sprintf("binlog.%06d", w.fileNum))

	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to create binlog file: %w", err)
	}

	// Write file header if new file
	info, _ := file.Stat()
	if info.Size() == 0 {
		header := fmt.Sprintf("# AProxy Binlog v1.0\n# Created: %s\n# Format: %s\n",
			time.Now().Format(time.RFC3339), w.config.Format)
		if _, err := file.WriteString(header); err != nil {
			file.Close()
			return fmt.Errorf("failed to write binlog header: %w", err)
		}
		w.position = uint64(len(header))
	}

	w.file = file
	w.logger.Info("Opened binlog file", zap.String("file", filename))
	return nil
}

// Write writes an event to the binlog
func (w *Writer) Write(event *BinlogEvent) error {
	if w == nil || w.closed.Load() {
		return nil
	}

	// Check if we should log this event type
	switch event.EventType {
	case EventTypeInsert, EventTypeUpdate, EventTypeDelete:
		if !w.config.LogDML {
			return nil
		}
	case EventTypeDDL:
		if !w.config.LogDDL {
			return nil
		}
	case EventTypeQuery:
		if !w.config.LogSelect {
			return nil
		}
	}

	// Send to async channel
	select {
	case w.eventChan <- event:
		return nil
	default:
		// Channel full, log warning and drop event
		w.logger.Warn("Binlog event channel full, dropping event")
		return fmt.Errorf("binlog event channel full")
	}
}

// asyncWriter processes events from the channel
func (w *Writer) asyncWriter() {
	for {
		select {
		case event := <-w.eventChan:
			if err := w.writeEvent(event); err != nil {
				w.logger.Error("Failed to write binlog event", zap.Error(err))
			}
		case <-w.doneChan:
			// Drain remaining events
			for {
				select {
				case event := <-w.eventChan:
					if err := w.writeEvent(event); err != nil {
						w.logger.Error("Failed to write binlog event during shutdown", zap.Error(err))
					}
				default:
					return
				}
			}
		}
	}
}

// periodicFlusher flushes buffer periodically
func (w *Writer) periodicFlusher() {
	ticker := time.NewTicker(w.config.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.mu.Lock()
			if err := w.flushBuffer(); err != nil {
				w.logger.Error("Failed to flush binlog buffer", zap.Error(err))
			}
			w.mu.Unlock()
		case <-w.doneChan:
			return
		}
	}
}

// writeEvent writes a single event (called from async writer)
func (w *Writer) writeEvent(event *BinlogEvent) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Check if rotation needed
	if w.position >= uint64(w.config.MaxFileSize) {
		if err := w.rotate(); err != nil {
			return err
		}
	}

	// Set position
	event.Position = w.position

	// Serialize event
	var data []byte
	var err error

	if w.config.Format == "json" {
		data, err = json.Marshal(event)
		if err != nil {
			return fmt.Errorf("failed to marshal binlog event: %w", err)
		}
		data = append(data, '\n')
	} else {
		data, err = w.serializeBinary(event)
		if err != nil {
			return err
		}
	}

	// Write to buffer
	if w.bufPos+len(data) > len(w.buffer) {
		if err := w.flushBuffer(); err != nil {
			return err
		}
	}

	copy(w.buffer[w.bufPos:], data)
	w.bufPos += len(data)
	w.position += uint64(len(data))

	// Sync mode handling
	if w.config.SyncMode == "sync" {
		return w.flushBuffer()
	}

	return nil
}

// serializeBinary serializes event to binary format
func (w *Writer) serializeBinary(event *BinlogEvent) ([]byte, error) {
	// Simple binary format:
	// [4 bytes: total length][8 bytes: timestamp][1 byte: event type]
	// [2 bytes: db len][db][2 bytes: table len][table]
	// [4 bytes: mysql query len][mysql query][4 bytes: pg query len][pg query]
	// [8 bytes: rows affected][8 bytes: exec time]

	totalLen := 4 + 8 + 1 + 2 + len(event.Database) + 2 + len(event.Table) +
		4 + len(event.MySQLQuery) + 4 + len(event.PGQuery) + 8 + 8

	buf := make([]byte, totalLen)
	pos := 0

	// Total length
	binary.BigEndian.PutUint32(buf[pos:], uint32(totalLen))
	pos += 4

	// Timestamp
	binary.BigEndian.PutUint64(buf[pos:], uint64(event.Timestamp.UnixNano()))
	pos += 8

	// Event type
	buf[pos] = byte(event.EventType)
	pos++

	// Database
	binary.BigEndian.PutUint16(buf[pos:], uint16(len(event.Database)))
	pos += 2
	copy(buf[pos:], event.Database)
	pos += len(event.Database)

	// Table
	binary.BigEndian.PutUint16(buf[pos:], uint16(len(event.Table)))
	pos += 2
	copy(buf[pos:], event.Table)
	pos += len(event.Table)

	// MySQL query
	binary.BigEndian.PutUint32(buf[pos:], uint32(len(event.MySQLQuery)))
	pos += 4
	copy(buf[pos:], event.MySQLQuery)
	pos += len(event.MySQLQuery)

	// PG query
	binary.BigEndian.PutUint32(buf[pos:], uint32(len(event.PGQuery)))
	pos += 4
	copy(buf[pos:], event.PGQuery)
	pos += len(event.PGQuery)

	// Rows affected
	binary.BigEndian.PutUint64(buf[pos:], uint64(event.RowsAffected))
	pos += 8

	// Exec time
	binary.BigEndian.PutUint64(buf[pos:], uint64(event.ExecTime))

	return buf, nil
}

// flushBuffer flushes the write buffer to disk
func (w *Writer) flushBuffer() error {
	if w.bufPos == 0 {
		return nil
	}

	if _, err := w.file.Write(w.buffer[:w.bufPos]); err != nil {
		return fmt.Errorf("failed to write to binlog file: %w", err)
	}

	if w.config.SyncMode == "fsync" {
		if err := w.file.Sync(); err != nil {
			return fmt.Errorf("failed to sync binlog file: %w", err)
		}
	}

	w.bufPos = 0
	return nil
}

// rotate rotates to a new binlog file
func (w *Writer) rotate() error {
	// Flush current buffer
	if err := w.flushBuffer(); err != nil {
		return err
	}

	// Close current file
	if w.file != nil {
		w.file.Close()
	}

	// Increment file number
	w.fileNum++
	w.position = 0

	// Create new file
	if err := w.createNewFile(); err != nil {
		return err
	}

	// Clean up old files
	go w.cleanupOldFiles()

	return nil
}

// cleanupOldFiles removes old binlog files beyond retention limit
func (w *Writer) cleanupOldFiles() {
	pattern := filepath.Join(w.config.Dir, "binlog.*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		w.logger.Error("Failed to list binlog files for cleanup", zap.Error(err))
		return
	}

	if len(matches) <= w.config.MaxFiles {
		return
	}

	// Remove oldest files
	toRemove := len(matches) - w.config.MaxFiles
	for i := 0; i < toRemove; i++ {
		if err := os.Remove(matches[i]); err != nil {
			w.logger.Error("Failed to remove old binlog file",
				zap.String("file", matches[i]),
				zap.Error(err))
		} else {
			w.logger.Info("Removed old binlog file", zap.String("file", matches[i]))
		}
	}
}

// Close closes the binlog writer
func (w *Writer) Close() error {
	if w == nil || w.closed.Swap(true) {
		return nil
	}

	close(w.doneChan)

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.flushBuffer(); err != nil {
		return err
	}

	if w.file != nil {
		return w.file.Close()
	}

	return nil
}

// GetPosition returns current binlog position
func (w *Writer) GetPosition() (fileNum uint32, position uint64) {
	if w == nil {
		return 0, 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fileNum, w.position
}

// Reader reads binlog events from files
type Reader struct {
	config *Config
	logger *zap.Logger
	file   *os.File
}

// NewReader creates a new binlog reader
func NewReader(config *Config, logger *zap.Logger) *Reader {
	return &Reader{
		config: config,
		logger: logger,
	}
}

// ReadFrom reads events from a specific position
func (r *Reader) ReadFrom(fileNum uint32, position uint64) (<-chan *BinlogEvent, error) {
	filename := filepath.Join(r.config.Dir, fmt.Sprintf("binlog.%06d", fileNum))

	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open binlog file: %w", err)
	}

	// Seek to position
	if _, err := file.Seek(int64(position), io.SeekStart); err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to seek to position: %w", err)
	}

	r.file = file

	eventChan := make(chan *BinlogEvent, 1000)

	go func() {
		defer close(eventChan)
		defer file.Close()

		decoder := json.NewDecoder(file)
		for {
			var event BinlogEvent
			if err := decoder.Decode(&event); err != nil {
				if err != io.EOF {
					r.logger.Error("Failed to decode binlog event", zap.Error(err))
				}
				return
			}
			eventChan <- &event
		}
	}()

	return eventChan, nil
}

// Close closes the reader
func (r *Reader) Close() error {
	if r.file != nil {
		return r.file.Close()
	}
	return nil
}
