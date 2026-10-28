package oauthrefresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Keepalive windows mirror the dispatch-preparation lead constants: tokens
// within the window of expiry (or without one) are refreshed ahead of demand.
const (
	AnthropicKeepaliveLead = 60 * time.Second
	GeminiKeepaliveLead    = 60 * time.Second
	GrokKeepaliveLead      = 5 * time.Minute
	// KeepalivePersistAttempts mirrors anthropicOAuthPersistAttempts and its
	// per-provider siblings: the CAS merge writer retries revision conflicts.
	KeepalivePersistAttempts = 3
	// DefaultKeepaliveBatch mirrors the OpenAI refresh batch default scale.
	DefaultKeepaliveBatch = 20
	// KeepaliveFailureBackoff mirrors the OpenAI refresh family's default
	// retry backoff: both failure classes (local configuration and untrusted
	// upstream/runtime) record the same 300s backoff in the failure state
	// store; only local-configuration failures escalate to the terminal
	// stopped state.
	KeepaliveFailureBackoff = DefaultRefreshBackoffSeconds * time.Second
)

// KeepaliveResult mirrors the refresh result shape for the three-vendor job.
type KeepaliveResult struct {
	Provider      string
	Scanned       int
	Due           int
	Refreshed     int
	Failed        int
	SkippedLocked int
	SkippedFresh  int
	// SkippedBackoff counts accounts held out of the batch by a live failure
	// backoff (OpenAI refresh parity).
	SkippedBackoff int
}

// KeepaliveJob runs the anthropic/gemini/grok OAuth keepalive refresh family.
// The refresh trigger/window semantics come from the dispatch preparation
// (oauth-dispatch-preparation.ts); the failure bookkeeping mirrors the OpenAI
// refresh family exactly (doc.go): both failure classes record a 300s backoff,
// three applied local-configuration failures stop an active account through
// the shared oauth_token_refresh_local_configuration_invalid code, a
// successful refresh clears the recorded state and restores a managed-code
// stop, and live backoffs filter the batch (unreadable states skip the round).
type KeepaliveJob struct {
	store     *Store
	exchanger TokenExchanger
	failures  FailureStateStore
	locks     *accountLocks
	clock     Clock
	logger    *slog.Logger
	// updateCredentials overrides the CAS merge writer (mirrors the Node
	// setXxxForTest injection seams; nil uses the store).
	updateCredentials func(ctx context.Context, accountID string, credentials map[string]any, expectedConfigRevision int64) (*RotationAccount, error)
}

// NewKeepaliveJob wires the job. failures defaults to the in-process store
// (memory version); redis-state deployments inject the family-isolated Redis
// store through WithKeepaliveFailureStateStore.
func NewKeepaliveJob(store *Store, exchanger TokenExchanger, opts ...func(*KeepaliveJob)) *KeepaliveJob {
	job := &KeepaliveJob{
		store:     store,
		exchanger: exchanger,
		failures:  NewMemoryFailureStateStore(),
		locks:     newAccountLocks(),
		clock:     SystemClock(),
		logger:    slog.Default(),
	}
	for _, opt := range opts {
		opt(job)
	}
	return job
}

// WithClock overrides the clock (tests).
func WithKeepaliveClock(clock Clock) func(*KeepaliveJob) {
	return func(job *KeepaliveJob) { job.clock = clock }
}

// WithKeepaliveLogger overrides the logger.
func WithKeepaliveLogger(logger *slog.Logger) func(*KeepaliveJob) {
	return func(job *KeepaliveJob) { job.logger = logger }
}

// WithKeepaliveFailureStateStore overrides the failure state store (the
// Redis-backed store under the keepalive family key namespace in redis-state
// deployments). The default stays the process-local memory store.
func WithKeepaliveFailureStateStore(store FailureStateStore) func(*KeepaliveJob) {
	return func(job *KeepaliveJob) { job.failures = store }
}

