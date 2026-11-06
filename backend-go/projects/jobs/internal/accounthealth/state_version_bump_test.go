package accounthealth

// B4 账户运行态版本栅栏：投影事务提交成功后递增共享序号；失败只告警，读者
// 按既有 60s 窗口回退。

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeAccountStateVersionBumper struct {
	mu  sync.Mutex
	ids []string
	err error
}

func (f *fakeAccountStateVersionBumper) BumpAccountState(_ context.Context, accountID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, accountID)
	return f.err
}

func TestProjectionAppliedBumpsAccountStateVersion(t *testing.T) {
	db, _ := w9hOpenKitDB(t)
	w35SeedFrozenAccountDB(t, db, w35Generation)
	outcome := w35RevivalOutcome("state-version-bump")
	business, err := NewProjectionBusinessDB(db)
	if err != nil {
		t.Fatal(err)
	}
	bumper := &fakeAccountStateVersionBumper{}
	projector, err := NewOutcomeProjector(&Store{}, OutcomeProjectorConfig{
		Business:         business,
		CredentialSecret: "w35-fence-guard-secret",
		Logger:           nil,
		Now:              func() time.Time { return outcome.ObservedAt.Add(time.Second) },
		StateVersion:     bumper,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := projector.projectOutcome(context.Background(), outcome)
	if err != nil {
		t.Fatalf("projectOutcome: %v", err)
	}
	if result.Disposition != ProjectionApplied {
		t.Fatalf("disposition = %s, want applied", result.Disposition)
	}
	bumper.mu.Lock()
	defer bumper.mu.Unlock()
	if len(bumper.ids) != 1 || bumper.ids[0] != w35AccountID {
		t.Fatalf("bumps = %v, want [%s]", bumper.ids, w35AccountID)
	}
}
