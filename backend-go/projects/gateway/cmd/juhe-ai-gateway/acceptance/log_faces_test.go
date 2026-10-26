// X04 404 项补齐：三日志读面（audit-logs / runtime-logs / public-api-logs）
// 的进程级 200 探测。go 模式下这三个家族在 compose 挂载 logreads.ReadsDeps
// （F3/F1/F5 数据集读 + 文件日志 grep），未登录保持 401 请先登录契约；
// 2026-09-28 默认开启整改后，未配 JUHE_AI_LOG_DIR 时组合根派生
// <DATA_DIR>/logs（写侧 NewFileSink 代建目录），grep 面可用——无命中时
// available:true + "没有匹配"提示；只有显式 disabled 才落回禁用契约。
// PostgreSQL + Redis 唯一存储形态：runtime-logs 读面经 juhe_dataset schema，
// F1 数据集表由 jobs 进程自建——fresh 验收环境由 harness 按生产 DDL 预置。
package acceptance

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"
)

// provisionRuntimeLogDataset mirrors a deployed F1 indexer for fresh
// acceptance boots：F1 runtime-log 数据集表由 jobs 进程自建（jobs internal
// runtimelog schema 的 PG DDL，juhe_dataset 限定；跨模块不可导入，此处按
// 生产 DDL 摘录建读面四表 + 索引），让 runtime-logs 读面可答。
func provisionRuntimeLogDataset(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open runtime-log dataset: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	statements := []string{
		`CREATE SCHEMA IF NOT EXISTS juhe_dataset`,
		`CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_logs (
		  id text PRIMARY KEY,
		  log_file text,
		  log_offset bigint,
		  line_number integer,
		  time timestamptz NOT NULL,
		  level text NOT NULL,
		  trace_id text,
		  event text,
		  message text,
		  error_message text,
		  raw_json text NOT NULL,
		  created_at timestamptz NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_facet_summary (
		  bucket_key text PRIMARY KEY,
		  total_count integer NOT NULL DEFAULT 0,
		  earliest_time timestamptz,
		  latest_time timestamptz,
		  updated_at timestamptz NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_level_facets (
		  bucket_key text NOT NULL,
		  level text NOT NULL,
		  count integer NOT NULL DEFAULT 0,
		  updated_at timestamptz NOT NULL,
		  PRIMARY KEY (bucket_key, level)
		)`,
		`CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_event_facets (
		  bucket_key text NOT NULL,
		  event text NOT NULL,
		  count integer NOT NULL DEFAULT 0,
		  latest_time timestamptz,
		  updated_at timestamptz NOT NULL,
		  PRIMARY KEY (bucket_key, event)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_runtime_logs_time ON juhe_dataset.runtime_logs(time DESC, id DESC)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("provision runtime-log dataset: %v", err)
		}
	}
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
