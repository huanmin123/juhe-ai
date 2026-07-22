package redis

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

const (
	SessionAffinityFormatVersion    = "go-revision-v1"
	SessionAffinityMaxTokenBytes    = 512
	SessionAffinityMaxValueBytes    = 4 * 1024
	SessionAffinityMaxRevisionBytes = 64
	SessionAffinityMaxTTL           = 24 * time.Hour
)

var ErrInvalidSessionAffinityRecord = errors.New("Redis 会话亲和记录格式无效")

const compareAndSetSessionAffinityLua = `
local current = redis.call('GET', KEYS[1])
local expected_revision = ARGV[1]
if expected_revision == '' then
  if current then
    return 0
  end
else
  if not current then
    return 0
  end
  local separator = string.find(current, '\n', 1, true)
  if not separator then
    return 0
  end
  local current_revision = string.sub(current, 1, separator - 1)
  if current_revision ~= expected_revision then
    return 0
  end
end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
return 1
`

const compareAndDeleteSessionAffinityLua = `
local current = redis.call('GET', KEYS[1])
if not current then
  return 0
end
local separator = string.find(current, '\n', 1, true)
if not separator then
  return 0
end
local current_revision = string.sub(current, 1, separator - 1)
if current_revision ~= ARGV[1] then
  return 0
end
redis.call('DEL', KEYS[1])
return 1
`

const touchSessionAffinityLua = `
local current = redis.call('GET', KEYS[1])
if not current then
  return 0
end
local separator = string.find(current, '\n', 1, true)
if not separator then
  return 0
end
local current_revision = string.sub(current, 1, separator - 1)
if current_revision ~= ARGV[1] then
  return 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`

var (
	compareAndSetSessionAffinityScript    = goredis.NewScript(compareAndSetSessionAffinityLua)
	compareAndDeleteSessionAffinityScript = goredis.NewScript(compareAndDeleteSessionAffinityLua)
	touchSessionAffinityScript            = goredis.NewScript(touchSessionAffinityLua)
)

type SessionAffinityRecord struct {
	Revision string
	Value    []byte
}

// SessionAffinityStore owns only the Redis token-to-value primitive. Gateway
// ordering, binding payloads and fallback policy remain in the caller.
//
// The go-revision-v1 key/value format is intentionally not compatible with the
// weaker Node raw-JSON CAS format. It must only be enabled after an atomic
// gateway owner cutover; existing short-lived Node affinity may then rebuild.
type SessionAffinityStore struct {
	keyPrefix        string
	newRevision      func() string
	get              func(context.Context, string) ([]byte, error)
	set              func(context.Context, string, []byte, time.Duration) error
	compareAndSet    func(context.Context, string, string, []byte, time.Duration) (bool, error)
	compareAndDelete func(context.Context, string, string) (bool, error)
	touch            func(context.Context, string, string, time.Duration) (bool, error)
}

func NewSessionAffinityStore(client *Client) (*SessionAffinityStore, error) {
	if client == nil || client.client == nil {
		return nil, fmt.Errorf("Redis cache client 不能为空")
	}
	store := &SessionAffinityStore{
		keyPrefix:   client.Key("session-affinity", "binding"),
		newRevision: uuid.NewString,
	}
	store.get = func(ctx context.Context, key string) ([]byte, error) {
		value, err := client.client.Get(ctx, key).Bytes()
		if errors.Is(err, goredis.Nil) {
			return nil, ErrNotFound
		}
		return value, err
	}
	store.set = func(ctx context.Context, key string, value []byte, ttl time.Duration) error {
		return client.client.Set(ctx, key, value, ttl).Err()
	}
	store.compareAndSet = func(ctx context.Context, key, expectedRevision string, value []byte, ttl time.Duration) (bool, error) {
		result, err := compareAndSetSessionAffinityScript.Run(
			ctx,
			client.client,
			[]string{key},
			expectedRevision,
			value,
			ttl.Milliseconds(),
		).Int64()
		return result == 1, err
	}
	store.compareAndDelete = func(ctx context.Context, key, expectedRevision string) (bool, error) {
		result, err := compareAndDeleteSessionAffinityScript.Run(
			ctx,
			client.client,
			[]string{key},
			expectedRevision,
		).Int64()
		return result == 1, err
	}
	store.touch = func(ctx context.Context, key, expectedRevision string, ttl time.Duration) (bool, error) {
		result, err := touchSessionAffinityScript.Run(
			ctx,
			client.client,
			[]string{key},
			expectedRevision,
			ttl.Milliseconds(),
		).Int64()
		return result == 1, err
	}
	return store, nil
}

