package logreads

// w11f 覆盖波次（文件 1/2）：纯函数分支 + grep 扫描分支。

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func w11fQueryRequest(query string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/__aisys__/api/x?"+query, nil)
}

func TestW11FAuditReadHelpers(t *testing.T) {
	// readQueryInt: 空 / 非数字 / 小数 / 越界 / 合法。
	if readQueryInt(w11fQueryRequest(""), "page") != nil {
		t.Fatal("empty int drift")
	}
	if readQueryInt(w11fQueryRequest("page=abc"), "page") != nil {
		t.Fatal("garbage int drift")
	}
	if readQueryInt(w11fQueryRequest("page=1.5"), "page") != nil {
		t.Fatal("fraction int drift")
	}
	if readQueryInt(w11fQueryRequest("page=1e400"), "page") != nil {
		t.Fatal("inf int drift")
	}
	if readQueryInt(w11fQueryRequest("page=9e16"), "page") != nil {
		t.Fatal("bound int drift")
	}
	if value := readQueryInt(w11fQueryRequest("page=42"), "page"); value == nil || *value != 42 {
		t.Fatal("valid int drift")
	}
	// readQueryStatusCode 范围。
	if readQueryStatusCode(w11fQueryRequest("status=99"), "status") != nil {
		t.Fatal("below 100 drift")
	}
	if readQueryStatusCode(w11fQueryRequest("status=600"), "status") != nil {
		t.Fatal("above 599 drift")
	}
	if value := readQueryStatusCode(w11fQueryRequest("status=404"), "status"); value == nil || *value != 404 {
		t.Fatal("valid status drift")
	}
	// readCanonicalInstant。
	if _, ok := readCanonicalInstant("zzz"); ok {
		t.Fatal("bad instant drift")
	}
	if canonical, ok := readCanonicalInstant("2026-01-02T10:00:00+08:00"); !ok || canonical != "2026-01-02T02:00:00.000Z" {
		t.Fatalf("instant = %q", canonical)
	}
	// readQueryDateTimeRange: 反转交换 / 非法 start / 非法 end。
	startAt, endAt, err := readQueryDateTimeRange(w11fQueryRequest("startAt=2026-02-01T00:00:00Z&endAt=2026-01-01T00:00:00Z"))
	if err != nil || startAt != "2026-01-01T00:00:00.000Z" || endAt != "2026-02-01T00:00:00.000Z" {
		t.Fatalf("swapped range = %s/%s/%v", startAt, endAt, err)
	}
	if _, _, err := readQueryDateTimeRange(w11fQueryRequest("startAt=zzz")); err == nil {
		t.Fatal("bad startAt must fail")
	}
	if _, _, err := readQueryDateTimeRange(w11fQueryRequest("endAt=zzz")); err == nil {
		t.Fatal("bad endAt must fail")
	}
	// readNormalizePageSize / readNormalizePage / readNormalizeAuditLogPage。
	if readNormalizePageSize(nil, 20, 100) != 20 {
		t.Fatal("nil pageSize drift")
	}
	if readNormalizePageSize(intPtrW11F(0), 20, 100) != 20 {
		t.Fatal("zero pageSize drift")
	}
	if readNormalizePageSize(intPtrW11F(500), 20, 100) != 100 {
		t.Fatal("clamped pageSize drift")
	}
	if readNormalizePageSize(intPtrW11F(50), 20, 100) != 50 {
		t.Fatal("valid pageSize drift")
	}
	if readNormalizePage(nil, 20) != 1 {
		t.Fatal("nil page drift")
	}
	if readNormalizePage(intPtrW11F(0), 20) != 1 {
		t.Fatal("zero page drift")
	}
	if readNormalizePage(intPtrW11F(9999), 20) != 50 {
		t.Fatal("clamped page drift")
	}
	if readNormalizeAuditLogPage(nil, 20, "sess-1") != 1 {
		t.Fatal("session nil page drift")
	}
	if readNormalizeAuditLogPage(intPtrW11F(9999), 20, "sess-1") != 9999 {
		t.Fatal("session deep page drift")
	}
	// readPagedTotal / readPageRows / 前缀上界。
	if readPagedTotal(0, 0, -1, false) != 0 {
		t.Fatal("paged total clamp drift")
	}
	if readPagedTotal(2, 10, 5, true) != 16 {
		t.Fatal("paged total hasMore drift")
	}
	rows, hasMore := readPageRows([]int{1, 2, 3}, 2)
	if len(rows) != 2 || !hasMore {
		t.Fatal("page rows drift")
	}
	if rows, hasMore = readPageRows([]int{1}, 2); len(rows) != 1 || hasMore {
		t.Fatal("page rows short drift")
	}
	if got := readTextPrefixUpperBound(string(rune(0x10FFFF))); got != string(rune(0x10FFFF))+"\U0010FFFF" {
		t.Fatalf("max rune bound = %q", got)
	}
	if got := readTextPrefixUpperBound("ab"); got != "ac" {
		t.Fatalf("text bound = %q", got)
	}
	if got := readAuditPrefixUpperBound("a"); got != "a￿" {
		t.Fatalf("audit bound = %q", got)
	}
	// trimJSSpace / URL 文本辅助。
	if trimJSSpace(" x　") != "x" {
		t.Fatal("trimJSSpace drift")
	}
	if trimmed, ok := sanitizeUrlCredentialsForLog("  "); ok || trimmed != "" {
		t.Fatal("blank sanitize drift")
	}
	if trimmed, ok := sanitizeUrlCredentialsForLog(" http://a "); !ok || trimmed != "http://a" {
		t.Fatal("sanitize drift")
	}
	if attemptProxyURL("  ") != "" {
		t.Fatal("blank proxy drift")
	}
	if attemptProxyURL(" http://p ") != "http://p" {
		t.Fatal("proxy drift")
	}
	if attemptUpstreamURL("  http://u  ") != "http://u" {
		t.Fatal("upstream trim drift")
	}
	if attemptUpstreamURL("   ") != "   " {
		t.Fatal("upstream blank fallback drift")
	}
	// 数字辅助。
	if readOptionalNumber("x") != nil || readOptionalNumber(nil) != nil {
		t.Fatal("optional number drift")
	}
	if number := readOptionalNumber(int64(7)); number == nil || *number != 7 {
		t.Fatal("optional number hit drift")
	}
	if readNumberOr("x", 5) != 5 || readNumberOr(int64(3), 5) != 3 {
		t.Fatal("numberOr drift")
	}
	// readRawString 全形态。
	cases := map[string]struct {
		value any
		want  string
	}{
		"nil":     {nil, ""},
		"string":  {"s", "s"},
		"bytes":   {[]byte("b"), "b"},
		"time":    {time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "2026-01-02T03:04:05.000Z"},
		"int64":   {int64(12), "12"},
		"float64": {1.5, "1.5"},
		"bool":    {true, "true"},
		"other":   {struct{ X int }{9}, "{9}"},
	}
	for name, c := range cases {
		if got := readRawString(c.value); got != c.want {
			t.Fatalf("readRawString(%s) = %q want %q", name, got, c.want)
		}
	}
	if readOptionalText("  x  ") != "x" || readOptionalText(3) != "3" {
		t.Fatal("optional text drift")
	}
}

