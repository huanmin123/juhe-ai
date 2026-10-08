package gatewaydispatch

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
)

// gatewayupstream 门面桥（REFACTOR-0006 阶段 A）。
//
// 上游传输族（transport/body/usageheaders/clientheaders/headerpolicy/
// firstbytedeadline/transport_urlpolicy/providerprotocol + errors/helpers
// 切块）已迁入 gatewayupstream 叶子包；本桥以类型 alias、常量 alias 与
// 自由函数转发保持根包留守文件、测试文件与全部外部消费文件的
// `gatewaydispatch.XXX` 前缀引用零改动。转发均为一行直调，无接口间接层。

// --- 错误类型（原 errors.go 上游传输块） ---

type UpstreamRequestTimeoutError = gatewayupstream.UpstreamRequestTimeoutError

type UpstreamRequestAbortedError = gatewayupstream.UpstreamRequestAbortedError

type FirstByteTimeoutSource = gatewayupstream.FirstByteTimeoutSource

const (
	FirstByteTimeoutSourceHardTimeout        = gatewayupstream.FirstByteTimeoutSourceHardTimeout
	FirstByteTimeoutSourceConfiguredDeadline = gatewayupstream.FirstByteTimeoutSourceConfiguredDeadline
)

type GatewayFirstByteTimeoutError = gatewayupstream.GatewayFirstByteTimeoutError

type GatewayResponsePrecommitDeadlineError = gatewayupstream.GatewayResponsePrecommitDeadlineError

type StartedTransportError = gatewayupstream.StartedTransportError

type StartedBodyTransportError = gatewayupstream.StartedBodyTransportError

type UpstreamBodyReadIncompleteError = gatewayupstream.UpstreamBodyReadIncompleteError

type UpstreamBodyReadMaxLifetimeError = gatewayupstream.UpstreamBodyReadMaxLifetimeError

type NonStreamUpstreamBodyPipeError = gatewayupstream.NonStreamUpstreamBodyPipeError

type UnsupportedUpstreamResponseEncodingError = gatewayupstream.UnsupportedUpstreamResponseEncodingError

type UnsafeResolvedUpstreamURLError = gatewayupstream.UnsafeResolvedUpstreamURLError

type UnsafeUpstreamURLError = gatewayupstream.UnsafeUpstreamURLError

func IsGatewayFirstByteTimeoutError(err error) bool {
	return gatewayupstream.IsGatewayFirstByteTimeoutError(err)
}

func IsGatewayResponsePrecommitDeadlineError(err error) bool {
	return gatewayupstream.IsGatewayResponsePrecommitDeadlineError(err)
}

func IsStartedUpstreamTransportError(err error) bool {
	return gatewayupstream.IsStartedUpstreamTransportError(err)
}

func timeoutLikeText(text string) bool { return gatewayupstream.TimeoutLikeText(text) }

// --- 传输族类型（原 transport.go / body.go 等） ---

type GatewayUpstreamResponse = gatewayupstream.GatewayUpstreamResponse

type DialPhaseTransportError = gatewayupstream.DialPhaseTransportError

type ConcurrencyGovernor = gatewayupstream.ConcurrencyGovernor

type NopConcurrencyGovernor = gatewayupstream.NopConcurrencyGovernor

type BoundedConcurrencyGovernor = gatewayupstream.BoundedConcurrencyGovernor

type CopySafeUpstreamHeadersOptions = gatewayupstream.CopySafeUpstreamHeadersOptions

type UpstreamHeaderAccount = gatewayupstream.UpstreamHeaderAccount

type UpstreamRequestOptions = gatewayupstream.UpstreamRequestOptions

type TransportDeps = gatewayupstream.TransportDeps

type LimitedBodyReadInput = gatewayupstream.LimitedBodyReadInput

type LimitedBodyReadResult = gatewayupstream.LimitedBodyReadResult

type IncomingHeaders = gatewayupstream.IncomingHeaders

type OfficialOAuthClientHeaderProfile = gatewayupstream.OfficialOAuthClientHeaderProfile

type CodexTurnMetadata = gatewayupstream.CodexTurnMetadata

type FirstByteDeadlineAction = gatewayupstream.FirstByteDeadlineAction

type FirstByteDeadlineDecisionInput = gatewayupstream.FirstByteDeadlineDecisionInput

type FirstByteDeadlineDecisionWaitOptions = gatewayupstream.FirstByteDeadlineDecisionWaitOptions

type FirstByteDeadlineHandler = gatewayupstream.FirstByteDeadlineHandler

type PassthroughUpstreamURLPolicy = gatewayupstream.PassthroughUpstreamURLPolicy

