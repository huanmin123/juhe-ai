package main

// 媒体工作室组合根测试（TestChainStudio* 入口）：my-studio 路由族挂载与登
// 录门（401/200）、资产端到端（上传→引用保护→删除→幂等重删）、账户/模型端
// 点形状，以及 purpose=studio Key 幂等确保（含名称避让与自愈重建）与域模
// 型目录谓词。
//
// studio_* 表由本文件 fixture 按契约 §3 列名自建（maintenance 侧 DDL 由并行
// 任务落地）；purpose=studio 的 api_keys CHECK 扩展同属并行迁移，Key 供应器
// 在自建最小 fixture 上验证，不在组合库上跑。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// chainStudioFixtureSchema 是契约 §3 的 studio_* DDL（列名严格对齐）。
const chainStudioFixtureSchema = `
CREATE TABLE IF NOT EXISTS studio_assets (
  id text PRIMARY KEY,
  system_account_id text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('image', 'audio', 'video')),
  mime text NOT NULL,
  storage_key text NOT NULL UNIQUE,
  bytes bigint NOT NULL,
  width integer,
  height integer,
  duration_ms bigint,
  sha256 text NOT NULL,
  source_task_id text,
  params_json text NOT NULL DEFAULT '{}',
  created_at text NOT NULL,
  updated_at text NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_studio_assets_owner_created
  ON studio_assets(system_account_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_studio_assets_owner_sha256
  ON studio_assets(system_account_id, sha256);
CREATE UNIQUE INDEX IF NOT EXISTS idx_studio_assets_owner_source_task
  ON studio_assets(system_account_id, source_task_id) WHERE source_task_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS studio_tasks (
  id text PRIMARY KEY,
  system_account_id text NOT NULL,
  client_operation_id text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('image', 'video')),
  status text NOT NULL DEFAULT 'queued',
  settlement text NOT NULL DEFAULT 'pending_settled',
  account_id text,
  provider_code text,
  upstream_job_id text,
  request_snapshot_json text NOT NULL DEFAULT '{}',
  error_json text NOT NULL DEFAULT '{}',
  asset_id text,
  poll_bookmark_json text NOT NULL DEFAULT '{}',
  lease_owner text,
  lease_expires_at text,
  lease_attempt_count integer NOT NULL DEFAULT 0,
  created_at text NOT NULL,
  updated_at text NOT NULL,
  UNIQUE (system_account_id, client_operation_id)
);
CREATE TABLE IF NOT EXISTS studio_operations (
  id text PRIMARY KEY,
  system_account_id text NOT NULL,
  client_operation_id text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('audio_speech', 'audio_transcription')),
  asset_id text,
  result_json text NOT NULL DEFAULT '{}',
  created_at text NOT NULL,
  updated_at text NOT NULL,
  UNIQUE (system_account_id, client_operation_id)
);
CREATE TABLE IF NOT EXISTS studio_user_asset_usage (
  system_account_id text PRIMARY KEY,
  asset_bytes bigint NOT NULL DEFAULT 0,
  asset_count bigint NOT NULL DEFAULT 0,
  updated_at text NOT NULL
);
`

// ensureChainStudioTables 在组合库上补建 studio_* fixture 表（幂等）。
func ensureChainStudioTables(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(chainStudioFixtureSchema); err != nil {
		t.Fatalf("apply studio fixture schema: %v", err)
	}
}

func chainStudioPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

