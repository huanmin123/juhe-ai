package main

import (
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// BUG-0268：Node 迁移回归修复——网关协议路径版本前缀可选契约。
//
// Node 的三个协议驱动都按"剥掉可选版本前缀"匹配请求路径（而非要求前缀
// 存在）：openai（protocols/registry.ts isOpenAIProtocolRequestPath，
// replace(/^\/v1(?=\/|$)/, '')）、anthropic（anthropic-v1/route-helpers.ts
// normalizedAnthropicPath，同一正则）、gemini（gemini-endpoint-modes.ts
// normalizedGeminiPath，replace(/^\/v1beta(?=\/|$)/, '')）。server.ts 把
// openAIGatewayRouter 挂在根路径，因此 /responses 与 /v1/responses、
// /messages 与 /v1/messages、/v1beta/models/{id}:generateContent 与
// /models/{id}:generateContent 全部同权受理。
//
// Go 迁移只把链挂到 /v1 子树（compose.go kern.Register("/v1", ...)），无
// 前缀根形态与 /v1beta 形态全部落入 kernel 404 兜底——客户端按
// base_url=https://<host> 配置（Codex wire_api="responses" 拼 {base}/responses）
// 时直接 404。链内判定本就基于 chainStripGatewayVersionPrefix（剥 /v1/ 或
// /v1beta/）归一化，天然兼容两种形态，缺的只是入口放行。
//
// 修复形态：入口按协议路径族清单放行根形态（与 Node 三 driver 的路径族
// 对齐），重写为链内协议门可受理的形态后转发进链。**按族分流**（独立复
// 审裁决，见 chain_gateway_root_test.go 的门传导断言）：
//   - openai/anthropic/openai-compatible 族补规范 /v1 前缀——链门
//     openai 谓词读原始 RequestURI（contains 语义，前缀无关）、anthropic
//     谓词剥可选 /v1，两形态均受理；
//   - gemini 原生族（/models/{id}:<action>、/interactions*、/v1beta 子树）
//     原样透传零重写——链门 gemini 谓词自剥 /v1beta 锚定 ^/models|
//     ^/interactions（gatewaygemini/routehelpers.go stripV1BetaPrefix +
//     modelActionPattern），补 /v1 过不了门、剥 /v1beta 会被 anthropic
//     谓词先拦（GET /models），原样形态恰好精确还原 Node 门语义。
//
// method 不在清单限定：ServeMux 的 method 限定会先于 handler 产生 405，
// 而 Node 对不可用组合回 404 JSON；转发后由链内守卫决定（与 /v1 同形态
// 请求行为一致）。

// mountGatewayRootForms 把根形态协议路径注册到 kernel，按族分流进入网关
// 链。清单只放行网关协议路径族；其余未知路径（管理面探测、扫描器噪音）
// 保持 kernel 404 JSON 兜底，不进链。
//
// 补 /v1 前缀转发的无前缀网关协议路径（Go 1.22 ServeMux 语法）逐条显式
// 注册。{rest...} 子树通配承载族内子路径（/responses/compact、
// /messages/count_tokens、images/audio/files 子资源）。清单对齐 Node 路径族：
//   - openai（registry.ts）：contains /chat/completions、contains /responses、
//     /models、/embeddings、/images*、/audio*
//   - openai-compatible 非协议族（server.ts openAICompatibleFilesRouter /
//     VectorStoresRouter 挂根 + chain_openaicompat.go chainCompatFamilies）：
//     /files*、/containers*、/vector_stores*（Node router 只注册 /v1/... 路径，
//     根形态在 Node 为 404；Go 放行并经链内有鉴权的 compat 面服务，属有意
//     增强，见问题-0268 档案"已知等价偏差"）
//   - anthropic（route-helpers.ts isSupportedAnthropicRequest）：/messages、
//     /messages/count_tokens（GET /models 与 openai 清单合并——Node 驱动
//     优先级同序）
//
// 显式逐条注册、不走 range 切片：前端契约脚本 api-contract-check 静态提取
// Go 注册面时只认显式 Register 调用，range 元素裸引用无法静态求值。
func mountGatewayRootForms(kern *kernel.Kernel, chain http.Handler) {
	if kern == nil || chain == nil {
		return
	}
	v1Adapter := gatewayRootFormV1Adapter(chain)
	kern.Register("/responses", v1Adapter)
	kern.Register("/responses/{rest...}", v1Adapter)
	kern.Register("/chat/completions", v1Adapter)
	kern.Register("/models", v1Adapter)
	kern.Register("/embeddings", v1Adapter)
	kern.Register("/images", v1Adapter)
	kern.Register("/images/{rest...}", v1Adapter)
	kern.Register("/audio", v1Adapter)
	kern.Register("/audio/{rest...}", v1Adapter)
	// M2 视频任务面（媒体设计 §4.2）：/videos 族根形态与 /audio 同批放行，
	// 经 v1Adapter 补 /v1 前缀进链（创建走派发、任务面走账户亲和直连）。
	kern.Register("/videos", v1Adapter)
	kern.Register("/videos/{rest...}", v1Adapter)
	kern.Register("/files", v1Adapter)
	kern.Register("/files/{rest...}", v1Adapter)
	kern.Register("/containers", v1Adapter)
	kern.Register("/containers/{rest...}", v1Adapter)
	kern.Register("/vector_stores", v1Adapter)
	kern.Register("/vector_stores/{rest...}", v1Adapter)
	kern.Register("/messages", v1Adapter)
	kern.Register("/messages/{rest...}", v1Adapter)
	// 以下 gemini 原生族原样透传（零重写）进链：/models/{id}:<action> 族
	//（modelActionPattern）、/interactions* 与 /v1beta 前缀形态整棵子树
	//（gemini-endpoint-modes.ts normalizedGeminiPath）。原样透传是精确还原
	// Node 门语义的形态：链门三谓词按 openai→anthropic→gemini 优先级读原始
	// 路径，/v1beta 前缀使 openai（剥 /v1，(?=\/|$) 不匹配 v1beta）与
	// anthropic（剥 /v1）双谓词都不中，gemini 谓词自剥 /v1beta 匹配
	// ^/models|^/interactions——GET /v1beta/models 因此返回 gemini 形态列表
	//（若剥前缀成裸 /models 会被 anthropic 谓词 GET /models 先拦，形态偏差；
	// 若补 /v1 则 gemini 谓词锚定 ^/models 直接落空，过不了门）。
	kern.Register("/models/{rest...}", chain)
	kern.Register("/interactions", chain)
	kern.Register("/interactions/{rest...}", chain)
	kern.Register("/v1beta/{rest...}", chain)
}

// rewriteRequestClone 返回重写 URL 后的浅克隆请求。Path 与 RawPath 同步
// 改写，避免 EscapedPath() 在两者失配时重新编码丢掉段内 %2F 等原始转义；
// RequestURI 刻意保留原始形态（GatewayRequest.PathAndQuery 读它，等价
// Node req.originalUrl）。
func rewriteRequestClone(r *http.Request, pathPrefix, stripPrefix string) *http.Request {
	rewritten := *r.URL
	rewritten.Path = pathPrefix + strings.TrimPrefix(r.URL.Path, stripPrefix)
	if r.URL.RawPath != "" {
		rewritten.RawPath = pathPrefix + strings.TrimPrefix(r.URL.RawPath, stripPrefix)
	}
	forwarded := r.Clone(r.Context())
	forwarded.URL = &rewritten
	return forwarded
}

// gatewayRootFormV1Adapter 把 openai/anthropic/compat 族根形态重写为规范
// /v1 前缀后转发进链：/responses → /v1/responses、/messages → /v1/messages，
// 与 Node 的剥前缀匹配语义互为等价变换，链内行为与现行 /v1 请求同构。
func gatewayRootFormV1Adapter(chain http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chain.ServeHTTP(w, rewriteRequestClone(r, "/v1", ""))
	})
}
