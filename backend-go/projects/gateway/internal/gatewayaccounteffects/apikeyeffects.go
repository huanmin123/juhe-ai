package gatewayaccounteffects

import (
	"context"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// accountApiKeySuccessWriteCoalesceMs mirrors the Node constant.
const accountAPIKeySuccessWriteCoalesceMs = int64(250)

// AccountAPIKeyFailureWrite mirrors the record_account_api_key_failure
// db-service operation.
type AccountAPIKeyFailureWrite struct {
	Account        gatewayruntimecache.OpenAIAccountSecret
	TrafficSource  string
	MutationContext AccountApiKeyPersistentMutationContext
	Input          AccountAPIKeyFailureWriteInput
}

// AccountAPIKeyFailureWriteInput mirrors the operation input.
type AccountAPIKeyFailureWriteInput struct {
	Status                       AccountApiKeyFailureStatus
	StatusCode                   *int64
	ErrorCode                    *string
	ErrorMessage                 *string
	TraceID                      *string
	CooldownUntil                *string
	QuotaRecoveryMode            QuotaRecoveryMode
	BreakQuotaRecoveryWindow     *bool
	ObservedAt                   string
	ExpectedStatus               *AccountApiKeyFailureStatus
	ExpectedNextProbeAt          *string
	ExpectedStateUpdatedAt       *string
	ExpectedAccountConfigRevision *int64
	ExpectedProbeClaimToken      *string
}

// AccountAPIKeySuccessWrite mirrors the record_account_api_key_success
// db-service operation.
type AccountAPIKeySuccessWrite struct {
	Account         gatewayruntimecache.OpenAIAccountSecret
	TrafficSource   string
	MutationContext AccountApiKeyPersistentMutationContext
	ObservedAt      string
	ExpectedStatus             *AccountApiKeyFailureStatus
	ExpectedNextProbeAt        *string
	ExpectedStateUpdatedAt     *string
	ExpectedAccountConfigRevision *int64
	ExpectedProbeClaimToken    *string
}

// APIKeyWriteResult mirrors { changed, skippedReason }.
type APIKeyWriteResult struct {
	Changed       bool
	SkippedReason *string
}

// AccountAPIKeyWriter is the db-service port of the api-key runtime writes.
type AccountAPIKeyWriter interface {
	RecordFailure(ctx context.Context, write AccountAPIKeyFailureWrite) (APIKeyWriteResult, error)
	RecordSuccess(ctx context.Context, write AccountAPIKeySuccessWrite) (APIKeyWriteResult, error)
}

// RecordFailureInput mirrors recordGatewayAccountApiKeyFailure's input.
type RecordFailureInput struct {
	Status             AccountApiKeyFailureStatus
	StatusCode         *int64
	ErrorCode          *string
	ErrorMessage       *string
	TraceID            *string
	CooldownUntil      *string
	QuotaRecoveryMode  QuotaRecoveryMode
	TrafficSource      string
	MutationContext    *AccountApiKeyPersistentMutationContext
	ClientIP           string
	APIKeyID           string
	ObservationEpoch   *int64
	AttemptStartedAt   *string
	Source             string
}

// AccountAPIKeyEffects mirrors account-api-key-effects.service.ts.
type AccountAPIKeyEffects struct {
	clock   Clock
	config  SideEffectsConfig
	logger  Logger
	guard   *AccountAPIKeyFailureGuard
	writer  AccountAPIKeyWriter
	invalidate func()
	sched   Scheduler

	mu      sync.Mutex
	pending map[string]*pendingAPIKeySuccessWrite
}

type pendingAPIKeySuccessWrite struct {
	account         gatewayruntimecache.OpenAIAccountSecret
	source          string
	trafficSource   string
	mutationContext AccountApiKeyPersistentMutationContext
	observedAt      string
	timer           SchedulerHandle
	writing         bool
}

// NewAccountAPIKeyEffects builds the effects service; invalidate mirrors
// clearGatewayRuntimeCache.
func NewAccountAPIKeyEffects(config SideEffectsConfig, guard *AccountAPIKeyFailureGuard, writer AccountAPIKeyWriter, invalidate func(), deps Clock, scheduler Scheduler, logger Logger) *AccountAPIKeyEffects {
	if deps == nil {
		deps = SystemClock{}
	}
	if scheduler == nil {
		scheduler = RealScheduler{}
	}
	if logger == nil {
		logger = NopLogger{}
	}
	return &AccountAPIKeyEffects{
		clock:      deps,
		config:     config,
		logger:     logger,
		guard:      guard,
		writer:     writer,
		invalidate: invalidate,
		sched:      scheduler,
		pending:    map[string]*pendingAPIKeySuccessWrite{},
	}
}

// RecordFailure mirrors recordGatewayAccountApiKeyFailure. Like the Node
// service it never surfaces the write error: every failure path logs a
// warning and keeps the gateway request path unaffected.
func (e *AccountAPIKeyEffects) RecordFailure(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret, input RecordFailureInput) {
	if account.SelectedAPIKeyFingerprint == nil || account.APIKeyRuntimeStateDisabled {
		return
	}
	var observedAt string
	if input.AttemptStartedAt != nil {
		observedAt = *input.AttemptStartedAt
	} else {
		observedAt = canonicalRFC3339(e.clock.Now())
	}
	var expectedAccountConfigRevision *int64
	if input.QuotaRecoveryMode != "" {
		expectedAccountConfigRevision = account.ConfigRevision
	}
	guardDecision := e.guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{
		Status:           input.Status,
		StatusCode:       input.StatusCode,
		ErrorCode:        input.ErrorCode,
		ErrorMessage:     input.ErrorMessage,
		TrafficSource:    input.TrafficSource,
		MutationContext:  input.MutationContext,
		ClientIP:         input.ClientIP,
		APIKeyID:         input.APIKeyID,
		ObservationEpoch: input.ObservationEpoch,
		Source:           input.Source,
	})
	if guardDecision.Reason == GuardReasonRedisTransientOnly {
		if _, err := e.guard.RecordTransientFailure(ctx, account, input.Status); err != nil {
			e.logger.Warn(map[string]any{
				"event":                     "gateway_account_api_key_transient_avoidance_write_failed",
				"accountId":                 account.ID,
				"selectedApiKeyFingerprint": *account.SelectedAPIKeyFingerprint,
				"source":                    input.Source,
			}, "账户内 API Key Redis 短暂避让写入失败")
		}
		return
	}
	if !guardDecision.Persist {
		return
	}
	if e.config.IsRedisDriver() && input.Source == "same_account_api_key_rotation_confirmed" {
		if _, err := e.guard.RecordTransientFailure(ctx, account, input.Status); err != nil {
			e.logger.Warn(map[string]any{
				"event":                     "gateway_account_api_key_confirmed_rotation_transient_write_failed",
				"accountId":                 account.ID,
				"selectedApiKeyFingerprint": *account.SelectedAPIKeyFingerprint,
				"source":                    input.Source,
			}, "账户内 API Key 已确认切换后的短暂避让写入失败")
		}
	}
	if input.MutationContext == nil || input.TrafficSource == "" {
		return
	}
	write := AccountAPIKeyFailureWrite{
		Account:        account,
		TrafficSource:  input.TrafficSource,
		MutationContext: *input.MutationContext,
		Input: AccountAPIKeyFailureWriteInput{
			Status:                        input.Status,
			StatusCode:                    input.StatusCode,
			ErrorCode:                     input.ErrorCode,
			ErrorMessage:                  input.ErrorMessage,
			TraceID:                       input.TraceID,
			CooldownUntil:                 input.CooldownUntil,
			QuotaRecoveryMode:             input.QuotaRecoveryMode,
			ObservedAt:                    observedAt,
			ExpectedAccountConfigRevision: expectedAccountConfigRevision,
		},
	}
	if e.config.IsRedisDriver() {
		go func() {
			result, err := e.writer.RecordFailure(context.WithoutCancel(ctx), write)
			if err != nil {
				e.logger.Warn(map[string]any{
					"event":                     "gateway_account_api_key_failure_side_effect_failed",
					"accountId":                 account.ID,
					"selectedApiKeyFingerprint": *account.SelectedAPIKeyFingerprint,
					"source":                    input.Source,
				}, "账户内 API Key 失败运行态异步写入失败")
				return
			}
			if result.Changed {
				e.invalidateRuntimeCache()
			}
		}()
		return
	}
	result, err := e.writer.RecordFailure(ctx, write)
	if err != nil {
		e.logger.Warn(map[string]any{
			"event":                     "gateway_account_api_key_failure_side_effect_failed",
			"accountId":                 account.ID,
			"selectedApiKeyFingerprint": *account.SelectedAPIKeyFingerprint,
			"source":                    input.Source,
		}, "账户内 API Key 失败运行态写入失败")
		return
	}
	if result.Changed {
		e.invalidateRuntimeCache()
	}
}

