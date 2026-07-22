package publicapilog_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	publicapilogjob "juhe-ai/backend-go/internal/jobs/publicapilog"
	"juhe-ai/backend-go/internal/modules/publicapilog"
)

type writerContract struct {
	Version       int    `json:"version"`
	RuntimeOwner  string `json:"runtimeOwner"`
	ProductionCut bool   `json:"goProductionTakeover"`
	Queue         struct {
		NodeLocalMaxItems         int    `json:"nodeLocalMaxItems"`
		NodeLocalMaxBytes         int    `json:"nodeLocalMaxBytes"`
		NodeFlushBatchSize        int    `json:"nodeFlushBatchSize"`
		NodeShutdownMaxBatches    int    `json:"nodeShutdownMaxBatches"`
		GoTaskType                string `json:"goTaskType"`
		GoQueueName               string `json:"goQueueName"`
		GoPayloadVersion          int    `json:"goPayloadVersion"`
		GoTaskTimeoutSeconds      int    `json:"goTaskTimeoutSeconds"`
		GoTaskRetentionHours      int    `json:"goTaskRetentionHours"`
		GoMaxRetry                int    `json:"goMaxRetry"`
		NodePostgresRowsPerInsert int    `json:"nodePostgresRowsPerInsert"`
		StableIDBeforeDispatch    bool   `json:"stableIdBeforeDispatch"`
		IdempotentConflict        string `json:"idempotentConflict"`
		RedisFailureFallback      string `json:"redisFailureFallback"`
	} `json:"queue"`
	Payload struct {
		MaxBytesPerSide    int      `json:"maxBytesPerSide"`
		MaxDepth           int      `json:"maxDepth"`
		MaxEntries         int      `json:"maxEntries"`
		StringPreviewBytes int      `json:"stringPreviewBytes"`
		CaptureStatuses    []string `json:"captureStatuses"`
		CapturedHeaders    []string `json:"capturedHeaders"`
		PreserveRawValues  bool     `json:"preserveRawValues"`
	} `json:"payload"`
	Retention struct {
		Setting     string `json:"setting"`
		DefaultDays int    `json:"defaultDays"`
		MinDays     int    `json:"minDays"`
		MaxDays     int    `json:"maxDays"`
		BatchRows   int    `json:"batchRows"`
		MaxBatches  int    `json:"maxBatchesPerRun"`
	} `json:"retention"`
	Reader struct {
		MaxVisibleRows  int      `json:"maxVisibleRows"`
		MaxPageSize     int      `json:"maxPageSize"`
		StableOrder     []string `json:"stableOrder"`
		SummaryExcludes []string `json:"summaryExcludes"`
		DetailIncludes  []string `json:"detailIncludes"`
	} `json:"reader"`
}

