package main

// w1: chain_chat_observation.go 单测。图片语义观察适配器（Node
// chat-image-observation.ts 移植）：纯解析函数、SQLite claim/settle 状态机、
// runObservation 全流程（ObjectStore/Executor 均为内存 stub，可回放）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

// w1FakeObjectStore 是 chat.ObjectStore 的内存 stub。
type w1FakeObjectStore struct{ data map[string][]byte }

func (s *w1FakeObjectStore) Write(string, []byte, int64, string) error { return nil }
func (s *w1FakeObjectStore) Delete([]string) error                     { return nil }
func (s *w1FakeObjectStore) Open(key string, maxBytes int64) ([]byte, int64, error) {
	data, ok := s.data[key]
	if !ok {
		return nil, 0, errors.New("对象不存在")
	}
	if int64(len(data)) > maxBytes {
		return nil, 0, errors.New("超出大小上限")
	}
	return data, int64(len(data)), nil
}

// w1FakeExecutor 是 chat.GenerationExecutor 的可编程 stub。
type w1FakeExecutor struct {
	status   int
	body     string
	err      error
	got      *chat.GenerationDispatchRequest
	notified chan struct{}
}

func (e *w1FakeExecutor) Dispatch(_ context.Context, req chat.GenerationDispatchRequest) (*chat.GenerationDispatchResponse, error) {
	e.got = &req
	if e.notified != nil {
		select {
		case <-e.notified:
		default:
			close(e.notified)
		}
	}
	if e.err != nil {
		return nil, e.err
	}
	return &chat.GenerationDispatchResponse{Status: e.status, Body: io.NopCloser(strings.NewReader(e.body))}, nil
}

func TestW1ChatResponsesOutputText(t *testing.T) {
	if got := chatResponsesOutputText(map[string]any{"output_text": "直接文本"}); got != "直接文本" {
		t.Fatalf("output_text 分支 = %q", got)
	}
	nested := map[string]any{"output": []any{
		map[string]any{"content": []any{
			map[string]any{"text": "第一段"},
			map[string]any{"text": ""},
			map[string]any{"text": "第二段"},
			"非对象元素",
		}},
		"非对象条目",
	}}
	if got := chatResponsesOutputText(nested); got != "第一段\n第二段" {
		t.Fatalf("output 数组分支 = %q，want 第一段\n第二段", got)
	}
	if got := chatResponsesOutputText(map[string]any{}); got != "" {
		t.Fatalf("空 payload = %q，want 空串", got)
	}
}

func TestW1ParseChatImageObservation(t *testing.T) {
	observation, err := parseChatImageObservation("```json\n{\"summary\":\"图片里有猫\",\"ocr\":[\"文字\"],\"objects\":[\"猫\"]}\n```")
	if err != nil {
		t.Fatalf("解析围栏 JSON: %v", err)
	}
	if observation["summary"] != "图片里有猫" {
		t.Fatalf("summary = %v", observation["summary"])
	}
	if got := observation["ocr"].([]string); len(got) != 1 || got[0] != "文字" {
		t.Fatalf("ocr = %v", got)
	}
	// 非 JSON 文本回落为 summary。
	observation, err = parseChatImageObservation("  纯文本说明  ")
	if err != nil || observation["summary"] != "纯文本说明" {
		t.Fatalf("纯文本回落 = %v, %v", observation, err)
	}
	// JSON 数组（非对象）也回落 summary。
	observation, err = parseChatImageObservation("[1,2]")
	if err != nil || observation["summary"] != "[1,2]" {
		t.Fatalf("数组回落 = %v, %v", observation, err)
	}
	// 空 summary 报错。
	if _, err := parseChatImageObservation(`{"summary":"  "}`); err == nil || err.Error() != "chat_image_observation_empty" {
		t.Fatalf("空 summary 错误 = %v", err)
	}
	// 字段截断：summary 超 12000 rune 截断；数组项超 4000 截断、空项剔除、上限 100。
	long := strings.Repeat("长", 12_500)
	observation, err = parseChatImageObservation(`{"summary":"` + long + `"}`)
	if err != nil {
		t.Fatalf("长 summary: %v", err)
	}
	if got := len([]rune(observation["summary"].(string))); got != 12_000 {
		t.Fatalf("summary 截断长度 = %d，want 12000", got)
	}
	items := []string{}
	for i := 0; i < 150; i++ {
		items = append(items, `"项"`)
	}
	observation, err = parseChatImageObservation(`{"summary":"s","ocr":[` + strings.Join(items, ",") + `]}`)
	if err != nil {
		t.Fatalf("长数组: %v", err)
	}
	if got := len(observation["ocr"].([]string)); got != 100 {
		t.Fatalf("ocr 上限 = %d，want 100", got)
	}
}

