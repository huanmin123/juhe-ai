package gatewaycodex

import (
	"context"
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Anthropic（Claude OAuth）unified rate limit 响应头的持久化窄口
// （AI账户Grok用量快照设计 §8.2）：与 codexheaders.go 的
// PersistOpenAICodexHeadersIfNeeded 同构的 fire-and-forget 侧面——本包只做
// 账户资格门 + 头存在性门，job 构造与入队由组合根派发器承接
// （compose_codex_usage_headers.go 的 record_maintenance_jobs 快照通道）。

// AnthropicUsageSnapshotSource 是 anthropic 快照行的固定 source（规格 §8.2：
// source='anthropic_unified_headers'）。与 codex 侧按流量来源/失败面重写
// source 不同，anthropic 采集族不区分成功/失败来源。
const AnthropicUsageSnapshotSource = "anthropic_unified_headers"

// AnthropicUsageHeadersDispatcher mirrors CodexUsageHeadersDispatcher for the
// anthropic unified rate limit headers. Implementations own the error
// handling (fire-and-forget; the enqueue failure logs a warn and never
// touches the request outcome).
type AnthropicUsageHeadersDispatcher interface {
	PersistAnthropicUsageHeaders(ctx context.Context, accountID string, headers http.Header, source string)
}

// PersistAnthropicUsageHeadersIfNeeded dispatches the anthropic usage
// snapshot side effect for `provider_code='anthropic' AND type='oauth'`
// accounts whose response carries unified rate limit headers. Ineligible
// accounts, header-only snapshots without anthropic data and a nil dispatcher
// return silently.
func PersistAnthropicUsageHeadersIfNeeded(
	ctx context.Context,
	account gatewayruntimecache.OpenAIAccountSecret,
	headers http.Header,
	source string,
	dispatcher AnthropicUsageHeadersDispatcher,
) {
	if account.Type != "oauth" || !isAnthropicUsageAccount(account) {
		return
	}
	if dispatcher == nil {
		return
	}
	if gatewayupstream.ParseAnthropicUsageHeaders(headers) == nil {
		return
	}
	dispatcher.PersistAnthropicUsageHeaders(ctx, account.ID, headers, source)
}

// isAnthropicUsageAccount 是 anthropic 侧的账户资格门：与 codex 侧的
// isOpenAIProtocolProfile（协议画像判断）对称，anthropic 按 provider_code
// 判定（设计 §8.2：仅 provider_code='anthropic' AND type='oauth' 触发；
// type 已由调用口先行判断）。
func isAnthropicUsageAccount(account gatewayruntimecache.OpenAIAccountSecret) bool {
	return normalizeProtocolToken(account.ProviderCode) == "anthropic"
}
