package studio

import (
	"errors"
	"image"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/image/webp"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// /my-studio 路由族（媒体工作室设计 §4）：资产上传/列表/删除/内容 + 账户
// 候选 + 域模型目录 + generations 受理（§4.3/§4.5，第二批）+ tasks 列表/快
// 照/取消（§4.4）。全部端点登录用户可用（viewScope 'self'，无角色限制，
// §7），服务端强制 system_account_id 归属过滤，越权与不存在同型 404；响应
// 为 {data, message} 包装，错误为 {message, code}（§4.0）。

// ObjectStore 是资产对象存储端口（chat_assets.storage_key 同款端口语义，
// §3.1 要点：复用既有存储端口，不新造存储栈）。组合根以 chat 资产同款本
// 地对象存储适配。
type ObjectStore interface {
	Write(storageKey string, data []byte, maxBytes int64, expectedSHA256 string) error
	Open(storageKey string, maxBytes int64) ([]byte, int64, error)
	Delete(storageKeys []string) error
}

// studioAssetMaxBytes 是单资产大小上限（本批任务约束：512 MiB；契约 §4.2
// P0 上传口径为 25MB——按任务指令放宽，差异已登记交付报告）。超限属 400
// studio_invalid_request（§4.2 错误表把"超限"归入上传参数错误；413 两码保
// 留给配额越界）。
const studioAssetMaxBytes = int64(512) << 20

// Deps 携带路由协作端口。
type Deps struct {
	Store *Store
	// RequireSession 与 chat 面同一套登录守卫（requireAuth +
	// forceSelfAccessScope）；nil 仅限测试直接注入鉴权上下文。
	RequireSession func(next http.Handler) http.Handler
	// ObjectStore 承载资产对象；nil 时上传/内容端点返回显式错误。
	ObjectStore ObjectStore
	// AccountOptions 是账户候选端口（internal/accounts 实现）。
	AccountOptions AccountOptionsLookup
	// Models 是域模型目录端口（组合根在 runtime cache 目录读链上实现）。
	Models ModelCatalog
	// Keys 是 purpose=studio 专用 Key 的幂等确保端口（§3.5；账户候选调用前
	// 异步确保，不阻塞响应路径）。
	Keys StudioKeyProvider
	// Executor 是进程内 /v1 派发链端口（generations 受理执行器，§4.5.1 进程
	// 内受理不经回环 HTTP；组合根以装配好的网关链适配）。nil 时生成端点显式
	// 503。
	Executor GatewayExecutor
	// Now 覆盖时钟（测试）；time.Now by default。
	Now func() time.Time

	// serials 是同用户创建串行化器（§4.5.7，generations 批次消费）。
	serials creationSerializer
	// flights 是图像后台执行的在途注册表（§4.4 a 态尽力中止）。
	flights imageFlights
}

// Register 挂载 my-studio 路由族；prefix 缺省 /__aisys__/api/my-studio。
func (d *Deps) Register(k *kernel.Kernel, prefix string) {
	if prefix == "" {
		prefix = "/__aisys__/api/my-studio"
	}
	rt := &studioRoutes{deps: d}
	mount := func(method, pattern string, handler http.HandlerFunc) {
		k.Register(method+" "+prefix+pattern, rt.wrap(handler))
	}
	mount("POST", "/assets", rt.uploadAsset)
	mount("GET", "/assets", rt.listAssets)
	mount("DELETE", "/assets/{assetId}", rt.deleteAsset)
	mount("GET", "/assets/{assetId}/content", rt.assetContent)
	mount("GET", "/accounts", rt.listAccounts)
	mount("GET", "/models", rt.listModels)
	// generations/tasks（媒体工作室设计 §4.3/§4.4，第二批）。
	mount("POST", "/generations/images", rt.generationImages)
	mount("POST", "/generations/videos", rt.generationVideos)
	mount("POST", "/generations/speech", rt.generationSpeech)
	mount("POST", "/generations/transcriptions", rt.generationTranscriptions)
	mount("GET", "/tasks", rt.listTasks)
	mount("GET", "/tasks/{taskId}", rt.getTask)
	mount("DELETE", "/tasks/{taskId}", rt.cancelTask)
}

type studioRoutes struct {
	deps *Deps
}

func (rt *studioRoutes) wrap(next http.HandlerFunc) http.Handler {
	handler := http.HandlerFunc(next)
	if rt.deps.RequireSession != nil {
		return rt.deps.RequireSession(handler)
	}
	return handler
}

func (rt *studioRoutes) now() string {
	if rt.deps.Now != nil {
		return rt.deps.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// requireStudioAuth 与 chat 的 requireChatAuth 同型（登录守卫后的兜底读取）。
func (rt *studioRoutes) requireStudioAuth(r *http.Request) (string, *authsys.AuthContext, error) {
	if auth := authsys.AuthContextFrom(r); auth != nil && auth.SystemAccountID != "" {
		return auth.SystemAccountID, auth, nil
	}
	return "", nil, errors.New("请先登录")
}

// --- 响应包装 ---

type okEnvelope struct {
	Data any `json:"data"`
}

func writeOKData(w http.ResponseWriter, data any) {
	kernel.WriteJSON(w, http.StatusOK, okEnvelope{Data: data})
}

// okMessageEnvelope 承载带 message 的成功包装（§4.0：幂等重放 200，
// message 注明"已返回既有任务/结果"）。
type okMessageEnvelope struct {
	Data    any    `json:"data"`
	Message string `json:"message,omitempty"`
}

func writeOKDataMessage(w http.ResponseWriter, data any, message string) {
	kernel.WriteJSON(w, http.StatusOK, okMessageEnvelope{Data: data, Message: message})
}

func writeCreated(w http.ResponseWriter, data any) {
	kernel.WriteJSON(w, http.StatusCreated, okEnvelope{Data: data})
}

type messageCodePayload struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

func writeMessageCode(w http.ResponseWriter, status int, message, code string) {
	kernel.WriteJSON(w, status, messageCodePayload{Message: message, Code: code})
}

// writeStudioRouteError 是统一错误映射：领域错误 → §4.6 稳定码；未知错误
// 保持 500 通用中文文案并记录底层原因（不吞错、不转写业务语义）。
func writeStudioRouteError(r *http.Request, w http.ResponseWriter, err error) {
	var route *RouteError
	if errors.As(err, &route) {
		writeMessageCode(w, route.Status, route.Message, route.Code)
		return
	}
	var quota *QuotaExceededError
	if errors.As(err, &quota) {
		quotaErr := AssetQuotaExceeded()
		writeMessageCode(w, quotaErr.Status, quotaErr.Message, quotaErr.Code)
		return
	}
	var count *CountExceededError
	if errors.As(err, &count) {
		countErr := AssetCountExceeded()
		writeMessageCode(w, countErr.Status, countErr.Message, countErr.Code)
		return
	}
	var referenced *ReferencedError
	if errors.As(err, &referenced) {
		refErr := AssetReferenced()
		writeMessageCode(w, refErr.Status, refErr.Message, refErr.Code)
		return
	}
	kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "媒体工作室服务暂不可用，请稍后重试", err)
}

// --- AssetDTO ---

// assetDTO 是各端点共用的资产投影（§4.2 AssetDTO）。
type assetDTO struct {
	ID           string         `json:"id"`
	Kind         string         `json:"kind"`
	Mime         string         `json:"mime"`
	Bytes        int64          `json:"bytes"`
	Width        *int64         `json:"width"`
	Height       *int64         `json:"height"`
	DurationMs   *int64         `json:"durationMs"`
	Sha256       string         `json:"sha256"`
	SourceTaskID *string        `json:"sourceTaskId"`
	Params       map[string]any `json:"params"`
	CreatedAt    string         `json:"createdAt"`
}

func assetDTOOf(asset *Asset) assetDTO {
	return assetDTO{
		ID:           asset.ID,
		Kind:         asset.Kind,
		Mime:         asset.Mime,
		Bytes:        asset.Bytes,
		Width:        asset.Width,
		Height:       asset.Height,
		DurationMs:   asset.DurationMs,
		Sha256:       asset.Sha256,
		SourceTaskID: asset.SourceTaskID,
		Params:       AssetParams(asset),
		CreatedAt:    asset.CreatedAt,
	}
}

// --- POST /assets（multipart 上传） ---

var studioUploadMimes = map[string]string{
	"image/png":   "image/png",
	"image/jpeg":  "image/jpeg",
	"image/jpg":   "image/jpeg",
	"image/pjpeg": "image/jpeg",
	"image/webp":  "image/webp",
}

type uploadedStudioFile struct {
	data             []byte
	declaredMimeType string
	sha256           string
	paramsJSON       string
}

// readMultipartUpload 解析 studio 上传表单：恰好一个 file 字段 + 至多一个
// paramsJson 字段（§4.2 表单契约；字段顺序不限，其余字段拒绝）。
func readMultipartUpload(r *http.Request) (*uploadedStudioFile, error) {
	contentType := r.Header.Get("Content-Type")
	mediaType, _, err := parseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" {
		return nil, InvalidRequest("素材上传必须使用 multipart/form-data")
	}
	reader, readerErr := r.MultipartReader()
	if readerErr != nil || reader == nil {
		return nil, InvalidRequest("multipart 请求格式无效")
	}
	var uploaded *uploadedStudioFile
	var paramsJSON *string
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			return nil, InvalidRequest("素材上传连接已中断")
		}
		formName := part.FormName()
		if formName != "file" && formName != "paramsJson" {
			_ = part.Close()
			return nil, InvalidRequest("素材上传不能包含额外表单字段")
		}
		if formName == "file" {
			if uploaded != nil {
				_ = part.Close()
				return nil, InvalidRequest("每次只能上传一个 file 素材字段")
			}
			declared := strings.ToLower(strings.TrimSpace(part.Header.Get("Content-Type")))
			if idx := strings.Index(declared, ";"); idx >= 0 {
				declared = strings.TrimSpace(declared[:idx])
			}
			mime, ok := studioUploadMimes[declared]
			if !ok {
				_ = part.Close()
				return nil, InvalidRequest("当前仅支持上传 PNG、JPEG 或 WebP 图片素材")
			}
			data, readErr := readAllLimited(part, studioAssetMaxBytes+1)
			_ = part.Close()
			if readErr != nil {
				return nil, InvalidRequest("素材上传连接已中断")
			}
			if int64(len(data)) > studioAssetMaxBytes {
				return nil, InvalidRequest("单个素材不能超过 512MiB")
			}
			if len(data) == 0 {
				return nil, InvalidRequest("上传素材不能为空")
			}
			digest := sha256Hex(data)
			uploaded = &uploadedStudioFile{data: data, declaredMimeType: mime, sha256: digest}
			continue
		}
		// paramsJson
		if paramsJSON != nil {
			_ = part.Close()
			return nil, InvalidRequest("paramsJson 字段只能出现一次")
		}
		raw, readErr := readAllLimited(part, 64*1024+1)
		_ = part.Close()
		if readErr != nil {
			return nil, InvalidRequest("素材上传连接已中断")
		}
		if int64(len(raw)) > 64*1024 {
			return nil, InvalidRequest("paramsJson 不能超过 64KiB")
		}
		text := strings.TrimSpace(string(raw))
		if text == "" {
			continue
		}
		paramsJSON = &text
	}
	if uploaded == nil {
		return nil, InvalidRequest("缺少 file 素材字段")
	}
	uploaded.paramsJSON = "{}"
	if paramsJSON != nil {
		if !isJSONObject(*paramsJSON) {
			return nil, InvalidRequest("paramsJson 必须是 JSON 对象")
		}
		uploaded.paramsJSON = *paramsJSON
	}
	return uploaded, nil
}

