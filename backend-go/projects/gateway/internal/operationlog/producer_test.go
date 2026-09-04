package operationlog

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSafeChangeSensitiveRedaction(t *testing.T) {
	change := SafeChange("password", "登录密码", "old-secret", "new-secret", true)
	if !change.Sensitive {
		t.Fatal("sensitive flag missing")
	}
	if change.Before != "已设置" {
		t.Fatalf("sensitive before = %v", change.Before)
	}
	if change.After != "已变更" {
		t.Fatalf("sensitive after = %v", change.After)
	}
	if strings.Contains(change.Before.(string), "secret") || strings.Contains(change.After.(string), "secret") {
		t.Fatal("sensitive values leaked")
	}
}

func TestSafeChangeStringTruncation(t *testing.T) {
	long := strings.Repeat("x", 300)
	change := SafeChange("displayName", "用户名称", long, "short", false)
	if !strings.HasSuffix(change.Before.(string), "...") {
		t.Fatal("long before value must be truncated with ellipsis")
	}
	if len(change.Before.(string)) > 206 {
		t.Fatalf("truncation length wrong: %d", len(change.Before.(string)))
	}
	if change.After != "short" {
		t.Fatalf("after = %v", change.After)
	}
}

func TestProducerPersistsAndSwallowsErrors(t *testing.T) {
	fake := &fakeStore{}
	producer := NewProducer(fake, OwnerLease{}, Config{InstanceID: "test"}, nil)

	producer.Record(Input{
		ActorSystemAccountID: "sysacc_1",
		ActorRole:            "admin",
		Module:               "system_accounts",
		Action:               "create",
		OperationKey:         "system_accounts.create",
		ResourceType:         "system_account",
		Summary:              "创建系统账户",
		CreatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fake.persisted() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fake.persisted() != 1 {
		t.Fatalf("expected 1 persisted entry, got %d", fake.persisted())
	}

	// Errors are swallowed: persist failure never panics or propagates.
	fake.failPersists = true
	producer.Record(Input{ActorSystemAccountID: "x", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fake.persistAttempts() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type fakeStore struct {
	mu            sync.Mutex
	persistCount  int
	failPersists  bool
	renewAccepted bool
}

func (f *fakeStore) persistAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.persistCount
}

func (f *fakeStore) persisted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.persistCount
}

func (f *fakeStore) EnsureSchema(context.Context) error { return nil }

func (f *fakeStore) AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error) {
	return OwnerLease{}, true, nil
}

func (f *fakeStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewAccepted = true
	return true, nil
}

func (f *fakeStore) ReleaseOwnerLease(context.Context, OwnerLease) error { return nil }

func (f *fakeStore) Persist(ctx context.Context, lease OwnerLease, input Input) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.persistCount++
	if f.failPersists {
		return false, context.DeadlineExceeded
	}
	return true, nil
}

func (f *fakeStore) List(context.Context, ListOptions) (ListResult, error) {
	return ListResult{}, nil
}

func (f *fakeStore) Detail(context.Context, string, string) (DetailSupplement, bool, error) {
	return DetailSupplement{}, false, nil
}

func (f *fakeStore) CleanupRetention(context.Context, OwnerLease, time.Time, int) (int64, error) {
	return 0, nil
}

func (f *fakeStore) RetentionDays(context.Context, int) (int, error) { return 365, nil }

func (f *fakeStore) Close() error { return nil }
