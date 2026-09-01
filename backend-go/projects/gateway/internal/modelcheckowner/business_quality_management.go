package modelcheckowner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

type QualityPolicyView struct {
	SystemAccountID          string `json:"systemAccountId"`
	Revision                 int    `json:"revision"`
	Profile                  string `json:"profile"`
	ManualEnforcementEnabled bool   `json:"manualEnforcementEnabled"`
	PenaltyThreshold         int    `json:"penaltyThreshold"`
	PenaltyAction            string `json:"penaltyAction"`
	RecoveryIntervalMinutes  int    `json:"recoveryIntervalMinutes"`
}
type QualityPolicyPatch struct {
	ExpectedRevision         int     `json:"expectedRevision"`
	Profile                  *string `json:"profile,omitempty"`
	ManualEnforcementEnabled *bool   `json:"manualEnforcementEnabled,omitempty"`
	PenaltyThreshold         *int    `json:"penaltyThreshold,omitempty"`
	PenaltyAction            *string `json:"penaltyAction,omitempty"`
	RecoveryIntervalMinutes  *int    `json:"recoveryIntervalMinutes,omitempty"`
}
type QualityScheduleInput struct {
	AccountID               string `json:"accountId"`
	Model                   string `json:"model"`
	IntervalMinutes         int    `json:"intervalMinutes"`
	Profile                 string `json:"profile"`
	PenaltyThreshold        int    `json:"penaltyThreshold"`
	PenaltyAction           string `json:"penaltyAction"`
	RecoveryIntervalMinutes int    `json:"recoveryIntervalMinutes"`
	Enabled                 *bool  `json:"enabled,omitempty"`
}
type QualitySchedulePatch struct {
	ExpectedRevision        int     `json:"expectedRevision"`
	Model                   *string `json:"model,omitempty"`
	IntervalMinutes         *int    `json:"intervalMinutes,omitempty"`
	Profile                 *string `json:"profile,omitempty"`
	PenaltyThreshold        *int    `json:"penaltyThreshold,omitempty"`
	PenaltyAction           *string `json:"penaltyAction,omitempty"`
	RecoveryIntervalMinutes *int    `json:"recoveryIntervalMinutes,omitempty"`
	Enabled                 *bool   `json:"enabled,omitempty"`
}
type QualityScheduleView struct {
	ID                              string  `json:"id"`
	SystemAccountID                 string  `json:"systemAccountId"`
	AccountID                       string  `json:"accountId"`
	AccountName                     string  `json:"accountName,omitempty"`
	ProviderCode                    string  `json:"providerCode,omitempty"`
	Model                           string  `json:"model"`
	IntervalMinutes                 int     `json:"intervalMinutes"`
	Profile                         string  `json:"profile"`
	PenaltyThreshold                int     `json:"penaltyThreshold"`
	PenaltyAction                   string  `json:"penaltyAction"`
	RecoveryIntervalMinutes         int     `json:"recoveryIntervalMinutes"`
	Enabled                         bool    `json:"enabled"`
	Revision                        int     `json:"revision"`
	NextRunAt                       string  `json:"nextRunAt"`
	LastRunID                       *string `json:"lastRunId,omitempty"`
	LastRunAt                       *string `json:"lastRunAt,omitempty"`
	LastRunStatus                   *string `json:"lastRunStatus,omitempty"`
	CurrentEnforcementAction        string  `json:"currentEnforcementAction,omitempty"`
	CurrentEnforcementRecoveryDueAt string  `json:"currentEnforcementRecoveryDueAt,omitempty"`
	CreatedAt                       string  `json:"createdAt"`
	UpdatedAt                       string  `json:"updatedAt"`
}
type QualityScheduleList struct {
	Items    []QualityScheduleView `json:"items"`
	Total    int                   `json:"total"`
	HasMore  bool                  `json:"hasMore"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"pageSize"`
}

