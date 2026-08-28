package modelcheckowner

import "testing"

func TestDisabledNeedsNoOwnerOrStorage(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil || cfg.Enabled {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestRejectsNonGatewayOwner(t *testing.T) {
	values := map[string]string{"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_J3B_OWNER": "jobs"}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("non-gateway J3b owner must fail closed")
	}
}

func TestRejectsSQLiteUntilHandoff(t *testing.T) {
	values := map[string]string{"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_J3B_OWNER": "gateway", "JUHE_AI_J3B_INSTANCE_ID": "gw-1", "JUHE_AI_J3B_STORE": "sqlite", "JUHE_AI_J3B_DATABASE_PATH": "j3b.db", "JUHE_AI_J3B_BUSINESS_DATABASE_PATH": "business.db", "JUHE_AI_J3B_CREDENTIAL_SECRET": "credential", "JUHE_AI_J3B_IDENTITY_SECRET": "identity"}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("SQLite must remain closed until owner handoff")
	}
}

func TestRejectsPostgresUntilRuntimeReadiness(t *testing.T) {
	values := map[string]string{"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_J3B_OWNER": "gateway", "JUHE_AI_J3B_INSTANCE_ID": "gw-1", "JUHE_AI_J3B_STORE": "postgres", "JUHE_AI_J3B_POSTGRES_URL": "postgres://j3b", "JUHE_AI_J3B_BUSINESS_POSTGRES_URL": "postgres://business", "JUHE_AI_J3B_CREDENTIAL_SECRET": "credential", "JUHE_AI_J3B_IDENTITY_SECRET": "identity"}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("Gateway J3b must remain closed until runtime readiness is wired")
	}
}

func TestRejectsConfirmedHandoffUntilNodeWriterStopped(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_J3B_ENABLED":                    "true",
		"JUHE_AI_J3B_OWNER":                      "gateway",
		"JUHE_AI_J3B_INSTANCE_ID":                "gw-1",
		"JUHE_AI_J3B_STORE":                      "postgres",
		"JUHE_AI_J3B_POSTGRES_URL":               "postgres://j3b",
		"JUHE_AI_J3B_BUSINESS_POSTGRES_URL":      "postgres://business",
		"JUHE_AI_J3B_CREDENTIAL_SECRET":          "credential",
		"JUHE_AI_J3B_IDENTITY_SECRET":            "identity",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED": "true",
	}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("confirmed Business handoff must fail closed while Node writer is active")
	}
}

func TestAcceptsOnlyWhenAllOwnerGatesAreExplicit(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_J3B_ENABLED":                    "true",
		"JUHE_AI_J3B_OWNER":                      "gateway",
		"JUHE_AI_J3B_INSTANCE_ID":                "gw-1",
		"JUHE_AI_J3B_STORE":                      "postgres",
		"JUHE_AI_J3B_POSTGRES_URL":               "postgres://j3b",
		"JUHE_AI_J3B_BUSINESS_POSTGRES_URL":      "postgres://business",
		"JUHE_AI_J3B_CREDENTIAL_SECRET":          "credential",
		"JUHE_AI_J3B_IDENTITY_SECRET":            "identity",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED": "true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED":        "true",
		"JUHE_AI_J3B_SCHEMA_READY":               "true",
		"JUHE_AI_J3B_HEALTH_BOUNDARY_READY":      "true",
		"JUHE_AI_J3B_RUNTIME_READY":              "true",
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL":          "redis://127.0.0.1:6379/9",
		"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE":    "dev",
	}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil || !cfg.Enabled || !cfg.BusinessHandoffConfirmed || !cfg.NodeWriterStopped || !cfg.SchemaReady || !cfg.HealthBoundaryReady || !cfg.RuntimeReady || cfg.CircuitRuntimeCapacity != 100000 || cfg.CircuitRuntimeRetention <= 0 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}
