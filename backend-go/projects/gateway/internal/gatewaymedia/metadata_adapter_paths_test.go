package gatewaymedia

// adapter 规范路径漂移防护：逐个 adapter 用“所有可选字段都生效”的输入构造
// 真实出站报文，断言报文中的每个叶子路径都落在该 adapter 声明的 reserved
// 集合内。新增/改名出站字段而忘记同步 metadata_adapter_paths.go 时，本用例
// 会失败——否则 metadata 可重新打开“补写规范字段抬高上游工作量”的计费绕过
// （设计《对外协议面对齐 new-api 设计》§5.1 的安全边界）。
//
// 矩阵覆盖 7 个视频 adapter + 2 个长音频 adapter + 4 个语音 adapter。
// GLM 语音当前没有独立 SpeechAdapter（注册表仅 gemini/minimax/volcengine），
// 不在本矩阵内；新增 adapter 时必须同步补入。

import (
	"context"
	"encoding/json"
	"testing"
)

func leafMetadataPaths(value any, prefix string, out map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			leafMetadataPaths(child, path, out)
		}
	default:
		if prefix != "" {
			out[prefix] = struct{}{}
		}
	}
}

func assertBodyPathsReserved(t *testing.T, label string, body []byte, reservedPaths []string, scope ...string) {
	t.Helper()
	var bodyObject map[string]any
	if err := json.Unmarshal(body, &bodyObject); err != nil {
		t.Fatalf("%s: 出站报文不是 JSON 对象: %v", label, err)
	}
	// scope 用于 metadata 合并层与报文根不同的 adapter（火山流式 TTS 的
	// metadata 只并入 req_params；user 等外层键不可经扩展通道写入）。
	var root any = bodyObject
	for _, segment := range scope {
		object, ok := root.(map[string]any)
		if !ok {
			t.Fatalf("%s: scope %v 不是 JSON 对象", label, scope)
		}
		child, ok := object[segment]
		if !ok {
			t.Fatalf("%s: scope %v 缺少段 %q，测试会空跑", label, scope, segment)
		}
		root = child
	}
	paths := map[string]struct{}{}
	leafMetadataPaths(root, "", paths)
	reserved := make(map[string]struct{}, len(reservedPaths))
	for _, path := range reservedPaths {
		reserved[path] = struct{}{}
	}
	for path := range paths {
		if !metadataPathReserved(path, reserved) {
			t.Errorf("%s: 出站路径 %q 不在 reserved 集合 %v 中（metadata 可补写该规范字段）",
				label, path, reservedPaths)
		}
	}
}

func TestVideoAdapterBodyPathsCoveredByMetadataReserved(t *testing.T) {
	seconds := 10.0
	seed := int64(7)
	audio := true
	fps := 30
	reference := "data:image/png;base64,aGVsbG8="
	input := func(model string) VideoCreateInput {
		return VideoCreateInput{Params: NormalizedVideoParams{
			Model: model, Prompt: "p", NegativePrompt: "n", Size: "1280x720",
			Seconds: &seconds, Seed: &seed, Audio: &audio, FPS: &fps,
			InputReference: &reference, N: 1,
		}}
	}
	for _, tc := range []struct {
		label    string
		adapter  VideoProviderAdapter
		model    string
		reserved []string
	}{
		{"glm", glmVideoAdapter{}, "cogvideox-3", glmVideoMetadataReservedPaths},
		{"minimax", minimaxVideoAdapter{}, "MiniMax-Hailuo-2.3", minimaxVideoMetadataReservedPaths},
		{"qwen", qwenVideoAdapter{}, "wan2.6-t2v", qwenVideoMetadataReservedPaths},
		{"veo", veoVideoAdapter{}, "veo-3.0-generate-preview", veoVideoMetadataReservedPaths},
		{"volcengine", volcengineVideoAdapter{}, "doubao-seedance-2-0", volcengineVideoMetadataReservedPaths},
		{"xai", xaiVideoAdapter{}, "grok-imagine-video-1.5", xaiVideoMetadataReservedPaths},
		{"openai", openaiVideoAdapter{}, "sora-2", openAIVideoMetadataReservedPaths},
	} {
		output, err := tc.adapter.Create(context.Background(), input(tc.model))
		if err != nil {
			t.Fatalf("%s create: %v", tc.label, err)
		}
		assertBodyPathsReserved(t, tc.label, output.Body, tc.reserved)
	}
}

func TestAudioJobAdapterBodyPathsCoveredByMetadataReserved(t *testing.T) {
	for _, tc := range []struct {
		label    string
		adapter  AudioJobProviderAdapter
		in       AudioJobCreateInput
		reserved []string
	}{
		{
			label:   "qwen-asr",
			adapter: qwenASRAdapter{},
			in: AudioJobCreateInput{Params: NormalizedAudioJobParams{
				Model: "paraformer-v2", InputURL: "https://example.com/a.mp3", Language: "zh",
			}},
			reserved: qwenASRMetadataReservedPaths,
		},
		{
			label:   "volcengine-asr",
			adapter: volcengineASRAdapter{},
			in: AudioJobCreateInput{
				Params:   NormalizedAudioJobParams{Model: "volc.seedasr.auc", InputURL: "https://example.com/a.mp3"},
				Metadata: map[string]any{"audio_format": "mp3"},
			},
			reserved: volcengineASRMetadataReservedPaths,
		},
	} {
		output, err := tc.adapter.Create(context.Background(), tc.in)
		if err != nil {
			t.Fatalf("%s create: %v", tc.label, err)
		}
		assertBodyPathsReserved(t, tc.label, output.Body, tc.reserved)
	}
}

func TestSpeechAdapterBodyPathsCoveredByMetadataReserved(t *testing.T) {
	speed := 1.2
	speech := SpeechRequest{
		Model: "m", Input: "hello", Voice: "v", Speed: &speed, ResponseFormat: "mp3",
	}
	for _, tc := range []struct {
		label    string
		build    func(SpeechRequest) (string, []byte, error)
		ir       SpeechRequest
		reserved []string
		scope    []string
	}{
		{
			label:    "minimax-tts",
			build:    minimaxSpeechAdapter{}.BuildRequest,
			ir:       speech,
			reserved: minimaxTTSMetadataReservedPaths,
		},
		{
			label:    "volcengine-tts",
			build:    volcengineSpeechAdapter{}.BuildRequest,
			ir:       speech,
			reserved: volcengineTTSMetadataReservedPaths,
		},
		{
			label: "gemini-tts",
			build: geminiSpeechAdapter{}.BuildRequest,
			ir: SpeechRequest{
				Model: "gemini-3.8-flash-tts", Input: "hello", Voice: "Kore", ResponseFormat: "pcm",
			},
			reserved: geminiTTSMetadataReservedPaths,
		},
		{
			label:    "volcengine-tts-stream",
			build:    buildVolcengineTTSStreamRequest,
			ir:       speech,
			reserved: volcengineTTSStreamMetadataReservedPaths,
			scope:    []string{"req_params"},
		},
	} {
		_, body, err := tc.build(tc.ir)
		if err != nil {
			t.Fatalf("%s build: %v", tc.label, err)
		}
		assertBodyPathsReserved(t, tc.label, body, tc.reserved, tc.scope...)
	}
}
