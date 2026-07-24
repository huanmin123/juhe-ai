package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	goredis "github.com/redis/go-redis/v9"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	AccountCircuitRedisStoreName         = "gateway-account-circuit"
	DefaultAccountCircuitClosedRetention = 5 * time.Minute
)

var invalidAccountCircuitNamespaceChars = regexp.MustCompile(`[^a-zA-Z0-9_.:-]+`)

const projectAccountCircuitRevisionLua = `
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local escalation_key = KEYS[4]
local revisions_key = KEYS[5]
local runtime_key = ARGV[1]
local incoming_revision = tonumber(ARGV[2])
local transition_id = ARGV[3]
local now_ms = tonumber(ARGV[4])
local retention_ms = tonumber(ARGV[5])
local function require_type(key, expected)
  local actual = redis.call('TYPE', key)['ok']
  if actual ~= 'none' and actual ~= expected then return false end
  return true
end
if not require_type(states_key, 'hash') or not require_type(due_key, 'zset')
  or not require_type(closed_key, 'zset') or not require_type(escalation_key, 'hash')
  or not require_type(revisions_key, 'hash') then
  return redis.error_reply('invalid account circuit Redis key type')
end
if not incoming_revision or incoming_revision < 1 or incoming_revision ~= math.floor(incoming_revision) then
  return redis.error_reply('invalid dispatch revision')
end

local current_raw = redis.call('HGET', revisions_key, runtime_key)
local current_revision = current_raw and tonumber(current_raw) or nil
if current_raw and not current_revision then
  return redis.error_reply('invalid revision tombstone')
end
if current_revision and current_revision > incoming_revision then
  return cjson.encode({ status = 'stale', currentRevision = current_revision, closedStates = 0 })
end
local already_projected = current_revision and current_revision == incoming_revision

local family_prefix = runtime_key .. ':authorized:'
local function matches_runtime_key(candidate)
  return candidate == runtime_key or (
    not string.find(runtime_key, ':authorized:', 1, true)
    and string.sub(candidate, 1, string.len(family_prefix)) == family_prefix
  )
end

local state_changes = {}
local max_seen_revision = current_revision or 0
local values = redis.call('HGETALL', states_key)
for index = 1, #values, 2 do
  local scope_key = values[index]
  local entry = cjson.decode(values[index + 1])
  local state = entry['state']
  local scope = state and state['scope'] or nil
  local state_runtime_key = scope and scope['accountRuntimeKey'] or nil
  if state_runtime_key and matches_runtime_key(state_runtime_key) then
    local state_revision = tonumber(state['dispatchRevision'])
    local state_generation = tonumber(state['generation'])
    if not state_revision or not state_generation or state_generation < 0 or state_generation ~= math.floor(state_generation) then
      return redis.error_reply('invalid state dispatch revision')
    end
    if state_revision > max_seen_revision then
      max_seen_revision = state_revision
    end
    if state_revision < incoming_revision then
      table.insert(state_changes, { scopeKey = scope_key, entry = entry, state = state, runtimeKey = state_runtime_key })
    end
  end
end

local evidence_values = redis.call('HGETALL', escalation_key)
local evidence_deletes = {}
for index = 1, #evidence_values, 2 do
  local evidence_runtime_key = evidence_values[index]
  local evidence = cjson.decode(evidence_values[index + 1])
  if matches_runtime_key(evidence_runtime_key) then
    local evidence_revision = tonumber(evidence['dispatchRevision'])
    if not evidence_revision then
      return redis.error_reply('invalid evidence dispatch revision')
    end
    if evidence_revision > max_seen_revision then
      max_seen_revision = evidence_revision
    end
    if evidence_revision < incoming_revision then
      table.insert(evidence_deletes, evidence_runtime_key)
    end
  end
end

if max_seen_revision > incoming_revision then
  return cjson.encode({ status = 'stale', currentRevision = max_seen_revision, closedStates = 0 })
end

local closed_states = 0
for _, change in ipairs(state_changes) do
  local state = change['state']
  local entry = change['entry']
  state['phase'] = 'CLOSED'
  state['generation'] = tonumber(state['generation'] or 0) + 1
	state['dispatchRevision'] = tostring(incoming_revision)
	state['ledgerRevision'] = nil
  state['transitionId'] = transition_id
  state['backoffAttempt'] = 0
  state['recoverySuccessCount'] = 0
  state['openedAtMs'] = nil
  state['retryAtMs'] = nil
  state['failureReason'] = nil
  state['lease'] = nil
  state['halfOpenOrigin'] = nil
  state['incidentId'] = nil
  state['shadowedByIncidentId'] = nil
  state['childIncidentIds'] = nil
  state['childScopeKeys'] = nil
  state['requiredRecoveryScopeKeys'] = nil
  state['recoveryEvidenceScopeKeys'] = nil
  state['updatedAtMs'] = now_ms
  entry['closedExpiresAtMs'] = now_ms + retention_ms
  entry['replayIds'] = { transition_id }
  entry['replayOrder'] = { transition_id }
  redis.call('HSET', states_key, change['scopeKey'], cjson.encode(entry))
  redis.call('ZREM', due_key, change['scopeKey'])
  redis.call('ZADD', closed_key, now_ms + retention_ms, change['scopeKey'])
  redis.call('HDEL', escalation_key, change['runtimeKey'])
  closed_states = closed_states + 1
end
for _, evidence_runtime_key in ipairs(evidence_deletes) do
  redis.call('HDEL', escalation_key, evidence_runtime_key)
end
redis.call('HSET', revisions_key, runtime_key, tostring(incoming_revision))
local result_status = already_projected and 'idempotent' or 'applied'
return cjson.encode({ status = result_status, currentRevision = incoming_revision, closedStates = closed_states })
`

