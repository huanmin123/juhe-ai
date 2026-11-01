package main

// T91-9 / B3-H31 接线测试：J3b 派发路径（gateway_dispatch.Dispatcher →
// KeyModel 端口 → business/key_model_runtime.MemoryStore）同样执行固定契约
// 「同一 CapabilityKey 最多 2 个未提交请求」：第 3 个并发准入返回
// ErrAttemptBlocked，不同 Key 不受影响。组合根零配置分支的 keyModelGate 就是
// newJ3bMemoryKeyModelGate()（main.go），本测试按同一装配驱动真实 Dispatcher。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gatewaydispatch "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/gateway_dispatch"
	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
)

// t91BlockingClient 记录进入上游的次数并阻塞到 release 关闭。
type t91BlockingClient struct {
	entered chan struct{}
	release chan struct{}
}

func (c *t91BlockingClient) Do(*http.Request) (*http.Response, error) {
	c.entered <- struct{}{}
	<-c.release
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp-ok"}`)),
	}, nil
}

func t91WaitEntered(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("等待上游请求进入超时")
	}
}

func TestT91J3bDispatchPathEnforcesForegroundLimitTwo(t *testing.T) {
	gate := newJ3bMemoryKeyModelGate()
	client := &t91BlockingClient{entered: make(chan struct{}, 4), release: make(chan struct{})}
	// 与 main.go 零配置分支同款装配：keyModelGate + NewMemoryCircuitGate()。
	dispatcher := &gatewaydispatch.Dispatcher{Client: client, KeyModel: gate, Circuit: gatewaydispatch.NewMemoryCircuitGate()}
	circuitInput := func() *gatewaydispatch.AccountCircuitInput {
		return &gatewaydispatch.AccountCircuitInput{
			AccountID:                 "acc-1",
			RequestLane:               "text",
			Model:                     "gpt-5.6",
			DispatchRevision:          1,
			ConfirmationLeaseDuration: time.Minute,
			ConfirmationEligible:      true,
			FailureEvidenceKey:        "t91-j3b",
		}
	}

	capability := keymodelruntime.Capability{
		CredentialSourceAccountID: "acc-1",
		KeyFingerprint:            "fp-key-1",
		ClientModel:               "gpt-5.6",
		ClientEndpointFamily:      "responses",
		FinalUpstreamModel:        "gpt-5.6",
		UpstreamEndpointMode:      "responses",
		DispatchRevision:          1,
	}
	dispatchAsync := func(attemptID string, capability keymodelruntime.Capability) <-chan error {
		done := make(chan error, 1)
		go func() {
			request := httptest.NewRequest(http.MethodPost, "http://upstream.test/v1/responses", nil)
			_, err := dispatcher.Dispatch(context.Background(), gatewaydispatch.Request{
				HTTP:           request,
				Capability:     capability,
				AttemptID:      attemptID,
				AccountCircuit: circuitInput(),
			})
			done <- err
		}()
		return done
	}

	first := dispatchAsync("attempt-1", capability)
	t91WaitEntered(t, client.entered)
	second := dispatchAsync("attempt-2", capability)
	t91WaitEntered(t, client.entered)

	// 前两个 permit 未释放：同 CapabilityKey 第 3 个并发准入必须被拒。
	request := httptest.NewRequest(http.MethodPost, "http://upstream.test/v1/responses", nil)
	if _, err := dispatcher.Dispatch(context.Background(), gatewaydispatch.Request{
		HTTP:           request,
		Capability:     capability,
		AttemptID:      "attempt-3",
		AccountCircuit: circuitInput(),
	}); !errors.Is(err, gatewaydispatch.ErrAttemptBlocked) {
		t.Fatalf("第 3 个并发准入必须 ErrAttemptBlocked, err = %v", err)
	}

	// 不同 Key（不同 CapabilityKey）不受 2 的约束，真实进入上游。
	otherKey := capability
	otherKey.KeyFingerprint = "fp-key-2"
	other := dispatchAsync("attempt-other", otherKey)
	t91WaitEntered(t, client.entered)

	close(client.release)
	for name, done := range map[string]<-chan error{"attempt-1": first, "attempt-2": second, "attempt-other": other} {
		if err := <-done; err != nil {
			t.Fatalf("%s dispatch 失败: %v", name, err)
		}
	}
}
