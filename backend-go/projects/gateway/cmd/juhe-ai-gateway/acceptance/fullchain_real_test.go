// 全链路验收 Stage 3：真实生产账户链（计划 §3 S1/S2/M1/M2）。
//
// 门控：JUHE_AI_E2E_FULLCHAIN=1 + JUHE_AI_E2E_REAL=1 + JUHE_AI_E2E_PROD_SECRET
// （生产 JUHE_AI_SECRET，由运行命令经授权 kubeconfig 注入进程 env；缺失时
// skip）。生产侧全程只读：`default_transaction_read_only=on` +
// `statement_timeout=30000`，仅执行 SELECT。
//
// 脱敏红线：生产 secret 与解密出的明文凭据只存在于测试进程内存；任何
// t.Log/错误消息/报告只允许 id 尾段/provider/模型名，不含凭据、连接串与
// 上游 base_url 明文。
//
// 成本纪律：真号调用 max_tokens ≤ 64、prompt 固定短句；单场景单次调用，
// 失败自动重试一次（上游偶发），仍失败记 FAIL 并保留脱敏响应摘要。
package acceptance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
	"github.com/jackc/pgx/v5"
)

// fullchainProdCredentialsPath 是生产 PG 凭据的 k8s Secret 清单（data 键
// postgresql_username / postgresql_password，base64）。
const fullchainProdCredentialsPath = "F:/k8s/ops/inventory/runtime/platform-credentials.secret.yaml"

// fullchainRealCandidates 是候选 20 个生产账户 id（provider 分布
// gpt5/openai4/anthropic4/glm3/deepseek2/xai2，全 api_key 型）。
var fullchainRealCandidates = []string{
	// gpt
	"acc_1788443809796_27a641e2", "acc_1789561829362_36a232aa", "acc_1787852331075_b20faeb6",
	"acc_1789001202983_0d53ec55", "acc_1789390589632_859e6d1e",
	// openai
	"acc_1789484462842_7ac203bf", "acc_1789483872417_3e405198", "acc_1785730004537_6235fa2b",
	"acc_1783576907911_6402098e",
	// anthropic
	"acc_1787150386386_cb0a173d", "acc_1786265377633_e21a22ee", "acc_1783813611107_ae56197c",
	"acc_1786863859668_7021e00a",
	// glm
	"acc_1789045270072_db574566", "acc_1788778970984_b7f4119b", "acc_1788836033821_896d5f49",
	// deepseek
	"acc_1786497597478_a8a7ad29", "acc_1787835840657_385f4dd9",
	// xai
	"acc_1786681028188_cb4bd10d", "acc_1787562734802_f117670e",
}

// fullchainProdAccount 是一个解密后的生产候选账户（仅内存）。
type fullchainProdAccount struct {
	ID                   string
	ProviderCode         string
	ProfileID            string
	HealthModel          string
	HealthMode           string
	Status               string
	DecryptedCredentials map[string]any
}

// requireRealGateWithSecret 在 REAL 门控之上再要求生产 secret。
func requireRealGateWithSecret(t *testing.T) string {
	t.Helper()
	requireRealGate(t)
	secret := os.Getenv("JUHE_AI_E2E_PROD_SECRET")
	if strings.TrimSpace(secret) == "" {
		t.Skip("真实账户场景需要 JUHE_AI_E2E_PROD_SECRET（由运行命令经授权 kubeconfig 注入）")
	}
	return secret
}

// prodPostgresCredentials 从授权 Secret 清单解码生产 PG 登录角色（仅内存）。
func prodPostgresCredentials(t *testing.T) (string, string) {
	t.Helper()
	raw, err := os.ReadFile(fullchainProdCredentialsPath)
	if err != nil {
		t.Skipf("生产凭据清单不可读（%s）: %v", fullchainProdCredentialsPath, err)
	}
	text := string(raw)
	decode := func(key string) string {
		marker := "  " + key + ": "
		idx := strings.Index(text, marker)
		if idx < 0 {
			t.Fatalf("生产凭据清单缺少键 %s", key)
		}
		rest := text[idx+len(marker):]
		if end := strings.IndexAny(rest, "\r\n"); end >= 0 {
			rest = rest[:end]
		}
		decoded, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
		if decodeErr != nil {
			t.Fatalf("解码 %s 失败", key)
		}
		return string(decoded)
	}
	return decode("postgresql_username"), decode("postgresql_password")
}

