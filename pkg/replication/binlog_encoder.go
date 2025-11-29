// Package replication provides MySQL binlog event encoding functionality.
// This encoder is based on the go-mysql library's decoder, implementing the reverse operation.
// Reference: https://github.com/go-mysql-org/go-mysql/blob/master/replication/row_event.go
package replication

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
)

// DECIMAL encoding constants and helper table
// MySQL stores decimal values in groups of 9 digits using 4 bytes.
// Remaining digits use compressed encoding.
const digitsPerInteger = 9

var decimalCompressedBytes = []int{0, 1, 1, 2, 2, 3, 3, 4, 4, 4}

// BinlogEncoder encodes binlog events to bytes
type BinlogEncoder struct {
	tableIDSize int // 6 for MySQL 5.6+, 4 for older versions
}

// NewBinlogEncoder creates a new binlog encoder
func NewBinlogEncoder() *BinlogEncoder {
	return &BinlogEncoder{
		tableIDSize: 6, // MySQL 5.6+ uses 6 bytes for table ID
	}
}

// EncodeEvent encodes a complete binlog event to bytes
// Format: [header (19 bytes)][payload][checksum (4 bytes)]
func (enc *BinlogEncoder) EncodeEvent(event *replication.BinlogEvent) ([]byte, error) {
	// If raw data is available, use it directly
	if event.RawData != nil {
		return event.RawData, nil
	}

	// Encode payload based on event type
	var payload []byte
	var err error

	switch e := event.Event.(type) {
	case *replication.QueryEvent:
		payload, err = enc.encodeQueryEvent(e)
	case *replication.XIDEvent:
		payload, err = enc.encodeXIDEvent(e)
	case *replication.TableMapEvent:
		payload, err = enc.encodeTableMapEvent(e)
	case *replication.RowsEvent:
		payload, err = enc.encodeRowsEvent(e)
	default:
		// For unknown events, create minimal payload
		payload = make([]byte, 4)
	}

	if err != nil {
		return nil, err
	}

	// Build complete event: header (19) + payload + checksum (4)
	eventSize := uint32(19 + len(payload) + 4)
	data := make([]byte, eventSize)

	// Header (19 bytes)
	binary.LittleEndian.PutUint32(data[0:4], event.Header.Timestamp)
	data[4] = byte(event.Header.EventType)
	binary.LittleEndian.PutUint32(data[5:9], event.Header.ServerID)
	binary.LittleEndian.PutUint32(data[9:13], eventSize)
	binary.LittleEndian.PutUint32(data[13:17], event.Header.LogPos)
	binary.LittleEndian.PutUint16(data[17:19], event.Header.Flags)

	// Payload
	copy(data[19:], payload)

	// Checksum (CRC32) - calculate over header + payload
	checksum := crc32.ChecksumIEEE(data[:19+len(payload)])
	binary.LittleEndian.PutUint32(data[19+len(payload):], checksum)

	return data, nil
}

// encodeQueryEvent encodes a QueryEvent
// Reference: https://dev.mysql.com/doc/dev/mysql-server/latest/classbinary__log_1_1Query__event.html
func (enc *BinlogEncoder) encodeQueryEvent(e *replication.QueryEvent) ([]byte, error) {
	// QueryEvent payload:
	// 4 bytes: slave_proxy_id (thread_id)
	// 4 bytes: execution_time
	// 1 byte: schema length
	// 2 bytes: error code
	// 2 bytes: status vars length
	// N bytes: status vars
	// N bytes: schema (null terminated)
	// N bytes: query

	schemaLen := len(e.Schema)
	statusVarsLen := len(e.StatusVars)
	queryLen := len(e.Query)

	payloadSize := 4 + 4 + 1 + 2 + 2 + statusVarsLen + schemaLen + 1 + queryLen
	payload := make([]byte, payloadSize)

	pos := 0

	// slave_proxy_id (4 bytes)
	binary.LittleEndian.PutUint32(payload[pos:], e.SlaveProxyID)
	pos += 4

	// execution_time (4 bytes)
	binary.LittleEndian.PutUint32(payload[pos:], e.ExecutionTime)
	pos += 4

	// schema length (1 byte)
	payload[pos] = byte(schemaLen)
	pos++

	// error code (2 bytes)
	binary.LittleEndian.PutUint16(payload[pos:], e.ErrorCode)
	pos += 2

	// status vars length (2 bytes)
	binary.LittleEndian.PutUint16(payload[pos:], uint16(statusVarsLen))
	pos += 2

	// status vars
	if statusVarsLen > 0 {
		copy(payload[pos:], e.StatusVars)
		pos += statusVarsLen
	}

	// schema (null terminated)
	copy(payload[pos:], e.Schema)
	pos += schemaLen
	payload[pos] = 0 // null terminator
	pos++

	// query
	copy(payload[pos:], e.Query)

	return payload, nil
}

// encodeXIDEvent encodes an XIDEvent
func (enc *BinlogEncoder) encodeXIDEvent(e *replication.XIDEvent) ([]byte, error) {
	// XIDEvent payload: 8 bytes XID
	payload := make([]byte, 8)
	binary.LittleEndian.PutUint64(payload, e.XID)
	return payload, nil
}