// RecordSuccess mirrors recordGatewayAccountApiKeyFailure's success twin:
// recordGatewayAccountApiKeySuccess.
func (e *AccountAPIKeyEffects) RecordSuccess(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret, input RecordSuccessInput) {
	if account.APIKeyRuntimeStateDisabled {
		return
	}
	observedAt := canonicalRFC3339(e.clock.Now())
	authorization := AuthorizeAccountApiKeyPersistentMutationForTrafficSource(MutationKindSuccess, input.TrafficSource, input.MutationContext)
	if input.MutationContext != nil && !authorization.Allowed {
		return
	}
	if input.TrafficSource == TrafficSourceGateway {
		e.guard.RecordSuccessGuard(account)
	}
	if input.TrafficSource == TrafficSourceGateway && e.config.IsRedisDriver() && account.SelectedAPIKeyFingerprint != nil {
		if _, err := e.guard.ClearTransientFailure(ctx, account); err != nil {
			e.logger.Warn(map[string]any{
				"event":                     "gateway_account_api_key_transient_avoidance_clear_failed",
				"accountId":                 account.ID,
				"selectedApiKeyFingerprint": *account.SelectedAPIKeyFingerprint,
				"source":                    input.Source,
			}, "账户内 API Key Redis 短暂避让清理失败")
		}
	}
	if account.SelectedAPIKeyFingerprint == nil || !authorization.Allowed || input.MutationContext == nil || input.TrafficSource == "" {
		return
	}
	e.coalesceSuccessWrite(account, input.Source, input.TrafficSource, *input.MutationContext, observedAt)
}

