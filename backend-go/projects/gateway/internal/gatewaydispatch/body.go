package gatewaydispatch

import (
	"context"
	"io"
	"strconv"
	"time"
)

// Non-stream upstream body piping, migrated from upstream/body.ts. The
// express Response write side becomes a DownstreamWriter; backpressure
// bookkeeping (res.write return value / drain events) is owned by the Go
// HTTP server, so writeResponseChunk degrades to a plain Write plus
// client-cancellation detection — recorded as a behavioral difference in the
// migration report.

// NonStreamPipeResult mirrors NonStreamPipeResult.
type NonStreamPipeResult struct {
	FirstByteMs        *int64
	CapturedBody       []byte
	CapturedBodyText   *string
	DiagnosticBodyText *string
	UsageTailText      *string
	CaptureTruncated   bool
	TransferredBytes   int
}

// InspectableNonStreamPipeResult mirrors InspectableNonStreamPipeResult.
type InspectableNonStreamPipeResult struct {
	NonStreamPipeResult
	FullyBuffered           bool
	InspectionLimitExceeded bool
	CompleteBody            []byte
	CompleteBodyText        *string
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

// Capture size constants mirror upstream/body.ts.
const (
	NonStreamResponseCaptureBytes  = 2 * 1024 * 1024
	NonStreamUsageTailCaptureBytes = 256 * 1024
	UpstreamErrorBodyCaptureBytes  = 256 * 1024
	// ResponseBackpressureWarnThresholdMs mirrors the Node constant; the Go
	// write path never blocks on drain bookkeeping (see doc comment).
	ResponseBackpressureWarnThresholdMs = 50
)

// DownstreamWriter is the downstream write target of the pipe functions.
type DownstreamWriter interface {
	Write(p []byte) (int, error)
}

// NonStreamPipeInput mirrors pipeNonStreamUpstreamResponse's input bag.
type NonStreamPipeInput struct {
	StartedAt                     int64
	CaptureBytes                  *int64
	UsageTailBytes                *int64
	CaptureBody                   *bool
	Signal                        context.Context
	OnFirstByte                   func()
	FirstByteTimeoutMs            *int64
	FirstByteDeadlineMs           *int64
	ResponsePrecommitDeadlineAtMs *int64
	MaxLifetimeMs                 *int64
	OnFirstByteDeadline           FirstByteDeadlineHandler
	OnFirstByteDeadlineSuperseded func()
	PrepareDownstream             func()
	OnChunkRead                   func([]byte)
	OnChunkWritten                func(int)
	OnBodyCompleted               func(int)
}

// PipeNonStreamUpstreamResponse mirrors pipeNonStreamUpstreamResponse.
func PipeNonStreamUpstreamResponse(
	ctx context.Context,
	upstreamBody io.Reader,
	downstream DownstreamWriter,
	input NonStreamPipeInput,
) (NonStreamPipeResult, error) {
	result, _, err := pipeNonStreamUpstreamResponseCommon(ctx, upstreamBody, downstream, input, false, nil)
	return result.result, err
}

// InspectableNonStreamPipeInput mirrors the inspection variant's input.
type InspectableNonStreamPipeInput struct {
	NonStreamPipeInput
	InspectBytes           int
	RequireFullyBuffered   bool
	BeforeDownstreamCommit func(inspectionBody []byte) error
}

// PipeNonStreamUpstreamResponseForInspection mirrors
// pipeNonStreamUpstreamResponseForInspection.
func PipeNonStreamUpstreamResponseForInspection(
	ctx context.Context,
	upstreamBody io.Reader,
	downstream DownstreamWriter,
	input InspectableNonStreamPipeInput,
) (InspectableNonStreamPipeResult, error) {
	result, _, err := pipeNonStreamUpstreamResponseCommon(ctx, upstreamBody, downstream, input.NonStreamPipeInput, false, &inspectPlan{
		inspectBytes:           input.InspectBytes,
		requireFullyBuffered:   input.RequireFullyBuffered,
		beforeDownstreamCommit: input.BeforeDownstreamCommit,
	})
	if err != nil {
		return InspectableNonStreamPipeResult{}, err
	}
	return InspectableNonStreamPipeResult{
		NonStreamPipeResult:     result.result,
		FullyBuffered:           result.fullyBuffered,
		InspectionLimitExceeded: result.inspectionLimitExceeded,
		CompleteBody:            result.completeBody,
		CompleteBodyText:        result.completeBodyText,
	}, nil
}

// inspectPlan carries the inspection variant configuration.
type inspectPlan struct {
	inspectBytes           int
	requireFullyBuffered   bool
	beforeDownstreamCommit func([]byte) error
}

// inspectOutcome extends the pipe result with inspection fields.
type inspectOutcome struct {
	result                  NonStreamPipeResult
	fullyBuffered           bool
	inspectionLimitExceeded bool
	completeBody            []byte
	completeBodyText        *string
}

func pipeNonStreamUpstreamResponseCommon(
	ctx context.Context,
	upstreamBody io.Reader,
	downstream DownstreamWriter,
	input NonStreamPipeInput,
	_ bool,
	inspect *inspectPlan,
) (inspectOutcome, bool, error) {
	signal := input.Signal
	if signal == nil {
		signal = ctx
	}
	if signal == nil {
		signal = context.Background()
	}
	captureLimit := NonStreamResponseCaptureBytes
	if input.CaptureBytes != nil {
		captureLimit = int(*input.CaptureBytes)
	}
	if input.CaptureBody != nil && !*input.CaptureBody {
		captureLimit = -1
	}
	capture := newLimitedBufferCapture(captureLimit)
	usageTailLimit := NonStreamUsageTailCaptureBytes
	if input.UsageTailBytes != nil {
		usageTailLimit = int(*input.UsageTailBytes)
	}
	usageTailCapture := newRollingBufferCapture(usageTailLimit)
	maxLifetimeDeadlineAt := nonStreamBodyMaxLifetimeDeadlineAt(input.StartedAt, input.MaxLifetimeMs)
	var transferredBytes int
	var firstByteMs int64
	firstByteSeen := false
	firstByteDeadlineObserved := false
	downstreamPrepared := false
	downstreamWriting := false
	var inspectionChunks [][]byte
	inspectionBytes := 0
	buffer := make([]byte, 32*1024)

	prepareDownstreamForWrite := func() {
		if downstreamPrepared {
			return
		}
		downstreamPrepared = true
		if input.PrepareDownstream != nil {
			input.PrepareDownstream()
		}
	}
	writeBufferedInspectionChunks := func() error {
		if len(inspectionChunks) == 0 {
			return nil
		}
		prepareDownstreamForWrite()
		for _, chunk := range inspectionChunks {
			if _, err := downstream.Write(chunk); err != nil {
				return err
			}
			if input.OnChunkWritten != nil {
				input.OnChunkWritten(len(chunk))
			}
		}
		inspectionChunks = nil
		return nil
	}
	failWhileWriting := func(err error) error {
		_ = closeReader(upstreamBody)
		if IsUpstreamRequestAbortedError(err) || signal.Err() != nil {
			return &UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}
		}
		return &NonStreamUpstreamBodyPipeError{
			Message:       errorMessageOf(err, "上游非流式响应正文中断"),
			PartialResult: buildNonStreamPipeResult(capture, usageTailCapture, firstByteSeen, firstByteMs, transferredBytes),
			OriginalError: err,
		}
	}

	for {
		if signal.Err() != nil {
			return inspectOutcome{}, downstreamWriting, &UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}
		}

		read, deadlineObserved, readErr := readFirstNonStreamChunk(input, upstreamBody, buffer, &firstByteSeen, firstByteDeadlineObserved, maxLifetimeDeadlineAt)
		firstByteDeadlineObserved = deadlineObserved
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return inspectOutcome{}, downstreamWriting, failWhileWriting(readErr)
		}
		if read.done {
			break
		}
		chunk := buffer[:read.n]
		if !firstByteSeen {
			firstByteSeen = true
			firstByteMs = NowMs() - input.StartedAt
			if inspect == nil && !downstreamPrepared {
				downstreamPrepared = true
				if input.PrepareDownstream != nil {
					input.PrepareDownstream()
				}
			}
			if input.OnFirstByte != nil {
				input.OnFirstByte()
			}
		}
		transferredBytes += read.n
		captured := append([]byte(nil), chunk...)
		capture.push(captured)
		usageTailCapture.push(captured)
		if input.OnChunkRead != nil {
			input.OnChunkRead(captured)
		}

		if inspect != nil {
			// Inspection buffering branch (Node
			// pipeNonStreamUpstreamResponseForInspection).
			if !downstreamWriting && inspectionBytes+len(captured) <= inspect.inspectBytes {
				inspectionChunks = append(inspectionChunks, captured)
				inspectionBytes += len(captured)
				continue
			}
			if !downstreamWriting {
				var inspectionBody []byte
				if inspectionBytes+len(captured) <= inspect.inspectBytes {
					inspectionBody = make([]byte, 0, inspectionBytes+len(captured))
					for _, item := range inspectionChunks {
						inspectionBody = append(inspectionBody, item...)
					}
					inspectionBody = append(inspectionBody, captured...)
				} else {
					inspectionBody = make([]byte, 0, inspect.inspectBytes)
					for _, item := range inspectionChunks {
						inspectionBody = append(inspectionBody, item...)
					}
					remaining := inspect.inspectBytes - inspectionBytes
					if remaining > 0 {
						inspectionBody = append(inspectionBody, captured[:remaining]...)
					}
				}
				if inspect.requireFullyBuffered {
					_ = closeReader(upstreamBody)
					outcome := inspectOutcome{
						result:                  buildNonStreamPipeResult(capture, usageTailCapture, firstByteSeen, firstByteMs, transferredBytes),
						fullyBuffered:           false,
						inspectionLimitExceeded: true,
						completeBody:            inspectionBody,
						completeBodyText:        bytesTextPtr(inspectionBody),
					}
					return outcome, downstreamWriting, nil
				}
				if inspect.beforeDownstreamCommit != nil {
					if err := inspect.beforeDownstreamCommit(inspectionBody); err != nil {
						_ = closeReader(upstreamBody)
						return inspectOutcome{}, downstreamWriting, err
					}
				}
				downstreamWriting = true
				if err := writeBufferedInspectionChunks(); err != nil {
					return inspectOutcome{}, downstreamWriting, failWhileWriting(err)
				}
			}
			prepareDownstreamForWrite()
			if _, err := downstream.Write(captured); err != nil {
				return inspectOutcome{}, downstreamWriting, failWhileWriting(err)
			}
			if input.OnChunkWritten != nil {
				input.OnChunkWritten(len(captured))
			}
			continue
		}

		if _, err := downstream.Write(captured); err != nil {
			return inspectOutcome{}, downstreamWriting, failWhileWriting(err)
		}
		if input.OnChunkWritten != nil {
			input.OnChunkWritten(read.n)
		}
	}

	_ = closeReader(upstreamBody)
	if inspect != nil && downstreamWriting {
		if !downstreamPrepared {
			prepareDownstreamForWrite()
		}
		return inspectOutcome{
			result:        buildNonStreamPipeResult(capture, usageTailCapture, firstByteSeen, firstByteMs, transferredBytes),
			fullyBuffered: false,
		}, downstreamWriting, nil
	}
	if inspect != nil {
		var completeBody []byte
		for _, chunk := range inspectionChunks {
			completeBody = append(completeBody, chunk...)
		}
		outcome := inspectOutcome{
			result:           buildNonStreamPipeResult(capture, usageTailCapture, firstByteSeen, firstByteMs, transferredBytes),
			fullyBuffered:    true,
			completeBody:     completeBody,
			completeBodyText: bytesTextPtr(completeBody),
		}
		return outcome, downstreamWriting, nil
	}

	if !downstreamPrepared {
		prepareDownstreamForWrite()
	}
	if input.OnBodyCompleted != nil {
		input.OnBodyCompleted(transferredBytes)
	}
	return inspectOutcome{
		result: buildNonStreamPipeResult(capture, usageTailCapture, firstByteSeen, firstByteMs, transferredBytes),
	}, downstreamWriting, nil
}

