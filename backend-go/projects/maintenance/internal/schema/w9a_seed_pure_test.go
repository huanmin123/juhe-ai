package schema

import (
	"strings"
	"testing"
	"time"
)

// TestW9ASeedPureHelpers 锁定 seed 共享纯函数的契约。
func TestW9ASeedPureHelpers(t *testing.T) {
	if got := seedTimestamp(time.Date(2026, 9, 1, 2, 3, 4, 5e6, time.UTC)); got != "2026-09-01T02:03:04.005Z" {
		t.Fatalf("seedTimestamp = %q", got)
	}
	if seedTimestamp(time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("X", 8*3600))) == "" {
		t.Fatal("seedTimestamp must handle non-UTC zones")
	}

	options := SeedOptions{}
	if options.seedSecret() != seedDefaultRuntimeSecret {
		t.Fatal("empty secret must fall back to the dev default")
	}
	options.Secret = "  custom  "
	if options.seedSecret() != "  custom  " {
		t.Fatalf("non-empty secret must be preserved: %q", options.seedSecret())
	}

	if got := seedBoolInt(true); got != 1 {
		t.Fatalf("seedBoolInt(true) = %d", got)
	}
	if got := seedBoolInt(false); got != 0 {
		t.Fatalf("seedBoolInt(false) = %d", got)
	}

	if got := seedKeyPrefix("sk-1234567890"); got != "sk-12345" {
		t.Fatalf("seedKeyPrefix = %q", got)
	}
	if got := seedKeyPrefix("short"); got != "short" {
		t.Fatalf("seedKeyPrefix short = %q", got)
	}
	if got := seedKeySuffix("sk-1234567890"); got != "34567890" {
		t.Fatalf("seedKeySuffix = %q", got)
	}
	if got := seedKeySuffix("short"); got != "short" {
		t.Fatalf("seedKeySuffix short = %q", got)
	}

	text := "value"
	if got := seedNullableString(&text); got != "value" {
		t.Fatalf("seedNullableString = %v", got)
	}
	if got := seedNullableString(nil); got != nil {
		t.Fatalf("seedNullableString nil = %v", got)
	}
	var number int64 = 7
	if got := seedNullableInt64(&number); got != int64(7) {
		t.Fatalf("seedNullableInt64 = %v", got)
	}
	if got := seedNullableInt64(nil); got != nil {
		t.Fatalf("seedNullableInt64 nil = %v", got)
	}
	var ratio = 1.5
	if got := seedNullableFloat64(&ratio); got != 1.5 {
		t.Fatalf("seedNullableFloat64 = %v", got)
	}
	if got := seedNullableFloat64(nil); got != nil {
		t.Fatalf("seedNullableFloat64 nil = %v", got)
	}

	if got := pgNullableText(""); got != nil {
		t.Fatalf("pgNullableText(\"\") = %v", got)
	}
	if got := pgNullableText("parent"); got != "parent" {
		t.Fatalf("pgNullableText(parent) = %v", got)
	}

	if seedHashSecret("a") == "" || len(seedHashSecret("a")) != 64 {
		t.Fatal("seedHashSecret must return a sha256 hex string")
	}

	apiKey, err := seedCreateAPIKey()
	if err != nil || !strings.HasPrefix(apiKey, "sk-") || len(apiKey) != len("sk-")+64 {
		t.Fatalf("seedCreateAPIKey = %q err %v", apiKey, err)
	}
	token, err := seedCreateExternalIntegrationToken()
	if err != nil || !strings.HasPrefix(token, "juis_") {
		t.Fatalf("seedCreateExternalIntegrationToken = %q err %v", token, err)
	}
	if seedHashExternalIntegrationToken("t") == seedHashSecret("t") {
		t.Fatal("external integration token hash must include the domain prefix")
	}
}

// TestW9ASeedJSONHelpers 覆盖 JSON helper 的正常与错误路径。
func TestW9ASeedJSONHelpers(t *testing.T) {
	encoded, err := seedJSONStringify(map[string]string{"k": "<v>"})
	if err != nil {
		t.Fatalf("seedJSONStringify: %v", err)
	}
	if string(encoded) != `{"k":"<v>"}` {
		t.Fatalf("seedJSONStringify must not HTML-escape: %s", encoded)
	}
	if _, err := seedJSONStringify(make(chan int)); err == nil {
		t.Fatal("unsupported value must fail to encode")
	}

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("seedStringify must panic on unsupported values")
		}
	}()
	_ = seedStringify(make(chan int))
}

// TestW9AMustJSONPanic 锁定 mustJSON 对不可编码值的 panic 行为。
func TestW9AMustJSONPanic(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("mustJSON must panic on unsupported values")
		}
	}()
	_ = mustJSON(make(chan int))
}

// TestW9ASeedEncryptJSONRoundTrip 锁定 v1 AES-GCM envelope 的往返语义。
func TestW9ASeedEncryptJSONRoundTrip(t *testing.T) {
	sealed, err := seedEncryptJSON("secret", map[string]string{"key": "sk-test"})
	if err != nil {
		t.Fatalf("seedEncryptJSON: %v", err)
	}
	parts := strings.Split(sealed, ":")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		t.Fatalf("unexpected envelope %q", sealed)
	}
	again, err := seedEncryptJSON("secret", map[string]string{"key": "sk-test"})
	if err != nil || again == sealed {
		t.Fatalf("random IV must change the ciphertext: %q vs %q", again, sealed)
	}
	if _, err := seedEncryptJSON("secret", make(chan int)); err == nil {
		t.Fatal("unsupported payload must fail")
	}
}

