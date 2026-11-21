// 推导规则表驱动测试（PLAN-20261011T141004000Z §推导规则 / §测试环境边界）：
// 覆盖纯对话四供应商、混合账户（volcengine、gemini+veo）、纯媒体（minimax）、
// 图像、以及缺省/空数组/null/非数组/未知模式的边界解析。授权实例继承与
// 软删除行在迁移器集成测试（migration_test.go）覆盖。

package capabilitydomain

import (
	"encoding/json"
	"testing"
)

func mustModesJSON(t *testing.T, value string) map[string]any {
	t.Helper()
	var credentials map[string]any
	if err := json.Unmarshal([]byte(value), &credentials); err != nil {
		t.Fatalf("parse credentials fixture: %v", err)
	}
	return credentials
}

// TestDeriveDomainTable 覆盖规则 ②③④⑤ 的命中顺序与「含对话 modes 即停」。
func TestDeriveDomainTable(t *testing.T) {
	cases := []struct {
		name  string
		modes []string
		want  Domain
	}{
		// 纯对话四供应商（不同档案族的对话族 mode 集）。
		{"gpt openai 族对话", []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}, DomainChat},
		{"xai responses 对话", []string{"responses_json", "responses_sse"}, DomainChat},
		{"deepseek anthropic messages", []string{"messages_json", "messages_sse", "message_token_counting"}, DomainChat},
		{"gemini openai chat 档案", []string{"chat_json"}, DomainChat},
		{"gemini native 全套", []string{"generate_content_json", "generate_content_sse", "count_tokens", "embed_content", "interactions_json", "interactions_sse"}, DomainChat},
		{"anthropic 仅辅助计数", []string{"message_token_counting"}, DomainChat},

		// v8-A 对话域纯粹化：短转写与实时会话不再是对话锚集成员，只声明
		// 其一即命中音频域（规则 ③）；images_json 锚外归属不变。
		{"短转写协议面（v8-A 归音频域）", []string{"audio_transcription_json"}, DomainAudio},
		{"realtime 协议面（v8-A 归音频域）", []string{"realtime_session"}, DomainAudio},
		{"短转写+实时会话", []string{"audio_transcription_json", "realtime_session"}, DomainAudio},
		{"短转写+语音合成", []string{"audio_transcription_json", "audio_speech"}, DomainAudio},

		// 混合账户：含对话 modes → 一律 chat（规则 ②-④ 被前提挡住）。
		{"volcengine 混合（对话+视频+tts）", []string{"chat_json", "video_create", "video_get", "video_content", "video_cancel", "audio_speech"}, DomainChat},
		{"gemini+veo 混合", []string{"generate_content_json", "generate_content_sse", "count_tokens", "video_create", "video_get", "video_content", "video_cancel"}, DomainChat},
		{"hybrid 全族混合", []string{"chat_json", "messages_json", "generate_content_json", "images_json", "audio_speech", "video_create"}, DomainChat},
		{"对话+图像共享 lane", []string{"chat_json", "images_json"}, DomainChat},

		// 纯媒体：无对话 modes，按 ②③④ 命中。
		{"minimax 纯媒体（视频任务四值+tts）", []string{"video_create", "video_get", "video_content", "video_cancel", "audio_speech"}, DomainVideo},
		{"仅视频创建 token", []string{"video_create"}, DomainVideo},
		{"视频任务组完整", []string{"video_create", "video_get", "video_content", "video_cancel"}, DomainVideo},
		{"tts 语音合成", []string{"audio_speech"}, DomainAudio},
		{"长音频任务单 token", []string{"audio_job_create"}, DomainAudio},
		{"长音频任务组完整", []string{"audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel"}, DomainAudio},
		{"纯图像", []string{"images_json"}, DomainImage},
		{"图像+音频（无视频无对话）", []string{"images_json", "audio_speech"}, DomainAudio},

		// v2（向量/重排拆分设计 §4）：纯单值向量/重排声明落独立域；一切混合
		// 组合（与对话锚、媒体值或彼此同存）照 v1 落 chat，不自动拆域。
		{"纯向量", []string{"embeddings_json"}, DomainEmbedding},
		{"纯重排", []string{"rerank_json"}, DomainRerank},
		{"向量+重排同存（混合，落 chat）", []string{"embeddings_json", "rerank_json"}, DomainChat},
		{"向量+对话锚（混合，落 chat）", []string{"embeddings_json", "chat_json"}, DomainChat},
		{"重排+对话锚（混合，落 chat）", []string{"rerank_json", "responses_json", "responses_sse"}, DomainChat},
		{"向量+图像（混合，照 v1 落 chat 不落 image）", []string{"embeddings_json", "images_json"}, DomainChat},
		{"重排+视频（混合，照 v1 落 chat 不落 video）", []string{"rerank_json", "video_create"}, DomainChat},
		{"向量+音频（混合，照 v1 落 chat 不落 audio，前端镜像钉值）", []string{"embeddings_json", "audio_speech"}, DomainChat},
		{"三向混合（向量+重排+对话锚，落 chat）", []string{"embeddings_json", "rerank_json", "chat_sse"}, DomainChat},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := DeriveDomain(testCase.modes); got != testCase.want {
				t.Fatalf("DeriveDomain(%v) = %q, want %q", testCase.modes, got, testCase.want)
			}
		})
	}
}

