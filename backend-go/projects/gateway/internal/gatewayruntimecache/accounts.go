package gatewayruntimecache

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// credentialVersionReadTimeout 是请求路径上凭据版本批量读的预算上限（计划
// §5 硬约束 7：每请求最多一次批量读、50–100ms）。读失败/超时按“版本未知”
// 处理，继续用既有缓存快照，不阻断请求。
const credentialVersionReadTimeout = 50 * time.Millisecond

// CredentialVersionReader 是账户 OAuth 凭据版本的只读端口（共享 Redis 投影，
// 写方为 gateway oauthmgmt / jobs oauthrefresh）。返回 map 覆盖全部入参
// accountID，缺失记 0。
type CredentialVersionReader interface {
	CredentialVersions(ctx context.Context, accountIDs []string) (map[string]int64, error)
}

// ---------------------------------------------------------------------------
// openai accounts for group cache (Node listCachedOpenAIAccountsForGroup /
// listCachedOpenAIAccountsForGroupAsync / listFreshOpenAIAccountsForGroupAsync /
// listRecoverableUnavailableOpenAIAccountsForGroupAsync)
// ---------------------------------------------------------------------------

// ListCachedOpenAIAccountsForGroup mirrors the sync read. The process cache is
// never shared: the snapshots carry decrypted upstream credentials. Stale
// entries (revalidateAtMs elapsed) serve as last-good while the background
// refresh runs — the same stale-while-revalidate contract as the runtime,
// group-access and inspection reads — so a jobs-side token rotation that only
// lands in the DB is picked up within the 60 s revalidate window instead of
// the full 2 min retain TTL (T91-4: retain tightened from the Node 10 min).
//
// B2（跨进程共享状态改造）：配置 CredentialVersions 后，命中缓存的每次读取
// 先批量比较账户凭据版本；版本变化立即同步重载该条目（只影响包含变化账户的
// 组条目），不必等 60s revalidate。版本读取失败时保留既有缓存路径。
func (s *Service) ListCachedOpenAIAccountsForGroup(ctx context.Context, groupID, systemAccountID string, opts CachedOpenAIAccountsForGroupOptions) ([]OpenAIAccountSecret, error) {
	cacheKey := gatewayOpenAIAccountsCacheKey(groupID, systemAccountID, opts.RequestedModel, opts.RequestedEndpointFamily)
	if cached, ok := s.accountsCache.get(cacheKey); ok {
		if s.credentialVersionsChanged(ctx, cached) {
			accounts, err := s.loadOpenAIAccountsForGroup(ctx, groupID, systemAccountID, cacheKey, opts)
			if err == nil {
				return accounts, nil
			}
			// 重载失败保留 last-good 快照（与既有 stale-while-revalidate 口径
			// 一致），不把一次版本检查变成请求失败。
			s.warnEvent("gateway_openai_accounts_credential_version_reload_failed", map[string]any{
				"groupID": groupID, "systemAccountID": systemAccountID, "err": err.Error(),
			}, "网关检测到 OAuth 凭据版本变化但重载失败，保留当前有界缓存快照")
		} else {
			if !isEntryFresh(cached.revalidateAtMs, s.nowMs()) {
				s.refreshOpenAIAccountsInBackground(groupID, systemAccountID, cacheKey, opts.RequestedModel, opts.RequestedEndpointFamily)
			}
			return s.cloneOpenAIAccountsWithCurrentConcurrency(ctx, cached.accounts)
		}
	}
	return s.loadOpenAIAccountsForGroup(ctx, groupID, systemAccountID, cacheKey, opts)
}

// loadOpenAIAccountsForGroup 直读 loader 并写入缓存条目（含凭据版本快照）。
func (s *Service) loadOpenAIAccountsForGroup(ctx context.Context, groupID, systemAccountID, cacheKey string, opts CachedOpenAIAccountsForGroupOptions) ([]OpenAIAccountSecret, error) {
	result, err := s.models.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, OpenAIAccountsForGroupOptions{
		RequestedModel:          opts.RequestedModel,
		RequestedEndpointFamily: opts.RequestedEndpointFamily,
	})
	if err != nil {
		return nil, err
	}
	accounts := cloneStaticOpenAIAccounts(result.Accounts)
	entry, entryErr := s.newOpenAIAccountsCacheEntry(ctx, accounts)
	if entryErr != nil {
		return nil, entryErr
	}
	s.accountsCache.set(cacheKey, entry, openAIAccountsRetainTTL)
	return s.cloneOpenAIAccountsWithCurrentConcurrency(ctx, result.Accounts)
}

