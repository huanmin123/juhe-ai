// Six-database physical identity gate (D-44, BUG-0175): the Go startup
// preflight previously only asserted the auxiliary paths were non-empty,
// while the archived Node storage/database.ts assertDistinctStoragePaths
// (database.ts:331-457) additionally proved that no two storage roles share
// one physical SQLite file via canonical-path (realpath) resolution, symlink
// resolution and dev:ino file identities (plus the nlink=1 hardlink gate).
// Without it, two roles pointed at the same file would corrupt each other's
// schema/seed and the WAL.
//
// Parity notes against the archive:
//   - canonical path resolution and the pairwise canonical/identity checks
//     are ported 1:1 (shared/platform/sqlitepath provides the realpath walk);
//   - nlink=1 is enforced where the platform exposes it (POSIX stat); on
//     Windows os.Stat carries no link count, so hardlink duplicates are still
//     caught pairwise through os.SameFile (volume serial + file id), but a
//     hardlink to a file outside the gated set is not refused (deviation);
//   - the two log-side paths (runtime-log / table-monitor) stay optional as
//     in the current Go composition: they join the pairwise gate only when
//     configured, instead of becoming new hard requirements.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlitepath"
)

// sqliteStorageTarget pairs a storage role with its configured path.
type sqliteStorageTarget struct {
	role string
	path string
}

// sqliteStorageIdentity is the resolved physical identity of one target
// (Node SqliteStoragePathIdentity).
type sqliteStorageIdentity struct {
	role          string
	canonicalPath string
	// fileIdentity is "dev:ino" when the file already exists and the
	// platform exposes a stable identity; empty for a not-yet-created file.
	fileIdentity string
}

// assertDistinctSQLiteStoragePaths fails the startup when any two storage
// roles resolve to the same physical SQLite file.
func assertDistinctSQLiteStoragePaths(cfg runtimeConfig) error {
	targets := []sqliteStorageTarget{
		{role: "业务库", path: cfg.DatabasePath},
		{role: "聊天库", path: cfg.ChatDatabasePath},
		{role: "数据集目录库", path: cfg.DatasetDatabasePath},
		{role: "使用记录目录库", path: cfg.UsageCatalogDatabasePath},
		{role: "统计结果库", path: cfg.StatsDatabasePath},
		// Log-side databases join the gate only when configured (see the
		// file header for the requiredness deviation).
		{role: "运行日志索引库", path: cfg.RuntimeLogDatabasePath},
		{role: "表存储监控输出库", path: cfg.TableMonitorDatabasePath},
	}
	for shardIndex := 0; shardIndex < cfg.CodexContextShardCount; shardIndex++ {
		targets = append(targets, sqliteStorageTarget{
			role: fmt.Sprintf("Responses 桥接状态索引库分片 %d", shardIndex),
			path: codexContextShardPath(cfg, shardIndex),
		})
	}

	identities := make([]sqliteStorageIdentity, 0, len(targets))
	for _, target := range targets {
		if strings.TrimSpace(target.path) == "" {
			// The non-log roles are already required by the preflight; skip
			// empties here so the optional log roles degrade gracefully.
			continue
		}
		identity, err := sqliteStoragePathIdentity(target.role, target.path)
		if err != nil {
			return err
		}
		identities = append(identities, identity)
	}
	for left := 0; left < len(identities); left++ {
		for right := left + 1; right < len(identities); right++ {
			if duplicate, err := sqliteIdentitiesCollide(identities[left], identities[right]); err != nil {
				return err
			} else if duplicate {
				return throwDuplicateSQLiteStoragePathError(identities[left].role, identities[right].role)
			}
		}
	}
	return nil
}

func sqliteStoragePathIdentity(role, configuredPath string) (sqliteStorageIdentity, error) {
	canonical, err := sqlitepath.CanonicalPath(configuredPath)
	if err != nil {
		return sqliteStorageIdentity{}, fmt.Errorf("%s 的 SQLite 路径无法解析为物理文件：%s（%w）", role, configuredPath, err)
	}
	fileIdentity, err := existingSQLiteFileIdentity(role, configuredPath)
	if err != nil {
		return sqliteStorageIdentity{}, err
	}
	return sqliteStorageIdentity{role: role, canonicalPath: canonical, fileIdentity: fileIdentity}, nil
}

// existingSQLiteFileIdentity mirrors existingSqliteFileIdentity: a regular
// file must exist, must not be hardlinked (where the platform exposes the
// link count) and must provide a stable identity.
func existingSQLiteFileIdentity(role, configuredPath string) (string, error) {
	info, err := os.Stat(configuredPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("%s 的 SQLite 路径无法读取物理文件 identity：%s（%w）", role, configuredPath, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s 的 SQLite 路径不是常规文件：%s", role, configuredPath)
	}
	if err := requireUnlinkedSQLiteFile(role, configuredPath, info); err != nil {
		return "", err
	}
	identity, ok := sqliteFileIdentity(info)
	if !ok {
		// Windows: Node skips the inode identity on win32 too; the pairwise
		// os.SameFile comparison below keeps same-file duplicates refused.
		return "", nil
	}
	return identity, nil
}

// sqliteIdentitiesCollide reports whether two resolved identities describe
// the same physical file: equal canonical paths, equal dev:ino identities,
// or (for platforms without identity output) os.SameFile on the still
// existing files.
func sqliteIdentitiesCollide(left, right sqliteStorageIdentity) (bool, error) {
	if sqliteStoragePathKey(left.canonicalPath) == sqliteStoragePathKey(right.canonicalPath) {
		return true, nil
	}
	if left.fileIdentity != "" && right.fileIdentity != "" && left.fileIdentity == right.fileIdentity {
		return true, nil
	}
	if left.fileIdentity == "" && right.fileIdentity == "" {
		// Both paths exist (identities resolved) but the platform produced no
		// dev:ino: fall back to the os-level same-file comparison so a
		// hardlink pair with different canonical paths is still refused.
		leftInfo, leftErr := os.Stat(left.canonicalPath)
		rightInfo, rightErr := os.Stat(right.canonicalPath)
		if leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo) {
			return true, nil
		}
	}
	return false, nil
}

func sqliteStoragePathKey(path string) string {
	normalized := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(normalized)
	}
	return normalized
}

func throwDuplicateSQLiteStoragePathError(role, existingRole string) error {
	return fmt.Errorf("%s 与 %s 指向同一个 SQLite 物理文件，请分别配置 JUHE_AI_DATABASE_PATH、JUHE_AI_CHAT_DATABASE_PATH、JUHE_AI_DATASET_DATABASE_PATH、JUHE_AI_RUNTIME_LOG_DATABASE_PATH、JUHE_AI_TABLE_MONITOR_DATABASE_PATH、JUHE_AI_USAGE_CATALOG_DATABASE_PATH、JUHE_AI_STATS_DATABASE_PATH、JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 和 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT", role, existingRole)
}

// codexContextShardPath mirrors the shard layout the preflight opens.
func codexContextShardPath(cfg runtimeConfig, shardIndex int) string {
	return filepath.Join(cfg.CodexContextShardRoot, fmt.Sprintf("state-%03d.sqlite3", shardIndex))
}
