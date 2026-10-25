package proxylatency

import (
	"context"
	"sync"
	"testing"
	"time"
)

// 本文件收尾 Store 与 Runner 的最后一批校验/跳过分支。

func TestWFEnsureRuntimeSchemaConcurrent(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	store := &Store{db: db, mode: StorePostgres, postgresSchemaReady: false}
	// 并发检查：为每个 goroutine 预登记一份完整夹具（按类型互不串扰）。
	for index := 0; index < 4; index++ {
		wfScriptSchema(t, rec)
	}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for index := 0; index < 4; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			errs[index] = store.CheckSchema(context.Background())
		}(index)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("并发 schema 检查 %d 失败: %v", index, err)
		}
	}
	if !store.postgresSchemaReady {
		t.Fatal("检查通过后必须置 ready")
	}
	// ready 后续租操作直接命中 UPSERT（不再触发 catalog 检查）：
	// 无脚本 → ErrNoRows → 未获取但不报错，证明跳过了 catalog 检查。
	if _, ok, err := store.AcquireOwnerLease(context.Background(), "o", time.Hour); err != nil || ok {
		t.Fatalf("ready 后租约操作 ok=%v err=%v", ok, err)
	}
}

func TestWFRunnerRecordErrorNilAndNowDefault(t *testing.T) {
	runner := NewRunner(RuntimeConfig{}, &Store{}, &wfFakeReader{}, nil)
	runner.recordError(nil)
	if runner.Status().LastError != "" {
		t.Fatal("nil 错误不得记账")
	}
	if runner.now().IsZero() {
		t.Fatal("默认时钟必须可用")
	}
}