func intPtrW11F(v int) *int { return &v }

func TestW11FGrepHelpers(t *testing.T) {
	// normalizeGrepKeywords: 分隔符 / 截断 / 短词 / 去重 / 上限。
	keywords, shortCount := normalizeGrepKeywords([]string{"alpha, beta；gamma，delta   epsilon"})
	if len(keywords) != 5 || keywords[0] != "alpha" || keywords[4] != "epsilon" {
		t.Fatalf("keywords = %v", keywords)
	}
	if shortCount != 0 {
		t.Fatalf("shortCount = %d", shortCount)
	}
	keywords, shortCount = normalizeGrepKeywords([]string{"ab", "cd"})
	if len(keywords) != 0 || shortCount != 2 {
		t.Fatalf("short keywords = %v/%d", keywords, shortCount)
	}
	keywords, _ = normalizeGrepKeywords([]string{"ALPHA", "alpha"})
	if len(keywords) != 1 || keywords[0] != "ALPHA" {
		t.Fatalf("dedupe = %v", keywords)
	}
	keywords, _ = normalizeGrepKeywords([]string{strings.Repeat("长", 200)})
	if utf8RuneCount(keywords[0]) != 128 {
		t.Fatalf("truncated keyword = %d", utf8RuneCount(keywords[0]))
	}
	many := make([]string, 12)
	for i := range many {
		many[i] = "keyword" + string(rune('a'+i))
	}
	if keywords, _ = normalizeGrepKeywords(many); len(keywords) != 10 {
		t.Fatalf("keyword cap = %d", len(keywords))
	}
	// normalizeGrepLimit。
	if normalizeGrepLimit(nil) != grepDefaultLimit {
		t.Fatal("nil limit drift")
	}
	if normalizeGrepLimit(intPtrW11F(0)) != 1 || normalizeGrepLimit(intPtrW11F(grepMaxLimit+100)) != grepMaxLimit {
		t.Fatal("limit clamp drift")
	}
	// parseGrepInstantMillis。
	if _, err := parseGrepInstantMillis("zzz"); err == nil {
		t.Fatal("bad grep instant must fail")
	}
	// millisToISO。
	if millisToISO(0) != "1970-01-01T00:00:00.000Z" {
		t.Fatal("millisToISO drift")
	}
	// compareGrepItems 平局链。
	left := orderedGrepItem{sortTimeMs: 100, fileOrder: 1, item: RuntimeLogGrepItem{LineNumber: 5}}
	sameTime := orderedGrepItem{sortTimeMs: 100, fileOrder: 0, item: RuntimeLogGrepItem{LineNumber: 1}}
	if compareGrepItems(left, sameTime) {
		t.Fatal("file order tiebreak drift")
	}
	sameFile := orderedGrepItem{sortTimeMs: 100, fileOrder: 1, item: RuntimeLogGrepItem{LineNumber: 9}}
	if compareGrepItems(left, sameFile) {
		t.Fatal("line number tiebreak drift")
	}
	// retainNewestGrepFile: 插入与淘汰。
	files := retainNewestGrepFile(nil, grepLogFile{mtimeMs: 100}, 2)
	files = retainNewestGrepFile(files, grepLogFile{mtimeMs: 300}, 2)
	if files[0].mtimeMs != 300 {
		t.Fatalf("retain order = %v", files)
	}
	files = retainNewestGrepFile(files, grepLogFile{mtimeMs: 200}, 2)
	if len(files) != 2 || files[1].mtimeMs != 200 {
		t.Fatalf("retain insert = %v", files)
	}
	files = retainNewestGrepFile(files, grepLogFile{mtimeMs: 50}, 2)
	if len(files) != 2 || files[1].mtimeMs != 200 {
		t.Fatalf("retain drop = %v", files)
	}
	// isRuntimeLogSearchRequestLine / isRuntimeLogSearchPath。
	longLine := `{"originalUrl":"` + strings.Repeat("x", 20001) + `/__aisys__/api/runtime-logs"}`
	if !isRuntimeLogSearchRequestLine(longLine) {
		t.Fatal("long search line drift")
	}
	if isRuntimeLogSearchRequestLine("not json") {
		t.Fatal("non json drift")
	}
	if isRuntimeLogSearchRequestLine(`{"event":"other","path":"/__aisys__/api/runtime-logs"}`) {
		t.Fatal("wrong event drift")
	}
	if !isRuntimeLogSearchRequestLine(`{"event":"http_request_completed","path":"/__aisys__/api/runtime-logs/grep?x=1"}`) {
		t.Fatal("path match drift")
	}
	if !isRuntimeLogSearchRequestLine(`{"event":"http_request_closed","originalUrl":"/__aisys__/api/runtime-logs/"}`) {
		t.Fatal("trailing slash drift")
	}
	if isRuntimeLogSearchPath("") || isRuntimeLogSearchPath("/other") {
		t.Fatal("search path drift")
	}
}

