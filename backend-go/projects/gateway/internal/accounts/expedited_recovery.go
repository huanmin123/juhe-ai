package accounts

// 特供快速恢复通道（AI账户特供快速恢复通道设计 v3.2）的 gateway accounts 侧
// 名额与展示辅助：设置/创建通道的名额断言（§9）与列表/详情的"已超限"展示位
// （§8.2/§9 上限下调不回溯）。消费方是 jobs 恢复道与 authsys 侧的
// expedited_account_limit 读写，本文件只做归属名下的计数与上限归一。

import (
	"context"
	"database/sql"
	"errors"
)

// defaultExpeditedAccountLimit mirrors system_accounts.expedited_account_limit
// 的 NULL 语义：读侧 COALESCE 归一为 3，不写回（设计 §2）。
const defaultExpeditedAccountLimit = int64(3)

// assertExpeditedRecoveryLimit mirrors 名额校验精确契约（设计 §9）：先对归属行
// SELECT expedited_account_limit ... LIMIT 1 FOR UPDATE（PG 锁归属行串行化并发
// 设置，SQLite 单写者事务天然等价），上限 = COALESCE(列值, 3)，0 即禁止新增/
// 重新启用；再执行归属名下特供计数（软删不计、不过滤 status——停用/异常行仍
// 占名额），计数 + 本次增量（待翻转的补丁行或待插入的创建行）> 上限时整体
// 回滚并返回名额已满 ValidationError。归属行缺失按 NULL 归一（默认 3）。
// 计数口径有意偏离 aiAccountLimit：授权实例计入、0 = 禁止而非不限制（§3.11）。
func (s *Store) assertExpeditedRecoveryLimit(ctx context.Context, q queryer, systemAccountID string, additional int64) error {
	var ownerLimit sql.NullInt64
	err := q.QueryRowContext(ctx, s.bind(`SELECT expedited_account_limit FROM `+s.table("system_accounts")+`
		WHERE id = ?
		LIMIT 1`+s.forUpdate()), systemAccountID).Scan(&ownerLimit)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	limit := defaultExpeditedAccountLimit
	if ownerLimit.Valid {
		limit = ownerLimit.Int64
	}
	var count int64
	if err := q.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("accounts")+`
		WHERE system_account_id = ?
			AND deleted_at IS NULL
			AND expedited_recovery_enabled = 1`), systemAccountID).Scan(&count); err != nil {
		return err
	}
	if count+additional > limit {
		return &ValidationError{Message: "特供账户数量已达上限（" + itoa64(limit) + "）"}
	}
	return nil
}

// expeditedOwnerUsage resolves the per-owner expedited counts and the
// COALESCE-normalized limits for the read projections (设计 §8.2/§9 "已超限"
// 展示位对管理员与归属人都输出)：one grouped COUNT over the accounts table plus
// one limits read over the distinct owner set. Missing owner rows simply stay
// absent from the limits map (callers fall back to the default).
func (s *Store) expeditedOwnerUsage(ctx context.Context, q queryer, ownerIDs []string) (counts map[string]int64, limits map[string]int64, err error) {
	counts = map[string]int64{}
	limits = map[string]int64{}
	if len(ownerIDs) == 0 {
		return counts, limits, nil
	}
	countRows, err := q.QueryContext(ctx, s.bind(`SELECT accounts.system_account_id, COUNT(*)
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.system_account_id IN (`+placeholders(len(ownerIDs))+`)
			AND accounts.deleted_at IS NULL
			AND accounts.expedited_recovery_enabled = 1
		GROUP BY accounts.system_account_id`), anySlice(ownerIDs)...)
	if err != nil {
		return nil, nil, err
	}
	for countRows.Next() {
		var owner string
		var count int64
		if err := countRows.Scan(&owner, &count); err != nil {
			countRows.Close()
			return nil, nil, err
		}
		counts[owner] = count
	}
	if err := countRows.Err(); err != nil {
		countRows.Close()
		return nil, nil, err
	}
	countRows.Close()
	limitRows, err := q.QueryContext(ctx, s.bind(`SELECT id, COALESCE(expedited_account_limit, `+itoa64(defaultExpeditedAccountLimit)+`)
		FROM `+s.table("system_accounts")+`
		WHERE id IN (`+placeholders(len(ownerIDs))+`)`), anySlice(ownerIDs)...)
	if err != nil {
		return nil, nil, err
	}
	for limitRows.Next() {
		var owner string
		var limit int64
		if err := limitRows.Scan(&owner, &limit); err != nil {
			limitRows.Close()
			return nil, nil, err
		}
		limits[owner] = limit
	}
	if err := limitRows.Err(); err != nil {
		limitRows.Close()
		return nil, nil, err
	}
	limitRows.Close()
	return counts, limits, nil
}
