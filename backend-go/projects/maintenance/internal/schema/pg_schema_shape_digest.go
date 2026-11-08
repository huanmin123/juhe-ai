// PostgresActualShapeDigest 计算 juhe_business schema 的「库内实际形态」指纹
//（T84-3，2026-11-08 用户裁决「根治」：选定 B 口径并与 A 口径版本前缀区分）。
//
// 序列化范围：columns + indexes + constraints 的标准化行（触发器等运行时对象
// 不入指纹——dirty 触发器由各自的 ensure/清理逻辑管理，属可再生态）。行排序后
// 聚合 md5，输出前缀 sha256shape:v1 与 A 口径（PostgresSchemaDigest 的代码
// DDL 清单哈希）区分——升级账本（WriteSchemaState/比对）只在新前缀值之间
// 比对，存量库的旧口径值自动跳过一次口径迁移。
//
// 与 A 口径的本质差异：PostgresSchemaDigest 是纯代码函数，对「绕过升级链的
// 手工 DDL」不敏感；本指纹读库内事实，任何手工加列/建索引/改约束都会改变
// 指纹——这是 §9.6 自改用户检测的实现基础。
package schema

import (
	"context"
	"database/sql"
)

const postgresActualShapeDigestQuery = `
SELECT 'sha256shape:v1:' || md5(string_agg(line, E'\n' ORDER BY line)) FROM (
  SELECT 'C|' || table_name || '|' || column_name || '|' || data_type || '|'
         || is_nullable || '|' || COALESCE(column_default, '') AS line
    FROM information_schema.columns
   WHERE table_schema = 'juhe_business'
  UNION ALL
  SELECT 'I|' || tablename || '|' || indexname || '|'
         || regexp_replace(indexdef, E'\\s+', ' ', 'g') AS line
    FROM pg_indexes
   WHERE schemaname = 'juhe_business'
  UNION ALL
  SELECT 'K|' || conrelid::regclass::text || '|' || conname || '|'
         || pg_get_constraintdef(oid) AS line
    FROM pg_constraint
   WHERE connamespace = 'juhe_business'::regnamespace
) s`

// PostgresActualShapeDigest 返回库内实际形态指纹（只读，单条聚合查询）。
func PostgresActualShapeDigest(ctx context.Context, db *sql.DB) (string, error) {
	var digest string
	if err := db.QueryRowContext(ctx, postgresActualShapeDigestQuery).Scan(&digest); err != nil {
		return "", err
	}
	return digest, nil
}

// ShapeDigestIsActualShape 判定账本里存的 digest 是否为 B 口径（实际形态）
// 值——旧口径（A：代码 DDL 清单哈希，无前缀）的存量值不参与比对，写入新值
// 后自然完成口径迁移。
func ShapeDigestIsActualShape(digest string) bool {
	return len(digest) >= len("sha256shape:v1:") && digest[:len("sha256shape:v1:")] == "sha256shape:v1:"
}