func (s *SessionAffinityStore) Get(ctx context.Context, token string) (SessionAffinityRecord, error) {
	if err := validateSessionAffinityContext(ctx); err != nil {
		return SessionAffinityRecord{}, err
	}
	key, err := s.redisKey(token)
	if err != nil {
		return SessionAffinityRecord{}, err
	}
	if s.get == nil {
		return SessionAffinityRecord{}, fmt.Errorf("Redis 会话亲和读取器未初始化")
	}
	raw, err := s.get(ctx, key)
	if err != nil {
		return SessionAffinityRecord{}, fmt.Errorf("读取 Redis 会话亲和记录: %w", err)
	}
	record, err := decodeSessionAffinityRecord(raw)
	if err != nil {
		return SessionAffinityRecord{}, err
	}
	return record, nil
}

func (s *SessionAffinityStore) Set(
	ctx context.Context,
	token string,
	value []byte,
	ttl time.Duration,
) (SessionAffinityRecord, error) {
	if err := validateSessionAffinityContext(ctx); err != nil {
		return SessionAffinityRecord{}, err
	}
	key, err := s.redisKey(token)
	if err != nil {
		return SessionAffinityRecord{}, err
	}
	if err := validateSessionAffinityValueAndTTL(value, ttl); err != nil {
		return SessionAffinityRecord{}, err
	}
	if s.set == nil || s.newRevision == nil {
		return SessionAffinityRecord{}, fmt.Errorf("Redis 会话亲和写入器未初始化")
	}
	record, raw, err := s.newRecord(value)
	if err != nil {
		return SessionAffinityRecord{}, err
	}
	if err := s.set(ctx, key, raw, ttl); err != nil {
		return SessionAffinityRecord{}, fmt.Errorf("写入 Redis 会话亲和记录: %w", err)
	}
	return record, nil
}

// CompareAndSet creates a record when expectedRevision is empty. Otherwise it
// replaces the record only when the currently stored revision still matches.
func (s *SessionAffinityStore) CompareAndSet(
	ctx context.Context,
	token string,
	expectedRevision string,
	value []byte,
	ttl time.Duration,
) (SessionAffinityRecord, bool, error) {
	if err := validateSessionAffinityContext(ctx); err != nil {
		return SessionAffinityRecord{}, false, err
	}
	key, err := s.redisKey(token)
	if err != nil {
		return SessionAffinityRecord{}, false, err
	}
	if err := validateSessionAffinityValueAndTTL(value, ttl); err != nil {
		return SessionAffinityRecord{}, false, err
	}
	if expectedRevision != "" {
		if err := validateSessionAffinityRevision(expectedRevision); err != nil {
			return SessionAffinityRecord{}, false, err
		}
	}
	if s.compareAndSet == nil || s.newRevision == nil {
		return SessionAffinityRecord{}, false, fmt.Errorf("Redis 会话亲和 CAS 写入器未初始化")
	}
	record, raw, err := s.newRecord(value)
	if err != nil {
		return SessionAffinityRecord{}, false, err
	}
	swapped, err := s.compareAndSet(ctx, key, expectedRevision, raw, ttl)
	if err != nil {
		return SessionAffinityRecord{}, false, fmt.Errorf("CAS 写入 Redis 会话亲和记录: %w", err)
	}
	if !swapped {
		return SessionAffinityRecord{}, false, nil
	}
	return record, true, nil
}

func (s *SessionAffinityStore) CompareAndDelete(
	ctx context.Context,
	token string,
	expectedRevision string,
) (bool, error) {
	if err := validateSessionAffinityContext(ctx); err != nil {
		return false, err
	}
	key, err := s.redisKey(token)
	if err != nil {
		return false, err
	}
	if err := validateSessionAffinityRevision(expectedRevision); err != nil {
		return false, err
	}
	if s.compareAndDelete == nil {
		return false, fmt.Errorf("Redis 会话亲和条件删除器未初始化")
	}
	deleted, err := s.compareAndDelete(ctx, key, expectedRevision)
	if err != nil {
		return false, fmt.Errorf("条件删除 Redis 会话亲和记录: %w", err)
	}
	return deleted, nil
}

