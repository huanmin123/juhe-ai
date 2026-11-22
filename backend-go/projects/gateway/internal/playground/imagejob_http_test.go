package playground

// 图像工作台 HTTP 端点测试（设计 §3.4，v1.2）：真实 handler + fake
// store/runner 注入，断言参考图上传（解码/嗅探/上限）、任务创建校验面（候选
// 404/400、prompt、词表、referenceIds 归属与上限、size 映射）、列表 query 解
// 析与分页、详情 404 口径、卡片归一与 imageDeleted 读时判定、队列满 503。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// newImageJobTestRoutes 装配带图像任务面的测试路由（fixture + fake store +
// 真实 runner；测试结束 Stop）。
func newImageJobTestRoutes(t *testing.T) (*http.ServeMux, *Deps, *fakeImageJobStore, *fakeImageCost) {
	t.Helper()
	deps := playgroundFixtureDeps(t, false)
	addImageCandidate(deps)
	store := newFakeImageJobStore()
	cost := &fakeImageCost{price: 0.05, ok: true}
	deps.ImageJobs = store
	deps.ImageCost = cost
	deps.ImageRunner = NewImageJobRunner(deps, store, cost)
	t.Cleanup(deps.ImageRunner.Stop)
	mux, _ := newTestRoutes(deps)
	return mux, deps, store, cost
}

// TestImageReferencesUpload 参考图上传：合法 PNG → {id,url}（相对地址）；
// base64 非法 400；非图片字节 400；空载荷 400。
func TestImageReferencesUpload(t *testing.T) {
	mux, deps, _, _ := newImageJobTestRoutes(t)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/images/references", mustJSON(map[string]any{
		"dataBase64": base64Of(pngOnePixel),
	}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("上传状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if response.Data.ID == "" || response.Data.URL != "/playground/artifacts/"+response.Data.ID+"/content" {
		t.Fatalf("响应形状不符: %+v", response.Data)
	}
	// data: 前缀不是纯 base64：按 §3.4.1 自然解码失败 400。
	recorder = doJSON(mux, "POST", "/__aisys__/api/playground/images/references", mustJSON(map[string]any{
		"dataBase64": "data:image/png;base64," + base64Of(pngOnePixel),
	}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("data URL 前缀应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = doJSON(mux, "POST", "/__aisys__/api/playground/images/references", mustJSON(map[string]any{
		"dataBase64": "not_base64!!!",
	}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法 base64 应 400: %d", recorder.Code)
	}
	recorder = doJSON(mux, "POST", "/__aisys__/api/playground/images/references", mustJSON(map[string]any{
		"dataBase64": base64Of([]byte("plain text bytes")),
	}))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "PNG/JPEG/WebP") {
		t.Fatalf("非图片字节应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = doJSON(mux, "POST", "/__aisys__/api/playground/images/references", mustJSON(map[string]any{
		"dataBase64": "",
	}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("空载荷应 400: %d", recorder.Code)
	}
	if deps.ImageRunner == nil {
		t.Fatal("runner 应已装配")
	}
}

