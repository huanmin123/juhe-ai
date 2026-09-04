package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// jsonRaw builds a checkpoint entry content payload.
func jsonRaw(value string) json.RawMessage { return json.RawMessage(value) }

// stubResult implements sql.Result for captured statements.
type stubResult struct{}

func (stubResult) LastInsertId() (int64, error) { return 0, nil }
func (stubResult) RowsAffected() (int64, error) { return 0, nil }

// stubQueryer captures executed statements for PG-mode SQL rendering tests.
// Only Exec is exercised by ensurePostgresChatMessagePartitions; the row APIs
// exist to satisfy the queryer port.
type stubQueryer struct {
	statements []string
}

func (s *stubQueryer) QueryRow(string, ...any) *sql.Row { return nil }

func (s *stubQueryer) Exec(query string, _ ...any) (sql.Result, error) {
	s.statements = append(s.statements, query)
	return stubResult{}, nil
}

func (s *stubQueryer) Query(string, ...any) (*sql.Rows, error) { return nil, nil }

func TestEnsurePostgresChatMessagePartitionsSQLiteNoop(t *testing.T) {
	f := newChatFixture(t)
	if f.store.Postgres() {
		t.Fatal("fixture must be sqlite mode")
	}
	if err := f.store.ensurePostgresChatMessagePartitions(f.store.db, f.nowISO); err != nil {
		t.Fatalf("sqlite mode must be a no-op, got %v", err)
	}
}

func TestEnsurePostgresChatMessagePartitionsSQLRendering(t *testing.T) {
	stub := &stubQueryer{}
	store := &Store{pg: true}
	current := "2026-03-10T08:00:00.000Z"
	if err := store.ensurePostgresChatMessagePartitions(stub, current); err != nil {
		t.Fatal(err)
	}
	if len(stub.statements) != 2 {
		t.Fatalf("expected create for current + next day, got %d statements", len(stub.statements))
	}
	wantFirst := "\n      CREATE TABLE IF NOT EXISTS juhe_chat.\"chat_messages_20260310\"\n      PARTITION OF juhe_chat.chat_messages\n      FOR VALUES FROM ('2026-03-10') TO ('2026-03-11')\n    "
	if stub.statements[0] != wantFirst {
		t.Fatalf("unexpected first statement:\n%s", stub.statements[0])
	}
	wantSecond := "\n      CREATE TABLE IF NOT EXISTS juhe_chat.\"chat_messages_20260311\"\n      PARTITION OF juhe_chat.chat_messages\n      FOR VALUES FROM ('2026-03-11') TO ('2026-03-12')\n    "
	if stub.statements[1] != wantSecond {
		t.Fatalf("unexpected second statement:\n%s", stub.statements[1])
	}
	// Memoized process-wide: the same day issues nothing.
	if err := store.ensurePostgresChatMessagePartitions(stub, current); err != nil {
		t.Fatal(err)
	}
	if len(stub.statements) != 2 {
		t.Fatalf("expected memoization to skip creation, got %d statements", len(stub.statements))
	}
	// A later day still creates its look-ahead pair.
	if err := store.ensurePostgresChatMessagePartitions(stub, "2026-03-12T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	if len(stub.statements) != 4 {
		t.Fatalf("expected two more partitions, got %d statements", len(stub.statements))
	}
	if !strings.Contains(stub.statements[2], `"chat_messages_20260312"`) || !strings.Contains(stub.statements[3], `"chat_messages_20260313"`) {
		t.Fatalf("unexpected look-ahead statements: %q, %q", stub.statements[2], stub.statements[3])
	}
	// Invalid timestamps are rejected with the Node message.
	if err := store.ensurePostgresChatMessagePartitions(stub, "not-a-date"); err == nil || err.Error() != "AI 问答消息时间无效：not-a-date" {
		t.Fatalf("expected invalid timestamp error, got %v", err)
	}
}

