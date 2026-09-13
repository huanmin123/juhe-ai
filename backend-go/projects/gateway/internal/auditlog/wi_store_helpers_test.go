package auditlog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWIHeaderValuesJSONRoundTrip(t *testing.T) {
	// 单值保持字符串；多值保持数组；空数组序列化为空数组。
	var single HeaderValues
	if err := single.UnmarshalJSON([]byte(`"v1"`)); err != nil {
		t.Fatalf("单值解码失败: %v", err)
	}
	if single.Array || len(single.Values) != 1 || single.Values[0] != "v1" {
		t.Fatalf("single=%+v", single)
	}
	encoded, err := single.MarshalJSON()
	if err != nil || string(encoded) != `"v1"` {
		t.Fatalf("单值编码=%s err=%v", encoded, err)
	}
	var many HeaderValues
	if err := many.UnmarshalJSON([]byte(`["a","b"]`)); err != nil {
		t.Fatalf("数组解码失败: %v", err)
	}
	if !many.Array || len(many.Values) != 2 {
		t.Fatalf("many=%+v", many)
	}
	encoded, err = many.MarshalJSON()
	if err != nil || string(encoded) != `["a","b"]` {
		t.Fatalf("数组编码=%s err=%v", encoded, err)
	}
	// 空 Values 数组形态序列化为 []。
	empty := HeaderValues{Array: true}
	if encoded, _ := empty.MarshalJSON(); string(encoded) != "[]" {
		t.Fatalf("空数组=%s", encoded)
	}
	// 空 Values 标量形态序列化为 ""。
	emptyScalar := HeaderValues{}
	if encoded, _ := emptyScalar.MarshalJSON(); string(encoded) != `""` {
		t.Fatalf("空标量=%s", encoded)
	}
	// 非法输入。
	var bad HeaderValues
	if err := bad.UnmarshalJSON([]byte(`42`)); err == nil {
		t.Fatal("数字 header 必须报错")
	}
}

func TestWIPayloadBodyUnmarshalForms(t *testing.T) {
	// null → 不存在。
	var none PayloadBody
	if err := none.UnmarshalJSON([]byte(`null`)); err != nil || none.Present {
		t.Fatalf("null=%+v err=%v", none, err)
	}
	// JSON 字符串。
	var text PayloadBody
	if err := text.UnmarshalJSON([]byte(`"hello"`)); err != nil || !text.Present || string(text.Bytes) != "hello" {
		t.Fatalf("text=%+v err=%v", text, err)
	}
	// base64 传输形态。
	var b64 PayloadBody
	if err := b64.UnmarshalJSON([]byte(`{"base64":"aGVsbG8="}`)); err != nil || string(b64.Bytes) != "hello" {
		t.Fatalf("base64=%+v err=%v", b64, err)
	}
	// 非法 base64。
	var badB64 PayloadBody
	if err := badB64.UnmarshalJSON([]byte(`{"base64":"%%%"}`)); err == nil {
		t.Fatal("坏 base64 必须报错")
	}
	// Node Buffer 形态。
	var buffer PayloadBody
	if err := buffer.UnmarshalJSON([]byte(`{"type":"Buffer","data":[104,105]}`)); err != nil || string(buffer.Bytes) != "hi" {
		t.Fatalf("buffer=%+v err=%v", buffer, err)
	}
	// 全部形态都不匹配。
	var bad PayloadBody
	if err := bad.UnmarshalJSON([]byte(`42`)); err == nil {
		t.Fatal("数字 body 必须报错")
	}
}

func TestWIValidateInputAndLifecycleGuards(t *testing.T) {
	valid := fixture("wi-valid", LifecycleFinalized)
	if err := validateInput(valid); err != nil {
		t.Fatalf("合法输入报错: %v", err)
	}
	// 缺关键字段。
	missing := valid
	missing.TraceID = "  "
	if err := validateInput(missing); err == nil {
		t.Fatal("空 traceId 必须报错")
	}
	// 非法 lifecycle。
	badLifecycle := valid
	badLifecycle.LifecycleStatus = "weird"
	if err := validateInput(badLifecycle); err == nil {
		t.Fatal("非法 lifecycle 必须报错")
	}
	// 空 lifecycle 归一化为 finalized。
	if got := normalizeLifecycle(""); got != LifecycleFinalized {
		t.Fatalf("normalizeLifecycle=%q", got)
	}
	if got := normalizeLifecycle(LifecycleInProgress); got != LifecycleInProgress {
		t.Fatalf("normalizeLifecycle=%q", got)
	}
	// isKnown 泛型矩阵。
	if !isKnown("a", "a", "b") || isKnown("c", "a", "b") {
		t.Fatal("isKnown 错误")
	}
	// 探针类 trafficSource 不属于持久化范围。
	for _, source := range []TrafficSource{TrafficSourceAccountHealthCheck, TrafficSourceRuntimeRecoveryProbe, TrafficSourceCooldownRetest} {
		probed := valid
		probed.TrafficSource = source
		if err := validateInput(probed); err == nil {
			t.Fatalf("探针 trafficSource %q 必须拒绝", source)
		}
	}
}

