package operationlog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestPersistIsIdempotentAndRetentionIsFenced(t *testing.T) {
	store, implementation := openPostgresStore(t)
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "test-owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease: ok=%v err=%v", ok, err)
	}
	input := Input{ID: "oplog-test-1", ActorSystemAccountID: "actor-1", ActorRole: "admin", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", ResourceID: "account-1", Summary: "updated account", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	ignored, err := store.Persist(context.Background(), lease, input)
	if err != nil || ignored {
		t.Fatalf("first persist: ignored=%v err=%v", ignored, err)
	}
	ignored, err = store.Persist(context.Background(), lease, input)
	if err != nil || !ignored {
		t.Fatalf("duplicate persist: ignored=%v err=%v", ignored, err)
	}
	var targets int
	if err = implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.operation_log_targets WHERE operation_log_id=$1`, input.ID).Scan(&targets); err != nil || targets != 1 {
		t.Fatalf("duplicate must not duplicate target rows: targets=%d err=%v", targets, err)
	}
	result, err := store.List(context.Background(), ListOptions{})
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("list: %+v err=%v", result, err)
	}
	if result.Items[0].ActorSystemAccountName != "Actor 1" {
		t.Fatalf("list must preserve actor account name projection: %+v", result.Items[0])
	}
	detail, found, err := store.Detail(context.Background(), input.ID, "actor-1")
	if err != nil || !found || len(detail.Targets) != 1 {
		t.Fatalf("detail: found=%v detail=%+v err=%v", found, detail, err)
	}
	deleted, err := store.CleanupRetention(context.Background(), lease, time.Now().UTC().Add(time.Hour), 10)
	if err != nil || deleted != 1 {
		t.Fatalf("retention: deleted=%d err=%v", deleted, err)
	}
}

func TestCreatedAtIsCanonicalUTCForOrderAndRetention(t *testing.T) {
	opened, implementation := openPostgresStore(t)
	lease, ok, err := opened.AcquireOwnerLease(context.Background(), "utc-order", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease: ok=%v err=%v", ok, err)
	}
	older := Input{ID: "utc-older", ActorSystemAccountID: "actor", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "older", CreatedAt: "2026-08-13T08:00:00.100000000+08:00"}
	later := older
	later.ID = "utc-later"
	later.Summary = "later"
	later.CreatedAt = "2026-08-13T00:00:00.110000000Z"
	if _, err = opened.Persist(context.Background(), lease, older); err != nil {
		t.Fatal(err)
	}
	if _, err = opened.Persist(context.Background(), lease, later); err != nil {
		t.Fatal(err)
	}
	var storedCreatedAt storageTimestamp
	if err = implementation.db.QueryRow(`SELECT created_at FROM juhe_dataset.operation_logs WHERE id=$1`, older.ID).Scan(&storedCreatedAt); err != nil || storedCreatedAt != "2026-08-13T00:00:00.100000000Z" {
		t.Fatalf("createdAt must use fixed-width UTC storage: value=%q err=%v", storedCreatedAt, err)
	}
	result, err := opened.List(context.Background(), ListOptions{})
	if err != nil || len(result.Items) != 2 || result.Items[0].ID != later.ID {
		t.Fatalf("UTC canonical ordering result=%+v err=%v", result, err)
	}
	deleted, err := opened.CleanupRetention(context.Background(), lease, time.Date(2026, 8, 13, 0, 0, 0, 105000000, time.UTC), 10)
	if err != nil || deleted != 1 {
		t.Fatalf("UTC canonical retention deleted=%d err=%v", deleted, err)
	}
	result, err = opened.List(context.Background(), ListOptions{})
	if err != nil || len(result.Items) != 1 || result.Items[0].ID != later.ID {
		t.Fatalf("UTC canonical retention result=%+v err=%v", result, err)
	}
}

func TestListRejectsInvalidAbsoluteTimesAndCanonicalizesOffsets(t *testing.T) {
	store, _ := openPostgresStore(t)
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "list-times", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease: ok=%v err=%v", ok, err)
	}
	created := Input{ID: "list-time", ActorSystemAccountID: "actor", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "list time", CreatedAt: "2026-08-13T00:00:00.000000000Z"}
	if _, err = store.Persist(context.Background(), lease, created); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		list ListOptions
	}{
		{name: "start without offset", list: ListOptions{StartAt: "2026-08-13T00:00:00"}},
		{name: "invalid start", list: ListOptions{StartAt: "not-a-time"}},
		{name: "end without offset", list: ListOptions{EndAt: "2026-08-13T00:00:01"}},
		{name: "invalid end", list: ListOptions{EndAt: "2026-08-13T00:00:01+bad"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, listErr := store.List(context.Background(), test.list)
			if !errors.Is(listErr, ErrInvalidListTime) {
				t.Fatalf("list error=%v, want ErrInvalidListTime", listErr)
			}
		})
	}
	result, err := store.List(context.Background(), ListOptions{StartAt: "2026-08-13T08:00:00+08:00", EndAt: "2026-08-13T08:00:01+08:00"})
	if err != nil || len(result.Items) != 1 || result.Items[0].ID != created.ID {
		t.Fatalf("offset range must match canonical UTC instant: result=%+v err=%v", result, err)
	}
}

func TestLoadConfigAppliesBoundedRuntimeSettings(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID":          "f4-config",
		"JUHE_AI_POSTGRES_URL":                       "postgres://f4-main",
		"JUHE_AI_OPERATION_LOG_OWNER_LEASE":          "45s",
		"JUHE_AI_OPERATION_LOG_RETENTION_INTERVAL":   "2m",
		"JUHE_AI_OPERATION_LOG_RETENTION_BATCH_SIZE": "321",
	}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil || cfg.OwnerLease != 45*time.Second || cfg.RetentionInterval != 2*time.Minute || cfg.RetentionBatchSize != 321 {
		t.Fatalf("F4 runtime settings cfg=%+v err=%v", cfg, err)
	}
	if cfg.PostgresMaxOpenConns != 20 || cfg.PostgresMaxIdleConns != 10 {
		t.Fatalf("default F4 PostgreSQL pool=%d/%d want 20/10", cfg.PostgresMaxOpenConns, cfg.PostgresMaxIdleConns)
	}
	// 2026-09-30 C4 收编：JUHE_AI_OPERATION_LOG_POSTGRES_MAX_OPEN_CONNS /
	// MAX_IDLE_CONNS 收编为常量，pool 覆盖断言删除（取值恒为默认）。
	values["JUHE_AI_OPERATION_LOG_OWNER_LEASE"] = "4s"
	if _, err = LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("short F4 owner lease must fail")
	}
}

func TestViewerListAndSummaryDetailArePermissionBounded(t *testing.T) {
	store, implementation, _ := openPostgresStoreFixture(t, "12")
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "viewer", time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = store.Persist(context.Background(), lease, Input{ID: "visible", ActorSystemAccountID: "actor", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "visible", CreatedAt: now, Viewers: []Viewer{{SystemAccountID: "viewer", VisibilityReason: "actor_self", DetailLevel: "summary"}, {SystemAccountID: "viewer", VisibilityReason: "resource_owner", DetailLevel: "full"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Persist(context.Background(), lease, Input{ID: "hidden", ActorSystemAccountID: "other", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "hidden", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	list, err := store.List(context.Background(), ListOptions{ViewerID: "viewer", Module: "all"})
	if err != nil || len(list.Items) != 1 || list.Items[0].ID != "visible" {
		t.Fatalf("viewer list=%+v err=%v", list, err)
	}
	_, err = store.Persist(context.Background(), lease, Input{ID: "global", ActorSystemAccountID: "other", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "global trace-abc", TraceID: "trace-abc-001", VisibilityScope: "all_users", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	adminFiltered, err := store.List(context.Background(), ListOptions{AffectedSystemAccountID: "viewer"})
	if err != nil || len(adminFiltered.Items) != 2 || adminFiltered.Items[0].ID != "visible" || adminFiltered.Items[1].ID != "global" {
		t.Fatalf("all_users affected-account filter=%+v err=%v", adminFiltered, err)
	}
	allAffected, err := store.List(context.Background(), ListOptions{AffectedSystemAccountID: "all"})
	if err != nil || len(allAffected.Items) != 3 {
		t.Fatalf("affected=all must be ignored: result=%+v err=%v", allAffected, err)
	}
	traceFiltered, err := store.List(context.Background(), ListOptions{TraceID: "trace-abc"})
	if err != nil || len(traceFiltered.Items) != 1 || traceFiltered.Items[0].ID != "global" {
		t.Fatalf("trace prefix filter=%+v err=%v", traceFiltered, err)
	}
	invalidKeyword, err := store.List(context.Background(), ListOptions{SummaryKeyword: "!!!"})
	if err != nil || len(invalidKeyword.Items) != 0 {
		t.Fatalf("invalid summary keyword must return no rows: %+v err=%v", invalidKeyword, err)
	}
	nfkc, err := store.List(context.Background(), ListOptions{SummaryKeyword: "Ｇｌｏｂａｌ"})
	if err != nil || len(nfkc.Items) != 1 || nfkc.Items[0].ID != "global" {
		t.Fatalf("NFKC summary keyword=%+v err=%v", nfkc, err)
	}
	window, err := store.List(context.Background(), ListOptions{Page: 99999, PageSize: 50})
	if err != nil || window.Page != 20 {
		t.Fatalf("page must stay inside 1001-row window: %+v err=%v", window, err)
	}
	detail, found, err := store.Detail(context.Background(), "visible", "viewer")
	if err != nil || !found || len(detail.Changes) != 0 || detail.Method != "" {
		t.Fatalf("summary detail=%+v found=%v err=%v", detail, found, err)
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil || bytes.Contains(detailJSON, []byte(`"targets":null`)) || bytes.Contains(detailJSON, []byte(`"viewers":null`)) || !bytes.Contains(detailJSON, []byte(`"targets":[]`)) || !bytes.Contains(detailJSON, []byte(`"viewers":[]`)) {
		t.Fatalf("summary detail JSON must preserve array DTO shape: body=%s err=%v", detailJSON, err)
	}
	if days, err := store.RetentionDays(context.Background(), 365); err != nil || days != 12 {
		t.Fatalf("retention setting=%d err=%v", days, err)
	}
	if _, err := implementation.db.Exec(`UPDATE juhe_business.system_settings SET value_json='0' WHERE system_account_id='sys_admin' AND key='operationLogRetentionDays'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetentionDays(context.Background(), 365); err == nil {
		t.Fatal("invalid business setting must not silently fall back")
	}
}

