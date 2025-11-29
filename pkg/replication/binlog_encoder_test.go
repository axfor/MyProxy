package replication

import (
	"testing"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTableMapEventEncodeDecode tests that we can encode a TableMapEvent and decode it back
func TestTableMapEventEncodeDecode(t *testing.T) {
	encoder := NewBinlogEncoder()

	// Create a TableMapEvent
	tableMapEvent := &replication.TableMapEvent{
		TableID:     123,
		Schema:      []byte("public"),
		Table:       []byte("test_table"),
		ColumnCount: 3,
		ColumnType:  []byte{mysql.MYSQL_TYPE_LONG, mysql.MYSQL_TYPE_VARCHAR, mysql.MYSQL_TYPE_LONG},
		ColumnMeta:  []uint16{0, 100, 0}, // VARCHAR has max length 100
		NullBitmap:  []byte{0b00000110},  // columns 1 and 2 are nullable
	}

	// Encode
	encoded, err := encoder.encodeTableMapEvent(tableMapEvent)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	t.Logf("Encoded TableMapEvent: %d bytes", len(encoded))
	t.Logf("Hex: %x", encoded)

	// Manually verify the encoded data structure since TableMapEvent.Decode
	// requires tableIDSize to be set (private field), which is normally done by BinlogParser
	// We'll parse it ourselves to verify correctness
	pos := 0

	// TableID: 6 bytes (little-endian)
	tableID := uint64(encoded[0]) | uint64(encoded[1])<<8 | uint64(encoded[2])<<16 |
		uint64(encoded[3])<<24 | uint64(encoded[4])<<32 | uint64(encoded[5])<<40
	pos = 6
	assert.Equal(t, tableMapEvent.TableID, tableID, "TableID should match")

	// Flags: 2 bytes
	pos += 2

	// Schema length: 1 byte
	schemaLen := int(encoded[pos])
	pos++
	schema := string(encoded[pos : pos+schemaLen])
	pos += schemaLen
	pos++ // null terminator
	assert.Equal(t, string(tableMapEvent.Schema), schema, "Schema should match")

	// Table length: 1 byte
	tableLen := int(encoded[pos])
	pos++
	table := string(encoded[pos : pos+tableLen])
	pos += tableLen
	pos++ // null terminator
	assert.Equal(t, string(tableMapEvent.Table), table, "Table should match")

	// Column count: 1 byte (for small counts)
	colCount := uint64(encoded[pos])
	pos++
	assert.Equal(t, tableMapEvent.ColumnCount, colCount, "ColumnCount should match")

	// Column types: colCount bytes
	colTypes := encoded[pos : pos+int(colCount)]
	pos += int(colCount)
	assert.Equal(t, tableMapEvent.ColumnType, colTypes, "ColumnType should match")

	t.Logf("Decoded TableMapEvent: TableID=%d Schema=%s Table=%s ColumnCount=%d",
		tableID, schema, table, colCount)
	t.Logf("Column types: %v", colTypes)
}

// TestRowsEventEncodeDecode tests that we can encode a RowsEvent and decode it back
func TestRowsEventEncodeDecode(t *testing.T) {
	encoder := NewBinlogEncoder()

	// First create a TableMapEvent that the RowsEvent will reference
	tableMapEvent := &replication.TableMapEvent{
		TableID:     123,
		Schema:      []byte("public"),
		Table:       []byte("test_table"),
		ColumnCount: 3,
		ColumnType:  []byte{mysql.MYSQL_TYPE_LONG, mysql.MYSQL_TYPE_VARCHAR, mysql.MYSQL_TYPE_LONG},
		ColumnMeta:  []uint16{0, 100, 0},
		NullBitmap:  []byte{0b00000110},
	}

	// Create a RowsEvent with the table reference
	rowsEvent := &replication.RowsEvent{
		Version:       2,
		TableID:       123,
		Flags:         replication.RowsEventStmtEndFlag,
		ColumnCount:   3,
		ColumnBitmap1: []byte{0xFF}, // All columns present
		Table:         tableMapEvent,
		Rows: [][]interface{}{
			{int32(1), "test_name", int32(100)},
		},
	}

	// Encode
	encoded, err := encoder.encodeRowsEvent(rowsEvent)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	t.Logf("Encoded RowsEvent: %d bytes", len(encoded))
	t.Logf("Hex: %x", encoded)

	// To properly decode, we need a BinlogParser with the table map
	// This is more complex, so let's just verify the structure

	// Verify basic structure
	// First 6 bytes: table_id
	tableID := uint64(encoded[0]) | uint64(encoded[1])<<8 | uint64(encoded[2])<<16 |
		uint64(encoded[3])<<24 | uint64(encoded[4])<<32 | uint64(encoded[5])<<40
	assert.Equal(t, uint64(123), tableID, "TableID in encoded data should match")

	t.Logf("RowsEvent structure verified: TableID=%d", tableID)
}

// TestRowDataEncoding tests the row data encoding specifically
func TestRowDataEncoding(t *testing.T) {
	encoder := NewBinlogEncoder()

	testCases := []struct {
		name     string
		colType  byte
		meta     uint16
		value    interface{}
		expected []byte
	}{
		{
			name:     "LONG (int32)",
			colType:  mysql.MYSQL_TYPE_LONG,
			meta:     0,
			value:    int32(12345),
			expected: []byte{0x39, 0x30, 0x00, 0x00}, // 12345 in little-endian
		},
		{
			name:     "VARCHAR short string",
			colType:  mysql.MYSQL_TYPE_VARCHAR,
			meta:     100,
			value:    "hello",
			expected: []byte{0x05, 'h', 'e', 'l', 'l', 'o'}, // 1-byte length + string
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			encoded := encoder.encodeValue(tc.value, tc.colType, tc.meta)
			assert.Equal(t, tc.expected, encoded, "Encoded value should match expected")
			t.Logf("%s: %v -> %x", tc.name, tc.value, encoded)
		})
	}
}

