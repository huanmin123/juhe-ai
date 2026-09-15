package main

// W1 散件纯函数补充覆盖（w1u 前缀）：main.go env 工具、compose.go 尾部适配器、
// chain_runtime.go 的 Redis/日志桥、chain_usage.go 投影、chain_catalog.go 值归一化、
// chain_request_failure_health.go 内存 marks + outbox fence 路径、chain_chat.go
// 资产对象存储、chain_chat_observation.go 纯解析、compose_account_balance_refresh.go
// 纯逻辑、chain_accounts_traffic_migration.go 与 chain_apikey_rotation_redis.go
// 剩余分支。与既有 *_test.go 已覆盖路径不重复（见各函数注释）。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	businesssettings "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/settings"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/settings"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

// ---------------------------------------------------------------------------
// 共享 w1u helper
// ---------------------------------------------------------------------------

// w1uSetupRedis 与 w1iSetupRedis 同模式（miniredis + go-redis），按约定使用
// 独立 w1u 前缀 helper，避免跨文件隐式耦合。
func w1uSetupRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("启动 miniredis 失败: %v", err)
	}
	t.Cleanup(func() { mr.Close() })
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// w1uOpenPlainSQLite 打开临时目录下的独立 SQLite 文件句柄。
func w1uOpenPlainSQLite(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := bootstrap.OpenSQLiteFile(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("打开 SQLite 文件 %s 失败: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// w1uOpenSeededBusinessDB 打开并 ensure+seed 临时业务库（真实 maintenance
// bootstrap 面），供 authsys 账户仓与 settings 仓测试复用。
func w1uOpenSeededBusinessDB(t *testing.T) *sql.DB {
	t.Helper()
	db := w1uOpenPlainSQLite(t, "business.sqlite3")
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("ensure 业务 schema 失败: %v", err)
	}
	if _, err := bootstrap.SeedSQLiteBusiness(context.Background(), db, bootstrap.SeedOptions{Secret: "w1u-seed-secret"}); err != nil {
		t.Fatalf("seed 业务库失败: %v", err)
	}
	return db
}

// ---------------------------------------------------------------------------
// main.go：envOrDefault / commaList / envBool / loadSessionRetentionConfig
// ---------------------------------------------------------------------------

func TestW1UEnvOrDefault(t *testing.T) {
	t.Setenv("W1U_ENV_OR_DEFAULT", "present")
	if got := envOrDefault("W1U_ENV_OR_DEFAULT", "fallback"); got != "present" {
		t.Errorf("envOrDefault 已设置 = %q，want \"present\"", got)
	}
	t.Setenv("W1U_ENV_OR_DEFAULT_EMPTY", "")
	if got := envOrDefault("W1U_ENV_OR_DEFAULT_EMPTY", "fallback"); got != "fallback" {
		t.Errorf("envOrDefault 空值 = %q，want \"fallback\"", got)
	}
	if got := envOrDefault("W1U_ENV_OR_DEFAULT_MISSING", "fallback"); got != "fallback" {
		t.Errorf("envOrDefault 未设置 = %q，want \"fallback\"", got)
	}
}

func TestW1UCommaList(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"空串", "", []string{}},
		{"单项", "a", []string{"a"}},
		{"多项", "a,b,c", []string{"a", "b", "c"}},
		{"首尾与项间空白", " a , b ,c", []string{"a", "b", "c"}},
		{"空段剔除", ",,a,,b,", []string{"a", "b"}},
		{"纯逗号", ",", []string{}},
		{"中文项", "模型A, 模型B", []string{"模型A", "模型B"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commaList(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("commaList(%q) = %#v，want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestW1UEnvBool(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"1", true}, {"true", true}, {"TRUE", true}, {"True", true},
		{"yes", true}, {"YES", true}, {"on", true}, {"ON", true},
		{" 1 ", true}, {" true\t", true},
		{"", false}, {"0", false}, {"false", false}, {"no", false},
		{"off", false}, {"truex", false}, {"2", false}, {"  ", false},
	}
	for _, tc := range cases {
		t.Setenv("W1U_ENV_BOOL", tc.raw)
		if got := envBool("W1U_ENV_BOOL"); got != tc.want {
			t.Errorf("envBool(%q) = %v，want %v", tc.raw, got, tc.want)
		}
	}
}

// loadSessionRetentionConfig 的默认值与非法值拒绝已由 main_test.go 覆盖；这里
// 只补合法自定义区间、首尾空白裁剪与负数/零批量分支。
func TestW1ULoadSessionRetentionConfigExtraBranches(t *testing.T) {
	getenv := func(interval, batch string) func(string) string {
		return func(key string) string {
			switch key {
			case "JUHE_AI_SESSION_RETENTION_INTERVAL":
				return interval
			case "JUHE_AI_SESSION_RETENTION_BATCH_SIZE":
				return batch
			}
			return ""
		}
	}
	interval, limit, err := loadSessionRetentionConfig(getenv("5m", "250"))
	if err != nil || interval != 5*time.Minute || limit != 250 {
		t.Fatalf("自定义 (5m, 250) = (%s, %d, %v)，want (5m, 250, nil)", interval, limit, err)
	}
	interval, limit, err = loadSessionRetentionConfig(getenv(" 2h ", " 300 "))
	if err != nil || interval != 2*time.Hour || limit != 300 {
		t.Fatalf("空白裁剪 ( 2h ,  300 ) = (%s, %d, %v)，want (2h, 300, nil)", interval, limit, err)
	}
	if _, _, err := loadSessionRetentionConfig(getenv("-1m", "")); err == nil {
		t.Error("负数 interval 必须报错")
	} else if !strings.Contains(err.Error(), "JUHE_AI_SESSION_RETENTION_INTERVAL") {
		t.Errorf("interval 错误消息 = %q，want 包含变量名", err.Error())
	}
	if _, _, err := loadSessionRetentionConfig(getenv("", "-1")); err == nil {
		t.Error("负数 batch 必须报错")
	} else if !strings.Contains(err.Error(), "JUHE_AI_SESSION_RETENTION_BATCH_SIZE") {
		t.Errorf("batch 错误消息 = %q，want 包含变量名", err.Error())
	}
	if _, _, err := loadSessionRetentionConfig(getenv("", "0")); err == nil {
		t.Error("零 batch 必须报错")
	}
}

// trustProxyCount 的 yes/12/17 已由 runtime_w4g_test.go 覆盖；这里补其余分支。
func TestW1UTrustProxyCountExtraBranches(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", 0},
		{"   ", 0},
		{" false ", 0},
		{"NO", 0},
		{"off", 0},
		{"true", 1},
		{"TRUE", 1},
		{" on ", 1},
		{"16", 16},
		{"0", 0},
		{"-1", 0},
		{"17", 0},
		{"abc", 0},
		{"1.5", 0},
	}
	for _, tc := range cases {
		if got := trustProxyCount(tc.raw); got != tc.want {
			t.Errorf("trustProxyCount(%q) = %d，want %d", tc.raw, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// compose.go 尾部：settingsTimezone / businessDialect / configureSQLiteConnection
// ---------------------------------------------------------------------------

func TestW1USettingsTimezoneAdapter(t *testing.T) {
	var seenKey string
	read := func(key string) (string, error) {
		seenKey = key
		return "Asia/Shanghai", nil
	}
	source := settingsTimezone(read)
	value, err := source(context.Background())
	if err != nil || value != "Asia/Shanghai" {
		t.Fatalf("settingsTimezone 读取 = (%q, %v)，want (Asia/Shanghai, nil)", value, err)
	}
	if seenKey != "usageStatsTimezone" {
		t.Fatalf("读取键 = %q，want usageStatsTimezone", seenKey)
	}
	wantErr := errors.New("settings 读取失败")
	failing := settingsTimezone(func(string) (string, error) { return "", wantErr })
	if _, err := failing(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("settingsTimezone 错误透传 = %v，want %v", err, wantErr)
	}
}

func TestW1UBusinessDialectMapping(t *testing.T) {
	if got := businessDialect(true); got != businesssettings.Postgres {
		t.Errorf("businessDialect(true) = %v，want Postgres", got)
	}
	if got := businessDialect(false); got != businesssettings.SQLite {
		t.Errorf("businessDialect(false) = %v，want SQLite", got)
	}
}

func TestW1UConfigureSQLiteConnectionPragmas(t *testing.T) {
	db := w1uOpenPlainSQLite(t, "pragma.sqlite3")
	if err := configureSQLiteConnection(db); err != nil {
		t.Fatalf("configureSQLiteConnection = %v，want nil", err)
	}
	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("读取 foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d，want 1", foreignKeys)
	}
	var busyTimeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("读取 busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d，want 5000", busyTimeout)
	}
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("读取 journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q，want wal", journalMode)
	}
}

// ---------------------------------------------------------------------------
// compose.go：aiAccountLimitSettingsAdapter.UserAiAccountLimit（真实 settings 仓）
// ---------------------------------------------------------------------------

// aiAccountLimitSettingsAdapter 的 settings 字段是具体 *settings.Store，无法
// 注入 fake，因此走真实仓：临时业务库上默认值 100 → Update 250 → 读表失败三段。
func TestW1UAiAccountLimitSettingsAdapterBranches(t *testing.T) {
	db := w1uOpenSeededBusinessDB(t)
	base := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	step := 0
	// 每次 now 前进 2 分钟：settingsCacheTTL（60s）必然过期，Load 总是重读库。
	now := func() time.Time {
		step++
		return base.Add(time.Duration(step) * 2 * time.Minute)
	}
	store, err := settings.NewStore(db, false, now, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	adapter := aiAccountLimitSettingsAdapter{settings: store}
	ctx := context.Background()

	got, err := adapter.UserAiAccountLimit(ctx)
	if err != nil || got != 100 {
		t.Fatalf("默认上限 = (%d, %v)，want (100, nil)", got, err)
	}
	if _, err := store.Update(ctx, map[string]any{"userAiAccountLimit": float64(250)}); err != nil {
		t.Fatalf("Update userAiAccountLimit=250: %v", err)
	}
	got, err = adapter.UserAiAccountLimit(ctx)
	if err != nil || got != 250 {
		t.Fatalf("更新后上限 = (%d, %v)，want (250, nil)", got, err)
	}
	if _, err := db.Exec("DROP TABLE system_settings"); err != nil {
		t.Fatalf("DROP system_settings: %v", err)
	}
	if got, err := adapter.UserAiAccountLimit(ctx); err == nil {
		t.Fatalf("读表失败必须透传错误，got (%d, nil)", got)
	}
}

// ---------------------------------------------------------------------------
// compose.go：devAutoLoginResolver（nil 分支 + 真实种子账户正/负路径）
// ---------------------------------------------------------------------------

func TestW1UDevAutoLoginResolver(t *testing.T) {
	// 未配置用户名 → nil。
	if got := devAutoLoginResolver(&authsys.Deps{}, ""); got != nil {
		t.Fatal("空用户名 resolver 必须为 nil")
	}
	// Accounts 未接线 → nil。
	if got := devAutoLoginResolver(&authsys.Deps{}, "admin"); got != nil {
		t.Fatal("Accounts 为 nil 时 resolver 必须为 nil")
	}

	db := w1uOpenSeededBusinessDB(t)
	accountStore, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, time.Now)
	if err != nil {
		t.Fatalf("NewAccountStore: %v", err)
	}
	deps := &authsys.Deps{Accounts: accountStore}

	// 种子 admin（sys_admin / super_admin / active）→ AuthContext。
	resolver := devAutoLoginResolver(deps, "admin")
	if resolver == nil {
		t.Fatal("已配置用户名必须返回 resolver")
	}
	ctx := resolver(nil)
	if ctx == nil {
		t.Fatal("种子 admin 必须解析出 AuthContext")
	}
	if ctx.SystemAccountID != "sys_admin" || ctx.Username != "admin" || ctx.Role != "super_admin" || ctx.SessionID != "development-auto-login" {
		t.Fatalf("AuthContext = %+v，want sys_admin/admin/super_admin/development-auto-login", ctx)
	}

	// 未知用户名 → nil 上下文（resolver 本身非 nil）。
	unknown := devAutoLoginResolver(deps, "w1u-no-such-user")
	if unknown == nil {
		t.Fatal("未知用户名仍应返回 resolver（运行时返回 nil 上下文）")
	}
	if got := unknown(nil); got != nil {
		t.Fatalf("未知用户名上下文 = %+v，want nil", got)
	}

	// 非 active 账户 → nil 上下文。
	if _, err := db.Exec("UPDATE system_accounts SET status = 'disabled' WHERE username = 'admin'"); err != nil {
		t.Fatalf("停用 admin: %v", err)
	}
	if got := resolver(nil); got != nil {
		t.Fatalf("停用账户上下文 = %+v，want nil", got)
	}
}

// ---------------------------------------------------------------------------
// chain_runtime.go：quotaRuntimeStateBridge / redisEvalClient / redisStateClientProvider
// ---------------------------------------------------------------------------

func TestW1UQuotaRuntimeStateBridge(t *testing.T) {
	client := w1uSetupRedis(t)
	alpha, err := gatewayquota.NewRedisRuntimeStateStore(client, "w1u-ns", "alpha")
	if err != nil {
		t.Fatalf("NewRedisRuntimeStateStore(alpha): %v", err)
	}
	beta, err := gatewayquota.NewRedisRuntimeStateStore(client, "w1u-ns", "beta")
	if err != nil {
		t.Fatalf("NewRedisRuntimeStateStore(beta): %v", err)
	}
	bridgeA := quotaRuntimeStateBridge{store: alpha, storeName: "alpha"}
	bridgeB := quotaRuntimeStateBridge{store: beta, storeName: "beta"}
	ctx := context.Background()

	if err := bridgeA.SetJSON(ctx, "key-1", map[string]any{"v": float64(7)}, time.Minute); err != nil {
		t.Fatalf("SetJSON: %v", err)
	}
	var dst map[string]any
	found, err := bridgeA.GetJSON(ctx, "key-1", &dst)
	if err != nil || !found || dst["v"] != float64(7) {
		t.Fatalf("GetJSON = (%v, %#v, %v)，want (true, v=7, nil)", found, dst, err)
	}
	// 不同 storeName 前缀隔离：beta 读不到 alpha 的键。
	var stray map[string]any
	if found, err := bridgeB.GetJSON(ctx, "key-1", &stray); err != nil || found {
		t.Fatalf("beta GetJSON = (%v, %v)，want (false, nil)", found, err)
	}
	if err := bridgeA.Delete(ctx, "key-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if found, err := bridgeA.GetJSON(ctx, "key-1", &dst); err != nil || found {
		t.Fatalf("删除后 GetJSON = (%v, %v)，want (false, nil)", found, err)
	}
	// 空键在三条通道都拒绝。
	if _, err := bridgeA.GetJSON(ctx, "  ", &dst); err == nil {
		t.Error("空白键 GetJSON 必须报错")
	}
	if err := bridgeA.SetJSON(ctx, "", map[string]any{}, time.Minute); err == nil {
		t.Error("空键 SetJSON 必须报错")
	}
	if err := bridgeA.Delete(ctx, "  "); err == nil {
		t.Error("空白键 Delete 必须报错")
	}
}

func TestW1URedisEvalClient(t *testing.T) {
	client := w1uSetupRedis(t)
	adapter := redisEvalClient{client: client}
	ctx := context.Background()
	got, err := adapter.Eval(ctx, "return {KEYS[1], ARGV[1]}", []string{"k1"}, "v1")
	if err != nil {
		t.Fatalf("Eval 数组脚本: %v", err)
	}
	if !reflect.DeepEqual(got, []any{"k1", "v1"}) {
		t.Fatalf("Eval 数组结果 = %#v，want [k1 v1]", got)
	}
	number, err := adapter.Eval(ctx, "return 1", nil)
	if err != nil || number != int64(1) {
		t.Fatalf("Eval 整数 = (%#v, %v)，want (int64(1), nil)", number, err)
	}

	// 上游不可达 → 原样透传错误。
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	dead := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = dead.Close() })
	mr.Close()
	if _, err := (redisEvalClient{client: dead}).Eval(ctx, "return 1", nil); err == nil {
		t.Error("Redis 不可用时 Eval 必须报错")
	}
}

func TestW1URedisStateClientProvider(t *testing.T) {
	var nilProvider redisStateClientProvider
	if _, err := nilProvider.Client(context.Background()); err == nil {
		t.Fatal("nil 客户端必须报错")
	} else if !strings.Contains(err.Error(), "不可用") {
		t.Errorf("nil 客户端错误 = %v，want 包含 不可用", err)
	}
	client := w1uSetupRedis(t)
	provider := redisStateClientProvider{client: client}
	got, err := provider.Client(context.Background())
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	evalClient, ok := got.(redisEvalClient)
	if !ok {
		t.Fatalf("Client = %T，want redisEvalClient", got)
	}
	if evalClient.client != client {
		t.Error("适配器必须携带同一 redis 客户端")
	}
	provider.Invalidate(context.Background(), got) // no-op，不 panic 即可。
}

// ---------------------------------------------------------------------------
// chain_runtime.go：chainRuntimeLogger / chainCircuitWaitLogger / chainRuntimeCacheLogger
// ---------------------------------------------------------------------------

func TestW1UChainRuntimeLoggers(t *testing.T) {
	var buf bytes.Buffer
	logger := chainRuntimeLogger{inner: w1uSlogText(&buf)}
	logger.Warn("evt_runtime", map[string]any{"accountId": "acc-1"}, "运行时警告")
	out := buf.String()
	if !strings.Contains(out, "event=evt_runtime") || !strings.Contains(out, "accountId=acc-1") || !strings.Contains(out, "运行时警告") {
		t.Fatalf("chainRuntimeLogger 输出 = %q，want 包含 event/accountId/消息", out)
	}

	buf.Reset()
	wait := chainCircuitWaitLogger{inner: w1uSlogText(&buf)}
	wait.Info(map[string]any{"phase": "wait"}, "等待开始")
	wait.Warn(map[string]any{"phase": "wait", "attempt": "2"}, "等待超时")
	out = buf.String()
	if !strings.Contains(out, "phase=wait") || !strings.Contains(out, "attempt=2") {
		t.Fatalf("chainCircuitWaitLogger 输出 = %q，want 包含字段", out)
	}
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "level=WARN") {
		t.Fatalf("chainCircuitWaitLogger 级别输出 = %q，want INFO 与 WARN", out)
	}
	if !strings.Contains(out, "等待开始") || !strings.Contains(out, "等待超时") {
		t.Fatalf("chainCircuitWaitLogger 消息输出 = %q", out)
	}

	buf.Reset()
	cache := chainRuntimeCacheLogger{inner: w1uSlogText(&buf)}
	cache.Warn("evt_cache", map[string]any{"op": "get"}, "缓存降级")
	out = buf.String()
	if !strings.Contains(out, "event=evt_cache") || !strings.Contains(out, "op=get") || !strings.Contains(out, "缓存降级") {
		t.Fatalf("chainRuntimeCacheLogger 输出 = %q，want 包含 event/op/消息", out)
	}
}

// ---------------------------------------------------------------------------
// chain_usage.go：usageModelAccountOf / headersAnyOf / requestModelHintOf
// ---------------------------------------------------------------------------

func TestW1UUsageModelAccountOfProjection(t *testing.T) {
	proxy := "http://127.0.0.1:7890"
	authzID, srcType, teamID := "az-1", "team", "team-1"
	groupAuthzID, groupSrcType, groupTeamID := "gz-1", "group", "team-2"
	account := gatewaydispatch.AccountCandidate{
		ID:                               "acc-1",
		Name:                             "账号一",
		ProviderCode:                     "openai",
		ProviderProtocolProfileID:        "ppp-1",
		ProtocolCode:                     "openai",
		ProtocolVersion:                  "v1",
		ProxyURL:                         &proxy,
		AccountOwnerSystemAccountID:      "sys-owner",
		GroupOwnerSystemAccountID:        "sys-group",
		AccountAccessType:                "personal",
		GroupAccessType:                  "team",
		AccountAuthorizationID:           &authzID,
		AccountAuthorizationSourceType:   &srcType,
		AccountAuthorizationSourceTeamID: &teamID,
		GroupAuthorizationID:             &groupAuthzID,
		GroupAuthorizationSourceType:     &groupSrcType,
		GroupAuthorizationSourceTeamID:   &groupTeamID,
	}
	want := gatewayusage.UsageModelAccount{
		ID:                        "acc-1",
		Name:                      "账号一",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "ppp-1",
		ProxyURL:                  "http://127.0.0.1:7890",
		UsageAccess: gatewayusage.UsageAccessFields{
			AccountOwnerSystemAccountID:      "sys-owner",
			GroupOwnerSystemAccountID:        "sys-group",
			AccountAccessType:                "personal",
			GroupAccessType:                  "team",
			AccountAuthorizationID:           "az-1",
			AccountAuthorizationSourceType:   "team",
			AccountAuthorizationSourceTeamID: "team-1",
			GroupAuthorizationID:             "gz-1",
			GroupAuthorizationSourceType:     "group",
			GroupAuthorizationSourceTeamID:   "team-2",
		},
		Profile: &gatewayusage.ProviderProtocolProfile{
			ProviderCode:    "openai",
			ProtocolCode:    "openai",
			ProtocolVersion: "v1",
			ProfileID:       "ppp-1",
		},
	}
	if got := usageModelAccountOf(account); !reflect.DeepEqual(got, want) {
		t.Fatalf("usageModelAccountOf = %+v，want %+v", got, want)
	}
	// ProxyURL 为 nil 时不投影（保持空串）。
	account.ProxyURL = nil
	got := usageModelAccountOf(account)
	if got.ProxyURL != "" {
		t.Fatalf("nil ProxyURL 投影 = %q，want 空串", got.ProxyURL)
	}
	want.ProxyURL = ""
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nil ProxyURL 完整投影 = %+v，want %+v", got, want)
	}
}

