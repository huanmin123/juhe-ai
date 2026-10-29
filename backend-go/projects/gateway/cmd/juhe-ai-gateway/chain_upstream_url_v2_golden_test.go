package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayanthropic"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
)

// TestChainUpstreamURLV2Golden 钉上游 Base URL 版本根语义 v2 的直拼 golden：
// 最终上游 URL = TrimRight(base_url,"/") + opPath（opPath 以 / 开头、不含
// 版本前缀），任何链路不再对 base/opPath 补插、剥离或去重版本段（R1/R2/R3）。
// lane 覆盖：volcengine chat（头号验收用例）、glm chat（旧行为多拼 /v1 的
// 行为修复）、qwen chat（/compatible-mode/v1 厂商前缀，R6）、qwen 视频
//（/api/v1 双根特例全路径不变）、deepseek anthropic、gemini 版本根。
func TestChainUpstreamURLV2Golden(t *testing.T) {
	driver := newChainProviderDriver()
	chatRequest := func(t *testing.T, target string) *gatewaypreauth.GatewayRequest {
		t.Helper()
		body := `{"model":"m","input":"hi"}`
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		req := gatewaypreauth.NewGatewayRequest(request)
		req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
		return req
	}

	t.Run("chat lanes via driver URL 构建", func(t *testing.T) {
		cases := []struct {
			name    string
			account gatewaydispatch.AccountCandidate
			target  string
			want    string
		}{
			{
				// 头号验收用例：volcengine 版本根 /api/v3 + 入站 /v1 剥离直拼。
				name: "volcengine /api/v3 版本根直拼",
				account: gatewaydispatch.AccountCandidate{
					ID: "acc_volc", VendorCode: "volcengine", ProtocolCode: "openai",
					Type: "api_key", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", APIKey: "ak",
				},
				target: "/v1/chat/completions",
				want:   "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
			},
			{
				// 行为修复：glm 版本根 /api/paas/v4 + 入站 /v1 剥离直拼；旧行为
				// 对非 /v1 结尾 base 多拼一层 /v1（/api/paas/v4/v1/chat/completions）。
				name: "glm /api/paas/v4 版本根直拼（旧行为多拼 /v1，此为行为修复）",
				account: gatewaydispatch.AccountCandidate{
					ID: "acc_glm", VendorCode: "glm", ProtocolCode: "openai",
					Type: "api_key", BaseURL: "https://open.bigmodel.cn/api/paas/v4", APIKey: "ak",
				},
				target: "/v1/chat/completions",
				want:   "https://open.bigmodel.cn/api/paas/v4/chat/completions",
			},
			{
				// R6：qwen chat 通用 opPath 恒拼 /compatible-mode/v1 前缀
				//（官方 origin 形态 base）。
				name: "qwen origin + compatible-mode/v1 前缀",
				account: gatewaydispatch.AccountCandidate{
					ID: "acc_qwen", VendorCode: "qwen", ProtocolCode: "openai",
					Type: "api_key", BaseURL: "https://dashscope.aliyuncs.com", APIKey: "ak",
				},
				target: "/v1/chat/completions",
				want:   "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), tc.account, chatRequest(t, tc.target))
				if err != nil {
					t.Fatalf("build urls: %v", err)
				}
				if len(urls) != 1 || urls[0] != tc.want {
					t.Fatalf("urls = %#v, want [%s]", urls, tc.want)
				}
			})
		}
	})

	t.Run("media 任务面 opPath 直拼", func(t *testing.T) {
		// qwen 视频：/api/v1/services 全路径 opPath 不变（双根特例 base=origin），
		// 与 origin base 直拼。
		plan, err := gatewaymedia.VideoAdapterForProvider("qwen").Create(context.Background(), gatewaymedia.VideoCreateInput{
			Params: gatewaymedia.NormalizedVideoParams{Model: "wan2.2-t2v-plus", Prompt: "p"},
		})
		if err != nil {
			t.Fatalf("qwen adapter create: %v", err)
		}
		joined, err := upstreamenvelope.JoinUpstreamURL("https://dashscope.aliyuncs.com", plan.Path)
		if err != nil {
			t.Fatalf("join: %v", err)
		}
		if joined != "https://dashscope.aliyuncs.com/api/v1/services/aigc/video-generation/video-synthesis" {
			t.Fatalf("qwen video url = %s", joined)
		}
		// glm 视频：opPath 不含版本段，/api/paas/v4 由版本根 base 承载。
		plan, err = gatewaymedia.VideoAdapterForProvider("glm").Create(context.Background(), gatewaymedia.VideoCreateInput{
			Params: gatewaymedia.NormalizedVideoParams{Model: "cogvideox", Prompt: "p"},
		})
		if err != nil {
			t.Fatalf("glm adapter create: %v", err)
		}
		joined, err = upstreamenvelope.JoinUpstreamURL("https://open.bigmodel.cn/api/paas/v4", plan.Path)
		if err != nil {
			t.Fatalf("join: %v", err)
		}
		if joined != "https://open.bigmodel.cn/api/paas/v4/videos/generations" {
			t.Fatalf("glm video url = %s", joined)
		}
	})

	t.Run("anthropic 版本根直拼", func(t *testing.T) {
		// deepseek anthropic 版本根 /anthropic/v1 + 入站 /v1 剥离直拼。
		built, err := gatewayanthropic.BuildUpstreamURL("https://api.deepseek.com/anthropic/v1", "/v1/messages")
		if err != nil {
			t.Fatalf("anthropic build: %v", err)
		}
		if built != "https://api.deepseek.com/anthropic/v1/messages" {
			t.Fatalf("anthropic url = %s", built)
		}
	})

	t.Run("gemini 版本根直拼", func(t *testing.T) {
		// gemini 版本根 /v1beta + 入站 /v1beta 剥离直拼，最终形态不变。
		built, err := gatewaygemini.BuildUpstreamURL("https://generativelanguage.googleapis.com/v1beta", "/v1beta/models/x:generateContent", false)
		if err != nil {
			t.Fatalf("gemini build: %v", err)
		}
		if built != "https://generativelanguage.googleapis.com/v1beta/models/x:generateContent" {
			t.Fatalf("gemini url = %s", built)
		}
	})
}