func TestWICloneAuditLogInputMutableFields(t *testing.T) {
	applied := true
	stream := true
	statusCode := 200
	duration := int64(12)
	httpDuration := int64(8)
	firstToken := int64(3)
	source := fixture("wi-clone", LifecycleFinalized)
	source.ModelMappingApplied = &applied
	source.Stream = &stream
	source.FinalStatusCode = &statusCode
	source.DurationMS = &duration
	source.HTTPDurationMS = &httpDuration
	source.FirstTokenMS = &firstToken
	source.Attempts = []AuditLogAttemptInput{{
		AttemptIndex: 1, ModelMappingApplied: &applied, UpstreamStatusCode: &statusCode, Success: &applied, DurationMS: &duration,
	}}
	source.Payloads = []AuditLogPayloadInput{{PartType: "body"}}

	cloned := cloneAuditLogInputMutableFields(source)
	if cloned.ModelMappingApplied == source.ModelMappingApplied {
		t.Fatal("ModelMappingApplied 必须深拷贝")
	}
	if cloned.Stream == source.Stream || cloned.FinalStatusCode == source.FinalStatusCode ||
		cloned.DurationMS == source.DurationMS || cloned.HTTPDurationMS == source.HTTPDurationMS ||
		cloned.FirstTokenMS == source.FirstTokenMS {
		t.Fatal("标量指针字段必须深拷贝")
	}
	if len(cloned.Attempts) != 1 || cloned.Attempts[0].ModelMappingApplied == source.Attempts[0].ModelMappingApplied ||
		cloned.Attempts[0].UpstreamStatusCode == source.Attempts[0].UpstreamStatusCode ||
		cloned.Attempts[0].Success == source.Attempts[0].Success ||
		cloned.Attempts[0].DurationMS == source.Attempts[0].DurationMS {
		t.Fatal("attempts 指针字段必须深拷贝")
	}
	// nil attempts 保持 nil。
	source.Attempts = nil
	if cloned2 := cloneAuditLogInputMutableFields(source); cloned2.Attempts != nil {
		t.Fatal("nil attempts 必须保持 nil")
	}
}

func TestWIStoreScalarHelpers(t *testing.T) {
	// nullIfEmpty / boolValue。
	if nullIfEmpty("") != nil {
		t.Fatal("空字符串必须 NULL")
	}
	if got := nullIfEmpty("v"); got != "v" {
		t.Fatalf("nullIfEmpty=%v", got)
	}
	truth := true
	if got := boolValue(&truth); got != true {
		t.Fatalf("boolValue=%v", got)
	}
	// 契约：nil 指针回落 false（SQLite 无 bool 类型，写入 0）。
	if got := boolValue(nil); got != false {
		t.Fatalf("nil boolValue=%v", got)
	}
	// dbTime / dbTimeText / nullableTime。
	zero := time.Time{}
	if got := dbTime(ModeSQLite, zero); got != "0001-01-01T00:00:00Z" {
		t.Fatalf("zero dbTime=%v", got)
	}
	utc := time.Date(2026, 9, 10, 8, 30, 0, 123_000_000, time.UTC)
	sqliteTime, isText := dbTime(ModeSQLite, utc).(string)
	if !isText || !strings.HasPrefix(sqliteTime, "2026-09-10T08:30:00.123") {
		t.Fatalf("dbTime=%v", sqliteTime)
	}
	// postgres 模式返回 time.Time。
	if _, isTime := dbTime(ModePostgres, utc).(time.Time); !isTime {
		t.Fatal("postgres dbTime 必须是 time.Time")
	}
	if got := dbTimeText(utc.Format(time.RFC3339Nano)); got == nil {
		t.Fatal("合法文本必须归一化")
	}
	if got := dbTimeText("not-a-time"); got != nil {
		t.Fatalf("非法文本=%v", got)
	}
	if got := nullableTime(ModeSQLite, "  "); got != nil {
		t.Fatalf("空 nullableTime=%v", got)
	}
	if got := nullableTime(ModeSQLite, "not-a-time"); got != nil {
		t.Fatalf("非法 nullableTime=%v", got)
	}
	if got := nullableTime(ModeSQLite, utc.Format(time.RFC3339Nano)); got == nil {
		t.Fatal("合法时间必须非 nil")
	}
	// nodeStatusCode：nil → 空串，非 nil → 原值。
	if got := nodeStatusCode(nil); got != "" {
		t.Fatalf("nil status=%v", got)
	}
	code := 502
	if got := nodeStatusCode(&code); got != 502 {
		t.Fatalf("node status=%v", got)
	}
}