var projectAccountCircuitRevisionScript = goredis.NewScript(projectAccountCircuitRevisionLua)

type accountCircuitRevisionKeys struct {
	states          string
	due             string
	closed          string
	escalation      string
	revisions       string
	scopeRuntime    string
	runtimeScopes   string
	accountRuntimes string
	runtimeAccounts string
	ledgerRevisions string
	indexMeta       string
}

type AccountCircuitRevisionProjector struct {
	keys      accountCircuitRevisionKeys
	retention time.Duration
	now       func() time.Time
	project   func(context.Context, accountCircuitRevisionKeys, port.GatewayAccountCircuitOutboxEvent, time.Duration, time.Time) ([]byte, error)
}

func NewAccountCircuitRevisionProjector(client *Client, retention time.Duration) (*AccountCircuitRevisionProjector, error) {
	if client == nil || client.client == nil {
		return nil, fmt.Errorf("Redis state client is required")
	}
	if retention == 0 {
		retention = DefaultAccountCircuitClosedRetention
	}
	if retention <= 0 || retention > 24*time.Hour {
		return nil, fmt.Errorf("account circuit closed retention is invalid")
	}
	keys, err := accountCircuitRevisionRedisKeys(client.namespace, AccountCircuitRedisStoreName)
	if err != nil {
		return nil, err
	}
	projector := &AccountCircuitRevisionProjector{keys: keys, retention: retention, now: time.Now}
	projector.project = func(ctx context.Context, keys accountCircuitRevisionKeys, event port.GatewayAccountCircuitOutboxEvent, retention time.Duration, now time.Time) ([]byte, error) {
		value, err := projectAccountCircuitRevisionScript.Run(
			ctx,
			client.client,
			[]string{keys.states, keys.due, keys.closed, keys.escalation, keys.revisions},
			event.AccountRuntimeKey,
			strconv.FormatInt(event.DispatchRevision, 10),
			event.TransitionID,
			now.UTC().UnixMilli(),
			retention.Milliseconds(),
		).Result()
		if err != nil {
			return nil, err
		}
		switch typed := value.(type) {
		case string:
			return []byte(typed), nil
		case []byte:
			return typed, nil
		default:
			return nil, fmt.Errorf("unexpected account circuit revision result type %T", value)
		}
	}
	return projector, nil
}

