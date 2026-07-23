package gatewaystreamrelay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/gatewayaudit"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
)

func TestRelayCompletesBoundedStreamAndBuildsTerminalHandoff(t *testing.T) {
	startedAt := time.Unix(100, 0)
	nowValues := []time.Time{startedAt.Add(25 * time.Millisecond), startedAt.Add(40 * time.Millisecond)}
	var nowIndex int
	inspector := &inspectorStub{
		inspection: Inspection{
			TerminalRequired: true,
			SemanticOutput:   true,
			Usage: gatewayusage.UsageFacts{
				InputTokens: int64Pointer(3), OutputTokens: int64Pointer(5),
			},
		},
		terminalOnText: "[DONE]",
	}
	sink := &recordingSink{}

	result, err := Relay(context.Background(), &sliceSource{chunks: [][]byte{[]byte("data: one\n\n"), []byte("data: [DONE]\n\n")}}, sink, Options{
		Limits:     testLimits(),
		Inspector:  inspector,
		StartedAt:  startedAt,
		StatusCode: 200,
		Now: func() time.Time {
			value := nowValues[nowIndex]
			nowIndex++
			return value
		},
	})
	if err != nil {
		t.Fatalf("Relay() error = %v", err)
	}
	if got, want := sink.String(), "data: one\n\ndata: [DONE]\n\n"; got != want {
		t.Fatalf("sink = %q, want %q", got, want)
	}
	if got, want := inspector.seen.String(), sink.String(); got != want {
		t.Fatalf("inspector = %q, want %q", got, want)
	}
	if !inspector.finished {
		t.Fatal("inspector was not finished on clean EOF")
	}
	if result.State != StateCompleted || !result.FirstByteSent || result.RetryAllowed {
		t.Fatalf("result state = %#v", result)
	}
	if result.FirstByteAt != nowValues[0] || result.CompletedAt != nowValues[1] {
		t.Fatalf("timestamps = first %s completed %s", result.FirstByteAt, result.CompletedAt)
	}
	if result.Handoff.Usage.Outcome != gatewayusage.OutcomeSucceeded || result.Handoff.Usage.FirstToken == nil || *result.Handoff.Usage.FirstToken != 25*time.Millisecond {
		t.Fatalf("usage handoff = %#v", result.Handoff.Usage)
	}
	if !result.Handoff.Audit.Success || result.Handoff.Audit.TerminalReceived != true {
		t.Fatalf("audit handoff = %#v", result.Handoff.Audit)
	}
}

func TestRelayClientCancelBeforeFirstByteDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Relay(ctx, blockingSource{}, &recordingSink{}, Options{Limits: testLimits(), StartedAt: time.Now()})
	if !errors.Is(err, ErrClientCanceled) {
		t.Fatalf("Relay() error = %v, want client canceled", err)
	}
	if result.State != StateFailedBeforeFirstByte || result.RetryAllowed || result.FirstByteSent {
		t.Fatalf("result = %#v", result)
	}
	if !result.Handoff.Audit.ClientAborted || result.Handoff.Audit.RequestedOutcome != gatewayaudit.OutcomeClientAborted {
		t.Fatalf("audit = %#v", result.Handoff.Audit)
	}
	if result.Handoff.Usage.FailureAttribution != gatewayusage.FailureAttributionClientLifecycle {
		t.Fatalf("usage = %#v", result.Handoff.Usage)
	}
}

func TestRelayIdleDeadlineBeforeFirstByteAllowsRetry(t *testing.T) {
	limits := testLimits()
	limits.IdleTimeout = 10 * time.Millisecond
	limits.TotalTimeout = time.Second
	result, err := Relay(context.Background(), blockingSource{}, &recordingSink{}, Options{Limits: limits, StartedAt: time.Now()})
	if !errors.Is(err, ErrIdleDeadline) {
		t.Fatalf("Relay() error = %v, want idle deadline", err)
	}
	if result.State != StateFailedBeforeFirstByte || !result.RetryAllowed {
		t.Fatalf("result = %#v", result)
	}
}

func TestRelayTotalDeadlineWinsAcrossProgress(t *testing.T) {
	limits := testLimits()
	limits.IdleTimeout = 100 * time.Millisecond
	limits.TotalTimeout = 20 * time.Millisecond
	source := SourceFunc(func(ctx context.Context, p []byte) (int, error) {
		select {
		case <-ctx.Done():
			return 0, context.Cause(ctx)
		case <-time.After(7 * time.Millisecond):
			p[0] = 'x'
			return 1, nil
		}
	})
	result, err := Relay(context.Background(), source, &recordingSink{}, Options{Limits: limits, StartedAt: time.Now()})
	if !errors.Is(err, ErrTotalDeadline) {
		t.Fatalf("Relay() error = %v, want total deadline", err)
	}
	if !result.FirstByteSent || result.State != StateFailedAfterFirstByte || result.RetryAllowed {
		t.Fatalf("result = %#v", result)
	}
}