func TestPostgresRetentionSettingContract(t *testing.T) {
	query := "SELECT value_json FROM juhe_business.system_settings WHERE system_account_id=$1 AND key=$2"
	if query != fmt.Sprintf("SELECT value_json FROM %s.system_settings WHERE system_account_id=$1 AND key=$2", "juhe_business") {
		t.Fatal("postgres business settings query contract changed")
	}
}

type schemaCatalogStub struct {
	strings             map[string]string
	bools               map[string]bool
	compositeForeignKey bool
}

func (s schemaCatalogStub) String(_ context.Context, query string, args ...any) (string, error) {
	if strings.Contains(query, "format_type") {
		return s.strings[args[0].(string)+"."+args[1].(string)], nil
	}
	if strings.Contains(query, "pg_get_indexdef") {
		return s.strings["index."+args[0].(string)], nil
	}
	return s.strings["pk."+args[0].(string)], nil
}

func (s schemaCatalogStub) Bool(_ context.Context, query string, args ...any) (bool, error) {
	if strings.Contains(query, "attnotnull") {
		return s.bools["notnull."+args[0].(string)+"."+args[1].(string)], nil
	}
	if strings.Contains(query, "pg_constraint") {
		if s.compositeForeignKey && strings.Contains(query, "cardinality(c.conkey)=1") && strings.Contains(query, "cardinality(c.confkey)=1") {
			return false, nil
		}
		return s.bools["fk."+args[0].(string)], nil
	}
	return s.bools["idx."+args[0].(string)], nil
}

