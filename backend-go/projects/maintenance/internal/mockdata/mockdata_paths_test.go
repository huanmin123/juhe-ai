package mockdata

import (
	"path/filepath"
	"strings"
	"testing"
)

// envMap 把显式 env 表包装成 ResolvePaths 需要的 getenv。
func envMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestResolvePathsDerivesFixedNamesUnderDataDir(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	join := func(name string) string { return filepath.Join(root, name) }
	cases := map[string]string{
		"business":             paths.Business,
		"chat":                 paths.Chat,
		"dataset":              paths.Dataset,
		"usage-catalog":        paths.UsageCatalog,
		"stats":                paths.Stats,
		"runtime-log":          paths.RuntimeLog,
		"table-monitor":        paths.TableMonitor,
		"audit-log":            paths.AuditLog,
		"operation-log":        paths.OperationLog,
		"model-check":          paths.ModelCheck,
		"task-runs":            paths.TaskRuns,
		"account-health":       paths.AccountHealth,
		"chat-assets":          paths.ChatAssetsRoot,
		"audit-blob":           paths.AuditBlobDirectory,
		"account-health-input": paths.AccountHealthInputDirectory,
	}
	want := map[string]string{
		"business":             join("business.sqlite3"),
		"chat":                 join("chat.sqlite3"),
		"dataset":              join("dataset.sqlite3"),
		"usage-catalog":        join("usage-catalog.sqlite3"),
		"stats":                join("stats.sqlite3"),
		"runtime-log":          join("runtime-log.sqlite3"),
		"table-monitor":        join("table-monitor.sqlite3"),
		"audit-log":            join("audit-log.sqlite3"),
		"operation-log":        join("operation-log.sqlite3"),
		"model-check":          join("model-check.sqlite3"),
		"task-runs":            join("task-runs.sqlite3"),
		"account-health":       join("account-health.sqlite3"),
		"chat-assets":          join("chat-assets"),
		"audit-blob":           join("audit-blob"),
		"account-health-input": join("account-health-input"),
	}
	for name, got := range cases {
		if got != want[name] {
			t.Fatalf("%s path = %q, want %q", name, got, want[name])
		}
	}
	if paths.CodexContextShardRoot != filepath.Join(root, "codex-context", "state-shards") {
		t.Fatalf("codex shard root = %q", paths.CodexContextShardRoot)
	}
	if paths.UsageShardRoot != join("usage-shards") {
		t.Fatalf("usage shard root = %q", paths.UsageShardRoot)
	}
	// search-hot 未配置时是 audit-blob 的同级兄弟（gateway auditlog store 的派生）。
	if paths.SearchHotDirectory != join("search-hot") {
		t.Fatalf("search hot = %q", paths.SearchHotDirectory)
	}
	if paths.CodexContextShardCount != defaultShardCount || paths.UsageShardCount != defaultShardCount {
		t.Fatalf("default shard counts = %d/%d", paths.CodexContextShardCount, paths.UsageShardCount)
	}
	if paths.LogDir != join("logs") {
		t.Fatalf("log dir = %q", paths.LogDir)
	}
}

