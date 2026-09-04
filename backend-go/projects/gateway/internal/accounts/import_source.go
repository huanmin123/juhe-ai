package accounts

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Import source adapters mirror account-import-source-adapters.ts: the
// third-party shapes (Sub2API exports, NewAPI/One-API channel dumps and
// CLIProxyAPI configs) are rewritten into the native import document before
// planning. YAML CLIProxyAPI configs stay with the companion slice — JSON
// input is accepted (JSON is a YAML subset for these flat configs).

type importSourceMode = string

const (
	importSourceNative  = "native"
	importSourceSub2Api = "sub2api"
	importSourceNewAPI  = "newapi"
	importSourceCPA     = "cpa"
	importSourceOneAPI  = "oneapi"
)

var importSourceModes = map[string]bool{
	importSourceNative: true, importSourceSub2Api: true, importSourceNewAPI: true,
	importSourceCPA: true, importSourceOneAPI: true,
}

// ImportSourceSummary mirrors AccountImportSourceSummary.
type ImportSourceSummary struct {
	Mode          string   `json:"mode"`
	Records       int      `json:"records"`
	Accepted      int      `json:"accepted"`
	Skipped       int      `json:"skipped"`
	IgnoredFields int      `json:"ignoredFields"`
	Messages      []string `json:"messages"`
}

// sourceLabel mirrors sourceLabel.
func sourceLabel(mode string) string {
	if mode == importSourceNewAPI {
		return "NewAPI"
	}
	return "One-API"
}

const (
	maxImportedAccounts       = 50
	maxImportedProxies        = 20
	defaultOpenAIBaseURL      = "https://api.openai.com/v1"
	importSourceMessageLimit  = 8
	gptVendorCode             = "gpt"
	gptOpenAIV1ProfileID      = "profile_gpt_openai_v1"
	openAICompatibleProvider  = "openai"
	openAICompatibleProfileID = "profile_openai_openai_v1"
)

// adaptImportSource mirrors adaptAccountImportSource. Native data passes
// through untouched; every other mode rewrites into the native document.
func adaptImportSource(data any, mode string) (any, ImportSourceSummary) {
	if mode == importSourceNative {
		return data, emptySourceSummary(mode)
	}
	state := &adapterState{source: emptySourceSummary(mode)}
	switch mode {
	case importSourceSub2Api:
		adaptSub2API(data, state)
	case importSourceNewAPI, importSourceOneAPI:
		adaptChannelSource(data, mode, state)
	default:
		adaptCLIProxyAPI(data, state)
	}
	if state.source.Records == 0 && len(state.source.Messages) == 0 {
		addSourceMessage(state, "未识别到可导入的来源账户记录")
	}
	if state.source.Skipped > importSourceMessageLimit {
		addSourceMessage(state, fmt.Sprintf("另有 %d 条来源记录未逐项展开", state.source.Skipped-importSourceMessageLimit))
	}
	document := map[string]any{
		"type":    accountImportProtocolType,
		"version": float64(accountImportProtocolVersion),
	}
	proxies := state.proxies
	if proxies == nil {
		proxies = []any{}
	}
	accounts := state.accounts
	if accounts == nil {
		accounts = []any{}
	}
	document["proxies"] = proxies
	document["accounts"] = accounts
	return document, state.source
}

func emptySourceSummary(mode string) ImportSourceSummary {
	return ImportSourceSummary{Mode: mode, Messages: []string{}}
}

type adapterState struct {
	source               ImportSourceSummary
	accounts             []any
	proxies              []any
	proxyRefByKey        map[string]string
	unavailableProxyKeys map[string]bool
}

func addSourceMessage(state *adapterState, message string) {
	for _, existing := range state.source.Messages {
		if existing == message {
			return
		}
	}
	state.source.Messages = append(state.source.Messages, message)
}

func skipSourceRecord(state *adapterState, index int, reason string) {
	state.source.Skipped++
	if state.source.Skipped <= importSourceMessageLimit {
		addSourceMessage(state, fmt.Sprintf("第 %d 条来源记录已跳过：%s", index, reason))
	}
}

func countIgnoredRecordKeys(record map[string]any, acceptedKeys map[string]bool, state *adapterState) {
	for key := range record {
		if !acceptedKeys[key] {
			state.source.IgnoredFields++
		}
	}
}

