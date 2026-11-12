package main

// New API 通用任务面长音频（2026-11-12）纯函数单测：路径归一后的创建请求
// 字段映射、metadata 唯一扩展通道（language 提升 + 其余键并入厂商
// provider_options）、创建/查询响应形状、status 词表与 url/format 规则。
// 不触 PG/HTTP（链路级覆盖见 chain_media_audio_test.go）。

import (
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
)

func TestChainNewAPIAudioBodyOfMapping(t *testing.T) {
	body := map[string]any{
		"model":     "paraformer-v2",
		"input_url": "https://oss.example.com/audio/meeting.mp3",
		"language":  "zh",
		"metadata": map[string]any{
			"language":            "en",
			"diarization_enabled": true,
			"nested":              map[string]any{"k": float64(1)},
		},
	}
	normalized, err := chainNewAPIAudioBodyOf(body, "qwen")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	// 入参不被修改。
	if _, mutated := body[gatewaymedia.ProviderOptionsKey]; mutated {
		t.Fatalf("chainNewAPIAudioBodyOf must not mutate the input body: %#v", body)
	}
	// 顶层显式 language 优先（metadata 中的同名键不覆盖请求者本意）。
	if normalized.body["language"] != "zh" || normalized.body["model"] != "paraformer-v2" ||
		normalized.body["input_url"] != "https://oss.example.com/audio/meeting.mp3" {
		t.Fatalf("normalized body = %#v", normalized.body)
	}
	// metadata 回显原样（含提升键与非提升键）。
	for _, key := range []string{"language", "diarization_enabled", "nested"} {
		if _, ok := normalized.metadata[key]; !ok {
			t.Fatalf("metadata echo missing %s: %#v", key, normalized.metadata)
		}
	}
	// 厂商扩展子对象只含未提升的 metadata 键。
	if _, ok := normalized.providerOptions["language"]; ok {
		t.Fatalf("promoted key must not leak into provider options: %#v", normalized.providerOptions)
	}
	if normalized.providerOptions["diarization_enabled"] != true || normalized.providerOptions["nested"] == nil {
		t.Fatalf("provider options = %#v", normalized.providerOptions)
	}
	// provider_options 内部注入键 = adapter.Provider()。
	providerOptions, ok := normalized.body[gatewaymedia.ProviderOptionsKey].(map[string]any)
	if !ok {
		t.Fatalf("injected provider_options missing: %#v", normalized.body)
	}
	sub, ok := providerOptions["qwen"].(map[string]any)
	if !ok || sub["diarization_enabled"] != true {
		t.Fatalf("injected vendor sub-object = %#v", providerOptions)
	}
	if _, leaked := sub["language"]; leaked {
		t.Fatalf("promoted language must not enter vendor sub-object: %#v", sub)
	}
}

func TestChainNewAPIAudioBodyOfPromotesMetadataLanguage(t *testing.T) {
	normalized, err := chainNewAPIAudioBodyOf(map[string]any{
		"model":     "paraformer-v2",
		"input_url": "https://oss.example.com/audio/a.wav",
		"metadata":  map[string]any{"language": "zh"},
	}, "qwen")
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if normalized.body["language"] != "zh" {
		t.Fatalf("metadata language not promoted: %#v", normalized.body)
	}
	if _, injected := normalized.body[gatewaymedia.ProviderOptionsKey]; injected {
		t.Fatalf("promoted-only metadata must not inject provider_options: %#v", normalized.body)
	}
}

func TestChainNewAPIAudioBodyOfRejectsLegacyChannels(t *testing.T) {
	if _, err := chainNewAPIAudioBodyOf(map[string]any{
		"model": "paraformer-v2", "input_url": "https://oss.example.com/a.mp3",
		"provider_options": map[string]any{"qwen": map[string]any{"x": true}},
	}, "qwen"); err == nil {
		t.Fatal("provider_options must be rejected")
	} else if !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("provider_options error should point at metadata: %v", err)
	}
	if _, err := chainNewAPIAudioBodyOf(map[string]any{
		"model": "paraformer-v2", "input_url": "https://oss.example.com/a.mp3", "metadata": "not-an-object",
	}, "qwen"); err == nil {
		t.Fatal("non-object metadata must be rejected")
	}
}

