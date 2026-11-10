// async_writer_test.go —— 运行日志文件路异步有界队列：正常写入按序落盘，
// 下游阻塞时请求侧仍立即返回，队列满丢弃并计数。
package processlog

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAsyncWriterFlushesInOrder(t *testing.T) {
	var target bytes.Buffer
	writer := NewAsyncWriter(&target, 8)
	if _, err := writer.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := target.String(); got != "first\nsecond\n" {
		t.Fatalf("async output = %q, want ordered writes", got)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

type blockingAsyncTarget struct {
	release chan struct{}
	writes  atomic.Int64
}

func (b *blockingAsyncTarget) Write(p []byte) (int, error) {
	<-b.release
	b.writes.Add(1)
	return len(p), nil
}

func TestAsyncWriterDoesNotBlockOnStuckTarget(t *testing.T) {
	target := &blockingAsyncTarget{release: make(chan struct{})}
	writer := NewAsyncWriter(target, 1)
	returned := make(chan struct{})
	go func() {
		for index := 0; index < 20; index++ {
			_, _ = writer.Write([]byte(strings.Repeat("x", 16)))
		}
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("下游阻塞时 Write 仍阻塞请求侧")
	}
	if writer.DroppedCount() == 0 {
		t.Fatal("队列满时必须记录丢弃，不能静默无限排队")
	}
	close(target.release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if writer.DroppedCount() == 0 {
		t.Fatal("丢弃计数必须在关闭后仍可读")
	}
}

func TestAsyncWriterCloseConcurrentWithWrites(t *testing.T) {
	writer := NewAsyncWriter(io.Discard, 8)
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < 100; index++ {
				_, _ = writer.Write([]byte("line\n"))
			}
		}()
	}
	closed := make(chan error, 1)
	go func() { closed <- writer.Close() }()
	wait.Wait()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close 未在超时内完成")
	}
}
