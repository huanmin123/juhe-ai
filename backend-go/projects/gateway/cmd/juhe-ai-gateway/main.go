package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
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
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckowner"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
)

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

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ownerMode, err := ownermode.Load(os.Getenv)
	if err != nil {
		fail(err)
	}
	if !ownerMode.OwnsWork() {
		runPassiveGateway(*healthAddress, ownerMode, logger)
		return
	}
	// The owner contract is parsed before any gateway stores/listeners are
	// opened. Until the J3b runtime is actually attached to this process, an
	// enabled flag must fail closed rather than silently serving a partial owner.
	j3bConfig, err := modelcheckowner.LoadConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load J3b gateway owner config: %w", err))
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
		retentionStore, retentionErr := sessionretention.New(businessConnection.DB, sessionretention.Mode(businessMode), "juhe_business", retentionGate)
		if retentionErr != nil {
			fail(fmt.Errorf("create J3b Gateway session retention owner: %w", retentionErr))
		}
		if retentionErr := retentionStore.CheckContract(context.Background()); retentionErr != nil {
			fail(fmt.Errorf("verify J3b Gateway session retention contract: %w", retentionErr))
		}
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
		j3bHost, hostErr := modelcheckowner.OpenHost(context.Background(), j3bConfig, modelcheckowner.HostDependencies{
			Resolve:           businessSource.Resolver(),
			ResolveComparison: businessSource.ComparisonResolver(),
			AccountOptions:    businessSource,
			Authorize:         modelcheckowner.NewAdminAuthorize(authenticator),
			Build:             businessSource.BuildRequest,
			Dispatcher:        &gatewaydispatch.ProbeAdapter{Dispatcher: &gatewaydispatch.Dispatcher{Client: &http.Client{}, KeyModel: keyModelStore, Circuit: gatewaydispatch.RuntimeCircuitGate{Store: runtimeStore}}},
			Enforcement:       enforcement,
			Quality:           quality,
			Tokenizer:         tokenizer,
			ModelLimits:       modelLimits,
			SchedulerFactory: func(store *modelcheckowner.Store, runtime *modelcheckowner.Runtime, projector *modelcheckowner.QualityProjector) (modelcheckowner.SchedulerSource, modelcheckowner.SchedulerExecutor) {
				source := schedulerSource
				source.Store = store
				build := func(ctx context.Context, payload modelcheckowner.ScheduledPayload) (modelcheckowner.RunRequest, error) {
					trigger := "scheduled"
					if payload.EnforcementID != "" {
						trigger = "quality_recovery"
					}
					target, err := businessSource.Resolve(ctx, modelcheckowner.RunRequest{SystemAccountID: payload.SystemAccountID, TargetType: payload.TargetType, TargetID: payload.TargetID, Model: payload.Model, ConfigRevision: payload.ConfigRevision, TriggerKind: trigger})
					if err != nil {
						return modelcheckowner.RunRequest{}, err
					}
					if target.ConfigRevision != payload.ConfigRevision {
						return modelcheckowner.RunRequest{}, errors.New("J3b scheduled account config revision is stale")
					}
					return modelcheckowner.RunRequest{TargetType: payload.TargetType, TargetID: payload.TargetID, Model: payload.Model, Profile: payload.Profile, SystemAccountID: payload.SystemAccountID, ActorSystemAccountID: payload.ActorSystemAccountID, ProviderCode: target.ProviderCode, Threshold: payload.Threshold, PenaltyAction: payload.PenaltyAction, ConfigRevision: payload.ConfigRevision, PolicyRevision: payload.PolicyRevision, ProbeSetVersion: payload.ProbeSetVersion, IdentityKey: payload.IdentityKey}, nil
				}
				executor := &modelcheckowner.SchedulerExecutorMux{Runs: &modelcheckowner.SchedulerRunExecutor{Runtime: runtime, Build: build, Recovery: recovery.Complete, Scheduled: source.CompleteScheduled}, Health: &modelcheckowner.HealthSyncRetryExecutor{Projector: projector}}
				return source, executor
			},
		})
		if hostErr != nil {
			_ = keyModelStore.Close()
			fail(fmt.Errorf("open J3b Gateway owner host: %w", hostErr))
		}
		defer keyModelStore.Close()
		managementMux := http.NewServeMux()
		var captchaService *modelcheckauth.CaptchaService
		if !envBool("JUHE_AI_AUTH_CAPTCHA_DISABLED") {
			captchaService = modelcheckauth.NewCaptchaService(time.Now)
		}
		managementMux.Handle("/auth/", http.StripPrefix("/auth", &modelcheckauth.HTTPHandler{Auth: authenticator, Captcha: captchaService, TemporaryAccessIPAllowlist: commaList(os.Getenv("JUHE_AI_TEMPORARY_ACCESS_IP_ALLOWLIST"))}))
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
	auditInputConfig, err := auditlog.LoadInputServerConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load F3 audit input config: %w", err))
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
	operationConfig, err := operationlog.LoadConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load F4 operation-log config: %w", err))
	}
	var operationStore operationlog.Store
	var operationInputConfig operationlog.InputServerConfig
	if operationConfig.Enabled {
		if operationConfig.Mode == operationlog.ModePostgres {
			operationConfig.PostgresPool, err = postgresPools.Acquire(operationConfig.PostgresURL, "gateway-store", operationConfig.PostgresMaxOpenConns, operationConfig.PostgresMaxIdleConns)
			if err != nil {
				fail(fmt.Errorf("open shared F4 PostgreSQL pool: %w", err))
			}
		}
		operationInputConfig, err = operationlog.LoadInputServerConfig(os.Getenv)
		if err != nil {
			fail(fmt.Errorf("load F4 operation-log input config: %w", err))
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

	listener, err := net.Listen("tcp", *healthAddress)
	if err != nil {
		fail(fmt.Errorf("listen gateway health endpoint %q: %w", *healthAddress, err))
	}
	defer listener.Close()
	var auditRunning atomic.Bool
	var operationRunning atomic.Bool
	var j3bRunning atomic.Bool
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
			Name: "F3 audit-log-owner",
			Run: func(runCtx context.Context) error {
				auditRunning.Store(true)
				defer auditRunning.Store(false)
				return auditlog.RunInputServer(runCtx, auditStore, auditConfig, auditInputConfig, logger)
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
			Name: "F4 operation-log-owner",
			Run: func(runCtx context.Context) error {
				operationRunning.Store(true)
				defer operationRunning.Store(false)
				return operationlog.RunInputServer(runCtx, operationStore, operationConfig, operationInputConfig, logger)
			},
			Close: operationStore.Close,
		})
	}
	healthServer := &http.Server{
		Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet || request.URL.Path != "/health" {
				http.NotFound(response, request)
				return
			}
			ready := auditRunning.Load() && (!operationConfig.Enabled || operationRunning.Load()) && (j3bHostComponent.Run == nil || j3bRunning.Load()) && (!retentionEnabled || retentionRunning.Load()) && (!circuitRuntimeEnabled || circuitRuntimeRunning.Load())
			response.Header().Set("Content-Type", "application/json")
			if !ready {
				response.WriteHeader(http.StatusServiceUnavailable)
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"ready": ready, "ownerReady": ready, "ownerMode": ownerMode, "auditLogReady": auditRunning.Load(), "operationLogReady": operationRunning.Load(), "j3bReady": j3bRunning.Load(), "sessionRetentionReady": retentionRunning.Load(), "accountCircuitRuntimeReady": circuitRuntimeRunning.Load()})
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- healthServer.Serve(listener) }()
	logger.Info("juhe-ai-gateway started", "healthAddress", listener.Addr().String(), "f4Enabled", operationConfig.Enabled)
	runErr := supervisor.Run(ctx, components, logger)
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
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		fail(fmt.Errorf("gateway component supervisor stopped: %w", runErr))
	}
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
	listener, err := net.Listen("tcp", healthAddress)
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