func TestWIChunkAndBuildHotSearchText(t *testing.T) {
	// 短文本单块。
	if got := chunkHotSearchText("abc"); len(got) != 1 || got[0] != "abc" {
		t.Fatalf("短文本=%v", got)
	}
	// 长文本按 maxHotSearchChunkBytes 切块。
	long := stringsRepeat2("x", maxHotSearchChunkBytes*2+7)
	chunks := chunkHotSearchText(long)
	if len(chunks) != 3 {
		t.Fatalf("块数=%d", len(chunks))
	}
	for i, chunk := range chunks {
		want := maxHotSearchChunkBytes
		if i == len(chunks)-1 {
			want = 7
		}
		if len(chunk) != want {
			t.Fatalf("块 %d 长度=%d want %d", i, len(chunk), want)
		}
	}
	// 构建文本包含关键过滤维度；空 part 被过滤。
	input := fixture("wi-hot-text", LifecycleFinalized)
	input.Success = false
	input.ErrorCode = "E1"
	input.FinalStatusCode = &[]int{502}[0]
	text := buildHotSearchText(input)
	if !strings.Contains(text, "E1") || !strings.Contains(text, "502") || !strings.Contains(text, "trace-wi-hot-text") {
		t.Fatalf("text=%q", text)
	}
	if strings.Contains(text, "  ") {
		t.Fatalf("过滤后不得有连续空格: %q", text)
	}
	// 非持久化来源列表。
	if !isNonPersistedTrafficSource("account_health_check") || isNonPersistedTrafficSource("gateway") {
		t.Fatal("isNonPersistedTrafficSource 错误")
	}
}

func stringsRepeat2(ch string, n int) string {
	out := make([]byte, 0, n*len(ch))
	for i := 0; i < n; i++ {
		out = append(out, ch...)
	}
	return string(out)
}

