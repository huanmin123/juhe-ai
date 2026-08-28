package circuitruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	accountCircuitRuntimeIndexVersion   = "1"
	accountCircuitRuntimeIndexOwnerMode = "go-runtime-state-v1"

	DefaultAccountCircuitRuntimeIndexScanCount       = 200
	DefaultAccountCircuitRuntimeIndexLockTTL         = 2 * time.Minute
	DefaultAccountCircuitRuntimeIndexMaxPages        = 1000
	DefaultAccountCircuitRuntimeIndexMaxFields       = 100000
	DefaultAccountCircuitRuntimeIndexMaxBytes        = 64 << 20
	DefaultAccountCircuitRuntimeIndexMaxScopeMembers = 100000
)

const beginAccountCircuitRuntimeIndexLua = `
local scope_runtime_key = KEYS[1]
local runtime_scopes_key = KEYS[2]
local account_runtimes_key = KEYS[3]
local runtime_accounts_key = KEYS[4]
local meta_key = KEYS[5]
local lock_key = KEYS[6]
local states_key = KEYS[7]
local escalation_key = KEYS[8]
local owner = ARGV[1]
local epoch = ARGV[2]
local function require_type(key, expected)
  local actual = redis.call('TYPE', key)['ok']
  return actual == 'none' or actual == expected
end
if redis.call('GET', lock_key) ~= owner then return redis.error_reply('account circuit runtime index lock is lost') end
if not require_type(scope_runtime_key, 'hash') or not require_type(runtime_scopes_key, 'hash')
  or not require_type(account_runtimes_key, 'hash') or not require_type(runtime_accounts_key, 'hash')
  or not require_type(meta_key, 'hash') or not require_type(states_key, 'hash') or not require_type(escalation_key, 'hash') then
  return redis.error_reply('invalid account circuit Redis key type')
end
redis.call('DEL', scope_runtime_key, runtime_scopes_key, account_runtimes_key, runtime_accounts_key)
redis.call('HSET', meta_key,
  'version', '1', 'status', 'building', 'buildEpoch', epoch,
  'phase', 'states', 'cursor', '0', 'ownerMode', 'pending',
  'stateCount', '0', 'evidenceCount', '0', 'revisionCount', '0', 'auditAtMs', '0')
return 1
`

const renewAccountCircuitRuntimeIndexLockLua = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`

const applyAccountCircuitRuntimeIndexPageLua = `
local scope_runtime_key = KEYS[1]
local runtime_scopes_key = KEYS[2]
local account_runtimes_key = KEYS[3]
local runtime_accounts_key = KEYS[4]
local meta_key = KEYS[5]
local lock_key = KEYS[6]
local source_key = KEYS[7]
local owner = ARGV[1]
local epoch = ARGV[2]
local phase = ARGV[3]
local next_cursor = ARGV[4]
local items = cjson.decode(ARGV[5])
local max_scope_members = tonumber(ARGV[6])

local function require_type(key, expected)
  local actual = redis.call('TYPE', key)['ok']
  return actual == 'none' or actual == expected
end
local function array(raw)
  if not raw then return {} end
  local values = cjson.decode(raw)
  if type(values) ~= 'table' or #values > max_scope_members then return nil end
  local count = 0
  for key, value in pairs(values) do
    if type(key) ~= 'number' or key < 1 or key ~= math.floor(key) or type(value) ~= 'string' then return nil end
    count = count + 1
  end
  if count ~= #values then return nil end
  return values
end
local function contains(values, target)
  for _, value in ipairs(values or {}) do if value == target then return true end end
  return false
end
local function canonical(values)
  local seen, result = {}, {}
  for _, value in ipairs(values or {}) do
    if type(value) ~= 'string' or value == '' then return nil end
    if not seen[value] then seen[value] = true; table.insert(result, value) end
  end
  table.sort(result)
  if #result > max_scope_members then return nil end
  return result
end
local function add(values, target)
  table.insert(values, target)
  return canonical(values)
end
local function persist_array(key, field, values)
  if not values or #values == 0 then redis.call('HDEL', key, field) else redis.call('HSET', key, field, cjson.encode(values)) end
end
if redis.call('GET', lock_key) ~= owner then return redis.error_reply('account circuit runtime index lock is lost') end
if not require_type(scope_runtime_key, 'hash') or not require_type(runtime_scopes_key, 'hash')
  or not require_type(account_runtimes_key, 'hash') or not require_type(runtime_accounts_key, 'hash')
  or not require_type(meta_key, 'hash') or not require_type(source_key, 'hash') then
  return redis.error_reply('invalid account circuit Redis key type')
end
if redis.call('HGET', meta_key, 'version') ~= '1' or redis.call('HGET', meta_key, 'status') ~= 'building'
  or redis.call('HGET', meta_key, 'buildEpoch') ~= epoch or redis.call('HGET', meta_key, 'phase') ~= phase then
  return redis.error_reply('account circuit runtime index build epoch is invalid')
end
if type(items) ~= 'table' then return redis.error_reply('invalid account circuit runtime index page') end

