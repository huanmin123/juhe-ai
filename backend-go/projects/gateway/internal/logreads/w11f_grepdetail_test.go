package logreads

// w11f 覆盖波次（文件 4/4）：grep 详情/字段解析、listLogFiles 降级、热搜索
// 截断与取消分支。

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestW11FGrepDetailAndFields(t *testing.T) {
	env := newReadsTestEnv(t, runtimeReadsDDL, nil, true)
	pinGrepClock(env)
	line1 := `{"time":"2026-06-03T12:30:00.000Z","level":50,"traceId":"tr-1","event":"gateway.request","msg":"payment crashed","err":{"message":"boom wrapped"}}`
	line2 := `{"time":1780182600000,"level":30,"message":"ok line"}`
	writeGrepLogFile(t, env, "w11f-detail.log", line1, line2)

	// 字段解析: JSON 各分支。
	fields := runtimeLogFieldsFromLine(line1)
	if fields.level != "error" || fields.traceID != "tr-1" || fields.message != "payment crashed" || fields.errorMessage != "boom wrapped" {
		t.Fatalf("fields = %+v", fields)
	}
	fields = runtimeLogFieldsFromLine(line2)
	if fields.level != "info" || fields.message != "ok line" || fields.time != millisToISO(1780182600000) {
		t.Fatalf("fields2 = %+v", fields)
	}
	// 非法 JSON / 超长行回退。
	fields = runtimeLogFieldsFromLine("not-json payment")
	if fields.level != "info" || fields.message != "not-json payment" {
		t.Fatalf("fallback fields = %+v", fields)
	}
	fields = runtimeLogFieldsFromLine(`{"message":"` + strings.Repeat("x", 20001) + `"}`)
	if fields.time != "" || fields.level != "info" {
		t.Fatalf("long line fields = %+v", fields)
	}
	// level 数字带与空白字符串回退。
	for value, want := range map[float64]string{60: "fatal", 50: "error", 40: "warn", 30: "info", 20: "debug", 10: "trace"} {
		if got := grepNormalizeLevel(value); got != want {
			t.Fatalf("level(%v) = %s", value, got)
		}
	}
	if grepNormalizeLevel("  ") != "info" || grepNormalizeLevel(" ERROR ") != "error" || grepNormalizeLevel(true) != "info" {
		t.Fatal("grepNormalizeLevel drift")
	}
	// grepTimeValue / grepSortTimeMs / trimGrepText。
	if grepTimeValue(3.0) == "" || grepTimeValue(true) != "" || grepTimeValue(" 2026 ") != "2026" {
		t.Fatal("grepTimeValue drift")
	}
	if _, ok := grepSortTimeMs("zzz"); ok {
		t.Fatal("grepSortTimeMs drift")
	}
	if trimGrepText("12345", 3) != "123..." || trimGrepText("123", 3) != "123" {
		t.Fatal("trimGrepText drift")
	}

	// Detail: 参数无效 / 未知文件 / 命中 / 过期。
	g := env.grep
	lookup := g.Detail("bad id", "w11f-detail.log", 1)
	if lookup.Status != "not_found" {
		t.Fatalf("bad id = %+v", lookup)
	}
	itemID := runtimeLogGrepItemID("w11f-detail.log", 1, line1)
	lookup = g.Detail(itemID, "missing.log", 1)
	if lookup.Status != "not_found" {
		t.Fatalf("missing file = %+v", lookup)
	}
	lookup = g.Detail(itemID, "w11f-detail.log", 1)
	if lookup.Status != "ok" || lookup.Detail.Line != line1 {
		t.Fatalf("ok lookup = %+v", lookup)
	}
	lookup = g.Detail(strings.Repeat("a", 64), "w11f-detail.log", 1)
	if lookup.Status != "stale" {
		t.Fatalf("stale lookup = %+v", lookup)
	}
	lookup = g.Detail(itemID, "w11f-detail.log", 99)
	if lookup.Status != "stale" {
		t.Fatalf("beyond eof = %+v", lookup)
	}
	// readRuntimeLogLine: 打不开的文件。
	if status, _ := readRuntimeLogLine(filepath.Join(env.logDir, "missing.log"), 1); status != "not_found" {
		t.Fatal("missing read drift")
	}

	// grep-detail handler: 400 / 404 / 409 / 200。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep-detail?id=&fileName=x&lineNumber=1", "")
	if code != http.StatusBadRequest || payloadW11FMessage(payload) != "grep 详情定位参数无效" {
		t.Fatalf("grep detail 400 = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep-detail?id="+strings.Repeat("a", 64)+"&fileName=missing.log&lineNumber=1", "")
	if code != http.StatusNotFound || payloadW11FMessage(payload) != "grep 匹配行不存在" {
		t.Fatalf("grep detail 404 = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep-detail?id="+strings.Repeat("a", 64)+"&fileName=w11f-detail.log&lineNumber=1", "")
	if code != http.StatusConflict {
		t.Fatalf("grep detail 409 = %d %v", code, payload)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep-detail?id="+itemID+"&fileName=w11f-detail.log&lineNumber=1", "")
	if code != http.StatusOK {
		t.Fatalf("grep detail 200 = %d", code)
	}
	// grep-options handler 命中。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep-options", "")
	if code != http.StatusOK {
		t.Fatalf("grep options = %d", code)
	}
}

func TestW11FGrepScanEdgeBranches(t *testing.T) {
	env := newReadsTestEnv(t, runtimeReadsDDL, nil, true)
	pinGrepClock(env)
	ctx := context.Background()

	// 超长行被跳过（tooLong continue），后续正常行命中。
	hugeLine := `{"message":"payment ` + strings.Repeat("x", 30000) + `"}`
	normalLine := `{"time":"2026-06-03T12:30:00.000Z","message":"payment normal"}`
	writeGrepLogFile(t, env, "w11f-toolong.log", hugeLine, normalLine)
	result, err := env.grep.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"payment"}})
	if err != nil || len(result.Items) != 1 || result.Items[0].Message != "payment normal" {
		t.Fatalf("tooLong scan = %+v/%v", result, err)
	}
	// 上下文取消 → 扫描失败消息。
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	result, err = env.grep.Search(canceled, RuntimeLogGrepOptions{Keywords: []string{"payment"}})
	if err != nil || !strings.Contains(result.Message, "日志文件扫描失败") {
		t.Fatalf("canceled scan = %+v/%v", result, err)
	}
	// 目录中带 .log 后缀的子目录被跳过。
	if err := os.MkdirAll(filepath.Join(env.logDir, "w11f-dir.log"), 0o750); err != nil {
		t.Fatal(err)
	}
	result, err = env.grep.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"payment"}})
	if err != nil {
		t.Fatal(err)
	}
	// listLogFiles deadline 降级（独立 grep 实例 + 时钟推进越过 2 秒扫描上限）。
	deadlineDir := t.TempDir()
	deadlineGrep := NewRuntimeLogGrep(RuntimeLogGrepConfig{FileEnabled: true, Directory: deadlineDir, MaxFiles: 500})
	base := time.Date(2026, 6, 3, 13, 0, 0, 0, time.UTC)
	calls := 0
	deadlineGrep.Now = func() time.Time {
		calls++
		if calls <= 1 {
			return base
		}
		return base.Add(3 * time.Second)
	}
	for _, name := range []string{"w11f-a.log", "w11f-b.log"} {
		path := filepath.Join(deadlineDir, name)
		if err := os.WriteFile(path, []byte("payment line\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(-30 * time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	files, warning, err := deadlineGrep.listLogFiles()
	if err != nil || warning == "" || len(files) > 1 {
		t.Fatalf("deadline listing = %v/%q/%v", files, warning, err)
	}
}

func TestW11FHotSearchTruncationAndCancel(t *testing.T) {
	env := newReadsTestEnv(t, runtimeReadsDDL, nil, true)
	bucketStamp := func(name string) {
		if err := os.MkdirAll(env.hotDir, 0o750); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(env.hotDir, name)
		if err := os.WriteFile(path, []byte("{}\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	// 条目超过 auditHotMaxDirectoryEntries=4096 → rangeTruncated。
	for index := 0; index < 4098; index++ {
		bucketStamp("audit-hot-pad-" + time.Duration(index).String() + ".ndjson")
	}
	env2, _ := w11fFaultEnv(t, auditReadsDDL)
	// 直接构造带热目录的 reader。
	audit := envAudit(env2)
	audit.hotDir = env.hotDir

	scan, err := audit.SearchHot(context.Background(), AuditHotSearchOptions{Keywords: []string{"kw"}})
	if err != nil || !scan.Truncated {
		t.Fatalf("range truncated = %+v/%v", scan, err)
	}
	if !strings.Contains(scan.Message, "热搜索文件范围超过读取上限") {
		t.Fatalf("message = %q", scan.Message)
	}
	// 坏 endAt → 错误。
	if _, err := audit.SearchHot(context.Background(), AuditHotSearchOptions{Keywords: []string{"kw"}, EndAt: "zzz"}); err == nil {
		t.Fatal("bad endAt must fail")
	}
	// 未配置热目录 → available=false。
	bareReader, err := NewAuditLogQueryReader(env2.db, ReadSQLite, AuditQueryDirectories{})
	if err != nil {
		t.Fatal(err)
	}
	bareScan, err := bareReader.SearchHot(context.Background(), AuditHotSearchOptions{Keywords: []string{"kw"}})
	if err != nil || bareScan.Available || bareScan.Message != "F3 审计内容搜索目录未配置" {
		t.Fatalf("bare scan = %+v/%v", bareScan, err)
	}
	// 取消上下文 → 扫描错误（需要一个真实桶文件进入扫描循环）。
	bucketStamp("audit-hot-" + time.Now().UTC().Format("2006010215") + ".ndjson")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := audit.SearchHot(canceled, AuditHotSearchOptions{Keywords: []string{"kw"}}); err == nil {
		t.Fatal("canceled hot search must fail")
	}
}

func TestW11FHotSearchHandlerTruncated(t *testing.T) {
	env, script := w11fFaultEnv(t, auditReadsDDL)
	now := time.Now().UTC()
	if err := os.MkdirAll(env.hotDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// 两行匹配 + limit=1 → resultTruncated + total 语义。
	name := "audit-hot-" + now.Format("2006010215") + ".ndjson"
	lines := []string{}
	for i := 0; i < 2; i++ {
		lines = append(lines, `{"auditLogId":"w11f-aid-`+string(rune('a'+i))+`","createdAt":"`+now.Format(time.RFC3339)+`","text":"w11f-hot keyword hit"}`)
	}
	if err := os.WriteFile(filepath.Join(env.hotDir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/search-hot?keywords=w11f-hot&limit=1", "")
	if code != http.StatusOK {
		t.Fatalf("truncated hot hit = %d %v", code, payload)
	}
	// ListAuditLogsByID 故障 → 500。
	os.WriteFile(filepath.Join(env.hotDir, "audit-hot-"+now.Add(-time.Hour).Format("2006010215")+".ndjson"), []byte(lines[0]+"\n"), 0o640)
	script.failQuery("FROM \"audit_logs\"")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/search-hot?keywords=w11f-hot", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("hydration fault = %d %v", code, payload)
	}
	_ = http.MethodGet
}

// TestW11FGrepSearchMessages 补 Search 消息臂: 并发占用、规范化错误、
// 警告合并与过滤空集。
func TestW11FGrepSearchMessages(t *testing.T) {
	env := newReadsTestEnv(t, runtimeReadsDDL, nil, true)
	pinGrepClock(env)
	ctx := context.Background()
	writeGrepLogFile(t, env, "w11f-msg.log", `{"time":"2026-06-03T12:30:00.000Z","message":"payment hit"}`)

	// 并发占用 → busy 消息。
	env.grep.active.Add(1)
	result, err := env.grep.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"payment"}})
	if err != nil || result.Available || result.Message != "已有 grep 搜索正在运行，请稍后重试。" {
		t.Fatalf("busy = %+v/%v", result, err)
	}
	env.grep.active.Add(-1)

	// 列举后规范化错误（坏 startAt）→ 直接返回错误。
	if _, err := env.grep.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"payment"}, StartAt: "zzz"}); err == nil {
		t.Fatal("normalize fault must fail")
	}

	// 调整窗口 + 短关键字警告 + 范围过滤空集。
	result, err = env.grep.Search(ctx, RuntimeLogGrepOptions{
		Keywords: []string{"payment", "ab"},
		StartAt:  grepPinnedNow.Add(-2 * time.Minute).Format(time.RFC3339),
		EndAt:    grepPinnedNow.Add(-1 * time.Minute).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Message, "已忽略少于 3 个字符的短关键字") {
		t.Fatalf("short warning missing: %q", result.Message)
	}
	if !strings.Contains(result.Message, "当前文件时间范围内没有可搜索的日志文件") {
		t.Fatalf("filtered empty missing: %q", result.Message)
	}

	// 列举条目上限警告进入消息（4098 个填充文件）。
	limitDir := t.TempDir()
	limitGrep := NewRuntimeLogGrep(RuntimeLogGrepConfig{FileEnabled: true, Directory: limitDir, MaxFiles: 500})
	limitGrep.Now = func() time.Time { return grepPinnedNow }
	for index := 0; index < 10001; index++ {
		if err := os.WriteFile(filepath.Join(limitDir, "pad-"+strconv.Itoa(index)), nil, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	limitLine := `{"time":"2026-06-03T12:30:00.000Z","message":"payment limit"}`
	limitPath := filepath.Join(limitDir, "w11f-limit.log")
	if err := os.WriteFile(limitPath, []byte(limitLine+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	limitStamp := grepPinnedNow.Add(-30 * time.Minute)
	if err := os.Chtimes(limitPath, limitStamp, limitStamp); err != nil {
		t.Fatal(err)
	}
	result, err = limitGrep.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"payment"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Message, "日志目录条目超过 10000 个") {
		t.Fatalf("entry limit warning missing: %q", result.Message)
	}
	// 调整窗口警告（startAt 早于最早文件）。
	result, err = env.grep.Search(ctx, RuntimeLogGrepOptions{
		Keywords: []string{"payment"},
		StartAt:  grepPinnedNow.Add(-10 * 24 * time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Message, "时间范围已自动调整") {
		t.Fatalf("adjusted warning missing: %q", result.Message)
	}

	// 超长行 + EOF（无换行结尾）→ tooLong+cleanEOF 分支。
	tailDir := t.TempDir()
	tailGrep := NewRuntimeLogGrep(RuntimeLogGrepConfig{FileEnabled: true, Directory: tailDir, MaxFiles: 500})
	tailGrep.Now = func() time.Time { return grepPinnedNow }
	huge := `{"message":"payment ` + strings.Repeat("y", 90000) + `"}`
	tailPath := filepath.Join(tailDir, "w11f-tail.log")
	if err := os.WriteFile(tailPath, []byte(huge), 0o640); err != nil {
		t.Fatal(err)
	}
	stamp2 := grepPinnedNow.Add(-30 * time.Minute)
	if err := os.Chtimes(tailPath, stamp2, stamp2); err != nil {
		t.Fatal(err)
	}
	result, err = tailGrep.Search(ctx, RuntimeLogGrepOptions{Keywords: []string{"payment"}})
	if err != nil || !strings.Contains(result.Message, "没有匹配的日志行") {
		t.Fatalf("tail scan = %+v/%v", result, err)
	}
	// readRuntimeLogLine 的 tooLong+cleanEOF 与目标命中。
	if status, _ := readRuntimeLogLine(tailPath, 1); status != "not_found" {
		t.Fatalf("tail status = %q", status)
	}
}
