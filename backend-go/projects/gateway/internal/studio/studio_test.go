package studio

// studio 包单测（TestStudio* 入口，匹配 -run 'Studio|ChainStudio'）：资产上
// 传/查重幂等/配额边界/删除保护/内容 304 与越权 404/列表形状/账户与模型端
// 点形状。fixture 按契约 §3 列名自建 SQLite 表（studio_assets /
// studio_tasks / studio_user_asset_usage）。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

const studioTestOwner = "sys_studio_a"
const studioTestOther = "sys_studio_b"
const studioTestNow = "2026-10-12T08:00:00.000Z"

// fixtureSchema 是契约 §3 的 DDL（列名严格对齐；studio_tasks 覆盖删除引用
// 保护依赖的列集）。
const fixtureSchema = `
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

// memObjectStore 是内存对象存储 stub（记录写入与删除，供断言）。
type memObjectStore struct {
	objects map[string][]byte
	deleted []string
}

func newMemObjectStore() *memObjectStore {
	return &memObjectStore{objects: map[string][]byte{}}
}

func (m *memObjectStore) Write(storageKey string, data []byte, maxBytes int64, expectedSHA256 string) error {
	m.objects[storageKey] = append([]byte(nil), data...)
	return nil
}

func (m *memObjectStore) Open(storageKey string, maxBytes int64) ([]byte, int64, error) {
	data, ok := m.objects[storageKey]
	if !ok {
		return nil, 0, &RouteError{Status: http.StatusNotFound, Code: "object_missing", Message: "对象不存在"}
	}
	return data, int64(len(data)), nil
}

func (m *memObjectStore) Delete(storageKeys []string) error {
	for _, key := range storageKeys {
		delete(m.objects, key)
		m.deleted = append(m.deleted, key)
	}
	return nil
}

type stubAccounts struct {
	rows []StudioAccountOption
}

func (s *stubAccounts) ListStudioAccountOptions(_ context.Context, _ string, _ bool, _ string) ([]StudioAccountOption, error) {
	return s.rows, nil
}

type stubModels struct {
	rows []StudioModel
}

func (s *stubModels) ListDomainModels(_ context.Context, _ string, _ bool, _ string) ([]StudioModel, error) {
	return s.rows, nil
}

type stubKeys struct {
	// calls 原子计数：账户候选面的 EnsureStudioKey 是异步确保（§3.5），测试
	// 主 goroutine 读取时与后台自增并发。
	calls    atomic.Int64
	secrets  map[string]string
	failFind bool
	// domains 覆盖域授权返回（nil = P0 默认四域）。
	domains []string
}

func (s *stubKeys) EnsureStudioKey(_ string) (string, error) {
	s.calls.Add(1)
	return "key_stub", nil
}

func (s *stubKeys) FindStudioKeySecret(keyID, _ string) (string, error) {
	if s.failFind {
		return "", errors.New("studio key missing")
	}
	if s.secrets == nil {
		s.secrets = map[string]string{}
	}
	if secret, ok := s.secrets[keyID]; ok {
		return secret, nil
	}
	secret := "sk-studio-" + keyID
	s.secrets[keyID] = secret
	return secret, nil
}

func (s *stubKeys) StudioKeyDomains(_ string) ([]string, error) {
	if s.domains != nil {
		return append([]string{}, s.domains...), nil
	}
	return append([]string{}, StudioAllDomains...), nil
}

type studioTestEnv struct {
	deps    *Deps
	routes  *studioRoutes
	store   *Store
	objects *memObjectStore
	keys    *stubKeys
	db      *sql.DB
}

func newStudioTestEnv(t *testing.T) *studioTestEnv {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/studio.sqlite3")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// 单连接串行化：后台执行体（受理即结算）与轮询读并发，SQLite 文件锁对并
	// 发写报 SQLITE_BUSY；单连接由 sql 包排队等价于 §3.4 的串行化语义。
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		t.Fatalf("busy timeout: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(fixtureSchema); err != nil {
		t.Fatalf("apply fixture schema: %v", err)
	}
	objects := newMemObjectStore()
	keys := &stubKeys{}
	deps := &Deps{
		Store:          NewStore(db, Dialect{}),
		ObjectStore:    objects,
		AccountOptions: &stubAccounts{},
		Models:         &stubModels{},
		Keys:           keys,
		Now: func() time.Time {
			return time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
		},
	}
	return &studioTestEnv{
		deps:    deps,
		routes:  &studioRoutes{deps: deps},
		store:   deps.Store,
		objects: objects,
		keys:    keys,
		db:      db,
	}
}

func studioAuthed(r *http.Request, owner string) *http.Request {
	return r.WithContext(authsys.WithAuthContext(r.Context(), &authsys.AuthContext{SystemAccountID: owner, Role: "user"}))
}

func studioAuthedAdmin(r *http.Request, owner string) *http.Request {
	return r.WithContext(authsys.WithAuthContext(r.Context(), &authsys.AuthContext{SystemAccountID: owner, Role: "admin"}))
}

func studioPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

func studioUploadRequest(t *testing.T, filename, contentType string, data []byte, fields map[string]string) *http.Request {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if data != nil {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
		header.Set("Content-Type", contentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/assets", &buffer)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func studioInvokeUpload(rt *studioRoutes, r *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	rt.uploadAsset(recorder, studioAuthed(r, studioTestOwner))
	return recorder
}

func studioErrorPayload(t *testing.T, recorder *httptest.ResponseRecorder) (message, code string) {
	t.Helper()
	var payload struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload: %v body=%s", err, recorder.Body.String())
	}
	return payload.Message, payload.Code
}

func studioUsageOf(t *testing.T, db *sql.DB, owner string) (bytes_, count int64) {
	t.Helper()
	err := db.QueryRow(`SELECT asset_bytes, asset_count FROM studio_user_asset_usage WHERE system_account_id = ?`, owner).Scan(&bytes_, &count)
	if err == sql.ErrNoRows {
		return 0, 0
	}
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	return bytes_, count
}

// --- 上传与幂等查重 ---

func TestStudioUploadAssetCreatesReadyAsset(t *testing.T) {
	env := newStudioTestEnv(t)
	data := studioPNG(t, 3, 2)
	recorder := studioInvokeUpload(env.routes, studioUploadRequest(t, "a.png", "image/png", data, map[string]string{
		"paramsJson": `{"source":"unit-test"}`,
	}))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data assetDTO `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode asset: %v", err)
	}
	asset := payload.Data
	if !strings.HasPrefix(asset.ID, "stasset_") {
		t.Errorf("asset id = %q, want stasset_ prefix", asset.ID)
	}
	if asset.Kind != "image" || asset.Mime != "image/png" {
		t.Errorf("kind/mime = %q/%q", asset.Kind, asset.Mime)
	}
	if asset.Bytes != int64(len(data)) {
		t.Errorf("bytes = %d, want %d", asset.Bytes, len(data))
	}
	if asset.Width == nil || *asset.Width != 3 || asset.Height == nil || *asset.Height != 2 {
		t.Errorf("dimensions = %v/%v, want 3/2", asset.Width, asset.Height)
	}
	if asset.Params["source"] != "unit-test" {
		t.Errorf("params = %+v", asset.Params)
	}
	if asset.SourceTaskID != nil {
		t.Errorf("sourceTaskId = %v, want nil (upload)", asset.SourceTaskID)
	}
	// 对象已写、账本入账。
	if len(env.objects.objects) != 1 {
		t.Fatalf("objects = %d, want 1", len(env.objects.objects))
	}
	if bytes_, count := studioUsageOf(t, env.db, studioTestOwner); bytes_ != int64(len(data)) || count != 1 {
		t.Fatalf("usage = %d/%d, want %d/1", bytes_, count, len(data))
	}
}

