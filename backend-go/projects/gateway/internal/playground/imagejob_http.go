package playground

// 演练场图像工作台 HTTP 端点（设计 §3.4，v1.2 异步生图）：
//
//	POST /images/references  参考图上传（落暂存孤儿态，绑定随任务创建）
//	POST /images/jobs        创建生成任务（校验 → size 映射 → 落库 → 入队）
//	GET  /images/jobs        生成历史列表（keyword/accountId/status/quality/
//	                         limit/before 游标）
//	GET  /images/jobs/{id}   任务详情（轮询；不存在/越权 404）
//
// 硬口径：单次恒 1 张无 n；响应 camelCase；错误码 playground_* 词表；记录行
// 永不删，图像字节删除只属 jobs 清理任务（本层只做读时判定）。

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// referenceImageMaxBytes 是单张参考图解码后字节上限（§3.4.1：≤10MB；请求体
// 读取上限按 base64 膨胀系数留倍数余量）。
const (
	referenceImageMaxBytes     = 10 * 1024 * 1024
	referenceImageBodyMaxBytes = referenceImageMaxBytes * 2
	imageJobReferencesMax      = 10
	imageJobsListDefaultLimit  = 24
	imageJobsListMaxLimit      = 100
)

// 参考图与任务参数词表（§3.4.1/§3.4.2，可省略项以空串表达「默认」）。
var (
	imageJobAspectRatios  = map[string]bool{"1:1": true, "16:9": true, "9:16": true}
	imageJobResolutions   = map[string]bool{"1k": true, "2k": true, "4k": true}
	imageJobQualities     = map[string]bool{"standard": true, "fine": true}
	imageJobOutputFormats = map[string]bool{"png": true, "jpeg": true, "webp": true}
	imageJobStatuses      = map[string]bool{imageJobStatusQueued: true, imageJobStatusRunning: true, imageJobStatusCompleted: true, imageJobStatusFailed: true, imageJobStatusExpired: true, imageJobStatusActive: true}
)

// imageJobAspectSizes 是 aspectRatio → 上游 size 静态映射表（§3.4.2：按白名
// 单模型支持集精确映射——gpt-image 系支持 1024x1024/1024x1536/1536x1024；
// grok-imagine 系同表透传；2k/4k 无对应档取最近 1k 档即 1024 系）。
var imageJobAspectSizes = map[string]string{
	"1:1":  "1024x1024",
	"16:9": "1536x1024",
	"9:16": "1024x1536",
}

// imageJobSizeFor 把 aspectRatio × resolution 映射为实际发送的 size：
//   - aspectRatio 给定 → 按表取版式（分辨率档在两家族支持集内无差异，1k 即
//     最近档）；
//   - 仅 resolution 给定 → 取支持集内方版 1k 档；
//   - 两者皆省略 → 空串 = 不传 size，透传上游默认。
func imageJobSizeFor(aspectRatio, resolution string) string {
	if size, hit := imageJobAspectSizes[aspectRatio]; hit {
		return size
	}
	if resolution != "" {
		return "1024x1024"
	}
	return ""
}

