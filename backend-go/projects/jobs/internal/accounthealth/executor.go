package accounthealth

import (
	"context"
	"crypto/rand"
	"fmt"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"log/slog"
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
func ExecuteInputProbe(ctx context.Context, store *Store, lease OwnerLease, input exactkeyprobe.Input, request ProbeRequest, options exactkeyprobe.ProbeOptions) (Outcome, error) {
	if strings.TrimSpace(request.RequestID) == "" || request.AccountID != input.AccountID || request.InputVersion != input.InputVersion || request.ConfigRevision != input.ConfigRevision || request.DispatchRevision != input.DispatchRevision {
		return newOutcome(input, request, exactkeyprobe.ProbeResult{Outcome: exactkeyprobe.OutcomeTaskFailed, ErrorCode: "request_fence_invalid", ErrorMessage: "请求与 input fence 不匹配"}, nil, now(options)), nil
	}
	if !request.Deadline.IsZero() && !request.Deadline.After(now(options)) {
		return newOutcome(input, request, exactkeyprobe.ProbeResult{Outcome: exactkeyprobe.OutcomeTaskFailed, ErrorCode: "request_deadline_elapsed", ErrorMessage: "探活请求已过期"}, nil, now(options)), nil
	}
	if input.Type == "oauth" || input.Type == "google_oauth" {
		if input.OAuthAccess == nil {
			return newOutcome(input, request, exactkeyprobe.ProbeResult{Outcome: exactkeyprobe.OutcomeTaskFailed, ErrorCode: "oauth_access_missing", ErrorMessage: "OAuth access token 缺失"}, nil, now(options)), nil
		}
		return newOutcome(input, request, exactkeyprobe.ProbeOpenAI(ctx, input, *input.OAuthAccess, options), nil, now(options)), nil
	}
	if len(input.APIKeys) == 0 || strings.TrimSpace(input.KeySetFingerprint) == "" {
		return newOutcome(input, request, exactkeyprobe.ProbeResult{Outcome: exactkeyprobe.OutcomeTaskFailed, ErrorCode: "api_key_pool_missing", ErrorMessage: "API Key pool 缺失"}, nil, now(options)), nil
	}
	start, found, err := store.LoadKeyCursor(ctx, input.AccountID, healthKeyCursorPurpose, input.KeySetFingerprint)
	if err != nil {
		return Outcome{}, fmt.Errorf("读取 API Key probe cursor 失败: %w", err)
	}
	if !found || start < 0 {
		start = 0
	}
	start %= len(input.APIKeys)
	var last exactkeyprobe.ProbeResult
	for offset := 0; offset < len(input.APIKeys); offset++ {
		index := (start + offset) % len(input.APIKeys)
		key := input.APIKeys[index]
		for _, timeout := range probeTimeoutLadder(options.Timeout) {
			attemptOptions := options
			attemptOptions.Timeout = timeout
			result := exactkeyprobe.ProbeOpenAI(ctx, input, key.Credential, attemptOptions)
			if result.Outcome == exactkeyprobe.OutcomeSuccess {
				next := (index + 1) % len(input.APIKeys)
				// Cursor 轮询记账是 best-effort：保存失败只降级为 warn 日志，
				// 不否决探针业务结果（J1 迁移契约 §5）。
				_ = saveProbeKeyCursor(ctx, store, lease, input.AccountID, input.KeySetFingerprint, next)
				return newOutcome(input, request, result, &index, now(options)), nil
			}
			last = result
			if result.ErrorCode != "upstream_timeout" {
				break
			}
		}
	}
	next := (start + 1) % len(input.APIKeys)
	// 全部 Key 失败时同样先尽力保存 cursor：失败降级为日志，业务 Outcome
	// （而非 cursor 错误）照常返回给调用方落库。
	_ = saveProbeKeyCursor(ctx, store, lease, input.AccountID, input.KeySetFingerprint, next)
	return newOutcome(input, request, last, nil, now(options)), nil
}

func probeTimeoutLadder(configured time.Duration) []time.Duration {
	// 2026-10-07 与 accountprobe.DiagnosticRetryTimeouts 同源上调（原
	// 10s/20s/30s）；configured 低于档位时按预算截断阶梯。
	const (
		first  = 20 * time.Second
		second = 30 * time.Second
		third  = 40 * time.Second
	)
	if configured <= 0 || configured >= third {
		return []time.Duration{first, second, third}
	}
	if configured <= first {
		return []time.Duration{configured}
	}
	if configured <= second {
		return []time.Duration{first, configured}
	}
	return []time.Duration{first, second, configured}
}

func saveProbeKeyCursor(ctx context.Context, store *Store, lease OwnerLease, accountID, fingerprint string, nextIndex int) error {
	// The cursor advances only after an attempt has completed.  It must not be
	// discarded merely because the probe context reached its deadline.  The
	// owner lease is checked by SaveKeyCursor, and remains the authoritative
	// fail-closed fence for this detached, bounded persistence context.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keyCursorPersistenceTimeout)
	defer cancel()
	if err := store.SaveKeyCursor(persistCtx, lease, accountID, healthKeyCursorPurpose, fingerprint, nextIndex); err != nil {
		// The cursor is round-robin bookkeeping ("which key to try first next
		// round"), never business evidence.  Losing it only restarts rotation
		// at key 0, so a save failure is downgraded to a warn log and must not
		// veto the probe outcome returned to the caller.
		slog.Warn("保存 API Key probe cursor 失败；cursor 仅为轮询记账，降级为日志，不影响本轮探针结果",
			"event", "account_health_key_cursor_save_failed",
			"account_id", accountID,
			"purpose", healthKeyCursorPurpose,
			"next_index", nextIndex,
			"error", err)
		return err
	}
	return nil
}

func newOutcome(input exactkeyprobe.Input, request ProbeRequest, result exactkeyprobe.ProbeResult, winner *int, observedAt time.Time) Outcome {
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

func now(options exactkeyprobe.ProbeOptions) time.Time {
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