// TABLE_MAP optional metadata types
const (
	TABLE_MAP_OPT_META_SIGNEDNESS byte = iota + 1
	TABLE_MAP_OPT_META_DEFAULT_CHARSET
	TABLE_MAP_OPT_META_COLUMN_CHARSET
	TABLE_MAP_OPT_META_COLUMN_NAME
	TABLE_MAP_OPT_META_SET_STR_VALUE
	TABLE_MAP_OPT_META_ENUM_STR_VALUE
	TABLE_MAP_OPT_META_GEOMETRY_TYPE
	TABLE_MAP_OPT_META_SIMPLE_PRIMARY_KEY
	TABLE_MAP_OPT_META_PRIMARY_KEY_WITH_PREFIX
	TABLE_MAP_OPT_META_ENUM_AND_SET_DEFAULT_CHARSET
	TABLE_MAP_OPT_META_ENUM_AND_SET_COLUMN_CHARSET
	TABLE_MAP_OPT_META_COLUMN_VISIBILITY
)

// encodeTableMapEvent encodes a TableMapEvent
// Reference: https://dev.mysql.com/doc/dev/mysql-server/latest/classbinary__log_1_1Table__map__event.html
func (enc *BinlogEncoder) encodeTableMapEvent(e *replication.TableMapEvent) ([]byte, error) {
	// TableMapEvent payload:
	// 6 bytes: table_id (or 4 bytes for older MySQL)
	// 2 bytes: flags
	// 1 byte: schema name length
	// N bytes: schema name
	// 1 byte: null terminator
	// 1 byte: table name length
	// N bytes: table name
	// 1 byte: null terminator
	// lenenc: column count
	// N bytes: column types (1 byte per column)
	// lenenc: metadata length
	// N bytes: metadata
	// N bytes: null bitmap ((column_count + 7) / 8 bytes)
	// N bytes: optional metadata (TLV format)

	schemaLen := len(e.Schema)
	tableLen := len(e.Table)
	columnCount := int(e.ColumnCount)

	// Build metadata
	metadata := enc.encodeColumnMeta(e.ColumnType, e.ColumnMeta)
	metadataLen := len(metadata)

	// NOTE: We don't include optional metadata (column names) because:
	// 1. Canal uses SHOW FULL COLUMNS to get column names, not binlog optional metadata
	// 2. Including malformed optional metadata causes decoding errors in go-mysql

	// Calculate null bitmap size
	nullBitmapSize := (columnCount + 7) / 8

	// Calculate column count encoded length
	columnCountEncoded := mysql.PutLengthEncodedInt(e.ColumnCount)
	metadataLenEncoded := mysql.PutLengthEncodedInt(uint64(metadataLen))

	// Calculate total payload size (no optional metadata)
	payloadSize := enc.tableIDSize + 2 + 1 + schemaLen + 1 + 1 + tableLen + 1 +
		len(columnCountEncoded) + columnCount + len(metadataLenEncoded) + metadataLen + nullBitmapSize

	payload := make([]byte, payloadSize)
	pos := 0

	// table_id (6 bytes for MySQL 5.6+)
	if enc.tableIDSize == 6 {
		binary.LittleEndian.PutUint32(payload[pos:], uint32(e.TableID))
		binary.LittleEndian.PutUint16(payload[pos+4:], uint16(e.TableID>>32))
	} else {
		binary.LittleEndian.PutUint32(payload[pos:], uint32(e.TableID))
	}
	pos += enc.tableIDSize

	// flags (2 bytes)
	binary.LittleEndian.PutUint16(payload[pos:], e.Flags)
	pos += 2

	// schema name length (1 byte)
	payload[pos] = byte(schemaLen)
	pos++

	// schema name
	copy(payload[pos:], e.Schema)
	pos += schemaLen

	// null terminator
	payload[pos] = 0
	pos++

	// table name length (1 byte)
	payload[pos] = byte(tableLen)
	pos++

	// table name
	copy(payload[pos:], e.Table)
	pos += tableLen

	// null terminator
	payload[pos] = 0
	pos++

	// column count (lenenc)
	copy(payload[pos:], columnCountEncoded)
	pos += len(columnCountEncoded)

	// column types
	copy(payload[pos:], e.ColumnType)
	pos += columnCount

	// metadata length (lenenc)
	copy(payload[pos:], metadataLenEncoded)
	pos += len(metadataLenEncoded)

	// metadata
	copy(payload[pos:], metadata)
	pos += metadataLen

	// null bitmap
	if e.NullBitmap != nil && len(e.NullBitmap) > 0 {
		copy(payload[pos:], e.NullBitmap)
	} else {
		// All columns nullable by default
		for i := 0; i < nullBitmapSize; i++ {
			payload[pos+i] = 0xFF
		}
	}
	pos += nullBitmapSize

	// Note: Optional metadata is intentionally omitted
	// Canal uses SHOW FULL COLUMNS to get column names, not optional metadata
	// Including optional metadata caused decode errors in go-mysql's decodeOptionalMeta

	return payload, nil
}

