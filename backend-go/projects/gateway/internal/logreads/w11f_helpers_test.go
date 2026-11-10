package logreads

// w11f 覆盖波次（文件 1/2）：纯函数分支 + grep 扫描分支。

import (
	"bufio"
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