type ResolvedUpstreamURLPolicy = gatewayupstream.ResolvedUpstreamURLPolicy

// --- 小工具与谓词转发（原 util.go / errorhelpers.go / bodypreparation.go /
// providerprotocol.go 内被留守文件消费的符号） ---

func trimString(value string) string { return gatewayupstream.TrimString(value) }

func normalizeProviderToken(value string) string {
	return gatewayupstream.NormalizeProviderToken(value)
}

func isOpenAIProtocolProfileWith(protocolCode, protocolVersion string) bool {
	return gatewayupstream.IsOpenAIProtocolProfileWith(protocolCode, protocolVersion)
}

func parseIsoDate(value string) *time.Time { return gatewayupstream.ParseIsoDate(value) }

var IsOpenAIOAuthCodexCompactRequest = gatewayupstream.IsOpenAIOAuthCodexCompactRequest

// NowMs 只读别名：生产消费方（cmd chain_*）经门面读默认时钟；测试注入统一
// 走 gatewayupstream.NowMs 真身（见 injectNowMs），两者初值一致。
var NowMs = gatewayupstream.NowMs

// --- provider 协议谓词与常量（原 providerprotocol.go，随传输族落位） ---

const (
	GPTVendorCode        = gatewayupstream.GPTVendorCode
	GPTOpenAIV1ProfileID = gatewayupstream.GPTOpenAIV1ProfileID
)

func IsGptVendorCode(value string) bool { return gatewayupstream.IsGptVendorCode(value) }

func isOpenAIProtocolProfileSecret(protocolCode, protocolVersion string) bool {
	return gatewayupstream.IsOpenAIProtocolProfileSecret(protocolCode, protocolVersion)
}

func IsDialPhaseStartedTransportError(err error) bool {
	return gatewayupstream.IsDialPhaseStartedTransportError(err)
}

// --- 跨包叶子小工具的私有名转发（原 util.go / bodypreparation.go） ---

func decodeJSONObject(raw []byte) (map[string]any, bool) {
	return gatewayupstream.DecodeJSONObject(raw)
}

func stripV1Prefix(path string) string { return gatewayupstream.StripV1Prefix(path) }

// --- 首字节死线小工具（原 firstbytedeadline.go 私有名，经桥保持留守文件
// 消费点零改动） ---

const FirstByteDeadlineActionAbort = gatewayupstream.FirstByteDeadlineActionAbort

func maxInt64(a, b int64) int64 { return gatewayupstream.MaxInt64(a, b) }

func minInt64(a, b int64) int64 { return gatewayupstream.MinInt64(a, b) }

func waitForDelayMs(ctx context.Context, delayMs int64) error {
	return gatewayupstream.WaitForDelayMs(ctx, delayMs)
}

// --- 传输族导出函数面（var 别名，普通函数值语义等价；泛型函数用 func 包装） ---

