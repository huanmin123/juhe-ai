package businesshandoff

// w12g 波次：补齐 VerifySQLiteSchema 的辅助查询错误传播分支（通过包级函数
// 变量注入 Mock）、缺索引/主键漂移/非 SQLite 文件的真实库分支，以及
// preflight 隔离临时目录创建失败路径。
//
// 不可达清单（w12g 登记，真实 modernc/sqlite 驱动与本地文件语义下无法注入，
// 已尽量贴近 95%）：
//   - schema.go:42-45  sql.Open 错误分支（驱动已注册且打开惰性）
//   - schema.go:53-55  query_only 未启用分支（DSN 固定携带 query_only(1)）
//   - schema.go:156    sqliteObjects 的 Scan 错误：driver.Value 全部类型
//     （含 bool/time.Time）都存在到 *string 的转换路径，无法构造失败
//   - schema.go:239-241、377-379、409-411  显式 rows.Close 的错误传播分支：
//     database/sql 在 Next 返回 io.EOF 时即完成底层行集关闭，随后显式
//     Close 只回放已记录的 lasterr，driver Close 错误无法再浮出
//   - preflight.go:113      canonicalPath 的 filepath.Abs 失败回退
//     （仅当前工作目录被删除时发生）
//   - preflight.go:135-174  隔离写探针的 seed 打开/建表/关闭、PRAGMA 读取、
//     写拒绝未生效、行计数失败/行数变化分支：均依赖真实 SQLite 违反自身
//     语义（query_only(1) 必拒绝写、探针文件生命周期受控）
//   - cutover_evidence.go:73-75,78-80  stat 成功后 os.Open / io.Copy 失败
//     （无符号链接/权限竞争注入点）
//   - preflight.go:143-145 seed.Close 错误分支（同上）

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

var errW12GInjected = errors.New("w12g 注入辅助查询失败")

// w12GMockHelper 临时替换一个包级辅助函数变量，测试结束自动还原。
func w12GMockHelper[T any](t *testing.T, target *T, replacement T) {
	t.Helper()
	original := *target
	*target = replacement
	t.Cleanup(func() { *target = original })
}

func TestW12GVerifySQLiteSchemaHelperErrorPropagation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	buildBusinessSQLiteFixture(t, path)

	assertWrapped := func(t *testing.T, err error, needle string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), needle) || !strings.Contains(err.Error(), errW12GInjected.Error()) {
			t.Fatalf("必须上抛 %q 包装错误: %v", needle, err)
		}
	}

	t.Run("objects tables error", func(t *testing.T) {
		w12GMockHelper(t, &sqliteObjects, func(ctx context.Context, db *sql.DB, objectType string) (map[string]bool, error) {
			return nil, errW12GInjected
		})
		_, err := VerifySQLiteSchema(context.Background(), path)
		assertWrapped(t, err, "list Business SQLite tables")
	})

	t.Run("objects indexes error", func(t *testing.T) {
		w12GMockHelper(t, &sqliteObjects, func(ctx context.Context, db *sql.DB, objectType string) (map[string]bool, error) {
			if objectType == "index" {
				return nil, errW12GInjected
			}
			return map[string]bool{}, nil
		})
		_, err := VerifySQLiteSchema(context.Background(), path)
		assertWrapped(t, err, "list Business SQLite indexes")
	})

	t.Run("columns error", func(t *testing.T) {
		w12GMockHelper(t, &sqliteColumns, func(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
			return nil, errW12GInjected
		})
		_, err := VerifySQLiteSchema(context.Background(), path)
		if err == nil || !strings.Contains(err.Error(), errW12GInjected.Error()) {
			t.Fatalf("columns 错误必须上抛: %v", err)
		}
	})

	t.Run("primary key error", func(t *testing.T) {
		w12GMockHelper(t, &sqlitePrimaryKey, func(ctx context.Context, db *sql.DB, table string) ([]string, error) {
			return nil, errW12GInjected
		})
		_, err := VerifySQLiteSchema(context.Background(), path)
		if err == nil || !strings.Contains(err.Error(), errW12GInjected.Error()) {
			t.Fatalf("primary key 错误必须上抛: %v", err)
		}
	})

	t.Run("unique constraint error", func(t *testing.T) {
		w12GMockHelper(t, &sqliteHasUniqueConstraint, func(ctx context.Context, db *sql.DB, table string, required []string) (bool, error) {
			return false, errW12GInjected
		})
		_, err := VerifySQLiteSchema(context.Background(), path)
		if err == nil || !strings.Contains(err.Error(), errW12GInjected.Error()) {
			t.Fatalf("unique constraint 错误必须上抛: %v", err)
		}
	})

	t.Run("index matches error", func(t *testing.T) {
		// unique 约束放行，避免先于 IndexDefinitions 检查返回。
		w12GMockHelper(t, &sqliteHasUniqueConstraint, func(ctx context.Context, db *sql.DB, table string, required []string) (bool, error) {
			return true, nil
		})
		w12GMockHelper(t, &sqliteIndexMatches, func(ctx context.Context, db *sql.DB, table string, required contracts.SQLiteIndexDefinition) (bool, string, error) {
			return false, "", errW12GInjected
		})
		_, err := VerifySQLiteSchema(context.Background(), path)
		assertWrapped(t, err, "inspect Business SQLite index")
	})

	t.Run("foreign keys error", func(t *testing.T) {
		w12GMockHelper(t, &sqliteHasUniqueConstraint, func(ctx context.Context, db *sql.DB, table string, required []string) (bool, error) {
			return true, nil
		})
		w12GMockHelper(t, &sqliteForeignKeys, func(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
			return nil, errW12GInjected
		})
		_, err := VerifySQLiteSchema(context.Background(), path)
		if err == nil || !strings.Contains(err.Error(), errW12GInjected.Error()) {
			t.Fatalf("foreign keys 错误必须上抛: %v", err)
		}
	})

	t.Run("index columns error inside unique check", func(t *testing.T) {
		w12GMockHelper(t, &sqliteIndexColumns, func(ctx context.Context, db *sql.DB, name string) ([]string, error) {
			return nil, errW12GInjected
		})
		_, err := VerifySQLiteSchema(context.Background(), path)
		if err == nil || !strings.Contains(err.Error(), errW12GInjected.Error()) {
			t.Fatalf("index columns 错误必须上抛: %v", err)
		}
	})
}

