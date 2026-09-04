package accounts

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// M09 route handlers: batch-edit-context, batch-update, import preview/confirm
// and export (Node account-batch-edit.routes.ts, account-import.routes.ts and
// account-export.routes.ts). The import/export handlers render every pipeline
// error as 400 with the pipeline message (the Node catch blocks); the batch
// handlers keep the typed 404/400/409 mapping. The my-* surface pins the scope
// to the caller (forceSelfAccessScope), including for admins.

// requestScopeFor mirrors the surface pair: admin routes read the scope query,
// my-* routes are force-self-scoped even for admins.
func requestScopeFor(r *http.Request, selfOnly bool) AccessScope {
	if selfOnly {
		return selfScope(r)
	}
	return requestScope(r)
}

func (d *Deps) batchEditContextHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.runBatchEditContext(w, r, requestScopeFor(r, selfOnly))
	}
}

func (d *Deps) batchUpdateHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.runBatchUpdate(w, r, requestScopeFor(r, selfOnly))
	}
}

func (d *Deps) importPreviewHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.runImportPreview(w, r, requestScopeFor(r, selfOnly))
	}
}

func (d *Deps) importConfirmHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.runImportConfirm(w, r, requestScopeFor(r, selfOnly))
	}
}

func (d *Deps) exportHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.runExportAccounts(w, r, requestScopeFor(r, selfOnly))
	}
}

func (d *Deps) runBatchEditContext(w http.ResponseWriter, r *http.Request, access AccessScope) {
	if !scopeQueryOK(r) {
		kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	accountIDs, fields, message := batchEditContextBody(body)
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	items, err := d.Store.LoadBatchEditContext(r.Context(), accountIDs, fields, access)
	if err != nil {
		d.writeError(w, err)
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, items, "")
}

func (d *Deps) runBatchUpdate(w http.ResponseWriter, r *http.Request, access AccessScope) {
	if !scopeQueryOK(r) {
		kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
		return
	}
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	input, message := batchUpdateBody(body)
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	result, err := d.Store.BatchUpdate(r.Context(), input, access)
	if err != nil {
		d.writeError(w, err)
		return
	}
	if d.Sink != nil && len(result.ChangedFields) > 0 {
		owner := result.OwnerSystemAccountID
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: owner,
			Mode:                          operationMode(access),
			Module:                        "accounts",
			Action:                        "batch_update",
			OperationKey:                  "accounts.batch_update",
			ResourceType:                  "account_batch",
			ResourceID:                    result.BatchID,
			ResourceName:                  fmt.Sprintf("%d 个 AI 账户", len(result.Items)),
			Summary:                       fmt.Sprintf("批量更新 %d 个 AI 账户", len(result.Items)),
			Changes: []authsys.OperationLogChange{
				safeChange("batchUpdateFields", "批量覆盖字段", []string{}, result.ChangedFields),
			},
			// Per-account targets ride on the Node operation-log entry; the Go
			// sink entry carries changes/viewers only (sink limitation).
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: owner, Reason: "resource_owner"},
			},
		}, r)
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, result, "")
}

