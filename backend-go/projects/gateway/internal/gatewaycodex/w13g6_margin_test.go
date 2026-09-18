// w13g6_margin_test.go lifts the remaining statement coverage of the
// gatewaycodex package past the 95% gate with a durable margin.
//
// 不可达语句归因登记（本文件登记，供覆盖率审计核对）：
//   - clock.go:39 randomUUID 的 rand.Read 失败臂：crypto/rand 在支持平台不失败（与源码注释一致）。
//   - compactioncontract.go:120 tailStart<0 守卫：该分支仅在 len(rawBody) >
//     2*codexCompactionRawBodyScanEdgeBytes 时可达，此时 tailStart > edge > 0，恒为假。
//   - segments.go:70/73 gzip.Writer 写入 bytes.Buffer 的错误臂：内存缓冲写入与 gzip
//     Close 对内存流不返回错误。
//   - segments.go:114/117 appendSegmentBytes 的 Seek/WriteAt 错误臂：临时目录内打开的
//     本地文件不产生 Seek 失败；WriteAt 失败需要 OS 级磁盘故障，无注入点。
//   - segments.go:161 ReadAt 返回 n==0 且 err==nil 臂：os.File.ReadAt 契约保证
//     n<len(b) 时 err 非空，该组合不可达。
//   - strategy.go:581 rawPath=="" 兜底：PathAndQuery 契约保证返回值带前导斜杠（恒非空）。
//   - chatbridgestate.go:957 encodeInlineCodexCompactionSummary 序列化错误臂：
//     序列化对象仅含一个 string 字段，json.Marshal 恒成功。
package gatewaycodex

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
)

// ---------------------------------------------------------------------------
// turn retry：memory 与 redis 清理臂
// ---------------------------------------------------------------------------

func TestW13g6TurnRetryMemoryClearArms(t *testing.T) {
	service := newTurnRetryService(t)
	strategy := avoidanceStrategy("w13g6-clear")
	stateKey := strategy.ClientSourceAvoidanceStateKey

	if _, err := service.RememberCodexTurnStreamFailureAsync(context.Background(), strategy, "acc-1", CodexTurnFailureInput{}); err != nil {
		t.Fatalf("remember acc-1: %v", err)
	}
	// 清理一个不在失败表中的账号：entry 存在但账号缺失（485-487）。
	if service.ClearCodexTurnAccountAvoidance(strategy, "acc-9") {
		t.Fatal("clearing an unknown account must report false")
	}
	// 记入第二个账号后清理 acc-1：剩余条目回写臂（493/499-503）。
	if _, err := service.RememberCodexTurnStreamFailureAsync(context.Background(), strategy, "acc-2", CodexTurnFailureInput{}); err != nil {
		t.Fatalf("remember acc-2: %v", err)
	}
	if !service.ClearCodexTurnAccountAvoidance(strategy, "acc-1") {
		t.Fatal("clearing a known account must succeed")
	}
	accountKey := codexTurnAccountStateKey(service.Secret, stateKey, "acc-2")
	entry, ok := service.memory.entries[accountKey]
	if !ok || entry.value.FailedAccounts["acc-1"] != nil || entry.value.FailedAccounts["acc-2"] == nil {
		t.Fatalf("rebuilt state: %+v", entry.value)
	}

	// 过期条目读取即删除（644-647）。
	service.memory.entries[accountKey].expiresAt = service.nowMs() - 1
	if state := service.getMemoryCodexTurnRetryState(accountKey); state != nil {
		t.Fatalf("expired state must read as nil: %+v", state)
	}
	if _, ok := service.memory.entries[accountKey]; ok {
		t.Fatal("expired entry must be deleted on read")
	}
}