// chunkRead is one consumed buffer chunk.
type chunkRead struct {
	n    int
	done bool
}

// readFirstNonStreamChunk dispatches between the first-chunk deadline reader
// and the absolute-deadline reader for later chunks.
func readFirstNonStreamChunk(
	input NonStreamPipeInput,
	reader io.Reader,
	buffer []byte,
	firstByteSeen *bool,
	firstByteDeadlineObserved bool,
	maxLifetimeDeadlineAt *int64,
) (chunkRead, bool, error) {
	if !*firstByteSeen {
		return readFirstNonStreamChunkWithDeadlines(reader, buffer, input.StartedAt, firstByteDeadlineReadInput{
			startedAt:                     input.StartedAt,
			signal:                        input.Signal,
			firstByteTimeoutMs:            input.FirstByteTimeoutMs,
			firstByteDeadlineMs:           input.FirstByteDeadlineMs,
			firstByteDeadlineObserved:     firstByteDeadlineObserved,
			onFirstByteDeadline:           input.OnFirstByteDeadline,
			onFirstByteDeadlineSuperseded: input.OnFirstByteDeadlineSuperseded,
			responsePrecommitDeadlineAtMs: input.ResponsePrecommitDeadlineAtMs,
			pendingReadSupersedesDeadline: true,
			maxLifetimeDeadlineAt:         maxLifetimeDeadlineAt,
			maxLifetimeMs:                 input.MaxLifetimeMs,
		})
	}
	n, err, _ := readNonStreamChunkWithAbsoluteDeadline(reader, buffer, input.Signal, maxLifetimeDeadlineAt, input.MaxLifetimeMs, input.ResponsePrecommitDeadlineAtMs)
	if err == io.EOF {
		return chunkRead{done: true}, firstByteDeadlineObserved, nil
	}
	return chunkRead{n: n}, firstByteDeadlineObserved, err
}

