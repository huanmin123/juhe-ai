package modelcheckowner

// w14m 覆盖率补强（第二批）：trust 投影的写入/游标错误臂、账户选项列表的
// 查询/扫描/迭代/合并错误臂与去重臂、business_schema SQLite 检查级失败臂、
// HTTP 处理器的解码与作用域校验臂。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// ---- trust_store：投影事务各写入臂 ----

func w14mTrustStore(t *testing.T) (*Store, *w14mFailpoint) {
	t.Helper()
	db, fp := w14mFailDB(t, runtimeTestDDL())
	return &Store{db: db, mode: "sqlite"}, fp
}

func w14mSeedTrustObservation(t *testing.T, store *Store) TrustProjection {
	t.Helper()
	const created = "2026-08-31T10:00:00Z"
	if _, err := store.db.Exec(`INSERT INTO model_check_observations(id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES ('obs-a','run-1','sys','acct','openai','gpt-5.6','gpt-5.6','protocol_basic','complete','consistent','unknown','consistent',100,?)`, created); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO model_trust_latest_dirty_accounts(system_account_id,account_id,requested_model,dirty_reason,updated_at) VALUES ('sys','acct','gpt-5.6','baseline_changed',?)`, created); err != nil {
		t.Fatal(err)
	}
	return TrustProjection{RunID: "run-1", SystemAccountID: "sys", AccountID: "acct", RequestedModel: "gpt-5.6", Report: TrustReport{IdentityStatus: "consistent", MappingStatus: "direct", UsageIntegrityStatus: "insufficient_evidence", ProtocolStatus: "consistent", EvidenceStatus: "stable", EvidenceFormed: true, TrustFormed: true, TrustScore: 1, EvidenceCoverage: 100, ReasonCodes: []string{"z", "a", "a"}}}
}

func TestW14MTrustProjectionWriteArms(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		need    string
	}{
		{"markConsumed", "SET aggregation_completed_at=COALESCE", "mark J3b trust observations consumed"},
		{"clearDirty", "DELETE FROM model_trust_latest_dirty_accounts", "clear J3b trust latest dirty result"},
		{"observationsQuery", "SELECT id,created_at,mapping_status", "read J3b trust observations"},
		{"receiptInsert", "INSERT INTO model_trust_observation_receipts", "record J3b trust observation receipt"},
		{"latestInsert", "INSERT INTO model_account_trust_results", "insert J3b trust latest result"},
		{"latestSelect", "SELECT identity_status,mapping_status", "read J3b trust latest result"},
		{"cursorInsert", "INSERT INTO model_trust_aggregation_state", "insert J3b trust cursor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fp := w14mTrustStore(t)
			projection := w14mSeedTrustObservation(t, store)
			fp.arm(tc.pattern)
			defer fp.disarm()
			err := store.ProjectTrust(context.Background(), projection)
			if err == nil || !strings.Contains(err.Error(), tc.need) {
				t.Fatalf("err = %v, want 包含 %q", err, tc.need)
			}
		})
	}

	t.Run("scanObservation", func(t *testing.T) {
		store, fp := w14mTrustStore(t)
		projection := w14mSeedTrustObservation(t, store)
		fp.armScan("SELECT id,created_at,mapping_status")
		defer fp.disarm()
		if err := store.ProjectTrust(context.Background(), projection); err == nil || !strings.Contains(err.Error(), "scan J3b trust observation") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("iterateObservations", func(t *testing.T) {
		store, fp := w14mTrustStore(t)
		projection := w14mSeedTrustObservation(t, store)
		fp.armNextErr("SELECT id,created_at,mapping_status")
		defer fp.disarm()
		if err := store.ProjectTrust(context.Background(), projection); err == nil || !strings.Contains(err.Error(), "iterate J3b trust observations") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("commit", func(t *testing.T) {
		store, fp := w14mTrustStore(t)
		projection := w14mSeedTrustObservation(t, store)
		fp.failCommitsFrom = 1
		defer fp.disarm()
		if err := store.ProjectTrust(context.Background(), projection); err == nil || !strings.Contains(err.Error(), "commit J3b trust projection") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("cursorUpdate", func(t *testing.T) {
		store, fp := w14mTrustStore(t)
		projection := w14mSeedTrustObservation(t, store)
		// 预置一个落后的聚合游标 → 走 UPDATE 推进分支。
		if _, err := store.db.Exec(`INSERT INTO model_trust_aggregation_state(scope_key,cursor_created_at,cursor_id,last_success_at,updated_at) VALUES (?,?,?,?,'')`,
			trustAggregationScope, "2026-08-30T10:00:00Z", "obs-0", "2026-08-30T10:00:00Z"); err != nil {
			t.Fatal(err)
		}
		fp.arm("UPDATE model_trust_aggregation_state")
		defer fp.disarm()
		if err := store.ProjectTrust(context.Background(), projection); err == nil || !strings.Contains(err.Error(), "advance J3b trust cursor") {
			t.Fatalf("err = %v", err)
		}
	})
}

// ---- account_options：查询/扫描/迭代/合并臂 ----

// w14mOptionsDDL 与 account_options_test 的表结构一致（含 protocol_version）。
var w14mOptionsDDL = []string{
	`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,name TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,protocol_code TEXT,protocol_version TEXT,type TEXT,status TEXT,schedulable INTEGER,account_expires_at TEXT,cooldown_until TEXT,last_error_code TEXT,availability_schedule_json TEXT,proxy_profile_id TEXT,authorization_instance_authorization_id TEXT,authorization_instance_source_account_id TEXT,deleted_at TEXT)`,
	`CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY,enabled INTEGER)`,
	// w14mOptionsAccountsDDL 的 group_accounts 需含 system_account_id。
	`CREATE TABLE group_accounts (account_id TEXT,system_account_id TEXT,group_id TEXT,account_authorization_id TEXT,enabled INTEGER)`,
	`CREATE TABLE groups (id TEXT PRIMARY KEY,system_account_id TEXT,enabled INTEGER)`,
	`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY,resource_type TEXT,resource_id TEXT,resource_owner_system_account_id TEXT,grantee_system_account_id TEXT,scope TEXT,status TEXT,expires_at TEXT)`,
	`CREATE TABLE account_supported_models (account_id TEXT,model TEXT)`,
	`CREATE TABLE account_model_mappings (account_id TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,upstream_endpoint_family TEXT,enabled INTEGER)`,
}

func w14mOptionsDB(t *testing.T) (*sql.DB, *w14mFailpoint) {
	t.Helper()
	return w14mFailDB(t, w14mOptionsDDL)
}

func w14mSeedOptionAccount(t *testing.T, db *sql.DB, id, systemID string) {
	t.Helper()
	statements := []string{
		`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1',1)`,
		`INSERT INTO groups VALUES ('group-1','` + systemID + `',1)`,
		`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('` + id + `','` + systemID + `','group-1',1)`,
		`INSERT INTO accounts(id,system_account_id,name,provider_code,provider_protocol_profile_id,protocol_code,protocol_version,type,status,schedulable,deleted_at) VALUES ('` + id + `','` + systemID + `','` + id + `','openai','profile_openai_openai_v1','openai','1','api_key','active',1,NULL)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("执行 %q 失败: %v", statement, err)
		}
	}
}

func TestW14MAccountOptionsValidationArms(t *testing.T) {
	var nilSource *BusinessTargetSource
	if _, err := nilSource.ListAccountOptions(context.Background(), AccountOptionsQuery{Purpose: "run"}); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil source 必须拒绝: %v", err)
	}
	empty := &BusinessTargetSource{}
	if _, err := empty.ListAccountOptions(context.Background(), AccountOptionsQuery{Purpose: "run"}); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil 库必须拒绝: %v", err)
	}
	db, fp := w14mBusinessDB(t)
	w14mSeedPlainAccount(t, db, "acct-1")
	source := w11eNewSource(t, db)
	defer fp.disarm()
	if _, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{Purpose: "run", SystemAccountID: "sys-1", AllSystemAccounts: true}); err == nil || !strings.Contains(err.Error(), "global scope cannot include systemAccountId") {
		t.Fatalf("全局 scope 叠加必须拒绝: %v", err)
	}
}

func TestW14MAccountOptionsQueryArms(t *testing.T) {
	ctx := context.Background()
	query := AccountOptionsQuery{Purpose: "run", SystemAccountID: "sys-1", Limit: 10}

	t.Run("queryError", func(t *testing.T) {
		db, fp := w14mOptionsDB(t)
		w14mSeedOptionAccount(t, db, "acct-1", "sys-1")
		source := w11eNewSource(t, db)
		fp.arm("ORDER BY LOWER(a.name)")
		defer fp.disarm()
		if _, err := source.ListAccountOptions(ctx, query); err == nil || !strings.Contains(err.Error(), "read J3b account options") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("scanError", func(t *testing.T) {
		db, fp := w14mOptionsDB(t)
		w14mSeedOptionAccount(t, db, "acct-1", "sys-1")
		source := w11eNewSource(t, db)
		fp.armScan("ORDER BY LOWER(a.name)")
		defer fp.disarm()
		if _, err := source.ListAccountOptions(ctx, query); err == nil || !strings.Contains(err.Error(), "scan J3b account option") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("iterateError", func(t *testing.T) {
		db, fp := w14mOptionsDB(t)
		w14mSeedOptionAccount(t, db, "acct-1", "sys-1")
		source := w11eNewSource(t, db)
		fp.armNextErr("ORDER BY LOWER(a.name)")
		defer fp.disarm()
		if _, err := source.ListAccountOptions(ctx, query); err == nil || !strings.Contains(err.Error(), "iterate J3b account options") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("configuredModelsError", func(t *testing.T) {
		db, fp := w14mOptionsDB(t)
		w14mSeedOptionAccount(t, db, "acct-1", "sys-1")
		source := w11eNewSource(t, db)
		fp.arm("FROM account_model_mappings")
		defer fp.disarm()
		withAccount := query
		withAccount.AccountID = "acct-1"
		if _, err := source.ListAccountOptions(ctx, withAccount); err == nil {
			t.Fatalf("configuredModelCheckModels 失败应传播")
		}
	})

	t.Run("authorizedMergeError", func(t *testing.T) {
		db, fp := w14mOptionsDB(t)
		w14mSeedOptionAccount(t, db, "acct-1", "sys-1")
		source := w11eNewSource(t, db)
		fp.arm("ra.grantee_system_account_id=")
		defer fp.disarm()
		if _, err := source.ListAccountOptions(ctx, query); err == nil {
			t.Fatalf("授权列表失败应传播")
		}
	})

	t.Run("authorizedDedup", func(t *testing.T) {
		db, fp := w14mBusinessDB(t)
		envelope := testCredentialEnvelope(t, "secret", `{"api_key":"key","supported_endpoint_modes":["responses_sse"]}`)
		w14mSeedAuthorizedFixture(t, db, envelope)
		// 账户选项查询引用 protocol_version 与非空 name，业务契约 DDL 尚未包含。
		if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN protocol_version TEXT`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE accounts SET name=id, protocol_version='1' WHERE name IS NULL OR protocol_version IS NULL`); err != nil {
			t.Fatal(err)
		}
		source := w11eNewSource(t, db)
		defer fp.disarm()
		options, err := source.ListAccountOptions(ctx, AccountOptionsQuery{Purpose: "run", SystemAccountID: "sys-1", Limit: 50})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		seen := map[string]int{}
		for _, item := range options {
			seen[item.ID]++
		}
		if seen["acct-source"] != 1 {
			t.Fatalf("acct-source 应去重为一次: %v", seen)
		}
	})
}

// ---- business_schema：SQLite 检查级失败臂 ----

// w14mContractTablesDB 按契约 spec 创建全表（全部必需列，TEXT 类型），
// 使 CheckBusinessSQLiteSchema 能越过缺表检查进入逐表 PRAGMA 检查。
func w14mContractTablesDB(t *testing.T) (*sql.DB, *w14mFailpoint) {
	t.Helper()
	statements := make([]string, 0, len(contracts.BusinessSQLiteSchema))
	for table, spec := range contracts.BusinessSQLiteSchema {
		cols := make([]string, 0, len(spec.Columns))
		for _, column := range spec.Columns {
			cols = append(cols, `"`+column+`" TEXT`)
		}
		statements = append(statements, "CREATE TABLE \""+table+"\" ("+strings.Join(cols, ",")+")")
	}
	return w14mFailDB(t, statements)
}

func TestW14MBusinessSQLiteSchemaCheckArms(t *testing.T) {
	ctx := context.Background()

	t.Run("columns", func(t *testing.T) {
		db, fp := w14mContractTablesDB(t)
		fp.arm("PRAGMA table_info")
		defer fp.disarm()
		if err := CheckBusinessSQLiteSchema(ctx, db); err == nil || !strings.Contains(err.Error(), "w14m") {
			t.Fatalf("err = %v", err)
		}
	})

	// 第二次 table_info 是主键检查；若首个遍历表未声明 PrimaryKey，该臂不会
	// 到达（契约表均未显式声明主键时由 w14f 的 helper 级测试覆盖）。
	t.Run("indexesList", func(t *testing.T) {
		db, fp := w14mContractTablesDB(t)
		fp.armAfter("FROM sqlite_master", 1)
		defer fp.disarm()
		if err := CheckBusinessSQLiteSchema(ctx, db); err == nil || !strings.Contains(err.Error(), "list Business SQLite indexes") {
			t.Fatalf("err = %v", err)
		}
	})
}

// ---- HTTP：解码与作用域臂 ----

func TestW14MHTTPDecodeAndScopeArms(t *testing.T) {
	handler := newTestHTTPHandler()

	t.Run("runDecodeError", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{bad-json`)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("streamDecodeError", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{bad-json`)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("policyPatchDecodeError", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/quality-policy", strings.NewReader(`{bad-json`)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("scheduleCreateDecodeError", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/quality-schedules", strings.NewReader(`{bad-json`)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("scopeParamInvalid", func(t *testing.T) {
		local := newTestHTTPHandler()
		local.AllowCrossAccount = true
		local.AccountOptions = w14mStubAccountOptions{}
		recorder := httptest.NewRecorder()
		local.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/account-options?purpose=run&systemAccountId=&systemAccountId=sys-2", nil))
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "systemAccountId") {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("optionsMissingActorScope", func(t *testing.T) {
		local := newTestHTTPHandler()
		local.AllowCrossAccount = true
		local.AccountOptions = w14mStubAccountOptions{}
		request := httptest.NewRequest(http.MethodGet, "/account-options?purpose=run", nil)
		recorder := httptest.NewRecorder()
		// 直接触发作用域缺失臂：scope 既无 AllSystemAccounts 也无 Selected 值。
		local.serveAccountOptions(recorder, request, ManagementScope{ActorSystemAccountID: "sys-1"})
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("deleteScheduleEmptyID", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/quality-schedules/", nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})
}

// w14mStubAccountOptions 仅满足接口以通过 owner 接线守卫。
type w14mStubAccountOptions struct{}

func (w14mStubAccountOptions) ListAccountOptions(context.Context, AccountOptionsQuery) ([]AccountOption, error) {
	return nil, nil
}

func (w14mStubAccountOptions) ModelCheckOptions() ModelCheckOptions {
	return ModelCheckOptions{}
}

var _ = sql.ErrNoRows
var _ = contracts.BusinessSQLiteSchemaVersion
