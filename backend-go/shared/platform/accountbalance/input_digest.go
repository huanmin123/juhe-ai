package accountbalance

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// CredentialBalanceIdentity 计算凭据 JSON 的余额逻辑身份：base_url（余额
// 端点直接由它构造）+ EffectiveAPIKeys 全池（多 Key 余额是逐 Key 查询的
// 合并，池内任意成员变化都改变查询结果）。credential_fingerprint 只覆盖
// 主 Key（sha256(trimmed source secret)），不足以表达这两者——审核轮
// （BUG-0286 收尾）补齐。两侧共用同一解密后凭据 map：J2 执行核用执行时
// 解封的 payload，gateway 读端用 DecryptJSON 解密 accounts.credentials_encrypted
// 的 payload，提取函数唯一（EffectiveAPIKeys），数组保序，不做结构化再
// 序列化。
func CredentialBalanceIdentity(credentials map[string]any) string {
	baseURL, _ := credentials["base_url"].(string)
	canonical := strings.Join([]string{
		"v1",
		strings.TrimSpace(baseURL),
		strings.Join(EffectiveAPIKeys(credentials), "\x1f"),
	}, "\x1f")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:8])
}

// BalanceInputDigest 计算余额查询的输入身份摘要：只覆盖决定"这条余额是谁
// 的、以什么配置查出来的"字段——provider、凭据指纹（credential_fingerprint，
// 主键材料变化必然变化）、balance_query_config_json 原文（适配器/自定义
// 指针）、凭据逻辑身份（base_url + 有效 Key 全池，CredentialBalanceIdentity）、
// proxy_profile_id（换绑代理改变查询出口与可达性）。刻意不纳入
// config_revision（全账户级版本，任何编辑——改名/备注/状态等非余额字段
// ——都会推进）与 intervalMinutes 之外的调度语义。代理档案行内字段编辑
// （host/port/密码）不改变本摘要：账户行未动，与原 config_revision 判据
// 的覆盖面对称，J2 下一轮查询自然刷新。
//
// 消费契约（BUG-0286，用户裁决"任何修改都打掉缓存是全局性缺陷"）：J2/自动
// 探测/手动刷新三个写入方在快照负载内落 inputDigest；gateway 列表 hydrate
// 对账户当前列值现算同一摘要并与之匹配。非余额相关编辑不再打断余额显示；
// 换 Key（主 Key 或池内成员）/ 改 base_url / 改适配器配置 / 换绑代理仍然
// 立即失效（摘要变化），下一轮查询（≤5 分钟）自愈。configJSON 两侧都取
// balance_query_config_json 列原文（trim 后），单一事实源是数据库列值，
// 不做结构化再序列化（避免键序/默认值填充造成字节漂移）。
func BalanceInputDigest(providerCode, credentialFingerprint, configJSON, credentialIdentity, proxyProfileID string) string {
	canonical := strings.Join([]string{
		"v1",
		strings.TrimSpace(providerCode),
		strings.TrimSpace(credentialFingerprint),
		strings.TrimSpace(configJSON),
		strings.TrimSpace(credentialIdentity),
		strings.TrimSpace(proxyProfileID),
	}, "\x1f")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:8])
}
