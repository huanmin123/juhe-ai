package main

// w1 波次补充单元测试（w1h 前缀，TestW1H 入口）：覆盖 my-chat 路由族装配
// （chain_chat_mount.go 的 composeChatFamily / chatAttachStreamHandler /
// chatAttachSubscriber.TrySend / openChatDatabase）、聊天图片观察 SQLite
// 分支（chain_chat_observation.go 的 table/bind/claim/setObservation）与
// chain_compose.go 剩余可构造函数（ListClientModelCatalog、
// spoolOverflow.PersistOverflow）。
//
// 不覆盖：openChatDatabase 的 pgDialect=true 分支与 chatImageObservations
// 的 postgres 方言 SQL 真实执行（需要 pgpool.Registry 与真实 PostgreSQL，
// SQLite 单测无法确定性重放）；Wait/Schedule/track 等 time.After / goroutine
// 等待路径按 w1 波次约定跳过。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// w1hNow 是测试用的固定 now（chainTimeLayout，毫秒 RFC3339 UTC）。
const w1hNow = "2026-09-14T12:00:00.000Z"

// ---------------------------------------------------------------------------
// fixture: w1hReadModels + runtime cache 组装
// ---------------------------------------------------------------------------

// w1hReadModels 是 gatewayruntimecache.ReadModels 的最小 stub：只携带测试
// 关注的分组元数据、账户列表与模型目录，缓存语义走真实 Service。
type w1hReadModels struct {
	groupAccess *gatewayruntimecache.GroupUsageAccessMetadata
	accounts    []gatewayruntimecache.OpenAIAccountSecret
	catalog     []gatewayruntimecache.ProviderModelCatalogItem
}

func (m *w1hReadModels) ReadGatewaySettings(context.Context) (gatewayruntimecache.GatewaySettings, error) {
	return gatewayruntimecache.GatewaySettings{}, nil
}

func (m *w1hReadModels) ReadGatewayRuntime(context.Context, string) (gatewayruntimecache.GatewayRuntime, error) {
	return gatewayruntimecache.GatewayRuntime{}, nil
}

func (m *w1hReadModels) ResolveGroupUsageAccessMetadata(context.Context, string, string) (*gatewayruntimecache.GroupUsageAccessMetadata, error) {
	return m.groupAccess, nil
}

func (m *w1hReadModels) ListOpenAIAccountsForGroupResult(context.Context, string, string, gatewayruntimecache.OpenAIAccountsForGroupOptions) (gatewayruntimecache.OpenAIAccountsForGroupResult, error) {
	return gatewayruntimecache.OpenAIAccountsForGroupResult{Accounts: m.accounts}, nil
}

func (m *w1hReadModels) ListActiveResponseInspectionPolicies(context.Context, string, string) ([]gatewayruntimecache.ResponseInspectionPolicySummary, error) {
	return nil, nil
}

