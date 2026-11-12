// Package gatewaymedia 承载媒体域（音频/视频）网关链的车道判定与后续媒体
// IR/adapter 基建（媒体设计 §3/§5）。本文件是媒体路径族的唯一词表落点：
// gatewayopenai.ResolveRequestLane 在入口路径判断阶段消费本函数，使
// /v1/audio/* 请求解析为 LaneAudio、POST /v1/videos 解析为 LaneVideo（媒体
// 车道豁免 speed-first 与同账户瞬态重试），模型名前缀硬编码清单（image
// lane）仍在 gatewayopenai/lane.go。
package gatewaymedia

import (
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// RequestLaneForPath 按方法 + 路径族判定请求车道（媒体设计 §3：路径族
// 优先，POST /v1/audio/* 同步端点 → LaneAudio，POST /v1/videos 与 POST
// /v1/tasks/audio（长音频异步任务，M3f——§3 裁决 /v1/tasks/audio* 归 video
// lane；旧 /v1/audio/jobs 词表随 2026-11-12 New API 批保留供内部判定）→
// LaneVideo；M5b：GET /v1/realtime WS 升级请求 → LaneRealtime，
// Realtime 设计 §6——任务面请求与 ephemeral token 签发端点不是 WS 升级
// 形态，不在此判定）。第二返回值报告路径是否命中已知媒体路径族；未命中时
// 由调用方回退其它判定（如模型目录 mode）。判定是纯路径匹配，与请求体无关：
// /v1/audio/speech（TTS）、/v1/audio/transcriptions 与 /v1/audio/translations
// （STT）、/v1/video/generations（视频创建，M2）、/v1/tasks/audio（长转写
// 创建，M3f）、/v1/realtime（WS 升级，M5b，GET 方法限定）。
// 任务面请求（GET/DELETE 媒体任务族、content 下载）不进派发循环（账户亲和
// 直连，媒体设计 §7），不是请求车道关注的请求，不在此判定。生产消费方是
// gatewayopenai.ResolveRequestLane（请求链车道判定）。
func RequestLaneForPath(method, path string) (gatewayproto.RequestLane, bool) {
	if method == http.MethodGet {
		switch stripQuery(path) {
		case "/v1/realtime", "/realtime":
			return gatewayproto.LaneRealtime, true
		}
		return "", false
	}
	if method != http.MethodPost {
		return "", false
	}
	normalized := stripQuery(path)
	// B7 工具端点：向量/重排/安全走无状态 lane，不产生会话亲和；子路径
	//（如 /v1/embeddings/multimodal）同属工具路径族，不能落回 text lane。
	for _, family := range []string{"/embeddings", "/rerank", "/moderations"} {
		if normalized == family || normalized == "/v1"+family || strings.HasPrefix(normalized, family+"/") || strings.HasPrefix(normalized, "/v1"+family+"/") {
			return gatewayproto.LaneUtility, true
		}
	}
	switch normalized {
	case "/v1/audio/speech", "/audio/speech":
		return gatewayproto.LaneAudio, true
	case "/v1/audio/transcriptions", "/audio/transcriptions",
		"/v1/audio/translations", "/audio/translations":
		return gatewayproto.LaneAudio, true
	case "/v1/video/generations", "/video/generations":
		// New API 兼容面创建端点（2026-11-12 起对外唯一视频创建路径）。
		return gatewayproto.LaneVideo, true
	case "/v1/tasks/audio", "/tasks/audio":
		// New API 通用任务面长音频创建端点（2026-11-12 起对外唯一长音频创建
		// 路径；§3 裁决长音频异步任务归 video lane）。
		return gatewayproto.LaneVideo, true
	case "/v1/videos", "/videos":
		// 旧 /v1/videos 已硬 404（不再注册路由）；词表保留供内部判定复用。
		return gatewayproto.LaneVideo, true
	case "/v1/audio/jobs", "/audio/jobs":
		// 旧 /v1/audio/jobs 已硬 404（对外改为 /v1/tasks/audio）；词表保留供
		// 内部形态判定与历史测试复用。
		return gatewayproto.LaneVideo, true
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
