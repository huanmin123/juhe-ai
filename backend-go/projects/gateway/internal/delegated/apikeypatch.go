// PATCH /api-keys/{id} for the delegated API: the Node
// patchApiKeyAsync subset reachable from apiKeyPatchSchema
// (expectedRevision + name/status/routeStrategyId) with optimistic
// revision locking (ApiKeyRevisionConflictError → 409 + currentRevision),
// the default/chat key guards, the selectable route-strategy probe and the
// hourly quota scope-binding sync. The apikeys store has no shared patch
// implementation, so the delegated slice owns the SQL (dual-mode).
package delegated

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// maxRequestQuotaHourlyWindowHours mirrors request-quota-limits.ts.
const maxRequestQuotaHourlyWindowHours = 24 * 30

// apiKeyRevisionConflictMessage mirrors ApiKeyRevisionConflictError.
const apiKeyRevisionConflictMessage = "API Key 已被其他操作修改，请刷新后重试"

// apiKeyPatchOutcome mirrors ApiKeyPatchOutcome.result: the route renders
// ok(outcome.result) verbatim (id/revision/changedFields/rowPatch).
type apiKeyPatchOutcome struct {
	ID            string         `json:"id"`
	Revision      string         `json:"revision"`
	ChangedFields []string       `json:"changedFields"`
	RowPatch      map[string]any `json:"rowPatch"`
}

// zodTypeError mirrors the zod v3 invalid_type copy for string fields.
func zodTypeError(raw any) string {
	return "Expected string, received " + jsonTypeName(raw)
}

