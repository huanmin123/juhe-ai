package chat

// w14l 覆盖率收尾（二）：暂停式故障驱动下的流式路由预备取消矩阵、重复提交
// 臂、SSE 解析臂、压缩服务重试延迟/认领钳制臂与检查点安装校验臂。
//
// w14l 残余登记（solo 波次结束时仍未覆盖、且在单进程 sqlite 测试装置下
// 不可达或需多进程/真实并发才能构造的主要类别）：
//   - turns.go conditionalStop 的 messageResult/conversationResult affected != 1
//     分支（1048-1077）：需要同一事务判定与 UPDATE 之间被并发修改，单连接
//     装置无法构造。
//   - routes.go clearConversation 的 claim==nil / cleared==nil 竞态臂（792/806）。
//   - compaction_service.go passiveDelayISO 的 half<windowMs（band1 下界互斥）、
//     delay<1 钳制、earlierTime 错误臂、RecordCompactionProgress 竞态 !progressed。
//   - stream_route.go 746/747/750（SSE 心跳与 End 需要真实断连）、838/845
//     （图片观察需要真实图片资产观察端口）。
//   - generation_images / assets.go commitChatAssetsToMessage 的部分臂需要
//     真实图片生成工具调用往返。
//   - postgres_partitions.go 全部为 pg 方言分支（sqlite 装置不执行）。

import (
	"context"
	"runtime"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	sqlite "modernc.org/sqlite"
)

// --- 暂停式连接层：命中子串后挂起，等待测试放行 ---

type w14lHold struct {
	mu       sync.Mutex
	query    string
	hits     int
	armed    bool
	canceled bool
	nullAt   int
	ready    chan int
	release  chan struct{}
}

func newW14LHold(query string) *w14lHold {
	return &w14lHold{query: query, ready: make(chan int, 64), release: make(chan struct{}, 64)}
}

// nullAtK 让第 k 次命中查询返回整行 NULL 行集（Scan 类型失败臂）。
func (h *w14lHold) nullAtK() *w14lHold {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nullAt = -1
	return h
}

// hitK 返回当前命中次数。
func (h *w14lHold) hitK() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits
}

// arm 在 fixture seed 完成后启用拦截。
func (h *w14lHold) arm() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.armed = true
}

// cancelNow 标记已取消：此后所有命中直接放行。
func (h *w14lHold) cancelNow() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.canceled = true
}

// waitHit 等待第 target 次命中；返回后请求仍处于挂起状态。
func (h *w14lHold) waitHit(t *testing.T, target int) {
	t.Helper()
	for i := 1; i <= target; i++ {
		select {
		case <-h.ready:
			if i < target {
				h.release <- struct{}{}
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("等待第 %d 次命中超时", i)
		}
	}
}

func (h *w14lHold) hitCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits
}

func (h *w14lHold) hitsSoFar() int { return h.hitCount() }

func (h *w14lHold) letGo() { h.release <- struct{}{} }

func (h *w14lHold) intercept(query string) (block bool) {
	h.mu.Lock()
	armed, canceled, nullAt := h.armed, h.canceled, h.nullAt
	h.mu.Unlock()
	if !armed || canceled || !strings.Contains(query, h.query) {
		return false
	}
	h.mu.Lock()
	h.hits++
	hit := h.hits
	h.mu.Unlock()
	if nullAt != 0 {
		// NULL 行集模式：第 k 次命中返回整行 NULL，其余放行。
		return hit == -nullAt
	}
	// 暂停模式：每次命中挂起，等待测试逐次放行。
	h.ready <- hit
	<-h.release
	return false
}

type w14lHoldConnector struct {
	base   driver.Connector
	hold   *w14lHold
	script *faultScriptW10D
}

func (c w14lHoldConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &w14lHoldConn{base: conn, hold: c.hold, script: c.script}, nil
}

func (c w14lHoldConnector) Driver() driver.Driver { return c.base.Driver() }

type w14lHoldConn struct {
	base   driver.Conn
	hold   *w14lHold
	script *faultScriptW10D
}

