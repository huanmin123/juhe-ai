package gatewayupstream

import (
	"context"
	"io"
)

// Upstream body helpers, migrated from upstream/body.ts. The non-stream
// reader pipeline family (pipeNonStreamUpstreamResponse /
// pipeNonStreamUpstreamResponseForInspection / ReadFirstNonStreamChunk* /
// RaceReadWithDeadlines / ReadNonStreamChunkWithAbsoluteDeadline) was removed
// in BUG-0247 项 3: the production non-stream pipe lives in the
// gatewayresponse package (nonstream.go, its own aligned implementation),
// leaving this family reachable only from tests. The error-body limited read
// (ReadUpstreamBodyLimited, transport error diagnostics) and the bounded
// captures stay — they are production surface. The express Response write
// side of the removed family became a DownstreamWriter; backpressure
// bookkeeping is owned by the Go HTTP server (behavioral difference recorded
// in the migration report).

// NonStreamPipeResult mirrors NonStreamPipeResult. It survives only as the
// NonStreamUpstreamBodyPipeError.PartialResult taxonomy field (errors.go);
// the production non-stream pipe result is gatewayresponse.NonStreamPipeResult.
type NonStreamPipeResult struct {
	FirstByteMs        *int64
	CapturedBody       []byte
	CapturedBodyText   *string
	DiagnosticBodyText *string
	UsageTailText      *string
	CaptureTruncated   bool
	TransferredBytes   int
}

// LimitedBodyReadResult mirrors LimitedBodyReadResult.
type LimitedBodyReadResult struct {
	Body               []byte
	BodyText           string
	DiagnosticBodyText string
	Truncated          bool
	ReadBytes          int
	FirstByteMs        *int64
}

// UpstreamErrorBodyCaptureBytes mirrors upstream/body.ts.
const UpstreamErrorBodyCaptureBytes = 256 * 1024

// ReadUpstreamBodyLimited mirrors readUpstreamBodyLimited.
func ReadUpstreamBodyLimited(ctx context.Context, upstreamBody io.Reader, input LimitedBodyReadInput) (LimitedBodyReadResult, error) {
	signal := input.Signal
	if signal == nil {
		signal = ctx
	}
	if upstreamBody == nil {
		return LimitedBodyReadResult{}, nil
	}
	maxBytes := int64(UpstreamErrorBodyCaptureBytes)
	if input.MaxBytes != nil {
		maxBytes = MaxInt64(0, *input.MaxBytes)
	}
	capture := NewLimitedBufferCapture(int(maxBytes))
	var readBytes int
	var firstByteMs *int64
	truncated := false
	buffer := make([]byte, 32*1024)
	for {
		n, err := ReadStreamChunkWithAbort(signal, upstreamBody, buffer)
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = CloseReader(upstreamBody)
			if IsUpstreamRequestAbortedError(err) || signal.Err() != nil {
				return LimitedBodyReadResult{}, err
			}
			return LimitedBodyReadResult{}, &UpstreamBodyReadIncompleteError{Cause: err}
		}
		if n == 0 {
			continue
		}
		chunk := buffer[:n]
		if firstByteMs == nil && input.StartedAt != nil {
			ms := NowMs() - *input.StartedAt
			firstByteMs = &ms
			if input.OnFirstByte != nil {
				input.OnFirstByte()
			}
		}
		readBytes += n
		capture.Push(chunk)
		if capture.Truncated {
			truncated = true
			_ = CloseReader(upstreamBody)
			break
		}
	}
	body := capture.Buffer()
	bodyText := string(body)
	diagnostic := bodyText
	if truncated {
		diagnostic = bodyText + "\n[truncated]"
	}
	return LimitedBodyReadResult{
		Body:               body,
		BodyText:           bodyText,
		DiagnosticBodyText: diagnostic,
		Truncated:          truncated,
		ReadBytes:          readBytes,
		FirstByteMs:        firstByteMs,
	}, nil
}

// LimitedBodyReadInput mirrors the readUpstreamBodyLimited options.
type LimitedBodyReadInput struct {
	MaxBytes    *int64
	StartedAt   *int64
	Signal      context.Context
	OnFirstByte func()
}

