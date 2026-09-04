package gatewayruntimecache

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// internal gateway registry (Node runtime/internal-gateway-registry.ts):
// performance-mode instances publish signed heartbeat entries into Redis so
// control-plane processes can enumerate live gateway origins.
// ---------------------------------------------------------------------------

const (
	registryEntryVersion     = 1
	registryEntryTTLSeconds  = 20
	registryHeartbeatEvery   = 5 * time.Second
	registryCommandTimeout   = 800 * time.Millisecond
	registryEntryLimit       = 64
	registryIndexTTLSeconds  = registryEntryTTLSeconds * 3
	registryEntryKeyPrefix   = "runtime:internal-gateway:v1:"
	registryIndexKeySuffix   = "runtime:internal-gateway-index:v1"
)

const registryPublishScript = `
local redis_time = redis.call('TIME')
local now_ms = redis_time[1] * 1000 + math.floor(redis_time[2] / 1000)
local minimum_score = now_ms - tonumber(ARGV[2]) * 1000
redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
redis.call('ZADD', KEYS[2], now_ms, KEYS[1])
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', '(' .. minimum_score)
local cardinality = redis.call('ZCARD', KEYS[2])
if cardinality > tonumber(ARGV[3]) then
  redis.call('ZREMRANGEBYRANK', KEYS[2], 0, cardinality - tonumber(ARGV[3]) - 1)
end
redis.call('EXPIRE', KEYS[2], tonumber(ARGV[4]))
return now_ms
`

const registryUnregisterScript = `
local raw = redis.call('GET', KEYS[1])
if not raw then return 0 end
local ok, entry = pcall(cjson.decode, raw)
if not ok or type(entry) ~= 'table' or entry.bootId ~= ARGV[1] then return 0 end
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], KEYS[1])
return 1
`

const registryReadScript = `
local redis_time = redis.call('TIME')
local now_ms = redis_time[1] * 1000 + math.floor(redis_time[2] / 1000)
local minimum_score = now_ms - tonumber(ARGV[1]) * 1000
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', '(' .. minimum_score)
return redis.call('ZRANGEBYSCORE', KEYS[1], minimum_score, now_ms, 'LIMIT', '0', ARGV[2])
`

// InternalGatewayEndpoint mirrors InternalGatewayEndpoint.
type InternalGatewayEndpoint struct {
	InstanceID string `json:"instanceId"`
	Origin     string `json:"origin"`
}

// registryEntry mirrors RegistryEntry.
type registryEntry struct {
	Version   int    `json:"version"`
	InstanceID string `json:"instanceId"`
	Origin    string `json:"origin"`
	BootID    string `json:"bootId"`
	Signature string `json:"signature"`
}

// RegistryConfig carries the runtime gates Node reads from runtimeConfig.
type RegistryConfig struct {
	RedisURL   string
	Namespace  string
	Secret     string
	InstanceID string
	Port       int
	// PublisherEnabled mirrors internalGatewayRegistryPublisherEnabled
	// (performance gateway server with a redis runtime-state driver).
	PublisherEnabled bool
	// ReaderEnabled mirrors internalGatewayRegistryReaderEnabled
	// (performance control / control-replica db-service).
	ReaderEnabled bool
}

// Registry is the lifecycle owner for one process: Start publishes heartbeats,
// Stop unregisters the boot id, ListEndpoints reads live peers.
type Registry struct {
	config RegistryConfig
	client *redis.Client
	prefix string
	indexKey string

	mu        sync.Mutex
	publishRequested bool
	session   *registrySession
	stopPromiseErr error
	stopDone  chan struct{}
}

type registrySession struct {
	bootID    string
	stopping  bool
	stopCh    chan struct{}
}

// NewRegistry builds the registry (client connected lazily by go-redis).
func NewRegistry(config RegistryConfig) (*Registry, error) {
	if strings.TrimSpace(config.RedisURL) == "" {
		return nil, errors.New("内部 Gateway 注册表需要 Redis URL")
	}
	if strings.TrimSpace(config.Secret) == "" {
		return nil, errors.New("内部 Gateway 注册表需要 runtime secret")
	}
	options, err := redis.ParseURL(config.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("parse registry Redis URL: %w", err)
	}
	namespace := strings.TrimRight(strings.TrimSpace(config.Namespace), ":")
	if !strings.HasPrefix(namespace, "juhe-ai:") {
		namespace = "juhe-ai:" + namespace
	}
	if strings.HasSuffix(namespace, "juhe-ai:") || namespace == "juhe-ai:" {
		namespace = "juhe-ai"
	}
	return &Registry{
		config:   config,
		client:   redis.NewClient(options),
		prefix:   namespace + ":" + registryEntryKeyPrefix,
		indexKey: namespace + ":" + registryIndexKeySuffix,
	}, nil
}

