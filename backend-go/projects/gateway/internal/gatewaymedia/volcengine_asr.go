// volcengine_asr.go 是火山方舟（豆包）大模型录音文件识别 2.0 的长音频异步
// 转写出站 adapter（契约 §9.2 长转写臂，2026-10-17 承接批；注册表挂
// audioJobAdapters 的 "volcengine" 条目）。上游为头驱动受理协议——与本网关
// 既有 body 驱动任务面不同构：submit/query 的成功与失败都由响应头
// X-Api-Status-Code 承载（成功响应 body 为空 {}）；任务 ID = 网关生成的
// 请求头 X-Api-Request-Id（上游不回传任务语义的 id——链上受理判定直接取
// 该出站值，见 volcASRRequestIDOf；响应头回显该 ID 仅供参考，缺席不报错；
// query 用同一值轮询）。
// 现役资源 volc.seedasr.auc（豆包录音文件识别模型 2.0；1.0 系资源
// volc.bigasr.* 与 sauc 流式系对该语音账户未开通——45000030 not granted
// 不是故障，承接不覆盖）。鉴权 X-Api-Key 单头（凭据沿用 speech_token 单值，
// 与 TTS 同一把新版语音控制台 API Key）；X-Api-Sequence: "-1" 为官方示例
// 固定值。无取消端点（SupportsCancel=false 本地收敛）。纯逻辑层，不触
// chain/HTTP 执行；出站 host 恒 openspeech.bytedance.com（语音服务域，
// 与账户 ark base_url 无关——链上层 chainVolcengineASRUpstreamURL 特例，
// 同 TTS chainVolcengineSpeechUpstreamURL 先例，共用同一语音服务域测试
// seam）。
package gatewaymedia

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// 火山长转写面常量（契约 §9.2）：资源 ID 即 API 真实标识（目录行同名列），
// 统一面 model=volc.seedasr.auc → resource id 头恒该值、body model_name 恒
// bigmodel（官方示例值）；user.uid 落网关标识（submit body 的 user.uid 由
// 上游用于回调/审计关联，网关无 per-请求账号语义可透——固定服务标识，同
// qwen 归一层不带此类字段的先例由本常量承载）。
const (
	VolcASRResourceID        = "volc.seedasr.auc"
	VolcASRSubmitPath        = "/api/v3/auc/bigmodel/submit"
	VolcASRQueryPath         = "/api/v3/auc/bigmodel/query"
	volcASRUpstreamModelName = "bigmodel"
	volcASRUpstreamUserID    = "juhe-ai-gateway"
	volcASRAPISuccessCode    = "20000000" // submit 受理 / query 终态成功
	volcASRAPIRunningCode    = "20000001" // query 处理中
	volcASRAPIQueuedCode     = "20000002" // query 队列中
)

// volcengineASRAdapter 实现豆包录音文件识别 2.0 长转写出站转换。
type volcengineASRAdapter struct{}

func (volcengineASRAdapter) Provider() string { return "volcengine" }

// Capabilities 按录音文件识别请求面现状声明（契约 §9.2 统一面映射只含
// model/input_url/audio.format）：language 无上游对应字段 → false，对外
// language 参数忽略 + 回显 params_ignored（§2.4 规则 4）。
func (volcengineASRAdapter) Capabilities() AudioJobCapabilities {
	return AudioJobCapabilities{SupportsLanguage: false}
}

// volcASRAudioFormatOf 由音频 URL 后缀推断 audio.format（契约 §9.2：
// .wav/.mp3/.ogg 直取；其余/无后缀落缺省 wav——上游不识别时显式报错回传，
// 不静默）。URL 可能带 query/fragment，先剥离再取后缀。
func volcASRAudioFormatOf(audioURL string) string {
	trimmed := audioURL
	if cut := strings.IndexAny(trimmed, "?#"); cut >= 0 {
		trimmed = trimmed[:cut]
	}
	switch {
	case strings.HasSuffix(trimmed, ".wav"):
		return "wav"
	case strings.HasSuffix(trimmed, ".mp3"):
		return "mp3"
	case strings.HasSuffix(trimmed, ".ogg"):
		return "ogg"
	default:
		return "wav"
	}
}

// volcASRRequestIDOf 派生 submit 的 X-Api-Request-Id（即上游任务 ID，query
// 轮询复用同一值；格式 volcasr-<24hex>）。契约 §9.2：任务 ID 是网关自定义的
// 出站头值，**上游不回传**（受理响应 body 为空、响应头只带协议状态）——创建
// 受理判定要从同源重建的出站计划取回该值，派生必须是请求输入的确定性纯函数
// （model + input_url + audio_format 的 sha256 截断），同请求同 id：同一账户
// 重复提交同一音频在上游呈请求级幂等（请求 id 即幂等键），换账户/换输入必得
// 新 id。分隔符 \x1f 防字段拼接歧义。
func volcASRRequestIDOf(model, audioURL, audioFormat string) string {
	sum := sha256.Sum256([]byte(model + "\x1f" + audioURL + "\x1f" + audioFormat))
	return "volcasr-" + hex.EncodeToString(sum[:12])
}

