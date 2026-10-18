package providerconnectionmigration

// manifest 模型（设计 §10 :421-467）：format_version 固定 1；每个 source row
// 记录 source table/PK、target table/PK、目标字段、判定规则、证据来源、错误、
// disposition（mapped/merged/blocked）与 target_ids[]；附各 source table row
// count、source snapshot hash、source/target schema version 与 UTC 创建时间。
//
// 脱敏契约：manifest 不得包含明文凭据、API key、OAuth token 或解密后
// credential；凭据只记 fingerprint（本切片口径：SHA-256(ciphertext UTF-8
// bytes) 十六进制，不做解密）。

// FormatVersion 是 manifest 契约版本（设计 §10 固定为 1）。
const FormatVersion = 1

// Disposition 词表（设计 §10 :526：manifest 每个 source row 必须有唯一
// disposition：mapped、merged 或 blocked）。
const (
	DispositionMapped  = "mapped"
	DispositionMerged  = "merged"
	DispositionBlocked = "blocked"
)

// BlockedCounts 是 §10 :469-479 的九项硬门槛计数（dry-run 允许非零；正式执行
// 前必须全部为 0）。
type BlockedCounts struct {
	AccountsBlocked        int `json:"accounts_blocked"`
	ConnectionsBlocked     int `json:"connections_blocked"`
	ModelsBlocked          int `json:"models_blocked"`
	SupportedModelsBlocked int `json:"account_supported_models_blocked"`
	MappingsBlocked        int `json:"mappings_blocked"`
	GroupsBlocked          int `json:"groups_blocked"`
	HealthChecksBlocked    int `json:"health_checks_blocked"`
	HistoryRowsUnmatched   int `json:"history_rows_unmatched"`
	PublicModelConflicts   int `json:"public_model_conflicts"`
}

// Evidence 是证据来源对象（设计 §10 :463：{source_table, source_pk,
// source_fields, retrieved_at_utc}；source_fields 记录参与判定的源列名与
// canonical 值 hash，不得含明文凭据或 token）。
type Evidence struct {
	SourceTable    string            `json:"source_table"`
	SourcePK       []string          `json:"source_pk"`
	SourceFields   map[string]string `json:"source_fields"`
	RetrievedAtUTC string            `json:"retrieved_at_utc"`
}

// RowDisposition 是单个 source row 的迁移判定行。
type RowDisposition struct {
	SourceTable string `json:"source_table"`
	SourcePK    []string `json:"source_pk"`
	// SourceRowCount 仅规则 11 的按表清点行使用（历史行本切片不逐行展开，
	// 见 migrate.go 切片口径）。
	SourceRowCount int `json:"source_row_count,omitempty"`
	// TargetTable 为空表示该行按设计退役、不产生目标行（providers 的
	// openai/hybrid 行、protocol profile 行等；见 rules.go 口径注释）。
	TargetTable string   `json:"target_table,omitempty"`
	TargetPK    []string `json:"target_pk,omitempty"`
	// TargetFields 记录目标字段取值或推导说明（值均为非敏感派生值；映射为
	// "来源说明=值" 或纯派生说明文本）。
	TargetFields map[string]string `json:"target_fields,omitempty"`
	// Rule 是设计 §10 的判定规则编号（0-13；1a/1b 归 1、6a 归 6、8a 归 8，
	// 用 RuleNote 区分）。
	Rule     int      `json:"rule"`
	RuleNote string   `json:"rule_note,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`
	Errors   []string `json:"errors,omitempty"`
	// Disposition ∈ {mapped, merged, blocked}。
	Disposition string   `json:"disposition"`
	TargetIDs   []string `json:"target_ids,omitempty"`
}

// GeneratedTargetTable 是迁移器确实生成、且没有对应 source row 的目标记录
// 清单（设计 §10 :467）：每张目标表单独列出 ID 数组与生成原因；没有该字段的
// 目标表按空数组处理。
type GeneratedTargetTable struct {
	TargetTable string   `json:"target_table"`
	Reason      string   `json:"reason"`
	IDs         []string `json:"ids"`
}

// PublicModelConflict 是规则 13 的冲突对象（设计 §10 :518 固定字段：
// {resource_account_id, wire_model, direct_model_ids[], mapping_ids[],
// resolution}；dry-run 不携带 resolution，留待 manifest 修复）。
type PublicModelConflict struct {
	ResourceAccountID string   `json:"resource_account_id"`
	WireModel         string   `json:"wire_model"`
	DirectModelIDs    []string `json:"direct_model_ids"`
	MappingIDs        []string `json:"mapping_ids"`
	Resolution        string   `json:"resolution,omitempty"`
}

// HealthCheckBlock 是规则 12 的阻塞对象（设计 §10 :463 固定字段：
// {resource_account_id, reason, candidate_connection_ids[], candidate_model_ids[],
// source_fields_hash}）。
type HealthCheckBlock struct {
	ResourceAccountID      string   `json:"resource_account_id"`
	Reason                 string   `json:"reason"`
	CandidateConnectionIDs []string `json:"candidate_connection_ids"`
	CandidateModelIDs      []string `json:"candidate_model_ids"`
	SourceFieldsHash       string   `json:"source_fields_hash"`
	Detail                 string   `json:"detail,omitempty"`
}

// Manifest 是 dry-run 产出的可复核清单（--manifest-out 的 JSON 内容）。
type Manifest struct {
	FormatVersion       int               `json:"format_version"`
	CreatedAtUTC        string            `json:"created_at_utc"`
	SourceSchemaVersion string            `json:"source_schema_version"`
	TargetSchemaVersion string            `json:"target_schema_version"`
	SourceSnapshotHash  string            `json:"source_snapshot_hash"`
	SourceTableRowCounts map[string]int   `json:"source_table_row_counts"`
	Rows                []RowDisposition  `json:"rows"`
	GeneratedTargetIDs  []GeneratedTargetTable `json:"generated_target_ids"`
	BlockedCounts       BlockedCounts     `json:"blocked_counts"`
	PublicModelConflicts []PublicModelConflict `json:"public_model_conflicts"`
	HealthCheckBlocks   []HealthCheckBlock `json:"health_check_blocks"`
	// Notes 记录切片边界与判定口径（正式执行切片复核清单）。
	Notes []string `json:"notes"`
}
