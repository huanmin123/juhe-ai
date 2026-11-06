// artifacturl.go 是媒体产物下载地址的 scheme 白名单（安全审计 SEC-01）：
// 上游轮询/创建响应里的 artifact.contentUrl 会经 media_jobs 落库、管理面详情
// 透出、前端详情抽屉直接渲染为 video/audio src 与 a.href 裸链接。上游响应
// 属于不可信输入，只允许 http/https 绝对地址，其余（javascript:/data:/file:/、
// 相对路径、无 host 等）一律视为上游协议违约。
package gatewaymedia

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateArtifactURL 校验媒体产物下载地址：空串视为无产物定位返回 nil；
// 非空必须能被 net/url 解析、scheme（小写归一后）为 http/https 且 host 非空，
// 否则返回中文错误。
func ValidateArtifactURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("产物下载地址无法解析: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("产物下载地址协议不受支持: %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("产物下载地址缺少主机名: %q", trimmed)
	}
	return nil
}

// SafeArtifactURL 是 ValidateArtifactURL 的读面包装：返回可安全透出/渲染的
// 地址。TrimSpace 后为空、校验失败均返回 ("", false)；成功返回去空白后的
// 原值（保留原始大小写与查询串）。
func SafeArtifactURL(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	if err := ValidateArtifactURL(trimmed); err != nil {
		return "", false
	}
	return trimmed, true
}