func acceptAccount(state *adapterState, index int, account map[string]any) {
	if len(state.accounts) >= maxImportedAccounts {
		skipSourceRecord(state, index, fmt.Sprintf("超过单次最多 %d 条账户的限制", maxImportedAccounts))
		return
	}
	state.accounts = append(state.accounts, account)
	state.source.Accepted++
}

// parseSourceJSON mirrors parseJsonInput: string inputs must decode as JSON.
func parseSourceJSON(value any, label string) (any, error) {
	if text, ok := value.(string); ok {
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return nil, fmt.Errorf("%s 导入内容必须是 JSON", label)
		}
		return parsed, nil
	}
	return value, nil
}

func adaptSub2API(input any, state *adapterState) {
	state.proxyRefByKey = map[string]string{}
	state.unavailableProxyKeys = map[string]bool{}
	parsed, err := parseSourceJSON(input, "Sub2API")
	if err != nil {
		addSourceMessage(state, err.Error())
		return
	}
	root, ok := parsed.(map[string]any)
	if !ok {
		addSourceMessage(state, "来源导入内容必须是对象")
		return
	}
	data := root
	if inner, ok := root["data"].(map[string]any); ok {
		data = inner
	}
	countIgnoredRecordKeys(data, map[string]bool{
		"type": true, "version": true, "exported_at": true, "proxies": true,
		"accounts": true, "skipped_shadows": true,
	}, state)

	proxies, _ := data["proxies"].([]any)
	for index, value := range proxies {
		adaptSub2APIProxy(value, index+1, state)
	}
	accounts, _ := data["accounts"].([]any)
	if len(accounts) == 0 {
		addSourceMessage(state, "Sub2API 数据未包含 accounts 数组")
		return
	}
	for index, value := range accounts {
		adaptSub2APIAccount(value, index+1, state)
	}
}

func adaptSub2APIProxy(value any, index int, state *adapterState) {
	record, ok := value.(map[string]any)
	if !ok {
		state.source.IgnoredFields++
		return
	}
	countIgnoredRecordKeys(record, map[string]bool{
		"proxy_key": true, "name": true, "protocol": true, "host": true, "port": true,
		"username": true, "password": true, "status": true,
	}, state)
	proxyKey := sourceText(record["proxy_key"])
	protocol := normalizeSourceProxyType(record["protocol"])
	host := sourceText(record["host"])
	port := positiveInteger(record["port"])
	active := normalizeSourceStatus(record["status"]) != "disabled"
	if proxyKey == "" || protocol == "" || host == "" || port == 0 || !active {
		if proxyKey != "" {
			if state.unavailableProxyKeys == nil {
				state.unavailableProxyKeys = map[string]bool{}
			}
			state.unavailableProxyKeys[proxyKey] = true
		}
		return
	}
	if len(state.proxies) >= maxImportedProxies {
		state.unavailableProxyKeys[proxyKey] = true
		return
	}
	ref := fmt.Sprintf("sub2api-proxy-%d", index)
	state.proxyRefByKey[proxyKey] = ref
	entry := map[string]any{
		"ref": ref, "type": protocol, "host": host, "port": float64(port), "enabled": true,
	}
	if name := sourceText(record["name"]); name != "" {
		entry["name"] = name
	} else {
		entry["name"] = fmt.Sprintf("Sub2API 代理 %d", index)
	}
	if username := sourceText(record["username"]); username != "" {
		entry["username"] = username
	}
	if password := sourceText(record["password"]); password != "" {
		entry["password"] = password
	}
	state.proxies = append(state.proxies, entry)
}

