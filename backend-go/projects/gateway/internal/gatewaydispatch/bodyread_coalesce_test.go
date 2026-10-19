package gatewaydispatch

// ReadUpstreamBodyLimited 的 (n>0, io.EOF) 合并返回回归（T5/BUG-0274 残余
// 装配期发现）：io.Reader 允许单次 Read 同时带回末批数据与 io.EOF，有限读
// 回必须先把末批数据入捕获再处理 EOF，否则小响应整包/尾段被静默丢弃（真实
// http body 对一次性小响应即此形态）。

import (
	"context"
	"io"
	"testing"
)

// eofCoalescedReader 在缓冲足够时把全部剩余字节与 io.EOF 合并在单次 Read
// 返回（读次数可断言，锁定"单次合并返回"形态）。
type eofCoalescedReader struct {
	payload []byte
	reads   int
}

func (r *eofCoalescedReader) Read(p []byte) (int, error) {
	r.reads++
	if len(r.payload) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.payload)
	r.payload = r.payload[n:]
	if len(r.payload) == 0 {
		return n, io.EOF
	}
	return n, nil
}

// chunkedTailReader 逐元素返回 chunks；最后一个元素与 io.EOF 合并返回
// （分块 + 尾段合并形态）。
type chunkedTailReader struct {
	chunks [][]byte
	reads  int
}

func (r *chunkedTailReader) Read(p []byte) (int, error) {
	r.reads++
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := r.chunks[0]
	if len(chunk) > len(p) {
		// 测试载荷远小于读缓冲（32KB），此分支不应到达。
		n := copy(p, chunk[:len(p)])
		r.chunks[0] = chunk[len(p):]
		return n, nil
	}
	copy(p, chunk)
	r.chunks = r.chunks[1:]
	if len(r.chunks) == 0 {
		return len(chunk), io.EOF
	}
	return len(chunk), nil
}

func TestReadUpstreamBodyLimitedCoalescedEOFCarriesData(t *testing.T) {
	payload := `{"choices":[{"message":{"content":"压缩后的摘要"}}]}`
	reader := &eofCoalescedReader{payload: []byte(payload)}
	startedAt := int64(1000)
	firstByteCalls := 0
	result, err := ReadUpstreamBodyLimited(context.Background(), reader, LimitedBodyReadInput{
		StartedAt:   &startedAt,
		OnFirstByte: func() { firstByteCalls++ },
	})
	if err != nil {
		t.Fatalf("ReadUpstreamBodyLimited: %v", err)
	}
	if reader.reads != 1 {
		t.Fatalf("reads = %d，want 1（必须是单次合并返回形态）", reader.reads)
	}
	if result.BodyText != payload {
		t.Fatalf("BodyText = %q，want %q（末批数据不得随 EOF 丢弃）", result.BodyText, payload)
	}
	if result.Truncated {
		t.Fatal("完整读回不得截断")
	}
	if result.ReadBytes != len(payload) {
		t.Fatalf("ReadBytes = %d，want %d", result.ReadBytes, len(payload))
	}
	if result.DiagnosticBodyText != payload {
		t.Fatalf("DiagnosticBodyText = %q，want %q", result.DiagnosticBodyText, payload)
	}
	if result.FirstByteMs == nil || *result.FirstByteMs < 0 {
		t.Fatalf("FirstByteMs = %v，want 非 nil 正样本", result.FirstByteMs)
	}
	if firstByteCalls != 1 {
		t.Fatalf("OnFirstByte 调用次数 = %d，want 1", firstByteCalls)
	}
}

func TestReadUpstreamBodyLimitedChunkedWithCoalescedTail(t *testing.T) {
	head := `{"choices":[{"message":{"content":"前半`
	tail := `末段"}]}`
	reader := &chunkedTailReader{chunks: [][]byte{[]byte(head), []byte(tail)}}
	result, err := ReadUpstreamBodyLimited(context.Background(), reader, LimitedBodyReadInput{})
	if err != nil {
		t.Fatalf("ReadUpstreamBodyLimited: %v", err)
	}
	if reader.reads != 2 {
		t.Fatalf("reads = %d，want 2（分块 + 尾段合并）", reader.reads)
	}
	want := head + tail
	if result.BodyText != want {
		t.Fatalf("BodyText = %q，want %q（合并尾段不得丢弃）", result.BodyText, want)
	}
	if result.Truncated {
		t.Fatal("完整读回不得截断")
	}
	if result.ReadBytes != len(want) {
		t.Fatalf("ReadBytes = %d，want %d", result.ReadBytes, len(want))
	}
	// 未携带 StartedAt 时不计首字样本（既有口径保持）。
	if result.FirstByteMs != nil {
		t.Fatalf("FirstByteMs = %v，want nil（无 StartedAt）", result.FirstByteMs)
	}
}

func TestReadUpstreamBodyLimitedCoalescedEOFStillTruncates(t *testing.T) {
	payload := "0123456789"
	reader := &eofCoalescedReader{payload: []byte(payload)}
	maxBytes := int64(4)
	result, err := ReadUpstreamBodyLimited(context.Background(), reader, LimitedBodyReadInput{MaxBytes: &maxBytes})
	if err != nil {
		t.Fatalf("ReadUpstreamBodyLimited: %v", err)
	}
	if !result.Truncated {
		t.Fatal("超限合并返回必须截断")
	}
	if result.BodyText != "0123" {
		t.Fatalf("BodyText = %q，want %q", result.BodyText, "0123")
	}
	if result.DiagnosticBodyText != "0123\n[truncated]" {
		t.Fatalf("DiagnosticBodyText = %q", result.DiagnosticBodyText)
	}
}