// firstByteDeadlineReadInput groups the deadline inputs of the first chunk.
type firstByteDeadlineReadInput struct {
	startedAt                     int64
	signal                        context.Context
	firstByteTimeoutMs            *int64
	firstByteDeadlineMs           *int64
	firstByteDeadlineObserved     bool
	onFirstByteDeadline           FirstByteDeadlineHandler
	onFirstByteDeadlineSuperseded func()
	responsePrecommitDeadlineAtMs *int64
	pendingReadSupersedesDeadline bool
	maxLifetimeDeadlineAt         *int64
	maxLifetimeMs                 *int64
}

// readFirstNonStreamChunkWithDeadlines mirrors
// readFirstNonStreamChunkWithDeadlines.
func readFirstNonStreamChunkWithDeadlines(
	reader io.Reader,
	buffer []byte,
	startedAt int64,
	input firstByteDeadlineReadInput,
) (chunkRead, bool, error) {
	if input.firstByteTimeoutMs == nil && input.firstByteDeadlineMs == nil &&
		input.responsePrecommitDeadlineAtMs == nil && input.maxLifetimeDeadlineAt == nil {
		n, err := ReadStreamChunkWithAbort(input.signal, reader, buffer)
		if err == io.EOF {
			return chunkRead{done: true}, input.firstByteDeadlineObserved, nil
		}
		return chunkRead{n: n}, input.firstByteDeadlineObserved, err
	}

	pendingRead := ObserveFirstBytePendingRead(func() (chunkResult, error) {
		n, err := reader.Read(buffer)
		return chunkResult{n: n, err: err}, err
	})
	var hardDeadlineAt *int64
	if input.firstByteTimeoutMs != nil {
		at := NowMs() + *input.firstByteTimeoutMs
		hardDeadlineAt = &at
	}
	var softDeadlineAt *int64
	if input.firstByteDeadlineMs != nil && !input.firstByteDeadlineObserved {
		at := startedAt + *input.firstByteDeadlineMs
		softDeadlineAt = &at
	}
	observed := input.firstByteDeadlineObserved

	fail := func(err error) (chunkRead, bool, error) {
		return chunkRead{}, observed, err
	}

	for {
		now := NowMs()
		var maxLifetimeRemainingMs *int64
		if input.maxLifetimeDeadlineAt != nil {
			remaining := *input.maxLifetimeDeadlineAt - now
			maxLifetimeRemainingMs = &remaining
		}
		var responsePrecommitRemainingMs *int64
		if input.responsePrecommitDeadlineAtMs != nil {
			remaining := *input.responsePrecommitDeadlineAtMs - now
			responsePrecommitRemainingMs = &remaining
		}
		if responsePrecommitRemainingMs != nil && *responsePrecommitRemainingMs <= 0 &&
			(maxLifetimeRemainingMs == nil || derefMin(input.responsePrecommitDeadlineAtMs, input.maxLifetimeDeadlineAt)) {
			return fail(&GatewayResponsePrecommitDeadlineError{DeadlineAtMs: *input.responsePrecommitDeadlineAtMs})
		}
		if maxLifetimeRemainingMs != nil && *maxLifetimeRemainingMs <= 0 {
			return fail(&UpstreamBodyReadMaxLifetimeError{TimeoutMs: derefInt64(input.maxLifetimeMs)})
		}
		var softRemainingMs *int64
		if softDeadlineAt != nil && !observed {
			remaining := *softDeadlineAt - now
			softRemainingMs = &remaining
		}
		if softRemainingMs != nil && *softRemainingMs <= 0 {
			observed = true
			decision := decideAfterPendingRead(pendingRead, input)
			if decision.precommit {
				return fail(decision.err)
			}
			if decision.hasRead {
				return firstNonStreamReadAfterDeadlineDecision(decision, observed, input)
			}
			if decision.action == FirstByteDeadlineActionAbort {
				return fail(&GatewayFirstByteTimeoutError{
					Message:   "上游非流式响应 " + formatSecondsText(derefInt64(input.firstByteDeadlineMs)) + " 后仍未返回首个字节",
					TimeoutMs: derefInt64(input.firstByteDeadlineMs),
					Source:    FirstByteTimeoutSourceConfiguredDeadline,
				})
			}
			continue
		}

		var hardRemainingMs *int64
		if hardDeadlineAt != nil {
			remaining := *hardDeadlineAt - now
			hardRemainingMs = &remaining
		}
		if hardRemainingMs != nil && *hardRemainingMs <= 0 {
			return fail(&GatewayFirstByteTimeoutError{
				Message:   "上游非流式响应 " + formatSecondsText(derefInt64(input.firstByteTimeoutMs)) + " 后仍未返回首个字节",
				TimeoutMs: derefInt64(input.firstByteTimeoutMs),
			})
		}

		raceType, result, _ := raceReadWithDeadlines(pendingRead, input.signal, softRemainingMs, hardRemainingMs, maxLifetimeRemainingMs, responsePrecommitRemainingMs)
		switch raceType {
		case raceReadDone:
			if input.responsePrecommitDeadlineAtMs != nil {
				settledAt, _ := pendingRead.SettledAtMs()
				if settledAt > *input.responsePrecommitDeadlineAtMs {
					return fail(&GatewayResponsePrecommitDeadlineError{DeadlineAtMs: *input.responsePrecommitDeadlineAtMs})
				}
			}
			if result.err == io.EOF {
				return chunkRead{done: true}, observed, nil
			}
			return chunkRead{n: result.n}, observed, result.err
		case raceAbort:
			return fail(&UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true})
		case raceHardTimeout:
			return fail(&GatewayFirstByteTimeoutError{
				Message:   "上游非流式响应 " + formatSecondsText(derefInt64(input.firstByteTimeoutMs)) + " 后仍未返回首个字节",
				TimeoutMs: derefInt64(input.firstByteTimeoutMs),
			})
		case raceMaxLifetimeTimeout:
			return fail(&UpstreamBodyReadMaxLifetimeError{TimeoutMs: derefInt64(input.maxLifetimeMs)})
		case raceResponsePrecommitTimeout:
			return fail(&GatewayResponsePrecommitDeadlineError{DeadlineAtMs: derefInt64(input.responsePrecommitDeadlineAtMs)})
		}

		// soft_timeout → routing decision (Node repeats the loop on 'continue').
		observed = true
		decision := decideAfterPendingRead(pendingRead, input)
		if decision.precommit {
			return fail(decision.err)
		}
		if decision.hasRead {
			return firstNonStreamReadAfterDeadlineDecision(decision, observed, input)
		}
		if decision.action == FirstByteDeadlineActionAbort {
			return fail(&GatewayFirstByteTimeoutError{
				Message:   "上游非流式响应 " + formatSecondsText(derefInt64(input.firstByteDeadlineMs)) + " 后仍未返回首个字节",
				TimeoutMs: derefInt64(input.firstByteDeadlineMs),
				Source:    FirstByteTimeoutSourceConfiguredDeadline,
			})
		}
	}
}

