package gatewaydispatch

// 切号时有效上游目标（SwitchTarget）与 target-aware 切号过滤。
//
// 契约：docs/functions/切号时有效上游目标与上下文迁移设计.md。一旦本请求
// 有账户候选完成 mapping 解析与上游请求构造（构造入口在 cmd 链路层
// chain_driver 的 BuildGatewayUpstreamRequestParts），就必须冻结该账户的
// 有效上游目标；此后所有换到不同账户的候选都必须能承接同一目标，候选筛选
// 不再按客户端请求的模型 / 协议族推导。初始路由的候选筛选完全不变：冻结
// 目标不存在时本文件的过滤逻辑根本不参与。
//
// 冻结输入与过滤输入同源：冻结时记录构造侧 mapping 解析实际使用的客户端
// 模型与协议族（cmd 层 requestMappingSourceFamilyOf 词表），各消费点的过滤
// 输入一律取自冻结快照，保证同一请求内冻结与过滤用同一族词表。

import (
	"context"
	"strings"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// SwitchTarget 是请求级、不可变的上游目标快照（设计文档 §2.1）。contextContract
// 字段暂缓（ConvertedContext 独立结构化同为暂缓项）。
type SwitchTarget struct {
	// ProviderCode 是冻结时账户的供应商；非 Chat 目标用于同供应商硬约束。
	ProviderCode string
	// ProviderProtocolProfileID 是冻结时账户的协议 profile；本次不做额外
	// 判定（同供应商内 profile 差异由既有链路处理）。
	ProviderProtocolProfileID string
	// UpstreamModel 是有效上游模型（比较用 EqualFold，转发保留原拼写）。
	UpstreamModel string
	// UpstreamEndpointFamily 是上游协议族（chat_completions / responses /
	// messages / generate_content 等，与模型映射 RHS 同词表）。
	UpstreamEndpointFamily string
	// UpstreamEndpointMode 是精确 JSON / SSE 形态（chat_json / chat_sse /
	// responses_json / responses_sse 等）。
	UpstreamEndpointMode string
}

// unresolved 报告冻结目标的不变量违规（设计文档 §6「target 无法解析」行）：
// 冻结目标必须能证明实际上游模型与协议族，缺失即不可解析，消费点 fail-closed。
// 构造侧协议族词表（requestMappingSourceFamilyOf）恒非空，空 family 只出现在
// 载体未按契约填充的异常路径。
func (t *SwitchTarget) unresolved() bool {
	if t == nil {
		return true
	}
	return strings.TrimSpace(t.UpstreamModel) == "" || strings.TrimSpace(t.UpstreamEndpointFamily) == ""
}

// SwitchTargetFreezeInput 是冻结输入：目标快照 + 构造侧实际使用的客户端请求
// 上下文（作为消费点过滤的 LHS 依据，与构造侧 mapping 解析同源）。
type SwitchTargetFreezeInput struct {
	// SourceAccountID 是首个完成上游请求构造的账户。
	SourceAccountID string
	// Target 是冻结的有效上游目标。
	Target *SwitchTarget
	// ClientRequestedModel 是构造侧 mapping 解析实际使用的客户端请求模型。
	ClientRequestedModel string
	// ClientSourceEndpointFamily 是构造侧 mapping 解析实际使用的客户端协议族。
	ClientSourceEndpointFamily string
}

// SwitchTargetSnapshot 是冻结状态的只读视图。
type SwitchTargetSnapshot struct {
	SourceAccountID            string
	Target                     *SwitchTarget
	ClientRequestedModel       string
	ClientSourceEndpointFamily string
	Frozen                     bool
}

// SwitchTargetFilterInput 是过滤函数的输入：冻结目标 + 客户端请求侧的模型 /
// 协议族（仅用于解析候选映射的 LHS，不参与资格判断）。
type SwitchTargetFilterInput struct {
	Target                     SwitchTarget
	ClientRequestedModel       string // 客户端请求模型（切号重建的 LHS 依据）
	ClientSourceEndpointFamily string // 客户端请求协议族
}

// FilterAccountsForSwitchTarget 按冻结目标过滤候选账户（纯函数，设计文档 §4
// 硬约束）。候选保留需同时满足：
//  1. mode 门：候选声明了 SupportedEndpointModes 时必须包含冻结的精确 mode
//     （候选空 modes = 不受限；目标 mode 为空 = 本次请求形状无闸门 mode，
//     两者都保持既有 D-155 语义不改变）；
//  2. 供应商门：非 Chat 目标禁止跨供应商（Responses / messages / gemini 等
//     上游状态不外流）；Chat 目标允许跨供应商；
//  3. 承接门：候选既有别名解析命中时，别名 RHS 必须 EqualFold 命中冻结模型
//     且协议族一致；无别名（直连）时仅当冻结目标为原生形态（目标协议族 ==
//     构造侧客户端协议族）且候选直接持有该模型时保留——防止桥接后的 Chat
//     目标被发给只能按客户端形态重建的直连候选。
func FilterAccountsForSwitchTarget(accounts []AccountCandidate, in SwitchTargetFilterInput) []AccountCandidate {
	kept := make([]AccountCandidate, 0, len(accounts))
	for _, account := range accounts {
		if accountServesSwitchTarget(account, in) {
			kept = append(kept, account)
		}
	}
	return kept
}

// accountServesSwitchTarget 判定单个候选能否承接冻结目标。
func accountServesSwitchTarget(account AccountCandidate, in SwitchTargetFilterInput) bool {
	target := in.Target
	// 门 1：精确 mode。
	if len(account.SupportedEndpointModes) > 0 && strings.TrimSpace(target.UpstreamEndpointMode) != "" {
		modeMatched := false
		for _, supported := range account.SupportedEndpointModes {
			if supported == target.UpstreamEndpointMode {
				modeMatched = true
				break
			}
		}
		if !modeMatched {
			return false
		}
	}
	// 门 2：供应商——Responses 等非 Chat 目标锁定冻结时供应商。
	if !strings.EqualFold(strings.TrimSpace(target.UpstreamEndpointFamily), gatewayrouting.EndpointFamilyChatCompletions) {
		if !strings.EqualFold(strings.TrimSpace(account.ProviderCode), strings.TrimSpace(target.ProviderCode)) {
			return false
		}
	}
	// 门 3：承接——复用初始路由同款别名解析器证明候选可达冻结目标。
	mapping := resolveAccountModelMapping(account, in.ClientRequestedModel, in.ClientSourceEndpointFamily)
	if mapping != nil {
		return strings.EqualFold(strings.TrimSpace(mapping.UpstreamModel), strings.TrimSpace(target.UpstreamModel)) &&
			mapping.UpstreamEndpointFamily == target.UpstreamEndpointFamily
	}
	// 无别名（直连）：仅当冻结目标为原生形态（未跨协议桥接）且候选直接持有
	// 该模型。
	if target.UpstreamEndpointFamily != in.ClientSourceEndpointFamily {
		return false
	}
	return accountSupportsUpstreamModelEqualFold(account.SupportedModels, target.UpstreamModel)
}

// accountSupportsUpstreamModelEqualFold 按模型逻辑名大小写不敏感地匹配直持
// 模型（设计文档 §2.1：候选可用自己的规范拼写发送，逻辑模型相同即可）。
func accountSupportsUpstreamModelEqualFold(supportedModels []string, upstreamModel string) bool {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return false
	}
	for _, model := range supportedModels {
		if strings.EqualFold(strings.TrimSpace(model), upstreamModel) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 请求级冻结载体
// ---------------------------------------------------------------------------

// SwitchTargetCapture 是请求级的冻结目标载体：首个完成上游请求构造的候选
// 冻结一次，随后续尝试、账户切换与分组回退保持不变（设计文档 §3.1 / §6）。
// 零值可用；经 WithSwitchTargetCapture 注入请求 ctx 后，构造入口
// （cmd 链路层 driver）与各切号消费点读取同一实例。
type SwitchTargetCapture struct {
	mu                  sync.Mutex
	snapshot            SwitchTargetSnapshot
	unresolvedDiagnosed bool
}

// Freeze 记录首次完成构造的冻结目标及其构造侧客户端上下文；后续调用为空
// 操作（每请求只捕获一次，不随后续尝试重置）。
func (c *SwitchTargetCapture) Freeze(input SwitchTargetFreezeInput) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snapshot.Frozen {
		return
	}
	c.snapshot = SwitchTargetSnapshot{
		SourceAccountID:            input.SourceAccountID,
		Target:                     input.Target,
		ClientRequestedModel:       strings.TrimSpace(input.ClientRequestedModel),
		ClientSourceEndpointFamily: strings.TrimSpace(input.ClientSourceEndpointFamily),
		Frozen:                     true,
	}
}

// Snapshot 返回冻结状态视图：Frozen=false 表示本请求尚无账户完成构造。
func (c *SwitchTargetCapture) Snapshot() SwitchTargetSnapshot {
	if c == nil {
		return SwitchTargetSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot
}

// MarkUnresolvedDiagnosed 在冻结目标不可解析时返回一次 true（每请求只输出
// 一条 switch_target_unresolved 诊断）。
func (c *SwitchTargetCapture) MarkUnresolvedDiagnosed() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unresolvedDiagnosed {
		return false
	}
	c.unresolvedDiagnosed = true
	return true
}

type switchTargetCaptureContextKey struct{}

// WithSwitchTargetCapture 把请求级冻结载体注入 ctx（cmd 链路层在派发前调用）。
func WithSwitchTargetCapture(ctx context.Context, capture *SwitchTargetCapture) context.Context {
	if ctx == nil || capture == nil {
		return ctx
	}
	return context.WithValue(ctx, switchTargetCaptureContextKey{}, capture)
}

// WithoutSwitchTargetCapture 从 ctx 剥掉请求级冻结载体：供内部合成 / 辅助
// / 打分类派发（如混合路由打分的 DispatchHybridAuxiliaryChatCompletion）在
// 主请求 ctx 上构造上游请求时使用——它们不是客户端请求的上游尝试，不得
// 抢先冻结主请求目标。剥离后冻结入口与消费点门均恢复惰性。
func WithoutSwitchTargetCapture(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, switchTargetCaptureContextKey{}, (*SwitchTargetCapture)(nil))
}

