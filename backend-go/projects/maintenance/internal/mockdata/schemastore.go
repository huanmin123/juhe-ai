package mockdata

import (
	"context"
	"fmt"
	"sort"
)

// PostgreSQL 单库多 schema 存储拓扑（docs/functions/Mockdata造数设计.md §4）：
// 造数域 → schema 的唯一映射。全部 SQL 全限定 schema；属主表（gateway/jobs
// 运行时建表）只探测不复制 DDL。
const (
	SchemaBusiness     = "juhe_business"
	SchemaUsage        = "juhe_usage"
	SchemaStats        = "juhe_stats"
	SchemaDataset      = "juhe_dataset"
	SchemaChat         = "juhe_chat"
	SchemaCodexContext = "juhe_codex_context"
	// SchemaJ3B 是 J3b 模型检测专库：由 j3bmodelcheck 的受控 bootstrap 建表，
	// schema 本体缺失时记 OwnerStoresPending 而不是失败。
	SchemaJ3B = "juhe_j3b"
	// SchemaJobs 是 jobs 账户健康等 owner schema：maintenance 不建它。
	SchemaJobs = "juhe_jobs"
)

// qualified 返回 schema.table 全限定名（框架内部 SQL 拼接用；列名同样只能
// 来自本包常量）。
func qualified(schema, table string) string { return schema + "." + table }

// quoteQualified 把全限定表名按标识符引用（输入来自本包枚举，格式非法时原样
// 返回——只发生在编程错误场景）。
func quoteQualified(schemaTable string) string {
	schema, table, err := splitQualified(schemaTable)
	if err != nil {
		return schemaTable
	}
	return quotePGIdentifier(schema) + "." + quotePGIdentifier(table)
}

// maintenanceSchemas 是 maintenance EnsurePostgres 建立并归属造数域的 schema
// 清单（不含属主 schema）。顺序即覆盖报告的枚举顺序。
var maintenanceSchemas = []string{
	SchemaBusiness,
	SchemaChat,
	SchemaDataset,
	SchemaUsage,
	SchemaStats,
	SchemaCodexContext,
}

// schemaDomain 返回 schema 归属的造数域（覆盖报告按域判定「未接线」）。
func schemaDomain(schema string) string {
	switch schema {
	case SchemaBusiness:
		return DomainBusiness
	case SchemaUsage:
		return DomainUsage
	case SchemaStats:
		return DomainStats
	case SchemaDataset:
		return DomainObservability
	case SchemaChat, SchemaCodexContext:
		return DomainChatCodexModelCheck
	default:
		return ""
	}
}

// ownerStore 是一个属主存储：表由 gateway / jobs / J3b 运行时（或受控
// bootstrap）创建，maintenance 不复制它们的 DDL。造数前探测；缺失时域跳过、
// 覆盖断言不评估，且不阻塞 Ready。
type ownerStore struct {
	// Key 是报告与日志里的逻辑名。
	Key string
	// Domain 是归属造数域；域未接线时该属主只探测不评估。
	Domain string
	// Schema 与 Tables 是探测目标（Tables 只列哨兵业务表，不含租约/游标族）。
	Schema string
	Tables []string
}

// ownerStores 是全部属主存储注册表。新增属主表时在这里登记，探测、域跳过与
// 覆盖白名单共用这一份事实。
var ownerStores = []ownerStore{
	{Key: "audit_log", Domain: DomainObservability, Schema: SchemaDataset, Tables: []string{"audit_logs"}},
	{Key: "operation_log", Domain: DomainObservability, Schema: SchemaDataset, Tables: []string{"operation_logs"}},
	{Key: "runtime_log", Domain: DomainObservability, Schema: SchemaDataset, Tables: []string{"runtime_logs"}},
	{Key: "model_check", Domain: DomainChatCodexModelCheck, Schema: SchemaJ3B, Tables: []string{"model_check_runs", "model_check_observations"}},
	{Key: "account_health", Domain: DomainChatCodexModelCheck, Schema: SchemaJobs, Tables: []string{"account_health_outcomes"}},
}

