package accountscore

// PatchChange mirrors AccountManagementPatchChange (REFACTOR-0005 阶段 B 下沉：
// 运行时重置子域的审计日志与门面 PATCH 面共用同一变更载荷类型).
type PatchChange struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}
