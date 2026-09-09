package gatewaydispatch

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat"
)

// Account preparation, migrated from dispatch/account-preparation.ts.

// SkipAccountForFailedProxyDispatch mirrors skipAccountForFailedProxyDispatch.
func (e *Engine) SkipAccountForFailedProxyDispatch(failedProxyDispatchKeys map[string]string, account AccountCandidate) *UpstreamAttempt {
	skippedProxyReason := failedProxyDispatchReason(failedProxyDispatchKeys, account)
	if skippedProxyReason == "" {
		return nil
	}

	message := "账户绑定的代理已在本次调度中失败，跳过重复尝试：" + skippedProxyReason
	return &UpstreamAttempt{
		AccountID:                 account.ID,
		AccountName:               account.Name,
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
		UpstreamURL:               "proxy:skipped",
		Message:                   message,
	}
}

func failedProxyDispatchReason(failedProxyDispatchKeys map[string]string, account AccountCandidate) string {
	key := accountProxyDispatchKey(account)
	if key == "" {
		return ""
	}
	return failedProxyDispatchKeys[key]
}

func rememberFailedProxyForDispatch(failedProxyDispatchKeys map[string]string, account AccountCandidate, reason string) {
	key := accountProxyDispatchKey(account)
	if key != "" {
		failedProxyDispatchKeys[key] = reason
	}
}

func accountProxyDispatchKey(account AccountCandidate) string {
	if account.ProxyProfileID != nil && *account.ProxyProfileID != "" {
		return "profile:" + *account.ProxyProfileID
	}
	if account.ProxyURL != nil && *account.ProxyURL != "" {
		return "url:" + *account.ProxyURL
	}
	return ""
}

// throwIfRequestAborted mirrors throwIfRequestAborted.
func throwIfRequestAborted(signal context.Context) error {
	if signal != nil && signal.Err() != nil {
		return &UpstreamRequestAbortedError{Message: "请求已取消"}
	}
	return nil
}

// shouldRecordAbortedUpstreamAttempt mirrors shouldRecordAbortedUpstreamAttempt.
func shouldRecordAbortedUpstreamAttempt(err error) bool {
	var aborted *UpstreamRequestAbortedError
	return errors.As(err, &aborted) && aborted.UpstreamRequestStarted
}

// HandleUnavailableProxyProfile mirrors handleUnavailableProxyProfile.
func (e *Engine) HandleUnavailableProxyProfile(
	ctx context.Context,
	req *gatewaypreauth.GatewayRequest,
	usageContext gatewaypreauth.GatewayFailureUsageContext,
	account AccountCandidate,
	settings gatewayruntimecache.GatewaySettings,
	failedProxyDispatchKeys map[string]string,
	accountStateMutationEnabled bool,
	auditCapture AuditCapture,
	auditAttemptIndex int,
) (*UpstreamAttempt, error) {
	if account.ProxyProfileUnavailable == nil || !*account.ProxyProfileUnavailable {
		return nil, nil
	}

	attemptStartedAt := NowMs()
	message := "账户绑定的代理不可用"
	if account.ProxyProfileErrorMessage != nil && *account.ProxyProfileErrorMessage != "" {
		message = *account.ProxyProfileErrorMessage
	}
	lastAttempt := &UpstreamAttempt{
		AccountID:                 account.ID,
		AccountName:               account.Name,
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
		UpstreamURL:               "proxy:configured",
		Message:                   message,
	}
	if e.Usage != nil {
		if err := e.Usage.RecordFailedUpstreamAttempt(ctx, req, usageContext, account, FailedAttemptRecord{
			UpstreamURL:  "proxy:configured",
			StartedAt:    attemptStartedAt,
			ErrorMessage: message,
		}); err != nil {
			return nil, err
		}
	}
	if !auditCapture.Nil() {
		auditCapture.RecordFailedDispatchAttempt(FailedDispatchAttemptInput{
			Account:                   account,
			AttemptIndex:              auditAttemptIndex,
			UpstreamURL:               "proxy:configured",
			Method:                    req.MethodUpper(),
			StartedAtMs:               attemptStartedAt,
			ErrorPhase:                "dispatch",
			ErrorCode:                 "proxy_unavailable",
			ErrorMessage:              message,
			RequestForModelAccounting: req,
		})
	}
	if accountStateMutationEnabled && usageContext.TrafficSource != "gateway" && e.AccountState != nil {
		if err := e.AccountState.ApplyErrorHandlingWithCacheInvalidation(ctx, account, AccountErrorInput{
			Success:       false,
			ErrorMessage:  message,
			Settings:      settings,
			TrafficSource: usageContext.TrafficSource,
		}); err != nil {
			return nil, err
		}
	}
	if accountStateMutationEnabled && e.AccountState != nil {
		localSuppression := e.AccountState.SuppressLocally(account, settings, message)
		if usageContext.TrafficSource == "gateway" {
			e.AccountState.RecordFailureForPrecheck(ctx, account, settings, PrecheckFailureInput{
				SystemAccountID:         usageContext.SystemAccountID,
				GroupID:                 usageContext.GroupID,
				APIKeyID:                usageContext.APIKeyID,
				ClientIP:                usageContext.ClientIP,
				Endpoint:                gatewaypreauth.RequestEndpoint(req),
				Reason:                  message,
				ForcePrecheck:           localSuppression.Action == "precheck_required",
				LocalSuppressionDelayMs: localSuppression.DelayMs,
			})
		}
		if e.ProxyHealth != nil {
			if err := e.ProxyHealth.RecordFailureAsync(ctx, account, message); err != nil {
				return nil, err
			}
		}
	}
	rememberFailedProxyForDispatch(failedProxyDispatchKeys, account, message)
	return lastAttempt, nil
}

