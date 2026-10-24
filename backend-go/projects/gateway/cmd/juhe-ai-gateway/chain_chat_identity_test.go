package main

// AI 问答内部身份通道 + 使用日志结构性抑制的护栏测试（W-A 批）：
//
//  1. chatInternalIdentityFromContext 三条分支（nil ctx / 空 owner / 正确注入）；
//  2. 外部 HTTP 请求不可伪造内部身份（真实 *http.Request 的 context 解析恒
//     false；NewGatewayRequest 产出结构体两字段恒零值——字段无任何 HTTP 映射，
//     唯一写入点是 handleOpenAIGatewayRequest 中经未导出 context key 的进程内
//     注入，等价于整条 chain 级的外部不可伪造断言）；
//  3. SuppressUsage 记录在持久化唯一闸口（FinalizationDispatch.
//     DispatchUsageRecord 入口，覆盖正常入队与溢出 spool 两臂）与
//     recorder 第二道保险（spooledUsageRecorder.EnqueueUsageRecord）都被
//     丢弃：返回 nil、计数不增、spool 不落盘，且闸口对未抑制记录仍然可用
//     （对照记录正常落盘）；
//  4. chatDispatchSystemAccountID 身份源：内部身份优先于 Runtime.APIKey，
//     无内部身份时回落运行时快照。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// TestChatInternalIdentityFromContext分支：nil ctx / 空 owner / 正确注入，
// 另含 key 上类型不符的包内错误注入分支。
func TestChatInternalIdentityFromContext分支(t *testing.T) {
	if _, ok := chatInternalIdentityFromContext(nil); ok {
		t.Fatal("nil ctx 不得解析出内部身份")
	}
	if _, ok := chatInternalIdentityFromContext(context.Background()); ok {
		t.Fatal("未注入的 ctx 不得解析出内部身份")
	}
	if _, ok := chatInternalIdentityFromContext(contextWithChatInternalIdentity(context.Background(), chatInternalIdentity{})); ok {
		t.Fatal("OwnerSystemAccountID 为空串不构成内部身份")
	}
	// 未导出类型包外无法构造；错误类型值经包内路径也不得误读（同
	// TestChatDispatchTargetFromContextRejectsExternal 形态）。
	if _, ok := chatInternalIdentityFromContext(context.WithValue(context.Background(), chatInternalIdentityContextKey{}, "owner")); ok {
		t.Fatal("非内部身份类型值不得解析为内部身份")
	}
	identity, ok := chatInternalIdentityFromContext(contextWithChatInternalIdentity(context.Background(), chatInternalIdentity{OwnerSystemAccountID: "owner-1"}))
	if !ok || identity.OwnerSystemAccountID != "owner-1" {
		t.Fatalf("正确注入解析 = (%+v, %v)，期望 (owner-1, true)", identity, ok)
	}
}

// TestExternalRequestCannotCarryInternalIdentity：真实外部 HTTP 请求（带
// authorization header 与请求体）不可能携带内部身份。
//
// 说明：整条 gatewayChain.ServeHTTP 级断言（经 fixture 起 chain 再打请求）与
// 此处等价——内部身份的唯一写入点是 handleOpenAIGatewayRequest 里
// chatInternalIdentityFromContext(ctx) 命中后的显式赋值，而外部请求 context
// 由 net/http 服务端构造，不可能携带未导出 key。因此直接断言
// gatewaypreauth.NewGatewayRequest(r) 产出的结构体两字段为零值：GatewayRequest
// 的这两个字段没有任何 header / query / body 映射，外部请求不可能携带内部
// 身份（SuppressUsage 同理，仅随内部身份一起置位）。
func TestExternalRequestCannotCarryInternalIdentity(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-x"}`))
	request.Header.Set("authorization", "Bearer sk-external")
	request.Header.Set("x-internal-owner", "owner-forged")
	if _, ok := chatInternalIdentityFromContext(request.Context()); ok {
		t.Fatal("外部请求 context 不得解析出内部身份")
	}
	req := gatewaypreauth.NewGatewayRequest(request)
	if req.InternalOwnerSystemAccountID != "" {
		t.Fatalf("外部请求构造的 GatewayRequest.InternalOwnerSystemAccountID = %q，期望零值", req.InternalOwnerSystemAccountID)
	}
	if req.SuppressUsage {
		t.Fatal("外部请求构造的 GatewayRequest.SuppressUsage = true，期望零值 false")
	}
}

