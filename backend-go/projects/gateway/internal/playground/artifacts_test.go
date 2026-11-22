package playground

// 产物暂存测试（设计 §3.5/§3.6/§3.8）：媒体端点经 Mock 派发回放上游响应后走
// 真实磁盘落盘（t.TempDir），断言语音覆盖替换、视频任务引用单条覆盖、GET
// /artifacts 清单形状（v1.2 起无 images 字段——图片历史走 GET /images/jobs）、
// content 归属（他户 id 404）与非法 id 400、响应形状（data 信封）。图像产物
// （参考图/结果图）写入与任务链路见 imagejob_*_test.go。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// artifactTestRoot 返回测试替身的文件系统根目录。
func artifactTestRoot(t *testing.T, deps *Deps) string {
	t.Helper()
	store, ok := deps.Artifacts.(*fsArtifactStore)
	if !ok || store.root == "" {
		t.Fatalf("测试暂存后端不是文件系统替身: %T", deps.Artifacts)
	}
	return store.root
}

// artifactUserDirFiles 列出指定用户暂存目录内的文件名（目录不存在视为空）。
func artifactUserDirFiles(t *testing.T, root, viewer string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, viewer))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("暂存目录读取失败: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

// addImageCandidate 给 fixture 补一个可路由的生图候选。
func addImageCandidate(deps *Deps) {
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"], CatalogItem{Model: "gpt-image-2", SupportedAPIProtocols: []string{"image_generation"}})
	transportView := deps.TransportAccounts.(*staticTransport)
	// 五域退役修订：图像候选按域路由（image 域账户才列生图模型）。
	transportView.views["acc_1"][0].CapabilityDomain = "image"
	transportView.views["acc_1"][0].SupportedEndpointModes = append(transportView.views["acc_1"][0].SupportedEndpointModes, "images_json")
}

// TestSpeechArtifactReplacedOnRegenerate 语音产物落盘 + 覆盖替换：二次合成后
// 旧音频消失、单条留存。
func TestSpeechArtifactReplacedOnRegenerate(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"], CatalogItem{Model: "speech-2.8-hd", SupportedAPIProtocols: []string{"audio_speech"}})
	transportView := deps.TransportAccounts.(*staticTransport)
	// 五域退役修订：语音候选按域路由。
	transportView.views["acc_1"][0].CapabilityDomain = "audio"
	transportView.views["acc_1"][0].SupportedEndpointModes = []string{"chat_sse", "audio_speech"}
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{
		{Header: http.Header{"Content-Type": []string{"audio/wav"}}, Body: wavBytes},
		{Header: http.Header{"Content-Type": []string{"audio/wav"}}, Body: wavBytes},
	}
	mux, _ := newTestRoutes(deps)
	payload, _ := json.Marshal(map[string]any{
		"accountId": "acc_1", "modelId": "speech-2.8-hd", "input": "演练场语音合成", "format": "wav",
	})
	synthesize := func() string {
		recorder := doJSON(mux, "POST", "/__aisys__/api/playground/speech", payload)
		if recorder.Code != http.StatusOK {
			t.Fatalf("语音状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Data struct {
				ID       string `json:"id"`
				URL      string `json:"url"`
				MimeType string `json:"mimeType"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("语音响应解析失败: %v", err)
		}
		if response.Data.MimeType != "audio/wav" || response.Data.URL != "/playground/artifacts/"+response.Data.ID+"/content" {
			t.Fatalf("语音响应形状不符: %+v", response.Data)
		}
		return response.Data.ID
	}
	firstID := synthesize()
	secondID := synthesize()
	files := artifactUserDirFiles(t, artifactTestRoot(t, deps), "sys_user_1")
	if len(files) != 1 || files[0] != secondID+".wav" {
		t.Fatalf("语音覆盖替换后文件不符: %v（期望仅 %s.wav）", files, secondID)
	}
	if firstID == secondID {
		t.Fatalf("两次合成产物 id 不应相同")
	}
}

// TestVideosTaskRefSingleEntry 视频任务引用落盘 + 单条覆盖：两次创建后只剩一
// 条 JSON，字段为最新 jobId/accountId/modelId/status/createdAt；创建响应形状
// 不变（{jobId}）。
func TestVideosTaskRefSingleEntry(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"], CatalogItem{Model: "grok-imagine-video-1.5", SupportedAPIProtocols: []string{"video"}})
	transportView := deps.TransportAccounts.(*staticTransport)
	// 五域退役修订：视频候选按域路由。
	transportView.views["acc_1"][0].CapabilityDomain = "video"
	transportView.views["acc_1"][0].SupportedEndpointModes = []string{"chat_sse", "video_create"}
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{
		{Body: []byte(`{"task_id":"job_v1","status":"queued"}`)},
		{Body: []byte(`{"task_id":"job_v2"}`)},
	}
	mux, _ := newTestRoutes(deps)
	payload, _ := json.Marshal(map[string]any{
		"accountId": "acc_1", "modelId": "grok-imagine-video-1.5", "prompt": "一只猫在跳舞",
	})
	for _, wantJobID := range []string{"job_v1", "job_v2"} {
		recorder := doJSON(mux, "POST", "/__aisys__/api/playground/videos", payload)
		if recorder.Code != http.StatusOK {
			t.Fatalf("创建状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
		}
		var created struct {
			Data struct {
				JobID string `json:"jobId"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil || created.Data.JobID != wantJobID {
			t.Fatalf("创建响应 = %s（期望 jobId %s）", recorder.Body.String(), wantJobID)
		}
	}
	files := artifactUserDirFiles(t, artifactTestRoot(t, deps), "sys_user_1")
	if len(files) != 1 || !strings.HasSuffix(files[0], ".json") {
		t.Fatalf("任务引用文件数不符: %v（期望单条 .json）", files)
	}
	var ref struct {
		JobID     string `json:"jobId"`
		AccountID string `json:"accountId"`
		ModelID   string `json:"modelId"`
		Status    string `json:"status"`
		CreatedAt int64  `json:"createdAt"`
	}
	data, err := os.ReadFile(filepath.Join(artifactTestRoot(t, deps), "sys_user_1", files[0]))
	if err != nil {
		t.Fatalf("任务引用读取失败: %v", err)
	}
	if err := json.Unmarshal(data, &ref); err != nil {
		t.Fatalf("任务引用解析失败: %v", err)
	}
	if ref.JobID != "job_v2" || ref.AccountID != "acc_1" || ref.ModelID != "grok-imagine-video-1.5" || ref.Status != "queued" || ref.CreatedAt <= 0 {
		t.Fatalf("任务引用字段不符: %+v", ref)
	}
}

// TestArtifactsListingShape 恢复端点清单形状（v1.2）：speech/video 齐备时逐
// 字段断言；空暂存时 speech/video 为 null；响应不再有 images 字段（图片历史
// 改走 GET /images/jobs，§3.8）。
func TestArtifactsListingShape(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"],
		CatalogItem{Model: "speech-2.8-hd", SupportedAPIProtocols: []string{"audio_speech"}},
		CatalogItem{Model: "grok-imagine-video-1.5", SupportedAPIProtocols: []string{"video"}},
	)
	transportView := deps.TransportAccounts.(*staticTransport)
	dispatch := deps.Dispatch.(*mockDispatch)
	mux, _ := newTestRoutes(deps)
	// generateImages 退役后单账户逐段服务多域产物，每步前翻能力域（五域退役
	// 修订：候选按域路由）。
	transportView.views["acc_1"][0].CapabilityDomain = "audio"
	dispatch.script = []scriptedResponse{{Header: http.Header{"Content-Type": []string{"audio/wav"}}, Body: wavBytes}}
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/speech", mustJSON(map[string]any{
		"accountId": "acc_1", "modelId": "speech-2.8-hd", "input": "演练场语音", "format": "wav",
	}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("语音状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	transportView.views["acc_1"][0].CapabilityDomain = "video"
	dispatch.script = []scriptedResponse{{Body: []byte(`{"task_id":"job_v1","status":"queued"}`)}}
	recorder = doJSON(mux, "POST", "/__aisys__/api/playground/videos", mustJSON(map[string]any{
		"accountId": "acc_1", "modelId": "grok-imagine-video-1.5", "prompt": "一只猫在跳舞",
	}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("视频创建状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}

	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/artifacts", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("清单状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var listing struct {
		Data struct {
			Speech *struct {
				ID        string `json:"id"`
				URL       string `json:"url"`
				MimeType  string `json:"mimeType"`
				CreatedAt int64  `json:"createdAt"`
			} `json:"speech"`
			Video *struct {
				JobID     string `json:"jobId"`
				AccountID string `json:"accountId"`
				ModelID   string `json:"modelId"`
				Status    string `json:"status"`
				CreatedAt int64  `json:"createdAt"`
			} `json:"video"`
			Images []struct {
				ID string `json:"id"`
			} `json:"images"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &listing); err != nil {
		t.Fatalf("清单解析失败: %v", err)
	}
	if listing.Data.Speech == nil || listing.Data.Speech.MimeType != "audio/wav" || listing.Data.Speech.ID == "" || listing.Data.Speech.CreatedAt <= 0 {
		t.Fatalf("speech 清单不符: %+v", listing.Data.Speech)
	}
	if listing.Data.Video == nil || listing.Data.Video.JobID != "job_v1" || listing.Data.Video.AccountID != "acc_1" ||
		listing.Data.Video.ModelID != "grok-imagine-video-1.5" || listing.Data.Video.Status != "queued" || listing.Data.Video.CreatedAt <= 0 {
		t.Fatalf("video 清单不符: %+v", listing.Data.Video)
	}
	if strings.Contains(recorder.Body.String(), `"images":`) {
		t.Fatalf("v1.2 清单不得再有 images 字段: %s", recorder.Body.String())
	}

	// 空暂存：speech/video null。
	emptyDeps := playgroundFixtureDeps(t, false)
	emptyMux, _ := newTestRoutes(emptyDeps)
	recorder = doJSON(emptyMux, "GET", "/__aisys__/api/playground/artifacts", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("空清单状态码 = %d", recorder.Code)
	}
	emptyBody := recorder.Body.String()
	if !strings.Contains(emptyBody, `"speech":null`) || !strings.Contains(emptyBody, `"video":null`) || strings.Contains(emptyBody, `"images":`) {
		t.Fatalf("空清单形状不符: %s", emptyBody)
	}
}

// TestArtifactContentOwnershipAndInvalidID content 归属与校验面：参考图上传
// 后本户可取、他户 404、非法 id 400、任务引用 JSON 不经此端点（404）。
func TestArtifactContentOwnershipAndInvalidID(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	catalog := deps.ProviderCatalog.(*staticCatalog)
	catalog.byProvider["gpt"] = append(catalog.byProvider["gpt"],
		CatalogItem{Model: "speech-2.8-hd", SupportedAPIProtocols: []string{"audio_speech"}},
		CatalogItem{Model: "grok-imagine-video-1.5", SupportedAPIProtocols: []string{"video"}},
	)
	transportView := deps.TransportAccounts.(*staticTransport)
	transportView.views["acc_1"][0].SupportedEndpointModes = []string{"chat_sse", "video_create", "audio_speech"}
	dispatch := deps.Dispatch.(*mockDispatch)
	viewer := "sys_user_1"
	deps.RequireSession = func(h http.Handler) http.Handler { return h }
	deps.ScopeResolver = func(*http.Request) (Scope, error) {
		return Scope{ViewerID: viewer, IsAdmin: false}, nil
	}
	mux := http.NewServeMux()
	deps.Register(muxRegisterer{mux}, "/__aisys__/api/playground")

	// 参考图上传（POST /images/references，§3.4.1）。
	transportView.views["acc_1"][0].CapabilityDomain = "image"
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/images/references", mustJSON(map[string]any{
		"dataBase64": base64Of(pngOnePixel),
	}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("参考图上传状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var reference struct {
		Data struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &reference); err != nil {
		t.Fatalf("参考图响应解析失败: %v", err)
	}
	if reference.Data.ID == "" || reference.Data.URL != "/playground/artifacts/"+reference.Data.ID+"/content" {
		t.Fatalf("参考图响应形状不符: %+v", reference.Data)
	}
	// 语音产物。
	transportView.views["acc_1"][0].CapabilityDomain = "audio"
	dispatch.script = []scriptedResponse{{Header: http.Header{"Content-Type": []string{"audio/wav"}}, Body: wavBytes}}
	recorder = doJSON(mux, "POST", "/__aisys__/api/playground/speech", mustJSON(map[string]any{
		"accountId": "acc_1", "modelId": "speech-2.8-hd", "input": "演练场语音", "format": "wav",
	}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("语音状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var speechOut struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &speechOut); err != nil {
		t.Fatalf("语音响应解析失败: %v", err)
	}
	// 视频任务引用。
	transportView.views["acc_1"][0].CapabilityDomain = "video"
	dispatch.script = []scriptedResponse{{Body: []byte(`{"task_id":"job_v1","status":"queued"}`)}}
	recorder = doJSON(mux, "POST", "/__aisys__/api/playground/videos", mustJSON(map[string]any{
		"accountId": "acc_1", "modelId": "grok-imagine-video-1.5", "prompt": "一只猫在跳舞",
	}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("视频创建状态码 = %d", recorder.Code)
	}

	// 本户取参考图：200 + 字节一致 + Content-Type + inline。
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/artifacts/"+reference.Data.ID+"/content", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("本户取参考图状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("参考图 Content-Type = %q，期望 image/png", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); got != "inline" {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if recorder.Body.String() != string(pngOnePixel) {
		t.Fatalf("参考图字节不符")
	}
	// 本户取语音：audio/wav。
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/artifacts/"+speechOut.Data.ID+"/content", nil)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "audio/wav" || recorder.Body.String() != string(wavBytes) {
		t.Fatalf("本户取语音不符: %d %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}

	// 他户 id：404 playground_artifact_not_found（跨用户不可达）。
	viewer = "sys_user_2"
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/artifacts/"+reference.Data.ID+"/content", nil)
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "playground_artifact_not_found") {
		t.Fatalf("他户 id 应 404 playground_artifact_not_found: %d %s", recorder.Code, recorder.Body.String())
	}

	// 非法 id：400 playground_invalid_request（白名单外字符；"."/".." 形态会
	// 先被 ServeMux 路径清洗重定向、根本到不了 handler，白名单防线由
	// TestSanitizeArtifactID 单测覆盖）。
	viewer = "sys_user_1"
	for _, badID := range []string{"job!x", "abc$def", "猫"} {
		recorder = doJSON(mux, "GET", "/__aisys__/api/playground/artifacts/"+badID+"/content", nil)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "playground_invalid_request") {
			t.Fatalf("非法 id %q 应 400 invalid_request: %d %s", badID, recorder.Code, recorder.Body.String())
		}
	}

	// 视频任务引用 JSON 不经 content 端点（404；视频内容走 §3.5 jobId 代理）。
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/artifacts/1730000000000000009-nothinghere/content", nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在 id 应 404: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestCleanupExpiredRemovesStaleAndEmptyDirs 24h 懒清理：过期文件（注入
// mtime -25h）删除、新鲜文件保留、清空后的用户目录删除（文件替身把图像文件
// 一并按孤儿口径清理，job 绑定语义由 PG 测试覆盖）。
func TestCleanupExpiredRemovesStaleAndEmptyDirs(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	mux, _ := newTestRoutes(deps)
	staleTime := time.Now().Add(-25 * time.Hour)
	stalePath := filepath.Join(artifactTestRoot(t, deps), "sys_user_1", "1700000000000000000-deadbeefdeadbeef.png")
	if err := os.MkdirAll(filepath.Join(artifactTestRoot(t, deps), "sys_user_1"), 0o700); err != nil {
		t.Fatalf("暂存目录构造失败: %v", err)
	}
	if err := os.WriteFile(stalePath, pngOnePixel, 0o600); err != nil {
		t.Fatalf("过期文件构造失败: %v", err)
	}
	if err := os.Chtimes(stalePath, staleTime, staleTime); err != nil {
		t.Fatalf("过期 mtime 注入失败: %v", err)
	}
	freshPath := filepath.Join(artifactTestRoot(t, deps), "sys_user_1", "1700000000000000001-cafebabecafebabe.wav")
	if err := os.WriteFile(freshPath, wavBytes, 0o600); err != nil {
		t.Fatalf("新鲜文件构造失败: %v", err)
	}
	// 另一用户目录仅存过期文件：清理后连目录一起删除。
	otherDir := filepath.Join(artifactTestRoot(t, deps), "sys_user_2")
	otherPath := filepath.Join(otherDir, "1700000000000000002-0123456789abcdef.mp3")
	if err := os.MkdirAll(otherDir, 0o700); err != nil {
		t.Fatalf("他户目录构造失败: %v", err)
	}
	if err := os.WriteFile(otherPath, wavBytes, 0o600); err != nil {
		t.Fatalf("他户过期文件构造失败: %v", err)
	}
	if err := os.Chtimes(otherPath, staleTime, staleTime); err != nil {
		t.Fatalf("他户过期 mtime 注入失败: %v", err)
	}

	recorder := doJSON(mux, "GET", "/__aisys__/api/playground/artifacts", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("清单状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("过期文件未被清理（err %v）", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("新鲜文件被误删（err %v）", err)
	}
	if _, err := os.Stat(otherDir); !os.IsNotExist(err) {
		t.Fatalf("清空后的用户目录未被删除（err %v）", err)
	}
	var listing struct {
		Data struct {
			Speech *struct {
				ID string `json:"id"`
			} `json:"speech"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &listing); err != nil {
		t.Fatalf("清单解析失败: %v", err)
	}
	if listing.Data.Speech == nil || listing.Data.Speech.ID != "1700000000000000001-cafebabecafebabe" {
		t.Fatalf("清理后清单不符: %+v", listing.Data.Speech)
	}
}

// TestSanitizeArtifactID 白名单单测：合法形状放行，穿越/空白/Unicode/超长拒绝。
func TestSanitizeArtifactID(t *testing.T) {
	cases := []struct {
		id    string
		valid bool
	}{
		{"1730000000000000000-aabbccddeeff0102", true},
		{"abc.def_ghi", true},
		{"A9-_z", true},
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{"a\\b", false},
		{"a b", false},
		{"产物", false},
		{"a%2Fb", false},
		{strings.Repeat("a", 129), false},
		{strings.Repeat("a", 128), true},
	}
	for _, testCase := range cases {
		if got := sanitizeArtifactID(testCase.id); got != testCase.valid {
			t.Fatalf("sanitizeArtifactID(%q) = %v，期望 %v", testCase.id, got, testCase.valid)
		}
	}
}

// TestArtifactsEndpointsDegradedWithoutRoot 暂存/任务面未接线：产物端点与图
// 像任务端点显式 503 降级，不进入派发路径（功能不设开关，非静默空行为）。
func TestArtifactsEndpointsDegradedWithoutRoot(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	deps.Artifacts = nil
	deps.ImageJobs = nil
	mux, _ := newTestRoutes(deps)

	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/images/references", mustJSON(map[string]any{
		"dataBase64": base64Of(pngOnePixel),
	}))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "playground_endpoint_unavailable") {
		t.Fatalf("参考图上传应 503 降级: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = doJSON(mux, "POST", "/__aisys__/api/playground/images/jobs", mustJSON(map[string]any{
		"accountId": "acc_1", "modelId": "gpt-image-2", "prompt": "一只猫",
	}))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "playground_endpoint_unavailable") {
		t.Fatalf("任务创建应 503 降级: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/images/jobs", nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("任务列表应 503 降级: %d", recorder.Code)
	}
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/artifacts", nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("清单应 503 降级: %d", recorder.Code)
	}
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/artifacts/some-id/content", nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("content 应 503 降级: %d", recorder.Code)
	}
	dispatch := deps.Dispatch.(*mockDispatch)
	if len(dispatch.calls) != 0 {
		t.Fatalf("降级路径不应派发（%d 次）", len(dispatch.calls))
	}
}

// TestArtifactContentNotFoundAndServing 形状补充：合法 id 无对应产物 404；
// ServeContent 的字节与 Last-Modified 落实（参考图上传真实落盘，读取路径可
// Range 请求）。
func TestArtifactContentNotFoundAndServing(t *testing.T) {
	deps := playgroundFixtureDeps(t, false)
	addImageCandidate(deps)
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/images/references", mustJSON(map[string]any{
		"dataBase64": base64Of(pngOnePixel),
	}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("参考图上传状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var reference struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &reference); err != nil {
		t.Fatalf("参考图响应解析失败: %v", err)
	}
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/artifacts/1730000000000000009-nothinghere/content", nil)
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "playground_artifact_not_found") {
		t.Fatalf("不存在 id 应 404: %d %s", recorder.Code, recorder.Body.String())
	}
	// Range 请求（ServeContent 语义）：单字节区间。
	request := httptest.NewRequest("GET", "/__aisys__/api/playground/artifacts/"+reference.Data.ID+"/content", nil)
	request.Header.Set("Range", "bytes=0-3")
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPartialContent || recorder.Body.Len() != 4 {
		t.Fatalf("Range 请求应 206 且 4 字节: %d %d", recorder.Code, recorder.Body.Len())
	}
}
