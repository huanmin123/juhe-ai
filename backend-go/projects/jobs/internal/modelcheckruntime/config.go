package modelcheckruntime

import (
	"errors"
	"os"
	"strings"
	"time"
)

// RuntimeConfig keeps the historical config shape: jobs only reads Enabled and
// fails closed for every enabled configuration (solution A makes Gateway the
// sole J3b runtime owner).
type RuntimeConfig struct {
	Enabled              bool
	InstanceID           string
	StoreMode            string
	JobsDatabasePath     string
	DatasetDatabasePath  string
	BusinessDatabasePath string
	JobsPostgresURL      string
	BusinessPostgresURL  string
	CredentialSecret     string
	IdentitySecret       string
	ManagementAddress    string
	ProbeSetVersion      string
	Deadline             time.Duration
	Heartbeat            time.Duration
	RetryAttempts        int
}

func LoadConfig(getenv func(string) string) (RuntimeConfig, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := RuntimeConfig{Enabled: strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_ENABLED")), "true")}
	if !cfg.Enabled {
		return cfg, nil
	}
	// Solution A makes Gateway the sole J3b runtime owner. Keep the jobs
	// binary unconditionally fail-closed for every enabled configuration so a
	// stale PostgreSQL/SQLite environment cannot resurrect a second owner.
	return RuntimeConfig{}, errors.New("J3b 不允许在 juhe-ai-jobs 中启用：方案 A 的唯一运行时 owner 是 juhe-ai-gateway")
}
