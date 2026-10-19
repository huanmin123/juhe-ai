package accounts

import (
	"context"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// CountByProvider 按当前可见范围与能力域聚合供应商账户数（读时聚合）：供应商
// 卡片墙的「N 个账户」直接消费本结果。可见范围子句与 ListPage 一致（软删排除
// + 授权实例可见性守卫 + 本人/管理员过滤范围），只叠加 capability_domain，
// 不带列表的关键词/类型/分组/标签/状态筛选与分页——返回值恒等于该范围的
// 真实账户行数。
func (s *Store) CountByProvider(ctx context.Context, access AccessScope, domain string) (map[string]int64, error) {
	ctx = ensureCtx(ctx)
	scoped := access.ManageableID()
	authorized := s.authorizedReadableIDs(ctx, access)
	clauses := []string{
		"accounts.deleted_at IS NULL",
		"(accounts.authorization_instance_authorization_id IS NULL OR authorizations.status IN ('active', 'paused', 'expired'))",
	}
	args := []any{}
	if scoped != "" {
		if ids := authorizedIDList(authorized); len(ids) > 0 {
			clauses = append(clauses, "(accounts.system_account_id = ? OR accounts.id IN ("+placeholders(len(ids))+"))")
			args = append(args, scoped)
			args = append(args, anySlice(ids)...)
		} else {
			clauses = append(clauses, "accounts.system_account_id = ?")
			args = append(args, scoped)
		}
	}
	if domain != "" {
		clauses = append(clauses, "accounts.capability_domain = ?")
		args = append(args, domain)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT COALESCE(accounts.vendor_code, '') AS provider_code, COUNT(*)
		FROM `+s.table("accounts")+` accounts
		LEFT JOIN `+s.table("resource_authorizations")+` authorizations
			ON authorizations.id = accounts.authorization_instance_authorization_id
		WHERE `+strings.Join(clauses, " AND ")+`
		GROUP BY COALESCE(accounts.vendor_code, '')
		ORDER BY COALESCE(accounts.vendor_code, '')`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var code string
		var count int64
		if scanErr := rows.Scan(&code, &count); scanErr != nil {
			return nil, scanErr
		}
		counts[code] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}

// providerCountsHandler 镜像 listHandler 的两面 scope 选择与 domain 参数校验，
// 为添加账户供应商卡片墙输出全量分组计数（2026-10-17 读时聚合契约）。
func (d *Deps) providerCountsHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		access := selfScope(r)
		if !selfOnly {
			access = requestScope(r)
		}
		domain := strings.TrimSpace(r.URL.Query().Get("domain"))
		if _, ok := accountscore.NormalizeAccountCapabilityDomain(domain); !ok && domain != "all" {
			kernel.WriteBadRequest(w, "账户能力域过滤参数无效")
			return
		}
		if domain == "all" {
			domain = ""
		}
		counts, err := d.Store.CountByProvider(r.Context(), access, domain)
		if err != nil {
			kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
			return
		}
		setNoStoreHeaders(w)
		kernel.WriteOK(w, counts, "")
	}
}
