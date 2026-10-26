package mockdata

// autofill 的 PG 集成测试（pgtestkit 每测试独立库）：用探针表钉住 autofill 的
// 核心契约——空表补 1..3 行结构合法占位、CHECK/jsonb/*_json/text[] 列合法、
// FK 父表缺失整表跳过、运行态跳过清单不被补行、重复 Run 不叠加、属主缺失时
// --verify-mockdata-coverage 仍 Ready。
// 真实 PG 由 pgtestkit 解析 .local/project-resources/dev/env/shared.env；连接
// 串内容不进入断言与日志。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// autofill 探针表名（挂在 juhe_stats，名字不得命中任何跳过清单，除 fk 父表
// 刻意用 _owner_leases 后缀验证跳过语义）。
const (
	autofillProbeTable       = "mockdata_autofill_probe"
	autofillProbeParentTable = "mockdata_autofill_parent_owner_leases"
	autofillProbeFKTable     = "mockdata_autofill_fk_probe"
)

// createAutofillProbeTables 在 EnsurePostgres 之后创建三张探针表：
//   - mockdata_autofill_probe：结构合法性的正面样例（bigserial 主键、CHECK IN、
//     jsonb、*_json 文本、text[]、timestamptz、numeric、可空 boolean）；
//   - mockdata_autofill_parent_owner_leases：命中 _owner_leases 跳过后缀的空
//     父表（autofill 不补行，制造「父表无可引用行」场景）；
//   - mockdata_autofill_fk_probe：NOT NULL 外键指向上述空父表（整表跳过）。
func createAutofillProbeTables(ctx context.Context, db *sql.DB) error {
	statements := []string{
		fmt.Sprintf(`CREATE TABLE %s.%s (
			id bigserial PRIMARY KEY,
			label text NOT NULL,
			status text NOT NULL CHECK (status IN ('alpha','beta')),
			payload jsonb NOT NULL,
			ext_json text NOT NULL,
			tags text[] NOT NULL,
			created_at timestamptz NOT NULL,
			amount numeric NOT NULL,
			flag boolean
		)`, SchemaStats, autofillProbeTable),
		fmt.Sprintf(`CREATE TABLE %s.%s (
			id bigint PRIMARY KEY,
			lease_key text NOT NULL
		)`, SchemaStats, autofillProbeParentTable),
		fmt.Sprintf(`CREATE TABLE %s.%s (
			id bigserial PRIMARY KEY,
			parent_id bigint NOT NULL REFERENCES %s.%s(id)
		)`, SchemaStats, autofillProbeFKTable, SchemaStats, autofillProbeParentTable),
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("创建 autofill 探针表: %w", err)
		}
	}
	return nil
}

// dropAutofillProbeTables 删除探针表（coverage Ready 断言前恢复原始 schema
// 形状：探针表的空态不该进入覆盖评估）。
func dropAutofillProbeTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf(
		"DROP TABLE IF EXISTS %s.%s, %s.%s, %s.%s",
		SchemaStats, autofillProbeFKTable,
		SchemaStats, autofillProbeParentTable,
		SchemaStats, autofillProbeTable))
	if err != nil {
		return fmt.Errorf("删除 autofill 探针表: %w", err)
	}
	return nil
}