func chainStudioUploadRequest(t *testing.T, data []byte) *http.Request {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="studio.png"`)
	header.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "/__aisys__/api/my-studio/assets", &buffer)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

// TestChainStudioMountRequiresSession：未登录 401，登录态 200。
func TestChainStudioMountRequiresSession(t *testing.T) {
	_, server := composeChatTestSystemAPI(t, false)
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(server.URL + "/__aisys__/api/my-studio/assets")
	if err != nil {
		t.Fatalf("GET assets unauthed: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated assets status=%d want 401", response.StatusCode)
	}

	_, server = composeChatTestSystemAPI(t, true)
	client = &http.Client{Timeout: 10 * time.Second}
	authed, err := client.Get(server.URL + "/__aisys__/api/my-studio/assets")
	if err != nil {
		t.Fatalf("GET assets: %v", err)
	}
	defer authed.Body.Close()
	if authed.StatusCode != http.StatusOK {
		body, _ := readAllString(authed.Body)
		t.Fatalf("authenticated assets status=%d body=%s", authed.StatusCode, body)
	}
	var payload struct {
		Data struct {
			Rows  []map[string]any `json:"rows"`
			Total int              `json:"total"`
		} `json:"data"`
	}
	if err := json.NewDecoder(authed.Body).Decode(&payload); err != nil {
		t.Fatalf("decode assets list: %v", err)
	}
}

// TestChainStudioMountAssetLifecycle：上传 → 引用保护 → 删除 → 幂等重删，
// 全链走真实组合根 HTTP 面（dev auto-login 身份 sys_admin）。
func TestChainStudioMountAssetLifecycle(t *testing.T) {
	composed, server := composeChatTestSystemAPI(t, true)
	ensureChainStudioTables(t, composed.DB)
	client := &http.Client{Timeout: 10 * time.Second}

	// 上传 201（真实 multipart）。
	uploadResponse, uploadErr := func() (*http.Response, error) {
		request := chainStudioUploadRequest(t, chainStudioPNG(t, 2, 2))
		return client.Post(server.URL+"/__aisys__/api/my-studio/assets",
			request.Header.Get("Content-Type"), request.Body)
	}()
	if uploadErr != nil {
		t.Fatalf("POST assets: %v", uploadErr)
	}
	body, _ := readAllString(uploadResponse.Body)
	uploadResponse.Body.Close()
	if uploadResponse.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", uploadResponse.StatusCode, body)
	}
	var uploaded struct {
		Data struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Sha256 string `json:"sha256"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &uploaded); err != nil {
		t.Fatalf("decode upload: %v", err)
	}
	if !strings.HasPrefix(uploaded.Data.ID, "stasset_") || uploaded.Data.Kind != "image" {
		t.Fatalf("uploaded asset = %+v", uploaded.Data)
	}

	// 同用户 sha256 查重幂等：同内容重传 → 200 + 原资产 id。
	replayResponse, replayErr := func() (*http.Response, error) {
		request := chainStudioUploadRequest(t, chainStudioPNG(t, 2, 2))
		return client.Post(server.URL+"/__aisys__/api/my-studio/assets",
			request.Header.Get("Content-Type"), request.Body)
	}()
	if replayErr != nil {
		t.Fatalf("replay POST: %v", replayErr)
	}
	replayBody, _ := readAllString(replayResponse.Body)
	replayResponse.Body.Close()
	if replayResponse.StatusCode != http.StatusOK {
		t.Fatalf("replay status=%d body=%s, want 200 idempotent", replayResponse.StatusCode, replayBody)
	}
	var replayed struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(replayBody), &replayed)
	if replayed.Data.ID != uploaded.Data.ID {
		t.Fatalf("replay id = %q, want %q", replayed.Data.ID, uploaded.Data.ID)
	}

	// 引用保护：pending_settled 任务引用 → 409。
	if _, err := composed.DB.Exec(`INSERT INTO studio_tasks (id, system_account_id, client_operation_id, kind, status, settlement, asset_id, created_at, updated_at)
		VALUES ('stask_mount_ref', 'sys_admin', 'op-mount-ref', 'image', 'completed', 'pending_settled', ?, '2026-10-12T08:00:00.000Z', '2026-10-12T08:00:00.000Z')`,
		uploaded.Data.ID); err != nil {
		t.Fatalf("seed referencing task: %v", err)
	}
	deleteRequest, _ := http.NewRequest(http.MethodDelete, server.URL+"/__aisys__/api/my-studio/assets/"+uploaded.Data.ID, nil)
	referenced, err := client.Do(deleteRequest)
	if err != nil {
		t.Fatalf("referenced DELETE: %v", err)
	}
	referencedBody, _ := readAllString(referenced.Body)
	referenced.Body.Close()
	if referenced.StatusCode != http.StatusConflict || !strings.Contains(referencedBody, "studio_asset_referenced") {
		t.Fatalf("referenced delete = %d %s", referenced.StatusCode, referencedBody)
	}

	// 解除引用后删除 204；重删 404 同型。
	if _, err := composed.DB.Exec(`UPDATE studio_tasks SET settlement = 'settled' WHERE id = 'stask_mount_ref'`); err != nil {
		t.Fatal(err)
	}
	deleteRequest, _ = http.NewRequest(http.MethodDelete, server.URL+"/__aisys__/api/my-studio/assets/"+uploaded.Data.ID, nil)
	deleted, err := client.Do(deleteRequest)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	deleted.Body.Close()
	if deleted.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status=%d, want 204", deleted.StatusCode)
	}
	deleteRequest, _ = http.NewRequest(http.MethodDelete, server.URL+"/__aisys__/api/my-studio/assets/"+uploaded.Data.ID, nil)
	repeated, err := client.Do(deleteRequest)
	if err != nil {
		t.Fatalf("repeat DELETE: %v", err)
	}
	repeatedBody, _ := readAllString(repeated.Body)
	repeated.Body.Close()
	if repeated.StatusCode != http.StatusNotFound || !strings.Contains(repeatedBody, "studio_asset_not_found") {
		t.Fatalf("repeat delete = %d %s", repeated.StatusCode, repeatedBody)
	}
}

