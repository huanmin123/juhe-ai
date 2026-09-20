package schema

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖 seed_shared.go / seed helpers 的值级契约（与 Node crypto.ts /
// JSON 语义逐值对齐），以及 SQLite seed 的陈旧模型回收分支。

func TestWMSeedKeyPrefixSuffixBoundaries(t *testing.T) {
	// Node key.slice(0,8)/slice(-8)：长值取前后 8 位，短值原样返回。
	if got := seedKeyPrefix("sk-1234567890abcdef"); got != "sk-12345" {
		t.Fatalf("seedKeyPrefix 长值: %q", got)
	}
	if got := seedKeyPrefix("short"); got != "short" {
		t.Fatalf("seedKeyPrefix 短值应原样: %q", got)
	}
	if got := seedKeySuffix("sk-1234567890abcdef"); got != "90abcdef" {
		t.Fatalf("seedKeySuffix 长值: %q", got)
	}
	if got := seedKeySuffix("abc"); got != "abc" {
		t.Fatalf("seedKeySuffix 短值应原样: %q", got)
	}
	if got := seedKeyPrefix("12345678"); got != "12345678" {
		t.Fatalf("恰好 8 位不截断: %q", got)
	}
}

func TestWMSeedReplacePrefixSuffixAnchored(t *testing.T) {
	// 锚定替换：前缀/后缀不匹配时原样返回（Node replace 仅替换一次锚定位）。
	if got := seedReplacePrefix("grp_default_gpt_x", "grp_", "route_strategy_"); got != "route_strategy_default_gpt_x" {
		t.Fatalf("前缀替换: %q", got)
	}
	if got := seedReplacePrefix("other_gpt", "grp_", "route_strategy_"); got != "other_gpt" {
		t.Fatalf("前缀不匹配应原样: %q", got)
	}
	if got := seedReplaceSuffix("GPT 默认分组", "分组", "路由"); got != "GPT 默认路由" {
		t.Fatalf("后缀替换: %q", got)
	}
	if got := seedReplaceSuffix("分组在中部", "分组", "路由"); got != "分组在中部" {
		t.Fatalf("后缀不匹配应原样: %q", got)
	}
	// 派生 ID 链：grp_ -> route_strategy_ -> key_default_。
	groupID := "grp_default_gpt_abcdef"
	if got := defaultRouteStrategyIDForGroup(groupID); got != "route_strategy_default_gpt_abcdef" {
		t.Fatalf("默认路由策略 ID 派生: %q", got)
	}
	if got := defaultRouteStrategyGroupBindingIDForGroup(groupID); got != "rsg_default_gpt_abcdef" {
		t.Fatalf("默认绑定 ID 派生: %q", got)
	}
	if got := defaultAPIKeyIDForRouteStrategy("route_strategy_default_gpt_abcdef"); got != "key_default_default_gpt_abcdef" {
		t.Fatalf("默认 API Key ID 派生: %q", got)
	}
	if got := defaultRouteStrategyNameForGroup("GPT 默认分组"); got != "GPT 默认路由" {
		t.Fatalf("默认路由策略名派生: %q", got)
	}
	if got := defaultAPIKeyNameForRouteStrategy("GPT 默认路由"); got != "GPT 默认API Key" {
		t.Fatalf("默认 API Key 名派生: %q", got)
	}
}

func TestWMSeedNullableAndBoolProjections(t *testing.T) {
	text := "x"
	var nilText *string
	if seedNullableString(&text) != "x" || seedNullableString(nilText) != nil {
		t.Fatal("seedNullableString 指针投影错误")
	}
	number := int64(7)
	var nilNumber *int64
	if seedNullableInt64(&number) != int64(7) || seedNullableInt64(nilNumber) != nil {
		t.Fatal("seedNullableInt64 指针投影错误")
	}
	ratio := 0.5
	var nilRatio *float64
	if seedNullableFloat64(&ratio) != 0.5 || seedNullableFloat64(nilRatio) != nil {
		t.Fatal("seedNullableFloat64 指针投影错误")
	}
	if seedBoolInt(true) != 1 || seedBoolInt(false) != 0 {
		t.Fatal("seedBoolInt 应映射 1/0")
	}
	if pgNullableText("") != nil || pgNullableText("gpt") != "gpt" {
		t.Fatal("pgNullableText 空串必须映射 NULL")
	}
}

