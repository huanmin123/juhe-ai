// Public response envelopes and sanitize helpers, ported from
// external-public-account-push.sanitize.ts and
// external-public-route-strategy.sanitize.ts. The public payloads are strict
// field subsets of the management DTOs; generatedAt is RFC3339 UTC "now".
package aipublic

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// PublicTarget mirrors the target summary shared by every public response.
type PublicTarget struct {
	Username        string `json:"username"`
	DisplayName     string `json:"displayName"`
	SystemAccountID string `json:"systemAccountId"`
	Created         bool   `json:"created"`
}

// PublicGroupTarget adds the group projection account add/delete carry.
type PublicGroupTarget struct {
	PublicTarget
	GroupID      string `json:"groupId"`
	GroupName    string `json:"groupName"`
	GroupCreated bool   `json:"groupCreated"`
}

// PublicGroupSummary mirrors PublicGroupSummary.
type PublicGroupSummary struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	ProviderCode string  `json:"providerCode"`
	Description  *string `json:"description,omitempty"`
	Enabled      bool    `json:"enabled"`
	GroupType    string  `json:"groupType"`
	IsDefault    bool    `json:"isDefault"`
}

// PublicBindingSummary mirrors PublicRouteStrategyGroupBindingSummary.
type PublicBindingSummary struct {
	ID           string  `json:"id"`
	GroupID      string  `json:"groupId"`
	GroupName    *string `json:"groupName,omitempty"`
	ProviderCode *string `json:"providerCode,omitempty"`
	Priority     int     `json:"priority"`
	Weight       int     `json:"weight"`
	Status       string  `json:"status"`
	GroupEnabled bool    `json:"groupEnabled"`
}

// PublicStrategySummary mirrors PublicRouteStrategySummary.
type PublicStrategySummary struct {
	ID                  string                 `json:"id"`
	Name                string                 `json:"name"`
	Description         *string                `json:"description,omitempty"`
	Mode                string                 `json:"mode"`
	Status              string                 `json:"status"`
	IsDefault           bool                   `json:"isDefault"`
	NormalRoutingConfig any                    `json:"normalRoutingConfig,omitempty"`
	HybridRoutingConfig any                    `json:"hybridRoutingConfig,omitempty"`
	GroupBindings       []PublicBindingSummary `json:"groupBindings"`
	APIKeyCount         int                    `json:"apiKeyCount,omitempty"`
	CreatedAt           string                 `json:"createdAt"`
	UpdatedAt           string                 `json:"updatedAt"`
}

// PublicApiKeySummary mirrors PublicApiKeySummary.
type PublicApiKeySummary struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	KeyPrefix            string  `json:"keyPrefix"`
	Key                  *string `json:"key,omitempty"`
	Status               string  `json:"status"`
	RouteStrategyID      string  `json:"routeStrategyId"`
	RouteStrategyName    *string `json:"routeStrategyName,omitempty"`
	RouteStrategyMode    *string `json:"routeStrategyMode,omitempty"`
	RouteStrategyStatus  *string `json:"routeStrategyStatus,omitempty"`
	ExpiresAt            *string `json:"expiresAt,omitempty"`
	AvailabilitySchedule any     `json:"availabilitySchedule,omitempty"`
}

// PublicAccountSummary mirrors PublicAccountPushResponse.account.
type PublicAccountSummary struct {
	ID                        string   `json:"id"`
	Name                      string   `json:"name"`
	ProviderCode              string   `json:"providerCode"`
	ProviderProtocolProfileID *string  `json:"providerProtocolProfileId,omitempty"`
	ProtocolCode              *string  `json:"protocolCode,omitempty"`
	ProtocolVersion           *string  `json:"protocolVersion,omitempty"`
	Type                      string   `json:"type"`
	ClientCompatibility       string   `json:"clientCompatibility"`
	Status                    string   `json:"status"`
	SupportedModels           []string `json:"supportedModels,omitempty"`
	BoundGroupID              *string  `json:"boundGroupId,omitempty"`
	BoundGroupName            *string  `json:"boundGroupName,omitempty"`
	Schedulable               bool     `json:"schedulable"`
	AvailabilitySchedule      any      `json:"availabilitySchedule,omitempty"`
}

// PublicAccountListItem adds the list-only concurrency/priority fields.
type PublicAccountListItem struct {
	PublicAccountSummary
	ConcurrencyLimit int `json:"concurrencyLimit"`
	Priority         int `json:"priority"`
}

func (d *Deps) generatedAt() string {
	return d.clock().UTC().Format(time.RFC3339Nano)
}

// writeStatsEnvelope writes ok({source:'stats', generatedAt, ...rest}).
func (d *Deps) writeStatsEnvelope(w http.ResponseWriter, rest map[string]any) {
	payload := map[string]any{"source": "stats", "generatedAt": d.generatedAt()}
	for key, value := range rest {
		payload[key] = value
	}
	kernel.WriteOK(w, payload, "")
}

// writeStatsCreated writes 201 ok({source:'stats', ...}) (Node
// res.status(201).json(ok(response))).
func (d *Deps) writeStatsCreated(w http.ResponseWriter, rest map[string]any) {
	payload := map[string]any{"source": "stats", "generatedAt": d.generatedAt()}
	for key, value := range rest {
		payload[key] = value
	}
	writeCreatedEnvelope(w, payload)
}

