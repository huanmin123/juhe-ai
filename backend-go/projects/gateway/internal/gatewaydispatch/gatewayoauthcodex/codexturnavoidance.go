package gatewayoauthcodex

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Codex turn (client source) avoidance dispatch filters: the Go engine
// internalizes the Node routes.ts dispatch loop, so the avoided-account filter
// and the last-resort reversal live here instead of the route layer. Node
// references: routes.ts:911-917 (the per-attempt filter), 1060-1075 +
// 1454-1465 (the last-resort reversal audited as
// client_source_avoided_accounts_last_resort), 1425-1433 (exhausted =
// failedAccountIds minus recoverableAccountIds) and 651 (the fallback group
// switch resets the reversal).

// StringSet builds an id set.
func StringSet(ids []string) map[string]struct{} {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// FilterCodexTurnAvoidedAccounts keeps the avoided accounts when reversed and
// the fresh (non-avoided) accounts otherwise (routes.ts:911-917 ternary).
func FilterCodexTurnAvoidedAccounts(accounts []gatewayruntimecache.OpenAIAccountSecret, avoided map[string]struct{}, reversed bool) []gatewayruntimecache.OpenAIAccountSecret {
	filtered := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(accounts))
	for _, account := range accounts {
		_, isAvoided := avoided[account.ID]
		// Keep the avoided accounts only when reversed (routes.ts:911-917
		// ternary); drop them from the normal pass.
		if isAvoided != reversed {
			continue
		}
		filtered = append(filtered, account)
	}
	return filtered
}

// CodexTurnReversalCandidates returns the avoided accounts that were not
// exhaustively failed: recoverable failures keep the account retryable
// (routes.ts:1425-1433 exhausted semantics), so they stay reversal-eligible.
func CodexTurnReversalCandidates(accounts []gatewayruntimecache.OpenAIAccountSecret, avoided map[string]struct{}, exhausted map[string]struct{}) []gatewayruntimecache.OpenAIAccountSecret {
	candidates := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(accounts))
	for _, account := range accounts {
		if _, isAvoided := avoided[account.ID]; !isAvoided {
			continue
		}
		if _, isExhausted := exhausted[account.ID]; isExhausted {
			continue
		}
		candidates = append(candidates, account)
	}
	return candidates
}

// NonRecoverableFailedAccountIDs mirrors the Node exhausted-set fill: failed
// accounts minus the recoverable ones.
func NonRecoverableFailedAccountIDs(failed, recoverable map[string]struct{}) map[string]struct{} {
	exhausted := make(map[string]struct{}, len(failed))
	for id := range failed {
		if _, isRecoverable := recoverable[id]; isRecoverable {
			continue
		}
		exhausted[id] = struct{}{}
	}
	return exhausted
}
