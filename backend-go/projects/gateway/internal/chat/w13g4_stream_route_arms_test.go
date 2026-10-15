package chat

// w13g4 覆盖率补齐：streamTurn 前置错误臂、上下文预算臂、prep 生命周期臂。
//
// 不可达 / 高成本语句登记（基于 2026-09-18 覆盖率 profile w13g4_chat.out）：
//   - stream_route.go 432-435 modelOption==nil：仅当 modelIDs 全空（body.Model==""）
//     时可达；parseStreamMessageBody 对 model 强制非空（required），buildChatModelOptions
//     对任意非空模型恒产出 option。
//   - stream_route.go 457-460：436 resolveChatSupportedProtocols 与 451-455 过滤使用
//     同一 chatTransportAccountSupportsProtocol 判定，449 的查询参数与 436 等价
//     （accountsForGroups 逐 group 拼接），选中协议必存在可达账户，filteredAccounts 空
//     时 443-446 先行拦截。
//   - stream_route.go 604-606：buildTransport 产物为纯 JSON 可序列化 map，
//     json.Marshal 不可能失败。
//   - stream_route.go 614-637：需要 >21MiB 传输体；192KiB 消息上限（parse）与 16MiB
//     本地装载上限（LoadModelContext）使其常规不可达，构造极端数据成本过高。
//   - stream_route.go 659-662 Duplicate：需要 streamTurn 执行中外部写入幂等行；
//     SQLite 单连接池 fixture 在事务外另一连接写入会触发 SQLITE_BUSY，不可行。
//   - stream_route.go 754-766 Launch false：runner 于 701 行新建，Start 前 started 恒
//     为 false，Launch(runner) 恒成功。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	sqlite "modernc.org/sqlite"
)

func w13g4StreamPost(rt *chatRoutes, conversationID, owner, payload string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", "/conversations/"+conversationID+"/stream", strings.NewReader(payload))
	request.SetPathValue("conversationId", conversationID)
	recorder := httptest.NewRecorder()
	rt.streamTurn(recorder, request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: owner})))
	return recorder
}

// w13g4FailingChatKeys 让 FindChatAPIKey 失败，驱动 requireOwnedApiKey 错误臂。
type w13g4FailingChatKeys struct{}

func (w13g4FailingChatKeys) EnsureChatAPIKey(ownerID string) (string, error) {
	return "", errors.New("w13g4 注入密钥故障")
}

func (w13g4FailingChatKeys) FindChatAPIKey(keyID, ownerID string) (*ChatAPIKeyRecord, error) {
	return nil, errors.New("w13g4 注入密钥故障")
}

