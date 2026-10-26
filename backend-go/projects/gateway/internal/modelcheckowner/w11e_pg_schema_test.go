package modelcheckowner

// w11e PostgreSQL 门控覆盖：CheckBusinessPostgresSchema 的完整正/负臂与
// OpenBusinessTargetConnection 的 PostgreSQL 分支。
//
// 隔离铁律（沿用 auditlog w10b 先例，2026-10-26 收口）：
//   - 建库走 pgtestkit 每测试独立临时库（用毕强制删库）；本测试专属
//     w11e_bs_* schema 与 juhe_business schema 的建删都在隔离库内进行，
//     主库对象一个字节不动。
//   - 连接配置由 pgtestkit 自 .local dev env 或 JUHE_AI_TEST_PG_ADMIN_URL
//     解析；任何输出不得携带连接串或密码。
//   - env 缺失或 PG 不可达由 pgtestkit 显式 Fatal 而非 skip。

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const w11ePgBudget = 60 * time.Second

// w11eOpenCoverDB 打开 pgtestkit 隔离 PG 测试库（本测试专属 schema 的建删
// 载体），返回已 ping 通过的连接池。测试结束自动关池并删库。
func w11eOpenCoverDB(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t)
}

// w11eOpenKitDB 同 w11eOpenCoverDB，但同时返回库连接串（需要以同库 URL
// 构造注入驱动或 store 的测试用）。
func w11eOpenKitDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t)
	return testDB.DB, testDB.DSN
}

// w11eSchemaPlan 描述对生成 DDL 的负向变体；零值生成满足契约的完整 schema。
type w11eSchemaPlan struct {
	skipTable        map[string]bool     // 完全跳过某表
	viewTables       map[string]bool     // 以视图替代基表
	partitionTables  map[string]bool     // 以分区表创建（relkind=p 正臂）
	missingColumn    map[string]string   // 表 -> 缺失列
	skipIndex        map[string]bool     // 跳过名称索引
	skipUnique       map[string][]string // 表 -> 跳过的唯一约束（按逗号连接的列）
	skipPK           map[string]bool     // 跳过主键
	skipFK           map[string]bool     // 跳过外键（签名 表->引用表）
	fkActionOverride map[string]string   // 签名 -> 覆盖 ON DELETE 动作
	indexVariant     map[string]string   // 索引名 -> 变体（expression/wrongcols/nonunique/nopredicate/wrongpredicate）
}

func w11eNewPlan() w11eSchemaPlan {
	return w11eSchemaPlan{
		skipTable: map[string]bool{}, viewTables: map[string]bool{}, partitionTables: map[string]bool{},
		missingColumn: map[string]string{}, skipIndex: map[string]bool{}, skipUnique: map[string][]string{},
		skipPK: map[string]bool{}, skipFK: map[string]bool{}, fkActionOverride: map[string]string{}, indexVariant: map[string]string{},
	}
}

