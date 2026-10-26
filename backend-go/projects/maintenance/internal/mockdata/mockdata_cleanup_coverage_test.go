package mockdata

// 清理与覆盖校验的 PG 集成测试：外键拓扑删除序、通用标识扫描、派生面重置，
// 以及 VerifyCoverage 的 Ready / NotCovered / OwnerStoresPending 语义。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// TestCleanupOrderPlacesChildrenBeforeParents 钉住外键拓扑的关键顺序（抽查
// FK 图中语义上必须先删的子表），并保证清单覆盖全部 maintenance schema。
func TestCleanupOrderPlacesChildrenBeforeParents(t *testing.T) {
	db := pgtestkit.NewTestDatabase(t, func(ctx context.Context, target *sql.DB) error {
		_, err := schema.EnsurePostgres(ctx, target)
		return err
	}).DB
	order, err := cleanupOrder(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	position := map[string]int{}
	for index, table := range order {
		if _, seen := position[table]; seen {
			t.Fatalf("清理顺序重复出现 %s", table)
		}
		position[table] = index
	}
	requireBefore := [][2]string{
		// 子表 → 父表（外键子表先删）。
		{qualified(SchemaBusiness, "announcement_reads"), qualified(SchemaBusiness, "announcements")},
		{qualified(SchemaBusiness, "account_supported_models"), qualified(SchemaBusiness, "models")},
		{qualified(SchemaBusiness, "account_endpoint_modes"), qualified(SchemaBusiness, "account_connections")},
		{qualified(SchemaBusiness, "api_keys"), qualified(SchemaBusiness, "route_strategies")},
		{qualified(SchemaUsage, "usage_record_shard_entries"), qualified(SchemaUsage, "usage_record_shards")},
		{qualified(SchemaChat, "chat_messages"), qualified(SchemaChat, "chat_conversations")},
	}
	for _, pair := range requireBefore {
		child, parent := pair[0], pair[1]
		childPos, childOK := position[child]
		parentPos, parentOK := position[parent]
		if !childOK || !parentOK {
			t.Fatalf("清理顺序缺表: child=%v parent=%v", childOK, parentOK)
		}
		if childPos >= parentPos {
			t.Fatalf("清理顺序错误: %s 必须先于 %s（%d >= %d）", child, parent, childPos, parentPos)
		}
	}
	for _, schema := range maintenanceSchemas {
		tables, err := schemaTables(context.Background(), db, schema)
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range tables {
			if _, ok := position[qualified(schema, table)]; !ok {
				t.Fatalf("清理顺序缺少 %s.%s", schema, table)
			}
		}
	}
}

// TestCleanupAllSweepsMarkersOnSeededDatabase 在真实造数库上验证清理：标记行
// 全部消失、seed 行保留、第二次清理为 no-op（幂等收敛）。
func TestCleanupAllSweepsMarkersOnSeededDatabase(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t).DSN
	runMockdataForTest(t, dsn, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC), "")

	ctx := context.Background()
	env, err := newEnv(Options{DSN: dsn, Days: 1, DailyRequests: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Close() }()

	count := func(query string) int {
		t.Helper()
		var n int
		if err := env.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// 清理前：造数行存在。
	if before := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "accounts")); before == 0 {
		t.Fatal("清理前 accounts 应非空")
	}
	deleted, skipped, err := cleanupAll(ctx, env, nil)
	if err != nil {
		t.Fatalf("cleanupAll: %v", err)
	}
	if deleted[qualified(SchemaBusiness, "accounts")] == 0 {
		t.Fatalf("清理未命中 accounts: %v", deleted)
	}
	// 造数行清空（父表与关键子表；groups / api_keys 保留 seed 行——默认分组
	// 与默认 Key 无造数标识，不属于清理范围，按标记列断言）。
	for _, table := range []string{"accounts", "announcements", "media_jobs", "system_teams"} {
		if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, table)); n != 0 {
			t.Fatalf("清理后 %s 仍有 %d 行", table, n)
		}
	}
	for _, table := range []string{"groups", "api_keys"} {
		if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, table) + " WHERE id LIKE '" + CleanupIDPrefix + "%'"); n != 0 {
			t.Fatalf("清理后 %s 仍有 %d 行造数样本", table, n)
		}
	}
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "api_keys") + " WHERE id LIKE '" + CleanupIDPrefix + "%'"); n != 0 {
		t.Fatalf("清理后 api_keys 仍有 %d 行造数样本", n)
	}
	// seed 行不受清理影响。
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "vendors")); n != 9 {
		t.Fatalf("清理动到了 seed vendors: %d", n)
	}
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "system_accounts") + " WHERE username = 'admin'"); n != 1 {
		t.Fatalf("清理动到了 seed admin: %d", n)
	}
	// 触发器写入的行（脏队列、查看者健康）必须随父行级联清空。
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "account_list_availability_dirty")); n != 0 {
		t.Fatalf("清理后 dirty 队列仍有 %d 行", n)
	}
	// 第二次清理：全部为 no-op。
	second, _, err := cleanupAll(ctx, env, nil)
	if err != nil {
		t.Fatalf("第二次 cleanupAll: %v", err)
	}
	for table, rows := range second {
		if rows != 0 {
			t.Fatalf("第二次清理仍删除 %s %d 行", table, rows)
		}
	}
	// 缺表场景：cleanupTable 把「表不存在」记入 skipped 而不是失败（降级库上
	// 清理仍是安全 no-op）；cleanupAll 的拓扑序由现存表推导，整表被 drop 的
	// 对象自然不参与清理。
	// 缺表对象选一个 ensure 不建的迁移遗留表（fresh 库真实不存在）。
	missingTable := qualified(SchemaChat, "chat_conversations_bind_legacy_backup")
	skippedMissing := []string{}
	if _, err := cleanupTable(ctx, env, missingTable, nil, map[string]int{}, &skippedMissing); err != nil {
		t.Fatalf("缺表 cleanupTable: %v", err)
	}
	if len(skippedMissing) != 1 || skippedMissing[0] != missingTable+"（表不存在）" {
		t.Fatalf("缺表未记录 skipped: %v", skippedMissing)
	}
	_ = skipped
	_ = deleted
}