func adaptSub2APIAccount(value any, index int, state *adapterState) {
	state.source.Records++
	record, ok := value.(map[string]any)
	if !ok {
		skipSourceRecord(state, index, "账户不是对象")
		return
	}
	countIgnoredRecordKeys(record, map[string]bool{
		"name": true, "notes": true, "platform": true, "type": true, "credentials": true,
		"proxy_key": true, "concurrency": true, "priority": true, "expires_at": true,
	}, state)
	if !isOpenAISourcePlatform(record["platform"]) {
		skipSourceRecord(state, index, "只支持 OpenAI 平台账户")
		return
	}
	sourceType := normalizeSub2APIAccountType(record["type"])
	if sourceType == "" {
		skipSourceRecord(state, index, "账户类型不是可导入的 API Key 或 OAuth")
		return
	}
	credentials, ok := record["credentials"].(map[string]any)
	if !ok {
		skipSourceRecord(state, index, "缺少 credentials")
		return
	}
	proxyRef, proceed := resolveSub2APIProxyRef(record["proxy_key"], index, state)
	if !proceed {
		return
	}
	input := adaptedAccountInput{
		credentials:    credentials,
		groupName:      "Sub2API 导入",
		status:         "active",
		proxyRef:       proxyRef,
		concurrency:    positiveInteger(record["concurrency"]),
		priority:       nonNegativeInteger(record["priority"]),
		notes:          sourceText(record["notes"]),
		accountExpires: isoDateString(record["expires_at"]),
	}
	if name := sourceText(record["name"]); name != "" {
		input.name = name
	} else {
		input.name = fmt.Sprintf("Sub2API %s %d", map[string]string{"api_key": "API Key", "oauth": "OAuth"}[sourceType], index)
	}
	var account map[string]any
	if sourceType == "api_key" {
		account = adaptAPIKeyAccount(input, state)
	} else {
		account = adaptOAuthAccount(input, state)
	}
	if account == nil {
		if sourceType == "api_key" {
			skipSourceRecord(state, index, "缺少可用 API Key 或 Base URL")
		} else {
			skipSourceRecord(state, index, "缺少可用 OAuth 凭据或 Base URL")
		}
		return
	}
	acceptAccount(state, index, account)
}

// resolveSub2APIProxyRef mirrors resolveSub2ApiProxyRef: (ref, true) when the
// account may continue, (nil, false)/(ref, false) when it must stop.
func resolveSub2APIProxyRef(value any, index int, state *adapterState) (string, bool) {
	proxyKey := sourceText(value)
	if proxyKey == "" {
		return "", true
	}
	if ref, ok := state.proxyRefByKey[proxyKey]; ok {
		return ref, true
	}
	if state.unavailableProxyKeys[proxyKey] {
		skipSourceRecord(state, index, "引用的来源代理不可用或超过本次导入上限")
		return "", false
	}
	skipSourceRecord(state, index, "引用的来源代理不存在")
	return "", false
}

func adaptChannelSource(input any, mode string, state *adapterState) {
	parsed, err := parseSourceJSON(input, sourceLabel(mode))
	if err != nil {
		addSourceMessage(state, err.Error())
		return
	}
	records := extractChannelRecords(parsed)
	if len(records) == 0 {
		addSourceMessage(state, fmt.Sprintf("%s 数据未包含 Channel 记录", sourceLabel(mode)))
		return
	}
	for index, value := range records {
		state.source.Records++
		record, ok := value.(map[string]any)
		if !ok {
			skipSourceRecord(state, index+1, "Channel 不是对象")
			continue
		}
		countIgnoredRecordKeys(record, map[string]bool{
			"id": true, "type": true, "key": true, "base_url": true, "name": true,
			"group": true, "status": true,
		}, state)
		if !isOpenAIChannel(record["type"], mode) {
			skipSourceRecord(state, index+1, "Channel 不是该来源定义的 OpenAI 类型")
			continue
		}
		apiKeys := sourceAPIKeyList(record["key"])
		if len(apiKeys) == 0 || len(apiKeys) > 10 {
			if len(apiKeys) == 0 {
				skipSourceRecord(state, index+1, "Channel 缺少 API Key")
			} else {
				skipSourceRecord(state, index+1, "Channel API Key 数量超过 10 条")
			}
			continue
		}
		baseURL := sourceText(record["base_url"])
		if baseURL == "" {
			baseURL = defaultOpenAIBaseURL
		}
		if !safeSourceBaseURL(baseURL, state) {
			skipSourceRecord(state, index+1, "Channel Base URL 不符合上游地址策略")
			continue
		}
		name := sourceText(record["name"])
		if name == "" {
			name = fmt.Sprintf("%s Channel %d", sourceLabel(mode), index+1)
		}
		acceptAccount(state, index+1, buildSourceAPIKeyAccount(apiKeys, baseURL, name,
			sourceGroupName(record["group"], fmt.Sprintf("%s 导入", sourceLabel(mode))),
			normalizeChannelStatus(record["status"]), "", 0, -1, "", ""))
	}
}

