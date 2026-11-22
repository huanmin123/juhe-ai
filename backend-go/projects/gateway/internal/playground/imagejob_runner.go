package playground

// 演练场图像任务进程内执行器（设计 §3.4.2，v1.2 异步生图）。
//
// 形态：缓冲 channel（容量 16）+ 2 worker goroutine，全部在 gateway 进程内；
// 队列满时 Submit 显式拒绝（503 playground_busy，不无限积压）。网关重启丢
// 失队列——queued 行由启动恢复重新入队，running 行置 failed（§3.4.2 启动
// 恢复），故 Submit 携带完整任务记录，worker 不回读存储即可派发。
//
// 派发收编 v1.0 images.go 的同步生图逻辑（该端点随 §8/v1.2 退役）：
//   - 无参考图 → POST /v1/images/generations（JSON，response_format=b64_json
//     优先）；
//   - 有参考图 → POST /v1/images/edits（multipart，fields 在前、image parts
//     在后——网关计量扫描层 fields 上限 16 / parts 上限 24，顺序保证计量字段
//     可提取；多参考图为多个 image part 直通上游裁决）；
//   - 上游只回临时 URL（grok）时经 SSRF 守卫客户端回退下载字节；
//   - 结果落 artifacts(kind='image_result', job_id=任务id)，恒 1 张；
//   - usage 解析宽容（缺省 0，不因此判失败）；扣费经 CostPort 按目录行本地
//     计算（无价 → cost_usd=null，卡片显示「-」，不虚构）；
//   - 终态 Complete / Fail（error_message 存上游错误原文摘要）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"time"
)

// imageJobQueueCapacity 是进程内队列容量（§3.4.2：显式拒绝不无限积压）；
// imageJobWorkers 是 worker 数（生图上游耗时秒级~十秒级，2 并发够演示台口径）；
// imageJobExecutionTimeout 是单任务执行上限（M4：派发 + URL 回退下载全程）。
const (
	imageJobQueueCapacity    = 16
	imageJobWorkers          = 2
	imageJobExecutionTimeout = 10 * time.Minute
)

// 上游 quality/output_format 派发词表（B6：任务参数真实生效）。
//
// quality 项目展示词表（standard 标准 | fine 精细，设计 §3.4.2）映射为上游
// gpt-image 系 quality 词表：standard→"medium"、fine→"high"。取证口径：中转
// 链不校验该字段——gatewayopenai lane 对 /v1/images* 直通、计量扫描层只提取
// 字段不改写（gatewayresponse/usage_media_metering），openaicompatstorage/
// imagegeneration.go:126-127 的 setOptionalString 透传形态证明 quality/
// output_format 原样到达真实上游，词表由上游裁决；本包内无其他既有映射口径
// （rg 全 internal/ 无 standard→medium 先例），按工作台裁决映射。
func imageJobQualityForUpstream(quality string) string {
	switch quality {
	case "standard":
		return "medium"
	case "fine":
		return "high"
	default:
		// handler 词表校验（standard|fine）外的值不在这里造错误，原样透传交
		// 上游裁决（与 openaicompatstorage 透传形态一致）。
		return quality
	}
}

// errImageJobQueueBusy 是队列满/已停止时的 Submit 拒绝哨兵（handler 映射
// 503 playground_busy）。
var errImageJobQueueBusy = errors.New("playground image job queue busy")

// ImageCostPort 是扣费计算端口（cmd 层注入：runtime cache 目录行 ×
// pricing.BuildCostBreakdown 本地计算；目录无价/行缺失 → false →
// cost_usd=null）。imageCount 恒 1（单次恒 1 张，无 n）。
type ImageCostPort interface {
	ImageCost(providerCode, model, size string, imageCount int, inputTokens, outputTokens int64) (float64, bool)
}

// imageJobTask 是队列元素：Create/RecoverAtBoot 产出的完整记录（worker 免回
// 读；Claim 的原子 UPDATE 仍是重复执行的唯一防线）。
type imageJobTask struct {
	record ImageJobRecord
}