// TestVerifyCoverageSemantics 验证覆盖校验的核心语义：business 接线后其断言
// 真实评估；未接线域进 NotCovered；属主缺失进 OwnerStoresPending 且不阻塞
// Ready；业务表被清空后 Ready=false（exit 3 的数据面）。
func TestVerifyCoverageSemantics(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t).DSN
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	summaryPath := coverageSummaryPath(t)
	runMockdataForTest(t, dsn, now, summaryPath)

	ctx := context.Background()
	report, err := VerifyCoverage(ctx, Options{
		DSN:           dsn,
		Days:          DefaultDays,
		DailyRequests: DefaultDailyRequests,
		SummaryFile:   summaryPath,
	})
	if err != nil {
		t.Fatalf("VerifyCoverage: %v", err)
	}
	// chat_codex_model_check 断言已评估（不在 NotCovered 里）；本测试只断言
	// 本域与跨域的门语义，其余域的接线状态由各自波次负责。
	assertionEvaluated := func(name string) bool {
		for _, item := range report.NotCovered {
			if len(item) >= len(name) && item[:len(name)] == name {
				return false
			}
		}
		return true
	}
	if !assertionEvaluated("accounts.status.active") {
		t.Fatalf("business 断言被标 NotCovered: %v", report.NotCovered)
	}
	if !assertionEvaluated("chat_conversations") {
		t.Fatalf("chat 断言被标 NotCovered: %v", report.NotCovered)
	}
	// 断言评估状态按「摘要接线快照 + 属主 pending」同源推导（跨波次稳定）：
	// 已接线域的非属主断言必须已评估；已接线域的属主断言在本库（无属主表）
	// 按属主缺失进 NotCovered；未接线域的断言按域未接线进 NotCovered。
	wiredSnapshot, snapshotErr := loadWiredDomainSnapshot(summaryPath)
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	wiredNames := map[string]bool{}
	for _, result := range wiredSnapshot {
		wiredNames[result.Name] = true
	}
	if len(wiredNames) == 0 {
		t.Fatal("摘要快照没有任何已接线域（Run 应写出 observability 接线）")
	}
	pendingStore := map[string]bool{}
	for _, pending := range report.OwnerStoresPending {
		pendingStore[pending.Store] = true
	}
	for _, assertion := range coverageAssertions() {
		wantEvaluated := wiredNames[assertion.Domain] &&
			(assertion.OwnerStore == "" || !pendingStore[assertion.OwnerStore])
		if assertionEvaluated(assertion.Name) != wantEvaluated {
			t.Fatalf("断言 %s 评估状态不符: evaluated=%v wantEvaluated=%v (NotCovered=%v)",
				assertion.Name, assertionEvaluated(assertion.Name), wantEvaluated, report.NotCovered)
		}
	}
	// observability 域抽查：maintenance 面断言已评估；属主断言按属主缺失进
	// NotCovered（本库未建属主表）。
	if !assertionEvaluated("public_api_logs") || !assertionEvaluated("public_api_logs.test_token") {
		t.Fatalf("observability maintenance 面断言应已评估: %v", report.NotCovered)
	}
	for _, name := range []string{"audit_logs", "runtime_log_facet_summary", "operation_log_targets"} {
		found := false
		for _, item := range report.NotCovered {
			if strings.Contains(item, "属主存储") && strings.HasPrefix(item, name) {
				found = true
			}
		}
		if !found {
			t.Fatalf("断言 %s 应因属主缺失进 NotCovered: %v", name, report.NotCovered)
		}
	}
	// 属主存储全部缺失且不阻塞 Ready；报告按 owner:<逻辑名> 记 Exists=false。
	// （属主缺失的 NotCovered 说明在「域已接线」时才出现——未接线域的断言
	// 先按域未接线归入 NotCovered，聚焦测试见 TestOwnerPendingNotCovered。）
	if len(report.OwnerStoresPending) != len(ownerStores) {
		t.Fatalf("OwnerStoresPending = %+v", report.OwnerStoresPending)
	}
	for _, pending := range report.OwnerStoresPending {
		found := false
		for _, store := range report.Schemas {
			if store.Name == "owner:"+pending.Store && !store.Exists {
				found = true
			}
		}
		if !found {
			t.Fatalf("属主 %s 缺失未出现在 Schemas: %+v", pending.Store, report.Schemas)
		}
	}
	// 覆盖是全量门：Empty 只允许未接线域的表——已接线域（按摘要快照）的任何
	// 空表都是缺陷；仍有未接线域时 Ready 必须为 false。
	allWired := true
	for _, entry := range domainRegistry {
		if !entry.Wired {
			allWired = false
		}
	}
	if !allWired && len(report.Empty) == 0 {
		t.Fatal("未接线域的空表应进 Empty")
	}
	for _, item := range report.Empty {
		schemaName := strings.SplitN(item, ".", 2)[0]
		if wiredNames[schemaDomain(schemaName)] {
			t.Fatalf("已接线域 %s 不应留空表: %v", schemaName, item)
		}
		if strings.HasPrefix(item, SchemaBusiness+".") || strings.HasPrefix(item, SchemaChat+".") ||
			strings.HasPrefix(item, SchemaCodexContext+".") || strings.HasPrefix(item, SchemaUsage+".") ||
			strings.HasPrefix(item, SchemaStats+".") || strings.HasPrefix(item, SchemaDataset+".") {
			if allWired {
				t.Fatalf("已产出数据的不应留空表: %v", report.Empty)
			}
		}
	}
	if len(report.Errors) != 0 {
		t.Fatalf("已接线域不应有 Errors: %v", report.Errors)
	}
	if report.Ready && !allWired {
		t.Fatalf("未接线域空表存在时覆盖不应 Ready: empty=%v", report.Empty)
	}

	// 清空一张 business 造数表后：Empty 命中 business 表、Ready 保持 false。
	env, err := newEnv(Options{DSN: dsn, Days: 1, DailyRequests: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Close() }()
	if _, err := env.db.ExecContext(ctx, "DELETE FROM "+qualified(SchemaBusiness, "announcements")); err != nil {
		t.Fatal(err)
	}
	report2, err := VerifyCoverage(ctx, Options{
		DSN:           dsn,
		Days:          DefaultDays,
		DailyRequests: DefaultDailyRequests,
		SummaryFile:   summaryPath,
	})
	if err != nil {
		t.Fatalf("第二次 VerifyCoverage: %v", err)
	}
	if report2.Ready {
		t.Fatal("清空 announcements 后覆盖不应 Ready")
	}
	foundEmpty := false
	for _, item := range report2.Empty {
		if item == qualified(SchemaBusiness, "announcements") {
			foundEmpty = true
		}
	}
	if !foundEmpty {
		t.Fatalf("Empty 缺 announcements: %v", report2.Empty)
	}

	// 摘要缺失（无隐式回退）：全部域未接线 → 全部断言 NotCovered。
	report3, err := VerifyCoverage(ctx, Options{DSN: dsn, Days: DefaultDays, DailyRequests: DefaultDailyRequests})
	if err != nil {
		t.Fatalf("无摘要 VerifyCoverage: %v", err)
	}
	if report3.Ready {
		t.Fatal("全部域未接线时不应 Ready")
	}
	for _, name := range []string{"accounts.status.active", "api_keys.mock_sample"} {
		found := false
		for _, item := range report3.NotCovered {
			if len(item) >= len(name) && item[:len(name)] == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("无摘要时断言 %s 应 NotCovered: %v", name, report3.NotCovered)
		}
	}
}

