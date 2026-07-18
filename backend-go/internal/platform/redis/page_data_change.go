package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

const (
	pageDataAccountsStaticDomain  = "accounts.static"
	pageDataAccountsOptionsDomain = "accounts.options"
	pageDataRangeResetOperation   = "range_reset"
	pageDataMaxEventOwners        = 256
	pageDataEventLogTTLSeconds    = 8 * 24 * 60 * 60
)

var (
	pageDataOccurredAtPattern  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	pageDataRedisNamespaceRule = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,64}$`)
)

// PageDataChangeEvent is the Node page-data protocol v2 event shape.
type PageDataChangeEvent struct {
	EventID               string   `json:"eventId"`
	Domain                string   `json:"domain"`
	EntityID              string   `json:"entityId,omitempty"`
	Operation             string   `json:"operation"`
	FieldMask             []string `json:"fieldMask"`
	OwnerSystemAccountIDs []string `json:"ownerSystemAccountIds"`
	MembershipChanged     bool     `json:"membershipChanged"`
	OrderChanged          bool     `json:"orderChanged"`
	FilterChanged         bool     `json:"filterChanged"`
	PageChanged           bool     `json:"pageChanged"`
	OccurredAt            string   `json:"occurredAt"`
	AllScopes             bool     `json:"allScopes,omitempty"`
}

// PageDataChangePublisher publishes accounts.static range_reset events to the
// Node-compatible Redis fanout streams. Events are built separately so callers
// can retry the same event value, including its eventId.
type PageDataChangePublisher struct {
	client        *Client
	keyPrefix     string
	proposedEpoch string
	now           func() time.Time
	newEventID    func() string

	getEpoch     func(context.Context, string) (string, error)
	setEpochNX   func(context.Context, string, string, time.Duration) (bool, error)
	runPublish   func(context.Context, []string, ...interface{}) error
	marshalEvent func(PageDataChangeEvent) ([]byte, error)

	epochMu sync.Mutex
	epoch   string
}

// NewPageDataChangePublisher creates a publisher rooted at the raw Redis
// namespace. The namespace must be the configured RedisNamespace, not a
// client namespace such as RedisNamespace+":state".
func NewPageDataChangePublisher(client *Client, redisNamespace string) (*PageDataChangePublisher, error) {
	if client == nil {
		return nil, errors.New("page data redis client is required")
	}
	if redisNamespace == "" {
		return nil, errors.New("page data redis namespace is required")
	}
	if redisNamespace != strings.TrimSpace(redisNamespace) || !pageDataRedisNamespaceRule.MatchString(redisNamespace) {
		return nil, fmt.Errorf("invalid page data redis namespace %q", redisNamespace)
	}
	publisher := &PageDataChangePublisher{
		client:        client,
		keyPrefix:     "juhe-ai:" + redisNamespace + ":page-data-change",
		proposedEpoch: uuid.NewString(),
		now:           time.Now,
		newEventID:    uuid.NewString,
		marshalEvent:  func(event PageDataChangeEvent) ([]byte, error) { return json.Marshal(event) },
	}
	publisher.getEpoch = func(ctx context.Context, key string) (string, error) {
		value, err := client.client.Get(ctx, key).Result()
		if errors.Is(err, goredis.Nil) {
			return "", nil
		}
		return value, err
	}
	publisher.setEpochNX = func(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
		return client.client.SetNX(ctx, key, value, ttl).Result()
	}
	publisher.runPublish = func(ctx context.Context, keys []string, args ...interface{}) error {
		return pageDataChangePublishScript.Run(ctx, client.client, keys, args...).Err()
	}
	return publisher, nil
}

// NewAccountsStaticResetEvents creates one or more independently publishable
// events. Owner ids are trimmed, deduplicated, sorted, and split into chunks.
func (p *PageDataChangePublisher) NewAccountsStaticResetEvents(ownerIDs []string, allScopes bool) ([]PageDataChangeEvent, error) {
	if p == nil {
		return nil, errors.New("page data publisher is required")
	}
	return p.NewRangeResetEvents(pageDataAccountsStaticDomain, ownerIDs, allScopes)
}

// NewRangeResetEvents creates Node-compatible range_reset events for a supported domain.
func (p *PageDataChangePublisher) NewRangeResetEvents(domain string, ownerIDs []string, allScopes bool) ([]PageDataChangeEvent, error) {
	if p == nil {
		return nil, errors.New("page data publisher is required")
	}
	if !isSupportedPageDataDomain(domain) {
		return nil, fmt.Errorf("unsupported page data domain %q", domain)
	}
	return newRangeResetEvents(domain, ownerIDs, allScopes, p.now(), p.newEventID), nil
}

// NewAccountStaticResetEvents builds Node-compatible accounts.static reset
// events using UUID event ids and the supplied occurrence time.
func NewAccountStaticResetEvents(ownerIDs []string, allScopes bool, now time.Time) []PageDataChangeEvent {
	return newRangeResetEvents(pageDataAccountsStaticDomain, ownerIDs, allScopes, now, uuid.NewString)
}

func newRangeResetEvents(domain string, ownerIDs []string, allScopes bool, now time.Time, newEventID func() string) []PageDataChangeEvent {
	owners := normalizePageDataOwners(ownerIDs)
	if len(owners) == 0 {
		owners = []string{}
	}
	events := make([]PageDataChangeEvent, 0, (len(owners)+pageDataMaxEventOwners-1)/pageDataMaxEventOwners)
	for start := 0; start < len(owners) || (len(owners) == 0 && start == 0); start += pageDataMaxEventOwners {
		end := min(start+pageDataMaxEventOwners, len(owners))
		events = append(events, PageDataChangeEvent{
			EventID:               newEventID(),
			Domain:                domain,
			Operation:             pageDataRangeResetOperation,
			FieldMask:             []string{},
			OwnerSystemAccountIDs: append([]string(nil), owners[start:end]...),
			MembershipChanged:     true,
			OrderChanged:          true,
			FilterChanged:         true,
			PageChanged:           true,
			OccurredAt:            now.UTC().Format("2006-01-02T15:04:05.000Z"),
			AllScopes:             allScopes,
		})
		if len(owners) == 0 {
			break
		}
	}
	return events
}

// Publish publishes one already-created event. Retrying this method with the
// same event preserves eventId and is therefore deduplicated by Redis.
func (p *PageDataChangePublisher) Publish(ctx context.Context, event PageDataChangeEvent) error {
	if p == nil {
		return errors.New("page data publisher is required")
	}
	if err := validatePageDataChangeEvent(event); err != nil {
		return err
	}
	if _, err := p.ensureEpoch(ctx); err != nil {
		return fmt.Errorf("ensure page data epoch: %w", err)
	}
	rawEvent, err := p.marshalEvent(event)
	if err != nil {
		return fmt.Errorf("marshal page data event %q: %w", event.EventID, err)
	}
	for _, scope := range pageDataFanoutScopes(event) {
		keys := pageDataStreamKeys(p.keyPrefix, scope, event.Domain)
		if err := p.runPublish(
			ctx,
			keys,
			event.EventID,
			string(rawEvent),
			strconv.Itoa(pageDataMaxEventOwners),
			strconv.Itoa(pageDataEventLogTTLSeconds),
		); err != nil {
			return fmt.Errorf("publish page data event %q to %s: %w", event.EventID, scope, err)
		}
	}
	return nil
}

func (p *PageDataChangePublisher) ensureEpoch(ctx context.Context) (string, error) {
	p.epochMu.Lock()
	defer p.epochMu.Unlock()
	if p.epoch != "" {
		return p.epoch, nil
	}
	key := p.keyPrefix + ":epoch:v2"
	existing, err := p.getEpoch(ctx, key)
	if err != nil {
		return "", err
	}
	if existing != "" {
		p.epoch = existing
		return existing, nil
	}
	inserted, err := p.setEpochNX(ctx, key, p.proposedEpoch, 0)
	if err != nil {
		return "", err
	}
	if inserted {
		p.epoch = p.proposedEpoch
		return p.epoch, nil
	}
	existing, err = p.getEpoch(ctx, key)
	if err != nil {
		return "", err
	}
	if existing == "" {
		existing = p.proposedEpoch
	}
	p.epoch = existing
	return existing, nil
}

func validatePageDataChangeEvent(event PageDataChangeEvent) error {
	if strings.TrimSpace(event.EventID) == "" {
		return errors.New("page data eventId is required")
	}
	if !isSupportedPageDataDomain(event.Domain) {
		return fmt.Errorf("unsupported page data domain %q", event.Domain)
	}
	if event.Operation != pageDataRangeResetOperation {
		return fmt.Errorf("unsupported page data operation %q", event.Operation)
	}
	if len(event.FieldMask) != 0 {
		return fmt.Errorf("%s range_reset fieldMask must be empty", event.Domain)
	}
	if len(event.OwnerSystemAccountIDs) > pageDataMaxEventOwners {
		return fmt.Errorf("page data owner count exceeds %d", pageDataMaxEventOwners)
	}
	for _, ownerID := range event.OwnerSystemAccountIDs {
		if ownerID == "" || ownerID != strings.TrimSpace(ownerID) {
			return errors.New("page data owner id must be non-empty and trimmed")
		}
	}
	if !event.MembershipChanged || !event.OrderChanged || !event.FilterChanged || !event.PageChanged {
		return fmt.Errorf("%s range_reset change flags must all be true", event.Domain)
	}
	if !pageDataOccurredAtPattern.MatchString(event.OccurredAt) {
		return errors.New("page data occurredAt must use UTC milliseconds")
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", event.OccurredAt); err != nil {
		return fmt.Errorf("invalid page data occurredAt: %w", err)
	}
	return nil
}

func isSupportedPageDataDomain(domain string) bool {
	return domain == pageDataAccountsStaticDomain || domain == pageDataAccountsOptionsDomain
}

func normalizePageDataOwners(ownerIDs []string) []string {
	seen := make(map[string]struct{}, len(ownerIDs))
	owners := make([]string, 0, len(ownerIDs))
	for _, ownerID := range ownerIDs {
		ownerID = strings.TrimSpace(ownerID)
		if ownerID == "" {
			continue
		}
		if _, ok := seen[ownerID]; ok {
			continue
		}
		seen[ownerID] = struct{}{}
		owners = append(owners, ownerID)
	}
	sort.Strings(owners)
	return owners
}

func pageDataFanoutScopes(event PageDataChangeEvent) []string {
	if event.AllScopes {
		return []string{"global", "all"}
	}
	scopes := []string{"global"}
	for _, ownerID := range normalizePageDataOwners(event.OwnerSystemAccountIDs) {
		scopes = append(scopes, "owner:"+ownerID)
	}
	return scopes
}

func pageDataStreamKeys(prefix, scope, domain string) []string {
	suffix := scope + ":" + domain
	return []string{prefix + ":sequence:" + suffix, prefix + ":log:" + suffix, prefix + ":dedupe:" + suffix}
}

var pageDataChangePublishLua = `
local sequenceKey = KEYS[1]
local logKey = KEYS[2]
local dedupeKey = KEYS[3]
local eventId = ARGV[1]
local rawEvent = ARGV[2]
local maxEvents = tonumber(ARGV[3])
local ttlSeconds = tonumber(ARGV[4])
if redis.call('SISMEMBER', dedupeKey, eventId) == 1 then
  return tonumber(redis.call('GET', sequenceKey) or '0')
end
redis.call('SADD', dedupeKey, eventId)
local sequence = redis.call('INCR', sequenceKey)
redis.call('RPUSH', logKey, tostring(sequence) .. '\t' .. rawEvent)
redis.call('LTRIM', logKey, -maxEvents, -1)
redis.call('EXPIRE', logKey, ttlSeconds)
redis.call('EXPIRE', dedupeKey, ttlSeconds)
return sequence
`

var pageDataChangePublishScript = goredis.NewScript(pageDataChangePublishLua)