func utf8RuneCount(v string) int { return len([]rune(v)) }

func TestW11FGrepTimeRange(t *testing.T) {
	g := NewRuntimeLogGrep(RuntimeLogGrepConfig{FileEnabled: true, Directory: t.TempDir()})
	now := time.Date(2026, 6, 3, 13, 0, 0, 0, time.UTC)
	g.Now = func() time.Time { return now }
	old := now.Add(-48 * time.Hour).UnixMilli()
	older := now.Add(-96 * time.Hour).UnixMilli()
	files := []grepLogFile{{mtimeMs: older}, {mtimeMs: old}}

	// 非法 endAt / startAt。
	if _, err := g.normalizeGrepTimeRange("", "zzz", files); err == nil {
		t.Fatal("bad endAt must fail")
	}
	if _, err := g.normalizeGrepTimeRange("zzz", "", files); err == nil {
		t.Fatal("bad startAt must fail")
	}
	// 未来 endAt → 钳制到 now。
	timeRange, err := g.normalizeGrepTimeRange("", now.Add(time.Hour).Format(time.RFC3339), files)
	if err != nil || !timeRange.adjusted || timeRange.endMs != now.UnixMilli() {
		t.Fatalf("future endAt = %+v/%v", timeRange, err)
	}
	// 显式 endAt 早于最早文件 → 提到最早文件。
	timeRange, err = g.normalizeGrepTimeRange("", time.UnixMilli(older).Add(-2*time.Hour).Format(time.RFC3339), files)
	if err != nil || timeRange.endMs != older {
		t.Fatalf("early endAt = %+v/%v", timeRange, err)
	}
	// 默认 endAt 早于最新文件 → 提到最新文件。
	lateFiles := []grepLogFile{{mtimeMs: now.Add(-1 * time.Hour).UnixMilli()}}
	g.Now = func() time.Time { return now.Add(-48 * time.Hour) }
	timeRange, err = g.normalizeGrepTimeRange("", "", lateFiles)
	if err != nil || timeRange.endMs != lateFiles[0].mtimeMs {
		t.Fatalf("default endAt bump = %+v/%v", timeRange, err)
	}
	g.Now = func() time.Time { return now }
	// startAt 显式早于最早文件 → 提到最早文件。
	timeRange, err = g.normalizeGrepTimeRange(time.UnixMilli(older-86400000*10).Format(time.RFC3339), "", files)
	if err != nil || timeRange.startMs != older {
		t.Fatalf("early startAt = %+v/%v", timeRange, err)
	}
	// startAt 晚于 endAt → 重算。
	timeRange, err = g.normalizeGrepTimeRange(now.Add(-1*time.Hour).Format(time.RFC3339), now.Add(-2*time.Hour).Format(time.RFC3339), nil)
	if err != nil || !timeRange.adjusted || timeRange.startMs > timeRange.endMs {
		t.Fatalf("reversed start = %+v/%v", timeRange, err)
	}
	// 超过 7 天 → 收缩。
	timeRange, err = g.normalizeGrepTimeRange(now.Add(-30*24*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339), nil)
	if err != nil || timeRange.endMs-timeRange.startMs > grepMaxRangeDays*grepDayMillis {
		t.Fatalf("oversized range = %+v/%v", timeRange, err)
	}
}

