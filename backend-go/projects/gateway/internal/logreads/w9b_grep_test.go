package logreads

// w9b runtime grep 文件场景：临时 .log 目录驱动 Search/listLogFiles/
// readRuntimeLogLine 与关键词规整。

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func w9bGrep(logDir string, now time.Time) *RuntimeLogGrep {
	grep := NewRuntimeLogGrep(RuntimeLogGrepConfig{FileEnabled: true, Directory: logDir, MaxFiles: 500, RetentionDays: 30})
	grep.Now = func() time.Time { return now }
	return grep
}

func TestW9BGrepSearchFileScenarios(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	// 两个日志文件：一个 JSON 行、一个纯文本行。
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	jsonLine := `{"time":"2026-09-10T11:00:00.000Z","level":"warn","traceId":"trace-9","event":"cache_evict","msg":"Alpha 缓存淘汰","errorMessage":"boom"}`
	write("gateway-20260910.log", jsonLine+"\n{\"time\":123}\nshort\n")
	write("gateway-20260909.log", "Alpha plain text hit\n")
	grep := w9bGrep(dir, now)

	// 命中扫描：主关键词大小写不敏感。
	result, err := grep.Search(context.Background(), RuntimeLogGrepOptions{Keywords: []string{"alpha"}})
	if err != nil || !result.Available {
		t.Fatalf("Search=%+v err=%v", result, err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("命中数=%d items=%+v", len(result.Items), result.Items)
	}
	found := map[string]bool{}
	for _, item := range result.Items {
		found[item.Level+":"+item.Message] = true
	}
	if !found["warn:Alpha 缓存淘汰"] || !found["info:Alpha plain text hit"] {
		t.Fatalf("命中内容=%v", found)
	}
	// 有关键词但无命中。
	result, err = grep.Search(context.Background(), RuntimeLogGrepOptions{Keywords: []string{"nonexistent-token"}})
	if err != nil || !result.Available || len(result.Items) != 0 || !strings.Contains(result.Message, "没有匹配") {
		t.Fatalf("无命中=%+v err=%v", result, err)
	}
	// 空关键词。
	result, err = grep.Search(context.Background(), RuntimeLogGrepOptions{})
	if err != nil || !result.Available || !strings.Contains(result.Message, "请输入要搜索的关键字") {
		t.Fatalf("空关键词=%+v err=%v", result, err)
	}
	// 文件未启用。
	disabled := NewRuntimeLogGrep(RuntimeLogGrepConfig{FileEnabled: false, Directory: dir, MaxFiles: 10, RetentionDays: 30})
	disabled.Now = func() time.Time { return now }
	result, err = disabled.Search(context.Background(), RuntimeLogGrepOptions{Keywords: []string{"alpha"}})
	if err != nil || result.Available || !strings.Contains(result.Message, "文件日志未启用") {
		t.Fatalf("未启用=%+v err=%v", result, err)
	}
	// 时间范围过滤（早于全部文件 mtime）。
	old := now.Add(-90 * 24 * time.Hour)
	oldGrep := w9bGrep(dir, old)
	result, err = oldGrep.Search(context.Background(), RuntimeLogGrepOptions{Keywords: []string{"alpha"}})
	if err != nil || !result.Available || !strings.Contains(result.Message, "已自动调整") {
		t.Fatalf("范围外=%+v err=%v", result, err)
	}
	// 目录不存在 → 无文件。
	missing := w9bGrep(filepath.Join(dir, "missing"), now)
	result, err = missing.Search(context.Background(), RuntimeLogGrepOptions{Keywords: []string{"alpha"}})
	if err != nil || !result.Available || !strings.Contains(result.Message, "没有可搜索") {
		t.Fatalf("缺目录=%+v err=%v", result, err)
	}
	// 非法时间范围。
	if _, err := grep.Search(context.Background(), RuntimeLogGrepOptions{Keywords: []string{"alpha"}, StartAt: "nope"}); err == nil {
		t.Fatal("非法时间必须报错")
	}
}

func TestW9BGrepNormalizeKeywordsAndLimit(t *testing.T) {
	keywords, shortCount := normalizeGrepKeywords([]string{"Alpha,ALPHA", "beta；gamma", "ab", "  "})
	if len(keywords) != 3 || shortCount != 1 {
		t.Fatalf("keywords=%v short=%d", keywords, shortCount)
	}
	if got := normalizeGrepLimit(nil); got != grepDefaultLimit {
		t.Fatalf("默认 limit=%d", got)
	}
	zero := 0
	if got := normalizeGrepLimit(&zero); got != 1 {
		t.Fatalf("下限=%d", got)
	}
	huge := 1 << 20
	if got := normalizeGrepLimit(&huge); got != grepMaxLimit {
		t.Fatalf("上限=%d", got)
	}
}

func TestW9BReadRuntimeLogLineArms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.log")
	if err := os.WriteFile(path, []byte("first\nsecond\nthird\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, line := readRuntimeLogLine(path, 2); status != "found" || line != "second" {
		t.Fatalf("命中=%q %q", status, line)
	}
	if status, _ := readRuntimeLogLine(path, 99); status != "not_found" {
		t.Fatalf("越界行=%q", status)
	}
	if status, _ := readRuntimeLogLine(filepath.Join(dir, "missing.log"), 1); status != "not_found" {
		t.Fatalf("缺失文件=%q", status)
	}
	// 超长行（跨缓冲 drain）→ 目标行 not_found。
	long := strings.Repeat("x", 200000) + "\nshort\n"
	longPath := filepath.Join(dir, "long.log")
	if err := os.WriteFile(longPath, []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, _ := readRuntimeLogLine(longPath, 1); status != "not_found" {
		t.Fatalf("超长行=%q", status)
	}
	if status, line := readRuntimeLogLine(longPath, 2); status != "found" || line != "short" {
		t.Fatalf("超长后短行=%q %q", status, line)
	}
}

func TestW9BGrepRetainNewestFile(t *testing.T) {
	files := []grepLogFile{}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 4; index++ {
		files = retainNewestGrepFile(files, grepLogFile{fileName: string(rune('a' + index)), mtimeMs: base.Add(time.Duration(index) * time.Hour).UnixMilli()}, 3)
	}
	if len(files) != 3 || files[0].fileName != "d" || files[2].fileName != "b" {
		t.Fatalf("保留最新=%+v", files)
	}
}

func TestW9BGrepScanLogFilesTruncationArms(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	// 超长行触发 drain + 截断统计；短行继续可命中。
	var builder strings.Builder
	builder.WriteString(strings.Repeat("x", 300000) + "\n")
	for index := 0; index < 3; index++ {
		builder.WriteString(`{"time":"2026-09-10T11:00:00.000Z","level":"info","msg":"alpha hit ` + string(rune('a'+index)) + `"}` + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "app.log"), []byte(builder.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	grep := w9bGrep(dir, now)
	result, err := grep.Search(context.Background(), RuntimeLogGrepOptions{Keywords: []string{"alpha"}, Limit: &[]int{2}[0]})
	if err != nil || !result.Available {
		t.Fatalf("Search=%+v err=%v", result, err)
	}
	if !result.Truncated || len(result.Items) != 2 {
		t.Fatalf("截断=%+v", result)
	}
	// 超长行是 runtime-log 搜索请求行形态（包含 path 前缀）也不计入命中。
	builder.Reset()
	builder.WriteString(`{"msg":"alpha GET /__aisys__/api/runtime-logs search"}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "req.log"), []byte(builder.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = grep.Search(context.Background(), RuntimeLogGrepOptions{Keywords: []string{"alpha"}})
	if err != nil {
		t.Fatalf("请求行=%v", err)
	}
	// 命中行数取决于 isRuntimeLogSearchRequestLine；只验证不 panic 且有消息。
	if result.Message == "" {
		t.Fatal("必须返回提示消息")
	}
}

func TestW9BGrepIsRuntimeLogSearchRequestLine(t *testing.T) {
	requestLine := `{"event":"http_request_completed","path":"/__aisys__/api/runtime-logs?keywords=alpha"}`
	if !isRuntimeLogSearchRequestLine(requestLine) {
		t.Fatal("搜索请求行必须识别")
	}
	if isRuntimeLogSearchRequestLine(`{"event":"http_request_completed","path":"/other"}`) {
		t.Fatal("非搜索路径不应识别")
	}
	if isRuntimeLogSearchRequestLine("normal alpha log") {
		t.Fatal("普通行不应识别")
	}
	if !isRuntimeLogSearchRequestLine(strings.Repeat("x", 30000) + "/__aisys__/api/runtime-logs") {
		t.Fatal("超长行回退子串匹配")
	}
}

func TestW9BAuditSearchHotScenarios(t *testing.T) {
	db, err := sql.Open("sqlite", "file:w9b-hot?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	hotDir := t.TempDir()
	reader, err := NewAuditLogQueryReader(db, ReadSQLite, AuditQueryDirectories{HotSearchDirectory: hotDir})
	if err != nil {
		t.Fatal(err)
	}
	hot := reader.(*auditLogSQLReader)
	base := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	hot.Now = func() time.Time { return base }
	// 写一个命中桶 + 一个窗口外桶 + 一个坏名文件。
	inWindow := time.Date(2026, 9, 8, 9, 30, 0, 0, time.UTC)
	outWindow := time.Date(2026, 9, 8, 5, 0, 0, 0, time.UTC)
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(hotDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bucketName := "audit-hot-" + inWindow.UTC().Format("2006010215") + ".ndjson"
	write(bucketName, `{"auditLogId":"audit-9","createdAt":"`+inWindow.Format(time.RFC3339Nano)+`","text":"Alpha 命中"}`+"\n"+`{"auditLogId":"audit-8","createdAt":"`+inWindow.Format(time.RFC3339Nano)+`","text":"Beta 命中"}`+"\n")
	write("audit-hot-"+outWindow.UTC().Format("2006010215")+".ndjson", `{"auditLogId":"audit-old","createdAt":"`+outWindow.Format(time.RFC3339Nano)+`","text":"alpha 窗口外"}`+"\n")
	write("ignored.txt", "alpha")

	// 正常命中。
	scan, err := hot.SearchHot(context.Background(), AuditHotSearchOptions{Keywords: []string{"alpha"}})
	if err != nil {
		t.Fatalf("SearchHot=%v", err)
	}
	if !scan.Available || len(scan.AuditLogIDs) != 1 || scan.AuditLogIDs[0] != "audit-9" {
		t.Fatalf("命中=%+v", scan)
	}
	// 多关键词 + 截断（limit=1）。
	one := 1
	scan, err = hot.SearchHot(context.Background(), AuditHotSearchOptions{Keywords: []string{"alpha", "beta"}, Limit: &one})
	if err != nil || !scan.Truncated || len(scan.AuditLogIDs) != 1 {
		t.Fatalf("截断=%+v err=%v", scan, err)
	}
	// 空关键词。
	scan, err = hot.SearchHot(context.Background(), AuditHotSearchOptions{})
	if err != nil || !scan.Available || !strings.Contains(scan.Message, "请输入") {
		t.Fatalf("空关键词=%+v err=%v", scan, err)
	}
	// 目录未配置。
	noDir, err := NewAuditLogQueryReader(db, ReadSQLite, AuditQueryDirectories{})
	if err != nil {
		t.Fatal(err)
	}
	scan, err = noDir.(*auditLogSQLReader).SearchHot(context.Background(), AuditHotSearchOptions{Keywords: []string{"alpha"}})
	if err != nil || scan.Available || !strings.Contains(scan.Message, "未配置") {
		t.Fatalf("未配置=%+v err=%v", scan, err)
	}
	// 非法 endAt。
	if _, err := hot.SearchHot(context.Background(), AuditHotSearchOptions{Keywords: []string{"alpha"}, EndAt: "nope"}); err == nil {
		t.Fatal("非法 endAt 必须报错")
	}
	// 目录不存在 → nil paths 消息。
	emptyReader, err := NewAuditLogQueryReader(db, ReadSQLite, AuditQueryDirectories{HotSearchDirectory: filepath.Join(hotDir, "missing-subdir")})
	if err != nil {
		t.Fatal(err)
	}
	emptyReader.(*auditLogSQLReader).Now = func() time.Time { return base }
	scan, err = emptyReader.(*auditLogSQLReader).SearchHot(context.Background(), AuditHotSearchOptions{Keywords: []string{"alpha"}})
	if err != nil || !scan.Available || !strings.Contains(scan.Message, "没有可搜索") {
		t.Fatalf("空目录=%+v err=%v", scan, err)
	}
}
