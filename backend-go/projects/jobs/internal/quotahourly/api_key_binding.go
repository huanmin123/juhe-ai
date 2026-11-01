// Package quotahourly 持有 jobs 侧 API Key 请求额度小时窗口作用域绑定的唯一写侧
// 契约。
//
// 权威写侧在 gateway internal/apikeys：
//   - syncQuotaHourlyWindowBinding（store.go）：API Key 创建 / 编辑 / 删除时
//     先 DELETE 该 Key 的 api_key 绑定，再在「active 且小时额度启用且窗口
//     1..720」时 upsert 单行；
//   - markQuotaHourlyWindowDirtyScopeAfterCommit（quota_hourly_dirty.go）：绑定
//     upsert 后提交后给 juhe_stats.usage_quota_hourly_window_dirty_scopes 打脏，
//     jobs statsagg 立即重算该 scope 的 usage_quota_hourly_windows。
//
// jobs 拥有 API Key 可用性排期同步：计划窗口把 Key 从非 active 翻为 active 时
// 必须重建同一个绑定（B3-H11：此前只翻状态，绑定永久缺失，statsagg 的
// request_quota_hourly_window_scope_bindings 唯一来源读不到该 Key，小时窗口
// 永远为 0，小时额度静默 fail-open）。
//
// 绑定行本身不存时区或窗口起点：statsagg 刷新时用 window_hours + 统计时区
// 计算滚动截止小时（stages_quota.go hourKey），因此跨进程契约只有行身份
// (system_account_id, 'api_key', api_key_id) / ('api_key', api_key_id) 与
// 校验后的 window_hours。判定口径与 maintenance 的
// pgSeedQuotaScopeBindingsFromAPIKeysCTE（active + hourly.enabled + 1..720）
// 以及 gateway ParseQuotaLimitsJSON + syncQuotaHourlyWindowBinding 对齐。
package quotahourly

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
)

// MaxWindowHours mirrors maxRequestQuotaHourlyWindowHours（gateway apikeys /
// gatewayquota，24 * 30）。
const MaxWindowHours = 24 * 30

// Execer 是绑定写侧使用的最小执行面：生产调用方传排期同步事务（*sql.Tx），
// 提交后打脏传连接池（*sql.DB），测试可直接传两者之一。
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// SyncAPIKeyBinding 复刻 gateway syncQuotaHourlyWindowBinding 的 api_key 分支：
// 先删除该 Key 的既有绑定（source_type='api_key' AND source_id=?），再在 active
// 且小时额度启用且窗口小时数合法时 upsert 单行。返回 true 表示写入了绑定行
// （调用方据此决定提交后是否打脏，与 gateway 的 bindingUpserted 门控同义）。
//
// 重复调用幂等：DELETE + ON CONFLICT DO UPDATE 保证每个 Key 至多一行；DELETE
// 先行使 created_at 每次都刷新为本次重建时间（gateway 同款 delete-then-insert
// 语义，ON CONFLICT 列集逐字段一致）。
func SyncAPIKeyBinding(ctx context.Context, q Execer, ownerID, apiKeyID, quotaLimitsJSON string, active bool, timestamp string) (bool, error) {
	if _, err := q.ExecContext(ctx, bind(`DELETE FROM juhe_business.request_quota_hourly_window_scope_bindings
		WHERE source_type = 'api_key' AND source_id = ?`), apiKeyID); err != nil {
		return false, err
	}
	if !active {
		return false, nil
	}
	hours, enabled, err := HourlyWindowHours(quotaLimitsJSON)
	if err != nil {
		return false, err
	}
	if !enabled {
		return false, nil
	}
	if _, err := q.ExecContext(ctx, bind(`INSERT INTO juhe_business.request_quota_hourly_window_scope_bindings
		(system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at)
		VALUES (?, 'api_key', ?, 'api_key', ?, ?, ?, ?)
		ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
			source_type = excluded.source_type,
			source_id = excluded.source_id,
			window_hours = excluded.window_hours,
			updated_at = excluded.updated_at`),
		ownerID, apiKeyID, apiKeyID, hours, timestamp, timestamp); err != nil {
		return false, err
	}
	return true, nil
}

// MarkAPIKeyDirty mirrors gateway markQuotaHourlyWindowDirtyScope /
// markPostgresRequestQuotaHourlyWindowDirtyScopes：generation 自增的脏范围
// upsert。调用方只在 SyncAPIKeyBinding 返回 true 后、主事务提交后调用（与
// gateway 的提交后尾随效果同序）；失败按调用方契约告警并可等待调度轮
// expiry 区间推进收敛。
func MarkAPIKeyDirty(ctx context.Context, q Execer, ownerID, apiKeyID, timestamp string) error {
	_, err := q.ExecContext(ctx, bind(`INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
		system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
	) VALUES (?, 'api_key', ?, 1, ?, ?)
	ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
		generation = juhe_stats.usage_quota_hourly_window_dirty_scopes.generation + 1,
		updated_at = EXCLUDED.updated_at`), ownerID, apiKeyID, timestamp, timestamp)
	return err
}

// HourlyWindowHours 从 api_keys.quota_limits_json 判定小时额度绑定窗口：
//   - 空串 / NULL：无绑定（hasWindow=false）；
//   - hourly 缺省、null 或 enabled != true：无绑定（hasWindow=false）。gateway
//     写侧归一化会剥离禁用窗口，因此该形态只可能来自历史或手工写入；按
//     maintenance 回填 CTE 的同一谓词（`hourly,enabled = 'true'`）处理，即
//     没有启用的小时额度就不需要绑定；
//   - hourly 存在且 enabled=true，但 JSON 非法、hours 非整数、缺省或不在
//     1..720：返回错误。此时无法确定窗口长度，静默不写绑定会让 active Key 的
//     小时额度不可判定（gateway ParseQuotaLimitsJSON 对同一文档同样报错），
//     调用方必须 fail closed 并保持该 Key 不可用；
//   - 合法：返回 hours 且 hasWindow=true。
//
// 只读取 hourly 的 enabled/hours；limit 由 gateway 写侧归一化保证。判定边界与
// maintenance pgSeedQuotaScopeBindingsFromAPIKeysCTE 的 SQL 谓词逐项对应。
func HourlyWindowHours(quotaLimitsJSON string) (int, bool, error) {
	trimmed := strings.TrimSpace(quotaLimitsJSON)
	if trimmed == "" {
		return 0, false, nil
	}
	var decoded struct {
		Hourly *struct {
			Enabled bool `json:"enabled"`
			Hours   int  `json:"hours"`
		} `json:"hourly"`
	}
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return 0, false, fmt.Errorf("API Key 额度限制 JSON 无法解析: %w", err)
	}
	if decoded.Hourly == nil || !decoded.Hourly.Enabled {
		return 0, false, nil
	}
	if decoded.Hourly.Hours < 1 || decoded.Hourly.Hours > MaxWindowHours {
		return 0, false, fmt.Errorf("小时额度窗口必须在 1-%d 之间", MaxWindowHours)
	}
	return decoded.Hourly.Hours, true, nil
}

func bind(query string) string { return sqldialect.BindSQL(query) }