var (
	ApplyOpenAICodexHeaders                   = gatewayupstream.ApplyOpenAICodexHeaders
	BuildAnthropicUsageRecordMaintenanceJob   = gatewayupstream.BuildAnthropicUsageRecordMaintenanceJob
	BuildOpenAICodexUsageRecordMaintenanceJob = gatewayupstream.BuildOpenAICodexUsageRecordMaintenanceJob
	BuildUpstreamHeaders                      = gatewayupstream.BuildUpstreamHeaders
	CopyOfficialOAuthClientRequestHeaders     = gatewayupstream.CopyOfficialOAuthClientRequestHeaders
	CopyResponseHeaders                       = gatewayupstream.CopyResponseHeaders
	CopySafeUpstreamRequestHeaders            = gatewayupstream.CopySafeUpstreamRequestHeaders
	HeadersToObject                           = gatewayupstream.HeadersToObject
	IsAnthropicMessagesScopedHeaderName       = gatewayupstream.IsAnthropicMessagesScopedHeaderName
	IsCodexResponsesScopedHeaderName          = gatewayupstream.IsCodexResponsesScopedHeaderName
	IsEffectiveOpenAIStreamRequest            = gatewayupstream.IsEffectiveOpenAIStreamRequest
	IsGeminiGenerateContentScopedHeaderName   = gatewayupstream.IsGeminiGenerateContentScopedHeaderName
	IsOpenAICodexClientHeaders                = gatewayupstream.IsOpenAICodexClientHeaders
	IsUpstreamRequestAbortedError             = gatewayupstream.IsUpstreamRequestAbortedError
	NewBoundedConcurrencyGovernor             = gatewayupstream.NewBoundedConcurrencyGovernor
	NewGatewayUpstreamResponseForTransform    = gatewayupstream.NewGatewayUpstreamResponseForTransform
	NewResolvedUpstreamURLPolicy              = gatewayupstream.NewResolvedUpstreamURLPolicy
	NormalizeOpenAICodexClientHeaders         = gatewayupstream.NormalizeOpenAICodexClientHeaders
	NormalizeOpenAICodexResponsesLiteBody     = gatewayupstream.NormalizeOpenAICodexResponsesLiteBody
	ParseAnthropicUsageHeaders                = gatewayupstream.ParseAnthropicUsageHeaders
	ParseOpenAICodexUsageHeaders              = gatewayupstream.ParseOpenAICodexUsageHeaders
	PersistAnthropicUsageHeaders              = gatewayupstream.PersistAnthropicUsageHeaders
	PersistOpenAICodexUsageHeaders            = gatewayupstream.PersistOpenAICodexUsageHeaders
	ReadStreamChunkWithAbort                  = gatewayupstream.ReadStreamChunkWithAbort
	ReadStreamChunkWithIdleTimeout            = gatewayupstream.ReadStreamChunkWithIdleTimeout
	ReadUpstreamBodyLimited                   = gatewayupstream.ReadUpstreamBodyLimited
	RequestUpstream                           = gatewayupstream.RequestUpstream
	StripAnthropicMessagesScopedHeaders       = gatewayupstream.StripAnthropicMessagesScopedHeaders
	StripCodexResponsesScopedHeaders          = gatewayupstream.StripCodexResponsesScopedHeaders
	StripGeminiGenerateContentScopedHeaders   = gatewayupstream.StripGeminiGenerateContentScopedHeaders
	UpstreamRequestTimeoutMs                  = gatewayupstream.UpstreamRequestTimeoutMs
	UpstreamSocketTimeoutMs                   = gatewayupstream.UpstreamSocketTimeoutMs
	UsesOpenAICodexResponsesLite              = gatewayupstream.UsesOpenAICodexResponsesLite
)

// ObserveFirstBytePendingRead mirrors the generic pending-read observer.
func ObserveFirstBytePendingRead[T any](pendingRead func() (T, error)) *gatewayupstream.ObservedFirstBytePendingRead[T] {
	return gatewayupstream.ObserveFirstBytePendingRead(pendingRead)
}

// DecideFirstByteDeadlineAfterPendingRead mirrors the generic deadline decision.
func DecideFirstByteDeadlineAfterPendingRead[T any](
	pendingRead *gatewayupstream.ObservedFirstBytePendingRead[T],
	handler FirstByteDeadlineHandler,
	input FirstByteDeadlineDecisionInput,
	options FirstByteDeadlineDecisionWaitOptions,
) gatewayupstream.FirstByteDeadlineDecisionResult[T] {
	return gatewayupstream.DecideFirstByteDeadlineAfterPendingRead(pendingRead, handler, input, options)
}

// --- 传输族常量面 ---

const (
	DeadlineDecisionAction            = gatewayupstream.DeadlineDecisionAction
	DeadlineDecisionRead              = gatewayupstream.DeadlineDecisionRead
	DeadlineDecisionResponsePrecommit = gatewayupstream.DeadlineDecisionResponsePrecommit

	FirstByteDeadlineActionContinue = gatewayupstream.FirstByteDeadlineActionContinue

	OpenAICodexOriginator          = gatewayupstream.OpenAICodexOriginator
	OpenAICodexResponsesLiteHeader = gatewayupstream.OpenAICodexResponsesLiteHeader

	OAuthHeaderProfileAnthropicClaude = gatewayupstream.OAuthHeaderProfileAnthropicClaude
	OAuthHeaderProfileGeminiCLI       = gatewayupstream.OAuthHeaderProfileGeminiCLI
	OAuthHeaderProfileOpenAICodex     = gatewayupstream.OAuthHeaderProfileOpenAICodex
	OAuthHeaderProfileXAIGrok         = gatewayupstream.OAuthHeaderProfileXAIGrok
)

// 传输族 scoped-header 名单（原 headerpolicy.go var）。
var (
	AnthropicMessagesScopedHeaderNames     = gatewayupstream.AnthropicMessagesScopedHeaderNames
	CodexResponsesScopedHeaderNames        = gatewayupstream.CodexResponsesScopedHeaderNames
	GeminiGenerateContentScopedHeaderNames = gatewayupstream.GeminiGenerateContentScopedHeaderNames
)

func firstNonEmpty(values ...string) string { return gatewayupstream.FirstNonEmpty(values...) }

