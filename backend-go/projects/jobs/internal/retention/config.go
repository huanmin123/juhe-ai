package retention

import (
	"fmt"
	"strconv"
	"strings"
)

// Config carries the runtime inputs the Node retention family reads from
// runtimeConfig. The per-domain enable switches were removed with the
// 2026-09-19 zero-config decision: every retention domain is permanently
// enabled inside the jobs process.
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

	// Now/Sleep inject the clock and the batch pause in tests.
	Now   Clock
	Sleep Sleeper
}

// LoadConfig mirrors the Node runtime config defaults for the retention
// family. The JUHE_AI_JOBS_RETENTION_<DOMAIN>_ENABLED sub-switches were
// removed on 2026-09-19 (domains permanently enabled); setting them has no
// effect anymore.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Mode:              ModeSQLite,
		ProcessRole:       "server",
		WorkerRole:        "worker",
		ChatRetentionDays: chatRetentionDefaultDays,
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
	return cfg, nil
}