// newOpenAIAccountsCacheEntry 在基础条目上记录各账户的凭据版本快照。读取失败
// 或未配置 reader 时版本为 nil，该条目退化为既有 60s 可见性窗口。
func (s *Service) newOpenAIAccountsCacheEntry(ctx context.Context, accounts []OpenAIAccountSecret) (openAIAccountsCacheEntry, error) {
	entry, err := newOpenAIAccountsCacheEntry(accounts, s.nowMs())
	if err != nil {
		return entry, err
	}
	entry.credentialVersions = s.readCredentialVersions(ctx, accounts)
	return entry, nil
}

// readCredentialVersions 批量读取账户凭据版本；未配置 reader、无账户或读失败
// 返回 nil（调用方按未知处理）。
func (s *Service) readCredentialVersions(ctx context.Context, accounts []OpenAIAccountSecret) map[string]int64 {
	if s.opts.CredentialVersions == nil || len(accounts) == 0 {
		return nil
	}
	ids := make([]string, 0, len(accounts))
	for index := range accounts {
		if id := strings.TrimSpace(accounts[index].ID); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	readCtx, cancel := context.WithTimeout(ctx, credentialVersionReadTimeout)
	defer cancel()
	versions, err := s.opts.CredentialVersions.CredentialVersions(readCtx, ids)
	if err != nil {
		s.warnEvent("gateway_openai_accounts_credential_version_read_failed", map[string]any{
			"err": err.Error(),
		}, "网关读取 OAuth 凭据版本失败，按既有 60s 缓存窗口回退")
		return nil
	}
	return versions
}

// credentialVersionsChanged 比较条目快照与当前版本；只有明确读到版本且与快照
// 不同才返回 true（版本未知不放行重载，避免 Redis 故障引发重载风暴）。
func (s *Service) credentialVersionsChanged(ctx context.Context, entry openAIAccountsCacheEntry) bool {
	if s.opts.CredentialVersions == nil || entry.credentialVersions == nil {
		return false
	}
	current := s.readCredentialVersions(ctx, entry.accounts)
	if current == nil {
		return false
	}
	for id, version := range entry.credentialVersions {
		if current[id] != version {
			return true
		}
	}
	return false
}

// ListCachedOpenAIAccountsForGroupAsync mirrors the async read with the stale
// fallback window.
func (s *Service) ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, opts CachedOpenAIAccountsForGroupOptions) ([]OpenAIAccountSecret, error) {
	s.syncInvalidationsBestEffort(ctx)
	if s.sharedGroup == nil {
		return s.ListCachedOpenAIAccountsForGroup(ctx, groupID, systemAccountID, opts)
	}
	// 共享模式（cacheDriver=redis）下进程缓存禁用（快照携带解密后的上游凭据，
	// 不得进共享层）：New 中 accountsCache.enabled 恒为 false，因此每次读都走
	// loader，loader 失败直接上抛，无 stale 回退。
	cacheKey := gatewayOpenAIAccountsCacheKey(groupID, systemAccountID, opts.RequestedModel, opts.RequestedEndpointFamily)
	return s.loadOpenAIAccountsForGroupAndPopulateCache(ctx, groupID, systemAccountID, cacheKey, opts.RequestedModel, opts.RequestedEndpointFamily)
}

// ListFreshOpenAIAccountsForGroupAsync mirrors listFreshOpenAIAccountsForGroupAsync:
// always loader-served, never cached.
func (s *Service) ListFreshOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, opts CachedOpenAIAccountsForGroupOptions) ([]OpenAIAccountSecret, error) {
	result, err := s.models.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, OpenAIAccountsForGroupOptions{
		RequestedModel:          opts.RequestedModel,
		RequestedEndpointFamily: opts.RequestedEndpointFamily,
	})
	if err != nil {
		return nil, err
	}
	return s.cloneOpenAIAccountsWithCurrentConcurrency(ctx, result.Accounts)
}