// RecordMaintenanceJob mirrors the job envelope the queue port receives
// (原 usageheaders.go 类型).
type OpenAICodexUsageSnapshot = gatewayupstream.OpenAICodexUsageSnapshot

type RecordMaintenanceJob = gatewayupstream.RecordMaintenanceJob

// --- 传输族内部结构的测试可见面（原私有名，测试原位保留所需的桥接） ---

type ReadOutcome[T any] = gatewayupstream.ReadOutcome[T]

type DeadlineHandlerPanic = gatewayupstream.DeadlineHandlerPanic

// ObservedFirstBytePendingRead mirrors the generic pending-read outcome type.
type ObservedFirstBytePendingRead[T any] = gatewayupstream.ObservedFirstBytePendingRead[T]

type rollingBufferCapture = gatewayupstream.RollingBufferCapture

func newRollingBufferCapture(limitBytes int) *rollingBufferCapture {
	return gatewayupstream.NewRollingBufferCapture(limitBytes)
}

func runDeadlineHandler(handler FirstByteDeadlineHandler, input FirstByteDeadlineDecisionInput) (FirstByteDeadlineAction, error) {
	return gatewayupstream.RunDeadlineHandler(handler, input)
}

func finishDeadlineDecision[T any](pendingRead *ObservedFirstBytePendingRead[T], action FirstByteDeadlineAction, decisionErr error) gatewayupstream.FirstByteDeadlineDecisionResult[T] {
	return gatewayupstream.FinishDeadlineDecision(pendingRead, action, decisionErr)
}

func notifyResponsePrecommitDeadline(callback func()) {
	gatewayupstream.NotifyResponsePrecommitDeadline(callback)
}

func syntheticCodexTurnMetadata(headers http.Header) CodexTurnMetadata {
	return gatewayupstream.SyntheticCodexTurnMetadata(headers)
}

func parsedCodexTurnMetadata(value string) map[string]any {
	return gatewayupstream.ParsedCodexTurnMetadata(value)
}

func extraMetadataKeys(current map[string]any) map[string]any {
	return gatewayupstream.ExtraMetadataKeys(current)
}

func codexIdentityPrefix(identity string) bool {
	return gatewayupstream.CodexIdentityPrefix(identity)
}

func firstNonEmptyString(values ...string) string {
	return gatewayupstream.FirstNonEmptyString(values...)
}

func headerGetTrimmed(headers http.Header, name string) string {
	return gatewayupstream.HeaderGetTrimmed(headers, name)
}

func setHeaderIfMissing(headers http.Header, name, value string) {
	gatewayupstream.SetHeaderIfMissing(headers, name, value)
}

var buildOpenAICodexUsageSnapshotPayload = gatewayupstream.BuildOpenAICodexUsageSnapshotPayload

func normalizeTransportError(err error) error {
	return gatewayupstream.NormalizeTransportError(err)
}

func isPreConnectionDialError(err error) bool {
	return gatewayupstream.IsPreConnectionDialError(err)
}

func isOpenAIProtocolProfile(account UpstreamHeaderAccount) bool {
	return gatewayupstream.IsOpenAIProtocolProfileWith(account.ProtocolCode, account.ProtocolVersion)
}

func parseContentEncodings(value string) []string {
	return gatewayupstream.ParseContentEncodings(value)
}

func allIdentity(encodings []string) bool {
	return gatewayupstream.AllIdentity(encodings)
}

func decodeUpstreamResponseBody(body io.ReadCloser, contentEncoding string) (io.ReadCloser, error) {
	return gatewayupstream.DecodeUpstreamResponseBody(body, contentEncoding)
}

func closeReader(reader io.Reader) error {
	return gatewayupstream.CloseReader(reader)
}

func isAllowedOfficialOAuthClientHeader(name string, profile OfficialOAuthClientHeaderProfile) bool {
	return gatewayupstream.IsAllowedOfficialOAuthClientHeader(name, profile)
}

func numberValueOf(value string) *float64 {
	return gatewayupstream.NumberValueOf(value)
}

func resetAtFromSeconds(baseTime time.Time, seconds *int64) string {
	return gatewayupstream.ResetAtFromSeconds(baseTime, seconds)
}

type NormalizedCodexLimits = gatewayupstream.NormalizedCodexLimits

type codexWindowCandidate = gatewayupstream.CodexWindowCandidate

func assignNormalizedWindow(normalized *NormalizedCodexLimits, key string, candidate codexWindowCandidate) {
	gatewayupstream.AssignNormalizedWindow(normalized, key, candidate)
}