func TestW11FGrepSearchBranches(t *testing.T) {
	env := newReadsTestEnv(t, runtimeReadsDDL, nil, true)
	pinGrepClock(env)
	ctx := context.Background()

	// 无关键字（含短词提示）。
	result, err := env.grep.Search(ctx, RuntimeLogGrepOptions{})
	if err != nil || !result.Available || result.Message != "请输入要搜索的关键字" {
		t.Fatalf("empty keywords = %+v/%v", result, err)
	}
	result, err = env.grep.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"ab"}})
	if err != nil || !strings.Contains(result.Message, "至少需要 3 个字符") {
		t.Fatalf("short keywords = %+v/%v", result, err)
	}
	// 文件未启用。
	disabled := NewRuntimeLogGrep(RuntimeLogGrepConfig{FileEnabled: false})
	disabled.Now = func() time.Time { return grepPinnedNow }
	result, err = disabled.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"keyword"}})
	if err != nil || !strings.Contains(result.Message, "文件日志未启用") {
		t.Fatalf("disabled = %+v/%v", result, err)
	}
	// 目录不存在 → 无可搜索文件。
	env.grep.Now = func() time.Time { return grepPinnedNow }
	result, err = env.grep.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"keyword"}})
	if err != nil || !result.Available || !strings.Contains(result.Message, "没有可搜索的日志文件") {
		t.Fatalf("missing dir = %+v/%v", result, err)
	}
	// 时间范围外文件 → 范围内无可搜索文件。
	env.grep.cfg.Directory = env.logDir
	os.WriteFile(filepath.Join(env.logDir, "w11f-range.log"), []byte("keyword hit\n"), 0o640)
	stamp := grepPinnedNow.Add(-30 * 24 * time.Hour)
	os.Chtimes(filepath.Join(env.logDir, "w11f-range.log"), stamp, stamp)
	env.grep.Now = func() time.Time { return grepPinnedNow }
	result, err = env.grep.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"keyword"}})
	if err != nil || !strings.Contains(result.Message, "没有可搜索的日志文件") && !strings.Contains(result.Message, "当前文件时间范围内没有可搜索的日志文件") {
		t.Fatalf("range filtered = %+v/%v", result, err)
	}
	// 命中 + 截断 + 无主关键字提示。
	writeGrepLogFile(t, env, "w11f-hit.log",
		`{"time":"2026-06-03T12:30:00.000Z","level":"error","message":"payment crashed hard"}`,
		`{"time":"2026-06-03T12:31:00.000Z","level":"info","message":"payment retried hard"}`,
		`{"time":"2026-06-03T12:32:00.000Z","level":"info","message":"unrelated line"}`)
	result, err = env.grep.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"payment", "hard"}, Limit: intPtrW11F(1)})
	if err != nil || !result.Available || len(result.Items) != 1 || !result.Truncated {
		t.Fatalf("hit truncated = %+v/%v", result, err)
	}
	// 命中但主关键字缺失（rg 无主命中提示）。
	result, err = env.grep.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"payment", "hard", "zzz-not-in-lines-long-enough"}})
	if err != nil {
		t.Fatal(err)
	}
	// 全词同时出现才命中: 单词不出现。
	result, err = env.grep.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"payment", "absent-keyword"}})
	if err != nil || len(result.Items) != 0 || !strings.Contains(result.Message, "没有匹配的日志行") {
		t.Fatalf("no match = %+v/%v", result, err)
	}
	// match_parse_limit: 超过 2000 行命中。
	var lines []string
	for i := 0; i < grepMaxMatchEvents+5; i++ {
		lines = append(lines, `{"time":"2026-06-03T12:30:00.000Z","level":"info","message":"payment overflow `+string(rune('a'+i%26))+`"}`)
	}
	writeGrepLogFile(t, env, "w11f-overflow.log", lines...)
	result, err = env.grep.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"payment"}})
	if err != nil || !strings.Contains(result.Message, "安全解析上限") {
		t.Fatalf("match limit = %+v/%v", result, err)
	}
}

