package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	circuitcontrolplane "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_control_plane"
	circuitprojector "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_projector"
	circuitruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_runtime"
	gatewaydispatch "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/gateway_dispatch"
	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
	sessionretention "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/session_retention"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckowner"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckquestionbank"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
	"github.com/huanminabc/juhe-ai/backend-go-platform/processlog"
	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
)

// corsSurfacePrefixes scopes the CORS middleware to the Node system-api app
// mount prefixes (system-api-app.ts): the management face /__aisys__ (the SPA
// plus /__aisys__/api), the public /__aipublic__ family and the delegated
// /__aidelegated__/v1 surface. The /v1 gateway chain (browser-less API-key
// clients) is intentionally outside the CORS surface.
var corsSurfacePrefixes = []string{"/__aisys__", "/__aipublic__", "/__aidelegated__/v1"}

func main() {
	version := flag.Bool("version", false, "print the gateway project contract version")
	check := flag.Bool("check-boundary", false, "verify the scaffold boundary")
	auditLegacyMigration := flag.Bool("migrate-audit-log-legacy-sqlite", false, "offline F3 legacy SQLite migration")
	operationLegacySQLiteMigration := flag.Bool("migrate-operation-log-legacy-sqlite", false, "offline F4 legacy SQLite migration")
	operationLegacyPostgresMigration := flag.Bool("migrate-operation-log-legacy-postgres", false, "offline F4 legacy PostgreSQL schema migration")
	nodeStopped := flag.Bool("node-stopped", false, "confirm Node is stopped for the offline migration")
	goStopped := flag.Bool("go-stopped", false, "confirm all Go owners are stopped for the offline migration")
	backupConfirmed := flag.Bool("backup-confirmed", false, "confirm a recoverable backup was verified for the offline migration")
	healthAddress := flag.String("health-listen-address", envOrDefault("JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS", "127.0.0.1:3306"), "loopback health listen address")
	var auditMigration auditlog.LegacyMigrationOptions
	var operationMigration operationlog.LegacyMigrationOptions
	flag.StringVar(&auditMigration.SourceDatabasePath, "source-db", "", "legacy Node audit SQLite database")
	flag.StringVar(&auditMigration.TargetDatabasePath, "target-db", "", "dedicated Go F3 SQLite database")
	flag.StringVar(&auditMigration.SourceBlobDirectory, "source-blob-dir", "", "legacy audit blob directory")
	flag.StringVar(&auditMigration.TargetBlobDirectory, "target-blob-dir", "", "dedicated Go F3 blob directory")
	flag.StringVar(&operationMigration.SourceDatabasePath, "operation-log-source-db", "", "legacy Node operation-log SQLite database")
	flag.Parse()
	if *version {
		fmt.Printf("juhe-ai-gateway project=%s contract=%s\n", contracts.ProjectGateway, contracts.ArchitectureVersion)
		return
	}
	if *check {
		fmt.Println("juhe-ai-gateway boundary=ready runtime=audit-operation-owner")
		return
	}
	if flag.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unsupported gateway arguments: %v\n", flag.Args())
		os.Exit(2)
	}
	auditMigration.NodeStopped, auditMigration.GoStopped = *nodeStopped, *goStopped
	operationMigration.NodeStopped, operationMigration.GoStopped, operationMigration.BackupConfirmed = *nodeStopped, *goStopped, *backupConfirmed
	migrationCount := 0
	for _, enabled := range []bool{*auditLegacyMigration, *operationLegacySQLiteMigration, *operationLegacyPostgresMigration} {
		if enabled {
			migrationCount++
		}
	}
	if migrationCount > 1 {
		fmt.Fprintln(os.Stderr, "offline migration modes are mutually exclusive")
		os.Exit(2)
	}
	if *auditLegacyMigration {
		runAuditLegacyMigration(auditMigration)
		return
	}
	if *operationLegacySQLiteMigration || *operationLegacyPostgresMigration {
		runOperationLogLegacyMigration(operationMigration, *operationLegacyPostgresMigration)
		return
	}

	// JUHE_AI_LOG_LEVEL (Node log-level.ts): trace..silent resolved before any
	// store opens; an invalid value fails fast like the Node startup guard.
	logLevel, err := processlog.LoadLevel(os.Getenv)
	if err != nil {
		fail(err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	processlog.CatchPanic(logger)
	processlog.KeepAliveOnBrokenOutputPipe()
	ownerMode, err := ownermode.Load(os.Getenv)
	if err != nil {
		fail(err)
	}
	if !ownerMode.OwnsWork() {
		runPassiveGateway(*healthAddress, ownerMode, logger)
		return
	}
	// G20 composition root: runtime config mirrors the Node env contract
	// (runtime.ts); an enabled chain gate fails fast with the missing adapter
	// list, and the system-api composition requires the proven business owner
	// handoff before any store is opened.
	runtimeCfg, err := loadRuntimeConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load gateway runtime config: %w", err))
	}
	if err := gateGatewayChain(runtimeCfg.ChainEnabled); err != nil {
		fail(err)
	}
	if err := runtimeCfg.businessOwnerGate(); err != nil {
		fail(fmt.Errorf("verify business owner gates: %w", err))
	}
	// 2026-09-19 零配置自动认领（BusinessOwnerAutoClaimed）下没有 cutover
	// evidence 文件可读：新装 standalone 部署无 Node 切流历史，跳过证据校验；
	// 显式配置 JUHE_AI_BUSINESS_* 的部署（生产切流）仍强制完整证据。
	if runtimeCfg.SystemAPIEnabled && !runtimeCfg.BusinessOwnerAutoClaimed {
		evidenceReport, evidenceErr := modelcheckowner.VerifyConfiguredCutoverEvidence(runtimeCfg.BusinessCutoverEvidencePath, runtimeCfg.BusinessOwnerEpoch, time.Now().UTC())
		if evidenceErr != nil {
			fail(fmt.Errorf("read business owner cutover evidence: %w", evidenceErr))
		}
		if !evidenceReport.Ready {
			fail(fmt.Errorf("verify business owner cutover evidence: %s", strings.Join(evidenceReport.Errors, "; ")))
		}
	}
	// The owner contract is parsed before any gateway stores/listeners are
	// opened. Until the J3b runtime is actually attached to this process, an
	// enabled flag must fail closed rather than silently serving a partial owner.
	j3bConfig, err := modelcheckowner.LoadConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load J3b gateway owner config: %w", err))
	}
	if j3bConfig.Enabled {
		// 2026-09-20 零配置自动认领没有 cutover 证据文件可读（与上方
		// BusinessOwnerAutoClaimed 同款）；只有显式配置证据路径时才校验。
		if j3bConfig.CutoverEvidencePath != "" {
			evidenceReport, evidenceErr := modelcheckowner.VerifyConfiguredCutoverEvidence(j3bConfig.CutoverEvidencePath, j3bConfig.OwnerEpoch, time.Now().UTC())
			if evidenceErr != nil {
				fail(fmt.Errorf("read J3b cutover evidence: %w", evidenceErr))
			}
			if !evidenceReport.Ready {
				fail(fmt.Errorf("verify J3b cutover evidence: %s", strings.Join(evidenceReport.Errors, "; ")))
			}
		}
		// secrets 未显式配置时按主配置同款零配置约定回落内置开发密钥；
		// 生产环境主配置已强制 JUHE_AI_SECRET 强度，这里继承其值。
		if j3bConfig.CredentialSecret == "" {
			j3bConfig.CredentialSecret = defaultRuntimeSecret
		}
		if j3bConfig.IdentitySecret == "" {
			j3bConfig.IdentitySecret = defaultRuntimeSecret
		}
	}
	var j3bHostComponent supervisor.Component
	var j3bManagementServer *http.Server
	var j3bManagementListener net.Listener
	var j3bManagementServeErr chan error
	var retentionComponent supervisor.Component
	var retentionEnabled bool
	var retentionRunning atomic.Bool
	var circuitRuntimeComponent supervisor.Component
	var circuitRuntimeEnabled bool
	var circuitRuntimeRunning atomic.Bool
	var keyModelStore *keymodelruntime.RedisStore
	if j3bConfig.Enabled {
		var businessMode modelcheckauth.Mode = modelcheckauth.SQLite
		if j3bConfig.StoreMode == "postgres" {
			businessMode = modelcheckauth.Postgres
		}
		businessConnection, openErr := modelcheckowner.OpenBusinessTargetConnection(context.Background(), j3bConfig)
		if openErr != nil {
			fail(fmt.Errorf("open J3b Business owner connection: %w", openErr))
		}
		defer businessConnection.Close()
		businessSource := businessConnection.Source
		authenticator, authErr := modelcheckauth.New(businessConnection.DB, businessMode, time.Now)
		if authErr != nil {
			fail(fmt.Errorf("create J3b Gateway authenticator: %w", authErr))
		}
		if authErr := authenticator.CheckContract(context.Background()); authErr != nil {
			fail(fmt.Errorf("verify J3b Gateway auth contract: %w", authErr))
		}
		retentionGate := sessionretention.OwnerGate{Confirmed: j3bConfig.BusinessHandoffConfirmed, SchemaReady: j3bConfig.SchemaReady, NodeWriterStopped: j3bConfig.NodeWriterStopped}
		// 按枚举映射保留模式（与下方 circuitMode 同款写法）。直接
		// sessionretention.Mode(businessMode) 会经 string(uint8) rune 转换
		// 得到 "\x01"/"\x02"，永远不是 "sqlite"/"postgres"，J3b 分支
		// 100% 在 ErrInvalidMode 处 fail。
		retentionMode := sessionretention.SQLite
		if businessMode == modelcheckauth.Postgres {
			retentionMode = sessionretention.Postgres
		}
		retentionStore, retentionErr := sessionretention.New(businessConnection.DB, retentionMode, "juhe_business", retentionGate)
		if retentionErr != nil {
			fail(fmt.Errorf("create J3b Gateway session retention owner: %w", retentionErr))
		}
		if retentionErr := retentionStore.CheckContract(context.Background()); retentionErr != nil {
			fail(fmt.Errorf("verify J3b Gateway session retention contract: %w", retentionErr))
		}
		// 2026-09-20 零配置自动认领：未配置 Redis 时 key-model 前台准入
		// 回退进程内 memory store（单进程 owner，重启即重置，语义与主链路
		// 准入可旁路一致），账户熔断 RuntimeGate 与 projector 组件不装配；
		// 配置 Redis 后恢复完整链路。
		var keyModelGate gatewaydispatch.KeyModelGate
		var probeCircuit gatewaydispatch.AccountCircuitGate
		if j3bConfig.CircuitRuntimeRedisURL != "" {
			circuitMode := circuitcontrolplane.SQLite
			if businessMode == modelcheckauth.Postgres {
				circuitMode = circuitcontrolplane.Postgres
			}
			circuitGate := circuitcontrolplane.OwnerGate{
				Confirmed:         j3bConfig.BusinessHandoffConfirmed,
				SchemaReady:       j3bConfig.SchemaReady,
				NodeWriterStopped: j3bConfig.NodeWriterStopped,
			}
			circuitStore, circuitErr := circuitcontrolplane.New(businessConnection.DB, circuitMode, "juhe_business", circuitGate)
			if circuitErr != nil {
				fail(fmt.Errorf("create J3b Gateway circuit control-plane owner: %w", circuitErr))
			}
			if circuitErr := circuitStore.CheckContract(context.Background()); circuitErr != nil {
				fail(fmt.Errorf("verify J3b Gateway circuit control-plane contract: %w", circuitErr))
			}
			runtimeStore, runtimeErr := circuitruntime.New(circuitruntime.Config{URL: j3bConfig.CircuitRuntimeRedisURL, Namespace: j3bConfig.CircuitRuntimeRedisNamespace, Capacity: j3bConfig.CircuitRuntimeCapacity, Retention: j3bConfig.CircuitRuntimeRetention}, circuitruntime.OwnerGate{Confirmed: j3bConfig.BusinessHandoffConfirmed, SchemaReady: j3bConfig.SchemaReady, NodeWriterStopped: j3bConfig.NodeWriterStopped})
			if runtimeErr != nil {
				fail(fmt.Errorf("create J3b Gateway circuit runtime owner: %w", runtimeErr))
			}
			if pingErr := runtimeStore.Ping(context.Background()); pingErr != nil {
				_ = runtimeStore.Close()
				fail(fmt.Errorf("ping J3b Gateway circuit runtime Redis: %w", pingErr))
			}
			if readyErr := runtimeStore.CheckReady(context.Background()); readyErr != nil {
				_ = runtimeStore.Close()
				fail(fmt.Errorf("verify J3b Gateway circuit runtime owner fence: %w", readyErr))
			}
			keyModelStore, runtimeErr = keymodelruntime.NewRedisStore(j3bConfig.CircuitRuntimeRedisURL, j3bConfig.CircuitRuntimeRedisNamespace, keymodelruntime.OwnerGate{Confirmed: j3bConfig.BusinessHandoffConfirmed, SchemaReady: j3bConfig.SchemaReady, NodeWriterStopped: j3bConfig.NodeWriterStopped})
			if runtimeErr != nil {
				_ = runtimeStore.Close()
				fail(fmt.Errorf("create J3b Gateway key-model runtime owner: %w", runtimeErr))
			}
			if pingErr := keyModelStore.Ping(context.Background()); pingErr != nil {
				_ = keyModelStore.Close()
				_ = runtimeStore.Close()
				fail(fmt.Errorf("ping J3b Gateway key-model runtime Redis: %w", pingErr))
			}
			projector, projectorErr := circuitprojector.New(circuitStore, runtimeStore, j3bConfig.InstanceID)
			if projectorErr != nil {
				_ = runtimeStore.Close()
				fail(fmt.Errorf("create J3b Gateway circuit projector: %w", projectorErr))
			}
			circuitRuntimeEnabled = true
			circuitRuntimeComponent = supervisor.Component{Name: "J3b account-circuit-runtime-owner", Run: func(runCtx context.Context) error {
				circuitRuntimeRunning.Store(true)
				defer circuitRuntimeRunning.Store(false)
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					if err := runtimeStore.CheckReady(runCtx); err != nil {
						return err
					}
					if _, err := projector.RunOnce(runCtx, time.Now().UTC(), 500); err != nil {
						return err
					}
					select {
					case <-runCtx.Done():
						return runCtx.Err()
					case <-ticker.C:
					}
				}
			}, Close: runtimeStore.Close}
			keyModelGate = keyModelStore
			probeCircuit = gatewaydispatch.RuntimeCircuitGate{Store: runtimeStore}
		} else {
			keyModelGate = newJ3bMemoryKeyModelGate()
		}
		retentionInterval, retentionLimit, retentionConfigErr := loadSessionRetentionConfig(os.Getenv)
		if retentionConfigErr != nil {
			fail(fmt.Errorf("load J3b Gateway session retention config: %w", retentionConfigErr))
		}
		retentionEnabled = true
		retentionComponent = supervisor.Component{
			Name: "J3b session-retention-owner",
			Run: func(runCtx context.Context) error {
				return runSessionRetention(runCtx, retentionStore, retentionInterval, retentionLimit, func() { retentionRunning.Store(true) })
			},
		}
		enforcement, enforcementErr := modelcheckowner.NewBusinessEnforcementApplier(businessConnection.DB, businessMode == modelcheckauth.Postgres)
		if enforcementErr != nil {
			fail(fmt.Errorf("create J3b Gateway enforcement owner: %w", enforcementErr))
		}
		recovery, recoveryErr := modelcheckowner.NewBusinessRecoveryApplier(businessConnection.DB, businessMode == modelcheckauth.Postgres)
		if recoveryErr != nil {
			fail(fmt.Errorf("create J3b Gateway recovery owner: %w", recoveryErr))
		}
		quality, qualityErr := modelcheckowner.NewBusinessQualityManager(businessConnection.DB, businessMode == modelcheckauth.Postgres)
		if qualityErr != nil {
			fail(fmt.Errorf("create J3b Gateway quality manager: %w", qualityErr))
		}
		// 题库域装配（计划阶段 2/3）：业务库句柄与 BusinessQualityManager
		// 同源；postgres 布尔沿用 businessMode 取法。store 同时充当质量配置
		// 写入侧的 approved 存在性校验端口与运行时题目解析端口。审计 sink：
		// F4 operation-log producer 在本块之后的 system API 装配段才存在，
		// J3b 管理面（含既有 model-checks 端点）没有进程内审计 sink 可注入，
		// 题库写操作审计按 handlers 的 nil-sink 契约降级为 no-op。
		questionBankStore, questionBankErr := modelcheckquestionbank.NewStore(businessConnection.DB, businessMode == modelcheckauth.Postgres)
		if questionBankErr != nil {
			fail(fmt.Errorf("create J3b Gateway question bank store: %w", questionBankErr))
		}
		questionBankAdmin := modelcheckquestionbank.NewHTTPHandlers(questionBankStore, nil, time.Now, true)
		questionBankSelf := modelcheckquestionbank.NewHTTPHandlers(questionBankStore, nil, time.Now, false)
		quality.SetQuestionBankVerifier(questionBankStore)
		schedulerSource := &modelcheckowner.BusinessSchedulerSource{Business: businessConnection.DB, Postgres: businessMode == modelcheckauth.Postgres, OwnerID: j3bConfig.InstanceID}
		if schedulerErr := schedulerSource.CheckContract(context.Background()); schedulerErr != nil {
			fail(fmt.Errorf("verify J3b Gateway scheduler contract: %w", schedulerErr))
		}
		tokenizer, tokenizerErr := modelcheckprobe.NewO200kTokenizer()
		if tokenizerErr != nil {
			fail(fmt.Errorf("create J3b Gateway tokenizer: %w", tokenizerErr))
		}
		modelLimits, modelLimitsErr := modelcheckowner.NewVersionedModelLimits(businessConnection.DB, businessMode == modelcheckauth.Postgres)
		if modelLimitsErr != nil {
			fail(fmt.Errorf("create J3b Gateway model-limit source: %w", modelLimitsErr))
		}
		healthStatHour, healthStatHourErr := modelcheckowner.LoadBusinessHealthStatHour(context.Background(), businessConnection.DB, businessMode == modelcheckauth.Postgres)
		if healthStatHourErr != nil {
			fail(fmt.Errorf("load J3b Gateway usage stats timezone: %w", healthStatHourErr))
		}
		if j3bConfig.StoreMode == "sqlite" && j3bConfig.AutoClaimed {
			// 零配置自动认领：专属库文件与 schema 由组合根幂等自举（与六库
			// preflight 同款语义）；严格切流模式仍要求外部预置并保持
			// SCHEMA_READY 门禁。
			if err := ensureJ3bDedicatedSQLiteBootstrap(context.Background(), j3bConfig.DatabasePath); err != nil {
				fail(err)
			}
		}
		j3bHost, hostErr := modelcheckowner.OpenHost(context.Background(), j3bConfig, modelcheckowner.HostDependencies{
			Resolve:           businessSource.Resolver(),
			ResolveComparison: businessSource.ComparisonResolver(),
			AccountOptions:    businessSource,
			Authorize:         modelcheckowner.NewAdminAuthorize(authenticator),
			Build:             businessSource.BuildRequest,
			BuildScoped:       businessSource.BuildScopedRequest,
			Dispatcher:        &gatewaydispatch.ProbeAdapter{Dispatcher: &gatewaydispatch.Dispatcher{Client: &http.Client{}, KeyModel: keyModelGate, Circuit: probeCircuit}},
			Enforcement:       enforcement,
			Quality:           quality,
			Tokenizer:         tokenizer,
			ModelLimits:       modelLimits,
			HealthStatHour:    healthStatHour,
			QuestionBankAdmin: questionBankAdmin,
			QuestionBankSelf:  questionBankSelf,
			QuestionBank:      questionBankStore,
			SchedulerFactory: func(store *modelcheckowner.Store, runtime *modelcheckowner.Runtime, projector *modelcheckowner.QualityProjector) (modelcheckowner.SchedulerSource, modelcheckowner.SchedulerExecutor) {
				source := schedulerSource
				source.Store = store
				build := func(ctx context.Context, payload modelcheckowner.ScheduledPayload) (modelcheckowner.RunRequest, error) {
					trigger := "scheduled"
					if payload.EnforcementID != "" {
						trigger = "quality_recovery"
					}
					target, err := businessSource.Resolve(ctx, modelcheckowner.RunRequest{SystemAccountID: payload.SystemAccountID, TargetType: payload.TargetType, TargetID: payload.TargetID, Model: payload.Model, ConfigRevision: payload.ConfigRevision, DispatchRevision: payload.DispatchRevision, SourceConfigRevision: payload.SourceConfigRevision, SourceDispatchRevision: payload.SourceDispatchRevision, TriggerKind: trigger})
					if err != nil {
						return modelcheckowner.RunRequest{}, err
					}
					if target.ConfigRevision != payload.ConfigRevision {
						return modelcheckowner.RunRequest{}, errors.New("J3b scheduled account config revision is stale")
					}
					return modelcheckowner.RunRequest{TargetType: payload.TargetType, TargetID: payload.TargetID, Model: payload.Model, Profile: payload.Profile, SystemAccountID: payload.SystemAccountID, ActorSystemAccountID: payload.ActorSystemAccountID, ProviderCode: target.ProviderCode, Threshold: payload.Threshold, PenaltyAction: payload.PenaltyAction, ConfigRevision: payload.ConfigRevision, SourceConfigRevision: payload.SourceConfigRevision, SourceDispatchRevision: payload.SourceDispatchRevision, PolicyRevision: payload.PolicyRevision, ProbeSetVersion: payload.ProbeSetVersion, IdentityKey: payload.IdentityKey, DispatchRevision: payload.DispatchRevision}, nil
				}
				executor := &modelcheckowner.SchedulerExecutorMux{Runs: &modelcheckowner.SchedulerRunExecutor{Runtime: runtime, Build: build, Recovery: recovery.Complete, Scheduled: source.CompleteScheduled}, Health: &modelcheckowner.HealthSyncRetryExecutor{Projector: projector}}
				return source, executor
			},
		})
		if hostErr != nil {
			if keyModelStore != nil {
				_ = keyModelStore.Close()
			}
			fail(fmt.Errorf("open J3b Gateway owner host: %w", hostErr))
		}
		if keyModelStore != nil {
			defer keyModelStore.Close()
		}
		managementMux := http.NewServeMux()
		var captchaService *modelcheckauth.CaptchaService
		if !envBool("JUHE_AI_AUTH_CAPTCHA_DISABLED") {
			captchaService = modelcheckauth.NewCaptchaService(time.Now)
		}
		managementMux.Handle("/auth/", http.StripPrefix("/auth", &modelcheckauth.HTTPHandler{Auth: authenticator, Captcha: captchaService, TemporaryAccessIPAllowlist: commaList(os.Getenv("JUHE_AI_TEMPORARY_ACCESS_IP_ALLOWLIST"))}))
		if err := j3bHost.MountScoped(managementMux, "/__aisys__/api/model-checks/", modelcheckowner.NewAdminAuthorize(authenticator), true); err != nil {
			fail(fmt.Errorf("mount J3b Gateway administrator routes: %w", err))
		}
		if err := j3bHost.MountScoped(managementMux, "/__aisys__/api/my-model-checks/", modelcheckowner.NewSelfAuthorize(authenticator), false); err != nil {
			fail(fmt.Errorf("mount J3b Gateway self routes: %w", err))
		}
		if err := j3bHost.Mount(managementMux, "/model-checks/"); err != nil {
			fail(fmt.Errorf("mount J3b Gateway management routes: %w", err))
		}
		managementAddress := envOrDefault("JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS", "127.0.0.1:3307")
		var listenErr error
		j3bManagementListener, listenErr = net.Listen("tcp", managementAddress)
		if listenErr != nil {
			fail(fmt.Errorf("listen J3b Gateway management endpoint %q: %w", managementAddress, listenErr))
		}
		defer j3bManagementListener.Close()
		// 注意：该 server 先于 gatewayusage.SetAuditCapturedDroppedTotal 注入
		// （main 下方）启动，但其 mux 只挂 /auth/ 与 model-checks 族，不含
		// /__aisys__/metrics——若未来把 metrics 挂上该 mux，注入顺序即成为
		// 真竞争，必须把注入移到本 Serve 之前。
		j3bManagementServer = &http.Server{Handler: managementMux, ReadHeaderTimeout: 5 * time.Second}
		j3bManagementServeErr = make(chan error, 1)
		go func() { j3bManagementServeErr <- j3bManagementServer.Serve(j3bManagementListener) }()
		j3bHostComponent = j3bHost.Component()
	}
	postgresPools := pgpool.NewRegistry()
	defer postgresPools.Close()
	auditConfig, err := auditlog.LoadConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load F3 audit-log config: %w", err))
	}
	if auditConfig.Mode == auditlog.ModePostgres {
		auditConfig.PostgresPool, err = postgresPools.Acquire(auditConfig.PostgresURL, "gateway-store", auditConfig.PostgresMaxOpenConns, auditConfig.PostgresMaxIdleConns)
		if err != nil {
			fail(fmt.Errorf("open shared F3 PostgreSQL pool: %w", err))
		}
	}
	auditStore, err := auditlog.OpenStore(auditConfig)
	if err != nil {
		fail(fmt.Errorf("open F3 audit-log store: %w", err))
	}
	defer auditStore.Close()
	if err := auditStore.EnsureSchema(context.Background()); err != nil {
		fail(fmt.Errorf("initialize F3 audit-log schema: %w", err))
	}
	// The F3 persistence lease is acquired once per process and shared by
	// the in-process audit producer (chain dispatch, compose.go) and the
	// resident owner (retention): both writers must fence under the same
	// owner_id/fence_token, otherwise a second holder would permanently fence
	// the first one out. 去跨进程战役第四刀：loopback F3 input server 删除，
	// producer 是唯一的链审计写入口。
	// ownerLeaseAcquireWait 在 F3/F4 两个启动获取点之前解析一次、共用同一
	// 等待值；解析失败在此 fail 一次。默认 0 时两个获取点与既有 fail-fast
	// 契约完全一致。
	ownerLeaseAcquireWait, waitErr := loadOwnerLeaseAcquireWait(os.Getenv)
	if waitErr != nil {
		fail(waitErr)
	}
	auditLease, ok, keeperErr := startLeaseKeeperWithWait(logger, "F3 audit", ownerLeaseAcquireWait, func() (*auditlog.LeaseKeeper, bool, error) {
		return auditlog.StartLeaseKeeper(context.Background(), auditStore, auditConfig.InstanceID, auditConfig.OwnerLease, logger)
	})
	if keeperErr != nil {
		fail(fmt.Errorf("acquire F3 audit owner lease: %w", keeperErr))
	}
	if !ok {
		fail(errors.New("F3 audit owner lease held by another owner process"))
	}
	// Registered ahead of auditStore.Close so the LIFO defer order releases
	// the lease first and closes the store handle last.
	defer auditLease.Close()
	// The F3 producer is the process-wide chain audit sink; it shares the
	// audit lease above and only extends it per record.
	auditProducer := auditlog.NewProducer(auditStore, auditLease.Lease(), auditConfig, producerLogger{})
	// Queue-saturation drops become a Prometheus gauge seam (before Serve, so
	// the write happens before any scrape goroutine reads it).
	gatewayusage.SetAuditCapturedDroppedTotal(auditProducer.DroppedTotal)
	operationConfig, err := operationlog.LoadConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load F4 operation-log config: %w", err))
	}
	// 2026-09-19 零配置自动认领臂：F4 sqlite 镜像与业务库同文件（同为
	// <DATA_DIR>/business.sqlite3 派生）时，业务库文件在组合根 preflight 之前
	// 尚不存在，F4 store 的只读镜像打开会失败。此处先对业务库执行一次
	// ensure+seed preflight（与组合根稍后的同一 preflight 幂等）。F4 显式
	// 配置了独立 settings 镜像的部署不满足同文件条件，维持原启动顺序。
	if runtimeCfg.BusinessOwnerAutoClaimed && operationConfig.Enabled && operationConfig.Mode == operationlog.ModeSQLite &&
		filepath.Clean(operationConfig.BusinessSettingsPath) == filepath.Clean(runtimeCfg.BusinessDatabasePath) {
		seedDB, seedErr := sql.Open("sqlite", sqliteFileDSN(runtimeCfg.BusinessDatabasePath))
		if seedErr != nil {
			fail(fmt.Errorf("open business sqlite database for zero-config seed: %w", seedErr))
		}
		seedDB.SetMaxOpenConns(1)
		if configureErr := configureSQLiteConnection(seedDB); configureErr != nil {
			_ = seedDB.Close()
			fail(fmt.Errorf("configure business sqlite database for zero-config seed: %w", configureErr))
		}
		if preflightErr := ensureGatewaySQLiteStoragePreflight(context.Background(), runtimeCfg, seedDB); preflightErr != nil {
			_ = seedDB.Close()
			fail(fmt.Errorf("zero-config sqlite storage preflight: %w", preflightErr))
		}
		_ = seedDB.Close()
	}
	var operationStore operationlog.Store
	if operationConfig.Enabled {
		if operationConfig.Mode == operationlog.ModePostgres {
			operationConfig.PostgresPool, err = postgresPools.Acquire(operationConfig.PostgresURL, "gateway-store", operationConfig.PostgresMaxOpenConns, operationConfig.PostgresMaxIdleConns)
			if err != nil {
				fail(fmt.Errorf("open shared F4 PostgreSQL pool: %w", err))
			}
		}
		// F4 镜像运行期同步选型（consumer 侧读业务库兜底）：SQLite 模式把
		// 业务库本体路径交给 F4 store，镜像缺失运行期新建/改名账户时名字
		// 解析兜底到业务库（部署契约里镜像路径常直接指向业务库文件，此时
		// store 复用句柄不重复打开）。PostgreSQL 模式直读 juhe_business，
		// 无镜像概念。2026-09-19：只透传显式 JUHE_AI_BUSINESS_DATABASE_PATH，
		// 不传 loadRuntimeConfig 的派生值——兜底句柄是可选增强，派生业务库
		// 文件尚不存在时（零配置首启动）不允许它阻塞 store 打开；零配置下
		// 镜像本身就是业务库文件，兜底无增益。
		if operationConfig.Mode == operationlog.ModeSQLite {
			operationConfig.BusinessDatabasePath = strings.TrimSpace(os.Getenv("JUHE_AI_BUSINESS_DATABASE_PATH"))
		}
		operationStore, err = operationlog.OpenStore(operationConfig)
		if err != nil {
			fail(fmt.Errorf("open F4 operation-log store: %w", err))
		}
		defer operationStore.Close()
		if err := operationStore.EnsureSchema(context.Background()); err != nil {
			fail(fmt.Errorf("initialize F4 operation-log schema: %w", err))
		}
	}

	var auditRunning atomic.Bool
	var operationRunning atomic.Bool
	var j3bRunning atomic.Bool
	// The F4 persistence lease is acquired once per process and shared by
	// the in-process producer (management-plane writes, compose.go) and the
	// resident owner (retention): both writers must fence under the same
	// owner_id/fence_token, otherwise the second holder would permanently
	// fence the first one out. 去跨进程战役第四刀：loopback F4 input server
	// 删除，producer 是本进程唯一写入方。
	var operationLease *operationlog.LeaseKeeper
	if runtimeCfg.SystemAPIEnabled && operationConfig.Enabled {
		keeper, ok, keeperErr := startLeaseKeeperWithWait(logger, "F4 operation log", ownerLeaseAcquireWait, func() (*operationlog.LeaseKeeper, bool, error) {
			return operationlog.StartLeaseKeeper(context.Background(), operationStore, operationConfig.InstanceID, operationConfig.OwnerLease, logger)
		})
		if keeperErr != nil {
			fail(fmt.Errorf("acquire F4 operation-log owner lease: %w", keeperErr))
		}
		if !ok {
			fail(errors.New("F4 operation log owner lease held by another owner process"))
		}
		operationLease = keeper
		// Registered ahead of composed.Shutdown so the LIFO defer order
		// drains the producer first and releases the lease last.
		defer operationLease.Close()
	}
	if j3bHostComponent.Run != nil {
		baseJ3bComponent := j3bHostComponent
		j3bHostComponent = supervisor.Component{
			Name: baseJ3bComponent.Name,
			Run: func(runCtx context.Context) error {
				j3bRunning.Store(true)
				defer j3bRunning.Store(false)
				return baseJ3bComponent.Run(runCtx)
			},
			Close: baseJ3bComponent.Close,
		}
	}
	components := []supervisor.Component{
		{
			// F3 resident owner (去跨进程战役第四刀)：loopback input server
			// 删除后组件只承载 retention 节拍；owner lease 续租循环迁移到
			// 共享的 auditlog.LeaseKeeper（原 RunInputServer 节拍不变：
			// OwnerLease/3、最低 1s），retention 节拍与失败语义不变。
			Name: "F3 audit-log-owner",
			Run: func(runCtx context.Context) error {
				auditRunning.Store(true)
				defer auditRunning.Store(false)
				return auditlog.RunOwner(runCtx, auditStore, auditLease, auditConfig, logger)
			},
			Close: auditStore.Close,
		},
	}
	if j3bHostComponent.Run != nil {
		components = append(components, j3bHostComponent)
	}
	if retentionEnabled {
		baseRetentionComponent := retentionComponent
		retentionComponent = supervisor.Component{
			Name: baseRetentionComponent.Name,
			Run: func(runCtx context.Context) error {
				defer retentionRunning.Store(false)
				return baseRetentionComponent.Run(runCtx)
			},
			Close: baseRetentionComponent.Close,
		}
		components = append(components, retentionComponent)
	}
	if circuitRuntimeEnabled {
		components = append(components, circuitRuntimeComponent)
	}
	if operationConfig.Enabled {
		components = append(components, supervisor.Component{
			// F4 resident owner (去跨进程战役第四刀)：loopback input server
			// 删除后组件只承载 retention 节拍（原 RunInputServerSharedLease
			// select 循环中的 retention ticker 迁移到 operationlog.RunOwner，
			// 节拍/失败语义不变）；进程内 producer 是本进程唯一写入方。
			Name: "F4 operation-log-owner",
			Run: func(runCtx context.Context) error {
				operationRunning.Store(true)
				defer operationRunning.Store(false)
				keeper := operationLease
				if keeper == nil {
					// F4 runs without the system-api composition: the
					// component owns a private lease (the retired
					// RunInputServer semantics) instead of the shared one.
					owned, ok, keeperErr := operationlog.StartLeaseKeeper(runCtx, operationStore, operationConfig.InstanceID, operationConfig.OwnerLease, logger)
					if keeperErr != nil {
						return fmt.Errorf("acquire F4 operation-log owner lease: %w", keeperErr)
					}
					if !ok {
						return errors.New("F4 operation log owner lease held by another owner process")
					}
					defer owned.Close()
					keeper = owned
				}
				return operationlog.RunOwner(runCtx, operationStore, keeper, operationConfig, logger)
			},
			Close: func() error {
				// Release the shared lease while the store handle is still
				// open so a successor process can take over immediately.
				if operationLease != nil {
					operationLease.Close()
				}
				return operationStore.Close()
			},
		})
	}

	// Owner readiness is wired once here and shared by the loopback /health
	// listener and the main-port GET /__aisys__/health route (compose.go), so
	// every probe face reports identical owner facts.
	ownerHealth := &gatewayOwnerHealth{
		ownerMode:             ownerMode,
		auditRunning:          &auditRunning,
		operationEnabled:      operationConfig.Enabled,
		operationRunning:      &operationRunning,
		j3bWired:              j3bHostComponent.Run != nil,
		j3bRunning:            &j3bRunning,
		retentionEnabled:      retentionEnabled,
		retentionRunning:      &retentionRunning,
		circuitRuntimeEnabled: circuitRuntimeEnabled,
		circuitRuntimeRunning: &circuitRuntimeRunning,
	}

	listener, err := listenLoopback(*healthAddress)
	if err != nil {
		fail(fmt.Errorf("listen gateway health endpoint %q: %w", *healthAddress, err))
	}
	defer listener.Close()

	// G20 system-api composition root: opens the business-owned stores and
	// mounts every ready route family on the kernel. The composition shares
	// this process' lifetime; its HTTP listener binds the Node-compatible
	// JUHE_AI_HOST:JUHE_AI_PORT entry (main HTTP entry of the flip).
	var composed *composition
	var mainListener net.Listener
	var mainServer *http.Server
	var mainServeErr chan error
	if runtimeCfg.SystemAPIEnabled {
		// X04: the F3 audit config backs the audit-logs read face (dataset
		// handle pool, hot-search and payload-blob roots); the in-process
		// producer built above is the chain audit write face.
		composed, err = composeSystemAPI(runtimeCfg, postgresPools, operationStore, operationLease, auditProducer, auditConfig, ownerHealth)
		if err != nil {
			fail(fmt.Errorf("compose gateway system api: %w", err))
		}
		defer composed.Shutdown()
		// T6d gateway-side consumption: re-project the runtime rows and quota
		// scope bindings the jobs expiry sweep cannot touch (jobregistry
		// GoBinding freeze). Best-effort compensator: failures never stop the
		// owner, so the component is not health-gated.
		if composed.AuthzStore != nil {
			components = append(components, newAuthzExpiryRuntimeSyncComponent(composed.AuthzStore))
		}
		// 去跨进程战役第三刀：gateway 进程内自采样 Go 运行时指标（role 默认
		// gateway，读 jobs+gateway 同一份共享 trend 库）。store 未启用（默认）
		// 时组合根不装配采样器；store 句柄由 composed.Shutdown 关闭。
		if composed.GoRuntimeSampler != nil {
			components = append(components, supervisor.Component{
				Name: "Go runtime metrics sampler",
				Run:  composed.GoRuntimeSampler.Run,
			})
		}
		mainListener, err = net.Listen("tcp", fmt.Sprintf("%s:%d", runtimeCfg.Host, runtimeCfg.Port))
		if err != nil {
			fail(fmt.Errorf("listen gateway system api endpoint %s:%d: %w", runtimeCfg.Host, runtimeCfg.Port, err))
		}
		defer mainListener.Close()
		// The CORS middleware guards the management surface on the main
		// JUHE_AI_HOST:JUHE_AI_PORT listener. This is a deliberate Go-side
		// hardening addition (the archived Node chain defined the origin
		// contract but never mounted CORS; see kernel.CORSMiddleware and the
		// from-scratch review report §7-D12). Requests without an Origin
		// header and the whole /v1 gateway chain stay byte-for-byte unchanged;
		// the middleware itself never rejects a request.
		mainServer = &http.Server{
			Handler:           kernel.CORSMiddleware(runtimeCfg.corsPolicy(), corsSurfacePrefixes...)(composed.Kernel),
			ReadHeaderTimeout: 30 * time.Second,
		}
		mainServeErr = make(chan error, 1)
		go func() { mainServeErr <- mainServer.Serve(mainListener) }()
		// Node server.ts:181 启动序列：网关 API Key 校验缓存 fire-and-forget
		// 预热（失败仅告警），compose_prewarm.go 承载。链条关闭时缓存未装配，
		// 无预热面。
		if composed.chainServices != nil {
			startGatewayAPIKeyCachePrewarm(composed.chainServices.Cache, logger)
		}
		logger.Info("gateway system api composed",
			"address", mainListener.Addr().String(),
			"databaseDriver", runtimeCfg.DatabaseDriver,
			"cacheDriver", runtimeCfg.CacheDriver,
			"runtimeStateDriver", runtimeCfg.RuntimeStateDriver,
			"chainEnabled", runtimeCfg.ChainEnabled,
		)
	}

	// W4-B（BUG-0175）D-135：内部网关注册表启动接线（Node
	// startInternalGatewayRegistryWhenReady，server.ts:561-563——dbService 就绪
	// 且 HTTP 监听后启动；此处 mainServer 已开始 Serve，supervisor 组件在
	// health/supervisor 生命周期内发布心跳，ctx 结束时注销 boot id）。
	// PublisherEnabled 镜像 internalGatewayRegistryPublisherEnabled：
	// performance 模式 + redis 运行态驱动。ReaderEnabled 是 performance 控制
	// 面（control/control-replica db-service）的读侧开关——Go 网关进程没有
	// 对等的内部源点消费面，保持 false（ListEndpoints 返回空集）。
	if runtimeCfg.SystemAPIEnabled && runtimeCfg.RuntimeMode == "performance" && runtimeCfg.RuntimeStateDriver == "redis" {
		// NewRegistry 的三个错误判据（RedisURL 空、Secret 空、redis.ParseURL）
		// 均已被先行排除：同一 RedisStateURL 已在 compose 的 NewRedisLoginGuard
		// 内急切解析，Secret 已被 compose 的 apikeys.NewStore 空秘钥守卫拒绝
		// （w2 登记，纯重复判据）。
		registry, _ := gatewayruntimecache.NewRegistry(gatewayruntimecache.RegistryConfig{
			RedisURL:         runtimeCfg.RedisStateURL,
			Namespace:        runtimeCfg.RedisNamespace,
			Secret:           runtimeCfg.Secret,
			InstanceID:       newCompositionID("gateway"),
			Port:             runtimeCfg.Port,
			PublisherEnabled: true,
			ReaderEnabled:    false,
		})
		components = append(components, supervisor.Component{
			Name: "Internal gateway registry",
			Run: func(componentCtx context.Context) error {
				registry.Start()
				<-componentCtx.Done()
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer stopCancel()
				return registry.Stop(stopCtx)
			},
			Close: registry.Close,
		})
	}

	collector := gometrics.New("juhe-ai", "gateway")
	healthHandler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			http.NotFound(response, request)
			return
		}
		status, payload := ownerHealth.readiness()
		response.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			response.WriteHeader(status)
		}
		_ = json.NewEncoder(response).Encode(payload)
	})
	healthServer := &http.Server{
		Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodGet && request.URL.Path == "/__aisys__/metrics" {
				collector.Handler().ServeHTTP(response, request)
				// D-190（BUG-0175）：prometheus HTTP/网关指标族追加在 Go 运行时
				// 指标之后，同一 text/plain exposition 内完成两次写入。
				_, _ = io.WriteString(response, gatewayusage.RenderPrometheusMetrics())
				return
			}
			healthHandler.ServeHTTP(response, request)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- healthServer.Serve(listener) }()
	logger.Info("juhe-ai-gateway started", "healthAddress", listener.Addr().String(), "f4Enabled", operationConfig.Enabled)
	// supervisor.Run 的错误出口仅剩空组件表与"组件定义不完整"预检（组件运行
	// 错误只重试不回传）；main 的全部组件静态完整（j3b/retention/circuit 均在
	// 启用分支内追加且 Run 非 nil），F3 组件无条件追加，runErr 恒 nil（w2 登记）。
	_ = supervisor.Run(ctx, components, logger)
	// Graceful stop mirrors the Node order (db-service.ts shutdownDbService):
	// stop accepting HTTP first, then drain in-process workers (composed
	// shutdown via defer: F4 producer lease + business handle), then the
	// supervisor-owned owners, then the shared pools (deferred).
	if mainServer != nil {
		mainShutdownCtx, mainShutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		mainShutdownErr := mainServer.Shutdown(mainShutdownCtx)
		mainShutdownCancel()
		if mainShutdownErr != nil {
			fail(fmt.Errorf("shutdown gateway system api endpoint: %w", mainShutdownErr))
		}
		if mainServeResult := <-mainServeErr; mainServeResult != nil && !errors.Is(mainServeResult, http.ErrServerClosed) {
			fail(fmt.Errorf("gateway system api endpoint stopped: %w", mainServeResult))
		}
	}
	if j3bManagementServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = j3bManagementServer.Shutdown(shutdownCtx)
		shutdownCancel()
		if j3bManagementServeErr != nil {
			serveErrValue := <-j3bManagementServeErr
			if serveErrValue != nil && !errors.Is(serveErrValue, http.ErrServerClosed) {
				fail(fmt.Errorf("J3b management endpoint stopped: %w", serveErrValue))
			}
		}
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := healthServer.Shutdown(shutdownCtx)
	shutdownCancel()
	serveResult := <-serveErr
	if shutdownErr != nil {
		fail(fmt.Errorf("shutdown gateway health endpoint: %w", shutdownErr))
	}
	if serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed) {
		fail(fmt.Errorf("gateway health endpoint stopped: %w", serveResult))
	}
}