func TestW13g6TurnRetryRedisClearArms(t *testing.T) {
	ctx := context.Background()
	strategy := avoidanceStrategy("w13g6-redis-clear")

	// 正常写入两个账号（同账号第二次失败触发 activation），再验证各失败/CAS 臂。
	store := &fakeRedisStore{values: map[string][]byte{}}
	service := newTurnRetryService(t)
	service.Store = store
	if _, err := service.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{}); err != nil {
		t.Fatalf("seed acc-1 first: %v", err)
	}
	first, err := service.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{})
	if err != nil || first == nil || first.Activation == nil {
		t.Fatalf("seed acc-1 activation: %+v %v", first, err)
	}
	if _, err := service.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-2", CodexTurnFailureInput{}); err != nil {
		t.Fatalf("seed acc-2: %v", err)
	}

	// CAS 持续不生效 -> 重试退避后耗尽（537/549/551-555）。
	exhausted := &fakeRedisStore{values: store.values, failCAS: true}
	service.Store = exhausted
	if cleared, err := service.ClearCodexTurnAccountAvoidanceAsync(ctx, strategy, "acc-1"); cleared || err != nil {
		t.Fatalf("cas-exhausted clear: %v %v", cleared, err)
	}

	// CompareSetJSON 出错 -> 错误透传（543-545）。
	service.Store = &w13g6ErrorTurnStore{values: store.values}
	if cleared, err := service.ClearCodexTurnAccountAvoidanceAsync(ctx, strategy, "acc-1"); cleared || err == nil {
		t.Fatalf("cas-error clear: %v %v", cleared, err)
	}

	// ByFence：状态缺失（600-602）。
	service.Store = store
	byFence := ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: strategy.ClientSourceAvoidanceStateKey, AccountID: "acc-3",
		SourceGeneration: 1, SourceFenceID: "0f0e0d0c-0b0a-4009-8008-070605040302",
	}
	if cleared, err := service.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, byFence); cleared || err != nil {
		t.Fatalf("by-fence missing state: %v %v", cleared, err)
	}
	// ByFence：generation/fence 不匹配（607-609）。
	mismatch := byFence
	mismatch.AccountID = "acc-1"
	mismatch.SourceFenceID = "0f0e0d0c-0b0a-4009-8008-070605040301"
	if cleared, err := service.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, mismatch); cleared || err != nil {
		t.Fatalf("by-fence mismatch: %v %v", cleared, err)
	}
	// ByFence：命中后清理（615-620）。generation/fence 以存储态为准。
	service.Store = store
	redisKey := service.redisCodexTurnRetryStateKey(strategy.ClientSourceAvoidanceStateKey, "acc-1")
	rawBackup := append([]byte(nil), store.values[redisKey]...)
	stored := decodeRetryState(store.values[redisKey])
	if stored == nil || stored.FailedAccounts["acc-1"] == nil ||
		stored.FailedAccounts["acc-1"].AvoidanceGeneration == nil || stored.FailedAccounts["acc-1"].AvoidanceFenceID == nil {
		t.Fatalf("stored state: %s", string(store.values[redisKey]))
	}
	hit := byFence
	hit.AccountID = "acc-1"
	hit.SourceGeneration = *stored.FailedAccounts["acc-1"].AvoidanceGeneration
	hit.SourceFenceID = *stored.FailedAccounts["acc-1"].AvoidanceFenceID
	cleared, err := service.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, hit)
	if err != nil || !cleared {
		t.Fatalf("by-fence hit: %v %v", cleared, err)
	}
	// ByFence：CAS 错误（621-623）与耗尽（627/629）。先恢复被命中清理掉的状态。
	store.values[redisKey] = rawBackup
	service.Store = &w13g6ErrorTurnStore{values: store.values}
	if cleared, err := service.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, hit); cleared || err == nil {
		t.Fatalf("by-fence cas error: %v %v", cleared, err)
	}
	exhaustedByFence := &fakeRedisStore{values: store.values, failCAS: true}
	service.Store = exhaustedByFence
	if cleared, err := service.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, hit); cleared || err != nil {
		t.Fatalf("by-fence cas exhausted: %v %v", cleared, err)
	}
}

// w13g6ErrorTurnStore 在 CAS 阶段返回错误。
type w13g6ErrorTurnStore struct {
	values map[string][]byte
}

func (s *w13g6ErrorTurnStore) GetJSON(_ context.Context, key string) (json.RawMessage, error) {
	if value, ok := s.values[key]; ok {
		return append(json.RawMessage(nil), value...), nil
	}
	return nil, nil
}

func (s *w13g6ErrorTurnStore) CompareSetJSON(context.Context, string, json.RawMessage, any, int64) (bool, error) {
	return false, assertText("w13g6 cas failure")
}

func (s *w13g6ErrorTurnStore) Incr(_ context.Context, _ string, ttl int64) (int64, error) {
	return ttl, nil
}

type assertText string

func (e assertText) Error() string { return string(e) }

