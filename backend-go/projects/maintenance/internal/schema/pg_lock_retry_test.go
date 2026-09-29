// Tests alongside pg_lock_retry.go：瞬时锁冲突 SQLSTATE 分类、退避序列、
// 语句摘要截断、执行器重试行为（40P01 恢复 / 耗尽保留原始错误 / 非锁冲突
// 不重试 / 退避取消不吞错），以及 EnsurePostgres 全流程下的中段语句重试
//（重试不重启流程、不重复已执行语句）。全部通过 wm_pg_fake_test.go 的
// 录制驱动注入错误序列，无需真实 PostgreSQL。

package schema

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// wmPGErr 构造一个指定 SQLSTATE 的 *pgconn.PgError。
func wmPGErr(code, message string) error {
	return &pgconn.PgError{Severity: "ERROR", Code: code, Message: message}
}

// wmRecordRetrySleeps 把 pgLockRetrySleep 替换为记录退避时长并立即成功的
// 桩（消除真实等待），返回还原函数。
func wmRecordRetrySleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	delays := &[]time.Duration{}
	original := pgLockRetrySleep
	pgLockRetrySleep = func(_ context.Context, delay time.Duration) error {
		*delays = append(*delays, delay)
		return nil
	}
	t.Cleanup(func() { pgLockRetrySleep = original })
	return delays
}

// wmCaptureStderr 捕获 os.Stderr 输出，返回累积器与还原函数；还原后累积
// 内容完整可用。
func wmCaptureStderr(t *testing.T) (*strings.Builder, func()) {
	t.Helper()
	original := os.Stderr
	reader, writer, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	os.Stderr = writer
	captured := &strings.Builder{}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(captured, reader)
		close(done)
	}()
	restore := func() {
		os.Stderr = original
		_ = writer.Close()
		// 先等 io.Copy 排干到 EOF 再关读端：提前 Close 读端会中断阻塞中的
		// 读取并丢失管道内未消费数据。
		<-done
		_ = reader.Close()
	}
	t.Cleanup(restore)
	return captured, restore
}

// TestPGTransientLockSQLStateClassification 锁定错误分类：仅 40P01/55P03/
// 57014 判定为可重试瞬时锁冲突，其余 PgError、普通错误与包装链均不可重试
// （包装的锁冲突 PgError 必须穿透 errors.As 识别）。
func TestPGTransientLockSQLStateClassification(t *testing.T) {
	deadlock := wmPGErr("40P01", "deadlock detected")
	cases := []struct {
		name      string
		err       error
		wantState string
		wantRetry bool
	}{
		{name: "deadlock detected", err: deadlock, wantState: "40P01", wantRetry: true},
		{name: "lock not available", err: wmPGErr("55P03", "lock not available"), wantState: "55P03", wantRetry: true},
		{name: "statement timeout", err: wmPGErr("57014", "canceling statement due to statement timeout"), wantState: "57014", wantRetry: true},
		{name: "syntax error", err: wmPGErr("42601", "syntax error"), wantRetry: false},
		{name: "unique violation", err: wmPGErr("23505", "duplicate key"), wantRetry: false},
		{name: "serialization failure", err: wmPGErr("40001", "could not serialize"), wantRetry: false},
		{name: "plain error", err: errors.New("wm schema fake: 注入执行失败"), wantRetry: false},
		{name: "joined errors without pg error", err: errors.Join(errors.New("a"), errors.New("b")), wantRetry: false},
		{name: "wrapped deadlock detected", err: fmt.Errorf("postgres schema statement 1 (juhe_business/x.ts): %w", deadlock), wantState: "40P01", wantRetry: true},
		{name: "wrapped twice deadlock detected", err: fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", deadlock)), wantState: "40P01", wantRetry: true},
	}
	for _, testCase := range cases {
		state, retry := pgTransientLockSQLState(testCase.err)
		if retry != testCase.wantRetry || state != testCase.wantState {
			t.Fatalf("%s：pgTransientLockSQLState = (%q, %v), want (%q, %v)", testCase.name, state, retry, testCase.wantState, testCase.wantRetry)
		}
	}
}

// TestPGLockRetryDelaySequence 锁定指数退避序列：1s、2s、4s。
func TestPGLockRetryDelaySequence(t *testing.T) {
	for attempt, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		if got := pgLockRetryDelay(attempt); got != want {
			t.Fatalf("pgLockRetryDelay(%d) = %v, want %v", attempt, got, want)
		}
	}
}

// TestPGStatementLogSummary 锁定重试日志摘要：折叠多行空白为单行、按前
// 80 个字符截断并追加省略号。
func TestPGStatementLogSummary(t *testing.T) {
	multiline := "CREATE OR REPLACE FUNCTION\n  juhe_business.some_fn()\n  RETURNS trigger"
	if got := pgStatementLogSummary(multiline); got != "CREATE OR REPLACE FUNCTION juhe_business.some_fn() RETURNS trigger" {
		t.Fatalf("多行折叠错误: %q", got)
	}
	long := strings.Repeat("x", 100)
	got := pgStatementLogSummary(long)
	if got != strings.Repeat("x", 80)+"…" {
		t.Fatalf("超长截断错误: %d 字符", len(got))
	}
	if got := pgStatementLogSummary(strings.Repeat("y", 80)); got != strings.Repeat("y", 80) {
		t.Fatalf("恰好 80 字符不应截断: %q", got)
	}
}

