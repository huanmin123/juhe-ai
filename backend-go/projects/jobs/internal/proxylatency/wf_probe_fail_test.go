package proxylatency

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWFProbeItemGuards(t *testing.T) {
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	// ctx 已取消 → probe_cancelled。
	if result := ProbeItem(cancelled, ProbeRequest{TargetURL: "https://api.openai.com/v1", Timeout: time.Second}); result.ErrorCode != "probe_cancelled" {
		t.Fatalf("取消码=%q", result.ErrorCode)
	}
	// 目标 URL 非法 → target_url_invalid。
	if result := ProbeItem(ctx, ProbeRequest{TargetURL: "::bad", Timeout: time.Second}); result.ErrorCode != "target_url_invalid" {
		t.Fatalf("目标码=%q", result.ErrorCode)
	}
	// 超时非法 → timeout_invalid。
	if result := ProbeItem(ctx, ProbeRequest{TargetURL: "https://api.openai.com/v1", Timeout: 0}); result.ErrorCode != "timeout_invalid" {
		t.Fatalf("超时码=%q", result.ErrorCode)
	}
	// 缺代理 → proxy_required（透传到任务失败码）。
	if result := ProbeItem(ctx, ProbeRequest{TargetURL: "https://api.openai.com/v1", Timeout: time.Second}); result.ErrorCode != "proxy_required" {
		t.Fatalf("缺代理码=%q", result.ErrorCode)
	}
	// 代理 URL 非法 → proxy_invalid。
	if result := ProbeItem(ctx, ProbeRequest{TargetURL: "https://api.openai.com/v1", Timeout: time.Second, ProxyURL: "://bad"}); result.ErrorCode != "proxy_invalid" {
		t.Fatalf("代理非法码=%q", result.ErrorCode)
	}
	// 不可达代理（本地拒绝端口）→ 传输失败且快速返回。
	result := ProbeItem(ctx, ProbeRequest{TargetURL: "https://api.openai.com/v1", Timeout: 2 * time.Second, ProxyURL: "http://127.0.0.1:1"})
	if result.Status != ItemFailed || result.ErrorCode == "" {
		t.Fatalf("不可达代理结果=%+v", result)
	}
}

func TestWFWaitRuntimeAndRenewal(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitRuntime(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消等待 err=%v", err)
	}
	if err := waitRuntime(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("正常等待 err=%v", err)
	}
	ch := make(chan error, 1)
	ch <- errors.New("renew boom")
	if err := readRenewalError(ch); err == nil {
		t.Fatal("续租错误必须读出")
	}
	if readRenewalError(make(chan error, 1)) != nil {
		t.Fatal("空通道必须返回 nil")
	}
	parent, cancel := boundedReleaseContext(nil)
	defer cancel()
	if parent == nil {
		t.Fatal("nil parent 必须回退 Background")
	}
}
