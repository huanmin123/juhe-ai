package main

import (
	"context"
	"crypto/subtle"
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
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/keymodelrecovery"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckruntime"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/runtimelog"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/tablemonitor"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
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
	ownerMode, err := ownermode.Load(os.Getenv)
	if err != nil {
		fail(err)
	}
	if !ownerMode.OwnsWork() {
		runPassiveJobs(*healthAddress, ownerMode, logger)
		return
	}
	postgresPools := pgpool.NewRegistry()
	defer postgresPools.Close()
	postgresPools.SetObserver(func(event pgpool.PoolEvent) {
		logger.Debug("jobs postgres pool event",
			"event", event.Kind,
			"role", event.Role,
			"max_open", event.MaxOpen,
			"max_idle", event.MaxIdle,
			"refs", event.Refs,
			"open", event.DBStats.OpenConnections,
			"in_use", event.DBStats.InUse,
			"idle", event.DBStats.Idle,
			"wait_count", event.DBStats.WaitCount,
			"wait_duration_ms", event.DBStats.WaitDuration.Milliseconds(),
		)
	})
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
	if cfg.Mode == tablemonitor.ModePostgres {
		cfg.PostgresPool, err = postgresPools.Acquire("pgx", cfg.PostgresURL, "jobs-store", cfg.PostgresMaxOpenConns, cfg.PostgresMaxIdleConns)
		if err != nil {
			fail(fmt.Errorf("open F2 shared PostgreSQL pool: %w", err))
		}
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
	var accountHealthInputPool *pgpool.Handle
	var accountHealthRunner *accounthealth.Runner
	var accountHealthReader *accounthealth.PostgresDirectInputReader
	if accountHealthConfig.Enabled {
		if accountHealthConfig.Store.Mode == accounthealth.StorePostgres {
			accountHealthConfig.Store.PostgresPool, err = postgresPools.Acquire("pgx", accountHealthConfig.Store.PostgresURL, "jobs-store", accountHealthConfig.Store.PostgresMaxOpenConns, accountHealthConfig.Store.PostgresMaxIdleConns)
			if err != nil {
				fail(fmt.Errorf("open J1 shared PostgreSQL jobs pool: %w", err))
			}
		}
		accountHealthStore, err = accounthealth.OpenStore(accountHealthConfig.Store)
		if err != nil {
			fail(fmt.Errorf("open J1 account-health store: %w", err))
		}
		if err := accountHealthStore.EnsureSchema(context.Background()); err != nil {
			_ = accountHealthStore.Close()
			fail(fmt.Errorf("initialize J1 account-health schema: %w", err))
		}
		if accountHealthConfig.InputSource == "postgres" {
			accountHealthInputPool, err = postgresPools.Acquire("pgx", accountHealthConfig.BusinessPostgresURL, "business-input", accountHealthConfig.DirectInputPostgresMaxOpenConns, accountHealthConfig.DirectInputPostgresMaxIdleConns)
			if err != nil {
				_ = accountHealthStore.Close()
				fail(fmt.Errorf("open J1 account-health direct-input database: %w", err))
			}
			accountHealthInputDB = accountHealthInputPool.DB()
			pingContext, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
			pingErr := accountHealthInputDB.PingContext(pingContext)
			pingCancel()
			if pingErr != nil {
				_ = accountHealthInputPool.Close()
				_ = accountHealthStore.Close()
				fail(fmt.Errorf("ping J1 account-health direct-input database: %w", pingErr))
			}
			reader, readerErr := accounthealth.NewPostgresDirectInputReader(accountHealthInputDB, accountHealthConfig.CredentialSecret, accountHealthConfig.InputTTL, accountHealthConfig.Now)
			if readerErr != nil {
				_ = accountHealthInputPool.Close()
				_ = accountHealthStore.Close()
				fail(fmt.Errorf("configure J1 account-health direct-input reader: %w", readerErr))
			}
			contractContext, contractCancel := context.WithTimeout(context.Background(), 10*time.Second)
			contractErr := reader.CheckContract(contractContext)
			contractCancel()
			if contractErr != nil {
				_ = accountHealthInputPool.Close()
				_ = accountHealthStore.Close()
				fail(fmt.Errorf("verify J1 account-health direct-input contract: %w", contractErr))
			}
			accountHealthReader = reader
			accountHealthRunner = accounthealth.NewRunnerWithDirectInputReader(accountHealthConfig, accountHealthStore, logger, reader)
		} else {
			accountHealthRunner = accounthealth.NewRunner(accountHealthConfig, accountHealthStore, logger)
		}
	}
	modelRecoveryConfig, err := keymodelrecovery.LoadRedisConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load model-recovery config: %w", err))
	}
	var modelRecoveryStore *keymodelrecovery.RedisStore
	var modelRecoveryRunner *keymodelrecovery.Runner
	if modelRecoveryConfig.Enabled {
		if accountHealthReader == nil {
			fail(errors.New("启用 model-recovery 必须同时启用 PostgreSQL J1 direct input reader"))
		}
		modelRecoveryStore, err = keymodelrecovery.OpenRedisStore(modelRecoveryConfig)
		if err != nil {
			fail(fmt.Errorf("open model-recovery Redis store: %w", err))
		}
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = modelRecoveryStore.Ping(pingCtx)
		pingCancel()
		if err != nil {
			_ = modelRecoveryStore.Close()
			fail(fmt.Errorf("ping model-recovery Redis store: %w", err))
		}
		modelRecoveryRunner = keymodelrecovery.NewRunner(modelRecoveryStore, accountHealthReader, logger)
	}
	accountBalanceConfig, err := accountbalance.LoadRuntimeConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load J2 account-balance config: %w", err))
	}
	var accountBalanceService *accountbalance.Service
	if accountBalanceConfig.Enabled {
		accountBalanceConfig.PostgresPool, err = postgresPools.Acquire("pgx", accountBalanceConfig.Store.PostgresURL, "jobs-store", accountBalanceConfig.PostgresMaxOpenConns, accountBalanceConfig.PostgresMaxIdleConns)
		if err != nil {
			fail(fmt.Errorf("open J2 shared PostgreSQL jobs pool: %w", err))
		}
		accountBalanceConfig.InputPostgresPool, err = postgresPools.Acquire("pgx", accountBalanceConfig.BusinessPostgresURL, "business-input", accountBalanceConfig.InputPostgresMaxOpenConns, accountBalanceConfig.InputPostgresMaxIdleConns)
		if err != nil {
			_ = accountBalanceConfig.PostgresPool.Close()
			fail(fmt.Errorf("open J2 shared PostgreSQL input pool: %w", err))
		}
		accountBalanceService, err = accountbalance.NewService(accountBalanceConfig, logger)
		if err != nil {
			fail(fmt.Errorf("initialize J2 account-balance service: %w", err))
		}
	}
	j3Config, err := proxylatency.LoadRuntimeConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load J3a proxy-latency config: %w", err))
	}
	j3ManagementConfig, err := proxylatency.LoadManualAdminConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load J3a proxy-latency management config: %w", err))
	}
	if j3ManagementConfig.Enabled && !j3Config.Enabled {
		fail(errors.New("启用 J3a 管理接口前必须启用 J3a Go owner"))
	}
	var j3Store *proxylatency.Store
	var j3InputDB *sql.DB
	var j3InputPool *pgpool.Handle
	var j3ResultDB *sql.DB
	var j3ResultPool *pgpool.Handle
	var j3Runner *proxylatency.Runner
	var j3Projector *proxylatency.ResultProjector
	var j3ManagementDB *sql.DB
	var j3ManagementPool *pgpool.Handle
	var j3ManagementSource *proxylatency.PostgresManualAdminSource
	if j3Config.Enabled {
		j3Config.Store.PostgresMaxOpenConns = j3Config.PostgresMaxOpenConns
		j3Config.Store.PostgresMaxIdleConns = j3Config.PostgresMaxIdleConns
		j3Config.Store.PostgresPool, err = postgresPools.Acquire("pgx", j3Config.Store.PostgresURL, "jobs-store", j3Config.Store.PostgresMaxOpenConns, j3Config.Store.PostgresMaxIdleConns)
		if err != nil {
			fail(fmt.Errorf("open J3a shared PostgreSQL jobs pool: %w", err))
		}
		j3Store, err = proxylatency.OpenStore(j3Config.Store)
		if err != nil {
			fail(fmt.Errorf("open J3a proxy-latency jobs store: %w", err))
		}
		if err := j3Store.CheckSchema(context.Background()); err != nil {
			_ = j3Store.Close()
			fail(fmt.Errorf("verify pre-provisioned J3a proxy-latency jobs schema: %w", err))
		}
		j3InputPool, err = postgresPools.Acquire("pgx", j3Config.BusinessPostgresURL, "business-input", j3Config.InputPostgresMaxOpenConns, j3Config.InputPostgresMaxIdleConns)
		if err != nil {
			_ = j3Store.Close()
			fail(fmt.Errorf("open J3a proxy-latency direct-input database: %w", err))
		}
		j3InputDB = j3InputPool.DB()
		pingContext, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
		pingErr := j3InputDB.PingContext(pingContext)
		pingCancel()
		if pingErr != nil {
			_ = j3InputPool.Close()
			_ = j3Store.Close()
			fail(fmt.Errorf("ping J3a proxy-latency direct-input database: %w", pingErr))
		}
		reader, readerErr := proxylatency.NewPostgresDirectInputReader(j3InputDB, j3Config.InputTTL, j3Config.Now)
		if readerErr != nil {
			_ = j3InputPool.Close()
			_ = j3Store.Close()
			fail(fmt.Errorf("configure J3a proxy-latency direct-input reader: %w", readerErr))
		}
		contractContext, contractCancel := context.WithTimeout(context.Background(), 10*time.Second)
		contractErr := reader.CheckContract(contractContext)
		contractCancel()
		if contractErr != nil {
			_ = j3InputPool.Close()
			_ = j3Store.Close()
			fail(fmt.Errorf("verify J3a proxy-latency direct-input contract: %w", contractErr))
		}
		j3ResultPool, err = postgresPools.Acquire("pgx", j3Config.ResultPostgresURL, "business-result", j3Config.InputPostgresMaxOpenConns, j3Config.InputPostgresMaxIdleConns)
		if err != nil {
			_ = j3InputPool.Close()
			_ = j3Store.Close()
			fail(fmt.Errorf("open J3a Go business-result PostgreSQL pool: %w", err))
		}
		j3ResultDB = j3ResultPool.DB()
		resultProjector, projectorErr := proxylatency.NewResultProjector(j3Store, j3ResultDB, proxylatency.ResultProjectorConfig{PollInterval: time.Second, BatchSize: j3Config.BatchSize, Now: j3Config.Now}, logger)
		if projectorErr != nil {
			_ = j3ResultPool.Close()
			_ = j3InputPool.Close()
			_ = j3Store.Close()
			fail(fmt.Errorf("initialize J3a Go business-result projector: %w", projectorErr))
		}
		projectorContext, projectorCancel := context.WithTimeout(context.Background(), 10*time.Second)
		projectorContractErr := resultProjector.CheckContract(projectorContext)
		projectorCancel()
		if projectorContractErr != nil {
			_ = j3ResultPool.Close()
			_ = j3InputPool.Close()
			_ = j3Store.Close()
			fail(fmt.Errorf("verify J3a Go business-result contract: %w", projectorContractErr))
		}
		j3Runner = proxylatency.NewRunner(j3Config, j3Store, reader, logger)
		j3Runner.SetResultProjector(resultProjector)
		j3Projector = resultProjector
		if j3ManagementConfig.Enabled {
			j3ManagementPool, err = postgresPools.Acquire("pgx", j3ManagementConfig.PostgresURL, "proxy-latency-management", j3ManagementConfig.MaxOpenConns, j3ManagementConfig.MaxIdleConns)
			if err != nil {
				_ = j3ResultPool.Close()
				_ = j3InputPool.Close()
				_ = j3Store.Close()
				fail(fmt.Errorf("open J3a management PostgreSQL pool: %w", err))
			}
			j3ManagementDB = j3ManagementPool.DB()
			j3ManagementSource, err = proxylatency.NewPostgresManualAdminSource(j3ManagementDB, j3Config.Now)
			if err != nil {
				_ = j3ManagementPool.Close()
				_ = j3ResultPool.Close()
				_ = j3InputPool.Close()
				_ = j3Store.Close()
				fail(fmt.Errorf("initialize J3a management source: %w", err))
			}
			managementContractCtx, managementContractCancel := context.WithTimeout(context.Background(), 10*time.Second)
			managementContractErr := j3ManagementSource.CheckContract(managementContractCtx)
			managementContractCancel()
			if managementContractErr != nil {
				_ = j3ManagementPool.Close()
				_ = j3ResultPool.Close()
				_ = j3InputPool.Close()
				_ = j3Store.Close()
				fail(fmt.Errorf("verify J3a management PostgreSQL contract: %w", managementContractErr))
			}
		}
	}
	j3bConfig, err := modelcheckruntime.LoadConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load J3b model-check config: %w", err))
	}
	if j3bConfig.Enabled {
		// Solution A reserves the J3b runtime for Gateway. Keep this guard even
		// when a future config parser changes, so jobs can never become a second
		// owner through an accidental startup path.
		fail(errors.New("J3b runtime is Gateway-owned; juhe-ai-jobs cannot be enabled"))
	}

	listener, err := listenLoopback(*healthAddress)
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
				if accountHealthInputPool != nil {
					closeErr = accountHealthInputPool.Close()
				}
				if err := accountHealthStore.Close(); err != nil && closeErr == nil {
					closeErr = err
				}
				return closeErr
			},
		})
	}
	if modelRecoveryRunner != nil {
		components = append(components, supervisor.Component{
			Name:  "model-recovery key-model",
			Run:   modelRecoveryRunner.Run,
			Close: modelRecoveryStore.Close,
		})
	}
	accountBalanceReady := func() bool { return true }
	if accountBalanceService != nil {
		accountBalanceReady = accountBalanceService.Ready
		components = append(components, supervisor.Component{
			Name:  "J2 account-balance",
			Run:   accountBalanceService.Run,
			Close: accountBalanceService.Close,
		})
	}
	j3Ready := func() bool { return true }
	if j3Runner != nil {
		j3Ready = j3Runner.Ready
		components = append(components, supervisor.Component{
			Name: "J3a Go business-result-projector",
			Run:  j3Projector.Run,
			Close: func() error {
				if j3ResultPool != nil {
					return j3ResultPool.Close()
				}
				return nil
			},
		})
		components = append(components, supervisor.Component{
			Name: "J3a proxy-latency",
			Run:  j3Runner.Run,
			Close: func() error {
				var closeErr error
				if j3InputPool != nil {
					closeErr = j3InputPool.Close()
				}
				if err := j3Store.Close(); err != nil && closeErr == nil {
					closeErr = err
				}
				return closeErr
			},
		})
	}
	if j3ManagementConfig.Enabled {
		managementListener, listenErr := net.Listen("tcp", j3ManagementConfig.ListenAddress)
		if listenErr != nil {
			fail(fmt.Errorf("listen J3a management endpoint %q: %w", j3ManagementConfig.ListenAddress, listenErr))
		}
		managementServer := &http.Server{
			Handler:           proxylatency.NewManualAdminHandler(j3Runner, j3ManagementSource, proxylatency.NewPostgresManualAdminAuditAppender(j3ManagementDB), j3ManagementConfig.RequestDeadline, logger),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       j3ManagementConfig.RequestDeadline + 5*time.Second,
			WriteTimeout:      j3ManagementConfig.RequestDeadline + 5*time.Second,
			IdleTimeout:       30 * time.Second,
		}
		components = append(components, supervisor.Component{
			Name: "J3a management API",
			Run: func(context.Context) error {
				err := managementServer.Serve(managementListener)
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}
				return err
			},
			Close: func() error {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				shutdownErr := managementServer.Shutdown(shutdownCtx)
				poolErr := j3ManagementPool.Close()
				if shutdownErr != nil {
					return shutdownErr
				}
				return poolErr
			},
		})
	}
	j3bReady := func() bool { return true }
	healthServer := &http.Server{
		Handler: jobsHTTPHandler(ownerMode, &runtimeRunning, tableRunner.Ready, accountHealthConfig.Enabled, accountHealthReady, accountBalanceConfig.Enabled, accountBalanceReady, accountBalanceService, accountBalanceConfig.ManualHTTPSecret, j3Config.Enabled, j3Ready, func() proxylatency.RunnerStatus {
			if j3Runner == nil {
				return proxylatency.RunnerStatus{}
			}
			return j3Runner.Status()
		}, func() (proxylatency.RunnerStatus, bool) {
			if j3Runner == nil {
				return proxylatency.RunnerStatus{}, true
			}
			return j3Runner.Snapshot()
		}, false, j3bReady),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- healthServer.Serve(listener) }()
	logger.Info("juhe-ai-jobs started", "healthAddress", listener.Addr().String(), "job", "table-monitor", "accountHealthEnabled", accountHealthConfig.Enabled, "accountHealthInputSource", accountHealthConfig.InputSource, "modelRecoveryEnabled", modelRecoveryConfig.Enabled, "accountBalanceEnabled", accountBalanceConfig.Enabled, "proxyLatencyEnabled", j3Config.Enabled, "modelCheckEnabled", false)
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

