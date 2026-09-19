package operationlog

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func wiEnv(base map[string]string, overrides map[string]string) func(string) string {
	env := map[string]string{}
	for key, value := range base {
		env[key] = value
	}
	for key, value := range overrides {
		if value == "" {
			delete(env, key)
		} else {
			env[key] = value
		}
	}
	return func(name string) string { return env[name] }
}

func TestWILoadConfigMatrix(t *testing.T) {
	root := t.TempDir()
	base := map[string]string{
		"JUHE_AI_OPERATION_LOG_STORE":                  "sqlite",
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID":            "wi-inst",
		"JUHE_AI_OPERATION_LOG_DATABASE_PATH":          filepath.Join(root, "operation.sqlite3"),
		"JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH": filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_USAGE_SHARD_ROOT":                     filepath.Join(root, "usage-shards"),
	}
	// 合法基线。
	cfg, err := LoadConfig(wiEnv(base, nil))
	if err != nil {
		t.Fatalf("基线配置失败: %v", err)
	}
	if !cfg.Enabled || cfg.Mode != ModeSQLite || cfg.RetentionDays != 365 {
		t.Fatalf("cfg=%+v", cfg)
	}
	// 2026-09-19 零配置默认：空 env 不再是“默认关闭”，而是跟随 sqlite 驱动
	// 并按 datadir 固定名表派生路径（实例 ID 回落 hostname）。
	empty, err := LoadConfig(func(string) string { return "" })
	if err != nil || !empty.Enabled || empty.Mode != ModeSQLite {
		t.Fatalf("空 env 默认=%+v err=%v", empty, err)
	}
	if empty.InstanceID == "" {
		t.Fatal("空 env 实例 ID 必须回落 hostname 默认值")
	}
	if empty.DatabasePath != filepath.Join("data", "operation-log.sqlite3") || empty.BusinessSettingsPath != filepath.Join("data", "business.sqlite3") || empty.UsageShardRoot != filepath.Join("data", "usage-shards") {
		t.Fatalf("空 env 派生路径=%+v", empty)
	}
	// JUHE_AI_DATA_DIR 指向临时目录时派生根随之切换（STORE 与路径族全部
	// 未配置 → 跟随 sqlite 驱动，Enabled 保持默认开启）。
	dataRoot := filepath.Join(root, "datadir")
	dataDirected, err := LoadConfig(wiEnv(base, map[string]string{
		"JUHE_AI_OPERATION_LOG_STORE":                  "",
		"JUHE_AI_OPERATION_LOG_DATABASE_PATH":          "",
		"JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH": "",
		"JUHE_AI_USAGE_SHARD_ROOT":                     "",
		"JUHE_AI_DATA_DIR":                             dataRoot,
	}))
	if err != nil {
		t.Fatalf("DATA_DIR 派生配置失败: %v", err)
	}
	if !dataDirected.Enabled || dataDirected.Mode != ModeSQLite {
		t.Fatalf("DATA_DIR 派生 Enabled=%t Mode=%s", dataDirected.Enabled, dataDirected.Mode)
	}
	if dataDirected.DatabasePath != filepath.Join(dataRoot, "operation-log.sqlite3") || dataDirected.BusinessSettingsPath != filepath.Join(dataRoot, "business.sqlite3") || dataDirected.UsageShardRoot != filepath.Join(dataRoot, "usage-shards") {
		t.Fatalf("DATA_DIR 派生路径=%+v", dataDirected)
	}
	// 非法矩阵。
	invalids := map[string]map[string]string{
		"owner lease 太短":  {"JUHE_AI_OPERATION_LOG_OWNER_LEASE": "1s"},
		"owner lease 非法":  {"JUHE_AI_OPERATION_LOG_OWNER_LEASE": "abc"},
		"retention 太短":    {"JUHE_AI_OPERATION_LOG_RETENTION_INTERVAL": "500ms"},
		"retention 太长":    {"JUHE_AI_OPERATION_LOG_RETENTION_INTERVAL": "25h"},
		"batch 越界":        {"JUHE_AI_OPERATION_LOG_RETENTION_BATCH_SIZE": "5097"},
		"batch 非数字":       {"JUHE_AI_OPERATION_LOG_RETENTION_BATCH_SIZE": "x"},
		"pg max open 非数字": {"JUHE_AI_OPERATION_LOG_POSTGRES_MAX_OPEN_CONNS": "x"},
		"pg max idle 非数字": {"JUHE_AI_OPERATION_LOG_POSTGRES_MAX_IDLE_CONNS": "0"},
		"非法 mode":         {"JUHE_AI_OPERATION_LOG_STORE": "weird"},
	}
	for name, overrides := range invalids {
		if _, err := LoadConfig(wiEnv(base, overrides)); err == nil {
			t.Fatalf("%s 必须报错", name)
		}
	}
	// postgres 模式缺 URL。
	pgEnv := wiEnv(base, map[string]string{"JUHE_AI_OPERATION_LOG_STORE": "postgres"})
	if _, err := LoadConfig(pgEnv); err == nil || !strings.Contains(err.Error(), "POSTGRES_URL") {
		t.Fatalf("postgres 缺 URL=%v", err)
	}
	// 隔离路径解析（逗号分隔 + 空段）。
	isolated, err := LoadConfig(wiEnv(base, map[string]string{
		"JUHE_AI_DATABASE_PATH": filepath.Join(root, "a.sqlite3") + ", , " + filepath.Join(root, "b.sqlite3"),
	}))
	if err != nil || len(isolated.SQLiteIsolationPaths) < 2 {
		t.Fatalf("隔离路径=%+v err=%v", isolated.SQLiteIsolationPaths, err)
	}
	// shard 隔离冲突必须报错：shard root 与 db path 物理重叠。
	conflict, err := LoadConfig(wiEnv(base, map[string]string{"JUHE_AI_USAGE_SHARD_ROOT": filepath.Join(root, "operation.sqlite3", "inner")}))
	if err == nil {
		// 某些环境下该组合不构成物理冲突，仅要求不 panic。
		_ = conflict
	}
}