func TestW1ChatObservationTextHelpers(t *testing.T) {
	if got := chatObservationText("  文本 ", 10); got != "文本" {
		t.Fatalf("trim = %q", got)
	}
	if got := chatObservationText(42, 10); got != "" {
		t.Fatalf("非字符串 = %q", got)
	}
	if got := chatObservationTextArray("不是数组"); len(got) != 0 {
		t.Fatalf("非数组 = %v", got)
	}
	if got := chatObservationTextArray([]any{" a ", "", 3}); len(got) != 1 || got[0] != "a" {
		t.Fatalf("数组过滤 = %v", got)
	}
	if got := truncateChatObservationText("abcdef", 3); got != "abc" {
		t.Fatalf("截断 = %q", got)
	}
	if got := truncateChatObservationText("ab", 3); got != "ab" {
		t.Fatalf("未超限 = %q", got)
	}
	if got := chatObservationJSONString(`带"引号`); got != "\"带\\\"引号\"" {
		t.Fatalf("JSON 编码 = %q", got)
	}
}

func TestW1ChatAssetHelpers(t *testing.T) {
	if _, _, err := chatAssetObjectRead(nil, "key", 100); err == nil {
		t.Fatal("nil store 必须报未接线错误")
	}
	data := []byte{1, 2, 3}
	if got := chatAssetSHA256Hex(data); len(got) != 64 {
		t.Fatalf("sha256 hex 长度 = %d", len(got))
	}
	if chatAssetSHA256Hex(data) != chatAssetSHA256Hex([]byte{1, 2, 3}) {
		t.Fatal("sha256 必须确定性")
	}
	if got := chatAssetBase64([]byte{1, 2, 3}); got != "AQID" {
		t.Fatalf("base64 = %q", got)
	}
}

// w1ObservationFixture 建观察状态机所需的最小 SQLite 表与适配器实例。
type w1ObservationFixture struct {
	db       *sql.DB
	objects  *w1FakeObjectStore
	executor *w1FakeExecutor
	obs      *chatImageObservations
}

const w1Future = "2099-01-01T00:00:00.000Z"

func newW1ObservationFixture(t *testing.T) *w1ObservationFixture {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/chat.sqlite3")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE chat_assets (
			id TEXT PRIMARY KEY, system_account_id TEXT, conversation_id TEXT,
			observation_status TEXT, observation_json TEXT, observation_revision INTEGER NOT NULL DEFAULT 0,
			observation_claim_id TEXT, observation_claimed_at TEXT,
			processing_status TEXT, cleanup_status TEXT, expires_at TEXT, updated_at TEXT,
			storage_key TEXT, processed_mime_type TEXT, processed_bytes INTEGER,
			processed_sha256 TEXT, source_kind TEXT)`,
		`CREATE TABLE chat_asset_references (
			asset_id TEXT, conversation_id TEXT, turn_id TEXT, message_id TEXT,
			reference_kind TEXT, expires_at TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed schema: %v", err)
		}
	}
	objects := &w1FakeObjectStore{data: map[string][]byte{}}
	executor := &w1FakeExecutor{status: 200}
	obs := newChatImageObservations(db, false, objects, executor)
	return &w1ObservationFixture{db: db, objects: objects, executor: executor, obs: obs}
}