// PrepareUpstreamAccount mirrors prepareUpstreamAccount.
func (e *Engine) PrepareUpstreamAccount(ctx context.Context, account AccountCandidate) (AccountCandidate, error) {
	return e.Driver.PrepareGatewayUpstreamAccount(ctx, account)
}

// SelectAccountApiKeyForDispatch mirrors selectAccountApiKeyForDispatch.
func (e *Engine) SelectAccountApiKeyForDispatch(ctx context.Context, account AccountCandidate, options SelectApiKeyOptions) (AccountCandidate, bool, error) {
	if account.Type != "api_key" {
		return account, true, nil
	}

	accountID := account.ID
	if account.CredentialSourceAccountID != nil && trimString(*account.CredentialSourceAccountID) != "" {
		accountID = trimString(*account.CredentialSourceAccountID)
	}
	credentials := accountApiKeySelectionCredentials(account)
	apiKeyEntries := accountApiKeyEntries(e.Config.Secret, credentials)
	apiKeyPoolIsolationEnabled := isAccountApiKeyPoolIsolationEnabled(account, apiKeyEntries)
	fixedFingerprint := ""
	if account.SelectedAPIKeyFingerprint != nil {
		fixedFingerprint = trimString(*account.SelectedAPIKeyFingerprint)
	}
	if fixedFingerprint != "" && apiKeyPoolIsolationEnabled {
		if _, excluded := options.ExcludeFingerprints[fixedFingerprint]; excluded && options.AllowExcludedFingerprint != fixedFingerprint {
			return AccountCandidate{}, false, nil
		}
		var fixed *apiKeyEntry
		for index := range apiKeyEntries {
			if apiKeyEntries[index].fingerprint == fixedFingerprint {
				fixed = &apiKeyEntries[index]
				break
			}
		}
		if fixed == nil {
			return AccountCandidate{}, false, nil
		}
		return accountWithSelectedApiKey(account, fixed.key, fixed.fingerprint, newIntPtr(fixed.index),
			account.SelectedAPIKeyTransientGeneration, account.SelectedAPIKeyRecoveryStartedAt), true, nil
	}

	transientStates, err := e.Cache.LoadApiKeyTransientStatesForDispatch(ctx, accountID, apiKeyEntryFingerprints(apiKeyEntries))
	if err != nil {
		return AccountCandidate{}, false, err
	}

	runtimeStates := append(append([]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState{}, account.APIKeyRuntimeStates...), transientStates...)
	selected, err := selectAccountRuntimeApiKeyEntry(ctx, apiKeySelectionInput{
		AccountID:                accountID,
		credentials:              credentials,
		ExcludeFingerprints:      options.ExcludeFingerprints,
		ContinueAfterFingerprint: options.ContinueAfterFingerprint,
		RuntimeStates:            runtimeStates,
		entries:                  apiKeyEntries,
		counter:                  e.keyRotationOf(),
	})
	if err != nil {
		return AccountCandidate{}, false, err
	}
	if selected == nil && apiKeyPoolIsolationEnabled {
		return AccountCandidate{}, false, nil
	}
	if selected == nil {
		return account, true, nil
	}

	var transientGeneration *string
	var recoveryStartedAt *string
	if apiKeyPoolIsolationEnabled {
		for _, state := range runtimeStates {
			if state.Fingerprint == selected.fingerprint {
				if state.Generation != nil {
					transientGeneration = state.Generation
				}
				recoveryStartedAt = state.RecoveryStartedAt
			}
		}
	}
	fingerprintOut := selected.fingerprint
	indexOut := selected.index
	if !apiKeyPoolIsolationEnabled {
		fingerprintOut = ""
		indexOut = 0
	}
	return accountWithSelectedApiKey(account, selected.key, fingerprintOut, &indexOut, transientGeneration, recoveryStartedAt), true, nil
}

