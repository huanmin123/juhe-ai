package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// Turn lifecycle mirrors acceptChatTurn / assertChatTurnReplaceable /
// completeChatTurn / failChatTurn / cancelChatTurn / cancelActiveChatTurnIfMatches /
// failInterruptedChatTurnIfMatches in chat.repository.ts.

// TurnSubmissionFact mirrors ChatTurnSubmissionFact.
type TurnSubmissionFact struct {
	TurnID             string
	AssistantMessageID string
	AssistantStatus    ChatMessageStatus
	ErrorCode          *string
	ErrorMessage       *string
	CompletedAt        *string
	TraceID            *string
}

// AcceptTurnResult mirrors the acceptChatTurn return shape.
type AcceptTurnResult struct {
	TurnID           string
	UserMessage      *Message
	AssistantMessage *Message
	Duplicate        bool
}

type AcceptTurnInput struct {
	ConversationID          string
	SystemAccountID         string
	ClientMessageID         string
	UserContent             string
	ContentBlocks           []InputContentBlock
	Model                   string
	Now                     string
	StorageQuotaBytes       int64
	RetentionDays           int
	MaxTurnsPerConversation int64
	ReplaceTurnID           string
}

// messageRow is the raw chat_messages scan target.
type messageRow struct {
	id                   string
	conversationID       string
	systemAccountID      string
	turnID               string
	sequenceNo           int64
	clientMessageID      sql.NullString
	role                 string
	status               string
	contentText          string
	contentBlocksJSON    string
	contentBytes         int64
	storageReservedBytes int64
	model                string
	traceID              sql.NullString
	finishReason         sql.NullString
	errorCode            sql.NullString
	errorMessage         sql.NullString
	createdAt            string
	completedAt          sql.NullString
	expiresAt            string
}

const messageColumns = `id, conversation_id, system_account_id, turn_id, sequence_no, client_message_id,
	role, status, content_text, content_blocks_json, content_bytes, storage_reserved_bytes, model,
	trace_id, finish_reason, error_code, error_message, created_at, completed_at, expires_at`

func scanMessageRow(scan func(...any) error) (messageRow, error) {
	var row messageRow
	err := scan(&row.id, &row.conversationID, &row.systemAccountID, &row.turnID, &row.sequenceNo,
		&row.clientMessageID, &row.role, &row.status, &row.contentText, &row.contentBlocksJSON,
		&row.contentBytes, &row.storageReservedBytes, &row.model, &row.traceID, &row.finishReason,
		&row.errorCode, &row.errorMessage, &row.createdAt, &row.completedAt, &row.expiresAt)
	return row, err
}

func mapMessage(row messageRow) (*Message, error) {
	if _, err := requireRFC3339Instant(row.createdAt, "聊天消息 created_at"); err != nil {
		return nil, err
	}
	if _, err := requireRFC3339Instant(row.expiresAt, "聊天消息 expires_at"); err != nil {
		return nil, err
	}
	var completedAt *string
	if row.completedAt.Valid {
		normalized, err := requireRFC3339Instant(row.completedAt.String, "聊天消息 completed_at")
		if err != nil {
			return nil, err
		}
		completedAt = &normalized
	}
	return &Message{
		ID:              row.id,
		ConversationID:  row.conversationID,
		TurnID:          row.turnID,
		SequenceNo:      row.sequenceNo,
		ClientMessageID: nullText(row.clientMessageID),
		Role:            ChatMessageRole(row.role),
		Status:          ChatMessageStatus(row.status),
		ContentText:     row.contentText,
		ContentBlocks:   parseContentBlocks(row.contentBlocksJSON),
		Model:           row.model,
		TraceID:         nullText(row.traceID),
		FinishReason:    nullText(row.finishReason),
		ErrorCode:       nullText(row.errorCode),
		ErrorMessage:    nullText(row.errorMessage),
		CreatedAt:       row.createdAt,
		CompletedAt:     completedAt,
		ExpiresAt:       row.expiresAt,
	}, nil
}

func utf8Len(value string) int { return utf8.RuneCountInString(value) }

func utf8Bytes(value string) int { return len(value) }

