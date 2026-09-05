package main

// G20 phase-2 composition-root adapter: the gateway usage persistence bridge.
//
//   - gatewayusage.UsageRecorder: the durable write-delivery entry. The
//     production writer lives in the jobs module (Node usagewriter), which the
//     three-project baseline forbids this process from importing. The bridge
//     therefore delivers through an in-process asynchronous buffer (Node
//     record-queue.service.ts local-path semantics: enqueue failures are
//     counted and logged, delivery is non-blocking) with the file spool
//     (gatewayusage.UsageRecordSpool, usage-record-spool.ts) as the overflow /
//     compensation sink. TAKEOVER POINT: when the Go jobs module ships its
//     usagewriter input, deliver() below switches to that IPC/stream writer
//     and this file shrinks to the buffer configuration; the
//     FinalizationDispatch pipeline (gatewayusage/finalization.go) stays
//     unchanged.
//   - gatewaydispatch.AttemptAuditSink: the attempt-level audit surface
//     (audit/capture.service.ts startAttempt / completeAttempt /
//     recordFailedDispatchAttempt) delegated onto the gatewayusage
//     AuditCaptureContext the request capture created.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// usageBridgeConfig carries the bridge budgets (Node record-queue.service.ts
// defaults).
type usageBridgeConfig struct {
	// BufferCapacity mirrors the local record queue depth.
	BufferCapacity int
	// SpoolDirectory enables the file spool when non-empty.
	SpoolDirectory string
	// SpoolMaxFiles / SpoolMaxFileBytes mirror the spool config.
	SpoolMaxFiles     int
	SpoolMaxFileBytes int64
}

// slogLogger adapts *slog.Logger to the gatewayusage.Logger port.
type slogLogger struct{ inner *slog.Logger }

func (l slogLogger) Debug(msg string, fields map[string]any) {
	l.inner.Debug(msg, fieldsArgs(fields)...)
}
func (l slogLogger) Warn(msg string, fields map[string]any) { l.inner.Warn(msg, fieldsArgs(fields)...) }
func (l slogLogger) Error(msg string, fields map[string]any) {
	l.inner.Error(msg, fieldsArgs(fields)...)
}

func fieldsArgs(fields map[string]any) []any {
	args := make([]any, 0, len(fields)*2)
	for key, value := range fields {
		args = append(args, key, value)
	}
	return args
}

// spooledUsageRecorder implements gatewayusage.UsageRecorder: bounded async
// buffer → durable deliver with the spool as the synchronous overflow path.
type spooledUsageRecorder struct {
	config usageBridgeConfig
	spool  *gatewayusage.UsageRecordSpool

	mu       sync.Mutex
	buffered chan gatewayusage.UsageRecordInput
	dropped  int64
	failed   int64
	closed   bool
	wg       sync.WaitGroup
}

func newSpooledUsageRecorder(config usageBridgeConfig, spool *gatewayusage.UsageRecordSpool) *spooledUsageRecorder {
	capacity := config.BufferCapacity
	if capacity <= 0 {
		capacity = 4096
	}
	recorder := &spooledUsageRecorder{
		config:   config,
		spool:    spool,
		buffered: make(chan gatewayusage.UsageRecordInput, capacity),
	}
	recorder.wg.Add(1)
	go recorder.drain()
	return recorder
}

// EnqueueUsageRecord implements gatewayusage.UsageRecorder. Node never
// surfaces enqueue failures to callers on the local path: overflow falls to
// the spool and only persistent failure counts + logs.
func (r *spooledUsageRecorder) EnqueueUsageRecord(ctx gatewayusage.Ctx, input gatewayusage.UsageRecordInput) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return r.persistOverflow(ctx, input)
	}
	select {
	case r.buffered <- input:
		r.mu.Unlock()
		return nil
	default:
		r.mu.Unlock()
	}
	return r.persistOverflow(ctx, input)
}

