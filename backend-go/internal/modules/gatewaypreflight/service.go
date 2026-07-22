package gatewaypreflight

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/apikeysecret"
	"juhe-ai/backend-go/internal/store/port"
)

const MaxActiveBindings = 20

type Service struct {
	store               port.GatewayPreflightReader
	quotaSnapshotReader port.GatewayPreflightQuotaSnapshotReader
	cache               *Cache
	now                 func() time.Time
}

type ServiceOptions struct {
	Store               port.GatewayPreflightReader
	QuotaSnapshotReader port.GatewayPreflightQuotaSnapshotReader
	Cache               *Cache
	Now                 func() time.Time
}

type gatewayPreflightStructure struct {
	decision Decision
	apiKey   *APIKey
	settings *Settings
	bindings []Binding
}

func NewService(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: opts.Store, quotaSnapshotReader: opts.QuotaSnapshotReader, cache: opts.Cache, now: now}
}

func (s *Service) Resolve(ctx context.Context, rawAPIKey string) (Result, error) {
	if !strings.HasPrefix(rawAPIKey, "sk-") {
		return Result{decision: newDecision(DecisionInvalidAPIKeyFormat)}, nil
	}
	if s == nil || s.store == nil {
		return Result{}, fmt.Errorf("gateway preflight store is required")
	}
	keyHash := apikeysecret.Hash(rawAPIKey)
	loader := func(loadCtx context.Context, _ string) (gatewayPreflightStructure, error) {
		return s.loadStructure(loadCtx, keyHash)
	}
	structure, err := s.loadStructureDirect(ctx, keyHash, loader)
	if err != nil {
		return Result{}, err
	}
	if structure.decision.Code() != DecisionReady {
		return structure.result(), nil
	}
	if structure.apiKey == nil {
		return Result{}, fmt.Errorf("gateway preflight ready result has no API key")
	}
	now := s.now()
	if structure.apiKey.expiresAt != nil && !now.Before(*structure.apiKey.expiresAt) {
		structure.decision = newDecision(DecisionAPIKeyExpired)
		return structure.result(), nil
	}
	structure.bindings = activeBindingsAt(structure.bindings, now)
	if len(structure.bindings) == 0 {
		structure.decision = newDecision(DecisionNoActiveBindings)
		return structure.result(), nil
	}
	if !hasEnabledQuota(structure.apiKey.quotaLimits) {
		return structure.result(), nil
	}
	decision := s.quotaDecision(ctx, *structure.apiKey)
	structure.decision = decision
	return structure.result(), nil
}

func (s *Service) loadStructureDirect(ctx context.Context, keyHash string, loader func(context.Context, string) (gatewayPreflightStructure, error)) (gatewayPreflightStructure, error) {
	if s.cache == nil {
		return loader(ctx, keyHash)
	}
	return s.cache.load(ctx, keyHash, loader)
}

func (s *Service) loadStructure(ctx context.Context, keyHash string) (gatewayPreflightStructure, error) {
	now := s.now()
	row, found, err := s.store.LoadGatewayPreflightAPIKey(ctx, keyHash)
	if err != nil {
		return gatewayPreflightStructure{}, fmt.Errorf("load gateway preflight api key: %w", err)
	}
	if !found {
		return gatewayPreflightStructure{decision: newDecision(DecisionAPIKeyNotFound)}, nil
	}
	if row.APIKeyStatus != "active" {
		return gatewayPreflightStructure{decision: newDecision(DecisionAPIKeyDisabled)}, nil
	}
	if row.ExpiresAt != nil && !now.Before(*row.ExpiresAt) {
		return gatewayPreflightStructure{decision: newDecision(DecisionAPIKeyExpired)}, nil
	}
	if row.SystemAccountStatus != "active" {
		return gatewayPreflightStructure{decision: newDecision(DecisionSystemAccountDisabled)}, nil
	}
	if row.RouteStrategyStatus != "active" {
		return gatewayPreflightStructure{decision: newDecision(DecisionRouteStrategyDisabled)}, nil
	}

	bindings, err := s.store.ListGatewayPreflightBindings(ctx, row.ID, row.RouteStrategyID, row.SystemAccountID, now, MaxActiveBindings)
	if err != nil {
		return gatewayPreflightStructure{}, fmt.Errorf("list gateway preflight bindings: %w", err)
	}
	if len(bindings) == 0 {
		return gatewayPreflightStructure{decision: newDecision(DecisionNoActiveBindings)}, nil
	}
	if len(bindings) > MaxActiveBindings {
		bindings = bindings[:MaxActiveBindings]
	}
	sort.SliceStable(bindings, func(i, j int) bool {
		if bindings[i].Priority != bindings[j].Priority {
			return bindings[i].Priority < bindings[j].Priority
		}
		if !bindings[i].CreatedAt.Equal(bindings[j].CreatedAt) {
			return bindings[i].CreatedAt.Before(bindings[j].CreatedAt)
		}
		return bindings[i].ID < bindings[j].ID
	})
	settingsRow, err := s.store.LoadGatewayPreflightSettings(ctx)
	if err != nil {
		return gatewayPreflightStructure{}, fmt.Errorf("load gateway preflight settings: %w", err)
	}
	apiKey := apiKeyFromRecord(row)
	resultBindings := make([]Binding, 0, len(bindings))
	for _, binding := range bindings {
		resultBindings = append(resultBindings, newBinding(binding))
	}
	return gatewayPreflightStructure{decision: newDecision(DecisionReady), apiKey: &apiKey, settings: ptr(settingsFromRecord(settingsRow)), bindings: resultBindings}, nil
}

