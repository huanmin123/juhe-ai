package upstreambaseurlmigration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

const testSecret = "upstream-base-url-migration-test-secret"

// sealCredential 用测试密钥封一份凭据载荷。
func sealCredential(t *testing.T, payload map[string]any) string {
	t.Helper()
	return sealWithSecret(t, testSecret, payload)
}

// sealWithSecret 用指定密钥封一份凭据载荷。
func sealWithSecret(t *testing.T, secret string, payload map[string]any) string {
	t.Helper()
	envelope, err := accountcrypto.EncryptJSON(secret, payload)
	if err != nil {
		t.Fatalf("封凭据失败: %v", err)
	}
	return envelope
}

// TestPlanFromConnections 覆盖连接行的规划计数与 manifest 明细（纯内存，零 DB）。
func TestPlanFromConnections(t *testing.T) {
	rows := planRows{
		Connections: []connectionRow{
			{ID: "conn-a", AccountID: "acc-1", Preset: "deepseek_openai_v1", BaseURL: "https://api.deepseek.com"},
			{ID: "conn-b", AccountID: "acc-1", Preset: "deepseek_openai_v1", BaseURL: "https://api.deepseek.com/v1"},
			{ID: "conn-c", AccountID: "acc-2", Preset: "generic_openai_v1", BaseURL: "https://proxy.example.com/custom"},
			{ID: "conn-d", AccountID: "acc-3", Preset: "volcengine_ark", BaseURL: ""},
		},
	}
	manifest := planFromRows(rows, testSecret, time.Unix(0, 0).UTC()).manifest
	if manifest.Connections != (Counts{Total: 4, Unchanged: 1, Rewritten: 1, Filled: 1, Review: 1}) {
		t.Fatalf("connections counts = %+v", manifest.Connections)
	}
	if len(manifest.Changes) != 2 {
		t.Fatalf("changes = %+v", manifest.Changes)
	}
	byID := map[string]Change{}
	for _, change := range manifest.Changes {
		byID[change.ID] = change
	}
	if change := byID["conn-a"]; change.NewValue != "https://api.deepseek.com/v1" || change.Reason != string(ReasonAppendVersionRoot) || change.OldValue != "https://api.deepseek.com" {
		t.Fatalf("conn-a change = %+v", change)
	}
	if change := byID["conn-d"]; change.NewValue != "https://ark.cn-beijing.volces.com/api/v3" || change.Reason != string(ReasonFillRegistryDefault) || change.OldValue != "" {
		t.Fatalf("conn-d change = %+v", change)
	}
	if len(manifest.Review) != 1 || manifest.Review[0].ID != "conn-c" || manifest.Review[0].Reason != string(ReviewNonconforming) {
		t.Fatalf("review = %+v", manifest.Review)
	}
	if manifest.BalanceImpactNote == "" || len(manifest.Notes) == 0 {
		t.Fatalf("manifest 披露缺失: note=%q notes=%d", manifest.BalanceImpactNote, len(manifest.Notes))
	}
}