func TestRelayCapFailureAfterFirstByteCannotRetry(t *testing.T) {
	limits := testLimits()
	limits.MaxBytes = 5
	limits.BufferBytes = 4
	sink := &recordingSink{}
	result, err := Relay(context.Background(), &sliceSource{chunks: [][]byte{[]byte("abcd"), []byte("ef")}}, sink, Options{Limits: limits, StartedAt: time.Now()})
	if !errors.Is(err, ErrStreamTooLarge) {
		t.Fatalf("Relay() error = %v, want stream too large", err)
	}
	if got := sink.String(); got != "abcd" {
		t.Fatalf("sink = %q, want first bounded chunk", got)
	}
	if result.BytesWritten != 4 || result.State != StateFailedAfterFirstByte || result.RetryAllowed {
		t.Fatalf("result = %#v", result)
	}
	if result.Handoff.Usage.FailureAttribution != gatewayusage.FailureAttributionGatewayCapacity {
		t.Fatalf("usage = %#v", result.Handoff.Usage)
	}
}

func TestRelayPartialDestinationWriteCommitsFirstByteAndDisablesRetry(t *testing.T) {
	sinkErr := errors.New("client disconnected")
	sink := SinkFunc(func(_ context.Context, p []byte) (int, error) { return 2, sinkErr })
	inspector := &inspectorStub{inspection: Inspection{SemanticOutput: true}}
	result, err := Relay(context.Background(), &sliceSource{chunks: [][]byte{[]byte("hello")}}, sink, Options{Limits: testLimits(), StartedAt: time.Now(), Inspector: inspector})
	if !errors.Is(err, sinkErr) || !errors.Is(err, ErrDestinationWrite) {
		t.Fatalf("Relay() error = %v", err)
	}
	if result.BytesWritten != 2 || result.State != StateFailedAfterFirstByte || result.RetryAllowed {
		t.Fatalf("result = %#v", result)
	}
	if !result.Handoff.Audit.ClientAborted {
		t.Fatalf("audit = %#v", result.Handoff.Audit)
	}
	if inspector.commitCalls != 1 || !inspector.transportCommitted || !inspector.semanticCommitted || inspector.downstreamBytes != 2 {
		t.Fatalf("commit observer = %#v", inspector)
	}
}

func TestRelayMissingRequiredTerminalFailsAfterPayloadWithoutRetry(t *testing.T) {
	inspector := &inspectorStub{inspection: Inspection{TerminalRequired: true}}
	result, err := Relay(context.Background(), &sliceSource{chunks: [][]byte{[]byte("data: partial\n\n")}}, &recordingSink{}, Options{
		Limits: testLimits(), Inspector: inspector, StartedAt: time.Now(),
	})
	if !errors.Is(err, ErrMissingTerminal) {
		t.Fatalf("Relay() error = %v, want missing terminal", err)
	}
	if result.State != StateFailedAfterFirstByte || result.RetryAllowed {
		t.Fatalf("result = %#v", result)
	}
	resolved := gatewayaudit.ResolveTerminal(result.Handoff.Audit)
	if resolved.Outcome != gatewayaudit.OutcomeStreamFailed || resolved.ErrorCode != gatewayaudit.ErrorCodeMissingStreamTerminal {
		t.Fatalf("resolved audit = %#v", resolved)
	}
}

func TestRelayInspectorRejectsChunkBeforeDownstreamCommit(t *testing.T) {
	inspectErr := errors.New("malformed terminal frame")
	inspector := &inspectorStub{observeErr: inspectErr}
	sink := &recordingSink{}
	result, err := Relay(context.Background(), &sliceSource{chunks: [][]byte{[]byte("bad")}}, sink, Options{
		Limits: testLimits(), Inspector: inspector, StartedAt: time.Now(),
	})
	if !errors.Is(err, inspectErr) || !errors.Is(err, ErrInspector) {
		t.Fatalf("Relay() error = %v", err)
	}
	if sink.Len() != 0 || result.State != StateFailedBeforeFirstByte || !result.RetryAllowed {
		t.Fatalf("sink/result = %q %#v", sink.String(), result)
	}
}

