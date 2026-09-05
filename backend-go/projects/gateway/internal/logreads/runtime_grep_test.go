package logreads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// grepPinnedNow pins the grep window clock.
var grepPinnedNow = time.Date(2026, 6, 3, 13, 0, 0, 0, time.UTC)

func pinGrepClock(env *readsTestEnv) {
	env.grep.Now = func() time.Time { return grepPinnedNow }
}

// writeGrepLogFile writes one .log fixture with a fresh mtime inside the
// pinned window and returns its lines.
func writeGrepLogFile(t *testing.T, env *readsTestEnv, name string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(env.logDir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(env.logDir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	stamp := grepPinnedNow.Add(-30 * time.Minute)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func grepItemID(fileName string, lineNumber int64, line string) string {
	digest := sha256.Sum256([]byte(fileName + "\x00" + strconv.FormatInt(lineNumber, 10) + "\x00" + line))
	return hex.EncodeToString(digest[:])
}

// TestRuntimeLogGrepFamily covers grep-options, the grep scan contract
// (all-keyword AND, search-request self-exclusion, level mapping, limit)
// and grep-detail (ok / 404 / stale).
func TestRuntimeLogGrepFamily(t *testing.T) {
	env := newReadsTestEnv(t, runtimeReadsDDL, nil, true)
	pinGrepClock(env)
	first := `{"time":"2026-06-03T12:30:00.000Z","level":"error","traceId":"tr-grep","event":"gateway.request","msg":"payment crashed","errorMessage":"boom happened"}`
	second := `{"time":"2026-06-03T12:20:00.000Z","level":50,"message":"payment retried"}`
	searchRequest := `{"time":"2026-06-03T12:10:00.000Z","level":"info","event":"http_request_completed","originalUrl":"/__aisys__/api/runtime-logs/grep?keywords=payment","message":"self hit payment"}`
	noise := `{"time":"2026-06-03T12:00:00.000Z","level":"info","message":"unrelated chatter"}`
	writeGrepLogFile(t, env, "gateway.log", first, second, searchRequest, noise)
	writeGrepLogFile(t, env, "scheduler.log", `{"time":"2026-06-03T11:00:00.000Z","level":"warn","message":"scheduler payment tick"}`)

	// grep-options mirrors the file listing window.
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep-options", "")
	if code != http.StatusOK {
		t.Fatalf("grep-options status: %d %v", code, payload)
	}
	options := wantData(t, payload)
	if wantFloat(t, options, "defaultRangeDays") != grepDefaultRangeDays || wantFloat(t, options, "maxRangeDays") != grepMaxRangeDays {
		t.Fatalf("grep-options range days: %v", options)
	}
	if wantFloat(t, options, "fileRetentionDays") != 30 || wantFloat(t, options, "maxConcurrentSearches") != 1 {
		t.Fatalf("grep-options bounds: %v", options)
	}
	if wantString(t, options, "earliestFileTime") == "" {
		t.Fatalf("grep-options earliestFileTime missing: %v", options)
	}

	// grep: all keywords must match (case-insensitive), the grep API's own
	// request lines never match, newest first.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep?keywords=payment&keywords=CRASHED", "")
	if code != http.StatusOK {
		t.Fatalf("grep status: %d %v", code, payload)
	}
	data := wantData(t, payload)
	if !wantBool(t, data, "available") || wantBool(t, data, "truncated") {
		t.Fatalf("grep envelope: %v", data)
	}
	if wantFloat(t, data, "scannedFileCount") != 2 || wantFloat(t, data, "limit") != 100 {
		t.Fatalf("grep scan bounds: %v", data)
	}
	items := wantItems(t, data)
	if len(items) != 1 {
		t.Fatalf("grep AND-match must keep only the crashed line: %v", items)
	}
	item := items[0]
	if wantString(t, item, "id") != grepItemID("gateway.log", 1, first) {
		t.Fatalf("grep item id: %v", item["id"])
	}
	if wantString(t, item, "fileName") != "gateway.log" || wantFloat(t, item, "lineNumber") != 1 {
		t.Fatalf("grep item anchor: %v", item)
	}
	if wantString(t, item, "level") != "error" || wantString(t, item, "traceId") != "tr-grep" ||
		wantString(t, item, "message") != "payment crashed" || wantString(t, item, "errorMessage") != "boom happened" {
		t.Fatalf("grep item fields: %v", item)
	}

	// Single keyword matches across files ordered newest first; the search
	// request line is excluded and the pino numeric level maps to error.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep?keywords=payment", "")
	if code != http.StatusOK {
		t.Fatalf("grep single status: %d %v", code, payload)
	}
	ids := make([]string, 0)
	for _, entry := range wantItems(t, wantData(t, payload)) {
		ids = append(ids, wantString(t, entry, "id"))
	}
	wantIDs := []string{
		grepItemID("gateway.log", 1, first),
		grepItemID("gateway.log", 2, second),
		grepItemID("scheduler.log", 1, `{"time":"2026-06-03T11:00:00.000Z","level":"warn","message":"scheduler payment tick"}`),
	}
	if strings.Join(ids, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("grep single keyword order: %v", ids)
	}

	// The self-referencing search request line carries level 50 -> "error"
	// when matched by another keyword window? It is excluded above; verify
	// the numeric level mapping through the second line instead.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep?keywords=retried", "")
	if code != http.StatusOK {
		t.Fatalf("grep retried status: %d %v", code, payload)
	}
	items = wantItems(t, wantData(t, payload))
	if len(items) != 1 || wantString(t, items[0], "level") != "error" {
		t.Fatalf("grep numeric level mapping: %v", items)
	}

	// grep-detail pins the exact line by content hash.
	detail := fmt.Sprintf("/__aisys__/api/runtime-logs/grep-detail?id=%s&fileName=gateway.log&lineNumber=1", grepItemID("gateway.log", 1, first))
	code, payload = env.do(t, http.MethodGet, detail, "")
	if code != http.StatusOK {
		t.Fatalf("grep-detail status: %d %v", code, payload)
	}
	detailData := wantData(t, payload)
	if !strings.HasSuffix(wantString(t, detailData, "file"), "gateway.log") {
		t.Fatalf("grep-detail file: %v", detailData)
	}
	if wantString(t, detailData, "line") != first {
		t.Fatalf("grep-detail line: %v", detailData["line"])
	}

	// A content change makes the anchor stale (409).
	rotated := strings.Replace(first, "crashed", "crashed2", 1)
	writeGrepLogFile(t, env, "gateway.log", rotated, second, searchRequest, noise)
	code, payload = env.do(t, http.MethodGet, detail, "")
	if code != http.StatusConflict || wantString(t, payload, "message") != "日志文件已经轮转或内容发生变化，请重新搜索" {
		t.Fatalf("grep-detail stale: %d %v", code, payload)
	}

	// Unknown file / invalid anchor are 404 / 400 with the Node messages.
	missing := fmt.Sprintf("/__aisys__/api/runtime-logs/grep-detail?id=%s&fileName=missing.log&lineNumber=1", grepItemID("missing.log", 1, first))
	code, payload = env.do(t, http.MethodGet, missing, "")
	if code != http.StatusNotFound || wantString(t, payload, "message") != "grep 匹配行不存在" {
		t.Fatalf("grep-detail missing: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep-detail?id=abc&fileName=gateway.log&lineNumber=1", "")
	if code != http.StatusNotFound || wantString(t, payload, "message") != "grep 匹配行不存在" {
		t.Fatalf("grep-detail bad id: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep-detail?id="+grepItemID("gateway.log", 1, first)+"&fileName=gateway.log&lineNumber=0", "")
	if code != http.StatusBadRequest || wantString(t, payload, "message") != "grep 详情定位参数无效" {
		t.Fatalf("grep-detail invalid anchor: %d %v", code, payload)
	}

	// Invalid bounds stay a 400 request fault.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep?endAt=nope", "")
	if code != http.StatusBadRequest || wantString(t, payload, "message") != "结束时间必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("grep invalid endAt: %d %v", code, payload)
	}

	// Empty keywords answer the available:true hint contract.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep", "")
	if code != http.StatusOK {
		t.Fatalf("grep empty status: %d %v", code, payload)
	}
	data = wantData(t, payload)
	if !wantBool(t, data, "available") || wantString(t, data, "message") != "请输入要搜索的关键字" {
		t.Fatalf("grep empty payload: %v", data)
	}

	// Permission gate: anonymous 401, non-admin 403.
	env.resetSession()
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous grep: %d %v", code, payload)
	}
	if _, err := env.accounts.Create(context.Background(), authsys.CreateInput{
		Username: "grep-viewer", DisplayName: "grep-viewer_name", Password: "grep-viewer-password-123", Role: "user",
		MustChangePassword: boolPtr(false),
	}); err != nil {
		t.Fatal(err)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/auth/login", `{"username":"grep-viewer","password":"grep-viewer-password-123"}`)
	if code != http.StatusOK {
		t.Fatalf("grep-viewer login: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/grep-options", "")
	if code != http.StatusForbidden || wantString(t, payload, "message") != "需要管理员权限" {
		t.Fatalf("grep-options as user: %d %v", code, payload)
	}
}

// TestRuntimeLogGrepDisabledContract covers the file-logging-disabled
// degradation (available:false + message, still 200).
func TestRuntimeLogGrepDisabledContract(t *testing.T) {
	service := NewRuntimeLogGrep(RuntimeLogGrepConfig{})
	result, err := service.Search(context.Background(), RuntimeLogGrepOptions{Keywords: []string{"anything"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Available || result.Message != "文件日志未启用，无法使用 grep 模式。" {
		t.Fatalf("disabled grep result: %#v", result)
	}
	if result.Items == nil || len(result.Items) != 0 {
		t.Fatalf("disabled grep items must serialize as []: %#v", result.Items)
	}
	runtime, err := service.Options()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.FileRetentionDays != 30 || runtime.MaxConcurrentSearches != 1 {
		t.Fatalf("disabled grep options: %#v", runtime)
	}
}
