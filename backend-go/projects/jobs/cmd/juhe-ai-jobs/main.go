package main

import (
	"context"
	"database/sql"
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
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/runtimelog"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/tablemonitor"
	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
)

func main() {
	version := flag.Bool("version", false, "print the jobs project contract version")
	check := flag.Bool("check-boundary", false, "verify the scaffold boundary")
	once := flag.Bool("once", false, "run one F2 table-monitor sampling cycle and exit")
	runtimeLegacyMigration := flag.Bool("migrate-runtime-log-legacy-sqlite", false, "offline F1 legacy SQLite migration")
	healthAddress := flag.String("health-listen-address", envOrDefault("JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS", "127.0.0.1:3305"), "loopback health listen address")
	flag.Parse()
	if *version {
		fmt.Printf("juhe-ai-jobs project=%s contract=%s\n", contracts.ProjectJobs, contracts.ArchitectureVersion)
		return
	}
	if *check {
		fmt.Println("juhe-ai-jobs boundary=ready runtime=table-monitor-owner")
		return
	}
	if flag.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unsupported jobs arguments: %v\n", flag.Args())
		os.Exit(2)
	}
	if *once && *runtimeLegacyMigration {
		fmt.Fprintln(os.Stderr, "--once and --migrate-runtime-log-legacy-sqlite are mutually exclusive")
		os.Exit(2)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	runtimeConfig, err := runtimelog.LoadConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load F1 runtime-log-indexer config: %w", err))
	}
	if runtimeConfig.Once {
		fail(errors.New("JUHE_AI_RUNTIME_LOG_ONCE=true is not supported by juhe-ai-jobs; use --migrate-runtime-log-legacy-sqlite for the explicit offline F1 migration"))
	}
	if *runtimeLegacyMigration {
		runRuntimeLegacyMigration(runtimeConfig)
		return
	}
	runtimeStore, err := runtimelog.OpenStore(context.Background(), runtimeConfig)
	if err != nil {
		fail(fmt.Errorf("open F1 runtime-log-indexer store: %w", err))
	}
	defer runtimeStore.Close()
	if err := runtimelog.EnsureSchema(context.Background(), runtimeStore); err != nil {
		fail(fmt.Errorf("initialize F1 runtime-log-indexer schema: %w", err))
	}
	if err := runtimeStore.CheckSchema(context.Background()); err != nil {
		fail(fmt.Errorf("verify F1 runtime-log-indexer schema: %w", err))
	}
	cfg, err := tablemonitor.LoadConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load F2 table-monitor config: %w", err))
	}
	store, err := tablemonitor.OpenStore(cfg)
	if err != nil {
		fail(fmt.Errorf("open F2 table-monitor store: %w", err))
	}
	defer store.Close()
	if err := store.EnsureSchema(context.Background()); err != nil {
		fail(fmt.Errorf("initialize F2 table-monitor schema: %w", err))
	}
	if *once {
		result, err := tablemonitor.RunSingleCycle(context.Background(), cfg, store)
		if err != nil {
			fail(fmt.Errorf("run F2 table-monitor sampling cycle: %w", err))
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			fail(fmt.Errorf("encode F2 table-monitor result: %w", err))
		}
		return
	}
	accountHealthConfig, err := accounthealth.LoadConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load J1 account-health config: %w", err))
	}
	var accountHealthStore *accounthealth.Store
	var accountHealthInputDB *sql.DB
	var accountHealthRunner *accounthealth.Runner
	if accountHealthConfig.Enabled {
		accountHealthStore, err = accounthealth.OpenStore(accountHealthConfig.Store)
		if err != nil {
			fail(fmt.Errorf("open J1 account-health store: %w", err))
		}
		if err := accountHealthStore.EnsureSchema(context.Background()); err != nil {
			_ = accountHealthStore.Close()
			fail(fmt.Errorf("initialize J1 account-health schema: %w", err))
		}
		if accountHealthConfig.InputSource == "postgres" {
			accountHealthInputDB, err = sql.Open("pgx", accountHealthConfig.BusinessPostgresURL)
			if err != nil {
				_ = accountHealthStore.Close()
				fail(fmt.Errorf("open J1 account-health direct-input database: %w", err))
			}
			accountHealthInputDB.SetMaxOpenConns(4)
			accountHealthInputDB.SetMaxIdleConns(4)
			pingContext, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
			pingErr := accountHealthInputDB.PingContext(pingContext)
			pingCancel()
			if pingErr != nil {
				_ = accountHealthInputDB.Close()
				_ = accountHealthStore.Close()
				fail(fmt.Errorf("ping J1 account-health direct-input database: %w", pingErr))
			}
			reader, readerErr := accounthealth.NewPostgresDirectInputReader(accountHealthInputDB, accountHealthConfig.CredentialSecret, accountHealthConfig.InputTTL, accountHealthConfig.Now)
			if readerErr != nil {
				_ = accountHealthInputDB.Close()
				_ = accountHealthStore.Close()
				fail(fmt.Errorf("configure J1 account-health direct-input reader: %w", readerErr))
			}
			contractContext, contractCancel := context.WithTimeout(context.Background(), 10*time.Second)
			contractErr := reader.CheckContract(contractContext)
			contractCancel()
			if contractErr != nil {
				_ = accountHealthInputDB.Close()
				_ = accountHealthStore.Close()
				fail(fmt.Errorf("verify J1 account-health direct-input contract: %w", contractErr))
			}
			accountHealthRunner = accounthealth.NewRunnerWithDirectInputReader(accountHealthConfig, accountHealthStore, logger, reader)
		} else {
			accountHealthRunner = accounthealth.NewRunner(accountHealthConfig, accountHealthStore, logger)
		}
	}

	listener, err := net.Listen("tcp", *healthAddress)
	if err != nil {
		fail(fmt.Errorf("listen jobs health endpoint %q: %w", *healthAddress, err))
	}
	defer listener.Close()
	tableRunner := tablemonitor.NewRunner(cfg, store, logger)
	runtimeIndexer := runtimelog.NewIndexer(runtimeConfig, runtimeStore)
	var runtimeRunning atomic.Bool
	components := []supervisor.Component{
		{
			Name: "F1 runtime-log-indexer",
			Run: func(runCtx context.Context) error {
				runtimeRunning.Store(true)
				defer runtimeRunning.Store(false)
				return runtimelog.RunWithOwnerLease(runCtx, runtimeConfig, runtimeStore, runtimeIndexer.Run)
			},
			Close: runtimeStore.Close,
		},
		{
			Name:  "F2 table-monitor",
			Run:   tableRunner.Run,
			Close: store.Close,
		},
	}
	accountHealthReady := func() bool { return true }
	if accountHealthRunner != nil {
		accountHealthReady = accountHealthRunner.Ready
		components = append(components, supervisor.Component{
			Name: "J1 account-health",
			Run:  accountHealthRunner.Run,
			Close: func() error {
				var closeErr error
				if accountHealthInputDB != nil {
					closeErr = accountHealthInputDB.Close()
				}
				if err := accountHealthStore.Close(); err != nil && closeErr == nil {
					closeErr = err
				}
				return closeErr
			},
		})
	}
	healthServer := &http.Server{
		Handler:           healthHandler(&runtimeRunning, tableRunner.Ready, accountHealthConfig.Enabled, accountHealthReady),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- healthServer.Serve(listener) }()
	logger.Info("juhe-ai-jobs started", "healthAddress", listener.Addr().String(), "job", "table-monitor", "accountHealthEnabled", accountHealthConfig.Enabled, "accountHealthInputSource", accountHealthConfig.InputSource)
	runErr := supervisor.Run(ctx, components, logger)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := healthServer.Shutdown(shutdownCtx)
	shutdownCancel()
	serveResult := <-serveErr
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		fail(fmt.Errorf("F2 table-monitor runner stopped: %w", runErr))
	}
	if shutdownErr != nil {
		fail(fmt.Errorf("shutdown jobs health endpoint: %w", shutdownErr))
	}
	if serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed) {
		fail(fmt.Errorf("jobs health endpoint stopped: %w", serveResult))
	}
}

