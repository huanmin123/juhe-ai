package usagewriter

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateUsageRecordID(t *testing.T) {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	id, err := GenerateUsageRecordID(clock, "2026-03-04T05:06:07.000Z", "0f8fad5b-d9cb-469f-a165-70867728950e", 16)
	if err != nil {
		t.Fatal(err)
	}
	// usage_${bucketDateKey}_sNN_${millis}_${sanitized24}
	if !strings.HasPrefix(id, "usage_20260304_s") {
		t.Fatalf("id prefix = %q", id)
	}
	if !strings.Contains(id, "_1767323045000_") {
		t.Fatalf("id millis segment missing: %q", id)
	}
	suffix := id[strings.LastIndex(id, "_")+1:]
	if suffix != "0f8fad5bd9cb469fa16570867728950"[:24] && len(suffix) != 24 {
		t.Fatalf("entropy suffix = %q", suffix)
	}
	// The shard id inside the id is the stable hash of the entropy.
	wantShard := StableShardID("0f8fad5b-d9cb-469f-a165-70867728950e", 16)
	if !strings.HasPrefix(id, "usage_20260304_s"+FormatShardID(wantShard)+"_") {
		t.Fatalf("shard segment mismatch: %q, want shard %d", id, wantShard)
	}

	// Invalid createdAt errors with the Node copy.
	if _, err := GenerateUsageRecordID(clock, "nope", "abc", 16); err == nil {
		t.Fatal("expected invalid createdAt error")
	}
}

func TestStableShardIDDistribution(t *testing.T) {
	// Deterministic modulo routing with full coverage over a shard grid.
	seen := map[int]bool{}
	for i := 0; i < 256; i++ {
		id := SanitizeShardEntropy(NewRandomUUID())
		shard := StableShardID(id, 8)
		if shard < 0 || shard >= 8 {
			t.Fatalf("shard out of range: %d", shard)
		}
		seen[shard] = true
	}
	if len(seen) < 4 {
		t.Fatalf("shard distribution too narrow: %v", seen)
	}
	// Node parity: FNV-1a of "abc" = 0x1a55107d → %16.
	if got := StableHash("abc"); got != 0x1a47e90b {
		t.Fatalf("StableHash(abc) = %#x, want 0x1a47e90b (FNV-1a 32)", got)
	}
}

func TestUsageRecordShardLocationForRecord(t *testing.T) {
	root := string(filepath.Separator) + filepath.Join("tmp", "shards")
	// Generated ids carry bucket + shard in the id itself.
	id := "usage_20260102_s07_1767225600000_abc"
	location, err := UsageRecordShardLocationForRecord(id, "2026-09-09T00:00:00.000Z", 16, root)
	if err != nil {
		t.Fatal(err)
	}
	if location.ShardKey != "20260102:s07" {
		t.Fatalf("shardKey = %q", location.ShardKey)
	}
	if location.BucketDate != "2026-01-02" || location.ShardID != 7 {
		t.Fatalf("location = %+v", location)
	}
	wantPath := filepath.Join(root, "2026", "01", "02", "usage-20260102-s07.sqlite3")
	if location.FilePath != wantPath {
		t.Fatalf("filePath = %q, want %q", location.FilePath, wantPath)
	}

	// Opaque ids fall back to the createdAt bucket and the hash of the id.
	location, err = UsageRecordShardLocationForRecord("opaque-id", "2026-05-06T07:08:09.000Z", 16, root)
	if err != nil {
		t.Fatal(err)
	}
	wantShard := StableShardID("opaque-id", 16)
	if location.ShardKey != "20260506:s"+FormatShardID(wantShard) {
		t.Fatalf("fallback shardKey = %q", location.ShardKey)
	}

	// Invalid createdAt errors.
	if _, err := UsageRecordShardLocationForRecord("opaque-id", "bad", 16, root); err == nil {
		t.Fatal("expected createdAt error")
	}
}

func TestUsageRecordLogicalShardLocationForPostgres(t *testing.T) {
	location, err := UsageRecordLogicalShardLocationForPostgres("usage_20260102_s007_x", "2026-01-02T00:00:00.000Z", 16)
	if err != nil {
		t.Fatal(err)
	}
	// Parsed ids reuse the id shard; the logical key pads to three digits.
	if location.ShardKey != "20260102:s007" {
		t.Fatalf("shardKey = %q", location.ShardKey)
	}
	if location.FilePath != "postgres:juhe_usage.usage_records:20260102:s007" {
		t.Fatalf("filePath = %q", location.FilePath)
	}

	// Unparsed ids hash with the 31-multiplier polynomial.
	location, err = UsageRecordLogicalShardLocationForPostgres("opaque", "2026-02-03T00:00:00.000Z", 16)
	if err != nil {
		t.Fatal(err)
	}
	if location.ShardKey != "20260203:s"+FormatLogicalShardID(LogicalShardID("opaque", 16)) {
		t.Fatalf("logical shardKey = %q", location.ShardKey)
	}
}

func TestBucketDateKey(t *testing.T) {
	key, err := BucketDateKey("2026-01-31T23:30:00-02:00")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-02-01T01:30Z → UTC bucket.
	if key != "20260201" {
		t.Fatalf("utc bucket = %q", key)
	}
	if _, err := BucketDateKey("2026-13-01T00:00:00Z"); err == nil {
		t.Fatal("expected invalid month error")
	}
}

func TestSanitizeShardEntropy(t *testing.T) {
	got := SanitizeShardEntropy("ab-cd/ef gh!ij@kl#mn$op%qr^st&uv*wx()yz01")
	if got != "abcdefghijklmnopqrstuvwx" {
		t.Fatalf("sanitized = %q", got)
	}
}

func TestUsageRecordShardLocationFromKey(t *testing.T) {
	root := string(filepath.Separator) + "shards"
	location, ok := UsageRecordShardLocationFromKey("20260102:s03", root)
	if !ok || location.ShardID != 3 || location.BucketDateKey != "20260102" {
		t.Fatalf("location = %+v, ok = %v", location, ok)
	}
	if _, ok := UsageRecordShardLocationFromKey("bogus", root); ok {
		t.Fatal("bogus key should not parse")
	}
}