// KeepalivePlan describes one provider's keepalive query parameters.
// 供应商解耦后（设计 §9.11）profile 维度退役：vendor+type 在 §1.1 registry
// 已唯一绑定连接预设，不再需要 profile ID 门。
type KeepalivePlan struct {
	Provider    string
	AccountType string
	Lead        time.Duration
}

// KeepalivePlans returns the three-provider plans.
func KeepalivePlans() []KeepalivePlan {
	return []KeepalivePlan{
		{Provider: ProviderAnthropic, AccountType: AccountTypeOAuth, Lead: AnthropicKeepaliveLead},
		{Provider: ProviderGemini, AccountType: AccountTypeGoogleOAuth, Lead: GeminiKeepaliveLead},
		{Provider: ProviderXAI, AccountType: AccountTypeOAuth, Lead: GrokKeepaliveLead},
	}
}

func (j *KeepaliveJob) now() time.Time { return j.clock.Now() }

// RunOnce refreshes every due account of the provider.
func (j *KeepaliveJob) RunOnce(ctx context.Context, plan KeepalivePlan, limit int) (KeepaliveResult, error) {
	result := KeepaliveResult{Provider: plan.Provider}
	if limit <= 0 {
		limit = DefaultKeepaliveBatch
	}
	now := j.now()
	j.failures.CleanupBackoff(now.UnixMilli())
	candidates, err := j.store.ListDueKeepaliveAccounts(ctx, plan.Provider, plan.AccountType, plan.Lead, limit, now)
	if err != nil {
		return result, err
	}
	result.Scanned = len(candidates)
	for _, candidate := range candidates {
		account := candidate.Account
		if account == nil {
			continue
		}
		// Batch-level backoff filter (OpenAI selectBatchCandidates parity):
		// an unreadable failure state must not read as "no backoff" (a Redis
		// outage would otherwise hammer the upstream), so the account skips
		// this round; a live backoff counts SkippedBackoff and never reaches
		// the token endpoint.
		observedFailure, readErr := j.failures.Read(ctx, account.ID, j.now().UnixMilli(), account.ConfigRevision)
		if readErr != nil {
			j.logger.Warn("供应商 OAuth 保活失败状态读取失败，本轮跳过该账户",
				"event", "oauth_keepalive_failure_state_read_failed",
				"error", readErr,
				"accountId", account.ID)
			continue
		}
		if observedFailure != nil && observedFailure.BackoffUntil > j.now().UnixMilli() {
			result.SkippedBackoff++
			continue
		}
		result.Due++
		refreshed := false
		lockErr := j.locks.TryLock(account.ID, func() error {
			flag, err := j.refreshOne(ctx, plan, account, now)
			refreshed = flag
			return err
		})
		if lockErr != nil {
			var busy *LockBusyError
			if errors.As(lockErr, &busy) {
				result.SkippedLocked++
				continue
			}
			result.Failed++
			j.recordKeepaliveFailure(ctx, plan, account, lockErr)
			continue
		}
		// Success: clear the observed failure state and restore a previously
		// stopped account (OpenAI restoreIfRecovered parity).
		if observedFailure != nil {
			if clearErr := j.failures.Clear(ctx, account.ID, *observedFailure); clearErr != nil {
				j.logger.Warn("OAuth 保活刷新成功后清理失败退避状态失败，退避残留至到期自然失效",
					"event", "oauth_keepalive_backoff_clear_failed",
					"accountId", account.ID, "error", clearErr)
			}
		}
		j.restoreIfRecovered(ctx, account)
		if !refreshed {
			result.SkippedFresh++
			continue
		}
		result.Refreshed++
	}
	return result, nil
}