func healthHandler(runtimeRunning *atomic.Bool, tableMonitorReady func() bool, accountHealthEnabled bool, accountHealthReady func() bool) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			http.NotFound(response, request)
			return
		}
		runtimeLogOwnerHeld := runtimeRunning.Load()
		tableMonitorIsReady := tableMonitorReady()
		accountHealthIsReady := !accountHealthEnabled || accountHealthReady()
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"ready":                runtimeLogOwnerHeld && tableMonitorIsReady && accountHealthIsReady,
			"runtimeLogOwnerHeld":  runtimeLogOwnerHeld,
			"tableMonitorReady":    tableMonitorIsReady,
			"accountHealthEnabled": accountHealthEnabled,
			"accountHealthReady":   accountHealthIsReady,
		})
	})
}

func runRuntimeLegacyMigration(config runtimelog.Config) {
	store, err := runtimelog.OpenStore(context.Background(), config)
	if err != nil {
		fail(fmt.Errorf("open F1 runtime-log-indexer store: %w", err))
	}
	defer store.Close()
	if err := runtimelog.EnsureSchema(context.Background(), store); err != nil {
		fail(fmt.Errorf("initialize F1 runtime-log-indexer schema: %w", err))
	}
	if err := store.CheckSchema(context.Background()); err != nil {
		fail(fmt.Errorf("verify F1 runtime-log-indexer schema: %w", err))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runtimelog.RunWithOwnerLease(ctx, config, store, func(ownerCtx context.Context) error {
		return runtimelog.MigrateLegacySQLite(ownerCtx, config, store)
	}); err != nil {
		fail(err)
	}
	fmt.Fprintln(os.Stdout, "旧运行日志 SQLite 数据迁移和完整性校验完成")
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
