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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckowner"
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
	if j3bConfig.Enabled {
		fail(errors.New("J3b Gateway owner config is valid but runtime/source/auth factories are not attached; keep JUHE_AI_J3B_ENABLED=false until schema preflight and complete owner wiring are present"))
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
			ready := auditRunning.Load() && (!operationConfig.Enabled || operationRunning.Load())
			response.Header().Set("Content-Type", "application/json")
			if !ready {
				response.WriteHeader(http.StatusServiceUnavailable)
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"ready": ready, "ownerReady": ready, "ownerMode": ownerMode, "auditLogReady": auditRunning.Load(), "operationLogReady": operationRunning.Load()})
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- healthServer.Serve(listener) }()
	logger.Info("juhe-ai-gateway started", "healthAddress", listener.Addr().String(), "f4Enabled", operationConfig.Enabled)
	runErr := supervisor.Run(ctx, components, logger)
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

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