func writeCreatedEnvelope(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// writeMockEnvelope writes ok({source:'mock', ...rest}).
func (d *Deps) writeMockEnvelope(w http.ResponseWriter, status int, rest map[string]any) {
	payload := map[string]any{"source": "mock", "generatedAt": d.generatedAt()}
	for key, value := range rest {
		payload[key] = value
	}
	if status == http.StatusCreated {
		writeCreatedEnvelope(w, payload)
		return
	}
	kernel.WriteOK(w, payload, "")
}

// emptyTarget mirrors publicGroupNotFoundResponse / publicApiKeyNotFoundResponse
// / publicRouteStrategyNotFoundResponse target projection.
func emptyTarget(usernameInput string) PublicTarget {
	username := normalizedText(usernameInput)
	return PublicTarget{Username: username, DisplayName: username, SystemAccountID: ""}
}

// emptyGroupTarget mirrors targetFromInput.
func emptyGroupTarget(usernameInput, groupNameInput string) PublicGroupTarget {
	username := normalizedText(usernameInput)
	groupName := normalizedText(groupNameInput)
	return PublicGroupTarget{
		PublicTarget: PublicTarget{Username: username, DisplayName: username},
		GroupName:    groupName,
	}
}

// sanitizeGroup mirrors sanitizeGroup over a groups.Detail.
func sanitizeGroup(detail *groups.Detail) PublicGroupSummary {
	return PublicGroupSummary{
		ID:           detail.ID,
		Name:         detail.Name,
		ProviderCode: detail.ProviderCode,
		Description:  detail.Description,
		Enabled:      detail.Enabled,
		GroupType:    detail.GroupType,
		IsDefault:    detail.IsDefault,
	}
}

// sanitizeStrategy mirrors sanitizeRouteStrategy over routestrategies.Detail.
func sanitizeStrategy(detail *routestrategies.Detail) PublicStrategySummary {
	bindings := make([]PublicBindingSummary, 0, len(detail.GroupBindings))
	for _, binding := range detail.GroupBindings {
		bindings = append(bindings, PublicBindingSummary{
			ID:           binding.ID,
			GroupID:      binding.GroupID,
			GroupName:    binding.GroupName,
			ProviderCode: binding.ProviderCode,
			Priority:     binding.Priority,
			Weight:       binding.Weight,
			Status:       binding.Status,
			GroupEnabled: binding.GroupEnabled,
		})
	}
	return PublicStrategySummary{
		ID:                  detail.ID,
		Name:                detail.Name,
		Description:         detail.Description,
		Mode:                detail.Mode,
		Status:              detail.Status,
		IsDefault:           detail.IsDefault,
		NormalRoutingConfig: normalRoutingConfigValue(detail.NormalRoutingConfig),
		HybridRoutingConfig: hybridRoutingConfigValue(detail.HybridRoutingConfig),
		GroupBindings:       bindings,
		APIKeyCount:         detail.APIKeyCount,
		CreatedAt:           detail.CreatedAt,
		UpdatedAt:           detail.UpdatedAt,
	}
}

func normalRoutingConfigValue(config *routestrategies.NormalRoutingConfig) any {
	if config == nil {
		return nil
	}
	return config
}

func hybridRoutingConfigValue(config *routestrategies.HybridRoutingConfig) any {
	if config == nil {
		return nil
	}
	return config
}

// sanitizeApiKeyItem mirrors sanitizeApiKey (no secret).
func sanitizeApiKeyItem(item *apikeys.ListItem) PublicApiKeySummary {
	return PublicApiKeySummary{
		ID:                   item.ID,
		Name:                 item.Name,
		KeyPrefix:            item.KeyPrefix,
		Status:               item.Status,
		RouteStrategyID:      item.RouteStrategyID,
		RouteStrategyName:    item.RouteStrategyName,
		RouteStrategyMode:    item.RouteStrategyMode,
		RouteStrategyStatus:  item.RouteStrategyStatus,
		ExpiresAt:            item.ExpiresAt,
		AvailabilitySchedule: availabilityScheduleValue(item.AvailabilitySchedule),
	}
}

func availabilityScheduleValue(schedule *apikeys.AvailabilitySchedule) any {
	if schedule == nil {
		return nil
	}
	return schedule
}

// sanitizeAccountItem mirrors sanitizeAccount plus concurrencyLimit/priority
// (the list projection).
func sanitizeAccountItem(item *accounts.ListItem, supportedModels []string) PublicAccountListItem {
	summary := PublicAccountSummary{
		ID:                  item.ID,
		Name:                item.Name,
		ProviderCode:        item.ProviderCode,
		Type:                item.Type,
		ClientCompatibility: item.ClientCompatibility,
		Status:              item.Status,
		Schedulable:         item.Schedulable,
	}
	if item.ProviderProtocolProfileID != "" {
		profileID := item.ProviderProtocolProfileID
		summary.ProviderProtocolProfileID = &profileID
	}
	if item.ProtocolCode != "" {
		protocolCode := item.ProtocolCode
		summary.ProtocolCode = &protocolCode
	}
	if item.ProtocolVersion != "" {
		protocolVersion := item.ProtocolVersion
		summary.ProtocolVersion = &protocolVersion
	}
	if len(supportedModels) > 0 {
		summary.SupportedModels = supportedModels
	}
	summary.BoundGroupID = item.BoundGroupID
	summary.BoundGroupName = item.BoundGroupName
	if item.AvailabilitySchedule != nil {
		summary.AvailabilitySchedule = item.AvailabilitySchedule
	}
	return PublicAccountListItem{
		PublicAccountSummary: summary,
		ConcurrencyLimit:     item.ConcurrencyLimit,
		Priority:             item.Priority,
	}
}