// runPassiveGateway never initializes F3/F4 stores or input servers. It is a
// health-only process for a standby/draining blue-green slot.
func runPassiveGateway(healthAddress string, ownerMode ownermode.Mode, logger *slog.Logger) {
	listener, err := listenLoopback(healthAddress)
	if err != nil {
		fail(fmt.Errorf("listen passive gateway health endpoint %q: %w", healthAddress, err))
	}
	defer listener.Close()
	server := &http.Server{Handler: passiveGatewayHealthHandler(ownerMode), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("juhe-ai-gateway passive", "healthAddress", listener.Addr().String(), "ownerMode", ownerMode)
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := server.Shutdown(shutdownCtx)
	cancel()
	serveResult := <-serveErr
	if shutdownErr != nil {
		fail(fmt.Errorf("shutdown passive gateway health endpoint: %w", shutdownErr))
	}
	if serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed) {
		fail(fmt.Errorf("passive gateway health endpoint stopped: %w", serveResult))
	}
}

func listenLoopback(address string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid loopback listen address %q: %w", address, err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, fmt.Errorf("invalid loopback listen address %q: port must be between 1 and 65535", address)
	}
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !(ip.IsLoopback()) {
			return nil, fmt.Errorf("invalid loopback listen address %q: host must be localhost or a loopback IP", address)
		}
	}
	return net.Listen("tcp", address)
}