func (d *Deps) runImportPreview(w http.ResponseWriter, r *http.Request, access AccessScope) {
	if !scopeQueryOK(r) {
		kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	request, ok := parseImportBody(body)
	if !ok {
		kernel.WriteBadRequest(w, "账户导入参数无效")
		return
	}
	result, err := d.Store.PreviewImport(r.Context(), request.Data, request.SourceMode, request.Options, access)
	if err != nil {
		kernel.WriteBadRequest(w, pipelineErrorMessage(err, "账户导入预览失败"))
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, result, "")
}

func (d *Deps) runImportConfirm(w http.ResponseWriter, r *http.Request, access AccessScope) {
	if !scopeQueryOK(r) {
		kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
		return
	}
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	request, ok := parseImportBody(body)
	if !ok {
		kernel.WriteBadRequest(w, "账户导入参数无效")
		return
	}
	result, err := d.Store.ExecuteImport(r.Context(), request.Data, request.SourceMode, request.Options, access)
	if err != nil {
		kernel.WriteBadRequest(w, pipelineErrorMessage(err, "账户导入失败"))
		return
	}
	if d.Sink != nil {
		owner := scopeOwnerID(access)
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: owner,
			Mode:                          operationMode(access),
			Module:                        "accounts",
			Action:                        "import",
			OperationKey:                  "accounts.import",
			ResourceType:                  "account",
			ResourceName:                  "AI 账户导入",
			Summary: fmt.Sprintf("从 %s 导入 AI 账户：创建 %d 个，跳过 %d 个，失败 %d 个",
				result.Source.Mode, result.Summary.Accounts.Create,
				result.Summary.Accounts.Skip+result.Source.Skipped, result.Summary.Accounts.Failed),
			Changes: []authsys.OperationLogChange{
				safeChange("accountCreated", "创建账户数", nil, result.Summary.Accounts.Create),
				safeChange("accountSkipped", "跳过账户数", nil, result.Summary.Accounts.Skip),
				safeChange("accountFailed", "失败账户数", nil, result.Summary.Accounts.Failed),
				safeChange("proxyCreated", "创建代理数", nil, result.Summary.Proxies.Create),
				safeChange("groupCreated", "创建分组数", nil, result.Summary.Groups.Create),
			},
			Viewers: []authsys.OperationLogViewer{
				{SystemAccountID: owner, Reason: "resource_owner"},
			},
		}, r)
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, result, "")
}

func (d *Deps) runExportAccounts(w http.ResponseWriter, r *http.Request, access AccessScope) {
	if !scopeQueryOK(r) {
		kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
		return
	}
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	parsed, ok := parseExportBody(body)
	if !ok {
		kernel.WriteBadRequest(w, fmt.Sprintf("账户导出参数无效，单次最多导出 %d 个账户", accountExportMaxAccounts))
		return
	}
	var result *ExportResult
	var err error
	if parsed.byIDs {
		result, err = d.Store.ExportAccounts(r.Context(), ExportOptions{AccountIDs: parsed.accountIDs}, access)
	} else {
		var accountIDs []string
		accountIDs, err = d.Store.CollectExportIDs(r.Context(), parsed.filters, access)
		if err == nil && len(accountIDs) == 0 {
			err = &ValidationError{Message: "当前筛选条件下没有匹配的 AI 账户"}
		}
		if err == nil {
			matched := len(accountIDs)
			result, err = d.Store.ExportAccounts(r.Context(), ExportOptions{AccountIDs: accountIDs, MatchedAccounts: &matched}, access)
		}
	}
	if err != nil {
		kernel.WriteBadRequest(w, pipelineErrorMessage(err, "导出账户失败"))
		return
	}
	if d.Sink != nil {
		owner := scopeOwnerID(access)
		matchedText := ""
		if result.Summary.MatchedAccounts != nil {
			matchedText = fmt.Sprintf("，匹配 %d 条", *result.Summary.MatchedAccounts)
		}
		changes := []authsys.OperationLogChange{
			safeChange("accountExported", "导出账户数", nil, result.Summary.Accounts),
			safeChange("proxyExported", "导出代理数", nil, result.Summary.Proxies),
			safeChange("accountSkipped", "跳过账户数", nil, result.Summary.SkippedAccounts),
		}
		if result.Summary.MatchedAccounts != nil {
			changes = append(changes, safeChange("accountMatched", "匹配账户数", nil, *result.Summary.MatchedAccounts))
		}
		entry := authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: owner,
			Mode:                          operationMode(access),
			Module:                        "accounts",
			Action:                        "export",
			OperationKey:                  "accounts.export",
			ResourceType:                  "account",
			ResourceName:                  "AI 账户导出",
			Summary: fmt.Sprintf("导出 AI 账户：%d 个账户，%d 个代理%s",
				result.Summary.Accounts, result.Summary.Proxies, matchedText),
			Changes: changes,
		}
		// Node records admin exports admin_only without viewers; user exports
		// carry the owner viewer.
		if !access.IsAdmin {
			entry.Viewers = []authsys.OperationLogViewer{
				{SystemAccountID: owner, Reason: "resource_owner"},
			}
		}
		d.Sink.Record(entry, r)
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, result, "")
}

// pipelineErrorMessage mirrors the import/export catch blocks: any pipeline
// error renders as 400 with the error message.
func pipelineErrorMessage(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return fallback
	}
	return message
}
