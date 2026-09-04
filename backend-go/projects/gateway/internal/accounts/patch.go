package accounts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// PatchChange mirrors AccountManagementPatchChange.
type PatchChange struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// PatchResult mirrors the PATCH response payload: { id, configRevision,
// changedFields } plus the fields the operation log needs.
type PatchResult struct {
	ID                   string        `json:"id"`
	ConfigRevision       int64         `json:"configRevision"`
	ChangedFields        []string      `json:"changedFields"`
	Name                 string        `json:"-"`
	OwnerSystemAccountID string        `json:"-"`
	Changes              []PatchChange `json:"-"`
	Tags                 []TagSummary  `json:"-"`
}

// PatchInput is the validated basic-edit payload: the account-edit-basic
// editable field set plus the expectedConfigRevision optimistic lock. Nil
// pointers mean the field was absent (undefined vs null distinction follows
// the Node optional/nullable schema pairs).
type PatchInput struct {
	ExpectedConfigRevision      int64
	Name                        *string
	Notes                       *string
	Status                      *string
	ConcurrencyLimit            *int
	Priority                    *int
	SuperPriorityEnabled        *bool
	FallbackEnabled             *bool
	Schedulable                 *bool
	Credentials                 Credentials
	CredentialsPresent          bool
	SupportedModels             []string
	SupportedModelsPresent      bool
	HealthCheckModel            *string
	HealthCheckEndpointMode     *string
	Tags                        []string
	TagsPresent                 bool
	AccountExpiresAt            *string
	AccountExpiresAtPresent     bool
	AvailabilitySchedule        any
	AvailabilitySchedulePresent bool
	ClearFailureState           bool
}

// accountPatchChangeLabel mirrors accountPatchChangeLabel (credentials.*
// fields collapse to 凭据).
func accountPatchChangeLabel(field string) string {
	if strings.HasPrefix(field, "credentials.") {
		return "凭据"
	}
	switch field {
	case "name":
		return "名称"
	case "notes":
		return "备注"
	case "credentials":
		return "凭据"
	case "status":
		return "状态"
	case "concurrencyLimit":
		return "并发限制"
	case "priority":
		return "优先级"
	case "superPriorityEnabled":
		return "超级优先"
	case "fallbackEnabled":
		return "降级备用"
	case "supportedModels":
		return "支持模型"
	case "healthCheckModel":
		return "检查模型"
	case "healthCheckEndpointMode":
		return "检查协议"
	case "tags":
		return "标签"
	case "proxyProfileId":
		return "代理"
	case "schedulable":
		return "参与调度"
	case "accountExpiresAt":
		return "过期时间"
	case "availabilitySchedule":
		return "时间计划"
	case "clearFailureState":
		return "异常恢复"
	default:
		return field
	}
}

