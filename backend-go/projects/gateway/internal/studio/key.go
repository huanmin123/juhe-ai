package studio

import "encoding/json"

// purpose=studio 专用 Key 端口（媒体工作室设计 §3.5）：每用户幂等确保的
// 实现镜像 EnsureChatAPIKey（含默认资源自愈与并发竞态恢复），落在组合根
// （与 chat 的 chatAPIKeyProvider 同层、复用其默认分组/默认策略路由自愈）。
// 本包只声明端口与 studio_domains_json 域授权的词表常量。

// StudioKeyProvider 是 purpose='studio' 专用 Key 端口（§3.5）：EnsureStudioKey
// 幂等确保（不存在则插入、被并发抢先则复用胜者行、被用户删除则自愈重建）；
// FindStudioKeySecret 返回 Key 明文（生成链 /v1 Bearer 用，镜像 chat 的
// FindChatAPIKey 语义）；StudioKeyDomains 读取域授权（studio_domains_json，
// §3.5.3 授权数据；生成链域校验用，§4.5）。生成链调用前同步确保（Key 是
// Bearer 与域校验的载荷源，缺 Key 无法受理；账户候选面保持 §3.5 异步确保）。
type StudioKeyProvider interface {
	EnsureStudioKey(ownerID string) (string, error)
	// FindStudioKeySecret 返回该 Key id 的明文密钥；不存在/非 active 返回错误。
	FindStudioKeySecret(keyID, ownerID string) (string, error)
	// StudioKeyDomains 返回该用户 studio Key 的域授权集；Key 不存在按 P0 默认
	// 四域处理（ParseStudioDomains("")）。
	StudioKeyDomains(ownerID string) ([]string, error)
}

// StudioAllDomains 是 P0 ensured Key 恒写的域授权集（§3.5.3：四域全授权，
// 不提供编辑入口；该列是授权数据不是功能开关）。
var StudioAllDomains = []string{"text", "image", "audio", "video"}

// StudioAllDomainsJSON 是 StudioAllDomains 的 JSON 字面量（写入
// api_keys.studio_domains_json 的固定值）。
const StudioAllDomainsJSON = `["text","image","audio","video"]`

// ParseStudioDomains 解析 studio_domains_json（域授权读侧，/v1 preauth 域
// 校验批次消费）；空值按 P0 默认四域处理，坏 JSON 报错不静默。
func ParseStudioDomains(raw string) ([]string, error) {
	if raw == "" {
		return append([]string{}, StudioAllDomains...), nil
	}
	var domains []string
	if err := json.Unmarshal([]byte(raw), &domains); err != nil {
		return nil, err
	}
	return domains, nil
}
