package main

// chain_media_video_probe.go：P4 视频产物时长探测的链层传输面（非对话媒体
// 统一计费设计 §8.4/AC-028）。
//
// 探测只在终态结算的兜底分支发生：upstream/request/default 三路都没有
// 秒数，且视频适配器显式声明 ProbeArtifactDuration。传输不复用账户认证，
// 但复用账户有效代理；每次探测创建独立 client（跟随 ≤3 次重定向），所有
// 直连拨号经 upstreamhttp.DialGuard，重定向目标在 CheckRedirect 中再次做
// scheme/host 预检，失败关闭为 usage_missing。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

const chainMediaArtifactProbeMaxRedirects = 3

// chainMediaJobProbeVideoSeconds 对 completed 视频任务的产物做有界 Range
// 探测。成功返回秒数；任何失败返回 ok=false，由调用方落 usage_missing。
// 不重试、不回退整段下载。
func (c *gatewayChain) chainMediaJobProbeVideoSeconds(ctx context.Context, account *chainMediaJobAccount, record *gatewaymedia.MediaJobRecord, ir *gatewaymedia.MediaJobIR) (float64, bool) {
	if c == nil || c.mediaJobs == nil || record == nil {
		return 0, false
	}
	artifactURL := ""
	if ir != nil {
		artifactURL = strings.TrimSpace(ir.Artifact.ContentURL)
	}
	if artifactURL == "" {
		artifactURL = strings.TrimSpace(record.Artifact.ContentURL)
	}
	if artifactURL == "" {
		return 0, false
	}
	fetch := func(fetchCtx context.Context, rawURL string, start, end int64) (int, http.Header, []byte, error) {
		return c.chainMediaJobFetchArtifactRange(fetchCtx, account, rawURL, start, end)
	}
	seconds, err := gatewaymedia.ProbeArtifactDuration(ctx, artifactURL, fetch)
	if err != nil {
		c.logMediaDurationProbeFailure(record, err)
		return 0, false
	}
	return seconds, true
}

// chainMediaJobFetchArtifactRange 执行一次 Range GET。不携带账户认证、
// Cookie 或 Authorization；只允许 http/https，拒绝 userinfo；直连与重定向
// 目标都经同一 DialGuard 预检，直连拨号再由 Transport 安装的 guard 钉住
// 已校验地址。
func (c *gatewayChain) chainMediaJobFetchArtifactRange(ctx context.Context, account *chainMediaJobAccount, rawURL string, start, end int64) (int, http.Header, []byte, error) {
	if start < 0 || end < start {
		return 0, nil, nil, fmt.Errorf("产物探测 Range 非法: %d-%d", start, end)
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return 0, nil, nil, fmt.Errorf("产物探测地址不合法")
	}
	// 片段标识不参与 HTTP 请求，显式清掉避免签名 URL 原样携带到请求行。
	parsed.Fragment = ""
	parsed.RawFragment = ""
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return 0, nil, nil, fmt.Errorf("产物探测地址协议不受支持")
	}
	guard := c.mediaJobs.transport.DialGuard
	if guard == nil {
		// 设计 §8.4/AC-028：没有 DialGuard 时 P4 不启用，失败关闭。
		return 0, nil, nil, fmt.Errorf("产物探测缺少 SSRF 拨号守卫")
	}
	if err := guard.ValidateHost(ctx, parsed.Hostname(), parsed.Port()); err != nil {
		return 0, nil, nil, err
	}
	proxyURL, err := c.mediaJobs.resolveMediaJobAccountProxyURL(ctx, account)
	if err != nil {
		return 0, nil, nil, err
	}
	if proxyURL != "" {
		proxyParsed, parseErr := url.Parse(proxyURL)
		if parseErr != nil {
			return 0, nil, nil, fmt.Errorf("产物探测代理地址不合法")
		}
		// SOCKS 分支由 transport 自行远端解析，DialGuard 无法钉住目标；
		// 探测面不使用该类代理，失败关闭（AC-028）。
		switch strings.ToLower(proxyParsed.Scheme) {
		case "http", "https":
		default:
			return 0, nil, nil, fmt.Errorf("产物探测不支持该代理类型")
		}
	}
	transport, err := sharedupstreamhttp.NewTransport(proxyURL, sharedupstreamhttp.TransportOptions{DialGuard: guard})
	if err != nil {
		return 0, nil, nil, err
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > chainMediaArtifactProbeMaxRedirects {
				return fmt.Errorf("产物探测重定向超过 %d 次", chainMediaArtifactProbeMaxRedirects)
			}
			if req.URL == nil || req.URL.Host == "" || req.URL.User != nil {
				return fmt.Errorf("产物探测重定向地址不合法")
			}
			switch strings.ToLower(req.URL.Scheme) {
			case "http", "https":
			default:
				return fmt.Errorf("产物探测重定向协议不受支持")
			}
			return guard.ValidateHost(req.Context(), req.URL.Hostname(), req.URL.Port())
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return 0, nil, nil, err
	}
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Accept", "video/*,*/*;q=0.1")
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	defer response.Body.Close()
	limit := end - start + 1
	if response.ContentLength > limit {
		return 0, nil, nil, gatewaymedia.ErrArtifactProbeTooLarge
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if readErr != nil {
		return 0, nil, nil, readErr
	}
	if int64(len(body)) > limit {
		return 0, nil, nil, gatewaymedia.ErrArtifactProbeTooLarge
	}
	return response.StatusCode, response.Header.Clone(), body, nil
}

// logMediaDurationProbeFailure 只记录稳定分类，不记录原始错误或签名 URL。
func (c *gatewayChain) logMediaDurationProbeFailure(record *gatewaymedia.MediaJobRecord, err error) {
	if c == nil || c.observability == nil || record == nil {
		return
	}
	reason := "unknown"
	switch {
	case errors.Is(err, gatewaymedia.ErrArtifactProbeInvalidURL):
		reason = "invalid_url"
	case errors.Is(err, gatewaymedia.ErrArtifactProbeUnsupported):
		reason = "range_unsupported"
	case errors.Is(err, gatewaymedia.ErrArtifactProbeTooLarge):
		reason = "too_large"
	case errors.Is(err, gatewaymedia.ErrArtifactProbeNoDuration):
		reason = "parse_failed"
	case errors.Is(err, gatewaymedia.ErrArtifactProbeFetch):
		reason = "fetch_failed"
	}
	c.observability.Logger().Warn("media_duration_probe_failed", map[string]any{
		"event": "media_duration_probe_failed",
		"jobId": record.ID, "model": record.RequestSnapshot.Model, "reason": reason,
	}, "视频产物时长探测失败，按 usage_missing 结算")
}
