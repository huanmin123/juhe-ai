package gatewayupstream

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
	result, _, err := PipeNonStreamUpstreamResponseCommon(ctx, upstreamBody, downstream, input, false, nil)
	return result.Result, err
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
	result, _, err := PipeNonStreamUpstreamResponseCommon(ctx, upstreamBody, downstream, input.NonStreamPipeInput, false, &InspectPlan{
		InspectBytes:           input.InspectBytes,
		RequireFullyBuffered:   input.RequireFullyBuffered,
		BeforeDownstreamCommit: input.BeforeDownstreamCommit,
	})
	if err != nil {
		return InspectableNonStreamPipeResult{}, err
	}
	return InspectableNonStreamPipeResult{
		NonStreamPipeResult:     result.Result,
		FullyBuffered:           result.FullyBuffered,
		InspectionLimitExceeded: result.InspectionLimitExceeded,
		CompleteBody:            result.completeBody,
		CompleteBodyText:        result.completeBodyText,
	}, nil
}

// InspectPlan carries the inspection variant configuration.
type InspectPlan struct {
	InspectBytes           int
	RequireFullyBuffered   bool
	BeforeDownstreamCommit func([]byte) error
}

// InspectOutcome extends the pipe result with inspection fields.
type InspectOutcome struct {
	Result                  NonStreamPipeResult
	FullyBuffered           bool
	InspectionLimitExceeded bool
	completeBody            []byte
	completeBodyText        *string
}

func PipeNonStreamUpstreamResponseCommon(
	ctx context.Context,
	upstreamBody io.Reader,
	downstream DownstreamWriter,
	input NonStreamPipeInput,
	_ bool,
	inspect *InspectPlan,
) (InspectOutcome, bool, error) {
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
	capture := NewLimitedBufferCapture(captureLimit)
	usageTailLimit := NonStreamUsageTailCaptureBytes
	if input.UsageTailBytes != nil {
		usageTailLimit = int(*input.UsageTailBytes)
	}
	usageTailCapture := NewRollingBufferCapture(usageTailLimit)
	maxLifetimeDeadlineAt := NonStreamBodyMaxLifetimeDeadlineAt(input.StartedAt, input.MaxLifetimeMs)
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
		_ = CloseReader(upstreamBody)
		if IsUpstreamRequestAbortedError(err) || signal.Err() != nil {
			return &UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}
		}
		return &NonStreamUpstreamBodyPipeError{
			Message:       ErrorMessageOf(err, "上游非流式响应正文中断"),
			PartialResult: BuildNonStreamPipeResult(capture, usageTailCapture, firstByteSeen, firstByteMs, transferredBytes),
			OriginalError: err,
		}
	}

	for {
		if signal.Err() != nil {
			return InspectOutcome{}, downstreamWriting, &UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}
		}

		read, deadlineObserved, readErr := ReadFirstNonStreamChunk(input, upstreamBody, buffer, &firstByteSeen, firstByteDeadlineObserved, maxLifetimeDeadlineAt, inspect == nil)
		firstByteDeadlineObserved = deadlineObserved
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return InspectOutcome{}, downstreamWriting, failWhileWriting(readErr)
		}
		if read.Done {
			break
		}
		chunk := buffer[:read.N]
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
		transferredBytes += read.N
		captured := append([]byte(nil), chunk...)
		capture.Push(captured)
		usageTailCapture.Push(captured)
		if input.OnChunkRead != nil {
			input.OnChunkRead(captured)
		}

		if inspect != nil {
			// Inspection buffering branch (Node
			// pipeNonStreamUpstreamResponseForInspection).
			if !downstreamWriting && inspectionBytes+len(captured) <= inspect.InspectBytes {
				inspectionChunks = append(inspectionChunks, captured)
				inspectionBytes += len(captured)
				continue
			}
			if !downstreamWriting {
				var inspectionBody []byte
				if inspectionBytes+len(captured) <= inspect.InspectBytes {
					inspectionBody = make([]byte, 0, inspectionBytes+len(captured))
					for _, item := range inspectionChunks {
						inspectionBody = append(inspectionBody, item...)
					}
					inspectionBody = append(inspectionBody, captured...)
				} else {
					inspectionBody = make([]byte, 0, inspect.InspectBytes)
					for _, item := range inspectionChunks {
						inspectionBody = append(inspectionBody, item...)
					}
					remaining := inspect.InspectBytes - inspectionBytes
					if remaining > 0 {
						inspectionBody = append(inspectionBody, captured[:remaining]...)
					}
				}
				if inspect.RequireFullyBuffered {
					_ = CloseReader(upstreamBody)
					outcome := InspectOutcome{
						Result:                  BuildNonStreamPipeResult(capture, usageTailCapture, firstByteSeen, firstByteMs, transferredBytes),
						FullyBuffered:           false,
						InspectionLimitExceeded: true,
						completeBody:            inspectionBody,
						completeBodyText:        BytesTextPtr(inspectionBody),
					}
					return outcome, downstreamWriting, nil
				}
				if inspect.BeforeDownstreamCommit != nil {
					if err := inspect.BeforeDownstreamCommit(inspectionBody); err != nil {
						_ = CloseReader(upstreamBody)
						return InspectOutcome{}, downstreamWriting, err
					}
				}
				downstreamWriting = true
				if err := writeBufferedInspectionChunks(); err != nil {
					return InspectOutcome{}, downstreamWriting, failWhileWriting(err)
				}
			}
			prepareDownstreamForWrite()
			if _, err := downstream.Write(captured); err != nil {
				return InspectOutcome{}, downstreamWriting, failWhileWriting(err)
			}
			if input.OnChunkWritten != nil {
				input.OnChunkWritten(len(captured))
			}
			continue
		}

		if _, err := downstream.Write(captured); err != nil {
			return InspectOutcome{}, downstreamWriting, failWhileWriting(err)
		}
		if input.OnChunkWritten != nil {
			input.OnChunkWritten(read.N)
		}
	}

	_ = CloseReader(upstreamBody)
	if inspect != nil && downstreamWriting {
		if !downstreamPrepared {
			prepareDownstreamForWrite()
		}
		return InspectOutcome{
			Result:        BuildNonStreamPipeResult(capture, usageTailCapture, firstByteSeen, firstByteMs, transferredBytes),
			FullyBuffered: false,
		}, downstreamWriting, nil
	}
	if inspect != nil {
		var completeBody []byte
		for _, chunk := range inspectionChunks {
			completeBody = append(completeBody, chunk...)
		}
		outcome := InspectOutcome{
			Result:           BuildNonStreamPipeResult(capture, usageTailCapture, firstByteSeen, firstByteMs, transferredBytes),
			FullyBuffered:    true,
			completeBody:     completeBody,
			completeBodyText: BytesTextPtr(completeBody),
		}
		return outcome, downstreamWriting, nil
	}

	if !downstreamPrepared {
		prepareDownstreamForWrite()
	}
	if input.OnBodyCompleted != nil {
		input.OnBodyCompleted(transferredBytes)
	}
	return InspectOutcome{
		Result: BuildNonStreamPipeResult(capture, usageTailCapture, firstByteSeen, firstByteMs, transferredBytes),
	}, downstreamWriting, nil
}

