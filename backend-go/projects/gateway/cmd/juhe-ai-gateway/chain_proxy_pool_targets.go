package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-platform/proxyresolve"
)

// chainPoolOverrideTTL covers the runtime cache revalidate/retain window so a
// request-time lease repair cannot be undone by an older cached member URL
// (proxy design §10.2). It is process-local, not a cross-instance channel.
const chainPoolOverrideTTL = 3 * time.Minute

type chainProxyPoolOverride struct {
	// cachedMemberID is the cache entry that existed when the override was
	// created. Once the runtime cache refreshes to a different member (new
	// lease or admin change), the override is stale and must yield.
	cachedMemberID string
	poolID         string
	profileID      string
	memberID       string
	url            string
	expiresAt      time.Time
}

// chainProxyPoolTargetResolver is the dispatch-preparation adapter over the
// shared resolver with a gateway lease writer (proxy design §10.1). It only
// acts on pool-bound candidates; direct-proxy and unbound candidates pass
// through unchanged. Successful in-request reassignments are remembered
// locally so subsequent requests do not fall back to the stale cached member.
type chainProxyPoolTargetResolver struct {
	resolver  chainProxyTargetResolver
	logger    *slog.Logger
	now       func() time.Time
	mu        sync.Mutex
	overrides map[string]chainProxyPoolOverride
}

func newChainProxyPoolTargetResolver(resolver chainProxyTargetResolver, logger *slog.Logger) *chainProxyPoolTargetResolver {
	if resolver == nil {
		return nil
	}
	return &chainProxyPoolTargetResolver{
		resolver: resolver, logger: logger, now: time.Now,
		overrides: map[string]chainProxyPoolOverride{},
	}
}

func (r *chainProxyPoolTargetResolver) ResolveAccountProxyTarget(
	ctx context.Context,
	account gatewaydispatch.AccountCandidate,
) (gatewaydispatch.AccountCandidate, error) {
	if r == nil || r.resolver == nil || account.ProxyPoolID == nil ||
		strings.TrimSpace(*account.ProxyPoolID) == "" {
		return account, nil
	}
	cachedPoolID := strings.TrimSpace(*account.ProxyPoolID)
	cachedMemberID := stringValueOf(account.ProxyMemberProfileID)
	if override, ok := r.lookupOverride(account.ID, cachedPoolID, cachedMemberID); ok {
		state, healthSource, healthErr := r.resolver.CheckMemberHealth(ctx, override.memberID)
		if healthErr == nil && state != proxyresolve.HealthDead {
			account = chainApplyPoolTarget(account, override.poolID, override.profileID, override.memberID, override.url)
			return account, nil
		}
		if healthErr != nil {
			r.warn("池成员健康读取失败", account, "health_source", healthSource, "error", healthErr)
			return chainProxyPoolUnavailableAccount(account, "代理池当前不可用，将在下一次请求重新分配"), nil
		}
		r.clearOverride(account.ID)
	}
	// 热路径（代理池设计 §7.4）：缓存成员 URL 可用时只读该成员的 Redis 健康
	// 键；healthy/degraded/unknown 直接复用缓存 URL，不查 PG。只有 dead 或
	// Redis 缺失且 PG 也 dead 时才进入完整解析/重分配。
	if memberID := stringValueOf(account.ProxyMemberProfileID); memberID != "" &&
		account.ProxyURL != nil && strings.TrimSpace(*account.ProxyURL) != "" {
		state, healthSource, healthErr := r.resolver.CheckMemberHealth(ctx, memberID)
		if healthErr != nil {
			r.warn("池成员健康读取失败", account, "health_source", healthSource, "error", healthErr)
			return chainProxyPoolUnavailableAccount(account, "代理池当前不可用，将在下一次请求重新分配"), nil
		}
		if state != proxyresolve.HealthDead {
			return account, nil
		}
		r.warn("池成员健康 dead，进入请求内重分配", account, "health_source", healthSource,
			"member_id", memberID)
	}
	targets, err := r.resolver.ResolveBatch(ctx, []string{account.ID})
	if err != nil {
		// A store failure is fail-closed for pool accounts: never fall back to
		// a direct dial or a stale member URL (proxy design §7.4).
		r.warn("池代理目标解析失败", account, "resolve_error", err)
		return chainProxyPoolUnavailableAccount(account, "代理池当前不可用，将在下一次请求重新分配"), nil
	}
	target, ok := targets[account.ID]
	if !ok || !target.Available() || target.Kind == proxyresolve.KindDirect || strings.TrimSpace(target.URL) == "" {
		message := strings.TrimSpace(target.UnavailableMessage)
		if message == "" {
			message = "代理池当前不可用，将在下一次请求重新分配"
		}
		r.warn("池代理目标不可用", account, "pool_reason", target.UnavailableCode, "message", message)
		r.clearOverride(account.ID)
		return chainProxyPoolUnavailableAccount(account, message), nil
	}
	poolID := target.PoolID
	if poolID == "" {
		poolID = strings.TrimSpace(*account.ProxyPoolID)
	}
	profileID := target.ProfileID
	memberID := target.MemberProfileID
	if memberID == "" {
		memberID = profileID
	}
	r.storeOverride(account.ID, chainProxyPoolOverride{
		cachedMemberID: cachedMemberID, poolID: poolID, profileID: profileID, memberID: memberID, url: target.URL,
	})
	account = chainApplyPoolTarget(account, poolID, profileID, memberID, target.URL)
	if target.Assigned && r.logger != nil {
		r.logger.Info("请求时修复代理池租约", "account_id", account.ID,
			"pool_id", poolID, "old_member", target.PreviousProfileID,
			"new_member", profileID, "assignment_reason", target.AssignmentReason,
			"generation", target.Generation, "health_source", target.HealthSource)
	}
	return account, nil
}

