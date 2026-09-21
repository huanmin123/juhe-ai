package modelcheckowner

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
	_ "modernc.org/sqlite"
)

// wbStubSchedulerSource / wbStubExecutor 是宿主装配测试所需的最小调度端口。
type wbStubSchedulerSource struct{}

func (wbStubSchedulerSource) Claim(context.Context, SchedulerKind, time.Time, int) ([]ScheduleTask, error) {
	return nil, nil
}

type wbStubExecutor struct{}

func (wbStubExecutor) Execute(context.Context, ScheduleTask) error { return nil }

// wbEmptyTokenizer / wbEmptyLimits 用于验证宿主对空版本快照的拒绝。
type wbEmptyTokenizer struct{}

func (wbEmptyTokenizer) Version() string           { return " " }
func (wbEmptyTokenizer) Count(string) (int, error) { return 0, nil }

type wbEmptyLimits struct{}

func (wbEmptyLimits) Version() string { return "" }
func (wbEmptyLimits) MaxInputTokens(string, string, modelcheckprofile.Protocol) (int, error) {
	return 0, nil
}

// wbCompleteStoreSeed 在空库上按 requiredColumns 契约建表，
// 与 CheckSchema 的只读校验保持同一份事实来源。
func wbCompleteStoreSeed(t *testing.T, db *sql.DB) {
	t.Helper()
	for table, columns := range requiredColumns {
		definitions := make([]string, 0, len(columns))
		for _, column := range columns {
			definitions = append(definitions, column+" TEXT")
		}
		if _, err := db.Exec(`CREATE TABLE ` + table + ` (` + strings.Join(definitions, ",") + `)`); err != nil {
			t.Fatalf("建表 %s 失败: %v", table, err)
		}
	}
}

// wbSeedSQLiteFile 在临时目录准备一个指定形态的 SQLite 文件库并关闭句柄。
func wbSeedSQLiteFile(t *testing.T, name string, complete bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	if complete {
		wbCompleteStoreSeed(t, seed)
	}
	return path
}

func wbValidHostDeps() HostDependencies {
	return HostDependencies{
		Resolve:        func(context.Context, RunRequest) (Target, error) { return Target{}, nil },
		Authorize:      func(context.Context, *http.Request) (string, error) { return "sys-1", nil },
		Build:          func(context.Context, string, RunCommand) (RunRequest, error) { return RunRequest{}, nil },
		Enforcement:    EnforcementApplierFunc(func(context.Context, QualityEnforcement) error { return nil }),
		Quality:        &wbQualityStub{},
		HealthStatHour: func(time.Time) (string, error) { return "2026-09-01T10", nil },
		Tokenizer:      runtimeTestTokenizer{},
		ModelLimits:    runtimeTestModelLimits{},
		Scheduler:      wbStubSchedulerSource{},
		Executor:       wbStubExecutor{},
	}
}

// OpenHost 门禁契约：任何未就绪的配置/依赖都必须失败关闭，
// 且失败路径不得返回半装配的宿主。
func TestWBOpenHostFailsClosedOnIncompleteGates(t *testing.T) {
	readyConfig := func(path string) Config {
		return Config{Enabled: true, StoreMode: "sqlite", DatabasePath: path, BusinessHandoffConfirmed: true, NodeWriterStopped: true, SchemaReady: true, HealthBoundaryReady: true, RuntimeReady: true}
	}
	untouchedPath := wbSeedSQLiteFile(t, "wb-gates-untouched.db", true)
	cases := []struct {
		name string
		cfg  Config
		deps func() HostDependencies
		need string
	}{
		{name: "disabled config", cfg: Config{Enabled: false}, deps: wbValidHostDeps, need: "disabled"},
		{name: "handoff without stopped node writer", cfg: Config{Enabled: true, BusinessHandoffConfirmed: true}, deps: wbValidHostDeps, need: "Node writer"},
		{name: "missing resolve", cfg: readyConfig(untouchedPath), deps: func() HostDependencies {
			deps := wbValidHostDeps()
			deps.Resolve = nil
			return deps
		}, need: "dependencies are incomplete"},
		{name: "blank tokenizer version", cfg: readyConfig(untouchedPath), deps: func() HostDependencies {
			deps := wbValidHostDeps()
			deps.Tokenizer = wbEmptyTokenizer{}
			return deps
		}, need: "tokenizer snapshot"},
		{name: "blank model limits version", cfg: readyConfig(untouchedPath), deps: func() HostDependencies {
			deps := wbValidHostDeps()
			deps.ModelLimits = wbEmptyLimits{}
			return deps
		}, need: "model-limit snapshot"},
		{name: "schema missing", cfg: readyConfig(wbSeedSQLiteFile(t, "wb-gates-empty.db", false)), deps: wbValidHostDeps, need: "verify J3b Gateway schema"},
		{name: "scheduler missing", cfg: readyConfig(wbSeedSQLiteFile(t, "wb-gates-complete.db", true)), deps: func() HostDependencies {
			deps := wbValidHostDeps()
			deps.Scheduler = nil
			deps.Executor = nil
			return deps
		}, need: "scheduler dependencies are incomplete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, err := OpenHost(context.Background(), tc.cfg, tc.deps())
			if err == nil || !strings.Contains(err.Error(), tc.need) {
				t.Fatalf("OpenHost err=%v want 包含 %q", err, tc.need)
			}
			if host != nil {
				t.Fatal("失败路径不得返回宿主")
			}
		})
	}
}

