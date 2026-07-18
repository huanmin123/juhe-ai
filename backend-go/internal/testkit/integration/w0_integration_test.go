//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/jobs/queue"
	postgreshealth "juhe-ai/backend-go/internal/platform/postgres"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	postgresImage = "postgres:18-bookworm"
	redisImage    = "redis:8.2.7-bookworm"
)

func TestW0PostgresMigrationSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer terminateContainer(t, ctx, container)

	connString, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}

	if result := postgreshealth.Check(ctx, connString); result.Status != "ok" {
		t.Fatalf("postgres health = %+v, want ok", result)
	}

	db := openSQLDB(t, connString)
	defer closeSQLDB(t, db)

	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.Up(db, filepath.Join(repoRoot(t), "db", "migrations")); err != nil {
		t.Fatalf("goose up: %v", err)
	}

	store, err := postgresstore.Open(ctx, connString)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	schemas, err := store.ListBaselineSchemas(ctx, []string{
		"juhe_business",
		"juhe_dataset",
		"juhe_usage",
		"juhe_stats",
	})
	if err != nil {
		t.Fatalf("list baseline schemas via sqlc: %v", err)
	}
	if got, want := fmt.Sprint(schemas), "[juhe_business juhe_dataset juhe_stats juhe_usage]"; got != want {
		t.Fatalf("baseline schemas = %s, want %s", got, want)
	}

	publicSettings, err := store.PublicGlobalSettings(ctx)
	if err != nil {
		t.Fatalf("public global settings: %v", err)
	}
	if publicSettings.AppName != "聚合 AI" || publicSettings.AppIcon != "/__aisys__/brand-icon.svg" {
		t.Fatalf("public settings = %+v", publicSettings)
	}
	rateLimitSettings, err := store.SystemAPIRateLimitSettings(ctx)
	if err != nil {
		t.Fatalf("system api rate limit settings: %v", err)
	}
	if rateLimitSettings.IPReadPerMinute != 600 ||
		rateLimitSettings.IPReadBurstPer10Seconds != 120 ||
		rateLimitSettings.IPWritePerMinute != 180 ||
		rateLimitSettings.IPWriteBurstPer10Seconds != 40 ||
		rateLimitSettings.UserReadPerMinute != 300 ||
		rateLimitSettings.UserWritePerMinute != 120 {
		t.Fatalf("rate limit settings = %+v", rateLimitSettings)
	}

	if err := store.InTx(ctx, func(ctx context.Context, q postgresstore.Reader) error {
		_, err := q.ListBaselineSchemas(ctx, []string{"juhe_business"})
		return err
	}); err != nil {
		t.Fatalf("store transaction query: %v", err)
	}
}