func TestW11FHotSearchHelpers(t *testing.T) {
	// isAuditHotBucketName 全形态。
	if _, ok := isAuditHotBucketName("not-a-bucket"); ok {
		t.Fatal("non bucket drift")
	}
	if _, ok := isAuditHotBucketName("audit-hot-123.ndjson"); ok {
		t.Fatal("short digits drift")
	}
	if _, ok := isAuditHotBucketName("audit-hot-abcdefghij.ndjson"); ok {
		t.Fatal("non numeric drift")
	}
	bucket, ok := isAuditHotBucketName("audit-hot-2026060312.ndjson")
	if !ok || bucket.UTC().Hour() != 12 {
		t.Fatalf("valid bucket = %v/%v", bucket, ok)
	}
	// normalizeHotSearchKeywords / Limit。
	if keywords := normalizeHotSearchKeywords([]string{" Alpha ", "alpha", "x", "  "}); len(keywords) != 1 || keywords[0] != "alpha" {
		t.Fatalf("hot keywords = %v", keywords)
	}
	many := make([]string, 15)
	for i := range many {
		many[i] = "kw" + string(rune('a'+i))
	}
	if keywords := normalizeHotSearchKeywords(many); len(keywords) != auditHotMaxKeywords {
		t.Fatalf("hot keyword cap = %d", len(keywords))
	}
	if normalizeHotSearchLimit(nil) != auditHotDefaultLimit {
		t.Fatal("hot limit default drift")
	}
	if normalizeHotSearchLimit(intPtrW11F(0)) != 1 || normalizeHotSearchLimit(intPtrW11F(500)) != 100 {
		t.Fatal("hot limit clamp drift")
	}
	if _, err := parseHotInstantMillis("zzz"); err == nil {
		t.Fatal("bad hot instant must fail")
	}
	// readBoundedHotLine: 正常 / 超长 / EOF / 超长+EOF / drain。
	small := bufio.NewReaderSize(strings.NewReader("line1\nline2"), 16)
	raw, consumed, cleanEOF, tooLong, err := readBoundedHotLine(small, 64)
	if err != nil || string(raw) != "line1" || cleanEOF || tooLong || consumed != 6 {
		t.Fatalf("bounded line1 = %q/%d/%v/%v/%v", raw, consumed, cleanEOF, tooLong, err)
	}
	raw, consumed, cleanEOF, _, err = readBoundedHotLine(small, 64)
	if err != nil || string(raw) != "line2" || !cleanEOF || consumed != 5 {
		t.Fatalf("bounded eof = %q/%d/%v/%v", raw, consumed, cleanEOF, err)
	}
	oversized := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 100)+"\nnext\n"), 16)
	raw, _, cleanEOF, tooLong, err = readBoundedHotLine(oversized, 20)
	if err != nil || raw != nil || !tooLong || cleanEOF {
		t.Fatalf("oversized = %q/%v/%v/%v", raw, tooLong, cleanEOF, err)
	}
	raw, _, _, _, err = readBoundedHotLine(oversized, 20)
	if err != nil || string(raw) != "next" {
		t.Fatalf("after drain = %q/%v", raw, err)
	}
	eofOversized := bufio.NewReaderSize(strings.NewReader(strings.Repeat("y", 50)), 16)
	raw, _, cleanEOF, tooLong, err = readBoundedHotLine(eofOversized, 20)
	if err != nil || raw != nil || !tooLong || cleanEOF {
		t.Fatalf("eof oversized = %q/%v/%v/%v", raw, tooLong, cleanEOF, err)
	}
	if err := drainRestOfHotLine(bufio.NewReaderSize(strings.NewReader("tail"), 8)); err != nil {
		t.Fatalf("drain eof = %v", err)
	}
	// collectAuditHotMatch: 非 JSON / 无 ID / 坏时间 / 窗口外 / 命中 / 更新时间。
	seen := map[string]int64{}
	collectAuditHotMatch("not-json", []string{"kw"}, 0, 100, seen)
	collectAuditHotMatch(`{"auditLogId":"","createdAt":"2026-01-01T00:00:00Z","text":"kw"}`, []string{"kw"}, 0, 100, seen)
	collectAuditHotMatch(`{"auditLogId":"a1","createdAt":"bad","text":"kw"}`, []string{"kw"}, 0, 100, seen)
	collectAuditHotMatch(`{"auditLogId":"a2","createdAt":"2026-01-01T00:00:00Z","text":"kw"}`, []string{"kw"}, 1000, 2000, seen)
	if len(seen) != 0 {
		t.Fatalf("no match expected = %v", seen)
	}
	collectAuditHotMatch(`{"auditLogId":"a3","createdAt":"2026-01-01T00:00:00Z","text":"hello KW world"}`, []string{"kw"}, 0, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).UnixMilli(), seen)
	if seen["a3"] == 0 {
		t.Fatalf("match missing = %v", seen)
	}
	collectAuditHotMatch(`{"auditLogId":"a3","createdAt":"2026-01-01T00:05:00Z","text":"hello KW again"}`, []string{"kw"}, 0, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).UnixMilli(), seen)
	if seen["a3"] != time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("newer match not kept = %v", seen)
	}
}

