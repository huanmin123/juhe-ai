package operationlog

// w9b 第二轮缺口：schema 校验错误臂、personal 双流裁剪、时间戳矩阵、
// normalizeInput 联动校验与 renewLoop 瞬时错误。

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"
)

// w9bGateCatalog 是 validatePostgresSchema 的可注入错误 catalog：默认按生产
// 期望值动态应答，注入错误字段后对应分支失败。
type w9bGateCatalog struct {
	colErr     error
	colValue   string // 非空时所有列类型都返回该值（type mismatch 臂）
	pkErr      error
	pkValue    string // 非空时所有主键都返回该值（mismatch 臂）
	notNullErr error
	notNull    bool
	fkErr      error
	fkPresent  bool
	idxErr     error
	idxValue   string
}

func (c w9bGateCatalog) String(_ context.Context, query string, args ...any) (string, error) {
	switch {
	case strings.Contains(query, "format_type"):
		if c.colErr != nil {
			return "", c.colErr
		}
		if c.colValue != "" {
			return c.colValue, nil
		}
		table, _ := args[0].(string)
		column, _ := args[1].(string)
		for _, def := range postgresSchemaColumns[strings.TrimPrefix(table, "juhe_dataset.")] {
			if def.name == column {
				return def.typeName, nil
			}
		}
		return "", nil
	case strings.Contains(query, "string_agg"):
		if c.pkErr != nil {
			return "", c.pkErr
		}
		if c.pkValue != "" {
			return c.pkValue, nil
		}
		table, _ := args[0].(string)
		return postgresPrimaryKeys[strings.TrimPrefix(table, "juhe_dataset.")], nil
	case strings.Contains(query, "pg_get_indexdef"):
		if c.idxErr != nil {
			return "", c.idxErr
		}
		if c.idxValue != "" {
			return c.idxValue, nil
		}
		index, _ := args[0].(string)
		return postgresRequiredIndexDefinitions[index], nil
	}
	return "", nil
}

func (c w9bGateCatalog) Bool(_ context.Context, query string, _ ...any) (bool, error) {
	if strings.Contains(query, "attnotnull") {
		if c.notNullErr != nil {
			return false, c.notNullErr
		}
		return c.notNull, nil
	}
	if c.fkErr != nil {
		return false, c.fkErr
	}
	return c.fkPresent, nil
}

func w9bValidGateCatalog() w9bGateCatalog {
	return w9bGateCatalog{notNull: true, fkPresent: true}
}

func TestW9BValidatePostgresSchemaErrorArms(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		mutate   func(c *w9bGateCatalog)
		fragment string
	}{
		{"column read error", func(c *w9bGateCatalog) { c.colErr = w9bErrBoom }, "is missing"},
		{"column type mismatch", func(c *w9bGateCatalog) { c.colValue = "integer" }, "type=integer"},
		{"primary key read error", func(c *w9bGateCatalog) { c.pkErr = w9bErrBoom }, "read primary key"},
		{"primary key mismatch", func(c *w9bGateCatalog) { c.pkValue = "weird" }, "primary key="},
		{"not null read error", func(c *w9bGateCatalog) { c.notNullErr = w9bErrBoom }, "read nullability"},
		{"not null violation", func(c *w9bGateCatalog) { c.notNull = false }, "must be NOT NULL"},
		{"foreign key read error", func(c *w9bGateCatalog) { c.fkErr = w9bErrBoom }, "read foreign key"},
		{"foreign key missing", func(c *w9bGateCatalog) { c.fkPresent = false }, "cascade foreign key"},
		{"index read error", func(c *w9bGateCatalog) { c.idxErr = w9bErrBoom }, "read index"},
		{"index invalid", func(c *w9bGateCatalog) { c.idxValue = "CREATE INDEX broken" }, "definition is missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			catalog := w9bValidGateCatalog()
			tc.mutate(&catalog)
			if err := validatePostgresSchema(ctx, catalog); err == nil || !strings.Contains(err.Error(), tc.fragment) {
				t.Fatalf("%s 必须失败：%v", tc.name, err)
			}
		})
	}
	// 全部合法 → 通过。
	if err := validatePostgresSchema(ctx, w9bValidGateCatalog()); err != nil {
		t.Fatalf("合法 catalog 必须通过：%v", err)
	}
}

