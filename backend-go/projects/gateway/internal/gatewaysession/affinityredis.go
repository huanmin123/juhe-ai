package gatewaysession

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// Redis record operations for the session affinity service. The three Lua
// scripts live in redisdriver.go, byte-identical to the Node service.

func (s *AffinityService) redisSessionAffinityClient(ctx context.Context) (RedisClient, error) {
	if s.redis != nil {
		return s.redis, nil
	}
	// Node redisSessionAffinityClient: the URL-less Redis cache driver is a
	// configuration error.
	return nil, fmt.Errorf("JUHE_AI_REDIS_CACHE_URL 在 Redis cache driver 下必须配置")
}

// getRedisSessionAffinityBindingForOrdering mirrors
// getRedisSessionAffinityBindingForOrdering: read failures degrade to no
// binding with a warn.
func (s *AffinityService) getRedisSessionAffinityBindingForOrdering(ctx context.Context, sessionAffinityKey string) *SessionBinding {
	if sessionAffinityKey == "" {
		return nil
	}
	record, err := s.getRedisSessionAffinityRecord(ctx, sessionAffinityKey, true)
	if err != nil {
		s.warn("redis_openai_session_affinity_read_failed", err, nil, "Redis 会话亲和绑定读取失败，已跳过本次亲和排序")
		return nil
	}
	if record == nil {
		return nil
	}
	binding := record.binding
	return &binding
}

// getRedisSessionAffinityRecord mirrors getRedisSessionAffinityRecord.
func (s *AffinityService) getRedisSessionAffinityRecord(ctx context.Context, sessionAffinityKey string, refreshTtl bool) (*redisSessionBindingRecord, error) {
	client, err := s.redisSessionAffinityClient(ctx)
	if err != nil {
		return nil, err
	}
	key, err := redisSessionAffinityBindingKey(s.cfg.RedisNamespace, sessionAffinityKey)
	if err != nil {
		return nil, err
	}
	rawValue, err := client.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if rawValue == nil {
		return nil, nil
	}
	binding := parseRedisSessionBinding(*rawValue)
	if binding == nil {
		if err := client.Del(ctx, key); err != nil {
			return nil, err
		}
		return nil, nil
	}
	record := &redisSessionBindingRecord{binding: *binding, rawValue: *rawValue}
	if refreshTtl {
		if _, err := s.refreshRedisSessionAffinityBinding(ctx, client, sessionAffinityKey, record); err != nil {
			return nil, err
		}
	}
	return record, nil
}

// setRedisSessionAffinityBinding mirrors setRedisSessionAffinityBinding.
func (s *AffinityService) setRedisSessionAffinityBinding(ctx context.Context, sessionAffinityKey string, binding SessionBinding, previous *redisSessionBindingRecord) (bool, error) {
	client, err := s.redisSessionAffinityClient(ctx)
	if err != nil {
		return false, err
	}
	priorRecord := previous
	if priorRecord == nil {
		priorRecord, err = s.getRedisSessionAffinityRecord(ctx, sessionAffinityKey, false)
		if err != nil {
			return false, err
		}
	}
	var oldIndexKeys []string
	if priorRecord != nil {
		oldIndexKeys = redisSessionAffinityIndexKeysForBinding(s.cfg.RedisNamespace, priorRecord.binding)
	}
	newIndexKeys := redisSessionAffinityIndexKeysForBinding(s.cfg.RedisNamespace, binding)
	bindingKey, err := redisSessionAffinityBindingKey(s.cfg.RedisNamespace, sessionAffinityKey)
	if err != nil {
		return false, err
	}
	keys := make([]string, 0, 1+len(oldIndexKeys)+len(newIndexKeys))
	keys = append(keys, bindingKey)
	keys = append(keys, oldIndexKeys...)
	keys = append(keys, newIndexKeys...)
	expected := redisMissingBindingExpectedValue
	if priorRecord != nil {
		expected = priorRecord.rawValue
	}
	result, err := client.Eval(ctx, redisSetSessionAffinityBindingScript, keys,
		expected,
		marshalSessionBinding(binding),
		strconv.FormatInt(sessionAffinityTtlMs, 10),
		strconv.FormatInt(sessionAffinityTtlMs+redisSessionAffinityIndexTtlPaddingMs, 10),
		strconv.FormatInt(s.clock().UnixMilli()+sessionAffinityTtlMs, 10),
		strconv.Itoa(len(oldIndexKeys)),
		sessionAffinityKey,
	)
	if err != nil {
		return false, err
	}
	return redisBooleanResult(result), nil
}

