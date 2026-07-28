package managementaccountdetails

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

type Level string

const (
	LevelEditBasic Level = "edit-basic"
	LevelAdvanced  Level = "advanced"
)

var (
	ErrCredentialsForbidden = errors.New("management account credentials forbidden")
	ErrRuntimeForbidden     = errors.New("management account api key runtime forbidden")
)

type CredentialCodec interface {
	DecryptJSON(value string) (map[string]any, error)
}

type ServiceOptions struct {
	Reader            port.ManagementAccountDetailReader
	CredentialCodec   CredentialCodec
	FingerprintSecret string
	Now               func() time.Time
}

type Service struct {
	reader            port.ManagementAccountDetailReader
	credentialCodec   CredentialCodec
	fingerprintSecret string
	now               func() time.Time
}

type Input struct {
	AccountID       string
	SystemAccountID string
}

type ResourcePermissions struct {
	CanUse                 bool `json:"canUse"`
	CanEdit                bool `json:"canEdit"`
	CanDelete              bool `json:"canDelete"`
	CanReturnAuthorization bool `json:"canReturnAuthorization"`
	CanAuthorize           bool `json:"canAuthorize"`
	CanViewCredentials     bool `json:"canViewCredentials"`
	CanManageAccounts      bool `json:"canManageAccounts"`
	CanBindToAPIKey        bool `json:"canBindToApiKey"`
}

type APIKeyRuntimeResponse struct {
	AccountID      string                `json:"accountId"`
	ConfigRevision int                   `json:"configRevision"`
	Items          []APIKeyRuntimeDetail `json:"items"`
}

type APIKeyRuntimeDetail struct {
	KeyIndex             int    `json:"keyIndex"`
	KeyFingerprintPrefix string `json:"keyFingerprintPrefix"`
	KeySuffix            string `json:"keySuffix,omitempty"`
	Weight               int    `json:"weight"`
	Status               string `json:"status"`
	FailureCount         int    `json:"failureCount"`
	ConsecutiveFailures  int    `json:"consecutiveFailures"`
	SuccessCount         int64  `json:"successCount"`
	CooldownUntil        string `json:"cooldownUntil,omitempty"`
	NextProbeAt          string `json:"nextProbeAt,omitempty"`
	LastAttemptAt        string `json:"lastAttemptAt,omitempty"`
	LastSuccessAt        string `json:"lastSuccessAt,omitempty"`
	LastFailureAt        string `json:"lastFailureAt,omitempty"`
	LastErrorCode        string `json:"lastErrorCode,omitempty"`
	LastErrorMessage     string `json:"lastErrorMessage,omitempty"`
	LastTraceID          string `json:"lastTraceId,omitempty"`
}

type apiKeyEntry struct {
	index       int
	key         string
	fingerprint string
	weight      int
}

func NewService(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		reader:            opts.Reader,
		credentialCodec:   opts.CredentialCodec,
		fingerprintSecret: opts.FingerprintSecret,
		now:               now,
	}
}

func (s *Service) Get(ctx context.Context, input Input, level Level) (map[string]any, bool, error) {
	if s.reader == nil {
		return nil, false, fmt.Errorf("management account detail reader is required")
	}
	if level != LevelEditBasic && level != LevelAdvanced {
		return nil, false, fmt.Errorf("unsupported management account detail level: %s", level)
	}
	source, found, err := s.reader.GetManagementAccountDetailSource(ctx, port.ManagementAccountDetailInput{
		AccountID:       strings.TrimSpace(input.AccountID),
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
	})
	if err != nil || !found {
		return nil, found, err
	}
	detail := map[string]any{}
	if err := json.Unmarshal([]byte(source.DetailJSON), &detail); err != nil {
		return nil, false, fmt.Errorf("decode management account detail: %w", err)
	}
	authorized := source.AccessType == "authorized"
	detail["permissions"] = permissions(authorized, source.HasActiveManualSource)
	detail["todayUsage"] = emptyUsage()
	detail["usage"] = emptyUsage()
	detail["effectiveAvailability"] = effectiveAvailability(detail, s.now())

	switch level {
	case LevelEditBasic:
		if authorized {
			return nil, false, ErrCredentialsForbidden
		}
		credentials, err := s.ownerCredentials(source)
		if err != nil {
			return nil, false, err
		}
		detail["credentials"] = credentials
		delete(detail, "modelMappings")
	case LevelAdvanced:
		if authorized {
			delete(detail, "credentials")
			break
		}
		credentials, err := s.ownerCredentials(source)
		if err != nil {
			return nil, false, err
		}
		detail["credentials"] = credentials
	}
	return detail, true, nil
}

