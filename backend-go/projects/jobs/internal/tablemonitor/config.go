package tablemonitor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultInterval                  = time.Minute
	defaultRunTimeout                = 45 * time.Second
	defaultOwnerLease                = 5 * time.Minute
	defaultRetentionDays             = 30
	defaultMaxTables                 = 256
	defaultMaxConcurrentSources      = 512
	defaultPerformanceSources        = 512
	defaultPostgresConcurrentSources = 512
	defaultRetentionBatchSize        = 512
	defaultRetentionMaxBatches       = 512
	defaultPostgresPoolSize          = 5096
)

func LoadConfig(getenv func(string) string) (Config, error) {
	instanceID := strings.TrimSpace(getenv("JUHE_AI_TABLE_MONITOR_INSTANCE_ID"))
	if instanceID == "" {
		return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_INSTANCE_ID 是必填配置")
	}
	configuredMode := strings.TrimSpace(firstNonEmpty(getenv("JUHE_AI_TABLE_MONITOR_STORE"), getenv("JUHE_AI_DATABASE_DRIVER")))
	if configuredMode == "" {
		return Config{}, fmt.Errorf("必须设置 JUHE_AI_TABLE_MONITOR_STORE 或 JUHE_AI_DATABASE_DRIVER")
	}
	mode := Mode(strings.ToLower(configuredMode))
	if mode != ModeSQLite && mode != ModePostgres {
		return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_STORE 必须为 sqlite 或 postgres")
	}
	interval, err := durationOrDefault("JUHE_AI_TABLE_MONITOR_INTERVAL", getenv("JUHE_AI_TABLE_MONITOR_INTERVAL"), defaultInterval)
	if err != nil {
		return Config{}, err
	}
	runTimeout, err := durationOrDefault("JUHE_AI_TABLE_MONITOR_RUN_TIMEOUT", getenv("JUHE_AI_TABLE_MONITOR_RUN_TIMEOUT"), defaultRunTimeout)
	if err != nil {
		return Config{}, err
	}
	ownerLease, err := durationOrDefault("JUHE_AI_TABLE_MONITOR_OWNER_LEASE", getenv("JUHE_AI_TABLE_MONITOR_OWNER_LEASE"), defaultOwnerLease)
	if err != nil || ownerLease < 5*time.Second {
		return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_OWNER_LEASE 必须是不少于 5s 的正 duration")
	}
	retentionDays, err := intOrDefault(getenv("JUHE_AI_TABLE_MONITOR_RETENTION_DAYS"), defaultRetentionDays, 1, 3650)
	if err != nil {
		return Config{}, err
	}
	maxTables, err := intOrDefault(getenv("JUHE_AI_TABLE_MONITOR_MAX_TABLES"), defaultMaxTables, 1, 10000)
	if err != nil {
		return Config{}, err
	}
	concurrencyDefault := defaultMaxConcurrentSources
	if mode == ModePostgres {
		concurrencyDefault = defaultPostgresConcurrentSources
	} else if strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_RUNTIME_MODE")), "performance") {
		concurrencyDefault = defaultPerformanceSources
	}
	maxConcurrentSources, err := intOrDefault(getenv("JUHE_AI_TABLE_MONITOR_MAX_CONCURRENT_SOURCES"), concurrencyDefault, 1, 5096)
	if err != nil {
		return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_MAX_CONCURRENT_SOURCES 无效: %w", err)
	}
	retentionBatchSize, err := intOrDefault(getenv("JUHE_AI_TABLE_MONITOR_RETENTION_BATCH_SIZE"), defaultRetentionBatchSize, 1, 5096)
	if err != nil {
		return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_RETENTION_BATCH_SIZE 无效: %w", err)
	}
	retentionMaxBatches, err := intOrDefault(getenv("JUHE_AI_TABLE_MONITOR_RETENTION_MAX_BATCHES"), defaultRetentionMaxBatches, 1, 5096)
	if err != nil {
		return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_RETENTION_MAX_BATCHES 无效: %w", err)
	}
	cfg := Config{
		InstanceID:           instanceID,
		OwnerLease:           ownerLease,
		Mode:                 mode,
		OutputPath:           strings.TrimSpace(getenv("JUHE_AI_TABLE_MONITOR_DATABASE_PATH")),
		BusinessPath:         strings.TrimSpace(getenv("JUHE_AI_DATABASE_PATH")),
		DatasetPath:          strings.TrimSpace(getenv("JUHE_AI_DATASET_DATABASE_PATH")),
		UsageCatalogPath:     strings.TrimSpace(getenv("JUHE_AI_USAGE_CATALOG_DATABASE_PATH")),
		StatsPath:            strings.TrimSpace(getenv("JUHE_AI_STATS_DATABASE_PATH")),
		CodexShardRoot:       strings.TrimSpace(getenv("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")),
		PostgresURL:          firstNonEmpty(getenv("JUHE_AI_TABLE_MONITOR_POSTGRES_URL"), getenv("JUHE_AI_POSTGRES_URL")),
		PostgresMaxOpenConns: 0,
		PostgresMaxIdleConns: 0,
		Interval:             interval,
		RunTimeout:           runTimeout,
		RetentionDays:        retentionDays,
		MaxTables:            maxTables,
		MaxConcurrentSources: maxConcurrentSources,
		RetentionBatchSize:   retentionBatchSize,
		RetentionMaxBatches:  retentionMaxBatches,
	}
	if cfg.PostgresMaxOpenConns, err = positiveIntOrDefault("JUHE_AI_TABLE_MONITOR_POSTGRES_MAX_OPEN_CONNS", getenv("JUHE_AI_TABLE_MONITOR_POSTGRES_MAX_OPEN_CONNS"), defaultPostgresPoolSize); err != nil {
		return Config{}, err
	}
	if cfg.PostgresMaxIdleConns, err = positiveIntOrDefault("JUHE_AI_TABLE_MONITOR_POSTGRES_MAX_IDLE_CONNS", getenv("JUHE_AI_TABLE_MONITOR_POSTGRES_MAX_IDLE_CONNS"), defaultPostgresPoolSize); err != nil {
		return Config{}, err
	}
	if mode == ModeSQLite {
		if cfg.OutputPath == "" {
			return Config{}, fmt.Errorf("sqlite 模式缺少 JUHE_AI_TABLE_MONITOR_DATABASE_PATH")
		}
		runtimeLogPath := strings.TrimSpace(getenv("JUHE_AI_RUNTIME_LOG_DATABASE_PATH"))
		if runtimeLogPath == "" {
			return Config{}, fmt.Errorf("sqlite 模式缺少 JUHE_AI_RUNTIME_LOG_DATABASE_PATH，无法验证 F1/F2 专用库隔离")
		}
		same, err := sameSQLiteFile(cfg.OutputPath, runtimeLogPath)
		if err != nil {
			return Config{}, fmt.Errorf("校验 JUHE_AI_TABLE_MONITOR_DATABASE_PATH 与 JUHE_AI_RUNTIME_LOG_DATABASE_PATH 的 SQLite 隔离失败: %w", err)
		}
		if same {
			return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_DATABASE_PATH 不得与 JUHE_AI_RUNTIME_LOG_DATABASE_PATH 共用 SQLite 文件")
		}
		for role, path := range cfg.sourcePaths() {
			if path == "" {
				return Config{}, fmt.Errorf("sqlite 模式缺少 %s 数据库路径", role)
			}
			same, err := sameSQLiteFile(cfg.OutputPath, path)
			if err != nil {
				return Config{}, fmt.Errorf("校验 JUHE_AI_TABLE_MONITOR_DATABASE_PATH 与 %s 的 SQLite 隔离失败: %w", role, err)
			}
			if same {
				return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_DATABASE_PATH 不得与 %s 共用 SQLite 文件", role)
			}
		}
		if cfg.CodexShardRoot == "" {
			return Config{}, fmt.Errorf("sqlite 模式缺少 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")
		}
		entries, err := filepath.Glob(filepath.Join(cfg.CodexShardRoot, "*.sqlite3"))
		if err != nil {
			return Config{}, fmt.Errorf("枚举 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 失败: %w", err)
		}
		shardKeys := make(map[string]string, len(entries))
		for _, entry := range entries {
			same, err := sameSQLiteFile(cfg.OutputPath, entry)
			if err != nil {
				return Config{}, fmt.Errorf("校验 JUHE_AI_TABLE_MONITOR_DATABASE_PATH 与 Codex context SQLite shard %q 的隔离失败: %w", entry, err)
			}
			if same {
				return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_DATABASE_PATH 不得与 Codex context SQLite shard 共用")
			}
			key := filepath.Base(entry)
			if prior, exists := shardKeys[key]; exists {
				same, err := sameSQLiteFile(prior, entry)
				if err != nil {
					return Config{}, fmt.Errorf("校验 Codex context SQLite shard %q 与 %q 的物理 identity 失败: %w", prior, entry, err)
				}
				if !same {
					return Config{}, fmt.Errorf("Codex context SQLite shard 文件名重复，无法形成稳定 table identity: %s", key)
				}
			}
			shardKeys[key] = entry
		}
		within, err := pathWithin(cfg.CodexShardRoot, cfg.OutputPath)
		if err != nil {
			return Config{}, fmt.Errorf("校验 JUHE_AI_TABLE_MONITOR_DATABASE_PATH 与 Codex context shard 根目录的隔离失败: %w", err)
		}
		if within {
			return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_DATABASE_PATH 不得放入 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")
		}
	} else if cfg.PostgresURL == "" {
		return Config{}, fmt.Errorf("postgres 模式缺少 JUHE_AI_TABLE_MONITOR_POSTGRES_URL 或 JUHE_AI_POSTGRES_URL")
	}
	return cfg, nil
}

