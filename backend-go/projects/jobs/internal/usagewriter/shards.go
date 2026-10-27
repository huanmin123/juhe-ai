package usagewriter

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Usage record shard routing mirroring backend/src/storage/
// usage-record-shards.ts. The shard-id format is owned by the writer slice
// (per the G17 port declaration in gateway/internal/gatewayusage/ports.go).

// UsageRecordShardLocation mirrors UsageRecordShardLocation.
type UsageRecordShardLocation struct {
	ShardKey      string
	BucketDate    string
	BucketDateKey string
	ShardID       int
	FilePath      string
}

// DefaultUsageShardCount mirrors the JUHE_AI_USAGE_SHARD_COUNT default (16).
const DefaultUsageShardCount = 16

var usageRecordShardIDPattern = regexp.MustCompile(`^usage_(\d{8})_s(\d+)_`)

// ShardCount mirrors usageRecordShardCount: at least 1.
func ShardCount(configured int) int {
	if configured < 1 {
		return 1
	}
	return configured
}

// GenerateUsageRecordID mirrors generateUsageRecordId(createdAt, entropy):
// `usage_${bucketDateKey}_sNN_${Date.now()}_${entropy sanitized to 24}`.
// The shard id is the stable hash of the entropy modulo shardCount, so all
// rows sharing one generated id prefix route to the same shard file. The
// clock is injected (Node Date.now()).
func GenerateUsageRecordID(clock Clock, createdAt string, entropy string, shardCount int) (string, error) {
	bucketDateKey, err := BucketDateKey(createdAt)
	if err != nil {
		return "", err
	}
	shardID := StableShardID(entropy, shardCount)
	return fmt.Sprintf("usage_%s_s%s_%d_%s", bucketDateKey, FormatShardID(shardID), clock.Now().UnixMilli(), SanitizeShardEntropy(entropy)), nil
}

// SanitizeShardEntropy mirrors entropy.replace(/[^a-zA-Z0-9]/g, ”).slice(0, 24).
func SanitizeShardEntropy(entropy string) string {
	var builder strings.Builder
	for _, r := range entropy {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			if builder.Len() >= 24 {
				break
			}
		}
	}
	return builder.String()
}

// UsageRecordLogicalShardLocationForPostgres mirrors
// usageRecordLogicalShardLocationForPostgres: a logical location (no file)
// for the partitioned juhe_usage.usage_records table. Note the Node source
// intentionally pads the logical shard id to three digits here (versus two
// digits in the SQLite path).
func UsageRecordLogicalShardLocationForPostgres(id string, createdAt string, shardCount int) (UsageRecordShardLocation, error) {
	bucketDateKey := ""
	shardID := 0
	if match := usageRecordShardIDPattern.FindStringSubmatch(strings.TrimSpace(id)); match != nil {
		bucketDateKey = match[1]
		parsed, err := strconv.Atoi(match[2])
		if err == nil && parsed >= 0 {
			shardID = parsed
		} else {
			shardID = LogicalShardID(id, shardCount)
		}
	} else {
		resolved, err := BucketDateKey(createdAt)
		if err != nil {
			return UsageRecordShardLocation{}, err
		}
		bucketDateKey = resolved
		shardID = LogicalShardID(id, shardCount)
	}
	bucketDate := bucketDateKey[0:4] + "-" + bucketDateKey[4:6] + "-" + bucketDateKey[6:8]
	shardKey := bucketDateKey + ":s" + FormatLogicalShardID(shardID)
	return UsageRecordShardLocation{
		ShardKey:      shardKey,
		BucketDate:    bucketDate,
		BucketDateKey: bucketDateKey,
		ShardID:       shardID,
		FilePath:      "postgres:juhe_usage.usage_records:" + shardKey,
	}, nil
}

// StableShardID mirrors stableShardId: FNV-1a 32-bit hash modulo shardCount.
func StableShardID(value string, shardCount int) int {
	return StableHash(value) % ShardCount(shardCount)
}

// StableHash mirrors stableHash: FNV-1a 32-bit over UTF-16 code units is
// approximated by bytes; the Node source hashes charCodeAt over ASCII
// entropy, so for ASCII inputs both agree byte-for-byte.
func StableHash(value string) int {
	hash := uint32(2166136261)
	for index := 0; index < len(value); index++ {
		hash ^= uint32(value[index])
		hash *= 16777619
	}
	return int(hash)
}

// LogicalShardID mirrors usageRecordLogicalShardId (the Postgres variant
// that hashes with the 31-multiplier polynomial).
func LogicalShardID(value string, shardCount int) int {
	hash := uint32(0)
	for index := 0; index < len(value); index++ {
		hash = hash*31 + uint32(value[index])
	}
	return int(hash) % ShardCount(shardCount)
}

// FormatShardID mirrors formatShardId (two digits, SQLite path).
func FormatShardID(shardID int) string {
	if shardID < 0 {
		shardID = 0
	}
	return fmt.Sprintf("%02d", shardID)
}

// FormatLogicalShardID mirrors formatUsageRecordLogicalShardId (three
// digits, Postgres logical path).
func FormatLogicalShardID(shardID int) string {
	if shardID < 0 {
		shardID = 0
	}
	return fmt.Sprintf("%03d", shardID)
}

// BucketDateKey mirrors bucketDateKeyFromIso: YYYYMMDD in UTC from an
// RFC3339 instant. Invalid instants error like the Node source.
func BucketDateKey(createdAt string) (string, error) {
	parsed, ok := parseRFC3339Instant(createdAt)
	if !ok {
		return "", fmt.Errorf("usage record createdAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	parsed = parsed.UTC()
	return fmt.Sprintf("%04d%02d%02d", parsed.Year(), int(parsed.Month()), parsed.Day()), nil
}

// BucketDateKeyFromClock mirrors bucketDateKeyFromIso() with no argument
// (nowIso fallback).
func BucketDateKeyFromClock(clock Clock) string {
	parsed := clock.Now().UTC()
	return fmt.Sprintf("%04d%02d%02d", parsed.Year(), int(parsed.Month()), parsed.Day())
}
