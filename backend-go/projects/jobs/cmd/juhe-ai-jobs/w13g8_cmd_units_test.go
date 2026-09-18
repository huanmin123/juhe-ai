// 波次 w13g8：cmd/juhe-ai-jobs 组合根单元批测（选取与并行波次 w14j/w14q
// 不重叠的主题）：
//   - wireListProjectionFamily 装配门禁 disabled 臂 + projectionRuntimeProbe /
//     projectionCredentials 适配（worker_projection_jobs.go）；
//   - wireProbeFamily SQLite 缺核心表 disabled 臂 + speedFirstCandidateSource
//     错误/nil 分支（worker_probe_jobs.go）；
//   - buildWorkerAssembly 各家族 SQLite 存储打开失败传播臂（worker_assembly.go）；
//   - jobsHTTPHandler /account-balance/manual 鉴权与解析分支、healthHandler
//     默认槽位（main.go）。
//
// 全部进程内、不依赖 PG/Redis；PG 装配成功链与探针闭包成功路径归 w14q。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/circuitstore"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

// w13g8StubRuntime / w13g8StubConcurrency 是 projectionRuntimeProbe 的读源
// stub（circuitstore.ConcurrencySource / RuntimeAvailabilitySource）。
type w13g8StubRuntime struct{ err error }

func (s w13g8StubRuntime) LoadRuntimeAvailability(_ context.Context, _ []string) (map[string]circuitstore.AccountRuntimeAvailability, error) {
	if s.err != nil {
		return nil, s.err
	}
	return map[string]circuitstore.AccountRuntimeAvailability{}, nil
}

type w13g8StubConcurrency struct{ err error }

func (s w13g8StubConcurrency) LoadConcurrency(_ context.Context, _ []string) (map[string]int, error) {
	if s.err != nil {
		return nil, s.err
	}
	return map[string]int{}, nil
}

// w13g8NewUnitAssembly 构造只带基础句柄的 assembly（不 wire 任何家族）。
func w13g8NewUnitAssembly(t *testing.T, patch func(*workerConfig)) *workerAssembly {
	t.Helper()
	config := workerConfig{
		Enabled:      true,
		Driver:       "sqlite",
		InstanceID:   "w13g8-units",
		WorkerRole:   "ingest-worker",
		Secret:       wgBalanceSecret,
		DrainTimeout: time.Second,
	}
	if patch != nil {
		patch(&config)
	}
	return newWorkerAssembly(config, nil)
}

// TestW13G8ListProjectionFamilyDisabledArms 覆盖 wireListProjectionFamily 的
// 全部装配门禁登记分支（SQLite/缺 Redis/非法 namespace/缺 secret/探针族
// 缺失/OAuth 族缺失）。
func TestW13G8ListProjectionFamilyDisabledArms(t *testing.T) {
	ctx := context.Background()
	// 先注册 TempDir 清理（RemoveAll），再在 build() 内注册 closeStores；
	// t.Cleanup 是 LIFO：closeStores 必须先于 RemoveAll 执行。
	unitDir := t.TempDir()
	build := func(patch func(*workerConfig)) *workerAssembly {
		assembly := w13g8NewUnitAssembly(t, patch)
		t.Cleanup(assembly.closeStores)
		return assembly
	}
	assertDisabled := func(t *testing.T, assembly *workerAssembly, wantFragment string) {
		t.Helper()
		if len(assembly.disabledJobs) == 0 {
			t.Fatalf("必须登记 disabled 任务，得到 0 条")
		}
		last := assembly.disabledJobs[len(assembly.disabledJobs)-1]
		if last.JobName != "account-list-availability-projection-maintenance" {
			t.Fatalf("disabled 任务名错误: %s", last.JobName)
		}
		if !strings.Contains(last.Reason, wantFragment) {
			t.Fatalf("disabled 原因必须包含 %q: %s", wantFragment, last.Reason)
		}
	}
	// SQLite 驱动：Node PostgreSQL-only 物化器。
	sqliteAssembly := build(func(config *workerConfig) {
		config.ListProjectionEnabled = true
	})
	business, err := sqliteAssembly.openSQLite(filepath.Join(unitDir, "unit-business.sqlite3"), "w13g8-projection")
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteAssembly.wireListProjectionFamily(ctx, &businessDB{db: business}, nil); err != nil {
		t.Fatalf("SQLite 门禁必须走 disabled 登记而非错误: %v", err)
	}
	assertDisabled(t, sqliteAssembly, "PostgreSQL-only")

	// PG 驱动 + 缺 Redis。
	noRedis := build(func(config *workerConfig) {
		config.Driver = "postgres"
		config.ListProjectionEnabled = true
	})
	if err := noRedis.wireListProjectionFamily(ctx, &businessDB{}, nil); err != nil {
		t.Fatalf("缺 Redis 必须 disabled: %v", err)
	}
	assertDisabled(t, noRedis, "JUHE_AI_REDIS_STATE_URL")

	// PG + Redis + 非法 namespace。
	badNamespace := build(func(config *workerConfig) {
		config.Driver = "postgres"
		config.ListProjectionEnabled = true
		config.RedisStateURL = "redis://127.0.0.1:6379/9"
		config.RedisNamespace = "w13g8 bad namespace!"
	})
	if err := badNamespace.wireListProjectionFamily(ctx, &businessDB{}, nil); err != nil {
		t.Fatalf("非法 namespace 必须 disabled: %v", err)
	}
	assertDisabled(t, badNamespace, "JUHE_AI_REDIS_NAMESPACE")

	// PG + Redis + 合法 namespace + 缺 secret。
	noSecret := build(func(config *workerConfig) {
		config.Driver = "postgres"
		config.ListProjectionEnabled = true
		config.RedisStateURL = "redis://127.0.0.1:6379/9"
		config.RedisNamespace = "w13g8"
		config.Secret = ""
	})
	if err := noSecret.wireListProjectionFamily(ctx, &businessDB{}, nil); err != nil {
		t.Fatalf("缺 secret 必须 disabled: %v", err)
	}
	assertDisabled(t, noSecret, "JUHE_AI_SECRET")

	// secret 就绪 + 探针族未装配。
	noProbe := build(func(config *workerConfig) {
		config.Driver = "postgres"
		config.ListProjectionEnabled = true
		config.RedisStateURL = "redis://127.0.0.1:6379/9"
		config.RedisNamespace = "w13g8"
	})
	if err := noProbe.wireListProjectionFamily(ctx, &businessDB{}, nil); err != nil {
		t.Fatalf("缺探针族必须 disabled: %v", err)
	}
	assertDisabled(t, noProbe, "探针族未装配")
}

