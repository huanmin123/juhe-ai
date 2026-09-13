package runtimelog

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// wn_postgres_store_test.go 覆盖 postgresStore 的 pgx 直连路径：
// schema 校验/初始化、owner lease 状态机、cursor、Commit、Cleanup 与
// 运行日志保留期设置读取。响应全部由 wn_pgfake_test.go 的假服务脚本化提供。

// 元数据查询的结果列声明，Describe 响应必须与 Execute 返回的行形状一致。
func pgFakeTableNameColumns() []pgFakeColumn {
	return []pgFakeColumn{{name: "table_name", oid: pgFakeOIDText}}
}

func pgFakeIndexNameColumns() []pgFakeColumn {
	return []pgFakeColumn{{name: "indexname", oid: pgFakeOIDText}}
}

func pgFakeColumnTypeColumns() []pgFakeColumn {
	return []pgFakeColumn{{name: "column_name", oid: pgFakeOIDText}, {name: "data_type", oid: pgFakeOIDText}}
}

// registerPostgresCatalog 注册 schema 校验所需的元数据查询脚本，
// 列定义直接复用生产 schema 常量，保证与真实 DDL 一致。
func registerPostgresCatalog(t *testing.T, server *pgFakeServer) {
	t.Helper()
	server.handleFunc("SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeTableNameColumns(), func(args []string) ([][]any, string, *pgFakeError) {
		if len(args) == 0 {
			return nil, "", &pgFakeError{code: "08P01", message: "缺少表名参数"}
		}
		return [][]any{{args[0]}}, "SELECT 1", nil
	})
	server.handleFunc("SELECT indexname FROM pg_indexes WHERE schemaname = 'juhe_dataset' AND indexname = $1", pgFakeIndexNameColumns(), func(args []string) ([][]any, string, *pgFakeError) {
		if len(args) == 0 {
			return nil, "", &pgFakeError{code: "08P01", message: "缺少索引名参数"}
		}
		return [][]any{{args[0]}}, "SELECT 1", nil
	})
	server.handleFunc("SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeColumnTypeColumns(), func(args []string) ([][]any, string, *pgFakeError) {
		table := ""
		if len(args) > 0 {
			table = args[0]
		}
		columns, ok := runtimeLogColumns[table]
		if !ok {
			return nil, "SELECT 0", nil
		}
		instant := runtimeLogPostgresInstantColumns[table]
		rows := make([][]any, 0, len(columns))
		for _, column := range columns {
			dataType := "text"
			if instant[column] {
				dataType = "timestamp with time zone"
			}
			rows = append(rows, []any{column, dataType})
		}
		return rows, "SELECT " + itoa(len(rows)), nil
	})
}

func itoa(value int) string {
	digits := ""
	if value == 0 {
		return "0"
	}
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func openFakePostgresStore(t *testing.T, server *pgFakeServer) *postgresStore {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	opened, err := OpenStore(ctx, Config{Mode: ModePostgres, PostgresURL: server.URL()})
	if err != nil {
		t.Fatalf("连接 PostgreSQL fake store 失败: %v", err)
	}
	store, ok := opened.(*postgresStore)
	if !ok {
		_ = opened.Close()
		t.Fatalf("OpenStore(ModePostgres) 返回 %T，期望 *postgresStore", opened)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("关闭 fake PostgreSQL store 失败: %v", closeErr)
		}
	})
	return store
}

func TestPostgresFakeOpenStoreEnsureAndCheckSchema(t *testing.T) {
	server := newPGFakeServer(t)
	registerPostgresCatalog(t, server)
	store := openFakePostgresStore(t, server)
	ctx := context.Background()

	if err := EnsureSchema(ctx, store); err != nil {
		t.Fatalf("健康的 PostgreSQL schema 必须跳过 bootstrap DDL: %v", err)
	}
	for _, entry := range server.executedLog() {
		if len(entry.sql) >= 6 && (entry.sql[:6] == "CREATE" || entry.sql[:5] == "ALTER") {
			t.Fatalf("健康 schema 不得执行 DDL，实际执行了 %q", entry.sql)
		}
	}
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("CheckSchema 必须通过元数据脚本校验: %v", err)
	}
}