func (s *SessionAffinityStore) Touch(
	ctx context.Context,
	token string,
	expectedRevision string,
	ttl time.Duration,
) (bool, error) {
	if err := validateSessionAffinityContext(ctx); err != nil {
		return false, err
	}
	key, err := s.redisKey(token)
	if err != nil {
		return false, err
	}
	if err := validateSessionAffinityRevision(expectedRevision); err != nil {
		return false, err
	}
	if err := validateSessionAffinityTTL(ttl); err != nil {
		return false, err
	}
	if s.touch == nil {
		return false, fmt.Errorf("Redis 会话亲和续期器未初始化")
	}
	touched, err := s.touch(ctx, key, expectedRevision, ttl)
	if err != nil {
		return false, fmt.Errorf("续期 Redis 会话亲和记录: %w", err)
	}
	return touched, nil
}

func (s *SessionAffinityStore) newRecord(value []byte) (SessionAffinityRecord, []byte, error) {
	revision := s.newRevision()
	if err := validateSessionAffinityRevision(revision); err != nil {
		return SessionAffinityRecord{}, nil, fmt.Errorf("生成 Redis 会话亲和 revision: %w", err)
	}
	valueCopy := append([]byte(nil), value...)
	record := SessionAffinityRecord{Revision: revision, Value: valueCopy}
	return record, encodeSessionAffinityRecord(record), nil
}

func (s *SessionAffinityStore) redisKey(token string) (string, error) {
	if s == nil || strings.TrimSpace(s.keyPrefix) == "" {
		return "", fmt.Errorf("Redis 会话亲和存储未初始化")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("会话亲和 token 不能为空")
	}
	if len(token) > SessionAffinityMaxTokenBytes {
		return "", fmt.Errorf("会话亲和 token 不能超过 %d 字节", SessionAffinityMaxTokenBytes)
	}
	return strings.TrimRight(s.keyPrefix, ":") + ":" + keyHash(token), nil
}

func validateSessionAffinityContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("context 不能为空")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func validateSessionAffinityValueAndTTL(value []byte, ttl time.Duration) error {
	if len(value) == 0 {
		return fmt.Errorf("会话亲和值不能为空")
	}
	if len(value) > SessionAffinityMaxValueBytes {
		return fmt.Errorf("会话亲和值不能超过 %d 字节", SessionAffinityMaxValueBytes)
	}
	return validateSessionAffinityTTL(ttl)
}

func validateSessionAffinityTTL(ttl time.Duration) error {
	if ttl < time.Millisecond {
		return fmt.Errorf("会话亲和 TTL 不能小于 1ms")
	}
	if ttl > SessionAffinityMaxTTL {
		return fmt.Errorf("会话亲和 TTL 不能超过 %s", SessionAffinityMaxTTL)
	}
	return nil
}

func validateSessionAffinityRevision(revision string) error {
	if strings.TrimSpace(revision) == "" {
		return fmt.Errorf("会话亲和 revision 不能为空")
	}
	if len(revision) > SessionAffinityMaxRevisionBytes {
		return fmt.Errorf("会话亲和 revision 不能超过 %d 字节", SessionAffinityMaxRevisionBytes)
	}
	if strings.ContainsAny(revision, "\r\n") {
		return fmt.Errorf("会话亲和 revision 不能包含换行符")
	}
	return nil
}

func encodeSessionAffinityRecord(record SessionAffinityRecord) []byte {
	result := make([]byte, 0, len(record.Revision)+1+len(record.Value))
	result = append(result, record.Revision...)
	result = append(result, '\n')
	result = append(result, record.Value...)
	return result
}

func decodeSessionAffinityRecord(raw []byte) (SessionAffinityRecord, error) {
	separator := bytes.IndexByte(raw, '\n')
	if separator <= 0 || separator > SessionAffinityMaxRevisionBytes {
		return SessionAffinityRecord{}, ErrInvalidSessionAffinityRecord
	}
	revision := string(raw[:separator])
	if err := validateSessionAffinityRevision(revision); err != nil {
		return SessionAffinityRecord{}, fmt.Errorf("%w: %v", ErrInvalidSessionAffinityRecord, err)
	}
	value := raw[separator+1:]
	if len(value) == 0 || len(value) > SessionAffinityMaxValueBytes {
		return SessionAffinityRecord{}, ErrInvalidSessionAffinityRecord
	}
	return SessionAffinityRecord{
		Revision: revision,
		Value:    append([]byte(nil), value...),
	}, nil
}
