package main

// w1_tails_sweep2_test.go：尾部清扫第二批（仍用 w1t helper / TestW1T 入口，
// 与 w1_tails_sweep_test.go 同一命名空间，按主题拆分控制单文件体量）。
// 覆盖范围：
//  1. chain_chat_observation.go：track/Schedule/Wait 与 runObservation 全臂
//     （claim 失败/无行、对象存储未接线、派发失败、读体失败、载荷超限、
//     非 2xx、非法 JSON、空说明、成功落库、提交冲突）。
//  2. chain_accounts.go：selector 各 loader 的查询错误/中毒行扫描错误臂。
//  3. chain_ports.go：localSessionAffinity 过期臂。
//  4. chain_account_locks.go：query_only 连接的 UPDATE 错误臂 + 无主键重复行
//     的 CAS 竞争臂（恢复竞争耗尽 / 结算丢栅栏 / 预约丢栅栏）。
//  5. chain_catalog.go：目录源缺表查询错误与坏数值行的解码错误臂。
//  6. chain_runtime.go：空 Secret / 非法驱动的组合失败臂。
// 明确跳过（不可达或需真实 PG）：见最终报告清单。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// chain_chat_observation.go：内存对象存储 / 可编程执行器 / fixture
// ---------------------------------------------------------------------------

// w1tObsObjectStore 是 chat.ObjectStore 的最小内存实现。
type w1tObsObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newW1tObsObjectStore() *w1tObsObjectStore {
	return &w1tObsObjectStore{objects: map[string][]byte{}}
}

func (s *w1tObsObjectStore) Write(storageKey string, data []byte, _ int64, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[storageKey] = append([]byte(nil), data...)
	return nil
}

func (s *w1tObsObjectStore) Open(storageKey string, _ int64) ([]byte, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[storageKey]
	if !ok {
		return nil, 0, errors.New("w1t 观察对象不存在: " + storageKey)
	}
	return append([]byte(nil), data...), int64(len(data)), nil
}

func (s *w1tObsObjectStore) Delete([]string) error { return nil }

// w1tObsExecutor 是可编程 chat.GenerationExecutor：可阻塞派发并记录请求。
type w1tObsExecutor struct {
	mu       sync.Mutex
	requests []chat.GenerationDispatchRequest
	entered  atomic.Bool
	release  chan struct{}
	handler  func() (*chat.GenerationDispatchResponse, error)
}

func (e *w1tObsExecutor) Dispatch(_ context.Context, req chat.GenerationDispatchRequest) (*chat.GenerationDispatchResponse, error) {
	e.mu.Lock()
	e.requests = append(e.requests, req)
	e.mu.Unlock()
	e.entered.Store(true)
	if e.release != nil {
		<-e.release
	}
	return e.handler()
}

func (e *w1tObsExecutor) lastRequest() chat.GenerationDispatchRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.requests) == 0 {
		return chat.GenerationDispatchRequest{}
	}
	return e.requests[len(e.requests)-1]
}

func w1tObsJSONResponse(status int, body string) *chat.GenerationDispatchResponse {
	return &chat.GenerationDispatchResponse{Status: status, Body: io.NopCloser(strings.NewReader(body))}
}

// w1tFailReader 是 Read 必然失败的响应体。
type w1tFailReader struct{}

func (w1tFailReader) Read([]byte) (int, error) { return 0, errors.New("w1t 响应体读取失败") }
func (w1tFailReader) Close() error             { return nil }

// w1tSeedObservationAsset 写入一条可 claim 的资产行 + user_input 引用行。
func w1tSeedObservationAsset(t *testing.T, db *sql.DB, assetID string, withReference bool) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO chat_assets (id, system_account_id, conversation_id, expires_at, updated_at)
		VALUES (?, 'sys-w1t', 'conv-w1t', '2027-01-01T00:00:00.000Z', '2026-09-01T00:00:00.000Z')`, assetID); err != nil {
		t.Fatalf("seed 资产行(%s): %v", assetID, err)
	}
	if withReference {
		if _, err := db.Exec(`INSERT INTO chat_asset_references (asset_id, conversation_id, turn_id, message_id, reference_kind, expires_at)
			VALUES (?, 'conv-w1t', 'turn-w1t', 'msg-w1t', 'user_input', '2027-01-01T00:00:00.000Z')`, assetID); err != nil {
			t.Fatalf("seed 引用行(%s): %v", assetID, err)
		}
	}
}

// w1tSeedObservationAssetBytes 写入带存储列的资产行 + 引用行，并把字节放入
// 对象存储（processed_bytes/processed_sha256 与实际内容一致）。
func w1tSeedObservationAssetBytes(t *testing.T, db *sql.DB, store *w1tObsObjectStore, assetID, sourceKind string) {
	t.Helper()
	data := []byte("w1t-png-bytes")
	sum := sha256.Sum256(data)
	storageKey := assetID + "-key"
	if _, err := db.Exec(`INSERT INTO chat_assets (id, system_account_id, conversation_id, expires_at, updated_at,
			storage_key, processed_mime_type, processed_bytes, processed_sha256, source_kind)
		VALUES (?, 'sys-w1t', 'conv-w1t', '2027-01-01T00:00:00.000Z', '2026-09-01T00:00:00.000Z',
			?, 'image/png', ?, ?, ?)`, assetID, storageKey, len(data), hex.EncodeToString(sum[:]), sourceKind); err != nil {
		t.Fatalf("seed 带存储资产行(%s): %v", assetID, err)
	}
	if err := store.Write(storageKey, data, int64(len(data)), ""); err != nil {
		t.Fatalf("写对象存储: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO chat_asset_references (asset_id, conversation_id, turn_id, message_id, reference_kind, expires_at)
		VALUES (?, 'conv-w1t', 'turn-w1t', 'msg-w1t', 'user_input', '2027-01-01T00:00:00.000Z')`, assetID); err != nil {
		t.Fatalf("seed 引用行(%s): %v", assetID, err)
	}
}

