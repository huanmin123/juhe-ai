package aipublic

// SEC-002 回归：外部集成面不得把内置超级管理员当作 target。
// requirePublicTarget 与 ensureTargetSystemAccount 共用该判定。

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

func TestRejectPublicSuperAdminTarget(t *testing.T) {
	for _, account := range []authsys.AccountSummary{
		{ID: "sys_admin", Username: "admin", Role: "super_admin"},
		{ID: "sysacc_custom", Username: "root", Role: "super_admin"},
	} {
		err := rejectPublicSuperAdminTarget(account)
		if err == nil || !strings.Contains(err.Error(), "超级管理员") {
			t.Fatalf("super admin target %+v must be rejected, got %v", account, err)
		}
	}
	for _, account := range []authsys.AccountSummary{
		{ID: "sysacc_user", Username: "alice", Role: "user"},
		{ID: "sysacc_admin", Username: "ops", Role: "admin"},
	} {
		if err := rejectPublicSuperAdminTarget(account); err != nil {
			t.Fatalf("target %+v must stay allowed, got %v", account, err)
		}
	}
}
