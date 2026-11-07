package main

// API Key 安全策略出站脱敏的请求链单点接入（API Key 安全策略与出站内容脱敏
// 设计 §2/§5/§9，批B2）。落点：/v1 编排链 body 定型（req.Body = bodyReq）之
// 后、audit capture 创建之前——脱敏执行一次，此后的多账户重试与协议桥接都
// 从 sanitized body 派生（§2「脱敏执行一次，在入口 body 定型后」），占位符
// 全链路一致。
//
// 传导通道（与任务契约的取证锚点一致）：
//   - 主通道 UpstreamBodyCache.PassthroughBody：clientUpstreamBody
//     （chain_driver.go）优先消费该缓存，回落 RawBody——sanitized 字节经既有
//     零冲突通道到达上游（Replace 前该缓存无生产者）。
//   - 读物化对象分支：codex OAuth 归一化
//     （gatewayoauthcodex.ParseOpenAIOAuthCodexJsonObjectBody）读
//     req.Body.Body 物化对象、不走 clientUpstreamBody——Applied 时以
//     sanitized 字节重建物化对象（不动 RawBody / State / Serialized）。
//   - RawBody 保持客户端原文（硬约束）：审计 client_request 捕获与内容指纹
//     （preflight 按 RawBody 计算，设计 §6.2）都依赖原文。
//
// 零开销路径：未绑定 / 内置默认行 / 空规则集（Empty policy）直接返回，现有
// 行为逐字节不变（验收 1）。系统提示注入已由 SanitizeBody 内部处理
// （HasPlaceholder 时，设计 §6.2），链路层不重复。

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysecpol"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/securitypolicies"
)

// applySecurityPolicyIfNeeded 对绑定了非默认安全策略的请求执行出站脱敏：
// 编译缓存取 CompiledPolicy → 判定 endpoint family → SanitizeBody → sanitized
// body 挂 UpstreamBodyCache 并重建读物化对象 → 执行事实挂 req.SecurityPolicy。
// 返回 error 仅来自编译缓存对损坏规则集的显式上抛（正常流程快照装载期已校
// 验，这里是同一入口的防御性复用）。
func applySecurityPolicyIfNeeded(req *gatewaypreauth.GatewayRequest, traceID string) error {
	if req == nil || req.Body == nil || req.Runtime == nil || req.Runtime.APIKey == nil {
		return nil
	}
	row := req.Runtime.APIKey
	// 零开销路径：未绑定（NULL 列）或显式绑定内置默认行 = 请求原样转发
	// （设计 §1.1 目标 4）。
	if row.SecurityPolicyID == "" || row.SecurityPolicyID == securitypolicies.BuiltinSecurityPolicyID {
		return nil
	}
	policy, err := gatewayruntimecache.CompiledPolicyForKeyRow(row)
	if err != nil {
		return err
	}
	// 空规则集（自定义空策略 / 内置行原文 {"rules":[]}）= 不处理 = 零开销路径。
	if policy.Empty() {
		return nil
	}
	family := securityPolicyFamilyForRequest(req)
	state := &gatewaypreauth.SecurityPolicyRequestState{
		PolicyID: row.SecurityPolicyID,
		Family:   family,
	}
	req.SecurityPolicy = state
	if family == "" {
		// 无法判定的 endpoint family：fail-open 跳过扫描（§9.4 宁可漏拦、不
		// 断流），绑定事实照记（§8 security_policy_applied 与命中无关）；debug
		// 日志只含判定结果与 traceId，不含任何内容。
		slog.Debug("安全策略 endpoint family 无法判定，跳过出站脱敏",
			"event", "gateway_security_policy_family_unresolved",
			"traceId", traceID,
			"policyId", row.SecurityPolicyID)
		return nil
	}
	result := gatewaysecpol.SanitizeBody(policy, req.Body.RawBody, family)
	state.Applied = result.Applied
	state.Hits = result.Hits
	state.TotalHits = result.TotalHits
	state.Skips = result.Skips
	state.Mapping = result.Mapping
	if result.Skips > 0 {
		// §5 fail-open 跳过计数：只记 count 与 traceId，不含任何内容。
		slog.Debug("安全策略出站脱敏存在 fail-open 跳过节点",
			"event", "gateway_security_policy_skip",
			"traceId", traceID,
			"count", result.Skips)
	}
	if !result.Applied {
		return nil
	}
	// sanitized body 进零冲突通道（clientUpstreamBody 优先消费），RawBody 保持
	// 客户端原文不动（审计 client_request 与内容指纹契约）。
	req.Body.UpstreamBodyCache = &gatewaybody.UpstreamBodyCache{PassthroughBody: result.Body}
	// 读物化对象重建：codex OAuth 等读物化对象分支读到 sanitized 内容。RawBody
	// / State / Serialized 不动——State 描述的仍是 RawBody 原文事实（model /
	// service_tier / stream 等派生字段不在扫描字段清单内，值不受脱敏影响）。
	if sanitizedObject, ok := securityPolicySanitizedJSONObject(result.Body); ok {
		req.Body.Body = sanitizedObject
	}
	return nil
}

