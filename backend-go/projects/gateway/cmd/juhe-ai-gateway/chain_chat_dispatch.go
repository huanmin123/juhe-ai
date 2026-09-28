package main

// AI 问答会话账户唯一绑定的进程内调度覆盖通道（AI 问答会话账户唯一绑定设计
// §5/§7；原三种绑定模式的调度通道随 group/api_key 模式退场收敛为单一路径）。
//
// 通道形态：chat.GenerationExecutor 的生产实现 chatGatewayExecutor 在会话绑定
// 账户后经 WithChatDispatchAccount 携带目标，Dispatch 时把目标注入请求
// context（key 为本包未导出结构体类型）。外部 HTTP 请求的 context 由
// net/http 服务端构造，不可能携带该未导出 key，因此不存在伪造通道；目标也
// 不经任何 header / 查询参数 / 请求体字段承载。
//
// /v1 链消费点（handleOpenAIGatewayRequest）：
//  1. 派发候选装配前：把 preflight 的原始候选窗口收敛到绑定作用域——按账户
//     当前启用分组（accounts.FindChatAccount 的 EnabledGroupIDs 同口径）逐组
//     解析并收敛为该账户单元素。候选走既有 FilterCandidates /
//     PrepareDispatchAccounts 管线，模型过滤、优先级、抑制、并发槽语义不变；
//     候选为空走既有"无可用账户"错误分支。
//  2. DispatchContext 装配后、派发循环前：GroupSchedulingPolicy 与
//     UsageContext.GroupID 对齐到生效分组（承载分组，即派发命中组/BoundGroupID
//     口径）。使用记录仍由 applyUsageAccountScope 按实际派发账户的
//     BoundGroupID 记账，此处只影响窗口级上下文。
//  3. 派发循环（v1DispatchLoop）：携带目标时禁用 API Key 分组回退切换，耗尽
//     走既有"无可用账户"终态，绑定会话永不逃逸出绑定作用域。
//
// target 为空（外部请求）时三条路径全部短路，调度行为与现状一致。未绑定/
// 归档会话在 internal/chat 发送预检拦截，不会进入生成链。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// chatDispatchTargetContextKey 是调度覆盖目标的 context key。未导出结构体
// 类型保证包外无法构造同名 key，外部 HTTP 请求无法注入目标。
type chatDispatchTargetContextKey struct{}

// chatDispatchTarget 是一次聊天派发的绑定目标：仅携带会话绑定的账户 ID——
// 会话不持久化承载分组，账户承载分组由 /v1 消费侧按账户当前启用分组解析
// （EnabledGroupIDs 同口径）。
type chatDispatchTarget struct {
	AccountID string
}

// pinned 报告目标是否要求调度覆盖（绑定账户非空）。
func (t chatDispatchTarget) pinned() bool {
	return t.AccountID != ""
}

// contextWithChatDispatchTarget 把绑定目标挂到 context 上（chatGatewayExecutor
// Dispatch 的唯一生产调用点）。
func contextWithChatDispatchTarget(ctx context.Context, target chatDispatchTarget) context.Context {
	return context.WithValue(ctx, chatDispatchTargetContextKey{}, target)
}

// chatDispatchTargetFromContext 读取绑定目标；context 未携带或目标不要求
// 覆盖时返回 ok=false（外部请求同一路径）。
func chatDispatchTargetFromContext(ctx context.Context) (chatDispatchTarget, bool) {
	if ctx == nil {
		return chatDispatchTarget{}, false
	}
	target, ok := ctx.Value(chatDispatchTargetContextKey{}).(chatDispatchTarget)
	if !ok || !target.pinned() {
		return chatDispatchTarget{}, false
	}
	return target, true
}

// ---------------------------------------------------------------------------
// 绑定账户承载分组解析端口（进程级装配槽）
// ---------------------------------------------------------------------------

