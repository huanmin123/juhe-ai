package modelcheckruntime

import "testing"

func TestLoadConfigDisabledDoesNotRequireSecrets(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil || cfg.Enabled {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadConfigRejectsEnabledSQLiteInJobs(t *testing.T) {
	values := map[string]string{"JUHE_AI_MODEL_CHECK_ENABLED": "true", "JUHE_AI_MODEL_CHECK_JOBS_OWNER": "go", "JUHE_AI_MODEL_CHECK_INSTANCE_ID": "j3b", "JUHE_AI_MODEL_CHECK_STORE": "sqlite", "JUHE_AI_MODEL_CHECK_JOBS_DATABASE_PATH": "jobs.db", "JUHE_AI_MODEL_CHECK_DATASET_DATABASE_PATH": "dataset.db", "JUHE_AI_MODEL_CHECK_BUSINESS_DATABASE_PATH": "business.db", "JUHE_AI_MODEL_CHECK_CREDENTIAL_SECRET": "credential", "JUHE_AI_MODEL_CHECK_IDENTITY_SECRET": "identity", "JUHE_AI_MODEL_CHECK_MANAGEMENT_SECRET": "01234567890123456789012345678901"}
	_, err := LoadConfig(func(key string) string { return values[key] })
	if err == nil {
		t.Fatal("jobs must reject enabled SQLite J3b because gateway is the sole runtime owner")
	}
}

func TestLoadConfigRejectsEnabledPostgresInJobs(t *testing.T) {
	values := map[string]string{"JUHE_AI_MODEL_CHECK_ENABLED": "true", "JUHE_AI_MODEL_CHECK_JOBS_OWNER": "go", "JUHE_AI_MODEL_CHECK_INSTANCE_ID": "j3b", "JUHE_AI_MODEL_CHECK_STORE": "postgres", "JUHE_AI_MODEL_CHECK_POSTGRES_URL": "postgres://jobs", "JUHE_AI_MODEL_CHECK_BUSINESS_POSTGRES_URL": "postgres://business", "JUHE_AI_MODEL_CHECK_CREDENTIAL_SECRET": "credential", "JUHE_AI_MODEL_CHECK_IDENTITY_SECRET": "identity"}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("jobs must reject enabled PostgreSQL J3b because gateway is the sole runtime owner")
	}
}