func TestRelayCancellationInterruptsBackpressuredSink(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sinkStarted := make(chan struct{})
	sink := SinkFunc(func(ctx context.Context, _ []byte) (int, error) {
		close(sinkStarted)
		<-ctx.Done()
		return 0, context.Cause(ctx)
	})
	done := make(chan struct{})
	var result Result
	var relayErr error
	go func() {
		defer close(done)
		result, relayErr = Relay(ctx, &sliceSource{chunks: [][]byte{[]byte("payload")}}, sink, Options{Limits: testLimits(), StartedAt: time.Now()})
	}()
	<-sinkStarted
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Relay did not stop after client cancellation")
	}
	if !errors.Is(relayErr, ErrClientCanceled) || result.FirstByteSent || result.RetryAllowed {
		t.Fatalf("result/error = %#v %v", result, relayErr)
	}
}

func TestRelayDoesNotReadNextChunkWhileSinkIsBackpressured(t *testing.T) {
	firstRead := make(chan struct{})
	secondRead := make(chan struct{})
	readCount := 0
	source := SourceFunc(func(_ context.Context, p []byte) (int, error) {
		readCount++
		switch readCount {
		case 1:
			close(firstRead)
			return copy(p, "one"), nil
		case 2:
			close(secondRead)
			return copy(p, "two"), nil
		default:
			return 0, io.EOF
		}
	})
	releaseSink := make(chan struct{})
	firstWrite := make(chan struct{})
	sink := SinkFunc(func(ctx context.Context, p []byte) (int, error) {
		if string(p) == "one" {
			close(firstWrite)
			select {
			case <-releaseSink:
			case <-ctx.Done():
				return 0, context.Cause(ctx)
			}
		}
		return len(p), nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := Relay(context.Background(), source, sink, Options{Limits: testLimits(), StartedAt: time.Now()})
		done <- err
	}()
	<-firstRead
	<-firstWrite
	select {
	case <-secondRead:
		t.Fatal("source advanced while the first downstream write was blocked")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseSink)
	if err := <-done; err != nil {
		t.Fatalf("Relay() error = %v", err)
	}
}

func TestRelayProcessesBytesReturnedWithEOF(t *testing.T) {
	source := SourceFunc(func(_ context.Context, p []byte) (int, error) {
		return copy(p, "last"), io.EOF
	})
	sink := &recordingSink{}
	result, err := Relay(context.Background(), source, sink, Options{Limits: testLimits(), StartedAt: time.Now()})
	if err != nil {
		t.Fatalf("Relay() error = %v", err)
	}
	if sink.String() != "last" || result.BytesWritten != 4 || result.State != StateCompleted {
		t.Fatalf("sink/result = %q %#v", sink.String(), result)
	}
}

func TestRelayRejectsLimitsAboveHardCap(t *testing.T) {
	limits := testLimits()
	limits.MaxBytes = MaxStreamBytes + 1
	_, err := Relay(context.Background(), &sliceSource{}, &recordingSink{}, Options{Limits: limits, StartedAt: time.Now()})
	if !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("Relay() error = %v, want invalid limits", err)
	}
}

func testLimits() Limits {
	return Limits{MaxBytes: 1024, BufferBytes: 32, IdleTimeout: time.Second, TotalTimeout: time.Second}
}

type sliceSource struct {
	chunks [][]byte
	index  int
}

func (s *sliceSource) Read(_ context.Context, p []byte) (int, error) {
	if s.index >= len(s.chunks) {
		return 0, io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return copy(p, chunk), nil
}

type blockingSource struct{}

func (blockingSource) Read(ctx context.Context, _ []byte) (int, error) {
	<-ctx.Done()
	return 0, context.Cause(ctx)
}

type recordingSink struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *recordingSink) Write(_ context.Context, p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *recordingSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *recordingSink) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Len()
}

type inspectorStub struct {
	seen               bytes.Buffer
	observeErr         error
	finishErr          error
	inspection         Inspection
	terminalOnText     string
	failedOnText       string
	finished           bool
	commitCalls        int
	transportCommitted bool
	semanticCommitted  bool
	downstreamBytes    int64
}

func (s *inspectorStub) Observe(p []byte) error {
	if s.observeErr != nil {
		return s.observeErr
	}
	_, _ = s.seen.Write(p)
	if s.terminalOnText != "" && bytes.Contains(s.seen.Bytes(), []byte(s.terminalOnText)) {
		s.inspection.TerminalReceived = true
	}
	if s.failedOnText != "" && bytes.Contains(s.seen.Bytes(), []byte(s.failedOnText)) {
		s.inspection.Failed = true
	}
	return nil
}

func (s *inspectorStub) Finish() error {
	s.finished = true
	return s.finishErr
}

func (s *inspectorStub) Snapshot() Inspection { return s.inspection }

func (s *inspectorStub) ObserveCommit(transportCommitted, semanticCommitted bool, downstreamBytes int64) {
	s.commitCalls++
	s.transportCommitted = transportCommitted
	s.semanticCommitted = semanticCommitted
	s.downstreamBytes = downstreamBytes
}

func int64Pointer(value int64) *int64 { return &value }