func jsonTypeName(raw any) string {
	switch raw.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

// zodUnrecognizedKeys mirrors the zod strict-object copy.
func zodUnrecognizedKeys(keys ...string) string {
	quoted := make([]string, len(keys))
	for i, key := range keys {
		quoted[i] = "'" + key + "'"
	}
	return "Unrecognized key(s) in object: " + strings.Join(quoted, ", ")
}

// zodBlank mirrors z.string().trim().min(1) on a whitespace-only value.
const zodBlank = "String must contain at least 1 character(s)"

// zodRequired mirrors a missing required string field.
const zodRequired = "Required"

type apiKeyPatchInput struct {
	ExpectedRevision   string
	Name               string
	Status             string
	RouteStrategyID    string
	HasName            bool
	HasStatus          bool
	HasRouteStrategyID bool
}

// parseApiKeyPatch mirrors apiKeyPatchSchema.safeParse. The zod issue order
// is schema field order (expectedRevision, name, status, routeStrategyId),
// then strict unrecognized keys, then the no-change refine; the route renders
// the first issue message.
func parseApiKeyPatch(body map[string]any) (*apiKeyPatchInput, bool, string) {
	input := &apiKeyPatchInput{}
	raw, exists := body["expectedRevision"]
	if !exists {
		return nil, false, zodRequired
	}
	text, isString := raw.(string)
	if !isString {
		return nil, false, zodTypeError(raw)
	}
	input.ExpectedRevision = strings.TrimSpace(text)
	if input.ExpectedRevision == "" {
		return nil, false, zodBlank
	}
	if value, exists := body["name"]; exists {
		trimmed, present, issue := optionalTrimmedString(value)
		if issue != "" {
			return nil, false, issue
		}
		input.HasName = present
		input.Name = trimmed
	}
	if value, exists := body["status"]; exists {
		if value == nil {
			return nil, false, "Expected 'active' | 'disabled', received null"
		}
		text, isString := value.(string)
		if !isString {
			return nil, false, "Expected 'active' | 'disabled', received " + jsonTypeName(value)
		}
		if text != "active" && text != "disabled" {
			return nil, false, fmt.Sprintf("Invalid enum value. Expected 'active' | 'disabled', received '%s'", text)
		}
		input.HasStatus = true
		input.Status = text
	}
	if value, exists := body["routeStrategyId"]; exists {
		trimmed, present, issue := optionalTrimmedString(value)
		if issue != "" {
			return nil, false, issue
		}
		input.HasRouteStrategyID = present
		input.RouteStrategyID = trimmed
	}
	var unknown []string
	for key := range body {
		switch key {
		case "expectedRevision", "name", "status", "routeStrategyId":
		default:
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		return nil, false, zodUnrecognizedKeys(unknown...)
	}
	if !input.HasName && !input.HasStatus && !input.HasRouteStrategyID {
		return nil, false, "请提供要修改的 API Key 内容"
	}
	return input, true, ""
}

func optionalTrimmedString(value any) (trimmed string, present bool, issue string) {
	text, isString := value.(string)
	if !isString {
		return "", false, zodTypeError(value)
	}
	if strings.TrimSpace(text) == "" {
		return "", false, zodBlank
	}
	return strings.TrimSpace(text), true, ""
}

// apiKeyMutationRow is the locked current row for the patch.
type apiKeyMutationRow struct {
	ID              string
	SystemAccountID string
	Name            string
	RouteStrategyID string
	Status          string
	IsDefault       bool
	Purpose         string
	KeyHash         string
	QuotaLimitsJSON sql.NullString
	UpdatedAt       string
}

func scanApiKeyMutationRow(scan func(...any) error) (*apiKeyMutationRow, error) {
	row := apiKeyMutationRow{}
	var isDefault int
	if err := scan(&row.ID, &row.SystemAccountID, &row.Name, &row.RouteStrategyID, &row.Status,
		&isDefault, &row.Purpose, &row.KeyHash, &row.QuotaLimitsJSON, &row.UpdatedAt); err != nil {
		return nil, err
	}
	row.IsDefault = isDefault == 1
	return &row, nil
}

// apiKeyRevisionConflict mirrors ApiKeyRevisionConflictError payload.
type apiKeyRevisionConflict struct{ CurrentRevision string }

func (e *apiKeyRevisionConflict) Error() string { return apiKeyRevisionConflictMessage }

func (d *Deps) patchApiKey(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	input, ok, issue := parseApiKeyPatch(body)
	if !ok {
		kernel.WriteBadRequest(w, issue)
		return
	}
	if d.DB == nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	systemAccountID, _ := access(r)
	outcome, err := d.patchApiKeyTx(r.Context(), r.PathValue("id"), systemAccountID, input)
	if err != nil {
		var revisionConflict *apiKeyRevisionConflict
		if errors.As(err, &revisionConflict) {
			kernel.WriteJSON(w, http.StatusConflict, struct {
				Message         string `json:"message"`
				CurrentRevision string `json:"currentRevision"`
			}{apiKeyRevisionConflictMessage, revisionConflict.CurrentRevision})
			return
		}
		message := errorText(err, "更新 API Key 失败")
		if strings.Contains(message, "已存在") {
			kernel.WriteError(w, http.StatusConflict, message)
			return
		}
		kernel.WriteBadRequest(w, message)
		return
	}
	if outcome == nil {
		kernel.WriteError(w, http.StatusNotFound, "API Key 不存在")
		return
	}
	kernel.WriteOK(w, outcome, "")
}

// patchApiKeyTx mirrors patchApiKeyAsync: locked row, revision gate, the
// mutable-field guards, changed-field collection and the guarded UPDATE.
func (d *Deps) patchApiKeyTx(ctx context.Context, id, systemAccountID string, input *apiKeyPatchInput) (*apiKeyPatchOutcome, error) {
	ctx = ensureDelegatedCtx(ctx)
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	lock := ""
	if d.PGDialect {
		lock = " FOR UPDATE"
	}
	current, err := scanApiKeyMutationRow(func(dst ...any) error {
		return tx.QueryRowContext(ctx, d.bind(`SELECT api_keys.id, api_keys.system_account_id, api_keys.name,
			api_keys.route_strategy_id, api_keys.status, api_keys.is_default, api_keys.purpose,
			api_keys.key_hash, api_keys.quota_limits_json, api_keys.updated_at
			FROM `+d.table("api_keys")+` api_keys
			WHERE api_keys.id = ? AND api_keys.system_account_id = ? LIMIT 1`+lock), id, systemAccountID).Scan(dst...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if current.UpdatedAt != input.ExpectedRevision {
		return nil, &apiKeyRevisionConflict{CurrentRevision: current.UpdatedAt}
	}

	outcome := &apiKeyPatchOutcome{
		ID:            current.ID,
		Revision:      current.UpdatedAt,
		ChangedFields: []string{},
		RowPatch:      map[string]any{"revision": current.UpdatedAt},
	}
	setClauses := []string{}
	setArgs := []any{}
	nextName := current.Name

	if input.HasName {
		if input.Name != current.Name {
			if current.Purpose == "chat" {
				return nil, errors.New("AI 对话 API Key 不允许修改名称")
			}
			if current.IsDefault {
				return nil, errors.New("默认 API Key 不允许修改名称")
			}
			nextName = input.Name
			outcome.ChangedFields = append(outcome.ChangedFields, "name")
			outcome.RowPatch["name"] = input.Name
			setClauses = append(setClauses, "name = ?")
			setArgs = append(setArgs, input.Name)
		}
	}
	if input.HasRouteStrategyID && input.RouteStrategyID != current.RouteStrategyID {
		if current.IsDefault && current.Purpose != "chat" {
			return nil, errors.New("默认 API Key 不允许更换策略路由")
		}
		reference, err := d.selectableRouteStrategy(ctx, tx, current.SystemAccountID, input.RouteStrategyID)
		if err != nil {
			return nil, err
		}
		outcome.ChangedFields = append(outcome.ChangedFields, "routeStrategyId")
		outcome.RowPatch["routeStrategyId"] = reference.id
		outcome.RowPatch["routeStrategyName"] = reference.name
		outcome.RowPatch["routeStrategyMode"] = reference.mode
		outcome.RowPatch["routeStrategyStatus"] = reference.status
		setClauses = append(setClauses, "route_strategy_id = ?")
		setArgs = append(setArgs, reference.id)
	}
	if input.HasStatus && input.Status != current.Status {
		outcome.ChangedFields = append(outcome.ChangedFields, "status")
		outcome.RowPatch["status"] = input.Status
		setClauses = append(setClauses, "status = ?")
		setArgs = append(setArgs, input.Status)
	}

	if len(outcome.ChangedFields) == 0 {
		return outcome, nil
	}
	revision, err := nextApiKeyRevision(current.UpdatedAt, d.clock())
	if err != nil {
		return nil, err
	}
	setClauses = append(setClauses, "updated_at = ?")
	setArgs = append(setArgs, revision)
	args := append([]any{}, setArgs...)
	args = append(args, current.ID, current.SystemAccountID, current.UpdatedAt)
	if _, err := tx.ExecContext(ctx, d.bind(`UPDATE `+d.table("api_keys")+` SET `+strings.Join(setClauses, ", ")+`
		WHERE id = ? AND system_account_id = ? AND updated_at = ?`), args...); err != nil {
		if isDuplicateKeyNameError(err) {
			return nil, fmt.Errorf("API Key 名称已存在：%s", input.Name)
		}
		return nil, err
	}
	outcome.Revision = revision
	outcome.RowPatch["revision"] = revision
	if d.hasStatusChange(outcome.ChangedFields) {
		if err := d.syncApiKeyQuotaScopeBinding(ctx, tx, current, input.Status == "active", revision); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = nextName
	return outcome, nil
}

func (d *Deps) hasStatusChange(changedFields []string) bool {
	for _, field := range changedFields {
		if field == "status" {
			return true
		}
	}
	return false
}

// routeStrategyReference mirrors apiKeyRouteStrategyReferenceAsync.
type routeStrategyReference struct {
	id     string
	name   string
	mode   string
	status string
}

// selectableRouteStrategy mirrors assertRouteStrategySelectableForApiKeyAsync
// + apiKeyRouteStrategyReferenceAsync for the owner scope.
func (d *Deps) selectableRouteStrategy(ctx context.Context, tx *sql.Tx, systemAccountID, strategyID string) (*routeStrategyReference, error) {
	var row routeStrategyReference
	err := tx.QueryRowContext(ctx, d.bind(`SELECT id, name, mode, status FROM `+d.table("route_strategies")+`
		WHERE id = ? AND system_account_id = ? LIMIT 1`), strategyID, systemAccountID).
		Scan(&row.id, &row.name, &row.mode, &row.status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("API Key 绑定的策略路由不存在或不属于当前用户")
	}
	if err != nil {
		return nil, err
	}
	if row.status != "active" {
		return nil, errors.New("API Key 只能绑定启用状态的策略路由")
	}
	return &row, nil
}

// syncApiKeyQuotaScopeBinding mirrors
// syncApiKeyRequestQuotaHourlyWindowScopeBindingForClientAsync: a status
// change rebuilds the api_key hourly-window binding (delete + optional
// insert when the key carries an enabled hourly quota and stays active).
func (d *Deps) syncApiKeyQuotaScopeBinding(ctx context.Context, tx *sql.Tx, current *apiKeyMutationRow, active bool, timestamp string) error {
	if _, err := tx.ExecContext(ctx, d.bind(`DELETE FROM `+d.table("request_quota_hourly_window_scope_bindings")+`
		WHERE source_type = 'api_key' AND source_id = ?`), current.ID); err != nil {
		return err
	}
	if !active {
		return nil
	}
	windowHours, ok := hourlyQuotaWindowHours(current.QuotaLimitsJSON)
	if !ok {
		return nil
	}
	_, err := tx.ExecContext(ctx, d.bind(`INSERT INTO `+d.table("request_quota_hourly_window_scope_bindings")+`
		(system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at)
		VALUES (?, 'api_key', ?, 'api_key', ?, ?, ?, ?)`),
		current.SystemAccountID, current.ID, current.ID, windowHours, timestamp, timestamp)
	return err
}

// hourlyQuotaWindowHours mirrors activeRequestQuotaHourlyWindowHours for the
// active branch: an integer 1..maxRequestQuotaHourlyWindowHours window from
// an enabled hourly quota.
func hourlyQuotaWindowHours(limitsJSON sql.NullString) (int, bool) {
	if !limitsJSON.Valid || limitsJSON.String == "" {
		return 0, false
	}
	type hourlyQuota struct {
		Enabled bool `json:"enabled"`
		Hours   *int `json:"hours"`
	}
	type quotaLimits struct {
		Hourly *hourlyQuota `json:"hourly"`
	}
	var limits quotaLimits
	if err := json.Unmarshal([]byte(limitsJSON.String), &limits); err != nil {
		return 0, false
	}
	if limits.Hourly == nil || !limits.Hourly.Enabled || limits.Hourly.Hours == nil {
		return 0, false
	}
	hours := *limits.Hourly.Hours
	if hours < 1 || hours > maxRequestQuotaHourlyWindowHours {
		return 0, false
	}
	return hours, true
}

// isDuplicateKeyNameError mirrors isDuplicateApiKeyNameError for the
// (system_account_id, name) unique index across both dialects.
func isDuplicateKeyNameError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint failed") ||
		strings.Contains(message, "duplicate key value violates unique constraint") ||
		strings.Contains(message, "SQLSTATE 23505")
}

// revisionFromMillis mirrors apiKeyRevisionFromTimestamp: microsecond
// rendering of the millisecond revision clock.
func revisionFromMillis(milliseconds int64) string {
	return time.UnixMilli(milliseconds).UTC().Format("2006-01-02T15:04:05.000000") + "Z"
}

// nextApiKeyRevision mirrors nextApiKeyRevision: monotonic versus the stored
// revision (now wins, or previous + 1ms).
func nextApiKeyRevision(current string, now time.Time) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, current)
	if err != nil {
		return "", fmt.Errorf("API Key revision 必须是带 Z 或数值 offset 的 RFC3339 时间：%s", current)
	}
	next := now.UnixMilli()
	if floor := parsed.UnixMilli() + 1; next < floor {
		next = floor
	}
	return revisionFromMillis(next), nil
}
