package main

// w1_ports_arms_test.go 覆盖 chain_ports.go 的组合根端口适配器与纯逻辑：
// localSessionAffinity 内存亲和、失败派发的纯投影/格式化函数、preauth
// 协作适配器、disabled 桩、slogObservability 与 audit 派发适配器。
// 全部为直接构造参数的白盒调用：无真实网络（httptest.NewRequest 仅构造
// 内存请求对象），时间敏感断言使用固定时钟或白盒写入 TTL。

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// w1pGatewayRequest 构造内存中的 GatewayRequest（无网络监听）。
func w1pGatewayRequest(method, target string, headers map[string]string) *gatewaypreauth.GatewayRequest {
	raw := httptest.NewRequest(method, target, nil)
	for name, value := range headers {
		raw.Header.Set(name, value)
	}
	return gatewaypreauth.NewGatewayRequest(raw)
}

// w1pClock 固定时钟，满足 gatewaypreauth.Clock。
type w1pClock struct{ now time.Time }

func (c w1pClock) Now() time.Time { return c.now }

// w1pCodedError 携带 ErrorCode 表面的错误，用于 upstreamRequestErrorCode。
type w1pCodedError struct{ code string }

func (e w1pCodedError) Error() string     { return "w1p coded failure" }
func (e w1pCodedError) ErrorCode() string { return e.code }

// w1pObservationPort 是 chainAPIKeyObservationPort 的可观测 fake。
type w1pObservationPort struct {
	epoch  *int64
	seenID string
}

func (p *w1pObservationPort) CaptureFailureObservation(account gatewaydispatch.AccountCandidate) *int64 {
	p.seenID = account.ID
	return p.epoch
}

// w1pAuditSink 记录 AttemptAuditSink 调用，用于失败派发审计断言。
type w1pAuditSink struct {
	completions     []gatewaydispatch.CompleteAttemptInput
	completeAttempt []string
	records         []gatewaydispatch.FailedDispatchAttemptInput
}

func (s *w1pAuditSink) StartAttempt(gatewaydispatch.StartAttemptInput) string { return "w1p-attempt" }

func (s *w1pAuditSink) CompleteAttempt(attemptID string, input gatewaydispatch.CompleteAttemptInput) {
	s.completeAttempt = append(s.completeAttempt, attemptID)
	s.completions = append(s.completions, input)
}

func (s *w1pAuditSink) RecordFailedDispatchAttempt(input gatewaydispatch.FailedDispatchAttemptInput) {
	s.records = append(s.records, input)
}

// w1pBridgePreflight 是 gatewaypreauth.CodexBridgePreflight 的透传 fake。
type w1pBridgePreflight struct{}

func (b *w1pBridgePreflight) CompactionExpectedForRequest(*gatewaypreauth.GatewayRequest) bool {
	return true
}

func (b *w1pBridgePreflight) ApplyContextStatePreflight(context.Context, gatewaypreauth.CodexContextStateInput) (bool, error) {
	return true, nil
}

func (b *w1pBridgePreflight) ApplyChatBridgeCompactPreflight(context.Context, gatewaypreauth.CodexCompactPreflightInput) (gatewaypreauth.CodexCompactPreflightResult, error) {
	return gatewaypreauth.CodexCompactPreflightResult{Completed: true}, nil
}

// ---------------------------------------------------------------------------
// localSessionAffinity
// ---------------------------------------------------------------------------

// TestW1PLocalSessionAffinityLifecycle：命中/未命中/过期/空参分支。
func TestW1PLocalSessionAffinityLifecycle(t *testing.T) {
	ctx := context.Background()
	scope := gatewaydispatch.AffinityScope{GroupID: "group-1"}
	affinity := newLocalSessionAffinity()

	// 未命中：空 key 直接返回提议账户且不建立亲和。
	if got, ok := affinity.ClaimAsync(ctx, "", "acc-A", scope); got != "acc-A" || ok {
		t.Fatalf("空 key ClaimAsync = (%q,%v)，want (acc-A,false)", got, ok)
	}
	// 未命中：空 proposed 账户直接返回。
	if got, ok := affinity.ClaimAsync(ctx, "sess-1", "", scope); got != "" || ok {
		t.Fatalf("空 proposed ClaimAsync = (%q,%v)，want (\"\",false)", got, ok)
	}
	// 未命中：新会话由提议账户占位并刷新 TTL。
	got, ok := affinity.ClaimAsync(ctx, "sess-1", "acc-A", scope)
	if got != "acc-A" || !ok {
		t.Fatalf("新会话 ClaimAsync = (%q,%v)，want (acc-A,true)", got, ok)
	}
	affinity.mu.Lock()
	if affinity.expired("sess-1") {
		t.Fatal("ClaimAsync 后 TTL 未刷新")
	}
	affinity.mu.Unlock()
	// 命中：RememberAsync 改写记忆后 ClaimAsync 返回记住的账户。
	affinity.RememberAsync(ctx, "sess-1", "acc-B", scope)
	if got, ok := affinity.ClaimAsync(ctx, "sess-1", "acc-A", scope); got != "acc-B" || !ok {
		t.Fatalf("命中 ClaimAsync = (%q,%v)，want (acc-B,true)", got, ok)
	}
	// 过期：白盒写入过期 TTL 后，记忆不生效，重新由提议账户占位。
	affinity.mu.Lock()
	affinity.keys["sess-2"] = localAffinityEntry{accountID: "acc-C", scopeKey: "group-1"}
	affinity.ttls["sess-2"] = time.Now().Add(-time.Hour)
	affinity.mu.Unlock()
	if got, ok := affinity.ClaimAsync(ctx, "sess-2", "acc-D", scope); got != "acc-D" || !ok {
		t.Fatalf("过期 ClaimAsync = (%q,%v)，want (acc-D,true)", got, ok)
	}
	// ForgetAsync：空 key 无操作；非空 key 清除后记忆消失。
	if err := affinity.ForgetAsync(ctx, "", "acc-B"); err != nil {
		t.Fatalf("空 key ForgetAsync 返回错误：%v", err)
	}
	if err := affinity.ForgetAsync(ctx, "sess-1", "acc-B"); err != nil {
		t.Fatalf("ForgetAsync 返回错误：%v", err)
	}
	if got, ok := affinity.ClaimAsync(ctx, "sess-1", "acc-A", scope); got != "acc-A" || !ok {
		t.Fatalf("Forget 后 ClaimAsync = (%q,%v)，want (acc-A,true)", got, ok)
	}
}

// TestW1PLocalSessionAffinityOrderAsync：记忆账户前移，其余保序。
func TestW1PLocalSessionAffinityOrderAsync(t *testing.T) {
	ctx := context.Background()
	accounts := []gatewaydispatch.AccountCandidate{
		{ID: "acc-A"}, {ID: "acc-B"}, {ID: "acc-C"},
	}
	affinity := newLocalSessionAffinity()

	// 空 key：原样返回。
	ordered, err := affinity.OrderAsync(ctx, accounts, "", gatewaydispatch.AffinityOrderingOptions{})
	if err != nil {
		t.Fatalf("空 key OrderAsync 错误：%v", err)
	}
	if len(ordered) != 3 || ordered[0].ID != "acc-A" || ordered[2].ID != "acc-C" {
		t.Fatalf("空 key OrderAsync 顺序被改变：%v", ordered)
	}
	// 未记忆 key：原样返回。
	ordered, err = affinity.OrderAsync(ctx, accounts, "sess-x", gatewaydispatch.AffinityOrderingOptions{})
	if err != nil || len(ordered) != 3 || ordered[0].ID != "acc-A" {
		t.Fatalf("未记忆 OrderAsync = %v, err=%v", ordered, err)
	}
	// 命中：acc-B 记忆后前移。
	affinity.RememberAsync(ctx, "sess-o", "acc-B", gatewaydispatch.AffinityScope{GroupID: "g"})
	ordered, err = affinity.OrderAsync(ctx, accounts, "sess-o", gatewaydispatch.AffinityOrderingOptions{})
	if err != nil {
		t.Fatalf("OrderAsync 错误：%v", err)
	}
	if len(ordered) != 3 || ordered[0].ID != "acc-B" || ordered[1].ID != "acc-A" || ordered[2].ID != "acc-C" {
		t.Fatalf("命中 OrderAsync 顺序 = %v，want [acc-B acc-A acc-C]", ordered)
	}
	// 过期：白盒写入过期 TTL 后不再重排。
	affinity.mu.Lock()
	affinity.keys["sess-e"] = localAffinityEntry{accountID: "acc-C"}
	affinity.ttls["sess-e"] = time.Now().Add(-time.Minute)
	affinity.mu.Unlock()
	ordered, err = affinity.OrderAsync(ctx, accounts, "sess-e", gatewaydispatch.AffinityOrderingOptions{})
	if err != nil || len(ordered) != 3 || ordered[0].ID != "acc-A" {
		t.Fatalf("过期 OrderAsync = %v, err=%v", ordered, err)
	}
}

