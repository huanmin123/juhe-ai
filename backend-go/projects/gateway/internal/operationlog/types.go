// Package operationlog owns F4 management operation-log persistence. It is
// intentionally separate from F3 auditlog: inputs, HTTP paths, signatures,
// schemas and owner leases must never be shared.
package operationlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const storageTimeLayout = "2006-01-02T15:04:05.000000000Z"

const storeOperationTimeout = 10 * time.Second

// ErrInvalidListTime marks a client-supplied list range that is not an
// RFC3339 instant.  The HTTP boundary maps this to a 4xx response instead of
// silently dropping the filter or reporting an internal server failure.
var ErrInvalidListTime = errors.New("operation log list time is invalid")

func storeContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, storeOperationTimeout)
}

func storageTime(value time.Time) string {
	return value.UTC().Format(storageTimeLayout)
}

func parseStorageTime(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	return storageTime(parsed), nil
}

type storageTimestamp string

func (timestamp *storageTimestamp) Scan(value any) error {
	switch typed := value.(type) {
	case time.Time:
		*timestamp = storageTimestamp(storageTime(typed))
		return nil
	case string:
		normalized, err := parseStorageTime(typed)
		if err != nil {
			return err
		}
		*timestamp = storageTimestamp(normalized)
		return nil
	case []byte:
		normalized, err := parseStorageTime(string(typed))
		if err != nil {
			return err
		}
		*timestamp = storageTimestamp(normalized)
		return nil
	default:
		return fmt.Errorf("operation log storage timestamp has unsupported type %T", value)
	}
}

type Mode string

const (
	ModeSQLite   Mode = "sqlite"
	ModePostgres Mode = "postgres"
)

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

// Input is the F4 RPC DTO. Node fixes ID and CreatedAt before signing so a
// retried request is an idempotent replay, never a second log entry.
type Input struct {
	ID                            string          `json:"id"`
	TraceID                       string          `json:"traceId,omitempty"`
	ActorSystemAccountID          string          `json:"actorSystemAccountId"`
	ActorUsername                 string          `json:"actorUsername,omitempty"`
	ActorDisplayName              string          `json:"actorDisplayName,omitempty"`
	ActorRole                     string          `json:"actorRole"`
	OperationScopeSystemAccountID string          `json:"operationScopeSystemAccountId,omitempty"`
	Mode                          string          `json:"mode,omitempty"`
	Module                        string          `json:"module"`
	Action                        string          `json:"action"`
	OperationKey                  string          `json:"operationKey"`
	ResourceType                  string          `json:"resourceType"`
	ResourceID                    string          `json:"resourceId,omitempty"`
	ResourceName                  string          `json:"resourceName,omitempty"`
	Summary                       string          `json:"summary"`
	DetailLevel                   string          `json:"detailLevel,omitempty"`
	VisibilityScope               string          `json:"visibilityScope,omitempty"`
	Changes                       []Change        `json:"changes,omitempty"`
	Metadata                      json.RawMessage `json:"metadata,omitempty"`
	Method                        string          `json:"method,omitempty"`
	Path                          string          `json:"path,omitempty"`
	StatusCode                    *int            `json:"statusCode,omitempty"`
	ClientIP                      string          `json:"clientIp,omitempty"`
	UserAgent                     string          `json:"userAgent,omitempty"`
	Targets                       []Target        `json:"targets,omitempty"`
	Viewers                       []Viewer        `json:"viewers,omitempty"`
	CreatedAt                     string          `json:"createdAt"`
}

type ListOptions struct {
	ViewerID                      string `json:"viewerId,omitempty"`
	Page                          int    `json:"page,omitempty"`
	PageSize                      int    `json:"pageSize,omitempty"`
	SummaryKeyword                string `json:"summaryKeyword,omitempty"`
	Module                        string `json:"module,omitempty"`
	Action                        string `json:"action,omitempty"`
	ResourceType                  string `json:"resourceType,omitempty"`
	ResourceID                    string `json:"resourceId,omitempty"`
	ActorSystemAccountID          string `json:"actorSystemAccountId,omitempty"`
	AffectedSystemAccountID       string `json:"affectedSystemAccountId,omitempty"`
	OperationScopeSystemAccountID string `json:"operationScopeSystemAccountId,omitempty"`
	TraceID                       string `json:"traceId,omitempty"`
	StartAt                       string `json:"startAt,omitempty"`
	EndAt                         string `json:"endAt,omitempty"`
}

type ListItem struct {
	ID                              string `json:"id"`
	TraceID                         string `json:"traceId,omitempty"`
	ActorSystemAccountID            string `json:"actorSystemAccountId"`
	ActorDisplayName                string `json:"actorDisplayName,omitempty"`
	ActorSystemAccountName          string `json:"actorSystemAccountName,omitempty"`
	OperationScopeSystemAccountID   string `json:"operationScopeSystemAccountId,omitempty"`
	OperationScopeSystemAccountName string `json:"operationScopeSystemAccountName,omitempty"`
	Module                          string `json:"module"`
	Action                          string `json:"action"`
	Summary                         string `json:"summary"`
	CreatedAt                       string `json:"createdAt"`
}

