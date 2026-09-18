package accounthealth

// w13g5_health_store_arms_test.go 用注入 kit 覆盖存储层语句级 err 传播臂。
// 每个子测试使用独立注入库：失败注入后 sql.Tx 标记 done，驱动级事务仅随
// 连接释放，独立库可彻底避免连接级事务残留。

import (
	"context"
	"testing"
	"time"
)

// w13g5HealthLeaseFixture 建表并取得真实租约。
func w13g5HealthLeaseFixture(t *testing.T) (*Store, OwnerLease, *w13g5HealthSpec) {
	t.Helper()
	store, spec := w13g5HealthInjectStore(t)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w13g5-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %v %v", acquired, err)
	}
	return store, lease, spec
}

func TestW13g5HealthAppendOutcomeStatementArms(t *testing.T) {
	cases := []struct {
		name    string
		match   string
		prepare func(t *testing.T, store *Store)
	}{
		{"verifyLease", "SET updated_at=updated_at", nil},
		{"outcome insert", "INSERT INTO account_health_outcomes", nil},
		{"current state write", "INSERT INTO account_health_current_state", nil},
		{"commit", "w13g5-COMMIT", nil},
		{"begin", "w13g5-BEGIN", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, lease, spec := w13g5HealthLeaseFixture(t)
			if tc.prepare != nil {
				tc.prepare(t, store)
			}
			spec.armOnce(tc.match)
			defer spec.disarm()
			if _, err := store.AppendOutcome(context.Background(), lease, w13g5HealthOutcome()); err == nil {
				t.Fatalf("%s 失败必须传播", tc.name)
			}
		})
	}
}

func TestW13g5HealthAppendOutcomeDuplicateArms(t *testing.T) {
	ctx := context.Background()
	// 先成功写入一次，再注入 duplicate-match 查询失败。
	store, lease, spec := w13g5HealthLeaseFixture(t)
	if _, err := store.AppendOutcome(ctx, lease, w13g5HealthOutcome()); err != nil {
		t.Fatal(err)
	}
	spec.armOnce("FROM account_health_outcomes WHERE request_id=?")
	if _, err := store.AppendOutcome(ctx, lease, w13g5HealthOutcome()); err == nil {
		t.Fatal("duplicate 查询失败必须传播")
	}
	// 注入解除后重复写入幂等返回 false。
	if inserted, err := store.AppendOutcome(ctx, lease, w13g5HealthOutcome()); err != nil || inserted {
		t.Fatalf("重复写入必须幂等: %v %v", inserted, err)
	}
}

func TestW13g5HealthLeaseStatementArms(t *testing.T) {
	ctx := context.Background()
	t.Run("acquire", func(t *testing.T) {
		store, spec := w13g5HealthInjectStore(t)
		if err := store.EnsureSchema(ctx); err != nil {
			t.Fatal(err)
		}
		spec.armOnce("INSERT INTO account_health_owner_leases")
		if _, _, err := store.AcquireOwnerLease(ctx, "w13g5-owner", time.Minute); err == nil {
			t.Fatal("租约 INSERT 失败必须传播")
		}
	})
	t.Run("acquire 未到期", func(t *testing.T) {
		store, _, _ := w13g5HealthLeaseFixture(t)
		// 现有租约未到期 → RETURNING 空 → false, nil。
		if _, acquired, err := store.AcquireOwnerLease(ctx, "w13g5-owner-2", time.Minute); err != nil || acquired {
			t.Fatalf("未到期租约必须返回 false: %v %v", acquired, err)
		}
	})
	t.Run("renew", func(t *testing.T) {
		store, lease, spec := w13g5HealthLeaseFixture(t)
		spec.armOnce("SET lease_until=?")
		if _, err := store.RenewOwnerLease(ctx, lease, time.Minute); err == nil {
			t.Fatal("续约失败必须传播")
		}
	})
	t.Run("release", func(t *testing.T) {
		store, lease, spec := w13g5HealthLeaseFixture(t)
		spec.armOnce("DELETE FROM account_health_owner_leases")
		if err := store.ReleaseOwnerLease(ctx, lease); err == nil {
			t.Fatal("释放失败必须传播")
		}
	})
}

func TestW13g5HealthEnsureSchemaArms(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		match string
	}{
		{"schema 锁", "BEGIN IMMEDIATE"},
		{"schema 执行", "CREATE TABLE IF NOT EXISTS account_health_outcomes"},
		{"列扩展探测", "PRAGMA table_info(account_health_current_state)"},
		{"schema 提交", "COMMIT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, spec := w13g5HealthInjectStore(t)
			spec.armOnce(tc.match)
			defer spec.disarm()
			if err := store.EnsureSchema(ctx); err == nil {
				t.Fatalf("%s 失败必须传播", tc.name)
			}
		})
	}
}