func w1tObsInput(assetID string) chat.ScheduleObservationInput {
	return chat.ScheduleObservationInput{
		ConversationID:   "conv-w1t",
		SystemAccountID:  "sys-w1t",
		APIKeySecret:     "sk-w1t-obs",
		Model:            "gpt-obs",
		UserContent:      "看这张图",
		AssistantContent: "这是回答",
		Targets: []chat.ObservationTarget{{
			AssetID: assetID, ExpectedTurnID: "turn-w1t", ExpectedMessageID: "msg-w1t",
		}},
	}
}

func w1tObservationOutputBody(summary string) string {
	payload, _ := json.Marshal(map[string]any{"output_text": summary})
	return string(payload)
}

func TestW1TObservationScheduleWaitTrackArms(t *testing.T) {
	// ① track + Wait 命中：waiters 收集后任务表删除该键。
	db := w1yChatAssetsDB(t)
	port := &chatImageObservations{db: db, postgres: false, tasks: map[string][]chan struct{}{}}
	done := make(chan struct{})
	port.track("ast-track", done)
	close(done)
	port.Wait([]string{"ast-track", "ast-missing"}, 1000)
	if _, exists := port.tasks["ast-track"]; exists {
		t.Fatal("Wait 命中后必须删除任务表键")
	}

	// ② 空 waiters：Wait 立即返回（不触发竞速 goroutine）。
	port.Wait(nil, 50)
	port.Wait([]string{"ast-never-tracked"}, 50)

	// ③ Schedule 过滤无效目标 + 去重，仅有效目标进入任务表；goroutine 内
	// runObservation 对不存在的资产行 claim 返回 nil（空表）。
	emptyDB := w1yChatAssetsDB(t)
	scheduler := &chatImageObservations{db: emptyDB, postgres: false, tasks: map[string][]chan struct{}{}}
	scheduler.Schedule(chat.ScheduleObservationInput{
		ConversationID:  "conv-w1t",
		SystemAccountID: "sys-w1t",
		Targets: []chat.ObservationTarget{
			{AssetID: "ast-sched", ExpectedTurnID: "turn-w1t", ExpectedMessageID: "msg-w1t"},
			{AssetID: "", ExpectedTurnID: "turn-w1t", ExpectedMessageID: "msg-w1t"},
			{AssetID: "ast-sched2", ExpectedTurnID: "", ExpectedMessageID: "msg-w1t"},
			{AssetID: "ast-sched2", ExpectedTurnID: "turn-w1t", ExpectedMessageID: ""},
			{AssetID: "ast-sched", ExpectedTurnID: "turn-w1t", ExpectedMessageID: "msg-w1t"},
		},
	})
	scheduler.Wait([]string{"ast-sched", "ast-sched2"}, 2000)
	if left := len(scheduler.tasks["ast-sched"]) + len(scheduler.tasks["ast-sched2"]); left != 0 {
		t.Fatalf("去重后应有 1 个目标且 Wait 后清空，实际残留 %d", left)
	}

	// ④ Wait 超时分支：waiter 永不关闭，按 timeoutMs 上界返回。
	slow := &chatImageObservations{db: emptyDB, postgres: false, tasks: map[string][]chan struct{}{}}
	slow.track("ast-slow", make(chan struct{}))
	started := time.Now()
	slow.Wait([]string{"ast-slow"}, 30)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("超时 Wait 未按 30ms 上界返回: %v", elapsed)
	}
}

