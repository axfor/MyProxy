package schema

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSearchPathExecutor struct {
	calls   int
	lastSQL string
	err     error
}

func (f *fakeSearchPathExecutor) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.calls++
	f.lastSQL = sql
	return pgconn.CommandTag{}, f.err
}

func TestResolveSchemaUsesExplicitRule(t *testing.T) {
	resolver := NewResolver(MappingConfig{
		DefaultSchema: "public",
		Rules: map[string]string{
			"app": "app_schema",
		},
	})

	resolved, err := resolver.ResolveSchema("app")
	require.NoError(t, err)
	assert.Equal(t, "app_schema", resolved)
}

func TestResolveSchemaFallsBackToDatabaseName(t *testing.T) {
	resolver := NewResolver(MappingConfig{
		DefaultSchema: "public",
		Rules:         map[string]string{},
	})

	resolved, err := resolver.ResolveSchema("analytics")
	require.NoError(t, err)
	assert.Equal(t, "analytics", resolved)
}

func TestResolveSchemaUsesDefaultSchemaForEmptyDatabase(t *testing.T) {
	resolver := NewResolver(MappingConfig{
		DefaultSchema: "public",
	})

	resolved, err := resolver.ResolveSchema("")
	require.NoError(t, err)
	assert.Equal(t, "public", resolved)
}

func TestResolveSchemaRejectsInvalidSchemaName(t *testing.T) {
	resolver := NewResolver(MappingConfig{
		DefaultSchema: "public",
		Rules: map[string]string{
			"app": "bad-schema",
		},
	})

	_, err := resolver.ResolveSchema("app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid schema name")
}

func TestValidateSchemaName(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		wantErr bool
	}{
		{name: "valid underscore", schema: "app_schema", wantErr: false},
		{name: "empty name", schema: "", wantErr: true},
		{name: "starts with digit", schema: "1schema", wantErr: true},
		{name: "contains dash", schema: "bad-schema", wantErr: true},
		{name: "contains dot", schema: "bad.schema", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSchemaName(tt.schema)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestBuildSearchPathSQLStrictMode(t *testing.T) {
	sql, err := BuildSearchPathSQL("app_schema", false)
	require.NoError(t, err)
	assert.Equal(t, "SET search_path TO app_schema", sql)
}

func TestBuildSearchPathSQLFallbackMode(t *testing.T) {
	sql, err := BuildSearchPathSQL("app_schema", true)
	require.NoError(t, err)
	assert.Equal(t, "SET search_path TO app_schema, public", sql)
}

func TestApplySchemaUsesBuildSearchPathSQL(t *testing.T) {
	executor := &fakeSearchPathExecutor{}

	expectedSQL, err := BuildSearchPathSQL("app_schema", true)
	require.NoError(t, err)

	err = ApplySchema(context.Background(), executor, "app_schema", true)
	require.NoError(t, err)
	assert.Equal(t, 1, executor.calls)
	assert.Equal(t, expectedSQL, executor.lastSQL)
}

func TestApplySchemaRejectsInvalidSchemaBeforeExecution(t *testing.T) {
	executor := &fakeSearchPathExecutor{}

	err := ApplySchema(context.Background(), executor, "bad-schema", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid schema name")
	assert.Equal(t, 0, executor.calls)
}

func TestApplySchemaStrictModeUsesOnlyTargetSchema(t *testing.T) {
	executor := &fakeSearchPathExecutor{}

	err := ApplySchema(context.Background(), executor, "app_schema", false)
	require.NoError(t, err)
	assert.Equal(t, 1, executor.calls)
	assert.Equal(t, "SET search_path TO app_schema", executor.lastSQL)
	assert.NotContains(t, executor.lastSQL, ", public")
}

func TestApplySchemaPropagatesExecutionError(t *testing.T) {
	executor := &fakeSearchPathExecutor{err: errors.New("exec failed")}

	err := ApplySchema(context.Background(), executor, "app_schema", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exec failed")
	assert.Equal(t, 1, executor.calls)
}

func TestResolverFallbackToPublicStrictByDefault(t *testing.T) {
	resolver := NewResolver(MappingConfig{
		DefaultSchema: "public",
	})

	sql, err := resolver.BuildSearchPathSQL("app_schema")
	require.NoError(t, err)
	assert.Equal(t, "SET search_path TO app_schema", sql)
	assert.NotContains(t, sql, ", public")
}

func TestResolverFallbackToPublicEnabledExplicitly(t *testing.T) {
	resolver := NewResolver(MappingConfig{
		DefaultSchema:    "public",
		FallbackToPublic: true,
	})

	sql, err := resolver.BuildSearchPathSQL("app_schema")
	require.NoError(t, err)
	assert.Equal(t, "SET search_path TO app_schema, public", sql)
}