func TestChatMessagePartitionDateKeyTable(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"2026-03-10T08:00:00.000Z", "20260310", true},
		{" 2026-03-10", "20260310", true},
		{"2026-13-01T00:00:00Z", "", false},
		{"2026-00-10T00:00:00Z", "", false},
		{"2026-03-32T00:00:00Z", "", false},
		{"bad", "", false},
	}
	for _, testCase := range cases {
		got, ok := chatMessagePartitionDateKeyFromISO(testCase.input)
		if ok != testCase.ok || (ok && got != testCase.want) {
			t.Fatalf("chatMessagePartitionDateKeyFromISO(%q) = %q,%v want %q,%v", testCase.input, got, ok, testCase.want, testCase.ok)
		}
	}
	// 2026 is not a leap year: Feb 29 must be rejected.
	if _, ok := chatMessagePartitionDateKeyFromISO("2026-02-29T00:00:00Z"); ok {
		t.Fatal("2026-02-29 must be rejected")
	}
	if _, ok := chatMessagePartitionDateKeyFromISO("2028-02-29T00:00:00Z"); !ok {
		t.Fatal("2028-02-29 must be accepted")
	}
}

func TestPostgresBindRendering(t *testing.T) {
	pgStore := &Store{pg: true}
	sqliteStore := &Store{}
	rendered := pgStore.bind("WHERE a = ? AND b IN (?, ?, ?) AND c = ?")
	if rendered != "WHERE a = $1 AND b IN ($2, $3, $4) AND c = $5" {
		t.Fatalf("unexpected pg rendering: %s", rendered)
	}
	if sqliteStore.bind("WHERE a = ? AND b = ?") != "WHERE a = ? AND b = ?" {
		t.Fatal("sqlite rendering must keep placeholders")
	}
	if pgStore.table("chat_messages") != "juhe_chat.chat_messages" {
		t.Fatal("pg must qualify with the juhe_chat schema")
	}
	if sqliteStore.table("chat_messages") != "chat_messages" {
		t.Fatal("sqlite must use bare table names")
	}
	if pgStore.lockSuffix() != " FOR UPDATE" || sqliteStore.lockSuffix() != "" {
		t.Fatal("lock suffix must be dialect aware")
	}
}