func TestChainAudioJobCreatePlanNewAPIMetadata(t *testing.T) {
	// metadata 唯一扩展通道：language 提升为公共参数，diarization_enabled
	// 并入 qwen provider_options 并回显生效键名；metadata 原样进任务快照。
	plan, err := chainAudioJobCreatePlanOf(map[string]any{
		"model":     "paraformer-v2",
		"input_url": "https://oss.example.com/audio/meeting.mp3",
		"metadata":  map[string]any{"language": "zh", "diarization_enabled": true},
	}, nil, gatewaydispatch.AccountCandidate{ID: "acc_qwen", VendorCode: "qwen"})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.params.Language != "zh" || !strings.Contains(string(plan.createRequest.Body), `"language_hints":["zh"]`) {
		t.Fatalf("language mapping = %q: %s", plan.params.Language, plan.createRequest.Body)
	}
	if !strings.Contains(string(plan.createRequest.Body), `"diarization_enabled":true`) {
		t.Fatalf("provider_options metadata not merged: %s", plan.createRequest.Body)
	}
	if len(plan.providerOptionsApplied) != 1 || plan.providerOptionsApplied[0] != "diarization_enabled" {
		t.Fatalf("provider_options_applied = %#v", plan.providerOptionsApplied)
	}
	snapshot := plan.snapshotFields()
	if snapshot.Language != "zh" || snapshot.Metadata["language"] != "zh" || snapshot.Metadata["diarization_enabled"] != true {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.ProviderOptionsApplied == nil || snapshot.ProviderOptionsApplied[0] != "diarization_enabled" {
		t.Fatalf("snapshot provider_options = %#v", snapshot.ProviderOptionsApplied)
	}
}

func TestChainNewAPIAudioCreateObjectShape(t *testing.T) {
	record := &gatewaymedia.MediaJobRecord{
		ID:            "audiojob_abc",
		Kind:          gatewaymedia.JobKindAudioTranscription,
		VendorCode:    "qwen",
		UpstreamJobID: "upstream_task",
		Status:        gatewaymedia.JobStatusQueued,
		CreatedAt:     time.Unix(1700000000, 0),
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model:                  "paraformer-v2",
			Language:               "zh",
			Metadata:               map[string]any{"language": "zh"},
			ProviderOptionsApplied: []string{"diarization_enabled"},
		},
		ParamsApplied: []string{"model", "input_url", "language"},
		ParamsIgnored: []string{},
	}
	object := chainNewAPIVideoCreateObjectOf(record)
	if object["id"] != "audiojob_abc" || object["task_id"] != "audiojob_abc" || object["status"] != "queued" ||
		object["object"] != "task" || object["model"] != "paraformer-v2" || object["created_at"] != int64(1700000000) {
		t.Fatalf("create object = %#v", object)
	}
	metadata, _ := object["metadata"].(map[string]any)
	if metadata["language"] != "zh" || metadata["provider"] != "qwen" || metadata["provider_job_id"] != "upstream_task" {
		t.Fatalf("create metadata = %#v", metadata)
	}
	if applied, _ := metadata["params_applied"].([]string); len(applied) != 3 {
		t.Fatalf("params_applied echo = %#v", metadata["params_applied"])
	}
	if marks, _ := metadata["provider_options_applied"].([]string); len(marks) != 1 || marks[0] != "diarization_enabled" {
		t.Fatalf("provider_options_applied echo = %#v", metadata["provider_options_applied"])
	}
}

