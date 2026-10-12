package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Domain types mirror the Node interfaces in chat.repository.ts. JSON field
// order matches the TypeScript declaration order so serialized responses keep
// the same shape.

// ChatMessageRole mirrors ChatMessageRole.
type ChatMessageRole string

const (
	RoleUser      ChatMessageRole = "user"
	RoleAssistant ChatMessageRole = "assistant"
)

// ChatMessageStatus mirrors ChatMessageStatus.
type ChatMessageStatus string

const (
	StatusCompleted ChatMessageStatus = "completed"
	StatusStreaming ChatMessageStatus = "streaming"
	StatusFailed    ChatMessageStatus = "failed"
	StatusCanceled  ChatMessageStatus = "canceled"
)

// Conversation mirrors ChatConversation (route response shape). 会话绑定收敛
// 为仅 account（AI 问答会话账户唯一绑定设计）：bindAccountId 为空即「未选账户」
// 状态；archived=1 是存量旧模式（api_key/group）会话的一次性迁移只读标记。
// 八个工具绑定列（search_account_id/search_model_id/image_account_id/
// default_image_model/video_account_id/default_video_model/audio_account_id/
// default_audio_model）已随站内工具体系退役（PLAN-20261012T121500000Z D2/D7），
// 结构与读写不再投影。lastModel 语义为「会话锁定模型」（D5）：首轮接受写入，
// 此后不可变；空值 = 未锁定。
type Conversation struct {
	ID                      string  `json:"id"`
	SystemAccountID         string  `json:"systemAccountId"`
	APIKeyID                *string `json:"apiKeyId,omitempty"`
	APIKeyNameSnapshot      string  `json:"apiKeyNameSnapshot"`
	BindAccountID           *string `json:"bindAccountId,omitempty"`
	BindAccountNameSnapshot string  `json:"bindAccountName,omitempty"`
	Archived                bool    `json:"archived"`
	Title                   string  `json:"title"`
	IsPinned                bool    `json:"isPinned"`
	LastModel               *string `json:"lastModel,omitempty"`
	ActiveTurnID            *string `json:"activeTurnId,omitempty"`
	UserTurnCount           int64   `json:"userTurnCount"`
	MessageRevision         int64   `json:"messageRevision"`
	LastMessageAt           string  `json:"lastMessageAt"`
	CreatedAt               string  `json:"createdAt"`
	UpdatedAt               string  `json:"updatedAt"`
}

// ContentBlock is the union of ChatMessageContentBlock variants. Stored and
// serialized as the tagged JSON objects Node produces. M7 问答音视频工具
//（问答音视频工具设计 §3，2026-10-04）增两块形：output_audio（assetId +
// mimeType，与 output_image 同族无尺寸）与 output_media_task（异步视频任务块：
// jobId/kind/status/progress/model/promptSummary/assetId?/error?，status 词表为
// media_jobs 任务状态 queued|in_progress|completed|failed）。
type ContentBlock struct {
	Type     string         `json:"type"`
	BlockID  string         `json:"blockId,omitempty"`
	Order    *int64         `json:"order,omitempty"`
	Text     *string        `json:"text,omitempty"`
	AssetID  *string        `json:"assetId,omitempty"`
	ID       *string        `json:"id,omitempty"`
	CallID   *string        `json:"callId,omitempty"`
	ToolType *string        `json:"toolType,omitempty"`
	Status   *string        `json:"status,omitempty"`
	Item     map[string]any `json:"item,omitempty"`
	MimeType *string        `json:"mimeType,omitempty"`
	Width    *int64         `json:"width,omitempty"`
	Height   *int64         `json:"height,omitempty"`
	// RevisedPrompt mirrors AssistantContentBlock.revisedPrompt (the
	// provider-rewritten image prompt) so REST reads restore the full
	// output_image shape the write side persists.
	RevisedPrompt *string `json:"revisedPrompt,omitempty"`
	// JobID/MediaKind/Progress/Model/PromptSummary/MediaError 是
	// output_media_task 块的任务面字段（设计 §3 块形）。
	JobID         *string `json:"jobId,omitempty"`
	MediaKind     *string `json:"kind,omitempty"`
	Progress      *int64  `json:"progress,omitempty"`
	Model         *string `json:"model,omitempty"`
	PromptSummary *string `json:"promptSummary,omitempty"`
	MediaError    *string `json:"error,omitempty"`
}

