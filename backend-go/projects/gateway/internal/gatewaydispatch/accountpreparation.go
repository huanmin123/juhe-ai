package gatewaydispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
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
	apiKeyEntries := accountApiKeyEntries(credentials)
	apiKeyPoolIsolationEnabled := isAccountApiKeyPoolIsolationEnabled(account, credentials)
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
	selected, err := selectAccountRuntimeApiKeyEntry(apiKeySelectionInput{
		AccountID:               accountID,
		credentials:             credentials,
		ExcludeFingerprints:     options.ExcludeFingerprints,
		ContinueAfterFingerprint: options.ContinueAfterFingerprint,
		RuntimeStates:           runtimeStates,
		entries:                 apiKeyEntries,
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
}

func accountApiKeyEntries(credentials map[string]any) []apiKeyEntry {
	// storage/account-api-key-rotation.ts accountApiKeyEntries: api_keys
	// array with stable fingerprints (sha256 of the key text); a bare
	// api_key contributes a single entry.
	raw, ok := credentials["api_keys"].([]any)
	if !ok {
		single, ok := credentials["api_key"].(string)
		if !ok || single == "" {
			return nil
		}
		return []apiKeyEntry{{key: single, fingerprint: apiKeyFingerprint(single), index: 0}}
	}
	entries := make([]apiKeyEntry, 0, len(raw))
	for index, item := range raw {
		key, ok := item.(string)
		if !ok || key == "" {
			continue
		}
		entries = append(entries, apiKeyEntry{key: key, fingerprint: apiKeyFingerprint(key), index: index})
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

func apiKeyFingerprint(key string) string {
	// storage/account-api-key-rotation.ts fingerprintKeyApiKey: sha256 hex of
	// the key text.
	digest := sha256HexBytes([]byte(key))
	return digest
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

func isAccountApiKeyPoolIsolationEnabled(account AccountCandidate, credentials map[string]any) bool {
	// storage/account-api-key-rotation.ts isAccountApiKeyPoolIsolationEnabled:
	// gpt-provider OpenAI protocol OAuth/api_key accounts with a configured
	// pool isolate the selected key per request.
	if _, ok := credentials["api_keys"]; !ok {
		return false
	}
	if account.Type == "oauth" {
		return IsGptVendorCode(account.ProviderCode) && isOpenAIProtocolProfileWith(account.ProtocolCode, account.ProtocolVersion)
	}
	return account.Type == "api_key" && IsGptVendorCode(account.ProviderCode) &&
		isOpenAIProtocolProfileWith(account.ProtocolCode, account.ProtocolVersion)
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

// apiKeySelectionInput mirrors selectAccountRuntimeApiKeyEntryAsync's input.
type apiKeySelectionInput struct {
	AccountID                string
	credentials              map[string]any
	ExcludeFingerprints      map[string]struct{}
	ContinueAfterFingerprint string
	RuntimeStates            []gatewayruntimecache.AccountAPIKeyRuntimeSelectionState
	entries                  []apiKeyEntry
}

type selectedApiKeyEntry struct {
	key         string
	fingerprint string
	index       int
}

// selectAccountRuntimeApiKeyEntry mirrors
// selectAccountRuntimeApiKeyEntryAsync: prefer the persisted selection, then
// recovery states, then the next non-excluded entry.
func selectAccountRuntimeApiKeyEntry(input apiKeySelectionInput) (*selectedApiKeyEntry, error) {
	excluded := input.ExcludeFingerprints
	if excluded == nil {
		excluded = map[string]struct{}{}
	}
	stateByFingerprint := make(map[string]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, len(input.RuntimeStates))
	for _, state := range input.RuntimeStates {
		stateByFingerprint[state.Fingerprint] = state
	}

	ordered := make([]apiKeyEntry, 0, len(input.entries))
	if input.ContinueAfterFingerprint != "" {
		afterIndex := -1
		for index, entry := range input.entries {
			if entry.fingerprint == input.ContinueAfterFingerprint {
				afterIndex = index
				break
			}
		}
		for index := afterIndex + 1; index < len(input.entries); index++ {
			ordered = append(ordered, input.entries[index])
		}
		for index := 0; index <= afterIndex; index++ {
			ordered = append(ordered, input.entries[index])
		}
	} else {
		ordered = append(ordered, input.entries...)
	}

	var healthy *selectedApiKeyEntry
	var recovery *selectedApiKeyEntry
	for _, entry := range ordered {
		if _, isExcluded := excluded[entry.fingerprint]; isExcluded {
			continue
		}
		state, hasState := stateByFingerprint[entry.fingerprint]
		if hasState && state.Disabled {
			continue
		}
		cooldownActive := false
		if hasState && state.CooldownUntil != nil {
			cooldownActive = cooldownUntilActive(*state.CooldownUntil)
		}
		candidate := &selectedApiKeyEntry{key: entry.key, fingerprint: entry.fingerprint, index: entry.index}
		if cooldownActive {
			if recovery == nil {
				recovery = candidate
			}
			continue
		}
		if healthy == nil {
			healthy = candidate
		}
	}
	if healthy != nil {
		return healthy, nil
	}
	return recovery, nil
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
		return PreparedRequestParts{}, e.wrapCodexPreparationError(ctx, req, usageContext, account, err)
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
