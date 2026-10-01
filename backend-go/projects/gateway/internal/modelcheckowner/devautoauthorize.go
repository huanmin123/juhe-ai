package modelcheckowner

import (
	"context"
	"errors"
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// DevAutoLoginAccount 是 dev 自动登录回退身份的最小投影：主面
// authsys.AuthContext 携带的字段中，J3b 面的 Authorize 契约只消费
// systemAccountId 与 role，用户名等展示字段不跨过该边界。
type DevAutoLoginAccount struct {
	SystemAccountID string
	Role            string
}

// DevAutoLoginSource 按配置用户名解析 dev 自动登录账号；第二个返回值为
// false 表示未命中（账号缺失、非 active 或查询失败），调用方保持原始的
// 未认证语义，不回退。
type DevAutoLoginSource func(context.Context) (DevAutoLoginAccount, bool)

// NewDevAutoLoginAuthorize 包装 base Authorize：与 authsys.sessionMiddleware
// 的 developmentAutoLogin 同一「仅无凭证回退」语义——仅当 base 因完全无
// 凭证失败（ErrLoginRequired）且 source 命中 active 账号时回退为 dev 身份；
// requireAdmin 时角色必须为 admin/super_admin，否则返回 ErrForbidden。
// source 未命中或 base 返回其他错误（无效令牌/会话过期/DB 故障）时原样
// 返回原错误，不回退（差异：主面对「已配置但账号缺失」按 D-216 报 500，
// 此处保持 401 未登录契约）。dev 身份不做 must_change_password 拦截（与主面
// developmentAutoLogin 的 MustChangePassword=false 一致）；生产环境由
// runtime.go 的 production fail-fast 门禁禁用，此处不重复门禁。
func NewDevAutoLoginAuthorize(base Authorize, source DevAutoLoginSource, requireAdmin bool) Authorize {
	return func(ctx context.Context, request *http.Request) (string, error) {
		if base == nil {
			return "", errors.New("J3b Gateway authenticator is not initialized")
		}
		actor, err := base(ctx, request)
		if err == nil || !errors.Is(err, modelcheckauth.ErrLoginRequired) {
			return actor, err
		}
		if source == nil {
			return actor, err
		}
		account, ok := source(ctx)
		if !ok {
			return actor, err
		}
		if requireAdmin && account.Role != "admin" && account.Role != "super_admin" {
			return "", modelcheckauth.ErrForbidden
		}
		return account.SystemAccountID, nil
	}
}
