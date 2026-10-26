// 波次 w14j：PG 契约冒烟。建库走 wgCmdKitDB 的 pgtestkit 隔离 PG 测试库
// （J1/J3a 生产形状表经权威 DDL 提取 + w14j 幂等清单预置，原 w1cover 共享
// 覆盖库门禁已退役），进程内验证 CheckContract/CheckSchema 语义通过，为子
// 进程 main() PG e2e 提供前置证据。连接串不写入日志与断言。
package main

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/keymodelrecovery"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

// TestW14JPGContractSmoke 进程内验证 J1 direct input / J3a jobs+input+
// management / J2 service 的生产契约预检在隔离测试库上通过。
func TestW14JPGContractSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控契约冒烟 skipped in -short mode")
	}
	db, pgURL := wgCmdKitDB(t)
	ctx := context.Background()

	t.Run("j1-direct-input-contract", func(t *testing.T) {
		reader, err := accounthealth.NewPostgresDirectInputReader(db, "0123456789abcdef0123456789abcdef", time.Minute, time.Now)
		if err != nil {
			t.Fatalf("构造 J1 reader: %v", err)
		}
		if err := reader.CheckContract(ctx); err != nil {
			t.Fatalf("J1 direct input CheckContract: %v", err)
		}
	})

	t.Run("j3a-jobs-store-schema", func(t *testing.T) {
		store, err := proxylatency.OpenStore(proxylatency.StoreConfig{Mode: proxylatency.StorePostgres, PostgresURL: pgURL, PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2})
		if err != nil {
			t.Fatalf("打开 J3a jobs store: %v", err)
		}
		defer func() { _ = store.Close() }()
		if err := store.CheckSchema(ctx); err != nil {
			t.Fatalf("J3a jobs store CheckSchema: %v", err)
		}
	})

	t.Run("j3a-direct-input-contract", func(t *testing.T) {
		reader, err := proxylatency.NewPostgresDirectInputReader(db, time.Minute, time.Now)
		if err != nil {
			t.Fatalf("构造 J3a reader: %v", err)
		}
		if err := reader.CheckContract(ctx); err != nil {
			t.Fatalf("J3a direct input CheckContract: %v", err)
		}
	})

	t.Run("j2-service", func(t *testing.T) {
		// J2 交接表预置：accountbalance EnsureSchema 是幂等 DDL（CheckSchema
		// 只读不建表，w14q/w16i 同款 bootstrap 前置）。
		bootstrap, err := accountbalance.OpenStore(accountbalance.StoreConfig{Mode: accountbalance.StorePostgres, PostgresURL: pgURL})
		if err != nil {
			t.Fatalf("打开 J2 store 做 schema bootstrap: %v", err)
		}
		if err := bootstrap.EnsureSchema(ctx); err != nil {
			_ = bootstrap.Close()
			t.Fatalf("J2 schema bootstrap: %v", err)
		}
		if err := bootstrap.Close(); err != nil {
			t.Logf("w14j: J2 bootstrap store 关闭: %v", err)
		}
		service, err := accountbalance.NewService(accountbalance.RuntimeConfig{
			Enabled:              true,
			OwnerID:              "w14j-contract-smoke",
			Store:                accountbalance.StoreConfig{Mode: accountbalance.StorePostgres, PostgresURL: pgURL},
			BusinessPostgresURL:  pgURL,
			CredentialSecret:     "0123456789abcdef0123456789abcdef",
			InputTTL:             time.Minute,
			Now:                  time.Now,
			PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2,
			InputPostgresMaxOpenConns: 4, InputPostgresMaxIdleConns: 2,
		}, nil)
		if err != nil {
			t.Fatalf("J2 NewService: %v", err)
		}
		defer func() { _ = service.Close() }()
	})

	t.Run("model-recovery-redis-config", func(t *testing.T) {
		redisURL := w14jSharedEnvValue(t, "JUHE_AI_REDIS_STATE_URL")
		if redisURL == "" {
			t.Skip("w14j: shared.env 无 Redis state URL")
		}
		store, err := keymodelrecovery.OpenRedisStore(keymodelrecovery.RedisConfig{URL: redisURL, Namespace: "w14j", Enabled: true})
		if err != nil {
			t.Fatalf("打开 model-recovery Redis store: %v", err)
		}
		defer func() { _ = store.Close() }()
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := store.Ping(pingCtx); err != nil {
			t.Fatalf("model-recovery Redis Ping: %v", err)
		}
	})
}
