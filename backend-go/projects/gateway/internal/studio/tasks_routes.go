package studio

// tasks 路由族（媒体工作室设计 §4.4）：任务列表（limit/offset/status/kind/
// operation_id，分页与排序同 §4.0）、任务快照（轮询端点）与取消（四态分层语
// 义，决策 #9⑨：queued 未上游受理本地取消不调上游；已上游受理尽力上游取消不
// 阻塞本地收口；completed 未结算 409；终态幂等返回当前态）。全部端点登录用
// 户可用，归属强制过滤，越权与不存在同型 404。

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// taskErrorDTO 是 TaskDTO.error（终态/结算错误，脱敏，§4.4）。
type taskErrorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// taskDTO 是任务投影（§4.4 TaskDTO）。
type taskDTO struct {
	ID                string        `json:"id"`
	Kind              string        `json:"kind"`
	Status            string        `json:"status"`
	Settlement        string        `json:"settlement"`
	Model             string        `json:"model"`
	AccountID         *string       `json:"accountId"`
	ProviderCode      *string       `json:"providerCode"`
	PromptSummary     string        `json:"promptSummary"`
	AssetID           *string       `json:"assetId"`
	Error             *taskErrorDTO `json:"error"`
	ClientOperationID string        `json:"clientOperationId"`
	CreatedAt         string        `json:"createdAt"`
	UpdatedAt         string        `json:"updatedAt"`
}

// taskSnapshot 是 request_snapshot_json 的读取投影（model + promptSummary 来
// 源；轻量参数快照不含资源字节，§3.2）。
type taskSnapshot struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// taskPromptSummary 截断 120 字符（对齐 media-jobs 先例，§4.4）。
func taskPromptSummary(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > 120 {
		return string(runes[:120])
	}
	return string(runes)
}

// taskErrorOf 解析 error_json（空/坏值按无错误处理，列 DEFAULT '{}'）。
func taskErrorOf(raw string) *taskErrorDTO {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" {
		return nil
	}
	var parsed taskErrorDTO
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil
	}
	if parsed.Code == "" && parsed.Message == "" {
		return nil
	}
	return &parsed
}

// taskDTOOf 投影任务行；existingAssets 为同页 settled 任务 asset_id 的存在性
// 集（§4.4：assetId 仅 settlement='settled' 且资产存在时返回，悬空引用为
// null——前端按产物缺失渲染，§6）。nil 集按"无存在性数据"处理，仅当任务
// settled 且带 asset_id 时需要调用方提供。
func taskDTOOf(task *Task, existingAssets map[string]bool) taskDTO {
	var snapshot taskSnapshot
	_ = json.Unmarshal([]byte(task.RequestSnapshotJSON), &snapshot)
	dto := taskDTO{
		ID:                task.ID,
		Kind:              task.Kind,
		Status:            task.Status,
		Settlement:        task.Settlement,
		Model:             snapshot.Model,
		AccountID:         task.AccountID,
		ProviderCode:      task.ProviderCode,
		PromptSummary:     taskPromptSummary(snapshot.Prompt),
		Error:             taskErrorOf(task.ErrorJSON),
		ClientOperationID: task.ClientOperationID,
		CreatedAt:         task.CreatedAt,
		UpdatedAt:         task.UpdatedAt,
	}
	if task.Settlement == "settled" && task.AssetID != nil {
		if existingAssets == nil || existingAssets[*task.AssetID] {
			dto.AssetID = task.AssetID
		}
	}
	return dto
}

// taskDTOFor 单任务投影（详情/取消/受理响应）：settled 且带 asset_id 时查存
// 在性。
func (rt *studioRoutes) taskDTOFor(ownerID string, task *Task) (taskDTO, error) {
	existing := map[string]bool{}
	if task.Settlement == "settled" && task.AssetID != nil {
		ids, err := rt.deps.Store.FilterExistingAssetIDs(ownerID, []string{*task.AssetID})
		if err != nil {
			return taskDTO{}, err
		}
		existing = ids
	}
	return taskDTOOf(task, existing), nil
}

// --- GET /tasks ---

// taskListQueryBounds 解析列表查询（§4.4：status 六值单值非法 400；kind
// image|video；operation_id 精确匹配；未知查询键 400 对齐资产列表口径）。
func taskListQueryBounds(r *http.Request) (TaskListQuery, error) {
	query := r.URL.Query()
	for key := range query {
		if key != "limit" && key != "offset" && key != "status" && key != "kind" && key != "operation_id" {
			return TaskListQuery{}, InvalidRequest("Unrecognized key: %q", key)
		}
	}
	parsed := TaskListQuery{
		Status:      strings.TrimSpace(query.Get("status")),
		Kind:        strings.TrimSpace(query.Get("kind")),
		OperationID: strings.TrimSpace(query.Get("operation_id")),
		Limit:       boundedQueryInt(query.Get("limit"), 20, 100),
		Offset:      boundedQueryOffset(query.Get("offset")),
	}
	if parsed.Status != "" && !StudioTaskStatuses[parsed.Status] {
		return TaskListQuery{}, InvalidRequest("status 必须是 queued、in_progress、completed、failed、cancelled 或 expired")
	}
	if parsed.Kind != "" && !StudioTaskKinds[parsed.Kind] {
		return TaskListQuery{}, InvalidRequest("kind 必须是 image 或 video")
	}
	return parsed, nil
}

