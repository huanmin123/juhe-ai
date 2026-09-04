package chat

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
)

// Context state machine mirrors chat-context.repository.ts.

// ContextState mirrors ChatContextState.
type ContextState string

// Canonical context state constants (chat-context.repository.ts union).
const (
	StateReady          ContextState = "ready"
	StateCompactPending ContextState = "compact_pending"
	StateCompacting     ContextState = "compacting"
	StateCompactFailed  ContextState = "compact_failed"
)

// ContextHead mirrors ChatContextHead.
type ContextHead struct {
	ConversationID              string
	SystemAccountID             string
	ContextRevision             int64
	ActiveCheckpointID          *string
	CompactedThroughSequence    int64
	ContextState                ContextState
	ActiveContextTokens         *int64
	EffectiveContextLimitTokens *int64
	UsageEstimated              bool
	ContextRetryAt              *string
	ContextAttemptCount         int64
	ContextErrorCode            *string
	NextSequenceNo              int64
}

type contextHeadRow struct {
	id                          string
	systemAccountID             string
	contextRevision             int64
	activeCheckpointID          sql.NullString
	compactedThroughSequence    int64
	contextState                string
	activeContextTokens         sql.NullInt64
	effectiveContextLimitTokens sql.NullInt64
	contextUsageEstimated       int64
	contextRetryAt              sql.NullString
	contextAttemptCount         int64
	contextErrorCode            sql.NullString
	nextSequenceNo              int64
	contextClaimID              sql.NullString
	contextClaimRevision        sql.NullInt64
	contextClaimThroughSequence sql.NullInt64
	contextProgressSequence     int64
	contextProgressEarliest     sql.NullString
	activeTurnID                sql.NullString
}

func scanContextHeadRow(scan func(...any) error) (contextHeadRow, error) {
	var row contextHeadRow
	err := scan(&row.id, &row.systemAccountID, &row.contextRevision, &row.activeCheckpointID,
		&row.compactedThroughSequence, &row.contextState, &row.activeContextTokens,
		&row.effectiveContextLimitTokens, &row.contextUsageEstimated, &row.contextRetryAt,
		&row.contextAttemptCount, &row.contextErrorCode, &row.nextSequenceNo)
	return row, err
}

const contextHeadColumns = `id, system_account_id, context_revision, active_checkpoint_id,
	compacted_through_sequence, context_state, active_context_tokens,
	effective_context_limit_tokens, context_usage_estimated,
	context_retry_at, context_attempt_count, context_error_code, next_sequence_no`

func normalizedContextState(value string) (ContextState, error) {
	switch ContextState(value) {
	case StateReady, StateCompactPending, StateCompacting, StateCompactFailed:
		return ContextState(value), nil
	}
	return "", &DomainError{Message: "未知聊天上下文状态：" + value}
}

func contextHeadFromRow(row contextHeadRow) (*ContextHead, error) {
	state, err := normalizedContextState(row.contextState)
	if err != nil {
		return nil, err
	}
	return &ContextHead{
		ConversationID:              row.id,
		SystemAccountID:             row.systemAccountID,
		ContextRevision:             row.contextRevision,
		ActiveCheckpointID:          nullText(row.activeCheckpointID),
		CompactedThroughSequence:    row.compactedThroughSequence,
		ContextState:                state,
		ActiveContextTokens:         nullInt64(row.activeContextTokens),
		EffectiveContextLimitTokens: nullInt64(row.effectiveContextLimitTokens),
		UsageEstimated:              row.contextUsageEstimated != 0,
		ContextRetryAt:              nullText(row.contextRetryAt),
		ContextAttemptCount:         row.contextAttemptCount,
		ContextErrorCode:            nullText(row.contextErrorCode),
		NextSequenceNo:              row.nextSequenceNo,
	}, nil
}

func nullInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

