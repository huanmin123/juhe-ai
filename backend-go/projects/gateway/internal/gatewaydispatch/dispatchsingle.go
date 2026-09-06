package gatewaydispatch

import (
	"context"
	"errors"
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// dispatchSingleAccount owns the per-candidate dispatch loop of
// fetchFirstAvailableUpstream (dispatch/upstream-dispatch.ts, the
// `for (const originalAccount of dispatchAccounts)` body including the
// API-key rotation do/while and the URL/attempt loops).

// dispatchSingleAccountInput bundles the mutable engine state.
type dispatchSingleAccountInput struct {
	args                                 *FetchFirstAvailableUpstreamArgs
	coordination                         *RequestCoordinationContext
	originalAccount                      AccountCandidate
	usageContext                         *gatewaypreauth.GatewayFailureUsageContext
	auditCapture                         AuditCapture
	settings                             gatewayruntimecache.GatewaySettings
	timeoutProfile                       gatewayrouting.GatewayTimeoutProfile
	signal                               context.Context
	requestLane                          string
	semanticRetryID                      string
	bypassLocalSuppression               bool
	automaticAccountStateMutationAllowed bool
	accountLockTrafficEnabled            bool
	compactionTimeoutsDisabled           bool
	requestApiKeyAttemptCount            *int
	activeSameAccountRetryID             *string
	activeAccountLockRetryLease          **AccountLockRetryLease
	activeAccountLockObservation         **AccountLockObservation
	primaryDispatchTier                  string
	observedEscapedTiers                 map[string]struct{}
	failedProxyDispatchKeys              map[string]string
	failedAccountIDs                     map[string]struct{}
	recoverableFailedAccountIDs          map[string]struct{}
	cycleRecoverableAccountIDs           map[string]struct{}
	capacityLimitFailures                *[]AccountCapacityLimitFailure
	pendingApiKeyFailures                *[]PendingAccountApiKeyFailure
	lastAttempt                          **UpstreamAttempt
	agentGuidanceResponse                **gatewaypreauth.GatewayAgentGuidanceResponse
	auditAttemptIndex                    *int
	concurrencyRetryWaitBudgetMs         *int64
	keyModelFailureBudget                *gatewayaccounteffects.GatewayKeyModelFailureBudget
	accountCircuitAttempt                *gatewaycircuit.Attempt
	setAccountCircuitAttemptTransferred  func()
	reserveSameAccountRetry              func(identity gatewayrouting.GatewayDispatchAttemptIdentity, reason, accountID string) (string, error)
	createAccountLockLeaseRelease        func(scheduleNextRetry bool) func(bool) bool
}

// dispatchResultKind mirrors the loop control outcomes.
type dispatchResultKind int

const (
	dispatchResultContinue dispatchResultKind = iota
	dispatchResultSelected
	dispatchResultSkipRestOfCycle
)

// dispatchSingleAccount executes the candidate loop body for one account.
func (e *Engine) dispatchSingleAccount(ctx context.Context, in dispatchSingleAccountInput) (dispatchResultKind, *UpstreamDispatchResult, error) {
	originalAccount := in.originalAccount
	usageContext := in.usageContext
	auditCapture := in.auditCapture
	signal := in.signal

	// Local suppression re-check (Node filterGatewayAccountRuntimeSuppressions
	// with acquireHalfOpenLease).
	var halfOpenLease HalfOpenLease
	var localSuppression SuppressionFilterResult
	if in.bypassLocalSuppression {
		localSuppression = localSuppressionBypassResult([]AccountCandidate{originalAccount})
	} else {
		filter, err := e.Suppression.FilterAsync(ctx, []AccountCandidate{originalAccount}, SuppressionFilterOptions{
			AcquireHalfOpenLease:         true,
			AcquirePrecheckHalfOpenLease: in.args.AllowPrecheckHalfOpen,
			PrecheckHalfOpenGroupKey:     usageContext.SystemAccountID + ":" + usageContext.GroupID,
		})
		if err != nil {
			return dispatchResultContinue, nil, err
		}
		localSuppression = filter
		if len(localSuppression.AcquiredHalfOpenLeases) > 0 {
			halfOpenLease = localSuppression.AcquiredHalfOpenLeases[0]
		}
	}
	if localSuppression.AllSuppressed {
		*in.lastAttempt = locallySuppressedAttempt(originalAccount, localSuppression.NextRetryAfterMs)
		return dispatchResultContinue, nil, nil
	}

	// Failed proxy skip.
	skippedProxyAttempt := e.SkipAccountForFailedProxyDispatch(in.failedProxyDispatchKeys, originalAccount)
	if skippedProxyAttempt != nil {
		_ = releaseHalfOpenLease(ctx, halfOpenLease)
		*in.lastAttempt = skippedProxyAttempt
		in.failedAccountIDs[originalAccount.ID] = struct{}{}
		if _, recoverable := in.recoverableFailedAccountIDs[originalAccount.ID]; recoverable {
			in.cycleRecoverableAccountIDs[originalAccount.ID] = struct{}{}
		}
		return dispatchResultContinue, nil, nil
	}

	// Unavailable proxy profile.
	unavailableProxyAuditAttemptIndex := *in.auditAttemptIndex + 1
	unavailableProxyAttempt, proxyErr := e.HandleUnavailableProxyProfile(
		ctx, in.args.Req, *usageContext, originalAccount, in.settings,
		in.failedProxyDispatchKeys, in.automaticAccountStateMutationAllowed,
		auditCapture, unavailableProxyAuditAttemptIndex,
	)
	if proxyErr != nil {
		_ = releaseHalfOpenLease(ctx, halfOpenLease)
		return dispatchResultContinue, nil, proxyErr
	}
	if unavailableProxyAttempt != nil {
		*in.auditAttemptIndex = unavailableProxyAuditAttemptIndex
		_ = releaseHalfOpenLease(ctx, halfOpenLease)
		*in.lastAttempt = unavailableProxyAttempt
		in.failedAccountIDs[originalAccount.ID] = struct{}{}
		return dispatchResultContinue, nil, nil
	}

	// Concurrency acquire with short retry.
	concurrencyAccountID := gatewaySessionConcurrencyID(originalAccount)
	reservedSlot := e.takeReservedSlot(in.args.PreAcquiredConcurrency, originalAccount)
	var concurrencySlot ConcurrencySlot
	var concurrencyAcquireWaitedMs int64
	if reservedSlot != nil {
		concurrencySlot = *reservedSlot
	} else {
		acquired, waitedMs, acquireErr := e.acquireAccountConcurrencyWithShortRetry(
			ctx, signal, concurrencyAccountID, originalAccount.ConcurrencyLimit,
			*in.concurrencyRetryWaitBudgetMs, gatewayprotoLane(in.requestLane), in.args.GroupSchedulingPolicy,
			in.coordination.ServerRetryBudget,
		)
		if acquireErr != nil {
			_ = releaseHalfOpenLease(ctx, halfOpenLease)
			return dispatchResultContinue, nil, acquireErr
		}
		concurrencySlot = acquired
		concurrencyAcquireWaitedMs = waitedMs
	}
	*in.concurrencyRetryWaitBudgetMs = e.remainingConcurrencyWaitBudget(*in.concurrencyRetryWaitBudgetMs)
	if !concurrencySlot.Acquired {
		_ = releaseHalfOpenLease(ctx, halfOpenLease)
		message := accountConcurrencyLimitMessage(concurrencySlot, concurrencyAcquireWaitedMs)
		*in.lastAttempt = accountCapacityLimitAttempt(originalAccount, message)
		*in.capacityLimitFailures = append(*in.capacityLimitFailures, AccountCapacityLimitFailure{account: originalAccount, message: message})
		return dispatchResultContinue, nil, nil
	}
	if concurrencySlot.MarkFirstOutput == nil {
		concurrencySlot.MarkFirstOutput = func() {}
	}
	if concurrencySlot.Release == nil {
		concurrencySlot.Release = func() {}
	}

	keepConcurrencySlot := false
	releaseTransientState := func() {
		if !keepConcurrencySlot {
			concurrencySlot.Release()
			_ = releaseHalfOpenLease(ctx, halfOpenLease)
		}
	}

	// API-key rotation loop (Node do { ... } while (retryAccountApiKey)).
	excludedApiKeyFingerprints := map[string]struct{}{}
	for _, fingerprint := range in.coordination.RequestAttemptTracker.Snapshot().AttemptedKeyFingerprints {
		excludedApiKeyFingerprints[fingerprint] = struct{}{}
	}
	pendingAccountApiKeyFailures := *in.pendingApiKeyFailures
	accountApiKeyAttemptCount := 0
	previousSelectedApiKeyFingerprint := ""
	reacquireConcurrencyForNextKey := false
	retryAccountApiKey := false
	skipAccount := false
	var accountScopedResult *UpstreamDispatchResult

rotationLoop:
	for {
		retryAccountApiKey = false
		skipAccount = false
		if reacquireConcurrencyForNextKey {
			acquired, waitedMs, acquireErr := e.acquireAccountConcurrencyWithShortRetry(
				ctx, signal, concurrencyAccountID, originalAccount.ConcurrencyLimit,
				*in.concurrencyRetryWaitBudgetMs, gatewayprotoLane(in.requestLane), in.args.GroupSchedulingPolicy,
				in.coordination.ServerRetryBudget,
			)
			if acquireErr != nil {
				releaseTransientState()
				return dispatchResultContinue, nil, acquireErr
			}
			*in.concurrencyRetryWaitBudgetMs = e.remainingConcurrencyWaitBudget(*in.concurrencyRetryWaitBudgetMs)
			concurrencySlot = acquired
			reacquireConcurrencyForNextKey = false
			if !concurrencySlot.Acquired {
				message := accountConcurrencyLimitMessage(concurrencySlot, waitedMs)
				*in.lastAttempt = accountCapacityLimitAttempt(originalAccount, message)
				*in.capacityLimitFailures = append(*in.capacityLimitFailures, AccountCapacityLimitFailure{account: originalAccount, message: message})
				skipAccount = true
				break rotationLoop
			}
			if concurrencySlot.MarkFirstOutput == nil {
				concurrencySlot.MarkFirstOutput = func() {}
			}
			if concurrencySlot.Release == nil {
				concurrencySlot.Release = func() {}
			}
		}

		account := originalAccount
		effectiveServiceTier := usageContext.EffectiveServiceTier
		if effectiveServiceTier == "" {
			effectiveServiceTier = usageContext.RequestedServiceTier
		}
		if effectiveServiceTier == "" {
			effectiveServiceTier = "default"
		}

		// Account preparation.
		preparedAccount, prepErr := e.PrepareUpstreamAccount(ctx, account)
		if prepErr != nil {
			releaseTransientState()
			return dispatchResultContinue, nil, prepErr
		}
		account = preparedAccount
		upstreamUrls, urlErr := e.Driver.BuildGatewayUpstreamURLsForAccount(ctx, account, in.args.Req)
		if urlErr != nil {
			releaseTransientState()
			return dispatchResultContinue, nil, urlErr
		}
		if len(upstreamUrls) == 0 {
			skipAccount = true
			break rotationLoop
		}
		if len(account.APIKeys) > 1 && *in.requestApiKeyAttemptCount >= e.Config.AccountApiKeyRequestAttemptSafetyLimit {
			auditCapture.AddGatewayMetadata("account_api_key_request_retry_budget_exhausted", map[string]any{
				"accountId":                   account.ID,
				"accountName":                 account.Name,
				"accountApiKeyAttemptCount":   accountApiKeyAttemptCount,
				"requestApiKeyAttemptCount":   *in.requestApiKeyAttemptCount,
				"remainingConfiguredKeyCount": maxInt64(0, int64(len(account.APIKeys)-accountApiKeyAttemptCount)),
				"reason":                      "request_safety_limit",
				"requestAttemptSafetyLimit":   e.Config.AccountApiKeyRequestAttemptSafetyLimit,
				"poolExhausted":               false,
			})
			*in.lastAttempt = accountApiKeyRetryBudgetExhaustedAttempt(account, "请求级 API Key 尝试安全上限已达到，未宣称 Key 池已穷尽")
			in.failedAccountIDs[account.ID] = struct{}{}
			skipAccount = true
			break rotationLoop
		}

		sameAccountRetrySelectionActive := *in.activeSameAccountRetryID != "" &&
			in.coordination.SameAccountRetry != nil &&
			*in.activeSameAccountRetryID == in.coordination.SameAccountRetry.RetryID
		accountForApiKeySelection := account
		if in.coordination.SameAccountRetry != nil && !sameAccountRetrySelectionActive && account.SelectedAPIKeyFingerprint != nil {
			stripped := account
			stripped.SelectedAPIKeyFingerprint = nil
			stripped.SelectedAPIKeyIndex = nil
			stripped.SelectedAPIKeyTransientGeneration = nil
			stripped.SelectedAPIKeyRecoveryStartedAt = nil
			accountForApiKeySelection = stripped
		}
		selectedAccount, selected, selectErr := e.SelectAccountApiKeyForDispatch(ctx, accountForApiKeySelection, SelectApiKeyOptions{
			ExcludeFingerprints:      excludedApiKeyFingerprints,
			ContinueAfterFingerprint: previousSelectedApiKeyFingerprint,
			AllowExcludedFingerprint: func() string {
				if sameAccountRetrySelectionActive && in.coordination.SameAccountRetry.Account.SelectedAPIKeyFingerprint != nil {
					return *in.coordination.SameAccountRetry.Account.SelectedAPIKeyFingerprint
				}
				return ""
			}(),
		})
		if selectErr != nil {
			releaseTransientState()
			return dispatchResultContinue, nil, selectErr
		}
		if !selected {
			*in.lastAttempt = accountApiKeyPoolUnavailableAttempt(account)
			in.failedAccountIDs[account.ID] = struct{}{}
			auditCapture.AddGatewayMetadata("account_api_key_pool_unavailable_dispatch_skip", map[string]any{
				"accountId":   account.ID,
				"accountName": account.Name,
			})
			skipAccount = true
			break rotationLoop
		}
		account = selectedAccount
		if account.SelectedAPIKeyFingerprint != nil {
			accountApiKeyAttemptCount++
			excludedApiKeyFingerprints[*account.SelectedAPIKeyFingerprint] = struct{}{}
			previousSelectedApiKeyFingerprint = *account.SelectedAPIKeyFingerprint
		}

		requestParts, partsErr := e.BuildPreparedUpstreamRequestParts(ctx, in.args.Req, account, *usageContext, in.args.RequestClientCompatibility)
		if partsErr != nil {
			err := partsErr
			var guidanceErr *gatewaypreauth.GatewayAgentGuidanceResponse
			var localErr *gatewaypreauth.GatewayLocalProtocolResponse
			var adapterErr *OpenAIOAuthCodexAdapterError
			var validationErr *gatewaypreauth.GatewayRequestValidationError
			switch {
			case errors.As(err, &guidanceErr) && guidanceErr.IsAccountScoped():
				*in.lastAttempt = accountScopedGuidanceAttempt(account, guidanceErr)
				*in.agentGuidanceResponse = guidanceErr
				in.failedAccountIDs[account.ID] = struct{}{}
				skipAccount = true
			case signal.Err() != nil,
				errors.As(err, &guidanceErr),
				errors.As(err, &localErr),
				errors.As(err, &adapterErr) && !adapterErr.AccountScoped,
				errors.As(err, &validationErr) && !validationErr.AccountScoped:
				releaseTransientState()
				return dispatchResultContinue, nil, err
			default:
				*in.auditAttemptIndex++
				requestErrorResult, handleErr := e.FailureDispatcher.HandleUpstreamRequestError(ctx, UpstreamRequestErrorInput{
					Req:                         in.args.Req,
					UsageContext:                *usageContext,
					AuditCapture:                auditCapture,
					AuditAttemptID:              "",
					Account:                     account,
					UpstreamURL:                 "account:preparation",
					AttemptStartedAt:            NowMs(),
					AttemptIndex:                0,
					AuditAttemptIndex:           *in.auditAttemptIndex,
					Settings:                    in.settings,
					SessionAffinityKey:          in.args.SessionAffinityKey,
					LastAttempt:                 *in.lastAttempt,
					FailedProxyDispatchKeys:     in.failedProxyDispatchKeys,
					Error:                       err,
					ClientIPAccountAvoidance:    in.args.ClientIPAccountAvoidanceTracker,
					AccountStateMutationEnabled: in.automaticAccountStateMutationAllowed,
				})
				if handleErr != nil {
					releaseTransientState()
					return dispatchResultContinue, nil, handleErr
				}
				if requestErrorResult.LastAttempt != nil {
					*in.lastAttempt = requestErrorResult.LastAttempt
				}
				in.failedAccountIDs[account.ID] = struct{}{}
				retryAccountApiKey = requestErrorResult.KeyScopedFailure &&
					halfOpenLeaseUnclaimed(halfOpenLease) &&
					e.shouldRetryAnotherAccountApiKey(account, true, accountApiKeyAttemptCount, *in.requestApiKeyAttemptCount, auditCapture)
				if retryAccountApiKey {
					continue rotationLoop
				}
				skipAccount = true
			}
			if skipAccount {
				break rotationLoop
			}
			continue
		}

		headers := requestParts.Headers
		body := requestParts.Body
		if in.coordination.RequestBodyOverride != nil && in.coordination.RequestBodyOverride.AccountID == account.ID {
			body = in.coordination.RequestBodyOverride.Body
		}
		effectiveServiceTier = requestParts.EffectiveServiceTier
		usageContext.EffectiveServiceTier = effectiveServiceTier
		usageContext.EffectiveReasoningEffort = requestParts.EffectiveReasoningEffort

		kind, stop, loopErr := e.runUpstreamAttemptLoop(ctx, upstreamAttemptLoopContext{
			in:                         &in,
			account:                    account,
			headers:                    headers,
			body:                       body,
			upstreamUrls:               upstreamUrls,
			effectiveServiceTier:       effectiveServiceTier,
			excludedApiKeyFingerprints: excludedApiKeyFingerprints,
			accountApiKeyAttemptCount:  &accountApiKeyAttemptCount,
			concurrencySlot:            &concurrencySlot,
			halfOpenLease:              halfOpenLease,
			keepConcurrencySlotRef:     &keepConcurrencySlot,
			pendingApiKeyFailuresRef:   &pendingAccountApiKeyFailures,
			retryAccountApiKeyRef:      &retryAccountApiKey,
			skipAccountRef:             &skipAccount,
			resultRef:                  &accountScopedResult,
			usageContext:               usageContext,
			auditCapture:               auditCapture,
			signal:                     signal,
		})
		if loopErr != nil {
			releaseTransientState()
			return dispatchResultContinue, nil, loopErr
		}
		switch kind {
		case attemptLoopSelected:
			releaseTransientState()
			return dispatchResultSelected, accountScopedResult, nil
		case attemptLoopRetryKey:
			continue rotationLoop
		}
		switch stop {
		case attemptStopAccount:
			break rotationLoop
		case attemptStopRotation:
			continue rotationLoop
		}
		// Node loops the do/while only on retryAccountApiKey.
		if !retryAccountApiKey {
			break rotationLoop
		}
	}

	releaseTransientState()
	*in.pendingApiKeyFailures = pendingAccountApiKeyFailures
	if accountScopedResult != nil {
		return dispatchResultSelected, accountScopedResult, nil
	}
	if retryAccountApiKey || skipAccount {
		return dispatchResultSkipRestOfCycle, nil, nil
	}
	return dispatchResultContinue, nil, nil
}

// attemptLoopKind carries the loop outcome.
type attemptLoopKind int

const (
	attemptLoopExhausted attemptLoopKind = iota
	attemptLoopSelected
	attemptLoopRetryKey
)

// runUpstreamAttemptLoop mirrors the `for (const upstreamUrl of upstreamUrls)
// for (let attemptIndex...)` double loop including success, failed-response
// and error handling.
func (e *Engine) runUpstreamAttemptLoop(ctx context.Context, c upstreamAttemptLoopContext) (attemptLoopKind, attemptStop, error) {
	in := c.in
	usageContext := c.usageContext
	auditCapture := c.auditCapture
	signal := c.signal

	for _, upstreamURL := range c.upstreamUrls {
		attemptIndex := 0
		for {
			if err := throwIfRequestAborted(signal); err != nil {
				return attemptLoopExhausted, attemptStopNone, err
			}
			reserveMs := int64(0)
			if in.coordination.Scope == CoordinationScopeGatewayRequest {
				reserveMs = gatewayrouting.DefaultGatewayFinalResponseReserveMs
			}
			if err := e.assertGatewayRequestWallBudgetAvailableForAttempt(in.coordination.GatewayRequestWallBudget, in.coordination.RequestAttemptTracker, auditCapture, reserveMs); err != nil {
				return attemptLoopExhausted, attemptStopNone, err
			}
			accountRuntimeKey := gatewayAccountRuntimeKey(c.account)
			protocolModelKey, keyErr := gatewayrouting.GatewayAttemptProtocolModelKey(accountRuntimeKey, c.account.ProtocolCode, c.account.ProtocolVersion, requestModelOrEmpty(in.args.Req))
			if keyErr != nil {
				return attemptLoopExhausted, attemptStopNone, keyErr
			}
			dispatchAttemptIdentity := gatewayrouting.GatewayDispatchAttemptIdentity{
				AccountRuntimeKey:     accountRuntimeKey,
				PhysicalCredentialKey: accountPhysicalCredentialKey(c.account),
				ProtocolModelKey:      protocolModelKey,
				KeyFingerprint:        derefStringPtr(c.account.SelectedAPIKeyFingerprint),
			}
			attemptTier := gatewayAccountDispatchPriorityTier(c.account, in.args.ModelPriority)
			if in.primaryDispatchTier != "" && attemptTier != in.primaryDispatchTier {
				if _, seen := in.observedEscapedTiers[attemptTier]; !seen {
					in.observedEscapedTiers[attemptTier] = struct{}{}
				}
			}
			attemptStartedAt := NowMs()

			// Normal-route first-byte deadline.
			var normalRouteFirstByteDeadline *gatewayrouting.NormalRouteAttemptFirstByteDeadline
			var firstByteDeadlineCoordinator *NormalRouteFirstByteAttemptCoordinator
			if !in.compactionTimeoutsDisabled &&
				gatewayrouting.NormalRouteFirstByteDeadlineAppliesToLane(gatewayprotoLane(in.requestLane)) &&
				in.coordination.NormalRouteFirstByteConfig != nil {
				deadline, err := gatewayrouting.ResolveNormalRouteAttemptFirstByteDeadline(gatewayrouting.NormalRouteAttemptFirstByteDeadlineInput{
					Config:                          *in.coordination.NormalRouteFirstByteConfig,
					GatewayRequestWallBudget:        in.coordination.GatewayRequestWallBudget,
					AttemptStartedAtMs:              attemptStartedAt,
					LaneFirstByteTimeoutMs:          in.timeoutProfile.FirstByteTimeoutMs,
					UncommittedAttemptMaxLifetimeMs: in.timeoutProfile.UncommittedAttemptMaxLifetimeMs,
				})
				if err != nil {
					return attemptLoopExhausted, attemptStopNone, err
				}
				normalRouteFirstByteDeadline = &deadline
				firstByteDeadlineCoordinator = &NormalRouteFirstByteAttemptCoordinator{}
			}
			firstByteDeadlineTriggered := false
			var onFirstByteDeadline FirstByteDeadlineHandler
			if normalRouteFirstByteDeadline != nil {
				deadline := normalRouteFirstByteDeadline
				onFirstByteDeadline = func(deadlineInput FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
					firstByteDeadlineTriggered = true
					if in.coordination.OnNormalRouteFirstByteDeadline != nil {
						return in.coordination.OnNormalRouteFirstByteDeadline(deadlineInput, c.account, *deadline, firstByteDeadlineCoordinator)
					}
					return FirstByteDeadlineActionAbort
				}
			}

			// Key-model admission.
			keyModelAttemptID := "keymodel:" + usageContext.TraceID + ":" + int64ToString(int64(attemptIndex)) + ":" + int64ToString(int64(*in.auditAttemptIndex+1)) + ":" + uuid4String()
			var keyModelAttempt *gatewayaccounteffects.GatewayKeyModelAttempt
			{
				var preparation gatewayaccounteffects.GatewayKeyModelAttemptPreparation
				if in.args.BypassKeyModelAdmission || e.KeyModel == nil {
					preparation = gatewayaccounteffects.GatewayKeyModelAttemptPreparation{Status: gatewayaccounteffects.AttemptPreparationDisabled}
				} else {
					capability := gatewayaccounteffects.GatewayKeyModelCapability{
						AccountID: c.account.ID,
					}
					prepared, prepErr := e.KeyModel.Prepare(ctx, e.KeyModelStore, gatewayaccounteffects.PrepareGatewayKeyModelAttemptInput{
						Route:         capability,
						RequestID:     usageContext.TraceID,
						AttemptID:     keyModelAttemptID,
						FailureBudget: in.keyModelFailureBudget,
					})
					if prepErr != nil {
						c.concurrencySlot.Release()
						reacquireNote()
						*in.lastAttempt = keyModelUnavailableAttempt(c.account, "state_unavailable")
						*c.retryAccountApiKeyRef = true
						return attemptLoopRetryKey, attemptStopNone, nil
					}
					preparation = prepared
				}
				if preparation.Status == gatewayaccounteffects.AttemptPreparationBusy ||
					preparation.Status == gatewayaccounteffects.AttemptPreparationBlocked {
					if c.account.SelectedAPIKeyFingerprint != nil {
						if *c.accountApiKeyAttemptCount > 0 {
							*c.accountApiKeyAttemptCount--
						}
						if preparation.Status == gatewayaccounteffects.AttemptPreparationBlocked {
							c.excludedApiKeyFingerprints[*c.account.SelectedAPIKeyFingerprint] = struct{}{}
						} else {
							delete(c.excludedApiKeyFingerprints, *c.account.SelectedAPIKeyFingerprint)
						}
					}
					c.concurrencySlot.Release()
					reacquireNote()
					*in.lastAttempt = keyModelUnavailableAttempt(c.account, string(preparation.Status))
					auditCapture.AddGatewayMetadata("key_model_foreground_dispatch_skip", map[string]any{
						"accountId":      c.account.ID,
						"keyFingerprint": derefStringPtr(c.account.SelectedAPIKeyFingerprint),
						"capabilityHash": preparation.CapabilityHash,
						"outcome":        string(preparation.Status),
						"wakeSequence":   preparation.WakeSequence,
					})
					*c.retryAccountApiKeyRef = true
					if preparation.Status == gatewayaccounteffects.AttemptPreparationBusy {
						if err := waitForDelayMs(signal, e.Config.KeyModelForegroundQueuePollMs); err != nil {
							return attemptLoopExhausted, attemptStopNone, &UpstreamRequestAbortedError{Message: "请求已取消"}
						}
					}
					return attemptLoopRetryKey, attemptStopNone, nil
				}
				if preparation.Status == gatewayaccounteffects.AttemptPreparationAdmitted {
					keyModelAttempt = preparation.Attempt
				}
				if keyModelAttempt != nil && in.accountCircuitAttempt != nil {
					_, _ = in.accountCircuitAttempt.ReportUnknown(ctx)
					in.accountCircuitAttempt = nil
				}
			}

			// Attempt registration.
			attemptRegistration, regErr := in.coordination.RequestAttemptTracker.TryRecordDispatchAttempt(gatewayrouting.GatewayDispatchAttemptRecordInput{
				GatewayDispatchAttemptIdentity: dispatchAttemptIdentity,
				MatchingConfirmation:           in.accountCircuitAttempt != nil && in.accountCircuitAttempt.IsConfirmation(),
				AllowKeyRotation:               *c.accountApiKeyAttemptCount > 1,
				SemanticRetryID:                in.semanticRetryID,
				SameAccountRetryID:             *in.activeSameAccountRetryID,
			})
			if regErr != nil {
				return attemptLoopExhausted, attemptStopNone, regErr
			}
			if !attemptRegistration.Allowed {
				if keyModelAttempt != nil {
					_ = keyModelAttempt.ReportUnknown(ctx)
				}
				*in.lastAttempt = requestDeduplicatedAttempt(c.account, attemptRegistration.Reason)
				in.failedAccountIDs[c.account.ID] = struct{}{}
				auditCapture.AddGatewayMetadata("gateway_request_attempt_deduplicated", map[string]any{
					"accountId":             c.account.ID,
					"accountRuntimeKey":     accountRuntimeKey,
					"physicalCredentialKey": accountPhysicalCredentialKey(c.account),
					"keyFingerprint":        derefStringPtr(c.account.SelectedAPIKeyFingerprint),
					"reason":                attemptRegistration.Reason,
					"coordinationScope":     in.coordination.Scope,
				})
				return attemptLoopExhausted, attemptStopAccount, nil
			}
			if c.account.SelectedAPIKeyFingerprint != nil {
				*in.requestApiKeyAttemptCount++
			}
			*in.activeSameAccountRetryID = ""

			// Account lock observation.
			if in.accountLockTrafficEnabled {
				lockState, stateErr := e.Locks.FindStateAsync(ctx, c.account.ID)
				if stateErr != nil {
					return attemptLoopExhausted, attemptStopNone, stateErr
				}
				if lockState != nil {
					observation := &AccountLockObservation{
						Generation: lockState.Generation,
						IncidentID: lockState.IncidentID,
					}
					if *in.activeAccountLockRetryLease != nil && (*in.activeAccountLockRetryLease).AccountID == c.account.ID {
						leaseID := (*in.activeAccountLockRetryLease).LeaseID
						observation.LeaseID = &leaseID
					}
					*in.activeAccountLockObservation = observation
				} else {
					*in.activeAccountLockObservation = nil
				}
			}

			*in.auditAttemptIndex++
			auditAttemptID := auditCapture.StartAttempt(StartAttemptInput{
				Account:                   c.account,
				AttemptIndex:              *in.auditAttemptIndex,
				UpstreamURL:               upstreamURL,
				Method:                    in.args.Req.MethodUpper(),
				Headers:                   headerToStringMap(c.headers),
				Body:                      c.body,
				RequestForModelAccounting: in.args.Req,
			})

			hotQualityAttempt := &hotQualityAttemptHandle{input: HotQualityLifecycleInput{
				AttemptID:   "hotq:" + usageContext.TraceID + ":" + int64ToString(int64(attemptIndex)) + ":" + int64ToString(int64(*in.auditAttemptIndex)) + ":" + uuid4String(),
				AccountID:   c.account.ID,
				RequestLane: in.requestLane,
				Model:       requestModelOrEmpty(in.args.Req),
			}}

			if in.coordination.OnUpstreamAttemptStarted != nil {
				in.coordination.OnUpstreamAttemptStarted(c.account, upstreamURL)
			}

			markFirstOutput := func() {
				if keyModelAttempt != nil {
					keyModelAttempt.MarkPrecommit()
				}
				c.concurrencySlot.MarkFirstOutput()
			}

			response, attemptErr := e.PerformUpstreamRequestAttempt(ctx, AttemptInput{
				Req:                        in.args.Req,
				Account:                    c.account,
				UpstreamURL:                upstreamURL,
				AttemptIndex:               attemptIndex,
				AuditAttemptIndex:          *in.auditAttemptIndex,
				Headers:                    c.headers,
				Body:                       c.body,
				TimeoutProfile:             in.timeoutProfile,
				AttemptStartedAt:           attemptStartedAt,
				FirstByteDeadlineMs:        deadlineMsPtr(normalRouteFirstByteDeadline),
				OnFirstByteDeadline:        onFirstByteDeadline,
				Signal:                     signal,
				RequestClientCompatibility: in.args.RequestClientCompatibility,
			})

			if attemptErr == nil {
				kind, stop, err := e.handleUpstreamAttemptResponse(ctx, upstreamAttemptResponseContext{
					// Pointer: the compatibility-recovery branch rewrites
					// loop.body for the semantic retry attempt (Node
					// `body = failedResponseResult.recovery.body; continue`).
					loop:                          &c,
					account:                       c.account,
					response:                      response,
					upstreamURL:                   upstreamURL,
					attemptIndex:                  &attemptIndex,
					attemptStartedAt:              attemptStartedAt,
					auditAttemptID:                auditAttemptID,
					hotQualityAttempt:             hotQualityAttempt,
					keyModelAttempt:               keyModelAttempt,
					normalRouteFirstByteDeadline:  normalRouteFirstByteDeadline,
					firstByteDeadlineCoordinator:  firstByteDeadlineCoordinator,
					firstByteDeadlineTriggeredRef: &firstByteDeadlineTriggered,
					onFirstByteDeadline:           onFirstByteDeadline,
					markFirstOutput:               markFirstOutput,
					dispatchAttemptIdentity:       dispatchAttemptIdentity,
				})
				if err != nil {
					return attemptLoopExhausted, attemptStopNone, err
				}
				if kind == responseKindSelected {
					return attemptLoopSelected, attemptStopNone, nil
				}
				switch stop {
				case responseStopRetrySameAccount:
					attemptIndex++
					continue
				case responseStopRetryKey:
					return attemptLoopRetryKey, attemptStopNone, nil
				case responseStopSkipAccount:
					return attemptLoopExhausted, attemptStopAccount, nil
				}
				attemptIndex++
				continue
			}

			// Error path (Node catch in the attempt loop).
			kind, stop, err := e.handleUpstreamAttemptError(ctx, upstreamAttemptErrorContext{
				loop:                          c,
				account:                       c.account,
				attemptFailure:                attemptErr,
				upstreamURL:                   upstreamURL,
				attemptIndex:                  attemptIndex,
				attemptStartedAt:              attemptStartedAt,
				auditAttemptID:                auditAttemptID,
				hotQualityAttempt:             hotQualityAttempt,
				keyModelAttempt:               keyModelAttempt,
				normalRouteFirstByteDeadline:  normalRouteFirstByteDeadline,
				firstByteDeadlineCoordinator:  firstByteDeadlineCoordinator,
				firstByteDeadlineTriggeredRef: &firstByteDeadlineTriggered,
				onFirstByteDeadline:           onFirstByteDeadline,
				dispatchAttemptIdentity:       dispatchAttemptIdentity,
			})
			if err != nil {
				return attemptLoopExhausted, attemptStopNone, err
			}
			if kind == errorKindRethrow {
				return attemptLoopExhausted, attemptStopNone, stop.rethrown
			}
			switch stop.kind {
			case errorStopSkipAccount:
				return attemptLoopExhausted, attemptStopAccount, nil
			case errorStopRetrySameAccount:
				attemptIndex++
				continue
			case errorStopRetryKey:
				return attemptLoopRetryKey, attemptStopNone, nil
			case errorStopURLBreak:
				break
			}
		}
	}
	return attemptLoopExhausted, attemptStopNone, nil
}

// reacquireNote mirrors reacquireConcurrencyForNextKey = true (the outer
// rotation loop re-acquires the slot before the next key attempt).
func reacquireNote() {}

// upstreamAttemptLoopContext carries the loop state.
type upstreamAttemptLoopContext struct {
	in                         *dispatchSingleAccountInput
	account                    AccountCandidate
	headers                    http.Header
	body                       []byte
	upstreamUrls               []string
	effectiveServiceTier       string
	excludedApiKeyFingerprints map[string]struct{}
	accountApiKeyAttemptCount  *int
	concurrencySlot            *ConcurrencySlot
	halfOpenLease              HalfOpenLease
	keepConcurrencySlotRef     *bool
	pendingApiKeyFailuresRef   *[]PendingAccountApiKeyFailure
	retryAccountApiKeyRef      *bool
	skipAccountRef             *bool
	resultRef                  **UpstreamDispatchResult
	usageContext               *gatewaypreauth.GatewayFailureUsageContext
	auditCapture               AuditCapture
	signal                     context.Context
}

// responseKind / stop tags for handleUpstreamAttemptResponse.
type responseKind int

const (
	responseKindContinue responseKind = iota
	responseKindSelected
)

type responseStopKind int

const (
	responseStopNone responseStopKind = iota
	responseStopRetrySameAccount
	responseStopRetryKey
	responseStopSkipAccount
	responseStopSemanticRetry
)

// upstreamAttemptResponseContext carries the response handling inputs.
type upstreamAttemptResponseContext struct {
	// loop is a pointer: the compatibility-recovery branch rewrites
	// loop.body in place for the retry attempt (Node mutates its local
	// `body` before `continue`).
	loop                          *upstreamAttemptLoopContext
	account                       AccountCandidate
	response                      *GatewayUpstreamResponse
	upstreamURL                   string
	attemptIndex                  *int
	attemptStartedAt              int64
	auditAttemptID                string
	hotQualityAttempt             *hotQualityAttemptHandle
	keyModelAttempt               *gatewayaccounteffects.GatewayKeyModelAttempt
	normalRouteFirstByteDeadline  *gatewayrouting.NormalRouteAttemptFirstByteDeadline
	firstByteDeadlineCoordinator  *NormalRouteFirstByteAttemptCoordinator
	firstByteDeadlineTriggeredRef *bool
	onFirstByteDeadline           FirstByteDeadlineHandler
	markFirstOutput               func()
	dispatchAttemptIdentity       gatewayrouting.GatewayDispatchAttemptIdentity
}

// errorKind / stop tags for handleUpstreamAttemptError.
type errorKind int

const (
	errorKindHandled errorKind = iota
	errorKindRethrow
)

type errorStopKind int

const (
	errorStopNone errorStopKind = iota
	errorStopSkipAccount
	errorStopRetrySameAccount
	errorStopRetryKey
	errorStopURLBreak
)

type errorStop struct {
	kind     errorStopKind
	rethrown error
}

// upstreamAttemptErrorContext carries the error handling inputs.
type upstreamAttemptErrorContext struct {
	loop                          upstreamAttemptLoopContext
	account                       AccountCandidate
	attemptFailure                error
	upstreamURL                   string
	attemptIndex                  int
	attemptStartedAt              int64
	auditAttemptID                string
	hotQualityAttempt             *hotQualityAttemptHandle
	keyModelAttempt               *gatewayaccounteffects.GatewayKeyModelAttempt
	normalRouteFirstByteDeadline  *gatewayrouting.NormalRouteAttemptFirstByteDeadline
	firstByteDeadlineCoordinator  *NormalRouteFirstByteAttemptCoordinator
	firstByteDeadlineTriggeredRef *bool
	onFirstByteDeadline           FirstByteDeadlineHandler
	dispatchAttemptIdentity       gatewayrouting.GatewayDispatchAttemptIdentity
}
