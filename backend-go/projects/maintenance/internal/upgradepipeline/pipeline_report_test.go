package upgradepipeline

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// TestNewBinaryIdentityCarriesCommit 钉死报告 binary 身份三项同源：
// 2026-11-07 dev 单机部署验证发现 commit 字段恒为空（Options 未传递），
// 报告无法与构建 commit 对照。
func TestNewBinaryIdentityCarriesCommit(t *testing.T) {
	identity := newBinaryIdentity(Options{AppVersion: "dev-31028f452", AppCommit: "31028f452"})
	if identity.Version != "dev-31028f452" {
		t.Fatalf("version = %q, want dev-31028f452", identity.Version)
	}
	if identity.Commit != "31028f452" {
		t.Fatalf("commit = %q, want 31028f452", identity.Commit)
	}
	if identity.SchemaRevision != contracts.SchemaRevision {
		t.Fatalf("schemaRevision = %d, want %d", identity.SchemaRevision, contracts.SchemaRevision)
	}
}