// SwitchTargetCaptureFromContext 返回请求级冻结载体；absent 或已被剥离
// （初始路由、探针、内部辅助派发）时返回 nil，冻结与过滤完全不参与。
func SwitchTargetCaptureFromContext(ctx context.Context) *SwitchTargetCapture {
	if ctx == nil {
		return nil
	}
	capture, _ := ctx.Value(switchTargetCaptureContextKey{}).(*SwitchTargetCapture)
	return capture
}

// ---------------------------------------------------------------------------
// 消费点统一门
// ---------------------------------------------------------------------------

// SwitchTargetGate 是一个切号消费点（引擎账户推进、流式重试重派窗口、分组
// 回退候选、模型感知 reload）看到的冻结目标视图。与冻结源同账户的候选不是
// 切换点（同账户 API Key 轮换 / 同账户重试语义不变），无条件保留；其余候选
// 必须通过 FilterAccountsForSwitchTarget 三门。过滤输入（客户端模型 / 协议
// 族）取自冻结快照，与构造侧同源。
type SwitchTargetGate struct {
	capture *SwitchTargetCapture
}

// SwitchTargetGateFromContext 从请求 ctx 构建消费点门；ctx 未注入冻结载体
// （或已被剥离）时返回 nil（过滤惰性）。
func SwitchTargetGateFromContext(ctx context.Context) *SwitchTargetGate {
	capture := SwitchTargetCaptureFromContext(ctx)
	if capture == nil {
		return nil
	}
	return &SwitchTargetGate{capture: capture}
}

