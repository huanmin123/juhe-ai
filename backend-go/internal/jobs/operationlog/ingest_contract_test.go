package operationlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type nodeIngestContractGolden struct {
	Version int    `json:"version"`
	Owner   string `json:"owner"`
	Node    struct {
		IPCMessageType string `json:"ipcMessageType"`
		LocalQueue     struct {
			FlushIntervalMS         int    `json:"flushIntervalMs"`
			BatchSize               int    `json:"batchSize"`
			ShutdownMaxBatches      int    `json:"shutdownMaxBatches"`
			MaxItems                int    `json:"maxItems"`
			MaxBytes                int    `json:"maxBytes"`
			OverflowPolicy          string `json:"overflowPolicy"`
			ShutdownPolicy          string `json:"shutdownPolicy"`
			PostgresShutdownAwaited bool   `json:"postgresShutdownAwaited"`
			KnownRisk               string `json:"knownRisk"`
		} `json:"localQueue"`
		RedisStream struct {
			StreamKey            string   `json:"streamKey"`
			GroupName            string   `json:"groupName"`
			EnqueueRetryDelaysMS []int    `json:"enqueueRetryDelaysMs"`
			ConsumeOrder         []string `json:"consumeOrder"`
			WriteFailurePolicy   string   `json:"writeFailurePolicy"`
			ShutdownPolicy       string   `json:"shutdownPolicy"`
		} `json:"redisStream"`
		Postgres struct {
			WriteMode   string `json:"writeMode"`
			Idempotency string `json:"idempotency"`
			ChildRows   string `json:"childRows"`
		} `json:"postgres"`
	} `json:"node"`
	GoAsynq struct {
		TaskType                 string `json:"taskType"`
		Queue                    string `json:"queue"`
		TaskTimeoutSeconds       int    `json:"taskTimeoutSeconds"`
		MaxRetry                 int    `json:"maxRetry"`
		RetentionHours           int    `json:"retentionHours"`
		DefaultIngestConcurrency int    `json:"defaultIngestConcurrency"`
		ShutdownTimeoutSeconds   int    `json:"shutdownTimeoutSeconds"`
	} `json:"goAsynq"`
	NonEquivalence []string `json:"nonEquivalence"`
}

