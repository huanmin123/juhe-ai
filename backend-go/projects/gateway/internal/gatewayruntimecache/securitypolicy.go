package gatewayruntimecache

// API Key 安全策略的进程内编译缓存（API Key 安全策略与出站内容脱敏设计 §10
// 「编译缓存」行，批B2 运行链接入）：运行时快照（GatewayAPIKeyRow）只携带
// rules_json 原文，regexp.Regexp 不可序列化、绝不进快照；编译产物只存本进程
// 内存，键为 rules_json 内容摘要（sha256 前 16 位 hex）——规则内容变更产生新
// 键，旧条目自然失效，无需显式淘汰；条目上限兜底防长生命周期进程内策略高频
// 编辑导致的无界增长（超限整表重建，重新编译成本为每策略一次 Compile）。
//
// 空 rules_json（未绑定 / NULL 列）返回 Empty policy（Empty()==true，请求原
// 样转发）；损坏的规则集返回显式错误——快照构建（loadGatewayAPIKeyRow）在装
// 载期以同一入口校验并上抛（对齐调度偏好 P2 引用损坏语义，禁止静默回落内
// 置），请求链（CompiledPolicyForKeyRow）因此正常不会再见编译错误。

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysecpol"
)

// securityPolicyCompileCacheMax 是编译缓存条目上限（策略规则集 ≤64KB、条数
// ≤50，单条编译产物小；4096 条远超现实策略数 × 版本数，超限即整表重建）。
const securityPolicyCompileCacheMax = 4096

var (
	securityPolicyCompiledMu    sync.Mutex
	securityPolicyCompiledCache = map[string]*gatewaysecpol.CompiledPolicy{}
)

// emptySecurityPolicy 是共享的空策略单例（Empty()==true：零规则 = 不处理）。
var emptySecurityPolicy = &gatewaysecpol.CompiledPolicy{}

// compiledSecurityPolicyForRules 返回 rulesJSON 的编译产物（带进程内缓存）。
// 空/纯空白输入返回 Empty policy；非空输入编译失败返回显式错误（不缓存失败
// 结果：损坏规则集修复后同键内容变化，无需失效协调）。
func compiledSecurityPolicyForRules(rulesJSON string) (*gatewaysecpol.CompiledPolicy, error) {
	trimmed := strings.TrimSpace(rulesJSON)
	if trimmed == "" {
		return emptySecurityPolicy, nil
	}
	digest := sha256.Sum256([]byte(trimmed))
	key := hex.EncodeToString(digest[:])[:16]
	securityPolicyCompiledMu.Lock()
	cached, ok := securityPolicyCompiledCache[key]
	securityPolicyCompiledMu.Unlock()
	if ok {
		return cached, nil
	}
	policy, err := gatewaysecpol.Compile(trimmed)
	if err != nil {
		return nil, err
	}
	securityPolicyCompiledMu.Lock()
	if len(securityPolicyCompiledCache) >= securityPolicyCompileCacheMax {
		securityPolicyCompiledCache = map[string]*gatewaysecpol.CompiledPolicy{}
	}
	securityPolicyCompiledCache[key] = policy
	securityPolicyCompiledMu.Unlock()
	return policy, nil
}

// CompiledPolicyForKeyRow 返回 Key 快照绑定策略的编译产物（请求链入口）。
// nil 行 / 未绑定 / 空规则集返回 Empty policy（调用方以 Empty() 判零开销路
// 径）；规则集损坏返回显式错误（正常流程快照构建已校验，这里是同一入口的
// 防御性复用，不是静默回落）。
func CompiledPolicyForKeyRow(row *GatewayAPIKeyRow) (*gatewaysecpol.CompiledPolicy, error) {
	if row == nil {
		return emptySecurityPolicy, nil
	}
	return compiledSecurityPolicyForRules(row.SecurityPolicyRulesJSON)
}

// validateSecurityPolicyRulesJSON 是快照构建期的装载校验（顺带预热编译缓
// 存）：非空 rulesJSON 必须可编译，损坏即显式错误上抛（loadGatewayAPIKeyRow
// 消费；空输入合法 = 无规则）。
func validateSecurityPolicyRulesJSON(rulesJSON string) error {
	_, err := compiledSecurityPolicyForRules(rulesJSON)
	return err
}