// Message mirrors ChatMessage.
type Message struct {
	ID              string            `json:"id"`
	ConversationID  string            `json:"conversationId"`
	TurnID          string            `json:"turnId"`
	SequenceNo      int64             `json:"sequenceNo"`
	ClientMessageID *string           `json:"clientMessageId,omitempty"`
	Role            ChatMessageRole   `json:"role"`
	Status          ChatMessageStatus `json:"status"`
	ContentText     string            `json:"contentText"`
	ContentBlocks   []ContentBlock    `json:"contentBlocks"`
	Model           string            `json:"model"`
	TraceID         *string           `json:"traceId,omitempty"`
	FinishReason    *string           `json:"finishReason,omitempty"`
	ErrorCode       *string           `json:"errorCode,omitempty"`
	ErrorMessage    *string           `json:"errorMessage,omitempty"`
	CreatedAt       string            `json:"createdAt"`
	CompletedAt     *string           `json:"completedAt,omitempty"`
	ExpiresAt       string            `json:"expiresAt"`
}

// conversationRow is the raw scan target with the full chat_conversations
// column list（账户唯一绑定形状：无 bind_mode/bind_group_id/
// bind_group_name_snapshot，含 archived；工具绑定八列已退役不投影——列仍在
// 存量 schema 中且可空，读写不再触碰）。
type conversationRow struct {
	id                          string
	systemAccountID             string
	apiKeyID                    sql.NullString
	apiKeyNameSnapshot          string
	bindAccountID               sql.NullString
	bindAccountNameSnapshot     sql.NullString
	archived                    int64
	title                       string
	titleSourceMessageID        sql.NullString
	isPinned                    int64
	lastModel                   sql.NullString
	nextSequenceNo              int64
	userTurnCount               int64
	messageRevision             int64
	activeTurnID                sql.NullString
	activeStartedAt             sql.NullString
	contextRevision             int64
	activeCheckpointID          sql.NullString
	compactedThroughSequence    int64
	contextState                string
	activeContextTokens         sql.NullInt64
	effectiveContextLimitTokens sql.NullInt64
	contextUsageEstimated       int64
	contextClaimID              sql.NullString
	contextClaimRevision        sql.NullInt64
	contextClaimThroughSequence sql.NullInt64
	contextClaimedAt            sql.NullString
	contextRetryAt              sql.NullString
	contextAttemptCount         int64
	contextErrorCode            sql.NullString
	contextProgressSequence     int64
	contextProgressEarliestExp  sql.NullString
	lastMessageAt               string
	createdAt                   string
	updatedAt                   string
}

const conversationColumns = `id, system_account_id, api_key_id, api_key_name_snapshot,
	bind_account_id, bind_account_name_snapshot, archived,
	title, title_source_message_id,
	is_pinned, last_model, next_sequence_no, user_turn_count, message_revision,
	active_turn_id, active_started_at, context_revision, active_checkpoint_id, compacted_through_sequence,
	context_state, active_context_tokens, effective_context_limit_tokens, context_usage_estimated,
	context_claim_id, context_claim_revision, context_claim_through_sequence, context_claimed_at,
	context_retry_at, context_attempt_count, context_error_code, context_progress_sequence,
	context_progress_earliest_expires_at, last_message_at, created_at, updated_at`

func scanConversationRow(scan func(...any) error) (conversationRow, error) {
	var row conversationRow
	err := scan(&row.id, &row.systemAccountID, &row.apiKeyID, &row.apiKeyNameSnapshot,
		&row.bindAccountID, &row.bindAccountNameSnapshot, &row.archived,
		&row.title,
		&row.titleSourceMessageID, &row.isPinned, &row.lastModel,
		&row.nextSequenceNo, &row.userTurnCount, &row.messageRevision, &row.activeTurnID,
		&row.activeStartedAt, &row.contextRevision, &row.activeCheckpointID,
		&row.compactedThroughSequence, &row.contextState, &row.activeContextTokens,
		&row.effectiveContextLimitTokens, &row.contextUsageEstimated, &row.contextClaimID,
		&row.contextClaimRevision, &row.contextClaimThroughSequence, &row.contextClaimedAt,
		&row.contextRetryAt, &row.contextAttemptCount, &row.contextErrorCode,
		&row.contextProgressSequence, &row.contextProgressEarliestExp, &row.lastMessageAt,
		&row.createdAt, &row.updatedAt)
	return row, err
}

