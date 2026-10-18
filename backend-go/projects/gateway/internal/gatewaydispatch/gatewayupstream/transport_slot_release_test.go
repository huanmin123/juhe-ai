package gatewayupstream

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayobs"
)

// BUG-0328 决定性回归：响应体被网关链的模型观察器包装后（chain_compose.go
// 装配形态），提前 Close 必须仍能归还全局并发槽。修复前 Close 链在
// observingUpstreamBodyReader 断流，本用例会阻塞到第二次 Acquire 超时。
func TestSlotReleasingBodyCloseThroughObserverWrapperReleasesGlobalSlot(t *testing.T) {
	governor := NewBoundedConcurrencyGovernor(1)
	release, err := governor.Acquire(context.Background())
	if err != nil {
		t.Fatalf("首次获取槽位失败: %v", err)
	}
	body := newSlotReleasingBodyWithCancel(io.NopCloser(strings.NewReader("upstream-body")), release, func() {}, 0)
	wrapped := gatewayobs.ObserveUpstreamResponseModelBodyPublishing(
		body,
		gatewayobs.CreateUpstreamResponseModelObservation(gatewayobs.UpstreamResponseModelObserverOptions{}),
		func(string) {},
	)
	if _, err := io.ReadFull(wrapped, make([]byte, 4)); err != nil {
		t.Fatalf("部分读取失败: %v", err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("提前 Close 返回错误: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	second, err := governor.Acquire(ctx)
	if err != nil {
		t.Fatalf("提前 Close 后全局槽未归还（客户端断开/上游中断即泄漏）: %v", err)
	}
	second()
}

// 基线：读尽 EOF 的归还路径保持不变。
func TestSlotReleasingBodyEOFReleasesGlobalSlot(t *testing.T) {
	governor := NewBoundedConcurrencyGovernor(1)
	release, err := governor.Acquire(context.Background())
	if err != nil {
		t.Fatalf("首次获取槽位失败: %v", err)
	}
	body := newSlotReleasingBodyWithCancel(io.NopCloser(strings.NewReader("done")), release, func() {}, 0)
	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("读尽响应体失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	second, err := governor.Acquire(ctx)
	if err != nil {
		t.Fatalf("EOF 后全局槽未归还: %v", err)
	}
	second()
}
