package logreads

// w11f 覆盖波次（文件 3/3）：payload blob 细节臂、子树分发、operation-log
// 读路由分支与 public 辅助函数。

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
)

func TestW11FBlobDetailArms(t *testing.T) {
	db := w9bBlobDB(t)
	blobDir := t.TempDir()
	reader := w9bBlobReader(t, db, blobDir)
	ctx := context.Background()

	// 满足生产 NOT NULL 约束的 audit_logs 行（EXISTS 子查询只读 id 与
	// traffic_source，其余列为生产列约束占位）。
	if _, err := db.Exec(`INSERT INTO juhe_dataset.audit_logs (id, traffic_source, trace_id, method, path,
		audit_outcome, success, sample_bucket, sample_reason, capture_status, lifecycle_status, started_at, ended_at, created_at)
		VALUES ('w11f-log', 'gateway', 'trace-w11f', 'POST', '/v1/chat/completions', 'success', true, 0, 'sampled',
		'complete', 'finalized', '2026-06-02T10:00:00.000Z', '2026-06-02T10:00:00.100Z', '2026-06-02T10:00:01.000Z')`); err != nil {
		t.Fatal(err)
	}
	// 满足生产 NOT NULL 约束的 audit_payload_refs 行。
	w11fRefInsert := func(id, auditLogID string, extra map[string]any) {
		t.Helper()
		columns := `INSERT INTO juhe_dataset.audit_payload_refs (id, audit_log_id, part_type, sequence_index, capture_status, created_at`
		values := `VALUES ($1, $2, 'client_request', 0, 'complete', '2026-06-01T10:00:01.000Z'`
		args := []any{id, auditLogID}
		index := 3
		for _, column := range []string{"attempt_id", "headers_blob_id", "body_blob_id", "raw_size_bytes"} {
			if value, ok := extra[column]; ok {
				columns += ", " + column
				values += ", $" + strconv.Itoa(index)
				args = append(args, value)
				index++
			}
		}
		if _, err := db.Exec(columns+") "+values+")", args...); err != nil {
			t.Fatal(err)
		}
	}

	gzipBytes := func(raw []byte) []byte {
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		if _, err := writer.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return buffer.Bytes()
	}

	// 空 ID → nil。
	if detail, err := reader.GetAuditLogPayload(ctx, " ", "p"); err != nil || detail != nil {
		t.Fatalf("blank ids = %v/%v", detail, err)
	}
	if detail, err := reader.GetAuditLogPayload(ctx, "w11f-log", "missing"); err != nil || detail != nil {
		t.Fatalf("missing ref = %v/%v", detail, err)
	}
	// 无任何 blob → not_saved 细节。
	w11fRefInsert("p-plain", "w11f-log", nil)
	detail, err := reader.GetAuditLogPayload(ctx, "w11f-log", "p-plain")
	if err != nil || detail == nil || detail.BodyStorageStatus != auditPayloadStatusNotSaved {
		t.Fatalf("plain detail = %+v/%v", detail, err)
	}
	// headers gzip + body gzip 非法 UTF-8 → base64。
	headerRaw := []byte(`{"content-type":"application/json"}`)
	bodyRaw := []byte{0xff, 0xfe, 0x00, 0x01}
	if err := os.WriteFile(filepath.Join(blobDir, "h.bin"), gzipBytes(headerRaw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobDir, "b.bin"), gzipBytes(bodyRaw), 0o644); err != nil {
		t.Fatal(err)
	}
	w9bBlobInsert(t, db, "bh", "h.bin", "gzip", int64(len(headerRaw)), int64(len(gzipBytes(headerRaw))))
	w9bBlobInsert(t, db, "bb", "b.bin", "gzip", int64(len(bodyRaw)), int64(len(gzipBytes(bodyRaw))))
	w11fRefInsert("p-full", "w11f-log", map[string]any{"headers_blob_id": "bh", "body_blob_id": "bb", "raw_size_bytes": int64(len(bodyRaw))})
	detail, err = reader.GetAuditLogPayload(ctx, "w11f-log", "p-full")
	if err != nil || detail == nil {
		t.Fatalf("full detail = %+v/%v", detail, err)
	}
	if detail.Headers == nil || detail.Headers["content-type"] != "application/json" {
		t.Fatalf("headers = %v", detail.Headers)
	}
	if detail.BodyBase64 == nil || *detail.BodyBase64 != base64.StdEncoding.EncodeToString(bodyRaw) {
		t.Fatalf("body base64 = %v", detail.BodyBase64)
	}
	// body 为合法 UTF-8 文本 → BodyText。
	textBody := []byte("纯文本正文")
	if err := os.WriteFile(filepath.Join(blobDir, "t.bin"), textBody, 0o644); err != nil {
		t.Fatal(err)
	}
	w9bBlobInsert(t, db, "bt", "t.bin", "none", int64(len(textBody)), 0)
	w11fRefInsert("p-text", "w11f-log", map[string]any{"body_blob_id": "bt", "raw_size_bytes": int64(len(textBody))})
	detail, err = reader.GetAuditLogPayload(ctx, "w11f-log", "p-text")
	if err != nil || detail == nil || detail.BodyText == nil || *detail.BodyText != string(textBody) {
		t.Fatalf("text body = %+v/%v", detail, err)
	}
	// header 窗口截断（bounded 读 → nextOffset）。
	bigHeader := []byte(`{"a":"` + strings.Repeat("x", 40) + `"}`)
	if err := os.WriteFile(filepath.Join(blobDir, "big.bin"), bigHeader, 0o644); err != nil {
		t.Fatal(err)
	}
	w9bBlobInsert(t, db, "big", "big.bin", "none", int64(len(bigHeader)), 0)
	w11fRefInsert("p-big", "w11f-log", map[string]any{"headers_blob_id": "big", "raw_size_bytes": int64(len(bigHeader))})
	detail, err = reader.GetAuditLogPayload(ctx, "w11f-log", "p-big")
	if err != nil || detail == nil {
		t.Fatalf("big header = %+v/%v", detail, err)
	}
	// 坏 gzip → 错误。
	if err := os.WriteFile(filepath.Join(blobDir, "badgz.bin"), []byte("not-gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	w9bBlobInsert(t, db, "bgz", "badgz.bin", "gzip", 8, 8)
	w11fRefInsert("p-badgz", "w11f-log", map[string]any{"body_blob_id": "bgz", "raw_size_bytes": int64(8)})
	if _, err := reader.GetAuditLogPayload(ctx, "w11f-log", "p-badgz"); err == nil {
		t.Fatal("bad gzip must fail")
	}
	// 解压后尺寸不一致。
	if err := os.WriteFile(filepath.Join(blobDir, "rawmismatch.bin"), gzipBytes([]byte("12345")), 0o644); err != nil {
		t.Fatal(err)
	}
	w9bBlobInsert(t, db, "rm", "rawmismatch.bin", "gzip", 99, int64(len(gzipBytes([]byte("12345")))))
	w11fRefInsert("p-rm", "w11f-log", map[string]any{"body_blob_id": "rm", "raw_size_bytes": int64(99)})
	if _, err := reader.GetAuditLogPayload(ctx, "w11f-log", "p-rm"); err == nil {
		t.Fatal("raw mismatch must fail")
	}
	// 越界 storage_key → resolveBlobPath 错误。
	w9bBlobInsert(t, db, "esc", "../escape.bin", "none", 3, 3)
	w11fRefInsert("p-esc", "w11f-log", map[string]any{"body_blob_id": "esc", "raw_size_bytes": int64(3)})
	if _, err := reader.GetAuditLogPayload(ctx, "w11f-log", "p-esc"); err == nil {
		t.Fatal("escaping storage_key must fail")
	}

	// resolveBlobPath 直连。
	if _, err := resolveBlobPath(blobDir, "../escape"); err == nil {
		t.Fatal("resolveBlobPath escape must fail")
	}
	if path, err := resolveBlobPath(blobDir, "sub/key.bin"); err != nil || !strings.HasPrefix(path, blobDir) {
		t.Fatalf("resolveBlobPath deep = %s/%v", path, err)
	}
	// utf8RoundTrips。
	if !utf8RoundTrips([]byte("ok")) || utf8RoundTrips([]byte{0xff}) {
		t.Fatal("utf8RoundTrips drift")
	}
	// emptyAuditBlobWindow 两分支。
	if window := emptyAuditBlobWindow(true, 5, 7, 9, "s"); window.limit != 7 || window.offset != 0 {
		t.Fatalf("empty full = %+v", window)
	}
	if window := emptyAuditBlobWindow(false, 5, 7, 9, "s"); window.offset != 5 || window.limit != 7 {
		t.Fatalf("empty bounded = %+v", window)
	}
}

func TestW11FAuditSubtreeDispatch(t *testing.T) {
	env, _ := w11fFaultEnv(t, auditReadsDDL)
	// 未知形状 → 404。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/a/payloads", "")
	if code != http.StatusNotFound || payloadW11FMessage(payload) != "资源不存在" {
		t.Fatalf("unknown shape = %d %v", code, payload)
	}
	// 尾斜杠 → 列表。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/", "")
	if code != http.StatusOK {
		t.Fatal("trailing slash list must 200")
	}
	// payloads 子路径: 缺失 → 404。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/w11f-log/payloads/p1", "")
	if code != http.StatusNotFound || payloadW11FMessage(payload) != "审计原文不存在" {
		t.Fatalf("payload missing = %d %v", code, payload)
	}
}

// w11fStubOperationReader 实现 OperationLogReader。
type w11fStubOperationReader struct {
	listErr   error
	detailErr error
	found     bool
}

func (s *w11fStubOperationReader) List(context.Context, operationlog.ListOptions) (operationlog.ListResult, error) {
	if s.listErr != nil {
		return operationlog.ListResult{}, s.listErr
	}
	return operationlog.ListResult{Items: []operationlog.ListItem{}, Total: 0, Page: 1, PageSize: 20}, nil
}

func (s *w11fStubOperationReader) Detail(context.Context, string, string) (operationlog.DetailSupplement, bool, error) {
	if s.detailErr != nil {
		return operationlog.DetailSupplement{}, false, s.detailErr
	}
	return operationlog.DetailSupplement{}, s.found, nil
}

func TestW11FOperationLogRouteHandlers(t *testing.T) {
	// 坏时间参数 → 400。
	deps := &Deps{Reader: &w11fStubOperationReader{}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/operation-logs?startAt=zzz", nil)
	deps.list(false)(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad time = %d %s", recorder.Code, recorder.Body.String())
	}
	// 列表错误 → 500。
	faulty := &Deps{Reader: &w11fStubOperationReader{listErr: w11fBoom}}
	recorder = httptest.NewRecorder()
	faulty.list(false)(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/api/operation-logs", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("list fault = %d", recorder.Code)
	}
	// self 面: viewer 注入 + 命中。
	withAuth := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-operation-logs", nil)
	auth := &authsys.AuthContext{SystemAccountID: "w11f-viewer", Role: "user"}
	withAuth = withAuth.WithContext(authsys.WithAuthContext(withAuth.Context(), auth))
	recorder = httptest.NewRecorder()
	deps.list(true)(recorder, withAuth)
	if recorder.Code != http.StatusOK {
		t.Fatalf("self list = %d %s", recorder.Code, recorder.Body.String())
	}
	// 详情: 错误 → 500; 未命中 → 404; 命中 → 200。
	detailFaulty := &Deps{Reader: &w11fStubOperationReader{detailErr: w11fBoom}}
	recorder = httptest.NewRecorder()
	detailFaulty.detail(false)(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/api/operation-logs/x", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("detail fault = %d", recorder.Code)
	}
	missing := &Deps{Reader: &w11fStubOperationReader{found: false}}
	recorder = httptest.NewRecorder()
	missing.detail(false)(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/api/operation-logs/x", nil))
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "操作日志不存在") {
		t.Fatalf("detail missing = %d %s", recorder.Code, recorder.Body.String())
	}
	found := &Deps{Reader: &w11fStubOperationReader{found: true}}
	recorder = httptest.NewRecorder()
	found.detail(true)(recorder, withAuth)
	if recorder.Code != http.StatusOK {
		t.Fatalf("detail hit = %d", recorder.Code)
	}
	_ = kernel.WriteOK
}

func TestW11FPublicHelpers(t *testing.T) {
	// readCaptureStatus 分支。
	for value, want := range map[string]string{
		"complete": "complete", "truncated": "truncated", "empty": "empty",
		"dropped": "dropped", "bogus": "empty", "": "empty", "  ": "empty",
	} {
		if got := readCaptureStatus(value); got != want {
			t.Fatalf("readCaptureStatus(%q) = %q want %q", value, got, want)
		}
	}
	// readJSONObject: 空 / 非字符串 / 坏 JSON / 非对象 / 对象。
	if got := readJSONObject(nil); len(got) != 0 {
		t.Fatalf("nil json = %v", got)
	}
	if got := readJSONObject(3); len(got) != 0 {
		t.Fatalf("non-string json = %v", got)
	}
	if got := readJSONObject("  "); len(got) != 0 {
		t.Fatalf("blank json = %v", got)
	}
	if got := readJSONObject("{bad"); len(got) != 0 {
		t.Fatalf("bad json = %v", got)
	}
	if got := readJSONObject("[1]"); len(got) != 0 {
		t.Fatalf("array json = %v", got)
	}
	if got := readJSONObject([]byte(`{"k":1}`)); len(got) != 1 {
		t.Fatalf("bytes json = %v", got)
	}
	// readOptionalInstant。
	if readOptionalInstant("zzz") != "" || readOptionalInstant("  ") != "" {
		t.Fatal("optional instant drift")
	}
	if got := readOptionalInstant("2026-01-01T00:00:00Z"); got != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("optional instant = %q", got)
	}
	// parsePublicApiLogListOptions: 非法 result 归零 + 时间错误。
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/public-api-logs?result=bogus&traceId=tr", nil)
	options, err := parsePublicApiLogListOptions(request)
	if err != nil || options.Result != "" || options.TraceID != "tr" {
		t.Fatalf("parse options = %+v/%v", options, err)
	}
	badRequest := httptest.NewRequest(http.MethodGet, "/__aisys__/api/public-api-logs?startAt=zzz", nil)
	if _, err := parsePublicApiLogListOptions(badRequest); err == nil {
		t.Fatal("bad time must fail")
	}
	// 前缀过滤参数（含 all）。
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/api/public-api-logs?sourceRefId=all&path=all&clientIp=10.0&result=failed&statusCode=500", nil)
	options, err = parsePublicApiLogListOptions(request)
	if err != nil || options.SourceRefID != "all" || options.StatusCode == nil || *options.StatusCode != 500 {
		t.Fatalf("filters = %+v/%v", options, err)
	}
	_ = url.Values{}
	_ = time.Now
}