// TestVerifyCoverageOwnerStorePresent 验证属主存储存在时的语义：juhe_j3b 经
// 受控 bootstrap 后 model_check 属主从 pending 消失；coverage 报告的属主条目
// Exists=true。
func TestVerifyCoverageOwnerStorePresent(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		return ensureJ3BSchemaOnly(ctx, db)
	}).DSN
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	summaryPath := coverageSummaryPath(t)
	runMockdataForTest(t, dsn, now, summaryPath)

	report, err := VerifyCoverage(context.Background(), Options{
		DSN:           dsn,
		Days:          DefaultDays,
		DailyRequests: DefaultDailyRequests,
		SummaryFile:   summaryPath,
	})
	if err != nil {
		t.Fatalf("VerifyCoverage: %v", err)
	}
	for _, pending := range report.OwnerStoresPending {
		if pending.Store == "model_check" {
			t.Fatalf("juhe_j3b bootstrap 后 model_check 仍 pending: %+v", report.OwnerStoresPending)
		}
	}
	modelCheckPresent := false
	for _, store := range report.Schemas {
		if store.Name == "owner:model_check" && store.Exists {
			modelCheckPresent = true
		}
	}
	if !modelCheckPresent {
		t.Fatalf("owner:model_check 应出现在 Schemas 且 Exists=true: %+v", report.Schemas)
	}
}

