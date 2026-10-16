package gatewayresponse

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 网关注入 stream_options.include_usage 的响应侧消费面（流式零计费修复）。
//
// 出站注入点在组合根 chain_driver 的 buildGatewayUpstreamRequestParts：openai
// 协议标准账户的 chat completions 流式出站 body 在改写链之后补
// "stream_options":{"include_usage":true}，上游由此回 usage chunk，网关计量
// 提取（StreamInspector 的既有 usage 解析）自动生效。本文件承担两件事：
//
//  1. gatewayInjectedChatStreamUsage 在响应侧识别"本次 usage chunk 是网关注入
//     引出的"（出站 body 携带 stream_options.include_usage=true + stream=true，
//     而客户端 body 未显式要求）——识别凭据是引擎回带的本次 attempt 实际发送体
//     （HandleUpstreamResponseInput.RequestBody）与客户端请求视图（Req），不
//     需要任何跨包新增请求作用域标记；
//  2. filterGatewayInjectedUsageOnlySseFrames 在流管道写下游前剥除 usage-only
//     SSE data 帧（choices 为空数组且 usage 为非空对象）。剥除发生在 inspector
//     消费之后（inspector 吃原始分片，计量照常），只影响下游字节——客户端
//     显式带 include_usage 的流（网关零注入）不进剥除路径，chunk 照旧透传。

// streamOptionsPropertyNameBytes / usagePropertyNameBytes 是帧级快路径预筛的
// 属性名字节形态：分片不含对应片段时零解析直接放行/跳过。
var (
	streamOptionsPropertyNameBytes = []byte(`"stream_options"`)
	usagePropertyNameBytes         = []byte(`"usage"`)
)

// gatewayInjectedChatStreamUsage 报告本次流式响应的 usage chunk 是否由网关
// 注入 stream_options.include_usage 引出（客户端未显式要求）。判定：
//   - 出站 body（RequestBody，已经模型映射/账户准备的最终发送体）携带
//     stream_options.include_usage=true 且 stream=true；
//   - 账户协议为 openai（注入只在 openai 协议标准账户出站点发生）；
//   - 客户端 body 已解析时未显式携带 stream_options.include_usage=true；
//     未解析（大 body scanned 形态）视为未要求——注入是字节级 splice，不依赖
//     ingress 解析，恰好是"客户端没说、网关补的"形态。
//
// codex OAuth /responses 出站体在归一器中剥除 stream_options，不会误入本判定。
func gatewayInjectedChatStreamUsage(req *gatewaypreauth.GatewayRequest, requestBody []byte, account AccountView) bool {
	if req == nil || len(requestBody) == 0 {
		return false
	}
	if account == nil || account.GetProtocolCode() != gatewayopenai.ProtocolCode {
		return false
	}
	if !bytes.Contains(requestBody, streamOptionsPropertyNameBytes) {
		return false
	}
	var root map[string]any
	if err := json.Unmarshal(requestBody, &root); err != nil || root == nil {
		return false
	}
	options, ok := root["stream_options"].(map[string]any)
	if !ok {
		return false
	}
	if requested, _ := options["include_usage"].(bool); !requested {
		return false
	}
	if stream, _ := root["stream"].(bool); !stream {
		return false
	}
	if parsed := req.ParsedJSONObjectBody(); parsed != nil {
		if clientOptions, ok := parsed["stream_options"].(map[string]any); ok {
			if requested, _ := clientOptions["include_usage"].(bool); requested {
				return false
			}
		}
	}
	return true
}

// filterGatewayInjectedUsageOnlySseFrames 从转发分片中剥除 usage-only SSE
// data 帧（data 载荷 JSON 满足 choices 为空数组且 usage 为非空对象）。返回
// (filtered, dropped)：dropped=false 时 filtered 为 nil，调用方沿用原始字节
// （零拷贝快路径）；dropped=true 时 filtered 是剥除后的下游字节（可能为空，
// 表示分片整体是 usage-only 帧）。事件边界（\n\n、\r\n\r\n、\r\r）与多行
// data 合并语义同 OpenAIStreamInterceptor 的分帧实现；尾部未完帧原样保留，
// data: [DONE] 与非 data 帧恒保留。
func filterGatewayInjectedUsageOnlySseFrames(chunk []byte) ([]byte, bool) {
	if !bytes.Contains(chunk, usagePropertyNameBytes) {
		return nil, false
	}
	dropped := false
	filtered := make([]byte, 0, len(chunk))
	for start := 0; start < len(chunk); {
		end, complete := sseEventEnd(chunk, start)
		event := chunk[start:end]
		if complete && sseDataPayloadIsUsageOnlyChatFrame(event) {
			dropped = true
		} else {
			filtered = append(filtered, event...)
		}
		start = end
	}
	if !dropped {
		return nil, false
	}
	return filtered, true
}

// sseEventEnd 返回从 start 开始的首个完整 SSE 事件边界（含边界空行字节）；
// 未找到边界时返回分片末尾且 complete=false（尾部未完帧）。边界词表：
// \n\n、\r\r、\r\n\r\n（分帧语义与 OpenAIStreamInterceptor.shiftEvent 同源，
// 额外补齐 CRLF 空行形态）。
func sseEventEnd(chunk []byte, start int) (end int, complete bool) {
	data := chunk[start:]
	for index := 0; index < len(data); index++ {
		switch data[index] {
		case '\n':
			if index+1 < len(data) && data[index+1] == '\n' {
				return start + index + 2, true
			}
		case '\r':
			if index+1 < len(data) && data[index+1] == '\r' {
				return start + index + 2, true
			}
			if index+3 < len(data) && data[index+1] == '\n' && data[index+2] == '\r' && data[index+3] == '\n' {
				return start + index + 4, true
			}
		}
	}
	return len(chunk), false
}

// sseDataPayloadIsUsageOnlyChatFrame 判定一个完整 SSE 事件的 data 载荷是否
// usage-only chat 帧：存在 data 行；载荷非 [DONE]；JSON 对象含 choices 键且
// 为空数组、usage 键且为非空对象。解析失败/形状不符一律视为普通帧保留。
func sseDataPayloadIsUsageOnlyChatFrame(event []byte) bool {
	var dataLines []string
	for _, rawLine := range strings.Split(string(event), "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataLines = append(dataLines, strings.TrimPrefix(line, "data:"))
	}
	if len(dataLines) == 0 {
		return false
	}
	payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
	if payload == "" || payload == "[DONE]" {
		return false
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(payload), &root); err != nil || root == nil {
		return false
	}
	choices, ok := root["choices"].([]any)
	if !ok || len(choices) != 0 {
		return false
	}
	usage, ok := root["usage"].(map[string]any)
	if !ok || len(usage) == 0 {
		return false
	}
	return true
}
