package gatewayresponse

import (
	"encoding/json"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-platform/mediaduration"
)

// speech_duration.go 承载同步 TTS/STT 的网关侧时长取证（非对话媒体统一
// 计费设计 §7.2/§8.3）：只解析字节/请求事实，不在本层决定是否计费。

// speechRequestResponseFormat 读取 /audio/speech 请求的 response_format
// （缺省空串 = OpenAI mp3 语义）。body 未物化时只对原始 JSON 做一次小对象
// 反序列化；失败返回空串。
func speechRequestResponseFormat(req *gatewaypreauth.GatewayRequest) string {
	if req == nil {
		return ""
	}
	body := req.ParsedJSONObjectBody()
	if body == nil && req.Body != nil && len(req.Body.RawBody) > 0 {
		var parsed map[string]any
		if json.Unmarshal(req.Body.RawBody, &parsed) == nil {
			body = parsed
		}
	}
	if body == nil {
		return ""
	}
	format, _ := body["response_format"].(string)
	return strings.TrimSpace(format)
}

// speechDurationMeterOptions 构造 TTS 二进制透传的 Meter 选项：format hint
// 来自请求 response_format，content-type 来自上游响应。
func speechDurationMeterOptions(req *gatewaypreauth.GatewayRequest, contentType string) mediaduration.Options {
	return mediaduration.Options{
		FormatHint:  speechRequestResponseFormat(req),
		ContentType: contentType,
	}
}

// speechAudioDurationFromBytes 解析已完整获得的 TTS 音频字节；失败不猜测。
func speechAudioDurationFromBytes(data []byte, formatHint, contentType string) (float64, bool) {
	if len(data) == 0 {
		return 0, false
	}
	result, ok := mediaduration.Parse(data, mediaduration.Options{
		FormatHint:  formatHint,
		ContentType: contentType,
	})
	if !ok || result.Seconds <= 0 {
		return 0, false
	}
	return result.Seconds, true
}

// ttsAudioSeconds 按设计 §7.2 的优先级确定 TTS 秒数与来源标记：
//  1. 上游回报时长（volcengine duration 秒 / minimax audio_length 毫秒）
//     → upstream；
//  2. 网关解析产物音频（转换型响应字节，或纯透传 Meter）→ gateway_parse；
//  3. 请求 input 字符数 ÷ charsPerSecond → estimated。
//
// 返回 false 表示无任何可计数量，由调用方置 usage_missing（不猜测）。
func ttsAudioSeconds(
	req *gatewaypreauth.GatewayRequest,
	upstreamJSON any,
	transformedAudio []byte,
	transformedContentType string,
	meter *mediaduration.Meter,
	chars int64,
) (float64, string, bool) {
	if seconds, ok := gatewaymedia.SpeechReportedSecondsFromJSON(upstreamJSON); ok && seconds > 0 {
		return seconds, "upstream", true
	}
	formatHint := speechRequestResponseFormat(req)
	if seconds, ok := speechAudioDurationFromBytes(transformedAudio, formatHint, transformedContentType); ok {
		return seconds, "gateway_parse", true
	}
	if meter != nil {
		if result, ok := meter.Duration(); ok && result.Seconds > 0 {
			return result.Seconds, "gateway_parse", true
		}
	}
	if seconds := gatewaymedia.TTSCharsFallbackSeconds(chars); seconds > 0 {
		return seconds, "estimated", true
	}
	return 0, "", false
}

// sttRequestAudioDuration 从 STT 请求体提取上传音频并解析秒数：
// multipart 取 file 字段（零拷贝切片），二进制体直接使用 RawBody；part
// Content-Type 缺失时由字节头嗅探。失败不阻断请求，由调用方置
// usage_missing。
func sttRequestAudioDuration(req *gatewaypreauth.GatewayRequest) (float64, bool) {
	if req == nil || req.Body == nil || len(req.Body.RawBody) == 0 {
		return 0, false
	}
	contentType := req.Header("Content-Type")
	if req.Body.State != nil && req.Body.State.ContentType != "" {
		contentType = req.Body.State.ContentType
	}
	data := req.Body.RawBody
	partType := contentType
	if strings.HasPrefix(strings.ToLower(contentType), "multipart/") {
		part, extractedType, ok := gatewaybody.ExtractMultipartFilePart(req.Body.RawBody, contentType, "file")
		if !ok {
			return 0, false
		}
		data = part
		partType = extractedType
	}
	result, ok := mediaduration.Parse(data, mediaduration.Options{ContentType: partType})
	if !ok || result.Seconds <= 0 {
		return 0, false
	}
	return result.Seconds, true
}
