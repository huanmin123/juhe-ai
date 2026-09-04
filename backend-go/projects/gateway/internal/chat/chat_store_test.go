package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// errorsAs is a small alias so test cases read uniformly.
func errorsAs(err error, target any) bool { return errors.As(err, target) }

// The DDL fixture mirrors backend-go/projects/maintenance/internal/schema
// sqliteChatDDL (chat_* tables). Tests run against one shared in-memory
// database per case with MaxOpenConns(1) so SQLite single-writer semantics
// match production.

const chatTestDDL = `
CREATE TABLE IF NOT EXISTS chat_conversations (
  id TEXT PRIMARY KEY,
  system_account_id TEXT NOT NULL,
  api_key_id TEXT,
  api_key_name_snapshot TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '新对话',
  title_source_message_id TEXT,
  is_pinned INTEGER NOT NULL DEFAULT 0,
  last_model TEXT,
  default_image_model TEXT NOT NULL DEFAULT 'gpt-image-2',
  next_sequence_no INTEGER NOT NULL DEFAULT 1,
  user_turn_count INTEGER NOT NULL DEFAULT 0,
  message_revision INTEGER NOT NULL DEFAULT 0,
  active_turn_id TEXT,
  active_started_at TEXT,
  context_revision INTEGER NOT NULL DEFAULT 0,
  active_checkpoint_id TEXT,
  compacted_through_sequence INTEGER NOT NULL DEFAULT 0,
  context_state TEXT NOT NULL DEFAULT 'ready',
  active_context_tokens INTEGER,
  effective_context_limit_tokens INTEGER,
  context_usage_estimated INTEGER NOT NULL DEFAULT 1,
  context_claim_id TEXT,
  context_claim_revision INTEGER,
  context_claim_through_sequence INTEGER,
  context_claimed_at TEXT,
  context_retry_at TEXT,
  context_attempt_count INTEGER NOT NULL DEFAULT 0,
  context_error_code TEXT,
  context_progress_sequence INTEGER NOT NULL DEFAULT 0,
  context_progress_earliest_expires_at TEXT,
  last_message_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (next_sequence_no >= 1),
  CHECK (user_turn_count >= 0),
  CHECK (message_revision >= 0),
  CHECK (is_pinned IN (0, 1)),
  CHECK (context_revision >= 0),
  CHECK (compacted_through_sequence >= 0 AND compacted_through_sequence < next_sequence_no),
  CHECK (context_state IN ('ready', 'compact_pending', 'compacting', 'compact_failed')),
  CHECK (active_context_tokens IS NULL OR active_context_tokens >= 0),
  CHECK (effective_context_limit_tokens IS NULL OR effective_context_limit_tokens > 0),
  CHECK (context_usage_estimated IN (0, 1)),
  CHECK (context_attempt_count >= 0),
  CHECK (context_progress_sequence >= 0),
  CHECK (
    (active_checkpoint_id IS NULL AND compacted_through_sequence = 0)
    OR active_checkpoint_id IS NOT NULL
  ),
  CHECK (
    (
      context_state = 'compacting'
      AND context_claim_id IS NOT NULL
      AND context_claim_revision = context_revision
      AND context_claim_through_sequence IS NOT NULL
      AND context_claim_through_sequence > compacted_through_sequence
      AND context_claim_through_sequence <= next_sequence_no - 3
      AND context_claimed_at IS NOT NULL
      AND context_progress_sequence >= compacted_through_sequence
      AND context_progress_sequence <= context_claim_through_sequence
    )
    OR (
      context_state != 'compacting'
      AND context_claim_id IS NULL
      AND context_claim_revision IS NULL
      AND context_claim_through_sequence IS NULL
      AND context_claimed_at IS NULL
      AND context_progress_sequence = 0
      AND context_progress_earliest_expires_at IS NULL
    )
  )
);
CREATE TABLE IF NOT EXISTS chat_messages (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL,
  system_account_id TEXT NOT NULL,
  turn_id TEXT NOT NULL,
  sequence_no INTEGER NOT NULL,
  client_message_id TEXT,
  role TEXT NOT NULL,
  status TEXT NOT NULL,
  content_text TEXT NOT NULL DEFAULT '',
  content_blocks_json TEXT NOT NULL DEFAULT '[]',
  content_bytes INTEGER NOT NULL DEFAULT 0,
  storage_reserved_bytes INTEGER NOT NULL DEFAULT 0,
  model TEXT NOT NULL,
  trace_id TEXT,
  finish_reason TEXT,
  error_code TEXT,
  error_message TEXT,
  created_at TEXT NOT NULL,
  completed_at TEXT,
  expires_at TEXT NOT NULL,
  CHECK (sequence_no >= 1),
  CHECK (content_bytes >= 0),
  CHECK (storage_reserved_bytes >= 0),
  CHECK (role IN ('user', 'assistant')),
  CHECK (status IN ('completed', 'streaming', 'failed', 'canceled')),
  CHECK (
    (role = 'user' AND client_message_id IS NOT NULL AND status = 'completed')
    OR (role = 'assistant' AND client_message_id IS NULL)
  ),
  CHECK (
    (role = 'assistant' AND status = 'streaming' AND storage_reserved_bytes > 0)
    OR (status != 'streaming' AND storage_reserved_bytes = 0)
  )
);
CREATE TABLE IF NOT EXISTS chat_message_idempotency (
  conversation_id TEXT NOT NULL,
  client_message_id TEXT NOT NULL,
  system_account_id TEXT NOT NULL,
  turn_id TEXT NOT NULL,
  user_message_id TEXT NOT NULL,
  assistant_message_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  PRIMARY KEY (conversation_id, client_message_id)
);
CREATE TABLE IF NOT EXISTS chat_user_storage_windows (
  system_account_id TEXT NOT NULL,
  bucket_date TEXT NOT NULL,
  content_bytes INTEGER NOT NULL DEFAULT 0,
  reserved_bytes INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (system_account_id, bucket_date),
  CHECK (content_bytes >= 0),
  CHECK (reserved_bytes >= 0)
);
CREATE TABLE IF NOT EXISTS chat_user_asset_usage (
  system_account_id TEXT PRIMARY KEY,
  asset_bytes INTEGER NOT NULL DEFAULT 0,
  asset_count INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  CHECK (asset_bytes >= 0),
  CHECK (asset_count >= 0)
);
CREATE TABLE IF NOT EXISTS chat_context_checkpoints (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL,
  system_account_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  source_revision INTEGER NOT NULL,
  source_from_sequence INTEGER NOT NULL,
  source_through_sequence INTEGER NOT NULL,
  recent_tail_from_sequence INTEGER NOT NULL,
  entry_from_sequence INTEGER NOT NULL,
  entry_through_sequence INTEGER NOT NULL,
  payload_digest TEXT NOT NULL,
  estimated_input_tokens INTEGER,
  upstream_input_tokens INTEGER,
  request_body_bytes INTEGER NOT NULL,
  model_id TEXT NOT NULL,
  provider_code TEXT,
  provider_profile_id TEXT,
  endpoint_family TEXT NOT NULL,
  compact_compatibility_hash TEXT,
  prompt_version TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  quality_status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  UNIQUE (conversation_id, version),
  CHECK (version >= 1),
  CHECK (source_revision >= 0),
  CHECK (source_from_sequence >= 1),
  CHECK (source_through_sequence >= source_from_sequence),
  CHECK (recent_tail_from_sequence = source_through_sequence + 1),
  CHECK (entry_from_sequence >= 1),
  CHECK (entry_through_sequence >= entry_from_sequence),
  CHECK (length(payload_digest) = 64),
  CHECK (estimated_input_tokens IS NULL OR estimated_input_tokens >= 0),
  CHECK (upstream_input_tokens IS NULL OR upstream_input_tokens >= 0),
  CHECK (request_body_bytes >= 0),
  CHECK (status IN ('pending', 'active', 'superseded', 'rejected')),
  CHECK (quality_status IN ('passed', 'failed'))
);
CREATE TABLE IF NOT EXISTS chat_context_entries (
  conversation_id TEXT NOT NULL,
  checkpoint_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  source_message_id TEXT,
  kind TEXT NOT NULL,
  content_json TEXT NOT NULL,
  content_bytes INTEGER NOT NULL,
  provenance TEXT NOT NULL,
  trust_level TEXT NOT NULL,
  token_count INTEGER,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  PRIMARY KEY (checkpoint_id, sequence),
  CHECK (sequence >= 1),
  CHECK (kind IN ('verbatim', 'durable_memory', 'task_state', 'tool_result', 'image_observation', 'provider_compaction')),
  CHECK (content_bytes >= 2),
  CHECK (provenance IN ('user', 'assistant', 'tool', 'asset', 'provider')),
  CHECK (trust_level IN ('untrusted', 'assistant_derived', 'provider_opaque')),
  CHECK (token_count IS NULL OR token_count >= 0)
);
CREATE TABLE IF NOT EXISTS chat_assets (
  id TEXT PRIMARY KEY,
  system_account_id TEXT NOT NULL,
  conversation_id TEXT NOT NULL,
  source_kind TEXT NOT NULL DEFAULT 'user_upload',
  original_filename TEXT NOT NULL,
  original_mime_type TEXT NOT NULL,
  original_width INTEGER,
  original_height INTEGER,
  original_bytes INTEGER NOT NULL,
  original_sha256 TEXT NOT NULL,
  processed_mime_type TEXT,
  processed_width INTEGER,
  processed_height INTEGER,
  processed_bytes INTEGER,
  processed_sha256 TEXT,
  storage_key TEXT,
  preview_mime_type TEXT,
  preview_width INTEGER,
  preview_height INTEGER,
  preview_bytes INTEGER,
  preview_sha256 TEXT,
  preview_storage_key TEXT,
  processing_status TEXT NOT NULL DEFAULT 'pending',
  processing_error_code TEXT,
  observation_status TEXT NOT NULL DEFAULT 'not_requested',
  observation_json TEXT,
  observation_revision INTEGER NOT NULL DEFAULT 0,
  observation_claim_id TEXT,
  observation_claimed_at TEXT,
  quota_bytes INTEGER NOT NULL,
  turn_id TEXT,
  message_id TEXT,
  committed_at TEXT,
  cleanup_status TEXT NOT NULL DEFAULT 'active',
  cleanup_claim_id TEXT,
  cleanup_attempt_count INTEGER NOT NULL DEFAULT 0,
  cleanup_claimed_at TEXT,
  cleanup_retry_at TEXT,
  cleanup_error_code TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  UNIQUE (id, conversation_id),
  CHECK (original_bytes > 0),
  CHECK (length(original_sha256) = 64),
  CHECK (source_kind IN ('user_upload', 'assistant_generated')),
  CHECK (processing_status IN ('pending', 'ready', 'failed')),
  CHECK (observation_status IN ('not_requested', 'pending', 'ready', 'failed')),
  CHECK (observation_revision >= 0),
  CHECK (quota_bytes > 0),
  CHECK (cleanup_status IN ('active', 'claimed', 'failed')),
  CHECK (cleanup_attempt_count >= 0),
  CHECK (
    (turn_id IS NULL AND message_id IS NULL AND committed_at IS NULL)
    OR (turn_id IS NOT NULL AND message_id IS NOT NULL AND committed_at IS NOT NULL)
  ),
  CHECK (
    (cleanup_status = 'claimed' AND cleanup_claim_id IS NOT NULL AND cleanup_claimed_at IS NOT NULL)
    OR (cleanup_status != 'claimed' AND cleanup_claim_id IS NULL AND cleanup_claimed_at IS NULL)
  )
);
CREATE TABLE IF NOT EXISTS chat_asset_references (
  asset_id TEXT NOT NULL,
  conversation_id TEXT NOT NULL,
  turn_id TEXT NOT NULL,
  message_id TEXT NOT NULL,
  reference_kind TEXT NOT NULL,
  content_order INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  UNIQUE (message_id, content_order),
  CHECK (reference_kind IN ('user_input', 'assistant_output')),
  CHECK (content_order >= 0)
);
CREATE TABLE IF NOT EXISTS chat_image_generations (
  asset_id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL,
  system_account_id TEXT NOT NULL,
  operation TEXT NOT NULL,
  model TEXT NOT NULL,
  prompt TEXT NOT NULL,
  source_asset_ids_json TEXT NOT NULL DEFAULT '[]',
  root_asset_id TEXT NOT NULL,
  size TEXT NOT NULL,
  quality TEXT NOT NULL,
  output_format TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  CHECK (operation IN ('generate', 'edit'))
);
`