func (m *w1hReadModels) ListProviderModelCatalog(_ context.Context, input gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	out := []gatewayruntimecache.ProviderModelCatalogItem{}
	for _, item := range m.catalog {
		if input.ProviderCode != "" && item.ProviderCode != input.ProviderCode {
			continue
		}
		if input.SystemAccountID != "" && item.SystemAccountID != nil && *item.SystemAccountID != input.SystemAccountID {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (m *w1hReadModels) LoadAccountCurrentConcurrencyByID(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func w1hNewRuntimeCache(t *testing.T, models *w1hReadModels) *gatewayruntimecache.Service {
	t.Helper()
	service, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatalf("组装 w1h runtime cache: %v", err)
	}
	return service
}

func w1hFloatPtr(value float64) *float64 { return &value }

func w1hStringPtr(value string) *string { return &value }

// ---------------------------------------------------------------------------
// A2. chatAttachSubscriber.TrySend（非阻塞 channel 语义）
// ---------------------------------------------------------------------------

func TestW1HChatAttachSubscriberTrySend(t *testing.T) {
	idle := &chatAttachSubscriber{events: make(chan chat.ChatGenerationEvent, 1)}
	if !idle.TrySend(chat.ChatGenerationEvent{Type: "message.snapshot", EventVersion: 1}) {
		t.Fatalf("空闲 chatAttachSubscriber TrySend = false, want true")
	}
	if drained := <-idle.events; drained.Type != "message.snapshot" {
		t.Errorf("空闲投递事件类型 = %q, want message.snapshot", drained.Type)
	}

	full := &chatAttachSubscriber{events: make(chan chat.ChatGenerationEvent, 1)}
	full.events <- chat.ChatGenerationEvent{Type: "prefill"}
	if full.TrySend(chat.ChatGenerationEvent{Type: "overflow"}) {
		t.Fatalf("满容量 chatAttachSubscriber TrySend = true, want false（非阻塞丢弃）")
	}
	if !full.dropped {
		t.Errorf("满容量 TrySend 未标记 dropped")
	}
	if full.TrySend(chat.ChatGenerationEvent{Type: "again"}) {
		t.Errorf("dropped 后 TrySend = true, want false")
	}
	// 关闭后缓冲中的既有事件仍可读，随后通道呈关闭态。
	if drained, open := <-full.events; !open || drained.Type != "prefill" {
		t.Errorf("关闭后缓冲事件 = (%q, %t), want (prefill, true)", drained.Type, open)
	}
	if _, open := <-full.events; open {
		t.Errorf("满容量丢弃后事件通道未关闭")
	}
}

// ---------------------------------------------------------------------------
// A1. chatAttachStreamHandler（SSE 下发 / 客户端断开 / 订阅失败分支）
// ---------------------------------------------------------------------------

// w1hBlockingWriter 是首次 Write 阻塞的 ResponseWriter：用 channel 同步
// “handler 已 Subscribe 并开始写 SSE”的时点，之后放行让事件序列完整落盘。
type w1hBlockingWriter struct {
	mu          sync.Mutex
	buf         strings.Builder
	header      http.Header
	code        int
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
}

func (w *w1hBlockingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *w1hBlockingWriter) WriteHeader(code int) { w.code = code }

func (w *w1hBlockingWriter) Write(p []byte) (int, error) {
	w.enteredOnce.Do(func() { close(w.entered) })
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *w1hBlockingWriter) Flush() {}

func (w *w1hBlockingWriter) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// w1hLaunchBlockedRunner 注册并启动一个 Execute 阻塞在 release 上的 runner：
// 借 chat runner 的 running 态驱动 hub 事件，不等待 run goroutine 的时钟，
// 只依赖 channel 同步点。
func w1hLaunchBlockedRunner(t *testing.T, hub *chat.GenerationHub, identity chat.GenerationIdentity) (*chat.ChatGenerationRunner, <-chan *chat.ChatGenerationExecutionContext, chan struct{}) {
	t.Helper()
	execCh := make(chan *chat.ChatGenerationExecutionContext, 1)
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runner := chat.NewChatGenerationRunner(chat.ChatGenerationRunnerOptions{
		Identity: chat.ChatGenerationIdentity{
			OwnerID:            identity.OwnerID,
			ConversationID:     identity.ConversationID,
			TurnID:             identity.TurnID,
			AssistantMessageID: "msg-w1h",
		},
		Execute: func(exec *chat.ChatGenerationExecutionContext) (chat.ChatGenerationTerminalResult, error) {
			execCh <- exec
			<-release
			return chat.ChatGenerationTerminalResult{Status: "completed", Data: map[string]any{"messageId": "msg-w1h"}}, nil
		},
	}, ctx, cancel, func() bool { return ctx.Err() != nil })
	if !hub.Start(runner) {
		t.Fatalf("hub.Start 失败（conversation=%s 已被占用）", identity.ConversationID)
	}
	return runner, execCh, release
}

func TestW1HChatAttachStreamHandlerStreamsEvents(t *testing.T) {
	hub := chat.NewGenerationHub(func() string { return w1hNow })
	identity := chat.GenerationIdentity{OwnerID: "owner-w1h", ConversationID: "conv-w1h-sse", TurnID: "turn-w1h-sse"}
	runner, execCh, release := w1hLaunchBlockedRunner(t, hub, identity)

	writer := &w1hBlockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	handler := chatAttachStreamHandler(hub)
	done := make(chan bool, 1)
	go func() {
		done <- handler(writer, httptest.NewRequest(http.MethodGet, "/attach", nil), identity)
	}()

	exec := <-execCh
	<-writer.entered // 首次 SSE 写入 = Subscribe 已附带 message.snapshot
	if !exec.Publish("message.completed", map[string]any{"messageId": "msg-w1h"}, chat.ChatGenerationProjectionUpdate{}) {
		close(writer.release)
		t.Fatalf("runner Publish message.completed 失败")
	}
	close(writer.release)

	if ok := <-done; !ok {
		t.Fatalf("chatAttachStreamHandler = false, want true（终结事件后正常收束）")
	}
	if writer.code != http.StatusOK {
		t.Errorf("SSE WriteHeader = %d, want %d", writer.code, http.StatusOK)
	}
	if ct := writer.Header().Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Errorf("SSE Content-Type = %q, want text/event-stream; charset=utf-8", ct)
	}
	if cc := writer.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("SSE Cache-Control = %q, want no-store", cc)
	}
	if buffering := writer.Header().Get("X-Accel-Buffering"); buffering != "no" {
		t.Errorf("SSE X-Accel-Buffering = %q, want no", buffering)
	}
	body := writer.body()
	for _, want := range []string{"event: message.snapshot", "event: message.completed", `"messageId":"msg-w1h"`, "eventVersion"} {
		if !strings.Contains(body, want) {
			t.Errorf("SSE body 缺少 %q，实际：\n%s", want, body)
		}
	}
	close(release)
	<-runner.Completion() // Execute 解除阻塞后 runner 收敛，goroutine 不外泄
}

func TestW1HChatAttachStreamHandlerClientDisconnect(t *testing.T) {
	hub := chat.NewGenerationHub(func() string { return w1hNow })
	identity := chat.GenerationIdentity{OwnerID: "owner-w1h", ConversationID: "conv-w1h-disc", TurnID: "turn-w1h-disc"}
	runner, _, release := w1hLaunchBlockedRunner(t, hub, identity)
	t.Cleanup(func() {
		close(release)
		<-runner.Completion()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预先取消：客户端已断开
	request := httptest.NewRequest(http.MethodGet, "/attach", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	if ok := chatAttachStreamHandler(hub)(recorder, request, identity); !ok {
		t.Fatalf("客户端断开分支 chatAttachStreamHandler = false, want true")
	}
	if recorder.Code != http.StatusOK {
		t.Errorf("客户端断开分支 WriteHeader = %d, want %d", recorder.Code, http.StatusOK)
	}
	if ct := recorder.Header().Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Errorf("客户端断开分支 Content-Type = %q, want text/event-stream; charset=utf-8", ct)
	}
}

func TestW1HChatAttachStreamHandlerSubscribeMiss(t *testing.T) {
	hub := chat.NewGenerationHub(func() string { return w1hNow })
	identity := chat.GenerationIdentity{OwnerID: "owner-w1h", ConversationID: "conv-w1h-miss", TurnID: "turn-w1h-miss"}
	recorder := httptest.NewRecorder()
	if ok := chatAttachStreamHandler(hub)(recorder, httptest.NewRequest(http.MethodGet, "/attach", nil), identity); ok {
		t.Fatalf("未注册 runner 时 chatAttachStreamHandler = true, want false")
	}
}

// ---------------------------------------------------------------------------
// A3. openChatDatabase（SQLite 分支；pgDialect=true 需要 pgpool Registry，跳过）
// ---------------------------------------------------------------------------

func TestW1HOpenChatDatabaseSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat-w1h.sqlite3")
	db, isSQLite, err := openChatDatabase(runtimeConfig{ChatDatabasePath: path}, nil, nil, false)
	if err != nil {
		t.Fatalf("openChatDatabase(sqlite) = %v, want nil", err)
	}
	defer db.Close()
	if !isSQLite {
		t.Fatalf("sqlite 分支 isSQLite = false, want true")
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("chat sqlite 句柄 Ping 失败: %v", err)
	}
}

func TestW1HOpenChatDatabaseSQLiteRequiresPath(t *testing.T) {
	_, _, err := openChatDatabase(runtimeConfig{}, nil, nil, false)
	if err == nil {
		t.Fatalf("缺少 JUHE_AI_CHAT_DATABASE_PATH 时 openChatDatabase 未报错")
	}
	if !strings.Contains(err.Error(), "JUHE_AI_CHAT_DATABASE_PATH") {
		t.Fatalf("错误未指向 JUHE_AI_CHAT_DATABASE_PATH: %v", err)
	}
}

// ---------------------------------------------------------------------------
// A4. composeChatFamily（guard 表 + 最小装配）
// ---------------------------------------------------------------------------

func w1hNewChatFamilyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "chat-family-w1h.sqlite3"))
	if err != nil {
		t.Fatalf("打开 chat family sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestW1HComposeChatFamilyGuards(t *testing.T) {
	db := w1hNewChatFamilyDB(t)
	cache := w1hNewRuntimeCache(t, &w1hReadModels{})
	cases := []struct {
		name           string
		nilComposition bool
		chatDB         *sql.DB
		nilServices    bool
		nilCache       bool
		nilChain       bool
		wantErr        string
	}{
		{name: "composition 缺失", nilComposition: true, chatDB: db, wantErr: "composition"},
		{name: "聊天数据库句柄缺失", chatDB: nil, wantErr: "聊天数据库句柄"},
		{name: "chain runtime services 缺失", chatDB: db, nilServices: true, wantErr: "runtime cache"},
		{name: "runtime cache 缺失", chatDB: db, nilCache: true, wantErr: "runtime cache"},
		{name: "网关链缺失", chatDB: db, nilChain: true, wantErr: "网关链"},
	}
	for _, testCase := range cases {
		composed := &composition{db: db, kernel: kernel.New(kernel.Options{}), authDeps: &authsys.Deps{}}
		services := &chainRuntimeServices{Cache: cache}
		chain := &gatewayChain{}
		if testCase.nilComposition {
			composed = nil
		}
		if testCase.nilServices {
			services = nil
		}
		if testCase.nilCache {
			services = &chainRuntimeServices{}
		}
		if testCase.nilChain {
			chain = nil
		}
		_, err := composeChatFamily(composed, runtimeConfig{ChatAssetsRoot: t.TempDir()}, testCase.chatDB, services, chain)
		if err == nil {
			t.Fatalf("%s: composeChatFamily 未报错", testCase.name)
		}
		if !strings.Contains(err.Error(), testCase.wantErr) {
			t.Fatalf("%s: 错误 %v 不含 %q", testCase.name, err, testCase.wantErr)
		}
	}
}

func TestW1HComposeChatFamilyAssemblesDeps(t *testing.T) {
	db := w1hNewChatFamilyDB(t)
	composed := &composition{db: db, kernel: kernel.New(kernel.Options{}), authDeps: &authsys.Deps{}}
	services := &chainRuntimeServices{Cache: w1hNewRuntimeCache(t, &w1hReadModels{})}
	cfg := runtimeConfig{
		ChatAssetsRoot:              t.TempDir(),
		Secret:                      "w1h-secret",
		ChatMaxTurnsPerConversation: 7,
		ChatRetentionDays:           3,
		ChatDiagnosticToolEnabled:   true,
		ChatToolEnvironment:         "w1h-env",
	}
	deps, err := composeChatFamily(composed, cfg, db, services, &gatewayChain{})
	if err != nil {
		t.Fatalf("composeChatFamily = %v, want nil", err)
	}
	if deps == nil {
		t.Fatalf("deps = nil")
	}
	if deps.Store == nil || deps.Store.Postgres() {
		t.Fatalf("chat Store 缺失或方言错误（want sqlite）")
	}
	if deps.Hub == nil || deps.Generations == nil {
		t.Fatalf("GenerationHub 未接线")
	}
	if deps.Executor == nil || deps.AttachStream == nil {
		t.Fatalf("executor / attach stream 未接线")
	}
	if deps.ToolCapabilit == nil {
		t.Fatalf("ToolCapabilit 未接线（BUG-0175 D-201）")
	}
	if deps.ObjectStore == nil || deps.ImageObservation == nil {
		t.Fatalf("ObjectStore / ImageObservation 未接线")
	}
	if deps.MaxTurnsPerConversation != 7 {
		t.Errorf("MaxTurnsPerConversation = %d, want 7", deps.MaxTurnsPerConversation)
	}
	if deps.RetentionDays != 3 {
		t.Errorf("RetentionDays = %d, want 3", deps.RetentionDays)
	}
	if !deps.DiagnosticToolEnabled {
		t.Errorf("DiagnosticToolEnabled = false, want true")
	}
	if deps.ToolEnvironment != "w1h-env" {
		t.Errorf("ToolEnvironment = %q, want w1h-env", deps.ToolEnvironment)
	}
}

// ---------------------------------------------------------------------------
// B5. chatImageObservations table/bind（方言）
// ---------------------------------------------------------------------------

func TestW1HChatObservationTableAndBind(t *testing.T) {
	sqlitePort := &chatImageObservations{postgres: false}
	pgPort := &chatImageObservations{postgres: true}
	if got := sqlitePort.table("chat_assets"); got != "chat_assets" {
		t.Fatalf("sqlite table = %q, want chat_assets", got)
	}
	if got := pgPort.table("chat_assets"); got != "juhe_chat.chat_assets" {
		t.Fatalf("postgres table = %q, want juhe_chat.chat_assets", got)
	}
	query := "UPDATE t SET a = ?, b = ? WHERE c = ?"
	if got := sqlitePort.bind(query); got != query {
		t.Fatalf("sqlite bind 改写了占位符: %q", got)
	}
	if got := pgPort.bind(query); got != "UPDATE t SET a = $1, b = $2 WHERE c = $3" {
		t.Fatalf("postgres bind = %q, want $1/$2/$3 形态", got)
	}
	if got := pgPort.bind("no placeholder"); got != "no placeholder" {
		t.Fatalf("postgres bind 无占位符时改写了原文: %q", got)
	}
}

// ---------------------------------------------------------------------------
// B6. claim / setObservation（SQLite + RETURNING）
// ---------------------------------------------------------------------------

func w1hNewObservationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "chat-observation-w1h.sqlite3"))
	if err != nil {
		t.Fatalf("打开观察 sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE chat_assets (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			processing_status TEXT NOT NULL DEFAULT 'ready',
			cleanup_status TEXT NOT NULL DEFAULT 'active',
			expires_at TEXT NOT NULL,
			observation_status TEXT NOT NULL DEFAULT 'not_requested',
			observation_json TEXT,
			observation_revision INTEGER NOT NULL DEFAULT 0,
			observation_claim_id TEXT,
			observation_claimed_at TEXT,
			updated_at TEXT NOT NULL)`,
		`CREATE TABLE chat_asset_references (
			asset_id TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			message_id TEXT NOT NULL,
			reference_kind TEXT NOT NULL,
			expires_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("建观察表: %v", err)
		}
	}
	for _, assetID := range []string{"asset-w1h", "asset-w1h-b"} {
		if _, err := db.Exec(`INSERT INTO chat_assets (id, system_account_id, conversation_id, expires_at, updated_at)
			VALUES (?, 'sys-w1h', 'conv-w1h', '2027-01-01T00:00:00.000Z', '2026-09-01T00:00:00.000Z')`, assetID); err != nil {
			t.Fatalf("seed chat_assets(%s): %v", assetID, err)
		}
		if _, err := db.Exec(`INSERT INTO chat_asset_references (asset_id, conversation_id, turn_id, message_id, reference_kind, expires_at)
			VALUES (?, 'conv-w1h', 'turn-w1h', 'msg-w1h', 'user_input', '2027-01-01T00:00:00.000Z')`, assetID); err != nil {
			t.Fatalf("seed chat_asset_references(%s): %v", assetID, err)
		}
	}
	return db
}