func w11eQuoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// w11eBareIntegerComparison 命中契约谓词中的裸整数字面量比较（列 = 123）；
// 字符串字面量（'key_model'）不匹配。
var w11eBareIntegerComparison = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(\d+)\b`)

// w11ePgFixturePredicate 把契约谓词改写为可在 TEXT 列 fixture 上执行的
// PostgreSQL 形态：PG 无隐式 text=integer，裸整数比较补 ::integer 转型。
// 校验侧的谓词等价归一会剥掉括号与 ::type 转型，实际谓词仍与契约谓词
// 等价（v16 groups 部分唯一索引 is_default = 1 首次引入整数谓词）。
func w11ePgFixturePredicate(predicate string) string {
	return w11eBareIntegerComparison.ReplaceAllString(predicate, "${1}::integer = ${2}")
}

// w11eSchemaDDL 从 contracts.BusinessSchema 生成结构等价的 PostgreSQL
// DDL。类型全部使用 TEXT：本契约只校验结构（列名/主键/唯一/外键/索引），
// 不校验列类型。被外键引用的列在 PostgreSQL 中必须有唯一约束，契约未声明的
// 一律补 UNIQUE（多余约束对本契约是前向兼容的）。
func w11eSchemaDDL(schema string, plan w11eSchemaPlan) []string {
	q := w11eQuoteIdentifier
	qualify := func(name string) string { return q(schema) + "." + q(name) }
	referencedUnique := map[string]bool{}
	for _, spec := range contracts.BusinessSchema {
		for _, fk := range spec.ForeignKeys {
			referencedUnique[fk.RefTable+"("+strings.Join(fk.RefColumns, ",")+")"] = true
		}
	}
	statements := []string{`CREATE SCHEMA ` + q(schema)}
	tables := make([]string, 0, len(contracts.BusinessSchema))
	for table := range contracts.BusinessSchema {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		if plan.skipTable[table] || plan.viewTables[table] {
			continue
		}
		spec := contracts.BusinessSchema[table]
		parts := make([]string, 0, len(spec.Columns))
		for _, column := range spec.Columns {
			if plan.missingColumn[table] == column {
				continue
			}
			parts = append(parts, q(column)+" TEXT")
		}
		covered := map[string]bool{}
		if len(spec.PrimaryKey) > 0 && !plan.skipPK[table] {
			quoted := make([]string, 0, len(spec.PrimaryKey))
			for _, column := range spec.PrimaryKey {
				quoted = append(quoted, q(column))
			}
			parts = append(parts, "PRIMARY KEY ("+strings.Join(quoted, ",")+")")
			covered[strings.Join(spec.PrimaryKey, ",")] = true
		}
		for _, unique := range spec.UniqueConstraints {
			covered[strings.Join(unique, ",")] = true
			if plan.skipUnique[table] != nil && strings.Join(plan.skipUnique[table], ",") == strings.Join(unique, ",") {
				continue
			}
			quoted := make([]string, 0, len(unique))
			for _, column := range unique {
				quoted = append(quoted, q(column))
			}
			parts = append(parts, "UNIQUE ("+strings.Join(quoted, ",")+")")
		}
		for key := range referencedUnique {
			columns, found := strings.CutPrefix(key, table+"(")
			if !found || !strings.HasSuffix(columns, ")") || covered[columns[:len(columns)-1]] {
				continue
			}
			quoted := make([]string, 0)
			for _, column := range strings.Split(columns[:len(columns)-1], ",") {
				quoted = append(quoted, q(column))
			}
			parts = append(parts, "UNIQUE ("+strings.Join(quoted, ",")+")")
		}
		create := "CREATE TABLE " + qualify(table) + " (" + strings.Join(parts, ",") + ")"
		if plan.partitionTables[table] && len(spec.Columns) > 0 {
			create += " PARTITION BY RANGE (" + q(spec.Columns[0]) + ")"
		}
		statements = append(statements, create)
	}
	// 外键在全部表存在后追加，避免依赖契约内表顺序。
	fkIndex := 0
	for _, table := range tables {
		if plan.skipTable[table] || plan.viewTables[table] {
			continue
		}
		spec := contracts.BusinessSchema[table]
		for _, fk := range spec.ForeignKeys {
			signature := table + "->" + fk.RefTable
			if plan.skipFK[signature] {
				continue
			}
			fkIndex++
			fromColumns := make([]string, 0, len(fk.Columns))
			for _, column := range fk.Columns {
				fromColumns = append(fromColumns, q(column))
			}
			refColumns := make([]string, 0, len(fk.RefColumns))
			for _, column := range fk.RefColumns {
				refColumns = append(refColumns, q(column))
			}
			onDelete := fk.OnDelete
			if override, ok := plan.fkActionOverride[signature]; ok {
				onDelete = override
			}
			clause := "ALTER TABLE " + qualify(table) + " ADD CONSTRAINT " + q(fmt.Sprintf("w11e_fk_%d", fkIndex)) + " FOREIGN KEY (" + strings.Join(fromColumns, ",") + ") REFERENCES " + qualify(fk.RefTable) + " (" + strings.Join(refColumns, ",") + ")"
			if onDelete != "" && onDelete != "NO ACTION" {
				clause += " ON DELETE " + onDelete
			}
			if fk.OnUpdate != "" && fk.OnUpdate != "NO ACTION" {
				clause += " ON UPDATE " + fk.OnUpdate
			}
			statements = append(statements, clause)
		}
	}
	for _, table := range tables {
		if plan.skipTable[table] || plan.viewTables[table] {
			continue
		}
		spec := contracts.BusinessSchema[table]
		definitionNames := map[string]bool{}
		for _, definition := range spec.IndexDefinitions {
			definitionNames[definition.Name] = true
		}
		for _, indexName := range spec.Indexes {
			if plan.skipIndex[indexName] || definitionNames[indexName] {
				continue
			}
			column := q(spec.Columns[0])
			statements = append(statements, "CREATE INDEX "+q(indexName)+" ON "+qualify(table)+" ("+column+")")
		}
		for _, definition := range spec.IndexDefinitions {
			indexName := definition.Name
			uniqueKeyword := ""
			if definition.Unique {
				uniqueKeyword = "UNIQUE "
			}
			columns := make([]string, 0, len(definition.Columns))
			for _, column := range definition.Columns {
				columns = append(columns, q(column))
			}
			columnList := strings.Join(columns, ",")
			predicate := ""
			if strings.TrimSpace(definition.Predicate) != "" {
				predicate = " WHERE " + w11ePgFixturePredicate(definition.Predicate)
			}
			switch plan.indexVariant[indexName] {
			case "expression":
				columnList = "lower(" + q(definition.Columns[0]) + ")," + q(definition.Columns[1])
				statements = append(statements, "CREATE "+uniqueKeyword+"INDEX "+q(indexName)+" ON "+qualify(table)+" ("+columnList+")"+predicate)
			case "wrongcols":
				reversed := make([]string, 0, len(definition.Columns))
				for index := len(definition.Columns) - 1; index >= 0; index-- {
					reversed = append(reversed, q(definition.Columns[index]))
				}
				statements = append(statements, "CREATE "+uniqueKeyword+"INDEX "+q(indexName)+" ON "+qualify(table)+" ("+strings.Join(reversed, ",")+")"+predicate)
			case "nonunique":
				statements = append(statements, "CREATE INDEX "+q(indexName)+" ON "+qualify(table)+" ("+columnList+")"+predicate)
			case "nopredicate":
				statements = append(statements, "CREATE "+uniqueKeyword+"INDEX "+q(indexName)+" ON "+qualify(table)+" ("+columnList+")")
			case "wrongpredicate":
				wrong := strings.ReplaceAll(definition.Predicate, "'key_model'", "'w11e_other'")
				statements = append(statements, "CREATE "+uniqueKeyword+"INDEX "+q(indexName)+" ON "+qualify(table)+" ("+columnList+") WHERE "+wrong)
			default:
				statements = append(statements, "CREATE "+uniqueKeyword+"INDEX "+q(indexName)+" ON "+qualify(table)+" ("+columnList+")"+predicate)
			}
		}
	}
	for table := range plan.viewTables {
		spec := contracts.BusinessSchema[table]
		selects := make([]string, 0, len(spec.Columns))
		for index, column := range spec.Columns {
			if plan.missingColumn[table] == column {
				continue
			}
			selects = append(selects, fmt.Sprintf("'w11e'::text AS %s", q(column))+" /* "+fmt.Sprint(index)+" */")
		}
		// 用一个真实基表做视图载体，保证视图在 information_schema 中可见。
		statements = append(statements, "CREATE VIEW "+qualify(table)+" AS SELECT "+strings.Join(selects, ",")+" FROM "+qualify("system_accounts"))
	}
	return statements
}

// w11eRecreateSchema 删除并按计划重建 schema；测试结束时再删一次。
func w11eRecreateSchema(t *testing.T, db *sql.DB, schema string, plan w11eSchemaPlan) {
	t.Helper()
	// drop 必须自建 context：t.Cleanup 在本函数 return（defer cancel 已触发）
	// 之后才运行，复用外层 ctx 会让 DROP 静默失败，把 w11e_fk_% 外键连同
	// schema 泄漏进隔离库，破坏后续用例对 fresh 形状的前提（W30）。
	drop := func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), w11ePgBudget)
		defer dropCancel()
		_, _ = db.ExecContext(dropCtx, `DROP SCHEMA IF EXISTS `+w11eQuoteIdentifier(schema)+` CASCADE`)
	}
	drop()
	ctx, cancel := context.WithTimeout(context.Background(), w11ePgBudget)
	defer cancel()
	for _, statement := range w11eSchemaDDL(schema, plan) {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("执行 DDL 失败（schema=%s）: %v", schema, err)
		}
	}
	t.Cleanup(drop)
}

func TestW11ECheckBusinessPostgresSchemaAcceptsContractShape(t *testing.T) {
	db := w11eOpenCoverDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), w11ePgBudget)
	defer cancel()

	plan := w11eNewPlan()
	w11eRecreateSchema(t, db, "w11e_bs_pos", plan)
	if err := CheckBusinessPostgresSchema(ctx, db, "w11e_bs_pos"); err != nil {
		t.Fatalf("生成契约结构必须通过校验: %v", err)
	}

	// relkind=p 分区表是合法 BASE TABLE 形态。
	partitioned := w11eNewPlan()
	partitioned.partitionTables["model_quality_policies"] = true
	w11eRecreateSchema(t, db, "w11e_bs_part", partitioned)
	if err := CheckBusinessPostgresSchema(ctx, db, "w11e_bs_part"); err != nil {
		t.Fatalf("分区表形态必须通过校验: %v", err)
	}

	// canceled context 必须在首个查询处失败关闭。
	canceled, cancelArm := context.WithCancel(context.Background())
	cancelArm()
	if err := CheckBusinessPostgresSchema(canceled, db, "w11e_bs_pos"); err == nil || !strings.Contains(err.Error(), "list Business PostgreSQL tables") {
		t.Fatalf("canceled context 必须失败关闭: %v", err)
	}

	// 空 schema 拒绝。
	if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS w11e_bs_empty CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA w11e_bs_empty`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// 同 w11eRecreateSchema：清理必须自建 context，测试级 ctx 此刻已取消。
		emptyCtx, emptyCancel := context.WithTimeout(context.Background(), w11ePgBudget)
		defer emptyCancel()
		_, _ = db.ExecContext(emptyCtx, `DROP SCHEMA IF EXISTS w11e_bs_empty CASCADE`)
	})
	if err := CheckBusinessPostgresSchema(ctx, db, "w11e_bs_empty"); err == nil || !strings.Contains(err.Error(), "contains no tables") {
		t.Fatalf("空 schema 必须拒绝: %v", err)
	}
}

