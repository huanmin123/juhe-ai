package accounts

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// quoteGoString encodes a raw YAML text payload as a JSON string literal for
// request bodies that carry YAML in the data field.
func quoteGoString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("quote yaml payload: %v", err)
	}
	return string(encoded)
}

// The unit tests pin the CPA YAML source adapter (import_source_yaml.go plus
// adaptCLIProxyAPI) against the Node account-import-source-adapters.ts
// semantics: field mapping, summaries and the exact Chinese skip/error
// messages.

func cpaAdapt(t *testing.T, data any) (map[string]any, ImportSourceSummary) {
	t.Helper()
	adapted, source := adaptImportSource(data, importSourceCPA)
	document, ok := adapted.(map[string]any)
	if !ok {
		t.Fatalf("cpa adapt document: %T", adapted)
	}
	return document, source
}

func cpaAccounts(t *testing.T, document map[string]any) []map[string]any {
	t.Helper()
	list, ok := document["accounts"].([]any)
	if !ok {
		t.Fatalf("cpa accounts: %v", document["accounts"])
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		out = append(out, item.(map[string]any))
	}
	return out
}

func sourceHasMessage(source ImportSourceSummary, message string) bool {
	for _, item := range source.Messages {
		if item == message {
			return true
		}
	}
	return false
}

func assertSourceSummary(t *testing.T, source ImportSourceSummary, records, accepted, skipped, ignored int, messages ...string) {
	t.Helper()
	if source.Records != records || source.Accepted != accepted || source.Skipped != skipped || source.IgnoredFields != ignored {
		t.Fatalf("source summary: got (records=%d accepted=%d skipped=%d ignored=%d), want (%d %d %d %d); messages=%v",
			source.Records, source.Accepted, source.Skipped, source.IgnoredFields, records, accepted, skipped, ignored, source.Messages)
	}
	if len(source.Messages) != len(messages) {
		t.Fatalf("source messages: got %v, want %v", source.Messages, messages)
	}
	for index, want := range messages {
		if source.Messages[index] != want {
			t.Fatalf("source message %d: got %q, want %q", index, source.Messages[index], want)
		}
	}
}

func TestCPAYAMLSourceAdapterOpenAICompatibility(t *testing.T) {
	yamlInput := `openai-compatibility:
  - name: 供应商一
    base-url: https://provider-one.example.com/v1
    api-key-entries:
      - api-key: |
          sk-one-1
          sk-one-2
      - api-key: sk-one-3
        base-url: https://entry.example.com/v1
  - api_key_entries:
      - key: sk-two-1
codex-api-key:
  - api-key: sk-codex-1
`
	document, source := cpaAdapt(t, yamlInput)
	assertSourceSummary(t, source, 4, 4, 0, 0)

	accounts := cpaAccounts(t, document)
	if len(accounts) != 4 {
		t.Fatalf("cpa accounts: %d", len(accounts))
	}
	// codex-api-key entries adapt before the openai-compatibility providers
	// (Node and Go order alike).
	first := accounts[0]
	if first["name"] != "CLIProxyAPI Codex API Key 1" {
		t.Fatalf("cpa codex entry name: %v", first)
	}
	if first["credentials"].(map[string]any)["base_url"] != defaultOpenAIBaseURL {
		t.Fatalf("cpa codex entry base URL: %v", first)
	}
	second := accounts[1]
	if second["name"] != "供应商一 1" || second["providerCode"] != "openai" ||
		second["providerProtocolProfileId"] != "profile_openai_openai_v1" ||
		second["type"] != "api_key" || second["status"] != "active" || second["groupName"] != "CLIProxyAPI 导入" {
		t.Fatalf("cpa account one: %v", second)
	}
	credentials := second["credentials"].(map[string]any)
	if credentials["api_key"] != "sk-one-1" || credentials["base_url"] != "https://provider-one.example.com/v1" {
		t.Fatalf("cpa account one credentials: %v", credentials)
	}
	failoverKeys := credentials["api_keys"].([]any)
	if len(failoverKeys) != 2 || failoverKeys[0] != "sk-one-1" || failoverKeys[1] != "sk-one-2" ||
		credentials["api_key_strategy"] != "failover" {
		t.Fatalf("cpa failover keys: %v", credentials)
	}
	third := accounts[2]
	if third["name"] != "供应商一 2" {
		t.Fatalf("cpa account two: %v", third)
	}
	if third["credentials"].(map[string]any)["base_url"] != "https://entry.example.com/v1" {
		t.Fatalf("cpa entry base-url override: %v", third)
	}
	// Provider two falls back to the underscore key spellings, the default
	// OpenAI base URL and the provider-derived label.
	fourth := accounts[3]
	if fourth["name"] != "CLIProxyAPI OpenAI Provider 2 1" {
		t.Fatalf("cpa provider fallback name: %v", fourth)
	}
	fourthCredentials := fourth["credentials"].(map[string]any)
	if fourthCredentials["api_key"] != "sk-two-1" || fourthCredentials["base_url"] != defaultOpenAIBaseURL {
		t.Fatalf("cpa provider two credentials: %v", fourthCredentials)
	}
}

