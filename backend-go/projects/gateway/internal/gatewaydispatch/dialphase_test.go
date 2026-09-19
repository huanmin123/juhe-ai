package gatewaydispatch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 建连前 dial 阶段（DNS 解析 / TCP dial）失败豁免回归测试：请求从未到达
// 上游，候选排除（failedAccountIDs / 换号扫描）保持不变，但不得喂账户熔断
// ——网关本机 DNS / 出口网络故障时，否则所有账户每次尝试都被记为已证实的
// 上游传输失败，批量污染熔断统计。非 dial 的 started transport 失败行为
// 完全不变（防回归对照）。

func TestIsPreConnectionDialErrorClassifiesDialPhase(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "tcp_dial_refused",
			err: &url.Error{Op: "Post", URL: "https://up.example/v1/chat/completions",
				Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}},
			want: true,
		},
		{
			name: "dns_nested_in_dial_op",
			err: &url.Error{Op: "Post", URL: "https://up.example/v1/chat/completions",
				Err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "up.example"}}},
			want: true,
		},
		{
			name: "bare_dns_error",
			err: &url.Error{Op: "Get", URL: "https://up.example/v1/chat/completions",
				Err: &net.DNSError{Err: "no such host", Name: "up.example"}},
			want: true,
		},
		{
			name: "post_connection_read_op",
			err: &url.Error{Op: "Post", URL: "https://up.example/v1/chat/completions",
				Err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}},
			want: false,
		},
		{
			name: "tls_verify_failure",
			err: &url.Error{Op: "Post", URL: "https://up.example/v1/chat/completions",
				Err: errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority")},
			want: false,
		},
		{name: "plain_error", err: errors.New("连接被重置"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range cases {
		if got := isPreConnectionDialError(tc.err); got != tc.want {
			t.Fatalf("%s: isPreConnectionDialError = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsDialPhaseStartedTransportErrorRequiresStartedWrapper(t *testing.T) {
	started := &StartedTransportError{Err: errors.New("dial tcp: connection refused")}
	if !IsDialPhaseStartedTransportError(&DialPhaseTransportError{Err: started}) {
		t.Fatal("started + dial 标记必须被识别为 dial 阶段 started 失败")
	}
	if IsDialPhaseStartedTransportError(&DialPhaseTransportError{Err: errors.New("缺少 started 包装")}) {
		t.Fatal("缺少 StartedTransportError 包装不得识别为 dial 阶段 started 失败")
	}
	if IsDialPhaseStartedTransportError(started) {
		t.Fatal("无 dial 标记的 started 失败不得识别为 dial 阶段")
	}
	if IsDialPhaseStartedTransportError(nil) {
		t.Fatal("nil 不得识别")
	}
}

// TestRequestUpstreamMarksDialPhaseStartedTransportError: 真实 client.Do 打到
// 已关闭端口，connection refused 属于建连前 dial 阶段，错误必须同时携带
// started 分类与 dial 标记。
func TestRequestUpstreamMarksDialPhaseStartedTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close() // 只借地址：端口已关闭 → 拒绝连接
	_, err := RequestUpstream(context.Background(), server.URL+"/v1/chat/completions", UpstreamRequestOptions{
		Method:          http.MethodPost,
		Body:            []byte(`{"model":"gpt-test"}`),
		DisableTimeouts: true,
	}, TransportDeps{})
	if err == nil {
		t.Fatal("已关闭端口必须失败")
	}
	var started *StartedTransportError
	if !errors.As(err, &started) {
		t.Fatalf("必须保持 started transport 分类, got %v", err)
	}
	var dialPhase *DialPhaseTransportError
	if !errors.As(err, &dialPhase) {
		t.Fatalf("connection refused 必须标记 dial 阶段, got %v", err)
	}
}

// newRecordingCircuitAttempt 构造挂真实 CircuitService 的熔断尝试，
// OnMutation 计数器捕获熔断喂食（suspect / 证据推进）。
func newRecordingCircuitAttempt(t *testing.T, accountID string) (*gatewaycircuit.Attempt, *atomic.Int64) {
	t.Helper()
	store, err := gatewaycircuit.NewMemoryStore(gatewaycircuit.MemoryStoreOptions{Capacity: 1024})
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	mutations := &atomic.Int64{}
	service, err := gatewaycircuit.NewCircuitService(store, gatewaycircuit.ServiceOptions{
		OnMutation: func(context.Context, gatewaycircuit.MutationEvent) error {
			mutations.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("circuit service: %v", err)
	}
	model := "gpt-test"
	preparation, err := service.PrepareAttempt(context.Background(), gatewaycircuit.PrepareAttemptInput{
		Account: gatewayruntimecache.OpenAIAccountSecret{
			ID:                        accountID,
			ProviderCode:              "openai",
			ProviderProtocolProfileID: "openai_profile",
			ProtocolCode:              "openai",
			ProtocolVersion:           "v1",
			Type:                      "api_key",
			APIKey:                    "sk-test",
		},
		RequestLane:                 gatewaycircuit.LaneText,
		Model:                       &model,
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil {
		t.Fatalf("prepare attempt: %v", err)
	}
	if preparation.Outcome != gatewaycircuit.PrepareDispatchable || preparation.Attempt == nil {
		t.Fatalf("prepare = (%s, %#v)", preparation.Outcome, preparation.Attempt)
	}
	return preparation.Attempt, mutations
}

// TestHandleUpstreamAttemptErrorDialPhaseSkipsCircuitButExcludesAccount:
// dial 阶段失败保持候选排除（failedAccountIDs），但不喂账户熔断。
func TestHandleUpstreamAttemptErrorDialPhaseSkipsCircuitButExcludesAccount(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	circuitAttempt, mutations := newRecordingCircuitAttempt(t, "a-1")
	dialErr := &PrimaryStartedGatewayTransportError{Err: &DialPhaseTransportError{Err: &StartedTransportError{
		Err: &url.Error{Op: "Post", URL: "https://up.example/v1/chat/completions",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")},
		},
	}}}
	_, kind, stop, err := harness.errorContext(testAccounts("a-1")[0], dialErr, func(errorCtx *upstreamAttemptErrorContext) {
		errorCtx.loop.in.accountCircuitAttempt = circuitAttempt
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if kind != errorKindHandled || stop.kind != errorStopSkipAccount {
		t.Fatalf("kind=%v stop=%#v", kind, stop)
	}
	if _, ok := harness.input.failedAccountIDs["a-1"]; !ok {
		t.Fatal("dial 阶段失败必须保持候选排除（failedAccountIDs）")
	}
	if mutations.Load() != 0 {
		t.Fatalf("dial 阶段失败不得喂账户熔断, mutations = %d", mutations.Load())
	}
}

// TestHandleUpstreamAttemptErrorNonDialTransportStillReportsCircuit: 非 dial
// 的 started transport 失败仍进账户熔断（防回归对照）。
func TestHandleUpstreamAttemptErrorNonDialTransportStillReportsCircuit(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	circuitAttempt, mutations := newRecordingCircuitAttempt(t, "a-1")
	started := &PrimaryStartedGatewayTransportError{Err: &StartedTransportError{Err: errors.New("连接被重置")}}
	_, kind, stop, err := harness.errorContext(testAccounts("a-1")[0], started, func(errorCtx *upstreamAttemptErrorContext) {
		errorCtx.loop.in.accountCircuitAttempt = circuitAttempt
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if kind != errorKindHandled || stop.kind != errorStopSkipAccount {
		t.Fatalf("kind=%v stop=%#v", kind, stop)
	}
	if _, ok := harness.input.failedAccountIDs["a-1"]; !ok {
		t.Fatal("非 dial transport 失败必须进入失败集合")
	}
	if mutations.Load() == 0 {
		t.Fatal("非 dial transport 失败必须喂账户熔断")
	}
}
