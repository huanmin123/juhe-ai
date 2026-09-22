// Package mockdata 是本地前后端联调造数的 Go 实现骨架：把一组可重复执行的
// 离线造数步骤收敛到 maintenance 的一次性 CLI（--mockdata），并给后续逐域实现
// （业务、用量、统计、可观测、chat/codex/model-check）留出稳定的注册点。
//
// 边界：
//   - 只写 SQLite（开发态 go-only + SQLite 布局）；postgres 由调用方在 CLI
//     层拒绝，包内不猜测 PG 语义。
//   - 只写 <dataDir> 下由本包解析出的存储；调用方必须显式给出数据根，包内
//     不提供「没配置就写当前目录」的隐式回退。
//   - 重复执行幂等：每次先按固定清理标识删除上一批数据，再重建。
//
// 依据：docs/functions/Mockdata造数设计.md（清理标识、覆盖点与验证点）与
// migration-backup-1/node/final-archive/backend/src/scripts/maintenance/mockdata/
// （Node 实现的只读参照物）。
package mockdata

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// 存储名：与 gateway/internal/datadir、jobs/cmd/juhe-ai-jobs 的固定名同源，
// 报告、清理结果与覆盖报告都用这些名字定位。
const (
	StoreBusiness      = "business"
	StoreChat          = "chat"
	StoreDataset       = "dataset"
	StoreUsageCatalog  = "usage-catalog"
	StoreStats         = "stats"
	StoreRuntimeLog    = "runtime-log"
	StoreTableMonitor  = "table-monitor"
	StoreAuditLog      = "audit-log"
	StoreOperationLog  = "operation-log"
	StoreModelCheck    = "model-check"
	StoreTaskRuns      = "task-runs"
	StoreAccountHealth = "account-health"
	// StoreCodexContextShardPrefix 与 StoreUsageShardPrefix 是分片存储的
	// 名字前缀，实际存储名为 <前缀>[<序号或日期>/<分片号>]。
	StoreCodexContextShardPrefix = "codex-context-shard"
	StoreUsageShardPrefix        = "usage-shard"
)

// 固定文件名（与 gateway/internal/datadir 的固定名表逐字一致）。
const (
	fixedBusinessDatabase      = "business.sqlite3"
	fixedChatDatabase          = "chat.sqlite3"
	fixedDatasetDatabase       = "dataset.sqlite3"
	fixedUsageCatalogDatabase  = "usage-catalog.sqlite3"
	fixedStatsDatabase         = "stats.sqlite3"
	fixedRuntimeLogDatabase    = "runtime-log.sqlite3"
	fixedTableMonitorDatabase  = "table-monitor.sqlite3"
	fixedModelCheckDatabase    = "model-check.sqlite3"
	fixedTaskRunsDatabase      = "task-runs.sqlite3"
	fixedAccountHealthDatabase = "account-health.sqlite3"
	fixedAuditLogDatabase      = "audit-log.sqlite3"
	fixedOperationLogDatabase  = "operation-log.sqlite3"
	fixedCodexContextShardRoot = "codex-context/state-shards"
	fixedUsageShardRoot        = "usage-shards"
	fixedChatAssetsDirectory   = "chat-assets"
	fixedAuditBlobDirectory    = "audit-blob"
	fixedSearchHotDirectory    = "search-hot"
	fixedAccountHealthInputDir = "account-health-input"
	fixedLogDirectory          = "logs"
)

// 默认分片数量：JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT /
// JUHE_AI_USAGE_SHARD_COUNT 未配置时的值（与 gateway/jobs 的默认值一致）。
const defaultShardCount = 16

// shardCountMin/Max 是分片数量的合法区间，与 gateway runtime 的
// JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT 校验（1..256）保持一致。
const (
	shardCountMin = 1
	shardCountMax = 256
)