// persistOverflow mirrors persistUsageRecordForQueueOverflow: the spool
// compensates when the buffer cannot admit the record.
func (r *spooledUsageRecorder) persistOverflow(ctx gatewayusage.Ctx, input gatewayusage.UsageRecordInput) error {
	if r.spool == nil {
		r.mu.Lock()
		r.dropped++
		r.mu.Unlock()
		return nil
	}
	return r.spool.Persist(ctx, input)
}

// drain is the delivery worker.
func (r *spooledUsageRecorder) drain() {
	defer r.wg.Done()
	for input := range r.buffered {
		r.deliver(input)
	}
}

// deliver hands one record to the durable writer. The default target is the
// spool (crash-safe, replayed by UsageRecordSpool.RunReplayOnce); the
// jobs-module usagewriter input takes over here per the registered takeover
// point above.
func (r *spooledUsageRecorder) deliver(input gatewayusage.UsageRecordInput) {
	if r.spool == nil {
		return
	}
	if err := r.spool.Persist(context.Background(), input); err != nil {
		r.mu.Lock()
		r.failed++
		r.mu.Unlock()
	}
}

// Close stops accepting records and drains the buffer.
func (r *spooledUsageRecorder) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	close(r.buffered)
	r.mu.Unlock()
	r.wg.Wait()
}

// chainAttemptAuditSink implements gatewaydispatch.AttemptAuditSink by
// delegating to the request's gatewayusage.AuditCaptureContext (the frozen
// capture carries the attempt state machine; the adapter only converts the
// input unions).
type chainAttemptAuditSink struct {
	capture *gatewayusage.AuditCaptureContext
}

func (s chainAttemptAuditSink) StartAttempt(input gatewaydispatch.StartAttemptInput) string {
	if s.capture == nil {
		return ""
	}
	return s.capture.StartAttempt(gatewayusage.StartAttemptInput{
		Account:      usageModelAccountOf(input.Account),
		AttemptIndex: input.AttemptIndex,
		UpstreamURL:  input.UpstreamURL,
		Method:       input.Method,
		Headers:      headersAnyOf(input.Headers),
		Body:         input.Body,
		HasBody:      len(input.Body) > 0,
		Model:        requestModelHintOf(input.RequestForModelAccounting),
	})
}

func (s chainAttemptAuditSink) CompleteAttempt(attemptID string, input gatewaydispatch.CompleteAttemptInput) {
	if s.capture == nil {
		return
	}
	s.capture.CompleteAttempt(attemptID, gatewayusage.CompleteAttemptInput{
		Success:      input.Success,
		ErrorPhase:   input.ErrorPhase,
		ErrorCode:    input.ErrorCode,
		ErrorMessage: input.ErrorMessage,
	})
}

func (s chainAttemptAuditSink) RecordFailedDispatchAttempt(input gatewaydispatch.FailedDispatchAttemptInput) {
	if s.capture == nil {
		return
	}
	s.capture.RecordFailedDispatchAttempt(gatewayusage.RecordFailedDispatchAttemptInput{
		Account:      usageModelAccountOf(input.Account),
		AttemptIndex: input.AttemptIndex,
		UpstreamURL:  input.UpstreamURL,
		Method:       input.Method,
		StartedAtMs:  input.StartedAtMs,
		ErrorPhase:   input.ErrorPhase,
		ErrorCode:    input.ErrorCode,
		ErrorMessage: input.ErrorMessage,
		Model:        requestModelHintOf(input.RequestForModelAccounting),
	})
}

// chainFinalizationUsage implements gatewayresponse.UsageAttemptRecorder —
// the finalization-side attempt recorder (usage/records.ts
// recordCompletedUpstreamAttempt). Completed attempts enqueue one durable
// usage record through the spooled bridge; failed attempts mirror the
// engine-side failure record.
type chainFinalizationUsage struct {
	recorder gatewayusage.UsageRecorder
}

