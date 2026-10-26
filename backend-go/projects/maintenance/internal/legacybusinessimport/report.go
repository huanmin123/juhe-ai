package legacybusinessimport

// Reports the outcome of one import run. Every counter is derived from rows
// that were actually written, so a reviewer can diff the report against the
// source database before declaring the cutover complete.
type Report struct {
	Mode string `json:"mode"`
	// SourceIdentity/TargetIdentity are current_database() values, recorded so
	// a report can never be mistaken for a different environment's cutover.
	SourceIdentity string `json:"source_identity"`
	TargetIdentity string `json:"target_identity"`
	SeededVendors  int    `json:"seeded_vendors"`
	// Tables maps a target table to the number of rows written.
	Tables map[string]int `json:"tables"`
	// DroppedDomains lists the legacy domains intentionally not carried over.
	DroppedDomains []string `json:"dropped_domains"`
	// LegacyProfiles maps every legacy provider_protocol_profile_id seen on a
	// migrated account to the connection preset it was translated into.
	LegacyProfiles map[string]string `json:"legacy_profiles"`
	// UnresolvedEndpointModes lists account IDs whose credential JSON carried
	// no usable supported_endpoint_modes. Such an account is migrated with the
	// preset vocabulary fallback and reported for review.
	UnresolvedEndpointModes []string `json:"unresolved_endpoint_modes"`
	// CreatedAccounts counts accounts whose legacy row had no deleted_at
	// (active resource accounts).
	CreatedAccounts int `json:"created_accounts"`
	// Notes records normalisations applied while translating legacy rows.
	//
	// T60（2026-10-25）：已删除 ImportedAuthorizations / SkippedDeletedAcocunts /
	// ConvertedHybridAccounts 三个恒 0 统计字段及其 stdout JSON 键
	// （imported_authorizations / skipped_deleted_accounts /
	// converted_hybrid_accounts）——全仓无赋值点，脚本 / 文档 / 前端 / 测试
	// 对这些键零解析，运维复核契约由其余字段承载。
	Notes []string `json:"notes,omitempty"`
}

// Ready reports whether the import committed without leaving any row it could
// not translate.
func (r Report) Ready() bool {
	return r.SourceIdentity != "" && r.TargetIdentity != "" && len(r.UnresolvedEndpointModes) == 0
}
