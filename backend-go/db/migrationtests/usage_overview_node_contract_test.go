package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func readUsageOverviewContractFile(t *testing.T, path string) string {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.ReplaceAll(string(source), "\r\n", "\n")
}

func TestNodeUsageOverviewWindowSchemaContract(t *testing.T) {
	const schemaPath = "../../../backend/src/storage/schema/stats-schema.ts"
	schema := readUsageOverviewContractFile(t, schemaPath)

	for tableName, wants := range map[string][]string{
		"usage_stats_hourly": {
			"scope_type TEXT NOT NULL",
			"scope_id TEXT NOT NULL DEFAULT ''",
			"stat_hour TEXT NOT NULL",
			"duration_ms_max INTEGER NOT NULL DEFAULT 0",
			"first_token_ms_max INTEGER NOT NULL DEFAULT 0",
			"PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour)",
		},
		"usage_model_daily": {
			"stat_date TEXT NOT NULL",
			"provider_code TEXT NOT NULL DEFAULT 'unknown'",
			"model TEXT NOT NULL DEFAULT 'unknown'",
			"cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0",
			"total_cost_usd REAL NOT NULL DEFAULT 0",
			"PRIMARY KEY (system_account_id, stat_date, provider_code, model)",
		},
		"usage_error_daily": {
			"error_group TEXT NOT NULL DEFAULT 'unknown'",
			"error_code TEXT NOT NULL DEFAULT 'unknown'",
			"status_code INTEGER NOT NULL DEFAULT 0",
			"error_message TEXT",
			"PRIMARY KEY (system_account_id, stat_date, error_group, provider_code, error_code, status_code)",
		},
		"usage_overview_summary_windows": {
			"window_key TEXT NOT NULL",
			"start_date TEXT NOT NULL DEFAULT ''",
			"end_date TEXT NOT NULL DEFAULT ''",
			"duration_ms_sum INTEGER NOT NULL DEFAULT 0",
			"first_token_ms_count INTEGER NOT NULL DEFAULT 0",
			"last_used_at TEXT",
			"PRIMARY KEY (system_account_id, window_key)",
		},
		"usage_overview_trend_windows": {
			"bucket_key TEXT NOT NULL",
			"error_count INTEGER NOT NULL DEFAULT 0",
			"cache_write_cost_usd REAL NOT NULL DEFAULT 0",
			"duration_ms_count INTEGER NOT NULL DEFAULT 0",
			"PRIMARY KEY (system_account_id, window_key, bucket_key)",
		},
		"usage_model_rank_windows": {
			"rank INTEGER NOT NULL",
			"provider_code TEXT NOT NULL DEFAULT 'unknown'",
			"model TEXT NOT NULL DEFAULT 'unknown'",
			"input_image_tokens INTEGER NOT NULL DEFAULT 0",
			"PRIMARY KEY (system_account_id, window_key, rank, provider_code, model)",
		},
		"usage_error_rank_windows": {
			"rank INTEGER NOT NULL",
			"error_code TEXT NOT NULL DEFAULT 'unknown'",
			"status_code INTEGER NOT NULL DEFAULT 0",
			"error_count INTEGER NOT NULL DEFAULT 0",
			"PRIMARY KEY (system_account_id, window_key, rank, provider_code, error_code, status_code)",
		},
	} {
		definition := nodeTableDefinition(t, schema, tableName)
		for _, want := range wants {
			if !strings.Contains(definition, want) {
				t.Errorf("Node table %s missing %q", tableName, want)
			}
		}
	}
}

func nodeTableDefinition(t *testing.T, schema string, tableName string) string {
	t.Helper()
	prefix := "CREATE TABLE IF NOT EXISTS " + tableName + " ("
	start := strings.Index(schema, prefix)
	if start < 0 {
		t.Fatalf("Node stats schema does not define table %s", tableName)
	}
	rest := schema[start:]
	end := strings.Index(rest, "\n        );")
	if end < 0 {
		t.Fatalf("Node stats schema table %s has no terminator", tableName)
	}
	return rest[:end]
}

func TestNodeUsageOverviewWindowIndexContract(t *testing.T) {
	const schemaPath = "../../../backend/src/storage/schema/stats-schema.ts"
	schema := readUsageOverviewContractFile(t, schemaPath)

	for _, want := range []string{
		"idx_usage_stats_hourly_scope_hour ON usage_stats_hourly(system_account_id, scope_type, scope_id, stat_hour)",
		"idx_usage_stats_hourly_scope_stat_hour ON usage_stats_hourly(system_account_id, scope_type, stat_hour, scope_id)",
		"idx_usage_stats_hourly_hour ON usage_stats_hourly(stat_hour)",
		"idx_usage_stats_hourly_updated ON usage_stats_hourly(updated_at)",
		"idx_usage_model_daily_date ON usage_model_daily(system_account_id, stat_date, model)",
		"idx_usage_model_daily_stat_date ON usage_model_daily(stat_date)",
		"idx_usage_model_daily_updated ON usage_model_daily(updated_at)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_model_daily_account_date_provider_model ON usage_model_daily(system_account_id, stat_date, provider_code, model)",
		"idx_usage_error_daily_date ON usage_error_daily(system_account_id, stat_date, error_code)",
		"idx_usage_error_daily_stat_date ON usage_error_daily(stat_date)",
		"idx_usage_error_daily_updated ON usage_error_daily(updated_at)",
		"idx_usage_overview_summary_windows_end ON usage_overview_summary_windows(end_date)",
		"idx_usage_overview_trend_windows_lookup ON usage_overview_trend_windows(system_account_id, window_key, bucket_key)",
		"idx_usage_overview_trend_windows_end ON usage_overview_trend_windows(end_date)",
		"idx_usage_model_rank_windows_lookup ON usage_model_rank_windows(system_account_id, window_key, rank)",
		"idx_usage_model_rank_windows_end ON usage_model_rank_windows(end_date)",
		"idx_usage_error_rank_windows_lookup ON usage_error_rank_windows(system_account_id, window_key, rank)",
		"idx_usage_error_rank_windows_end ON usage_error_rank_windows(end_date)",
	} {
		if !strings.Contains(schema, want) {
			t.Errorf("Node stats schema missing index contract %q", want)
		}
	}
}

func TestNodeUsageOverviewWriterDependencyContract(t *testing.T) {
	const repositoryPath = "../../../backend/src/storage/usage-stats.repository.ts"
	repository := readUsageOverviewContractFile(t, repositoryPath)

	for _, want := range []string{
		"name: 'usage_overview_windows'",
		"sourceTables: ['usage_stats_totals', 'usage_stats_daily', 'usage_stats_hourly', 'usage_model_daily', 'usage_error_daily']",
		"run: refreshUsageOverviewWindowSnapshots",
		"runInBackground: (database, context, options) => refreshUsageOverviewWindowSnapshotsInStages",
	} {
		if !strings.Contains(repository, want) {
			t.Errorf("Node overview writer dependency contract missing %q", want)
		}
	}

	const windowsPath = "../../../backend/src/storage/usage-overview-windows.repository.ts"
	windows := readUsageOverviewContractFile(t, windowsPath)
	for _, want := range []string{
		"refreshUsageOverviewSummaryWindowSnapshotsAsync(client, context, options)",
		"refreshUsageOverviewTrendWindowSnapshotsAsync(client, context, options)",
		"refreshUsageModelRankWindowSnapshotsAsync(client, context, options)",
		"refreshUsageErrorRankWindowSnapshotsAsync(client, context, options)",
	} {
		if !strings.Contains(windows, want) {
			t.Errorf("Node overview writer stage contract missing %q", want)
		}
	}
}
