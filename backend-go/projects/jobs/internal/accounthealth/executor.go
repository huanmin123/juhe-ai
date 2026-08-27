package accounthealth

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

const healthKeyCursorPurpose = "health_check"

// Key-cursor persistence is durable scheduling state, not part of the
// upstream probe deadline.  A probe can legitimately exhaust its context
// while the owner lease is still valid; give the cursor write a small bounded
// window of its own.  SaveKeyCursor still verifies the owner lease inside the
// write transaction, so detaching cancellation cannot let a stale worker
// mutate state.
const keyCursorPersistenceTimeout = 5 * time.Second

// ExecuteInputProbe runs exactly one request against the immutable input. The
// caller owns scheduling and outcome persistence; this function never calls a
// service or mutates a business database.
func ExecuteInputProbe(ctx context.Context, store *Store, lease OwnerLease, input Input, request ProbeRequest, options ProbeOptions) (Outcome, error) {
	if strings.TrimSpace(request.RequestID) == "" || request.AccountID != input.AccountID || request.InputVersion != input.InputVersion || request.ConfigRevision != input.ConfigRevision || request.DispatchRevision != input.DispatchRevision {
		return newOutcome(input, request, ProbeResult{Outcome: OutcomeTaskFailed, ErrorCode: "request_fence_invalid", ErrorMessage: "请求与 input fence 不匹配"}, nil, now(options)), nil
	}
	if !request.Deadline.IsZero() && !request.Deadline.After(now(options)) {
		return newOutcome(input, request, ProbeResult{Outcome: OutcomeTaskFailed, ErrorCode: "request_deadline_elapsed", ErrorMessage: "探活请求已过期"}, nil, now(options)), nil
	}
	if input.Type == "oauth" || input.Type == "google_oauth" {
		if input.OAuthAccess == nil {
			return newOutcome(input, request, ProbeResult{Outcome: OutcomeTaskFailed, ErrorCode: "oauth_access_missing", ErrorMessage: "OAuth access token 缺失"}, nil, now(options)), nil
		}
		return newOutcome(input, request, ProbeOpenAI(ctx, input, *input.OAuthAccess, options), nil, now(options)), nil
	}
	if len(input.APIKeys) == 0 || strings.TrimSpace(input.KeySetFingerprint) == "" {
		return newOutcome(input, request, ProbeResult{Outcome: OutcomeTaskFailed, ErrorCode: "api_key_pool_missing", ErrorMessage: "API Key pool 缺失"}, nil, now(options)), nil
	}
	start, found, err := store.LoadKeyCursor(ctx, input.AccountID, healthKeyCursorPurpose, input.KeySetFingerprint)
	if err != nil {
		return Outcome{}, fmt.Errorf("读取 API Key probe cursor 失败: %w", err)
	}
	if !found || start < 0 {
		start = 0
	}
	start %= len(input.APIKeys)
	var last ProbeResult
	for offset := 0; offset < len(input.APIKeys); offset++ {
		index := (start + offset) % len(input.APIKeys)
		key := input.APIKeys[index]
		result := ProbeOpenAI(ctx, input, key.Credential, options)
		if result.Outcome == OutcomeSuccess {
			next := (index + 1) % len(input.APIKeys)
			if err := saveProbeKeyCursor(ctx, store, lease, input.AccountID, input.KeySetFingerprint, next); err != nil {
				return Outcome{}, fmt.Errorf("保存 API Key probe cursor 失败: %w", err)
			}
			return newOutcome(input, request, result, &index, now(options)), nil
		}
		last = result
	}
	next := (start + 1) % len(input.APIKeys)
	if err := saveProbeKeyCursor(ctx, store, lease, input.AccountID, input.KeySetFingerprint, next); err != nil {
		return Outcome{}, fmt.Errorf("保存 API Key probe cursor 失败: %w", err)
	}
	return newOutcome(input, request, last, nil, now(options)), nil
}

func saveProbeKeyCursor(ctx context.Context, store *Store, lease OwnerLease, accountID, fingerprint string, nextIndex int) error {
	// The cursor advances only after an attempt has completed.  It must not be
	// discarded merely because the probe context reached its deadline.  The
	// owner lease is checked by SaveKeyCursor, and remains the authoritative
	// fail-closed fence for this detached, bounded persistence context.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keyCursorPersistenceTimeout)
	defer cancel()
	return store.SaveKeyCursor(persistCtx, lease, accountID, healthKeyCursorPurpose, fingerprint, nextIndex)
}

func newOutcome(input Input, request ProbeRequest, result ProbeResult, winner *int, observedAt time.Time) Outcome {
	winnerFingerprint := ""
	if winner != nil && *winner >= 0 && *winner < len(input.APIKeys) {
		winnerFingerprint = input.APIKeys[*winner].Fingerprint
	}
	return Outcome{
		OutcomeID:            newOutcomeID(),
		RequestID:            request.RequestID,
		AccountID:            input.AccountID,
		Outcome:              result.Outcome,
		ObservedAt:           observedAt.UTC(),
		InputVersion:         input.InputVersion,
		ConfigRevision:       input.ConfigRevision,
		DispatchRevision:     input.DispatchRevision,
		StatusCode:           result.StatusCode,
		ErrorCode:            result.ErrorCode,
		ErrorMessage:         result.ErrorMessage,
		WinnerIndex:          winner,
		SourceFence:          request.SourceFence,
		KeyModelFence:        request.KeyModelFence,
		WinnerKeyFingerprint: winnerFingerprint,
	}
}

func now(options ProbeOptions) time.Time {
	if options.Now != nil {
		return options.Now().UTC()
	}
	return time.Now().UTC()
}

func newOutcomeID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("outcome-fallback-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("outcome-%x", value[:])
}