func TestStudioUploadDeduplicatesBySha256PerUser(t *testing.T) {
	env := newStudioTestEnv(t)
	data := studioPNG(t, 2, 2)
	first := studioInvokeUpload(env.routes, studioUploadRequest(t, "a.png", "image/png", data, nil))
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d", first.Code)
	}
	second := studioInvokeUpload(env.routes, studioUploadRequest(t, "b.png", "image/png", data, nil))
	if second.Code != http.StatusOK {
		t.Fatalf("replay status = %d body=%s, want 200 idempotent", second.Code, second.Body.String())
	}
	var payload struct {
		Data assetDTO `json:"data"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	var firstPayload struct {
		Data assetDTO `json:"data"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &firstPayload)
	if payload.Data.ID != firstPayload.Data.ID {
		t.Fatalf("replay id = %q, want original %q", payload.Data.ID, firstPayload.Data.ID)
	}
	var count int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM studio_assets WHERE system_account_id = ?`, studioTestOwner).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("asset rows = %d, want 1（同用户 sha256 查重复用既有资产）", count)
	}
	if _, count := studioUsageOf(t, env.db, studioTestOwner); count != 1 {
		t.Fatalf("usage count = %d, want 1（重复上传不重复占额）", count)
	}
	// 跨用户同 hash 不构成匹配：另一用户上传同内容应新建行。
	other := httptest.NewRecorder()
	env.routes.uploadAsset(other, studioAuthed(studioUploadRequest(t, "c.png", "image/png", data, nil), studioTestOther))
	if other.Code != http.StatusCreated {
		t.Fatalf("other-user upload status = %d body=%s", other.Code, other.Body.String())
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM studio_assets`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("asset rows after cross-user upload = %d, want 2", count)
	}
}

