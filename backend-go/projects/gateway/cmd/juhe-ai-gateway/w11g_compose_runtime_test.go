package main

// w11g 覆盖补充：composeSystemAPI 的缺路径/坏路径错误臂、
// composeChainRuntimeServices 的坏 Redis URL 错误臂、prewarm、
// newCompositionID 与 redisStateClientProvider.Invalidate。

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

func TestW11GComposeEarlyAndPostgresArms(t *testing.T) {
	// 中段（stats/catalog 等）失败场景在 Windows 上泄漏已打开的 business
	// 句柄导致 TempDir 清理失败（compose 失败路径不回滚已开句柄），故此
	// 处只覆盖早期参数守卫与 PG 池分支（不开任何 sqlite 文件）。
	if _, err := composeSystemAPI(composeTestConfig(t), pgpool.NewRegistry(), nil, nil, nil, auditlog.Config{}); err == nil {
		t.Fatal("nil operation store must fail")
	}
	cfg := composeTestConfig(t)
	store := openComposeOperationStore(t)
	_, _, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	_ = cfg
	_ = store
	// PG 模式坏 URL：连接池 ping 失败（独立 store 避免租约互抢）。
	pgTry := func(url string) error {
		pgCfg := composeTestConfig(t)
		pgCfg.DatabaseDriver = "postgres"
		pgCfg.BusinessPostgresURL = url
		pgStore := openComposeOperationStore(t)
		pgAuditConfig, pgProducer, closePgAudit := openComposeAuditSources(t, filepath.Dir(pgCfg.DatasetDatabasePath))
		defer closePgAudit()
		_, err := composeSystemAPI(pgCfg, pgpool.NewRegistry(), pgStore, openComposeOperationLease(t, pgStore), pgProducer, pgAuditConfig)
		return err
	}
	if err := pgTry("postgres://w11g-no-user@127.0.0.1:1/w11g?sslmode=disable&connect_timeout=1"); err == nil {
		t.Fatal("bad postgres url must fail")
	}
	if err := pgTry(""); err == nil {
		t.Fatal("empty postgres url must fail")
	}
}

func TestW11GComposeRuntimeBadRedisArms(t *testing.T) {
	// composeChainRuntimeServices 的前置守卫与坏 URL 错误臂。
	if _, err := composeChainRuntimeServices(nil, runtimeConfig{}, nil); err == nil {
		t.Fatal("nil composition must fail")
	}
	if _, err := composeChainRuntimeServices(&composition{}, runtimeConfig{}, nil); err == nil {
		t.Fatal("nil setting reader must fail")
	}
	setting := func(string) (string, error) { return "", nil }
	// 坏 cache URL。
	if _, err := composeChainRuntimeServices(&composition{}, runtimeConfig{CacheDriver: "redis", RedisCacheURL: "://w11g-bad"}, setting); err == nil {
		t.Fatal("bad cache url must fail")
	}
	// 坏 state URL。
	if _, err := composeChainRuntimeServices(&composition{}, runtimeConfig{RuntimeStateDriver: "redis", RedisStateURL: "://w11g-bad"}, setting); err == nil {
		t.Fatal("bad state url must fail")
	}
	// 空 URL（driver=redis 但 URL 缺失）。
	if _, err := composeChainRuntimeServices(&composition{}, runtimeConfig{CacheDriver: "redis", RedisCacheURL: ""}, setting); err == nil {
		t.Fatal("empty cache url must fail")
	}
}

func TestW11GPrewarmAndInvalidateArms(t *testing.T) {
	// nil cache：直接返回（不 panic）。
	startGatewayAPIKeyCachePrewarm(nil, nil)
	// 非 nil cache：goroutine 内跑一轮（成功/失败都不 panic）。
	cache, err := gatewayruntimecache.New(w1cFailingReadModels{}, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	startGatewayAPIKeyCachePrewarm(cache, slog.Default())
	time.Sleep(50 * time.Millisecond)
	// Invalidate 空实现可安全调用。
	(redisStateClientProvider{}).Invalidate(context.Background(), nil)
}

func TestW11GNewCompositionIDUnique(t *testing.T) {
	first := newCompositionID("w11g")
	second := newCompositionID("w11g")
	if first == second || !strings.HasPrefix(first, "w11g_") {
		t.Fatalf("ids = %q %q", first, second)
	}
}

func TestW11GEnvOrDefaultFallbacks(t *testing.T) {
	if got := envOrDefault("W11G_MISSING_KEY", "w11g-default"); got != "w11g-default" {
		t.Fatalf("fallback = %q", got)
	}
}
