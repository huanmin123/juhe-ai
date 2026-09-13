package auditlog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWIValidateInputFieldMatrix(t *testing.T) {
	valid := fixture("wi-matrix", LifecycleFinalized)
	mutations := map[string]func(*AuditLogInput){
		"缺 startedAt":      func(i *AuditLogInput) { i.StartedAt = " " },
		"缺 sampleReason":   func(i *AuditLogInput) { i.SampleReason = "" },
		"非法 auditOutcome":  func(i *AuditLogInput) { i.AuditOutcome = "weird" },
		"非法 trafficSource": func(i *AuditLogInput) { i.TrafficSource = "weird" },
		"非法 lifecycle":     func(i *AuditLogInput) { i.LifecycleStatus = "weird" },
		"非法 captureStatus": func(i *AuditLogInput) { i.CaptureStatus = "weird" },
		"非法 path 缺失":       func(i *AuditLogInput) { i.Path = "  " },
	}
	for name, mutate := range mutations {
		input := valid
		mutate(&input)
		if err := validateInput(input); err == nil {
			t.Fatalf("%s 必须报错", name)
		}
	}
	// payload 维度的非法矩阵。
	badPart := valid
	badPart.Payloads = []AuditLogPayloadInput{{PartType: "weird"}}
	if err := validateInput(badPart); err == nil {
		t.Fatal("非法 payload partType 必须报错")
	}
	badCapture := valid
	badCapture.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayResponse, CaptureStatus: "weird"}}
	if err := validateInput(badCapture); err == nil {
		t.Fatal("非法 payload captureStatus 必须报错")
	}
	// canonicalAuditTime：空/非法/合法。
	if _, err := canonicalAuditTime("", "f", false); err == nil {
		t.Fatal("空必填时间必须报错")
	}
	if got, err := canonicalAuditTime("", "f", true); err != nil || got != "" {
		t.Fatalf("空可选时间=%q err=%v", got, err)
	}
	if _, err := canonicalAuditTime("not-a-time", "f", false); err == nil {
		t.Fatal("非法时间必须报错")
	}
	canonical, err := canonicalAuditTime("2026-09-10T16:30:00+08:00", "f", false)
	if err != nil || !strings.HasPrefix(canonical, "2026-09-10T08:30:00") {
		t.Fatalf("canonical=%q err=%v", canonical, err)
	}
}

func TestWISearchHotSearchContract(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	store := openSQLiteStore(t, cfg)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)

	// 无关键词必须报错。
	if _, err := store.SearchHotSearch(ctx, HotSearchOptions{}); err == nil {
		t.Fatal("无关键词必须报错")
	}
	// start 晚于 end 必须报错。
	if _, err := store.SearchHotSearch(ctx, HotSearchOptions{
		Keywords: []string{"error"}, StartAt: time.Now().UTC().Add(time.Hour), EndAt: time.Now().UTC(),
	}); err == nil {
		t.Fatal("startAt 晚于 endAt 必须报错")
	}

	// 写入两条可检索记录（不同 TraceID 保持可区分）。
	hit := fixture("wi-search-hit", LifecycleFinalized)
	hit.ErrorCode = "WI_SEARCH_NEEDLE"
	miss := fixture("wi-search-miss", LifecycleFinalized)
	if _, err := store.AppendHotSearch(ctx, lease, []AuditLogInput{hit, miss}); err != nil {
		t.Fatalf("写入 hot search 失败: %v", err)
	}

	// 命中检索（fixture 的 created 落在固定日期，窗口按它计算）。
	created, _ := time.Parse(time.RFC3339Nano, hit.CreatedAt)
	result, err := store.SearchHotSearch(ctx, HotSearchOptions{
		Keywords: []string{"wi_search_needle"},
		StartAt:  created.Add(-time.Hour), EndAt: created.Add(time.Hour), Limit: 10,
	})
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	if len(result.AuditLogIDs) != 1 || result.AuditLogIDs[0] != hit.ID {
		t.Fatalf("result=%+v", result)
	}
	// limit 截断标记。
	limitResult, err := store.SearchHotSearch(ctx, HotSearchOptions{
		Keywords: []string{"post"}, StartAt: created.Add(-time.Hour), EndAt: created.Add(time.Hour), Limit: 1,
	})
	if err != nil {
		t.Fatalf("limit 检索失败: %v", err)
	}
	if len(limitResult.AuditLogIDs) == 0 {
		t.Fatalf("limit result=%+v", limitResult)
	}
	// 只有全部关键词为空（短于 2 rune）时报错。
	if _, err := store.SearchHotSearch(ctx, HotSearchOptions{Keywords: []string{" ", "x"}}); err == nil {
		t.Fatal("全部关键词无效必须报错")
	}
}

func TestWIOpenStoreRejectsDirectoryAsDatabase(t *testing.T) {
	root := t.TempDir()
	cfg := sqliteConfig(t, root)
	// 数据库路径指向一个目录 → 打开失败（PRAGMA 阶段报错）。
	if err := os.MkdirAll(cfg.AuditDatabasePath, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(cfg); err == nil {
		t.Fatal("目录作为数据库必须打开失败")
	}
	_ = filepath.Join(root, "unused")
}

func TestWIBlobFileMetadataGuards(t *testing.T) {
	dir := t.TempDir()
	storageKey := "2026/09/blob.bin"
	full := filepath.Join(dir, "2026", "09", "blob.bin")
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	// 文件不存在 → present=false 且无错。
	present, err := blobFileMatchesMetadata(dir, storageKey, 4)
	if err != nil || present {
		t.Fatalf("缺失文件=%v err=%v", present, err)
	}
	// 大小不匹配必须报错（不一致是数据完整性问题而非缺失）。
	if err := os.WriteFile(full, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := blobFileMatchesMetadata(dir, storageKey, 4); err == nil {
		t.Fatal("大小不匹配必须报错")
	}
	// 匹配 → present=true。
	if err := os.WriteFile(full, []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}
	present, err = blobFileMatchesMetadata(dir, storageKey, 4)
	if err != nil || !present {
		t.Fatalf("匹配=%v err=%v", present, err)
	}
	// blobFilePath 拼接。
	resolved, err := blobFilePath(dir, storageKey)
	if err != nil || resolved != full {
		t.Fatalf("blobFilePath=%q err=%v", resolved, err)
	}
	// 空 storage_key 必须报错。
	if _, err := blobFilePath(dir, "  "); err == nil {
		t.Fatal("空 storage_key 必须报错")
	}
}

func TestWIStoreCloseIsIdempotent(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	if err := store.Close(); err != nil {
		t.Fatalf("首次关闭失败: %v", err)
	}
	// 二次关闭必须安全。
	if err := store.Close(); err != nil {
		t.Fatalf("二次关闭失败: %v", err)
	}
}