// Paths 是 mockdata 需要读写的全部存储位置。
//
// 每个字段的解析规则都是「显式 env 优先，未配置才按 <dataDir>/<固定名> 派生」
// （2026-09-19 零配置存储约定，见 gateway/internal/datadir）。为什么维护一张
// 本地副本而不是 import gateway 的 datadir：Go 三项目基线禁止跨项目 import，
// 且 datadir 是 gateway 模块的内部包；副本由 paths_mockdata_test.go 的
// env→路径表逐项钉住，改动必须同步两侧。
type Paths struct {
	// DataDir 是本次造数的数据根；所有派生路径都以它为基准。
	DataDir string
	// LogDir 是运行日志文件目录（JUHE_AI_LOG_DIR 语义），不是 SQLite 文件。
	LogDir string

	Business                    string
	Chat                        string
	Dataset                     string
	UsageCatalog                string
	Stats                       string
	RuntimeLog                  string
	TableMonitor                string
	AuditLog                    string
	OperationLog                string
	ModelCheck                  string
	TaskRuns                    string
	AccountHealth               string
	CodexContextShardRoot       string
	CodexContextShardCount      int
	UsageShardRoot              string
	UsageShardCount             int
	ChatAssetsRoot              string
	AuditBlobDirectory          string
	SearchHotDirectory          string
	AccountHealthInputDirectory string
}

// ResolvePaths 按显式优先规则解析全部存储路径。
//
// dataDir 必须显式给出（空白即错）：maintenance 的造数命令不得隐式落到
// 「当前目录下的 data」，否则一次误执行就会在仓库工作区里造出数据。
// getenv 为 nil 时视为空环境，便于测试与嵌入式调用。
func ResolvePaths(dataDir, logDir string, getenv func(string) string) (Paths, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return Paths{}, fmt.Errorf("mockdata 需要显式数据根目录（--mockdata-data-dir 或 JUHE_AI_DATA_DIR）")
	}
	path := func(envName, fixedName string) string {
		if value := strings.TrimSpace(getenv(envName)); value != "" {
			return value
		}
		return filepath.Join(dataDir, fixedName)
	}
	codexCount, err := shardCount(getenv, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT")
	if err != nil {
		return Paths{}, err
	}
	usageCount, err := shardCount(getenv, "JUHE_AI_USAGE_SHARD_COUNT")
	if err != nil {
		return Paths{}, err
	}
	logDir = strings.TrimSpace(logDir)
	if logDir == "" {
		logDir = strings.TrimSpace(getenv("JUHE_AI_LOG_DIR"))
	}
	if logDir == "" {
		logDir = filepath.Join(dataDir, fixedLogDirectory)
	}
	auditBlob := path("JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY", fixedAuditBlobDirectory)
	searchHot := strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY"))
	if searchHot == "" {
		// 与 gateway auditlog store 的派生一致：热点检索目录是 payload blob
		// 目录的同级兄弟，未配置时才这么派生。
		searchHot = filepath.Join(filepath.Dir(auditBlob), fixedSearchHotDirectory)
	}
	return Paths{
		DataDir:      dataDir,
		LogDir:       logDir,
		Business:     path("JUHE_AI_DATABASE_PATH", fixedBusinessDatabase),
		Chat:         path("JUHE_AI_CHAT_DATABASE_PATH", fixedChatDatabase),
		Dataset:      path("JUHE_AI_DATASET_DATABASE_PATH", fixedDatasetDatabase),
		UsageCatalog: path("JUHE_AI_USAGE_CATALOG_DATABASE_PATH", fixedUsageCatalogDatabase),
		Stats:        path("JUHE_AI_STATS_DATABASE_PATH", fixedStatsDatabase),
		RuntimeLog:   path("JUHE_AI_RUNTIME_LOG_DATABASE_PATH", fixedRuntimeLogDatabase),
		TableMonitor: path("JUHE_AI_TABLE_MONITOR_DATABASE_PATH", fixedTableMonitorDatabase),
		AuditLog:     path("JUHE_AI_AUDIT_LOG_DATABASE_PATH", fixedAuditLogDatabase),
		OperationLog: path("JUHE_AI_OPERATION_LOG_DATABASE_PATH", fixedOperationLogDatabase),
		// model-check 是 J3b owner 的专库；env 名沿用 gateway
		// modelcheckowner 的 JUHE_AI_J3B_DATABASE_PATH（不存在
		// JUHE_AI_MODEL_CHECK_DATABASE_PATH，见 datadir.ModelCheckDatabase
		// 与 gateway/internal/modelcheckowner/config.go:103）。
		ModelCheck:                  path("JUHE_AI_J3B_DATABASE_PATH", fixedModelCheckDatabase),
		TaskRuns:                    path("JUHE_AI_TASK_RUNS_DATABASE_PATH", fixedTaskRunsDatabase),
		AccountHealth:               path("JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH", fixedAccountHealthDatabase),
		CodexContextShardRoot:       path("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT", fixedCodexContextShardRoot),
		CodexContextShardCount:      codexCount,
		UsageShardRoot:              path("JUHE_AI_USAGE_SHARD_ROOT", fixedUsageShardRoot),
		UsageShardCount:             usageCount,
		ChatAssetsRoot:              path("JUHE_AI_CHAT_ASSETS_ROOT", fixedChatAssetsDirectory),
		AuditBlobDirectory:          auditBlob,
		SearchHotDirectory:          searchHot,
		AccountHealthInputDirectory: path("JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY", fixedAccountHealthInputDir),
	}, nil
}

