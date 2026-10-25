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

// Fixed names under the data root (gateway-side table, 2026-09-19;
// 2026-10-24 存储形态收口：全部 *.sqlite3 专库文件名与 usage-shards /
// codex-context/state-shards 分片子目录随 sqlite 存储形态删除，保留的只有
// PG+Redis 形态仍消费的普通文件目录面).
const (
	CodexContextRoot = "codex-context"
	// UsageRecordSpoolDirectory 是 gateway↔jobs 用量记录 spool 交接目录
	// （D1，PLAN-20261024T051925945Z）：JUHE_AI_USAGE_SPOOL_DIRECTORY 未配置
	// 时组合根派生 <DATA_DIR>/usage-record-spool（显式配置优先；旧名
	// JUHE_AI_USAGE_SPOOL_DIR 回落语义保留在组合根）。
	UsageRecordSpoolDirectory = "usage-record-spool"
	AuditBlobDirectory        = "audit-blob"
	ChatAssetsDirectory       = "chat-assets"
	// RuntimeLogsDirectory is the 2026-09-28 default-on derivation for
	// JUHE_AI_LOG_DIR: an unset env derives <DATA_DIR>/logs (symmetric with
	// the jobs runtimelog config derivation); the gateway file sink and the
	// jobs F1 indexer/cleanup then share one directory in a zero-config
	// deployment. The literal "disabled" stays a composition-root concern
	// (explicit opt-out), not a fixed name.
	RuntimeLogsDirectory = "logs"
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
