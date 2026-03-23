package mapper

import (
	"context"
	"testing"

	schemamapping "MyProxy/pkg/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSchemaUsageChecker struct {
	access map[string]bool
	err    error
}

func (f fakeSchemaUsageChecker) HasSchemaUsage(_ context.Context, schemaName string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}

	return f.access[schemaName], nil
}

func TestShowDatabasesCandidatesFromRules(t *testing.T) {
	candidates := ShowDatabasesCandidates(DatabaseExposureConfig{
		Rules: map[string]string{
			"app":       "app_schema",
			"analytics": "analytics_schema",
		},
	})

	assert.Equal(t, []string{"analytics", "app"}, candidates)
}

func TestShowDatabasesCandidatesExplicitOverride(t *testing.T) {
	candidates := ShowDatabasesCandidates(DatabaseExposureConfig{
		Rules: map[string]string{
			"app": "app_schema",
		},
		ExposedDatabases: []string{"custom_db", "app"},
	})

	assert.Equal(t, []string{"app", "custom_db"}, candidates)
}

func TestShowDatabasesCandidatesDeduplicatesAndSorts(t *testing.T) {
	candidates := ShowDatabasesCandidates(DatabaseExposureConfig{
		ExposedDatabases: []string{"zeta", "alpha", "zeta", "beta"},
	})

	assert.Equal(t, []string{"alpha", "beta", "zeta"}, candidates)
}

func TestShowDatabasesFiltersSchemasWithoutUsagePermission(t *testing.T) {
	databases, err := ResolveAccessibleDatabases(
		context.Background(),
		DatabaseExposureConfig{
			Rules: map[string]string{
				"app":       "tenant_app",
				"analytics": "tenant_analytics",
			},
		},
		schemamapping.MappingConfig{
			DefaultSchema: "public",
			Rules: map[string]string{
				"app":       "tenant_app",
				"analytics": "tenant_analytics",
			},
		},
		fakeSchemaUsageChecker{
			access: map[string]bool{
				"tenant_app":       true,
				"tenant_analytics": false,
			},
		},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"app"}, databases)
}

func TestShowDatabasesReturnsLogicalDatabaseNames(t *testing.T) {
	databases, err := ResolveAccessibleDatabases(
		context.Background(),
		DatabaseExposureConfig{
			Rules: map[string]string{
				"app": "tenant_app",
			},
		},
		schemamapping.MappingConfig{
			DefaultSchema: "public",
			Rules: map[string]string{
				"app": "tenant_app",
			},
		},
		fakeSchemaUsageChecker{
			access: map[string]bool{
				"tenant_app": true,
			},
		},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"app"}, databases)
	assert.NotContains(t, databases, "tenant_app")
}

func TestShowDatabasesDoesNotEnumerateAllSchemas(t *testing.T) {
	databases, err := ResolveAccessibleDatabases(
		context.Background(),
		DatabaseExposureConfig{
			ExposedDatabases: []string{"app"},
			Rules: map[string]string{
				"app": "tenant_app",
			},
		},
		schemamapping.MappingConfig{
			DefaultSchema: "public",
			Rules: map[string]string{
				"app": "tenant_app",
			},
		},
		fakeSchemaUsageChecker{
			access: map[string]bool{
				"tenant_app":   true,
				"rogue_schema": true,
			},
		},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"app"}, databases)
	assert.NotContains(t, databases, "rogue_schema")
}
