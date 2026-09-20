package main

// compose_accounts_authorized_wiring_test.go —— accounts Deps.Authorized 的
// 组合根接线断言（M10 authorized-instance read hookup，compose.go Mount 段）。
//
// 接线事实：authz.Store.AuthorizedReadableAccountIDs(ctx, viewerSystemAccountID)
// (map[string]bool, error)（internal/authz/authorized_reads.go）与
// accounts.AuthorizedAccountReader（= accountscore.AuthorizedAccountReader，
// internal/accounts/authorized.go 别名）逐参一致，组合根直接注入
// *authz.Store，无需适配器。此前 Authorized 恒零值 → Mount 内
// Store.SetAuthorizedReader(nil)，被授权人视角的实例账户投影整体缺失；
// 管理员路径经 authorizedReadableIDs 的 CanAccessAll 短路，不受影响。
//
// 行为级覆盖：authz 侧 authorized_reads_test.go /
// instance_provision_test.go、accounts 侧 m10_authorized_test.go（包内
// fake reader 驱动 list/detail 投影）保持绿即为本端口的契约验证；本文件
// 只锁「组合根注入的实现就是 authz 生产 store」这一编译期事实。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

var _ accounts.AuthorizedAccountReader = (*authz.Store)(nil)

func TestAuthzStoreSatisfiesAccountsAuthorizedReader(t *testing.T) {
	var store any = &authz.Store{}
	if _, ok := store.(accounts.AuthorizedAccountReader); !ok {
		t.Fatal("*authz.Store 必须结构性满足 accounts.AuthorizedAccountReader（组合根直接注入的契约）")
	}
}
