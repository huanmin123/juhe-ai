package accountbalance

import "testing"

func TestRuntimeConfigDisabledByDefault(t *testing.T) {
	cfg, err := LoadRuntimeConfig(func(string) string { return "" })
	if err != nil || cfg.Enabled {
		t.Fatalf("J2 must be disabled by default: %#v %v", cfg, err)
	}
}

func TestRuntimeConfigRejectsShortOwnerLease(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_ENABLED": "true", "JUHE_AI_ACCOUNT_BALANCE_OWNER_ID": "j2-test",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER": "go",
		"JUHE_AI_ACCOUNT_BALANCE_STORE":      "postgres", "JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL": "postgres://j2-store",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL": "postgres://input", "JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET": "secret", "JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET": "0123456789abcdef0123456789abcdef",
		"JUHE_AI_ACCOUNT_BALANCE_OWNER_LEASE": "20s", "JUHE_AI_ACCOUNT_BALANCE_PROBE_TIMEOUT": "15s",
	}
	if _, err := LoadRuntimeConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("J2 must reject an owner lease shorter than the worst-case batch")
	}
}

func TestRuntimeConfigRejectsImplicitNodeOwner(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_ENABLED":  "true",
		"JUHE_AI_ACCOUNT_BALANCE_OWNER_ID": "j2-test",
	}
	if _, err := LoadRuntimeConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("J2 must reject an implicit Node owner")
	}
}

func TestRuntimeConfigRejectsShortAccountLease(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_ENABLED": "true", "JUHE_AI_ACCOUNT_BALANCE_OWNER_ID": "j2-test",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER": "go",
		"JUHE_AI_ACCOUNT_BALANCE_STORE":      "postgres", "JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL": "postgres://j2-store",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL": "postgres://input", "JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET": "secret", "JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET": "0123456789abcdef0123456789abcdef",
		"JUHE_AI_ACCOUNT_BALANCE_ACCOUNT_LEASE": "1s", "JUHE_AI_ACCOUNT_BALANCE_PROBE_TIMEOUT": "15s",
	}
	if _, err := LoadRuntimeConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("J2 must reject an account lease shorter than one probe")
	}
}

func TestRuntimeConfigBoundsRecoveryBatchWithinPeriodicBatch(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_ENABLED": "true", "JUHE_AI_ACCOUNT_BALANCE_OWNER_ID": "j2-test",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER": "go",
		"JUHE_AI_ACCOUNT_BALANCE_STORE":      "postgres", "JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL": "postgres://j2-store",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL": "postgres://input", "JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET": "secret", "JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET": "0123456789abcdef0123456789abcdef",
		"JUHE_AI_ACCOUNT_BALANCE_BATCH_SIZE": "4", "JUHE_AI_ACCOUNT_BALANCE_RECOVERY_BATCH_SIZE": "5",
	}
	if _, err := LoadRuntimeConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("J2 recovery batch must not exceed the periodic batch")
	}
}

func TestRuntimeConfigRejectsOwnerLeaseShorterThanCycleBudget(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_ENABLED": "true", "JUHE_AI_ACCOUNT_BALANCE_OWNER_ID": "j2-test",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER": "go",
		"JUHE_AI_ACCOUNT_BALANCE_STORE":      "postgres", "JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL": "postgres://j2-store",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL": "postgres://input", "JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET": "secret", "JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET": "0123456789abcdef0123456789abcdef",
		"JUHE_AI_ACCOUNT_BALANCE_OWNER_LEASE": "30s", "JUHE_AI_ACCOUNT_BALANCE_CYCLE_BUDGET": "45s",
	}
	if _, err := LoadRuntimeConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("J2 owner lease must cover the complete cycle budget")
	}
}

func TestRuntimeConfigRequiresManualBridgeSecret(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_ENABLED": "true", "JUHE_AI_ACCOUNT_BALANCE_OWNER_ID": "j2-test",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER": "go",
		"JUHE_AI_ACCOUNT_BALANCE_STORE":      "postgres", "JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL": "postgres://j2-store",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL": "postgres://input", "JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET": "secret",
	}
	if _, err := LoadRuntimeConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("J2 must refuse an unauthenticated manual HTTP bridge")
	}
	values["JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET"] = "0123456789abcdef0123456789abcdef"
	if _, err := LoadRuntimeConfig(func(name string) string { return values[name] }); err != nil {
		t.Fatalf("J2 valid manual bridge secret rejected: %v", err)
	}
}

func TestRuntimeConfigRejectsSQLiteGoOwnerStore(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_ENABLED": "true", "JUHE_AI_ACCOUNT_BALANCE_OWNER_ID": "j2-test",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER": "go", "JUHE_AI_ACCOUNT_BALANCE_STORE": "sqlite",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL": "postgres://input", "JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET": "secret", "JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET": "0123456789abcdef0123456789abcdef",
	}
	if _, err := LoadRuntimeConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("J2 Go owner must reject SQLite jobs store because Node projects PG outcomes only")
	}
}
