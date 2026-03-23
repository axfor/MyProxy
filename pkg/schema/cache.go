package schema

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// syncMapTyped is a type-safe wrapper around sync.Map
// It provides generic type safety and eliminates runtime type assertions
type syncMapTyped[K comparable, V any] struct {
	m sync.Map
}

// Load returns the value stored in the map for a key, or zero value if not present
func (s *syncMapTyped[K, V]) Load(key K) (V, bool) {
	v, ok := s.m.Load(key)
	if !ok {
		var zero V
		return zero, false
	}
	return v.(V), true
}

// Store sets the value for a key
func (s *syncMapTyped[K, V]) Store(key K, value V) {
	s.m.Store(key, value)
}

// Delete deletes the value for a key
func (s *syncMapTyped[K, V]) Delete(key K) {
	s.m.Delete(key)
}

// Range calls f sequentially for each key and value present in the map
func (s *syncMapTyped[K, V]) Range(f func(key K, value V) bool) {
	s.m.Range(func(k, v any) bool {
		return f(k.(K), v.(V))
	})
}

// ColumnInfo contains detailed information about a table column
type ColumnInfo struct {
	Name         string // Column name
	OrdinalPos   int    // Column position (1-based)
	DataType     string // PostgreSQL data type
	MySQLType    uint8  // MySQL type constant (e.g., MYSQL_TYPE_LONG)
	MySQLMeta    uint16 // MySQL type metadata (e.g., varchar length)
	IsNullable   bool   // Whether column allows NULL
	IsPrimaryKey bool   // Whether column is part of primary key
	IsAutoIncr   bool   // Whether column is auto-increment (SERIAL)
	IsUnsigned   bool   // Whether numeric type is unsigned
	MaxLength    int    // Maximum length for string types
	NumPrecision int    // Numeric precision
	NumScale     int    // Numeric scale
}

// TableInfo contains schema information for a table
type TableInfo struct {
	TableName      string
	Schema         string       // Schema name (e.g., "public")
	AutoIncrColumn string       // Empty string if no auto-increment column
	Columns        []ColumnInfo // Full column information
	PrimaryKey     []string     // Primary key column names
	TableID        uint64       // Unique table ID for binlog
	LastRefreshed  time.Time    // When this info was last queried
	TTL            time.Duration
}

// Cache is a global schema cache shared across all sessions
type Cache struct {
	tables      *syncMapTyped[string, *TableInfo] // Type-safe map[string]*TableInfo
	ttl         time.Duration
	mu          sync.RWMutex
	nextTableID uint64 // Counter for generating unique table IDs
}

var (
	// GlobalCache is the singleton schema cache instance
	GlobalCache *Cache
	once        sync.Once
)

// InitGlobalCache initializes the global schema cache
func InitGlobalCache(ttl time.Duration) *Cache {
	once.Do(func() {
		GlobalCache = &Cache{
			tables: &syncMapTyped[string, *TableInfo]{},
			ttl:    ttl,
		}
	})
	return GlobalCache
}

// GetGlobalCache returns the global schema cache instance
func GetGlobalCache() *Cache {
	if GlobalCache == nil {
		return InitGlobalCache(5 * time.Minute) // Default 5 minutes TTL
	}
	return GlobalCache
}

type autoIncrementQueryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// GetAutoIncrementColumn returns the AUTO_INCREMENT column name for a table
// It uses cached data if available and not expired, otherwise queries PostgreSQL
// The cache key format is "schema.table" to support multiple schemas
func (c *Cache) GetAutoIncrementColumn(conn *pgx.Conn, schemaName, tableName string) string {
	cacheKey := buildTableCacheKey(schemaName, tableName)

	// Try to get from cache (no type assertion needed with generics!)
	if tableInfo, ok := c.tables.Load(cacheKey); ok {
		// Check if cache is still valid
		if time.Since(tableInfo.LastRefreshed) < tableInfo.TTL {
			return tableInfo.AutoIncrColumn
		}
	}

	// Cache miss or expired, query from PostgreSQL
	columnName := c.queryAutoIncrementColumn(conn, schemaName, tableName)

	// Update cache
	c.tables.Store(cacheKey, &TableInfo{
		TableName:      tableName,
		Schema:         schemaName,
		AutoIncrColumn: columnName,
		LastRefreshed:  time.Now(),
		TTL:            c.ttl,
	})

	return columnName
}

// queryAutoIncrementColumn queries PostgreSQL system tables to find auto-increment column
func (c *Cache) queryAutoIncrementColumn(conn *pgx.Conn, schemaName, tableName string) string {
	if conn == nil {
		return ""
	}

	return c.queryAutoIncrementColumnWithQueryer(conn, schemaName, tableName)
}

