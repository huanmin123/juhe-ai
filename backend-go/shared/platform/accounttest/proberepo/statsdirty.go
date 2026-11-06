package proberepo

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

// 账号 API Key 运行态翻转后的分组统计标脏（账号#F8 缺口补齐）。
//
// 移植基线：Node account-api-key-runtime-state.repository.ts 的
// markRuntimeStateChanged(Async)——recordAccountApiKeyRuntimeSuccess/Failure
// 在 changed>0 时同步把来源账户与授权实例同源账户所在分组 upsert 进
// group_account_stats_dirty（reason 固定 account_api_key_runtime），供 stats
// 家族 RefreshDirtyGroupAccountStats 消费重建。gateway 侧同表写路径
// （accountkeystates finishMutation → markRuntimeStateChanged）已带该标脏；
// jobs 探针族（本包 RecordKeySuccess/RecordKeyFailure）此前缺失。
//
// Node 同函数尾部的 notifyGatewayRuntimeCacheInvalidation 是 gateway 进程内
// 运行态缓存失效通知；jobs 侧不能直接调用 gateway 总线，B3b 改为在 PG 写
// 提交后按同一受影响账户集合递增 crossprocessstate 状态序号，让 gateway
// runtime 下一次读重载。group_account_stats_dirty 脏行仍是统计面事实源。
//
// MarkPrecheckTemporaryUnavailable（写 accounts.status 的 precheck 路径）在
// Node 侧不标分组脏；B3b 仍为它补状态序号（gateway 需感知账户状态变化）。
// DeferKeyProbe 仅推 next_probe_at，不翻状态，不标脏也不 bump。

// accountKeyRuntimeStatsDirtyReason 对齐 Node markRuntimeStateChanged 的
// reason 常量（gateway accountkeystates statsDirtyReason 同源）。
const accountKeyRuntimeStatsDirtyReason = "account_api_key_runtime"

// markRuntimeStateChanged 等价 markRuntimeStateChanged(Async)。
func (s *Store) markRuntimeStateChanged(ctx context.Context, sourceAccountID string) error {
	affected, err := s.affectedAccountIdsBySourceAccount(ctx, sourceAccountID)
	if err != nil {
		return err
	}
	if len(affected) == 0 {
		affected = []string{sourceAccountID}
	}
	// B3b：Key 运行态 PG 写已提交，先递增来源 + 授权实例账户的共享状态序号；
	// 后续分组标脏失败不回滚已提交的 Key 状态，序号仍需让 gateway 重载。
	s.bumpAccountStateVersions(ctx, affected)
	return s.markGroupAccountStatsDirtyByAccountIds(ctx, affected)
}

// bumpAccountStateVersions 在 Key 运行态提交后按受影响账户递增共享序号；失败
// 只告警，gateway 按既有 60s/2min 窗口回退。
func (s *Store) bumpAccountStateVersions(ctx context.Context, accountIDs []string) {
	if s.stateVersion == nil || len(accountIDs) == 0 {
		return
	}
	bumpCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	for _, accountID := range normalizeDirtyAccountIds(accountIDs) {
		if err := s.stateVersion.BumpAccountState(bumpCtx, accountID); err != nil {
			slog.Warn("Key 运行态版本栅栏写入失败，gateway 按既有缓存窗口回退",
				"event", "account_state_version_bump_failed",
				"accountId", accountID, "error", err)
		}
	}
}

// affectedAccountIdsBySourceAccount 等价 accountIdsAffectedBySourceAccount(Async)：
// 来源账户 + 以其为授权实例源的全部账户（与 Node 一致不过滤 deleted_at）。
func (s *Store) affectedAccountIdsBySourceAccount(ctx context.Context, sourceAccountID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(fmt.Sprintf(`
    SELECT id FROM %s
    WHERE id = ? OR authorization_instance_source_account_id = ?
  `, s.table("accounts"))), sourceAccountID, sourceAccountID)
	if err != nil {
		return nil, fmt.Errorf("读取 key 运行态标脏受影响账户失败: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id.String != "" {
			ids = append(ids, id.String)
		}
	}
	return ids, rows.Err()
}

// markGroupAccountStatsDirtyByAccountIds 等价
// markGroupAccountStatsDirtyByAccountIds(Async)：账户 id 去重后 900 分批查
// group_accounts DISTINCT group_id，逐组 last-write-wins upsert 脏行。
func (s *Store) markGroupAccountStatsDirtyByAccountIds(ctx context.Context, accountIds []string) error {
	ids := normalizeDirtyAccountIds(accountIds)
	if len(ids) == 0 {
		return nil
	}
	groupIds := []string{}
	seen := map[string]bool{}
	for start := 0; start < len(ids); start += 900 {
		end := start + 900
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for index, id := range chunk {
			placeholders[index] = "?"
			args[index] = id
		}
		query := fmt.Sprintf(`SELECT DISTINCT group_id FROM %s WHERE account_id IN (%s)`,
			s.table("group_accounts"), strings.Join(placeholders, ", "))
		rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
		if err != nil {
			return fmt.Errorf("读取 key 运行态标脏分组失败: %w", err)
		}
		for rows.Next() {
			var groupID sql.NullString
			if err := rows.Scan(&groupID); err != nil {
				rows.Close()
				return err
			}
			if groupID.String != "" && !seen[groupID.String] {
				seen[groupID.String] = true
				groupIds = append(groupIds, groupID.String)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	if len(groupIds) == 0 {
		return nil
	}
	updatedAt := s.now().UTC().Format(rfc3339Milli)
	upsert := fmt.Sprintf(`
    INSERT INTO %s (group_id, reason, updated_at)
    VALUES (?, ?, ?)
    ON CONFLICT (group_id) DO UPDATE SET
      reason = excluded.reason,
      updated_at = excluded.updated_at
  `, s.table("group_account_stats_dirty"))
	for _, groupID := range groupIds {
		if _, err := s.db.ExecContext(ctx, s.bind(upsert), groupID, accountKeyRuntimeStatsDirtyReason, updatedAt); err != nil {
			return fmt.Errorf("标记 key 运行态分组统计脏行失败: %w", err)
		}
	}
	return nil
}

// normalizeDirtyAccountIds 对齐 Node 调用侧的 trim + 去重 + 空值过滤。
func normalizeDirtyAccountIds(accountIds []string) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, id := range accountIds {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		ids = append(ids, trimmed)
	}
	return ids
}

// keyMutationResultWithDirty 在 changed>0 时补 runtime-state-changed 分组标脏
// （Node recordAccountApiKeyRuntimeSuccess/Failure 的 markRuntimeStateChanged
// 尾部；gateway accountkeystates finishMutation 同款错误上抛语义），未 changed
// 时保持 rowsToResult 的 stale_probe_state 形状。
func (s *Store) keyMutationResultWithDirty(ctx context.Context, result sql.Result, sourceAccountID string, fenceProvided bool) (accountquality.KeyMutationResult, error) {
	changed, err := result.RowsAffected()
	if err != nil {
		return accountquality.KeyMutationResult{}, err
	}
	if changed > 0 {
		if err := s.markRuntimeStateChanged(ctx, sourceAccountID); err != nil {
			return accountquality.KeyMutationResult{}, err
		}
		return accountquality.KeyMutationResult{Changed: true}, nil
	}
	if fenceProvided {
		return changedFalse("stale_probe_state"), nil
	}
	return accountquality.KeyMutationResult{Changed: false}, nil
}