func (c *w14lHoldConn) Prepare(query string) (driver.Stmt, error) { return c.base.Prepare(query) }
func (c *w14lHoldConn) Close() error                              { return c.base.Close() }

func (c *w14lHoldConn) Begin() (driver.Tx, error) { return c.base.Begin() }

func (c *w14lHoldConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bt, ok := c.base.(driver.ConnBeginTx); ok {
		return bt.BeginTx(ctx, opts)
	}
	return c.base.Begin()
}

func (c *w14lHoldConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.script.take(query); err != nil {
		return nil, err
	}
	return c.base.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *w14lHoldConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.script.take(query); err != nil {
		return nil, err
	}
	if c.hold.intercept(query) {
		return &w14lNullRows{}, nil
	}
	return c.base.(driver.QueryerContext).QueryContext(ctx, query, args)
}

// w14lNullRows 返回一行全 NULL 再 EOF：非可空目标的 Scan 必然失败。
type w14lNullRows struct{ sent bool }

func (r *w14lNullRows) Columns() []string { return []string{"w14l_c1"} }
func (r *w14lNullRows) Close() error      { return nil }
func (r *w14lNullRows) Next(dest []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	r.sent = true
	for i := range dest {
		dest[i] = nil
	}
	return nil
}

func newW14LHoldFixture(t *testing.T, hold *w14lHold) (*chatFixture, *faultScriptW10D) {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	base, err := sqlite.NewConnector("file:chat-w14l-hold-" + name + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := newFaultScriptW10D()
	db := sql.OpenDB(w14lHoldConnector{base: base, hold: hold, script: script})
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

func w14lStreamPostRaw(rt *chatRoutes, conversationID, payload, owner string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", "/conversations/"+conversationID+"/stream", strings.NewReader(payload))
	request.SetPathValue("conversationId", conversationID)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: owner, Username: owner, DisplayName: owner, Role: "user"}))
	recorder := httptest.NewRecorder()
	rt.streamTurn(recorder, request)
	return recorder
}