func TestChainNewAPIAudioQueryObjectShape(t *testing.T) {
	base := &gatewaymedia.MediaJobRecord{
		ID:            "audiojob_abc",
		Kind:          gatewaymedia.JobKindAudioTranscription,
		VendorCode:    "qwen",
		UpstreamJobID: "up_1",
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model:    "paraformer-v2",
			Metadata: map[string]any{"language": "zh"},
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
		{"queued", gatewaymedia.JobStatusQueued, "", "", "", "queued", "", "json"},
		{"in progress", gatewaymedia.JobStatusQueued, gatewaymedia.JobStatusInProgress, "", "", "in_progress", "", "json"},
		{"completed direct", gatewaymedia.JobStatusCompleted, "", "https://cdn.invalid/result.json", "", "completed", "https://cdn.invalid/result.json", "json"},
		{"completed fallback", gatewaymedia.JobStatusCompleted, "", "", "", "completed", "/v1/tasks/audio/audiojob_abc/content", "json"},
		{"failed with error", gatewaymedia.JobStatusFailed, "", "", "qwen_failed", "failed", "", "json"},
		{"cancelled normalized", gatewaymedia.JobStatusCancelled, "", "", "cancelled", "failed", "", "json"},
		{"expired normalized", gatewaymedia.JobStatusExpired, "", "", "expired", "failed", "", "json"},
	}
	for _, tc := range cases {
		record := *base
		record.Status = tc.status
		record.Artifact = gatewaymedia.MediaJobArtifact{ContentURL: tc.artifact}
		if tc.errorCode == "qwen_failed" {
			record.Error = &gatewaymedia.MediaJobError{Code: "qwen_failed", Message: "boom"}
		}
		object := chainNewAPIQueryObjectOf(&record, tc.live, nil, "")
		if object["task_id"] != "audiojob_abc" || object["status"] != tc.wantStatus ||
			object["url"] != tc.wantURL || object["format"] != tc.wantFormat {
			t.Fatalf("%s: query object = %#v", tc.name, object)
		}
		if object["object"] != "task" || object["code"] != "success" || object["message"] != "" {
			t.Fatalf("%s: envelope = %#v", tc.name, object)
		}
		data, _ := object["data"].(map[string]any)
		if data == nil || data["task_id"] != "audiojob_abc" || data["status"] != tc.wantStatus ||
			data["url"] != tc.wantURL || data["format"] != tc.wantFormat || data["object"] != "task" {
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
		if metadata, _ := data["metadata"].(map[string]any); metadata == nil || metadata["provider"] != "qwen" {
			t.Fatalf("%s: data metadata = %#v", tc.name, data["metadata"])
		}
	}
}

func TestChainNewAPIAudioDirectURLRequiresAdapterCapability(t *testing.T) {
	// 火山长转写无产物下载 URL（ContentFromArtifact=false）：即使行内有地址也
	// 不直出，completed 时回退网关 content 端点。
	volcengine := &gatewaymedia.MediaJobRecord{
		ID:         "audiojob_volc",
		Kind:       gatewaymedia.JobKindAudioTranscription,
		VendorCode: "volcengine",
		Status:     gatewaymedia.JobStatusCompleted,
		Artifact:   gatewaymedia.MediaJobArtifact{ContentURL: "https://internal.invalid/result.json"},
	}
	object := chainNewAPIQueryObjectOf(volcengine, "", nil, "")
	if object["url"] != "/v1/tasks/audio/audiojob_volc/content" {
		t.Fatalf("volcengine url = %v, want content fallback", object["url"])
	}
}

func TestChainNewAPIAudioFormatOf(t *testing.T) {
	cases := map[string]string{
		"https://cdn.invalid/a/result.json":  "json",
		"https://cdn.invalid/a/result.JSON":  "json",
		"https://cdn.invalid/a/result.txt":   "txt",
		"https://cdn.invalid/a/result":       "json",
		"/v1/tasks/audio/audiojob_1/content": "json",
		"":                                   "json",
	}
	for rawURL, want := range cases {
		if got := chainNewAPIFormatOf(gatewaymedia.JobKindAudioTranscription, rawURL); got != want {
			t.Fatalf("chainNewAPIFormatOf(audio, %q) = %q, want %q", rawURL, got, want)
		}
	}
}