func TestW1TObservationRunArms(t *testing.T) {
	ctx := context.Background()

	t.Run("关闭句柄 claim 失败", func(t *testing.T) {
		port := &chatImageObservations{db: w1yOpenClosedSQLite(t), postgres: false, tasks: map[string][]chan struct{}{}}
		if err := port.runObservation(ctx, w1tObsInput("ast-x"), w1tObsInput("ast-x").Targets[0]); err == nil ||
			!strings.Contains(err.Error(), "sql: database is closed") {
			t.Fatalf("关闭句柄 runObservation = %v, want database is closed", err)
		}
	})

	t.Run("无引用行 claim 返回 nil 直接结束", func(t *testing.T) {
		db := w1yChatAssetsDB(t)
		w1tSeedObservationAsset(t, db, "ast-noref", false)
		port := &chatImageObservations{db: db, postgres: false, tasks: map[string][]chan struct{}{}}
		if err := port.runObservation(ctx, w1tObsInput("ast-noref"), w1tObsInput("ast-noref").Targets[0]); err != nil {
			t.Fatalf("无引用行 runObservation = %v, want nil", err)
		}
	})

	t.Run("对象存储未接线 fail 分支", func(t *testing.T) {
		db := w1yChatAssetsDB(t)
		w1tSeedObservationAsset(t, db, "ast-unwired", true)
		port := &chatImageObservations{db: db, postgres: false, tasks: map[string][]chan struct{}{}}
		err := port.runObservation(ctx, w1tObsInput("ast-unwired"), w1tObsInput("ast-unwired").Targets[0])
		if err == nil || !strings.Contains(err.Error(), "chat 资产存储未接线") {
			t.Fatalf("对象存储未接线 runObservation = %v, want 未接线错误", err)
		}
		var status string
		if err := db.QueryRow(`SELECT observation_status FROM chat_assets WHERE id = 'ast-unwired'`).Scan(&status); err != nil {
			t.Fatalf("回读状态: %v", err)
		}
		if status != "failed" {
			t.Fatalf("fail 分支后 observation_status = %q, want failed", status)
		}
	})

	t.Run("派发失败 fail 分支", func(t *testing.T) {
		db := w1yChatAssetsDB(t)
		store := newW1tObsObjectStore()
		w1tSeedObservationAssetBytes(t, db, store, "ast-dispatch-err", "user_uploaded")
		executor := &w1tObsExecutor{handler: func() (*chat.GenerationDispatchResponse, error) {
			return nil, errors.New("w1t 派发失败")
		}}
		port := &chatImageObservations{db: db, postgres: false, tasks: map[string][]chan struct{}{}, objects: store, executor: executor}
		err := port.runObservation(ctx, w1tObsInput("ast-dispatch-err"), w1tObsInput("ast-dispatch-err").Targets[0])
		if err == nil || !strings.Contains(err.Error(), "w1t 派发失败") {
			t.Fatalf("派发失败 runObservation = %v, want 原样错误", err)
		}
		if got := executor.lastRequest().Path; got != "/v1/responses" {
			t.Fatalf("派发 Path = %q, want /v1/responses", got)
		}
	})

	t.Run("响应体读取失败 fail 分支", func(t *testing.T) {
		db := w1yChatAssetsDB(t)
		store := newW1tObsObjectStore()
		w1tSeedObservationAssetBytes(t, db, store, "ast-read-err", "user_uploaded")
		executor := &w1tObsExecutor{handler: func() (*chat.GenerationDispatchResponse, error) {
			return &chat.GenerationDispatchResponse{Status: http.StatusOK, Body: io.ReadCloser(w1tFailReader{})}, nil
		}}
		port := &chatImageObservations{db: db, postgres: false, tasks: map[string][]chan struct{}{}, objects: store, executor: executor}
		if err := port.runObservation(ctx, w1tObsInput("ast-read-err"), w1tObsInput("ast-read-err").Targets[0]); err == nil ||
			!strings.Contains(err.Error(), "w1t 响应体读取失败") {
			t.Fatalf("读体失败 runObservation = %v, want 原样错误", err)
		}
	})

	t.Run("载荷超 128KiB fail 分支", func(t *testing.T) {
		db := w1yChatAssetsDB(t)
		store := newW1tObsObjectStore()
		w1tSeedObservationAssetBytes(t, db, store, "ast-huge", "user_uploaded")
		executor := &w1tObsExecutor{handler: func() (*chat.GenerationDispatchResponse, error) {
			return w1tObsJSONResponse(http.StatusOK, strings.Repeat("x", 128*1024+1)), nil
		}}
		port := &chatImageObservations{db: db, postgres: false, tasks: map[string][]chan struct{}{}, objects: store, executor: executor}
		err := port.runObservation(ctx, w1tObsInput("ast-huge"), w1tObsInput("ast-huge").Targets[0])
		if err == nil || !strings.Contains(err.Error(), "payload_too_large") {
			t.Fatalf("载荷超限 runObservation = %v, want payload_too_large", err)
		}
	})

	t.Run("非 2xx fail 分支", func(t *testing.T) {
		db := w1yChatAssetsDB(t)
		store := newW1tObsObjectStore()
		w1tSeedObservationAssetBytes(t, db, store, "ast-http500", "user_uploaded")
		executor := &w1tObsExecutor{handler: func() (*chat.GenerationDispatchResponse, error) {
			return w1tObsJSONResponse(http.StatusInternalServerError, `{"error":"boom"}`), nil
		}}
		port := &chatImageObservations{db: db, postgres: false, tasks: map[string][]chan struct{}{}, objects: store, executor: executor}
		err := port.runObservation(ctx, w1tObsInput("ast-http500"), w1tObsInput("ast-http500").Targets[0])
		if err == nil || !strings.Contains(err.Error(), "chat_image_observation_http_500") {
			t.Fatalf("非 2xx runObservation = %v, want http_500", err)
		}
	})

	t.Run("响应非 JSON fail 分支", func(t *testing.T) {
		db := w1yChatAssetsDB(t)
		store := newW1tObsObjectStore()
		w1tSeedObservationAssetBytes(t, db, store, "ast-badjson", "user_uploaded")
		executor := &w1tObsExecutor{handler: func() (*chat.GenerationDispatchResponse, error) {
			return w1tObsJSONResponse(http.StatusOK, "not-json"), nil
		}}
		port := &chatImageObservations{db: db, postgres: false, tasks: map[string][]chan struct{}{}, objects: store, executor: executor}
		if err := port.runObservation(ctx, w1tObsInput("ast-badjson"), w1tObsInput("ast-badjson").Targets[0]); err == nil {
			t.Fatal("响应非 JSON 必须失败")
		}
	})

	t.Run("空说明 fail 分支", func(t *testing.T) {
		db := w1yChatAssetsDB(t)
		store := newW1tObsObjectStore()
		w1tSeedObservationAssetBytes(t, db, store, "ast-empty-obs", "user_uploaded")
		executor := &w1tObsExecutor{handler: func() (*chat.GenerationDispatchResponse, error) {
			return w1tObsJSONResponse(http.StatusOK, w1tObservationOutputBody("   ")), nil
		}}
		port := &chatImageObservations{db: db, postgres: false, tasks: map[string][]chan struct{}{}, objects: store, executor: executor}
		err := port.runObservation(ctx, w1tObsInput("ast-empty-obs"), w1tObsInput("ast-empty-obs").Targets[0])
		if err == nil || !strings.Contains(err.Error(), "chat_image_observation_empty") {
			t.Fatalf("空说明 runObservation = %v, want empty", err)
		}
	})

	t.Run("成功落库 ready", func(t *testing.T) {
		db := w1yChatAssetsDB(t)
		store := newW1tObsObjectStore()
		w1tSeedObservationAssetBytes(t, db, store, "ast-ok", "assistant_generated")
		executor := &w1tObsExecutor{handler: func() (*chat.GenerationDispatchResponse, error) {
			return w1tObsJSONResponse(http.StatusOK, w1tObservationOutputBody(`{"summary":"图片说明","ocr":["文字1"],"objects":[],"questionRelevantFacts":["事实"],"uncertainties":[]}`)), nil
		}}
		port := &chatImageObservations{db: db, postgres: false, tasks: map[string][]chan struct{}{}, objects: store, executor: executor}
		if err := port.runObservation(ctx, w1tObsInput("ast-ok"), w1tObsInput("ast-ok").Targets[0]); err != nil {
			t.Fatalf("成功路径 runObservation = %v, want nil", err)
		}
		var status, observationJSON string
		if err := db.QueryRow(`SELECT observation_status, observation_json FROM chat_assets WHERE id = 'ast-ok'`).Scan(&status, &observationJSON); err != nil {
			t.Fatalf("回读观察结果: %v", err)
		}
		if status != "ready" {
			t.Fatalf("成功路径 observation_status = %q, want ready", status)
		}
		if !strings.Contains(observationJSON, "图片说明") || !strings.Contains(observationJSON, "文字1") {
			t.Fatalf("观察 JSON = %q, want 含 summary/ocr 投影", observationJSON)
		}
	})

	t.Run("提交冲突 fail 分支（阻塞派发期间被重claim）", func(t *testing.T) {
		db := w1yChatAssetsDB(t)
		store := newW1tObsObjectStore()
		w1tSeedObservationAssetBytes(t, db, store, "ast-conflict", "user_uploaded")
		release := make(chan struct{})
		executor := &w1tObsExecutor{release: release, handler: func() (*chat.GenerationDispatchResponse, error) {
			return w1tObsJSONResponse(http.StatusOK, w1tObservationOutputBody(`{"summary":"冲突前说明"}`)), nil
		}}
		port := &chatImageObservations{db: db, postgres: false, tasks: map[string][]chan struct{}{}, objects: store, executor: executor}
		type outcome struct {
			err error
		}
		done := make(chan outcome, 1)
		go func() {
			done <- outcome{err: port.runObservation(ctx, w1tObsInput("ast-conflict"), w1tObsInput("ast-conflict").Targets[0])}
		}()
		for !executor.entered.Load() {
			time.Sleep(2 * time.Millisecond)
		}
		// 阻塞期间重 claim：revision 递增 + 换 claim id。
		rival := &chatImageObservations{db: db, postgres: false, tasks: map[string][]chan struct{}{}}
		reClaimed, err := rival.claim(ctx, "ast-conflict", "conv-w1t", "sys-w1t", "turn-w1t", "msg-w1t", time.Now().UTC().Format(chainTimeLayout))
		if err != nil || reClaimed == nil {
			t.Fatalf("重 claim = (%v, %v), want 成功抢占", reClaimed, err)
		}
		close(release)
		result := <-done
		if result.err == nil || !strings.Contains(result.err.Error(), "commit_conflict") {
			t.Fatalf("提交冲突 runObservation = %v, want commit_conflict", result.err)
		}
	})
}