// requestCompaction drives the compaction state machine into compact_pending.
func requestCompaction(t *testing.T, f *chatFixture, conversationID, ownerID string, expectedRevision, through int64) bool {
	t.Helper()
	accepted, err := f.store.RequestContextCompaction(RequestCompactionInput{
		ConversationID: conversationID, SystemAccountID: ownerID,
		ExpectedRevision: expectedRevision, SourceThroughSequence: through, Now: f.nowISO,
	})
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

// contextRevision reads the live context revision for a conversation.
func contextRevision(t *testing.T, f *chatFixture, conversationID, ownerID string) int64 {
	t.Helper()
	head, err := f.store.GetContextHead(conversationID, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	return head.ContextRevision
}

func digest64(seed string) string {
	out := make([]byte, 0, 64)
	for len(out) < 64 {
		out = append(out, seed...)
	}
	return string(out[:64])
}

func TestContextCompactionStateMachine(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	f.seedTurns("owner-1", "chat_conv_a", 2)

	// Two completed turns are compactable through sequence 2; each accepted
	// turn bumped context_revision, so the expected revision is read live.
	headBefore := contextRevision(t, f, "chat_conv_a", "owner-1")
	if requestCompaction(t, f, "chat_conv_a", "owner-1", headBefore, 2) != true {
		t.Fatal("two completed turns must be compactable through sequence 2")
	}
	head, err := f.store.GetContextHead("chat_conv_a", "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	if head.ContextState != StateCompactPending {
		t.Fatalf("expected compact_pending, got %s", head.ContextState)
	}

	claim, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1",
		ExpectedRevision: headBefore, SourceThroughSequence: 2,
		Now: f.nowISO, StaleClaimBefore: f.nowISO,
	})
	if err != nil {
		t.Fatal(err)
	}
	if claim == nil {
		t.Fatal("expected a claim")
	}
	if claim.ClaimID == "" || claim.SourceFromSequence != 1 || claim.SourceThroughSequence != 2 || claim.AttemptCount != 1 {
		t.Fatalf("unexpected claim: %+v", claim)
	}
	// A second claim on the compacting head with a fresh stale window fails.
	staleClaim, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1",
		ExpectedRevision: headBefore, SourceThroughSequence: 2,
		Now: f.nowISO, StaleClaimBefore: "2020-01-01T00:00:00.000Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if staleClaim != nil {
		t.Fatal("compacting head must not re-claim before the stale window")
	}

	// Node requires progress through the claim window before install.
	if progressed, err := f.store.RecordCompactionProgress(RecordCompactionProgressInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ClaimID: claim.ClaimID,
		ThroughSequence: 2, EarliestExpiresAt: "2026-03-11T08:00:00.000Z", Now: f.nowISO,
	}); err != nil || !progressed {
		t.Fatalf("expected progress to apply: %v %v", progressed, err)
	}
	expiry := "2026-03-11T08:00:00.000Z"
	installed, err := f.store.InstallContextCheckpoint(InstallCheckpointInput{
		ClaimID: claim.ClaimID, ConversationID: "chat_conv_a", SystemAccountID: "owner-1",
		SourceRevision: headBefore, SourceThroughSequence: 2,
		ExpiresAt: expiry, PayloadDigest: digest64("ab"),
		EstimatedInputTokens: int64Ptr(120), UpstreamInputTokens: int64Ptr(130),
		EffectiveContextLimitTokens: int64Ptr(1000),
		RequestBodyBytes:            2048,
		ModelID:                     "gpt-5", EndpointFamily: "responses", PromptVersion: "v1",
		Entries: []CheckpointEntryInput{
			{Kind: "verbatim", Content: jsonRaw(`{"role":"user","text":"问题1"}`), Provenance: "user", TrustLevel: "untrusted", TokenCount: int64Ptr(10)},
			{Kind: "verbatim", Content: jsonRaw(`{"role":"assistant","text":"回答1"}`), Provenance: "assistant", TrustLevel: "assistant_derived"},
		},
		Now: f.nowISO,
	})
	if err != nil {
		t.Fatal(err)
	}
	if installed.status != "active" || installed.version != headBefore+1 || installed.sourceRevision != headBefore ||
		installed.entryThroughSequence != 2 || installed.entryFromSequence != 1 {
		t.Fatalf("unexpected installed checkpoint: %+v", installed)
	}
	head, err = f.store.GetContextHead("chat_conv_a", "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	if head.ContextState != StateReady || head.CompactedThroughSequence != 2 || head.ActiveCheckpointID == nil {
		t.Fatalf("expected ready head with checkpoint: %+v", head)
	}
	if head.UsageEstimated {
		t.Fatal("upstream tokens present → usage must not be estimated")
	}

	// Loading the model context now comes from checkpoint entries only.
	loaded, err := f.store.LoadModelContext("chat_conv_a", "owner-1", f.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || !loaded.Complete || loaded.TruncatedAt != nil {
		t.Fatalf("expected complete load: %+v", loaded)
	}
	// sourceThrough=2 compacts only the first turn; the second turn remains
	// a complete suffix pair (Node margin: through <= next_sequence_no - 3).
	if len(loaded.Entries) != 2 || len(loaded.Suffix) != 2 {
		t.Fatalf("expected 2 checkpoint entries and one suffix pair: %d/%d", len(loaded.Entries), len(loaded.Suffix))
	}
	entryBytes := int64(len(`{"role":"user","text":"问题1"}`) + len(`{"role":"assistant","text":"回答1"}`))
	if loaded.LoadedBytes < entryBytes {
		t.Fatalf("loaded bytes must cover the entries: %d < %d", loaded.LoadedBytes, entryBytes)
	}
	if loaded.Checkpoint == nil || loaded.Checkpoint.id != installed.id {
		t.Fatalf("expected checkpoint in result: %+v", loaded.Checkpoint)
	}
}

