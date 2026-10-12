package studio

// generations 受理执行器（媒体工作室设计 §4.3/§4.5）：四个生成端点经进程内
// /v1 派发链受理——图像 /v1/images/generations（同步形态、后台任务行承载、
// 受理即结算）、视频 /v1/videos（创建即返回、受理边界确立后由 jobs 轮询收
// 口）、TTS /v1/audio/speech 与 STT /v1/audio/transcriptions（同步、幂等走
// studio_operations 收据）。审计头 x-juhe-ai-purpose: studio_*（§4.5.2，审计
// 来源标注）；账户收敛经 DispatchAccount 由组合根执行器注入进程内调度覆盖
// （chat 绑定账户同款机制，§4.5.3）；确定性 4xx 短路与受理边界由 /v1 既有链
// 承担，studio 侧不重复实现（§4.5.4/§4.5.5）。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// x-juhe-ai-purpose 审计头词表（§4.5.2；先例 chat_context_compaction /
// chat_image_memory）。该头是审计来源标注，不作为授权依据。
const (
	StudioPurposeHeader = "x-juhe-ai-purpose"
	PurposeStudioImage  = "studio_image"
	PurposeStudioVideo  = "studio_video"
	PurposeStudioAudio  = "studio_audio"
	PurposeStudioText   = "studio_text"
)

// 同步生成收据 kind 词表（studio_operations.kind，§3.3）。
const (
	OperationKindAudioSpeech        = "audio_speech"
	OperationKindAudioTranscription = "audio_transcription"
)

// 产物与报文限额：图像沿用 chat 生成产物 16MiB 限额先例；音频 32MiB（问答
// 音视频工具同款）；STT 上传文件 25MB（§4.3 契约）；任务/收据 JSON 报文
// 1MiB（上游小 JSON 载荷，chain_media_video maxMediaJobObjectBytes 同量级）。
const (
	studioImageMaxBytes         = int64(16) << 20
	studioAudioMaxBytes         = int64(32) << 20
	studioTranscriptionMaxBytes = int64(25) << 20
	studioGenerationBodyMax     = int64(1) << 20
	studioUpstreamJSONMax       = int64(1) << 20
	// studioErrorSummaryMax 是脱敏错误摘要的长度上限（error_json/响应文案）。
	studioErrorSummaryMax = 300
	// studioUpstreamCancelTimeout 是取消面尽力上游 DELETE 的有界等待（§4.4 b
	// 态：尽力上游取消不阻塞本地收口）。
	studioUpstreamCancelTimeout = 5 * time.Second
)

// GatewayDispatchRequest 是进程内 /v1 请求（chat GenerationDispatchRequest 同
// 构抽象，studio 包自持不 import chat 包）。DispatchAccount 是账户收敛目标：
// 组合根执行器据此注入进程内调度覆盖 context，外部 HTTP 请求无法触达该通道。
type GatewayDispatchRequest struct {
	Method          string
	Path            string
	Headers         map[string]string
	Body            []byte
	DispatchAccount string
}

// GatewayDispatchResponse 是缓冲读的 /v1 响应（studio 消费的端点都是有限体：
// 图像/音频字节或小 JSON）。
type GatewayDispatchResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// GatewayExecutor 是进程内 /v1 派发链端口（§4.5.1：不经回环 HTTP，组合根以
// 装配好的网关链 http.Handler 适配）。
type GatewayExecutor interface {
	Dispatch(ctx context.Context, req GatewayDispatchRequest) (*GatewayDispatchResponse, error)
}

// creationSerializer 是同用户创建串行化器（§4.5.7：同一
// (system_account_id, kind) 的创建调用进程内串行执行，排队而非拒绝，作为
// §5.5 恢复关联确定性的前提）。锁条目按 (owner, kind) 常驻，规模以用户数 ×
// 端点数为界。
type creationSerializer struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (s *creationSerializer) lock(key string) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	m, ok := s.locks[key]
	if !ok {
		m = &sync.Mutex{}
		s.locks[key] = m
	}
	m.Lock()
	return m.Unlock
}

// imageFlights 是图像后台执行的在途注册表（§4.4 a 态：已发出未返回的 /v1 同
// 步调用尽力 context 中止）。
type imageFlights struct {
	mu      sync.Mutex
	flights map[string]context.CancelFunc
}

func (f *imageFlights) register(taskID string, cancel context.CancelFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.flights == nil {
		f.flights = map[string]context.CancelFunc{}
	}
	f.flights[taskID] = cancel
}

func (f *imageFlights) release(taskID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.flights, taskID)
}

