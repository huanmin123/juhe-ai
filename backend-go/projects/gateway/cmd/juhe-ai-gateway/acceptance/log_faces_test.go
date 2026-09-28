// X04 404 项补齐：三日志读面（audit-logs / runtime-logs / public-api-logs）
// 的进程级 200 探测。go 模式下这三个家族在 compose 挂载 logreads.ReadsDeps
// （F3/F1/F5 数据集读 + 文件日志 grep），未登录保持 401 请先登录契约；
// 2026-09-28 默认开启整改后，未配 JUHE_AI_LOG_DIR 时组合根派生
// <DATA_DIR>/logs（写侧 NewFileSink 代建目录），grep 面可用——无命中时
// available:true + "没有匹配"提示；只有显式 disabled 才落回禁用契约。
package acceptance

import (
	"database/sql"
	"net/http"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

// provisionRuntimeLogDataset mirrors a deployed F1 indexer for fresh
// acceptance boots: the runtime-log SQLite file is F1-jobs-owned (the
// gateway only opens it read-only), so the minimal dataset schema is applied
// here to make the runtime-logs read faces answerable.
func provisionRuntimeLogDataset(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open runtime-log dataset: %v", err)
	}
	defer db.Close()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS runtime_logs (
		  id TEXT PRIMARY KEY, log_file TEXT, log_offset INTEGER, line_number INTEGER,
		  time TEXT NOT NULL, level TEXT NOT NULL, trace_id TEXT, event TEXT, message TEXT,
		  error_message TEXT, raw_json TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS runtime_log_facet_summary (
		  bucket_key TEXT PRIMARY KEY, total_count INTEGER NOT NULL DEFAULT 0,
		  earliest_time TEXT, latest_time TEXT, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS runtime_log_level_facets (
		  bucket_key TEXT NOT NULL, level TEXT NOT NULL, count INTEGER NOT NULL DEFAULT 0,
		  updated_at TEXT NOT NULL, PRIMARY KEY (bucket_key, level))`,
		`CREATE TABLE IF NOT EXISTS runtime_log_event_facets (
		  bucket_key TEXT NOT NULL, event TEXT NOT NULL, count INTEGER NOT NULL DEFAULT 0,
		  latest_time TEXT, updated_at TEXT NOT NULL, PRIMARY KEY (bucket_key, event))`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("provision runtime-log dataset: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close runtime-log dataset: %v", err)
	}
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
}

func TestAcceptanceLogReadFaces(t *testing.T) {
	fixture := startGateway(t, gatewayEnvOptions{})
	client := &acceptanceClient{t: t, http: fixture.admin, baseURL: fixture.baseURL}

	for _, path := range []string{
		"/__aisys__/api/audit-logs",
		"/__aisys__/api/audit-logs/runtime",
		"/__aisys__/api/audit-logs/search-hot",
		"/__aisys__/api/audit-logs/error-groups",
		"/__aisys__/api/runtime-logs",
		"/__aisys__/api/runtime-logs/facets",
		"/__aisys__/api/runtime-logs/grep-options",
		"/__aisys__/api/runtime-logs/grep",
		"/__aisys__/api/public-api-logs",
	} {
		client.do(http.MethodGet, path, nil, wantStatus(http.StatusOK))
	}

	// grep 未配 JUHE_AI_LOG_DIR：默认派生 <DATA_DIR>/logs，目录可用、无命中
	// → available:true + "没有匹配"提示（默认开启整改 2026-09-28）。
	_, grep := client.do(http.MethodGet, "/__aisys__/api/runtime-logs/grep?keywords=probe", nil, wantStatus(http.StatusOK))
	if data(grep)["available"] != true {
		t.Fatalf("grep available=%v, want the derived-directory contract: %v", data(grep)["available"], data(grep))
	}
	// search-hot 无关键字：available:true + 中文提示。
	_, hot := client.do(http.MethodGet, "/__aisys__/api/audit-logs/search-hot", nil, wantStatus(http.StatusOK))
	if data(hot)["available"] != true {
		t.Fatalf("search-hot available=%v", data(hot)["available"])
	}

	// 未登录 → 401 请先登录（requireAdmin 契约，不是 404 回落）。
	anonymous := &acceptanceClient{t: t, http: &http.Client{}, baseURL: fixture.baseURL}
	for _, path := range []string{
		"/__aisys__/api/audit-logs",
		"/__aisys__/api/audit-logs/search-hot",
		"/__aisys__/api/audit-logs/some-id",
		"/__aisys__/api/audit-logs/some-id/payloads/some-payload",
		"/__aisys__/api/runtime-logs",
		"/__aisys__/api/runtime-logs/grep",
		"/__aisys__/api/public-api-logs",
	} {
		anonymous.do(http.MethodGet, path, nil, wantStatus(http.StatusUnauthorized))
	}
}
