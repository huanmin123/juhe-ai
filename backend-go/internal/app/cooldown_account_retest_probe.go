package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/accountprobe"
	module "juhe-ai/backend-go/internal/modules/cooldownaccountretest"
	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/platform/proberevocationgate"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/platform/upstreamurlpolicy"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const cooldownProbeDependencyPingTimeout = 5 * time.Second

type nativeCooldownAccountRetestProbeStore interface {
	accountprobe.CandidateReader
	accountprobe.CooldownCandidateReader
	port.GatewayCandidateHydrationReader
	gatewaycandidatewindow.APIKeyRuntimeReader
	port.OAuthCredentialRefreshStore
}

type cooldownProbeRevocationProtector struct{ guard *proberevocationgate.Guard }

func (p cooldownProbeRevocationProtector) ProtectExternal(
	ctx context.Context,
	reload func(context.Context) error,
	send func(context.Context) error,
) error {
	return p.guard.ProtectExternal(ctx, proberevocationgate.ExternalFinalReload(reload), proberevocationgate.SendRequest(send))
}

func newNativeCooldownAccountRetestProbe(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	runtimeStore cooldownAccountRetestRuntimeStore,
) (module.Probe, func(), error) {
	store, ok := runtimeStore.(nativeCooldownAccountRetestProbeStore)
	if !ok {
		return nil, nil, fmt.Errorf("cooldown account retest store lacks native probe capabilities")
	}
	if strings.TrimSpace(cfg.RedisStateURL) == "" {
		return nil, nil, fmt.Errorf("JUHE_AI_REDIS_STATE_URL 不能为空")
	}
	if strings.TrimSpace(cfg.Secret) == "" {
		return nil, nil, fmt.Errorf("JUHE_AI_SECRET 不能为空")
	}
	stateRedisNamespace := cooldownProbeStateRedisNamespace(cfg.RedisNamespace)
	if stateRedisNamespace == "" {
		return nil, nil, fmt.Errorf("JUHE_AI_REDIS_NAMESPACE 不能为空")
	}
	if logger == nil {
		logger = slog.Default()
	}

	stateRedis, err := redisplatform.NewClient(cfg.RedisStateURL, stateRedisNamespace)
	if err != nil {
		return nil, nil, fmt.Errorf("JUHE_AI_REDIS_STATE_URL 无效: %w", err)
	}
	gateStore, err := postgresstore.Open(ctx, cfg.PostgresURL)
	if err != nil {
		_ = stateRedis.Close()
		return nil, nil, fmt.Errorf("open dedicated cooldown account retest revocation gate PostgreSQL: %w", err)
	}
	closeResources := func() {
		gateStore.Close()
		_ = stateRedis.Close()
	}
	pingCtx, cancelPing := context.WithTimeout(ctx, cooldownProbeDependencyPingTimeout)
	err = stateRedis.Ping(pingCtx)
	cancelPing()
	if err != nil {
		closeResources()
		return nil, nil, fmt.Errorf("ping cooldown account retest state Redis: %w", err)
	}
	pingCtx, cancelPing = context.WithTimeout(ctx, cooldownProbeDependencyPingTimeout)
	err = gateStore.Ping(pingCtx)
	cancelPing()
	if err != nil {
		closeResources()
		return nil, nil, fmt.Errorf("ping cooldown account retest revocation gate PostgreSQL: %w", err)
	}

	oauthLock, err := redisplatform.NewOAuthRefreshLock(stateRedis, redisplatform.OAuthRefreshLockOptions{
		OnReleaseError: func(releaseErr error) {
			logger.Error("释放 OAuth refresh lock 失败", slog.String("error", releaseErr.Error()))
		},
	})
	if err != nil {
		closeResources()
		return nil, nil, fmt.Errorf("create cooldown account retest OAuth refresh lock: %w", err)
	}
	revocationGuard, err := proberevocationgate.New(gateStore, proberevocationgate.Options{})
	if err != nil {
		closeResources()
		return nil, nil, fmt.Errorf("create cooldown account retest revocation gate: %w", err)
	}

	codec := secretcrypto.NewJSONCodec(cfg.Secret)
	hydrator := gatewaycandidatewindow.NewBatchHydrator(gatewaycandidatewindow.BatchHydratorOptions{
		Reader: store, APIKeyRuntime: store, CredentialCodec: codec, FingerprintSecret: cfg.Secret,
	})
	loader := accountprobe.Loader{Reader: store, Hydrator: hydrator}
	urlPolicy := upstreamurlpolicy.Config{
		AllowPrivateBaseURLs:    cfg.AllowPrivateUpstreamBaseURLs,
		PrivateBaseURLAllowlist: append([]string(nil), cfg.UpstreamBaseURLPrivateAllowlist...),
		Production:              strings.EqualFold(strings.TrimSpace(cfg.Env), "production"),
	}
	rawTransportFactory := accountprobe.TransportFactory{URLPolicy: urlPolicy}
	guardedTransportFactory := accountprobe.RevocationGuardTransportFactory{
		Next: rawTransportFactory, Guard: cooldownProbeRevocationProtector{guard: revocationGuard},
	}
	oauthSnapshots := accountprobe.OAuthSnapshotLoader{Loader: loader, Codec: codec}
	oauthCoordinator := accountprobe.OAuthCoordinator{
		Reloader: oauthSnapshots,
		Lock:     accountprobe.NewRedisOAuthRefreshLockRunner(oauthLock),
		CAS:      accountprobe.OAuthCredentialCASAdapter{Codec: codec, Store: store},
		Refresh:  accountprobe.OAuthRefreshTransportExecutor{URLPolicy: urlPolicy},
		Enricher: accountprobe.OAuthGeminiRefreshEnricher{Enricher: accountprobe.NewGeminiOAuthEnricher(
			accountprobe.GeminiOAuthEnrichmentTransportExecutor{URLPolicy: urlPolicy},
		)},
	}
	probe := accountprobe.CooldownProbe{
		Loader: loader, Current: store, TransportFactory: guardedTransportFactory,
		OAuthSnapshots: oauthSnapshots, OAuthCoordinator: oauthCoordinator,
	}
	return probe, closeResources, nil
}

func cooldownProbeStateRedisNamespace(namespace string) string {
	namespace = strings.Trim(strings.TrimSpace(namespace), ":")
	if namespace == "" {
		return ""
	}
	return "juhe-ai:" + namespace + ":state"
}
