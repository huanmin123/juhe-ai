package main

// New API 兼容面（2026-11-12）纯函数单测：路径归一、创建请求字段映射、
// metadata 唯一扩展通道、创建/查询响应形状、状态词表与 url/format 规则。
// 不触 PG/HTTP（链路级覆盖见 chain_media_video_test.go）。

import (
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestChainMediaJobStrippedPathOfNewAPIVideo(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/v1/video/generations", "/videos"},
		{"/video/generations", "/videos"},
		{"/v1/video/generations?limit=1", "/videos"},
		{"/v1/video/generations/video_1", "/videos/video_1"},
		{"/v1/video/generations/video_1/content", "/videos/video_1/content"},
		{"/V1/VIDEO/GENERATIONS/VIDEO_1", "/videos/video_1"},
		// 旧路径与相邻路径不误改写。
		{"/v1/videos", "/videos"},
		{"/v1/videos/video_1", "/videos/video_1"},
		{"/v1/videos/generations", "/videos/generations"},
		{"/v1/video/generations2", "/video/generations2"},
		{"/v1/audio/jobs", "/audio/jobs"},
		// 长音频 New API 通用任务面归一（旧 /audio/jobs 为内部词表）。
		{"/v1/tasks/audio", "/audio/jobs"},
		{"/tasks/audio", "/audio/jobs"},
		{"/v1/tasks/audio?limit=1", "/audio/jobs"},
		{"/v1/tasks/audio/audiojob_1", "/audio/jobs/audiojob_1"},
		{"/v1/tasks/audio/audiojob_1/content", "/audio/jobs/audiojob_1/content"},
		{"/V1/TASKS/AUDIO/AUDIOJOB_1", "/audio/jobs/audiojob_1"},
		{"/v1/tasks/audio2", "/tasks/audio2"},
	}
	for _, tc := range cases {
		if got := chainMediaJobStrippedPathOf(tc.path); got != tc.want {
			t.Fatalf("chainMediaJobStrippedPathOf(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestChainNewAPIVideoBodyOfMapping(t *testing.T) {
	body := map[string]any{
		"model":           "sora-2",
		"prompt":          "一只猫",
		"duration":        float64(5),
		"width":           float64(1280),
		"height":          float64(720),
		"image":           "https://img.invalid/first.png",
		"seed":            float64(7),
		"n":               float64(1),
		"fps":             float64(24),
		"response_format": "mp4",
		"user":            "u1",
		"metadata": map[string]any{
			"negative_prompt": "低清",
			"audio":           false,
			"seed":            float64(9),
			"camera_fixed":    true,
			"nested":          map[string]any{"k": float64(1)},
		},
	}
	normalized, err := chainNewAPIVideoBodyOf(body)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	// 入参不被修改。
	if _, mutated := body["seconds"]; mutated {
		t.Fatalf("chainNewAPIVideoBodyOf must not mutate the input body: %#v", body)
	}
	if normalized.body["seconds"] != float64(5) || normalized.body["size"] != "1280x720" ||
		normalized.body["input_reference"] != "https://img.invalid/first.png" {
		t.Fatalf("normalized body = %#v", normalized.body)
	}
	// metadata 已知 L2 键提升（顶层显式值优先）。
	if normalized.body["negative_prompt"] != "低清" || normalized.body["audio"] != false || normalized.body["seed"] != float64(7) {
		t.Fatalf("promoted L2 keys = %#v", normalized.body)
	}
	// metadata 回显原样（提升键 + 其余键 + 本地无统一语义键）。
	for _, key := range []string{"negative_prompt", "audio", "seed", "camera_fixed", "nested", "fps", "response_format", "user"} {
		if _, ok := normalized.metadata[key]; !ok {
			t.Fatalf("metadata echo missing %s: %#v", key, normalized.metadata)
		}
	}
	// 厂商扩展子对象只含未提升的 metadata 键。
	if _, ok := normalized.providerOptions["negative_prompt"]; ok {
		t.Fatalf("promoted key must not leak into provider options: %#v", normalized.providerOptions)
	}
	if normalized.providerOptions["camera_fixed"] != true || normalized.providerOptions["nested"] == nil {
		t.Fatalf("provider options = %#v", normalized.providerOptions)
	}
	if _, ok := normalized.providerOptions["fps"]; ok {
		t.Fatalf("local ignored key must not enter provider options: %#v", normalized.providerOptions)
	}
	ignored := strings.Join(normalized.extraIgnored, ",")
	if ignored != "fps,response_format,user" {
		t.Fatalf("extraIgnored = %q", ignored)
	}
}

func TestChainNewAPIVideoBodyOfAliasesAndSingleSidedDimensions(t *testing.T) {
	// 旧 seconds/size/input_reference 容忍别名。
	normalized, err := chainNewAPIVideoBodyOf(map[string]any{
		"model": "sora-2", "prompt": "别名", "seconds": "4", "size": "1280x720", "input_reference": "data:image/png;base64,AA",
	})
	if err != nil {
		t.Fatalf("aliases: %v", err)
	}
	if normalized.body["seconds"] != "4" || normalized.body["size"] != "1280x720" || normalized.body["input_reference"] != "data:image/png;base64,AA" {
		t.Fatalf("alias body = %#v", normalized.body)
	}

	// 单边 width：忽略并回显键名，不生成 size。
	normalized, err = chainNewAPIVideoBodyOf(map[string]any{"model": "sora-2", "prompt": "单边", "width": float64(1280)})
	if err != nil {
		t.Fatalf("single side: %v", err)
	}
	if _, ok := normalized.body["size"]; ok {
		t.Fatalf("single-sided width must not synthesize size: %#v", normalized.body)
	}
	if strings.Join(normalized.extraIgnored, ",") != "width" {
		t.Fatalf("extraIgnored = %v, want width", normalized.extraIgnored)
	}
}

func TestChainNewAPIVideoBodyOfRejectsLegacyChannels(t *testing.T) {
	if _, err := chainNewAPIVideoBodyOf(map[string]any{
		"model": "sora-2", "prompt": "旧通道", "provider_options": map[string]any{"openai": map[string]any{"x": true}},
	}); err == nil {
		t.Fatal("provider_options must be rejected")
	} else if !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("provider_options error should point at metadata: %v", err)
	}
	if _, err := chainNewAPIVideoBodyOf(map[string]any{
		"model": "sora-2", "prompt": "坏 metadata", "metadata": "not-an-object",
	}); err == nil {
		t.Fatal("non-object metadata must be rejected")
	}
	if _, err := chainNewAPIVideoBodyOf(map[string]any{
		"model": "sora-2", "prompt": "坏维度", "width": "wide",
	}); err == nil {
		t.Fatal("non-integer width must be rejected")
	}
}

func TestChainNewAPIBodyOfMetadataNullTreatedAsAbsent(t *testing.T) {
	// 视频与长音频两族同口径：metadata 显式 null 按未携带处理——容忍不 400、
	// 不提升公共参数、不并入厂商 provider_options、响应 metadata 回显为空
	//（出站报文由 adapter 按 params 构造，扩展键不进上游 wire；与图像面
	// normalizeImageMetadataBody 的 null 删除语义等效。2026-11-13 复审测试
	// 缺口补齐）。
	videoNormalized, err := chainNewAPIVideoBodyOf(map[string]any{
		"model": "sora-2", "prompt": "空 metadata", "metadata": nil,
	})
	if err != nil {
		t.Fatalf("video: null metadata must be tolerated: %v", err)
	}
	if len(videoNormalized.metadata) != 0 || len(videoNormalized.providerOptions) != 0 {
		t.Fatalf("video: null metadata must not promote/merge: %#v / %#v", videoNormalized.metadata, videoNormalized.providerOptions)
	}
	audioNormalized, err := chainNewAPIAudioBodyOf(map[string]any{
		"model": "m", "input_url": "https://audio.invalid/a.wav", "metadata": nil,
	}, "qwen")
	if err != nil {
		t.Fatalf("audio: null metadata must be tolerated: %v", err)
	}
	if len(audioNormalized.metadata) != 0 || len(audioNormalized.providerOptions) != 0 {
		t.Fatalf("audio: null metadata must not promote/merge: %#v / %#v", audioNormalized.metadata, audioNormalized.providerOptions)
	}
}

func TestChainNewAPIVideoCreateObjectShape(t *testing.T) {
	record := &gatewaymedia.MediaJobRecord{
		ID:            "video_abc",
		Kind:          gatewaymedia.JobKindVideo,
		VendorCode:    "gpt",
		UpstreamJobID: "video_upstream",
		Status:        gatewaymedia.JobStatusQueued,
		CreatedAt:     time.Unix(1700000000, 0),
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model:                  "sora-2",
			Metadata:               map[string]any{"negative_prompt": "低清"},
			ProviderOptionsApplied: []string{"camera_fixed"},
		},
		ParamsApplied: []string{"model", "prompt", "seconds"},
		ParamsIgnored: []string{"seed"},
	}
	object := chainNewAPIVideoCreateObjectOf(record)
	if object["id"] != "video_abc" || object["task_id"] != "video_abc" || object["status"] != "queued" ||
		object["object"] != "video" || object["model"] != "sora-2" || object["created_at"] != int64(1700000000) {
		t.Fatalf("create object = %#v", object)
	}
	metadata, _ := object["metadata"].(map[string]any)
	if metadata["negative_prompt"] != "低清" || metadata["provider"] != "gpt" ||
		metadata["provider_job_id"] != "video_upstream" {
		t.Fatalf("create metadata = %#v", metadata)
	}
	if applied, _ := metadata["params_applied"].([]string); len(applied) != 3 {
		t.Fatalf("params_applied echo = %#v", metadata["params_applied"])
	}
	if marks, _ := metadata["provider_options_applied"].([]string); len(marks) != 1 || marks[0] != "camera_fixed" {
		t.Fatalf("provider_options_applied echo = %#v", metadata["provider_options_applied"])
	}
}

func TestChainNewAPIVideoQueryObjectShape(t *testing.T) {
	base := &gatewaymedia.MediaJobRecord{
		ID:            "video_abc",
		Kind:          gatewaymedia.JobKindVideo,
		VendorCode:    "xai",
		UpstreamJobID: "up_1",
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model:    "grok-imagine-video-1.5",
			Metadata: map[string]any{"seed": float64(42)},
		},
	}
	cases := []struct {
		name       string
		status     gatewaymedia.MediaJobStatus
		live       gatewaymedia.MediaJobStatus
		artifact   string
		errorCode  string
		wantStatus string
		wantURL    string
		wantFormat string
	}{
		{"queued", gatewaymedia.JobStatusQueued, "", "", "", "queued", "", "mp4"},
		{"in progress", gatewaymedia.JobStatusQueued, gatewaymedia.JobStatusInProgress, "", "", "in_progress", "", "mp4"},
		{"completed direct", gatewaymedia.JobStatusCompleted, "", "https://cdn.invalid/clip.webm", "", "completed", "https://cdn.invalid/clip.webm", "webm"},
		{"completed fallback", gatewaymedia.JobStatusCompleted, "", "", "", "completed", "/v1/video/generations/video_abc/content", "mp4"},
		{"failed with error", gatewaymedia.JobStatusFailed, "", "", "xai_failed", "failed", "", "mp4"},
		{"cancelled normalized", gatewaymedia.JobStatusCancelled, "", "", "cancelled", "failed", "", "mp4"},
		{"expired normalized", gatewaymedia.JobStatusExpired, "", "", "expired", "failed", "", "mp4"},
	}
	for _, tc := range cases {
		record := *base
		record.Status = tc.status
		record.Artifact = gatewaymedia.MediaJobArtifact{ContentURL: tc.artifact}
		if tc.errorCode == "xai_failed" {
			record.Error = &gatewaymedia.MediaJobError{Code: "xai_failed", Message: "boom"}
		}
		object := chainNewAPIVideoQueryObjectOf(&record, tc.live, nil, "")
		if object["task_id"] != "video_abc" || object["status"] != tc.wantStatus ||
			object["url"] != tc.wantURL || object["format"] != tc.wantFormat {
			t.Fatalf("%s: query object = %#v", tc.name, object)
		}
		if object["code"] != "success" || object["message"] != "" {
			t.Fatalf("%s: code/message envelope = %#v", tc.name, object)
		}
		data, _ := object["data"].(map[string]any)
		if data == nil || data["task_id"] != "video_abc" || data["status"] != tc.wantStatus ||
			data["url"] != tc.wantURL || data["format"] != tc.wantFormat {
			t.Fatalf("%s: data envelope = %#v", tc.name, object["data"])
		}
		flatError, _ := object["error"].(map[string]any)
		if tc.errorCode == "" {
			if flatError != nil {
				t.Fatalf("%s: unexpected error %#v", tc.name, flatError)
			}
		} else if flatError == nil || flatError["code"] != tc.errorCode {
			t.Fatalf("%s: error = %#v, want code %s", tc.name, flatError, tc.errorCode)
		}
		if _, ok := data["metadata"]; !ok {
			t.Fatalf("%s: data missing metadata: %#v", tc.name, data)
		}
	}
}

