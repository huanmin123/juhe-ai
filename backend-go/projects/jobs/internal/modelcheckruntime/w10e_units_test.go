package modelcheckruntime

import (
	"testing"
)

// 覆盖 run 的 nil ctx 回退、NewID 缺省回退与 valueOrZero(nil)。

func TestW10ENilCtxAndNewIDFallback(t *testing.T) {
	durable, dataset, _, now := openRuntimeStores(t)
	defer durable.Close()
	defer dataset.Close()

	// NewID 缺省回退（128-130）。
	svc := newRuntimeService(durable, dataset, "http://127.0.0.1:9", now)
	svc.NewID = nil

	// nil ctx → context.Background()（107）。
	// endpoint 不可达，运行应失败但绝不应 panic。
	_, _ = svc.Run(nil, runtimeRequest(now))

	// RunWithProgress 的进度回调投递（276-277）与 nil 进度短路。
	svc2 := newRuntimeService(durable, dataset, "http://127.0.0.1:9", now)
	events := 0
	_, _ = svc2.RunWithProgress(nil, runtimeRequest(now), func(ProgressEvent) { events++ })
	_, _ = svc2.RunWithProgress(nil, runtimeRequest(now), nil)
}

func TestW10EValueOrZero(t *testing.T) {
	if got := valueOrZero(nil); got != 0 {
		t.Fatalf("valueOrZero(nil) = %v", got)
	}
	v := int64(42)
	if got := valueOrZero(&v); got != 42 {
		t.Fatalf("valueOrZero(&v) = %v", got)
	}
}