func TestNodeOperationLogIngestContractGolden(t *testing.T) {
	golden := loadNodeIngestContractGolden(t)
	if golden.Version != 1 || golden.Owner != "node" {
		t.Fatalf("golden ownership = version:%d owner:%q, want version 1 owned by node", golden.Version, golden.Owner)
	}
	if len(golden.NonEquivalence) < 5 {
		t.Fatalf("non-equivalence entries = %d, want explicit Go cutover gaps", len(golden.NonEquivalence))
	}

	queueSource := readRepositoryFile(t, "backend/src/modules/operation-logs/operation-log-queue.service.ts")
	for _, snippet := range []string{
		"const operationLogFlushIntervalMs = 100",
		"const operationLogBatchSize = 200",
		"const operationLogShutdownFlushMaxBatches = 100",
		"const operationLogQueueMaxItems = 5_000",
		"const operationLogQueueMaxBytes = 32 * 1024 * 1024",
		"type: 'background_worker_operation_logs'",
		"runRedisEnqueueWithBoundedRetry(() => operationLogRedisStreamQueue().enqueue(input))",
		"const claimed = await queue.claimPending()",
		"await queue.readNew()",
		"await createOperationLogsBatchAsync(inputs)",
		"await queue.ack(messages.map((message) => message.id))",
		"Redis Stream 操作日志落库失败，消息保持 pending 等待重投",
		"await queue.closeConsumer()",
		"await operationLogRedisConsumerPromise",
		"process.once('beforeExit', flushOperationLogQueueForShutdown)",
	} {
		if !strings.Contains(queueSource, snippet) {
			t.Fatalf("Node operation-log queue no longer contains golden anchor %q; update the contract intentionally", snippet)
		}
	}
	if !strings.Contains(queueSource, "recordOperationLogLocalDrop(queued, 'overflow')") || !strings.Contains(queueSource, "recordOperationLogLocalDrop(queued, 'oversize')") {
		t.Fatal("Node local queue no longer records both overflow and oversize drops")
	}
	drainSource := readRepositoryFile(t, "backend/src/shared/redis-stream-drain.ts")
	if !strings.Contains(drainSource, "operationLogs: { name: 'operation-logs', streamKey: 'juhe-ai:queue:operation-logs', groupName: 'juhe-ai:operation-log-writers' }") {
		t.Fatal("Node operation-log Redis Stream key or consumer group drifted")
	}
	retrySource := readRepositoryFile(t, "backend/src/shared/redis-enqueue-retry.ts")
	if !strings.Contains(retrySource, "const defaultRedisEnqueueRetryDelaysMs = [25, 100] as const") {
		t.Fatal("Node Redis enqueue retry schedule drifted")
	}
	dbServiceSource := readRepositoryFile(t, "backend/src/modules/db-service/db-service-ipc.ts")
	for _, snippet := range []string{
		"case 'background_worker_operation_logs':",
		"runtimeConfig.processRole === 'server'",
		"rejectRedisStreamLocalQueueForward('background_worker_operation_logs', items.length)",
		"items.filter(operationLogQueue.isOperationLogInput)",
		"backgroundIpc.sendOperationLogsToWorker(operationLogs)",
	} {
		if !strings.Contains(dbServiceSource, snippet) {
			t.Fatalf("Node DB-service operation-log forwarding no longer contains golden anchor %q", snippet)
		}
	}
	backgroundIPCSource := readRepositoryFile(t, "backend/src/modules/background/background-ipc.ts")
	if !strings.Contains(backgroundIPCSource, "export function sendOperationLogsToWorker(items: OperationLogInput[]): boolean") || !strings.Contains(backgroundIPCSource, "type: 'background_worker_operation_logs'") {
		t.Fatal("Node server-to-ingest-worker operation-log IPC contract drifted")
	}

	workerSource := readRepositoryFile(t, "backend/src/worker.ts")
	for _, snippet := range []string{
		"startOperationLogRedisStreamConsumer()",
		"await stopOperationLogRedisStreamConsumer()",
		"flushOperationLogQueueForShutdown()",
	} {
		if !strings.Contains(workerSource, snippet) {
			t.Fatalf("Node ingest worker no longer contains golden shutdown anchor %q", snippet)
		}
	}
	if strings.Contains(workerSource, "await flushOperationLogQueueForShutdown()") {
		t.Fatal("Node now awaits the PostgreSQL shutdown flush; update this golden and reassess the recorded risk")
	}

	if golden.Node.IPCMessageType != "background_worker_operation_logs" || golden.Node.LocalQueue.FlushIntervalMS != 100 || golden.Node.LocalQueue.BatchSize != 200 || golden.Node.LocalQueue.ShutdownMaxBatches != 100 || golden.Node.LocalQueue.MaxItems != 5000 || golden.Node.LocalQueue.MaxBytes != 32*1024*1024 {
		t.Fatalf("invalid Node local queue golden: %+v", golden.Node.LocalQueue)
	}
	if golden.Node.LocalQueue.OverflowPolicy != "drop_new_and_count" || golden.Node.LocalQueue.ShutdownPolicy != "bounded_drain_without_retry" {
		t.Fatalf("invalid Node local queue policies: %+v", golden.Node.LocalQueue)
	}
	if golden.Node.LocalQueue.PostgresShutdownAwaited || golden.Node.LocalQueue.KnownRisk == "" {
		t.Fatalf("Node PostgreSQL shutdown risk must remain explicit: %+v", golden.Node.LocalQueue)
	}
	if golden.Node.RedisStream.StreamKey != "juhe-ai:queue:operation-logs" || golden.Node.RedisStream.GroupName != "juhe-ai:operation-log-writers" || !sameInts(golden.Node.RedisStream.EnqueueRetryDelaysMS, []int{25, 100}) || !sameStrings(golden.Node.RedisStream.ConsumeOrder, []string{"claim_pending", "read_new", "postgres_batch_write", "ack_after_write"}) || golden.Node.RedisStream.WriteFailurePolicy != "keep_pending_for_retry" || golden.Node.RedisStream.ShutdownPolicy != "close_consumer_then_await_consumer_loop" {
		t.Fatalf("invalid Node Redis Stream golden: %+v", golden.Node.RedisStream)
	}
	if golden.Node.Postgres.WriteMode != "single_transaction_batched_insert" || golden.Node.Postgres.Idempotency != "operation_log_id_conflict_do_nothing_returning_id" || golden.Node.Postgres.ChildRows != "write_only_for_newly_inserted_log_ids" {
		t.Fatalf("invalid Node PostgreSQL write golden: %+v", golden.Node.Postgres)
	}
	writeSource := readRepositoryFile(t, "backend/src/storage/operation-log-write.repository.ts")
	for _, snippet := range []string{
		"await client.transaction(async (tx) => {",
		"const insertedLogIds = await insertPostgresOperationLogsBatch(tx, preparedLogs)",
		"const insertedLogs = preparedLogs.filter((prepared) => insertedLogIds.has(prepared.id))",
		"ON CONFLICT(id) DO NOTHING",
		"RETURNING id",
	} {
		if !strings.Contains(writeSource, snippet) {
			t.Fatalf("Node PostgreSQL operation-log writer no longer contains golden anchor %q", snippet)
		}
	}
}

func TestGoAsynqOperationLogIngestGapGolden(t *testing.T) {
	golden := loadNodeIngestContractGolden(t)
	if golden.GoAsynq.TaskType != TaskTypeWrite || golden.GoAsynq.Queue != QueueName || golden.GoAsynq.TaskTimeoutSeconds != int(defaultTaskTimeout/time.Second) || golden.GoAsynq.MaxRetry != defaultMaxRetry || golden.GoAsynq.RetentionHours != int(defaultTaskRetention/time.Hour) || golden.GoAsynq.DefaultIngestConcurrency != 1 || golden.GoAsynq.ShutdownTimeoutSeconds != 10 {
		t.Fatalf("Go Asynq golden drifted: %+v", golden.GoAsynq)
	}
	workerSource := readRepositoryFile(t, "backend-go/internal/jobs/worker/ingest.go")
	for _, snippet := range []string{
		"const DefaultIngestConcurrency = 1",
		"defaultDuration(opts.ShutdownTimeout, 10*time.Second)",
		"server.Shutdown()",
	} {
		if !strings.Contains(workerSource, snippet) {
			t.Fatalf("Go Asynq ingest no longer contains golden anchor %q; reassess the Node cutover gap", snippet)
		}
	}
}

func loadNodeIngestContractGolden(t *testing.T) nodeIngestContractGolden {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "node_ingest_contract.json"))
	if err != nil {
		t.Fatalf("read operation log ingest golden: %v", err)
	}
	var golden nodeIngestContractGolden
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("decode operation log ingest golden: %v", err)
	}
	return golden
}

func readRepositoryFile(t *testing.T, relativePath string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate current test file")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relativePath)))
	if err != nil {
		t.Fatalf("read %s: %v", relativePath, err)
	}
	return string(data)
}

func sameInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