// abort 中止在途调用；返回是否命中（取消面尽力语义，不阻塞收口）。
func (f *imageFlights) abort(taskID string) bool {
	f.mu.Lock()
	cancel, ok := f.flights[taskID]
	f.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// --- 公共校验工具 ---

// isAdminRole 是 authsys.IsAdminRole 的包内别名（两档范围口径，§4.1）。
func isAdminRole(role string) bool {
	return authsys.IsAdminRole(role)
}

// studioDecodeJSONObject 读取并解析 JSON 对象请求体（上限内）。
func studioDecodeJSONObject(r *http.Request, limit int64) (map[string]any, error) {
	data, err := readAllLimited(r.Body, limit+1)
	if err != nil {
		return nil, InvalidRequest("生成请求体读取失败")
	}
	if int64(len(data)) > limit {
		return nil, InvalidRequest("生成请求体超过上限")
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, InvalidRequest("生成请求体必须是非空 JSON 对象")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil || parsed == nil {
		return nil, InvalidRequest("生成请求体必须是 JSON 对象")
	}
	return parsed, nil
}

// studioRejectUnknownKeys 拒绝请求 DTO 之外的键（§4.3 与 /v1 L2"不猜测、不
// 降级"一致）。
func studioRejectUnknownKeys(body map[string]any, allowed ...string) error {
	for key := range body {
		known := false
		for _, name := range allowed {
			if key == name {
				known = true
				break
			}
		}
		if !known {
			return InvalidRequest("不支持请求字段 %q", key)
		}
	}
	return nil
}

// studioStringField 读取字符串字段；required 为真时缺失/空值报 400。
func studioStringField(body map[string]any, name string, required bool) (string, error) {
	raw, ok := body[name]
	if !ok || raw == nil {
		if required {
			return "", InvalidRequest("%s 必填", name)
		}
		return "", nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", InvalidRequest("%s 必须是字符串", name)
	}
	text = strings.TrimSpace(text)
	if required && text == "" {
		return "", InvalidRequest("%s 必填", name)
	}
	return text, nil
}

// studioOperationIDOf 校验 clientOperationId（§4.0：必填、1..128 字符、用户
// 范围唯一）。
func studioOperationIDOf(body map[string]any) (string, error) {
	raw, ok := body["clientOperationId"]
	if !ok {
		return "", OperationIDInvalid()
	}
	text, ok := raw.(string)
	if !ok {
		return "", OperationIDInvalid()
	}
	trimmed := strings.TrimSpace(text)
	length := utf8.RuneCountInString(trimmed)
	if length < 1 || length > 128 {
		return "", OperationIDInvalid()
	}
	return trimmed, nil
}

// studioProviderOptionsOf 校验 providerOptions（L3 透传通道，§4.3：工作室不
// 解析其内容，仅要求对象形态）。
func studioProviderOptionsOf(body map[string]any) (map[string]any, bool, error) {
	raw, ok := body["providerOptions"]
	if !ok || raw == nil {
		return nil, false, nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, false, InvalidRequest("providerOptions 必须是 JSON 对象")
	}
	return object, true, nil
}

// capabilityList 读取能力载荷数组键。
func capabilityList(caps map[string]any, key string) []any {
	if caps == nil {
		return nil
	}
	raw, ok := caps[key].([]any)
	if !ok {
		return nil
	}
	return raw
}

// capabilityFlag 读取能力载荷布尔键。
func capabilityFlag(caps map[string]any, key string) bool {
	if caps == nil {
		return false
	}
	value, _ := caps[key].(bool)
	return value
}

// capabilityAllowsList 报告取值是否落在能力载荷声明列表内（松散相等：数值按
// float64、字符串按原文）。
func capabilityAllowsList(caps map[string]any, key string, value any) bool {
	for _, item := range capabilityList(caps, key) {
		if studioValueEqual(item, value) {
			return true
		}
	}
	return false
}

func studioValueEqual(a, b any) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	}
	return false
}

