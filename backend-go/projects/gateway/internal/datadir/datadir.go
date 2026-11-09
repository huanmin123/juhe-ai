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
// codex-context/state-shards 分片子目录随 sqlite 存储形态删除；2026-11-07
// codex-context 根常量随跨协议桥退役删除，磁盘历史目录按部署文档人工清理).
// 保留的只有 PG+Redis 形态仍消费的普通文件目录面.
const (
	// 2026-11-09 T188：UsageRecordSpoolDirectory 随网关直投 PG 删除
	// （usage-record-spool 目录与 JUHE_AI_USAGE_SPOOL_* env 一并退役）。
	AuditBlobDirectory = "audit-blob"
	// RuntimeLogsDirectory is the default-on derivation for JUHE_AI_LOG_DIR:
	// an unset env derives <DATA_DIR>/logs. The gateway file sink writes
	// locally and self-prunes rotated files; the jobs F1 PG indexer was
	// retired on 2026-11-09, so no cross-process directory sharing is needed.
	// The literal "disabled" stays a composition-root concern (explicit
	// opt-out), not a fixed name.
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
