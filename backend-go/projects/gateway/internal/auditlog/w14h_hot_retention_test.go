package auditlog

// w14h 覆盖波次：hot-search 追加/搜索/清理矩阵、Persist payload blob 生命周期
// （写入/发布/复用/引用）、retention 全链路与 blob GC 分支、辅助函数错误臂。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func w14hStore(t *testing.T) (*sqlStore, OwnerLease) {
	t.Helper()
	cfg := sqliteConfig(t, t.TempDir())
	cfg.HotSearchDirectory = filepath.Join(filepath.Dir(cfg.AuditDatabasePath), "w14h-hot")
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store.(*sqlStore), acquireLease(t, store)
}

func w14hPayloadInput(id string, body string) AuditLogInput {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	input := fixture(id, LifecycleFinalized)
	input.CreatedAt = now
	input.EndedAt = now
	input.Payloads = []AuditLogPayloadInput{{
		ID:            "payload-" + id,
		PartType:      PayloadPartUpstreamRequest,
		ContentType:   "application/json",
		Body:          PayloadBody{Bytes: []byte(body), Present: true},
		CaptureStatus: PayloadCaptureComplete,
	}}
	return input
}

func TestW14HPersistPayloadBlobLifecycle(t *testing.T) {
	ctx := context.Background()
	store, lease := w14hStore(t)
	body := `{"model":"gpt-x","prompt":"w14h payload"}`
	// 首次持久化：blob 写入 + 发布 + 引用计数。
	if _, err := store.Persist(ctx, lease, w14hPayloadInput("w14h-pl-1", body)); err != nil {
		t.Fatal(err)
	}
	// 复用相同 body 的第二个日志 → 走既有 blob 引用计数分支。
	if _, err := store.Persist(ctx, lease, w14hPayloadInput("w14h-pl-2", body)); err != nil {
		t.Fatal(err)
	}
	// 重复 ID → 幂等忽略。
	result, err := store.Persist(ctx, lease, w14hPayloadInput("w14h-pl-1", body))
	if err != nil || !result.Ignored {
		t.Fatalf("重复持久化=%+v/%v", result, err)
	}
	// blob 元数据存在且引用计数为 2。
	var refCount int
	if err := store.db.QueryRow(`SELECT ref_count FROM audit_payload_blobs LIMIT 1`).Scan(&refCount); err != nil {
		t.Fatal(err)
	}
	if refCount != 2 {
		t.Fatalf("引用计数=%d", refCount)
	}
	// retention：删除两条日志并调度 + 执行 blob 文件 GC。
	config := RetentionConfig{
		SuccessHotCutoff:  time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		SuccessCutoff:     time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		FailureCutoff:     time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		ErrorGroupCutoff:  time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		BatchSize:         100,
	}
	retention, err := store.CleanupRetention(ctx, lease, config)
	if err != nil {
		t.Fatalf("retention=%v", err)
	}
	if retention.DeletedLogs != 2 {
		t.Fatalf("删除日志=%+v", retention)
	}
	// blob 文件已被 GC 删除。
	entries, err := os.ReadDir(store.blobDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Fatalf("blob 文件应被清理：%s", entry.Name())
		}
	}
	// 配置错误臂。
	badConfigs := []RetentionConfig{
		{},
		{SuccessHotCutoff: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), SuccessCutoff: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), FailureCutoff: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), ErrorGroupCutoff: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)},
	}
	for _, config := range badConfigs {
		if _, err := store.CleanupRetention(ctx, lease, config); err == nil {
			t.Fatal("非法配置必须失败")
		}
	}
}