// volcASRUpstreamHeaders 组装 submit/query 共用的鉴权与资源头三件套：
// X-Api-Resource-Id（恒 volc.seedasr.auc）+ X-Api-Request-Id（任务 ID）+
// X-Api-Sequence（官方示例固定 "-1"）。X-Api-Key 凭据由链上层按账户
// speech_token 注入（chain_media_video 的 mediaJobRequestUpstream），不在此
// 处携带凭据。Content-Type 显式 application/json（query 空 body {} 也是
// JSON 面）。
func volcASRUpstreamHeaders(requestID string) http.Header {
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	header.Set("X-Api-Resource-Id", VolcASRResourceID)
	header.Set("X-Api-Request-Id", requestID)
	header.Set("X-Api-Sequence", "-1")
	return header
}

// Create 构造契约 §9.2 创建报文：POST /api/v3/auc/bigmodel/submit，body
// {"user":{"uid":...},"audio":{"url","format"},"request":{"model_name":"bigmodel"}}。
// 映射规则：
//   - input_url → audio.url（只接受公网 URL——上游服务端自行拉取，与零存储
//     对齐；multipart 在归一层前置拒绝）；
//   - audio.format 由 URL 后缀推断（.wav/.mp3/.ogg），可经
//     provider_options.volcengine.audio_format 覆盖，缺省 wav；
//   - language 无对应字段（Capabilities 不声明）由归一层忽略回显；
//   - 任务 ID 本请求即确立：按 model+input_url+audio_format 确定性派生
//     X-Api-Request-Id 放 ExtraHeaders（上游不回传任务 id，链上受理判定
//     直接取同一派生值，见 volcASRRequestIDOf）。
func (volcengineASRAdapter) Create(_ context.Context, in AudioJobCreateInput) (AudioJobCreateOutput, error) {
	params := in.Params
	if params.Model == "" {
		return AudioJobCreateOutput{}, fmt.Errorf("长音频转写请求缺少 model")
	}
	if params.InputURL == "" {
		return AudioJobCreateOutput{}, fmt.Errorf("长音频转写请求缺少 input_url")
	}
	audioFormat := volcASRAudioFormatOf(params.InputURL)
	if options, ok := in.ProviderOptions["volcengine"].(map[string]any); ok {
		if override, ok := options["audio_format"].(string); ok && strings.TrimSpace(override) != "" {
			audioFormat = strings.TrimSpace(override)
		}
	}
	body := map[string]any{
		"user": map[string]any{"uid": volcASRUpstreamUserID},
		"audio": map[string]any{
			"url":    params.InputURL,
			"format": audioFormat,
		},
		"request": map[string]any{"model_name": volcASRUpstreamModelName},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return AudioJobCreateOutput{}, fmt.Errorf("编码火山长转写创建请求体失败: %w", err)
	}
	return AudioJobCreateOutput{
		Method: http.MethodPost,
		Path:   VolcASRSubmitPath,
		Body:   encoded,
		ExtraHeaders: map[string]string{
			"X-Api-Request-Id":  volcASRRequestIDOf(params.Model, params.InputURL, audioFormat),
			"X-Api-Resource-Id": VolcASRResourceID,
			"X-Api-Sequence":    "-1",
		},
	}, nil
}

// BuildPollRequest 构造轮询请求：POST /api/v3/auc/bigmodel/query，空对象
// body {}；任务 ID 经 X-Api-Request-Id 头携带（头驱动协议，非路径参数）。
// 返回的头由链上层合并出站（X-Api-Key 凭据链上层注入）。
func (volcengineASRAdapter) BuildPollRequest(upstreamJobID string) (string, string, http.Header, []byte) {
	return http.MethodPost, VolcASRQueryPath, volcASRUpstreamHeaders(upstreamJobID), []byte("{}")
}

// ContentFromArtifact 声明火山长转写无产物下载 URL（qwen transcription_url
// 同构物不存在）：终态转写 JSON 即产物，content 请求 = 同构 query 请求
// （再查一次上游、响应体直接代理）→ false，链上层走 BuildContentRequest。
func (volcengineASRAdapter) ContentFromArtifact() bool { return false }

// BuildContentRequest 构造产物请求：与轮询同构的 query（契约 §9.2 content
// 语义——同路径同头同空对象 body {}）。
func (volcengineASRAdapter) BuildContentRequest(upstreamJobID string) (string, string, http.Header, []byte) {
	method, path, header, body := volcengineASRAdapter{}.BuildPollRequest(upstreamJobID)
	return method, path, header, body
}

