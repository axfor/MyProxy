package config

import (
	"fmt"
	"os"
	"time"

	"MyProxy/pkg/mapper"
	schemamapping "MyProxy/pkg/schema"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server          ServerConfig          `yaml:"server"`
	Postgres        PostgresConfig        `yaml:"postgres"`
	DatabaseMapping DatabaseMappingConfig `yaml:"database_mapping"`
	Auth            AuthConfig            `yaml:"auth"`
	Security        SecurityConfig        `yaml:"security"`
	SQLRewrite      SQLRewriteConfig      `yaml:"sql_rewrite"`
	Observability   ObservabilityConfig   `yaml:"observability"`
	SchemaCache     SchemaCacheConfig     `yaml:"schema_cache"`
	Binlog          BinlogConfig          `yaml:"binlog"`
}

type ServerConfig struct {
	Host           string        `yaml:"host"`
	Port           int           `yaml:"port"`
	MaxConnections int           `yaml:"max_connections"`
	MaxPacketSize  int64         `yaml:"max_packet_size"`
	ReadTimeout    time.Duration `yaml:"read_timeout"`
	WriteTimeout   time.Duration `yaml:"write_timeout"`
}

type PostgresConfig struct {
	Host           string `yaml:"host"`
	Port           int    `yaml:"port"`
	Database       string `yaml:"database"`
	User           string `yaml:"user"`
	Password       string `yaml:"password"`
	MaxPoolSize    int    `yaml:"max_pool_size"`
	ConnectionMode string `yaml:"connection_mode"`
	SSLMode        string `yaml:"ssl_mode"`
}

type DatabaseMappingConfig struct {
	DefaultSchema    string            `yaml:"default_schema"`
	FallbackToPublic bool              `yaml:"fallback_to_public"`
	ExposeMode       string            `yaml:"expose_mode"`
	ExposedDatabases []string          `yaml:"exposed_databases"`
	Rules            map[string]string `yaml:"rules"`
}

func (c DatabaseMappingConfig) ToSchemaMappingConfig() schemamapping.MappingConfig {
	rules := make(map[string]string, len(c.Rules))
	for mysqlDB, schemaName := range c.Rules {
		rules[mysqlDB] = schemaName
	}

	return schemamapping.MappingConfig{
		DefaultSchema:    c.DefaultSchema,
		FallbackToPublic: c.FallbackToPublic,
		Rules:            rules,
	}
}

func (c DatabaseMappingConfig) ToDatabaseExposureConfig() mapper.DatabaseExposureConfig {
	rules := make(map[string]string, len(c.Rules))
	for mysqlDB, schemaName := range c.Rules {
		rules[mysqlDB] = schemaName
	}

	exposed := append([]string(nil), c.ExposedDatabases...)

	return mapper.DatabaseExposureConfig{
		ExposeMode:       c.ExposeMode,
		ExposedDatabases: exposed,
		Rules:            rules,
	}
}

type AuthConfig struct {
	Mode         string   `yaml:"mode"`
	AllowedUsers []string `yaml:"allowed_users"`
}

type SecurityConfig struct {
	RateLimitPerSecond         int      `yaml:"rate_limit_per_second"`
	MaxConnectionsPerIP        int      `yaml:"max_connections_per_ip"`
	EnableTLS                  bool     `yaml:"enable_tls"`
	TLSCert                    string   `yaml:"tls_cert"`
	TLSKey                     string   `yaml:"tls_key"`
	DangerousCommandsBlacklist []string `yaml:"dangerous_commands_blacklist"`
}

type SQLRewriteConfig struct {
	Enabled     bool   `yaml:"enabled"`
	CustomRules string `yaml:"custom_rules"`
	DebugSQL    bool   `yaml:"debug_sql"` // Enable SQL rewrite debugging (prints original and rewritten SQL)
}

type ObservabilityConfig struct {
	MetricsPort      int    `yaml:"metrics_port"`
	LogLevel         string `yaml:"log_level"`
	LogFormat        string `yaml:"log_format"`
	EnableQueryLog   bool   `yaml:"enable_query_log"`
	RedactParameters bool   `yaml:"redact_parameters"`
	EnableTracing    bool   `yaml:"enable_tracing"`
	TracingEndpoint  string `yaml:"tracing_endpoint"`
}

type SchemaCacheConfig struct {
	Enabled         bool          `yaml:"enabled"`
	TTL             time.Duration `yaml:"ttl"`
	MaxEntries      int           `yaml:"max_entries"`
	InvalidateOnDDL bool          `yaml:"invalidate_on_ddl"`
}

