package gatewaysession

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Concurrency identity + model priority projections of the Node import chain
// (dispatch/account-concurrency-identity.ts, dispatch/model-filter.ts). Only
// the consumed surface is carried here; the owning slices keep the full
// contracts.

// GatewayAccountConcurrencyIdentity mirrors GatewayAccountConcurrencyIdentity.
type GatewayAccountConcurrencyIdentity struct {
	ID                        string
	CredentialSourceAccountID string
}

// GatewayAccountConcurrencyAccountID mirrors gatewayAccountConcurrencyAccountId.
func GatewayAccountConcurrencyAccountID(account GatewayAccountConcurrencyIdentity) string {
	if trimmed := strings.TrimSpace(account.CredentialSourceAccountID); trimmed != "" {
		return trimmed
	}
	return account.ID
}

// GatewayAccountConcurrencyAccountIDs mirrors gatewayAccountConcurrencyAccountIds.
func GatewayAccountConcurrencyAccountIDs(accounts []GatewayAccountConcurrencyIdentity) []string {
	result := make([]string, 0, len(accounts))
	seen := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		accountID := GatewayAccountConcurrencyAccountID(account)
		if accountID == "" {
			continue
		}
		if _, ok := seen[accountID]; ok {
			continue
		}
		seen[accountID] = struct{}{}
		result = append(result, accountID)
	}
	return result
}

// GatewayAccountConcurrencyIdentityOf projects an account secret.
func GatewayAccountConcurrencyIdentityOf(account gatewayruntimecache.OpenAIAccountSecret) GatewayAccountConcurrencyIdentity {
	credentialSource := ""
	if account.CredentialSourceAccountID != nil {
		credentialSource = *account.CredentialSourceAccountID
	}
	return GatewayAccountConcurrencyIdentity{ID: account.ID, CredentialSourceAccountID: credentialSource}
}

// GatewayAccountConcurrencyIdentities projects a batch of account secrets.
func GatewayAccountConcurrencyIdentities(accounts []gatewayruntimecache.OpenAIAccountSecret) []GatewayAccountConcurrencyIdentity {
	out := make([]GatewayAccountConcurrencyIdentity, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, GatewayAccountConcurrencyIdentityOf(account))
	}
	return out
}

// Model priority ranks mirror gatewayAccountModelPriorityRank.
const (
	ModelPriorityRankDirect      = 0
	ModelPriorityRankMapping     = 1
	ModelPriorityRankUnsupported = 2
)

// GatewayAccountModelPriority mirrors GatewayAccountModelPriority. Only the
// rank map feeds affinity comparisons.
type GatewayAccountModelPriority struct {
	RequestedModel       string
	SourceEndpointFamily string
	RankByAccountID      map[string]int
}

// CompareGatewayAccountModelPriority mirrors compareGatewayAccountModelPriority.
func CompareGatewayAccountModelPriority(leftID string, rightID string, priority *GatewayAccountModelPriority) int {
	return GatewayAccountModelPriorityRank(leftID, priority) - GatewayAccountModelPriorityRank(rightID, priority)
}

// GatewayAccountModelPriorityRank mirrors gatewayAccountModelPriority.
func GatewayAccountModelPriorityRank(accountID string, priority *GatewayAccountModelPriority) int {
	if priority == nil {
		return ModelPriorityRankDirect
	}
	if rank, ok := priority.RankByAccountID[accountID]; ok {
		return rank
	}
	return ModelPriorityRankUnsupported
}

// Group types mirror domain GroupType.
const (
	GroupTypePersonal        = "personal"
	GroupTypeHighConcurrency = "high_concurrency"
	RequestLaneImage         = "image"
	ConcurrencyLaneText      = "text"
)

// AccountInFlightStats mirrors AccountInFlightStats.
type AccountInFlightStats struct {
	CurrentConcurrency   int
	SlowInFlightCount    int
	FirstOutputSlowCount int
	OldestInFlightMs     int64
}

// InFlightThresholds mirrors the loadAccountInFlightStatsByIds input.
type InFlightThresholds struct {
	SlowRequestThresholdMs     int64
	FirstOutputSlowThresholdMs int64
}

