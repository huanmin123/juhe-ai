package gatewayresponse

import (
	"bytes"
)

// 审计/诊断正文捕获，对齐 upstream/body.ts 的 LimitedBufferCapture：有界追加，
// 截断后 completeBuffer 返回空，诊断文本带 '\n[truncated]' 后缀。

// LimitedCapture 对齐 LimitedBufferCapture。limitBytes < 0 表示完全不捕获。
type LimitedCapture struct {
	limitBytes int
	chunks     [][]byte
	size       int
	truncated  bool
}

// NewLimitedCapture 构造捕获器。
func NewLimitedCapture(limitBytes int) *LimitedCapture {
	return &LimitedCapture{limitBytes: limitBytes}
}

// Push 对齐 push。
func (c *LimitedCapture) Push(chunk []byte) {
	if c == nil || len(chunk) == 0 || c.limitBytes < 0 {
		return
	}
	remaining := c.limitBytes - c.size
	if remaining <= 0 {
		c.truncated = true
		return
	}
	if len(chunk) > remaining {
		c.chunks = append(c.chunks, chunk[:remaining])
		c.size += remaining
		c.truncated = true
		return
	}
	c.chunks = append(c.chunks, chunk)
	c.size += len(chunk)
}

// IsTruncated 对齐 isTruncated。
func (c *LimitedCapture) IsTruncated() bool { return c.truncated }

// Buffer 对齐 buffer()。
func (c *LimitedCapture) Buffer() []byte {
	out := make([]byte, 0, c.size)
	for _, chunk := range c.chunks {
		out = append(out, chunk...)
	}
	return out
}

// CompleteBuffer 对齐 completeBuffer：截断或为空时返回 nil。
func (c *LimitedCapture) CompleteBuffer() []byte {
	if c.truncated || len(c.chunks) == 0 {
		return nil
	}
	return c.Buffer()
}

// Clear 对齐 clear()。
func (c *LimitedCapture) Clear() {
	c.chunks = nil
	c.size = 0
	c.truncated = false
}

// ToText 对齐 toText()。
func (c *LimitedCapture) ToText() (string, bool) {
	if c == nil || len(c.chunks) == 0 {
		return "", false
	}
	return string(c.Buffer()), true
}

// ToDiagnosticText 对齐 toDiagnosticText()。
func (c *LimitedCapture) ToDiagnosticText() (string, bool) {
	text, ok := c.ToText()
	if !ok {
		return "", false
	}
	if c.truncated {
		return text + "\n[truncated]", true
	}
	return text, true
}

// RollingCapture 对齐 RollingBufferCapture：只保留末尾 limitBytes。Node 用它
// 捕获非流式 usage tail；这里按相同语义实现（末尾窗口）。
type RollingCapture struct {
	limitBytes int
	buf        bytes.Buffer
	filled     bool
}

// NewRollingCapture 构造滚动捕获器。
func NewRollingCapture(limitBytes int) *RollingCapture {
	return &RollingCapture{limitBytes: limitBytes}
}

// Push 追加一段正文，只保留末尾窗口。
func (c *RollingCapture) Push(chunk []byte) {
	if c.limitBytes <= 0 || len(chunk) == 0 {
		return
	}
	if c.buf.Len()+len(chunk) > c.limitBytes {
		c.filled = true
	}
	c.buf.Write(chunk)
	if c.buf.Len() > c.limitBytes {
		drop := c.buf.Len() - c.limitBytes
		kept := append([]byte(nil), c.buf.Bytes()[drop:]...)
		c.buf.Reset()
		c.buf.Write(kept)
	}
}

// Buffer 返回当前窗口内容。
func (c *RollingCapture) Buffer() []byte { return append([]byte(nil), c.buf.Bytes()...) }

// Text 返回窗口文本（空时 ok=false，对齐 toText）。
func (c *RollingCapture) Text() (string, bool) {
	if c.buf.Len() == 0 {
		return "", false
	}
	return c.buf.String(), true
}

// Filled 报告窗口是否曾被填满（usage tail 可能截断开头）。
func (c *RollingCapture) Filled() bool { return c.filled }