func TestCPAYAMLSourceAdapterJSONStringInput(t *testing.T) {
	// JSON stays a valid CPA payload (YAML subset) and keeps the exact
	// adapter contract the object form already exercises.
	jsonInput := `{"openai-compatibility":[{"name":"cpa-provider","base-url":"https://cpa.example.com/v1",
		"api-key-entries":[{"api-key":"sk-cpa-secret"}]}]}`
	document, source := cpaAdapt(t, jsonInput)
	assertSourceSummary(t, source, 1, 1, 0, 0)
	accounts := cpaAccounts(t, document)
	if accounts[0]["name"] != "cpa-provider 1" {
		t.Fatalf("json string account: %v", accounts[0])
	}
	if document["type"] != accountImportProtocolType || document["version"] != float64(accountImportProtocolVersion) {
		t.Fatalf("cpa document envelope: %v", document)
	}
}

func TestCPAYAMLSourceAdapterCodexAuthFile(t *testing.T) {
	yamlInput := `type: codex
email: user@example.com
token_data:
  access_token: ey-access
  refresh_token: ey-refresh
  account_id: acc-123
  expires_at: 1735689600
`
	document, source := cpaAdapt(t, yamlInput)
	// The merged token view carries `type` and `token_data` past the oauth
	// accepted-key set (mirror of the Node spread merge), so ignored=2.
	assertSourceSummary(t, source, 1, 1, 0, 2)
	accounts := cpaAccounts(t, document)
	if len(accounts) != 1 {
		t.Fatalf("codex auth accounts: %d", len(accounts))
	}
	account := accounts[0]
	if account["name"] != "user@example.com" || account["providerCode"] != "gpt" ||
		account["providerProtocolProfileId"] != "profile_gpt_openai_v1" ||
		account["type"] != "oauth" || account["status"] != "active" ||
		account["groupName"] != "CLIProxyAPI 导入" {
		t.Fatalf("codex auth account: %v", account)
	}
	credentials := account["credentials"].(map[string]any)
	if credentials["access_token"] != "ey-access" || credentials["refresh_token"] != "ey-refresh" ||
		credentials["account_id"] != "acc-123" || credentials["email"] != "user@example.com" ||
		credentials["expires_at"] != "2025-01-01T00:00:00.000Z" ||
		credentials["base_url"] != defaultOpenAIBaseURL {
		t.Fatalf("codex auth credentials: %v", credentials)
	}
}

func TestCPAYAMLSourceAdapterInvalidInputs(t *testing.T) {
	cases := []struct {
		name     string
		input    any
		messages []string
	}{
		{"非法 YAML", "foo: [unclosed", []string{"CLIProxyAPI 导入内容必须是有效 YAML 或 JSON"}},
		{"标量 YAML", "just-a-string", []string{"来源导入内容必须是对象"}},
		{"数组 YAML", "- one\n- two", []string{"来源导入内容必须是对象"}},
		{"空字符串", "", []string{"来源导入内容必须是对象"}},
		{"多文档 YAML", "a: 1\n---\nb: 2", []string{"CLIProxyAPI 导入内容必须是有效 YAML 或 JSON"}},
	}
	for _, testCase := range cases {
		_, source := cpaAdapt(t, testCase.input)
		assertSourceSummary(t, source, 0, 0, 0, 0, testCase.messages...)
	}
}

func TestCPAYAMLSourceAdapterSkips(t *testing.T) {
	// Missing key, masked key, oversized list and unsafe base URL all keep
	// the Node skip messages; the empty config reports the aggregate message.
	yamlInput := `openai-compatibility:
  - name: bad-provider
    api-key-entries:
      - base-url: https://ok.example.com/v1
      - api-key: "sk-***masked***"
      - api-key: [sk-a-1, sk-a-2, sk-a-3, sk-a-4, sk-a-5, sk-a-6, sk-a-7, sk-a-8, sk-a-9, sk-a-10, sk-a-11]
      - api-key: sk-good-1
        base-url: "ftp://files.example.com"
`
	_, source := cpaAdapt(t, yamlInput)
	assertSourceSummary(t, source, 4, 0, 4, 1,
		"第 1 条来源记录已跳过：缺少 API Key",
		"第 2 条来源记录已跳过：缺少 API Key",
		"第 3 条来源记录已跳过：API Key 数量超过 10 条",
		"第 4 条来源记录已跳过：API Key Base URL 不符合上游地址策略",
	)
	if source.Mode != importSourceCPA {
		t.Fatalf("source mode: %s", source.Mode)
	}

	_, emptySource := cpaAdapt(t, "openai-compatibility: []\ncodex-api-key: []\n")
	assertSourceSummary(t, emptySource, 0, 0, 0, 0,
		"CLIProxyAPI 配置未包含 codex-api-key 或 openai-compatibility API Key")

	_, missingCredentials := cpaAdapt(t, "type: codex\nemail: user@example.com\n")
	// `type` falls outside the oauthCredentials accepted-key set, so the
	// empty token view also counts one ignored field.
	assertSourceSummary(t, missingCredentials, 1, 0, 1, 1,
		"第 1 条来源记录已跳过：Codex auth-file 缺少可用 OAuth 凭据")
}