// seedReadyAsset 插入一张可观察的 ready 资产与其用户引用。
func (f *w1ObservationFixture) seedReadyAsset(t *testing.T, assetID string) {
	t.Helper()
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	f.objects.data["key_"+assetID] = png
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := f.db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO chat_assets (id, system_account_id, conversation_id, observation_status,
		observation_revision, processing_status, cleanup_status, expires_at, updated_at,
		storage_key, processed_mime_type, processed_bytes, processed_sha256, source_kind)
		VALUES (?, 'sys_1', 'conv_1', 'not_requested', 0, 'ready', 'active', ?, '2026-01-01T00:00:00.000Z',
		?, 'image/png', ?, ?, 'user_upload')`,
		assetID, w1Future, "key_"+assetID, len(png), chatAssetSHA256Hex(png))
	seed(`INSERT INTO chat_asset_references (asset_id, conversation_id, turn_id, message_id, reference_kind, expires_at)
		VALUES (?, 'conv_1', 'turn_1', 'msg_1', 'user_input', ?)`, assetID, w1Future)
}

func (f *w1ObservationFixture) assetRow(t *testing.T, assetID string) (status string, observationJSON sql.NullString, revision int64) {
	t.Helper()
	if err := f.db.QueryRow(`SELECT observation_status, observation_json, observation_revision
		FROM chat_assets WHERE id = ?`, assetID).Scan(&status, &observationJSON, &revision); err != nil {
		t.Fatalf("read asset: %v", err)
	}
	return status, observationJSON, revision
}

func w1ScheduleInput() chat.ScheduleObservationInput {
	return chat.ScheduleObservationInput{
		ConversationID:   "conv_1",
		SystemAccountID:  "sys_1",
		APIKeySecret:     "sk-obs",
		Model:            "gpt-test",
		UserContent:      "图片里有什么",
		AssistantContent: "我看看",
	}
}

func TestW1ObservationTableAndBind(t *testing.T) {
	sqlite := newChatImageObservations(nil, false, nil, nil)
	if got := sqlite.table("chat_assets"); got != "chat_assets" {
		t.Fatalf("sqlite table = %q", got)
	}
	if got := sqlite.bind("UPDATE t SET a = ? WHERE b = ?"); got != "UPDATE t SET a = ? WHERE b = ?" {
		t.Fatalf("sqlite bind = %q", got)
	}
	pg := newChatImageObservations(nil, true, nil, nil)
	if got := pg.table("chat_assets"); got != "juhe_chat.chat_assets" {
		t.Fatalf("pg table = %q", got)
	}
	if got := pg.bind("UPDATE t SET a = ? WHERE b = ? OR c = ?"); got != "UPDATE t SET a = $1 WHERE b = $2 OR c = $3" {
		t.Fatalf("pg bind = %q", got)
	}
}

func TestW1ObservationClaimAndSettle(t *testing.T) {
	f := newW1ObservationFixture(t)
	f.seedReadyAsset(t, "asset_ok")
	ctx := context.Background()
	now := "2026-09-10T00:00:00.000Z"

	// now 格式错误 fail fast。
	if _, err := f.obs.claim(ctx, "asset_ok", "conv_1", "sys_1", "turn_1", "msg_1", "not-a-time"); err == nil {
		t.Fatal("非法 now 必须报错")
	}
	claim, err := f.obs.claim(ctx, "asset_ok", "conv_1", "sys_1", "turn_1", "msg_1", now)
	if err != nil || claim == nil {
		t.Fatalf("claim = %v, %v", claim, err)
	}
	if claim.observationRevision != 1 || claim.claimID == "" {
		t.Fatalf("claim = %+v", claim)
	}
	// 引用不存在：claim 落空。
	f.seedReadyAsset(t, "asset_noref")
	if _, err := f.db.Exec(`DELETE FROM chat_asset_references WHERE asset_id = 'asset_noref'`); err != nil {
		t.Fatalf("delete ref: %v", err)
	}
	claim2, err := f.obs.claim(ctx, "asset_noref", "conv_1", "sys_1", "turn_1", "msg_1", now)
	if err != nil || claim2 != nil {
		t.Fatalf("无引用 claim = %v, %v，want nil/nil", claim2, err)
	}
	// settled：ready + 正确 revision + claimID。
	ok, err := f.obs.setObservation(ctx, "asset_ok", "conv_1", "sys_1", "ready",
		map[string]any{"summary": "图片里有猫"}, claim.observationRevision, claim.claimID, now)
	if err != nil || !ok {
		t.Fatalf("setObservation = %v, %v", ok, err)
	}
	status, observationJSON, _ := f.assetRow(t, "asset_ok")
	if status != "ready" || !strings.Contains(observationJSON.String, "图片里有猫") {
		t.Fatalf("settled row = %q %q", status, observationJSON.String)
	}
	// 同一 claim 二次提交：claim 已清空 → false（提交冲突）。
	ok, err = f.obs.setObservation(ctx, "asset_ok", "conv_1", "sys_1", "ready",
		map[string]any{"summary": "重复"}, claim.observationRevision, claim.claimID, now)
	if err != nil || ok {
		t.Fatalf("重复 setObservation = %v, %v，want false/nil", ok, err)
	}
	// ready 必须带 observation。
	if _, err := f.obs.setObservation(ctx, "asset_noref", "conv_1", "sys_1", "ready", nil, 9, "claim", now); err == nil {
		t.Fatal("ready 无 observation 必须报错")
	}
	// observation 超出 64KiB 上限报错。
	huge := map[string]any{"summary": strings.Repeat("长", 40_000)}
	if _, err := f.obs.setObservation(ctx, "asset_noref", "conv_1", "sys_1", "ready", huge, 9, "claim", now); err == nil {
		t.Fatal("超大 observation 必须报错")
	}
}

func TestW1RunObservationSuccessFlow(t *testing.T) {
	f := newW1ObservationFixture(t)
	f.seedReadyAsset(t, "asset_run")
	f.executor.body = `{"output_text":"{\"summary\":\"图片里有猫\",\"ocr\":[\"文字\"],\"uncertainties\":[]}"}`
	input := w1ScheduleInput()
	target := chat.ObservationTarget{AssetID: "asset_run", ExpectedTurnID: "turn_1", ExpectedMessageID: "msg_1"}
	if err := f.obs.runObservation(context.Background(), input, target); err != nil {
		t.Fatalf("runObservation: %v", err)
	}
	if f.executor.got == nil {
		t.Fatal("executor 未被调用")
	}
	if f.executor.got.Path != "/v1/responses" || f.executor.got.Method != "POST" {
		t.Fatalf("dispatch target = %s %s", f.executor.got.Method, f.executor.got.Path)
	}
	if f.executor.got.Headers["authorization"] != "Bearer sk-obs" {
		t.Fatalf("authorization = %q", f.executor.got.Headers["authorization"])
	}
	var body map[string]any
	if err := json.Unmarshal(f.executor.got.Body, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body["model"] != "gpt-test" || body["stream"] != false {
		t.Fatalf("request body = %v", body)
	}
	status, observationJSON, _ := f.assetRow(t, "asset_run")
	if status != "ready" || !strings.Contains(observationJSON.String, "图片里有猫") {
		t.Fatalf("settled = %q %q", status, observationJSON.String)
	}
}

func TestW1RunObservationFailureFlows(t *testing.T) {
	ctx := context.Background()
	input := w1ScheduleInput()
	target := chat.ObservationTarget{AssetID: "asset_x", ExpectedTurnID: "turn_1", ExpectedMessageID: "msg_1"}

	// 引用缺失：claim 落空直接返回 nil。
	missingRef := newW1ObservationFixture(t)
	if err := missingRef.obs.runObservation(ctx, input, target); err != nil {
		t.Fatalf("无引用 runObservation = %v，want nil", err)
	}

	// 存储对象缺失（claim 成功但 dataURL 重建失败）→ failed。
	noData := newW1ObservationFixture(t)
	noData.seedReadyAsset(t, "asset_x")
	delete(noData.objects.data, "key_asset_x")
	if err := noData.obs.runObservation(ctx, input, target); err == nil {
		t.Fatal("存储对象缺失必须失败")
	}
	if status, _, _ := noData.assetRow(t, "asset_x"); status != "failed" {
		t.Fatalf("失败后状态 = %q，want failed", status)
	}

	// executor 错误 / 非 2xx / 空 summary / 超大 payload 均落 failed。
	cases := []struct {
		name     string
		executor w1FakeExecutor
	}{
		{"dispatch 错误", w1FakeExecutor{err: errors.New("网络故障")}},
		{"http 500", w1FakeExecutor{status: 500, body: "{}"}},
		{"空 summary", w1FakeExecutor{status: 200, body: `{"output_text":"{\"summary\":\"\"}"}`}},
		{"空 output 文本", w1FakeExecutor{status: 200, body: `{"output_text":""}`}},
		{"超大 payload", w1FakeExecutor{status: 200, body: `{"output_text":"` + strings.Repeat("x", 130*1024) + `"}"`}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newW1ObservationFixture(t)
			f.seedReadyAsset(t, "asset_x")
			*f.executor = testCase.executor
			if err := f.obs.runObservation(ctx, input, target); err == nil {
				t.Fatal("该场景必须失败")
			}
			if status, _, _ := f.assetRow(t, "asset_x"); status != "failed" {
				t.Fatalf("失败后状态 = %q，want failed", status)
			}
		})
	}
}