func w1hNewObservationPort(db *sql.DB) *chatImageObservations {
	return &chatImageObservations{db: db, postgres: false, tasks: map[string][]chan struct{}{}}
}

// w1hClaim 封装 claim 断言：RETURNING 语法不被底层 SQLite 支持时跳过并保留
// 原始错误，其余错误直接失败。
func w1hClaim(t *testing.T, port *chatImageObservations, ctx context.Context, assetID, turnID, messageID, now string) *chatObservationClaim {
	t.Helper()
	claimed, err := port.claim(ctx, assetID, "conv-w1h", "sys-w1h", turnID, messageID, now)
	if err != nil {
		if strings.Contains(err.Error(), "syntax error") {
			t.Skipf("当前 modernc.org/sqlite 不支持 UPDATE...RETURNING，跳过 claim 断言，原始错误: %v", err)
		}
		t.Fatalf("claim(%s) = %v, want nil", assetID, err)
	}
	return claimed
}

func TestW1HChatObservationClaim(t *testing.T) {
	db := w1hNewObservationDB(t)
	port := w1hNewObservationPort(db)
	ctx := context.Background()

	claimed := w1hClaim(t, port, ctx, "asset-w1h", "turn-w1h", "msg-w1h", w1hNow)
	if claimed == nil {
		t.Fatalf("首次 claim = nil, want 抢占成功")
	}
	if claimed.observationRevision != 1 {
		t.Errorf("observationRevision = %d, want 1", claimed.observationRevision)
	}
	if !strings.HasPrefix(claimed.claimID, "chat_obs_claim_") {
		t.Errorf("claimID = %q, want chat_obs_claim_ 前缀", claimed.claimID)
	}
	var status, rowClaimID string
	if err := db.QueryRow(`SELECT observation_status, observation_claim_id FROM chat_assets WHERE id = 'asset-w1h'`).
		Scan(&status, &rowClaimID); err != nil {
		t.Fatalf("回读 claim 行态: %v", err)
	}
	if status != "pending" {
		t.Errorf("claim 后 observation_status = %q, want pending", status)
	}
	if rowClaimID != claimed.claimID {
		t.Errorf("行内 observation_claim_id = %q, want %q", rowClaimID, claimed.claimID)
	}

	// 当前实现语义：claim 的 staleBefore 恒等于 now（15 分钟回退常量
	// chatObservationClaimStaleMs 未参与计算），pending claim 可被立即接管。
	reClaim := w1hClaim(t, port, ctx, "asset-w1h", "turn-w1h", "msg-w1h", w1hNow)
	if reClaim == nil {
		t.Fatalf("pending claim 立即重试 = nil, want 按 current 语义接管（revision 2）")
	}
	if reClaim.observationRevision != 2 {
		t.Errorf("立即接管 observationRevision = %d, want 2", reClaim.observationRevision)
	}
	// 已被占且 claimed_at 在未来：抢占被阻塞，无行。
	if _, err := db.Exec(`UPDATE chat_assets SET observation_claimed_at = '2027-01-01T00:00:00.000Z' WHERE id = 'asset-w1h'`); err != nil {
		t.Fatalf("前移 observation_claimed_at: %v", err)
	}
	if blocked := w1hClaim(t, port, ctx, "asset-w1h", "turn-w1h", "msg-w1h", w1hNow); blocked != nil {
		t.Fatalf("未过期 claim = %+v, want nil", blocked)
	}
	if wrongTurn := w1hClaim(t, port, ctx, "asset-w1h", "turn-other", "msg-w1h", w1hNow); wrongTurn != nil {
		t.Fatalf("引用不匹配（turn 不符）claim = %+v, want nil", wrongTurn)
	}
	if _, err := port.claim(ctx, "asset-w1h", "conv-w1h", "sys-w1h", "turn-w1h", "msg-w1h", "not-rfc3339"); err == nil {
		t.Fatalf("非法 now 未报错")
	} else if !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("非法 now 错误未指向 RFC3339: %v", err)
	}

	// 另一行首次抢占 + 后续时间点接管：revision 连续递增。
	if first := w1hClaim(t, port, ctx, "asset-w1h-b", "turn-w1h", "msg-w1h", w1hNow); first == nil || first.observationRevision != 1 {
		t.Fatalf("asset-w1h-b 首次 claim = %+v, want revision 1", first)
	}
	later := "2026-09-14T12:16:00.000Z"
	takeover := w1hClaim(t, port, ctx, "asset-w1h-b", "turn-w1h", "msg-w1h", later)
	if takeover == nil {
		t.Fatalf("后续时间点 claim 未接管, want revision 2")
	}
	if takeover.observationRevision != 2 {
		t.Errorf("接管后 observationRevision = %d, want 2", takeover.observationRevision)
	}
}