func TestW12GVerifySQLiteSchemaDetectsMissingPlainIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	buildBusinessSQLiteFixture(t, path)
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP INDEX "idx_account_quality_enforcements_recovery"`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := VerifySQLiteSchema(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || len(report.MissingIndexes["account_quality_enforcements"]) == 0 {
		t.Fatalf("缺失普通索引必须 fail closed: %+v", report)
	}
}

func TestW12GVerifySQLiteSchemaDetectsPrimaryKeyDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	buildBusinessSQLiteFixture(t, path)
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	// 重建为同列但无主键的表：覆盖 PRIMARY KEY 漂移分支。
	if _, err = db.Exec(`DROP TABLE "account_quality_enforcements"`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	spec := contracts.BusinessSQLiteSchema["account_quality_enforcements"]
	defs := make([]string, 0, len(spec.Columns))
	for _, col := range spec.Columns {
		defs = append(defs, `"`+col+`" TEXT`)
	}
	if _, err = db.Exec(`CREATE TABLE "account_quality_enforcements" (` + strings.Join(defs, ",") + `)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := VerifySQLiteSchema(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || len(report.MissingConstraints["account_quality_enforcements"]) == 0 ||
		!containsString(report.MissingConstraints["account_quality_enforcements"], "PRIMARY KEY (account_id)") {
		t.Fatalf("主键漂移必须 fail closed: %+v", report.MissingConstraints)
	}
}

func TestW12GVerifySQLiteSchemaRejectsNonSQLiteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "text.sqlite3")
	if err := os.WriteFile(path, []byte("definitely not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := VerifySQLiteSchema(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "list Business SQLite tables") {
		t.Fatalf("非 SQLite 文件必须在列表查询处失败: %v", err)
	}
}

func TestW12GPreflightFailsWhenTempDirUnavailable(t *testing.T) {
	// TMP 指向不存在的深层路径 → MkdirTemp 失败 → Verify 上抛。
	t.Setenv("TMP", `Q:\w12g\no-such-dir`)
	t.Setenv("TEMP", `Q:\w12g\no-such-dir`)
	report, err := Verify(context.Background(), "a.sqlite3", "b.sqlite3")
	if err == nil || !strings.Contains(err.Error(), "create isolated SQLite directory") {
		t.Fatalf("临时目录创建失败必须上抛: %v", err)
	}
	if report.Ready || report.PathIsolationReady {
		t.Fatalf("失败时不得产出就绪报告: %+v", report)
	}
}

func containsString(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}