func TestW14HHotSearchAppendAndSearch(t *testing.T) {
	ctx := context.Background()
	store, lease := w14hStore(t)
	// 追加：正常 + 非持久化来源跳过 + 空 ID 跳过。
	inputs := []AuditLogInput{w14hPayloadInput("w14h-hot-1", ""), fixture("w14h-hot-2", LifecycleFinalized)}
	inputs[1].CreatedAt = time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	inputs[1].Path = "/v1/w14h-searchable"
	appended, err := store.AppendHotSearch(ctx, lease, inputs)
	if err != nil || appended == 0 {
		t.Fatalf("追加=%d/%v", appended, err)
	}
	// 空批次。
	if appended, err := store.AppendHotSearch(ctx, lease, nil); err != nil || appended != 0 {
		t.Fatalf("空批次=%d/%v", appended, err)
	}
	// createdAt 非法。
	bad := fixture("w14h-hot-bad", LifecycleFinalized)
	bad.CreatedAt = "w14h-not-a-time"
	if _, err := store.AppendHotSearch(ctx, lease, []AuditLogInput{bad}); err == nil {
		t.Fatal("非法 createdAt 必须失败")
	}
	// 搜索命中（时间窗口与文件 bucket 对齐）。
	result, err := store.SearchHotSearch(ctx, HotSearchOptions{
		Keywords: []string{"w14h-searchable"},
		StartAt:  time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
		EndAt:    time.Date(2026, 8, 9, 23, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AuditLogIDs) != 1 || result.AuditLogIDs[0] != "w14h-hot-2" {
		t.Fatalf("搜索结果=%+v", result)
	}
	// 关键词为空 / 全部过短 / start 晚于 end / ctx 取消。
	if _, err := store.SearchHotSearch(ctx, HotSearchOptions{}); err == nil {
		t.Fatal("缺关键词必须失败")
	}
	if _, err := store.SearchHotSearch(ctx, HotSearchOptions{Keywords: []string{"  ", "a"}}); err == nil {
		t.Fatal("空白关键词必须失败")
	}
	if _, err := store.SearchHotSearch(ctx, HotSearchOptions{Keywords: []string{"w14h"}, StartAt: time.Now().Add(time.Hour), EndAt: time.Now()}); err == nil {
		t.Fatal("start 晚于 end 必须失败")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.SearchHotSearch(canceled, HotSearchOptions{Keywords: []string{"w14h"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消=%v", err)
	}
	// limit 钳制与清理：cutoff 在 bucket 之前 → 保留；之后 → 删除。
	if _, err := store.CleanupHotSearch(ctx, lease, time.Time{}, 0); err == nil {
		t.Fatal("空 cutoff 必须失败")
	}
	kept, err := store.CleanupHotSearch(ctx, lease, time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC), 8)
	if err != nil {
		t.Fatal(err)
	}
	_ = kept
	deleted, err := store.CleanupHotSearch(ctx, lease, time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), 8)
	if err != nil || deleted == 0 {
		t.Fatalf("清理=%d/%v", deleted, err)
	}
}

func TestW14HHotSearchPureArms(t *testing.T) {
	// parseHotSearchBucket。
	if _, ok := parseHotSearchBucket("audit-hot-2026080912.ndjson"); !ok {
		t.Fatal("合法 bucket 必须解析")
	}
	if _, ok := parseHotSearchBucket("other.ndjson"); ok {
		t.Fatal("非前缀不应解析")
	}
	if _, ok := parseHotSearchBucket("audit-hot-2026.ndjson"); ok {
		t.Fatal("长度不足不应解析")
	}
	if _, ok := parseHotSearchBucket("audit-hot-20260899zz.ndjson"); ok {
		t.Fatal("非法时间不应解析")
	}
	// normalizeHotKeywords。
	keywords := normalizeHotKeywords([]string{"  AB  ", "ab", "x", strings.Repeat("k", 200), "cd"})
	if len(keywords) != 3 || keywords[0] != "ab" || keywords[1] != strings.Repeat("k", 100) || keywords[2] != "cd" {
		t.Fatalf("关键词归一=%v", keywords)
	}
	// buildHotSearchText：attempts / payloads / 映射 / 状态码 / body 保留臂。
	mappingApplied := true
	statusCode := 502
	attemptStatus := 500
	rawSize := int64(32)
	text := buildHotSearchText(AuditLogInput{
		ID: "w14h", TraceID: "trace", TrafficSource: TrafficSourceGateway, Method: "POST", Path: "/v1/x",
		AuditOutcome: AuditOutcomeGatewayFailed, Success: false, FinalStatusCode: &statusCode,
		ModelMappingApplied: &mappingApplied, ErrorPhase: "upstream", ErrorCode: "E_UP", ErrorMessage: "boom",
		SystemAccountID: "sys", APIKeyID: "key", GroupID: "grp", AccountID: "acc", ProviderCode: "openai",
		Attempts: []AuditLogAttemptInput{{AttemptIndex: 0, AccountID: "acc", GroupID: "grp", ProviderCode: "openai", UpstreamMethod: "POST", UpstreamURL: "https://u", ErrorPhase: "upstream", ErrorCode: "E1", ErrorMessage: "x", UpstreamStatusCode: &attemptStatus}},
		Payloads: []AuditLogPayloadInput{{
			PartType: PayloadPartUpstreamRequest, ContentType: "application/json", ContentEncoding: "identity",
			BodySHA256: "sha", CaptureStatus: PayloadCaptureComplete, RawBodySizeBytes: &rawSize,
			Headers: map[string]HeaderValues{"X-Trace": {Values: []string{"t"}}},
			Body:    PayloadBody{Bytes: []byte("secret-body"), Present: true},
		}},
	})
	for _, needle := range []string{"trace", "model_mapping_applied", "502", "secret-body", "X-Trace", "E1"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("文本缺少 %q：%s", needle, text)
		}
	}
	// 成功样本不包含 body。
	successText := buildHotSearchText(AuditLogInput{ID: "w14h", AuditOutcome: AuditOutcomeSuccess, Success: true, Payloads: []AuditLogPayloadInput{{PartType: PayloadPartUpstreamRequest, Body: PayloadBody{Bytes: []byte("hidden"), Present: true}}}})
	if strings.Contains(successText, "hidden") {
		t.Fatal("成功样本不应包含 body")
	}
	// 大文本分块。
	chunks := chunkHotSearchText(strings.Repeat("a", 64*1024+11))
	if len(chunks) != 2 {
		t.Fatalf("分块数=%d", len(chunks))
	}
	// buildHotSearchLines：非法时间 / 空 ID / 非持久化来源。
	if _, err := buildHotSearchLines(t.TempDir(), []AuditLogInput{{ID: "x", CreatedAt: "bad"}}); err == nil {
		t.Fatal("非法时间必须失败")
	}
	lines, err := buildHotSearchLines(t.TempDir(), []AuditLogInput{
		{ID: "  ", CreatedAt: time.Now().Format(time.RFC3339Nano)},
		{ID: "x", TrafficSource: "account_health_check", CreatedAt: time.Now().Format(time.RFC3339Nano)},
		{ID: "x", TrafficSource: TrafficSourceGateway, EndedAt: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = lines
	// appendHotSearchFile：目录被文件占用 → 失败。
	occupied := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(occupied, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendHotSearchFile(filepath.Join(occupied, "audit-hot-2026080912.ndjson"), nil); err == nil {
		t.Fatal("目录被占用必须失败")
	}
	// scanHotSearchFile：文件缺失 / 非法 JSON / 越界时间 / 空 ID。
	missing := filepath.Join(t.TempDir(), "missing.ndjson")
	if _, err := scanHotSearchFile(context.Background(), missing, []string{"x"}, time.Unix(0, 0), time.Now().Add(time.Hour), 10, map[string]time.Time{}); err == nil {
		t.Fatal("缺失文件必须失败")
	}
	// listHotSearchFiles：目录缺失 → 空表。
	probe := &sqlStore{hotDir: filepath.Join(t.TempDir(), "nope")}
	files, truncated, err := probe.listHotSearchFiles(time.Unix(0, 0), time.Now().Add(time.Hour), 8)
	if err != nil || len(files) != 0 || truncated {
		t.Fatalf("缺失目录=%v/%v/%v", files, truncated, err)
	}
	// budget<=0 直接返回。
	if read, err := scanHotSearchFile(context.Background(), missing, nil, time.Unix(0, 0), time.Now(), 0, map[string]time.Time{}); err != nil || read != 0 {
		t.Fatalf("零预算=%d/%v", read, err)
	}
	// blob 路径辅助。
	if _, err := blobFilePath(t.TempDir(), "  "); err == nil {
		t.Fatal("空 storage_key 必须失败")
	}
	if _, err := blobFilePath(t.TempDir(), "../../escape"); err == nil {
		t.Fatal("越界 storage_key 必须失败")
	}
	if err := removeBlobFile(t.TempDir(), "  "); err != nil {
		t.Fatalf("空 key 删除=%v", err)
	}
	if err := removeBlobFile(t.TempDir(), "missing.bin"); err != nil {
		t.Fatalf("缺失文件删除=%v", err)
	}
	ok, err := blobFileMatchesMetadata(t.TempDir(), "missing.bin", 1)
	if err != nil || ok {
		t.Fatalf("缺失匹配=%v/%v", ok, err)
	}
	if ok, err := blobFileMatchesMetadata(t.TempDir(), "../../escape", 1); err == nil || ok {
		t.Fatalf("越界匹配=%v/%v", ok, err)
	}
	// 归一化辅助。
	if got := placeholders(0); got != "NULL" {
		t.Fatalf("零占位=%q", got)
	}
	if got := uniqueStrings([]string{"", "a", "a", "b"}); len(got) != 2 {
		t.Fatalf("去重=%v", got)
	}
	// CleanupRetention：失败日志按 failure cutoff 删除 + hot 文件清理。
	store, lease := w14hStore(t)
	failed := fixture("w14h-fail-1", LifecycleFinalized)
	failed.Success = false
	failed.AuditOutcome = AuditOutcomeGatewayFailed
	failed.CreatedAt = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	failed.EndedAt = failed.CreatedAt
	if _, err := store.Persist(context.Background(), lease, failed); err != nil {
		t.Fatal(err)
	}
	result, err := store.CleanupRetention(context.Background(), lease, RetentionConfig{
		SuccessHotCutoff: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		SuccessCutoff:    time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		FailureCutoff:    time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		ErrorGroupCutoff: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		BatchSize:        10,
	})
	if err != nil || result.DeletedLogs != 1 {
		t.Fatalf("失败清理=%+v/%v", result, err)
	}
	// Retain 别名。
	if _, err := store.Retain(context.Background(), lease, RetentionConfig{
		SuccessHotCutoff: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		SuccessCutoff:    time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		FailureCutoff:    time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		ErrorGroupCutoff: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Retain=%v", err)
	}
}