// upstreamErrorMessage 提取上游错误摘要（脱敏：只透出上游 message 字段，chat
// upstreamMessagePayload 同款）。
func upstreamErrorMessage(payload, fallback string) string {
	var parsed struct {
		Message string `json:"message"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &parsed); err == nil {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return truncateSummary(parsed.Error.Message)
		}
		if parsed.Message != "" {
			return truncateSummary(parsed.Message)
		}
	}
	return truncateSummary(fallback)
}

func truncateSummary(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > studioErrorSummaryMax {
		return string(runes[:studioErrorSummaryMax])
	}
	return string(runes)
}

// --- 域授权 / 账户范围 / 模型目录 ---

// requireDomainAuthorized 校验 studio Key 域授权（studio_domains_json，§3.5.3
// 授权数据；§4.5 执行链域校验）。Keys 未装配按 P0 默认四域放行（测试兜底）。
func (rt *studioRoutes) requireDomainAuthorized(ownerID, domain string) error {
	if rt.deps.Keys == nil {
		return nil
	}
	domains, err := rt.deps.Keys.StudioKeyDomains(ownerID)
	if err != nil {
		return err
	}
	for _, authorized := range domains {
		if authorized == domain {
			return nil
		}
	}
	return DomainNotAuthorized()
}

// requireAccountInScope 校验账户在当前用户可派发范围（§4.1 两档口径，§4.3 公
// 共规则：越范围 403 studio_account_not_authorized）。
func (rt *studioRoutes) requireAccountInScope(r *http.Request, ownerID string, authRole string, accountID, domain string) error {
	options, err := rt.deps.AccountOptions.ListStudioAccountOptions(r.Context(), ownerID, isAdminRole(authRole), domain)
	if err != nil {
		return err
	}
	for _, option := range options {
		if option.AccountID == accountID {
			return nil
		}
	}
	return AccountNotAuthorized()
}

// resolveDomainModel 校验模型属于请求域目录并返回能力载荷（§4.3：model 必须
// 属于请求域目录，400 studio_model_domain_mismatch；能力载荷驱动可选参数门
// 控，未声明的项 400 不静默忽略）。endpointMode 非空时要求行端点模式包含该
// 形态（STT/TTS 页签同款过滤）。
func (rt *studioRoutes) resolveDomainModel(r *http.Request, ownerID string, authRole, model, domain, endpointMode string) (*StudioModel, error) {
	models, err := rt.deps.Models.ListDomainModels(r.Context(), ownerID, isAdminRole(authRole), domain)
	if err != nil {
		return nil, err
	}
	for index := range models {
		row := &models[index]
		if row.Model != model {
			continue
		}
		if endpointMode != "" && !studioModesContain(row.EndpointModes, endpointMode) {
			return nil, ModelDomainMismatch()
		}
		return row, nil
	}
	return nil, ModelDomainMismatch()
}

func studioModesContain(modes []string, want string) bool {
	for _, mode := range modes {
		if mode == want {
			return true
		}
	}
	return false
}

// --- Key 载荷（Bearer + 域授权来源） ---

// resolveStudioKeySecret 生成链受理前同步确保 purpose=studio Key 并取明文
// （§3.5：Key 是 /v1 Bearer 的载荷源，缺 Key 无法受理；账户候选面保持异步确
// 保不变）。
func (rt *studioRoutes) resolveStudioKeySecret(ownerID string) (string, error) {
	if rt.deps.Keys == nil {
		return "", errors.New("媒体工作室 Key 端口未装配")
	}
	keyID, err := rt.deps.Keys.EnsureStudioKey(ownerID)
	if err != nil {
		return "", err
	}
	return rt.deps.Keys.FindStudioKeySecret(keyID, ownerID)
}

// dispatchHeaders 构造 /v1 受理头（Bearer + JSON + 审计 purpose 头，§4.5.2）。
func studioDispatchHeaders(secret, purpose string) map[string]string {
	return map[string]string{
		"authorization":     "Bearer " + secret,
		"content-type":      "application/json",
		StudioPurposeHeader: purpose,
	}
}

// --- POST /generations/images（异步受理，受理即结算） ---

func (rt *studioRoutes) generationImages(w http.ResponseWriter, r *http.Request) {
	ownerID, auth, authErr := rt.requireStudioAuth(r)
	if authErr != nil {
		kernel.WriteError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	if err := rt.requireGenerationPorts(); err != nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	body, err := studioDecodeJSONObject(r, studioGenerationBodyMax)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if err := studioRejectUnknownKeys(body, "accountId", "model", "prompt", "negativePrompt",
		"size", "referenceAssetId", "providerOptions", "clientOperationId"); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	operationID, err := studioOperationIDOf(body)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	accountID, err := studioStringField(body, "accountId", true)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	model, err := studioStringField(body, "model", true)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	prompt, err := studioStringField(body, "prompt", true)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	providerOptions, hasProviderOptions, err := studioProviderOptionsOf(body)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if err := rt.requireDomainAuthorized(ownerID, "image"); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if err := rt.requireAccountInScope(r, ownerID, authRoleOf(auth), accountID, "image"); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	modelRow, err := rt.resolveDomainModel(r, ownerID, authRoleOf(auth), model, "image", "images_json")
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	caps := modelRow.Capabilities
	if raw, present := body["size"]; present && raw != nil {
		if !capabilityAllowsList(caps, "sizes", raw) {
			writeStudioRouteError(r, w, InvalidRequest("模型能力未声明该尺寸档位（sizes）"))
			return
		}
	}
	if raw, present := body["negativePrompt"]; present && raw != nil {
		if !capabilityFlag(caps, "supportsNegativePrompt") {
			writeStudioRouteError(r, w, InvalidRequest("模型能力未声明负向提示词（supportsNegativePrompt）"))
			return
		}
	}
	if raw, present := body["referenceAssetId"]; present && raw != nil {
		if !capabilityFlag(caps, "supportsReferenceImage") {
			writeStudioRouteError(r, w, InvalidRequest("模型能力未声明参考图输入（supportsReferenceImage）"))
			return
		}
	}
	secret, err := rt.resolveStudioKeySecret(ownerID)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusServiceUnavailable, "媒体工作室 Key 未就绪，请稍后重试", err)
		return
	}
	snapshot := map[string]any{"model": model, "prompt": prompt}
	if raw, present := body["size"]; present && raw != nil {
		snapshot["size"] = raw
	}
	if raw, present := body["negativePrompt"]; present && raw != nil {
		snapshot["negativePrompt"] = raw
	}
	if raw, present := body["referenceAssetId"]; present && raw != nil {
		snapshot["referenceAssetId"] = raw
	}
	if hasProviderOptions {
		snapshot["providerOptions"] = providerOptions
	}
	snapshotJSON, _ := json.Marshal(snapshot)
	task, created, err := rt.deps.Store.CreateTask(CreateTaskInput{
		SystemAccountID:     ownerID,
		ClientOperationID:   operationID,
		Kind:                "image",
		RequestSnapshotJSON: string(snapshotJSON),
		Now:                 rt.now(),
	})
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if !created {
		// 幂等重放：同 ID 返回原任务，不再调用上游（§4.0）。
		dto, dtoErr := rt.taskDTOFor(ownerID, task)
		if dtoErr != nil {
			writeStudioRouteError(r, w, dtoErr)
			return
		}
		writeOKDataMessage(w, dto, "已返回既有任务")
		return
	}
	// 后台执行 + 任务行承载（§4.3 images：提交即建任务 → 轮询终态）。
	go rt.executeImageTask(task, ownerID, accountID, secret, snapshot)
	dto, dtoErr := rt.taskDTOFor(ownerID, task)
	if dtoErr != nil {
		writeStudioRouteError(r, w, dtoErr)
		return
	}
	writeCreated(w, dto)
}

// requireGenerationPorts 是生成端点的装配守卫（执行链/账户/目录端口缺失属组
// 合根装配缺陷，显式 503 不静默）。
func (rt *studioRoutes) requireGenerationPorts() error {
	if rt.deps.Executor == nil {
		return errors.New("生成执行链未装配")
	}
	if rt.deps.AccountOptions == nil {
		return errors.New("账户范围端口未装配")
	}
	if rt.deps.Models == nil {
		return errors.New("模型目录端口未装配")
	}
	if rt.deps.Store == nil {
		return errors.New("任务仓库未装配")
	}
	return nil
}

// executeImageTask 是图像任务的后台执行体（§4.5 受理即结算路径）：同用户串
// 行 → 取消感知 → 进程内 /v1/images/generations（purpose studio_image）→ 2xx
// 即产物落 studio_assets + 任务单事务收口 completed+settled（§5.5 产物落库
// 事务，取消竞态经条件 UPDATE 门控丢弃产物）；上游失败 → failed 保留脱敏错
// 误。
func (rt *studioRoutes) executeImageTask(task *Task, ownerID, accountID, secret string, snapshot map[string]any) {
	unlock := rt.deps.serials.lock(ownerID + "\x00image")
	defer unlock()
	// 取消感知（§4.4 a 态）：任务已取消则停止发起，不再调用上游。
	current, err := rt.deps.Store.GetTask(task.ID, ownerID)
	if err != nil || current == nil || current.Status != "queued" {
		return
	}
	model, _ := snapshot["model"].(string)
	prompt, _ := snapshot["prompt"].(string)
	payload := map[string]any{"model": model, "prompt": prompt}
	if size, ok := snapshot["size"]; ok {
		payload["size"] = size
	}
	body, _ := json.Marshal(payload)
	execCtx, cancel := context.WithCancel(context.Background())
	rt.deps.flights.register(task.ID, cancel)
	defer rt.deps.flights.release(task.ID)
	defer cancel()
	response, err := rt.deps.Executor.Dispatch(execCtx, GatewayDispatchRequest{
		Method:          http.MethodPost,
		Path:            "/v1/images/generations",
		Headers:         studioDispatchHeaders(secret, PurposeStudioImage),
		Body:            body,
		DispatchAccount: accountID,
	})
	now := rt.now()
	if err != nil {
		rt.failTask(task.ID, ownerID, "studio_dispatch_exhausted", "生成派发失败，请稍后重试", now)
		return
	}
	if response.Status < 200 || response.Status >= 300 {
		summary := upstreamErrorMessage(string(response.Body), fmt.Sprintf("上游返回状态码 %d", response.Status))
		rt.failTask(task.ID, ownerID, "studio_upstream_failed", summary, now)
		return
	}
	imageBytes, decodeErr := studioImageBytesFromResponse(response.Body)
	if decodeErr != nil {
		rt.failTask(task.ID, ownerID, "studio_upstream_failed", decodeErr.Error(), now)
		return
	}
	mimeType := studioImageMimeFromBytes(imageBytes)
	if mimeType == "" {
		rt.failTask(task.ID, ownerID, "studio_upstream_failed", "生成图像格式无法识别", now)
		return
	}
	digest := sha256Hex(imageBytes)
	assetID := NewAssetID()
	storageKey := studioStorageKey(assetID, digest, mimeType)
	if rt.deps.ObjectStore == nil {
		rt.failTaskSettlement(task.ID, ownerID, "studio_settle_failed", "素材存储未装配", now)
		return
	}
	if writeErr := rt.deps.ObjectStore.Write(storageKey, imageBytes, studioImageMaxBytes, digest); writeErr != nil {
		rt.failTaskSettlement(task.ID, ownerID, "studio_settle_failed", "生成图像落库失败", now)
		return
	}
	width, height := imageDimensions(mimeType, imageBytes)
	assetParams, _ := json.Marshal(snapshot)
	asset, settleErr := rt.deps.Store.SettleTaskWithAsset(SettleTaskInput{
		TaskID:  task.ID,
		OwnerID: ownerID,
		Asset: CreateAssetInput{
			ID:              assetID,
			SystemAccountID: ownerID,
			Kind:            "image",
			Mime:            mimeType,
			StorageKey:      storageKey,
			Bytes:           int64(len(imageBytes)),
			Width:           width,
			Height:          height,
			Sha256:          digest,
			SourceTaskID:    &task.ID,
			ParamsJSON:      string(assetParams),
			Now:             now,
		},
		Now: now,
	})
	if settleErr != nil {
		// 行/账本未提交：已写对象必须回收（不残留占存储的孤儿对象）。
		_ = rt.deps.ObjectStore.Delete([]string{storageKey})
		var quota *QuotaExceededError
		var count *CountExceededError
		switch {
		case errors.Is(settleErr, ErrTaskSettleConflict):
			// 迟到成功（取消后上游完成）：任务保持 cancelled，产物不落库、
			// 不复活任务、账本一致（§4.4/§6）。
			return
		case errors.As(settleErr, &quota):
			rt.failTaskSettlement(task.ID, ownerID, "studio_settle_quota", "素材存储容量已满，产物未入库", now)
		case errors.As(settleErr, &count):
			rt.failTaskSettlement(task.ID, ownerID, "studio_settle_quota", "素材个数已达上限，产物未入库", now)
		default:
			rt.failTaskSettlement(task.ID, ownerID, "studio_settle_failed", "生成产物入库失败", now)
		}
		return
	}
	_ = asset
}

// studioImageBytesFromResponse 解析 /v1 images 响应载荷（OpenAI images 形态：
// b64_json 优先，url 形态有界下载）。
func studioImageBytesFromResponse(body []byte) ([]byte, error) {
	var parsed struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Data) == 0 {
		return nil, errors.New("生成图像响应缺少数据")
	}
	first := parsed.Data[0]
	if encoded := strings.TrimSpace(first.B64JSON); encoded != "" {
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, errors.New("生成图像 base64 解码失败")
		}
		return data, nil
	}
	if rawURL := strings.TrimSpace(first.URL); rawURL != "" {
		return studioDownloadBounded(rawURL, studioImageMaxBytes)
	}
	return nil, errors.New("生成图像响应缺少图像数据")
}

// studioDownloadBounded 有界下载产物 URL（仅 http/https；上限外失败不落库）。
func studioDownloadBounded(rawURL string, limit int64) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("生成图像产物地址无效")
	}
	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Get(rawURL)
	if err != nil {
		return nil, errors.New("生成图像产物下载失败")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("生成图像产物下载失败（HTTP %d）", response.StatusCode)
	}
	data, err := readAllLimited(response.Body, limit+1)
	if err != nil {
		return nil, errors.New("生成图像产物下载中断")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("生成图像产物超过大小上限")
	}
	return data, nil
}

// studioImageMimeFromBytes 嗅探图像 MIME（不信任上游 Content-Type，chat 先例）。
func studioImageMimeFromBytes(data []byte) string {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	detected := http.DetectContentType(head)
	switch detected {
	case "image/png", "image/jpeg", "image/webp":
		return detected
	}
	return ""
}

// failTask 落任务失败终态（无产物可结算 → settlement='settled'，§3.2）；收口
// 失败保留原始错误日志（下一轮取消/查询仍可见 queued 态，不静默改写）。
func (rt *studioRoutes) failTask(taskID, ownerID, code, message, now string) {
	errorJSON, _ := json.Marshal(map[string]string{"code": code, "message": message})
	if _, err := rt.deps.Store.FailTask(taskID, ownerID, string(errorJSON), now); err != nil {
		slog.Warn("studio 任务失败终态收口失败", "event", "studio_task_fail_failed", "taskId", taskID, "error", err.Error())
	}
}

// failTaskSettlement 落结算终局失败（status='failed' + settlement='failed'，
// §3.6：产物入库失败不虚计不静默丢弃；error 记原因）。
func (rt *studioRoutes) failTaskSettlement(taskID, ownerID, code, message, now string) {
	errorJSON, _ := json.Marshal(map[string]string{"code": code, "message": message})
	if _, err := rt.deps.Store.FailTaskSettlement(taskID, ownerID, string(errorJSON), now); err != nil {
		slog.Warn("studio 任务结算失败态收口失败", "event", "studio_task_settle_fail_failed", "taskId", taskID, "error", err.Error())
	}
}

// --- POST /generations/videos（异步受理，受理边界确立后 jobs 轮询收口） ---

func (rt *studioRoutes) generationVideos(w http.ResponseWriter, r *http.Request) {
	ownerID, auth, authErr := rt.requireStudioAuth(r)
	if authErr != nil {
		kernel.WriteError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	if err := rt.requireGenerationPorts(); err != nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	body, err := studioDecodeJSONObject(r, studioGenerationBodyMax)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if err := studioRejectUnknownKeys(body, "accountId", "model", "prompt", "firstFrameAssetId",
		"seconds", "size", "providerOptions", "clientOperationId"); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	operationID, err := studioOperationIDOf(body)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	accountID, err := studioStringField(body, "accountId", true)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	model, err := studioStringField(body, "model", true)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	prompt, err := studioStringField(body, "prompt", true)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	providerOptions, hasProviderOptions, err := studioProviderOptionsOf(body)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if err := rt.requireDomainAuthorized(ownerID, "video"); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if err := rt.requireAccountInScope(r, ownerID, authRoleOf(auth), accountID, "video"); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	modelRow, err := rt.resolveDomainModel(r, ownerID, authRoleOf(auth), model, "video", "video_create")
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	caps := modelRow.Capabilities
	if raw, present := body["firstFrameAssetId"]; present && raw != nil {
		if !capabilityFlag(caps, "supportsFirstFrame") {
			writeStudioRouteError(r, w, InvalidRequest("模型能力未声明图生视频首帧（supportsFirstFrame）"))
			return
		}
	}
	if raw, present := body["seconds"]; present && raw != nil {
		if !capabilityAllowsList(caps, "secondsOptions", raw) {
			writeStudioRouteError(r, w, InvalidRequest("模型能力未声明该时长档位（secondsOptions）"))
			return
		}
	}
	if raw, present := body["size"]; present && raw != nil {
		if !capabilityAllowsList(caps, "sizeOptions", raw) {
			writeStudioRouteError(r, w, InvalidRequest("模型能力未声明该尺寸档位（sizeOptions）"))
			return
		}
	}
	secret, err := rt.resolveStudioKeySecret(ownerID)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusServiceUnavailable, "媒体工作室 Key 未就绪，请稍后重试", err)
		return
	}
	snapshot := map[string]any{"model": model, "prompt": prompt}
	if raw, present := body["seconds"]; present && raw != nil {
		snapshot["seconds"] = raw
	}
	if raw, present := body["size"]; present && raw != nil {
		snapshot["size"] = raw
	}
	if hasProviderOptions {
		snapshot["providerOptions"] = providerOptions
	}
	snapshotJSON, _ := json.Marshal(snapshot)
	// 同用户创建串行化（§4.5.7）：受理全程持锁，排队而非拒绝。
	unlock := rt.deps.serials.lock(ownerID + "\x00video")
	defer unlock()
	task, created, err := rt.deps.Store.CreateTask(CreateTaskInput{
		SystemAccountID:     ownerID,
		ClientOperationID:   operationID,
		Kind:                "video",
		RequestSnapshotJSON: string(snapshotJSON),
		Now:                 rt.now(),
	})
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if !created {
		dto, dtoErr := rt.taskDTOFor(ownerID, task)
		if dtoErr != nil {
			writeStudioRouteError(r, w, dtoErr)
			return
		}
		writeOKDataMessage(w, dto, "已返回既有任务")
		return
	}
	payload := map[string]any{"model": model, "prompt": prompt}
	if raw, present := body["seconds"]; present && raw != nil {
		payload["seconds"] = raw
	}
	if raw, present := body["size"]; present && raw != nil {
		payload["size"] = raw
	}
	if hasProviderOptions {
		payload["provider_options"] = providerOptions
	}
	requestBody, _ := json.Marshal(payload)
	response, dispatchErr := rt.deps.Executor.Dispatch(r.Context(), GatewayDispatchRequest{
		Method:          http.MethodPost,
		Path:            "/v1/videos",
		Headers:         studioDispatchHeaders(secret, PurposeStudioVideo),
		Body:            requestBody,
		DispatchAccount: accountID,
	})
	now := rt.now()
	if dispatchErr != nil {
		// 受理前派发失败（穷尽/不可用）：短路报错 + 任务 failed 收口。
		rt.failTask(task.ID, ownerID, "studio_dispatch_exhausted", "生成派发失败，请稍后重试", now)
		writeStudioRouteError(r, w, UpstreamUnavailable())
		return
	}
	if response.Status == 400 || response.Status == 413 || response.Status == 422 {
		// 确定性 4xx 参数类错误短路（§4.5.4：不换账户不重试，原样映射 400）。
		summary := upstreamErrorMessage(string(response.Body), fmt.Sprintf("上游返回状态码 %d", response.Status))
		rt.failTask(task.ID, ownerID, "studio_upstream_failed", summary, now)
		writeStudioRouteError(r, w, InvalidRequest("生成参数被上游拒绝：%s", summary))
		return
	}
	if response.Status < 200 || response.Status >= 300 {
		summary := upstreamErrorMessage(string(response.Body), fmt.Sprintf("上游返回状态码 %d", response.Status))
		rt.failTask(task.ID, ownerID, "studio_dispatch_exhausted", summary, now)
		writeStudioRouteError(r, w, UpstreamUnavailable())
		return
	}
	var job struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(response.Body, &job); err != nil || strings.TrimSpace(job.ID) == "" {
		rt.failTask(task.ID, ownerID, "studio_upstream_failed", "上游响应缺少任务凭据（job id）", now)
		writeStudioRouteError(r, w, GenerationFailed("上游响应缺少任务凭据"))
		return
	}
	providerCode := strings.TrimSpace(job.Provider)
	if providerCode == "" {
		providerCode = modelRow.ProviderCode
	}
	// 受理确立（2xx + job id）：账户亲和三元组一次落定，upstream_job_id 此后
	// 永不重建（§4.5.5）。取消竞态先落 cancelled 时 0 行：任务保持取消态，产
	// 物按取消语义不落库（jobs 领取谓词对 cancelled+settled 不命中）。
	if _, err := rt.deps.Store.MarkTaskDispatched(task.ID, ownerID, accountID, providerCode, strings.TrimSpace(job.ID), now); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	fresh, err := rt.deps.Store.GetTask(task.ID, ownerID)
	if err != nil || fresh == nil {
		writeStudioRouteError(r, w, err)
		return
	}
	dto, dtoErr := rt.taskDTOFor(ownerID, fresh)
	if dtoErr != nil {
		writeStudioRouteError(r, w, dtoErr)
		return
	}
	writeCreated(w, dto)
}

// --- POST /generations/speech（同步 TTS，收据幂等） ---

func (rt *studioRoutes) generationSpeech(w http.ResponseWriter, r *http.Request) {
	ownerID, auth, authErr := rt.requireStudioAuth(r)
	if authErr != nil {
		kernel.WriteError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	if err := rt.requireGenerationPorts(); err != nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	body, err := studioDecodeJSONObject(r, studioGenerationBodyMax)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if err := studioRejectUnknownKeys(body, "accountId", "model", "input", "voice", "speed",
		"volume", "emotion", "responseFormat", "providerOptions", "clientOperationId"); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	operationID, err := studioOperationIDOf(body)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	accountID, err := studioStringField(body, "accountId", true)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	model, err := studioStringField(body, "model", true)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	input, err := studioStringField(body, "input", true)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	providerOptions, hasProviderOptions, err := studioProviderOptionsOf(body)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if err := rt.requireDomainAuthorized(ownerID, "audio"); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if err := rt.requireAccountInScope(r, ownerID, authRoleOf(auth), accountID, "audio"); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	modelRow, err := rt.resolveDomainModel(r, ownerID, authRoleOf(auth), model, "audio", "audio_speech")
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	caps := modelRow.Capabilities
	if err := studioGateSpeechParams(caps, body); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	// 同用户创建串行化（§4.5.7）+ 收据幂等（§3.3）：同 ID 命中收据直接返回原
	// 资产，不再调用上游、不重复计费。
	unlock := rt.deps.serials.lock(ownerID + "\x00" + OperationKindAudioSpeech)
	defer unlock()
	if existing, opErr := rt.deps.Store.GetOperation(ownerID, operationID, OperationKindAudioSpeech); opErr != nil {
		writeStudioRouteError(r, w, opErr)
		return
	} else if existing != nil {
		rt.replySpeechReceipt(r, w, ownerID, existing)
		return
	}
	secret, err := rt.resolveStudioKeySecret(ownerID)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusServiceUnavailable, "媒体工作室 Key 未就绪，请稍后重试", err)
		return
	}
	payload := map[string]any{"model": model, "input": input}
	for name, key := range map[string]string{"voice": "voice", "responseFormat": "response_format",
		"speed": "speed", "volume": "volume", "emotion": "emotion"} {
		if raw, present := body[name]; present && raw != nil {
			payload[key] = raw
		}
	}
	if hasProviderOptions {
		payload["provider_options"] = providerOptions
	}
	requestBody, _ := json.Marshal(payload)
	response, dispatchErr := rt.deps.Executor.Dispatch(r.Context(), GatewayDispatchRequest{
		Method:          http.MethodPost,
		Path:            "/v1/audio/speech",
		Headers:         studioDispatchHeaders(secret, PurposeStudioAudio),
		Body:            requestBody,
		DispatchAccount: accountID,
	})
	if dispatchErr != nil {
		writeStudioRouteError(r, w, UpstreamUnavailable())
		return
	}
	if response.Status == 400 || response.Status == 413 || response.Status == 422 {
		summary := upstreamErrorMessage(string(response.Body), fmt.Sprintf("上游返回状态码 %d", response.Status))
		writeStudioRouteError(r, w, InvalidRequest("生成参数被上游拒绝：%s", summary))
		return
	}
	if response.Status < 200 || response.Status >= 300 {
		summary := upstreamErrorMessage(string(response.Body), fmt.Sprintf("上游返回状态码 %d", response.Status))
		writeStudioRouteError(r, w, GenerationFailed(summary))
		return
	}
	if int64(len(response.Body)) > studioAudioMaxBytes {
		writeStudioRouteError(r, w, GenerationFailed("生成音频超过大小上限"))
		return
	}
	mimeType := studioAudioMimeFromBytes(response.Body)
	if mimeType == "" {
		mimeType = studioAudioHeaderMime(response.Header)
	}
	if mimeType == "" {
		writeStudioRouteError(r, w, GenerationFailed("语音合成响应缺少有效的音频格式"))
		return
	}
	if err := rt.deps.Store.AssertQuotaAvailable(ownerID, int64(len(response.Body))); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	digest := sha256Hex(response.Body)
	assetID := NewAssetID()
	storageKey := studioStorageKey(assetID, digest, mimeType)
	if rt.deps.ObjectStore == nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, "素材存储未装配")
		return
	}
	if writeErr := rt.deps.ObjectStore.Write(storageKey, response.Body, studioAudioMaxBytes, digest); writeErr != nil {
		writeStudioRouteError(r, w, writeErr)
		return
	}
	assetParams := map[string]any{"model": model, "input": input}
	if raw, present := body["voice"]; present && raw != nil {
		assetParams["voice"] = raw
	}
	if raw, present := body["responseFormat"]; present && raw != nil {
		assetParams["responseFormat"] = raw
	}
	if hasProviderOptions {
		assetParams["providerOptions"] = providerOptions
	}
	paramsJSON, _ := json.Marshal(assetParams)
	asset, err := rt.deps.Store.CreateOperationWithAsset(
		CreateAssetInput{
			ID:              assetID,
			SystemAccountID: ownerID,
			Kind:            "audio",
			Mime:            mimeType,
			StorageKey:      storageKey,
			Bytes:           int64(len(response.Body)),
			DurationMs:      nil,
			Sha256:          digest,
			ParamsJSON:      string(paramsJSON),
			Now:             rt.now(),
		},
		CreateOperationInput{
			SystemAccountID:   ownerID,
			ClientOperationID: operationID,
			Kind:              OperationKindAudioSpeech,
			Now:               rt.now(),
		})
	if err != nil {
		// 行/账本未提交：回收已写对象（不残留占存储的孤儿对象）。
		_ = rt.deps.ObjectStore.Delete([]string{storageKey})
		if errors.Is(err, ErrOperationExists) {
			// 并发同 ID 败者：回读既有收据按原结果返回（§3.3）。
			if existing, opErr := rt.deps.Store.GetOperation(ownerID, operationID, OperationKindAudioSpeech); opErr == nil && existing != nil {
				rt.replySpeechReceipt(r, w, ownerID, existing)
				return
			}
		}
		writeStudioRouteError(r, w, err)
		return
	}
	writeOKData(w, assetDTOOf(asset))
}

// replySpeechReceipt 按收据返回既有资产（§3.3：资产被用户删除后收据保留，同
// ID 重放返回资产缺失 404，不重新生成）。
func (rt *studioRoutes) replySpeechReceipt(r *http.Request, w http.ResponseWriter, ownerID string, receipt *Operation) {
	if receipt.AssetID == nil {
		writeStudioRouteError(r, w, AssetNotFound())
		return
	}
	asset, err := rt.deps.Store.GetAsset(*receipt.AssetID, ownerID)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if asset == nil {
		writeStudioRouteError(r, w, AssetNotFound())
		return
	}
	writeOKDataMessage(w, assetDTOOf(asset), "已返回既有结果")
}

// studioGateSpeechParams 是 TTS 可选参数的能力门控（§4.1 capabilities 规范
// 键：voice 需 voices[] 非空且此时必填；responseFormat 需 ∈ responseFormats[]；
// speed/volume/emotion 需对应能力键声明——P0 目录无能力载荷来源，传入即 400，
// 不猜测不降级）。
func studioGateSpeechParams(caps map[string]any, body map[string]any) error {
	voices := capabilityList(caps, "voices")
	if raw, present := body["voice"]; present && raw != nil {
		if len(voices) == 0 {
			return InvalidRequest("模型能力未声明音色列表（voices）")
		}
		if !capabilityAllowsList(caps, "voices", raw) {
			return InvalidRequest("音色不在模型能力声明的 voices 列表内")
		}
	} else if len(voices) > 0 {
		return InvalidRequest("该模型声明了音色列表，voice 必填")
	}
	if raw, present := body["responseFormat"]; present && raw != nil {
		if len(capabilityList(caps, "responseFormats")) == 0 {
			return InvalidRequest("模型能力未声明响应格式（responseFormats）")
		}
		if !capabilityAllowsList(caps, "responseFormats", raw) {
			return InvalidRequest("响应格式不在模型能力声明的 responseFormats 列表内")
		}
	}
	if raw, present := body["speed"]; present && raw != nil {
		if _, declared := caps["speedRange"]; !declared {
			return InvalidRequest("模型能力未声明语速范围（speedRange）")
		}
	}
	if raw, present := body["volume"]; present && raw != nil {
		if _, declared := caps["volume"]; !declared {
			return InvalidRequest("模型能力未声明音量能力（volume）")
		}
	}
	if raw, present := body["emotion"]; present && raw != nil {
		if _, declared := caps["emotionOptions"]; !declared {
			return InvalidRequest("模型能力未声明情感选项（emotionOptions）")
		}
	}
	return nil
}

// studioAudioMimeFromBytes 嗅探音频封装（不信任上游 Content-Type，chat 先例）。
func studioAudioMimeFromBytes(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 'I' && data[1] == 'D' && data[2] == '3':
		return "audio/mpeg"
	case len(data) >= 2 && data[0] == 0xFF && data[1]&0xE0 == 0xE0:
		return "audio/mpeg"
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return "audio/wav"
	case len(data) >= 4 && string(data[0:4]) == "OggS":
		return "audio/ogg"
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		return "audio/mp4"
	}
	return ""
}

// studioAudioHeaderMime 回落上游 Content-Type（仅接受 audio/* 且嗅探不识别时）。
func studioAudioHeaderMime(header http.Header) string {
	if header == nil {
		return ""
	}
	contentType := strings.ToLower(strings.TrimSpace(header.Get("Content-Type")))
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = strings.TrimSpace(contentType[:idx])
	}
	if strings.HasPrefix(contentType, "audio/") {
		return contentType
	}
	return ""
}

// --- POST /generations/transcriptions（同步 STT，multipart 透传 + 收据幂等） ---

type studioTranscriptionUpload struct {
	data               []byte
	accountID          string
	model              string
	clientOperationID  string
	language           string
	providerOptions    map[string]any
	hasProviderOptions bool
}

// readTranscriptionUpload 解析 STT multipart 表单（§4.3：file 必填音频 ≤25MB，
// accountId/model/clientOperationId 必填，language/providerOptions 可选）。
func readTranscriptionUpload(r *http.Request) (*studioTranscriptionUpload, error) {
	mediaType, _, err := parseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return nil, InvalidRequest("转写请求必须使用 multipart/form-data")
	}
	reader, readerErr := r.MultipartReader()
	if readerErr != nil || reader == nil {
		return nil, InvalidRequest("multipart 请求格式无效")
	}
	upload := &studioTranscriptionUpload{}
	sawFile := false
	seenProviderOptions := false
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			return nil, InvalidRequest("转写上传连接已中断")
		}
		switch part.FormName() {
		case "file":
			if sawFile {
				_ = part.Close()
				return nil, InvalidRequest("每次只能上传一个 file 音频字段")
			}
			declared := strings.ToLower(strings.TrimSpace(part.Header.Get("Content-Type")))
			if idx := strings.Index(declared, ";"); idx >= 0 {
				declared = strings.TrimSpace(declared[:idx])
			}
			data, readErr := readAllLimited(part, studioTranscriptionMaxBytes+1)
			_ = part.Close()
			if readErr != nil {
				return nil, InvalidRequest("转写上传连接已中断")
			}
			if int64(len(data)) > studioTranscriptionMaxBytes {
				return nil, InvalidRequest("转写音频不能超过 25MB")
			}
			if len(data) == 0 {
				return nil, InvalidRequest("转写音频不能为空")
			}
			if !strings.HasPrefix(declared, "audio/") && studioAudioMimeFromBytes(data) == "" {
				return nil, InvalidRequest("转写文件必须是音频")
			}
			upload.data = data
			sawFile = true
		case "accountId":
			value, fieldErr := readTextFormPart(part, 256)
			if fieldErr != nil {
				return nil, fieldErr
			}
			upload.accountID = value
		case "model":
			value, fieldErr := readTextFormPart(part, 256)
			if fieldErr != nil {
				return nil, fieldErr
			}
			upload.model = value
		case "clientOperationId":
			value, fieldErr := readTextFormPart(part, 256)
			if fieldErr != nil {
				return nil, fieldErr
			}
			upload.clientOperationID = value
		case "language":
			value, fieldErr := readTextFormPart(part, 64)
			if fieldErr != nil {
				return nil, fieldErr
			}
			upload.language = value
		case "providerOptions":
			if seenProviderOptions {
				_ = part.Close()
				return nil, InvalidRequest("providerOptions 字段只能出现一次")
			}
			seenProviderOptions = true
			value, fieldErr := readTextFormPart(part, 64*1024)
			if fieldErr != nil {
				return nil, fieldErr
			}
			if strings.TrimSpace(value) == "" {
				continue
			}
			var parsed map[string]any
			if err := json.Unmarshal([]byte(value), &parsed); err != nil || parsed == nil {
				return nil, InvalidRequest("providerOptions 必须是 JSON 对象")
			}
			upload.providerOptions = parsed
			upload.hasProviderOptions = true
		default:
			_ = part.Close()
			return nil, InvalidRequest("转写上传不能包含额外表单字段 %q", part.FormName())
		}
	}
	if !sawFile {
		return nil, InvalidRequest("缺少 file 音频字段")
	}
	if upload.accountID == "" || upload.model == "" {
		return nil, InvalidRequest("accountId 与 model 必填")
	}
	trimmed := strings.TrimSpace(upload.clientOperationID)
	length := utf8.RuneCountInString(trimmed)
	if length < 1 || length > 128 {
		return nil, InvalidRequest("clientOperationId 必填且为 1 到 128 个字符")
	}
	upload.clientOperationID = trimmed
	return upload, nil
}

func readTextFormPart(part interface {
	Read([]byte) (int, error)
	Close() error
}, limit int64) (string, error) {
	data, err := readAllLimited(part, limit+1)
	_ = part.Close()
	if err != nil {
		return "", InvalidRequest("转写上传连接已中断")
	}
	if int64(len(data)) > limit {
		return "", InvalidRequest("转写表单字段超过上限")
	}
	return strings.TrimSpace(string(data)), nil
}

func (rt *studioRoutes) generationTranscriptions(w http.ResponseWriter, r *http.Request) {
	ownerID, auth, authErr := rt.requireStudioAuth(r)
	if authErr != nil {
		kernel.WriteError(w, http.StatusUnauthorized, authErr.Error())
		return
	}
	if err := rt.requireGenerationPorts(); err != nil {
		kernel.WriteError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	upload, err := readTranscriptionUpload(r)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	// 域授权词表：STT 属 studio_text 审计域（§4.5.2）；模型/账户候选按 audio
	// 能力域的 audio_transcription 形态过滤（§4.1：text 域 P0 无工作台目录）。
	if err := rt.requireDomainAuthorized(ownerID, "text"); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if err := rt.requireAccountInScope(r, ownerID, authRoleOf(auth), upload.accountID, "audio"); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	if _, err := rt.resolveDomainModel(r, ownerID, authRoleOf(auth), upload.model, "audio", "audio_transcription"); err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	// 同用户创建串行化（§4.5.7）+ 收据幂等（§3.3）。
	unlock := rt.deps.serials.lock(ownerID + "\x00" + OperationKindAudioTranscription)
	defer unlock()
	if existing, opErr := rt.deps.Store.GetOperation(ownerID, upload.clientOperationID, OperationKindAudioTranscription); opErr != nil {
		writeStudioRouteError(r, w, opErr)
		return
	} else if existing != nil {
		rt.replyTranscriptionReceipt(r, w, existing)
		return
	}
	secret, err := rt.resolveStudioKeySecret(ownerID)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusServiceUnavailable, "媒体工作室 Key 未就绪，请稍后重试", err)
		return
	}
	requestBody, contentType, err := studioTranscriptionForwardBody(upload)
	if err != nil {
		writeStudioRouteError(r, w, err)
		return
	}
	headers := map[string]string{
		"authorization":     "Bearer " + secret,
		"content-type":      contentType,
		StudioPurposeHeader: PurposeStudioText,
	}
	response, dispatchErr := rt.deps.Executor.Dispatch(r.Context(), GatewayDispatchRequest{
		Method:          http.MethodPost,
		Path:            "/v1/audio/transcriptions",
		Headers:         headers,
		Body:            requestBody,
		DispatchAccount: upload.accountID,
	})
	if dispatchErr != nil {
		writeStudioRouteError(r, w, UpstreamUnavailable())
		return
	}
	if response.Status == 400 || response.Status == 413 || response.Status == 422 {
		summary := upstreamErrorMessage(string(response.Body), fmt.Sprintf("上游返回状态码 %d", response.Status))
		writeStudioRouteError(r, w, InvalidRequest("生成参数被上游拒绝：%s", summary))
		return
	}
	if response.Status < 200 || response.Status >= 300 {
		summary := upstreamErrorMessage(string(response.Body), fmt.Sprintf("上游返回状态码 %d", response.Status))
		writeStudioRouteError(r, w, GenerationFailed(summary))
		return
	}
	var result struct {
		Text string `json:"text"`
		SRT  string `json:"srt"`
	}
	if err := json.Unmarshal(response.Body, &result); err != nil {
		writeStudioRouteError(r, w, GenerationFailed("转写响应无法解析"))
		return
	}
	// 基础输出为 text；SRT 仅当上游给出（不能从普通文本虚构时间轴，§4.3）。
	resultPayload := map[string]any{"text": result.Text}
	if strings.TrimSpace(result.SRT) != "" {
		resultPayload["srt"] = result.SRT
	}
	resultJSON, _ := json.Marshal(resultPayload)
	if _, err := rt.deps.Store.CreateOperation(CreateOperationInput{
		SystemAccountID:   ownerID,
		ClientOperationID: upload.clientOperationID,
		Kind:              OperationKindAudioTranscription,
		ResultJSON:        string(resultJSON),
		Now:               rt.now(),
	}); err != nil {
		if errors.Is(err, ErrOperationExists) {
			// 并发同 ID 败者：回读既有收据按原结果返回（§3.3）。
			if existing, opErr := rt.deps.Store.GetOperation(ownerID, upload.clientOperationID, OperationKindAudioTranscription); opErr == nil && existing != nil {
				rt.replyTranscriptionReceipt(r, w, existing)
				return
			}
		}
		writeStudioRouteError(r, w, err)
		return
	}
	writeOKDataMessage(w, resultPayload, "转写完成")
}

// replyTranscriptionReceipt 按收据返回既有转写结果（§3.3）。
func (rt *studioRoutes) replyTranscriptionReceipt(r *http.Request, w http.ResponseWriter, receipt *Operation) {
	payload := map[string]any{}
	if strings.TrimSpace(receipt.ResultJSON) != "" {
		_ = json.Unmarshal([]byte(receipt.ResultJSON), &payload)
	}
	writeOKDataMessage(w, payload, "已返回既有结果")
}

// studioTranscriptionForwardBody 重建 /v1 转写 multipart（file + model +
// language? + provider_options?）。
func studioTranscriptionForwardBody(upload *studioTranscriptionUpload) ([]byte, string, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if err := writer.WriteField("model", upload.model); err != nil {
		return nil, "", err
	}
	if upload.language != "" {
		if err := writer.WriteField("language", upload.language); err != nil {
			return nil, "", err
		}
	}
	if upload.hasProviderOptions {
		encoded, _ := json.Marshal(upload.providerOptions)
		if err := writer.WriteField("provider_options", string(encoded)); err != nil {
			return nil, "", err
		}
	}
	part, err := writer.CreateFormFile("file", "studio-audio.bin")
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(upload.data); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}