// encodeOptionalMeta encodes optional metadata for TableMapEvent
func (enc *BinlogEncoder) encodeOptionalMeta(e *replication.TableMapEvent) []byte {
	var result []byte

	// Encode column names if present
	if len(e.ColumnName) > 0 {
		// Build column names data
		var namesData []byte
		for _, name := range e.ColumnName {
			// Each name is: length (1 byte) + name bytes
			namesData = append(namesData, byte(len(name)))
			namesData = append(namesData, name...)
		}

		// TLV format: Type (1 byte) + Length (lenenc) + Value
		result = append(result, TABLE_MAP_OPT_META_COLUMN_NAME)
		lenEnc := mysql.PutLengthEncodedInt(uint64(len(namesData)))
		result = append(result, lenEnc...)
		result = append(result, namesData...)
	}

	return result
}

// encodeColumnMeta encodes column metadata based on column types
func (enc *BinlogEncoder) encodeColumnMeta(columnTypes []byte, columnMeta []uint16) []byte {
	var metadata []byte

	for i, t := range columnTypes {
		var meta uint16
		if i < len(columnMeta) {
			meta = columnMeta[i]
		}

		switch t {
		case mysql.MYSQL_TYPE_STRING:
			// 2 bytes: real type << 8 | pack/field length
			metadata = append(metadata, byte(meta>>8), byte(meta))

		case mysql.MYSQL_TYPE_NEWDECIMAL:
			// 2 bytes: precision << 8 | decimals
			metadata = append(metadata, byte(meta>>8), byte(meta))

		case mysql.MYSQL_TYPE_VAR_STRING,
			mysql.MYSQL_TYPE_VARCHAR,
			mysql.MYSQL_TYPE_BIT:
			// 2 bytes: max length (little endian)
			metadata = append(metadata, byte(meta), byte(meta>>8))

		case mysql.MYSQL_TYPE_BLOB,
			mysql.MYSQL_TYPE_DOUBLE,
			mysql.MYSQL_TYPE_FLOAT,
			mysql.MYSQL_TYPE_GEOMETRY,
			mysql.MYSQL_TYPE_JSON:
			// 1 byte: pack length
			metadata = append(metadata, byte(meta))

		case mysql.MYSQL_TYPE_TIME2,
			mysql.MYSQL_TYPE_DATETIME2,
			mysql.MYSQL_TYPE_TIMESTAMP2:
			// 1 byte: fractional seconds precision
			metadata = append(metadata, byte(meta))

		// Types with no metadata
		case mysql.MYSQL_TYPE_DECIMAL,
			mysql.MYSQL_TYPE_TINY,
			mysql.MYSQL_TYPE_SHORT,
			mysql.MYSQL_TYPE_LONG,
			mysql.MYSQL_TYPE_NULL,
			mysql.MYSQL_TYPE_TIMESTAMP,
			mysql.MYSQL_TYPE_LONGLONG,
			mysql.MYSQL_TYPE_INT24,
			mysql.MYSQL_TYPE_DATE,
			mysql.MYSQL_TYPE_TIME,
			mysql.MYSQL_TYPE_DATETIME,
			mysql.MYSQL_TYPE_YEAR:
			// No metadata needed
		}
	}

	return metadata
}

// encodeRowsEvent encodes a RowsEvent
// Reference: https://dev.mysql.com/doc/dev/mysql-server/latest/classbinary__log_1_1Rows__event.html
func (enc *BinlogEncoder) encodeRowsEvent(e *replication.RowsEvent) ([]byte, error) {
	// RowsEvent payload (v2):
	// 6 bytes: table_id
	// 2 bytes: flags
	// 2 bytes: extra data length (for v2)
	// N bytes: extra data (optional)
	// lenenc: column count
	// N bytes: columns present bitmap 1
	// N bytes: columns present bitmap 2 (for UPDATE only)
	// N bytes: row data

	columnCount := int(e.ColumnCount)
	bitmapSize := (columnCount + 7) / 8

	// Encode column count
	columnCountEncoded := mysql.PutLengthEncodedInt(e.ColumnCount)

	// Encode row data
	rowData := enc.encodeRowData(e)

	// Calculate payload size
	// For v2: table_id(6) + flags(2) + extra_data_len(2) + column_count + bitmap1 + [bitmap2] + row_data
	payloadSize := enc.tableIDSize + 2 + 2 + len(columnCountEncoded) + bitmapSize

	// Add bitmap2 for UPDATE events
	needBitmap2 := e.Version >= 1 && len(e.ColumnBitmap2) > 0
	if needBitmap2 {
		payloadSize += bitmapSize
	}

	payloadSize += len(rowData)

	payload := make([]byte, payloadSize)
	pos := 0

	// table_id (6 bytes)
	if enc.tableIDSize == 6 {
		binary.LittleEndian.PutUint32(payload[pos:], uint32(e.TableID))
		binary.LittleEndian.PutUint16(payload[pos+4:], uint16(e.TableID>>32))
	} else {
		binary.LittleEndian.PutUint32(payload[pos:], uint32(e.TableID))
	}
	pos += enc.tableIDSize

	// flags (2 bytes)
	binary.LittleEndian.PutUint16(payload[pos:], e.Flags)
	pos += 2

	// extra data length (2 bytes) - v2 only
	binary.LittleEndian.PutUint16(payload[pos:], 2) // minimum length
	pos += 2

	// column count (lenenc)
	copy(payload[pos:], columnCountEncoded)
	pos += len(columnCountEncoded)

	// column bitmap 1
	if e.ColumnBitmap1 != nil && len(e.ColumnBitmap1) > 0 {
		copy(payload[pos:], e.ColumnBitmap1)
	} else {
		// All columns present
		for i := 0; i < bitmapSize; i++ {
			payload[pos+i] = 0xFF
		}
	}
	pos += bitmapSize

	// column bitmap 2 (for UPDATE events)
	if needBitmap2 {
		copy(payload[pos:], e.ColumnBitmap2)
		pos += bitmapSize
	}

	// row data
	copy(payload[pos:], rowData)

	return payload, nil
}

