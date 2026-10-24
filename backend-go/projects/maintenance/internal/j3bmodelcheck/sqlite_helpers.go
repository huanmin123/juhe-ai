package j3bmodelcheck

// J3b 一次性 backfill/readback/inventory 子系统已于 2026-10-24 删除（对应
// CLI flag 早在 2026-09-30 随迁移期死命令族退役，库层实现零生产调用）。
// 本文件保留仍被 bootstrap 读写路径使用的 SQLite 列/主键探测与引号 helper。

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

var sqlitePrimaryKeysDB = sqlitePrimaryKeysDBImpl

func sqlitePrimaryKeysDBImpl(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteIdent(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[int]string{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		if pk > 0 {
			keys[pk] = name
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	positions := make([]int, 0, len(keys))
	for position := range keys {
		positions = append(positions, position)
	}
	sort.Ints(positions)
	result := make([]string, len(positions))
	for i, position := range positions {
		result[i] = keys[position]
	}
	return result, nil
}

func quoteIdent(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func joinQuoted(values []string) string {
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	return joinQuotedInOrder(copied)
}

// joinQuotedInOrder preserves declaration order. SELECT projections are
// sorted for digest stability, while ORDER BY primary keys must retain the
// composite-key sequence declared by SQLite; sorting key names can change
// row ordering for a valid legacy schema.
func joinQuotedInOrder(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = quoteIdent(value)
	}
	return strings.Join(quoted, ",")
}

var sqliteColumns = sqliteColumnsImpl

func sqliteColumnsImpl(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteIdent(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("table %s is missing", table)
	}
	return columns, nil
}