// deleteRedisSessionAffinityBinding mirrors deleteRedisSessionAffinityBinding.
func (s *AffinityService) deleteRedisSessionAffinityBinding(ctx context.Context, client RedisClient, sessionAffinityKey string, record *redisSessionBindingRecord) (bool, error) {
	bindingKey, err := redisSessionAffinityBindingKey(s.cfg.RedisNamespace, sessionAffinityKey)
	if err != nil {
		return false, err
	}
	keys := append([]string{bindingKey}, redisSessionAffinityIndexKeysForBinding(s.cfg.RedisNamespace, record.binding)...)
	result, err := client.Eval(ctx, redisDeleteSessionAffinityBindingScript, keys, record.rawValue, sessionAffinityKey)
	if err != nil {
		return false, err
	}
	return redisBooleanResult(result), nil
}

// refreshRedisSessionAffinityBinding mirrors refreshRedisSessionAffinityBinding.
func (s *AffinityService) refreshRedisSessionAffinityBinding(ctx context.Context, client RedisClient, sessionAffinityKey string, record *redisSessionBindingRecord) (bool, error) {
	bindingKey, err := redisSessionAffinityBindingKey(s.cfg.RedisNamespace, sessionAffinityKey)
	if err != nil {
		return false, err
	}
	keys := append([]string{bindingKey}, redisSessionAffinityIndexKeysForBinding(s.cfg.RedisNamespace, record.binding)...)
	result, err := client.Eval(ctx, redisRefreshSessionAffinityBindingScript, keys,
		record.rawValue,
		strconv.FormatInt(sessionAffinityTtlMs, 10),
		strconv.FormatInt(s.clock().UnixMilli()+sessionAffinityTtlMs, 10),
		sessionAffinityKey,
		strconv.FormatInt(sessionAffinityTtlMs+redisSessionAffinityIndexTtlPaddingMs, 10),
	)
	if err != nil {
		return false, err
	}
	return redisBooleanResult(result), nil
}

// redisSessionAffinityIndexKeysForBinding mirrors
// redisSessionAffinityIndexKeysForBinding.
func redisSessionAffinityIndexKeysForBinding(namespace string, binding SessionBinding) []string {
	keys := []string{mustRedisSessionAffinityAccountIndexKey(namespace, binding.AccountID)}
	if binding.Scope != nil && binding.Scope.SystemAccountID != "" {
		keys = append(keys, mustRedisSessionAffinityAccountSystemIndexKey(namespace, binding.AccountID, binding.Scope.SystemAccountID))
		if binding.Scope.APIKeyID != "" {
			keys = append(keys, mustRedisSessionAffinityAccountSystemAPIKeyIndexKey(namespace, binding.AccountID, binding.Scope.SystemAccountID, binding.Scope.APIKeyID))
		}
	}
	return keys
}