func TestChainNewAPIVideoDirectURLRequiresAdapterCapability(t *testing.T) {
	// openai/Sora 的 content[].url 需要上游账户认证（ContentFromArtifact=false），
	// 不能作为 New API url 直出；completed 时强制回退网关 content 端点。
	gpt := &gatewaymedia.MediaJobRecord{
		ID:         "video_gpt",
		Kind:       gatewaymedia.JobKindVideo,
		VendorCode: "gpt",
		Status:     gatewaymedia.JobStatusCompleted,
		Artifact:   gatewaymedia.MediaJobArtifact{ContentURL: "https://api.openai.com/v1/videos/v_1/content"},
	}
	object := chainNewAPIVideoQueryObjectOf(gpt, "", nil, "")
	if object["url"] != "/v1/video/generations/video_gpt/content" {
		t.Fatalf("gpt url = %v, want content fallback", object["url"])
	}
	// 非直链厂商但未完成：不得把上游鉴权地址提前暴露。
	gptQueued := *gpt
	gptQueued.Status = gatewaymedia.JobStatusQueued
	if object := chainNewAPIVideoQueryObjectOf(&gptQueued, "", nil, ""); object["url"] != "" {
		t.Fatalf("gpt queued url = %v, want empty", object["url"])
	}
	// 具备直链能力的厂商保持上游直链。
	xai := &gatewaymedia.MediaJobRecord{
		ID:         "video_xai",
		Kind:       gatewaymedia.JobKindVideo,
		VendorCode: "xai",
		Status:     gatewaymedia.JobStatusCompleted,
		Artifact:   gatewaymedia.MediaJobArtifact{ContentURL: "https://cdn.invalid/clip.mp4"},
	}
	if object := chainNewAPIVideoQueryObjectOf(xai, "", nil, ""); object["url"] != "https://cdn.invalid/clip.mp4" {
		t.Fatalf("xai url = %v, want direct", object["url"])
	}
}

