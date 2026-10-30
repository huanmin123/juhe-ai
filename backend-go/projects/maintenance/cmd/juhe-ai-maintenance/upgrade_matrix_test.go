// 升级矩阵（升级兼容与迁移账本设计 §15，P4）：对每个指定 tag——
// 归档该 tag 的 backend-go 树 → 临时构建**该版本** maintenance 二进制 →
// 在 pgtestkit 隔离空库执行该版的 --ensure-schema --seed（真实旧版 DDL/种子，
// §15.1 v2 口径：tag 归档即夹具，替代 pg_dump 方案——快照逆向不可行见 D14）
// → 运行 HEAD 的 upgrade 全量编排 → 断言 upgraded/账本/状态版本 → 重跑短路。
//
// 触发方式：JUHE_AI_UPGRADE_MATRIX_TAGS=v1.0.0,v1.1.0（逗号分隔；缺省 SKIP），
// 外部真库授权门 pgtestkit.SkipUnlessExternalAuthorized；推荐经
// scripts/upgrade-matrix.sh 调用。tag 不足 3 个时对现有 tag 全量执行（§16.3
// 冷启动口径）；首个 tag 之前本测试 SKIP，升级路径由 P1-P3 验收测试覆盖。
//
// 边界（§15.2 v2 口径）：gateway/jobs 启动与登录冒烟不在矩阵内——那需要完整
// Redis/env 装配，由部署后 verify-release 与既有验收链覆盖；本矩阵钉住数据面
// （结构 + 账本 + 编排收敛）。
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const matrixTagsEnv = "JUHE_AI_UPGRADE_MATRIX_TAGS"

// TestUpgradeMatrixFromArchivedTags 是矩阵主入口（env 门控触发）。
func TestUpgradeMatrixFromArchivedTags(t *testing.T) {
	pgtestkit.SkipUnlessExternalAuthorized(t)
	tagsValue := strings.TrimSpace(os.Getenv(matrixTagsEnv))
	if tagsValue == "" {
		t.Skipf("未设置 %s（逗号分隔 tag 清单），矩阵跳过；首个 tag 发布后由 scripts/upgrade-matrix.sh 触发", matrixTagsEnv)
	}
	const matrixSecret = "upgrade-matrix-secret"
	mockdata := os.Getenv("JUHE_AI_UPGRADE_MATRIX_MOCKDATA") == "1"
	for _, tag := range strings.Split(tagsValue, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		t.Run(tag, func(t *testing.T) {
			runMatrixForRef(t, tag, matrixSecret, mockdata)
		})
	}
}

// TestUpgradeMatrixHeadSelfFixture 是矩阵机制的常绿自检（外部授权即跑）：
// 以 HEAD 归档为「旧版夹具」，验证归档构建 → 旧版 ensure+seed → HEAD upgrade
// 的全链路机制；tag 化后由 TestUpgradeMatrixFromArchivedTags 承担真实矩阵。
func TestUpgradeMatrixHeadSelfFixture(t *testing.T) {
	pgtestkit.SkipUnlessExternalAuthorized(t)
	runMatrixForRef(t, "HEAD", "upgrade-matrix-secret", false)
}

