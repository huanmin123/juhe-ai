package oauthrefresh

// B3-H11 修复：API Key 可用性排期激活的小时额度窗口作用域绑定重建。
//
// 排期同步只写 api_keys.status；小时额度判定读的是 statsagg 生成的
// usage_quota_hourly_windows，而该表的唯一来源是
// juhe_business.request_quota_hourly_window_scope_bindings（见
// internal/statsagg/stages_quota.go）。gateway 的人工创建 / 编辑 / 删除
// （internal/apikeys store.go syncQuotaHourlyWindowBinding）会维护绑定，排期
// 激活此前没有对应写侧，Key 被计划任务启用后绑定永久缺失、小时额度静默
// fail-open。
//
// 本文件把激活写侧放进排期同步事务：读取该 Key 的 owner 与 quota_limits_json，
// 用 internal/quotahourly 的同一判定/列集重建绑定；事务提交后给 statsagg
// 打脏，使既有窗口用量立即重算（与 gateway 提交后打脏同序）。

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/quotahourly"
)

// APIKeyScheduleActivationHook 返回生产激活 hook：在排期同步事务里重建该
// Key 的小时额度窗口绑定，事务提交后打脏。绑定写失败向上返回以回滚整笔
// 同步（宁可保持停用并让任务失败可见，也不把 Key 置为 active 却缺少额度
// 控制）；打脏是提交后的尾随效果，失败只告警，由调度轮 expiry 区间推进
// 收敛（与 gateway 语义一致）。
func (s *Store) APIKeyScheduleActivationHook(logger *slog.Logger) APIKeyActivationHook {
	if logger == nil {
		logger = slog.Default()
	}
	return APIKeyActivationHookFunc(func(ctx context.Context, tx *sql.Tx, apiKeyID, nowISO string) (func(), error) {
		ownerID, upserted, err := s.RebuildAPIKeyQuotaHourlyWindowBinding(ctx, tx, apiKeyID, nowISO)
		if err != nil {
			return nil, err
		}
		if !upserted {
			return nil, nil
		}
		return func() {
			if err := quotahourly.MarkAPIKeyDirty(ctx, s.db, ownerID, apiKeyID, nowISO); err != nil {
				logger.Warn("API Key 排期激活的小时额度窗口打脏失败，等待调度轮 expiry 区间推进收敛",
					"event", "api_key_schedule_activation_hourly_window_dirty_mark_failed",
					"apiKeyId", apiKeyID, "error", err.Error())
			}
		}, nil
	})
}

// RebuildAPIKeyQuotaHourlyWindowBinding mirrors gateway
// apikeys.syncQuotaHourlyWindowBinding for the api_key scope: it reads the
// owner and stored quota limits inside the sync transaction (the uncommitted
// status flip is already visible there), replaces the Key's binding row and
// reports whether a row was upserted (the caller gates the after-commit dirty
// mark on it, same as gateway's bindingUpserted). A missing row or a non-active
// status leaves no binding: the schedule sync only calls the hook after a
// successful non-active -> active flip, so this is a defensive guard. An
// unparseable quota document is returned as an error so the caller can decide
// (the sync evaluates the same document before flipping the status, so this arm
// is defensive and never the primary guard).
func (s *Store) RebuildAPIKeyQuotaHourlyWindowBinding(ctx context.Context, tx *sql.Tx, apiKeyID, nowISO string) (string, bool, error) {
	var ownerID string
	var quotaJSON sql.NullString
	err := tx.QueryRowContext(ctx, s.bind(`SELECT system_account_id, quota_limits_json
		FROM `+s.table("api_keys")+`
		WHERE id = ? AND status = 'active'`), apiKeyID).Scan(&ownerID, &quotaJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	upserted, err := quotahourly.SyncAPIKeyBinding(ctx, tx, ownerID, apiKeyID, quotaJSON.String, true, nowISO)
	if err != nil {
		return "", false, err
	}
	return ownerID, upserted, nil
}