func TestLoadModelContextTruncationBoundaries(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	f.seedTurns("owner-1", "chat_conv_a", 2)

	// Row budget: one entry max → checkpoint_entries truncation with one entry.
	headBefore := contextRevision(t, f, "chat_conv_a", "owner-1")
	compactionRequest(t, f, "chat_conv_a", "owner-1", headBefore, 2)
	claim := claimCompaction(t, f, "chat_conv_a", "owner-1", headBefore, 2)
	if progressed, err := f.store.RecordCompactionProgress(RecordCompactionProgressInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ClaimID: claim.ClaimID,
		ThroughSequence: 2, EarliestExpiresAt: "2026-03-11T08:00:00.000Z", Now: f.nowISO,
	}); err != nil || !progressed {
		t.Fatalf("expected progress to apply: %v %v", progressed, err)
	}
	expiry := "2026-03-11T08:00:00.000Z"
	entryContent := `{"text":"x"}`
	_, err := f.store.InstallContextCheckpoint(InstallCheckpointInput{
		ClaimID: claim.ClaimID, ConversationID: "chat_conv_a", SystemAccountID: "owner-1",
		SourceRevision: headBefore, SourceThroughSequence: 2,
		ExpiresAt: expiry, PayloadDigest: digest64("cd"),
		RequestBodyBytes: 10, ModelID: "gpt-5", EndpointFamily: "responses", PromptVersion: "v1",
		Entries: []CheckpointEntryInput{
			{Kind: "verbatim", Content: jsonRaw(entryContent), Provenance: "user", TrustLevel: "untrusted"},
			{Kind: "verbatim", Content: jsonRaw(entryContent), Provenance: "assistant", TrustLevel: "assistant_derived"},
		},
		Now: f.nowISO,
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := f.store.LoadModelContext("chat_conv_a", "owner-1", f.nowISO, 1, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TruncatedAt == nil || *loaded.TruncatedAt != "checkpoint_entries" {
		t.Fatalf("expected checkpoint_entries truncation, got %+v", loaded.TruncatedAt)
	}
	if len(loaded.Entries) != 1 || loaded.Complete {
		t.Fatalf("expected exactly one entry and incomplete: %d %v", len(loaded.Entries), loaded.Complete)
	}

	// Byte budget: only the first entry fits.
	loaded, err = f.store.LoadModelContext("chat_conv_a", "owner-1", f.nowISO, 512, len(entryContent))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != 1 || loaded.TruncatedAt == nil || *loaded.TruncatedAt != "checkpoint_entries" {
		t.Fatalf("expected byte-limit truncation after first entry: %+v", loaded)
	}

	// No checkpoint: suffix pair loading with a row budget that keeps pairs.
	detach, err := f.db.Exec(`UPDATE chat_conversations SET active_checkpoint_id = NULL,
		compacted_through_sequence = 0, context_state = 'ready' WHERE id = 'chat_conv_a'`)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := detach.RowsAffected(); affected != 1 {
		t.Fatal("detach failed")
	}
	loaded, err = f.store.LoadModelContext("chat_conv_a", "owner-1", f.nowISO, 4, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Complete || len(loaded.Entries) != 0 || len(loaded.Suffix) != 4 {
		t.Fatalf("expected 2 suffix pairs: complete=%v entries=%d suffix=%d", loaded.Complete, len(loaded.Entries), len(loaded.Suffix))
	}
	if loaded.Suffix[0].role != "user" || loaded.Suffix[1].role != "assistant" || loaded.Suffix[0].turnID != loaded.Suffix[1].turnID {
		t.Fatalf("suffix pairs must stay ordered: %+v", loaded.Suffix)
	}
	// contentBytes falls back to text + blocks JSON lengths.
	for _, message := range loaded.Suffix {
		if message.contentBytes < int64(len(message.contentText)) {
			t.Fatalf("contentBytes must cover the text: %+v", message)
		}
	}
	// Odd row budget floors to whole pairs: 1 row budget → 0 pairs loaded.
	loaded, err = f.store.LoadModelContext("chat_conv_a", "owner-1", f.nowISO, 1+4-4, 16*1024*1024) // budget 1 → but checkpoint missing, suffix only
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Suffix) != 0 || loaded.Complete {
		t.Fatalf("odd row budget must drop the trailing pair: %+v", loaded)
	}

	// Suffix byte budget truncation.
	loaded, err = f.store.LoadModelContext("chat_conv_a", "owner-1", f.nowISO, 512, 8)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TruncatedAt == nil || *loaded.TruncatedAt != "suffix_messages" || len(loaded.Suffix) != 0 {
		t.Fatalf("expected suffix_messages truncation: %+v", loaded)
	}

	// Unknown conversation → nil.
	missing, err := f.store.LoadModelContext("chat_conv_missing", "owner-1", f.nowISO, 8, 1024)
	if err != nil || missing != nil {
		t.Fatalf("expected nil for missing conversation, got %+v %v", missing, err)
	}
	// Validation errors carry the Node messages.
	_, err = f.store.LoadModelContext("chat_conv_a", "owner-1", f.nowISO, 0, 1024)
	if err == nil || err.Error() != "maxRows 必须是 1..512 的整数" {
		t.Fatalf("unexpected maxRows error: %v", err)
	}
	_, err = f.store.LoadModelContext("chat_conv_a", "owner-1", f.nowISO, 8, 0)
	if err == nil || err.Error() != "maxBytes 必须是 1..16777216 的整数" {
		t.Fatalf("unexpected maxBytes error: %v", err)
	}
}

// Small helpers so the truncation test stays linear.
func compactionRequest(t *testing.T, f *chatFixture, conversationID, ownerID string, revision, through int64) {
	t.Helper()
	if !requestCompaction(t, f, conversationID, ownerID, revision, through) {
		t.Fatal("compaction request must be accepted")
	}
}

func claimCompaction(t *testing.T, f *chatFixture, conversationID, ownerID string, revision, through int64) *ContextCompactionClaim {
	t.Helper()
	claim, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: conversationID, SystemAccountID: ownerID,
		ExpectedRevision: revision, SourceThroughSequence: through,
		Now: f.nowISO, StaleClaimBefore: f.nowISO,
	})
	if err != nil {
		t.Fatal(err)
	}
	if claim == nil {
		t.Fatal("expected claim")
	}
	return claim
}

