package pgpool

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	pgx "github.com/jackc/pgx/v5"
)

// 7100d97eb 新增的 JUHE_AI_DEBUG_SQL 诊断面（sqlDebugTracer + openPGX 调试臂）
// 在此集中覆盖；全部走懒打开/内存路径，不依赖任何外部服务。

var _ pgx.QueryTracer = sqlDebugTracer{}

func TestSqlDebugTracerStoresStartData(t *testing.T) {
	start := pgx.TraceQueryStartData{SQL: "SELECT 1", Args: []any{int64(7)}}
	ctx := sqlDebugTracer{}.TraceQueryStart(context.Background(), nil, start)
	got, ok := ctx.Value(sqlDebugKey{}).(pgx.TraceQueryStartData)
	if !ok {
		t.Fatal("TraceQueryStart 必须把 start 数据写入 context")
	}
	if got.SQL != start.SQL || len(got.Args) != 1 || got.Args[0] != int64(7) {
		t.Fatalf("context 数据不匹配: %+v", got)
	}
}

func TestSqlDebugTracerEndSuccessIsSilent(t *testing.T) {
	var buf bytes.Buffer
	restore := captureSlogError(t, &buf)
	defer restore()
	start := sqlDebugTracer{}.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	sqlDebugTracer{}.TraceQueryEnd(start, nil, pgx.TraceQueryEndData{})
	if buf.Len() != 0 {
		t.Fatalf("成功查询不应产生日志: %q", buf.String())
	}
}

func TestSqlDebugTracerEndErrorWithoutStartContext(t *testing.T) {
	var buf bytes.Buffer
	restore := captureSlogError(t, &buf)
	defer restore()
	sqlDebugTracer{}.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{Err: errors.New("boom")})
	if buf.Len() != 0 {
		t.Fatalf("无 start 上下文时不应产生日志: %q", buf.String())
	}
}

func TestSqlDebugTracerEndErrorLogsSQLAndArgs(t *testing.T) {
	var buf bytes.Buffer
	restore := captureSlogError(t, &buf)
	defer restore()
	start := sqlDebugTracer{}.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT $1", Args: []any{"v"}})
	sqlDebugTracer{}.TraceQueryEnd(start, nil, pgx.TraceQueryEndData{Err: errors.New("boom")})
	logged := buf.String()
	for _, want := range []string{"SQL_DEBUG 查询失败", "SELECT $1", "boom"} {
		if !bytes.Contains([]byte(logged), []byte(want)) {
			t.Fatalf("失败 SQL 日志缺少 %q: %q", want, logged)
		}
	}
}

func captureSlogError(t *testing.T, buf *bytes.Buffer) func() {
	t.Helper()
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelError})))
	return func() { slog.SetDefault(old) }
}

func TestOpenPGXDefaultArmUsesLazyOpen(t *testing.T) {
	t.Setenv("JUHE_AI_DEBUG_SQL", "")
	db, err := openPGX("postgres://127.0.0.1:1/w1tracer?sslmode=disable")
	if err != nil {
		t.Fatalf("默认臂必须懒打开成功: %v", err)
	}
	if db == nil {
		t.Fatal("默认臂必须返回非空 *sql.DB")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭 = %v", err)
	}
}

func TestOpenPGXDebugArmAttachesTracer(t *testing.T) {
	t.Setenv("JUHE_AI_DEBUG_SQL", "1")
	db, err := openPGX("postgres://127.0.0.1:1/w1tracer?sslmode=disable")
	if err != nil {
		t.Fatalf("调试臂合法连接串必须打开成功: %v", err)
	}
	if db == nil {
		t.Fatal("调试臂必须返回非空 *sql.DB")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭 = %v", err)
	}
}

func TestOpenPGXDebugArmPropagatesParseError(t *testing.T) {
	t.Setenv("JUHE_AI_DEBUG_SQL", "1")
	db, err := openPGX("postgres://127.0.0.1:notaport/w1tracer?sslmode=disable")
	if err == nil {
		if db != nil {
			_ = db.Close()
		}
		t.Fatal("调试臂必须上抛 ParseConfig 错误")
	}
	if db != nil {
		t.Fatal("解析失败时必须返回空 *sql.DB")
	}
}