func TestW9BListPersonalWindowTrimming(t *testing.T) {
	ctx := context.Background()
	row := func(id, createdAt string) []driver.Value {
		return []driver.Value{id, "", "actor", "Actor", "", "m", "a", "summary " + id, createdAt}
	}
	// 双流各 2 行：size=1、page=1 时触发裁剪与 hasMore。
	script := &w9bScript{steps: []w9bStep{
		{matcher: []string{"visible JOIN operation_logs"}, cols: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9"}, rows: [][]driver.Value{row("t1", "2026-09-01T00:00:00.000000000Z"), row("t2", "2026-09-01T00:00:01.000000000Z")}},
		{matcher: []string{"FROM operation_logs ol WHERE"}, cols: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9"}, rows: [][]driver.Value{row("a1", "2026-09-01T00:00:00.000000000Z"), row("a2", "2026-09-02T00:00:00.000000000Z")}},
		{matcher: []string{"juhe_business.system_accounts"}, noRows: true},
	}}
	store := &sqlStore{db: w9bOpen(t, script)}
	result, err := store.List(ctx, ListOptions{ViewerID: "viewer", Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("personal 裁剪=%v", err)
	}
	if len(result.Items) != 1 || !result.HasMore || result.Items[0].ID != "a2" {
		t.Fatalf("personal 排序裁剪=%+v", result)
	}
	// start 超出合并结果 → 空页。
	script2 := &w9bScript{steps: []w9bStep{
		{matcher: []string{"visible JOIN operation_logs"}, cols: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9"}, rows: [][]driver.Value{row("t1", "2026-09-01T00:00:00.000000000Z")}},
		{matcher: []string{"FROM operation_logs ol WHERE"}, cols: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9"}, rows: nil},
		{matcher: []string{"juhe_business.system_accounts"}, noRows: true},
	}}
	result2, err := (&sqlStore{db: w9bOpen(t, script2)}).List(ctx, ListOptions{ViewerID: "viewer", Page: 5, PageSize: 1})
	if err != nil || len(result2.Items) != 0 {
		t.Fatalf("越界页=%+v err=%v", result2, err)
	}
}

func TestW9BStorageTimestampByteArm(t *testing.T) {
	var timestamp storageTimestamp
	if err := timestamp.Scan([]byte("nope")); err == nil {
		t.Fatal("非法 []byte 必须报错")
	}
}

func TestW9BNormalizeInputChildValidationArms(t *testing.T) {
	base := Input{ID: "id", ActorSystemAccountID: "a", ActorRole: "admin", Mode: "self", Module: "m", Action: "act", OperationKey: "m.act", ResourceType: "r", Summary: "s", DetailLevel: "full", VisibilityScope: "targeted", CreatedAt: "2026-09-10T08:00:00Z", Targets: []Target{{TargetType: "account", Relation: "primary"}}, Viewers: []Viewer{{SystemAccountID: "v", VisibilityReason: "actor_self", DetailLevel: "full"}}}
	// 非法 viewer reason。
	badViewer := base
	badViewer.Viewers = []Viewer{{SystemAccountID: "v", VisibilityReason: "weird", DetailLevel: "full"}}
	if _, err := normalizeInput(badViewer); err == nil || !strings.Contains(err.Error(), "viewer is invalid") {
		t.Fatalf("非法 viewer=%v", err)
	}
	// 非法 target relation。
	badRelation := base
	badRelation.Targets = []Target{{TargetType: "account", Relation: "weird"}}
	if _, err := normalizeInput(badRelation); err == nil || !strings.Contains(err.Error(), "relation is invalid") {
		t.Fatalf("非法 relation=%v", err)
	}
	// 缺 target type。
	badType := base
	badType.Targets = []Target{{TargetType: " ", Relation: "primary"}}
	if _, err := normalizeInput(badType); err == nil || !strings.Contains(err.Error(), "target type is required") {
		t.Fatalf("缺 target type=%v", err)
	}
	// viewer/detail 空值走默认 level。
	emptyLevel := base
	emptyLevel.Viewers = []Viewer{{SystemAccountID: "v", VisibilityReason: "actor_self"}}
	normalized, err := normalizeInput(emptyLevel)
	if err != nil {
		t.Fatalf("默认 level=%v", err)
	}
	for _, viewer := range normalized.Viewers {
		if viewer.DetailLevel != "full" {
			t.Fatalf("默认 viewer level=%+v", normalized.Viewers)
		}
	}
}

func TestW9BLeaseKeeperTransientRenewErrorKeepsLoop(t *testing.T) {
	// renew 瞬时失败只记录日志，keeper 保持存活（不进入 Lost）。
	store := &w9bRenewErrStore{}
	keeper, ok, err := StartLeaseKeeper(context.Background(), store, "w9b-transient", 60*time.Millisecond, nil)
	if err != nil || !ok {
		t.Fatalf("启动=%v err=%v", ok, err)
	}
	time.Sleep(80 * time.Millisecond)
	if keeper.LostError() != nil {
		t.Fatalf("瞬时续租失败不得进入终态：%v", keeper.LostError())
	}
	select {
	case <-keeper.Lost():
		t.Fatal("Lost 不应关闭")
	default:
	}
	keeper.Close()
}

type w9bRenewErrStore struct{ w9bAcquireOKStore }

func (s *w9bRenewErrStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	return false, w9bErrBoom
}

func TestW9BMigrateLegacyPostgresOpenStoreFailure(t *testing.T) {
	// PostgresURL 无法解析 → OpenStore 失败包装。
	_, err := MigrateLegacyPostgres(context.Background(), Config{Mode: ModePostgres, PostgresURL: "postgres://bad url with spaces"}, LegacyMigrationOptions{NodeStopped: true, GoStopped: true, BackupConfirmed: true})
	if err == nil || !strings.Contains(err.Error(), "打开 F4 PostgreSQL 目标失败") {
		t.Fatalf("坏 URL=%v", err)
	}
}

func TestW9BDetailViewersRowErrorArms(t *testing.T) {
	ctx := context.Background()
	// admin 视角 viewers 行迭代错误。
	viewersErr := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT ol.operation_key"}, cols: []string{"operation_key", "resource_type", "resource_id", "resource_name", "visibility_scope", "detail_level"}, rows: [][]driver.Value{{"key", "account", "acc", "Alpha", "targeted", "full"}}},
		{matcher: []string{"SELECT changes_json,COALESCE(method"}, cols: []string{"changes", "m", "p", "ip"}, rows: [][]driver.Value{{"[]", "PATCH", "/x", "1.2.3.4"}}},
		{matcher: []string{"SELECT id,target_type,COALESCE(target_id"}, noRows: true},
		{matcher: []string{"SELECT system_account_id,visibility_reason"}, cols: []string{"a"}, rows: [][]driver.Value{{"viewer"}}, eofErr: w9bErrBoom},
	}}
	if _, _, err := (&sqlStore{db: w9bOpen(t, viewersErr)}).Detail(ctx, "id", ""); err == nil {
		t.Fatal("viewers 行迭代错误必须透传")
	}
	// accountNames 查询失败。
	namesErr := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT ol.operation_key"}, cols: []string{"operation_key", "resource_type", "resource_id", "resource_name", "visibility_scope", "detail_level"}, rows: [][]driver.Value{{"key", "account", "acc", "Alpha", "targeted", "full"}}},
		{matcher: []string{"SELECT changes_json,COALESCE(method"}, cols: []string{"changes", "m", "p", "ip"}, rows: [][]driver.Value{{"[]", "PATCH", "/x", "1.2.3.4"}}},
		{matcher: []string{"SELECT id,target_type,COALESCE(target_id"}, noRows: true},
		{matcher: []string{"SELECT system_account_id,visibility_reason"}, cols: []string{"a", "b", "c"}, rows: [][]driver.Value{{"viewer", "actor_self", "full"}}},
		{matcher: []string{"SELECT id,COALESCE(NULLIF(display_name"}, rowsErr: w9bErrBoom},
	}}
	if _, _, err := (&sqlStore{db: w9bOpen(t, namesErr)}).Detail(ctx, "id", ""); err == nil || !strings.Contains(err.Error(), "read F4 system account names") {
		t.Fatalf("账户名查询失败=%v", err)
	}
}