func TestW1BuildAssetDataURL(t *testing.T) {
	f := newW1ObservationFixture(t)
	f.seedReadyAsset(t, "asset_du")
	ctx := context.Background()
	now := "2026-09-10T00:00:00.000Z"
	// 资产不存在：空串无错误。
	got, err := f.obs.buildAssetDataURL(ctx, "missing", "conv_1", "sys_1", now)
	if err != nil || got != "" {
		t.Fatalf("缺失资产 = %q, %v", got, err)
	}
	got, err = f.obs.buildAssetDataURL(ctx, "asset_du", "conv_1", "sys_1", now)
	if err != nil || !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("dataURL = %q, %v", got, err)
	}
	// 字节数不匹配。
	if _, err := f.db.Exec(`UPDATE chat_assets SET processed_bytes = 99 WHERE id = 'asset_du'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := f.obs.buildAssetDataURL(ctx, "asset_du", "conv_1", "sys_1", now); err == nil || !strings.Contains(err.Error(), "大小校验失败") {
		t.Fatalf("字节数不匹配错误 = %v", err)
	}
	// sha 不匹配。
	if _, err := f.db.Exec(`UPDATE chat_assets SET processed_bytes = 8, processed_sha256 = 'bad' WHERE id = 'asset_du'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := f.obs.buildAssetDataURL(ctx, "asset_du", "conv_1", "sys_1", now); err == nil || !strings.Contains(err.Error(), "完整性校验失败") {
		t.Fatalf("sha 不匹配错误 = %v", err)
	}
}