// studioStorageKey 承载对象存储键（chat_assets.storage_key 同款端口语义：
// 摘要分桶 + 资产段 + 摘要短码 + 扩展名；stasset 段保证与 chat 键空间不交）。
func studioStorageKey(assetID, sha256Value, mimeType string) string {
	digest := strings.ToLower(strings.TrimSpace(sha256Value))
	segment := assetID
	extension := assetExtension(mimeType)
	short := digest
	if len(short) > 16 {
		short = short[:16]
	}
	return digest[:2] + "/" + digest[2:4] + "/" + segment + "-" + short + extension
}

// imageDimensions 解析图片宽高（§4.2 ready 提交解析宽高）；来源缺失保持
// NULL（解码失败不阻断上传，元数据留空）。
func imageDimensions(mimeType string, data []byte) (*int64, *int64) {
	var config image.Config
	var err error
	switch mimeType {
	case "image/png":
		config, _, err = image.DecodeConfig(strings.NewReader(string(data)))
	case "image/jpeg":
		config, _, err = image.DecodeConfig(strings.NewReader(string(data)))
	case "image/webp":
		config, err = webp.DecodeConfig(strings.NewReader(string(data)))
	default:
		return nil, nil
	}
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return nil, nil
	}
	width, height := int64(config.Width), int64(config.Height)
	return &width, &height
}