// recordKeepaliveFailure mirrors the OpenAI processCandidate failure branch:
// classify (local configuration vs untrusted upstream/runtime), record the
// failure state with the default 300s backoff, and write the terminal stopped
// state after 3 applied local-configuration failures on an active account.
// Upstream failures never mark an account terminal.
func (j *KeepaliveJob) recordKeepaliveFailure(ctx context.Context, plan KeepalivePlan, account *RotationAccount, refreshErr error) {
	failureKind := FailureKindUntrustedUpstream
	if IsLocalConfigurationError(refreshErr) {
		failureKind = FailureKindLocalConfiguration
	}
	failureState, recordErr := j.failures.Record(ctx, account.ID, j.now().Add(KeepaliveFailureBackoff).UnixMilli(), failureKind, account.ConfigRevision)
	if recordErr != nil {
		j.logger.Error("供应商 OAuth 保活失败状态写入失败", "error", recordErr, "accountId", account.ID)
		return
	}
	logAttrs := append([]any{
		"event", "oauth_keepalive_refresh_account_failed",
		"provider", plan.Provider,
		"accountId", account.ID,
		"accountName", account.Name,
		"failureCount", failureState.Count,
		"localConfigurationFailureCount", failureState.LocalConfigurationCount,
		"failureKind", string(failureKind),
		"failureStateApplied", failureState.Applied,
	}, tokenExchangeFailureLogAttrs(refreshErr, plan.Provider, "oauth_keepalive_refresh", map[string]any{
		"provider":  plan.Provider,
		"accountId": account.ID,
	})...)
	j.logger.Warn("供应商 OAuth 保活刷新失败", logAttrs...)

	if failureState.Applied &&
		failureState.LocalConfigurationCount >= openAIOAuthRefreshFailureThreshold &&
		account.Status == "active" {
		updated, err := j.store.MarkAccountFailureState(ctx, account.ID,
			OpenAIOAuthTokenRefreshLocalConfigurationInvalidCode,
			keepaliveLocalConfigurationStoppedMessage(plan.Provider, int(failureState.LocalConfigurationCount), sanitizedErrorMessage(refreshErr)),
			failureState.ConfigRevision, "active")
		if err != nil {
			j.logger.Error("供应商 OAuth 保活本地配置异常标记失败", "error", err, "accountId", account.ID)
			return
		}
		if updated {
			if clearErr := j.failures.Clear(ctx, account.ID, failureState); clearErr != nil {
				j.logger.Warn("OAuth 保活本地配置异常停用后清理失败退避状态失败",
					"event", "oauth_keepalive_backoff_clear_failed",
					"accountId", account.ID, "error", clearErr)
			}
			j.logger.Warn("供应商 OAuth 保活连续本地配置失败，已停止该账户自动刷新",
				"event", "oauth_keepalive_local_configuration_invalid",
				"provider", plan.Provider,
				"accountId", account.ID)
		}
	}
}

// restoreIfRecovered mirrors the OpenAI restoreIfRecovered: a successful
// keepalive refresh restores an account previously stopped by a managed
// refresh error code (re-authorization bumps the revision so the next batch
// refresh confirms recovery and clears the stop).
func (j *KeepaliveJob) restoreIfRecovered(ctx context.Context, account *RotationAccount) {
	if account.Status != "error" || !isManagedOpenAIOAuthRefreshErrorCode(account.LastErrorCode) {
		return
	}
	changed, status, err := j.store.ClearAccountFailureState(ctx, account.ID, ManagedRefreshErrorCodes)
	if err != nil {
		j.logger.Error("供应商 OAuth 保活刷新失败账户恢复失败", "error", err, "accountId", account.ID)
		return
	}
	if changed && status == "active" {
		j.logger.Info("供应商 OAuth 保活刷新成功，已自动恢复此前刷新失败异常",
			"event", "oauth_keepalive_refresh_account_restored",
			"accountId", account.ID,
			"accountName", account.Name)
	}
}