func TestW13g5HealthReadArms(t *testing.T) {
	ctx := context.Background()
	t.Run("LoadCurrentState", func(t *testing.T) {
		store, lease, spec := w13g5HealthLeaseFixture(t)
		if _, err := store.AppendOutcome(ctx, lease, w13g5HealthOutcome()); err != nil {
			t.Fatal(err)
		}
		spec.armOnce("FROM account_health_current_state WHERE account_id")
		defer spec.disarm()
		if _, _, err := store.LoadCurrentState(ctx, "w13g5-acc"); err == nil {
			t.Fatal("状态读取失败必须传播")
		}
	})
	t.Run("LoadCurrentState 缺行", func(t *testing.T) {
		store, _, _ := w13g5HealthLeaseFixture(t)
		if _, found, err := store.LoadCurrentState(ctx, "w13g5-missing"); err != nil || found {
			t.Fatalf("缺行必须返回 false: %v %v", found, err)
		}
	})
	t.Run("LoadDirectInputSuppressions", func(t *testing.T) {
		store, _, spec := w13g5HealthLeaseFixture(t)
		spec.armOnce("FROM account_health_direct_input_suppressions")
		defer spec.disarm()
		if _, err := store.LoadDirectInputSuppressions(ctx, time.Now()); err == nil {
			t.Fatal("抑制读取失败必须传播")
		}
	})
	t.Run("HasRequest", func(t *testing.T) {
		store, _, spec := w13g5HealthLeaseFixture(t)
		spec.armOnce("SELECT 1 FROM account_health_outcomes WHERE request_id=?")
		defer spec.disarm()
		if _, err := store.HasRequest(ctx, "w13g5-request"); err == nil {
			t.Fatal("HasRequest 失败必须传播")
		}
	})
	t.Run("LoadKeyCursor", func(t *testing.T) {
		store, _, spec := w13g5HealthLeaseFixture(t)
		spec.armOnce("FROM account_health_key_cursors")
		defer spec.disarm()
		if _, _, err := store.LoadKeyCursor(ctx, "w13g5-acc", "w13g5-purpose", "w13g5-fp"); err == nil {
			t.Fatal("cursor 读取失败必须传播")
		}
	})
	t.Run("SaveKeyCursor", func(t *testing.T) {
		store, lease, spec := w13g5HealthLeaseFixture(t)
		spec.armOnce("INSERT INTO account_health_key_cursors")
		defer spec.disarm()
		if err := store.SaveKeyCursor(ctx, lease, "w13g5-acc", "w13g5-purpose", "w13g5-fp", 1); err == nil {
			t.Fatal("cursor 写入失败必须传播")
		}
	})
}

func TestW13g5HealthDirectInputSuppressionWriteArms(t *testing.T) {
	ctx := context.Background()
	future := time.Now().Add(time.Hour)
	base := func() Outcome {
		outcome := w13g5HealthOutcome()
		outcome.Outcome = OutcomeTaskFailed
		outcome.ErrorCode = "direct_input_invalid"
		outcome.NextDueAt = &future
		return outcome
	}
	t.Run("抑制写入", func(t *testing.T) {
		store, lease, spec := w13g5HealthLeaseFixture(t)
		spec.armOnce("INSERT INTO account_health_direct_input_suppressions")
		defer spec.disarm()
		if _, err := store.AppendOutcome(ctx, lease, base()); err == nil {
			t.Fatal("抑制写入失败必须传播")
		}
	})
	t.Run("抑制写入成功", func(t *testing.T) {
		store, lease, _ := w13g5HealthLeaseFixture(t)
		if _, err := store.AppendOutcome(ctx, lease, base()); err != nil {
			t.Fatal(err)
		}
		suppressions, err := store.LoadDirectInputSuppressions(ctx, time.Now())
		if err != nil || len(suppressions) != 1 {
			t.Fatalf("抑制必须可读: %+v %v", suppressions, err)
		}
	})
}

func TestW13g5HealthStaleLeaseArms(t *testing.T) {
	store, lease, _ := w13g5HealthLeaseFixture(t)
	// 过代 fence token → ErrOwnerLeaseLost。
	stale := OwnerLease{OwnerID: lease.OwnerID, FenceToken: lease.FenceToken + 99}
	if _, err := store.AppendOutcome(context.Background(), stale, w13g5HealthOutcome()); err == nil {
		t.Fatal("过代租约必须报错")
	}
}