func TestW1ObservationScheduleAndWait(t *testing.T) {
	f := newW1ObservationFixture(t)
	f.seedReadyAsset(t, "asset_s1")
	f.seedReadyAsset(t, "asset_s2")
	f.executor.body = `{"output_text":"{\"summary\":\"猫\"}"}`
	f.executor.notified = make(chan struct{})
	input := w1ScheduleInput()
	input.Targets = []chat.ObservationTarget{
		{AssetID: "asset_s1", ExpectedTurnID: "turn_1", ExpectedMessageID: "msg_1"},
		{AssetID: "", ExpectedTurnID: "turn_1", ExpectedMessageID: "msg_1"},
		{AssetID: "asset_s2", ExpectedTurnID: "", ExpectedMessageID: "msg_1"},
		{AssetID: "asset_s2", ExpectedTurnID: "turn_1", ExpectedMessageID: "msg_1"},
	}
	f.obs.Schedule(input)
	// 两个有效目标各派发一次（notified 关闭即 goroutine 已进入 Dispatch）。
	for i := 0; i < 2; i++ {
		select {
		case <-f.executor.notified:
		case <-time.After(10 * time.Second):
			t.Fatalf("第 %d 次观察派发未发生", i+1)
		}
	}
	// Wait 消费 tasks 并等待 settle；Settle 完成后再断言状态。
	f.obs.Wait([]string{"asset_s1", "asset_s2", "asset_missing"}, 10_000)
	f.obs.Wait([]string{"asset_s1"}, 1)
	var status string
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, _, _ = f.assetRow(t, "asset_s1")
		if status == "ready" || status == "failed" || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if status != "ready" {
		t.Fatalf("asset_s1 状态 = %q，want ready", status)
	}
	if f.executor.got == nil || f.executor.got.Path != "/v1/responses" {
		t.Fatalf("executor path = %v", f.executor.got)
	}
	// track 直接注册的 done channel 可被 Wait 消费。
	done := make(chan struct{})
	close(done)
	f.obs.track("asset_tracked", done)
	f.obs.Wait([]string{"asset_tracked"}, 1_000)
	// 空目标 Schedule 不派发。
	empty := newW1ObservationFixture(t)
	empty.obs.Schedule(w1ScheduleInput())
	empty.obs.Wait(nil, 1)
}
