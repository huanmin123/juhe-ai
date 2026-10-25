package gatewaycodex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
