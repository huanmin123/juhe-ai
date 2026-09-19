package accountscore

// AccessScope mirrors storage/access-scope.ts for the accounts slice: admins
// see every row unless a systemAccountId filter narrows the view; users are
// pinned to their own rows (forceSelfAccessScope).
type AccessScope struct {
	ViewerID string
	IsAdmin  bool
	FilterID string
}

// ManageableID mirrors manageableSystemAccountId: admins pass the filter
// through (possibly empty = unscoped), non-admins are pinned to themselves.
func (a AccessScope) ManageableID() string {
	if a.IsAdmin {
		return a.FilterID
	}
	return a.ViewerID
}

func (a AccessScope) CanAccessAll() bool { return a.IsAdmin }

// EffectiveViewerID mirrors userVisibleSystemAccountId: the filter for scoped
// admins, the caller otherwise. (原根包私有方法 viewerID；因与 ViewerID 字段
// 导出后同名冲突，改为 EffectiveViewerID。)
func (a AccessScope) EffectiveViewerID() string {
	if id := a.ManageableID(); id != "" {
		return id
	}
	return a.ViewerID
}

// OwnerID mirrors writeSystemAccountId: the account stamped on newly created
// rows (explicit group ownership may override it in Create).
func (a AccessScope) OwnerID() (string, error) {
	if a.ViewerID != "" {
		return a.ViewerID, nil
	}
	return "", &ValidationError{Message: "缺少系统账户上下文"}
}