type QualityManagement interface {
	Policy(context.Context, string) (QualityPolicyView, error)
	PatchPolicy(context.Context, string, QualityPolicyPatch) (QualityPolicyView, error)
	ListSchedules(context.Context, string, int, int) (QualityScheduleList, error)
	CreateSchedule(context.Context, string, QualityScheduleInput) (QualityScheduleView, error)
	PatchSchedule(context.Context, string, string, QualitySchedulePatch) (QualityScheduleView, error)
	DeleteSchedule(context.Context, string, string) (bool, error)
}
type BusinessQualityManager struct {
	db       *sql.DB
	postgres bool
}

func NewBusinessQualityManager(db *sql.DB, postgres bool) (*BusinessQualityManager, error) {
	if db == nil {
		return nil, errors.New("J3b Business quality database is required")
	}
	return &BusinessQualityManager{db: db, postgres: postgres}, nil
}

func (m *BusinessQualityManager) Policy(ctx context.Context, systemID string) (QualityPolicyView, error) {
	if strings.TrimSpace(systemID) == "" {
		return QualityPolicyView{}, errors.New("J3b quality policy system account is required")
	}
	var out QualityPolicyView
	var enabled int
	err := m.db.QueryRowContext(ctx, m.bind(`SELECT system_account_id,revision,profile,manual_enforcement_enabled,penalty_threshold,penalty_action,recovery_interval_minutes FROM `+m.table("model_quality_policies")+` WHERE system_account_id=?`), systemID).Scan(&out.SystemAccountID, &out.Revision, &out.Profile, &enabled, &out.PenaltyThreshold, &out.PenaltyAction, &out.RecoveryIntervalMinutes)
	if errors.Is(err, sql.ErrNoRows) {
		return QualityPolicyView{SystemAccountID: systemID, Revision: 0, Profile: "quick", ManualEnforcementEnabled: true, PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}, nil
	}
	if err != nil {
		return out, err
	}
	out.ManualEnforcementEnabled = enabled == 1
	return out, nil
}
func (m *BusinessQualityManager) PatchPolicy(ctx context.Context, systemID string, patch QualityPolicyPatch) (QualityPolicyView, error) {
	if err := validatePolicyPatch(patch); err != nil {
		return QualityPolicyView{}, err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return QualityPolicyView{}, err
	}
	defer tx.Rollback()
	current, err := m.policyTx(ctx, tx, systemID)
	if err != nil {
		return QualityPolicyView{}, err
	}
	if current.Revision != patch.ExpectedRevision {
		return QualityPolicyView{}, errors.New("模型质量检测配置已被其他操作修改，请刷新后重试")
	}
	next := current
	if patch.Profile != nil {
		next.Profile = *patch.Profile
	}
	if patch.ManualEnforcementEnabled != nil {
		next.ManualEnforcementEnabled = *patch.ManualEnforcementEnabled
	}
	if patch.PenaltyThreshold != nil {
		next.PenaltyThreshold = *patch.PenaltyThreshold
	}
	if patch.PenaltyAction != nil {
		next.PenaltyAction = *patch.PenaltyAction
	}
	if patch.RecoveryIntervalMinutes != nil {
		next.RecoveryIntervalMinutes = *patch.RecoveryIntervalMinutes
	}
	if next == current {
		return current, tx.Commit()
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if current.Revision == 0 {
		_, err = tx.ExecContext(ctx, m.bind(`INSERT INTO `+m.table("model_quality_policies")+` (system_account_id,revision,profile,manual_enforcement_enabled,penalty_threshold,penalty_action,recovery_interval_minutes,created_at,updated_at) VALUES (?,1,?,?,?,?,?,?,?)`), systemID, next.Profile, boolIntQuality(next.ManualEnforcementEnabled), next.PenaltyThreshold, next.PenaltyAction, next.RecoveryIntervalMinutes, now, now)
		next.Revision = 1
	} else {
		res, e := tx.ExecContext(ctx, m.bind(`UPDATE `+m.table("model_quality_policies")+` SET profile=?,manual_enforcement_enabled=?,penalty_threshold=?,penalty_action=?,recovery_interval_minutes=?,revision=revision+1,updated_at=? WHERE system_account_id=? AND revision=?`), next.Profile, boolIntQuality(next.ManualEnforcementEnabled), next.PenaltyThreshold, next.PenaltyAction, next.RecoveryIntervalMinutes, now, systemID, current.Revision)
		err = e
		if err == nil {
			n, _ := res.RowsAffected()
			if n != 1 {
				err = errors.New("模型质量检测配置已被其他操作修改，请刷新后重试")
			}
			next.Revision++
		}
	}
	if err != nil {
		return QualityPolicyView{}, err
	}
	return next, tx.Commit()
}
func (m *BusinessQualityManager) ListSchedules(ctx context.Context, systemID string, page, size int) (QualityScheduleList, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 50
	}
	var total int
	if err := m.db.QueryRowContext(ctx, m.bind(`SELECT COUNT(*) FROM `+m.table("model_quality_schedules")+` mqs JOIN `+m.table("accounts")+` a ON a.id=mqs.account_id AND a.deleted_at IS NULL WHERE mqs.system_account_id=?`), systemID).Scan(&total); err != nil {
		return QualityScheduleList{}, err
	}
	rows, err := m.db.QueryContext(ctx, m.bind(`SELECT mqs.id,mqs.system_account_id,mqs.account_id,a.name,a.provider_code,mqs.model,mqs.interval_minutes,mqs.profile,mqs.penalty_threshold,mqs.penalty_action,mqs.recovery_interval_minutes,mqs.enabled,mqs.revision,mqs.next_run_at,mqs.last_run_id,mqs.last_run_at,mqs.last_run_status,aqe.action,aqe.recovery_due_at,mqs.created_at,mqs.updated_at FROM `+m.table("model_quality_schedules")+` mqs JOIN `+m.table("accounts")+` a ON a.id=mqs.account_id AND a.deleted_at IS NULL LEFT JOIN `+m.table("account_quality_enforcements")+` aqe ON aqe.account_id=mqs.account_id AND aqe.state='active' WHERE mqs.system_account_id=? ORDER BY mqs.created_at DESC,mqs.id DESC LIMIT ? OFFSET ?`), systemID, size+1, (page-1)*size)
	if err != nil {
		return QualityScheduleList{}, err
	}
	defer rows.Close()
	items := make([]QualityScheduleView, 0, size)
	for rows.Next() {
		v, err := scanQualitySchedule(rows)
		if err != nil {
			return QualityScheduleList{}, err
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		return QualityScheduleList{}, err
	}
	more := len(items) > size
	if more {
		items = items[:size]
	}
	return QualityScheduleList{Items: items, Total: total, HasMore: more, Page: page, PageSize: size}, nil
}
func (m *BusinessQualityManager) CreateSchedule(ctx context.Context, systemID string, input QualityScheduleInput) (QualityScheduleView, error) {
	if err := validateSchedule(input); err != nil {
		return QualityScheduleView{}, err
	}
	if err := m.checkScheduleAccount(ctx, systemID, input.AccountID, input.Model); err != nil {
		return QualityScheduleView{}, err
	}
	now := time.Now().UTC()
	enabled := input.Enabled == nil || *input.Enabled
	id := newID("mqs")
	res, err := m.db.ExecContext(ctx, m.bind(`INSERT INTO `+m.table("model_quality_schedules")+` (id,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,enabled,revision,next_run_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,1,?,?,?) ON CONFLICT(system_account_id,account_id) DO NOTHING`), id, systemID, input.AccountID, input.Model, input.IntervalMinutes, input.Profile, input.PenaltyThreshold, input.PenaltyAction, input.RecoveryIntervalMinutes, boolIntQuality(enabled), now.Add(time.Duration(input.IntervalMinutes)*time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return QualityScheduleView{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return QualityScheduleView{}, errors.New("该账户已存在定时检查配置，请使用字段级更新")
	}
	return m.scheduleByID(ctx, systemID, id)
}
func (m *BusinessQualityManager) PatchSchedule(ctx context.Context, systemID, id string, patch QualitySchedulePatch) (QualityScheduleView, error) {
	if err := validateSchedulePatch(patch); err != nil {
		return QualityScheduleView{}, err
	}
	current, err := m.scheduleByID(ctx, systemID, id)
	if err != nil {
		return QualityScheduleView{}, err
	}
	if current.Revision != patch.ExpectedRevision {
		return QualityScheduleView{}, errors.New("定时检查配置已变化，请刷新后重试")
	}
	next := current
	if patch.Model != nil {
		next.Model = *patch.Model
	}
	if patch.IntervalMinutes != nil {
		next.IntervalMinutes = *patch.IntervalMinutes
	}
	if patch.Profile != nil {
		next.Profile = *patch.Profile
	}
	if patch.PenaltyThreshold != nil {
		next.PenaltyThreshold = *patch.PenaltyThreshold
	}
	if patch.PenaltyAction != nil {
		next.PenaltyAction = *patch.PenaltyAction
	}
	if patch.RecoveryIntervalMinutes != nil {
		next.RecoveryIntervalMinutes = *patch.RecoveryIntervalMinutes
	}
	if patch.Enabled != nil {
		next.Enabled = *patch.Enabled
	}
	if next.Model == current.Model && next.IntervalMinutes == current.IntervalMinutes && next.Profile == current.Profile && next.PenaltyThreshold == current.PenaltyThreshold && next.PenaltyAction == current.PenaltyAction && next.RecoveryIntervalMinutes == current.RecoveryIntervalMinutes && next.Enabled == current.Enabled {
		return current, nil
	}
	if err := m.checkScheduleAccount(ctx, systemID, next.AccountID, next.Model); err != nil {
		return QualityScheduleView{}, err
	}
	now := time.Now().UTC()
	res, err := m.db.ExecContext(ctx, m.bind(`UPDATE `+m.table("model_quality_schedules")+` SET model=?,interval_minutes=?,profile=?,penalty_threshold=?,penalty_action=?,recovery_interval_minutes=?,enabled=?,next_run_at=?,revision=revision+1,updated_at=? WHERE id=? AND system_account_id=? AND revision=?`), next.Model, next.IntervalMinutes, next.Profile, next.PenaltyThreshold, next.PenaltyAction, next.RecoveryIntervalMinutes, boolIntQuality(next.Enabled), now.Add(time.Duration(next.IntervalMinutes)*time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, systemID, current.Revision)
	if err != nil {
		return QualityScheduleView{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return QualityScheduleView{}, errors.New("定时检查配置已变化，请刷新后重试")
	}
	return m.scheduleByID(ctx, systemID, id)
}
func (m *BusinessQualityManager) DeleteSchedule(ctx context.Context, systemID, id string) (bool, error) {
	res, err := m.db.ExecContext(ctx, m.bind(`DELETE FROM `+m.table("model_quality_schedules")+` WHERE id=? AND system_account_id=?`), id, systemID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
func (m *BusinessQualityManager) policyTx(ctx context.Context, tx *sql.Tx, systemID string) (QualityPolicyView, error) {
	var out QualityPolicyView
	var enabled int
	err := tx.QueryRowContext(ctx, m.bind(`SELECT system_account_id,revision,profile,manual_enforcement_enabled,penalty_threshold,penalty_action,recovery_interval_minutes FROM `+m.table("model_quality_policies")+` WHERE system_account_id=?`), systemID).Scan(&out.SystemAccountID, &out.Revision, &out.Profile, &enabled, &out.PenaltyThreshold, &out.PenaltyAction, &out.RecoveryIntervalMinutes)
	if errors.Is(err, sql.ErrNoRows) {
		return QualityPolicyView{SystemAccountID: systemID, Profile: "quick", ManualEnforcementEnabled: true, PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}, nil
	}
	if err != nil {
		return out, err
	}
	out.ManualEnforcementEnabled = enabled == 1
	return out, nil
}
func (m *BusinessQualityManager) scheduleByID(ctx context.Context, systemID, id string) (QualityScheduleView, error) {
	row := m.db.QueryRowContext(ctx, m.bind(`SELECT mqs.id,mqs.system_account_id,mqs.account_id,a.name,a.provider_code,mqs.model,mqs.interval_minutes,mqs.profile,mqs.penalty_threshold,mqs.penalty_action,mqs.recovery_interval_minutes,mqs.enabled,mqs.revision,mqs.next_run_at,mqs.last_run_id,mqs.last_run_at,mqs.last_run_status,aqe.action,aqe.recovery_due_at,mqs.created_at,mqs.updated_at FROM `+m.table("model_quality_schedules")+` mqs JOIN `+m.table("accounts")+` a ON a.id=mqs.account_id AND a.deleted_at IS NULL LEFT JOIN `+m.table("account_quality_enforcements")+` aqe ON aqe.account_id=mqs.account_id AND aqe.state='active' WHERE mqs.id=? AND mqs.system_account_id=?`), id, systemID)
	v, err := scanQualitySchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return QualityScheduleView{}, errors.New("定时检查配置不存在")
	}
	return v, err
}
func (m *BusinessQualityManager) checkScheduleAccount(ctx context.Context, systemID, accountID, model string) error {
	var provider, profileID string
	err := m.db.QueryRowContext(ctx, m.bind(`SELECT provider_code,provider_protocol_profile_id FROM `+m.table("accounts")+` WHERE id=? AND system_account_id=? AND deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`), accountID, systemID).Scan(&provider, &profileID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("账户不存在、不是当前系统账户的自有账户或无权配置定时检查")
	}
	if err != nil {
		return err
	}
	profile, ok := modelcheckprofile.Find(provider, profileID)
	if !ok || profile.ID == "" {
		return errors.New("账户模型限制或供应商协议不支持定时检查模型")
	}
	upstreamMapping, err := resolveConfiguredUpstreamModelMapping(ctx, m.db, m.postgres, accountID, profile, model)
	if err != nil {
		return err
	}
	if upstreamMapping.UpstreamModel == "" {
		return errors.New("账户模型限制或供应商协议不支持定时检查模型")
	}
	return nil
}
func (m *BusinessQualityManager) table(name string) string {
	if m.postgres {
		return "juhe_business." + name
	}
	return name
}
func (m *BusinessQualityManager) bind(s string) string {
	if !m.postgres {
		return s
	}
	var b strings.Builder
	i := 0
	for _, r := range s {
		if r == '?' {
			i++
			fmt.Fprintf(&b, "$%d", i)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func scanQualitySchedule(row interface{ Scan(...any) error }) (QualityScheduleView, error) {
	var v QualityScheduleView
	var enabled int
	var accountName, providerCode, lastID, lastAt, lastStatus, enforcementAction, enforcementRecoveryDueAt sql.NullString
	err := row.Scan(&v.ID, &v.SystemAccountID, &v.AccountID, &accountName, &providerCode, &v.Model, &v.IntervalMinutes, &v.Profile, &v.PenaltyThreshold, &v.PenaltyAction, &v.RecoveryIntervalMinutes, &enabled, &v.Revision, &v.NextRunAt, &lastID, &lastAt, &lastStatus, &enforcementAction, &enforcementRecoveryDueAt, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	v.Enabled = enabled == 1
	if accountName.Valid {
		v.AccountName = accountName.String
	}
	if providerCode.Valid {
		v.ProviderCode = providerCode.String
	}
	if lastID.Valid {
		v.LastRunID = &lastID.String
	}
	if lastAt.Valid {
		v.LastRunAt = &lastAt.String
	}
	if lastStatus.Valid {
		v.LastRunStatus = &lastStatus.String
	}
	if enforcementAction.Valid {
		v.CurrentEnforcementAction = enforcementAction.String
	}
	if enforcementRecoveryDueAt.Valid {
		v.CurrentEnforcementRecoveryDueAt = enforcementRecoveryDueAt.String
	}
	return v, nil
}
func validatePolicyPatch(p QualityPolicyPatch) error {
	if p.ExpectedRevision < 0 {
		return errors.New("模型质量检测配置 revision 无效")
	}
	if p.Profile == nil && p.ManualEnforcementEnabled == nil && p.PenaltyThreshold == nil && p.PenaltyAction == nil && p.RecoveryIntervalMinutes == nil {
		return errors.New("模型质量检测配置没有变化")
	}
	return validateQualityValues(valueOr(p.Profile, "quick"), intOr(p.PenaltyThreshold, 70), valueOr(p.PenaltyAction, "fallback"), intOr(p.RecoveryIntervalMinutes, 10))
}
func validateSchedule(i QualityScheduleInput) error {
	if strings.TrimSpace(i.AccountID) == "" || strings.TrimSpace(i.Model) == "" {
		return errors.New("定时检查账户和模型不能为空")
	}
	if i.IntervalMinutes < 10 || i.IntervalMinutes > 10080 {
		return errors.New("定时检查间隔必须是 10 到 10080 的整数分钟")
	}
	return validateQualityValues(i.Profile, i.PenaltyThreshold, i.PenaltyAction, i.RecoveryIntervalMinutes)
}
func validateSchedulePatch(p QualitySchedulePatch) error {
	if p.ExpectedRevision < 1 {
		return errors.New("定时检查配置 revision 无效")
	}
	if p.Model == nil && p.IntervalMinutes == nil && p.Profile == nil && p.PenaltyThreshold == nil && p.PenaltyAction == nil && p.RecoveryIntervalMinutes == nil && p.Enabled == nil {
		return errors.New("定时检查配置没有变化")
	}
	if p.Model != nil && strings.TrimSpace(*p.Model) == "" {
		return errors.New("定时检查模型不能为空")
	}
	if p.IntervalMinutes != nil && (*p.IntervalMinutes < 10 || *p.IntervalMinutes > 10080) {
		return errors.New("定时检查间隔必须是 10 到 10080 的整数分钟")
	}
	if p.Profile != nil || p.PenaltyThreshold != nil || p.PenaltyAction != nil || p.RecoveryIntervalMinutes != nil {
		return validateQualityValues(valueOr(p.Profile, "quick"), intOr(p.PenaltyThreshold, 70), valueOr(p.PenaltyAction, "fallback"), intOr(p.RecoveryIntervalMinutes, 10))
	}
	return nil
}
func validateQualityValues(profile string, threshold int, action string, interval int) error {
	if profile != "quick" && profile != "full" {
		return errors.New("profile 必须为 quick 或 full")
	}
	if threshold < 40 || threshold > 100 {
		return errors.New("处罚阈值必须是 40 到 100 的整数")
	}
	if action != "disable" && action != "fallback" && action != "quality_isolate" {
		return errors.New("处罚方式无效")
	}
	if interval < 10 || interval > 10080 {
		return errors.New("恢复周期必须是 10 到 10080 分钟")
	}
	return nil
}
func valueOr(v *string, def string) string {
	if v == nil {
		return def
	}
	return *v
}
func intOr(v *int, def int) int {
	if v == nil {
		return def
	}
	return *v
}
func boolIntQuality(v bool) int {
	if v {
		return 1
	}
	return 0
}