func TestWMSeedTimestampAndSecretResolution(t *testing.T) {
	clock := time.Date(2026, 9, 4, 8, 0, 0, 123456000, time.UTC)
	if got := seedTimestamp(clock); got != "2026-09-04T08:00:00.123Z" {
		t.Fatalf("seedTimestamp 必须是 Node toISOString 形态: %q", got)
	}
	// nowTime() 缺省走 time.Now()，此处只锁非零与 UTC 格式化路径。
	if seedTimestamp(SeedOptions{}.nowTime()) == "" {
		t.Fatal("缺省时钟必须产出时间戳")
	}
	if got := (SeedOptions{Now: func() time.Time { return clock }}).nowTime(); got != clock {
		t.Fatalf("注入时钟应被使用: %v", got)
	}
	// 行为存疑：<实现只把空白值视为未配置，非空白值原样返回（不去尾随空白）。
	// 此处按当前实际行为断言。
	if got := (SeedOptions{Secret: "  custom  "}).seedSecret(); got != "  custom  " {
		t.Fatalf("显式 secret 按当前实现应原样返回: %q", got)
	}
	if got := (SeedOptions{Secret: "   "}).seedSecret(); got != seedDefaultRuntimeSecret {
		t.Fatalf("纯空白 secret 必须回落 Node dev 默认: %q", got)
	}
	if got := (SeedOptions{}).seedSecret(); got != seedDefaultRuntimeSecret {
		t.Fatalf("空 secret 必须回落 Node dev 默认: %q", got)
	}
}

func TestWMSeedCryptoHelpersKeepNodeEnvelopeShapes(t *testing.T) {
	apiKey, err := seedCreateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(apiKey) != len("sk-")+64 {
		t.Fatalf("api key 形态应为 sk- + 64 hex: %q", apiKey)
	}
	token, err := seedCreateExternalIntegrationToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != len("juis_")+43 {
		t.Fatalf("外部集成 token 形态应为 juis_ + 43 base64url: %q", token)
	}
	tokenHash := seedHashExternalIntegrationToken(token)
	if len(tokenHash) != 64 {
		t.Fatalf("token 哈希应为 sha256 hex: %q", tokenHash)
	}
	if seedHashSecret("abc") != seedHashSecret("abc") || seedHashSecret("abc") == seedHashSecret("abd") {
		t.Fatal("seedHashSecret 必须稳定且可区分")
	}

	encrypted, err := seedEncryptJSONWithOptions(SeedOptions{Secret: "k"}, map[string]string{"key": "value"})
	if err != nil {
		t.Fatal(err)
	}
	parts := splitWMSecretEnvelope(encrypted)
	if len(parts) != 4 || parts[0] != "v1" {
		t.Fatalf("AES 信封应为 v1:iv:tag:ciphertext: %q", encrypted)
	}
	// 不同 secret 的密文必须不同（key 为 sha256(secret)）。
	other, err := seedEncryptJSON("k2", map[string]string{"key": "value"})
	if err != nil {
		t.Fatal(err)
	}
	if other == encrypted {
		t.Fatal("不同 secret 必须产出不同密文")
	}
	// seedStringify 必须是紧凑 JSON 且不做 HTML 转义。
	if got := seedStringify([]string{"a", "<b>"}); got != `["a","<b>"]` {
		t.Fatalf("seedStringify 输出形态: %q", got)
	}
}