// deadlineDecision flattens FirstByteDeadlineDecisionResult for the reader.
type deadlineDecision struct {
	precommit   bool
	hasRead     bool
	read        chunkResult
	action      FirstByteDeadlineAction
	decisionErr error
	err         error
}

func decideAfterPendingRead(pendingRead *ObservedFirstBytePendingRead[chunkResult], input firstByteDeadlineReadInput) deadlineDecision {
	decision := DecideFirstByteDeadlineAfterPendingRead(pendingRead, input.onFirstByteDeadline, FirstByteDeadlineDecisionInput{
		ElapsedMs: NowMs() - input.startedAt,
		TimeoutMs: derefInt64(input.firstByteDeadlineMs),
		Transport: "non_stream",
	}, FirstByteDeadlineDecisionWaitOptions{
		ResponsePrecommitDeadlineAtMs: input.responsePrecommitDeadlineAtMs,
		OnResponsePrecommitDeadline:   input.onFirstByteDeadlineSuperseded,
	})
	switch decision.Type {
	case DeadlineDecisionResponsePrecommit:
		return deadlineDecision{precommit: true, err: decision.Error}
	case DeadlineDecisionRead:
		return deadlineDecision{hasRead: true, read: decision.Result, action: decision.Action, decisionErr: decision.DecisionError, err: decision.Error}
	default:
		return deadlineDecision{action: decision.Action}
	}
}

