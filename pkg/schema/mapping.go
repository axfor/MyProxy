package schema

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5/pgconn"
)

var schemaNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type MappingConfig struct {
	DefaultSchema    string
	FallbackToPublic bool
	Rules            map[string]string
}

type Resolver struct {
	config MappingConfig
}

type SearchPathExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func NewResolver(config MappingConfig) *Resolver {
	if config.Rules == nil {
		config.Rules = map[string]string{}
	}

	return &Resolver{config: config}
}

func (r *Resolver) ResolveSchema(mysqlDB string) (string, error) {
	schemaName := mysqlDB

	if mysqlDB == "" {
		schemaName = r.config.DefaultSchema
	} else if mappedSchema, ok := r.config.Rules[mysqlDB]; ok {
		schemaName = mappedSchema
	}

	if err := ValidateSchemaName(schemaName); err != nil {
		return "", err
	}

	return schemaName, nil
}

func (r *Resolver) BuildSearchPathSQL(schemaName string) (string, error) {
	return BuildSearchPathSQL(schemaName, r.config.FallbackToPublic)
}

func ValidateSchemaName(schemaName string) error {
	if !schemaNamePattern.MatchString(schemaName) {
		return fmt.Errorf("invalid schema name: %s", schemaName)
	}

	return nil
}

func BuildSearchPathSQL(schemaName string, fallbackToPublic bool) (string, error) {
	if err := ValidateSchemaName(schemaName); err != nil {
		return "", err
	}

	if fallbackToPublic {
		return fmt.Sprintf("SET search_path TO %s, public", schemaName), nil
	}

	return fmt.Sprintf("SET search_path TO %s", schemaName), nil
}

func ApplySchema(ctx context.Context, conn SearchPathExecutor, schemaName string, fallbackToPublic bool) error {
	resolver := NewResolver(MappingConfig{
		FallbackToPublic: fallbackToPublic,
	})

	sql, err := resolver.BuildSearchPathSQL(schemaName)
	if err != nil {
		return err
	}

	_, err = conn.Exec(ctx, sql)
	return err
}