// TestDeriveDomainOrderPinsVideoOverAudio pins the rule order: a pure-media
// account declaring both video and audio lanes lands in video (rule ② first).
func TestDeriveDomainOrderPinsVideoOverAudio(t *testing.T) {
	if got := DeriveDomain([]string{"video_create", "audio_speech"}); got != DomainVideo {
		t.Fatalf("DeriveDomain(video+audio) = %q, want video (规则 ② 先于 ③)", got)
	}
	if got := DeriveDomain([]string{"images_json", "video_create"}); got != DomainVideo {
		t.Fatalf("DeriveDomain(image+video) = %q, want video (规则 ② 先于 ④)", got)
	}
}

// TestLaneModesForDomain 钉住 v2 六域 lane 集（向量/重排拆分设计 §3）：
// chat 13 值、embedding/rerank 各 1 值、媒体域不变。
func TestLaneModesForDomain(t *testing.T) {
	if chat := LaneModesForDomain(DomainChat); len(chat) != 13 {
		t.Fatalf("chat lane = %d 值, want 13", len(chat))
	}
	if embedding := LaneModesForDomain(DomainEmbedding); len(embedding) != 1 || embedding[0] != "embeddings_json" {
		t.Fatalf("embedding lane = %v, want [embeddings_json]", embedding)
	}
	if rerank := LaneModesForDomain(DomainRerank); len(rerank) != 1 || rerank[0] != "rerank_json" {
		t.Fatalf("rerank lane = %v, want [rerank_json]", rerank)
	}
	if LaneModesForDomain(Domain("unknown")) != nil {
		t.Fatal("未知域 lane 应返回 nil")
	}
}

// TestTestKindForAccount 钉住 v2 新域恒连接测试（拆分设计 §7）。
func TestTestKindForAccount(t *testing.T) {
	for _, domain := range []string{string(DomainEmbedding), string(DomainRerank)} {
		if got := TestKindForAccount(domain, []string{"embeddings_json"}); got != TestKindConnection {
			t.Fatalf("TestKindForAccount(%s) = %q, want connection", domain, got)
		}
	}
	if got := TestKindForAccount(string(DomainChat), []string{"embeddings_json"}); got != TestKindConnection {
		t.Fatalf("chat 域非生成形态 = %q, want connection", got)
	}
	if got := TestKindForAccount(string(DomainChat), []string{"chat_json"}); got != TestKindModel {
		t.Fatalf("chat 域生成形态 = %q, want model", got)
	}
}

// TestDeclarationRetired 钉住五域声明退役判定（上游接口能力设计 §3）：
// 仅 image/audio/video/embedding/rerank 退役，chat 与未知值/空串（列
// DEFAULT 'chat' 同义）不退役。
func TestDeclarationRetired(t *testing.T) {
	for _, domain := range []Domain{DomainImage, DomainAudio, DomainVideo, DomainEmbedding, DomainRerank} {
		if !DeclarationRetired(domain) {
			t.Fatalf("DeclarationRetired(%s) = false, want true", domain)
		}
	}
	for _, domain := range []Domain{DomainChat, "", Domain("unknown")} {
		if DeclarationRetired(domain) {
			t.Fatalf("DeclarationRetired(%q) = true, want false", domain)
		}
	}
}