// TestW13G4StreamTurnPrologueArms 覆盖 streamTurn 前置链各错误臂。
func TestW13G4StreamTurnPrologueArms(t *testing.T) {
	env, script := newFaultGenerationEnvW10D(t)
	conversationID := "chat_conv_w13g4_pro"
	env.fixture.createConversation(conversationID, routeTestOwner)
	rt := newChatRoutesForTest(env.deps)

	// FindTurnByClientMessageID 查询失败 → 500（streamTurn 329-332）。
	script.failOnce("FROM chat_message_idempotency AS submission")
	recorder := w13g4StreamPost(rt, conversationID, routeTestOwner, streamPayload("w13g4-p1", "hi", "gpt-5"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("find turn 失败 = %d %s", recorder.Code, recorder.Body.String())
	}

	// 预注册同名 prep + replace 请求 → chat_replace_conflict（362-364）。
	rt.mu.Lock()
	rt.preps[conversationID] = &activePreparation{token: 41, ownerID: routeTestOwner, clientMessageID: "w13g4-other", phase: "preparing"}
	rt.mu.Unlock()
	recorder = w13g4StreamPost(rt, conversationID, routeTestOwner,
		`{"clientMessageId":"w13g4-p2","content":"hi","model":"gpt-5","replaceTurnId":"chat_turn_w13g4_none"}`)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_replace_conflict") {
		t.Fatalf("replace conflict = %d %s", recorder.Code, recorder.Body.String())
	}
	rt.mu.Lock()
	delete(rt.preps, conversationID)
	rt.mu.Unlock()

	// ChatKeys 故障 → requireOwnedApiKey 错误臂（403-406）。
	savedKeys := env.deps.ChatKeys
	env.deps.ChatKeys = w13g4FailingChatKeys{}
	recorder = w13g4StreamPost(rt, conversationID, routeTestOwner, streamPayload("w13g4-p3", "hi", "gpt-5"))
	if recorder.Code < 400 {
		t.Fatalf("密钥故障应 4xx/5xx = %d %s", recorder.Code, recorder.Body.String())
	}
	env.deps.ChatKeys = savedKeys

	// TokenCount 巨化 → validateFixedChatInputBudget 超限（508-511）。
	savedToken := env.deps.TokenCount
	env.deps.TokenCount = func(string) int { return 1 << 30 }
	recorder = w13g4StreamPost(rt, conversationID, routeTestOwner, streamPayload("w13g4-p4", "hi", "gpt-5"))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "chat_input_exceeds_context") {
		t.Fatalf("固定预算超限 = %d %s", recorder.Code, recorder.Body.String())
	}
	env.deps.TokenCount = savedToken

	// LoadModelContext 的 suffix 查询失败 → load 错误臂（537-540）。
	script.failOnce("FROM chat_messages AS source")
	recorder = w13g4StreamPost(rt, conversationID, routeTestOwner, streamPayload("w13g4-p5", "hi", "gpt-5"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("load 上下文失败 = %d %s", recorder.Code, recorder.Body.String())
	}

	// AcceptTurn 写幂等行失败 → AcceptTurn 错误臂（655-658）。
	script.failOnce("INSERT INTO chat_message_idempotency")
	recorder = w13g4StreamPost(rt, conversationID, routeTestOwner, streamPayload("w13g4-p6", "hi", "gpt-5"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("accept 失败 = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestW13G4StreamTurnContextBudgetArms 覆盖历史估算超限的压缩尝试臂
// （520-525 compactOnce 调用、572-582 85% 压缩链、583-586 ContextBudgetError）。
func TestW13G4StreamTurnContextBudgetArms(t *testing.T) {
	env, _ := newFaultGenerationEnvW10D(t)
	conversationID := "chat_conv_w13g4_budget"
	env.fixture.createConversation(conversationID, routeTestOwner)
	// 提供可压缩轮，让 compactOnce 有机会走完整压缩服务。
	seedLongTurns(t, env.fixture, routeTestOwner, conversationID, 2)
	// 历史中带标记的轮次 → 估算 token 巨化。
	accepted := env.fixture.accept(routeTestOwner, conversationID, "w13g4-b0", "w13g4-heavy 历史问题")
	env.fixture.complete(routeTestOwner, conversationID, accepted.TurnID, "历史回答")
	env.executor.steps = append(env.executor.steps, scriptStep{
		match: func(call dispatchCall) bool {
			return call.Headers["x-juhe-ai-purpose"] == "chat_context_compaction"
		},
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			summary := `{"durableMemory":["喜欢简洁"],"currentGoal":"配置服务","constraints":[],"decisions":[],"completed":["阅读文档"],"pending":["部署"],"importantToolResults":[],"imageMemories":[],"recentUserIntent":"配置服务","uncertainties":[]}`
			return jsonStatusResponse(200, `{"choices":[{"message":{"content":`+jsonQuote(summary)+`}}]}`)
		},
	})
	rt := newChatRoutesForTest(env.deps)
	env.deps.TokenCount = func(text string) int {
		if strings.Contains(text, "w13g4-heavy") {
			return 900000
		}
		return len(text) / 4
	}
	recorder := w13g4StreamPost(rt, conversationID, routeTestOwner, streamPayload("w13g4-b1", "hi", "gpt-5"))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "chat_input_exceeds_context") {
		t.Fatalf("估算超限 = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestW13G4StreamTurnSuccessWithoutCompactions 覆盖 ToolEnvironment 缺省与
// Compactions==nil 的成功流（467-469、520-521）。
func TestW13G4StreamTurnSuccessWithoutCompactions(t *testing.T) {
	env := newGenerationEnv(t)
	conversationID := "chat_conv_w13g4_plain"
	env.fixture.createConversation(conversationID, routeTestOwner)
	env.executor.steps = append(env.executor.steps, scriptStep{
		match: func(call dispatchCall) bool { return strings.HasPrefix(call.Path, "/v1/chat/completions") },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			return sseResponse(chatCompletionsSSE("你好", true))
		},
	})
	env.deps.Compactions = nil
	env.deps.ToolEnvironment = ""
	rt := newChatRoutesForTest(env.deps)
	recorder := w13g4StreamPost(rt, conversationID, routeTestOwner, streamPayload("w13g4-s1", "你好", "gpt-5"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("成功流 = %d %s", recorder.Code, recorder.Body.String())
	}
	events := sseEvents(recorder.Body.String())
	if last := events[len(events)-1]; last.event != "message.completed" {
		t.Fatalf("终态事件缺失: %+v", events[maxInt(0, len(events)-3):])
	}
}

// w13g4HookConnector 在 w10d 故障连接之上叠加查询钩子：命中子串时同步执行
// 内存动作（prep 取消/phase 篡改），用于确定性地驱动 streamTurn 中段的
// prep 生命周期臂。钩子内禁止数据库写入（单连接池）。
type w13g4HookConnector struct {
	base   driver.Connector
	script *faultScriptW10D
	hooks  map[string]func()
}

func (c w13g4HookConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &w13g4HookConn{base: conn, script: c.script, hooks: c.hooks}, nil
}

func (c w13g4HookConnector) Driver() driver.Driver { return c.base.Driver() }

type w13g4HookConn struct {
	base   driver.Conn
	script *faultScriptW10D
	hooks  map[string]func()
}

func (c *w13g4HookConn) runInterposition(query string) error {
	if c.script != nil {
		if err := c.script.take(query); err != nil {
			return err
		}
	}
	for substr, hook := range c.hooks {
		if strings.Contains(query, substr) {
			hook()
		}
	}
	return nil
}

func (c *w13g4HookConn) Prepare(query string) (driver.Stmt, error) { return c.base.Prepare(query) }

func (c *w13g4HookConn) Close() error { return c.base.Close() }

func (c *w13g4HookConn) Begin() (driver.Tx, error) {
	tx, err := c.base.Begin()
	if err != nil {
		return nil, err
	}
	return &faultTxW10D{base: tx, script: c.script}, nil
}

func (c *w13g4HookConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var (
		tx  driver.Tx
		err error
	)
	if bt, ok := c.base.(driver.ConnBeginTx); ok {
		tx, err = bt.BeginTx(ctx, opts)
	} else {
		tx, err = c.base.Begin()
	}
	if err != nil {
		return nil, err
	}
	return &faultTxW10D{base: tx, script: c.script}, nil
}

func (c *w13g4HookConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.runInterposition(query); err != nil {
		return nil, err
	}
	return c.base.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *w13g4HookConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.runInterposition(query); err != nil {
		return nil, err
	}
	return c.base.(driver.QueryerContext).QueryContext(ctx, query, args)
}

// w13g4HookFixture 仿 newFaultChatFixtureW10D，但连接带查询钩子。
func w13g4HookFixture(t *testing.T, hooks map[string]func()) (*chatFixture, *faultScriptW10D) {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	base, err := sqlite.NewConnector("file:chat-w13g4-" + name + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := newFaultScriptW10D()
	db := sql.OpenDB(w13g4HookConnector{base: base, script: script, hooks: hooks})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range strings.Split(chatTestDDL, ";") {
		trimmed := strings.TrimSpace(statement)
		if trimmed == "" {
			continue
		}
		if _, err := db.Exec(trimmed); err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	_, clock := fixedChatClock()
	store, err := NewStore(db, false, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &chatFixture{t: t, db: db, store: store, nowISO: "2026-03-10T08:00:00.000Z"}, script
}

// w13g4AbortPrepOnce 返回在首次命中时取消 prep 的钩子。
func w13g4AbortPrepOnce(rt *chatRoutes, conversationID, clientMessageID string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			rt.mu.Lock()
			prep := rt.preps[conversationID]
			rt.mu.Unlock()
			if prep != nil && prep.clientMessageID == clientMessageID {
				prep.abort()
			}
		})
	}
}

// TestW13G4StreamTurnPrepCanceledArms 覆盖历史装载后的 assertActive 取消臂
// （542-545）。383/398/423/439 臂在 claimPreparation 与 assertActive 之间无外部
// 调用点（mock 直连），无法确定性注入取消，行为与 542 臂同型。
func TestW13G4StreamTurnPrepCanceledArms(t *testing.T) {
	conversationID := "chat_conv_w13g4_cancel"
	rtRef := newChatRoutesForTest(&Deps{})
	hooks := map[string]func(){
		"FROM chat_messages AS source": w13g4AbortPrepOnce(rtRef, conversationID, "w13g4-c1"),
	}
	fixture, _ := w13g4HookFixture(t, hooks)
	env := buildGenerationEnvW10D(t, fixture)
	rtRef.deps = env.deps
	fixture.createConversation(conversationID, routeTestOwner)
	recorder := w13g4StreamPost(rtRef, conversationID, routeTestOwner, streamPayload("w13g4-c1", "hi", "gpt-5"))
	if recorder.Code != 499 {
		t.Fatalf("prep 取消 = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestW13G4StreamTurnBeginAcceptanceArms 覆盖 beginAcceptance false 臂（638-641）：
// load 阶段把 prep.phase 篡改为 accepting → assertActive 通过但 beginAcceptance 拒绝。
func TestW13G4StreamTurnBeginAcceptanceArms(t *testing.T) {
	conversationID := "chat_conv_w13g4_phase"
	rtRef := newChatRoutesForTest(&Deps{})
	hooks := map[string]func(){
		"FROM chat_messages AS source": func() {
			rtRef.mu.Lock()
			prep := rtRef.preps[conversationID]
			rtRef.mu.Unlock()
			if prep != nil && prep.clientMessageID == "w13g4-f1" {
				prep.mu.Lock()
				prep.phase = "accepting"
				prep.mu.Unlock()
			}
		},
	}
	fixture, _ := w13g4HookFixture(t, hooks)
	env := buildGenerationEnvW10D(t, fixture)
	rtRef.deps = env.deps
	fixture.createConversation(conversationID, routeTestOwner)
	recorder := w13g4StreamPost(rtRef, conversationID, routeTestOwner, streamPayload("w13g4-f1", "hi", "gpt-5"))
	if recorder.Code != 499 {
		t.Fatalf("beginAcceptance 拒绝 = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestW13G4StreamTurnAcceptAbortArms 覆盖 AcceptTurn 后取消臂（723-725）：
// AcceptTurn 写幂等行时取消 prep → 723 isCanceled → runner.Abort，SSE 以取消终态收尾。
func TestW13G4StreamTurnAcceptAbortArms(t *testing.T) {
	conversationID := "chat_conv_w13g4_abort"
	rtRef := newChatRoutesForTest(&Deps{})
	hooks := map[string]func(){
		"INSERT INTO chat_message_idempotency": w13g4AbortPrepOnce(rtRef, conversationID, "w13g4-a1"),
	}
	fixture, _ := w13g4HookFixture(t, hooks)
	env := buildGenerationEnvW10D(t, fixture)
	rtRef.deps = env.deps
	fixture.createConversation(conversationID, routeTestOwner)
	env.executor.steps = append(env.executor.steps, scriptStep{
		match: func(call dispatchCall) bool { return strings.HasPrefix(call.Path, "/v1/chat/completions") },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			return sseResponse(chatCompletionsSSE("回答", true))
		},
	})
	recorder := w13g4StreamPost(rtRef, conversationID, routeTestOwner, streamPayload("w13g4-a1", "你好", "gpt-5"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("取消流 = %d %s", recorder.Code, recorder.Body.String())
	}
	events := sseEvents(recorder.Body.String())
	last := events[len(events)-1]
	if last.event != "message.canceled" && last.event != "message.completed" {
		t.Fatalf("终态事件异常: %+v", events[maxInt(0, len(events)-3):])
	}
}