// ChunkRead is one consumed buffer chunk.
type ChunkRead struct {
	N    int
	Done bool
}

// ReadFirstNonStreamChunk dispatches between the first-chunk deadline reader
// and the absolute-deadline reader for later Chunks. pendingReadSupersedesDeadline
// mirrors the Node split (body.ts:169 vs :305): the plain forward pipe keeps a
// parallel raw read (true), while the inspection pipe — where the speed-first
// cutover decision lives — requires semantic output to supersede an abort
// (false); raw first bytes alone still throw the configured-deadline timeout
// so the same-request cutover fires.
func ReadFirstNonStreamChunk(
	input NonStreamPipeInput,
	reader io.Reader,
	buffer []byte,
	firstByteSeen *bool,
	firstByteDeadlineObserved bool,
	maxLifetimeDeadlineAt *int64,
	pendingReadSupersedesDeadline bool,
) (ChunkRead, bool, error) {
	if !*firstByteSeen {
		return ReadFirstNonStreamChunkWithDeadlines(reader, buffer, input.StartedAt, FirstByteDeadlineReadInput{
			StartedAt:                     input.StartedAt,
			Signal:                        input.Signal,
			FirstByteTimeoutMs:            input.FirstByteTimeoutMs,
			FirstByteDeadlineMs:           input.FirstByteDeadlineMs,
			FirstByteDeadlineObserved:     firstByteDeadlineObserved,
			OnFirstByteDeadline:           input.OnFirstByteDeadline,
			OnFirstByteDeadlineSuperseded: input.OnFirstByteDeadlineSuperseded,
			ResponsePrecommitDeadlineAtMs: input.ResponsePrecommitDeadlineAtMs,
			PendingReadSupersedesDeadline: pendingReadSupersedesDeadline,
			MaxLifetimeDeadlineAt:         maxLifetimeDeadlineAt,
			MaxLifetimeMs:                 input.MaxLifetimeMs,
		})
	}
	n, err, _ := ReadNonStreamChunkWithAbsoluteDeadline(reader, buffer, input.Signal, maxLifetimeDeadlineAt, input.MaxLifetimeMs, input.ResponsePrecommitDeadlineAtMs)
	if err == io.EOF {
		return ChunkRead{Done: true}, firstByteDeadlineObserved, nil
	}
	return ChunkRead{N: n}, firstByteDeadlineObserved, err
}