func splitWMSecretEnvelope(value string) []string {
	parts := make([]string, 0, 4)
	start := 0
	for index := 0; index < len(value); index++ {
		if value[index] == ':' {
			parts = append(parts, value[start:index])
			start = index + 1
		}
	}
	return append(parts, value[start:])
}

func TestWMSeedStringArrayHelpers(t *testing.T) {
	items, err := parseSeedStringArray(`["a","b"]`)
	if err != nil || len(items) != 2 || items[1] != "b" {
		t.Fatalf("parseSeedStringArray: %v %v", items, err)
	}
	if _, err := parseSeedStringArray("{bad"); err == nil {
		t.Fatal("坏 JSON 必须报错")
	}
	if got := mustJSON([]string{"x"}); got != `["x"]` {
		t.Fatalf("mustJSON: %q", got)
	}
	if got := mergeSeedDistinct([]string{"a", "b"}, []string{"b", "c"}); len(got) != 3 || got[2] != "c" {
		t.Fatalf("mergeSeedDistinct 必须保序去重: %v", got)
	}
	if seedStringSlicesEqual([]string{"a"}, []string{"a", "a"}) || !seedStringSlicesEqual([]string{"a", "b"}, []string{"a", "b"}) {
		t.Fatal("seedStringSlicesEqual 语义错误")
	}
}

// TestWMSeedSQLiteRecoversStaleGeneratedModels 覆盖 SQLite seed 的陈旧模型
// 回收分支：非内置 (provider_code, model) 组合必须被停用并从目录隐藏。
func TestWMSeedSQLiteRecoversStaleGeneratedModels(t *testing.T) {
	ctx := context.Background()
	db := openSeedTestDatabase(t)
	options := SeedOptions{Now: func() time.Time { return sqliteSeedTestClock }, Secret: sqliteSeedTestSecret}
	if _, err := SeedSQLiteDefaults(ctx, db, options); err != nil {
		t.Fatalf("首次 seed: %v", err)
	}
	// 插入一行非内置的生成模型：下一次 seed 必须把它当作陈旧生成行停用。
	if _, err := db.ExecContext(ctx, "INSERT INTO provider_model_catalog (id, provider_code, model, status, catalog_visible, source, created_at, updated_at) VALUES ('provider_model_wm_stale','gpt','wm-stale-custom-model','active',1,'generated','2026-09-04T08:00:00.000Z','2026-09-04T08:00:00.000Z')"); err != nil {
		t.Fatalf("插入陈旧生成模型: %v", err)
	}
	staleID := "provider_model_wm_stale"
	if _, err := SeedSQLiteDefaults(ctx, db, options); err != nil {
		t.Fatalf("二次 seed（含陈旧行）: %v", err)
	}
	var status string
	var visible int
	if err := db.QueryRowContext(ctx, "SELECT status, catalog_visible FROM provider_model_catalog WHERE id=?", staleID).Scan(&status, &visible); err != nil {
		t.Fatal(err)
	}
	if status != "disabled" || visible != 0 {
		t.Fatalf("陈旧生成模型必须被停用: status=%s visible=%d", status, visible)
	}
}

