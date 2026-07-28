package w7real_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"juhe-ai/backend-go/internal/accounthealth"
	job "juhe-ai/backend-go/internal/jobs/cooldownaccountretest"
	"juhe-ai/backend-go/internal/jobs/queue"
	worker "juhe-ai/backend-go/internal/jobs/worker"
	module "juhe-ai/backend-go/internal/modules/cooldownaccountretest"
	"juhe-ai/backend-go/internal/platform/proberevocationgate"
	platformredis "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	realGateEnv       = "JUHE_AI_W7_REAL"
	realPostgresEnv   = "JUHE_AI_W7_REAL_POSTGRES_URL"
	realRedisStateEnv = "JUHE_AI_W7_REAL_REDIS_STATE_URL"
	realRedisQueueEnv = "JUHE_AI_W7_REAL_REDIS_QUEUE_URL"
	realNamespaceEnv  = "JUHE_AI_W7_REAL_NAMESPACE"
)

func TestRealPostgresRevocationGateAndHTTPWriteFence(t *testing.T) {
	postgresURL := requiredRealEnv(t, realPostgresEnv)
	pool, err := pgxpool.New(t.Context(), postgresURL)
	if err != nil {
		t.Fatalf("open real PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(t.Context()); err != nil {
		t.Fatalf("ping real PostgreSQL: %v", err)
	}
	for _, table := range proberevocationgate.ProtectedTables() {
		var relation *string
		if err := pool.QueryRow(t.Context(), "SELECT to_regclass($1)::text", table).Scan(&relation); err != nil || relation == nil || *relation != table {
			t.Fatalf("protected table %q is unavailable: relation=%v error=%v", table, relation, err)
		}
	}
	guard, err := proberevocationgate.New(pool, proberevocationgate.Options{
		HoldTimeout: 3 * time.Second, ReleaseTimeout: time.Second,
		RetryMinDelay: 10 * time.Millisecond, RetryMaxDelay: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("create real PostgreSQL revocation guard: %v", err)
	}

	t.Run("waits for earlier writer before final reload", func(t *testing.T) {
		writer, err := pool.BeginTx(t.Context(), pgx.TxOptions{})
		if err != nil {
			t.Fatalf("begin writer transaction: %v", err)
		}
		defer func() { _ = writer.Rollback(context.Background()) }()
		if _, err := writer.Exec(t.Context(), "LOCK TABLE juhe_business.accounts IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatalf("lock accounts as writer: %v", err)
		}

		reloadEntered := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- guard.Protect(t.Context(), func(ctx context.Context, query proberevocationgate.Queryer) error {
				close(reloadEntered)
				return verifyProtectedTables(ctx, query)
			}, wroteRequestOnly)
		}()
		select {
		case <-reloadEntered:
			t.Fatal("final reload entered while an earlier writer held ACCESS EXCLUSIVE")
		case <-time.After(150 * time.Millisecond):
		}
		if err := writer.Rollback(t.Context()); err != nil {
			t.Fatalf("release earlier writer: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("guard after writer release: %v", err)
			}
		case <-time.After(4 * time.Second):
			t.Fatal("guard did not finish after earlier writer released")
		}
	})

	t.Run("releases table locks at real HTTP request write", func(t *testing.T) {
		requestSeen := make(chan struct{})
		releaseResponse := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			close(requestSeen)
			<-releaseResponse
			response.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		done := make(chan error, 1)
		go func() {
			done <- guard.Protect(t.Context(), verifyProtectedTables, func(ctx context.Context) error {
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(`{"probe":true}`))
				if err != nil {
					return err
				}
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					return err
				}
				_, readErr := io.Copy(io.Discard, response.Body)
				return errors.Join(readErr, response.Body.Close())
			})
		}()
		select {
		case <-requestSeen:
		case <-time.After(3 * time.Second):
			t.Fatal("mock upstream did not receive the real probe request")
		}

		writerCtx, cancelWriter := context.WithTimeout(t.Context(), time.Second)
		defer cancelWriter()
		writer, err := pool.BeginTx(writerCtx, pgx.TxOptions{})
		if err != nil {
			t.Fatalf("begin post-write writer: %v", err)
		}
		if _, err := writer.Exec(writerCtx, "LOCK TABLE juhe_business.accounts IN ACCESS EXCLUSIVE MODE NOWAIT"); err != nil {
			_ = writer.Rollback(context.Background())
			t.Fatalf("request bytes were written but revocation gate still held table locks: %v", err)
		}
		if err := writer.Rollback(writerCtx); err != nil {
			t.Fatalf("rollback post-write writer: %v", err)
		}
		close(releaseResponse)
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("real HTTP guarded request: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("guard did not finish after mock upstream response")
		}
	})
}

