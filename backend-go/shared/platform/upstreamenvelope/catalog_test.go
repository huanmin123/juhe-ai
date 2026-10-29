package upstreamenvelope

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// TestCatalogEntries 逐条断言登记表与三个探针包现状取证一致（§4.2：测试按
// catalog 真实支持的组合逐条覆盖，不构造虚假笛卡尔积）。
func TestCatalogEntries(t *testing.T) {
	want := []Endpoint{
		{
			Protocol: ProtocolOpenAI, Mode: ModeChatJSON, Method: http.MethodPost,
			PathTemplate: "/chat/completions", Encoding: EncodingJSON,
			BudgetField: BudgetFieldMaxTokens, Auth: AuthBearer,
		},
		{
			Protocol: ProtocolOpenAI, Mode: ModeChatSSE, Method: http.MethodPost,
			PathTemplate: "/chat/completions", Encoding: EncodingSSE,
			BudgetField: BudgetFieldMaxTokens, Auth: AuthBearer,
		},
		{
			Protocol: ProtocolOpenAI, Mode: ModeResponsesJSON, Method: http.MethodPost,
			PathTemplate: "/responses", Encoding: EncodingJSON,
			BudgetField: BudgetFieldMaxOutputTokens, Auth: AuthBearer,
		},
		{
			Protocol: ProtocolOpenAI, Mode: ModeResponsesSSE, Method: http.MethodPost,
			PathTemplate: "/responses", Encoding: EncodingSSE,
			BudgetField: BudgetFieldMaxOutputTokens, Auth: AuthBearer,
		},
		{
			Protocol: ProtocolAnthropic, Mode: ModeMessagesJSON, Method: http.MethodPost,
			PathTemplate: "/messages", Encoding: EncodingJSON,
			BudgetField: BudgetFieldMaxTokens, BudgetRequired: true, Auth: AuthAnthropicAPIKey,
			Headers: map[string]string{"anthropic-version": "2023-06-01"},
		},
		{
			Protocol: ProtocolAnthropic, Mode: ModeMessagesSSE, Method: http.MethodPost,
			PathTemplate: "/messages", Encoding: EncodingSSE,
			BudgetField: BudgetFieldMaxTokens, BudgetRequired: true, Auth: AuthAnthropicAPIKey,
			Headers: map[string]string{"anthropic-version": "2023-06-01"},
		},
		{
			Protocol: ProtocolGemini, Mode: ModeGenerateContentJSON, Method: http.MethodPost,
			PathTemplate: "/{model}:generateContent", Encoding: EncodingJSON,
			BudgetField: BudgetFieldGeminiGenerationConfig, Auth: AuthGeminiAPIKey,
		},
		{
			Protocol: ProtocolGemini, Mode: ModeGenerateContentSSE, Method: http.MethodPost,
			PathTemplate: "/{model}:streamGenerateContent?alt=sse", Encoding: EncodingSSE,
			BudgetField: BudgetFieldGeminiGenerationConfig, Auth: AuthGeminiAPIKey,
		},
		{
			Protocol: ProtocolGemini, Mode: ModeInteractionsJSON, Method: http.MethodPost,
			PathTemplate: "/interactions", Encoding: EncodingJSON,
			BudgetField: BudgetFieldNone, Auth: AuthGeminiAPIKey,
			Headers: map[string]string{"api-revision": "2026-05-20"},
		},
		{
			Protocol: ProtocolGemini, Mode: ModeInteractionsSSE, Method: http.MethodPost,
			PathTemplate: "/interactions", Encoding: EncodingSSE,
			BudgetField: BudgetFieldNone, Auth: AuthGeminiAPIKey,
			Headers: map[string]string{"api-revision": "2026-05-20"},
		},
		{
			Protocol: ProtocolOpenAI, Mode: ModeImagesJSON, Method: http.MethodPost,
			PathTemplate: "/images/generations", Encoding: EncodingJSON,
			BudgetField: BudgetFieldNone, Auth: AuthBearer,
		},
		{
			Protocol: ProtocolOpenAI, Mode: ModeAudioSpeech, Method: http.MethodPost,
			PathTemplate: "/audio/speech", Encoding: EncodingJSON,
			BudgetField: BudgetFieldNone, Auth: AuthBearer,
		},
		{
			Protocol: ProtocolOpenAI, Mode: ModeAudioTranscriptionJSON, Method: http.MethodPost,
			PathTemplate: "/audio/transcriptions", Encoding: EncodingMultipart,
			BudgetField: BudgetFieldNone, Auth: AuthBearer,
		},
	}
	entries := Entries()
	if len(entries) != len(want) {
		t.Fatalf("catalog 条目数 = %d，期望 %d", len(entries), len(want))
	}
	for index, entry := range entries {
		if !reflect.DeepEqual(entry, want[index]) {
			t.Fatalf("catalog 第 %d 条不一致：\n got  %+v\n want %+v", index, entry, want[index])
		}
		if entry.Mode != catalogOrder[index] {
			t.Fatalf("Entries 顺序与 catalogOrder 不一致：第 %d 条 = %s", index, entry.Mode)
		}
	}
}

