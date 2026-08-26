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

func TestRuntimeConfigUsesHighPerformanceConcurrencyAndPoolDefaults(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_ENABLED":            "true",
		"JUHE_AI_ACCOUNT_BALANCE_OWNER_ID":           "j2-test",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER":         "go",
		"JUHE_AI_ACCOUNT_BALANCE_STORE":              "postgres",
		"JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL":       "postgres://j2-store",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL": "postgres://input",
		"JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET":  "secret",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET":   "0123456789abcdef0123456789abcdef",
	}
	cfg, err := LoadRuntimeConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrency != defaultAccountBalanceConcurrency || cfg.BatchSize != defaultAccountBalanceBatchSize || cfg.RecoveryBatchSize != defaultAccountBalanceRecoveryBatchSize || cfg.PostgresMaxOpenConns != 5096 || cfg.PostgresMaxIdleConns != 5096 || cfg.InputPostgresMaxOpenConns != 5096 || cfg.InputPostgresMaxIdleConns != 5096 {
		t.Fatalf("unexpected high-performance defaults: %#v", cfg)
	}
}

func TestRuntimeConfigAcceptsExternalPoolAndConcurrency(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_ENABLED":                       "true",
		"JUHE_AI_ACCOUNT_BALANCE_OWNER_ID":                      "j2-test",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER":                    "go",
		"JUHE_AI_ACCOUNT_BALANCE_STORE":                         "postgres",
		"JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL":                  "postgres://j2-store",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL":            "postgres://input",
		"JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET":             "secret",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET":              "0123456789abcdef0123456789abcdef",
		"JUHE_AI_ACCOUNT_BALANCE_MAX_CONCURRENCY":               "64",
		"JUHE_AI_ACCOUNT_BALANCE_POSTGRES_MAX_OPEN_CONNS":       "1200",
		"JUHE_AI_ACCOUNT_BALANCE_POSTGRES_MAX_IDLE_CONNS":       "1100",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_MAX_OPEN_CONNS": "900",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_MAX_IDLE_CONNS": "800",
	}
	cfg, err := LoadRuntimeConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrency != 64 || cfg.PostgresMaxOpenConns != 1200 || cfg.PostgresMaxIdleConns != 1100 || cfg.InputPostgresMaxOpenConns != 900 || cfg.InputPostgresMaxIdleConns != 800 {
		t.Fatalf("external pool configuration not applied: %#v", cfg)
	}
}

func TestRuntimeConfigAcceptsLargeGoConcurrencyAndBatch(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_ENABLED": "true", "JUHE_AI_ACCOUNT_BALANCE_OWNER_ID": "j2-test",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER": "go", "JUHE_AI_ACCOUNT_BALANCE_STORE": "postgres", "JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL": "postgres://j2-store",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL": "postgres://input", "JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET": "secret", "JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET": "0123456789abcdef0123456789abcdef",
		"JUHE_AI_ACCOUNT_BALANCE_MAX_CONCURRENCY": "5096", "JUHE_AI_ACCOUNT_BALANCE_BATCH_SIZE": "5096", "JUHE_AI_ACCOUNT_BALANCE_RECOVERY_BATCH_SIZE": "5095",
		"JUHE_AI_ACCOUNT_BALANCE_OWNER_LEASE": "20m", "JUHE_AI_ACCOUNT_BALANCE_CYCLE_BUDGET": "15m", "JUHE_AI_ACCOUNT_BALANCE_PROBE_TIMEOUT": "15s",
	}
	cfg, err := LoadRuntimeConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrency != maxAccountBalanceWorkItems || cfg.BatchSize != maxAccountBalanceWorkItems || cfg.RecoveryBatchSize != maxAccountBalanceWorkItems-1 {
		t.Fatalf("large Go capacity configuration not applied: %#v", cfg)
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