-- Precompute the complete page mutation before the first write. Redis does not
-- roll back Lua writes when a later redis.error_reply is returned.
local planned_scope_runtime = {}
local planned_runtime_account = {}
local planned_scopes = {}
local planned_runtimes = {}
for _, item in ipairs(items) do
  if type(item) ~= 'table' or type(item['field']) ~= 'string' or type(item['runtime']) ~= 'string'
    or type(item['account']) ~= 'string' or type(item['source']) ~= 'string' then
    return redis.error_reply('invalid account circuit runtime index item')
  end
  if redis.call('HGET', source_key, item['field']) ~= item['source'] then
    return redis.error_reply('account circuit runtime source changed during backfill')
  end
  local mapped_account = redis.call('HGET', runtime_accounts_key, item['runtime'])
  if mapped_account and mapped_account ~= item['account'] then return redis.error_reply('invalid account circuit runtime index') end
  if planned_runtime_account[item['runtime']] and planned_runtime_account[item['runtime']] ~= item['account'] then return redis.error_reply('invalid account circuit runtime index') end
  planned_runtime_account[item['runtime']] = item['account']
  if not planned_scopes[item['runtime']] then planned_scopes[item['runtime']] = array(redis.call('HGET', runtime_scopes_key, item['runtime'])) end
  if not planned_runtimes[item['account']] then planned_runtimes[item['account']] = array(redis.call('HGET', account_runtimes_key, item['account'])) end
  if not planned_scopes[item['runtime']] or not planned_runtimes[item['account']] then return redis.error_reply('invalid account circuit runtime index') end
  if phase == 'states' then
    local mapped_runtime = redis.call('HGET', scope_runtime_key, item['field'])
    if mapped_runtime and mapped_runtime ~= item['runtime'] then return redis.error_reply('invalid account circuit runtime index') end
    if planned_scope_runtime[item['field']] and planned_scope_runtime[item['field']] ~= item['runtime'] then return redis.error_reply('invalid account circuit runtime index') end
    planned_scope_runtime[item['field']] = item['runtime']
    planned_scopes[item['runtime']] = add(planned_scopes[item['runtime']], item['field'])
    if not planned_scopes[item['runtime']] then return redis.error_reply('invalid account circuit runtime index') end
  end
  planned_runtimes[item['account']] = add(planned_runtimes[item['account']], item['runtime'])
  if not planned_runtimes[item['account']] then return redis.error_reply('invalid account circuit runtime index') end
end