func mapConversation(row conversationRow) (*Conversation, error) {
	if _, err := requireRFC3339Instant(row.lastMessageAt, "聊天会话 last_message_at"); err != nil {
		return nil, err
	}
	if _, err := requireRFC3339Instant(row.createdAt, "聊天会话 created_at"); err != nil {
		return nil, err
	}
	if _, err := requireRFC3339Instant(row.updatedAt, "聊天会话 updated_at"); err != nil {
		return nil, err
	}
	if row.userTurnCount < 0 {
		return nil, &DomainError{Message: "聊天会话轮次计数无效"}
	}
	if row.messageRevision < 0 {
		return nil, &DomainError{Message: "聊天会话消息 revision 无效"}
	}
	return &Conversation{
		ID:                      row.id,
		SystemAccountID:         row.systemAccountID,
		APIKeyID:                nullText(row.apiKeyID),
		APIKeyNameSnapshot:      row.apiKeyNameSnapshot,
		BindAccountID:           nullText(row.bindAccountID),
		BindAccountNameSnapshot: row.bindAccountNameSnapshot.String,
		Archived:                row.archived == 1,
		Title:                   row.title,
		IsPinned:                row.isPinned == 1,
		LastModel:               nullText(row.lastModel),
		ActiveTurnID:            nullText(row.activeTurnID),
		UserTurnCount:           row.userTurnCount,
		MessageRevision:         row.messageRevision,
		LastMessageAt:           row.lastMessageAt,
		CreatedAt:               row.createdAt,
		UpdatedAt:               row.updatedAt,
	}, nil
}

// CreateConversationInput mirrors the createChatConversation input object.
// 会话绑定收敛为仅 account（AI 问答会话账户唯一绑定设计）：创建即空会话
// （bind_account_id NULL，未选账户），账户选定后经 UpdateConversation 写入。
// 工具偏好继承列已随工具体系退役（PLAN-20261012T121500000Z D2/D7）。
type CreateConversationInput struct {
	ID                      string
	SystemAccountID         string
	APIKeyID                string
	APIKeyNameSnapshot      string
	BindAccountID           string
	BindAccountNameSnapshot string
	Now                     string
	MaxConversationsPerUser int
}