func TestW0RedisAndAsynqSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcredis.Run(ctx, redisImage)
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	defer terminateContainer(t, ctx, container)

	redisURL, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}

	if result := redisplatform.Check(ctx, redisURL); result.Status != "ok" {
		t.Fatalf("redis health = %+v, want ok", result)
	}
	if result := queue.Check(ctx, redisURL); result.Status != "ok" {
		t.Fatalf("queue health = %+v, want ok", result)
	}

	cacheClient, err := redisplatform.NewClient(redisURL, "w0:cache")
	if err != nil {
		t.Fatalf("open redis cache client: %v", err)
	}
	defer closeRedisClient(t, cacheClient)
	stateClient, err := redisplatform.NewClient(redisURL, "w0:state")
	if err != nil {
		t.Fatalf("open redis state client: %v", err)
	}
	defer closeRedisClient(t, stateClient)
	if err := cacheClient.Ping(ctx); err != nil {
		t.Fatalf("redis cache ping: %v", err)
	}
	if err := stateClient.Ping(ctx); err != nil {
		t.Fatalf("redis state ping: %v", err)
	}
	if err := cacheClient.Set(ctx, "ttl-item", []byte("value"), 100*time.Millisecond); err != nil {
		t.Fatalf("redis set ttl item: %v", err)
	}
	if got, err := cacheClient.Get(ctx, "ttl-item"); err != nil || string(got) != "value" {
		t.Fatalf("redis get ttl item = %q, %v", got, err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := cacheClient.Get(ctx, "ttl-item"); !errors.Is(err, redisplatform.ErrNotFound) {
		t.Fatalf("redis expired item error = %v, want ErrNotFound", err)
	}
	if err := stateClient.SetMany(ctx, []redisplatform.SetItem{
		{Key: "pipeline:a", Value: []byte("a"), TTL: time.Minute},
		{Key: "pipeline:b", Value: []byte("b"), TTL: time.Minute},
	}); err != nil {
		t.Fatalf("redis pipeline set: %v", err)
	}
	if got, err := stateClient.Get(ctx, "pipeline:b"); err != nil || string(got) != "b" {
		t.Fatalf("redis pipeline get = %q, %v", got, err)
	}
	if got, err := stateClient.IncrWithTTL(ctx, "counter", time.Minute); err != nil || got != 1 {
		t.Fatalf("redis incr = %d, %v", got, err)
	}
	limits := []redisplatform.FixedWindowLimit{
		{Key: "system-api:ip-read:203-0-113-10:minute", Limit: 2, Window: time.Second},
		{Key: "system-api:ip-read:203-0-113-10:burst", Limit: 1, Window: 100 * time.Millisecond},
	}
	if decision, err := stateClient.AllowFixedWindow(ctx, limits); err != nil || !decision.Allowed {
		t.Fatalf("redis fixed-window first decision = %+v, %v; want allowed", decision, err)
	}
	if decision, err := stateClient.AllowFixedWindow(ctx, limits); err != nil || decision.Allowed || decision.RetryAfterSeconds <= 0 {
		t.Fatalf("redis fixed-window burst decision = %+v, %v; want denied with retry-after", decision, err)
	}
	time.Sleep(150 * time.Millisecond)
	if decision, err := stateClient.AllowFixedWindow(ctx, limits); err != nil || !decision.Allowed {
		t.Fatalf("redis fixed-window third decision = %+v, %v; want allowed after burst reset", decision, err)
	}
	stateClientPeer, err := redisplatform.NewClient(redisURL, "w0:state")
	if err != nil {
		t.Fatalf("open redis peer state client: %v", err)
	}
	defer closeRedisClient(t, stateClientPeer)
	sharedLimits := []redisplatform.FixedWindowLimit{
		{Key: "system-api:ip-read:198-51-100-7:minute", Limit: 2, Window: time.Second},
		{Key: "system-api:ip-read:198-51-100-7:burst", Limit: 2, Window: time.Second},
	}
	if decision, err := stateClient.AllowFixedWindow(ctx, sharedLimits); err != nil || !decision.Allowed {
		t.Fatalf("redis fixed-window shared first decision = %+v, %v; want allowed", decision, err)
	}
	if decision, err := stateClientPeer.AllowFixedWindow(ctx, sharedLimits); err != nil || !decision.Allowed {
		t.Fatalf("redis fixed-window shared peer decision = %+v, %v; want allowed", decision, err)
	}
	if decision, err := stateClient.AllowFixedWindow(ctx, sharedLimits); err != nil || decision.Allowed {
		t.Fatalf("redis fixed-window shared third decision = %+v, %v; want denied", decision, err)
	}
	concurrentLimits := []redisplatform.FixedWindowLimit{
		{Key: "system-api:ip-read:198-51-100-8:minute", Limit: 5, Window: time.Second},
	}
	var allowedCount atomic.Int32
	errCh := make(chan error, 20)
	var concurrentWG sync.WaitGroup
	for i := 0; i < 20; i++ {
		concurrentWG.Add(1)
		go func() {
			defer concurrentWG.Done()
			decision, err := stateClientPeer.AllowFixedWindow(ctx, concurrentLimits)
			if err != nil {
				errCh <- err
				return
			}
			if decision.Allowed {
				allowedCount.Add(1)
			}
		}()
	}
	concurrentWG.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("redis fixed-window concurrent decision: %v", err)
	}
	if got := allowedCount.Load(); got != 5 {
		t.Fatalf("redis fixed-window concurrent allowed = %d, want 5", got)
	}
	penaltyLimits := []redisplatform.PenaltyWindowLimit{
		{
			StoreName:  "external_source_public_api",
			ScopeKey:   "extsrc_w1b:exttok_w1b:juis_w1b",
			Window:     time.Second,
			Limit:      1,
			MaxPenalty: 2 * time.Second,
			MaxIdle:    time.Minute,
		},
	}
	if decision, err := stateClient.AllowPenaltyWindow(ctx, penaltyLimits); err != nil || !decision.Allowed {
		t.Fatalf("redis penalty-window first decision = %+v, %v; want allowed", decision, err)
	}
	if decision, err := stateClientPeer.AllowPenaltyWindow(ctx, penaltyLimits); err != nil || decision.Allowed || decision.RetryAfterSeconds <= 0 || decision.BlockedWindowIndex != 1 {
		t.Fatalf("redis penalty-window second decision = %+v, %v; want denied with retry-after", decision, err)
	}

	redisOpts, err := queue.ParseRedisURL(redisURL)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}

	client := queue.NewClient(redisOpts)
	defer closeClient(t, client)
	if err := client.Ping(); err != nil {
		t.Fatalf("asynq client ping: %v", err)
	}

	asynqRedis := asynq.RedisClientOpt{
		Addr:         redisOpts.Addr,
		Username:     redisOpts.Username,
		Password:     redisOpts.Password,
		DB:           redisOpts.DB,
		TLSConfig:    redisOpts.TLS,
		DialTimeout:  redisOpts.DialTimeout,
		ReadTimeout:  redisOpts.ReadTimeout,
		WriteTimeout: redisOpts.WriteTimeout,
	}
	server := asynq.NewServer(asynqRedis, asynq.Config{
		Concurrency:              1,
		Queues:                   map[string]int{"w0": 1},
		RetryDelayFunc:           func(int, error, *asynq.Task) time.Duration { return 20 * time.Millisecond },
		TaskCheckInterval:        20 * time.Millisecond,
		DelayedTaskCheckInterval: 20 * time.Millisecond,
		ShutdownTimeout:          time.Second,
		LogLevel:                 asynq.ErrorLevel,
	})

	mux := asynq.NewServeMux()
	retryDone := make(chan struct{})
	archiveHandled := make(chan struct{})
	var retryAttempts atomic.Int32
	var exhaustAttempts atomic.Int32
	var retryOnce sync.Once
	var archiveOnce sync.Once

	mux.HandleFunc("w0:retry-once", func(context.Context, *asynq.Task) error {
		if retryAttempts.Add(1) == 1 {
			return errors.New("force one retry")
		}
		retryOnce.Do(func() { close(retryDone) })
		return nil
	})
	mux.HandleFunc("w0:archive", func(context.Context, *asynq.Task) error {
		archiveOnce.Do(func() { close(archiveHandled) })
		return asynq.SkipRetry
	})
	mux.HandleFunc("w0:exhaust", func(context.Context, *asynq.Task) error {
		exhaustAttempts.Add(1)
		return errors.New("force retry exhaustion")
	})

	if err := server.Start(mux); err != nil {
		t.Fatalf("start asynq server: %v", err)
	}
	defer server.Shutdown()

	maxRetry := 2
	retryTask, err := client.Enqueue(ctx, "w0:retry-once", []byte(`{"version":1}`), queue.EnqueueOptions{
		Queue:     "w0",
		MaxRetry:  &maxRetry,
		Timeout:   time.Second,
		Retention: time.Minute,
	})
	if err != nil {
		t.Fatalf("enqueue retry task: %v", err)
	}
	if retryTask.Queue != "w0" || retryTask.Type != "w0:retry-once" || retryTask.MaxRetry != maxRetry {
		t.Fatalf("unexpected retry task info: %+v", retryTask)
	}

	select {
	case <-retryDone:
	case <-ctx.Done():
		t.Fatalf("retry task was not processed twice: %v", ctx.Err())
	}
	if got := retryAttempts.Load(); got != 2 {
		t.Fatalf("retry attempts = %d, want 2", got)
	}

	noRetry := 0
	archiveTask, err := client.Enqueue(ctx, "w0:archive", []byte(`{"version":1}`), queue.EnqueueOptions{
		Queue:     "w0",
		MaxRetry:  &noRetry,
		Timeout:   time.Second,
		Retention: time.Minute,
	})
	if err != nil {
		t.Fatalf("enqueue archive task: %v", err)
	}

	select {
	case <-archiveHandled:
	case <-ctx.Done():
		t.Fatalf("archive task was not handled: %v", ctx.Err())
	}
	inspector := queue.NewInspector(redisOpts)
	defer closeInspector(t, inspector)

	waitForArchivedTask(t, ctx, inspector, archiveTask.ID)

	exhaustRetry := 1
	exhaustTask, err := client.Enqueue(ctx, "w0:exhaust", []byte(`{"version":1}`), queue.EnqueueOptions{
		Queue:    "w0",
		MaxRetry: &exhaustRetry,
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("enqueue exhaust task: %v", err)
	}
	waitForArchivedTask(t, ctx, inspector, exhaustTask.ID)
	if got := exhaustAttempts.Load(); got < 2 {
		t.Fatalf("exhaust attempts = %d, want at least 2", got)
	}
	if info, err := inspector.QueueInfo("w0"); err != nil || info.Archived < 2 {
		t.Fatalf("queue info = %+v, %v; want at least 2 archived", info, err)
	}
	if err := queue.Smoke(ctx, redisURL); err != nil {
		t.Fatalf("queue smoke: %v", err)
	}
}

