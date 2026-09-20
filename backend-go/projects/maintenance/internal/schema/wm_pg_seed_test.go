package schema

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"
)

// 本文件用录制客户端覆盖完整 seedPostgresDefaults（pg_seed.go）：语句流捕获、
// 确定性（固定时钟下语句序列稳定）、错误传播与 profile account_types 修复
// 分支。接口注入来自 pg_seed.go 的 postgresSeedClient 设计。

type wmSeedCaptureClient struct {
	db  *sql.DB
	rec *wmSchemaRecorder
}

func (c *wmSeedCaptureClient) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	c.rec.mu.Lock()
	if c.rec.failExecAfter > 0 && len(c.rec.execs)+1 > c.rec.failExecAfter {
		c.rec.mu.Unlock()
		return nil, errors.New("wm seed capture: 注入执行失败")
	}
	c.rec.execs = append(c.rec.execs, wmCapturedStatement{query: query, args: args})
	c.rec.mu.Unlock()
	// 不再委托底层连接，避免底层录制器二次计数；seed 只关心执行成功与否。
	return wmSeedRowsAffected{count: 3}, nil
}

// wmSeedRowsAffected 模拟 stale disable 的 RowsAffected 语义（Node changes）。
type wmSeedRowsAffected struct{ count int64 }

func (r wmSeedRowsAffected) LastInsertId() (int64, error) { return 0, errors.New("not supported") }
func (r wmSeedRowsAffected) RowsAffected() (int64, error) { return r.count, nil }

func (c *wmSeedCaptureClient) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	c.rec.mu.Lock()
	c.rec.queries = append(c.rec.queries, wmCapturedStatement{query: query, args: args})
	c.rec.mu.Unlock()
	return c.db.QueryRowContext(ctx, query, args...)
}

func TestWMSeedPostgresDefaultsProducesStableStatementStream(t *testing.T) {
	clock := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	run := func(t *testing.T) (PGSeedResult, []string, wmCapturedStatement) {
		t.Helper()
		rec := &wmSchemaRecorder{}
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		client := &wmSeedCaptureClient{db: db, rec: rec}
		result, err := seedPostgresDefaults(context.Background(), client, SeedOptions{Now: func() time.Time { return clock }, Secret: "wm-secret"})
		if err != nil {
			t.Fatalf("seedPostgresDefaults: %v", err)
		}
		first := wmCapturedStatement{}
		rec.mu.Lock()
		if len(rec.execs) > 0 {
			first = rec.execs[0]
		}
		rec.mu.Unlock()
		return result, rec.execQueries(), first
	}
	// 固定流程内先跑一次基准，再跑第二次对比语句序列（密码/密钥为随机值，
	// 因此仅对比语句文本序列与条数，不对比参数）。
	result, queries, first := run(t)
	// StatementCount 含模型目录 bulk upsert 与 stale disable 的 RowsAffected
	// 计数（Node statementCount += changes 语义），因此必须不小于捕获执行数。
	if result.StatementCount < len(queries) {
		t.Fatalf("StatementCount 不应小于捕获执行数: %d vs %d", result.StatementCount, len(queries))
	}
	secondResult, secondQueries, _ := run(t)
	if secondResult.StatementCount != result.StatementCount {
		t.Fatalf("固定时钟下两次 seed 语句数必须一致: %d vs %d", secondResult.StatementCount, result.StatementCount)
	}
	for index := range queries {
		if queries[index] != secondQueries[index] {
			t.Fatalf("固定时钟下语句序列必须确定: 第 %d 条不一致\n%s\n%s", index, queries[index], secondQueries[index])
		}
	}
	joined := strings.Join(queries, "\n")
	for _, fragment := range []string{
		`INSERT INTO "juhe_business"."system_accounts"`,
		"provider_model_catalog", // 模型目录 bulk upsert
	} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("完整 seed 缺少语句族 %s", fragment)
		}
	}
	// 首条语句是超管账户插入，密码哈希必须是 Node 兼容的 pbkdf2 信封。
	if !strings.Contains(first.query, `"juhe_business"."system_accounts"`) {
		t.Fatalf("首条 seed 语句应是超管插入: %q", first.query)
	}
	if len(first.args) != 11 {
		t.Fatalf("超管插入应为 11 参: %d", len(first.args))
	}
	passwordHash, _ := first.args[6].(string)
	if !strings.HasPrefix(passwordHash, "pbkdf2$sha512$120000$") {
		t.Fatalf("seed 管理员密码哈希格式错误: %q", passwordHash)
	}
	// 固定时钟时间戳参数必须落在语句里。
	if timestamp, _ := first.args[9].(string); timestamp != "2026-09-04T08:00:00.000Z" {
		t.Fatalf("seed 时间戳参数应来自注入时钟: %v", first.args[9])
	}
}

