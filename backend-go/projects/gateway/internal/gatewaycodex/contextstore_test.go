package gatewaycodex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSQLiteStore(t *testing.T) (*SQLiteShardContextStateStore, string) {
	t.Helper()
	root := t.TempDir()
	store, err := NewSQLiteShardContextStateStore(SQLiteShardStoreConfig{Root: root, ShardCount: 4})
	if err != nil {
		t.Fatalf("NewSQLiteShardContextStateStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, root
}

func TestCodexContextStateShardIndexForKey(t *testing.T) {
	// FNV-1a over UTF-16 code units, modulo the clamped shard count.
	if got := CodexContextStateShardIndexForKey("resp_1", 4); got < 0 || got >= 4 {
		t.Fatalf("shard index %d outside range", got)
	}
	// Shard count clamp mirrors codexContextStateShardCount (1..256).
	if got := CodexContextStateShardIndexForKey("x", 0); got != 0 {
		t.Errorf("empty count normalized index = %d, want 0", got)
	}
	if got := CodexContextStateShardIndexForKey("x", 1000); got > 255 {
		t.Errorf("oversized count index = %d, want < 256", got)
	}
	if got := CodexContextStateShardIndexForKey("x", -3); got != 0 {
		t.Errorf("negative count index = %d, want 0", got)
	}
	// ASCII text hashes over code units equal bytes.
	if got := CodexContextStateShardIndexForKey("abc", 7); got != fnv1aReference([]rune("abc"), 7) {
		t.Errorf("ascii index = %d", got)
	}
}

func fnv1aReference(units []rune, count int) int {
	hash := uint32(2166136261)
	for _, unit := range units {
		hash ^= uint32(unit)
		hash *= 16777619
	}
	return int(hash % uint32(count))
}

func TestCodexContextStateShardFilesCreated(t *testing.T) {
	store, root := newSQLiteStore(t)
	ctx := context.Background()
	row := CodexContextResponseStateIndex{
		CodexContextStateBoundary: CodexContextStateBoundary{
			SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "group-1", ProviderCode: "openai",
		},
		CodexContextPayloadReference: CodexContextPayloadReference{
			StorageKey: "sessions/s/segments/1.json.gz", SHA256: "abc", Compression: "gzip", SchemaVersion: 2,
			RawSizeBytes: 1, CompressedSizeBytes: 1,
		},
		ResponseID: "resp_chat_bridge_1",
		SessionID:  "session-1",
		CreatedAt:  "2026-09-04T00:00:00.000Z",
		UpdatedAt:  "2026-09-04T00:00:00.000Z",
		LastUsedAt: "2026-09-04T00:00:00.000Z",
		ExpiresAt:  "2026-09-11T00:00:00.000Z",
	}
	if err := SaveCodexContextResponseStateIndex(ctx, store, row); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, err := filepath.Glob(filepath.Join(root, "state-*.sqlite3"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("shard files = %v err %v", entries, err)
	}
	read, err := store.ReadResponseStateRow(ctx, row.ResponseID)
	if err != nil || read == nil {
		t.Fatalf("read row: %v %v", read, err)
	}
	if read.SessionID != row.SessionID || read.ProviderCode != "openai" || read.PreviousResponseID != "" {
		t.Errorf("row mismatch: %+v", read)
	}
}

func TestReadCodexContextResponseStateChainOutcomes(t *testing.T) {
	ctx := context.Background()
	base := CodexContextStateBoundary{SystemAccountID: "sys", GroupID: "group", ProviderCode: "openai"}
	makeRow := func(id, previousID string, expiresAt string) CodexContextResponseStateIndex {
		row := CodexContextResponseStateIndex{
			CodexContextStateBoundary: base,
			CodexContextPayloadReference: CodexContextPayloadReference{
				StorageKey: "k", SHA256: "h", Compression: "gzip", SchemaVersion: 2,
				RawSizeBytes: 1, CompressedSizeBytes: 1,
			},
			ResponseID: id,
			SessionID:  "session-a",
			CreatedAt:  "2026-09-04T00:00:00.000Z",
			UpdatedAt:  "2026-09-04T00:00:00.000Z",
			LastUsedAt: "2026-09-04T00:00:00.000Z",
			ExpiresAt:  expiresAt,
		}
		if previousID != "" {
			row.PreviousResponseID = previousID
		}
		return row
	}
	now := "2026-09-05T00:00:00.000Z"
	fresh := "2026-09-20T00:00:00.000Z"

	tests := []struct {
		name        string
		rows        []CodexContextResponseStateIndex
		startAt     string
		maxDepth    int
		wantOutcome string
		wantCount   int
		wantFirstID string
	}{
		{
			name:        "not found",
			startAt:     "resp_missing",
			wantOutcome: CodexContextOutcomeNotFound,
		},
		{
			name:        "single row found",
			rows:        []CodexContextResponseStateIndex{makeRow("resp_a", "", fresh)},
			startAt:     "resp_a",
			wantOutcome: CodexContextOutcomeFound,
			wantCount:   1,
			wantFirstID: "resp_a",
		},
		{
			name: "chain found reversed",
			rows: []CodexContextResponseStateIndex{
				makeRow("resp_c", "resp_b", fresh),
				makeRow("resp_b", "resp_a", fresh),
				makeRow("resp_a", "", fresh),
			},
			startAt:     "resp_c",
			wantOutcome: CodexContextOutcomeFound,
			wantCount:   3,
			wantFirstID: "resp_a",
		},
		{
			name:        "chain broken",
			rows:        []CodexContextResponseStateIndex{makeRow("resp_c", "resp_b", fresh)},
			startAt:     "resp_c",
			wantOutcome: CodexContextOutcomeChainBroken,
		},
		{
			name:        "expired",
			rows:        []CodexContextResponseStateIndex{makeRow("resp_e", "", "2026-09-04T12:00:00.000Z")},
			startAt:     "resp_e",
			wantOutcome: CodexContextOutcomeExpired,
		},
		{
			name: "boundary mismatch",
			rows: []CodexContextResponseStateIndex{func() CodexContextResponseStateIndex {
				row := makeRow("resp_x", "", fresh)
				row.APIKeyID = "other-key"
				return row
			}()},
			startAt:     "resp_x",
			wantOutcome: CodexContextOutcomeBoundaryMismat,
		},
		{
			name: "chain too deep",
			rows: []CodexContextResponseStateIndex{
				makeRow("resp_deep_a", "resp_deep_b", fresh),
				makeRow("resp_deep_b", "", fresh),
			},
			startAt:     "resp_deep_a",
			maxDepth:    1,
			wantOutcome: CodexContextOutcomeChainTooDeep,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subStore, _ := newSQLiteStore(t)
			for _, row := range tt.rows {
				if err := SaveCodexContextResponseStateIndex(ctx, subStore, row); err != nil {
					t.Fatalf("save %s: %v", row.ResponseID, err)
				}
			}
			result, err := ReadCodexContextResponseStateChain(ctx, subStore, ResponseChainReadInput{
				ResponseID: tt.startAt,
				Boundary:   base,
				MaxDepth:   tt.maxDepth,
				Now:        now,
			})
			if err != nil {
				t.Fatalf("read chain: %v", err)
			}
			if result.Outcome != tt.wantOutcome {
				t.Fatalf("outcome = %q, want %q", result.Outcome, tt.wantOutcome)
			}
			if tt.wantOutcome == CodexContextOutcomeFound {
				if len(result.Responses) != tt.wantCount {
					t.Fatalf("responses = %d, want %d", len(result.Responses), tt.wantCount)
				}
				if result.Responses[0].ResponseID != tt.wantFirstID {
					t.Errorf("first = %q, want %q", result.Responses[0].ResponseID, tt.wantFirstID)
				}
				if result.SessionID != "session-a" {
					t.Errorf("sessionId = %q", result.SessionID)
				}
				// Touch refreshes expiry forward.
				touched, err := subStore.ReadResponseStateRow(ctx, tt.startAt)
				if err != nil {
					t.Fatalf("read touched: %v", err)
				}
				if touched.ExpiresAt != "2026-09-05T00:00:00.000Z" {
					t.Errorf("touch did not refresh expiresAt: %q", touched.ExpiresAt)
				}
			}
		})
	}
}

func TestReadCodexContextCompactStateOutcomes(t *testing.T) {
	store, _ := newSQLiteStore(t)
	ctx := context.Background()
	base := CodexContextStateBoundary{SystemAccountID: "sys", GroupID: "group", ProviderCode: "openai"}
	row := CodexContextCompactStateIndex{
		CodexContextStateBoundary: base,
		CodexContextPayloadReference: CodexContextPayloadReference{
			StorageKey: "k", SHA256: "h", Compression: "gzip", SchemaVersion: 2,
			RawSizeBytes: 1, CompressedSizeBytes: 1,
		},
		CompactID:     "cmp_1",
		SessionID:     "session-c",
		SummaryDigest: strings.Repeat("a", 64),
		CreatedAt:     "2026-09-04T00:00:00.000Z",
		UpdatedAt:     "2026-09-04T00:00:00.000Z",
		LastUsedAt:    "2026-09-04T00:00:00.000Z",
		ExpiresAt:     "2026-09-20T00:00:00.000Z",
	}
	if err := SaveCodexContextCompactStateIndex(ctx, store, row); err != nil {
		t.Fatalf("save: %v", err)
	}
	found, err := ReadCodexContextCompactState(ctx, store, CompactStateReadInput{CompactID: "cmp_1", Boundary: base, Now: "2026-09-05T00:00:00.000Z"})
	if err != nil || found.Outcome != CodexContextOutcomeFound || found.Compact == nil {
		t.Fatalf("found = %+v err %v", found, err)
	}
	if found.Compact.SessionID != "session-c" {
		t.Errorf("sessionId = %q", found.Compact.SessionID)
	}
	notFound, err := ReadCodexContextCompactState(ctx, store, CompactStateReadInput{CompactID: "cmp_missing", Boundary: base, Now: "2026-09-05T00:00:00.000Z"})
	if err != nil || notFound.Outcome != CodexContextOutcomeNotFound {
		t.Errorf("notFound = %+v err %v", notFound, err)
	}
	otherBoundary := base
	otherBoundary.ProviderCode = "hybrid"
	mismatch, err := ReadCodexContextCompactState(ctx, store, CompactStateReadInput{CompactID: "cmp_1", Boundary: otherBoundary, Now: "2026-09-05T00:00:00.000Z"})
	if err != nil || mismatch.Outcome != CodexContextOutcomeBoundaryMismat {
		t.Errorf("mismatch = %+v err %v", mismatch, err)
	}
}

func TestSegmentStoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	store, err := NewSegmentStore(SegmentStoreConfig{Root: root})
	if err != nil {
		t.Fatalf("NewSegmentStore: %v", err)
	}
	now := time.Date(2026, 9, 4, 13, 5, 0, 0, time.UTC)
	payload := map[string]any{"summary": "摘要", "value": 42}
	reference, err := store.WriteSegmentPayload(context.Background(), "session/unsafe id", payload, now)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.HasPrefix(reference.StorageKey, "sessions/") || !strings.HasSuffix(reference.StorageKey, "/segments/2026090413.json.gz") {
		t.Errorf("storageKey = %q", reference.StorageKey)
	}
	if reference.Compression != "gzip" || reference.SchemaVersion != 2 || reference.StorageOffsetBytes != 0 {
		t.Errorf("reference = %+v", reference)
	}
	raw, err := store.ReadSegmentPayload(reference)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var decoded map[string]any
	if err := jsonUnmarshalStrict(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["value"].(float64) != 42 {
		t.Errorf("decoded = %+v", decoded)
	}
	// Second write appends at a new offset.
	second, err := store.WriteSegmentPayload(context.Background(), "session/unsafe id", map[string]any{"n": 2}, now)
	if err != nil {
		t.Fatalf("write2: %v", err)
	}
	if second.StorageOffsetBytes <= reference.StorageOffsetBytes {
		t.Errorf("second offset = %d", second.StorageOffsetBytes)
	}
	if _, err := store.ReadSegmentPayload(second); err != nil {
		t.Fatalf("read2: %v", err)
	}
	// Tampered sha fails verification.
	tampered := second
	tampered.SHA256 = strings.Repeat("0", 64)
	if _, err := store.ReadSegmentPayload(tampered); err == nil {
		t.Error("tampered sha read succeeded")
	}
	// Path traversal rejected.
	if _, err := store.WriteSegmentPayload(context.Background(), "..", payload, now); err == nil {
		t.Error("path traversal write succeeded")
	}
	// Storage key values stay on disk within the root.
	if _, err := os.Stat(filepath.Join(root, reference.StorageKey)); err != nil {
		t.Errorf("segment file missing: %v", err)
	}
}

func TestSegmentStorageKeySafeSegment(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	key := SegmentStorageKey("sess!@#$%^&*()id", now)
	const sentinel = "sess__________id-"
	if !strings.HasPrefix(key, "sessions/"+sentinel) {
		t.Errorf("safe segment key = %q", key)
	}
	if strings.ContainsAny(key[len("sessions/"):], "!@#$%^&*()") {
		t.Errorf("unsafe characters survived: %q", key)
	}
	empty := SegmentStorageKey("   ", now)
	if !strings.HasPrefix(empty, "sessions/session-") {
		t.Errorf("empty session key = %q", empty)
	}
	long := SegmentStorageKey(strings.Repeat("a", 200), now)
	segment := long[len("sessions/"):]
	if index := strings.Index(segment, "/"); segment[:index][96] != '-' {
		t.Errorf("long prefix not truncated: %q", segment[:index])
	}
}
