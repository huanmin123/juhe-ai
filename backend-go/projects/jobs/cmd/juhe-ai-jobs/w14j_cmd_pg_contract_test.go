// 波次 w14j：PG 覆盖库契约冒烟。w1cover 库经一次性加法幂等 DDL 补建了
// J1/J3a 生产形状表（juhe_business.system_teams/resource_authorization_grants/
// account_health_jobs_input_versions/account_health_jobs_input_outbox/
// account_health_projection_cursors/account_health_projection_receipts 与
// juhe_jobs.proxy_latency_* 六表两索引，DDL 摘自 maintenance 权威定义），
// 这里先在进程内验证 CheckContract/CheckSchema 语义通过，为子进程 main()
// PG e2e 提供前置证据。连接串只从 shared.env 读，不写入日志与断言。
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/keymodelrecovery"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// w14jSharedEnvValue 从 shared.env 读取指定键值；找不到返回空串。值绝不打印。
func w14jSharedEnvValue(t *testing.T, key string) string {
	t.Helper()
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	candidates := []string{
		filepath.Join("..", "..", "..", "..", ".local", "project-resources", "dev", "env", "shared.env"),
		filepath.FromSlash("F:/sub2api-lite/.local/project-resources/dev/env/shared.env"),
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if value, ok := strings.CutPrefix(line, key+"="); ok {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

// w14jOpenPG 直接以 pgx 驱动打开覆盖库连接（门控失败 t.Skip）。
func w14jOpenPG(t *testing.T) *sql.DB {
	t.Helper()
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w14j: 覆盖库连接串不可用")
	}
	db, err := sql.Open("pgx", pgURL)
	if err != nil {
		t.Skipf("w14j: 打开覆盖库失败（跳过）: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("w14j: 覆盖库不可达（跳过）: %v", err)
	}
	return db
}

// TestW14JPGContractSmoke 进程内验证 J1 direct input / J3a jobs+input+
// management / J2 service 的生产契约预检在覆盖库上通过。
func TestW14JPGContractSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控契约冒烟 skipped in -short mode")
	}
	db := w14jOpenPG(t)
	pgURL := w12cPGOverrideURL(t)
	ctx := context.Background()

	// 与 w12dSeedSettings 同惯例：幂等补齐 direct schedule canonical 设置行
	//（ON CONFLICT DO NOTHING，不改既有值）。
	for key, value := range map[string]string{
		"accountHealthCheckIntervalHours":      "24",
		"accountHealthCheckJitterMinutes":      "10",
		"accountHealthCheckFailureThreshold":   "3",
		"defaultTemporaryUnschedulableMinutes": "60",
		"cooldownAccountRetestMaxBackoffHours": "24",
		"usageStatsTimezone":                   "\"Asia/Shanghai\"",
	} {
		if _, err := db.Exec(`INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
			VALUES ('sys_admin', $1, $2, $3) ON CONFLICT (system_account_id, key) DO NOTHING`, key, value, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("seed direct schedule settings: %v", err)
		}
	}

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

	// j3a 直连/管理面契约在 w1cover 上结构性不可达：覆盖库 proxy_profiles/
	// providers 的 enabled 列是前波最小形状 text 型，而候选 SQL 需要
	// boolean/integer 比较（42883）；列类型改写属破坏性 schema 变更，被禁止。
	// 这里只验证该失败以原始 PG 错误呈现（fail closed 语义）。
	t.Run("j3a-direct-input-contract-blocked-by-shared-shape", func(t *testing.T) {
		reader, err := proxylatency.NewPostgresDirectInputReader(db, time.Minute, time.Now)
		if err != nil {
			t.Fatalf("构造 J3a reader: %v", err)
		}
		if err := reader.CheckContract(ctx); err == nil {
			t.Fatalf("J3a direct input CheckContract 意外通过；覆盖库形状已变化，请重新评估 J3a e2e 可行性")
		} else if !strings.Contains(err.Error(), "SQLSTATE") {
			t.Fatalf("J3a direct input 契约失败必须保留原始 PG 错误: %v", err)
		}
	})

	t.Run("j2-service", func(t *testing.T) {
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