// TestExecPostgresStatementWithLockRetryRecoversTransientDeadlock 覆盖
// 40P01 后成功的恢复路径：重试一次成功、退避为 1s、日志含 SQLSTATE、重试
// 序号与语句摘要。
func TestExecPostgresStatementWithLockRetryRecoversTransientDeadlock(t *testing.T) {
	stderr, restoreStderr := wmCaptureStderr(t)
	delays := wmRecordRetrySleeps(t)
	rec := &wmSchemaRecorder{failNextExecsWith: []error{wmPGErr("40P01", "deadlock detected")}}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()

	const statement = "CREATE OR REPLACE FUNCTION juhe_business.demo_fn() RETURNS trigger"
	execSQL := "SET search_path TO \"juhe_business\", public;\n" + statement
	if err := execPostgresStatementWithLockRetry(context.Background(), db, execSQL, statement); err != nil {
		t.Fatalf("瞬时死锁重试后应成功: %v", err)
	}
	if got := rec.execAttemptCount(); got != 2 {
		t.Fatalf("执行尝试次数=%d, want 2（首次失败 + 重试成功）", got)
	}
	if got := rec.execCount(); got != 1 {
		t.Fatalf("成功执行次数=%d, want 1", got)
	}
	if len(*delays) != 1 || (*delays)[0] != time.Second {
		t.Fatalf("退避序列=%v, want [1s]", *delays)
	}
	restoreStderr()
	logLine := stderr.String()
	for _, fragment := range []string{"SQLSTATE=40P01", "第 1/3 次", "1s", "CREATE OR REPLACE FUNCTION juhe_business.demo_fn() RETURNS trigger"} {
		if !strings.Contains(logLine, fragment) {
			t.Fatalf("重试日志缺少片段 %q: %q", fragment, logLine)
		}
	}
}

