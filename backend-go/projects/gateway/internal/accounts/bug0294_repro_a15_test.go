// BUG-0294 / A15 红绿回归（由最小复现定性载体转正）。
//
// 档案：docs/bug/问题-0294-迁移期行为存疑按现状断言未建档清点.md A15。
// 原注释：w2_import_test.go:584（关联 w10a_import_arms_test.go:128
// 「skip 臂数学不可达」）。
// 现象：skipDuplicates=true 时，对已存在重名账户本应记 skip（Node 契约，
// docs/bug/问题-0162-M08账户迁移遗漏端点与运行态副作用.md 第 277 行：
// 「遇到名称重复且 skipDuplicates 开启则记为 skip，否则记为 failed」），
// 实际落 failed（0291 同类：写语义静默失效）。
//
// 定性结论：确认缺陷，已修复（最小层级：执行层分类）。
// 机制（修复后）：accountstransfer/import.go 执行器的 skip 判定除保留既有
// 原始 UNIQUE 错误分类外，新增对 *accountscore.ConflictError 的判定——
// Create（write.go duplicateAccountNameError）在写入口把 UNIQUE 冲突预转为
// ConflictError 是 write/patch 链共用的既定契约，不改；执行层
// errors.As 识别该预转结果后 skip 臂可达。CreateAccount 路径上的
// ConflictError 只可能是账户重名（代理/分组重名的 ConflictError 由 import
// 自身创建臂产生，不经过该判定）。
// 端到端 skip 臂另见 w10a_import_arms_test.go TestW10AImportDuplicateAccountSkipAndFail
// 与 w2_import_test.go 的 HTTP 重放断言（本批同步转正）。
package accounts

import (
	"context"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// TestBug0294A15DuplicateNameSkipsWhenSkipDuplicates 断言 skipDuplicates 开启
// 时重名账户经真实 ExecuteImport 链记 skip（修复前恒 failed）。
func TestBug0294A15DuplicateNameSkipsWhenSkipDuplicates(t *testing.T) {
	env, adminID := w10aImportEnv(t)
	env.seedM11Account(t, "acc-bug0294-a15", adminID, "A15重复名账户", "api_key", "active",
		Credentials{"api_key": "sk-existing"})

	document := map[string]any{
		"type": "sub2api-export", "version": float64(2), "exported_at": "2026-01-01T00:00:00Z",
		"accounts": []any{
			map[string]any{"name": "A15重复名账户", "platform": "OpenAI", "type": "api_key",
				"credentials": map[string]any{"api_key": "sk-bug0294-a15", "base_url": "https://api.openai.com/v1"}},
		},
	}

	result, err := env.store.ExecuteImport(context.Background(), document, importSourceSub2Api,
		ImportOptions{SkipDuplicates: boolPtrW10(true)}, AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil {
		t.Fatalf("ExecuteImport 错误：%v", err)
	}
	if result.Summary.Accounts.Skip != 1 {
		t.Fatalf("skipDuplicates=true 时重名应记 skip：%+v", result.Summary)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionSkip {
		t.Fatalf("账户项应跳过：%+v", result.Accounts)
	}
	// 重名不落库：跳过语义无写入副作用。
	if count := env.count(t, `SELECT COUNT(*) FROM accounts WHERE name = 'A15重复名账户'`); count != 1 {
		t.Fatalf("重名不应创建第二个同名账户：%d", count)
	}
}

// TestBug0294A15ConflictClassificationChain 钉住两级分类契约：
// 第一级（Create 写入口）把原始 UNIQUE 冲突预转为 ConflictError；
// 第二级（执行器）以 errors.As 识别该预转结果——这正是修复引入的判定，
// 旧的 DuplicateAccountNameError 文本分类对转换后错误恒 nil（保留断言
// 说明执行层为何必须识别 ConflictError）。
func TestBug0294A15ConflictClassificationChain(t *testing.T) {
	name := "导入账户"

	// 第一级（Create 内部）：原始唯一约束错误被识别并转为 ConflictError。
	raw := errors.New(`pq: duplicate key value violates unique constraint "idx_accounts_owner_name_unique"`)
	converted := accountscore.DuplicateAccountNameError(raw, name)
	if converted == nil {
		t.Fatal("第一级：原始 UNIQUE 冲突必须被识别为重名冲突（Create 预转契约）")
	}

	// 文本分类对已转换错误恒 nil（函数语义未变，执行层不能依赖它识别预转结果）。
	if again := accountscore.DuplicateAccountNameError(converted, name); again != nil {
		t.Fatalf("文本分类对已转换 ConflictError 应返回 nil，实际 %v", again)
	}

	// 第二级（执行器，修复引入）：errors.As 命中预转的 *ConflictError。
	var nameConflict *accountscore.ConflictError
	if !errors.As(converted, &nameConflict) {
		t.Fatal("执行层必须能经 errors.As 识别 Create 预转的 *ConflictError")
	}
}