func TestPostgresFakeEnsureSchemaBootstrapsMissingSchema(t *testing.T) {
	server := newPGFakeServer(t)
	// 表存在性查询首次返回缺失（触发 bootstrap DDL），之后的校验返回已创建。
	var tablesQueryCount atomic.Int64
	server.handleFunc("SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeTableNameColumns(), func(args []string) ([][]any, string, *pgFakeError) {
		if tablesQueryCount.Add(1) == 1 {
			return nil, "SELECT 0", nil
		}
		return [][]any{{args[0]}}, "SELECT 1", nil
	})
	server.handleFunc("SELECT indexname FROM pg_indexes WHERE schemaname = 'juhe_dataset' AND indexname = $1", pgFakeIndexNameColumns(), func(args []string) ([][]any, string, *pgFakeError) {
		return [][]any{{args[0]}}, "SELECT 1", nil
	})
	server.handleFunc("SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeColumnTypeColumns(), func(args []string) ([][]any, string, *pgFakeError) {
		columns := runtimeLogColumns[args[0]]
		instant := runtimeLogPostgresInstantColumns[args[0]]
		rows := make([][]any, 0, len(columns))
		for _, column := range columns {
			dataType := "text"
			if instant[column] {
				dataType = "timestamp with time zone"
			}
			rows = append(rows, []any{column, dataType})
		}
		return rows, "SELECT 6", nil
	})
	store := openFakePostgresStore(t, server)
	ctx := context.Background()

	if err := EnsureSchema(ctx, store); err != nil {
		t.Fatalf("schema 缺失时 EnsureSchema 必须执行 bootstrap DDL: %v", err)
	}
	created := 0
	for _, entry := range server.executedLog() {
		if len(entry.sql) >= 6 && entry.sql[:6] == "CREATE" {
			created++
		}
	}
	if created < 7 {
		t.Fatalf("bootstrap DDL 必须包含全部表与索引，实际 CREATE 数量 %d", created)
	}
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("bootstrap 后 schema 校验必须通过: %v", err)
	}
}

func TestPostgresFakeCheckSchemaClassifiesFailures(t *testing.T) {
	t.Run("missing table", func(t *testing.T) {
		server := newPGFakeServer(t)
		server.handleFunc("SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", nil, func([]string) ([][]any, string, *pgFakeError) {
			return nil, "SELECT 0", nil
		})
		store := openFakePostgresStore(t, server)
		err := store.CheckSchema(context.Background())
		if err == nil || !strings.Contains(err.Error(), "缺少 PostgreSQL 表") {
			t.Fatalf("缺少表必须返回可诊断错误: %v", err)
		}
	})

	t.Run("missing index", func(t *testing.T) {
		server := newPGFakeServer(t)
		registerPostgresCatalog(t, server)
		server.handleFunc("SELECT indexname FROM pg_indexes WHERE schemaname = 'juhe_dataset' AND indexname = $1", pgFakeIndexNameColumns(), func([]string) ([][]any, string, *pgFakeError) {
			return nil, "SELECT 0", nil
		})
		store := openFakePostgresStore(t, server)
		err := store.CheckSchema(context.Background())
		if err == nil || !strings.Contains(err.Error(), "缺少 PostgreSQL 运行日志索引") {
			t.Fatalf("缺少索引必须返回可诊断错误: %v", err)
		}
	})

	t.Run("instant column not timestamptz", func(t *testing.T) {
		server := newPGFakeServer(t)
		server.handleFunc("SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeTableNameColumns(), func(args []string) ([][]any, string, *pgFakeError) {
			return [][]any{{args[0]}}, "SELECT 1", nil
		})
		server.handleFunc("SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeColumnTypeColumns(), func(args []string) ([][]any, string, *pgFakeError) {
			columns := runtimeLogColumns[args[0]]
			rows := make([][]any, 0, len(columns))
			for _, column := range columns {
				rows = append(rows, []any{column, "timestamp without time zone"})
			}
			return rows, "SELECT 6", nil
		})
		store := openFakePostgresStore(t, server)
		err := store.CheckSchema(context.Background())
		if err == nil || !strings.Contains(err.Error(), "必须是 timestamptz") {
			t.Fatalf("timestamptz 列缺失必须要求离线迁移: %v", err)
		}
	})

	t.Run("missing column", func(t *testing.T) {
		server := newPGFakeServer(t)
		server.handleFunc("SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeTableNameColumns(), func(args []string) ([][]any, string, *pgFakeError) {
			return [][]any{{args[0]}}, "SELECT 1", nil
		})
		server.handleFunc("SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = 'juhe_dataset' AND table_name = $1", pgFakeColumnTypeColumns(), func([]string) ([][]any, string, *pgFakeError) {
			return nil, "SELECT 0", nil
		})
		store := openFakePostgresStore(t, server)
		err := store.CheckSchema(context.Background())
		if err == nil || !strings.Contains(err.Error(), "缺少运行日志字段") {
			t.Fatalf("缺少字段必须返回可诊断错误: %v", err)
		}
	})

	t.Run("uncatalogued metadata error", func(t *testing.T) {
		server := newPGFakeServer(t)
		server.handleError("SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", "42501", "permission denied for information_schema")
		store := openFakePostgresStore(t, server)
		err := store.CheckSchema(context.Background())
		// 非 ErrNoRows 的元数据错误必须保留原始错误且不归类为 schema 缺失。
		if err == nil || !strings.Contains(err.Error(), "permission denied for information_schema") {
			t.Fatalf("非缺表错误必须保留原始错误: %v", err)
		}
		if errors.Is(err, errPostgresRuntimeLogSchemaMissing) {
			t.Fatalf("权限错误不得归类为 PostgreSQL 运行日志 schema 缺失: %v", err)
		}
		// 非 schema 缺失错误不得触发 bootstrap。
		bootstrap, bootstrapErr := postgresRuntimeLogSchemaBootstrapRequired(err)
		if bootstrap || bootstrapErr == nil {
			t.Fatalf("权限错误不得触发 bootstrap: bootstrap=%t err=%v", bootstrap, bootstrapErr)
		}
	})
}