// TestPlanFromAccounts 覆盖凭据信封的解封分类：改写、键缺失跳过、解密失败
// 复核、空信封跳过。
func TestPlanFromAccounts(t *testing.T) {
	sealed := sealCredential(t, map[string]any{
		"base_url": "https://api.deepseek.com",
		"api_keys": []any{"sk-1", "sk-2"},
		"api_key":  "sk-1",
	})
	noBaseURL := sealCredential(t, map[string]any{"api_key": "sk-oauth"})
	rows := planRows{
		Accounts: []accountRow{
			{ID: "acc-rewrite", Vendor: "deepseek", Protocol: "openai", Envelope: sealed},
			{ID: "acc-no-baseurl", Vendor: "gpt", Protocol: "openai", Envelope: noBaseURL},
			{ID: "acc-corrupt", Vendor: "gpt", Protocol: "openai", Envelope: "v1:broken:broken:broken"},
			{ID: "acc-empty-envelope", Vendor: "gpt", Protocol: "openai", Envelope: ""},
			{ID: "acc-wrong-secret", Vendor: "gpt", Protocol: "openai", Envelope: sealWithSecret(t, "another-secret", map[string]any{"base_url": "https://api.openai.com"})},
		},
	}
	plan := planFromRows(rows, testSecret, time.Unix(0, 0).UTC())
	manifest := plan.manifest
	if manifest.Credentials != (Counts{Total: 5, Unchanged: 2, Rewritten: 1, Review: 2}) {
		t.Fatalf("credentials counts = %+v", manifest.Credentials)
	}
	if len(plan.credentials) != 1 || plan.credentials[0].id != "acc-rewrite" {
		t.Fatalf("credential plan = %+v", plan.credentials)
	}
	// 新信封必须可解密且保留其他键、仅改 base_url。
	var payload map[string]json.RawMessage
	if err := accountcrypto.DecryptJSON(testSecret, plan.credentials[0].newEnvelope, &payload); err != nil {
		t.Fatalf("新信封解密失败: %v", err)
	}
	var keys []string
	if err := json.Unmarshal(payload["api_keys"], &keys); err != nil || len(keys) != 2 || keys[0] != "sk-1" || keys[1] != "sk-2" {
		t.Fatalf("api_keys 未保留: %v %+v", err, payload["api_keys"])
	}
	var baseURL string
	if err := json.Unmarshal(payload["base_url"], &baseURL); err != nil || baseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("base_url 未回填: %v %q", err, baseURL)
	}
	// 解密失败的两行进复核桶，不中断。
	reviewReasons := map[string]string{}
	for _, entry := range manifest.Review {
		reviewReasons[entry.ID] = entry.Reason
	}
	if reviewReasons["acc-corrupt"] != string(ReviewDecryptFailed) || reviewReasons["acc-wrong-secret"] != string(ReviewDecryptFailed) {
		t.Fatalf("review = %+v", manifest.Review)
	}
}

// TestPlanIdempotentRerun 模拟正式执行后的第二次规划：把首轮新值代回行集再
// 规划，必须零改动零凭据写计划（幂等契约）。
func TestPlanIdempotentRerun(t *testing.T) {
	first := planFromRows(planRows{
		Connections: []connectionRow{{ID: "conn-1", AccountID: "acc-1", Preset: "deepseek_openai_v1", BaseURL: "https://api.deepseek.com"}},
		Accounts:    []accountRow{{ID: "acc-1", Vendor: "deepseek", Protocol: "openai", Envelope: sealCredential(t, map[string]any{"base_url": "https://api.deepseek.com", "api_key": "sk-1"})}},
	}, testSecret, time.Unix(0, 0).UTC())
	if len(first.manifest.Changes) != 2 || len(first.credentials) != 1 {
		t.Fatalf("首轮规划 = %+v", first.manifest)
	}
	// 把首轮产物代回（连接取新值；凭据取新信封解出的载荷再封）。
	connectionNewValue := ""
	for _, change := range first.manifest.Changes {
		if change.Kind == KindConnection {
			connectionNewValue = change.NewValue
		}
	}
	var payload map[string]any
	if err := accountcrypto.DecryptJSON(testSecret, first.credentials[0].newEnvelope, &payload); err != nil {
		t.Fatalf("解密新信封失败: %v", err)
	}
	secondEnvelope := sealCredential(t, payload)
	second := planFromRows(planRows{
		Connections: []connectionRow{{ID: "conn-1", AccountID: "acc-1", Preset: "deepseek_openai_v1", BaseURL: connectionNewValue}},
		Accounts:    []accountRow{{ID: "acc-1", Vendor: "deepseek", Protocol: "openai", Envelope: secondEnvelope}},
	}, testSecret, time.Unix(0, 0).UTC())
	if len(second.manifest.Changes) != 0 || len(second.credentials) != 0 {
		t.Fatalf("重跑规划仍有改动: changes=%+v credentials=%+v", second.manifest.Changes, second.credentials)
	}
	if second.manifest.Connections.Unchanged != 1 || second.manifest.Credentials.Unchanged != 1 {
		t.Fatalf("重跑计数 = conn:%+v cred:%+v", second.manifest.Connections, second.manifest.Credentials)
	}
}

// TestVerifyReportReady 钉住 postflight 就绪判定：rewrite/fill 残留拒绝就绪，
// 复核桶不影响就绪。
func TestVerifyReportReady(t *testing.T) {
	pending := VerifyReport{PendingConnections: 1}
	if pending.Ready() {
		t.Fatal("有残留不应就绪")
	}
	reviewOnly := VerifyReport{Review: []ReviewEntry{{Kind: KindConnection, ID: "conn-x", Reason: string(ReviewNonconforming)}}}
	if !reviewOnly.Ready() {
		t.Fatal("仅复核桶应就绪")
	}
}