// FirstByteDeadlineReadInput groups the deadline inputs of the first chunk.
type FirstByteDeadlineReadInput struct {
	StartedAt                     int64
	Signal                        context.Context
	FirstByteTimeoutMs            *int64
	FirstByteDeadlineMs           *int64
	FirstByteDeadlineObserved     bool
	OnFirstByteDeadline           FirstByteDeadlineHandler
	OnFirstByteDeadlineSuperseded func()
	ResponsePrecommitDeadlineAtMs *int64
	PendingReadSupersedesDeadline bool
	MaxLifetimeDeadlineAt         *int64
	MaxLifetimeMs                 *int64
}

// ReadFirstNonStreamChunkWithDeadlines mirrors
// ReadFirstNonStreamChunkWithDeadlines.
func ReadFirstNonStreamChunkWithDeadlines(
	reader io.Reader,
	buffer []byte,
	startedAt int64,
	input FirstByteDeadlineReadInput,
) (ChunkRead, bool, error) {
	if input.FirstByteTimeoutMs == nil && input.FirstByteDeadlineMs == nil &&
		input.ResponsePrecommitDeadlineAtMs == nil && input.MaxLifetimeDeadlineAt == nil {
		n, err := ReadStreamChunkWithAbort(input.Signal, reader, buffer)
		if err == io.EOF {
			return ChunkRead{Done: true}, input.FirstByteDeadlineObserved, nil
		}
		return ChunkRead{N: n}, input.FirstByteDeadlineObserved, err
	}

	pendingRead := ObserveFirstBytePendingRead(func() (ChunkResult, error) {
		n, err := reader.Read(buffer)
		return ChunkResult{N: n, Err: err}, err
	})
	var hardDeadlineAt *int64
	if input.FirstByteTimeoutMs != nil {
		at := NowMs() + *input.FirstByteTimeoutMs
		hardDeadlineAt = &at
	}
	var softDeadlineAt *int64
	if input.FirstByteDeadlineMs != nil && !input.FirstByteDeadlineObserved {
		at := startedAt + *input.FirstByteDeadlineMs
		softDeadlineAt = &at
	}
	observed := input.FirstByteDeadlineObserved

	fail := func(err error) (ChunkRead, bool, error) {
		return ChunkRead{}, observed, err
	}

	for {
		now := NowMs()
		var maxLifetimeRemainingMs *int64
		if input.MaxLifetimeDeadlineAt != nil {
			remaining := *input.MaxLifetimeDeadlineAt - now
			maxLifetimeRemainingMs = &remaining
		}
		var responsePrecommitRemainingMs *int64
		if input.ResponsePrecommitDeadlineAtMs != nil {
			remaining := *input.ResponsePrecommitDeadlineAtMs - now
			responsePrecommitRemainingMs = &remaining
		}
		if responsePrecommitRemainingMs != nil && *responsePrecommitRemainingMs <= 0 &&
			(maxLifetimeRemainingMs == nil || DerefMin(input.ResponsePrecommitDeadlineAtMs, input.MaxLifetimeDeadlineAt)) {
			return fail(&GatewayResponsePrecommitDeadlineError{DeadlineAtMs: *input.ResponsePrecommitDeadlineAtMs})
		}
		if maxLifetimeRemainingMs != nil && *maxLifetimeRemainingMs <= 0 {
			return fail(&UpstreamBodyReadMaxLifetimeError{TimeoutMs: DerefInt64(input.MaxLifetimeMs)})
		}
		var softRemainingMs *int64
		if softDeadlineAt != nil && !observed {
			remaining := *softDeadlineAt - now
			softRemainingMs = &remaining
		}
		if softRemainingMs != nil && *softRemainingMs <= 0 {
			observed = true
			decision := decideAfterPendingRead(pendingRead, input)
			if decision.Precommit {
				return fail(decision.Err)
			}
			if decision.HasRead {
				return FirstNonStreamReadAfterDeadlineDecision(decision, observed, input)
			}
			if decision.Action == FirstByteDeadlineActionAbort {
				return fail(&GatewayFirstByteTimeoutError{
					Message:   "上游非流式响应 " + FormatSecondsText(DerefInt64(input.FirstByteDeadlineMs)) + " 后仍未返回首个字节",
					TimeoutMs: DerefInt64(input.FirstByteDeadlineMs),
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
				Message:   "上游非流式响应 " + FormatSecondsText(DerefInt64(input.FirstByteTimeoutMs)) + " 后仍未返回首个字节",
				TimeoutMs: DerefInt64(input.FirstByteTimeoutMs),
			})
		}

		raceType, result, _ := RaceReadWithDeadlines(pendingRead, input.Signal, softRemainingMs, hardRemainingMs, maxLifetimeRemainingMs, responsePrecommitRemainingMs)
		switch raceType {
		case RaceReadDone:
			if input.ResponsePrecommitDeadlineAtMs != nil {
				settledAt, _ := pendingRead.SettledAtMs()
				if settledAt > *input.ResponsePrecommitDeadlineAtMs {
					return fail(&GatewayResponsePrecommitDeadlineError{DeadlineAtMs: *input.ResponsePrecommitDeadlineAtMs})
				}
			}
			if result.Err == io.EOF {
				return ChunkRead{Done: true}, observed, nil
			}
			return ChunkRead{N: result.N}, observed, result.Err
		case RaceAbort:
			return fail(&UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true})
		case RaceHardTimeout:
			return fail(&GatewayFirstByteTimeoutError{
				Message:   "上游非流式响应 " + FormatSecondsText(DerefInt64(input.FirstByteTimeoutMs)) + " 后仍未返回首个字节",
				TimeoutMs: DerefInt64(input.FirstByteTimeoutMs),
			})
		case RaceMaxLifetimeTimeout:
			return fail(&UpstreamBodyReadMaxLifetimeError{TimeoutMs: DerefInt64(input.MaxLifetimeMs)})
		case RaceResponsePrecommitTimeout:
			return fail(&GatewayResponsePrecommitDeadlineError{DeadlineAtMs: DerefInt64(input.ResponsePrecommitDeadlineAtMs)})
		}

		// soft_timeout → routing decision (Node repeats the loop on 'continue').
		observed = true
		decision := decideAfterPendingRead(pendingRead, input)
		if decision.Precommit {
			return fail(decision.Err)
		}
		if decision.HasRead {
			return FirstNonStreamReadAfterDeadlineDecision(decision, observed, input)
		}
		if decision.Action == FirstByteDeadlineActionAbort {
			return fail(&GatewayFirstByteTimeoutError{
				Message:   "上游非流式响应 " + FormatSecondsText(DerefInt64(input.FirstByteDeadlineMs)) + " 后仍未返回首个字节",
				TimeoutMs: DerefInt64(input.FirstByteDeadlineMs),
				Source:    FirstByteTimeoutSourceConfiguredDeadline,
			})
		}
	}
}

