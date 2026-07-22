package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	defaultMutationProcessingTTL = 120 * time.Second
	defaultMutationSucceededTTL  = 60 * time.Second
	defaultMutationFailedTTL     = 10 * time.Second
	maxMutationEntries           = 5000
	mutationCleanupBatchSize     = 128
)

type mutationStatus string

const (
	mutationStatusProcessing mutationStatus = "processing"
	mutationStatusSucceeded  mutationStatus = "succeeded"
	mutationStatusFailed     mutationStatus = "failed"
)

type mutationGuardConfig struct {
	operationKey string
	fingerprint  func(http.ResponseWriter, *http.Request) (any, error)
	failedTTL    *time.Duration
}

type mutationGuardStore struct {
	mu      sync.Mutex
	entries map[string]mutationEntry
}

type mutationEntry struct {
	status    mutationStatus
	expiresAt time.Time
}

func newMutationGuardStore() *mutationGuardStore {
	return &mutationGuardStore{entries: map[string]mutationEntry{}}
}

func (s *mutationGuardStore) Middleware(config mutationGuardConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			fingerprint, err := config.fingerprint(w, r)
			if err != nil {
				var maxBytesErr *http.MaxBytesError
				if errors.As(err, &maxBytesErr) {
					writeMessageError(w, http.StatusRequestEntityTooLarge, "请求体过大")
					return
				}
				writeMessageError(w, http.StatusBadRequest, "请求参数无效")
				return
			}
			actor := "anonymous"
			if authContext, ok := ManagementAuthContextFromRequest(r); ok && strings.TrimSpace(authContext.SystemAccountID) != "" {
				actor = strings.TrimSpace(authContext.SystemAccountID)
			}
			key := strings.Join([]string{
				actor,
				"",
				strings.ToUpper(r.Method),
				config.operationKey,
				hashMutationStableValue(fingerprint),
			}, ":")
			if entry, claimed := s.claim(key); !claimed {
				writeMessageError(w, http.StatusConflict, duplicateMutationMessage(entry.status))
				return
			}

			recorder := &mutationStatusRecorder{ResponseWriter: w}
			completed := false
			defer func() {
				if !completed {
					s.complete(key, mutationStatusFailed, config.failedTTL)
				}
			}()
			next.ServeHTTP(recorder, r)
			statusCode := recorder.statusCode
			if statusCode == 0 {
				statusCode = http.StatusOK
			}
			if statusCode >= 200 && statusCode < 400 {
				s.complete(key, mutationStatusSucceeded, config.failedTTL)
			} else {
				s.complete(key, mutationStatusFailed, config.failedTTL)
			}
			completed = true
		})
	}
}

func (s *mutationGuardStore) claim(key string) (mutationEntry, bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now, mutationCleanupBatchSize)
	if entry, ok := s.entries[key]; ok && entry.expiresAt.After(now) {
		return entry, false
	}
	entry := mutationEntry{status: mutationStatusProcessing, expiresAt: now.Add(defaultMutationProcessingTTL)}
	delete(s.entries, key)
	s.entries[key] = entry
	s.trimLocked(now, key)
	return entry, true
}

