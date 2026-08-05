// Package gatewayfallbackquota adapts the published authorization quota
// snapshot to the Node-ordered cross-group fallback policy.
package gatewayfallbackquota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"juhe-ai/backend-go/internal/modules/gatewayfallbackpolicy"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	groupAuthorizationScope   = "group_authorization"
	accountAuthorizationScope = "account_authorization"
)

// Service makes the same snapshot decision that the Node server makes before
// its exact DB fallback. The Go fallback owner has no exact DB fallback yet,
// so an unreadable or absent snapshot is an explicit error rather than an
// implicit allow.
type Service struct {
	reader port.GatewayPreflightQuotaSnapshotReader
}

func NewService(reader port.GatewayPreflightQuotaSnapshotReader) (*Service, error) {
	if reader == nil {
		return nil, fmt.Errorf("gateway fallback authorization quota snapshot reader is required")
	}
	return &Service{reader: reader}, nil
}

func (s *Service) CheckFallbackAuthorizationQuota(ctx context.Context, input gatewayfallbackpolicy.AuthorizationQuotaInput) (gatewayfallbackpolicy.AuthorizationQuotaResult, error) {
	if s == nil || s.reader == nil {
		return gatewayfallbackpolicy.AuthorizationQuotaResult{}, fmt.Errorf("gateway fallback authorization quota checker is not configured")
	}
	if ctx == nil {
		return gatewayfallbackpolicy.AuthorizationQuotaResult{}, fmt.Errorf("gateway fallback authorization quota context is required")
	}
	groupID := strings.TrimSpace(input.Window.Access.GroupAuthorizationID)
	groupLimited, err := quotaLimited(input.Window.Access.GroupAuthorizationLimitsJSON)
	if err != nil {
		return gatewayfallbackpolicy.AuthorizationQuotaResult{}, fmt.Errorf("parse target group authorization quota limits: %w", err)
	}
	if groupLimited && groupID == "" {
		return gatewayfallbackpolicy.AuthorizationQuotaResult{}, fmt.Errorf("target group has enabled authorization quota without an authorization id")
	}
	type accountFact struct {
		id, authorizationID string
		limited             bool
	}
	facts := make([]accountFact, 0, len(input.Candidates))
	requiresDecision := groupID != "" && groupLimited
	for _, candidate := range input.Candidates {
		accountID := strings.TrimSpace(candidate.Projection.AccountID)
		if accountID == "" {
			return gatewayfallbackpolicy.AuthorizationQuotaResult{}, fmt.Errorf("fallback authorization quota candidate has no account id")
		}
		accountAuthorizationID := strings.TrimSpace(candidate.Projection.AccountAuthorizationID)
		accountLimited, limitErr := quotaLimited(candidate.Projection.AuthorizationLimitsJSON)
		if limitErr != nil {
			return gatewayfallbackpolicy.AuthorizationQuotaResult{}, fmt.Errorf("parse fallback account authorization quota limits for %q: %w", accountID, limitErr)
		}
		if accountLimited && accountAuthorizationID == "" {
			return gatewayfallbackpolicy.AuthorizationQuotaResult{}, fmt.Errorf("fallback account %q has enabled authorization quota without an authorization id", accountID)
		}
		if accountAuthorizationID != "" && accountLimited {
			requiresDecision = true
		}
		facts = append(facts, accountFact{id: accountID, authorizationID: accountAuthorizationID, limited: accountLimited})
	}
	allowed := make(map[string]bool, len(facts))
	if !requiresDecision {
		for _, fact := range facts {
			allowed[fact.id] = true
		}
		return gatewayfallbackpolicy.AuthorizationQuotaResult{Complete: true, AllowedByAccountID: allowed}, nil
	}
	snapshot, found, err := s.reader.LoadGatewayPreflightQuotaSnapshotCurrent(ctx)
	if err != nil {
		return gatewayfallbackpolicy.AuthorizationQuotaResult{}, fmt.Errorf("load gateway authorization quota snapshot: %w", err)
	}
	if !found || strings.TrimSpace(snapshot.GeneratedAt) == "" {
		return gatewayfallbackpolicy.AuthorizationQuotaResult{}, fmt.Errorf("gateway authorization quota snapshot is missing")
	}
	decisions, err := authorizationDecisionIndex(snapshot.AuthorizationEntries)
	if err != nil {
		return gatewayfallbackpolicy.AuthorizationQuotaResult{}, err
	}
	for _, fact := range facts {
		allowed[fact.id] = allowsSnapshot(decisions, snapshot.AuthorizationEntriesComplete, groupAuthorizationScope, groupID, groupLimited) &&
			allowsSnapshot(decisions, snapshot.AuthorizationEntriesComplete, accountAuthorizationScope, fact.authorizationID, fact.limited)
	}
	return gatewayfallbackpolicy.AuthorizationQuotaResult{Complete: true, AllowedByAccountID: allowed}, nil
}

