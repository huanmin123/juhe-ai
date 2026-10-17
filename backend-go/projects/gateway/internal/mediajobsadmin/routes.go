// Package mediajobsadmin 是 M2 媒体任务管理面只读面（媒体设计 §8.2
// "管理面新增只读媒体任务列表页"）：GET /__aisys__/api/media-jobs 列表、
// GET /__aisys__/api/media-jobs/{id} 详情（2026-10-17 增补）为 requireAdmin
// 全量面；GET /__aisys__/api/my-media-jobs 列表与 /{id} 详情（2026-10-17
// 增补）为 RequireSession 用户面，归属钉死调用者且省略上游任务号。
// GET …/{id}/content 内容实时获取（2026-10-17 任务中心闭环批）：两面对称
// 各一条，处理函数由组合根注入（MediaJobContentHandler，实现复用 /v1
// content 面链路原语），本包只做鉴权门禁与路由——零资源存储 §8.1 不变，
// 边取边回不落盘。权限沿
// 使用记录列表（requireAdmin，statreads usage-records 同款门禁）。路由注册
// 模式沿 providers/routes.go 先例（prefix + Auth 门禁 + kernel envelope）；
// 行数据经 gatewaymedia jobsrepo 的管理面查询（ListManagement /
// GetManagementByID / GetByOwnerAndID），本包只做参数解析与渲染，无写端点。
// 独立小包而非并入 gatewaymedia：后者被 kernel 间接引用（import cycle），
// 管理面依赖 kernel/authsys 必须外置。
package mediajobsadmin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// MediaJobContentHandler 是任务中心内容实时获取的处理函数（组合根注入，
// cmd 侧实现：复用 /v1 content 面链路原语，鉴权主体为登录会话/管理员）。
// ownerScoped 为真时按当前登录用户归属过滤；nil 时 content 路由 503。
type MediaJobContentHandler func(w http.ResponseWriter, r *http.Request, ownerScoped bool)

// Deps bundles the media-jobs management surface collaborators.
type Deps struct {
	// Repo 是 media_jobs 仓储（组合根与 /v1 任务面同源业务库构造）。
	Repo *gatewaymedia.MediaJobsRepo
	Auth *authsys.Deps
	// ContentHandler 是 {id}/content 两路由的内容处理函数（可选；未装配时
	// 路由返回 503——任务中心闭环需要它，组合根在 chain 组装后回填）。
	ContentHandler MediaJobContentHandler
}

// Mount 注册管理面与用户面媒体任务路由（只读；管理面 requireAdmin，用户面
// RequireSession 钉死调用者归属）。
func (d *Deps) Mount(k *kernel.Kernel) {
	k.Register("GET /__aisys__/api/media-jobs", d.Auth.RequireAdmin(http.HandlerFunc(d.listHandler)))
	k.Register("GET /__aisys__/api/media-jobs/{id}", d.Auth.RequireAdmin(http.HandlerFunc(d.detailHandler)))
	k.Register("GET /__aisys__/api/my-media-jobs", d.Auth.RequireSession(true)(http.HandlerFunc(d.myListHandler)))
	k.Register("GET /__aisys__/api/my-media-jobs/{id}", d.Auth.RequireSession(true)(http.HandlerFunc(d.myDetailHandler)))
	// 内容实时获取（任务中心闭环批）：两面对称，处理函数组合根注入。
	k.Register("GET /__aisys__/api/media-jobs/{id}/content", d.Auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.serveContent(w, r, false)
	})))
	k.Register("GET /__aisys__/api/my-media-jobs/{id}/content", d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.serveContent(w, r, true)
	})))
}

// serveContent 分发内容处理函数；未装配时 503（组合根 chain 组装前窗口）。
func (d *Deps) serveContent(w http.ResponseWriter, r *http.Request, ownerScoped bool) {
	if d.ContentHandler == nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, "媒体任务内容获取面未装配")
		return
	}
	d.ContentHandler(w, r, ownerScoped)
}

// mediaJobPromptSummaryLimit 是 promptSummary 的截断上限（rune 计数，列表
// 行不透传完整 prompt，保持响应轻量）。
const mediaJobPromptSummaryLimit = 120

