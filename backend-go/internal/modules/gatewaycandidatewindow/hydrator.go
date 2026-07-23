package gatewaycandidatewindow

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const qualityFreshnessWindow = 24 * time.Hour
const maxAPIKeysPerCandidate = 256

const (
	DropCredentialDecrypt  = "credential_decrypt_failed"
	DropCredentialEmpty    = "credential_empty"
	DropAPIKeyMissing      = "api_key_missing"
	DropAPIKeyPoolTooLarge = "api_key_pool_too_large"
	DropOAuthTokenMissing  = "oauth_token_missing"
	DropAccountFacts       = "account_facts_missing"
	DropProxyMissing       = "proxy_missing"
	DropProxyDisabled      = "proxy_disabled"
	DropProxyCredential    = "proxy_credential_invalid"
)

type CredentialSet struct {
	values map[string]any
}

func NewCredentialSet(values map[string]any) CredentialSet {
	copy := make(map[string]any, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return CredentialSet{values: copy}
}

func (CredentialSet) String() string   { return "[REDACTED]" }
func (CredentialSet) GoString() string { return "[REDACTED]" }

func (c CredentialSet) Value(key string) (any, bool) {
	value, ok := c.values[key]
	return value, ok
}

func (c CredentialSet) StringValue(key string) (string, bool) {
	value, ok := c.values[key].(string)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func (c CredentialSet) StringValues(key string) []string {
	return normalizedCredentialStrings(c.values[key])
}

type CredentialCodec interface {
	DecryptJSON(string) (map[string]any, error)
}

type APIKeyRuntimeReader interface {
	ListManagementAccountAPIKeyRuntimeStatesByFingerprints(context.Context, map[string][]string) (map[string][]port.ManagementAccountAPIKeyRuntimeState, error)
}

type BatchHydratorOptions struct {
	Reader            port.GatewayCandidateHydrationReader
	QualityReader     port.GatewayCandidateQualityReader
	APIKeyRuntime     APIKeyRuntimeReader
	CredentialCodec   CredentialCodec
	FingerprintSecret string
	Now               func() time.Time
}

type BatchHydrator struct {
	reader            port.GatewayCandidateHydrationReader
	qualityReader     port.GatewayCandidateQualityReader
	apiKeyRuntime     APIKeyRuntimeReader
	credentialCodec   CredentialCodec
	fingerprintSecret string
	now               func() time.Time
}

func NewBatchHydrator(options BatchHydratorOptions) *BatchHydrator {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	qualityReader := options.QualityReader
	if qualityReader == nil {
		qualityReader, _ = options.Reader.(port.GatewayCandidateQualityReader)
	}
	return &BatchHydrator{
		reader:            options.Reader,
		qualityReader:     qualityReader,
		apiKeyRuntime:     options.APIKeyRuntime,
		credentialCodec:   options.CredentialCodec,
		fingerprintSecret: options.FingerprintSecret,
		now:               now,
	}
}

func (h *BatchHydrator) PreRank(ctx context.Context, candidates []port.GatewayAccountCandidate) (map[string]CandidateRankFacts, error) {
	result := make(map[string]CandidateRankFacts, len(candidates))
	if h.qualityReader == nil || len(candidates) == 0 {
		return result, nil
	}
	if len(candidates) > port.GatewayAccountCandidateScanLimit {
		return nil, fmt.Errorf("gateway candidate pre-rank exceeds scan limit: %d", len(candidates))
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.AccountID)
	}
	facts, err := h.qualityReader.LoadGatewayCandidateQualityFacts(ctx, ids, h.now().UTC().Add(-qualityFreshnessWindow))
	if err != nil {
		return nil, err
	}
	for accountID, quality := range facts {
		result[accountID] = CandidateRankFacts{
			QualityScore: quality.QualityScore, QualityState: quality.QualityState,
			QualityEWMAFirstTokenMS: quality.QualityEWMAFirstTokenMS,
		}
	}
	return result, nil
}

type preparedCandidate struct {
	row         port.GatewayAccountCandidate
	accountID   string
	accountType string
	proxyID     string
	credentials CredentialSet
	apiKeys     []candidateAPIKey
	dropReason  string
}

type candidateAPIKey struct {
	fingerprint string
	index       int
}

type credentialAPIKey struct {
	key   string
	index int
}

func (h *BatchHydrator) Hydrate(ctx context.Context, input HydrateInput) ([]HydrationResult, error) {
	if h.reader == nil {
		return nil, fmt.Errorf("gateway candidate hydration reader is required")
	}
	if h.credentialCodec == nil {
		return nil, fmt.Errorf("gateway candidate credential codec is required")
	}
	if len(input.Candidates) > FinalLimit {
		return nil, fmt.Errorf("gateway candidate hydration batch exceeds limit: %d", len(input.Candidates))
	}
	if len(input.Candidates) == 0 {
		return []HydrationResult{}, nil
	}
	prepared := make([]preparedCandidate, len(input.Candidates))
	accountIDs := make([]string, 0, len(input.Candidates))
	proxyIDs := make([]string, 0, len(input.Candidates))
	fingerprintsByAccount := make(map[string][]string)
	for index, row := range input.Candidates {
		candidate := h.prepare(row)
		prepared[index] = candidate
		if candidate.dropReason != "" {
			continue
		}
		accountIDs = append(accountIDs, candidate.accountID)
		if candidate.proxyID != "" {
			proxyIDs = append(proxyIDs, candidate.proxyID)
		}
		if len(candidate.apiKeys) > 0 {
			for _, key := range candidate.apiKeys {
				fingerprintsByAccount[candidate.accountID] = append(fingerprintsByAccount[candidate.accountID], key.fingerprint)
			}
		}
	}
	if len(fingerprintsByAccount) > 0 {
		if strings.TrimSpace(h.fingerprintSecret) == "" {
			return nil, fmt.Errorf("gateway candidate fingerprint secret is required for api key accounts")
		}
		if h.apiKeyRuntime == nil {
			return nil, fmt.Errorf("gateway candidate api key runtime reader is required for api key accounts")
		}
	}

	facts, err := h.reader.LoadGatewayCandidateHydrationFacts(ctx, port.GatewayCandidateHydrationInput{
		AccountIDs: accountIDs,
		ProxyIDs:   proxyIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("load gateway candidate hydration facts: %w", err)
	}
	runtimeStates := map[string][]port.ManagementAccountAPIKeyRuntimeState{}
	if len(fingerprintsByAccount) > 0 {
		runtimeStates, err = h.apiKeyRuntime.ListManagementAccountAPIKeyRuntimeStatesByFingerprints(ctx, fingerprintsByAccount)
		if err != nil {
			return nil, fmt.Errorf("load gateway candidate api key runtime: %w", err)
		}
	}

	results := make([]HydrationResult, 0, len(prepared))
	for _, candidate := range prepared {
		result := HydrationResult{AccountID: candidate.row.AccountID, DropReason: candidate.dropReason}
		if result.DropReason != "" {
			results = append(results, result)
			continue
		}
		accountFacts, ok := facts.Accounts[candidate.accountID]
		if !ok {
			result.DropReason = DropAccountFacts
			results = append(results, result)
			continue
		}
		qualityFacts := input.PreRanks[candidate.row.AccountID]
		hydrated := Candidate{
			Credentials:             candidate.credentials,
			SupportedModels:         append([]string(nil), accountFacts.SupportedModels...),
			ModelMappings:           mapModelMappings(accountFacts.ModelMappings),
			APIKeyRuntime:           mapAPIKeyRuntime(candidate.apiKeys, runtimeStates[candidate.accountID]),
			QualityScore:            qualityFacts.QualityScore,
			QualityState:            qualityFacts.QualityState,
			QualityEWMAFirstTokenMS: qualityFacts.QualityEWMAFirstTokenMS,
		}
		if candidate.proxyID != "" {
			proxyFacts, exists := facts.Proxies[candidate.proxyID]
			switch {
			case !exists:
				hydrated.Proxy = &ProxyRuntime{ID: candidate.proxyID, Available: false, UnavailableReason: DropProxyMissing}
			case !proxyFacts.Enabled:
				hydrated.Proxy = &ProxyRuntime{ID: candidate.proxyID, Type: proxyFacts.Type, Enabled: false, Available: false, UnavailableReason: DropProxyDisabled}
			default:
				proxy, proxyErr := h.hydrateProxy(proxyFacts)
				if proxyErr != nil {
					hydrated.Proxy = &ProxyRuntime{ID: candidate.proxyID, Type: proxyFacts.Type, Enabled: proxyFacts.Enabled, Available: false, UnavailableReason: DropProxyCredential}
				} else {
					hydrated.Proxy = &proxy
				}
			}
		}
		if result.DropReason == "" {
			result.Candidate = hydrated
		}
		results = append(results, result)
	}
	return results, nil
}

func (h *BatchHydrator) prepare(row port.GatewayAccountCandidate) preparedCandidate {
	accountID := row.AccountID
	accountType := row.Type
	encrypted := row.CredentialsEncrypted
	proxyID := row.ProxyProfileID
	if strings.TrimSpace(row.ResourceAccountID) != "" {
		accountID = row.ResourceAccountID
		accountType = row.ResourceType
		encrypted = row.ResourceCredentialsEncrypted
		proxyID = row.ResourceProxyProfileID
	}
	prepared := preparedCandidate{row: row, accountID: strings.TrimSpace(accountID), accountType: strings.TrimSpace(accountType), proxyID: strings.TrimSpace(proxyID)}
	credentials, err := h.credentialCodec.DecryptJSON(encrypted)
	if err != nil {
		prepared.dropReason = DropCredentialDecrypt
		return prepared
	}
	if len(credentials) == 0 {
		prepared.dropReason = DropCredentialEmpty
		return prepared
	}
	prepared.credentials = NewCredentialSet(credentials)
	if strings.EqualFold(prepared.accountType, "api_key") {
		keys := credentialAPIKeys(credentials)
		if len(keys) == 0 {
			prepared.dropReason = DropAPIKeyMissing
			return prepared
		}
		if len(keys) > maxAPIKeysPerCandidate {
			prepared.dropReason = DropAPIKeyPoolTooLarge
			return prepared
		}
		prepared.apiKeys = fingerprintKeys(keys, h.fingerprintSecret)
	} else if strings.EqualFold(prepared.accountType, "oauth") || strings.EqualFold(prepared.accountType, "google_oauth") {
		_, hasAccessToken := prepared.credentials.StringValue("access_token")
		_, hasRefreshToken := prepared.credentials.StringValue("refresh_token")
		if !hasAccessToken && !hasRefreshToken {
			prepared.dropReason = DropOAuthTokenMissing
			return prepared
		}
	}
	return prepared
}

func (h *BatchHydrator) hydrateProxy(facts port.GatewayCandidateProxyFacts) (ProxyRuntime, error) {
	if strings.TrimSpace(facts.ID) == "" || strings.TrimSpace(facts.Type) == "" || strings.TrimSpace(facts.Host) == "" || facts.Port < 1 || facts.Port > 65535 {
		return ProxyRuntime{}, fmt.Errorf("proxy facts are invalid")
	}
	proxyType := strings.ToLower(strings.TrimSpace(facts.Type))
	if proxyType == "socks5" {
		proxyType = "socks5h"
	}
	proxy := ProxyRuntime{ID: facts.ID, Type: proxyType, Host: facts.Host, Port: facts.Port, Username: facts.Username, Enabled: facts.Enabled, Available: true}
	if strings.TrimSpace(facts.PasswordEncrypted) == "" {
		return proxy, nil
	}
	credentials, err := h.credentialCodec.DecryptJSON(facts.PasswordEncrypted)
	if err != nil {
		return ProxyRuntime{}, err
	}
	proxy.Credentials = NewCredentialSet(credentials)
	return proxy, nil
}

func credentialAPIKeys(credentials map[string]any) []credentialAPIKey {
	if values, exists := credentials["api_keys"]; exists {
		switch pool := values.(type) {
		case []any:
			if len(pool) > 0 {
				return normalizedCredentialKeyEntries(pool)
			}
		case []string:
			if len(pool) > 0 {
				return normalizedCredentialKeyEntries(pool)
			}
		}
	}
	return normalizedCredentialKeyEntries(credentials["api_key"])
}

func normalizedCredentialKeyEntries(value any) []credentialAPIKey {
	var raw []any
	switch values := value.(type) {
	case []any:
		raw = values
	case []string:
		raw = make([]any, len(values))
		for index := range values {
			raw[index] = values[index]
		}
	default:
		raw = []any{value}
	}
	result := make([]credentialAPIKey, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for index, item := range raw {
		key, ok := item.(string)
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, credentialAPIKey{key: key, index: index})
	}
	return result
}

func normalizedCredentialStrings(value any) []string {
	var raw []any
	switch values := value.(type) {
	case []any:
		raw = values
	case []string:
		raw = make([]any, len(values))
		for index := range values {
			raw[index] = values[index]
		}
	default:
		raw = []any{value}
	}
	result := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		text = strings.TrimSpace(text)
		if !ok || text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		result = append(result, text)
	}
	return result
}

func fingerprintKeys(keys []credentialAPIKey, secret string) []candidateAPIKey {
	result := make([]candidateAPIKey, 0, len(keys))
	for _, key := range keys {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(key.key))
		result = append(result, candidateAPIKey{fingerprint: hex.EncodeToString(mac.Sum(nil)), index: key.index})
	}
	return result
}

