package accounts

// w14b sub2api 来源全字段适配：OAuth/API Key 账户的 notes/并发/优先级/过期/
// 代理引用透传、私网 Base URL 默认放行（2026-09-19 决策）、平台/类型/凭据
// 缺失跳过臂。

import (
	"context"
	"strings"
	"testing"
)

func TestW14BSub2APIFullFieldAdaptation(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	document := map[string]any{
		"type": "sub2api-export", "version": float64(2), "exported_at": "2026-01-01T00:00:00Z",
		"proxies": []any{
			map[string]any{"proxy_key": "p1", "name": "w14b-src-proxy", "protocol": "HTTP",
				"host": "proxy.example.com", "port": float64(8080), "status": "active"},
		},
		"accounts": []any{
			// 全字段 api_key：notes/并发/优先级/过期/代理引用全部透传。
			map[string]any{
				"name": "w14b-full-key", "platform": "openai", "type": "api_key",
				"credentials": map[string]any{"api_key": "sk-src-full", "base_url": "https://api.openai.com/v1"},
				"notes":       "w14b 来源备注", "concurrency": float64(7), "priority": float64(3),
				"expires_at": "2031-01-01", "proxy_key": "p1",
			},
			// OAuth：refresh+access+account_id 全齐（凭据归一化臂）。
			map[string]any{
				"name": "w14b-src-oauth", "platform": "openai", "type": "oauth",
				"credentials": map[string]any{
					"refresh_token": "rt-w14b", "access_token": "at-w14b", "account_id": "acct-w14b",
					"expires_at": "2031-01-01T00:00:00Z",
				},
			},
			// 2026-09-19 决策：私网 Base URL 默认放行，原跳过臂转为接受。
			map[string]any{
				"name": "w14b-unsafe", "platform": "openai", "type": "api_key",
				"credentials": map[string]any{"api_key": "sk-x", "base_url": "http://127.0.0.1:9999/v1"},
			},
			// 未知平台：跳过臂。
			map[string]any{"name": "w14b-claude", "platform": "anthropic", "type": "api_key",
				"credentials": map[string]any{"api_key": "sk-x"}},
			// 未知类型：跳过臂。
			map[string]any{"name": "w14b-bogus", "platform": "openai", "type": "gemini",
				"credentials": map[string]any{"api_key": "sk-x"}},
			// 缺 credentials：跳过臂。
			map[string]any{"name": "w14b-nocred", "platform": "openai", "type": "api_key"},
		},
	}
	result, err := env.store.PreviewImport(context.Background(), document, "sub2api", ImportOptions{}, scope)
	if err != nil {
		t.Fatalf("sub2api 预览应成功：%v", err)
	}
	// 2026-09-19 决策：私网 base_url 默认放行，原被 SSRF 策略拒绝的条目
	// 现在被接受（2→3）。
	if len(result.Accounts) != 3 {
		t.Fatalf("应接受三条账户条目：%d", len(result.Accounts))
	}
	joined := ""
	for _, item := range result.Accounts {
		name := ""
		if item.Name != nil {
			name = *item.Name
		}
		joined += name + ":" + strings.Join(item.Messages, "|") + ";" + item.Action + "\n"
	}
	if !strings.Contains(joined, "w14b-full-key") || !strings.Contains(joined, "w14b-src-oauth") {
		t.Fatalf("全字段账户应被接受：%s", joined)
	}
	skipMessages := strings.Join(result.Source.Messages, "|")
	// 2026-09-19 决策：私网 base_url 默认放行后，原「缺少可用 API Key 或
	// Base URL」跳过臂不再触发（与上方 3 条接受一致）。
	if strings.Contains(skipMessages, "缺少可用 API Key 或 Base URL") {
		t.Fatalf("私网 base_url 记录应被接受而非跳过：%s", skipMessages)
	}
	if !strings.Contains(skipMessages, "只支持 OpenAI 平台账户") {
		t.Fatalf("非 OpenAI 平台应跳过：%s", skipMessages)
	}
	if !strings.Contains(skipMessages, "类型不是可导入") {
		t.Fatalf("未知类型应跳过：%s", skipMessages)
	}
	if !strings.Contains(skipMessages, "缺少 credentials") {
		t.Fatalf("缺凭据应跳过：%s", skipMessages)
	}
}