type ListResult struct {
	Items    []ListItem `json:"items"`
	Total    int        `json:"total"`
	HasMore  bool       `json:"hasMore"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}
type DetailTarget struct {
	ID                           string `json:"id"`
	TargetType                   string `json:"targetType"`
	TargetID                     string `json:"targetId,omitempty"`
	TargetName                   string `json:"targetName,omitempty"`
	TargetOwnerSystemAccountName string `json:"targetOwnerSystemAccountName,omitempty"`
	Relation                     string `json:"relation"`
}
type DetailViewer struct {
	SystemAccountID   string `json:"systemAccountId"`
	SystemAccountName string `json:"systemAccountName,omitempty"`
	VisibilityReason  string `json:"visibilityReason"`
	DetailLevel       string `json:"detailLevel"`
}
type DetailSupplement struct {
	OperationKey    string         `json:"operationKey"`
	ResourceType    string         `json:"resourceType"`
	ResourceID      string         `json:"resourceId,omitempty"`
	ResourceName    string         `json:"resourceName,omitempty"`
	VisibilityScope string         `json:"visibilityScope"`
	Changes         []Change       `json:"changes"`
	Method          string         `json:"method,omitempty"`
	Path            string         `json:"path,omitempty"`
	ClientIP        string         `json:"clientIp,omitempty"`
	Targets         []DetailTarget `json:"targets"`
	Viewers         []DetailViewer `json:"viewers"`
}

func normalizeInput(input Input) (Input, error) {
	for name, value := range map[string]string{"id": input.ID, "actorSystemAccountId": input.ActorSystemAccountID, "actorRole": input.ActorRole, "module": input.Module, "action": input.Action, "operationKey": input.OperationKey, "resourceType": input.ResourceType, "summary": input.Summary, "createdAt": input.CreatedAt} {
		if strings.TrimSpace(value) == "" {
			return Input{}, fmt.Errorf("operation log input missing %s", name)
		}
	}
	createdAt, err := parseStorageTime(input.CreatedAt)
	if err != nil {
		return Input{}, fmt.Errorf("operation log createdAt invalid: %w", err)
	}
	input.CreatedAt = createdAt
	if input.Mode == "" {
		input.Mode = "self"
	}
	if input.DetailLevel == "" {
		input.DetailLevel = "full"
	}
	if input.VisibilityScope == "" {
		input.VisibilityScope = "targeted"
	}
	if !known(input.Mode, "self", "admin") || !known(input.DetailLevel, "full", "summary") || !known(input.VisibilityScope, "targeted", "all_users", "admin_only") {
		return Input{}, fmt.Errorf("operation log enum value invalid")
	}
	if input.Changes == nil {
		input.Changes = []Change{}
	}
	if len(input.Metadata) == 0 {
		input.Metadata = json.RawMessage("{}")
	} else if !json.Valid(input.Metadata) {
		return Input{}, fmt.Errorf("operation log metadata is not valid JSON")
	}
	if !hasPrimary(input.Targets) && (input.ResourceID != "" || input.ResourceName != "") {
		input.Targets = append(input.Targets, Target{TargetType: input.ResourceType, TargetID: input.ResourceID, TargetName: input.ResourceName, TargetOwnerSystemAccountID: input.OperationScopeSystemAccountID, Relation: "primary"})
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
	}
	if input.VisibilityScope != "targeted" {
		input.Viewers = nil
	}
	targets, err := normalizedTargets(input.Targets)
	if err != nil {
		return Input{}, err
	}
	input.Targets = targets
	viewers, err := normalizedViewers(input.Viewers, input.DetailLevel)
	if err != nil {
		return Input{}, err
	}
	input.Viewers = viewers
	return input, nil
}
func known(value string, values ...string) bool {
	for _, item := range values {
		if value == item {
			return true
		}
	}
	return false
}
func hasPrimary(items []Target) bool {
	for _, item := range items {
		if item.Relation == "primary" {
			return true
		}
	}
	return false
}
func normalizedTargets(items []Target) ([]Target, error) {
	out := make([]Target, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.TargetType) == "" {
			return nil, fmt.Errorf("operation log target type is required")
		}
		if item.Relation == "" {
			item.Relation = "affected"
		}
		if !known(item.Relation, "primary", "affected", "created", "deleted", "owner", "grantee", "team_member", "bound_resource") {
			return nil, fmt.Errorf("operation log target relation is invalid")
		}
		out = append(out, item)
	}
	return out, nil
}
func normalizedViewers(items []Viewer, defaultLevel string) ([]Viewer, error) {
	seen := map[string]bool{}
	out := make([]Viewer, 0, len(items))
	for _, item := range items {
		item.SystemAccountID = strings.TrimSpace(item.SystemAccountID)
		if item.SystemAccountID == "" {
			continue
		}
		if !known(item.VisibilityReason, "actor_self", "resource_owner", "admin_managed_my_resource", "authorization_owner", "authorization_grantee", "team_member", "team_authorization", "global_affected", "bound_resource_affected") {
			return nil, fmt.Errorf("operation log viewer is invalid")
		}
		if item.DetailLevel == "" {
			item.DetailLevel = defaultLevel
		}
		if !known(item.DetailLevel, "full", "summary") {
			return nil, fmt.Errorf("operation log viewer detail level is invalid")
		}
		key := item.SystemAccountID + ":" + item.VisibilityReason + ":" + item.DetailLevel
		if !seen[key] {
			seen[key] = true
			out = append(out, item)
		}
	}
	return out, nil
}
