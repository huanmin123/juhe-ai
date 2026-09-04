package retention

import (
	"fmt"
	"strconv"
	"strings"
)

// Config carries the runtime inputs the Node retention family reads from
// runtimeConfig plus the per-domain enable switches that keep every domain
// an independently disableable owner inside the jobs process.
type Config struct {
	// Mode mirrors JUHE_AI_DATABASE_DRIVER (sqlite default, postgres for the
	// performance runtime).
	Mode Mode
	// ProcessRole mirrors JUHE_AI_PROCESS_ROLE (Node default server). The
	// retention jobs only act when it is "worker".
	ProcessRole string
	// WorkerRole mirrors JUHE_AI_WORKER_ROLE (Node default worker). The
	// data-retention stages additionally require "ingest-worker".
	WorkerRole string
	// ChatRetentionDays mirrors JUHE_AI_CHAT_RETENTION_DAYS (default 3,
	// range 1..365).
	ChatRetentionDays int
	// CodexContextRoot mirrors JUHE_AI_CODEX_CONTEXT_ROOT; required for the
	// codex-context storage cleanup.
	CodexContextRoot string

	// Per-domain switches: an independently disableable owner per domain.
	DataEnabled              bool
	ChatEnabled              bool
	ExpiredAccountEnabled    bool
	RecordMaintenanceEnabled bool
	APIKeyRetryEnabled       bool
	AccountRetryEnabled      bool

	// Now/Sleep inject the clock and the batch pause in tests.
	Now   Clock
	Sleep Sleeper
}

// LoadConfig mirrors the Node runtime config defaults for the retention
// family and applies the jobs-side enable switches
// (JUHE_AI_JOBS_RETENTION_<DOMAIN>_ENABLED, default true).
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Mode:                     ModeSQLite,
		ProcessRole:              "server",
		WorkerRole:               "worker",
		ChatRetentionDays:        chatRetentionDefaultDays,
		DataEnabled:              true,
		ChatEnabled:              true,
		ExpiredAccountEnabled:    true,
		RecordMaintenanceEnabled: true,
		APIKeyRetryEnabled:       true,
		AccountRetryEnabled:      true,
	}
	if value := strings.TrimSpace(getenv("JUHE_AI_DATABASE_DRIVER")); value != "" {
		mode := Mode(strings.ToLower(value))
		if mode != ModeSQLite && mode != ModePostgres {
			return Config{}, fmt.Errorf("JUHE_AI_DATABASE_DRIVER 必须为 sqlite 或 postgres")
		}
		cfg.Mode = mode
	}
	if value := strings.TrimSpace(getenv("JUHE_AI_PROCESS_ROLE")); value != "" {
		cfg.ProcessRole = value
	}
	if value := strings.TrimSpace(getenv("JUHE_AI_WORKER_ROLE")); value != "" {
		cfg.WorkerRole = value
	}
	if value := strings.TrimSpace(getenv("JUHE_AI_CHAT_RETENTION_DAYS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > chatRetentionMaxDays {
			return Config{}, fmt.Errorf("JUHE_AI_CHAT_RETENTION_DAYS 必须在 1 到 %d 之间的整数", chatRetentionMaxDays)
		}
		cfg.ChatRetentionDays = parsed
	}
	cfg.CodexContextRoot = strings.TrimSpace(getenv("JUHE_AI_CODEX_CONTEXT_ROOT"))
	for _, toggle := range []struct {
		env    string
		target *bool
	}{
		{"JUHE_AI_JOBS_RETENTION_DATA_ENABLED", &cfg.DataEnabled},
		{"JUHE_AI_JOBS_RETENTION_CHAT_ENABLED", &cfg.ChatEnabled},
		{"JUHE_AI_JOBS_RETENTION_EXPIRED_ACCOUNT_ENABLED", &cfg.ExpiredAccountEnabled},
		{"JUHE_AI_JOBS_RETENTION_RECORD_MAINTENANCE_ENABLED", &cfg.RecordMaintenanceEnabled},
		{"JUHE_AI_JOBS_RETENTION_API_KEY_RETRY_ENABLED", &cfg.APIKeyRetryEnabled},
		{"JUHE_AI_JOBS_RETENTION_ACCOUNT_RETRY_ENABLED", &cfg.AccountRetryEnabled},
	} {
		if value := strings.TrimSpace(getenv(toggle.env)); value != "" {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return Config{}, fmt.Errorf("%s 必须是布尔值", toggle.env)
			}
			*toggle.target = parsed
		}
	}
	return cfg, nil
}
