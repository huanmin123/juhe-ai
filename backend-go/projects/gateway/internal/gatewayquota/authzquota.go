package gatewayquota

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// AuthorizationQuotaExceededMessage mirrors AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE.
const AuthorizationQuotaExceededMessage = "额度已用完，请联系管理员提升额度"

// authorizationQuotaCacheTTL mirrors AUTHORIZATION_QUOTA_CACHE_TTL_MS.
const authorizationQuotaCacheTTL = 5 * time.Second

// authorizationQuotaCacheMax mirrors the createAppCache max.
const authorizationQuotaCacheMax = 10000

// authorizationRowChunk mirrors chunkValues(ids, 900).
const authorizationRowChunk = 900

// GroupAccessMetadata is the GroupUsageAccessMetadata subset the quota reads.
type GroupAccessMetadata struct {
	GroupAuthorizationID           string
	GroupAuthorizationQuotaLimited bool
}

// AccountAuthorizationSummary is the OpenAIAccountSecret subset the quota reads.
type AccountAuthorizationSummary struct {
	ID                               string
	AccountAuthorizationID           string
	AccountAuthorizationQuotaLimited bool
}

// AuthorizationQuotaRow mirrors the resource_authorizations projection row.
type AuthorizationQuotaRow struct {
	ID                           string
	ResourceOwnerSystemAccountID string
	GranteeSystemAccountID       string
	ResourceType                 string
	ResourceID                   string
	InstanceAccountID            sql.NullString
	EffectiveSourceTeamID        sql.NullString
	LimitsJSON                   sql.NullString
}

// TeamAuthorizationQuotaRow mirrors TeamAuthorizationQuotaRow (+ the batch
// alias authorization_id kept for the loader).
type TeamAuthorizationQuotaRow struct {
	AuthorizationID                     string
	ID                                  string
	ResourceOwnerSystemAccountID        string
	AuthorizationGranteeSystemAccountID sql.NullString
	ResourceType                        string
	ResourceID                          string
	AuthorizationInstanceAccountID      sql.NullString
	LimitsJSON                          sql.NullString
}

// AuthorizationQuotaConfig wires the service.
type AuthorizationQuotaConfig struct {
	Modes     Modes
	Business  *sql.DB
	Stats     *StatsStore
	Timezone  StatsTimezoneProvider
	Snapshot  *SnapshotCache
	Shared    SharedJSONCache
	DBService DBServiceClient
	Syncer    InvalidationSyncer
	Now       func() time.Time
	Log       LogHook
}

// AuthorizationQuotaService ports authorization-quota.service.ts.
type AuthorizationQuotaService struct {
	modes     Modes
	business  *sql.DB
	stats     *StatsStore
	tz        StatsTimezoneProvider
	snapshot  *SnapshotCache
	shared    SharedJSONCache
	dbService DBServiceClient
	syncer    InvalidationSyncer
	now       func() time.Time
	log       LogHook
	memory    *simpleMemoryCache
}