func TestRealRedisOAuthRefreshLock(t *testing.T) {
	rawURL := requiredRealEnv(t, realRedisStateEnv)
	namespace := requiredRealEnv(t, realNamespaceEnv) + ":state"
	client, err := platformredis.NewClient(rawURL, namespace)
	if err != nil {
		t.Fatalf("create real Redis state client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(t.Context()); err != nil {
		t.Fatalf("ping real Redis state: %v", err)
	}

	firstLock, err := platformredis.NewOAuthRefreshLock(client, platformredis.OAuthRefreshLockOptions{
		TTL: 300 * time.Millisecond, Wait: 100 * time.Millisecond, Retry: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("create first OAuth refresh lock: %v", err)
	}
	secondLock, err := platformredis.NewOAuthRefreshLock(client, platformredis.OAuthRefreshLockOptions{
		TTL: 300 * time.Millisecond, Wait: 100 * time.Millisecond, Retry: 10 * time.Millisecond, FailIfLocked: true,
	})
	if err != nil {
		t.Fatalf("create second OAuth refresh lock: %v", err)
	}
	first, err := firstLock.Acquire(t.Context(), "openai", "w7-real-source")
	if err != nil {
		t.Fatalf("acquire first real OAuth refresh lock: %v", err)
	}
	if _, err := secondLock.Acquire(t.Context(), "openai", "w7-real-source"); !errors.Is(err, platformredis.ErrOAuthRefreshLockBusy) {
		t.Fatalf("second real OAuth refresh lock error = %v, want busy", err)
	}
	time.Sleep(450 * time.Millisecond)
	if err := first.AssertOwned(t.Context()); err != nil {
		t.Fatalf("background renewal did not preserve real OAuth refresh lock: %v", err)
	}
	released, err := first.Release(t.Context())
	if err != nil || !released {
		t.Fatalf("release first real OAuth refresh lock = %t, %v", released, err)
	}
	second, err := secondLock.Acquire(t.Context(), "openai", "w7-real-source")
	if err != nil {
		t.Fatalf("acquire second real OAuth refresh lock after release: %v", err)
	}
	if released, err := second.Release(t.Context()); err != nil || !released {
		t.Fatalf("release second real OAuth refresh lock = %t, %v", released, err)
	}
}

func TestRealAsynqCooldownConsumerAndUniqueLease(t *testing.T) {
	rawURL := requiredRealEnv(t, realRedisQueueEnv)
	redisOptions, err := queue.ParseRedisURL(rawURL)
	if err != nil {
		t.Fatalf("parse real Redis queue URL: %v", err)
	}
	queueClient := queue.NewClient(redisOptions)
	t.Cleanup(func() { _ = queueClient.Close() })
	if err := queueClient.Ping(); err != nil {
		t.Fatalf("ping real Redis queue: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	observation := now.Add(-time.Minute)
	candidate := port.CooldownAccountRetestCandidate{
		ID: "w7-real-account", ConfigRevision: 3, DispatchRevision: 4,
		CooldownUntil: now.Add(-time.Second), CreatedAt: now.Add(-time.Hour),
		ObservationStartedAt: &observation, Generation: "w7-real-generation",
		SystemAccountID: "w7-real-system", GroupID: "w7-real-group",
		HealthCheckModel: "gpt-5", HealthCheckEndpointMode: "chat_json",
		MaxPauseMinutes: 60, MaxRecoveryHours: 24,
	}
	task := port.CooldownAccountRetestTask{
		AccountID: candidate.ID, ConfigRevision: candidate.ConfigRevision, DispatchRevision: candidate.DispatchRevision,
		ObservationStartedAt: candidate.ObservationStartedAt, Generation: candidate.Generation,
		MaxPauseMinutes: candidate.MaxPauseMinutes, MaxRecoveryHours: candidate.MaxRecoveryHours,
	}
	outcomes := &realOutcomeStore{deferred: make(chan time.Duration, 1)}
	processor := module.Processor{
		Store: realCandidateStore{candidate: candidate}, Outcomes: outcomes,
		Probe: realNeutralProbe{}, Quota: realQuotaChecker{},
		TaskTimeout: 5 * time.Second, OutcomeTimeout: 3 * time.Second,
	}
	enqueuer := job.Enqueuer{Client: queueClient, UniqueTTL: 2 * time.Minute, TaskTimeout: 5 * time.Second}
	enqueued, err := enqueuer.EnqueueCooldownAccountRetest(t.Context(), task)
	if err != nil || !enqueued {
		t.Fatalf("enqueue real cooldown task = %t, %v", enqueued, err)
	}
	duplicate, err := enqueuer.EnqueueCooldownAccountRetest(t.Context(), task)
	if err != nil || duplicate {
		t.Fatalf("duplicate real cooldown task = %t, %v, want unique conflict", duplicate, err)
	}

	consumerCtx, cancelConsumer := context.WithCancel(t.Context())
	consumerDone := make(chan error, 1)
	go func() {
		consumerDone <- worker.RunCooldownAccountRetestConsumer(consumerCtx, worker.CooldownAccountRetestConsumerOptions{
			Redis: redisOptions, Processor: processor, ShutdownTimeout: 3 * time.Second, LogLevel: "error", Concurrency: 1,
		})
	}()
	select {
	case delay := <-outcomes.deferred:
		if delay < 3*time.Second || delay > 15*time.Minute {
			t.Fatalf("real cooldown neutral defer delay = %v", delay)
		}
	case <-time.After(10 * time.Second):
		cancelConsumer()
		t.Fatal("real Asynq consumer did not persist neutral cooldown outcome")
	}
	cancelConsumer()
	select {
	case err := <-consumerDone:
		if err != nil {
			t.Fatalf("stop real cooldown consumer: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("real cooldown consumer did not stop within shutdown budget")
	}
}

func requiredRealEnv(t *testing.T, name string) string {
	t.Helper()
	if strings.TrimSpace(os.Getenv(realGateEnv)) != "1" {
		t.Skip(realGateEnv + "=1 is required")
	}
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required when %s=1", name, realGateEnv)
	}
	return value
}

func verifyProtectedTables(ctx context.Context, query proberevocationgate.Queryer) error {
	for _, table := range proberevocationgate.ProtectedTables() {
		if _, err := query.Exec(ctx, "SELECT 1 FROM "+table+" LIMIT 0"); err != nil {
			return fmt.Errorf("read protected table %s: %w", table, err)
		}
	}
	return nil
}

func wroteRequestOnly(ctx context.Context) error {
	trace := httptrace.ContextClientTrace(ctx)
	if trace == nil || trace.WroteRequest == nil {
		return errors.New("WroteRequest trace is missing")
	}
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	return nil
}

type realCandidateStore struct {
	candidate port.CooldownAccountRetestCandidate
}

func (s realCandidateStore) ListDueCooldownAccountRetests(context.Context, port.CooldownAccountRetestListInput) (port.CooldownAccountRetestPage, error) {
	return port.CooldownAccountRetestPage{Candidates: []port.CooldownAccountRetestCandidate{s.candidate}}, nil
}

func (s realCandidateStore) FindDueCooldownAccountRetest(_ context.Context, id string, _ time.Time) (port.CooldownAccountRetestCandidate, bool, error) {
	return s.candidate, id == s.candidate.ID, nil
}

type realQuotaChecker struct{}

func (realQuotaChecker) EligibleByAccountID(_ context.Context, candidates []port.CooldownAccountRetestCandidate, _ time.Time) (map[string]bool, error) {
	result := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		result[candidate.ID] = true
	}
	return result, nil
}

type realNeutralProbe struct{}

func (realNeutralProbe) Probe(context.Context, port.CooldownAccountRetestCandidate) (port.CooldownAccountRetestProbeResult, error) {
	return port.CooldownAccountRetestProbeResult{Outcome: string(accounthealth.ProbeOutcomeFramingCompleteNeutral), StatusCode: http.StatusUnauthorized}, nil
}

type realOutcomeStore struct {
	deferred chan time.Duration
}

func (*realOutcomeStore) RecordCooldownAccountRetestSuccess(context.Context, port.CooldownAccountRetestTask) error {
	return errors.New("unexpected success outcome")
}

func (s *realOutcomeStore) DeferCooldownAccountRetest(_ context.Context, _ port.CooldownAccountRetestTask, delay time.Duration) error {
	s.deferred <- delay
	return nil
}

func (*realOutcomeStore) RecordCooldownAccountRetestFailure(context.Context, port.CooldownAccountRetestTask, port.CooldownAccountRetestProbeResult) error {
	return errors.New("unexpected failure outcome")
}