func openSQLDB(t *testing.T, connString string) *sql.DB {
	t.Helper()

	cfg, err := pgx.ParseConfig(connString)
	if err != nil {
		t.Fatalf("parse pgx config: %v", err)
	}
	return stdlib.OpenDB(*cfg)
}

func closeSQLDB(t *testing.T, db *sql.DB) {
	t.Helper()

	if err := db.Close(); err != nil {
		t.Fatalf("close sql db: %v", err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func waitForArchivedTask(t *testing.T, ctx context.Context, inspector *queue.Inspector, taskID string) {
	t.Helper()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		ids, err := inspector.ArchivedTaskIDs("w0")
		if err != nil {
			t.Fatalf("list archived tasks: %v", err)
		}
		for _, id := range ids {
			if id == taskID {
				return
			}
		}

		select {
		case <-ctx.Done():
			t.Fatalf("archived task %s not found: %v", taskID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func closeClient(t *testing.T, client *queue.Client) {
	t.Helper()

	if err := client.Close(); err != nil {
		t.Fatalf("close queue client: %v", err)
	}
}

func closeInspector(t *testing.T, inspector *queue.Inspector) {
	t.Helper()

	if err := inspector.Close(); err != nil {
		t.Fatalf("close queue inspector: %v", err)
	}
}

func closeRedisClient(t *testing.T, client *redisplatform.Client) {
	t.Helper()

	if err := client.Close(); err != nil {
		t.Fatalf("close redis client: %v", err)
	}
}

func terminateContainer(t *testing.T, ctx context.Context, container testcontainers.Container) {
	t.Helper()

	if err := container.Terminate(ctx); err != nil {
		t.Fatalf("terminate container: %v", err)
	}
}

func TestImagesArePinnedForW0(t *testing.T) {
	if postgresImage == "" || redisImage == "" {
		t.Fatal("integration images must be explicit")
	}
	if postgresImage == "latest" || redisImage == "latest" {
		t.Fatal("integration images must not use latest")
	}
	if got := fmt.Sprintf("%s %s", postgresImage, redisImage); got == "" {
		t.Fatal("integration images are empty")
	}
}