// Close releases the Redis client.
func (r *Registry) Close() error { return r.client.Close() }

// EntryKey mirrors internalGatewayRegistryEntryKey.
func (r *Registry) EntryKey(instanceID string) string {
	return r.prefix + sanitizeRegistryNamespacePart(instanceID)
}

// Start mirrors startInternalGatewayRegistry.
func (r *Registry) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishRequested = true
	if !r.config.PublisherEnabled || r.session != nil || r.stopDone != nil {
		return
	}
	session := &registrySession{bootID: newRegistryBootID(), stopCh: make(chan struct{})}
	r.session = session
	go r.publishAndScheduleHeartbeat(session)
}

// Stop mirrors stopInternalGatewayRegistry: stop heartbeats, wait for the
// in-flight publish, unregister this boot id.
func (r *Registry) Stop(ctx context.Context) error {
	r.mu.Lock()
	r.publishRequested = false
	session := r.session
	r.session = nil
	if session == nil {
		r.mu.Unlock()
		return nil
	}
	if r.stopDone == nil {
		r.stopDone = make(chan struct{})
		done := r.stopDone
		session.stopping = true
		close(session.stopCh)
		go func() {
			r.unregister(ctx, session.bootID)
			r.mu.Lock()
			r.stopDone = nil
			r.mu.Unlock()
			close(done)
		}()
	}
	done := r.stopDone
	r.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ListEndpoints mirrors listInternalGatewayEndpoints: live entries within the
// TTL window, deduped by instance id, sorted by instance id.
func (r *Registry) ListEndpoints(ctx context.Context) ([]InternalGatewayEndpoint, error) {
	if !r.config.ReaderEnabled {
		return []InternalGatewayEndpoint{}, nil
	}
	values, err := r.runScript(ctx, registryReadScript, []string{r.indexKey},
		strconv.Itoa(registryEntryTTLSeconds), strconv.Itoa(registryEntryLimit))
	if err != nil {
		return nil, err
	}
	keys, ok := values.([]any)
	if !ok {
		return []InternalGatewayEndpoint{}, nil
	}
	entryKeysSet := map[string]bool{}
	entryKeys := []string{}
	for _, raw := range keys {
		key, isString := raw.(string)
		if !isString || !strings.HasPrefix(key, r.prefix) {
			continue
		}
		if entryKeysSet[key] || len(entryKeys) >= registryEntryLimit {
			continue
		}
		entryKeysSet[key] = true
		entryKeys = append(entryKeys, key)
	}
	if len(entryKeys) == 0 {
		return []InternalGatewayEndpoint{}, nil
	}
	rawEntries, err := r.client.MGet(ctx, entryKeys...).Result()
	if err != nil {
		return nil, err
	}
	endpoints := map[string]InternalGatewayEndpoint{}
	for _, raw := range rawEntries {
		text, isString := raw.(string)
		if !isString {
			continue
		}
		entry := parseRegistryEntry(text, r.config.Secret)
		if entry == nil {
			continue
		}
		endpoints[entry.InstanceID] = InternalGatewayEndpoint{InstanceID: entry.InstanceID, Origin: entry.Origin}
	}
	out := make([]InternalGatewayEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		out = append(out, endpoint)
	}
	sortRegistryEndpoints(out)
	return out, nil
}

func sortRegistryEndpoints(endpoints []InternalGatewayEndpoint) {
	for i := 1; i < len(endpoints); i++ {
		for j := i; j > 0 && endpoints[j].InstanceID < endpoints[j-1].InstanceID; j-- {
			endpoints[j], endpoints[j-1] = endpoints[j-1], endpoints[j]
		}
	}
}

func (r *Registry) runScript(ctx context.Context, script string, keys []string, args ...string) (any, error) {
	scriptCtx, cancel := context.WithTimeout(ctx, registryCommandTimeout)
	defer cancel()
	return r.client.Eval(scriptCtx, script, keys, toAnySlice(args)...).Result()
}

func toAnySlice(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// publishAndScheduleHeartbeat mirrors publishAndScheduleHeartbeat +
// finishPublishAndScheduleHeartbeat: publish, then re-arm after the interval
// unless the session stopped.
func (r *Registry) publishAndScheduleHeartbeat(session *registrySession) {
	for {
		r.publish(session)
		timer := time.NewTimer(registryHeartbeatEvery)
		defer timer.Stop()
		select {
		case <-session.stopCh:
			return
		case <-timer.C:
		}
		r.mu.Lock()
		current := r.publishRequested && r.session == session && !session.stopping && r.config.PublisherEnabled
		r.mu.Unlock()
		if !current {
			return
		}
	}
}

func (r *Registry) publish(session *registrySession) {
	r.mu.Lock()
	current := r.publishRequested && r.session == session && !session.stopping && r.config.PublisherEnabled
	r.mu.Unlock()
	if !current || r.config.RedisURL == "" {
		return
	}
	origin := fmt.Sprintf("http://127.0.0.1:%d", r.config.Port)
	entry := registryEntry{
		Version:    registryEntryVersion,
		InstanceID: r.config.InstanceID,
		Origin:     origin,
		BootID:     session.bootID,
	}
	entry.Signature = registrySignature(entry.Version, entry.InstanceID, entry.Origin, entry.BootID, r.config.Secret)
	encoded, err := json.Marshal(entry)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), registryCommandTimeout)
	defer cancel()
	_, _ = r.runScript(ctx, registryPublishScript,
		[]string{r.EntryKey(entry.InstanceID), r.indexKey},
		string(encoded),
		strconv.Itoa(registryEntryTTLSeconds),
		strconv.Itoa(registryEntryLimit),
		strconv.Itoa(registryIndexTTLSeconds))
}