func TestWMSeedPostgresDefaultsProfileRepairMergesAccountTypes(t *testing.T) {
	clock := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	profile := pgSeedProfiles[0]

	t.Run("current superset skips update", func(t *testing.T) {
		rec := &wmSchemaRecorder{}
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		// 只脚本化第一个 profile 的修复查询：返回已包含内置类型的 JSON。
		rec.script("account_types_json", []string{"c0"}, [][]driver.Value{{seedStringify(profile.AccountTypes)}})
		client := &wmSeedCaptureClient{db: db, rec: rec}
		if _, err := seedPostgresDefaults(context.Background(), client, SeedOptions{Now: func() time.Time { return clock }}); err != nil {
			t.Fatalf("seedPostgresDefaults: %v", err)
		}
		for _, statement := range rec.execs {
			if strings.HasPrefix(strings.TrimSpace(statement.query), "UPDATE") && strings.Contains(statement.query, "account_types_json") {
				t.Fatalf("已有超集时不应产生修复 UPDATE: %q", statement.query)
			}
		}
	})

	t.Run("missing account type triggers update", func(t *testing.T) {
		rec := &wmSchemaRecorder{}
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		// 返回空数组：修复必须合并内置类型并下发 UPDATE。
		rec.script("account_types_json", []string{"c0"}, [][]driver.Value{{"[]"}})
		client := &wmSeedCaptureClient{db: db, rec: rec}
		if _, err := seedPostgresDefaults(context.Background(), client, SeedOptions{Now: func() time.Time { return clock }}); err != nil {
			t.Fatalf("seedPostgresDefaults: %v", err)
		}
		found := false
		for _, statement := range rec.execs {
			if strings.HasPrefix(strings.TrimSpace(statement.query), "UPDATE") && strings.Contains(statement.query, "account_types_json") {
				found = true
				if statement.args[0] != seedStringify(profile.AccountTypes) {
					t.Fatalf("修复 UPDATE 应写入合并后的内置类型: %v", statement.args[0])
				}
				if statement.args[2] != profile.ID {
					t.Fatalf("修复 UPDATE 应定位第一个 profile: %v", statement.args[2])
				}
			}
		}
		if !found {
			t.Fatal("缺少内置类型时必须产生修复 UPDATE")
		}
	})

	t.Run("malformed json skips repair", func(t *testing.T) {
		rec := &wmSchemaRecorder{}
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		rec.script("account_types_json", []string{"c0"}, [][]driver.Value{{"not-json"}})
		client := &wmSeedCaptureClient{db: db, rec: rec}
		result, err := seedPostgresDefaults(context.Background(), client, SeedOptions{Now: func() time.Time { return clock }})
		if err != nil {
			t.Fatalf("坏 JSON 应镜像 Node 的跳过语义而不是失败: %v", err)
		}
		if result.StatementCount <= 0 {
			t.Fatalf("语句计数必须为正: %d", result.StatementCount)
		}
	})
}

