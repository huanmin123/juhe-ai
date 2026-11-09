package main

import (
	"context"
	"database/sql"
	"testing"
)

// missingRetentionTable 是 J3b / 余额清理的表缺席守卫：老库缺表时整阶段
// 跳过，避免 data-retention-cleanup 因 undefined_table 每轮失败并拖累其它
// 清理。本测试锁定「全部存在返回空串 / 首个缺失返回 schema.table」语义。
func TestMissingRetentionTable(t *testing.T) {
	ctx := context.Background()
	db, _ := wgOpenPGDialectDB(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_retention_probe`)
		if err != nil {
			return err
		}
		_, err = db.ExecContext(ctx, `CREATE TABLE juhe_retention_probe.present (id text PRIMARY KEY)`)
		return err
	})

	missing, err := missingRetentionTable(ctx, db, "juhe_retention_probe", []string{"present"})
	if err != nil {
		t.Fatalf("全部存在时不得报错: %v", err)
	}
	if missing != "" {
		t.Fatalf("全部存在应返回空串，got %q", missing)
	}

	missing, err = missingRetentionTable(ctx, db, "juhe_retention_probe", []string{"present", "absent", "also_absent"})
	if err != nil {
		t.Fatalf("缺表探测不得报错: %v", err)
	}
	if missing != "juhe_retention_probe.absent" {
		t.Fatalf("应返回第一个缺失表，got %q", missing)
	}
}