func TestStudioUploadValidation(t *testing.T) {
	env := newStudioTestEnv(t)
	data := studioPNG(t, 1, 1)

	cases := []struct {
		name     string
		request  *http.Request
		wantCode string
	}{
		{"非 multipart", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/assets", strings.NewReader("{}"))
			r.Header.Set("Content-Type", "application/json")
			return r
		}(), CodeInvalidRequest},
		{"缺 file 字段", studioUploadRequest(t, "", "", nil, map[string]string{"paramsJson": "{}"}), CodeInvalidRequest},
		{"MIME 不符", studioUploadRequest(t, "a.gif", "image/gif", data, nil), CodeInvalidRequest},
		{"paramsJson 非对象", studioUploadRequest(t, "a.png", "image/png", data, map[string]string{"paramsJson": `["x"]`}), CodeInvalidRequest},
		{"额外表单字段", studioUploadRequest(t, "a.png", "image/png", data, map[string]string{"note": "x"}), CodeInvalidRequest},
	}
	for _, testCase := range cases {
		recorder := studioInvokeUpload(env.routes, testCase.request)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d body=%s, want 400", testCase.name, recorder.Code, recorder.Body.String())
			continue
		}
		if _, code := studioErrorPayload(t, recorder); code != testCase.wantCode {
			t.Errorf("%s: code = %q, want %q", testCase.name, code, testCase.wantCode)
		}
	}
	// 上传失败不残留对象、不占额。
	if len(env.objects.objects) != 0 {
		t.Errorf("objects = %d, want 0（失败路径不残留对象）", len(env.objects.objects))
	}
	if bytes_, count := studioUsageOf(t, env.db, studioTestOwner); bytes_ != 0 || count != 0 {
		t.Errorf("usage = %d/%d, want 0/0", bytes_, count)
	}
}

// --- 配额边界 ---