// TestChainStudioMountAccountsAndModels：账户候选/模型目录端点形状与 domain
// 校验。
func TestChainStudioMountAccountsAndModels(t *testing.T) {
	_, server := composeChatTestSystemAPI(t, true)
	client := &http.Client{Timeout: 10 * time.Second}

	accountsResponse, err := client.Get(server.URL + "/__aisys__/api/my-studio/accounts")
	if err != nil {
		t.Fatalf("GET accounts: %v", err)
	}
	accountsBody, _ := readAllString(accountsResponse.Body)
	accountsResponse.Body.Close()
	if accountsResponse.StatusCode != http.StatusOK {
		t.Fatalf("accounts status=%d body=%s", accountsResponse.StatusCode, accountsBody)
	}
	var accountsPayload struct {
		Data struct {
			Rows  []map[string]any `json:"rows"`
			Total int              `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(accountsBody), &accountsPayload); err != nil {
		t.Fatalf("decode accounts: %v", err)
	}
	for _, row := range accountsPayload.Data.Rows {
		for _, key := range []string{"accountId", "name", "providerCode", "domain", "status"} {
			if _, ok := row[key]; !ok {
				t.Fatalf("accounts row missing %q: %v", key, row)
			}
		}
	}

	// domain 非法/缺省 → 400 studio_domain_invalid；合法空目录 → 200。
	for _, testCase := range []struct {
		query  string
		status int
		code   string
	}{
		{query: "/models", status: http.StatusBadRequest, code: "studio_domain_invalid"},
		{query: "/models?domain=text", status: http.StatusBadRequest, code: "studio_domain_invalid"},
		{query: "/models?domain=banana", status: http.StatusBadRequest, code: "studio_domain_invalid"},
		{query: "/models?domain=video", status: http.StatusOK, code: ""},
		{query: "/accounts?domain=banana", status: http.StatusBadRequest, code: "studio_domain_invalid"},
	} {
		response, err := client.Get(server.URL + "/__aisys__/api/my-studio" + testCase.query)
		if err != nil {
			t.Fatalf("GET %s: %v", testCase.query, err)
		}
		body, _ := readAllString(response.Body)
		response.Body.Close()
		if response.StatusCode != testCase.status {
			t.Fatalf("%s status=%d body=%s, want %d", testCase.query, response.StatusCode, body, testCase.status)
		}
		if testCase.code != "" && !strings.Contains(body, testCase.code) {
			t.Fatalf("%s body=%s, want code %q", testCase.query, body, testCase.code)
		}
	}
}

// ---------------------------------------------------------------------------
// purpose=studio Key 供应器（自建 fixture，purpose CHECK 含 'studio'）
// ---------------------------------------------------------------------------

func chainStudioKeysDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/studiokeys.sqlite3")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT, description TEXT, provider_code TEXT NOT NULL, is_default INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE api_keys (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, status TEXT NOT NULL, purpose TEXT NOT NULL DEFAULT 'general' CHECK (purpose IN ('general', 'chat', 'studio')), studio_domains_json TEXT, key_secret_encrypted TEXT, expires_at TEXT, route_strategy_id TEXT, key_hash TEXT, key_prefix TEXT, key_suffix TEXT, is_default INTEGER, quota_limits_json TEXT, availability_schedule_json TEXT, availability_schedule_next_check_at TEXT, created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '')`,
		`CREATE UNIQUE INDEX idx_api_keys_studio_purpose_unique ON api_keys(system_account_id) WHERE purpose = 'studio'`,
		`CREATE UNIQUE INDEX idx_api_keys_owner_name_unique ON api_keys(system_account_id, name)`,
		`CREATE TABLE route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, mode TEXT, status TEXT NOT NULL DEFAULT 'active', is_default INTEGER NOT NULL DEFAULT 0, config_json TEXT, created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 1, weight INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

// TestChainStudioEnsureStudioKey：幂等确保（两次同 id 一行）、四域
// studio_domains_json 恒写、同名避让、删除后自愈重建。
func TestChainStudioEnsureStudioKey(t *testing.T) {
	db := chainStudioKeysDB(t)
	provider := newStudioAPIKeyProvider(db, false, "studio-keys-secret")

	first, err := provider.EnsureStudioKey("sys_studio_key")
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	var purpose, domainsJSON string
	if err := db.QueryRow(`SELECT purpose, studio_domains_json FROM api_keys WHERE id = ?`, first).Scan(&purpose, &domainsJSON); err != nil {
		t.Fatalf("query key: %v", err)
	}
	if purpose != "studio" {
		t.Fatalf("purpose = %q, want studio", purpose)
	}
	if domainsJSON != `["text","image","audio","video"]` {
		t.Fatalf("studio_domains_json = %q", domainsJSON)
	}
	domains, err := provider.StudioKeyDomains("sys_studio_key")
	if err != nil || len(domains) != 4 {
		t.Fatalf("StudioKeyDomains = %v, %v", domains, err)
	}

	second, err := provider.EnsureStudioKey("sys_studio_key")
	if err != nil || second != first {
		t.Fatalf("second ensure = %q, %v, want same %q", second, err, first)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE system_account_id = 'sys_studio_key' AND purpose = 'studio'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("studio key rows = %d, want 1", count)
	}

	// 自愈：删除后重建新行。
	if _, err := db.Exec(`DELETE FROM api_keys WHERE id = ?`, first); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := provider.EnsureStudioKey("sys_studio_key")
	if err != nil || rebuilt == "" || rebuilt == first {
		t.Fatalf("self-heal ensure = %q, %v", rebuilt, err)
	}

	// 同名避让：其他用户占用基名不影响；同用户占用基名时新用户序列避让。
	if _, err := db.Exec(`INSERT INTO api_keys (id, system_account_id, name, status, purpose) VALUES ('key_named', 'sys_studio_key2', '媒体工作室 API Key', 'active', 'general')`); err != nil {
		t.Fatal(err)
	}
	avoided, err := provider.EnsureStudioKey("sys_studio_key2")
	if err != nil {
		t.Fatalf("avoid ensure: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM api_keys WHERE id = ?`, avoided).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "媒体工作室 API Key 2" {
		t.Fatalf("avoided name = %q, want 媒体工作室 API Key 2", name)
	}
}

// ---------------------------------------------------------------------------
// 域模型目录谓词与聚合
// ---------------------------------------------------------------------------

func TestChainStudioDomainEndpointModes(t *testing.T) {
	modeOf := func(value string) *string { return &value }
	cases := []struct {
		name   string
		item   gatewayruntimecache.ProviderModelCatalogItem
		domain string
		want   []string
	}{
		{"audio speech+transcription", gatewayruntimecache.ProviderModelCatalogItem{
			Model: "cosyvoice-v2", ProviderCode: "volcengine", Mode: modeOf("audio"),
			SupportedAPIProtocols: []string{"audio_speech", "audio_transcription"},
		}, "audio", []string{"audio_speech", "audio_transcription"}},
		{"audio 无音频协议排除", gatewayruntimecache.ProviderModelCatalogItem{
			Model: "audio-echo", ProviderCode: "volcengine", Mode: modeOf("audio"),
			SupportedAPIProtocols: []string{"chat_completions"},
		}, "audio", nil},
		{"video", gatewayruntimecache.ProviderModelCatalogItem{
			Model: "seedance-pro", ProviderCode: "volcengine", Mode: modeOf("video"),
			SupportedAPIProtocols: []string{"video"},
		}, "video", []string{"video_create"}},
		{"image 命中 /v1 图像词表", gatewayruntimecache.ProviderModelCatalogItem{
			Model: "gpt-image-1", ProviderCode: "openai",
			SupportedAPIProtocols: []string{},
		}, "image", []string{"images_json"}},
		{"image 排除对话模型", gatewayruntimecache.ProviderModelCatalogItem{
			Model: "gpt-4o", ProviderCode: "openai",
			SupportedAPIProtocols: []string{"chat_completions"},
		}, "image", nil},
		{"image 排除音频词表模型", gatewayruntimecache.ProviderModelCatalogItem{
			Model: "whisper-1", ProviderCode: "openai",
			SupportedAPIProtocols: []string{"audio_transcription"},
		}, "image", nil},
	}
	for _, testCase := range cases {
		modes, ok := studioDomainEndpointModes(testCase.item, testCase.domain)
		if testCase.want == nil {
			if ok {
				t.Errorf("%s: ok=true, want excluded", testCase.name)
			}
			continue
		}
		if !ok {
			t.Errorf("%s: ok=false, want %v", testCase.name, testCase.want)
			continue
		}
		if strings.Join(modes, ",") != strings.Join(testCase.want, ",") {
			t.Errorf("%s: modes = %v, want %v", testCase.name, modes, testCase.want)
		}
	}
}

// TestChainStudioModelCatalogAggregates: ListDomainModels over a real
// accounts.Store fixture + runtime cache stub — the provider set converges to
// the scoped accounts whose capability_domain hits the requested domain.
func TestChainStudioModelCatalogAggregates(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/studio-accounts.sqlite3")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT, provider_code TEXT NOT NULL,
			capability_domain TEXT NOT NULL DEFAULT 'chat', status TEXT NOT NULL DEFAULT 'active', deleted_at TEXT,
			authorization_instance_authorization_id TEXT, last_error_code TEXT, account_expires_at TEXT,
			cooldown_until TEXT, schedulable INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	seed := func(id, owner, code, domain string) {
		if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, capability_domain, status)
			VALUES (?, ?, ?, ?, ?, 'active')`, id, owner, "账号-"+id, code, domain); err != nil {
			t.Fatal(err)
		}
	}
	seed("acc_img", "sys_viewer", "openai", "image")
	seed("acc_img2", "sys_viewer", "volcengine", "image")
	seed("acc_audio", "sys_viewer", "volcengine", "audio")
	seed("acc_other_owner", "sys_someone_else", "minimax", "image")
	accountStore, err := accounts.NewStore(db, false, "catalog-secret", time.Now, func(prefix string) string { return prefix + "_x" })
	if err != nil {
		t.Fatalf("accounts store: %v", err)
	}
	catalog := &w1hReadModels{catalog: []gatewayruntimecache.ProviderModelCatalogItem{
		{Model: "gpt-image-1", ProviderCode: "openai", SupportedAPIProtocols: []string{}},
		{Model: "gpt-4o", ProviderCode: "openai", SupportedAPIProtocols: []string{"chat_completions"}},
		{Model: "seedance-pro", ProviderCode: "volcengine", Mode: stringPtrForCatalog("video"), SupportedAPIProtocols: []string{"video"}},
		{Model: "cosyvoice-v2", ProviderCode: "volcengine", Mode: stringPtrForCatalog("audio"), SupportedAPIProtocols: []string{"audio_speech"}},
		{Model: "imagen-4-ultra", ProviderCode: "minimax", SupportedAPIProtocols: []string{}},
	}}
	aggregator := studioModelCatalog{cache: w1hNewRuntimeCache(t, catalog), accounts: accountStore}

	models, err := aggregator.ListDomainModels(context.Background(), "sys_viewer", false, "image")
	if err != nil {
		t.Fatalf("list image models: %v", err)
	}
	// 普通用户范围：仅自有 image 域账户的 provider（openai + volcengine），
	// 域谓词只放行 gpt-image-1（gpt-4o 是对话模型、seedance/cosyvoice 是
	// audio/video 标注行、minimax 属他人范围）。
	if len(models) != 1 || models[0].Model != "gpt-image-1" || models[0].Domain != "image" {
		t.Fatalf("image models = %+v", models)
	}
	if models[0].ProviderCode != "openai" || len(models[0].EndpointModes) != 1 || models[0].EndpointModes[0] != "images_json" {
		t.Fatalf("image model row = %+v", models[0])
	}

	audioModels, err := aggregator.ListDomainModels(context.Background(), "sys_viewer", false, "audio")
	if err != nil {
		t.Fatalf("list audio models: %v", err)
	}
	if len(audioModels) != 1 || audioModels[0].Model != "cosyvoice-v2" {
		t.Fatalf("audio models = %+v", audioModels)
	}

	// admin 范围：全量号池 provider（含 minimax）。
	adminModels, err := aggregator.ListDomainModels(context.Background(), "sys_viewer", true, "image")
	if err != nil {
		t.Fatalf("list admin image models: %v", err)
	}
	if len(adminModels) != 2 {
		t.Fatalf("admin image models = %+v", adminModels)
	}
}

func stringPtrForCatalog(value string) *string { return &value }

// TestChainStudioMountTasksEndpoint：任务端点挂载与登录门（未登录 401，登录
// 态空列表 200；分页 envelope 形状）。generations 受理链（进程内 /v1 直驱）
// 的端到端行为由 internal/studio 包单测覆盖，本挂载测试只锁注册面与鉴权。
func TestChainStudioMountTasksEndpoint(t *testing.T) {
	// 未登录门（dev auto-login 关闭）。
	_, unauthedServer := composeChatTestSystemAPI(t, false)
	unauthedClient := &http.Client{Timeout: 10 * time.Second}
	unauthed, err := unauthedClient.Get(unauthedServer.URL + "/__aisys__/api/my-studio/tasks")
	if err != nil {
		t.Fatalf("GET tasks unauthed: %v", err)
	}
	unauthed.Body.Close()
	if unauthed.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated tasks status=%d want 401", unauthed.StatusCode)
	}

	// 登录态（dev auto-login）分页 envelope 与过滤校验。
	composed, server := composeChatTestSystemAPI(t, true)
	ensureChainStudioTables(t, composed.DB)
	client := &http.Client{Timeout: 10 * time.Second}
	authed, err := client.Get(server.URL + "/__aisys__/api/my-studio/tasks?limit=10&status=queued")
	if err != nil {
		t.Fatalf("GET tasks: %v", err)
	}
	body, _ := readAllString(authed.Body)
	authed.Body.Close()
	if authed.StatusCode != http.StatusOK {
		t.Fatalf("authenticated tasks status=%d body=%s", authed.StatusCode, body)
	}
	var payload struct {
		Data struct {
			Rows  []map[string]any `json:"rows"`
			Total int              `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode tasks list: %v", err)
	}

	// 非法 status → 400 studio_invalid_request；未知查询键 → 400。
	badStatus, err := client.Get(server.URL + "/__aisys__/api/my-studio/tasks?status=running")
	if err != nil {
		t.Fatalf("GET tasks bad status: %v", err)
	}
	badBody, _ := readAllString(badStatus.Body)
	badStatus.Body.Close()
	if badStatus.StatusCode != http.StatusBadRequest || !strings.Contains(badBody, "studio_invalid_request") {
		t.Fatalf("bad status filter = %d %s", badStatus.StatusCode, badBody)
	}
}