// firstNonStreamReadAfterDeadlineDecision mirrors
// firstNonStreamReadAfterDeadlineDecision.
func firstNonStreamReadAfterDeadlineDecision(
	decision deadlineDecision,
	firstByteDeadlineObserved bool,
	input firstByteDeadlineReadInput,
) (chunkRead, bool, error) {
	if input.pendingReadSupersedesDeadline {
		if input.onFirstByteDeadlineSuperseded != nil {
			input.onFirstByteDeadlineSuperseded()
		}
		if decision.read.err == io.EOF {
			return chunkRead{done: true}, firstByteDeadlineObserved, nil
		}
		return chunkRead{n: decision.read.n}, firstByteDeadlineObserved, decision.read.err
	}
	if decision.decisionErr != nil {
		return chunkRead{}, firstByteDeadlineObserved, decision.decisionErr
	}
	if decision.action == FirstByteDeadlineActionAbort {
		return chunkRead{}, firstByteDeadlineObserved, &GatewayFirstByteTimeoutError{
			Message:   "上游非流式响应 " + formatSecondsText(derefInt64(input.firstByteDeadlineMs)) + " 后仍未返回完整语义响应",
			TimeoutMs: derefInt64(input.firstByteDeadlineMs),
			Source:    FirstByteTimeoutSourceConfiguredDeadline,
		}
	}
	if decision.read.err == io.EOF {
		return chunkRead{done: true}, firstByteDeadlineObserved, nil
	}
	return chunkRead{n: decision.read.n}, firstByteDeadlineObserved, decision.read.err
}

