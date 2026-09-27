package accountsbalance

// 账户管理列表的余额快照叠加读取面。对齐 Node
// account-status-snapshot.service.ts 的列表余额投影（:172 owner 且
// balanceQueryEnabled 的账户才读快照；:256-265 balanceQueryEnabled/
// balanceQueryNextRefreshAt 仅 owner 行投影，balanceSnapshot 仅在
// "启用且快照与当前配置匹配"时写入）+ account-balance.repository.ts
// loadAccountBalanceSnapshotRecordsByAccountIdsAsync（juhe_stats
// account_usage_snapshots kind='relay_balance'，900 分块）+
// accountBalanceSnapshotForList（列表形状剥 keyBalances）。
//
// 匹配判定复用本包 BalanceSnapshotMatchesConfiguration（Node
// accountBalanceSnapshotMatchesConfiguration 的移植：快照必须携带与账户当前
// config_revision 相等的 configRevision，且 next_refresh_after 与配置的
// balance_query_next_refresh_at 毫秒相等或双空）。Node 读侧还有一个
// isAccountBalanceSnapshotSuppressed 闸门（进程内清理协调器的运行时抑制
// 状态）；Go gateway 无该协调器对应物，读端不设抑制——配置变更后的旧快照
// 已被 configRevision 不匹配过滤，覆盖抑制的主用途。

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// AccountBalanceSnapshotPublic mirrors AccountBalanceSnapshot（列表公共形状，
// 前端 frontend/src/types/domain/accounts.ts:306-324）：逐 Key 明细
// （keyBalances）只走明细接口，列表形状按白名单字段重编。camelCase 与
// Node/前端契约逐字段对齐。
type AccountBalanceSnapshotPublic struct {
	Status                       string  `json:"status"`
	ConfigRevision               *int64  `json:"configRevision,omitempty"`
	RemainingUsd                 *string `json:"remainingUsd,omitempty"`
	RawUnit                      *string `json:"rawUnit,omitempty"`
	ErrorMessage                 *string `json:"errorMessage,omitempty"`
	LastAttemptAt                *string `json:"lastAttemptAt,omitempty"`
	LastSuccessAt                *string `json:"lastSuccessAt,omitempty"`
	ConsecutiveTransientFailures *int64  `json:"consecutiveTransientFailures,omitempty"`
	LastTransientErrorMessage    *string `json:"lastTransientErrorMessage,omitempty"`
	LastTransientFailureAt       *string `json:"lastTransientFailureAt,omitempty"`
	Scope                        *string `json:"scope,omitempty"`
	Aggregation                  *string `json:"aggregation,omitempty"`
	KeyCount                     *int64  `json:"keyCount,omitempty"`
	QueriedKeyCount              *int64  `json:"queriedKeyCount,omitempty"`
}

// BalanceSnapshotPublicFromSnapshot 按白名单字段把 stats 行 snapshot_json 的
// 反序列化结果重编成列表公共形状（accountBalanceSnapshotForList 剥
// keyBalances 的等价实现：白名单之外的一切字段——含 keyBalances——不透出；
// 字段形状非法时该字段省略，不影响其余字段）。
func BalanceSnapshotPublicFromSnapshot(snapshot map[string]any) *AccountBalanceSnapshotPublic {
	if snapshot == nil {
		return nil
	}
	out := &AccountBalanceSnapshotPublic{}
	if text, ok := snapshot["status"].(string); ok {
		out.Status = text
	}
	out.ConfigRevision = publicSnapshotNumber(snapshot, "configRevision")
	out.RemainingUsd = publicSnapshotText(snapshot, "remainingUsd")
	out.RawUnit = publicSnapshotText(snapshot, "rawUnit")
	out.ErrorMessage = publicSnapshotText(snapshot, "errorMessage")
	out.LastAttemptAt = publicSnapshotText(snapshot, "lastAttemptAt")
	out.LastSuccessAt = publicSnapshotText(snapshot, "lastSuccessAt")
	out.ConsecutiveTransientFailures = publicSnapshotNumber(snapshot, "consecutiveTransientFailures")
	out.LastTransientErrorMessage = publicSnapshotText(snapshot, "lastTransientErrorMessage")
	out.LastTransientFailureAt = publicSnapshotText(snapshot, "lastTransientFailureAt")
	out.Scope = publicSnapshotText(snapshot, "scope")
	out.Aggregation = publicSnapshotText(snapshot, "aggregation")
	out.KeyCount = publicSnapshotNumber(snapshot, "keyCount")
	out.QueriedKeyCount = publicSnapshotNumber(snapshot, "queriedKeyCount")
	return out
}

// publicSnapshotText reads one optional string field（空串省略）.
func publicSnapshotText(snapshot map[string]any, key string) *string {
	if text := OptionalSnapshotString(snapshot[key]); strings.TrimSpace(text) != "" {
		return &text
	}
	return nil
}

// publicSnapshotNumber reads one optional JSON number field（JSON 反序列化后
// 数值一律 float64，越界/NaN 场景由反序列化层兜底）.
func publicSnapshotNumber(snapshot map[string]any, key string) *int64 {
	if number, ok := snapshot[key].(float64); ok {
		value := int64(number)
		return &value
	}
	return nil
}

// LoadRelayBalanceSnapshotRecords mirrors
// loadAccountBalanceSnapshotRecordsByAccountIdsAsync：kind='relay_balance' 的
// stats 快照行按 account id 键控返回（900 分块 IN，模式对齐
// loadOAuthUsageSnapshots）。表主键是 (system_account_id, account_id, kind)，
// 同一 account_id 跨 owner 命名空间的多行按 Node output.set 语义后写覆盖。
// snapshot_json 解析失败的行整行跳过（读端降级：该账户不带快照，不失败）。
func (s *Service) LoadRelayBalanceSnapshotRecords(ctx context.Context, accountIDs []string) (map[string]*BalanceSnapshotRecord, error) {
	ids := []string{}
	seen := map[string]bool{}
	for _, id := range accountIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	result := map[string]*BalanceSnapshotRecord{}
	if len(ids) == 0 {
		return result, nil
	}
	const chunkSize = 900
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		rows, err := s.statsDB().QueryContext(ctx, s.store.Bind(`SELECT account_id, snapshot_json, next_refresh_after, updated_at
			FROM `+s.StatsTable("account_usage_snapshots")+`
			WHERE kind = 'relay_balance' AND account_id IN (`+accountscore.Placeholders(len(chunk))+`)`), accountscore.AnySlice(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				accountID        string
				snapshotJSON     string
				nextRefreshAfter sql.NullString
				updatedAt        string
			)
			if err := rows.Scan(&accountID, &snapshotJSON, &nextRefreshAfter, &updatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			record := &BalanceSnapshotRecord{NextRefreshAfter: nextRefreshAfter, UpdatedAt: updatedAt}
			if strings.TrimSpace(snapshotJSON) != "" {
				if err := json.Unmarshal([]byte(snapshotJSON), &record.Snapshot); err != nil {
					rows.Close()
					continue
				}
			}
			result[accountID] = record
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}
