package accountscore

// ConflictError maps to the Node route family 409 paths: the owner-scoped
// duplicate account name error.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// ValidationError maps to the Node 400 mutation message set surfaced through
// the body validation and repository normalization layers.
type ValidationError struct {
	Message string
	// Code 非空时作为稳定错误码随 {code,message} 返回（超级分组禁止直接
	// 绑定账户：group_kind_conflict）。
	Code string
}

func (e *ValidationError) Error() string { return e.Message }

// RevisionConflictError maps to AccountManagementPatchRevisionConflictError /
// the lock config-revision CAS failures: the route family renders 409 with the
// Node copy ('账户配置已被其他操作更新，请刷新后重试' for the patch, the lock
// copies for the lock family).
type RevisionConflictError struct{ Message string }

func (e *RevisionConflictError) Error() string { return e.Message }

// 代理池写入面错误码（代理池设计 §8.6，ValidationError.Code 随 {code,message}
// 400 返回）。门面与 accountstransfer 批量/导入域共用，避免字面量分叉。
const (
	// ProxyTargetConflictCode: proxyProfileId 与 proxyPoolId 同时非空（400）。
	ProxyTargetConflictCode = "proxy_target_conflict"
	// ProxyPoolNotFoundCode: 池不存在（400，路由族按 ValidationError 渲染）。
	ProxyPoolNotFoundCode = "proxy_pool_not_found"
	// ProxyPoolDisabledCode: 池停用（400）。
	ProxyPoolDisabledCode = "proxy_pool_disabled"
	// ProxyPoolEmptyCode: 池无 enabled 成员（400）。
	ProxyPoolEmptyCode = "proxy_pool_empty"
)

// RevisionConflictMessage mirrors the accounts.routes.ts PATCH catch copy.
const RevisionConflictMessage = "账户配置已被其他操作更新，请刷新后重试"