// TestImageJobCreateValidation 任务创建校验面：模型不在候选 400、账户越界
// 404、prompt 空/超限 400、词表外 400、referenceIds 越界 400、参考图归属失
// 败 400 playground_reference_not_found。
func TestImageJobCreateValidation(t *testing.T) {
	mux, _, _, _ := newImageJobTestRoutes(t)
	base := map[string]any{"accountId": "acc_1", "modelId": "gpt-image-2", "prompt": "一只赛博猫"}

	post := func(extra map[string]any) *httptest.ResponseRecorder {
		payload := map[string]any{}
		for key, value := range base {
			payload[key] = value
		}
		for key, value := range extra {
			payload[key] = value
		}
		return doJSON(mux, "POST", "/__aisys__/api/playground/images/jobs", mustJSON(payload))
	}

	if recorder := post(nil); recorder.Code != http.StatusOK {
		t.Fatalf("合法创建应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := post(map[string]any{"modelId": "nope-image"}); recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "playground_model_not_available") {
		t.Fatalf("模型不在候选应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := post(map[string]any{"accountId": "acc_other"}); recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "playground_account_not_found") {
		t.Fatalf("账户越界应 404: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := post(map[string]any{"prompt": "   "}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("空 prompt 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := post(map[string]any{"prompt": strings.Repeat("猫", imagesPromptMaxBytes)}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("超限 prompt 应 400: %d", recorder.Code)
	}
	if recorder := post(map[string]any{"aspectRatio": "4:3"}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("aspectRatio 词表外应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := post(map[string]any{"resolution": "8k"}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("resolution 词表外应 400: %d", recorder.Code)
	}
	if recorder := post(map[string]any{"quality": "ultra"}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("quality 词表外应 400: %d", recorder.Code)
	}
	if recorder := post(map[string]any{"outputFormat": "gif"}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("outputFormat 词表外应 400: %d", recorder.Code)
	}
	manyRefs := make([]string, imageJobReferencesMax+1)
	for index := range manyRefs {
		manyRefs[index] = "ref_x"
	}
	if recorder := post(map[string]any{"referenceIds": manyRefs}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("referenceIds 越界应 400: %d", recorder.Code)
	}
	if recorder := post(map[string]any{"referenceIds": []string{"ref_missing"}}); recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "playground_reference_not_found") {
		t.Fatalf("参考图归属失败应 400 playground_reference_not_found: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestImageJobCreateSizeMapping size 静态映射：1:1/16:9/9:16 → 1024 系三版
// 式；仅 resolution 给定 → 1k 方版；两者皆省略 → 不传 size（透传上游默认）。
func TestImageJobCreateSizeMapping(t *testing.T) {
	mux, _, store, _ := newImageJobTestRoutes(t)
	create := func(extra map[string]any) string {
		payload := map[string]any{"accountId": "acc_1", "modelId": "gpt-image-2", "prompt": "一只赛博猫"}
		for key, value := range extra {
			payload[key] = value
		}
		recorder := doJSON(mux, "POST", "/__aisys__/api/playground/images/jobs", mustJSON(payload))
		if recorder.Code != http.StatusOK {
			t.Fatalf("创建状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Data struct {
				JobID  string `json:"jobId"`
				Status string `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("响应解析失败: %v", err)
		}
		if response.Data.Status != imageJobStatusQueued || response.Data.JobID == "" {
			t.Fatalf("创建响应不符: %+v", response.Data)
		}
		return response.Data.JobID
	}

	cases := []struct {
		extra map[string]any
		want  string
	}{
		{map[string]any{"aspectRatio": "1:1"}, "1024x1024"},
		{map[string]any{"aspectRatio": "16:9"}, "1536x1024"},
		{map[string]any{"aspectRatio": "9:16"}, "1024x1536"},
		{map[string]any{"aspectRatio": "16:9", "resolution": "4k"}, "1536x1024"}, // 2k/4k 无对应档取最近 1k 系
		{map[string]any{"resolution": "2k"}, "1024x1024"},
		{nil, ""},
	}
	for _, testCase := range cases {
		jobID := create(testCase.extra)
		record, ok := store.snapshot(jobID)
		if !ok || record.Size != testCase.want {
			t.Fatalf("size 映射不符: %v → %q（期望 %q）", testCase.extra, record.Size, testCase.want)
		}
	}
	// 两者皆省略：执行器出站体不携带 size（独立夹具隔离——此前用例的任务失
	// 败派发与本用例共享 mock，无法按序定位）。
	soleDeps := func() *Deps {
		deps := playgroundFixtureDeps(t, false)
		addImageCandidate(deps)
		soleStore := newFakeImageJobStore()
		runner := NewImageJobRunner(deps, soleStore, nil)
		t.Cleanup(runner.Stop)
		deps.ImageJobs = soleStore
		deps.ImageRunner = runner
		mux, _ := newTestRoutes(deps)
		dispatch := deps.Dispatch.(*mockDispatch)
		dispatch.script = []scriptedResponse{{
			Body: []byte(`{"data":[{"b64_json":"` + base64Of(pngOnePixel) + `"}]}`),
		}}
		recorder := doJSON(mux, "POST", "/__aisys__/api/playground/images/jobs", mustJSON(map[string]any{
			"accountId": "acc_1", "modelId": "gpt-image-2", "prompt": "一只赛博猫",
		}))
		if recorder.Code != http.StatusOK {
			t.Fatalf("缺省用例创建状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
		}
		soleStore.waitTerminal(t, 5*time.Second)
		return deps
	}()
	if len(soleDeps.Dispatch.(*mockDispatch).calls) != 1 {
		t.Fatalf("缺省用例派发次数 = %d", len(soleDeps.Dispatch.(*mockDispatch).calls))
	}
	var outbound map[string]any
	if err := json.Unmarshal(soleDeps.Dispatch.(*mockDispatch).calls[0].Request.Body, &outbound); err != nil {
		t.Fatal(err)
	}
	if _, hit := outbound["size"]; hit {
		t.Fatalf("全省略时不得发送 size: %v", outbound)
	}
	// 出站体其余形状（model/prompt/response_format）。
	if outbound["model"] != "gpt-image-2" || outbound["response_format"] != "b64_json" {
		t.Fatalf("缺省出站体不符: %v", outbound)
	}
}

// TestImageJobsListAndDetail 列表 query 解析与详情：status 词表（含 active/
// expired 派生态）、limit 边界、{jobs,nextCursor} 形状；详情不存在/越权 404
// playground_job_not_found。
func TestImageJobsListAndDetail(t *testing.T) {
	mux, _, store, _ := newImageJobTestRoutes(t)
	store.mu.Lock()
	now := time.Now().UnixMilli()
	fixtures := []ImageJobRecord{
		{ViewerID: "sys_user_1", ID: "job_q", Status: imageJobStatusQueued, AccountID: "acc_1", ModelID: "gpt-image-2", Prompt: "赛博猫", CreatedAt: now},
		{ViewerID: "sys_user_1", ID: "job_r", Status: imageJobStatusRunning, AccountID: "acc_1", ModelID: "gpt-image-2", Prompt: "水彩猫", Quality: "fine", CreatedAt: now - 1000},
		{ViewerID: "sys_user_1", ID: "job_x", Status: imageJobStatusCompleted, AccountID: "acc_1", ModelID: "gpt-image-2", Prompt: "过期猫", ExpiresAt: now - 2000, CompletedAt: now - 8000, CreatedAt: now - 9000},
	}
	for _, record := range fixtures {
		value := record
		store.records[value.ID] = &value
		store.order = append(store.order, value.ID)
	}
	// 他户任务不可见。
	store.records["job_other"] = &ImageJobRecord{ViewerID: "sys_user_2", ID: "job_other", Status: imageJobStatusQueued, CreatedAt: now}
	store.order = append(store.order, "job_other")
	store.mu.Unlock()

	recorder := doJSON(mux, "GET", "/__aisys__/api/playground/images/jobs", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("列表状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var page struct {
		Data struct {
			Jobs []struct {
				JobID string `json:"jobId"`
			} `json:"jobs"`
			NextCursor int64 `json:"nextCursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatalf("列表解析失败: %v", err)
	}
	if len(page.Data.Jobs) != 3 {
		t.Fatalf("列表应只见本户 3 条: %d", len(page.Data.Jobs))
	}
	// active 派生态（queued+running）。
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/images/jobs?status=active", nil)
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data.Jobs) != 2 || page.Data.Jobs[0].JobID != "job_q" || page.Data.Jobs[1].JobID != "job_r" {
		t.Fatalf("active 过滤不符: %+v", page.Data.Jobs)
	}
	// expired 派生态。
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/images/jobs?status=expired", nil)
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data.Jobs) != 1 || page.Data.Jobs[0].JobID != "job_x" {
		t.Fatalf("expired 过滤不符: %+v", page.Data.Jobs)
	}
	// 词表外 status 400。
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/images/jobs?status=nope", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status 词表外应 400: %d", recorder.Code)
	}
	// limit 解析：非数字与 ≤0 一律 400（不再静默回落默认值）。
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/images/jobs?limit=abc", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("limit 非数字应 400: %d", recorder.Code)
	}
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/images/jobs?limit=0", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("limit=0 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/images/jobs?limit=-5", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("limit=-5 应 400: %d %s", recorder.Code, recorder.Body.String())
	}

	// 详情：本户 200；他户/不存在 404 playground_job_not_found。
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/images/jobs/job_q", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("详情状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/images/jobs/job_other", nil)
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "playground_job_not_found") {
		t.Fatalf("越权详情应 404: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/images/jobs/no_such", nil)
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "playground_job_not_found") {
		t.Fatalf("不存在详情应 404: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestImageJobCardNormalization 卡片归一（fake store 数据驱动）：completed 未
// 删给 images/references 的 {id,url}、expiresAt 有值；usage/costUsd completed
// 含已删都有值（M1：历史记录价值，数据库有值就透出）；时间戳 unix 秒；
// imageDeleted 读时判定（expires_at 已过 or images_expired）。
func TestImageJobCardNormalization(t *testing.T) {
	mux, _, store, _ := newImageJobTestRoutes(t)
	nowMilli := time.Now().UnixMilli()
	cost := 0.07
	expiredIDs := []string{"res_gone"}
	store.mu.Lock()
	store.records["job_live"] = &ImageJobRecord{
		ViewerID: "sys_user_1", ID: "job_live", Status: imageJobStatusCompleted,
		AccountID: "acc_1", ModelID: "gpt-image-2", Prompt: "成品图", Size: "1024x1024",
		Quality: "standard", OutputFormat: "png",
		ReferenceIDs: []string{"ref_1", "ref_2"}, ResultIDs: expiredIDs,
		InputTokens: 12, OutputTokens: 4096, TotalTokens: 4108, CostUSD: &cost,
		ExpiresAt:   nowMilli + int64(imageJobRetention/time.Millisecond),
		CompletedAt: nowMilli, CreatedAt: nowMilli - 3000, UpdatedAt: nowMilli,
	}
	store.records["job_gone"] = &ImageJobRecord{
		ViewerID: "sys_user_1", ID: "job_gone", Status: imageJobStatusCompleted,
		AccountID: "acc_1", ModelID: "gpt-image-2", Prompt: "已删图",
		ResultIDs: expiredIDs, InputTokens: 5, OutputTokens: 100, TotalTokens: 105, CostUSD: &cost,
		ExpiresAt: nowMilli - 1000, CompletedAt: nowMilli - 8000, CreatedAt: nowMilli - 9000,
	}
	store.records["job_flagged"] = &ImageJobRecord{
		ViewerID: "sys_user_1", ID: "job_flagged", Status: imageJobStatusCompleted,
		AccountID: "acc_1", ModelID: "gpt-image-2", Prompt: "标记删除图",
		ResultIDs: expiredIDs, ImagesExpired: true, InputTokens: 1, OutputTokens: 2, TotalTokens: 3,
		CreatedAt: nowMilli - 500,
	}
	store.order = []string{"job_live", "job_gone", "job_flagged"}
	store.mu.Unlock()

	cards := map[string]struct {
		deleted     bool
		imagesCount int
		refsCount   int
		hasUsage    bool
		hasCost     bool
		expiresAt   int64
		usageTotal  int64
	}{}
	for _, id := range []string{"job_live", "job_gone", "job_flagged"} {
		recorder := doJSON(mux, "GET", "/__aisys__/api/playground/images/jobs/"+id, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("详情 %s 状态码 = %d", id, recorder.Code)
		}
		var response struct {
			Data ImageJobCard `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("详情解析失败: %v", err)
		}
		card := response.Data
		usageTotal := int64(-1)
		if card.Usage != nil {
			usageTotal = card.Usage.TotalTokens
		}
		cards[id] = struct {
			deleted     bool
			imagesCount int
			refsCount   int
			hasUsage    bool
			hasCost     bool
			expiresAt   int64
			usageTotal  int64
		}{card.ImageDeleted, len(card.Images), len(card.References), card.Usage != nil, card.CostUSD != nil, card.ExpiresAt, usageTotal}
		if card.CreatedAt <= 1_000_000_000 || (card.ExpiresAt != 0 && card.ExpiresAt <= 1_000_000_000) {
			t.Fatalf("时间戳应为 unix 秒: %+v", card)
		}
	}
	live := cards["job_live"]
	if live.deleted || live.imagesCount != 1 || live.refsCount != 2 || !live.hasUsage || !live.hasCost || live.expiresAt == 0 {
		t.Fatalf("completed 未删卡片不符: %+v", live)
	}
	// 已删 completed：images/references 收敛空、expiresAt 收敛 0，usage/costUsd
	// 仍有值（历史记录价值）。
	gone := cards["job_gone"]
	if !gone.deleted || gone.imagesCount != 0 || gone.refsCount != 0 || gone.expiresAt != 0 {
		t.Fatalf("过期读时判定不符: %+v", gone)
	}
	if !gone.hasUsage || gone.usageTotal != 105 || !gone.hasCost {
		t.Fatalf("已删卡片应保留 usage/costUsd: %+v", gone)
	}
	flagged := cards["job_flagged"]
	if !flagged.deleted || flagged.imagesCount != 0 || !flagged.hasUsage || flagged.usageTotal != 3 {
		t.Fatalf("images_expired 判定不符: %+v", flagged)
	}
}

// TestImageJobsCreateBusy 队列满载：POST /images/jobs 返回 503
// playground_busy（显式拒绝，不无限积压），且已落库的任务行同步收敛为
// failed（B3：不留 queued 僵尸行，错误文案随行存储）。
func TestImageJobsCreateBusy(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	addImageCandidate(deps)
	store := newFakeImageJobStore()
	deps.ImageJobs = store
	// 挂一个已满载的执行器（不经 Register 装配）。
	runner := &ImageJobRunner{deps: deps, store: store, queue: make(chan imageJobTask, 1), stop: make(chan struct{})}
	_ = runner.Submit(ImageJobRecord{ID: "fill"})
	deps.ImageRunner = runner
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/images/jobs", mustJSON(map[string]any{
		"accountId": "acc_1", "modelId": "gpt-image-2", "prompt": "一只赛博猫",
	}))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "playground_busy") {
		t.Fatalf("队列满应 503 playground_busy: %d %s", recorder.Code, recorder.Body.String())
	}
	// 满 1 条占位 + 1 条被拒任务。
	store.mu.Lock()
	lastID := store.order[len(store.order)-1]
	store.mu.Unlock()
	record, ok := store.snapshot(lastID)
	if !ok {
		t.Fatalf("被拒任务行缺失: %v", store.order)
	}
	if record.Status != imageJobStatusFailed {
		t.Fatalf("队列满被拒后任务行应收敛 failed（不留 queued 僵尸行）: %+v", record)
	}
	if record.ErrorMessage != "排队已满，请稍后重试" {
		t.Fatalf("失败文案不符: %q", record.ErrorMessage)
	}
}

// TestImageReferencesUploadThroughKernelBodyLimit B1 装配级验证：真实 kernel
// （默认 body limit 256KiB）包裹 playground 路由，参考图上传端点经
// Deps.Register 登记 20MB 路由级覆盖——
//  1. 12MB+ 请求体（9MB 原图 base64 ≈ 12.58MB）穿过 kernel 默认上限成功；
//  2. 超覆盖值的 ~23MB 请求体被 kernel 上限拒绝（400，readJSONBody 口径）；
//  3. 覆盖内但原图 >10MB 的请求仍被领域校验拒绝（400 超过 10 MiB 上限）。
func TestImageReferencesUploadThroughKernelBodyLimit(t *testing.T) {
	gateway := kernel.New(kernel.Options{})
	deps := playgroundFixtureDeps(t, false)
	deps.RequireSession = func(h http.Handler) http.Handler { return h }
	deps.ScopeResolver = func(*http.Request) (Scope, error) { return Scope{ViewerID: "sys_user_1"}, nil }
	deps.Register(gateway, "/__aisys__/api/playground")
	handler := gateway.Handler()
	postReference := func(decodedBytes int) *httptest.ResponseRecorder {
		image := make([]byte, decodedBytes)
		copy(image, pngOnePixel)
		request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/playground/images/references",
			strings.NewReader(`{"dataBase64":"`+base64Of(image)+`"}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	recorder := postReference(9 * 1024 * 1024)
	if recorder.Code != http.StatusOK {
		t.Fatalf("12MB+ 参考图上传应穿过 kernel 覆盖上限成功: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = postReference(16 * 1024 * 1024)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "请求体读取失败或超过上限") {
		t.Fatalf("超覆盖值请求应被拒绝: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = postReference(10*1024*1024 + 1)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "10 MiB") {
		t.Fatalf("覆盖内超领域 10MB 上限仍应拒绝: %d %s", recorder.Code, recorder.Body.String())
	}
}