// w1pFakeConcurrencyStore 是 gatewaydispatch.AccountConcurrencyStore 的可控
// fake：LoadCurrentAsync / LoadCurrentByLaneAsync 返回预置计数副本并记录
// lane 查询；TryAcquireAsync 不在忙判定路径上，恒返回未获取的空槽。
type w1pFakeConcurrencyStore struct {
	current   map[string]int
	lane      map[string]int
	laneCalls []string
}

func (s *w1pFakeConcurrencyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	out := make(map[string]int, len(s.current))
	for id, value := range s.current {
		out[id] = value
	}
	return out, nil
}

func (s *w1pFakeConcurrencyStore) LoadCurrentByLaneAsync(_ context.Context, _ []string, lane string) (map[string]int, error) {
	s.laneCalls = append(s.laneCalls, lane)
	out := make(map[string]int, len(s.lane))
	for id, value := range s.lane {
		out[id] = value
	}
	return out, nil
}

func (s *w1pFakeConcurrencyStore) TryAcquireAsync(context.Context, string, int, gatewaydispatch.AccountConcurrencyAcquireOptions) (gatewaydispatch.ConcurrencySlot, error) {
	return gatewaydispatch.ConcurrencySlot{}, nil
}

// TestW1PLocalSessionAffinityHighConcurrencyBusy：高并发忙判定谓词。
// nil store（组合测试零值）恒不忙；注入 store 后 busy = 所有候选并发满
// （总并发达硬上限 max(1, ConcurrencyLimit)），且仅 high_concurrency 分组
// 参与判定（Node areHighConcurrencyAccountsBusyForLaneAsync 语义）。
func TestW1PLocalSessionAffinityHighConcurrencyBusy(t *testing.T) {
	ctx := context.Background()
	hcOptions := gatewaydispatch.HighConcurrencyBusyOptions{
		AffinityOrderingOptions: gatewaydispatch.AffinityOrderingOptions{GroupType: "high_concurrency"},
	}
	candidate := []gatewaydispatch.AccountCandidate{{ID: "acc-A", ConcurrencyLimit: 1}}

	// (a) 零值 localSessionAffinity（nil store）：恒 false。
	nilAffinity := &localSessionAffinity{}
	busy, err := nilAffinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, candidate, hcOptions)
	if err != nil || busy {
		t.Fatalf("nil store busy = %v, %v，want false,nil", busy, err)
	}

	// (b) high_concurrency + 单账户总并发 1 >= 硬上限 1 → true。
	affinity := newLocalSessionAffinity()
	affinity.concurrency = &w1pFakeConcurrencyStore{current: map[string]int{"acc-A": 1}}
	busy, err = affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, candidate, hcOptions)
	if err != nil {
		t.Fatalf("AreHighConcurrencyAccountsBusyForLaneAsync 错误：%v", err)
	}
	if !busy {
		t.Fatal("总并发 1 >= 硬上限 1 应报告忙")
	}

	// (c) 未占满（0 < 1）→ false。
	affinity.concurrency = &w1pFakeConcurrencyStore{current: map[string]int{"acc-A": 0}}
	busy, err = affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, candidate, hcOptions)
	if err != nil || busy {
		t.Fatalf("未占满 busy = %v, %v，want false,nil", busy, err)
	}

	// (d) 非 high_concurrency 分组即使占满也不忙。
	affinity.concurrency = &w1pFakeConcurrencyStore{current: map[string]int{"acc-A": 1}}
	personalOptions := gatewaydispatch.HighConcurrencyBusyOptions{
		AffinityOrderingOptions: gatewaydispatch.AffinityOrderingOptions{GroupType: "personal"},
	}
	busy, err = affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, candidate, personalOptions)
	if err != nil || busy {
		t.Fatalf("非 high_concurrency busy = %v, %v，want false,nil", busy, err)
	}
}

// TestW1PLocalSessionAffinityHighConcurrencyBusyImageLane：image 请求的
// lane 腿——总并发未满但 image lane 并发达
// EffectiveImageLaneConcurrencyLimit（policy imageLaneMaxConcurrency 收紧）
// → 忙；text 请求不看 lane 腿 → 不忙。
func TestW1PLocalSessionAffinityHighConcurrencyBusyImageLane(t *testing.T) {
	ctx := context.Background()
	accounts := []gatewaydispatch.AccountCandidate{{ID: "acc-A", ConcurrencyLimit: 4}}
	policy := gatewayruntimecache.GroupSchedulingPolicy{"imageLaneMaxConcurrency": 2}
	imageOptions := gatewaydispatch.HighConcurrencyBusyOptions{
		AffinityOrderingOptions: gatewaydispatch.AffinityOrderingOptions{
			GroupType:        "high_concurrency",
			SchedulingPolicy: &policy,
		},
		RequestLane: "image",
	}
	affinity := newLocalSessionAffinity()
	store := &w1pFakeConcurrencyStore{
		current: map[string]int{"acc-A": 1},
		lane:    map[string]int{"acc-A": 2},
	}
	affinity.concurrency = store

	// 总并发 1 < 4 未满；image lane 2 >= EffectiveImageLaneConcurrencyLimit(4, policy)=2 → 忙。
	if effective := gatewayhotquality.EffectiveImageLaneConcurrencyLimit(4, policy); effective != 2 {
		t.Fatalf("EffectiveImageLaneConcurrencyLimit(4, policy) = %d，want 2", effective)
	}
	busy, err := affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, imageOptions)
	if err != nil {
		t.Fatalf("image lane busy 错误：%v", err)
	}
	if !busy {
		t.Fatal("image lane 并发 2 >= 收紧上限 2 应报告忙")
	}
	if len(store.laneCalls) != 1 || store.laneCalls[0] != "image" {
		t.Fatalf("image 请求应恰好查询一次 image lane：%v", store.laneCalls)
	}

	// text 请求不看 lane 腿：同一计数下不忙，且不追加 lane 查询。
	textOptions := imageOptions
	textOptions.RequestLane = "text"
	busy, err = affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, textOptions)
	if err != nil || busy {
		t.Fatalf("text 请求 busy = %v, %v，want false,nil", busy, err)
	}
	if len(store.laneCalls) != 1 {
		t.Fatalf("text 请求不应追加 lane 查询：%v", store.laneCalls)
	}

	// image lane 未达收紧上限（1 < 2）→ 不忙。
	store.lane["acc-A"] = 1
	busy, err = affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, imageOptions)
	if err != nil || busy {
		t.Fatalf("lane 并发 1 < 2 busy = %v, %v，want false,nil", busy, err)
	}
}

// ---------------------------------------------------------------------------
// 纯转换 / 诊断辅助
// ---------------------------------------------------------------------------

// TestW1PUpstreamRequestErrorDiagnostics：错误名去掉包限定，错误码仅取
// ErrorCode 表面。
func TestW1PUpstreamRequestErrorDiagnostics(t *testing.T) {
	if name := upstreamRequestErrorName(nil); name != "" {
		t.Fatalf("nil 错误 name = %q，want 空串", name)
	}
	if code := upstreamRequestErrorCode(nil); code != "" {
		t.Fatalf("nil 错误 code = %q，want 空串", code)
	}
	plain := errors.New("boom")
	if name := upstreamRequestErrorName(plain); name != "errorString" {
		t.Fatalf("普通错误 name = %q，want errorString", name)
	}
	if code := upstreamRequestErrorCode(plain); code != "" {
		t.Fatalf("普通错误 code = %q，want 空串", code)
	}
	coded := w1pCodedError{code: "E_W1P"}
	if name := upstreamRequestErrorName(coded); name != "w1pCodedError" {
		t.Fatalf("自定义错误 name = %q，want w1pCodedError", name)
	}
	if code := upstreamRequestErrorCode(coded); code != "E_W1P" {
		t.Fatalf("自定义错误 code = %q，want E_W1P", code)
	}
}