// runMatrixForRef 对单个 ref 执行：归档构建 → 空库旧版初始化 → HEAD upgrade
// 三段断言（§15.2 数据面）。
func runMatrixForRef(t *testing.T, ref, matrixSecret string, mockdata bool) {
	t.Helper()
	workDir := t.TempDir()
	oldBinary := buildArchivedMaintenance(t, ref, workDir)

	td := pgtestkit.NewTestDatabase(t)
	runOldMaintenance(t, oldBinary, "--ensure-schema", "--dsn", td.DSN)
	runOldMaintenance(t, oldBinary, "--seed", "--dsn", td.DSN, "--secret", matrixSecret)
	if mockdata {
		runOldMaintenance(t, oldBinary, "--mockdata", "--dsn", td.DSN, "--secret", matrixSecret)
	}

	opts := upgradepipeline.Options{
		PrimaryDSN:         td.DSN,
		Secret:             matrixSecret,
		StopWriteConfirmed: true,
		BackupConfirmed:    true,
		StepTimeout:        4 * time.Minute,
		AppVersion:         "HEAD-matrix",
	}
	steps := func() []upgradepipeline.Step { return upgradeSteps(td.DSN, matrixSecret) }

	// 预演：库落后于 HEAD → blocked/3（夹具状态越旧 pending 越多）。
	dryOpts := opts
	dryOpts.DryRun = true
	out := upgradepipeline.Run(context.Background(), dryOpts, steps())
	if out.ExitCode != upgradepipeline.ExitNeedsHuman || out.Report == nil || out.Report.Result != upgradepipeline.ResultBlocked {
		t.Fatalf("[%s] dry-run 应 blocked/3，got code=%d result=%v", ref, out.ExitCode, reportResult(out.Report))
	}

	// 正式升级：upgraded/0，postflight 通过。
	out = upgradepipeline.Run(context.Background(), opts, steps())
	if out.ExitCode != upgradepipeline.ExitOK || out.Report == nil || out.Report.Result != upgradepipeline.ResultUpgraded {
		t.Fatalf("[%s] 升级应 upgraded/0，got code=%d result=%v stderr=%s",
			ref, out.ExitCode, reportResult(out.Report), out.Stderr)
	}

	// 账本与状态版本。
	var applied int
	if err := td.DB.QueryRowContext(context.Background(),
		`SELECT count(*) FROM juhe_business.schema_migrations WHERE status = 'applied'`).Scan(&applied); err != nil {
		t.Fatalf("[%s] 读账本失败: %v", ref, err)
	}
	var stateRevision int64
	if err := td.DB.QueryRowContext(context.Background(),
		`SELECT schema_revision FROM juhe_business.schema_state WHERE id = 1`).Scan(&stateRevision); err != nil {
		t.Fatalf("[%s] 读 schema_state 失败: %v", ref, err)
	}
	if applied != 12 || stateRevision != contracts.SchemaRevision {
		t.Fatalf("[%s] 账本 applied=%d（want 12）/ state revision=%d（want %d）",
			ref, applied, stateRevision, contracts.SchemaRevision)
	}

	// 重跑短路：up-to-date/0。
	out = upgradepipeline.Run(context.Background(), opts, steps())
	if out.ExitCode != upgradepipeline.ExitOK || out.Report == nil || out.Report.Result != upgradepipeline.ResultUpToDate {
		t.Fatalf("[%s] 重跑应 up-to-date/0，got code=%d result=%v",
			ref, out.ExitCode, reportResult(out.Report))
	}
}

// repoRootForMatrix 从包目录向上定位仓库根（.git）。
func repoRootForMatrix(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("未找到仓库根（.git）")
	return ""
}

// buildArchivedMaintenance 把 ref 的 backend-go 树归档到临时目录并构建该版
// maintenance 二进制（相对 replace 指令随目录结构保留，模块缓存热命中离线
// 可构建；临时目录由 t.TempDir 清理，不产生任何仓库内副本）。
func buildArchivedMaintenance(t *testing.T, ref, workDir string) string {
	t.Helper()
	tarPath := filepath.Join(workDir, "archive.tar")
	gitCmd := exec.Command("git", "archive", "--format=tar", ref, "backend-go")
	gitCmd.Dir = repoRootForMatrix(t)
	tarFile, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	gitCmd.Stdout = tarFile
	if err := gitCmd.Run(); err != nil {
		tarFile.Close()
		t.Fatalf("git archive %s 失败（tag 不存在或树不可归档）: %v", ref, err)
	}
	tarFile.Close()

	extractDir := filepath.Join(workDir, "src")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tarCmd := exec.Command("tar", "-xf", "../archive.tar")
	tarCmd.Dir = extractDir
	if output, tarErr := tarCmd.CombinedOutput(); tarErr != nil {
		t.Fatalf("解档 %s 失败: %v\n%s", ref, tarErr, output)
	}

	moduleDir := filepath.Join(extractDir, "backend-go", "projects", "maintenance")
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	binary := filepath.Join(workDir, "maintenance-"+strings.ReplaceAll(ref, "/", "_")+suffix)
	build := exec.Command("go", "build", "-o", binary, "./cmd/juhe-ai-maintenance")
	build.Dir = moduleDir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建 %s 版 maintenance 失败: %v\n%s", ref, err, output)
	}
	return binary
}

// runOldMaintenance 用旧版二进制对夹具库执行一次性初始化命令。
func runOldMaintenance(t *testing.T, binary string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("旧版 maintenance %v 失败: %v\n%s", args, err, output)
	}
}

var _ = fmt.Sprintf