func (g *SwitchTargetGate) snapshot() SwitchTargetSnapshot {
	if g == nil {
		return SwitchTargetSnapshot{}
	}
	return g.capture.Snapshot()
}

// Frozen 报告本请求是否已有账户完成构造（冻结发生）。
func (g *SwitchTargetGate) Frozen() bool {
	return g.snapshot().Frozen
}

// Unresolved 报告冻结目标是否不可解析（fail-closed 条件）。
func (g *SwitchTargetGate) Unresolved() bool {
	snapshot := g.snapshot()
	return snapshot.Frozen && snapshot.Target.unresolved()
}

// MarkUnresolvedDiagnosed 每请求一次返回 true，调用方据此输出
// switch_target_unresolved 结构化诊断。
func (g *SwitchTargetGate) MarkUnresolvedDiagnosed() bool {
	if g == nil {
		return false
	}
	return g.capture.MarkUnresolvedDiagnosed()
}

// SwitchTargetGateSourceOf 返回冻结源账户 ID（诊断字段）；未冻结时为空串。
func SwitchTargetGateSourceOf(gate *SwitchTargetGate) string {
	snapshot := gate.snapshot()
	if !snapshot.Frozen {
		return ""
	}
	return snapshot.SourceAccountID
}

// FilterAccounts 对候选窗口做冻结目标后置过滤。过滤后为空时调用方走既有
// 最终失败 / 兜底路径，不得放宽、不得回退客户端协议筛选。
func (g *SwitchTargetGate) FilterAccounts(accounts []AccountCandidate) []AccountCandidate {
	if g == nil {
		return accounts
	}
	snapshot := g.snapshot()
	if !snapshot.Frozen {
		return accounts
	}
	input := SwitchTargetFilterInput{
		ClientRequestedModel:       snapshot.ClientRequestedModel,
		ClientSourceEndpointFamily: snapshot.ClientSourceEndpointFamily,
	}
	if snapshot.Target != nil {
		input.Target = *snapshot.Target
	}
	kept := make([]AccountCandidate, 0, len(accounts))
	for _, account := range accounts {
		if account.ID == snapshot.SourceAccountID || accountServesSwitchTarget(account, input) {
			kept = append(kept, account)
		}
	}
	return kept
}