// TestW13g6TurnRetryEvidenceCounters 覆盖提交重试信号与中断窗口计数臂（835-877）。
func TestW13g6TurnRetryEvidenceCounters(t *testing.T) {
	service := newTurnRetryService(t)
	strategy := avoidanceStrategy("w13g6-evidence")

	// 提交重试信号：计数器累加（835-837）。
	for i := 0; i < 2; i++ {
		result, err := service.RememberCodexTurnStreamFailureAsync(context.Background(), strategy, "acc-1",
			CodexTurnFailureInput{Evidence: EvidenceCommittedRetrySignal})
		if err != nil || result == nil {
			t.Fatalf("committed retry remember: %+v %v", result, err)
		}
	}
	// 中断放弃：首建窗口（855-858）。
	if _, err := service.RememberCodexTurnStreamFailureAsync(context.Background(), strategy, "acc-1",
		CodexTurnFailureInput{Evidence: EvidenceIncompleteDownstreamAbort}); err != nil {
		t.Fatalf("abort remember: %v", err)
	}
	// 观察编号超过上限时裁剪（875-877）。
	for i := 0; i < codexTurnRecentObservationLimit+2; i++ {
		if _, err := service.RememberCodexTurnStreamFailureAsync(context.Background(), strategy, "acc-1",
			CodexTurnFailureInput{ObservationID: "w13g6-obs-" + strconv.Itoa(i)}); err != nil {
			t.Fatalf("observation remember: %v", err)
		}
	}
	stateKey := codexTurnAccountStateKey(service.Secret, strategy.ClientSourceAvoidanceStateKey, "acc-1")
	entry, ok := service.memory.entries[stateKey]
	if !ok {
		t.Fatal("state entry missing")
	}
	observed := entry.value.FailedAccounts["acc-1"]
	if observed == nil || observed.CommittedRetrySignalCount == nil || *observed.CommittedRetrySignalCount != 2 {
		t.Fatalf("committed retry counter: %+v", observed)
	}
	if observed.IncompleteDownstreamAbortCount == nil || *observed.IncompleteDownstreamAbortCount != 1 {
		t.Fatalf("abort counter: %+v", observed)
	}
	if len(observed.RecentObservationIDs) > codexTurnRecentObservationLimit {
		t.Fatalf("observation trim: %v", observed.RecentObservationIDs)
	}
}

func hexEncodeInt(value int) string {
	return strings.TrimSpace(strings.Join([]string{"o", string(rune('a' + value%26))}, ""))
}

// ---------------------------------------------------------------------------
// segments：格式化 / 存储路径 / 载荷读取错误臂
// ---------------------------------------------------------------------------

