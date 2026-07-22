package redis

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const accountConcurrencyBatchSize = 100

var invalidAccountConcurrencyNamespaceChars = regexp.MustCompile(`[^a-zA-Z0-9_.:-]+`)

const loadAccountConcurrencyLua = `
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)

local function hdel_expired(metadata_key, expired)
  local index = 1
  while index <= #expired do
    local last = math.min(index + 199, #expired)
    redis.call('HDEL', metadata_key, unpack(expired, index, last))
    index = last + 1
  end
end

local expired = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now_ms)
if #expired > 0 then
  hdel_expired(KEYS[5], expired)
end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now_ms)

if redis.call('ZCARD', KEYS[1]) == 0 then
  redis.call('DEL', KEYS[1])
end
if redis.call('ZCARD', KEYS[2]) == 0 then
  redis.call('DEL', KEYS[2])
end
if redis.call('ZCARD', KEYS[3]) == 0 then
  redis.call('DEL', KEYS[3])
end
if redis.call('HLEN', KEYS[5]) == 0 then
  redis.call('DEL', KEYS[5])
end
return redis.call('ZCARD', KEYS[4])
`

var loadAccountConcurrencyScript = goredis.NewScript(loadAccountConcurrencyLua)

type AccountConcurrencyReader struct {
	run           func(context.Context, []string, int64) (int64, error)
	rootNamespace string
}

func NewAccountConcurrencyReader(client *Client, rootNamespace string) (*AccountConcurrencyReader, error) {
	if client == nil || client.client == nil {
		return nil, fmt.Errorf("redis state client is required")
	}
	namespace, err := normalizeAccountConcurrencyNamespace(rootNamespace)
	if err != nil {
		return nil, err
	}
	return &AccountConcurrencyReader{
		rootNamespace: namespace,
		run: func(ctx context.Context, keys []string, _ int64) (int64, error) {
			return loadAccountConcurrencyScript.Run(
				ctx,
				client.client,
				keys,
			).Int64()
		},
	}, nil
}

func (r *AccountConcurrencyReader) LoadAccountCurrentConcurrencyByIDs(
	ctx context.Context,
	accountIDs []string,
	now time.Time,
) (map[string]int, error) {
	if r == nil || r.run == nil {
		return nil, fmt.Errorf("account concurrency reader is required")
	}
	ids := uniqueAccountConcurrencyIDs(accountIDs)
	result := make(map[string]int, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	nowMillis := now.UnixMilli()
	for start := 0; start < len(ids); start += accountConcurrencyBatchSize {
		end := min(start+accountConcurrencyBatchSize, len(ids))
		batch := ids[start:end]
		type loadResult struct {
			accountID string
			count     int64
			err       error
		}
		results := make(chan loadResult, len(batch))
		var wait sync.WaitGroup
		wait.Add(len(batch))
		for _, accountID := range batch {
			go func() {
				defer wait.Done()
				count, err := r.run(ctx, r.accountConcurrencyKeys(accountID), nowMillis)
				results <- loadResult{accountID: accountID, count: count, err: err}
			}()
		}
		wait.Wait()
		close(results)
		for item := range results {
			if item.err != nil {
				return nil, fmt.Errorf("load account %q current concurrency: %w", item.accountID, item.err)
			}
			if item.count < 0 {
				return nil, fmt.Errorf("load account %q current concurrency returned negative count", item.accountID)
			}
			result[item.accountID] = int(item.count)
		}
	}
	return result, nil
}

func (r *AccountConcurrencyReader) accountConcurrencyKeys(accountID string) []string {
	prefix := "juhe-ai:" + r.rootNamespace + ":account-concurrency-v2:" + accountID + ":"
	total := prefix + "total"
	return []string{
		total,
		prefix + "text",
		prefix + "image",
		total,
		prefix + "metadata",
	}
}

func normalizeAccountConcurrencyNamespace(value string) (string, error) {
	normalized := invalidAccountConcurrencyNamespaceChars.ReplaceAllString(strings.TrimSpace(value), "_")
	normalized = strings.Trim(normalized, "_")
	if normalized == "" {
		return "", fmt.Errorf("redis root namespace is required")
	}
	if len(normalized) > 64 {
		return "", fmt.Errorf("redis root namespace must be at most 64 characters")
	}
	return normalized, nil
}

func uniqueAccountConcurrencyIDs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