func (c Config) sourcePaths() map[string]string {
	return map[string]string{
		"JUHE_AI_DATABASE_PATH":               c.BusinessPath,
		"JUHE_AI_DATASET_DATABASE_PATH":       c.DatasetPath,
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH": c.UsageCatalogPath,
		"JUHE_AI_STATS_DATABASE_PATH":         c.StatsPath,
	}
}

func durationOrDefault(name, value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s 必须是正 duration", name)
	}
	return parsed, nil
}

func intOrDefault(value string, fallback, min, max int) (int, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < min || parsed > max {
		return 0, fmt.Errorf("表监控数值必须在 %d..%d", min, max)
	}
	return parsed, nil
}

func positiveIntOrDefault(name, value string, fallback int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s 必须是正整数", name)
	}
	return parsed, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sameSQLiteFile(left, right string) (bool, error) {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false, fmt.Errorf("SQLite 路径不能为空: left=%q right=%q", left, right)
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr != nil && !errors.Is(leftErr, os.ErrNotExist) {
		return false, fmt.Errorf("stat 左侧 SQLite 路径 %q 失败: %w", left, leftErr)
	}
	if rightErr != nil && !errors.Is(rightErr, os.ErrNotExist) {
		return false, fmt.Errorf("stat 右侧 SQLite 路径 %q 失败: %w", right, rightErr)
	}
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo), nil
	}
	leftPath, err := canonicalPath(left)
	if err != nil {
		return false, fmt.Errorf("解析左侧 SQLite 路径 %q 失败: %w", left, err)
	}
	rightPath, err := canonicalPath(right)
	if err != nil {
		return false, fmt.Errorf("解析右侧 SQLite 路径 %q 失败: %w", right, err)
	}
	if equalFilesystemPath(leftPath, rightPath) {
		return true, nil
	}
	return false, nil
}

