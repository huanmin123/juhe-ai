// audiojobparams.go 是对外 POST /v1/audio/jobs 请求体的统一参数归一层
// （媒体设计 §4.2/§6 自建规范 L1/L2/L3；M3f 首版输入形态：model + input_url
// 公网音频 URL——paraformer 系上游只收 file_urls 不收文件、网关零存储不
// 暂存，multipart 文件输入待有文件上传型上游再开）。产出
// NormalizedAudioJobParams 供 adapter 构造上游报文与链上回显
// params_applied/params_ignored；能力裁决按 AudioJobCapabilities 做出
// （ErrParamUnsupported 语义沿 videoparams.go 先例）。
package gatewaymedia

import (
	"fmt"
	"strings"
)

// NormalizedAudioJobParams 是归一后的长音频任务创建参数。指针语义沿
// NormalizedVideoParams：本面无指针字段（language 空串=未传）。
// ParamsApplied 固定顺序 model/input_url/language；ParamsIgnored 是传入但
// 被忽略的键名列表（契约 §2.4 规则 1，回显不静默）。
type NormalizedAudioJobParams struct {
	Model    string
	InputURL string
	// Language 是可选的语言提示（对外单字符串；adapter 映射为
	// parameters.language_hints 数组，如 paraformer）。
	Language      string
	ParamsApplied []string
	ParamsIgnored []string
}

// ParseAudioJobParams 从已解析的对外 POST /v1/audio/jobs 请求 JSON 对象
// 提取归一参数。校验面：
//   - L1 必填：model、input_url 非空字符串（input_url 是唯一输入形态；
//     multipart 文件输入在链上层以"请求体必须是 JSON 对象"前置 400——
//     body 非 JSON 对象到不了本函数）；
//   - input_url 形态：必须是 http(s) URL（公网音频定位，契约 §10.2 只收
//     公网 URL 数组；网关不下载不暂存，仅指针透传）；
//   - L2 可选：language 非空字符串（语言提示；值域由上游裁决，网关不
//     硬编码 BCP-47 词表）；
//   - 能力裁决：language 已传但 adapter 无对应字段 → 进 ParamsIgnored
//     （契约 §2.4 规则 1，不降级不猜测）。
func ParseAudioJobParams(body map[string]any, caps AudioJobCapabilities) (NormalizedAudioJobParams, error) {
	if body == nil {
		return NormalizedAudioJobParams{}, fmt.Errorf("长音频转写请求体必须是 JSON 对象")
	}
	model, _ := body["model"].(string)
	params := NormalizedAudioJobParams{Model: strings.TrimSpace(model)}
	if params.Model == "" {
		return NormalizedAudioJobParams{}, fmt.Errorf("长音频转写请求缺少 model")
	}
	inputURL, _ := body["input_url"].(string)
	params.InputURL = strings.TrimSpace(inputURL)
	if params.InputURL == "" {
		return NormalizedAudioJobParams{}, fmt.Errorf("长音频转写请求缺少 input_url（唯一输入形态：公网音频 URL；multipart 文件输入暂不支持）")
	}
	if !isValidPublicHTTPURL(params.InputURL) {
		return NormalizedAudioJobParams{}, fmt.Errorf("长音频转写请求 input_url 必须是 http(s) 公网音频 URL，实际为 %q", inputURL)
	}
	params.ParamsApplied = append(params.ParamsApplied, "model", "input_url")

	if raw, present := body["language"]; present && raw != nil {
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return NormalizedAudioJobParams{}, fmt.Errorf("长音频转写请求 language 必须是非空字符串，实际为 %v", raw)
		}
		params.Language = strings.TrimSpace(text)
		if caps.SupportsLanguage {
			params.ParamsApplied = append(params.ParamsApplied, "language")
		}
	}

	params.ParamsIgnored = computeAudioJobParamsIgnored(params, caps)
	return params, nil
}

// computeAudioJobParamsIgnored 按 adapter 能力产出被忽略的键名列表（契约
// §2.4 规则 1）。当前唯一 L2 可选参数是 language（M3f 首版参数面）。
func computeAudioJobParamsIgnored(params NormalizedAudioJobParams, caps AudioJobCapabilities) []string {
	var ignored []string
	if params.Language != "" && !caps.SupportsLanguage {
		ignored = append(ignored, "language")
	}
	return ignored
}

// isValidPublicHTTPURL 校验 input_url 为 http/https URL 形态（scheme + host
// 非空）。公网可达性与音频格式合法性由上游裁决，网关不下载验证（零存储，
// 契约 §10.2 只收公网 URL）。
func isValidPublicHTTPURL(text string) bool {
	scheme, rest, ok := strings.Cut(text, "://")
	if !ok {
		return false
	}
	switch strings.ToLower(scheme) {
	case "http", "https":
	default:
		return false
	}
	host := rest
	if index := strings.IndexAny(rest, "/?#"); index >= 0 {
		host = rest[:index]
	}
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " \t") {
		return false
	}
	// userinfo 形态（user@host）不属于公网定位的常见输入，拒绝。
	return !strings.Contains(host, "@")
}