// chainChatDispatchAccountGroups 解析账户当前启用分组列表（accounts.
// FindChatAccount 的 EnabledGroupIDs 同口径：enabled=1 的 group_accounts 绑定，
// 确定性排序）。第二返回值 false = 账户未解析（不存在/已删除/查询失败），
// 调用方按空启用分组处理（空候选 → 既有"无可用账户"语义）。
//
// 组合根（composeChatFamily）在装配期置位一次，此后只读；与
// chain_obs_wiring.go 的进程级观察槽同样遵守"装配期置位、运行期只读"纪律
// （观察槽用 atomic.Pointer，本槽因回调需测试还原用 RWMutex）。nil = 未装配
// （空候选）。
var (
	chainChatDispatchAccountGroupsMu sync.RWMutex
	chainChatDispatchAccountGroups   func(accountID string) ([]string, bool)
)

// setChainChatDispatchAccountGroups 装配承载分组解析端口（组合根唯一置位点；
// 测试可显式置位与还原）。
func setChainChatDispatchAccountGroups(resolve func(accountID string) ([]string, bool)) {
	chainChatDispatchAccountGroupsMu.Lock()
	defer chainChatDispatchAccountGroupsMu.Unlock()
	chainChatDispatchAccountGroups = resolve
}

// chainChatDispatchAccountGroupsOf 读取账户启用分组列表。
func chainChatDispatchAccountGroupsOf(accountID string) ([]string, bool) {
	chainChatDispatchAccountGroupsMu.RLock()
	resolve := chainChatDispatchAccountGroups
	chainChatDispatchAccountGroupsMu.RUnlock()
	if resolve == nil {
		return nil, false
	}
	return resolve(accountID)
}

// chatDispatchTargetAwareExecutor 是 cmd 侧执行器覆盖端口的最小断言面
// （与 internal/chat 的 chatDispatchTargetAware 同形），观察适配器据此在
// 派发前绑定目标。
type chatDispatchTargetAwareExecutor interface {
	WithChatDispatchAccount(accountID string) chat.GenerationExecutor
}

// chatConversationDispatchTarget 读会话绑定账户（chat 观察适配器的只读查询：
// 压缩服务在 internal/chat 内经 Store 解析，观察适配器在 cmd 侧无 Store
// 端口，直接读同库同表）。返回会话是否存在。
func chatConversationDispatchTarget(db *sql.DB, table string, bind func(string) string, conversationID, systemAccountID string) (chatDispatchTarget, bool, error) {
	query := bind(`SELECT COALESCE(bind_account_id, '')
		FROM ` + table + ` WHERE id = ? AND system_account_id = ? LIMIT 1`)
	var accountID string
	if err := db.QueryRow(query, conversationID, systemAccountID).Scan(&accountID); err != nil {
		return chatDispatchTarget{}, false, err
	}
	return chatDispatchTarget{AccountID: accountID}, true, nil
}

// chatDispatchTargetScope 是调度目标在派发候选装配前的解析结果：accounts 是
// 已收敛到绑定作用域的原始候选窗口（直接装配进 preflight 的
// CandidateAccounts，走既有候选管线）；groupID 是生效分组（承载分组，即候选
// 命中组/BoundGroupID 口径；空=解析不出，仅影响窗口组展示，空候选已走既有
// 无可用账户语义）；recoverableScope 是注入候选的可恢复等待作用域（可靠性
// 批次2 缺陷5：账户启用分组全集+目标账户），随候选一起注入 preflight。
type chatDispatchTargetScope struct {
	groupID          string
	accounts         []gatewayruntimecache.OpenAIAccountSecret
	recoverableScope *gatewaypreauth.CandidateRecoverableScope
}

