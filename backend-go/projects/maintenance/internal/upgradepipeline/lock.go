// 会话级 advisory 锁与输出捕获（§9.5 / §7.2 detail 捕获）。
package upgradepipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/huanminabc/juhe-ai/backend-go-platform/advisorylock"
)

// lockWaitTimeout 与 AccountListDirty 的 2s 快失败口径一致（BUG-0313 教训：
// 持锁者饿死等待方时快速失败本轮，由操作者重试）。
const lockWaitTimeout = "2s"

// AcquireOrchestrationLock 是 acquireUpgradeLock 的公开口：--ensure-schema /
// --seed（runStorageBootstrap）与 upgrade 以同一把锁互斥（§9.5）。
func AcquireOrchestrationLock(ctx context.Context, db *sql.DB) (func(), error) {
	return acquireUpgradeLock(ctx, db)
}

// acquireUpgradeLock 取会话级 advisory 锁（advisorylock.UpgradeOrchestration），
// 全程持有直到 release；等待超过 2s 视为有并发编排/结构变更在进行，快失败。
// 锁绑定在独立连接上（pg_advisory_lock 为会话锁，连接归还即释放）。
//
// 失败分类：仅「锁等待超时（55P03）」按竞争失败上抛（exit 1）；连接不可达/
// 连接串配置错误返回「无锁继续」——连接坏了后续操作必然以原始错误失败
// （既有 stderr 契约如「postgres ensure-schema 失败：… sslmode is invalid」
// 由 w1_cli 测试钉死），锁层不得吞换报错形态，也就不存在无锁并发的实际窗口。
func acquireUpgradeLock(ctx context.Context, db *sql.DB) (func(), error) {
	noop := func() {}
	conn, err := db.Conn(ctx)
	if err != nil {
		return noop, nil
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET lock_timeout = '%s'", lockWaitTimeout)); err != nil {
		conn.Close()
		if isLockTimeout(err) {
			return nil, fmt.Errorf("获取升级编排锁失败（等待超过 %s，可能有并发 upgrade/ensure-schema）: %w", lockWaitTimeout, err)
		}
		return noop, nil
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisorylock.UpgradeOrchestration); err != nil {
		conn.Close()
		if isLockTimeout(err) {
			return nil, fmt.Errorf("获取升级编排锁失败（等待超过 %s，可能有并发 upgrade/ensure-schema）: %w", lockWaitTimeout, err)
		}
		return noop, nil
	}
	release := func() {
		// 会话结束锁自动释放；显式 unlock 尽力而为，连接关闭兜底。
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", advisorylock.UpgradeOrchestration)
		conn.Close()
	}
	return release, nil
}

// isLockTimeout 判定 PG 55P03（lock_not_available）。
func isLockTimeout(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "55P03"
	}
	return false
}

// CaptureOutput 在 fn 执行期间捕获 stdout/stderr（步骤 Apply 复用既有
// *Result 函数，那些函数直接向 os.Stdout 打报告；捕获后进 detail_json，
// 保证 upgrade 自身的「stdout 单行 JSON 报告」契约不被污染）。
func CaptureOutput(fn func()) (stdout, stderr string) {
	savedStdout, savedStderr := os.Stdout, os.Stderr
	stdoutRead, stdoutWrite, _ := os.Pipe()
	stderrRead, stderrWrite, _ := os.Pipe()
	os.Stdout, os.Stderr = stdoutWrite, stderrWrite
	defer func() {
		os.Stdout, os.Stderr = savedStdout, savedStderr
	}()
	doneOut := make(chan string, 1)
	doneErr := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(stdoutRead)
		doneOut <- string(out)
	}()
	go func() {
		out, _ := io.ReadAll(stderrRead)
		doneErr <- string(out)
	}()
	defer func() {
		stdoutWrite.Close()
		stderrWrite.Close()
	}()
	fn()
	stdoutWrite.Close()
	stderrWrite.Close()
	return <-doneOut, <-doneErr
}
