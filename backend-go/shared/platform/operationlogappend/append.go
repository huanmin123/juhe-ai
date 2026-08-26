// Package operationlogappend provides the narrow PostgreSQL append contract
// used by Go-owned business actions. It intentionally does not own F4's
// listener, retention worker, or schema lifecycle; those remain in the F4
// gateway service. Producers use this package to append an already-authorized
// audit record without introducing a Go-to-Go HTTP hop.
package operationlogappend

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	defaultTimeout = 5 * time.Second
	maxSearchTerms = 1500
)

// Change, Target, and Viewer mirror the persisted F4 operation-log shape.
// They are deliberately small producer DTOs: the F4 reader remains the
// authoritative API for listing and rendering audit history.
type Change struct {
	Field     string `json:"field"`
	Label     string `json:"label"`
	Before    any    `json:"before,omitempty"`
	After     any    `json:"after,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

type Target struct {
	TargetType                 string `json:"targetType"`
	TargetID                   string `json:"targetId,omitempty"`
	TargetName                 string `json:"targetName,omitempty"`
	TargetOwnerSystemAccountID string `json:"targetOwnerSystemAccountId,omitempty"`
	Relation                   string `json:"relation,omitempty"`
}

type Viewer struct {
	SystemAccountID  string `json:"systemAccountId"`
	VisibilityReason string `json:"visibilityReason"`
	DetailLevel      string `json:"detailLevel,omitempty"`
}

// Input is an immutable audit event. ID and CreatedAt are assigned by the
// caller so a retry can remain idempotent.
type Input struct {
	ID                            string
	TraceID                       string
	ActorSystemAccountID          string
	ActorUsername                 string
	ActorDisplayName              string
	ActorRole                     string
	OperationScopeSystemAccountID string
	Mode                          string
	Module                        string
	Action                        string
	OperationKey                  string
	ResourceType                  string
	ResourceID                    string
	ResourceName                  string
	Summary                       string
	DetailLevel                   string
	VisibilityScope               string
	Changes                       []Change
	Metadata                      json.RawMessage
	Method                        string
	Path                          string
	StatusCode                    *int
	ClientIP                      string
	UserAgent                     string
	Targets                       []Target
	Viewers                       []Viewer
	CreatedAt                     time.Time
}

// NewID creates a collision-resistant ID compatible with the text primary
// key used by F4. The prefix is caller controlled only as a stable namespace.
func NewID(prefix string) (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate operation log id: %w", err)
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "oplog"
	}
	return prefix + "_" + hex.EncodeToString(bytes), nil
}

// AppendPostgres appends one F4-compatible record and its index children in
// one PostgreSQL transaction. ON CONFLICT makes an identical retry harmless.
func AppendPostgres(parent context.Context, db *sql.DB, input Input) error {
	if db == nil {
		return errors.New("operation log PostgreSQL database is required")
	}
	normalized, err := normalize(input)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, defaultTimeout)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin operation log append transaction: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		"SET LOCAL statement_timeout = '5s'",
		"SET LOCAL lock_timeout = '1s'",
		"SET LOCAL idle_in_transaction_session_timeout = '5s'",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure operation log append transaction: %w", err)
		}
	}
	changes, err := json.Marshal(normalized.Changes)
	if err != nil {
		return fmt.Errorf("encode operation log changes: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO juhe_dataset.operation_logs (
  id,trace_id,actor_system_account_id,actor_username,actor_display_name,actor_role,
  operation_scope_system_account_id,mode,module,action,operation_key,resource_type,
  resource_id,resource_name,summary,detail_level,visibility_scope,changes_json,
  metadata_json,method,path,status_code,client_ip,user_agent,created_at
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18::jsonb,
  $19::jsonb,$20,$21,$22,$23,$24,$25
) ON CONFLICT (id) DO NOTHING`,
		normalized.ID, nilIfEmpty(normalized.TraceID), normalized.ActorSystemAccountID,
		nilIfEmpty(normalized.ActorUsername), nilIfEmpty(normalized.ActorDisplayName), normalized.ActorRole,
		nilIfEmpty(normalized.OperationScopeSystemAccountID), normalized.Mode, normalized.Module,
		normalized.Action, normalized.OperationKey, normalized.ResourceType, nilIfEmpty(normalized.ResourceID),
		nilIfEmpty(normalized.ResourceName), normalized.Summary, normalized.DetailLevel, normalized.VisibilityScope,
		string(changes), string(normalized.Metadata), nilIfEmpty(normalized.Method), nilIfEmpty(normalized.Path),
		normalized.StatusCode, nilIfEmpty(normalized.ClientIP), nilIfEmpty(normalized.UserAgent), normalized.CreatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert operation log: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return tx.Commit()
	}
	for index, target := range normalized.Targets {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO juhe_dataset.operation_log_targets (
  id,operation_log_id,target_type,target_id,target_name,target_owner_system_account_id,relation,created_at
) VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),$7,$8)`,
			fmt.Sprintf("optgt_%s_%d", normalized.ID, index), normalized.ID, target.TargetType,
			target.TargetID, target.TargetName, target.TargetOwnerSystemAccountID, target.Relation, normalized.CreatedAt.UTC()); err != nil {
			return fmt.Errorf("insert operation log target: %w", err)
		}
	}
	for _, viewer := range normalized.Viewers {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO juhe_dataset.operation_log_viewers (
  operation_log_id,system_account_id,visibility_reason,detail_level,created_at
) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
			normalized.ID, viewer.SystemAccountID, viewer.VisibilityReason, viewer.DetailLevel, normalized.CreatedAt.UTC()); err != nil {
			return fmt.Errorf("insert operation log viewer: %w", err)
		}
	}
	terms := searchTerms(normalized.Summary)
	if len(terms) > 0 {
		logIDs := make([]string, len(terms))
		createdAt := make([]string, len(terms))
		created := normalized.CreatedAt.UTC().Format(time.RFC3339Nano)
		for index := range terms {
			logIDs[index] = normalized.ID
			createdAt[index] = created
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO juhe_dataset.operation_log_summary_search_terms (
  operation_log_id,term,created_at
)
SELECT operation_log_id,term,created_at::timestamptz
FROM unnest($1::text[],$2::text[],$3::text[]) AS s(operation_log_id,term,created_at)
ON CONFLICT DO NOTHING`, logIDs, terms, createdAt); err != nil {
			return fmt.Errorf("insert operation log search terms: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit operation log append: %w", err)
	}
	return nil
}