// ---------------------------------------------------------------------------
// chain_accounts.go：selector loader 查询/扫描错误臂
// ---------------------------------------------------------------------------

func TestW1TAccountsSelectorLoaderArms(t *testing.T) {
	ctx := context.Background()
	db := seedChainSelectorDB(t)
	t.Cleanup(func() { _ = db.Close() })
	selector, err := newChainAccountsSelector(db, false, "w1t-secret", time.Now, 20)
	if err != nil {
		t.Fatalf("构造 selector: %v", err)
	}

	t.Run("裸库查询错误臂", func(t *testing.T) {
		bare := w1uOpenPlainSQLite(t, "w1t-selector-bare.sqlite3")
		bareSelector, err := newChainAccountsSelector(bare, false, "w1t-secret", time.Now, 20)
		if err != nil {
			t.Fatalf("构造裸库 selector: %v", err)
		}
		if _, err := bareSelector.loadSupportedModelsByAccountIds(ctx, []string{"acc-x"}); err == nil {
			t.Fatal("缺 account_supported_models 表必须查询失败")
		}
		if _, err := bareSelector.loadFreshQualityRows(ctx, []string{"acc-x"}, "2026-01-01T00:00:00.000Z"); err == nil {
			t.Fatal("缺 account_quality_scores 表必须查询失败")
		}
		if _, err := bareSelector.activeResourceAuthorizationsByIDs(ctx, []string{"auth-x"}, "sys-x"); err == nil {
			t.Fatal("缺 resource_authorizations 表必须查询失败")
		}
	})

	t.Run("中毒行扫描错误臂", func(t *testing.T) {
		mustExec := func(query string, args ...any) {
			t.Helper()
			if _, err := db.Exec(query, args...); err != nil {
				t.Fatalf("seed 中毒行 %q: %v", query, err)
			}
		}
		mustExec(`INSERT INTO account_supported_models (account_id, model, created_at) VALUES ('acc-w1t-p1', NULL, 'now')`)
		if _, err := selector.loadSupportedModelsByAccountIds(ctx, []string{"acc-w1t-p1"}); err == nil {
			t.Fatal("NULL model 必须扫描失败")
		}
		mustExec(`INSERT INTO account_model_mappings (account_id, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled)
			VALUES ('acc-w1t-p2', NULL, 'chat_completions', 'up', 'chat_completions', 1)`)
		if _, err := selector.loadModelMappingsByAccountIds(ctx, []string{"acc-w1t-p2"}); err == nil {
			t.Fatal("NULL source_model 必须扫描失败")
		}
		mustExec(`INSERT INTO account_api_key_runtime_states (account_id, key_fingerprint, key_index, status, cooldown_until, next_probe_at, recovery_started_at)
			VALUES ('acc-w1t-p3', NULL, 0, 'active', NULL, NULL, NULL)`)
		if _, err := selector.loadAPIKeyRuntimeStatesByAccountIds(ctx, []string{"acc-w1t-p3"}); err == nil {
			t.Fatal("NULL key_fingerprint 必须扫描失败")
		}
		mustExec(`INSERT INTO proxy_profiles (id, type, host, port, username, password_encrypted, enabled)
			VALUES ('pp-w1t-p4', 'http', NULL, 8080, NULL, NULL, 1)`)
		if _, err := selector.resolveProxyURLsForProfiles(ctx, []string{"pp-w1t-p4"}); err == nil {
			t.Fatal("NULL host 必须扫描失败")
		}
		mustExec(`INSERT INTO account_quality_scores (account_id, quality_score, quality_state, ewma_first_token_ms, last_sample_at)
			VALUES ('acc-w1t-p5', 'oops', 'bad', 120, '2026-09-14T00:00:00.000Z')`)
		if _, err := selector.loadFreshQualityRows(ctx, []string{"acc-w1t-p5"}, "2026-01-01T00:00:00.000Z"); err == nil {
			t.Fatal("quality_score 非数值必须失败")
		}
		mustExec(`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT,
			resource_owner_system_account_id TEXT, grantee_system_account_id TEXT, status TEXT, expires_at TEXT,
			effective_source_type TEXT, effective_source_team_id TEXT, limits_json TEXT)`)
		mustExec(`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
			grantee_system_account_id, status, expires_at, effective_source_type, effective_source_team_id, limits_json)
			VALUES ('auth-w1t-p9', 'ai_account', NULL, 'sys-owner', 'sys-w1t-g', 'active', NULL, NULL, NULL, NULL)`)
		if _, err := selector.activeResourceAuthorizationsByIDs(ctx, []string{"auth-w1t-p9"}, "sys-w1t-g"); err == nil {
			t.Fatal("NULL resource_id 必须扫描失败")
		}
	})

	t.Run("IncludeUnavailable 正常列表不受影响", func(t *testing.T) {
		credentials, credErr := accounts.EncryptJSON("w1t-secret", accounts.Credentials{"api_key": "sk-w1t-extra", "base_url": "http://127.0.0.1:9"})
		if credErr != nil {
			t.Fatalf("加密凭据: %v", credErr)
		}
		if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, provider_code, name, type, status, schedulable, concurrency_limit, priority, super_priority_enabled, fallback_enabled, config_revision, dispatch_revision, credentials_encrypted, deleted_at)
			VALUES ('acc-w1t-extra', 'sys_admin', 'openai', 'extra', 'api_key', 'active', 1, 0, 0, 0, 0, 1, 1, ?, NULL)`, credentials); err != nil {
			t.Fatalf("seed 额外账户: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO group_accounts VALUES ('acc-w1t-extra', 'sys_admin', 'grp-limit', NULL, 0, 0, 0, 1, '2026-01-01T00:00:00.000Z')`); err != nil {
			t.Fatalf("seed 额外绑定: %v", err)
		}
		result, err := selector.ListOpenAIAccountsForGroupResult(ctx, "grp-limit", "sys_admin",
			gatewayruntimecache.OpenAIAccountsForGroupOptions{IncludeUnavailable: true})
		if err != nil {
			t.Fatalf("IncludeUnavailable 列表: %v", err)
		}
		found := false
		for _, account := range result.Accounts {
			if account.ID == "acc-w1t-extra" {
				found = true
			}
		}
		if !found {
			t.Fatalf("IncludeUnavailable 列表必须包含可水合的新增账户，实际 %d 个", len(result.Accounts))
		}
	})
}