func TestChainNewAPIContentFallbackURLPublicBase(t *testing.T) {
	// JUHE_AI_GATEWAY_PUBLIC_BASE_URL（New API ServerAddress 同构）：配置时
	// 回退 content URL 是绝对地址，未配置保持相对路径（客户端自行拼 Host）。
	video := &gatewaymedia.MediaJobRecord{
		ID:     "video_1",
		Kind:   gatewaymedia.JobKindVideo,
		Status: gatewaymedia.JobStatusCompleted,
	}
	audio := &gatewaymedia.MediaJobRecord{
		ID:     "audiojob_1",
		Kind:   gatewaymedia.JobKindAudioTranscription,
		Status: gatewaymedia.JobStatusCompleted,
	}
	if got := chainNewAPIContentFallbackURLOf(video, ""); got != "/v1/video/generations/video_1/content" {
		t.Fatalf("video fallback without base = %q", got)
	}
	if got := chainNewAPIContentFallbackURLOf(video, "https://api.example.com"); got != "https://api.example.com/v1/video/generations/video_1/content" {
		t.Fatalf("video fallback with base = %q", got)
	}
	if got := chainNewAPIContentFallbackURLOf(audio, "https://api.example.com/"); got != "https://api.example.com/v1/tasks/audio/audiojob_1/content" {
		t.Fatalf("audio fallback with base = %q", got)
	}
}