// ---------------------------------------------------------------------------
// Captures
// ---------------------------------------------------------------------------

// LimitedBufferCapture mirrors LimitedBufferCapture.
type LimitedBufferCapture struct {
	Chunks    [][]byte
	Size      int
	Truncated bool
	Limit     int
}

func NewLimitedBufferCapture(limitBytes int) *LimitedBufferCapture {
	return &LimitedBufferCapture{Limit: limitBytes}
}

func (c *LimitedBufferCapture) Push(buffer []byte) {
	if len(buffer) == 0 || c.Limit < 0 {
		return
	}
	remaining := c.Limit - c.Size
	if remaining <= 0 {
		c.Truncated = true
		return
	}
	if len(buffer) > remaining {
		c.Chunks = append(c.Chunks, append([]byte(nil), buffer[:remaining]...))
		c.Size += remaining
		c.Truncated = true
		return
	}
	c.Chunks = append(c.Chunks, append([]byte(nil), buffer...))
	c.Size += len(buffer)
}

func (c *LimitedBufferCapture) Buffer() []byte {
	out := make([]byte, 0, c.Size)
	for _, chunk := range c.Chunks {
		out = append(out, chunk...)
	}
	return out
}

func (c *LimitedBufferCapture) CompleteBuffer() []byte {
	if c.Truncated || len(c.Chunks) == 0 {
		return nil
	}
	return c.Buffer()
}

func (c *LimitedBufferCapture) ToText() *string {
	if len(c.Chunks) == 0 {
		return nil
	}
	text := string(c.Buffer())
	return &text
}

// RollingBufferCapture mirrors RollingBufferCapture.
type RollingBufferCapture struct {
	Chunks    [][]byte
	HeadIndex int
	Size      int
	Limit     int
}

func NewRollingBufferCapture(limitBytes int) *RollingBufferCapture {
	return &RollingBufferCapture{Limit: limitBytes}
}

func (c *RollingBufferCapture) Push(buffer []byte) {
	if len(buffer) == 0 || c.Limit <= 0 {
		return
	}
	if len(buffer) >= c.Limit {
		c.Chunks = [][]byte{append([]byte(nil), buffer[len(buffer)-c.Limit:]...)}
		c.HeadIndex = 0
		c.Size = c.Limit
		return
	}
	c.Chunks = append(c.Chunks, append([]byte(nil), buffer...))
	c.Size += len(buffer)
	c.TrimOverflow()
}

func (c *RollingBufferCapture) ToText() *string {
	if c.Size == 0 {
		return nil
	}
	text := string(c.ActiveChunks())
	return &text
}

func (c *RollingBufferCapture) TrimOverflow() {
	overflow := c.Size - c.Limit
	for overflow > 0 && c.HeadIndex < len(c.Chunks) {
		first := c.Chunks[c.HeadIndex]
		if len(first) <= overflow {
			c.HeadIndex++
			c.Size -= len(first)
			overflow -= len(first)
		} else {
			c.Chunks[c.HeadIndex] = append([]byte(nil), first[overflow:]...)
			c.Size -= overflow
			overflow = 0
		}
	}
	c.CompactConsumedChunks()
}

func (c *RollingBufferCapture) ActiveChunks() []byte {
	Chunks := c.Chunks
	if c.HeadIndex > 0 {
		Chunks = Chunks[c.HeadIndex:]
	}
	out := make([]byte, 0, c.Size)
	for _, chunk := range Chunks {
		out = append(out, chunk...)
	}
	return out
}

func (c *RollingBufferCapture) CompactConsumedChunks() {
	if c.HeadIndex == 0 {
		return
	}
	if c.HeadIndex >= len(c.Chunks) {
		c.Chunks = nil
		c.HeadIndex = 0
		return
	}
	if c.HeadIndex > 64 && c.HeadIndex*2 > len(c.Chunks) {
		c.Chunks = append([][]byte(nil), c.Chunks[c.HeadIndex:]...)
		c.HeadIndex = 0
	}
}

func CloseReader(reader io.Reader) error {
	if closer, ok := reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
