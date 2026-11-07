package auditlog

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLeaseKeysMultipleInstancesExternal 验证多副本写入 lease：不同实例键
// 可以同时持有；旧全局键仍可被 retention 独立持有。需要真实 PostgreSQL：
// JUHE_AI_LEASE_TEST_DSN 指向一次性测试库。
func TestLeaseKeysMultipleInstancesExternal(t *testing.T) {
	dsn := os.Getenv("JUHE_AI_LEASE_TEST_DSN")
	if dsn == "" {
		t.Skip("JUHE_AI_LEASE_TEST_DSN 未设置，跳过真实 PostgreSQL 租约集成测试")
	}
	open := func(instanceID, leaseKey string) (Store, func()) {
		cfg := Config{
			InstanceID:           instanceID,
			OwnerLease:           30 * time.Second,
			PostgresURL:          dsn,
			PostgresMaxOpenConns: 5,
			PostgresMaxIdleConns: 2,
			PayloadBlobDirectory: t.TempDir(),
			LeaseKey:             leaseKey,
		}
		store, err := OpenStore(cfg)
		if err != nil {
			t.Fatalf("OpenStore(%s): %v", instanceID, err)
		}
		if err := store.EnsureSchema(context.Background()); err != nil {
			store.Close()
			t.Fatalf("EnsureSchema(%s): %v", instanceID, err)
		}
		return store, func() { _ = store.Close() }
	}
	storeA, closeA := open("inst-a", LeaseKeyForInstance("inst-a"))
	defer closeA()
	storeB, closeB := open("inst-b", LeaseKeyForInstance("inst-b"))
	defer closeB()
	storeR, closeR := open("retention", DefaultLeaseKey)
	defer closeR()

	ctx := context.Background()
	leaseA, okA, errA := storeA.AcquireOwnerLease(ctx, "inst-a", 30*time.Second)
	if errA != nil || !okA {
		t.Fatalf("写实例 A 获取租约失败: ok=%v err=%v", okA, errA)
	}
	leaseB, okB, errB := storeB.AcquireOwnerLease(ctx, "inst-b", 30*time.Second)
	if errB != nil || !okB {
		t.Fatalf("写实例 B 获取租约失败: ok=%v err=%v", okB, errB)
	}
	if leaseA.OwnerID == leaseB.OwnerID {
		t.Fatal("两个实例的 owner 不应相同")
	}
	leaseR, okR, errR := storeR.AcquireOwnerLease(ctx, "retention", 30*time.Second)
	if errR != nil || !okR {
		t.Fatalf("retention 全局租约获取失败: ok=%v err=%v", okR, errR)
	}
	if err := storeA.ReleaseOwnerLease(ctx, leaseA); err != nil {
		t.Fatalf("释放 A: %v", err)
	}
	if err := storeB.ReleaseOwnerLease(ctx, leaseB); err != nil {
		t.Fatalf("释放 B: %v", err)
	}
	if err := storeR.ReleaseOwnerLease(ctx, leaseR); err != nil {
		t.Fatalf("释放 retention: %v", err)
	}
}