func adaptCLIProxyAPI(input any, state *adapterState) {
	parsed, err := parseSourceJSON(input, "CLIProxyAPI")
	if err != nil {
		addSourceMessage(state, "CLIProxyAPI 导入内容必须是有效 YAML 或 JSON")
		return
	}
	root, ok := parsed.(map[string]any)
	if !ok {
		addSourceMessage(state, "来源导入内容必须是对象")
		return
	}
	sourceType := strings.ToLower(firstSourceText(root["type"], recordField(root, "metadata", "type"), root["provider"]))
	if sourceType == "codex" {
		adaptCPACodexAuthFile(root, state)
		return
	}
	countIgnoredRecordKeys(root, map[string]bool{
		"codex-api-key": true, "codex_api_key": true,
		"openai-compatibility": true, "openai_compatibility": true,
	}, state)
	if state.proxyRefByKey == nil {
		state.proxyRefByKey = map[string]string{}
		state.unavailableProxyKeys = map[string]bool{}
	}
	codexEntries, _ := firstPresentList(root, "codex-api-key", "codex_api_key")
	for index, value := range codexEntries {
		adaptCPAAPIKeyEntry(value, index+1, "CLIProxyAPI Codex API Key", "CLIProxyAPI 导入", "", state)
	}
	providers, _ := firstPresentList(root, "openai-compatibility", "openai_compatibility")
	for providerIndex, value := range providers {
		provider, ok := value.(map[string]any)
		if !ok {
			state.source.IgnoredFields++
			continue
		}
		countIgnoredRecordKeys(provider, map[string]bool{
			"name": true, "base-url": true, "base_url": true,
			"api-key-entries": true, "api_key_entries": true,
		}, state)
		providerName := sourceText(provider["name"])
		if providerName == "" {
			providerName = fmt.Sprintf("CLIProxyAPI OpenAI Provider %d", providerIndex+1)
		}
		providerBaseURL := firstSourceText(provider["base-url"], provider["base_url"])
		entries, _ := firstPresentList(provider, "api-key-entries", "api_key_entries")
		for entryIndex, entry := range entries {
			adaptCPAAPIKeyEntry(entry, entryIndex+1, providerName, "CLIProxyAPI 导入", providerBaseURL, state)
		}
	}
	if state.source.Records == 0 {
		addSourceMessage(state, "CLIProxyAPI 配置未包含 codex-api-key 或 openai-compatibility API Key")
	}
}

func adaptCPAAPIKeyEntry(value any, index int, label, groupName, baseURL string, state *adapterState) {
	state.source.Records++
	record, _ := value.(map[string]any)
	if record != nil {
		countIgnoredRecordKeys(record, map[string]bool{
			"api-key": true, "api_key": true, "key": true, "base-url": true, "base_url": true,
		}, state)
	}
	var keyValue any = value
	if record != nil {
		keyValue = firstPresent(record, "api-key", "api_key", "key")
	}
	apiKeys := sourceAPIKeyList(keyValue)
	if len(apiKeys) == 0 || len(apiKeys) > 10 {
		if len(apiKeys) == 0 {
			skipSourceRecord(state, index, "缺少 API Key")
		} else {
			skipSourceRecord(state, index, "API Key 数量超过 10 条")
		}
		return
	}
	// record may be a nil map: reads stay nil-safe and firstSourceText skips
	// the blank results (mirror of firstText(record?.['base-url'], ...)).
	resolved := firstSourceText(record["base-url"], record["base_url"], baseURL)
	if resolved == "" {
		resolved = defaultOpenAIBaseURL
	}
	if !safeSourceBaseURL(resolved, state) {
		skipSourceRecord(state, index, "API Key Base URL 不符合上游地址策略")
		return
	}
	acceptAccount(state, index, buildSourceAPIKeyAccount(apiKeys, resolved,
		fmt.Sprintf("%s %d", label, index), groupName, "active", "", 0, -1, "", ""))
}