func passiveGatewayHealthHandler(ownerMode ownermode.Mode) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"ready":             false,
			"ownerReady":        false,
			"ownerMode":         ownerMode,
			"auditLogReady":     false,
			"operationLogReady": false,
		})
	})
}

func runOperationLogLegacyMigration(options operationlog.LegacyMigrationOptions, postgres bool) {
	config, err := operationlog.LoadConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load F4 operation-log config: %w", err))
	}
	var result operationlog.LegacyMigrationResult
	if postgres {
		result, err = operationlog.MigrateLegacyPostgres(context.Background(), config, options)
	} else {
		result, err = operationlog.MigrateLegacySQLite(context.Background(), config, options)
	}
	if err != nil {
		fail(fmt.Errorf("F4 operation-log legacy migration failed: %w", err))
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fail(fmt.Errorf("encode F4 operation-log migration result: %w", err))
	}
}

func runAuditLegacyMigration(options auditlog.LegacyMigrationOptions) {
	result, err := auditlog.MigrateLegacySQLite(context.Background(), options)
	if err != nil {
		fail(fmt.Errorf("F3 audit SQLite migration failed: %w", err))
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fail(fmt.Errorf("encode F3 audit migration result: %w", err))
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func commaList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func envBool(name string) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func loadSessionRetentionConfig(getenv func(string) string) (time.Duration, int, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	interval := 15 * time.Minute
	if raw := strings.TrimSpace(getenv("JUHE_AI_SESSION_RETENTION_INTERVAL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return 0, 0, fmt.Errorf("JUHE_AI_SESSION_RETENTION_INTERVAL must be a positive duration: %q", raw)
		}
		interval = parsed
	}
	// This is a transaction-size/recovery window, not a product throughput
	// limit. Keep the default high; operators can raise it when the storage
	// backend and transaction budget support larger cleanup batches.
	limit := 10000
	if raw := strings.TrimSpace(getenv("JUHE_AI_SESSION_RETENTION_BATCH_SIZE")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return 0, 0, fmt.Errorf("JUHE_AI_SESSION_RETENTION_BATCH_SIZE must be a positive integer: %q", raw)
		}
		limit = parsed
	}
	return interval, limit, nil
}

// ownerLeaseAcquireWaitEnv 允许部署侧为进程启动时的 owner 租约获取注入有界
// 等待（Go duration，time.ParseDuration 解析）。
const ownerLeaseAcquireWaitEnv = "JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT"

// loadOwnerLeaseAcquireWait 解析 ownerLeaseAcquireWaitEnv：空字符串 = 0；
// 非空但解析失败或为负视为配置错误（与 loadSessionRetentionConfig 同款
// "must be a positive duration" 风格），由调用方 fail 阻止带病启动。
func loadOwnerLeaseAcquireWait(getenv func(string) string) (time.Duration, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := strings.TrimSpace(getenv(ownerLeaseAcquireWaitEnv))
	if raw == "" {
		return 0, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be a positive duration: %q", ownerLeaseAcquireWaitEnv, raw)
	}
	return parsed, nil
}

// startLeaseKeeperWithWait 包裹进程启动时的 owner 租约获取：前任 owner 被
// 强杀（defer 释放未执行）后，租约要等 TTL 过期才能被安全接管。部署侧通过
// JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT 注入有界等待（Go duration），默认空 /
// 0 保持既有 fail-fast 契约——活 owner 并存场景必须快速失败而非静默排队。
//
// ok=false 且 wait>0 时按 1s 间隔重试 start（末次间隔截断到总 deadline），
// deadline 到仍被持有则返回 ok=false，由调用方维持原文案 fail-fast；wait < 1s
// 时自然退化为一次性等待后重试一次。err 非 nil 立即返回不重试——传输错误与
// "被持有"是不同语义，保持现有行为。首条 Info 只在进入等待时打一次，不逐秒
// 刷屏。supervisor 运行期分支（runCtx 那处）不经过这里：组件失败已由
// supervisor 有界退避无限重试覆盖。
func startLeaseKeeperWithWait[T any](logger *slog.Logger, label string, wait time.Duration, start func() (T, bool, error)) (T, bool, error) {
	keeper, ok, err := start()
	if err != nil || ok || wait <= 0 {
		return keeper, ok, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	deadline := time.Now().Add(wait)
	logger.Info("owner lease held by another owner process, waiting for predecessor lease expiry",
		"lease", label, "waitBudget", wait.String())
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return keeper, ok, err
		}
		step := time.Second
		if remaining < step {
			step = remaining
		}
		time.Sleep(step)
		keeper, ok, err = start()
		if err != nil || ok {
			return keeper, ok, err
		}
	}
}

func runSessionRetention(ctx context.Context, store *sessionretention.Store, interval time.Duration, limit int, markReady func()) error {
	if store == nil || interval <= 0 || limit <= 0 {
		return errors.New("session retention component configuration is invalid")
	}
	cleanup := func() error {
		if _, err := store.Cleanup(ctx, sessionretention.CleanupInput{Limit: limit}); err != nil {
			return err
		}
		return nil
	}
	if err := cleanup(); err != nil {
		return err
	}
	if markReady != nil {
		markReady()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := cleanup(); err != nil {
				return err
			}
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
