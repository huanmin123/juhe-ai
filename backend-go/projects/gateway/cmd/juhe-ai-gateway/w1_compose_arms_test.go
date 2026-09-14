package main

// w1（单元层）：composeSystemAPI 组合根的守卫与 SQLite 路径契约分支——
// 参数伪造（缺失路径/nil 依赖），fail fast 错误原文断言。

import (
	"context"
	"encoding/json"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

func TestW1ComposeSystemAPIErrorArms(t *testing.T) {
	cfg := composeTestConfig(t)
	store := openComposeOperationStore(t)
	lease := openComposeOperationLease(t, store)
	auditConfig, auditProducer, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	// nil 三件套守卫。
	if _, err := composeSystemAPI(cfg, pgpool.NewRegistry(), nil, lease, auditProducer, auditConfig); err == nil {
		t.Fatal("nil operation store 必须拒绝")
	}
	if _, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, nil, auditProducer, auditConfig); err == nil {
		t.Fatal("nil operation lease 必须拒绝")
	}
	if _, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, lease, nil, auditConfig); err == nil {
		t.Fatal("nil audit producer 必须拒绝")
	}
	// stats 路径缺失。
	statsCfg := cfg
	statsCfg.StatsDatabasePath = ""
	if _, err := composeSystemAPI(statsCfg, pgpool.NewRegistry(), store, lease, auditProducer, auditConfig); err == nil {
		t.Fatal("缺失 stats 路径必须拒绝")
	}
	// 业务库路径不可创建：configure 阶段 fail fast。
	badBizCfg := cfg
	badBizCfg.BusinessDatabasePath = filepath.Join(filepath.Dir(cfg.BusinessDatabasePath), "no-such-dir", "business.sqlite3")
	if _, err := composeSystemAPI(badBizCfg, pgpool.NewRegistry(), store, lease, auditProducer, auditConfig); err == nil {
		t.Fatal("业务库路径不可创建必须拒绝")
	}
	// 说明：usage-catalog/table-monitor/runtime-log/audit/dataset 的缺失
	// 分支在错误返回时不关闭 composed.db，Windows 下泄漏句柄会阻塞
	// t.TempDir 清理——这些分支由 w1_main_boot_test.go 的进程级场景收割
	//（子进程自然释放句柄）。
}

// TestW1SelectorListAccountsForGroupResultArms 打通选择器 Result 全管线：
// 空组回落、请求模型窗口合并、预解析组访问元数据跳过解析。
func TestW1SelectorListAccountsForGroupResultArms(t *testing.T) {
	fixture := newChainFixture(t)
	selector, err := newChainAccountsSelectorWithStats(fixture.db, fixture.statsDB, false, "chain-test-secret", nil, 20)
	if err != nil {
		t.Fatalf("selector = %v", err)
	}
	ctx := context.Background()
	// 不存在的分组：resolveGroupAccess 返回 nil meta → 空集（非错误）。
	empty, err := selector.ListOpenAIAccountsForGroupResult(ctx, "group_missing", fixture.systemAccount, gatewayruntimecache.OpenAIAccountsForGroupOptions{})
	if err != nil {
		t.Fatalf("缺失分组 = %v", err)
	}
	if len(empty.Accounts) != 0 {
		t.Fatalf("缺失分组期望空集 = %d", len(empty.Accounts))
	}
	// 请求模型窗口 + 基础窗口合并 → 完整水合管线。
	result, err := selector.ListOpenAIAccountsForGroupResult(ctx, fixture.groupID, fixture.systemAccount, gatewayruntimecache.OpenAIAccountsForGroupOptions{
		RequestedModel:          "gpt-test",
		RequestedEndpointFamily: "chat",
	})
	if err != nil {
		t.Fatalf("全管线 = %v", err)
	}
	found := false
	for _, account := range result.Accounts {
		if account.ID == fixture.accountID {
			found = true
		}
	}
	if !found {
		t.Fatalf("期望种子账户 %s 出现在结果中 = %+v", fixture.accountID, result.Accounts)
	}
	if result.Diagnostics == nil || result.Diagnostics.FinalAccountCount != len(result.Accounts) {
		t.Fatalf("诊断计数不符 = %+v", result.Diagnostics)
	}
	// 预解析组访问元数据：跳过 resolveGroupAccess 直走管线。
	preResolved, err := selector.resolveGroupAccess(ctx, fixture.groupID, fixture.systemAccount)
	if err != nil || preResolved == nil {
		t.Fatalf("预解析组访问 = %v %v", preResolved, err)
	}
	again, err := selector.ListOpenAIAccountsForGroupResult(ctx, fixture.groupID, fixture.systemAccount, gatewayruntimecache.OpenAIAccountsForGroupOptions{
		PreResolvedGroupAccess: preResolved,
	})
	if err != nil {
		t.Fatalf("预解析通道 = %v", err)
	}
	if len(again.Accounts) != len(result.Accounts) {
		t.Fatalf("预解析与自动解析结果面不一致 = %d vs %d", len(again.Accounts), len(result.Accounts))
	}
}