func adaptCPACodexAuthFile(root map[string]any, state *adapterState) {
	state.source.Records++
	tokens := root
	if tokenData, ok := root["token_data"].(map[string]any); ok {
		merged := map[string]any{}
		for key, value := range tokenData {
			merged[key] = value
		}
		for key, value := range root {
			merged[key] = value
		}
		tokens = merged
	}
	countIgnoredRecordKeys(root, map[string]bool{
		"type": true, "provider": true, "metadata": true, "token_data": true,
		"access_token": true, "accessToken": true, "refresh_token": true, "refreshToken": true,
		"id_token": true, "idToken": true, "email": true, "account_id": true, "accountId": true,
		"chatgpt_account_id": true, "expires_at": true, "expiresAt": true, "name": true, "id": true,
	}, state)
	credentials := sourceOAuthCredentials(tokens, state)
	if credentials == nil {
		skipSourceRecord(state, 1, "Codex auth-file 缺少可用 OAuth 凭据")
		return
	}
	name := firstSourceText(tokens["email"], root["email"], root["name"], root["id"])
	if name == "" {
		name = "CLIProxyAPI Codex OAuth"
	}
	acceptAccount(state, 1, map[string]any{
		"name":                      name,
		"providerCode":              gptVendorCode,
		"providerProtocolProfileId": gptOpenAIV1ProfileID,
		"type":                      "oauth",
		"status":                    "active",
		"groupName":                 "CLIProxyAPI 导入",
		"credentials":               credentials,
	})
}

// adaptedAccountInput is the shared shape for the sub2api adapters.
type adaptedAccountInput struct {
	credentials    map[string]any
	name           string
	groupName      string
	status         string
	proxyRef       string
	concurrency    int
	priority       int
	notes          string
	accountExpires string
}

func adaptAPIKeyAccount(input adaptedAccountInput, state *adapterState) map[string]any {
	countIgnoredRecordKeys(input.credentials, map[string]bool{
		"api_key": true, "api_keys": true, "key": true, "base_url": true,
	}, state)
	apiKeys := sourceAPIKeyList(firstPresent(input.credentials, "api_key", "api_keys", "key"))
	if len(apiKeys) == 0 || len(apiKeys) > 10 {
		return nil
	}
	baseURL := sourceText(input.credentials["base_url"])
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	if !safeSourceBaseURL(baseURL, state) {
		return nil
	}
	return buildSourceAPIKeyAccount(apiKeys, baseURL, input.name, input.groupName, input.status,
		input.proxyRef, input.concurrency, input.priority, input.notes, input.accountExpires)
}

func adaptOAuthAccount(input adaptedAccountInput, state *adapterState) map[string]any {
	credentials := sourceOAuthCredentials(input.credentials, state)
	if credentials == nil {
		return nil
	}
	account := map[string]any{
		"name":                      input.name,
		"providerCode":              gptVendorCode,
		"providerProtocolProfileId": gptOpenAIV1ProfileID,
		"type":                      "oauth",
		"status":                    input.status,
		"groupName":                 input.groupName,
		"credentials":               credentials,
	}
	if input.proxyRef != "" {
		account["proxyRef"] = input.proxyRef
	}
	if input.concurrency > 0 {
		account["concurrencyLimit"] = float64(input.concurrency)
	}
	if input.priority >= 0 {
		account["priority"] = float64(input.priority)
	}
	if input.accountExpires != "" {
		account["accountExpiresAt"] = input.accountExpires
	}
	if input.notes != "" {
		account["notes"] = input.notes
	}
	return account
}

// buildSourceAPIKeyAccount mirrors buildApiKeyAccount: absent numeric fields
// use the -1 sentinel and stay out of the document.
func buildSourceAPIKeyAccount(apiKeys []string, baseURL, name, groupName, status, proxyRef string, concurrency, priority int, notes, accountExpires string) map[string]any {
	account := map[string]any{
		"name":                      name,
		"providerCode":              openAICompatibleProvider,
		"providerProtocolProfileId": openAICompatibleProfileID,
		"type":                      "api_key",
		"status":                    status,
		"groupName":                 groupName,
		"credentials": map[string]any{
			"api_key":  apiKeys[0],
			"base_url": baseURL,
		},
	}
	if len(apiKeys) > 1 {
		list := make([]any, 0, len(apiKeys))
		for _, key := range apiKeys {
			list = append(list, key)
		}
		account["credentials"].(map[string]any)["api_keys"] = list
		account["credentials"].(map[string]any)["api_key_strategy"] = "failover"
	}
	if proxyRef != "" {
		account["proxyRef"] = proxyRef
	}
	if concurrency > 0 {
		account["concurrencyLimit"] = float64(concurrency)
	}
	if priority >= 0 {
		account["priority"] = float64(priority)
	}
	if accountExpires != "" {
		account["accountExpiresAt"] = accountExpires
	}
	if notes != "" {
		account["notes"] = notes
	}
	return account
}