// redisSessionAffinityMigrationCandidateKeys mirrors
// redisSessionAffinityMigrationCandidateKeys.
func (s *AffinityService) redisSessionAffinityMigrationCandidateKeys(ctx context.Context, sourceAccountID string, scope *OpenAIGatewaySessionAffinityScope) ([]string, error) {
	indexKey, err := redisSessionAffinityMigrationIndexKey(s.cfg.RedisNamespace, sourceAccountID, scope)
	if err != nil {
		return nil, err
	}
	now := s.clock().UnixMilli()
	client, err := s.redisSessionAffinityClient(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := client.SendCommand(ctx, "ZREMRANGEBYSCORE", indexKey, "-inf", strconv.FormatInt(now-1, 10)); err != nil {
		return nil, err
	}
	result, err := client.SendCommand(ctx, "ZRANGEBYSCORE", indexKey, strconv.FormatInt(now, 10), "+inf")
	if err != nil {
		return nil, err
	}
	return stringArrayRedisResult(result), nil
}

func redisSessionAffinityMigrationIndexKey(namespace string, sourceAccountID string, scope *OpenAIGatewaySessionAffinityScope) (string, error) {
	if scope != nil && scope.SystemAccountID != "" && scope.APIKeyID != "" {
		return redisSessionAffinityAccountSystemAPIKeyIndexKey(namespace, sourceAccountID, scope.SystemAccountID, scope.APIKeyID)
	}
	if scope != nil && scope.SystemAccountID != "" {
		return redisSessionAffinityAccountSystemIndexKey(namespace, sourceAccountID, scope.SystemAccountID)
	}
	return redisSessionAffinityAccountIndexKey(namespace, sourceAccountID)
}

// Traffic migration preference redis operations ------------------------------

func (s *AffinityService) setRedisTrafficMigrationPreference(ctx context.Context, scopeKey string, preference TrafficMigrationPreference) error {
	client, err := s.redisSessionAffinityClient(ctx)
	if err != nil {
		return err
	}
	key, err := redisTrafficMigrationPreferenceKey(s.cfg.RedisNamespace, scopeKey)
	if err != nil {
		return err
	}
	return client.SetPX(ctx, key, marshalTrafficMigrationPreference(preference), trafficMigrationPreferenceTtlMs)
}

// getRedisTrafficMigrationPreference mirrors getRedisTrafficMigrationPreference.
func (s *AffinityService) getRedisTrafficMigrationPreference(ctx context.Context, scopeKey string) (*TrafficMigrationPreference, error) {
	client, err := s.redisSessionAffinityClient(ctx)
	if err != nil {
		return nil, err
	}
	key, err := redisTrafficMigrationPreferenceKey(s.cfg.RedisNamespace, scopeKey)
	if err != nil {
		return nil, err
	}
	rawValue, err := client.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if rawValue == nil {
		return nil, nil
	}
	preference := parseRedisTrafficMigrationPreference(*rawValue)
	if preference == nil {
		if err := client.Del(ctx, key); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if _, err := client.SendCommand(ctx, "PEXPIRE", key, strconv.FormatInt(trafficMigrationPreferenceTtlMs, 10)); err != nil {
		return nil, err
	}
	return preference, nil
}

func (s *AffinityService) deleteRedisTrafficMigrationPreference(ctx context.Context, scopeKey string) error {
	client, err := s.redisSessionAffinityClient(ctx)
	if err != nil {
		return err
	}
	key, err := redisTrafficMigrationPreferenceKey(s.cfg.RedisNamespace, scopeKey)
	if err != nil {
		return err
	}
	return client.Del(ctx, key)
}

// trafficMigrationPreferenceForAccounts mirrors trafficMigrationPreferenceForAccounts
// (process-local driver).
func (s *AffinityService) trafficMigrationPreferenceForAccounts(accountIDs []string, scope *OpenAIGatewaySessionAffinityScope) *TrafficMigrationPreference {
	if !s.canUseProcessLocalSessionAffinity() {
		return nil
	}
	if len(accountIDs) < 2 {
		return nil
	}
	s.mu.Lock()
	scopedKey, preference := s.trafficMigrationPreferenceForScopeLocked(scope)
	s.mu.Unlock()
	if preference == nil {
		return nil
	}
	if containsString(accountIDs, preference.SourceAccountID) {
		s.mu.Lock()
		s.trafficMigrationPreference.Delete(scopedKey)
		s.mu.Unlock()
		return nil
	}
	if containsString(accountIDs, preference.TargetAccountID) {
		return preference
	}
	return nil
}

// trafficMigrationPreferenceForAccountsAsync mirrors
// trafficMigrationPreferenceForAccountsAsync (Redis driver); read failures
// degrade to no preference with a warn.
func (s *AffinityService) trafficMigrationPreferenceForAccountsAsync(ctx context.Context, accountIDs []string, scope *OpenAIGatewaySessionAffinityScope) *TrafficMigrationPreference {
	if len(accountIDs) < 2 {
		return nil
	}
	scopedKey, preference, err := s.trafficMigrationPreferenceForScopeAsync(ctx, scope)
	if err != nil {
		s.warn("redis_openai_traffic_migration_preference_read_failed", err, nil, "Redis 流量迁移偏向读取失败，已跳过本次偏向排序")
		return nil
	}
	if preference == nil {
		return nil
	}
	if containsString(accountIDs, preference.SourceAccountID) {
		_ = s.deleteRedisTrafficMigrationPreference(ctx, scopedKey)
		return nil
	}
	if containsString(accountIDs, preference.TargetAccountID) {
		return preference
	}
	return nil
}

// trafficMigrationPreferenceForScopeLocked mirrors
// trafficMigrationPreferenceForScope. Caller must hold s.mu.
func (s *AffinityService) trafficMigrationPreferenceForScopeLocked(scope *OpenAIGatewaySessionAffinityScope) (string, *TrafficMigrationPreference) {
	for _, key := range trafficMigrationPreferenceScopeKeys(scope) {
		if preference, ok := s.trafficMigrationPreference.Get(key); ok {
			return key, &preference
		}
	}
	return "", nil
}

// trafficMigrationPreferenceForScopeAsync mirrors
// trafficMigrationPreferenceForScopeAsync.
func (s *AffinityService) trafficMigrationPreferenceForScopeAsync(ctx context.Context, scope *OpenAIGatewaySessionAffinityScope) (string, *TrafficMigrationPreference, error) {
	for _, key := range trafficMigrationPreferenceScopeKeys(scope) {
		preference, err := s.getRedisTrafficMigrationPreference(ctx, key)
		if err != nil {
			return "", nil, err
		}
		if preference != nil {
			return key, preference, nil
		}
	}
	return "", nil, nil
}

// sessionTrafficMigrationTargetForAccounts mirrors
// sessionTrafficMigrationTargetForAccounts (process-local driver).
func (s *AffinityService) sessionTrafficMigrationTargetForAccounts(accountIDs []string, sessionAffinityKey string) string {
	if !s.canUseProcessLocalSessionAffinity() {
		return ""
	}
	if sessionAffinityKey == "" || len(accountIDs) < 2 {
		return ""
	}
	s.mu.Lock()
	binding := s.sessionAffinityCacheGetLocked(sessionAffinityKey)
	s.mu.Unlock()
	if binding == nil || !binding.TrafficMigrationPreferred {
		return ""
	}
	return sessionTrafficMigrationTargetForAccountsFromBinding(accountIDs, binding)
}

// sessionTrafficMigrationTargetForAccountsFromBinding mirrors
// sessionTrafficMigrationTargetForAccountsFromBinding.
func sessionTrafficMigrationTargetForAccountsFromBinding(accountIDs []string, binding *SessionBinding) string {
	if binding == nil || !binding.TrafficMigrationPreferred {
		return ""
	}
	if containsString(accountIDs, binding.AccountID) {
		return binding.AccountID
	}
	return ""
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// Redis key builders ---------------------------------------------------------

func redisSessionAffinityBindingKey(namespace string, sessionAffinityKey string) (string, error) {
	prefix, err := RedisNamespacedKey(namespace, "juhe-ai:session-affinity:binding:")
	if err != nil {
		return "", err
	}
	return prefix + redisSessionAffinityKeyPart(sessionAffinityKey), nil
}

func redisSessionAffinityAccountIndexKey(namespace string, accountID string) (string, error) {
	prefix, err := RedisNamespacedKey(namespace, "juhe-ai:session-affinity:index:account:")
	if err != nil {
		return "", err
	}
	return prefix + redisSessionAffinityKeyPart(accountID), nil
}

func redisSessionAffinityAccountSystemIndexKey(namespace string, accountID string, systemAccountID string) (string, error) {
	prefix, err := RedisNamespacedKey(namespace, "juhe-ai:session-affinity:index:account-system:")
	if err != nil {
		return "", err
	}
	return prefix + redisSessionAffinityKeyPart(accountID) + ":" + redisSessionAffinityKeyPart(systemAccountID), nil
}

func redisSessionAffinityAccountSystemAPIKeyIndexKey(namespace string, accountID string, systemAccountID string, apiKeyID string) (string, error) {
	prefix, err := RedisNamespacedKey(namespace, "juhe-ai:session-affinity:index:account-system-api-key:")
	if err != nil {
		return "", err
	}
	return prefix + redisSessionAffinityKeyPart(accountID) + ":" + redisSessionAffinityKeyPart(systemAccountID) + ":" + redisSessionAffinityKeyPart(apiKeyID), nil
}

func redisTrafficMigrationPreferenceKey(namespace string, scopeKey string) (string, error) {
	prefix, err := RedisNamespacedKey(namespace, "juhe-ai:traffic-migration-preference:")
	if err != nil {
		return "", err
	}
	return prefix + redisSessionAffinityKeyPart(scopeKey), nil
}

func mustRedisSessionAffinityAccountIndexKey(namespace string, accountID string) string {
	key, _ := redisSessionAffinityAccountIndexKey(namespace, accountID)
	return key
}

func mustRedisSessionAffinityAccountSystemIndexKey(namespace string, accountID string, systemAccountID string) string {
	key, _ := redisSessionAffinityAccountSystemIndexKey(namespace, accountID, systemAccountID)
	return key
}

func mustRedisSessionAffinityAccountSystemAPIKeyIndexKey(namespace string, accountID string, systemAccountID string, apiKeyID string) string {
	key, _ := redisSessionAffinityAccountSystemAPIKeyIndexKey(namespace, accountID, systemAccountID, apiKeyID)
	return key
}

// redisSessionAffinityKeyPart mirrors redisSessionAffinityKeyPart.
func redisSessionAffinityKeyPart(value string) string {
	encoded := encodeURIComponent(value)
	if encoded == "" {
		return "default"
	}
	return encoded
}

// Redis result helpers -------------------------------------------------------

// stringArrayRedisResult mirrors stringArrayRedisResult.
func stringArrayRedisResult(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text := fmt.Sprintf("%v", item)
		if text == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}

// redisBooleanResult mirrors redisBooleanResult.
func redisBooleanResult(value any) bool {
	switch result := value.(type) {
	case int64:
		return result == 1
	case string:
		return result == "1"
	case bool:
		return result
	default:
		return false
	}
}

// Binding / preference JSON codecs ------------------------------------------
// Field order and optionality mirror JSON.stringify of the Node objects:
// { accountId, scope?, trafficMigrationPreferred? } with scope written as
// { systemAccountId, apiKeyId?, groupId }.

type sessionBindingJSON struct {
	AccountID                 string                                 `json:"accountId"`
	Scope                     *openAIGatewaySessionAffinityScopeJSON `json:"scope,omitempty"`
	TrafficMigrationPreferred *bool                                  `json:"trafficMigrationPreferred,omitempty"`
}

// openAIGatewaySessionAffinityScopeJSON keeps the explicit field order.
type openAIGatewaySessionAffinityScopeJSON struct {
	SystemAccountID string  `json:"systemAccountId"`
	APIKeyID        *string `json:"apiKeyId,omitempty"`
	GroupID         string  `json:"groupId"`
}

func marshalSessionBinding(binding SessionBinding) string {
	payload := sessionBindingJSON{AccountID: binding.AccountID}
	if binding.Scope != nil {
		scopeJSON := &openAIGatewaySessionAffinityScopeJSON{
			SystemAccountID: binding.Scope.SystemAccountID,
			GroupID:         binding.Scope.GroupID,
		}
		if binding.Scope.APIKeyID != "" {
			apiKeyID := binding.Scope.APIKeyID
			scopeJSON.APIKeyID = &apiKeyID
		}
		payload.Scope = scopeJSON
	}
	if binding.TrafficMigrationPreferred {
		flag := true
		payload.TrafficMigrationPreferred = &flag
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		// A struct payload cannot fail marshaling.
		return "{}"
	}
	return string(encoded)
}

// parseRedisSessionBinding mirrors parseRedisSessionBinding: JSON parse
// failures yield no binding; a malformed scope or flag only drops that field,
// exactly like the Node per-field typeof checks.
func parseRedisSessionBinding(rawValue string) *SessionBinding {
	var parsed struct {
		AccountID                 json.RawMessage `json:"accountId"`
		Scope                     json.RawMessage `json:"scope"`
		TrafficMigrationPreferred json.RawMessage `json:"trafficMigrationPreferred"`
	}
	if err := json.Unmarshal([]byte(rawValue), &parsed); err != nil {
		return nil
	}
	var accountID any
	if len(parsed.AccountID) > 0 {
		if err := json.Unmarshal(parsed.AccountID, &accountID); err != nil {
			return nil
		}
	}
	accountIDValue, accountOK := stringValueFromAny(accountID)
	if !accountOK {
		return nil
	}
	binding := &SessionBinding{AccountID: accountIDValue}
	if scope := parseRedisSessionBindingScope(parsed.Scope); scope != nil {
		binding.Scope = scope
	}
	if len(parsed.TrafficMigrationPreferred) > 0 {
		var flag any
		if err := json.Unmarshal(parsed.TrafficMigrationPreferred, &flag); err == nil && flag == true {
			binding.TrafficMigrationPreferred = true
		}
	}
	return binding
}

// parseRedisSessionBindingScope mirrors parseRedisSessionBindingScope: the
// value must be a non-array object with string systemAccountId and groupId.
func parseRedisSessionBindingScope(raw json.RawMessage) *OpenAIGatewaySessionAffinityScope {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	systemAccountID, systemOK := stringValueFromAny(record["systemAccountId"])
	groupID, groupOK := stringValueFromAny(record["groupId"])
	if !systemOK || !groupOK {
		return nil
	}
	scope := &OpenAIGatewaySessionAffinityScope{
		SystemAccountID: systemAccountID,
		GroupID:         groupID,
	}
	if apiKeyID, apiKeyOK := stringValueFromAny(record["apiKeyId"]); apiKeyOK {
		scope.APIKeyID = apiKeyID
	}
	return scope
}

// stringValueFromAny mirrors the Node stringValue helper over unknown JSON:
// strings trim to non-empty, everything else is absent.
func stringValueFromAny(value any) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return stringValue(text)
}

func marshalTrafficMigrationPreference(preference TrafficMigrationPreference) string {
	encoded, err := json.Marshal(struct {
		SourceAccountID string `json:"sourceAccountId"`
		TargetAccountID string `json:"targetAccountId"`
	}{
		SourceAccountID: preference.SourceAccountID,
		TargetAccountID: preference.TargetAccountID,
	})
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// parseRedisTrafficMigrationPreference mirrors
// parseRedisTrafficMigrationPreference with per-field typeof checks.
func parseRedisTrafficMigrationPreference(rawValue string) *TrafficMigrationPreference {
	var parsed struct {
		SourceAccountID json.RawMessage `json:"sourceAccountId"`
		TargetAccountID json.RawMessage `json:"targetAccountId"`
	}
	if err := json.Unmarshal([]byte(rawValue), &parsed); err != nil {
		return nil
	}
	var source any
	var target any
	if len(parsed.SourceAccountID) > 0 {
		_ = json.Unmarshal(parsed.SourceAccountID, &source)
	}
	if len(parsed.TargetAccountID) > 0 {
		_ = json.Unmarshal(parsed.TargetAccountID, &target)
	}
	sourceID, sourceOK := stringValueFromAny(source)
	targetID, targetOK := stringValueFromAny(target)
	if !sourceOK || !targetOK {
		return nil
	}
	return &TrafficMigrationPreference{
		SourceAccountID: sourceID,
		TargetAccountID: targetID,
	}
}
