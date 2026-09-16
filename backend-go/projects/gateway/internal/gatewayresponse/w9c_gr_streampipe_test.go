package gatewayresponse

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// ---------------------------------------------------------------------------
// 流管道首字截止决策面（streamread.go）：直接驱动 pendingRead / 竞速 / 决策。
// ---------------------------------------------------------------------------

type w9cClock struct {
	mu  sync.Mutex
	now int64
}

func (c *w9cClock) NowMs() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *w9cClock) Set(now int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func w9cNewPipe(t *testing.T, clock *w9cClock) *streamPipe {
	t.Helper()
	pipe := newStreamPipe(PipeUpstreamStreamInput{
		UpstreamBody: NewSliceUpstreamBody(),
		Downstream: StreamDownstream{
			Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder()),
		},
		TimeoutProfile: TimeoutProfile{
			FirstResponseTimeoutMs:          60_000,
			IdleTimeoutMs:                   30_000,
			UncommittedAttemptMaxLifetimeMs: 300_000,
		},
		StartedAtMs: 1000,
		Options:     StreamPipeOptions{NowMs: clock.NowMs},
	})
	pipe.startedAt = 1000
	return pipe
}

func w9cSettledPending(chunk ChunkResult, nowMs func() int64) *pendingRead {
	source := make(chan ChunkResult, 1)
	source <- chunk
	return newPendingRead(source, nowMs)
}

func w9cUnsettledPending(nowMs func() int64) *pendingRead {
	return newPendingRead(make(chan ChunkResult), nowMs)
}

func TestW9CPendingReadLifecycle(t *testing.T) {
	clock := &w9cClock{now: 5000}
	// Settled with a value.
	pending := w9cSettledPending(ChunkResult{Data: []byte("x")}, clock.NowMs)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !pending.isSettled() {
		time.Sleep(2 * time.Millisecond)
	}
	if !pending.isSettled() {
		t.Fatal("pre-loaded pending must settle")
	}
	if at, ok := pending.settledAtMs(); !ok || at != 5000 {
		t.Fatalf("settledAt = %d %v", at, ok)
	}
	chunk, ok := pending.receive()
	if !ok || string(chunk.Data) != "x" {
		t.Fatalf("receive = %+v %v", chunk, ok)
	}
	// Unsettled pending reports no settle time.
	unsettled := w9cUnsettledPending(clock.NowMs)
	if _, ok := unsettled.settledAtMs(); ok {
		t.Fatal("unsettled pending must not report settle time")
	}
	// Closed source settles with the interrupted read.
	closed := make(chan ChunkResult)
	close(closed)
	aborted := newPendingRead(closed, clock.NowMs)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !aborted.isSettled() {
		time.Sleep(2 * time.Millisecond)
	}
	chunk, ok = aborted.receive()
	if !ok || !errorsIs(chunk.Err, io.ErrUnexpectedEOF) {
		t.Fatalf("closed source chunk = %+v %v", chunk, ok)
	}
}

func errorsIs(err error, target error) bool {
	if err == nil {
		return false
	}
	return err == target || err.Error() == target.Error()
}

func TestW9CDecideFirstByteDeadlineWithoutPrecommit(t *testing.T) {
	clock := &w9cClock{now: 5000}
	pipe := w9cNewPipe(t, clock)

	// No handler + unsettled pending: decision carries the abort action.
	decision := pipe.decideFirstByteDeadlineAfterPendingRead(w9cUnsettledPending(clock.NowMs), FirstByteDeadlineInput{})
	if decision.read || !decision.hasAction || decision.action != FirstByteDeadlineAbort {
		t.Fatalf("unsettled decision = %+v", decision)
	}
	// No handler + settled pending: the chunk is read.
	settledOK := w9cSettledPending(ChunkResult{Data: []byte("ok")}, clock.NowMs)
	waitSettled(t, settledOK)
	decision = pipe.decideFirstByteDeadlineAfterPendingRead(settledOK, FirstByteDeadlineInput{})
	if !decision.read || string(decision.chunk.Data) != "ok" {
		t.Fatalf("settled decision = %+v", decision)
	}
	// Handler error + unsettled pending surfaces the error only.
	pipe.options.OnFirstByteDeadline = func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		return FirstByteDeadlineContinue, context.DeadlineExceeded
	}
	decision = pipe.decideFirstByteDeadlineAfterPendingRead(w9cUnsettledPending(clock.NowMs), FirstByteDeadlineInput{})
	if decision.read || decision.decisionError == nil {
		t.Fatalf("handler error decision = %+v", decision)
	}
	// Handler error + settled pending yields read + decisionError (the
	// resolved outcome drops the action once the read wins).
	handlerSettled := w9cSettledPending(ChunkResult{Data: []byte("z")}, clock.NowMs)
	waitSettled(t, handlerSettled)
	decision = pipe.decideFirstByteDeadlineAfterPendingRead(handlerSettled, FirstByteDeadlineInput{})
	if !decision.read || decision.decisionError == nil {
		t.Fatalf("settled handler error decision = %+v", decision)
	}
}