// GetContextHead mirrors getChatContextHead.
func (s *Store) GetContextHead(conversationID, ownerID string) (*ContextHead, error) {
	row, err := scanContextHeadRow(func(targets ...any) error {
		return s.db.QueryRow(s.bind(`SELECT `+contextHeadColumns+`
			FROM `+s.table("chat_conversations")+`
			WHERE id = ? AND system_account_id = ?
			LIMIT 1`), conversationID, ownerID).Scan(targets...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return contextHeadFromRow(row)
}

// RecordContextUsage mirrors recordChatContextUsage: revision-guarded usage
// write; changes-!==1 → false.
func (s *Store) RecordContextUsage(input RecordContextUsageInput) (bool, error) {
	result, err := s.db.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
		SET active_context_tokens = ?, effective_context_limit_tokens = ?,
			context_usage_estimated = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND context_revision = ?`),
		input.ActiveContextTokens, sqlInt64(input.EffectiveContextLimitTokens),
		boolToInt(input.UsageEstimated), input.Now,
		input.ConversationID, input.SystemAccountID, input.ExpectedContextRevision)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

type RecordContextUsageInput struct {
	ConversationID              string
	SystemAccountID             string
	ExpectedContextRevision     int64
	ActiveContextTokens         int64
	EffectiveContextLimitTokens *int64
	UsageEstimated              bool
	Now                         string
}

func sqlInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// sqlNullText renders an already-scanned nullable text column for storage.
func sqlNullText(value sql.NullString) any {
	if !value.Valid || value.String == "" {
		return nil
	}
	return value.String
}

// sqlNullInt64 renders an already-scanned nullable integer column for storage.
func sqlNullInt64(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func sqlIntFromInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

// RequestContextCompaction mirrors requestChatContextCompaction.
func (s *Store) RequestContextCompaction(input RequestCompactionInput) (bool, error) {
	result, err := s.db.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
		SET context_state = 'compact_pending', context_retry_at = NULL,
			context_error_code = NULL, context_progress_sequence = 0,
			context_progress_earliest_expires_at = NULL, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND context_revision = ?
			AND active_turn_id IS NULL
			AND (
				context_state = 'ready'
				OR (context_state = 'compact_failed' AND context_retry_at IS NOT NULL AND context_retry_at <= ?)
			)
			AND ? > compacted_through_sequence AND ? <= next_sequence_no - 3`),
		input.Now, input.ConversationID, input.SystemAccountID, input.ExpectedRevision, input.Now,
		input.SourceThroughSequence, input.SourceThroughSequence)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

type RequestCompactionInput struct {
	ConversationID       string
	SystemAccountID      string
	ExpectedRevision     int64
	SourceThroughSequence int64
	Now                  string
}

// ContextCompactionClaim mirrors ChatContextCompactionClaim.
type ContextCompactionClaim struct {
	ClaimID              string
	ConversationID       string
	SystemAccountID      string
	SourceRevision       int64
	SourceFromSequence   int64
	SourceThroughSequence int64
	ProgressSequence     int64
	AttemptCount         int64
	ClaimedAt            string
}

// ClaimContextCompaction mirrors claimChatContextCompaction.
func (s *Store) ClaimContextCompaction(input ClaimCompactionInput) (*ContextCompactionClaim, error) {
	claimID := "chat_context_claim_" + randomHex32()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := s.lockedCompactionHead(tx, input)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	storedCheckpointID := nullText(current.activeCheckpointID)
	var activeCheckpoint *checkpoint
	if storedCheckpointID != nil {
		activeCheckpoint, err = s.loadActiveCheckpoint(tx, *storedCheckpointID, input.ConversationID, input.SystemAccountID, input.Now)
		if err != nil {
			return nil, err
		}
	}
	invalidatedCheckpoint := storedCheckpointID != nil && activeCheckpoint == nil
	// Node: activeCheckpoint ? Number(current.compacted_through_sequence) : 0.
	effectiveCompactedThrough := int64(0)
	if activeCheckpoint != nil {
		effectiveCompactedThrough = current.compactedThroughSequence
	}
	claimRevision := input.ExpectedRevision
	if invalidatedCheckpoint {
		claimRevision++
	}
	result, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
		SET context_revision = ?, active_checkpoint_id = ?,
			compacted_through_sequence = ?,
			active_context_tokens = CASE WHEN ? = 1 THEN NULL ELSE active_context_tokens END,
			context_usage_estimated = CASE WHEN ? = 1 THEN 1 ELSE context_usage_estimated END,
			context_state = 'compacting', context_claim_id = ?,
			context_claim_revision = ?, context_claim_through_sequence = ?,
			context_claimed_at = ?, context_retry_at = NULL,
			context_error_code = NULL, context_progress_sequence = ?,
			context_progress_earliest_expires_at = ?,
			updated_at = ?
		WHERE id = ? AND system_account_id = ? AND context_revision = ?
			AND active_turn_id IS NULL
			AND compacted_through_sequence = ?
			AND ? > ? AND ? <= next_sequence_no - 3
			AND (
				context_state IN ('ready', 'compact_pending')
				OR (context_state = 'compact_failed' AND context_retry_at IS NOT NULL AND context_retry_at <= ?)
				OR (context_state = 'compacting' AND context_claimed_at <= ?)
			)`),
		claimRevision, sqlText(activeCheckpointID(activeCheckpoint)), effectiveCompactedThrough,
		boolToInt(invalidatedCheckpoint), boolToInt(invalidatedCheckpoint),
		claimID, claimRevision, input.SourceThroughSequence, input.Now,
		effectiveCompactedThrough, activeCheckpointExpires(activeCheckpoint),
		input.Now, input.ConversationID, input.SystemAccountID, input.ExpectedRevision,
		current.compactedThroughSequence, input.SourceThroughSequence, effectiveCompactedThrough,
		input.SourceThroughSequence, input.Now, input.StaleClaimBefore)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, nil
	}
	if invalidatedCheckpoint {
		if _, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_context_checkpoints")+`
			SET status = 'superseded'
			WHERE id = ? AND conversation_id = ? AND system_account_id = ?
				AND status = 'active' AND expires_at <= ?`),
			*storedCheckpointID, input.ConversationID, input.SystemAccountID, input.Now); err != nil {
			return nil, err
		}
	}
	claim, err := s.findCompactionClaim(tx, input.ConversationID, input.SystemAccountID, claimID)
	if err != nil {
		return nil, err
	}
	if claim == nil {
		return nil, &ContextConflictError{Message: "聊天上下文压缩认领已失效"}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claim, nil
}

type ClaimCompactionInput struct {
	ConversationID        string
	SystemAccountID       string
	ExpectedRevision      int64
	SourceThroughSequence int64
	Now                   string
	StaleClaimBefore      string
}

func activeCheckpointID(checkpoint *checkpoint) *string {
	if checkpoint == nil {
		return nil
	}
	return &checkpoint.id
}

func activeCheckpointExpires(checkpoint *checkpoint) any {
	if checkpoint == nil {
		return nil
	}
	return checkpoint.expiresAt
}

func (s *Store) lockedCompactionHead(tx queryer, input ClaimCompactionInput) (contextHeadRow, error) {
	return scanContextHeadRow(func(targets ...any) error {
		return tx.QueryRow(s.bind(`SELECT `+contextHeadColumns+`
			FROM `+s.table("chat_conversations")+`
			WHERE id = ? AND system_account_id = ? AND context_revision = ?`+s.lockSuffix()),
			input.ConversationID, input.SystemAccountID, input.ExpectedRevision).Scan(targets...)
	})
}

func (s *Store) findCompactionClaim(q queryer, conversationID, ownerID, claimID string) (*ContextCompactionClaim, error) {
	var id, systemAccountID, contextClaimID, claimedAt string
	var contextClaimRevision, compactedThrough, claimThrough, progressSequence, attemptCount int64
	err := q.QueryRow(s.bind(`SELECT id, system_account_id, context_claim_id, context_claim_revision,
			compacted_through_sequence, context_claim_through_sequence,
			context_progress_sequence, context_attempt_count, context_claimed_at
		FROM `+s.table("chat_conversations")+`
		WHERE id = ? AND system_account_id = ?
			AND context_state = 'compacting' AND context_claim_id = ?
		LIMIT 1`), conversationID, ownerID, claimID).Scan(
		&id, &systemAccountID, &contextClaimID, &contextClaimRevision,
		&compactedThrough, &claimThrough, &progressSequence, &attemptCount, &claimedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ContextCompactionClaim{
		ClaimID:              contextClaimID,
		ConversationID:       id,
		SystemAccountID:      systemAccountID,
		SourceRevision:       contextClaimRevision,
		SourceFromSequence:   compactedThrough + 1,
		SourceThroughSequence: claimThrough,
		ProgressSequence:     progressSequence,
		AttemptCount:         attemptCount + 1,
		ClaimedAt:            claimedAt,
	}, nil
}

func randomHex32() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// checkpoint mirrors ChatContextCheckpoint.
type checkpoint struct {
	id                     string
	conversationID         string
	systemAccountID        string
	version                int64
	sourceRevision         int64
	sourceFromSequence     int64
	sourceThroughSequence  int64
	recentTailFromSequence int64
	entryFromSequence      int64
	entryThroughSequence   int64
	payloadDigest          string
	estimatedInputTokens   sql.NullInt64
	upstreamInputTokens    sql.NullInt64
	requestBodyBytes       int64
	modelID                string
	providerCode           sql.NullString
	providerProfileID      sql.NullString
	endpointFamily         string
	compatHash             sql.NullString
	promptVersion          string
	status                 string
	qualityStatus          string
	createdAt              string
	expiresAt              string
}

const checkpointColumns = `id, conversation_id, system_account_id, version, source_revision,
	source_from_sequence, source_through_sequence, recent_tail_from_sequence,
	entry_from_sequence, entry_through_sequence, payload_digest,
	estimated_input_tokens, upstream_input_tokens, request_body_bytes,
	model_id, provider_code, provider_profile_id, endpoint_family,
	compact_compatibility_hash, prompt_version, status, quality_status,
	created_at, expires_at`

func scanCheckpoint(scan func(...any) error) (*checkpoint, error) {
	row := &checkpoint{}
	err := scan(&row.id, &row.conversationID, &row.systemAccountID, &row.version, &row.sourceRevision,
		&row.sourceFromSequence, &row.sourceThroughSequence, &row.recentTailFromSequence,
		&row.entryFromSequence, &row.entryThroughSequence, &row.payloadDigest,
		&row.estimatedInputTokens, &row.upstreamInputTokens, &row.requestBodyBytes,
		&row.modelID, &row.providerCode, &row.providerProfileID, &row.endpointFamily,
		&row.compatHash, &row.promptVersion, &row.status, &row.qualityStatus,
		&row.createdAt, &row.expiresAt)
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (s *Store) loadActiveCheckpoint(q queryer, checkpointID, conversationID, ownerID, now string) (*checkpoint, error) {
	row, err := scanCheckpoint(func(targets ...any) error {
		return q.QueryRow(s.bind(`SELECT `+checkpointColumns+`
			FROM `+s.table("chat_context_checkpoints")+`
			WHERE id = ? AND conversation_id = ? AND system_account_id = ?
				AND status = 'active' AND expires_at > ?
			LIMIT 1`), checkpointID, conversationID, ownerID, now).Scan(targets...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return row, err
}

// contextSourceMessage mirrors ChatContextSourceMessage.
type contextSourceMessage struct {
	id                string
	turnID            string
	sequenceNo        int64
	role              string
	contentText       string
	contentBlocksJSON string
	modelID           string
	contentBytes      int64
	createdAt         string
	completedAt       sql.NullString
	expiresAt         string
}

// ModelContextLoadResult mirrors ChatModelContextLoadResult; TruncatedAt uses
// the Node literals 'checkpoint_entries' | 'suffix_messages'.
type ModelContextLoadResult struct {
	Head        *ContextHead
	Checkpoint  *checkpoint
	Entries     []contextEntry
	Suffix      []contextSourceMessage
	LoadedBytes int64
	Complete    bool
	TruncatedAt *string
}

type contextEntry struct {
	conversationID  string
	checkpointID    string
	sequence        int64
	sourceMessageID sql.NullString
	kind            string
	contentJSON     string
	contentBytes    int64
	provenance      string
	trustLevel      string
	tokenCount      sql.NullInt64
	createdAt       string
	expiresAt       string
}

const (
	maxContextLoadRows  = 512
	maxContextLoadBytes = 16 * 1024 * 1024
)

// LoadModelContext mirrors loadChatModelContext: active-checkpoint entries
// first (row + byte budgets, whole-pair suffix turns), truncation flags when
// either budget cuts the window.
func (s *Store) LoadModelContext(conversationID, ownerID, nowValue string, maxRows, maxBytes int) (*ModelContextLoadResult, error) {
	if maxRows < 1 || maxRows > maxContextLoadRows {
		return nil, &DomainError{Message: "maxRows 必须是 1..512 的整数"}
	}
	if maxBytes < 1 || maxBytes > maxContextLoadBytes {
		return nil, &DomainError{Message: "maxBytes 必须是 1..16777216 的整数"}
	}
	if _, err := requireRFC3339Instant(nowValue, "聊天上下文 now"); err != nil {
		return nil, err
	}
	headRow, err := scanContextHeadRow(func(targets ...any) error {
		return s.db.QueryRow(s.bind(`SELECT `+contextHeadColumns+`
			FROM `+s.table("chat_conversations")+`
			WHERE id = ? AND system_account_id = ?
			LIMIT 1`), conversationID, ownerID).Scan(targets...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	storedHead, err := contextHeadFromRow(headRow)
	if err != nil {
		return nil, err
	}
	var activeCheckpoint *checkpoint
	if storedHead.ActiveCheckpointID != nil {
		activeCheckpoint, err = s.loadActiveCheckpoint(s.db, *storedHead.ActiveCheckpointID, conversationID, ownerID, nowValue)
		if err != nil {
			return nil, err
		}
	}
	head := *storedHead
	if activeCheckpoint == nil {
		head.ActiveCheckpointID = nil
		head.CompactedThroughSequence = 0
	}
	result := &ModelContextLoadResult{Head: &head, Checkpoint: activeCheckpoint}
	loadedBytes := int64(0)
	if activeCheckpoint != nil {
		entryRows, err := s.queryContextEntries(conversationID, activeCheckpoint.id, maxRows+1)
		if err != nil {
			return nil, err
		}
		for _, row := range entryRows {
			if len(result.Entries) >= maxRows {
				truncated := "checkpoint_entries"
				result.TruncatedAt = &truncated
				result.Complete = false
				return result, nil
			}
			entry := row
			if loadedBytes+entry.contentBytes > int64(maxBytes) {
				truncated := "checkpoint_entries"
				result.TruncatedAt = &truncated
				result.Complete = false
				return result, nil
			}
			result.Entries = append(result.Entries, entry)
			loadedBytes += entry.contentBytes
		}
		if len(entryRows) > maxRows {
			truncated := "checkpoint_entries"
			result.TruncatedAt = &truncated
			result.Complete = false
			return result, nil
		}
		expectedEntryCount := activeCheckpoint.entryThroughSequence - activeCheckpoint.entryFromSequence + 1
		if int64(len(result.Entries)) != expectedEntryCount {
			truncated := "checkpoint_entries"
			result.TruncatedAt = &truncated
			result.Complete = false
			return result, nil
		}
	}
	remainingRows := maxRows - len(result.Entries)
	suffixRowBudget := remainingRows - remainingRows%2
	messagesTable := s.table("chat_messages")
	rows, err := s.db.Query(s.bind(`SELECT source.id, source.turn_id, source.sequence_no, source.role, source.content_text,
			source.content_blocks_json, source.content_bytes, source.model, source.created_at, source.completed_at, source.expires_at
		FROM `+messagesTable+` AS source
		WHERE source.conversation_id = ? AND source.system_account_id = ?
			AND source.status = 'completed' AND source.expires_at > ? AND source.sequence_no > ?
			AND EXISTS (
				SELECT 1 FROM `+messagesTable+` AS pair
				WHERE pair.conversation_id = source.conversation_id
					AND pair.system_account_id = source.system_account_id
					AND pair.turn_id = source.turn_id
					AND pair.status = 'completed' AND pair.expires_at > ?
					AND (
						(source.role = 'user' AND pair.role = 'assistant' AND pair.sequence_no = source.sequence_no + 1)
						OR (source.role = 'assistant' AND pair.role = 'user' AND pair.sequence_no = source.sequence_no - 1)
					)
			)
		ORDER BY source.sequence_no ASC
		LIMIT ?`), conversationID, ownerID, nowValue, head.CompactedThroughSequence, nowValue, suffixRowBudget+2)
	if err != nil {
		return nil, err
	}
	suffixRows := []contextSourceMessage{}
	for rows.Next() {
		var message contextSourceMessage
		var contentText, contentBlocksJSON string
		var contentBytes int64
		if err := rows.Scan(&message.id, &message.turnID, &message.sequenceNo, &message.role, &contentText,
			&contentBlocksJSON, &contentBytes, &message.modelID, &message.createdAt, &message.completedAt, &message.expiresAt); err != nil {
			rows.Close()
			return nil, err
		}
		message.contentText = contentText
		message.contentBlocksJSON = contentBlocksJSON
		message.contentBytes = maxI64(contentBytes, int64(len(contentText)+len(contentBlocksJSON)))
		suffixRows = append(suffixRows, message)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	limit := len(suffixRows)
	if limit > suffixRowBudget {
		limit = suffixRowBudget
	}
	for index := 0; index < limit; index += 2 {
		if index+1 >= len(suffixRows) {
			break
		}
		user := suffixRows[index]
		assistant := suffixRows[index+1]
		if user.role != "user" || assistant.role != "assistant" ||
			user.turnID != assistant.turnID || assistant.sequenceNo != user.sequenceNo+1 {
			return nil, &DomainError{Message: "聊天上下文完整轮次顺序不一致"}
		}
		pairBytes := user.contentBytes + assistant.contentBytes
		if loadedBytes+pairBytes > int64(maxBytes) {
			truncated := "suffix_messages"
			result.TruncatedAt = &truncated
			result.Complete = false
			return result, nil
		}
		result.Suffix = append(result.Suffix, user, assistant)
		loadedBytes += pairBytes
	}
	complete := len(suffixRows) <= suffixRowBudget
	result.LoadedBytes = loadedBytes
	result.Complete = complete
	if !complete {
		truncated := "suffix_messages"
		result.TruncatedAt = &truncated
	}
	return result, nil
}

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (s *Store) queryContextEntries(conversationID, checkpointID string, limit int) ([]contextEntry, error) {
	rows, err := s.db.Query(s.bind(`SELECT conversation_id, checkpoint_id, sequence, source_message_id, kind,
			content_json, content_bytes, provenance, trust_level, token_count,
			created_at, expires_at
		FROM `+s.table("chat_context_entries")+`
		WHERE conversation_id = ? AND checkpoint_id = ?
		ORDER BY sequence ASC
		LIMIT ?`), conversationID, checkpointID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contextEntry{}
	for rows.Next() {
		var entry contextEntry
		if err := rows.Scan(&entry.conversationID, &entry.checkpointID, &entry.sequence, &entry.sourceMessageID,
			&entry.kind, &entry.contentJSON, &entry.contentBytes, &entry.provenance, &entry.trustLevel,
			&entry.tokenCount, &entry.createdAt, &entry.expiresAt); err != nil {
			return nil, err
		}
		// Node: Math.max(Number(row.content_bytes), byteLength(content_json)).
		entry.contentBytes = maxI64(entry.contentBytes, int64(len(entry.contentJSON)))
		out = append(out, entry)
	}
	return out, rows.Err()
}

// InstallContextCheckpoint mirrors installChatContextCheckpoint: claim +
// progress validation, checkpoint + entries insert, supersede + activate,
// revision-bumped head install.
func (s *Store) InstallContextCheckpoint(input InstallCheckpointInput) (*checkpoint, error) {
	if input.SourceRevision < 0 {
		return nil, &DomainError{Message: "sourceRevision 必须是非负安全整数"}
	}
	if input.SourceThroughSequence < 1 {
		return nil, &DomainError{Message: "sourceThroughSequence 必须是正安全整数"}
	}
	expiresAt, err := requireRFC3339Instant(input.ExpiresAt, "expiresAt")
	if err != nil {
		return nil, err
	}
	now, err := requireRFC3339Instant(input.Now, "now")
	if err != nil {
		return nil, err
	}
	expiresAtMs, okMs := rfc3339Millis(expiresAt)
	nowMs, okNow := rfc3339Millis(now)
	if !okMs || !okNow {
		return nil, &DomainError{Message: "checkpoint 时间必须是带 Z 或数值 offset 的 RFC3339 时间"}
	}
	if expiresAtMs <= nowMs {
		return nil, &DomainError{Message: "不能安装已过期的 checkpoint"}
	}
	checkpointID := input.CheckpointID
	if checkpointID == "" {
		checkpointID = "chat_checkpoint_" + randomHex32()
	}
	if !validControlFreeText(checkpointID, 128) {
		return nil, &DomainError{Message: "checkpointId 无效"}
	}
	if !digestPattern.MatchString(stringsLower(input.PayloadDigest)) {
		return nil, &DomainError{Message: "payloadDigest 必须是 SHA-256 十六进制摘要"}
	}
	payloadDigest := stringsLower(input.PayloadDigest)
	version := input.SourceRevision + 1
	entries, err := serializeCheckpointEntries(input.Entries, checkpointID, input.ConversationID, now, expiresAt)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := s.lockedInstallHead(tx, input.ConversationID, input.SystemAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ContextConflictError{}
	}
	if err != nil {
		return nil, err
	}
	if current.contextRevision != input.SourceRevision ||
		current.contextState != string(StateCompacting) ||
		nullText(current.contextClaimID) == nil || *nullText(current.contextClaimID) != input.ClaimID ||
		current.contextClaimRevision.Int64 != input.SourceRevision ||
		current.contextClaimThroughSequence.Int64 != input.SourceThroughSequence ||
		current.contextProgressSequence != input.SourceThroughSequence ||
		!current.contextProgressEarliest.Valid ||
		current.activeTurnID.Valid ||
		input.SourceThroughSequence > current.nextSequenceNo-3 {
		return nil, &ContextConflictError{}
	}
	earliestExpiresAt, err := requireRFC3339Instant(current.contextProgressEarliest.String, "contextProgressEarliestExpiresAt")
	if err != nil {
		return nil, err
	}
	earliestMs, okEarliest := rfc3339Millis(earliestExpiresAt)
	if !okEarliest {
		return nil, &DomainError{Message: "contextProgressEarliestExpiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间"}
	}
	if expiresAtMs > earliestMs {
		return nil, &DomainError{Message: "checkpoint 过期时间不能晚于来源消息最早过期时间"}
	}
	checkpointSourceFromSequence := current.compactedThroughSequence + 1
	if current.activeCheckpointID.Valid {
		activeRow, err := scanCheckpoint(func(targets ...any) error {
			return tx.QueryRow(s.bind(`SELECT `+checkpointColumns+`
				FROM `+s.table("chat_context_checkpoints")+` WHERE id = ? LIMIT 1`),
				current.activeCheckpointID.String).Scan(targets...)
		})
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (activeRow.status != "active" || activeRow.conversationID != input.ConversationID)) {
			return nil, &ContextConflictError{Message: "活动 checkpoint 已变化，当前压缩结果不能安装"}
		}
		if err != nil {
			return nil, err
		}
		checkpointSourceFromSequence = activeRow.sourceFromSequence
	}
	if _, err := tx.Exec(s.bind(`INSERT INTO `+s.table("chat_context_checkpoints")+` (
		id, conversation_id, system_account_id, version, source_revision,
		source_from_sequence, source_through_sequence, recent_tail_from_sequence,
		entry_from_sequence, entry_through_sequence, payload_digest,
		estimated_input_tokens, upstream_input_tokens, request_body_bytes,
		model_id, provider_code, provider_profile_id, endpoint_family,
		compact_compatibility_hash, prompt_version, status, quality_status,
		created_at, expires_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 'passed', ?, ?)`),
		checkpointID, input.ConversationID, input.SystemAccountID, version, input.SourceRevision,
		checkpointSourceFromSequence, input.SourceThroughSequence, input.SourceThroughSequence+1,
		len(entries), payloadDigest, sqlInt64(input.EstimatedInputTokens), sqlInt64(input.UpstreamInputTokens),
		input.RequestBodyBytes, input.ModelID, sqlText(input.ProviderCode), sqlText(input.ProviderProfileID),
		input.EndpointFamily, sqlText(input.CompactCompatibilityHash), input.PromptVersion,
		now, expiresAt); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if _, err := tx.Exec(s.bind(`INSERT INTO `+s.table("chat_context_entries")+` (
			conversation_id, checkpoint_id, sequence, source_message_id, kind,
			content_json, content_bytes, provenance, trust_level, token_count,
			created_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			entry.conversationID, entry.checkpointID, entry.sequence, sqlNullText(entry.sourceMessageID),
			entry.kind, entry.contentJSON, entry.contentBytes, entry.provenance, entry.trustLevel,
			sqlNullInt64(entry.tokenCount), entry.createdAt, entry.expiresAt); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_context_checkpoints")+`
		SET status = 'superseded'
		WHERE conversation_id = ? AND status = 'active'`), input.ConversationID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_context_checkpoints")+`
		SET status = 'active'
		WHERE id = ? AND conversation_id = ? AND status = 'pending'`),
		checkpointID, input.ConversationID); err != nil {
		return nil, err
	}
	installed, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
		SET context_revision = context_revision + 1, active_checkpoint_id = ?,
			compacted_through_sequence = ?, context_state = 'ready',
			active_context_tokens = ?, effective_context_limit_tokens = ?,
			context_usage_estimated = ?,
			context_claim_id = NULL, context_claim_revision = NULL,
			context_claim_through_sequence = NULL, context_claimed_at = NULL,
			context_retry_at = NULL, context_error_code = NULL,
			context_attempt_count = 0,
			context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
			updated_at = ?
		WHERE id = ? AND system_account_id = ? AND context_revision = ?
			AND active_turn_id IS NULL
			AND context_state = 'compacting' AND context_claim_id = ?
			AND context_claim_revision = ? AND context_claim_through_sequence = ?`),
		checkpointID, input.SourceThroughSequence,
		sqlInt64(activeContextTokensFor(input)), sqlInt64(input.EffectiveContextLimitTokens),
		boolToInt(input.UpstreamInputTokens == nil), now,
		input.ConversationID, input.SystemAccountID, input.SourceRevision,
		input.ClaimID, input.SourceRevision, input.SourceThroughSequence)
	if err != nil {
		return nil, err
	}
	if affected, _ := installed.RowsAffected(); affected != 1 {
		return nil, &ContextConflictError{}
	}
	installedRow, err := scanCheckpoint(func(targets ...any) error {
		return tx.QueryRow(s.bind(`SELECT `+checkpointColumns+`
			FROM `+s.table("chat_context_checkpoints")+` WHERE id = ? LIMIT 1`), checkpointID).Scan(targets...)
	})
	if err != nil {
		return nil, &DomainError{Message: "checkpoint 安装后读取失败"}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return installedRow, nil
}

type InstallCheckpointInput struct {
	CheckpointID               string
	ClaimID                    string
	ConversationID             string
	SystemAccountID            string
	SourceRevision             int64
	SourceThroughSequence      int64
	ExpiresAt                  string
	PayloadDigest              string
	EstimatedInputTokens       *int64
	UpstreamInputTokens        *int64
	ActiveContextTokens        *int64
	EffectiveContextLimitTokens *int64
	RequestBodyBytes           int64
	ModelID                    string
	ProviderCode               *string
	ProviderProfileID          *string
	EndpointFamily             string
	CompactCompatibilityHash   *string
	PromptVersion              string
	Entries                    []CheckpointEntryInput
	Now                        string
}

func activeContextTokensFor(input InstallCheckpointInput) *int64 {
	if input.ActiveContextTokens != nil {
		return input.ActiveContextTokens
	}
	if input.UpstreamInputTokens != nil {
		return input.UpstreamInputTokens
	}
	return input.EstimatedInputTokens
}

func (s *Store) lockedInstallHead(tx queryer, conversationID, ownerID string) (contextHeadRow, error) {
	row := contextHeadRow{}
	err := tx.QueryRow(s.bind(`SELECT id, system_account_id, context_revision, active_checkpoint_id,
			compacted_through_sequence, context_state, active_context_tokens, active_turn_id,
			effective_context_limit_tokens, next_sequence_no,
			context_usage_estimated,
			context_claim_id, context_claim_revision, context_claim_through_sequence,
			context_progress_sequence, context_progress_earliest_expires_at
		FROM `+s.table("chat_conversations")+`
		WHERE id = ? AND system_account_id = ?`+s.lockSuffix()), conversationID, ownerID).Scan(
		&row.id, &row.systemAccountID, &row.contextRevision, &row.activeCheckpointID,
		&row.compactedThroughSequence, &row.contextState, &row.activeContextTokens, &row.activeTurnID,
		&row.effectiveContextLimitTokens, &row.nextSequenceNo,
		&row.contextUsageEstimated,
		&row.contextClaimID, &row.contextClaimRevision, &row.contextClaimThroughSequence,
		&row.contextProgressSequence, &row.contextProgressEarliest)
	return row, err
}

// CheckpointEntryInput mirrors ChatContextCheckpointEntryInput.
type CheckpointEntryInput struct {
	SourceMessageID string
	Kind            string
	Content         json.RawMessage
	Provenance      string
	TrustLevel      string
	TokenCount      *int64
}

const (
	maxCheckpointEntries     = 256
	maxCheckpointEntryBytes  = 256 * 1024
	maxCheckpointPayloadByte = 2 * 1024 * 1024
)

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func stringsLower(value string) string {
	out := []rune(value)
	for i, r := range out {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + ('a' - 'A')
		}
	}
	return string(out)
}