// TestWMSeedSQLiteStripsCodexAutoReviewFromGPTDefaults 锁定 SQLite seed 的
// GPT 默认清单清洗步（sqSeedGPTVendorCodexAutoReviewRemoval）：老库残留的
// codex-auto-review 必须在下一次 seed 时被剔除，其他供应商不受影响。
func TestWMSeedSQLiteStripsCodexAutoReviewFromGPTDefaults(t *testing.T) {
	ctx := context.Background()
	db := openSeedTestDatabase(t)
	options := SeedOptions{Now: func() time.Time { return sqliteSeedTestClock }, Secret: sqliteSeedTestSecret}
	if _, err := SeedSQLiteDefaults(ctx, db, options); err != nil {
		t.Fatalf("首次 seed: %v", err)
	}
	// 模拟老库残留：把 GPT 默认清单替换为含已退役模型的旧列表。
	if _, err := db.ExecContext(ctx, `UPDATE providers SET default_supported_models_json = ? WHERE code = ?`,
		`["codex-auto-review","gpt-5.5","gpt-5.4"]`, gptVendorCode); err != nil {
		t.Fatalf("注入残留清单: %v", err)
	}
	var openaiList string
	if err := db.QueryRowContext(ctx, `SELECT default_supported_models_json FROM providers WHERE code = 'openai'`).Scan(&openaiList); err != nil {
		t.Fatal(err)
	}
	// 清洗语句与共享常量必须指向同一退役模型，防止字面量漂移。
	if !strings.Contains(sqSeedGPTVendorCodexAutoReviewRemoval, retiredCodexAutoReviewModel) {
		t.Fatalf("SQLite 清洗语句必须包含退役模型字面量 %q", retiredCodexAutoReviewModel)
	}
	if _, err := SeedSQLiteDefaults(ctx, db, options); err != nil {
		t.Fatalf("二次 seed（含残留清单）: %v", err)
	}
	var gptList string
	if err := db.QueryRowContext(ctx, `SELECT default_supported_models_json FROM providers WHERE code = ?`, gptVendorCode).Scan(&gptList); err != nil {
		t.Fatal(err)
	}
	if gptList != `["gpt-5.5","gpt-5.4"]` {
		t.Fatalf("残留 codex-auto-review 必须被剔除: %s", gptList)
	}
	var openaiAfter string
	if err := db.QueryRowContext(ctx, `SELECT default_supported_models_json FROM providers WHERE code = 'openai'`).Scan(&openaiAfter); err != nil {
		t.Fatal(err)
	}
	if openaiAfter != openaiList {
		t.Fatalf("清洗步只允许作用于 GPT 供应商行: %s -> %s", openaiList, openaiAfter)
	}
	// 幂等：清洗后的库再跑一次 seed，清单保持不变。
	if _, err := SeedSQLiteDefaults(ctx, db, options); err != nil {
		t.Fatalf("三次 seed（幂等）: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT default_supported_models_json FROM providers WHERE code = ?`, gptVendorCode).Scan(&gptList); err != nil {
		t.Fatal(err)
	}
	if gptList != `["gpt-5.5","gpt-5.4"]` {
		t.Fatalf("清洗后清单必须保持稳定: %s", gptList)
	}
}

// TestWMSeedSQLiteInsertsBuiltInGroupsForNewAccounts 覆盖
// seedSQLiteBuiltInGroupsForAllSystemAccounts 的“新账户补建内置分组”分支。
func TestWMSeedSQLiteInsertsBuiltInGroupsForNewAccounts(t *testing.T) {
	ctx := context.Background()
	db := openSeedTestDatabase(t)
	options := SeedOptions{Now: func() time.Time { return sqliteSeedTestClock }, Secret: sqliteSeedTestSecret}
	if _, err := SeedSQLiteDefaults(ctx, db, options); err != nil {
		t.Fatalf("首次 seed: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO system_accounts (id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, created_at, updated_at) VALUES ('sys_wm_second','wm-second','第二账户','测试用','admin','active','pbkdf2$sha512$120000$wm$wm',0,0,'2026-09-04T08:00:00.000Z','2026-09-04T08:00:00.000Z')`); err != nil {
		t.Fatalf("插入第二个系统账户: %v", err)
	}
	var before int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM groups WHERE system_account_id='sys_wm_second'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatalf("新账户不应已有分组: %d", before)
	}
	if _, err := SeedSQLiteDefaults(ctx, db, options); err != nil {
		t.Fatalf("二次 seed（新账户）: %v", err)
	}
	var after int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM groups WHERE system_account_id='sys_wm_second'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != len(pgSeedGroups) {
		t.Fatalf("新账户应获得每个 provider 一个内置分组: got %d want %d", after, len(pgSeedGroups))
	}
}
