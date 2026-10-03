// Package gatewaymedia 承载媒体域（音频/视频）网关链的车道判定与后续媒体
// IR/adapter 基建（音频设计 §3/§5）。本文件是音频路径族的唯一词表落点：
// gatewayopenai.ResolveRequestLane 在入口路径判断阶段消费本函数，使
// /v1/audio/* 请求解析为 LaneAudio（媒体车道豁免 speed-first 与同账户瞬态
// 重试），模型名前缀硬编码清单（image lane）仍在 gatewayopenai/lane.go。
package gatewaymedia

import (
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// RequestLaneForPath 按方法 + 路径族判定请求车道（音频设计 §3：路径族
// 优先，POST /v1/audio/* 同步端点 → LaneAudio）。第二返回值报告路径是否
// 命中已知音频路径族；未命中时由调用方回退其它判定（如模型目录 mode）。
// 判定是纯路径匹配，与请求体无关：/v1/audio/speech（TTS）、
// /v1/audio/transcriptions 与 /v1/audio/translations（STT）。生产消费方是
// gatewayopenai.ResolveRequestLane（请求链车道判定）。
func RequestLaneForPath(method, path string) (gatewayproto.RequestLane, bool) {
	if method != http.MethodPost {
		return "", false
	}
	switch stripQuery(path) {
	case "/v1/audio/speech", "/audio/speech":
		return gatewayproto.LaneAudio, true
	case "/v1/audio/transcriptions", "/audio/transcriptions",
		"/v1/audio/translations", "/audio/translations":
		return gatewayproto.LaneAudio, true
	default:
		return "", false
	}
}

// stripQuery 去掉查询串并做最小归一（前导斜杠），保持大小写不敏感比较
// 由调用侧路径来源决定；OpenAI 路径族均为小写字面量，这里直接小写化。
func stripQuery(path string) string {
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	path = strings.ToLower(strings.TrimSpace(path))
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}