func (c *Cache) queryAutoIncrementColumnWithQueryer(queryer autoIncrementQueryer, schemaName, tableName string) string {
	if queryer == nil {
		return ""
	}

	ctx := context.Background()

	// Query PostgreSQL information_schema to find SERIAL or IDENTITY columns
	// SERIAL columns have column_default like 'nextval(...)'
	// IDENTITY columns have is_identity = 'YES'
	query := `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = $1
		  AND table_name = $2
		  AND (
		      column_default LIKE 'nextval(%'
		      OR is_identity = 'YES'
		  )
		ORDER BY ordinal_position
		LIMIT 1
	`

	var columnName string
	err := queryer.QueryRow(ctx, query, schemaName, strings.ToLower(tableName)).Scan(&columnName)
	if err != nil {
		// No auto-increment column found or query failed
		return ""
	}

	return columnName
}

// InvalidateTable removes a table from the cache
// This should be called when a DDL statement modifies the table
// The key format is "schema.table"
func (c *Cache) InvalidateTable(schemaName, tableName string) {
	cacheKey := buildTableCacheKey(schemaName, tableName)
	c.tables.Delete(cacheKey)
}

// InvalidateAll clears the entire cache
func (c *Cache) InvalidateAll() {
	c.tables.Range(func(key string, value *TableInfo) bool {
		c.tables.Delete(key)
		return true
	})
}

// RefreshTable forces a refresh of table schema information
func (c *Cache) RefreshTable(conn *pgx.Conn, schemaName, tableName string) string {
	// Force refresh by invalidating first
	c.InvalidateTable(schemaName, tableName)
	// Then query and cache
	return c.GetAutoIncrementColumn(conn, schemaName, tableName)
}

// GetCacheStats returns statistics about the cache
func (c *Cache) GetCacheStats() map[string]interface{} {
	count := 0
	c.tables.Range(func(key string, value *TableInfo) bool {
		count++
		return true
	})

	return map[string]interface{}{
		"cached_tables": count,
		"ttl_seconds":   c.ttl.Seconds(),
	}
}

// SetTTL updates the TTL for new cache entries
func (c *Cache) SetTTL(ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl = ttl
}

// StartPeriodicRefresh starts a background goroutine that periodically refreshes expired entries
// This is optional and can be used to proactively refresh popular tables
func (c *Cache) StartPeriodicRefresh(interval time.Duration, conn *pgx.Conn) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			c.refreshExpiredEntries(conn)
		}
	}()
}

// refreshExpiredEntries refreshes all expired cache entries
func (c *Cache) refreshExpiredEntries(conn *pgx.Conn) {
	now := time.Now()
	var expiredKeys []string

	// Collect expired tables
	c.tables.Range(func(cacheKey string, info *TableInfo) bool {
		if now.Sub(info.LastRefreshed) >= info.TTL {
			expiredKeys = append(expiredKeys, cacheKey)
		}
		return true
	})

	// Refresh expired tables
	// Note: We can't refresh without knowing the database context
	// So we just invalidate expired entries and let them refresh on next access
	for _, cacheKey := range expiredKeys {
		c.tables.Delete(cacheKey)
	}
}

// MySQL type constants (from go-mysql-org/go-mysql/mysql/const.go)
const (
	MYSQL_TYPE_DECIMAL byte = iota
	MYSQL_TYPE_TINY
	MYSQL_TYPE_SHORT
	MYSQL_TYPE_LONG
	MYSQL_TYPE_FLOAT
	MYSQL_TYPE_DOUBLE
	MYSQL_TYPE_NULL
	MYSQL_TYPE_TIMESTAMP
	MYSQL_TYPE_LONGLONG
	MYSQL_TYPE_INT24
	MYSQL_TYPE_DATE
	MYSQL_TYPE_TIME
	MYSQL_TYPE_DATETIME
	MYSQL_TYPE_YEAR
	MYSQL_TYPE_NEWDATE
	MYSQL_TYPE_VARCHAR
	MYSQL_TYPE_BIT
)

const (
	MYSQL_TYPE_JSON byte = iota + 0xf5
	MYSQL_TYPE_NEWDECIMAL
	MYSQL_TYPE_ENUM
	MYSQL_TYPE_SET
	MYSQL_TYPE_TINY_BLOB
	MYSQL_TYPE_MEDIUM_BLOB
	MYSQL_TYPE_LONG_BLOB
	MYSQL_TYPE_BLOB
	MYSQL_TYPE_VAR_STRING
	MYSQL_TYPE_STRING
	MYSQL_TYPE_GEOMETRY
)

// GetTableInfo returns full table information including all columns
// It uses cached data if available and not expired, otherwise queries PostgreSQL
func (c *Cache) GetTableInfo(conn *pgx.Conn, schemaName, tableName string) (*TableInfo, error) {
	cacheKey := schemaName + "." + tableName

	// Try to get from cache
	if tableInfo, ok := c.tables.Load(cacheKey); ok {
		if time.Since(tableInfo.LastRefreshed) < tableInfo.TTL {
			return tableInfo, nil
		}
	}

	// Cache miss or expired, query from PostgreSQL
	tableInfo, err := c.queryTableInfo(conn, schemaName, tableName)
	if err != nil {
		return nil, err
	}

	// Update cache
	c.tables.Store(cacheKey, tableInfo)

	return tableInfo, nil
}

