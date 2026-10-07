package accountbalance

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// BalanceInputDigest 计算余额查询的输入身份摘要：只覆盖决定"这条余额是谁
// 的、以什么配置查出来的"字段——provider、凭据指纹（credential_fingerprint，
// 键材料变化必然变化）、balance_query_config_json 原文（适配器/自定义指针）。
// 刻意不纳入 config_revision（全账户级版本，任何编辑——改名/备注/状态等
// 非余额字段——都会推进）与 intervalMinutes 之外的调度语义。
//
// 消费契约（BUG-0286，用户裁决"任何修改都打掉缓存是全局性缺陷"）：J2/自动
// 探测/手动刷新三个写入方在快照负载内落 inputDigest；gateway 列表 hydrate
// 对账户当前列值现算同一摘要并与之匹配。非余额相关编辑不再打断余额显示；
// 换 Key / 改适配器配置仍然立即失效（摘要变化），下一轮查询（≤5 分钟）自愈。
// configJSON 两侧都取 balance_query_config_json 列原文（trim 后），单一事实
// 源是数据库列值，不做结构化再序列化（避免键序/默认值填充造成字节漂移）。
func BalanceInputDigest(providerCode, credentialFingerprint, configJSON string) string {
	canonical := strings.Join([]string{
		"v1",
		strings.TrimSpace(providerCode),
		strings.TrimSpace(credentialFingerprint),
		strings.TrimSpace(configJSON),
	}, "\x1f")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:8])
}
