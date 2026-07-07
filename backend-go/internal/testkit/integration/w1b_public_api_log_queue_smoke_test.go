//go:build integration

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
	publicapilogjob "juhe-ai/backend-go/internal/jobs/publicapilog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/store/port"
)

func TestW1bPublicAPILogAsynqSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	postgresContainer, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer terminateContainer(t, ctx, postgresContainer)

	postgresURL, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	runGooseMigrations(t, db)

	redisContainer, err := tcredis.Run(ctx, redisImage)
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	defer terminateContainer(t, ctx, redisContainer)

	redisURL, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}
	redisOpts, err := queue.ParseRedisURL(redisURL)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}

	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	var workerErrMu sync.Mutex
	var workerRunErr error
	go func() {
		err := app.RunIngestWorker(workerCtx, config.Config{
			PostgresURL:     postgresURL,
			RedisQueueURL:   redisURL,
			RedisNamespace:  "juhe-ai",
			LogLevel:        "error",
			ShutdownTimeout: time.Second,
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		workerErrMu.Lock()
		workerRunErr = err
		workerErrMu.Unlock()
		close(workerDone)
	}()
	defer func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("ingest worker shutdown timed out")
		}
		workerErrMu.Lock()
		err := workerRunErr
		workerErrMu.Unlock()
		if err != nil {
			t.Fatalf("ingest worker run: %v", err)
		}
	}()

	client := queue.NewClient(redisOpts)
	defer closeClient(t, client)
	input := publicAPILogQueueSmokeFixture()
	if _, err := publicapilogjob.EnqueueWrite(ctx, client, input); err != nil {
		t.Fatalf("enqueue public api log write task: %v", err)
	}
	if _, err := publicapilogjob.EnqueueWrite(ctx, client, input); err != nil {
		t.Fatalf("enqueue duplicate public api log write task: %v", err)
	}

	inspector := queue.NewInspector(redisOpts)
	defer func() {
		if err := inspector.Close(); err != nil {
			t.Fatalf("close queue inspector: %v", err)
		}
	}()
	if err := waitForPublicAPILogQueueDrained(ctx, inspector, workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM juhe_dataset.public_api_logs WHERE id = $1", input.ID).Scan(&count); err != nil {
		t.Fatalf("count public api log: %v", err)
	}
	if count != 1 {
		t.Fatalf("public api log count = %d, want 1", count)
	}
}

func waitForPublicAPILogQueueDrained(ctx context.Context, inspector *queue.Inspector, workerDone <-chan struct{}, workerErr func() error) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		info, err := inspector.QueueInfo(publicapilogjob.QueueName)
		if err != nil {
			return err
		}
		if info.Archived > 0 {
			return fmt.Errorf("public api log queue archived %d task(s)", info.Archived)
		}
		if info.Pending == 0 && info.Active == 0 && info.Retry == 0 {
			return nil
		}

		select {
		case <-workerDone:
			if err := workerErr(); err != nil {
				return err
			}
			return fmt.Errorf("ingest worker stopped before public api log queue drained")
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func runGooseMigrations(t *testing.T, db *sql.DB) {
	t.Helper()

	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.Up(db, filepath.Join(repoRoot(t), "db", "migrations")); err != nil {
		t.Fatalf("goose up: %v", err)
	}
}

func publicAPILogQueueSmokeFixture() port.PublicAPILogInput {
	statusCode := 200
	durationMs := int64(12)
	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	return port.PublicAPILogInput{
		ID:                    "publog_w1b_asynq_smoke",
		TraceID:               "trace_w1b_asynq_smoke",
		SourceRefID:           "extsrc_w1b_smoke",
		SourceName:            "W1b Smoke Source",
		TokenID:               "exttok_w1b_smoke",
		TokenName:             "W1b Smoke Token",
		TokenPrefix:           "juis_w1b",
		Method:                "GET",
		Path:                  "/__aipublic__/group/list",
		StatusCode:            &statusCode,
		Success:               true,
		DurationMs:            &durationMs,
		RequestSizeBytes:      20,
		ResponseSizeBytes:     30,
		RequestCaptureStatus:  port.PublicAPILogCaptureComplete,
		ResponseCaptureStatus: port.PublicAPILogCaptureComplete,
		RequestData:           map[string]any{"query": map[string]any{"targetUsername": "admin"}},
		ResponseData:          map[string]any{"body": map[string]any{"items": []any{}}},
		StartedAt:             startedAt,
		EndedAt:               startedAt.Add(time.Duration(durationMs) * time.Millisecond),
		CreatedAt:             startedAt.Add(time.Duration(durationMs) * time.Millisecond),
	}
}