func authorizationDecisionIndex(entries []port.GatewayAuthorizationQuotaSnapshotEntry) (map[string]bool, error) {
	result := make(map[string]bool, len(entries))
	for _, entry := range entries {
		scopeType := strings.TrimSpace(entry.ScopeType)
		authorizationID := strings.TrimSpace(entry.AuthorizationID)
		if scopeType != groupAuthorizationScope && scopeType != accountAuthorizationScope {
			return nil, fmt.Errorf("gateway authorization quota snapshot has an invalid scope type: %q", scopeType)
		}
		if authorizationID == "" {
			return nil, fmt.Errorf("gateway authorization quota snapshot has an empty authorization id")
		}
		key := authorizationDecisionKey(scopeType, authorizationID)
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("gateway authorization quota snapshot has a duplicate authorization decision: %q", key)
		}
		result[key] = entry.Allowed
	}
	return result, nil
}

func allowsSnapshot(decisions map[string]bool, complete bool, scopeType, authorizationID string, limited bool) bool {
	authorizationID = strings.TrimSpace(authorizationID)
	if authorizationID == "" {
		return true
	}
	if allowed, found := decisions[authorizationDecisionKey(scopeType, authorizationID)]; found {
		return allowed
	}
	// Node only fails closed for a missing decision when the authorization
	// actually has an enabled quota and the bounded snapshot is incomplete.
	return !limited || complete
}

func authorizationDecisionKey(scopeType, authorizationID string) string {
	return scopeType + "\x00" + authorizationID
}

func quotaLimited(raw string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return false, nil
	}
	var values map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&values); err != nil {
		return false, fmt.Errorf("limits json is invalid: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("limits json has trailing data: %w", err)
	}
	if values == nil {
		return false, nil
	}
	for key, value := range values {
		switch key {
		case "hourly":
			if err := validateQuotaLimit(value, true); err != nil {
				return false, fmt.Errorf("hourly quota is invalid: %w", err)
			}
		case "daily", "weekly", "monthly", "total":
			if err := validateQuotaLimit(value, false); err != nil {
				return false, fmt.Errorf("%s quota is invalid: %w", key, err)
			}
		default:
			return false, fmt.Errorf("limits json has an unsupported field: %q", key)
		}
	}
	return len(values) > 0, nil
}

func validateQuotaLimit(raw json.RawMessage, hourly bool) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("quota value must be an object")
	}
	var value map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("quota value must contain one object: %w", err)
	}
	if value == nil {
		return fmt.Errorf("quota value must be an object")
	}
	allowed := map[string]struct{}{"enabled": {}, "limit": {}}
	if hourly {
		allowed["hours"] = struct{}{}
	}
	for key := range value {
		if _, supported := allowed[key]; !supported {
			return fmt.Errorf("quota value has an unsupported field: %q", key)
		}
	}
	enabled, found := value["enabled"]
	if !found || !bytes.Equal(bytes.TrimSpace(enabled), []byte("true")) {
		return fmt.Errorf("quota enabled must be true")
	}
	limit, found := value["limit"]
	if !found {
		return fmt.Errorf("quota limit is required")
	}
	if err := validateQuotaAmount(limit); err != nil {
		return err
	}
	if !hourly {
		return nil
	}
	hours, found := value["hours"]
	if !found {
		return fmt.Errorf("hourly quota hours are required")
	}
	return validateHourlyWindow(hours)
}

func validateQuotaAmount(raw json.RawMessage) error {
	value, text, err := quotaJSONNumber(raw)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 9007199254740991 {
		return fmt.Errorf("quota limit must be a positive finite amount")
	}
	scaled := value * 1000000
	if math.Round(scaled) != scaled {
		return fmt.Errorf("quota limit has more than six decimal places")
	}
	if text == "" {
		return fmt.Errorf("quota limit is required")
	}
	return nil
}

func validateHourlyWindow(raw json.RawMessage) error {
	value, _, err := quotaJSONNumber(raw)
	if err != nil || math.Trunc(value) != value || value < 1 || value > 720 {
		return fmt.Errorf("hourly quota hours must be an integer in 1..720")
	}
	return nil
}

func quotaJSONNumber(raw json.RawMessage) (float64, string, error) {
	if len(raw) == 0 {
		return 0, "", fmt.Errorf("quota number is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value json.Number
	if err := decoder.Decode(&value); err != nil {
		return 0, "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return 0, "", fmt.Errorf("quota number must contain one value: %w", err)
	}
	parsed, err := strconv.ParseFloat(value.String(), 64)
	if err != nil {
		return 0, "", err
	}
	return parsed, value.String(), nil
}

var _ gatewayfallbackpolicy.AuthorizationQuotaChecker = (*Service)(nil)
