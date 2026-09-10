package main

// openaicompat 404 JSON 契约全部进程内驱动，不依赖真实上游。

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

func TestW1ChatGatewayExecutorDispatch(t *testing.T) {
	// 未接线错误。
	executor := newChatGatewayExecutor(nil)
	if _, err := executor.Dispatch(context.Background(), chat.GenerationDispatchRequest{}); err == nil {
		t.Fatal("nil chain 必须报未接线错误")
	}
	// 正常流：path 归一化 + header 透传 + body 回读。
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("authorization") != "Bearer k" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	chainExecutor := newChatGatewayExecutor(inner)
	response, err := chainExecutor.Dispatch(context.Background(), chat.GenerationDispatchRequest{
		Path:    "v1/chat/completions",
		Method:  "POST",
		Headers: map[string]string{"authorization": "Bearer k"},
		Body:    []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if response.Status != http.StatusTeapot {
		t.Fatalf("status = %d", response.Status)
	}
	if response.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("content-type = %q", response.Header.Get("Content-Type"))
	}
	body := make([]byte, 64)
	n, _ := response.Body.Read(body)
	if !strings.Contains(string(body[:n]), "ok") {
		t.Fatalf("body = %q", string(body[:n]))
	}
	_ = response.Body.Close()
	// 空 path 默认 /v1/chat/completions。
	defaultPathExecutor := newChatGatewayExecutor(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	if _, err := defaultPathExecutor.Dispatch(context.Background(), chat.GenerationDispatchRequest{}); err != nil {
		t.Fatalf("默认 path dispatch: %v", err)
	}
	// ctx 取消：Dispatch 返回 ctx 错误。
	blocking := newChatGatewayExecutor(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := blocking.Dispatch(ctx, chat.GenerationDispatchRequest{Path: "/v1/chat/completions"}); err == nil {
		t.Fatal("已取消 ctx 必须返回错误")
	}
}

func TestW1ChatPipeWriterHeaderIdempotent(t *testing.T) {
	reader, writer := io.Pipe()
	target := newChatPipeWriter(writer)
	target.WriteHeader(http.StatusTeapot)
	target.WriteHeader(http.StatusOK) // 二次提交被忽略
	if target.status != http.StatusTeapot {
		t.Fatalf("status = %d，want 418", target.status)
	}
	select {
	case <-target.headerCh:
	default:
		t.Fatal("headerCh 未关闭")
	}
	// io.Pipe 同步语义：写入在读取方消费前阻塞，放后台 goroutine 完成写入与关闭。
	go func() {
		_, _ = target.Write([]byte("data"))
		target.finish()
	}()
	content, err := io.ReadAll(reader)
	if err != nil || string(content) != "data" {
		t.Fatalf("pipe 数据 = %q, %v", string(content), err)
	}
}

func TestW1ChatTokenCount(t *testing.T) {
	count, err := newChatTokenCount()
	if err != nil {
		t.Fatalf("newChatTokenCount: %v", err)
	}
	if count("你好") <= 0 {
		t.Fatalf("token 计数 = %d，必须为正", count("你好"))
	}
}

func TestW1ErrChainCompatAndBearer(t *testing.T) {
	err := errChainCompat("组合缺少句柄")
	if err.Error() != "组合缺少句柄" {
		t.Fatalf("Error() = %q", err.Error())
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	if got := bearerTokenOf(request); got != "" {
		t.Fatalf("无 header = %q", got)
	}
	request.Header.Set("authorization", "Bearer  sk-key ")
	if got := bearerTokenOf(request); got != "sk-key" {
		t.Fatalf("bearer = %q", got)
	}
	request.Header.Set("authorization", "Basic abc")
	if got := bearerTokenOf(request); got != "" {
		t.Fatalf("非 bearer = %q", got)
	}
	request.Header.Set("authorization", "BearerOnly")
	if got := bearerTokenOf(request); got != "" {
		t.Fatalf("无空格 bearer = %q", got)
	}
}

func TestW1ChainCompatDispatcher404Contract(t *testing.T) {
	mux := &chainCompatMux{mux: http.NewServeMux()}
	mux.Register("GET /v1/files", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[]}`))
	}))
	dispatcher := &chainCompatDispatcher{mux: mux}
	if !dispatcher.matches("/v1/files") || !dispatcher.matches("/v1/files/x") || dispatcher.matches("/v1/filesystem") || dispatcher.matches("/v1/other") {
		t.Fatal("matches 前缀判断错误")
	}
	recorder := httptest.NewRecorder()
	dispatcher.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/files", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"files":[]}` {
		t.Fatalf("命中路由 = %d %q", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	dispatcher.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/v1/files", nil))
	if recorder.Code != http.StatusNotFound || recorder.Body.String() != `{"message":"资源不存在"}` {
		t.Fatalf("未匹配 = %d %q", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	dispatcher.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/other", nil))
	if recorder.Code != http.StatusNotFound || recorder.Body.String() != `{"message":"资源不存在"}` {
		t.Fatalf("非族路径 = %d %q", recorder.Code, recorder.Body.String())
	}
	var nilDispatcher *chainCompatDispatcher
	recorder = httptest.NewRecorder()
	nilDispatcher.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/files", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("nil dispatcher = %d", recorder.Code)
	}
}

func TestW1MountChainOpenAICompatFamiliesFailFast(t *testing.T) {
	if err := mountChainOpenAICompatFamilies(nil, nil, runtimeConfig{}, nil); err == nil {
		t.Fatal("nil composition 必须报错")
	}
	if err := mountChainOpenAICompatFamilies(&composition{}, nil, runtimeConfig{}, nil); err == nil {
		t.Fatal("缺业务库句柄必须报错")
	}
}
func TestW1ChatAssetObjectStoreLifecycle(t *testing.T) {
	if _, err := newChatAssetObjectStore("  "); err == nil {
		t.Fatal("空 root 必须报错")
	}
	root := filepath.Join(t.TempDir(), "assets")
	store, err := newChatAssetObjectStore(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	// 行为存疑无：穿越键经 filepath.Clean("/"+key) 从根消解，映射到根内而不越界。
	escaped, err := store.path("../escape")
	if err != nil {
		t.Fatalf("穿越键归一化报错: %v", err)
	}
	if !strings.HasPrefix(escaped, root) || strings.Contains(escaped, "..") {
		t.Fatalf("穿越键映射越界: %q", escaped)
	}
	data := []byte("图片字节")
	if err := store.Write("conv/a.png", data, 100, strings.ToUpper(chatAssetSHA256Hex(data))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := store.Write("conv/a.png", data, 2, ""); err == nil {
		t.Fatal("超限写入必须报错")
	}
	if err := store.Write("conv/a.png", data, 100, "deadbeef"); err == nil {
		t.Fatal("sha 不匹配必须报错")
	}
	got, size, err := store.Open("conv/a.png", 100)
	if err != nil || string(got) != "图片字节" || size != int64(len(data)) {
		t.Fatalf("open = %q, %d, %v", got, size, err)
	}
	if _, _, err := store.Open("conv/a.png", 2); err == nil {
		t.Fatal("超限读取必须报错")
	}
	if _, _, err := store.Open("conv/missing.png", 100); err == nil {
		t.Fatal("缺失读取必须报错")
	}
	// 删除存在的键；缺失/空键静默通过（Node remove 语义）。
	if err := store.Delete([]string{"", "conv/missing.png", "conv/a.png"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := store.Open("conv/a.png", 100); err == nil {
		t.Fatal("删除后读取必须报错")
	}
}

// w1ChatKeysFixture 建 chat API key 生命周期所需的最小业务表。
type w1ChatKeysFixture struct {
	db         *sql.DB
	provider   *chatAPIKeyProvider
	secret     string
	ownerID    string
	groupID    string
	strategyID string
}

func newW1ChatKeysFixture(t *testing.T) *w1ChatKeysFixture {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "keys.sqlite3"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT, name TEXT, provider_code TEXT, enabled INTEGER, is_default INTEGER, created_at TEXT)`,
		`CREATE TABLE route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT, name TEXT, description TEXT, mode TEXT, status TEXT, is_default INTEGER, config_json TEXT, created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT, system_account_id TEXT, group_id TEXT, priority INTEGER, weight INTEGER, status TEXT, created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE api_keys (id TEXT PRIMARY KEY, system_account_id TEXT, route_strategy_id TEXT, name TEXT, description TEXT, key_hash TEXT, key_prefix TEXT, key_suffix TEXT, key_secret_encrypted TEXT, status TEXT, is_default INTEGER, purpose TEXT, expires_at TEXT, quota_limits_json TEXT, availability_schedule_json TEXT, availability_schedule_next_check_at TEXT, created_at TEXT, updated_at TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed schema: %v", err)
		}
	}
	fixture := &w1ChatKeysFixture{db: db, secret: "w1-chat-keys-secret", ownerID: "sys_chat", groupID: "grp_gpt", strategyID: "rs_gpt"}
	fixture.provider = newChatAPIKeyProvider(db, false, fixture.secret)
	return fixture
}

// seedDefaultGPTGroup 插入默认 gpt 分组，让默认策略路由可创建。
func (f *w1ChatKeysFixture) seedDefaultGPTGroup(t *testing.T) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, created_at)
		VALUES (?, ?, 'GPT 分组', 'gpt', 1, 1, '2026-01-01T00:00:00.000Z')`, f.groupID, f.ownerID); err != nil {
		t.Fatalf("seed group: %v", err)
	}
}

func TestW1ChatKeysProviderTableAndBind(t *testing.T) {
	f := newW1ChatKeysFixture(t)
	if got := f.provider.table("api_keys"); got != "api_keys" {
		t.Fatalf("sqlite table = %q", got)
	}
	if got := f.provider.bind("SELECT 1 WHERE a = ? AND b = ?"); got != "SELECT 1 WHERE a = ? AND b = ?" {
		t.Fatalf("sqlite bind = %q", got)
	}
	pg := newChatAPIKeyProvider(nil, true, "secret")
	if got := pg.table("api_keys"); got != "juhe_business.api_keys" {
		t.Fatalf("pg table = %q", got)
	}
	if got := pg.bind("SELECT 1 WHERE a = ? AND b = ?"); got != "SELECT 1 WHERE a = $1 AND b = $2" {
		t.Fatalf("pg bind = %q", got)
	}
}

func TestW1EnsureChatAPIKeyLifecycle(t *testing.T) {
	f := newW1ChatKeysFixture(t)
	// 无默认分组：fail fast（不触达 SQL）。
	if _, err := f.provider.EnsureChatAPIKey(f.ownerID); err == nil || !strings.Contains(err.Error(), "默认分组") {
		t.Fatalf("无默认分组错误 = %v", err)
	}
	f.seedDefaultGPTGroup(t)
	// 行为存疑：defaultGptRouteStrategyForSystemAccount 的 SQL 把
	// route_strategies 表别名为 route_strategy_groups（INNER JOIN %[1]s
	// route_strategy_groups），route_strategy_groups.route_strategy_id 引用
	// 不存在的列，首次创建路径在 SQLite/PostgreSQL 下都必然失败。当前实际
	// 行为按此断言；见报告「疑似生产问题」。
	if _, err := f.provider.EnsureChatAPIKey(f.ownerID); err == nil || !strings.Contains(err.Error(), "no such column") {
		t.Fatalf("首次 ensure 按当前实现必须失败（SQL 别名缺陷）: %v", err)
	}
	if _, err := f.provider.defaultGptRouteStrategyForSystemAccount(f.ownerID); err == nil || !strings.Contains(err.Error(), "no such column") {
		t.Fatalf("gpt strategy 按当前实现必须失败（SQL 别名缺陷）: %v", err)
	}
	// 预插入一条 chat key：ensure 命中 existing 分支直接返回（幂等路径）。
	secret := "sk-w1-chat-existing"
	sealed, err := w1SealChatKey(f.secret, secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := f.db.Exec(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_secret_encrypted, status, purpose)
		VALUES ('key_chat_1', ?, 'rs_1', 'AI 对话 API Key', 'hash', ?, 'active', 'chat')`, f.ownerID, sealed); err != nil {
		t.Fatalf("seed chat key: %v", err)
	}
	first, err := f.provider.EnsureChatAPIKey(f.ownerID)
	if err != nil || first != "key_chat_1" {
		t.Fatalf("existing ensure = %q, %v", first, err)
	}
	// FindChatAPIKey 解密成功。
	record, err := f.provider.FindChatAPIKey(first, f.ownerID)
	if err != nil || record == nil {
		t.Fatalf("find = %v, %v", record, err)
	}
	if record.ID != first || record.Status != "active" || record.Secret != secret {
		t.Fatalf("record = %+v secret=%q", record, record.Secret)
	}
	// 未知 key：nil。
	missing, err := f.provider.FindChatAPIKey("key_missing", f.ownerID)
	if err != nil || missing != nil {
		t.Fatalf("缺失 find = %v, %v", missing, err)
	}
	// 过期 key：nil。
	if _, err := f.db.Exec(`UPDATE api_keys SET expires_at = '2020-01-01T00:00:00.000Z' WHERE id = ?`, first); err != nil {
		t.Fatalf("expire: %v", err)
	}
	expired, err := f.provider.FindChatAPIKey(first, f.ownerID)
	if err != nil || expired != nil {
		t.Fatalf("过期 find = %v, %v", expired, err)
	}
}

func TestW1EnsureDefaultRouteStrategiesCreatesBinding(t *testing.T) {
	f := newW1ChatKeysFixture(t)
	// 无默认分组：fail fast。
	if err := f.provider.ensureDefaultRouteStrategies(f.ownerID, "2026-01-01T00:00:00.000Z"); err == nil {
		t.Fatal("无默认分组必须报错")
	}
	f.seedDefaultGPTGroup(t)
	if err := f.provider.ensureDefaultRouteStrategies(f.ownerID, "2026-01-01T00:00:00.000Z"); err != nil {
		t.Fatalf("ensure strategies: %v", err)
	}
	var name, mode string
	var isDefault int
	if err := f.db.QueryRow(`SELECT name, mode, is_default FROM route_strategies WHERE system_account_id = ?`, f.ownerID).
		Scan(&name, &mode, &isDefault); err != nil {
		t.Fatalf("read strategy: %v", err)
	}
	if mode != "normal" || isDefault != 1 {
		t.Fatalf("strategy mode/is_default = %q %d", mode, isDefault)
	}
	var bindingCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM route_strategy_groups WHERE system_account_id = ?`, f.ownerID).Scan(&bindingCount); err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if bindingCount != 1 {
		t.Fatalf("策略分组绑定 = %d，want 1", bindingCount)
	}
	// 幂等：再次 ensure 不新建。
	if err := f.provider.ensureDefaultRouteStrategies(f.ownerID, "2026-01-01T00:00:00.000Z"); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	var strategyCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM route_strategies WHERE system_account_id = ?`, f.ownerID).Scan(&strategyCount); err != nil {
		t.Fatalf("count strategies: %v", err)
	}
	if strategyCount != 1 {
		t.Fatalf("策略数 = %d，want 1", strategyCount)
	}
}

// w1SealChatKey 用生产加密封装构造 key_secret_encrypted。
func w1SealChatKey(secret, plaintext string) (string, error) {
	return apikeys.EncryptJSON(secret, map[string]string{"key": plaintext})
}

func TestW1NextDefaultNames(t *testing.T) {
	f := newW1ChatKeysFixture(t)
	name, err := f.provider.nextDefaultApiKeyName(f.ownerID, "AI 对话 API Key")
	if err != nil || name != "AI 对话 API Key" {
		t.Fatalf("首名 = %q, %v", name, err)
	}
	if _, err := f.db.Exec(`INSERT INTO api_keys (id, system_account_id, name, key_hash, status)
		VALUES ('key_1', ?, 'AI 对话 API Key', 'hash', 'active')`, f.ownerID); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	name, err = f.provider.nextDefaultApiKeyName(f.ownerID, "AI 对话 API Key")
	if err != nil || name != "AI 对话 API Key 2" {
		t.Fatalf("次名 = %q, %v", name, err)
	}
	strategyName, err := f.provider.nextDefaultRouteStrategyName(f.ownerID, "GPT路由")
	if err != nil || strategyName != "GPT路由" {
		t.Fatalf("策略名 = %q, %v", strategyName, err)
	}
}

func TestW1ChatNameHelpers(t *testing.T) {
	if got := chainNextDefaultNameFromExisting(nil, "基名"); got != "基名" {
		t.Fatalf("无冲突 = %q", got)
	}
	if got := chainNextDefaultNameFromExisting([]string{" 基名 ", "基名 2"}, "基名"); got != "基名 3" {
		t.Fatalf("冲突递增 = %q", got)
	}
	if got := chainDefaultRouteStrategyNameForGroup("  "); got != "默认路由" {
		t.Fatalf("空名 = %q", got)
	}
	if got := chainDefaultRouteStrategyNameForGroup("GPT 分组"); got != "GPT 路由" {
		t.Fatalf("分组后缀 = %q", got)
	}
	if got := chainDefaultRouteStrategyNameForGroup("默认"); got != "默认" {
		t.Fatalf("无后缀 = %q", got)
	}
	if isDuplicateAPIKeyNameError(nil) || isDuplicateChatAPIKeyError(nil) || isDuplicateRouteStrategyNameError(nil) {
		t.Fatal("nil 错误必须返回 false")
	}
	if !isDuplicateAPIKeyNameError(errors.New("UNIQUE constraint failed: api_keys.system_account_id, api_keys.name")) {
		t.Fatal("api key 名冲突未识别")
	}
	if !isDuplicateChatAPIKeyError(errors.New("idx_api_keys_chat_purpose_unique failed")) {
		t.Fatal("chat key 冲突未识别")
	}
	if !isDuplicateRouteStrategyNameError(errors.New("idx_route_strategies_owner_name_unique")) {
		t.Fatal("策略名冲突未识别")
	}
}

func TestW1ChatAttachSubscriberTrySend(t *testing.T) {
	subscriber := &chatAttachSubscriber{events: make(chan chat.ChatGenerationEvent, 2)}
	for i := 0; i < 2; i++ {
		if !subscriber.TrySend(chat.ChatGenerationEvent{Type: "tick"}) {
			t.Fatalf("缓冲内第 %d 条必须成功", i+1)
		}
	}
	if subscriber.TrySend(chat.ChatGenerationEvent{Type: "overflow"}) {
		t.Fatal("溢出必须 drop")
	}
	if !subscriber.dropped {
		t.Fatal("溢出后必须标记 dropped")
	}
	drained := 0
	for range subscriber.events {
		drained++
	}
	if drained != 2 {
		t.Fatalf("缓冲事件 = %d，want 2", drained)
	}
	if subscriber.TrySend(chat.ChatGenerationEvent{Type: "after"}) {
		t.Fatal("dropped 后必须直接 false")
	}
}

func TestW1OpenChatDatabase(t *testing.T) {
	// SQLite 缺路径：fail fast。
	if _, _, err := openChatDatabase(runtimeConfig{}, nil, nil, false); err == nil {
		t.Fatal("缺 chat 数据库路径必须报错")
	}
	db, owns, err := openChatDatabase(runtimeConfig{ChatDatabasePath: filepath.Join(t.TempDir(), "chat.sqlite3")}, nil, nil, false)
	if err != nil || db == nil || !owns {
		t.Fatalf("open sqlite chat db = %v, %v", owns, err)
	}
	_ = db.Close()
}
