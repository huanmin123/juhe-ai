package manualtest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proberepo"
)

// 手动账号测试执行器：Node worker 侧 runAccountTestQueueItem 诊断链的 Go 收口。
//   - 任务认领由 opsjobs.ManualTestQueue 完成（mark_running → 执行器 →
//     complete/fail/cancel + started_at 围栏）；
//   - draft 路径：任务行 draft_account_encrypted v1 信封解密（Node
//     accountTestDraftSnapshot；解密/规范化失败回退保存账户路径）；
//   - 保存账户路径：proberepo 视图解析（find_account_for_test +
//     find_openai_account_for_group 双查询投影）；
//   - 诊断执行：accountprobe.ManualDiagnostics（Key 池 / 单凭据分级，
//     [10s,20s,30s]，images_json 单次 120s）；
//   - 结果写回：result_json 信封对齐 Node AccountTestResult（诊断失败走
//     fail + 信封，配置错误走 fail 无信封，取消走 cancel）。
//
// 已知边界（与 Node worker 的差异，均不产生错误状态写入）：
//   - 不复刻 OAuth token 刷新与代理档案解析（jobs 探针窄路径约定，凭据按
//     快照原样使用）；
//   - 保存账户路径不重放 gateway 的可用性文案门（任务创建时已门禁；创建到
//     执行之间的状态漂移窗口不再拦截）；
//   - stateTargetAccountId 仅用于定位表单草稿归属，执行一律使用快照凭据，
//     不回读状态账户行（Node 的授权实例二次门禁属创建时检查）。

// unsupportedGatewayProtocolTestMessage 对齐 Node worker 侧同名文案。
const unsupportedGatewayProtocolTestMessage = "当前仅支持测试 OpenAI、Anthropic 或 Gemini 协议账户"

// accountMissingMessage 对齐 Node worker 侧 loadAccountForTest 缺失文案。
const accountMissingMessage = "账户不存在"

// SavedAccountSource 是保存账户路径的视图解析 port（proberepo.Store 实现）。
type SavedAccountSource interface {
	LoadAccountForTest(ctx context.Context, accountID string) (*proberepo.AccountForTestView, error)
	LoadAccountForGroup(ctx context.Context, groupID, accountID, systemAccountID string) (*proberepo.CandidateAccount, error)
}

// ExecutorOptions 组装执行器。
type ExecutorOptions struct {
	// Probe 是诊断引擎（手动测试分支：池 / 单凭据分级）。
	Probe *accountprobe.Service
	// SavedAccounts 提供保存账户路径视图；nil 时保存账户路径任务按
	// “账户不存在”失败（draft 路径不受影响）。
	SavedAccounts SavedAccountSource
	// Secret 是凭据 v1 信封密钥（解密 draft_account_encrypted）。
	Secret string
	// Now 供总耗时测量；nil 使用 time.Now。
	Now func() time.Time
}

// Executor 实现 opsjobs.ManualTestExecutor 语义（经 Adapter 注入队列）。
type Executor struct {
	probe         *accountprobe.Service
	savedAccounts SavedAccountSource
	secret        string
	now           func() time.Time
}

