package gatewaycodex

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Port of the segment payload storage inside
// codex-responses/chat-bridge-state.ts: gzip json segments appended under
// JUHE_AI_CODEX_CONTEXT_ROOT, addressed by (storageKey, offset, sha256,
// sizes) references.

const (
	maxStoredPayloadBytes  = 8 * 1024 * 1024
	maxRestoredInputBytes  = 32 * 1024 * 1024
	codexContextStateTtlMs = 7 * 24 * 60 * 60 * 1000
)

// SegmentStoreConfig carries JUHE_AI_CODEX_CONTEXT_ROOT.
type SegmentStoreConfig struct {
	Root string
}

// SegmentStore stores gzip payload segments on the local filesystem. Writes
// to one storage key are serialized like the Node segmentWriteLocks promise
// chain.
type SegmentStore struct {
	config SegmentStoreConfig

	locksMu sync.Mutex
	locks   map[string]*keyWriteLock
}

type keyWriteLock struct {
	mu sync.Mutex
}

// NewSegmentStore builds the store.
func NewSegmentStore(config SegmentStoreConfig) (*SegmentStore, error) {
	if strings.TrimSpace(config.Root) == "" {
		return nil, fmt.Errorf("JUHE_AI_CODEX_CONTEXT_ROOT 未配置，无法写入 Responses 桥接状态")
	}
	return &SegmentStore{config: config, locks: map[string]*keyWriteLock{}}, nil
}

// WriteSegmentPayload mirrors writeSegmentPayload: compact JSON, gzip,
// sha256 over the compressed bytes, append at the current file end.
func (s *SegmentStore) WriteSegmentPayload(ctx context.Context, sessionID string, payload any, now time.Time) (CodexContextPayloadReference, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return CodexContextPayloadReference{}, err
	}
	if len(raw) > maxStoredPayloadBytes {
		return CodexContextPayloadReference{}, fmt.Errorf("Codex Responses Chat bridge 单条状态超过 %d 字节上限", maxStoredPayloadBytes)
	}
	var compressedBuffer bytes.Buffer
	writer := gzip.NewWriter(&compressedBuffer)
	if _, err := writer.Write(raw); err != nil {
		return CodexContextPayloadReference{}, err
	}
	if err := writer.Close(); err != nil {
		return CodexContextPayloadReference{}, err
	}
	compressed := compressedBuffer.Bytes()
	sum := sha256.Sum256(compressed)
	storageKey := SegmentStorageKey(sessionID, now)
	offset, err := s.appendSegmentBytes(ctx, storageKey, compressed)
	if err != nil {
		return CodexContextPayloadReference{}, err
	}
	return CodexContextPayloadReference{
		StorageKey:          storageKey,
		StorageOffsetBytes:  offset,
		SHA256:              hex.EncodeToString(sum[:]),
		RawSizeBytes:        int64(len(raw)),
		CompressedSizeBytes: int64(len(compressed)),
		Compression:         "gzip",
		SchemaVersion:       2,
	}, nil
}

func (s *SegmentStore) appendSegmentBytes(ctx context.Context, storageKey string, data []byte) (int64, error) {
	lock := s.lockFor(storageKey)
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	finalPath, err := s.resolveStoragePath(storageKey)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return 0, err
	}
	file, err := os.OpenFile(finalPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if _, err := file.WriteAt(data, offset); err != nil {
		return 0, err
	}
	return offset, nil
}

func (s *SegmentStore) lockFor(storageKey string) *keyWriteLock {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	lock, ok := s.locks[storageKey]
	if !ok {
		lock = &keyWriteLock{}
		s.locks[storageKey] = lock
	}
	return lock
}