// SelectApiKeyOptions mirrors the selection options.
type SelectApiKeyOptions struct {
	ExcludeFingerprints      map[string]struct{}
	ContinueAfterFingerprint string
	AllowExcludedFingerprint string
}

type apiKeyEntry struct {
	key         string
	fingerprint string
	index       int
	weight      int
}

// accountApiKeyEntries mirrors accountApiKeyEntries（归档 :117-139）：
// api_keys 池优先（空数组也回落单 api_key），Key 去空白、按去空白文本去重
// 并保留原始 index，weight 取 api_key_weights[index]（normalizeApiKeyWeight）。
func accountApiKeyEntries(secret string, credentials map[string]any) []apiKeyEntry {
	var rawKeys []any
	if list, ok := credentials["api_keys"].([]any); ok && len(list) > 0 {
		rawKeys = list
	} else {
		rawKeys = []any{credentials["api_key"]}
	}
	weights, _ := credentials["api_key_weights"].([]any)
	entries := make([]apiKeyEntry, 0, len(rawKeys))
	seen := map[string]bool{}
	for index, value := range rawKeys {
		text, ok := value.(string)
		if !ok {
			continue
		}
		key := strings.TrimSpace(text)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, apiKeyEntry{
			key:         key,
			fingerprint: apiKeyFingerprint(secret, key),
			index:       index,
			weight:      normalizeAPIKeyWeight(credentialListValue(weights, index)),
		})
	}
	return entries
}

func apiKeyEntryFingerprints(entries []apiKeyEntry) []string {
	fingerprints := make([]string, 0, len(entries))
	for _, entry := range entries {
		fingerprints = append(fingerprints, entry.fingerprint)
	}
	return fingerprints
}

// apiKeyFingerprint 等价 fingerprintAccountApiKey（归档 :173-175，
// createHmac('sha256', runtimeConfig.secret).update(key).digest('hex')），
// 与 accountkeystates.FingerprintAPIKey 逐字节一致（B-1，BUG-0174：原先误用
// 裸 sha256，dispatch 产出的 SelectedAPIKeyFingerprint 与探活/DB Key 状态池
// 必 miss）。Node createHmac 以空 key 正常计算，secret 未注入（空串）时同样
// 不走空串快捷路径；组合根必须与水合层（chainAccountsSelector.secret）注入
// 同一 JUHE_AI_SECRET。
func apiKeyFingerprint(secret, key string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

// normalizeAPIKeyWeight mirrors normalizeApiKeyWeight（归档 :235-237）：
// 1..100 的整数保持，其余（含小数）回落 1。
func normalizeAPIKeyWeight(value any) int {
	number, ok := credentialFloatValue(value)
	if !ok {
		return 1
	}
	if number == float64(int64(number)) && number >= 1 && number <= 100 {
		return int(number)
	}
	return 1
}

// credentialFloatValue narrows the JSON-decoded credential numbers
//（等价 accountkeystates.asFloat 的取值面）。
func credentialFloatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	}
	return 0, false
}