func TestW9CDecideFirstByteDeadlineWithPrecommit(t *testing.T) {
	clock := &w9cClock{now: 5000}
	pipe := w9cNewPipe(t, clock)
	superseded := 0
	pipe.options.OnFirstByteDeadlineSuperseded = func() { superseded++ }

	deadlineAt := int64(6000) // future wall clock
	pipe.options.ResponsePrecommitDeadlineAtMs = &deadlineAt

	// Pending settles before the wall clock: read wins with handler error.
	pipe.options.OnFirstByteDeadline = func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		return FirstByteDeadlineContinue, io.EOF
	}
	wallSettled := w9cSettledPending(ChunkResult{Data: []byte("w")}, clock.NowMs)
	waitSettled(t, wallSettled)
	decision := pipe.decideFirstByteDeadlineAfterPendingRead(wallSettled, FirstByteDeadlineInput{})
	if !decision.read || string(decision.chunk.Data) != "w" || decision.decisionError == nil {
		t.Fatalf("pending-beats-wall decision = %+v", decision)
	}

	// Wall clock already fired and pending settled in time: read + the
	// precommit error rides along as decisionError.
	clock.Set(6000)
	firedSettled := w9cSettledPending(ChunkResult{Data: []byte("v")}, clock.NowMs)
	waitSettled(t, firedSettled)
	decision = pipe.decideFirstByteDeadlineAfterPendingRead(firedSettled, FirstByteDeadlineInput{})
	if !decision.read || decision.precommitDeadline || !IsResponsePrecommitDeadlineError(decision.decisionError) {
		t.Fatalf("fired wall with settled read = %+v", decision)
	}

	// Wall clock fired, pending settled late: superseded + precommit deadline.
	clock.Set(7000)
	lateClock := &w9cClock{now: 7500}
	late := newPendingRead(preloadedChan(ChunkResult{Data: []byte("late")}), lateClock.NowMs)
	waitSettled(t, late)
	decision = pipe.decideFirstByteDeadlineAfterPendingRead(late, FirstByteDeadlineInput{})
	if decision.read || !decision.precommitDeadline || superseded == 0 {
		t.Fatalf("late settle decision = %+v superseded=%d", decision, superseded)
	}

	// Wall clock fired, pending unsettled: superseded + precommit deadline.
	// The handler error no longer short-circuits once the wall is the only
	// outcome (nil handler = immediate abort decision).
	pipe.options.OnFirstByteDeadline = nil
	before := superseded
	decision = pipe.decideFirstByteDeadlineAfterPendingRead(w9cUnsettledPending(clock.NowMs), FirstByteDeadlineInput{})
	if decision.read || !decision.precommitDeadline || superseded == before {
		t.Fatalf("unsettled precommit decision = %+v superseded=%d", decision, superseded)
	}
}

func preloadedChan(chunk ChunkResult) <-chan ChunkResult {
	source := make(chan ChunkResult, 1)
	source <- chunk
	return source
}

func waitSettled(t *testing.T, pending *pendingRead) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !pending.isSettled() {
		time.Sleep(2 * time.Millisecond)
	}
	if !pending.isSettled() {
		t.Fatal("pending never settled")
	}
}