// DeadlineDecision flattens FirstByteDeadlineDecisionResult for the reader.
type DeadlineDecision struct {
	Precommit   bool
	HasRead     bool
	Read        ChunkResult
	Action      FirstByteDeadlineAction
	DecisionErr error
	Err         error
}

func decideAfterPendingRead(pendingRead *ObservedFirstBytePendingRead[ChunkResult], input FirstByteDeadlineReadInput) DeadlineDecision {
	decision := DecideFirstByteDeadlineAfterPendingRead(pendingRead, input.OnFirstByteDeadline, FirstByteDeadlineDecisionInput{
		ElapsedMs: NowMs() - input.StartedAt,
		TimeoutMs: DerefInt64(input.FirstByteDeadlineMs),
		Transport: "non_stream",
	}, FirstByteDeadlineDecisionWaitOptions{
		ResponsePrecommitDeadlineAtMs: input.ResponsePrecommitDeadlineAtMs,
		OnResponsePrecommitDeadline:   input.OnFirstByteDeadlineSuperseded,
	})
	switch decision.Type {
	case DeadlineDecisionResponsePrecommit:
		return DeadlineDecision{Precommit: true, Err: decision.Error}
	case DeadlineDecisionRead:
		return DeadlineDecision{HasRead: true, Read: decision.Result, Action: decision.Action, DecisionErr: decision.DecisionError, Err: decision.Error}
	default:
		return DeadlineDecision{Action: decision.Action}
	}
}