// chunkResult carries a raw buffered read through the generic observer.
type chunkResult struct {
	n   int
	err error
}

// race outcome tags mirror raceReadWithDeadlines.
type raceReadType int

const (
	raceReadDone raceReadType = iota
	raceSoftTimeout
	raceHardTimeout
	raceMaxLifetimeTimeout
	raceResponsePrecommitTimeout
	raceAbort
)

// raceReadWithDeadlines mirrors raceReadWithDeadlines. The shared request
// precommit deadline is a hard wall: when it coincides with the configurable
// speed-first deadline, the wall-clock attribution wins
// (responsePrecommitTimeoutMs <= softTimeoutMs → response_precommit_timeout).
func raceReadWithDeadlines(
	pendingRead *ObservedFirstBytePendingRead[chunkResult],
	signal context.Context,
	softTimeoutMs *int64,
	hardTimeoutMs *int64,
	maxLifetimeTimeoutMs *int64,
	responsePrecommitTimeoutMs *int64,
) (raceReadType, chunkResult, error) {
	type raceEvent struct {
		tag    raceReadType
		result chunkResult
		err    error
	}
	events := make(chan raceEvent, 5)
	if softTimeoutMs != nil {
		after := time.After(time.Duration(maxInt64(1, *softTimeoutMs)) * time.Millisecond)
		go func() { <-after; events <- raceEvent{tag: raceSoftTimeout} }()
	}
	if hardTimeoutMs != nil {
		after := time.After(time.Duration(maxInt64(1, *hardTimeoutMs)) * time.Millisecond)
		go func() { <-after; events <- raceEvent{tag: raceHardTimeout} }()
	}
	if responsePrecommitTimeoutMs != nil {
		after := time.After(time.Duration(maxInt64(1, *responsePrecommitTimeoutMs)) * time.Millisecond)
		go func() { <-after; events <- raceEvent{tag: raceResponsePrecommitTimeout} }()
	}
	if maxLifetimeTimeoutMs != nil {
		after := time.After(time.Duration(maxInt64(1, *maxLifetimeTimeoutMs)) * time.Millisecond)
		go func() { <-after; events <- raceEvent{tag: raceMaxLifetimeTimeout} }()
	}
	if signal != nil {
		go func() {
			<-signal.Done()
			events <- raceEvent{tag: raceAbort}
		}()
	}

	readDone := make(chan raceEvent, 1)
	go func() {
		result, err := pendingRead.Await()
		readDone <- raceEvent{result: result, err: err}
	}()

	select {
	case event := <-readDone:
		return raceReadDone, event.result, event.err
	case event := <-events:
		switch event.tag {
		case raceSoftTimeout:
			if responsePrecommitTimeoutMs != nil && *responsePrecommitTimeoutMs <= derefOrMax(softTimeoutMs) {
				return raceResponsePrecommitTimeout, chunkResult{}, nil
			}
			return raceSoftTimeout, chunkResult{}, nil
		default:
			return event.tag, chunkResult{}, nil
		}
	}
}

