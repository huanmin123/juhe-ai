package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/text/unicode/norm"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultOperationLogMode            = "self"
	defaultOperationLogDetailLevel     = "full"
	defaultOperationLogVisibilityScope = "targeted"
	defaultOperationLogTargetRelation  = "primary"
	operationLogActorSelfViewerReason  = "actor_self"
	operationLogResourceOwnerReason    = "resource_owner"
	operationLogAdminManagedReason     = "admin_managed_my_resource"
	maxOperationLogSearchTerms         = 1500
	maxOperationLogSearchTermRunes     = 256
	maxOperationLogSearchGramRunes     = 128
)

func (s *Store) InsertOperationLog(ctx context.Context, input port.OperationLogInput) error {
	normalized, err := normalizeOperationLogInput(input)
	if err != nil {
		return err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin operation log tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	q := s.queries().WithTx(tx)
	insertedID, err := q.InsertOperationLog(ctx, insertOperationLogParams(normalized))
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit duplicate operation log tx: %w", err)
		}
		committed = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("insert operation log: %w", err)
	}

	for _, target := range operationLogTargets(normalized) {
		if err := q.InsertOperationLogTarget(ctx, postgresqueries.InsertOperationLogTargetParams{
			ID:                         "oplogtgt_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			OperationLogID:             insertedID,
			TargetType:                 strings.TrimSpace(target.TargetType),
			TargetID:                   pgText(strings.TrimSpace(target.TargetID)),
			TargetName:                 pgText(strings.TrimSpace(target.TargetName)),
			TargetOwnerSystemAccountID: pgText(strings.TrimSpace(target.TargetOwnerSystemAccountID)),
			Relation:                   defaultText(target.Relation, defaultOperationLogTargetRelation),
			CreatedAt:                  pgTimestamptz(normalized.CreatedAt),
		}); err != nil {
			return fmt.Errorf("insert operation log target: %w", err)
		}
	}

	for _, viewer := range operationLogViewers(normalized) {
		if err := q.InsertOperationLogViewer(ctx, postgresqueries.InsertOperationLogViewerParams{
			OperationLogID:   insertedID,
			SystemAccountID:  strings.TrimSpace(viewer.SystemAccountID),
			VisibilityReason: defaultText(viewer.VisibilityReason, operationLogResourceOwnerReason),
			DetailLevel:      defaultText(viewer.DetailLevel, normalized.DetailLevel),
			CreatedAt:        pgTimestamptz(normalized.CreatedAt),
		}); err != nil {
			return fmt.Errorf("insert operation log viewer: %w", err)
		}
	}

	if terms := operationLogSummarySearchTerms(normalized.Summary); len(terms) > 0 {
		if err := q.InsertOperationLogSearchTerms(ctx, postgresqueries.InsertOperationLogSearchTermsParams{
			OperationLogID: insertedID,
			CreatedAt:      pgTimestamptz(normalized.CreatedAt),
			Terms:          terms,
		}); err != nil {
			return fmt.Errorf("insert operation log search terms: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return fmt.Errorf("commit operation log tx rolled back: %w", err)
		}
		return fmt.Errorf("commit operation log tx: %w", err)
	}
	committed = true
	return nil
}

func normalizeOperationLogInput(input port.OperationLogInput) (port.OperationLogInput, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.TraceID = strings.TrimSpace(input.TraceID)
	input.ActorSystemAccountID = strings.TrimSpace(input.ActorSystemAccountID)
	input.ActorUsername = strings.TrimSpace(input.ActorUsername)
	input.ActorDisplayName = strings.TrimSpace(input.ActorDisplayName)
	input.ActorRole = strings.TrimSpace(input.ActorRole)
	input.OperationScopeSystemAccountID = strings.TrimSpace(input.OperationScopeSystemAccountID)
	input.Mode = defaultText(input.Mode, defaultOperationLogMode)
	input.Module = strings.TrimSpace(input.Module)
	input.Action = strings.TrimSpace(input.Action)
	input.OperationKey = strings.TrimSpace(input.OperationKey)
	input.ResourceType = strings.TrimSpace(input.ResourceType)
	input.ResourceID = strings.TrimSpace(input.ResourceID)
	input.ResourceName = strings.TrimSpace(input.ResourceName)
	input.Summary = strings.TrimSpace(input.Summary)
	input.DetailLevel = defaultText(input.DetailLevel, defaultOperationLogDetailLevel)
	input.VisibilityScope = defaultText(input.VisibilityScope, defaultOperationLogVisibilityScope)
	input.Method = strings.TrimSpace(input.Method)
	input.Path = strings.TrimSpace(input.Path)
	input.ClientIP = strings.TrimSpace(input.ClientIP)
	input.UserAgent = strings.TrimSpace(input.UserAgent)
	input.CreatedAt = input.CreatedAt.UTC()

	if input.ID == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log id is required")
	}
	if input.ActorSystemAccountID == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log actor_system_account_id is required")
	}
	if input.ActorRole == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log actor_role is required")
	}
	if input.Module == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log module is required")
	}
	if input.Action == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log action is required")
	}
	if input.OperationKey == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log operation_key is required")
	}
	if input.ResourceType == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log resource_type is required")
	}
	if input.Summary == "" {
		return port.OperationLogInput{}, fmt.Errorf("operation log summary is required")
	}
	if input.CreatedAt.IsZero() {
		return port.OperationLogInput{}, fmt.Errorf("operation log created_at is required")
	}
	return input, nil
}

