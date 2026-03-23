package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigDatabaseMapping(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.DatabaseMapping.DefaultSchema != "public" {
		t.Fatalf("expected default schema %q, got %q", "public", cfg.DatabaseMapping.DefaultSchema)
	}

	if cfg.DatabaseMapping.FallbackToPublic {
		t.Fatal("expected fallback_to_public to default to false")
	}
}

func TestLoadConfigDatabaseMappingRules(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
database_mapping:
  default_schema: app_default
  fallback_to_public: false
  rules:
    app: app_schema
    analytics: reporting
`

	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.DatabaseMapping.DefaultSchema != "app_default" {
		t.Fatalf("expected loaded default schema %q, got %q", "app_default", cfg.DatabaseMapping.DefaultSchema)
	}

	if cfg.DatabaseMapping.FallbackToPublic {
		t.Fatal("expected loaded fallback_to_public to remain false")
	}

	expectedRules := map[string]string{
		"app":       "app_schema",
		"analytics": "reporting",
	}

	if len(cfg.DatabaseMapping.Rules) != len(expectedRules) {
		t.Fatalf("expected %d rules, got %d", len(expectedRules), len(cfg.DatabaseMapping.Rules))
	}

	for mysqlDB, schema := range expectedRules {
		if got := cfg.DatabaseMapping.Rules[mysqlDB]; got != schema {
			t.Fatalf("expected rule %q -> %q, got %q", mysqlDB, schema, got)
		}
	}
}

func TestDatabaseExposureDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.DatabaseMapping.ExposeMode != "explicit" {
		t.Fatalf("expected expose_mode %q, got %q", "explicit", cfg.DatabaseMapping.ExposeMode)
	}

	if len(cfg.DatabaseMapping.ExposedDatabases) != 0 {
		t.Fatalf("expected no exposed_databases by default, got %#v", cfg.DatabaseMapping.ExposedDatabases)
	}
}

func TestDatabaseExposureConfigParsing(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
database_mapping:
  default_schema: app_default
  fallback_to_public: false
  expose_mode: explicit
  exposed_databases:
    - app
    - analytics
  rules:
    app: app_schema
`

	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.DatabaseMapping.ExposeMode != "explicit" {
		t.Fatalf("expected expose_mode %q, got %q", "explicit", cfg.DatabaseMapping.ExposeMode)
	}

	expected := []string{"app", "analytics"}
	if len(cfg.DatabaseMapping.ExposedDatabases) != len(expected) {
		t.Fatalf("expected %d exposed_databases, got %d", len(expected), len(cfg.DatabaseMapping.ExposedDatabases))
	}

	for i, name := range expected {
		if cfg.DatabaseMapping.ExposedDatabases[i] != name {
			t.Fatalf("expected exposed_databases[%d] = %q, got %q", i, name, cfg.DatabaseMapping.ExposedDatabases[i])
		}
	}
}

func TestDefaultConfigFallbackToPublicUsesStrictMappingConfig(t *testing.T) {
	cfg := DefaultConfig()

	mappingCfg := cfg.DatabaseMapping.ToSchemaMappingConfig()

	if mappingCfg.DefaultSchema != "public" {
		t.Fatalf("expected default schema %q, got %q", "public", mappingCfg.DefaultSchema)
	}

	if mappingCfg.FallbackToPublic {
		t.Fatal("expected fallback_to_public to remain disabled by default in schema mapping config")
	}

	if len(mappingCfg.Rules) != 0 {
		t.Fatalf("expected no mapping rules by default, got %#v", mappingCfg.Rules)
	}
}

func TestLoadConfigFallbackToPublicEnabledInMappingConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
database_mapping:
  default_schema: app_default
  fallback_to_public: true
  rules:
    app: app_schema
`

	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	mappingCfg := cfg.DatabaseMapping.ToSchemaMappingConfig()
	if !mappingCfg.FallbackToPublic {
		t.Fatal("expected fallback_to_public to remain explicitly enabled in schema mapping config")
	}
}