// shardCount 解析分片数量 env：未配置取默认 16，非法（非整数或越界）即错，
// 不做静默钳制——分片数量决定文件布局，猜错会让造数写到运行时读不到的目录。
func shardCount(getenv func(string) string, envName string) (int, error) {
	raw := strings.TrimSpace(getenv(envName))
	if raw == "" {
		return defaultShardCount, nil
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < shardCountMin || count > shardCountMax {
		return 0, fmt.Errorf("%s 必须在 %d 到 %d 之间: %q", envName, shardCountMin, shardCountMax, raw)
	}
	return count, nil
}

// codexContextShardFilename 与 Node codexContextStateShardPath 的文件名一致。
func codexContextShardFilename(shardIndex int) string {
	return fmt.Sprintf("state-%03d.sqlite3", shardIndex)
}

// codexContextShardName 是分片存储名（<前缀>[<序号>]），报告与覆盖报告用它
// 区分同一目录下的多个分片文件。
func codexContextShardName(shardIndex int) string {
	return fmt.Sprintf("%s[%d]", StoreCodexContextShardPrefix, shardIndex)
}

// store 描述 mockdata 覆盖的单个 SQLite 文件。
type store struct {
	// Name 是稳定存储名（报告、清理计数、覆盖报告的 key）。
	Name string
	// Path 是解析后的文件路径。
	Path string
	// Domain 是声明拥有该存储的造数域；覆盖报告用它把「域未接线」的断言
	// 从非空硬要求里区分出来（见 coverage.go）。
	Domain string
}

// fixedStores 返回固定文件存储清单（不含分片）。顺序固定，便于报告比对。
func (p Paths) fixedStores() []store {
	return []store{
		{Name: StoreBusiness, Path: p.Business, Domain: DomainBusiness},
		{Name: StoreChat, Path: p.Chat, Domain: DomainChatCodexModelCheck},
		{Name: StoreDataset, Path: p.Dataset, Domain: DomainObservability},
		{Name: StoreUsageCatalog, Path: p.UsageCatalog, Domain: DomainUsage},
		{Name: StoreStats, Path: p.Stats, Domain: DomainStats},
		{Name: StoreRuntimeLog, Path: p.RuntimeLog, Domain: DomainObservability},
		{Name: StoreTableMonitor, Path: p.TableMonitor, Domain: DomainObservability},
		{Name: StoreAuditLog, Path: p.AuditLog, Domain: DomainObservability},
		{Name: StoreOperationLog, Path: p.OperationLog, Domain: DomainObservability},
		{Name: StoreModelCheck, Path: p.ModelCheck, Domain: DomainChatCodexModelCheck},
		{Name: StoreTaskRuns, Path: p.TaskRuns, Domain: DomainChatCodexModelCheck},
		{Name: StoreAccountHealth, Path: p.AccountHealth, Domain: DomainChatCodexModelCheck},
	}
}

// codexContextStores 展开 codex context state 分片清单。分片数量由
// JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT 决定；即使某个分片文件尚未存在也
// 返回它，让清理步骤可以显式记录 skipped 而不是无声跳过。
func (p Paths) codexContextStores() []store {
	shards := make([]store, 0, p.CodexContextShardCount)
	for shardIndex := 0; shardIndex < p.CodexContextShardCount; shardIndex++ {
		shards = append(shards, store{
			Name:   codexContextShardName(shardIndex),
			Path:   filepath.Join(p.CodexContextShardRoot, codexContextShardFilename(shardIndex)),
			Domain: DomainChatCodexModelCheck,
		})
	}
	return shards
}