for target_scope, target_runtime in pairs(planned_scope_runtime) do redis.call('HSET', scope_runtime_key, target_scope, target_runtime) end
for target_runtime, target_account in pairs(planned_runtime_account) do redis.call('HSET', runtime_accounts_key, target_runtime, target_account) end
for target_runtime, scopes in pairs(planned_scopes) do if phase == 'states' then persist_array(runtime_scopes_key, target_runtime, scopes) end end
for target_account, runtimes in pairs(planned_runtimes) do persist_array(account_runtimes_key, target_account, runtimes) end
local count_field = phase == 'states' and 'stateCount' or 'evidenceCount'
local current = tonumber(redis.call('HGET', meta_key, count_field) or '0')
redis.call('HSET', meta_key, 'cursor', next_cursor, count_field, tostring(current + #items))
return #items
`

const applyAccountCircuitDispatchRevisionPageLua = `
local revisions_key = KEYS[1]
local meta_key = KEYS[2]
local lock_key = KEYS[3]
local lock_token = ARGV[1]
local epoch = ARGV[2]
local next_cursor = ARGV[3]
local items = cjson.decode(ARGV[4])
local function require_type(key, expected) local actual = redis.call('TYPE', key)['ok']; return actual == 'none' or actual == expected end
if redis.call('GET', lock_key) ~= lock_token then return redis.error_reply('account circuit runtime index lock is lost') end
if not require_type(revisions_key, 'hash') or not require_type(meta_key, 'hash') then return redis.error_reply('invalid account circuit Redis key type') end
if redis.call('HGET', meta_key, 'version') ~= '1' or redis.call('HGET', meta_key, 'status') ~= 'building'
  or redis.call('HGET', meta_key, 'buildEpoch') ~= epoch or redis.call('HGET', meta_key, 'phase') ~= 'revisions' then
  return redis.error_reply('account circuit runtime index build epoch is invalid')
end
if type(items) ~= 'table' then return redis.error_reply('invalid account circuit dispatch revision page') end
for _, item in ipairs(items) do
  local incoming = type(item) == 'table' and tonumber(item['dispatchRevision']) or nil
  local account_id = type(item) == 'table' and item['accountId'] or nil
  if type(account_id) ~= 'string' or not incoming or incoming < 1 or incoming ~= math.floor(incoming) then return redis.error_reply('invalid account circuit dispatch revision item') end
  local current_raw = redis.call('HGET', revisions_key, account_id); local current = current_raw and tonumber(current_raw) or nil
  if current_raw and (not current or current < 1 or current ~= math.floor(current)) then return redis.error_reply('invalid revision tombstone') end
  if current and current > incoming then return redis.error_reply('account circuit revision tombstone is ahead of durable account') end
end
for _, item in ipairs(items) do redis.call('HSET', revisions_key, item['accountId'], tostring(item['dispatchRevision'])) end
local count = tonumber(redis.call('HGET', meta_key, 'revisionCount') or '0')
redis.call('HSET', meta_key, 'cursor', next_cursor, 'revisionCount', tostring(count + #items))
return #items
`

const advanceAccountCircuitRuntimeIndexPhaseLua = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return redis.error_reply('account circuit runtime index lock is lost') end
if redis.call('HGET', KEYS[2], 'version') ~= '1' or redis.call('HGET', KEYS[2], 'status') ~= 'building'
  or redis.call('HGET', KEYS[2], 'buildEpoch') ~= ARGV[2] or redis.call('HGET', KEYS[2], 'phase') ~= ARGV[3] then
  return redis.error_reply('account circuit runtime index build epoch is invalid')
end
redis.call('HSET', KEYS[2], 'phase', ARGV[4], 'cursor', '0')
return 1
`

const finalizeAccountCircuitRuntimeIndexLua = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return redis.error_reply('account circuit runtime index lock is lost') end
if redis.call('HGET', KEYS[2], 'version') ~= '1' or redis.call('HGET', KEYS[2], 'status') ~= 'building'
  or redis.call('HGET', KEYS[2], 'buildEpoch') ~= ARGV[2] or redis.call('HGET', KEYS[2], 'phase') ~= 'auditing' then
  return redis.error_reply('account circuit runtime index build epoch is invalid')
end
redis.call('HSET', KEYS[2], 'status', 'ready', 'ownerMode', 'go-runtime-state-v1', 'auditAtMs', ARGV[3], 'phase', 'ready', 'cursor', '0')
return 1
`

const invalidateAccountCircuitRuntimeIndexLua = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
if redis.call('HGET', KEYS[2], 'buildEpoch') ~= ARGV[2] then return 0 end
redis.call('HSET', KEYS[2], 'status', 'invalid', 'ownerMode', 'pending', 'error', ARGV[3], 'phase', 'invalid')
return 1
`

var (
	beginAccountCircuitRuntimeIndexScript         = goredis.NewScript(beginAccountCircuitRuntimeIndexLua)
	renewAccountCircuitRuntimeIndexLockScript     = goredis.NewScript(renewAccountCircuitRuntimeIndexLockLua)
	applyAccountCircuitRuntimeIndexPageScript     = goredis.NewScript(applyAccountCircuitRuntimeIndexPageLua)
	applyAccountCircuitDispatchRevisionPageScript = goredis.NewScript(applyAccountCircuitDispatchRevisionPageLua)
	advanceAccountCircuitRuntimeIndexPhaseScript  = goredis.NewScript(advanceAccountCircuitRuntimeIndexPhaseLua)
	finalizeAccountCircuitRuntimeIndexScript      = goredis.NewScript(finalizeAccountCircuitRuntimeIndexLua)
	invalidateAccountCircuitRuntimeIndexScript    = goredis.NewScript(invalidateAccountCircuitRuntimeIndexLua)
)

type GatewayAccountCircuitRuntimeIndexBackfillInput struct {
	OwnerID         string
	LockTTL         time.Duration
	ScanCount       int
	MaxPages        int
	MaxFields       int
	MaxBytes        int
	MaxScopeMembers int
}

type GatewayAccountCircuitRuntimeIndexBackfillResult struct {
	Epoch         string
	StateCount    int
	EvidenceCount int
	RevisionCount int
	Pages         int
	AuditedAt     time.Time
}

type accountCircuitDispatchRevisionIndexItem struct {
	AccountID        string `json:"accountId"`
	DispatchRevision int64  `json:"dispatchRevision"`
}

type accountCircuitRuntimeIndexItem struct {
	Field    string `json:"field"`
	Runtime  string `json:"runtime"`
	Account  string `json:"account"`
	Source   string `json:"source"`
	Revision int64  `json:"-"`
}

type accountCircuitRuntimeIndexStateEntry struct {
	State             accountCircuitRuntimeStateWire `json:"state"`
	ClosedExpiresAtMS *int64                         `json:"closedExpiresAtMs,omitempty"`
	ReplayOrder       []string                       `json:"replayOrder"`
}

type accountCircuitRuntimeIndexEvidence struct {
	DispatchRevision string `json:"dispatchRevision"`
	Scopes           []struct {
		ScopeKey              string `json:"scopeKey"`
		IncidentID            string `json:"incidentId"`
		EvidenceID            string `json:"evidenceId"`
		ConfirmedFailureCount int    `json:"confirmedFailureCount"`
		ObservedAtMS          int64  `json:"observedAtMs"`
	} `json:"scopes"`
}

type AccountCircuitRuntimeIndexBackfiller struct {
	client         *Client
	keys           accountCircuitRevisionKeys
	revisionReader GatewayAccountCircuitDispatchRevisionReader
	now            func() time.Time
}

func (b *AccountCircuitRuntimeIndexBackfiller) WithDispatchRevisionReader(reader GatewayAccountCircuitDispatchRevisionReader) *AccountCircuitRuntimeIndexBackfiller {
	b.revisionReader = reader
	return b
}

func NewAccountCircuitRuntimeIndexBackfiller(client *Client) (*AccountCircuitRuntimeIndexBackfiller, error) {
	if client == nil || client.client == nil {
		return nil, fmt.Errorf("Redis state client is required")
	}
	keys, err := accountCircuitRevisionRedisKeys(client.namespace, AccountCircuitRedisStoreName)
	if err != nil {
		return nil, err
	}
	return &AccountCircuitRuntimeIndexBackfiller{client: client, keys: keys, now: time.Now}, nil
}

func (b *AccountCircuitRuntimeIndexBackfiller) WithNow(now func() time.Time) *AccountCircuitRuntimeIndexBackfiller {
	if now != nil {
		b.now = now
	}
	return b
}

// BackfillGatewayAccountCircuitRuntimeIndex is intentionally one-shot. It is
// only safe after every legacy Node/Go state writer is drained; the lock guards
// competing Go maintenance jobs but cannot fence a legacy writer.
func (b *AccountCircuitRuntimeIndexBackfiller) BackfillGatewayAccountCircuitRuntimeIndex(ctx context.Context, input GatewayAccountCircuitRuntimeIndexBackfillInput) (result GatewayAccountCircuitRuntimeIndexBackfillResult, err error) {
	if b == nil || b.client == nil || b.client.client == nil {
		return result, fmt.Errorf("account circuit runtime index backfiller is required")
	}
	if ctx == nil {
		return result, fmt.Errorf("account circuit runtime index context is required")
	}
	if b.revisionReader == nil {
		return result, fmt.Errorf("account circuit durable dispatch revision reader is required")
	}
	input, err = normalizeAccountCircuitRuntimeIndexBackfillInput(input)
	if err != nil {
		return result, err
	}
	epoch, err := newAccountCircuitRuntimeIndexEpoch()
	if err != nil {
		return result, err
	}
	lockToken := input.OwnerID + ":" + epoch
	if ok, acquireErr := b.client.client.SetNX(ctx, b.keys.indexLock, lockToken, input.LockTTL).Result(); acquireErr != nil {
		return result, fmt.Errorf("acquire account circuit runtime index lock: %w", acquireErr)
	} else if !ok {
		return result, fmt.Errorf("account circuit runtime index backfill is already running")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = b.client.client.Eval(cleanupCtx, `if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end return 0`, []string{b.keys.indexLock}, lockToken).Result()
	}()
	defer func() {
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = b.invalidate(cleanupCtx, lockToken, epoch, err.Error())
		}
	}()

	if _, err = beginAccountCircuitRuntimeIndexScript.Run(ctx, b.client.client, []string{b.keys.scopeRuntime, b.keys.runtimeScopes, b.keys.accountRuntimes, b.keys.runtimeAccounts, b.keys.indexMeta, b.keys.indexLock, b.keys.states, b.keys.escalation}, lockToken, epoch).Result(); err != nil {
		return result, fmt.Errorf("begin account circuit runtime index backfill: %w", err)
	}
	result.Epoch = epoch
	stateCount, statePages, err := b.scanAndApply(ctx, input, lockToken, epoch, "states", b.keys.states)
	if err != nil {
		return result, err
	}
	result.StateCount, result.Pages = stateCount, statePages
	if err = b.advancePhase(ctx, lockToken, epoch, "states", "escalation"); err != nil {
		return result, err
	}
	evidenceCount, evidencePages, err := b.scanAndApply(ctx, input, lockToken, epoch, "escalation", b.keys.escalation)
	if err != nil {
		return result, err
	}
	result.EvidenceCount, result.Pages = evidenceCount, result.Pages+evidencePages
	if err = b.advancePhase(ctx, lockToken, epoch, "escalation", "revisions"); err != nil {
		return result, err
	}
	revisions, revisionPages, err := b.seedDispatchRevisions(ctx, input, lockToken, epoch)
	if err != nil {
		return result, err
	}
	result.RevisionCount, result.Pages = len(revisions), result.Pages+revisionPages
	if err = b.advancePhase(ctx, lockToken, epoch, "revisions", "auditing"); err != nil {
		return result, err
	}
	if err = b.renew(ctx, lockToken, input.LockTTL); err != nil {
		return result, err
	}
	if err = b.audit(ctx, input, lockToken, revisions); err != nil {
		return result, err
	}
	result.AuditedAt = b.now().UTC()
	if _, err = finalizeAccountCircuitRuntimeIndexScript.Run(ctx, b.client.client, []string{b.keys.indexLock, b.keys.indexMeta}, lockToken, epoch, strconv.FormatInt(result.AuditedAt.UnixMilli(), 10)).Result(); err != nil {
		return result, fmt.Errorf("publish account circuit runtime index readiness: %w", err)
	}
	return result, nil
}

func normalizeAccountCircuitRuntimeIndexBackfillInput(input GatewayAccountCircuitRuntimeIndexBackfillInput) (GatewayAccountCircuitRuntimeIndexBackfillInput, error) {
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	if !validAccountCircuitRevisionText(input.OwnerID, 256) {
		return input, fmt.Errorf("account circuit runtime index owner is invalid")
	}
	if input.LockTTL == 0 {
		input.LockTTL = DefaultAccountCircuitRuntimeIndexLockTTL
	}
	if input.ScanCount == 0 {
		input.ScanCount = DefaultAccountCircuitRuntimeIndexScanCount
	}
	if input.MaxPages == 0 {
		input.MaxPages = DefaultAccountCircuitRuntimeIndexMaxPages
	}
	if input.MaxFields == 0 {
		input.MaxFields = DefaultAccountCircuitRuntimeIndexMaxFields
	}
	if input.MaxBytes == 0 {
		input.MaxBytes = DefaultAccountCircuitRuntimeIndexMaxBytes
	}
	if input.MaxScopeMembers == 0 {
		input.MaxScopeMembers = DefaultAccountCircuitRuntimeIndexMaxScopeMembers
	}
	if input.LockTTL < 10*time.Second || input.LockTTL > 30*time.Minute || input.ScanCount < 1 || input.ScanCount > 1000 || input.MaxPages < 1 || input.MaxPages > 100000 || input.MaxFields < 1 || input.MaxFields > 1000000 || input.MaxBytes < 1024 || input.MaxBytes > 256<<20 || input.MaxScopeMembers < 1 || input.MaxScopeMembers > 1000000 {
		return input, fmt.Errorf("account circuit runtime index backfill bounds are invalid")
	}
	return input, nil
}

func ValidateGatewayAccountCircuitRuntimeIndexBackfillInput(input GatewayAccountCircuitRuntimeIndexBackfillInput) error {
	_, err := normalizeAccountCircuitRuntimeIndexBackfillInput(input)
	return err
}

func (b *AccountCircuitRuntimeIndexBackfiller) scanAndApply(ctx context.Context, input GatewayAccountCircuitRuntimeIndexBackfillInput, lockToken, epoch, phase, sourceKey string) (int, int, error) {
	var cursor uint64
	pages, fields, bytesRead := 0, 0, 0
	for {
		if pages >= input.MaxPages {
			return 0, pages, fmt.Errorf("account circuit runtime index %s scan exceeded page bound", phase)
		}
		values, next, err := b.client.client.HScan(ctx, sourceKey, cursor, "", int64(input.ScanCount)).Result()
		if err != nil {
			return 0, pages, fmt.Errorf("scan account circuit runtime %s: %w", phase, err)
		}
		if len(values)%2 != 0 {
			return 0, pages, fmt.Errorf("account circuit runtime %s scan returned invalid fields", phase)
		}
		items := make([]accountCircuitRuntimeIndexItem, 0, len(values)/2)
		for index := 0; index < len(values); index += 2 {
			fields++
			bytesRead += len(values[index]) + len(values[index+1])
			if fields > input.MaxFields || bytesRead > input.MaxBytes {
				return 0, pages, fmt.Errorf("account circuit runtime index %s scan exceeded data bound", phase)
			}
			item, err := accountCircuitRuntimeIndexItemFromSource(phase, values[index], values[index+1])
			if err != nil {
				return 0, pages, err
			}
			items = append(items, item)
		}
		if err := b.renew(ctx, lockToken, input.LockTTL); err != nil {
			return 0, pages, err
		}
		rawItems, err := json.Marshal(items)
		if err != nil {
			return 0, pages, fmt.Errorf("encode account circuit runtime index page: %w", err)
		}
		if _, err = applyAccountCircuitRuntimeIndexPageScript.Run(ctx, b.client.client, []string{b.keys.scopeRuntime, b.keys.runtimeScopes, b.keys.accountRuntimes, b.keys.runtimeAccounts, b.keys.indexMeta, b.keys.indexLock, sourceKey}, lockToken, epoch, phase, strconv.FormatUint(next, 10), string(rawItems), strconv.Itoa(input.MaxScopeMembers)).Result(); err != nil {
			return 0, pages, fmt.Errorf("apply account circuit runtime index %s page: %w", phase, err)
		}
		pages++
		cursor = next
		if cursor == 0 {
			return fields, pages, nil
		}
	}
}

func (b *AccountCircuitRuntimeIndexBackfiller) seedDispatchRevisions(ctx context.Context, input GatewayAccountCircuitRuntimeIndexBackfillInput, lockToken, epoch string) (map[string]string, int, error) {
	result := make(map[string]string)
	after := ""
	pages, bytesRead := 0, 0
	pageLimit := min(input.ScanCount, GatewayAccountCircuitRuntimeMaxRevisionPage)
	for {
		if pages >= input.MaxPages {
			return nil, pages, fmt.Errorf("account circuit dispatch revision scan exceeded page bound")
		}
		page, err := b.revisionReader.ListGatewayAccountCircuitDispatchRevisions(ctx, GatewayAccountCircuitDispatchRevisionPageInput{AfterAccountID: after, Limit: pageLimit})
		if err != nil {
			return nil, pages, fmt.Errorf("list durable account circuit dispatch revisions: %w", err)
		}
		if len(page.Items) > pageLimit || (len(page.Items) == 0 && page.NextAfterAccountID != "") {
			return nil, pages, fmt.Errorf("account circuit dispatch revision page is invalid")
		}
		items := make([]accountCircuitDispatchRevisionIndexItem, 0, len(page.Items))
		previous := after
		for _, item := range page.Items {
			if !validAccountCircuitRevisionText(item.AccountID, 256) || item.AccountID <= previous || item.DispatchRevision < 1 {
				return nil, pages, fmt.Errorf("account circuit dispatch revision page ordering is invalid")
			}
			if _, exists := result[item.AccountID]; exists {
				return nil, pages, fmt.Errorf("account circuit dispatch revision page contains duplicate account")
			}
			bytesRead += len(item.AccountID) + 8
			if len(result)+len(items)+1 > input.MaxFields || bytesRead > input.MaxBytes {
				return nil, pages, fmt.Errorf("account circuit dispatch revision scan exceeded data bound")
			}
			items = append(items, accountCircuitDispatchRevisionIndexItem{AccountID: item.AccountID, DispatchRevision: item.DispatchRevision})
			previous = item.AccountID
		}
		if page.NextAfterAccountID != "" && (len(items) == 0 || page.NextAfterAccountID != items[len(items)-1].AccountID) {
			return nil, pages, fmt.Errorf("account circuit dispatch revision cursor is invalid")
		}
		if err := b.renew(ctx, lockToken, input.LockTTL); err != nil {
			return nil, pages, err
		}
		raw, err := json.Marshal(items)
		if err != nil {
			return nil, pages, fmt.Errorf("encode account circuit dispatch revision page: %w", err)
		}
		if _, err = applyAccountCircuitDispatchRevisionPageScript.Run(ctx, b.client.client, []string{b.keys.revisions, b.keys.indexMeta, b.keys.indexLock}, lockToken, epoch, page.NextAfterAccountID, string(raw)).Result(); err != nil {
			return nil, pages, fmt.Errorf("apply account circuit dispatch revision page: %w", err)
		}
		for _, item := range items {
			result[item.AccountID] = strconv.FormatInt(item.DispatchRevision, 10)
		}
		pages++
		if page.NextAfterAccountID == "" {
			return result, pages, nil
		}
		after = page.NextAfterAccountID
	}
}

func accountCircuitRuntimeIndexItemFromSource(phase, field, source string) (accountCircuitRuntimeIndexItem, error) {
	if !validAccountCircuitRevisionText(field, 2048) || source == "" {
		return accountCircuitRuntimeIndexItem{}, fmt.Errorf("account circuit runtime index source field is invalid")
	}
	if phase == "states" {
		var entry accountCircuitRuntimeIndexStateEntry
		if err := json.Unmarshal([]byte(source), &entry); err != nil {
			return accountCircuitRuntimeIndexItem{}, fmt.Errorf("decode account circuit runtime state source: %w", err)
		}
		if err := validateAccountCircuitRuntimeIndexStateEntry(entry); err != nil {
			return accountCircuitRuntimeIndexItem{}, err
		}
		scope := GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeKind(entry.State.Scope.Kind), AccountRuntimeKey: entry.State.Scope.AccountRuntimeKey, KeyFingerprint: entry.State.Scope.KeyFingerprint, ProtocolProfile: entry.State.Scope.ProtocolProfile, RequestLane: entry.State.Scope.RequestLane, ModelBucket: entry.State.Scope.ModelBucket}
		scopeKey, err := GatewayAccountCircuitScopeKey(scope)
		if err != nil || entry.State.ScopeKey != field || scopeKey != field || !validAccountCircuitRevisionText(entry.State.DispatchRevision, 64) {
			return accountCircuitRuntimeIndexItem{}, fmt.Errorf("account circuit runtime state source is invalid")
		}
		if revision, err := strconv.ParseInt(entry.State.DispatchRevision, 10, 64); err != nil || revision < 0 {
			return accountCircuitRuntimeIndexItem{}, fmt.Errorf("account circuit runtime state revision is invalid")
		}
		runtime := scope.AccountRuntimeKey
		account := accountCircuitRuntimeAccountID(runtime)
		if !validAccountCircuitRevisionText(account, 256) {
			return accountCircuitRuntimeIndexItem{}, fmt.Errorf("account circuit runtime state account is invalid")
		}
		revision, _ := strconv.ParseInt(entry.State.DispatchRevision, 10, 64)
		return accountCircuitRuntimeIndexItem{Field: field, Runtime: runtime, Account: account, Source: source, Revision: revision}, nil
	}
	if phase == "escalation" {
		var evidence accountCircuitRuntimeIndexEvidence
		if err := json.Unmarshal([]byte(source), &evidence); err != nil {
			return accountCircuitRuntimeIndexItem{}, fmt.Errorf("decode account circuit runtime escalation source: %w", err)
		}
		if revision, err := strconv.ParseInt(evidence.DispatchRevision, 10, 64); err != nil || revision < 1 || len(evidence.Scopes) > GatewayAccountCircuitRuntimeMaxEvidenceScopes {
			return accountCircuitRuntimeIndexItem{}, fmt.Errorf("account circuit runtime escalation source is invalid")
		}
		for _, scope := range evidence.Scopes {
			if !validAccountCircuitRevisionText(scope.ScopeKey, 2048) || !validAccountCircuitRevisionText(scope.IncidentID, 256) || !validAccountCircuitRevisionText(scope.EvidenceID, 256) || scope.ConfirmedFailureCount < 1 || scope.ObservedAtMS < 0 {
				return accountCircuitRuntimeIndexItem{}, fmt.Errorf("account circuit runtime escalation evidence is invalid")
			}
		}
		account := accountCircuitRuntimeAccountID(field)
		if !validAccountCircuitRevisionText(account, 256) {
			return accountCircuitRuntimeIndexItem{}, fmt.Errorf("account circuit runtime escalation account is invalid")
		}
		revision, _ := strconv.ParseInt(evidence.DispatchRevision, 10, 64)
		return accountCircuitRuntimeIndexItem{Field: field, Runtime: field, Account: account, Source: source, Revision: revision}, nil
	}
	return accountCircuitRuntimeIndexItem{}, fmt.Errorf("account circuit runtime index phase is invalid")
}

func validateAccountCircuitRuntimeIndexStateEntry(entry accountCircuitRuntimeIndexStateEntry) error {
	if err := validateAccountCircuitRuntimeWireRelations(entry.State); err != nil {
		return err
	}
	state, err := runtimeStateFromWire(entry.State)
	if err != nil || state.DispatchRevision < 1 {
		return fmt.Errorf("account circuit runtime state source is invalid")
	}
	if state.Phase == GatewayAccountCircuitPhaseClosed {
		if entry.ClosedExpiresAtMS == nil || *entry.ClosedExpiresAtMS <= 0 {
			return fmt.Errorf("account circuit runtime closed deadline is invalid")
		}
	} else if entry.ClosedExpiresAtMS != nil {
		return fmt.Errorf("account circuit runtime non-closed deadline is invalid")
	}
	if len(entry.ReplayOrder) > GatewayAccountCircuitRuntimeMaxReplayIDs {
		return fmt.Errorf("account circuit runtime replay history is invalid")
	}
	seen := make(map[string]struct{}, len(entry.ReplayOrder))
	for _, transitionID := range entry.ReplayOrder {
		if !validAccountCircuitRevisionText(transitionID, 256) {
			return fmt.Errorf("account circuit runtime replay history is invalid")
		}
		if _, exists := seen[transitionID]; exists {
			return fmt.Errorf("account circuit runtime replay history is invalid")
		}
		seen[transitionID] = struct{}{}
	}
	return nil
}

func validateAccountCircuitRuntimeWireRelations(state accountCircuitRuntimeStateWire) error {
	for _, relation := range []struct {
		values   []string
		maxBytes int
	}{
		{state.ChildIncidentIDs, 256},
		{state.ChildScopeKeys, 2048},
		{state.RequiredRecoveryScopeKeys, 2048},
		{state.RecoveryEvidenceScopeKeys, 2048},
	} {
		if len(relation.values) > GatewayAccountCircuitRuntimeMaxEvidenceScopes {
			return fmt.Errorf("account circuit runtime relation list is invalid")
		}
		seen := make(map[string]struct{}, len(relation.values))
		for _, value := range relation.values {
			if !validAccountCircuitRevisionText(value, relation.maxBytes) {
				return fmt.Errorf("account circuit runtime relation list is invalid")
			}
			if _, exists := seen[value]; exists {
				return fmt.Errorf("account circuit runtime relation list is invalid")
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

func (b *AccountCircuitRuntimeIndexBackfiller) advancePhase(ctx context.Context, owner, epoch, current, next string) error {
	if _, err := advanceAccountCircuitRuntimeIndexPhaseScript.Run(ctx, b.client.client, []string{b.keys.indexLock, b.keys.indexMeta}, owner, epoch, current, next).Result(); err != nil {
		return fmt.Errorf("advance account circuit runtime index phase: %w", err)
	}
	return nil
}

func (b *AccountCircuitRuntimeIndexBackfiller) renew(ctx context.Context, owner string, ttl time.Duration) error {
	value, err := renewAccountCircuitRuntimeIndexLockScript.Run(ctx, b.client.client, []string{b.keys.indexLock}, owner, strconv.FormatInt(ttl.Milliseconds(), 10)).Int64()
	if err != nil {
		return fmt.Errorf("renew account circuit runtime index lock: %w", err)
	}
	if value != 1 {
		return fmt.Errorf("account circuit runtime index lock is lost")
	}
	return nil
}

func (b *AccountCircuitRuntimeIndexBackfiller) invalidate(ctx context.Context, owner, epoch, reason string) error {
	reason = strings.TrimSpace(reason)
	if len(reason) > 512 {
		reason = reason[:512]
	}
	_, err := invalidateAccountCircuitRuntimeIndexScript.Run(ctx, b.client.client, []string{b.keys.indexLock, b.keys.indexMeta}, owner, epoch, reason).Result()
	return err
}

func (b *AccountCircuitRuntimeIndexBackfiller) audit(ctx context.Context, input GatewayAccountCircuitRuntimeIndexBackfillInput, lockToken string, expectedRevisions map[string]string) error {
	states, err := b.readHash(ctx, b.keys.states, input, lockToken)
	if err != nil {
		return err
	}
	evidence, err := b.readHash(ctx, b.keys.escalation, input, lockToken)
	if err != nil {
		return err
	}
	expectedScopeRuntime := make(map[string]string, len(states))
	expectedRuntimeScopes := make(map[string][]string)
	expectedRuntimeAccounts := make(map[string]string)
	expectedAccountRuntimes := make(map[string][]string)
	for field, source := range states {
		item, err := accountCircuitRuntimeIndexItemFromSource("states", field, source)
		if err != nil {
			return err
		}
		durableRevision, err := strconv.ParseInt(expectedRevisions[item.Account], 10, 64)
		if err != nil || item.Revision != durableRevision {
			return fmt.Errorf("account circuit runtime state revision does not match durable account")
		}
		expectedScopeRuntime[item.Field] = item.Runtime
		expectedRuntimeScopes[item.Runtime] = append(expectedRuntimeScopes[item.Runtime], item.Field)
		expectedRuntimeAccounts[item.Runtime] = item.Account
		expectedAccountRuntimes[item.Account] = append(expectedAccountRuntimes[item.Account], item.Runtime)
	}
	for field, source := range evidence {
		item, err := accountCircuitRuntimeIndexItemFromSource("escalation", field, source)
		if err != nil {
			return err
		}
		if existing, ok := expectedRuntimeAccounts[item.Runtime]; ok && existing != item.Account {
			return fmt.Errorf("account circuit runtime index evidence account is inconsistent")
		}
		durableRevision, err := strconv.ParseInt(expectedRevisions[item.Account], 10, 64)
		if err != nil || item.Revision != durableRevision {
			return fmt.Errorf("account circuit escalation revision does not match durable account")
		}
		expectedRuntimeAccounts[item.Runtime] = item.Account
		expectedAccountRuntimes[item.Account] = append(expectedAccountRuntimes[item.Account], item.Runtime)
	}
	for runtime, scopes := range expectedRuntimeScopes {
		expectedRuntimeScopes[runtime] = uniqueSortedRuntimeStrings(scopes)
	}
	for account, runtimes := range expectedAccountRuntimes {
		expectedAccountRuntimes[account] = uniqueSortedRuntimeStrings(runtimes)
	}
	actualScopeRuntime, err := b.readHash(ctx, b.keys.scopeRuntime, input, lockToken)
	if err != nil {
		return err
	}
	actualRuntimeScopes, err := b.readJSONArrays(ctx, b.keys.runtimeScopes, input, lockToken)
	if err != nil {
		return err
	}
	actualRuntimeAccounts, err := b.readHash(ctx, b.keys.runtimeAccounts, input, lockToken)
	if err != nil {
		return err
	}
	actualAccountRuntimes, err := b.readJSONArrays(ctx, b.keys.accountRuntimes, input, lockToken)
	if err != nil {
		return err
	}
	actualRevisions, err := b.readHash(ctx, b.keys.revisions, input, lockToken)
	if err != nil {
		return err
	}
	if !equalRuntimeStringMap(expectedScopeRuntime, actualScopeRuntime) || !equalRuntimeStringMap(expectedRuntimeAccounts, actualRuntimeAccounts) || !equalRuntimeArrayMap(expectedRuntimeScopes, actualRuntimeScopes) || !equalRuntimeArrayMap(expectedAccountRuntimes, actualAccountRuntimes) {
		return fmt.Errorf("account circuit runtime index audit failed")
	}
	for accountID, revision := range expectedRevisions {
		if actualRevisions[accountID] != revision {
			return fmt.Errorf("account circuit dispatch revision audit failed")
		}
	}
	return nil
}

func (b *AccountCircuitRuntimeIndexBackfiller) readHash(ctx context.Context, key string, input GatewayAccountCircuitRuntimeIndexBackfillInput, lockToken string) (map[string]string, error) {
	result := make(map[string]string)
	var cursor uint64
	pages, fields, bytesRead := 0, 0, 0
	for {
		if pages >= input.MaxPages {
			return nil, fmt.Errorf("account circuit runtime index audit exceeded page bound")
		}
		values, next, err := b.client.client.HScan(ctx, key, cursor, "", int64(input.ScanCount)).Result()
		if err != nil {
			return nil, fmt.Errorf("scan account circuit runtime index audit: %w", err)
		}
		if len(values)%2 != 0 {
			return nil, fmt.Errorf("account circuit runtime index audit fields are invalid")
		}
		if err := b.renew(ctx, lockToken, input.LockTTL); err != nil {
			return nil, err
		}
		for index := 0; index < len(values); index += 2 {
			fields++
			bytesRead += len(values[index]) + len(values[index+1])
			if fields > input.MaxFields || bytesRead > input.MaxBytes {
				return nil, fmt.Errorf("account circuit runtime index audit exceeded data bound")
			}
			if existing, exists := result[values[index]]; exists {
				if existing != values[index+1] {
					return nil, fmt.Errorf("account circuit runtime index audit source changed during scan")
				}
				continue
			}
			result[values[index]] = values[index+1]
		}
		pages++
		cursor = next
		if cursor == 0 {
			return result, nil
		}
	}
}

func (b *AccountCircuitRuntimeIndexBackfiller) readJSONArrays(ctx context.Context, key string, input GatewayAccountCircuitRuntimeIndexBackfillInput, lockToken string) (map[string][]string, error) {
	raw, err := b.readHash(ctx, key, input, lockToken)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]string, len(raw))
	for field, value := range raw {
		var values []string
		if err := json.Unmarshal([]byte(value), &values); err != nil || len(values) == 0 || len(values) > input.MaxScopeMembers || !isUniqueSortedRuntimeStrings(values) {
			return nil, fmt.Errorf("account circuit runtime index array is invalid")
		}
		for _, item := range values {
			if !validAccountCircuitRevisionText(item, 2048) {
				return nil, fmt.Errorf("account circuit runtime index array item is invalid")
			}
		}
		result[field] = values
	}
	return result, nil
}

func newAccountCircuitRuntimeIndexEpoch() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate account circuit runtime index epoch: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func uniqueSortedRuntimeStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index := len(result) - 1; index > 0; index-- {
		if result[index] == result[index-1] {
			result = append(result[:index], result[index+1:]...)
		}
	}
	return result
}

func isUniqueSortedRuntimeStrings(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}
	return true
}

func equalRuntimeStringMap(expected, actual map[string]string) bool {
	if len(expected) != len(actual) {
		return false
	}
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func equalRuntimeArrayMap(expected, actual map[string][]string) bool {
	if len(expected) != len(actual) {
		return false
	}
	for key, expectedValues := range expected {
		actualValues, ok := actual[key]
		if !ok || len(expectedValues) != len(actualValues) {
			return false
		}
		for index, value := range expectedValues {
			if actualValues[index] != value {
				return false
			}
		}
	}
	return true
}