// mediaJobListRow 是列表行渲染形状（camelCase，前端页面对齐契约）。
type mediaJobListRow struct {
	ID            string                      `json:"id"`
	Kind          string                      `json:"kind"`
	Status        string                      `json:"status"`
	ProviderCode  string                      `json:"providerCode"`
	ProviderJobID string                      `json:"providerJobId,omitempty"`
	Model         string                      `json:"model"`
	PromptSummary string                      `json:"promptSummary"`
	Seconds       *float64                    `json:"seconds,omitempty"`
	Size          string                      `json:"size,omitempty"`
	APIKeyID      string                      `json:"apiKeyId"`
	AccountID     string                      `json:"accountId"`
	CreatedAt     string                      `json:"createdAt"`
	UpdatedAt     string                      `json:"updatedAt"`
	CostUsd       float64                     `json:"costUsd"`
	Error         *gatewaymedia.MediaJobError `json:"error,omitempty"`
}

// listHandler 服务 GET /__aisys__/api/media-jobs：limit（默认 20，上限 100）、
// offset、status?/kind?/api_key_id?/account_id? 过滤；非法数值回落默认（与
// /v1/videos limit 解析同语义，不 400）。响应 {data:{rows,total}} kernel
// envelope。
func (d *Deps) listHandler(w http.ResponseWriter, r *http.Request) {
	if d.Repo == nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, "媒体任务面未装配")
		return
	}
	query := gatewaymedia.MediaJobListQuery{
		Status:               strings.TrimSpace(r.URL.Query().Get("status")),
		Kind:                 strings.TrimSpace(r.URL.Query().Get("kind")),
		APIKeyID:             strings.TrimSpace(r.URL.Query().Get("api_key_id")),
		AccountID:            strings.TrimSpace(r.URL.Query().Get("account_id")),
		OwnerSystemAccountID: strings.TrimSpace(r.URL.Query().Get("system_account_id")),
		Limit:                mediaJobQueryInt(r, "limit", 20, 1, 100),
		Offset:               mediaJobQueryInt(r, "offset", 0, 0, 1<<30),
	}
	rows, total, err := d.Repo.ListManagement(r.Context(), query)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "查询媒体任务列表失败", err)
		return
	}
	payload := make([]mediaJobListRow, 0, len(rows))
	for _, record := range rows {
		payload = append(payload, mediaJobListPayload(record, true))
	}
	kernel.WriteOK(w, map[string]any{"rows": payload, "total": total}, "")
}

// mediaJobListPayload 把仓储行渲染为列表行；includeProviderJobID 为假时省略
// 上游任务号（用户面不透出平台内部信息）。
func mediaJobListPayload(record *gatewaymedia.MediaJobRecord, includeProviderJobID bool) mediaJobListRow {
	row := mediaJobListRow{
		ID:            record.ID,
		Kind:          string(record.Kind),
		Status:        string(record.Status),
		ProviderCode:  record.ProviderCode,
		Model:         record.RequestSnapshot.Model,
		PromptSummary: mediaJobPromptSummary(record.RequestSnapshot.Prompt),
		Seconds:       record.RequestSnapshot.Seconds,
		Size:          record.RequestSnapshot.Size,
		APIKeyID:      record.APIKeyID,
		AccountID:     record.AccountID,
		CreatedAt:     mediaJobListTime(record.CreatedAt),
		UpdatedAt:     mediaJobListTime(record.UpdatedAt),
		CostUsd:       record.CostUsd,
		Error:         record.Error,
	}
	if includeProviderJobID {
		row.ProviderJobID = record.UpstreamJobID
	}
	return row
}