// firstNonStreamReadAfterDeadlineDecision mirrors
// firstNonStreamReadAfterDeadlineDecision.
func FirstNonStreamReadAfterDeadlineDecision(
	decision DeadlineDecision,
	firstByteDeadlineObserved bool,
	input FirstByteDeadlineReadInput,
) (ChunkRead, bool, error) {
	if input.PendingReadSupersedesDeadline {
		if input.OnFirstByteDeadlineSuperseded != nil {
			input.OnFirstByteDeadlineSuperseded()
		}
		if decision.Read.Err == io.EOF {
			return ChunkRead{Done: true}, firstByteDeadlineObserved, nil
		}
		return ChunkRead{N: decision.Read.N}, firstByteDeadlineObserved, decision.Read.Err
	}
	if decision.DecisionErr != nil {
		return ChunkRead{}, firstByteDeadlineObserved, decision.DecisionErr
	}
	if decision.Action == FirstByteDeadlineActionAbort {
		return ChunkRead{}, firstByteDeadlineObserved, &GatewayFirstByteTimeoutError{
			Message:   "上游非流式响应 " + FormatSecondsText(DerefInt64(input.FirstByteDeadlineMs)) + " 后仍未返回完整语义响应",
			TimeoutMs: DerefInt64(input.FirstByteDeadlineMs),
			Source:    FirstByteTimeoutSourceConfiguredDeadline,
		}
	}
	if decision.Read.Err == io.EOF {
		return ChunkRead{Done: true}, firstByteDeadlineObserved, nil
	}
	return ChunkRead{N: decision.Read.N}, firstByteDeadlineObserved, decision.Read.Err
}

// ChunkResult carries a raw buffered read through the generic observer.
type ChunkResult struct {
	N   int
	Err error
}

// race outcome tags mirror raceReadWithDeadlines.
type RaceReadType int

const (
	RaceReadDone RaceReadType = iota
	RaceSoftTimeout
	RaceHardTimeout
	RaceMaxLifetimeTimeout
	RaceResponsePrecommitTimeout
	RaceAbort
)

// raceReadWithDeadlines mirrors raceReadWithDeadlines. The shared request
// precommit deadline is a hard wall: when it coincides with the configurable
// speed-first deadline, the wall-clock attribution wins
// (responsePrecommitTimeoutMs <= softTimeoutMs → response_precommit_timeout).
func RaceReadWithDeadlines(
	pendingRead *ObservedFirstBytePendingRead[ChunkResult],
	signal context.Context,
	softTimeoutMs *int64,
	hardTimeoutMs *int64,
	maxLifetimeTimeoutMs *int64,
	responsePrecommitTimeoutMs *int64,
) (RaceReadType, ChunkResult, error) {
	type raceEvent struct {
		tag    RaceReadType
		result ChunkResult
		err    error
	}
	events := make(chan raceEvent, 5)
	if softTimeoutMs != nil {
		after := time.After(time.Duration(MaxInt64(1, *softTimeoutMs)) * time.Millisecond)
		go func() { <-after; events <- raceEvent{tag: RaceSoftTimeout} }()
	}
	if hardTimeoutMs != nil {
		after := time.After(time.Duration(MaxInt64(1, *hardTimeoutMs)) * time.Millisecond)
		go func() { <-after; events <- raceEvent{tag: RaceHardTimeout} }()
	}
	if responsePrecommitTimeoutMs != nil {
		after := time.After(time.Duration(MaxInt64(1, *responsePrecommitTimeoutMs)) * time.Millisecond)
		go func() { <-after; events <- raceEvent{tag: RaceResponsePrecommitTimeout} }()
	}
	if maxLifetimeTimeoutMs != nil {
		after := time.After(time.Duration(MaxInt64(1, *maxLifetimeTimeoutMs)) * time.Millisecond)
		go func() { <-after; events <- raceEvent{tag: RaceMaxLifetimeTimeout} }()
	}
	if signal != nil {
		go func() {
			<-signal.Done()
			events <- raceEvent{tag: RaceAbort}
		}()
	}

	readDone := make(chan raceEvent, 1)
	go func() {
		result, err := pendingRead.Await()
		readDone <- raceEvent{result: result, err: err}
	}()

	select {
	case event := <-readDone:
		return RaceReadDone, event.result, event.err
	case event := <-events:
		switch event.tag {
		case RaceSoftTimeout:
			if responsePrecommitTimeoutMs != nil && *responsePrecommitTimeoutMs <= DerefOrMax(softTimeoutMs) {
				return RaceResponsePrecommitTimeout, ChunkResult{}, nil
			}
			return RaceSoftTimeout, ChunkResult{}, nil
		default:
			return event.tag, ChunkResult{}, nil
		}
	}
}