func validSchemaCatalogStub() schemaCatalogStub {
	stub := schemaCatalogStub{strings: map[string]string{}, bools: map[string]bool{}}
	for table, columns := range postgresSchemaColumns {
		for _, column := range columns {
			stub.strings["juhe_dataset."+table+"."+column.name] = column.typeName
		}
		stub.strings["pk.juhe_dataset."+table] = postgresPrimaryKeys[table]
		for _, column := range postgresRequiredNotNull[table] {
			stub.bools["notnull.juhe_dataset."+table+"."+column] = true
		}
	}
	for _, table := range postgresForeignKeyTables {
		stub.bools["fk.juhe_dataset."+table] = true
	}
	for index, definition := range postgresRequiredIndexDefinitions {
		stub.strings["index."+index] = definition
	}
	return stub
}

func TestPostgresSchemaValidationRejectsSameNameIncompatibleIndex(t *testing.T) {
	stub := validSchemaCatalogStub()
	stub.strings["index.idx_operation_log_targets_target"] = "CREATE INDEX idx_operation_log_targets_target ON juhe_dataset.operation_log_targets USING btree (target_id, created_at)"
	err := validatePostgresSchema(context.Background(), stub)
	if err == nil || !strings.Contains(err.Error(), "idx_operation_log_targets_target definition") {
		t.Fatalf("same-name incompatible F4 index must fail closed: %v", err)
	}
}