func (u chainFinalizationUsage) RecordCompletedUpstreamAttempt(input gatewayresponse.CompletedAttemptInput) {
	if u.recorder == nil {
		return
	}
	record := gatewayusage.UsageRecordInput{
		TraceID:         input.UsageContext.TraceID,
		TrafficSource:   gatewayusage.OpenAIGatewayTrafficSource(input.UsageContext.TrafficSource),
		ClientIP:        input.UsageContext.ClientIP,
		SystemAccountID: input.UsageContext.SystemAccountID,
		APIKeyID:        input.UsageContext.APIKeyID,
		GroupID:         input.UsageContext.GroupID,
		Endpoint:        input.UsageContext.Endpoint,
		ProviderCode:    input.UsageContext.ProviderCode,
		UsageSemantic:   "gateway_request",
		Success:         input.Success,
		ErrorCode:       input.ErrorCode,
		ErrorMessage:    input.ErrorMessage,
		CreatedAt:       time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
	if input.Account != nil && input.Account.GetID() != "" {
		record.AccountID = input.Account.GetID()
	}
	stream := input.Stream
	record.Stream = &stream
	statusCode := input.StatusCode
	record.StatusCode = &statusCode
	if input.FirstTokenMs != nil {
		firstTokenMs := int(*input.FirstTokenMs)
		record.FirstTokenMs = &firstTokenMs
	}
	if input.CompletedAtMs != nil {
		durationMs := int(*input.CompletedAtMs - input.StartedAtMs)
		record.DurationMs = &durationMs
	}
	_ = u.recorder.EnqueueUsageRecord(context.Background(), record)
}

func (u chainFinalizationUsage) RecordFailedUpstreamAttempt(input gatewayresponse.FailedAttemptInput) {
	if u.recorder == nil {
		return
	}
	statusCode := 0
	if input.StatusCode != nil {
		statusCode = *input.StatusCode
	}
	record := gatewayusage.UsageRecordInput{
		TrafficSource:      "gateway",
		UsageSemantic:      "gateway_request",
		Success:            false,
		ErrorCode:          "upstream_retryable_error",
		ErrorMessage:       input.ErrorMessage,
		FailureAttribution: input.FailureAttribution,
		CreatedAt:          time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
	record.StatusCode = &statusCode
	println("DEBUG enqueue failed attempt, recorder nil?", u.recorder == nil)
	_ = u.recorder.EnqueueUsageRecord(context.Background(), record)
}

// usageModelAccountOf projects the dispatch candidate into the usage account
// view (identity + usage scope + protocol profile).
func usageModelAccountOf(account gatewaydispatch.AccountCandidate) gatewayusage.UsageModelAccount {
	out := gatewayusage.UsageModelAccount{
		ID:                        account.ID,
		Name:                      account.Name,
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		UsageAccess: gatewayusage.UsageAccessFields{
			AccountOwnerSystemAccountID:      account.AccountOwnerSystemAccountID,
			GroupOwnerSystemAccountID:        account.GroupOwnerSystemAccountID,
			AccountAccessType:                account.AccountAccessType,
			GroupAccessType:                  account.GroupAccessType,
			AccountAuthorizationID:           deref(account.AccountAuthorizationID),
			AccountAuthorizationSourceType:   deref(account.AccountAuthorizationSourceType),
			AccountAuthorizationSourceTeamID: deref(account.AccountAuthorizationSourceTeamID),
			GroupAuthorizationID:             deref(account.GroupAuthorizationID),
			GroupAuthorizationSourceType:     deref(account.GroupAuthorizationSourceType),
			GroupAuthorizationSourceTeamID:   deref(account.GroupAuthorizationSourceTeamID),
		},
		Profile: &gatewayusage.ProviderProtocolProfile{
			ProviderCode:    account.ProviderCode,
			ProtocolCode:    account.ProtocolCode,
			ProtocolVersion: account.ProtocolVersion,
			ProfileID:       account.ProviderProtocolProfileID,
		},
	}
	if account.ProxyURL != nil {
		out.ProxyURL = *account.ProxyURL
	}
	return out
}

func headersAnyOf(headers map[string]string) map[string]any {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]any, len(headers))
	for key, value := range headers {
		out[key] = value
	}
	return out
}

func requestModelHintOf(req *gatewaypreauth.GatewayRequest) string {
	if req == nil {
		return ""
	}
	if model, ok := gatewaypreauth.RequestModel(req); ok {
		return model
	}
	return ""
}
