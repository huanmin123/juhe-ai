package oauthmgmt

// T65-①（网关上游 401 自动刷新）：网关派发链对 OAuth 账户上游 401 的进程内
// 自动刷新入口。与 refreshToken handler（routes.go）同一刷新序列：
//
//	findRotationAccount（DB 现读）→ 按 vendor_code + account type 匹配 plan 并
//	过 plan.rotatable 门 → openai blocked 错误账户守卫 → requiresProxy 守卫与
//	proxyProfileRequestURL → plan.refreshStored 换发 → RotateCredentials。
//
// 与管理面刷新的两处差异：
//  1. Expected revision 用库内现读的 config_revision（不用任何缓存快照的旧
//     revision）；CAS 冲突 = 他人（另一并发请求 / jobs 定时刷新）已刷新，按
//     already-current 语义视同成功返回。
//  2. Access 用进程内自动化 scope（IsAdmin）：调用方是网关链面自动化组件，
//     不是任何管理面用户会话，不限定 owner。
//
// providerPlans / plan.refreshStored 是包内闭包，本方法是对外的最小封装，
// 不导出 plan 结构本身。上游换发失败以 AutoRefreshUpstreamError 标记，供链面
// 协调器区分「上游失败（记 5 分钟不再自动刷）」与「本地错误（仅跳过）」。

import (
	"context"
	"errors"
	"strings"
)

// AutoRefreshResult is the outcome of one automatic rotation attempt.
type AutoRefreshResult struct {
	// Rotated 表示本次调用完成了轮换写库（config_revision 已前进）。
	Rotated bool
	// AlreadyCurrent 表示库内凭据已是最新（CAS 冲突 = 他人已刷新，或换发结果
	// 与现值相同）：不视为失败，调用方可直接用库内新凭据重试。
	AlreadyCurrent bool
	// ConfigRevision 是轮换后（或现读）的 config_revision。
	ConfigRevision int64
	UpdatedAt      string
}

// AutoRefreshUpstreamError 标记错误发生在上游换发阶段（plan.refreshStored，
// invalid_grant 等任何 token 端点错误）。链面协调器据此记 per-account 5 分钟
// 「不再自动刷」节流；本地错误（代理缺失 / plan 不支持 / 存储错误）不带本标记。
type AutoRefreshUpstreamError struct{ Err error }

func (e *AutoRefreshUpstreamError) Error() string { return e.Err.Error() }

// Unwrap keeps the original refreshStored error (oauthmgmt.UpstreamError 等)
// reachable for errors.As 消费方。
func (e *AutoRefreshUpstreamError) Unwrap() error { return e.Err }

// RotateAccountTokensFromStored 用库内当前凭据换发一次 OAuth 令牌并提交轮换。
// 序列与字段语义见文件头注释；返回值区分「本次轮换」与「已是最新」。
func (s *Store) RotateAccountTokensFromStored(ctx context.Context, accountID string) (*AutoRefreshResult, error) {
	ctx = ensureContext(ctx)
	// 进程内自动化 scope：网关链面自动刷新不是用户会话操作，IsAdmin 直读
	//（manageableID 只在 FilterID 非空时收窄，这里保持空 = 全量）。
	access := AccessScope{IsAdmin: true}
	current, err := s.findRotationAccount(ctx, accountID, access)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, &ValidationError{Message: "OAuth 账户不存在"}
	}
	plan, ok := rotationPlanForAccount(current)
	if !ok {
		// 错误面与手动刷新 handler 的 rotatable 门同口径（routes.go：命中
		// vendor+type 但协议族不可轮换时按 plan.label 渲染）；plan 未命中保持
		// 原类型门文案。
		if plan.providerCode != "" {
			return nil, &ValidationError{Message: plan.label + " OAuth 账户不存在或无权操作"}
		}
		return nil, &ValidationError{Message: "账户不是支持自动刷新的 OAuth 类型"}
	}
	// blocked 守卫与手动刷新 handler（routes.go refreshToken）同款同口径：
	// OpenAI blocked 错误账户必须先走异常恢复，自动刷新不得绕过。
	if plan.slug == "openai" && isOpenAIBlockedErrorAccount(current) {
		return nil, &ValidationError{Message: "异常账户请先执行异常恢复后再操作"}
	}
	// 存量刷新走账户绑定代理（resolveRefreshProxyUrlOrThrow 对应物，与
	// refreshToken handler 同语义）：配置不可用直接报错，不做直连回退。
	if plan.requiresProxy && s.proxyRequired && strings.TrimSpace(current.ProxyProfileID) == "" {
		return nil, &ValidationError{Message: plan.label + " OAuth 账户未绑定代理，无法刷新：请先为账户配置出海代理"}
	}
	proxyURL, err := s.proxyProfileRequestURL(ctx, current.ProxyProfileID)
	if err != nil {
		return nil, err
	}
	tokenCredentials, err := plan.refreshStored(ctx, s, current, proxyURL)
	if err != nil {
		return nil, &AutoRefreshUpstreamError{Err: err}
	}
	result, err := s.RotateCredentials(ctx, RotateCredentialsInput{
		AccountID:              current.ID,
		ExpectedConfigRevision: current.ConfigRevision,
		ExpectedProviderCode:   plan.providerCode,
		ExpectedAccountType:    plan.accountType,
		Credentials:            mergeRotationCredentials(current.Credentials, tokenCredentials, plan.preserveBaseURL),
		Access:                 access,
	})
	if err != nil {
		// revision CAS 冲突 = 他人已刷新：按 already-current 语义视同成功，
		// 调用方重新水合后拿到的就是库内新凭据。
		var conflict *RevisionConflictError
		if errors.As(err, &conflict) {
			return &AutoRefreshResult{AlreadyCurrent: true, ConfigRevision: current.ConfigRevision}, nil
		}
		return nil, err
	}
	if result == nil {
		return nil, &ValidationError{Message: "OAuth 账户不存在或身份已变化"}
	}
	return &AutoRefreshResult{
		Rotated:        result.Changed,
		AlreadyCurrent: !result.Changed,
		ConfigRevision: result.ConfigRevision,
		UpdatedAt:      result.UpdatedAt,
	}, nil
}

// rotationPlanForAccount 按 vendor_code（§2.1 行为键，真实厂商 == provider_code）
// + account type 匹配四家 plan，并复用 handler 的 rotatable 门（vendor 身份 +
// 账户类型 + 协议族一致才可轮换，routes.go refreshToken 臂同款）。非 OAuth
// 类型 / 未接入厂商返回 (零值, false)；命中 vendor+type 但协议族不匹配返回
// (命中 plan, false)——调用方据 plan 非零渲染 handler 同口径错误面。
func rotationPlanForAccount(account *rotationAccount) (providerPlan, bool) {
	for _, plan := range providerPlans() {
		if plan.providerCode == account.ProviderCode && plan.accountType == account.Type {
			return plan, plan.rotatable(account)
		}
	}
	return providerPlan{}, false
}