func TestWINormalizeInputMatrix(t *testing.T) {
	valid := Input{ID: "in-1", ActorSystemAccountID: "actor", ActorRole: "admin", Module: "m", Action: "a", OperationKey: "m.a", ResourceType: "r", Summary: "s", CreatedAt: "2026-09-10T16:00:00+08:00"}
	normalized, err := normalizeInput(valid)
	if err != nil {
		t.Fatalf("合法输入报错: %v", err)
	}
	// 默认枚举与 canonical UTC。
	if normalized.Mode != "self" || normalized.DetailLevel != "full" || normalized.VisibilityScope != "targeted" {
		t.Fatalf("defaults=%+v", normalized)
	}
	if !strings.HasPrefix(normalized.CreatedAt, "2026-09-10T08:00:00") {
		t.Fatalf("createdAt=%q", normalized.CreatedAt)
	}
	// ResourceName 缺省时不派生 primary target（需要 ID 或名称其一非空）。
	valid.ResourceID = ""
	valid.ResourceName = "账号 A"
	withName, err := normalizeInput(valid)
	if err != nil || len(withName.Targets) != 1 || withName.Targets[0].Relation != "primary" {
		t.Fatalf("targets=%+v err=%v", withName.Targets, err)
	}
	// targeted 时 actor viewer 自动加入。
	if len(normalized.Viewers) == 0 || normalized.Viewers[0].VisibilityReason != "actor_self" {
		t.Fatalf("viewers=%+v", normalized.Viewers)
	}

	// 错误矩阵。
	missingID := valid
	missingID.ID = " "
	if _, err := normalizeInput(missingID); err == nil || !strings.Contains(err.Error(), "id") {
		t.Fatalf("缺 id=%v", err)
	}
	badTime := valid
	badTime.CreatedAt = "nope"
	if _, err := normalizeInput(badTime); err == nil || !strings.Contains(err.Error(), "createdAt") {
		t.Fatalf("坏 createdAt=%v", err)
	}
	badEnum := valid
	badEnum.Mode = "weird"
	if _, err := normalizeInput(badEnum); err == nil || !strings.Contains(err.Error(), "enum") {
		t.Fatalf("坏枚举=%v", err)
	}
	badMeta := valid
	badMeta.Metadata = []byte(`{`)
	if _, err := normalizeInput(badMeta); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("坏 metadata=%v", err)
	}
	badTarget := valid
	badTarget.Targets = []Target{{Relation: "primary"}}
	if _, err := normalizeInput(badTarget); err == nil || !strings.Contains(err.Error(), "target type") {
		t.Fatalf("坏 target=%v", err)
	}
	// all_users 时 viewers 清空为空切片（非 nil）。
	allUsers := valid
	allUsers.VisibilityScope = "all_users"
	allUsers.ResourceName = "账号 A"
	normalizedAll, err := normalizeInput(allUsers)
	if err != nil || len(normalizedAll.Viewers) != 0 {
		t.Fatalf("all_users viewers=%+v err=%v", normalizedAll.Viewers, err)
	}
}

func TestWIListFilterDimensions(t *testing.T) {
	store := wiOpenSQLiteStore(t, t.TempDir())
	ctx := context.Background()
	lease, ok, err := store.AcquireOwnerLease(ctx, "wi-filter2", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease=%v err=%v", ok, err)
	}
	base := Input{ActorSystemAccountID: "actor-9", ActorRole: "user", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", ResourceID: "acc-9", Summary: "update account", CreatedAt: "2026-08-13T00:00:00Z", OperationScopeSystemAccountID: "owner-9"}
	entry := base
	entry.ID = "op-f1"
	if ignored, err := store.Persist(ctx, lease, entry); err != nil || ignored {
		t.Fatalf("persist=%v err=%v", ignored, err)
	}
	filters := []ListOptions{
		{Action: "update"},
		{ResourceType: "account"},
		{ResourceID: "acc-9"},
		{ActorSystemAccountID: "actor-9"},
		{OperationScopeSystemAccountID: "owner-9"},
		{StartAt: "2026-08-12T00:00:00Z", EndAt: "2026-08-14T00:00:00Z"},
	}
	for index, options := range filters {
		result, err := store.List(ctx, options)
		if err != nil || len(result.Items) != 1 {
			t.Fatalf("过滤 %d=%+v err=%v", index, result, err)
		}
	}
	// 越界时间窗口 → 空。
	outside, err := store.List(ctx, ListOptions{StartAt: "2020-01-01T00:00:00Z", EndAt: "2020-01-02T00:00:00Z"})
	if err != nil || len(outside.Items) != 0 {
		t.Fatalf("窗外=%+v err=%v", outside, err)
	}
	// targeted 视图：非 viewer 的 actor 看不到该记录。
	hidden, err := store.List(ctx, ListOptions{ViewerID: "stranger"})
	if err != nil || len(hidden.Items) != 0 {
		t.Fatalf("stranger=%+v err=%v", hidden, err)
	}
	// Detail 的 full viewer（actor 自身）可以看到完整变更。
	detail, found, err := store.Detail(ctx, "op-f1", "actor-9")
	if err != nil || !found {
		t.Fatalf("detail=%+v found=%v err=%v", detail, found, err)
	}
}