// uploadAsset 服务 POST /my-studio/assets（§4.2）：配额预检 → 幂等查重 →
// 对象写入 → 单事务落行 + 账本增量；任一步失败清理已写对象，不残留占额。
func (rt *studioRoutes) uploadAsset(w http.ResponseWriter, r *http.Request) {
	ownerID, _, authErr := rt.requireStudioAuth(r)
	if authErr != nil {
		kernel.WriteError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	uploaded, err := readMultipartUpload(r)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if rt.deps.ObjectStore == nil || rt.deps.Store == nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, "素材存储未装配")
		return
	}
	// 同用户 sha256 查重（幂等上传，任务约束 §5.4 digest 同用户查重语义）：
	// 命中即返回既有资产，不重复写对象、不重复占额。
	if existing, findErr := rt.deps.Store.FindAssetBySha256(ownerID, uploaded.sha256); findErr != nil {
		writeStudioRouteError(r, w, findErr)
		return
	} else if existing != nil {
		writeOKData(w, assetDTOOf(existing))
		return
	}
	if err := rt.deps.Store.AssertQuotaAvailable(ownerID, int64(len(uploaded.data))); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	nowValue := rt.now()
	assetID := NewAssetID()
	storageKey := studioStorageKey(assetID, uploaded.sha256, uploaded.declaredMimeType)
	if err := rt.deps.ObjectStore.Write(storageKey, uploaded.data, studioAssetMaxBytes, uploaded.sha256); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	width, height := imageDimensions(uploaded.declaredMimeType, uploaded.data)
	asset, err := rt.deps.Store.CreateAsset(CreateAssetInput{
		ID:              assetID,
		SystemAccountID: ownerID,
		Kind:            "image",
		Mime:            uploaded.declaredMimeType,
		StorageKey:      storageKey,
		Bytes:           int64(len(uploaded.data)),
		Width:           width,
		Height:          height,
		Sha256:          uploaded.sha256,
		ParamsJSON:      uploaded.paramsJSON,
		Now:             nowValue,
	})
	if err != nil {
		// 行未落（或事务回滚）：已写对象必须回收，避免孤儿对象冒占存储。
		_ = rt.deps.ObjectStore.Delete([]string{storageKey})
		writeStudioRouteError(r, w, err)
		return
	}
	writeCreated(w, assetDTOOf(asset))
}