// encodeRowData encodes the row data portion of a RowsEvent
func (enc *BinlogEncoder) encodeRowData(e *replication.RowsEvent) []byte {
	var data []byte

	if e.Table == nil {
		return data
	}

	columnCount := int(e.ColumnCount)
	nullBitmapSize := (columnCount + 7) / 8

	for _, row := range e.Rows {
		// Null bitmap for this row
		nullBitmap := make([]byte, nullBitmapSize)
		var rowValues []byte

		for i, val := range row {
			if val == nil {
				// Set null bit
				nullBitmap[i/8] |= 1 << uint(i%8)
			} else {
				// Encode value based on column type
				var colType byte
				var meta uint16
				if i < len(e.Table.ColumnType) {
					colType = e.Table.ColumnType[i]
				}
				if i < len(e.Table.ColumnMeta) {
					meta = e.Table.ColumnMeta[i]
				}
				encoded := enc.encodeValue(val, colType, meta)
				rowValues = append(rowValues, encoded...)
			}
		}

		data = append(data, nullBitmap...)
		data = append(data, rowValues...)
	}

	return data
}

// encodeValue encodes a single value based on its MySQL type
// This is the reverse of RowsEvent.decodeValue() in go-mysql
func (enc *BinlogEncoder) encodeValue(val interface{}, colType byte, meta uint16) []byte {
	switch colType {
	case mysql.MYSQL_TYPE_NULL:
		return nil

	case mysql.MYSQL_TYPE_TINY:
		return enc.encodeTiny(val)

	case mysql.MYSQL_TYPE_SHORT:
		return enc.encodeShort(val)

	case mysql.MYSQL_TYPE_INT24:
		return enc.encodeInt24(val)

	case mysql.MYSQL_TYPE_LONG:
		return enc.encodeLong(val)

	case mysql.MYSQL_TYPE_LONGLONG:
		return enc.encodeLongLong(val)

	case mysql.MYSQL_TYPE_FLOAT:
		return enc.encodeFloat(val)

	case mysql.MYSQL_TYPE_DOUBLE:
		return enc.encodeDouble(val)

	case mysql.MYSQL_TYPE_NEWDECIMAL:
		return enc.encodeDecimal(val, meta)

	case mysql.MYSQL_TYPE_YEAR:
		return enc.encodeYear(val)

	case mysql.MYSQL_TYPE_DATE:
		return enc.encodeDate(val)

	case mysql.MYSQL_TYPE_TIME:
		return enc.encodeTime(val)

	case mysql.MYSQL_TYPE_TIME2:
		return enc.encodeTime2(val, meta)

	case mysql.MYSQL_TYPE_DATETIME:
		return enc.encodeDatetime(val)

	case mysql.MYSQL_TYPE_DATETIME2:
		return enc.encodeDatetime2(val, meta)

	case mysql.MYSQL_TYPE_TIMESTAMP:
		return enc.encodeTimestamp(val)

	case mysql.MYSQL_TYPE_TIMESTAMP2:
		return enc.encodeTimestamp2(val, meta)

	case mysql.MYSQL_TYPE_VARCHAR, mysql.MYSQL_TYPE_VAR_STRING:
		return enc.encodeVarString(val, meta)

	case mysql.MYSQL_TYPE_STRING:
		return enc.encodeFixedString(val, meta)

	case mysql.MYSQL_TYPE_BLOB:
		return enc.encodeBlob(val, meta)

	case mysql.MYSQL_TYPE_BIT:
		return enc.encodeBit(val, meta)

	case mysql.MYSQL_TYPE_ENUM:
		return enc.encodeEnum(val, meta)

	case mysql.MYSQL_TYPE_SET:
		return enc.encodeSet(val, meta)

	case mysql.MYSQL_TYPE_JSON:
		return enc.encodeJSON(val, meta)

	case mysql.MYSQL_TYPE_GEOMETRY:
		return enc.encodeBlob(val, meta) // Geometry uses blob encoding

	default:
		// Default: encode as variable string
		return enc.encodeVarString(val, 65535)
	}
}

// ============================================================================
// Integer type encoders
// ============================================================================

