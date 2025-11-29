package replication

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestCheckpointSaveLoad(t *testing.T) {
	// Create temp directory for test
	tmpDir := t.TempDir()
	checkpointFile := filepath.Join(tmpDir, "test_checkpoint.json")

	logger, _ := zap.NewDevelopment()
	config := &ServerConfig{
		CheckpointFile: checkpointFile,
		PGSlotName:     "test_slot",
	}

	// Create streamer with minimal config (won't actually connect)
	streamer := &PGStreamer{
		config:        config,
		logger:        logger,
		clientXLogPos: 12345678,
	}

	// Test save checkpoint
	err := streamer.saveCheckpoint()
	if err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(checkpointFile); os.IsNotExist(err) {
		t.Fatal("Checkpoint file was not created")
	}

	// Read and verify content
	data, err := os.ReadFile(checkpointFile)
	if err != nil {
		t.Fatalf("Failed to read checkpoint file: %v", err)
	}

	var checkpoint Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		t.Fatalf("Failed to parse checkpoint JSON: %v", err)
	}

	if checkpoint.LSN != 12345678 {
		t.Errorf("Expected LSN=12345678, got %d", checkpoint.LSN)
	}

	if checkpoint.SlotName != "test_slot" {
		t.Errorf("Expected SlotName='test_slot', got '%s'", checkpoint.SlotName)
	}

	// Test load checkpoint
	streamer2 := &PGStreamer{
		config: config,
		logger: logger,
	}

	err = streamer2.loadCheckpoint()
	if err != nil {
		t.Fatalf("Failed to load checkpoint: %v", err)
	}

	if uint64(streamer2.clientXLogPos) != 12345678 {
		t.Errorf("Expected loaded LSN=12345678, got %d", streamer2.clientXLogPos)
	}
}

func TestCheckpointSlotMismatch(t *testing.T) {
	// Create temp directory for test
	tmpDir := t.TempDir()
	checkpointFile := filepath.Join(tmpDir, "test_checkpoint.json")

	// Write checkpoint with different slot name
	checkpoint := Checkpoint{
		LSN:       99999,
		Timestamp: time.Now(),
		SlotName:  "different_slot",
	}
	data, _ := json.Marshal(checkpoint)
	os.WriteFile(checkpointFile, data, 0644)

	logger, _ := zap.NewDevelopment()
	config := &ServerConfig{
		CheckpointFile: checkpointFile,
		PGSlotName:     "my_slot", // Different from saved
	}

	streamer := &PGStreamer{
		config: config,
		logger: logger,
	}

	// Load should succeed but ignore checkpoint due to slot mismatch
	err := streamer.loadCheckpoint()
	if err != nil {
		t.Fatalf("Load should succeed but ignore checkpoint: %v", err)
	}

	// LSN should remain 0 (not loaded)
	if streamer.clientXLogPos != 0 {
		t.Errorf("Expected LSN=0 (ignored checkpoint), got %d", streamer.clientXLogPos)
	}
}

func TestCheckpointMissingFile(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := &ServerConfig{
		CheckpointFile: "/non/existent/path/checkpoint.json",
		PGSlotName:     "test_slot",
	}

	streamer := &PGStreamer{
		config: config,
		logger: logger,
	}

	// Load should succeed (no error for missing file)
	err := streamer.loadCheckpoint()
	if err != nil {
		t.Fatalf("Load should succeed for missing file: %v", err)
	}

	// LSN should remain 0
	if streamer.clientXLogPos != 0 {
		t.Errorf("Expected LSN=0 (no checkpoint), got %d", streamer.clientXLogPos)
	}
}

func TestCheckpointEmptyPath(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := &ServerConfig{
		CheckpointFile: "", // Empty path disables checkpointing
		PGSlotName:     "test_slot",
	}

	streamer := &PGStreamer{
		config:        config,
		logger:        logger,
		clientXLogPos: 12345,
	}

	// Save should succeed (no-op for empty path)
	err := streamer.saveCheckpoint()
	if err != nil {
		t.Fatalf("Save should succeed for empty path: %v", err)
	}

	// Load should succeed (no-op for empty path)
	err = streamer.loadCheckpoint()
	if err != nil {
		t.Fatalf("Load should succeed for empty path: %v", err)
	}
}