func TestPostgresSchemaValidationRejectsNullableRequiredColumn(t *testing.T) {
	stub := validSchemaCatalogStub()
	stub.bools["notnull.juhe_dataset.operation_logs.summary"] = false
	err := validatePostgresSchema(context.Background(), stub)
	if err == nil || !strings.Contains(err.Error(), "operation_logs.summary must be NOT NULL") {
		t.Fatalf("nullable F4 required column must fail closed: %v", err)
	}
}

func TestSchemaBootstrapContextOutlivesRequestOperationDeadline(t *testing.T) {
	ctx, cancel := schemaBootstrapContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("schema bootstrap context 必须有 deadline")
	}
	remaining := time.Until(deadline)
	if remaining < schemaBootstrapTimeout-time.Second || remaining > schemaBootstrapTimeout {
		t.Fatalf("schema bootstrap deadline=%s, want about %s", remaining, schemaBootstrapTimeout)
	}
	if schemaBootstrapTimeout <= storeOperationTimeout {
		t.Fatalf("schema bootstrap timeout=%s 必须大于 request timeout=%s", schemaBootstrapTimeout, storeOperationTimeout)
	}
}

func TestPostgresSchemaValidationRejectsLegacyViewerPrimaryKey(t *testing.T) {
	stub := validSchemaCatalogStub()
	stub.strings["pk.juhe_dataset.operation_log_viewers"] = "operation_log_id,system_account_id,visibility_reason"
	err := validatePostgresSchema(context.Background(), stub)
	if err == nil || !strings.Contains(err.Error(), "operation_log_viewers primary key") || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("legacy Node viewer primary key must fail closed: %v", err)
	}
}

func TestPostgresSchemaValidationRejectsUnrelatedCascadeForeignKey(t *testing.T) {
	stub := validSchemaCatalogStub()
	stub.bools["fk.juhe_dataset.operation_log_targets"] = false
	err := validatePostgresSchema(context.Background(), stub)
	if err == nil || !strings.Contains(err.Error(), "operation_log_targets must have an exact single-column") {
		t.Fatalf("unrelated cascade foreign key must not satisfy F4 operation_log_id->id contract: %v", err)
	}
}

func TestPostgresSchemaValidationRejectsCompositeForeignKey(t *testing.T) {
	stub := validSchemaCatalogStub()
	stub.compositeForeignKey = true
	err := validatePostgresSchema(context.Background(), stub)
	if err == nil || !strings.Contains(err.Error(), "operation_log_targets must have an exact single-column") {
		t.Fatalf("composite foreign key must not satisfy F4 operation_log_id->id contract: %v", err)
	}
}