// runPassiveJobs never initializes stores or leases. It exists only for a
// candidate readiness endpoint during standby/drain; ownerReady stays false.
func runPassiveJobs(healthAddress string, ownerMode ownermode.Mode, logger *slog.Logger) {
	listener, err := listenLoopback(healthAddress)
	if err != nil {
		fail(fmt.Errorf("listen passive jobs health endpoint %q: %w", healthAddress, err))
	}
	defer listener.Close()
	server := &http.Server{Handler: passiveJobsHealthHandler(ownerMode), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("juhe-ai-jobs passive", "healthAddress", listener.Addr().String(), "ownerMode", ownerMode)
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := server.Shutdown(shutdownCtx)
	cancel()
	serveResult := <-serveErr
	if shutdownErr != nil {
		fail(fmt.Errorf("shutdown passive jobs health endpoint: %w", shutdownErr))
	}
	if serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed) {
		fail(fmt.Errorf("passive jobs health endpoint stopped: %w", serveResult))
	}
}

func listenLoopback(address string) (net.Listener, error) {
	if err := validateLoopbackListenAddress(address); err != nil {
		return nil, err
	}
	return net.Listen("tcp", address)
}

func validateLoopbackListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid loopback listen address %q: %w", address, err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("invalid loopback listen address %q: port must be between 1 and 65535", address)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !isLoopbackListenIP(ip) {
		return fmt.Errorf("invalid loopback listen address %q: host must be localhost or a loopback IP", address)
	}
	return nil
}