// OwnerStorePending 是一个缺失（或部分缺失）的属主存储。出现在报告里不构成
// 运行失败：属主表由各 owner 进程或受控 bootstrap 创建，maintenance 只如实
// 记录「本次造数跳过了它们」。
type OwnerStorePending struct {
	Store         string   `json:"store"`
	Schema        string   `json:"schema"`
	MissingTables []string `json:"missingTables"`
}

// probeOwnerStores 探测全部属主存储，返回缺失清单（顺序按注册表）。
// model_check 属主（juhe_j3b）在探测前由调用方完成受控 bootstrap。
func probeOwnerStores(ctx context.Context, db queryer) ([]OwnerStorePending, error) {
	var pending []OwnerStorePending
	for _, store := range ownerStores {
		var missing []string
		for _, table := range store.Tables {
			exists, err := tableExists(ctx, db, store.Schema, table)
			if err != nil {
				return nil, fmt.Errorf("探测属主表 %s.%s: %w", store.Schema, table, err)
			}
			if !exists {
				missing = append(missing, table)
			}
		}
		if len(missing) > 0 {
			pending = append(pending, OwnerStorePending{Store: store.Key, Schema: store.Schema, MissingTables: missing})
		}
	}
	return pending, nil
}

// tableExists 用 to_regclass 探测表存在性（不写 information_schema 之外的
// 元数据，也不创建任何对象）。
func tableExists(ctx context.Context, db queryer, schema, table string) (bool, error) {
	var regclass *string
	if err := db.QueryRowContext(ctx,
		"SELECT to_regclass($1)::text", schema+"."+table).Scan(&regclass); err != nil {
		return false, err
	}
	return regclass != nil, nil
}