func TestCompactionProgressReleaseAndFailure(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	f.seedTurns("owner-1", "chat_conv_a", 2)
	headBefore := contextRevision(t, f, "chat_conv_a", "owner-1")
	compactionRequest(t, f, "chat_conv_a", "owner-1", headBefore, 2)
	claim := claimCompaction(t, f, "chat_conv_a", "owner-1", headBefore, 2)

	// Progress beyond the claim window fails.
	advanced, err := f.store.RecordCompactionProgress(RecordCompactionProgressInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ClaimID: claim.ClaimID,
		ThroughSequence: 3, EarliestExpiresAt: "2026-03-11T08:00:00.000Z", Now: f.nowISO,
	})
	if err != nil {
		t.Fatal(err)
	}
	if advanced {
		t.Fatal("progress beyond claim window must not apply")
	}
	progressed, err := f.store.RecordCompactionProgress(RecordCompactionProgressInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ClaimID: claim.ClaimID,
		ThroughSequence: 2, EarliestExpiresAt: "2026-03-11T08:00:00.000Z", Now: f.nowISO,
	})
	if err != nil || !progressed {
		t.Fatalf("expected progress to apply: %v %v", progressed, err)
	}

	// Release flips back to compact_pending and clears claim columns.
	released, err := f.store.ReleaseCompactionClaim("chat_conv_a", "owner-1", claim.ClaimID, f.nowISO)
	if err != nil || !released {
		t.Fatalf("expected release: %v %v", released, err)
	}
	head, _ := f.store.GetContextHead("chat_conv_a", "owner-1")
	if head.ContextState != StateCompactPending {
		t.Fatalf("expected compact_pending after release, got %s", head.ContextState)
	}

	// Fail with retryAt: retry allowed only after retryAt.
	claim2 := claimCompaction(t, f, "chat_conv_a", "owner-1", head.ContextRevision, 2)
	failed, err := f.store.FailCompaction(FailCompactionInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ClaimID: claim2.ClaimID,
		ErrorCode: "chat_context_compaction_stale", RetryAt: stringPtr(f.nowISO), Now: f.nowISO,
	})
	if err != nil || !failed {
		t.Fatalf("expected failure apply: %v %v", failed, err)
	}
	head, _ = f.store.GetContextHead("chat_conv_a", "owner-1")
	if head.ContextState != StateCompactFailed || head.ContextErrorCode == nil || *head.ContextErrorCode != "chat_context_compaction_stale" {
		t.Fatalf("expected compact_failed with code: %+v", head)
	}
	if head.ContextAttemptCount != 1 {
		t.Fatalf("expected attempt count 1, got %d", head.ContextAttemptCount)
	}
	// Retry not allowed before retryAt (now == retryAt → allowed by <=).
	if !requestCompaction(t, f, "chat_conv_a", "owner-1", head.ContextRevision, 2) {
		t.Fatal("retry after retryAt must be accepted")
	}
	// FailPending on a compact_pending head.
	head, _ = f.store.GetContextHead("chat_conv_a", "owner-1")
	pendingFailed, err := f.store.FailPendingCompaction(FailPendingCompactionInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ExpectedRevision: head.ContextRevision,
		ErrorCode: "chat_context_compaction_stale", Now: f.nowISO,
	})
	if err != nil || !pendingFailed {
		t.Fatalf("expected pending failure apply: %v %v", pendingFailed, err)
	}
	// Invalid error codes are rejected.
	if _, err := f.store.FailPendingCompaction(FailPendingCompactionInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1", ExpectedRevision: 99,
		ErrorCode: "bad\n 코드", Now: f.nowISO,
	}); err == nil || err.Error() != "errorCode 无效" {
		t.Fatalf("expected invalid errorCode error, got %v", err)
	}
}