func (p *AccountCircuitRevisionProjector) WithNow(now func() time.Time) *AccountCircuitRevisionProjector {
	if now != nil {
		p.now = now
	}
	return p
}

func (p *AccountCircuitRevisionProjector) ProjectGatewayAccountCircuitRevision(ctx context.Context, event port.GatewayAccountCircuitOutboxEvent) (port.GatewayAccountCircuitRevisionProjection, error) {
	if p == nil || p.project == nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("account circuit revision projector is required")
	}
	if ctx == nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("account circuit revision context is required")
	}
	if err := validateAccountCircuitRevisionEvent(event); err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, err
	}
	raw, err := p.project(ctx, p.keys, event, p.retention, p.now())
	if err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("project account circuit dispatch revision: %w", err)
	}
	var result port.GatewayAccountCircuitRevisionProjection
	if err := json.Unmarshal(raw, &result); err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("decode account circuit revision projection: %w", err)
	}
	if err := validateAccountCircuitRevisionProjection(result); err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, err
	}
	return result, nil
}

func accountCircuitRevisionRedisKeys(namespace, name string) (accountCircuitRevisionKeys, error) {
	namespace = invalidAccountCircuitNamespaceChars.ReplaceAllString(strings.TrimSpace(namespace), "_")
	namespace = strings.Trim(namespace, "_")
	name = invalidAccountCircuitNamespaceChars.ReplaceAllString(strings.TrimSpace(name), "_")
	name = strings.Trim(name, "_")
	if namespace == "" || name == "" {
		return accountCircuitRevisionKeys{}, fmt.Errorf("account circuit Redis namespace is invalid")
	}
	prefix := "juhe-ai:" + namespace + ":account-circuit:" + name
	return accountCircuitRevisionKeys{
		states: prefix + ":states", due: prefix + ":due", closed: prefix + ":closed",
		escalation: prefix + ":escalation", revisions: prefix + ":dispatch-revisions",
		scopeRuntime: prefix + ":scope-runtime", runtimeScopes: prefix + ":runtime-scopes",
		accountRuntimes: prefix + ":account-runtimes", indexMeta: prefix + ":runtime-index-meta",
		runtimeAccounts: prefix + ":runtime-accounts",
		ledgerRevisions: prefix + ":ledger-revisions",
	}, nil
}

func validateAccountCircuitRevisionEvent(event port.GatewayAccountCircuitOutboxEvent) error {
	if event.EventType != port.GatewayAccountCircuitDispatchRevisionChanged || event.ProjectionKey != port.GatewayAccountCircuitProjectionKey || event.AccountRuntimeKey != event.AccountID || !validAccountCircuitRevisionText(event.AccountID, 1024) || !validAccountCircuitRevisionText(event.TransitionID, 256) || event.DispatchRevision < 1 {
		return fmt.Errorf("account circuit revision event is invalid")
	}
	return nil
}

func validateAccountCircuitRevisionProjection(value port.GatewayAccountCircuitRevisionProjection) error {
	if value.CurrentRevision < 1 || value.ClosedStates < 0 {
		return fmt.Errorf("account circuit revision projection result is invalid")
	}
	switch value.Status {
	case port.GatewayAccountCircuitRevisionApplied, port.GatewayAccountCircuitRevisionIdempotent, port.GatewayAccountCircuitRevisionStale:
		return nil
	default:
		return fmt.Errorf("account circuit revision projection status is invalid")
	}
}

func validAccountCircuitRevisionText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

var _ port.GatewayAccountCircuitRevisionProjector = (*AccountCircuitRevisionProjector)(nil)