// imageReferencesHandler 服务 POST /images/references（§3.4.1：纯 base64 参
// 考图上传 → 解码 + MIME 嗅探 → 落暂存 kind='image_reference' 孤儿态，响应
// {id,url}；孤儿 24h 未绑定任务由懒清理删除）。
func (rt *playgroundRoutes) imageReferencesHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	if !rt.artifactsReady() {
		writeHTTPError(w, artifactsUnavailable())
		return
	}
	// 写点挂懒清理（§3.8，M6 与「挂全部产物读写点」一致）：参考图上传是孤儿
	// 性行的主要写入点，顺手清理过期暂存。
	rt.artifacts.cleanupExpired()
	body, err := readJSONBody(r, referenceImageBodyMaxBytes)
	if err != nil {
		writeHTTPError(w, invalidRequest("请求体读取失败或超过上限"))
		return
	}
	var req struct {
		DataBase64 string `json:"dataBase64"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeHTTPError(w, invalidRequest("请求体必须是合法 JSON"))
		return
	}
	decoded, decodeErr := decodeReferenceImage(req.DataBase64)
	if decodeErr != nil {
		writeHTTPError(w, decodeErr)
		return
	}
	id, storeErr := rt.artifacts.saveImageReference(scope.ViewerID, *decoded)
	if storeErr != nil {
		writeHTTPError(w, artifactsError(storeErr))
		return
	}
	writeOKPayload(w, imageResultEntry{ID: id, URL: artifactContentURL(id)})
}

// decodeReferenceImage 解码纯 base64 参考图（无 data: 前缀，data: 前缀会因非
// base64 字符自然解码失败）并嗅探 MIME（png/jpeg/webp 三族）。
func decodeReferenceImage(raw string) (*decodedImage, *HTTPError) {
	clean := cleanBase64(strings.TrimSpace(raw))
	if clean == "" {
		return nil, invalidRequest("dataBase64 不能为空")
	}
	data, err := base64.StdEncoding.DecodeString(clean)
	if err != nil || len(data) == 0 {
		return nil, invalidRequest("参考图 base64 无法解码")
	}
	if len(data) > referenceImageMaxBytes {
		return nil, invalidRequest("参考图超过 10 MiB 上限")
	}
	mime := sniffImageMIME(data)
	if mime == "" {
		return nil, invalidRequest("参考图必须是 PNG/JPEG/WebP 图片")
	}
	return &decodedImage{Data: data, MIME: mime}, nil
}

// imageJobCreateRequest 是 POST /images/jobs 请求体（§3.4.2；参数四项可省略
// = 上游默认；referenceIds ≤10 张）。
type imageJobCreateRequest struct {
	APIKeyID     string   `json:"apiKeyId"`
	ModelID      string   `json:"modelId"`
	Prompt       string   `json:"prompt"`
	AspectRatio  string   `json:"aspectRatio"`
	Resolution   string   `json:"resolution"`
	Quality      string   `json:"quality"`
	OutputFormat string   `json:"outputFormat"`
	ReferenceIDs []string `json:"referenceIds"`
}

// imageJobsCreateHandler 服务 POST /images/jobs：候选校验 → 词表校验 → size
// 映射 → 落库（queued + 参考图事务绑定）→ 进程内队列 → 响应 {jobId,status}。
func (rt *playgroundRoutes) imageJobsCreateHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	if !rt.imageJobsReady() {
		writeHTTPError(w, imageJobsUnavailable())
		return
	}
	body, err := readJSONBody(r, 0)
	if err != nil {
		writeHTTPError(w, invalidRequest("请求体读取失败或超过上限"))
		return
	}
	var req imageJobCreateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeHTTPError(w, invalidRequest("请求体必须是合法 JSON"))
		return
	}
	rows, candidateErr := rt.resolveKeyModelRows(r.Context(), scope.ViewerID, strings.TrimSpace(req.APIKeyID))
	if candidateErr != nil {
		writeHTTPError(w, candidateErr)
		return
	}
	// 图像行携带 providerCode（任务 params 计价投影的来源）：行级唯一命中，
	// 跨供应商同名行取首条（分类入口已按 (modelId, kind, providerCode) 去重）。
	providerCode := ""
	imageRowFound := false
	for _, row := range rows {
		if row.Kind == KindImage && row.ModelID == strings.TrimSpace(req.ModelID) {
			imageRowFound = true
			if providerCode == "" {
				providerCode = row.ProviderCode
			}
		}
	}
	if !imageRowFound {
		writeHTTPError(w, modelNotAvailable("模型不在所选 Key 的演练场生图候选内"))
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeHTTPError(w, invalidRequest("prompt 不能为空"))
		return
	}
	if len(prompt) > imagesPromptMaxBytes {
		writeHTTPError(w, invalidRequest("prompt 超过字节上限"))
		return
	}
	if httpErr := validateImageJobParams(req.AspectRatio, req.Resolution, req.Quality, req.OutputFormat); httpErr != nil {
		writeHTTPError(w, httpErr)
		return
	}
	// referenceIds 上限按客户端提供的条目数收敛（剔空后计数，再保序去重——
	// 去重会折叠重复项，不能作为越界判定基准）。
	requested := make([]string, 0, len(req.ReferenceIDs))
	for _, id := range req.ReferenceIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			requested = append(requested, trimmed)
		}
	}
	if len(requested) > imageJobReferencesMax {
		writeHTTPError(w, invalidRequest("参考图最多 10 张"))
		return
	}
	references := dedupeNonEmptyStrings(requested)
	record, createErr := rt.imageJobs.Create(&ImageJobRecord{
		ViewerID:     scope.ViewerID,
		AccountID:    strings.TrimSpace(req.APIKeyID),
		APIKeyID:     strings.TrimSpace(req.APIKeyID),
		ProviderCode: providerCode,
		ModelID:      strings.TrimSpace(req.ModelID),
		Prompt:       prompt,
		AspectRatio:  strings.TrimSpace(req.AspectRatio),
		Resolution:   strings.TrimSpace(req.Resolution),
		Quality:      strings.TrimSpace(req.Quality),
		OutputFormat: strings.TrimSpace(req.OutputFormat),
		Size:         imageJobSizeFor(strings.TrimSpace(req.AspectRatio), strings.TrimSpace(req.Resolution)),
		ReferenceIDs: references,
	})
	if createErr != nil {
		if isErrImageJobReferenceNotFound(createErr) {
			writeHTTPError(w, &HTTPError{Status: http.StatusBadRequest, Code: codeImageJobReferenceNotFound, Message: "参考图不存在或不可用"})
			return
		}
		writeHTTPError(w, imageJobsError(createErr))
		return
	}
	if err := rt.imageRunner.Submit(record); err != nil {
		// 队列满的显式拒绝（§3.4.2：503 playground_busy，不无限积压）。拒绝前
		// 先把已落库的 queued 行收敛为 failed（B3：运行期无自愈，僵尸 queued 行
		// 会让历史页永久显示「排队中」）；收敛落库失败不改变响应口径——行留
		// queued 由启动恢复或用户重试重新入队。
		_ = rt.imageJobs.Fail(record.ViewerID, record.ID, "排队已满，请稍后重试")
		writeHTTPError(w, &HTTPError{Status: http.StatusServiceUnavailable, Code: codeImageJobBusy, Message: "生成任务排队已满，请稍后重试"})
		return
	}
	writeOKPayload(w, imageJobCreateResponse{JobID: record.ID, Status: imageJobStatusQueued})
}

// validateImageJobParams 校验参数词表（§3.4.2：词表外 → 400；空串 = 默认）。
func validateImageJobParams(aspectRatio, resolution, quality, outputFormat string) *HTTPError {
	if aspectRatio != "" && !imageJobAspectRatios[aspectRatio] {
		return invalidRequest("aspectRatio 词表外（1:1 | 16:9 | 9:16）")
	}
	if resolution != "" && !imageJobResolutions[resolution] {
		return invalidRequest("resolution 词表外（1k | 2k | 4k）")
	}
	if quality != "" && !imageJobQualities[quality] {
		return invalidRequest("quality 词表外（standard | fine）")
	}
	if outputFormat != "" && !imageJobOutputFormats[outputFormat] {
		return invalidRequest("outputFormat 词表外（png | jpeg | webp）")
	}
	return nil
}

// imageJobCreateResponse 是创建响应（§3.4.2）。
type imageJobCreateResponse struct {
	JobID  string `json:"jobId"`
	Status string `json:"status"`
}

// 错误哨兵判定辅助（errors.Is 统一收敛，避免调用方逐个比对）。
func isErrImageJobNotFound(err error) bool { return errors.Is(err, errImageJobNotFound) }
func isErrImageJobReferenceNotFound(err error) bool {
	return errors.Is(err, errImageJobReferenceNotFound)
}
func isErrImageJobStoreUnavailable(err error) bool {
	return errors.Is(err, errImageJobStoreUnavailable)
}
func isErrImageJobFilterInvalid(err error) bool { return errors.Is(err, errImageJobFilterInvalid) }
func isErrArtifactViewerInvalid(err error) bool { return errors.Is(err, errArtifactViewerInvalid) }

// imageJobsListHandler 服务 GET /images/jobs（§3.4.3 历史列表：query 解析 →
// store 过滤 → {jobs, nextCursor}）。
func (rt *playgroundRoutes) imageJobsListHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	if !rt.imageJobsReady() {
		writeHTTPError(w, imageJobsUnavailable())
		return
	}
	rt.artifacts.cleanupExpired()
	query := r.URL.Query()
	filter := ImageJobFilter{
		Keyword:   strings.TrimSpace(query.Get("keyword")),
		AccountID: strings.TrimSpace(query.Get("apiKeyId")),
		Status:    strings.TrimSpace(query.Get("status")),
		Quality:   strings.TrimSpace(query.Get("quality")),
	}
	if filter.Status != "" && !imageJobStatuses[filter.Status] {
		writeHTTPError(w, invalidRequest("status 词表外"))
		return
	}
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, parseErr := strconv.Atoi(raw)
		if parseErr != nil || limit <= 0 {
			// 非数字与 ≤0 一律 400（M5：不再静默回落默认值，文案与校验一致）。
			writeHTTPError(w, invalidRequest("limit 必须是正整数"))
			return
		}
		filter.Limit = limit
	}
	if raw := strings.TrimSpace(query.Get("before")); raw != "" {
		before, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil {
			writeHTTPError(w, invalidRequest("before 必须是毫秒时间戳"))
			return
		}
		filter.Before = before
	}
	page, listErr := rt.imageJobs.List(scope.ViewerID, filter)
	if listErr != nil {
		writeHTTPError(w, imageJobsError(listErr))
		return
	}
	writeOKPayload(w, page)
}

// imageJobGetHandler 服务 GET /images/jobs/{id}（§3.4.4 任务详情轮询；不存在
// 或越权统一 404 playground_job_not_found，不泄露他户任务存在性）。
func (rt *playgroundRoutes) imageJobGetHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	if !rt.imageJobsReady() {
		writeHTTPError(w, imageJobsUnavailable())
		return
	}
	jobID := strings.TrimSpace(r.PathValue("id"))
	if jobID == "" {
		writeHTTPError(w, invalidRequest("jobId 无效"))
		return
	}
	card, getErr := rt.imageJobs.Get(scope.ViewerID, jobID)
	if getErr != nil {
		writeHTTPError(w, imageJobsError(getErr))
		return
	}
	writeOKPayload(w, card)
}

// imageJobsReady 报告图像任务面是否接线（store + runner 双槽；缺一显式降级）。
func (rt *playgroundRoutes) imageJobsReady() bool {
	return rt.imageJobs != nil && rt.imageRunner != nil
}

// imageJobsUnavailable 是图像任务面未接线的显式降级。
func imageJobsUnavailable() *HTTPError {
	return &HTTPError{Status: http.StatusServiceUnavailable, Code: codeEndpointDegraded, Message: "图像任务暂不可用，请稍后重试"}
}

// imageJobsError 把任务存储错误映射 HTTP 错误面（身份非法 → 400，任务不存在
// → 404，过滤词表外 → 400，存储未接线 → 503，其余 → 500）。
func imageJobsError(err error) *HTTPError {
	switch {
	case isErrArtifactViewerInvalid(err):
		return invalidRequest("任务身份无效")
	case isErrImageJobNotFound(err):
		return &HTTPError{Status: http.StatusNotFound, Code: codeImageJobNotFound, Message: "任务不存在或不在当前用户范围内"}
	case isErrImageJobFilterInvalid(err):
		return invalidRequest("查询过滤词表外")
	case isErrImageJobStoreUnavailable(err):
		return imageJobsUnavailable()
	default:
		return &HTTPError{Status: http.StatusInternalServerError, Code: codeImageJobStoreFailed, Message: "生成任务读写失败，请重试"}
	}
}