func TestWICleanupHotSearchAndBucketParsing(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	store := openSQLiteStore(t, cfg)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	implementation := store.(*sqlStore)

	// 空 cutoff 必须报错。
	if _, err := store.CleanupHotSearch(ctx, lease, time.Time{}, 0); err == nil {
		t.Fatal("空 cutoff 必须报错")
	}

	// 写入两个小时的桶文件，断言只删除整点早于 cutoff 的桶。
	base := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	inputs := []AuditLogInput{fixture("wi-hot-cleanup", LifecycleFinalized)}
	if _, err := store.AppendHotSearch(ctx, lease, inputs); err != nil {
		t.Fatalf("写入 hot search 失败: %v", err)
	}
	// 追加发生在当前小时；再手工放置一个旧桶文件。
	oldBucket := filepath.Join(implementation.hotDir, hotSearchFileName(base.Add(-3*time.Hour)))
	if err := os.MkdirAll(filepath.Dir(oldBucket), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldBucket, []byte(`{"auditLogId":"old","createdAt":"x","sequence":0,"text":"t"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldBucket); err != nil {
		t.Fatalf("旧桶未创建: %v", err)
	}
	deleted, err := store.CleanupHotSearch(ctx, lease, base.Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if deleted < 1 {
		t.Fatalf("至少删除旧桶: deleted=%d", deleted)
	}
	if _, err := os.Stat(oldBucket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧桶必须删除: %v", err)
	}
	// 过期 fence 必须拒绝清理。
	stale := OwnerLease{OwnerID: lease.OwnerID, FenceToken: lease.FenceToken + 9}
	if _, err := store.CleanupHotSearch(ctx, stale, base, 10); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("过期 fence 必须报 ErrOwnerLeaseLost: %v", err)
	}
	// 桶名解析矩阵。
	parsed, ok := parseHotSearchBucket(hotSearchFileName(base))
	if !ok || !parsed.Equal(base.Truncate(time.Hour)) {
		t.Fatalf("parsed=%v ok=%v", parsed, ok)
	}
	if _, ok := parseHotSearchBucket("not-a-bucket.ndjson"); ok {
		t.Fatal("非法桶名不得解析")
	}
}

func TestWIConfigValidateRetentionPolicyMatrix(t *testing.T) {
	base := sqliteConfig(t, t.TempDir())
	if err := base.validateRetentionPolicy(); err != nil {
		t.Fatalf("基线配置必须合法: %v", err)
	}
	invalids := []func(*Config){
		func(c *Config) { c.RetentionInterval = 500 * time.Millisecond },
		func(c *Config) { c.RetentionInterval = 25 * time.Hour },
		func(c *Config) { c.RetentionBatchSize = 0 },
		func(c *Config) { c.RetentionBatchSize = 5097 },
		func(c *Config) { c.SuccessHotRetentionHours = -1 },
		func(c *Config) { c.SuccessHotRetentionHours = 169 },
		func(c *Config) { c.SuccessSampleRate = -0.1 },
		func(c *Config) { c.SuccessSampleRate = 1.1 },
		func(c *Config) { c.SuccessRetentionDays = -1 },
		func(c *Config) { c.SuccessRetentionDays = 3651 },
		func(c *Config) { c.ProblemRetentionDays = -1 },
		func(c *Config) { c.ProblemRetentionDays = 3651 },
	}
	for i, mutate := range invalids {
		cfg := base
		mutate(&cfg)
		if err := cfg.validateRetentionPolicy(); err == nil {
			t.Fatalf("第 %d 个非法配置必须报错", i)
		}
	}
	// parseBoundedDecimal 矩阵。
	fallback := 0.5
	if got, err := parseBoundedDecimal("n", "", fallback, 0, 1, 4); err != nil || got != fallback {
		t.Fatalf("空值回落=%v err=%v", got, err)
	}
	if _, err := parseBoundedDecimal("n", "abc", fallback, 0, 1, 4); err == nil {
		t.Fatal("非数字必须报错")
	}
	if _, err := parseBoundedDecimal("n", "1.12345", fallback, 0, 2, 4); err == nil {
		t.Fatal("超小数位必须报错")
	}
	if _, err := parseBoundedDecimal("n", "1.5", fallback, 0, 1, 4); err == nil {
		t.Fatal("超范围必须报错")
	}
	if got, err := parseBoundedDecimal("n", "0.25", fallback, 0, 1, 4); err != nil || got != 0.25 {
		t.Fatalf("合法小数=%v err=%v", got, err)
	}
}

func TestWIRetentionConfigAtDerivesCutoffs(t *testing.T) {
	cfg := Config{
		SuccessHotRetentionHours: 6,
		SuccessRetentionDays:     10,
		ProblemRetentionDays:     20,
		SuccessSampleRate:        0.25,
		RetentionBatchSize:       500,
	}
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	config := cfg.RetentionConfigAt(now)
	if !config.SuccessHotCutoff.Equal(now.Add(-6 * time.Hour)) {
		t.Fatalf("hot cutoff=%v", config.SuccessHotCutoff)
	}
	if !config.SuccessCutoff.Equal(now.Add(-10 * 24 * time.Hour)) {
		t.Fatalf("success cutoff=%v", config.SuccessCutoff)
	}
	if !config.FailureCutoff.Equal(now.Add(-20 * 24 * time.Hour)) {
		t.Fatalf("failure cutoff=%v", config.FailureCutoff)
	}
	if config.SuccessSampleBucketThreshold != 2500 || config.BatchSize != 500 {
		t.Fatalf("config=%+v", config)
	}
	// 零 success retention 天数回落 hot 窗口。
	cfg.SuccessRetentionDays = 0
	config = cfg.RetentionConfigAt(now)
	if !config.SuccessCutoff.Equal(config.SuccessHotCutoff) {
		t.Fatalf("零 success retention 必须回落 hot 窗口: %+v", config)
	}
}

func TestWIJSONMarshalAuditLogOutput(t *testing.T) {
	// MarshalJSON 契约：可序列化的完整输入 round-trip 后保留关键字段。
	input := fixture("wi-json", LifecycleFinalized)
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var decoded AuditLogInput
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if decoded.ID != input.ID || decoded.TraceID != input.TraceID || decoded.Path != input.Path {
		t.Fatalf("round-trip=%+v", decoded)
	}
}
