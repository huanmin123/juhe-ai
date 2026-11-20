// volcengine_asr_sync.go 是火山 openspeech 大模型录音文件识别极速版的同步
// 转写出站 adapter（对外 POST /v1/audio/transcriptions）。上游为
// POST /api/v3/auc/bigmodel/recognize/flash，头驱动状态（X-Api-Status-Code
// 20000000 成功），请求体同时支持公网 audio.url 与 base64 audio.data；网关
// 零存储且同步面收到的是 multipart 文件，因此固定用 base64 直传，不落盘、
// 不引入对象存储。响应 {audio_info.duration 毫秒, result.text/utterances}
// 归一为 OpenAI json/verbose_json/text/srt/vtt。
package gatewaymedia

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
)

// 火山同步转写面常量（2026-11-20 真实上游实测确认端点存在且 base64 可用）。
const (
	VolcASRSyncPath          = "/api/v3/auc/bigmodel/recognize/flash"
	VolcASRSyncResourceID    = VolcASRResourceID
	VolcASRSyncMaxAudioBytes = 25 * 1024 * 1024
	volcASRSyncModelName     = "bigmodel"
	volcASRSyncSuccessCode   = "20000000"
)

// volcengineTranscriptionAdapter 实现火山极速版同步转写出站转换。
type volcengineTranscriptionAdapter struct{}

func (volcengineTranscriptionAdapter) Provider() string { return "volcengine" }

// VolcASRSyncAudioFormat 由 multipart file part 的 Content-Type/filename
// 推断火山 audio.format。只声明已核实可用格式（mp3/wav/ogg/pcm）；未知
// 格式显式 400，不猜测默认值。Content-Type 缺失时回退 filename 后缀。
func VolcASRSyncAudioFormat(partContentType, filename string) (string, error) {
	mediaType := strings.ToLower(strings.TrimSpace(partContentType))
	if cut := strings.IndexByte(mediaType, ';'); cut >= 0 {
		mediaType = strings.TrimSpace(mediaType[:cut])
	}
	switch mediaType {
	case "audio/mpeg", "audio/mp3", "audio/mpeg3", "audio/x-mpeg-3":
		return "mp3", nil
	case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		return "wav", nil
	case "audio/ogg", "application/ogg":
		return "ogg", nil
	case "audio/pcm", "audio/l16", "audio/x-pcm":
		return "pcm", nil
	}
	ext := strings.ToLower(path.Ext(strings.TrimSpace(filename)))
	switch ext {
	case ".mp3":
		return "mp3", nil
	case ".wav":
		return "wav", nil
	case ".ogg":
		return "ogg", nil
	case ".pcm":
		return "pcm", nil
	}
	return "", fmt.Errorf("%w: 同步转写不支持的音频格式（Content-Type=%q filename=%q），支持 mp3/wav/ogg/pcm", ErrParamUnsupported, partContentType, filename)
}

// volcASRSyncNormalizedResponseFormat 归一 response_format；空值按 OpenAI
// 默认 json，词表外显式 400。
func volcASRSyncNormalizedResponseFormat(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return "json", nil
	}
	switch normalized {
	case "json", "text", "verbose_json", "srt", "vtt":
		return normalized, nil
	default:
		return "", fmt.Errorf("%w: 同步转写不支持 response_format=%q（支持 json/text/verbose_json/srt/vtt）", ErrParamUnsupported, value)
	}
}

// volcASRSyncRequestID 生成单次上游请求 ID（X-Api-Request-Id 是上游的请求
// 幂等/追踪键）。同步面每次请求必须唯一，不能像异步 submit 那样按内容派生。
func volcASRSyncRequestID() string {
	var buffer [12]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return fmt.Sprintf("volcsync-%d", time.Now().UnixNano())
	}
	return "volcsync-" + hex.EncodeToString(buffer[:])
}