// TestCatalogEntriesCount 钉住登记总数：新增条目必须连同契约测试一起扩展
// （§8.1），防止登记表与测试矩阵漂移。
func TestCatalogEntriesCount(t *testing.T) {
	const wantCount = 13
	if got := len(Entries()); got != wantCount {
		t.Fatalf("catalog 条目总数 = %d，期望 %d（新增端点必须先登记并扩展契约测试）", got, wantCount)
	}
}

// TestCatalogConsistency 断言登记表内部约束：SSE 条目集合、multipart 唯一
// 性、预算字段与协议的对应关系。
func TestCatalogConsistency(t *testing.T) {
	wantSSE := map[EndpointMode]bool{
		ModeChatSSE:            true,
		ModeResponsesSSE:       true,
		ModeMessagesSSE:        true,
		ModeGenerateContentSSE: true,
		ModeInteractionsSSE:    true,
	}
	multipartCount := 0
	for _, entry := range Entries() {
		if entry.Method != http.MethodPost {
			t.Errorf("%s: method = %q，当前系统探针端点全部为 POST", entry.Mode, entry.Method)
		}
		isSSE := wantSSE[entry.Mode]
		if isSSE != (entry.Encoding == EncodingSSE) {
			t.Errorf("%s: Encoding = %q 与 SSE 集合判定不一致", entry.Mode, entry.Encoding)
		}
		if entry.Encoding == EncodingMultipart {
			multipartCount++
			if entry.BudgetField != BudgetFieldNone {
				t.Errorf("%s: multipart 端点不应登记文本预算字段", entry.Mode)
			}
		}
		switch entry.Protocol {
		case ProtocolOpenAI:
			switch entry.Mode {
			case ModeChatJSON, ModeChatSSE:
				if entry.BudgetField != BudgetFieldMaxTokens {
					t.Errorf("%s: Chat 预算字段应为 max_tokens", entry.Mode)
				}
			case ModeResponsesJSON, ModeResponsesSSE:
				if entry.BudgetField != BudgetFieldMaxOutputTokens {
					t.Errorf("%s: Responses 预算字段应为 max_output_tokens", entry.Mode)
				}
			}
		case ProtocolAnthropic:
			if entry.BudgetField != BudgetFieldMaxTokens || !entry.BudgetRequired {
				t.Errorf("%s: Anthropic 预算字段为协议必填 max_tokens", entry.Mode)
			}
		case ProtocolGemini:
			switch entry.Mode {
			case ModeGenerateContentJSON, ModeGenerateContentSSE:
				if entry.BudgetField != BudgetFieldGeminiGenerationConfig {
					t.Errorf("%s: Gemini 预算字段应为 generationConfig.maxOutputTokens", entry.Mode)
				}
			case ModeInteractionsJSON, ModeInteractionsSSE:
				if entry.BudgetField != BudgetFieldNone {
					t.Errorf("%s: Interactions 契约无预算字段，应登记 none", entry.Mode)
				}
			}
		}
		if !validAuthStrategy(entry.Auth) {
			t.Errorf("%s: 未知认证策略 %q", entry.Mode, entry.Auth)
		}
	}
	if multipartCount != 1 {
		t.Fatalf("multipart 条目数 = %d，期望仅 audio_transcription_json 一条", multipartCount)
	}
}