// chatFixture bundles one store test environment.
type chatFixture struct {
	t    *testing.T
	db   *sql.DB
	store *Store
	// nowISO is the fixed clock value in canonical ISO form.
	nowISO string
}

func fixedChatClock() (time.Time, func() time.Time) {
	base := time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC)
	return base, func() time.Time { return base }
}

func newChatFixture(t *testing.T) *chatFixture {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	db, err := sql.Open("sqlite", "file:chat-"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range strings.Split(chatTestDDL, ";") {
		trimmed := strings.TrimSpace(statement)
		if trimmed == "" {
			continue
		}
		if _, err := db.Exec(trimmed); err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	_, clock := fixedChatClock()
	store, err := NewStore(db, false, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &chatFixture{t: t, db: db, store: store, nowISO: "2026-03-10T08:00:00.000Z"}
}

// createConversation inserts a conversation directly through the store.
func (f *chatFixture) createConversation(id, ownerID string) *Conversation {
	f.t.Helper()
	conversation, err := f.store.CreateConversation(CreateConversationInput{
		ID:                      id,
		SystemAccountID:         ownerID,
		APIKeyID:                "chat_key_1",
		APIKeyNameSnapshot:      "对话密钥",
		DefaultModel:            "gpt-5",
		Now:                     f.nowISO,
		MaxConversationsPerUser: 30,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return conversation
}

// accept accepts a plain text turn.
func (f *chatFixture) accept(ownerID, conversationID, clientMessageID, content string) *AcceptTurnResult {
	f.t.Helper()
	result, err := f.store.AcceptTurn(AcceptTurnInput{
		ConversationID:          conversationID,
		SystemAccountID:         ownerID,
		ClientMessageID:         clientMessageID,
		UserContent:             content,
		Model:                   "gpt-5",
		Now:                     f.nowISO,
		StorageQuotaBytes:       2 * 1024 * 1024 * 1024,
		RetentionDays:           30,
		MaxTurnsPerConversation: 100,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return result
}

// complete finalizes the streaming assistant message of a turn.
func (f *chatFixture) complete(ownerID, conversationID, turnID, content string) *Message {
	f.t.Helper()
	message, err := f.store.CompleteChatTurn(CompleteTurnInput{
		ConversationID:   conversationID,
		SystemAccountID:  ownerID,
		TurnID:           turnID,
		AssistantContent: content,
		FinishReason:     "stop",
		TraceID:          "",
		Now:              f.nowISO,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return message
}

// seedTurns accepts and completes n turns.
func (f *chatFixture) seedTurns(ownerID, conversationID string, n int) {
	f.t.Helper()
	for i := 1; i <= n; i++ {
		accepted := f.accept(ownerID, conversationID, fmt.Sprintf("cmid-%d", i), fmt.Sprintf("问题%d", i))
		f.complete(ownerID, conversationID, accepted.TurnID, fmt.Sprintf("回答%d", i))
	}
}

func storageWindowTotal(t *testing.T, db *sql.DB, ownerID string) int64 {
	t.Helper()
	var total int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(content_bytes + reserved_bytes), 0) FROM chat_user_storage_windows
		WHERE system_account_id = ?`, ownerID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	return total
}

func nextSequenceNo(t *testing.T, db *sql.DB, conversationID string) int64 {
	t.Helper()
	var value int64
	if err := db.QueryRow(`SELECT next_sequence_no FROM chat_conversations WHERE id = ?`, conversationID).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func userBytesFor(t *testing.T, db *sql.DB, messageID, content string) int64 {
	t.Helper()
	var persistedBlocks string
	if err := db.QueryRow(`SELECT content_blocks_json FROM chat_messages WHERE id = ?`, messageID).Scan(&persistedBlocks); err != nil {
		t.Fatal(err)
	}
	return int64(len(content) + len(persistedBlocks))
}

func TestCreateConversationAndDefaults(t *testing.T) {
	f := newChatFixture(t)
	conversation := f.createConversation("chat_conv_a", "owner-1")
	if conversation.Title != "新对话" || conversation.DefaultImageModel != ImageModelGPTImage2 {
		t.Fatalf("unexpected defaults: %+v", conversation)
	}
	if nextSequenceNo(t, f.db, "chat_conv_a") != 1 || conversation.UserTurnCount != 0 {
		t.Fatalf("unexpected counters: %+v", conversation)
	}
	if conversation.LastModel == nil || *conversation.LastModel != "gpt-5" {
		t.Fatalf("expected default model persisted: %+v", conversation.LastModel)
	}
	// Empty default model must persist NULL (Node `defaultModel ?? null`).
	bare, err := f.store.CreateConversation(CreateConversationInput{
		ID: "chat_conv_b", SystemAccountID: "owner-1", APIKeyID: "k", APIKeyNameSnapshot: "n",
		Now: f.nowISO, MaxConversationsPerUser: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bare.LastModel != nil {
		t.Fatalf("expected NULL last_model, got %v", *bare.LastModel)
	}
	var raw sql.NullString
	if err := f.db.QueryRow(`SELECT last_model FROM chat_conversations WHERE id = 'chat_conv_b'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw.Valid {
		t.Fatalf("expected NULL column, got %q", raw.String)
	}
}

func TestConversationLimitConflict(t *testing.T) {
	f := newChatFixture(t)
	if _, err := f.store.CreateConversation(CreateConversationInput{
		SystemAccountID: "owner-1", APIKeyID: "k", APIKeyNameSnapshot: "n",
		Now: f.nowISO, MaxConversationsPerUser: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateConversation(CreateConversationInput{
		SystemAccountID: "owner-1", APIKeyID: "k", APIKeyNameSnapshot: "n",
		Now: f.nowISO, MaxConversationsPerUser: 2,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.CreateConversation(CreateConversationInput{
		SystemAccountID: "owner-1", APIKeyID: "k", APIKeyNameSnapshot: "n",
		Now: f.nowISO, MaxConversationsPerUser: 2,
	})
	var conflict *ConflictError
	if !errorsAs(err, &conflict) || conflict.Code != ConflictConversationLimit {
		t.Fatalf("expected conversation limit conflict, got %v", err)
	}
	if conflict.Error() != "会话数量已达到上限，请先删除部分会话" {
		t.Fatalf("unexpected message: %q", conflict.Error())
	}
}

func TestListConversationsKeysetPagination(t *testing.T) {
	f := newChatFixture(t)
	for i := 1; i <= 4; i++ {
		f.createConversation(fmt.Sprintf("chat_conv_%02d", i), "owner-1")
	}
	// Pin one conversation and give each a distinct last_message_at.
	for i := 1; i <= 4; i++ {
		pinned := 0
		if i == 3 {
			pinned = 1
		}
		if _, err := f.db.Exec(`UPDATE chat_conversations SET is_pinned = ?, last_message_at = ?
			WHERE id = ?`, pinned, fmt.Sprintf("2026-03-10T08:00:0%d.000Z", i), fmt.Sprintf("chat_conv_%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.store.ListConversations(ListConversationsInput{SystemAccountID: "owner-1", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].ID != "chat_conv_03" || page[1].ID != "chat_conv_04" {
		t.Fatalf("unexpected first page: %+v", page)
	}
	nextPage, err := f.store.ListConversations(ListConversationsInput{
		SystemAccountID:     "owner-1",
		BeforeIsPinned:      boolPtr(page[1].IsPinned),
		BeforeLastMessageAt: stringPtr(page[1].LastMessageAt),
		BeforeID:            stringPtr(page[1].ID),
		Limit:               2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nextPage) != 2 || nextPage[0].ID != "chat_conv_02" || nextPage[1].ID != "chat_conv_01" {
		t.Fatalf("unexpected second page: %+v", nextPage)
	}
	// Cross-owner isolation.
	other, err := f.store.ListConversations(ListConversationsInput{SystemAccountID: "owner-2", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("expected no cross-owner rows, got %d", len(other))
	}
}

func TestUpdateConversationPartialAnd404(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	updated, err := f.store.UpdateConversation(UpdateConversationInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1",
		Title: stringPtr(" 新标题 "), Now: f.nowISO,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != " 新标题 " {
		t.Fatalf("title should be stored raw (route trims), got %q", updated.Title)
	}
	var titleSource sql.NullString
	if err := f.db.QueryRow(`SELECT title_source_message_id FROM chat_conversations WHERE id = 'chat_conv_a'`).Scan(&titleSource); err != nil {
		t.Fatal(err)
	}
	if titleSource.Valid {
		t.Fatalf("title update must clear title_source_message_id")
	}
	missing, err := f.store.UpdateConversation(UpdateConversationInput{
		ConversationID: "chat_conv_missing", SystemAccountID: "owner-1",
		IsPinned: boolPtr(true), Now: f.nowISO,
	})
	if err != nil || missing != nil {
		t.Fatalf("expected nil conversation + nil err, got %v %v", missing, err)
	}
	if _, err := f.store.UpdateConversation(UpdateConversationInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1",
		DefaultImageModel: stringPtr("dall-e-3"), Now: f.nowISO,
	}); err == nil {
		t.Fatal("expected invalid image model error")
	}
}

func TestDeleteConversationLifecycle(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	f.seedTurns("owner-1", "chat_conv_a", 1)
	if total := storageWindowTotal(t, f.db, "owner-1"); total == 0 {
		t.Fatal("expected storage window entries before delete")
	}
	deleted, err := f.store.DeleteConversation("chat_conv_a", "owner-1")
	if err != nil || !deleted {
		t.Fatalf("expected delete, got %v %v", deleted, err)
	}
	if total := storageWindowTotal(t, f.db, "owner-1"); total != 0 {
		t.Fatalf("expected released windows, got %d", total)
	}
	again, err := f.store.DeleteConversation("chat_conv_a", "owner-1")
	if err != nil || again {
		t.Fatalf("expected second delete false, got %v %v", again, err)
	}
	// Cross-owner delete is a 404, not a mutation.
	f.createConversation("chat_conv_b", "owner-1")
	cross, err := f.store.DeleteConversation("chat_conv_b", "owner-2")
	if err != nil || cross {
		t.Fatalf("expected cross-owner delete false, got %v %v", cross, err)
	}
}

func TestDeleteConversationActiveTurnConflict(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	f.accept("owner-1", "chat_conv_a", "cmid-1", "你好")
	_, err := f.store.DeleteConversation("chat_conv_a", "owner-1")
	var conflict *ConflictError
	if !errorsAs(err, &conflict) || conflict.Code != ConflictMessageInProgress {
		t.Fatalf("expected chat_message_in_progress, got %v", err)
	}
	if conflict.Error() != "当前会话正在生成回答" {
		t.Fatalf("unexpected message %q", conflict.Error())
	}
}

func TestClearConversationResetsState(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	f.seedTurns("owner-1", "chat_conv_a", 2)
	cleared, err := f.store.ClearConversation(ClearConversationInput{ConversationID: "chat_conv_a", SystemAccountID: "owner-1", Now: f.nowISO})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.UserTurnCount != 0 || cleared.Title != "新对话" || cleared.ActiveTurnID != nil {
		t.Fatalf("unexpected cleared conversation: %+v", cleared)
	}
	var revision, contextRevision int64
	if err := f.db.QueryRow(`SELECT message_revision, context_revision FROM chat_conversations WHERE id = 'chat_conv_a'`).Scan(&revision, &contextRevision); err != nil {
		t.Fatal(err)
	}
	if revision == 0 || contextRevision == 0 {
		t.Fatalf("clear must bump revisions: %d %d", revision, contextRevision)
	}
	var messageCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM chat_messages WHERE conversation_id = 'chat_conv_a'`).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if messageCount != 0 {
		t.Fatalf("expected wiped messages, got %d", messageCount)
	}
	if total := storageWindowTotal(t, f.db, "owner-1"); total != 0 {
		t.Fatalf("expected released windows, got %d", total)
	}
	// Clearing during an active turn conflicts.
	f.accept("owner-1", "chat_conv_a", "cmid-x", "进行中")
	_, err = f.store.ClearConversation(ClearConversationInput{ConversationID: "chat_conv_a", SystemAccountID: "owner-1", Now: f.nowISO})
	var conflict *ConflictError
	if !errorsAs(err, &conflict) || conflict.Code != ConflictMessageInProgress {
		t.Fatalf("expected chat_message_in_progress, got %v", err)
	}
}

func TestAcceptTurnLifecycleAndIdempotency(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	first := f.accept("owner-1", "chat_conv_a", "cmid-1", "第一问")
	if first.Duplicate {
		t.Fatal("first accept must not be duplicate")
	}
	if first.UserMessage.SequenceNo != 1 || first.AssistantMessage.SequenceNo != 2 {
		t.Fatalf("unexpected sequences: %+v", first)
	}
	if first.AssistantMessage.Status != StatusStreaming {
		t.Fatalf("expected streaming assistant, got %s", first.AssistantMessage.Status)
	}
	// Idempotent replay returns the same turn.
	replay, err := f.store.AcceptTurn(AcceptTurnInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ClientMessageID: "cmid-1",
		UserContent: "第一问", Model: "gpt-5", Now: f.nowISO,
		StorageQuotaBytes: 2 * 1024 * 1024 * 1024, RetentionDays: 30, MaxTurnsPerConversation: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Duplicate || replay.TurnID != first.TurnID {
		t.Fatalf("expected duplicate replay, got %+v", replay)
	}
	// Finalize the first turn before submitting the next one.
	f.complete("owner-1", "chat_conv_a", first.TurnID, "第一答")
	userBytes := userBytesFor(t, f.db, first.UserMessage.ID, "第一问")
	assistantBytes := int64(len("第一答") + len("[]"))
	// Window after finalize: user1 + assistant1 (+ turn2 streaming below).
	// Second turn advances sequences and bumps counters.
	second := f.accept("owner-1", "chat_conv_a", "cmid-2", "第二问")
	if second.UserMessage.SequenceNo != 3 || second.AssistantMessage.SequenceNo != 4 {
		t.Fatalf("unexpected second sequences: %+v", second)
	}
	conversation, err := f.store.GetConversation("chat_conv_a", "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.UserTurnCount != 2 || nextSequenceNo(t, f.db, "chat_conv_a") != 5 {
		t.Fatalf("unexpected counters: %+v", conversation)
	}
	if conversation.Title != "第一问" {
		t.Fatalf("first turn should set the title, got %q", conversation.Title)
	}
	// Storage windows: user1 + assistant1 content, plus user2 + reservation
	// (turn 2 stays streaming).
	wantTotal := 2*userBytes + assistantBytes + AssistantStorageReservationBytes
	if total := storageWindowTotal(t, f.db, "owner-1"); total != wantTotal {
		t.Fatalf("expected window total %d, got %d", wantTotal, total)
	}
}

func TestAcceptTurnTitleFromMultilineContent(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	// Node replaces control characters before the first-line split, so the
	// whole content collapses into one title line.
	f.accept("owner-1", "chat_conv_a", "cmid-1", "首行\n第二行\t更多")
	conversation, err := f.store.GetConversation("chat_conv_a", "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Title != "首行 第二行 更多" {
		t.Fatalf("unexpected title %q", conversation.Title)
	}
}

func TestTitleFromContentTable(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", "新对话"},
		{"spaces only", "   \n\t ", "新对话"},
		{"simple", "你好", "你好"},
		{"multiline collapses", "a\nb\nc", "a b c"},
		{"control flattened", "a\x07b", "a b"},
		{"collapse runs", "a\n\n  b", "a b"},
		{"cjk 60 cap", strings.Repeat("汉", 70), strings.Repeat("汉", 60)},
		{"trailing trimmed", "  标题  ", "标题"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := TitleFromContent(testCase.content); got != testCase.want {
				t.Fatalf("TitleFromContent(%q) = %q, want %q", testCase.content, got, testCase.want)
			}
		})
	}
}

func TestAcceptTurnTurnLimitAndQuota(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	_, err := f.store.AcceptTurn(AcceptTurnInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ClientMessageID: "cmid-1",
		UserContent: "问", Model: "gpt-5", Now: f.nowISO,
		StorageQuotaBytes: 2 * 1024 * 1024 * 1024, RetentionDays: 30, MaxTurnsPerConversation: 0,
	})
	var turnLimit *ConflictError
	if !errorsAs(err, &turnLimit) || turnLimit.Code != ConflictTurnLimitExceeded {
		t.Fatalf("expected chat_turn_limit_exceeded, got %v", err)
	}
	if turnLimit.Error() != "当前会话轮次已达到上限，请新建会话继续提问" {
		t.Fatalf("unexpected message %q", turnLimit.Error())
	}
	// Quota: reservation + user bytes exceed a tiny quota.
	_, err = f.store.AcceptTurn(AcceptTurnInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ClientMessageID: "cmid-2",
		UserContent: "问", Model: "gpt-5", Now: f.nowISO,
		StorageQuotaBytes: 64, RetentionDays: 30, MaxTurnsPerConversation: 10,
	})
	var quota *ConflictError
	if !errorsAs(err, &quota) || quota.Code != ConflictStorageQuotaExceeded {
		t.Fatalf("expected chat_storage_quota_exceeded, got %v", err)
	}
	if quota.Error() != "聊天容量已达到上限，请先删除部分会话" {
		t.Fatalf("unexpected message %q", quota.Error())
	}
}

func TestAcceptTurnActiveTurnConflict(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	f.accept("owner-1", "chat_conv_a", "cmid-1", "进行中")
	_, err := f.store.AcceptTurn(AcceptTurnInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ClientMessageID: "cmid-2",
		UserContent: "再来", Model: "gpt-5", Now: f.nowISO,
		StorageQuotaBytes: 2 * 1024 * 1024 * 1024, RetentionDays: 30, MaxTurnsPerConversation: 10,
	})
	var conflict *ConflictError
	if !errorsAs(err, &conflict) || conflict.Code != ConflictMessageInProgress {
		t.Fatalf("expected chat_message_in_progress, got %v", err)
	}
	// Replace during an active turn is chat_replace_conflict.
	_, err = f.store.AcceptTurn(AcceptTurnInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ClientMessageID: "cmid-3",
		UserContent: "替换", Model: "gpt-5", Now: f.nowISO,
		StorageQuotaBytes: 2 * 1024 * 1024 * 1024, RetentionDays: 30, MaxTurnsPerConversation: 10,
		ReplaceTurnID: "chat_turn_missing",
	})
	if !errorsAs(err, &conflict) || conflict.Code != ConflictReplaceConflict {
		t.Fatalf("expected chat_replace_conflict, got %v", err)
	}
}

func TestReplaceTurnFlow(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	f.seedTurns("owner-1", "chat_conv_a", 1)
	first, err := f.store.ListMessages(ListMessagesInput{ConversationID: "chat_conv_a", SystemAccountID: "owner-1", Now: f.nowISO, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	turnID := first[0].TurnID
	beforeTotal := storageWindowTotal(t, f.db, "owner-1")
	result, err := f.store.AcceptTurn(AcceptTurnInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ClientMessageID: "cmid-2",
		UserContent: "重新问", Model: "gpt-5", Now: f.nowISO,
		StorageQuotaBytes: 2 * 1024 * 1024 * 1024, RetentionDays: 30, MaxTurnsPerConversation: 1,
		ReplaceTurnID: turnID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.UserMessage.SequenceNo != 1 || result.AssistantMessage.SequenceNo != 2 {
		t.Fatalf("replace must reuse sequences: %+v", result)
	}
	conversation, err := f.store.GetConversation("chat_conv_a", "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.UserTurnCount != 1 || nextSequenceNo(t, f.db, "chat_conv_a") != 3 {
		t.Fatalf("replace must not advance counters: %+v", conversation)
	}
	afterTotal := storageWindowTotal(t, f.db, "owner-1")
	expectedUserBytes := userBytesFor(t, f.db, result.UserMessage.ID, "重新问")
	if afterTotal != expectedUserBytes+AssistantStorageReservationBytes {
		t.Fatalf("expected replaced window total %d, got %d (before %d)", expectedUserBytes+AssistantStorageReservationBytes, afterTotal, beforeTotal)
	}
	// Old idempotency row must be gone.
	var idempotencyCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM chat_message_idempotency WHERE turn_id = ?`, turnID).Scan(&idempotencyCount); err != nil {
		t.Fatal(err)
	}
	if idempotencyCount != 0 {
		t.Fatalf("expected replaced idempotency rows to be deleted, got %d", idempotencyCount)
	}
	// Replacing a non-latest turn conflicts.
	if _, err := f.store.AcceptTurn(AcceptTurnInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ClientMessageID: "cmid-3",
		UserContent: "再替换", Model: "gpt-5", Now: f.nowISO,
		StorageQuotaBytes: 2 * 1024 * 1024 * 1024, RetentionDays: 30, MaxTurnsPerConversation: 10,
		ReplaceTurnID: turnID,
	}); err == nil {
		t.Fatal("expected stale replace to fail")
	} else {
		var conflict *ConflictError
		if !errorsAs(err, &conflict) || conflict.Code != ConflictReplaceConflict {
			t.Fatalf("expected chat_replace_conflict, got %v", err)
		}
	}
}

func TestFinalizeTurnPaths(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_c", "owner-1")
	f.createConversation("chat_conv_d", "owner-2")

	t.Run("complete settles reservation", func(t *testing.T) {
		accepted := f.accept("owner-1", "chat_conv_c", "cmid-1", "问")
		blocks := []ContentBlock{{Type: "output_text", Text: stringPtr("部分")}}
		message, err := f.store.CompleteChatTurn(CompleteTurnInput{
			ConversationID: "chat_conv_c", SystemAccountID: "owner-1", TurnID: accepted.TurnID,
			AssistantContent: "完整回答", FinishReason: "stop", TraceID: "trace-1", ContentBlocks: blocks, Now: f.nowISO,
		})
		if err != nil {
			t.Fatal(err)
		}
		if message.Status != StatusCompleted || message.ContentText != "完整回答" || message.FinishReason == nil || *message.FinishReason != "stop" {
			t.Fatalf("unexpected finalized message: %+v", message)
		}
		if message.TraceID == nil || *message.TraceID != "trace-1" {
			t.Fatalf("expected trace persisted: %+v", message.TraceID)
		}
		// Window: user bytes + assistant bytes (no reservation remainder).
		want := userBytesFor(t, f.db, accepted.UserMessage.ID, "问") + int64(len("完整回答"))
		assistantJSON, err := serializeContentBlocks(blocks)
		if err != nil {
			t.Fatal(err)
		}
		want += int64(len(assistantJSON))
		if total := storageWindowTotal(t, f.db, "owner-1"); total != want {
			t.Fatalf("expected window %d, got %d", want, total)
		}
		var conversationActive sql.NullString
		if err := f.db.QueryRow(`SELECT active_turn_id FROM chat_conversations WHERE id = 'chat_conv_c'`).Scan(&conversationActive); err != nil {
			t.Fatal(err)
		}
		if conversationActive.Valid {
			t.Fatal("finalize must clear active_turn_id")
		}
	})

	t.Run("storage limit downgrades to failed", func(t *testing.T) {
		accepted := f.accept("owner-2", "chat_conv_d", "cmid-1", "问")
		huge := strings.Repeat("很", AssistantStorageReservationBytes) // > reservation in bytes (3 bytes per rune)
		_, err := f.store.CompleteChatTurn(CompleteTurnInput{
			ConversationID: "chat_conv_d", SystemAccountID: "owner-2", TurnID: accepted.TurnID,
			AssistantContent: huge, FinishReason: "stop", Now: f.nowISO,
		})
		var limitErr *AssistantStorageLimitError
		if !errorsAs(err, &limitErr) {
			t.Fatalf("expected assistant storage limit error, got %v", err)
		}
		var status, errorCode, errorMessage string
		if err := f.db.QueryRow(`SELECT status, error_code, error_message FROM chat_messages WHERE id = ?`,
			accepted.AssistantMessage.ID).Scan(&status, &errorCode, &errorMessage); err != nil {
			t.Fatal(err)
		}
		if status != "failed" || errorCode != "chat_assistant_storage_limit_exceeded" || errorMessage != "AI 回答超过聊天存储上限" {
			t.Fatalf("unexpected downgrade: %s %s %s", status, errorCode, errorMessage)
		}
		// Reservation fully converts to content bytes ("[]" = 2 bytes).
		wantWindow := userBytesFor(t, f.db, accepted.UserMessage.ID, "问") + 2
		if total := storageWindowTotal(t, f.db, "owner-2"); total != wantWindow {
			t.Fatalf("unexpected window after downgrade: %d (want %d)", total, wantWindow)
		}
	})
}

func TestConditionalStopStates(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")

	// not_found on unknown conversation.
	missing, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
		ConversationID: "chat_conv_missing", SystemAccountID: "owner-1", ExpectedTurnID: "t", Now: f.nowISO,
	})
	if err != nil || missing.State != CancelStateNotFound {
		t.Fatalf("expected not_found, got %+v %v", missing, err)
	}

	accepted := f.accept("owner-1", "chat_conv_a", "cmid-1", "问")

	// turn_mismatch on wrong turn id.
	mismatch, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ExpectedTurnID: "chat_turn_other", Now: f.nowISO,
	})
	if err != nil || mismatch.State != CancelStateTurnMismatch {
		t.Fatalf("expected turn_mismatch, got %+v %v", mismatch, err)
	}

	// already_terminal once the turn finished.
	f.complete("owner-1", "chat_conv_a", accepted.TurnID, "答")
	terminal, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ExpectedTurnID: accepted.TurnID, Now: f.nowISO,
	})
	if err != nil || terminal.State != CancelStateAlreadyTerminal || terminal.AssistantStatus != StatusCompleted {
		t.Fatalf("expected already_terminal completed, got %+v %v", terminal, err)
	}

	// cancel on a live stream releases the reservation.
	second := f.accept("owner-1", "chat_conv_a", "cmid-2", "第二问")
	beforeCancel := storageWindowTotal(t, f.db, "owner-1")
	canceled, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ExpectedTurnID: second.TurnID, Now: f.nowISO,
	})
	if err != nil || canceled.State != CancelStateCanceled || canceled.AssistantStatus != StatusCanceled {
		t.Fatalf("expected canceled, got %+v %v", canceled, err)
	}
	afterCancel := storageWindowTotal(t, f.db, "owner-1")
	if afterCancel >= beforeCancel {
		t.Fatalf("cancel must release the reservation: %d -> %d", beforeCancel, afterCancel)
	}

	// failInterrupted stamps stream_interrupted.
	third := f.accept("owner-1", "chat_conv_a", "cmid-3", "第三问")
	interrupted, err := f.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ExpectedTurnID: third.TurnID, Now: f.nowISO,
	})
	if err != nil || interrupted.State != CancelStateAlreadyTerminal || interrupted.AssistantStatus != StatusFailed {
		t.Fatalf("expected already_terminal failed, got %+v %v", interrupted, err)
	}
	var errorCode string
	if err := f.db.QueryRow(`SELECT error_code FROM chat_messages WHERE id = ?`, third.AssistantMessage.ID).Scan(&errorCode); err != nil {
		t.Fatal(err)
	}
	if errorCode != "stream_interrupted" {
		t.Fatalf("expected stream_interrupted, got %q", errorCode)
	}
}

func TestListMessagesPaginationBoundaries(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	for i := 1; i <= 5; i++ {
		accepted := f.accept("owner-1", "chat_conv_a", fmt.Sprintf("cmid-%d", i), fmt.Sprintf("问%d", i))
		f.complete("owner-1", "chat_conv_a", accepted.TurnID, fmt.Sprintf("答%d", i))
	}
	base := ListMessagesInput{ConversationID: "chat_conv_a", SystemAccountID: "owner-1", Now: f.nowISO, Limit: 100}

	all, err := f.store.ListMessages(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 10 || all[0].SequenceNo != 1 || all[9].SequenceNo != 10 {
		t.Fatalf("expected 10 ascending messages, got %d (first %d last %d)", len(all), all[0].SequenceNo, all[9].SequenceNo)
	}

	before := int64(9)
	page, err := f.store.ListMessages(ListMessagesInput{ConversationID: "chat_conv_a", SystemAccountID: "owner-1", Now: f.nowISO, Limit: 2, BeforeSequenceNo: &before})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].SequenceNo != 7 || page[1].SequenceNo != 8 {
		t.Fatalf("DESC before-cursor must reverse to ASC: %+v", page)
	}

	after := int64(8)
	afterPage, err := f.store.ListMessages(ListMessagesInput{ConversationID: "chat_conv_a", SystemAccountID: "owner-1", Now: f.nowISO, Limit: 2, AfterSequenceNo: &after})
	if err != nil {
		t.Fatal(err)
	}
	if len(afterPage) != 2 || afterPage[0].SequenceNo != 9 || afterPage[1].SequenceNo != 10 {
		t.Fatalf("unexpected after page: %+v", afterPage)
	}

	from := int64(9)
	fromPage, err := f.store.ListMessages(ListMessagesInput{ConversationID: "chat_conv_a", SystemAccountID: "owner-1", Now: f.nowISO, Limit: 3, FromSequenceNo: &from})
	if err != nil {
		t.Fatal(err)
	}
	if len(fromPage) != 2 || fromPage[0].SequenceNo != 9 {
		t.Fatalf("from-cursor is inclusive: %+v", fromPage)
	}

	// Clamp: limit 0 → 1 (store clamps; route default is 100).
	clamped, err := f.store.ListMessages(ListMessagesInput{ConversationID: "chat_conv_a", SystemAccountID: "owner-1", Now: f.nowISO, Limit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(clamped) != 1 {
		t.Fatalf("expected single row after clamp, got %d", len(clamped))
	}

	// More than one cursor → 消息游标只能指定一个.
	two := int64(2)
	_, err = f.store.ListMessages(ListMessagesInput{ConversationID: "chat_conv_a", SystemAccountID: "owner-1", Now: f.nowISO, Limit: 2, BeforeSequenceNo: &two, AfterSequenceNo: &two})
	if err == nil || err.Error() != "消息游标只能指定一个" {
		t.Fatalf("expected cursor conflict, got %v", err)
	}

	// Unknown conversation → 会话不存在.
	_, err = f.store.ListMessages(ListMessagesInput{ConversationID: "chat_conv_x", SystemAccountID: "owner-1", Now: f.nowISO, Limit: 2})
	if err == nil || err.Error() != "会话不存在" {
		t.Fatalf("expected missing conversation error, got %v", err)
	}
}

func TestFindTurnByClientMessageID(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	accepted := f.accept("owner-1", "chat_conv_a", "cmid-1", "问")
	fact, err := f.store.FindTurnByClientMessageID("chat_conv_a", "owner-1", "cmid-1")
	if err != nil {
		t.Fatal(err)
	}
	if fact == nil || fact.TurnID != accepted.TurnID || fact.AssistantStatus != StatusStreaming {
		t.Fatalf("unexpected fact: %+v", fact)
	}
	f.complete("owner-1", "chat_conv_a", accepted.TurnID, "答")
	fact, err = f.store.FindTurnByClientMessageID("chat_conv_a", "owner-1", "cmid-1")
	if err != nil {
		t.Fatal(err)
	}
	if fact.AssistantStatus != StatusCompleted || fact.CompletedAt == nil {
		t.Fatalf("expected completed fact: %+v", fact)
	}
	missing, err := f.store.FindTurnByClientMessageID("chat_conv_a", "owner-1", "nope")
	if err != nil || missing != nil {
		t.Fatalf("expected nil fact, got %+v %v", missing, err)
	}
	// Cross-owner cannot see the submission.
	other, err := f.store.FindTurnByClientMessageID("chat_conv_a", "owner-2", "cmid-1")
	if err != nil || other != nil {
		t.Fatalf("expected cross-owner nil, got %+v %v", other, err)
	}
}

func TestRFC3339CanonicalizationTable(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{"zulu", "2026-03-10T08:00:00Z", "2026-03-10T08:00:00.000Z", false},
		{"offset", "2026-03-10T16:00:00+08:00", "2026-03-10T08:00:00.000Z", false},
		{"millis", "2026-03-10T08:00:00.123Z", "2026-03-10T08:00:00.123Z", false},
		{"micro trimmed", "2026-03-10T08:00:00.1Z", "2026-03-10T08:00:00.100Z", false},
		{"bare datetime rejected", "2026-03-10T08:00:00", "", true},
		{"date only rejected", "2026-03-10", "", true},
		{"feb 30 rejected", "2026-02-30T00:00:00Z", "", true},
		{"hour 24 rejected", "2026-03-10T24:00:00Z", "", true},
		{"offset minutes rejected", "2026-03-10T08:00:00+00:99", "", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := requireRFC3339Instant(testCase.value, "标签")
			if testCase.wantErr {
				if err == nil || err.Error() != "标签必须是带 Z 或数值 offset 的 RFC3339 时间" {
					t.Fatalf("expected labeled error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.want {
				t.Fatalf("canonicalize(%q) = %q, want %q", testCase.value, got, testCase.want)
			}
		})
	}
}

func TestParseContentBlocksDegradesGracefully(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantLen int
	}{
		{"empty", "", 0},
		{"invalid", "{", 0},
		{"not array", `{"a":1}`, 0},
		{"valid output_text", `[{"type":"output_text","text":"x"}]`, 1},
		{"output_text missing text", `[{"type":"output_text"}]`, 0},
		{"input_image ok", `[{"type":"input_image","order":0,"assetId":"chat_asset_1"}]`, 1},
		{"input_image blank asset", `[{"type":"input_image","order":0,"assetId":"  "}]`, 0},
		{"input_image order out of range", `[{"type":"input_image","order":11,"assetId":"a"}]`, 0},
		{"tool_call ok", `[{"type":"tool_call","callId":"c1","toolType":"web_search","status":"started"}]`, 1},
		{"tool_call bad status", `[{"type":"tool_call","callId":"c1","toolType":"t","status":"weird"}]`, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			blocks := parseContentBlocks(testCase.json)
			if len(blocks) != testCase.wantLen {
				t.Fatalf("parseContentBlocks(%q) len = %d, want %d", testCase.json, len(blocks), testCase.wantLen)
			}
		})
	}
}

func TestSerializeInputContentMarkers(t *testing.T) {
	content, err := serializeInputContentMarkers(nil, "正文")
	if err != nil {
		t.Fatal(err)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 || parsed[0]["type"] != "input_text" || parsed[0]["text"] != "正文" || parsed[0]["order"] != float64(0) {
		t.Fatalf("unexpected markers: %v", parsed)
	}
	_, err = serializeInputContentMarkers([]InputContentBlock{{Type: "input_image", AssetID: stringPtr(" ")}}, "x")
	if err == nil || err.Error() != "图片资产 ID 不能为空" {
		t.Fatalf("expected blank asset error, got %v", err)
	}
	_, err = serializeInputContentMarkers([]InputContentBlock{{Type: "tool_call"}}, "x")
	if err == nil || err.Error() != "用户输入块类型无效" {
		t.Fatalf("expected invalid block type error, got %v", err)
	}
	many := make([]InputContentBlock, 12)
	for i := range many {
		many[i] = InputContentBlock{Type: "input_text", Text: stringPtr("t")}
	}
	_, err = serializeInputContentMarkers(many, "x")
	if err == nil || err.Error() != "用户输入块不能超过 11 个" {
		t.Fatalf("expected marker count error, got %v", err)
	}
}

func TestConcurrentAcceptSerializesPerOwner(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	const goroutines = 8
	results := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func(index int) {
			_, err := f.store.AcceptTurn(AcceptTurnInput{
				ConversationID: "chat_conv_a", SystemAccountID: "owner-1",
				ClientMessageID: fmt.Sprintf("race-%d", index),
				UserContent:     "并发问", Model: "gpt-5", Now: f.nowISO,
				StorageQuotaBytes: 2 * 1024 * 1024 * 1024, RetentionDays: 30, MaxTurnsPerConversation: 100,
			})
			results <- err
		}(i)
	}
	accepted, conflicted := 0, 0
	for i := 0; i < goroutines; i++ {
		err := <-results
		switch {
		case err == nil:
			accepted++
		default:
			var conflict *ConflictError
			if errorsAs(err, &conflict) && conflict.Code == ConflictMessageInProgress {
				conflicted++
			} else {
				t.Fatalf("unexpected concurrent error: %v", err)
			}
		}
	}
	if accepted != 1 || conflicted != goroutines-1 {
		t.Fatalf("expected 1 accepted + %d conflicts, got %d/%d", goroutines-1, accepted, conflicted)
	}
}

func TestStorageWindowReleaseStrictErrors(t *testing.T) {
	f := newChatFixture(t)
	if err := f.store.releaseStorageWindowReservationStrict(f.store.db, "owner-9", f.nowISO, AssistantStorageReservationBytes, f.nowISO); err == nil {
		t.Fatal("expected missing bucket error")
	} else if err.Error() != "聊天容量预留数据不一致：2026-03-10 日桶缺失或不足" {
		t.Fatalf("unexpected error text: %v", err)
	}
	if err := f.store.decrementStorageWindowStrict(f.store.db, "owner-9", "2026-03-10", 5, f.nowISO); err == nil {
		t.Fatal("expected missing window error")
	} else if err.Error() != "聊天容量窗口数据不一致：2026-03-10 日桶缺失或不足" {
		t.Fatalf("unexpected error text: %v", err)
	}
}

func TestRecentStorageBytesRetentionWindow(t *testing.T) {
	f := newChatFixture(t)
	// Buckets inside and outside the retention window.
	inserts := []struct {
		bucket string
		bytes  int64
	}{
		{"2026-03-10", 100},
		{"2026-03-05", 40},
		{"2026-01-01", 999},
	}
	for _, insert := range inserts {
		if _, err := f.db.Exec(`INSERT INTO chat_user_storage_windows (system_account_id, bucket_date, content_bytes, reserved_bytes, updated_at)
			VALUES ('owner-1', ?, ?, 0, ?)`, insert.bucket, insert.bytes, f.nowISO); err != nil {
			t.Fatal(err)
		}
	}
	total, err := f.store.recentStorageBytes(f.store.db, "owner-1", f.nowISO, 30)
	if err != nil {
		t.Fatal(err)
	}
	if total != 140 {
		t.Fatalf("expected 140 within the 30-day window, got %d", total)
	}
}
