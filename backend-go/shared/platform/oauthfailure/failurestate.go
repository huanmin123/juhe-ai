// Package oauthfailure 提供 OAuth 刷新失败状态的跨进程只读契约。
//
// jobs 的 OAuth 刷新/keepalive 任务把账户级失败退避记录在 Redis；gateway 的
// 401 自动刷新在换发 token 前读取同一记录，避免 jobs 已记录 invalid_grant
// 后 gateway 仍重复调用 token 端点。这里只固化键形状与最小解析语义，不复制
// jobs 的计数/终态状态机：gateway 只判断“当前 config_revision 是否仍处于
// 有效退避窗口”，写入与清理仍归 jobs 所有。
package oauthfailure

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

const (
	// OpenAIKeyPrefix 是 OpenAI 刷新族的既有 Redis 前缀；保持逐字节不变，
	// 存量退避记录继续可读。
	OpenAIKeyPrefix = "juhe-ai:state:openai-oauth-refresh-failure:"
	// KeepaliveKeyPrefix 是 anthropic/gemini/grok keepalive 族的独立前缀。
	KeepaliveKeyPrefix = "juhe-ai:state:oauth-keepalive-failure:"
)

// KeySuffix 是账户 ID 的稳定键后缀：base64url(sha256(accountID))。
func KeySuffix(accountID string) string {
	sum := sha256.Sum256([]byte(accountID))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Key 返回命名空间限定的失败状态键：
// juhe-ai:{<namespace>}:state:openai-oauth-refresh-failure:<suffix>。namespace
// 为空时保持历史 juhe-ai:state:... 无 tag 键，与 jobs 无 Redis 运行态/测试臂
// 一致。
func Key(namespace, accountID string) string {
	return KeyWithPrefix(OpenAIKeyPrefix, namespace, accountID)
}

// KeyForVendor 按账户 vendor 选择 jobs 的失败状态族：anthropic / gemini / xai
// 由 keepalive 族记账（独立前缀），其余 OAuth 刷新族走 OpenAI 前缀。未知
// vendor 保持 OpenAI 族；新增 keepalive 厂商时必须同步这里与 jobs 的
// keepalive plan 清单。
func KeyForVendor(vendor, namespace, accountID string) string {
	switch strings.ToLower(strings.TrimSpace(vendor)) {
	case "anthropic", "gemini", "xai":
		return KeyWithPrefix(KeepaliveKeyPrefix, namespace, accountID)
	default:
		return Key(namespace, accountID)
	}
}

// KeyWithPrefix 用显式前缀构造失败状态键（测试与其他族复用）。
func KeyWithPrefix(prefix, namespace, accountID string) string {
	return rediscfg.NamespacedKey(prefix, namespace) + KeySuffix(accountID)
}

// State 是 gateway 关心的失败状态最小投影。
type State struct {
	Count                   int64 `json:"count"`
	LocalConfigurationCount int64 `json:"localConfigurationCount"`
	BackoffUntil            int64 `json:"backoffUntil"`
	ConfigRevision          int64 `json:"configRevision"`
}

// Parse 解析 jobs 写入的失败状态 JSON；空值/非法值返回 nil，不把损坏记录
// 当成有效退避。
func Parse(raw string) *State {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var state State
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil
	}
	return &state
}

// ActiveBackoff 判断给定 configRevision 是否仍有有效退避，返回退避截止时间
// （epoch 毫秒）。只有存储 revision 与当前 revision 完全一致时才生效：较新的
// 记录说明调用方快照过期，较旧的记录说明凭据已轮换，二者都不得阻断刷新。
// 解析失败、缺失或已过期一律返回 (0,false)。
func ActiveBackoff(raw string, nowMillis, configRevision int64) (int64, bool) {
	state := Parse(raw)
	if state == nil || state.ConfigRevision < 1 {
		return 0, false
	}
	normalizedRevision := configRevision
	if normalizedRevision < 1 {
		normalizedRevision = 1
	}
	if state.ConfigRevision != normalizedRevision {
		return 0, false
	}
	if state.BackoffUntil <= nowMillis {
		return 0, false
	}
	return state.BackoffUntil, true
}