func mapModelMappings(values []port.GatewayCandidateModelMapping) []ModelMapping {
	result := make([]ModelMapping, 0, len(values))
	for _, value := range values {
		result = append(result, ModelMapping{
			ProviderCode: value.ProviderCode, SourceModel: value.SourceModel,
			SourceEndpointFamily: value.SourceEndpointFamily, UpstreamModel: value.UpstreamModel,
			UpstreamEndpointFamily: value.UpstreamEndpointFamily,
			Enabled:                value.Enabled,
		})
	}
	return result
}

func mapAPIKeyRuntime(keys []candidateAPIKey, states []port.ManagementAccountAPIKeyRuntimeState) []APIKeyRuntime {
	stateByFingerprint := make(map[string]port.ManagementAccountAPIKeyRuntimeState, len(states))
	for _, state := range states {
		stateByFingerprint[state.KeyFingerprint] = state
	}
	result := make([]APIKeyRuntime, 0, len(keys))
	for _, key := range keys {
		state, exists := stateByFingerprint[key.fingerprint]
		if !exists {
			result = append(result, APIKeyRuntime{KeyFingerprint: key.fingerprint, KeyIndex: key.index, Status: "active"})
			continue
		}
		result = append(result, APIKeyRuntime{
			KeyFingerprint: key.fingerprint, KeyIndex: state.KeyIndex, Status: state.Status,
			CooldownUntil: state.CooldownUntil, NextProbeAt: state.NextProbeAt,
		})
	}
	return result
}

var _ Hydrator = (*BatchHydrator)(nil)
var _ CandidatePreRanker = (*BatchHydrator)(nil)