// RecordSuccessInput mirrors recordGatewayAccountApiKeySuccess's input.
type RecordSuccessInput struct {
	Source          string
	TrafficSource   string
	MutationContext *AccountApiKeyPersistentMutationContext
}

func (e *AccountAPIKeyEffects) coalesceSuccessWrite(account gatewayruntimecache.OpenAIAccountSecret, source string, trafficSource string, mutationContext AccountApiKeyPersistentMutationContext, observedAt string) {
	key := accountAPIKeySuccessWriteKey(account)
	e.mu.Lock()
	defer e.mu.Unlock()
	current := e.pending[key]
	if current != nil {
		if observedAt >= current.observedAt {
			current.account = account
			current.source = source
			current.trafficSource = trafficSource
			current.mutationContext = mutationContext
			current.observedAt = observedAt
		}
		return
	}
	entry := &pendingAPIKeySuccessWrite{
		account:         account,
		source:          source,
		trafficSource:   trafficSource,
		mutationContext: mutationContext,
		observedAt:      observedAt,
	}
	e.pending[key] = entry
	e.scheduleSuccessWriteLocked(key, entry)
}

func (e *AccountAPIKeyEffects) scheduleSuccessWriteLocked(key string, entry *pendingAPIKeySuccessWrite) {
	if entry.timer != nil || entry.writing {
		return
	}
	entry.timer = e.sched.After(accountAPIKeySuccessWriteCoalesceMs, func() {
		e.mu.Lock()
		entry.timer = nil
		e.mu.Unlock()
		e.flushSuccessWrite(key, entry)
	})
}

func (e *AccountAPIKeyEffects) flushSuccessWrite(key string, entry *pendingAPIKeySuccessWrite) {
	e.mu.Lock()
	if entry.writing || e.pending[key] != entry {
		e.mu.Unlock()
		return
	}
	entry.writing = true
	account := entry.account
	source := entry.source
	trafficSource := entry.trafficSource
	mutationContext := entry.mutationContext
	observedAt := entry.observedAt
	e.mu.Unlock()

	result, err := e.writer.RecordSuccess(context.Background(), AccountAPIKeySuccessWrite{
		Account:         account,
		TrafficSource:   trafficSource,
		MutationContext: mutationContext,
		ObservedAt:      observedAt,
	})
	if err != nil {
		var fingerprint string
		if account.SelectedAPIKeyFingerprint != nil {
			fingerprint = *account.SelectedAPIKeyFingerprint
		}
		e.logger.Warn(map[string]any{
			"event":                     "gateway_account_api_key_success_side_effect_failed",
			"accountId":                 account.ID,
			"selectedApiKeyFingerprint": fingerprint,
			"source":                    source,
		}, "账户内 API Key 成功运行态写入失败")
	} else if result.Changed {
		e.invalidateRuntimeCache()
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	entry.writing = false
	if e.pending[key] != entry {
		return
	}
	if entry.observedAt == observedAt {
		delete(e.pending, key)
	} else {
		e.scheduleSuccessWriteLocked(key, entry)
	}
}

// FlushSuccessWritesForTest mirrors flushGatewayAccountApiKeySuccessWritesForTest.
func (e *AccountAPIKeyEffects) FlushSuccessWritesForTest(ctx context.Context) {
	for {
		e.mu.Lock()
		entries := make([]*pendingAPIKeySuccessWrite, 0, len(e.pending))
		keys := make([]string, 0, len(e.pending))
		for key, entry := range e.pending {
			keys = append(keys, key)
			entries = append(entries, entry)
			if entry.timer != nil {
				entry.timer.Cancel()
				entry.timer = nil
			}
		}
		e.mu.Unlock()
		if len(entries) == 0 {
			return
		}
		done := make(chan struct{}, len(entries))
		for index, entry := range entries {
			go func(k string, item *pendingAPIKeySuccessWrite) {
				e.flushSuccessWrite(k, item)
				done <- struct{}{}
			}(keys[index], entry)
		}
		for range entries {
			<-done
		}
	}
}

func (e *AccountAPIKeyEffects) invalidateRuntimeCache() {
	if e.invalidate != nil {
		e.invalidate()
	}
}

func accountAPIKeySuccessWriteKey(account gatewayruntimecache.OpenAIAccountSecret) string {
	source := account.ID
	if account.CredentialSourceAccountID != nil {
		source = *account.CredentialSourceAccountID
	}
	fingerprint := ""
	if account.SelectedAPIKeyFingerprint != nil {
		fingerprint = *account.SelectedAPIKeyFingerprint
	}
	return source + "\x00" + fingerprint
}