func validControlFreeText(value string, maxLength int) bool {
	normalized := trimSpace(value)
	if normalized == "" || utf8Len(normalized) > maxLength {
		return false
	}
	for _, r := range normalized {
		if (r >= 0x00 && r <= 0x1f) || r == 0x7f {
			return false
		}
	}
	return true
}

func serializeCheckpointEntries(inputs []CheckpointEntryInput, checkpointID, conversationID, createdAt, expiresAt string) ([]contextEntry, error) {
	if len(inputs) < 1 || len(inputs) > maxCheckpointEntries {
		return nil, &DomainError{Message: "checkpoint entry 数量必须在 1..256 之间"}
	}
	payloadBytes := int64(0)
	entries := make([]contextEntry, 0, len(inputs))
	for index, input := range inputs {
		contentJSON := string(input.Content)
		contentBytes := int64(len(contentJSON))
		if contentBytes < 2 || contentBytes > maxCheckpointEntryBytes {
			return nil, &DomainError{Message: "checkpoint entry " + itoa(index+1) + " 超过单条大小限制"}
		}
		payloadBytes += contentBytes
		if payloadBytes > maxCheckpointPayloadByte {
			return nil, &DomainError{Message: "checkpoint entries 超过总大小限制"}
		}
		if !validEntryKind(input.Kind) {
			return nil, &DomainError{Message: "未知 checkpoint entry 类型：" + input.Kind}
		}
		if !validProvenance(input.Provenance) {
			return nil, &DomainError{Message: "未知 checkpoint provenance：" + input.Provenance}
		}
		if !validTrustLevel(input.TrustLevel) {
			return nil, &DomainError{Message: "未知 checkpoint trust level：" + input.TrustLevel}
		}
		var sourceMessageID *string
		if input.SourceMessageID != "" {
			if !validControlFreeText(input.SourceMessageID, 128) {
				return nil, &DomainError{Message: "sourceMessageId 无效"}
			}
			sourceMessageID = &input.SourceMessageID
		}
		entries = append(entries, contextEntry{
			conversationID:  conversationID,
			checkpointID:    checkpointID,
			sequence:        int64(index + 1),
			sourceMessageID: sql.NullString{String: valueOrEmpty(sourceMessageID), Valid: sourceMessageID != nil},
			kind:            input.Kind,
			contentJSON:     contentJSON,
			contentBytes:    contentBytes,
			provenance:      input.Provenance,
			trustLevel:      input.TrustLevel,
			tokenCount:      sql.NullInt64{Int64: derefI64(input.TokenCount), Valid: input.TokenCount != nil},
			createdAt:       createdAt,
			expiresAt:       expiresAt,
		})
	}
	return entries, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefI64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func validEntryKind(value string) bool {
	switch value {
	case "verbatim", "durable_memory", "task_state", "tool_result", "image_observation", "provider_compaction":
		return true
	}
	return false
}

func validProvenance(value string) bool {
	switch value {
	case "user", "assistant", "tool", "asset", "provider":
		return true
	}
	return false
}

func validTrustLevel(value string) bool {
	switch value {
	case "untrusted", "assistant_derived", "provider_opaque":
		return true
	}
	return false
}

// RecordCompactionProgress mirrors recordChatContextCompactionProgress.
func (s *Store) RecordCompactionProgress(input RecordCompactionProgressInput) (bool, error) {
	result, err := s.db.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
		SET context_progress_sequence = ?,
			context_progress_earliest_expires_at = CASE
				WHEN context_progress_earliest_expires_at IS NULL OR context_progress_earliest_expires_at > ? THEN ?
				ELSE context_progress_earliest_expires_at
			END,
			context_claimed_at = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ?
			AND context_state = 'compacting' AND context_claim_id = ?
			AND context_progress_sequence < ? AND context_claim_through_sequence >= ?`),
		input.ThroughSequence, input.EarliestExpiresAt, input.EarliestExpiresAt, input.Now, input.Now,
		input.ConversationID, input.SystemAccountID, input.ClaimID, input.ThroughSequence, input.ThroughSequence)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

type RecordCompactionProgressInput struct {
	ConversationID     string
	SystemAccountID    string
	ClaimID            string
	ThroughSequence    int64
	EarliestExpiresAt  string
	Now                string
}

// ReleaseCompactionClaim mirrors releaseChatContextCompactionClaim.
func (s *Store) ReleaseCompactionClaim(conversationID, ownerID, claimID, now string) (bool, error) {
	result, err := s.db.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
		SET context_state = 'compact_pending', context_claim_id = NULL,
			context_claim_revision = NULL, context_claim_through_sequence = NULL,
			context_claimed_at = NULL, context_retry_at = NULL,
			context_error_code = NULL, context_progress_sequence = 0,
			context_progress_earliest_expires_at = NULL, updated_at = ?
		WHERE id = ? AND system_account_id = ?
			AND context_state = 'compacting' AND context_claim_id = ?`),
		now, conversationID, ownerID, claimID)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

