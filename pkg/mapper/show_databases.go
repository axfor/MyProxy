package mapper

import (
	"context"
	"fmt"
	"sort"
	"strings"

	schemamapping "MyProxy/pkg/schema"
	"github.com/jackc/pgx/v5"
)

const (
	ExposeModeExplicit             = "explicit"
	ExposeModeAllAccessibleSchemas = "all-accessible-schemas"
)

type DatabaseExposureConfig struct {
	ExposeMode       string
	ExposedDatabases []string
	Rules            map[string]string
}

type SchemaUsageChecker interface {
	HasSchemaUsage(ctx context.Context, schemaName string) (bool, error)
}

type pgSchemaUsageChecker struct {
	conn *pgx.Conn
}

func (c pgSchemaUsageChecker) HasSchemaUsage(ctx context.Context, schemaName string) (bool, error) {
	var hasUsage bool
	err := c.conn.QueryRow(ctx, "SELECT has_schema_privilege(current_user, $1, 'USAGE')", schemaName).Scan(&hasUsage)
	return hasUsage, err
}

func ShowDatabasesCandidates(config DatabaseExposureConfig) []string {
	var candidates []string

	if len(config.ExposedDatabases) > 0 {
		candidates = append(candidates, config.ExposedDatabases...)
	} else {
		for mysqlDB := range config.Rules {
			candidates = append(candidates, mysqlDB)
		}
	}

	if len(candidates) == 0 {
		return []string{}
	}

	seen := make(map[string]struct{}, len(candidates))
	unique := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		unique = append(unique, candidate)
	}

	sort.Strings(unique)
	return unique
}

func ResolveAccessibleDatabases(ctx context.Context, exposureConfig DatabaseExposureConfig, mappingConfig schemamapping.MappingConfig, checker SchemaUsageChecker) ([]string, error) {
	candidates := ShowDatabasesCandidates(exposureConfig)
	if len(candidates) == 0 {
		return []string{}, nil
	}

	resolver := schemamapping.NewResolver(mappingConfig)
	accessible := make([]string, 0, len(candidates))
	for _, databaseName := range candidates {
		schemaName, err := resolver.ResolveSchema(databaseName)
		if err != nil {
			return nil, err
		}

		hasUsage, err := checker.HasSchemaUsage(ctx, schemaName)
		if err != nil {
			return nil, err
		}

		if hasUsage {
			accessible = append(accessible, databaseName)
		}
	}

	return accessible, nil
}

func BuildShowDatabasesResultSQL(databases []string) string {
	if len(databases) == 0 {
		return `SELECT CAST(NULL AS TEXT) AS "Database" WHERE FALSE`
	}

	parts := make([]string, 0, len(databases))
	for _, databaseName := range databases {
		escaped := strings.ReplaceAll(databaseName, "'", "''")
		parts = append(parts, fmt.Sprintf("SELECT '%s' AS \"Database\"", escaped))
	}

	return strings.Join(parts, " UNION ALL ")
}
