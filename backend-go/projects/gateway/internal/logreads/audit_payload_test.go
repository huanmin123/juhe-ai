package logreads

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// hotSearchPinnedNow pins the search-hot window clock (window [12:00, 13:00]
// UTC on 2026-06-03).
var hotSearchPinnedNow = time.Date(2026, 6, 3, 13, 0, 0, 0, time.UTC)

// pinAuditHotClock pins the concrete audit reader clock.
func pinAuditHotClock(audit AuditLogReader, runtime RuntimeLogReader, public PublicApiLogReader) {
	_, _ = runtime, public
	concrete, ok := audit.(*auditLogSQLReader)
	if !ok {
		return
	}
	concrete.Now = func() time.Time { return hotSearchPinnedNow }
}

// writeAuditHotBucket writes one NDJSON bucket inside the given hour.
func writeAuditHotBucket(t *testing.T, hotDir string, when time.Time, lines ...string) string {
	t.Helper()
	if err := os.MkdirAll(hotDir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(hotDir, "audit-hot-"+when.UTC().Format("2006010215")+".ndjson")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	return path
}

func hotLine(id, createdAt, text string) string {
	encoded, err := json.Marshal(map[string]any{"auditLogId": id, "createdAt": createdAt, "text": text})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// TestAuditLogReadsSearchHot covers the /search-hot scan, item hydration,
// the empty-keyword contract, the strict range and the admin gate.
func TestAuditLogReadsSearchHot(t *testing.T) {
	env := newReadsTestEnv(t, auditReadsDDL, pinAuditHotClock, true)
	seedAuditReads(t, env)
	// One bucket inside the pinned window: log-2 (newest) and log-1 match
	// the "gpt" keyword, log-3 is internal probe traffic that the by-id
	// hydration drops, one malformed line is skipped.
	writeAuditHotBucket(t, env.hotDir, hotSearchPinnedNow.Add(-30*time.Minute),
		hotLine("log-2", "2026-06-03T12:50:00.000Z", "gpt-4o success body"),
		hotLine("log-1", "2026-06-03T12:10:00.000Z", "gpt-4o upstream exploded"),
		hotLine("log-3", "2026-06-03T12:20:00.000Z", "gpt internal probe"),
		"{not-json",
	)
	// A bucket outside the window (previous day) is never scanned.
	writeAuditHotBucket(t, env.hotDir, hotSearchPinnedNow.Add(-24*time.Hour),
		hotLine("log-2", "2026-06-02T13:00:00.000Z", "gpt old"))

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/search-hot?keywords=gpt", "")
	if code != http.StatusOK {
		t.Fatalf("search-hot status: %d %v", code, payload)
	}
	data := wantData(t, payload)
	if got := wantBool(t, data, "available"); !got {
		t.Fatalf("search-hot available: %v", data)
	}
	if got := wantFloat(t, data, "page"); got != 1 {
		t.Fatalf("search-hot page: %v", data["page"])
	}
	if got := wantFloat(t, data, "pageSize"); got != 100 {
		t.Fatalf("search-hot pageSize: %v", data["pageSize"])
	}
	if got := wantFloat(t, data, "total"); got != 2 {
		t.Fatalf("search-hot total: %v", data["total"])
	}
	if wantBool(t, data, "hasMore") {
		t.Fatalf("search-hot hasMore: %v", data["hasMore"])
	}
	ids := make([]string, 0)
	for _, item := range wantItems(t, data) {
		ids = append(ids, wantString(t, item, "id"))
	}
	if strings.Join(ids, ",") != "log-2,log-1" {
		t.Fatalf("search-hot items newest-first without internal probes: %v", ids)
	}
	if wantString(t, data, "startAt") != "2026-06-03T12:00:00.000Z" || wantString(t, data, "endAt") != "2026-06-03T13:00:00.000Z" {
		t.Fatalf("search-hot window: %v %v", data["startAt"], data["endAt"])
	}

	// Multi-value keywords behave additively; a keyword nobody matches
	// yields the empty envelope.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/search-hot?keywords=nobody&keywords=said", "")
	if code != http.StatusOK {
		t.Fatalf("search-hot multi status: %d %v", code, payload)
	}
	if got := wantFloat(t, wantData(t, payload), "total"); got != 0 {
		t.Fatalf("search-hot multi total: %v", wantData(t, payload)["total"])
	}

	// Empty keywords keep the Node available:true + message contract.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/search-hot", "")
	if code != http.StatusOK {
		t.Fatalf("search-hot empty status: %d %v", code, payload)
	}
	data = wantData(t, payload)
	if !wantBool(t, data, "available") || wantString(t, data, "message") != "请输入要搜索的审计内容关键字" {
		t.Fatalf("search-hot empty payload: %v", data)
	}

	// Invalid bounds are request faults (400, Node messages).
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/search-hot?startAt=not-a-time", "")
	if code != http.StatusBadRequest || wantString(t, payload, "message") != "开始时间必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("search-hot invalid startAt: %d %v", code, payload)
	}

	// Missing bucket directory answers the Node message.
	if err := os.RemoveAll(env.hotDir); err != nil {
		t.Fatal(err)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/search-hot?keywords=gpt", "")
	if code != http.StatusOK || wantString(t, wantData(t, payload), "message") != "最近 1 小时没有可搜索的审计内容" {
		t.Fatalf("search-hot missing directory: %d %v", code, payload)
	}
}

// seedPayloadBlobs provisions the audit_payload_blobs rows and files behind
// the seeded pay-1 refs (headers plain JSON, body gzip).
func seedPayloadBlobs(t *testing.T, env *readsTestEnv) (bodyText string) {
	t.Helper()
	headers := `{"content-type":"application/json","x-multi":["a","b"]}`
	body := `{"hello":"world"}`
	if err := os.MkdirAll(filepath.Join(env.blobDir, "sha256"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.blobDir, "sha256", "h1.blob"), []byte(headers), 0o640); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.blobDir, "sha256", "b1.gz"), compressed.Bytes(), 0o640); err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO audit_payload_blobs (id, sha256, raw_size_bytes, compressed_size_bytes, content_type,
		content_encoding, compression, storage_key, ref_count, first_seen_at, last_seen_at, created_at)
		VALUES ('blob-h1', 'sha-h', ?, ?, 'application/json', NULL, 'none', 'sha256/h1.blob', 1, ?, ?, ?)`,
		len(headers), len(headers), "2026-06-01T10:00:00.000Z", "2026-06-01T10:00:00.000Z", "2026-06-01T10:00:00.000Z")
	env.exec(t, `INSERT INTO audit_payload_blobs (id, sha256, raw_size_bytes, compressed_size_bytes, content_type,
		content_encoding, compression, storage_key, ref_count, first_seen_at, last_seen_at, created_at)
		VALUES ('blob-b1', 'sha-b', ?, ?, 'application/json', NULL, 'gzip', 'sha256/b1.gz', 1, ?, ?, ?)`,
		len(body), compressed.Len(), "2026-06-01T10:00:00.000Z", "2026-06-01T10:00:00.000Z", "2026-06-01T10:00:00.000Z")
	return body
}

// TestAuditLogReadsPayloadDetail covers the full payload read: headers
// object, gzip body as bodyText, storage statuses and the 404/permission
// contracts.
func TestAuditLogReadsPayloadDetail(t *testing.T) {
	env := newReadsTestEnv(t, auditReadsDDL, pinAuditHotClock, true)
	seedAuditReads(t, env)
	body := seedPayloadBlobs(t, env)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/log-1/payloads/pay-1", "")
	if code != http.StatusOK {
		t.Fatalf("payload detail status: %d %v", code, payload)
	}
	data := wantData(t, payload)
	if got := wantFloat(t, data, "sizeBytes"); got != 100 {
		t.Fatalf("payload sizeBytes: %v", data["sizeBytes"])
	}
	if got := wantFloat(t, data, "compressedSizeBytes"); got != 50 {
		t.Fatalf("payload compressedSizeBytes: %v", data["compressedSizeBytes"])
	}
	if !wantBool(t, data, "hasHeaders") || !wantBool(t, data, "hasBody") || !wantBool(t, data, "headersIncluded") {
		t.Fatalf("payload flags: %v", data)
	}
	if got := wantString(t, data, "bodyText"); got != body {
		t.Fatalf("payload bodyText: %q", got)
	}
	if _, present := data["bodyBase64"]; present {
		t.Fatalf("payload must not carry bodyBase64: %v", data)
	}
	headers, ok := data["headers"].(map[string]any)
	if !ok || headers["content-type"] != "application/json" {
		t.Fatalf("payload headers: %v", data["headers"])
	}
	if wantString(t, data, "headersStorageStatus") != "available" || wantString(t, data, "bodyStorageStatus") != "available" {
		t.Fatalf("payload storage statuses: %v", data)
	}
	if got := wantFloat(t, data, "bodyBytesReturned"); got != float64(len(body)) {
		t.Fatalf("payload bodyBytesReturned: %v", data["bodyBytesReturned"])
	}
	if got := wantFloat(t, data, "bodyTotalBytes"); got != float64(len(body)) {
		t.Fatalf("payload bodyTotalBytes: %v", data["bodyTotalBytes"])
	}
	if wantBool(t, data, "bodyTruncated") {
		t.Fatalf("payload bodyTruncated: %v", data)
	}

	// pay-2 carries no blob ids: not_saved statuses, no body/bodyText.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/log-1/payloads/pay-2", "")
	if code != http.StatusOK {
		t.Fatalf("payload without blobs status: %d %v", code, payload)
	}
	data = wantData(t, payload)
	if wantBool(t, data, "hasHeaders") || wantBool(t, data, "hasBody") {
		t.Fatalf("payload without blobs flags: %v", data)
	}
	if wantString(t, data, "headersStorageStatus") != "not_saved" || wantString(t, data, "bodyStorageStatus") != "not_saved" {
		t.Fatalf("payload without blobs statuses: %v", data)
	}
	if _, present := data["bodyText"]; present {
		t.Fatalf("payload without blobs must omit bodyText: %v", data)
	}

	// Unknown payload id answers the Node 404 message.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/log-1/payloads/pay-404", "")
	if code != http.StatusNotFound || wantString(t, payload, "message") != "审计原文不存在" {
		t.Fatalf("payload missing: %d %v", code, payload)
	}

	// Payloads of non-persisted traffic (the internal probe log) stay hidden.
	env.exec(t, `INSERT INTO audit_payload_refs (id, audit_log_id, part_type, sequence_index, raw_size_bytes,
		compressed_size_bytes, capture_status, created_at)
		VALUES ('pay-internal', 'log-3', 'client_request', 0, 10, 10, 'complete', '2026-06-03T10:00:01.000Z')`)
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/log-3/payloads/pay-internal", "")
	if code != http.StatusNotFound || wantString(t, payload, "message") != "审计原文不存在" {
		t.Fatalf("internal payload must 404: %d %v", code, payload)
	}

	// The subtree 404 contract for unknown audit sub-shapes.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/log-1/unknown/shape", "")
	if code != http.StatusNotFound {
		t.Fatalf("unknown audit subtree: %d %v", code, payload)
	}

	// Non-admin callers are denied; anonymous callers are unauthorized.
	if _, err := env.accounts.Create(context.Background(), authsys.CreateInput{
		Username: "viewer", DisplayName: "viewer_name", Password: "viewer-password-123", Role: "user",
		MustChangePassword: boolPtr(false),
	}); err != nil {
		t.Fatal(err)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/auth/login", `{"username":"viewer","password":"viewer-password-123"}`)
	if code != http.StatusOK {
		t.Fatalf("viewer login: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/log-1/payloads/pay-1", "")
	if code != http.StatusForbidden || wantString(t, payload, "message") != "需要管理员权限" {
		t.Fatalf("payload as user: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/search-hot?keywords=gpt", "")
	if code != http.StatusForbidden {
		t.Fatalf("search-hot as user: %d %v", code, payload)
	}
	env.resetSession()
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/log-1/payloads/pay-1", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous payload: %d %v", code, payload)
	}
}