// ListFreshOpenAIAccountsForChatPinnedAsync 是 AI 问答 chat-pinned 派发的专用
// 直取读（AI 问答设计 §5.2 2026-10-10 修订）：loader 按（分组, 账户 ID）直取
// 该账户的运行时快照（含非 active/冷却/过期/禁调度账户，绕过共享候选窗的
// 可用性窗口），仅收敛到该账户单元素；分组禁用/不存在或账户不在该分组时返回
// 空切片。恒 fresh 直读不走缓存（pinned 快照的可用性语义与常规窗口缓存不同
// 键空间，不共享缓存条目）；返回前仅注入实时并发叠加，不做可用性剔除。
// /v1 常规读路径不经过本方法，语义不受影响。
func (s *Service) ListFreshOpenAIAccountsForChatPinnedAsync(ctx context.Context, groupID, systemAccountID, accountID string) ([]OpenAIAccountSecret, error) {
	result, err := s.models.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, OpenAIAccountsForGroupOptions{
		ChatPinnedAccountID: accountID,
	})
	if err != nil {
		return nil, err
	}
	return s.cloneOpenAIAccountsWithCurrentConcurrencyKeepAll(ctx, result.Accounts)
}

// ListRecoverableUnavailableOpenAIAccountsForGroupAsync mirrors
// listRecoverableUnavailableOpenAIAccountsForGroupAsync: loader with
// includeUnavailable then the local recoverability window filter.
func (s *Service) ListRecoverableUnavailableOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, opts CachedOpenAIAccountsForGroupOptions, windowMs *int64) ([]OpenAIAccountSecret, error) {
	result, err := s.models.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, OpenAIAccountsForGroupOptions{
		RequestedModel:          opts.RequestedModel,
		RequestedEndpointFamily: opts.RequestedEndpointFamily,
		IncludeUnavailable:      true,
	})
	if err != nil {
		return nil, err
	}
	accounts, err := recoverableUnavailableOpenAIAccounts(result.Accounts, windowMs, s.nowMs())
	if err != nil {
		return nil, err
	}
	out := make([]OpenAIAccountSecret, len(accounts))
	for i, account := range accounts {
		out[i] = CloneStaticOpenAIAccountSecret(account)
	}
	return out, nil
}

// recoverableUnavailableOpenAIAccounts mirrors
// recoverableUnavailableOpenAIAccountsFromResult.
func recoverableUnavailableOpenAIAccounts(accounts []OpenAIAccountSecret, windowMs *int64, nowMs int64) ([]OpenAIAccountSecret, error) {
	latestRecoverableAtMs := nowMs + normalizeRecoverableUnavailableWindowMs(windowMs)
	out := []OpenAIAccountSecret{}
	for i := range accounts {
		account := &accounts[i]
		cooldownUntilMs, err := accountRecoverableCooldownUntilMs(account)
		if err != nil {
			return nil, err
		}
		if cooldownUntilMs == nil || *cooldownUntilMs > latestRecoverableAtMs {
			continue
		}
		if account.Status == AccountStatusActive {
			if *cooldownUntilMs > nowMs {
				out = append(out, *account)
			}
			continue
		}
		if account.Status == AccountStatusTemporaryUnavailable || account.Status == AccountStatusRateLimited {
			out = append(out, *account)
		}
	}
	return out, nil
}

// normalizeRecoverableUnavailableWindowMs mirrors the Node helper (30s default).
func normalizeRecoverableUnavailableWindowMs(value *int64) int64 {
	if value == nil {
		return 30_000
	}
	if *value < 0 {
		return 0
	}
	return *value
}

// accountRecoverableCooldownUntilMs mirrors accountRecoverableCooldownUntilMs.
func accountRecoverableCooldownUntilMs(account *OpenAIAccountSecret) (*int64, error) {
	if account.CooldownUntil == nil {
		return nil, nil
	}
	ms, ok := rfc3339Millis(*account.CooldownUntil)
	if !ok {
		return nil, errors.New("账户 cooldownUntil 必须是带 Z 或数值 offset 的 RFC3339 时间：" + *account.CooldownUntil)
	}
	return &ms, nil
}