func insertOperationLogParams(input port.OperationLogInput) postgresqueries.InsertOperationLogParams {
	return postgresqueries.InsertOperationLogParams{
		ID:                            input.ID,
		TraceID:                       pgText(input.TraceID),
		ActorSystemAccountID:          input.ActorSystemAccountID,
		ActorUsername:                 pgText(input.ActorUsername),
		ActorDisplayName:              pgText(input.ActorDisplayName),
		ActorRole:                     input.ActorRole,
		OperationScopeSystemAccountID: pgText(input.OperationScopeSystemAccountID),
		Mode:                          input.Mode,
		Module:                        input.Module,
		Action:                        input.Action,
		OperationKey:                  input.OperationKey,
		ResourceType:                  input.ResourceType,
		ResourceID:                    pgText(input.ResourceID),
		ResourceName:                  pgText(input.ResourceName),
		Summary:                       input.Summary,
		DetailLevel:                   input.DetailLevel,
		VisibilityScope:               input.VisibilityScope,
		ChangesJson:                   safeOperationLogChangesJSONString(input.Changes),
		MetadataJson:                  safeJSONObjectString(input.Metadata),
		Method:                        pgText(input.Method),
		Path:                          pgText(input.Path),
		StatusCode:                    pgInt4Ptr(input.StatusCode),
		ClientIp:                      pgText(input.ClientIP),
		UserAgent:                     pgText(input.UserAgent),
		CreatedAt:                     pgTimestamptz(input.CreatedAt),
	}
}

func operationLogTargets(input port.OperationLogInput) []port.OperationLogTargetInput {
	targets := make([]port.OperationLogTargetInput, 0, len(input.Targets)+1)
	hasPrimary := false
	for _, target := range input.Targets {
		target.TargetType = strings.TrimSpace(target.TargetType)
		target.Relation = defaultText(target.Relation, defaultOperationLogTargetRelation)
		if target.TargetType == "" {
			continue
		}
		if target.Relation == defaultOperationLogTargetRelation {
			hasPrimary = true
		}
		targets = append(targets, target)
	}
	if !hasPrimary {
		targets = append(targets, port.OperationLogTargetInput{
			TargetType:                 input.ResourceType,
			TargetID:                   input.ResourceID,
			TargetName:                 input.ResourceName,
			TargetOwnerSystemAccountID: input.OperationScopeSystemAccountID,
			Relation:                   defaultOperationLogTargetRelation,
		})
	}
	return targets
}

func operationLogViewers(input port.OperationLogInput) []port.OperationLogViewerInput {
	if input.VisibilityScope == "admin_only" || input.VisibilityScope == "all_users" {
		return nil
	}
	viewers := make([]port.OperationLogViewerInput, 0, len(input.Viewers)+2)
	hasSystemAccount := map[string]bool{}
	seen := map[string]struct{}{}
	add := func(viewer port.OperationLogViewerInput) {
		viewer.SystemAccountID = strings.TrimSpace(viewer.SystemAccountID)
		viewer.VisibilityReason = defaultText(viewer.VisibilityReason, operationLogResourceOwnerReason)
		viewer.DetailLevel = defaultText(viewer.DetailLevel, input.DetailLevel)
		if viewer.SystemAccountID == "" {
			return
		}
		key := viewer.SystemAccountID + "\x00" + viewer.VisibilityReason + "\x00" + viewer.DetailLevel
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		hasSystemAccount[viewer.SystemAccountID] = true
		viewers = append(viewers, viewer)
	}
	for _, viewer := range input.Viewers {
		add(viewer)
	}
	add(port.OperationLogViewerInput{
		SystemAccountID:  input.ActorSystemAccountID,
		VisibilityReason: operationLogActorSelfViewerReason,
		DetailLevel:      input.DetailLevel,
	})
	if input.OperationScopeSystemAccountID != "" &&
		input.OperationScopeSystemAccountID != input.ActorSystemAccountID &&
		!hasSystemAccount[input.OperationScopeSystemAccountID] {
		reason := operationLogResourceOwnerReason
		if isOperationLogAdminRole(input.ActorRole) {
			reason = operationLogAdminManagedReason
		}
		add(port.OperationLogViewerInput{
			SystemAccountID:  input.OperationScopeSystemAccountID,
			VisibilityReason: reason,
			DetailLevel:      input.DetailLevel,
		})
	}
	return viewers
}

func safeOperationLogChangesJSONString(value []port.OperationLogChange) string {
	if value == nil {
		return "[]"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func operationLogSummarySearchTerms(summary string) []string {
	normalized := strings.ToLower(strings.TrimSpace(norm.NFKC.String(summary)))
	if normalized == "" {
		return nil
	}
	parts := strings.FieldsFunc(normalized, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	compact := strings.Join(parts, "")
	terms := make([]string, 0, min(maxOperationLogSearchTerms, 128))
	seen := map[string]struct{}{}
	add := func(value string) bool {
		term := truncateRunes(strings.TrimSpace(value), maxOperationLogSearchTermRunes)
		if term == "" {
			return len(terms) >= maxOperationLogSearchTerms
		}
		if _, exists := seen[term]; exists {
			return len(terms) >= maxOperationLogSearchTerms
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
		return len(terms) >= maxOperationLogSearchTerms
	}
	if add(normalized) || add(compact) {
		return terms
	}
	for _, part := range parts {
		if add(part) {
			return terms
		}
	}
	for _, source := range append([]string{compact}, parts...) {
		runes := []rune(source)
		maxGramLength := min(maxOperationLogSearchGramRunes, len(runes))
		for size := 2; size <= maxGramLength; size++ {
			for start := 0; start+size <= len(runes); start++ {
				if add(string(runes[start : start+size])) {
					return terms
				}
			}
		}
	}
	return terms
}

func truncateRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func defaultText(value string, fallback string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return fallback
	}
	return text
}

func isOperationLogAdminRole(role string) bool {
	role = strings.TrimSpace(role)
	return role == "admin" || role == "super_admin"
}

var _ port.OperationLogStore = (*Store)(nil)