func TestW11ECheckBusinessPostgresSchemaRejectsDrifts(t *testing.T) {
	db := w11eOpenCoverDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), w11ePgBudget)
	defer cancel()
	missingTable := w11eNewPlan()
	missingTable.skipTable["model_quality_policies"] = true
	asView := w11eNewPlan()
	asView.viewTables["model_quality_policies"] = true
	missingColumn := w11eNewPlan()
	missingColumn.missingColumn["model_quality_policies"] = "profile"
	missingPK := w11eNewPlan()
	missingPK.skipPK["account_quality_enforcements"] = true
	missingUnique := w11eNewPlan()
	missingUnique.skipUnique["system_accounts"] = []string{"username"}
	missingFK := w11eNewPlan()
	missingFK.skipFK["system_sessions->system_accounts"] = true
	wrongFKAction := w11eNewPlan()
	wrongFKAction.fkActionOverride["system_sessions->system_accounts"] = "RESTRICT"
	missingIndex := w11eNewPlan()
	missingIndex.skipIndex["idx_system_sessions_expires_at"] = true
	expressionIndex := w11eNewPlan()
	expressionIndex.indexVariant["idx_account_circuit_incidents_key_model_capability"] = "expression"
	wrongColumns := w11eNewPlan()
	wrongColumns.indexVariant["idx_account_circuit_incidents_key_model_capability"] = "wrongcols"
	nonUnique := w11eNewPlan()
	nonUnique.indexVariant["idx_account_circuit_incidents_key_model_capability"] = "nonunique"
	noPredicate := w11eNewPlan()
	noPredicate.indexVariant["idx_account_circuit_incidents_key_model_capability"] = "nopredicate"
	wrongPredicate := w11eNewPlan()
	wrongPredicate.indexVariant["idx_account_circuit_incidents_key_model_capability"] = "wrongpredicate"
	cases := []struct {
		schema string
		plan   w11eSchemaPlan
		want   string
	}{
		{schema: "w11e_bs_neg1", plan: missingTable, want: "missing table model_quality_policies"},
		{schema: "w11e_bs_neg2", plan: asView, want: "want BASE TABLE"},
		{schema: "w11e_bs_neg3", plan: missingColumn, want: "missing column model_quality_policies.profile"},
		{schema: "w11e_bs_neg4", plan: missingPK, want: "missing primary key account_quality_enforcements"},
		{schema: "w11e_bs_neg5", plan: missingUnique, want: "missing unique constraint system_accounts"},
		{schema: "w11e_bs_neg6", plan: missingFK, want: "missing foreign key"},
		{schema: "w11e_bs_neg7", plan: wrongFKAction, want: "missing foreign key"},
		{schema: "w11e_bs_neg8", plan: missingIndex, want: "missing index system_sessions.idx_system_sessions_expires_at"},
		{schema: "w11e_bs_neg9", plan: expressionIndex, want: "index contains an expression"},
		{schema: "w11e_bs_neg10", plan: wrongColumns, want: "incompatible index"},
		{schema: "w11e_bs_neg11", plan: nonUnique, want: "incompatible index"},
		{schema: "w11e_bs_neg12", plan: noPredicate, want: "incompatible index"},
		{schema: "w11e_bs_neg13", plan: wrongPredicate, want: "incompatible index"},
	}
	for _, tc := range cases {
		t.Run(tc.schema, func(t *testing.T) {
			w11eRecreateSchema(t, db, tc.schema, tc.plan)
			if err := CheckBusinessPostgresSchema(ctx, db, tc.schema); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望错误 %q，实际: %v", tc.want, err)
			}
		})
	}
}

