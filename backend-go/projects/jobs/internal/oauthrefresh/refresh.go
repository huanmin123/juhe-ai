package oauthrefresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Managed error codes and copy mirror openai-oauth-access-token-refresh.service.ts
// byte-for-byte (contract: failure terminal states and Chinese copy stay
// identical).
const (
	OpenAIOAuthTokenRefreshFailedErrorCode               = "oauth_token_refresh_failed"
	OpenAIOAuthTokenRefreshLocalConfigurationInvalidCode = "oauth_token_refresh_local_configuration_invalid"
	openAIOAuthRefreshFailureThreshold                   = 3
	openAIOAuthRefreshStartAdmissionBudget               = 55 * time.Second
)

// ManagedRefreshErrorCodes are the failure codes the refresh family owns and
// auto-restores after a successful refresh.
var ManagedRefreshErrorCodes = []string{
	OpenAIOAuthTokenRefreshFailedErrorCode,
	OpenAIOAuthTokenRefreshLocalConfigurationInvalidCode,
}

// Refresh option bounds mirror settingsInteger/optionInteger clamps.
const (
	DefaultRefreshLeadSeconds    = 300
	DefaultRefreshBatchSize      = 20
	DefaultRefreshBackoffSeconds = 300
	MinRefreshLeadSeconds        = 60
	MaxRefreshLeadSeconds        = 86400
	MinRefreshBatchSize          = 1
	MaxRefreshBatchSize          = 200
	MinRefreshBackoffSeconds     = 0
	MaxRefreshBackoffSeconds     = 86400
	MinAdmissionBudgetMs         = 1
	MaxAdmissionBudgetMs         = 300_000
	// DefaultRefreshConcurrency mirrors runtimeConfig.concurrency.globalMax
	// (default 5000): the batch is capped at 200 anyway, so the default lets
	// the whole selected batch run in parallel like the Node worker pool.
	DefaultRefreshConcurrency = 5000
)

// LocalConfigurationError mirrors OpenAIOAuthRefreshLocalConfigurationError:
// only locally verifiable failures (missing refresh token, unusable proxy,
// undecryptable credentials) count toward the terminal stopped state.
type LocalConfigurationError struct {
	Message                string
	ExpectedConfigRevision int64
}

func (e *LocalConfigurationError) Error() string { return e.Message }

// IsLocalConfigurationError reports whether err is a local configuration
// failure.
func IsLocalConfigurationError(err error) bool {
	var local *LocalConfigurationError
	return errors.As(err, &local)
}

// LockBusyError mirrors ProviderOAuthRefreshLockBusyError: the per-account
// refresh lock is held, the batch job skips the account this round.
type LockBusyError struct{ Message string }

func (e *LockBusyError) Error() string { return e.Message }

// accountLocks is the in-process per-account refresh lock registry (the jobs
// equivalent of runWithProviderOAuthRefreshLock; failIfLocked mirrors the
// batch lockMode 'skip').
type accountLocks struct {
	mu   sync.Mutex
	held map[string]*sync.Mutex
}

func newAccountLocks() *accountLocks {
	return &accountLocks{held: map[string]*sync.Mutex{}}
}

// TryLock runs task under the account lock; when the lock is taken it fails
// with LockBusyError without waiting.
func (l *accountLocks) TryLock(accountID string, task func() error) error {
	l.mu.Lock()
	mutex, ok := l.held[accountID]
	if !ok {
		mutex = &sync.Mutex{}
		l.held[accountID] = mutex
	}
	l.mu.Unlock()
	if !mutex.TryLock() {
		return &LockBusyError{Message: "供应商 OAuth 刷新锁被占用"}
	}
	defer mutex.Unlock()
	return task()
}