func (s *Service) quotaDecision(ctx context.Context, apiKey APIKey) Decision {
	if s.quotaSnapshotReader == nil {
		return newDecision(DecisionQuotaSnapshotMissing)
	}
	snapshot, found, err := s.quotaSnapshotReader.LoadGatewayPreflightQuotaSnapshotCurrent(ctx)
	if err != nil {
		return newDecision(DecisionQuotaSnapshotUnavailable)
	}
	if !found || strings.TrimSpace(snapshot.GeneratedAt) == "" {
		return newDecision(DecisionQuotaSnapshotMissing)
	}
	wantHours := quotaHourlyWindowHours(apiKey.quotaLimits)
	for _, entry := range snapshot.CostEntries {
		if entry.SystemAccountID != apiKey.systemAccountID || entry.ScopeType != "api_key" || entry.ScopeID != apiKey.id || normalizeHours(entry.HourlyWindowHours) != wantHours {
			continue
		}
		if quotaExceeded(apiKey.quotaLimits, entry.Costs) {
			return newDecision(DecisionQuotaExceeded)
		}
		return newDecision(DecisionReady)
	}
	if !snapshot.CostEntriesComplete {
		return newDecision(DecisionQuotaSnapshotIncomplete)
	}
	return newDecision(DecisionQuotaSnapshotMissing)
}

func (s gatewayPreflightStructure) result() Result {
	result := Result{decision: s.decision, bindings: append([]Binding(nil), s.bindings...)}
	if s.apiKey != nil {
		copy := *s.apiKey
		copy.expiresAt = cloneTimePtr(s.apiKey.expiresAt)
		copy.quotaLimits = cloneQuotaLimits(s.apiKey.quotaLimits)
		result.apiKey = &copy
	}
	if s.settings != nil {
		copy := *s.settings
		result.settings = &copy
	}
	return result
}

func ptr[T any](value T) *T { return &value }

func hasEnabledQuota(value port.ManagementRequestQuotaLimits) bool {
	return (value.Hourly != nil && value.Hourly.Enabled) || (value.Daily != nil && value.Daily.Enabled) || (value.Weekly != nil && value.Weekly.Enabled) || (value.Monthly != nil && value.Monthly.Enabled) || (value.Total != nil && value.Total.Enabled)
}

func quotaHourlyWindowHours(value port.ManagementRequestQuotaLimits) int {
	if value.Hourly == nil || !value.Hourly.Enabled {
		return 0
	}
	return normalizeHours(value.Hourly.Hours)
}

func normalizeHours(value int) int {
	if value <= 0 {
		return 0
	}
	return value
}

func activeBindingsAt(bindings []Binding, now time.Time) []Binding {
	active := make([]Binding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.accessExpiresAt != nil && !now.Before(*binding.accessExpiresAt) {
			continue
		}
		active = append(active, binding)
	}
	return active
}

func quotaExceeded(limits port.ManagementRequestQuotaLimits, costs port.GatewayQuotaCosts) bool {
	return (limits.Hourly != nil && limits.Hourly.Enabled && costs.Hourly >= limits.Hourly.Limit) || (limits.Daily != nil && limits.Daily.Enabled && costs.Daily >= limits.Daily.Limit) || (limits.Weekly != nil && limits.Weekly.Enabled && costs.Weekly >= limits.Weekly.Limit) || (limits.Monthly != nil && limits.Monthly.Enabled && costs.Monthly >= limits.Monthly.Limit) || (limits.Total != nil && limits.Total.Enabled && costs.Total >= limits.Total.Limit)
}