// ReadSegmentPayload mirrors readSegmentPayload: bounded read at the
// reference offset, sha256 verification, size checks, gzip decode, JSON
// decode.
func (s *SegmentStore) ReadSegmentPayload(row CodexContextPayloadReference) (json.RawMessage, error) {
	if row.CompressedSizeBytes > maxStoredPayloadBytes {
		return nil, fmt.Errorf("Codex Responses Chat bridge 状态文件超过读取上限")
	}
	path, err := s.resolveStoragePath(row.StorageKey)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	compressed := make([]byte, row.CompressedSizeBytes)
	totalRead := 0
	for totalRead < len(compressed) {
		n, readErr := file.ReadAt(compressed[totalRead:], row.StorageOffsetBytes+int64(totalRead))
		totalRead += n
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, readErr
		}
		if n == 0 {
			break
		}
	}
	if totalRead != len(compressed) {
		return nil, fmt.Errorf("Codex Responses Chat bridge 状态文件大小不匹配")
	}
	sum := sha256.Sum256(compressed)
	if hex.EncodeToString(sum[:]) != row.SHA256 {
		return nil, fmt.Errorf("Codex Responses Chat bridge 状态文件校验失败")
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != row.RawSizeBytes || int64(len(raw)) > maxStoredPayloadBytes {
		return nil, fmt.Errorf("Codex Responses Chat bridge 状态文件解压大小异常")
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("Codex Responses Chat bridge 状态文件结构无效")
	}
	return json.RawMessage(raw), nil
}

// resolveStoragePath mirrors resolveStoragePath: backslash normalization,
// '..' rejection and a containment check against the configured root.
func (s *SegmentStore) resolveStoragePath(storageKey string) (string, error) {
	normalizedKey := strings.ReplaceAll(storageKey, "\\", "/")
	normalizedKey = strings.TrimLeft(normalizedKey, "/")
	if strings.Contains(normalizedKey, "..") {
		return "", fmt.Errorf("Responses 桥接状态 storage key 非法")
	}
	root, err := filepath.Abs(s.config.Root)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, normalizedKey))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("Responses 桥接状态 storage key 超出数据目录")
	}
	return target, nil
}

// SegmentStorageKey mirrors segmentStorageKey: hourly segment files per
// session (sessions/<safe>/segments/YYYYMMDDHH.json.gz).
func SegmentStorageKey(sessionID string, now time.Time) string {
	utc := now.UTC()
	hourKey := strings.NewReplacer("-", "", "T", "", ":", "").Replace(utc.Format("2006-01-02T15"))
	return "sessions/" + safePathSegment(sessionID) + "/segments/" + hourKey + ".json.gz"
}

// safePathSegment mirrors safePathSegment: a readable prefix plus a 24 hex
// digest of the full value.
func safePathSegment(input string) string {
	normalized := strings.TrimSpace(input)
	var readable strings.Builder
	for _, r := range normalized {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			readable.WriteRune(r)
			continue
		}
		readable.WriteRune('_')
	}
	readablePrefix := readable.String()
	if len(readablePrefix) > 96 {
		readablePrefix = readablePrefix[:96]
	}
	if readablePrefix == "" {
		readablePrefix = "session"
	}
	sum := sha256.Sum256([]byte(normalized))
	digest := hex.EncodeToString(sum[:])[:24]
	return readablePrefix + "-" + digest
}

// expiresAtFromISO mirrors expiresAtFrom: now + 7 days as ISO (UTC).
func expiresAtFromISO(now time.Time) string {
	return now.UTC().Add(time.Duration(codexContextStateTtlMs) * time.Millisecond).Format("2006-01-02T15:04:05.000Z07:00")
}

// ISOFormat mirrors new Date(...).toISOString().
func ISOFormat(now time.Time) string {
	return now.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// digestText mirrors digestText: sha256 hex of the utf8 text.
func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// randomHex8 mirrors randomUUID().slice(0, 8) for compact ids.
func randomHex8() string {
	var bytes [4]byte
	_, _ = rand.Read(bytes[:])
	return hex.EncodeToString(bytes[:])
}

// base36 mirrors Number.prototype.toString(36) for integers.
func base36(value int64) string {
	if value == 0 {
		return "0"
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	negative := value < 0
	unsigned := uint64(value)
	if negative {
		unsigned = uint64(-value)
	}
	var out []byte
	for unsigned > 0 {
		out = append([]byte{digits[unsigned%36]}, out...)
		unsigned /= 36
	}
	if negative {
		return "-" + string(out)
	}
	return string(out)
}