func (s *mutationGuardStore) complete(
	key string,
	status mutationStatus,
	failedTTLOverride *time.Duration,
) {
	now := time.Now()
	ttl := defaultMutationFailedTTL
	if status == mutationStatusSucceeded {
		ttl = defaultMutationSucceededTTL
	} else if failedTTLOverride != nil {
		ttl = *failedTTLOverride
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok || entry.status != mutationStatusProcessing {
		return
	}
	if ttl <= 0 {
		delete(s.entries, key)
		return
	}
	entry.status = status
	entry.expiresAt = now.Add(ttl)
	s.entries[key] = entry
}

func (s *mutationGuardStore) cleanupExpiredLocked(now time.Time, limit int) {
	inspected := 0
	for key, entry := range s.entries {
		if inspected >= limit {
			break
		}
		if !entry.expiresAt.After(now) {
			delete(s.entries, key)
		}
		inspected++
	}
}

func (s *mutationGuardStore) trimLocked(now time.Time, protectedKey string) {
	if len(s.entries) <= maxMutationEntries {
		return
	}
	s.cleanupExpiredLocked(now, mutationCleanupBatchSize)
	for key := range s.entries {
		if len(s.entries) <= maxMutationEntries {
			return
		}
		if key == protectedKey {
			continue
		}
		delete(s.entries, key)
	}
}

type mutationStatusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (w *mutationStatusRecorder) WriteHeader(statusCode int) {
	if w.statusCode != 0 {
		return
	}
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *mutationStatusRecorder) Write(body []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func managementProxyCreateMutationGuardConfig() mutationGuardConfig {
	return mutationGuardConfig{
		operationKey: "proxies.create",
		fingerprint: func(w http.ResponseWriter, r *http.Request) (any, error) {
			fields, err := mutationJSONFields(w, r)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"name":     mutationStringField(fields, "name"),
				"type":     mutationStringField(fields, "type"),
				"host":     mutationStringField(fields, "host"),
				"port":     mutationAnyField(fields, "port"),
				"username": mutationStringField(fields, "username"),
				"password": mutationSensitiveFingerprint(mutationStringField(fields, "password")),
			}, nil
		},
	}
}

func managementGroupCreateMutationGuardConfig(scope managementGroupOptionScope) mutationGuardConfig {
	return mutationGuardConfig{
		operationKey: "groups.create",
		fingerprint: func(w http.ResponseWriter, r *http.Request) (any, error) {
			fields, err := managementGroupCreateMutationJSONFields(w, r)
			if err != nil {
				return nil, err
			}
			ownerSystemAccountID := ""
			if authContext, ok := ManagementAuthContextFromRequest(r); ok {
				ownerSystemAccountID = strings.TrimSpace(authContext.SystemAccountID)
			}
			if scope == managementGroupScopeAdmin {
				selectedSystemAccountID := firstManagementQueryText(r.URL.Query(), "systemAccountId")
				if selectedSystemAccountID != "" && selectedSystemAccountID != "all" {
					ownerSystemAccountID = selectedSystemAccountID
				}
			}
			return map[string]any{
				"owner":        ownerSystemAccountID,
				"providerCode": mutationStringField(fields, "providerCode"),
				"name":         mutationStringField(fields, "name"),
			}, nil
		},
	}
}

func managementRouteStrategyCreateMutationGuardConfig(
	scope managementRouteStrategyOptionScope,
) mutationGuardConfig {
	return mutationGuardConfig{
		operationKey: "route_strategies.create",
		fingerprint: func(w http.ResponseWriter, r *http.Request) (any, error) {
			fields, err := managementGroupCreateMutationJSONFields(w, r)
			if err != nil {
				return nil, err
			}
			ownerSystemAccountID := ""
			if authContext, ok := ManagementAuthContextFromRequest(r); ok {
				ownerSystemAccountID = strings.TrimSpace(authContext.SystemAccountID)
			}
			if scope == managementRouteStrategyScopeAdmin {
				selectedSystemAccountID := firstManagementQueryText(
					r.URL.Query(),
					"systemAccountId",
				)
				if selectedSystemAccountID != "" && selectedSystemAccountID != "all" {
					ownerSystemAccountID = selectedSystemAccountID
				}
			}
			return map[string]any{
				"owner": ownerSystemAccountID,
				"name":  mutationStringField(fields, "name"),
			}, nil
		},
	}
}

func managementAPIKeyRefreshMutationGuardConfig(scope managementAPIKeyScope) mutationGuardConfig {
	return mutationGuardConfig{
		operationKey: "api_keys.refresh_key",
		fingerprint: func(_ http.ResponseWriter, r *http.Request) (any, error) {
			ownerSystemAccountID := ""
			if authContext, ok := ManagementAuthContextFromRequest(r); ok {
				ownerSystemAccountID = strings.TrimSpace(authContext.SystemAccountID)
			}
			if scope == managementAPIKeyScopeAdmin {
				selectedSystemAccountID := firstManagementQueryText(r.URL.Query(), "systemAccountId")
				if selectedSystemAccountID == "all" {
					selectedSystemAccountID = ""
				}
				ownerSystemAccountID = selectedSystemAccountID
			}
			return map[string]any{
				"owner": ownerSystemAccountID,
				"id":    strings.TrimSpace(chi.URLParam(r, "id")),
			}, nil
		},
	}
}

func managementAPIKeyCreateMutationGuardConfig(scope managementAPIKeyScope) mutationGuardConfig {
	releaseFailedClaim := time.Duration(0)
	return mutationGuardConfig{
		operationKey: "api_keys.create",
		failedTTL:    &releaseFailedClaim,
		fingerprint: func(_ http.ResponseWriter, r *http.Request) (any, error) {
			validated, ok := managementAPIKeyCreateValidationFromRequest(r)
			if !ok {
				return nil, fmt.Errorf("API Key create request was not validated for scope %d", scope)
			}
			return map[string]any{
				"owner": validated.OwnerSystemAccountID,
				"name":  strings.TrimSpace(validated.Payload.Name),
			}, nil
		},
	}
}

func managementClientIPPolicyMutationGuardConfig(action string) mutationGuardConfig {
	if action != managementClientIPPolicyActionAllowlist &&
		action != managementClientIPPolicyActionUnallowlist &&
		action != managementClientIPPolicyActionBlacklist &&
		action != managementClientIPPolicyActionUnblock {
		panic("unsupported management client IP policy mutation action")
	}
	return mutationGuardConfig{
		operationKey: "client_ip_stats." + action,
		fingerprint: func(w http.ResponseWriter, r *http.Request) (any, error) {
			fields, err := managementGroupCreateMutationJSONFields(w, r)
			if err != nil {
				return nil, err
			}
			fingerprint := map[string]any{
				"ipHash": chi.URLParam(r, "ipHash"),
				"reason": mutationAnyField(fields, "reason"),
			}
			if action == managementClientIPPolicyActionBlacklist {
				fingerprint["durationMinutes"] = mutationNodeJSONNumberField(fields, "durationMinutes")
				fingerprint["durationDays"] = mutationNodeJSONNumberField(fields, "durationDays")
			}
			return fingerprint, nil
		},
	}
}

func managementExternalIntegrationSourceUpdateMutationGuardConfig() mutationGuardConfig {
	return mutationGuardConfig{
		operationKey: "external_integration_sources.update",
		fingerprint: func(w http.ResponseWriter, r *http.Request) (any, error) {
			fields, err := mutationJSONFields(w, r)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"id":         strings.TrimSpace(chi.URLParam(r, "id")),
				"name":       mutationAnyField(fields, "name"),
				"status":     mutationAnyField(fields, "status"),
				"expiresAt":  mutationAnyField(fields, "expiresAt"),
				"rateLimits": mutationNodeJSONValue(mutationAnyField(fields, "rateLimits")),
			}, nil
		},
	}
}