func TestW9CRaceStreamReadWithDeadlines(t *testing.T) {
	clock := &w9cClock{now: 1000}
	// Plain read.
	outcome := raceStreamReadWithDeadlines(w9cSettledPending(ChunkResult{Data: []byte("r")}, clock.NowMs), nil, nil, nil, nil)
	if outcome.kind != raceRead || string(outcome.chunk.Data) != "r" {
		t.Fatalf("read outcome = %+v", outcome)
	}
	// Closed pending bridge maps to interrupted read.
	closed := make(chan ChunkResult)
	close(closed)
	outcome = raceStreamReadWithDeadlines(newPendingRead(closed, clock.NowMs), nil, nil, nil, nil)
	if outcome.kind != raceRead || !errorsIs(outcome.chunk.Err, io.ErrUnexpectedEOF) {
		t.Fatalf("closed outcome = %+v", outcome)
	}
	// Plan timeout fires.
	outcome = raceStreamReadWithDeadlines(w9cUnsettledPending(clock.NowMs), nil, nil, int64PtrOf(5), nil)
	if outcome.kind != racePlanTimeout {
		t.Fatalf("plan timeout outcome = %+v", outcome)
	}
	// Soft timeout fires.
	outcome = raceStreamReadWithDeadlines(w9cUnsettledPending(clock.NowMs), nil, int64PtrOf(5), nil, nil)
	if outcome.kind != raceSoftTimeout {
		t.Fatalf("soft timeout outcome = %+v", outcome)
	}
	// Precommit coinciding with a soft timeout keeps the precommit attribution.
	outcome = raceStreamReadWithDeadlines(w9cUnsettledPending(clock.NowMs), nil, int64PtrOf(50), nil, int64PtrOf(5))
	if outcome.kind != raceResponsePrecommitTimeout {
		t.Fatalf("precommit attribution outcome = %+v", outcome)
	}
	// Precommit alone.
	outcome = raceStreamReadWithDeadlines(w9cUnsettledPending(clock.NowMs), nil, nil, nil, int64PtrOf(5))
	if outcome.kind != raceResponsePrecommitTimeout {
		t.Fatalf("precommit outcome = %+v", outcome)
	}
	// Abort signal.
	signal := make(chan struct{})
	close(signal)
	outcome = raceStreamReadWithDeadlines(w9cUnsettledPending(clock.NowMs), signal, nil, int64PtrOf(60000), nil)
	if outcome.kind != raceAbort {
		t.Fatalf("abort outcome = %+v", outcome)
	}
	// planTimeoutMs projection.
	if planTimeoutMs(nil) != nil {
		t.Fatal("nil plan has no timeout")
	}
	value := int64(7)
	if got := planTimeoutMs(&StreamReadPlan{TimeoutMs: 7}); got != &value && *got != 7 {
		t.Fatal("plan timeout projection")
	}
	if derefInt64(nil) != 0 || derefInt64(&value) != 7 || derefInt64Zero(nil) != 0 {
		t.Fatal("deref helpers")
	}
	if ceilDiv(0, 1000) != 0 || ceilDiv(1, 1000) != 1 || ceilDiv(2500, 1000) != 3 || ceilDiv(5, 0) != 0 {
		t.Fatal("ceilDiv semantics")
	}
}

func TestW9CSettleFirstByteDeadlineReadDecision(t *testing.T) {
	clock := &w9cClock{now: 1000}
	pipe := w9cNewPipe(t, clock)
	deadlineMs := int64(4000)
	pipe.options.FirstByteDeadlineMs = &deadlineMs

	// No pending decision: no-op.
	if err := pipe.settleStreamFirstByteDeadlineReadDecision(false); err != nil {
		t.Fatalf("nil decision = %v", err)
	}
	// Semantic result in read: superseded hook runs, no error.
	superseded := 0
	pipe.options.OnFirstByteDeadlineSuperseded = func() { superseded++ }
	pipe.pendingReadDecision = &streamFirstByteDeadlineReadDecision{}
	if err := pipe.settleStreamFirstByteDeadlineReadDecision(true); err != nil || superseded != 1 {
		t.Fatalf("semantic settle = %v superseded=%d", err, superseded)
	}
	// decisionError propagates.
	pipe.pendingReadDecision = &streamFirstByteDeadlineReadDecision{decisionError: io.EOF}
	if err := pipe.settleStreamFirstByteDeadlineReadDecision(false); !errorsIs(err, io.EOF) {
		t.Fatalf("decision error settle = %v", err)
	}
	// Abort action surfaces the first-byte timeout.
	pipe.pendingReadDecision = &streamFirstByteDeadlineReadDecision{hasAction: true, action: FirstByteDeadlineAbort}
	err := pipe.settleStreamFirstByteDeadlineReadDecision(false)
	timeout, ok := err.(*FirstByteTimeoutError)
	if !ok || timeout.Source != "configured_deadline" || timeout.TimeoutMs != 4000 {
		t.Fatalf("abort settle = %v", err)
	}
	// Continue action clears without error.
	pipe.pendingReadDecision = &streamFirstByteDeadlineReadDecision{hasAction: true, action: FirstByteDeadlineContinue}
	if err := pipe.settleStreamFirstByteDeadlineReadDecision(false); err != nil {
		t.Fatalf("continue settle = %v", err)
	}
	if pipe.pendingReadDecision != nil {
		t.Fatal("decision must be consumed")
	}
}

