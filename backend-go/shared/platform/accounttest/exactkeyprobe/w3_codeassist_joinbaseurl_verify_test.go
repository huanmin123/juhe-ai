// W3 强制前置（任务书 §Code Assist 项、设计 §9.2-7）：joinBaseURL 的 // 折叠
// 语义独立验证，不能跨适配器类比。golden 钉 buildProbeRequest 对 Code
// Assist 各 base 形态的产出 URL，与信封寻址口径（UpstreamURL 与 Build 共用
// 同一 buildUpstreamURL 实现）同输入对比；Code Assist 包装后的请求路径恒为
// wrapGeminiCodeAssist 返回的 /v1internal:streamGenerateContent?alt=sse。
//
// 2026-10-12 验证与裁决（设计 §9.2-7）：迁移前 joinBaseURL 与 Build 口径在
// 生产冻结 base、空路径、/v1beta 根、尾斜杠四类输入逐字一致；双斜杠 base
// 两类输入不一致——旧 joinBaseURL 保留 //（如 https://api.test//prefix 的
// URL path 原样保留双斜杠），Build 折叠为单斜杠。生产 Code Assist base 由
// directBaseURL 冻结为 cloudcode-pa.googleapis.com（// 输入不可达），普通
// gemini 面折叠口径 M3 起已上线，裁决收敛为折叠口径（joinBaseURL 删除）。
// 本 golden 全部断言折叠口径：接线后 buildProbeRequest 即信封单一 URL 产出，
// 双斜杠臂同时记录旧 join 语义备查。
package exactkeyprobe

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
)

func TestW3CodeAssistJoinBaseURLSlashSemantics(t *testing.T) {
	cases := []struct {
		name string
		base string
		// legacySlashJoin 记录该输入在旧 joinBaseURL 下的非折叠产出（接线
		// 前实证），折叠裁决下仅作文档对照，不参与断言。
		legacySlashJoin string
	}{
		{name: "生产冻结base", base: "https://cloudcode-pa.googleapis.com"},
		{name: "空路径base", base: "https://api.test"},
		{name: "v1beta根base", base: "https://api.test/v1beta"},
		{name: "尾斜杠base", base: "https://api.test/"},
		{
			name:            "双斜杠前缀base",
			base:            "https://api.test//prefix",
			legacySlashJoin: "https://api.test//prefix/v1internal:streamGenerateContent?alt=sse（旧 joinBaseURL 保留 //，折叠裁决后不再产出）",
		},
		{
			name:            "嵌套双斜杠base",
			base:            "https://api.test/prefix//nested",
			legacySlashJoin: "https://api.test/prefix//nested/v1internal:streamGenerateContent?alt=sse（旧 joinBaseURL 保留 //，折叠裁决后不再产出）",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, err := url.Parse(tc.base)
			if err != nil {
				t.Fatal(err)
			}
			input := testInput(tc.base, "generate_content_json")
			input.Provider = "gemini"
			input.ProtocolProfileID = "profile_gemini_native_v1beta"
			input.ProtocolCode = "gemini"
			input.ProtocolVersion = "v1beta"
			input.HealthModel = "gemini-w3"
			input.Type = "google_oauth"
			input.OAuthType = "code_assist"
			input.OAuthProjectID = "w3-project"
			expiresAt := time.Now().UTC().Add(time.Hour)
			input.OAuthExpiresAt = &expiresAt
			request, err := buildProbeRequest(context.Background(), base, input, "w3-token")
			if err != nil {
				t.Fatalf("buildProbeRequest 构造失败: %v", err)
			}
			// wrapGeminiCodeAssist 返回的固定请求路径（Build 包装后交给
			// buildUpstreamURL 的同一输入）。
			const wrappedPath = "/v1internal:streamGenerateContent?alt=sse"
			envelopeURL, err := upstreamenvelope.UpstreamURL(tc.base, upstreamenvelope.ModeGenerateContentJSON, "profile_gemini_native_v1beta", upstreamenvelope.AdapterGeminiCodeAssist, wrappedPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("base=%q buildProbeRequest=%q envelope=%q legacy=%q", tc.base, request.URL.String(), envelopeURL, tc.legacySlashJoin)
			if request.URL.String() != envelopeURL {
				t.Fatalf("Code Assist URL 与信封折叠口径不一致：%q != %q", request.URL.String(), envelopeURL)
			}
		})
	}
}