// sourceOAuthCredentials mirrors oauthCredentials.
func sourceOAuthCredentials(value map[string]any, state *adapterState) map[string]any {
	countIgnoredRecordKeys(value, map[string]bool{
		"access_token": true, "accessToken": true, "refresh_token": true, "refreshToken": true,
		"expires_at": true, "expiresAt": true, "client_id": true, "clientId": true,
		"id_token": true, "idToken": true, "token_type": true, "tokenType": true, "scope": true,
		"email": true, "account_id": true, "accountId": true, "chatgpt_account_id": true,
		"chatgptAccountId": true, "chatgpt_user_id": true, "chatgptUserId": true,
		"plan_type": true, "planType": true, "organization_id": true, "organizationId": true,
		"base_url": true,
	}, state)
	refreshToken := firstSourceText(value["refresh_token"], value["refreshToken"])
	accessToken := firstSourceText(value["access_token"], value["accessToken"])
	accountID := firstSourceText(value["account_id"], value["accountId"], value["chatgpt_account_id"], value["chatgptAccountId"])
	if refreshToken == "" && accessToken == "" {
		return nil
	}
	if accessToken != "" && accountID == "" && refreshToken != "" {
		accessToken = ""
		state.source.IgnoredFields++
	}
	if accessToken != "" && accountID == "" {
		return nil
	}
	baseURL := sourceText(value["base_url"])
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	if !safeSourceBaseURL(baseURL, state) {
		return nil
	}
	credentials := map[string]any{"base_url": baseURL}
	if refreshToken != "" {
		credentials["refresh_token"] = refreshToken
	}
	if accessToken != "" {
		credentials["access_token"] = accessToken
	}
	if text := isoDateString(firstPresent(value, "expires_at", "expiresAt")); text != "" {
		credentials["expires_at"] = text
	}
	if text := firstSourceText(value["client_id"], value["clientId"]); text != "" {
		credentials["client_id"] = text
	}
	if text := firstSourceText(value["id_token"], value["idToken"]); text != "" {
		credentials["id_token"] = text
	}
	if text := firstSourceText(value["token_type"], value["tokenType"]); text != "" {
		credentials["token_type"] = text
	}
	if text := sourceText(value["scope"]); text != "" {
		credentials["scope"] = text
	}
	if text := sourceText(value["email"]); text != "" {
		credentials["email"] = text
	}
	if accountID != "" {
		credentials["account_id"] = accountID
	}
	if text := firstSourceText(value["organization_id"], value["organizationId"]); text != "" {
		credentials["organization_id"] = text
	}
	if text := firstSourceText(value["chatgpt_user_id"], value["chatgptUserId"]); text != "" {
		credentials["chatgpt_user_id"] = text
	}
	if text := firstSourceText(value["plan_type"], value["planType"]); text != "" {
		credentials["plan_type"] = text
	}
	return credentials
}

func extractChannelRecords(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if list, ok := record["items"].([]any); ok {
		return list
	}
	if list, ok := record["channels"].([]any); ok {
		return list
	}
	if inner, ok := record["data"]; ok {
		return extractChannelRecords(inner)
	}
	return []any{value}
}

func recordField(record map[string]any, container, key string) any {
	if record == nil {
		return nil
	}
	if inner, ok := record[container].(map[string]any); ok {
		return inner[key]
	}
	return nil
}

// firstPresent returns the first present value among the keys.
func firstPresent(record map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, exists := record[key]; exists && value != nil {
			return value
		}
	}
	return nil
}

// firstPresentList returns the first key that holds an array.
func firstPresentList(record map[string]any, keys ...string) ([]any, bool) {
	for _, key := range keys {
		if list, ok := record[key].([]any); ok {
			return list, true
		}
	}
	return nil, false
}

func sourceText(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func firstSourceText(values ...any) string {
	for _, value := range values {
		if text := sourceText(value); text != "" {
			return text
		}
	}
	return ""
}

func positiveInteger(value any) int {
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) || int(number) <= 0 {
		return 0
	}
	return int(number)
}

