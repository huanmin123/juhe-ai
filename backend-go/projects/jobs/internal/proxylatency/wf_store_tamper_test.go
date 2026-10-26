package proxylatency

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// 本文件通过直接改写 SQLite jobs 库的行数据，覆盖 ListCommittedOutcomes /
// FindCommittedOutcome 对损坏持久化记录的 fail-closed
// 分支（时间非法、载荷非法、摘要不一致、行元数据与载荷不一致）。
//
// 组B保留（SQLite-only 生产臂，PLAN-20261024T051925945Z proxylatency 切片
// 裁决，整文件不迁，随生产 sqlite 臂删除批次退役）。证据：全部注入手法依赖
// SQLite TEXT 列接受畸形字节——observed_at='not-a-time'、issued_at='garbage'、
// payload='not-json' 在 PG timestamptz/jsonb 强类型列上写入即被数据库拒绝
// （invalid input syntax），同等注入形态在 PG 臂不可复现；PG 强类型本身即
// 构成该防御面的数据库层等价物。stored_at 零值变体同样不可迁移：PG 扫描
// year-1 时间戳得到的 time.Time 非零值，IsZero 分支不触发。

func wfInsertRawOutcome(t *testing.T, store *Store, columns, values string) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO proxy_latency_outcomes(outcome_id,request_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,observed_at,stored_at,payload,payload_digest,committed) VALUES(` + values + `)`); err != nil {
		t.Fatalf("插入原始行失败: %v", err)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