func TestWMSeedPostgresDefaultsPropagatesFailure(t *testing.T) {
	clock := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	rec := &wmSchemaRecorder{failExecAfter: 2}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	client := &wmSeedCaptureClient{db: db, rec: rec}
	_, err := seedPostgresDefaults(context.Background(), client, SeedOptions{Now: func() time.Time { return clock }})
	// 失败发生在第 3 次执行，此时已完成 2 条，序号 = 已完成数 + 1。
	if err == nil || !strings.Contains(err.Error(), "postgres seed statement 3") {
		t.Fatalf("失败必须带语句序号: %v", err)
	}
}

func TestWMSeedPostgresDefaultsAdminKeyExistenceBranches(t *testing.T) {
	clock := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	rec := &wmSchemaRecorder{}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	group := pgSeedGroups[0]
	// 第一个分组已存在：默认分组创建分支跳过，后续 route strategy/API key
	// 查询仍按 ErrNoRows 走“插入”路径。
	rec.script("FROM \"juhe_business\".\"groups\"", []string{"c0", "c1"}, [][]driver.Value{{group.ID, group.Name}})
	client := &wmSeedCaptureClient{db: db, rec: rec}
	result, err := seedPostgresDefaults(context.Background(), client, SeedOptions{Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatalf("seedPostgresDefaults（分组已存在分支）: %v", err)
	}
	if result.StatementCount <= 0 {
		t.Fatalf("语句计数必须为正: %d", result.StatementCount)
	}
}

func TestWMHashSeedPasswordIsNodeVerifiableEnvelope(t *testing.T) {
	hash, err := hashSeedPassword("admin")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 5 || parts[0] != "pbkdf2" || parts[1] != "sha512" || parts[2] != "120000" {
		t.Fatalf("哈希信封格式错误: %q", hash)
	}
	// 盐必须是 base64url 文本（Node 直接把盐文本喂给 pbkdf2Sync）。
	for _, r := range parts[3] + parts[4] {
		if strings.ContainsRune("+/", r) {
			t.Fatalf("盐/摘要必须使用 base64url 字母表: %q", hash)
		}
	}
}

// TestWMSeedPostgresDefaultsStripsCodexAutoReviewFromGPTDefaults 覆盖 PG 种子
// 的 codex-auto-review 清洗步（SQLite sqSeedGPTVendorCodexAutoReviewRemoval
// 的 PG 等价）：残留老库清单必须剔除该模型，其余守卫分支跳过修复。
func TestWMSeedPostgresDefaultsStripsCodexAutoReviewFromGPTDefaults(t *testing.T) {
	clock := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	// 清洗 UPDATE 与空列表修复 UPDATE（pgSeedProviderDefaultModelsRepair）的
	// SET 子句和前缀完全重叠，包含匹配会误报；fake 捕获的是 ExecContext 收到
	// 的原文，因此按清洗语句常量精确相等识别。
	isRemovalUpdate := func(statement wmCapturedStatement) bool {
		return statement.query == pgSeedGPTVendorCodexAutoReviewRemovalUpdate
	}
	findRemovalUpdate := func(rec *wmSchemaRecorder) (wmCapturedStatement, bool) {
		for _, statement := range rec.execs {
			if isRemovalUpdate(statement) {
				return statement, true
			}
		}
		return wmCapturedStatement{}, false
	}

	t.Run("residual model triggers update", func(t *testing.T) {
		rec := &wmSchemaRecorder{}
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		// 模拟老库残留：GPT 默认列表含已退役的 codex-auto-review。
		rec.script(`FROM "juhe_business"."providers"`, []string{"c0"}, [][]driver.Value{{`["codex-auto-review","gpt-5.5","gpt-5.4"]`}})
		client := &wmSeedCaptureClient{db: db, rec: rec}
		result, err := seedPostgresDefaults(context.Background(), client, SeedOptions{Now: func() time.Time { return clock }})
		if err != nil {
			t.Fatalf("seedPostgresDefaults: %v", err)
		}
		if result.StatementCount <= 0 {
			t.Fatalf("语句计数必须为正: %d", result.StatementCount)
		}
		statement, found := findRemovalUpdate(rec)
		if !found {
			t.Fatal("残留 codex-auto-review 时必须产生清洗 UPDATE")
		}
		if got, _ := statement.args[0].(string); got != `["gpt-5.5","gpt-5.4"]` {
			t.Fatalf("清洗 UPDATE 必须写入剔除后的列表: %q", got)
		}
		if got, _ := statement.args[1].(string); got != "2026-09-04T08:00:00.000Z" {
			t.Fatalf("清洗 UPDATE 必须使用注入时钟: %v", statement.args[1])
		}
		if got, _ := statement.args[2].(string); got != gptVendorCode {
			t.Fatalf("清洗 UPDATE 必须定位 GPT 供应商行: %v", statement.args[2])
		}
	})

	t.Run("clean list skips update", func(t *testing.T) {
		rec := &wmSchemaRecorder{}
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		rec.script(`FROM "juhe_business"."providers"`, []string{"c0"}, [][]driver.Value{{`["gpt-5.5","gpt-5.4"]`}})
		client := &wmSeedCaptureClient{db: db, rec: rec}
		if _, err := seedPostgresDefaults(context.Background(), client, SeedOptions{Now: func() time.Time { return clock }}); err != nil {
			t.Fatalf("seedPostgresDefaults: %v", err)
		}
		if _, found := findRemovalUpdate(rec); found {
			t.Fatal("清单不含 codex-auto-review 时不应改写（幂等，无 updated_at 抖动）")
		}
	})

	t.Run("malformed json skips update", func(t *testing.T) {
		rec := &wmSchemaRecorder{}
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		// json_valid=false 等价：非 JSON 文本跳过修复且不失败。
		rec.script(`FROM "juhe_business"."providers"`, []string{"c0"}, [][]driver.Value{{"{bad"}})
		client := &wmSeedCaptureClient{db: db, rec: rec}
		if _, err := seedPostgresDefaults(context.Background(), client, SeedOptions{Now: func() time.Time { return clock }}); err != nil {
			t.Fatalf("坏 JSON 应镜像 SQLite 守卫的跳过语义而不是失败: %v", err)
		}
		if _, found := findRemovalUpdate(rec); found {
			t.Fatal("非 JSON 老值不应被清洗改写")
		}
	})

	t.Run("non-array json skips update", func(t *testing.T) {
		rec := &wmSchemaRecorder{}
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		// json_type<>'array' 等价：JSON 字符串标量不是模型清单。
		rec.script(`FROM "juhe_business"."providers"`, []string{"c0"}, [][]driver.Value{{`"gpt-5.5"`}})
		client := &wmSeedCaptureClient{db: db, rec: rec}
		if _, err := seedPostgresDefaults(context.Background(), client, SeedOptions{Now: func() time.Time { return clock }}); err != nil {
			t.Fatalf("非数组 JSON 应镜像 SQLite 守卫的跳过语义而不是失败: %v", err)
		}
		if _, found := findRemovalUpdate(rec); found {
			t.Fatal("非数组老值不应被清洗改写")
		}
	})

	t.Run("missing provider row skips update", func(t *testing.T) {
		rec := &wmSchemaRecorder{}
		db := openWMSchemaFakeDB(rec)
		defer db.Close()
		// 不脚本化：默认零行（ErrNoRows）→ 新库无残留，无需清洗。
		client := &wmSeedCaptureClient{db: db, rec: rec}
		if _, err := seedPostgresDefaults(context.Background(), client, SeedOptions{Now: func() time.Time { return clock }}); err != nil {
			t.Fatalf("seedPostgresDefaults: %v", err)
		}
		if _, found := findRemovalUpdate(rec); found {
			t.Fatal("供应商行缺失时不应产生清洗 UPDATE")
		}
	})
}
