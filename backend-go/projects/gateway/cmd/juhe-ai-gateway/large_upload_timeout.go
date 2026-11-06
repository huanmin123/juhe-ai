package main

import (
	"net/http"
	"strings"
	"time"
)

// 大请求体路由的读超时豁免（2026-11-06 安全加固复审 blocker）：
// mainServer.ReadTimeout 是全局 body 慢速占连上限，但 /v1/files 的契约单文件
// 上限是 512MiB，64MiB 的图片/音频车道也高于默认窗口能覆盖的速率。这里在
// 监听最外层按路由延长连接读 deadline；必须包在全部 ResponseWriter 包装器
// 之前，http.NewResponseController 才能触达原始连接。
const (
	defaultLargeUploadReadTimeout = 60 * time.Minute // /v1/files：512MiB ≈ 146KiB/s
	mediaUploadReadTimeout        = 30 * time.Minute // 64MiB 媒体车道 ≈ 36KiB/s
)

func withLargeUploadReadTimeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if timeout, ok := largeUploadReadTimeout(r); ok {
			// HTTP/2 或测试 writer 可能不支持 SetReadDeadline；不支持时保留
			// 服务级 ReadTimeout，不改变请求处理。
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(timeout))
		}
		next.ServeHTTP(w, r)
	})
}

// largeUploadReadTimeout reports the extended body-read window for the routes
// whose documented body limits cannot fit the default 10-minute window. Both
// the canonical /v1 form and the documented bare form (chain_gateway_root.go
// rewrites it to /v1 before dispatch) select the same window.
func largeUploadReadTimeout(r *http.Request) (time.Duration, bool) {
	if r == nil || r.Method != http.MethodPost {
		return 0, false
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/v1" || strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	switch path {
	case "/files":
		return defaultLargeUploadReadTimeout, true
	case "/images/edits", "/audio/transcriptions", "/audio/translations":
		return mediaUploadReadTimeout, true
	default:
		return 0, false
	}
}
