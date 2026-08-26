package accountbalance

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/schedulejitter"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Service struct {
	config    RuntimeConfig
	store     *Store
	inputDB   *sql.DB
	inputPool *pgpool.Handle
	reader    *PostgresDirectInputReader
	runner    *Runner
	ready     atomic.Bool
	logger    *slog.Logger
}

func NewService(config RuntimeConfig, logger *slog.Logger) (*Service, error) {
	if !config.Enabled {
		return nil, errors.New("J2 account-balance service 未启用")
	}
	if logger == nil {
		logger = slog.Default()
	}
	storeConfig := config.Store
	storeConfig.PostgresMaxOpenConns = config.PostgresMaxOpenConns
	storeConfig.PostgresMaxIdleConns = config.PostgresMaxIdleConns
	storeConfig.PostgresPool = config.PostgresPool
	store, err := OpenStore(storeConfig)
	if err != nil {
		return nil, err
	}
	if err := store.CheckSchema(context.Background()); err != nil {
		_ = store.Close()
		return nil, err
	}
	inputPool := config.InputPostgresPool
	if inputPool == nil {
		registry := pgpool.NewRegistry()
		inputPool, err = registry.Acquire("pgx", config.BusinessPostgresURL, "account-balance-input", config.InputPostgresMaxOpenConns, config.InputPostgresMaxIdleConns)
		if err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	db := inputPool.DB()
	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = db.PingContext(pingCtx)
	cancel()
	if err != nil {
		_ = inputPool.Close()
		_ = store.Close()
		return nil, err
	}
	reader, err := NewPostgresDirectInputReader(db, config.CredentialSecret, config.InputTTL, config.Now)
	if err != nil {
		_ = inputPool.Close()
		_ = store.Close()
		return nil, err
	}
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = reader.CheckContract(checkCtx)
	checkCancel()
	if err != nil {
		_ = inputPool.Close()
		_ = store.Close()
		return nil, err
	}
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: config.OwnerID, OwnerLeaseTTL: config.OwnerLease, AccountLeaseTTL: config.AccountLease, InputTTL: config.InputTTL, MaxConcurrent: config.MaxConcurrency, CredentialSecret: config.CredentialSecret, ProbeTimeout: config.ProbeTimeout, MaxResponseBytes: config.MaxResponseBytes, Now: config.Now})
	if err != nil {
		_ = inputPool.Close()
		_ = store.Close()
		return nil, err
	}
	service := &Service{config: config, store: store, inputDB: db, inputPool: inputPool, reader: reader, runner: runner, logger: logger}
	return service, nil
}

func (s *Service) Ready() bool { return s != nil && s.ready.Load() }

// RunManual executes one frozen manual input and returns the jobs-owned
// snapshot after the outcome has been appended. It is the only synchronous
// bridge used by the Node manual route in Go-owner mode.
func (s *Service) RunManual(ctx context.Context, input Input) (SnapshotRecord, RunReport, error) {
	if s == nil || s.runner == nil || s.store == nil {
		return SnapshotRecord{}, RunReport{}, errors.New("J2 account-balance service 未就绪")
	}
	input.Trigger = TriggerManual
	report, err := s.runner.RunManual(ctx, input)
	if err != nil {
		return SnapshotRecord{}, report, err
	}
	if itemErr, ok := report.Errors[input.AccountID]; ok {
		return SnapshotRecord{}, report, itemErr
	}
	if report.Stale > 0 {
		return SnapshotRecord{}, report, ErrOutcomeStale
	}
	if report.Skipped > 0 || report.Executed == 0 {
		return SnapshotRecord{}, report, ErrAccountLeaseHeld
	}
	outcome, found, err := s.store.LoadOutcome(ctx, OutcomeIDForInput(input))
	if err != nil {
		return SnapshotRecord{}, report, err
	}
	if !found {
		return SnapshotRecord{}, report, errors.New("J2 manual outcome 未生成结果")
	}
	return SnapshotRecord{AccountID: outcome.AccountID, InputVersion: outcome.InputVersion, ConfigRevision: outcome.ConfigRevision, Trigger: outcome.Trigger, Snapshot: outcome.Snapshot, NextRefreshAt: outcome.NextRefreshAt, UpdatedAt: outcome.ObservedAt}, report, nil
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.ready.Store(false)
	var first error
	if s.inputDB != nil {
		if s.inputPool != nil {
			first = s.inputPool.Close()
		} else {
			first = s.inputDB.Close()
		}
	}
	if err := s.store.Close(); first == nil {
		first = err
	}
	return first
}

func (s *Service) Run(ctx context.Context) error {
	if s == nil || s.runner == nil || s.store == nil {
		return errors.New("J2 account-balance service 未就绪")
	}
	if err := s.runCycle(ctx); err != nil {
		s.ready.Store(false)
		s.logger.Error("J2 余额刷新首轮失败", "error", err)
	} else {
		s.ready.Store(true)
	}
	timer := time.NewTimer(schedulejitter.Delay(s.config.ScanInterval))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if err := s.runCycle(ctx); err != nil {
				s.ready.Store(false)
				s.logger.Error("J2 余额刷新轮次失败", "error", err)
			} else {
				s.ready.Store(true)
			}
			timer.Reset(schedulejitter.Delay(s.config.ScanInterval))
		}
	}
}

func (s *Service) runCycle(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.config.CycleBudget)
	defer cancel()
	recovery, err := s.reader.LoadRecovery(ctx, s.config.RecoveryBatchSize)
	if err != nil {
		return err
	}
	if len(recovery) > 0 {
		report, runErr := s.runner.RunPeriodic(ctx, recovery)
		s.logger.Info("J2 丢失调度余额恢复完成", "seen", report.Seen, "executed", report.Executed, "skipped", report.Skipped, "stale", report.Stale, "errors", len(report.Errors))
		if runErr != nil {
			return runErr
		}
	}
	remaining := s.config.BatchSize - len(recovery)
	if remaining > 0 {
		periodic, loadErr := s.reader.LoadDue(ctx, remaining)
		if loadErr != nil {
			return loadErr
		}
		if len(periodic) > 0 {
			report, runErr := s.runner.RunPeriodic(ctx, periodic)
			s.logger.Info("J2 周期余额刷新完成", "seen", report.Seen, "executed", report.Executed, "skipped", report.Skipped, "stale", report.Stale, "errors", len(report.Errors))
			if runErr != nil {
				return runErr
			}
		}
	}
	first, err := s.reader.LoadFirstProbe(ctx, s.config.BatchSize)
	if err != nil {
		return err
	}
	if len(first) > 0 {
		report, runErr := s.runner.RunFirstProbe(ctx, first)
		s.logger.Info("J2 首次余额探测完成", "seen", report.Seen, "executed", report.Executed, "skipped", report.Skipped, "errors", len(report.Errors))
		if runErr != nil {
			return runErr
		}
	}
	return nil
}