// newOpenAIAccountsCacheEntry mirrors openAIAccountsCacheEntry (malformed
// account instants surface the Node error instead of caching a poisoned TTL).
func newOpenAIAccountsCacheEntry(accounts []OpenAIAccountSecret, now int64) (openAIAccountsCacheEntry, error) {
	ttl, err := openAIAccountsCacheTTL(accounts, now)
	if err != nil {
		return openAIAccountsCacheEntry{}, err
	}
	return openAIAccountsCacheEntry{
		accounts:       accounts,
		revalidateAtMs: now + ttl.Milliseconds(),
	}, nil
}

// cloneStaticOpenAIAccounts maps cloneStaticOpenAIAccountSecret over a list.
func cloneStaticOpenAIAccounts(accounts []OpenAIAccountSecret) []OpenAIAccountSecret {
	out := make([]OpenAIAccountSecret, len(accounts))
	for i, account := range accounts {
		out[i] = CloneStaticOpenAIAccountSecret(account)
	}
	return out
}

// cloneOpenAIAccountsWithCurrentConcurrency mirrors the sync overlay.
func (s *Service) cloneOpenAIAccountsWithCurrentConcurrency(ctx context.Context, accounts []OpenAIAccountSecret) ([]OpenAIAccountSecret, error) {
	concurrency, err := s.models.LoadAccountCurrentConcurrencyByID(ctx, gatewayAccountConcurrencyAccountIDs(accounts))
	if err != nil {
		return nil, err
	}
	return s.applyConcurrencyOverlay(accounts, concurrency)
}

// applyConcurrencyOverlay mirrors cloneOpenAIAccountSecretForDispatch + the
// currentConcurrency stamp: runtime-unusable accounts drop out of dispatch.
func (s *Service) applyConcurrencyOverlay(accounts []OpenAIAccountSecret, concurrency map[string]int) ([]OpenAIAccountSecret, error) {
	nowMs := s.nowMs()
	// 预分配结果容量：账户结构体很大，从空 append 的倍增复制会额外产生约
	// 一倍的垃圾字节（50 账户隔离实测 122KB→65KB，读路径 131KB→73KB，见
	// docs/reports 热路径分配画像报告）。
	out := make([]OpenAIAccountSecret, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		usable, err := isOpenAIAccountRuntimeUsableAt(account, nowMs)
		if err != nil {
			return nil, err
		}
		if !usable {
			continue
		}
		cloned := CloneStaticOpenAIAccountSecret(*account)
		value := 0
		if observed, ok := concurrency[gatewayAccountConcurrencyAccountID(account)]; ok {
			value = observed
		}
		cloned.CurrentConcurrency = &value
		out = append(out, cloned)
	}
	return out, nil
}

// cloneOpenAIAccountsWithCurrentConcurrencyKeepAll 是 chat-pinned 直取专用的
// 克隆叠加（AI 问答设计 §5.2 2026-10-10 修订）：与 cloneOpenAIAccountsWith-
// CurrentConcurrency 相同的实时并发注入，但不做 isOpenAIAccountRuntimeUsableAt
// 可用性剔除——pinned 快照必须含禁用/冷却/过期账户。常规 /v1 读路径不经过
// 本方法（applyConcurrencyOverlay 的过滤语义保持现状）。
func (s *Service) cloneOpenAIAccountsWithCurrentConcurrencyKeepAll(ctx context.Context, accounts []OpenAIAccountSecret) ([]OpenAIAccountSecret, error) {
	concurrency, err := s.models.LoadAccountCurrentConcurrencyByID(ctx, gatewayAccountConcurrencyAccountIDs(accounts))
	if err != nil {
		return nil, err
	}
	out := make([]OpenAIAccountSecret, 0, len(accounts))
	for i := range accounts {
		cloned := CloneStaticOpenAIAccountSecret(accounts[i])
		value := 0
		if observed, ok := concurrency[gatewayAccountConcurrencyAccountID(&accounts[i])]; ok {
			value = observed
		}
		cloned.CurrentConcurrency = &value
		out = append(out, cloned)
	}
	return out, nil
}

// gatewayAccountConcurrencyAccountID mirrors gatewayAccountConcurrencyAccountId:
// the credential source account wins over the physical id.
func gatewayAccountConcurrencyAccountID(account *OpenAIAccountSecret) string {
	if account.CredentialSourceAccountID != nil && trimSpace(*account.CredentialSourceAccountID) != "" {
		return trimSpace(*account.CredentialSourceAccountID)
	}
	return account.ID
}

