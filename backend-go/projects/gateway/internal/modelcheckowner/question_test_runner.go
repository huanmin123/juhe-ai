// question_test_runner.go 实现题库提交门禁的测试执行器（
// modelcheckquestionbank.TestRunner 的 owner 侧装配）：按 actor 解析目标
// AI 账户（Resolver 端口，与 HostDependencies.Resolve 同款窄函数），以单题
// 最小套件执行 probe RunQuizDiagnosis 两跳诊断，并把诊断结果映射为题库域
// 契约。依赖 Resolver 函数端口而非 *BusinessTargetSource 具体类型：与
// owner 既有端口风格一致，测试可用 fake resolver 断言 RunRequest 契约。
package modelcheckowner

import (
	"context"
	"strings"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckquestionbank"
)

// questionTestProbeTimeout 是单题提交前诊断的套件超时。整链同步预算由
// 题库 HTTP 层的 120s ctx 截止为准（重试阶梯耗尽时 ctx 先到）；此处取同
// 量级上限，仅作为 Options.Timeout 兜底。
const questionTestProbeTimeout = 2 * time.Minute

// NewQuestionTestRunner 构造题库提交门禁的测试执行器。resolve 是目标解析
// 端口（main 装配传 businessSource.Resolver()）；dispatcher 是检测链路的
// 传输派发端口（main 装配传 HostDependencies 同款 ProbeAdapter，与 J3b
// 运行链同 KeyModel gate 与账户熔断 gate 语义：Diagnostic 探针不写 usage、
// 传输失败对被测账户中性结算）。dispatcher 为 nil 时 probe 回退直连。
func NewQuestionTestRunner(resolve Resolver, dispatcher modelcheckprobe.DispatcherPort) modelcheckquestionbank.TestRunner {
	return &questionTestRunner{resolve: resolve, dispatcher: dispatcher}
}

type questionTestRunner struct {
	resolve    Resolver
	dispatcher modelcheckprobe.DispatcherPort
}

// TestQuestion 实现 modelcheckquestionbank.TestRunner。Resolve 不携带任何
// revision fence（门禁测试不持有冻结快照，以解析当下状态为准；Resolve 对
// 空 revision 跳过 fence 比对）。Resolve error 原样返回——账户不存在、
// 不可调度等属于"无法执行"，由题库 HTTP 层按 400 透传。
func (r *questionTestRunner) TestQuestion(ctx context.Context, input modelcheckquestionbank.TestQuestionInput) (modelcheckquestionbank.TestQuestionResult, error) {
	target, err := r.resolve(ctx, RunRequest{
		SystemAccountID: input.SystemAccountID,
		TargetType:      "account",
		TargetID:        input.AccountID,
		Model:           input.Model,
	})
	if err != nil {
		return modelcheckquestionbank.TestQuestionResult{}, err
	}
	diagnosis, err := modelcheckprobe.RunQuizDiagnosis(ctx, r.suiteFor(target, input), questionTestProbeTimeout)
	if err != nil {
		return modelcheckquestionbank.TestQuestionResult{}, err
	}
	keyPointResults := make([]modelcheckquestionbank.TestKeyPointResult, 0, len(diagnosis.KeyPointResults))
	for _, point := range diagnosis.KeyPointResults {
		keyPointResults = append(keyPointResults, modelcheckquestionbank.TestKeyPointResult{Point: point.Point, Met: point.Met, Note: point.Note})
	}
	accountName := strings.TrimSpace(target.TargetName)
	if accountName == "" {
		accountName = input.AccountID
	}
	// VerdictReason 取裁判 reason 原文（与 GraderNote 同源，均为诊断裁判
	// 输出的截断 reason）：前端 misaligned 引导与 fail 诊断读该字段，不得
	// 恒空。request_failed 时诊断不产出 reason，两字段同为空串，错误信息走
	// ErrorMessage。
	return modelcheckquestionbank.TestQuestionResult{
		Verdict:         diagnosis.Verdict,
		Alignment:       diagnosis.Alignment,
		KeyPointResults: keyPointResults,
		AnswerExcerpt:   diagnosis.AnswerExcerpt,
		GraderNote:      diagnosis.GraderNote,
		VerdictReason:   diagnosis.GraderNote,
		HTTPStatus:      diagnosis.HTTPStatus,
		LatencyMs:       diagnosis.LatencyMs,
		ErrorMessage:    diagnosis.ErrorMessage,
		AccountName:     accountName,
	}, nil
}

// suiteFor 组装单题最小诊断套件：不挂 Tokenizer/ModelLimits/Comparison/
// 进度 hook（本链路只跑 quiz 诊断族，其余家族不执行，快照依赖无观测面）。
// Model 用解析出的上游映射模型，RequestModel 保留公开请求模型，二者不等
// 即 ModelMappingApplied；Retry 取 quick 档（与手动检测默认档一致）。
// Capability 构造与 J3b 运行链（runtime.go probeSuite.Capability）同构：
// 派发器接通时账户熔断门以 CredentialSourceAccountID 为准入键，缺失会被
// Prepare 直接拦截（"account id is required"），故回退链与运行链逐字段对齐。
func (r *questionTestRunner) suiteFor(target Target, input modelcheckquestionbank.TestQuestionInput) modelcheckprobe.Suite {
	probeModel := target.UpstreamModel
	upstreamEndpointMode := target.UpstreamEndpointMode
	if upstreamEndpointMode == "" {
		upstreamEndpointMode = modelcheckprofile.EndpointModeForProtocol(target.UpstreamProtocol, false)
	}
	credentialSourceID := target.CredentialSourceAccountID
	if credentialSourceID == "" {
		credentialSourceID = input.AccountID
	}
	clientEndpointFamily := string(target.SourceEndpointFamily)
	if clientEndpointFamily == "" {
		clientEndpointFamily = string(target.Protocol)
	}
	return modelcheckprobe.Suite{
		Endpoint:                  target.Endpoint,
		ProviderCode:              target.ProviderCode,
		ProviderProtocolProfileID: target.ProviderProtocolProfileID,
		CredentialType:            target.CredentialType,
		Headers:                   target.Headers,
		Model:                     probeModel,
		RequestModel:              input.Model,
		ModelMappingApplied:       probeModel != input.Model,
		Protocol:                  target.Protocol,
		UpstreamProtocol:          target.UpstreamProtocol,
		EndpointMode:              target.EndpointMode,
		UpstreamEndpointMode:      target.UpstreamEndpointMode,
		SupportedEndpointModes:    append([]string(nil), target.SupportedEndpointModes...),
		SupportedModels:           append([]string(nil), target.SupportedModels...),
		Client:                    target.Client,
		Adapter:                   target.UpstreamAdapter,
		Dispatcher:                r.dispatcher,
		Capability:                keymodelruntime.Capability{CredentialSourceAccountID: credentialSourceID, KeyFingerprint: credentialSourceID, ClientModel: input.Model, ClientEndpointFamily: clientEndpointFamily, FinalUpstreamModel: probeModel, UpstreamEndpointMode: upstreamEndpointMode, DispatchRevision: target.DispatchRevision},
		Retry:                     modelcheckprobe.RetryOptionsForProfile("quick"),
		QuizRequested:             true,
		QuizQuestions: []modelcheckprobe.QuizQuestion{{
			Question:        input.QuestionText,
			ReferenceAnswer: input.ReferenceAnswer,
			KeyPoints:       append([]string(nil), input.KeyPoints...),
		}},
	}
}