// NewAuthorizationQuotaService validates the wiring and builds the runtime cache.
func NewAuthorizationQuotaService(cfg AuthorizationQuotaConfig) (*AuthorizationQuotaService, error) {
	if cfg.Business == nil {
		return nil, errors.New("gatewayquota authorization quota service requires a business database")
	}
	if cfg.Stats == nil {
		return nil, errors.New("gatewayquota authorization quota service requires a stats store")
	}
	if cfg.Timezone == nil {
		return nil, errors.New("gatewayquota authorization quota service requires a timezone provider")
	}
	if cfg.Snapshot == nil {
		return nil, errors.New("gatewayquota authorization quota service requires the snapshot cache")
	}
	if cfg.Modes.RedisCache && cfg.Shared == nil {
		return nil, errors.New("gatewayquota authorization quota service requires a shared cache in redis mode")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	log := cfg.Log
	if log == nil {
		log = noopLog
	}
	return &AuthorizationQuotaService{
		modes:     cfg.Modes,
		business:  cfg.Business,
		stats:     cfg.Stats,
		tz:        cfg.Timezone,
		snapshot:  cfg.Snapshot,
		shared:    cfg.Shared,
		dbService: cfg.DBService,
		syncer:    cfg.Syncer,
		now:       now,
		log:       log,
		memory:    newSimpleMemoryCache(now, authorizationQuotaCacheTTL, authorizationQuotaCacheMax),
	}, nil
}

func (s *AuthorizationQuotaService) nowMs() int64 { return s.now().UnixMilli() }

// authorizationQuotaCheck is one materialized scope cost evaluation.
type authorizationQuotaCheck struct {
	cacheKey string
	exceeded bool
}

// authorizationQuotaCostCheck is a scope cost request pending materialization.
type authorizationQuotaCostCheck struct {
	cacheKey  string
	limits    RequestQuotaLimits
	costInput CostInput
}

type authorizationQuotaScopeType string

const (
	scopeAccountAuthorization authorizationQuotaScopeType = "account_authorization"
	scopeGroupAuthorization   authorizationQuotaScopeType = "group_authorization"
)

type authorizationQuotaScopeRequest struct {
	authorizationID string
	scopeType       authorizationQuotaScopeType
}

// CheckAuthorizationQuota mirrors checkGatewayAuthorizationQuota.
func (s *AuthorizationQuotaService) CheckAuthorizationQuota(ctx context.Context, groupAccess GroupAccessMetadata, account *AccountAuthorizationSummary, now time.Time) (Decision, error) {
	var accountAuthorizationID string
	if account != nil {
		accountAuthorizationID = account.AccountAuthorizationID
	}
	return s.CheckAuthorizationQuotaByIDs(ctx, groupAccess.GroupAuthorizationID, accountAuthorizationID, now)
}

// CheckAuthorizationQuotaAsync mirrors checkGatewayAuthorizationQuotaAsync.
func (s *AuthorizationQuotaService) CheckAuthorizationQuotaAsync(ctx context.Context, groupAccess GroupAccessMetadata, account *AccountAuthorizationSummary) (Decision, error) {
	if s.modes.RedisRuntimeState && s.syncer != nil {
		if err := s.syncer.SyncGatewayCacheInvalidations(ctx); err != nil {
			return Decision{}, err
		}
	}
	accountAuthorizationID := ""
	accountQuotaLimited := false
	if account != nil {
		accountAuthorizationID = account.AccountAuthorizationID
		accountQuotaLimited = account.AccountAuthorizationQuotaLimited
	}
	if groupAccess.GroupAuthorizationID == "" && accountAuthorizationID == "" {
		return AllowedDecision(), nil
	}
	now := s.now()
	cacheKey, err := s.runtimeCacheKey(ctx, groupAccess.GroupAuthorizationID, accountAuthorizationID, now)
	if err != nil {
		return Decision{}, err
	}
	if !s.modes.RedisCache {
		if cached, ok := s.memory.get(cacheKey); ok {
			return cached.decision(), nil
		}
	}
	if sharedCached, ok, err := s.sharedGet(ctx, cacheKey); err != nil {
		return Decision{}, err
	} else if ok {
		s.setCacheEntry(cacheKey, sharedCached, true)
		return sharedCached.decision(), nil
	}
	if s.modes.RedisCache && s.modes.ServerRole {
		decision, err := s.checkRedisServerRole(ctx, groupAccess, accountAuthorizationID, accountQuotaLimited, cacheKey)
		if err != nil {
			return Decision{}, err
		}
		return decision, nil
	}
	if s.modes.ServerRole {
		snapshotInput := authorizationSnapshotInput{
			groupAuthorizationID:             groupAccess.GroupAuthorizationID,
			groupAuthorizationQuotaLimited:   groupAccess.GroupAuthorizationQuotaLimited,
			accountAuthorizationID:           accountAuthorizationID,
			accountAuthorizationQuotaLimited: accountQuotaLimited,
		}
		if s.snapshotNeedsDBFallback(snapshotInput) {
			decision, dbErr := s.dbServiceCheckAuthorizationQuota(ctx, groupAccess.GroupAuthorizationID, accountAuthorizationID)
			if dbErr == nil {
				if err := s.setCacheEntryAsync(ctx, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
					return Decision{}, err
				}
				return decision, nil
			}
			s.log("gateway_authorization_quota_snapshot_fallback_failed", map[string]any{
				"groupAuthorizationId":   groupAccess.GroupAuthorizationID,
				"accountAuthorizationId": accountAuthorizationID,
				"error":                  dbErr.Error(),
			}, "授权配额快照不完整且 DB service 精确补判失败，按保护策略继续使用快照判定")
		}
		decision := s.decisionFromSnapshot(snapshotInput)
		if err := s.setCacheEntryAsync(ctx, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
			return Decision{}, err
		}
		return decision, nil
	}
	var decision Decision
	if s.modes.PostgresDatabase {
		decision, err = s.CheckAuthorizationQuotaByIDsExactAsync(ctx, groupAccess.GroupAuthorizationID, accountAuthorizationID, now)
	} else {
		decision, err = s.CheckAuthorizationQuotaByIDs(ctx, groupAccess.GroupAuthorizationID, accountAuthorizationID, now)
	}
	if err != nil {
		return Decision{}, err
	}
	if err := s.setCacheEntryAsync(ctx, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

// checkRedisServerRole is the `cacheDriver === 'redis' && processRole ===
// 'server'` branch of checkGatewayAuthorizationQuotaAsync.
func (s *AuthorizationQuotaService) checkRedisServerRole(ctx context.Context, groupAccess GroupAccessMetadata, accountAuthorizationID string, accountQuotaLimited bool, cacheKey string) (Decision, error) {
	snapshotInput := authorizationSnapshotInput{
		groupAuthorizationID:             groupAccess.GroupAuthorizationID,
		groupAuthorizationQuotaLimited:   groupAccess.GroupAuthorizationQuotaLimited,
		accountAuthorizationID:           accountAuthorizationID,
		accountAuthorizationQuotaLimited: accountQuotaLimited,
	}
	snapshotAvailable, err := s.snapshot.HasGatewayQuotaSnapshotAsync(ctx)
	if err != nil {
		return Decision{}, err
	}
	snapshotNeedsFallback := true
	if snapshotAvailable {
		snapshotNeedsFallback, err = s.snapshotNeedsDBFallbackAsync(ctx, snapshotInput)
		if err != nil {
			return Decision{}, err
		}
	}
	if !snapshotNeedsFallback {
		decision, err := s.decisionFromSnapshotAsync(ctx, snapshotInput)
		if err != nil {
			return Decision{}, err
		}
		if err := s.setCacheEntryAsync(ctx, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
			return Decision{}, err
		}
		return decision, nil
	}
	decision, dbErr := s.dbServiceCheckAuthorizationQuota(ctx, groupAccess.GroupAuthorizationID, accountAuthorizationID)
	if dbErr != nil {
		s.log("gateway_authorization_quota_redis_exact_check_failed", map[string]any{
			"groupAuthorizationId":   groupAccess.GroupAuthorizationID,
			"accountAuthorizationId": accountAuthorizationID,
			"snapshotAvailable":      snapshotAvailable,
			"snapshotNeedsFallback":  snapshotNeedsFallback,
			"error":                  dbErr.Error(),
		}, "Redis 模式授权配额共享快照未命中且精确补判失败，按保护策略拒绝请求")
		return DeniedDecision(AuthorizationQuotaExceededMessage), nil
	}
	if err := s.setCacheEntryAsync(ctx, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

// CheckAuthorizationQuotaBatchAsync mirrors checkGatewayAuthorizationQuotaBatchAsync.
func (s *AuthorizationQuotaService) CheckAuthorizationQuotaBatchAsync(ctx context.Context, groupAccess GroupAccessMetadata, accounts []AccountAuthorizationSummary) (map[string]Decision, error) {
	if s.modes.RedisRuntimeState && s.syncer != nil {
		if err := s.syncer.SyncGatewayCacheInvalidations(ctx); err != nil {
			return nil, err
		}
	}
	hasGroupAuthorizationQuota := groupAccess.GroupAuthorizationID != ""
	now := s.now()
	accountsToCheck := make([]AccountAuthorizationSummary, 0, len(accounts))
	if hasGroupAuthorizationQuota {
		accountsToCheck = append(accountsToCheck, accounts...)
	} else {
		for _, account := range accounts {
			if account.AccountAuthorizationID != "" {
				accountsToCheck = append(accountsToCheck, account)
			}
		}
	}
	if len(accountsToCheck) == 0 {
		output := make(map[string]Decision, len(accounts))
		for _, account := range accounts {
			output[account.ID] = AllowedDecision()
		}
		return output, nil
	}
	cachedDecisionsByAccountID := map[string]Decision{}
	missingAccounts := make([]AccountAuthorizationSummary, 0, len(accountsToCheck))
	missingCacheKeys := map[string]string{}
	requestedMissingCacheKeys := map[string]struct{}{}
	for _, account := range accountsToCheck {
		cacheKey, err := s.runtimeCacheKey(ctx, groupAccess.GroupAuthorizationID, account.AccountAuthorizationID, now)
		if err != nil {
			return nil, err
		}
		if !s.modes.RedisCache {
			if cached, ok := s.memory.get(cacheKey); ok {
				cachedDecisionsByAccountID[account.ID] = cached.decision()
				continue
			}
		}
		missingCacheKeys[account.ID] = cacheKey
		if _, seen := requestedMissingCacheKeys[cacheKey]; seen {
			continue
		}
		requestedMissingCacheKeys[cacheKey] = struct{}{}
		missingAccounts = append(missingAccounts, account)
	}
	if len(missingAccounts) == 0 {
		return s.fillBatchOutput(accounts, cachedDecisionsByAccountID), nil
	}
	if s.modes.RedisCache {
		stillMissing := make([]AccountAuthorizationSummary, 0, len(missingAccounts))
		for _, account := range missingAccounts {
			cacheKey := missingCacheKeys[account.ID]
			sharedCached, found, err := s.sharedGet(ctx, cacheKey)
			if err != nil {
				return nil, err
			}
			if found {
				s.setCacheEntry(cacheKey, sharedCached, true)
				cachedDecisionsByAccountID[account.ID] = sharedCached.decision()
				continue
			}
			stillMissing = append(stillMissing, account)
		}
		missingAccounts = stillMissing
		if len(missingAccounts) == 0 {
			return s.fillBatchOutput(accounts, cachedDecisionsByAccountID), nil
		}
	}
	if s.modes.ServerRole {
		return s.checkBatchServerRole(ctx, groupAccess, accounts, accountsToCheck, missingAccounts, missingCacheKeys, cachedDecisionsByAccountID)
	}
	// Worker role: exact batch (postgres) or direct batch (sqlite).
	var missingDecisions []Decision
	var err error
	if s.modes.PostgresDatabase {
		missingDecisions, err = s.CheckAuthorizationQuotaBatchByIDsExactAsync(ctx, groupAccess.GroupAuthorizationID, toAccountRefs(missingAccounts), now)
	} else {
		missingDecisions, err = s.CheckAuthorizationQuotaBatchByIDs(ctx, groupAccess.GroupAuthorizationID, toAccountRefs(missingAccounts), now)
	}
	if err != nil {
		return nil, err
	}
	missingDecisionsByCacheKey := map[string]Decision{}
	for index, account := range missingAccounts {
		decision := AllowedDecision()
		if index < len(missingDecisions) {
			decision = missingDecisions[index]
		}
		cacheKey := missingCacheKeys[account.ID]
		missingDecisionsByCacheKey[cacheKey] = decision
		if err := s.setCacheEntryAsync(ctx, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
			return nil, err
		}
	}
	output := cachedDecisionsByAccountID
	for accountID, cacheKey := range missingCacheKeys {
		if decision, ok := missingDecisionsByCacheKey[cacheKey]; ok {
			output[accountID] = decision
		} else {
			output[accountID] = AllowedDecision()
		}
	}
	return s.fillBatchOutput(accounts, output), nil
}

// checkBatchServerRole ports the processRole === 'server' branch of
// checkGatewayAuthorizationQuotaBatchAsync.
func (s *AuthorizationQuotaService) checkBatchServerRole(
	ctx context.Context,
	groupAccess GroupAccessMetadata,
	accounts []AccountAuthorizationSummary,
	accountsToCheck []AccountAuthorizationSummary,
	missingAccounts []AccountAuthorizationSummary,
	missingCacheKeys map[string]string,
	cachedDecisionsByAccountID map[string]Decision,
) (map[string]Decision, error) {
	output := make(map[string]Decision, len(cachedDecisionsByAccountID)+len(missingCacheKeys))
	for accountID, decision := range cachedDecisionsByAccountID {
		output[accountID] = decision
	}
	if s.modes.RedisCache {
		snapshotAvailable, err := s.snapshot.HasGatewayQuotaSnapshotAsync(ctx)
		if err != nil {
			return nil, err
		}
		if snapshotAvailable {
			snapshotDecisionsByCacheKey := map[string]Decision{}
			stillMissing := make([]AccountAuthorizationSummary, 0, len(missingAccounts))
			for _, account := range missingAccounts {
				cacheKey, ok := missingCacheKeys[account.ID]
				if !ok {
					continue
				}
				snapshotInput := authorizationSnapshotInput{
					groupAuthorizationID:             groupAccess.GroupAuthorizationID,
					groupAuthorizationQuotaLimited:   groupAccess.GroupAuthorizationQuotaLimited,
					accountAuthorizationID:           account.AccountAuthorizationID,
					accountAuthorizationQuotaLimited: account.AccountAuthorizationQuotaLimited,
				}
				needsFallback, err := s.snapshotNeedsDBFallbackAsync(ctx, snapshotInput)
				if err != nil {
					return nil, err
				}
				if needsFallback {
					stillMissing = append(stillMissing, account)
					continue
				}
				decision, err := s.decisionFromSnapshotAsync(ctx, snapshotInput)
				if err != nil {
					return nil, err
				}
				snapshotDecisionsByCacheKey[cacheKey] = decision
				if err := s.setCacheEntryAsync(ctx, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
					return nil, err
				}
			}
			for accountID, cacheKey := range missingCacheKeys {
				if decision, ok := snapshotDecisionsByCacheKey[cacheKey]; ok {
					output[accountID] = decision
				}
			}
			missingAccounts = stillMissing
			if len(missingAccounts) == 0 {
				return s.fillBatchOutput(accounts, output), nil
			}
		}
		batchDecisions, dbErr := s.dbServiceCheckAuthorizationQuotaBatch(ctx, groupAccess.GroupAuthorizationID, toAccountRefs(missingAccounts))
		if dbErr == nil {
			missingDecisionsByCacheKey := map[string]Decision{}
			for index, account := range missingAccounts {
				decision := AllowedDecision()
				if index < len(batchDecisions) {
					decision = batchDecisions[index]
				}
				cacheKey := missingCacheKeys[account.ID]
				missingDecisionsByCacheKey[cacheKey] = decision
				if err := s.setCacheEntryAsync(ctx, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
					return nil, err
				}
			}
			for accountID, cacheKey := range missingCacheKeys {
				if decision, ok := missingDecisionsByCacheKey[cacheKey]; ok {
					output[accountID] = decision
				} else {
					output[accountID] = AllowedDecision()
				}
			}
			return s.fillBatchOutput(accounts, output), nil
		}
		s.log("gateway_authorization_quota_batch_redis_exact_check_failed", map[string]any{
			"groupAuthorizationId": groupAccess.GroupAuthorizationID,
			"accountCount":         len(missingAccounts),
			"error":                dbErr.Error(),
		}, "Redis 模式授权配额批量精确补判失败，按保护策略拒绝请求")
		for _, account := range accounts {
			if _, ok := output[account.ID]; !ok {
				output[account.ID] = DeniedDecision(AuthorizationQuotaExceededMessage)
			}
		}
		return output, nil
	}
	if s.batchSnapshotNeedsDBFallback(groupAccess, missingAccounts) {
		batchDecisions, dbErr := s.dbServiceCheckAuthorizationQuotaBatch(ctx, groupAccess.GroupAuthorizationID, toAccountRefs(missingAccounts))
		if dbErr == nil {
			missingDecisionsByCacheKey := map[string]Decision{}
			for index, account := range missingAccounts {
				decision := AllowedDecision()
				if index < len(batchDecisions) {
					decision = batchDecisions[index]
				}
				cacheKey := missingCacheKeys[account.ID]
				missingDecisionsByCacheKey[cacheKey] = decision
				if err := s.setCacheEntryAsync(ctx, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
					return nil, err
				}
			}
			for accountID, cacheKey := range missingCacheKeys {
				if decision, ok := missingDecisionsByCacheKey[cacheKey]; ok {
					output[accountID] = decision
				} else {
					output[accountID] = AllowedDecision()
				}
			}
			return s.fillBatchOutput(accounts, output), nil
		}
		s.log("gateway_authorization_quota_batch_snapshot_fallback_failed", map[string]any{
			"groupAuthorizationId": groupAccess.GroupAuthorizationID,
			"accountCount":         len(missingAccounts),
			"error":                dbErr.Error(),
		}, "授权配额快照不完整且 DB service 批量补判失败，按保护策略继续使用快照判定")
	}
	for _, account := range missingAccounts {
		cacheKey := missingCacheKeys[account.ID]
		decision := s.decisionFromSnapshot(authorizationSnapshotInput{
			groupAuthorizationID:             groupAccess.GroupAuthorizationID,
			groupAuthorizationQuotaLimited:   groupAccess.GroupAuthorizationQuotaLimited,
			accountAuthorizationID:           account.AccountAuthorizationID,
			accountAuthorizationQuotaLimited: account.AccountAuthorizationQuotaLimited,
		})
		output[account.ID] = decision
		if cacheKey != "" {
			if err := s.setCacheEntryAsync(ctx, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
				return nil, err
			}
		}
	}
	for accountID, cacheKey := range missingCacheKeys {
		if _, ok := output[accountID]; ok {
			continue
		}
		var account *AccountAuthorizationSummary
		for index := range accountsToCheck {
			if accountsToCheck[index].ID == accountID {
				account = &accountsToCheck[index]
				break
			}
		}
		decision := s.decisionFromSnapshot(authorizationSnapshotInput{
			groupAuthorizationID:             groupAccess.GroupAuthorizationID,
			groupAuthorizationQuotaLimited:   groupAccess.GroupAuthorizationQuotaLimited,
			accountAuthorizationID:           accountSummaryAuthorizationID(account),
			accountAuthorizationQuotaLimited: accountSummaryQuotaLimited(account),
		})
		output[accountID] = decision
		if err := s.setCacheEntryAsync(ctx, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
			return nil, err
		}
	}
	return s.fillBatchOutput(accounts, output), nil
}

func accountSummaryAuthorizationID(account *AccountAuthorizationSummary) string {
	if account == nil {
		return ""
	}
	return account.AccountAuthorizationID
}

func accountSummaryQuotaLimited(account *AccountAuthorizationSummary) bool {
	if account == nil {
		return false
	}
	return account.AccountAuthorizationQuotaLimited
}

// fillBatchOutput mirrors the final `input.accounts.forEach(... output.set(...))`
// default fill in every batch branch.
func (s *AuthorizationQuotaService) fillBatchOutput(accounts []AccountAuthorizationSummary, output map[string]Decision) map[string]Decision {
	for _, account := range accounts {
		if _, ok := output[account.ID]; !ok {
			output[account.ID] = AllowedDecision()
		}
	}
	return output
}

func toAccountRefs(accounts []AccountAuthorizationSummary) []AccountRef {
	refs := make([]AccountRef, 0, len(accounts))
	for _, account := range accounts {
		refs = append(refs, AccountRef{AccountID: account.ID, AccountAuthorizationID: account.AccountAuthorizationID})
	}
	return refs
}

// CheckAuthorizationQuotaByIDs mirrors checkGatewayAuthorizationQuotaByIds.
func (s *AuthorizationQuotaService) CheckAuthorizationQuotaByIDs(ctx context.Context, groupAuthorizationID, accountAuthorizationID string, now time.Time) (Decision, error) {
	decisions, err := s.CheckAuthorizationQuotaBatchByIDs(ctx, groupAuthorizationID, []AccountRef{{
		AccountID:              accountAuthorizationID,
		AccountAuthorizationID: accountAuthorizationID,
	}}, now)
	if err != nil {
		return Decision{}, err
	}
	if len(decisions) > 0 {
		return decisions[0], nil
	}
	return AllowedDecision(), nil
}

// CheckAuthorizationQuotaBatchByIDs mirrors checkGatewayAuthorizationQuotaBatchByIds.
func (s *AuthorizationQuotaService) CheckAuthorizationQuotaBatchByIDs(ctx context.Context, groupAuthorizationID string, accounts []AccountRef, now time.Time) ([]Decision, error) {
	if s.modes.ServerRole {
		return nil, errServerLocalSQLite("checkGatewayAuthorizationQuotaBatchByIds")
	}
	if now.IsZero() {
		now = s.now()
	}
	scopes := uniqueAuthorizationQuotaScopes(authorizationQuotaScopeList(groupAuthorizationID, accounts))
	location, err := s.tz.StatsTimezone(ctx)
	if err != nil {
		return nil, err
	}
	costChecksByScope, err := s.loadCostChecksByScope(ctx, scopes, now, location)
	if err != nil {
		return nil, err
	}
	checksByCacheKey, err := s.materializeCostCheckMap(ctx, costChecksByScope, location)
	if err != nil {
		return nil, err
	}
	return s.decisionsForAccounts(groupAuthorizationID, accounts, costChecksByScope, checksByCacheKey), nil
}

// CheckAuthorizationQuotaByIDsReadOnly mirrors checkGatewayAuthorizationQuotaByIdsReadOnly.
func (s *AuthorizationQuotaService) CheckAuthorizationQuotaByIDsReadOnly(ctx context.Context, groupAuthorizationID, accountAuthorizationID string, now time.Time) (Decision, error) {
	decisions, err := s.CheckAuthorizationQuotaBatchByIDsReadOnly(ctx, groupAuthorizationID, []AccountRef{{
		AccountID:              accountAuthorizationID,
		AccountAuthorizationID: accountAuthorizationID,
	}}, now)
	if err != nil {
		return Decision{}, err
	}
	if len(decisions) > 0 {
		return decisions[0], nil
	}
	return AllowedDecision(), nil
}

// CheckAuthorizationQuotaBatchByIDsReadOnly mirrors
// checkGatewayAuthorizationQuotaBatchByIdsReadOnly.
func (s *AuthorizationQuotaService) CheckAuthorizationQuotaBatchByIDsReadOnly(ctx context.Context, groupAuthorizationID string, accounts []AccountRef, now time.Time) ([]Decision, error) {
	if now.IsZero() {
		now = s.now()
	}
	scopes := uniqueAuthorizationQuotaScopes(authorizationQuotaScopeList(groupAuthorizationID, accounts))
	location, err := s.tz.StatsTimezone(ctx)
	if err != nil {
		return nil, err
	}
	costChecksByScope, err := s.loadCostChecksByScope(ctx, scopes, now, location)
	if err != nil {
		return nil, err
	}
	checksByCacheKey, err := s.materializeCostCheckMap(ctx, costChecksByScope, location)
	if err != nil {
		return nil, err
	}
	return s.decisionsForAccountsReadOnly(groupAuthorizationID, accounts, costChecksByScope, checksByCacheKey), nil
}

// CheckAuthorizationQuotaByIDsExactAsync mirrors
// checkGatewayAuthorizationQuotaByIdsExactAsync.
func (s *AuthorizationQuotaService) CheckAuthorizationQuotaByIDsExactAsync(ctx context.Context, groupAuthorizationID, accountAuthorizationID string, now time.Time) (Decision, error) {
	decisions, err := s.CheckAuthorizationQuotaBatchByIDsExactAsync(ctx, groupAuthorizationID, []AccountRef{{
		AccountID:              accountAuthorizationID,
		AccountAuthorizationID: accountAuthorizationID,
	}}, now)
	if err != nil {
		return Decision{}, err
	}
	if len(decisions) > 0 {
		return decisions[0], nil
	}
	return AllowedDecision(), nil
}

// CheckAuthorizationQuotaBatchByIDsExactAsync mirrors
// checkGatewayAuthorizationQuotaBatchByIdsExactAsync.
func (s *AuthorizationQuotaService) CheckAuthorizationQuotaBatchByIDsExactAsync(ctx context.Context, groupAuthorizationID string, accounts []AccountRef, now time.Time) ([]Decision, error) {
	if !s.modes.PostgresDatabase {
		return s.CheckAuthorizationQuotaBatchByIDs(ctx, groupAuthorizationID, accounts, now)
	}
	if s.modes.RedisRuntimeState && s.syncer != nil {
		if err := s.syncer.SyncGatewayCacheInvalidations(ctx); err != nil {
			return nil, err
		}
	}
	if now.IsZero() {
		now = s.now()
	}
	scopes := uniqueAuthorizationQuotaScopes(authorizationQuotaScopeList(groupAuthorizationID, accounts))
	location, err := s.tz.StatsTimezone(ctx)
	if err != nil {
		return nil, err
	}
	costChecksByScope, err := s.loadCostChecksByScope(ctx, scopes, now, location)
	if err != nil {
		return nil, err
	}
	checksByCacheKey, err := s.materializeCostCheckMap(ctx, costChecksByScope, location)
	if err != nil {
		return nil, err
	}
	return s.decisionsForAccounts(groupAuthorizationID, accounts, costChecksByScope, checksByCacheKey), nil
}

// authorizationQuotaScopeList mirrors the scope list construction.
func authorizationQuotaScopeList(groupAuthorizationID string, accounts []AccountRef) []authorizationQuotaScopeRequest {
	scopes := make([]authorizationQuotaScopeRequest, 0, len(accounts)+1)
	if groupAuthorizationID != "" {
		scopes = append(scopes, authorizationQuotaScopeRequest{authorizationID: groupAuthorizationID, scopeType: scopeGroupAuthorization})
	}
	for _, account := range accounts {
		if account.AccountAuthorizationID != "" {
			scopes = append(scopes, authorizationQuotaScopeRequest{authorizationID: account.AccountAuthorizationID, scopeType: scopeAccountAuthorization})
		}
	}
	return scopes
}

func authorizationQuotaScopeKey(authorizationID string, scopeType authorizationQuotaScopeType) string {
	return string(scopeType) + "\x00" + authorizationID
}

func uniqueAuthorizationQuotaScopes(scopes []authorizationQuotaScopeRequest) []authorizationQuotaScopeRequest {
	seen := map[string]struct{}{}
	output := make([]authorizationQuotaScopeRequest, 0, len(scopes))
	for _, scope := range scopes {
		key := authorizationQuotaScopeKey(scope.authorizationID, scope.scopeType)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		output = append(output, scope)
	}
	return output
}

// loadCostChecksByScope mirrors loadAuthorizationQuotaCostChecksByScope(_Async).
func (s *AuthorizationQuotaService) loadCostChecksByScope(ctx context.Context, scopes []authorizationQuotaScopeRequest, now time.Time, location *time.Location) (map[string][]authorizationQuotaCostCheck, error) {
	ids := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		ids = append(ids, scope.authorizationID)
	}
	rowsByID, err := s.loadAuthorizationRows(ctx, ids)
	if err != nil {
		return nil, err
	}
	rows := make([]AuthorizationQuotaRow, 0, len(rowsByID))
	for _, row := range rowsByID {
		rows = append(rows, row)
	}
	teamRowsByAuthorizationID, err := s.loadTeamRowsByAuthorizationID(ctx, rows)
	if err != nil {
		return nil, err
	}
	output := make(map[string][]authorizationQuotaCostCheck, len(scopes))
	for _, scope := range scopes {
		row, ok := rowsByID[scope.authorizationID]
		checks := []authorizationQuotaCostCheck{}
		if ok {
			checks, err = authorizationQuotaCostChecksForAuthorizationRow(row, scope.scopeType, now, location)
			if err != nil {
				return nil, err
			}
			if row.EffectiveSourceTeamID.Valid && row.EffectiveSourceTeamID.String != "" {
				if teamRow, teamOK := teamRowsByAuthorizationID[row.ID]; teamOK {
					teamChecks, err := authorizationQuotaCostChecksForTeamRow(teamRow, scope.scopeType, row.EffectiveSourceTeamID.String, now, location)
					if err != nil {
						return nil, err
					}
					checks = append(checks, teamChecks...)
				}
			}
		}
		output[authorizationQuotaScopeKey(scope.authorizationID, scope.scopeType)] = checks
	}
	return output, nil
}

// authorizationQuotaCostChecksForAuthorizationRow mirrors
// authorizationQuotaCostChecksForAuthorizationRow(_Async): a limits_json
// parse error propagates like the Node throw (fail-closed), only a successful
// parse without any enabled limit yields no checks.
func authorizationQuotaCostChecksForAuthorizationRow(row AuthorizationQuotaRow, scopeType authorizationQuotaScopeType, now time.Time, location *time.Location) ([]authorizationQuotaCostCheck, error) {
	limits, err := ParseRequestQuotaLimitsJSON(nullStringOrEmpty(row.LimitsJSON))
	if err != nil {
		return nil, err
	}
	if !HasEnabledRequestQuotaLimit(limits) {
		return nil, nil
	}
	systemAccountID := authorizationQuotaStatsSystemAccountID(row, scopeType)
	hourly, hasHourly := limitsHourly(limits)
	costInput := CostInput{
		SystemAccountID:   systemAccountID,
		ScopeType:         string(scopeType),
		ScopeID:           row.ID,
		Now:               now,
		HourlyWindowHours: hourly,
		HasHourlyWindow:   hasHourly,
	}
	return []authorizationQuotaCostCheck{{
		cacheKey:  "authorization\x00" + systemAccountID + "\x00" + string(scopeType) + "\x00" + row.ID + "\x00" + CostKey(costInput, location) + "\x00" + nullStringOrEmpty(row.LimitsJSON),
		limits:    limits,
		costInput: costInput,
	}}, nil
}

// authorizationQuotaCostChecksForTeamRow mirrors
// authorizationQuotaCostChecksForTeamRow(_Async): a limits_json parse error
// propagates like the Node throw (fail-closed).
func authorizationQuotaCostChecksForTeamRow(row TeamAuthorizationQuotaRow, scopeType authorizationQuotaScopeType, teamID string, now time.Time, location *time.Location) ([]authorizationQuotaCostCheck, error) {
	limits, err := ParseRequestQuotaLimitsJSON(nullStringOrEmpty(row.LimitsJSON))
	if err != nil {
		return nil, err
	}
	if !HasEnabledRequestQuotaLimit(limits) {
		return nil, nil
	}
	systemAccountID := teamAuthorizationQuotaStatsSystemAccountID(row, scopeType)
	resourceID, hasResourceID := teamAuthorizationResourceID(row, scopeType)
	if !hasResourceID || resourceID == "" {
		return nil, nil
	}
	scopeID := resourceID + ":" + teamID
	hourly, hasHourly := limitsHourly(limits)
	costInput := CostInput{
		SystemAccountID:   systemAccountID,
		ScopeType:         string(teamAuthorizationScopeType(scopeType)),
		ScopeID:           scopeID,
		Now:               now,
		HourlyWindowHours: hourly,
		HasHourlyWindow:   hasHourly,
	}
	return []authorizationQuotaCostCheck{{
		cacheKey:  "team_authorization\x00" + systemAccountID + "\x00" + string(scopeType) + "\x00" + teamID + "\x00" + row.ID + "\x00" + CostKey(costInput, location) + "\x00" + nullStringOrEmpty(row.LimitsJSON),
		limits:    limits,
		costInput: costInput,
	}}, nil
}

func limitsHourly(limits RequestQuotaLimits) (int, bool) {
	if limits.Hourly == nil {
		return 0, false
	}
	return limits.Hourly.Hours, true
}

// materializeCostCheckMap mirrors materializeAuthorizationQuotaCostCheckMap(_Async).
func (s *AuthorizationQuotaService) materializeCostCheckMap(ctx context.Context, costChecksByScope map[string][]authorizationQuotaCostCheck, location *time.Location) (map[string]authorizationQuotaCheck, error) {
	all := uniqueAuthorizationQuotaCostChecks(costChecksByScope)
	inputs := make([]CostInput, 0, len(all))
	for _, check := range all {
		inputs = append(inputs, check.costInput)
	}
	costsByKey, err := s.stats.LoadCostsBatch(ctx, inputs, location)
	if err != nil {
		return nil, err
	}
	checks := make(map[string]authorizationQuotaCheck, len(all))
	for _, check := range all {
		costs := EmptyRequestQuotaCosts()
		if loaded, ok := costsByKey[CostKey(check.costInput, location)]; ok {
			costs = loaded
		}
		checks[check.cacheKey] = authorizationQuotaCheck{
			cacheKey: check.cacheKey,
			exceeded: IsRequestQuotaExceeded(check.limits, costs),
		}
	}
	return checks, nil
}

// uniqueAuthorizationQuotaCostChecks mirrors uniqueAuthorizationQuotaCostChecks.
func uniqueAuthorizationQuotaCostChecks(byScope map[string][]authorizationQuotaCostCheck) []authorizationQuotaCostCheck {
	seen := map[string]struct{}{}
	output := []authorizationQuotaCostCheck{}
	for _, checks := range byScope {
		for _, check := range checks {
			if _, dup := seen[check.cacheKey]; dup {
				continue
			}
			seen[check.cacheKey] = struct{}{}
			output = append(output, check)
		}
	}
	return output
}

// decisionsForAccounts mirrors the per-account check assembly and
// authorizationQuotaDecisionFromChecks.
func (s *AuthorizationQuotaService) decisionsForAccounts(
	groupAuthorizationID string,
	accounts []AccountRef,
	costChecksByScope map[string][]authorizationQuotaCostCheck,
	checksByCacheKey map[string]authorizationQuotaCheck,
) []Decision {
	output := make([]Decision, 0, len(accounts))
	for _, account := range accounts {
		checks := authorizationQuotaChecksForScope(groupAuthorizationID, scopeGroupAuthorization, costChecksByScope, checksByCacheKey)
		checks = append(checks, authorizationQuotaChecksForScope(account.AccountAuthorizationID, scopeAccountAuthorization, costChecksByScope, checksByCacheKey)...)
		if len(checks) == 0 {
			output = append(output, AllowedDecision())
			continue
		}
		cacheKey := joinCheckCacheKeys(checks)
		if !s.modes.RedisCache {
			if cached, ok := s.memory.get(cacheKey); ok {
				output = append(output, cached.decision())
				continue
			}
		}
		allowed := true
		for _, check := range checks {
			if check.exceeded {
				allowed = false
				break
			}
		}
		decision := Decision{Allowed: allowed}
		if !allowed {
			decision.Message = AuthorizationQuotaExceededMessage
		}
		if !s.modes.RedisCache {
			s.setCacheEntry(cacheKey, newCachedDecision(decision, s.nowMs()), false)
		}
		output = append(output, decision)
	}
	return output
}

// decisionsForAccountsReadOnly mirrors authorizationQuotaDecisionFromChecksReadOnly.
func (s *AuthorizationQuotaService) decisionsForAccountsReadOnly(
	groupAuthorizationID string,
	accounts []AccountRef,
	costChecksByScope map[string][]authorizationQuotaCostCheck,
	checksByCacheKey map[string]authorizationQuotaCheck,
) []Decision {
	output := make([]Decision, 0, len(accounts))
	for _, account := range accounts {
		checks := authorizationQuotaChecksForScope(groupAuthorizationID, scopeGroupAuthorization, costChecksByScope, checksByCacheKey)
		checks = append(checks, authorizationQuotaChecksForScope(account.AccountAuthorizationID, scopeAccountAuthorization, costChecksByScope, checksByCacheKey)...)
		if len(checks) == 0 {
			output = append(output, AllowedDecision())
			continue
		}
		allowed := true
		for _, check := range checks {
			if check.exceeded {
				allowed = false
				break
			}
		}
		decision := Decision{Allowed: allowed}
		if !allowed {
			decision.Message = AuthorizationQuotaExceededMessage
		}
		output = append(output, decision)
	}
	return output
}

func joinCheckCacheKeys(checks []authorizationQuotaCheck) string {
	keys := make([]string, 0, len(checks))
	for _, check := range checks {
		keys = append(keys, check.cacheKey)
	}
	return strings.Join(keys, "|")
}

// authorizationQuotaChecksForScope mirrors authorizationQuotaChecksForScope.
func authorizationQuotaChecksForScope(
	authorizationID string,
	scopeType authorizationQuotaScopeType,
	costChecksByScope map[string][]authorizationQuotaCostCheck,
	checksByCacheKey map[string]authorizationQuotaCheck,
) []authorizationQuotaCheck {
	if authorizationID == "" {
		return nil
	}
	checks := []authorizationQuotaCheck{}
	for _, pending := range costChecksByScope[authorizationQuotaScopeKey(authorizationID, scopeType)] {
		if materialized, ok := checksByCacheKey[pending.cacheKey]; ok {
			checks = append(checks, materialized)
		}
	}
	return checks
}

// runtimeCacheKey mirrors authorizationQuotaRuntimeCacheKey(_Async). The
// server role rotates with the authorization snapshot version so bumps
// invalidate the runtime entries.
func (s *AuthorizationQuotaService) runtimeCacheKey(ctx context.Context, groupAuthorizationID, accountAuthorizationID string, now time.Time) (string, error) {
	location, err := s.tz.StatsTimezone(ctx)
	if err != nil {
		return "", err
	}
	windowKey := CostKey(CostInput{
		SystemAccountID: "",
		ScopeType:       ScopeTypeAuthorizationRuntime,
		ScopeID:         "",
		Now:             now,
	}, location)
	version := int64(0)
	if s.modes.ServerRole {
		version = s.snapshot.AuthorizationQuotaSnapshotVersion()
	}
	return "runtime_authorization_quota\x00" + groupAuthorizationID + "\x00" + accountAuthorizationID + "\x00" + windowKey + "\x00" + itoa64(version), nil
}

func itoa64(value int64) string {
	digits := ""
	negative := value < 0
	if negative {
		value = -value
	}
	if value == 0 {
		return "0"
	}
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	if negative {
		return "-" + digits
	}
	return digits
}

// loadAuthorizationRows mirrors loadAuthorizationQuotaRows(_Async).
func (s *AuthorizationQuotaService) loadAuthorizationRows(ctx context.Context, authorizationIDs []string) (map[string]AuthorizationQuotaRow, error) {
	ids := uniqueNonEmpty(authorizationIDs)
	rowsByID := map[string]AuthorizationQuotaRow{}
	if len(ids) == 0 {
		return rowsByID, nil
	}
	for _, chunk := range chunkStrings(ids, authorizationRowChunk) {
		rows, err := s.queryAuthorizationRows(ctx, chunk)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			rowsByID[row.ID] = row
		}
	}
	return rowsByID, nil
}

func (s *AuthorizationQuotaService) queryAuthorizationRows(ctx context.Context, ids []string) ([]AuthorizationQuotaRow, error) {
	query := `
		SELECT ra.id, ra.resource_owner_system_account_id, ra.grantee_system_account_id, ra.resource_type, ra.resource_id,
			instance_accounts.id AS instance_account_id,
			ra.effective_source_team_id, ra.limits_json
		FROM ` + statsBusinessTable(s.modes.PostgresDatabase, "resource_authorizations") + ` ra
		LEFT JOIN ` + statsBusinessTable(s.modes.PostgresDatabase, "accounts") + ` instance_accounts
			ON ra.resource_type = 'account'
			AND instance_accounts.authorization_instance_authorization_id = ra.id
			AND instance_accounts.system_account_id = ra.grantee_system_account_id
			AND instance_accounts.authorization_instance_source_account_id = ra.resource_id
		WHERE ra.status = 'active'
			AND ra.id IN (` + sqlPlaceholders(len(ids)) + `)`
	rows, err := s.business.QueryContext(ctx, bindPlaceholders(s.modes.PostgresDatabase, query), toAnySlice(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	output := []AuthorizationQuotaRow{}
	for rows.Next() {
		var row AuthorizationQuotaRow
		if err := rows.Scan(&row.ID, &row.ResourceOwnerSystemAccountID, &row.GranteeSystemAccountID, &row.ResourceType, &row.ResourceID,
			&row.InstanceAccountID, &row.EffectiveSourceTeamID, &row.LimitsJSON); err != nil {
			return nil, err
		}
		output = append(output, row)
	}
	return output, rows.Err()
}

// loadTeamRowsByAuthorizationID mirrors loadTeamAuthorizationQuotaRowsByAuthorizationId(_Async):
// the last row per authorization id wins (Node map overwrite semantics).
func (s *AuthorizationQuotaService) loadTeamRowsByAuthorizationID(ctx context.Context, rows []AuthorizationQuotaRow) (map[string]TeamAuthorizationQuotaRow, error) {
	ids := []string{}
	for _, row := range rows {
		if row.EffectiveSourceTeamID.Valid && row.EffectiveSourceTeamID.String != "" {
			ids = append(ids, row.ID)
		}
	}
	teamRowsByAuthorizationID := map[string]TeamAuthorizationQuotaRow{}
	if len(ids) == 0 {
		return teamRowsByAuthorizationID, nil
	}
	for _, chunk := range chunkStrings(ids, authorizationRowChunk) {
		query := `
			SELECT ra.id AS authorization_id, grant_rows.id, grant_rows.resource_owner_system_account_id,
				ra.grantee_system_account_id AS authorization_grantee_system_account_id,
				instance_accounts.id AS authorization_instance_account_id,
				grant_rows.resource_type, grant_rows.resource_id, grant_rows.limits_json
			FROM ` + statsBusinessTable(s.modes.PostgresDatabase, "resource_authorizations") + ` ra
			INNER JOIN ` + statsBusinessTable(s.modes.PostgresDatabase, "resource_authorization_grants") + ` grant_rows
				ON grant_rows.resource_type = ra.resource_type
				AND grant_rows.resource_id = ra.resource_id
				AND grant_rows.grantee_type = 'team'
				AND grant_rows.grantee_team_id = ra.effective_source_team_id
				AND grant_rows.status = 'active'
			LEFT JOIN ` + statsBusinessTable(s.modes.PostgresDatabase, "accounts") + ` instance_accounts
				ON ra.resource_type = 'account'
				AND instance_accounts.authorization_instance_authorization_id = ra.id
				AND instance_accounts.system_account_id = ra.grantee_system_account_id
				AND instance_accounts.authorization_instance_source_account_id = ra.resource_id
			WHERE ra.status = 'active'
				AND ra.effective_source_team_id IS NOT NULL
				AND ra.id IN (` + sqlPlaceholders(len(chunk)) + `)`
		rows, err := s.business.QueryContext(ctx, bindPlaceholders(s.modes.PostgresDatabase, query), toAnySlice(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row TeamAuthorizationQuotaRow
			if err := rows.Scan(&row.AuthorizationID, &row.ID, &row.ResourceOwnerSystemAccountID,
				&row.AuthorizationGranteeSystemAccountID, &row.AuthorizationInstanceAccountID,
				&row.ResourceType, &row.ResourceID, &row.LimitsJSON); err != nil {
				rows.Close()
				return nil, err
			}
			teamRowsByAuthorizationID[row.AuthorizationID] = row
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return teamRowsByAuthorizationID, nil
}

// authorizationQuotaStatsSystemAccountID mirrors authorizationQuotaStatsSystemAccountId.
func authorizationQuotaStatsSystemAccountID(row AuthorizationQuotaRow, scopeType authorizationQuotaScopeType) string {
	if scopeType == scopeAccountAuthorization {
		return row.GranteeSystemAccountID
	}
	return row.ResourceOwnerSystemAccountID
}

// teamAuthorizationQuotaStatsSystemAccountID mirrors
// teamAuthorizationQuotaStatsSystemAccountId.
func teamAuthorizationQuotaStatsSystemAccountID(row TeamAuthorizationQuotaRow, scopeType authorizationQuotaScopeType) string {
	if scopeType == scopeAccountAuthorization {
		if row.AuthorizationGranteeSystemAccountID.Valid && row.AuthorizationGranteeSystemAccountID.String != "" {
			return row.AuthorizationGranteeSystemAccountID.String
		}
		return row.ResourceOwnerSystemAccountID
	}
	return row.ResourceOwnerSystemAccountID
}

// teamAuthorizationResourceID mirrors teamAuthorizationResourceId.
func teamAuthorizationResourceID(row TeamAuthorizationQuotaRow, scopeType authorizationQuotaScopeType) (string, bool) {
	if scopeType == scopeAccountAuthorization {
		if !row.AuthorizationInstanceAccountID.Valid {
			return "", false
		}
		return row.AuthorizationInstanceAccountID.String, true
	}
	return row.ResourceID, true
}

// teamAuthorizationScopeType mirrors teamAuthorizationScopeType.
func teamAuthorizationScopeType(scopeType authorizationQuotaScopeType) authorizationQuotaScopeType {
	if scopeType == scopeAccountAuthorization {
		return "account_authorization_team"
	}
	return "group_authorization_team"
}

func nullStringOrEmpty(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]struct{}{}
	output := []string{}
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		output = append(output, value)
	}
	return output
}

func chunkStrings(values []string, size int) [][]string {
	if size < 1 {
		size = 1
	}
	chunks := [][]string{}
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		chunks = append(chunks, values[start:end])
	}
	return chunks
}

func sqlPlaceholders(count int) string {
	if count < 1 {
		count = 1
	}
	placeholders := make([]string, count)
	for i := range placeholders {
		placeholders[i] = "?"
	}
	return strings.Join(placeholders, ",")
}

func toAnySlice(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

func (s *AuthorizationQuotaService) dbServiceCheckAuthorizationQuota(ctx context.Context, groupAuthorizationID, accountAuthorizationID string) (Decision, error) {
	if s.dbService == nil {
		return Decision{}, errors.New("db service client is not configured")
	}
	return s.dbService.CheckAuthorizationQuota(ctx, groupAuthorizationID, accountAuthorizationID)
}

func (s *AuthorizationQuotaService) dbServiceCheckAuthorizationQuotaBatch(ctx context.Context, groupAuthorizationID string, accounts []AccountRef) ([]Decision, error) {
	if s.dbService == nil {
		return nil, errors.New("db service client is not configured")
	}
	return s.dbService.CheckAuthorizationQuotaBatch(ctx, groupAuthorizationID, accounts)
}

// sharedGet mirrors getAuthorizationQuotaSharedCacheEntry (redis only).
func (s *AuthorizationQuotaService) sharedGet(ctx context.Context, cacheKey string) (CachedDecision, bool, error) {
	if !s.modes.RedisCache {
		return CachedDecision{}, false, nil
	}
	var entry CachedDecision
	found, err := s.shared.Get(ctx, sharedCacheKey(cacheKey), &entry)
	if err != nil || !found {
		return CachedDecision{}, false, err
	}
	return entry, true, nil
}

// setCacheEntry mirrors setAuthorizationQuotaCacheEntry.
func (s *AuthorizationQuotaService) setCacheEntry(cacheKey string, entry CachedDecision, skipShared bool) {
	if s.modes.RedisCache {
		if !skipShared {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := s.shared.Set(ctx, sharedCacheKey(cacheKey), entry, authorizationQuotaCacheTTL); err != nil {
				s.log("authorization_quota_shared_cache_set_failed", map[string]any{"error": err.Error()},
					"授权配额 Redis shared cache 写入失败")
			}
		}
		return
	}
	s.memory.set(cacheKey, entry)
}

// setCacheEntryAsync mirrors setAuthorizationQuotaCacheEntryAsync.
func (s *AuthorizationQuotaService) setCacheEntryAsync(ctx context.Context, cacheKey string, entry CachedDecision) error {
	if s.modes.RedisCache {
		return s.shared.Set(ctx, sharedCacheKey(cacheKey), entry, authorizationQuotaCacheTTL)
	}
	s.memory.set(cacheKey, entry)
	return nil
}

// ClearCache mirrors clearAuthorizationQuotaCache.
func (s *AuthorizationQuotaService) ClearCache(ctx context.Context) {
	s.memory.clear()
	if s.modes.RedisCache {
		if err := s.shared.Clear(ctx); err != nil {
			s.log("authorization_quota_shared_cache_clear_failed", map[string]any{"error": err.Error()},
				"授权配额 Redis shared cache 清理失败")
		}
	}
}

// ClearCacheAsync mirrors clearAuthorizationQuotaCacheAsync (shared clear
// errors propagate).
func (s *AuthorizationQuotaService) ClearCacheAsync(ctx context.Context) error {
	s.memory.clear()
	if s.modes.RedisCache {
		return s.shared.Clear(ctx)
	}
	return nil
}

// authorizationSnapshotInput mirrors the snapshot input literal.
type authorizationSnapshotInput struct {
	groupAuthorizationID             string
	groupAuthorizationQuotaLimited   bool
	accountAuthorizationID           string
	accountAuthorizationQuotaLimited bool
}

// batchSnapshotNeedsDBFallback mirrors authorizationQuotaBatchSnapshotNeedsDbFallback.
func (s *AuthorizationQuotaService) batchSnapshotNeedsDBFallback(groupAccess GroupAccessMetadata, accounts []AccountAuthorizationSummary) bool {
	if !s.snapshot.IsAuthorizationSnapshotIncomplete() {
		return false
	}
	if groupAccess.GroupAuthorizationID != "" && groupAccess.GroupAuthorizationQuotaLimited {
		if _, found := s.snapshot.ReadAuthorizationSnapshot(string(scopeGroupAuthorization), groupAccess.GroupAuthorizationID); !found {
			return true
		}
	}
	for _, account := range accounts {
		if s.snapshotNeedsDBFallback(authorizationSnapshotInput{
			groupAuthorizationID:             groupAccess.GroupAuthorizationID,
			groupAuthorizationQuotaLimited:   groupAccess.GroupAuthorizationQuotaLimited,
			accountAuthorizationID:           account.AccountAuthorizationID,
			accountAuthorizationQuotaLimited: account.AccountAuthorizationQuotaLimited,
		}) {
			return true
		}
	}
	return false
}

// snapshotNeedsDBFallback mirrors authorizationQuotaSnapshotNeedsDbFallback.
func (s *AuthorizationQuotaService) snapshotNeedsDBFallback(input authorizationSnapshotInput) bool {
	if !s.snapshot.IsAuthorizationSnapshotIncomplete() {
		return false
	}
	if input.groupAuthorizationID != "" && input.groupAuthorizationQuotaLimited {
		if _, found := s.snapshot.ReadAuthorizationSnapshot(string(scopeGroupAuthorization), input.groupAuthorizationID); !found {
			return true
		}
	}
	return input.accountAuthorizationID != "" && input.accountAuthorizationQuotaLimited &&
		func() bool {
			_, found := s.snapshot.ReadAuthorizationSnapshot(string(scopeAccountAuthorization), input.accountAuthorizationID)
			return !found
		}()
}

// snapshotNeedsDBFallbackAsync mirrors authorizationQuotaSnapshotNeedsDbFallbackAsync.
func (s *AuthorizationQuotaService) snapshotNeedsDBFallbackAsync(ctx context.Context, input authorizationSnapshotInput) (bool, error) {
	incomplete, err := s.snapshot.IsAuthorizationSnapshotIncompleteAsync(ctx)
	if err != nil {
		return false, err
	}
	if !incomplete {
		return false, nil
	}
	if input.groupAuthorizationID != "" && input.groupAuthorizationQuotaLimited {
		_, found, err := s.snapshot.ReadAuthorizationSnapshotAsync(ctx, string(scopeGroupAuthorization), input.groupAuthorizationID)
		if err != nil {
			return false, err
		}
		if !found {
			return true, nil
		}
	}
	if input.accountAuthorizationID != "" && input.accountAuthorizationQuotaLimited {
		_, found, err := s.snapshot.ReadAuthorizationSnapshotAsync(ctx, string(scopeAccountAuthorization), input.accountAuthorizationID)
		if err != nil {
			return false, err
		}
		if !found {
			return true, nil
		}
	}
	return false, nil
}

// decisionFromSnapshot mirrors authorizationQuotaDecisionFromSnapshot.
func (s *AuthorizationQuotaService) decisionFromSnapshot(input authorizationSnapshotInput) Decision {
	groupDecision, found := s.snapshot.ReadAuthorizationSnapshot(string(scopeGroupAuthorization), input.groupAuthorizationID)
	if found && !groupDecision.Allowed {
		return groupDecision
	}
	if input.groupAuthorizationID != "" && input.groupAuthorizationQuotaLimited && !found && s.snapshot.IsAuthorizationSnapshotIncomplete() {
		return DeniedDecision(AuthorizationQuotaExceededMessage)
	}
	accountDecision, accountFound := s.snapshot.ReadAuthorizationSnapshot(string(scopeAccountAuthorization), input.accountAuthorizationID)
	if accountFound && !accountDecision.Allowed {
		return accountDecision
	}
	if input.accountAuthorizationID != "" && input.accountAuthorizationQuotaLimited && !accountFound && s.snapshot.IsAuthorizationSnapshotIncomplete() {
		return DeniedDecision(AuthorizationQuotaExceededMessage)
	}
	return AllowedDecision()
}

// decisionFromSnapshotAsync mirrors authorizationQuotaDecisionFromSnapshotAsync.
func (s *AuthorizationQuotaService) decisionFromSnapshotAsync(ctx context.Context, input authorizationSnapshotInput) (Decision, error) {
	groupDecision, groupFound, err := s.snapshot.ReadAuthorizationSnapshotAsync(ctx, string(scopeGroupAuthorization), input.groupAuthorizationID)
	if err != nil {
		return Decision{}, err
	}
	if groupFound && !groupDecision.Allowed {
		return groupDecision, nil
	}
	snapshotIncomplete, err := s.snapshot.IsAuthorizationSnapshotIncompleteAsync(ctx)
	if err != nil {
		return Decision{}, err
	}
	if input.groupAuthorizationID != "" && input.groupAuthorizationQuotaLimited && !groupFound && snapshotIncomplete {
		return DeniedDecision(AuthorizationQuotaExceededMessage), nil
	}
	accountDecision, accountFound, err := s.snapshot.ReadAuthorizationSnapshotAsync(ctx, string(scopeAccountAuthorization), input.accountAuthorizationID)
	if err != nil {
		return Decision{}, err
	}
	if accountFound && !accountDecision.Allowed {
		return accountDecision, nil
	}
	if input.accountAuthorizationID != "" && input.accountAuthorizationQuotaLimited && !accountFound && snapshotIncomplete {
		return DeniedDecision(AuthorizationQuotaExceededMessage), nil
	}
	return AllowedDecision(), nil
}