// AcceptTurn mirrors acceptChatTurn. All conflict paths and the replacement
// bookkeeping (storage windows, idempotency, asset retention, message delete)
// keep the Node statement sequence.
func (s *Store) AcceptTurn(input AcceptTurnInput) (*AcceptTurnResult, error) {
	now, err := requireRFC3339Instant(input.Now, "聊天轮次 now")
	if err != nil {
		return nil, err
	}
	release := s.lockUserPolicy(input.SystemAccountID)
	defer release()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.lockChatUserStorageQuota(tx, input.SystemAccountID); err != nil {
		return nil, err
	}
	conversation, err := s.lockedConversation(tx, input.ConversationID, input.SystemAccountID)
	if err != nil {
		return nil, err
	}
	existing, err := s.findIdempotencyTx(tx, input.ConversationID, input.SystemAccountID, input.ClientMessageID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		user, assistant, pairErr := s.loadMessagePairTx(tx, input.ConversationID, input.SystemAccountID, existing.TurnID)
		if pairErr != nil {
			return nil, pairErr
		}
		return &AcceptTurnResult{TurnID: existing.TurnID, UserMessage: user, AssistantMessage: assistant, Duplicate: true}, nil
	}

	if !input.ReplaceTurnIDValid() && conversation.userTurnCount >= input.MaxTurnsPerConversation {
		return nil, &ConflictError{Code: ConflictTurnLimitExceeded}
	}
	if err := s.ensurePostgresChatMessagePartitions(tx, now); err != nil {
		return nil, err
	}

	userContentBlocksJSON, err := serializeInputContentMarkers(input.ContentBlocks, input.UserContent)
	if err != nil {
		return nil, err
	}
	userBytes := int64(utf8Bytes(input.UserContent) + utf8Bytes(userContentBlocksJSON))
	userSequence := conversation.nextSequenceNo
	assistantSequence := userSequence + 1
	var replacedUserMessageID, replacedAssistantMessageID string
	if input.ReplaceTurnID != "" {
		if conversation.activeTurnID.Valid {
			return nil, &ConflictError{Code: ConflictReplaceConflict}
		}
		replacement, err := s.requireReplaceableTurn(tx, replaceableTurnQuery{
			Conversation:    conversation,
			ConversationID:  input.ConversationID,
			SystemAccountID: input.SystemAccountID,
			ReplaceTurnID:   input.ReplaceTurnID,
			Now:             now,
		})
		if err != nil {
			return nil, err
		}
		userSequence = replacement.userMessage.sequenceNo
		assistantSequence = replacement.assistantMessage.sequenceNo
		replacedUserMessageID = replacement.userMessage.id
		replacedAssistantMessageID = replacement.assistantMessage.id
		usedBytes, err := s.recentStorageBytes(tx, input.SystemAccountID, now, input.RetentionDays)
		if err != nil {
			return nil, err
		}
		if usedBytes < replacement.totalBytes {
			return nil, &DomainError{Message: "聊天容量窗口数据不一致：最近窗口小于待替换轮次"}
		}
		if usedBytes-replacement.totalBytes+userBytes+AssistantStorageReservationBytes > input.StorageQuotaBytes {
			return nil, &ConflictError{Code: ConflictStorageQuotaExceeded}
		}
		for bucketDate, bytes := range replacement.bytesByBucket {
			if err := s.decrementStorageWindowStrict(tx, input.SystemAccountID, bucketDate, bytes, now); err != nil {
				return nil, err
			}
		}
		deletedIdempotency, err := tx.Exec(s.bind(`DELETE FROM `+s.table("chat_message_idempotency")+`
			WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?`),
			input.ConversationID, input.SystemAccountID, input.ReplaceTurnID)
		if err != nil {
			return nil, err
		}
		if affected, _ := deletedIdempotency.RowsAffected(); affected != 1 {
			return nil, &ConflictError{Code: ConflictReplaceConflict}
		}
		retainedAssetIDs := uniqueStrings(inputImageAssetIDs(input.ContentBlocks))
		retainedFilter := ""
		if len(retainedAssetIDs) > 0 {
			retainedFilter = ` AND id NOT IN (` + placeholders(len(retainedAssetIDs)) + `)`
		}
		args := []any{now, now, now, now, input.SystemAccountID, input.ConversationID, replacedUserMessageID}
		args = append(args, toAnySlice(retainedAssetIDs)...)
		if _, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_assets")+`
			SET expires_at = CASE WHEN expires_at > ? THEN ? ELSE expires_at END,
				cleanup_retry_at = CASE WHEN cleanup_status = 'failed' THEN ? ELSE cleanup_retry_at END,
				updated_at = ?
			WHERE system_account_id = ? AND conversation_id = ? AND message_id = ?
				AND source_kind = 'user_upload'`+retainedFilter), args...); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_assets")+`
			SET turn_id = NULL, message_id = NULL, committed_at = NULL,
				observation_status = 'not_requested', observation_json = NULL,
				observation_revision = observation_revision + 1,
				observation_claim_id = NULL, observation_claimed_at = NULL,
				updated_at = ?
			WHERE system_account_id = ? AND conversation_id = ? AND message_id = ?`),
			now, input.SystemAccountID, input.ConversationID, replacedUserMessageID); err != nil {
			return nil, err
		}
		replacedUserInputAssetIDs, err := s.queryUserInputAssetIDs(tx, input.ConversationID, replacedUserMessageID, now)
		if err != nil {
			return nil, err
		}
		if err := s.removeChatAssetReferencesForMessage(tx, input.SystemAccountID, input.ConversationID, replacedUserMessageID, now); err != nil {
			return nil, err
		}
		if len(replacedUserInputAssetIDs) > 0 {
			notExistsFilter := ` AND NOT EXISTS (
				SELECT 1 FROM ` + s.table("chat_asset_references") + ` AS reference
				WHERE reference.asset_id = asset.id
					AND reference.conversation_id = asset.conversation_id
					AND reference.expires_at > ?)`
			retainedAssistantFilter := ""
			if len(retainedAssetIDs) > 0 {
				retainedAssistantFilter = ` AND asset.id NOT IN (` + placeholders(len(retainedAssetIDs)) + `)`
			}
			args := []any{now, now, now, now, input.SystemAccountID, input.ConversationID}
			args = append(args, toAnySlice(replacedUserInputAssetIDs)...)
			query := `UPDATE ` + s.table("chat_assets") + ` AS asset
				SET expires_at = CASE WHEN expires_at > ? THEN ? ELSE expires_at END,
					cleanup_retry_at = CASE WHEN cleanup_status = 'failed' THEN ? ELSE cleanup_retry_at END,
					updated_at = ?
				WHERE asset.system_account_id = ? AND asset.conversation_id = ?
					AND asset.source_kind = 'assistant_generated'
					AND asset.id IN (` + placeholders(len(replacedUserInputAssetIDs)) + `)
					AND asset.turn_id IS NULL AND asset.message_id IS NULL` + notExistsFilter + retainedAssistantFilter
			args = append(args, now)
			args = append(args, toAnySlice(retainedAssetIDs)...)
			if _, err := tx.Exec(s.bind(query), args...); err != nil {
				return nil, err
			}
		}
		if err := s.removeChatAssetReferencesForMessage(tx, input.SystemAccountID, input.ConversationID, replacedAssistantMessageID, now); err != nil {
			return nil, err
		}
		args2 := []any{now, now, now, now, input.SystemAccountID, input.ConversationID, replacedAssistantMessageID}
		args2 = append(args2, toAnySlice(retainedAssetIDs)...)
		retainedAssistant2 := ""
		if len(retainedAssetIDs) > 0 {
			retainedAssistant2 = ` AND id NOT IN (` + placeholders(len(retainedAssetIDs)) + `)`
		}
		if _, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_assets")+`
			SET expires_at = CASE WHEN expires_at > ? THEN ? ELSE expires_at END,
				cleanup_retry_at = CASE WHEN cleanup_status = 'failed' THEN ? ELSE cleanup_retry_at END,
				updated_at = ?
			WHERE system_account_id = ? AND conversation_id = ? AND message_id = ?
				AND source_kind = 'assistant_generated'
				AND cleanup_status IN ('active', 'failed')`+retainedAssistant2), args2...); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_assets")+`
			SET turn_id = NULL, message_id = NULL, committed_at = NULL,
				observation_status = 'not_requested', observation_json = NULL,
				observation_revision = observation_revision + 1,
				observation_claim_id = NULL, observation_claimed_at = NULL,
				updated_at = ?
			WHERE system_account_id = ? AND conversation_id = ? AND message_id = ?
				AND source_kind = 'assistant_generated'
				AND cleanup_status IN ('active', 'failed')`),
			now, input.SystemAccountID, input.ConversationID, replacedAssistantMessageID); err != nil {
			return nil, err
		}
		deletedMessages, err := tx.Exec(s.bind(`DELETE FROM `+s.table("chat_messages")+`
			WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?`),
			input.ConversationID, input.SystemAccountID, input.ReplaceTurnID)
		if err != nil {
			return nil, err
		}
		if affected, _ := deletedMessages.RowsAffected(); affected != 2 {
			return nil, &ConflictError{Code: ConflictReplaceConflict}
		}
	} else {
		if conversation.activeTurnID.Valid {
			return nil, &ConflictError{Code: ConflictMessageInProgress}
		}
		usedBytes, err := s.recentStorageBytes(tx, input.SystemAccountID, now, input.RetentionDays)
		if err != nil {
			return nil, err
		}
		if usedBytes+userBytes+AssistantStorageReservationBytes > input.StorageQuotaBytes {
			return nil, &ConflictError{Code: ConflictStorageQuotaExceeded}
		}
	}

	turnID := s.newID("turn")
	userMessageID := s.newID("msg")
	assistantMessageID := s.newID("msg")
	expiresAt, err := addDays(now, input.RetentionDays, "聊天消息 expiresAt 基准时间")
	if err != nil {
		return nil, err
	}
	messagesTable := s.table("chat_messages")
	if _, err := tx.Exec(s.bind(`INSERT INTO `+messagesTable+` (
		id, conversation_id, system_account_id, turn_id, sequence_no, client_message_id,
		role, status, content_text, content_blocks_json, content_bytes, model, created_at, completed_at, expires_at
	) VALUES (?, ?, ?, ?, ?, ?, 'user', 'completed', ?, ?, ?, ?, ?, ?, ?)`),
		userMessageID, input.ConversationID, input.SystemAccountID, turnID, userSequence, input.ClientMessageID,
		input.UserContent, userContentBlocksJSON, userBytes, input.Model, now, now, expiresAt); err != nil {
		return nil, err
	}
	if err := s.commitChatAssetsToMessage(tx, inputImageAssetIDs(input.ContentBlocks), input.SystemAccountID,
		input.ConversationID, userMessageID, now, input.RetentionDays); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(s.bind(`INSERT INTO `+messagesTable+` (
		id, conversation_id, system_account_id, turn_id, sequence_no,
		role, status, content_text, content_bytes, storage_reserved_bytes, model, created_at, expires_at
	) VALUES (?, ?, ?, ?, ?, 'assistant', 'streaming', '', 0, ?, ?, ?, ?)`),
		assistantMessageID, input.ConversationID, input.SystemAccountID, turnID, assistantSequence,
		AssistantStorageReservationBytes, input.Model, now, expiresAt); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(s.bind(`INSERT INTO `+s.table("chat_message_idempotency")+` (
		conversation_id, client_message_id, system_account_id, turn_id,
		user_message_id, assistant_message_id, created_at, expires_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		input.ConversationID, input.ClientMessageID, input.SystemAccountID, turnID,
		userMessageID, assistantMessageID, now, expiresAt); err != nil {
		return nil, err
	}
	if err := s.incrementStorageWindow(tx, input.SystemAccountID, now, userBytes, AssistantStorageReservationBytes); err != nil {
		return nil, err
	}
	title := TitleFromContent(input.UserContent)
	if replacedUserMessageID != "" {
		if _, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
			SET title = CASE WHEN title_source_message_id = ? THEN ? ELSE title END,
				title_source_message_id = CASE WHEN title_source_message_id = ? THEN ? ELSE title_source_message_id END,
				message_revision = message_revision + 1,
				context_revision = context_revision + 1,
				context_state = CASE WHEN context_state = 'compacting' THEN 'compact_pending' ELSE context_state END,
				context_claim_id = NULL, context_claim_revision = NULL,
				context_claim_through_sequence = NULL, context_claimed_at = NULL,
				context_retry_at = CASE WHEN context_state = 'compacting' THEN NULL ELSE context_retry_at END,
				context_error_code = CASE WHEN context_state = 'compacting' THEN NULL ELSE context_error_code END,
				context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
				active_turn_id = ?, active_started_at = ?, last_model = ?, last_message_at = ?, updated_at = ?
			WHERE id = ? AND system_account_id = ?`),
			replacedUserMessageID, title, replacedUserMessageID, userMessageID, turnID, now,
			input.Model, now, now, input.ConversationID, input.SystemAccountID); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
			SET title = CASE WHEN next_sequence_no = 1 AND title = '新对话' AND title_source_message_id IS NULL THEN ? ELSE title END,
				title_source_message_id = CASE WHEN next_sequence_no = 1 AND title = '新对话' AND title_source_message_id IS NULL THEN ? ELSE title_source_message_id END,
				message_revision = message_revision + 1,
				context_revision = context_revision + 1,
				context_state = CASE WHEN context_state = 'compacting' THEN 'compact_pending' ELSE context_state END,
				context_claim_id = NULL, context_claim_revision = NULL,
				context_claim_through_sequence = NULL, context_claimed_at = NULL,
				context_retry_at = CASE WHEN context_state = 'compacting' THEN NULL ELSE context_retry_at END,
				context_error_code = CASE WHEN context_state = 'compacting' THEN NULL ELSE context_error_code END,
				context_progress_sequence = 0, context_progress_earliest_expires_at = NULL,
				next_sequence_no = ?, user_turn_count = user_turn_count + 1, active_turn_id = ?, active_started_at = ?,
				last_model = ?, last_message_at = ?, updated_at = ?
			WHERE id = ? AND system_account_id = ?`),
			title, userMessageID, assistantSequence+1, turnID, now, input.Model, now, now,
			input.ConversationID, input.SystemAccountID); err != nil {
			return nil, err
		}
	}
	user, assistant, err := s.loadMessagePairTx(tx, input.ConversationID, input.SystemAccountID, turnID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &AcceptTurnResult{TurnID: turnID, UserMessage: user, AssistantMessage: assistant}, nil
}

