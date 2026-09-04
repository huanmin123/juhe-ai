package gatewaydispatch

import (
	"context"
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// handleUpstreamAttemptResponse + handleUpstreamAttemptError: the response-ok
// / failed-response / error branches of the attempt loop
// (dispatch/upstream-dispatch.ts).

// handleUpstreamAttemptResponse mirrors the `if (response.ok) {...}` block
// plus the failed-response tail of the attempt loop.
func (e *Engine) handleUpstreamAttemptResponse(ctx context.Context, c upstreamAttemptResponseContext) (responseKind, responseStopKind, error) {
	in := c.loop.in
	usageContext := c.loop.usageContext
	auditCapture := c.loop.auditCapture
	response := c.response

	*in.lastAttempt = &UpstreamAttempt{
		AccountID:                 c.account.ID,
		AccountName:               c.account.Name,
		ProviderCode:              c.account.ProviderCode,
		ProviderProtocolProfileID: c.account.ProviderProtocolProfileID,
		ProtocolCode:              c.account.ProtocolCode,
		ProtocolVersion:           c.account.ProtocolVersion,
		UpstreamURL:               c.upstreamURL,
		Status:                    response.Status(),
		HasStatus:                 true,
	}

	if response.OK() {
		e.Affinity.RememberAsync(ctx, in.args.SessionAffinityKey, c.account.ID, AffinityScope{
			SystemAccountID: usageContext.SystemAccountID,
			APIKeyID:        usageContext.APIKeyID,
			GroupID:         usageContext.GroupID,
		})
		*c.loop.keepConcurrencySlotRef = true
		in.setAccountCircuitAttemptTransferred()
		var responsePrecommitDeadlineAtMs *int64
		if in.requestLane != "image" && !in.coordination.GatewayRequestWallBudget.Unbounded {
			value := in.coordination.GatewayRequestWallBudget.DeadlineAtMs - gatewayrouting.DefaultGatewayFinalResponseReserveMs
			responsePrecommitDeadlineAtMs = &value
		}
		accountLockLeaseRelease := in.createAccountLockLeaseRelease(false)
		confirmFailures := append([]PendingAccountApiKeyFailure{}, *c.loop.pendingApiKeyFailuresRef...)
		*c.loop.resultRef = &UpstreamDispatchResult{
			Account:             c.account,
			Response:            response,
			RequestBody:         c.loop.body,
			UpstreamURL:         c.upstreamURL,
			AuditAttemptID:      c.auditAttemptID,
			AttemptStartedAt:    c.attemptStartedAt,
			EffectiveServiceTier: c.loop.effectiveServiceTier,
			TimeoutProfile:      in.timeoutProfile,
			ReleaseConcurrency:  onceFunc(c.loop.concurrencySlot.Release),
			MarkFirstOutput:     c.markFirstOutput,
			ConfirmSameAccountApiKeyFailures: func() error {
				return e.recordConfirmedSameAccountApiKeyFailures(ctx, confirmFailures, c.account, usageContext)
			},
			ConfirmHalfOpenSuccess: func() bool {
				if !in.automaticAccountStateMutationAllowed {
					return false
				}
				return completeHalfOpenLeaseSuccess(ctx, c.loop.halfOpenLease)
			},
			ReleaseHalfOpenLease: func() bool {
				return releaseHalfOpenLease(ctx, c.loop.halfOpenLease)
			},
			HotQualityAttempt:             c.hotQualityAttempt,
			NormalRouteFirstByteDeadline:  c.normalRouteFirstByteDeadline,
			ResponsePrecommitDeadlineAtMs: responsePrecommitDeadlineAtMs,
			OnFirstByteDeadline:           c.onFirstByteDeadline,
			FirstByteDeadlineCoordinator:  c.firstByteDeadlineCoordinator,
			AccountLockObservation:        *in.activeAccountLockObservation,
			AccountLockRetryLease:         *in.activeAccountLockRetryLease,
			ReleaseAccountLockRetryLease:  accountLockLeaseRelease,
		}
		return responseKindSelected, responseStopNone, nil
	}

	// Failed HTTP response path.
	if c.firstByteDeadlineCoordinator != nil {
		c.firstByteDeadlineCoordinator.Supersede()
	}
	failedResponseResult, handleErr := e.FailureDispatcher.HandleFailedUpstreamResponse(ctx, FailedUpstreamResponseInput{
		Req:                          in.args.Req,
		RequestLane:                  in.requestLane,
		UsageContext:                 *usageContext,
		AuditCapture:                 auditCapture,
		AuditAttemptID:               c.auditAttemptID,
		Account:                      c.account,
		UpstreamURL:                  c.upstreamURL,
		Response:                     response,
		RequestBody:                  c.loop.body,
		Settings:                     in.settings,
		AttemptStartedAt:             c.attemptStartedAt,
		AttemptIndex:                 *c.attemptIndex,
		AuditAttemptIndex:            *in.auditAttemptIndex,
		SessionAffinityKey:           in.args.SessionAffinityKey,
		LastAttempt:                  *in.lastAttempt,
		RequestClientCompatibility:   in.args.RequestClientCompatibility,
		ClientIPAccountAvoidance:     in.args.ClientIPAccountAvoidanceTracker,
		AccountStateMutationEnabled:  in.args.AccountStateMutationEnabled,
		AutomaticAccountStateMutationEnabled: in.automaticAccountStateMutationAllowed,
		DeferAutomaticSameAccountKeyRotation: !*c.firstByteDeadlineTriggeredRef &&
			halfOpenLeaseUnclaimed(c.loop.halfOpenLease) &&
			isTransientSameAccountHttpStatus(response.Status()),
	})
	if handleErr != nil {
		auditCapture.CompleteAttempt(c.auditAttemptID, CompleteAttemptInput{
			Success: false, ErrorPhase: "upstream_request", ErrorMessage: handleErr.Error(),
		})
		return responseKindContinue, responseStopNone, handleErr
	}
	explicitPolicyFailure := failedResponseResult.Action != FailedResponseActionReturnResponse &&
		failedResponseResult.FailureKind == FailureKindExplicitPolicy
	if failedResponseResult.Action != FailedResponseActionReturnResponse {
		outcomeClass := HotQualityOutcomeUpstreamResponseFailure
		failureScope := "none"
		source := "upstream_response"
		if explicitPolicyFailure {
			outcomeClass = HotQualityOutcomeExplicitPolicyFailure
			failureScope = "account"
			source = "explicit_policy"
		}
		c.hotQualityAttempt.RecordTerminal(ctx, HotQualityTerminal{
			OutcomeClass: outcomeClass, FailureScope: failureScope, Source: source,
		})
	}
	if failedResponseResult.Action == FailedResponseActionReturnResponse {
		*c.loop.keepConcurrencySlotRef = true
		in.setAccountCircuitAttemptTransferred()
		var responsePrecommitDeadlineAtMs *int64
		if in.requestLane != "image" && !in.coordination.GatewayRequestWallBudget.Unbounded {
			value := in.coordination.GatewayRequestWallBudget.DeadlineAtMs - gatewayrouting.DefaultGatewayFinalResponseReserveMs
			responsePrecommitDeadlineAtMs = &value
		}
		accountLockLeaseRelease := in.createAccountLockLeaseRelease(false)
		*c.loop.resultRef = &UpstreamDispatchResult{
			Account:             c.account,
			Response:            failedResponseResult.Response,
			RequestBody:         c.loop.body,
			UpstreamURL:         c.upstreamURL,
			AuditAttemptID:      c.auditAttemptID,
			AttemptStartedAt:    c.attemptStartedAt,
			EffectiveServiceTier: c.loop.effectiveServiceTier,
			TimeoutProfile:      in.timeoutProfile,
			ReleaseConcurrency:  onceFunc(c.loop.concurrencySlot.Release),
			MarkFirstOutput:     c.markFirstOutput,
			ConfirmSameAccountApiKeyFailures: func() error { return nil },
			ConfirmHalfOpenSuccess: func() bool { return false },
			ReleaseHalfOpenLease: func() bool {
				return releaseHalfOpenLease(ctx, c.loop.halfOpenLease)
			},
			HotQualityAttempt:             c.hotQualityAttempt,
			NormalRouteFirstByteDeadline:  c.normalRouteFirstByteDeadline,
			ResponsePrecommitDeadlineAtMs: responsePrecommitDeadlineAtMs,
			OnFirstByteDeadline:           c.onFirstByteDeadline,
			AccountLockObservation:        *in.activeAccountLockObservation,
			AccountLockRetryLease:         *in.activeAccountLockRetryLease,
			ReleaseAccountLockRetryLease:  accountLockLeaseRelease,
		}
		return responseKindSelected, responseStopNone, nil
	}
	if c.keyModelAttempt != nil {
		_ = c.keyModelAttempt.ReportUnknown(ctx)
	}
	if failedResponseResult.Action == FailedResponseActionRetryWithCompatibilityRecovery {
		c.loop.body = failedResponseResult.Recovery.Body
		in.coordination.SemanticRetryID = failedResponseResult.Recovery.SemanticRetryID
		return responseKindContinue, responseStopSemanticRetry, nil
	}
	if in.accountCircuitAttempt != nil {
		_, _ = in.accountCircuitAttempt.ReportFramingComplete(ctx)
	}
	*in.lastAttempt = failedResponseResult.LastAttempt
	in.failedAccountIDs[c.account.ID] = struct{}{}
	delete(in.recoverableFailedAccountIDs, c.account.ID)
	delete(in.cycleRecoverableAccountIDs, c.account.ID)

	// API-key rotation decision (Node
	// shouldTryAnotherAccountApiKeyForRequest / shouldRetryAnotherAccountApiKey).
	tryNextKey := false
	if failedResponseResult.TryNextApiKeyForRequest {
		tryNextKey = e.shouldTryAnotherAccountApiKeyForRequest(c.account, *c.loop.accountApiKeyAttemptCount, *in.requestApiKeyAttemptCount)
	} else {
		tryNextKey = e.shouldRetryAnotherAccountApiKey(c.account, failedResponseResult.KeyScopedFailure, *c.loop.accountApiKeyAttemptCount, *in.requestApiKeyAttemptCount, auditCapture)
	}
	if !*c.firstByteDeadlineTriggeredRef && halfOpenLeaseUnclaimed(c.loop.halfOpenLease) && tryNextKey {
		if failedResponseResult.PendingApiKeyFailure != nil {
			*c.loop.pendingApiKeyFailuresRef = append(*c.loop.pendingApiKeyFailuresRef, *failedResponseResult.PendingApiKeyFailure)
		}
		*c.loop.retryAccountApiKeyRef = true
		return responseKindContinue, responseStopRetryKey, nil
	}
	if in.requestLane == "text" && !*c.firstByteDeadlineTriggeredRef &&
		halfOpenLeaseUnclaimed(c.loop.halfOpenLease) &&
		failedResponseResult.Action == FailedResponseActionSkipAccount &&
		failedResponseResult.FailureKind != FailureKindExplicitPolicy &&
		isTransientSameAccountHttpStatus(lastStatusOf(failedResponseResult.LastAttempt)) {
		sameAccountRetryID, retryErr := in.reserveSameAccountRetry(c.dispatchAttemptIdentity, "upstream_http_response", c.account.ID)
		if retryErr != nil {
			return responseKindContinue, responseStopNone, retryErr
		}
		if sameAccountRetryID != "" {
			*in.activeSameAccountRetryID = sameAccountRetryID
			return responseKindContinue, responseStopRetrySameAccount, nil
		}
	}
	if in.accountLockTrafficEnabled {
		lockState, stateErr := e.Locks.FindStateAsync(ctx, c.account.ID)
		if stateErr == nil && lockState != nil && accountLockBlocksCrossAccount(*lockState) {
			return responseKindContinue, responseStopSkipAccount, nil
		}
	}
	return responseKindContinue, responseStopSkipAccount, nil
}

// handleUpstreamAttemptError mirrors the `catch (error) {...}` block of the
// attempt loop.
func (e *Engine) handleUpstreamAttemptError(ctx context.Context, c upstreamAttemptErrorContext) (errorKind, errorStop, error) {
	in := c.loop.in
	usageContext := c.loop.usageContext
	auditCapture := c.loop.auditCapture
	signal := c.loop.signal
	err := c.attemptFailure

	var wallErr *GatewayRequestWallBudgetExhaustedError
	if errorsAs(err, &wallErr) {
		return errorKindRethrow, errorStop{kind: errorStopNone, rethrown: err}, nil
	}
	configuredFirstByteDeadline := IsGatewayFirstByteTimeoutError(err) &&
		firstByteTimeoutSourceOf(err) == FirstByteTimeoutSourceConfiguredDeadline
	neutralFirstByteDeadline := configuredFirstByteDeadline &&
		c.normalRouteFirstByteDeadline != nil &&
		(c.normalRouteFirstByteDeadline.LimitingFactor == gatewayrouting.FirstByteLimitingFactorConfigured ||
			c.normalRouteFirstByteDeadline.LimitingFactor == gatewayrouting.FirstByteLimitingFactorWallPrecommit)
	configuredDeadlineCutover := neutralFirstByteDeadline &&
		c.normalRouteFirstByteDeadline.LimitingFactor == gatewayrouting.FirstByteLimitingFactorConfigured
	if !configuredDeadlineCutover && c.firstByteDeadlineCoordinator != nil {
		c.firstByteDeadlineCoordinator.Supersede()
	}
	localRequestFailure := isLocalRequestFailure(err)
	primaryStartedTransportFailure := IsPrimaryStartedGatewayTransportError(err)
	provenBodyTransportFailure := IsProvenUpstreamBodyTransportError(err)
	provenStartedTransportFailure := primaryStartedTransportFailure || provenBodyTransportFailure
	if signal.Err() != nil || *c.firstByteDeadlineTriggeredRef || localRequestFailure || !provenStartedTransportFailure {
		if c.keyModelAttempt != nil {
			_ = c.keyModelAttempt.ReportUnknown(ctx)
		}
	}
	retryAnotherAccountApiKey := false
	{
		failoverAllowed := e.FailureDispatcher.IsOpaqueUpstreamFailoverAllowed(in.args.Req) ||
			(in.requestLane == "text" && in.accountCircuitAttempt != nil && in.accountCircuitAttempt.IsConfirmation())
		retryAnotherAccountApiKey = failoverAllowed && !localRequestFailure &&
			provenStartedTransportFailure && !neutralFirstByteDeadline &&
			signal.Err() == nil && !*c.firstByteDeadlineTriggeredRef &&
			halfOpenLeaseUnclaimed(c.loop.halfOpenLease) &&
			e.shouldTryAnotherAccountApiKeyForRequest(c.account, *c.loop.accountApiKeyAttemptCount, *in.requestApiKeyAttemptCount)
	}
	deferredConfirmationFailure := false
	if retryAnotherAccountApiKey && in.accountCircuitAttempt != nil {
		deferredConfirmationFailure = in.accountCircuitAttempt.DeferConfirmationTransportFailureForKeyRotation()
	}
	confirmedTransportQuality := in.accountCircuitAttempt != nil && in.accountCircuitAttempt.IsConfirmation() &&
		!localRequestFailure && provenStartedTransportFailure && !neutralFirstByteDeadline && !deferredConfirmationFailure
	{
		terminal := HotQualityTerminal{OutcomeClass: HotQualityOutcomeUnknown, FailureScope: "none", Source: "request_lifecycle"}
		if confirmedTransportQuality {
			if signal.Err() != nil {
				terminal = HotQualityTerminal{OutcomeClass: HotQualityOutcomeClientCancellation, Source: "request_lifecycle"}
			} else {
				failure := circuitTransportFailure(err, lastMessageOf(*in.lastAttempt))
				if failure.kind == TransportFailureKindTimeout {
					terminal = HotQualityTerminal{OutcomeClass: HotQualityOutcomeTimeout, FailureScope: "protocol_model", Source: "gateway_transport"}
				} else {
					terminal = HotQualityTerminal{OutcomeClass: HotQualityOutcomeTransportFailure, FailureScope: "protocol_model", Source: "gateway_transport"}
				}
			}
		}
		c.hotQualityAttempt.RecordTerminal(ctx, terminal)
	}
	if signal.Err() != nil {
		_ = e.Affinity.ForgetAsync(ctx, in.args.SessionAffinityKey, c.account.ID)
	}
	var guidanceErr *gatewaypreauth.GatewayAgentGuidanceResponse
	var localErr *gatewaypreauth.GatewayLocalProtocolResponse
	var validationErr *gatewaypreauth.GatewayRequestValidationError
	var adapterErr *OpenAIOAuthCodexAdapterError
	if errorsAs(err, &guidanceErr) && guidanceErr.IsAccountScoped() {
		auditCapture.CompleteAttempt(c.auditAttemptID, CompleteAttemptInput{
			Success: false, ErrorPhase: "request_validation", ErrorMessage: err.Error(),
		})
		*in.lastAttempt = accountScopedGuidanceAttempt(c.account, guidanceErr)
		*in.agentGuidanceResponse = guidanceErr
		in.failedAccountIDs[c.account.ID] = struct{}{}
		return errorKindHandled, errorStop{kind: errorStopSkipAccount}, nil
	}
	if errorsAs(err, &guidanceErr) || errorsAs(err, &localErr) || errorsAs(err, &validationErr) || errorsAs(err, &adapterErr) {
		auditCapture.CompleteAttempt(c.auditAttemptID, CompleteAttemptInput{
			Success: false, ErrorPhase: "request_validation", ErrorMessage: err.Error(),
		})
		return errorKindRethrow, errorStop{kind: errorStopNone, rethrown: err}, nil
	}
	if neutralFirstByteDeadline && c.normalRouteFirstByteDeadline != nil {
		message := err.Error()
		auditCapture.CompleteAttempt(c.auditAttemptID, CompleteAttemptInput{
			Success: false, ErrorPhase: "upstream_request",
			ErrorCode: "normal_route_first_byte_timeout", ErrorMessage: message,
		})
		if e.Usage != nil {
			_ = e.Usage.RecordFailedUpstreamAttempt(ctx, in.args.Req, *usageContext, c.account, FailedAttemptRecord{
				UpstreamURL:               c.upstreamURL,
				StartedAt:                 c.attemptStartedAt,
				ErrorMessage:              message,
				FailureAttribution:        "gateway_policy",
				InterpretUpstreamSemantics: boolPtr(false),
			})
		}
		_ = e.Affinity.ForgetAsync(ctx, in.args.SessionAffinityKey, c.account.ID)
		if in.accountCircuitAttempt != nil {
			_, _ = in.accountCircuitAttempt.ReportUnknown(ctx)
		}
		if c.normalRouteFirstByteDeadline.LimitingFactor == gatewayrouting.FirstByteLimitingFactorWallPrecommit {
			if c.firstByteDeadlineCoordinator != nil {
				c.firstByteDeadlineCoordinator.Supersede()
			}
			return errorKindRethrow, errorStop{kind: errorStopNone, rethrown: &GatewayRequestWallBudgetExhaustedError{
				WallRemainingMs: in.coordination.GatewayRequestWallBudget.RemainingMs(NowMs()),
				BudgetKind:      WallBudgetKindWall,
			}}, nil
		}
		var cutoverReservation *SpeedFirstCutoverReservationView
		if c.firstByteDeadlineCoordinator != nil {
			cutoverReservation = c.firstByteDeadlineCoordinator.TransferForCutover()
		}
		return errorKindRethrow, errorStop{kind: errorStopNone, rethrown: &NormalRouteFirstByteCutoverError{
			AccountID:          c.account.ID,
			AccountName:        c.account.Name,
			Deadline:           *c.normalRouteFirstByteDeadline,
			Message:            message,
			CutoverReservation: cutoverReservation,
		}}, nil
	}
	if signal.Err() != nil && shouldRecordAbortedUpstreamAttempt(err) {
		if _, handleErr := e.FailureDispatcher.HandleUpstreamRequestError(ctx, UpstreamRequestErrorInput{
			Req:                         in.args.Req,
			UsageContext:                *usageContext,
			AuditCapture:                auditCapture,
			AuditAttemptID:              c.auditAttemptID,
			Account:                     c.account,
			UpstreamURL:                 c.upstreamURL,
			AttemptStartedAt:            c.attemptStartedAt,
			AttemptIndex:                c.attemptIndex,
			AuditAttemptIndex:           *in.auditAttemptIndex,
			Settings:                    in.settings,
			SessionAffinityKey:          in.args.SessionAffinityKey,
			LastAttempt:                 *in.lastAttempt,
			FailedProxyDispatchKeys:     in.failedProxyDispatchKeys,
			Error:                       err,
			ClientIPAccountAvoidance:    in.args.ClientIPAccountAvoidanceTracker,
			AccountStateMutationEnabled: in.automaticAccountStateMutationAllowed,
		}); handleErr != nil {
			return errorKindHandled, errorStop{}, handleErr
		}
		return errorKindRethrow, errorStop{kind: errorStopNone, rethrown: err}, nil
	}
	if !provenStartedTransportFailure {
		auditCapture.CompleteAttempt(c.auditAttemptID, CompleteAttemptInput{
			Success: false, ErrorPhase: "gateway_local_dispatch",
			ErrorCode: "unproven_upstream_transport_failure", ErrorMessage: err.Error(),
		})
		auditCapture.AddGatewayMetadata("gateway_unproven_upstream_transport_failure", map[string]any{
			"accountId":      c.account.ID,
			"keyFingerprint": derefStringPtr(c.account.SelectedAPIKeyFingerprint),
			"requestLane":    in.requestLane,
			"endpoint":       usageContext.Endpoint,
		})
		var unsafeErr *UnsafeResolvedUpstreamURLError
		if in.args.AccountStateMutationEnabled && usageContext.TrafficSource == "gateway" && errorsAs(err, &unsafeErr) && e.AccountState != nil {
			marked, markErr := e.AccountState.MarkTemporaryUnavailableWithCacheInvalidation(ctx, c.account,
				"上游 Base URL 的 DNS 解析命中本机、内网、链路本地或保留地址，已临时停止调度",
				"unsafe_resolved_upstream_url")
			auditCapture.AddGatewayMetadata("gateway_unsafe_resolved_upstream_url_account_temporary_unavailable", map[string]any{
				"accountId":                 c.account.ID,
				"markedTemporaryUnavailable": marked,
			})
			if markErr != nil {
				return errorKindHandled, errorStop{}, markErr
			}
		}
		if in.accountCircuitAttempt != nil {
			_, _ = in.accountCircuitAttempt.ReportUnknown(ctx)
		}
		return errorKindRethrow, errorStop{kind: errorStopNone, rethrown: err}, nil
	}
	requestErrorResult, handleErr := e.FailureDispatcher.HandleUpstreamRequestError(ctx, UpstreamRequestErrorInput{
		Req:                         in.args.Req,
		UsageContext:                *usageContext,
		AuditCapture:                auditCapture,
		AuditAttemptID:              c.auditAttemptID,
		Account:                     c.account,
		UpstreamURL:                 c.upstreamURL,
		AttemptStartedAt:            c.attemptStartedAt,
		AttemptIndex:                c.attemptIndex,
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
		return errorKindHandled, errorStop{}, handleErr
	}
	if in.accountLockTrafficEnabled {
		_ = e.Locks.RecordFailureAsync(ctx, c.account.ID, "upstream_transport_failure", *in.activeAccountLockObservation)
		_ = e.Locks.SettleDeadlineAsync(ctx, c.account.ID, NowMs(), *in.activeAccountLockObservation)
	}
	if requestErrorResult.LastAttempt != nil {
		*in.lastAttempt = requestErrorResult.LastAttempt
	}
	if requestErrorResult.Action == FailedResponseActionSkipAccount && !retryAnotherAccountApiKey {
		if *in.lastAttempt != nil {
			failure := circuitTransportFailure(err, (*in.lastAttempt).Message)
			if failure.kind == TransportFailureKindTimeout {
				(*in.lastAttempt).TransportFailureKind = TransportFailureKindTimeout
			} else {
				(*in.lastAttempt).TransportFailureKind = TransportFailureKindConnection
			}
		}
		if in.accountCircuitAttempt != nil && signal.Err() == nil && !deferredConfirmationFailure {
			failure := circuitTransportFailure(err, lastMessageOf(*in.lastAttempt))
			_, _ = in.accountCircuitAttempt.ReportTransportFailure(ctx, gatewaycircuit.TransportFailure{Kind: failure.kind, Reason: failure.reason})
		}
		if in.requestLane == "text" && provenStartedTransportFailure && !neutralFirstByteDeadline &&
			!*c.firstByteDeadlineTriggeredRef && halfOpenLeaseUnclaimed(c.loop.halfOpenLease) {
			sameAccountRetryID, retryErr := in.reserveSameAccountRetry(c.dispatchAttemptIdentity, "upstream_transport_failure", c.account.ID)
			if retryErr != nil {
				return errorKindHandled, errorStop{}, retryErr
			}
			if sameAccountRetryID != "" {
				if c.keyModelAttempt != nil {
					_ = c.keyModelAttempt.ReportUnknown(ctx)
				}
				*in.activeSameAccountRetryID = sameAccountRetryID
				return errorKindHandled, errorStop{kind: errorStopRetrySameAccount}, nil
			}
		}
		in.failedAccountIDs[c.account.ID] = struct{}{}
		if in.accountLockTrafficEnabled {
			lockState, stateErr := e.Locks.FindStateAsync(ctx, c.account.ID)
			if stateErr == nil && lockState != nil && accountLockBlocksCrossAccount(*lockState) {
				return errorKindHandled, errorStop{kind: errorStopSkipAccount}, nil
			}
		}
		if c.keyModelAttempt != nil {
			_ = c.keyModelAttempt.ReportUpstreamNotComplete(ctx)
		}
		return errorKindHandled, errorStop{kind: errorStopSkipAccount}, nil
	}
	if !retryAnotherAccountApiKey {
		in.failedAccountIDs[c.account.ID] = struct{}{}
	}
	if in.accountCircuitAttempt != nil && signal.Err() == nil && !deferredConfirmationFailure {
		failure := circuitTransportFailure(err, lastMessageOf(*in.lastAttempt))
		_, _ = in.accountCircuitAttempt.ReportTransportFailure(ctx, gatewaycircuit.TransportFailure{Kind: failure.kind, Reason: failure.reason})
	}
	if retryAnotherAccountApiKey {
		if c.keyModelAttempt != nil {
			_ = c.keyModelAttempt.ReportUpstreamNotComplete(ctx)
		}
		if shouldRetainTransportFailureForRecovery(c.upstreamURL, signal) &&
			c.account.SelectedAPIKeyFingerprint != nil &&
			!c.account.APIKeyRuntimeStateDisabled &&
			len(c.account.APIKeys) > *c.loop.accountApiKeyAttemptCount &&
			*in.requestApiKeyAttemptCount < e.Config.AccountApiKeyRequestAttemptSafetyLimit &&
			e.APIKeyEffects != nil {
			observationEpoch := e.APIKeyEffects.CaptureFailureObservation(c.account)
			*c.loop.pendingApiKeyFailuresRef = append(*c.loop.pendingApiKeyFailuresRef, PendingAccountApiKeyFailure{
				Account:          c.account,
				Status:           "temporary_unavailable",
				ObservationEpoch: observationEpoch,
				ErrorMessage:     err.Error(),
			})
		}
		*c.loop.retryAccountApiKeyRef = true
		return errorKindHandled, errorStop{kind: errorStopRetryKey}, nil
	}
	return errorKindHandled, errorStop{kind: errorStopSkipAccount}, nil
}

func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}
