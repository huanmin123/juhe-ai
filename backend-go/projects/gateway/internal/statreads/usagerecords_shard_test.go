package statreads

// usage-records SQLite shard 读取契约：目录登记的 shard 文件枚举、跨 shard
// 合并倒序、名称水合（api_key/group/account/system_account）、失败原因归一
// 与分页上界（Node usage-records.repository.ts + usage-record-mappers.ts）。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

const shardSchema = `
	CREATE TABLE usage_records (
		id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, trace_id TEXT NOT NULL, traffic_source TEXT NOT NULL,
		client_ip TEXT, api_key_id TEXT, group_id TEXT, account_id TEXT, endpoint TEXT,
		model TEXT, upstream_model TEXT, upstream_response_model TEXT, billed_service_tier TEXT,
		effective_reasoning_effort TEXT, model_mapping_applied INTEGER DEFAULT 0, stream INTEGER DEFAULT 0,
		status_code INTEGER, success INTEGER DEFAULT 0, failure_attribution TEXT,
		error_code TEXT, error_message TEXT, first_token_ms INTEGER, duration_ms INTEGER,
		input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER, cost_usd REAL,
		created_at TEXT NOT NULL);
`

func TestUsageRecordsSQLiteShardWalk(t *testing.T) {
	fixture := newFixture(t)
	shardDir := t.TempDir()
	shardPath := filepath.Join(shardDir, "usage_20260904_s000.sqlite3")
	shardDB, err := sql.Open("sqlite", shardPath)
	if err != nil {
		t.Fatalf("open shard: %v", err)
	}
	if _, err := shardDB.Exec(shardSchema); err != nil {
		t.Fatalf("shard schema: %v", err)
	}
	seedRows := []string{
		// 旧记录在前；两条记录跨同一 shard 验证倒序与水合。
		`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, client_ip, api_key_id, group_id,
			account_id, endpoint, stream, status_code, success, first_token_ms, duration_ms, input_tokens, output_tokens,
			cache_read_tokens, cost_usd, created_at) VALUES
			('u-1', 'sys-user-1', 'trace-1', 'gateway', '10.0.0.1', 'key-1', 'g-1', 'acct-1', '/v1/chat',
			 1, 200, 1, 120, 900, 100, 50, 10, 0.25, '2026-09-04T10:00:00.000Z')`,
		`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, client_ip, error_code, error_message,
			failure_attribution, success, status_code, created_at) VALUES
			('u-2', 'sys-user-1', 'trace-2', 'gateway', '10.0.0.2', 'rate_limit_exceeded', NULL, 'gateway_policy',
			 0, 429, '2026-09-04T11:00:00.000Z')`,
	}
	for _, statement := range seedRows {
		if _, err := shardDB.Exec(statement); err != nil {
			t.Fatalf("shard seed: %v", err)
		}
	}
	if err := shardDB.Close(); err != nil {
		t.Fatalf("close shard: %v", err)
	}

	catalog, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	defer catalog.Close()
	if _, err := catalog.Exec(`CREATE TABLE usage_record_shards (
		shard_key TEXT PRIMARY KEY, bucket_date TEXT NOT NULL, shard_id INTEGER NOT NULL,
		file_path TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active')`); err != nil {
		t.Fatalf("catalog schema: %v", err)
	}
	if _, err := catalog.Exec(`INSERT INTO usage_record_shards (shard_key, bucket_date, shard_id, file_path, status)
		VALUES ('20260904:s000', '2026-09-04', 0, ?, 'active')`, shardPath); err != nil {
		t.Fatalf("catalog seed: %v", err)
	}
	fixture.deps.UsageCatalog = catalog

	// 业务侧名称水合源。
	for _, statement := range []string{
		`INSERT INTO api_keys (id, name, system_account_id) VALUES ('key-1', '主 Key', 'sys-user-1')`,
		`INSERT INTO groups (id, name, system_account_id) VALUES ('g-1', '默认组', 'sys-user-1')`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status) VALUES ('acct-1', '账户A', 'sys-user-1', 'openai', 'api_key', 'active')`,
		`INSERT INTO system_accounts (id, username, display_name) VALUES ('sys-user-1', 'user1', 'User One')`,
	} {
		if _, err := fixture.db.Exec(statement); err != nil {
			t.Fatalf("business seed: %v", err)
		}
	}

	handler := fixture.deps.usageRecordsListHandler(true)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-usage-records", nil)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{
		SystemAccountID: "sys-user-1", Username: "user1", Role: "user",
	}))
	handler(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Items []struct {
				ID                    string  `json:"id"`
				SystemAccountID       *string `json:"systemAccountId"`
				SystemAccountName     *string `json:"systemAccountName"`
				ApiKeyName            *string `json:"apiKeyName"`
				GroupName             *string `json:"groupName"`
				AccountName           *string `json:"accountName"`
				Success               bool    `json:"success"`
				StatusCode            *int64  `json:"statusCode"`
				FailureReason         *string `json:"failureReason"`
				FailureAttribution    *string `json:"failureAttribution"`
				UpstreamModelMismatch bool    `json:"upstreamModelMismatch"`
				CreatedAt             string  `json:"createdAt"`
			} `json:"items"`
			Total int `json:"total"`
		} `json:"data"`
	}
	if err := jsonUnmarshalHelper(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	items := payload.Data.Items
	// self 面（user 角色）不含 system_account 字段，但保持全部行。
	if len(items) != 2 || payload.Data.Total != 2 {
		t.Fatalf("items wrong: %#v", payload)
	}
	// created_at DESC：u-2 在前。
	if items[0].ID != "u-2" || items[1].ID != "u-1" {
		t.Fatalf("order wrong: %#v", items)
	}
	failed := items[0]
	if failed.FailureAttribution == nil || *failed.FailureAttribution != "gateway_policy" {
		t.Fatalf("attribution wrong: %#v", failed.FailureAttribution)
	}
	if failed.FailureReason == nil {
		t.Fatalf("failure reason missing")
	}
	// Node 契约：errorCode/errorMessage 组成的事实串优先（有 error_code 时
	// 直接展示该代码，中文映射只在其余归因上生效）。
	if *failed.FailureReason != "rate_limit_exceeded" {
		t.Fatalf("failure reason wrong: %q", *failed.FailureReason)
	}
	if items[1].ApiKeyName == nil || *items[1].ApiKeyName != "主 Key" ||
		items[1].GroupName == nil || *items[1].GroupName != "默认组" ||
		items[1].AccountName == nil || *items[1].AccountName != "账户A" {
		t.Fatalf("hydration wrong: %#v", items[1])
	}
	// 管理面（admin 未过滤 scope）读全局，system_account 字段注入 display 名。
	adminHandler := fixture.deps.usageRecordsListHandler(false)
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/api/usage-records", nil)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), adminAuth("super_admin")))
	adminHandler(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin list not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	var adminPayload struct {
		Data struct {
			Items []struct {
				SystemAccountName *string `json:"systemAccountName"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := jsonUnmarshalHelper(recorder.Body.Bytes(), &adminPayload); err != nil {
		t.Fatalf("decode admin: %v", err)
	}
	if len(adminPayload.Data.Items) != 2 || adminPayload.Data.Items[0].SystemAccountName == nil || *adminPayload.Data.Items[0].SystemAccountName != "User One" {
		t.Fatalf("admin hydration wrong: %#v", adminPayload.Data.Items)
	}
	_ = context.Background
}
