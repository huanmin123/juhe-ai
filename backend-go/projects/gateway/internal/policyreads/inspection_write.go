// inspection_write.go owns the response inspection policy write paths:
// create, patch with optimistic locking and delete.
package policyreads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Create mirrors createResponseInspectionPolicyAsync.
func (s *InspectionStore) Create(ctx context.Context, input *InspectionCreateInput) (*InspectionDetail, error) {
	ctx = ensureCtx(ctx)
	if err := s.assertCapacity(ctx); err != nil {
		return nil, err
	}
	merged := inspectionMergedInput{
		Name:         input.Name,
		Enabled:      input.Enabled == nil || *input.Enabled,
		Priority:     100,
		ScopeType:    input.ScopeType,
		ProtocolCode: input.ProtocolCode,
		ProviderCode: nullableAny(input.ProviderCode),
		Match:        input.Match,
		Action:       input.Action,
		Notes:        nullableAny(input.Notes),
	}
	if input.Priority != nil {
		merged.Priority = *input.Priority
	}
	normalized, err := s.normalizeMerged(ctx, s.db, merged, true)
	if err != nil {
		return nil, err
	}
	now := s.nowISO()
	id := s.generateID("rip")
	matchJSON, err := json.Marshal(normalized.Match)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("response_inspection_policies")+`
		(id, name, enabled, priority, scope_type, protocol_code, provider_code, match_json, action, notes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, normalized.Name, boolToInt(normalized.Enabled), normalized.Priority, normalized.ScopeType,
		normalized.ProtocolCode, normalized.ProviderCode, string(matchJSON), normalized.Action,
		normalized.Notes, now, now); err != nil {
		return nil, err
	}
	s.invalidateRuntime("response_inspection_policy_created")
	normalized.UpdatedAt = now
	detail := normalized.detail()
	detail.ID = id
	detail.ProviderName, err = s.providerName(ctx, normalized.ProviderCode)
	if err != nil {
		return nil, err
	}
	return detail, nil
}

// InspectionCreateInput is the zod-validated POST payload; nil pointers mean
// "absent or null" exactly like the optional/nullable zod fields.
type InspectionCreateInput struct {
	Name         string
	Enabled      *bool
	Priority     *int
	ScopeType    string
	ProtocolCode string
	ProviderCode *string // nil = absent or null
	Match        any
	Action       string
	Notes        *string // nil = absent or null
}

// InspectionPatch is the zod-validated PATCH payload; SetFields tracks which
// patchable fields are present (expectedUpdatedAt excluded). Present-as-null
// fields (providerCode, notes) carry a nil value with the flag set.
type InspectionPatch struct {
	SetFields    map[string]bool
	Name         *string
	Enabled      *bool
	Priority     *int
	ScopeType    *string
	ProtocolCode *string
	ProviderCode any // string when present; nil when present-as-null
	Match        any
	Action       *string
	Notes        any // string when present; nil when present-as-null
	ExpectedAt   string
}

// InspectionPatchOutcome mirrors ResponseInspectionPolicyPatchOutcome.
type InspectionPatchOutcome struct {
	Status        string // not_found | conflict | noop | updated
	Current       *InspectionDetail
	Policy        *InspectionDetail
	ChangedFields []string
}

// Patch mirrors patchResponseInspectionPolicyAsync.
func (s *InspectionStore) Patch(ctx context.Context, id string, patch *InspectionPatch) (*InspectionPatchOutcome, error) {
	ctx = ensureCtx(ctx)
	var row inspectionPatchRow
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, name, enabled, priority, scope_type, protocol_code,
		provider_code, match_json, action, notes, updated_at
		FROM `+s.table("response_inspection_policies")+` WHERE id = ?`), id).
		Scan(&row.id, &row.name, &row.enabled, &row.priority, &row.scopeType, &row.protocolCode,
			&row.providerCode, &row.matchJSON, &row.action, &row.notes, &row.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return &InspectionPatchOutcome{Status: "not_found"}, nil
	}
	if err != nil {
		return nil, err
	}
	if row.updatedAt != patch.ExpectedAt {
		return &InspectionPatchOutcome{Status: "conflict"}, nil
	}
	current, err := s.normalizedFromPatchRow(ctx, row)
	if err != nil {
		return nil, err
	}
	merged := inspectionMergedInput{
		Name: current.Name, Enabled: current.Enabled, Priority: current.Priority,
		ScopeType: current.ScopeType, ProtocolCode: current.ProtocolCode,
		ProviderCode: nullableAny(current.ProviderCode), Match: matchAsAny(current.Match),
		Action: current.Action, Notes: nullableAny(current.Notes),
	}
	if patch.SetFields["name"] {
		merged.Name = *patch.Name
	}
	if patch.SetFields["enabled"] {
		merged.Enabled = *patch.Enabled
	}
	if patch.SetFields["priority"] {
		merged.Priority = *patch.Priority
	}
	if patch.SetFields["scopeType"] {
		merged.ScopeType = *patch.ScopeType
	}
	if patch.SetFields["protocolCode"] {
		merged.ProtocolCode = *patch.ProtocolCode
	}
	if patch.SetFields["providerCode"] {
		merged.ProviderCode = patch.ProviderCode
	}
	if patch.SetFields["match"] {
		merged.Match = patch.Match
	}
	if patch.SetFields["action"] {
		merged.Action = *patch.Action
	}
	if patch.SetFields["notes"] {
		merged.Notes = patch.Notes
	}
	validateMembership := patch.SetFields["scopeType"] || patch.SetFields["protocolCode"] || patch.SetFields["providerCode"]
	next, err := s.normalizeMerged(ctx, s.db, merged, validateMembership)
	if err != nil {
		return nil, err
	}
	currentProviderName, err := s.providerName(ctx, current.ProviderCode)
	if err != nil {
		return nil, err
	}
	currentDetail := current.detail()
	currentDetail.ProviderName = currentProviderName

	nextUpdatedAt, err := nextRFC3339Millis(patch.ExpectedAt, s.now(), "响应检查策略 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	if err != nil {
		return nil, err
	}

	assignments := []string{}
	values := []any{}
	changedFields := []string{}
	addChange := func(field, column string, value any) {
		assignments = append(assignments, column+" = ?")
		values = append(values, value)
		changedFields = append(changedFields, field)
	}
	if patch.SetFields["name"] && current.Name != next.Name {
		addChange("name", "name", next.Name)
	}
	if patch.SetFields["enabled"] && current.Enabled != next.Enabled {
		addChange("enabled", "enabled", boolToInt(next.Enabled))
	}
	if patch.SetFields["priority"] && current.Priority != next.Priority {
		addChange("priority", "priority", next.Priority)
	}
	if patch.SetFields["scopeType"] && current.ScopeType != next.ScopeType {
		addChange("scopeType", "scope_type", next.ScopeType)
	}
	if patch.SetFields["protocolCode"] && current.ProtocolCode != next.ProtocolCode {
		addChange("protocolCode", "protocol_code", next.ProtocolCode)
	}
	if patch.SetFields["providerCode"] && !sameNullable(current.ProviderCode, next.ProviderCode) {
		addChange("providerCode", "provider_code", next.ProviderCode)
	}
	if patch.SetFields["match"] && !matchEqual(current.Match, next.Match) {
		matchJSON, err := json.Marshal(next.Match)
		if err != nil {
			return nil, err
		}
		addChange("match", "match_json", string(matchJSON))
	}
	if patch.SetFields["action"] && current.Action != next.Action {
		addChange("action", "action", next.Action)
	}
	if patch.SetFields["notes"] && !sameNullable(current.Notes, next.Notes) {
		addChange("notes", "notes", next.Notes)
	}

	if len(assignments) == 0 {
		return &InspectionPatchOutcome{
			Status: "noop", Current: currentDetail, Policy: currentDetail, ChangedFields: []string{},
		}, nil
	}
	values = append(values, nextUpdatedAt, id, patch.ExpectedAt)
	update, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("response_inspection_policies")+`
		SET `+strings.Join(assignments, ", ")+", updated_at = ? WHERE id = ? AND updated_at = ?"), values...)
	if err != nil {
		return nil, err
	}
	if affected, _ := update.RowsAffected(); affected == 0 {
		return &InspectionPatchOutcome{Status: "conflict"}, nil
	}
	s.invalidateRuntime("response_inspection_policy_updated")
	next.UpdatedAt = nextUpdatedAt
	policyDetail := next.detail()
	if sameNullable(current.ProviderCode, next.ProviderCode) {
		policyDetail.ProviderName = currentProviderName
	} else {
		providerName, err := s.providerName(ctx, next.ProviderCode)
		if err != nil {
			return nil, err
		}
		policyDetail.ProviderName = providerName
	}
	return &InspectionPatchOutcome{
		Status: "updated", Current: currentDetail, Policy: policyDetail, ChangedFields: changedFields,
	}, nil
}

func nullableAny(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func matchAsAny(match InspectionMatch) any {
	if match == nil {
		return nil
	}
	converted := map[string]any{}
	for key, values := range match {
		list := make([]any, len(values))
		for i, value := range values {
			list[i] = value
		}
		converted[key] = list
	}
	return converted
}

func sameNullable(current, next *string) bool {
	if current == nil || *current == "" {
		return next == nil || *next == ""
	}
	return next != nil && *current == *next
}

func matchEqual(left, right InspectionMatch) bool {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}

// Delete mirrors deleteResponseInspectionPolicyAsync.
func (s *InspectionStore) Delete(ctx context.Context, id string) (bool, error) {
	ctx = ensureCtx(ctx)
	result, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("response_inspection_policies")+` WHERE id = ?`), id)
	if err != nil {
		return false, err
	}
	deleted, _ := result.RowsAffected()
	if deleted > 0 {
		s.invalidateRuntime("response_inspection_policy_deleted")
	}
	return deleted > 0, nil
}