func (enc *BinlogEncoder) encodeTiny(val interface{}) []byte {
	var v int8
	switch t := val.(type) {
	case int8:
		v = t
	case int:
		v = int8(t)
	case int64:
		v = int8(t)
	case int32:
		v = int8(t)
	case int16:
		v = int8(t)
	case bool:
		if t {
			v = 1
		}
	}
	return []byte{byte(v)}
}

func (enc *BinlogEncoder) encodeShort(val interface{}) []byte {
	var v int16
	switch t := val.(type) {
	case int16:
		v = t
	case int:
		v = int16(t)
	case int64:
		v = int16(t)
	case int32:
		v = int16(t)
	}
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf, uint16(v))
	return buf
}

func (enc *BinlogEncoder) encodeInt24(val interface{}) []byte {
	var v int32
	switch t := val.(type) {
	case int32:
		v = t
	case int:
		v = int32(t)
	case int64:
		v = int32(t)
	}
	// 3 bytes little endian
	buf := make([]byte, 3)
	buf[0] = byte(v)
	buf[1] = byte(v >> 8)
	buf[2] = byte(v >> 16)
	return buf
}

func (enc *BinlogEncoder) encodeLong(val interface{}) []byte {
	var v int32
	switch t := val.(type) {
	case int32:
		v = t
	case int:
		v = int32(t)
	case int64:
		v = int32(t)
	}
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(v))
	return buf
}

func (enc *BinlogEncoder) encodeLongLong(val interface{}) []byte {
	var v int64
	switch t := val.(type) {
	case int64:
		v = t
	case int:
		v = int64(t)
	case int32:
		v = int64(t)
	}
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(v))
	return buf
}

// ============================================================================
// Float type encoders
// ============================================================================

func (enc *BinlogEncoder) encodeFloat(val interface{}) []byte {
	var v float32
	switch t := val.(type) {
	case float32:
		v = t
	case float64:
		v = float32(t)
	}
	buf := make([]byte, 4)
	bits := math.Float32bits(v)
	binary.LittleEndian.PutUint32(buf, bits)
	return buf
}

func (enc *BinlogEncoder) encodeDouble(val interface{}) []byte {
	var v float64
	switch t := val.(type) {
	case float64:
		v = t
	case float32:
		v = float64(t)
	}
	buf := make([]byte, 8)
	bits := math.Float64bits(v)
	binary.LittleEndian.PutUint64(buf, bits)
	return buf
}

// encodeDecimal encodes a DECIMAL value
// Reference: decodeDecimal in go-mysql
func (enc *BinlogEncoder) encodeDecimal(val interface{}, meta uint16) []byte {
	// Extract precision and scale from metadata
	precision := int(meta >> 8)
	scale := int(meta & 0xFF)

	if precision == 0 {
		precision = 10 // Default precision
	}

	// Convert value to string representation
	var s string
	switch t := val.(type) {
	case string:
		s = t
	case float64:
		s = strconv.FormatFloat(t, 'f', scale, 64)
	case float32:
		s = strconv.FormatFloat(float64(t), 'f', scale, 64)
	case int, int32, int64:
		s = fmt.Sprintf("%d", t)
	default:
		s = fmt.Sprintf("%v", val)
	}

	// Parse sign and numeric string
	negative := false
	if strings.HasPrefix(s, "-") {
		negative = true
		s = s[1:]
	}

	// Split into integer and fractional parts
	var intPart, fracPart string
	if idx := strings.Index(s, "."); idx >= 0 {
		intPart = s[:idx]
		fracPart = s[idx+1:]
	} else {
		intPart = s
		fracPart = ""
	}

	// Remove leading zeros from integer part
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}

	// Calculate storage layout
	intDigits := precision - scale
	if intDigits < 0 {
		intDigits = 0 // Safety: handle invalid precision < scale
	}
	uncompIntegral := intDigits / digitsPerInteger
	uncompFractional := scale / digitsPerInteger
	compIntegral := intDigits - (uncompIntegral * digitsPerInteger)
	compFractional := scale - (uncompFractional * digitsPerInteger)

	// Calculate binary size
	binSize := uncompIntegral*4 + decimalCompressedBytes[compIntegral] +
		uncompFractional*4 + decimalCompressedBytes[compFractional]

	// Handle edge case: zero size decimal (both precision and scale are 0)
	if binSize == 0 {
		binSize = 1 // At minimum, we need 1 byte
	}

	buf := make([]byte, binSize)

	// Mask for negative numbers (XOR all bytes with 0xFF)
	var mask byte = 0
	if negative {
		mask = 0xFF
	}

	pos := 0

	// Pad integer part to full width (handle case where intPart is longer than intDigits)
	var paddedInt string
	if intDigits == 0 {
		paddedInt = ""
	} else if len(intPart) >= intDigits {
		// Truncate to last intDigits characters (overflow case)
		paddedInt = intPart[len(intPart)-intDigits:]
	} else {
		// Pad with leading zeros
		paddedInt = strings.Repeat("0", intDigits-len(intPart)) + intPart
	}

	// Encode compressed integral part (first compIntegral digits)
	if compIntegral > 0 {
		compBytes := decimalCompressedBytes[compIntegral]
		digits := paddedInt[:compIntegral]
		value, _ := strconv.ParseUint(digits, 10, 32)
		enc.encodeDecimalValue(buf[pos:pos+compBytes], uint32(value), compBytes, mask)
		pos += compBytes
	}

	// Encode uncompressed integral parts (groups of 9 digits)
	for i := 0; i < uncompIntegral; i++ {
		start := compIntegral + i*digitsPerInteger
		end := start + digitsPerInteger
		digits := paddedInt[start:end]
		value, _ := strconv.ParseUint(digits, 10, 32)
		enc.encodeDecimalValue(buf[pos:pos+4], uint32(value), 4, mask)
		pos += 4
	}

	// Pad fractional part to full width (handle case where fracPart is longer than scale)
	var paddedFrac string
	if scale == 0 {
		paddedFrac = ""
	} else if len(fracPart) >= scale {
		// Truncate to first scale characters
		paddedFrac = fracPart[:scale]
	} else {
		// Pad with trailing zeros
		paddedFrac = fracPart + strings.Repeat("0", scale-len(fracPart))
	}

	// Encode uncompressed fractional parts (groups of 9 digits)
	for i := 0; i < uncompFractional; i++ {
		start := i * digitsPerInteger
		end := start + digitsPerInteger
		digits := paddedFrac[start:end]
		value, _ := strconv.ParseUint(digits, 10, 32)
		enc.encodeDecimalValue(buf[pos:pos+4], uint32(value), 4, mask)
		pos += 4
	}

	// Encode compressed fractional part (last compFractional digits)
	if compFractional > 0 {
		compBytes := decimalCompressedBytes[compFractional]
		start := uncompFractional * digitsPerInteger
		end := start + compFractional
		digits := paddedFrac[start:end]
		value, _ := strconv.ParseUint(digits, 10, 32)
		enc.encodeDecimalValue(buf[pos:pos+compBytes], uint32(value), compBytes, mask)
		pos += compBytes
	}

	// Set sign bit by XORing first byte with 0x80
	// For positive (mask=0): high bit 0 becomes 1 (indicates positive)
	// For negative (mask=0xFF): XORed high bit 1 becomes 0 (indicates negative)
	// This matches how go-mysql decodes: data[0] ^= 0x80 to clear sign
	buf[0] ^= 0x80

	return buf
}