func credentialListValue(values []any, index int) any {
	if index >= 0 && index < len(values) {
		return values[index]
	}
	return nil
}

func accountApiKeySelectionCredentials(account AccountCandidate) map[string]any {
	credentials := make(map[string]any, len(account.Credentials)+2)
	for key, value := range account.Credentials {
		credentials[key] = value
	}
	credentials["api_key"] = account.APIKey
	if len(account.APIKeys) > 0 {
		keys := make([]any, len(account.APIKeys))
		for index, key := range account.APIKeys {
			keys[index] = key
		}
		credentials["api_keys"] = keys
	}
	return credentials
}

// isAccountApiKeyPoolIsolationEnabled mirrors isAccountApiKeyPoolIsolationEnabled
// （归档 :141-155）：仅 api_key 类型 + 受支持 provider/protocol + 池内有效
// Key 数 > 1 时按请求隔离所选 Key。entries 由调用方预算（避免重复 HMAC），
// 计数语义等价 accountApiKeyEntries(credentials).length（去空白去重后）。
//
// M-7（BUG-0174）：废弃旧 dispatch 版「gpt vendor + openai 协议 + 含 OAuth」
// 判据，对齐 accounts/patch_runtime_state.go isAccountAPIKeyPoolIsolationEnabled
// 与 cmd/juhe-ai-gateway/chain_accounts_secret.go
// chainAccountAPIKeyPoolIsolationEnabled（后两处本波不改；cmd 版额外放行
// xai/hybrid，偏差披露见该文件）。三处为同源副本，未抽公共函数的原因：
// 允许修改面仅 gatewaydispatch（accounts/cmd/shared 均在本波边界外）。
func isAccountApiKeyPoolIsolationEnabled(account AccountCandidate, entries []apiKeyEntry) bool {
	if account.Type != "api_key" {
		return false
	}
	if !isAccountAPIKeyPoolProviderSupported(account.ProviderCode, account.ProtocolCode, account.ProtocolVersion) {
		return false
	}
	return len(entries) > 1
}

// isAccountAPIKeyPoolProviderSupported mirrors
// isAccountApiKeyPoolProviderSupported（归档 :157-171）：openai-compatible
// 供应商码族（openai/gpt）、deepseek、glm、gemini、anthropic 供应商，或
// anthropic 协议 profile（protocolCode=anthropic 且 protocolVersion=v1）。
func isAccountAPIKeyPoolProviderSupported(providerCode, protocolCode, protocolVersion string) bool {
	switch normalizeProviderToken(providerCode) {
	case "openai", "gpt", "deepseek", "glm", "gemini", "anthropic":
		return true
	}
	return normalizeProviderToken(protocolCode) == "anthropic" &&
		normalizeProviderToken(protocolVersion) == "v1"
}

func accountWithSelectedApiKey(
	account AccountCandidate,
	apiKey string,
	selectedAPIKeyFingerprint string,
	selectedAPIKeyIndex *int,
	selectedAPIKeyTransientGeneration *string,
	selectedAPIKeyRecoveryStartedAt *string,
) AccountCandidate {
	account.APIKey = apiKey
	if selectedAPIKeyFingerprint != "" {
		account.SelectedAPIKeyFingerprint = &selectedAPIKeyFingerprint
	} else {
		account.SelectedAPIKeyFingerprint = nil
	}
	account.SelectedAPIKeyIndex = selectedAPIKeyIndex
	account.SelectedAPIKeyTransientGeneration = selectedAPIKeyTransientGeneration
	account.SelectedAPIKeyRecoveryStartedAt = selectedAPIKeyRecoveryStartedAt
	credentials := make(map[string]any, len(account.Credentials)+1)
	for key, value := range account.Credentials {
		credentials[key] = value
	}
	credentials["api_key"] = apiKey
	account.Credentials = credentials
	return account
}