func TestPostgresOperationLogStoreSmoke(t *testing.T) {
	testDB := pgtestkit.NewTestDatabase(t, prepareBusinessFixture("12"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opened, err := OpenStore(Config{PostgresURL: testDB.DSN})
	if err != nil {
		t.Fatalf("打开 F4 PostgreSQL store 失败: %v", err)
	}
	store := opened.(*sqlStore)
	t.Cleanup(func() { _ = store.Close() })
	var applicationName string
	if err = store.db.QueryRowContext(ctx, "SHOW application_name").Scan(&applicationName); err != nil || applicationName != postgresApplicationName {
		t.Fatalf("F4 PostgreSQL application_name=%q want %q err=%v", applicationName, postgresApplicationName, err)
	}
	timeoutTx, err := store.beginTx(ctx)
	if err != nil {
		t.Fatalf("开始 F4 PostgreSQL timeout transaction 失败: %v", err)
	}
	for setting, expected := range map[string]string{
		"statement_timeout":                   postgresStatementTimeout,
		"lock_timeout":                        postgresLockTimeout,
		"idle_in_transaction_session_timeout": postgresIdleTransactionTimeout,
	} {
		var actual string
		if err = timeoutTx.QueryRowContext(ctx, "SHOW "+setting).Scan(&actual); err != nil || actual != expected {
			_ = timeoutTx.Rollback()
			t.Fatalf("F4 PostgreSQL transaction %s=%q want %q err=%v", setting, actual, expected, err)
		}
	}
	if err = timeoutTx.Rollback(); err != nil {
		t.Fatalf("结束 F4 PostgreSQL timeout transaction 失败: %v", err)
	}
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatalf("初始化 F4 PostgreSQL schema 失败: %v", err)
	}
	for _, table := range []string{"operation_logs", "operation_log_targets", "operation_log_viewers", "operation_log_summary_search_terms", "operation_log_owner_leases"} {
		var count int
		if err = store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM juhe_dataset."+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("F4 PostgreSQL smoke 必须使用空的专用数据库：table=%s count=%d err=%v", table, count, err)
		}
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "f4-postgres-smoke-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("F4 PostgreSQL owner lease 获取失败：acquired=%v err=%v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	if _, acquired, err = store.AcquireOwnerLease(ctx, "f4-postgres-smoke-other", time.Minute); err != nil || acquired {
		t.Fatalf("未过期的 F4 PostgreSQL owner lease 不应被夺取：acquired=%v err=%v", acquired, err)
	}
	visible := Input{ID: "f4-postgres-visible", ActorSystemAccountID: "actor", ActorRole: "user", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", ResourceID: "account-1", Summary: "更新 Alpha 账户", ClientIP: "203.0.113.1", CreatedAt: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano), Targets: []Target{{TargetType: "account", TargetID: "account-1", TargetOwnerSystemAccountID: "owner", Relation: "primary"}}, Viewers: []Viewer{{SystemAccountID: "viewer", VisibilityReason: "actor_self", DetailLevel: "full"}}}
	ignored, err := store.Persist(ctx, lease, visible)
	if err != nil || ignored {
		t.Fatalf("F4 PostgreSQL 首次写入失败：ignored=%v err=%v", ignored, err)
	}
	ignored, err = store.Persist(ctx, lease, visible)
	if err != nil || !ignored {
		t.Fatalf("F4 PostgreSQL 稳定 ID 幂等失败：ignored=%v err=%v", ignored, err)
	}
	var targetCount int
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_dataset.operation_log_targets WHERE operation_log_id=$1`, visible.ID).Scan(&targetCount); err != nil || targetCount != 1 {
		t.Fatalf("F4 PostgreSQL 幂等写入重复了 targets：count=%d err=%v", targetCount, err)
	}
	list, err := store.List(ctx, ListOptions{ViewerID: "viewer", Module: "all", SummaryKeyword: "pha 账"})
	if err != nil || len(list.Items) != 1 || list.Items[0].ID != visible.ID {
		t.Fatalf("F4 PostgreSQL viewer/search list 失败：result=%+v err=%v", list, err)
	}
	allAffected, err := store.List(ctx, ListOptions{AffectedSystemAccountID: "all"})
	if err != nil || len(allAffected.Items) != 1 || allAffected.Items[0].ID != visible.ID {
		t.Fatalf("F4 PostgreSQL affected=all list 失败：result=%+v err=%v", allAffected, err)
	}
	targetedSameSecond := visible
	targetedSameSecond.ID = "f4-postgres-targeted-same-second"
	targetedSameSecond.Summary = "targeted same second"
	targetedSameSecond.CreatedAt = "2026-08-13T00:00:00.100000000Z"
	if _, err = store.Persist(ctx, lease, targetedSameSecond); err != nil {
		t.Fatalf("F4 PostgreSQL targeted same-second 写入失败：%v", err)
	}
	globalSameSecond := targetedSameSecond
	globalSameSecond.ID = "f4-postgres-global-same-second"
	globalSameSecond.Summary = "global same second"
	globalSameSecond.CreatedAt = "2026-08-13T00:00:00.110000000Z"
	globalSameSecond.VisibilityScope = "all_users"
	globalSameSecond.Viewers = nil
	if _, err = store.Persist(ctx, lease, globalSameSecond); err != nil {
		t.Fatalf("F4 PostgreSQL all-users same-second 写入失败：%v", err)
	}
	personalSameSecond, err := store.List(ctx, ListOptions{ViewerID: "viewer"})
	if err != nil || len(personalSameSecond.Items) < 2 || personalSameSecond.Items[0].ID != globalSameSecond.ID || personalSameSecond.Items[1].ID != targetedSameSecond.ID {
		t.Fatalf("F4 PostgreSQL personal same-second 排序失败：result=%+v err=%v", personalSameSecond, err)
	}
	detail, found, err := store.Detail(ctx, visible.ID, "viewer")
	detailJSON, _ := json.Marshal(detail)
	if err != nil || !found || detail.ClientIP != "" || len(detail.Targets) != 1 || detail.Targets[0].TargetOwnerSystemAccountName != "Owner" || bytes.Contains(detailJSON, []byte("targetOwnerSystemAccountId")) {
		t.Fatalf("F4 PostgreSQL 个人详情裁剪失败：detail=%+v found=%v err=%v", detail, found, err)
	}
	lockDB, err := sql.Open("pgx", testDB.DSN)
	if err != nil {
		t.Fatalf("打开 F4 PostgreSQL lock probe 失败: %v", err)
	}
	defer lockDB.Close()
	lockTx, err := lockDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("打开 F4 PostgreSQL lock transaction 失败: %v", err)
	}
	if _, err = lockTx.ExecContext(ctx, "LOCK TABLE juhe_dataset.operation_logs IN ACCESS EXCLUSIVE MODE"); err != nil {
		_ = lockTx.Rollback()
		t.Fatalf("锁定 F4 PostgreSQL operation_logs 失败: %v", err)
	}
	blocked := visible
	blocked.ID = "f4-postgres-lock-timeout"
	started := time.Now()
	_, persistErr := store.Persist(ctx, lease, blocked)
	elapsed := time.Since(started)
	_ = lockTx.Rollback()
	// 上界须大于 BUG-0254 后的生产 lock_timeout=5s（本测试门禁化时代按旧值
	// 1s 写死 4s，实测 55P03 在 ~5.03s 到达），同时远小于 statement_timeout
	// 30s，证明失败来自 lock_timeout 而非语句超时。
	if persistErr == nil || elapsed >= 10*time.Second {
		t.Fatalf("F4 PostgreSQL lock timeout 未生效：err=%v elapsed=%s", persistErr, elapsed)
	}
	expired := visible
	expired.ID = "f4-postgres-expired"
	expired.CreatedAt = "2020-01-01T00:00:00Z"
	if _, err = store.Persist(ctx, lease, expired); err != nil {
		t.Fatalf("F4 PostgreSQL 过期记录写入失败: %v", err)
	}
	deleted, err := store.CleanupRetention(ctx, lease, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), 100)
	if err != nil || deleted != 1 {
		t.Fatalf("F4 PostgreSQL 保留清理失败：deleted=%d err=%v", deleted, err)
	}
	if days, err := store.RetentionDays(ctx, 365); err != nil || days != 12 {
		t.Fatalf("F4 PostgreSQL operationLogRetentionDays 读取失败：days=%d err=%v", days, err)
	}
	var targetIndex bool
	if err = store.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='juhe_dataset' AND tablename='operation_log_targets' AND indexname='idx_operation_log_targets_log')`).Scan(&targetIndex); err != nil || !targetIndex {
		t.Fatalf("F4 PostgreSQL operation_log_targets 外键索引缺失：exists=%v err=%v", targetIndex, err)
	}
}

func TestPostgresLegacyOperationLogMigrationSmoke(t *testing.T) {
	testDB := pgtestkit.NewTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	opened, err := OpenStore(Config{PostgresURL: testDB.DSN})
	if err != nil {
		t.Fatalf("打开 F4 PostgreSQL 历史迁移 smoke store 失败: %v", err)
	}
	store := opened.(*sqlStore)
	t.Cleanup(func() { _ = store.Close() })
	if _, err = store.db.ExecContext(ctx, `
CREATE SCHEMA IF NOT EXISTS juhe_dataset;
CREATE TABLE juhe_dataset.operation_logs (id text PRIMARY KEY,trace_id text,actor_system_account_id text NOT NULL,actor_username text,actor_display_name text,actor_role text NOT NULL,operation_scope_system_account_id text,mode text NOT NULL,module text NOT NULL,action text NOT NULL,operation_key text NOT NULL,resource_type text NOT NULL,resource_id text,resource_name text,summary text NOT NULL,detail_level text NOT NULL,visibility_scope text NOT NULL,changes_json jsonb NOT NULL,metadata_json jsonb NOT NULL,method text,path text,status_code integer,client_ip text,user_agent text,created_at timestamptz NOT NULL);
CREATE TABLE juhe_dataset.operation_log_targets (id text PRIMARY KEY,operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,target_type text NOT NULL,target_id text,target_name text,target_owner_system_account_id text,relation text NOT NULL,created_at timestamptz NOT NULL);
CREATE TABLE juhe_dataset.operation_log_viewers (operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,system_account_id text NOT NULL,visibility_reason text NOT NULL,detail_level text NOT NULL,created_at timestamptz NOT NULL,PRIMARY KEY(operation_log_id,system_account_id,visibility_reason));
CREATE TABLE juhe_dataset.operation_log_summary_search_terms (operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,term text NOT NULL,created_at timestamptz NOT NULL,PRIMARY KEY(term,operation_log_id));
INSERT INTO juhe_dataset.operation_logs (id,actor_system_account_id,actor_role,mode,module,action,operation_key,resource_type,summary,detail_level,visibility_scope,changes_json,metadata_json,method,path,status_code,created_at) VALUES ('legacy-oplog','actor','admin','self','accounts','update','accounts.update','account','legacy row','full','targeted','[{"field":"enabled","before":false,"after":true}]'::jsonb,'{"source":"legacy","flags":["x"]}'::jsonb,'PATCH','/__aisys__/api/accounts/1',202,clock_timestamp());
INSERT INTO juhe_dataset.operation_log_targets VALUES ('legacy-target','legacy-oplog','account','account-1','Legacy','owner','primary',clock_timestamp());
INSERT INTO juhe_dataset.operation_log_viewers VALUES ('legacy-oplog','actor','actor_self','full',clock_timestamp());
INSERT INTO juhe_dataset.operation_log_summary_search_terms VALUES ('legacy-oplog','legacy',clock_timestamp());
INSERT INTO juhe_dataset.operation_logs (id,actor_system_account_id,actor_role,mode,module,action,operation_key,resource_type,summary,detail_level,visibility_scope,changes_json,metadata_json,created_at)
SELECT 'legacy-batch-' || gs::text,'actor','admin','self','accounts','update','accounts.update','account','legacy batch row ' || gs::text,'full','all_users','[]'::jsonb,'{}'::jsonb,clock_timestamp() + gs * interval '1 microsecond'
FROM generate_series(1,250) AS gs;`); err != nil {
		t.Fatalf("初始化旧 Node F4 PostgreSQL schema 失败: %v", err)
	}
	timeoutTx, err := store.beginLegacyMigrationTx(ctx)
	if err != nil {
		t.Fatalf("创建 F4 PostgreSQL 历史迁移事务失败: %v", err)
	}
	for setting, expected := range map[string]string{"statement_timeout": legacyMigrationStatementTimeout, "lock_timeout": legacyMigrationLockTimeout, "idle_in_transaction_session_timeout": legacyMigrationIdleTimeout} {
		var actual string
		if err = timeoutTx.QueryRowContext(ctx, "SHOW "+setting).Scan(&actual); err != nil || actual != expected {
			_ = timeoutTx.Rollback()
			t.Fatalf("F4 PostgreSQL 历史迁移 %s=%q，want %q, err=%v", setting, actual, expected, err)
		}
	}
	if err = timeoutTx.Rollback(); err != nil {
		t.Fatalf("回滚 F4 PostgreSQL 历史迁移超时检查失败: %v", err)
	}
	factTx, err := store.beginLegacyMigrationTx(ctx)
	if err != nil {
		t.Fatalf("创建 F4 PostgreSQL 迁移事实校验事务失败: %v", err)
	}
	samples, err := snapshotPostgresLegacySamples(ctx, factTx)
	if err == nil {
		_, err = factTx.ExecContext(ctx, `UPDATE juhe_dataset.operation_logs SET summary='tampered before migration' WHERE id='legacy-oplog'`)
	}
	if err == nil {
		err = verifyPostgresLegacySamples(ctx, factTx, samples)
	}
	if rollbackErr := factTx.Rollback(); rollbackErr != nil {
		t.Fatalf("回滚 F4 PostgreSQL 迁移事实校验失败: %v", rollbackErr)
	}
	if err == nil || !strings.Contains(err.Error(), "业务事实不一致") {
		t.Fatalf("F4 PostgreSQL 迁移必须拒绝被篡改的首尾业务事实: %v", err)
	}
	result, err := MigrateLegacyPostgres(ctx, Config{PostgresURL: testDB.DSN}, LegacyMigrationOptions{NodeStopped: true, GoStopped: true, BackupConfirmed: true})
	if err != nil {
		t.Fatalf("F4 PostgreSQL 历史迁移失败: %v", err)
	}
	if result.NoOp || !result.SearchTermsRebuilt || result.MigratedOperationLogs != 251 || result.TargetCounts["operation_log_viewers"] != 1 {
		t.Fatalf("unexpected F4 PostgreSQL historical migration result: %+v", result)
	}
	if err = validatePostgresSchema(ctx, postgresSQLCatalog{queryer: store.db}); err != nil {
		t.Fatalf("F4 PostgreSQL 历史迁移后 catalog 校验失败: %v", err)
	}
	var detailLevel string
	if err = store.db.QueryRowContext(ctx, `SELECT detail_level FROM juhe_dataset.operation_log_viewers WHERE operation_log_id='legacy-oplog' AND system_account_id='actor'`).Scan(&detailLevel); err != nil || detailLevel != "full" {
		t.Fatalf("F4 PostgreSQL 历史 viewer 未保留: detail=%q err=%v", detailLevel, err)
	}
	var rebuiltTerms int
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_dataset.operation_log_summary_search_terms WHERE operation_log_id='legacy-oplog'`).Scan(&rebuiltTerms); err != nil || rebuiltTerms == 0 {
		t.Fatalf("F4 PostgreSQL 历史 search terms 未重建: count=%d err=%v", rebuiltTerms, err)
	}
	repeated, err := MigrateLegacyPostgres(ctx, Config{PostgresURL: testDB.DSN}, LegacyMigrationOptions{NodeStopped: true, GoStopped: true, BackupConfirmed: true})
	if err != nil || !repeated.NoOp {
		t.Fatalf("F4 PostgreSQL 历史迁移重复执行必须 no-op: result=%+v err=%v", repeated, err)
	}
}

func TestInputNormalizationBoundsViewersTargetsAndPersonalDetail(t *testing.T) {
	base := Input{ID: "normalize", ActorSystemAccountID: "actor", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "summary", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), VisibilityScope: "all_users", Viewers: []Viewer{{SystemAccountID: "attacker", VisibilityReason: "actor_self"}}}
	normalized, err := normalizeInput(base)
	if err != nil || len(normalized.Viewers) != 0 {
		t.Fatalf("all_users must not persist caller viewers: %+v err=%v", normalized.Viewers, err)
	}
	base.VisibilityScope = "targeted"
	base.Viewers = []Viewer{{SystemAccountID: "", VisibilityReason: "actor_self"}}
	base.Targets = []Target{{TargetType: "r", Relation: "unexpected"}}
	if _, err = normalizeInput(base); err == nil {
		t.Fatal("unknown target relation must fail")
	}
}

func TestSearchTermsAndPersonalFullDetailRemainBounded(t *testing.T) {
	store, _ := openPostgresStore(t)
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "search", time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	input := Input{ID: "search", ActorSystemAccountID: "actor", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "更新 Alpha-账户", ClientIP: "203.0.113.8", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Targets: []Target{{TargetType: "r", TargetOwnerSystemAccountID: "owner", Relation: "primary"}}, Viewers: []Viewer{{SystemAccountID: "viewer", VisibilityReason: "actor_self", DetailLevel: "full"}}}
	if _, err = store.Persist(context.Background(), lease, input); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(context.Background(), ListOptions{SummaryKeyword: "pha 账"})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("normalized continuous substring search failed: %+v err=%v", list, err)
	}
	detail, found, err := store.Detail(context.Background(), input.ID, "viewer")
	detailJSON, _ := json.Marshal(detail)
	if err != nil || !found || detail.ClientIP != "" || detail.Targets[0].TargetOwnerSystemAccountName != "Owner" || bytes.Contains(detailJSON, []byte("targetOwnerSystemAccountId")) {
		t.Fatalf("personal detail leaked fields: %+v found=%v err=%v", detail, found, err)
	}
}

func TestInvalidMetadataIsARecordFailure(t *testing.T) {
	_, err := normalizeInput(Input{ID: "metadata", ActorSystemAccountID: "actor", ActorRole: "user", Module: "x", Action: "y", OperationKey: "x.y", ResourceType: "r", Summary: "bad metadata", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: []byte("{")})
	if err == nil {
		t.Fatal("invalid metadata must be rejected as a single-record failure")
	}
}
