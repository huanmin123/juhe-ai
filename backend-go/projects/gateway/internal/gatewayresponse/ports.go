package gatewayresponse

import (
	"context"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// G17/G13/G14/G18 接缝：usage 记录、审计尝试、账号副作用、session 亲和遗忘、
// 健康检查投递、模型目录加载与 HTTP 完成观察。实现归属后续工作包；本包冻结
// 调用顺序与入参形状。

// AccountView 是 finalization 消费的账户最小投影（Node UpstreamAccount 子集）。
// 生产装配直接使用 gatewayruntimecache.OpenAIAccountSecret。
type AccountView interface {
	GetID() string
	GetName() string
	GetProviderCode() string
	GetProviderProtocolProfileID() string
	GetProtocolCode() string
	GetProtocolVersion() string
	GetClientCompatibility() string
}

// OpenAIAccountView 把 gatewayruntimecache.OpenAIAccountSecret 适配为
// AccountView（只读适配，不复制账户数据）。
type OpenAIAccountView struct {
	Account gatewayruntimecache.OpenAIAccountSecret
}

func (v OpenAIAccountView) GetID() string           { return v.Account.ID }
func (v OpenAIAccountView) GetName() string         { return v.Account.Name }
func (v OpenAIAccountView) GetProviderCode() string { return v.Account.ProviderCode }
func (v OpenAIAccountView) GetProviderProtocolProfileID() string {
	return v.Account.ProviderProtocolProfileID
}
func (v OpenAIAccountView) GetProtocolCode() string        { return v.Account.ProtocolCode }
func (v OpenAIAccountView) GetProtocolVersion() string     { return v.Account.ProtocolVersion }
func (v OpenAIAccountView) GetClientCompatibility() string { return v.Account.ClientCompatibility }

// CompletedAttemptInput 对齐 recordCompletedUpstreamAttempt /
// recordDownstreamClosedUpstreamAttempt 共用的尝试负载（G17）。
type CompletedAttemptInput struct {
	UsageContext                        gatewaypreauth.GatewayFailureUsageContext
	Account                             AccountView
	StatusCode                          int
	Success                             bool
	ProtocolValidatedSuccess            bool
	AccountAPIKeySuccessAlreadyRecorded bool
	Stream                              bool
	FirstTokenMs                        *int64
	StartedAtMs                         int64
	CompletedAtMs                       *int64
	Usage                               gatewayproto.ParsedUsage
	ErrorCode                           string
	ErrorMessage                        string
	FailureAttribution                  string
	RequestSnapshot                     *UsageRequestSnapshotView
	ResponseSnapshot                    *UsageResponseSnapshotView
	// RequestedModel 是客户端请求模型（Node requestModel(req) 语义）；usage
	// 记录的 model 归因用它，而不是上游响应观察模型。空串表示请求体缺失或
	// 未携带 model。
	RequestedModel string
}

// requestModelHint 读取请求模型（RequestModel 的字符串投影；nil 请求安全）。
func requestModelHint(req *gatewaypreauth.GatewayRequest) string {
	if req == nil {
		return ""
	}
	if model, ok := gatewaypreauth.RequestModel(req); ok {
		return model
	}
	return ""
}

// FailedAttemptInput 对齐 recordFailedUpstreamAttempt 的 input。
type FailedAttemptInput struct {
	// UsageContext / Account 承载 Node recordFailedUpstreamAttempt 的
	// usageContext / account 参数：失败 usage 记录同样携带完整身份维度
	// （records.ts 失败路径经 dispatchUsageRecord 写入 traceId/clientIp/
	// systemAccountId/apiKeyId/groupId/accountId/endpoint/providerCode）。
	UsageContext       gatewaypreauth.GatewayFailureUsageContext
	Account            AccountView
	UpstreamURL        string
	StartedAtMs        int64
	StatusCode         *int
	Headers            map[string]string
	BodyText           string
	ErrorPayload       gatewayproto.ErrorPayload
	ErrorMessage       string
	FailureAttribution string
}

// UsageRequestSnapshotView 对齐 usage/records 消费的 requestSnapshot 投影。
type UsageRequestSnapshotView struct {
	Method                   string
	Path                     string
	OriginalURL              string
	ClientIP                 string
	TraceID                  string
	RequestedServiceTier     string
	RequestedReasoningEffort string
	BodyOmission             *StreamBodyOmissionSummary
	// OmittedBody 为 true 时快照不含正文（usageRequestSnapshotWithBodyOmission）。
	OmittedBody bool
}

// UsageResponseSnapshotView 对齐 buildUsageResponseSnapshot 输出。
type UsageResponseSnapshotView struct {
	UpstreamURL  string
	StatusCode   int
	Headers      map[string]string
	BodyText     string
	BodyOmission *StreamBodyOmissionSummary
	ErrorMessage string
	GeneratedBy  string // '' | 'gateway'
}

// UsageAttemptRecorder 对齐 usage/records.ts 的尝试记录面（G17）。
type UsageAttemptRecorder interface {
	RecordCompletedUpstreamAttempt(input CompletedAttemptInput)
	RecordFailedUpstreamAttempt(input FailedAttemptInput)
}

// FailureUsageRecordInput 对齐 recordGatewayFailure 的负载。
type FailureUsageRecordInput struct {
	UsageContext gatewaypreauth.GatewayFailureUsageContext
	// Model / Stream 对齐 recordGatewayFailure 的请求事实（requestModel(req)
	// / requestStream(req)）：失败 usage 行据此落 model 与 stream 列。空
	// Model 表示请求体缺失或未携带 model。
	Model              string
	Stream             bool
	StatusCode         int
	StartedAtMs        int64
	CompletedAtMs      int64
	ResponsePayload    GatewayErrorPayloadCarrier
	ErrorMessage       string
	FailureAttribution string
	ResponseSnapshot   *UsageResponseSnapshotView
}

// GatewayErrorPayloadCarrier 是 errorPayload map 的载体（保留额外键）。
type GatewayErrorPayloadCarrier struct {
	Error map[string]any
	Extra map[string]any
}

// FailureUsageRecorder 对齐 recordGatewayFailure（G17）。
type FailureUsageRecorder interface {
	RecordGatewayFailure(input FailureUsageRecordInput)
}

// ModelsUsageDispatchInput 对齐 dispatchUsageRecord 的 models 快路径负载。
type ModelsUsageDispatchInput struct {
	UsageContext  gatewaypreauth.GatewayFailureUsageContext
	ProviderCode  string
	UsageSemantic string
	Stream        bool
	StatusCode    int
	Success       bool
	FirstTokenMs  int64
	DurationMs    int64
}

// UsageDispatcher 对齐 dispatchUsageRecord（G17）。
type UsageDispatcher interface {
	DispatchUsageRecord(input ModelsUsageDispatchInput)
}

// AttemptAuditCapture 扩展 G05 冻结的 AuditCaptureContext：Node
// completeAttempt / finalizeLazy / omitPayloadBodies 的触发点（G17 消费）。
type AttemptAuditCapture interface {
	gatewaypreauth.AuditCaptureContext
	// CompleteAttempt 对齐 completeAttempt(attemptId, input)。
	CompleteAttempt(attemptID string, input AttemptAuditInput)
	// FinalizeLazy 对齐 finalizeLazy(provider)。
	FinalizeLazy(provider func() gatewaypreauth.AuditFinalizeInput)
	// OmitPayloadBodies 对齐 omitPayloadBodies。
	OmitPayloadBodies(input OmitPayloadBodiesInput)
	// ShouldCaptureSuccessPayloads 对齐 shouldCaptureSuccessPayloads()。
	ShouldCaptureSuccessPayloads() bool
}

// AttemptAuditInput 对齐 CompleteAuditInput 的消费子集。
type AttemptAuditInput struct {
	StatusCode      int
	ResponseHeaders any // Headers 或 map；G17 归一化
	ResponseBody    []byte
	Success         bool
	ErrorPhase      string
	ErrorCode       string
	ErrorMessage    string
	// 扩展（finalization 的 finalize 输入）：
	Outcome          string
	ResponsePartType string
	AccountID        string
	FirstTokenMs     *int64
}

// OmitPayloadBodiesInput 对齐 omitPayloadBodies 的入参。
type OmitPayloadBodiesInput struct {
	Label                      string
	Metadata                   map[string]any
	PartTypes                  []string
	AlreadyOmittedPayloadCount int
	AlreadyOmittedBodyBytes    int64
}

// AccountFailureEffects 对齐 runtime/account-effects.ts 的 stream failure 面
// （G13）。shouldMutateAccount 由 finalization 决策。
type AccountFailureEffects interface {
	// HandleStreamFailure 对齐 handleStreamFailure。
	HandleStreamFailure(account AccountView, message string, errorCode string, context StreamFailureContext, shouldMutateAccount bool) error
	// ForgetSessionAffinity 对齐 forgetOpenAIAccountForSessionAsync（G14）。
	ForgetSessionAffinity(sessionAffinityKey string, accountID string)
	// DispatchRequestFailureAccountHealthCheck 对齐
	// dispatchRequestFailureAccountHealthCheck（含 per-request 去重）。
	DispatchRequestFailureAccountHealthCheck(trafficSource string, accountID string) bool
	// ApplyInspectionPolicySideEffects 对齐
	// applyResponseInspectionPolicyRuntimeSideEffects（G13）。
	ApplyInspectionPolicySideEffects(decision *ResponseInspectionDecision, account AccountView, accountStateMutationEnabled bool) error
	// SuppressLocally / SuppressUpstreamBucket 保留给显式策略副作用。
}

// GatewayModelBinding 是一次 /v1/models 成员装载的激活绑定投影
// (GroupID, ProviderCode)。canonical 定义在 gatewaypreauth（
// ModelsResponseInput 携带 bindings，而本包依赖 gatewaypreauth，不能反向
// 导入）；此别名保证 cmd 装配按设计稿 6.4 的 gatewayresponse.GatewayModelBinding
// 名称引用同一类型。
type GatewayModelBinding = gatewaypreauth.GatewayModelBinding

// GatewayKeyModelList 是一次 /v1/models 成员装载的结果：条目已按模型名
// 字典序排序、成员∪元数据合并完成。ValidUntil 来自并集缓存，供诊断，
// 响应构建不依赖它。
type GatewayKeyModelList struct {
	Entries    []ModelCatalogEntry
	ValidUntil time.Time
}

// ModelCatalogLoader 对齐 listGatewayKeyModelsAsync（/v1/models 账户并集，
// 设计 4.6.1/6.4）：装载失败必须原样返回错误（数据面失败禁止渲染成 200
// 空列表、禁止回退目录）；缓存面失败由实现按既有容错降级。
type ModelCatalogLoader interface {
	ListGatewayKeyModels(ctx context.Context, systemAccountID string, bindings []GatewayModelBinding) (GatewayKeyModelList, error)
}

// HTTPCompletion 对齐 observeGatewayHttpCompletion(res).wait()。
type HTTPCompletion interface {
	// Wait 返回完成时刻（ms）；实现负责在响应真正结束后送达。
	Wait() <-chan int64
}

// HTTPCompletionObserver 对齐 observeGatewayHttpCompletion。req 携带请求级
// 完成观测载体（Go 侧完成 subject 挂请求上下文，与每请求生命周期一一对应，
// 组合根据此解析同一 subject）；实现必须对 nil req 安全（返回 nil 时 sink
// 回退 nowMs 兜底）。
type HTTPCompletionObserver interface {
	Observe(req *gatewaypreauth.GatewayRequest, res gatewaypreauth.GatewayResponseWriter) HTTPCompletion
}

// ClientSourceAvoidance 对齐 client-profiles 的来源避让（G18）。
type ClientSourceAvoidance interface {
	// RememberFailure 对齐 rememberGatewayClientSourceFailureAsync；返回
	// stateKey 与 duplicateObservation 供审计元数据。
	RememberFailure(accountID string, input ClientSourceFailureInput) (ClientSourceFailureResult, bool)
}

// ClientSourceFailureInput 对齐 remember 输入。
type ClientSourceFailureInput struct {
	ErrorCode     string
	Message       string
	Evidence      string
	ObservationID string
}

// ClientSourceFailureResult 对齐 remember 输出的审计投影。
type ClientSourceFailureResult struct {
	StateKey                     string
	FailureCount                 int
	FailedAccountIDs             []string
	AvoidanceActivatedAccountIDs []string
	DuplicateObservation         bool
}