// Patch mirrors patchAccountManagementAsync restricted to the basic editable
// field set: scope-checked row load, config_revision CAS (409 on mismatch),
// field-wise diff, tags/ models/ search-terms maintenance and the revision
// increment. Returns (nil, nil) when the account is missing or outside the
// access scope.
func (s *Store) Patch(ctx context.Context, accountID string, input PatchInput, access AccessScope) (*PatchResult, error) {
	ctx = ensureCtx(ctx)
	if input.ExpectedConfigRevision < 1 {
		return nil, &ValidationError{Message: "账户配置版本无效"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	scoped := access.manageableID()
	scopeClause := ""
	args := []any{strings.TrimSpace(accountID)}
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row struct {
		id                      string
		configRevision          int64
		systemAccountID         string
		name                    string
		notes                   sql.NullString
		accountType             string
		credentialsEncrypted    string
		status                  string
		concurrencyLimit        int
		priority                int
		superPriorityEnabled    int
		fallbackEnabled         int
		schedulable             int
		availabilitySchedule    sql.NullString
		accountExpiresAt        sql.NullString
		lastErrorCode           sql.NullString
		lastErrorMessage        sql.NullString
		lastErrorTraceID        sql.NullString
		cooldownUntil           sql.NullString
		healthCheckModel        string
		healthCheckEndpointMode string
		providerCode            string
	}
	err = tx.QueryRowContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision,
			accounts.system_account_id, accounts.name, accounts.notes, accounts.type,
			accounts.credentials_encrypted, accounts.status, accounts.concurrency_limit,
			accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled,
			accounts.schedulable, accounts.availability_schedule_json, accounts.account_expires_at,
			accounts.last_error_code, accounts.last_error_message, accounts.last_error_trace_id,
			accounts.cooldown_until, accounts.health_check_model, accounts.health_check_endpoint_mode,
			accounts.provider_code
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.configRevision, &row.systemAccountID, &row.name, &row.notes,
		&row.accountType, &row.credentialsEncrypted, &row.status, &row.concurrencyLimit,
		&row.priority, &row.superPriorityEnabled, &row.fallbackEnabled, &row.schedulable,
		&row.availabilitySchedule, &row.accountExpiresAt, &row.lastErrorCode,
		&row.lastErrorMessage, &row.lastErrorTraceID, &row.cooldownUntil,
		&row.healthCheckModel, &row.healthCheckEndpointMode, &row.providerCode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !access.canAccessAll() && row.systemAccountID != access.ViewerID {
		return nil, nil
	}
	if row.configRevision != input.ExpectedConfigRevision {
		return nil, &RevisionConflictError{Message: RevisionConflictMessage}
	}

	now := s.now()
	nowISO := isoMillis(now)
	changes := []PatchChange{}
	addChange := func(field string, before, after any) {
		changes = append(changes, PatchChange{Field: field, Before: before, After: after})
	}
	sets := []string{}
	setArgs := []any{}

	if input.ClearFailureState {
		addChange("clearFailureState", false, true)
		sets = append(sets,
			"last_error_code = NULL", "last_error_message = NULL", "last_error_trace_id = NULL",
			"cooldown_until = NULL", "health_check_failure_count = 0", "health_check_failure_started_at = NULL",
			"cooldown_retest_failure_count = 0", "cooldown_retest_observation_started_at = NULL",
			"cooldown_retest_last_at = NULL", "cooldown_retest_last_status_code = NULL")
	}

	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			return nil, &ValidationError{Message: "账户名称不能为空"}
		}
		if len([]rune(name)) > maxAccountNameLength {
			return nil, &ValidationError{Message: "账户名称不能超过 128 个字符"}
		}
		if name != row.name {
			addChange("name", row.name, name)
			sets = append(sets, "name = ?")
			setArgs = append(setArgs, name)
		}
	}
	if input.Notes != nil {
		trimmed := strings.TrimSpace(*input.Notes)
		var next sql.NullString
		if trimmed != "" {
			next = sql.NullString{String: trimmed, Valid: true}
		}
		if next.Valid != row.notes.Valid || next.String != row.notes.String {
			addChange("notes", nullPtrString(row.notes), nullPtrString(next))
			sets = append(sets, "notes = ?")
			setArgs = append(setArgs, next)
		}
	}
	if input.Status != nil {
		if !accountStatusValues[*input.Status] {
			return nil, &ValidationError{Message: "账户状态无效"}
		}
		if *input.Status != row.status {
			addChange("status", row.status, *input.Status)
			sets = append(sets, "status = ?")
			setArgs = append(setArgs, *input.Status)
		}
	}
	if input.ConcurrencyLimit != nil {
		if *input.ConcurrencyLimit < 1 {
			return nil, &ValidationError{Message: "并发限制必须是大于 0 的整数"}
		}
		if *input.ConcurrencyLimit != row.concurrencyLimit {
			addChange("concurrencyLimit", row.concurrencyLimit, *input.ConcurrencyLimit)
			sets = append(sets, "concurrency_limit = ?")
			setArgs = append(setArgs, *input.ConcurrencyLimit)
		}
	}
	if input.Priority != nil {
		if *input.Priority < 0 {
			return nil, &ValidationError{Message: "优先级必须是大于等于 0 的整数"}
		}
		if *input.Priority != row.priority {
			addChange("priority", row.priority, *input.Priority)
			sets = append(sets, "priority = ?")
			setArgs = append(setArgs, *input.Priority)
		}
	}
	if input.SuperPriorityEnabled != nil {
		next := boolInt(*input.SuperPriorityEnabled)
		if next != row.superPriorityEnabled {
			if next == 1 && boolInt(input.FallbackEnabled != nil && *input.FallbackEnabled) == 1 {
				return nil, &ValidationError{Message: "超级优先和降级备用不能同时开启"}
			}
			addChange("superPriorityEnabled", row.superPriorityEnabled == 1, *input.SuperPriorityEnabled)
			sets = append(sets, "super_priority_enabled = ?")
			setArgs = append(setArgs, next)
		}
	}
	if input.FallbackEnabled != nil {
		next := boolInt(*input.FallbackEnabled)
		if next != row.fallbackEnabled {
			if next == 1 && boolInt(input.SuperPriorityEnabled != nil && *input.SuperPriorityEnabled) == 1 {
				return nil, &ValidationError{Message: "超级优先和降级备用不能同时开启"}
			}
			addChange("fallbackEnabled", row.fallbackEnabled == 1, *input.FallbackEnabled)
			sets = append(sets, "fallback_enabled = ?")
			setArgs = append(setArgs, next)
		}
	}
	if input.Schedulable != nil {
		next := boolInt(*input.Schedulable)
		if next != row.schedulable {
			addChange("schedulable", row.schedulable == 1, *input.Schedulable)
			sets = append(sets, "schedulable = ?")
			setArgs = append(setArgs, next)
		}
	}
	if input.AccountExpiresAtPresent {
		var next sql.NullString
		if input.AccountExpiresAt != nil && strings.TrimSpace(*input.AccountExpiresAt) != "" {
			canonical, valid := canonicalRFC3339(*input.AccountExpiresAt)
			if !valid {
				return nil, &ValidationError{Message: "账户套餐到期时间必须是有效时间字符串"}
			}
			next = sql.NullString{String: canonical, Valid: true}
		}
		if next.Valid != row.accountExpiresAt.Valid || next.String != row.accountExpiresAt.String {
			addChange("accountExpiresAt", nullPtrString(row.accountExpiresAt), nullPtrString(next))
			sets = append(sets, "account_expires_at = ?")
			setArgs = append(setArgs, next)
		}
	}
	if input.AvailabilitySchedulePresent {
		schedule, err := NormalizeSchedule(input.AvailabilitySchedule)
		if err != nil {
			return nil, err
		}
		var next sql.NullString
		if raw, ok := ScheduleJSON(schedule); ok {
			next = sql.NullString{String: raw, Valid: true}
		}
		if next.Valid != row.availabilitySchedule.Valid || next.String != row.availabilitySchedule.String {
			addChange("availabilitySchedule", parseScheduleOrNull(row.availabilitySchedule), schedule)
			sets = append(sets, "availability_schedule_json = ?", "availability_schedule_next_check_at = ?")
			setArgs = append(setArgs, next, scheduleNextCheckArg(schedule, now))
		}
	}

	// Health check model: must remain inside the supported model set.
	if input.HealthCheckModel != nil || input.HealthCheckEndpointMode != nil || input.SupportedModelsPresent {
		supportedModels := []string{}
		modelRows, err := tx.QueryContext(ctx, s.bind(`SELECT model FROM `+s.table("account_supported_models")+`
			WHERE account_id = ? ORDER BY model ASC`), row.id)
		if err != nil {
			return nil, err
		}
		for modelRows.Next() {
			var model string
			if err := modelRows.Scan(&model); err != nil {
				modelRows.Close()
				return nil, err
			}
			supportedModels = append(supportedModels, model)
		}
		modelRows.Close()
		if err := modelRows.Err(); err != nil {
			return nil, err
		}
		if input.SupportedModelsPresent {
			next, err := normalizeSupportedModelsInput(anySliceOrNil(input.SupportedModels))
			if err != nil {
				return nil, err
			}
			if err := assertSupportedModelsRequired(next); err != nil {
				return nil, err
			}
			if !stringSlicesEqual(supportedModels, next) {
				addChange("supportedModels", supportedModels, next)
				if err := s.replaceAccountSupportedModels(ctx, tx, row.id, row.providerCode, next, nowISO); err != nil {
					return nil, err
				}
				supportedModels = next
			}
		}
		if input.HealthCheckModel != nil {
			next, err := normalizedHealthCheckModel(*input.HealthCheckModel, supportedModels)
			if err != nil {
				return nil, err
			}
			if next != strings.TrimSpace(row.healthCheckModel) {
				addChange("healthCheckModel", strings.TrimSpace(row.healthCheckModel), next)
				sets = append(sets, "health_check_model = ?")
				setArgs = append(setArgs, next)
			}
		}
		if input.HealthCheckEndpointMode != nil {
			if !accountHealthCheckEndpointModes[*input.HealthCheckEndpointMode] {
				return nil, &ValidationError{Message: "账户参数无效"}
			}
			if *input.HealthCheckEndpointMode != row.healthCheckEndpointMode {
				addChange("healthCheckEndpointMode", row.healthCheckEndpointMode, *input.HealthCheckEndpointMode)
				sets = append(sets, "health_check_endpoint_mode = ?")
				setArgs = append(setArgs, *input.HealthCheckEndpointMode)
			}
		}
	}

	// Credentials: editable-key merge into the decrypted record, then re-seal
	// with fresh fingerprint/mask columns.
	if input.CredentialsPresent {
		current := Credentials{}
		if err := DecryptJSON(s.secret, row.credentialsEncrypted, &current); err != nil {
			return nil, err
		}
		next := Credentials{}
		for key, value := range current {
			next[key] = value
		}
		for key, value := range input.Credentials {
			next[key] = value
		}
		source, err := requiredAccountCredentialSource(row.accountType, next)
		if err != nil {
			return nil, err
		}
		sealed, err := EncryptJSON(s.secret, map[string]any(next))
		if err != nil {
			return nil, err
		}
		fingerprint := accountCredentialFingerprint(source)
		addChange("credentials", "已设置", "已变更")
		sets = append(sets, "credentials_encrypted = ?", "credential_fingerprint = ?", "credential_mask = ?")
		setArgs = append(setArgs, sealed, fingerprint, MaskSecret(source))
	}

	// Tags: replace through the shared tag maintenance.
	var savedTags []TagSummary
	if input.TagsPresent {
		tagNames, err := normalizeAccountTagNamesInput(anySliceOrNil(input.Tags))
		if err != nil {
			return nil, err
		}
		currentTags := []TagSummary{}
		tagRows, err := tx.QueryContext(ctx, s.bind(`SELECT account_tags.id, account_tags.name
			FROM `+s.table("account_tag_bindings")+` account_tag_bindings
			INNER JOIN `+s.table("account_tags")+` account_tags
				ON account_tags.id = account_tag_bindings.tag_id
			WHERE account_tag_bindings.account_id = ?
			ORDER BY account_tags.name ASC, account_tags.id ASC`), row.id)
		if err != nil {
			return nil, err
		}
		for tagRows.Next() {
			var tag TagSummary
			if err := tagRows.Scan(&tag.ID, &tag.Name); err != nil {
				tagRows.Close()
				return nil, err
			}
			currentTags = append(currentTags, tag)
		}
		tagRows.Close()
		if err := tagRows.Err(); err != nil {
			return nil, err
		}
		if !tagListsEqual(currentTags, tagNames) {
			addChange("tags", currentTags, tagNames)
			savedTags, err = s.replaceAccountTags(ctx, tx, row.id, row.systemAccountID, tagNames, nowISO)
			if err != nil {
				return nil, err
			}
		}
	}

	result := &PatchResult{
		ID:                   row.id,
		ConfigRevision:       row.configRevision,
		ChangedFields:        []string{},
		Name:                 row.name,
		OwnerSystemAccountID: row.systemAccountID,
		Changes:              changes,
	}
	if len(changes) == 0 {
		return result, nil
	}
	// config_revision = config_revision + 1 with the CAS guard re-checked.
	sets = append(sets, "config_revision = config_revision + 1", "updated_at = ?")
	setArgs = append(setArgs, nowISO)
	updateArgs := append(append([]any{}, setArgs...), row.id, row.configRevision)
	exec, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` SET
		`+strings.Join(sets, ", ")+`
		WHERE id = ? AND config_revision = ? AND deleted_at IS NULL`), updateArgs...)
	if err != nil {
		if duplicate := duplicateAccountNameError(err, row.name); duplicate != nil {
			return nil, duplicate
		}
		return nil, err
	}
	if affected, _ := exec.RowsAffected(); affected != 1 {
		return nil, &RevisionConflictError{Message: RevisionConflictMessage}
	}
	for _, change := range changes {
		result.ChangedFields = append(result.ChangedFields, change.Field)
	}
	result.ConfigRevision = row.configRevision + 1
	result.Tags = savedTags
	return result, tx.Commit()
}

func scheduleNextCheckArg(schedule *AvailabilitySchedule, now time.Time) sql.NullString {
	if raw, ok := NextScheduleCheckAt(schedule, now); ok {
		return sql.NullString{String: raw, Valid: true}
	}
	return sql.NullString{}
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func tagListsEqual(tags []TagSummary, names []string) bool {
	if len(tags) != len(names) {
		return false
	}
	for index := range tags {
		if tags[index].Name != names[index] {
			return false
		}
	}
	return true
}