// mediaJobDetailArtifact 是详情行的产物定位面（零资源存储 §8.1：只透出
// 上游定位指针与时效，不代理上游实时取回；两者皆空时整对象省略）。
type mediaJobDetailArtifact struct {
	ContentURL string `json:"contentUrl,omitempty"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
}

// mediaJobDetailUsage 是详情行的用量面（结构化计量 + Raw 原生用量字段；
// 全空时整对象省略）。
type mediaJobDetailUsage struct {
	OutputVideoSeconds *float64       `json:"outputVideoSeconds,omitempty"`
	AudioInputSeconds  *float64       `json:"audioInputSeconds,omitempty"`
	Raw                map[string]any `json:"raw,omitempty"`
}

// mediaJobDetailRow 是详情行渲染形状（列表行字段 + 全量细节：prompt 全文、
// 请求参数快照回显、产物定位、用量、参数回显列表）。
type mediaJobDetailRow struct {
	ID                     string                      `json:"id"`
	Kind                   string                      `json:"kind"`
	Status                 string                      `json:"status"`
	ProviderCode           string                      `json:"providerCode"`
	ProviderJobID          string                      `json:"providerJobId,omitempty"`
	Model                  string                      `json:"model"`
	Prompt                 string                      `json:"prompt,omitempty"`
	Seconds                *float64                    `json:"seconds,omitempty"`
	Size                   string                      `json:"size,omitempty"`
	N                      int                         `json:"n,omitempty"`
	Language               string                      `json:"language,omitempty"`
	ProviderOptionsApplied []string                    `json:"providerOptionsApplied,omitempty"`
	APIKeyID               string                      `json:"apiKeyId"`
	AccountID              string                      `json:"accountId"`
	CreatedAt              string                      `json:"createdAt"`
	UpdatedAt              string                      `json:"updatedAt"`
	CostUsd                float64                     `json:"costUsd"`
	Artifact               *mediaJobDetailArtifact     `json:"artifact,omitempty"`
	Usage                  *mediaJobDetailUsage        `json:"usage,omitempty"`
	ParamsApplied          []string                    `json:"paramsApplied,omitempty"`
	ParamsIgnored          []string                    `json:"paramsIgnored,omitempty"`
	Error                  *gatewaymedia.MediaJobError `json:"error,omitempty"`
}

// detailHandler 服务 GET /__aisys__/api/media-jobs/{id}：按 id 取单行全量
// （无归属过滤，requireAdmin 已限定管理员）；未命中 404 不区分存在性语义
// 之外的细节。
func (d *Deps) detailHandler(w http.ResponseWriter, r *http.Request) {
	if d.Repo == nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, "媒体任务面未装配")
		return
	}
	record, err := d.Repo.GetManagementByID(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if errors.Is(err, gatewaymedia.ErrMediaJobNotFound) {
		kernel.WriteError(w, http.StatusNotFound, "媒体任务不存在")
		return
	}
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "查询媒体任务详情失败", err)
		return
	}
	kernel.WriteOK(w, mediaJobDetailPayload(record, true), "")
}

// myListHandler 服务 GET /__aisys__/api/my-media-jobs：RequireSession 后把
// 归属钉死当前登录用户（api_keys.system_account_id），只能看到自己名下任务；
// 行形状同管理面但省略上游任务号。身份缺失按 401 拒绝——归属条件不允许
// 空属主回退成不过滤。
func (d *Deps) myListHandler(w http.ResponseWriter, r *http.Request) {
	if d.Repo == nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, "媒体任务面未装配")
		return
	}
	auth := authsys.AuthContextFrom(r)
	if auth == nil || strings.TrimSpace(auth.SystemAccountID) == "" {
		kernel.WriteError(w, http.StatusUnauthorized, "缺少登录身份")
		return
	}
	query := gatewaymedia.MediaJobListQuery{
		Status:               strings.TrimSpace(r.URL.Query().Get("status")),
		Kind:                 strings.TrimSpace(r.URL.Query().Get("kind")),
		APIKeyID:             strings.TrimSpace(r.URL.Query().Get("api_key_id")),
		OwnerSystemAccountID: auth.SystemAccountID,
		Limit:                mediaJobQueryInt(r, "limit", 20, 1, 100),
		Offset:               mediaJobQueryInt(r, "offset", 0, 0, 1<<30),
	}
	rows, total, err := d.Repo.ListManagement(r.Context(), query)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "查询媒体任务列表失败", err)
		return
	}
	payload := make([]mediaJobListRow, 0, len(rows))
	for _, record := range rows {
		payload = append(payload, mediaJobListPayload(record, false))
	}
	kernel.WriteOK(w, map[string]any{"rows": payload, "total": total}, "")
}

// myDetailHandler 服务 GET /__aisys__/api/my-media-jobs/{id}：按 id + 当前
// 登录用户归属取行，跨属主与未命中统一 404（不泄露存在性）。
func (d *Deps) myDetailHandler(w http.ResponseWriter, r *http.Request) {
	if d.Repo == nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, "媒体任务面未装配")
		return
	}
	auth := authsys.AuthContextFrom(r)
	if auth == nil || strings.TrimSpace(auth.SystemAccountID) == "" {
		kernel.WriteError(w, http.StatusUnauthorized, "缺少登录身份")
		return
	}
	record, err := d.Repo.GetByOwnerAndID(r.Context(), strings.TrimSpace(r.PathValue("id")), auth.SystemAccountID)
	if errors.Is(err, gatewaymedia.ErrMediaJobNotFound) {
		kernel.WriteError(w, http.StatusNotFound, "媒体任务不存在")
		return
	}
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "查询媒体任务详情失败", err)
		return
	}
	kernel.WriteOK(w, mediaJobDetailPayload(record, false), "")
}

// mediaJobDetailPayload 把仓储行渲染为详情响应对象（JSON 列按空省略）；
// includeProviderJobID 为假时省略上游任务号（用户面不透出平台内部信息）。
func mediaJobDetailPayload(record *gatewaymedia.MediaJobRecord, includeProviderJobID bool) *mediaJobDetailRow {
	row := &mediaJobDetailRow{
		ID:                     record.ID,
		Kind:                   string(record.Kind),
		Status:                 string(record.Status),
		ProviderCode:           record.ProviderCode,
		Model:                  record.RequestSnapshot.Model,
		Prompt:                 strings.TrimSpace(record.RequestSnapshot.Prompt),
		Seconds:                record.RequestSnapshot.Seconds,
		Size:                   record.RequestSnapshot.Size,
		N:                      record.RequestSnapshot.N,
		Language:               record.RequestSnapshot.Language,
		ProviderOptionsApplied: record.RequestSnapshot.ProviderOptionsApplied,
		APIKeyID:               record.APIKeyID,
		AccountID:              record.AccountID,
		CreatedAt:              mediaJobListTime(record.CreatedAt),
		UpdatedAt:              mediaJobListTime(record.UpdatedAt),
		CostUsd:                record.CostUsd,
		ParamsApplied:          record.ParamsApplied,
		ParamsIgnored:          record.ParamsIgnored,
		Error:                  record.Error,
	}
	if includeProviderJobID {
		row.ProviderJobID = record.UpstreamJobID
	}
	if contentURL := strings.TrimSpace(record.Artifact.ContentURL); contentURL != "" || record.Artifact.ExpiresAt != nil {
		artifact := &mediaJobDetailArtifact{ContentURL: contentURL}
		if record.Artifact.ExpiresAt != nil {
			artifact.ExpiresAt = mediaJobListTime(*record.Artifact.ExpiresAt)
		}
		row.Artifact = artifact
	}
	if record.Usage.OutputVideoSeconds != nil || record.Usage.AudioInputSeconds != nil || len(record.Usage.Raw) > 0 {
		row.Usage = &mediaJobDetailUsage{
			OutputVideoSeconds: record.Usage.OutputVideoSeconds,
			AudioInputSeconds:  record.Usage.AudioInputSeconds,
			Raw:                record.Usage.Raw,
		}
	}
	return row
}

// mediaJobPromptSummary 截断 prompt 到 rune 上限（超出加省略号标记）。
func mediaJobPromptSummary(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if utf8.RuneCountInString(trimmed) <= mediaJobPromptSummaryLimit {
		return trimmed
	}
	runes := []rune(trimmed)
	return string(runes[:mediaJobPromptSummaryLimit]) + "…"
}

// mediaJobListTime 按毫秒 RFC3339 UTC 渲染（与 media_jobs 时间列同构）。
func mediaJobListTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

// mediaJobQueryInt 解析整数查询参数（缺省/非法/越界回落 fallback）。
func mediaJobQueryInt(r *http.Request, key string, fallback, minValue, maxValue int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < minValue || parsed > maxValue {
		return fallback
	}
	return parsed
}