// TestW9AProviderModelCatalogID 锁定 slug 与截断规则。
func TestW9AProviderModelCatalogID(t *testing.T) {
	id := providerModelCatalogID("GPT", "gpt-5.6 sol")
	if !strings.HasPrefix(id, "provider_model_gpt_gpt_5_6_sol_") {
		t.Fatalf("unexpected id %q", id)
	}
	if len(id) != len("provider_model_")+len("gpt_gpt_5_6_sol")+1+12 {
		t.Fatalf("id length drifted: %q (%d)", id, len(id))
	}
	long := providerModelCatalogID("provider", strings.Repeat("m", 200))
	slugPart := strings.Split(strings.TrimPrefix(long, "provider_model_"), "_")[0]
	if len(slugPart) > 72 {
		t.Fatalf("slug must be truncated to 72 chars: %d", len(slugPart))
	}
	if long == providerModelCatalogID("other", strings.Repeat("m", 200)) {
		t.Fatal("digest suffix must disambiguate truncated slugs")
	}
}

// TestW9ASeedArrayHelpers 锁定数组合并/比较与命名转换。
func TestW9ASeedArrayHelpers(t *testing.T) {
	items, err := parseSeedStringArray(`["a","b"]`)
	if err != nil || len(items) != 2 {
		t.Fatalf("parseSeedStringArray = %v err %v", items, err)
	}
	if _, err := parseSeedStringArray("{bad"); err == nil {
		t.Fatal("invalid JSON must fail")
	}

	if got := mergeSeedDistinct([]string{"a", "b"}, []string{"b", "c"}); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("mergeSeedDistinct = %v", got)
	}
	if got := mergeSeedDistinct(nil, nil); len(got) != 0 {
		t.Fatalf("mergeSeedDistinct empty = %v", got)
	}

	if !seedStringSlicesEqual([]string{"a"}, []string{"a"}) {
		t.Fatal("equal slices must compare equal")
	}
	if seedStringSlicesEqual([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("length mismatch must not compare equal")
	}
	if seedStringSlicesEqual([]string{"a"}, []string{"b"}) {
		t.Fatal("content mismatch must not compare equal")
	}

	if got := defaultRouteStrategyIDForGroup("grp_gpt_default"); got != "route_strategy_gpt_default" {
		t.Fatalf("defaultRouteStrategyIDForGroup = %q", got)
	}
	if got := defaultRouteStrategyGroupBindingIDForGroup("grp_gpt_default"); got != "rsg_gpt_default" {
		t.Fatalf("defaultRouteStrategyGroupBindingIDForGroup = %q", got)
	}
	if got := defaultAPIKeyIDForRouteStrategy("route_strategy_x"); got != "key_default_x" {
		t.Fatalf("defaultAPIKeyIDForRouteStrategy = %q", got)
	}
	if got := defaultRouteStrategyNameForGroup("gpt默认分组"); got != "gpt默认路由" {
		t.Fatalf("defaultRouteStrategyNameForGroup = %q", got)
	}
	if got := defaultAPIKeyNameForRouteStrategy("gpt默认路由"); got != "gpt默认API Key" {
		t.Fatalf("defaultAPIKeyNameForRouteStrategy = %q", got)
	}
	if got := seedReplacePrefix("plain", "nope", "x"); got != "plain" {
		t.Fatalf("seedReplacePrefix no match = %q", got)
	}
	if got := seedReplaceSuffix("plain", "nope", "x"); got != "plain" {
		t.Fatalf("seedReplaceSuffix no match = %q", got)
	}
}

// TestW9AActiveModelCatalogSeedRows 锁定 shutdown 过滤语义：活跃行不得包含
// shutdown_date 早于截止日的目录项，且过滤结果确定。
func TestW9AActiveModelCatalogSeedRows(t *testing.T) {
	if len(modelCatalogSeedRows) == 0 {
		t.Fatal("model catalog seed data must not be empty")
	}
	active := activeModelCatalogSeedRows("2026-09-01")
	if len(active) == 0 {
		t.Fatal("active rows must not be empty for the current cutoff")
	}
	for _, row := range active {
		if row.ShutdownDate != nil && *row.ShutdownDate <= "2026-09-01" {
			t.Fatalf("row %s 已停用却仍在活跃集合中", row.ID)
		}
	}
	if got := activeModelCatalogSeedRows("2026-09-01"); len(got) != len(active) {
		t.Fatal("active rows not deterministic")
	}
	// 截止日越大，被过滤的已停用行越多；极早截止日保留全部行。
	late := activeModelCatalogSeedRows("9999-12-31")
	if len(late) >= len(active) {
		t.Fatalf("late cutoff must keep no more rows than the current cutoff: %d vs %d", len(late), len(active))
	}
	earliest := activeModelCatalogSeedRows("0000-01-01")
	if len(earliest) != len(modelCatalogSeedRows) {
		t.Fatalf("earliest cutoff must keep every row: %d vs %d", len(earliest), len(modelCatalogSeedRows))
	}
}
