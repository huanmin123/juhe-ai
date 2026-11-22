package playground

// 图像任务执行器测试（设计 §3.4.2，v1.2）：Mock DispatchPort 回放上游响应 +
// fakeImageJobStore/fakeImageCost 注入，断言派发请求构造（generations JSON
// b64_json / edits multipart fields 前 image parts 后、purpose 头）、结果落
// artifacts(kind='image_result')、usage 宽容解析、CostPort 注入与无价收敛、
// 失败落 error_message（含上游原文摘要）、Submit 满载拒绝与启动恢复重入队。
// URL 回退下载经自定义 RoundTripper 回放，不触网。

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
)

// startImageJobRunner 装配 fixture 依赖 + fake store/cost 并启动执行器；返回
// deps 与执行器（测试结束 Stop）。
func startImageJobRunner(t *testing.T, store *fakeImageJobStore, cost ImageCostPort) (*Deps, *ImageJobRunner) {
	t.Helper()
	deps := playgroundFixtureDeps(t, false)
	addImageCandidate(deps)
	runner := NewImageJobRunner(deps, store, cost)
	t.Cleanup(runner.Stop)
	return deps, runner
}

// TestImageJobRunnerGenerationsSuccess 文生图成功路径：POST /v1/images/generations
// 请求体（model/prompt/size/response_format）与 purpose 头；结果落
// artifacts(kind='image_result')；usage 解析 + cost 注入 + Complete 形状。
func TestImageJobRunnerGenerationsSuccess(t *testing.T) {
	store := newFakeImageJobStore()
	cost := &fakeImageCost{price: 0.0631, ok: true}
	deps, runner := startImageJobRunner(t, store, cost)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"data":[{"b64_json":"` + base64Of(pngOnePixel) + `"}],"usage":{"input_tokens":12,"output_tokens":4096,"total_tokens":4108}}`),
	}}
	created, err := store.Create(&ImageJobRecord{
		ViewerID: "sys_user_1", AccountID: "acc_1", ModelID: "gpt-image-2",
		Prompt: "一只赛博猫", AspectRatio: "1:1", Size: "1024x1024",
		Quality: "standard", OutputFormat: "png",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Submit(created); err != nil {
		t.Fatal(err)
	}
	store.waitTerminal(t, 5*time.Second)

	if len(dispatch.calls) != 1 {
		t.Fatalf("派发次数 = %d", len(dispatch.calls))
	}
	call := dispatch.calls[0]
	if call.Request.Path != "/v1/images/generations" || call.Request.Method != "POST" {
		t.Fatalf("派发目标 = %s %s", call.Request.Method, call.Request.Path)
	}
	if call.Request.Headers["x-juhe-ai-purpose"] != "playground_images" {
		t.Fatalf("purpose 头 = %q", call.Request.Headers["x-juhe-ai-purpose"])
	}
	if call.Request.AccountID != "acc_1" || call.Request.OwnerID != "sys_user_1" {
		t.Fatalf("固定派发定位不符: %+v", call.Request)
	}
	var outbound struct {
		Model          string `json:"model"`
		Prompt         string `json:"prompt"`
		Size           string `json:"size"`
		Quality        string `json:"quality"`
		OutputFormat   string `json:"output_format"`
		ResponseFormat string `json:"response_format"`
	}
	if err := json.Unmarshal(call.Request.Body, &outbound); err != nil {
		t.Fatalf("请求体解析失败: %v", err)
	}
	// B6：quality/outputFormat 必须真实进入派发体（standard→上游 medium，
	// output_format 原值）。
	if outbound.Model != "gpt-image-2" || outbound.Prompt != "一只赛博猫" || outbound.Size != "1024x1024" || outbound.ResponseFormat != "b64_json" {
		t.Fatalf("出站体不符: %+v", outbound)
	}
	if outbound.Quality != "medium" || outbound.OutputFormat != "png" {
		t.Fatalf("quality/output_format 派发不符: %+v", outbound)
	}
	record, ok := store.snapshot(created.ID)
	if !ok || record.Status != imageJobStatusCompleted {
		t.Fatalf("终态不符: %+v", record)
	}
	if record.InputTokens != 12 || record.OutputTokens != 4096 || record.TotalTokens != 4108 {
		t.Fatalf("usage 解析不符: %+v", record)
	}
	if record.CostUSD == nil || *record.CostUSD != 0.0631 {
		t.Fatalf("cost 注入不符: %v", record.CostUSD)
	}
	if len(record.ResultIDs) != 1 {
		t.Fatalf("结果产物数 = %d", len(record.ResultIDs))
	}
	if cost.calls[len(cost.calls)-1] != "gpt\x00gpt-image-2\x001024x1024" {
		t.Fatalf("计价端口入参不符: %q", cost.calls[len(cost.calls)-1])
	}
	// 结果图落 artifacts（文件替身可读回字节）。
	content, openErr := deps.Artifacts.openArtifact("sys_user_1", record.ResultIDs[0])
	if openErr != nil || !bytes.Equal(content.Data, pngOnePixel) || content.MIME != "image/png" {
		t.Fatalf("结果图落暂存不符: %+v err=%v", content, openErr)
	}
	card, _ := store.Get("sys_user_1", created.ID)
	if card.ExpiresAt <= card.CompletedAt {
		t.Fatalf("expires_at 必须晚于完成时刻: %+v", card)
	}
}

// TestImageJobRunnerEditsMultipart 图生图：multipart POST /v1/images/edits，
// fields 在前（model/prompt/size/quality/output_format/response_format）、每
// 个参考图一个 image part 在后（真实 MIME + 文件名）；B6 参数进入派发体。
func TestImageJobRunnerEditsMultipart(t *testing.T) {
	store := newFakeImageJobStore()
	deps, runner := startImageJobRunner(t, store, nil)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"data":[{"b64_json":"` + base64Of(pngOnePixel) + `"}]}`),
	}}
	refA, refErr := deps.Artifacts.saveImageReference("sys_user_1", decodedImage{Data: pngOnePixel, MIME: "image/png"})
	if refErr != nil {
		t.Fatal(refErr)
	}
	refB, refErr := deps.Artifacts.saveImageReference("sys_user_1", decodedImage{Data: wavBytes, MIME: "image/jpeg"})
	if refErr != nil {
		t.Fatal(refErr)
	}
	created, err := store.Create(&ImageJobRecord{
		ViewerID: "sys_user_1", AccountID: "acc_1", ModelID: "gpt-image-2",
		Prompt: "改成水彩风", Size: "1024x1536", ReferenceIDs: []string{refA, refB},
		Quality: "fine", OutputFormat: "webp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Submit(created); err != nil {
		t.Fatal(err)
	}
	store.waitTerminal(t, 5*time.Second)

	call := dispatch.calls[0]
	if call.Request.Path != "/v1/images/edits" {
		t.Fatalf("派发目标 = %s", call.Request.Path)
	}
	mediaType, params, parseErr := mime.ParseMediaType(call.Request.Headers["content-type"])
	if parseErr != nil || mediaType != "multipart/form-data" {
		t.Fatalf("content-type = %q（err=%v）", call.Request.Headers["content-type"], parseErr)
	}
	reader := multipart.NewReader(bytes.NewReader(call.Request.Body), params["boundary"])
	var order []string
	values := map[string]string{}
	imagesSeen := 0
	for {
		part, partErr := reader.NextPart()
		if partErr != nil {
			break
		}
		data, _ := io.ReadAll(part)
		switch part.FormName() {
		case "model":
			order = append(order, "model")
			if string(data) != "gpt-image-2" {
				t.Fatalf("model 字段不符: %q", data)
			}
		case "prompt":
			order = append(order, "prompt")
		case "size":
			order = append(order, "size")
		case "response_format":
			order = append(order, "response_format")
			if string(data) != "b64_json" {
				t.Fatalf("response_format = %q", data)
			}
		case "quality", "output_format":
			order = append(order, part.FormName())
			values[part.FormName()] = string(data)
		case "image":
			order = append(order, "image")
			imagesSeen++
		}
	}
	// B6：quality 映射上游词表（fine→high）、output_format 原值透传。
	if values["quality"] != "high" || values["output_format"] != "webp" {
		t.Fatalf("quality/output_format 字段不符: %v", values)
	}
	// fields 在前、image parts 在后（网关计量扫描层顺序保证）。
	fieldsEnd := 0
	for fieldsEnd < len(order) && order[fieldsEnd] != "image" {
		fieldsEnd++
	}
	for index := fieldsEnd; index < len(order); index++ {
		if order[index] != "image" {
			t.Fatalf("image part 后不得再出 fields: %v", order)
		}
	}
	if imagesSeen != 2 {
		t.Fatalf("参考图 part 数 = %d，期望 2", imagesSeen)
	}
}