// securityPolicySanitizedJSONObject 把 sanitized body 解析回 JSON 对象（数字
// 以 json.Number 保字面，供读物化对象分支消费）。sanitized body 产自有序遍历
// 的可解析对象（Applied=true 的前提），解析失败理论不可达，返回 false 时保留
// 原读物化对象（fail-open，不阻断）。
func securityPolicySanitizedJSONObject(body []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

// securityPolicyFamilyForRequest 把请求映射到 gatewaysecpol 出站协议族（设计
// §5 五族）。判定来源与既有链路面同源：chat/responses 路径词表对齐 preflight
// 的 openAIEndpointFamilyFromPath，anthropic /v1/messages 与 gemini
// :generateContent 对齐 gatewayRequestEndpointFamily 的路径判定，媒体族用
// gatewaymedia.RequestLaneForPath（媒体路径族的唯一词表落点，媒体设计 §3）。
// 无法判定返回空串（调用方 fail-open 跳过扫描并记 debug 日志）。
func securityPolicyFamilyForRequest(req *gatewaypreauth.GatewayRequest) string {
	if req == nil || req.HTTP == nil {
		return ""
	}
	method := req.MethodUpper()
	path := strings.ToLower(strings.TrimSpace(gatewaypreauth.RequestPathWithoutQuery(req)))
	if path == "" {
		return ""
	}
	if method == http.MethodPost {
		// 媒体域优先：audio/video lane（/v1/audio/speech、/v1/videos、
		// /v1/audio/jobs 等）与图像生成的 prompt/text 类 JSON 出站体都判
		// media（STT 的 multipart 输入不是 JSON，SanitizeBody 顶层 fail-open）。
		if lane, known := gatewaymedia.RequestLaneForPath(method, path); known &&
			(lane == gatewayproto.LaneAudio || lane == gatewayproto.LaneVideo) {
			return gatewaysecpol.FamilyMedia
		}
		if strings.Contains(path, "/images/generations") {
			return gatewaysecpol.FamilyMedia
		}
	}
	if strings.Contains(path, "/chat/completions") {
		return gatewaysecpol.FamilyOpenAIChat
	}
	if strings.Contains(path, "/responses") {
		return gatewaysecpol.FamilyOpenAIResponses
	}
	if method == http.MethodPost && securityPolicyStripV1Prefix(path) == "/messages" {
		return gatewaysecpol.FamilyAnthropic
	}
	if method == http.MethodPost &&
		(strings.Contains(path, ":generatecontent") || strings.Contains(path, ":streamgeneratecontent")) {
		return gatewaysecpol.FamilyGemini
	}
	return ""
}

// securityPolicyStripV1Prefix 镜像 gatewaypreauth.stripV1Prefix（包内未导出，
// main 包按字面复刻以保持路径判定同源——chainCompatBlacklistIPMessage 先例）。
func securityPolicyStripV1Prefix(path string) string {
	if strings.HasPrefix(path, "/v1") && (len(path) == 3 || path[3] == '/') {
		return path[3:]
	}
	return path
}

// securityPolicyHitsOf 取请求级命中计数的副本（usage 记账构造点消费；nil 状
// 态 / nil 计数返回 nil，omitempty 不写 piiHits——设计 §8 仅命中数 > 0 时写）。
// 复制隔离：usage 记录异步落盘，与请求级状态不再共享底层 map。
func securityPolicyHitsOf(req *gatewaypreauth.GatewayRequest) map[string]int {
	if req == nil || req.SecurityPolicy == nil || len(req.SecurityPolicy.Hits) == 0 {
		return nil
	}
	out := make(map[string]int, len(req.SecurityPolicy.Hits))
	for label, count := range req.SecurityPolicy.Hits {
		out[label] = count
	}
	return out
}

// securityPolicyProtectionIncrTimeout 是防护次数终态 INCRBY 的 fire-and-forget
// 硬预算（安全策略防护次数计数，写后缓存设计 §4.3.1）：写侧在独立 goroutine
// 执行，本预算只约束单次 Redis 往返——超时即放弃本次计数（展示用途的运行态
// 增量，宁丢不阻塞）。
const securityPolicyProtectionIncrTimeout = 50 * time.Millisecond

// recordSecurityPolicyProtectionOnce 在请求终态处理入口（FinalizationDeps 注
// 入 usage 的同一构造点）执行防护次数 Redis INCRBY：当本请求 TotalHits（原始
// 命中总数，不去重——同请求同值出现 N 次计 N 次，设计 §4.3.1）> 0 时，对请
// 求绑定的 policyID 计数键累加。增量由 gateway 进程内刷写任务（protection
// flush）周期 GETDEL 合并进 security_policies.protection_count 列。
//
// 每请求至多一次：FinalizationDeps 构造点随多账户重试 / 多 attempt 每 attempt
// 进入一次，req.SecurityPolicy.ProtectionCounted 的 CAS 让首次到达的终态入口
// 执行 INCRBY、其余 attempt 直接返回——脱敏每请求一次，防护计数同请求只计
// 一次（不随重试重复）。
//
// fire-and-forget：独立 goroutine + 50ms 硬预算（请求 ctx 在响应结束后
// cancel，计数用 Background 派生 ctx），失败只在 ProtectionCountStore 内记
// debug 日志（事件 gateway_security_policy_hit_incr_failed，含 traceId/
// policyId，不含任何内容），绝不阻塞或影响请求响应。未装配 Redis（组合测试
// / ProtectionCountStore nil）、无请求级脱敏状态（零开销路径）或零命中时整
// 体无操作。
func (c *gatewayChain) recordSecurityPolicyProtectionOnce(req *gatewaypreauth.GatewayRequest, traceID string) {
	if c.securityPolicyProtection == nil || req == nil || req.SecurityPolicy == nil {
		return
	}
	state := req.SecurityPolicy
	total := state.TotalHits
	if total <= 0 {
		return
	}
	// CAS 防重：多 attempt 共享同一请求级状态指针（浅复制同源），首次置位
	// 后续全部跳过。置位在 goroutine 外完成，串行 attempt 循环下无竞争。
	if !state.ProtectionCounted.CompareAndSwap(false, true) {
		return
	}
	policyID := state.PolicyID
	totalToIncr := int64(total)
	protection := c.securityPolicyProtection
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), securityPolicyProtectionIncrTimeout)
		defer cancel()
		_ = protection.Increment(ctx, policyID, totalToIncr, traceID)
	}()
}
