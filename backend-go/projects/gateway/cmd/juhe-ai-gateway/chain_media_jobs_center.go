package main

// 任务中心内容实时获取面（零资源存储 §8.1 不变）：GET /__aisys__/api/
// my-media-jobs/{id}/content（RequireSession 归属钉死）与 GET /__aisys__/
// api/media-jobs/{id}/content（requireAdmin）。鉴权主体从 /v1 content 面的
// API Key 换成登录会话/管理员（2026-10-17 任务中心闭环批）：链路原语与
// serveMediaJobContent 完全同源（hydrateAccount + adapter BuildContentRequest/
// Artifact 直连 + mediaJobRequestUpstream），边取边回、不落盘不缓存——
// 任务中心抽屉因此能对零存储任务（长音频转写，产物=转写结果 JSON）实时
// 取回展示，而非只留"需通过 API Key 调用"的提示。

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
)

// serveMediaJobsCenterContent 服务任务中心两面的内容获取路由。ownerScoped
// 为真时按当前登录用户归属取行（跨属主与未命中统一 404，不泄露存在性），
// 假时为管理员全量面。
func (c *gatewayChain) serveMediaJobsCenterContent(w http.ResponseWriter, r *http.Request, ownerScoped bool) {
	if c == nil || c.mediaJobs == nil || c.mediaJobs.repo == nil {
		writeMediaJobsCenterError(w, http.StatusServiceUnavailable, "媒体任务面未装配")
		return
	}
	var (
		record *gatewaymedia.MediaJobRecord
		err    error
	)
	if ownerScoped {
		auth := authsys.AuthContextFrom(r)
		if auth == nil || strings.TrimSpace(auth.SystemAccountID) == "" {
			writeMediaJobsCenterError(w, http.StatusUnauthorized, "缺少登录身份")
			return
		}
		record, err = c.mediaJobs.repo.GetByOwnerAndID(r.Context(), strings.TrimSpace(r.PathValue("id")), auth.SystemAccountID)
	} else {
		record, err = c.mediaJobs.repo.GetManagementByID(r.Context(), strings.TrimSpace(r.PathValue("id")))
	}
	if errors.Is(err, gatewaymedia.ErrMediaJobNotFound) {
		writeMediaJobsCenterError(w, http.StatusNotFound, "媒体任务不存在")
		return
	}
	if err != nil {
		writeMediaJobsCenterError(w, http.StatusInternalServerError, "查询媒体任务失败")
		return
	}
	if record.Status != gatewaymedia.JobStatusCompleted {
		writeMediaJobsCenterError(w, http.StatusConflict, "任务未完成，产物不可下载（当前状态 "+string(record.Status)+"）")
		return
	}
	// 账户水合与可连性校验：与 loadMediaVideoAccount 同判据的安静版（错误
	// 以 JSON 呈现，不经 /v1 错误渲染面）。
	account, err := c.mediaJobs.hydrateAccount(r.Context(), record.AccountID)
	if err != nil {
		writeMediaJobsCenterError(w, http.StatusInternalServerError, "水合媒体任务账户失败")
		return
	}
	if account == nil || account.Deleted || !chainMediaJobAccountPollEligible(account, record, time.Now().UTC()) {
		writeMediaJobsCenterError(w, http.StatusServiceUnavailable, "媒体任务账户不可用（已删除或禁用）")
		return
	}
	adapter := chainMediaJobTaskAdapterFor(record.Kind, account.ProviderCode)
	if adapter == nil {
		writeMediaJobsCenterError(w, http.StatusServiceUnavailable, "该任务种类暂不支持内容获取")
		return
	}
	defaultContentType := "video/mp4"
	if record.Kind == gatewaymedia.JobKindAudioTranscription {
		defaultContentType = "application/json"
	}
	var response *gatewayupstream.GatewayUpstreamResponse
	var upstreamErr error
	if adapter.ContentFromArtifact() {
		artifactURL := strings.TrimSpace(record.Artifact.ContentURL)
		if artifactURL == "" {
			writeMediaJobsCenterError(w, http.StatusGone, "产物已过期或已被上游清除")
			return
		}
		response, upstreamErr = c.mediaJobArtifactRequestUpstream(r.Context(), account, artifactURL)
	} else {
		method, path, contentHeader, contentBody := adapter.BuildContentRequest(record.UpstreamJobID)
		response, upstreamErr = c.mediaJobRequestUpstream(r.Context(), account, record.Kind, method, path, contentHeader, contentBody)
	}
	if upstreamErr != nil {
		writeMediaJobsCenterError(w, http.StatusBadGateway, "上游产物获取失败（网络错误）")
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.Status() == http.StatusNotFound || response.Status() == http.StatusGone {
		writeMediaJobsCenterError(w, http.StatusGone, "产物已过期或已被上游清除")
		return
	}
	if response.Status() < http.StatusOK || response.Status() >= http.StatusMultipleChoices {
		writeMediaJobsCenterError(w, http.StatusBadGateway, "上游产物获取失败（状态码 "+strconv.Itoa(response.Status())+"）")
		return
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = defaultContentType
	}
	w.Header().Set("Content-Type", contentType)
	if contentLength := response.Header.Get("Content-Length"); contentLength != "" {
		w.Header().Set("Content-Length", contentLength)
	}
	w.Header().Set("Content-Disposition", "attachment; filename=\""+record.ID+"-content\"")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	// 零资源存储（§8.1）：边收边转发，不落盘不缓存。
	_, _ = io.Copy(w, response.Body)
}

// writeMediaJobsCenterError 以 /v1 媒体面同构的平铺错误信封写响应（任务
// 中心前端按 JSON 解析 message 展示）。
func writeMediaJobsCenterError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"message":` + strconv.Quote(message) + `,"type":"media_job_content_error"}}`))
}