// encodeDecimalValue writes a decimal component value in big-endian format with XOR mask
func (enc *BinlogEncoder) encodeDecimalValue(buf []byte, value uint32, size int, mask byte) {
	switch size {
	case 1:
		buf[0] = byte(value) ^ mask
	case 2:
		buf[0] = byte(value>>8) ^ mask
		buf[1] = byte(value) ^ mask
	case 3:
		buf[0] = byte(value>>16) ^ mask
		buf[1] = byte(value>>8) ^ mask
		buf[2] = byte(value) ^ mask
	case 4:
		buf[0] = byte(value>>24) ^ mask
		buf[1] = byte(value>>16) ^ mask
		buf[2] = byte(value>>8) ^ mask
		buf[3] = byte(value) ^ mask
	}
}

// ============================================================================
// Date/Time type encoders
// ============================================================================

func (enc *BinlogEncoder) encodeYear(val interface{}) []byte {
	var year int
	switch t := val.(type) {
	case int:
		year = t
	case int64:
		year = int(t)
	case int32:
		year = int(t)
	}
	if year == 0 {
		return []byte{0}
	}
	return []byte{byte(year - 1900)}
}

func (enc *BinlogEncoder) encodeDate(val interface{}) []byte {
	// DATE is stored as 3 bytes: day + month*32 + year*16*32
	var s string
	switch t := val.(type) {
	case string:
		s = t
	case time.Time:
		s = t.Format("2006-01-02")
	default:
		return make([]byte, 3) // zero date
	}

	// Parse date string "YYYY-MM-DD"
	var year, month, day int
	fmt.Sscanf(s, "%d-%d-%d", &year, &month, &day)

	i32 := uint32(year*16*32 + month*32 + day)
	buf := make([]byte, 3)
	buf[0] = byte(i32)
	buf[1] = byte(i32 >> 8)
	buf[2] = byte(i32 >> 16)
	return buf
}

func (enc *BinlogEncoder) encodeTime(val interface{}) []byte {
	// TIME is stored as 3 bytes: second + minute*100 + hour*10000
	var s string
	switch t := val.(type) {
	case string:
		s = t
	default:
		return make([]byte, 3) // zero time
	}

	var hour, minute, second int
	fmt.Sscanf(s, "%d:%d:%d", &hour, &minute, &second)

	i32 := uint32(hour*10000 + minute*100 + second)
	buf := make([]byte, 3)
	buf[0] = byte(i32)
	buf[1] = byte(i32 >> 8)
	buf[2] = byte(i32 >> 16)
	return buf
}