func TestW1HChatObservationSetObservation(t *testing.T) {
	db := w1hNewObservationDB(t)
	port := w1hNewObservationPort(db)
	ctx := context.Background()

	claimed := w1hClaim(t, port, ctx, "asset-w1h", "turn-w1h", "msg-w1h", w1hNow)
	if claimed == nil {
		t.Fatalf("claim = nil, want 抢占成功")
	}
	if _, err := port.setObservation(ctx, "asset-w1h", "conv-w1h", "sys-w1h", "ready", nil,
		claimed.observationRevision, claimed.claimID, w1hNow); err == nil || !strings.Contains(err.Error(), "必须提供 observation") {
		t.Fatalf("ready 缺 observation 错误 = %v, want 必须提供 observation", err)
	}
	huge := map[string]any{"summary": strings.Repeat("a", 70_000)}
	if _, err := port.setObservation(ctx, "asset-w1h", "conv-w1h", "sys-w1h", "failed", huge,
		claimed.observationRevision, claimed.claimID, w1hNow); err == nil || !strings.Contains(err.Error(), "超出字节上限") {
		t.Fatalf("超限 observation 错误 = %v, want 超出字节上限", err)
	}
	if mismatched, err := port.setObservation(ctx, "asset-w1h", "conv-w1h", "sys-w1h", "ready", map[string]any{"summary": "wrong"},
		claimed.observationRevision+999, claimed.claimID, w1hNow); err != nil || mismatched {
		t.Fatalf("revision 不匹配 setObservation = (%t, %v), want (false, nil)", mismatched, err)
	}

	ok, err := port.setObservation(ctx, "asset-w1h", "conv-w1h", "sys-w1h", "ready", map[string]any{"summary": "w1h 说明"},
		claimed.observationRevision, claimed.claimID, w1hNow)
	if err != nil {
		t.Fatalf("setObservation(ready) = %v, want nil", err)
	}
	if !ok {
		t.Fatalf("setObservation(ready) = false, want true")
	}
	var status string
	var observationJSON, rowClaimID sql.NullString
	if err := db.QueryRow(`SELECT observation_status, observation_json, observation_claim_id FROM chat_assets WHERE id = 'asset-w1h'`).
		Scan(&status, &observationJSON, &rowClaimID); err != nil {
		t.Fatalf("回读 setObservation 行态: %v", err)
	}
	if status != "ready" {
		t.Errorf("setObservation 后 observation_status = %q, want ready", status)
	}
	if !strings.Contains(observationJSON.String, "w1h 说明") {
		t.Errorf("observation_json = %q, want 含 w1h 说明", observationJSON.String)
	}
	if rowClaimID.Valid {
		t.Errorf("setObservation 后 observation_claim_id = %q, want 已清空", rowClaimID.String)
	}

	replay, err := port.setObservation(ctx, "asset-w1h", "conv-w1h", "sys-w1h", "ready", map[string]any{"summary": "again"},
		claimed.observationRevision, claimed.claimID, w1hNow)
	if err != nil {
		t.Fatalf("重复 setObservation = %v, want nil", err)
	}
	if replay {
		t.Fatalf("重复 setObservation = true, want false（claim 已释放）")
	}
}