// Lock runs task under the account lock, waiting when taken (the manual
// refresh path).
func (l *accountLocks) Lock(accountID string, task func() error) error {
	l.mu.Lock()
	mutex, ok := l.held[accountID]
	if !ok {
		mutex = &sync.Mutex{}
		l.held[accountID] = mutex
	}
	l.mu.Unlock()
	mutex.Lock()
	defer mutex.Unlock()
	return task()
}

// SettingsReader mirrors the settings lookups (settingsInteger): absent keys
// fall back to the schema defaults.
type SettingsReader interface {
	SettingInt(ctx context.Context, key string) (int64, bool, error)
}

// MapSettingsReader adapts a plain map (tests, config preload).
type MapSettingsReader map[string]int64

// SettingInt implements SettingsReader.
func (m MapSettingsReader) SettingInt(_ context.Context, key string) (int64, bool, error) {
	value, ok := m[key]
	return value, ok, nil
}

// RefreshOptions mirrors OpenAIOAuthAccessTokenRefreshOptions.
type RefreshOptions struct {
	LeadSeconds            *int
	BatchSize              *int
	RetryBackoffSeconds    *int
	StartAdmissionBudgetMs *int
	AccountIDs             []string
	Concurrency            int
}

// RefreshResult mirrors OpenAIOAuthAccessTokenRefreshResult.
type RefreshResult struct {
	Scanned        int
	Due            int
	Refreshed      int
	Failed         int
	Exceptioned    int
	Cooldowned     int
	SkippedBackoff int
	Started        int
	SkippedLocked  int
	DeferredBudget int
}

// RefreshJob runs the openai-oauth access token refresh family.
type RefreshJob struct {
	store     *Store
	exchanger TokenExchanger
	failures  FailureStateStore
	settings  SettingsReader
	locks     *accountLocks
	clock     Clock
	logger    *slog.Logger
}

