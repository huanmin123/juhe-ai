package retention

import (
	"testing"
	"time"
)

// TestRegistryEntriesMirrorNodeRegistry freezes the Node registry metadata
// for the retention family (background-jobs.ts wiring + entries.ts).
func TestRegistryEntriesMirrorNodeRegistry(t *testing.T) {
	entries := RegistryEntries()
	want := map[string]struct {
		role     string
		interval time.Duration
		lease    time.Duration
	}{
		"data-retention-cleanup":          {"ingest-worker", 10 * time.Minute, 10 * time.Minute},
		"chat-retention-cleanup":          {"ops-worker", 10 * time.Minute, 5 * time.Minute},
		"expired-deleted-account-cleanup": {"ops-worker", 24 * time.Hour, 10 * time.Minute},
		"api-key-record-cleanup-retry":    {"ingest-worker", time.Minute, 2 * time.Minute},
		"account-record-cleanup-retry":    {"ingest-worker", time.Minute, 2 * time.Minute},
	}
	if len(entries) != len(want) {
		t.Fatalf("registry size = %d, want %d", len(entries), len(want))
	}
	for _, entry := range entries {
		expected, ok := want[entry.JobName]
		if !ok {
			t.Fatalf("unexpected registry entry %q", entry.JobName)
		}
		if entry.DefaultRole != expected.role {
			t.Fatalf("%s role = %q, want %q", entry.JobName, entry.DefaultRole, expected.role)
		}
		if entry.Interval != expected.interval {
			t.Fatalf("%s interval = %v, want %v", entry.JobName, entry.Interval, expected.interval)
		}
		if entry.LeaseTTL != expected.lease {
			t.Fatalf("%s leaseTTL = %v, want %v", entry.JobName, entry.LeaseTTL, expected.lease)
		}
		if entry.Category != "scheduled" || entry.Kind != "maintenance" {
			t.Fatalf("%s category/kind = %q/%q", entry.JobName, entry.Category, entry.Kind)
		}
	}
}

func TestScheduleConstantsMirrorNodeWiring(t *testing.T) {
	checks := map[string][2]time.Duration{
		"data initial delay":      {DataRetentionScheduleInitialDelay, 450 * time.Second},
		"data schedule timeout":   {DataRetentionScheduleTimeout, 5 * time.Minute},
		"chat initial delay":      {ChatRetentionScheduleInitialDelay, 270 * time.Second},
		"chat scheduler timeout":  {ChatRetentionSchedulerTimeout, 2 * time.Minute},
		"chat db service timeout": {ChatRetentionDbServiceTimeout, 60 * time.Second},
		"expired account delay":   {ExpiredAccountScheduleInitialDelay, 14 * time.Minute},
		"chat retention interval": {ChatRetentionInterval, 10 * time.Minute},
	}
	for name, check := range checks {
		if check[0] != check[1] {
			t.Fatalf("%s = %v, want %v", name, check[0], check[1])
		}
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadConfig() unexpected error: %v", err)
	}
	if cfg.Mode != ModeSQLite {
		t.Fatalf("mode = %q, want sqlite default", cfg.Mode)
	}
	if cfg.ProcessRole != "server" || cfg.WorkerRole != "worker" {
		t.Fatalf("roles = %q/%q, want server/worker defaults", cfg.ProcessRole, cfg.WorkerRole)
	}
	if cfg.ChatRetentionDays != chatRetentionDefaultDays {
		t.Fatalf("chat retention days = %d, want 3", cfg.ChatRetentionDays)
	}
	if !cfg.DataEnabled || !cfg.ChatEnabled || !cfg.ExpiredAccountEnabled || !cfg.RecordMaintenanceEnabled || !cfg.APIKeyRetryEnabled || !cfg.AccountRetryEnabled {
		t.Fatal("domains must default to enabled")
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	environment := map[string]string{
		"JUHE_AI_DATABASE_DRIVER":                           "postgres",
		"JUHE_AI_PROCESS_ROLE":                              "worker",
		"JUHE_AI_WORKER_ROLE":                               "ingest-worker",
		"JUHE_AI_CHAT_RETENTION_DAYS":                       "30",
		"JUHE_AI_CODEX_CONTEXT_ROOT":                        "/tmp/codex",
		"JUHE_AI_JOBS_RETENTION_DATA_ENABLED":               "false",
		"JUHE_AI_JOBS_RETENTION_RECORD_MAINTENANCE_ENABLED": "0",
	}
	cfg, err := LoadConfig(func(name string) string { return environment[name] })
	if err != nil {
		t.Fatalf("LoadConfig() unexpected error: %v", err)
	}
	if cfg.Mode != ModePostgres || cfg.ProcessRole != "worker" || cfg.WorkerRole != "ingest-worker" {
		t.Fatalf("config not applied: %+v", cfg)
	}
	if cfg.ChatRetentionDays != 30 || cfg.CodexContextRoot != "/tmp/codex" {
		t.Fatalf("chat config not applied: %+v", cfg)
	}
	if cfg.DataEnabled || cfg.RecordMaintenanceEnabled {
		t.Fatal("disable toggles not applied")
	}
	if !cfg.ChatEnabled || !cfg.ExpiredAccountEnabled || !cfg.APIKeyRetryEnabled || !cfg.AccountRetryEnabled {
		t.Fatal("untouched toggles must stay enabled")
	}
}

func TestLoadConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "invalid driver",
			env:     map[string]string{"JUHE_AI_DATABASE_DRIVER": "oracle"},
			wantErr: "JUHE_AI_DATABASE_DRIVER 必须为 sqlite 或 postgres",
		},
		{
			name:    "chat retention below range",
			env:     map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "0"},
			wantErr: "JUHE_AI_CHAT_RETENTION_DAYS 必须在 1 到 365 之间的整数",
		},
		{
			name:    "chat retention above range",
			env:     map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "366"},
			wantErr: "JUHE_AI_CHAT_RETENTION_DAYS 必须在 1 到 365 之间的整数",
		},
		{
			name:    "non-boolean toggle",
			env:     map[string]string{"JUHE_AI_JOBS_RETENTION_CHAT_ENABLED": "maybe"},
			wantErr: "JUHE_AI_JOBS_RETENTION_CHAT_ENABLED 必须是布尔值",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(func(name string) string { return tt.env[name] })
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