// CreateConversation mirrors createChatConversation: per-user policy lock,
// per-user conversation-count guard, insert with 新对话 defaults. last_model
// 落 NULL：空会话未锁定（D5）。工具绑定八列不再写入（存量 schema 列可空，
// default_image_model 沿 schema 默认值）。
func (s *Store) CreateConversation(input CreateConversationInput) (*Conversation, error) {
	now, err := requireRFC3339Instant(input.Now, "聊天会话 now")
	if err != nil {
		return nil, err
	}
	id := input.ID
	if id == "" {
		id = s.newID("conv")
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
	var total int64
	if err := tx.QueryRow(s.bind(`SELECT COUNT(*) AS total FROM `+s.table("chat_conversations")+` WHERE system_account_id = ?`),
		input.SystemAccountID).Scan(&total); err != nil {
		return nil, err
	}
	if total >= int64(input.MaxConversationsPerUser) {
		return nil, &ConflictError{Code: ConflictConversationLimit}
	}
	_, err = tx.Exec(s.bind(`INSERT INTO `+s.table("chat_conversations")+` (
		id, system_account_id, api_key_id, api_key_name_snapshot,
		bind_account_id, bind_account_name_snapshot,
		title, last_model,
		next_sequence_no, user_turn_count, last_message_at, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, '新对话', NULL, 1, 0, ?, ?, ?)`),
		id, input.SystemAccountID, input.APIKeyID, input.APIKeyNameSnapshot,
		optString(input.BindAccountID), input.BindAccountNameSnapshot,
		now, now, now)
	if err != nil {
		return nil, err
	}
	conversation, err := s.requireConversationTx(tx, id, input.SystemAccountID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return conversation, nil
}

func stringPtr(value string) *string { return &value }

// optString mirrors Node `value ?? null`: an empty string means "absent" at
// the store boundary.
func optString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
}

func (s *Store) requireConversationTx(tx queryer, id, ownerID string) (*Conversation, error) {
	row, err := scanConversationRow(func(targets ...any) error {
		return tx.QueryRow(s.bind(`SELECT `+conversationColumns+` FROM `+s.table("chat_conversations")+`
			WHERE id = ? AND system_account_id = ?`), id, ownerID).Scan(targets...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &DomainError{Message: "会话不存在"}
	}
	if err != nil {
		return nil, err
	}
	return mapConversation(row)
}

// GetConversation mirrors getChatConversation (nil when missing).
func (s *Store) GetConversation(conversationID, ownerID string) (*Conversation, error) {
	row, err := scanConversationRow(func(targets ...any) error {
		return s.db.QueryRow(s.bind(`SELECT `+conversationColumns+` FROM `+s.table("chat_conversations")+`
			WHERE id = ? AND system_account_id = ?`), conversationID, ownerID).Scan(targets...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return mapConversation(row)
}

// ListConversations mirrors listChatConversations keyset pagination:
// (is_pinned DESC, last_message_at DESC, id DESC) with the pinned-aware
// before cursor, clamped to 50.
func (s *Store) ListConversations(input ListConversationsInput) ([]*Conversation, error) {
	var beforeLastMessageAt string
	if input.BeforeLastMessageAt != nil {
		normalized, err := requireRFC3339Instant(*input.BeforeLastMessageAt, "聊天会话分页 beforeLastMessageAt")
		if err != nil {
			return nil, err
		}
		beforeLastMessageAt = normalized
	}
	hasCursor := input.BeforeIsPinned != nil && beforeLastMessageAt != "" && input.BeforeID != nil && *input.BeforeID != ""
	beforePinned := int64(0)
	if input.BeforeIsPinned != nil && *input.BeforeIsPinned {
		beforePinned = 1
	}
	limit := clampInt(input.Limit, 1, 50)
	var rows []conversationRow
	var err error
	if hasCursor {
		rows, err = s.queryConversationRows(s.bind(`SELECT `+conversationColumns+` FROM `+s.table("chat_conversations")+`
			WHERE system_account_id = ?
			AND (is_pinned < ? OR (is_pinned = ? AND (last_message_at < ? OR (last_message_at = ? AND id < ?))))
			ORDER BY is_pinned DESC, last_message_at DESC, id DESC
			LIMIT ?`),
			input.SystemAccountID, beforePinned, beforePinned, beforeLastMessageAt, beforeLastMessageAt, *input.BeforeID, limit)
	} else {
		rows, err = s.queryConversationRows(s.bind(`SELECT `+conversationColumns+` FROM `+s.table("chat_conversations")+`
			WHERE system_account_id = ?
			ORDER BY is_pinned DESC, last_message_at DESC, id DESC
			LIMIT ?`), input.SystemAccountID, limit)
	}
	if err != nil {
		return nil, err
	}
	out := make([]*Conversation, 0, len(rows))
	for _, row := range rows {
		conversation, mapErr := mapConversation(row)
		if mapErr != nil {
			return nil, mapErr
		}
		out = append(out, conversation)
	}
	return out, nil
}

type ListConversationsInput struct {
	SystemAccountID     string
	BeforeIsPinned      *bool
	BeforeLastMessageAt *string
	BeforeID            *string
	Limit               int
}

func (s *Store) queryConversationRows(query string, args ...any) ([]conversationRow, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []conversationRow{}
	for rows.Next() {
		row, err := scanConversationRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// UpdateConversation mirrors updateChatConversation: partial assignments and
// the changes-!==1 → undefined → 404 contract. BindAccountID 写入/切换会话绑定
// 账户（handler 已完成数据范围校验与锁定裁决并解析名称快照）；工具绑定键与
// ClearLastModel 已随工具体系/锁定语义退役（PLAN-20261012T121500000Z D2/D5）。
func (s *Store) UpdateConversation(input UpdateConversationInput) (*Conversation, error) {
	now, err := requireRFC3339Instant(input.Now, "聊天会话 now")
	if err != nil {
		return nil, err
	}
	assignments := []string{}
	params := []any{}
	if input.Title != nil {
		assignments = append(assignments, "title = ?", "title_source_message_id = NULL")
		params = append(params, *input.Title)
	}
	if input.IsPinned != nil {
		assignments = append(assignments, "is_pinned = ?")
		params = append(params, boolToInt(*input.IsPinned))
	}
	if input.BindAccountID != nil {
		assignments = append(assignments, "bind_account_id = ?", "bind_account_name_snapshot = ?")
		params = append(params, *input.BindAccountID, input.BindAccountNameSnapshot)
	}
	assignments = append(assignments, "updated_at = ?")
	params = append(params, now, input.ConversationID, input.SystemAccountID)
	result, err := s.db.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
		SET `+joinAssignments(assignments)+`
		WHERE id = ? AND system_account_id = ?`), params...)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, nil
	}
	return s.GetConversation(input.ConversationID, input.SystemAccountID)
}

func joinAssignments(assignments []string) string {
	out := ""
	for i, assignment := range assignments {
		if i > 0 {
			out += ", "
		}
		out += assignment
	}
	return out
}

type UpdateConversationInput struct {
	ConversationID          string
	SystemAccountID         string
	Title                   *string
	IsPinned                *bool
	BindAccountID           *string
	BindAccountNameSnapshot string
	Now                     string
}

// DeleteConversation mirrors deleteChatConversation: quota lock, row lock,
// active-turn conflict, storage release + asset expiry, hard delete.
func (s *Store) DeleteConversation(conversationID, ownerID string) (bool, error) {
	release := s.lockUserPolicy(ownerID)
	defer release()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := s.lockChatUserStorageQuota(tx, ownerID); err != nil {
		return false, err
	}
	row, err := s.lockedConversation(tx, conversationID, ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := requireRFC3339Instant(row.lastMessageAt, "聊天会话 last_message_at"); err != nil {
		return false, err
	}
	if _, err := requireRFC3339Instant(row.createdAt, "聊天会话 created_at"); err != nil {
		return false, err
	}
	if _, err := requireRFC3339Instant(row.updatedAt, "聊天会话 updated_at"); err != nil {
		return false, err
	}
	if row.activeTurnID.Valid {
		return false, &ConflictError{Code: ConflictMessageInProgress}
	}
	if err := s.releaseConversationStorageAndExpireAssets(tx, conversationID, ownerID, s.nowISO()); err != nil {
		return false, err
	}
	result, err := tx.Exec(s.bind(`DELETE FROM `+s.table("chat_conversations")+` WHERE id = ? AND system_account_id = ?`),
		conversationID, ownerID)
	if err != nil {
		return false, err
	}
	deleted, _ := result.RowsAffected()
	if deleted != 1 {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) lockedConversation(tx queryer, conversationID, ownerID string) (conversationRow, error) {
	return scanConversationRow(func(targets ...any) error {
		return tx.QueryRow(s.bind(`SELECT `+conversationColumns+` FROM `+s.table("chat_conversations")+`
			WHERE id = ? AND system_account_id = ?`+s.lockSuffix()), conversationID, ownerID).Scan(targets...)
	})
}

// ClearConversation mirrors clearChatConversation: wipes turn data, resets
// context state, bumps both revisions and stamps 新对话.
func (s *Store) ClearConversation(input ClearConversationInput) (*Conversation, error) {
	now, err := requireRFC3339Instant(input.Now, "聊天会话清空 now")
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
	row, err := s.lockedConversation(tx, input.ConversationID, input.SystemAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.activeTurnID.Valid {
		return nil, &ConflictError{Code: ConflictMessageInProgress}
	}
	if row.contextState == "compacting" {
		return nil, &ConflictError{Code: ConflictContextCompacting}
	}
	if err := s.releaseConversationStorageAndExpireAssets(tx, input.ConversationID, input.SystemAccountID, now); err != nil {
		return nil, err
	}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{s.bind(`DELETE FROM ` + s.table("chat_asset_references") + ` WHERE conversation_id = ?`),
			[]any{input.ConversationID}},
		{s.bind(`DELETE FROM ` + s.table("chat_message_idempotency") + ` WHERE conversation_id = ? AND system_account_id = ?`),
			[]any{input.ConversationID, input.SystemAccountID}},
		{s.bind(`DELETE FROM ` + s.table("chat_messages") + ` WHERE conversation_id = ? AND system_account_id = ?`),
			[]any{input.ConversationID, input.SystemAccountID}},
		{s.bind(`DELETE FROM ` + s.table("chat_context_checkpoints") + ` WHERE conversation_id = ? AND system_account_id = ?`),
			[]any{input.ConversationID, input.SystemAccountID}},
	} {
		if _, err := tx.Exec(statement.sql, statement.args...); err != nil {
			return nil, err
		}
	}
	result, err := tx.Exec(s.bind(`UPDATE `+s.table("chat_conversations")+`
		SET title = '新对话', title_source_message_id = NULL,
			next_sequence_no = 1, user_turn_count = 0,
			message_revision = message_revision + 1,
			active_turn_id = NULL, active_started_at = NULL,
			context_revision = context_revision + 1,
			active_checkpoint_id = NULL, compacted_through_sequence = 0,
			context_state = 'ready', active_context_tokens = NULL,
			effective_context_limit_tokens = NULL, context_usage_estimated = 1,
			context_claim_id = NULL, context_claim_revision = NULL,
			context_claim_through_sequence = NULL, context_claimed_at = NULL,
			context_retry_at = NULL, context_attempt_count = 0,
			context_error_code = NULL, context_progress_sequence = 0,
			context_progress_earliest_expires_at = NULL,
			last_message_at = ?, updated_at = ?
		WHERE id = ? AND system_account_id = ?
			AND active_turn_id IS NULL AND context_state != 'compacting'`),
		now, now, input.ConversationID, input.SystemAccountID)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &DomainError{Message: "清空会话状态发生并发冲突"}
	}
	cleared, err := s.requireConversationTx(tx, input.ConversationID, input.SystemAccountID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return cleared, nil
}

type ClearConversationInput struct {
	ConversationID  string
	SystemAccountID string
	Now             string
}

// TitleFromContent mirrors titleFromContent. Node replaces control characters
// with spaces over the whole content BEFORE the (therefore no-op) first-line
// split, collapses whitespace runs, trims and caps at 60 characters with the
// 新对话 fallback.
func TitleFromContent(content string) string {
	flattened := sanitizeControl(content)
	flattened = collapseSpaces(flattened)
	flattened = jsTrim(flattened)
	runes := []rune(flattened)
	if len(runes) > 60 {
		runes = runes[:60]
	}
	title := string(runes)
	if title == "" {
		return "新对话"
	}
	return title
}

// isJSSpace mirrors the JavaScript \s character class.
func isJSSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x85, 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// jsTrim trims the JavaScript \s set from both ends.
func jsTrim(value string) string {
	return strings.TrimFunc(value, isJSSpace)
}

func sanitizeControl(value string) string {
	out := make([]rune, 0, len(value))
	for _, r := range value {
		if (r >= 0x00 && r <= 0x1f) || r == 0x7f {
			out = append(out, ' ')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

func collapseSpaces(value string) string {
	out := make([]rune, 0, len(value))
	lastSpace := false
	for _, r := range value {
		isSpace := isJSSpace(r)
		if isSpace {
			if !lastSpace {
				out = append(out, ' ')
			}
			lastSpace = true
			continue
		}
		out = append(out, r)
		lastSpace = false
	}
	return string(out[:len(out)])
}

// serializeContentBlocks mirrors serializeContentBlocks with the 256 KiB cap.
func serializeContentBlocks(blocks []ContentBlock) (string, error) {
	value, err := json.Marshal(blocks)
	if err != nil {
		return "", &DomainError{Message: "消息结构化内容超过 256 KiB 上限"}
	}
	if len(value) > maxContentBlocksBytes {
		return "", &DomainError{Message: "消息结构化内容超过 256 KiB 上限"}
	}
	return string(value), nil
}

const maxContentBlocksBytes = 256 * 1024
const maxInputContentBlocks = 11

// AssistantStorageReservationBytes mirrors chatAssistantStorageReservationBytes.
const AssistantStorageReservationBytes = (192 + 192 + 64) * 1024

// parseContentBlocks mirrors parseContentBlocks: invalid/oversized payloads
// degrade to an empty list instead of failing the read.
func parseContentBlocks(value string) []ContentBlock {
	if value == "" || len(value) > maxContentBlocksBytes {
		return []ContentBlock{}
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return []ContentBlock{}
	}
	out := []ContentBlock{}
	for _, item := range parsed {
		if block, ok := contentBlockFromMap(item); ok {
			out = append(out, block)
		}
	}
	return out
}

// contentBlockFromMap projects one persisted JSON block back onto the DTO.
// Read-side restores are best-effort: optional metadata (order/item/blockId/
// mimeType/width/height/revisedPrompt) is recovered only when present and
// well-formed, and is omitted otherwise instead of dropping the whole block.
// This mirrors the write side (assistantBlock), which persists those fields,
// so REST reads return the same shape the SSE path already streams.
func contentBlockFromMap(item map[string]any) (ContentBlock, bool) {
	blockType, _ := item["type"].(string)
	block := ContentBlock{Type: blockType}
	switch blockType {
	case "output_text", "reasoning":
		text, ok := item["text"].(string)
		if !ok {
			return ContentBlock{}, false
		}
		block.Text = &text
		if status, ok := item["status"].(string); ok && blockType == "reasoning" {
			switch status {
			case "started", "completed", "failed", "canceled":
			default:
				return ContentBlock{}, false
			}
			block.Status = &status
		}
		if id, ok := item["blockId"].(string); ok {
			block.BlockID = id
		}
		if order, ok := numericIndex(item["order"]); ok {
			block.Order = &order
		}
		return block, true
	case "input_text":
		order, ok := numericIndex(item["order"])
		if !ok || order < 0 || order >= maxInputContentBlocks {
			return ContentBlock{}, false
		}
		text, ok := item["text"].(string)
		if !ok {
			return ContentBlock{}, false
		}
		block.Order = &order
		block.Text = &text
		return block, true
	case "input_image":
		order, ok := numericIndex(item["order"])
		if !ok || order < 0 || order >= maxInputContentBlocks {
			return ContentBlock{}, false
		}
		assetID, ok := item["assetId"].(string)
		if !ok || trimSpace(assetID) == "" {
			return ContentBlock{}, false
		}
		block.Order = &order
		block.AssetID = &assetID
		return block, true
	case "output_image":
		blockID, ok := item["blockId"].(string)
		if !ok || blockID == "" {
			return ContentBlock{}, false
		}
		order, ok := numericIndex(item["order"])
		if !ok || order < 0 {
			return ContentBlock{}, false
		}
		assetID, ok := item["assetId"].(string)
		if !ok || assetID == "" {
			return ContentBlock{}, false
		}
		status, ok := item["status"].(string)
		if !ok {
			return ContentBlock{}, false
		}
		switch status {
		case "started", "completed", "failed", "canceled":
		default:
			return ContentBlock{}, false
		}
		block.BlockID = blockID
		block.Order = &order
		block.AssetID = &assetID
		block.Status = &status
		if mimeType, ok := item["mimeType"].(string); ok {
			block.MimeType = &mimeType
		}
		if width, ok := numericIndex(item["width"]); ok {
			block.Width = &width
		}
		if height, ok := numericIndex(item["height"]); ok {
			block.Height = &height
		}
		if revisedPrompt, ok := item["revisedPrompt"].(string); ok {
			block.RevisedPrompt = &revisedPrompt
		}
		return block, true
	case "output_audio":
		// M7 output_audio（问答音视频工具设计 §3）：与 output_image 同族的
		// 资产回看块，无尺寸字段。
		blockID, ok := item["blockId"].(string)
		if !ok || blockID == "" {
			return ContentBlock{}, false
		}
		order, ok := numericIndex(item["order"])
		if !ok || order < 0 {
			return ContentBlock{}, false
		}
		assetID, ok := item["assetId"].(string)
		if !ok || assetID == "" {
			return ContentBlock{}, false
		}
		status, ok := item["status"].(string)
		if !ok {
			return ContentBlock{}, false
		}
		switch status {
		case "started", "completed", "failed", "canceled":
		default:
			return ContentBlock{}, false
		}
		block.BlockID = blockID
		block.Order = &order
		block.AssetID = &assetID
		block.Status = &status
		if mimeType, ok := item["mimeType"].(string); ok {
			block.MimeType = &mimeType
		}
		return block, true
	case "output_media_task":
		// M7 output_media_task（问答音视频工具设计 §3）：异步视频任务块，状态
		// 词表为 media_jobs 任务状态（queued|in_progress|completed|failed|
		// cancelled|expired——终态幂等结算后随资产/错误字段定格）。
		blockID, ok := item["blockId"].(string)
		if !ok || blockID == "" {
			return ContentBlock{}, false
		}
		order, ok := numericIndex(item["order"])
		if !ok || order < 0 {
			return ContentBlock{}, false
		}
		jobID, ok := item["jobId"].(string)
		if !ok || jobID == "" {
			return ContentBlock{}, false
		}
		kind, ok := item["kind"].(string)
		if !ok || kind != "video" {
			return ContentBlock{}, false
		}
		status, ok := item["status"].(string)
		if !ok {
			return ContentBlock{}, false
		}
		switch status {
		case "queued", "in_progress", "completed", "failed", "cancelled", "expired":
		default:
			return ContentBlock{}, false
		}
		block.BlockID = blockID
		block.Order = &order
		block.JobID = &jobID
		block.MediaKind = &kind
		block.Status = &status
		if progress, ok := numericIndex(item["progress"]); ok && progress >= 0 && progress <= 100 {
			block.Progress = &progress
		}
		if model, ok := item["model"].(string); ok {
			block.Model = &model
		}
		if promptSummary, ok := item["promptSummary"].(string); ok {
			block.PromptSummary = &promptSummary
		}
		if assetID, ok := item["assetId"].(string); ok && assetID != "" {
			block.AssetID = &assetID
		}
		if mediaError, ok := item["error"].(string); ok && mediaError != "" {
			block.MediaError = &mediaError
		}
		return block, true
	case "tool_call":
		id, hasID := item["id"].(string)
		callID, hasCallID := item["callId"].(string)
		if !hasID && !hasCallID {
			return ContentBlock{}, false
		}
		toolType, ok := item["toolType"].(string)
		if !ok {
			return ContentBlock{}, false
		}
		status, ok := item["status"].(string)
		if !ok {
			return ContentBlock{}, false
		}
		switch status {
		case "started", "updated", "completed", "failed", "canceled":
		default:
			return ContentBlock{}, false
		}
		if hasID {
			block.ID = &id
		}
		if hasCallID {
			block.CallID = &callID
		}
		block.ToolType = &toolType
		block.Status = &status
		if blockID, ok := item["blockId"].(string); ok {
			block.BlockID = blockID
		}
		if order, ok := numericIndex(item["order"]); ok {
			block.Order = &order
		}
		if toolItem, ok := item["item"].(map[string]any); ok {
			block.Item = toolItem
		}
		return block, true
	}
	return ContentBlock{}, false
}

func numericIndex(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		if typed != truncF(typed) || typed < 0 {
			return 0, false
		}
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	}
	return 0, false
}

func truncF(value float64) float64 {
	if value < 0 {
		return -float64(int64(-value))
	}
	return float64(int64(value))
}

func trimSpace(value string) string {
	start, end := 0, len(value)
	for start < end && isSpaceByte(value[start]) {
		start++
	}
	for end > start && isSpaceByte(value[end-1]) {
		end--
	}
	return value[start:end]
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}

// parseStoredInputMarkers mirrors parseStoredInputMarkers: exact key sets
// {order,text,type} / {assetId,order,type} with order === index.
func parseStoredInputMarkers(value string) ([]ContentBlock, bool) {
	if value == "" || len(value) > maxContentBlocksBytes {
		return nil, false
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return nil, false
	}
	if len(parsed) < 1 || len(parsed) > maxInputContentBlocks {
		return nil, false
	}
	markers := make([]ContentBlock, 0, len(parsed))
	for order, item := range parsed {
		blockType, _ := item["type"].(string)
		switch blockType {
		case "input_text":
			if len(item) != 3 {
				return nil, false
			}
			orderValue, ok := numericIndex(item["order"])
			if !ok || orderValue != int64(order) {
				return nil, false
			}
			text, ok := item["text"].(string)
			if !ok {
				return nil, false
			}
			markers = append(markers, ContentBlock{Type: "input_text", Text: &text, Order: int64Ptr(int64(order))})
		case "input_image":
			if len(item) != 3 {
				return nil, false
			}
			orderValue, ok := numericIndex(item["order"])
			if !ok || orderValue != int64(order) {
				return nil, false
			}
			assetID, ok := item["assetId"].(string)
			if !ok || trimSpace(assetID) == "" {
				return nil, false
			}
			normalized := trimSpace(assetID)
			markers = append(markers, ContentBlock{Type: "input_image", AssetID: &normalized, Order: int64Ptr(int64(order))})
		default:
			return nil, false
		}
	}
	return markers, true
}

func int64Ptr(value int64) *int64 { return &value }

// serializeInputContentMarkers mirrors serializeInputContentMarkers.
func serializeInputContentMarkers(blocks []InputContentBlock, userContent string) (string, error) {
	normalized := blocks
	if len(normalized) == 0 {
		normalized = []InputContentBlock{{Type: "input_text", Text: &userContent}}
	}
	if len(normalized) > maxInputContentBlocks {
		return "", &DomainError{Message: "用户输入块不能超过 " + itoa(maxInputContentBlocks) + " 个"}
	}
	markers := make([]ContentBlock, 0, len(normalized))
	for order, block := range normalized {
		switch block.Type {
		case "input_image":
			assetID := ""
			if block.AssetID != nil {
				assetID = trimSpace(*block.AssetID)
			}
			if assetID == "" {
				return "", &DomainError{Message: "图片资产 ID 不能为空"}
			}
			markers = append(markers, ContentBlock{Type: "input_image", Order: int64Ptr(int64(order)), AssetID: &assetID})
		case "input_text":
			text := ""
			if block.Text != nil {
				text = *block.Text
			}
			markers = append(markers, ContentBlock{Type: "input_text", Text: &text, Order: int64Ptr(int64(order))})
		default:
			return "", &DomainError{Message: "用户输入块类型无效"}
		}
	}
	return serializeContentBlocks(markers)
}

// InputContentBlock mirrors ChatInputContentBlock.
type InputContentBlock struct {
	Type    string
	Text    *string
	AssetID *string
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