// ---------------------------------------------------------------------------
// chain_ports.go：localSessionAffinity 过期臂
// ---------------------------------------------------------------------------

func TestW1TLocalAffinityExpiredArm(t *testing.T) {
	affinity := newLocalSessionAffinity()
	affinity.mu.Lock()
	affinity.keys["k-expired"] = localAffinityEntry{accountID: "acc-w1t-front"}
	affinity.ttls["k-expired"] = time.Now().Add(-time.Minute)
	affinity.mu.Unlock()
	accounts := []gatewaydispatch.AccountCandidate{{ID: "acc-1"}, {ID: "acc-2"}}
	got, err := affinity.OrderAsync(context.Background(), accounts, "k-expired", gatewaydispatch.AffinityOrderingOptions{})
	if err != nil || len(got) != 2 || got[0].ID != "acc-1" || got[1].ID != "acc-2" {
		t.Fatalf("过期记忆 OrderAsync = (%+v, %v), want 原序不重排", got, err)
	}
}

// ---------------------------------------------------------------------------
// chain_account_locks.go：query_only 写失败臂 + 无主键重复行 CAS 竞争臂
// ---------------------------------------------------------------------------

// w1tLocksOpenReadWrite 写入锁 schema（无唯一约束，允许重复行）。
func w1tLocksOpenReadWrite(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("打开 sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, system_account_id TEXT, provider_code TEXT, name TEXT, type TEXT,
			status TEXT, schedulable INTEGER DEFAULT 1, cooldown_until TEXT, updated_at TEXT, deleted_at TEXT)`,
		`CREATE TABLE account_lock_states (account_id TEXT, enabled INTEGER, lock_state TEXT,
			lock_death_timeout_seconds INTEGER, lock_retry_interval_seconds INTEGER, incident_id TEXT, generation INTEGER,
			incident_started_at TEXT, deadline_at TEXT, original_status TEXT, provenance TEXT, next_retry_at_ms INTEGER,
			lease_id TEXT, lease_until_ms INTEGER, updated_at TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("建锁表: %v", err)
		}
	}
	return db
}

func w1tLocksInsertState(t *testing.T, db *sql.DB, row chainAccountLockRow) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_lock_states (account_id, enabled, lock_state, lock_death_timeout_seconds,
			lock_retry_interval_seconds, incident_id, generation, incident_started_at, deadline_at, original_status,
			provenance, next_retry_at_ms, lease_id, lease_until_ms, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, NULL, ?, ?, ?, '2026-09-14T00:00:00.000Z')`,
		row.accountID, row.enabled, row.lockState, row.deathTimeout, row.retryInterval,
		row.incidentID, row.generation, row.deadlineAt, row.originalStatus,
		row.nextRetryAtMs, row.leaseID, row.leaseUntilMs); err != nil {
		t.Fatalf("seed 锁行(%s): %v", row.accountID, err)
	}
}

func w1tLocksSeedAccount(t *testing.T, db *sql.DB, accountID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, provider_code, name, type, status, schedulable, updated_at, deleted_at)
		VALUES (?, 'sys-w1t', 'openai', 'acc', 'api_key', 'active', 1, '2026-09-14T00:00:00.000Z', NULL)`, accountID); err != nil {
		t.Fatalf("seed 账户(%s): %v", accountID, err)
	}
}