// FailCompaction mirrors failChatContextCompaction.
func (s *Store) FailCompaction(input FailCompactionInput) (bool, error) {
	if !validControlFreeText(input.ErrorCode, 128) {
		return false, &DomainError{Message: "errorCode 无效"}
	}
	result, err := s.db.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
		SET context_state = 'compact_failed', context_claim_id = NULL,
			context_claim_revision = NULL, context_claim_through_sequence = NULL,
			context_claimed_at = NULL, context_retry_at = ?, context_error_code = ?,
			context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
			context_attempt_count = context_attempt_count + 1,
			updated_at = ?
		WHERE id = ? AND system_account_id = ?
			AND context_state = 'compacting' AND context_claim_id = ?`),
		sqlText(input.RetryAt), input.ErrorCode, input.Now,
		input.ConversationID, input.SystemAccountID, input.ClaimID)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

type FailCompactionInput struct {
	ConversationID  string
	SystemAccountID string
	ClaimID         string
	ErrorCode       string
	RetryAt         *string
	Now             string
}

// FailPendingCompaction mirrors failPendingChatContextCompaction.
func (s *Store) FailPendingCompaction(input FailPendingCompactionInput) (bool, error) {
	if !validControlFreeText(input.ErrorCode, 128) {
		return false, &DomainError{Message: "errorCode 无效"}
	}
	result, err := s.db.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
		SET context_state = 'compact_failed', context_claim_id = NULL,
			context_claim_revision = NULL, context_claim_through_sequence = NULL,
			context_claimed_at = NULL, context_retry_at = ?, context_error_code = ?,
			context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
			context_attempt_count = context_attempt_count + 1,
			updated_at = ?
		WHERE id = ? AND system_account_id = ? AND context_revision = ?
			AND context_state = 'compact_pending'`),
		sqlText(input.RetryAt), input.ErrorCode, input.Now,
		input.ConversationID, input.SystemAccountID, input.ExpectedRevision)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}

type FailPendingCompactionInput struct {
	ConversationID  string
	SystemAccountID string
	ExpectedRevision int64
	ErrorCode       string
	RetryAt         *string
	Now             string
}