func (rt *studioRoutes) listTasks(w http.ResponseWriter, r *http.Request) {
	ownerID, _, authErr := rt.requireStudioAuth(r)
	if authErr != nil {
		kernel.WriteError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	parsed, err := taskListQueryBounds(r)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	tasks, total, err := rt.deps.Store.ListTasks(ownerID, parsed)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	existing, err := rt.settledAssetExistence(ownerID, tasks)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	rows := make([]taskDTO, 0, len(tasks))
	for _, task := range tasks {
		rows = append(rows, taskDTOOf(task, existing))
	}
	writeOKData(w, map[string]any{"rows": rows, "total": total})
}

// settledAssetExistence 批量确认同页 settled 任务的资产存在性。
func (rt *studioRoutes) settledAssetExistence(ownerID string, tasks []*Task) (map[string]bool, error) {
	ids := []string{}
	for _, task := range tasks {
		if task.Settlement == "settled" && task.AssetID != nil {
			ids = append(ids, *task.AssetID)
		}
	}
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}
	return rt.deps.Store.FilterExistingAssetIDs(ownerID, ids)
}

// --- GET /tasks/{taskId} ---

func (rt *studioRoutes) getTask(w http.ResponseWriter, r *http.Request) {
	ownerID, _, authErr := rt.requireStudioAuth(r)
	if authErr != nil {
		kernel.WriteError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	task, err := rt.deps.Store.GetTask(r.PathValue("taskId"), ownerID)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if task == nil {
		writeStudioRouteError(r, w, TaskNotFound())
		return
	}
	dto, err := rt.taskDTOFor(ownerID, task)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	writeOKData(w, dto)
}

// --- DELETE /tasks/{taskId}（取消四态） ---

// isStudioTerminalTask 报告任务处于终态（failed/cancelled/expired，或
// completed+settled——§4.4 终态幂等无操作行）。
func isStudioTerminalTask(task *Task) bool {
	switch task.Status {
	case "failed", "cancelled", "expired":
		return true
	case "completed":
		return task.Settlement == "settled"
	}
	return false
}

func (rt *studioRoutes) cancelTask(w http.ResponseWriter, r *http.Request) {
	ownerID, _, authErr := rt.requireStudioAuth(r)
	if authErr != nil {
		kernel.WriteError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	task, err := rt.deps.Store.GetTask(r.PathValue("taskId"), ownerID)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if task == nil {
		writeStudioRouteError(r, w, TaskNotFound())
		return
	}
	// c 态（第二批任务指令裁决）：completed 未结算 → 409，仅停止展示由前端处
	// 理，本地不删（后台结算照常完成）。
	if task.Status == "completed" && task.Settlement != "settled" {
		writeStudioRouteError(r, w, TaskAlreadyCompleted())
		return
	}
	// 终态（failed/cancelled/expired 或 completed+settled）：幂等无操作。
	if isStudioTerminalTask(task) {
		dto, dtoErr := rt.taskDTOFor(ownerID, task)
		if dtoErr != nil {
			writeStudioRouteError(r, w, dtoErr)
			return
		}
		writeOKData(w, dto)
		return
	}
	upstreamJobID := ""
	if task.UpstreamJobID != nil {
		upstreamJobID = strings.TrimSpace(*task.UpstreamJobID)
	}
	now := rt.now()
	if upstreamJobID == "" {
		// a 态：未上游受理——本地置 cancelled，不调上游；已发出未返回的 /v1
		// 同步调用尽力 context 中止（§4.4 a）。
		if _, err := rt.deps.Store.CancelTask(task.ID, ownerID, now); err != nil {
			writeStudioRouteError(r, w, err)
			return
		}
		rt.deps.flights.abort(task.ID)
	} else {
		// b 态：已上游受理——经任务面账户亲和尽力上游取消，不阻塞本地收口。
		rt.cancelUpstreamVideoBestEffort(task)
		if _, err := rt.deps.Store.CancelTask(task.ID, ownerID, now); err != nil {
			writeStudioRouteError(r, w, err)
			return
		}
	}
	fresh, err := rt.deps.Store.GetTask(task.ID, ownerID)
	if err != nil || fresh == nil {
		if err == nil {
			err = errors.New("任务取消后回读失败")
		}
		writeStudioRouteError(r, w, err)
		return
	}
	dto, dtoErr := rt.taskDTOFor(ownerID, fresh)
	if dtoErr != nil {
		writeStudioRouteError(r, w, dtoErr)
		return
	}
	writeOKData(w, dto)
}

// cancelUpstreamVideoBestEffort 尽力经 /v1 任务面取消上游视频任务（DELETE
// /v1/videos/{id}，账户亲和直连语义；purpose 头 studio_video 同审计口径）。
// 上游拒绝/失败不阻塞本地收口（§4.4 b 态），错误只记日志不外泄。
func (rt *studioRoutes) cancelUpstreamVideoBestEffort(task *Task) {
	if rt.deps.Executor == nil || rt.deps.Keys == nil || task.UpstreamJobID == nil {
		return
	}
	keyID, err := rt.deps.Keys.EnsureStudioKey(task.SystemAccountID)
	if err != nil {
		slog.Warn("studio 取消面 Key 确保失败", "event", "studio_task_cancel_key_failed", "taskId", task.ID, "error", err.Error())
		return
	}
	secret, err := rt.deps.Keys.FindStudioKeySecret(keyID, task.SystemAccountID)
	if err != nil {
		slog.Warn("studio 取消面 Key 明文读取失败", "event", "studio_task_cancel_key_failed", "taskId", task.ID, "error", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), studioUpstreamCancelTimeout)
	defer cancel()
	_, _ = rt.deps.Executor.Dispatch(ctx, GatewayDispatchRequest{
		Method: http.MethodDelete,
		Path:   "/v1/videos/" + strings.TrimSpace(*task.UpstreamJobID),
		Headers: map[string]string{
			"authorization":     "Bearer " + secret,
			StudioPurposeHeader: PurposeStudioVideo,
		},
	})
}