func TestStudioQuotaBoundaries(t *testing.T) {
	env := newStudioTestEnv(t)
	// 容量边界：账本已接近 20GiB 上限 → 413 studio_asset_quota_exceeded。
	if _, err := env.db.Exec(`INSERT INTO studio_user_asset_usage (system_account_id, asset_bytes, asset_count, updated_at)
		VALUES (?, ?, 1, ?)`, studioTestOwner, StudioAssetUserMaxBytes-int64(8), studioTestNow); err != nil {
		t.Fatal(err)
	}
	recorder := studioInvokeUpload(env.routes, studioUploadRequest(t, "a.png", "image/png", studioPNG(t, 8, 8), nil))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("bytes quota status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, code := studioErrorPayload(t, recorder); code != CodeAssetQuotaExceeded {
		t.Fatalf("bytes quota code = %q", code)
	}
	// 个数边界：账本 count 已达 2000 → 413 studio_asset_count_exceeded。
	if _, err := env.db.Exec(`UPDATE studio_user_asset_usage SET asset_bytes = 0, asset_count = ? WHERE system_account_id = ?`, StudioAssetUserMaxCount, studioTestOwner); err != nil {
		t.Fatal(err)
	}
	recorder = studioInvokeUpload(env.routes, studioUploadRequest(t, "a.png", "image/png", studioPNG(t, 8, 8), nil))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("count quota status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, code := studioErrorPayload(t, recorder); code != CodeAssetCountExceeded {
		t.Fatalf("count quota code = %q", code)
	}
	// 配额失败不写对象。
	if len(env.objects.objects) != 0 {
		t.Fatalf("objects = %d, want 0（配额拒绝不残留对象）", len(env.objects.objects))
	}
	// 预检直接拒绝（AssertQuotaAvailable 语义与事务内一致）。
	if err := env.store.AssertQuotaAvailable(studioTestOwner, 1); err == nil {
		t.Fatalf("AssertQuotaAvailable at max count = nil, want error")
	}
}

// --- 列表 ---