func chainApplyPoolTarget(
	account gatewaydispatch.AccountCandidate,
	poolID, profileID, memberID, proxyURL string,
) gatewaydispatch.AccountCandidate {
	account.ProxyPoolID = &poolID
	account.ProxyProfileID = &profileID
	account.ProxyMemberProfileID = &memberID
	account.ProxyURL = &proxyURL
	account.ProxyProfileUnavailable = nil
	account.ProxyProfileErrorMessage = nil
	return account
}

func (r *chainProxyPoolTargetResolver) lookupOverride(accountID, poolID, cachedMemberID string) (chainProxyPoolOverride, bool) {
	if r == nil {
		return chainProxyPoolOverride{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	override, ok := r.overrides[accountID]
	if !ok {
		return chainProxyPoolOverride{}, false
	}
	if override.poolID != poolID || override.cachedMemberID != cachedMemberID ||
		!r.clock().Before(override.expiresAt) {
		delete(r.overrides, accountID)
		return chainProxyPoolOverride{}, false
	}
	return override, true
}

func (r *chainProxyPoolTargetResolver) storeOverride(accountID string, override chainProxyPoolOverride) {
	if r == nil || accountID == "" {
		return
	}
	override.expiresAt = r.clock().Add(chainPoolOverrideTTL)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.overrides == nil {
		r.overrides = map[string]chainProxyPoolOverride{}
	}
	r.overrides[accountID] = override
}

func (r *chainProxyPoolTargetResolver) clearOverride(accountID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.overrides, accountID)
}

func (r *chainProxyPoolTargetResolver) clock() time.Time {
	if r != nil && r.now != nil {
		return r.now()
	}
	return time.Now()
}

func chainProxyPoolUnavailableAccount(
	account gatewaydispatch.AccountCandidate,
	message string,
) gatewaydispatch.AccountCandidate {
	account.ProxyProfileID = nil
	account.ProxyMemberProfileID = nil
	account.ProxyURL = nil
	unavailable := true
	account.ProxyProfileUnavailable = &unavailable
	account.ProxyProfileErrorMessage = &message
	return account
}

func (r *chainProxyPoolTargetResolver) warn(message string, account gatewaydispatch.AccountCandidate, extra ...any) {
	if r == nil || r.logger == nil {
		return
	}
	fields := []any{
		"account_id", account.ID,
		"pool_id", stringValueOf(account.ProxyPoolID),
		"pool_reason", proxyresolve.CodeProxyPoolUnavailable,
	}
	fields = append(fields, extra...)
	r.logger.Warn(message, fields...)
}

// chainProxyPasswordDecrypter reads the proxy_profiles.password_encrypted
// envelope with the same account secret used by the rest of the gateway.
type chainProxyPasswordDecrypter struct {
	secret string
}

func (d chainProxyPasswordDecrypter) DecryptProxyPassword(_ context.Context, encrypted string) (string, error) {
	var envelope struct {
		Password *string `json:"password"`
	}
	if err := accounts.DecryptJSON(d.secret, encrypted, &envelope); err != nil {
		return "", err
	}
	if envelope.Password == nil {
		return "", errors.New("代理密码缺失")
	}
	return *envelope.Password, nil
}