// apiKeySelectionInput mirrors selectAccountRuntimeApiKeyEntry's input.
type apiKeySelectionInput struct {
	AccountID                string
	credentials              map[string]any
	ExcludeFingerprints      map[string]struct{}
	ContinueAfterFingerprint string
	RuntimeStates            []gatewayruntimecache.AccountAPIKeyRuntimeSelectionState
	entries                  []apiKeyEntry
	counter                  APIKeyRotationCounter
}

type selectedApiKeyEntry struct {
	key         string
	fingerprint string
	index       int
}

func cooldownUntilActive(cooldownUntil string) bool {
	parsed := parseIsoDate(cooldownUntil)
	if parsed == nil {
		return false
	}
	return parsed.UnixMilli() > NowMs()
}

// BuildPreparedUpstreamRequestParts mirrors buildPreparedUpstreamRequestParts.
func (e *Engine) BuildPreparedUpstreamRequestParts(
	ctx context.Context,
	req *gatewaypreauth.GatewayRequest,
	account AccountCandidate,
	usageContext gatewaypreauth.GatewayFailureUsageContext,
	requestClientCompatibility string,
) (PreparedRequestParts, error) {
	if e.CodexBridge != nil {
		if err := e.CodexBridge.PrepareContextForAccount(req, account); err != nil {
			return PreparedRequestParts{}, e.wrapCodexPreparationError(ctx, req, usageContext, account, err)
		}
	}
	if !e.defersCodexResponsesHistorySanitizationToOpenAIOAuthWorker(req, account, requestClientCompatibility) {
		e.sanitizeCodexResponsesHistoryForAccount(req, account, requestClientCompatibility)
	}
	parts, err := e.Driver.BuildGatewayUpstreamRequestParts(ctx, req, account, UsageIdentity{
		SystemAccountID: usageContext.SystemAccountID,
		APIKeyID:        usageContext.APIKeyID,
		GroupID:         usageContext.GroupID,
	}, requestClientCompatibility)
	if err != nil {
		return PreparedRequestParts{}, e.wrapCodexPreparationError(ctx, req, usageContext, account, convertBridgeGuidanceError(err))
	}
	body := e.SanitizePreparedCodexResponsesHistoryForAccount(req, account, parts.Body, requestClientCompatibility)
	metadata := PreparedUpstreamBodyMetadata(req, body)
	parts.Body = body
	parts.EffectiveServiceTier = "default"
	if metadata != nil && metadata.ServiceTier != nil {
		parts.EffectiveServiceTier = *metadata.ServiceTier
	}
	if metadata != nil {
		parts.EffectiveReasoningEffort = derefStringPtr(metadata.ReasoningEffort)
	}
	return parts, nil
}

// convertBridgeGuidanceError lifts the openaicompat.BridgeGuidanceError
// payload (Node GatewayAgentGuidanceResponse: 200 agent_guidance) onto the
// gatewaypreauth error the engine's errors.As recognition consumes
// (dispatchsingle / attemptoutcomes guidance branches). accountScoped keeps
// the Node `accountScoped !== false` default (nil reads as true).
func convertBridgeGuidanceError(err error) error {
	var guidanceErr *openaicompat.BridgeGuidanceError
	if !errors.As(err, &guidanceErr) {
		return err
	}
	return &gatewaypreauth.GatewayAgentGuidanceResponse{
		Message:  guidanceErr.Message,
		Code:     guidanceErr.Code,
		Protocol: gatewaypreauth.GatewayAgentGuidanceProtocol(guidanceErr.Protocol),
		Stream:   guidanceErr.Stream,
		Model:    guidanceErr.Model,
	}
}