func TestCPAYAMLSourceAdapterNumberAndTimestampNormalization(t *testing.T) {
	// A numeric api key decodes as int, normalizes to float64 and is then
	// dropped by the string-only key list exactly like the Node adapter.
	yamlInput := `openai-compatibility:
  - name: num-provider
    base-url: https://num.example.com/v1
    api-key-entries:
      - api-key: 12345
`
	_, source := cpaAdapt(t, yamlInput)
	assertSourceSummary(t, source, 1, 0, 1, 0,
		"第 1 条来源记录已跳过：缺少 API Key")

	// Epoch and ISO timestamps normalize through the JSON-shaped helpers.
	yamlEpochCodex := `type: codex
provider: openai
access_token: ey-access
account_id: acc-9
expires_at: 2025-06-01T12:30:45Z
`
	document, codexSource := cpaAdapt(t, yamlEpochCodex)
	// `type` and `provider` fall outside the oauthCredentials accepted-key
	// set (the root layer accepts both), mirroring the Node count.
	assertSourceSummary(t, codexSource, 1, 1, 0, 2)
	credentials := cpaAccounts(t, document)[0]["credentials"].(map[string]any)
	if credentials["expires_at"] != "2025-06-01T12:30:45.000Z" {
		t.Fatalf("yaml timestamp normalization: %v", credentials)
	}
	if !strings.HasPrefix(credentials["access_token"].(string), "ey-") {
		t.Fatalf("access token: %v", credentials)
	}
}

// TestAccountImportCPAYAMLSource drives the full preview/confirm chain with
// raw YAML strings in the data field (the deferred Node parseCpaInput slice).
func TestAccountImportCPAYAMLSource(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedOpenAICompatibleProvider(t, env)

	yamlPlan := "openai-compatibility:\n" +
		"  - name: cpa-yaml-provider\n" +
		"    base-url: https://yaml.example.com/v1\n" +
		"    api-key-entries:\n" +
		"      - api-key: |\n" +
		"          sk-yaml-1\n" +
		"          sk-yaml-2\n" +
		"      - api-key: sk-yaml-3\n"
	code, preview := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview",
		`{"data":`+quoteGoString(t, yamlPlan)+`,"sourceMode":"cpa"}`)
	if code != http.StatusOK {
		t.Fatalf("cpa yaml preview: %d %v", code, preview)
	}
	plan := dataMap(t, preview)
	source := plan["source"].(map[string]any)
	if source["mode"] != "cpa" || source["records"] != float64(2) || source["accepted"] != float64(2) {
		t.Fatalf("cpa yaml source summary: %v", source)
	}
	items := plan["accounts"].([]any)
	first := items[0].(map[string]any)
	if first["action"] != "create" || first["name"] != "cpa-yaml-provider 1" {
		t.Fatalf("cpa yaml planned item: %v", first)
	}
	if plan["canImport"] != true {
		t.Fatalf("cpa yaml plan: %v", plan)
	}

	// Invalid YAML keeps the Node error text and fails the plan.
	code, invalid := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview",
		`{"data":"foo: [unclosed","sourceMode":"cpa"}`)
	if code != http.StatusOK {
		t.Fatalf("cpa invalid preview: %d %v", code, invalid)
	}
	invalidPlan := dataMap(t, invalid)
	invalidSource := invalidPlan["source"].(map[string]any)
	if invalidSource["messages"].([]any)[0].(string) != "CLIProxyAPI 导入内容必须是有效 YAML 或 JSON" {
		t.Fatalf("cpa invalid messages: %v", invalidSource)
	}
	if invalidPlan["canImport"] == true {
		t.Fatalf("cpa invalid plan must not import: %v", invalidPlan)
	}

	// The YAML plan confirms into real rows with the failover strategy.
	code, confirmed := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/confirm",
		`{"data":`+quoteGoString(t, yamlPlan)+`,"sourceMode":"cpa"}`)
	if code != http.StatusOK {
		t.Fatalf("cpa yaml confirm: %d %v", code, confirmed)
	}
	confirmItems := dataMap(t, confirmed)["accounts"].([]any)
	if len(confirmItems) != 2 {
		t.Fatalf("cpa yaml confirm items: %v", confirmItems)
	}
	createdID := confirmItems[0].(map[string]any)["accountId"].(string)
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND name = 'cpa-yaml-provider 1'
		AND provider_code = 'openai'`, createdID) != 1 {
		t.Fatal("cpa yaml row contract violated")
	}
	if env.count(t, `SELECT COUNT(*) FROM groups WHERE name = 'CLIProxyAPI 导入'`) != 1 {
		t.Fatal("cpa yaml group missing")
	}
}
