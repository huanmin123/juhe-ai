package retention

import (
	"strings"
)

// Config carries the runtime inputs the Node retention family reads from
// runtimeConfig. The per-domain enable switches were removed with the
// 2026-09-19 zero-config decision: every retention domain is permanently
// enabled inside the jobs process.
type Config struct {
	// Mode 恒 ModePostgres（JUHE_AI_DATABASE_DRIVER env 已随 sqlite 形态退役）。
	Mode Mode
	// ProcessRole mirrors JUHE_AI_PROCESS_ROLE (Node default server). The
	// retention jobs only act when it is "worker".
	ProcessRole string
	// WorkerRole mirrors JUHE_AI_WORKER_ROLE (Node default worker). The
	// data-retention stages additionally require "ingest-worker".
	WorkerRole string
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
		Mode:        ModePostgres,
		ProcessRole: "server",
		WorkerRole:  "worker",
	}
	if value := strings.TrimSpace(getenv("JUHE_AI_WORKER_ROLE")); value != "" {
		cfg.WorkerRole = value
	}
	cfg.CodexContextRoot = strings.TrimSpace(getenv("JUHE_AI_CODEX_CONTEXT_ROOT"))
	return cfg, nil
}