func (s *Service) APIKeyRuntime(ctx context.Context, input Input) (APIKeyRuntimeResponse, bool, error) {
	if s.reader == nil {
		return APIKeyRuntimeResponse{}, false, fmt.Errorf("management account detail reader is required")
	}
	source, found, err := s.reader.GetManagementAccountDetailSource(ctx, port.ManagementAccountDetailInput{
		AccountID:       strings.TrimSpace(input.AccountID),
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
	})
	if err != nil || !found {
		return APIKeyRuntimeResponse{}, found, err
	}
	if source.AccessType == "authorized" || source.SourceAccountID != source.ID {
		return APIKeyRuntimeResponse{}, false, ErrRuntimeForbidden
	}
	credentials, err := s.ownerCredentials(source)
	if err != nil {
		return APIKeyRuntimeResponse{}, false, err
	}
	entries := accountAPIKeyEntries(credentials, s.fingerprintSecret)
	if !apiKeyPoolSupported(source, len(entries)) {
		entries = nil
	}
	states, err := s.reader.ListManagementAccountAPIKeyRuntimeStates(ctx, source.SourceAccountID)
	if err != nil {
		return APIKeyRuntimeResponse{}, false, err
	}
	stateByFingerprint := make(map[string]port.ManagementAccountAPIKeyRuntimeState, len(states))
	for _, state := range states {
		stateByFingerprint[state.KeyFingerprint] = state
	}
	items := make([]APIKeyRuntimeDetail, 0, len(entries))
	for _, entry := range entries {
		state, hasState := stateByFingerprint[entry.fingerprint]
		status := "active"
		if hasState && strings.TrimSpace(state.Status) != "" {
			status = state.Status
		}
		items = append(items, APIKeyRuntimeDetail{
			KeyIndex:             entry.index,
			KeyFingerprintPrefix: prefix(entry.fingerprint, 12),
			KeySuffix:            suffix(entry.key, 4),
			Weight:               entry.weight,
			Status:               status,
			FailureCount:         nonNegative(state.FailureCount),
			ConsecutiveFailures:  nonNegative(state.ConsecutiveFailures),
			SuccessCount:         nonNegative64(state.SuccessCount),
			CooldownUntil:        state.CooldownUntil,
			NextProbeAt:          state.NextProbeAt,
			LastAttemptAt:        state.LastAttemptAt,
			LastSuccessAt:        state.LastSuccessAt,
			LastFailureAt:        state.LastFailureAt,
			LastErrorCode:        state.LastErrorCode,
			LastErrorMessage:     state.LastErrorMessage,
			LastTraceID:          state.LastTraceID,
		})
	}
	return APIKeyRuntimeResponse{
		AccountID:      source.ID,
		ConfigRevision: max(1, source.ConfigRevision),
		Items:          items,
	}, true, nil
}

func (s *Service) ownerCredentials(source port.ManagementAccountDetailSource) (map[string]any, error) {
	if s.credentialCodec == nil {
		return nil, fmt.Errorf("management account detail credential codec is required")
	}
	credentials, err := s.credentialCodec.DecryptJSON(source.CredentialsEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt management account credentials: %w", err)
	}
	if credentials == nil {
		credentials = map[string]any{}
	}
	return credentials, nil
}