func (enc *BinlogEncoder) encodeTime2(val interface{}, meta uint16) []byte {
	// TIME2 format with fractional seconds support
	// meta indicates the number of fractional digits (0-6)
	// Format: 3 bytes for integer part + (meta+1)/2 bytes for fractional part
	fracBytes := int((meta + 1) / 2)
	n := 3 + fracBytes
	buf := make([]byte, n)

	var s string
	switch t := val.(type) {
	case string:
		s = t
	case time.Time:
		s = t.Format("15:04:05.000000")
	default:
		return buf
	}

	// Parse time components including optional fractional seconds
	var hour, minute, second int
	var frac int64
	var fracStr string

	// Try parsing with fractional seconds first
	parts := strings.SplitN(s, ".", 2)
	if len(parts) == 2 {
		fmt.Sscanf(parts[0], "%d:%d:%d", &hour, &minute, &second)
		fracStr = parts[1]
		// Pad or truncate to match meta digits
		for len(fracStr) < int(meta) {
			fracStr += "0"
		}
		if len(fracStr) > int(meta) {
			fracStr = fracStr[:meta]
		}
		fmt.Sscanf(fracStr, "%d", &frac)
	} else {
		fmt.Sscanf(s, "%d:%d:%d", &hour, &minute, &second)
	}

	// Handle negative time (MySQL supports negative TIME values)
	negative := hour < 0
	if negative {
		hour = -hour
	}

	// Pack integer part into TIME2 format
	// Format: 1 bit sign + 1 bit unused + 10 bits hour + 6 bits minute + 6 bits second
	intPart := int64(hour)<<12 | int64(minute)<<6 | int64(second)
	if !negative {
		intPart += 0x800000 // TIMEF_INT_OFS - offset for positive values
	} else {
		intPart = 0x800000 - intPart // Negative offset
	}

	// Write 3-byte integer part (big-endian)
	buf[0] = byte(intPart >> 16)
	buf[1] = byte(intPart >> 8)
	buf[2] = byte(intPart)

	// Write fractional part based on meta
	if fracBytes > 0 && meta > 0 {
		// Scale fractional value to the correct size
		// meta=1-2: 1 byte, meta=3-4: 2 bytes, meta=5-6: 3 bytes
		switch fracBytes {
		case 1: // meta 1-2: store 2 digits
			buf[3] = byte(frac)
		case 2: // meta 3-4: store 4 digits
			buf[3] = byte(frac >> 8)
			buf[4] = byte(frac)
		case 3: // meta 5-6: store 6 digits
			buf[3] = byte(frac >> 16)
			buf[4] = byte(frac >> 8)
			buf[5] = byte(frac)
		}
	}

	return buf
}

func (enc *BinlogEncoder) encodeDatetime(val interface{}) []byte {
	// DATETIME is stored as 8 bytes: YYYYMMDDHHMMSS as decimal
	var s string
	switch t := val.(type) {
	case string:
		s = t
	case time.Time:
		s = t.Format("2006-01-02 15:04:05")
	default:
		return make([]byte, 8)
	}

	var year, month, day, hour, minute, second int
	fmt.Sscanf(s, "%d-%d-%d %d:%d:%d", &year, &month, &day, &hour, &minute, &second)

	d := int64(year*10000 + month*100 + day)
	t := int64(hour*10000 + minute*100 + second)
	i64 := uint64(d*1000000 + t)

	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, i64)
	return buf
}

func (enc *BinlogEncoder) encodeDatetime2(val interface{}, meta uint16) []byte {
	// DATETIME2 format with fractional seconds
	n := int(5 + (meta+1)/2)
	buf := make([]byte, n)

	var t time.Time
	switch v := val.(type) {
	case time.Time:
		t = v
	case string:
		t, _ = time.Parse("2006-01-02 15:04:05", v)
	default:
		return buf
	}

	year := t.Year()
	month := int(t.Month())
	day := t.Day()
	hour := t.Hour()
	minute := t.Minute()
	second := t.Second()

	// Pack into DATETIME2 format
	ym := int64(year*13 + month)
	ymd := ym<<5 | int64(day)
	hms := int64(hour)<<12 | int64(minute)<<6 | int64(second)
	ymdhms := ymd<<17 | hms

	intPart := ymdhms + 0x8000000000 // DATETIMEF_INT_OFS

	// 5 bytes big endian
	buf[0] = byte(intPart >> 32)
	buf[1] = byte(intPart >> 24)
	buf[2] = byte(intPart >> 16)
	buf[3] = byte(intPart >> 8)
	buf[4] = byte(intPart)

	return buf
}

func (enc *BinlogEncoder) encodeTimestamp(val interface{}) []byte {
	var ts int64
	switch t := val.(type) {
	case time.Time:
		ts = t.Unix()
	case string:
		if t == "0000-00-00 00:00:00" {
			ts = 0
		} else {
			parsed, _ := time.Parse("2006-01-02 15:04:05", t)
			ts = parsed.Unix()
		}
	case int64:
		ts = t
	}
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(ts))
	return buf
}

