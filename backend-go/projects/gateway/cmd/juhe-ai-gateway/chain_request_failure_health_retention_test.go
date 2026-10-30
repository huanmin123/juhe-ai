package main

// BUG-0339 回归：失败健康检查派发标记必须随请求对象回收（Node per-request
// Symbol/WeakMap 语义）。旧实现是进程级 map[*GatewayRequest]*list.Element +
// 4096 FIFO，以请求指针为强键把整个 GatewayRequest（含 Body.RawBody 完整
// 请求体）钉在堆里直至淘汰，生产实测 ~1GB 活堆增长。本文件验证：
//  1. per-request 标记语义（同请求首次 false、标记后 true，请求间互不影响）；
//  2. 标记后请求可被 GC（runtime.SetFinalizer 必须触发，证明无全局强引用）；
//  3. 浅复制（model override clone）共享同一标记（同一逻辑请求只派发一次）。

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestRequestFailureHealthCheckMarkPerRequest(t *testing.T) {
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	other := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	if req.RequestFailureHealthCheckDispatched() {
		t.Fatal("首次查询必须为 false")
	}
	req.MarkRequestFailureHealthCheckDispatched()
	if !req.RequestFailureHealthCheckDispatched() {
		t.Fatal("标记后必须为 true")
	}
	if other.RequestFailureHealthCheckDispatched() {
		t.Fatal("两个不同请求互不得影响")
	}
	// 重复标记幂等。
	req.MarkRequestFailureHealthCheckDispatched()
	if !req.RequestFailureHealthCheckDispatched() {
		t.Fatal("重复标记后必须保持 true")
	}
}

// TestRequestFailureHealthCheckMarkReclaimedWithRequest 是关键回归：标记后的
// 请求对象（连同其 Body.RawBody）必须可被 GC。若派发标记存在任何进程级强
// 引用，finalizer 永不触发，本测试失败。
func TestRequestFailureHealthCheckMarkReclaimedWithRequest(t *testing.T) {
	finalized := make(chan struct{})
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	// 用生产量级的原始请求体放大旧缺陷的可观测性：被强键钉住即 ~2MB 驻留。
	req.Body = &gatewaybody.Request{RawBody: make([]byte, 2<<20)}

	req.MarkRequestFailureHealthCheckDispatched()
	if !req.RequestFailureHealthCheckDispatched() {
		t.Fatal("标记后必须为 true")
	}
	runtime.SetFinalizer(req, func(*gatewaypreauth.GatewayRequest) { close(finalized) })
	req = nil

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-finalized:
			return
		default:
		}
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("标记并置 nil 后请求未被 GC：finalizer 未触发，存在全局强引用钉住 GatewayRequest（BUG-0339 复发）")
}

func TestRequestFailureHealthCheckMarkSharedByShallowCopy(t *testing.T) {
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	// gatewaydispatch gatewayRequestWithModelOverride 同款浅复制：指针字段
	// 随之共享，副本与原请求是同一逻辑请求，标记必须互通。
	clone := *req
	if clone.RequestFailureHealthCheckDispatched() {
		t.Fatal("克隆初始必须未标记")
	}
	clone.MarkRequestFailureHealthCheckDispatched()
	if !req.RequestFailureHealthCheckDispatched() {
		t.Fatal("副本置位后原请求必须命中（浅复制共享标记）")
	}
}
