package managementauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

const (
	LoginGuardWindow            = 10 * time.Minute
	LoginGuardLock              = 15 * time.Minute
	LoginGuardIPThreshold       = 10
	LoginGuardUsernameThreshold = 10

	LoginGuardIPBlockedMessage       = "尝试过于频繁，请稍后再试"
	LoginGuardUsernameBlockedMessage = "账号暂时锁定，请稍后再试"
)

type LoginGuardStore interface {
	CheckFailureLocks(ctx context.Context, scopes []redisplatform.FailureLockScope) (redisplatform.FailureLockDecision, error)
	RecordFailureWithLock(ctx context.Context, scopes []redisplatform.FailureLockScope) (redisplatform.FailureLockDecision, error)
	Delete(ctx context.Context, key string) error
}

type LoginGuardDecision struct {
	Blocked           bool
	Message           string
	RetryAfterSeconds int
}

type LoginGuardService struct {
	store LoginGuardStore
}

func NewLoginGuardService(store LoginGuardStore) *LoginGuardService {
	return &LoginGuardService{store: store}
}

func (s *LoginGuardService) CheckAllowed(ctx context.Context, clientIP string, username string) (LoginGuardDecision, error) {
	if s == nil || s.store == nil {
		return LoginGuardDecision{}, errors.New("management auth login guard store is required")
	}
	decision, err := s.store.CheckFailureLocks(ctx, LoginGuardScopes(clientIP, username))
	if err != nil {
		return LoginGuardDecision{}, err
	}
	return loginGuardDecisionFromRedis(decision), nil
}

func (s *LoginGuardService) RecordFailed(ctx context.Context, clientIP string, username string) (LoginGuardDecision, error) {
	if s == nil || s.store == nil {
		return LoginGuardDecision{}, errors.New("management auth login guard store is required")
	}
	decision, err := s.store.RecordFailureWithLock(ctx, LoginGuardScopes(clientIP, username))
	if err != nil {
		return LoginGuardDecision{}, err
	}
	return loginGuardDecisionFromRedis(decision), nil
}

func (s *LoginGuardService) RecordSuccess(ctx context.Context, clientIP string, username string) error {
	if s == nil || s.store == nil {
		return errors.New("management auth login guard store is required")
	}
	var errs []error
	for _, key := range LoginGuardKeys(clientIP, username) {
		if err := s.store.Delete(ctx, key); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func LoginGuardScopes(clientIP string, username string) []redisplatform.FailureLockScope {
	ipHash := LoginGuardScopeHash("ip", normalizeLoginClientIP(clientIP))
	usernameHash := LoginGuardScopeHash("username", NormalizeLoginUsername(username))
	return []redisplatform.FailureLockScope{
		{
			CounterKey: "auth_login_guard:ip:" + ipHash + ":count",
			LockKey:    "auth_login_guard:ip:" + ipHash + ":lock",
			Threshold:  LoginGuardIPThreshold,
			Window:     LoginGuardWindow,
			Lock:       LoginGuardLock,
		},
		{
			CounterKey: "auth_login_guard:username:" + usernameHash + ":count",
			LockKey:    "auth_login_guard:username:" + usernameHash + ":lock",
			Threshold:  LoginGuardUsernameThreshold,
			Window:     LoginGuardWindow,
			Lock:       LoginGuardLock,
		},
	}
}

func LoginGuardKeys(clientIP string, username string) []string {
	scopes := LoginGuardScopes(clientIP, username)
	return []string{
		scopes[0].CounterKey,
		scopes[0].LockKey,
		scopes[1].CounterKey,
		scopes[1].LockKey,
	}
}

func NormalizeLoginUsername(username string) string {
	text := strings.ToLower(strings.TrimSpace(username))
	if text == "" {
		return "unknown"
	}
	return text
}

func LoginGuardScopeHash(scope string, value string) string {
	sum := sha256.Sum256([]byte("auth_login_guard\x00" + scope + "\x00" + value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func normalizeLoginClientIP(clientIP string) string {
	text := strings.TrimSpace(clientIP)
	if text == "" {
		return "unknown"
	}
	return text
}

func loginGuardDecisionFromRedis(decision redisplatform.FailureLockDecision) LoginGuardDecision {
	if decision.Allowed {
		return LoginGuardDecision{}
	}
	message := LoginGuardUsernameBlockedMessage
	if decision.BlockedIndex == 1 {
		message = LoginGuardIPBlockedMessage
	}
	retryAfter := decision.RetryAfterSeconds
	if retryAfter <= 0 {
		retryAfter = int(LoginGuardLock / time.Second)
	}
	return LoginGuardDecision{
		Blocked:           true,
		Message:           message,
		RetryAfterSeconds: retryAfter,
	}
}