func nonNegativeInteger(value any) int {
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) || int(number) < 0 {
		return -1
	}
	return int(number)
}

func isOpenAISourcePlatform(value any) bool {
	normalized := strings.ToLower(sourceText(value))
	return normalized == "openai" || normalized == "gpt" || normalized == "chatgpt"
}

func isOpenAIChannel(value any, mode string) bool {
	if mode != importSourceOneAPI && value == float64(1) {
		return true
	}
	if mode == importSourceOneAPI && value == float64(1) {
		return true
	}
	normalized := strings.ToLower(sourceText(value))
	normalized = strings.NewReplacer(" ", "", "_", "", "-", "").Replace(normalized)
	return normalized == "openai" || normalized == "openaicompatible"
}

func normalizeSourceStatus(value any) string {
	if value == float64(0) || value == false {
		return "disabled"
	}
	normalized := strings.ToLower(sourceText(value))
	if normalized == "disabled" || normalized == "inactive" || normalized == "banned" {
		return "disabled"
	}
	return "active"
}

func normalizeChannelStatus(value any) string {
	if value == float64(1) {
		return "active"
	}
	normalized := strings.ToLower(sourceText(value))
	if normalized == "1" || normalized == "active" || normalized == "enabled" {
		return "active"
	}
	return "disabled"
}

func normalizeSourceProxyType(value any) string {
	normalized := strings.ToLower(sourceText(value))
	switch normalized {
	case "http", "https", "socks5", "socks5h":
		return normalized
	default:
		return ""
	}
}

func normalizeSub2APIAccountType(value any) string {
	normalized := strings.ToLower(sourceText(value))
	if normalized == "apikey" || normalized == "api_key" {
		return "api_key"
	}
	if normalized == "oauth" {
		return "oauth"
	}
	return ""
}

func sourceGroupName(value any, fallback string) string {
	normalized := sourceText(value)
	if normalized == "" || strings.ContainsAny(normalized, ",;\n\r") {
		return fallback
	}
	return normalized
}

// isoDateString mirrors the adapter isoDate: strings pass through, epoch
// numbers (seconds or milliseconds) render as ISO.
func isoDateString(value any) string {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		return trimmed
	case float64:
		timestamp := int64(typed)
		if timestamp <= 10_000_000_000 {
			timestamp *= 1000
		}
		return time.UnixMilli(timestamp).UTC().Format("2006-01-02T15:04:05.000") + "Z"
	default:
		return ""
	}
}

// sourceAPIKeyList mirrors apiKeyList: nested JSON arrays, newline splits,
// masked keys dropped, duplicates collapsed.
func sourceAPIKeyList(value any) []string {
	input, ok := value.([]any)
	if !ok {
		input = []any{value}
	}
	out := []string{}
	seen := map[string]bool{}
	var addKey func(string)
	addKey = func(raw string) {
		key := strings.TrimSpace(raw)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, key)
	}
	for _, item := range input {
		text, ok := item.(string)
		if !ok {
			continue
		}
		raw := strings.TrimSpace(text)
		if raw == "" || isMaskedAPIKey(raw) {
			continue
		}
		if strings.HasPrefix(raw, "[") {
			var nested any
			if err := json.Unmarshal([]byte(raw), &nested); err == nil {
				for _, key := range sourceAPIKeyList(nested) {
					addKey(key)
				}
				continue
			}
		}
		for _, line := range strings.Split(raw, "\r\n") {
			for _, piece := range strings.Split(line, "\n") {
				addKey(piece)
			}
		}
	}
	return out
}

func isMaskedAPIKey(value string) bool {
	normalized := strings.ToLower(value)
	return strings.Contains(normalized, "***") ||
		strings.Contains(normalized, "…") ||
		strings.Contains(normalized, "...") ||
		normalized == "<redacted>" || normalized == "[redacted]" || normalized == "masked"
}

// safeSourceBaseURL mirrors the safeBaseUrl adapter hook: the strict SSRF
// policy (DNS allowlists) belongs to the platform request layer, so the
// adapter performs the structural check only.
func safeSourceBaseURL(value string, state *adapterState) bool {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		state.source.IgnoredFields++
		return false
	}
	if _, err := strconv.Atoi(parsed.Port()); parsed.Port() != "" && err != nil {
		state.source.IgnoredFields++
		return false
	}
	return true
}