// NewExecutor 构建执行器；依赖缺失返回错误。
func NewExecutor(options ExecutorOptions) (*Executor, error) {
	if options.Probe == nil {
		return nil, errors.New("manualtest 缺少诊断探针服务")
	}
	if strings.TrimSpace(options.Secret) == "" {
		return nil, errors.New("manualtest 缺少凭据解密密钥")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Executor{probe: options.Probe, savedAccounts: options.SavedAccounts, secret: options.Secret, now: now}, nil
}

// Execute 执行单条测试任务（队列在 mark_running 之后调用）。
func (e *Executor) Execute(ctx context.Context, task opsjobs.ManualTestTaskRecord, report opsjobs.ProgressReporter) (opsjobs.ManualTestTaskExecutorResult, error) {
	startedAt := e.now()
	view, failMessage, err := e.resolveView(ctx, task)
	if err != nil {
		return opsjobs.ManualTestTaskExecutorResult{}, err
	}
	if failMessage != "" {
		// Node：配置/定位错误走 failAccountTestTask（无结果信封）。
		return opsjobs.ManualTestTaskExecutorResult{Success: false, Message: failMessage}, nil
	}
	if view == nil {
		return opsjobs.ManualTestTaskExecutorResult{}, errors.New(accountMissingMessage)
	}
	limited := strings.EqualFold(strings.TrimSpace(task.Diagnostics), "limited")
	reportProgress(report, view)
	observation, poolAttempts, diagErr := e.probe.ManualDiagnostics(ctx, view, limited)
	if diagErr != nil {
		if ctx.Err() != nil || errors.Is(diagErr, context.Canceled) {
			return opsjobs.ManualTestTaskExecutorResult{Canceled: true}, nil
		}
		return opsjobs.ManualTestTaskExecutorResult{}, diagErr
	}
	if ctx.Err() != nil {
		// Node：controller.signal.aborted → markAccountTestTaskCanceled。
		return opsjobs.ManualTestTaskExecutorResult{Canceled: true}, nil
	}
	if observation == nil {
		return opsjobs.ManualTestTaskExecutorResult{}, errors.New("账户测试没有产生诊断结果")
	}
	result := e.buildResult(task, view, observation, poolAttempts, startedAt)
	return opsjobs.ManualTestTaskExecutorResult{
		Success:    result.Success,
		Message:    result.Message,
		ResultJSON: result.envelopeJSON,
	}, nil
}

// resolveView 组装探针视图。failMessage 非空表示配置/定位类失败（fail 无信封）。
func (e *Executor) resolveView(ctx context.Context, task opsjobs.ManualTestTaskRecord) (*accountprobe.View, string, error) {
	if task.DraftAccountEncrypted != "" {
		draft, err := DecryptDraft(e.secret, task.DraftAccountEncrypted)
		if err != nil {
			// Node：解密失败 → draftAccount undefined → 回退保存账户路径。
			draft = nil
		}
		if draft != nil {
			if !isGatewaySupportedDraftProtocol(draft.ProtocolCode, draft.ProtocolVersion) {
				return nil, unsupportedGatewayProtocolTestMessage, nil
			}
			return draftView(e.secret, draft, task.Model, task.TestEndpointMode), "", nil
		}
	}
	if e.savedAccounts == nil {
		return nil, accountMissingMessage, nil
	}
	account, err := e.savedAccounts.LoadAccountForTest(ctx, task.AccountID)
	if err != nil {
		return nil, "", err
	}
	if account == nil {
		return nil, accountMissingMessage, nil
	}
	systemAccountID := firstNonEmpty(task.RequestSystemAccountFilterID, task.RequestSystemAccountID, account.OwnerSystemAccountID, account.SystemAccountID)
	candidate, err := e.savedAccounts.LoadAccountForGroup(ctx, account.BoundGroupID, task.AccountID, systemAccountID)
	if err != nil {
		return nil, "", err
	}
	if candidate == nil {
		return nil, fmt.Sprintf("账户 %s 不在当前分组或凭据不可用，无法执行网关测试", task.AccountID), nil
	}
	view := proberepo.AssembleProbeView(account, candidate)
	if strings.TrimSpace(task.Model) != "" {
		view.HealthCheckModel = strings.TrimSpace(task.Model)
	}
	if strings.TrimSpace(task.TestEndpointMode) != "" {
		view.HealthCheckEndpointMode = strings.TrimSpace(task.TestEndpointMode)
	}
	return view, "", nil
}

// ---- 结果信封（Node AccountTestResult 的 worker 写回投影）----

type poolItemResult struct {
	KeyIndex   int     `json:"keyIndex"`
	KeyPrefix  *string `json:"keyPrefix,omitempty"`
	KeySuffix  *string `json:"keySuffix,omitempty"`
	Success    bool    `json:"success"`
	StatusCode *int    `json:"statusCode,omitempty"`
	ErrorCode  string  `json:"errorCode,omitempty"`
	Message    string  `json:"message"`
	DurationMs *int64  `json:"durationMs,omitempty"`
}

type apiKeyPoolResult struct {
	Total                int              `json:"total"`
	Tested               int              `json:"tested"`
	SuccessCount         int              `json:"successCount"`
	FailedCount          int              `json:"failedCount"`
	RequiredSuccessCount int              `json:"requiredSuccessCount"`
	Results              []poolItemResult `json:"results"`
}

type testResultEnvelope struct {
	AccountID                 string            `json:"accountId"`
	AccountName               string            `json:"accountName"`
	ProviderCode              string            `json:"providerCode"`
	ProviderProtocolProfileID string            `json:"providerProtocolProfileId,omitempty"`
	ProtocolCode              string            `json:"protocolCode,omitempty"`
	ProtocolVersion           string            `json:"protocolVersion,omitempty"`
	Type                      string            `json:"type"`
	TraceID                   string            `json:"traceId,omitempty"`
	Success                   bool              `json:"success"`
	StatusCode                *int              `json:"statusCode,omitempty"`
	ErrorCode                 string            `json:"errorCode,omitempty"`
	Message                   string            `json:"message"`
	Model                     string            `json:"model,omitempty"`
	TestEndpointMode          string            `json:"testEndpointMode,omitempty"`
	ResponseHeaders           map[string]string `json:"responseHeaders,omitempty"`
	ResponseText              string            `json:"responseText,omitempty"`
	FirstTokenMS              *int64            `json:"firstTokenMs,omitempty"`
	DurationMs                *int64            `json:"durationMs,omitempty"`
	AccountStatus             string            `json:"accountStatus,omitempty"`
	APIKeyPool                *apiKeyPoolResult `json:"apiKeyPool,omitempty"`
}

type builtResult struct {
	Success      bool
	Message      string
	envelopeJSON string
}

// buildResult 组装结果信封并序列化（Node complete/fail 的 result_json）。
func (e *Executor) buildResult(task opsjobs.ManualTestTaskRecord, view *accountprobe.View, observation *accountquality.ProbeObservation, poolAttempts []accountprobe.PoolKeyAttempt, startedAt time.Time) builtResult {
	result := observation.Result
	envelope := &testResultEnvelope{
		AccountID:                 view.AccountID,
		AccountName:               view.AccountName,
		ProviderCode:              view.ProviderCode,
		ProviderProtocolProfileID: view.ProviderProtocolProfileID,
		ProtocolCode:              view.ProtocolCode,
		ProtocolVersion:           view.ProtocolVersion,
		Type:                      view.Type,
		TraceID:                   result.TraceID,
		Success:                   result.Success,
		StatusCode:                result.StatusCode,
		ErrorCode:                 result.ErrorCode,
		Message:                   result.Message,
		Model:                     view.HealthCheckModel,
		TestEndpointMode:          strings.TrimSpace(task.TestEndpointMode),
		ResponseHeaders:           result.ResponseHeaders,
		ResponseText:              result.ResponseBodyText,
		AccountStatus:             view.Status,
	}
	if result.FirstTokenMS > 0 {
		firstToken := result.FirstTokenMS
		envelope.FirstTokenMS = &firstToken
	}
	durationMS := e.now().Sub(startedAt).Milliseconds()
	envelope.DurationMs = &durationMS
	if len(poolAttempts) > 0 {
		pool := buildPoolSummary(len(view.APIKeyEntries), poolAttempts)
		envelope.APIKeyPool = pool
		envelope.Success = pool.SuccessCount >= 1
		envelope.ErrorCode = ""
		if !envelope.Success {
			envelope.ErrorCode = result.ErrorCode
		}
		envelope.Message = poolTestMessage(pool)
	}
	encoded, err := marshalJSONEnvelope(envelope)
	if err != nil {
		// 信封序列化失败按执行异常处理（队列 fail 收口）。
		return builtResult{Success: false, Message: "账号测试任务执行失败"}
	}
	return builtResult{Success: envelope.Success, Message: envelope.Message, envelopeJSON: encoded}
}

// buildPoolSummary 等价 accountApiKeyPoolSummaryResult 的池投影（tested 为
// 已完成 Key 数，results 按 entry.index 升序）。
func buildPoolSummary(total int, poolAttempts []accountprobe.PoolKeyAttempt) *apiKeyPoolResult {
	items := make([]poolItemResult, 0, len(poolAttempts))
	successCount := 0
	for _, attempt := range poolAttempts {
		result := attempt.Observation.Result
		if result.Success {
			successCount++
		}
		item := poolItemResult{
			KeyIndex:   attempt.Entry.Index,
			KeyPrefix:  keySliceForDisplay(attempt.Entry.Key, 0, 4),
			KeySuffix:  keySliceForDisplay(attempt.Entry.Key, maxInt(0, len(attempt.Entry.Key)-4), len(attempt.Entry.Key)),
			Success:    result.Success,
			StatusCode: result.StatusCode,
			ErrorCode:  result.ErrorCode,
			Message:    result.Message,
		}
		if result.DurationMs > 0 {
			duration := result.DurationMs
			item.DurationMs = &duration
		}
		items = append(items, item)
	}
	return &apiKeyPoolResult{
		Total:                total,
		Tested:               len(items),
		SuccessCount:         successCount,
		FailedCount:          len(items) - successCount,
		RequiredSuccessCount: 1,
		Results:              items,
	}
}

// poolTestMessage 对齐 accountApiKeyPoolTestMessage 文案（逐分支一致）。
func poolTestMessage(pool *apiKeyPoolResult) string {
	if pool.SuccessCount >= 1 {
		skippedCount := pool.Total - pool.Tested
		if skippedCount < 0 {
			skippedCount = 0
		}
		suffix := ""
		if skippedCount > 0 {
			suffix = fmt.Sprintf("，%d 个未测试", skippedCount)
		}
		if pool.FailedCount > 0 {
			return fmt.Sprintf("API Key 池测试通过：已测 %d/%d，%d 个 Key 可用，%d 个 Key 未通过%s",
				pool.Tested, pool.Total, pool.SuccessCount, pool.FailedCount, suffix)
		}
		return fmt.Sprintf("API Key 池测试通过：已测 %d/%d，%d 个 Key 可用%s",
			pool.Tested, pool.Total, pool.SuccessCount, suffix)
	}
	if pool.Tested < pool.Total {
		return fmt.Sprintf("API Key 池测试未完成：0/%d 个 Key 可用", pool.Total)
	}
	return fmt.Sprintf("API Key 池测试未通过：0/%d 个 Key 可用", pool.Total)
}

// reportProgress 对齐 accountDiagnosticAttemptMessage：maxTotalTimeoutMs 取
// 整个分级序列总预算（Node accountDiagnosticAttemptProgress 的 schedule 总和，
// generation 60s / images 120s）。Node 每次尝试写同一消息；Go 在诊断启动前
// 写一次（最终 status_message 等价）。
func reportProgress(report opsjobs.ProgressReporter, view *accountprobe.View) {
	mode := accountprobe.EndpointMode(strings.TrimSpace(view.HealthCheckEndpointMode))
	total := int64(0)
	for _, timeout := range accountprobe.DiagnosticRetryTimeouts {
		total += timeout.Milliseconds()
	}
	if mode == accountprobe.ModeImagesJSON {
		total = accountprobe.ImageDiagnosticRetryTimeouts[0].Milliseconds()
	}
	report(opsjobs.DiagnosticAttemptProgressMessage(total, string(mode)))
}

func keySliceForDisplay(key string, start, end int) *string {
	if key == "" || start < 0 {
		return nil
	}
	if end > len(key) {
		end = len(key)
	}
	if start >= end {
		return nil
	}
	value := key[start:end]
	return &value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