func TestChainNewAPIVideoFormatOf(t *testing.T) {
	cases := map[string]string{
		"https://cdn.invalid/a/clip.mp4":     "mp4",
		"https://cdn.invalid/a/clip.MP4?x=1": "mp4",
		"https://cdn.invalid/a/clip.webm":    "webm",
		"https://cdn.invalid/a/clip":         "mp4",
		"/v1/video/generations/x/content":    "mp4",
		"":                                   "mp4",
	}
	for rawURL, want := range cases {
		if got := chainNewAPIVideoFormatOf(rawURL); got != want {
			t.Fatalf("chainNewAPIVideoFormatOf(%q) = %q, want %q", rawURL, got, want)
		}
	}
}

func TestChainMediaJobTaskPlaneVideoListCancelDispatch(t *testing.T) {
	// 形态判定：新 API 路径的集合 GET 与 DELETE 都按 video 族形态识别（链内
	// 再由 serveMediaJobTaskPlane 返回 404）；旧路径同样识别但公开路由已 404。
	shape, id, kind := chainMediaJobRequestShapeOf("GET", "/v1/video/generations")
	if shape != chainVideoShapeList || id != "" || kind != gatewaymedia.JobKindVideo {
		t.Fatalf("GET collection shape = %v/%q/%v", shape, id, kind)
	}
	shape, id, kind = chainMediaJobRequestShapeOf("DELETE", "/v1/video/generations/video_1")
	if shape != chainVideoShapeCancel || id != "video_1" || kind != gatewaymedia.JobKindVideo {
		t.Fatalf("DELETE shape = %v/%q/%v", shape, id, kind)
	}
	shape, id, kind = chainMediaJobRequestShapeOf("GET", "/v1/video/generations/video_1/content")
	if shape != chainVideoShapeContent || id != "video_1" || kind != gatewaymedia.JobKindVideo {
		t.Fatalf("content shape = %v/%q/%v", shape, id, kind)
	}
	// 长音频任务面按 New API 通用任务面路径同样识别（旧 /audio/jobs 词表内部
	// 归一；公开路由已 404）。
	shape, id, kind = chainMediaJobRequestShapeOf("DELETE", "/v1/tasks/audio/audiojob_1")
	if shape != chainVideoShapeCancel || id != "audiojob_1" || kind != gatewaymedia.JobKindAudioTranscription {
		t.Fatalf("audio delete shape = %v/%q/%v", shape, id, kind)
	}
	shape, id, kind = chainMediaJobRequestShapeOf("GET", "/v1/tasks/audio/audiojob_1/content")
	if shape != chainVideoShapeContent || id != "audiojob_1" || kind != gatewaymedia.JobKindAudioTranscription {
		t.Fatalf("audio content shape = %v/%q/%v", shape, id, kind)
	}
	shape, id, kind = chainMediaJobRequestShapeOf("GET", "/v1/audio/jobs/audiojob_1")
	if shape != chainVideoShapePoll || id != "audiojob_1" || kind != gatewaymedia.JobKindAudioTranscription {
		t.Fatalf("audio poll shape = %v/%q/%v", shape, id, kind)
	}
}

