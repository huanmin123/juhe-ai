// studiosettle 的上游查询面（媒体工作室设计 §5.2/§5.3 本批裁决形态：jobs
// 直接读库取账户亲和三元组、按 gateway 任务面同 URL 形态直连上游 GET）。
//
// jobs 不 import gateway 模块：openai 视频任务的轮询/下载 URL 形态与状态词
// 表在此镜像 gateway internal/gatewaymedia（VideoProviderAdapter
// BuildPollRequest/BuildContentRequest + NormalizeOpenAIStatus），当前启用
// 供应商为 openai（项目事实）；新增供应商时按 gateway adapter 语义扩镜像。
package studiosettle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxPollObjectBytes 限制上游任务对象读取（创建/轮询响应都是小 JSON；超限
// 即上游协议异常，gateway maxMediaJobObjectBytes 同量级）。
const maxPollObjectBytes int64 = 1 << 20

// VideoPollPath 构造轮询路径（gateway openaiVideoAdapter.BuildPollRequest
// 同形态：GET /v1/videos/{raw_upstream_id}，相对账户 base_url）。
func VideoPollPath(rawUpstreamJobID string) (method, path string) {
	return http.MethodGet, "/v1/videos/" + rawUpstreamJobID
}

// VideoContentPath 构造产物下载路径（gateway openaiVideoAdapter.
// BuildContentRequest 同形态：GET /v1/videos/{raw_upstream_id}/content；
// openai 族下载定位由 base_url + job id 构造，不经绝对 Artifact URL 直连）。
func VideoContentPath(rawUpstreamJobID string) (method, path string) {
	return http.MethodGet, "/v1/videos/" + rawUpstreamJobID + "/content"
}

// NormalizeOpenAIStatus 把 openai 视频任务状态字面量归一为统一 status
//（gateway gatewaymedia.NormalizeOpenAIStatus 镜像）：queued/in_progress/
// completed/failed；未知值（含空串）归 in_progress 并返回 false，不猜失败
// 不伪造终态。cancelled/expired 是本地终态，不在归一词表内。
func NormalizeOpenAIStatus(value string) (status string, known bool) {
	switch value {
	case "queued":
		return StatusQueued, true
	case "in_progress":
		return StatusInProgress, true
	case "completed":
		return StatusCompleted, true
	case "failed":
		return StatusFailed, true
	default:
		return StatusInProgress, false
	}
}

// VideoPoll 是上游轮询响应的归一结果（gateway MediaJobIR 的 jobs 侧窄投影）。
type VideoPoll struct {
	UpstreamJobID string
	Status        string
	RawStatus     string
	Progress      *int
	// Error 是 failed 状态的脱敏错误摘要（code/message）。
	Error *PollError
	// OutputSeconds 是 completed 响应的 seconds_length（时长计量来源，缺失
	// 为 nil；duration_ms 按其换算，来源缺失保持 NULL）。
	OutputSeconds *float64
}

// PollError 是归一后的上游错误摘要（error_json 的失败镜像来源）。
type PollError struct {
	Code    string
	Message string
}