// TestResolvePathsExplicitEnvWins 逐项核验「显式优先」：表里每个 env 都必须
// 覆盖对应的派生默认值。
func TestResolvePathsExplicitEnvWins(t *testing.T) {
	root := t.TempDir()
	explicit := t.TempDir()
	env := map[string]string{
		"JUHE_AI_DATABASE_PATH":                   filepath.Join(explicit, "b.sqlite3"),
		"JUHE_AI_CHAT_DATABASE_PATH":              filepath.Join(explicit, "c.sqlite3"),
		"JUHE_AI_DATASET_DATABASE_PATH":           filepath.Join(explicit, "d.sqlite3"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":     filepath.Join(explicit, "u.sqlite3"),
		"JUHE_AI_STATS_DATABASE_PATH":             filepath.Join(explicit, "s.sqlite3"),
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH":       filepath.Join(explicit, "rl.sqlite3"),
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH":     filepath.Join(explicit, "tm.sqlite3"),
		"JUHE_AI_AUDIT_LOG_DATABASE_PATH":         filepath.Join(explicit, "al.sqlite3"),
		"JUHE_AI_OPERATION_LOG_DATABASE_PATH":     filepath.Join(explicit, "ol.sqlite3"),
		"JUHE_AI_J3B_DATABASE_PATH":               filepath.Join(explicit, "mc.sqlite3"),
		"JUHE_AI_TASK_RUNS_DATABASE_PATH":         filepath.Join(explicit, "tr.sqlite3"),
		"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":    filepath.Join(explicit, "ah.sqlite3"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT":  filepath.Join(explicit, "codex"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT": "3",
		"JUHE_AI_USAGE_SHARD_ROOT":                filepath.Join(explicit, "usage"),
		"JUHE_AI_USAGE_SHARD_COUNT":               "5",
		"JUHE_AI_CHAT_ASSETS_ROOT":                filepath.Join(explicit, "assets"),
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY":        filepath.Join(explicit, "blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY":  filepath.Join(explicit, "hot"),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":  filepath.Join(explicit, "ah-input"),
		"JUHE_AI_LOG_DIR":                         filepath.Join(explicit, "logs"),
		// 显式配置的数据根必须被 dataDir 参数覆盖（参数优先于 env）。
		"JUHE_AI_DATA_DIR": filepath.Join(explicit, "ignored"),
	}
	paths, err := ResolvePaths(root, "", envMap(env))
	if err != nil {
		t.Fatal(err)
	}
	if paths.DataDir != root {
		t.Fatalf("dataDir = %q, want the explicit parameter %q", paths.DataDir, root)
	}
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"business", paths.Business, env["JUHE_AI_DATABASE_PATH"]},
		{"chat", paths.Chat, env["JUHE_AI_CHAT_DATABASE_PATH"]},
		{"dataset", paths.Dataset, env["JUHE_AI_DATASET_DATABASE_PATH"]},
		{"usage-catalog", paths.UsageCatalog, env["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"]},
		{"stats", paths.Stats, env["JUHE_AI_STATS_DATABASE_PATH"]},
		{"runtime-log", paths.RuntimeLog, env["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"]},
		{"table-monitor", paths.TableMonitor, env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"]},
		{"audit-log", paths.AuditLog, env["JUHE_AI_AUDIT_LOG_DATABASE_PATH"]},
		{"operation-log", paths.OperationLog, env["JUHE_AI_OPERATION_LOG_DATABASE_PATH"]},
		{"model-check", paths.ModelCheck, env["JUHE_AI_J3B_DATABASE_PATH"]},
		{"task-runs", paths.TaskRuns, env["JUHE_AI_TASK_RUNS_DATABASE_PATH"]},
		{"account-health", paths.AccountHealth, env["JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH"]},
		{"codex-shard-root", paths.CodexContextShardRoot, env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"]},
		{"usage-shard-root", paths.UsageShardRoot, env["JUHE_AI_USAGE_SHARD_ROOT"]},
		{"chat-assets", paths.ChatAssetsRoot, env["JUHE_AI_CHAT_ASSETS_ROOT"]},
		{"audit-blob", paths.AuditBlobDirectory, env["JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY"]},
		{"search-hot", paths.SearchHotDirectory, env["JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY"]},
		{"account-health-input", paths.AccountHealthInputDirectory, env["JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY"]},
		{"log-dir", paths.LogDir, env["JUHE_AI_LOG_DIR"]},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Fatalf("%s = %q, want explicit %q", check.name, check.got, check.want)
		}
	}
	if paths.CodexContextShardCount != 3 || paths.UsageShardCount != 5 {
		t.Fatalf("explicit shard counts = %d/%d", paths.CodexContextShardCount, paths.UsageShardCount)
	}
}

func TestResolvePathsRejectsBadInput(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name    string
		dataDir string
		logDir  string
		env     map[string]string
		want    string
	}{
		{name: "empty data dir", dataDir: "  ", want: "需要显式数据根目录"},
		{name: "blank data dir", dataDir: "", want: "需要显式数据根目录"},
		{name: "codex count not a number", dataDir: root, env: map[string]string{"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT": "abc"}, want: "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT"},
		{name: "codex count zero", dataDir: root, env: map[string]string{"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT": "0"}, want: "必须在 1 到 256 之间"},
		{name: "codex count too large", dataDir: root, env: map[string]string{"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT": "257"}, want: "必须在 1 到 256 之间"},
		{name: "usage count not a number", dataDir: root, env: map[string]string{"JUHE_AI_USAGE_SHARD_COUNT": "x"}, want: "JUHE_AI_USAGE_SHARD_COUNT"},
		{name: "usage count negative", dataDir: root, env: map[string]string{"JUHE_AI_USAGE_SHARD_COUNT": "-1"}, want: "必须在 1 到 256 之间"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ResolvePaths(testCase.dataDir, testCase.logDir, envMap(testCase.env))
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want substring %q", err, testCase.want)
			}
		})
	}
}

func TestResolvePathsLogDirAndCodexShardFilename(t *testing.T) {
	root := t.TempDir()
	// logDir 参数优先于 JUHE_AI_LOG_DIR。
	explicitLog := filepath.Join(root, "explicit-logs")
	paths, err := ResolvePaths(root, explicitLog, envMap(map[string]string{"JUHE_AI_LOG_DIR": filepath.Join(root, "ignored")}))
	if err != nil {
		t.Fatal(err)
	}
	if paths.LogDir != explicitLog {
		t.Fatalf("log dir = %q, want %q", paths.LogDir, explicitLog)
	}
	// 无 logDir 参数时 JUHE_AI_LOG_DIR 生效。
	paths, err = ResolvePaths(root, "", envMap(map[string]string{"JUHE_AI_LOG_DIR": filepath.Join(root, "env-logs")}))
	if err != nil {
		t.Fatal(err)
	}
	if paths.LogDir != filepath.Join(root, "env-logs") {
		t.Fatalf("log dir = %q", paths.LogDir)
	}
	if got := codexContextShardFilename(7); got != "state-007.sqlite3" {
		t.Fatalf("shard filename = %q", got)
	}
	if got := codexContextShardName(7); got != "codex-context-shard[7]" {
		t.Fatalf("shard name = %q", got)
	}
	if paths.CodexContextShardCount != defaultShardCount {
		t.Fatalf("shard count = %d", paths.CodexContextShardCount)
	}
}

func TestPathsStoreExpansion(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(map[string]string{
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT": "2",
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixed := paths.fixedStores()
	if len(fixed) != 12 {
		t.Fatalf("fixed stores = %d, want 12", len(fixed))
	}
	shards := paths.codexContextStores()
	if len(shards) != 2 {
		t.Fatalf("codex shards = %d, want 2", len(shards))
	}
	if shards[1].Path != filepath.Join(paths.CodexContextShardRoot, "state-001.sqlite3") {
		t.Fatalf("shard path = %q", shards[1].Path)
	}
	if shards[0].Domain != DomainChatCodexModelCheck {
		t.Fatalf("shard domain = %q", shards[0].Domain)
	}
	// usage 分片只取文件系统上已经存在的文件。
	if got := paths.usageShardStores(); len(got) != 0 {
		t.Fatalf("usage shards = %v, want none before any file exists", got)
	}
	all := paths.allStores()
	if len(all) != 14 {
		t.Fatalf("all stores = %d, want 14", len(all))
	}
}
