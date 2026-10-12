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
		{"realtime 协议面", []string{"realtime_session"}, DomainChat},
		{"短转写协议面", []string{"audio_transcription_json"}, DomainChat},

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

// TestKnownModesClosure 断言已知词表与对话锚集合的闭合关系：已知 25 值 =
// 对话 15 + 媒体 10，对话集合与媒体集合不相交。
func TestKnownModesClosure(t *testing.T) {
	chat := ChatModes()
	known := KnownModes()
	if len(chat) != 15 {
		t.Fatalf("对话 modes 锚集合必须为 15 值，实际 %d", len(chat))
	}
	if len(known) != 25 {
		t.Fatalf("已知词表必须为 25 值（对话 15 + 媒体 10），实际 %d", len(known))
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
	// known 内落在对话锚集合的值应恰为 15（媒体 10 值不在对话集合内）。
	if overlap != 15 {
		t.Fatalf("known ∩ chat 应为 15 值，实际 %d", overlap)
	}
	for _, mode := range mediaModes {
		if chatSet[mode] {
			t.Fatalf("媒体值 %q 不得出现在对话锚集合内", mode)
		}
	}
	for _, mode := range append(chat, mediaModes...) {
		if !knownModeSet[mode] {
			t.Fatalf("词表值 %q 未登记进 knownModeSet", mode)
		}
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