func TestW13g6SegmentHelpers(t *testing.T) {
	if base36(0) != "0" || base36(-35) != "-z" || base36(35) != "z" {
		t.Fatal("base36 arms")
	}
	store, err := NewSegmentStore(SegmentStoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// 空存储 key -> 相对路径 "." -> 超出数据目录（209-211）。
	if _, err := store.resolveStoragePath(""); err == nil || !strings.Contains(err.Error(), "超出数据目录") {
		t.Fatalf("blank storage key: %v", err)
	}

	segmentKey := SegmentStorageKey("w13g6-session", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fullPath := filepath.Join(store.config.Root, segmentKey)
	reference := func(raw []byte) CodexContextPayloadReference {
		sum := sha256.Sum256(raw)
		return CodexContextPayloadReference{
			StorageKey: segmentKey, StorageOffsetBytes: 0,
			SHA256: hex.EncodeToString(sum[:]), RawSizeBytes: int64(len(raw)),
			CompressedSizeBytes: int64(len(raw)), Compression: "gzip", SchemaVersion: 2,
		}
	}
	// 非法 gzip 头 -> gzip.NewReader 错误（173-175）。
	corrupt := []byte("this is not gzip data")
	if err := writeFileAt(fullPath, corrupt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadSegmentPayload(reference(corrupt)); err == nil {
		t.Fatal("corrupt gzip must fail")
	}

	// 解压大小与记录不符（180-182）。
	var sized bytes.Buffer
	writer := gzip.NewWriter(&sized)
	_, _ = writer.Write([]byte(`{"ok":true}`))
	_ = writer.Close()
	if err := writeFileAt(fullPath, sized.Bytes()); err != nil {
		t.Fatal(err)
	}
	sizedRef := reference(sized.Bytes())
	sizedRef.RawSizeBytes = 3
	if _, err := store.ReadSegmentPayload(sizedRef); err == nil || !strings.Contains(err.Error(), "解压大小异常") {
		t.Fatalf("size mismatch: %v", err)
	}

	// 解压结果非 JSON（183-185）。
	invalidSource := []byte("not json at all")
	var invalid bytes.Buffer
	invalidWriter := gzip.NewWriter(&invalid)
	_, _ = invalidWriter.Write(invalidSource)
	_ = invalidWriter.Close()
	if err := writeFileAt(fullPath, invalid.Bytes()); err != nil {
		t.Fatal(err)
	}
	invalidRef := reference(invalid.Bytes())
	invalidRef.RawSizeBytes = int64(len(invalidSource))
	if _, err := store.ReadSegmentPayload(invalidRef); err == nil || !strings.Contains(err.Error(), "结构无效") {
		t.Fatalf("invalid json payload: %v", err)
	}

	// 正常往返保持可用。
	if err := writeFileAt(fullPath, sized.Bytes()); err != nil {
		t.Fatal(err)
	}
	goodRef := reference(sized.Bytes())
	goodRef.RawSizeBytes = 11
	payload, err := store.ReadSegmentPayload(goodRef)
	if err != nil || string(payload) != `{"ok":true}` {
		t.Fatalf("valid payload: %s %v", string(payload), err)
	}
}

func writeFileAt(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, werr := file.WriteAt(data, 0)
	return werr
}

// TestW13g6MaterializedMutationNonArray 覆盖非数组物化输入的原样返回臂。
func TestW13g6MaterializedMutationNonArray(t *testing.T) {
	start := 2
	state := &CodexResponsesContextRequestState{
		PreviousResponseKind:                PreviousKindInternal,
		MaterializedCurrentInputStartIndex:  &start,
	}
	input := "scalar-input"
	if got := currentInputFromMaterializedMutation(state, input); got != input {
		t.Fatalf("non-array input: %v", got)
	}
}

// ---------------------------------------------------------------------------
// 压缩契约 / 请求头 / 来源身份
// ---------------------------------------------------------------------------

func TestW13g6CompactionContractArms(t *testing.T) {
	// 空 / 根路径归一化（75-77、87-89）。
	root := &gatewaypreauth.GatewayRequest{HTTP: httptest.NewRequest(http.MethodPost, "/", nil)}
	if isOpenAIResponsesPostRequest(root) {
		t.Fatal("root path is not /responses")
	}
	v1Only := &gatewaypreauth.GatewayRequest{HTTP: httptest.NewRequest(http.MethodPost, "/v1", nil)}
	if isOpenAIResponsesCompactPostRequest(v1Only) {
		t.Fatal("/v1 path is not /responses/compact")
	}
	// 超长非 JSON 体：compaction 触发串位于前缘窗口（116-118）。
	padding := strings.Repeat("x", 3*64*1024)
	body := []byte(`{"type":"compaction_trigger"` + padding)
	big := &gatewaypreauth.GatewayRequest{
		HTTP: httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		Body: &gatewaybody.Request{RawBody: body},
	}
	if !requestBodyHasCompactionTrigger(big) {
		t.Fatal("prefix compaction trigger must be detected")
	}
}

func TestW13g6CodexUsageHeadersSecondaryWindow(t *testing.T) {
	headers := http.Header{}
	headers.Set("x-codex-secondary-window-minutes", "321.7")
	snapshot := ParseOpenAICodexUsageHeaders(headers, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if snapshot == nil || snapshot.SecondaryWindowMinutes == nil || *snapshot.SecondaryWindowMinutes != 321 {
		t.Fatalf("secondary window snapshot: %+v", snapshot)
	}
}

func TestW13g6SourceIdentityInteractionArm(t *testing.T) {
	resolver := &SourceIdentityResolver{
		Secret: "w13g6-source-secret",
		GeminiInteractionResourceID: func(*gatewaypreauth.GatewayRequest) string {
			return "w13g6-interaction-1"
		},
	}
	request := &gatewaypreauth.GatewayRequest{HTTP: httptest.NewRequest(http.MethodPost, "/v1beta/interactions", nil)}
	source := resolver.ResolveGatewayClientSourceIdentity(request, GatewayClientSourceIdentityInput{
		ClientProfile:      "gemini_cli",
		DownstreamProtocol: "gemini_interactions/v1beta",
		SystemAccountID:    "w13g6-sys",
		APIKeyID:           "w13g6-key",
		ClientIP:           "10.0.0.1",
	})
	if source.Status != SourceStatusResolved || source.SourceKey == "" || source.AffinityKey != "w13g6-interaction-1" {
		t.Fatalf("interaction source: %+v", source)
	}
	if source.SemanticNamespace != "google.gemini.interaction" {
		t.Fatalf("interaction namespace: %+v", source)
	}
}

// TestW13g6AnthropicDownstreamUnknownStream 覆盖流式非 messages 请求的未知流臂。
func TestW13g6AnthropicDownstreamUnknownStream(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/other", nil)
	request.Header.Set("Accept", "text/event-stream")
	req := &gatewaypreauth.GatewayRequest{HTTP: request}
	if got := ResolveAnthropicGatewayDownstreamProtocol(req); got != DownstreamUnknownStream {
		t.Fatalf("unknown stream protocol: %q", got)
	}
	// 正常流式 messages 走 SSE 分支，保持既有契约。
	messages := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	messages.Header.Set("Accept", "text/event-stream")
	if got := ResolveAnthropicGatewayDownstreamProtocol(&gatewaypreauth.GatewayRequest{HTTP: messages}); got != DownstreamMessagesSSE {
		t.Fatalf("messages sse protocol: %q", got)
	}
}

	// 静态引用 gatewaysession，保证会话身份 seam 的编译期契约。
	var _ = gatewaysession.IdentityStatusMissing