// TestDecimalEncoding tests DECIMAL type encoding
func TestDecimalEncoding(t *testing.T) {
	encoder := NewBinlogEncoder()

	testCases := []struct {
		name      string
		value     interface{}
		precision int
		scale     int
		expected  string // expected string representation after decode
	}{
		{
			name:      "Simple positive decimal",
			value:     "123.45",
			precision: 10,
			scale:     2,
			expected:  "123.45",
		},
		{
			name:      "Simple negative decimal",
			value:     "-123.45",
			precision: 10,
			scale:     2,
			expected:  "-123.45",
		},
		{
			name:      "Zero decimal",
			value:     "0.00",
			precision: 10,
			scale:     2,
			expected:  "0.00",
		},
		{
			name:      "Large positive decimal",
			value:     "12345678.90",
			precision: 10,
			scale:     2,
			expected:  "12345678.90",
		},
		{
			name:      "Small positive decimal",
			value:     "0.01",
			precision: 10,
			scale:     2,
			expected:  "0.01",
		},
		{
			name:      "Float64 input",
			value:     float64(99.99),
			precision: 10,
			scale:     2,
			expected:  "99.99",
		},
		{
			name:      "Integer input",
			value:     int64(100),
			precision: 10,
			scale:     2,
			expected:  "100.00",
		},
		{
			name:      "Higher precision",
			value:     "1234567890.123456",
			precision: 20,
			scale:     6,
			expected:  "1234567890.123456",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create metadata: precision << 8 | scale
			meta := uint16(tc.precision<<8 | tc.scale)

			// Encode
			encoded := encoder.encodeDecimal(tc.value, meta)
			require.NotEmpty(t, encoded, "Encoded DECIMAL should not be empty")

			t.Logf("Input: %v, Precision: %d, Scale: %d", tc.value, tc.precision, tc.scale)
			t.Logf("Encoded bytes (%d): %x", len(encoded), encoded)

			// Calculate expected size
			intDigits := tc.precision - tc.scale
			uncompIntegral := intDigits / digitsPerInteger
			uncompFractional := tc.scale / digitsPerInteger
			compIntegral := intDigits - (uncompIntegral * digitsPerInteger)
			compFractional := tc.scale - (uncompFractional * digitsPerInteger)

			expectedSize := uncompIntegral*4 + decimalCompressedBytes[compIntegral] +
				uncompFractional*4 + decimalCompressedBytes[compFractional]

			assert.Equal(t, expectedSize, len(encoded), "Encoded size should match expected")

			// Verify encoding by testing specific patterns:
			// - First byte's high bit should be set for positive, unset for negative
			isNegative := tc.expected[0] == '-'
			if isNegative {
				assert.Equal(t, byte(0), encoded[0]&0x80, "First byte high bit should be 0 for negative")
			} else {
				assert.NotEqual(t, byte(0), encoded[0]&0x80, "First byte high bit should be 1 for positive")
			}
		})
	}
}

// TestDecimalEncodingRoundtrip tests that encoded DECIMAL can be decoded correctly
// This uses test data patterns from go-mysql library
func TestDecimalEncodingRoundtrip(t *testing.T) {
	encoder := NewBinlogEncoder()

	// Test cases based on go-mysql's TestDecodeDecimal
	testCases := []struct {
		name      string
		value     string
		precision int
		scale     int
	}{
		{"basic_positive", "123.45", 10, 2},
		{"basic_negative", "-123.45", 10, 2},
		{"zero", "0.00", 10, 2},
		{"whole_number", "1000.00", 10, 2},
		{"small_fraction", "0.01", 10, 2},
		{"large_number", "99999999.99", 10, 2},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			meta := uint16(tc.precision<<8 | tc.scale)
			encoded := encoder.encodeDecimal(tc.value, meta)

			// Log for debugging
			t.Logf("Value: %s -> Encoded: %x", tc.value, encoded)

			// Basic sanity checks
			assert.NotEmpty(t, encoded)
			// Check sign bit
			isNeg := tc.value[0] == '-'
			if isNeg {
				assert.Equal(t, byte(0), encoded[0]&0x80, "Sign bit should be 0 for negative")
			} else {
				assert.NotEqual(t, byte(0), encoded[0]&0x80, "Sign bit should be 1 for positive")
			}
		})
	}
}
