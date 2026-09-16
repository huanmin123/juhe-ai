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

	// 建全 payload 所需表。
	for _, statement := range []string{
		`CREATE TABLE audit_logs (id TEXT PRIMARY KEY, traffic_source TEXT NOT NULL DEFAULT 'gateway', created_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE audit_payload_refs (id TEXT PRIMARY KEY, audit_log_id TEXT NOT NULL, part_type TEXT NOT NULL DEFAULT '', sequence_index INTEGER NOT NULL DEFAULT 0, attempt_id TEXT, content_type TEXT, content_encoding TEXT, headers_sha256 TEXT, body_sha256 TEXT, raw_size_bytes INTEGER NOT NULL DEFAULT 0, compressed_size_bytes INTEGER NOT NULL DEFAULT 0, capture_status TEXT NOT NULL DEFAULT '', drop_reason TEXT, headers_blob_id TEXT, body_blob_id TEXT, created_at TEXT NOT NULL DEFAULT '')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO audit_logs (id, traffic_source, created_at) VALUES ('w11f-log', 'gateway', '2026-06-02T10:00:01.000Z')`); err != nil {
		t.Fatal(err)
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
	if _, err := db.Exec(`INSERT INTO audit_payload_refs (id, audit_log_id) VALUES ('p-plain', 'w11f-log')`); err != nil {
		t.Fatal(err)
	}
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
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs VALUES ('bh', 'h.bin', 'gzip', ?, ?)`, len(headerRaw), len(gzipBytes(headerRaw))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs VALUES ('bb', 'b.bin', 'gzip', ?, ?)`, len(bodyRaw), len(gzipBytes(bodyRaw))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_refs (id, audit_log_id, headers_blob_id, body_blob_id, raw_size_bytes) VALUES ('p-full', 'w11f-log', 'bh', 'bb', ?)`, len(bodyRaw)); err != nil {
		t.Fatal(err)
	}
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
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs VALUES ('bt', 't.bin', 'none', ?, 0)`, len(textBody)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_refs (id, audit_log_id, body_blob_id, raw_size_bytes) VALUES ('p-text', 'w11f-log', 'bt', ?)`, len(textBody)); err != nil {
		t.Fatal(err)
	}
	detail, err = reader.GetAuditLogPayload(ctx, "w11f-log", "p-text")
	if err != nil || detail == nil || detail.BodyText == nil || *detail.BodyText != string(textBody) {
		t.Fatalf("text body = %+v/%v", detail, err)
	}
	// header 窗口截断（bounded 读 → nextOffset）。
	bigHeader := []byte(`{"a":"` + strings.Repeat("x", 40) + `"}`)
	if err := os.WriteFile(filepath.Join(blobDir, "big.bin"), bigHeader, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs VALUES ('big', 'big.bin', 'none', ?, 0)`, len(bigHeader)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_refs (id, audit_log_id, headers_blob_id, raw_size_bytes) VALUES ('p-big', 'w11f-log', 'big', ?)`, len(bigHeader)); err != nil {
		t.Fatal(err)
	}
	detail, err = reader.GetAuditLogPayload(ctx, "w11f-log", "p-big")
	if err != nil || detail == nil {
		t.Fatalf("big header = %+v/%v", detail, err)
	}
	// 坏 gzip → 错误。
	if err := os.WriteFile(filepath.Join(blobDir, "badgz.bin"), []byte("not-gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs VALUES ('bgz', 'badgz.bin', 'gzip', 8, 8)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_refs (id, audit_log_id, body_blob_id, raw_size_bytes) VALUES ('p-badgz', 'w11f-log', 'bgz', 8)`); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.GetAuditLogPayload(ctx, "w11f-log", "p-badgz"); err == nil {
		t.Fatal("bad gzip must fail")
	}
	// 解压后尺寸不一致。
	if err := os.WriteFile(filepath.Join(blobDir, "rawmismatch.bin"), gzipBytes([]byte("12345")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs VALUES ('rm', 'rawmismatch.bin', 'gzip', 99, ?)`, len(gzipBytes([]byte("12345")))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_refs (id, audit_log_id, body_blob_id, raw_size_bytes) VALUES ('p-rm', 'w11f-log', 'rm', 99)`); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.GetAuditLogPayload(ctx, "w11f-log", "p-rm"); err == nil {
		t.Fatal("raw mismatch must fail")
	}
	// 越界 storage_key → resolveBlobPath 错误。
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs VALUES ('esc', '../escape.bin', 'none', 3, 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_refs (id, audit_log_id, body_blob_id, raw_size_bytes) VALUES ('p-esc', 'w11f-log', 'esc', 3)`); err != nil {
		t.Fatal(err)
	}
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
