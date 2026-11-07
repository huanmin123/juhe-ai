package accounts

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// 产品契约（2026-11-07 用户裁决）：一个账户只能有一个上游连接（一个 Base URL），
// 多个 API Key 放在同一凭据 JSON 中；多连接不是待实现功能，而是明确不支持。
func TestResolveConnectionSetRejectsMultipleConnections(t *testing.T) {
	_, err := resolveConnectionSet("chat", "deepseek", "api_key", "", []accountscore.ConnectionInput{
		{ConnectionPresetCode: "deepseek_openai_v1"},
		{ConnectionPresetCode: "deepseek_anthropic_v1"},
	})
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("多连接必须是 ValidationError，got %v", err)
	}
	if !strings.Contains(validation.Message, "只支持一个上游连接") ||
		!strings.Contains(validation.Message, "多个 API Key") {
		t.Fatalf("多连接错误文案必须说明单连接与多 Key 口径：%q", validation.Message)
	}
}

func TestCreateRejectsMultipleConnections(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	single := b1ConnectionsJSON("chat_json", "chat_sse")
	multi := "[" + strings.TrimSuffix(strings.TrimPrefix(single, "["), "]") +
		"," + strings.TrimSuffix(strings.TrimPrefix(single, "["), "]") + "]"
	payload := strings.Replace(createPayload("多连接拒绝"), `"connections":`+single, `"connections":`+multi, 1)
	if payload == createPayload("多连接拒绝") {
		t.Fatal("测试夹具未替换 connections")
	}
	code, response := env.do(t, http.MethodPost, "/__aisys__/api/accounts", payload)
	if code != http.StatusBadRequest {
		t.Fatalf("多连接创建应 400：%d %v", code, response)
	}
	if message, _ := response["message"].(string); !strings.Contains(message, "只支持一个上游连接") {
		t.Fatalf("多连接错误文案不一致：%q", message)
	}
}

func TestPatchRejectsMultipleConnections(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	single := b1ConnectionsJSON("chat_json", "chat_sse")
	payload := createPayload("多连接PATCH拒绝")
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts", payload)
	if code != http.StatusCreated {
		t.Fatalf("创建单连接账户应 201：%d %v", code, created)
	}
	data := dataMap(t, created)
	id, _ := data["id"].(string)
	revision, _ := data["configRevision"].(float64)
	if id == "" || revision == 0 {
		t.Fatalf("创建响应缺少 id/configRevision：%v", data)
	}
	multi := "[" + strings.TrimSuffix(strings.TrimPrefix(single, "["), "]") +
		"," + strings.TrimSuffix(strings.TrimPrefix(single, "["), "]") + "]"
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":`+strconv.FormatFloat(revision, 'f', 0, 64)+`,"connections":`+multi+`}`)
	if code != http.StatusBadRequest {
		t.Fatalf("多连接 PATCH 应 400：%d %v", code, patched)
	}
	if message, _ := patched["message"].(string); !strings.Contains(message, "只支持一个上游连接") {
		t.Fatalf("多连接 PATCH 错误文案不一致：%q", message)
	}
}