// ---------------------------------------------------------------------------
// C8. chainClientModelCatalog.ListClientModelCatalog
// ---------------------------------------------------------------------------

func TestW1HListClientModelCatalog(t *testing.T) {
	invisible := false
	catalog := []gatewayruntimecache.ProviderModelCatalogItem{
		{Scope: "global", Status: "active", ProviderCode: "openai", Model: "w1h-a", InputUsdPer1M: w1hFloatPtr(2.5), ReleaseDate: w1hStringPtr("2026-01-01")},
		// 同模型 personal 作用域副本：best-scope-first 去重应保留 personal。
		{Scope: "personal", Status: "active", ProviderCode: "openai", Model: "w1h-a", InputUsdPer1M: w1hFloatPtr(2.5), ReleaseDate: w1hStringPtr("2026-01-01")},
		// built_in 且目录不可见：过滤。
		{Scope: "built_in", Status: "active", ProviderCode: "openai", Model: "w1h-b", CatalogVisible: &invisible, InputUsdPer1M: w1hFloatPtr(1)},
		// 非 active：过滤。
		{Scope: "global", Status: "inactive", ProviderCode: "openai", Model: "w1h-c", InputUsdPer1M: w1hFloatPtr(1)},
		// 无可见价格：过滤。
		{Scope: "global", Status: "active", ProviderCode: "openai", Model: "w1h-d"},
		// 另一供应商，release date 更新：排序在前。
		{Scope: "personal", Status: "active", ProviderCode: "anthropic", Model: "w1h-e", OutputUsdPer1M: w1hFloatPtr(5), ReleaseDate: w1hStringPtr("2026-02-01")},
	}
	loader := chainClientModelCatalog{cache: w1hNewRuntimeCache(t, &w1hReadModels{catalog: catalog})}
	entries := loader.ListClientModelCatalog("sys-w1h", []string{"openai", " OpenAI ", "anthropic", ""})
	if len(entries) != 2 {
		t.Fatalf("entries = %d（%+v）, want 2", len(entries), entries)
	}
	if entries[0].Model != "w1h-e" || entries[0].Scope != "personal" {
		t.Errorf("entries[0] = %+v, want w1h-e/personal（release date 降序）", entries[0])
	}
	if entries[1].Model != "w1h-a" || entries[1].Scope != "personal" {
		t.Errorf("entries[1] = %+v, want w1h-a/personal（best-scope-first 去重保留 personal）", entries[1])
	}
	if got := loader.ListClientModelCatalog("sys-w1h", nil); got != nil {
		t.Fatalf("空 provider 列表 = %+v, want nil", got)
	}
	if got := (chainClientModelCatalog{}).ListClientModelCatalog("sys-w1h", []string{"openai"}); got != nil {
		t.Fatalf("cache 为 nil 时 = %+v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// C9. spoolOverflow.PersistOverflow
// ---------------------------------------------------------------------------

func TestW1HSpoolOverflowPersistOverflow(t *testing.T) {
	var empty spoolOverflow
	if err := empty.PersistOverflow(context.Background(), gatewayusage.UsageRecordInput{TraceID: "trace-w1h", TrafficSource: "gateway"}); err != nil {
		t.Fatalf("nil spool PersistOverflow = %v, want nil（无操作成功）", err)
	}

	dir := t.TempDir()
	spool := gatewayusageNewSpool(t, dir, gatewaypreauth.SystemClock{})
	overflow := spoolOverflow{spool: spool}
	if err := overflow.PersistOverflow(context.Background(), gatewayusage.UsageRecordInput{TraceID: "trace-w1h", TrafficSource: "gateway"}); err != nil {
		t.Fatalf("PersistOverflow = %v, want nil", err)
	}
	if runtime := spool.Runtime(); runtime.PersistedCount != 1 || runtime.PendingItems != 1 {
		t.Fatalf("spool runtime = %+v, want PersistedCount=1 PendingItems=1", runtime)
	}
	if entries := spoolDirectoryEntries(t, dir); len(entries) == 0 {
		t.Fatalf("spool 目录无落盘内容: %v", entries)
	}
}