func TestCheckpointAtomicWrite(t *testing.T) {
	// Create temp directory for test
	tmpDir := t.TempDir()
	checkpointFile := filepath.Join(tmpDir, "subdir", "checkpoint.json")

	logger, _ := zap.NewDevelopment()
	config := &ServerConfig{
		CheckpointFile: checkpointFile,
		PGSlotName:     "test_slot",
	}

	streamer := &PGStreamer{
		config:        config,
		logger:        logger,
		clientXLogPos: 11111,
	}

	// Save should create subdirectory and write file
	err := streamer.saveCheckpoint()
	if err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// Verify temp file doesn't exist (should be renamed)
	tempFile := checkpointFile + ".tmp"
	if _, err := os.Stat(tempFile); !os.IsNotExist(err) {
		t.Error("Temp file should not exist after successful save")
	}

	// Verify final file exists
	if _, err := os.Stat(checkpointFile); os.IsNotExist(err) {
		t.Error("Checkpoint file should exist")
	}
}

func TestDecodeTupleDataWithFallback(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	streamer := &PGStreamer{
		logger: logger,
	}

	tableInfo := &TableInfo{
		Columns: []ColumnInfo{
			{Name: "id", DataType: 23},   // int4
			{Name: "name", DataType: 25}, // text
			{Name: "data", DataType: 25}, // text (TOAST)
		},
	}

	// Old values (complete)
	oldValues := []interface{}{int32(1), "old_name", "old_data_very_long_toast_content"}

	// Test that fallback is used for unchanged columns
	// Simulating 'u' (unchanged) columns is tricky without real pglogrepl data
	// This test verifies the function doesn't panic with nil fallback
	result := streamer.decodeTupleData(nil, tableInfo, oldValues)
	if result != nil {
		t.Error("Expected nil result for nil tuple")
	}
}

func TestPGTypeToMySQLType(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	streamer := &PGStreamer{
		logger: logger,
	}

	tests := []struct {
		pgType   uint32
		expected uint8
		name     string
	}{
		{16, 1, "bool -> TINY"},
		{21, 2, "int2 -> SHORT"},
		{23, 3, "int4 -> LONG"},
		{20, 8, "int8 -> LONGLONG"},
		{700, 4, "float4 -> FLOAT"},
		{701, 5, "float8 -> DOUBLE"},
		{1700, 246, "numeric -> NEWDECIMAL"},
		{25, 252, "text -> BLOB"},
		{1043, 253, "varchar -> VAR_STRING"},
		{2950, 254, "uuid -> STRING"},
		{114, 245, "json -> JSON"},
		{3802, 245, "jsonb -> JSON"},
		{1082, 10, "date -> DATE"},
		{1083, 11, "time -> TIME"},
		{1114, 12, "timestamp -> DATETIME"},
		{17, 252, "bytea -> BLOB"},
		{99999, 253, "unknown -> VAR_STRING (default)"},
	}

	for _, tt := range tests {
		result := streamer.pgTypeToMySQLType(tt.pgType)
		if result != tt.expected {
			t.Errorf("%s: pgTypeToMySQLType(%d) = %d, expected %d",
				tt.name, tt.pgType, result, tt.expected)
		}
	}
}

func TestConvertPGValue(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	streamer := &PGStreamer{
		logger: logger,
	}

	tests := []struct {
		value    string
		pgType   uint32
		expected interface{}
		name     string
	}{
		{"t", 16, int8(1), "bool true"},
		{"f", 16, int8(0), "bool false"},
		{"123", 21, int16(123), "int2"},
		{"456", 23, int32(456), "int4"},
		{"789", 20, int64(789), "int8"},
		{"3.14", 700, float32(3.14), "float4"},
		{"2.718", 701, float64(2.718), "float8"},
		{"hello", 25, "hello", "text"},
		{"2024-01-15", 1082, "2024-01-15", "date"},
		{"{1,2,3}", 1007, "[1,2,3]", "int4 array"},
	}

	for _, tt := range tests {
		result := streamer.convertPGValue(tt.value, tt.pgType)

		switch expected := tt.expected.(type) {
		case float32:
			if r, ok := result.(float32); !ok || (r-expected > 0.001 && expected-r > 0.001) {
				t.Errorf("%s: convertPGValue(%s, %d) = %v, expected %v",
					tt.name, tt.value, tt.pgType, result, tt.expected)
			}
		case float64:
			if r, ok := result.(float64); !ok || (r-expected > 0.001 && expected-r > 0.001) {
				t.Errorf("%s: convertPGValue(%s, %d) = %v, expected %v",
					tt.name, tt.value, tt.pgType, result, tt.expected)
			}
		default:
			if result != tt.expected {
				t.Errorf("%s: convertPGValue(%s, %d) = %v, expected %v",
					tt.name, tt.value, tt.pgType, result, tt.expected)
			}
		}
	}
}