func (enc *BinlogEncoder) encodeTimestamp2(val interface{}, meta uint16) []byte {
	n := int(4 + (meta+1)/2)
	buf := make([]byte, n)

	var t time.Time
	switch v := val.(type) {
	case time.Time:
		t = v
	case string:
		if v == "0000-00-00 00:00:00" {
			return buf
		}
		t, _ = time.Parse("2006-01-02 15:04:05", v)
	}

	// Seconds as big endian uint32
	sec := uint32(t.Unix())
	buf[0] = byte(sec >> 24)
	buf[1] = byte(sec >> 16)
	buf[2] = byte(sec >> 8)
	buf[3] = byte(sec)

	return buf
}

// ============================================================================
// String type encoders
// ============================================================================

// encodeVarString encodes VARCHAR/VAR_STRING
// Reference: decodeString in go-mysql
func (enc *BinlogEncoder) encodeVarString(val interface{}, meta uint16) []byte {
	var s string
	switch t := val.(type) {
	case string:
		s = t
	case []byte:
		s = string(t)
	default:
		s = fmt.Sprintf("%v", val)
	}

	length := len(s)
	maxLen := int(meta)

	if maxLen < 256 {
		// 1 byte length prefix
		buf := make([]byte, 1+length)
		buf[0] = byte(length)
		copy(buf[1:], s)
		return buf
	}
	// 2 bytes length prefix (little endian)
	buf := make([]byte, 2+length)
	binary.LittleEndian.PutUint16(buf, uint16(length))
	copy(buf[2:], s)
	return buf
}

// encodeFixedString encodes CHAR/STRING type
func (enc *BinlogEncoder) encodeFixedString(val interface{}, meta uint16) []byte {
	// Parse meta to get real length
	length := int(meta)
	if meta >= 256 {
		length = int(meta & 0xFF)
	}
	return enc.encodeVarString(val, uint16(length))
}

// encodeBlob encodes BLOB type
// Reference: decodeBlob in go-mysql
func (enc *BinlogEncoder) encodeBlob(val interface{}, meta uint16) []byte {
	var data []byte
	switch t := val.(type) {
	case []byte:
		data = t
	case string:
		data = []byte(t)
	default:
		return nil
	}

	length := len(data)

	// meta determines the number of bytes used for length prefix
	switch meta {
	case 1:
		buf := make([]byte, 1+length)
		buf[0] = byte(length)
		copy(buf[1:], data)
		return buf
	case 2:
		buf := make([]byte, 2+length)
		binary.LittleEndian.PutUint16(buf, uint16(length))
		copy(buf[2:], data)
		return buf
	case 3:
		buf := make([]byte, 3+length)
		buf[0] = byte(length)
		buf[1] = byte(length >> 8)
		buf[2] = byte(length >> 16)
		copy(buf[3:], data)
		return buf
	case 4:
		buf := make([]byte, 4+length)
		binary.LittleEndian.PutUint32(buf, uint32(length))
		copy(buf[4:], data)
		return buf
	default:
		// Default to 2 byte length
		buf := make([]byte, 2+length)
		binary.LittleEndian.PutUint16(buf, uint16(length))
		copy(buf[2:], data)
		return buf
	}
}

// ============================================================================
// Other type encoders
// ============================================================================

func (enc *BinlogEncoder) encodeBit(val interface{}, meta uint16) []byte {
	nbits := ((meta >> 8) * 8) + (meta & 0xFF)
	n := int(nbits+7) / 8

	var v int64
	switch t := val.(type) {
	case int64:
		v = t
	case int:
		v = int64(t)
	case bool:
		if t {
			v = 1
		}
	}

	buf := make([]byte, n)
	// Big endian for BIT type
	for i := n - 1; i >= 0; i-- {
		buf[i] = byte(v)
		v >>= 8
	}
	return buf
}

func (enc *BinlogEncoder) encodeEnum(val interface{}, meta uint16) []byte {
	l := meta & 0xFF
	var v int64
	switch t := val.(type) {
	case int64:
		v = t
	case int:
		v = int64(t)
	}

	switch l {
	case 1:
		return []byte{byte(v)}
	case 2:
		buf := make([]byte, 2)
		binary.LittleEndian.PutUint16(buf, uint16(v))
		return buf
	default:
		return []byte{byte(v)}
	}
}

func (enc *BinlogEncoder) encodeSet(val interface{}, meta uint16) []byte {
	n := int(meta & 0xFF)
	var v int64
	switch t := val.(type) {
	case int64:
		v = t
	case int:
		v = int64(t)
	}

	buf := make([]byte, n)
	// Little endian for SET type
	for i := 0; i < n; i++ {
		buf[i] = byte(v)
		v >>= 8
	}
	return buf
}

func (enc *BinlogEncoder) encodeJSON(val interface{}, meta uint16) []byte {
	var data []byte
	switch t := val.(type) {
	case []byte:
		data = t
	case string:
		data = []byte(t)
	default:
		data = []byte("{}")
	}

	// JSON uses meta bytes for length prefix
	length := len(data)
	buf := make([]byte, int(meta)+length)

	// Length prefix (meta bytes, little endian)
	for i := 0; i < int(meta); i++ {
		buf[i] = byte(length >> (8 * i))
	}
	copy(buf[meta:], data)
	return buf
}