// NewRefreshJob wires the job. failures defaults to the in-process store;
// settings defaults to the schema defaults.
func NewRefreshJob(store *Store, exchanger TokenExchanger, opts ...func(*RefreshJob)) *RefreshJob {
	job := &RefreshJob{
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

// WithFailureStateStore overrides the failure state store (Redis in
// redis-state deployments).
func WithFailureStateStore(store FailureStateStore) func(*RefreshJob) {
	return func(job *RefreshJob) { job.failures = store }
}

// WithSettings overrides the settings reader.
func WithSettings(reader SettingsReader) func(*RefreshJob) {
	return func(job *RefreshJob) { job.settings = reader }
}

// WithClock overrides the clock (tests).
func WithClock(clock Clock) func(*RefreshJob) {
	return func(job *RefreshJob) { job.clock = clock }
}

// WithLogger overrides the logger.
func WithLogger(logger *slog.Logger) func(*RefreshJob) {
	return func(job *RefreshJob) { job.logger = logger }
}

func (j *RefreshJob) now() time.Time { return j.clock.Now() }

// optionInt mirrors optionInteger: the value must sit inside [min,max].
func optionInt(value int, label string, min, max int) (int, error) {
	if value < min || value > max {
		return 0, fmt.Errorf("%s 必须在 %d 到 %d 之间", label, min, max)
	}
	return value, nil
}

func (j *RefreshJob) settingsInt(ctx context.Context, key string, pointer *int, fallback, min, max int) (int, error) {
	if pointer != nil {
		return optionInt(*pointer, key, min, max)
	}
	if j.settings != nil {
		value, ok, err := j.settings.SettingInt(ctx, key)
		if err != nil {
			return 0, err
		}
		if ok {
			return optionInt(int(value), "系统设置 "+key, min, max)
		}
	}
	return fallback, nil
}

// RunOnce executes one refresh cycle
// (refreshDueOpenAIOAuthAccessTokens).
func (j *RefreshJob) RunOnce(ctx context.Context, options RefreshOptions) (RefreshResult, error) {
	result := RefreshResult{}
	if err := ctx.Err(); err != nil {
		return result, nil
	}
	now := j.now()
	leadSeconds, err := j.settingsInt(ctx, "oauthAccessTokenRefreshLeadSeconds", options.LeadSeconds, DefaultRefreshLeadSeconds, MinRefreshLeadSeconds, MaxRefreshLeadSeconds)
	if err != nil {
		return result, err
	}
	batchSize, err := j.settingsInt(ctx, "oauthAccessTokenRefreshBatchSize", options.BatchSize, DefaultRefreshBatchSize, MinRefreshBatchSize, MaxRefreshBatchSize)
	if err != nil {
		return result, err
	}
	retryBackoffSeconds, err := j.settingsInt(ctx, "oauthAccessTokenRefreshRetryBackoffSeconds", options.RetryBackoffSeconds, DefaultRefreshBackoffSeconds, MinRefreshBackoffSeconds, MaxRefreshBackoffSeconds)
	if err != nil {
		return result, err
	}
	startAdmissionBudgetMs, err := j.settingsInt(ctx, "startAdmissionBudgetMs", options.StartAdmissionBudgetMs, int(openAIOAuthRefreshStartAdmissionBudget.Milliseconds()), MinAdmissionBudgetMs, MaxAdmissionBudgetMs)
	if err != nil {
		return result, err
	}
	leadMs := int64(leadSeconds) * 1000
	retryBackoffMs := int64(retryBackoffSeconds) * 1000
	admissionDeadline := now.Add(time.Duration(startAdmissionBudgetMs) * time.Millisecond)

	j.failures.CleanupBackoff(now.UnixMilli())

	candidates, err := j.selectBatchCandidates(ctx, leadSeconds, batchSize, normalizedRefreshAccountIDSet(options.AccountIDs), now, leadMs, admissionDeadline, &result)
	if err != nil {
		return result, err
	}
	result.Scanned = len(candidates)
	result.Due = len(candidates)

	concurrency := options.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultRefreshConcurrency
	}
	retryBackoff := time.Duration(retryBackoffMs) * time.Millisecond

	var (
		mu        sync.Mutex
		nextIndex int
		wg        sync.WaitGroup
		firstErr  error
	)
	workerCount := len(candidates)
	if workerCount > concurrency {
		workerCount = concurrency
	}
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if nextIndex >= len(candidates) {
					mu.Unlock()
					return
				}
				if !j.now().Before(admissionDeadline) {
					mu.Unlock()
					return
				}
				index := nextIndex
				nextIndex++
				mu.Unlock()
				if err := ctx.Err(); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				j.processCandidate(ctx, candidates[index], retryBackoff, &result, &mu)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	result.DeferredBudget += len(candidates) - nextIndex
	err = firstErr
	mu.Unlock()
	if err != nil {
		return result, err
	}
	return result, nil
}

// refreshCandidateFetchLimit mirrors refreshCandidateFetchLimit.
func (j *RefreshJob) refreshCandidateFetchLimit(batchSize int, requestedAccountCount int) int {
	if requestedAccountCount > 0 {
		limit := requestedAccountCount * 5
		if limit < batchSize {
			limit = batchSize
		}
		return clampInt(limit, 1, 500)
	}
	if memory, isMemory := j.failures.(*MemoryFailureStateStore); isMemory {
		return clampInt(batchSize+memory.snapshotCount(), 1, 500)
	}
	return clampInt(batchSize*5, 1, 200)
}

// selectBatchCandidates mirrors selectOpenAIOAuthRefreshBatchCandidates: due
// filtering, backoff reads and the batch/admission windows.
func (j *RefreshJob) selectBatchCandidates(ctx context.Context, leadSeconds, batchSize int, filter map[string]bool, now time.Time, leadMs int64, admissionDeadline time.Time, result *RefreshResult) ([]RefreshCandidate, error) {
	fetchLimit := j.refreshCandidateFetchLimit(batchSize, len(filter))
	listed, err := j.store.ListDueOpenAIRefreshAccounts(ctx, leadSeconds, fetchLimit, OpenAIOAuthTokenRefreshLocalConfigurationInvalidCode, now)
	if err != nil {
		return nil, err
	}
	// A decryptable sibling proves the local keyring can read this batch.
	// Without that proof, treating every decrypt failure as an account-local
	// defect could turn a process-wide secret/keyring outage into a full-pool
	// account outage.
	accountLocalEvidenceConfirmed := false
	for _, candidate := range listed {
		if !candidate.IsDecryptFailure() {
			accountLocalEvidenceConfirmed = true
			break
		}
	}
	due := make([]RefreshCandidate, 0, len(listed))
	for _, candidate := range listed {
		if filter != nil && !filter[candidateID(candidate)] {
			continue
		}
		if candidate.IsDecryptFailure() {
			candidate.AccountLocalEvidenceConfirmed = accountLocalEvidenceConfirmed
			due = append(due, candidate)
			continue
		}
		if !isExistingOpenAIOAuthAccountForRefresh(candidate.Account) {
			continue
		}
		if !shouldPreRefreshAccessToken(candidate.Account.Credentials, now, leadMs) {
			continue
		}
		due = append(due, candidate)
	}

	candidates := make([]RefreshCandidate, 0, batchSize)
	for offset := 0; offset < len(due) && len(candidates) < batchSize; {
		if !j.now().Before(admissionDeadline) {
			result.DeferredBudget += len(due) - offset
			break
		}
		windowEnd := offset + batchSize - len(candidates)
		if windowEnd > len(due) {
			windowEnd = len(due)
		}
		window := due[offset:windowEnd]
		offset += len(window)
		for _, candidate := range window {
			if len(candidates) >= batchSize {
				break
			}
			state, err := j.failures.Read(ctx, candidateID(candidate), j.now().UnixMilli(), candidateConfigRevision(candidate))
			if err != nil {
				return nil, err
			}
			if state != nil && state.BackoffUntil > j.now().UnixMilli() {
				result.SkippedBackoff++
				continue
			}
			candidate.ObservedFailure = state
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

// processCandidate mirrors processCandidate: local-configuration handling,
// the locked refresh call, failure recording and the terminal stopped state.
func (j *RefreshJob) processCandidate(ctx context.Context, candidate RefreshCandidate, retryBackoff time.Duration, result *RefreshResult, mu *sync.Mutex) {
	accountID := candidateID(candidate)
	accountName := candidateName(candidate)
	accountStatus := candidateStatus(candidate)
	attemptRevision := candidateConfigRevision(candidate)
	countedAsStarted := false
	markStarted := func() {
		if countedAsStarted {
			return
		}
		countedAsStarted = true
		mu.Lock()
		result.Started++
		mu.Unlock()
	}

	var refreshErr error
	if candidate.IsDecryptFailure() {
		markStarted()
		if candidate.AccountLocalEvidenceConfirmed {
			refreshErr = &LocalConfigurationError{
				Message:                candidate.ErrorMessage,
				ExpectedConfigRevision: candidate.ConfigRevision,
			}
		} else {
			refreshErr = errors.New("OpenAI OAuth 凭据存储当前不可验证，未改变账户调度状态")
		}
	} else {
		refreshErr = j.locks.TryLock(accountID, func() error {
			markStarted()
			_, refreshErr := j.refreshWithRaceRetry(ctx, candidate.Account, RefreshAccountOptions{})
			return refreshErr
		})
	}
	if refreshErr == nil {
		// Success: clear the observed failure state and restore a previously
		// stopped account (restoreOpenAIOAuthTokenRefreshFailureIfRecovered).
		if candidate.ObservedFailure != nil {
			_ = j.failures.Clear(ctx, accountID, *candidate.ObservedFailure)
		}
		if !candidate.IsDecryptFailure() {
			j.restoreIfRecovered(ctx, candidate.Account, result, mu)
		}
		mu.Lock()
		result.Refreshed++
		mu.Unlock()
		return
	}

	var busy *LockBusyError
	if errors.As(refreshErr, &busy) {
		mu.Lock()
		result.SkippedLocked++
		mu.Unlock()
		return
	}

	mu.Lock()
	result.Failed++
	mu.Unlock()

	failureKind := FailureKindUntrustedUpstream
	if IsLocalConfigurationError(refreshErr) {
		failureKind = FailureKindLocalConfiguration
	}
	failureState, recordErr := j.failures.Record(ctx, accountID, j.now().Add(retryBackoff).UnixMilli(), failureKind, attemptRevision)
	if recordErr != nil {
		j.logger.Error("OAuth 刷新失败状态写入失败", "error", recordErr, "accountId", accountID)
		return
	}
	expiredOrMissing := true
	if !candidate.IsDecryptFailure() {
		expiredOrMissing = accessTokenExpiredOrMissing(candidate.Account.Credentials, j.now())
	}
	j.logger.Warn("OpenAI OAuth 访问令牌刷新失败",
		"event", "openai_oauth_access_token_refresh_account_failed",
		"accountId", accountID,
		"accountName", accountName,
		"failureCount", failureState.Count,
		"localConfigurationFailureCount", failureState.LocalConfigurationCount,
		"failureKind", string(failureKind),
		"failureStateApplied", failureState.Applied,
		"accessTokenExpiredOrMissing", expiredOrMissing,
		"error", refreshErr)

	if failureState.Applied &&
		failureState.LocalConfigurationCount >= openAIOAuthRefreshFailureThreshold &&
		accountStatus == "active" {
		updated, err := j.store.MarkAccountFailureState(ctx, accountID,
			OpenAIOAuthTokenRefreshLocalConfigurationInvalidCode,
			openAIOAuthTokenRefreshLocalConfigurationStoppedMessage(int(failureState.LocalConfigurationCount), sanitizedErrorMessage(refreshErr)),
			failureState.ConfigRevision, "active")
		if err != nil {
			j.logger.Error("OpenAI OAuth 本地配置异常标记失败", "error", err, "accountId", accountID)
			return
		}
		if updated {
			_ = j.failures.Clear(ctx, accountID, failureState)
			mu.Lock()
			result.Exceptioned++
			mu.Unlock()
		}
	}
}

func (j *RefreshJob) restoreIfRecovered(ctx context.Context, account *RotationAccount, result *RefreshResult, mu *sync.Mutex) {
	if account.Status != "error" || !isManagedOpenAIOAuthRefreshErrorCode(account.LastErrorCode) {
		return
	}
	changed, status, err := j.store.ClearAccountFailureState(ctx, account.ID, ManagedRefreshErrorCodes)
	if err != nil {
		j.logger.Error("OpenAI OAuth 刷新失败账户恢复失败", "error", err, "accountId", account.ID)
		return
	}
	if changed && status == "active" {
		j.logger.Info("OpenAI OAuth Access Token 后台刷新成功，已自动恢复此前刷新失败异常",
			"event", "openai_oauth_access_token_refresh_account_restored",
			"accountId", account.ID,
			"accountName", account.Name)
		mu.Lock()
		result.Cooldowned++
		mu.Unlock()
	}
}

// RefreshAccountOptions mirrors OpenAIOAuthAccountRefreshCallOptions (the
// single-account path).
type RefreshAccountOptions struct {
	Force               bool
	LeadSeconds         *int
	RestoreFailureState bool
}

// RefreshAccount mirrors refreshOpenAIOAuthAccountAccessToken for one account
// (waiting on the per-account lock, manual path).
func (j *RefreshJob) RefreshAccount(ctx context.Context, account *RotationAccount, options RefreshAccountOptions) (*RotationAccount, error) {
	var refreshed *RotationAccount
	err := j.locks.Lock(account.ID, func() error {
		result, taskErr := j.refreshWithRaceRetry(ctx, account, options)
		refreshed = result
		return taskErr
	})
	if err != nil {
		return nil, err
	}
	return refreshed, nil
}

// refreshWithRaceRetry mirrors refreshOpenAIOAuthAccountAccessTokenLocked
// including the refresh-token race retry (fixedRetryPolicy('…race', 0, 1): one
// extra attempt with the latest stored refresh token).
func (j *RefreshJob) refreshWithRaceRetry(ctx context.Context, account *RotationAccount, options RefreshAccountOptions) (*RotationAccount, error) {
	const raceRetryAttempts = 1
	now := j.now()
	leadMs := int64(DefaultRefreshLeadSeconds) * 1000
	if options.LeadSeconds != nil {
		leadMs = int64(*options.LeadSeconds) * 1000
	}
	current, err := j.store.FindRotationAccount(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	retryWithLatestRefreshToken := false
	for attempt := 0; attempt <= raceRetryAttempts; attempt++ {
		if current == nil {
			return nil, errors.New("OpenAI OAuth 账户不存在或无法刷新")
		}
		credentials := current.Credentials
		refreshToken := stringCredential(credentials, "refresh_token")
		if refreshToken == "" {
			return nil, &LocalConfigurationError{Message: "OpenAI OAuth 账户缺少刷新令牌", ExpectedConfigRevision: current.ConfigRevision}
		}
		if credentialsChanged(account.Credentials, credentials) && !accessTokenExpiredOrMissing(credentials, now) {
			return current, nil
		}
		if !options.Force && !retryWithLatestRefreshToken && !shouldPreRefreshAccessToken(credentials, now, leadMs) {
			return current, nil
		}
		// Once a refresh starts, the provider may rotate the refresh token
		// before persistence completes; every downstream error goes through
		// the race recovery check exactly like the Node catch block.
		tokenInfo, refreshErr := RefreshOpenAIToken(ctx, j.exchanger, refreshToken, stringCredential(credentials, "client_id"), j.now())
		cause := refreshErr
		if cause == nil {
			nextCredentials := mergeCredentials(credentials, BuildOpenAIOAuthCredentials(tokenInfo, refreshToken))
			rotation, rotateErr := j.store.RotateCredentials(ctx, RotateCredentialsInput{
				AccountID:                         current.ID,
				ExpectedConfigRevision:            current.ConfigRevision,
				ExpectedProviderCode:              ProviderGPT,
				ExpectedAccountType:               AccountTypeOAuth,
				ExpectedProviderProtocolProfileID: current.ProviderProtocolProfileID,
				Credentials:                       nextCredentials,
			})
			if rotateErr != nil {
				cause = rotateErr
			} else if rotation == nil {
				cause = errors.New("OpenAI OAuth 账户不存在或无法更新")
			} else {
				updated := *current
				updated.Credentials = rotation.Credentials
				updated.ConfigRevision = rotation.ConfigRevision
				updated.UpdatedAt = rotation.UpdatedAt
				return &updated, nil
			}
		}
		recovered, recoverErr := j.tryRecoverRace(ctx, current)
		if recoverErr != nil {
			return nil, recoverErr
		}
		if recovered.result == raceRecoveredFresh {
			return recovered.account, nil
		}
		if recovered.result == raceRecoveredRetry && attempt < raceRetryAttempts {
			current = recovered.account
			retryWithLatestRefreshToken = true
			continue
		}
		return nil, cause
	}
	return nil, errors.New("OpenAI OAuth 访问令牌刷新失败")
}

type raceRecoveryResult struct {
	result  string // "fresh" | "retry" | "none"
	account *RotationAccount
}

const (
	raceRecoveredFresh = "fresh"
	raceRecoveredRetry = "retry"
	raceRecoveredNone  = "none"
)

// tryRecoverRace mirrors tryRecoverOpenAIOAuthRefreshRace.
func (j *RefreshJob) tryRecoverRace(ctx context.Context, used *RotationAccount) (raceRecoveryResult, error) {
	latest, err := j.store.FindRotationAccount(ctx, used.ID)
	if err != nil {
		return raceRecoveryResult{}, err
	}
	if latest == nil {
		return raceRecoveryResult{result: raceRecoveredNone}, nil
	}
	usedRefreshToken := stringCredential(used.Credentials, "refresh_token")
	latestRefreshToken := stringCredential(latest.Credentials, "refresh_token")
	refreshTokenChanged := usedRefreshToken != "" && latestRefreshToken != "" && usedRefreshToken != latestRefreshToken
	accessTokenChanged := stringCredential(used.Credentials, "access_token") != stringCredential(latest.Credentials, "access_token")
	latestAccessTokenUsable := !accessTokenExpiredOrMissing(latest.Credentials, j.now())
	if latestAccessTokenUsable && (refreshTokenChanged || accessTokenChanged || credentialExpiresAtLater(latest.Credentials, used.Credentials)) {
		return raceRecoveryResult{result: raceRecoveredFresh, account: latest}, nil
	}
	if refreshTokenChanged {
		return raceRecoveryResult{result: raceRecoveredRetry, account: latest}, nil
	}
	return raceRecoveryResult{result: raceRecoveredNone}, nil
}

// ---------------------------------------------------------------------------
// Credential/time predicates (mirror the Node helpers verbatim)
// ---------------------------------------------------------------------------

// shouldPreRefreshAccessToken mirrors shouldPreRefreshAccessToken: missing
// access token, missing/unparsable expires_at, or inside the lead window.
func shouldPreRefreshAccessToken(credentials map[string]any, now time.Time, leadMs int64) bool {
	if stringCredential(credentials, "access_token") == "" {
		return true
	}
	expiresAt, err := parseCredentialExpiresAt(credentials)
	if err != nil {
		return true
	}
	if expiresAt == nil {
		return true
	}
	return *expiresAt-now.UnixMilli() <= leadMs
}

// accessTokenExpiredOrMissing mirrors isAccessTokenExpiredOrMissing.
func accessTokenExpiredOrMissing(credentials map[string]any, now time.Time) bool {
	if stringCredential(credentials, "access_token") == "" {
		return true
	}
	expiresAt, err := parseCredentialExpiresAt(credentials)
	if err != nil {
		return true
	}
	return expiresAt == nil || *expiresAt <= now.UnixMilli()
}

// parseCredentialExpiresAt mirrors parseCredentialExpiresAt with the exact
// error copy.
func parseCredentialExpiresAt(credentials map[string]any) (*int64, error) {
	expiresAtText := credentialExpiresAt(credentials)
	if expiresAtText == "" {
		return nil, nil
	}
	millis, ok := rfc3339Millis(expiresAtText)
	if !ok {
		return nil, errors.New("OpenAI OAuth credentials.expires_at必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return &millis, nil
}

func credentialExpiresAt(credentials map[string]any) string {
	value, exists := credentials["expires_at"]
	if !exists {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func credentialsChanged(left, right map[string]any) bool {
	return stringCredential(left, "access_token") != stringCredential(right, "access_token") ||
		stringCredential(left, "refresh_token") != stringCredential(right, "refresh_token") ||
		credentialExpiresAt(left) != credentialExpiresAt(right)
}

func credentialExpiresAtLater(next, current map[string]any) bool {
	nextExpiresAt, nextErr := parseCredentialExpiresAt(next)
	currentExpiresAt, currentErr := parseCredentialExpiresAt(current)
	if nextErr != nil || currentErr != nil {
		return false
	}
	return nextExpiresAt != nil && (currentExpiresAt == nil || *nextExpiresAt > *currentExpiresAt)
}

// mergeCredentials mirrors {...current, ...token}.
func mergeCredentials(current, token map[string]any) map[string]any {
	merged := make(map[string]any, len(current)+len(token))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range token {
		merged[key] = value
	}
	return merged
}

// isExistingOpenAIOAuthAccountForRefresh mirrors
// isExistingOpenAIOAuthAccountForRefresh: GPT vendor + oauth type (the
// OpenAI-protocol profile and authorized-account exclusions are enforced by
// the SQL filter) and not in the terminal stopped state.
func isExistingOpenAIOAuthAccountForRefresh(account *RotationAccount) bool {
	if account == nil {
		return false
	}
	if account.ProviderCode != ProviderGPT || account.Type != AccountTypeOAuth {
		return false
	}
	return !shouldStopOpenAIOAuthBackgroundRefresh(account)
}

func shouldStopOpenAIOAuthBackgroundRefresh(account *RotationAccount) bool {
	return account.Status == "error" && account.LastErrorCode == OpenAIOAuthTokenRefreshLocalConfigurationInvalidCode
}

func isManagedOpenAIOAuthRefreshErrorCode(errorCode string) bool {
	for _, managed := range ManagedRefreshErrorCodes {
		if managed == errorCode {
			return true
		}
	}
	return false
}

// openAIOAuthTokenRefreshLocalConfigurationStoppedMessage mirrors the terminal
// reason copy (joined and truncated to 1000 characters).
func openAIOAuthTokenRefreshLocalConfigurationStoppedMessage(failureCount int, lastError string) string {
	message := strings.Join([]string{
		fmt.Sprintf("OpenAI OAuth 访问令牌连续 %d 次因本地配置错误无法启动刷新，已停止自动刷新。", failureCount),
		"该结论只来自本地可验证的凭据、代理配置解析或解密失败，不使用上游 HTTP 状态、错误码或响应正文推断账户状态。",
		"请在账户页检查并修正 OAuth 凭据或代理配置，然后重新检查账户。",
		fmt.Sprintf("最后本地错误：%s", lastError),
	}, " ")
	if len([]rune(message)) > 1000 {
		message = truncateRunes(message, 1000)
	}
	return message
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// sanitizedErrorMessage mirrors errorMessage: upstream errors keep their
// verbatim message, other errors localize to the 502 system copy; both
// truncate to 240 with a trailing "...".
func sanitizedErrorMessage(err error) string {
	message := err.Error()
	if _, upstream := AsUpstreamError(err); upstream {
		// sanitizeOpenAIOAuthErrorMessage is identity in Node.
		_ = upstream
	} else if !IsLocalConfigurationError(err) {
		message = "系统内部错误，请稍后重试"
	}
	if len([]rune(message)) > 240 {
		message = string([]rune(message)[:237]) + "..."
	}
	return message
}

func normalizedRefreshAccountIDSet(accountIDs []string) map[string]bool {
	if accountIDs == nil {
		return nil
	}
	set := map[string]bool{}
	for _, id := range accountIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			set[trimmed] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

func candidateID(candidate RefreshCandidate) string {
	if candidate.Account != nil {
		return candidate.Account.ID
	}
	return candidate.AccountID
}

func candidateName(candidate RefreshCandidate) string {
	if candidate.Account != nil {
		return candidate.Account.Name
	}
	return candidate.AccountName
}

func candidateStatus(candidate RefreshCandidate) string {
	if candidate.Account != nil {
		return candidate.Account.Status
	}
	return candidate.AccountStatus
}

func candidateConfigRevision(candidate RefreshCandidate) int64 {
	if candidate.Account != nil {
		if candidate.Account.ConfigRevision >= 1 {
			return candidate.Account.ConfigRevision
		}
		return 1
	}
	if candidate.ConfigRevision >= 1 {
		return candidate.ConfigRevision
	}
	return 1
}