// gatewayAccountConcurrencyAccountIDs mirrors gatewayAccountConcurrencyAccountIds.
func gatewayAccountConcurrencyAccountIDs(accounts []OpenAIAccountSecret) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		id := gatewayAccountConcurrencyAccountID(account)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func trimSpace(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t' || value[start] == '\n' || value[start] == '\r') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t' || value[end-1] == '\n' || value[end-1] == '\r') {
		end--
	}
	return value[start:end]
}

// loadOpenAIAccountsForGroupAndPopulateCache mirrors the Node loader with the
// generation guard.
func (s *Service) loadOpenAIAccountsForGroupAndPopulateCache(ctx context.Context, groupID, systemAccountID, cacheKey, requestedModel, requestedEndpointFamily string) ([]OpenAIAccountSecret, error) {
	generation := s.currentRuntimeGeneration()
	result, err := s.models.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, OpenAIAccountsForGroupOptions{
		RequestedModel:          requestedModel,
		RequestedEndpointFamily: requestedEndpointFamily,
	})
	if err != nil {
		return nil, err
	}
	accounts := cloneStaticOpenAIAccounts(result.Accounts)
	if s.isRuntimeGenerationCurrent(generation) {
		entry, entryErr := s.newOpenAIAccountsCacheEntry(ctx, accounts)
		if entryErr != nil {
			return nil, entryErr
		}
		s.accountsCache.set(cacheKey, entry, openAIAccountsRetainTTL)
	}
	return s.cloneOpenAIAccountsWithCurrentConcurrency(ctx, result.Accounts)
}

// refreshOpenAIAccountsInBackground mirrors the Node stale-fallback background
// refresh with per-key dedupe (same primitive as the group-access and
// inspection refreshes): one in-flight loader call per cache key, guarded by
// the runtime generation so an invalidation mid-flight cannot repopulate.
// Failure keeps the bounded last-good snapshot and warns once per occurrence.
func (s *Service) refreshOpenAIAccountsInBackground(groupID, systemAccountID, cacheKey, requestedModel, requestedEndpointFamily string) {
	s.mu.Lock()
	if _, pending := s.pendingAccountRefreshes[cacheKey]; pending {
		s.mu.Unlock()
		return
	}
	// s.mu 已持有：直接读世代字段，禁止重入 currentRuntimeGeneration。
	generation := s.runtimeGeneration
	call := newRefreshCall()
	s.pendingAccountRefreshes[cacheKey] = call
	s.mu.Unlock()

	go func() {
		defer safego.Recover("gatewayruntimecache.accounts.background_refresh")
		defer func() {
			s.mu.Lock()
			delete(s.pendingAccountRefreshes, cacheKey)
			s.mu.Unlock()
			call.finish()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), GatewayRuntimeLoadTimeout)
		defer cancel()
		result, err := s.models.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, OpenAIAccountsForGroupOptions{
			RequestedModel:          requestedModel,
			RequestedEndpointFamily: requestedEndpointFamily,
		})
		if err != nil {
			s.warnEvent("gateway_openai_accounts_stale_refresh_failed", map[string]any{
				"groupID": groupID, "systemAccountID": systemAccountID, "err": err.Error(),
			}, "网关 OpenAI 账号后台刷新失败，保留当前有界缓存快照")
			return
		}
		if !s.isRuntimeGenerationCurrent(generation) {
			return
		}
		entry, entryErr := s.newOpenAIAccountsCacheEntry(ctx, cloneStaticOpenAIAccounts(result.Accounts))
		if entryErr != nil {
			s.warnEvent("gateway_openai_accounts_stale_refresh_failed", map[string]any{
				"groupID": groupID, "systemAccountID": systemAccountID, "err": entryErr.Error(),
			}, "网关 OpenAI 账号后台刷新构建缓存条目失败，保留当前有界缓存快照")
			return
		}
		s.accountsCache.set(cacheKey, entry, openAIAccountsRetainTTL)
	}()
}

// ---------------------------------------------------------------------------
// singleflight refresh call primitive
// ---------------------------------------------------------------------------