func TestW9CReadNextStreamChunkArms(t *testing.T) {
	clock := &w9cClock{now: 1000}
	pipe := w9cNewPipe(t, clock)

	// Plain read path.
	pipe.body = w9cChanBody(ChunkResult{Data: []byte(chatDeltaChunk)})
	result, err := pipe.readNextStreamChunk()
	if err != nil || !strings.Contains(string(result.chunk.Data), "chat.completion.chunk") {
		t.Fatalf("plain read = %v, %v", result, err)
	}

	// Precommit deadline already passed.
	past := int64(900)
	pipe2 := w9cNewPipe(t, clock)
	pipe2.options.ResponsePrecommitDeadlineAtMs = &past
	if _, err := pipe2.readNextStreamChunk(); err == nil || !IsResponsePrecommitDeadlineError(err) {
		t.Fatalf("past precommit = %v", err)
	}

	// First-byte deadline expired with an aborting handler.
	pipe3 := w9cNewPipe(t, clock)
	deadline := int64(500)
	pipe3.options.FirstByteDeadlineMs = &deadline
	pipe3.options.OnFirstByteDeadline = func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
		return FirstByteDeadlineAbort, nil
	}
	pipe3.body = w9cChanBody()
	_, err = pipe3.readNextStreamChunk()
	timeout, ok := err.(*FirstByteTimeoutError)
	if !ok || timeout.Source != "configured_deadline" || timeout.TimeoutMs != 500 {
		t.Fatalf("expired first byte = %v", err)
	}

	// First-byte deadline expired while the precommit wall already passed.
	pipe4 := w9cNewPipe(t, clock)
	pipe4.options.FirstByteDeadlineMs = &deadline
	precommitAt := int64(1200)
	pipe4.options.ResponsePrecommitDeadlineAtMs = &precommitAt
	pipe4.body = w9cChanBody()
	_, err = pipe4.readNextStreamChunk()
	if !IsResponsePrecommitDeadlineError(err) {
		t.Fatalf("expired deadline under wall = %v", err)
	}

	// Client abort surfaces the upstream-aborted error.
	pipe5 := w9cNewPipe(t, clock)
	signal := make(chan struct{})
	close(signal)
	pipe5.input.Signal = chanSignal{ch: signal}
	pipe5.body = w9cChanBody()
	_, err = pipe5.readNextStreamChunk()
	if _, ok := err.(*UpstreamRequestAbortedError); !ok {
		t.Fatalf("abort read = %v", err)
	}
}

func TestW9CStreamPipeStatusHelpers(t *testing.T) {
	clock := &w9cClock{now: 1000}
	pipe := w9cNewPipe(t, clock)
	if !pipe.waitingForFirstOutputStatus() == false && true {
		t.Fatal("placeholder")
	}
	// No deadline configured: never waiting for first output.
	deadline := int64(3000)
	pipe.options.FirstByteDeadlineMs = &deadline
	if !pipe.waitingForFirstOutputStatus() {
		t.Fatal("fresh pipe with deadline waits for first output")
	}
	firstToken := int64(1100)
	pipe.firstTokenMs = &firstToken
	if pipe.waitingForFirstOutputStatus() {
		t.Fatal("first token observed stops the wait")
	}
	pipe.firstTokenMs = nil
	pipe.downstreamCommit.MarkSemanticCommitted(1)
	if pipe.waitingForFirstOutputStatus() {
		t.Fatal("semantic commit stops the wait")
	}
	pipe.downstreamCommit = &DownstreamCommitState{}

	status := pipe.readPlanStatus()
	if !status.WaitingForFirstChunk || status.LastUpstreamActivityAt != 1000 {
		t.Fatalf("read plan status = %+v", status)
	}
	// signalChannel without a signal is nil.
	if pipe.signalChannel() != nil {
		t.Fatal("nil signal must project nil")
	}
	// streamReadPlanTimeoutError kinds.
	if err := pipe.streamReadPlanTimeoutError(&StreamReadPlan{TimeoutKind: "stream_lifetime", TimeoutMessage: "m"}); !strings.Contains(err.Error(), "m") {
		t.Fatalf("lifetime error = %v", err)
	}
	if err := pipe.streamReadPlanTimeoutError(&StreamReadPlan{TimeoutKind: "idle", TimeoutMessage: "i"}); err == nil {
		t.Fatal("idle error expected")
	}
	// Zero profile: every plan timer is immediate and the loop surfaces the
	// max-lifetime plan timeout.
	pipe.body = w9cChanBody()
	pipe.waitingForFirstChunk = true
	pipe.profile = TimeoutProfile{}
	_, err := pipe.readNextStreamChunk()
	planTimeout, ok := err.(*StreamReadPlanTimeoutError)
	if !ok || planTimeout.TimeoutKind != "stream_lifetime" {
		t.Fatalf("zero profile timeout = %v", err)
	}
}

// w9cChanBody wraps a chunk channel as an UpstreamBody.
type w9cBody struct {
	ch   chan ChunkResult
	once sync.Once
}

func w9cChanBody(chunks ...ChunkResult) *w9cBody {
	body := &w9cBody{ch: make(chan ChunkResult, len(chunks)+1)}
	for _, chunk := range chunks {
		body.ch <- chunk
	}
	if len(chunks) == 0 {
		// stay open: reads block until a later push or test end
	}
	return body
}

func (b *w9cBody) Next() <-chan ChunkResult { return b.ch }
func (b *w9cBody) Close()                   { b.once.Do(func() { close(b.ch) }) }