// ConcurrencySource ports shared/account-concurrency.ts onto the affinity
// service. Node imports the process-global functions directly; in Go the
// owning slice injects its implementation (memory or Redis backed). The
// process-local guard (assertProcessLocalAccountConcurrencyAllowed) stays
// inside the implementation, matching the Node module boundary.
type ConcurrencySource interface {
	// GetAccountCurrentConcurrency mirrors getAccountCurrentConcurrency;
	// lane "" is the total lane.
	GetAccountCurrentConcurrency(accountID string, lane string) int
	// LoadAccountCurrentConcurrencyByIDsAsync mirrors
	// loadAccountCurrentConcurrencyByIdsAsync; lane "" is the total lane.
	LoadAccountCurrentConcurrencyByIDsAsync(ctx context.Context, accountIDs []string, lane string) (map[string]int, error)
	// LoadAccountInFlightStatsByIDs mirrors loadAccountInFlightStatsByIds.
	LoadAccountInFlightStatsByIDs(accountIDs []string, thresholds InFlightThresholds) map[string]AccountInFlightStats
	// LoadAccountInFlightStatsByIDsAsync mirrors
	// loadAccountInFlightStatsByIdsAsync.
	LoadAccountInFlightStatsByIDsAsync(ctx context.Context, accountIDs []string, thresholds InFlightThresholds) (map[string]AccountInFlightStats, error)
}

// orderOpenAIAccountsByModelPriority mirrors orderOpenAIAccountsByModelPriority
// (stable sort, mirroring V8's stable Array.prototype.sort).
func orderOpenAIAccountsByModelPriority(accounts []gatewayruntimecache.OpenAIAccountSecret, modelPriority *GatewayAccountModelPriority) []gatewayruntimecache.OpenAIAccountSecret {
	if modelPriority == nil || len(accounts) < 2 {
		return accounts
	}
	ordered := append([]gatewayruntimecache.OpenAIAccountSecret(nil), accounts...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return CompareGatewayAccountModelPriority(ordered[i].ID, ordered[j].ID, modelPriority) < 0
	})
	return ordered
}

// accountCurrentConcurrency mirrors accountCurrentConcurrency: the runtime
// stat wins over the account snapshot; both clamp at 0.
func accountCurrentConcurrency(account gatewayruntimecache.OpenAIAccountSecret, runtimeCurrentConcurrency *int) int {
	if runtimeCurrentConcurrency != nil {
		return int(math.Max(0, math.Trunc(float64(*runtimeCurrentConcurrency))))
	}
	if account.CurrentConcurrency != nil {
		return int(math.Max(0, math.Trunc(float64(*account.CurrentConcurrency))))
	}
	return 0
}

// accountHardConcurrencyLimit mirrors accountHardConcurrencyLimit. Go ints are
// always finite, so the Number.isFinite guard collapses to the max(1, …) clamp.
func accountHardConcurrencyLimit(account gatewayruntimecache.OpenAIAccountSecret) int {
	if account.ConcurrencyLimit > 1 {
		return account.ConcurrencyLimit
	}
	return 1
}

func accountFallbackRank(account gatewayruntimecache.OpenAIAccountSecret) int {
	if account.FallbackEnabled {
		return 1
	}
	return 0
}

// compareAccountQualityRank mirrors compareAccountQualityRank: missing quality
// scores rank as +Infinity and never compare as different.
func compareAccountQualityRank(left gatewayruntimecache.OpenAIAccountSecret, right gatewayruntimecache.OpenAIAccountSecret) int {
	leftRank := accountQualityRank(left)
	rightRank := accountQualityRank(right)
	if leftRank == rightRank {
		return 0
	}
	if math.IsInf(leftRank, 1) && math.IsInf(rightRank, 1) {
		return 0
	}
	if leftRank < rightRank {
		return -1
	}
	return 1
}

func accountQualityRank(account gatewayruntimecache.OpenAIAccountSecret) float64 {
	if account.QualityScore != nil {
		return *account.QualityScore
	}
	return math.Inf(1)
}