func TestW1UHeadersAnyOf(t *testing.T) {
	if got := headersAnyOf(nil); got != nil {
		t.Fatalf("headersAnyOf(nil) = %#v，want nil", got)
	}
	if got := headersAnyOf(map[string]string{}); got != nil {
		t.Fatalf("headersAnyOf(空) = %#v，want nil", got)
	}
	source := map[string]string{"x-a": "1", "x-b": "2"}
	got := headersAnyOf(source)
	if !reflect.DeepEqual(got, map[string]any{"x-a": "1", "x-b": "2"}) {
		t.Fatalf("headersAnyOf = %#v", got)
	}
	got["x-c"] = "3" // 返回副本：写结果不得泄漏回源 map。
	if _, leaked := source["x-c"]; leaked {
		t.Fatal("headersAnyOf 必须返回副本，不得共享底层 map")
	}
}

func TestW1URequestModelHintOf(t *testing.T) {
	if got := requestModelHintOf(nil); got != "" {
		t.Fatalf("requestModelHintOf(nil) = %q，want 空串", got)
	}
	if got := requestModelHintOf(&gatewaypreauth.GatewayRequest{}); got != "" {
		t.Fatalf("空请求 hint = %q，want 空串", got)
	}
	stateModel := "gpt-state"
	withState := &gatewaypreauth.GatewayRequest{Body: &gatewaybody.Request{
		State: &gatewaybody.BodyState{Model: &stateModel},
	}}
	if got := requestModelHintOf(withState); got != "gpt-state" {
		t.Fatalf("state 模型 hint = %q，want gpt-state", got)
	}
	withParsed := &gatewaypreauth.GatewayRequest{Body: &gatewaybody.Request{
		Body: map[string]any{"model": "gpt-parsed"},
	}}
	if got := requestModelHintOf(withParsed); got != "gpt-parsed" {
		t.Fatalf("解析体模型 hint = %q，want gpt-parsed", got)
	}
	blank := &gatewaypreauth.GatewayRequest{Body: &gatewaybody.Request{Body: map[string]any{"top_p": 0.9}}}
	if got := requestModelHintOf(blank); got != "" {
		t.Fatalf("无模型 hint = %q，want 空串", got)
	}
}