func normalize(input Input) (Input, error) {
	for name, value := range map[string]string{
		"id": input.ID, "actorSystemAccountId": input.ActorSystemAccountID, "actorRole": input.ActorRole,
		"module": input.Module, "action": input.Action, "operationKey": input.OperationKey,
		"resourceType": input.ResourceType, "summary": input.Summary,
	} {
		if strings.TrimSpace(value) == "" {
			return Input{}, fmt.Errorf("operation log input missing %s", name)
		}
	}
	if input.CreatedAt.IsZero() {
		return Input{}, errors.New("operation log input missing createdAt")
	}
	if input.Mode == "" {
		input.Mode = "self"
	}
	if input.DetailLevel == "" {
		input.DetailLevel = "full"
	}
	if input.VisibilityScope == "" {
		input.VisibilityScope = "targeted"
	}
	if !oneOf(input.Mode, "self", "admin") || !oneOf(input.DetailLevel, "full", "summary") || !oneOf(input.VisibilityScope, "targeted", "all_users", "admin_only") {
		return Input{}, errors.New("operation log enum value invalid")
	}
	if len(input.Metadata) == 0 {
		input.Metadata = json.RawMessage("{}")
	} else if !json.Valid(input.Metadata) {
		return Input{}, errors.New("operation log metadata is not valid JSON")
	}
	if !hasPrimary(input.Targets) && (input.ResourceID != "" || input.ResourceName != "") {
		input.Targets = append(input.Targets, Target{
			TargetType: input.ResourceType, TargetID: input.ResourceID, TargetName: input.ResourceName,
			TargetOwnerSystemAccountID: input.OperationScopeSystemAccountID, Relation: "primary",
		})
	}
	if input.VisibilityScope == "targeted" {
		input.Viewers = append(input.Viewers, Viewer{SystemAccountID: input.ActorSystemAccountID, VisibilityReason: "actor_self", DetailLevel: input.DetailLevel})
		if input.OperationScopeSystemAccountID != "" && input.OperationScopeSystemAccountID != input.ActorSystemAccountID {
			reason := "resource_owner"
			if input.ActorRole == "admin" {
				reason = "admin_managed_my_resource"
			}
			input.Viewers = append(input.Viewers, Viewer{SystemAccountID: input.OperationScopeSystemAccountID, VisibilityReason: reason, DetailLevel: input.DetailLevel})
		}
	} else {
		input.Viewers = nil
	}
	for index := range input.Targets {
		target := &input.Targets[index]
		if strings.TrimSpace(target.TargetType) == "" {
			return Input{}, errors.New("operation log target type is required")
		}
		if target.Relation == "" {
			target.Relation = "affected"
		}
		if !oneOf(target.Relation, "primary", "affected", "created", "deleted", "owner", "grantee", "team_member", "bound_resource") {
			return Input{}, errors.New("operation log target relation is invalid")
		}
	}
	seen := map[string]bool{}
	viewers := make([]Viewer, 0, len(input.Viewers))
	for _, viewer := range input.Viewers {
		viewer.SystemAccountID = strings.TrimSpace(viewer.SystemAccountID)
		if viewer.SystemAccountID == "" {
			continue
		}
		if viewer.DetailLevel == "" {
			viewer.DetailLevel = input.DetailLevel
		}
		if !oneOf(viewer.VisibilityReason, "actor_self", "resource_owner", "admin_managed_my_resource", "authorization_owner", "authorization_grantee", "team_member", "team_authorization", "global_affected", "bound_resource_affected") || !oneOf(viewer.DetailLevel, "full", "summary") {
			return Input{}, errors.New("operation log viewer is invalid")
		}
		key := viewer.SystemAccountID + ":" + viewer.VisibilityReason + ":" + viewer.DetailLevel
		if !seen[key] {
			seen[key] = true
			viewers = append(viewers, viewer)
		}
	}
	input.Viewers = viewers
	if input.Changes == nil {
		input.Changes = []Change{}
	}
	return input, nil
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func hasPrimary(targets []Target) bool {
	for _, target := range targets {
		if target.Relation == "primary" {
			return true
		}
	}
	return false
}

func searchTerms(value string) []string {
	value = normalizeSearchText(value)
	if value == "" {
		return nil
	}
	compact := strings.ReplaceAll(value, " ", "")
	parts := strings.Fields(value)
	set := map[string]bool{}
	add := func(term string) {
		if length := len([]rune(term)); length >= 1 && length <= 128 {
			set[term] = true
		}
	}
	add(value)
	add(compact)
	for _, part := range parts {
		add(part)
	}
	for _, candidate := range append([]string{value, compact}, parts...) {
		chars := []rune(candidate)
		if len(chars) > 256 {
			chars = chars[:256]
		}
		for length := 1; length <= 128 && length <= len(chars); length++ {
			for start := 0; start+length <= len(chars) && len(set) < maxSearchTerms; start++ {
				add(string(chars[start : start+length]))
			}
			if len(set) >= maxSearchTerms {
				break
			}
		}
		if len(set) >= maxSearchTerms {
			break
		}
	}
	result := make([]string, 0, len(set))
	for term := range set {
		result = append(result, term)
	}
	sort.Strings(result)
	return result
}

func normalizeSearchText(value string) string {
	value = strings.ToLower(strings.TrimSpace(norm.NFKC.String(value)))
	var builder strings.Builder
	needsSpace := false
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			builder.WriteRune(character)
			needsSpace = false
		} else if builder.Len() > 0 && !needsSpace {
			builder.WriteByte(' ')
			needsSpace = true
		}
	}
	return strings.TrimSpace(builder.String())
}