func validAuthStrategy(strategy AuthStrategy) bool {
	switch strategy {
	case AuthBearer, AuthAnthropicAPIKey, AuthGeminiAPIKey, AuthOAuth:
		return true
	}
	return false
}

// TestLookupUnknownModeFails 断言未知 endpointMode 明确报错，不得静默兜底
// （§4.2：对不支持组合断言明确失败）。
func TestLookupUnknownModeFails(t *testing.T) {
	entry, err := Lookup("robot_json")
	if err == nil {
		t.Fatalf("未知 endpointMode 必须返回错误，实际返回条目 %+v", entry)
	}
	if !strings.Contains(err.Error(), "robot_json") {
		t.Fatalf("错误信息应包含未知 endpointMode 原文，实际: %v", err)
	}
}

// TestLookupReturnsHeaderCopy 断言返回的 Headers 是副本，调用方改动不得
// 污染共享登记表。
func TestLookupReturnsHeaderCopy(t *testing.T) {
	entry, err := Lookup(ModeMessagesJSON)
	if err != nil {
		t.Fatal(err)
	}
	entry.Headers["anthropic-version"] = "tampered"
	again, err := Lookup(ModeMessagesJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Headers["anthropic-version"]; got != "2023-06-01" {
		t.Fatalf("登记表 Headers 被调用方污染: %q", got)
	}
}

// TestResolveBudgetPolicy 断言 M1 的两类命名预算策略与 §4.3 一致。
func TestResolveBudgetPolicy(t *testing.T) {
	cases := []struct {
		name        string
		mode        EndpointMode
		probeFamily string
		adapter     Adapter
		want        BudgetPolicy
	}{
		{name: "chat 默认有界 1024", mode: ModeChatJSON, probeFamily: "protocol_basic", want: BudgetBounded(DefaultProbeOutputTokens)},
		{name: "responses 默认有界 1024", mode: ModeResponsesJSON, probeFamily: "protocol_basic", want: BudgetBounded(DefaultProbeOutputTokens)},
		{name: "anthropic 默认有界 1024", mode: ModeMessagesJSON, probeFamily: "protocol_basic", want: BudgetBounded(DefaultProbeOutputTokens)},
		{name: "gemini 默认有界 1024", mode: ModeGenerateContentJSON, probeFamily: "protocol_basic", want: BudgetBounded(DefaultProbeOutputTokens)},
		{name: "codex 适配器无预算", mode: ModeResponsesJSON, probeFamily: "protocol_basic", adapter: AdapterOpenAIOAuthCodex, want: BudgetNone},
		// §9.1-B：B 形态不继承 A 的 BudgetNone 例外，预算不覆盖，走端点默认
		// bounded（保留档案/探针族预算字段）。
		{name: "codex 兼容画像适配器保留默认有界预算", mode: ModeResponsesJSON, probeFamily: "protocol_basic", adapter: AdapterOpenAICodexCompatibility, want: BudgetBounded(DefaultProbeOutputTokens)},
		{name: "无预算字段端点恒 none", mode: ModeImagesJSON, probeFamily: "image", want: BudgetNone},
		{name: "interactions 契约无预算字段", mode: ModeInteractionsJSON, probeFamily: "protocol_basic", want: BudgetNone},
		{name: "audio 契约无预算字段", mode: ModeAudioTranscriptionJSON, probeFamily: "stt", want: BudgetNone},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got, err := ResolveBudgetPolicy(item.mode, item.probeFamily, item.adapter)
			if err != nil {
				t.Fatalf("ResolveBudgetPolicy(%s, %s, %s) 失败: %v", item.mode, item.probeFamily, item.adapter, err)
			}
			if got != item.want {
				t.Fatalf("ResolveBudgetPolicy(%s, %s, %s) = %+v，期望 %+v", item.mode, item.probeFamily, item.adapter, got, item.want)
			}
		})
	}

	if _, err := ResolveBudgetPolicy(ModeChatJSON, "protocol_basic", "unknown_adapter"); err == nil {
		t.Fatal("未登记适配器必须返回错误")
	}
	if _, err := ResolveBudgetPolicy("robot_json", "protocol_basic", AdapterNone); err == nil {
		t.Fatal("未知 endpointMode 必须返回错误")
	}
}