func ReadNonStreamChunkWithAbsoluteDeadline(
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
		(maxLifetimeRemainingMs == nil || DerefMin(responsePrecommitDeadlineAtMs, maxLifetimeDeadlineAt)) {
		return 0, &GatewayResponsePrecommitDeadlineError{DeadlineAtMs: *responsePrecommitDeadlineAtMs}, false
	}
	if maxLifetimeRemainingMs != nil && *maxLifetimeRemainingMs <= 0 {
		return 0, &UpstreamBodyReadMaxLifetimeError{TimeoutMs: DerefInt64(maxLifetimeMs)}, false
	}
	pendingRead := ObserveFirstBytePendingRead(func() (ChunkResult, error) {
		n, err := reader.Read(buffer)
		return ChunkResult{N: n, Err: err}, err
	})
	raceType, result, _ := RaceReadWithDeadlines(pendingRead, signal, nil, nil, maxLifetimeRemainingMs, responsePrecommitRemainingMs)
	switch raceType {
	case RaceReadDone:
		if responsePrecommitDeadlineAtMs != nil {
			settledAt, _ := pendingRead.SettledAtMs()
			if settledAt > *responsePrecommitDeadlineAtMs {
				return 0, &GatewayResponsePrecommitDeadlineError{DeadlineAtMs: *responsePrecommitDeadlineAtMs}, false
			}
		}
		return result.N, result.Err, false
	case RaceAbort:
		return 0, &UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}, false
	case RaceMaxLifetimeTimeout:
		return 0, &UpstreamBodyReadMaxLifetimeError{TimeoutMs: DerefInt64(maxLifetimeMs)}, false
	case RaceResponsePrecommitTimeout:
		return 0, &GatewayResponsePrecommitDeadlineError{DeadlineAtMs: DerefInt64(responsePrecommitDeadlineAtMs)}, false
	}
	return 0, &UpstreamBodyReadMaxLifetimeError{TimeoutMs: DerefInt64(maxLifetimeMs)}, false
}

func NonStreamBodyMaxLifetimeDeadlineAt(startedAt int64, maxLifetimeMs *int64) *int64 {
	if maxLifetimeMs == nil || *maxLifetimeMs <= 0 {
		return nil
	}
	at := startedAt + MaxInt64(1, *maxLifetimeMs)
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

func BuildNonStreamPipeResult(
	capture *LimitedBufferCapture,
	usageTailCapture *RollingBufferCapture,
	firstByteSeen bool,
	firstByteMs int64,
	transferredBytes int,
) NonStreamPipeResult {
	capturedBody := capture.CompleteBuffer()
	var capturedBodyText *string
	if capturedBody != nil {
		text := string(capturedBody)
		capturedBodyText = &text
	} else {
		capturedBodyText = capture.ToText()
	}
	captureTruncated := capture.Truncated
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
		UsageTailText:      usageTailCapture.ToText(),
		CaptureTruncated:   captureTruncated,
		TransferredBytes:   transferredBytes,
	}
}

func CloseReader(reader io.Reader) error {
	if closer, ok := reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func ErrorMessageOf(err error, fallback string) string {
	if err != nil && err.Error() != "" {
		return err.Error()
	}
	return fallback
}

func BytesTextPtr(body []byte) *string {
	text := string(body)
	return &text
}

func DerefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func DerefOrMax(value *int64) int64 {
	if value == nil {
		return int64(1) << 62
	}
	return *value
}

// DerefMin mirrors `(a ?? +Inf) <= (b ?? +Inf)`.
func DerefMin(a, b *int64) bool {
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

func FormatSecondsText(ms int64) string {
	return strconv.FormatInt(Int64CeilDiv(ms, 1000), 10) + "s"
}