// queryTableInfo queries PostgreSQL to get full table information
func (c *Cache) queryTableInfo(conn *pgx.Conn, schemaName, tableName string) (*TableInfo, error) {
	if conn == nil {
		return nil, nil
	}

	ctx := context.Background()

	// Generate unique table ID
	c.mu.Lock()
	c.nextTableID++
	tableID := c.nextTableID
	c.mu.Unlock()

	tableInfo := &TableInfo{
		TableName:     tableName,
		Schema:        schemaName,
		TableID:       tableID,
		Columns:       []ColumnInfo{},
		PrimaryKey:    []string{},
		LastRefreshed: time.Now(),
		TTL:           c.ttl,
	}

	// Query column information
	columnQuery := `
		SELECT
			c.column_name,
			c.ordinal_position,
			c.data_type,
			c.is_nullable,
			COALESCE(c.character_maximum_length, 0),
			COALESCE(c.numeric_precision, 0),
			COALESCE(c.numeric_scale, 0),
			CASE WHEN c.column_default LIKE 'nextval(%' OR c.is_identity = 'YES' THEN true ELSE false END as is_auto_incr
		FROM information_schema.columns c
		WHERE c.table_schema = $1
		  AND c.table_name = $2
		ORDER BY c.ordinal_position
	`

	rows, err := conn.Query(ctx, columnQuery, schemaName, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var col ColumnInfo
		var isNullable string
		err := rows.Scan(
			&col.Name,
			&col.OrdinalPos,
			&col.DataType,
			&isNullable,
			&col.MaxLength,
			&col.NumPrecision,
			&col.NumScale,
			&col.IsAutoIncr,
		)
		if err != nil {
			return nil, err
		}

		col.IsNullable = isNullable == "YES"
		col.MySQLType, col.MySQLMeta = pgTypeToMySQL(col.DataType, col.MaxLength)

		if col.IsAutoIncr {
			tableInfo.AutoIncrColumn = col.Name
		}

		tableInfo.Columns = append(tableInfo.Columns, col)
	}

	// Query primary key columns
	pkQuery := `
		SELECT kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON tc.constraint_name = kcu.constraint_name
			AND tc.table_schema = kcu.table_schema
		WHERE tc.constraint_type = 'PRIMARY KEY'
		  AND tc.table_schema = $1
		  AND tc.table_name = $2
		ORDER BY kcu.ordinal_position
	`

	pkRows, err := conn.Query(ctx, pkQuery, schemaName, tableName)
	if err == nil {
		defer pkRows.Close()
		for pkRows.Next() {
			var pkColName string
			if err := pkRows.Scan(&pkColName); err == nil {
				tableInfo.PrimaryKey = append(tableInfo.PrimaryKey, pkColName)
				// Mark column as primary key
				for i := range tableInfo.Columns {
					if tableInfo.Columns[i].Name == pkColName {
						tableInfo.Columns[i].IsPrimaryKey = true
						break
					}
				}
			}
		}
	}

	return tableInfo, nil
}

// pgTypeToMySQL converts PostgreSQL data type to MySQL type constant and metadata
func pgTypeToMySQL(pgType string, maxLength int) (uint8, uint16) {
	switch pgType {
	case "smallint":
		return MYSQL_TYPE_SHORT, 0
	case "integer":
		return MYSQL_TYPE_LONG, 0
	case "bigint":
		return MYSQL_TYPE_LONGLONG, 0
	case "real":
		return MYSQL_TYPE_FLOAT, 4
	case "double precision":
		return MYSQL_TYPE_DOUBLE, 8
	case "numeric", "decimal":
		return MYSQL_TYPE_NEWDECIMAL, (10 << 8) | 2 // default precision 10, scale 2
	case "boolean":
		return MYSQL_TYPE_TINY, 0
	case "character varying", "varchar":
		if maxLength == 0 {
			maxLength = 255
		}
		return MYSQL_TYPE_VARCHAR, uint16(maxLength)
	case "character", "char":
		if maxLength == 0 {
			maxLength = 1
		}
		return MYSQL_TYPE_STRING, uint16(maxLength)
	case "text":
		return MYSQL_TYPE_BLOB, 2 // TEXT uses 2 byte length prefix
	case "bytea":
		return MYSQL_TYPE_BLOB, 2
	case "date":
		return MYSQL_TYPE_DATE, 0
	case "time", "time without time zone", "time with time zone":
		return MYSQL_TYPE_TIME, 0
	case "timestamp", "timestamp without time zone", "timestamp with time zone":
		return MYSQL_TYPE_TIMESTAMP, 0
	case "json", "jsonb":
		return MYSQL_TYPE_JSON, 4
	case "uuid":
		return MYSQL_TYPE_STRING, 36
	default:
		return MYSQL_TYPE_VARCHAR, 255
	}
}

func buildTableCacheKey(schemaName, tableName string) string {
	return schemaName + "." + tableName
}
