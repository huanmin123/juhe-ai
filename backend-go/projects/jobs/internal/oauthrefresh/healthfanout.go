package oauthrefresh

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// 授权过期 sweep 的健康任务输入 fanout（T6d 组合根 GrantFinalizer 的可移植
// Node 等价物）。
//
// Node 权威源（resource-authorization-write.repository.ts:995-1002 的第三段
// 下游副作用）：syncResourceAuthorizationGrantRuntimeAsync 每个翻转的 grant
// 调 enqueueAccountHealthJobsInputsForAuthorizationSourceInTransactionAsync
// （account-health-jobs-input-authorization-fanout.repository.ts:51-78）——
// resource_type='account' 时，把每个授权实例账户（authorization_instance_
// source_account_id = resource_id）经
// reserveAndEnqueueAccountHealthJobsInputInTransactionAsync
// （kind='snapshot'，reason='authorization_grant_changed'）预留版本并入
// outbox。全部写入与 sweep 同事务（GrantFinalizer 持有 *sql.Tx）。
//
// 未移植段（显式登记，不静默）：同函数的 runtime sync（resource_authorizations
// 投影 + refreshResourceAuthorizationEffectiveSourceAsync）与 quota 窗口
// scope bindings 属 gateway authz sync 域（gateway internal/authz，jobs 侧
// 不可 import），由组合根在 sweep 后打点交接日志并在 jobregistry GoBinding
// 登记冻结依据。

// authorizationFanoutProviderCodes mirrors the Node whitelist
// (account-health-jobs-input-authorization-fanout.repository.ts:59).
var authorizationFanoutProviderCodes = []string{
	"gpt", "openai", "xai", "anthropic", "deepseek", "glm", "gemini", "hybrid",
}

// authorizationFanoutAccountTypes mirrors the Node type whitelist
// (account-health-jobs-input-authorization-fanout.repository.ts:60).
var authorizationFanoutAccountTypes = []string{"api_key", "oauth", "google_oauth"}

// AuthorizationGrantHealthFanoutReason mirrors the Node reason constant
// ('authorization_grant_changed').
const AuthorizationGrantHealthFanoutReason = "authorization_grant_changed"

// EnqueueAccountHealthInputsForAuthorizationSourceTx mirrors
// enqueueAccountHealthJobsInputsForAuthorizationSourceInTransactionAsync：
// resource_type != 'account' 直接返回 0；否则对每个命中白名单的授权实例账户
// 预留下一个输入版本并写入 pending outbox 行（kind='snapshot'）。返回入队
// 数。必须在 sweep 事务内调用（PG 命中行 FOR UPDATE，与 Node 一致）。
func (s *Store) EnqueueAccountHealthInputsForAuthorizationSourceTx(ctx context.Context, tx *sql.Tx, resourceType, resourceID, reason string) (int, error) {
	if strings.TrimSpace(resourceType) != "account" {
		return 0, nil
	}
	now := s.nowISO()
	lockSuffix := ""
	if s.pg {
		lockSuffix = `
		FOR UPDATE`
	}
	query := fmt.Sprintf(`SELECT id, config_revision, dispatch_revision
		FROM %s
		WHERE authorization_instance_source_account_id = ?
			AND deleted_at IS NULL
			AND provider_code IN (%s)
			AND type IN (%s)
		ORDER BY id ASC%s`, s.table("accounts"),
		sqlPlaceholders(len(authorizationFanoutProviderCodes)),
		sqlPlaceholders(len(authorizationFanoutAccountTypes)), lockSuffix)
	args := []any{strings.TrimSpace(resourceID)}
	for _, code := range authorizationFanoutProviderCodes {
		args = append(args, code)
	}
	for _, accountType := range authorizationFanoutAccountTypes {
		args = append(args, accountType)
	}
	rows, err := tx.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return 0, err
	}
	type instanceAccount struct {
		id               string
		configRevision   int64
		dispatchRevision int64
	}
	instances := []instanceAccount{}
	for rows.Next() {
		var (
			id                               string
			configRevision, dispatchRevision sql.NullInt64
		)
		if err := rows.Scan(&id, &configRevision, &dispatchRevision); err != nil {
			rows.Close()
			return 0, err
		}
		instances = append(instances, instanceAccount{id: id, configRevision: configRevision.Int64, dispatchRevision: dispatchRevision.Int64})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	enqueued := 0
	for _, instance := range instances {
		version, err := s.reserveAccountHealthInputVersionTx(ctx, tx, instance.id, now)
		if err != nil {
			return enqueued, err
		}
		if _, err := tx.ExecContext(ctx, s.bind(fmt.Sprintf(`
    INSERT INTO account_health_jobs_input_outbox (
      event_id, account_id, input_version, event_kind, reason,
      config_revision, dispatch_revision, status, available_at,
      created_at, updated_at
    ) VALUES (?, ?, ?, 'snapshot', ?, ?, ?, 'pending', ?, ?, ?)`)),
			newSweepEventID(), instance.id, version, reason,
			instance.configRevision, instance.dispatchRevision, now, now, now); err != nil {
			return enqueued, err
		}
		enqueued++
	}
	return enqueued, nil
}

func (s *Store) reserveAccountHealthInputVersionTx(ctx context.Context, tx *sql.Tx, accountID, now string) (int64, error) {
	normalized := strings.TrimSpace(accountID)
	if normalized == "" {
		return 0, fmt.Errorf("J1 snapshot version 缺少 account ID")
	}
	lockSuffix := ""
	if s.pg {
		lockSuffix = " FOR UPDATE"
	}
	var current sql.NullInt64
	err := tx.QueryRowContext(ctx, s.bind("SELECT current_version FROM "+s.table("account_health_jobs_input_versions")+" WHERE account_id = ?"+lockSuffix), normalized).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	next := int64(1)
	if err == nil && current.Valid {
		next = current.Int64 + 1
	}
	if err == nil && current.Valid {
		if _, err := tx.ExecContext(ctx, s.bind("UPDATE "+s.table("account_health_jobs_input_versions")+" SET current_version = ?, reserved_at = ? WHERE account_id = ?"), next, now, normalized); err != nil {
			return 0, err
		}
		return next, nil
	}
	if _, err := tx.ExecContext(ctx, s.bind("INSERT INTO "+s.table("account_health_jobs_input_versions")+" (account_id, current_version, reserved_at) VALUES (?, ?, ?)"), normalized, next, now); err != nil {
		return 0, err
	}
	return next, nil
}

func sqlPlaceholders(count int) string {
	return strings.TrimRight(strings.Repeat("?, ", count), ", ")
}

func newSweepEventID() string {
	buffer := make([]byte, 16)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}