// TestW14LPreparationCanceledMatrix 逐点暂停存储查询，再取消预备，命中
// 各个 assertActive 失败臂（402-560 区段）。
func TestW14LPreparationCanceledMatrix(t *testing.T) {
	for cancelAfter := 1; cancelAfter <= 4; cancelAfter++ {
		cancelAfter := cancelAfter
		t.Run(fmt.Sprintf("cancel%02d", cancelAfter), func(t *testing.T) {
			hold := newW14LHold("FROM chat_messages")
			fixture, _ := newW14LHoldFixture(t, hold)
			conversationID := fmt.Sprintf("chat_conv_w14l_hold_%d", cancelAfter)
			fixture.createConversation(conversationID, routeTestOwner)
			// 预置一条已完成轮次供 replace 校验通过。
			accepted := fixture.accept(routeTestOwner, conversationID, "w14l-hold-seed", "旧问题")
			fixture.complete(routeTestOwner, conversationID, accepted.TurnID, "旧回答")
			env := buildGenerationEnvW10D(t, fixture)
			rt := newChatRoutesForTest(env.deps)
			hold.arm()

			payload := `{"clientMessageId":"w14l-hold-cmid","content":"新问题","model":"gpt-5","replaceTurnId":"` + accepted.TurnID + `"}`
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- w14lStreamPostRaw(rt, conversationID, payload, routeTestOwner)
			}()
			// 依次放行前 cancelAfter-1 次命中，在第 cancelAfter 次命中挂起时取消预备。
			hold.waitHit(t, cancelAfter)
			phase, canceled := rt.cancelPreparation(conversationID, routeTestOwner, "w14l-hold-cmid")
			if !canceled {
				t.Fatal("取消预备失败")
			}
			t.Logf("canceled prep phase=%q hits=%d", phase, hold.hitsSoFar())
			hold.cancelNow()
			hold.letGo()
			if phase != "preparing" {
				// 接受阶段之后的取消不再使请求失败：运行器自行收口。
				<-done
				return
			}
			var recorder *httptest.ResponseRecorder
			select {
			case recorder = <-done:
			case <-time.After(30 * time.Second):
				buf := make([]byte, 1<<16)
				n := runtime.Stack(buf, true)
				t.Fatalf("流式请求未返回\n%s", buf[:n])
			}
			if !strings.Contains(recorder.Body.String(), "preparation") && recorder.Code < 400 {
				t.Fatalf("取消后应失败：%d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// TestW14LStreamDuplicateAccept 覆盖重复 clientMessageId 的冲突臂。
func TestW14LStreamDuplicateAccept(t *testing.T) {
	fixture := newChatFixture(t)
	conversationID := "chat_conv_w14l_dup"
	fixture.createConversation(conversationID, routeTestOwner)
	env := buildGenerationEnvW10D(t, fixture)
	rt := newChatRoutesForTest(env.deps)
	payload := streamPayload("w14l-dup-cmid", "问题", "gpt-5")
	if recorder := w14lStreamPostRaw(rt, conversationID, payload, routeTestOwner); recorder.Code >= 500 {
		t.Fatalf("首次提交 = %d %s", recorder.Code, recorder.Body.String())
	}
	// 等待运行器收口（活跃轮清空）后再以同一 cmid 提交。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var active sql.NullString
		if err := fixture.db.QueryRow(`SELECT active_turn_id FROM chat_conversations WHERE id = ?`, conversationID).Scan(&active); err == nil && !active.Valid {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	recorder := w14lStreamPostRaw(rt, conversationID, payload, routeTestOwner)
	if !strings.Contains(recorder.Body.String(), "chat_message_already_exists") {
		t.Fatalf("重复提交 = %d %s", recorder.Code, recorder.Body.String())
	}
}

// --- SSE 解析臂 ---

// TestW14LCollectOpenAIChatSseArms 覆盖网关 SSE 聚合的边界臂。
func TestW14LCollectOpenAIChatSseArms(t *testing.T) {
	// 无 data 行的事件被忽略。
	if _, err := CollectOpenAIChatSse(strings.NewReader("event: ping\n\n"), 1024, nil, 8); err == nil {
		t.Fatal("缺少 [DONE] 应报错")
	}
	// 内容超限。
	stream := "data: " + `{"choices":[{"delta":{"content":"` + strings.Repeat("字", 700) + `"}}]}` + "\n\ndata: [DONE]\n\n"
	if _, err := CollectOpenAIChatSse(strings.NewReader(stream), 10, nil, 8); err == nil {
		t.Fatal("内容超限应报错")
	}
	// 工具参数超限。
	args := strings.Repeat("a", 70*1024)
	toolStream := "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"f","arguments":"` + args + `"}}]}}]}` + "\n\ndata: [DONE]\n\n"
	if _, err := CollectOpenAIChatSse(strings.NewReader(toolStream), 1024, nil, 8); err == nil {
		t.Fatal("工具参数超限应报错")
	}
	// 事件数超限。
	if _, err := CollectOpenAIChatSse(strings.NewReader("data: {}\n\ndata: {}\n\ndata: [DONE]\n\n"), 1024, nil, 1); err == nil {
		t.Fatal("事件数超限应报错")
	}
	// 尾部残留缓冲 + 工具乱序聚合。
	messy := "data: {bad-json}\n\n" +
		"data: " + `{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"c","function":{"name":"n","arguments":"{}"}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"m","arguments":"{}"}}]}}]}` + "\n\n" +
		"trailing-garbage"
	result, err := CollectOpenAIChatSse(strings.NewReader(messy), 1024, nil, 32)
	if err == nil {
		t.Fatal("坏 JSON 事件应报错")
	}
	_ = result
	ordered := "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"c","function":{"name":"n","arguments":"{}"}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"m","arguments":"{}"}}]}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	orderedResult, err := CollectOpenAIChatSse(strings.NewReader(ordered), 1024, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(orderedResult.ToolCalls) != 2 || orderedResult.ToolCalls[0].CallID != "a" {
		t.Fatalf("工具应按 index 排序：%+v", orderedResult.ToolCalls)
	}
}

// --- 压缩服务单元臂 ---

// TestW14LPassiveDelayAndFailClaim 覆盖重试延迟窗口与认领失败钳制。
func TestW14LPassiveDelayAndFailClaim(t *testing.T) {
	_, clock := fixedChatClock()
	service := NewCompactionService(nil, nil, nil, nil)
	service.WallClock = clock
	sampled := 0.0
	service.Random = func() float64 { return sampled }
	for _, interval := range []int64{1, 50000, 120000, 4000000, 5 * 86400000, 10 * 86400000} {
		delay := service.passiveDelayISO(interval)
		if delay == "" {
			t.Fatalf("interval %d 应产出延迟", interval)
		}
	}
	// failClaim：attempt 钳制臂（直接构造认领）。
	fixture := newChatFixture(t)
	fixture.createConversation("chat_conv_w14l_fc", routeTestOwner)
	committed := NewCompactionService(fixture.store, &mockExecutor{}, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
	for _, attempt := range []int{0, 31} {
		result := committed.failClaim(CompactionInput{ConversationID: "chat_conv_w14l_fc", SystemAccountID: routeTestOwner},
			&ContextCompactionClaim{ClaimID: "claim-w14l", AttemptCount: int64(attempt)}, errors.New("w14l boom"))
		if result.Status != "failed" {
			t.Fatalf("failClaim 应返回 failed：%+v", result)
		}
	}
}

// TestW14LCheckpointInstallValidation 覆盖检查点安装的载荷与来源消息校验。
func TestW14LCheckpointInstallValidation(t *testing.T) {
	fixture := newChatFixture(t)
	conversationID := "chat_conv_w14l_ckpt"
	fixture.createConversation(conversationID, routeTestOwner)
	fixture.seedTurns(routeTestOwner, conversationID, 3)
	head, err := fixture.store.GetContextHead(conversationID, routeTestOwner)
	if err != nil || head == nil {
		t.Fatalf("head = %+v / %v", head, err)
	}
	claim, err := fixture.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: conversationID, SystemAccountID: routeTestOwner,
		ExpectedRevision: head.ContextRevision, SourceThroughSequence: head.NextSequenceNo - 3,
		Now: fixture.nowISO, StaleClaimBefore: fixture.nowISO,
	})
	if err != nil || claim == nil {
		t.Fatalf("认领 = %+v / %v", claim, err)
	}
	// 巨型载荷 → 超限臂。
	huge := jsonRaw(`{"text":"` + strings.Repeat("占", maxCheckpointPayloadByte/2) + `"}`)
	_, err = fixture.store.InstallContextCheckpoint(InstallCheckpointInput{
		ClaimID: claim.ClaimID, ConversationID: conversationID, SystemAccountID: routeTestOwner,
		SourceRevision: head.ContextRevision, SourceThroughSequence: 2,
		ExpiresAt: "2026-03-11T08:00:00.000Z", PayloadDigest: digest64("cd"),
		Entries:     []CheckpointEntryInput{{Kind: "task_state", Content: huge, Provenance: "assistant", TrustLevel: "assistant_derived"}},
		Now:         fixture.nowISO,
	})
	if err == nil {
		t.Fatal("巨型载荷应拒绝")
	}
	// 非法来源消息 ID → 校验臂（条目级 SourceMessageID）。
	badEntry := installInputW14C(fixture, conversationID, claim, head.ContextRevision)
	badEntry.Entries = []CheckpointEntryInput{{SourceMessageID: "bad\x01id", Kind: "task_state", Content: jsonRaw(`{}`), Provenance: "assistant", TrustLevel: "assistant_derived"}}
	if _, err := fixture.store.InstallContextCheckpoint(badEntry); err == nil {
		t.Fatal("非法来源消息 ID 应拒绝")
	}
}

var _ = io.Discard
var _ = driver.ErrSkip