// TestW13G8ProjectionRuntimeProbeArms 用 stub 读源驱动
// projectionRuntimeProbe.Probe 的失败/成功分支。
func TestW13G8ProjectionRuntimeProbeArms(t *testing.T) {
	// runtime 失败 → fail closed。
	runtimeFail := projectionRuntimeProbe{
		runtime:     w13g8StubRuntime{err: errors.New("runtime down")},
		concurrency: w13g8StubConcurrency{},
	}
	if _, _, err := runtimeFail.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "runtime down") {
		t.Fatalf("runtime 读失败必须透传: %v", err)
	}
	// concurrency 失败 → fail closed。
	concFail := projectionRuntimeProbe{
		runtime:     w13g8StubRuntime{},
		concurrency: w13g8StubConcurrency{err: errors.New("concurrency down")},
	}
	if _, _, err := concFail.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "concurrency down") {
		t.Fatalf("concurrency 读失败必须透传: %v", err)
	}
	// 双源就绪。
	ok := projectionRuntimeProbe{runtime: w13g8StubRuntime{}, concurrency: w13g8StubConcurrency{}}
	runtimeReady, concurrencyReady, err := ok.Probe(context.Background())
	if err != nil || !runtimeReady || !concurrencyReady {
		t.Fatalf("双源就绪必须 true,true,nil: %v %v %v", runtimeReady, concurrencyReady, err)
	}
}