func readNonStreamChunkWithAbsoluteDeadline(
	reader io.Reader,
	buffer []byte,
	signal context.Context,
	maxLifetimeDeadlineAt *int64,
	maxLifetimeMs *int64,
	responsePrecommitDeadlineAtMs *int64,
) (int, error, bool) {
	if maxLifetimeDeadlineAt == nil && responsePrecommitDeadlineAtMs == nil {
		n, err := ReadStreamChunkWithAbort(signal, reader, buffer)
		return n, err, false
	}
	now := NowMs()
	var maxLifetimeRemainingMs *int64
	if maxLifetimeDeadlineAt != nil {
		remaining := *maxLifetimeDeadlineAt - now
		maxLifetimeRemainingMs = &remaining
	}
	var responsePrecommitRemainingMs *int64
	if responsePrecommitDeadlineAtMs != nil {
		remaining := *responsePrecommitDeadlineAtMs - now
		responsePrecommitRemainingMs = &remaining
	}
	if responsePrecommitRemainingMs != nil && *responsePrecommitRemainingMs <= 0 &&
		(maxLifetimeRemainingMs == nil || derefMin(responsePrecommitDeadlineAtMs, maxLifetimeDeadlineAt)) {
		return 0, &GatewayResponsePrecommitDeadlineError{DeadlineAtMs: *responsePrecommitDeadlineAtMs}, false
	}
	if maxLifetimeRemainingMs != nil && *maxLifetimeRemainingMs <= 0 {
		return 0, &UpstreamBodyReadMaxLifetimeError{TimeoutMs: derefInt64(maxLifetimeMs)}, false
	}
	pendingRead := ObserveFirstBytePendingRead(func() (chunkResult, error) {
		n, err := reader.Read(buffer)
		return chunkResult{n: n, err: err}, err
	})
	raceType, result, _ := raceReadWithDeadlines(pendingRead, signal, nil, nil, maxLifetimeRemainingMs, responsePrecommitRemainingMs)
	switch raceType {
	case raceReadDone:
		if responsePrecommitDeadlineAtMs != nil {
			settledAt, _ := pendingRead.SettledAtMs()
			if settledAt > *responsePrecommitDeadlineAtMs {
				return 0, &GatewayResponsePrecommitDeadlineError{DeadlineAtMs: *responsePrecommitDeadlineAtMs}, false
			}
		}
		return result.n, result.err, false
	case raceAbort:
		return 0, &UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}, false
	case raceMaxLifetimeTimeout:
		return 0, &UpstreamBodyReadMaxLifetimeError{TimeoutMs: derefInt64(maxLifetimeMs)}, false
	case raceResponsePrecommitTimeout:
		return 0, &GatewayResponsePrecommitDeadlineError{DeadlineAtMs: derefInt64(responsePrecommitDeadlineAtMs)}, false
	}
	return 0, &UpstreamBodyReadMaxLifetimeError{TimeoutMs: derefInt64(maxLifetimeMs)}, false
}

func nonStreamBodyMaxLifetimeDeadlineAt(startedAt int64, maxLifetimeMs *int64) *int64 {
	if maxLifetimeMs == nil || *maxLifetimeMs <= 0 {
		return nil
	}
	at := startedAt + maxInt64(1, *maxLifetimeMs)
	return &at
}

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
		maxBytes = maxInt64(0, *input.MaxBytes)
	}
	capture := newLimitedBufferCapture(int(maxBytes))
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
			_ = closeReader(upstreamBody)
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
		capture.push(chunk)
		if capture.truncated {
			truncated = true
			_ = closeReader(upstreamBody)
			break
		}
	}
	body := capture.buffer()
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

// limitedBufferCapture mirrors LimitedBufferCapture.
type limitedBufferCapture struct {
	chunks    [][]byte
	size      int
	truncated bool
	limit     int
}

func newLimitedBufferCapture(limitBytes int) *limitedBufferCapture {
	return &limitedBufferCapture{limit: limitBytes}
}

func (c *limitedBufferCapture) push(buffer []byte) {
	if len(buffer) == 0 || c.limit < 0 {
		return
	}
	remaining := c.limit - c.size
	if remaining <= 0 {
		c.truncated = true
		return
	}
	if len(buffer) > remaining {
		c.chunks = append(c.chunks, append([]byte(nil), buffer[:remaining]...))
		c.size += remaining
		c.truncated = true
		return
	}
	c.chunks = append(c.chunks, append([]byte(nil), buffer...))
	c.size += len(buffer)
}

func (c *limitedBufferCapture) buffer() []byte {
	out := make([]byte, 0, c.size)
	for _, chunk := range c.chunks {
		out = append(out, chunk...)
	}
	return out
}

func (c *limitedBufferCapture) completeBuffer() []byte {
	if c.truncated || len(c.chunks) == 0 {
		return nil
	}
	return c.buffer()
}

