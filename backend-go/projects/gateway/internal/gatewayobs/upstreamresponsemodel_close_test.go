package gatewayobs

import (
	"io"
	"strings"
	"testing"
)

type closeRecordingBody struct {
	io.Reader
	closed int
}

func (body *closeRecordingBody) Close() error {
	body.closed++
	return nil
}

// BUG-0328 回归：发布包装的 Close 必须穿透 observingUpstreamBodyReader 到达
// 真实响应体——slotReleasingBody 持有全局并发槽，Close 链在本层断流即槽位
// 泄漏；提前终止时仍按已观察状态发布一次（既有契约）。
func TestPublishingObservedBodyCloseReachesInnerBody(t *testing.T) {
	body := &closeRecordingBody{Reader: strings.NewReader("data")}
	published := false
	wrapped := ObserveUpstreamResponseModelBodyPublishing(
		body,
		CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{}),
		func(string) { published = true },
	)
	if _, err := io.ReadAll(io.LimitReader(wrapped, 2)); err != nil {
		t.Fatalf("部分读取失败: %v", err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("Close 返回错误: %v", err)
	}
	if body.closed != 1 {
		t.Fatalf("内层响应体未被关闭: closed=%d", body.closed)
	}
	if !published {
		t.Fatal("提前 Close 应触发一次发布")
	}
}

// 包装非 Closer 的裸 reader 时 Close 必须保持 no-op 语义，不得 panic。
func TestObservingUpstreamBodyCloseWithoutCloserIsNoop(t *testing.T) {
	wrapped := ObserveUpstreamResponseModelBody(
		io.Reader(strings.NewReader("data")),
		CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{}),
	)
	closer, ok := wrapped.(io.Closer)
	if !ok {
		t.Fatal("observingUpstreamBodyReader 应实现 io.Closer")
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("Close 返回错误: %v", err)
	}
}
