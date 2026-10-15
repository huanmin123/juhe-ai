package main

import (
	"context"
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// Gemini interactions 亲和写面接线（审计 P1：G04 迁移时链面零调用）。
//
// Node 契约（finalization.ts:483/:905/:1054 rememberGeminiInteractionBeforeDownstreamCommit
// / deleteGeminiInteractionBeforeDownstreamCommit）：创建（2xx）与 DELETE（2xx）
// 在**下游提交前**写亲和；写失败记录日志与审计元数据后上抛
// GeminiInteractionAffinityUnavailableError（routes.ts:1621 rethrow），请求以
// 503 / code interaction_affinity_unavailable 终止、响应不提交。
//
// 修复前：创建成功不写映射、DELETE 成功不移除映射，所有资源访问
// （GET/DELETE/cancel）在 preflight 亲和解析处恒 404
// interaction_affinity_not_found（违反《Gemini账号接入.md》:191-192 契约）。

// interactionAffinityRememberBeforeCommit 构造创建成功的提交前亲和写入回调。
// 门（对齐 Node）：interactions create 请求 + 上游 2xx + gemini 账户 + 亲和已
// 装配；任一不满足返回 nil（响应层仅在非 nil 时接线为管道提交前钩子）。
func (c *gatewayChain) interactionAffinityRememberBeforeCommit(
	ctx context.Context,
	req *gatewaypreauth.GatewayRequest,
	context *gatewaypreauth.DispatchContext,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	auditCapture gatewaypreauth.AuditCaptureContext,
) func(responseResourceId string) error {
	if c.preauth == nil || c.preauth.Affinity == nil || req == nil || req.HTTP == nil {
		return nil
	}
	if !gatewaygemini.IsInteractionCreateRequest(req.HTTP) {
		return nil
	}
	if dispatched.Response == nil || !dispatched.Response.OK() {
		return nil
	}
	if dispatched.Account.ProviderCode != gatewaygemini.ProviderCode {
		return nil
	}
	account := gatewaygemini.UpstreamAccount{
		ID:                        dispatched.Account.ID,
		Type:                      dispatched.Account.Type,
		ProviderCode:              dispatched.Account.ProviderCode,
		ProviderProtocolProfileID: dispatched.Account.ProviderProtocolProfileID,
	}
	scope := interactionAffinityScopeOf(context)
	return func(responseResourceId string) error {
		result, err := c.preauth.Affinity.Remember(ctx, responseResourceId, account, scope)
		if err != nil {
			c.recordInteractionAffinityWriteFailure(auditCapture, "remember", account.ID, scope.GroupID, err)
			return err
		}
		c.recordInteractionAffinityMutation(auditCapture, result)
		return nil
	}
}

// deleteInteractionAffinityBeforeCommit 在 DELETE 2xx 的下游写入前移除亲和
// （Node finalization.ts:905，ID 取自请求路径）。错误上抛后由
// handleUpstreamResponse 的 503 分支按 AffinityUnavailableError 渲染。
func (c *gatewayChain) deleteInteractionAffinityBeforeCommit(
	ctx context.Context,
	req *gatewaypreauth.GatewayRequest,
	context *gatewaypreauth.DispatchContext,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	auditCapture gatewaypreauth.AuditCaptureContext,
) error {
	if c.preauth == nil || c.preauth.Affinity == nil || req == nil || req.HTTP == nil {
		return nil
	}
	if req.MethodUpper() != "DELETE" || !gatewaygemini.IsInteractionResourceRequest(req.HTTP) {
		return nil
	}
	if dispatched.Response == nil || !dispatched.Response.OK() {
		return nil
	}
	scope := interactionAffinityScopeOf(context)
	result, err := c.preauth.Affinity.Delete(ctx, gatewaygemini.ResourceIDFromRequest(req.HTTP), scope)
	if err != nil {
		c.recordInteractionAffinityWriteFailure(auditCapture, "delete", dispatched.Account.ID, scope.GroupID, err)
		return err
	}
	c.recordInteractionAffinityMutation(auditCapture, result)
	return nil
}

func interactionAffinityScopeOf(context *gatewaypreauth.DispatchContext) gatewaygemini.AffinityScope {
	return gatewaygemini.AffinityScope{
		SystemAccountID: context.UsageContext.SystemAccountID,
		APIKeyID:        context.UsageContext.APIKeyID,
		GroupID:         context.UsageContext.GroupID,
	}
}

// recordInteractionAffinityMutation 成功写入的审计元数据（Node
// auditCapture.addGatewayMetadata label='gemini_interaction_account_affinity_update'，
// metadata 为 mutation 展开 + commitPhase）。
func (c *gatewayChain) recordInteractionAffinityMutation(auditCapture gatewaypreauth.AuditCaptureContext, result gatewaygemini.AffinityMutationResult) {
	if auditCapture == nil || result.Action == gatewaygemini.AffinityActionNone {
		return
	}
	auditCapture.AddGatewayMetadata("gemini_interaction_account_affinity_update", map[string]any{
		"action":        string(result.Action),
		"interactionId": result.InteractionID,
		"commitPhase":   "before_downstream_commit",
	})
}

// recordInteractionAffinityWriteFailure 写失败的日志与审计元数据（Node
// recordGeminiInteractionAffinityPersistenceFailure：日志字段按原始存储错误
// 记录，审计元数据仅 operation/accountId）。
func (c *gatewayChain) recordInteractionAffinityWriteFailure(auditCapture gatewaypreauth.AuditCaptureContext, operation, accountID, groupID string, err error) {
	if auditCapture != nil {
		auditCapture.AddGatewayMetadata("gemini_interaction_account_affinity_update_failed", map[string]any{
			"operation": operation,
			"accountId": accountID,
		})
	}
	c.observability.Logger().Warn("gateway_gemini_interaction_affinity_update_failed", map[string]any{
		"event":     "gateway_gemini_interaction_affinity_update_failed",
		"operation": operation,
		"accountId": accountID,
		"groupId":   groupID,
		"error":     affinityWriteFailureCause(err).Error(),
	}, "Gemini Interaction 账号亲和状态更新失败")
}

// affinityWriteFailureCause 对齐 Node recordGeminiInteractionAffinityPersistenceFailure
// 的 originalError 解包：日志记原始存储错误，而非对外 503 文案。
func affinityWriteFailureCause(err error) error {
	var unavailable *gatewaygemini.AffinityUnavailableError
	if errors.As(err, &unavailable) && unavailable.Err != nil {
		return unavailable.Err
	}
	return err
}
