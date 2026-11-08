// T84-3（§9.4-6/§9.6）库形态指纹集成测试（pgtestkit 隔离库）：
//   - PostgresActualShapeDigest 确定性（同库两次一致）与敏感性（手工加列即变、
//     恢复后回到原值）；
//   - Run 级：账本存 actual 指纹后，手工 DDL 触发 schemaShapeDrift + stderr
//     WARN，且升级仍按当前形态收敛成功（WARN 不阻断）。
package upgradepipeline

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func openShapeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := schema.EnsurePostgres(ctx, db); err != nil {
			return err
		}
		return nil
	})
}

func TestPostgresActualShapeDigestDeterministicAndSensitive(t *testing.T) {
	db := openPipelineTestDB(t)
	ctx := context.Background()

	first, err := schema.PostgresActualShapeDigest(ctx, db)
	if err != nil {
		t.Fatalf("首次计算失败: %v", err)
	}
	second, err := schema.PostgresActualShapeDigest(ctx, db)
	if err != nil {
		t.Fatalf("二次计算失败: %v", err)
	}
	if first != second || !strings.HasPrefix(first, "sha256shape:v1:") {
		t.Fatalf("同库指纹应稳定且带 actual 前缀: %q vs %q", first, second)
	}

	// 手工 DDL：加一列 → 指纹必须变化（A 口径代码清单哈希对此不敏感）
	if _, err := db.Exec(`ALTER TABLE juhe_business.system_accounts ADD COLUMN t84_probe_col text`); err != nil {
		t.Fatalf("手工加列失败: %v", err)
	}
	defer func() {
		_, _ = db.Exec(`ALTER TABLE juhe_business.system_accounts DROP COLUMN t84_probe_col`)
	}()
	afterAdd, err := schema.PostgresActualShapeDigest(ctx, db)
	if err != nil {
		t.Fatalf("加列后计算失败: %v", err)
	}
	if afterAdd == first {
		t.Fatal("手工加列后指纹必须变化（否则对自改用户无检测价值）")
	}

	// 恢复 → 回到原值
	if _, err := db.Exec(`ALTER TABLE juhe_business.system_accounts DROP COLUMN t84_probe_col`); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	restored, err := schema.PostgresActualShapeDigest(ctx, db)
	if err != nil {
		t.Fatalf("恢复后计算失败: %v", err)
	}
	if restored != first {
		t.Fatalf("恢复后指纹应回到原值: %q vs %q", restored, first)
	}
}

// Run 级：账本先存 actual 指纹 → 手工加列 → Run（upgrade 全链）→ 报告
// schemaShapeDrift=true 且 stderr 含 WARN，升级成功不阻断。
func TestRunShapeDriftWarnsButConverges(t *testing.T) {
	handle := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := schema.EnsurePostgres(ctx, db); err != nil {
			return err
		}
		return schema.EnsureUpgradeLedger(ctx, db)
	})

	// 预置：账本存「上次升级」的 actual 指纹（fresh 库形态）
	baseline, err := schema.PostgresActualShapeDigest(context.Background(), handle.DB)
	if err != nil {
		t.Fatalf("计算基线失败: %v", err)
	}
	if err := WriteSchemaState(context.Background(), handle.DB, int64(contracts.SchemaRevision), 0, true, baseline, "test", time.Now().UTC()); err != nil {
		t.Fatalf("预置账本失败: %v", err)
	}
	// 手工 DDL（§9.6 自改用户场景）
	if _, err := handle.DB.Exec(`ALTER TABLE juhe_business.system_accounts ADD COLUMN t84_probe_col2 text`); err != nil {
		t.Fatalf("手工加列失败: %v", err)
	}

	out := Run(context.Background(), kcvRunOptions(handle.DSN), kcvTestSteps())
	if out.ExitCode != ExitOK || out.Report == nil || out.Report.Result != ResultUpgraded {
		t.Fatalf("exit=%d result=%v stderr=%s", out.ExitCode, out.Report, out.Stderr)
	}
	if !out.Report.Database.SchemaShapeDrift {
		t.Fatalf("schemaShapeDrift 应为 true（手工列未收敛进指纹基线）")
	}
	if !strings.Contains(out.Stderr, "库结构形态与账本上次记录不一致") {
		t.Fatalf("stderr 缺 WARN 文案: %s", out.Stderr)
	}
}