// OpenBusinessTargetConnection 的 PostgreSQL 分支：SchemaReady 门禁、
// 缺 URL 拒绝与共享句柄契约。
func TestW11EOpenBusinessTargetConnectionPostgresArms(t *testing.T) {
	testDB := pgtestkit.NewTestDatabase(t)
	db := testDB.DB
	coverURL := testDB.DSN
	ctx, cancel := context.WithTimeout(context.Background(), w11ePgBudget)
	defer cancel()

	// PostgreSQL 模式缺 URL 必须拒绝。
	if _, err := OpenBusinessTargetConnection(context.Background(), Config{Enabled: true, CredentialSecret: "w11e-secret"}); err == nil || !strings.Contains(err.Error(), "PostgreSQL URL is required") {
		t.Fatalf("缺 URL 必须拒绝: %v", err)
	}

	// juhe_business 不满足契约时 SchemaReady 必须拒绝。
	broken := w11eNewPlan()
	broken.skipTable["model_quality_policies"] = true
	w11eRecreateSchema(t, db, "juhe_business", broken)
	if _, err := OpenBusinessTargetConnection(ctx, Config{Enabled: true, BusinessPostgresURL: coverURL, CredentialSecret: "w11e-secret", SchemaReady: true}); err == nil || !strings.Contains(err.Error(), "missing table model_quality_policies") {
		t.Fatalf("SchemaReady 契约失败必须传播: %v", err)
	}

	// 完整契约下打开成功，Source 与 DB 共享句柄。
	plan := w11eNewPlan()
	w11eRecreateSchema(t, db, "juhe_business", plan)
	connection, err := OpenBusinessTargetConnection(ctx, Config{Enabled: true, BusinessPostgresURL: coverURL, CredentialSecret: "w11e-secret", SchemaReady: true})
	if err != nil {
		t.Fatalf("完整契约必须允许打开: %v", err)
	}
	if connection.DB == nil || connection.Source == nil {
		t.Fatal("连接与 Source 必须共享已验证句柄")
	}
	if _, err := connection.DB.QueryContext(ctx, `SELECT 1`); err != nil {
		t.Fatalf("共享句柄必须可用: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
}