// TestOwnerPendingNotCovered 聚焦钉住「域已接线 + 属主存储缺失」的组合：
// 该域的属主断言按「属主存储缺失」进 NotCovered 而不是失败。
func TestOwnerPendingNotCovered(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t).DSN
	env, err := newEnv(Options{DSN: dsn, Days: 1, DailyRequests: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Close() }()
	// 注入「observability 已接线」的记账（摘要快照路径的同源语义）。
	env.recordDomainResult(DomainResult{Name: DomainObservability, Counts: map[string]int{"rows": 1}})
	tx, err := env.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	pendingByStore := map[string]OwnerStorePending{
		"audit_log": {Store: "audit_log", Schema: SchemaDataset, MissingTables: []string{"audit_logs"}},
	}
	notCovered, failures, err := evaluateAssertions(context.Background(), tx, env, pendingByStore)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Fatalf("属主缺失不应产生失败: %v", failures)
	}
	ownerPendingReasons := 0
	for _, item := range notCovered {
		if strings.Contains(item, "属主存储 audit_log 缺失") {
			ownerPendingReasons++
		}
	}
	// audit_logs / audit_logs.media_endpoints / audit_error_groups /
	// audit_payload_blobs / audit_payload_refs 五条 audit_log 属主断言。
	if ownerPendingReasons != 5 {
		t.Fatalf("属主缺失说明数量 %d 不符: %v", ownerPendingReasons, notCovered)
	}
}