func TestChainVideoCreatePlanValidatedThroughGatewayRequestError(t *testing.T) {
	// metadata 形态错误映射为 GatewayRequestValidationError（本地 400，不换
	// 账户）；provider 能力缺失不受影响。
	req := &gatewaypreauth.GatewayRequest{}
	account := newChainVideoPlanTestAccount()
	if _, err := chainVideoCreatePlanOf(map[string]any{
		"model": "sora-2", "prompt": "x", "provider_options": map[string]any{"openai": map[string]any{"a": true}},
	}, req, account); err == nil {
		t.Fatal("expected validation error for provider_options")
	}
}

// newChainVideoPlanTestAccount 构造只用于 adapter 身份解析的最小候选账户
// （chainVideoAdapterForAccount 只消费 VendorCode）。
func newChainVideoPlanTestAccount() gatewaydispatch.AccountCandidate {
	return gatewaydispatch.AccountCandidate{ID: "acc_test", VendorCode: "gpt"}
}

func TestChainIsLegacyVideoPath(t *testing.T) {
	legacy := []string{
		"/v1/videos", "/videos", "/V1/Videos", "/v1/videos?x=1",
		"/v1/videos/video_1", "/v1/videos/video_1/content", "/videos/generations",
	}
	for _, path := range legacy {
		if !chainIsLegacyVideoPath(path) {
			t.Fatalf("chainIsLegacyVideoPath(%q) = false, want true", path)
		}
	}
	current := []string{
		"/v1/video/generations", "/video/generations", "/v1/video/generations/video_1",
		"/v1/video/generations/video_1/content", "/v1/audio/jobs", "/v1/audio/jobs/audiojob_1",
		"/v1/tasks/audio", "/v1/tasks/audio/audiojob_1",
	}
	for _, path := range current {
		if chainIsLegacyVideoPath(path) {
			t.Fatalf("chainIsLegacyVideoPath(%q) = true, want false", path)
		}
	}
}

func TestChainIsLegacyAudioJobPath(t *testing.T) {
	legacy := []string{
		"/v1/audio/jobs", "/audio/jobs", "/V1/AUDIO/JOBS", "/v1/audio/jobs?x=1",
		"/v1/audio/jobs/audiojob_1", "/v1/audio/jobs/audiojob_1/content",
	}
	for _, path := range legacy {
		if !chainIsLegacyAudioJobPath(path) {
			t.Fatalf("chainIsLegacyAudioJobPath(%q) = false, want true", path)
		}
	}
	current := []string{
		"/v1/tasks/audio", "/tasks/audio", "/v1/tasks/audio/audiojob_1",
		"/v1/tasks/audio/audiojob_1/content",
		// 同步音频三端点不得被旧路径判定误伤。
		"/v1/audio/speech", "/v1/audio/transcriptions", "/v1/audio/translations",
		"/v1/audio", "/v1/audio/jobs2",
	}
	for _, path := range current {
		if chainIsLegacyAudioJobPath(path) {
			t.Fatalf("chainIsLegacyAudioJobPath(%q) = true, want false", path)
		}
	}
}
