// Package mediajobsadmin 是 M2 媒体任务管理面只读列表（媒体设计 §8.2
// "管理面新增只读媒体任务列表页"）：GET /__aisys__/api/media-jobs，权限沿
// 使用记录列表（requireAdmin，statreads usage-records 同款门禁）。路由注册
// 模式沿 providers/routes.go 先例（prefix + Auth.RequireAdmin + kernel
// envelope）；行数据经 gatewaymedia jobsrepo 的管理面查询（ListManagement），
// 本包只做参数解析与渲染，无写端点。独立小包而非并入 gatewaymedia：后者被
// kernel 间接引用（import cycle），管理面依赖 kernel/authsys 必须外置。
package mediajobsadmin

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Deps bundles the media-jobs management surface collaborators.
type Deps struct {
	// Repo 是 media_jobs 仓储（组合根与 /v1 任务面同源业务库构造）。
	Repo *gatewaymedia.MediaJobsRepo
	Auth *authsys.Deps
}

// Mount 注册管理面媒体任务路由（只读，requireAdmin）。
func (d *Deps) Mount(k *kernel.Kernel) {
	k.Register("GET /__aisys__/api/media-jobs", d.Auth.RequireAdmin(http.HandlerFunc(d.listHandler)))
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
	ProviderJobID string                      `json:"providerJobId"`
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
		Status:    strings.TrimSpace(r.URL.Query().Get("status")),
		Kind:      strings.TrimSpace(r.URL.Query().Get("kind")),
		APIKeyID:  strings.TrimSpace(r.URL.Query().Get("api_key_id")),
		AccountID: strings.TrimSpace(r.URL.Query().Get("account_id")),
		Limit:     mediaJobQueryInt(r, "limit", 20, 1, 100),
		Offset:    mediaJobQueryInt(r, "offset", 0, 0, 1<<30),
	}
	rows, total, err := d.Repo.ListManagement(r.Context(), query)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "查询媒体任务列表失败", err)
		return
	}
	payload := make([]mediaJobListRow, 0, len(rows))
	for _, record := range rows {
		payload = append(payload, mediaJobListRow{
			ID:            record.ID,
			Kind:          string(record.Kind),
			Status:        string(record.Status),
			ProviderCode:  record.ProviderCode,
			ProviderJobID: record.UpstreamJobID,
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
		})
	}
	kernel.WriteOK(w, map[string]any{"rows": payload, "total": total}, "")
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