func TestInstallCheckpointConflictGuards(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_a", "owner-1")
	f.seedTurns("owner-1", "chat_conv_a", 2)
	base := InstallCheckpointInput{
		ConversationID: "chat_conv_a", SystemAccountID: "owner-1",
		SourceRevision: contextRevision(t, f, "chat_conv_a", "owner-1"), SourceThroughSequence: 2,
		ExpiresAt: "2026-03-11T08:00:00.000Z", PayloadDigest: digest64("ef"),
		RequestBodyBytes: 10, ModelID: "gpt-5", EndpointFamily: "responses", PromptVersion: "v1",
		Entries: []CheckpointEntryInput{
			{Kind: "verbatim", Content: jsonRaw(`{"a":1}`), Provenance: "user", TrustLevel: "untrusted"},
		},
		Now: f.nowISO,
	}
	// Installing without a claim → conflict.
	if _, err := f.store.InstallContextCheckpoint(base); err == nil || err.Error() != "聊天上下文已变化，当前压缩结果不能安装" {
		t.Fatalf("expected conflict error, got %v", err)
	}
	// Invalid source revision/digest/expiry validations.
	invalidDigest := base
	invalidDigest.PayloadDigest = "zz"
	if _, err := f.store.InstallContextCheckpoint(invalidDigest); err == nil || err.Error() != "payloadDigest 必须是 SHA-256 十六进制摘要" {
		t.Fatalf("expected digest error, got %v", err)
	}
	expired := base
	expired.ExpiresAt = "2020-01-01T00:00:00.000Z"
	if _, err := f.store.InstallContextCheckpoint(expired); err == nil || err.Error() != "不能安装已过期的 checkpoint" {
		t.Fatalf("expected expired error, got %v", err)
	}
	noEntries := base
	noEntries.Entries = nil
	if _, err := f.store.InstallContextCheckpoint(noEntries); err == nil || err.Error() != "checkpoint entry 数量必须在 1..256 之间" {
		t.Fatalf("expected entries count error, got %v", err)
	}
	badKind := base
	badKind.Entries = []CheckpointEntryInput{{Kind: "mystery", Content: jsonRaw(`{}`), Provenance: "user", TrustLevel: "untrusted"}}
	if _, err := f.store.InstallContextCheckpoint(badKind); err == nil || err.Error() != "未知 checkpoint entry 类型：mystery" {
		t.Fatalf("expected kind error, got %v", err)
	}
}

func TestGenerationErrorClassification(t *testing.T) {
	upstream := ClassifyChatGenerationErrorByCode("upstream_http_error")
	if upstream.Code != GenErrUpstreamHTTP || upstream.Message != "模型服务请求失败，请稍后重试" {
		t.Fatalf("unexpected classification: %+v", upstream)
	}
	unknown := ClassifyChatGenerationErrorByCode("whatever")
	if unknown.Code != GenErrInternal || unknown.Message != "生成任务异常结束，请重新发送" {
		t.Fatalf("unexpected fallback: %+v", unknown)
	}
	network := ClassifyUnknownChatGenerationError(errors.New("ECONNRESET"))
	if network.Code != GenErrUpstreamStream {
		t.Fatalf("expected upstream_stream_failed for network codes, got %+v", network)
	}
	generic := ClassifyUnknownChatGenerationError(errors.New("数据库繁忙"))
	if generic.Code != GenErrInternal || generic.Message != "生成任务异常结束，请重新发送；详情：数据库繁忙" {
		t.Fatalf("unexpected generic classification: %+v", generic)
	}
	sanitized := sanitizeChatDiagnosticMessage("failed https://user:pass@upstream.example.com/v1?key=abc123 Bearer sk-tokens_here sk-proj12345678")
	if strings.Contains(sanitized, "user:pass") || strings.Contains(sanitized, "abc123") ||
		strings.Contains(sanitized, "sk-tokens_here") || strings.Contains(sanitized, "sk-proj12345678") {
		t.Fatalf("secrets must be redacted: %s", sanitized)
	}
	if !strings.Contains(sanitized, "[upstream-url]") || !strings.Contains(sanitized, "[REDACTED]") {
		t.Fatalf("expected redaction placeholders: %s", sanitized)
	}
	long := strings.Repeat("长", 1300)
	truncated := sanitizeChatDiagnosticMessage(long)
	if runeCount := len([]rune(truncated)); runeCount != 1200 || !strings.HasSuffix(truncated, "…") {
		t.Fatalf("expected 1200-rune ellipsis truncation, got %d", runeCount)
	}
}