func TestNodeWriterGoReaderContract(t *testing.T) {
	root := repositoryRoot(t)
	contract := readWriterContract(t, filepath.Join(root, "backend-go", "internal", "modules", "publicapilog", "testdata", "node_writer_contract.json"))

	if contract.Version != 1 || contract.RuntimeOwner != "node" || contract.ProductionCut {
		t.Fatalf("owner contract = version %d owner %q takeover %v", contract.Version, contract.RuntimeOwner, contract.ProductionCut)
	}
	if contract.Queue.GoTaskType != publicapilogjob.TaskTypeWrite || contract.Queue.GoQueueName != publicapilogjob.QueueName || contract.Queue.GoPayloadVersion != publicapilogjob.PayloadVersion {
		t.Fatalf("Go queue contract drifted: %+v", contract.Queue)
	}
	if contract.Payload.MaxBytesPerSide != publicapilog.SnapshotMaxBytes ||
		contract.Payload.MaxDepth != publicapilog.SnapshotMaxDepth ||
		contract.Payload.MaxEntries != publicapilog.SnapshotMaxEntries ||
		contract.Payload.StringPreviewBytes != publicapilog.SnapshotStringPreviewBytes {
		t.Fatalf("Go snapshot contract drifted: %+v", contract.Payload)
	}
	if !contract.Queue.StableIDBeforeDispatch || contract.Queue.IdempotentConflict != "do_nothing" || contract.Queue.RedisFailureFallback != "none" {
		t.Fatalf("delivery contract is unsafe: %+v", contract.Queue)
	}
	if !contract.Payload.PreserveRawValues || !slices.Equal(contract.Payload.CaptureStatuses, []string{"complete", "truncated", "empty", "dropped"}) || !slices.Equal(contract.Payload.CapturedHeaders, []string{"contentType", "contentLength"}) {
		t.Fatalf("capture contract drifted: %+v", contract.Payload)
	}

	queueSource := readSource(t, filepath.Join(root, "backend", "src", "modules", "public-api-logs", "public-api-log-queue.service.ts"))
	mustContainAll(t, queueSource,
		"const publicApiLogQueueMaxSize = "+strconv.Itoa(contract.Queue.NodeLocalMaxItems),
		"const publicApiLogQueueMaxBytes = 32 * 1024 * 1024",
		"const publicApiLogFlushBatchSize = "+strconv.Itoa(contract.Queue.NodeFlushBatchSize),
		"const publicApiLogShutdownFlushMaxBatches = "+strconv.Itoa(contract.Queue.NodeShutdownMaxBatches),
		"const stableInput = ensurePublicApiLogQueueId(input)",
		"高性能模式禁止回退 IPC 或本地队列",
	)
	if contract.Queue.NodeLocalMaxBytes != 32*1024*1024 {
		t.Fatalf("node queue byte budget = %d", contract.Queue.NodeLocalMaxBytes)
	}

	captureSource := readSource(t, filepath.Join(root, "backend", "src", "modules", "public-api-logs", "public-api-log-capture.middleware.ts"))
	mustContainAll(t, captureSource,
		"const publicApiSnapshotMaxBytes = 32 * 1024",
		"const publicApiSnapshotMaxDepth = "+strconv.Itoa(contract.Payload.MaxDepth),
		"const publicApiSnapshotMaxEntries = "+strconv.Itoa(contract.Payload.MaxEntries),
		"const publicApiSnapshotStringPreviewBytes = "+strconv.Itoa(contract.Payload.StringPreviewBytes),
		"contentType",
		"contentLength",
	)

	settingsSource := readSource(t, filepath.Join(root, "backend", "src", "storage", "settings.repository.ts"))
	defaultsSource := readSource(t, filepath.Join(root, "backend", "src", "storage", "schema-defaults.ts"))
	cleanupConstants := readSource(t, filepath.Join(root, "backend", "src", "modules", "background", "data-retention-cleanup.constants.ts"))
	logRepository := readSource(t, filepath.Join(root, "backend", "src", "storage", "public-api-logs.repository.ts"))
	goEnqueueSource := readSource(t, filepath.Join(root, "backend-go", "internal", "jobs", "publicapilog", "enqueue.go"))
	goInsertSQL := readSource(t, filepath.Join(root, "backend-go", "internal", "store", "postgres", "queries", "w1b_public_api.sql"))
	mustContainAll(t, settingsSource, contract.Retention.Setting+": integerSetting("+strconv.Itoa(contract.Retention.MinDays)+", "+strconv.Itoa(contract.Retention.MaxDays)+")")
	mustContainAll(t, defaultsSource, "['"+contract.Retention.Setting+"', "+strconv.Itoa(contract.Retention.DefaultDays)+"]")
	mustContainAll(t, cleanupConstants,
		"DATA_RETENTION_CLEANUP_BATCH_SIZE = "+strconv.Itoa(contract.Retention.BatchRows),
		"DATA_RETENTION_CLEANUP_MAX_BATCHES_PER_RUN = "+strconv.Itoa(contract.Retention.MaxBatches),
	)
	mustContainAll(t, logRepository,
		"const publicApiLogPostgresRowsPerInsert = "+strconv.Itoa(contract.Queue.NodePostgresRowsPerInsert),
		"WHERE created_at < ? ORDER BY created_at ASC, id ASC LIMIT ?",
		"ON CONFLICT(id) DO NOTHING",
	)
	mustContainAll(t, goEnqueueSource,
		"defaultTaskTimeout   = "+strconv.Itoa(contract.Queue.GoTaskTimeoutSeconds)+" * time.Second",
		"defaultTaskRetention = "+strconv.Itoa(contract.Queue.GoTaskRetentionHours)+" * time.Hour",
		"defaultMaxRetry      = "+strconv.Itoa(contract.Queue.GoMaxRetry),
	)
	mustContainAll(t, goInsertSQL, "ON CONFLICT(id) DO NOTHING")

	readerSource := readSource(t, filepath.Join(root, "backend-go", "internal", "store", "postgres", "managementpublicapilogs.go"))
	if contract.Reader.MaxVisibleRows != 1000 || contract.Reader.MaxPageSize != 100 || !slices.Equal(contract.Reader.StableOrder, []string{"created_at DESC", "id DESC"}) {
		t.Fatalf("reader window contract drifted: %+v", contract.Reader)
	}
	mustContainAll(t, readerSource,
		"maxManagementPublicAPILogListLimit     = "+strconv.Itoa(contract.Reader.MaxPageSize),
		"maxManagementPublicAPILogListRows      = "+strconv.Itoa(contract.Reader.MaxVisibleRows),
		"ORDER BY pal.created_at DESC, pal.id DESC",
		"WHERE pal.id = $1::text",
		"LIMIT 1",
	)
	for _, column := range contract.Reader.SummaryExcludes {
		if strings.Contains(summaryProjection(readerSource), column) {
			t.Fatalf("summary projection must exclude %q", column)
		}
	}
	for _, column := range contract.Reader.DetailIncludes {
		if !strings.Contains(readerSource, column) {
			t.Fatalf("detail projection must include %q", column)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}

func readWriterContract(t *testing.T, path string) writerContract {
	t.Helper()
	data := []byte(readSource(t, path))
	var contract writerContract
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatalf("decode writer contract: %v", err)
	}
	return contract
}

func readSource(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func mustContainAll(t *testing.T, source string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(source, fragment) {
			t.Errorf("source is missing contract fragment %q", fragment)
		}
	}
}

func summaryProjection(source string) string {
	const start = "const managementPublicAPILogSummarySelectColumns = `"
	const end = "`\n\nconst managementPublicAPILogDetailQuery"
	startIndex := strings.Index(source, start)
	if startIndex < 0 {
		return source
	}
	projection := source[startIndex+len(start):]
	endIndex := strings.Index(projection, end)
	if endIndex < 0 {
		return projection
	}
	return projection[:endIndex]
}