// refreshOne mirrors refreshAndPersistXxxOAuthAccount: re-read the source,
// re-check the window, refresh, merge (base_url preserved) and CAS-persist
// with at most KeepalivePersistAttempts attempts. It returns false when the
// account turned out to be still fresh (no refresh needed).
func (j *KeepaliveJob) refreshOne(ctx context.Context, plan KeepalivePlan, account *RotationAccount, now time.Time) (bool, error) {
	source, err := j.store.FindRotationAccount(ctx, account.ID)
	if err != nil {
		return false, err
	}
	if source == nil {
		return false, &LocalConfigurationError{Message: providerSourceMissingMessage(plan.Provider), ExpectedConfigRevision: account.ConfigRevision}
	}
	if !isRefreshableKeepaliveAccount(plan, source) {
		return false, &LocalConfigurationError{Message: providerSourceMissingMessage(plan.Provider), ExpectedConfigRevision: account.ConfigRevision}
	}
	if !shouldKeepaliveRefresh(source.Credentials, now, plan.Lead) {
		return false, nil
	}
	refreshToken := stringCredential(source.Credentials, "refresh_token")
	if refreshToken == "" {
		return false, &LocalConfigurationError{Message: providerMissingRefreshTokenMessage(plan.Provider), ExpectedConfigRevision: source.ConfigRevision}
	}
	// Resolve the account-bound proxy before any token request. A resolution
	// failure surfaces as this account's refresh failure (local configuration
	// kind); it never falls back to a direct dial.
	proxyURL, proxyErr := j.store.proxyProfileRequestURL(ctx, source.ProxyProfileID)
	if proxyErr != nil {
		return false, proxyErr
	}

	var tokenCredentials map[string]any
	switch plan.Provider {
	case ProviderAnthropic:
		info, refreshErr := RefreshAnthropicToken(ctx, j.exchanger, refreshToken, stringCredential(source.Credentials, "client_id"), j.now(), proxyURL)
		if refreshErr != nil {
			return false, refreshErr
		}
		tokenCredentials = BuildAnthropicOAuthCredentials(info, refreshToken)
	case ProviderGemini:
		fallback := geminiFallbackFromCredentials(source.Credentials)
		fallback.ProxyURL = proxyURL
		info, refreshErr := RefreshGeminiToken(ctx, j.exchanger, refreshToken, fallback, j.now())
		if refreshErr != nil {
			return false, refreshErr
		}
		tokenCredentials = BuildGeminiOAuthCredentials(info, &fallback)
	case ProviderXAI:
		info, refreshErr := RefreshGrokToken(ctx, j.exchanger, refreshToken, stringCredential(source.Credentials, "client_id"), j.now(), proxyURL)
		if refreshErr != nil {
			return false, refreshErr
		}
		tokenCredentials = BuildGrokOAuthCredentials(info, refreshToken)
	default:
		return false, fmt.Errorf("不支持的保活供应商：%s", plan.Provider)
	}

	var lastConflict error
	for attempt := 0; attempt < KeepalivePersistAttempts; attempt++ {
		// {...current, ...token} with the stored base_url preserved (the
		// anthropic/gemini/grok merge keeps the operator-managed base URL).
		merged := mergeCredentials(source.Credentials, tokenCredentials)
		if baseURL := stringCredential(source.Credentials, "base_url"); baseURL != "" {
			merged["base_url"] = baseURL
		}
		updated, persistErr := j.persistCredentials(ctx, source.ID, merged, source.ConfigRevision)
		if persistErr == nil {
			if updated == nil {
				return false, errors.New(providerSourceMissingMessage(plan.Provider))
			}
			return true, nil
		}
		var conflict *RevisionConflictError
		if !errors.As(persistErr, &conflict) {
			return false, persistErr
		}
		lastConflict = persistErr
		latest, loadErr := j.store.FindRotationAccount(ctx, source.ID)
		if loadErr != nil {
			return false, loadErr
		}
		if latest == nil || !isRefreshableKeepaliveAccount(plan, latest) {
			return false, persistErr
		}
		latestRefreshToken := stringCredential(latest.Credentials, "refresh_token")
		if latestRefreshToken != refreshToken {
			if !shouldKeepaliveRefresh(latest.Credentials, j.now(), plan.Lead) {
				// Another writer already refreshed the account with a usable
				// token; keep it (Node dispatch-preparation parity).
				return false, nil
			}
		}
		source = latest
	}
	if lastConflict != nil {
		return false, lastConflict
	}
	return false, errors.New("OAuth 凭据并发写回失败")
}