func (c *limitedBufferCapture) toText() *string {
	if len(c.chunks) == 0 {
		return nil
	}
	text := string(c.buffer())
	return &text
}

// rollingBufferCapture mirrors RollingBufferCapture.
type rollingBufferCapture struct {
	chunks    [][]byte
	headIndex int
	size      int
	limit     int
}

func newRollingBufferCapture(limitBytes int) *rollingBufferCapture {
	return &rollingBufferCapture{limit: limitBytes}
}

func (c *rollingBufferCapture) push(buffer []byte) {
	if len(buffer) == 0 || c.limit <= 0 {
		return
	}
	if len(buffer) >= c.limit {
		c.chunks = [][]byte{append([]byte(nil), buffer[len(buffer)-c.limit:]...)}
		c.headIndex = 0
		c.size = c.limit
		return
	}
	c.chunks = append(c.chunks, append([]byte(nil), buffer...))
	c.size += len(buffer)
	c.trimOverflow()
}

func (c *rollingBufferCapture) toText() *string {
	if c.size == 0 {
		return nil
	}
	text := string(c.activeChunks())
	return &text
}

func (c *rollingBufferCapture) trimOverflow() {
	overflow := c.size - c.limit
	for overflow > 0 && c.headIndex < len(c.chunks) {
		first := c.chunks[c.headIndex]
		if len(first) <= overflow {
			c.headIndex++
			c.size -= len(first)
			overflow -= len(first)
		} else {
			c.chunks[c.headIndex] = append([]byte(nil), first[overflow:]...)
			c.size -= overflow
			overflow = 0
		}
	}
	c.compactConsumedChunks()
}

func (c *rollingBufferCapture) activeChunks() []byte {
	chunks := c.chunks
	if c.headIndex > 0 {
		chunks = chunks[c.headIndex:]
	}
	out := make([]byte, 0, c.size)
	for _, chunk := range chunks {
		out = append(out, chunk...)
	}
	return out
}

func (c *rollingBufferCapture) compactConsumedChunks() {
	if c.headIndex == 0 {
		return
	}
	if c.headIndex >= len(c.chunks) {
		c.chunks = nil
		c.headIndex = 0
		return
	}
	if c.headIndex > 64 && c.headIndex*2 > len(c.chunks) {
		c.chunks = append([][]byte(nil), c.chunks[c.headIndex:]...)
		c.headIndex = 0
	}
}

func buildNonStreamPipeResult(
	capture *limitedBufferCapture,
	usageTailCapture *rollingBufferCapture,
	firstByteSeen bool,
	firstByteMs int64,
	transferredBytes int,
) NonStreamPipeResult {
	capturedBody := capture.completeBuffer()
	var capturedBodyText *string
	if capturedBody != nil {
		text := string(capturedBody)
		capturedBodyText = &text
	} else {
		capturedBodyText = capture.toText()
	}
	captureTruncated := capture.truncated
	var diagnosticBodyText *string
	if capturedBodyText != nil {
		if captureTruncated {
			text := *capturedBodyText + "\n[truncated]"
			diagnosticBodyText = &text
		} else {
			diagnosticBodyText = capturedBodyText
		}
	}
	var firstByteMsPtr *int64
	if firstByteSeen {
		firstByteMsPtr = &firstByteMs
	}
	return NonStreamPipeResult{
		FirstByteMs:        firstByteMsPtr,
		CapturedBody:       capturedBody,
		CapturedBodyText:   capturedBodyText,
		DiagnosticBodyText: diagnosticBodyText,
		UsageTailText:      usageTailCapture.toText(),
		CaptureTruncated:   captureTruncated,
		TransferredBytes:   transferredBytes,
	}
}

func closeReader(reader io.Reader) error {
	if closer, ok := reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func errorMessageOf(err error, fallback string) string {
	if err != nil && err.Error() != "" {
		return err.Error()
	}
	return fallback
}

func bytesTextPtr(body []byte) *string {
	text := string(body)
	return &text
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func derefOrMax(value *int64) int64 {
	if value == nil {
		return int64(1) << 62
	}
	return *value
}

// derefMin mirrors `(a ?? +Inf) <= (b ?? +Inf)`.
func derefMin(a, b *int64) bool {
	const infinity = int64(1) << 62
	valueA := infinity
	if a != nil {
		valueA = *a
	}
	valueB := infinity
	if b != nil {
		valueB = *b
	}
	return valueA <= valueB
}

func formatSecondsText(ms int64) string {
	return strconv.FormatInt(int64CeilDiv(ms, 1000), 10) + "s"
}