// spooledUsageRecorderCounters 读取 recorder 的 failed/dropped 计数。
func spooledUsageRecorderCounters(r *spooledUsageRecorder) (failed, dropped int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed, r.dropped
}

// spoolEntryNames 列出 spool 落盘文件名；目录不存在视为空（未发生任何持久化）。
func spoolEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "gateway-chain"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("读取 spool 目录 = %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// TestSpooledUsageRecorderSuppressUsage丢弃不落盘：抑制记录在持久化唯一闸口
// 丢弃——返回 nil、failed/dropped 计数不变、spool 零落盘；对照未抑制记录照常
// 落盘，证明闸口本身未被关闭。
func TestSpooledUsageRecorderSuppressUsage丢弃不落盘(t *testing.T) {
	dir := t.TempDir()
	spool := newUsageSpool(dir, gatewaypreauth.SystemClock{}, newTestSlogLogger(), usageSpoolCapacity{})
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 8, Logger: newTestSlogLogger()}, spool)

	if err := recorder.EnqueueUsageRecord(t.Context(), gatewayusage.UsageRecordInput{
		TraceID: "trace-suppressed", TrafficSource: "gateway", Success: true, SuppressUsage: true,
	}); err != nil {
		t.Fatalf("抑制记录入队 = %v，期望 nil", err)
	}
	if failed, dropped := spooledUsageRecorderCounters(recorder); failed != 0 || dropped != 0 {
		t.Fatalf("抑制记录不得触碰计数：failed=%d dropped=%d，期望 0/0", failed, dropped)
	}
	if names := spoolEntryNames(t, dir); len(names) != 0 {
		t.Fatalf("抑制记录不得落盘，实际 spool 文件 %v", names)
	}

	// 对照组：未抑制记录经同一闸口照常落盘（闸口可用性不被抑制分支破坏）。
	if err := recorder.EnqueueUsageRecord(t.Context(), gatewayusage.UsageRecordInput{
		TraceID: "trace-control", TrafficSource: "gateway", Success: true,
	}); err != nil {
		t.Fatalf("对照记录入队 = %v，期望 nil", err)
	}
	recorder.Close()

	names := spoolEntryNames(t, dir)
	if len(names) != 1 {
		t.Fatalf("对照记录应恰好落盘 1 个文件，实际 %v", names)
	}
	content, err := os.ReadFile(filepath.Join(dir, "gateway-chain", names[0]))
	if err != nil {
		t.Fatalf("读取 spool 文件 = %v", err)
	}
	var record gatewayusage.UsageRecordInput
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatalf("解析 spool 记录 = %v", err)
	}
	if record.TraceID != "trace-control" {
		t.Fatalf("spool 记录 traceId = %q，期望对照记录 trace-control（抑制记录未落盘）", record.TraceID)
	}
	if failed, dropped := spooledUsageRecorderCounters(recorder); failed != 0 || dropped != 0 {
		t.Fatalf("对照组计数应保持 0/0：failed=%d dropped=%d", failed, dropped)
	}
}

// TestChatDispatchSystemAccountID身份源：内部身份优先于 Runtime.APIKey 快照，
// 无内部身份时回落运行时快照，两者皆缺返回空串。
func TestChatDispatchSystemAccountID身份源(t *testing.T) {
	if got := chatDispatchSystemAccountID(nil); got != "" {
		t.Fatalf("nil req = %q，期望空串", got)
	}
	withRuntime := &gatewaypreauth.GatewayRequest{
		Runtime: &gatewayruntimecache.GatewayRuntime{
			APIKey: &gatewayruntimecache.GatewayAPIKeyRow{SystemAccountID: "owner-key"},
		},
	}
	if got := chatDispatchSystemAccountID(withRuntime); got != "owner-key" {
		t.Fatalf("无内部身份回落运行时快照 = %q，期望 owner-key", got)
	}
	internal := &gatewaypreauth.GatewayRequest{
		InternalOwnerSystemAccountID: "owner-internal",
		Runtime: &gatewayruntimecache.GatewayRuntime{
			APIKey: &gatewayruntimecache.GatewayAPIKeyRow{SystemAccountID: "owner-key"},
		},
	}
	if got := chatDispatchSystemAccountID(internal); got != "owner-internal" {
		t.Fatalf("内部身份应优先于 Runtime.APIKey = %q，期望 owner-internal", got)
	}
	bare := &gatewaypreauth.GatewayRequest{}
	if got := chatDispatchSystemAccountID(bare); got != "" {
		t.Fatalf("两者皆缺 = %q，期望空串", got)
	}
}