// upstreamVideoObject 是 openai 视频对象消费面（gateway openaiVideoObject
// 镜像：id/status/progress/seconds_length/content/error）。
type upstreamVideoObject struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Progress      *int   `json:"progress"`
	SecondsLength *int   `json:"seconds_length"`
	Content       []struct {
		URL string `json:"url"`
	} `json:"content"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ParseVideoPollResponse 把上游轮询响应归一（gateway openaiVideoAdapter.
// ParsePollResponse 镜像）：非 2xx 返回 *UpstreamStatusError（调用方区分
// 404 保持轮询与 5xx 故障，不伪造终态）；completed 但缺少 content 下载定位
// 属上游协议违约，返回错误不猜测产物可达性。
func ParseVideoPollResponse(statusCode int, body []byte) (*VideoPoll, error) {
	if statusCode < 200 || statusCode >= 300 {
		return nil, &UpstreamStatusError{StatusCode: statusCode, Body: string(body)}
	}
	var obj upstreamVideoObject
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("视频任务响应不是有效 JSON: %w", err)
	}
	if obj.ID == "" {
		return nil, fmt.Errorf("视频任务响应缺少 id（受理凭据字段）")
	}
	status, known := NormalizeOpenAIStatus(obj.Status)
	poll := &VideoPoll{UpstreamJobID: obj.ID, Status: status, Progress: obj.Progress}
	if !known {
		poll.RawStatus = obj.Status
	}
	if obj.Error != nil {
		poll.Error = &PollError{Code: obj.Error.Code, Message: obj.Error.Message}
	}
	if status == StatusCompleted {
		if obj.SecondsLength != nil {
			seconds := float64(*obj.SecondsLength)
			poll.OutputSeconds = &seconds
		}
		if len(obj.Content) == 0 || strings.TrimSpace(obj.Content[0].URL) == "" {
			return nil, fmt.Errorf("视频任务 completed 但缺少 content 下载定位（id=%s）", obj.ID)
		}
	}
	return poll, nil
}

// UpstreamStatusError 保留上游非 2xx 的状态码与响应体（gateway
// gatewaymedia.UpstreamStatusError 同义镜像：404 保持轮询直至 TTL，5xx 为
// 受理后故障，都不伪造终态）。
type UpstreamStatusError struct {
	StatusCode int
	Body       string
}

func (e *UpstreamStatusError) Error() string {
	return fmt.Sprintf("上游视频任务接口返回状态码 %d: %s", e.StatusCode, e.Body)
}

// AccountRuntime 是账户亲和的水合结果（组合根按 accounts 行 + 凭据封套解密
// 提供；与 gateway 任务面 hydrateAccount 同语义：原账户原凭据，永不换账户）。
type AccountRuntime struct {
	// Reachable=false 表示账户已删/禁用（任务面"任务不可达"语义 → 任务置
	// expired 收口，§5.2）。
	Reachable bool
	// BaseURL 是账户上游根（credentials.base_url 优先，缺省按协议默认，
	// gateway chainBaseURLOf 同语义）。
	BaseURL string
	// Bearer 是上游认证凭据（api_key 族取凭据池首个 api_key，oauth 族取
	// access_token，gateway chainRuntimeCredentialSource 同语义）。
	Bearer string
}

// AccountResolver 按账户 id 水合账户亲和运行时。返回错误表示读库/解密失败
//（结算失败可重试，attempt 上限收口）；Reachable=false 与错误是两个语义面，
// 不混用（不可达 → 任务 expired 收口）。
type AccountResolver interface {
	Resolve(ctx context.Context, accountID string) (AccountRuntime, error)
}

// HTTPDoer 是出站 HTTP 端口（组合根注入共享客户端；测试注入 stub）。
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// UpstreamClient 按账户亲和三元组直连上游执行轮询与有界下载（"与 gateway
// 任务面同 URL 形态"：账户 base_url + 镜像路径 + Bearer 账户凭据）。
type UpstreamClient struct {
	Doer HTTPDoer
}

// PollVideo 轮询上游视频任务（GET {base}{poll path}，Bearer 账户凭据，响应
// 限读 1MiB）。返回归一结果；上游非 2xx 经归一器以 *UpstreamStatusError 报出。
func (c *UpstreamClient) PollVideo(ctx context.Context, account AccountRuntime, rawUpstreamJobID string) (*VideoPoll, error) {
	method, path := VideoPollPath(rawUpstreamJobID)
	body, status, err := c.fetch(ctx, account, method, path, maxPollObjectBytes)
	if err != nil {
		return nil, err
	}
	return ParseVideoPollResponse(status, body)
}

// DownloadVideo 有界下载产物字节（GET {base}{content path}）。返回字节与
// media type：响应 Content-Type 去参数后的 media type 优先，缺失/通用值按
// application/octet-stream 如实落库，不猜测扩展语义。
func (c *UpstreamClient) DownloadVideo(ctx context.Context, account AccountRuntime, rawUpstreamJobID string, maxBytes int64) ([]byte, string, error) {
	method, path := VideoContentPath(rawUpstreamJobID)
	body, status, contentType, err := c.fetch(ctx, account, method, path, maxBytes)
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status >= 300 {
		return nil, "", &UpstreamStatusError{StatusCode: status, Body: truncateBody(body)}
	}
	if media := mediaTypeFromHeader(contentType); media != "" {
		return body, media, nil
	}
	return body, "application/octet-stream", nil
}

// DownloadURL 有界下载图像产物 URL（§5.5 图像 pending_settled 重下载臂；
// 仅 http/https，gateway studioDownloadBounded 同约束）。media type 同
// DownloadVideo 口径（响应头优先）。
func (c *UpstreamClient) DownloadURL(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, "", fmt.Errorf("生成图像产物地址无效")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	body, status, contentType, err := c.roundTrip(ctx, req, maxBytes)
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status >= 300 {
		return nil, "", &UpstreamStatusError{StatusCode: status, Body: truncateBody(body)}
	}
	if media := mediaTypeFromHeader(contentType); media != "" {
		return body, media, nil
	}
	return body, "application/octet-stream", nil
}

// fetch 执行账户亲和 GET 并返回（body, status, content-type, error）；非 2xx
// 不在此报错，由调用方按状态码裁决（轮询 404 保持轮询语义）。
func (c *UpstreamClient) fetch(ctx context.Context, account AccountRuntime, method, path string, maxBytes int64) ([]byte, int, string, error) {
	if c.Doer == nil {
		return nil, 0, "", fmt.Errorf("上游客户端未装配")
	}
	base := strings.TrimRight(strings.TrimSpace(account.BaseURL), "/")
	if base == "" {
		return nil, 0, "", fmt.Errorf("账户上游地址缺失")
	}
	if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, nil)
	if err != nil {
		return nil, 0, "", err
	}
	if bearer := strings.TrimSpace(account.Bearer); bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return c.roundTrip(ctx, req, maxBytes)
}

func (c *UpstreamClient) roundTrip(ctx context.Context, req *http.Request, maxBytes int64) ([]byte, int, string, error) {
	response, err := c.Doer.Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("上游请求失败: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, response.StatusCode, "", fmt.Errorf("读取上游响应失败: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return nil, response.StatusCode, "", fmt.Errorf("上游响应超过处理上限 %d 字节", maxBytes)
	}
	return body, response.StatusCode, response.Header.Get("Content-Type"), nil
}

// mediaTypeFromHeader 从 Content-Type 头提取 media type（去参数、小写）；
// 缺失/通用值返回空串由调用方按 application/octet-stream 落库。
func mediaTypeFromHeader(contentType string) string {
	media := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	media = strings.ToLower(media)
	if media == "" || media == "application/octet-stream" {
		return ""
	}
	return media
}

func truncateBody(body []byte) string {
	const limit = 512
	if len(body) > limit {
		return string(body[:limit])
	}
	return string(body)
}