// ReplaceTurnIDValid reports whether a replacement turn was requested; kept as
// a method so empty-string semantics stay local to the input type.
func (i AcceptTurnInput) ReplaceTurnIDValid() bool { return i.ReplaceTurnID != "" }

func inputImageAssetIDs(blocks []InputContentBlock) []string {
	ids := []string{}
	for _, block := range blocks {
		if block.Type == "input_image" && block.AssetID != nil && *block.AssetID != "" {
			ids = append(ids, *block.AssetID)
		}
	}
	return ids
}

type idempotencyFact struct {
	TurnID             string
	UserMessageID      string
	AssistantMessageID string
}

func (s *Store) findIdempotencyTx(tx queryer, conversationID, ownerID, clientMessageID string) (*idempotencyFact, error) {
	var fact idempotencyFact
	err := tx.QueryRow(s.bind(`SELECT turn_id, user_message_id, assistant_message_id
		FROM `+s.table("chat_message_idempotency")+`
		WHERE conversation_id = ? AND client_message_id = ? AND system_account_id = ?`),
		conversationID, clientMessageID, ownerID).Scan(&fact.TurnID, &fact.UserMessageID, &fact.AssistantMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &fact, nil
}

// FindTurnByClientMessageID mirrors findChatTurnByClientMessageId.
func (s *Store) FindTurnByClientMessageID(conversationID, ownerID, clientMessageID string) (*TurnSubmissionFact, error) {
	var turnID, assistantMessageID, assistantStatus string
	var errorCode, errorMessage, completedAt, traceID sql.NullString
	err := s.db.QueryRow(s.bind(`SELECT submission.turn_id, assistant.id AS assistant_message_id,
			assistant.status AS assistant_status, assistant.error_code, assistant.error_message,
			assistant.completed_at, assistant.trace_id
		FROM `+s.table("chat_message_idempotency")+` AS submission
		JOIN `+s.table("chat_messages")+` AS assistant
			ON assistant.id = submission.assistant_message_id
			AND assistant.conversation_id = submission.conversation_id
			AND assistant.system_account_id = submission.system_account_id
			AND assistant.turn_id = submission.turn_id
			AND assistant.role = 'assistant'
		WHERE submission.conversation_id = ?
			AND submission.client_message_id = ?
			AND submission.system_account_id = ?`),
		conversationID, clientMessageID, ownerID).Scan(&turnID, &assistantMessageID, &assistantStatus,
		&errorCode, &errorMessage, &completedAt, &traceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fact := &TurnSubmissionFact{
		TurnID:             turnID,
		AssistantMessageID: assistantMessageID,
		AssistantStatus:    ChatMessageStatus(assistantStatus),
		ErrorCode:          nullText(errorCode),
		ErrorMessage:       nullText(errorMessage),
		TraceID:            nullText(traceID),
	}
	if completedAt.Valid {
		normalized, err := requireRFC3339Instant(completedAt.String, "聊天消息 completed_at")
		if err != nil {
			return nil, err
		}
		fact.CompletedAt = &normalized
	}
	return fact, nil
}

func (s *Store) loadMessagePairTx(tx queryer, conversationID, ownerID, turnID string) (*Message, *Message, error) {
	rows, err := tx.Query(s.bind(`SELECT `+messageColumns+` FROM `+s.table("chat_messages")+`
		WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
		ORDER BY sequence_no ASC`), conversationID, ownerID, turnID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	messages := []messageRow{}
	for rows.Next() {
		row, err := scanMessageRow(rows.Scan)
		if err != nil {
			return nil, nil, err
		}
		messages = append(messages, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(messages) != 2 {
		return nil, nil, &DomainError{Message: "聊天轮次数据不完整"}
	}
	user, err := mapMessage(messages[0])
	if err != nil {
		return nil, nil, err
	}
	assistant, err := mapMessage(messages[1])
	if err != nil {
		return nil, nil, err
	}
	return user, assistant, nil
}

type replaceableTurnQuery struct {
	Conversation    conversationRow
	ConversationID  string
	SystemAccountID string
	ReplaceTurnID   string
	Now             string
}

type replaceableTurn struct {
	userMessage      messageRow
	assistantMessage messageRow
	bytesByBucket    map[string]int64
	totalBytes       int64
}

// requireReplaceableTurn mirrors requireReplaceableTurn.
func (s *Store) requireReplaceableTurn(tx queryer, input replaceableTurnQuery) (*replaceableTurn, error) {
	rows, err := tx.Query(s.bind(`SELECT `+messageColumns+` FROM `+s.table("chat_messages")+`
		WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
		ORDER BY sequence_no ASC`), input.ConversationID, input.SystemAccountID, input.ReplaceTurnID)
	if err != nil {
		return nil, err
	}
	turnRows := []messageRow{}
	for rows.Next() {
		row, err := scanMessageRow(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		turnRows = append(turnRows, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(turnRows) != 2 {
		return nil, &ConflictError{Code: ConflictReplaceConflict}
	}
	userMessage, assistantMessage := turnRows[0], turnRows[1]
	userSequence, assistantSequence := userMessage.sequenceNo, assistantMessage.sequenceNo
	if userMessage.role != "user" || assistantMessage.role != "assistant" ||
		userMessage.status != "completed" ||
		!containsString([]string{"completed", "failed", "canceled"}, assistantMessage.status) ||
		assistantSequence != userSequence+1 {
		return nil, &ConflictError{Code: ConflictReplaceConflict}
	}
	userExpiresAt, err := requireRFC3339Instant(userMessage.expiresAt, "聊天用户消息 expires_at")
	if err != nil {
		return nil, err
	}
	assistantExpiresAt, err := requireRFC3339Instant(assistantMessage.expiresAt, "聊天助手消息 expires_at")
	if err != nil {
		return nil, err
	}
	if userExpiresAt <= input.Now || assistantExpiresAt <= input.Now {
		return nil, &ConflictError{Code: ConflictReplaceConflict}
	}
	var maxSequenceNo sql.NullInt64
	if err := tx.QueryRow(s.bind(`SELECT MAX(sequence_no) AS max_sequence_no
		FROM `+s.table("chat_messages")+`
		WHERE conversation_id = ? AND system_account_id = ?`),
		input.ConversationID, input.SystemAccountID).Scan(&maxSequenceNo); err != nil {
		return nil, err
	}
	if !maxSequenceNo.Valid || maxSequenceNo.Int64 != assistantSequence {
		return nil, &ConflictError{Code: ConflictReplaceConflict}
	}
	markers, ok := parseStoredInputMarkers(userMessage.contentBlocksJSON)
	if !ok {
		return nil, &ConflictError{Code: ConflictReplaceConflict}
	}
	if len(markers) == 0 {
		return nil, &ConflictError{Code: ConflictReplaceConflict}
	}
	idempotencyRows, err := s.queryIdempotencyForTurn(tx, input.ConversationID, input.SystemAccountID, input.ReplaceTurnID)
	if err != nil {
		return nil, err
	}
	if len(idempotencyRows) != 1 {
		return nil, &ConflictError{Code: ConflictReplaceConflict}
	}
	idempotency := idempotencyRows[0]
	if idempotency.UserMessageID != userMessage.id ||
		idempotency.AssistantMessageID != assistantMessage.id ||
		nullText(userMessage.clientMessageID) == nil ||
		idempotency.ClientMessageID != *nullText(userMessage.clientMessageID) ||
		idempotency.SystemAccountID != input.SystemAccountID ||
		input.Conversation.titleSourceMessageID.Valid && input.Conversation.titleSourceMessageID.String == assistantMessage.id {
		return nil, &ConflictError{Code: ConflictReplaceConflict}
	}
	bytesByBucket := map[string]int64{}
	var totalBytes int64
	for _, row := range turnRows {
		bytes := row.contentBytes
		createdAt, err := requireRFC3339Instant(row.createdAt, "聊天消息 created_at")
		if err != nil {
			return nil, err
		}
		bucketDate := createdAt[:10]
		if bytes < 0 || !validBucketDate(bucketDate) {
			return nil, &ConflictError{Code: ConflictReplaceConflict}
		}
		bytesByBucket[bucketDate] += bytes
		totalBytes += bytes
	}
	return &replaceableTurn{
		userMessage:      userMessage,
		assistantMessage: assistantMessage,
		bytesByBucket:    bytesByBucket,
		totalBytes:       totalBytes,
	}, nil
}

type turnIdempotencyRow struct {
	TurnID             string
	ClientMessageID    string
	SystemAccountID    string
	UserMessageID      string
	AssistantMessageID string
}

func (s *Store) queryIdempotencyForTurn(tx queryer, conversationID, ownerID, turnID string) ([]turnIdempotencyRow, error) {
	rows, err := tx.Query(s.bind(`SELECT conversation_id, client_message_id, system_account_id, turn_id,
			user_message_id, assistant_message_id
		FROM `+s.table("chat_message_idempotency")+`
		WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?`),
		conversationID, ownerID, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []turnIdempotencyRow{}
	for rows.Next() {
		var conversationIDValue, clientMessageID, systemAccountID, turnIDValue, userMessageID, assistantMessageID string
		if err := rows.Scan(&conversationIDValue, &clientMessageID, &systemAccountID, &turnIDValue, &userMessageID, &assistantMessageID); err != nil {
			return nil, err
		}
		out = append(out, turnIdempotencyRow{
			TurnID: turnIDValue, ClientMessageID: clientMessageID, SystemAccountID: systemAccountID,
			UserMessageID: userMessageID, AssistantMessageID: assistantMessageID,
		})
	}
	return out, rows.Err()
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func validBucketDate(value string) bool {
	if len(value) != 10 || value[4] != '-' || value[7] != '-' {
		return false
	}
	for i, ch := range value {
		if i == 4 || i == 7 {
			continue
		}
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// AssertTurnReplaceable mirrors assertChatTurnReplaceable.
func (s *Store) AssertTurnReplaceable(input AssertReplaceableInput) error {
	now, err := requireRFC3339Instant(input.Now, "聊天轮次替换 now")
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	conversation, err := s.lockedConversation(tx, input.ConversationID, input.SystemAccountID)
	if err != nil {
		return err
	}
	if conversation.activeTurnID.Valid {
		return &ConflictError{Code: ConflictReplaceConflict}
	}
	if _, err := s.requireReplaceableTurn(tx, replaceableTurnQuery{
		Conversation:    conversation,
		ConversationID:  input.ConversationID,
		SystemAccountID: input.SystemAccountID,
		ReplaceTurnID:   input.ReplaceTurnID,
		Now:             now,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

type AssertReplaceableInput struct {
	ConversationID  string
	SystemAccountID string
	ReplaceTurnID   string
	Now             string
}

type finalizeTurnInput struct {
	ConversationID   string
	SystemAccountID  string
	TurnID           string
	AssistantContent string
	Status           ChatMessageStatus
	FinishReason     *string
	ErrorCode        *string
	ErrorMessage     *string
	TraceID          *string
	ContentBlocks    []ContentBlock
	ContentBlocksRaw json.RawMessage
	Now              string
}

// CompleteChatTurn mirrors completeChatTurn.
func (s *Store) CompleteChatTurn(input CompleteTurnInput) (*Message, error) {
	return s.finalizeTurn(finalizeTurnInput{
		ConversationID:   input.ConversationID,
		SystemAccountID:  input.SystemAccountID,
		TurnID:           input.TurnID,
		AssistantContent: input.AssistantContent,
		Status:           StatusCompleted,
		FinishReason:     stringPtr(input.FinishReason),
		TraceID:          stringPtr(input.TraceID),
		ContentBlocks:    input.ContentBlocks,
		ContentBlocksRaw: input.ContentBlocksRaw,
		Now:              input.Now,
	})
}

// CompleteTurnInput mirrors the completeChatTurn input object.
type CompleteTurnInput struct {
	ConversationID   string
	SystemAccountID  string
	TurnID           string
	AssistantContent string
	FinishReason     string
	TraceID          string
	ContentBlocks    []ContentBlock
	ContentBlocksRaw json.RawMessage
	Now              string
}

// FailChatTurn mirrors failChatTurn.
func (s *Store) FailChatTurn(input FailTurnInput) (*Message, error) {
	errorCode := input.ErrorCode
	errorMessage := input.ErrorMessage
	return s.finalizeTurn(finalizeTurnInput{
		ConversationID:   input.ConversationID,
		SystemAccountID:  input.SystemAccountID,
		TurnID:           input.TurnID,
		AssistantContent: input.AssistantContent,
		Status:           StatusFailed,
		ErrorCode:        &errorCode,
		ErrorMessage:     &errorMessage,
		TraceID:          input.TraceID,
		ContentBlocks:    input.ContentBlocks,
		Now:              input.Now,
	})
}

// FailTurnInput mirrors the failChatTurn input object.
type FailTurnInput struct {
	ConversationID   string
	SystemAccountID  string
	TurnID           string
	AssistantContent string
	ErrorCode        string
	ErrorMessage     string
	TraceID          *string
	ContentBlocks    []ContentBlock
	ContentBlocksRaw json.RawMessage
	Now              string
}

// CancelChatTurn mirrors cancelChatTurn.
func (s *Store) CancelChatTurn(input CancelTurnInput) (*Message, error) {
	return s.finalizeTurn(finalizeTurnInput{
		ConversationID:   input.ConversationID,
		SystemAccountID:  input.SystemAccountID,
		TurnID:           input.TurnID,
		AssistantContent: input.AssistantContent,
		Status:           StatusCanceled,
		TraceID:          input.TraceID,
		ContentBlocks:    input.ContentBlocks,
		Now:              input.Now,
	})
}

// CancelTurnInput mirrors the cancelChatTurn input object.
type CancelTurnInput struct {
	ConversationID   string
	SystemAccountID  string
	TurnID           string
	AssistantContent string
	TraceID          *string
	ContentBlocks    []ContentBlock
	ContentBlocksRaw json.RawMessage
	Now              string
}

// finalizeTurn mirrors finalizeChatTurn including the storage-limit downgrade
// to failed with chat_assistant_storage_limit_exceeded.
func (s *Store) finalizeTurn(input finalizeTurnInput) (*Message, error) {
	now, err := requireRFC3339Instant(input.Now, "聊天回答终结 now")
	if err != nil {
		return nil, err
	}
	requestedContentBlocksJSON := "[]"
	requestedBytes := int64(AssistantStorageReservationBytes + 1)
	serializationExceeded := false
	// ContentBlocksRaw carries the runner timeline JSON (Node passes the
	// timeline blocks verbatim); the typed fallback keeps older callers.
	if len(input.ContentBlocksRaw) > 0 {
		if len(input.ContentBlocksRaw) <= maxContentBlocksBytes {
			requestedContentBlocksJSON = string(input.ContentBlocksRaw)
			requestedBytes = int64(utf8Bytes(input.AssistantContent) + utf8Bytes(requestedContentBlocksJSON))
		} else {
			serializationExceeded = true
		}
	} else {
		// Node passes `contentBlocks ?? []` so nil marshals as [] rather than null.
		requestedBlocks := input.ContentBlocks
		if requestedBlocks == nil {
			requestedBlocks = []ContentBlock{}
		}
		if serialized, err := serializeContentBlocks(requestedBlocks); err == nil {
			requestedContentBlocksJSON = serialized
			requestedBytes = int64(utf8Bytes(input.AssistantContent) + utf8Bytes(requestedContentBlocksJSON))
		} else {
			serializationExceeded = true
		}
	}
	release := s.lockUserPolicy(input.SystemAccountID)
	defer release()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	conversation, err := s.lockedConversation(tx, input.ConversationID, input.SystemAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &DomainError{Message: "会话不存在"}
	}
	if err != nil {
		return nil, err
	}
	if nullText(conversation.activeTurnID) == nil || *nullText(conversation.activeTurnID) != input.TurnID {
		return nil, &DomainError{Message: "活动回答不存在"}
	}
	current, err := s.lockedStreamingAssistant(tx, input.ConversationID, input.SystemAccountID, input.TurnID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &DomainError{Message: "活动回答不存在"}
	}
	if err != nil {
		return nil, err
	}
	reservationBytes, err := requiredAssistantStorageReservation(current.storageReservedBytes)
	if err != nil {
		return nil, err
	}
	storageLimitExceeded := serializationExceeded || requestedBytes > reservationBytes
	contentText := input.AssistantContent
	contentBlocksJSON := requestedContentBlocksJSON
	bytes := requestedBytes
	status := input.Status
	finishReason := input.FinishReason
	errorCode := input.ErrorCode
	errorMessage := input.ErrorMessage
	if storageLimitExceeded {
		contentText = ""
		contentBlocksJSON = "[]"
		bytes = int64(utf8Bytes("[]"))
		status = StatusFailed
		finishReason = nil
		limitCode := "chat_assistant_storage_limit_exceeded"
		limitMessage := "AI 回答超过聊天存储上限"
		errorCode = &limitCode
		errorMessage = &limitMessage
	}
	result, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_messages")+`
		SET status = ?, content_text = ?, content_blocks_json = ?, content_bytes = ?, trace_id = ?,
			storage_reserved_bytes = 0, finish_reason = ?, error_code = ?, error_message = ?, completed_at = ?
		WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
			AND role = 'assistant' AND status = 'streaming'`),
		status, contentText, contentBlocksJSON, bytes, sqlText(input.TraceID),
		sqlText(finishReason), sqlText(errorCode), sqlText(errorMessage), now,
		input.ConversationID, input.SystemAccountID, input.TurnID)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &DomainError{Message: "活动回答不存在"}
	}
	if err := s.settleStorageWindowReservationStrict(tx, input.SystemAccountID, current.createdAt, reservationBytes, bytes, now); err != nil {
		return nil, err
	}
	conversationResult, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
		SET active_turn_id = NULL, active_started_at = NULL,
			message_revision = message_revision + 1,
			last_message_at = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND active_turn_id = ?`),
		now, now, input.ConversationID, input.SystemAccountID, input.TurnID)
	if err != nil {
		return nil, err
	}
	if affected, _ := conversationResult.RowsAffected(); affected != 1 {
		return nil, &DomainError{Message: "活动轮次状态更新失败"}
	}
	_, assistant, err := s.loadMessagePairTx(tx, input.ConversationID, input.SystemAccountID, input.TurnID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if storageLimitExceeded {
		return nil, &AssistantStorageLimitError{}
	}
	return assistant, nil
}

func (s *Store) lockedStreamingAssistant(tx queryer, conversationID, ownerID, turnID string) (messageRow, error) {
	return scanMessageRow(func(targets ...any) error {
		return tx.QueryRow(s.bind(`SELECT `+messageColumns+` FROM `+s.table("chat_messages")+`
			WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
				AND role = 'assistant' AND status = 'streaming'`+s.lockSuffix()),
			conversationID, ownerID, turnID).Scan(targets...)
	})
}

// CancelActiveTurnState mirrors CancelActiveChatTurnResult states.
type CancelActiveTurnState string

const (
	CancelStateCanceled        CancelActiveTurnState = "canceled"
	CancelStateAlreadyTerminal CancelActiveTurnState = "already_terminal"
	CancelStateTurnMismatch    CancelActiveTurnState = "turn_mismatch"
	CancelStateNotFound        CancelActiveTurnState = "not_found"
)

// CancelActiveTurnResult mirrors CancelActiveChatTurnResult.
type CancelActiveTurnResult struct {
	State           CancelActiveTurnState
	AssistantStatus ChatMessageStatus
}

// CancelActiveTurnIfMatches mirrors cancelActiveChatTurnIfMatches.
func (s *Store) CancelActiveTurnIfMatches(input CancelIfMatchesInput) (*CancelActiveTurnResult, error) {
	return s.conditionalStop(input, "canceled")
}

// FailInterruptedTurnIfMatches mirrors failInterruptedChatTurnIfMatches.
func (s *Store) FailInterruptedTurnIfMatches(input CancelIfMatchesInput) (*CancelActiveTurnResult, error) {
	return s.conditionalStop(input, "interrupted")
}

type CancelIfMatchesInput struct {
	ConversationID  string
	SystemAccountID string
	ExpectedTurnID  string
	Now             string
}

func (s *Store) conditionalStop(input CancelIfMatchesInput, mode string) (*CancelActiveTurnResult, error) {
	nowLabel := "聊天轮次取消 now"
	if mode == "interrupted" {
		nowLabel = "聊天轮次中断 now"
	}
	now, err := requireRFC3339Instant(input.Now, nowLabel)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	conversation, err := s.lockedConversation(tx, input.ConversationID, input.SystemAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return &CancelActiveTurnResult{State: CancelStateNotFound}, nil
	}
	if err != nil {
		return nil, err
	}
	assistant, err := s.lockedTurnAssistant(tx, input.ConversationID, input.SystemAccountID, input.ExpectedTurnID)
	if err != nil {
		return nil, err
	}
	initialState := classifyConditionalStopState(conversation, assistant, input.ExpectedTurnID)
	if initialState != nil {
		return initialState, nil
	}
	reservationBytes, err := requiredAssistantStorageReservation(assistant.storageReservedBytes)
	if err != nil {
		return nil, err
	}
	status := StatusCanceled
	errorCode := sql.NullString{}
	errorMessage := sql.NullString{}
	if mode == "interrupted" {
		status = StatusFailed
		errorCode = sql.NullString{String: "stream_interrupted", Valid: true}
		errorMessage = sql.NullString{String: "生成进程异常中断，未取得原始异常详情", Valid: true}
	}
	messageResult, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_messages")+`
		SET status = ?, storage_reserved_bytes = 0,
			finish_reason = NULL, error_code = ?, error_message = ?, completed_at = ?
		WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?
			AND role = 'assistant' AND status = 'streaming'`),
		status, errorCode, errorMessage, now,
		input.ConversationID, input.SystemAccountID, input.ExpectedTurnID)
	if err != nil {
		return nil, err
	}
	if affected, _ := messageResult.RowsAffected(); affected != 1 {
		authoritative, readErr := s.readConditionalStopState(tx, input)
		if readErr != nil {
			return nil, readErr
		}
		if authoritative != nil {
			return authoritative, nil
		}
		return &CancelActiveTurnResult{State: CancelStateTurnMismatch}, nil
	}
	if err := s.releaseStorageWindowReservationStrict(tx, input.SystemAccountID, assistant.createdAt, reservationBytes, now); err != nil {
		return nil, err
	}
	conversationResult, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
		SET active_turn_id = NULL, active_started_at = NULL,
			message_revision = message_revision + 1, last_message_at = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ? AND active_turn_id = ?`),
		now, now, input.ConversationID, input.SystemAccountID, input.ExpectedTurnID)
	if err != nil {
		return nil, err
	}
	if affected, _ := conversationResult.RowsAffected(); affected != 1 {
		if mode == "interrupted" {
			return nil, &DomainError{Message: "活动轮次中断收口失败"}
		}
		authoritative, readErr := s.readConditionalStopState(tx, input)
		if readErr != nil {
			return nil, readErr
		}
		if authoritative != nil && !(authoritative.State == CancelStateAlreadyTerminal && authoritative.AssistantStatus == StatusCanceled) {
			return authoritative, nil
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if mode == "interrupted" {
		return &CancelActiveTurnResult{State: CancelStateAlreadyTerminal, AssistantStatus: StatusFailed}, nil
	}
	return &CancelActiveTurnResult{State: CancelStateCanceled, AssistantStatus: StatusCanceled}, nil
}

func (s *Store) lockedTurnAssistant(tx queryer, conversationID, ownerID, turnID string) (*messageRow, error) {
	row, err := scanMessageRow(func(targets ...any) error {
		return tx.QueryRow(s.bind(`SELECT `+messageColumns+` FROM `+s.table("chat_messages")+`
			WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ? AND role = 'assistant'`+s.lockSuffix()),
			conversationID, ownerID, turnID).Scan(targets...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) readConditionalStopState(tx queryer, input CancelIfMatchesInput) (*CancelActiveTurnResult, error) {
	conversation, err := s.lockedConversation(tx, input.ConversationID, input.SystemAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return &CancelActiveTurnResult{State: CancelStateNotFound}, nil
	}
	if err != nil {
		return nil, err
	}
	assistant, err := s.lockedTurnAssistant(tx, input.ConversationID, input.SystemAccountID, input.ExpectedTurnID)
	if err != nil {
		return nil, err
	}
	return classifyConditionalStopState(conversation, assistant, input.ExpectedTurnID), nil
}

func classifyConditionalStopState(conversation conversationRow, assistant *messageRow, expectedTurnID string) *CancelActiveTurnResult {
	if assistant == nil {
		if conversation.activeTurnID.Valid && conversation.activeTurnID.String != expectedTurnID {
			return &CancelActiveTurnResult{State: CancelStateTurnMismatch}
		}
		return &CancelActiveTurnResult{State: CancelStateNotFound}
	}
	assistantStatus := ChatMessageStatus(assistant.status)
	if assistantStatus != StatusStreaming {
		return &CancelActiveTurnResult{State: CancelStateAlreadyTerminal, AssistantStatus: assistantStatus}
	}
	if !conversation.activeTurnID.Valid || conversation.activeTurnID.String != expectedTurnID {
		return &CancelActiveTurnResult{State: CancelStateTurnMismatch}
	}
	return nil
}

// ListMessages mirrors listChatMessages: single-cursor pagination with
// before (DESC) / after,from (ASC) semantics, expires_at guard and the
// 100-limit clamp. Node reverses DESC rows to ASC on read.
func (s *Store) ListMessages(input ListMessagesInput) ([]*Message, error) {
	now, err := requireRFC3339Instant(input.Now, "聊天消息列表 now")
	if err != nil {
		return nil, err
	}
	cursorCount := 0
	for _, value := range []*int64{input.BeforeSequenceNo, input.AfterSequenceNo, input.FromSequenceNo} {
		if value != nil {
			cursorCount++
		}
	}
	if cursorCount > 1 {
		return nil, &DomainError{Message: "消息游标只能指定一个"}
	}
	if _, err := s.requireConversation(input.ConversationID, input.SystemAccountID); err != nil {
		return nil, err
	}
	var cursor *int64
	for _, value := range []*int64{input.BeforeSequenceNo, input.AfterSequenceNo, input.FromSequenceNo} {
		if value != nil {
			cursor = value
			break
		}
	}
	cursorCondition := ""
	switch {
	case input.BeforeSequenceNo != nil:
		cursorCondition = "AND sequence_no < ?"
	case input.AfterSequenceNo != nil:
		cursorCondition = "AND sequence_no > ?"
	case input.FromSequenceNo != nil:
		cursorCondition = "AND sequence_no >= ?"
	}
	ascending := input.AfterSequenceNo != nil || input.FromSequenceNo != nil
	limit := clampInt(input.Limit, 1, 100)
	query := s.bind(`SELECT ` + messageColumns + ` FROM ` + s.table("chat_messages") + `
		WHERE conversation_id = ? AND system_account_id = ? AND expires_at > ?
		` + cursorCondition + `
		ORDER BY sequence_no ` + ascDesc(ascending) + `
		LIMIT ?`)
	var rows []messageRow
	var queryErr error
	if cursor != nil {
		rows, queryErr = s.queryMessageRows(query, input.ConversationID, input.SystemAccountID, now, *cursor, limit)
	} else {
		rows, queryErr = s.queryMessageRows(query, input.ConversationID, input.SystemAccountID, now, limit)
	}
	if queryErr != nil {
		return nil, queryErr
	}
	if !ascending {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	out := make([]*Message, 0, len(rows))
	for _, row := range rows {
		message, err := mapMessage(row)
		if err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	return out, nil
}

type ListMessagesInput struct {
	ConversationID   string
	SystemAccountID  string
	BeforeSequenceNo *int64
	AfterSequenceNo  *int64
	FromSequenceNo   *int64
	Limit            int
	Now              string
}

func ascDesc(ascending bool) string {
	if ascending {
		return "ASC"
	}
	return "DESC"
}

func (s *Store) queryMessageRows(query string, args ...any) ([]messageRow, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []messageRow{}
	for rows.Next() {
		row, err := scanMessageRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) requireConversation(conversationID, ownerID string) (*Conversation, error) {
	row, err := scanConversationRow(func(targets ...any) error {
		return s.db.QueryRow(s.bind(`SELECT `+conversationColumns+` FROM `+s.table("chat_conversations")+`
			WHERE id = ? AND system_account_id = ?`), conversationID, ownerID).Scan(targets...)
	})
	if err != nil {
		return nil, &DomainError{Message: "会话不存在"}
	}
	return mapConversation(row)
}

// syncHeadMessage mirrors ChatConversationSyncMessage.
type syncHeadMessage struct {
	ID          string
	TurnID      string
	SequenceNo  int64
	Role        ChatMessageRole
	Status      ChatMessageStatus
	CompletedAt *string
	ExpiresAt   string
}

// ConversationSyncHead mirrors ChatConversationSyncHead.
type ConversationSyncHead struct {
	ConversationID           string
	MessageRevision          int64
	LastSequenceNo           int64
	ActiveTurnID             *string
	ActiveAssistantMessageID *string
	ActiveStartedAt          *string
	Tail                     []syncHeadMessage
}

type syncRow struct {
	conversationID           string
	messageRevision          int64
	lastSequenceNo           int64
	activeAssistantMessageID sql.NullString
	activeTurnID             sql.NullString
	activeStartedAt          sql.NullString
	tailID                   sql.NullString
	tailTurnID               sql.NullString
	tailSequenceNo           sql.NullInt64
	tailRole                 sql.NullString
	tailStatus               sql.NullString
	tailCompletedAt          sql.NullString
	tailExpiresAt            sql.NullString
}

// GetConversationSyncHead mirrors getChatConversationSyncHead (CTE port).
func (s *Store) GetConversationSyncHead(conversationID, ownerID, nowValue string) (*ConversationSyncHead, error) {
	now, err := requireRFC3339Instant(nowValue, "聊天会话同步 now")
	if err != nil {
		return nil, err
	}
	query := s.bind(`WITH owned_conversation AS (
			SELECT id, message_revision, active_turn_id, active_started_at
			FROM ` + s.table("chat_conversations") + `
			WHERE id = ? AND system_account_id = ?
		), candidate_messages AS (
			SELECT message.id, message.turn_id, message.sequence_no, message.role,
				message.status, message.completed_at, message.expires_at
			FROM ` + s.table("chat_messages") + ` AS message
			JOIN owned_conversation AS conversation ON conversation.id = message.conversation_id
			WHERE message.system_account_id = ? AND message.expires_at > ?
			ORDER BY message.sequence_no DESC
			LIMIT 16
		), complete_turns AS (
			SELECT turn_id, MAX(sequence_no) AS latest_sequence_no
			FROM candidate_messages
			GROUP BY turn_id
			HAVING COUNT(*) = 2
				AND SUM(CASE WHEN role = 'user' THEN 1 ELSE 0 END) = 1
				AND SUM(CASE WHEN role = 'assistant' THEN 1 ELSE 0 END) = 1
				AND MAX(sequence_no) = MIN(sequence_no) + 1
		), tail_turn AS (
			SELECT turn_id
			FROM complete_turns
			ORDER BY latest_sequence_no DESC
			LIMIT 1
		), tail_messages AS (
			SELECT message.id, message.turn_id, message.sequence_no, message.role,
				message.status, message.completed_at, message.expires_at
			FROM candidate_messages AS message
			JOIN tail_turn ON tail_turn.turn_id = message.turn_id
		), active_assistant AS (
			SELECT message.id, message.turn_id
			FROM ` + s.table("chat_messages") + ` AS message
			JOIN owned_conversation AS conversation
				ON conversation.id = message.conversation_id
				AND conversation.active_turn_id = message.turn_id
			WHERE message.system_account_id = ? AND message.expires_at > ?
				AND message.role = 'assistant' AND message.status = 'streaming'
			LIMIT 1
		)
		SELECT conversation.id AS conversation_id,
			conversation.message_revision,
			COALESCE((
				SELECT message.sequence_no
				FROM ` + s.table("chat_messages") + ` AS message
				WHERE message.conversation_id = conversation.id
					AND message.system_account_id = ? AND message.expires_at > ?
				ORDER BY message.sequence_no DESC
				LIMIT 1
			), 0) AS last_sequence_no,
			active.id AS active_assistant_message_id,
			active.turn_id AS active_turn_id,
			conversation.active_started_at,
			tail.id AS tail_id,
			tail.turn_id AS tail_turn_id,
			tail.sequence_no AS tail_sequence_no,
			tail.role AS tail_role,
			tail.status AS tail_status,
			tail.completed_at AS tail_completed_at,
			tail.expires_at AS tail_expires_at
		FROM owned_conversation AS conversation
		LEFT JOIN active_assistant AS active ON 1 = 1
		LEFT JOIN tail_messages AS tail ON 1 = 1
		ORDER BY tail.sequence_no ASC`)
	rows, err := s.db.Query(query, conversationID, ownerID, ownerID, now, ownerID, now, ownerID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parsed := []syncRow{}
	for rows.Next() {
		var row syncRow
		if err := rows.Scan(&row.conversationID, &row.messageRevision, &row.lastSequenceNo,
			&row.activeAssistantMessageID, &row.activeTurnID, &row.activeStartedAt,
			&row.tailID, &row.tailTurnID, &row.tailSequenceNo, &row.tailRole, &row.tailStatus,
			&row.tailCompletedAt, &row.tailExpiresAt); err != nil {
			return nil, err
		}
		parsed = append(parsed, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(parsed) == 0 {
		return nil, nil
	}
	first := parsed[0]
	activeTurnID := nullText(first.activeTurnID)
	activeAssistantMessageID := nullText(first.activeAssistantMessageID)
	var activeStartedAt *string
	if first.activeStartedAt.Valid {
		normalized, err := requireRFC3339Instant(first.activeStartedAt.String, "聊天会话 active_started_at")
		if err != nil {
			return nil, err
		}
		activeStartedAt = &normalized
	}
	tail := []syncHeadMessage{}
	for _, row := range parsed {
		id := nullText(row.tailID)
		turnID := nullText(row.tailTurnID)
		if id == nil || turnID == nil {
			if row.tailExpiresAt.Valid {
				if _, err := requireRFC3339Instant(row.tailExpiresAt.String, "聊天消息 expires_at"); err != nil {
					return nil, err
				}
			}
			continue
		}
		expiresAt, err := requireRFC3339Instant(row.tailExpiresAt.String, "聊天消息 expires_at")
		if err != nil {
			return nil, err
		}
		var completedAt *string
		if row.tailCompletedAt.Valid {
			normalized, err := requireRFC3339Instant(row.tailCompletedAt.String, "聊天消息 completed_at")
			if err != nil {
				return nil, err
			}
			completedAt = &normalized
		}
		tail = append(tail, syncHeadMessage{
			ID:          *id,
			TurnID:      *turnID,
			SequenceNo:  row.tailSequenceNo.Int64,
			Role:        ChatMessageRole(row.tailRole.String),
			Status:      ChatMessageStatus(row.tailStatus.String),
			CompletedAt: completedAt,
			ExpiresAt:   expiresAt,
		})
	}
	return &ConversationSyncHead{
		ConversationID:           first.conversationID,
		MessageRevision:          first.messageRevision,
		LastSequenceNo:           first.lastSequenceNo,
		ActiveTurnID:             activeTurnID,
		ActiveAssistantMessageID: activeAssistantMessageID,
		ActiveStartedAt:          activeStartedAt,
		Tail:                     tail,
	}, nil
}

// truncateUTF8 mirrors boundedUtf8: cut at maxBytes without splitting a rune.
func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 {
		if r, size := utf8.DecodeLastRuneInString(value[:end]); r != utf8.RuneError || size != 1 {
			return value[:end]
		}
		end--
	}
	return ""
}