// TestParseEndpointModesTable 覆盖缺省/空数组/null/非数组/未知模式/非字符串
// 元素的边界分类：除 ModesOK 外全部进失败清单，不猜域。
func TestParseEndpointModesTable(t *testing.T) {
	cases := []struct {
		name          string
		credentials   string
		wantStatus    ModesStatus
		wantFailKind  FailureKind
		wantModesHint []string
	}{
		{
			name:         "缺省：键不存在",
			credentials:  `{"api_key":"x"}`,
			wantStatus:   ModesMissing,
			wantFailKind: FailureModesMissing,
		},
		{
			name:         "空数组",
			credentials:  `{"supported_endpoint_modes":[]}`,
			wantStatus:   ModesEmpty,
			wantFailKind: FailureModesEmpty,
		},
		{
			name:         "显式 null",
			credentials:  `{"supported_endpoint_modes":null}`,
			wantStatus:   ModesNull,
			wantFailKind: FailureModesNull,
		},
		{
			name:         "非数组（字符串）",
			credentials:  `{"supported_endpoint_modes":"chat_json"}`,
			wantStatus:   ModesNotArray,
			wantFailKind: FailureModesNotArray,
		},
		{
			name:         "非数组（对象）",
			credentials:  `{"supported_endpoint_modes":{"chat_json":true}}`,
			wantStatus:   ModesNotArray,
			wantFailKind: FailureModesNotArray,
		},
		{
			name:         "未知模式（伪 video_* 不自动放行）",
			credentials:  `{"supported_endpoint_modes":["video_render_3d"]}`,
			wantStatus:   ModesUnknown,
			wantFailKind: FailureModesUnknown,
		},
		{
			name:         "合法集内混未知值",
			credentials:  `{"supported_endpoint_modes":["chat_json","mystery_mode"]}`,
			wantStatus:   ModesUnknown,
			wantFailKind: FailureModesUnknown,
		},
		{
			name:         "非字符串元素",
			credentials:  `{"supported_endpoint_modes":[42]}`,
			wantStatus:   ModesUnknown,
			wantFailKind: FailureModesUnknown,
		},
		{
			name:          "合法对话集",
			credentials:   `{"supported_endpoint_modes":["chat_json","chat_sse"]}`,
			wantStatus:    ModesOK,
			wantModesHint: []string{"chat_json", "chat_sse"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reading := ParseEndpointModes(mustModesJSON(t, testCase.credentials))
			if reading.Status != testCase.wantStatus {
				t.Fatalf("status = %d, want %d（detail: %s）", reading.Status, testCase.wantStatus, reading.Detail)
			}
			failure, bad := FailureFor("acct-x", reading)
			if bad {
				if failure.Kind != testCase.wantFailKind {
					t.Fatalf("failure kind = %q, want %q", failure.Kind, testCase.wantFailKind)
				}
				if failure.Detail == "" {
					t.Fatal("失败清单条目必须带脱敏 detail")
				}
			} else if testCase.wantFailKind != "" {
				t.Fatalf("expected failure %q, got ModesOK", testCase.wantFailKind)
			}
			if testCase.wantModesHint != nil {
				if len(reading.Modes) != len(testCase.wantModesHint) {
					t.Fatalf("modes = %v, want %v", reading.Modes, testCase.wantModesHint)
				}
				for i := range reading.Modes {
					if reading.Modes[i] != testCase.wantModesHint[i] {
						t.Fatalf("modes = %v, want %v", reading.Modes, testCase.wantModesHint)
					}
				}
			}
		})
	}
}