// ---------------------------------------------------------------------------
// chain_catalog.go：catalogBoolValue / normalizeCatalogValue / decorate 行派生
// ---------------------------------------------------------------------------

func TestW1UCatalogBoolValue(t *testing.T) {
	cases := []struct {
		in   any
		want any
	}{
		{float64(0), false},
		{float64(1), true},
		{float64(0.5), true},
		{int64(0), false},
		{int64(2), true},
		{int(0), false},
		{int(1), true},
		{nil, false},
		{"x", "x"},
		{true, true},
	}
	for _, tc := range cases {
		if got := catalogBoolValue(tc.in); got != tc.want {
			t.Errorf("catalogBoolValue(%#v) = %#v，want %#v", tc.in, got, tc.want)
		}
	}
}

func TestW1UNormalizeCatalogValue(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"字节 JSON 数组", []byte("[1,2]"), []any{float64(1), float64(2)}},
		{"字节 JSON 对象", []byte(`{"a":true}`), map[string]any{"a": true}},
		{"字节纯文本", []byte("plain"), "plain"},
		{"字节坏 JSON 回退文本", []byte("[broken"), "[broken"},
		{"字符串 JSON 数组", "[3]", []any{float64(3)}},
		{"字符串文本原样", " txt ", " txt "},
		{"字符串坏 JSON 回退", "{bad", "{bad"},
		{"数值透传", float64(1.5), float64(1.5)},
		{"nil 透传", nil, nil},
		{"整数透传", int64(7), int64(7)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeCatalogValue(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("normalizeCatalogValue(%#v) = %#v，want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestW1UCatalogDecorateRows(t *testing.T) {
	row := map[string]any{"supportedServiceTiers": []any{"auto", "flex"}}
	decorateBuiltinCatalogRow(row)
	if row["scope"] != "built_in" || row["supportsServiceTier"] != true {
		t.Fatalf("builtin 行派生 = %#v，want scope=built_in 且 supportsServiceTier=true", row)
	}
	row = map[string]any{"supportedServiceTiers": []any{}}
	decorateBuiltinCatalogRow(row)
	if row["supportsServiceTier"] != false {
		t.Fatalf("空 tier 列表 supportsServiceTier = %v，want false", row["supportsServiceTier"])
	}
	row = map[string]any{}
	decorateBuiltinCatalogRow(row)
	if row["supportsServiceTier"] != false {
		t.Fatalf("缺 tier 键 supportsServiceTier = %v，want false", row["supportsServiceTier"])
	}

	custom := map[string]any{"scope": "global", "supportedServiceTiers": []any{"auto"}}
	decorateCustomCatalogRow(custom)
	if custom["source"] != "custom-global" || custom["supportsPromptCaching"] != false || custom["supportsServiceTier"] != true {
		t.Fatalf("custom global 行派生 = %#v", custom)
	}
	custom = map[string]any{"scope": "personal", "cachedInputUsdPer1M": nil}
	decorateCustomCatalogRow(custom)
	if custom["source"] != "custom-personal" || custom["supportsPromptCaching"] != true || custom["supportsServiceTier"] != false {
		t.Fatalf("custom personal 行派生（键存在即缓存，nil 值也算）= %#v", custom)
	}
}

// w1uSlogText 构造写入缓冲区的文本 slog，用于断言日志桥输出。
func w1uSlogText(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

// ---------------------------------------------------------------------------
// chain_request_failure_health.go：chainRequestDispatchMarks / EnqueueProbeRequest fence
// ---------------------------------------------------------------------------

func TestW1UChainRequestDispatchMarks(t *testing.T) {
	req1 := &gatewaypreauth.GatewayRequest{}
	req2 := &gatewaypreauth.GatewayRequest{}
	req3 := &gatewaypreauth.GatewayRequest{}
	req4 := &gatewaypreauth.GatewayRequest{}

	// nil 请求安全。
	m := newChainRequestDispatchMarks(2)
	if m.marked(nil) {
		t.Error("marked(nil) 必须为 false")
	}
	m.mark(nil) // 不 panic。

	// 容量 2：mark(a) 重复标记不改变 FIFO 位置，随后 b、c 入队应逐出 a。
	m.mark(req1)
	if !m.marked(req1) {
		t.Fatal("mark 后必须 marked")
	}
	m.mark(req1) // 重复标记。
	m.mark(req2)
	if !m.marked(req2) {
		t.Fatal("mark(req2) 后必须 marked")
	}
	m.mark(req3) // 触发逐出最老的 req1（重复标记不得刷新位置）。
	if m.marked(req1) {
		t.Error("容量逐出后 req1 必须 unmarked（重复 mark 不得刷新 FIFO 位置）")
	}
	if !m.marked(req2) || !m.marked(req3) {
		t.Error("req2/req3 必须保持 marked")
	}
	m.mark(req4) // 逐出 req2。
	if m.marked(req2) {
		t.Error("req2 被逐出后必须 unmarked")
	}
	if !m.marked(req3) || !m.marked(req4) {
		t.Error("req3/req4 必须保持 marked")
	}
}

// EnqueueProbeRequest 的 queued/nil-fence/inert/空账户路径已由
// chain_failure_health_wiring_test.go 覆盖；这里补 source fence 投影与
// 账户/reason/trace 归一化。
func TestW1UEnqueueProbeRequestSourceFence(t *testing.T) {
	db := w1uOpenPlainSQLite(t, "outbox-fence.sqlite3")
	writer := newChainProbeRequestOutboxWriter(db, false, 30_000)
	fence := &chainHealthDispatchSourceFence{
		StateKey:         "sk-w1u",
		AccountID:        "acc-9",
		SourceGeneration: 7,
		SourceFenceID:    "fence-1",
		RuntimeKey:       "rk-w1u",
		ProbeGeneration:  2,
		ConfigRevision:   5,
	}
	outcome := writer.EnqueueProbeRequest(context.Background(), " acc-9 ", "  request_failure  ", " trace-9 ", fence)
	if outcome.Outcome != gatewaycodex.HealthDispatchQueued || outcome.DecisionCode != "queued" || outcome.TargetRole != "go-jobs" {
		t.Fatalf("fence 派发结果 = %#v，want queued/go-jobs", outcome)
	}
	var accountID, reason, traceID, sourceFence, status string
	err := db.QueryRow(`SELECT account_id, reason, trace_id, source_fence, status
		FROM account_health_probe_request_outbox ORDER BY created_at DESC LIMIT 1`).
		Scan(&accountID, &reason, &traceID, &sourceFence, &status)
	if err != nil {
		t.Fatalf("读取 outbox 行: %v", err)
	}
	if accountID != "acc-9" || reason != "request_failure" || traceID != "trace-9" || status != "pending" {
		t.Fatalf("行投影 = (%q, %q, %q, %q)，want 归一化后的 acc-9/request_failure/trace-9/pending", accountID, reason, traceID, status)
	}
	var decoded chainHealthDispatchSourceFence
	if err := json.Unmarshal([]byte(sourceFence), &decoded); err != nil {
		t.Fatalf("source_fence 不是 JSON: %v (%q)", err, sourceFence)
	}
	if !reflect.DeepEqual(decoded, *fence) {
		t.Fatalf("source_fence 投影 = %+v，want %+v", decoded, *fence)
	}
}

// ---------------------------------------------------------------------------
// chain_chat.go：chatAssetObjectStore / newChatTokenCount
// ---------------------------------------------------------------------------

func TestW1UChatAssetObjectStorePathGuard(t *testing.T) {
	if _, err := newChatAssetObjectStore("   "); err == nil {
		t.Fatal("空白根目录必须报错")
	} else if !strings.Contains(err.Error(), "JUHE_AI_CHAT_ASSETS_ROOT") {
		t.Errorf("空白根目录错误 = %v，want 提示 JUHE_AI_CHAT_ASSETS_ROOT", err)
	}
	root := filepath.Join(t.TempDir(), "assets")
	store, err := newChatAssetObjectStore(root)
	if err != nil {
		t.Fatalf("newChatAssetObjectStore: %v", err)
	}
	got, err := store.path("conv-1/ast-1.png")
	if err != nil {
		t.Fatalf("合法键 path: %v", err)
	}
	if want := filepath.Join(root, "conv-1", "ast-1.png"); got != want {
		t.Fatalf("path = %q，want %q", got, want)
	}
	// Clean 吸收前导 .. 后仍残留 ".." 子串的键被拒绝。
	for _, bad := range []string{"..hidden", "conv/..hidden", "a/..b/c"} {
		if _, err := store.path(bad); err == nil {
			t.Errorf("键 %q 必须因包含 .. 被拒绝", bad)
		} else if !strings.Contains(err.Error(), "不合法") {
			t.Errorf("键 %q 错误 = %v，want 包含 不合法", bad, err)
		}
	}
	// 前导 "/" 锚定后 ".." 被 Clean 吸收：限定在根目录内（契约行为锁定的
	// 归一化结果，不是逃逸）。
	got, err = store.path("../../outside.png")
	if err != nil {
		t.Fatalf("Clean 吸收后的 .. 键: %v", err)
	}
	if want := filepath.Join(root, "outside.png"); got != want {
		t.Fatalf(".. 吸收 path = %q，want %q", got, want)
	}
}

func TestW1UChatAssetObjectStoreLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assets")
	store, err := newChatAssetObjectStore(root)
	if err != nil {
		t.Fatalf("newChatAssetObjectStore: %v", err)
	}
	payload := []byte("w1u-asset-payload")
	sum := chatAssetSHA256Hex(payload)

	if err := store.Write("conv/asset.bin", payload, 1<<20, sum); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := store.Write("conv/asset.bin", payload, 1<<20, strings.Repeat("0", 64)); err == nil {
		t.Fatal("SHA 不匹配必须报错")
	} else if !strings.Contains(err.Error(), "校验失败") {
		t.Errorf("SHA 错误 = %v，want 包含 校验失败", err)
	}
	if err := store.Write("conv/asset.bin", payload, 4, sum); err == nil {
		t.Fatal("超过大小上限必须报错")
	} else if !strings.Contains(err.Error(), "大小上限") {
		t.Errorf("超限错误 = %v，want 包含 大小上限", err)
	}

	data, size, err := store.Open("conv/asset.bin", 1<<20)
	if err != nil || size != int64(len(payload)) || string(data) != string(payload) {
		t.Fatalf("Open = (%q, %d, %v)，want 原文回读", data, size, err)
	}
	if _, _, err := store.Open("conv/asset.bin", 4); err == nil {
		t.Fatal("Open 超限必须报错")
	}
	if _, _, err := store.Open("conv/missing.bin", 1<<20); err == nil {
		t.Fatal("缺失文件 Open 必须报错")
	}

	if err := store.Delete([]string{"", "  ", "conv/missing.bin"}); err != nil {
		t.Fatalf("Delete 忽略空键与缺失文件 = %v，want nil", err)
	}
	if err := store.Delete([]string{"conv/asset.bin"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := store.Open("conv/asset.bin", 1<<20); err == nil {
		t.Fatal("删除后 Open 必须报错")
	}
	// 非法键在 Delete 中保留首个错误。
	if err := store.Delete([]string{"..bad"}); err == nil {
		t.Fatal("Delete 非法键必须报错")
	}
}

func TestW1UNewChatTokenCount(t *testing.T) {
	count, err := newChatTokenCount()
	if err != nil {
		t.Fatalf("newChatTokenCount: %v", err)
	}
	if got := count(""); got != 0 {
		t.Errorf("count(\"\") = %d，want 0", got)
	}
	first := count("hello, w1u token world")
	if first <= 0 {
		t.Fatalf("count(文本) = %d，want > 0", first)
	}
	if again := count("hello, w1u token world"); again != first {
		t.Fatalf("count 必须确定可重放：%d 与 %d 不一致", first, again)
	}
}

// ---------------------------------------------------------------------------
// chain_chat_observation.go：buildAssetDataURL 与纯解析助手
// ---------------------------------------------------------------------------

const w1uChatAssetTimeLayout = "2006-01-02T15:04:05.000000000Z"

func w1uCreateChatAssetsTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS chat_assets (
		id TEXT PRIMARY KEY,
		system_account_id TEXT NOT NULL,
		conversation_id TEXT NOT NULL,
		processing_status TEXT NOT NULL,
		cleanup_status TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		storage_key TEXT NOT NULL,
		processed_mime_type TEXT NOT NULL,
		processed_bytes INTEGER NOT NULL,
		processed_sha256 TEXT NOT NULL,
		source_kind TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("建最小 chat_assets 表: %v", err)
	}
}

func w1uInsertChatAssetRow(t *testing.T, db *sql.DB, id, expiresAt, sourceKind string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO chat_assets
		(id, system_account_id, conversation_id, processing_status, cleanup_status, expires_at,
		 storage_key, processed_mime_type, processed_bytes, processed_sha256, source_kind)
		VALUES (?, 'sys-1', 'conv-1', 'ready', 'active', ?, 'conv-1/asset.png', 'image/png', ?, ?, ?)`,
		id, expiresAt, 0, "", sourceKind); err != nil {
		t.Fatalf("插入 chat_assets 行: %v", err)
	}
}

// buildAssetDataURL 走最小 chat_assets 表 + 临时目录对象存储，覆盖就绪回链、
// 大小不符、完整性不符、无可消费行四个分支。
func TestW1UBuildAssetDataURL(t *testing.T) {
	db := w1uOpenPlainSQLite(t, "chat-assets.sqlite3")
	w1uCreateChatAssetsTable(t, db)
	store, err := newChatAssetObjectStore(filepath.Join(t.TempDir(), "assets"))
	if err != nil {
		t.Fatalf("newChatAssetObjectStore: %v", err)
	}
	obs := newChatImageObservations(db, false, store, nil)
	base := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	now := base.Format(w1uChatAssetTimeLayout)
	future := base.Add(time.Hour).Format(w1uChatAssetTimeLayout)
	past := base.Add(-time.Hour).Format(w1uChatAssetTimeLayout)
	ctx := context.Background()

	payload := []byte("w1u-image-bytes")
	sha := chatAssetSHA256Hex(payload)
	if err := store.Write("conv-1/asset.png", payload, 1<<20, sha); err != nil {
		t.Fatalf("写入对象: %v", err)
	}
	w1uInsertChatAssetRow(t, db, "ast-ok", future, "user_uploaded")
	if _, err := db.Exec(`UPDATE chat_assets SET processed_bytes = ?, processed_sha256 = ? WHERE id = 'ast-ok'`,
		len(payload), sha); err != nil {
		t.Fatalf("回填行: %v", err)
	}

	got, err := obs.buildAssetDataURL(ctx, "ast-ok", "conv-1", "sys-1", now)
	if err != nil {
		t.Fatalf("就绪资产回链: %v", err)
	}
	want := "data:image/png;base64," + chatAssetBase64(payload)
	if got != want {
		t.Fatalf("data URL = %q，want %q", got, want)
	}

	// 大小校验失败：存储对象与行记录字节数不一致。
	if err := store.Write("conv-1/asset.png", append(payload, '!'), 1<<20, chatAssetSHA256Hex(append(payload, '!'))); err != nil {
		t.Fatalf("覆写对象: %v", err)
	}
	if _, err := obs.buildAssetDataURL(ctx, "ast-ok", "conv-1", "sys-1", now); err == nil ||
		!strings.Contains(err.Error(), "大小校验失败") {
		t.Fatalf("大小不符错误 = %v，want 包含 大小校验失败", err)
	}

	// 完整性校验失败：同尺寸不同内容。
	tampered := []byte("w1u-image-bytex")
	if err := store.Write("conv-1/asset.png", tampered, 1<<20, chatAssetSHA256Hex(tampered)); err != nil {
		t.Fatalf("覆写对象: %v", err)
	}
	if _, err := obs.buildAssetDataURL(ctx, "ast-ok", "conv-1", "sys-1", now); err == nil ||
		!strings.Contains(err.Error(), "完整性校验失败") {
		t.Fatalf("完整性不符错误 = %v，want 包含 完整性校验失败", err)
	}

	// 无可消费行：不存在 / 已过期 都返回 ("", nil)。
	if got, err := obs.buildAssetDataURL(ctx, "ast-missing", "conv-1", "sys-1", now); err != nil || got != "" {
		t.Fatalf("缺失资产 = (%q, %v)，want (\"\", nil)", got, err)
	}
	w1uInsertChatAssetRow(t, db, "ast-expired", past, "user_uploaded")
	if _, err := db.Exec(`UPDATE chat_assets SET processed_bytes = ?, processed_sha256 = ? WHERE id = 'ast-expired'`,
		len(tampered), chatAssetSHA256Hex(tampered)); err != nil {
		t.Fatalf("回填过期行: %v", err)
	}
	if got, err := obs.buildAssetDataURL(ctx, "ast-expired", "conv-1", "sys-1", now); err != nil || got != "" {
		t.Fatalf("过期资产 = (%q, %v)，want (\"\", nil)", got, err)
	}
}

func TestW1UChatResponsesOutputText(t *testing.T) {
	if got := chatResponsesOutputText(map[string]any{"output_text": "直接文本"}); got != "直接文本" {
		t.Fatalf("output_text 快捷路径 = %q", got)
	}
	payload := map[string]any{"output": []any{
		map[string]any{"content": []any{
			map[string]any{"text": "a"},
			map[string]any{"text": "b"},
			"非对象跳过",
			map[string]any{"text": ""},
			map[string]any{"other": "x"},
		}},
		"非对象项跳过",
		map[string]any{"content": "非数组跳过"},
	}}
	if got := chatResponsesOutputText(payload); got != "a\nb" {
		t.Fatalf("output 遍历 = %q，want \"a\\nb\"", got)
	}
	if got := chatResponsesOutputText(map[string]any{}); got != "" {
		t.Fatalf("空载荷 = %q，want 空串", got)
	}
}

func TestW1UParseChatImageObservation(t *testing.T) {
	parsed, err := parseChatImageObservation(`{"summary":" 摘要 ","ocr":["o1"],"objects":[],"questionRelevantFacts":["f1"],"uncertainties":["u1"]}`)
	if err != nil {
		t.Fatalf("解析 JSON: %v", err)
	}
	if parsed["summary"] != "摘要" {
		t.Errorf("summary = %#v，want 去空白摘要", parsed["summary"])
	}
	if !reflect.DeepEqual(parsed["ocr"], []string{"o1"}) ||
		!reflect.DeepEqual(parsed["objects"], []string{}) ||
		!reflect.DeepEqual(parsed["questionRelevantFacts"], []string{"f1"}) ||
		!reflect.DeepEqual(parsed["uncertainties"], []string{"u1"}) {
		t.Fatalf("数组字段 = %#v", parsed)
	}

	fenced, err := parseChatImageObservation("```json\n{\"summary\":\"围栏\"}\n```")
	if err != nil || fenced["summary"] != "围栏" {
		t.Fatalf("```json 围栏 = (%#v, %v)", fenced, err)
	}
	fallback, err := parseChatImageObservation("  普通文本描述  ")
	if err != nil || fallback["summary"] != "普通文本描述" {
		t.Fatalf("非 JSON 回退 = (%#v, %v)", fallback, err)
	}
	nonObject, err := parseChatImageObservation("[1,2]")
	if err != nil || nonObject["summary"] != "[1,2]" {
		t.Fatalf("JSON 非对象回退 = (%#v, %v)，want summary [1,2]", nonObject, err)
	}
	if _, err := parseChatImageObservation(`{"summary":"  "}`); err == nil || err.Error() != "chat_image_observation_empty" {
		t.Fatalf("空 summary 错误 = %v，want chat_image_observation_empty", err)
	}
	if _, err := parseChatImageObservation(`{"summary":42}`); err == nil {
		t.Fatal("非字符串 summary 必须按空处理并报错")
	}
	long := strings.Repeat("中", 12_001)
	truncated, err := parseChatImageObservation(`{"summary":"` + long + `"}`)
	if err != nil {
		t.Fatalf("超长 summary 解析: %v", err)
	}
	if got, ok := truncated["summary"].(string); !ok || len([]rune(got)) != 12_000 {
		t.Fatalf("超长 summary 截断长度 = %d，want 12000", len([]rune(got)))
	}
}

func TestW1UChatObservationHelpers(t *testing.T) {
	if got := chatObservationText("  x  ", 10); got != "x" {
		t.Errorf("chatObservationText 去空白 = %q，want x", got)
	}
	if got := chatObservationText(7, 10); got != "" {
		t.Errorf("chatObservationText 非字符串 = %q，want 空串", got)
	}
	if got := chatObservationText("中文字符", 2); got != "中文" {
		t.Errorf("chatObservationText 截断 = %q，want 中文", got)
	}
	if got := chatObservationTextArray(nil); len(got) != 0 {
		t.Errorf("chatObservationTextArray(nil) = %#v，want 空切片", got)
	}
	if got := chatObservationTextArray([]any{"a", 1, "  ", "", "b"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("chatObservationTextArray 过滤 = %#v，want [a b]", got)
	}
	many := make([]any, 0, 150)
	for i := 0; i < 150; i++ {
		many = append(many, "x")
	}
	if got := chatObservationTextArray(many); len(got) != 100 {
		t.Errorf("chatObservationTextArray 上限 = %d，want 100", len(got))
	}
	if got := truncateChatObservationText("abcd", 2); got != "ab" {
		t.Errorf("truncateChatObservationText = %q，want ab", got)
	}
	if got := truncateChatObservationText("中文abc", 3); got != "中文a" {
		t.Errorf("truncateChatObservationText 按字节安全截断 = %q，want 中文a", got)
	}
	if got := truncateChatObservationText("abc", 5); got != "abc" {
		t.Errorf("truncateChatObservationText 不截断 = %q，want abc", got)
	}
	if got := chatObservationJSONString(`a"b`); got != "\"a\\\"b\"" {
		t.Errorf("chatObservationJSONString = %q，want \"a\\\"b\"", got)
	}
	if _, _, err := chatAssetObjectRead(nil, "k", 10); err == nil || !strings.Contains(err.Error(), "未接线") {
		t.Errorf("chatAssetObjectRead(nil) 错误 = %v，want 包含 未接线", err)
	}
	if got := chatAssetSHA256Hex([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("chatAssetSHA256Hex(abc) = %s", got)
	}
	if got := chatAssetBase64([]byte("abc")); got != "YWJj" {
		t.Errorf("chatAssetBase64(abc) = %s，want YWJj", got)
	}
}

// ---------------------------------------------------------------------------
// compose_account_balance_refresh.go：协议档案与目录/探针纯逻辑
// ---------------------------------------------------------------------------

func TestW1UProviderModelSupportsProtocolProfile(t *testing.T) {
	cases := []struct {
		name           string
		modelProtocols []string
		providerCode   string
		protocolCode   string
		want           bool
	}{
		{"空协议清单放行", nil, "vendor", "openai", true},
		{"gpt 供应商放行", []string{"messages"}, " GPT ", "openai", true},
		{"openai 档案命中", []string{"chat_completions"}, "vendor", "openai", true},
		{"openai 档案未命中", []string{"messages"}, "vendor", "openai", false},
		{"anthropic 档案命中", []string{"messages"}, "vendor", "anthropic", true},
		{"gemini 档案命中", []string{"count_tokens"}, "vendor", "gemini", true},
		{"未知档案拒绝", []string{"chat_completions"}, "vendor", "unknown", false},
		{"空白档案拒绝", []string{"messages"}, "vendor", "   ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerModelSupportsProtocolProfile(tc.modelProtocols, tc.providerCode, tc.protocolCode); got != tc.want {
				t.Fatalf("providerModelSupportsProtocolProfile = %v，want %v", got, tc.want)
			}
		})
	}
}

func TestW1UDefaultProtocolFamilies(t *testing.T) {
	openai := defaultProtocolFamilies(" OpenAI ")
	if !openai["chat_completions"] || !openai["responses"] || len(openai) != 2 {
		t.Fatalf("openai families = %#v", openai)
	}
	anthropic := defaultProtocolFamilies("anthropic")
	if !anthropic["messages"] || len(anthropic) != 1 {
		t.Fatalf("anthropic families = %#v", anthropic)
	}
	gemini := defaultProtocolFamilies("GEMINI")
	for _, family := range []string{"generate_content", "stream_generate_content", "count_tokens", "embed_content", "interactions"} {
		if !gemini[family] {
			t.Fatalf("gemini families 缺少 %s：%#v", family, gemini)
		}
	}
	if len(gemini) != 5 {
		t.Fatalf("gemini families = %#v，want 恰好 5 项", gemini)
	}
	if got := defaultProtocolFamilies("nope"); len(got) != 0 {
		t.Fatalf("未知协议 families = %#v，want 空", got)
	}
}

func TestW1UAccountModelCatalogAdditions(t *testing.T) {
	localModels := []providers.ModelCatalogItem{
		{Model: "m-add", SupportedAPIProtocols: []string{"chat_completions"}},
		{Model: "m-selected", SupportedAPIProtocols: []string{"chat_completions"}},
		{Model: "m-mismatch", SupportedAPIProtocols: []string{"messages"}},
		{Model: "m-not-upstream", SupportedAPIProtocols: []string{"chat_completions"}},
		{Model: "  m-add  "},
		{Model: "   "},
		{Model: "m-no-protocols"},
	}
	upstream := map[string]bool{
		"m-add": true, "m-selected": true, "m-mismatch": true,
		"m-no-protocols": true,
	}
	got := accountModelCatalogAdditions([]string{" m-selected "}, upstream, localModels, "vendor", "openai")
	want := []string{"m-add", "m-no-protocols"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("accountModelCatalogAdditions = %#v，want %#v", got, want)
	}
	if empty := accountModelCatalogAdditions(nil, map[string]bool{}, nil, "vendor", "openai"); len(empty) != 0 {
		t.Fatalf("空输入 = %#v，want 空切片", empty)
	}
}

func TestW1URecommendedAccountHealthCheckModel(t *testing.T) {
	testModels := []providers.ModelCatalogItem{
		{Model: "t1", SupportedAPIProtocols: []string{"chat_completions"}},
		{Model: "t2", SupportedAPIProtocols: []string{"chat_completions"}},
		{Model: "t3", SupportedAPIProtocols: []string{"messages"}},
	}
	upstream := map[string]bool{"t1": true, "t2": true, "t3": true}
	if got := recommendedAccountHealthCheckModel("t2", upstream, testModels, "vendor", "openai"); got != "t2" {
		t.Errorf("已配置候选保持 = %q，want t2", got)
	}
	if got := recommendedAccountHealthCheckModel("t3", upstream, testModels, "vendor", "openai"); got != "t1" {
		t.Errorf("配置不在候选取首项 = %q，want t1", got)
	}
	if got := recommendedAccountHealthCheckModel("   ", upstream, testModels, "vendor", "openai"); got != "t1" {
		t.Errorf("空白配置取首项 = %q，want t1", got)
	}
	if got := recommendedAccountHealthCheckModel("t1", map[string]bool{}, testModels, "vendor", "openai"); got != "" {
		t.Errorf("无候选返回空 = %q，want 空串", got)
	}
	if got := recommendedAccountHealthCheckModel("", upstream, testModels, "vendor", "anthropic"); got != "t3" {
		t.Errorf("anthropic 档案候选 = %q，want t3", got)
	}
}

func TestW1USnapshotToMapAndTextHelpers(t *testing.T) {
	snapshot := accountbalance.Snapshot{
		RemainingUSD:              "12.5",
		RawRemaining:              "1250",
		ConsecutiveTransientFails: 3,
	}
	payload, err := snapshotToMap(snapshot)
	if err != nil {
		t.Fatalf("snapshotToMap: %v", err)
	}
	if payload["remainingUsd"] != "12.5" || payload["rawRemaining"] != "1250" || payload["consecutiveTransientFailures"] != float64(3) {
		t.Fatalf("snapshotToMap = %#v", payload)
	}
	if got := textFromCredentials("tok-1"); got != "tok-1" {
		t.Errorf("textFromCredentials 字符串 = %q", got)
	}
	if got := textFromCredentials(42); got != "" {
		t.Errorf("textFromCredentials 非字符串 = %q，want 空串", got)
	}
	if got := textFromCredentials(nil); got != "" {
		t.Errorf("textFromCredentials(nil) = %q，want 空串", got)
	}
	value := "指针文本"
	if got := derefText(&value); got != "指针文本" {
		t.Errorf("derefText = %q", got)
	}
	if got := derefText(nil); got != "" {
		t.Errorf("derefText(nil) = %q，want 空串", got)
	}
}

// ---------------------------------------------------------------------------
// chain_accounts_traffic_migration.go：scope 映射 + 迁移桥（内存驱动）
// ---------------------------------------------------------------------------

func TestW1UTrafficMigrationScopeMapping(t *testing.T) {
	if got := trafficMigrationScope(nil); got != nil {
		t.Fatalf("trafficMigrationScope(nil) = %v，want nil", got)
	}
	scope := &accounts.TrafficMigrationScope{SystemAccountID: "sys-9", GroupID: "g-9"}
	got := trafficMigrationScope(scope)
	want := &gatewaysession.OpenAIGatewaySessionAffinityScope{SystemAccountID: "sys-9", GroupID: "g-9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trafficMigrationScope = %+v，want %+v", got, want)
	}
}

// trafficRuntimeMigratorBridge 走 gatewaysession 内存驱动（CacheDriver 非redis
// 时迁移在进程内索引完成），通过 Remember 预置会话绑定验证迁移计数。
func TestW1UTrafficRuntimeMigratorBridge(t *testing.T) {
	svc, err := gatewaysession.NewAffinityService(gatewaysession.AffinityConfig{Secret: "w1u-affinity-secret"})
	if err != nil {
		t.Fatalf("NewAffinityService: %v", err)
	}
	svc.RememberOpenAIAccountForSession("sess-w1u-1", "acc-src", nil)
	bridge := trafficRuntimeMigratorBridge{affinity: svc}
	ctx := context.Background()

	count, err := bridge.MigrateOpenAIAccountTrafficRuntime(ctx, accounts.TrafficRuntimeMigrationInput{
		SourceAccountID:        "acc-src",
		TargetAccountID:        "acc-dst",
		PreferMigratedSessions: true,
	})
	if err != nil || count != 1 {
		t.Fatalf("迁移计数 = (%d, %v)，want (1, nil)", count, err)
	}
	// 同账户迁移直接零值返回。
	if count, err := bridge.MigrateOpenAIAccountTrafficRuntime(ctx, accounts.TrafficRuntimeMigrationInput{
		SourceAccountID: "acc-src",
		TargetAccountID: "acc-src",
	}); err != nil || count != 0 {
		t.Fatalf("同账户迁移 = (%d, %v)，want (0, nil)", count, err)
	}
	// 无绑定的全新服务 → 0 且无错误（preference 写为 nil scope no-op）。
	fresh, err := gatewaysession.NewAffinityService(gatewaysession.AffinityConfig{Secret: "w1u-affinity-secret"})
	if err != nil {
		t.Fatalf("NewAffinityService(fresh): %v", err)
	}
	if count, err := (trafficRuntimeMigratorBridge{affinity: fresh}).MigrateOpenAIAccountTrafficRuntime(ctx, accounts.TrafficRuntimeMigrationInput{
		SourceAccountID: "acc-x",
		TargetAccountID: "acc-y",
		AffinityScope:   &accounts.TrafficMigrationScope{SystemAccountID: "sys-1", GroupID: "g-1"},
		PreferenceScope: &accounts.TrafficMigrationScope{SystemAccountID: "sys-1", GroupID: "g-1"},
	}); err != nil || count != 0 {
		t.Fatalf("无绑定迁移 = (%d, %v)，want (0, nil)", count, err)
	}
}

// ---------------------------------------------------------------------------
// chain_apikey_rotation_redis.go：NextIndex 剩余错误分支
// （happy path / 键名 / TTL / nil 构造已由 chain_account_locks_test.go 覆盖）
// ---------------------------------------------------------------------------

func TestW1UChainAPIKeyRotationCounterRemainingBranches(t *testing.T) {
	ctx := context.Background()
	var nilCounter *chainAPIKeyRotationRedisCounter
	// modulo 守卫先于 nil 接收者检查：nil 适配器 + modulo<=0 仍返回 (0, nil)。
	if got, err := nilCounter.NextIndex(ctx, "acc", "round-robin", 0); err != nil || got != 0 {
		t.Fatalf("nil 接收者 modulo 0 = (%d, %v)，want (0, nil)", got, err)
	}
	if _, err := nilCounter.NextIndex(ctx, "acc", "round-robin", 3); err == nil {
		t.Fatal("nil 接收者必须报错")
	} else if !strings.Contains(err.Error(), "缺少客户端") {
		t.Errorf("nil 接收者错误 = %v，want 包含 缺少客户端", err)
	}
	// 结构非 nil 但客户端字段为 nil。
	empty := &chainAPIKeyRotationRedisCounter{}
	if _, err := empty.NextIndex(ctx, "acc", "round-robin", 3); err == nil {
		t.Fatal("nil 客户端字段必须报错")
	}
	// 上游不可达 → eval 错误透传。
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	dead := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = dead.Close() })
	mr.Close()
	if _, err := (&chainAPIKeyRotationRedisCounter{client: dead}).NextIndex(ctx, "acc", "round-robin", 3); err == nil {
		t.Fatal("Redis 不可用时 NextIndex 必须报错")
	}
	// 存活客户端的基线返回（与既有测试不同账户，验证剩余分支仍走正常脚本）。
	live := w1uSetupRedis(t)
	liveCounter := &chainAPIKeyRotationRedisCounter{client: live}
	if got, err := liveCounter.NextIndex(ctx, "acc-w1u-rest", "weighted", 5); err != nil || got != 0 {
		t.Fatalf("存活客户端 NextIndex = (%d, %v)，want (0, nil)", got, err)
	}
}