func TestStudioListAssets(t *testing.T) {
	env := newStudioTestEnv(t)
	for index, kind := range []string{"image", "audio", "video", "image"} {
		if _, err := env.store.CreateAsset(CreateAssetInput{
			SystemAccountID: studioTestOwner,
			Kind:            kind,
			Mime:            "application/octet-stream",
			StorageKey:      studioStorageKey(NewAssetID(), strings.Repeat(string(rune('a'+index)), 64), "image/png"),
			Bytes:           int64(index + 1),
			Sha256:          strings.Repeat(string(rune('a'+index)), 64),
			Now:             studioTestNow,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 其他用户的行不进入本用户的列表与 total。
	if _, err := env.store.CreateAsset(CreateAssetInput{
		SystemAccountID: studioTestOther,
		Kind:            "image",
		Mime:            "image/png",
		StorageKey:      "other/key",
		Bytes:           1,
		Sha256:          strings.Repeat("z", 64),
		Now:             studioTestNow,
	}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/assets?limit=2&offset=0", nil)
	recorder := httptest.NewRecorder()
	env.routes.listAssets(recorder, studioAuthed(request, studioTestOwner))
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Rows  []assetDTO `json:"rows"`
			Total int        `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(payload.Data.Rows) != 2 || payload.Data.Total != 4 {
		t.Fatalf("rows/total = %d/%d, want 2/4", len(payload.Data.Rows), payload.Data.Total)
	}

	// kind 过滤 + limit 上限 100 收敛 + offset。
	request = httptest.NewRequest(http.MethodGet, "/assets?kind=image&limit=999&offset=1", nil)
	recorder = httptest.NewRecorder()
	env.routes.listAssets(recorder, studioAuthed(request, studioTestOwner))
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Total != 2 || len(payload.Data.Rows) != 1 {
		t.Fatalf("kind filter rows/total = %d/%d, want 1/2", len(payload.Data.Rows), payload.Data.Total)
	}

	// 未知 kind → 400；未知查询键 → 400。
	request = httptest.NewRequest(http.MethodGet, "/assets?kind=model", nil)
	recorder = httptest.NewRecorder()
	env.routes.listAssets(recorder, studioAuthed(request, studioTestOwner))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad kind status = %d", recorder.Code)
	}
	if _, code := studioErrorPayload(t, recorder); code != CodeInvalidRequest {
		t.Fatalf("bad kind code = %q", code)
	}
	request = httptest.NewRequest(http.MethodGet, "/assets?cursor=x", nil)
	recorder = httptest.NewRecorder()
	env.routes.listAssets(recorder, studioAuthed(request, studioTestOwner))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown key status = %d", recorder.Code)
	}
}

// --- 删除（引用保护 + 配额回落 + 对象回收） ---

func TestStudioDeleteAssetLifecycle(t *testing.T) {
	env := newStudioTestEnv(t)
	asset, err := env.store.CreateAsset(CreateAssetInput{
		SystemAccountID: studioTestOwner,
		Kind:            "image",
		Mime:            "image/png",
		StorageKey:      "ab/cd/asset-1",
		Bytes:           128,
		Sha256:          strings.Repeat("a", 64),
		Now:             studioTestNow,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 引用保护：pending_settled 任务引用 → 409 studio_asset_referenced。
	if _, err := env.db.Exec(`INSERT INTO studio_tasks (id, system_account_id, client_operation_id, kind, status, settlement, asset_id, created_at, updated_at)
		VALUES ('stask_ref', ?, 'op-ref', 'image', 'completed', 'pending_settled', ?, ?, ?)`,
		studioTestOwner, asset.ID, studioTestNow, studioTestNow); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/assets/"+asset.ID, nil)
	request.SetPathValue("assetId", asset.ID)
	recorder := httptest.NewRecorder()
	env.routes.deleteAsset(recorder, studioAuthed(request, studioTestOwner))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("referenced delete status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, code := studioErrorPayload(t, recorder); code != CodeAssetReferenced {
		t.Fatalf("referenced code = %q", code)
	}

	// 引用解除（settlement 终态）后删除成功：204 + 行删 + 账本回落 + 对象回收。
	if _, err := env.db.Exec(`UPDATE studio_tasks SET settlement = 'settled' WHERE id = 'stask_ref'`); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodDelete, "/assets/"+asset.ID, nil)
	request.SetPathValue("assetId", asset.ID)
	recorder = httptest.NewRecorder()
	env.routes.deleteAsset(recorder, studioAuthed(request, studioTestOwner))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s, want 204", recorder.Code, recorder.Body.String())
	}
	var count int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM studio_assets WHERE id = ?`, asset.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("asset row still present")
	}
	if bytes_, assetCount := studioUsageOf(t, env.db, studioTestOwner); bytes_ != 0 || assetCount != 0 {
		t.Fatalf("usage after delete = %d/%d, want 0/0", bytes_, assetCount)
	}
	if len(env.objects.deleted) != 1 || env.objects.deleted[0] != asset.StorageKey {
		t.Fatalf("deleted objects = %v, want [%s]", env.objects.deleted, asset.StorageKey)
	}

	// 重复删除 → 404 同型。
	request = httptest.NewRequest(http.MethodDelete, "/assets/"+asset.ID, nil)
	request.SetPathValue("assetId", asset.ID)
	recorder = httptest.NewRecorder()
	env.routes.deleteAsset(recorder, studioAuthed(request, studioTestOwner))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("repeat delete status = %d", recorder.Code)
	}
	if _, code := studioErrorPayload(t, recorder); code != CodeAssetNotFound {
		t.Fatalf("repeat delete code = %q", code)
	}
}

// --- 内容端点（ETag/304/归属 404/preview 拒绝/下载头） ---

func TestStudioAssetContent(t *testing.T) {
	env := newStudioTestEnv(t)
	data := studioPNG(t, 4, 4)
	recorder := studioInvokeUpload(env.routes, studioUploadRequest(t, "a.png", "image/png", data, nil))
	var payload struct {
		Data assetDTO `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	assetID := payload.Data.ID
	etag := `"` + payload.Data.Sha256 + `"`

	contentRequest := func(owner string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/assets/"+assetID+"/content", nil)
		request.SetPathValue("assetId", assetID)
		result := httptest.NewRecorder()
		env.routes.assetContent(result, studioAuthed(request, owner))
		return result
	}

	// 200 + 内联头 + nosniff。
	result := contentRequest(studioTestOwner)
	if result.Code != http.StatusOK {
		t.Fatalf("content status = %d", result.Code)
	}
	if got := result.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := result.Header().Get("Content-Disposition"); got != "inline" {
		t.Errorf("Content-Disposition = %q, want inline", got)
	}
	if got := result.Header().Get("ETag"); got != etag {
		t.Errorf("ETag = %q, want %q", got, etag)
	}
	if got := result.Header().Get("Cache-Control"); got != "private, max-age=86400, immutable" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := result.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if !bytes.Equal(result.Body.Bytes(), data) {
		t.Errorf("content body mismatch")
	}

	// If-None-Match 命中 → 304（不读对象：读前清空对象缓存仍应 304）。
	request := httptest.NewRequest(http.MethodGet, "/assets/"+assetID+"/content", nil)
	request.SetPathValue("assetId", assetID)
	request.Header.Set("If-None-Match", etag)
	env.objects.objects = map[string][]byte{}
	result = httptest.NewRecorder()
	env.routes.assetContent(result, studioAuthed(request, studioTestOwner))
	if result.Code != http.StatusNotModified {
		t.Fatalf("304 status = %d, want 304", result.Code)
	}
	// 恢复对象（304 用例刻意清空，后续分支需要真实对象）。
	env.objects.objects[studioStorageKey(assetID, payload.Data.Sha256, "image/png")] = data

	// 越权读取 → 404 同型（不泄露存在性）。
	result = contentRequest(studioTestOther)
	if result.Code != http.StatusNotFound {
		t.Fatalf("cross-user content status = %d, want 404", result.Code)
	}
	if _, code := studioErrorPayload(t, result); code != CodeAssetNotFound {
		t.Fatalf("cross-user code = %q", code)
	}

	// preview 变体 P0 未开放 → 400 studio_asset_preview_unavailable。
	request = httptest.NewRequest(http.MethodGet, "/assets/"+assetID+"/content?variant=preview", nil)
	request.SetPathValue("assetId", assetID)
	result = httptest.NewRecorder()
	env.routes.assetContent(result, studioAuthed(request, studioTestOwner))
	if result.Code != http.StatusBadRequest {
		t.Fatalf("preview status = %d", result.Code)
	}
	if _, code := studioErrorPayload(t, result); code != CodeAssetPreviewUnavailable {
		t.Fatalf("preview code = %q", code)
	}

	// download=1 → attachment。
	request = httptest.NewRequest(http.MethodGet, "/assets/"+assetID+"/content?download=1", nil)
	request.SetPathValue("assetId", assetID)
	result = httptest.NewRecorder()
	var dbStorageKey string
	if err := env.db.QueryRow(`SELECT storage_key FROM studio_assets WHERE id = ?`, assetID).Scan(&dbStorageKey); err != nil {
		t.Fatal(err)
	}
	if _, _, openErr := env.objects.Open(dbStorageKey, studioAssetMaxBytes); openErr != nil {
		t.Fatalf("direct open %q failed: %v; keys=%v", dbStorageKey, openErr, env.objects.objects)
	}
	env.routes.assetContent(result, studioAuthed(request, studioTestOwner))
	if result.Code != http.StatusOK {
		t.Fatalf("download status = %d body=%s objects=%v", result.Code, result.Body.String(), env.objects.objects)
	}
	if got := result.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Errorf("download Content-Disposition = %q, want attachment", got)
	}
}

// --- 账户候选与域模型目录端点 ---

func TestStudioAccountsEndpoint(t *testing.T) {
	env := newStudioTestEnv(t)
	accounts := &stubAccounts{rows: []StudioAccountOption{
		{AccountID: "acc_1", Name: "图像号", ProviderCode: "openai", Domain: "image", Status: "active"},
		{AccountID: "acc_2", Name: "音频号", ProviderCode: "volcengine", Domain: "audio", Status: "pending_test"},
	}}
	env.deps.AccountOptions = accounts

	request := httptest.NewRequest(http.MethodGet, "/accounts", nil)
	recorder := httptest.NewRecorder()
	env.routes.listAccounts(recorder, studioAuthedAdmin(request, studioTestOwner))
	if recorder.Code != http.StatusOK {
		t.Fatalf("accounts status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Rows  []StudioAccountOption `json:"rows"`
			Total int                   `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Total != 2 || len(payload.Data.Rows) != 2 {
		t.Fatalf("accounts rows/total = %d/%d", len(payload.Data.Rows), payload.Data.Total)
	}
	if payload.Data.Rows[0].AccountID != "acc_1" || payload.Data.Rows[0].Domain != "image" {
		t.Fatalf("row = %+v", payload.Data.Rows[0])
	}
	// 账户候选调用触发 studio Key 异步确保（§3.5 触发时机）。
	deadline := time.Now().Add(2 * time.Second)
	for env.keys.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if env.keys.calls.Load() == 0 {
		t.Fatalf("EnsureStudioKey 未被账户候选调用触发")
	}

	// 非法 domain → 400 studio_domain_invalid。
	request = httptest.NewRequest(http.MethodGet, "/accounts?domain=images", nil)
	recorder = httptest.NewRecorder()
	env.routes.listAccounts(recorder, studioAuthed(request, studioTestOwner))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad domain status = %d", recorder.Code)
	}
	if _, code := studioErrorPayload(t, recorder); code != CodeDomainInvalid {
		t.Fatalf("bad domain code = %q", code)
	}

	// 未登录 → 401。
	request = httptest.NewRequest(http.MethodGet, "/accounts", nil)
	recorder = httptest.NewRecorder()
	env.routes.listAccounts(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed status = %d", recorder.Code)
	}
}

func TestStudioModelsEndpoint(t *testing.T) {
	env := newStudioTestEnv(t)
	models := &stubModels{rows: []StudioModel{
		{Model: "gpt-image-1", ProviderCode: "openai", Domain: "image", EndpointModes: []string{"images_json"}, Capabilities: map[string]any{}},
	}}
	env.deps.Models = models

	request := httptest.NewRequest(http.MethodGet, "/models?domain=image", nil)
	recorder := httptest.NewRecorder()
	env.routes.listModels(recorder, studioAuthed(request, studioTestOwner))
	if recorder.Code != http.StatusOK {
		t.Fatalf("models status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Rows  []StudioModel `json:"rows"`
			Total int           `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Total != 1 || payload.Data.Rows[0].Model != "gpt-image-1" {
		t.Fatalf("models rows = %+v", payload.Data)
	}

	// domain 缺省 / text 域 / 非法值 → 400 studio_domain_invalid。
	for _, query := range []string{"/models", "/models?domain=text", "/models?domain=banana"} {
		request = httptest.NewRequest(http.MethodGet, query, nil)
		recorder = httptest.NewRecorder()
		env.routes.listModels(recorder, studioAuthed(request, studioTestOwner))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", query, recorder.Code)
		}
		if _, code := studioErrorPayload(t, recorder); code != CodeDomainInvalid {
			t.Fatalf("%s code = %q", query, code)
		}
	}
}

// --- 域授权词表（studio_domains_json） ---

func TestStudioDomainsJSONRoundTrip(t *testing.T) {
	domains, err := ParseStudioDomains(StudioAllDomainsJSON)
	if err != nil {
		t.Fatalf("parse all-domains: %v", err)
	}
	if len(domains) != 4 {
		t.Fatalf("domains = %v, want four domains", domains)
	}
	if empty, err := ParseStudioDomains(""); err != nil || len(empty) != 4 {
		t.Fatalf("empty parse = %v, %v, want four-domain default", empty, err)
	}
	if _, err := ParseStudioDomains("{bad"); err == nil {
		t.Fatalf("bad json = nil error, want error")
	}
}