type BinlogConfig struct {
	Enabled       bool          `yaml:"enabled"`
	Dir           string        `yaml:"dir"`
	MaxFileSize   int64         `yaml:"max_file_size"`
	MaxFiles      int           `yaml:"max_files"`
	SyncMode      string        `yaml:"sync_mode"` // async, sync, fsync
	Format        string        `yaml:"format"`    // json, binary
	FlushInterval time.Duration `yaml:"flush_interval"`
	BufferSize    int           `yaml:"buffer_size"`
	LogDDL        bool          `yaml:"log_ddl"`
	LogDML        bool          `yaml:"log_dml"`
	LogSelect     bool          `yaml:"log_select"`
}

func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host:           "0.0.0.0",
			Port:           3306,
			MaxConnections: 1000,
			MaxPacketSize:  16777216,
			ReadTimeout:    30 * time.Second,
			WriteTimeout:   30 * time.Second,
		},
		Postgres: PostgresConfig{
			Host:           "localhost",
			Port:           5432,
			Database:       "postgres",
			User:           "postgres",
			Password:       "",
			MaxPoolSize:    100,
			ConnectionMode: "session_affinity",
			SSLMode:        "prefer",
		},
		DatabaseMapping: DatabaseMappingConfig{
			DefaultSchema:    "public",
			FallbackToPublic: false,
			ExposeMode:       "explicit",
			ExposedDatabases: []string{},
			Rules:            map[string]string{},
		},
		Auth: AuthConfig{
			Mode:         "pass_through",
			AllowedUsers: []string{},
		},
		Security: SecurityConfig{
			RateLimitPerSecond:  1000,
			MaxConnectionsPerIP: 10,
			EnableTLS:           false,
			TLSCert:             "",
			TLSKey:              "",
			DangerousCommandsBlacklist: []string{
				"COM_BINLOG_DUMP",
				"FLUSH PRIVILEGES",
			},
		},
		SQLRewrite: SQLRewriteConfig{
			Enabled:     true,
			CustomRules: "",
			DebugSQL:    false,
		},
		Observability: ObservabilityConfig{
			MetricsPort:      9090,
			LogLevel:         "info",
			LogFormat:        "json",
			EnableQueryLog:   false,
			RedactParameters: true,
			EnableTracing:    false,
			TracingEndpoint:  "localhost:4318",
		},
		SchemaCache: SchemaCacheConfig{
			Enabled:         true,
			TTL:             5 * time.Minute,
			MaxEntries:      10000,
			InvalidateOnDDL: true,
		},
		Binlog: BinlogConfig{
			Enabled:       true,
			Dir:           "./data/binlog",
			MaxFileSize:   100 * 1024 * 1024, // 100MB
			MaxFiles:      10,
			SyncMode:      "async",
			Format:        "json",
			FlushInterval: 1 * time.Second,
			BufferSize:    64 * 1024, // 64KB
			LogDDL:        true,
			LogDML:        true,
			LogSelect:     false,
		},
	}
}

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if cfg.DatabaseMapping.Rules == nil {
		cfg.DatabaseMapping.Rules = map[string]string{}
	}

	if cfg.DatabaseMapping.ExposeMode == "" {
		cfg.DatabaseMapping.ExposeMode = "explicit"
	}

	if cfg.DatabaseMapping.ExposedDatabases == nil {
		cfg.DatabaseMapping.ExposedDatabases = []string{}
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", c.Server.Port)
	}

	if c.Server.MaxConnections < 1 {
		return fmt.Errorf("max_connections must be at least 1")
	}

	if c.Server.MaxPacketSize < 1024 {
		return fmt.Errorf("max_packet_size must be at least 1024 bytes")
	}

	if c.Postgres.Host == "" {
		return fmt.Errorf("postgres host is required")
	}

	if c.Postgres.Port < 1 || c.Postgres.Port > 65535 {
		return fmt.Errorf("invalid postgres port: %d", c.Postgres.Port)
	}

	if c.Postgres.MaxPoolSize < 1 {
		return fmt.Errorf("postgres max_pool_size must be at least 1")
	}

	if c.DatabaseMapping.DefaultSchema == "" {
		return fmt.Errorf("database_mapping default_schema is required")
	}

	if c.DatabaseMapping.ExposeMode != "" &&
		c.DatabaseMapping.ExposeMode != mapper.ExposeModeExplicit &&
		c.DatabaseMapping.ExposeMode != mapper.ExposeModeAllAccessibleSchemas {
		return fmt.Errorf("invalid database_mapping expose_mode: %s", c.DatabaseMapping.ExposeMode)
	}

	if c.Auth.Mode != "pass_through" && c.Auth.Mode != "proxy_auth" {
		return fmt.Errorf("invalid auth mode: %s (must be 'pass_through' or 'proxy_auth')", c.Auth.Mode)
	}

	if c.Security.EnableTLS {
		if c.Security.TLSCert == "" || c.Security.TLSKey == "" {
			return fmt.Errorf("tls_cert and tls_key are required when enable_tls is true")
		}
	}

	return nil
}