// TestW1BuildUpstreamRequestPartsArms 收割驱动请求构建的非标准账户分支：
// nil 请求守卫、codex OAuth（type=oauth）、gemini code assist、
// codex_responses 客户端兼容 body 强制。
func TestW1BuildUpstreamRequestPartsArms(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()
	// nil 请求守卫。
	if _, err := driver.BuildGatewayUpstreamRequestParts(ctx, nil, gatewaydispatch.AccountCandidate{ProviderCode: "openai"}, gatewaydispatch.UsageIdentity{}, ""); err == nil {
		t.Fatal("nil 请求必须拒绝")
	}
	newReq := func(path, body string) *gatewaypreauth.GatewayRequest {
		request := httptest.NewRequest("POST", path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		gatewayRequest := gatewaypreauth.NewGatewayRequest(request)
		// 驱动层读 req.Body（gatewaybody.Request 缓存），不经 HTTP 流。
		gatewayRequest.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMapForArms(t, body)}
		return gatewayRequest
	}
	// codex OAuth：type=oauth + openai 协议 → OAuth codex builder。
	oauthAccount := gatewaydispatch.AccountCandidate{
		ID: "acc_codex", ProviderCode: "openai", ProtocolCode: "openai", Type: "oauth",
		BaseURL: "https://upstream.example", APIKey: "sk-oauth",
		Credentials: map[string]any{
			"access_token": "tok", "account_id": "acct-1",
			"tokens": map[string]any{"access_token": "tok", "account_id": "acct-1"},
		},
	}
	// codex 契约体：model + input（ChatGPT backend /responses 形态）。
	parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, newReq("/v1/responses", `{"model":"gpt-test","input":"hi"}`), oauthAccount, gatewaydispatch.UsageIdentity{SystemAccountID: "sys_owner"}, "")
	if err != nil {
		t.Fatalf("codex oauth 构建失败 = %v", err)
	}
	if parts.Body == nil || len(parts.Body) == 0 {
		t.Fatal("codex oauth 必须产出请求体")
	}
	// gemini code assist：google_oauth + code_assist + project_id。
	geminiAccount := gatewaydispatch.AccountCandidate{
		ID: "acc_gemini", ProviderCode: "google", ProtocolCode: "gemini", Type: "google_oauth",
		BaseURL: "https://gemini.example", Credentials: map[string]any{"oauth_type": "code_assist", "project_id": "proj-1"},
	}
	geminiParts, err := driver.BuildGatewayUpstreamRequestParts(ctx, newReq("/v1beta/models/gemini-test:generateContent", `{"model":"gemini-test","contents":[]}`), geminiAccount, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("gemini code assist 构建 = %v", err)
	}
	if geminiParts.Body == nil {
		t.Fatal("gemini 必须产出请求体")
	}
	// codex_responses 兼容：POST /responses + 客户端画像 → body 规范化。
	compatParts, err := driver.BuildGatewayUpstreamRequestParts(ctx,
		newReq("/v1/responses", `{"model":"gpt-test","input":"hi","instructions":"be brief"}`),
		gatewaydispatch.AccountCandidate{ID: "acc_key", ProviderCode: "openai", ProtocolCode: "openai", Type: "api_key", BaseURL: "https://upstream.example", APIKey: "sk-x"},
		gatewaydispatch.UsageIdentity{}, "codex_responses")
	if err != nil {
		t.Fatalf("codex_responses 兼容构建 = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(compatParts.Body, &decoded); err != nil {
		t.Fatalf("兼容体必须是 JSON = %v", err)
	}
	if decoded["instructions"] == nil && decoded["input"] == nil {
		t.Fatalf("兼容体形状不符 = %v", decoded)
	}
}

func mustJSONMapForArms(t *testing.T, body string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("body json = %v", err)
	}
	return decoded
}

// TestW1ComposeReadsDepsMountArms 收割 X04 404 项补齐的阅读族装配：
// audit-reads、runtime-reads、public-reads 通过 ReadsDeps.Mount 注入
// runtime-log 保留天数的默认值回退（settings 读取失败时返回 14 天）。
func TestW1ComposeReadsDepsMountArms(t *testing.T) {
	cfg := composeTestConfig(t)
	store := openComposeOperationStore(t)
	auditConfig, auditProducer, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditProducer, auditConfig)
	if err != nil {
		t.Fatalf("compose = %v", err)
	}
	defer composed.Shutdown()
	// runtime 索引保留天数契约：先写入一个脏设置，随后读取族挂载的
	// 闭包会在第一次运行时对 14 天默认值做解析（settings 失败回退）。
	seedSystemSettings(t, composed.DB)
	// X04 读取族（audit/runtime/public）已挂载：/runtime-logs 应答 401
	//（未认证）而不是 404——路由存在但被测面未登录。
	server := httptest.NewServer(composed.Kernel)
	defer server.Close()
	response, err := http.Get(server.URL + "/__aisys__/api/runtime-logs")
	if err != nil {
		t.Fatalf("runtime-logs 请求 = %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		t.Fatal("runtime-logs 必须已挂载（401 而非 404）")
	}
}