func (r *Registry) unregister(ctx context.Context, bootID string) {
	if r.config.RedisURL == "" {
		return
	}
	unregisterCtx, cancel := context.WithTimeout(ctx, registryCommandTimeout)
	defer cancel()
	_, _ = r.runScript(unregisterCtx, registryUnregisterScript,
		[]string{r.EntryKey(r.config.InstanceID), r.indexKey}, bootID)
}

// parseRegistryEntry mirrors parseRegistryEntry: strict shape, loopback-only
// origin and a constant-time signature check.
func parseRegistryEntry(raw string, secret string) *registryEntry {
	var parsed registryEntry
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	parsed.InstanceID = strings.TrimSpace(parsed.InstanceID)
	parsed.Origin = strings.TrimSpace(parsed.Origin)
	parsed.BootID = strings.TrimSpace(parsed.BootID)
	if parsed.Version != registryEntryVersion || parsed.InstanceID == "" || parsed.BootID == "" {
		return nil
	}
	if !isLoopbackHTTPOrigin(parsed.Origin) {
		return nil
	}
	expected := registrySignature(parsed.Version, parsed.InstanceID, parsed.Origin, parsed.BootID, secret)
	if !hmac.Equal([]byte(expected), []byte(parsed.Signature)) {
		return nil
	}
	return &parsed
}

// registrySignature mirrors registrySignature (HMAC-SHA256 hex over
// "version|instanceId|origin|bootId").
func registrySignature(version int, instanceID, origin, bootID, secret string) string {
	payload := strconv.Itoa(version) + "|" + instanceID + "|" + origin + "|" + bootID
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// isLoopbackHTTPOrigin mirrors isLoopbackHttpOrigin.
func isLoopbackHTTPOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
		return false
	}
	if parsed.User != nil {
		return false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return false
	}
	return parsed.RawQuery == "" && parsed.Fragment == ""
}

// sanitizeRegistryNamespacePart mirrors sanitizeRedisNamespacePart.
func sanitizeRegistryNamespacePart(value string) string {
	normalized := strings.TrimSpace(value)
	var out strings.Builder
	for _, r := range normalized {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == ':', r == '-':
			out.WriteRune(r)
		default:
			out.WriteRune('_')
		}
	}
	result := strings.Trim(out.String(), "_")
	if result == "" {
		return "_"
	}
	return result
}

func newRegistryBootID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