// --- GET /assets ---

// listAssetQueryBounds 对齐 mediaJobs 先例（§4.0）：limit 默认 20、上限 100、
// 非法回落默认；offset 负数/非法回落 0。
func listAssetQueryBounds(r *http.Request) (int, int, error) {
	query := r.URL.Query()
	for key := range query {
		if key != "limit" && key != "offset" && key != "kind" {
			return 0, 0, InvalidRequest("Unrecognized key: %q", key)
		}
	}
	limit := boundedQueryInt(query.Get("limit"), 20, 100)
	offset := boundedQueryOffset(query.Get("offset"))
	kind := strings.TrimSpace(query.Get("kind"))
	if kind != "" && !WorkbenchDomainValues[kind] {
		return 0, 0, InvalidRequest("kind 必须是 image、audio 或 video")
	}
	return limit, offset, nil
}

func boundedQueryInt(raw string, fallback, maxValue int) int {
	text := strings.TrimSpace(raw)
	if text == "" {
		return fallback
	}
	value, ok := parseInt(text)
	if !ok || value <= 0 {
		return fallback
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func boundedQueryOffset(raw string) int {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0
	}
	value, ok := parseInt(text)
	if !ok || value < 0 {
		return 0
	}
	return value
}

func (rt *studioRoutes) listAssets(w http.ResponseWriter, r *http.Request) {
	ownerID, _, authErr := rt.requireStudioAuth(r)
	if authErr != nil {
		kernel.WriteError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	limit, offset, err := listAssetQueryBounds(r)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	assets, total, err := rt.deps.Store.ListAssets(ownerID, kind, limit, offset)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	rows := make([]assetDTO, 0, len(assets))
	for _, asset := range assets {
		rows = append(rows, assetDTOOf(asset))
	}
	writeOKData(w, map[string]any{"rows": rows, "total": total})
}

// --- DELETE /assets/{assetId} ---

func (rt *studioRoutes) deleteAsset(w http.ResponseWriter, r *http.Request) {
	ownerID, _, authErr := rt.requireStudioAuth(r)
	if authErr != nil {
		kernel.WriteError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	asset, err := rt.deps.Store.DeleteAsset(r.PathValue("assetId"), ownerID, rt.now())
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if asset == nil {
		writeStudioRouteError(r, w, AssetNotFound())
		return
	}
	// 行 + 账本已事务性收口；对象删除尽力执行（失败只留无行引用的孤儿对
	// 象，不占配额、对用户不可见）。
	if rt.deps.ObjectStore != nil {
		_ = rt.deps.ObjectStore.Delete([]string{asset.StorageKey})
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- GET /assets/{assetId}/content ---

func (rt *studioRoutes) assetContent(w http.ResponseWriter, r *http.Request) {
	ownerID, _, authErr := rt.requireStudioAuth(r)
	if authErr != nil {
		kernel.WriteError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	variant := "original"
	if raw := strings.TrimSpace(r.URL.Query().Get("variant")); raw != "" {
		switch raw {
		case "original":
			variant = "original"
		case "preview":
			// P0 无 preview 生成管线（§4.2：不做转码；preview 仅图像域按既有
			// 处理管线考虑，本批未交付）——一律 400 studio_asset_preview_unavailable。
			writeStudioRouteError(r, w, AssetPreviewUnavailable())
			return
		default:
			writeStudioRouteError(r, w, InvalidRequest("variant 必须是 original 或 preview"))
			return
		}
	}
	download := ""
	if raw := strings.TrimSpace(r.URL.Query().Get("download")); raw != "" {
		if raw != "0" && raw != "1" {
			writeStudioRouteError(r, w, InvalidRequest("download 必须是 0 或 1"))
			return
		}
		download = raw
	}
	asset, err := rt.deps.Store.GetAsset(r.PathValue("assetId"), ownerID)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	// 非 ready 或归属不符一律 404 同型（§4.2：不泄露存在性）。
	if asset == nil {
		writeStudioRouteError(r, w, AssetNotFound())
		return
	}
	etag := "\"" + asset.Sha256 + "\""
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if requestEtagMatches(r.Header.Values("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if rt.deps.ObjectStore == nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, "素材存储未装配")
		return
	}
	data, objectBytes, err := rt.deps.ObjectStore.Open(asset.StorageKey, studioAssetMaxBytes)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "素材内容读取失败", err)
		return
	}
	if download == "1" {
		w.Header().Set("Content-Disposition", "attachment; filename=\"studio-"+asset.ID+assetExtension(asset.Mime)+"\"")
	} else {
		w.Header().Set("Content-Disposition", "inline")
	}
	w.Header().Set("Content-Type", asset.Mime)
	w.Header().Set("Content-Length", itoa(int(objectBytes)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	_ = variant
}

func assetExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav":
		return ".wav"
	case "audio/ogg":
		return ".ogg"
	case "audio/mp4":
		return ".m4a"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	}
	return ".bin"
}

func requestEtagMatches(values []string, etag string) bool {
	header := strings.Join(values, ",")
	if header == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		normalized := strings.TrimSpace(candidate)
		if normalized == "*" || normalized == etag || normalized == "W/"+etag {
			return true
		}
	}
	return false
}

// --- GET /accounts ---

// listAccounts 服务 GET /my-studio/accounts（§4.1）：两档范围口径（admin/
// super_admin 全量号池，普通用户自有 + 被授权），全状态可见；domain 可选过
// 滤。调用前异步确保 purpose=studio Key（§3.5 触发时机，不阻塞响应路径）。
func (rt *studioRoutes) listAccounts(w http.ResponseWriter, r *http.Request) {
	ownerID, auth, authErr := rt.requireStudioAuth(r)
	if authErr != nil {
		kernel.WriteError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	if domain != "" && !AccountDomainValues[domain] {
		writeStudioRouteError(r, w, DomainInvalid())
		return
	}
	if rt.deps.Keys != nil {
		// 异步确保（§3.5）：失败不阻断账户候选读取，下次调用自愈重试。
		go func(keyOwner string) {
			_, _ = rt.deps.Keys.EnsureStudioKey(keyOwner)
		}(ownerID)
	}
	if rt.deps.AccountOptions == nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, "账户列表暂不可用，请稍后重试")
		return
	}
	options, err := rt.deps.AccountOptions.ListStudioAccountOptions(r.Context(), ownerID, authsys.IsAdminRole(auth.Role), domain)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "查询账户列表失败", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeOKData(w, map[string]any{"rows": options, "total": len(options)})
}

// --- GET /models ---

func (rt *studioRoutes) listModels(w http.ResponseWriter, r *http.Request) {
	if _, _, authErr := rt.requireStudioAuth(r); authErr != nil {
		kernel.WriteError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	// domain 必填且仅收三个工作台域（§4.1：text 域 P0 无工作台目录端点，
	// 缺省/非法同 400 studio_domain_invalid）。
	if !WorkbenchDomainValues[domain] {
		writeStudioRouteError(r, w, DomainInvalid())
		return
	}
	if rt.deps.Models == nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, "模型目录暂不可用，请稍后重试")
		return
	}
	auth := authsys.AuthContextFrom(r)
	models, err := rt.deps.Models.ListDomainModels(r.Context(), authOwnerOf(r), authsys.IsAdminRole(authRoleOf(auth)), domain)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "查询模型目录失败", err)
		return
	}
	writeOKData(w, map[string]any{"rows": models, "total": len(models)})
}

func authRoleOf(auth *authsys.AuthContext) string {
	if auth == nil {
		return ""
	}
	return auth.Role
}

func authOwnerOf(r *http.Request) string {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID
	}
	return ""
}