// resolveChatDispatchTargetScope 在派发候选装配前按绑定账户解析生效分组并
// 收敛原始候选窗口：按账户启用分组逐组解析、按账户 ID 收敛为单元素（多启用
// 分组时任一组解析都可收敛，取第一命中组），生效分组 = 命中组（与该候选的
// BoundGroupID 同口径）；无命中时回落第一启用组、空候选走既有"无可用账户"
// 语义。
//
// 运行时身份缺失时返回错误（fail-closed：宁可失败也不脱离绑定作用域派发）。
func (c *gatewayChain) resolveChatDispatchTargetScope(ctx context.Context, req *gatewaypreauth.GatewayRequest, target chatDispatchTarget) (chatDispatchTargetScope, error) {
	systemAccountID := chatDispatchSystemAccountID(req)
	if systemAccountID == "" {
		return chatDispatchTargetScope{}, errors.New("聊天调度目标缺少运行时身份，无法收敛派发作用域")
	}
	model, _ := gatewaypreauth.RequestModel(req)
	enabledGroups, _ := chainChatDispatchAccountGroupsOf(target.AccountID)
	narrowed := []gatewayruntimecache.OpenAIAccountSecret{}
	hitGroup := ""
	for _, groupID := range enabledGroups {
		accounts, err := c.preauth.RuntimeCache.ListCachedOpenAIAccountsForGroupAsync(ctx, groupID, systemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{
			RequestedModel: model,
		})
		if err != nil {
			return chatDispatchTargetScope{}, fmt.Errorf("收敛聊天调度目标候选失败: %w", err)
		}
		for _, account := range accounts {
			if account.ID != target.AccountID {
				continue
			}
			narrowed = append(narrowed, account)
			hitGroup = groupID
			break
		}
		if len(narrowed) > 0 {
			// 收敛为指定账户单元素：第一命中组的候选即派发目标（该账户可属
			// 多个启用分组，跨组解析同账户一致，任一组均可收敛）。
			break
		}
	}
	if hitGroup == "" && len(enabledGroups) > 0 {
		// 启用分组都未解析出该账户（如模型过滤后不可派发）：窗口组回落第一
		// 启用组（仅影响展示/失败记录窗口组），空候选走既有"无可用账户"语义。
		hitGroup = enabledGroups[0]
	}
	return chatDispatchTargetScope{
		groupID:  hitGroup,
		accounts: narrowed,
		// 可恢复等待作用域取账户启用分组全集+目标账户：目标账户恢复（冷却
		// 结束回到任一启用分组的可用窗口）即可被重新收敛为候选。
		recoverableScope: &gatewaypreauth.CandidateRecoverableScope{GroupIDs: enabledGroups, AccountID: target.AccountID},
	}, nil
}

// applyChatDispatchGroupContext 把 DispatchContext 的窗口级分组上下文对齐到
// 生效分组：调度策略（含 weighted/round_robin 动态模式）与 usage/审计窗口组
// 取生效分组的 access 元数据。使用记录的最终归属仍由 applyUsageAccountScope
// 按实际派发账户的 BoundGroupID 记账，本函数不改 usage 记账路径。groupID 为
// 空（解析不出承载分组）或分组 access 缺失时静默保持现状；access 解析失败
// 返回错误（与 preflight 缓存读失败同一处理面）。
func (c *gatewayChain) applyChatDispatchGroupContext(ctx context.Context, systemAccountID, groupID string, context *gatewaypreauth.DispatchContext) error {
	if context == nil || groupID == "" {
		return nil
	}
	groupAccess, err := c.preauth.RuntimeCache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, groupID, systemAccountID)
	if err != nil {
		return fmt.Errorf("解析聊天调度目标分组上下文失败: %w", err)
	}
	if groupAccess == nil {
		return nil
	}
	context.GroupSchedulingPolicy = groupAccess.SchedulingPolicy
	context.UsageContext.GroupID = groupID
	return nil
}

// chatDispatchSystemAccountID 取请求的归属系统账户（preauth 已解析的运行时
// 快照；缺失时返回空串，由调用方 fail-closed）。
func chatDispatchSystemAccountID(req *gatewaypreauth.GatewayRequest) string {
	if req == nil || req.Runtime == nil || req.Runtime.APIKey == nil {
		return ""
	}
	return req.Runtime.APIKey.SystemAccountID
}