func pathWithin(root, candidate string) (bool, error) {
	rootAbs, err := canonicalPath(root)
	if err != nil {
		return false, fmt.Errorf("解析根路径 %q 失败: %w", root, err)
	}
	candidateAbs, err := canonicalPath(candidate)
	if err != nil {
		return false, fmt.Errorf("解析候选路径 %q 失败: %w", candidate, err)
	}
	relative, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil {
		return false, fmt.Errorf("比较候选路径 %q 与根路径 %q 失败: %w", candidate, root, err)
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func canonicalPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("SQLite 路径不能为空")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("获取绝对路径失败: %w", err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Lstat(abs)
	if err == nil {
		resolved, evalErr := filepath.EvalSymlinks(abs)
		if evalErr != nil {
			return "", fmt.Errorf("解析符号链接失败: %w", evalErr)
		}
		if _, statErr := os.Stat(abs); statErr != nil {
			return "", fmt.Errorf("stat 路径目标失败: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if _, statErr := os.Stat(resolved); statErr != nil {
				return "", fmt.Errorf("stat 符号链接目标失败: %w", statErr)
			}
		}
		return filepath.Clean(resolved), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("检查路径失败: %w", err)
	}
	parent := filepath.Dir(abs)
	suffix := []string{filepath.Base(abs)}
	for {
		_, parentErr := os.Lstat(parent)
		if parentErr == nil {
			resolvedParent, evalErr := filepath.EvalSymlinks(parent)
			if evalErr != nil {
				return "", fmt.Errorf("解析真实父目录 %q 失败: %w", parent, evalErr)
			}
			parentStat, statErr := os.Stat(parent)
			if statErr != nil {
				return "", fmt.Errorf("stat 真实父目录 %q 失败: %w", parent, statErr)
			}
			if !parentStat.IsDir() {
				return "", fmt.Errorf("真实父路径 %q 不是目录", parent)
			}
			parts := append([]string{resolvedParent}, suffix...)
			return filepath.Clean(filepath.Join(parts...)), nil
		}
		if !errors.Is(parentErr, os.ErrNotExist) {
			return "", fmt.Errorf("检查父路径 %q 失败: %w", parent, parentErr)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("路径没有可解析的真实父目录")
		}
		suffix = append([]string{filepath.Base(parent)}, suffix...)
		parent = next
	}
}

func equalFilesystemPath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