// BuildRequest 构造极速版识别请求：POST /api/v3/auc/bigmodel/recognize/flash，
// body {"user":{"uid":...},"audio":{"data":"<base64>","format":...},
// "request":{"model_name":"bigmodel"}}。audio.data 承载 multipart 上传文件，
// 因此同步面不需要公网 URL 或临时对象存储。
func (volcengineTranscriptionAdapter) BuildRequest(ir TranscriptionRequest) (string, []byte, map[string]string, error) {
	if strings.TrimSpace(ir.Model) == "" {
		return "", nil, nil, fmt.Errorf("%w: 同步转写请求缺少 model", ErrParamUnsupported)
	}
	if len(ir.Audio) == 0 {
		return "", nil, nil, fmt.Errorf("%w: 同步转写请求缺少 file", ErrParamUnsupported)
	}
	if len(ir.Audio) > VolcASRSyncMaxAudioBytes {
		return "", nil, nil, fmt.Errorf("%w: 同步转写音频不得超过 %d 字节，长音频请改用 /v1/audio/jobs", ErrParamUnsupported, VolcASRSyncMaxAudioBytes)
	}
	if ir.AudioFormat == "" {
		return "", nil, nil, fmt.Errorf("%w: 同步转写请求缺少 audio.format", ErrParamUnsupported)
	}
	if _, err := volcASRSyncNormalizedResponseFormat(ir.ResponseFormat); err != nil {
		return "", nil, nil, err
	}
	body := map[string]any{
		"user": map[string]any{"uid": volcASRUpstreamUserID},
		"audio": map[string]any{
			"data":   base64.StdEncoding.EncodeToString(ir.Audio),
			"format": ir.AudioFormat,
		},
		"request": map[string]any{"model_name": volcASRSyncModelName},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", nil, nil, fmt.Errorf("编码火山同步转写请求体失败: %w", err)
	}
	return VolcASRSyncPath, encoded, map[string]string{
		"X-Api-Resource-Id": VolcASRSyncResourceID,
		"X-Api-Request-Id":  volcASRSyncRequestID(),
		"X-Api-Sequence":    "-1",
	}, nil
}

// volcASRSyncResponse 是极速版上游成功响应体（音频时长毫秒 + 全文 + 分句）。
type volcASRSyncResponse struct {
	AudioInfo struct {
		Duration *int64 `json:"duration"`
	} `json:"audio_info"`
	Result struct {
		Text       string `json:"text"`
		Utterances []struct {
			StartTime *int64 `json:"start_time"`
			EndTime   *int64 `json:"end_time"`
			Text      string `json:"text"`
		} `json:"utterances"`
	} `json:"result"`
}

// TransformResponse 校验火山头驱动业务状态并把响应归一为 OpenAI 兼容形态。
// 上游成功码 20000000；其它状态（即使 HTTP 200）按协议失败返回错误，由
// 响应管道渲染 502，不能把上游错误 JSON 当成功转写透传。
func (volcengineTranscriptionAdapter) TransformResponse(statusCode int, header http.Header, upstreamBody []byte, ir TranscriptionRequest) ([]byte, string, error) {
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("火山同步转写上游 HTTP 状态异常: %d", statusCode)
	}
	status := ""
	message := ""
	if header != nil {
		status = strings.TrimSpace(header.Get("X-Api-Status-Code"))
		message = strings.TrimSpace(header.Get("X-Api-Message"))
	}
	if status != volcASRSyncSuccessCode {
		if message == "" {
			message = strings.TrimSpace(string(upstreamBody))
		}
		return nil, "", fmt.Errorf("火山同步转写上游业务状态异常: code=%s message=%s", status, message)
	}
	var response volcASRSyncResponse
	if err := json.Unmarshal(upstreamBody, &response); err != nil {
		return nil, "", fmt.Errorf("解析火山同步转写响应失败: %w", err)
	}
	responseFormat, err := volcASRSyncNormalizedResponseFormat(ir.ResponseFormat)
	if err != nil {
		return nil, "", err
	}
	durationSeconds := 0.0
	if response.AudioInfo.Duration != nil && *response.AudioInfo.Duration > 0 {
		durationSeconds = float64(*response.AudioInfo.Duration) / 1000
	}
	switch responseFormat {
	case "text":
		return []byte(response.Result.Text), "text/plain; charset=utf-8", nil
	case "srt":
		return []byte(renderVolcASRSyncSRT(response, durationSeconds)), "application/x-subrip; charset=utf-8", nil
	case "vtt":
		return []byte(renderVolcASRSyncVTT(response, durationSeconds)), "text/vtt; charset=utf-8", nil
	case "verbose_json":
		segments := volcASRSyncSegments(response, durationSeconds)
		payload := map[string]any{
			"task":     "transcribe",
			"duration": durationSeconds,
			"text":     response.Result.Text,
			"segments": segments,
		}
		if language := strings.TrimSpace(ir.Language); language != "" {
			payload["language"] = language
		}
		return marshalVolcASRSyncJSON(payload)
	default:
		// json：保留 duration 供统一媒体计费从上游回报时长结算；OpenAI
		// 客户端会忽略该额外字段。
		payload := map[string]any{"text": response.Result.Text}
		if durationSeconds > 0 {
			payload["duration"] = durationSeconds
		}
		return marshalVolcASRSyncJSON(payload)
	}
}

