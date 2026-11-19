// duration_probe.go 是 P4 视频产物时长探测的协议层（非对话媒体统一计费
// 设计 §8.4）：只负责 Range 请求编排、响应边界校验与容器时长解析。HTTP
// 传输由调用方注入（链层用带 DialGuard 的独立 client），本文件不发请求、
// 不落盘、不缓存。
package gatewaymedia

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediaduration"
)

const (
	// ArtifactProbeHeadBytes 是首个 Range 请求的字节数（设计 §8.4：前 2MB）。
	ArtifactProbeHeadBytes int64 = 2 << 20
	// ArtifactProbeTailBytes 是尾部 Range 请求的字节数（设计 §8.4：尾 4MB）。
	ArtifactProbeTailBytes int64 = 4 << 20
	// ArtifactProbeMaxBytes 是单次探测允许下载的总字节上限（设计 §8.4：≤8MB）。
	ArtifactProbeMaxBytes int64 = 8 << 20
	// ArtifactProbeTimeout 是单次探测的总超时（设计 §8.4：10s）。
	ArtifactProbeTimeout = 10 * time.Second
)

// 探测失败分类。调用方只据此记录稳定原因并落 usage_missing，不重试、不
// 整段下载。
var (
	// ErrArtifactProbeInvalidURL 产物地址不是可探测的 http/https 绝对 URL，
	// 或带 userinfo 凭据。
	ErrArtifactProbeInvalidURL = errors.New("产物探测地址无效")
	// ErrArtifactProbeUnsupported 上游不支持 Range（200 整包/416）或
	// Content-Range 缺失/非法。
	ErrArtifactProbeUnsupported = errors.New("产物不支持有界 Range 探测")
	// ErrArtifactProbeFetch 是探测 HTTP 请求失败（网络、超时、代理等）。
	ErrArtifactProbeFetch = errors.New("产物探测请求失败")
	// ErrArtifactProbeTooLarge 是响应体超过本次 Range 允许的字节数。
	ErrArtifactProbeTooLarge = errors.New("产物探测响应超出字节上限")
	// ErrArtifactProbeNoDuration 是片段无法解析出可靠时长。
	ErrArtifactProbeNoDuration = errors.New("产物片段未解析出时长")
)

// ArtifactRangeFetcher 拉取 [start,end] 闭区间字节。status 为上游状态码，
// header 至少包含 Content-Range/Content-Type；body 不得超过 end-start+1。
// 实现必须使用带 SSRF 防线的传输面，且不携带账户凭据。
type ArtifactRangeFetcher func(ctx context.Context, rawURL string, start, end int64) (status int, header http.Header, body []byte, err error)

// ProbeArtifactDuration 对远程视频产物做有界 Range 探测并返回秒数。
//
// 先取前 2MB；若对象更大，再取尾 4MB，总下载不超过 8MB。服务器不支持
// Range、响应越界、片段无法解析或超时都返回错误，由调用方按
// usage_missing 处理，绝不回退整段下载。
func ProbeArtifactDuration(ctx context.Context, rawURL string, fetch ArtifactRangeFetcher) (float64, error) {
	if fetch == nil {
		return 0, &ArtifactProbeError{Kind: ErrArtifactProbeFetch, Detail: "未配置 Range 拉取器"}
	}
	parsed, err := validateProbeURL(rawURL)
	if err != nil {
		return 0, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, ArtifactProbeTimeout)
	defer cancel()

	headStart, headEnd := int64(0), ArtifactProbeHeadBytes-1
	status, header, head, err := fetch(probeCtx, parsed.String(), headStart, headEnd)
	if err != nil {
		kind := ErrArtifactProbeFetch
		if errors.Is(err, ErrArtifactProbeTooLarge) {
			kind = ErrArtifactProbeTooLarge
		}
		return 0, &ArtifactProbeError{Kind: kind, Detail: "首个 Range 请求失败"}
	}
	opts := mediaduration.Options{
		ContentType: header.Get("Content-Type"),
		FormatHint:  artifactFormatHint(parsed),
	}

	switch status {
	case http.StatusPartialContent:
		start, end, total, ok := parseContentRange(header.Get("Content-Range"))
		if !ok || start != headStart || end < start || total <= end {
			return 0, &ArtifactProbeError{Kind: ErrArtifactProbeUnsupported, Detail: "Content-Range 缺失或不合法"}
		}
		if int64(len(head)) == 0 || int64(len(head)) > end-start+1 {
			return 0, &ArtifactProbeError{Kind: ErrArtifactProbeTooLarge, Detail: "首个 Range 响应体长度异常"}
		}
		if total <= int64(len(head)) {
			return parseProbeBytes(head[:total], opts)
		}
		return probeArtifactTail(probeCtx, fetch, parsed.String(), total, head, opts)
	case http.StatusOK:
		// 服务器忽略 Range 返回整包：仅当对象本身不超过首个 Range 上限时
		// 可直接解析；否则拒绝，避免整段下载（设计 §8.4）。
		if int64(len(head)) == 0 || int64(len(head)) > ArtifactProbeHeadBytes {
			return 0, &ArtifactProbeError{Kind: ErrArtifactProbeUnsupported, Detail: "服务器忽略 Range 且响应超过 2MB"}
		}
		return parseProbeBytes(head, opts)
	case http.StatusRequestedRangeNotSatisfiable:
		return 0, &ArtifactProbeError{Kind: ErrArtifactProbeUnsupported, Detail: "服务器返回 416"}
	default:
		return 0, &ArtifactProbeError{Kind: ErrArtifactProbeUnsupported, Detail: fmt.Sprintf("服务器返回状态码 %d", status)}
	}
}

