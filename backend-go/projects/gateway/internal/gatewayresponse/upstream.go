package gatewayresponse

import (
	"context"
	"io"
	"sync"
)

// UpstreamBody 对齐 AsyncIterable<Uint8Array>：上游响应正文的拉式迭代。
type UpstreamBody interface {
	// Next 返回下一个分片的 future；接收方必须恰好接收一次。管道在致命超时
	// 后调用 Close 并放弃未决 future（与 Node 的 pending read 一致）。
	Next() <-chan ChunkResult
	// Close 对齐 closeAsyncIterator：尽力提前终止并释放读取 goroutine。
	Close()
}

// ChunkResult 是一次分片读取的结果；Err == io.EOF 表示干净 EOF。
type ChunkResult struct {
	Data []byte
	Err  error
}

// SliceUpstreamBody 是测试/内存实现。
type SliceUpstreamBody struct {
	chunks [][]byte
	index  int
	closed bool
	mu     sync.Mutex
}

// NewSliceUpstreamBody 构造分片序列上游。
func NewSliceUpstreamBody(chunks ...[]byte) *SliceUpstreamBody {
	return &SliceUpstreamBody{chunks: chunks}
}

// Next 实现 UpstreamBody。
func (b *SliceUpstreamBody) Next() <-chan ChunkResult {
	ch := make(chan ChunkResult, 1)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		ch <- ChunkResult{Err: ErrBodyClosed}
		close(ch)
		return ch
	}
	if b.index >= len(b.chunks) {
		ch <- ChunkResult{Err: io.EOF}
		close(ch)
		return ch
	}
	chunk := b.chunks[b.index]
	b.index++
	ch <- ChunkResult{Data: chunk}
	close(ch)
	return ch
}

// Close 实现 UpstreamBody。
func (b *SliceUpstreamBody) Close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
}

// ErrBodyClosed 是 Close 后再读的错误。
var ErrBodyClosed = io.ErrClosedPipe

// ReaderUpstreamBody 以后台 goroutine 读取 io.Reader（真实上游响应体）。
type ReaderUpstreamBody struct {
	reader io.Reader
	ctx    context.Context
	cancel context.CancelFunc
	ch     chan ChunkResult
	once   sync.Once
}

// NewReaderUpstreamBody 构造读取器上游。
func NewReaderUpstreamBody(ctx context.Context, reader io.Reader) *ReaderUpstreamBody {
	readCtx, cancel := context.WithCancel(ctx)
	body := &ReaderUpstreamBody{
		reader: reader,
		ctx:    readCtx,
		cancel: cancel,
		ch:     make(chan ChunkResult),
	}
	go body.pump()
	return body
}

func (b *ReaderUpstreamBody) pump() {
	defer close(b.ch)
	buffer := make([]byte, 32*1024)
	for {
		count, err := b.reader.Read(buffer)
		if count > 0 {
			chunk := make([]byte, count)
			copy(chunk, buffer[:count])
			select {
			case b.ch <- ChunkResult{Data: chunk}:
			case <-b.ctx.Done():
				return
			}
		}
		if err != nil {
			if err == io.EOF {
				select {
				case b.ch <- ChunkResult{Err: io.EOF}:
				case <-b.ctx.Done():
				}
				return
			}
			select {
			case b.ch <- ChunkResult{Err: &StartedBodyTransportError{Err: err}}:
			case <-b.ctx.Done():
			}
			return
		}
	}
}

// Next 实现 UpstreamBody。
func (b *ReaderUpstreamBody) Next() <-chan ChunkResult { return b.ch }

// Close 实现 UpstreamBody。
func (b *ReaderUpstreamBody) Close() {
	b.once.Do(func() {
		b.cancel()
		if closer, ok := b.reader.(io.Closer); ok {
			_ = closer.Close()
		}
	})
}
