package main

// BUG-0194 方案 B 装配：J1 探针观测 → usagewriter 使用记录。
//
// go-only 形态探针由 jobs 直连上游，Node 时代经 /v1 派发链天然产生的
// account_health_check / cooldown_retest / runtime_recovery_probe 使用记录
// 在此补齐（使用记录页来源筛选、account_health_hourly 原聚合臂与用量统计
// 的数据源）。
//
// 作用域解析与网关请求时语义同构：
//   - 自有账户（无 authorization_instance_authorization_id）→ owner 元组；
//   - 授权实例账户 → account_authorized 元组（ra 资源人/授权 ID/来源对）；
//   - 分组绑定存在且分组授权可解析 → group_authorized 元组；解析不到的
//     作用域留空，由 usagewriter.NormalizeUsageRecordInput 完整性规则
//     整组清空（安全网），保证不会写入半截作用域。

import (
	"context"
	"database/sql"
	"errors"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

// probeUsageWriter 是探针观测到使用记录的适配器（业务库作用域解析 +
// usagewriter 投递）。
type probeUsageWriter struct {
	business *businessDB
	writer   *usagewriter.Writer
	clock    usagewriter.Clock
}

// wireProbeUsageRecorder 装配探针使用记录写入面。usagewriter 未装配（writer
// 为空）时返回错误：用量记录是探针观测的正式持久面，装配失败按降级 warn 由
// 调用方处理（runner 未绑定即跳过补记）。
func (a *workerAssembly) wireProbeUsageRecorder() (accounthealth.ProbeUsageRecorder, error) {
	if a.writer == nil {
		return nil, errors.New("usagewriter 未装配，探针观测无法补记使用记录")
	}
	business, err := openBusinessDB(a, "probe-usage")
	if err != nil {
		return nil, err
	}
	a.addCloser(business.close)
	return &probeUsageWriter{business: business, writer: a.writer, clock: usagewriter.SystemClock{}}, nil
}

// RecordProbeUsage 实现 accounthealth.ProbeUsageRecorder：解析账户归属与
// 作用域五元组，归一化后经 usagewriter 投递。账户已删除时跳过；作用域解析
// 失败降级为无作用域记录（归一化清空对应组），不因查询失败丢弃观测。
func (w *probeUsageWriter) RecordProbeUsage(ctx context.Context, observation accounthealth.ProbeUsageObservation) error {
	if w == nil || w.writer == nil {
		return nil
	}
	input := usagewriter.UsageRecordInput{
		TrafficSource: observation.TrafficSource,
		AccountID:     observation.AccountID,
		Endpoint:      observation.Endpoint,
		Model:         observation.Model,
		Success:       observation.Success,
		CreatedAt:     observation.ObservedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if observation.StatusCode != 0 {
		statusCode := observation.StatusCode
		input.StatusCode = &statusCode
	}
	if observation.ErrorCode != "" {
		input.ErrorCode = observation.ErrorCode
	}
	if observation.ErrorMessage != "" {
		input.ErrorMessage = observation.ErrorMessage
	}
	w.resolveScope(ctx, observation.AccountID, &input)
	if err := w.writer.Enqueue(ctx, input); err != nil {
		return err
	}
	return nil
}

type probeUsageScopeRow struct {
	providerCode            sql.NullString
	systemAccountID         sql.NullString
	instanceAuthorizationID sql.NullString
	resourceOwner           sql.NullString
	authorizationStatus     sql.NullString
	effectiveSourceTeamID   sql.NullString
}

// resolveScope 按账户行 + 授权实例 + 分组绑定补齐作用域五元组；查询失败
// 静默保留无作用域输入（归一化整组清空，不阻塞观测补记）。
func (w *probeUsageWriter) resolveScope(ctx context.Context, accountID string, input *usagewriter.UsageRecordInput) {
	var row probeUsageScopeRow
	err := w.business.db.QueryRowContext(ctx, w.business.bind(`SELECT a.provider_code, a.system_account_id,
			a.authorization_instance_authorization_id,
			ra.resource_owner_system_account_id, ra.status, ra.effective_source_team_id
		FROM `+w.business.table("accounts")+` a
		LEFT JOIN `+w.business.table("resource_authorizations")+` ra ON ra.id = a.authorization_instance_authorization_id
		WHERE a.id = ? AND a.deleted_at IS NULL`), accountID).Scan(
		&row.providerCode, &row.systemAccountID, &row.instanceAuthorizationID,
		&row.resourceOwner, &row.authorizationStatus, &row.effectiveSourceTeamID)
	if err != nil {
		return
	}
	if input.ProviderCode == "" {
		input.ProviderCode = row.providerCode.String
	}
	input.SystemAccountID = row.systemAccountID.String
	if row.instanceAuthorizationID.Valid && row.instanceAuthorizationID.String != "" {
		input.AccountOwnerSystemAccountID = row.resourceOwner.String
		input.AccountAccessType = usagewriter.AccountAccessTypeAccountAuthorized
		input.AccountAuthorizationID = row.instanceAuthorizationID.String
		if row.effectiveSourceTeamID.Valid && row.effectiveSourceTeamID.String != "" {
			input.AccountAuthorizationSourceType = usagewriter.AuthorizationSourceTypeTeam
			input.AccountAuthorizationSourceTeamID = row.effectiveSourceTeamID.String
		} else {
			input.AccountAuthorizationSourceType = usagewriter.AuthorizationSourceTypeManual
		}
	} else {
		input.AccountOwnerSystemAccountID = row.systemAccountID.String
		input.AccountAccessType = usagewriter.AccountAccessTypeOwner
	}

	var groupID, groupOwner, groupAuthzID, groupTeamID sql.NullString
	// 分组授权过期过滤与读侧同口径（statreads/accountusage.go 的
	// rfc3339Millis）：expires_at 以 RFC3339 毫秒 UTC 文本
	// （2006-01-02T15:04:05.000Z）存储，同格式字典序比较，NULL 视为不过期。
	now := w.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	err = w.business.db.QueryRowContext(ctx, w.business.bind(`SELECT ga.group_id, g.system_account_id,
			ra.id, ra.effective_source_team_id
		FROM `+w.business.table("group_accounts")+` ga
		JOIN `+w.business.table("groups")+` g ON g.id = ga.group_id
		LEFT JOIN `+w.business.table("resource_authorizations")+` ra
			ON ra.resource_type = 'group' AND ra.resource_id = ga.group_id
			AND ra.grantee_system_account_id = ?
			AND ra.status = 'active'
			AND (ra.expires_at IS NULL OR ra.expires_at > ?)
		WHERE ga.account_id = ? AND ga.enabled = 1
		ORDER BY ga.updated_at DESC, ga.group_id ASC LIMIT 1`),
		row.systemAccountID.String, now, accountID).Scan(&groupID, &groupOwner, &groupAuthzID, &groupTeamID)
	if err != nil || groupID.String == "" || groupOwner.String == "" || !groupAuthzID.Valid || groupAuthzID.String == "" {
		return
	}
	input.GroupID = groupID.String
	input.GroupOwnerSystemAccountID = groupOwner.String
	input.GroupAccessType = usagewriter.GroupAccessTypeAuthorized
	input.GroupAuthorizationID = groupAuthzID.String
	if groupTeamID.Valid && groupTeamID.String != "" {
		input.GroupAuthorizationSourceType = usagewriter.AuthorizationSourceTypeTeam
		input.GroupAuthorizationSourceTeamID = groupTeamID.String
	} else {
		input.GroupAuthorizationSourceType = usagewriter.AuthorizationSourceTypeManual
	}
}
