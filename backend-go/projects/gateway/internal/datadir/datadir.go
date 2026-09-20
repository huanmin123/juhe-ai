// Package datadir implements the 2026-09-19 open-source zero-config storage
// convention for the Go gateway: a single data directory root
// (JUHE_AI_DATA_DIR, default ./data relative to the process cwd) backs every
// path-class env var. An unconfigured path env derives <DATA_DIR>/<fixed
// name>; an explicitly configured value always wins (显式优先). The
// derivation helpers live inside the gateway module by design — the shared
// platform module stays untouched.
//
// The fixed-name table is shared by the composition root (runtime.go), the
// F3 audit-log config and the F4 operation-log config so every consumer
// derives identical paths in the same empty-env deployment.
package datadir

import (
	"os"
	"path/filepath"
	"strings"
)

// EnvDataDir is the data-root override env; DefaultDirName is the
// cwd-relative fallback root used when it is unset or blank.
const (
	EnvDataDir     = "JUHE_AI_DATA_DIR"
	DefaultDirName = "data"
)

// Fixed names under the data root (gateway-side table, 2026-09-19).
const (
	BusinessDatabase           = "business.sqlite3"
	ChatDatabase               = "chat.sqlite3"
	DatasetDatabase            = "dataset.sqlite3"
	RuntimeLogDatabase         = "runtime-log.sqlite3"
	UsageCatalogDatabase       = "usage-catalog.sqlite3"
	StatsDatabase              = "stats.sqlite3"
	TableMonitorDatabase       = "table-monitor.sqlite3"
	UsageShardRoot             = "usage-shards"
	CodexContextRoot           = "codex-context"
	CodexContextStateShardRoot = "codex-context/state-shards"
	AuditLogDatabase           = "audit-log.sqlite3"
	AuditBlobDirectory         = "audit-blob"
	OperationLogDatabase       = "operation-log.sqlite3"
	ChatAssetsDirectory        = "chat-assets"
	// ModelCheckDatabase is the J3b model-check owner's dedicated store
	// (2026-09-20 zero-config arm). It must stay distinct from
	// BusinessDatabase: the J3b owner contract requires a separate file.
	ModelCheckDatabase = "model-check.sqlite3"
)

// Dir resolves the data root: JUHE_AI_DATA_DIR when configured (trimmed),
// else the cwd-relative "data" directory. The returned value is not
// absolutized: relative roots keep resolving against the process cwd exactly
// like the previous explicit-path deployments.
func Dir(getenv func(string) string) string {
	if value := strings.TrimSpace(getenv(EnvDataDir)); value != "" {
		return value
	}
	return DefaultDirName
}

// Path returns the trimmed explicit env value when configured (显式优先),
// else the derived <dir>/<fixedName> default.
func Path(getenv func(string) string, dir, envName, fixedName string) string {
	if value := strings.TrimSpace(getenv(envName)); value != "" {
		return value
	}
	return filepath.Join(dir, fixedName)
}

// DefaultInstanceID returns the stable per-host instance id default shared by
// the F3/F4 owner leases: os.Hostname(), degrading to "juhe-ai-gateway" when
// the hostname is unavailable or blank.
func DefaultInstanceID() string {
	hostname, err := os.Hostname()
	hostname = strings.TrimSpace(hostname)
	if err != nil || hostname == "" {
		return "juhe-ai-gateway"
	}
	return hostname
}