// TestExecPostgresStatementWithLockRetryExhaustsAndPreservesOriginalError
// 覆盖重试耗尽：4 次 40P01（首次 + 3 次重试）后上抛的仍是原始 PgError，
// 退避序列为 1s/2s/4s。
func TestExecPostgresStatementWithLockRetryExhaustsAndPreservesOriginalError(t *testing.T) {
	delays := wmRecordRetrySleeps(t)
	rec := &wmSchemaRecorder{failNextExecsWith: []error{
		wmPGErr("40P01", "deadlock detected"),
		wmPGErr("40P01", "deadlock detected"),
		wmPGErr("40P01", "deadlock detected"),
		wmPGErr("40P01", "deadlock detected"),
	}}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()

	err := execPostgresStatementWithLockRetry(context.Background(), db, "SELECT 1", "SELECT 1")
	if err == nil {
		t.Fatal("重试耗尽后必须上抛错误")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40P01" || pgErr.Message != "deadlock detected" {
		t.Fatalf("上抛的错误必须是原始 40P01 PgError: %v", err)
	}
	if got := rec.execAttemptCount(); got != 4 {
		t.Fatalf("执行尝试次数=%d, want 4（首次 + 3 次重试）", got)
	}
	if got := rec.execCount(); got != 0 {
		t.Fatalf("成功执行次数=%d, want 0", got)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if len(*delays) != len(want) {
		t.Fatalf("退避序列=%v, want %v", *delays, want)
	}
	for i, delay := range want {
		if (*delays)[i] != delay {
			t.Fatalf("退避序列=%v, want %v", *delays, want)
		}
	}
}

// TestExecPostgresStatementWithLockRetrySkipsNonLockErrors 覆盖非锁冲突
// 错误不重试：无论普通错误还是非锁 SQLSTATE 的 PgError，都只执行一次并
// 原样上抛。
func TestExecPostgresStatementWithLockRetrySkipsNonLockErrors(t *testing.T) {
	delays := wmRecordRetrySleeps(t)
	for name, injected := range map[string]error{
		"plain error":        errors.New("wm schema fake: 注入执行失败"),
		"syntax error pgerr": wmPGErr("42601", "syntax error"),
		"unique violation":   wmPGErr("23505", "duplicate key value"),
	} {
		rec := &wmSchemaRecorder{failNextExecsWith: []error{injected, errors.New("不可达的第二次失败")}}
		db := openWMSchemaFakeDB(rec)
		err := execPostgresStatementWithLockRetry(context.Background(), db, "SELECT 1", "SELECT 1")
		db.Close()
		if err == nil || !strings.Contains(err.Error(), injected.Error()) {
			t.Fatalf("%s：必须原样上抛注入错误: %v", name, err)
		}
		if got := rec.execAttemptCount(); got != 1 {
			t.Fatalf("%s：执行尝试次数=%d, want 1（不重试）", name, got)
		}
		if got := rec.remainingInjectedFailures(); got != 1 {
			t.Fatalf("%s：剩余注入错误=%d, want 1（未消费第二次注入）", name, got)
		}
	}
	if len(*delays) != 0 {
		t.Fatalf("非锁冲突不应产生退避: %v", *delays)
	}
}

// TestExecPostgresStatementWithLockRetryBackoffCancelReturnsOriginalError
// 覆盖退避等待期间 ctx 取消：返回原始语句错误而不是 ctx.Err()（不吞错）。
// ctx 在首次执行发出时必须是活的（预取消的 ctx 会让 database/sql 直接返回
// context.Canceled，走不到重试分支），因此由退避桩在等待时取消 ctx。
func TestExecPostgresStatementWithLockRetryBackoffCancelReturnsOriginalError(t *testing.T) {
	rec := &wmSchemaRecorder{failNextExecsWith: []error{wmPGErr("40P01", "deadlock detected")}}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := pgLockRetrySleep
	pgLockRetrySleep = func(context.Context, time.Duration) error {
		cancel()
		return ctx.Err()
	}
	t.Cleanup(func() { pgLockRetrySleep = original })

	err := execPostgresStatementWithLockRetry(ctx, db, "SELECT 1", "SELECT 1")
	if err == nil {
		t.Fatal("ctx 取消时必须返回错误")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40P01" {
		t.Fatalf("必须返回原始 40P01 语句错误而非 ctx 错误: %v", err)
	}
}

// TestEnsurePostgresRetriesMidFlowDeadlockAndContinues 覆盖全流程中段语句
// 死锁重试：第 3 次执行调用（juhe_business 第 1 条语句）首次 40P01 失败
// 后重试成功，流程继续，成功执行数不变（不重启、不重复已执行语句），仅
// 多出一次尝试。
func TestEnsurePostgresRetriesMidFlowDeadlockAndContinues(t *testing.T) {
	wmRecordRetrySleeps(t)
	rec := &wmSchemaRecorder{failNextExecsWith: []error{nil, nil, wmPGErr("40P01", "deadlock detected")}}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()

	result, err := EnsurePostgres(context.Background(), db)
	if err != nil {
		t.Fatalf("中段死锁重试后 EnsurePostgres 应成功: %v", err)
	}
	if result.StatementCount != len(postgresSchemaStatements) || result.SchemaCount != 6 {
		t.Fatalf("结果异常: %+v", result)
	}
	if got := rec.execCount(); got != len(postgresSchemaStatements)+6 {
		t.Fatalf("成功执行次数=%d, want %d（重试不得重启流程或重复语句）", got, len(postgresSchemaStatements)+6)
	}
	if got := rec.execAttemptCount(); got != len(postgresSchemaStatements)+6+1 {
		t.Fatalf("执行尝试次数=%d, want %d（恰好多一次重试尝试）", got, len(postgresSchemaStatements)+6+1)
	}
}

// TestEnsurePostgresLockRetryExhaustionSurfacesOriginalErrorWithPosition
// 覆盖全流程重试耗尽：中段语句连续 4 次 40P01 后，错误带语句定位上抛且
// 原始 PgError 保留在错误链中。
func TestEnsurePostgresLockRetryExhaustionSurfacesOriginalErrorWithPosition(t *testing.T) {
	wmRecordRetrySleeps(t)
	rec := &wmSchemaRecorder{failNextExecsWith: []error{
		nil, nil,
		wmPGErr("40P01", "deadlock detected"),
		wmPGErr("40P01", "deadlock detected"),
		wmPGErr("40P01", "deadlock detected"),
		wmPGErr("40P01", "deadlock detected"),
	}}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()

	_, err := EnsurePostgres(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "postgres schema statement 1 (juhe_business/") {
		t.Fatalf("重试耗尽必须带语句定位上抛: %v", err)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40P01" {
		t.Fatalf("错误链必须保留原始 40P01 PgError: %v", err)
	}
	if got := rec.execCount(); got != 2 {
		t.Fatalf("成功执行次数=%d, want 2（CREATE SCHEMA + 第 0 条语句）", got)
	}
	if got := rec.execAttemptCount(); got != 6 {
		t.Fatalf("执行尝试次数=%d, want 6（2 次成功 + 4 次失败）", got)
	}
}

// TestEnsurePostgresNonLockErrorIsNotRetried 覆盖全流程非锁冲突错误立即
// 中止：中段语句注入 42601 后只执行一次即上抛。
func TestEnsurePostgresNonLockErrorIsNotRetried(t *testing.T) {
	wmRecordRetrySleeps(t)
	rec := &wmSchemaRecorder{failNextExecsWith: []error{nil, nil, wmPGErr("42601", "syntax error")}}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()

	_, err := EnsurePostgres(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "postgres schema statement 1 (juhe_business/") {
		t.Fatalf("非锁冲突必须立即带定位上抛: %v", err)
	}
	if got := rec.execAttemptCount(); got != 3 {
		t.Fatalf("执行尝试次数=%d, want 3（2 次成功 + 1 次失败，不重试）", got)
	}
}