func managementExternalIntegrationSourceCreateMutationGuardConfig() mutationGuardConfig {
	return mutationGuardConfig{
		operationKey: "external_integration_sources.create",
		fingerprint: func(w http.ResponseWriter, r *http.Request) (any, error) {
			fields, err := mutationJSONFields(w, r)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"name": mutationAnyField(fields, "name"),
			}, nil
		},
	}
}

func managementExternalIntegrationSourceTokenCreateMutationGuardConfig() mutationGuardConfig {
	return mutationGuardConfig{
		operationKey: "external_integration_sources.create_token",
		fingerprint: func(w http.ResponseWriter, r *http.Request) (any, error) {
			fields, err := mutationJSONFields(w, r)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"id":        chi.URLParam(r, "id"),
				"name":      mutationAnyField(fields, "name"),
				"expiresAt": mutationAnyField(fields, "expiresAt"),
			}, nil
		},
	}
}

func managementExternalIntegrationSourceTokenUpdateMutationGuardConfig() mutationGuardConfig {
	return mutationGuardConfig{
		operationKey: "external_integration_sources.update_token",
		fingerprint: func(w http.ResponseWriter, r *http.Request) (any, error) {
			fields, err := mutationJSONFieldsWithLimit(w, r, managementExternalIntegrationSourceTokenUpdateMaxBodyBytes)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"id":        chi.URLParam(r, "id"),
				"tokenId":   chi.URLParam(r, "tokenId"),
				"name":      mutationAnyField(fields, "name"),
				"status":    mutationAnyField(fields, "status"),
				"expiresAt": mutationAnyField(fields, "expiresAt"),
			}, nil
		},
	}
}

func managementExternalIntegrationSourceDeleteMutationGuardConfig() mutationGuardConfig {
	return mutationGuardConfig{
		operationKey: "external_integration_sources.delete",
		fingerprint: func(_ http.ResponseWriter, r *http.Request) (any, error) {
			return map[string]any{
				"id": strings.TrimFunc(
					chi.URLParam(r, "id"),
					managementGroupListECMAScriptWhitespace,
				),
			}, nil
		},
	}
}

func managementExternalIntegrationSourceBuiltInResetMutationGuardConfig() mutationGuardConfig {
	return mutationGuardConfig{
		operationKey: "external_integration_sources.reset_builtin_test_token",
		fingerprint: func(http.ResponseWriter, *http.Request) (any, error) {
			return map[string]any{"target": "built_in_test_token"}, nil
		},
	}
}

