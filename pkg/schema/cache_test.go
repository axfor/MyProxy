package schema

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
)

type fakeAutoIncrementRow struct {
	columnName string
	err        error
}

func (r fakeAutoIncrementRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}

	*(dest[0].(*string)) = r.columnName
	return nil
}

type fakeAutoIncrementQueryer struct {
	lastSQL  string
	lastArgs []any
	row      pgx.Row
}

func (q *fakeAutoIncrementQueryer) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	q.lastSQL = sql
	q.lastArgs = append([]any(nil), args...)
	return q.row
}

func TestCacheSchemaKeyUsesSchemaTableFormat(t *testing.T) {
	assert.Equal(t, "tenant_a.orders", buildTableCacheKey("tenant_a", "orders"))
}

func TestCacheSchemaInvalidationUsesSchemaScopedKey(t *testing.T) {
	cache := &Cache{
		tables: &syncMapTyped[string, *TableInfo]{},
		ttl:    time.Minute,
	}

	cache.tables.Store(buildTableCacheKey("tenant_a", "orders"), &TableInfo{
		TableName:      "orders",
		Schema:         "tenant_a",
		AutoIncrColumn: "id",
		LastRefreshed:  time.Now(),
		TTL:            time.Minute,
	})
	cache.tables.Store(buildTableCacheKey("tenant_b", "orders"), &TableInfo{
		TableName:      "orders",
		Schema:         "tenant_b",
		AutoIncrColumn: "id",
		LastRefreshed:  time.Now(),
		TTL:            time.Minute,
	})

	cache.InvalidateTable("tenant_a", "orders")

	_, tenantAExists := cache.tables.Load(buildTableCacheKey("tenant_a", "orders"))
	_, tenantBExists := cache.tables.Load(buildTableCacheKey("tenant_b", "orders"))

	assert.False(t, tenantAExists)
	assert.True(t, tenantBExists)
}

func TestQueryAutoIncrementColumnUsesExplicitSchemaArgument(t *testing.T) {
	cache := &Cache{}
	queryer := &fakeAutoIncrementQueryer{
		row: fakeAutoIncrementRow{columnName: "id"},
	}

	columnName := cache.queryAutoIncrementColumnWithQueryer(queryer, "tenant_a", "orders")

	assert.Equal(t, "id", columnName)
	assert.NotContains(t, queryer.lastSQL, "current_schema()")
	assert.Equal(t, []any{"tenant_a", "orders"}, queryer.lastArgs)
}