func marshalVolcASRSyncJSON(payload map[string]any) ([]byte, string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("编码火山同步转写响应失败: %w", err)
	}
	return encoded, "application/json", nil
}

// volcASRSyncSegments 把上游 utterances（毫秒）转为 OpenAI verbose_json 的
// segments（秒）。上游缺分句时用全文构造单段，保证 duration 可被下游消费。
func volcASRSyncSegments(response volcASRSyncResponse, durationSeconds float64) []map[string]any {
	segments := make([]map[string]any, 0, len(response.Result.Utterances))
	for index, utterance := range response.Result.Utterances {
		start := 0.0
		end := durationSeconds
		if utterance.StartTime != nil && *utterance.StartTime >= 0 {
			start = float64(*utterance.StartTime) / 1000
		}
		if utterance.EndTime != nil && *utterance.EndTime >= 0 {
			end = float64(*utterance.EndTime) / 1000
		}
		segments = append(segments, map[string]any{
			"id":    index,
			"start": start,
			"end":   end,
			"text":  utterance.Text,
		})
	}
	if len(segments) == 0 {
		segments = append(segments, map[string]any{
			"id":    0,
			"start": 0.0,
			"end":   durationSeconds,
			"text":  response.Result.Text,
		})
	}
	return segments
}

func renderVolcASRSyncSRT(response volcASRSyncResponse, durationSeconds float64) string {
	segments := volcASRSyncSegments(response, durationSeconds)
	var builder strings.Builder
	for index, segment := range segments {
		start, _ := segment["start"].(float64)
		end, _ := segment["end"].(float64)
		text, _ := segment["text"].(string)
		builder.WriteString(fmt.Sprintf("%d\n%s --> %s\n%s\n\n", index+1, volcASRSyncTime(start, ','), volcASRSyncTime(end, ','), text))
	}
	return builder.String()
}

func renderVolcASRSyncVTT(response volcASRSyncResponse, durationSeconds float64) string {
	segments := volcASRSyncSegments(response, durationSeconds)
	var builder strings.Builder
	builder.WriteString("WEBVTT\n\n")
	for _, segment := range segments {
		start, _ := segment["start"].(float64)
		end, _ := segment["end"].(float64)
		text, _ := segment["text"].(string)
		builder.WriteString(fmt.Sprintf("%s --> %s\n%s\n\n", volcASRSyncTime(start, '.'), volcASRSyncTime(end, '.'), text))
	}
	return builder.String()
}

// volcASRSyncTime 格式化 SRT/VTT 时间戳（毫秒精度）。
func volcASRSyncTime(seconds float64, millisecondSeparator byte) string {
	if seconds < 0 {
		seconds = 0
	}
	totalMilliseconds := int64(seconds*1000 + 0.5)
	hours := totalMilliseconds / 3_600_000
	totalMilliseconds %= 3_600_000
	minutes := totalMilliseconds / 60_000
	totalMilliseconds %= 60_000
	wholeSeconds := totalMilliseconds / 1000
	milliseconds := totalMilliseconds % 1000
	return fmt.Sprintf("%02d:%02d:%02d%c%03d", hours, minutes, wholeSeconds, millisecondSeparator, milliseconds)
}