func managementResponseInspectionPolicyCreateMutationGuardConfig() mutationGuardConfig {
	return mutationGuardConfig{
		operationKey: "response_inspection_policies.create",
		fingerprint: func(w http.ResponseWriter, r *http.Request) (any, error) {
			fields, err := mutationJSONFieldsWithLimit(w, r, responseInspectionPolicyMaxBodyBytes)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"payloadHash": hashMutationStableValue(mutationNodeJSONValue(mutationAnyFields(fields))),
			}, nil
		},
	}
}

func managementResponseInspectionPolicyUpdateMutationGuardConfig() mutationGuardConfig {
	return mutationGuardConfig{
		operationKey: "response_inspection_policies.update",
		fingerprint: func(w http.ResponseWriter, r *http.Request) (any, error) {
			fields, err := mutationJSONFieldsWithLimit(w, r, responseInspectionPolicyMaxBodyBytes)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"id":          chi.URLParam(r, "id"),
				"payloadHash": hashMutationStableValue(mutationNodeJSONValue(mutationAnyFields(fields))),
			}, nil
		},
	}
}

func managementResponseInspectionPolicyDeleteMutationGuardConfig() mutationGuardConfig {
	return mutationGuardConfig{
		operationKey: "response_inspection_policies.delete",
		fingerprint: func(_ http.ResponseWriter, r *http.Request) (any, error) {
			return map[string]any{"id": chi.URLParam(r, "id")}, nil
		},
	}
}

func managementGroupCreateMutationJSONFields(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '[' {
		return map[string]json.RawMessage{}, nil
	}
	return decodeMutationJSONFields(raw)
}

func mutationJSONFields(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, error) {
	return mutationJSONFieldsWithLimit(w, r, 1<<20)
}

func mutationJSONFieldsWithLimit(w http.ResponseWriter, r *http.Request, maxBytes int64) (map[string]json.RawMessage, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return decodeMutationJSONFields(raw)
}

func decodeMutationJSONFields(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing json")
	}
	if fields == nil {
		return nil, fmt.Errorf("json object is required")
	}
	return fields, nil
}

func mutationStringField(fields map[string]json.RawMessage, name string) string {
	raw, ok := fields[name]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func mutationAnyField(fields map[string]json.RawMessage, name string) any {
	raw, ok := fields[name]
	if !ok {
		return nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	return value
}

func mutationAnyFields(fields map[string]json.RawMessage) map[string]any {
	result := make(map[string]any, len(fields))
	for name := range fields {
		result[name] = mutationAnyField(fields, name)
	}
	return result
}

func mutationNodeJSONNumberField(fields map[string]json.RawMessage, name string) any {
	return mutationNodeJSONValue(mutationAnyField(fields, name))
}

func mutationNodeJSONValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		return mutationNodeJSONNumber(typed)
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[key] = mutationNodeJSONValue(item)
		}
		return normalized
	case []any:
		normalized := make([]any, 0, len(typed))
		for _, item := range typed {
			normalized = append(normalized, mutationNodeJSONValue(item))
		}
		return normalized
	default:
		return typed
	}
}

func mutationNodeJSONNumber(number json.Number) any {
	normalized, err := strconv.ParseFloat(number.String(), 64)
	if math.IsInf(normalized, 0) {
		return nil
	}
	if err != nil {
		return number
	}
	if normalized == 0 {
		return float64(0)
	}
	return normalized
}

func mutationSensitiveFingerprint(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return hashMutationStableValue(strings.TrimSpace(value))
}

func hashMutationStableValue(value any) string {
	raw, _ := json.Marshal(normalizeMutationValue(value))
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func normalizeMutationValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		normalized := make([][2]any, 0, len(keys))
		for _, key := range keys {
			normalized = append(normalized, [2]any{key, normalizeMutationValue(typed[key])})
		}
		out := make(map[string]any, len(normalized))
		for _, item := range normalized {
			out[item[0].(string)] = item[1]
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, normalizeMutationValue(item))
		}
		return out
	default:
		return typed
	}
}

func duplicateMutationMessage(status mutationStatus) string {
	switch status {
	case mutationStatusProcessing:
		return "请求正在处理中，请勿重复提交"
	case mutationStatusFailed:
		return "请求刚刚失败，请稍后重试"
	default:
		return "该操作刚刚已处理，请刷新列表查看结果"
	}
}
