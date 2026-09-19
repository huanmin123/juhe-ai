package accountscore

// ConflictError maps to the Node route family 409 paths: the owner-scoped
// duplicate account name error.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// ValidationError maps to the Node 400 mutation message set surfaced through
// the body validation and repository normalization layers.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// RevisionConflictError maps to AccountManagementPatchRevisionConflictError /
// the lock config-revision CAS failures: the route family renders 409 with the
// Node copy ('账户配置已被其他操作更新，请刷新后重试' for the patch, the lock
// copies for the lock family).
type RevisionConflictError struct{ Message string }

func (e *RevisionConflictError) Error() string { return e.Message }

// RevisionConflictMessage mirrors the accounts.routes.ts PATCH catch copy.
const RevisionConflictMessage = "账户配置已被其他操作更新，请刷新后重试"