// TestW13G8ProjectionCredentialsAdapter 驱动 proberepo 凭据解码适配的
// 成功与坏 envelope 分支。
func TestW13G8ProjectionCredentialsAdapter(t *testing.T) {
	store, handle := w9hProbeStoreFixture(t)
	adapter := projectionCredentials{store: store}
	if _, err := adapter.DecryptCredentials("w13g8-not-an-envelope"); err == nil {
		t.Fatal("坏 envelope 必须报错")
	}
	plaintext, err := json.Marshal(map[string]any{"api_key": "sk-w13g8"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := accountbalance.EncryptV1Envelope(wgBalanceSecret, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := adapter.DecryptCredentials(envelope)
	if err != nil || credentials["api_key"] != "sk-w13g8" {
		t.Fatalf("合法 envelope 必须解码: %v %v", credentials, err)
	}
	if entries := adapter.AccountAPIKeyEntries(credentials); len(entries) != 1 {
		t.Fatalf("api_key 凭据必须产出 1 个池条目: %d", len(entries))
	}
	if entries := adapter.AccountAPIKeyEntries(map[string]any{}); len(entries) != 0 {
		t.Fatalf("空凭据必须产出 0 个池条目: %d", len(entries))
	}
	_ = handle
}

// TestW13G8ProbeFamilyDisabledOnMissingCoreTables 驱动 wireProbeFamily 的
// SQLite 契约校验失败 → 三任务 disabled 登记分支。
func TestW13G8ProbeFamilyDisabledOnMissingCoreTables(t *testing.T) {
	dir := t.TempDir()
	assembly := w13g8NewUnitAssembly(t, func(config *workerConfig) {
		config.ProbeEnabled = true
		config.BusinessSQLitePath = filepath.Join(dir, "business.sqlite3")
		config.StatsSQLitePath = filepath.Join(dir, "stats.sqlite3")
	})
	t.Cleanup(assembly.closeStores)
	if err := assembly.wireProbeFamily(context.Background()); err != nil {
		t.Fatalf("缺核心表必须走 disabled 登记而非错误: %v", err)
	}
	if len(assembly.disabledJobs) < 3 {
		t.Fatalf("探针族 disabled 必须登记至少 3 个任务: %d", len(assembly.disabledJobs))
	}
	for _, disabled := range assembly.disabledJobs {
		if !strings.Contains(disabled.Reason, "契约校验失败") && !strings.Contains(disabled.Reason, "schema 初始化失败") {
			t.Fatalf("disabled 原因必须是探针族契约原因: %s", disabled.Reason)
		}
	}
}

// TestW13G8SpeedFirstCandidateSourceArms 驱动 FindAccountForTest /
// FindCandidateAccount 的 nil 与错误分支。
func TestW13G8SpeedFirstCandidateSourceArms(t *testing.T) {
	store, handle := w9hProbeStoreFixture(t)
	source := speedFirstCandidateSource{store: store}
	ctx := context.Background()
	// 缺账户 → (nil, nil)。
	view, err := source.FindAccountForTest(ctx, "w13g8-missing", "sys")
	if err != nil || view != nil {
		t.Fatalf("缺账户必须 nil,nil: %v %v", view, err)
	}
	candidate, err := source.FindCandidateAccount(ctx, "g", "w13g8-missing", "sys")
	if err != nil || candidate != nil {
		t.Fatalf("缺候选必须 nil,nil: %v %v", candidate, err)
	}
	// store 底层连接关闭后 → 查询错误分支。
	if err := handle.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.FindAccountForTest(ctx, "w13g8-x", "sys"); err == nil {
		t.Fatal("连接关闭后必须报错")
	}
	if _, err := source.FindCandidateAccount(ctx, "g", "w13g8-x", "sys"); err == nil {
		t.Fatal("连接关闭后必须报错")
	}
}

// TestW13G8BuildWorkerAssemblySQLiteStoreFailArms 用目录路径（openSQLite 的
// PRAGMA WAL 立即失败）驱动各家族存储打开错误传播臂。
func TestW13G8BuildWorkerAssemblySQLiteStoreFailArms(t *testing.T) {
	dir := t.TempDir()
	base := func(config *workerConfig) {
		config.BusinessSQLitePath = filepath.Join(dir, "business.sqlite3")
		config.StatsSQLitePath = filepath.Join(dir, "stats.sqlite3")
		config.TaskRunsSQLitePath = filepath.Join(dir, "task-runs.sqlite3")
		config.UsageCatalogSQLitePath = filepath.Join(dir, "usage-catalog.sqlite3")
		config.UsageShardRoot = filepath.Join(dir, "usage-shards")
	}
	for _, test := range []struct {
		name     string
		patch    func(*workerConfig)
		fragment string
	}{
		{"task-runs 目录路径", func(config *workerConfig) {
			config.TaskRunsEnabled = true
			config.TaskRunsSQLitePath = dir
		}, "task-runs"},
		{"stats 目录路径", func(config *workerConfig) {
			config.StatsEnabled = true
			config.StatsSQLitePath = dir
		}, "stats"},
		{"usage-writer 目录路径", func(config *workerConfig) {
			config.UsageWriterEnabled = true
			config.UsageCatalogSQLitePath = dir
		}, "usage-writer"},
		{"balance-detect stats 目录路径", func(config *workerConfig) {
			// balance-detect 需要 task-runs 租约存储先装配成功才会打开 stats。
			config.TaskRunsEnabled = true
			config.BalanceDetectEnabled = true
			config.StatsSQLitePath = dir
		}, "balance-detect"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := workerConfig{
				Enabled:      true,
				Driver:       "sqlite",
				InstanceID:   "w13g8-fail-arms",
				WorkerRole:   "ingest-worker",
				Secret:       wgBalanceSecret,
				DrainTimeout: time.Second,
			}
			base(&config)
			test.patch(&config)
			assembly, err := buildWorkerAssembly(config, nil)
			if err == nil {
				assembly.closeStores()
				t.Fatalf("目录路径必须使装配失败")
			}
			if !strings.Contains(err.Error(), test.fragment) {
				t.Fatalf("错误必须属于 %s 家族: %v", test.fragment, err)
			}
		})
	}
	// retention：SQLite 模式缺必需路径的显式报错分支。
	retentionConfig := workerConfig{
		Enabled:          true,
		Driver:           "sqlite",
		InstanceID:       "w13g8-retention-fail",
		WorkerRole:       "ingest-worker",
		RetentionEnabled: true,
		DrainTimeout:     time.Second,
	}
	if _, err := buildWorkerAssembly(retentionConfig, nil); err == nil || !strings.Contains(err.Error(), "RETENTION_ENABLED") {
		t.Fatalf("缺业务库路径必须报 retention 配置错误: %v", err)
	}
}

// TestW13G8JobsHTTPHandlerManualBridgeArms 覆盖 /account-balance/manual 的
// 方法/服务/鉴权/解析分支（不含 RunManual 成功链，那是 PG fixture 域）。
func TestW13G8JobsHTTPHandlerManualBridgeArms(t *testing.T) {
	const secret = "w13g8-manual-secret-0123456789abcdef"
	newHandler := func(service *accountbalance.Service) http.Handler {
		return jobsHTTPHandler(ownermode.Mode("owner"), &atomic.Bool{}, func() bool { return true },
			false, nil, true, func() bool { return true }, service, secret)
	}
	do := func(handler http.Handler, method string, body string, header map[string]string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/account-balance/manual", strings.NewReader(body))
		for key, value := range header {
			request.Header.Set(key, value)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	auth := map[string]string{"Authorization": "Bearer " + secret}
	// GET → 404。
	if response := do(newHandler(nil), http.MethodGet, "", auth); response.Code != http.StatusNotFound {
		t.Fatalf("GET 必须 404: %d", response.Code)
	}
	// POST + 服务未装配 → 404。
	if response := do(newHandler(nil), http.MethodPost, `{"input":{}}`, auth); response.Code != http.StatusNotFound {
		t.Fatalf("服务未装配 POST 必须 404: %d", response.Code)
	}
	// POST + 缺鉴权头 → 401。
	if response := do(newHandler(&accountbalance.Service{}), http.MethodPost, `{"input":{}}`, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("缺鉴权必须 401: %d", response.Code)
	}
	// POST + 错误 secret → 401。
	badAuth := map[string]string{"Authorization": "Bearer wrong-secret-0123456789abcdef!!!!"}
	if response := do(newHandler(&accountbalance.Service{}), http.MethodPost, `{"input":{}}`, badAuth); response.Code != http.StatusUnauthorized {
		t.Fatalf("错误 secret 必须 401: %d", response.Code)
	}
	// POST + 非法 JSON → 400。
	if response := do(newHandler(&accountbalance.Service{}), http.MethodPost, `w13g8-not-json`, auth); response.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 必须 400: %d", response.Code)
	}
	// POST + 尾随 JSON → 400。
	if response := do(newHandler(&accountbalance.Service{}), http.MethodPost, `{"input":{}} {"trailing":1}`, auth); response.Code != http.StatusBadRequest {
		t.Fatalf("尾随 JSON 必须 400: %d", response.Code)
	}
	// POST + 未知字段 → 400（DisallowUnknownFields）。
	if response := do(newHandler(&accountbalance.Service{}), http.MethodPost, `{"input":{},"unexpected":1}`, auth); response.Code != http.StatusBadRequest {
		t.Fatalf("未知字段必须 400: %d", response.Code)
	}
}

// TestW13G8HealthHandlerDefaultSlots 覆盖 healthHandler 无 j2 槽位时的
// 默认值分支（J2/J3a/J3b/worker 全部未启用）。
func TestW13G8HealthHandlerDefaultSlots(t *testing.T) {
	// healthHandler 的 ready 依赖 runtimeLogOwnerHeld（F1 owner lease 持有）。
	runtimeRunning := &atomic.Bool{}
	runtimeRunning.Store(true)
	handler := healthHandler(ownermode.Mode("owner"), runtimeRunning, func() bool { return true }, false, nil)
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("/health 必须 200: %d", response.Code)
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"accountBalanceEnabled": false,
		"proxyLatencyEnabled":   false,
		"modelCheckEnabled":     false,
		"workerEnabled":         false,
		"ready":                 true,
	} {
		if payload[key] != want {
			t.Fatalf("%s 必须为 %v: %v", key, want, payload[key])
		}
	}
	if _, ok := payload["worker"]; ok {
		t.Fatalf("workerStatus 默认 nil 时不得输出 worker 快照: %v", payload["worker"])
	}
	// 非 GET /health 路径 → 404。
	notFound := httptest.NewRecorder()
	handler.ServeHTTP(notFound, httptest.NewRequest(http.MethodPost, "/health", nil))
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("POST /health 必须 404: %d", notFound.Code)
	}
}