func TestW11FListAuditHotBuckets(t *testing.T) {
	dir := t.TempDir()
	// 目录缺失 → (nil,false,nil)。
	if paths, exists, err := listAuditHotBuckets(filepath.Join(dir, "missing"), 0, 1<<62); err != nil || exists || paths != nil {
		t.Fatalf("missing dir = %v/%v/%v", paths, exists, err)
	}
	// 命中: 桶文件在窗口内; 目录项与非桶文件跳过; 窗口外跳过。
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o750); err != nil {
		t.Fatal(err)
	}
	names := []string{
		"audit-hot-2026060310.ndjson",
		"audit-hot-2026060311.ndjson",
		"audit-hot-2026060309.ndjson",
		"audit-hot-2026060308.ndjson",
		"other.txt",
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC).UnixMilli()
	end := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC).UnixMilli()
	paths, _, err := listAuditHotBuckets(dir, start, end)
	if err != nil || len(paths) < 2 {
		t.Fatalf("buckets = %v/%v", paths, err)
	}
	if filepath.Base(paths[0]) != "audit-hot-2026060311.ndjson" {
		t.Fatalf("bucket order = %v", paths)
	}
	for _, path := range paths {
		if filepath.Base(path) == "other.txt" || filepath.Base(path) == "audit-hot-2026060308.ndjson" {
			t.Fatalf("unexpected bucket %v", path)
		}
	}
}