// loadProdAccounts 只读拉取并解密候选账户（SELECT ONLY）。
func loadProdAccounts(t *testing.T, secret string) []fullchainProdAccount {
	t.Helper()
	user, password := prodPostgresCredentials(t)
	dsn := fmt.Sprintf("postgres://%s:%s@192.168.1.203:5432/juhe_ai_prod?default_transaction_read_only=on&statement_timeout=30000",
		user, password)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("生产 PG 不可达（只读会话建立失败）: %v", err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT id, provider_code, provider_protocol_profile_id,
			COALESCE(health_check_model, ''), COALESCE(health_check_endpoint_mode, ''), status, credentials_encrypted
		FROM juhe_business.accounts
		WHERE id = ANY($1) AND type = 'api_key' AND deleted_at IS NULL`, fullchainRealCandidates)
	if err != nil {
		t.Skipf("生产账户只读查询失败: %v", err)
	}
	defer rows.Close()
	out := []fullchainProdAccount{}
	for rows.Next() {
		var account fullchainProdAccount
		var encrypted string
		if err := rows.Scan(&account.ID, &account.ProviderCode, &account.ProfileID,
			&account.HealthModel, &account.HealthMode, &account.Status, &encrypted); err != nil {
			t.Fatalf("scan prod account: %v", err)
		}
		if err := accountcrypto.DecryptJSON(secret, encrypted, &account.DecryptedCredentials); err != nil {
			// 解密失败只报账户 id（生产 secret 不匹配或信封异常），不落凭据。
			t.Logf("账户 %s（%s）凭据解密失败，跳过导入", account.ID, account.ProviderCode)
			continue
		}
		out = append(out, account)
	}
	if err := rows.Err(); err != nil {
		t.Skipf("生产账户行遍历失败: %v", err)
	}
	t.Logf("生产候选账户解密成功 %d/%d（凭据明细不落日志）", len(out), len(fullchainRealCandidates))
	return out
}

// fullchainRealAccount 是导入到本地隔离实例的真号（本地 secret 重加密）。
type fullchainRealAccount struct {
	ID           string
	ProviderCode string
	Model        string
	HealthMode   string
}

// fullchainRealFixture 是 REAL 场景共享夹具：真号导入结果 + 隔离实例。
type fullchainRealFixture struct {
	f             *fullchainFixture
	byProvider    map[string][]fullchainRealAccount
	rawByProvider map[string][]fullchainProdAccount
	imported      int
	skipped       int
}

// startRealFixture 组装 REAL 共享夹具：加载生产候选、逐个经管理面导入
// （本地 secret 重加密；导入分组为每 provider 一个，场景再用专用分组改绑）。
func startRealFixture(t *testing.T, prodSecret string) *fullchainRealFixture {
	t.Helper()
	prodAccounts := loadProdAccounts(t, prodSecret)
	if len(prodAccounts) == 0 {
		t.Skip("没有可导入的生产候选账户（解密全失败或库为空）")
	}
	f := startFullchainFixture(t)
	real := &fullchainRealFixture{f: f, byProvider: map[string][]fullchainRealAccount{}, rawByProvider: map[string][]fullchainProdAccount{}}
	groupByProvider := map[string]string{}
	for _, prodAccount := range prodAccounts {
		// 请求模型：生产侧健康检查模型（该上游确认支持的模型，成本可控）。
		if prodAccount.HealthModel == "" {
			prodAccount.HealthModel = "gpt-5.6-sol"
		}
		real.rawByProvider[prodAccount.ProviderCode] = append(real.rawByProvider[prodAccount.ProviderCode], prodAccount)
		groupID, ok := groupByProvider[prodAccount.ProviderCode]
		if !ok {
			groupID = f.createGroupWithProvider(fmt.Sprintf("全链路-真号导入组-%s", prodAccount.ProviderCode), prodAccount.ProviderCode)
			groupByProvider[prodAccount.ProviderCode] = groupID
		}
		credentials := map[string]any{}
		for key, value := range prodAccount.DecryptedCredentials {
			credentials[key] = value
		}
		payload := map[string]any{
			"providerCode":              prodAccount.ProviderCode,
			"providerProtocolProfileId": prodAccount.ProfileID,
			"name":                      fmt.Sprintf("全链路-真号-%s-%s", prodAccount.ProviderCode, prodAccount.ID[len(prodAccount.ID)-8:]),
			"type":                      "api_key",
			"credentials":               credentials,
			"supportedModels":           []string{prodAccount.HealthModel, fullchainProbeModel},
			"healthCheckModel":          fullchainProbeModel,
			"status":                    "active",
			"groupId":                   groupID,
		}
		_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", payload, 0)
		createdData := data(created)
		if createdData == nil || str(createdData["id"]) == "" {
			real.skipped++
			// 400 消息只含字段名/校验文案（无凭据值），可安全留证。
			t.Logf("账户 %s（%s）导入被拒：%s", prodAccount.ID, prodAccount.ProviderCode, str(created["message"]))
			continue
		}
		real.byProvider[prodAccount.ProviderCode] = append(real.byProvider[prodAccount.ProviderCode], fullchainRealAccount{
			ID:           str(createdData["id"]),
			ProviderCode: prodAccount.ProviderCode,
			Model:        prodAccount.HealthModel,
			HealthMode:   prodAccount.HealthMode,
		})
		real.imported++
	}
	t.Logf("真号导入完成：成功 %d，跳过 %d", real.imported, real.skipped)
	if real.imported == 0 {
		t.Skip("所有生产候选账户导入失败（凭据形状与本地写侧契约不匹配）")
	}
	return real
}

// pick 返回某 provider 的第 n 个导入账户。
func (r *fullchainRealFixture) pick(providerCode string, nth int) (fullchainRealAccount, bool) {
	list := r.byProvider[providerCode]
	if nth >= len(list) {
		return fullchainRealAccount{}, false
	}
	return list[nth], true
}

// realRoute 为一个真号建立专用分组（改绑该账户）→ 策略 → API Key 拓扑，
// 保证场景内真号路由确定性（导入分组可能含多个同 provider 账户）。
func (r *fullchainRealFixture) realRoute(tag, providerCode string, account fullchainRealAccount) fullchainRoute {
	r.f.t.Helper()
	groupID := r.f.createGroupWithProvider(fmt.Sprintf("全链路-%s-组", tag), providerCode)
	r.f.rebindAccountGroup(account.ID, groupID, 100)
	strategyID := r.f.createStrategy(fmt.Sprintf("全链路-%s-策略", tag), "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := r.f.createAPIKey(fmt.Sprintf("全链路-%s-Key", tag), strategyID)
	return fullchainRoute{
		groupIDs:   []string{groupID},
		accountIDs: []string{account.ID},
		apiKey:     apiKey,
		apiKeyID:   r.f.apiKeyIDByName(fmt.Sprintf("全链路-%s-Key", tag)),
	}
}

// realRouteImported 通过导入写路径导入一个真号并建立专用分组→策略→Key 拓扑
// （codex 客户端场景 C1/C5 必须走导入：创建路径硬编码 openai_standard，
// 见 E2E-FINDING #7）。credentialExtra/payloadExtra 透传 importRealAccountWith。
func (r *fullchainRealFixture) realRouteImported(tag, providerCode string, prodAccount fullchainProdAccount,
	credentialExtra, payloadExtra map[string]any,
) (fullchainRoute, fullchainRealAccount, bool) {
	r.f.t.Helper()
	groupID := r.f.createGroupWithProvider(fmt.Sprintf("全链路-%s-组", tag), providerCode)
	account, ok := r.importRealAccountWith(tag, prodAccount, groupID, credentialExtra, payloadExtra)
	if !ok {
		return fullchainRoute{}, fullchainRealAccount{}, false
	}
	strategyID := r.f.createStrategy(fmt.Sprintf("全链路-%s-策略", tag), "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := r.f.createAPIKey(fmt.Sprintf("全链路-%s-Key", tag), strategyID)
	return fullchainRoute{
		groupIDs:   []string{groupID},
		accountIDs: []string{account.ID},
		apiKey:     apiKey,
		apiKeyID:   r.f.apiKeyIDByName(fmt.Sprintf("全链路-%s-Key", tag)),
	}, account, true
}

// pickRaw 返回某 provider 的第 n 个原始生产候选（含解密凭据，仅内存）。
func (r *fullchainRealFixture) pickRaw(providerCode string, nth int) (fullchainProdAccount, bool) {
	list := r.rawByProvider[providerCode]
	if nth >= len(list) {
		return fullchainProdAccount{}, false
	}
	return list[nth], true
}

// importRealAccount 通过管理面导入 API（/accounts/import/preview+confirm）
// 导入一个真号到指定分组。导入 confirm 复用 Store.Create，createInTx 按
// deriveOpenAIAccountClientCompatibility 落列（gpt api_key → codex_responses，
// E2E-FINDING #7 修复后的写侧语义）。
func (r *fullchainRealFixture) importRealAccount(tag string, prodAccount fullchainProdAccount, groupID string) (fullchainRealAccount, bool) {
	return r.importRealAccountWith(tag, prodAccount, groupID, nil, nil)
}

// importRealAccountWith 在导入文档上叠加 credentials 覆盖键（如 mock 的
// base_url/api_key）与账户字段覆盖（如 priority），供 mock 账户复用导入
// 写路径派生 client_compatibility=codex_responses。
func (r *fullchainRealFixture) importRealAccountWith(tag string, prodAccount fullchainProdAccount, groupID string,
	credentialExtra, payloadExtra map[string]any,
) (fullchainRealAccount, bool) {
	r.f.t.Helper()
	model := prodAccount.HealthModel
	if model == "" {
		model = "gpt-5.6-sol"
	}
	credentials := map[string]any{}
	for key, value := range prodAccount.DecryptedCredentials {
		credentials[key] = value
	}
	for key, value := range credentialExtra {
		credentials[key] = value
	}
	entry := map[string]any{
		"name":                      fmt.Sprintf("全链路-真号导入-%s-%s", tag, prodAccount.ID[len(prodAccount.ID)-8:]),
		"providerCode":              prodAccount.ProviderCode,
		"providerProtocolProfileId": prodAccount.ProfileID,
		"type":                      "api_key",
		"status":                    "active",
		"credentials":               credentials,
		"supportedModels":           []any{model, fullchainProbeModel},
		"healthCheckModel":          fullchainProbeModel,
		"healthCheckEndpointMode":   "chat_json",
		"groupId":                   groupID,
	}
	for key, value := range payloadExtra {
		entry[key] = value
	}
	document := map[string]any{
		"type":     "juhe-ai-account-import",
		"version":  1,
		"accounts": []any{entry},
	}
	body := map[string]any{"data": document}
	_, preview := r.f.admin.do(http.MethodPost, "/__aisys__/api/accounts/import/preview", body, wantStatus(http.StatusOK))
	items, _ := data(preview)["accounts"].([]any)
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok && str(item["action"]) == "failed" {
			r.f.t.Logf("导入预览失败（%s/%s）：%v", prodAccount.ProviderCode, prodAccount.ID, item["messages"])
			return fullchainRealAccount{}, false
		}
	}
	_, confirmed := r.f.admin.do(http.MethodPost, "/__aisys__/api/accounts/import/confirm", body, wantStatus(http.StatusOK))
	confirmItems, _ := data(confirmed)["accounts"].([]any)
	for _, raw := range confirmItems {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if accountID := str(item["accountId"]); accountID != "" {
			return fullchainRealAccount{
				ID:           accountID,
				ProviderCode: prodAccount.ProviderCode,
				Model:        model,
				HealthMode:   prodAccount.HealthMode,
			}, true
		}
	}
	r.f.t.Logf("导入确认未返回 accountId（%s/%s）", prodAccount.ProviderCode, prodAccount.ID)
	return fullchainRealAccount{}, false
}

// profileForProvider 返回 OpenAI 兼容 chat 场景的 seed profile id（mock
// 账户按同 provider 建立时使用）。
func profileForProvider(providerCode string) string {
	switch providerCode {
	case "gpt":
		return "profile_gpt_openai_v1"
	case "openai":
		return "profile_openai_openai_v1"
	case "deepseek":
		return "profile_deepseek_openai_v1"
	case "glm":
		return "profile_glm_general_openai_v1"
	case "xai":
		return "profile_xai_openai_v1"
	case "anthropic":
		return "profile_anthropic_anthropic_v1"
	}
	return "profile_gpt_openai_v1"
}

// realChatPayload 构造受控成本的非流式 chat 请求。
func realChatPayload(model string) string {
	return fmt.Sprintf(`{"model":"%s","max_tokens":64,"messages":[{"role":"user","content":"回复OK"}]}`, model)
}

// assertRealChat 断言一次真号 chat 调用（失败自动重试一次）。
func assertRealChat(t *testing.T, f *fullchainFixture, apiKey, model string) fullchainChatResponse {
	t.Helper()
	var last fullchainChatResponse
	for attempt := 0; attempt < 2; attempt++ {
		last = f.chat(apiKey, realChatPayload(model))
		if last.Status == http.StatusOK && strings.Contains(last.Body, "choices") && strings.TrimSpace(realContentOf(last.Body)) != "" {
			return last
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("real chat failed after retry (model=%s): status=%d body=%s", model, last.Status, maskRealBody(last.Body))
	return last
}

// realContentOf 从 chat completions 响应提取文本；reasoning 模型把预算花在
// reasoning_content 时以其作为非空回复证据。
func realContentOf(body string) string {
	var decoded struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil || len(decoded.Choices) == 0 {
		return ""
	}
	if strings.TrimSpace(decoded.Choices[0].Message.Content) != "" {
		return decoded.Choices[0].Message.Content
	}
	return decoded.Choices[0].Message.ReasoningContent
}

// maskRealBody 脱敏真号响应摘要：截断（上游错误细节可能含账户信息，不透传全文）。
func maskRealBody(body string) string {
	if len(body) > 300 {
		body = body[:300]
	}
	return body
}

// TestFullchainRealChain Stage 3 全链真号场景（S1/S2/M1/M2）。
func TestFullchainRealChain(t *testing.T) {
	prodSecret := requireRealGateWithSecret(t)
	r := startRealFixture(t, prodSecret)
	f := r.f

	t.Run("S1_real_chat_per_provider", func(t *testing.T) {
		// 每个 chat provider 一次非流式真号调用；gpt 另加一次流式。
		// 每 provider 一个子测试：单 provider 失败只影响自身。
		for _, providerCode := range []string{"openai", "glm", "deepseek", "xai", "gpt"} {
			account, ok := r.pick(providerCode, 0)
			if !ok {
				t.Logf("S1 %s：无可用导入账户，跳过该 provider", providerCode)
				continue
			}
			t.Run("provider_"+providerCode, func(t *testing.T) {
				route := r.realRoute("S1-"+providerCode, providerCode, account)
				response := assertRealChat(t, f, route.apiKey, account.Model)
				t.Logf("S1 %s（…%s）：200，回复长度 %d，模型 %s", providerCode, account.ID[len(account.ID)-8:], len(realContentOf(response.Body)), account.Model)

				// 审计归因：成功请求的全部 attempt 都归属该真号且最终上游
				// 200（同账户瞬态重试/Key 轮换时 attempt 数可 >1）。
				detail := f.waitAuditLogDetail(route.apiKeyID, func(log fullchainAuditLog) bool {
					if !log.Success || len(log.Attempts) == 0 {
						return false
					}
					for _, attempt := range log.Attempts {
						if str(attempt["accountId"]) != account.ID {
							return false
						}
					}
					last := log.Attempts[len(log.Attempts)-1]
					return attemptStatus(last) != nil && *attemptStatus(last) == 200
				}, "success attributed to the real account")
				t.Logf("S1 %s 归因：attempt 数 %d，全部命中真号，最终上游 200",
					providerCode, len(detail.Attempts))
				if providerCode == "gpt" {
					// gpt 流式一次（成本同纪律；用同一真号）。
					streamRoute := r.realRoute("S1-gpt-stream", "gpt", account)
					streamResponse := f.chat(streamRoute.apiKey, fmt.Sprintf(`{"model":"%s","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"回复OK"}]}`, account.Model))
					if streamResponse.Status != http.StatusOK || !strings.Contains(streamResponse.ContentType, "text/event-stream") {
						t.Fatalf("S1 gpt stream status=%d contentType=%s body=%s", streamResponse.Status, streamResponse.ContentType, maskRealBody(streamResponse.Body))
					}
					if !strings.Contains(streamResponse.Body, "data:") {
						t.Fatalf("S1 gpt stream missing SSE frames: %s", maskRealBody(streamResponse.Body))
					}
					t.Logf("S1 gpt stream（…%s）：200 SSE", account.ID[len(account.ID)-8:])
				}
			})
		}
	})

	t.Run("S2_real_messages_anthropic", func(t *testing.T) {
		// E2E-FINDING #6（已修复）：/v1 协议门（chain_v1.go gatewayIsProtocolRequest）
		// 是三协议并集，POST /v1/messages 进入 anthropic native 分发链。
		// 完整断言：200 + 响应含 content 字段（真号回复）。
		account, ok := r.pick("anthropic", 0)
		if !ok {
			t.Skip("S2：无可用 anthropic 导入账户")
		}
		route := r.realRoute("S2", "anthropic", account)
		request, _ := http.NewRequest(http.MethodPost, f.gw.baseURL+"/v1/messages",
			strings.NewReader(fmt.Sprintf(`{"model":"%s","max_tokens":64,"messages":[{"role":"user","content":"回复OK"}]}`, account.Model)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+route.apiKey)
		response := f.doRaw(request)
		var last fullchainChatResponse
		last = response
		for attempt := 0; attempt < 2 && !(last.Status == http.StatusOK && strings.Contains(last.Body, "content")); attempt++ {
			retry, _ := http.NewRequest(http.MethodPost, f.gw.baseURL+"/v1/messages",
				strings.NewReader(fmt.Sprintf(`{"model":"%s","max_tokens":64,"messages":[{"role":"user","content":"回复OK"}]}`, account.Model)))
			retry.Header.Set("Content-Type", "application/json")
			retry.Header.Set("Authorization", "Bearer "+route.apiKey)
			last = f.doRaw(retry)
		}
		if last.Status != http.StatusOK || !strings.Contains(last.Body, "content") {
			t.Fatalf("S2 messages failed: status=%d body=%s", last.Status, maskRealBody(last.Body))
		}
		t.Logf("S2 anthropic（…%s）：200，模型 %s", account.ID[len(account.ID)-8:], account.Model)
	})

	t.Run("M1_mock500_then_real_failover", func(t *testing.T) {
		account, ok := r.pick("gpt", 0)
		if !ok {
			account, ok = r.pick("openai", 0)
			if !ok {
				t.Skip("M1：无可用 gpt/openai 导入账户")
			}
		}
		mockKey := fullchainUpstreamKey(t, "M1-mock")
		// mock 账户与真号同 provider（分组/模型过滤契约），并把真号请求模型
		// 加入 mock 账户支持列表，避免被模型过滤直接剔除。
		mockAccountExtra := map[string]any{
			"providerCode":              account.ProviderCode,
			"providerProtocolProfileId": profileForProvider(account.ProviderCode),
			"supportedModels":           []string{account.Model, acceptanceModel, fullchainProbeModel},
		}
		groupID := f.createGroupWithProvider("全链路-M1-组", account.ProviderCode)
		mockAccountID := f.createAccount("全链路-M1-mock账户", mockKey, groupID, mockAccountExtra)
		f.rebindAccountGroup(account.ID, groupID, 100)
		f.mock.setDefault(mockKey, mockupstream.ScenarioStatus500)
		strategyID := f.createStrategy("全链路-M1-策略", "normal", []map[string]any{
			{"groupId": groupID, "priority": 1, "weight": 100},
		}, nil)
		apiKey := f.createAPIKey("全链路-M1-Key", strategyID)

		// mock 恒 500 → 同账户重试耗尽 → 真号接管（失败重试一次由
		// assertRealChat 兜底上游偶发）。
		response := assertRealChat(t, f, apiKey, account.Model)
		if strings.Contains(response.Body, "MOCK") {
			t.Fatalf("M1 response came from mock: %s", maskRealBody(response.Body))
		}
		// 诊断：mock/real 账户状态与审计 attempt 归因。
		mockSnapshot := f.accountSnapshot(mockAccountID)
		realSnapshot := f.accountSnapshot(account.ID)
		t.Logf("M1 diagnostic: mock(status=%s schedulable=%v models=%v) real(status=%s schedulable=%v priority=%v)",
			mockSnapshot["status"], mockSnapshot["schedulable"], mockSnapshot["supportedModels"], realSnapshot["status"], realSnapshot["schedulable"], realSnapshot["priority"])
		apiKeyID := f.apiKeyIDByName("全链路-M1-Key")
		for _, item := range f.findAuditLogs(apiKeyID) {
			auditDetail := f.auditLogDetail(str(item["id"]))
			accounts := []string{}
			for _, attempt := range auditDetail.Attempts {
				accounts = append(accounts, fmt.Sprintf("%s(%v)", str(attempt["accountId"])[len(str(attempt["accountId"]))-8:], attempt["upstreamStatusCode"]))
			}
			t.Logf("M1 diagnostic audit: success=%v attempts=%v", auditDetail.Success, accounts)
		}
		if got := len(f.mock.protocolCallsByKeyModel(mockKey, account.Model)); got < 2 {
			t.Fatalf("M1 mock attempts=%d want >=2", got)
		}
		// 审计归因两行：mock 失败 attempt + 真号成功 attempt（usage 面被
		// E2E-FINDING #1 阻塞，attempt 行是同源证据）。
		detail := f.waitAuditLogDetail(apiKeyID, func(log fullchainAuditLog) bool {
			return len(log.Attempts) >= 2
		}, "with mock + real attempts")
		if str(detail.Attempts[0]["accountId"]) != mockAccountID {
			t.Fatalf("M1 first attempt account wrong: %s", str(detail.Attempts[0]["accountId"]))
		}
		if str(detail.Attempts[len(detail.Attempts)-1]["accountId"]) != account.ID {
			t.Fatalf("M1 final attempt account wrong: %s", str(detail.Attempts[len(detail.Attempts)-1]["accountId"]))
		}
		t.Logf("M1（…%s 接管）：mock 尝试 %d 次，最终 200 真号内容", account.ID[len(account.ID)-8:], len(f.mock.protocolCallsByKeyModel(mockKey, account.Model)))
		f.mock.clearKey(mockKey)
	})

	t.Run("M2_mock_serves_real_zero_call", func(t *testing.T) {
		account, ok := r.pick("openai", 0)
		if !ok {
			account, ok = r.pick("glm", 0)
			if !ok {
				t.Skip("M2：无可用 openai/glm 导入账户")
			}
		}
		mockKey := fullchainUpstreamKey(t, "M2-mock")
		// 同 provider 分组 + mock 账户支持真号请求模型；mock 保持默认
		// priority 0，真号以 priority 100 加入（组内升序派发：mock 先、
		// 真号后备）。
		mockAccountExtra := map[string]any{
			"providerCode":              account.ProviderCode,
			"providerProtocolProfileId": profileForProvider(account.ProviderCode),
			"supportedModels":           []string{account.Model, acceptanceModel, fullchainProbeModel},
		}
		groupID := f.createGroupWithProvider("全链路-M2-组", account.ProviderCode)
		mockAccountID := f.createAccount("全链路-M2-mock账户", mockKey, groupID, mockAccountExtra)
		f.rebindAccountGroup(account.ID, groupID, 100)
		strategyID := f.createStrategy("全链路-M2-策略", "normal", []map[string]any{
			{"groupId": groupID, "priority": 1, "weight": 100},
		}, nil)
		apiKey := f.createAPIKey("全链路-M2-Key", strategyID)

		response := f.chat(apiKey, realChatPayload(account.Model))
		if response.Status != http.StatusOK {
			t.Fatalf("M2 mock-first status=%d body=%s", response.Status, maskRealBody(response.Body))
		}
		if !strings.Contains(response.Body, "MOCK-OK reply") {
			t.Fatalf("M2 expected mock content: %s", maskRealBody(response.Body))
		}
		// 成本护栏证据：全部 attempt 都是 mock 账户（真号零出网）。
		apiKeyID := f.apiKeyIDByName("全链路-M2-Key")
		detail := f.waitAuditLogDetail(apiKeyID, func(log fullchainAuditLog) bool {
			return log.Success && len(log.Attempts) >= 1
		}, "mock-first success")
		for _, attempt := range detail.Attempts {
			if str(attempt["accountId"]) != mockAccountID {
				t.Fatalf("M2 real account was hit (cost guard violated): %s", str(attempt["accountId"]))
			}
		}
		t.Logf("M2（真号 …%s 零调用）：mock 服务 200", account.ID[len(account.ID)-8:])
	})
}