// probeArtifactTail 在对象大于首个 Range 时取尾部 4MB，并与头段一起解析。
func probeArtifactTail(ctx context.Context, fetch ArtifactRangeFetcher, rawURL string, total int64, head []byte, opts mediaduration.Options) (float64, error) {
	tailStart := total - ArtifactProbeTailBytes
	if tailStart < 0 {
		tailStart = 0
	}
	tailEnd := total - 1
	status, header, tail, err := fetch(ctx, rawURL, tailStart, tailEnd)
	if err != nil {
		kind := ErrArtifactProbeFetch
		if errors.Is(err, ErrArtifactProbeTooLarge) {
			kind = ErrArtifactProbeTooLarge
		}
		return 0, &ArtifactProbeError{Kind: kind, Detail: "尾部 Range 请求失败"}
	}
	if status != http.StatusPartialContent {
		return 0, &ArtifactProbeError{Kind: ErrArtifactProbeUnsupported, Detail: fmt.Sprintf("尾部 Range 返回状态码 %d", status)}
	}
	start, end, gotTotal, ok := parseContentRange(header.Get("Content-Range"))
	if !ok || start != tailStart || end < start || gotTotal != total || end > total-1 {
		return 0, &ArtifactProbeError{Kind: ErrArtifactProbeUnsupported, Detail: "尾部 Content-Range 缺失或不合法"}
	}
	if int64(len(tail)) == 0 || int64(len(tail)) > ArtifactProbeTailBytes || int64(len(tail)) > end-start+1 {
		return 0, &ArtifactProbeError{Kind: ErrArtifactProbeTooLarge, Detail: "尾部 Range 响应体长度异常"}
	}
	if int64(len(head))+int64(len(tail)) > ArtifactProbeMaxBytes {
		return 0, &ArtifactProbeError{Kind: ErrArtifactProbeTooLarge, Detail: "探测下载总量超过 8MB 上限"}
	}
	segments := []mediaduration.Segment{
		{Offset: 0, Data: head},
		{Offset: tailStart, Data: tail},
	}
	result, ok := mediaduration.ParseSegments(segments, total, opts)
	if !ok || result.Seconds <= 0 {
		return 0, &ArtifactProbeError{Kind: ErrArtifactProbeNoDuration, Detail: "头/尾片段未解析出时长"}
	}
	return result.Seconds, nil
}

func parseProbeBytes(data []byte, opts mediaduration.Options) (float64, error) {
	result, ok := mediaduration.Parse(data, opts)
	if !ok || result.Seconds <= 0 {
		return 0, &ArtifactProbeError{Kind: ErrArtifactProbeNoDuration, Detail: "完整小对象未解析出时长"}
	}
	return result.Seconds, nil
}

// validateProbeURL 在 chain 的 artifacturl 预检之外再做探测面收紧：拒绝
// userinfo（探测不携带任何凭据），只允许 http/https 绝对地址。
func validateProbeURL(raw string) (*url.URL, error) {
	if err := ValidateArtifactURL(raw); err != nil {
		return nil, &ArtifactProbeError{Kind: ErrArtifactProbeInvalidURL, Detail: "scheme 或 host 不合法"}
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return nil, &ArtifactProbeError{Kind: ErrArtifactProbeInvalidURL, Detail: "地址无法解析"}
	}
	if parsed.User != nil {
		return nil, &ArtifactProbeError{Kind: ErrArtifactProbeInvalidURL, Detail: "地址包含 userinfo 凭据"}
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, &ArtifactProbeError{Kind: ErrArtifactProbeInvalidURL, Detail: "地址协议不受支持"}
	}
	return parsed, nil
}

// artifactFormatHint 从地址扩展名提取容器提示；查询串/签名参数不参与。
func artifactFormatHint(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(parsed.Path)), ".")
	switch ext {
	case "mp4", "m4v", "m4a", "mov", "qt":
		return "mp4"
	case "webm", "mkv":
		return "webm"
	default:
		return ""
	}
}

// parseContentRange 解析 "bytes start-end/total"；total 必须为确定值，
// "*" 或格式不符一律失败关闭。
func parseContentRange(value string) (start, end, total int64, ok bool) {
	value = strings.TrimSpace(value)
	const prefix = "bytes "
	if len(value) < len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		return 0, 0, 0, false
	}
	rangeAndTotal := strings.TrimSpace(value[len(prefix):])
	parts := strings.SplitN(rangeAndTotal, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "*" {
		return 0, 0, 0, false
	}
	rangeParts := strings.SplitN(strings.TrimSpace(parts[0]), "-", 2)
	if len(rangeParts) != 2 {
		return 0, 0, 0, false
	}
	start, startErr := strconv.ParseInt(strings.TrimSpace(rangeParts[0]), 10, 64)
	end, endErr := strconv.ParseInt(strings.TrimSpace(rangeParts[1]), 10, 64)
	total, totalErr := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if startErr != nil || endErr != nil || totalErr != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

// ArtifactProbeError 保留稳定失败分类与不含签名 URL 的简短说明。
type ArtifactProbeError struct {
	Kind   error
	Detail string
}

func (e *ArtifactProbeError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail == "" {
		return e.Kind.Error()
	}
	return e.Kind.Error() + ": " + e.Detail
}

func (e *ArtifactProbeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind
}