// schemaTables 枚举一个 schema 下造数可评估的用户表：排除分区子表
// （usage_records_YYYYMMDD / chat_messages_YYYYMMDD 由父表代答）、内部对象与
// 登记为属主表的哨兵表。按名字排序保证报告稳定。
func schemaTables(ctx context.Context, db queryer, schema string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1
		  AND c.relkind IN ('r', 'p')
		  AND c.relispartition = false
		ORDER BY c.relname`, schema)
	if err != nil {
		return nil, fmt.Errorf("枚举 schema %s 表清单: %w", schema, err)
	}
	defer rows.Close()
	ownerSentinels := map[string]bool{}
	for _, store := range ownerStores {
		if store.Schema != schema {
			continue
		}
		for _, table := range store.Tables {
			ownerSentinels[table] = true
		}
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if ownerSentinels[name] {
			continue
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("枚举 schema %s 表清单: %w", schema, err)
	}
	return tables, nil
}

// fkEdge 是一条「引用表 → 被引用表」外键边（都在 maintenance schema 内）。
type fkEdge struct {
	Child  string
	Parent string
}

// cleanupOrder 返回 maintenance schema 全表的删除顺序：外键子表先于父表。
//
// 实现：读 information_schema 的外键边（child 持有约束、parent 是被引用的
// 唯一键表；两边都限定在 maintenance schema 内），Kahn 拓扑排序——无子表的
// 叶子先出队，父表最后；成环的表组（accounts ↔ account_connections 的互引）
// 按名字序排在最后，由 cleanup.go 的 FK 冲突重试轮兜底。自引用边（accounts
// 的授权实例列）不参与排序，由业务侧两段式删除规则处理。返回值是全限定表名，
// 顺序稳定（同层级按 schema + 名字排序）。
//
// 属主哨兵表（schemastore 的 ownerStores）若落在 maintenance schema 内
// （juhe_dataset 的 audit_logs / operation_logs / runtime_logs），造数会写入
// 它们，清理节点集也显式包含：表不存在时由 cleanupTable 记入 skipped（首次
// 运行的属主缺失场景）。juhe_j3b / juhe_jobs 等非 maintenance schema 的属主
// 不是造数写入面，不参与清理。
func cleanupOrder(ctx context.Context, db queryer) ([]string, error) {
	// 全量节点：六个 maintenance schema 的全部用户表（含分区父表，分区子表
	// 随父表删除），再加 maintenance schema 内的属主哨兵表。
	nodes := map[string]bool{}
	for _, schema := range maintenanceSchemas {
		tables, err := schemaTables(ctx, db, schema)
		if err != nil {
			return nil, err
		}
		for _, table := range tables {
			nodes[qualified(schema, table)] = true
		}
	}
	maintenanceSchemaSet := map[string]bool{}
	for _, schema := range maintenanceSchemas {
		maintenanceSchemaSet[schema] = true
	}
	for _, store := range ownerStores {
		if !maintenanceSchemaSet[store.Schema] {
			continue
		}
		for _, table := range store.Tables {
			nodes[qualified(store.Schema, table)] = true
		}
	}
	edges, err := foreignKeyEdges(ctx, db)
	if err != nil {
		return nil, err
	}
	// 入度：parent 被多少条边指着（parent 必须晚于全部 child 删除）。
	indegree := map[string]int{}
	children := map[string][]string{}
	for _, edge := range edges {
		if edge.Child == edge.Parent {
			continue
		}
		indegree[edge.Parent]++
		children[edge.Child] = append(children[edge.Child], edge.Parent)
	}
	var queue []string
	for node := range nodes {
		if indegree[node] == 0 {
			queue = append(queue, node)
		}
	}
	sortStrings(queue)
	ordered := make([]string, 0, len(nodes))
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		ordered = append(ordered, node)
		parents := append([]string(nil), children[node]...)
		sortStrings(parents)
		for _, parent := range parents {
			indegree[parent]--
			if indegree[parent] == 0 {
				queue = append(queue, parent)
				sortStrings(queue)
			}
		}
	}
	if len(ordered) < len(nodes) {
		// 成环节点：按名字序兜底追加（FK 冲突由重试轮处理）。
		remaining := make([]string, 0, len(nodes)-len(ordered))
		placed := map[string]bool{}
		for _, node := range ordered {
			placed[node] = true
		}
		for node := range nodes {
			if !placed[node] {
				remaining = append(remaining, node)
			}
		}
		sortStrings(remaining)
		ordered = append(ordered, remaining...)
	}
	return ordered, nil
}

// foreignKeyEdges 读取 maintenance schema 内的全部外键边。child 是约束所在
// 表，parent 是被引用唯一键表；任一端不在 maintenance schema 内的边忽略
// （maintenance 表不引用属主表，属主表也不参与造数清理）。
func foreignKeyEdges(ctx context.Context, db queryer) ([]fkEdge, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT
			tc.table_schema, tc.table_name,
			uc.table_schema, uc.table_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.referential_constraints rc
		  ON rc.constraint_name = tc.constraint_name
		 AND rc.constraint_schema = tc.constraint_schema
		JOIN information_schema.table_constraints uc
		  ON uc.constraint_name = rc.unique_constraint_name
		 AND uc.constraint_schema = rc.unique_constraint_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'`)
	if err != nil {
		return nil, fmt.Errorf("读取外键依赖图: %w", err)
	}
	defer rows.Close()
	schemaSet := map[string]bool{}
	for _, schema := range maintenanceSchemas {
		schemaSet[schema] = true
	}
	var edges []fkEdge
	for rows.Next() {
		var childSchema, childTable, parentSchema, parentTable string
		if err := rows.Scan(&childSchema, &childTable, &parentSchema, &parentTable); err != nil {
			return nil, fmt.Errorf("读取外键依赖图: %w", err)
		}
		if !schemaSet[childSchema] || !schemaSet[parentSchema] {
			continue
		}
		edges = append(edges, fkEdge{
			Child:  qualified(childSchema, childTable),
			Parent: qualified(parentSchema, parentTable),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取外键依赖图: %w", err)
	}
	return edges, nil
}

// sortStrings 是 sort.Strings 的语义别名（框架内多处稳定排序共用）。
func sortStrings(values []string) { sort.Strings(values) }