func w1tLocksQueryOnly(t *testing.T, rw *sql.DB) *chainAccountLocks {
	t.Helper()
	rw.SetMaxOpenConns(1)
	if _, err := rw.Exec(`PRAGMA query_only = 1`); err != nil {
		t.Fatalf("设置 query_only: %v", err)
	}
	locks, err := newChainAccountLocks(rw, false, nil)
	if err != nil {
		t.Fatalf("构造锁端口: %v", err)
	}
	locks.now = func() time.Time { return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC) }
	return locks
}

func TestW1TAccountLocksReadOnlyWriteArms(t *testing.T) {
	ctx := context.Background()
	future := "2027-01-01T00:00:00.000Z"
	past := "2026-01-01T00:00:00.000Z"

	build := func(t *testing.T, name string, seed func(t *testing.T, db *sql.DB)) *chainAccountLocks {
		t.Helper()
		db := w1tLocksOpenReadWrite(t, name)
		seed(t, db)
		return w1tLocksQueryOnly(t, db)
	}
	requireWriteErr := func(t *testing.T, name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: query_only 连接上的写操作必须失败", name)
		}
	}

	t.Run("恢复自愈 UPDATE 失败", func(t *testing.T) {
		locks := build(t, "locks-ro-find.sqlite3", func(t *testing.T, db *sql.DB) {
			w1tLocksSeedAccount(t, db, "acc-ro-find")
			w1tLocksInsertState(t, db, chainAccountLockRow{accountID: "acc-ro-find", enabled: 1,
				lockState: "DEAD_CONFIRMED", deathTimeout: 300, retryInterval: 5, generation: 7})
		})
		_, err := locks.FindStateAsync(ctx, "acc-ro-find")
		requireWriteErr(t, "FindStateAsync", err)
	})

	t.Run("RecordFailure UPDATE 失败", func(t *testing.T) {
		locks := build(t, "locks-ro-fail.sqlite3", func(t *testing.T, db *sql.DB) {
			w1tLocksSeedAccount(t, db, "acc-ro-fail")
			w1tLocksInsertState(t, db, chainAccountLockRow{accountID: "acc-ro-fail", enabled: 1,
				lockState: "LOCKED_IDLE", deathTimeout: 300, retryInterval: 5, generation: 3})
		})
		requireWriteErr(t, "RecordFailureAsync", locks.RecordFailureAsync(ctx, "acc-ro-fail", "boom", nil))
	})

	t.Run("结算 UPDATE 失败", func(t *testing.T) {
		locks := build(t, "locks-ro-settle.sqlite3", func(t *testing.T, db *sql.DB) {
			w1tLocksSeedAccount(t, db, "acc-ro-settle")
			w1tLocksInsertState(t, db, chainAccountLockRow{accountID: "acc-ro-settle", enabled: 1,
				lockState: "ENGAGED", deathTimeout: 300, retryInterval: 5, generation: 4,
				incidentID: sql.NullString{String: "acc-ro-settle:4:t", Valid: true},
				deadlineAt: sql.NullString{String: past, Valid: true}})
		})
		requireWriteErr(t, "SettleDeadlineAsync", locks.SettleDeadlineAsync(ctx, "acc-ro-settle", time.Now().UnixMilli(), nil))
	})

	t.Run("预约租约 UPDATE 失败", func(t *testing.T) {
		locks := build(t, "locks-ro-acquire.sqlite3", func(t *testing.T, db *sql.DB) {
			w1tLocksSeedAccount(t, db, "acc-ro-acquire")
			w1tLocksInsertState(t, db, chainAccountLockRow{accountID: "acc-ro-acquire", enabled: 1,
				lockState: "ENGAGED", deathTimeout: 300, retryInterval: 5, generation: 2,
				incidentID: sql.NullString{String: "acc-ro-acquire:2:t", Valid: true},
				deadlineAt: sql.NullString{String: future, Valid: true}})
		})
		if _, err := locks.AcquireRetryLeaseAsync(ctx, "acc-ro-acquire", 3_000); err == nil {
			t.Fatal("query_only 连接上的预约 UPDATE 必须失败")
		}
	})

	t.Run("消费租约 UPDATE 失败", func(t *testing.T) {
		locks := build(t, "locks-ro-consume.sqlite3", func(t *testing.T, db *sql.DB) {
			w1tLocksSeedAccount(t, db, "acc-ro-consume")
			fixed := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
			w1tLocksInsertState(t, db, chainAccountLockRow{accountID: "acc-ro-consume", enabled: 1,
				lockState: "ENGAGED", deathTimeout: 300, retryInterval: 5, generation: 5,
				incidentID:    sql.NullString{String: "acc-ro-consume:5:t", Valid: true},
				deadlineAt:    sql.NullString{String: future, Valid: true},
				nextRetryAtMs: sql.NullInt64{Int64: fixed.Add(-time.Minute).UnixMilli(), Valid: true},
				leaseID:       sql.NullString{String: "lk-w1t", Valid: true},
				leaseUntilMs:  sql.NullInt64{Int64: fixed.Add(time.Hour).UnixMilli(), Valid: true}})
		})
		if _, err := locks.ConsumeRetryLeaseAsync(ctx, "acc-ro-consume", "lk-w1t"); err == nil {
			t.Fatal("query_only 连接上的消费 UPDATE 必须失败")
		}
	})

	t.Run("释放租约 UPDATE 失败", func(t *testing.T) {
		locks := build(t, "locks-ro-release.sqlite3", func(t *testing.T, db *sql.DB) {
			w1tLocksSeedAccount(t, db, "acc-ro-release")
			w1tLocksInsertState(t, db, chainAccountLockRow{accountID: "acc-ro-release", enabled: 1,
				lockState: "ENGAGED", deathTimeout: 300, retryInterval: 5, generation: 6,
				incidentID:   sql.NullString{String: "acc-ro-release:6:t", Valid: true},
				deadlineAt:   sql.NullString{String: future, Valid: true},
				leaseID:      sql.NullString{String: "lk-w1t-r", Valid: true},
				leaseUntilMs: sql.NullInt64{Int64: time.Now().Add(time.Hour).UnixMilli(), Valid: true}})
		})
		_, err := locks.ReleaseRetryLeaseAsync(ctx, gatewaydispatch.ReleaseRetryLeaseInput{
			AccountID: "acc-ro-release", LeaseID: "lk-w1t-r", ScheduleNextRetry: true, GlobalDelayMs: 3_000,
		})
		requireWriteErr(t, "ReleaseRetryLeaseAsync", err)
	})

	t.Run("放弃预约 UPDATE 失败", func(t *testing.T) {
		locks := build(t, "locks-ro-abandon.sqlite3", func(t *testing.T, db *sql.DB) {
			w1tLocksSeedAccount(t, db, "acc-ro-abandon")
		})
		requireWriteErr(t, "AbandonRetryReservationAsync", locks.AbandonRetryReservationAsync(ctx,
			gatewaydispatch.AccountLockRetryLease{AccountID: "acc-ro-abandon", LeaseID: "lk-w1t-a"}))
	})
}