// wrapCodexPreparationError mirrors the OpenAIOAuthCodexAdapterError branch
// of buildPreparedUpstreamRequestParts's catch.
func (e *Engine) wrapCodexPreparationError(
	ctx context.Context,
	req *gatewaypreauth.GatewayRequest,
	usageContext gatewaypreauth.GatewayFailureUsageContext,
	account AccountCandidate,
	err error,
) error {
	var adapterErr *OpenAIOAuthCodexAdapterError
	if !errors.As(err, &adapterErr) {
		return err
	}
	responseBody := map[string]any{
		"error": map[string]any{
			"message": adapterErr.Message,
			"type":    adapterErr.Type,
			"code":    adapterErr.Code,
		},
	}
	serialized, marshalErr := json.Marshal(responseBody)
	if marshalErr != nil {
		serialized = []byte("{}")
	}
	upstreamURL := "gateway:local-validation"
	if account.Type == "oauth" && isOpenAIProtocolProfileWith(account.ProtocolCode, account.ProtocolVersion) {
		upstreamURL = "openai-oauth-codex:local-validation"
	}
	if e.Usage != nil {
		_ = e.Usage.RecordFailedUpstreamAttempt(ctx, req, usageContext, account, FailedAttemptRecord{
			UpstreamURL:    upstreamURL,
			StartedAt:      NowMs(),
			StatusCode:     adapterErr.StatusCode,
			HasStatusCode:  true,
			BodyText:       string(serialized),
			ErrorMessage:   adapterErr.Message,
		})
	}
	return err
}

func (e *Engine) defersCodexResponsesHistorySanitizationToOpenAIOAuthWorker(
	req *gatewaypreauth.GatewayRequest,
	account AccountCandidate,
	requestClientCompatibility string,
) bool {
	return account.Type == "oauth" &&
		IsGptVendorCode(account.ProviderCode) &&
		isOpenAIProtocolProfileWith(account.ProtocolCode, account.ProtocolVersion) &&
		account.ProviderProtocolProfileID == GPTOpenAIV1ProfileID &&
		requestClientCompatibility == "codex_responses" &&
		GatewayRequestEndpointFamily(req) == "responses"
}

func (e *Engine) sanitizeCodexResponsesHistoryForAccount(
	req *gatewaypreauth.GatewayRequest,
	account AccountCandidate,
	requestClientCompatibility string,
) {
	if requestClientCompatibility != "codex_responses" {
		return
	}
	if GatewayRequestEndpointFamily(req) != "responses" {
		return
	}
	body, ok := gatewaybodyJSONObject(req)
	if !ok {
		return
	}
	items, ok := body["input"].([]any)
	if !ok {
		return
	}
	if SanitizeCodexHistory == nil {
		return
	}
	result := SanitizeCodexHistory(items, SanitizeCodexHistoryOptions{
		Store:                  false,
		TargetScopeKey:         "account:" + account.ID,
		TargetPersistenceScope: "none",
	})
	if !result.Changed {
		return
	}
	updated := make(map[string]any, len(body))
	for key, value := range body {
		updated[key] = value
	}
	updated["input"] = result.Items
	gatewayReplaceJSONBody(req, updated)
}

// SanitizePreparedCodexResponsesHistoryForAccount mirrors
// sanitizePreparedCodexResponsesHistoryForAccount.
func (e *Engine) SanitizePreparedCodexResponsesHistoryForAccount(
	req *gatewaypreauth.GatewayRequest,
	account AccountCandidate,
	body []byte,
	requestClientCompatibility string,
) []byte {
	if body == nil {
		return nil
	}
	if requestClientCompatibility != "codex_responses" {
		return body
	}
	if GatewayRequestEndpointFamily(req) != "responses" {
		return body
	}
	if IsGatewayCodexHistorySanitized(body) {
		return body
	}
	parsed, ok := decodeJSONObject(body)
	if !ok {
		return body
	}
	items, ok := parsed["input"].([]any)
	if !ok {
		return body
	}
	if SanitizeCodexHistory == nil {
		return body
	}
	result := SanitizeCodexHistory(items, SanitizeCodexHistoryOptions{
		Store:                  false,
		TargetScopeKey:         "account:" + account.ID,
		TargetPersistenceScope: "none",
	})
	if !result.Changed {
		return body
	}
	sanitized := make(map[string]any, len(parsed))
	for key, value := range parsed {
		sanitized[key] = value
	}
	sanitized["input"] = result.Items
	return SerializeGatewayJSONObject(sanitized)
}

func gatewayReplaceJSONBody(req *gatewaypreauth.GatewayRequest, body map[string]any) {
	if req == nil || req.Body == nil {
		return
	}
	req.Body.Body = body
}

func derefStringPtr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func newIntPtr(value int) *int { return &value }