func isLoopbackListenIP(ip net.IP) bool {
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4[0] == 127
	}
	return ip.Equal(net.IPv6loopback)
}

func passiveJobsHealthHandler(ownerMode ownermode.Mode) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"ready":                 false,
			"ownerReady":            false,
			"ownerMode":             ownerMode,
			"runtimeLogOwnerHeld":   false,
			"tableMonitorReady":     false,
			"accountHealthEnabled":  false,
			"accountHealthReady":    false,
			"accountBalanceEnabled": false,
			"accountBalanceReady":   false,
			"proxyLatencyEnabled":   false,
			"proxyLatencyReady":     false,
			"modelCheckEnabled":     false,
			"modelCheckReady":       false,
		})
	})
}

func jobsHTTPHandler(ownerMode ownermode.Mode, runtimeRunning *atomic.Bool, tableMonitorReady func() bool, accountHealthEnabled bool, accountHealthReady func() bool, accountBalanceEnabled bool, accountBalanceReady func() bool, accountBalanceService *accountbalance.Service, accountBalanceManualSecret string, j3 ...any) http.Handler {
	mux := http.NewServeMux()
	readinessArgs := append([]any{accountBalanceEnabled, accountBalanceReady}, j3...)
	mux.Handle("/health", healthHandler(ownerMode, runtimeRunning, tableMonitorReady, accountHealthEnabled, accountHealthReady, readinessArgs...))
	mux.HandleFunc("/account-balance/manual", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || accountBalanceService == nil {
			http.NotFound(response, request)
			return
		}
		if !matchesAccountBalanceManualSecret(request, accountBalanceManualSecret) {
			response.Header().Set("WWW-Authenticate", `Bearer realm="juhe-ai-jobs"`)
			http.Error(response, "J2 manual bridge 未授权", http.StatusUnauthorized)
			return
		}
		request.Body = http.MaxBytesReader(response, request.Body, 512<<10)
		var envelope struct {
			Input accountbalance.Input `json:"input"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&envelope); err != nil {
			http.Error(response, "J2 manual input 无效", http.StatusBadRequest)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			http.Error(response, "J2 manual input 不得包含尾随 JSON", http.StatusBadRequest)
			return
		}
		if envelope.Input.Trigger == "" {
			envelope.Input.Trigger = accountbalance.TriggerManual
		}
		record, _, err := accountBalanceService.RunManual(request.Context(), envelope.Input)
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, accountbalance.ErrAccountLeaseHeld) {
				status = http.StatusConflict
			}
			if errors.Is(err, accountbalance.ErrOutcomeStale) {
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(response).Encode(manualHandoverResult(envelope.Input, accountbalance.Snapshot{Status: accountbalance.StatusPending}, envelope.Input.NextRefreshAt, false, "stale"))
				return
			}
			http.Error(response, err.Error(), status)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(manualHandoverResult(envelope.Input, record.Snapshot, record.NextRefreshAt, true, manualOutcome(record.Snapshot.Status)))
	})
	return mux
}

func matchesAccountBalanceManualSecret(request *http.Request, expected string) bool {
	if request == nil || len(expected) < 32 {
		return false
	}
	const prefix = "Bearer "
	provided := request.Header.Get("Authorization")
	if len(provided) < len(prefix) || provided[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided[len(prefix):]), []byte(expected)) == 1
}

func manualHandoverResult(input accountbalance.Input, snapshot accountbalance.Snapshot, nextRefreshAfter *time.Time, committed bool, outcome string) map[string]any {
	result := map[string]any{
		"schemaVersion":    1,
		"job":              "account-balance-refresh",
		"accountId":        input.AccountID,
		"systemAccountId":  input.SystemAccountID,
		"configRevision":   input.ConfigRevision,
		"nextRefreshAfter": nextRefreshAfter,
		"outcome":          outcome,
		"committed":        committed,
		"snapshot":         snapshot,
	}
	if input.Trigger != accountbalance.TriggerManual {
		result["expectedNextRefreshAt"] = input.NextRefreshAt
	}
	return map[string]any{
		"schemaVersion": 1,
		"job":           "account-balance-refresh",
		"result":        result,
	}
}

func manualOutcome(status accountbalance.Status) string {
	if status == accountbalance.StatusUnsupported {
		return "unsupported"
	}
	if status == accountbalance.StatusFresh || status == accountbalance.StatusUnlimited {
		return "refreshed"
	}
	return "failed"
}

func healthHandler(ownerMode ownermode.Mode, runtimeRunning *atomic.Bool, tableMonitorReady func() bool, accountHealthEnabled bool, accountHealthReady func() bool, j2 ...any) http.Handler {
	accountBalanceEnabled := false
	accountBalanceReady := func() bool { return true }
	proxyLatencyEnabled := false
	proxyLatencyReady := func() bool { return true }
	proxyLatencyStatus := func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} }
	proxyLatencySnapshot := func() (proxylatency.RunnerStatus, bool) {
		return proxyLatencyStatus(), proxyLatencyReady()
	}
	modelCheckEnabled := false
	modelCheckReady := func() bool { return true }
	if len(j2) > 0 {
		if value, ok := j2[0].(bool); ok {
			accountBalanceEnabled = value
		}
	}
	if len(j2) > 1 {
		if value, ok := j2[1].(func() bool); ok {
			accountBalanceReady = value
		}
	}
	if len(j2) > 2 {
		if value, ok := j2[2].(bool); ok {
			proxyLatencyEnabled = value
		}
	}
	if len(j2) > 3 {
		if value, ok := j2[3].(func() bool); ok {
			proxyLatencyReady = value
		}
	}
	if len(j2) > 4 {
		if value, ok := j2[4].(func() proxylatency.RunnerStatus); ok {
			proxyLatencyStatus = value
		}
	}
	if len(j2) > 5 {
		if value, ok := j2[5].(func() (proxylatency.RunnerStatus, bool)); ok {
			proxyLatencySnapshot = value
		}
	}
	if len(j2) > 6 {
		if value, ok := j2[6].(bool); ok {
			modelCheckEnabled = value
		}
	}
	if len(j2) > 7 {
		if value, ok := j2[7].(func() bool); ok {
			modelCheckReady = value
		}
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			http.NotFound(response, request)
			return
		}
		runtimeLogOwnerHeld := runtimeRunning.Load()
		tableMonitorIsReady := tableMonitorReady()
		accountHealthIsReady := !accountHealthEnabled || accountHealthReady()
		accountBalanceIsReady := !accountBalanceEnabled || accountBalanceReady()
		proxyStatus, proxyReady := proxyLatencySnapshot()
		proxyLatencyIsReady := !proxyLatencyEnabled || proxyReady
		modelCheckIsReady := !modelCheckEnabled || modelCheckReady()
		response.Header().Set("Content-Type", "application/json")
		ready := runtimeLogOwnerHeld && tableMonitorIsReady && accountHealthIsReady && accountBalanceIsReady && proxyLatencyIsReady && modelCheckIsReady
		_ = json.NewEncoder(response).Encode(map[string]any{
			"ready":                         ready,
			"ownerReady":                    ready,
			"ownerMode":                     ownerMode,
			"runtimeLogOwnerHeld":           runtimeLogOwnerHeld,
			"tableMonitorReady":             tableMonitorIsReady,
			"accountHealthEnabled":          accountHealthEnabled,
			"accountHealthReady":            accountHealthIsReady,
			"accountBalanceEnabled":         accountBalanceEnabled,
			"accountBalanceReady":           accountBalanceIsReady,
			"proxyLatencyEnabled":           proxyLatencyEnabled,
			"proxyLatencyReady":             proxyLatencyIsReady,
			"modelCheckEnabled":             modelCheckEnabled,
			"modelCheckReady":               modelCheckIsReady,
			"proxyLatencyOwnerHeld":         proxyStatus.OwnerHeld,
			"proxyLatencyLastCycleAt":       proxylatencyTime(proxyStatus.LastCycleAt),
			"proxyLatencyLastSuccessAt":     proxylatencyTime(proxyStatus.LastSuccess),
			"proxyLatencyLastError":         proxyStatus.LastError,
			"proxyLatencyInputs":            proxyStatus.Inputs,
			"proxyLatencyExecuted":          proxyStatus.Executed,
			"proxyLatencyFailures":          proxyStatus.ProxyFailures,
			"proxyLatencySelected":          proxyStatus.Selected,
			"proxyLatencyTarget":            proxyStatus.Target,
			"proxyLatencyClaimed":           proxyStatus.Claimed,
			"proxyLatencyStarted":           proxyStatus.Started,
			"proxyLatencyProcessed":         proxyStatus.Processed,
			"proxyLatencySkippedLeases":     proxyStatus.SkippedLeases,
			"proxyLatencyDeferred":          proxyStatus.Deferred,
			"proxyLatencyExecutionFailures": proxyStatus.ExecutionFailures,
			"proxyLatencyReleaseFailures":   proxyStatus.ReleaseFailures,
			"proxyLatencyPartial":           proxyStatus.Partial,
		})
	})
}

func proxylatencyTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
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
