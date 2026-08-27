package modelcheckruntime

import "testing"

func TestLoadConfigDisabledDoesNotRequireSecrets(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil || cfg.Enabled {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadConfigRequiresExplicitGoOwnerAndSeparateSQLitePaths(t *testing.T) {
	values := map[string]string{"JUHE_AI_MODEL_CHECK_ENABLED": "true", "JUHE_AI_MODEL_CHECK_JOBS_OWNER": "go", "JUHE_AI_MODEL_CHECK_INSTANCE_ID": "j3b", "JUHE_AI_MODEL_CHECK_STORE": "sqlite", "JUHE_AI_MODEL_CHECK_JOBS_DATABASE_PATH": "jobs.db", "JUHE_AI_MODEL_CHECK_DATASET_DATABASE_PATH": "dataset.db", "JUHE_AI_MODEL_CHECK_BUSINESS_DATABASE_PATH": "business.db", "JUHE_AI_MODEL_CHECK_CREDENTIAL_SECRET": "credential", "JUHE_AI_MODEL_CHECK_IDENTITY_SECRET": "identity", "JUHE_AI_MODEL_CHECK_MANAGEMENT_SECRET": "01234567890123456789012345678901"}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil || !cfg.Enabled || cfg.ManagementAddress != "127.0.0.1:3308" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}