func TestAutofillFillsEmptyProbeTablesWithCoverageReady(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	summaryPath := filepath.Join(t.TempDir(), "mockdata-summary.json")
	tdb := pgtestkit.NewTestDatabase(t, func(ctx context.Context, target *sql.DB) error {
		if _, err := schema.EnsurePostgres(ctx, target); err != nil {
			return err
		}
		return createAutofillProbeTables(ctx, target)
	})
	dsn := tdb.DSN

	probeKey := qualified(SchemaStats, autofillProbeTable)
	parentKey := qualified(SchemaStats, autofillProbeParentTable)
	fkKey := qualified(SchemaStats, autofillProbeFKTable)

	first := runMockdataForTest(t, dsn, now, summaryPath)

	// 1. 空探针表被补 1..3 行，且本次运行没有预期之外的补行对象（fresh 库的
	//    全部空表要么被域写入、要么被 seed、要么在白名单/跳过清单内）。
	rows, filled := first.Autofill[probeKey]
	if !filled || rows < 1 || rows > 3 {
		t.Fatalf("探针表应被补 1..3 行: autofill=%v", first.Autofill)
	}
	if len(first.Autofill) != 1 {
		t.Fatalf("fresh 库只应补探针表一张: autofill=%v", first.Autofill)
	}

	// 2. 补行结构合法：NOT NULL 列有值、CHECK 取允许值、JSON 合法、数组非空、
	//    主键由序列生成。
	e, err := newEnv(Options{DSN: dsn, Days: 1, DailyRequests: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	var (
		label, status, payload, extJSON, tags string
		createdAt                             time.Time
		amount                                float64
		flag                                  sql.NullBool
		probeID                               int64
	)
	if err := e.db.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT id, label, status, payload::text, ext_json, tags::text, created_at, amount, flag FROM %s ORDER BY id LIMIT 1",
		quoteQualified(probeKey))).Scan(&probeID, &label, &status, &payload, &extJSON, &tags, &createdAt, &amount, &flag); err != nil {
		t.Fatalf("读探针行: %v", err)
	}
	if probeID <= 0 {
		t.Fatalf("bigserial 主键应由序列生成: %d", probeID)
	}
	if !strings.HasPrefix(label, CleanupIDPrefix) {
		t.Fatalf("label 应携带清理标识: %q", label)
	}
	if status != "alpha" && status != "beta" {
		t.Fatalf("status 应取 CHECK 允许值: %q", status)
	}
	var payloadValue any
	if err := json.Unmarshal([]byte(payload), &payloadValue); err != nil {
		t.Fatalf("payload 应为合法 JSON: %q (%v)", payload, err)
	}
	if !json.Valid([]byte(extJSON)) {
		t.Fatalf("ext_json 应为合法 JSON: %q", extJSON)
	}
	if !strings.HasPrefix(tags, "{") || !strings.Contains(tags, CleanupTracePrefix) {
		t.Fatalf("tags 应为携带清理标识的非空数组: %q", tags)
	}
	if createdAt.IsZero() || !strings.HasPrefix(createdAt.UTC().Format(isoMillisLayout), "2026-10-25") {
		t.Fatalf("created_at 应为本次时间基准附近: %v", createdAt)
	}
	if amount <= 0 {
		t.Fatalf("amount 应为正数占位: %v", amount)
	}
	if flag.Valid {
		t.Fatalf("可空列 flag 不应被补值: %+v", flag)
	}

	// 3. FK 父表缺失：子表整表跳过并登记原因；父表命中 _owner_leases 跳过。
	if reason, ok := first.AutofillSkipped[fkKey]; !ok || !strings.Contains(reason, "外键父表") || !strings.Contains(reason, parentKey) {
		t.Fatalf("FK 子表应因父表缺行跳过: skipped=%v", first.AutofillSkipped[fkKey])
	}
	if reason, ok := first.AutofillSkipped[parentKey]; !ok || !strings.Contains(reason, "_owner_leases") {
		t.Fatalf("父表应命中跳过后缀: skipped=%v", first.AutofillSkipped[parentKey])
	}

	// 4. 运行态 / 游标 / 瞬时队列跳过清单不被补行（EnsurePostgres 建的表；
	//    account_health_current_state 等运行时建表在 fresh 库不存在，不进
	//    枚举也就不在跳过清单里）。
	for _, table := range []string{
		"background_job_leases", "stats_job_state", "backfill_progress",
		"usage_range_window_requests",
	} {
		key := qualified(SchemaStats, table)
		if _, ok := first.AutofillSkipped[key]; !ok {
			t.Fatalf("运行态表 %s 应出现在跳过清单", key)
		}
		count, err := e.queryCount(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("运行态表 %s 不应被补行: %d 行", key, count)
		}
	}
	queueKey := qualified(SchemaCodexContext, "codex_context_storage_cleanup_queue")
	if _, ok := first.AutofillSkipped[queueKey]; !ok {
		t.Fatalf("瞬时队列 %s 应出现在跳过清单", queueKey)
	}

	// 5. 重复 Run 不叠加：清理阶段删掉探针的标识行后 autofill 重新补齐，行数
	//    与首次一致，FK 跳过语义稳定。
	second := runMockdataForTest(t, dsn, now.Add(2*time.Hour), summaryPath)
	if second.Autofill[probeKey] != first.Autofill[probeKey] {
		t.Fatalf("重复执行叠加了探针表: first=%d second=%d", first.Autofill[probeKey], second.Autofill[probeKey])
	}
	count, err := e.queryCount(ctx, probeKey)
	if err != nil {
		t.Fatal(err)
	}
	if count != rows {
		t.Fatalf("探针表行数不符: want %d got %d", rows, count)
	}
	if reason, ok := second.AutofillSkipped[fkKey]; !ok || !strings.Contains(reason, "外键父表") {
		t.Fatalf("第二次运行的 FK 跳过语义应稳定: %v", second.AutofillSkipped[fkKey])
	}

	// 6. 删除探针表后 --verify-mockdata-coverage Ready：Empty 为空、Errors 为
	//    空、属主存储缺失只进 OwnerStoresPending 不阻塞 Ready。
	if err := dropAutofillProbeTables(ctx, e.db); err != nil {
		t.Fatal(err)
	}
	report, err := VerifyCoverage(ctx, Options{
		DSN:           dsn,
		Days:          DefaultDays,
		DailyRequests: DefaultDailyRequests,
		SummaryFile:   summaryPath,
	})
	if err != nil {
		t.Fatalf("VerifyCoverage: %v", err)
	}
	if len(report.Empty) != 0 {
		t.Fatalf("autofill 后覆盖仍有空表: %v", report.Empty)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("覆盖断言失败: %v", report.Errors)
	}
	if len(report.OwnerStoresPending) != len(ownerStores) {
		t.Fatalf("OwnerStoresPending = %+v, 期望 %d 项", report.OwnerStoresPending, len(ownerStores))
	}
	if !report.Ready {
		t.Fatalf("覆盖未 Ready: empty=%v errors=%v notCovered=%v", report.Empty, report.Errors, report.NotCovered)
	}
}