// TestImageJobRunnerURLFallbackDownload grok 只回临时 URL：经守卫客户端下载
// 字节并落 artifacts（本测试换装 RoundTripper 回放字节，不触网）。
func TestImageJobRunnerURLFallbackDownload(t *testing.T) {
	store := newFakeImageJobStore()
	deps, runner := startImageJobRunner(t, store, nil)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"data":[{"url":"https://93.184.216.34/img.png"}]}`),
	}}
	previousClient := imageUrlDownloadClient
	t.Cleanup(func() { imageUrlDownloadClient = previousClient })
	imageUrlDownloadClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "93.184.216.34" {
			return nil, errors.New("测试仅回放 93.184.216.34")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(bytes.NewReader(pngOnePixel)),
		}, nil
	})}
	created, err := store.Create(&ImageJobRecord{
		ViewerID: "sys_user_1", AccountID: "acc_1", ModelID: "grok-imagine-image",
		Prompt: "一只赛博猫", Size: "1024x1024",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Submit(created); err != nil {
		t.Fatal(err)
	}
	store.waitTerminal(t, 5*time.Second)
	record, _ := store.snapshot(created.ID)
	if record.Status != imageJobStatusCompleted || len(record.ResultIDs) != 1 {
		t.Fatalf("URL 回退终态不符: %+v", record)
	}
	content, openErr := deps.Artifacts.openArtifact("sys_user_1", record.ResultIDs[0])
	if openErr != nil || !bytes.Equal(content.Data, pngOnePixel) {
		t.Fatalf("回退下载字节不符: %+v err=%v", content, openErr)
	}
}

// TestImageJobRunnerUpstreamErrorFails 上游 4xx：任务 failed，error_message 存
// 上游状态码摘要与原文 message；无图无 expires_at。
func TestImageJobRunnerUpstreamErrorFails(t *testing.T) {
	store := newFakeImageJobStore()
	deps, runner := startImageJobRunner(t, store, nil)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Status: http.StatusTooManyRequests,
		Body:   []byte(`{"error":{"message":"rate limited by upstream"}}`),
	}}
	created, err := store.Create(&ImageJobRecord{
		ViewerID: "sys_user_1", AccountID: "acc_1", ModelID: "gpt-image-2", Prompt: "一只赛博猫",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Submit(created); err != nil {
		t.Fatal(err)
	}
	if got := store.waitTerminal(t, 5*time.Second); got != created.ID {
		t.Fatalf("终态任务 = %q", got)
	}
	record, _ := store.snapshot(created.ID)
	if record.Status != imageJobStatusFailed || record.ExpiresAt != 0 || len(record.ResultIDs) != 0 {
		t.Fatalf("失败终态不符: %+v", record)
	}
	if !strings.Contains(record.ErrorMessage, "上游 HTTP 429") || !strings.Contains(record.ErrorMessage, "rate limited by upstream") {
		t.Fatalf("error_message 缺上游原文摘要: %q", record.ErrorMessage)
	}
}

// TestImageJobRunnerCostMissingIsNull 计价端口无价/缺失：costUsd=null（卡片显
// 示「-」，不虚构金额）；usage 缺省全 0 不判失败。
func TestImageJobRunnerCostMissingIsNull(t *testing.T) {
	store := newFakeImageJobStore()
	deps, runner := startImageJobRunner(t, store, nil) // 无计价端口
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"data":[{"b64_json":"` + base64Of(pngOnePixel) + `"}]}`),
	}}
	created, err := store.Create(&ImageJobRecord{
		ViewerID: "sys_user_1", AccountID: "acc_1", ModelID: "gpt-image-2", Prompt: "一只赛博猫",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Submit(created); err != nil {
		t.Fatal(err)
	}
	store.waitTerminal(t, 5*time.Second)
	record, _ := store.snapshot(created.ID)
	if record.Status != imageJobStatusCompleted || record.CostUSD != nil {
		t.Fatalf("无价完成态不符: %+v", record)
	}
	if record.InputTokens != 0 || record.OutputTokens != 0 || record.TotalTokens != 0 {
		t.Fatalf("usage 缺省应为全 0: %+v", record)
	}
	card, _ := store.Get("sys_user_1", created.ID)
	if card.CostUSD != nil || card.Usage == nil {
		t.Fatalf("卡片 costUsd 应为 null: %+v", card)
	}
}

// TestImageJobRunnerSubmitBusy 队列满载显式拒绝（503 playground_busy 语义，
// 不无限积压）。
func TestImageJobRunnerSubmitBusy(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	store := newFakeImageJobStore()
	runner := &ImageJobRunner{
		deps:  deps,
		store: store,
		queue: make(chan imageJobTask, 1),
		stop:  make(chan struct{}),
	}
	// 占满容量 1 的队列。
	if err := runner.Submit(ImageJobRecord{ID: "fill_1"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Submit(ImageJobRecord{ID: "fill_2"}); !errors.Is(err, errImageJobQueueBusy) {
		t.Fatalf("满载 Submit 应拒绝，得到 %v", err)
	}
}

// TestImageJobRunnerRecoverAtBoot 启动恢复：queued 重入队继续执行；running 置
// failed("网关重启，任务中断")。
func TestImageJobRunnerRecoverAtBoot(t *testing.T) {
	store := newFakeImageJobStore()
	deps, runner := startImageJobRunner(t, store, nil)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"data":[{"b64_json":"` + base64Of(pngOnePixel) + `"}]}`),
	}}
	// 造一条 running 行（重启时 goroutine 已死）与一条 queued 行。
	store.mu.Lock()
	store.records["run_dead"] = &ImageJobRecord{
		ViewerID: "sys_user_1", ID: "run_dead", Status: imageJobStatusRunning,
		AccountID: "acc_1", ModelID: "gpt-image-2", Prompt: "中断图",
	}
	store.order = append(store.order, "run_dead")
	store.mu.Unlock()
	created, err := store.Create(&ImageJobRecord{
		ViewerID: "sys_user_1", AccountID: "acc_1", ModelID: "gpt-image-2", Prompt: "续跑图", Size: "1024x1024",
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := runner.RecoverAtBoot()
	if err != nil || len(queued) != 1 || queued[0].ID != created.ID {
		t.Fatalf("恢复返回 queued 清单不符: %+v err=%v", queued, err)
	}
	store.waitTerminal(t, 5*time.Second)
	// queued 行继续执行到完成。
	record, _ := store.snapshot(created.ID)
	if record.Status != imageJobStatusCompleted {
		t.Fatalf("恢复任务未继续执行: %+v", record)
	}
	// running 行置 failed。
	dead, _ := store.snapshot("run_dead")
	if dead.Status != imageJobStatusFailed || dead.ErrorMessage != "网关重启，任务中断" {
		t.Fatalf("running 恢复不符: %+v", dead)
	}
}