// OpenHost 成功装配契约：HTTP 与调度器共享同一 Store/Runtime，
// Mount 暴露管理路由，Close 释放存储并解除就绪状态。
func TestWBOpenHostAssemblesAndClosesCompleteOwner(t *testing.T) {
	path := wbSeedSQLiteFile(t, "wb-host-complete.db", true)
	host, err := OpenHost(context.Background(), testSQLiteConfig(path), wbValidHostDeps())
	if err != nil {
		t.Fatalf("OpenHost 失败: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	if !host.Ready() || host.Store == nil || host.Runtime == nil || host.Projector == nil || host.Scheduler == nil || host.Handler == nil {
		t.Fatalf("宿主装配不完整: ready=%v store=%v runtime=%v projector=%v scheduler=%v handler=%v", host.Ready(), host.Store, host.Runtime, host.Projector, host.Scheduler, host.Handler)
	}
	if host.Runtime.Store != host.Store || host.Projector.Store != host.Store {
		t.Fatal("Runtime/Projector 必须与宿主共享同一存储")
	}
	mux := http.NewServeMux()
	if err := host.Mount(mux, "/j3b/"); err != nil {
		t.Fatalf("Mount 失败: %v", err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/j3b/run/active", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("挂载后的路由 status=%d body=%s", response.Code, response.Body.String())
	}
	mountCases := []struct {
		name   string
		host   *Host
		mux    *http.ServeMux
		prefix string
		need   string
	}{
		{name: "unready host", host: &Host{}, mux: http.NewServeMux(), prefix: "/j3b/", need: "not ready"},
		{name: "nil mux", host: host, mux: nil, prefix: "/j3b/", need: "mux is nil"},
		{name: "relative prefix", host: host, mux: http.NewServeMux(), prefix: "j3b", need: "prefix"},
	}
	for _, tc := range mountCases {
		t.Run("mount "+tc.name, func(t *testing.T) {
			if err := tc.host.Mount(tc.mux, tc.prefix); err == nil || !strings.Contains(err.Error(), tc.need) {
				t.Fatalf("Mount err=%v want 包含 %q", err, tc.need)
			}
		})
	}
	component := host.Component()
	if component.Name != "J3b model-check owner" {
		t.Fatalf("supervisor 组件名=%q", component.Name)
	}
	if err := component.Close(); err != nil {
		t.Fatalf("组件 Close 失败: %v", err)
	}
	if host.Ready() {
		t.Fatal("Close 之后宿主必须解除就绪状态")
	}
	if err := host.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("关闭后的宿主必须拒绝 Run: err=%v", err)
	}
}

// Host.Run/Close 空值契约：nil 宿主不得 panic。
func TestWBHostNilAndUnreadyContracts(t *testing.T) {
	var host *Host
	if err := host.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("nil 宿主 Run err=%v", err)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("nil 宿主 Close 必须为幂等成功: err=%v", err)
	}
	unready := &Host{}
	if err := unready.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("未就绪宿主 Run err=%v", err)
	}
	if unready.Ready() {
		t.Fatal("未装配宿主不得报告就绪")
	}
	var component supervisor.Component = unready.Component()
	if component.Name == "" {
		t.Fatal("nil 宿主的组件名不得为空")
	}
	if err := component.Close(); err != nil {
		t.Fatalf("nil 宿主组件 Close 必须幂等成功: %v", err)
	}
}

// BusinessSchedulerSource.CheckContract 契约：调度/恢复租约事务依赖的
// 四张 Business 表与列必须存在，缺失任何一张都要失败关闭。
func TestWBBusinessSchedulerCheckContractVerifiesBusinessTables(t *testing.T) {
	ddl := []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,provider_code TEXT,config_revision INTEGER,dispatch_revision INTEGER,authorization_instance_source_account_id TEXT,deleted_at TEXT,authorization_instance_authorization_id TEXT,status TEXT,health_check_model TEXT,availability_schedule_json TEXT,schedulable INTEGER,fallback_enabled INTEGER,super_priority_enabled INTEGER,last_error_code TEXT,last_error_message TEXT,updated_at TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,revision INTEGER,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,custom_question_ids TEXT,enabled INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,updated_at TEXT)`,
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,created_at TEXT,updated_at TEXT,custom_question_ids TEXT)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,trigger_run_id TEXT,config_source TEXT,config_source_id TEXT,policy_revision INTEGER,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_model TEXT,account_config_revision INTEGER,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,last_recovery_run_id TEXT,cleared_at TEXT,updated_at TEXT)`,
	}
	t.Run("contract satisfied", func(t *testing.T) {
		source := &BusinessSchedulerSource{Business: wbOpenMemoryDB(t, ddl)}
		if err := source.CheckContract(context.Background()); err != nil {
			t.Fatalf("契约校验失败: %v", err)
		}
	})
	t.Run("missing table", func(t *testing.T) {
		db := wbOpenMemoryDB(t, ddl[:3])
		source := &BusinessSchedulerSource{Business: db}
		if err := source.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "account_quality_enforcements") {
			t.Fatalf("缺表必须失败关闭: err=%v", err)
		}
	})
	t.Run("uninitialized", func(t *testing.T) {
		source := &BusinessSchedulerSource{}
		if err := source.CheckContract(context.Background()); err == nil {
			t.Fatal("未初始化的调度源必须报错")
		}
	})
}