func TestW1TAccountLocksDuplicateRowCASArms(t *testing.T) {
	ctx := context.Background()
	future := "2027-01-01T00:00:00.000Z"
	past := "2026-01-01T00:00:00.000Z"

	newLocks := func(t *testing.T, name string) (*sql.DB, *chainAccountLocks) {
		t.Helper()
		db := w1tLocksOpenReadWrite(t, name)
		locks, err := newChainAccountLocks(db, false, nil)
		if err != nil {
			t.Fatalf("构造锁端口: %v", err)
		}
		locks.now = func() time.Time { return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC) }
		return db, locks
	}

	t.Run("恢复 CAS 双行命中后按恢复态返回", func(t *testing.T) {
		db, locks := newLocks(t, "locks-dup-find.sqlite3")
		w1tLocksSeedAccount(t, db, "acc-dup-find")
		for i := 0; i < 2; i++ {
			w1tLocksInsertState(t, db, chainAccountLockRow{accountID: "acc-dup-find", enabled: 1,
				lockState: "DEAD_CONFIRMED", deathTimeout: 300, retryInterval: 5, generation: 7})
		}
		row, err := locks.findState(ctx, "acc-dup-find")
		if err != nil || row == nil {
			t.Fatalf("恢复 findState = (%v, %v), want 无错误返回", row, err)
		}
		if row.lockState != "LOCKED_IDLE" {
			t.Fatalf("CAS 双命中后返回 lockState = %q, want LOCKED_IDLE（恢复投影）", row.lockState)
		}
	})

	t.Run("结算 CAS 双行命中放弃状态迁移", func(t *testing.T) {
		db, locks := newLocks(t, "locks-dup-settle.sqlite3")
		w1tLocksSeedAccount(t, db, "acc-dup-settle")
		for i := 0; i < 2; i++ {
			w1tLocksInsertState(t, db, chainAccountLockRow{accountID: "acc-dup-settle", enabled: 1,
				lockState: "ENGAGED", deathTimeout: 300, retryInterval: 5, generation: 4,
				incidentID:     sql.NullString{String: "acc-dup-settle:4:t", Valid: true},
				deadlineAt:     sql.NullString{String: past, Valid: true},
				originalStatus: sql.NullString{String: "active", Valid: true}})
		}
		if err := locks.SettleDeadlineAsync(ctx, "acc-dup-settle", time.Now().UnixMilli(),
			&gatewaydispatch.AccountLockObservation{Generation: 4, IncidentID: "acc-dup-settle:4:t"}); err != nil {
			t.Fatalf("双行结算 SettleDeadlineAsync = %v, want nil", err)
		}
		var lockState string
		if err := db.QueryRow(`SELECT lock_state FROM account_lock_states WHERE account_id = 'acc-dup-settle' LIMIT 1`).Scan(&lockState); err != nil {
			t.Fatalf("回读锁态: %v", err)
		}
		if lockState != "ENGAGED" {
			t.Fatalf("双行命中后 lock_state = %q, want 保持 ENGAGED", lockState)
		}
	})

	t.Run("预约 CAS 双行命中拒绝放行", func(t *testing.T) {
		db, locks := newLocks(t, "locks-dup-acquire.sqlite3")
		w1tLocksSeedAccount(t, db, "acc-dup-acquire")
		for i := 0; i < 2; i++ {
			w1tLocksInsertState(t, db, chainAccountLockRow{accountID: "acc-dup-acquire", enabled: 1,
				lockState: "ENGAGED", deathTimeout: 300, retryInterval: 5, generation: 2,
				incidentID:    sql.NullString{String: "acc-dup-acquire:2:t", Valid: true},
				deadlineAt:    sql.NullString{String: future, Valid: true},
				nextRetryAtMs: sql.NullInt64{Int64: time.Now().Add(-time.Minute).UnixMilli(), Valid: true}})
		}
		lease, err := locks.AcquireRetryLeaseAsync(ctx, "acc-dup-acquire", 3_000)
		if err != nil {
			t.Fatalf("双行预约 AcquireRetryLeaseAsync = %v", err)
		}
		if lease.Allowed || lease.WaitMs < 1 || lease.LeaseID != "" {
			t.Fatalf("双行命中 lease = %+v, want 拒绝且无租约 ID", lease)
		}
	})
}

// ---------------------------------------------------------------------------
// chain_catalog.go：目录源缺表 / 坏数值行臂
// ---------------------------------------------------------------------------

