package managementauth

import (
	"context"
	"errors"
	"strings"
	"testing"

	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

func TestLoginGuardScopesHashSensitiveValues(t *testing.T) {
	scopes := LoginGuardScopes("203.0.113.10", "Admin@Example.test")
	if len(scopes) != 2 {
		t.Fatalf("scopes length = %d, want 2", len(scopes))
	}
	for _, scope := range scopes {
		for _, raw := range []string{"203.0.113.10", "Admin", "admin@example.test"} {
			if strings.Contains(scope.CounterKey, raw) || strings.Contains(scope.LockKey, raw) {
				t.Fatalf("scope leaks raw value: %+v", scope)
			}
		}
	}
	if scopes[0].Threshold != LoginGuardIPThreshold || scopes[1].Threshold != LoginGuardUsernameThreshold {
		t.Fatalf("scopes = %+v", scopes)
	}
}

func TestLoginGuardCheckAllowedMapsIPAndUsernameBlocks(t *testing.T) {
	store := &loginGuardStoreStub{
		checkDecision: redisplatform.FailureLockDecision{Allowed: false, RetryAfterSeconds: 12, BlockedIndex: 1},
	}
	service := NewLoginGuardService(store)

	decision, err := service.CheckAllowed(context.Background(), "203.0.113.10", "admin")
	if err != nil {
		t.Fatalf("CheckAllowed() error = %v", err)
	}
	if !decision.Blocked || decision.Message != LoginGuardIPBlockedMessage || decision.RetryAfterSeconds != 12 {
		t.Fatalf("decision = %+v", decision)
	}

	store.checkDecision = redisplatform.FailureLockDecision{Allowed: false, RetryAfterSeconds: 7, BlockedIndex: 2}
	decision, err = service.CheckAllowed(context.Background(), "203.0.113.10", "admin")
	if err != nil {
		t.Fatalf("CheckAllowed() username error = %v", err)
	}
	if !decision.Blocked || decision.Message != LoginGuardUsernameBlockedMessage || decision.RetryAfterSeconds != 7 {
		t.Fatalf("username decision = %+v", decision)
	}
}

func TestLoginGuardRecordFailedUsesAtomicStore(t *testing.T) {
	store := &loginGuardStoreStub{
		recordDecision: redisplatform.FailureLockDecision{Allowed: false, RetryAfterSeconds: 900, BlockedIndex: 1},
	}
	service := NewLoginGuardService(store)

	decision, err := service.RecordFailed(context.Background(), "203.0.113.10", "admin")
	if err != nil {
		t.Fatalf("RecordFailed() error = %v", err)
	}
	if !decision.Blocked || decision.Message != LoginGuardIPBlockedMessage {
		t.Fatalf("decision = %+v", decision)
	}
	if len(store.recordScopes) != 2 {
		t.Fatalf("record scopes = %+v", store.recordScopes)
	}
}

func TestLoginGuardRecordSuccessDeletesCountersAndLocks(t *testing.T) {
	store := &loginGuardStoreStub{}
	service := NewLoginGuardService(store)

	if err := service.RecordSuccess(context.Background(), "203.0.113.10", "Admin"); err != nil {
		t.Fatalf("RecordSuccess() error = %v", err)
	}
	if len(store.deletedKeys) != 4 {
		t.Fatalf("deleted keys = %#v, want 4", store.deletedKeys)
	}
	for _, key := range store.deletedKeys {
		if strings.Contains(key, "203.0.113.10") || strings.Contains(key, "Admin") {
			t.Fatalf("deleted key leaks raw value: %q", key)
		}
	}
}

func TestLoginGuardRecordSuccessReturnsDeleteErrors(t *testing.T) {
	store := &loginGuardStoreStub{deleteErr: errors.New("redis down")}
	service := NewLoginGuardService(store)

	err := service.RecordSuccess(context.Background(), "203.0.113.10", "admin")
	if !errors.Is(err, store.deleteErr) {
		t.Fatalf("RecordSuccess() error = %v, want delete error", err)
	}
}

type loginGuardStoreStub struct {
	checkScopes   []redisplatform.FailureLockScope
	checkDecision redisplatform.FailureLockDecision
	checkErr      error

	recordScopes   []redisplatform.FailureLockScope
	recordDecision redisplatform.FailureLockDecision
	recordErr      error

	deletedKeys []string
	deleteErr   error
}

func (s *loginGuardStoreStub) CheckFailureLocks(_ context.Context, scopes []redisplatform.FailureLockScope) (redisplatform.FailureLockDecision, error) {
	s.checkScopes = append([]redisplatform.FailureLockScope(nil), scopes...)
	if s.checkErr != nil {
		return redisplatform.FailureLockDecision{}, s.checkErr
	}
	if s.checkDecision == (redisplatform.FailureLockDecision{}) {
		return redisplatform.FailureLockDecision{Allowed: true}, nil
	}
	return s.checkDecision, nil
}

func (s *loginGuardStoreStub) RecordFailureWithLock(_ context.Context, scopes []redisplatform.FailureLockScope) (redisplatform.FailureLockDecision, error) {
	s.recordScopes = append([]redisplatform.FailureLockScope(nil), scopes...)
	if s.recordErr != nil {
		return redisplatform.FailureLockDecision{}, s.recordErr
	}
	if s.recordDecision == (redisplatform.FailureLockDecision{}) {
		return redisplatform.FailureLockDecision{Allowed: true}, nil
	}
	return s.recordDecision, nil
}

func (s *loginGuardStoreStub) Delete(_ context.Context, key string) error {
	s.deletedKeys = append(s.deletedKeys, key)
	return s.deleteErr
}