func permissions(authorized bool, canReturn bool) ResourcePermissions {
	if authorized {
		return ResourcePermissions{
			CanUse:                 true,
			CanReturnAuthorization: canReturn,
		}
	}
	return ResourcePermissions{
		CanUse:             true,
		CanEdit:            true,
		CanDelete:          true,
		CanAuthorize:       true,
		CanViewCredentials: true,
		CanManageAccounts:  true,
		CanBindToAPIKey:    true,
	}
}

func emptyUsage() map[string]any {
	return map[string]any{
		"requestCount":       0,
		"inputTokens":        0,
		"outputTokens":       0,
		"cacheReadTokens":    0,
		"cacheReadCost":      0,
		"cacheWriteTokens":   0,
		"cacheWrite1hTokens": 0,
		"cacheWriteCost":     0,
		"thinkingTokens":     0,
		"inputImageTokens":   0,
		"outputImageTokens":  0,
		"totalTokens":        0,
		"totalCost":          0,
	}
}

func effectiveAvailability(detail map[string]any, now time.Time) map[string]any {
	status, _ := detail["status"].(string)
	schedulable, _ := detail["schedulable"].(bool)
	if status == "active" && schedulable && !timeReached(detail["accountExpiresAt"], now) && !timeReached(detail["cooldownUntil"], now) {
		return map[string]any{"available": true, "status": "available", "label": "可用", "color": "green"}
	}
	label := "不可用"
	availabilityStatus := status
	if availabilityStatus == "" || availabilityStatus == "active" {
		availabilityStatus = "instance_unschedulable"
	}
	if status == "pending_test" {
		label = "待检查"
	}
	return map[string]any{
		"available":    false,
		"status":       availabilityStatus,
		"label":        label,
		"color":        "red",
		"blockerScope": "account",
	}
}

func timeReached(value any, now time.Time) bool {
	text, _ := value.(string)
	if strings.TrimSpace(text) == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	return err == nil && !parsed.After(now)
}

func accountAPIKeyEntries(credentials map[string]any, secret string) []apiKeyEntry {
	rawKeys := []any{credentials["api_key"]}
	if values, ok := credentials["api_keys"].([]any); ok && len(values) > 0 {
		rawKeys = values
	} else if values, ok := credentials["api_keys"].([]string); ok && len(values) > 0 {
		rawKeys = make([]any, len(values))
		for index, value := range values {
			rawKeys[index] = value
		}
	}
	weights := anySlice(credentials["api_key_weights"])
	seen := map[string]struct{}{}
	entries := make([]apiKeyEntry, 0, len(rawKeys))
	for index, raw := range rawKeys {
		key, ok := raw.(string)
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		weight := 1
		if index < len(weights) {
			if value, ok := jsonNumberInt(weights[index]); ok && value >= 1 && value <= 100 {
				weight = value
			}
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(key))
		entries = append(entries, apiKeyEntry{
			index: index, key: key, fingerprint: hex.EncodeToString(mac.Sum(nil)), weight: weight,
		})
	}
	return entries
}

func apiKeyPoolSupported(source port.ManagementAccountDetailSource, keyCount int) bool {
	if source.Type != "api_key" || keyCount < 2 {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(source.ProviderCode))
	protocol := strings.ToLower(strings.TrimSpace(source.ProtocolCode))
	if protocol == "anthropic" && strings.EqualFold(source.ProtocolVersion, "v1") {
		return true
	}
	switch provider {
	case "openai", "gpt", "deepseek", "glm", "gemini", "anthropic":
		return true
	default:
		return false
	}
}

func anySlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []int:
		output := make([]any, len(typed))
		for index, item := range typed {
			output[index] = item
		}
		return output
	default:
		return nil
	}
}

func jsonNumberInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case float64:
		parsed := int(typed)
		return parsed, typed == float64(parsed)
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func prefix(value string, size int) string {
	if len(value) <= size {
		return value
	}
	return value[:size]
}

func suffix(value string, size int) string {
	if len(value) <= size {
		return value
	}
	return value[len(value)-size:]
}

func nonNegative(value int) int {
	return max(0, value)
}

func nonNegative64(value int64) int64 {
	return max(0, value)
}