func TestW1TCatalogSourceQueryArms(t *testing.T) {
	ctx := context.Background()
	options := gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "gpt", IncludeInactive: true}

	t.Run("内置目录表缺失", func(t *testing.T) {
		source, err := newChainCatalogSource(w1uOpenPlainSQLite(t, "w1t-cat-bare.sqlite3"), false)
		if err != nil {
			t.Fatalf("构造目录源: %v", err)
		}
		if _, err := source.ListProviderModelCatalog(ctx, options); err == nil {
			t.Fatal("缺 provider_model_catalog 表必须查询失败")
		}
	})

	t.Run("自定义目录表缺失", func(t *testing.T) {
		db := w1uOpenSeededBusinessDB(t)
		if _, err := db.Exec(`DROP TABLE custom_provider_models`); err != nil {
			t.Fatalf("删 custom_provider_models: %v", err)
		}
		source, err := newChainCatalogSource(db, false)
		if err != nil {
			t.Fatalf("构造目录源: %v", err)
		}
		if _, err := source.ListProviderModelCatalog(ctx, options); err == nil {
			t.Fatal("缺 custom_provider_models 表必须查询失败")
		}
	})

	t.Run("内置目录坏数值行解码失败", func(t *testing.T) {
		db := w1uOpenSeededBusinessDB(t)
		if _, err := db.Exec(`INSERT INTO provider_model_catalog (id, provider_code, model, status, catalog_visible, context_window_tokens, source, created_at, updated_at)
			VALUES ('w1t-cat-poison', 'gpt', 'w1t-m-poison', 'active', 1, 'not-a-number', 'built_in', '2026-09-14T00:00:00.000Z', '2026-09-14T00:00:00.000Z')`); err != nil {
			t.Fatalf("seed 坏数值目录行: %v", err)
		}
		source, err := newChainCatalogSource(db, false)
		if err != nil {
			t.Fatalf("构造目录源: %v", err)
		}
		if _, err := source.ListProviderModelCatalog(ctx, options); err == nil {
			t.Fatal("context_window_tokens 非数值必须解码失败")
		}
	})

	t.Run("自定义目录坏数值行解码失败", func(t *testing.T) {
		db := w1uOpenSeededBusinessDB(t)
		if _, err := db.Exec(`INSERT INTO custom_provider_models (id, provider_code, model, scope, system_account_id, status, mode, context_window_tokens, created_by, created_at, updated_at)
			VALUES ('w1t-cpm-poison', 'gpt', 'w1t-m-poison', 'global', NULL, 'active', NULL, 'not-a-number', 'w1t', '2026-09-14T00:00:00.000Z', '2026-09-14T00:00:00.000Z')`); err != nil {
			t.Fatalf("seed 坏数值自定义行: %v", err)
		}
		source, err := newChainCatalogSource(db, false)
		if err != nil {
			t.Fatalf("构造目录源: %v", err)
		}
		if _, err := source.ListProviderModelCatalog(ctx, options); err == nil {
			t.Fatal("custom context_window_tokens 非数值必须解码失败")
		}
	})
}

func TestW1TBalanceRefresherEmptySecretArms(t *testing.T) {
	ctx := context.Background()
	db := w1tProxyDB(t, `INSERT INTO proxy_profiles (id, type, host, port) VALUES ('p-w1t-plain', 'socks5', '10.0.0.8', 1080)`)

	// resolveProxyURLEnvelope：空 secret 构建 proxy_url 信封失败上抛。
	if _, err := resolveProxyURLEnvelope(ctx, db, false, "", "p-w1t-plain"); err == nil {
		t.Fatal("空 secret 的代理信封构建必须报错")
	}

	// TestDraft：空 secret 构建 api_key 信封失败上抛（在配置校验之后）。
	refresher := &gatewayManualBalanceRefresher{db: db, pg: false, secret: "", now: time.Now}
	if _, err := refresher.TestDraft(ctx, accounts.BalanceDraftProbeInput{
		Credentials: accounts.Credentials{"api_key": "sk-w1t", "base_url": "http://127.0.0.1:9"},
		Config:      map[string]any{"adapter": "builtin", "intervalMinutes": 5},
	}); err == nil {
		t.Fatal("空 secret 的草稿探测信封构建必须报错")
	}
}

// ---------------------------------------------------------------------------
// chain_runtime.go：组合失败臂补充
// ---------------------------------------------------------------------------

func TestW1TRuntimeComposeSecretAndDriverArms(t *testing.T) {
	t.Run("空 Secret 组合失败", func(t *testing.T) {
		composed := &composition{
			db:      w1uOpenPlainSQLite(t, "w1t-rt-sec-b.sqlite3"),
			statsDB: w1uOpenPlainSQLite(t, "w1t-rt-sec-s.sqlite3"),
		}
		cfg := runtimeConfig{Secret: "", CacheDriver: "memory", RuntimeStateDriver: "memory", DispatchAccountCandidateLimit: 20}
		if _, err := composeChainRuntimeServices(composed, cfg, w1tSettingValue); err == nil {
			t.Fatal("空 Secret 的组合必须失败（会话身份/凭据解封需要 Secret）")
		}
	})

	t.Run("非法 runtime state 驱动组合失败", func(t *testing.T) {
		composed := &composition{
			db:      w1uOpenPlainSQLite(t, "w1t-rt-drv-b.sqlite3"),
			statsDB: w1uOpenPlainSQLite(t, "w1t-rt-drv-s.sqlite3"),
		}
		cfg := runtimeConfig{
			Secret: "w1t-runtime-secret", CacheDriver: "memory", RuntimeStateDriver: "w1t-bogus-driver",
			DispatchAccountCandidateLimit: 20,
		}
		if _, err := composeChainRuntimeServices(composed, cfg, w1tSettingValue); err == nil {
			t.Fatal("非法 runtime state 驱动的组合必须失败")
		}
	})
}
