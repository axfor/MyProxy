package session

import "testing"

func TestNewSessionInitializesCurrentSchema(t *testing.T) {
	sess := NewSession("alice", "app_db", "127.0.0.1")

	if sess.CurrentSchema != "app_db" {
		t.Fatalf("expected CurrentSchema %q, got %q", "app_db", sess.CurrentSchema)
	}

	if sess.Database != "app_db" {
		t.Fatalf("expected Database %q, got %q", "app_db", sess.Database)
	}
}

func TestSetCurrentSchemaUpdatesCompatibilityDatabase(t *testing.T) {
	sess := NewSession("alice", "app_db", "127.0.0.1")

	sess.SetCurrentSchema("tenant_schema")

	if sess.CurrentSchema != "tenant_schema" {
		t.Fatalf("expected CurrentSchema %q, got %q", "tenant_schema", sess.CurrentSchema)
	}

	if sess.Database != "tenant_schema" {
		t.Fatalf("expected Database %q, got %q", "tenant_schema", sess.Database)
	}
}

func TestSchemaStateRetainsBackendDatabase(t *testing.T) {
	sess := NewSession("alice", "app_db", "127.0.0.1")

	sess.SetBackendDatabase("postgres_main")
	sess.SetCurrentSchema("tenant_schema")

	if sess.BackendDatabase != "postgres_main" {
		t.Fatalf("expected BackendDatabase %q, got %q", "postgres_main", sess.BackendDatabase)
	}

	if sess.CurrentSchema != "tenant_schema" {
		t.Fatalf("expected CurrentSchema %q, got %q", "tenant_schema", sess.CurrentSchema)
	}
}
