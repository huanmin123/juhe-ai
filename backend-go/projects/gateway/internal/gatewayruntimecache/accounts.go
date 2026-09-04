package gatewayruntimecache

import (
	"context"
	"errors"
)

// ---------------------------------------------------------------------------
// openai accounts for group cache (Node listCachedOpenAIAccountsForGroup /
// listCachedOpenAIAccountsForGroupAsync / listFreshOpenAIAccountsForGroupAsync /
// listRecoverableUnavailableOpenAIAccountsForGroupAsync)
// ---------------------------------------------------------------------------

// ListCachedOpenAIAccountsForGroup mirrors the sync read. The process cache is
// never shared: the snapshots carry decrypted upstream credentials.
func (s *Service) ListCachedOpenAIAccountsForGroup(ctx context.Context, groupID, systemAccountID string, opts CachedOpenAIAccountsForGroupOptions) ([]OpenAIAccountSecret, error) {
	cacheKey := gatewayOpenAIAccountsCacheKey(groupID, systemAccountID, opts.RequestedModel, opts.RequestedEndpointFamily)
	if cached, ok := s.accountsCache.get(cacheKey); ok {
		return s.cloneOpenAIAccountsWithCurrentConcurrency(ctx, cached.accounts)
	}
	result, err := s.models.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, OpenAIAccountsForGroupOptions{
		RequestedModel:          opts.RequestedModel,
		RequestedEndpointFamily: opts.RequestedEndpointFamily,
	})
	if err != nil {
		return nil, err
	}
	accounts := cloneStaticOpenAIAccounts(result.Accounts)
	entry, entryErr := newOpenAIAccountsCacheEntry(accounts, s.nowMs())
	if entryErr != nil {
		return nil, entryErr
	}
	s.accountsCache.set(cacheKey, entry, openAIAccountsRetainTTL)
	return s.cloneOpenAIAccountsWithCurrentConcurrency(ctx, result.Accounts)
}

// ListCachedOpenAIAccountsForGroupAsync mirrors the async read with the stale
// fallback window.
func (s *Service) ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, opts CachedOpenAIAccountsForGroupOptions) ([]OpenAIAccountSecret, error) {
	s.syncInvalidationsBestEffort(ctx)
	if s.sharedGroup == nil {
		return s.ListCachedOpenAIAccountsForGroup(ctx, groupID, systemAccountID, opts)
	}
	cacheKey := gatewayOpenAIAccountsCacheKey(groupID, systemAccountID, opts.RequestedModel, opts.RequestedEndpointFamily)
	cached, ok := s.accountsCache.get(cacheKey)
	if ok {
		if !isEntryFresh(cached.revalidateAtMs, s.nowMs()) {
			s.refreshOpenAIAccountsForGroupInBackground(groupID, systemAccountID, cacheKey, opts.RequestedModel, opts.RequestedEndpointFamily)
		}
		return s.cloneOpenAIAccountsWithCurrentConcurrency(ctx, cached.accounts)
	}
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
	out := []OpenAIAccountSecret{}
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
	out := []string{}
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
		entry, entryErr := newOpenAIAccountsCacheEntry(accounts, s.nowMs())
		if entryErr != nil {
			return nil, entryErr
		}
		s.accountsCache.set(cacheKey, entry, openAIAccountsRetainTTL)
	}
	return s.cloneOpenAIAccountsWithCurrentConcurrency(ctx, result.Accounts)
}

// refreshOpenAIAccountsForGroupInBackground mirrors
// refreshOpenAIAccountsForGroupInBackground.
func (s *Service) refreshOpenAIAccountsForGroupInBackground(groupID, systemAccountID, cacheKey, requestedModel, requestedEndpointFamily string) {
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
			s.warnEvent("gateway_accounts_stale_refresh_failed", map[string]any{
				"groupId": groupID, "systemAccountId": systemAccountID, "err": err.Error(),
			}, "网关候选账号后台刷新失败，保留当前有界缓存快照")
			return
		}
		if !s.isRuntimeGenerationCurrent(generation) {
			return
		}
		entry, entryErr := newOpenAIAccountsCacheEntry(cloneStaticOpenAIAccounts(result.Accounts), s.nowMs())
		if entryErr != nil {
			s.warnEvent("gateway_accounts_stale_refresh_failed", map[string]any{
				"groupId": groupID, "systemAccountId": systemAccountID, "err": entryErr.Error(),
			}, "网关候选账号后台刷新失败，保留当前有界缓存快照")
			return
		}
		s.accountsCache.set(cacheKey, entry, openAIAccountsRetainTTL)
	}()
}

// ---------------------------------------------------------------------------
// singleflight refresh call primitive
// ---------------------------------------------------------------------------
