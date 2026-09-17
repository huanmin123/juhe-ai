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
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
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
	// Logger 接收投递告警（nil 时回落 slog.Default()）。
	Logger *slog.Logger
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
	logger *slog.Logger
	// clock / idFactory 驱动 enqueue 入口的记录归一化（稳定 id/createdAt）。
	clock     gatewayusage.Clock
	idFactory gatewayusage.UsageRecordIDFactory

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
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	recorder := &spooledUsageRecorder{
		config:    config,
		spool:     spool,
		logger:    logger,
		clock:     gatewayusage.SystemClock{},
		idFactory: usageShardRecordIDFactory{now: time.Now},
		buffered:  make(chan gatewayusage.UsageRecordInput, capacity),
	}
	recorder.wg.Add(1)
	go recorder.drain()
	return recorder
}

// EnqueueUsageRecord implements gatewayusage.UsageRecorder. Node never
// surfaces enqueue failures to callers on the local path: overflow falls to
// the spool and only persistent failure counts + logs.
//
// 入口第一步先做记录归一化（Node record-queue.service.ts enqueueUsageRecord
// 语义）：补齐稳定 id 与 RFC3339 createdAt 并收紧快照。spool 落盘 JSON 因此
// 带稳定 id/createdAt，jobs usagespooldrain 的 parseSpoolRecord 契约（缺
// id/createdAt 判损坏隔离）在无 Redis 的 spool 交接路径上成立。
func (r *spooledUsageRecorder) EnqueueUsageRecord(ctx gatewayusage.Ctx, input gatewayusage.UsageRecordInput) error {
	normalized, err := gatewayusage.NormalizeUsageRecordInput(input, r.clock, r.idFactory)
	if err != nil {
		r.mu.Lock()
		r.failed++
		failed := r.failed
		r.mu.Unlock()
		if failed <= 10 || failed%100 == 0 {
			r.logger.Warn("usage 记录归一化失败，无法持久投递，已丢弃",
				"event", "usage_record_normalize_failed",
				"traceId", input.TraceID,
				"trafficSource", input.TrafficSource,
				"normalizeFailureCount", failed,
				"error", err.Error())
		}
		return nil
	}
	input = normalized
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
// spool (crash-safe, replayed by the jobs usage spool drain); the jobs-module
// usagewriter input takes over here per the registered takeover point above.
//
// BUG-0175 D-72 告警面：spool 未装配或写入失败意味着该记录没有到达任何
// 持久投递面（jobs drain 无从消费），除计数外按采样告警（前 10 条逐条、
// 之后每 100 条一条，对齐 Node droppedLogSampleLimit 采样），不允许静默丢弃。
func (r *spooledUsageRecorder) deliver(input gatewayusage.UsageRecordInput) {
	if r.spool == nil {
		r.mu.Lock()
		r.dropped++
		dropped := r.dropped
		r.mu.Unlock()
		if dropped <= 10 || dropped%100 == 0 {
			r.logger.Warn("usage spool 未装配，用量记录无法持久投递，已丢弃",
				"event", "usage_record_spool_unavailable",
				"usageRecordId", input.ID,
				"traceId", input.TraceID,
				"trafficSource", input.TrafficSource,
				"droppedCount", dropped)
		}
		return
	}
	if err := r.spool.Persist(context.Background(), input); err != nil {
		r.mu.Lock()
		r.failed++
		failed := r.failed
		r.mu.Unlock()
		if failed <= 10 || failed%100 == 0 {
			r.logger.Error("usage spool 写入失败，用量记录未持久化",
				"event", "usage_record_spool_persist_failed",
				"usageRecordId", input.ID,
				"traceId", input.TraceID,
				"trafficSource", input.TrafficSource,
				"persistFailureCount", failed,
				"error", err.Error())
		}
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
	converted := gatewayusage.CompleteAttemptInput{
		Success:      input.Success,
		ErrorPhase:   input.ErrorPhase,
		ErrorCode:    input.ErrorCode,
		ErrorMessage: input.ErrorMessage,
	}
	// D-123（BUG-0175）：失败尝试审计透传上游响应事实（statusCode /
	// responseHeaders / responseBody，failure-dispatch.ts:276-283）。
	if input.StatusCode != nil {
		status := *input.StatusCode
		converted.StatusCode = &status
	}
	if input.ResponseHeaders != nil {
		headers := map[string]any{}
		for name, values := range input.ResponseHeaders {
			if len(values) > 0 {
				headers[name] = values[0]
			}
		}
		converted.ResponseHeaders = headers
	}
	if len(input.ResponseBody) > 0 {
		converted.ResponseBody = input.ResponseBody
		converted.HasResponseBody = true
	}
	s.capture.CompleteAttempt(attemptID, converted)
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
		// Node normalizeUsageRecordInput 的 scope 完整性规则：groupId/accountId
		// 只有伴随 owner/accessType 授权五元组齐备才保留，否则整组清空。
		// Group scope 在 usageContext 上，Account scope 在账户视图上。
		GroupOwnerSystemAccountID:      input.UsageContext.GroupOwnerSystemAccountID,
		GroupAccessType:                input.UsageContext.GroupAccessType,
		GroupAuthorizationID:           input.UsageContext.GroupAuthorizationID,
		GroupAuthorizationSourceType:   input.UsageContext.GroupAuthorizationSourceType,
		GroupAuthorizationSourceTeamID: input.UsageContext.GroupAuthorizationSourceTeamID,
		Model:                          input.RequestedModel,
	}
	applyUsageAccountScope(&record, input.Account)
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

// applyUsageAccountScope 把账户视图携带的 usage scope 投影到记录（对齐
// usageModelAccountOf 的 UsageAccessFields 投影面；非 OpenAIAccountView 的
// 测试实现保持零值，归一化按缺失 scope 清空 accountId，与 Node 行为一致）。
func applyUsageAccountScope(record *gatewayusage.UsageRecordInput, account gatewayresponse.AccountView) {
	if account == nil {
		return
	}
	record.AccountID = account.GetID()
	view, ok := account.(gatewayresponse.OpenAIAccountView)
	if !ok {
		return
	}
	secret := view.Account
	record.AccountOwnerSystemAccountID = secret.AccountOwnerSystemAccountID
	record.AccountAccessType = secret.AccountAccessType
	record.AccountAuthorizationID = derefString(secret.AccountAuthorizationID)
	record.AccountAuthorizationSourceType = derefString(secret.AccountAuthorizationSourceType)
	record.AccountAuthorizationSourceTeamID = derefString(secret.AccountAuthorizationSourceTeamID)
	record.GroupOwnerSystemAccountID = firstNonEmptyChainUsage(record.GroupOwnerSystemAccountID, secret.GroupOwnerSystemAccountID)
	record.GroupAccessType = firstNonEmptyChainUsage(record.GroupAccessType, secret.GroupAccessType)
	record.GroupAuthorizationID = firstNonEmptyChainUsage(record.GroupAuthorizationID, derefString(secret.GroupAuthorizationID))
	record.GroupAuthorizationSourceType = firstNonEmptyChainUsage(record.GroupAuthorizationSourceType, derefString(secret.GroupAuthorizationSourceType))
	record.GroupAuthorizationSourceTeamID = firstNonEmptyChainUsage(record.GroupAuthorizationSourceTeamID, derefString(secret.GroupAuthorizationSourceTeamID))
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func firstNonEmptyChainUsage(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (u chainFinalizationUsage) RecordFailedUpstreamAttempt(input gatewayresponse.FailedAttemptInput) {
	if u.recorder == nil {
		return
	}
	record := gatewayusage.UsageRecordInput{
		TraceID:            input.UsageContext.TraceID,
		TrafficSource:      "gateway",
		ClientIP:           input.UsageContext.ClientIP,
		SystemAccountID:    input.UsageContext.SystemAccountID,
		APIKeyID:           input.UsageContext.APIKeyID,
		GroupID:            input.UsageContext.GroupID,
		Endpoint:           input.UsageContext.Endpoint,
		ProviderCode:       input.UsageContext.ProviderCode,
		UsageSemantic:      "gateway_request",
		Success:            false,
		ErrorCode:          "upstream_retryable_error",
		ErrorMessage:       input.ErrorMessage,
		FailureAttribution: input.FailureAttribution,
		CreatedAt:          time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		// scope 完整性五元组，与成功路径同规则。
		GroupOwnerSystemAccountID:      input.UsageContext.GroupOwnerSystemAccountID,
		GroupAccessType:                input.UsageContext.GroupAccessType,
		GroupAuthorizationID:           input.UsageContext.GroupAuthorizationID,
		GroupAuthorizationSourceType:   input.UsageContext.GroupAuthorizationSourceType,
		GroupAuthorizationSourceTeamID: input.UsageContext.GroupAuthorizationSourceTeamID,
	}
	applyUsageAccountScope(&record, input.Account)
	if input.StatusCode != nil {
		statusCode := *input.StatusCode
		record.StatusCode = &statusCode
	}
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

// ---------------------------------------------------------------------------
// usage record id factory
// ---------------------------------------------------------------------------

// usageDefaultUsageShardCount mirrors the jobs usagewriter
// DefaultUsageShardCount（JUHE_AI_USAGE_SHARD_COUNT 默认 16）。三项目基线
// 禁止 gateway 进程 import jobs 模块，故 shard-id 格式在此按
// generateUsageRecordId 契约复制实现，单测对照 jobs 契约防漂移。分片 id
// 只影响同 id 行的路由映射（drain → writer 按 id 前缀 parse 路由），与
// jobs 侧配置的 ShardCount 不一致时仍保证 id→文件 的确定性。
const usageDefaultUsageShardCount = 16

// usageShardRecordIDFactory implements gatewayusage.UsageRecordIDFactory:
// `usage_<bucketDateKey>_sNN_<unixmilli>_<entropy sanitized to 24>`
// (storage/usage-record-shards.ts generateUsageRecordId)。G17 port 声明把
// shard-id 格式留在 writer slice；组合根在无 Redis 交接路径上需要本进程
// 生成稳定 id（spool 记录缺 id 会被 jobs drain 判损坏），因此此处复制该
// 格式，使 spool 记录与 Redis 队列路径的记录 id 同构。
type usageShardRecordIDFactory struct {
	// now 注入时间源（单测确定性）；nil 回落 wall clock。
	now func() time.Time
}

// GenerateUsageRecordID implements gatewayusage.UsageRecordIDFactory.
// createdAt 已由 NormalizeUsageRecordInput 校验为 RFC3339 instant；解析
// 失败时回落当前时间 bucket，保持 id 可路由（与 jobs idFactory 兜底分支
// 同形）。
func (f usageShardRecordIDFactory) GenerateUsageRecordID(createdAt string) string {
	now := f.now
	if now == nil {
		now = time.Now
	}
	bucket, err := usageBucketDateKey(createdAt)
	if err != nil {
		bucket = now().UTC().Format("20060102")
	}
	entropy := usageRecordEntropyUUID()
	shardID := usageStableHash(entropy) % usageDefaultUsageShardCount
	return fmt.Sprintf("usage_%s_s%02d_%d_%s", bucket, shardID, now().UnixMilli(), usageSanitizeShardEntropy(entropy))
}

// usageBucketDateKey mirrors bucketDateKeyFromIso: YYYYMMDD in UTC from an
// RFC3339 instant.
func usageBucketDateKey(createdAt string) (string, error) {
	parsed, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return "", err
	}
	return parsed.UTC().Format("20060102"), nil
}

// usageRecordEntropyUUID 生成随机 entropy（RFC 4122 v4 同形的 hex 段）。
// 随机源失败属环境级异常：回落纳秒时间熵，保持 id 唯一性与可路由性。
func usageRecordEntropyUUID() string {
	bytes := make([]byte, 16)
	if _, err := crand.Read(bytes); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return hex.EncodeToString(bytes)
}

// usageSanitizeShardEntropy mirrors entropy.replace(/[^a-zA-Z0-9]/g,
// ”).slice(0, 24).
func usageSanitizeShardEntropy(entropy string) string {
	var builder strings.Builder
	for _, r := range entropy {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			if builder.Len() >= 24 {
				break
			}
		}
	}
	return builder.String()
}

// usageStableHash mirrors stableShardId 的 FNV-1a 32-bit（与 jobs
// usagewriter.StableHash 同式，ASCII 输入逐字节一致）。
func usageStableHash(value string) int {
	hash := uint32(2166136261)
	for index := 0; index < len(value); index++ {
		hash ^= uint32(value[index])
		hash *= 16777619
	}
	return int(hash)
}