// TestKnownModesClosure 断言已知词表与对话锚集合的闭合关系：已知 27 值 =
// 对话 13 + 向量/重排 2 + 媒体 12（v2 向量/重排拆分把 embeddings_json /
// rerank_json 自对话锚集移入独立命中集，并集不变），对话、向量/重排与媒体
// 集合两两不相交；对话域纯粹化移动的三项归属同时钉住。
func TestKnownModesClosure(t *testing.T) {
	chat := ChatModes()
	known := KnownModes()
	if len(chat) != 13 {
		t.Fatalf("对话 modes 锚集合必须为 13 值（v8-A 移出三项 + v2 移出向量/重排两项），实际 %d", len(chat))
	}
	if len(known) != 27 {
		t.Fatalf("已知词表必须为 27 值（对话 13 + 向量/重排 2 + 媒体 12），实际 %d", len(known))
	}
	chatSet := map[string]bool{}
	for _, mode := range chat {
		chatSet[mode] = true
	}
	overlap := 0
	for _, mode := range known {
		if chatSet[mode] {
			overlap++
		}
	}
	// known 内落在对话锚集合的值应恰为 13（向量/重排 2 值与媒体 12 值不在
	// 对话集合内）。
	if overlap != 13 {
		t.Fatalf("known ∩ chat 应为 13 值，实际 %d", overlap)
	}
	for _, mode := range mediaModes {
		if chatSet[mode] {
			t.Fatalf("媒体值 %q 不得出现在对话锚集合内", mode)
		}
	}
	// v2 归属钉住：向量/重排两值移出对话锚集，各归独立命中集。
	for _, mode := range []string{"embeddings_json", "rerank_json"} {
		if chatSet[mode] {
			t.Fatalf("v2 后 %q 不得再出现在对话锚集合", mode)
		}
	}
	if len(embeddingModes) != 1 || embeddingModes[0] != "embeddings_json" {
		t.Fatalf("embeddings_json 命中集归属不得变化：%v", embeddingModes)
	}
	if len(rerankModes) != 1 || rerankModes[0] != "rerank_json" {
		t.Fatalf("rerank_json 命中集归属不得变化：%v", rerankModes)
	}
	for _, mode := range append(chat, append(embeddingModes, append(rerankModes, mediaModes...)...)...) {
		if !knownModeSet[mode] {
			t.Fatalf("词表值 %q 未登记进 knownModeSet", mode)
		}
	}
	// v8-A 锚集收缩归属钉住：三项全部移出对话域；前两项与 realtime_session
	// 归音频域命中集，images_json 留在图像域命中集。
	for _, mode := range []string{"images_json", "audio_transcription_json", "realtime_session"} {
		if chatSet[mode] {
			t.Fatalf("v8-A 后 %q 不得再出现在对话锚集合", mode)
		}
	}
	audioSet := map[string]bool{}
	for _, mode := range audioModes {
		audioSet[mode] = true
	}
	for _, mode := range []string{"audio_transcription_json", "realtime_session"} {
		if !audioSet[mode] {
			t.Fatalf("v8-A 后 %q 必须在音频域命中集内", mode)
		}
	}
	if len(imageModes) != 1 || imageModes[0] != "images_json" {
		t.Fatalf("images_json 命中集归属不得变化：%v", imageModes)
	}
}

// TestDeriveDomainOfParsedFixtures 走「解密后 JSON → 解析 → 推导」的完整纯
// 函数链，锚定四个金集账户。
func TestDeriveDomainOfParsedFixtures(t *testing.T) {
	cases := []struct {
		name        string
		credentials string
		want        Domain
	}{
		{"gpt", `{"supported_endpoint_modes":["chat_json","chat_sse","responses_json","responses_sse"]}`, DomainChat},
		{"volcengine 混合", `{"supported_endpoint_modes":["chat_json","video_create","video_get","video_content","video_cancel","audio_speech"]}`, DomainChat},
		{"minimax 纯媒体", `{"supported_endpoint_modes":["video_create","video_get","video_content","video_cancel","audio_speech"]}`, DomainVideo},
		{"纯图像", `{"supported_endpoint_modes":["images_json"]}`, DomainImage},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reading := ParseEndpointModes(mustModesJSON(t, testCase.credentials))
			failure, bad := FailureFor("acct", reading)
			if bad {
				t.Fatalf("金集账户不得进失败清单: %v", failure)
			}
			if got := DeriveDomain(reading.Modes); got != testCase.want {
				t.Fatalf("domain = %q, want %q", got, testCase.want)
			}
		})
	}
}