// ImageJobRunner 是进程内任务执行器。构造即启动 worker（功能默认生效，无开
// 关）；Stop 供测试优雅收尾（生产随进程退出，靠启动恢复收敛）。
type ImageJobRunner struct {
	deps     *Deps
	store    ImageJobStore
	cost     ImageCostPort
	queue    chan imageJobTask
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewImageJobRunner 构造执行器并启动 worker。deps 提供派发与账户端口；cost
// 可为 nil（= 目录无价语义，cost_usd 恒 null）。
func NewImageJobRunner(deps *Deps, store ImageJobStore, cost ImageCostPort) *ImageJobRunner {
	runner := &ImageJobRunner{
		deps:  deps,
		store: store,
		cost:  cost,
		queue: make(chan imageJobTask, imageJobQueueCapacity),
		stop:  make(chan struct{}),
	}
	for index := 0; index < imageJobWorkers; index++ {
		runner.wg.Add(1)
		go runner.worker()
	}
	return runner
}

// Submit 入队一份已落库的 queued 任务；队列满或执行器已停止返回
// errImageJobQueueBusy。
func (r *ImageJobRunner) Submit(record ImageJobRecord) error {
	if r == nil {
		return errImageJobQueueBusy
	}
	select {
	case r.queue <- imageJobTask{record: record}:
		return nil
	default:
		return errImageJobQueueBusy
	}
}

// RecoverAtBoot 完成启动恢复（§3.4.2）：store 层把 running 置 failed 并返回
// queued 行，随后逐条重新入队（队列容量 16 < 待恢复数时后台节拍重试，直到
// 全部入队或执行器停止——恢复路径不得静默丢弃任务）。
func (r *ImageJobRunner) RecoverAtBoot() ([]ImageJobRecord, error) {
	if r == nil || r.store == nil {
		return nil, errImageJobStoreUnavailable
	}
	queued, err := r.store.RecoverAtBoot()
	if err != nil {
		return nil, err
	}
	if len(queued) > 0 {
		r.wg.Add(1)
		go r.drainRecovered(queued)
	}
	return queued, nil
}

// drainRecovered 把启动恢复的 queued 行喂回队列（满时 100ms 节拍重试）。
func (r *ImageJobRunner) drainRecovered(queued []ImageJobRecord) {
	defer r.wg.Done()
	for _, record := range queued {
		enqueued := false
		for !enqueued {
			select {
			case r.queue <- imageJobTask{record: record}:
				enqueued = true
			case <-r.stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}

// worker 消费队列直到停止；in-flight 任务在 Stop 返回前执行完（优雅停止，
// 测试口径；生产进程退出由启动恢复兜底）。
func (r *ImageJobRunner) worker() {
	defer r.wg.Done()
	for {
		select {
		case <-r.stop:
			return
		case task := <-r.queue:
			r.executeJob(task.record)
		}
	}
}

// Stop 关闭队列并等待 worker（含恢复入队 goroutine）退出。
func (r *ImageJobRunner) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
	r.wg.Wait()
}

// executeJob 执行单个任务：Claim（原子防重启恢复竞态）→ 派发 → 归一 → 落
// artifacts → 计价 → Complete；任一步失败 Fail（error_message 存上游/本地错
// 误原文摘要，无图无 expires_at）。派发与下载全程受 imageJobExecutionTimeout
// 约束（M4）：与派发执行器、下载客户端的链内既有超时双保险——任一内层超时
// 缺失或失效时，任务不会被无限占用 worker，超时经派发错误路径 Fail 收敛终态，
// 不产生僵尸 running 行。
func (r *ImageJobRunner) executeJob(record ImageJobRecord) {
	claimed, claimErr := r.store.Claim(record.ViewerID, record.ID)
	if claimErr != nil || !claimed {
		// claim 失败 = 任务已被启动恢复置 failed 或记录异常：放弃执行（不
		// 覆盖既有终态）。
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), imageJobExecutionTimeout)
	defer cancel()
	decoded, usage, runErr := r.generateImage(ctx, record)
	if runErr != nil {
		r.failJob(record, runErr.Message)
		return
	}
	if len(decoded) == 0 {
		r.failJob(record, "生图结果为空")
		return
	}
	resultID, saveErr := r.saveResultArtifact(record, decoded[0])
	if saveErr != nil {
		r.failJob(record, "生成结果落库失败: "+saveErr.Error())
		return
	}
	costUSD := r.resolveCost(record, usage)
	if err := r.store.Complete(record.ViewerID, record.ID, []string{resultID}, usage, costUSD, record.Size); err != nil {
		r.failJob(record, "任务完结落库失败: "+err.Error())
	}
}

// failJob 是终态失败收敛（error_message 存摘要原文；落库失败本身不产生用户
// 可见错误面——任务卡片 failed 即契约透出口）。
func (r *ImageJobRunner) failJob(record ImageJobRecord, message string) {
	if err := r.store.Fail(record.ViewerID, record.ID, message); err != nil {
		// 保留原始错误不可达时的兜底：状态机停在 running，由下一次启动恢复
		// 置 failed（设计 §3.4.2 语义），不静默改写为其他终态。
		_ = err
	}
}

// generateImage 构造派发并归一结果（§3.4.2 收编 v1.0 派发口径；真实 Key 化
// 修订：按任务 params 里的 apiKeyId 派发，计费随该 Key 走完整 /v1 链）。ctx
// 携带任务级 deadline（imageJobExecutionTimeout）。返回已解码产物与宽容解析
// 的 usage。缺 apiKeyId 的存量任务按失败收敛（错误信息说明原因，不静默改
// 派发通道）。
func (r *ImageJobRunner) generateImage(ctx context.Context, record ImageJobRecord) ([]decodedImage, ImageJobUsage, *HTTPError) {
	if strings.TrimSpace(record.APIKeyID) == "" {
		return nil, ImageJobUsage{}, &HTTPError{Status: http.StatusBadGateway, Code: codeDispatchFailed, Message: "任务缺少所选 API Key（存量任务创建于真实 Key 派发改版前，无法按 Key 计费派发，请重新创建任务）"}
	}
	headers := map[string]string{
		"accept": "application/json",
	}
	var request DispatchRequest
	if len(record.ReferenceIDs) == 0 {
		headers["content-type"] = "application/json"
		payloadValues := map[string]any{"model": record.ModelID, "prompt": record.Prompt}
		if record.Size != "" {
			payloadValues["size"] = record.Size
		}
		if record.Quality != "" {
			payloadValues["quality"] = imageJobQualityForUpstream(record.Quality)
		}
		if record.OutputFormat != "" {
			payloadValues["output_format"] = record.OutputFormat
		}
		// b64_json 优先：结果字节落服务端暂存（§3.8）；grok 等上游只回临时
		// URL 时由 decodedImagesFromPayload 回退下载。
		payloadValues["response_format"] = "b64_json"
		payload, _ := json.Marshal(payloadValues)
		request = DispatchRequest{
			APIKeyID: record.APIKeyID, OwnerID: record.ViewerID,
			Path: "/v1/images/generations", Method: http.MethodPost, Headers: headers, Body: payload,
		}
	} else {
		body, contentType, buildErr := r.buildEditsMultipart(record)
		if buildErr != nil {
			return nil, ImageJobUsage{}, buildErr
		}
		headers["content-type"] = contentType
		request = DispatchRequest{
			APIKeyID: record.APIKeyID, OwnerID: record.ViewerID,
			Path: "/v1/images/edits", Method: http.MethodPost, Headers: headers, Body: body,
		}
	}
	response, err := r.deps.Dispatch.Dispatch(ctx, request)
	if err != nil {
		return nil, ImageJobUsage{}, dispatchError("生图派发失败", err)
	}
	if response == nil {
		return nil, ImageJobUsage{}, dispatchFailed("生图派发失败")
	}
	respBody, readErr := io.ReadAll(io.LimitReader(response.Body, imageGeneratedMaxBytes+1024))
	_ = response.Body.Close()
	if readErr != nil {
		return nil, ImageJobUsage{}, dispatchFailed("生图响应读取失败")
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, ImageJobUsage{}, dispatchFailed(upstreamDispatchMessage("生图", response.Status, respBody))
	}
	usage := parseImageUsage(respBody)
	decoded, parseErr := decodedImagesFromPayload(ctx, respBody, 1)
	if parseErr != nil {
		return nil, usage, dispatchFailed("生图结果归一失败: " + parseErr.Error())
	}
	return decoded, usage, nil
}

// buildEditsMultipart 构造 /v1/images/edits 表单：fields 在前（model/prompt/
// size?/response_format/quality?/output_format?），image parts 在后（每个参考
// 图一个 part，真实 MIME——中转上游按 part Content-Type 解析图，BUG-0232 同
// 口径）。参考图字节经暂存读取；行缺失（清理窗口竞态）按派发失败收敛。
func (r *ImageJobRunner) buildEditsMultipart(record ImageJobRecord) ([]byte, string, *HTTPError) {
	form := &bytes.Buffer{}
	writer := multipart.NewWriter(form)
	_ = writer.WriteField("model", record.ModelID)
	_ = writer.WriteField("prompt", record.Prompt)
	if record.Size != "" {
		_ = writer.WriteField("size", record.Size)
	}
	_ = writer.WriteField("response_format", "b64_json")
	if record.Quality != "" {
		_ = writer.WriteField("quality", imageJobQualityForUpstream(record.Quality))
	}
	if record.OutputFormat != "" {
		_ = writer.WriteField("output_format", record.OutputFormat)
	}
	for _, referenceID := range record.ReferenceIDs {
		artifact, openErr := r.deps.Artifacts.openArtifact(record.ViewerID, referenceID)
		if openErr != nil {
			return nil, "", dispatchFailed("参考图已不可用: " + referenceID)
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename="reference.%s"`, mimeExtension(artifact.MIME)))
		header.Set("Content-Type", artifact.MIME)
		part, partErr := writer.CreatePart(header)
		if partErr != nil {
			return nil, "", dispatchFailed("参考图 multipart 构造失败")
		}
		if _, partErr := part.Write(artifact.Data); partErr != nil {
			return nil, "", dispatchFailed("参考图 multipart 写入失败")
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", dispatchFailed("参考图 multipart 收尾失败")
	}
	return form.Bytes(), writer.FormDataContentType(), nil
}

// saveResultArtifact 把结果图落暂存（kind='image_result'，job_id 绑定任务，
// 恒 1 张；保留期 7d 由 jobs 清理任务按任务到期删字节）。
func (r *ImageJobRunner) saveResultArtifact(record ImageJobRecord, image decodedImage) (string, error) {
	if r.deps.Artifacts == nil || !r.deps.Artifacts.available() {
		return "", errArtifactStoreUnavailable
	}
	return r.deps.Artifacts.saveImageResult(record.ViewerID, record.ID, image)
}

// resolveCost 经计价端口按目录行本地计算扣费（§3.4.2/§3.7：演练场本地口径，
// 不落 usage_records；真实 Key 化修订：providerCode 直接取任务 params 投影，
// 不再经 AccountLookup 查账户）；端口缺失/无价/provider 缺失 → nil
// （cost_usd=null，卡片显示「-」，不虚构金额）。
func (r *ImageJobRunner) resolveCost(record ImageJobRecord, usage ImageJobUsage) *float64 {
	if r.cost == nil {
		return nil
	}
	value, ok := r.cost.ImageCost(record.ProviderCode, record.ModelID, record.Size, 1, usage.InputTokens, usage.OutputTokens)
	if !ok {
		return nil
	}
	return &value
}

// parseImageUsage 宽容解析上游响应 usage（§3.4.2：缺省 0，不因此判失败；
// total 缺省时按 input+output 收敛，openai images 形态顶层 usage 对象）。
func parseImageUsage(body []byte) ImageJobUsage {
	var payload struct {
		Usage *struct {
			InputTokens  *int64 `json:"input_tokens"`
			OutputTokens *int64 `json:"output_tokens"`
			TotalTokens  *int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Usage == nil {
		return ImageJobUsage{}
	}
	var usage ImageJobUsage
	if payload.Usage.InputTokens != nil {
		usage.InputTokens = *payload.Usage.InputTokens
	}
	if payload.Usage.OutputTokens != nil {
		usage.OutputTokens = *payload.Usage.OutputTokens
	}
	if payload.Usage.TotalTokens != nil {
		usage.TotalTokens = *payload.Usage.TotalTokens
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	return usage
}