// TestW1PChainSanitizedUpstreamURL：TrimSpace 后空值降级 unknown。
func TestW1PChainSanitizedUpstreamURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空串", "", "unknown"},
		{"纯空白", "   \t ", "unknown"},
		{"两端空白", "  https://up.example/v1  ", "https://up.example/v1"},
		{"原样", "https://up.example/v1?k=v", "https://up.example/v1?k=v"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chainSanitizedUpstreamURL(tc.in); got != tc.want {
				t.Fatalf("chainSanitizedUpstreamURL(%q) = %q，want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestW1PResponseProjectionNilGuards：nil response 的守卫分支。
// 非 nil 分支需要 gatewaydispatch.GatewayUpstreamResponse（字段未导出且仅能
// 经 RequestUpstream 真实网络构造），按无网络约束跳过。
func TestW1PResponseProjectionNilGuards(t *testing.T) {
	if got := responseContentTypeOf(nil); got != "" {
		t.Fatalf("responseContentTypeOf(nil) = %q，want 空串", got)
	}
	if got := responseHeadersOf(nil); got != nil {
		t.Fatalf("responseHeadersOf(nil) = %v，want nil", got)
	}
	if got := usageFailureHeadersOf(nil); got != nil {
		t.Fatalf("usageFailureHeadersOf(nil) = %v，want nil", got)
	}
	if got := responseHTTPHeaderOf(nil); got != nil {
		t.Fatalf("responseHTTPHeaderOf(nil) = %v，want nil", got)
	}
	if got := parsedFailureBodyOf(nil, `{"error":"x"}`); got != nil {
		t.Fatalf("parsedFailureBodyOf(nil, body) = %v，want nil", got)
	}
	if got := parsedFailureBodyOf(nil, ""); got != nil {
		t.Fatalf("parsedFailureBodyOf(nil, \"\") = %v，want nil", got)
	}
}

// TestW1PChainUpstreamHeaderAccountOf：候选账户到流检测账户的投影。
func TestW1PChainUpstreamHeaderAccountOf(t *testing.T) {
	credentials := map[string]any{"token": "sk-w1p"}
	account := gatewaydispatch.AccountCandidate{
		ID:                        "acc-1",
		APIKey:                    "sk-up",
		Type:                      "oauth",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile-7",
		ProtocolCode:              "openai",
		ProtocolVersion:           "2025-01",
		Credentials:               credentials,
	}
	projected := chainUpstreamHeaderAccountOf(account)
	if projected == nil {
		t.Fatal("chainUpstreamHeaderAccountOf 返回 nil")
	}
	if projected.ID != "acc-1" || projected.APIKey != "sk-up" || projected.Type != "oauth" ||
		projected.ProviderCode != "openai" || projected.ProviderProtocolProfileID != "profile-7" ||
		projected.ProtocolCode != "openai" || projected.ProtocolVersion != "2025-01" {
		t.Fatalf("投影字段不匹配：%+v", projected)
	}
	if len(projected.Credentials) != 1 || projected.Credentials["token"] != "sk-w1p" {
		t.Fatalf("Credentials 投影不匹配：%v", projected.Credentials)
	}
}

// TestW1PChainObservationEpochOf：nil 端口 / nil 代际 / 命中代际。
func TestW1PChainObservationEpochOf(t *testing.T) {
	account := gatewaydispatch.AccountCandidate{ID: "acc-epoch"}
	if got := chainObservationEpochOf(nil, account); got != "" {
		t.Fatalf("nil 端口 epoch = %q，want 空串", got)
	}
	if got := chainObservationEpochOf(&w1pObservationPort{}, account); got != "" {
		t.Fatalf("nil 代际 epoch = %q，want 空串", got)
	}
	epoch := int64(42)
	port := &w1pObservationPort{epoch: &epoch}
	if got := chainObservationEpochOf(port, account); got != "42" {
		t.Fatalf("epoch = %q，want 42", got)
	}
	if port.seenID != "acc-epoch" {
		t.Fatalf("观察端口未收到账户：%q", port.seenID)
	}
}

// ---------------------------------------------------------------------------
// 失败派发：下游关闭记录 / 失败体读取守卫
// ---------------------------------------------------------------------------

// TestW1PRecordDownstreamClosedRequestError：审计尝试关闭与失败派发记录
// 均携带固定 downstream 归因。
func TestW1PRecordDownstreamClosedRequestError(t *testing.T) {
	ctx := context.Background()
	account := gatewaydispatch.AccountCandidate{ID: "acc-1", Name: "账户一"}
	upstreamURL := "https://up.example/v1/responses"

	t.Run("已有审计尝试ID走CompleteAttempt", func(t *testing.T) {
		dispatcher := &chainFailureDispatcher{}
		sink := &w1pAuditSink{}
		input := gatewaydispatch.UpstreamRequestErrorInput{
			Account:           account,
			UpstreamURL:       upstreamURL,
			AuditCapture:      gatewaydispatch.AuditCapture{Sink: sink},
			AuditAttemptID:    "attempt-9",
			AuditAttemptIndex: 2,
			AttemptStartedAt:  1728000000000,
			LastAttempt: &gatewaydispatch.UpstreamAttempt{
				AccountID: "acc-1", UpstreamURL: upstreamURL, Status: 502, HasStatus: true,
			},
		}
		if err := dispatcher.recordDownstreamClosedRequestError(ctx, input); err != nil {
			t.Fatalf("recordDownstreamClosedRequestError 错误：%v", err)
		}
		if len(sink.completions) != 1 || len(sink.completeAttempt) != 1 || sink.completeAttempt[0] != "attempt-9" {
			t.Fatalf("CompleteAttempt 记录 = %v / %v", sink.completeAttempt, sink.completions)
		}
		completion := sink.completions[0]
		if completion.Success {
			t.Fatal("下游关闭完成记录不应为成功")
		}
		if completion.ErrorPhase != "downstream" {
			t.Fatalf("ErrorPhase = %q，want downstream", completion.ErrorPhase)
		}
		if completion.ErrorMessage != gatewayresponse.DownstreamConnectionClosedMessage {
			t.Fatalf("ErrorMessage = %q，want %q", completion.ErrorMessage, gatewayresponse.DownstreamConnectionClosedMessage)
		}
		if len(sink.records) != 0 {
			t.Fatalf("CompleteAttempt 分支不应再记录失败派发尝试：%v", sink.records)
		}
	})

	t.Run("无审计尝试ID走RecordFailedDispatchAttempt", func(t *testing.T) {
		dispatcher := &chainFailureDispatcher{}
		sink := &w1pAuditSink{}
		input := gatewaydispatch.UpstreamRequestErrorInput{
			Account:           account,
			UpstreamURL:       upstreamURL,
			AuditCapture:      gatewaydispatch.AuditCapture{Sink: sink},
			AuditAttemptIndex: 1,
			AttemptStartedAt:  1728000000000,
		}
		if err := dispatcher.recordDownstreamClosedRequestError(ctx, input); err != nil {
			t.Fatalf("recordDownstreamClosedRequestError 错误：%v", err)
		}
		if len(sink.completions) != 0 {
			t.Fatalf("不应有 CompleteAttempt：%v", sink.completions)
		}
		if len(sink.records) != 1 {
			t.Fatalf("RecordFailedDispatchAttempt 记录数 = %d，want 1", len(sink.records))
		}
		record := sink.records[0]
		if record.Account.ID != "acc-1" || record.UpstreamURL != upstreamURL {
			t.Fatalf("记录账户/URL = %q / %q", record.Account.ID, record.UpstreamURL)
		}
		if record.AttemptIndex != 1 || record.StartedAtMs != 1728000000000 {
			t.Fatalf("记录索引/时间 = %d / %d", record.AttemptIndex, record.StartedAtMs)
		}
		if record.ErrorPhase != "downstream" {
			t.Fatalf("ErrorPhase = %q，want downstream", record.ErrorPhase)
		}
		if record.ErrorMessage != gatewayresponse.DownstreamConnectionClosedMessage {
			t.Fatalf("ErrorMessage = %q，want %q", record.ErrorMessage, gatewayresponse.DownstreamConnectionClosedMessage)
		}
		if record.Method != "" {
			t.Fatalf("nil 请求 Method = %q，want 空串", record.Method)
		}
	})
}

// TestW1PReadUpstreamFailureBodyGuards：nil 响应与已取消上下文守卫。
// 有响应体的有界读取分支需要 GatewayUpstreamResponse（见上），跳过。
func TestW1PReadUpstreamFailureBodyGuards(t *testing.T) {
	body, truncated, err := readUpstreamFailureBody(context.Background(), nil, 16)
	if body != "" || truncated || err != nil {
		t.Fatalf("nil 响应 = (%q,%v,%v)，want (\"\",false,nil)", body, truncated, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	// nil 响应守卫先于 ctx 检查：取消上下文 + nil 响应仍静默返回。
	body, truncated, err = readUpstreamFailureBody(cancelled, nil, 16)
	if body != "" || truncated || err != nil {
		t.Fatalf("取消上下文 + nil 响应 = (%q,%v,%v)，want (\"\",false,nil)", body, truncated, err)
	}
}

// ---------------------------------------------------------------------------
// lastAttempt 重建投影
// ---------------------------------------------------------------------------

// TestW1PFailedResponseAttemptOf：无先前尝试新建 + 有先前尝试复制后覆盖。
func TestW1PFailedResponseAttemptOf(t *testing.T) {
	account := gatewaydispatch.AccountCandidate{
		ID: "acc-2", Name: "账户二", ProviderCode: "openai",
		ProviderProtocolProfileID: "profile-2", ProtocolCode: "openai", ProtocolVersion: "v2",
	}
	parsed := map[string]any{"error": map[string]any{"code": "rate_limited"}}

	fresh := failedResponseAttemptOf(gatewaydispatch.FailedUpstreamResponseInput{
		Account:     account,
		UpstreamURL: "https://up.example/v1/responses",
	}, "err-body", parsed)
	if fresh.AccountID != "acc-2" || fresh.AccountName != "账户二" ||
		fresh.ProviderCode != "openai" || fresh.ProviderProtocolProfileID != "profile-2" ||
		fresh.ProtocolCode != "openai" || fresh.ProtocolVersion != "v2" {
		t.Fatalf("新建 attempt 账户投影不匹配：%+v", fresh)
	}
	if fresh.UpstreamURL != "https://up.example/v1/responses" {
		t.Fatalf("UpstreamURL = %q", fresh.UpstreamURL)
	}
	if fresh.HasStatus || fresh.Status != 0 {
		t.Fatalf("nil 响应不应有状态：%+v", fresh)
	}
	if fresh.ResponseBodyText != "err-body" || fresh.ParsedResponseBody["error"] == nil {
		t.Fatalf("失败体/解析体不匹配：%q / %v", fresh.ResponseBodyText, fresh.ParsedResponseBody)
	}

	copied := failedResponseAttemptOf(gatewaydispatch.FailedUpstreamResponseInput{
		Account:     account,
		UpstreamURL: "https://up.example/v1/responses",
		LastAttempt: &gatewaydispatch.UpstreamAttempt{
			AccountID: "acc-old", Message: "旧消息", ErrorCode: "E_OLD", Status: 500, HasStatus: true,
		},
	}, "new-body", nil)
	if copied.Message != "旧消息" || copied.ErrorCode != "E_OLD" {
		t.Fatalf("复制分支保留字段被改写：%+v", copied)
	}
	if copied.AccountID != "acc-2" {
		t.Fatalf("AccountID 未覆盖：%q，want acc-2", copied.AccountID)
	}
	// Response 为 nil 时不改写状态：先前尝试的 500/HasStatus 原样保留。
	if !copied.HasStatus || copied.Status != 500 {
		t.Fatalf("复制分支应保留先前状态：%+v", copied)
	}
	if copied.ResponseBodyText != "new-body" {
		t.Fatalf("ResponseBodyText = %q", copied.ResponseBodyText)
	}
}

// TestW1PDownstreamClosedAttemptOf：匹配先前尝试携带状态，否则无状态。
func TestW1PDownstreamClosedAttemptOf(t *testing.T) {
	input := gatewaydispatch.UpstreamRequestErrorInput{
		Account:     gatewaydispatch.AccountCandidate{ID: "acc-1", Name: "账户一"},
		UpstreamURL: "https://up.example/v1/responses",
	}
	attempt := downstreamClosedAttemptOf(input)
	if attempt.AccountID != "acc-1" || attempt.AccountName != "账户一" {
		t.Fatalf("账户投影 = %q / %q", attempt.AccountID, attempt.AccountName)
	}
	if attempt.Message != gatewayresponse.DownstreamConnectionClosedMessage {
		t.Fatalf("Message = %q，want %q", attempt.Message, gatewayresponse.DownstreamConnectionClosedMessage)
	}
	if attempt.HasStatus {
		t.Fatal("无先前尝试不应有状态")
	}

	match := input
	match.LastAttempt = &gatewaydispatch.UpstreamAttempt{
		AccountID: "acc-1", UpstreamURL: input.UpstreamURL, Status: 503, HasStatus: true,
	}
	attempt = downstreamClosedAttemptOf(match)
	if !attempt.HasStatus || attempt.Status != 503 {
		t.Fatalf("匹配先前尝试应携带状态：%+v", attempt)
	}

	mismatch := input
	mismatch.LastAttempt = &gatewaydispatch.UpstreamAttempt{
		AccountID: "acc-other", UpstreamURL: input.UpstreamURL, Status: 503, HasStatus: true,
	}
	attempt = downstreamClosedAttemptOf(mismatch)
	if attempt.HasStatus {
		t.Fatalf("账户不匹配不应携带状态：%+v", attempt)
	}
}

// TestW1PTransportFailureAttemptOf：复制 + 覆盖 + 传输失败种类附加。
func TestW1PTransportFailureAttemptOf(t *testing.T) {
	input := gatewaydispatch.UpstreamRequestErrorInput{
		Account:     gatewaydispatch.AccountCandidate{ID: "acc-3", Name: "账户三", ProviderCode: "openai"},
		UpstreamURL: "https://up.example/v1/chat/completions",
		LastAttempt: &gatewaydispatch.UpstreamAttempt{AccountID: "acc-old", ErrorCode: "E_KEEP"},
	}
	attempt := transportFailureAttemptOf(input, "dial failed", gatewaydispatch.TransportFailureKindConnection)
	if attempt.AccountID != "acc-3" || attempt.AccountName != "账户三" || attempt.ProviderCode != "openai" {
		t.Fatalf("账户覆盖不匹配：%+v", attempt)
	}
	if attempt.UpstreamURL != input.UpstreamURL || attempt.Message != "dial failed" {
		t.Fatalf("URL/消息 = %q / %q", attempt.UpstreamURL, attempt.Message)
	}
	if attempt.TransportFailureKind != gatewaydispatch.TransportFailureKindConnection {
		t.Fatalf("TransportFailureKind = %q", attempt.TransportFailureKind)
	}
	if attempt.ErrorCode != "E_KEEP" {
		t.Fatalf("复制字段 ErrorCode = %q，want E_KEEP", attempt.ErrorCode)
	}
}

// ---------------------------------------------------------------------------
// 错误消息格式化 / 用量载荷投影
// ---------------------------------------------------------------------------

// TestW1PFormatUpstreamRequestErrorMessage：非空消息透传，空/nil 降级。
func TestW1PFormatUpstreamRequestErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil错误", nil, "请求失败"},
		{"空白消息", errors.New("   "), "请求失败"},
		{"两端空白", errors.New("  dial failed  "), "dial failed"},
		{"普通消息", errors.New("connection refused"), "connection refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatUpstreamRequestErrorMessage(tc.err); got != tc.want {
				t.Fatalf("formatUpstreamRequestErrorMessage(%v) = %q，want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestW1PFormatUpstreamRequestTransportFailureKind：超时判定优先，
// 其余按响应是否开始分 connection / read_incomplete。
func TestW1PFormatUpstreamRequestTransportFailureKind(t *testing.T) {
	started := &gatewaydispatch.UpstreamAttempt{Status: 200, HasStatus: true}
	notStarted := &gatewaydispatch.UpstreamAttempt{}
	cases := []struct {
		name string
		err  error
		prev *gatewaydispatch.UpstreamAttempt
		want string
	}{
		{"timeout字样", errors.New("dial tcp: i/o timeout"), nil, gatewaydispatch.TransportFailureKindTimeout},
		{"timed out字样", errors.New("request timed out"), nil, gatewaydispatch.TransportFailureKindTimeout},
		{"etimedout字样", errors.New("syscall: ETIMEDOUT"), nil, gatewaydispatch.TransportFailureKindTimeout},
		{"中文超时", errors.New("上游请求超时"), nil, gatewaydispatch.TransportFailureKindTimeout},
		{"无先前尝试", errors.New("connection refused"), nil, gatewaydispatch.TransportFailureKindConnection},
		{"响应未开始", errors.New("connection refused"), notStarted, gatewaydispatch.TransportFailureKindConnection},
		{"响应已开始", errors.New("connection refused"), started, gatewaydispatch.TransportFailureKindReadIncomplete},
		{"nil错误且响应已开始", nil, started, gatewaydispatch.TransportFailureKindReadIncomplete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatUpstreamRequestTransportFailureKind(tc.err, tc.prev)
			if got != tc.want {
				t.Fatalf("failure kind = %q，want %q", got, tc.want)
			}
		})
	}
}

// TestW1PUsageErrorPayloadAndPointers：usageErrorPayloadOf / statusPointer /
// requestMethodOf。
func TestW1PUsageErrorPayloadAndPointers(t *testing.T) {
	if got := usageErrorPayloadOf(gatewayproto.ErrorPayload{}); got != nil {
		t.Fatalf("空载荷 = %v，want nil", got)
	}
	got := usageErrorPayloadOf(gatewayproto.ErrorPayload{Code: "rate_limited", Type: "server_error", Message: "限流"})
	value, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("载荷类型 = %T，want map[string]any", got)
	}
	if len(value) != 3 || value["code"] != "rate_limited" || value["type"] != "server_error" || value["message"] != "限流" {
		t.Fatalf("载荷字段 = %v", value)
	}
	partial := usageErrorPayloadOf(gatewayproto.ErrorPayload{Type: "request_error"})
	partialMap, ok := partial.(map[string]any)
	if !ok || len(partialMap) != 1 || partialMap["type"] != "request_error" {
		t.Fatalf("部分载荷 = %v", partial)
	}

	if statusPointer(false, 5) != nil {
		t.Fatal("statusPointer(false,5) 应为 nil")
	}
	if pointer := statusPointer(true, 5); pointer == nil || *pointer != 5 {
		t.Fatalf("statusPointer(true,5) = %v", pointer)
	}
	if got := requestMethodOf(nil); got != "" {
		t.Fatalf("requestMethodOf(nil) = %q，want 空串", got)
	}
}

// ---------------------------------------------------------------------------
// preauth 协作适配器
// ---------------------------------------------------------------------------

// TestW1PClientStrategyAdapter：nil deps 降级 generic_openai；真实 deps 透传
// G18 解析结果；AuditMetadata 输出三个键。
func TestW1PClientStrategyAdapter(t *testing.T) {
	req := w1pGatewayRequest(http.MethodPost, "/v1/chat/completions", nil)
	input := gatewaypreauth.ClientStrategyInput{
		SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "g-1",
		Endpoint: "/v1/chat/completions", ProviderCode: "openai", ClientIP: "127.0.0.1",
	}

	degraded := clientStrategyAdapter{}.Resolve(req, input)
	if degraded.ClientProfile != gatewaycodex.ClientProfileGenericOpenAI {
		t.Fatalf("nil deps ClientProfile = %q，want %q", degraded.ClientProfile, gatewaycodex.ClientProfileGenericOpenAI)
	}
	if degraded.Opaque != nil {
		t.Fatalf("nil deps Opaque = %T，want nil", degraded.Opaque)
	}

	deps := &gatewaycodex.ClientStrategyDeps{}
	resolved := deps.ResolveOpenAIGatewayClientStrategy(req, gatewaycodex.ClientStrategyIdentity{
		SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "g-1",
		Endpoint: "/v1/chat/completions", ProviderCode: "openai", ClientIP: "127.0.0.1",
	})
	bridged := clientStrategyAdapter{deps: deps}.Resolve(req, input)
	if bridged.ClientProfile != resolved.ClientProfile || bridged.DownstreamProtocol != resolved.DownstreamProtocol ||
		bridged.RequestClientCompatibility != resolved.RequestClientCompatibility {
		t.Fatalf("deps 桥接结果不一致：%+v vs %+v", bridged, resolved)
	}
	if resolved.ClientProfile != gatewaycodex.ClientProfileGenericOpenAI {
		t.Fatalf("普通请求 profile = %q，want generic_openai", resolved.ClientProfile)
	}
	opaque, ok := bridged.Opaque.(gatewaycodex.OpenAIGatewayClientStrategyContext)
	if !ok {
		t.Fatalf("Opaque 类型 = %T，want OpenAIGatewayClientStrategyContext", bridged.Opaque)
	}
	if opaque.ClientProfile != resolved.ClientProfile {
		t.Fatalf("Opaque 不一致：%+v", opaque)
	}

	metadata := clientStrategyAdapter{deps: deps}.AuditMetadata(bridged)
	if metadata["clientProfile"] != bridged.ClientProfile ||
		metadata["downstreamProtocol"] != bridged.DownstreamProtocol ||
		metadata["requestClientCompatibility"] != bridged.RequestClientCompatibility {
		t.Fatalf("AuditMetadata = %v", metadata)
	}
	if _, ok := metadata["clientProfile"]; !ok {
		t.Fatal("AuditMetadata 缺少 clientProfile 键")
	}
}

// TestW1PSessionIdentityAdapter：nil req、无服务头部透传、服务已装配的
// resolved 与 missing 分支。
func TestW1PSessionIdentityAdapter(t *testing.T) {
	input := gatewaypreauth.SessionIdentityInput{ClientProfile: "codex", SystemAccountID: "sys-1", APIKeyID: "key-9"}

	if identity := (sessionIdentityAdapter{}).ResolveGatewaySessionIdentity(nil, input); identity.SessionID != "" || identity.ConversationKey != "" {
		t.Fatalf("nil req = %+v，want 空身份", identity)
	}

	passthrough := sessionIdentityAdapter{}.ResolveGatewaySessionIdentity(
		w1pGatewayRequest(http.MethodPost, "/v1/responses", map[string]string{
			"x-session-id":       "  sess-42\t",
			"x-conversation-key": "\tconv-9 ",
		}), input)
	if passthrough.SessionID != "sess-42" || passthrough.ConversationKey != "conv-9" {
		t.Fatalf("头部透传 = %+v", passthrough)
	}
	empty := sessionIdentityAdapter{}.ResolveGatewaySessionIdentity(
		w1pGatewayRequest(http.MethodPost, "/v1/responses", nil), input)
	if empty.SessionID != "" || empty.ConversationKey != "" {
		t.Fatalf("无头部透传 = %+v，want 空身份", empty)
	}

	identityService, err := gatewaysession.NewIdentityService("w1p-hmac-secret")
	if err != nil {
		t.Fatalf("NewIdentityService：%v", err)
	}
	services := &sessionIdentityServices{Identity: identityService, Secret: "w1p-hmac-secret"}

	resolvedReq := w1pGatewayRequest(http.MethodPost, "/v1/responses", map[string]string{"session-id": "abc-session-123"})
	resolved := (sessionIdentityAdapter{services: services}).ResolveGatewaySessionIdentity(resolvedReq, input)
	if resolved.SessionID != "abc-session-123" || resolved.ConversationKey == "" {
		t.Fatalf("resolved 身份 = %+v", resolved)
	}
	again := (sessionIdentityAdapter{services: services}).ResolveGatewaySessionIdentity(
		w1pGatewayRequest(http.MethodPost, "/v1/responses", map[string]string{"session-id": "abc-session-123"}), input)
	if again.ConversationKey != resolved.ConversationKey {
		t.Fatalf("会话键不确定：%q vs %q", again.ConversationKey, resolved.ConversationKey)
	}

	// 服务已装配但身份 missing：不再回落到头部透传。
	missing := (sessionIdentityAdapter{services: services}).ResolveGatewaySessionIdentity(
		w1pGatewayRequest(http.MethodGet, "/v1/models", map[string]string{"x-session-id": "header-only"}), input)
	if missing.SessionID != "" || missing.ConversationKey != "" {
		t.Fatalf("missing 分支 = %+v，want 空身份", missing)
	}
}

// TestW1PCodexSourceSessionAdapter：nil 服务 / nil 请求 / resolved 投影。
func TestW1PCodexSourceSessionAdapter(t *testing.T) {
	input := gatewaypreauth.SessionIdentityInput{ClientProfile: "codex", SystemAccountID: "sys-1", APIKeyID: "key-9"}
	if identity := (codexSourceSessionAdapter{}).ResolveSessionIdentity(
		w1pGatewayRequest(http.MethodPost, "/v1/responses", nil), input); identity.Status != gatewaysession.IdentityStatusMissing {
		t.Fatalf("nil 服务 Status = %q，want missing", identity.Status)
	}
	identityService, err := gatewaysession.NewIdentityService("w1p-hmac-secret")
	if err != nil {
		t.Fatalf("NewIdentityService：%v", err)
	}
	if identity := (codexSourceSessionAdapter{identity: identityService}).ResolveSessionIdentity(nil, input); identity.Status != gatewaysession.IdentityStatusMissing {
		t.Fatalf("nil 请求 Status = %q，want missing", identity.Status)
	}
	identity := (codexSourceSessionAdapter{identity: identityService}).ResolveSessionIdentity(
		w1pGatewayRequest(http.MethodPost, "/v1/responses", map[string]string{"session-id": "abc-session-123"}), input)
	if identity.Status != gatewaysession.IdentityStatusResolved {
		t.Fatalf("resolved Status = %q", identity.Status)
	}
	if identity.SessionID != "abc-session-123" || identity.ConversationKey == "" {
		t.Fatalf("resolved 身份 = %+v", identity)
	}
}

// TestW1PSessionAffinityAdapter：未装配与已装配（HMAC 键确定性）分支。
func TestW1PSessionAffinityAdapter(t *testing.T) {
	scope := gatewaypreauth.SessionAffinityScope{
		SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "group-1", RouteStrategyID: "rs-1",
	}

	if key, ok := (sessionAffinityAdapter{}).ResolveKeyFromClientSource(nil, scope); key != "" || ok {
		t.Fatalf("未装配 ResolveKeyFromClientSource = (%q,%v)", key, ok)
	}
	if key, ok := (sessionAffinityAdapter{}).ResolveKey(gatewaypreauth.SessionIdentity{ConversationKey: "conv-a"}, scope); key != "" || ok {
		t.Fatalf("未装配 ResolveKey = (%q,%v)", key, ok)
	}
	emptyAffinity := &sessionIdentityServices{}
	if key, ok := (sessionAffinityAdapter{services: emptyAffinity}).ResolveKey(gatewaypreauth.SessionIdentity{ConversationKey: "conv-a"}, scope); key != "" || ok {
		t.Fatalf("Affinity 为 nil 的 ResolveKey = (%q,%v)", key, ok)
	}

	affinityService, err := gatewaysession.NewAffinityService(gatewaysession.AffinityConfig{Secret: "w1p-hmac-secret"})
	if err != nil {
		t.Fatalf("NewAffinityService：%v", err)
	}
	services := &sessionIdentityServices{Affinity: affinityService, Secret: "w1p-hmac-secret"}
	adapter := sessionAffinityAdapter{services: services}

	if key, ok := adapter.ResolveKey(gatewaypreauth.SessionIdentity{}, scope); key != "" || ok {
		t.Fatalf("空会话键 ResolveKey = (%q,%v)", key, ok)
	}
	first, ok := adapter.ResolveKey(gatewaypreauth.SessionIdentity{ConversationKey: "conv-a"}, scope)
	if !ok || first == "" {
		t.Fatalf("ResolveKey = (%q,%v)，want 非空键", first, ok)
	}
	second, _ := adapter.ResolveKey(gatewaypreauth.SessionIdentity{ConversationKey: "conv-a"}, scope)
	if first != second {
		t.Fatalf("会话亲和键不确定：%q vs %q", first, second)
	}
	other, _ := adapter.ResolveKey(gatewaypreauth.SessionIdentity{ConversationKey: "conv-b"}, scope)
	if other == first {
		t.Fatal("不同会话键不应得到相同亲和键")
	}

	if key, ok := adapter.ResolveKeyFromClientSource(nil, scope); key != "" || ok {
		t.Fatalf("nil clientSource = (%q,%v)", key, ok)
	}
	if key, ok := adapter.ResolveKeyFromClientSource(&gatewaypreauth.ClientSource{
		SessionIdentity: &gatewaypreauth.SessionIdentity{ConversationKey: "   "},
	}, scope); key != "" || ok {
		t.Fatalf("空白客户端键 = (%q,%v)", key, ok)
	}
	clientKey, ok := adapter.ResolveKeyFromClientSource(&gatewaypreauth.ClientSource{
		SessionIdentity: &gatewaypreauth.SessionIdentity{ConversationKey: "conv-a"},
	}, scope)
	if !ok || clientKey != first {
		t.Fatalf("客户端来源键 = (%q,%v)，want 与 ResolveKey 一致的 %q", clientKey, ok, first)
	}
}

// TestW1PGatewaySessionAffinityScopeOf：scope 字段映射（含 HMAC secret）。
func TestW1PGatewaySessionAffinityScopeOf(t *testing.T) {
	scope := gatewaypreauth.SessionAffinityScope{
		SystemAccountID: "sys-9", APIKeyID: "key-9", GroupID: "group-9", RouteStrategyID: "rs-9",
	}
	mapped := gatewaySessionAffinityScopeOf(scope, "w1p-secret")
	if mapped.HMACSecret != "w1p-secret" || mapped.SystemAccountID != "sys-9" ||
		mapped.APIKeyID != "key-9" || mapped.GroupID != "group-9" || mapped.RouteStrategyID != "rs-9" {
		t.Fatalf("scope 映射 = %+v", mapped)
	}
}

// ---------------------------------------------------------------------------
// 头部裁剪 / 协议门 / codex preflight
// ---------------------------------------------------------------------------

// TestW1PTrimSpaceLocal：仅裁剪首尾空格与制表符。
func TestW1PTrimSpaceLocal(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空串", "", ""},
		{"纯空白", " \t \t ", ""},
		{"首部空白", " \tvalue", "value"},
		{"尾部空白", "value\t ", "value"},
		{"两端空白", "\t value \t", "value"},
		{"内部空白保留", "a b\tc", "a b\tc"},
		{"其他空白符不裁剪", "\nvalue\n", "\nvalue\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := trimSpaceLocal(tc.in); got != tc.want {
				t.Fatalf("trimSpaceLocal(%q) = %q，want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestW1PTrimmedHeader：取值并裁剪，nil 请求安全。
func TestW1PTrimmedHeader(t *testing.T) {
	req := w1pGatewayRequest(http.MethodPost, "/v1/responses", map[string]string{
		"x-session-id": "  sess-1\t", "x-missing": "",
	})
	if got := trimmedHeader(req, "x-session-id"); got != "sess-1" {
		t.Fatalf("trimmedHeader = %q，want sess-1", got)
	}
	if got := trimmedHeader(req, "x-missing"); got != "" {
		t.Fatalf("缺失头部 = %q，want 空串", got)
	}
	if got := trimmedHeader(nil, "x-session-id"); got != "" {
		t.Fatalf("nil 请求 = %q，want 空串", got)
	}
}

// TestW1PProtocolGateHelpers：openai 协议路径与 anthropic / gemini 原生请求。
func TestW1PProtocolGateHelpers(t *testing.T) {
	openaiCases := []struct {
		path string
		want bool
	}{
		{"/v1/chat/completions", true},
		{"/v1/responses", true},
		{"/v1/embeddings", true},
		{"/v1/models", true},
		{"/health", false},
		{"", false},
	}
	for _, tc := range openaiCases {
		if got := gatewayopenaiIsProtocolPath(tc.path); got != tc.want {
			t.Fatalf("gatewayopenaiIsProtocolPath(%q) = %v，want %v", tc.path, got, tc.want)
		}
	}

	anthropicCases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{"messages", http.MethodPost, "/v1/messages", true},
		{"count_tokens", http.MethodPost, "/v1/messages/count_tokens", true},
		{"models", http.MethodGet, "/v1/models", true},
		{"非原生路径", http.MethodPost, "/v1/complete", false},
		{"方法不符", http.MethodGet, "/v1/messages", false},
	}
	for _, tc := range anthropicCases {
		t.Run("anthropic/"+tc.name, func(t *testing.T) {
			if got := gatewayanthropicIsNative(httptest.NewRequest(tc.method, tc.path, nil)); got != tc.want {
				t.Fatalf("gatewayanthropicIsNative(%s %s) = %v，want %v", tc.method, tc.path, got, tc.want)
			}
		})
	}
	if gatewayanthropicIsNative(nil) {
		t.Fatal("gatewayanthropicIsNative(nil) 应为 false")
	}

	// E2E-FINDING #6：组合协议门是三协议并集（Node isGatewayProtocolRequest），
	// anthropic / gemini native 面放行，未知路径维持 404 兜底。
	gateCases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{"openai chat", http.MethodPost, "/v1/chat/completions", true},
		{"anthropic messages", http.MethodPost, "/v1/messages", true},
		{"anthropic messages query", http.MethodPost, "/v1/messages?beta=true", true},
		{"anthropic count_tokens", http.MethodPost, "/v1/messages/count_tokens", true},
		{"gemini generateContent", http.MethodPost, "/v1beta/models/gemini-test:generateContent", true},
		{"未知路径 404 兜底", http.MethodPost, "/v1/complete", false},
	}
	for _, tc := range gateCases {
		t.Run("gate/"+tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			if got := gatewayIsProtocolRequest(gatewaypreauth.NewGatewayRequest(request)); got != tc.want {
				t.Fatalf("gatewayIsProtocolRequest(%s %s) = %v，want %v", tc.method, tc.path, got, tc.want)
			}
		})
	}

	geminiCases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{"models", http.MethodGet, "/v1beta/models", true},
		{"generateContent", http.MethodPost, "/v1beta/models/gemini-pro:generateContent", true},
		{"非原生路径", http.MethodPost, "/v1beta/other", false},
		{"models需GET", http.MethodPost, "/v1beta/models", false},
	}
	for _, tc := range geminiCases {
		t.Run("gemini/"+tc.name, func(t *testing.T) {
			if got := gatewaygeminiIsNative(httptest.NewRequest(tc.method, tc.path, nil)); got != tc.want {
				t.Fatalf("gatewaygeminiIsNative(%s %s) = %v，want %v", tc.method, tc.path, got, tc.want)
			}
		})
	}
	if gatewaygeminiIsNative(nil) {
		t.Fatal("gatewaygeminiIsNative(nil) 应为 false")
	}
}

// TestW1PChainCodexBridgePreflight：nil 桥接降级为直通适配器；非 nil 原样
// 返回；降级适配器的三个方法保持“未完成”语义。
func TestW1PChainCodexBridgePreflight(t *testing.T) {
	fake := &w1pBridgePreflight{}
	if got := chainCodexBridgePreflight(fake); got != gatewaypreauth.CodexBridgePreflight(fake) {
		t.Fatal("非 nil 桥接应原样返回")
	}

	adapter := chainCodexBridgePreflight(nil)
	if adapter == nil {
		t.Fatal("nil 桥接应返回降级适配器")
	}
	if _, isAdapter := adapter.(codexPreflightAdapter); !isAdapter {
		t.Fatalf("降级实现类型 = %T，want codexPreflightAdapter", adapter)
	}
	completed, err := adapter.ApplyContextStatePreflight(context.Background(), gatewaypreauth.CodexContextStateInput{})
	if completed || err != nil {
		t.Fatalf("ApplyContextStatePreflight = (%v,%v)，want (false,nil)", completed, err)
	}
	accounts := []gatewaydispatch.AccountCandidate{{ID: "acc-1"}, {ID: "acc-2"}}
	compact, err := adapter.ApplyChatBridgeCompactPreflight(context.Background(), gatewaypreauth.CodexCompactPreflightInput{
		DispatchAccounts: accounts,
	})
	if err != nil {
		t.Fatalf("ApplyChatBridgeCompactPreflight 错误：%v", err)
	}
	if compact.Completed {
		t.Fatal("降级 compact preflight 不应标记完成")
	}
	if len(compact.Accounts) != 2 || compact.Accounts[0].ID != "acc-1" || compact.Accounts[1].ID != "acc-2" {
		t.Fatalf("派发账户被改写：%v", compact.Accounts)
	}
	if adapter.CompactionExpectedForRequest(w1pGatewayRequest(http.MethodPost, "/v1/responses", nil)) {
		t.Fatal("普通 /v1/responses 请求不应预期 compaction")
	}
	if !adapter.CompactionExpectedForRequest(w1pGatewayRequest(http.MethodPost, "/v1/responses/compact", nil)) {
		t.Fatal("POST /v1/responses/compact 应预期 compaction")
	}
}

// ---------------------------------------------------------------------------
// slogObservability / 阶段日志级别
// ---------------------------------------------------------------------------

// TestW1PSlogObservability：固定时钟 trace id、透传 URL、阶段日志级别与
// warn logger 适配。
func TestW1PSlogObservability(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	obs := newSlogObservability(logger, w1pClock{now: time.Unix(0, 1728000000000000000)})

	if got := obs.CreateTraceID(); got != "trace_1728000000000000000" {
		t.Fatalf("CreateTraceID = %q", got)
	}
	if got := obs.TraceID(); got != "" {
		t.Fatalf("TraceID = %q，want 空串（trace 由 /v1 编排器创建）", got)
	}
	if got := obs.SanitizeURLForLog(" http://x/y?a=b "); got != " http://x/y?a=b " {
		t.Fatalf("SanitizeURLForLog = %q，want 原样透传", got)
	}

	obs.Logger().Warn("w1p_event", map[string]any{"k1": "v1"}, "w1p警告消息")
	output := buffer.String()
	if !strings.Contains(output, "level=WARN") || !strings.Contains(output, "event=w1p_event") ||
		!strings.Contains(output, "w1p警告消息") || !strings.Contains(output, "k1=v1") {
		t.Fatalf("warn logger 输出不符：%s", output)
	}

	buffer.Reset()
	obs.LogRequestStage("auth", nil, "unexpected_failure", time.Now())
	output = buffer.String()
	if !strings.Contains(output, "level=ERROR") || !strings.Contains(output, "请求阶段未预期失败：auth") {
		t.Fatalf("unexpected_failure 输出不符：%s", output)
	}

	buffer.Reset()
	obs.LogRequestStage("dispatch", nil, "expected_failure", time.Now())
	output = buffer.String()
	if !strings.Contains(output, "level=WARN") || !strings.Contains(output, "请求阶段预期失败：dispatch") {
		t.Fatalf("expected_failure 输出不符：%s", output)
	}

	buffer.Reset()
	obs.LogRequestStage("forward", nil, "aborted", time.Now())
	output = buffer.String()
	if !strings.Contains(output, "level=WARN") || !strings.Contains(output, "请求阶段中断：forward") {
		t.Fatalf("aborted 输出不符：%s", output)
	}

	buffer.Reset()
	obs.LogRequestStage("route", nil, "success", time.Now().Add(-2*time.Second))
	output = buffer.String()
	if !strings.Contains(output, "level=INFO") || !strings.Contains(output, "请求慢阶段完成：route") {
		t.Fatalf("慢阶段输出不符：%s", output)
	}

	buffer.Reset()
	obs.LogRequestStage("route", nil, "success", time.Now())
	output = buffer.String()
	if !strings.Contains(output, "level=DEBUG") || !strings.Contains(output, "请求阶段完成：route") {
		t.Fatalf("快阶段输出不符：%s", output)
	}

	defaults := newSlogObservability(nil, nil)
	if defaults == nil || defaults.logger != slog.Default() {
		t.Fatal("nil logger 应回落 slog.Default()")
	}
}

// TestW1PGatewayRequestStageLogLevel：级别策略表。
func TestW1PGatewayRequestStageLogLevel(t *testing.T) {
	cases := []struct {
		name       string
		outcome    string
		durationMs int64
		want       string
	}{
		{"未预期失败", "unexpected_failure", 0, "error"},
		{"预期失败慢", "expected_failure", 5000, "warn"},
		{"中断", "aborted", 0, "warn"},
		{"慢阶段", "success", 1500, "info"},
		{"阈值边界", "success", 1000, "info"},
		{"快阶段", "success", 999, "debug"},
		{"未知结果", "", 10, "debug"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gatewayRequestStageLogLevel(tc.outcome, tc.durationMs); got != tc.want {
				t.Fatalf("gatewayRequestStageLogLevel(%q,%d) = %q，want %q", tc.outcome, tc.durationMs, got, tc.want)
			}
		})
	}
}

// TestW1PFmtInt64：十进制渲染（含 0 / 负数 / 极值）。
func TestW1PFmtInt64(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{-42, "-42"},
		{9223372036854775807, "9223372036854775807"},
		// 修复后契约：绝对值在无符号域计算，MinInt64 正常渲染
		//（历史缺陷：有符号取负溢出曾使此处返回 "-"）。
		{-9223372036854775808, "-9223372036854775808"},
		{-1, "-1"},
	}
	for _, tc := range cases {
		if got := fmtInt64(tc.in); got != tc.want {
			t.Fatalf("fmtInt64(%d) = %q，want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// disabled 桩
// ---------------------------------------------------------------------------

// TestW1PDisabledStubs：降级桩的空行为契约。
func TestW1PDisabledStubs(t *testing.T) {
	ctx := context.Background()
	accounts := []gatewaydispatch.AccountCandidate{{ID: "acc-1"}, {ID: "acc-2"}}

	suppression := &disabledSuppression{}
	filtered, err := suppression.FilterAsync(ctx, accounts, gatewaydispatch.SuppressionFilterOptions{})
	if err != nil {
		t.Fatalf("FilterAsync 错误：%v", err)
	}
	if len(filtered.Accounts) != 2 || filtered.Accounts[0].ID != "acc-1" || filtered.SuppressedCount != 0 {
		t.Fatalf("FilterAsync = %+v", filtered)
	}
	local, handled, err := suppression.ResolveLocalSuppressionFilter(ctx, gatewaydispatch.LocalSuppressionPreflightInput{Accounts: accounts})
	if err != nil || handled || local == nil {
		t.Fatalf("ResolveLocalSuppressionFilter = (%v,%v,%v)", local, handled, err)
	}
	if len(local.Accounts) != 2 || local.Accounts[1].ID != "acc-2" {
		t.Fatalf("本地预检账户被改写：%v", local.Accounts)
	}

	degradation := &disabledDegradation{}
	order := degradation.OrderGatewayAccountsByRuntimeDegradation(accounts, map[string]int{"acc-1": 5})
	if len(order.Accounts) != 2 || order.Accounts[0].ID != "acc-1" || order.Applied {
		t.Fatalf("OrderGatewayAccountsByRuntimeDegradation = %+v", order)
	}
	laneOrder, err := degradation.OrderWithLaneAsync(ctx, accounts, "lane-1", nil, nil)
	if err != nil || len(laneOrder.Accounts) != 2 || laneOrder.Accounts[1].ID != "acc-2" {
		t.Fatalf("OrderWithLaneAsync = (%+v,%v)", laneOrder, err)
	}
	syncOrder := degradation.OrderSync(accounts, nil)
	if len(syncOrder.Accounts) != 2 || syncOrder.Accounts[0].ID != "acc-1" {
		t.Fatalf("OrderSync = %+v", syncOrder)
	}

	locks := &disabledAccountLocks{}
	state, err := locks.FindStateAsync(ctx, "acc-1")
	if err != nil || state == nil || state.Generation != 0 || state.BlocksCrossAccount {
		t.Fatalf("FindStateAsync = (%v,%v)", state, err)
	}
	acquire, err := locks.AcquireRetryLeaseAsync(ctx, "acc-1", 1000)
	if err != nil || !acquire.Allowed {
		t.Fatalf("AcquireRetryLeaseAsync = (%+v,%v)", acquire, err)
	}
	if consumed, err := locks.ConsumeRetryLeaseAsync(ctx, "acc-1", "lease-1"); !consumed || err != nil {
		t.Fatalf("ConsumeRetryLeaseAsync = (%v,%v)", consumed, err)
	}
	if released, err := locks.ReleaseRetryLeaseAsync(ctx, gatewaydispatch.ReleaseRetryLeaseInput{AccountID: "acc-1", LeaseID: "lease-1"}); !released || err != nil {
		t.Fatalf("ReleaseRetryLeaseAsync = (%v,%v)", released, err)
	}
	if err := locks.AbandonRetryReservationAsync(ctx, gatewaydispatch.AccountLockRetryLease{AccountID: "acc-1", LeaseID: "lease-1"}); err != nil {
		t.Fatalf("AbandonRetryReservationAsync 错误：%v", err)
	}
	if err := locks.RecordFailureAsync(ctx, "acc-1", "source-1", nil); err != nil {
		t.Fatalf("RecordFailureAsync 错误：%v", err)
	}
	if err := locks.SettleDeadlineAsync(ctx, "acc-1", 1000, nil); err != nil {
		t.Fatalf("SettleDeadlineAsync 错误：%v", err)
	}
	states, err := locks.ListStatesAsync(ctx, []string{"acc-1", "acc-2"})
	if err != nil || len(states) != 2 {
		t.Fatalf("ListStatesAsync = (%v,%v)", states, err)
	}
	if _, ok := states["acc-2"]; !ok {
		t.Fatalf("ListStatesAsync 缺少 acc-2：%v", states)
	}
}

// ---------------------------------------------------------------------------
// audit settings / usage model resolver / audit 派发适配器
// ---------------------------------------------------------------------------

// TestW1PAuditSettingsAndUsageModelResolver：enabled 函数边界与用量模型
// 直通解析。
func TestW1PAuditSettingsAndUsageModelResolver(t *testing.T) {
	if (auditSettingsAdapter{}).AuditLogEnabled() {
		t.Fatal("nil enabled 函数应返回 false")
	}
	if !(auditSettingsAdapter{enabled: func() bool { return true }}).AuditLogEnabled() {
		t.Fatal("enabled=true 应返回 true")
	}
	if (auditSettingsAdapter{enabled: func() bool { return false }}).AuditLogEnabled() {
		t.Fatal("enabled=false 应返回 false")
	}

	if settings := (auditSettingsSourceAdapter{}).ReadAuditLogSettings(); settings.Enabled {
		t.Fatalf("nil enabled 函数 ReadAuditLogSettings = %+v", settings)
	}
	if settings := (auditSettingsSourceAdapter{}).ReadAuditLogSettings(); settings.SuccessSampleRate != 0 || settings.SuccessHotRetentionHours != 0 {
		t.Fatalf("零值构造 ReadAuditLogSettings = %+v，want 采样字段 0", settings)
	}
	if settings := (auditSettingsSourceAdapter{enabled: func() bool { return true }}).ReadAuditLogSettings(); !settings.Enabled {
		t.Fatalf("ReadAuditLogSettings = %+v，want Enabled=true", settings)
	}
	// E2E-FINDING #10：采样字段构造透传（值源 auditlog.LoadConfig）。
	if settings := (auditSettingsSourceAdapter{successSampleRate: 0.1, successHotRetentionHours: 1}).ReadAuditLogSettings(); settings.SuccessSampleRate != 0.1 || settings.SuccessHotRetentionHours != 1 {
		t.Fatalf("ReadAuditLogSettings = %+v，want 采样字段透传 0.1/1", settings)
	}

	resolution := (usageModelResolverAdapter{}).ResolveUsageModel(
		gatewayusage.UsageModelAccount{ID: "acc-1"}, "gpt-w1p", "chat_completions")
	if resolution.UpstreamModel != "gpt-w1p" || resolution.ModelMappingApplied {
		t.Fatalf("ResolveUsageModel = %+v", resolution)
	}
	if resolution.SourceEndpointFamily != "chat_completions" || resolution.UpstreamEndpointFamily != "chat_completions" {
		t.Fatalf("端点族投影 = %+v", resolution)
	}
}

// TestW1PAuditDispatchAdapters：nil producer 静默丢弃；零值 Producer（store
// 为 nil）在 Capture 的同步 nil 检查处短路，不派生 goroutine，不 panic。
// 真实持久化断言需要等待 Capture 的 fire-and-forget goroutine，按“禁止并发
// 等待类测试”约束跳过，仅覆盖静默丢弃契约。
func TestW1PAuditDispatchAdapters(t *testing.T) {
	input := gatewaypreauth.DispatchedAuditLogInput{
		ID: "audit-1", LifecycleStatus: "final", TraceID: "trace-1", TrafficSource: "gateway",
		AuditOutcome: "sampled", Success: true, Method: http.MethodPost, Path: "/v1/chat/completions",
		QueryString: "", ClientIP: "127.0.0.1", UserAgent: "w1p-ua", FinalStatusCode: 200,
	}
	(auditDispatchAdapter{}).Dispatch(input)
	(auditDispatchAdapter{producer: &auditlog.Producer{}}).Dispatch(input)

	(auditUsageDispatcher{}).DispatchAuditLog(context.Background(), gatewayusage.AuditLogInput{
		ID: "audit-2", TraceID: "trace-2", Method: http.MethodPost, Path: "/v1/responses",
	})
	(auditUsageDispatcher{producer: &auditlog.Producer{}}).DispatchAuditLog(context.Background(), gatewayusage.AuditLogInput{
		ID: "audit-3", TraceID: "trace-3", Method: http.MethodGet, Path: "/v1/models",
	})
	// 调用后仍可继续执行即证明未 panic。
	t.Log("w1p audit dispatch 适配器静默丢弃契约通过")
}