// BuildCancelRequest 在火山形态下不参与取消：openspeech 面无任务取消端点，
// SupportsCancel 为 false，链上层不发上游请求、直接本地收敛 cancelled。
func (volcengineASRAdapter) BuildCancelRequest(string) (string, string) {
	return http.MethodPost, ""
}

// SupportsCancel：录音文件识别 submit/query 面无取消端点 → false。
func (volcengineASRAdapter) SupportsCancel() bool { return false }

// volcASRResultObject 是 query 终态响应 body 的消费面（契约 §9.2）：
// audio_info.duration 毫秒 + result.text 全文 + utterances 词级时间戳。
// 受理形态（submit 成功响应）body 为空对象——duration 与 text 双缺即受理。
type volcASRResultObject struct {
	AudioInfo struct {
		Duration *int64 `json:"duration"`
	} `json:"audio_info"`
	Result struct {
		Text string `json:"text"`
	} `json:"result"`
}

// ParsePollResponse 把 submit（创建）与 query（轮询）响应归一为 MediaJobIR
// （头驱动协议，契约 §9.2 状态归一总表 volcengine 长转写臂）：
//   - HTTP 非 2xx → *UpstreamStatusError（传输/网关层故障，链上层区分 404
//     收敛与 5xx 故障）；
//   - 响应头 X-Api-Status-Code 承载业务状态（body 驱动厂商不同构）：
//     20000000 → body 空 {}（submit 受理形态，无 audio_info/result）为
//     queued，任务 ID = 网关生成的出站 X-Api-Request-Id——上游不回传任务
//     id（响应头若回显请求 ID 则照读，缺席不报错），链上受理判定直接取
//     volcASRRequestIDOf 同一派生值，出站值缺席即受理失败；body 携带
//     result → completed +
//     audio_info.duration 毫秒 ÷1000 秒抽 AudioInputSeconds（缺失/非正值
//     不填，终态计费由链上按 §2.8 兜底 usage_missing 不猜测）；
//     20000001/20000002 → in_progress（处理中/队列中）；
//     450000xx → failed（Error.Code=状态码、Message=X-Api-Message；如
//     45000006 未文档化——实测为音频 URL 拉取失败）；
//     未知值 → in_progress 记 RawStatus 不猜测；
//   - 头缺失（无 X-Api-Status-Code）→ 协议违约报错。
//
// 创建路径的失败（450000xx 归一 failed）由链上层判受理失败（failed 无
// upstream_job_id 语义），不落任务行。
func (volcengineASRAdapter) ParsePollResponse(statusCode int, header http.Header, body []byte) (*MediaJobIR, error) {
	if statusCode < 200 || statusCode >= 300 {
		return nil, &UpstreamStatusError{StatusCode: statusCode, Body: string(body)}
	}
	code := strings.TrimSpace(header.Get("X-Api-Status-Code"))
	if code == "" {
		return nil, fmt.Errorf("火山长转写响应缺少 X-Api-Status-Code 头（头驱动协议违约）")
	}
	requestID := strings.TrimSpace(header.Get("X-Api-Request-Id"))
	ir := &MediaJobIR{Kind: JobKindAudioTranscription}
	switch {
	case code == volcASRAPISuccessCode:
		var result volcASRResultObject
		_ = json.Unmarshal(body, &result) // 空 body/空对象按零值处理（受理形态）
		if result.Result.Text == "" && result.AudioInfo.Duration == nil {
			// submit 受理形态：body 空 {}。任务 ID = 网关生成的出站
			// X-Api-Request-Id（上游不回传；响应头回显则照读，缺席交链上层
			// 从同源复析计划补全）。
			ir.Status = JobStatusQueued
			ir.UpstreamJobID = requestID
			return ir, nil
		}
		ir.Status = JobStatusCompleted
		ir.UpstreamJobID = requestID
		if result.AudioInfo.Duration != nil && *result.AudioInfo.Duration > 0 {
			seconds := float64(*result.AudioInfo.Duration) / 1000
			ir.Usage.AudioInputSeconds = &seconds
		}
		return ir, nil
	case code == volcASRAPIRunningCode || code == volcASRAPIQueuedCode:
		ir.Status = JobStatusInProgress
		ir.UpstreamJobID = requestID
		return ir, nil
	case strings.HasPrefix(code, "450000"):
		message := strings.TrimSpace(header.Get("X-Api-Message"))
		if message == "" {
			message = "火山长转写任务失败（X-Api-Status-Code " + code + "）"
		}
		ir.Status = JobStatusFailed
		ir.UpstreamJobID = requestID
		ir.Error = &MediaJobError{Code: code, Message: message}
		return ir, nil
	default:
		// 未知码归 in_progress 记 RawStatus，不猜测失败（§2.6 归一规则）。
		ir.Status = JobStatusInProgress
		ir.UpstreamJobID = requestID
		ir.RawStatus = code
		return ir, nil
	}
}