// persistCredentials routes the CAS merge writer through the test seam when
// installed.
func (j *KeepaliveJob) persistCredentials(ctx context.Context, accountID string, credentials map[string]any, expectedConfigRevision int64) (*RotationAccount, error) {
	if j.updateCredentials != nil {
		return j.updateCredentials(ctx, accountID, credentials, expectedConfigRevision)
	}
	return j.store.UpdateAccountCredentials(ctx, accountID, credentials, expectedConfigRevision)
}

// shouldKeepaliveRefresh mirrors shouldRefreshXxxOAuthCredentials: missing
// access token, missing expires_at, unparsable expires_at or inside the lead
// window. Unparsable timestamps read as due so a scheduled sweeper cannot
// wedge on one malformed row; the row surfaces through the failure log.
func shouldKeepaliveRefresh(credentials map[string]any, now time.Time, lead time.Duration) bool {
	if stringCredential(credentials, "access_token") == "" {
		return true
	}
	expiresAtText := credentialExpiresAt(credentials)
	if expiresAtText == "" {
		return true
	}
	expiresAtMs, ok := rfc3339Millis(expiresAtText)
	if !ok {
		return true
	}
	return expiresAtMs-now.UnixMilli() <= lead.Milliseconds()
}

func isRefreshableKeepaliveAccount(plan KeepalivePlan, account *RotationAccount) bool {
	if account.ProviderCode != plan.Provider || account.Type != plan.AccountType {
		return false
	}
	return true
}

func geminiFallbackFromCredentials(credentials map[string]any) GeminiCredentialFallback {
	return GeminiCredentialFallback{
		RefreshToken:   stringCredential(credentials, "refresh_token"),
		OAuthType:      stringCredential(credentials, "oauth_type"),
		ClientID:       stringCredential(credentials, "client_id"),
		ClientSecret:   stringCredential(credentials, "client_secret"),
		ProjectID:      stringCredential(credentials, "project_id"),
		TierID:         stringCredential(credentials, "tier_id"),
		QuotaProjectID: stringCredential(credentials, "quota_project_id"),
		BaseURL:        stringCredential(credentials, "base_url"),
		Scope:          stringCredential(credentials, "scope"),
	}
}

func providerSourceMissingMessage(provider string) string {
	switch provider {
	case ProviderAnthropic:
		return "Anthropic OAuth 凭据源账户不存在或类型不匹配"
	case ProviderGemini:
		return "Gemini OAuth 凭据源账户不存在或类型不匹配"
	}
	return "Grok OAuth 凭据源账户不存在或类型不匹配"
}

func providerMissingRefreshTokenMessage(provider string) string {
	switch provider {
	case ProviderAnthropic:
		return "Anthropic OAuth 账户缺少 Refresh Token"
	case ProviderGemini:
		return "Gemini OAuth 账户缺少 Refresh Token"
	}
	return "Grok OAuth 账户缺少 Refresh Token"
}

func providerDisplayName(provider string) string {
	switch provider {
	case ProviderAnthropic:
		return "Anthropic"
	case ProviderGemini:
		return "Gemini"
	}
	return "Grok"
}

// keepaliveLocalConfigurationStoppedMessage mirrors the OpenAI terminal reason
// copy with the provider display name (the terminal error code is the shared
// oauth_token_refresh_local_configuration_invalid; joined and truncated to
// 1000 characters).
func keepaliveLocalConfigurationStoppedMessage(provider string, failureCount int, lastError string) string {
	message := strings.Join([]string{
		fmt.Sprintf("%s OAuth 访问令牌连续 %d 次因本地配置错误无法启动刷新，已停止自动刷新。", providerDisplayName(provider), failureCount),
		"该结论只来自本地可验证的凭据、代理配置解析或解密失败，不使用上游 HTTP 状态、错误码或响应正文推断账户状态。",
		"请在账户页检查并修正 OAuth 凭据或代理配置，然后重新检查账户。",
		fmt.Sprintf("最后本地错误：%s", lastError),
	}, " ")
	if len([]rune(message)) > 1000 {
		message = truncateRunes(message, 1000)
	}
	return message
}
