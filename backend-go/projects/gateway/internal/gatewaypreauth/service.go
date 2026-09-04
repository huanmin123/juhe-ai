package gatewaypreauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// RuntimeCacheReader is the read surface of the gateway runtime cache the
// pre-auth and preflight pipeline consumes. *gatewayruntimecache.Service
// implements it directly (work package G10).
type RuntimeCacheReader interface {
	ReadCachedGatewayRuntimeAsync(ctx context.Context, apiKey string) (gatewayruntimecache.GatewayRuntime, error)
	ReadCachedGatewaySettingsAsync(ctx context.Context) (gatewayruntimecache.GatewaySettings, error)
	ResolveCachedGroupUsageAccessMetadataAsync(ctx context.Context, groupID, systemAccountID string) (*gatewayruntimecache.GroupUsageAccessMetadata, error)
	ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, opts gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error)
	ListFreshOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, opts gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error)
	ListRecoverableUnavailableOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, opts gatewayruntimecache.CachedOpenAIAccountsForGroupOptions, windowMs *int64) ([]gatewayruntimecache.OpenAIAccountSecret, error)
	ListCachedActiveResponseInspectionPoliciesForAccountsAsync(ctx context.Context, accounts []gatewayruntimecache.OpenAIAccountSecret) ([]gatewayruntimecache.ResponseInspectionPolicySummary, error)
	ListCachedProviderModelCatalogAsync(ctx context.Context, input gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error)
}

// APIKeyQuotaChecker mirrors checkGatewayApiKeyQuotaAsync. The concrete
// *gatewayquota.APIKeyQuotaService implements it (work package G07).
type APIKeyQuotaChecker interface {
	CheckAPIKeyQuotaAsync(ctx context.Context, apiKey gatewayquota.APIKeyRow) (gatewayquota.Decision, error)
}

// AuthorizationQuotaChecker mirrors checkGatewayAuthorizationQuotaAsync. The
// concrete *gatewayquota.AuthorizationQuotaService implements it.
type AuthorizationQuotaChecker interface {
	CheckAuthorizationQuotaAsync(ctx context.Context, groupAccess gatewayquota.GroupAccessMetadata, account *gatewayquota.AccountAuthorizationSummary) (gatewayquota.Decision, error)
}

// InflightReserver mirrors reserveGatewayApiKeyInflightCost. The concrete
// *gatewayquota.InflightQuotaService implements it.
type InflightReserver interface {
	ReserveGatewayCost(ctx context.Context, input gatewayquota.GatewayReserveInput) (gatewayquota.InflightDecision, error)
}

// GeminiInteractionAffinity removed: the preflight uses the concrete
// *gatewaygemini.InteractionAffinity (G04) which is fully injectable through
// its AffinityStateStore.

// GatewayAPIKeyValidator mirrors validateGatewayApiKeyAsync
// (storage/gateway-api-key.repository.ts): the models fast path validates the
// raw key through the repository read instead of the runtime cache. The
// concrete adapter belongs to the composition root.
type GatewayAPIKeyValidator interface {
	Validate(ctx context.Context, apiKey string) (*gatewayruntimecache.GatewayAPIKeyRow, error)
}

// Service is the G05 pre-auth + preflight orchestration service. Direct
// collaborators (runtime cache, quota services, gemini affinity) are
// interfaces satisfied by the existing concrete packages; the later-slice
// collaborators arrive through the ports.
type Service struct {
	// direct collaborators (import + reuse)
	RuntimeCache       RuntimeCacheReader
	APIKeyQuota        APIKeyQuotaChecker
	AuthorizationQuota AuthorizationQuotaChecker
	InflightQuota      InflightReserver
	Affinity           *gatewaygemini.InteractionAffinity
	APIKeyValidator    GatewayAPIKeyValidator
	RouteResolver      RouteResolver
	AccountAvoidance   ClientIPAccountAvoidanceFactory

	// ports toward later slices
	Circuits        PreAuthCircuits
	IPPolicy        ClientIPPolicy
	UserLimits      UserRequestLimits
	ModelsRateLimit AuthenticatedModelsRateLimit
	ClientStrategy  ClientStrategy
	SessionIdentity SessionIdentityResolver
	SessionAffinity SessionAffinity
	Codex           CodexBridgePreflight
	Candidates      CandidatePipeline
	Images          ImagePermissionPreflight
	Responses       ResponseSink
	Recoverable     RecoverableWait
	AuditSettings   AuditSettings
	AuditDispatch   AuditDispatcher
	Observability   Observability

	// Clock injects time (tests); defaults to the system clock.
	Clock Clock
}

// New validates the required wiring. The runtime cache and the observability
// surface are mandatory; everything else degrades to a disabled port so the
// orchestrations that do not touch them still run.
func New(deps Service) (*Service, error) {
	if deps.RuntimeCache == nil {
		return nil, errors.New("gatewaypreauth 需要 RuntimeCache")
	}
	if deps.Observability == nil {
		return nil, errors.New("gatewaypreauth 需要 Observability")
	}
	if deps.Clock == nil {
		deps.Clock = SystemClock{}
	}
	return &deps, nil
}

// NowMs returns the injected clock in unix milliseconds.
func (s *Service) NowMs() int64 { return s.Clock.Now().UnixMilli() }

// StartedAt records a stage start (performance.now equivalent).
func (s *Service) StartedAt() time.Time { return s.Clock.Now() }

// newAuditID mirrors `audit_${Date.now()}_${randomUUID()}`.
func (s *Service) newAuditID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("audit_%d", s.NowMs())
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	dst := make([]byte, 36)
	hex.Encode(dst, buf[:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], buf[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], buf[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], buf[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:], buf[10:])
	return fmt.Sprintf("audit_%d_%s", s.NowMs(), string(dst))
}
