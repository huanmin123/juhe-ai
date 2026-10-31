package main

// w11g 覆盖补充：Gemini 映射 URL 守卫与随机 token 稳定性。
// （chat 图片处理边界覆盖随 my-chat 会话面退役一并移除，设计 §8。）

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// TestW11GPreflightMissingPathArms 已随 2026-09-19 零配置改造移除：路径类
// env 未配置时由 loadRuntimeConfig 按 datadir 固定名表派生，preflight 不再
// 承担缺路径 fail-fast 职责；派生路径直通 preflight 的覆盖见
// storage_bootstrap_test.go TestZeroConfigStoragePreflightWithDerivedPaths。

func TestW11GGeminiMappedURLGuards(t *testing.T) {
	_ = http.MethodPost
	request := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req := gatewaypreauth.NewGatewayRequest(request)
	// 空/空白上游模型：拒绝。
	if _, err := geminiModelMappedUpstreamURL("https://w11g.example.com", req, ""); err == nil {
		t.Fatal("empty upstream model must fail")
	}
	if _, err := geminiModelMappedUpstreamURL("https://w11g.example.com", req, "  "); err == nil {
		t.Fatal("blank upstream model must fail")
	}
	// 正常：models/ 前缀剥除后拼 URL。
	url, err := geminiModelMappedUpstreamURL("https://w11g.example.com", req, "models/w11g-model")
	if err != nil || !strings.Contains(url, "w11g-model") {
		t.Fatalf("url=%q err=%v", url, err)
	}
	_ = fmt.Sprint
}