// BusinessSchedulerSource 生命周期契约：只有健康重试任务走 J3b 存储，
// scheduled/recovery 的完成与失败由 Business 事务独占（此处为显式 no-op）。
func TestWBBusinessSchedulerLifecycleDelegatesHealthRetryOnly(t *testing.T) {
	j3b := wbOpenMemoryDB(t, []string{
		`CREATE TABLE model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT,due_at TEXT,claim_owner TEXT,claim_until TEXT,fence_token INTEGER,state TEXT,last_error TEXT,completed_at TEXT,payload TEXT,updated_at TEXT)`,
		`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,account_id TEXT,system_account_id TEXT,provider_code TEXT,model TEXT,profile TEXT,level TEXT,score INTEGER,schedule_id TEXT,policy_snapshot_json TEXT,quality_decision_json TEXT,request_summary_json TEXT,finished_at TEXT,status TEXT,quality_health_sync_status TEXT,updated_at TEXT)`,
	})
	store := &Store{db: j3b, mode: "sqlite"}
	defer store.Close()
	now := time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC)
	insertTask := func(id string) {
		t.Helper()
		if _, err := j3b.Exec(`INSERT INTO model_check_scheduler_tasks (id,kind,due_at,state,fence_token,payload,updated_at) VALUES (?,'health_sync_retry',?,'pending',0,?,?)`, id, now.Add(-time.Minute).Format(time.RFC3339Nano), `{"runId":"run-1"}`, now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	source := &BusinessSchedulerSource{Business: wbOpenMemoryDB(t, nil), Store: store, OwnerID: "wb-owner"}
	t.Run("non health kinds are explicit no-ops", func(t *testing.T) {
		task := ScheduleTask{ID: "schedule:1", Kind: SchedulerScheduled, OwnerID: "wb-owner", FenceToken: 1}
		if err := source.Complete(context.Background(), task); err != nil {
			t.Fatalf("scheduled 完成必须为显式 no-op: %v", err)
		}
		if err := source.Fail(context.Background(), task, errors.New("boom")); err != nil {
			t.Fatalf("scheduled 失败必须为显式 no-op: %v", err)
		}
	})
	t.Run("health retry complete", func(t *testing.T) {
		insertTask("health:run-complete")
		tasks, err := source.Claim(context.Background(), SchedulerHealthRetry, now, 5)
		if err != nil || len(tasks) != 1 {
			t.Fatalf("健康重试任务认领=%+v err=%v", tasks, err)
		}
		if err := source.Complete(context.Background(), tasks[0]); err != nil {
			t.Fatalf("健康重试完成失败: %v", err)
		}
		var state string
		if err := j3b.QueryRow(`SELECT state FROM model_check_scheduler_tasks WHERE id=?`, tasks[0].ID).Scan(&state); err != nil || state != "completed" {
			t.Fatalf("任务状态=%q err=%v", state, err)
		}
	})
	t.Run("health retry fail records cause", func(t *testing.T) {
		insertTask("health:run-fail")
		tasks, err := source.Claim(context.Background(), SchedulerHealthRetry, now, 5)
		if err != nil || len(tasks) != 1 {
			t.Fatalf("健康重试任务认领=%+v err=%v", tasks, err)
		}
		if err := source.Fail(context.Background(), tasks[0], errors.New("健康投影失败")); err != nil {
			t.Fatalf("健康重试失败登记: %v", err)
		}
		var state, lastError string
		if err := j3b.QueryRow(`SELECT state,last_error FROM model_check_scheduler_tasks WHERE id=?`, tasks[0].ID).Scan(&state, &lastError); err != nil || state != "failed" || lastError != "健康投影失败" {
			t.Fatalf("任务状态=%q last_error=%q err=%v", state, lastError, err)
		}
	})
	t.Run("uninitialized claim", func(t *testing.T) {
		if _, err := (&BusinessSchedulerSource{Store: store}).Claim(context.Background(), SchedulerScheduled, now, 5); err == nil {
			t.Fatal("缺少 OwnerID 的认领必须报错")
		}
		if _, err := source.Claim(context.Background(), SchedulerKind("bogus"), now, 5); err == nil {
			t.Fatal("未知任务族必须报错")
		}
	})
}
